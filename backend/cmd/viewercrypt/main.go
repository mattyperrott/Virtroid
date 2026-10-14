package main

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	protocolMagic = "VRTENC1\n"
	// protocolMagicV2 is the handshake in which the viewer and viewercrypt
	// each prove they hold the runtime's viewer key before any stream flows.
	protocolMagicV2      = "VRTENC2\n"
	viewerAuthKeySize    = 32
	serverNonceSize      = 32
	maxPublicKey         = 2048
	maxPlainFrame        = 32 * 1024
	maxCipherFrame       = maxPlainFrame + 16
	maxConcurrentClients = 16
	handshakeTimeout     = 10 * time.Second
	upstreamDialTimeout  = 10 * time.Second
	maxSessionDuration   = time.Hour
)

func main() {
	listenAddr := flag.String("listen", "127.0.0.1:7017", "encrypted listener address")
	upstreamAddr := flag.String("upstream", "127.0.0.1:7007", "plaintext scrcpy upstream address")
	publicKeyFile := flag.String("public-key-file", "", "optional path to write the base64 server ECDH public key")
	authKeyFile := flag.String("auth-key-file", "", "file holding the runtime's base64 viewer key; read, then removed, and viewers must prove it")
	flag.Parse()

	var authKey []byte
	if *authKeyFile != "" {
		key, err := readAuthKeyFile(*authKeyFile)
		if err != nil {
			log.Fatalf("viewer key: %v", err)
		}
		authKey = key
	}

	serverPrivate, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		log.Fatalf("generate server key: %v", err)
	}
	serverPublicDER, err := x509.MarshalPKIXPublicKey(&serverPrivate.PublicKey)
	if err != nil {
		log.Fatalf("marshal server public key: %v", err)
	}
	if *publicKeyFile != "" {
		if err := writePublicKeyFile(*publicKeyFile, serverPublicDER); err != nil {
			log.Fatalf("write public key file: %v", err)
		}
	}

	listener, err := listen(*listenAddr)
	if err != nil {
		log.Fatalf("listen %s: %v", *listenAddr, err)
	}
	log.Printf("viewercrypt listening on %s upstream=%s", *listenAddr, *upstreamAddr)

	clientSlots := make(chan struct{}, maxConcurrentClients)
	for {
		client, err := listener.Accept()
		if err != nil {
			log.Printf("accept: %v", err)
			continue
		}
		select {
		case clientSlots <- struct{}{}:
			go func() {
				defer func() {
					<-clientSlots
					if recovered := recover(); recovered != nil {
						log.Printf("client handler panic")
					}
				}()
				handleClient(client, *upstreamAddr, serverPrivate, authKey)
			}()
		default:
			log.Printf("rejecting viewer connection: capacity exhausted")
			_ = client.Close()
		}
	}
}

// splitAddress reads "unix:/path" as a Unix socket and anything else as TCP.
func splitAddress(address string) (string, string) {
	if path, ok := strings.CutPrefix(address, "unix:"); ok {
		return "unix", path
	}
	return "tcp", address
}

// listen opens the encrypted listener. A Unix socket replaces any stale one
// and is made readable by its owner only: apps in the runtime share its
// network, so only the node, which reaches it as root, may connect.
func listen(address string) (net.Listener, error) {
	network, path := splitAddress(address)
	if network != "unix" {
		return net.Listen(network, path)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = listener.Close()
		return nil, err
	}
	return listener, nil
}

// readAuthKeyFile reads the viewer key the node left for this process and
// removes it, so it stays only in this process's memory.
func readAuthKeyFile(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if err := os.Remove(path); err != nil {
		log.Printf("remove viewer key file: %v", err)
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(data)))
	if err != nil || len(key) != viewerAuthKeySize {
		return nil, errors.New("viewer key file does not hold a 32-byte base64 key")
	}
	return key, nil
}

func handleClient(rawClient net.Conn, upstreamAddr string, serverPrivate *ecdsa.PrivateKey, authKey []byte) {
	defer rawClient.Close()

	if err := rawClient.SetDeadline(time.Now().Add(handshakeTimeout)); err != nil {
		log.Printf("set handshake deadline: %v", err)
		return
	}
	var client *encryptedConn
	var err error
	if authKey != nil {
		client, err = serverHandshakeV2(rawClient, serverPrivate, authKey)
	} else {
		client, err = serverHandshake(rawClient, serverPrivate)
	}
	if err != nil {
		log.Printf("handshake: %v", err)
		return
	}
	sessionDeadline := time.Now().Add(maxSessionDuration)
	if err := rawClient.SetDeadline(sessionDeadline); err != nil {
		log.Printf("set client session deadline: %v", err)
		return
	}

	network, address := splitAddress(upstreamAddr)
	upstream, err := net.DialTimeout(network, address, upstreamDialTimeout)
	if err != nil {
		log.Printf("dial upstream: %v", err)
		return
	}
	defer upstream.Close()
	if err := upstream.SetDeadline(sessionDeadline); err != nil {
		log.Printf("set upstream session deadline: %v", err)
		return
	}

	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(upstream, client)
		_ = upstream.Close()
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(client, upstream)
		_ = client.Close()
		done <- struct{}{}
	}()
	<-done
}

func serverHandshake(conn net.Conn, serverPrivate *ecdsa.PrivateKey) (*encryptedConn, error) {
	if serverPrivate == nil {
		return nil, errors.New("server private key is required")
	}
	clientPublicDER, err := readHandshake(conn)
	if err != nil {
		return nil, err
	}
	clientPublic, err := parseClientPublicKey(clientPublicDER)
	if err != nil {
		return nil, err
	}

	serverPublicDER, err := x509.MarshalPKIXPublicKey(&serverPrivate.PublicKey)
	if err != nil {
		return nil, err
	}
	if err := writeHandshake(conn, serverPublicDER); err != nil {
		return nil, err
	}

	sharedSecret, err := ecdhSecret(clientPublic, serverPrivate)
	if err != nil {
		return nil, err
	}
	keys := deriveKeys(sharedSecret, append(bytes.Clone(clientPublicDER), serverPublicDER...))
	return newEncryptedConn(conn, keys.clientToServer, keys.serverToClient), nil
}

// serverHandshakeV2 is serverHandshake for a runtime whose viewers must hold
// its viewer key. The client proves the key over the whole handshake,
// including a fresh nonce, before anything reaches the screen server; then
// viewercrypt proves it back, and the key is mixed into the traffic keys.
func serverHandshakeV2(conn net.Conn, serverPrivate *ecdsa.PrivateKey, authKey []byte) (*encryptedConn, error) {
	if serverPrivate == nil || len(authKey) != viewerAuthKeySize {
		return nil, errors.New("server private key and viewer key are required")
	}
	clientPublicDER, err := readHandshakeMagic(conn, protocolMagicV2)
	if err != nil {
		return nil, err
	}
	clientPublic, err := parseClientPublicKey(clientPublicDER)
	if err != nil {
		return nil, err
	}
	serverPublicDER, err := x509.MarshalPKIXPublicKey(&serverPrivate.PublicKey)
	if err != nil {
		return nil, err
	}
	serverNonce := make([]byte, serverNonceSize)
	if _, err := rand.Read(serverNonce); err != nil {
		return nil, err
	}
	if err := writeHandshakeMagic(conn, protocolMagicV2, serverPublicDER); err != nil {
		return nil, err
	}
	if _, err := conn.Write(serverNonce); err != nil {
		return nil, err
	}

	transcriptHash := handshakeTranscriptV2(clientPublicDER, serverPublicDER, serverNonce)
	clientProof := make([]byte, sha256.Size)
	if _, err := io.ReadFull(conn, clientProof); err != nil {
		return nil, err
	}
	if !hmac.Equal(clientProof, viewerProof(authKey, viewerProofClient, transcriptHash)) {
		return nil, errors.New("viewer did not prove the runtime's viewer key")
	}
	if _, err := conn.Write(viewerProof(authKey, viewerProofRuntime, transcriptHash)); err != nil {
		return nil, err
	}

	sharedSecret, err := ecdhSecret(clientPublic, serverPrivate)
	if err != nil {
		return nil, err
	}
	keys := deriveKeysV2(sharedSecret, transcriptHash, authKey)
	return newEncryptedConn(conn, keys.clientToServer, keys.serverToClient), nil
}

const (
	viewerProofClient  = "virtroid-viewer-auth-v2 client\n"
	viewerProofRuntime = "virtroid-viewer-auth-v2 runtime\n"
)

func viewerProof(authKey []byte, role string, transcriptHash []byte) []byte {
	mac := hmac.New(sha256.New, authKey)
	mac.Write([]byte(role))
	mac.Write(transcriptHash)
	return mac.Sum(nil)
}

func handshakeTranscriptV2(clientPublicDER, serverPublicDER, serverNonce []byte) []byte {
	digest := sha256.New()
	digest.Write([]byte(protocolMagicV2))
	digest.Write(clientPublicDER)
	digest.Write(serverPublicDER)
	digest.Write(serverNonce)
	return digest.Sum(nil)
}

func parseClientPublicKey(der []byte) (*ecdsa.PublicKey, error) {
	parsed, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return nil, fmt.Errorf("parse client public key: %w", err)
	}
	clientPublic, ok := parsed.(*ecdsa.PublicKey)
	if !ok || clientPublic.Curve == nil || clientPublic.Curve.Params().Name != elliptic.P256().Params().Name {
		return nil, errors.New("client public key must be P-256 ECDH")
	}
	return clientPublic, nil
}

func ecdhSecret(clientPublic *ecdsa.PublicKey, serverPrivate *ecdsa.PrivateKey) ([]byte, error) {
	sharedX, _ := clientPublic.Curve.ScalarMult(clientPublic.X, clientPublic.Y, serverPrivate.D.Bytes())
	if sharedX == nil {
		return nil, errors.New("derive shared secret")
	}
	return leftPad(sharedX.Bytes(), 32), nil
}

func writePublicKeyFile(path string, publicKeyDER []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".viewer-key-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	payload := base64.StdEncoding.EncodeToString(publicKeyDER) + "\n"
	if _, err := tmp.WriteString(payload); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func readHandshake(r io.Reader) ([]byte, error) {
	return readHandshakeMagic(r, protocolMagic)
}

func readHandshakeMagic(r io.Reader, expected string) ([]byte, error) {
	magic := make([]byte, len(expected))
	if _, err := io.ReadFull(r, magic); err != nil {
		return nil, err
	}
	if string(magic) != expected {
		return nil, errors.New("invalid encryption protocol magic")
	}
	var lengthBytes [2]byte
	if _, err := io.ReadFull(r, lengthBytes[:]); err != nil {
		return nil, err
	}
	length := int(binary.BigEndian.Uint16(lengthBytes[:]))
	if length <= 0 || length > maxPublicKey {
		return nil, fmt.Errorf("invalid public key length %d", length)
	}
	payload := make([]byte, length)
	_, err := io.ReadFull(r, payload)
	return payload, err
}

func writeHandshake(w io.Writer, publicKeyDER []byte) error {
	return writeHandshakeMagic(w, protocolMagic, publicKeyDER)
}

func writeHandshakeMagic(w io.Writer, magic string, publicKeyDER []byte) error {
	if len(publicKeyDER) <= 0 || len(publicKeyDER) > maxPublicKey {
		return fmt.Errorf("invalid public key length %d", len(publicKeyDER))
	}
	if _, err := w.Write([]byte(magic)); err != nil {
		return err
	}
	var lengthBytes [2]byte
	binary.BigEndian.PutUint16(lengthBytes[:], uint16(len(publicKeyDER)))
	if _, err := w.Write(lengthBytes[:]); err != nil {
		return err
	}
	_, err := w.Write(publicKeyDER)
	return err
}

type trafficKeys struct {
	clientToServer []byte
	serverToClient []byte
}

func deriveKeys(sharedSecret, transcript []byte) trafficKeys {
	salt := sha256.Sum256(transcript)
	prk := hmacSHA256(salt[:], sharedSecret)
	return trafficKeys{
		clientToServer: hkdfExpand(prk, []byte("virtroid-viewer-e2ee-v1 client-to-runtime"), 32),
		serverToClient: hkdfExpand(prk, []byte("virtroid-viewer-e2ee-v1 runtime-to-client"), 32),
	}
}

// deriveKeysV2 mixes the viewer key into the ECDH secret, so a party without
// it ends up with different traffic keys even if a proof check were skipped.
func deriveKeysV2(sharedSecret, transcriptHash, authKey []byte) trafficKeys {
	prk := hmacSHA256(transcriptHash, append(bytes.Clone(sharedSecret), authKey...))
	return trafficKeys{
		clientToServer: hkdfExpand(prk, []byte("virtroid-viewer-e2ee-v2 client-to-runtime"), 32),
		serverToClient: hkdfExpand(prk, []byte("virtroid-viewer-e2ee-v2 runtime-to-client"), 32),
	}
}

func hkdfExpand(prk, info []byte, length int) []byte {
	var output []byte
	var previous []byte
	for counter := byte(1); len(output) < length; counter++ {
		mac := hmac.New(sha256.New, prk)
		mac.Write(previous)
		mac.Write(info)
		mac.Write([]byte{counter})
		previous = mac.Sum(nil)
		output = append(output, previous...)
	}
	return output[:length]
}

func hmacSHA256(key, payload []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(payload)
	return mac.Sum(nil)
}

func leftPad(value []byte, size int) []byte {
	if len(value) >= size {
		return value
	}
	out := make([]byte, size)
	copy(out[size-len(value):], value)
	return out
}

type encryptedConn struct {
	net.Conn
	readAEAD  cipher.AEAD
	writeAEAD cipher.AEAD
	readSeq   uint64
	writeSeq  uint64
	readBuf   bytes.Buffer
	writeMu   sync.Mutex
}

func newEncryptedConn(conn net.Conn, readKey, writeKey []byte) *encryptedConn {
	readBlock, err := aes.NewCipher(readKey)
	if err != nil {
		panic(err)
	}
	writeBlock, err := aes.NewCipher(writeKey)
	if err != nil {
		panic(err)
	}
	readAEAD, err := cipher.NewGCM(readBlock)
	if err != nil {
		panic(err)
	}
	writeAEAD, err := cipher.NewGCM(writeBlock)
	if err != nil {
		panic(err)
	}
	return &encryptedConn{
		Conn:      conn,
		readAEAD:  readAEAD,
		writeAEAD: writeAEAD,
	}
}

func (c *encryptedConn) Read(p []byte) (int, error) {
	for c.readBuf.Len() == 0 {
		var lengthBytes [4]byte
		if _, err := io.ReadFull(c.Conn, lengthBytes[:]); err != nil {
			return 0, err
		}
		length := int(binary.BigEndian.Uint32(lengthBytes[:]))
		if length <= 0 || length > maxCipherFrame {
			return 0, fmt.Errorf("invalid encrypted frame length %d", length)
		}
		ciphertext := make([]byte, length)
		if _, err := io.ReadFull(c.Conn, ciphertext); err != nil {
			return 0, err
		}
		plaintext, err := c.readAEAD.Open(nil, frameNonce(c.readSeq), ciphertext, nil)
		if err != nil {
			return 0, err
		}
		c.readSeq++
		c.readBuf.Write(plaintext)
	}
	return c.readBuf.Read(p)
}

func (c *encryptedConn) Write(p []byte) (int, error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	written := 0
	for written < len(p) {
		end := written + maxPlainFrame
		if end > len(p) {
			end = len(p)
		}
		ciphertext := c.writeAEAD.Seal(nil, frameNonce(c.writeSeq), p[written:end], nil)
		c.writeSeq++

		var lengthBytes [4]byte
		binary.BigEndian.PutUint32(lengthBytes[:], uint32(len(ciphertext)))
		if _, err := c.Conn.Write(lengthBytes[:]); err != nil {
			return written, err
		}
		if _, err := c.Conn.Write(ciphertext); err != nil {
			return written, err
		}
		written = end
	}
	return len(p), nil
}

func frameNonce(seq uint64) []byte {
	nonce := make([]byte, 12)
	binary.BigEndian.PutUint64(nonce[4:], seq)
	return nonce
}
