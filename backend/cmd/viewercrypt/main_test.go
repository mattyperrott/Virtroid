package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
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

func TestServerHandshakeV2RequiresTheViewerKey(t *testing.T) {
	authKey := bytes.Repeat([]byte{7}, viewerAuthKeySize)
	for _, tc := range []struct {
		name      string
		clientKey []byte
		wantOK    bool
	}{
		{"right key", authKey, true},
		{"wrong key", bytes.Repeat([]byte{8}, viewerAuthKeySize), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			serverSide, clientSide := net.Pipe()
			defer serverSide.Close()
			defer clientSide.Close()
			serverPrivate, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			serverErr := make(chan error, 1)
			go func() {
				conn, err := serverHandshakeV2(serverSide, serverPrivate, authKey)
				if err != nil {
					_ = serverSide.Close()
					serverErr <- err
					return
				}
				defer conn.Close()
				incoming := make([]byte, len("input"))
				if _, err := io.ReadFull(conn, incoming); err != nil {
					serverErr <- err
					return
				}
				_, err = conn.Write([]byte("frame"))
				serverErr <- err
			}()

			clientConn, clientErr := clientHandshakeV2(clientSide, tc.clientKey)
			if !tc.wantOK {
				if clientErr == nil {
					t.Fatal("client finished a handshake with the wrong viewer key")
				}
				if err := <-serverErr; err == nil || err.Error() != "viewer did not prove the runtime's viewer key" {
					t.Fatalf("server error = %v", err)
				}
				return
			}
			if clientErr != nil {
				t.Fatalf("client handshake: %v", clientErr)
			}
			if _, err := clientConn.Write([]byte("input")); err != nil {
				t.Fatal(err)
			}
			incoming := make([]byte, len("frame"))
			if _, err := io.ReadFull(clientConn, incoming); err != nil || string(incoming) != "frame" {
				t.Fatalf("client read %q, %v", incoming, err)
			}
			if err := <-serverErr; err != nil {
				t.Fatalf("server: %v", err)
			}
		})
	}
}

func TestServerHandshakeV2RefusesVersionOneViewers(t *testing.T) {
	serverSide, clientSide := net.Pipe()
	defer serverSide.Close()
	defer clientSide.Close()
	serverPrivate, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serverErr := make(chan error, 1)
	go func() {
		_, err := serverHandshakeV2(serverSide, serverPrivate, bytes.Repeat([]byte{7}, viewerAuthKeySize))
		_ = serverSide.Close()
		serverErr <- err
	}()
	if _, err := clientHandshake(clientSide); err == nil {
		t.Fatal("a version 1 viewer completed a handshake")
	}
	if err := <-serverErr; err == nil {
		t.Fatal("server accepted a version 1 viewer")
	}
}

func TestReadAuthKeyFileRemovesTheFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.key")
	key := bytes.Repeat([]byte{9}, viewerAuthKeySize)
	if err := os.WriteFile(path, []byte(base64.StdEncoding.EncodeToString(key)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readAuthKeyFile(path)
	if err != nil || !bytes.Equal(got, key) {
		t.Fatalf("readAuthKeyFile = %x, %v", got, err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("viewer key file left behind: %v", err)
	}
	if err := os.WriteFile(path, []byte("c2hvcnQ=\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readAuthKeyFile(path); err == nil {
		t.Fatal("accepted a short viewer key")
	}
}

// The phone's ViewerEncryption must produce these same values.
func TestViewerProtocolV2Vectors(t *testing.T) {
	authKey := bytes.Repeat([]byte{0x11}, 32)
	transcriptHash := handshakeTranscriptV2([]byte("client-public"), []byte("server-public"), bytes.Repeat([]byte{0x22}, 32))
	keys := deriveKeysV2(bytes.Repeat([]byte{0x33}, 32), transcriptHash, authKey)
	for name, tc := range map[string]struct {
		got  []byte
		want string
	}{
		"transcript": {transcriptHash, "711bcdb68798c5884260f3420cd35d55d311faddc4b40634688f766acce64121"},
		"client":     {viewerProof(authKey, viewerProofClient, transcriptHash), "04746ae333598f9388dbbc93e53bde921aeba012cd8bfcdb8c2d65b2ea0202b1"},
		"runtime":    {viewerProof(authKey, viewerProofRuntime, transcriptHash), "298b70ed7a278845269d669a91f7f220442eae8d4d0122fff011fdf1ce9a2245"},
		"c2s":        {keys.clientToServer, "9bbaeb35a49bab0fec8f4c1cac61f8f10dd90aa703d40addc4dfa96411b7d288"},
		"s2c":        {keys.serverToClient, "3167ce4de470e13b0df113cd1ba9591b66b38d067c150d130132646facf5dc4f"},
	} {
		if got := fmt.Sprintf("%x", tc.got); got != tc.want {
			t.Errorf("%s = %s, want %s", name, got, tc.want)
		}
	}
}

func clientHandshakeV2(conn net.Conn, authKey []byte) (*encryptedConn, error) {
	clientPrivate, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	clientPublicDER, err := x509.MarshalPKIXPublicKey(&clientPrivate.PublicKey)
	if err != nil {
		return nil, err
	}
	if err := writeHandshakeMagic(conn, protocolMagicV2, clientPublicDER); err != nil {
		return nil, err
	}
	serverPublicDER, err := readHandshakeMagic(conn, protocolMagicV2)
	if err != nil {
		return nil, err
	}
	serverNonce := make([]byte, serverNonceSize)
	if _, err := io.ReadFull(conn, serverNonce); err != nil {
		return nil, err
	}
	transcriptHash := handshakeTranscriptV2(clientPublicDER, serverPublicDER, serverNonce)
	if _, err := conn.Write(viewerProof(authKey, viewerProofClient, transcriptHash)); err != nil {
		return nil, err
	}
	serverProof := make([]byte, 32)
	if _, err := io.ReadFull(conn, serverProof); err != nil {
		return nil, err
	}
	if !bytes.Equal(serverProof, viewerProof(authKey, viewerProofRuntime, transcriptHash)) {
		return nil, errors.New("runtime did not prove the viewer key")
	}
	serverPublic, err := parseClientPublicKey(serverPublicDER)
	if err != nil {
		return nil, err
	}
	sharedSecret, err := ecdhSecret(serverPublic, clientPrivate)
	if err != nil {
		return nil, err
	}
	keys := deriveKeysV2(sharedSecret, transcriptHash, authKey)
	return newEncryptedConn(conn, keys.serverToClient, keys.clientToServer), nil
}
