// virtroid-measurements manages the signed list of runtime VM launch
// measurements that phones accept. Keep the private key offline: generate and
// sign on your own machine, then copy only the signed list to the server.
//
//	virtroid-measurements keygen --out measurement-signing.key
//	virtroid-measurements sign --key measurement-signing.key --serial 1 \
//	    --measurement <96 hex> [--measurement ...] \
//	    --min-tcb Genoa=bootloader=12,tee=0,snp=28,microcode=88 \
//	    [--valid-for 60d] --out snp-measurements.json
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
	"strconv"
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
	validFor := flags.String("valid-for", "60d", "how long phones accept the list (e.g. 60d, 720h); sign a new one before then")
	var values, minTCBs repeated
	flags.Var(&values, "measurement", "approved launch measurement (96 hex); repeatable")
	flags.Var(&minTCBs, "min-tcb", "Product=bootloader=N,tee=N,snp=N,microcode=N[,fmc=N]: the oldest host firmware phones accept; repeatable")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *keyFile == "" || *out == "" {
		return errors.New("sign requires --key and --out")
	}
	lifetime, err := parseLifetime(*validFor)
	if err != nil {
		return err
	}
	minTCB := map[string]measurements.TCB{}
	for _, entry := range minTCBs {
		product, levels, ok := strings.Cut(entry, "=")
		if !ok {
			return fmt.Errorf("--min-tcb %q needs Product=levels", entry)
		}
		tcb, err := measurements.ParseTCB(levels)
		if err != nil {
			return fmt.Errorf("--min-tcb %s: %w", product, err)
		}
		minTCB[strings.TrimSpace(product)] = tcb
	}
	if len(minTCB) == 0 {
		return errors.New("sign requires --min-tcb for each AMD product the VMs run on")
	}
	material, err := os.ReadFile(*keyFile)
	if err != nil {
		return err
	}
	key, _, err := nodeauth.LoadPrivateKey(string(material))
	if err != nil || key == nil {
		return errors.New("--key must hold a P-256 private key from keygen")
	}
	now := time.Now()
	signed, err := measurements.Sign(key, *serial, values, now, measurements.SignOptions{
		ExpiresAt: now.Add(lifetime),
		MinTCB:    minTCB,
	})
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
	_, err = fmt.Fprintf(stdout, "signed serial %d with %d measurement(s), valid until %s, to %s\n",
		*serial, len(values), now.Add(lifetime).UTC().Format(time.RFC3339), *out)
	return err
}

// parseLifetime reads a duration that may be given in days ("60d").
func parseLifetime(raw string) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	var lifetime time.Duration
	if days, ok := strings.CutSuffix(raw, "d"); ok {
		count, err := strconv.Atoi(days)
		if err != nil {
			return 0, fmt.Errorf("invalid --valid-for %q", raw)
		}
		lifetime = time.Duration(count) * 24 * time.Hour
	} else {
		parsed, err := time.ParseDuration(raw)
		if err != nil {
			return 0, fmt.Errorf("invalid --valid-for %q", raw)
		}
		lifetime = parsed
	}
	if lifetime < time.Hour || lifetime > 180*24*time.Hour {
		return 0, errors.New("--valid-for must be between 1 hour and 180 days")
	}
	return lifetime, nil
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
	fmt.Fprintf(stdout, "serial %d issued %s expires %s\n", list.Serial, list.IssuedAt, valueOr(list.ExpiresAt, "never"))
	for _, product := range measurements.Products {
		if tcb, ok := list.MinTCB[product]; ok {
			fmt.Fprintf(stdout, "minimum %s firmware: bootloader=%d tee=%d snp=%d microcode=%d fmc=%d\n",
				product, tcb.Bootloader, tcb.TEE, tcb.SNP, tcb.Microcode, tcb.FMC)
		}
	}
	for _, measurement := range list.Measurements {
		fmt.Fprintln(stdout, measurement)
	}
	return nil
}

func valueOr(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
