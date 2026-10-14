package main

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"encoding/hex"
	"net/http/httptest"
	"testing"
)

// sealImport is what the phone does (RuntimeImportSeal.seal).
func sealImport(t *testing.T, key []byte, runtimeID, kind, name string, content, nonce []byte) []byte {
	t.Helper()
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	plaintext := binary.BigEndian.AppendUint16(nil, uint16(len(name)))
	plaintext = append(plaintext, name...)
	plaintext = append(plaintext, content...)
	sealed := append([]byte(sealedImportMagic), nonce...)
	return aead.Seal(sealed, nonce, plaintext, sealedImportAAD(runtimeID, kind))
}

func TestSealedImportOpensOnlyForItsRuntimeAndKind(t *testing.T) {
	material := bytes.Repeat([]byte{0x42}, 32)
	key := runtimeImportKey(material, "rt-import")
	sealed := sealImport(t, key, "rt-import", "photo", "IMG_1.jpg", []byte("jpeg bytes"), bytes.Repeat([]byte{1}, 12))

	name, content, err := openSealedImport(key, "rt-import", "photo", sealed)
	if err != nil || name != "IMG_1.jpg" || string(content) != "jpeg bytes" {
		t.Fatalf("open = %q %q %v", name, content, err)
	}
	if _, _, err := openSealedImport(key, "rt-other", "photo", sealed); err == nil {
		t.Fatal("opened an import sealed for another runtime")
	}
	if _, _, err := openSealedImport(key, "rt-import", "file", sealed); err == nil {
		t.Fatal("opened a photo as a file import")
	}
	if _, _, err := openSealedImport(runtimeImportKey(material, "rt-other"), "rt-import", "photo", sealed); err == nil {
		t.Fatal("opened an import with another runtime's key")
	}
	if _, _, err := openSealedImport(key, "rt-import", "photo", []byte("\xff\xd8 plain jpeg")); err != errUnsealedImport {
		t.Fatalf("plain body error = %v", err)
	}
}

func TestUnsealedImportRequestIsRefused(t *testing.T) {
	node := &nodeAgent{}
	request := httptest.NewRequest("POST", "/api/v1/internal/runtimes/rt-import/photos?name=SU1HLmpwZw", bytes.NewReader([]byte("\xff\xd8")))
	if _, _, err := node.readRuntimeImport(request.Context(), request, "rt-import", "photo", maxRuntimePhotoImportBytes); err != errUnsealedImport {
		t.Fatalf("readRuntimeImport error = %v, want errUnsealedImport", err)
	}
}

// The phone's RuntimeImportSealTest seals the same import to the same bytes.
func TestSealedImportVector(t *testing.T) {
	key := runtimeImportKey(bytes.Repeat([]byte{0x42}, 32), "rt-viewer-vector")
	if got := hex.EncodeToString(key); got != "df0e102409cd96117bfbb35e5c07ff8706656cfb97644b380d3c5b9e60cfc533" {
		t.Fatalf("import key = %s", got)
	}
	sealed := sealImport(t, key, "rt-viewer-vector", "file", "notes.txt", []byte("hello"), bytes.Repeat([]byte{0x07}, 12))
	if got := hex.EncodeToString(sealed); got != "565254494d50310a070707070707070707070707df7a46737b2d5b626521866a3af4e4067fca1a588d7943126752d1fda832b98d" {
		t.Fatalf("sealed = %s", got)
	}
}
