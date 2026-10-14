package main

import (
	"crypto"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
)

// A phone seals a runtime's blob key to this VM before handing it over. The
// node key the provisioner injects is also on the control plane, so phones
// that can check this VM's attestation seal to a key made here instead: it
// lives only in this process's memory, and the attested TLS key signs it.
const (
	blobKeyEnvelopeAlgorithmSealed = "P256_ECDH_HKDF_SHA256_AESGCM_V2_SEALED"
	blobSealKeyHKDFInfo            = "virtroid-blob-key-envelope-v2"
	// Seal key signature input: blobSealKeyDomain || node id || "\n" || key.
	blobSealKeyDomain = "virtroid-blob-seal-key-v1\n"
)

// blobSealKey is the in-memory key and what the node reports for it.
type blobSealKey struct {
	private   *ecdh.PrivateKey
	PublicKey string `json:"public_key"`
	Signature string `json:"signature"`
}

// newBlobSealKey makes a fresh P-256 key and signs its public key with the
// node's attested TLS key, for nodeID.
func newBlobSealKey(signer crypto.Signer, nodeID string) (*blobSealKey, error) {
	if signer == nil {
		return nil, errors.New("no TLS key to sign the seal key")
	}
	private, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalPKIXPublicKey(private.PublicKey())
	if err != nil {
		return nil, fmt.Errorf("encode seal key: %w", err)
	}
	publicKey := base64.StdEncoding.EncodeToString(der)
	digest := sha256.Sum256([]byte(blobSealKeyDomain + nodeID + "\n" + publicKey))
	signature, err := signer.Sign(rand.Reader, digest[:], crypto.SHA256)
	if err != nil {
		return nil, fmt.Errorf("sign seal key: %w", err)
	}
	return &blobSealKey{
		private:   private,
		PublicKey: publicKey,
		Signature: base64.StdEncoding.EncodeToString(signature),
	}, nil
}

// sharedSecret is the ECDH secret with a phone's ephemeral key.
func (k *blobSealKey) sharedSecret(ephemeralPublicKey string) ([]byte, error) {
	if k == nil || k.private == nil {
		return nil, errors.New("this node has no seal key")
	}
	peer, err := parseECDHPublicKey(ephemeralPublicKey)
	if err != nil {
		return nil, err
	}
	secret, err := k.private.ECDH(peer)
	if err != nil {
		return nil, fmt.Errorf("derive sealed blob key envelope secret: %w", err)
	}
	return secret, nil
}
