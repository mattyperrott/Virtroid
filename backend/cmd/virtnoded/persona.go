package main

import (
	"cmp"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
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
	// Builds are real builds the phone shipped, keyed by Android release, so
	// a persona claims only releases and builds the phone ran. A release may
	// list several; a persona takes the one closest to the image's patch.
	Builds map[string][]personaBuild
}

// personaBuild is a build as the phone's own properties show it. DisplayID
// and Description are empty where the vendor keeps Android's defaults: the
// build ID, and "<product>-user <release> <id> <incremental> release-keys".
type personaBuild struct {
	ID          string
	DisplayID   string
	Incremental string
	Description string
	// SecurityPatch (YYYY-MM-DD) is the build's patch level. Personas do not
	// set ro.build.version.security_patch — the image keeps its own — but it
	// picks, per phone, the build closest to the image's patch.
	SecurityPatch string
	// DateUTC is the build's timestamp (ro.build.date and ro.build.date.utc)
	// in Unix seconds, so Build.TIME reads the phone's build date, not the
	// image's.
	DateUTC int64
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
	BuildDate    string `json:"build_date"`
	BuildDateUTC int64  `json:"build_date_utc"`
	BuildHost    string `json:"build_host"`
	BuildUser    string `json:"build_user"`
	Serial       string `json:"serial"`
}

// The newest Android 16 builds confirmed for the Pixels, which moved to
// Android 17 in June 2026: April 2026's for the Pixel 6 and 7, May's for
// the rest.
var (
	pixelApril2026 = personaBuild{ID: "CP1A.260405.005", Incremental: "15001963", SecurityPatch: "2026-04-05", DateUTC: 1772841600}
	pixelMay2026   = personaBuild{ID: "CP1A.260505.005", Incremental: "15081906", SecurityPatch: "2026-05-05", DateUTC: 1774392233}
)

// personaCatalog's builds are each phone's latest Android 16 build we could
// confirm from its own records: Google's OTA metadata, getprop dumps, stock
// fingerprints in LineageOS device trees and vendor firmware lists, most
// also seen in the GPSTest database. gnss_receiver.go names each one's GNSS
// chip.
var personaCatalog = []personaTemplate{
	{Brand: "google", Manufacturer: "Google", Model: "Pixel 6", Device: "oriole", Product: "oriole",
		Builds: map[string][]personaBuild{"16": {pixelApril2026}}},
	{Brand: "google", Manufacturer: "Google", Model: "Pixel 7", Device: "panther", Product: "panther",
		Builds: map[string][]personaBuild{"16": {pixelApril2026}}},
	{Brand: "google", Manufacturer: "Google", Model: "Pixel 8", Device: "shiba", Product: "shiba",
		Builds: map[string][]personaBuild{"16": {pixelMay2026}}},
	{Brand: "google", Manufacturer: "Google", Model: "Pixel 8 Pro", Device: "husky", Product: "husky",
		Builds: map[string][]personaBuild{"16": {pixelMay2026}}},
	{Brand: "google", Manufacturer: "Google", Model: "Pixel 8a", Device: "akita", Product: "akita",
		Builds: map[string][]personaBuild{"16": {pixelMay2026}}},
	{Brand: "google", Manufacturer: "Google", Model: "Pixel 9", Device: "tokay", Product: "tokay",
		Builds: map[string][]personaBuild{"16": {pixelMay2026}}},
	{Brand: "google", Manufacturer: "Google", Model: "Pixel 9 Pro", Device: "caiman", Product: "caiman",
		Builds: map[string][]personaBuild{"16": {pixelMay2026}}},
	// Samsung's description names the open-market product of an EEA one. The
	// build dates are first field-sightings of the exact incremental; hosts
	// and users are a representative Samsung build's.
	{Brand: "samsung", Manufacturer: "samsung", Model: "SM-S901B", Device: "r0s", Product: "r0sxeea",
		Builds: map[string][]personaBuild{"16": {{ID: "BP2A.250605.031.A3", DisplayID: "BP2A.250605.031.A3.S901BXXSNGZD7",
			Incremental: "S901BXXSNGZD7", Description: "r0sxxx-user 16 BP2A.250605.031.A3 S901BXXSNGZD7 release-keys",
			SecurityPatch: "2026-09-01", DateUTC: 1788459605}}}},
	{Brand: "samsung", Manufacturer: "samsung", Model: "SM-S918B", Device: "dm3q", Product: "dm3qxxx",
		Builds: map[string][]personaBuild{"16": {{ID: "BP4A.251205.006", DisplayID: "BP4A.251205.006.S918BXXSAFZG1",
			Incremental: "S918BXXSAFZG1", SecurityPatch: "2026-09-01", DateUTC: 1785844829}}}},
	{Brand: "samsung", Manufacturer: "samsung", Model: "SM-S928B", Device: "e3q", Product: "e3qxxx",
		Builds: map[string][]personaBuild{"16": {{ID: "BP4A.251205.006", DisplayID: "BP4A.251205.006.S928BXXS6DZI1",
			Incremental: "S928BXXS6DZI1", SecurityPatch: "2026-10-01", DateUTC: 1791382741}}}},
	{Brand: "samsung", Manufacturer: "samsung", Model: "SM-A556B", Device: "a55x", Product: "a55xnaeea",
		Builds: map[string][]personaBuild{"16": {{ID: "BP4A.251205.006", DisplayID: "BP4A.251205.006.A556BXXUEDZE4",
			Incremental: "A556BXXUEDZE4", Description: "a55xnaxx-user 16 BP4A.251205.006 A556BXXUEDZE4 release-keys",
			SecurityPatch: "2026-06-01", DateUTC: 1782864000}}}},
	{Brand: "Redmi", Manufacturer: "Xiaomi", Model: "2312DRA50G", Device: "garnet", Product: "garnet_global",
		Builds: map[string][]personaBuild{"16": {{ID: "BP2A.250605.031.A3", Incremental: "OS3.0.302.0.WNRMIXM",
			Description:   "missi-user 16 BP2A.250605.031.A3 OS3.0.302.0.WNRMIXM release-keys",
			SecurityPatch: "2026-09-01", DateUTC: 1788956263}}}},
	// OnePlus's description is its system image's, stamped with the build time
	// in milliseconds; the build date is that stamp.
	{Brand: "OnePlus", Manufacturer: "OnePlus", Model: "CPH2581", Device: "OP595DL1", Product: "CPH2581",
		Builds: map[string][]personaBuild{"16": {{ID: "BP2A.250605.015", DisplayID: "CPH2581_16.0.10.501(EX01)",
			Incremental: "U.R4T3.1ee2725_642f40_642bb6", Description: "qssi_64-user 16 BP2A.250605.015 1786422211127 release-keys",
			SecurityPatch: "2026-08-01", DateUTC: 1786422211}}}},
	{Brand: "OnePlus", Manufacturer: "OnePlus", Model: "CPH2653", Device: "OP5D55L1", Product: "CPH2653EEA",
		Builds: map[string][]personaBuild{"16": {{ID: "BP2A.250605.015", DisplayID: "CPH2653_16.0.10.600(EX01)",
			Incremental: "V.R4T3.3a5f42a-1481288-1481284", Description: "qssi_64-user 16 BP2A.250605.015 1789356131451 release-keys",
			SecurityPatch: "2026-09-01", DateUTC: 1789356131}}}},
	{Brand: "motorola", Manufacturer: "motorola", Model: "motorola edge 60 pro", Device: "cybert", Product: "cybert_g_syse",
		Builds: map[string][]personaBuild{"16": {{ID: "W1VVS36H.7-108-8-8", Incremental: "856a5e-59f4b",
			SecurityPatch: "2026-08-01", DateUTC: 1787738400}}}},
	{Brand: "Sony", Manufacturer: "Sony", Model: "XQ-EC54", Device: "XQ-EC54", Product: "XQ-EC54", FingerprintProduct: "XQ-EC54_EEA",
		Builds: map[string][]personaBuild{"16": {{ID: "69.2.A.4.90", DisplayID: "69.2.A.4.90 release-keys",
			Incremental: "069002A004009001747781348", SecurityPatch: "2026-07-01", DateUTC: 1784851200}}}},
	// Nothing's incremental is the build time (yymmddhhmm, China time), which
	// is also the build date.
	{Brand: "Nothing", Manufacturer: "Nothing", Model: "A065", Device: "Pong", Product: "Pong",
		Builds: map[string][]personaBuild{"16": {{ID: "BQ2A.250721.001-BP2A.250605.031.A3", DisplayID: "B4.1-260818-1726",
			Incremental: "2608181726", Description: "qssi_64-user 16 BQ2A.250721.001-BP2A.250605.031.A3 2608181726 release-keys",
			SecurityPatch: "2026-08-01", DateUTC: 1787045160}}}},
}

func buildSessionPersona(runtime runtimeAssignment) sessionPersona {
	seed := fmt.Sprintf("%s:%d:%s", runtime.ID, runtime.PersonaVersion, runtime.AndroidVersion)
	release := runtimeAndroidRelease(runtime.AndroidVersion)
	phones := personaPhonesOn(release)
	template := phones[int(stableHash(seed)%uint32(len(phones)))]
	build, ok := pickBuildForPatch(template.Builds[release], runtimeImageSecurityPatch())
	if !ok {
		build = genericBuild(release, seed)
	}
	return newSessionPersona(template, build, release, runtime.PersonaVersion, strings.ToUpper(base36Token(stableHash(seed+":serial"), 12)))
}

// pickBuildForPatch chooses, among a phone's builds for a release, the one
// whose security patch is closest to the image's: the newest build in the
// image's own patch month, else the newest at or before it, else the newest
// of all. It keeps a persona from claiming a build the phone had not yet
// shipped when the image was cut. With no image patch (the node is not told
// one) it claims the newest build.
func pickBuildForPatch(candidates []personaBuild, imagePatch string) (personaBuild, bool) {
	if len(candidates) == 0 {
		return personaBuild{}, false
	}
	byNewest := slices.Clone(candidates)
	slices.SortStableFunc(byNewest, func(a, b personaBuild) int { return cmp.Compare(b.DateUTC, a.DateUTC) })
	if imagePatch == "" {
		return byNewest[0], true
	}
	month := imagePatch
	if len(month) >= 7 {
		month = month[:7]
	}
	atOrBefore, haveAtOrBefore := personaBuild{}, false
	for _, build := range byNewest {
		if len(build.SecurityPatch) >= 7 && build.SecurityPatch[:7] == month {
			return build, true
		}
		if !haveAtOrBefore && build.SecurityPatch != "" && build.SecurityPatch <= imagePatch {
			atOrBefore, haveAtOrBefore = build, true
		}
	}
	if haveAtOrBefore {
		return atOrBefore, true
	}
	return byNewest[0], true
}

// runtimeImageSecurityPatch is the Android security patch the image carries,
// when the image tells the node (NODE_RUNTIME_SECURITY_PATCH). The persona
// keeps the image's patch and, per phone, claims the build closest to it;
// without it the persona claims the newest build.
func runtimeImageSecurityPatch() string {
	return strings.TrimSpace(os.Getenv("NODE_RUNTIME_SECURITY_PATCH"))
}

// personaPhonesOn is the catalog's phones that got the release. Should the
// image run a release none of them has a build for, every phone claims it
// with a generic build.
func personaPhonesOn(release string) []personaTemplate {
	var phones []personaTemplate
	for _, template := range personaCatalog {
		if len(template.Builds[release]) > 0 {
			phones = append(phones, template)
		}
	}
	if len(phones) == 0 {
		return personaCatalog
	}
	return phones
}

// genericBuild is a Pixel-shaped build: an AOSP build ID for the release, an
// 8-digit incremental and a build date, so a release with no catalog build
// still overrides the image's own build properties.
func genericBuild(release, seed string) personaBuild {
	return personaBuild{
		ID:          buildIDForRelease(release, stableHash(seed+":build")),
		Incremental: strconv.FormatUint(uint64(10000000+stableHash(seed+":inc")%90000000), 10),
		DateUTC:     int64(1750000000 + stableHash(seed+":date")%40000000),
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

	host, user, zone := brandBuild(template.Brand)
	buildDate := time.Unix(build.DateUTC, 0).In(zone).Format("Mon Jan _2 15:04:05 MST 2006")

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
		BuildDate:    buildDate,
		BuildDateUTC: build.DateUTC,
		BuildHost:    host,
		BuildUser:    user,
		Serial:       serial,
	}
}

// brandBuild is the build machine, user and local time zone an OEM stamps
// into ro.build.host, ro.build.user and ro.build.date, taken from a getprop
// dump of one of its Android 16 phones. One representative per brand: real
// build hosts are shared across a vendor's models.
func brandBuild(brand string) (host, user string, zone *time.Location) {
	switch brand {
	case "samsung":
		return "21DODA03", "dpi", time.FixedZone("KST", 9*3600)
	case "Xiaomi", "Redmi", "POCO":
		return "pangu-build-component-system-1094538-dwgkq-l9l29-cclmq", "builder", time.FixedZone("CST", 8*3600)
	case "OnePlus":
		return "kvm-slave-build-s-system-13776704", "root", time.FixedZone("CST", 8*3600)
	case "motorola":
		return "ilclbld230", "hudsoncm", time.FixedZone("CDT", -5*3600)
	case "Sony":
		return "BuildHost", "BuildUser", time.FixedZone("CET", 1*3600)
	case "Nothing":
		return "NTSV-J8001XSA", "nothing", time.FixedZone("CST", 8*3600)
	default: // google, and any generic build
		return "184aed4401b2", "android-build", time.UTC
	}
}

const (
	personaBuildTags = "release-keys"
	personaBuildType = "user"
)

// buildPartitions are the partitions that carry their own
// ro.<partition>.build.* and ro.product.<partition>.*, which the image
// otherwise fills with its own: those Build.getFingerprintedPartitions
// reports, and vendor_dlkm, which ReDroid's device has too.
var buildPartitions = []string{"system", "system_ext", "product", "vendor", "odm", "bootimage", "vendor_dlkm"}

// personaOverrideProps are the persona's properties as arguments to ReDroid's
// init, which reads its arguments as a kernel command line: split at spaces
// outside double quotes, and the quotes dropped. A value with spaces (a build
// date, a description) goes in quotes, or init keeps only its first word.
func personaOverrideProps(persona sessionPersona) []string {
	props := personaProps(persona)
	for i, prop := range props {
		if key, value, _ := strings.Cut(prop, "="); strings.Contains(value, " ") {
			props[i] = key + `="` + value + `"`
		}
	}
	return props
}

func personaProps(persona sessionPersona) []string {
	utc := strconv.FormatInt(persona.BuildDateUTC, 10)
	props := []string{
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
		"ro.build.flavor=" + persona.Product + "-" + personaBuildType,
		"ro.build.tags=" + personaBuildTags,
		"ro.build.type=" + personaBuildType,
		"ro.build.date=" + persona.BuildDate,
		"ro.build.date.utc=" + utc,
		"ro.build.host=" + persona.BuildHost,
		"ro.build.user=" + persona.BuildUser,
		"ro.serialno=" + persona.Serial,
	}
	// Every partition carries the one build, so the partitions
	// Build.getFingerprintedPartitions lists and the ro.product.<partition>.*
	// an app reads all name the claimed phone, not the image beneath it. Real
	// phones diverge here (vendor on an older release, an OEM's own system
	// product); presenting one consistent build is left as a later step.
	for _, part := range buildPartitions {
		props = append(props,
			"ro.product."+part+".brand="+persona.Brand,
			"ro.product."+part+".manufacturer="+persona.Manufacturer,
			"ro.product."+part+".model="+persona.Model,
			"ro.product."+part+".device="+persona.Device,
			"ro.product."+part+".name="+persona.Product,
			"ro."+part+".build.fingerprint="+persona.Fingerprint,
			"ro."+part+".build.id="+persona.BuildID,
			"ro."+part+".build.version.incremental="+persona.Incremental,
			"ro."+part+".build.tags="+personaBuildTags,
			"ro."+part+".build.type="+personaBuildType,
			"ro."+part+".build.date="+persona.BuildDate,
			"ro."+part+".build.date.utc="+utc,
		)
	}
	return props
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
