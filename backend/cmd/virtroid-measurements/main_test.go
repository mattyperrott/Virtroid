package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func TestKeygenSignVerify(t *testing.T) {
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "signing.key")
	listFile := filepath.Join(dir, "list.json")
	var out bytes.Buffer
	if err := run([]string{"keygen", "--out", keyFile}, &out, &out); err != nil {
		t.Fatal(err)
	}
	public := strings.TrimSpace(strings.SplitN(strings.SplitN(out.String(), "public key:", 2)[1], "\n", 2)[0])
	if err := run([]string{"keygen", "--out", keyFile}, &out, &out); err == nil {
		t.Fatal("keygen overwrote an existing key")
	}
	measurement := strings.Repeat("0f", 48)
	if err := run([]string{"sign", "--key", keyFile, "--serial", "3", "--measurement", measurement, "--min-tcb", "Genoa=bootloader=12,tee=0,snp=28,microcode=88", "--out", listFile}, &out, &out); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := run([]string{"verify", "--public-key", public, listFile}, &out, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "serial 3") || !strings.Contains(out.String(), measurement) {
		t.Fatalf("verify output = %q", out.String())
	}
}
