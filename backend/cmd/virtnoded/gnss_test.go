package main

import (
	"bytes"
	"context"
	"io"
	"log"
	"math"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// gnssRoot is a short runtime root: socket paths are limited to about 100
// bytes, which a test's usual temporary directory can exceed.
func gnssRoot(t *testing.T) string {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "vgnss")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	return root
}

func testGNSSFeeds() *gnssFeeds {
	feeds := newGNSSFeeds(-1, -1)
	feeds.now = func() time.Time { return time.UnixMilli(1_790_000_000_000) }
	feeds.noise = func() gnssNoise { return gnssNoise{accuracyMeters: 4.2} }
	return feeds
}

// askGNSS sends one command as the HAL does and returns the whole answer.
func askGNSS(t *testing.T, root, runtimeID, command string) string {
	t.Helper()
	conn, err := net.Dial("unix", filepath.Join(gnssFeedDir(root, runtimeID), gnssSocketName))
	if err != nil {
		t.Fatalf("dial %s: %v", runtimeID, err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.WriteString(conn, command); err != nil {
		t.Fatal(err)
	}
	_ = conn.(*net.UnixConn).CloseWrite()
	answer, err := io.ReadAll(conn)
	if err != nil {
		t.Fatal(err)
	}
	return string(answer)
}

// parseFix reads a record as FixLocationParser does: the first line, at
// least 12 comma-separated fields.
func parseFix(t *testing.T, record string) (latitude, longitude, accuracy float64, unixMillis int64) {
	t.Helper()
	if !strings.HasSuffix(record, gnssRecordEnd) {
		t.Fatalf("record %q lacks the HAL's terminator", record)
	}
	fields := strings.Split(strings.SplitN(record, "\n", 2)[0], ",")
	if len(fields) != 12 || fields[0] != "Fix" || fields[1] != "GPS" {
		t.Fatalf("record fields = %q", fields)
	}
	number := func(index int) float64 {
		value, err := strconv.ParseFloat(fields[index], 64)
		if err != nil {
			t.Fatalf("field %d %q: %v", index, fields[index], err)
		}
		return value
	}
	for index := 2; index < len(fields); index++ {
		number(index)
	}
	millis, err := strconv.ParseInt(fields[8], 10, 64)
	if err != nil {
		t.Fatalf("time %q: %v", fields[8], err)
	}
	return number(2), number(3), number(6), millis
}

func TestGNSSFixRecordMatchesTheHALFormat(t *testing.T) {
	now := time.UnixMilli(1_790_000_000_123)
	record := gnssFixRecord(gnssPlace{latitude: 52.52, longitude: 13.405}, now, gnssNoise{accuracyMeters: 4.2})
	latitude, longitude, accuracy, millis := parseFix(t, record)
	if latitude != 52.52 || longitude != 13.405 || accuracy != 4.2 || millis != now.UnixMilli() {
		t.Fatalf("fix = %v, %v, %v, %v", latitude, longitude, accuracy, millis)
	}
}

func TestGNSSFixesWanderOnlyAFewMetres(t *testing.T) {
	place := gnssPlace{latitude: -33.8688, longitude: 151.2093}
	for range 500 {
		noise := realisticGNSSNoise()
		latitude, longitude, accuracy, _ := parseFix(t, gnssFixRecord(place, time.Now(), noise))
		north := (latitude - place.latitude) * 111_320
		east := (longitude - place.longitude) * 111_320 * math.Cos(place.latitude*math.Pi/180)
		if distance := math.Hypot(north, east); distance > 3.05 {
			t.Fatalf("fix %.2f m from the place", distance)
		}
		if accuracy < 3.5 || accuracy > 8 {
			t.Fatalf("accuracy %.1f m", accuracy)
		}
	}
}

func TestGNSSFixStaysOnTheGlobe(t *testing.T) {
	for _, place := range []gnssPlace{{90, 0}, {-90, 45}, {0, 180}, {0, -180}, {89.99999, 179.99999}} {
		noise := gnssNoise{northMeters: 3, eastMeters: 3, accuracyMeters: 5}
		latitude, longitude, _, _ := parseFix(t, gnssFixRecord(place, time.Now(), noise))
		if math.Abs(latitude) > 90 || math.Abs(longitude) > 180 {
			t.Fatalf("%v gave %v, %v", place, latitude, longitude)
		}
	}
}

func TestGNSSFeedAnswersOnlyALocationRequestWithAPlace(t *testing.T) {
	root := gnssRoot(t)
	feeds := testGNSSFeeds()
	if err := feeds.start(root, "rt"); err != nil {
		t.Fatal(err)
	}
	defer feeds.stop("rt")
	if answer := askGNSS(t, root, "rt", gnssGetLocation); answer != "" {
		t.Fatalf("no place yet, but answered %q", answer)
	}
	feeds.set("rt", &gnssPlace{latitude: 48.8566, longitude: 2.3522})
	if latitude, longitude, _, _ := parseFix(t, askGNSS(t, root, "rt", gnssGetLocation)); latitude != 48.8566 || longitude != 2.3522 {
		t.Fatalf("fix = %v, %v", latitude, longitude)
	}
	for _, command := range []string{"CMD_GET_RAWMEASUREMENT", "CMD_GET_LOCATIONX", "HELLO", ""} {
		if answer := askGNSS(t, root, "rt", command); answer != "" {
			t.Fatalf("%q answered %q", command, answer)
		}
	}
	// The place moves when the owner picks another one.
	feeds.set("rt", &gnssPlace{latitude: 35.6762, longitude: 139.6503})
	if latitude, _, _, _ := parseFix(t, askGNSS(t, root, "rt", gnssGetLocation)); latitude != 35.6762 {
		t.Fatalf("latitude after the change = %v", latitude)
	}
	// And a region taken away leaves no fix.
	feeds.set("rt", nil)
	if answer := askGNSS(t, root, "rt", gnssGetLocation); answer != "" {
		t.Fatalf("place cleared, but answered %q", answer)
	}
}

func TestGNSSFeedsKeepRuntimesApart(t *testing.T) {
	root := gnssRoot(t)
	feeds := testGNSSFeeds()
	for _, runtimeID := range []string{"rt-a", "rt-b"} {
		if err := feeds.start(root, runtimeID); err != nil {
			t.Fatal(err)
		}
	}
	defer feeds.stop("rt-b")
	feeds.set("rt-a", &gnssPlace{latitude: 1.3521, longitude: 103.8198})
	feeds.set("rt-b", &gnssPlace{latitude: -23.5505, longitude: -46.6333})
	if latitude, _, _, _ := parseFix(t, askGNSS(t, root, "rt-a", gnssGetLocation)); latitude != 1.3521 {
		t.Fatalf("runtime a latitude = %v", latitude)
	}
	if latitude, _, _, _ := parseFix(t, askGNSS(t, root, "rt-b", gnssGetLocation)); latitude != -23.5505 {
		t.Fatalf("runtime b latitude = %v", latitude)
	}

	feeds.stop("rt-a")
	if _, err := os.Stat(gnssFeedDir(root, "rt-a")); !os.IsNotExist(err) {
		t.Fatalf("stopped runtime's feed directory remains: %v", err)
	}
	if _, err := net.Dial("unix", filepath.Join(gnssFeedDir(root, "rt-a"), gnssSocketName)); err == nil {
		t.Fatal("a stopped runtime's feed still answers")
	}
	// Setting a stopped runtime's place does not reopen or touch anything.
	feeds.set("rt-a", &gnssPlace{latitude: 10, longitude: 10})
	if _, err := os.Stat(gnssFeedDir(root, "rt-a")); !os.IsNotExist(err) {
		t.Fatal("setting a stopped runtime's place recreated its feed")
	}
	if latitude, _, _, _ := parseFix(t, askGNSS(t, root, "rt-b", gnssGetLocation)); latitude != -23.5505 {
		t.Fatalf("runtime b after a stopped: latitude = %v", latitude)
	}
}

func TestGNSSFeedRefusesRuntimeIDsThatLeaveItsDirectory(t *testing.T) {
	feeds := testGNSSFeeds()
	for _, runtimeID := range []string{"", ".", "..", "../rt", "a/b", `a\b`} {
		if err := feeds.start(gnssRoot(t), runtimeID); err == nil {
			t.Fatalf("runtime id %q accepted", runtimeID)
		}
	}
}

func TestGNSSFeedReplacesASocketLeftByAnEarlierNode(t *testing.T) {
	root := gnssRoot(t)
	dir := gnssFeedDir(root, "rt")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A node that died left its socket behind.
	stale, err := net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(dir, gnssSocketName), Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	stale.SetUnlinkOnClose(false)
	_ = stale.Close()

	feeds := testGNSSFeeds()
	if err := feeds.start(root, "rt"); err != nil {
		t.Fatalf("start over a stale socket: %v", err)
	}
	defer feeds.stop("rt")
	feeds.set("rt", &gnssPlace{latitude: 51.5074, longitude: -0.1278})
	if latitude, _, _, _ := parseFix(t, askGNSS(t, root, "rt", gnssGetLocation)); latitude != 51.5074 {
		t.Fatalf("latitude = %v", latitude)
	}
	// Starting an open feed again changes nothing.
	if err := feeds.start(root, "rt"); err != nil {
		t.Fatal(err)
	}
	if latitude, _, _, _ := parseFix(t, askGNSS(t, root, "rt", gnssGetLocation)); latitude != 51.5074 {
		t.Fatalf("latitude after a second start = %v", latitude)
	}
}

func TestGNSSSocketIsClosedToOtherUsers(t *testing.T) {
	root := gnssRoot(t)
	feeds := testGNSSFeeds()
	if err := feeds.start(root, "rt"); err != nil {
		t.Fatal(err)
	}
	defer feeds.stop("rt")
	info, err := os.Stat(filepath.Join(gnssFeedDir(root, "rt"), gnssSocketName))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSocket == 0 || info.Mode().Perm() != gnssSocketMode {
		t.Fatalf("socket mode = %v, want socket %o", info.Mode(), gnssSocketMode)
	}
}

func TestGNSSFeedLogsNoPlace(t *testing.T) {
	var logged bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logged)
	defer log.SetOutput(previous)

	root := gnssRoot(t)
	feeds := testGNSSFeeds()
	if err := feeds.start(root, "rt"); err != nil {
		t.Fatal(err)
	}
	feeds.set("rt", &gnssPlace{latitude: 40.4168, longitude: -3.7038})
	answer := askGNSS(t, root, "rt", gnssGetLocation)
	askGNSS(t, root, "rt", "nonsense")
	feeds.set("rt", nil)
	feeds.stop("rt")
	if !strings.Contains(answer, "40.4168") {
		t.Fatalf("the feed itself did not answer: %q", answer)
	}
	for _, fragment := range []string{"40.41", "-3.70", "3.7038"} {
		if strings.Contains(logged.String(), fragment) {
			t.Fatalf("log names the place (%q): %s", fragment, logged.String())
		}
	}
}

func TestRuntimeWithoutARegionHasNoFix(t *testing.T) {
	root := gnssRoot(t)
	node := &nodeAgent{gnss: testGNSSFeeds()}
	if err := node.gnss.start(root, "rt"); err != nil {
		t.Fatal(err)
	}
	defer node.gnss.stop("rt")
	node.gnss.set("rt", &gnssPlace{latitude: 1, longitude: 1})
	empty := " "
	for _, sealed := range []*string{nil, &empty} {
		if err := node.ensureRuntimeRegion(context.Background(), runtimeAssignment{ID: "rt", RegionSealed: sealed}, "container"); err != nil {
			t.Fatal(err)
		}
		if answer := askGNSS(t, root, "rt", gnssGetLocation); answer != "" {
			t.Fatalf("a runtime without a region still has a fix: %q", answer)
		}
	}
}

func TestNodeWithoutFeedsIgnoresThem(t *testing.T) {
	var feeds *gnssFeeds
	if err := feeds.start(gnssRoot(t), "rt"); err != nil {
		t.Fatal(err)
	}
	feeds.set("rt", &gnssPlace{})
	feeds.stop("rt")
}
