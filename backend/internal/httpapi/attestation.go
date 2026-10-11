package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
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
	for _, field := range []string{bundle.Report, bundle.VCEK, bundle.ASK, bundle.ARK} {
		if decoded, err := base64.StdEncoding.DecodeString(field); err != nil || len(decoded) == 0 {
			return "", errors.New("snp_attestation fields must be base64")
		}
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
