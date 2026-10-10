package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

const (
	nodeTLSKeyFile  = "node-tls.key"
	nodeTLSCertFile = "node-tls.crt"
)

// loadOrCreateNodeTLS returns the node's long-lived self-signed TLS identity and
// the base64 SHA-256 of its public key, which the control plane hands to phones
// as a pin. The key is kept on disk so the pin survives restarts.
func loadOrCreateNodeTLS(dir, nodeID string) (tls.Certificate, string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return tls.Certificate{}, "", fmt.Errorf("create node TLS directory: %w", err)
	}
	keyPath := filepath.Join(dir, nodeTLSKeyFile)
	certPath := filepath.Join(dir, nodeTLSCertFile)

	certificate, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return tls.Certificate{}, "", fmt.Errorf("load node TLS identity: %w", err)
		}
		if certificate, err = createNodeTLS(keyPath, certPath, nodeID); err != nil {
			return tls.Certificate{}, "", err
		}
	}
	leaf, err := x509.ParseCertificate(certificate.Certificate[0])
	if err != nil {
		return tls.Certificate{}, "", fmt.Errorf("parse node TLS certificate: %w", err)
	}
	certificate.Leaf = leaf
	return certificate, spkiPin(leaf), nil
}

func createNodeTLS(keyPath, certPath, nodeID string) (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("generate node TLS key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("generate node TLS serial: %w", err)
	}
	now := time.Now()
	template := x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "virtroid-node " + nodeID},
		NotBefore:    now.Add(-time.Hour),
		// Peers trust this certificate by public-key pin, not by expiry or chain.
		NotAfter:    now.AddDate(20, 0, 0),
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("create node TLS certificate: %w", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("encode node TLS key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return tls.Certificate{}, fmt.Errorf("write node TLS key: %w", err)
	}
	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		return tls.Certificate{}, fmt.Errorf("write node TLS certificate: %w", err)
	}
	return tls.X509KeyPair(certPEM, keyPEM)
}

func spkiPin(certificate *x509.Certificate) string {
	sum := sha256.Sum256(certificate.RawSubjectPublicKeyInfo)
	return base64.StdEncoding.EncodeToString(sum[:])
}

// validNodePublicAddr accepts only IP:port, so the advertised address is exactly
// what the operator allowlisted and cannot be redirected through DNS.
func validNodePublicAddr(raw string) error {
	host, port, err := net.SplitHostPort(raw)
	if err != nil {
		return fmt.Errorf("NODE_PUBLIC_ADDR must be IP:port: %w", err)
	}
	if net.ParseIP(host) == nil {
		return errors.New("NODE_PUBLIC_ADDR host must be an IP address")
	}
	if value, err := strconv.Atoi(port); err != nil || value < 1 || value > 65535 {
		return errors.New("NODE_PUBLIC_ADDR port is invalid")
	}
	return nil
}

// publicNodeHandler serves only what remote peers need: the phone's stream
// relay and the control plane's signed callbacks. Metrics and capabilities stay
// on the private listener.
func publicNodeHandler(private *http.ServeMux) http.Handler {
	public := http.NewServeMux()
	for _, pattern := range []string{
		"GET /healthz",
		"POST /api/v1/internal/viewer/prepare",
		"POST /api/v1/internal/runtimes/{id}/files",
		"POST /api/v1/internal/runtimes/{id}/photos",
		"POST /api/v1/internal/runtimes/{id}/videos",
		"POST /api/v1/internal/blob-key/verify",
		"CONNECT /api/v1/relay/{id}",
		"GET /api/v1/relay/{id}",
	} {
		public.Handle(pattern, private)
	}
	return public
}

func newPublicNodeServer(bind string, certificate tls.Certificate, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              bind,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       60 * time.Second,
		TLSConfig: &tls.Config{
			Certificates: []tls.Certificate{certificate},
			MinVersion:   tls.VersionTLS12,
		},
	}
}
