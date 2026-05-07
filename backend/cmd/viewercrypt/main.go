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
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"sync"
)

const (
	protocolMagic  = "VRTENC1\n"
	maxPublicKey   = 2048
	maxPlainFrame  = 32 * 1024
	maxCipherFrame = maxPlainFrame + 16
)

func main() {
	listenAddr := flag.String("listen", "127.0.0.1:7017", "encrypted listener address")
	upstreamAddr := flag.String("upstream", "127.0.0.1:7007", "plaintext scrcpy upstream address")
	flag.Parse()

	listener, err := net.Listen("tcp", *listenAddr)
	if err != nil {
		log.Fatalf("listen %s: %v", *listenAddr, err)
	}
	log.Printf("viewercrypt listening on %s upstream=%s", *listenAddr, *upstreamAddr)

	for {
		client, err := listener.Accept()
		if err != nil {
			log.Printf("accept: %v", err)
			continue
		}
		go handleClient(client, *upstreamAddr)
	}
}

func handleClient(rawClient net.Conn, upstreamAddr string) {
	defer rawClient.Close()

	client, err := serverHandshake(rawClient)
	if err != nil {
		log.Printf("handshake: %v", err)
		return
	}

	upstream, err := net.Dial("tcp", upstreamAddr)
	if err != nil {
		log.Printf("dial upstream: %v", err)
		return
	}
	defer upstream.Close()

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

func serverHandshake(conn net.Conn) (*encryptedConn, error) {
	clientPublicDER, err := readHandshake(conn)
	if err != nil {
		return nil, err
	}
	parsedClientPublic, err := x509.ParsePKIXPublicKey(clientPublicDER)
	if err != nil {
		return nil, fmt.Errorf("parse client public key: %w", err)
	}
	clientPublic, ok := parsedClientPublic.(*ecdsa.PublicKey)
	if !ok || clientPublic.Curve == nil || clientPublic.Curve.Params().Name != elliptic.P256().Params().Name {
		return nil, errors.New("client public key must be P-256 ECDH")
	}

	serverPrivate, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
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

	sharedX, _ := clientPublic.Curve.ScalarMult(clientPublic.X, clientPublic.Y, serverPrivate.D.Bytes())
	if sharedX == nil {
		return nil, errors.New("derive shared secret")
	}
	sharedSecret := leftPad(sharedX.Bytes(), 32)
	keys := deriveKeys(sharedSecret, append(bytes.Clone(clientPublicDER), serverPublicDER...))
	return newEncryptedConn(conn, keys.clientToServer, keys.serverToClient), nil
}

func readHandshake(r io.Reader) ([]byte, error) {
	magic := make([]byte, len(protocolMagic))
	if _, err := io.ReadFull(r, magic); err != nil {
		return nil, err
	}
	if string(magic) != protocolMagic {
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
	if len(publicKeyDER) <= 0 || len(publicKeyDER) > maxPublicKey {
		return fmt.Errorf("invalid public key length %d", len(publicKeyDER))
	}
	if _, err := w.Write([]byte(protocolMagic)); err != nil {
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
