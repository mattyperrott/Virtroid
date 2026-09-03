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

func SyncFDroid(ctx context.Context, st Store, indexURL, expectedSHA256 string, maxApps int) (int, error) {
	pinnedSHA256, err := normalizeSHA256(expectedSHA256)
	if err != nil {
		return 0, fmt.Errorf("fdroid index pin: %w", err)
	}
	indexURL, err = validateIndexURL(indexURL)
	if err != nil {
		return 0, err
	}
	if maxApps <= 0 {
		maxApps = 1500
	}

	index, err := fetchPinnedIndex(ctx, fdroidHTTPClient(), indexURL, pinnedSHA256)
	if err != nil {
		return 0, err
	}

	return replaceFDroidIndex(ctx, st, index, maxApps)
}

func fdroidHTTPClient() *http.Client {
	return &http.Client{
		Timeout: 90 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func fetchPinnedIndex(ctx context.Context, client *http.Client, indexURL, pinnedSHA256 string) (fdroidIndex, error) {
	if client == nil {
		return fdroidIndex{}, errors.New("F-Droid HTTP client is required")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, indexURL, nil)
	if err != nil {
		return fdroidIndex{}, err
	}
	req.Header.Set("User-Agent", "virtroid-app-catalog/0.1")
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return fdroidIndex{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fdroidIndex{}, fmt.Errorf("fdroid index returned HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > maxIndexBytes {
		return fdroidIndex{}, fmt.Errorf("fdroid index exceeds %d-byte limit", maxIndexBytes)
	}

	index, err := decodePinnedIndex(resp.Body, pinnedSHA256)
	if err != nil {
		return fdroidIndex{}, err
	}
	return index, nil
}

func replaceFDroidIndex(ctx context.Context, st Store, index fdroidIndex, maxApps int) (int, error) {
	entries := buildEntries(index, maxApps)
	if len(entries) == 0 {
		_, err := st.DisableAppCatalogSource(ctx, "fdroid")
		return 0, err
	}
	return st.UpsertAppCatalogEntries(ctx, entries)
}

func validateIndexURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = defaultIndexURL
	}
	parsed, err := url.ParseRequestURI(raw)
	if err != nil || parsed.Scheme != "https" ||
		!strings.EqualFold(parsed.Hostname(), "f-droid.org") || parsed.Port() != "" ||
		parsed.User != nil || parsed.Path != "/repo/index-v2.json" ||
		parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("fdroid index URL must be exactly %s", defaultIndexURL)
	}
	return defaultIndexURL, nil
}

func decodePinnedIndex(reader io.Reader, expectedSHA256 string) (fdroidIndex, error) {
	expectedSHA256, err := normalizeSHA256(expectedSHA256)
	if err != nil {
		return fdroidIndex{}, fmt.Errorf("fdroid index pin: %w", err)
	}

	limited := &io.LimitedReader{R: reader, N: maxIndexBytes + 1}
	hash := sha256.New()
	decoder := json.NewDecoder(io.TeeReader(limited, hash))
	var index fdroidIndex
	if err := decoder.Decode(&index); err != nil {
		return fdroidIndex{}, fmt.Errorf("decode fdroid index: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fdroidIndex{}, fmt.Errorf("decode fdroid index: multiple JSON values")
		}
		return fdroidIndex{}, fmt.Errorf("decode fdroid index trailing data: %w", err)
	}
	if _, err := io.Copy(io.Discard, io.TeeReader(limited, hash)); err != nil {
		return fdroidIndex{}, fmt.Errorf("read fdroid index: %w", err)
	}
	readBytes := maxIndexBytes + 1 - limited.N
	if readBytes > maxIndexBytes {
		return fdroidIndex{}, fmt.Errorf("fdroid index exceeds %d-byte limit", maxIndexBytes)
	}
	actualSHA256 := fmt.Sprintf("%x", hash.Sum(nil))
	if actualSHA256 != expectedSHA256 {
		return fdroidIndex{}, fmt.Errorf("fdroid index SHA-256 mismatch")
	}
	return index, nil
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

func buildEntries(index fdroidIndex, maxApps int) []store.AppCatalogEntry {
	now := time.Now().UTC()
	entries := make([]store.AppCatalogEntry, 0, min(len(index.Packages), maxApps))
	for packageName, pkg := range index.Packages {
		version, ok := latestCompatibleVersion(pkg)
		if !ok {
			continue
		}

		displayName := localizedString(pkg.Metadata.Name, packageName)
		entry := store.AppCatalogEntry{
			PackageName:      packageName,
			Source:           "fdroid",
			DisplayName:      displayName,
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
		if entry.DisplayName == "" ||
			entry.APKURL == "" ||
			entry.APKSHA256 == "" ||
			entry.APKSizeBytes <= 0 {
			continue
		}
		entries = append(entries, entry)
	}

	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Recommended != entries[j].Recommended {
			return entries[i].Recommended
		}
		return strings.ToLower(entries[i].DisplayName) < strings.ToLower(entries[j].DisplayName)
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

type fdroidIndex struct {
	Packages map[string]fdroidPackage `json:"packages"`
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
