// virtroid-measurements manages the signed list of runtime VM launch
// measurements that phones accept. Keep the private key offline: generate and
// sign on your own machine, then copy only the signed list to the server.
//
//	virtroid-measurements keygen --out measurement-signing.key
//	virtroid-measurements sign --key measurement-signing.key --serial 1 \
//	    --measurement <96 hex> [--measurement ...] --out snp-measurements.json
//	virtroid-measurements verify --public-key <base64> snp-measurements.json
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"virtroid/backend/internal/measurements"
	"virtroid/backend/internal/nodeauth"
)

type repeated []string

func (r *repeated) String() string     { return strings.Join(*r, ",") }
func (r *repeated) Set(v string) error { *r = append(*r, v); return nil }

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "virtroid-measurements:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errors.New("command is required: keygen, sign, or verify")
	}
	switch args[0] {
	case "keygen":
		return keygen(args[1:], stdout, stderr)
	case "sign":
		return sign(args[1:], stdout, stderr)
	case "verify":
		return verify(args[1:], stdout, stderr)
	}
	return fmt.Errorf("unknown command %q", args[0])
}

func keygen(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("keygen", flag.ContinueOnError)
	flags.SetOutput(stderr)
	out := flags.String("out", "", "file for the new private key (created, mode 0600)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *out == "" {
		return errors.New("keygen requires --out")
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(*out, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.WriteString(base64.StdEncoding.EncodeToString(der) + "\n"); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	public, err := nodeauth.PublicKeyMaterial(&key.PublicKey)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "private key: %s (keep it offline)\npublic key:  %s\n", *out, public)
	return err
}

func sign(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("sign", flag.ContinueOnError)
	flags.SetOutput(stderr)
	keyFile := flags.String("key", "", "private key file from keygen")
	serial := flags.Int64("serial", 0, "list serial; must be higher than the previous list's")
	out := flags.String("out", "", "file for the signed list")
	var values repeated
	flags.Var(&values, "measurement", "approved launch measurement (96 hex); repeatable")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *keyFile == "" || *out == "" {
		return errors.New("sign requires --key and --out")
	}
	material, err := os.ReadFile(*keyFile)
	if err != nil {
		return err
	}
	key, _, err := nodeauth.LoadPrivateKey(string(material))
	if err != nil || key == nil {
		return errors.New("--key must hold a P-256 private key from keygen")
	}
	signed, err := measurements.Sign(key, *serial, values, time.Now())
	if err != nil {
		return err
	}
	document, err := json.MarshalIndent(signed, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(*out, append(document, '\n'), 0o644); err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "signed serial %d with %d measurement(s) to %s\n", *serial, len(values), *out)
	return err
}

func verify(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("verify", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var publicKeys repeated
	flags.Var(&publicKeys, "public-key", "trusted public key (base64); repeatable")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 1 || len(publicKeys) == 0 {
		return errors.New("verify requires --public-key and one list file")
	}
	var trusted []*ecdsa.PublicKey
	for _, material := range publicKeys {
		key, err := nodeauth.ParsePublicKey(material)
		if err != nil {
			return fmt.Errorf("--public-key: %w", err)
		}
		trusted = append(trusted, key)
	}
	data, err := os.ReadFile(flags.Arg(0))
	if err != nil {
		return err
	}
	var signed measurements.SignedList
	if err := json.Unmarshal(data, &signed); err != nil {
		return errors.New("list file is not a signed measurement list")
	}
	list, err := measurements.Verify(signed, trusted)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "serial %d issued %s\n", list.Serial, list.IssuedAt)
	for _, measurement := range list.Measurements {
		fmt.Fprintln(stdout, measurement)
	}
	return nil
}
