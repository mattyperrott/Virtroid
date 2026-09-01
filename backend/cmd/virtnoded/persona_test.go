package main

import "testing"

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
