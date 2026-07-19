package appcatalog

import (
	"context"
	"crypto/sha256"
	"fmt"
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
