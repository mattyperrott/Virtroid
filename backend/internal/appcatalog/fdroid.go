package appcatalog

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
}

func SyncFDroid(ctx context.Context, st Store, indexURL string, maxApps int) (int, error) {
	indexURL = strings.TrimSpace(indexURL)
	if indexURL == "" {
		indexURL = defaultIndexURL
	}
	if maxApps <= 0 {
		maxApps = 1500
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, indexURL, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", "virtroid-app-catalog/0.1")

	client := &http.Client{Timeout: 90 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0, fmt.Errorf("fdroid index returned HTTP %d", resp.StatusCode)
	}

	var index fdroidIndex
	decoder := json.NewDecoder(io.LimitReader(resp.Body, maxIndexBytes))
	if err := decoder.Decode(&index); err != nil {
		return 0, fmt.Errorf("decode fdroid index: %w", err)
	}

	entries := buildEntries(index, maxApps)
	return st.UpsertAppCatalogEntries(ctx, entries)
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
	if strings.HasPrefix(name, "http://") || strings.HasPrefix(name, "https://") {
		return name
	}
	return repoBaseURL + "/" + strings.TrimPrefix(name, "/")
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
