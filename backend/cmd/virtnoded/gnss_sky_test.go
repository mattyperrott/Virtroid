package main

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"testing"
	"time"
)

// skyTime is when the built-in orbits are freshest, so the tests hold
// whenever they are refreshed.
func skyTime() time.Time {
	var latest time.Time
	for _, orbit := range gnssOrbits {
		if orbit.constellation == gnssGPS && orbit.epoch.After(latest) {
			latest = orbit.epoch
		}
	}
	return latest.Truncate(time.Hour)
}

var (
	tokyo  = gnssPlace{latitude: 35.6762, longitude: 139.6503}
	london = gnssPlace{latitude: 51.5074, longitude: -0.1278}
	delhi  = gnssPlace{latitude: 28.6139, longitude: 77.2090}
	quito  = gnssPlace{latitude: -0.1807, longitude: -78.4678}
	sydney = gnssPlace{latitude: -33.8688, longitude: 151.2093}
)

func deviceFor(model string) gnssDevice {
	return newGNSSDevice(sessionPersona{Model: model, Serial: "SERIAL" + model})
}

func TestGNSSOrbitsAreTheConstellations(t *testing.T) {
	counts := map[gnssConstellation]int{}
	seen := map[[2]int]bool{}
	for _, orbit := range gnssOrbits {
		key := [2]int{int(orbit.constellation), orbit.svid}
		if seen[key] {
			t.Fatalf("two orbits for %v", key)
		}
		seen[key] = true
		counts[orbit.constellation]++
		if orbit.constellation == gnssGLONASS && (orbit.glonassChannel < -7 || orbit.glonassChannel > 6) {
			t.Fatalf("GLONASS %d channel %d", orbit.svid, orbit.glonassChannel)
		}
		position := orbit.position(orbit.epoch)
		radius := math.Sqrt(position[0]*position[0] + position[1]*position[1] + position[2]*position[2])
		if radius < 25_000 || radius > 46_000 {
			t.Fatalf("%v orbits %.0f km from the Earth's centre", key, radius)
		}
	}
	for system, least := range map[gnssConstellation]int{gnssGPS: 24, gnssGLONASS: 18, gnssGalileo: 20,
		gnssBeiDou: 24, gnssQZSS: 3, gnssIRNSS: 4, gnssSBAS: 6} {
		if counts[system] < least {
			t.Fatalf("%d satellites of system %d, want at least %d", counts[system], system, least)
		}
	}
	if _, err := parseGNSSOrbits("constellation,svid\ngps,1\n"); err == nil {
		t.Fatal("orbits without elements parsed")
	}
}

func TestEveryPersonaHasItsOwnReceiver(t *testing.T) {
	for _, template := range personaCatalog {
		receiver, ok := gnssReceivers[template.Model]
		if !ok {
			t.Fatalf("no GNSS receiver for %s", template.Model)
		}
		if receiver.name == "" || receiver.year < 2016 || !receiver.tracks(gnssGPS) {
			t.Fatalf("%s: %+v", template.Model, receiver)
		}
		if receiver.tracks(gnssIRNSS) && !receiver.dualFrequency {
			t.Fatalf("%s tracks NavIC, which needs the L5 band", template.Model)
		}
	}
}

// A phone outdoors sees a dozen or two satellites per system it tracks,
// above the horizon, with believable strengths and geometry.
func TestGNSSSkyLooksLikeAPhoneOutdoors(t *testing.T) {
	device := deviceFor("Pixel 8")
	for _, place := range []gnssPlace{tokyo, london, delhi, quito, sydney, {latitude: 78.2232, longitude: 15.6267}} {
		for _, hours := range []int{0, 5, 11, 17, 23} {
			at := skyTime().Add(time.Duration(hours) * time.Hour)
			sky := gnssSky(device, place, at, gnssOrbits)
			gps, used := 0, map[[2]int]bool{}
			for _, satellite := range sky {
				if satellite.elevation < gnssTrackElevation || satellite.azimuth < 0 || satellite.azimuth >= 360 {
					t.Fatalf("%v: %+v", place, satellite)
				}
				if satellite.cn0 < 12 || satellite.cn0 > 50 || satellite.basebandCN0 >= satellite.cn0 {
					t.Fatalf("%v: strength %+v", place, satellite)
				}
				if satellite.constellation == gnssGPS && !satellite.signal.l5Band {
					gps++
				}
				if satellite.usedInFix() {
					used[[2]int{int(satellite.constellation), satellite.svid}] = true
				}
			}
			if gps < 5 || gps > 14 {
				t.Fatalf("%v at +%dh: %d GPS satellites", place, hours, gps)
			}
			if len(used) < 15 || len(sky) > gnssMaxSignals {
				t.Fatalf("%v at +%dh: %d used, %d signals", place, hours, len(used), len(sky))
			}
			dop := dilutionOfPrecision(sky)
			if dop.horizontal < 0.3 || dop.horizontal > 1.5 || dop.position < dop.horizontal {
				t.Fatalf("%v at +%dh: DOP %+v", place, hours, dop)
			}
		}
	}
}

func signalsBy(sky []gnssSatellite, keep func(gnssSatellite) bool) int {
	count := 0
	for _, satellite := range sky {
		if keep(satellite) {
			count++
		}
	}
	return count
}

func TestGNSSSkyFollowsTheReceiver(t *testing.T) {
	at := skyTime()
	l5 := func(s gnssSatellite) bool { return s.signal.l5Band }
	navic := func(s gnssSatellite) bool { return s.constellation == gnssIRNSS }
	sbas := func(s gnssSatellite) bool { return s.constellation == gnssSBAS }
	b1c := func(s gnssSatellite) bool {
		return s.constellation == gnssBeiDou && s.signal.code == "P" && !s.signal.l5Band
	}

	single := gnssSky(deviceFor("Pixel 8a"), delhi, at, gnssOrbits)
	if signalsBy(single, l5) != 0 || signalsBy(single, navic) != 0 || signalsBy(single, sbas) != 0 {
		t.Fatal("a single-frequency phone reports L5-band signals, NavIC or SBAS")
	}
	if signalsBy(single, b1c) == 0 {
		t.Fatal("the Pixel 8a reports no BeiDou B1C over Delhi")
	}
	dual := gnssSky(deviceFor("Pixel 8"), delhi, at, gnssOrbits)
	if signalsBy(dual, l5) == 0 || signalsBy(dual, navic) == 0 {
		t.Fatal("a Pixel 8 over Delhi reports no L5 band or no NavIC")
	}
	if signalsBy(dual, b1c) != 0 {
		t.Fatal("the Pixel 8's Broadcom chip reports B1C")
	}
	qualcomm := gnssSky(deviceFor("SM-S928B"), london, at, gnssOrbits)
	if signalsBy(qualcomm, sbas) == 0 || signalsBy(qualcomm, navic) != 0 {
		t.Fatal("a Galaxy S24 Ultra over London shows no SBAS, or NavIC")
	}
	for _, satellite := range qualcomm {
		if satellite.constellation == gnssSBAS && satellite.usedInFix() {
			t.Fatal("an SBAS satellite used in the fix")
		}
	}
}

func TestGNSSSkyDependsOnThePlace(t *testing.T) {
	at := skyTime()
	device := deviceFor("Pixel 8")
	qzss := func(s gnssSatellite) bool { return s.constellation == gnssQZSS && s.elevation > 20 }
	if signalsBy(gnssSky(device, tokyo, at, gnssOrbits), qzss) == 0 {
		t.Fatal("QZSS is not high over Tokyo")
	}
	if signalsBy(gnssSky(device, london, at, gnssOrbits), func(s gnssSatellite) bool { return s.constellation == gnssQZSS }) != 0 {
		t.Fatal("QZSS seen from London")
	}
	if signalsBy(gnssSky(device, london, at, gnssOrbits), func(s gnssSatellite) bool { return s.constellation == gnssIRNSS }) != 0 {
		t.Fatal("NavIC seen from London")
	}
}

// Every runtime at a place sees the same satellites where they are; each
// device has its own signal strengths, and so holds the L5 band on its own
// set of them.
func TestGNSSSkyIsTheSameSkyForEveryDevice(t *testing.T) {
	at := skyTime()
	type key struct {
		constellation gnssConstellation
		svid          int
		carrier       float64
	}
	signals := func(serial string) map[key]gnssSatellite {
		out := map[key]gnssSatellite{}
		for _, satellite := range gnssSky(newGNSSDevice(sessionPersona{Model: "Pixel 8", Serial: serial}), tokyo, at, gnssOrbits) {
			out[key{satellite.constellation, satellite.svid, satellite.signal.carrierHz}] = satellite
		}
		return out
	}
	one, other := signals("ONE"), signals("OTHER")
	shared, differ := 0, 0
	for k, a := range one {
		b, ok := other[k]
		if !ok {
			if !a.signal.l5Band {
				t.Fatalf("%v tracked by one device only", k)
			}
			continue
		}
		shared++
		if a.elevation != b.elevation || a.azimuth != b.azimuth {
			t.Fatalf("%+v and %+v", a, b)
		}
		if a.cn0 != b.cn0 {
			differ++
		}
	}
	if differ < shared/2 {
		t.Fatalf("only %d of %d strengths differ between devices", differ, shared)
	}
}

func TestGNSSSkyMovesAndStrengthsWanderSlowly(t *testing.T) {
	device := deviceFor("Pixel 8")
	start := skyTime()
	type key struct {
		constellation gnssConstellation
		svid          int
		carrier       float64
	}
	previous := map[key]gnssSatellite{}
	first := map[key]gnssSatellite{}
	changes := 0
	for second := range 120 {
		current := map[key]gnssSatellite{}
		for _, satellite := range gnssSky(device, london, start.Add(time.Duration(second)*time.Second), gnssOrbits) {
			k := key{satellite.constellation, satellite.svid, satellite.signal.carrierHz}
			current[k] = satellite
			if last, ok := previous[k]; ok {
				if math.Abs(last.cn0-satellite.cn0) > 1.0 {
					t.Fatalf("%v jumped %.1f dB in a second", k, satellite.cn0-last.cn0)
				}
				if math.Abs(last.elevation-satellite.elevation) > 0.15 {
					t.Fatalf("%v moved %.2f° in a second", k, satellite.elevation-last.elevation)
				}
			}
			if _, ok := first[k]; !ok {
				first[k] = satellite
			}
		}
		for k := range previous {
			if _, ok := current[k]; !ok {
				changes++
			}
		}
		previous = current
	}
	// Signals come and go only as satellites rise and set.
	if changes > 6 {
		t.Fatalf("the tracked set lost a signal %d times in two minutes", changes)
	}
	moved, wandered := 0, 0
	for k, last := range previous {
		if math.Abs(last.elevation-first[k].elevation) >= 0.2 {
			moved++
		}
		if last.cn0 != first[k].cn0 {
			wandered++
		}
	}
	if moved < len(previous)/2 || wandered < len(previous)/2 {
		t.Fatalf("in two minutes %d of %d moved, %d wandered", moved, len(previous), wandered)
	}
}

func TestGNSSLocationRecordCarriesTheSky(t *testing.T) {
	at := skyTime()
	fix := newGNSSFix(tokyo, at, gnssNoise{northMeters: 1, eastMeters: -2, accuracyMeters: 4.25})
	sky := gnssSky(deviceFor("Pixel 8"), tokyo, at, gnssOrbits)
	record := gnssLocationRecord(fix, sky)
	latitude, longitude, accuracy, millis := parseFix(t, record)
	if latitude != fix.latitude || longitude != fix.longitude || accuracy != 4.3 || millis != at.UnixMilli() {
		t.Fatalf("fix %v %v %v %v", latitude, longitude, accuracy, millis)
	}
	if strings.HasSuffix(record, "\n"+gnssRecordEnd) {
		t.Fatal("a newline before the terminator would start the next record")
	}
	svLines, nmeaLines := 0, 0
	for _, line := range strings.Split(strings.TrimSuffix(record, gnssRecordEnd), "\n")[1:] {
		fields := strings.Split(line, ",")
		switch fields[0] {
		case "Sv":
			if len(fields) != 9 {
				t.Fatalf("Sv line %q", line)
			}
			for _, field := range fields[1:] {
				if _, err := strconv.ParseFloat(field, 64); err != nil {
					t.Fatalf("Sv line %q: %v", line, err)
				}
			}
			svLines++
		case "Nmea":
			if !strings.HasPrefix(line, "Nmea,$") {
				t.Fatalf("NMEA line %q", line)
			}
			nmeaLines++
		default:
			t.Fatalf("line %q", line)
		}
	}
	if svLines != len(sky) || nmeaLines < 5 {
		t.Fatalf("%d Sv lines for %d signals, %d NMEA lines", svLines, len(sky), nmeaLines)
	}
}

func TestGNSSReceiverRecordNamesTheChip(t *testing.T) {
	record := gnssReceiverRecord(gnssReceiverFor("2312DRA50G"))
	lines := strings.Split(strings.TrimSuffix(record, gnssRecordEnd), "\n")
	fields := strings.SplitN(lines[0], ",", 4)
	if len(fields) != 4 || fields[0] != "Receiver" || fields[1] != "2016" || fields[2] != "0" ||
		fields[3] != "qcom;MPSS.DE.3.1.1.c1-00021-NETRANI_GENALL_PACK-1.79677.71;" {
		t.Fatalf("receiver line %q", lines[0])
	}
	signals := map[string]bool{}
	for _, line := range lines[1:] {
		signals[line] = true
	}
	for _, want := range []string{"Signal,1,1575420000,C", "Signal,3,1602000000,C", "Signal,5,1561098000,I", "Signal,2,1575420000,C"} {
		if !signals[want] {
			t.Fatalf("no %q in %q", want, lines)
		}
	}
	if signals["Signal,1,1176450000,Q"] {
		t.Fatal("a single-frequency chip claims L5")
	}
	if !strings.HasPrefix(gnssReceiverRecord(gnssReceiverFor("Pixel 8")), "Receiver,2020,1,Broadcom, BCM4776,") {
		t.Fatal("the Pixel 8's receiver")
	}
}

func TestGNSSNMEADescribesTheFix(t *testing.T) {
	at := skyTime().Add(1234 * time.Millisecond)
	fix := newGNSSFix(sydney, at, gnssNoise{accuracyMeters: 5})
	sky := gnssSky(deviceFor("SM-S928B"), sydney, at, gnssOrbits)
	sentences := gnssNMEA(fix, sky)
	inView := map[string]int{}
	gsvSatellites := 0
	for _, sentence := range sentences {
		if len(sentence) > 82 {
			t.Fatalf("%d characters: %s", len(sentence), sentence)
		}
		body, checksum, ok := strings.Cut(strings.TrimPrefix(sentence, "$"), "*")
		if !ok || nmeaSentence(body) != sentence || len(checksum) != 2 {
			t.Fatalf("checksum: %s", sentence)
		}
		fields := strings.Split(body, ",")
		switch fields[0][2:] {
		case "GGA":
			if fields[1] != at.UTC().Format("150405")+".23" || fields[3] != "S" || fields[5] != "E" || fields[6] != "1" {
				t.Fatalf("GGA %s", sentence)
			}
			degrees, _ := strconv.ParseFloat(fields[2][:2], 64)
			minutes, _ := strconv.ParseFloat(fields[2][2:], 64)
			if math.Abs(-(degrees+minutes/60)-fix.latitude) > 1e-6 {
				t.Fatalf("GGA latitude %s for %v", fields[2], fix.latitude)
			}
		case "RMC":
			if fields[2] != "A" || fields[9] != at.UTC().Format("020106") {
				t.Fatalf("RMC %s", sentence)
			}
		case "GSV":
			key := fields[0][:2] + "/" + fields[len(fields)-1]
			total, _ := strconv.Atoi(fields[3])
			inView[key] = total
			gsvSatellites += (len(fields) - 5) / 4
		}
	}
	listed := 0
	for _, total := range inView {
		listed += total
	}
	if listed != len(sky) || gsvSatellites != len(sky) {
		t.Fatalf("GSV lists %d (%d) of %d signals: %v", gsvSatellites, listed, len(sky), inView)
	}
	if latitude, _ := nmeaCoordinate(-33.99999999, 2, "N", "S"); latitude != "3400.00000" {
		t.Fatalf("rounding carried to %s", latitude)
	}
	if fmt.Sprint(nmeaSatelliteID(gnssGLONASS, 7), nmeaSatelliteID(gnssSBAS, 131), nmeaSatelliteID(gnssQZSS, 194)) != "71 44 2" {
		t.Fatal("NMEA satellite numbers")
	}
}

func TestGNSSFeedAnswersTheReceiver(t *testing.T) {
	root := gnssRoot(t)
	feeds := testGNSSFeeds()
	if err := feeds.start(root, "rt", sessionPersona{Model: "Pixel 9", Serial: "S"}); err != nil {
		t.Fatal(err)
	}
	defer feeds.stop("rt")
	// Asked before any place is set, as the HAL does when Android starts it.
	if answer := askGNSS(t, root, "rt", gnssGetReceiver); !strings.HasPrefix(answer, "Receiver,2023,1,S.LSI,K042,") ||
		!strings.HasSuffix(answer, gnssRecordEnd) {
		t.Fatalf("receiver answer %q", answer)
	}
	feeds.set("rt", &tokyo)
	if answer := askGNSS(t, root, "rt", gnssGetLocation); !strings.Contains(answer, "\nSv,") || !strings.Contains(answer, "\nNmea,$GNGGA,") {
		t.Fatalf("location answer has no sky: %q", answer[:min(200, len(answer))])
	}
}
