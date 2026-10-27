package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func TestPersonaCatalogHasBroadUniqueCoverage(t *testing.T) {
	if len(personaCatalog) < 16 {
		t.Fatalf("persona catalog size = %d, want at least 16", len(personaCatalog))
	}

	seen := make(map[string]struct{}, len(personaCatalog))
	manufacturers := make(map[string]struct{})
	for _, template := range personaCatalog {
		if template.Brand == "" || template.Manufacturer == "" || template.Model == "" ||
			template.Device == "" || template.Product == "" {
			t.Fatalf("persona template has an empty build field: %+v", template)
		}
		key := template.Brand + "/" + template.Product + "/" + template.Device
		if _, exists := seen[key]; exists {
			t.Fatalf("duplicate persona build identity %q", key)
		}
		seen[key] = struct{}{}
		manufacturers[template.Manufacturer] = struct{}{}
	}
	if len(manufacturers) < 7 {
		t.Fatalf("persona manufacturer count = %d, want at least 7", len(manufacturers))
	}
}

func TestBuildSessionPersonaIsStable(t *testing.T) {
	runtime := runtimeAssignment{
		ID:             "11111111-2222-3333-4444-555555555555",
		PersonaVersion: 4,
		AndroidVersion: "android-14",
	}
	first := buildSessionPersona(runtime)
	second := buildSessionPersona(runtime)
	if first != second {
		t.Fatalf("persona changed for stable runtime seed: first=%+v second=%+v", first, second)
	}
	if first.Release != "14" || first.Fingerprint == "" || first.Serial == "" {
		t.Fatalf("persona build metadata incomplete: %+v", first)
	}
}

func TestPersonaClaimsTheImagesAndroidRelease(t *testing.T) {
	t.Setenv("NODE_RUNTIME_ANDROID_RELEASE", "16")
	persona := buildSessionPersona(runtimeAssignment{
		ID:             "11111111-2222-3333-4444-555555555555",
		PersonaVersion: 4,
		AndroidVersion: "android-14",
	})
	if persona.Release != "16" || !strings.Contains(persona.Fingerprint, ":16/"+persona.BuildID+"/") {
		t.Fatalf("persona = %+v, want the image's Android 16", persona)
	}
}

// Phones personas once claimed Android 16 on, which never shipped it
// (Xperia 1 V and edge 40 pro: no Android 16 build found).
var phonesWithoutAndroid16 = []string{"SM-A528B", "2201116SG", "CPH2413", "XQ-DQ54", "XT2301-4"}

func TestPersonasClaimOnlyReleasesTheirPhonesGot(t *testing.T) {
	t.Setenv("NODE_RUNTIME_ANDROID_RELEASE", "16")
	claimed := map[string]bool{}
	for index := 0; index < 2000; index++ {
		persona := buildSessionPersona(runtimeAssignment{ID: fmt.Sprintf("runtime-%d", index), PersonaVersion: 1})
		build, ok := pickBuildForPatch(catalogPhone(t, persona.Model).Builds["16"], "")
		if !ok || persona.Release != "16" || persona.BuildID != build.ID || persona.Incremental != build.Incremental {
			t.Fatalf("persona %+v claims a build its phone never shipped on Android 16", persona)
		}
		claimed[persona.Model] = true
	}
	for _, template := range personaCatalog {
		if len(template.Builds["16"]) > 0 && !claimed[template.Model] {
			t.Fatalf("no persona claims the %s", template.Model)
		}
	}
	for _, model := range phonesWithoutAndroid16 {
		if claimed[model] {
			t.Fatalf("a persona claims Android 16 on the %s", model)
		}
	}
}

func catalogPhone(t *testing.T, model string) personaTemplate {
	t.Helper()
	for _, template := range personaCatalog {
		if template.Model == model {
			return template
		}
	}
	t.Fatalf("%q is not in the catalog", model)
	return personaTemplate{}
}

// Each vendor's Android 16 builds have their own shape, which an app
// comparing build properties with the model would expect.
func TestPersonaBuildsLookLikeTheirVendors(t *testing.T) {
	for _, template := range personaCatalog {
		for release, builds := range template.Builds {
			if release != "16" {
				t.Fatalf("%s: no vendor shapes for Android %s builds yet", template.Model, release)
			}
			for _, build := range builds {
				assertBuildLooksLikeItsVendor(t, template, build, release)
			}
		}
	}
}

func assertBuildLooksLikeItsVendor(t *testing.T, template personaTemplate, build personaBuild, release string) {
	t.Helper()
	aosp := `[A-Z]P[1-4]A\.\d{6}\.\d{3}(\.[A-Z]\d)?`
	p := newSessionPersona(template, build, release, 1, "SERIAL")
	product := template.Product
	if template.FingerprintProduct != "" {
		product = template.FingerprintProduct
	}
	if want := fmt.Sprintf("%s/%s/%s:16/%s/%s:user/release-keys", template.Brand, product, template.Device, p.BuildID, p.Incremental); p.Fingerprint != want {
		t.Fatalf("%s fingerprint %q, want %q", template.Model, p.Fingerprint, want)
	}
	if !regexp.MustCompile(`^\S+-user 16 ` + regexp.QuoteMeta(p.BuildID) + ` \S+ release-keys$`).MatchString(p.Description) {
		t.Fatalf("%s description %q", template.Model, p.Description)
	}
	shape := func(field, value, pattern string) {
		t.Helper()
		if !regexp.MustCompile("^(" + pattern + ")$").MatchString(value) {
			t.Fatalf("%s %s %q does not match %s", template.Model, field, value, pattern)
		}
	}
	defaultDescription := product + "-user 16 " + p.BuildID + " " + p.Incremental + " release-keys"
	switch template.Brand {
	case "google":
		shape("build ID", p.BuildID, aosp)
		shape("incremental", p.Incremental, `\d{8}`)
		shape("display ID", p.DisplayID, regexp.QuoteMeta(p.BuildID))
		shape("description", p.Description, regexp.QuoteMeta(defaultDescription))
	case "samsung":
		// Model code, region, update type, bootloader, One UI, year
		// (Y 2025, Z 2026), month, build: S928BXXS6DZI1.
		shape("build ID", p.BuildID, aosp)
		shape("incremental", p.Incremental, strings.TrimPrefix(template.Model, "SM-")+`XX[SU][0-9A-Z][A-Z][YZ][A-L][0-9A-Z]`)
		shape("display ID", p.DisplayID, regexp.QuoteMeta(p.BuildID+"."+p.Incremental))
		shape("description", p.Description, regexp.QuoteMeta(template.Device)+`\w+-user 16 `+regexp.QuoteMeta(p.BuildID+" "+p.Incremental)+` release-keys`)
	case "Redmi", "Xiaomi", "POCO":
		// HyperOS 3, W for Android 16, then device, region and XM.
		shape("build ID", p.BuildID, aosp)
		shape("incremental", p.Incremental, `OS3\.0\.\d+\.0\.W[A-Z]{4}XM`)
		shape("display ID", p.DisplayID, regexp.QuoteMeta(p.BuildID))
		shape("description", p.Description, `missi-user 16 `+regexp.QuoteMeta(p.BuildID)+` \S+ release-keys`)
	case "OnePlus":
		shape("build ID", p.BuildID, aosp)
		shape("incremental", p.Incremental, `[A-Z]\.R4T\d\.[0-9a-f]{6,7}([_-][0-9a-f]{5,7}){2}`)
		shape("display ID", p.DisplayID, regexp.QuoteMeta(template.Model)+`_16\.0\.\d+\.\d{3}\(EX01\)`)
		shape("description", p.Description, `qssi_64-user 16 `+regexp.QuoteMeta(p.BuildID)+` \d{13} release-keys`)
	case "motorola":
		// W for Android 16, 36 its API level.
		shape("build ID", p.BuildID, `W1[A-Z]{2,4}36[A-Z]?\.\d+(-\d+)+`)
		shape("incremental", p.Incremental, `[0-9a-f]{5,6}-[0-9a-f]{5,7}`)
		shape("display ID", p.DisplayID, regexp.QuoteMeta(p.BuildID))
		shape("description", p.Description, regexp.QuoteMeta(defaultDescription))
	case "Sony":
		// 69.2.A.4.90 is 069002A0040090, then a release stamp.
		shape("build ID", p.BuildID, `\d{2}\.\d\.A\.\d\.\d+`)
		var major, minor, patch, number int
		if _, err := fmt.Sscanf(p.BuildID, "%d.%d.A.%d.%d", &major, &minor, &patch, &number); err != nil {
			t.Fatal(err)
		}
		shape("incremental", p.Incremental, fmt.Sprintf(`%03d%03dA%03d%04d\d{11}`, major, minor, patch, number))
		shape("display ID", p.DisplayID, regexp.QuoteMeta(p.BuildID+" release-keys"))
		shape("description", p.Description, regexp.QuoteMeta(defaultDescription))
	case "Nothing":
		// The incremental is the build time, yymmddhhmm, which the
		// display ID repeats after Nothing OS's version.
		shape("build ID", p.BuildID, `BQ2A\.\d{6}\.\d{3}-`+aosp)
		shape("incremental", p.Incremental, `\d{10}`)
		shape("display ID", p.DisplayID, `B4\.\d-`+p.Incremental[:6]+`-`+p.Incremental[6:])
		shape("description", p.Description, `qssi_64-user 16 `+regexp.QuoteMeta(p.BuildID+" "+p.Incremental)+` release-keys`)
	default:
		t.Fatalf("%s: no Android 16 shapes for brand %q", template.Model, template.Brand)
	}
}

func TestPersonaPropsCarryTheBuild(t *testing.T) {
	t.Setenv("NODE_RUNTIME_ANDROID_RELEASE", "16")
	for index := 0; index < 200; index++ {
		persona := buildSessionPersona(runtimeAssignment{ID: fmt.Sprintf("runtime-%d", index), PersonaVersion: 1})
		if persona.Brand != "samsung" {
			continue
		}
		props := strings.Join(personaOverrideProps(persona), "\n")
		if !strings.Contains(props, "\nro.build.id="+persona.BuildID+"\n") ||
			!strings.Contains(props, "\nro.build.display.id="+persona.BuildID+"."+persona.Incremental+"\n") {
			t.Fatalf("props %q", props)
		}
		return
	}
	t.Fatal("no Samsung persona")
}

// Everything Build.getFingerprintedPartitions and Build.TIME report has to
// name the claimed phone; none of it may still carry the image's own build
// identity underneath.
func TestPersonaPropsCoverEveryPartition(t *testing.T) {
	t.Setenv("NODE_RUNTIME_ANDROID_RELEASE", "16")
	persona := buildSessionPersona(runtimeAssignment{ID: "partition-runtime", PersonaVersion: 1})
	props := "\n" + strings.Join(personaOverrideProps(persona), "\n") + "\n"
	for _, part := range fingerprintedPartitions {
		for _, want := range []string{
			"\nro." + part + ".build.fingerprint=" + persona.Fingerprint + "\n",
			"\nro." + part + ".build.id=" + persona.BuildID + "\n",
			"\nro." + part + ".build.version.incremental=" + persona.Incremental + "\n",
			"\nro." + part + ".build.date.utc=" + strconv.FormatInt(persona.BuildDateUTC, 10) + "\n",
			"\nro.product." + part + ".model=" + persona.Model + "\n",
			"\nro.product." + part + ".device=" + persona.Device + "\n",
		} {
			if !strings.Contains(props, want) {
				t.Fatalf("partition %s missing %q in:\n%s", part, want, props)
			}
		}
	}
	// The image names itself virtroid-lineage / lineage_*_x86_64, built by
	// eng.ubuntu; nothing a persona sets may still say so.
	for _, leak := range []string{"virtroid", "lineage", "eng.ubuntu"} {
		if strings.Contains(strings.ToLower(props), leak) {
			t.Fatalf("persona props leak %q:\n%s", leak, props)
		}
	}
}

func TestPersonaCarriesABuildDateHostAndUser(t *testing.T) {
	t.Setenv("NODE_RUNTIME_ANDROID_RELEASE", "16")
	for index := 0; index < 400; index++ {
		persona := buildSessionPersona(runtimeAssignment{ID: fmt.Sprintf("date-%d", index), PersonaVersion: 1})
		if persona.BuildDateUTC <= 0 || persona.BuildDate == "" {
			t.Fatalf("persona has no build date: %+v", persona)
		}
		if persona.BuildHost == "" || persona.BuildUser == "" || persona.BuildHost == "virtroid-lineage" {
			t.Fatalf("persona build host/user wrong: %+v", persona)
		}
		props := "\n" + strings.Join(personaOverrideProps(persona), "\n") + "\n"
		for _, want := range []string{
			"\nro.build.date.utc=" + strconv.FormatInt(persona.BuildDateUTC, 10) + "\n",
			"\nro.build.date=" + persona.BuildDate + "\n",
			"\nro.build.host=" + persona.BuildHost + "\n",
			"\nro.build.user=" + persona.BuildUser + "\n",
		} {
			if !strings.Contains(props, want) {
				t.Fatalf("props missing %q:\n%s", want, props)
			}
		}
	}
}

func TestPickBuildForPatch(t *testing.T) {
	older := personaBuild{ID: "A", SecurityPatch: "2026-07-01", DateUTC: 100}
	sameMonth := personaBuild{ID: "B", SecurityPatch: "2026-10-01", DateUTC: 200}
	newer := personaBuild{ID: "C", SecurityPatch: "2026-12-01", DateUTC: 300}
	cases := []struct {
		name       string
		candidates []personaBuild
		imagePatch string
		wantID     string
		wantOK     bool
	}{
		{"same month wins", []personaBuild{older, sameMonth, newer}, "2026-10-05", "B", true},
		{"newest at or before", []personaBuild{older, newer}, "2026-10-05", "A", true},
		{"newest of all when none fit", []personaBuild{newer}, "2026-10-05", "C", true},
		{"no image patch takes newest", []personaBuild{older, sameMonth, newer}, "", "C", true},
		{"empty candidates", nil, "2026-10-05", "", false},
	}
	for _, tc := range cases {
		build, ok := pickBuildForPatch(tc.candidates, tc.imagePatch)
		if ok != tc.wantOK || build.ID != tc.wantID {
			t.Fatalf("%s: got (%q, %v), want (%q, %v)", tc.name, build.ID, ok, tc.wantID, tc.wantOK)
		}
	}
}

// The catalog is curated against the LineageOS image's own security patch; no
// persona may claim a build the phone shipped after it, and at least one
// claims a build from the image's own patch month.
func TestCatalogBuildsAreNotNewerThanTheImagePatch(t *testing.T) {
	const imageSecurityPatch = "2026-10-01"
	sawSameMonth := false
	for _, template := range personaCatalog {
		for _, build := range template.Builds["16"] {
			if build.SecurityPatch == "" {
				t.Fatalf("%s build %s has no security patch", template.Model, build.ID)
			}
			if build.SecurityPatch > imageSecurityPatch {
				t.Fatalf("%s claims %s, newer than the image patch %s", template.Model, build.SecurityPatch, imageSecurityPatch)
			}
			if build.SecurityPatch[:7] == imageSecurityPatch[:7] {
				sawSameMonth = true
			}
		}
	}
	if !sawSameMonth {
		t.Fatalf("no phone claims a build from the image's own patch month %s", imageSecurityPatch[:7])
	}
}
