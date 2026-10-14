package main

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha512"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// A phone seals a runtime's keys to this VM before handing them over. The
// node key the provisioner injects is also on the control plane, so phones
// that can check this VM's attestation seal to a key made here instead. It
// lives only in this process's memory, and for each handoff the chip signs a
// fresh report binding it to the phone's challenge, so neither a replayed
// report nor anything stored on the VM's disk can stand in for it.
const (
	blobKeyEnvelopeAlgorithmSealed = "P256_ECDH_HKDF_SHA256_AESGCM_V2_SEALED"
	blobSealKeyHKDFInfo            = "virtroid-blob-key-envelope-v2"
	// Report data: SHA-512(blobSealReportDomain || seal key DER || nonce).
	blobSealReportDomain  = "virtroid-blob-seal-key-v2\x00"
	blobSealNonceSize     = 32
	blobSealAttestTimeout = 30 * time.Second
)

// blobSealKey is the in-memory key phones seal runtime keys to.
type blobSealKey struct {
	private   *ecdh.PrivateKey
	publicDER []byte
}

func newBlobSealKey() (*blobSealKey, error) {
	private, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalPKIXPublicKey(private.PublicKey())
	if err != nil {
		return nil, fmt.Errorf("encode seal key: %w", err)
	}
	return &blobSealKey{private: private, publicDER: der}, nil
}

// blobSealReportData binds a seal key to a phone's challenge.
func blobSealReportData(publicDER, nonce []byte) [64]byte {
	payload := make([]byte, 0, len(blobSealReportDomain)+len(publicDER)+len(nonce))
	payload = append(payload, blobSealReportDomain...)
	payload = append(payload, publicDER...)
	payload = append(payload, nonce...)
	return sha512.Sum512(payload)
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

// sealAttestation is what a phone verifies before sealing to this VM.
type sealAttestation struct {
	snpAttestation
	SealPublicKey string `json:"seal_public_key"`
}

// handleBlobSealAttestation returns a fresh report binding the seal key to
// the challenge a phone sent through the control plane.
func (n *nodeAgent) handleBlobSealAttestation(w http.ResponseWriter, r *http.Request) {
	if !n.requireControlPlaneCallback(w, r) {
		return
	}
	var req struct {
		Nonce string `json:"nonce"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid json"})
		return
	}
	nonce, err := base64.StdEncoding.DecodeString(strings.TrimSpace(req.Nonce))
	if err != nil || len(nonce) != blobSealNonceSize {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "nonce must be 32 bytes of base64"})
		return
	}
	if n.blobSealKey == nil || n.snpSource == nil {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "this node is not attested"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), blobSealAttestTimeout)
	defer cancel()
	bundle, err := buildSNPReport(ctx, *n.snpSource, blobSealReportData(n.blobSealKey.publicDER, nonce))
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": "attestation report unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, sealAttestation{
		snpAttestation: bundle,
		SealPublicKey:  base64.StdEncoding.EncodeToString(n.blobSealKey.publicDER),
	})
}
