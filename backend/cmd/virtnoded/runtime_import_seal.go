package main

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"
)

// Phones seal what they import into a runtime (a file's name and contents)
// with a key derived from the runtime's viewer key, which only the account's
// phones and this attested VM hold. The control plane relaying an import sees
// neither, and cannot slip a file of its own into the runtime.
const (
	sealedImportMagic       = "VRTIMP1\n"
	sealedImportNonceSize   = 12
	runtimeImportKeyContext = "VIRTROID-RUNTIME-IMPORT-KEY-V1"
	// sealedImportOverhead bounds what sealing adds to a file: the magic,
	// nonce and tag, and the name with its length.
	sealedImportOverhead = 512
)

var errUnsealedImport = errors.New("imports must be sealed by the phone; update the Virtroid app")

func runtimeImportKey(material []byte, runtimeID string) []byte {
	viewerKey := runtimeViewerKey(material, runtimeID)
	defer clearBytes(viewerKey)
	mac := hmac.New(sha256.New, viewerKey)
	_, _ = mac.Write([]byte(runtimeImportKeyContext))
	return mac.Sum(nil)
}

// sealedImportAAD binds an import to its runtime and kind ("file", "photo"
// or "video"), so one sealed for another runtime or route does not open.
func sealedImportAAD(runtimeID, kind string) []byte {
	return []byte("virtroid-import-v1\n" + strings.TrimSpace(runtimeID) + "\n" + kind)
}

// openSealedImport returns the name and contents a phone sealed.
func openSealedImport(key []byte, runtimeID, kind string, sealed []byte) (string, []byte, error) {
	header := len(sealedImportMagic) + sealedImportNonceSize
	if len(sealed) < header+16 || string(sealed[:len(sealedImportMagic)]) != sealedImportMagic {
		return "", nil, errUnsealedImport
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return "", nil, err
	}
	plaintext, err := aead.Open(nil, sealed[len(sealedImportMagic):header], sealed[header:], sealedImportAAD(runtimeID, kind))
	if err != nil {
		return "", nil, errors.New("import was not sealed for this runtime")
	}
	if len(plaintext) < 2 {
		return "", nil, errors.New("sealed import has no name")
	}
	nameLength := int(binary.BigEndian.Uint16(plaintext[:2]))
	if nameLength == 0 || len(plaintext) < 2+nameLength {
		return "", nil, errors.New("sealed import has no name")
	}
	name := string(plaintext[2 : 2+nameLength])
	if !utf8.ValidString(name) {
		return "", nil, errors.New("sealed import name is not UTF-8")
	}
	return name, plaintext[2+nameLength:], nil
}

// readRuntimeImport reads an import's name and contents from a request,
// opening the phone's seal. Unsealed imports are refused.
func (n *nodeAgent) readRuntimeImport(ctx context.Context, r *http.Request, runtimeID, kind string, maxBytes int64) (string, []byte, error) {
	if r.URL.Query().Get("sealed") != "1" {
		return "", nil, errUnsealedImport
	}
	sealed, err := io.ReadAll(io.LimitReader(r.Body, maxBytes+sealedImportOverhead+1))
	if err != nil {
		return "", nil, err
	}
	if int64(len(sealed)) > maxBytes+sealedImportOverhead {
		return "", nil, fmt.Errorf("sealed %s is too large", kind)
	}
	material, err := n.runtimeBlobKeyWithContext(ctx, runtimeAssignment{ID: runtimeID})
	if err != nil {
		return "", nil, fmt.Errorf("the runtime's keys are not on this node: %w", err)
	}
	defer clearBytes(material)
	key := runtimeImportKey(material, runtimeID)
	defer clearBytes(key)
	name, content, err := openSealedImport(key, runtimeID, kind, sealed)
	if err != nil {
		return "", nil, err
	}
	name, err = checkRuntimeMediaName(name)
	if err != nil {
		return "", nil, err
	}
	return name, content, nil
}
