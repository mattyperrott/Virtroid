package main

import (
	"bufio"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

// Database backups leave this server encrypted with a key derived from the
// indexd wallet's recovery phrase, which the operator already keeps offline:
// restoring needs only that phrase and the copy. The file is sealed in 64 KiB
// chunks, each numbered and the last one marked, so a cut-off, reordered or
// altered copy does not open.
const (
	backupMagic       = "VRTBAK1\n"
	backupSaltSize    = 16
	backupPrefixSize  = 7
	backupChunkSize   = 64 << 10
	backupKeyInfo     = "virtroid-db-backup-v1"
	maxRecoveryPhrase = 1024
)

func runBackupSeal(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	phrase, input, output, closeAll, err := backupFiles("backup-seal", args, stdin, stdout, stderr)
	if err != nil {
		return err
	}
	defer closeAll()
	writer := bufio.NewWriter(output)
	if err := sealBackup(writer, input, phrase); err != nil {
		return err
	}
	return writer.Flush()
}

func runBackupOpen(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	phrase, input, output, closeAll, err := backupFiles("backup-open", args, stdin, stdout, stderr)
	if err != nil {
		return err
	}
	defer closeAll()
	writer := bufio.NewWriter(output)
	if err := openBackup(writer, bufio.NewReader(input), phrase); err != nil {
		return err
	}
	return writer.Flush()
}

func backupFiles(name string, args []string, stdin io.Reader, stdout, stderr io.Writer) (string, io.Reader, io.Writer, func(), error) {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(stderr)
	phraseFile := flags.String("phrase-file", "", "file holding the indexd recovery phrase, alone or as indexd.yml's recoveryPhrase line")
	inPath := flags.String("in", "-", "input file, or - for standard input")
	outPath := flags.String("out", "-", "output file, or - for standard output")
	if err := flags.Parse(args); err != nil {
		return "", nil, nil, nil, err
	}
	if flags.NArg() != 0 || *phraseFile == "" {
		return "", nil, nil, nil, fmt.Errorf("%s needs --phrase-file and no positional arguments", name)
	}
	phrase, err := readRecoveryPhrase(*phraseFile)
	if err != nil {
		return "", nil, nil, nil, err
	}
	var closers []io.Closer
	closeAll := func() {
		for _, closer := range closers {
			_ = closer.Close()
		}
	}
	input := stdin
	if *inPath != "-" {
		file, err := os.Open(*inPath)
		if err != nil {
			return "", nil, nil, nil, err
		}
		closers = append(closers, file)
		input = file
	}
	output := stdout
	if *outPath != "-" {
		file, err := os.OpenFile(*outPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
		if err != nil {
			closeAll()
			return "", nil, nil, nil, err
		}
		closers = append(closers, file)
		output = file
	}
	return phrase, input, output, closeAll, nil
}

// readRecoveryPhrase reads the phrase from a file holding just the phrase or
// from indexd.yml, and normalizes its spacing and case.
func readRecoveryPhrase(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if len(data) > 64<<10 {
		return "", errors.New("recovery phrase file is too large")
	}
	text := string(data)
	for _, line := range strings.Split(text, "\n") {
		if value, ok := strings.CutPrefix(strings.TrimSpace(line), "recoveryPhrase:"); ok {
			text = strings.Trim(strings.TrimSpace(value), `"'`)
			break
		}
	}
	words := strings.Fields(strings.ToLower(text))
	if (len(words) != 12 && len(words) != 24) || len(text) > maxRecoveryPhrase {
		return "", errors.New("recovery phrase must be 12 or 24 words")
	}
	return strings.Join(words, " "), nil
}

func backupAEAD(phrase string, salt []byte) (cipher.AEAD, error) {
	key, err := hkdf.Key(sha256.New, []byte(phrase), salt, backupKeyInfo, 32)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func backupNonce(prefix []byte, counter uint32, last bool) []byte {
	nonce := make([]byte, 12)
	copy(nonce, prefix)
	binary.BigEndian.PutUint32(nonce[backupPrefixSize:], counter)
	if last {
		nonce[11] = 1
	}
	return nonce
}

func sealBackup(w io.Writer, r io.Reader, phrase string) error {
	header := make([]byte, len(backupMagic)+backupSaltSize+backupPrefixSize)
	copy(header, backupMagic)
	if _, err := rand.Read(header[len(backupMagic):]); err != nil {
		return err
	}
	salt := header[len(backupMagic) : len(backupMagic)+backupSaltSize]
	prefix := header[len(backupMagic)+backupSaltSize:]
	aead, err := backupAEAD(phrase, salt)
	if err != nil {
		return err
	}
	if _, err := w.Write(header); err != nil {
		return err
	}
	current, next := make([]byte, backupChunkSize), make([]byte, backupChunkSize)
	n, readErr := io.ReadFull(r, current)
	for counter := uint32(0); ; counter++ {
		if readErr != nil && !errors.Is(readErr, io.EOF) && !errors.Is(readErr, io.ErrUnexpectedEOF) {
			return readErr
		}
		last := readErr != nil
		var m int
		var nextErr error
		if !last {
			m, nextErr = io.ReadFull(r, next)
			if errors.Is(nextErr, io.EOF) {
				last = true
			} else if nextErr != nil && !errors.Is(nextErr, io.ErrUnexpectedEOF) {
				return nextErr
			}
		}
		if _, err := w.Write(aead.Seal(nil, backupNonce(prefix, counter, last), current[:n], header)); err != nil {
			return err
		}
		if last {
			return nil
		}
		if counter == ^uint32(0) {
			return errors.New("backup is too large")
		}
		current, next = next, current
		n, readErr = m, nextErr
	}
}

func openBackup(w io.Writer, r io.Reader, phrase string) error {
	header := make([]byte, len(backupMagic)+backupSaltSize+backupPrefixSize)
	if _, err := io.ReadFull(r, header); err != nil || string(header[:len(backupMagic)]) != backupMagic {
		return errors.New("not a Virtroid database backup")
	}
	salt := header[len(backupMagic) : len(backupMagic)+backupSaltSize]
	prefix := header[len(backupMagic)+backupSaltSize:]
	aead, err := backupAEAD(phrase, salt)
	if err != nil {
		return err
	}
	sealedSize := backupChunkSize + aead.Overhead()
	current, next := make([]byte, sealedSize), make([]byte, sealedSize)
	n, readErr := io.ReadFull(r, current)
	for counter := uint32(0); ; counter++ {
		if errors.Is(readErr, io.EOF) {
			return errors.New("backup is cut off")
		}
		if readErr != nil && !errors.Is(readErr, io.ErrUnexpectedEOF) {
			return readErr
		}
		last := readErr != nil
		var m int
		var nextErr error
		if !last {
			m, nextErr = io.ReadFull(r, next)
			if errors.Is(nextErr, io.EOF) {
				last = true
			} else if nextErr != nil && !errors.Is(nextErr, io.ErrUnexpectedEOF) {
				return nextErr
			}
		}
		plaintext, err := aead.Open(nil, backupNonce(prefix, counter, last), current[:n], header)
		if err != nil {
			return errors.New("backup does not open with this recovery phrase, or was altered or cut off")
		}
		if _, err := w.Write(plaintext); err != nil {
			return err
		}
		if last {
			return nil
		}
		current, next = next, current
		n, readErr = m, nextErr
	}
}
