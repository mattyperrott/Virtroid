package appcatalog

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"virtroid/backend/internal/store"
)

type catalogSyncStore struct {
	disabledSource string
}

func (s *catalogSyncStore) UpsertAppCatalogEntries(context.Context, []store.AppCatalogEntry) (int, error) {
	return 0, nil
}

func (s *catalogSyncStore) DisableAppCatalogSource(_ context.Context, source string) (int, error) {
	s.disabledSource = source
	return 3, nil
}

func TestValidateIndexURLAllowsOnlyCanonicalFDroidIndex(t *testing.T) {
	for _, value := range []string{"", defaultIndexURL} {
		got, err := validateIndexURL(value)
		if err != nil {
			t.Fatalf("validateIndexURL(%q): %v", value, err)
		}
		if got != defaultIndexURL {
			t.Fatalf("validateIndexURL(%q) = %q, want %q", value, got, defaultIndexURL)
		}
	}

	for _, value := range []string{
		"http://f-droid.org/repo/index-v2.json",
		"https://example.com/repo/index-v2.json",
		"https://f-droid.org/repo/../index-v2.json",
		"https://f-droid.org/repo/index-v2.json?mirror=1",
		"https://user@f-droid.org/repo/index-v2.json",
	} {
		if _, err := validateIndexURL(value); err == nil {
			t.Fatalf("validateIndexURL(%q) unexpectedly succeeded", value)
		}
	}
}

func TestDecodePinnedIndexRequiresExactDigestAndSingleJSONValue(t *testing.T) {
	payload := `{"packages":{}}`
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(payload)))
	index, err := decodePinnedIndex(strings.NewReader(payload), digest)
	if err != nil {
		t.Fatalf("decodePinnedIndex: %v", err)
	}
	if index.Packages == nil || len(index.Packages) != 0 {
		t.Fatalf("decoded packages = %#v, want empty map", index.Packages)
	}

	if _, err := decodePinnedIndex(strings.NewReader(payload), strings.Repeat("0", 64)); err == nil || !strings.Contains(err.Error(), "mismatch") {
		t.Fatalf("wrong digest error = %v, want mismatch", err)
	}
	if _, err := decodePinnedIndex(strings.NewReader(payload+` {}`), digest); err == nil || !strings.Contains(err.Error(), "multiple JSON") {
		t.Fatalf("trailing JSON error = %v, want multiple JSON values", err)
	}
	if _, err := decodePinnedIndex(strings.NewReader(payload), ""); err == nil {
		t.Fatal("empty digest unexpectedly succeeded")
	}
}

func TestFetchPinnedIndexHandlesHTTPFailuresAndRejectsRedirects(t *testing.T) {
	payload := []byte(`{"packages":{}}`)
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

	if _, err := fetchPinnedIndex(context.Background(), fdroidHTTPClient(), server.URL+"/index-v2.json", digest); err != nil {
		t.Fatalf("fetch pinned index: %v", err)
	}
	if _, err := fetchPinnedIndex(context.Background(), fdroidHTTPClient(), server.URL+"/redirect", digest); err == nil || !strings.Contains(err.Error(), "HTTP 302") {
		t.Fatalf("redirect error = %v, want fail-closed status", err)
	}
	if _, err := fetchPinnedIndex(context.Background(), fdroidHTTPClient(), server.URL+"/unavailable", digest); err == nil || !strings.Contains(err.Error(), "HTTP 503") {
		t.Fatalf("server error = %v, want HTTP status", err)
	}
	if _, err := fetchPinnedIndex(context.Background(), fdroidHTTPClient(), server.URL+"/oversized", digest); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized error = %v, want size rejection", err)
	}
	if _, err := fetchPinnedIndex(context.Background(), nil, server.URL+"/index-v2.json", digest); err == nil {
		t.Fatal("nil HTTP client unexpectedly accepted")
	}
}

func TestBuildEntriesKeepsOnlyLatestRuntimeCompatibleArtifact(t *testing.T) {
	index := fdroidIndex{Packages: map[string]fdroidPackage{
		"org.example.compatible": {
			Metadata: fdroidMetadata{Name: map[string]string{"en-US": "Compatible"}},
			Versions: map[string]fdroidVersion{
				"old": {
					File:     fdroidFile{Name: "org.example.compatible_1.apk", SHA256: strings.Repeat("a", 64), Size: 100},
					Manifest: fdroidManifest{VersionName: "1", VersionCode: 1, UsesSDK: fdroidUsesSDK{MinSDKVersion: 28}},
				},
				"new": {
					File:     fdroidFile{Name: "org.example.compatible_2.apk", SHA256: strings.Repeat("b", 64), Size: 200},
					Manifest: fdroidManifest{VersionName: "2", VersionCode: 2, NativeCode: []string{"x86_64"}, UsesSDK: fdroidUsesSDK{MinSDKVersion: 34}},
				},
			},
		},
		"org.example.armonly": {
			Metadata: fdroidMetadata{Name: map[string]string{"en": "ARM only"}},
			Versions: map[string]fdroidVersion{"one": {
				File:     fdroidFile{Name: "org.example.armonly_1.apk", SHA256: strings.Repeat("c", 64), Size: 100},
				Manifest: fdroidManifest{VersionName: "1", VersionCode: 1, NativeCode: []string{"arm64-v8a"}},
			}},
		},
		"org.example.future": {
			Metadata: fdroidMetadata{Name: map[string]string{"en": "Future"}},
			Versions: map[string]fdroidVersion{"one": {
				File:     fdroidFile{Name: "org.example.future_1.apk", SHA256: strings.Repeat("d", 64), Size: 100},
				Manifest: fdroidManifest{VersionName: "1", VersionCode: 1, UsesSDK: fdroidUsesSDK{MinSDKVersion: runtimeMinSDK + 1}},
			}},
		},
	}}

	entries := buildEntries(index, 250)
	if len(entries) != 1 {
		t.Fatalf("entries = %+v, want one compatible package", entries)
	}
	if entries[0].PackageName != "org.example.compatible" || entries[0].VersionCode != 2 {
		t.Fatalf("selected entry = %+v, want latest compatible version", entries[0])
	}
}

func TestSyncFDroidDisablesStaleSourceForEmptyPinnedIndex(t *testing.T) {
	payload := `{"packages":{}}`
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(payload)))
	index, err := decodePinnedIndex(strings.NewReader(payload), digest)
	if err != nil {
		t.Fatalf("decodePinnedIndex: %v", err)
	}
	fake := &catalogSyncStore{}
	count, err := replaceFDroidIndex(context.Background(), fake, index, 250)
	if err != nil {
		t.Fatalf("replaceFDroidIndex: %v", err)
	}
	if count != 0 {
		t.Fatalf("replace count = %d, want 0", count)
	}
	if fake.disabledSource != "fdroid" {
		t.Fatalf("disabled source = %q, want fdroid", fake.disabledSource)
	}
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
