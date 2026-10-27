package main

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"os"
	"strconv"
	"strings"
)

type personaTemplate struct {
	Brand        string
	Manufacturer string
	Model        string
	Device       string
	Product      string
	// FingerprintProduct is the product the fingerprint names where it is
	// not ro.product.name, as on Sony's regional builds.
	FingerprintProduct string
	// Builds are real builds the phone shipped, one per Android release it
	// got, so a persona claims only releases and builds the phone ran.
	Builds map[string]personaBuild
}

// personaBuild is a build as the phone's own properties show it. DisplayID
// and Description are empty where the vendor keeps Android's defaults: the
// build ID, and "<product>-user <release> <id> <incremental> release-keys".
type personaBuild struct {
	ID          string
	DisplayID   string
	Incremental string
	Description string
}

type sessionPersona struct {
	Version      int    `json:"version"`
	Brand        string `json:"brand"`
	Manufacturer string `json:"manufacturer"`
	Model        string `json:"model"`
	Device       string `json:"device"`
	Product      string `json:"product"`
	BuildID      string `json:"build_id"`
	DisplayID    string `json:"display_id"`
	Incremental  string `json:"incremental"`
	Release      string `json:"release"`
	Fingerprint  string `json:"fingerprint"`
	Description  string `json:"description"`
	Serial       string `json:"serial"`
}

// The newest Android 16 builds confirmed for the Pixels, which moved to
// Android 17 in June 2026: April 2026's for the Pixel 6 and 7, May's for
// the rest.
var (
	pixelApril2026 = personaBuild{ID: "CP1A.260405.005", Incremental: "15001963"}
	pixelMay2026   = personaBuild{ID: "CP1A.260505.005", Incremental: "15081906"}
)

// personaCatalog's builds are each phone's latest Android 16 build we could
// confirm from its own records: Google's OTA metadata, getprop dumps, stock
// fingerprints in LineageOS device trees and vendor firmware lists, most
// also seen in the GPSTest database. gnss_receiver.go names each one's GNSS
// chip.
var personaCatalog = []personaTemplate{
	{Brand: "google", Manufacturer: "Google", Model: "Pixel 6", Device: "oriole", Product: "oriole",
		Builds: map[string]personaBuild{"16": pixelApril2026}},
	{Brand: "google", Manufacturer: "Google", Model: "Pixel 7", Device: "panther", Product: "panther",
		Builds: map[string]personaBuild{"16": pixelApril2026}},
	{Brand: "google", Manufacturer: "Google", Model: "Pixel 8", Device: "shiba", Product: "shiba",
		Builds: map[string]personaBuild{"16": pixelMay2026}},
	{Brand: "google", Manufacturer: "Google", Model: "Pixel 8 Pro", Device: "husky", Product: "husky",
		Builds: map[string]personaBuild{"16": pixelMay2026}},
	{Brand: "google", Manufacturer: "Google", Model: "Pixel 8a", Device: "akita", Product: "akita",
		Builds: map[string]personaBuild{"16": pixelMay2026}},
	{Brand: "google", Manufacturer: "Google", Model: "Pixel 9", Device: "tokay", Product: "tokay",
		Builds: map[string]personaBuild{"16": pixelMay2026}},
	{Brand: "google", Manufacturer: "Google", Model: "Pixel 9 Pro", Device: "caiman", Product: "caiman",
		Builds: map[string]personaBuild{"16": pixelMay2026}},
	// Samsung's description names the open-market product of an EEA one.
	{Brand: "samsung", Manufacturer: "samsung", Model: "SM-S901B", Device: "r0s", Product: "r0sxeea",
		Builds: map[string]personaBuild{"16": {ID: "BP2A.250605.031.A3", DisplayID: "BP2A.250605.031.A3.S901BXXSNGZD7",
			Incremental: "S901BXXSNGZD7", Description: "r0sxxx-user 16 BP2A.250605.031.A3 S901BXXSNGZD7 release-keys"}}},
	{Brand: "samsung", Manufacturer: "samsung", Model: "SM-S918B", Device: "dm3q", Product: "dm3qxxx",
		Builds: map[string]personaBuild{"16": {ID: "BP4A.251205.006", DisplayID: "BP4A.251205.006.S918BXXSAFZG1",
			Incremental: "S918BXXSAFZG1"}}},
	{Brand: "samsung", Manufacturer: "samsung", Model: "SM-S928B", Device: "e3q", Product: "e3qxxx",
		Builds: map[string]personaBuild{"16": {ID: "BP4A.251205.006", DisplayID: "BP4A.251205.006.S928BXXS6DZI1",
			Incremental: "S928BXXS6DZI1"}}},
	{Brand: "samsung", Manufacturer: "samsung", Model: "SM-A556B", Device: "a55x", Product: "a55xnaeea",
		Builds: map[string]personaBuild{"16": {ID: "BP4A.251205.006", DisplayID: "BP4A.251205.006.A556BXXUEDZE4",
			Incremental: "A556BXXUEDZE4", Description: "a55xnaxx-user 16 BP4A.251205.006 A556BXXUEDZE4 release-keys"}}},
	{Brand: "Redmi", Manufacturer: "Xiaomi", Model: "2312DRA50G", Device: "garnet", Product: "garnet_global",
		Builds: map[string]personaBuild{"16": {ID: "BP2A.250605.031.A3", Incremental: "OS3.0.302.0.WNRMIXM",
			Description: "missi-user 16 BP2A.250605.031.A3 OS3.0.302.0.WNRMIXM release-keys"}}},
	// OnePlus's description is its system image's, stamped with the build
	// time in milliseconds.
	{Brand: "OnePlus", Manufacturer: "OnePlus", Model: "CPH2581", Device: "OP595DL1", Product: "CPH2581",
		Builds: map[string]personaBuild{"16": {ID: "BP2A.250605.015", DisplayID: "CPH2581_16.0.10.501(EX01)",
			Incremental: "U.R4T3.1ee2725_642f40_642bb6", Description: "qssi_64-user 16 BP2A.250605.015 1786422211127 release-keys"}}},
	{Brand: "OnePlus", Manufacturer: "OnePlus", Model: "CPH2653", Device: "OP5D55L1", Product: "CPH2653EEA",
		Builds: map[string]personaBuild{"16": {ID: "BP2A.250605.015", DisplayID: "CPH2653_16.0.10.600(EX01)",
			Incremental: "V.R4T3.3a5f42a-1481288-1481284", Description: "qssi_64-user 16 BP2A.250605.015 1789356131451 release-keys"}}},
	{Brand: "motorola", Manufacturer: "motorola", Model: "motorola edge 60 pro", Device: "cybert", Product: "cybert_g_syse",
		Builds: map[string]personaBuild{"16": {ID: "W1VVS36H.7-108-8-8", Incremental: "856a5e-59f4b"}}},
	{Brand: "Sony", Manufacturer: "Sony", Model: "XQ-EC54", Device: "XQ-EC54", Product: "XQ-EC54", FingerprintProduct: "XQ-EC54_EEA",
		Builds: map[string]personaBuild{"16": {ID: "69.2.A.4.90", DisplayID: "69.2.A.4.90 release-keys",
			Incremental: "069002A004009001747781348"}}},
	{Brand: "Nothing", Manufacturer: "Nothing", Model: "A065", Device: "Pong", Product: "Pong",
		Builds: map[string]personaBuild{"16": {ID: "BQ2A.250721.001-BP2A.250605.031.A3", DisplayID: "B4.1-260818-1726",
			Incremental: "2608181726", Description: "qssi_64-user 16 BQ2A.250721.001-BP2A.250605.031.A3 2608181726 release-keys"}}},
}

func buildSessionPersona(runtime runtimeAssignment) sessionPersona {
	seed := fmt.Sprintf("%s:%d:%s", runtime.ID, runtime.PersonaVersion, runtime.AndroidVersion)
	release := runtimeAndroidRelease(runtime.AndroidVersion)
	phones := personaPhonesOn(release)
	template := phones[int(stableHash(seed)%uint32(len(phones)))]
	build, ok := template.Builds[release]
	if !ok {
		build = genericBuild(release, seed)
	}
	return newSessionPersona(template, build, release, runtime.PersonaVersion, strings.ToUpper(base36Token(stableHash(seed+":serial"), 12)))
}

// personaPhonesOn is the catalog's phones that got the release. Should the
// image run a release none of them has a build for, every phone claims it
// with a generic build.
func personaPhonesOn(release string) []personaTemplate {
	var phones []personaTemplate
	for _, template := range personaCatalog {
		if _, ok := template.Builds[release]; ok {
			phones = append(phones, template)
		}
	}
	if len(phones) == 0 {
		return personaCatalog
	}
	return phones
}

// genericBuild is a Pixel-shaped build: an AOSP build ID for the release
// and an 8-digit incremental.
func genericBuild(release, seed string) personaBuild {
	return personaBuild{
		ID:          buildIDForRelease(release, stableHash(seed+":build")),
		Incremental: strconv.FormatUint(uint64(10000000+stableHash(seed+":inc")%90000000), 10),
	}
}

func newSessionPersona(template personaTemplate, build personaBuild, release string, version int, serial string) sessionPersona {
	product := template.FingerprintProduct
	if product == "" {
		product = template.Product
	}
	displayID := build.DisplayID
	if displayID == "" {
		displayID = build.ID
	}
	description := build.Description
	if description == "" {
		description = fmt.Sprintf("%s-user %s %s %s release-keys", product, release, build.ID, build.Incremental)
	}
	// Fingerprints keep the brand's own case: OnePlus/, Redmi/, Sony/.
	fingerprint := fmt.Sprintf(
		"%s/%s/%s:%s/%s/%s:user/release-keys",
		template.Brand,
		product,
		template.Device,
		release,
		build.ID,
		build.Incremental,
	)

	return sessionPersona{
		Version:      version,
		Brand:        template.Brand,
		Manufacturer: template.Manufacturer,
		Model:        template.Model,
		Device:       template.Device,
		Product:      template.Product,
		BuildID:      build.ID,
		DisplayID:    displayID,
		Incremental:  build.Incremental,
		Release:      release,
		Fingerprint:  fingerprint,
		Description:  description,
		Serial:       serial,
	}
}

func personaOverrideProps(persona sessionPersona) []string {
	return []string{
		"ro.product.brand=" + persona.Brand,
		"ro.product.manufacturer=" + persona.Manufacturer,
		"ro.product.model=" + persona.Model,
		"ro.product.device=" + persona.Device,
		"ro.product.name=" + persona.Product,
		"ro.build.product=" + persona.Product,
		"ro.build.id=" + persona.BuildID,
		"ro.build.display.id=" + persona.DisplayID,
		"ro.build.version.incremental=" + persona.Incremental,
		"ro.build.fingerprint=" + persona.Fingerprint,
		"ro.build.description=" + persona.Description,
		"ro.serialno=" + persona.Serial,
	}
}

func marshalSessionPersona(persona sessionPersona) string {
	payload, err := json.Marshal(persona)
	if err != nil {
		return ""
	}
	return string(payload)
}

func personaSummary(persona sessionPersona) string {
	return fmt.Sprintf(
		"%s %s [%s/%s] release=%s fingerprint=%s",
		persona.Manufacturer,
		persona.Model,
		persona.Product,
		persona.Device,
		persona.Release,
		persona.Fingerprint,
	)
}

func stableHash(value string) uint32 {
	hasher := fnv.New32a()
	_, _ = hasher.Write([]byte(value))
	return hasher.Sum32()
}

func androidRelease(version string) string {
	version = strings.TrimSpace(strings.TrimPrefix(version, "android-"))
	if version == "" {
		return "12"
	}
	return version
}

// runtimeAndroidRelease is the release the persona claims: the one the VM's
// measured Android image runs (NODE_RUNTIME_ANDROID_RELEASE), since the
// control plane's per-runtime version is only a label.
func runtimeAndroidRelease(version string) string {
	if release := strings.TrimSpace(os.Getenv("NODE_RUNTIME_ANDROID_RELEASE")); release != "" {
		return release
	}
	return androidRelease(version)
}

// buildIDForRelease is a generic build ID for genericBuild; it names no
// real build.
func buildIDForRelease(release string, seed uint32) string {
	prefix, date := "SP1A", "220624"
	switch release {
	case "13":
		prefix = "TQ3A"
	case "14":
		prefix = "UP1A"
	case "15":
		prefix = "AP3A"
	}
	return fmt.Sprintf("%s.%s.%03d", prefix, date, 100+seed%900)
}

func base36Token(seed uint32, width int) string {
	const alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	if width <= 0 {
		width = 8
	}
	value := uint64(seed)
	buffer := make([]byte, width)
	for index := width - 1; index >= 0; index-- {
		buffer[index] = alphabet[value%36]
		value = value/36 + 17
	}
	return string(buffer)
}
