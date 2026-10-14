package appcatalog

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"virtroid/backend/internal/store"
)

type catalogSyncStore struct {
	disabledSource string
	upserted       int
}

func (s *catalogSyncStore) UpsertAppCatalogEntries(_ context.Context, entries []store.AppCatalogEntry) (int, error) {
	if s.upserted > 0 {
		return len(entries), nil
	}
	return 0, nil
}

func (s *catalogSyncStore) DisableAppCatalogSource(_ context.Context, source string) (int, error) {
	s.disabledSource = source
	return 3, nil
}

// testIndex is an index-v2.json with one runtime-compatible package and
// two that are not.
const testIndex = `{
  "repo": {"name": {"en-US": "Test"}, "timestamp": 1},
  "packages": {
    "org.example.compatible": {
      "metadata": {"name": {"en-US": "Compatible"}},
      "versions": {
        "old": {"file": {"name": "org.example.compatible_1.apk", "sha256": "` + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" + `", "size": 100},
                "manifest": {"versionName": "1", "versionCode": 1, "usesSdk": {"minSdkVersion": 28}}},
        "new": {"file": {"name": "org.example.compatible_2.apk", "sha256": "` + "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" + `", "size": 200},
                "manifest": {"versionName": "2", "versionCode": 2, "nativecode": ["x86_64"], "usesSdk": {"minSdkVersion": 34}}}
      }
    },
    "org.example.armonly": {
      "metadata": {"name": {"en": "ARM only"}},
      "versions": {"one": {"file": {"name": "org.example.armonly_1.apk", "sha256": "` + "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc" + `", "size": 100},
                           "manifest": {"versionName": "1", "versionCode": 1, "nativecode": ["arm64-v8a"]}}}
    },
    "org.example.future": {
      "metadata": {"name": {"en": "Future"}},
      "versions": {"one": {"file": {"name": "org.example.future_1.apk", "sha256": "` + "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd" + `", "size": 100},
                           "manifest": {"versionName": "1", "versionCode": 1, "usesSdk": {"minSdkVersion": 35}}}}
    }
  }
}`

func TestDecodePinnedEntriesKeepsLatestCompatibleVersionsOfAMatchingIndex(t *testing.T) {
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(testIndex)))
	entries, err := decodePinnedEntries(strings.NewReader(testIndex), digest)
	if err != nil {
		t.Fatalf("decodePinnedEntries: %v", err)
	}
	if len(entries) != 1 || entries[0].PackageName != "org.example.compatible" || entries[0].VersionCode != 2 {
		t.Fatalf("entries = %+v, want only the compatible package at version 2", entries)
	}

	if _, err := decodePinnedEntries(strings.NewReader(testIndex), strings.Repeat("0", 64)); err == nil || !strings.Contains(err.Error(), "mismatch") {
		t.Fatalf("wrong digest error = %v, want mismatch", err)
	}
	trailing := testIndex + ` {}`
	if _, err := decodePinnedEntries(strings.NewReader(trailing), fmt.Sprintf("%x", sha256.Sum256([]byte(trailing)))); err == nil || !strings.Contains(err.Error(), "trailing") {
		t.Fatalf("trailing JSON error = %v, want trailing data", err)
	}
	if _, err := decodePinnedEntries(strings.NewReader(testIndex), ""); err == nil {
		t.Fatal("empty digest unexpectedly succeeded")
	}
}

func TestFetchPinnedEntriesHandlesHTTPFailuresAndRejectsRedirects(t *testing.T) {
	payload := []byte(testIndex)
	digest := fmt.Sprintf("%x", sha256.Sum256(payload))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/index-v2.json":
			w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
			_, _ = w.Write(payload)
		case "/redirect":
			http.Redirect(w, r, "/index-v2.json", http.StatusFound)
		case "/unavailable":
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
		case "/oversized":
			w.Header().Set("Content-Length", strconv.FormatInt(maxIndexBytes+1, 10))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	if _, err := fetchPinnedEntries(context.Background(), fdroidHTTPClient(), server.URL+"/index-v2.json", digest); err != nil {
		t.Fatalf("fetch pinned index: %v", err)
	}
	if _, err := fetchPinnedEntries(context.Background(), fdroidHTTPClient(), server.URL+"/redirect", digest); err == nil || !strings.Contains(err.Error(), "HTTP 302") {
		t.Fatalf("redirect error = %v, want fail-closed status", err)
	}
	if _, err := fetchPinnedEntries(context.Background(), fdroidHTTPClient(), server.URL+"/unavailable", digest); err == nil || !strings.Contains(err.Error(), "HTTP 503") {
		t.Fatalf("server error = %v, want HTTP status", err)
	}
	if _, err := fetchPinnedEntries(context.Background(), fdroidHTTPClient(), server.URL+"/oversized", digest); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized error = %v, want size rejection", err)
	}
	if _, err := fetchPinnedEntries(context.Background(), nil, server.URL+"/index-v2.json", digest); err == nil {
		t.Fatal("nil HTTP client unexpectedly accepted")
	}
}

func TestReplaceFDroidEntriesDisablesTheSourceForAnEmptyIndex(t *testing.T) {
	fake := &catalogSyncStore{}
	count, err := replaceFDroidEntries(context.Background(), fake, nil, 250)
	if err != nil || count != 0 || fake.disabledSource != "fdroid" {
		t.Fatalf("count=%d err=%v disabled=%q, want 0, nil, fdroid", count, err, fake.disabledSource)
	}
}

func TestVerifySignedEntryJarAcceptsFDroidsRealSignature(t *testing.T) {
	jar, err := os.ReadFile("testdata/fdroid-entry.jar")
	if err != nil {
		t.Fatal(err)
	}
	entryJSON, err := verifySignedEntryJar(jar, fdroidRepoCertSHA256)
	if err != nil {
		t.Fatalf("F-Droid's own entry.jar was rejected: %v", err)
	}
	entry, err := parseFDroidEntry(entryJSON)
	if err != nil || entry.Index.Name != "/index-v2.json" || entry.Index.Size <= 0 {
		t.Fatalf("entry = %+v err=%v, want the index named with a size", entry, err)
	}

	if _, err := verifySignedEntryJar(jar, strings.Repeat("0", 64)); err == nil {
		t.Fatal("accepted a signature from a certificate other than the pinned one")
	}
	for name, change := range map[string]func(map[string][]byte){
		"entry.json": func(files map[string][]byte) {
			files["entry.json"] = bytes.Replace(files["entry.json"], []byte(`"sha256": "`), []byte(`"sha256": "0`), 1)
		},
		"manifest": func(files map[string][]byte) {
			files["META-INF/MANIFEST.MF"] = append(files["META-INF/MANIFEST.MF"], ' ')
		},
		"signature": func(files map[string][]byte) {
			files["META-INF/CIARANG.SF"] = append(files["META-INF/CIARANG.SF"], ' ')
		},
		"an extra file": func(files map[string][]byte) { files["index-v2.json"] = []byte("{}") },
		"no signature":  func(files map[string][]byte) { delete(files, "META-INF/CIARANG.RSA") },
	} {
		if _, err := verifySignedEntryJar(rewriteJar(t, jar, change), fdroidRepoCertSHA256); err == nil {
			t.Fatalf("accepted entry.jar with a changed %s", name)
		}
	}
}

func TestSyncSignedFetchesTheIndexOnlyWhenItChanges(t *testing.T) {
	key, cert := testSigningCertificate(t)
	certSum := sha256.Sum256(cert.Raw)
	pin := hex.EncodeToString(certSum[:])
	indexSum := fmt.Sprintf("%x", sha256.Sum256([]byte(testIndex)))
	entryAt := func(timestamp int64) []byte {
		return signTestEntryJar(t, key, cert, fmt.Sprintf(
			`{"timestamp": %d, "version": 30000, "index": {"name": "/index-v2.json", "sha256": "%s", "size": %d}}`,
			timestamp, indexSum, len(testIndex)))
	}
	jar := entryAt(2000)
	indexFetches := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/entry.jar":
			_, _ = w.Write(jar)
		case "/index-v2.json":
			indexFetches++
			_, _ = w.Write([]byte(testIndex))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	sync := func(last SignedSyncState) (int, SignedSyncState, error) {
		return syncSigned(context.Background(), &catalogSyncStore{upserted: 1}, server.Client(),
			server.URL+"/entry.jar", server.URL+"/index-v2.json", pin, last, 250)
	}

	count, state, err := sync(SignedSyncState{})
	if err != nil || count != 1 || state.IndexSHA256 != indexSum || state.Timestamp != 2000 || indexFetches != 1 {
		t.Fatalf("first sync count=%d state=%+v err=%v fetches=%d", count, state, err, indexFetches)
	}
	if count, _, err := sync(state); err != nil || count != 0 || indexFetches != 1 {
		t.Fatalf("unchanged index: count=%d err=%v fetches=%d, want no download", count, err, indexFetches)
	}
	jar = entryAt(1000)
	if _, _, err := sync(state); err == nil {
		t.Fatal("accepted an entry older than the last one synced")
	}
	jar = signTestEntryJar(t, key, cert, `{"timestamp": 3000, "index": {"name": "/index-v1.json", "sha256": "`+indexSum+`", "size": 10}}`)
	if _, _, err := sync(state); err == nil {
		t.Fatal("accepted an entry naming another index")
	}
}

// rewriteJar rebuilds a jar with its files changed by change.
func rewriteJar(t *testing.T, jar []byte, change func(map[string][]byte)) []byte {
	t.Helper()
	reader, err := zip.NewReader(bytes.NewReader(jar), int64(len(jar)))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{}
	var order []string
	for _, file := range reader.File {
		body, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(body)
		body.Close()
		if err != nil {
			t.Fatal(err)
		}
		files[file.Name] = data
		order = append(order, file.Name)
	}
	change(files)
	for name := range files {
		if !slices.Contains(order, name) {
			order = append(order, name)
		}
	}
	return writeJar(t, files, order)
}

func writeJar(t *testing.T, files map[string][]byte, order []string) []byte {
	t.Helper()
	var out bytes.Buffer
	writer := zip.NewWriter(&out)
	for _, name := range order {
		data, ok := files[name]
		if !ok {
			continue
		}
		w, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func testSigningCertificate(t *testing.T) (*rsa.PrivateKey, *x509.Certificate) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test repo"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return key, cert
}

// signTestEntryJar signs entry.json the way F-Droid does: a v1 JAR signature
// with a detached PKCS#7 SHA-256/RSA signature over the .SF file.
func signTestEntryJar(t *testing.T, key *rsa.PrivateKey, cert *x509.Certificate, entryJSON string) []byte {
	t.Helper()
	digest := func(data []byte) string {
		sum := sha256.Sum256(data)
		return base64.StdEncoding.EncodeToString(sum[:])
	}
	manifest := []byte("Manifest-Version: 1.0\r\n\r\nName: entry.json\r\nSHA-256-Digest: " + digest([]byte(entryJSON)) + "\r\n\r\n")
	signatureFile := []byte("Signature-Version: 1.0\r\nSHA-256-Digest-Manifest: " + digest(manifest) + "\r\n\r\n")
	sfDigest := sha256.Sum256(signatureFile)
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sfDigest[:])
	if err != nil {
		t.Fatal(err)
	}
	issuerAndSerial, err := asn1.Marshal(struct {
		Issuer asn1.RawValue
		Serial *big.Int
	}{asn1.RawValue{FullBytes: cert.RawIssuer}, cert.SerialNumber})
	if err != nil {
		t.Fatal(err)
	}
	sha256Algorithm := pkix.AlgorithmIdentifier{Algorithm: oidSHA256, Parameters: asn1.NullRawValue}
	signed, err := asn1.Marshal(pkcs7SignedData{
		Version:          1,
		DigestAlgorithms: []pkix.AlgorithmIdentifier{sha256Algorithm},
		ContentInfo:      pkcs7ContentInfo{ContentType: asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 1}},
		Certificates:     asn1.RawValue{Class: asn1.ClassContextSpecific, Tag: 0, IsCompound: true, Bytes: cert.Raw},
		SignerInfos: []pkcs7SignerInfo{{
			Version:                   1,
			IssuerAndSerialNumber:     asn1.RawValue{FullBytes: issuerAndSerial},
			DigestAlgorithm:           sha256Algorithm,
			DigestEncryptionAlgorithm: pkix.AlgorithmIdentifier{Algorithm: oidRSAEncryption, Parameters: asn1.NullRawValue},
			EncryptedDigest:           signature,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	block, err := asn1.Marshal(pkcs7ContentInfo{
		ContentType: oidPKCS7SignedData,
		Content:     asn1.RawValue{Class: asn1.ClassContextSpecific, Tag: 0, IsCompound: true, Bytes: signed},
	})
	if err != nil {
		t.Fatal(err)
	}
	return writeJar(t, map[string][]byte{
		"entry.json":           []byte(entryJSON),
		"META-INF/MANIFEST.MF": manifest,
		"META-INF/TEST.SF":     signatureFile,
		"META-INF/TEST.RSA":    block,
	}, []string{"entry.json", "META-INF/MANIFEST.MF", "META-INF/TEST.SF", "META-INF/TEST.RSA"})
}

func TestRepoURLRejectsUntrustedAndTraversalURLs(t *testing.T) {
	if got := repoURL("org.example.app_1.apk"); got != "https://f-droid.org/repo/org.example.app_1.apk" {
		t.Fatalf("repoURL(relative) = %q", got)
	}
	if got := repoURL("https://f-droid.org/repo/org.example.app_1.apk"); got != "https://f-droid.org/repo/org.example.app_1.apk" {
		t.Fatalf("repoURL(canonical) = %q", got)
	}
	for _, value := range []string{
		"http://f-droid.org/repo/app.apk",
		"https://example.com/repo/app.apk",
		"../app.apk",
		"//example.com/app.apk",
		"https://f-droid.org/repo/app.apk?download=1",
	} {
		if got := repoURL(value); got != "" {
			t.Fatalf("repoURL(%q) = %q, want empty", value, got)
		}
	}
}
