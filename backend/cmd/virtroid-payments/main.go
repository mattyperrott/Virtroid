// virtroid-payments holds the hot wallet that accounts' USDT (TRC20) deposit
// addresses belong to. It finds confirmed deposits, checks each one on a
// second Tron node, and reports it to the control plane, which credits the
// account. The seed never leaves this service.
//
//	virtroid-payments seed FILE   write a new 24-word seed phrase to FILE
//	                              (never printed) and print its xpub
//	virtroid-payments public      print what virtroidd needs: the xpub of
//	                              PAYMENTS_SEED_FILE and the public key of
//	                              PAYMENTS_PRIVATE_KEY_FILE
//	virtroid-payments run         watch deposits
package main

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"virtroid/backend/internal/nodeauth"
	"virtroid/backend/internal/tron"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "seed":
		if len(os.Args) != 3 {
			usage()
		}
		err = writeSeed(os.Args[2])
	case "public":
		err = printPublic()
	case "run":
		err = run()
	default:
		usage()
	}
	if err != nil {
		log.Fatalf("virtroid-payments: %v", err)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: virtroid-payments seed FILE | public | run")
	os.Exit(2)
}

func writeSeed(path string) error {
	phrase, err := tron.NewSeedPhrase()
	if err != nil {
		return err
	}
	wallet, err := tron.OpenWallet(phrase)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create %s (an existing seed is never replaced): %w", path, err)
	}
	if _, err := fmt.Fprintln(file, phrase); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	xpub, err := wallet.XPub()
	if err != nil {
		return err
	}
	fmt.Printf("TRON_DEPOSIT_XPUB=%s\n", xpub)
	fmt.Fprintf(os.Stderr, "the seed phrase is in %s; copy it to two offline places\n", path)
	return nil
}

func openWallet() (*tron.Wallet, error) {
	path := strings.TrimSpace(os.Getenv("PAYMENTS_SEED_FILE"))
	if path == "" {
		return nil, errors.New("PAYMENTS_SEED_FILE is required")
	}
	phrase, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return tron.OpenWallet(string(phrase))
}

func signingKey() (*ecdsa.PrivateKey, string, error) {
	keyFile := strings.TrimSpace(os.Getenv("PAYMENTS_PRIVATE_KEY_FILE"))
	if keyFile == "" {
		return nil, "", errors.New("PAYMENTS_PRIVATE_KEY_FILE is required")
	}
	keyMaterial, err := os.ReadFile(keyFile)
	if err != nil {
		return nil, "", err
	}
	privateKey, publicKey, err := nodeauth.LoadPrivateKey(string(keyMaterial))
	if err != nil || privateKey == nil {
		return nil, "", errors.New("PAYMENTS_PRIVATE_KEY_FILE must hold a P-256 private key")
	}
	return privateKey, publicKey, nil
}

func printPublic() error {
	wallet, err := openWallet()
	if err != nil {
		return err
	}
	xpub, err := wallet.XPub()
	if err != nil {
		return err
	}
	_, publicKey, err := signingKey()
	if err != nil {
		return err
	}
	fmt.Printf("TRON_DEPOSIT_XPUB=%s\nPAYMENTS_PUBLIC_KEY=%s\n", xpub, publicKey)
	return nil
}

func run() error {
	wallet, err := openWallet()
	if err != nil {
		return err
	}
	privateKey, _, err := signingKey()
	if err != nil {
		return err
	}
	controlPlaneURL := strings.TrimSpace(os.Getenv("CONTROL_PLANE_URL"))
	if controlPlaneURL == "" {
		return errors.New("CONTROL_PLANE_URL is required")
	}
	apiKey := ""
	if path := strings.TrimSpace(os.Getenv("TRONGRID_API_KEY_FILE")); path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		apiKey = strings.TrimSpace(string(raw))
	}
	interval := 30 * time.Second
	if raw := strings.TrimSpace(os.Getenv("PAYMENTS_POLL_INTERVAL")); raw != "" {
		if interval, err = time.ParseDuration(raw); err != nil || interval < 5*time.Second {
			return errors.New("PAYMENTS_POLL_INTERVAL must be a duration of at least 5s")
		}
	}
	client := &http.Client{Timeout: 30 * time.Second}
	w := newWatcher(
		wallet,
		httpControlPlane{baseURL: controlPlaneURL, privateKey: privateKey, client: client},
		tronGrid{baseURL: envOrDefault("TRONGRID_URL", "https://api.trongrid.io"), apiKey: apiKey, client: client},
		tronNode{baseURL: envOrDefault("TRON_VERIFY_URL", "https://tron-rpc.publicnode.com"), client: client},
	)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	log.Printf("virtroid-payments: watching deposits every %s", interval)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := w.cycle(ctx); err != nil {
			log.Printf("payments: %v", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func envOrDefault(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
