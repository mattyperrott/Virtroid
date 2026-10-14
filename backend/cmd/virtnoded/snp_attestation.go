package main

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// A node inside an AMD SEV-SNP VM proves its TLS key lives in that VM: it asks
// the AMD secure processor for a report whose REPORT_DATA is a hash of the key,
// and ships the report with AMD's certificates for the chip. Phones verify the
// bundle themselves, so the control plane can relay it without being trusted.

const (
	snpReportSize        = 1184
	snpReportDataOffset  = 0x50
	snpReportedTCBOffset = 0x180
	snpCPUIDFamilyOffset = 0x188
	snpCPUIDModelOffset  = 0x189
	snpChipIDOffset      = 0x1a0
	snpChipIDSize        = 64

	// REPORT_DATA = SHA-512(nodeTLSKeyDomain || DER SubjectPublicKeyInfo).
	nodeTLSKeyDomain = "virtroid-node-tls-key-v1\x00"
	// Viewer key signature input: viewerKeyDomain || runtime id || "\n" || key.
	viewerKeyDomain = "virtroid-viewer-key-v1\n"

	defaultTSMReportRoot = "/sys/kernel/config/tsm/report"
	amdKDSBaseURL        = "https://kdsintf.amd.com/vcek/v1"
	maxAMDCertBytes      = 64 << 10
)

// snpAttestation is the bundle a phone needs to verify a node offline.
type snpAttestation struct {
	Version int    `json:"version"`
	Product string `json:"product"`
	Report  string `json:"report"`
	// TLSSPKI is the DER public key the report binds; phones match its
	// SHA-256 against the node's TLS pin.
	TLSSPKI string `json:"tls_spki"`
	VCEK    string `json:"vcek"`
	ASK     string `json:"ask"`
	ARK     string `json:"ark"`
}

func nodeKeyReportData(spkiDER []byte) [64]byte {
	return sha512.Sum512(append([]byte(nodeTLSKeyDomain), spkiDER...))
}

// signViewerKey binds a runtime's viewer encryption key to the node's
// attested TLS key, so a phone can trust the viewer key on any path.
func signViewerKey(signer crypto.Signer, runtimeID, viewerPublicKey string) (string, error) {
	digest := sha256.Sum256([]byte(viewerKeyDomain + runtimeID + "\n" + viewerPublicKey))
	signature, err := signer.Sign(rand.Reader, digest[:], crypto.SHA256)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(signature), nil
}

// tsmReport asks the kernel's configfs-tsm interface for an SEV-SNP report.
func tsmReport(root string, reportData [64]byte) ([]byte, error) {
	provider, err := os.MkdirTemp(root, "virtroid-")
	if err != nil {
		return nil, fmt.Errorf("create TSM report entry: %w", err)
	}
	defer os.Remove(provider)
	if name, err := os.ReadFile(filepath.Join(provider, "provider")); err == nil &&
		strings.TrimSpace(string(name)) != "sev_guest" {
		return nil, fmt.Errorf("TSM provider is %q, not sev_guest", strings.TrimSpace(string(name)))
	}
	if err := os.WriteFile(filepath.Join(provider, "inblob"), reportData[:], 0); err != nil {
		return nil, fmt.Errorf("write TSM report data: %w", err)
	}
	before, err := os.ReadFile(filepath.Join(provider, "generation"))
	if err != nil {
		return nil, fmt.Errorf("read TSM generation: %w", err)
	}
	report, err := os.ReadFile(filepath.Join(provider, "outblob"))
	if err != nil {
		return nil, fmt.Errorf("read SNP report: %w", err)
	}
	after, err := os.ReadFile(filepath.Join(provider, "generation"))
	if err != nil || string(after) != string(before) {
		return nil, errors.New("TSM report entry changed while it was read")
	}
	if len(report) < snpReportSize {
		return nil, fmt.Errorf("SNP report is %d bytes, want %d", len(report), snpReportSize)
	}
	report = report[:snpReportSize]
	if string(report[snpReportDataOffset:snpReportDataOffset+64]) != string(reportData[:]) {
		return nil, errors.New("SNP report does not carry the requested report data")
	}
	return report, nil
}

// snpProduct names the EPYC generation that signed a report, from the CPUID
// fields of report version 3 and later.
func snpProduct(report []byte) (string, error) {
	if binary.LittleEndian.Uint32(report[0:4]) < 3 {
		return "", errors.New("SNP report predates CPUID fields; set NODE_SNP_PRODUCT")
	}
	family, model := report[snpCPUIDFamilyOffset], report[snpCPUIDModelOffset]
	switch {
	case family == 0x19 && model < 0x10:
		return "Milan", nil
	case family == 0x19 && model < 0x20:
		return "Genoa", nil
	case family == 0x1a:
		return "Turin", nil
	}
	return "", fmt.Errorf("unknown SNP CPU family 0x%x model 0x%x", family, model)
}

// vcekURL is AMD's Key Distribution Service address of the VCEK for the
// chip and TCB a report was signed at. Turin keys use the first 8 bytes of
// the chip id and add the FMC level.
func vcekURL(kdsBase, product string, report []byte) string {
	chipID := report[snpChipIDOffset : snpChipIDOffset+snpChipIDSize]
	tcb := report[snpReportedTCBOffset : snpReportedTCBOffset+8]
	if product == "Turin" {
		return fmt.Sprintf("%s/Turin/%s?fmcSPL=%d&blSPL=%d&teeSPL=%d&snpSPL=%d&ucodeSPL=%d",
			kdsBase, hex.EncodeToString(chipID[:8]), tcb[0], tcb[1], tcb[2], tcb[3], tcb[7])
	}
	return fmt.Sprintf("%s/%s/%s?blSPL=%d&teeSPL=%d&snpSPL=%d&ucodeSPL=%d",
		kdsBase, product, hex.EncodeToString(chipID), tcb[0], tcb[1], tcb[6], tcb[7])
}

func fetchAMD(ctx context.Context, client *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxAMDCertBytes))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("AMD KDS %s: status %d", url, resp.StatusCode)
	}
	return body, nil
}

// cachedAMD reads an AMD certificate from cacheDir, fetching it once. VCEKs
// are fixed per chip and TCB, so the cache never goes stale.
func cachedAMD(ctx context.Context, client *http.Client, cacheDir, name, url string) ([]byte, error) {
	path := filepath.Join(cacheDir, name)
	if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
		return data, nil
	}
	data, err := fetchAMD(ctx, client, url)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(cacheDir, 0o700); err == nil {
		_ = writeFileAndSync(path, data, 0o600)
	}
	return data, nil
}

// attestationSource is where buildSNPAttestation gets reports and AMD
// certificates; tests replace both.
type attestationSource struct {
	report   func(reportData [64]byte) ([]byte, error)
	client   *http.Client
	kdsBase  string
	cacheDir string
	product  string
}

// buildSNPAttestation produces the bundle for the node's TLS key.
func buildSNPAttestation(ctx context.Context, source attestationSource, spkiDER []byte) (snpAttestation, error) {
	bundle, err := buildSNPReport(ctx, source, nodeKeyReportData(spkiDER))
	if err != nil {
		return snpAttestation{}, err
	}
	bundle.TLSSPKI = base64.StdEncoding.EncodeToString(spkiDER)
	return bundle, nil
}

// buildSNPReport asks the chip for a report over reportData and adds the AMD
// certificates that verify it.
func buildSNPReport(ctx context.Context, source attestationSource, reportData [64]byte) (snpAttestation, error) {
	report, err := source.report(reportData)
	if err != nil {
		return snpAttestation{}, err
	}
	candidates := []string{source.product}
	if source.product == "" {
		if product, err := snpProduct(report); err == nil {
			candidates = []string{product}
		} else {
			// Reports before version 3 do not name the CPU generation; AMD's
			// key service only knows the chip under its real one.
			candidates = []string{"Genoa", "Milan", "Turin"}
		}
	}
	var product string
	var vcek []byte
	for _, candidate := range candidates {
		url := vcekURL(source.kdsBase, candidate, report)
		vcekName := fmt.Sprintf("vcek-%x.der", sha256.Sum256([]byte(url)))
		vcek, err = cachedAMD(ctx, source.client, source.cacheDir, vcekName, url)
		if err == nil {
			product = candidate
			break
		}
	}
	if err != nil {
		return snpAttestation{}, fmt.Errorf("fetch VCEK: %w", err)
	}
	if _, err := x509.ParseCertificate(vcek); err != nil {
		return snpAttestation{}, fmt.Errorf("AMD KDS returned an invalid VCEK: %w", err)
	}
	chainPEM, err := cachedAMD(ctx, source.client, source.cacheDir, "cert-chain-"+strings.ToLower(product)+".pem",
		source.kdsBase+"/"+product+"/cert_chain")
	if err != nil {
		return snpAttestation{}, fmt.Errorf("fetch AMD certificate chain: %w", err)
	}
	var chain [][]byte
	for rest := chainPEM; ; {
		var block *pem.Block
		if block, rest = pem.Decode(rest); block == nil {
			break
		}
		chain = append(chain, block.Bytes)
	}
	if len(chain) != 2 {
		return snpAttestation{}, fmt.Errorf("AMD certificate chain has %d certificates, want ASK and ARK", len(chain))
	}
	encode := base64.StdEncoding.EncodeToString
	return snpAttestation{
		Version: 1,
		Product: product,
		Report:  encode(report),
		VCEK:    encode(vcek),
		ASK:     encode(chain[0]),
		ARK:     encode(chain[1]),
	}, nil
}

// attestNode builds the node's attestation according to NODE_SNP_ATTESTATION:
// "off", "auto" (attest when the SNP report interface exists), or "required".
func (n *nodeAgent) attestNode(ctx context.Context, spkiDER []byte) {
	mode := strings.ToLower(strings.TrimSpace(n.cfg.SNPAttestation))
	if mode == "off" {
		return
	}
	root := n.cfg.SNPReportRoot
	if root == "" {
		root = defaultTSMReportRoot
	}
	if _, err := os.Stat(root); err != nil {
		if mode == "required" {
			log.Fatalf("SNP attestation required but %s is unavailable: %v", root, err)
		}
		return
	}
	source := attestationSource{
		report:   func(reportData [64]byte) ([]byte, error) { return tsmReport(root, reportData) },
		client:   &http.Client{Timeout: 30 * time.Second},
		kdsBase:  amdKDSBaseURL,
		cacheDir: filepath.Join(n.cfg.TLSDir, "amd"),
		product:  n.cfg.SNPProduct,
	}
	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		attestation, err := buildSNPAttestation(ctx, source, spkiDER)
		if err == nil {
			n.snpAttestation = &attestation
			n.snpSource = &source
			log.Printf("virtnoded SNP attestation ready: product=%s", attestation.Product)
			return
		}
		lastErr = err
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Duration(attempt+1) * 5 * time.Second):
		}
	}
	if mode == "required" {
		log.Fatalf("SNP attestation required but unavailable: %v", lastErr)
	}
	log.Printf("virtnoded SNP attestation unavailable: %v", lastErr)
}
