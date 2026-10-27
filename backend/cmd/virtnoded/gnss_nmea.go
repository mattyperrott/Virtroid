package main

import (
	"fmt"
	"math"
	"slices"
	"strings"
	"time"
)

// NMEA 0183 (4.11) sentences for one fix and its sky, which the HAL hands to
// apps that listen for NMEA (OnNmeaMessageListener) in place of the default
// HAL's "$TEST" line: GGA, GSA and GSV for each system, RMC and VTG, all
// describing the same fix and satellites the HAL reports.

type gnssNMEASystem struct {
	constellation gnssConstellation
	talker        string
	// systemID is GSA's system ID; 0 for SBAS, never used in a fix.
	systemID int
}

// In the order receivers send them; SBAS goes with GPS.
var gnssNMEASystems = []gnssNMEASystem{
	{gnssGPS, "GP", 1}, {gnssSBAS, "GP", 0}, {gnssGLONASS, "GL", 2}, {gnssGalileo, "GA", 3},
	{gnssBeiDou, "GB", 4}, {gnssQZSS, "GQ", 5}, {gnssIRNSS, "GI", 6},
}

// nmeaSatelliteID is the number NMEA calls a satellite by.
func nmeaSatelliteID(constellation gnssConstellation, svid int) int {
	switch constellation {
	case gnssSBAS:
		return svid - 87
	case gnssGLONASS:
		return svid + 64
	case gnssQZSS:
		return svid - 192
	}
	return svid
}

func nmeaSentence(body string) string {
	var checksum byte
	for index := 0; index < len(body); index++ {
		checksum ^= body[index]
	}
	return fmt.Sprintf("$%s*%02X", body, checksum)
}

// nmeaCoordinate is degrees as NMEA's (d)ddmm.mmmmm and a hemisphere.
func nmeaCoordinate(value float64, width int, positive, negative string) (string, string) {
	hemisphere := positive
	if value < 0 {
		hemisphere = negative
	}
	value = math.Abs(value)
	whole := math.Floor(value)
	minutes := (value - whole) * 60
	// Rounding can carry the minutes to 60.
	if math.Round(minutes*1e5)/1e5 >= 60 {
		whole, minutes = whole+1, 0
	}
	return fmt.Sprintf("%0*d%08.5f", width, int(whole), minutes), hemisphere
}

// gnssNMEA describes the fix (at fix.time, at its place) and its sky.
func gnssNMEA(fix gnssFix, sky []gnssSatellite) []string {
	at := fix.time.UTC()
	clock := at.Format("150405") + fmt.Sprintf(".%02d", at.Nanosecond()/int(10*time.Millisecond))
	latitude, north := nmeaCoordinate(fix.latitude, 2, "N", "S")
	longitude, east := nmeaCoordinate(fix.longitude, 3, "E", "W")
	dop := dilutionOfPrecision(sky)

	used := map[gnssConstellation][]int{}
	seen := map[[2]int]bool{}
	for _, satellite := range sky {
		key := [2]int{int(satellite.constellation), satellite.svid}
		if satellite.usedInFix() && !seen[key] {
			seen[key] = true
			used[satellite.constellation] = append(used[satellite.constellation], nmeaSatelliteID(satellite.constellation, satellite.svid))
		}
	}

	sentences := []string{nmeaSentence(fmt.Sprintf("GNGGA,%s,%s,%s,%s,%s,1,%02d,%.1f,0.0,M,0.0,M,,",
		clock, latitude, north, longitude, east, min(len(seen), 99), dop.horizontal))}
	for _, system := range gnssNMEASystems {
		ids := used[system.constellation]
		if system.systemID == 0 || len(ids) == 0 {
			continue
		}
		for start := 0; start < len(ids); start += 12 {
			fields := make([]string, 12)
			for index, id := range ids[start:min(start+12, len(ids))] {
				fields[index] = fmt.Sprintf("%02d", id)
			}
			sentences = append(sentences, nmeaSentence(fmt.Sprintf("GNGSA,A,3,%s,%.1f,%.1f,%.1f,%X",
				strings.Join(fields, ","), dop.position, dop.horizontal, dop.vertical, system.systemID)))
		}
	}
	talkers := map[gnssConstellation]string{}
	var talkerOrder []string
	for _, system := range gnssNMEASystems {
		talkers[system.constellation] = system.talker
		if !slices.Contains(talkerOrder, system.talker) {
			talkerOrder = append(talkerOrder, system.talker)
		}
	}
	for _, talker := range talkerOrder {
		// One run of GSV sentences per talker and signal, as NMEA 4.11 has
		// it: SBAS satellites go in GPS's.
		var order []int
		bySignal := map[int][]gnssSatellite{}
		for _, satellite := range sky {
			if talkers[satellite.constellation] != talker {
				continue
			}
			id := satellite.signal.nmeaID
			if _, ok := bySignal[id]; !ok {
				order = append(order, id)
			}
			bySignal[id] = append(bySignal[id], satellite)
		}
		for _, id := range order {
			satellites := bySignal[id]
			total := (len(satellites) + 3) / 4
			for message := range total {
				var body strings.Builder
				fmt.Fprintf(&body, "%sGSV,%d,%d,%02d", talker, total, message+1, len(satellites))
				for _, satellite := range satellites[message*4 : min(message*4+4, len(satellites))] {
					fmt.Fprintf(&body, ",%02d,%02d,%03d,%02d", nmeaSatelliteID(satellite.constellation, satellite.svid),
						int(math.Round(satellite.elevation)), int(math.Round(satellite.azimuth))%360, int(math.Round(satellite.cn0)))
				}
				fmt.Fprintf(&body, ",%X", id)
				sentences = append(sentences, nmeaSentence(body.String()))
			}
		}
	}
	sentences = append(sentences,
		nmeaSentence(fmt.Sprintf("GNRMC,%s,A,%s,%s,%s,%s,0.000,,%s,,,A,V",
			clock, latitude, north, longitude, east, at.Format("020106"))),
		nmeaSentence("GNVTG,,T,,M,0.000,N,0.000,K,A"))
	return sentences
}
