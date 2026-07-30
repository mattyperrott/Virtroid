package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"virtroid/backend/internal/nodeauth"
)

func TestPublicKeyCommandIsDatabaseFreeAndNeverPrintsPrivateMaterial(t *testing.T) {
	privateMaterial, expectedPublicKey, expectedFingerprint := adminTestKey(t)
	t.Setenv("DATABASE_URL", "")
	t.Setenv("NODE_PRIVATE_KEY_B64", privateMaterial)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if err := run(context.Background(), []string{"public-key"}, &stdout, &stderr); err != nil {
		t.Fatalf("run public-key: %v (stderr %q)", err, stderr.String())
	}
	output := stdout.String()
	if !strings.Contains(output, "public_key_b64="+expectedPublicKey) {
		t.Fatalf("public-key output = %q, want canonical public key", output)
	}
	if !strings.Contains(output, "fingerprint_sha256="+expectedFingerprint) {
		t.Fatalf("public-key output = %q, want fingerprint", output)
	}
	if strings.Contains(output, privateMaterial) || strings.Contains(stderr.String(), privateMaterial) {
		t.Fatal("public-key command printed private key material")
	}
}

func TestPublicKeyCommandReadsExplicitEnvironmentOrFile(t *testing.T) {
	privateMaterial, expectedPublicKey, _ := adminTestKey(t)
	t.Setenv("CUSTOM_NODE_KEY", privateMaterial)
	for name, args := range map[string][]string{
		"environment": {"public-key", "--private-key-env", "CUSTOM_NODE_KEY"},
		"file":        {"public-key", "--private-key-file", writeAdminTestKey(t, privateMaterial)},
	} {
		t.Run(name, func(t *testing.T) {
			var stdout bytes.Buffer
			if err := run(context.Background(), args, &stdout, &bytes.Buffer{}); err != nil {
				t.Fatalf("run public-key: %v", err)
			}
			if !strings.Contains(stdout.String(), expectedPublicKey) {
				t.Fatalf("output = %q, want public key", stdout.String())
			}
		})
	}
}

func TestInvalidDatabaseCommandsFailBeforeDatabaseSetup(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	for name, args := range map[string][]string{
		"unknown command": {"typo"},
		"approve missing key": {
			"approve", "--node", "node-1", "--operator", "operator-1",
		},
		"approve invalid key": {
			"approve", "--node", "node-1", "--operator", "operator-1", "--public-key", "not-a-key",
		},
		"list invalid node": {"list", "--node", "bad node id"},
		"operator approve missing id": {
			"operator-approve", "--actor", "security-admin",
		},
		"operator approve missing reason": {
			"operator-approve", "--operator", "operator-1", "--actor", "security-admin", "--reason", "",
		},
		"operator revoke invalid id": {
			"operator-revoke", "--operator", "bad operator id", "--actor", "security-admin", "--reason", "compromised",
		},
		"operator revoke missing reason": {
			"operator-revoke", "--operator", "operator-1", "--actor", "security-admin",
		},
		"bootstrap invite ttl too short": {
			"bootstrap-invite", "--ttl", "59s",
		},
		"bootstrap invite ttl too long": {
			"bootstrap-invite", "--ttl", "169h",
		},
		"bootstrap invite missing actor": {
			"bootstrap-invite", "--actor", "",
		},
		"revoke missing reason": {
			"revoke", "--node", "node-1",
		},
		"rotate invalid overlap": {
			"rotate", "--node", "node-1", "--public-key", "not-a-key", "--overlap", "25h",
		},
	} {
		t.Run(name, func(t *testing.T) {
			err := run(context.Background(), args, &bytes.Buffer{}, &bytes.Buffer{})
			if err == nil {
				t.Fatal("run accepted invalid command")
			}
			if strings.Contains(err.Error(), "DATABASE_URL") || strings.Contains(err.Error(), "open database") || strings.Contains(err.Error(), "schema") {
				t.Fatalf("invalid command reached database setup: %v", err)
			}
		})
	}
}

func TestOperatorCommandsValidateBeforeDatabaseSetup(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	for name, args := range map[string][]string{
		"approve": {
			"operator-approve", "--operator", "operator-1", "--name", "Production operator",
			"--actor", "security-admin", "--reason", "identity verified",
		},
		"revoke": {
			"operator-revoke", "--operator", "operator-1", "--actor", "security-admin",
			"--reason", "credential compromise",
		},
	} {
		t.Run(name, func(t *testing.T) {
			err := run(context.Background(), args, &bytes.Buffer{}, &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), "DATABASE_URL is required") {
				t.Fatalf("valid operator command error = %v, want database requirement after validation", err)
			}
		})
	}
}

func TestBootstrapInviteCommandValidatesBeforeDatabaseSetup(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	err := run(
		context.Background(),
		[]string{"bootstrap-invite", "--ttl", "15m", "--label", "Pixel 9", "--actor", "security-admin"},
		&bytes.Buffer{},
		&bytes.Buffer{},
	)
	if err == nil || !strings.Contains(err.Error(), "DATABASE_URL is required") {
		t.Fatalf("valid bootstrap-invite command error = %v, want database requirement after validation", err)
	}
}

func TestGenerateBootstrapInviteTokenIsRandomAndMatchesReturnedDigest(t *testing.T) {
	first, firstDigest, err := generateBootstrapInviteToken()
	if err != nil {
		t.Fatalf("generate first bootstrap invitation token: %v", err)
	}
	second, _, err := generateBootstrapInviteToken()
	if err != nil {
		t.Fatalf("generate second bootstrap invitation token: %v", err)
	}
	if first == second {
		t.Fatal("generated duplicate bootstrap invitation tokens")
	}
	if len(first) != 43 {
		t.Fatalf("token length = %d, want 43 base64url characters", len(first))
	}
	if strings.ContainsAny(first, "+/=") {
		t.Fatalf("token %q is not unpadded base64url", first)
	}
	expectedDigest := sha256.Sum256([]byte(first))
	if !bytes.Equal(firstDigest, expectedDigest[:]) {
		t.Fatal("returned invitation digest does not match the plaintext token")
	}
}

func TestReadPrivateKeyRejectsAmbiguousOrInvalidSources(t *testing.T) {
	if _, _, err := readPrivateKey("NODE_PRIVATE_KEY_B64", "node.key"); err == nil {
		t.Fatal("readPrivateKey accepted two explicit sources")
	}
	if _, _, err := readPrivateKey("INVALID-NAME", ""); err == nil {
		t.Fatal("readPrivateKey accepted an invalid environment name")
	}
	t.Setenv("NODE_PRIVATE_KEY_B64", "")
	if _, _, err := readPrivateKey("", ""); err == nil {
		t.Fatal("readPrivateKey accepted an unset default environment variable")
	}
}

func TestReadPublicKeyRequiresExactlyOneSource(t *testing.T) {
	if _, err := readPublicKey("", ""); err == nil {
		t.Fatal("readPublicKey accepted no source")
	}
	if _, err := readPublicKey("inline", "key.pem"); err == nil {
		t.Fatal("readPublicKey accepted two sources")
	}
}

func TestReadPublicKeyReadsAndTrimsFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "node-public-key.pem")
	if err := os.WriteFile(path, []byte("  public-key-material\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	material, err := readPublicKey("", path)
	if err != nil {
		t.Fatalf("readPublicKey: %v", err)
	}
	if material != "public-key-material" {
		t.Fatalf("material = %q, want trimmed file contents", material)
	}
}

func TestReadPublicKeyRejectsEmptyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.pem")
	if err := os.WriteFile(path, []byte(strings.Repeat(" ", 4)), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, err := readPublicKey("", path); err == nil {
		t.Fatal("readPublicKey accepted an empty file")
	}
}

func adminTestKey(t *testing.T) (string, string, string) {
	t.Helper()
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatalf("MarshalPKCS8PrivateKey: %v", err)
	}
	publicKey, err := nodeauth.PublicKeyMaterial(&privateKey.PublicKey)
	if err != nil {
		t.Fatalf("PublicKeyMaterial: %v", err)
	}
	canonicalPublicKey, fingerprint, err := nodeauth.NormalizePublicKey(publicKey)
	if err != nil {
		t.Fatalf("NormalizePublicKey: %v", err)
	}
	return base64.StdEncoding.EncodeToString(privateDER), canonicalPublicKey, fingerprint
}

func writeAdminTestKey(t *testing.T, material string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "node-private-key")
	if err := os.WriteFile(path, []byte(material), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}
