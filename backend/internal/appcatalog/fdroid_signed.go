package appcatalog

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
	"time"
)

// F-Droid signs entry.jar with its repository key; the entry.json inside it
// names index-v2.json with its SHA-256 and size. Checking that signature
// against the repository's published certificate fingerprint lets the catalog
// follow F-Droid on its own, with every app still pinned by hash.

const (
	entryJarURL = "https://f-droid.org/repo/entry.jar"
	// fdroidRepoCertSHA256 is the SHA-256 of f-droid.org/repo's signing
	// certificate, as F-Droid publishes it and its own client pins it.
	fdroidRepoCertSHA256 = "43238d512c1e5eb2d6569f4a3afbf5523418b82e0a3ed1552770abb9a9c9ccab"
	maxEntryJarBytes     = 1 << 20
)

var (
	oidSHA256          = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 2, 1}
	oidRSAEncryption   = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 1, 1}
	oidSHA256WithRSA   = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 1, 11}
	oidPKCS7SignedData = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 2}
	oidMessageDigest   = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 4}
)

// SignedSyncState is what the last sync saw, so an unchanged index is not
// downloaded again and an older signed entry is never accepted.
type SignedSyncState struct {
	IndexSHA256 string
	Timestamp   int64
}

// SyncFDroidSigned refreshes the catalog from F-Droid's signed index. It
// returns how many apps it wrote (0 when the index has not changed) and the
// state for the next run.
func SyncFDroidSigned(ctx context.Context, st Store, last SignedSyncState, maxApps int) (int, SignedSyncState, error) {
	return syncSigned(ctx, st, fdroidHTTPClient(), entryJarURL, defaultIndexURL, fdroidRepoCertSHA256, last, maxApps)
}

func syncSigned(
	ctx context.Context,
	st Store,
	client *http.Client,
	jarURL, indexURL, certSHA256 string,
	last SignedSyncState,
	maxApps int,
) (int, SignedSyncState, error) {
	entry, err := fetchSignedEntry(ctx, client, jarURL, certSHA256)
	if err != nil {
		return 0, last, err
	}
	if entry.Timestamp < last.Timestamp {
		return 0, last, fmt.Errorf("fdroid entry is older than the last one synced")
	}
	next := SignedSyncState{IndexSHA256: entry.Index.SHA256, Timestamp: entry.Timestamp}
	if entry.Index.SHA256 == last.IndexSHA256 {
		return 0, next, nil
	}
	entries, err := fetchPinnedEntries(ctx, client, indexURL, entry.Index.SHA256)
	if err != nil {
		return 0, last, err
	}
	count, err := replaceFDroidEntries(ctx, st, entries, maxApps)
	if err != nil {
		return 0, last, err
	}
	return count, next, nil
}

type fdroidEntry struct {
	Timestamp int64 `json:"timestamp"`
	Index     struct {
		Name   string `json:"name"`
		SHA256 string `json:"sha256"`
		Size   int64  `json:"size"`
	} `json:"index"`
}

func fetchSignedEntry(ctx context.Context, client *http.Client, jarURL, certSHA256 string) (fdroidEntry, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, jarURL, nil)
	if err != nil {
		return fdroidEntry{}, err
	}
	req.Header.Set("User-Agent", "virtroid-app-catalog/0.2")
	resp, err := client.Do(req)
	if err != nil {
		return fdroidEntry{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fdroidEntry{}, fmt.Errorf("fdroid entry returned HTTP %d", resp.StatusCode)
	}
	jar, err := io.ReadAll(io.LimitReader(resp.Body, maxEntryJarBytes+1))
	if err != nil {
		return fdroidEntry{}, fmt.Errorf("read fdroid entry: %w", err)
	}
	if len(jar) > maxEntryJarBytes {
		return fdroidEntry{}, fmt.Errorf("fdroid entry exceeds %d-byte limit", maxEntryJarBytes)
	}
	entryJSON, err := verifySignedEntryJar(jar, certSHA256)
	if err != nil {
		return fdroidEntry{}, err
	}
	return parseFDroidEntry(entryJSON)
}

func parseFDroidEntry(raw []byte) (fdroidEntry, error) {
	var entry fdroidEntry
	if err := json.Unmarshal(raw, &entry); err != nil {
		return fdroidEntry{}, fmt.Errorf("decode fdroid entry: %w", err)
	}
	if entry.Index.Name != "/index-v2.json" {
		return fdroidEntry{}, fmt.Errorf("fdroid entry names index %q", entry.Index.Name)
	}
	sum, err := normalizeSHA256(entry.Index.SHA256)
	if err != nil {
		return fdroidEntry{}, fmt.Errorf("fdroid entry index digest: %w", err)
	}
	entry.Index.SHA256 = sum
	if entry.Index.Size <= 0 || entry.Index.Size > maxIndexBytes {
		return fdroidEntry{}, fmt.Errorf("fdroid entry index size %d is out of range", entry.Index.Size)
	}
	if entry.Timestamp <= 0 {
		return fdroidEntry{}, errors.New("fdroid entry has no timestamp")
	}
	return entry, nil
}

// verifySignedEntryJar checks a v1 JAR signature: the signature block signs
// the .SF file with the pinned certificate's key, the .SF holds the digest of
// MANIFEST.MF, and the manifest holds the digest of entry.json, the jar's only
// other file. It returns entry.json.
func verifySignedEntryJar(jar []byte, certSHA256 string) ([]byte, error) {
	reader, err := zip.NewReader(bytes.NewReader(jar), int64(len(jar)))
	if err != nil {
		return nil, fmt.Errorf("open fdroid entry jar: %w", err)
	}
	files := map[string][]byte{}
	for _, file := range reader.File {
		if _, dup := files[file.Name]; dup {
			return nil, fmt.Errorf("fdroid entry jar repeats %s", file.Name)
		}
		if file.UncompressedSize64 > maxEntryJarBytes {
			return nil, fmt.Errorf("fdroid entry jar file %s is too large", file.Name)
		}
		body, err := file.Open()
		if err != nil {
			return nil, err
		}
		data, err := io.ReadAll(io.LimitReader(body, maxEntryJarBytes+1))
		body.Close()
		if err != nil {
			return nil, err
		}
		if len(data) > maxEntryJarBytes {
			return nil, fmt.Errorf("fdroid entry jar file %s is too large", file.Name)
		}
		files[file.Name] = data
	}

	var sfName, blockName string
	for name := range files {
		switch {
		case name == "entry.json" || name == "META-INF/MANIFEST.MF":
		case path.Dir(name) == "META-INF" && strings.HasSuffix(name, ".SF"):
			if sfName != "" {
				return nil, errors.New("fdroid entry jar has more than one signature file")
			}
			sfName = name
		case path.Dir(name) == "META-INF" && (strings.HasSuffix(name, ".RSA") || strings.HasSuffix(name, ".EC") || strings.HasSuffix(name, ".DSA")):
			if blockName != "" {
				return nil, errors.New("fdroid entry jar has more than one signature block")
			}
			blockName = name
		default:
			return nil, fmt.Errorf("fdroid entry jar has unexpected file %s", name)
		}
	}
	entryJSON, manifest := files["entry.json"], files["META-INF/MANIFEST.MF"]
	if entryJSON == nil || manifest == nil || sfName == "" || blockName == "" ||
		strings.TrimSuffix(sfName, ".SF") != strings.TrimSuffix(blockName, path.Ext(blockName)) {
		return nil, errors.New("fdroid entry jar is not signed")
	}
	signatureFile := files[sfName]

	if err := verifyPKCS7Detached(files[blockName], signatureFile, certSHA256); err != nil {
		return nil, err
	}
	sfMain := parseJarManifest(signatureFile)
	if len(sfMain) == 0 || !digestMatches(sfMain[0]["SHA-256-Digest-Manifest"], manifest) {
		return nil, errors.New("fdroid entry signature does not cover its manifest")
	}
	matched := false
	for _, section := range parseJarManifest(manifest) {
		if section["Name"] == "entry.json" {
			matched = digestMatches(section["SHA-256-Digest"], entryJSON)
		}
	}
	if !matched {
		return nil, errors.New("fdroid entry manifest does not match entry.json")
	}
	return entryJSON, nil
}

func digestMatches(encoded string, data []byte) bool {
	want, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil || len(want) != sha256.Size {
		return false
	}
	got := sha256.Sum256(data)
	return bytes.Equal(want, got[:])
}

// parseJarManifest splits a JAR manifest or signature file into sections of
// attributes, joining 72-byte continuation lines.
func parseJarManifest(data []byte) []map[string]string {
	var sections []map[string]string
	current := map[string]string{}
	lastKey := ""
	flush := func() {
		if len(current) > 0 {
			sections = append(sections, current)
		}
		current, lastKey = map[string]string{}, ""
	}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")
		switch {
		case line == "":
			flush()
		case strings.HasPrefix(line, " ") && lastKey != "":
			current[lastKey] += line[1:]
		default:
			key, value, ok := strings.Cut(line, ": ")
			if !ok {
				continue
			}
			current[key], lastKey = value, key
		}
	}
	flush()
	return sections
}

type pkcs7ContentInfo struct {
	ContentType asn1.ObjectIdentifier
	Content     asn1.RawValue `asn1:"explicit,optional,tag:0"`
}

type pkcs7SignedData struct {
	Version          int
	DigestAlgorithms []pkix.AlgorithmIdentifier `asn1:"set"`
	ContentInfo      pkcs7ContentInfo
	Certificates     asn1.RawValue     `asn1:"optional,tag:0"`
	CRLs             asn1.RawValue     `asn1:"optional,tag:1"`
	SignerInfos      []pkcs7SignerInfo `asn1:"set"`
}

type pkcs7SignerInfo struct {
	Version                   int
	IssuerAndSerialNumber     asn1.RawValue
	DigestAlgorithm           pkix.AlgorithmIdentifier
	AuthenticatedAttributes   asn1.RawValue `asn1:"optional,tag:0"`
	DigestEncryptionAlgorithm pkix.AlgorithmIdentifier
	EncryptedDigest           []byte
	UnauthenticatedAttributes asn1.RawValue `asn1:"optional,tag:1"`
}

type pkcs7Attribute struct {
	Type   asn1.ObjectIdentifier
	Values asn1.RawValue `asn1:"set"`
}

// verifyPKCS7Detached checks a detached PKCS#7 signature over content made
// with SHA-256 and the RSA key of the certificate whose DER has the pinned
// SHA-256. The certificate is trusted by that pin alone.
func verifyPKCS7Detached(block, content []byte, certSHA256 string) error {
	var info pkcs7ContentInfo
	if rest, err := asn1.Unmarshal(block, &info); err != nil || len(rest) != 0 || !info.ContentType.Equal(oidPKCS7SignedData) {
		return errors.New("fdroid entry signature is not PKCS#7 signed data")
	}
	var signed pkcs7SignedData
	if _, err := asn1.Unmarshal(info.Content.Bytes, &signed); err != nil {
		return fmt.Errorf("parse fdroid entry signature: %w", err)
	}
	certs, err := x509.ParseCertificates(signed.Certificates.Bytes)
	if err != nil {
		return fmt.Errorf("parse fdroid entry certificates: %w", err)
	}
	var pinned *x509.Certificate
	for _, cert := range certs {
		sum := sha256.Sum256(cert.Raw)
		if hex.EncodeToString(sum[:]) == certSHA256 {
			pinned = cert
		}
	}
	if pinned == nil {
		return errors.New("fdroid entry is not signed with F-Droid's repository certificate")
	}
	key, ok := pinned.PublicKey.(*rsa.PublicKey)
	if !ok {
		return errors.New("fdroid repository certificate has no RSA key")
	}
	if len(signed.SignerInfos) != 1 {
		return errors.New("fdroid entry signature must have one signer")
	}
	signer := signed.SignerInfos[0]
	if !signer.DigestAlgorithm.Algorithm.Equal(oidSHA256) ||
		!(signer.DigestEncryptionAlgorithm.Algorithm.Equal(oidRSAEncryption) || signer.DigestEncryptionAlgorithm.Algorithm.Equal(oidSHA256WithRSA)) {
		return errors.New("fdroid entry signature does not use SHA-256 with RSA")
	}

	contentDigest := sha256.Sum256(content)
	signedDigest := contentDigest
	if len(signer.AuthenticatedAttributes.FullBytes) > 0 {
		// With signed attributes, the signature covers them (as a SET) and
		// they carry the content's digest.
		var attributes []pkcs7Attribute
		if _, err := asn1.UnmarshalWithParams(signer.AuthenticatedAttributes.FullBytes, &attributes, "set,tag:0"); err != nil {
			return fmt.Errorf("parse fdroid entry signed attributes: %w", err)
		}
		found := false
		for _, attribute := range attributes {
			if !attribute.Type.Equal(oidMessageDigest) {
				continue
			}
			var digest []byte
			if _, err := asn1.Unmarshal(attribute.Values.Bytes, &digest); err != nil || !bytes.Equal(digest, contentDigest[:]) {
				return errors.New("fdroid entry signed digest does not match")
			}
			found = true
		}
		if !found {
			return errors.New("fdroid entry signed attributes have no digest")
		}
		set := append([]byte{0x31}, signer.AuthenticatedAttributes.FullBytes[1:]...)
		signedDigest = sha256.Sum256(set)
	}
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, signedDigest[:], signer.EncryptedDigest); err != nil {
		return errors.New("fdroid entry signature is not valid")
	}
	return nil
}
