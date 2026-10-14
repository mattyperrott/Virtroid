package main

import (
	"bytes"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testPhrase = "abandon ability able about above absent absorb abstract absurd abuse access accident"

func TestBackupRoundTripsAtEverySize(t *testing.T) {
	for _, size := range []int{0, 1, backupChunkSize - 1, backupChunkSize, backupChunkSize + 1, 3*backupChunkSize + 17} {
		plain := make([]byte, size)
		_, _ = rand.Read(plain)
		var sealed, opened bytes.Buffer
		if err := sealBackup(&sealed, bytes.NewReader(plain), testPhrase); err != nil {
			t.Fatalf("size %d seal: %v", size, err)
		}
		if bytes.Contains(sealed.Bytes(), plain) && size > 16 {
			t.Fatalf("size %d: plaintext visible in the sealed backup", size)
		}
		if err := openBackup(&opened, bytes.NewReader(sealed.Bytes()), testPhrase); err != nil {
			t.Fatalf("size %d open: %v", size, err)
		}
		if !bytes.Equal(opened.Bytes(), plain) {
			t.Fatalf("size %d: opened backup differs", size)
		}
	}
}

func TestBackupRejectsWrongPhraseTruncationAndTampering(t *testing.T) {
	plain := bytes.Repeat([]byte("pg_dump "), 3*backupChunkSize/8)
	var sealed bytes.Buffer
	if err := sealBackup(&sealed, bytes.NewReader(plain), testPhrase); err != nil {
		t.Fatal(err)
	}
	data := sealed.Bytes()
	header := len(backupMagic) + backupSaltSize + backupPrefixSize
	chunk := backupChunkSize + 16
	cases := map[string][]byte{
		"cut after a whole chunk": data[:header+chunk],
		"cut mid-chunk":           data[:len(data)-5],
		"flipped byte":            flipByte(data, header+100),
		"chunks swapped":          append(append(append([]byte{}, data[:header]...), data[header+chunk:header+2*chunk]...), append(append([]byte{}, data[header:header+chunk]...), data[header+2*chunk:]...)...),
	}
	for name, candidate := range cases {
		if err := openBackup(&bytes.Buffer{}, bytes.NewReader(candidate), testPhrase); err == nil {
			t.Fatalf("%s: opened", name)
		}
	}
	other := strings.Replace(testPhrase, "abandon", "zoo", 1)
	if err := openBackup(&bytes.Buffer{}, bytes.NewReader(data), other); err == nil {
		t.Fatal("opened with another phrase")
	}
}

func TestRecoveryPhraseIsReadFromIndexdConfig(t *testing.T) {
	dir := t.TempDir()
	config := filepath.Join(dir, "indexd.yml")
	if err := os.WriteFile(config, []byte("autoOpenWebUI: false\nrecoveryPhrase: \"Abandon  ability able about above absent absorb abstract absurd abuse access accident\"\nadminAPI:\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	phrase, err := readRecoveryPhrase(config)
	if err != nil || phrase != testPhrase {
		t.Fatalf("phrase = %q, %v", phrase, err)
	}
	bare := filepath.Join(dir, "phrase")
	if err := os.WriteFile(bare, []byte(testPhrase+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if phrase, err := readRecoveryPhrase(bare); err != nil || phrase != testPhrase {
		t.Fatalf("bare phrase = %q, %v", phrase, err)
	}
	if err := os.WriteFile(bare, []byte("too few words\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readRecoveryPhrase(bare); err == nil {
		t.Fatal("accepted a three-word phrase")
	}
}

func flipByte(data []byte, index int) []byte {
	out := append([]byte{}, data...)
	out[index] ^= 0x01
	return out
}
