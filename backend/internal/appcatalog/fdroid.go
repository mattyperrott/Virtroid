package appcatalog

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"
	"time"

	"virtroid/backend/internal/store"
)

const (
	defaultIndexURL = "https://f-droid.org/repo/index-v2.json"
	repoBaseURL     = "https://f-droid.org/repo"
	maxIndexBytes   = 256 << 20
	runtimeMinSDK   = 34
	// defaultMaxApps keeps every runtime-compatible F-Droid app; the app
	// pages through the catalog.
	defaultMaxApps = 10000
)

var recommendedPackages = map[string]struct{}{
	"app.organicmaps":           {},
	"com.beemdevelopment.aegis": {},
	"org.mozilla.fennec_fdroid": {},
	"org.schabi.newpipe":        {},
	"org.videolan.vlc":          {},
}

type Store interface {
	UpsertAppCatalogEntries(context.Context, []store.AppCatalogEntry) (int, error)
	DisableAppCatalogSource(context.Context, string) (int, error)
}

// fdroidHTTPClient never follows redirects. Its requests end with their
// context: the index is tens of megabytes, so it gets the sync's whole budget.
func fdroidHTTPClient() *http.Client {
	return &http.Client{
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// fetchPinnedEntries downloads index-v2.json and returns its catalog
// entries once the whole file matches the pinned SHA-256.
func fetchPinnedEntries(ctx context.Context, client *http.Client, indexURL, pinnedSHA256 string) ([]store.AppCatalogEntry, error) {
	if client == nil {
		return nil, errors.New("F-Droid HTTP client is required")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, indexURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "virtroid-app-catalog/0.2")
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("fdroid index returned HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > maxIndexBytes {
		return nil, fmt.Errorf("fdroid index exceeds %d-byte limit", maxIndexBytes)
	}
	return decodePinnedEntries(resp.Body, pinnedSHA256)
}

func replaceFDroidEntries(ctx context.Context, st Store, entries []store.AppCatalogEntry, maxApps int) (int, error) {
	entries = sortAndCapEntries(entries, maxApps)
	if len(entries) == 0 {
		_, err := st.DisableAppCatalogSource(ctx, "fdroid")
		return 0, err
	}
	return st.UpsertAppCatalogEntries(ctx, entries)
}

// decodePinnedEntries streams index-v2.json one package at a time, keeping
// only each package's catalog entry, so the large index never sits in memory
// whole. Nothing is returned unless the bytes match expectedSHA256.
func decodePinnedEntries(reader io.Reader, expectedSHA256 string) ([]store.AppCatalogEntry, error) {
	expectedSHA256, err := normalizeSHA256(expectedSHA256)
	if err != nil {
		return nil, fmt.Errorf("fdroid index pin: %w", err)
	}

	limited := &io.LimitedReader{R: reader, N: maxIndexBytes + 1}
	hash := sha256.New()
	decoder := json.NewDecoder(io.TeeReader(limited, hash))
	now := time.Now().UTC()
	var entries []store.AppCatalogEntry
	if err := expectDelim(decoder, '{'); err != nil {
		return nil, err
	}
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return nil, fmt.Errorf("decode fdroid index: %w", err)
		}
		if key != "packages" {
			var skip json.RawMessage
			if err := decoder.Decode(&skip); err != nil {
				return nil, fmt.Errorf("decode fdroid index: %w", err)
			}
			continue
		}
		if err := expectDelim(decoder, '{'); err != nil {
			return nil, err
		}
		for decoder.More() {
			name, err := decoder.Token()
			if err != nil {
				return nil, fmt.Errorf("decode fdroid index: %w", err)
			}
			packageName, ok := name.(string)
			if !ok {
				return nil, errors.New("decode fdroid index: package name is not a string")
			}
			var pkg fdroidPackage
			if err := decoder.Decode(&pkg); err != nil {
				return nil, fmt.Errorf("decode fdroid package %s: %w", packageName, err)
			}
			if entry, ok := entryForPackage(packageName, pkg, now); ok {
				entries = append(entries, entry)
			}
		}
		if err := expectDelim(decoder, '}'); err != nil {
			return nil, err
		}
	}
	if err := expectDelim(decoder, '}'); err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, errors.New("decode fdroid index: trailing data")
	}
	if _, err := io.Copy(io.Discard, io.TeeReader(limited, hash)); err != nil {
		return nil, fmt.Errorf("read fdroid index: %w", err)
	}
	if maxIndexBytes+1-limited.N > maxIndexBytes {
		return nil, fmt.Errorf("fdroid index exceeds %d-byte limit", maxIndexBytes)
	}
	if fmt.Sprintf("%x", hash.Sum(nil)) != expectedSHA256 {
		return nil, fmt.Errorf("fdroid index SHA-256 mismatch")
	}
	return entries, nil
}

func expectDelim(decoder *json.Decoder, want json.Delim) error {
	token, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("decode fdroid index: %w", err)
	}
	if delim, ok := token.(json.Delim); !ok || delim != want {
		return fmt.Errorf("decode fdroid index: expected %q", want)
	}
	return nil
}

func normalizeSHA256(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if len(value) != sha256.Size*2 {
		return "", fmt.Errorf("must contain exactly %d hexadecimal characters", sha256.Size*2)
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return "", fmt.Errorf("must be hexadecimal")
		}
	}
	return value, nil
}

// entryForPackage is the catalog entry for a package's latest version the
// runtime can run, if it has one.
func entryForPackage(packageName string, pkg fdroidPackage, now time.Time) (store.AppCatalogEntry, bool) {
	version, ok := latestCompatibleVersion(pkg)
	if !ok {
		return store.AppCatalogEntry{}, false
	}
	entry := store.AppCatalogEntry{
		PackageName:      packageName,
		Source:           "fdroid",
		DisplayName:      localizedString(pkg.Metadata.Name, packageName),
		Summary:          localizedString(pkg.Metadata.Summary, ""),
		IconURL:          repoURL(localizedFile(pkg.Metadata.Icon).Name),
		VersionName:      strings.TrimSpace(version.Manifest.VersionName),
		VersionCode:      version.Manifest.VersionCode,
		APKURL:           repoURL(version.File.Name),
		APKSHA256:        strings.TrimSpace(version.File.SHA256),
		APKSizeBytes:     version.File.Size,
		MinSDK:           version.Manifest.UsesSDK.MinSDKVersion,
		NativeCode:       strings.Join(version.Manifest.NativeCode, ","),
		License:          strings.TrimSpace(pkg.Metadata.License),
		CategoriesJSON:   jsonString(pkg.Metadata.Categories),
		AntiFeaturesJSON: jsonString(pkg.Metadata.AntiFeatures),
		Recommended:      recommended(packageName),
		CatalogUpdatedAt: now,
	}
	if entry.DisplayName == "" || entry.APKURL == "" || entry.APKSHA256 == "" || entry.APKSizeBytes <= 0 {
		return store.AppCatalogEntry{}, false
	}
	return entry, true
}

// sortAndCapEntries puts recommended apps first, then the rest by name, and
// keeps at most maxApps.
func sortAndCapEntries(entries []store.AppCatalogEntry, maxApps int) []store.AppCatalogEntry {
	if maxApps <= 0 {
		maxApps = defaultMaxApps
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Recommended != entries[j].Recommended {
			return entries[i].Recommended
		}
		left, right := strings.ToLower(entries[i].DisplayName), strings.ToLower(entries[j].DisplayName)
		if left != right {
			return left < right
		}
		return entries[i].PackageName < entries[j].PackageName
	})
	if len(entries) > maxApps {
		entries = entries[:maxApps]
	}
	return entries
}

func latestCompatibleVersion(pkg fdroidPackage) (fdroidVersion, bool) {
	var best fdroidVersion
	found := false
	for _, version := range pkg.Versions {
		if !compatibleVersion(version) {
			continue
		}
		if !found || version.Manifest.VersionCode > best.Manifest.VersionCode {
			best = version
			found = true
		}
	}
	return best, found
}

func compatibleVersion(version fdroidVersion) bool {
	if strings.TrimSpace(version.File.Name) == "" ||
		strings.TrimSpace(version.File.SHA256) == "" ||
		version.File.Size <= 0 ||
		strings.TrimSpace(version.Manifest.VersionName) == "" {
		return false
	}
	minSDK := version.Manifest.UsesSDK.MinSDKVersion
	if minSDK > runtimeMinSDK {
		return false
	}
	nativeCode := version.Manifest.NativeCode
	if len(nativeCode) == 0 {
		return true
	}
	for _, abi := range nativeCode {
		if strings.EqualFold(strings.TrimSpace(abi), "x86_64") {
			return true
		}
	}
	return false
}

func localizedString(values map[string]string, fallback string) string {
	for _, key := range []string{"en-US", "en", "en-GB"} {
		if value := strings.TrimSpace(values[key]); value != "" {
			return value
		}
	}
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return strings.TrimSpace(fallback)
}

func localizedFile(values map[string]fdroidFile) fdroidFile {
	for _, key := range []string{"en-US", "en", "en-GB"} {
		if file := values[key]; strings.TrimSpace(file.Name) != "" {
			return file
		}
	}
	for _, file := range values {
		if strings.TrimSpace(file.Name) != "" {
			return file
		}
	}
	return fdroidFile{}
}

func repoURL(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	parsed, err := url.Parse(name)
	if err != nil || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return ""
	}
	if parsed.IsAbs() {
		if parsed.Scheme != "https" || !strings.EqualFold(parsed.Hostname(), "f-droid.org") ||
			parsed.Port() != "" || !strings.HasPrefix(parsed.Path, "/repo/") {
			return ""
		}
		cleaned := path.Clean(parsed.Path)
		if !strings.HasPrefix(cleaned, "/repo/") || cleaned == "/repo" {
			return ""
		}
		parsed.Path = cleaned
		parsed.RawPath = ""
		return parsed.String()
	}
	if parsed.Host != "" || strings.HasPrefix(name, "//") {
		return ""
	}
	cleaned := path.Clean(strings.TrimPrefix(parsed.Path, "/"))
	if cleaned == "." || cleaned == "" || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return ""
	}
	result := &url.URL{Scheme: "https", Host: "f-droid.org", Path: "/repo/" + cleaned}
	return result.String()
}

func recommended(packageName string) bool {
	_, ok := recommendedPackages[packageName]
	return ok
}

func jsonString(value any) string {
	data, err := json.Marshal(value)
	if err != nil || len(data) == 0 || string(data) == "null" {
		return "[]"
	}
	return string(data)
}

type fdroidPackage struct {
	Metadata fdroidMetadata           `json:"metadata"`
	Versions map[string]fdroidVersion `json:"versions"`
}

type fdroidMetadata struct {
	Name         map[string]string     `json:"name"`
	Summary      map[string]string     `json:"summary"`
	Icon         map[string]fdroidFile `json:"icon"`
	License      string                `json:"license"`
	Categories   []string              `json:"categories"`
	AntiFeatures map[string]any        `json:"antiFeatures"`
}

type fdroidVersion struct {
	File     fdroidFile     `json:"file"`
	Manifest fdroidManifest `json:"manifest"`
}

type fdroidFile struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type fdroidManifest struct {
	VersionName string        `json:"versionName"`
	VersionCode int64         `json:"versionCode"`
	NativeCode  []string      `json:"nativecode"`
	UsesSDK     fdroidUsesSDK `json:"usesSdk"`
}

type fdroidUsesSDK struct {
	MinSDKVersion int `json:"minSdkVersion"`
}
