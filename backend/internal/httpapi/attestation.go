package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"os"

	"virtroid/backend/internal/measurements"
)

const maxNodeAttestationBytes = 32 << 10

// nodeAttestationJSON checks the shape of the SEV-SNP attestation bundle a
// node sends with its heartbeat and returns it compacted, or "" when absent.
// The control plane only relays it: phones verify AMD's signatures and the
// binding to the node's TLS key themselves.
func nodeAttestationJSON(raw json.RawMessage, tlsPin string) (string, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return "", nil
	}
	if tlsPin == "" {
		return "", errors.New("snp_attestation requires a public TLS endpoint")
	}
	if len(raw) > maxNodeAttestationBytes {
		return "", errors.New("snp_attestation is too large")
	}
	var bundle struct {
		Version int    `json:"version"`
		Product string `json:"product"`
		Report  string `json:"report"`
		TLSSPKI string `json:"tls_spki"`
		VCEK    string `json:"vcek"`
		ASK     string `json:"ask"`
		ARK     string `json:"ark"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&bundle); err != nil {
		return "", errors.New("snp_attestation is not a valid attestation bundle")
	}
	if bundle.Version != 1 {
		return "", errors.New("unsupported snp_attestation version")
	}
	switch bundle.Product {
	case "Milan", "Genoa", "Turin":
	default:
		return "", errors.New("unknown snp_attestation product")
	}
	for _, field := range []string{bundle.Report, bundle.TLSSPKI, bundle.VCEK, bundle.ASK, bundle.ARK} {
		if decoded, err := base64.StdEncoding.DecodeString(field); err != nil || len(decoded) == 0 {
			return "", errors.New("snp_attestation fields must be base64")
		}
	}
	spki, _ := base64.StdEncoding.DecodeString(bundle.TLSSPKI)
	digest := sha256.Sum256(spki)
	if base64.StdEncoding.EncodeToString(digest[:]) != tlsPin {
		return "", errors.New("snp_attestation is for a different TLS key than the node's pin")
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		return "", err
	}
	return compact.String(), nil
}

// viewerConfidentiality returns the session fields a phone uses to verify the
// runtime's VM: the node's attestation, its signature over the viewer key,
// and whether the runtime is a provisioned confidential VM, in which case
// the phone refuses to connect without a valid attestation.
func (a *API) viewerConfidentiality(ctx context.Context, hostID string, viewer preparedViewer) (map[string]any, error) {
	fields := map[string]any{}
	attestation, err := a.store.HostSNPAttestation(ctx, hostID)
	if err != nil {
		return nil, err
	}
	if attestation != "" {
		fields["node_attestation"] = json.RawMessage(attestation)
	}
	if viewer.Signature != "" {
		fields["viewer_key_signature"] = viewer.Signature
	}
	if _, provisioned, err := a.store.LiveProvisionedVM(ctx, hostID); err != nil {
		return nil, err
	} else if provisioned {
		fields["confidential_runtime"] = true
	}
	return fields, nil
}

// snpMeasurements serves the signed list of approved runtime VM launch
// measurements. It is signed offline; phones verify it with a key built into
// the app, so serving it needs no trust in this server.
func (a *API) snpMeasurements(w http.ResponseWriter, r *http.Request) {
	if a.cfg.SNPMeasurementsPath == "" {
		writeAPIError(w, http.StatusNotFound, "snp_measurements_unavailable", "no measurement list is published")
		return
	}
	data, err := os.ReadFile(a.cfg.SNPMeasurementsPath)
	var signed measurements.SignedList
	if err != nil || len(bytes.TrimSpace(data)) == 0 || json.Unmarshal(data, &signed) != nil ||
		signed.Payload == "" || signed.Signature == "" {
		writeAPIError(w, http.StatusNotFound, "snp_measurements_unavailable", "no measurement list is published")
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=300")
	writeJSON(w, http.StatusOK, signed)
}
