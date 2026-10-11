package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testDERCertificate(t *testing.T, name string) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: name},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return der
}

// syntheticReport is an SNP report as the secure processor would return it
// for reportData, with the given CPUID family/model (report version 3).
func syntheticReport(reportData [64]byte, version uint32, family, model byte) []byte {
	report := make([]byte, snpReportSize)
	binary.LittleEndian.PutUint32(report[0:4], version)
	copy(report[snpReportDataOffset:], reportData[:])
	copy(report[snpReportedTCBOffset:], []byte{3, 0, 0, 0, 0, 0, 8, 115})
	report[snpCPUIDFamilyOffset] = family
	report[snpCPUIDModelOffset] = model
	for i := 0; i < snpChipIDSize; i++ {
		report[snpChipIDOffset+i] = byte(i)
	}
	return report
}

type fakeKDS struct {
	server   *httptest.Server
	vcek     []byte
	chain    []byte
	products map[string]bool
	hits     atomic.Int32
}

func newFakeKDS(t *testing.T, products ...string) *fakeKDS {
	t.Helper()
	kds := &fakeKDS{
		vcek:     testDERCertificate(t, "SEV-VCEK"),
		products: map[string]bool{},
	}
	for _, product := range products {
		kds.products[product] = true
	}
	kds.chain = append(
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: testDERCertificate(t, "SEV-ASK")}),
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: testDERCertificate(t, "ARK")})...,
	)
	kds.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		kds.hits.Add(1)
		product, rest, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
		if !kds.products[product] {
			http.NotFound(w, r)
			return
		}
		if rest == "cert_chain" {
			_, _ = w.Write(kds.chain)
			return
		}
		_, _ = w.Write(kds.vcek)
	}))
	t.Cleanup(kds.server.Close)
	return kds
}

func TestBuildSNPAttestationBindsTheTLSKeyAndCachesAMDCertificates(t *testing.T) {
	kds := newFakeKDS(t, "Genoa")
	spki := []byte("node tls subject public key info")
	source := attestationSource{
		report: func(reportData [64]byte) ([]byte, error) {
			return syntheticReport(reportData, 3, 0x19, 0x11), nil
		},
		client:   kds.server.Client(),
		kdsBase:  kds.server.URL,
		cacheDir: t.TempDir(),
	}
	attestation, err := buildSNPAttestation(context.Background(), source, spki)
	if err != nil {
		t.Fatal(err)
	}
	if attestation.Product != "Genoa" || attestation.Version != 1 {
		t.Fatalf("attestation = %+v", attestation)
	}
	report, _ := base64.StdEncoding.DecodeString(attestation.Report)
	want := nodeKeyReportData(spki)
	if string(report[snpReportDataOffset:snpReportDataOffset+64]) != string(want[:]) {
		t.Fatal("report data does not bind the TLS key")
	}
	if vcek, _ := base64.StdEncoding.DecodeString(attestation.VCEK); string(vcek) != string(kds.vcek) {
		t.Fatal("VCEK is not the one AMD returned")
	}
	if attestation.ASK == "" || attestation.ARK == "" {
		t.Fatal("chain certificates missing")
	}

	hits := kds.hits.Load()
	if _, err := buildSNPAttestation(context.Background(), source, spki); err != nil {
		t.Fatal(err)
	}
	if kds.hits.Load() != hits {
		t.Fatal("AMD certificates were fetched again instead of read from the cache")
	}
}

func TestBuildSNPAttestationFindsTheGenerationOfOldReports(t *testing.T) {
	kds := newFakeKDS(t, "Milan")
	source := attestationSource{
		report: func(reportData [64]byte) ([]byte, error) {
			return syntheticReport(reportData, 2, 0, 0), nil
		},
		client:   kds.server.Client(),
		kdsBase:  kds.server.URL,
		cacheDir: t.TempDir(),
	}
	attestation, err := buildSNPAttestation(context.Background(), source, []byte("spki"))
	if err != nil || attestation.Product != "Milan" {
		t.Fatalf("attestation = %+v err=%v, want Milan", attestation, err)
	}
}

func TestVCEKURLForARealMilanReport(t *testing.T) {
	raw, err := os.ReadFile("testdata/snp_report_milan.hex")
	if err != nil {
		t.Fatal(err)
	}
	report, err := hex.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil || len(report) != snpReportSize {
		t.Fatalf("decode report: len=%d err=%v", len(report), err)
	}
	// AMD's key service answered this URL with the VCEK whose key signed the report.
	got := vcekURL(amdKDSBaseURL, "Milan", report)
	wantPrefix := amdKDSBaseURL + "/Milan/" + hex.EncodeToString(report[snpChipIDOffset:snpChipIDOffset+snpChipIDSize])
	if got != wantPrefix+"?blSPL=3&teeSPL=0&snpSPL=8&ucodeSPL=115" {
		t.Fatalf("vcekURL = %s", got)
	}
	if _, err := snpProduct(report); err == nil {
		t.Fatal("a version 2 report should not claim a CPU generation")
	}

	turin := syntheticReport([64]byte{}, 3, 0x1a, 0x02)
	copy(turin[snpReportedTCBOffset:], []byte{1, 2, 3, 4, 0, 0, 0, 9})
	if got := vcekURL("https://kds", "Turin", turin); got != "https://kds/Turin/0001020304050607?fmcSPL=1&blSPL=2&teeSPL=3&snpSPL=4&ucodeSPL=9" {
		t.Fatalf("Turin vcekURL = %s", got)
	}
	if product, err := snpProduct(turin); err != nil || product != "Turin" {
		t.Fatalf("snpProduct(Turin) = %q err=%v", product, err)
	}
}

func TestSignViewerKeyIsVerifiableWithTheTLSKey(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signature, err := signViewerKey(key, "runtime-1", "viewer-key")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := base64.StdEncoding.DecodeString(signature)
	digest := sha256.Sum256([]byte(viewerKeyDomain + "runtime-1\nviewer-key"))
	if !ecdsa.VerifyASN1(&key.PublicKey, digest[:], raw) {
		t.Fatal("viewer key signature does not verify")
	}
	other := sha256.Sum256([]byte(viewerKeyDomain + "runtime-2\nviewer-key"))
	if ecdsa.VerifyASN1(&key.PublicKey, other[:], raw) {
		t.Fatal("signature is not bound to the runtime")
	}
}
