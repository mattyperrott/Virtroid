package httpapi

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestValidRuntimeNotificationAcceptsMetadataOnlyEvent(t *testing.T) {
	event := runtimeNotificationEvent{
		EventID:     "d9c31cc4-5395-45d7-bc93-52f249c5245a",
		PackageName: "org.thoughtcrime.securesms",
		AppLabel:    "Signal",
		PostedAt:    time.Now().UTC(),
		Title:       "Alice",
	}
	if !validRuntimeNotification(event) {
		t.Fatal("valid metadata-only event was rejected")
	}
}

func TestRuntimeNotificationPayloadContainsOnlyApprovedMetadata(t *testing.T) {
	event := runtimeNotificationEvent{
		EventID:     "d9c31cc4-5395-45d7-bc93-52f249c5245a",
		PackageName: "org.thoughtcrime.securesms",
		AppLabel:    "Signal",
		PostedAt:    time.Now().UTC(),
		Title:       "Alice",
	}
	payload, err := encodeRuntimeNotificationPayload(event)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"version": true, "event_id": true, "package_name": true,
		"app_label": true, "posted_at": true, "title": true,
	}
	if len(decoded) != len(want) {
		t.Fatalf("payload keys = %v", decoded)
	}
	for key := range decoded {
		if !want[key] {
			t.Fatalf("payload contains unapproved key %q", key)
		}
	}
}

func TestNotificationStreamHubRoutesOnlyToTargetDevice(t *testing.T) {
	hub := newNotificationStreamHub()
	target, unsubscribeTarget := hub.subscribe("device-a")
	defer unsubscribeTarget()
	other, unsubscribeOther := hub.subscribe("device-b")
	defer unsubscribeOther()

	hub.notify("device-a")
	select {
	case <-target:
	default:
		t.Fatal("target device was not notified")
	}
	select {
	case <-other:
		t.Fatal("notification crossed device boundary")
	default:
	}
}

func TestValidRuntimeNotificationRejectsOversizedUnicodeTitle(t *testing.T) {
	event := runtimeNotificationEvent{
		EventID:     "d9c31cc4-5395-45d7-bc93-52f249c5245a",
		PackageName: "org.thoughtcrime.securesms",
		AppLabel:    "Signal",
		PostedAt:    time.Now().UTC(),
		Title:       strings.Repeat("🙂", maxNotificationTitleRunes+1),
	}
	if utf8.RuneCountInString(event.Title) != maxNotificationTitleRunes+1 {
		t.Fatal("test title rune count is wrong")
	}
	if validRuntimeNotification(event) {
		t.Fatal("oversized Unicode title was accepted")
	}
}

func TestDecodeStrictJSONRejectsContentField(t *testing.T) {
	var event runtimeNotificationEvent
	err := decodeStrictJSON(
		strings.NewReader(`{"event_id":"d9c31cc4-5395-45d7-bc93-52f249c5245a","package_name":"org.signal","app_label":"Signal","posted_at":"2026-09-02T00:00:00Z","title":"Alice","message_content":"secret"}`),
		maxRuntimeNotificationBodyBytes,
		&event,
	)
	if err == nil {
		t.Fatal("metadata endpoint accepted an undeclared message-content field")
	}
}

func TestSecurityNoticePayloadContainsOnlySanitizedClientFields(t *testing.T) {
	event := securityNoticeEvent{
		EventID:    "d9c31cc4-5395-45d7-bc93-52f249c5245a",
		Source:     "falco",
		Severity:   "critical",
		Summary:    clientSecuritySummary("falco", "Virtroid Runtime Root Access By Unexpected Process"),
		ObservedAt: time.Now().UTC(),
	}
	payload, err := encodeSecurityNoticePayload(event)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"version": true, "kind": true, "event_id": true, "source": true,
		"severity": true, "summary": true, "observed_at": true,
	}
	if len(decoded) != len(want) {
		t.Fatalf("payload keys = %v", decoded)
	}
	for key := range decoded {
		if !want[key] {
			t.Fatalf("security payload contains unapproved key %q", key)
		}
	}
	encoded := string(payload)
	for _, forbidden := range []string{"command", "path", "node_id", "ip_address", "event_json"} {
		if strings.Contains(encoded, forbidden) {
			t.Fatalf("security payload contains forbidden detail %q: %s", forbidden, encoded)
		}
	}
}

func TestClientSecuritySeverityMapping(t *testing.T) {
	for input, want := range map[string]string{
		"NOTICE": "notice", "warning": "warning", "ERROR": "critical", "CRITICAL": "critical",
	} {
		if got := clientSecuritySeverity(input); got != want {
			t.Fatalf("clientSecuritySeverity(%q) = %q, want %q", input, got, want)
		}
	}
}
