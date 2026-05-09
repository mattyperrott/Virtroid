package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestServerHandshakeEncryptedRoundTrip(t *testing.T) {
	serverSide, clientSide := net.Pipe()
	defer serverSide.Close()
	defer clientSide.Close()

	serverErr := make(chan error, 1)
	go func() {
		serverPrivate, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			serverErr <- err
			return
		}
		conn, err := serverHandshake(serverSide, serverPrivate)
		if err != nil {
			serverErr <- err
			return
		}
		defer conn.Close()

		incoming := make([]byte, len("client-to-runtime"))
		if _, err := io.ReadFull(conn, incoming); err != nil {
			serverErr <- err
			return
		}
		if string(incoming) != "client-to-runtime" {
			serverErr <- errors.New("server received wrong plaintext")
			return
		}
		if _, err := conn.Write([]byte("runtime-to-client")); err != nil {
			serverErr <- err
			return
		}
		serverErr <- nil
	}()

	clientConn, err := clientHandshake(clientSide)
	if err != nil {
		t.Fatalf("client handshake failed: %v", err)
	}
	defer clientConn.Close()

	if _, err := clientConn.Write([]byte("client-to-runtime")); err != nil {
		t.Fatalf("client write failed: %v", err)
	}
	incoming := make([]byte, len("runtime-to-client"))
	if _, err := io.ReadFull(clientConn, incoming); err != nil {
		t.Fatalf("client read failed: %v", err)
	}
	if string(incoming) != "runtime-to-client" {
		t.Fatalf("client received wrong plaintext: %q", string(incoming))
	}
	if err := <-serverErr; err != nil {
		t.Fatalf("server side failed: %v", err)
	}
}

func TestWritePublicKeyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "viewer-public-key")
	publicKeyDER := []byte("public-key")

	if err := writePublicKeyFile(path, publicKeyDER); err != nil {
		t.Fatalf("writePublicKeyFile returned error: %v", err)
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read public key file: %v", err)
	}
	decoded, err := base64.StdEncoding.DecodeString(string(bytes.TrimSpace(payload)))
	if err != nil {
		t.Fatalf("decode public key: %v", err)
	}
	if !bytes.Equal(decoded, publicKeyDER) {
		t.Fatalf("decoded public key = %q, want %q", decoded, publicKeyDER)
	}
}

func clientHandshake(conn net.Conn) (*encryptedConn, error) {
	clientPrivate, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	clientPublicDER, err := x509.MarshalPKIXPublicKey(&clientPrivate.PublicKey)
	if err != nil {
		return nil, err
	}
	if err := writeHandshake(conn, clientPublicDER); err != nil {
		return nil, err
	}

	serverPublicDER, err := readHandshake(conn)
	if err != nil {
		return nil, err
	}
	parsedServerPublic, err := x509.ParsePKIXPublicKey(serverPublicDER)
	if err != nil {
		return nil, err
	}
	serverPublic, ok := parsedServerPublic.(*ecdsa.PublicKey)
	if !ok {
		return nil, errors.New("server public key is not ECDSA")
	}

	sharedX, _ := serverPublic.Curve.ScalarMult(serverPublic.X, serverPublic.Y, clientPrivate.D.Bytes())
	if sharedX == nil {
		return nil, errors.New("derive shared secret")
	}
	sharedSecret := leftPad(sharedX.Bytes(), 32)
	keys := deriveKeys(sharedSecret, append(bytes.Clone(clientPublicDER), serverPublicDER...))
	return newEncryptedConn(conn, keys.serverToClient, keys.clientToServer), nil
}
