package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"testing"
)

func TestServerHandshakeEncryptedRoundTrip(t *testing.T) {
	serverSide, clientSide := net.Pipe()
	defer serverSide.Close()
	defer clientSide.Close()

	serverErr := make(chan error, 1)
	go func() {
		conn, err := serverHandshake(serverSide)
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
