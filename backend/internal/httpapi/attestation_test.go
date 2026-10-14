package httpapi

import (
	"encoding/json"
	"testing"
)

func TestNodeBlobSealKeyJSON(t *testing.T) {
	valid := json.RawMessage(`{"public_key":"cHVibGlj","signature":"c2ln"}`)
	if got, err := nodeBlobSealKeyJSON(valid, `{"version":1}`); err != nil || got != string(valid) {
		t.Fatalf("valid seal key: %q err=%v", got, err)
	}
	if got, err := nodeBlobSealKeyJSON(nil, `{"version":1}`); err != nil || got != "" {
		t.Fatalf("absent seal key: %q err=%v", got, err)
	}
	for name, check := range map[string]struct {
		raw         string
		attestation string
	}{
		"without an attestation": {`{"public_key":"cHVibGlj","signature":"c2ln"}`, ""},
		"with an unknown field":  {`{"public_key":"cHVibGlj","signature":"c2ln","extra":1}`, `{"version":1}`},
		"not base64":             {`{"public_key":"not base64!","signature":"c2ln"}`, `{"version":1}`},
		"empty signature":        {`{"public_key":"cHVibGlj","signature":""}`, `{"version":1}`},
	} {
		if _, err := nodeBlobSealKeyJSON(json.RawMessage(check.raw), check.attestation); err == nil {
			t.Fatalf("seal key %s was accepted", name)
		}
	}
}
