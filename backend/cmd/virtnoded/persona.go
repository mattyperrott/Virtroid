package main

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"strconv"
	"strings"
)

type personaTemplate struct {
	Brand        string
	Manufacturer string
	Model        string
	Device       string
	Product      string
}

type sessionPersona struct {
	Version      int    `json:"version"`
	Brand        string `json:"brand"`
	Manufacturer string `json:"manufacturer"`
	Model        string `json:"model"`
	Device       string `json:"device"`
	Product      string `json:"product"`
	BuildID      string `json:"build_id"`
	Incremental  string `json:"incremental"`
	Release      string `json:"release"`
	Fingerprint  string `json:"fingerprint"`
	Description  string `json:"description"`
	Serial       string `json:"serial"`
}

var personaCatalog = []personaTemplate{
	{Brand: "google", Manufacturer: "Google", Model: "Pixel 6", Device: "oriole", Product: "oriole"},
	{Brand: "google", Manufacturer: "Google", Model: "Pixel 7", Device: "panther", Product: "panther"},
	{Brand: "google", Manufacturer: "Google", Model: "Pixel 8", Device: "shiba", Product: "shiba"},
	{Brand: "google", Manufacturer: "Google", Model: "Pixel 8 Pro", Device: "husky", Product: "husky"},
	{Brand: "google", Manufacturer: "Google", Model: "Pixel 8a", Device: "akita", Product: "akita"},
	{Brand: "google", Manufacturer: "Google", Model: "Pixel 9", Device: "tokay", Product: "tokay"},
	{Brand: "google", Manufacturer: "Google", Model: "Pixel 9 Pro", Device: "caiman", Product: "caiman"},
	{Brand: "samsung", Manufacturer: "samsung", Model: "SM-S901B", Device: "r0s", Product: "r0seea"},
	{Brand: "samsung", Manufacturer: "samsung", Model: "SM-A528B", Device: "a52sxq", Product: "a52sxxeea"},
	{Brand: "samsung", Manufacturer: "samsung", Model: "SM-S918B", Device: "dm3q", Product: "dm3qxxx"},
	{Brand: "samsung", Manufacturer: "samsung", Model: "SM-S928B", Device: "e3q", Product: "e3qxxx"},
	{Brand: "Xiaomi", Manufacturer: "Xiaomi", Model: "2201116SG", Device: "ingres", Product: "ingres_global"},
	{Brand: "OnePlus", Manufacturer: "OnePlus", Model: "CPH2413", Device: "ossi", Product: "ossi_global"},
	{Brand: "OnePlus", Manufacturer: "OnePlus", Model: "CPH2449", Device: "salami", Product: "CPH2449"},
	{Brand: "motorola", Manufacturer: "motorola", Model: "XT2301-4", Device: "rtwo", Product: "rtwo_g"},
	{Brand: "Sony", Manufacturer: "Sony", Model: "XQ-DQ54", Device: "pdx234", Product: "pdx234"},
	{Brand: "Nothing", Manufacturer: "Nothing", Model: "A065", Device: "Pong", Product: "Pong"},
}

func buildSessionPersona(runtime runtimeAssignment) sessionPersona {
	seed := fmt.Sprintf("%s:%d:%s", runtime.ID, runtime.PersonaVersion, runtime.AndroidVersion)
	seedValue := stableHash(seed)
	template := personaCatalog[int(seedValue%uint32(len(personaCatalog)))]
	release := androidRelease(runtime.AndroidVersion)
	buildID := buildIDForRelease(release, stableHash(seed+":build"))
	incremental := strconv.FormatUint(uint64(100000000+stableHash(seed+":inc")%900000000), 10)
	fingerprint := fmt.Sprintf(
		"%s/%s/%s:%s/%s/%s:user/release-keys",
		strings.ToLower(template.Brand),
		template.Product,
		template.Device,
		release,
		buildID,
		incremental,
	)
	description := fmt.Sprintf("%s-user %s %s %s release-keys", template.Product, release, buildID, incremental)

	return sessionPersona{
		Version:      runtime.PersonaVersion,
		Brand:        template.Brand,
		Manufacturer: template.Manufacturer,
		Model:        template.Model,
		Device:       template.Device,
		Product:      template.Product,
		BuildID:      buildID,
		Incremental:  incremental,
		Release:      release,
		Fingerprint:  fingerprint,
		Description:  description,
		Serial:       strings.ToUpper(base36Token(stableHash(seed+":serial"), 12)),
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
		"ro.build.display.id=" + persona.BuildID,
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

func buildIDForRelease(release string, seed uint32) string {
	prefix := "SP1A"
	switch release {
	case "13":
		prefix = "TQ3A"
	case "14":
		prefix = "UP1A"
	case "15":
		prefix = "AP3A"
	case "16":
		prefix = "BP1A"
	}
	return fmt.Sprintf("%s.220624.%03d", prefix, 100+seed%900)
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
