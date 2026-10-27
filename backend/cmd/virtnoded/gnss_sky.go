package main

import (
	_ "embed"
	"encoding/binary"
	"encoding/csv"
	"errors"
	"fmt"
	"hash/fnv"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// A runtime's sky: the satellites a phone at its place would track now, each
// with the signal strength it would have, which the GNSS HAL reports with
// every fix (gnss.go) in place of the default HAL's fixed list. The orbits
// are the real ones, built in (assets/gnss-orbits.csv, written by
// deploy/aleph/refresh-gnss-orbits.py) and moved on from their mean elements
// with the Earth's oblateness: within about a degree of the real sky for a
// month after a refresh, so every runtime at a place sees the sky a phone
// there would.

// gnssConstellation is Android's GnssConstellationType.
type gnssConstellation int

const (
	gnssGPS     gnssConstellation = 1
	gnssSBAS    gnssConstellation = 2
	gnssGLONASS gnssConstellation = 3
	gnssQZSS    gnssConstellation = 4
	gnssBeiDou  gnssConstellation = 5
	gnssGalileo gnssConstellation = 6
	gnssIRNSS   gnssConstellation = 7
)

// IGnssCallback.GnssSvFlags.
const (
	gnssSvHasEphemeris        = 1
	gnssSvHasAlmanac          = 2
	gnssSvUsedInFix           = 4
	gnssSvHasCarrierFrequency = 8
)

const (
	// Phones track satellites a few degrees above the horizon and use those
	// clear of the clutter near it.
	gnssTrackElevation = 5.0
	gnssUseElevation   = 10.0
	gnssUseCN0         = 25.0
	// The L5 band is weaker: receivers hold it only on stronger satellites.
	gnssTrackL5CN0 = 27.0
	// More than a dual-frequency phone sees anywhere; the record stays small.
	gnssMaxSignals = 96

	earthMu         = 398600.4418 // km³/s²
	earthRadius     = 6378.137    // km, WGS 84
	earthFlattening = 1 / 298.257223563
	earthJ2         = 1.08262668e-3
)

//go:embed assets/gnss-orbits.csv
var gnssOrbitsCSV string

var gnssOrbits = mustParseGNSSOrbits(gnssOrbitsCSV)

// gnssOrbit is one satellite: the number receivers call it by and its mean
// elements at an epoch.
type gnssOrbit struct {
	constellation  gnssConstellation
	svid           int
	glonassChannel int
	// l5 is whether it sends the L5-band civil signal (GPS and QZSS L5,
	// Galileo E5a, BeiDou B2a, NavIC L5); b1c, for BeiDou, B1C beside B1I.
	l5, b1c bool
	epoch   time.Time
	// Revolutions a day, and half its rate (revolutions a day²).
	meanMotion, meanMotionDot float64
	eccentricity              float64
	// Degrees.
	inclination, raan, argPerigee, meanAnomaly float64
}

var gnssConstellationNames = map[string]gnssConstellation{
	"gps": gnssGPS, "sbas": gnssSBAS, "glonass": gnssGLONASS, "qzss": gnssQZSS,
	"beidou": gnssBeiDou, "galileo": gnssGalileo, "irnss": gnssIRNSS,
}

func mustParseGNSSOrbits(text string) []gnssOrbit {
	orbits, err := parseGNSSOrbits(text)
	if err != nil {
		panic(fmt.Sprintf("assets/gnss-orbits.csv: %v", err))
	}
	return orbits
}

func parseGNSSOrbits(text string) ([]gnssOrbit, error) {
	reader := csv.NewReader(strings.NewReader(text))
	reader.Comment = '#'
	records, err := reader.ReadAll()
	if err != nil {
		return nil, err
	}
	if len(records) < 2 {
		return nil, errors.New("no orbits")
	}
	column := map[string]int{}
	for index, name := range records[0] {
		column[name] = index
	}
	for _, name := range []string{"constellation", "svid", "glonass_channel", "l5", "b1c", "epoch", "mean_motion",
		"mean_motion_dot", "eccentricity", "inclination", "raan", "arg_perigee", "mean_anomaly"} {
		if _, ok := column[name]; !ok {
			return nil, fmt.Errorf("no %s column", name)
		}
	}
	orbits := make([]gnssOrbit, 0, len(records)-1)
	for line, record := range records[1:] {
		field := func(name string) string { return record[column[name]] }
		number := func(name string) float64 {
			if err != nil {
				return 0
			}
			var value float64
			value, err = strconv.ParseFloat(field(name), 64)
			return value
		}
		constellation, ok := gnssConstellationNames[field("constellation")]
		if !ok {
			return nil, fmt.Errorf("line %d: constellation %q", line+2, field("constellation"))
		}
		orbit := gnssOrbit{
			constellation: constellation,
			l5:            field("l5") == "1",
			b1c:           field("b1c") == "1",
			meanMotion:    number("mean_motion"),
			meanMotionDot: number("mean_motion_dot"),
			eccentricity:  number("eccentricity"),
			inclination:   number("inclination"),
			raan:          number("raan"),
			argPerigee:    number("arg_perigee"),
			meanAnomaly:   number("mean_anomaly"),
		}
		svid := number("svid")
		if channel := field("glonass_channel"); channel != "" && err == nil {
			var value int
			value, err = strconv.Atoi(channel)
			orbit.glonassChannel = value
		}
		if err == nil {
			orbit.epoch, err = time.Parse("2006-01-02T15:04:05.999999Z", field("epoch"))
		}
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", line+2, err)
		}
		orbit.svid = int(svid)
		if orbit.meanMotion <= 0 || orbit.eccentricity < 0 || orbit.eccentricity >= 1 {
			return nil, fmt.Errorf("line %d: not an orbit", line+2)
		}
		orbits = append(orbits, orbit)
	}
	return orbits, nil
}

func radians(degrees float64) float64 { return degrees * math.Pi / 180 }
func degrees(radians float64) float64 { return radians * 180 / math.Pi }

// position is where the satellite is at t, in Earth-fixed coordinates (km):
// its mean elements moved on by Kepler's equation, with the drift of the
// node, the perigee and the mean anomaly that the Earth's oblateness (J2)
// causes, then turned with the Earth by Greenwich sidereal time.
func (o gnssOrbit) position(t time.Time) [3]float64 {
	seconds := t.Sub(o.epoch).Seconds()
	days := seconds / 86400
	motion := o.meanMotion * 2 * math.Pi / 86400 // rad/s
	axis := math.Cbrt(earthMu / (motion * motion))
	e := o.eccentricity
	inclination := radians(o.inclination)
	cosI := math.Cos(inclination)
	semilatus := axis * (1 - e*e)
	j2 := 1.5 * earthJ2 * (earthRadius / semilatus) * (earthRadius / semilatus) * motion
	node := radians(o.raan) - j2*cosI*seconds
	perigee := radians(o.argPerigee) + j2/2*(5*cosI*cosI-1)*seconds
	mean := radians(o.meanAnomaly) + (motion+j2/2*math.Sqrt(1-e*e)*(3*cosI*cosI-1))*seconds +
		2*math.Pi*o.meanMotionDot*days*days

	eccentric := mean
	for range 8 {
		eccentric -= (eccentric - e*math.Sin(eccentric) - mean) / (1 - e*math.Cos(eccentric))
	}
	x := axis * (math.Cos(eccentric) - e)
	y := axis * math.Sqrt(1-e*e) * math.Sin(eccentric)

	cosW, sinW := math.Cos(perigee), math.Sin(perigee)
	cosO, sinO := math.Cos(node), math.Sin(node)
	sinI := math.Sin(inclination)
	inertial := [3]float64{
		(cosO*cosW-sinO*sinW*cosI)*x + (-cosO*sinW-sinO*cosW*cosI)*y,
		(sinO*cosW+cosO*sinW*cosI)*x + (-sinO*sinW+cosO*cosW*cosI)*y,
		sinW*sinI*x + cosW*sinI*y,
	}
	theta := greenwichSiderealTime(t)
	cosT, sinT := math.Cos(theta), math.Sin(theta)
	return [3]float64{
		cosT*inertial[0] + sinT*inertial[1],
		-sinT*inertial[0] + cosT*inertial[1],
		inertial[2],
	}
}

// greenwichSiderealTime is the Earth's rotation angle at t (IAU 1982), with
// UTC for UT1: a second off is a few metres of satellite position.
func greenwichSiderealTime(t time.Time) float64 {
	days := float64(t.UnixNano())/86400e9 + 2440587.5 - 2451545.0
	centuries := days / 36525
	angle := 280.46061837 + 360.98564736629*days + 0.000387933*centuries*centuries -
		centuries*centuries*centuries/38710000
	return radians(math.Mod(angle, 360))
}

// earthFixed is a place on the WGS 84 ellipsoid in Earth-fixed coordinates
// (km).
func earthFixed(latitude, longitude, altitudeKm float64) [3]float64 {
	lat, lon := radians(latitude), radians(longitude)
	e2 := earthFlattening * (2 - earthFlattening)
	normal := earthRadius / math.Sqrt(1-e2*math.Sin(lat)*math.Sin(lat))
	return [3]float64{
		(normal + altitudeKm) * math.Cos(lat) * math.Cos(lon),
		(normal + altitudeKm) * math.Cos(lat) * math.Sin(lon),
		(normal*(1-e2) + altitudeKm) * math.Sin(lat),
	}
}

// lookAngles are the elevation and azimuth (degrees) of a satellite from a
// place, and the unit vector toward it (east, north, up).
func lookAngles(place gnssPlace, observer, satellite [3]float64) (elevation, azimuth float64, toward [3]float64) {
	lat, lon := radians(place.latitude), radians(place.longitude)
	dx, dy, dz := satellite[0]-observer[0], satellite[1]-observer[1], satellite[2]-observer[2]
	east := -math.Sin(lon)*dx + math.Cos(lon)*dy
	north := -math.Sin(lat)*math.Cos(lon)*dx - math.Sin(lat)*math.Sin(lon)*dy + math.Cos(lat)*dz
	up := math.Cos(lat)*math.Cos(lon)*dx + math.Cos(lat)*math.Sin(lon)*dy + math.Sin(lat)*dz
	distance := math.Sqrt(east*east + north*north + up*up)
	azimuth = math.Mod(degrees(math.Atan2(east, north))+360, 360)
	return degrees(math.Asin(up / distance)), azimuth, [3]float64{east / distance, north / distance, up / distance}
}

// gnssSatellite is one signal the receiver tracks, as the HAL reports it.
type gnssSatellite struct {
	constellation      gnssConstellation
	svid               int
	signal             gnssSignal
	cn0, basebandCN0   float64
	elevation, azimuth float64
	flags              int
	toward             [3]float64
	glonassChannel     int
	// exactElevation orders signals for the cap: rounded, two would tie and
	// swap places from one fix to the next.
	exactElevation float64
}

func (s gnssSatellite) usedInFix() bool { return s.flags&gnssSvUsedInFix != 0 }

// gnssSky is what the device at the place tracks at t: every signal its
// receiver takes from each satellite above the horizon, strongest overhead.
func gnssSky(device gnssDevice, place gnssPlace, t time.Time, orbits []gnssOrbit) []gnssSatellite {
	observer := earthFixed(place.latitude, place.longitude, 0)
	var sky []gnssSatellite
	for _, orbit := range orbits {
		signals := device.receiver.signals(orbit)
		if len(signals) == 0 || orbit.constellation == gnssIRNSS && !navICReaches(place) {
			continue
		}
		elevation, azimuth, toward := lookAngles(place, observer, orbit.position(t))
		if elevation < gnssTrackElevation {
			continue
		}
		for _, signal := range signals {
			steady, cn0 := device.cn0(orbit, signal, elevation, t)
			// On its steady strength, so it doesn't come and go every fix.
			if signal.l5Band && steady < gnssTrackL5CN0 {
				continue
			}
			flags := gnssSvHasCarrierFrequency
			if orbit.constellation != gnssSBAS {
				flags |= gnssSvHasEphemeris | gnssSvHasAlmanac
				if elevation >= gnssUseElevation && cn0 >= gnssUseCN0 {
					flags |= gnssSvUsedInFix
				}
			}
			sky = append(sky, gnssSatellite{
				constellation:  orbit.constellation,
				svid:           orbit.svid,
				signal:         signal,
				cn0:            cn0,
				basebandCN0:    math.Round((cn0-3.5)*10) / 10,
				elevation:      math.Round(elevation*10) / 10,
				azimuth:        math.Round(azimuth*10) / 10,
				flags:          flags,
				toward:         toward,
				glonassChannel: orbit.glonassChannel,
				exactElevation: elevation,
			})
		}
	}
	// Over the cap, the lowest go, as a receiver drops the weakest: only when
	// one rises or sets does the set change.
	if len(sky) > gnssMaxSignals {
		sort.SliceStable(sky, func(i, j int) bool { return sky[i].exactElevation > sky[j].exactElevation })
		sky = sky[:gnssMaxSignals]
	}
	sort.SliceStable(sky, func(i, j int) bool {
		a, b := sky[i], sky[j]
		if a.constellation != b.constellation {
			return a.constellation < b.constellation
		}
		if a.svid != b.svid {
			return a.svid < b.svid
		}
		return a.signal.carrierHz > b.signal.carrierHz
	})
	return sky
}

// gnssDevice is the phone a runtime's persona claims: its receiver, and what
// makes its signal strengths its own (the antenna, each satellite's path).
type gnssDevice struct {
	receiver gnssReceiver
	seed     uint64
	antenna  float64
}

func newGNSSDevice(persona sessionPersona) gnssDevice {
	device := gnssDevice{receiver: gnssReceiverFor(persona.Model), seed: gnssSeed(persona.Serial + "/" + persona.Model)}
	device.antenna = 3 * (device.unit("antenna") - 0.5)
	return device
}

func gnssSeed(value string) uint64 {
	hasher := fnv.New64a()
	_, _ = hasher.Write([]byte(value))
	return hasher.Sum64()
}

// unit is a number in [0, 1) fixed by the device and the key.
func (d gnssDevice) unit(key string) float64 {
	hasher := fnv.New64a()
	var seed [8]byte
	binary.LittleEndian.PutUint64(seed[:], d.seed)
	_, _ = hasher.Write(seed[:])
	_, _ = hasher.Write([]byte(key))
	return float64(hasher.Sum64()>>11) / (1 << 53)
}

// cn0 is the signal's strength (dB-Hz) as a phone outdoors sees it: about 26
// near the horizon to 45 overhead, a few dB less on the L5 band and from
// GLONASS, with an offset of its own for the antenna and each satellite
// (steady), and with a slow wander over tens of seconds as well, never a jump
// from one fix to the next (cn0).
func (d gnssDevice) cn0(orbit gnssOrbit, signal gnssSignal, elevation float64, t time.Time) (steady, cn0 float64) {
	strength := 19 + 26*math.Pow(math.Sin(radians(math.Max(elevation, 0.5))), 0.55)
	if signal.l5Band {
		strength -= 4
	}
	if orbit.constellation == gnssGLONASS {
		strength -= 2
	}
	key := fmt.Sprintf("%d/%d/%.0f/", orbit.constellation, orbit.svid, signal.carrierHz)
	strength += d.antenna + 5*(d.unit(key+"offset")-0.5)
	seconds := float64(t.UnixMilli()) / 1000
	slow, fast := 40+50*d.unit(key+"slow"), 11+14*d.unit(key+"fast")
	wander := 1.2*math.Sin(2*math.Pi*(seconds/slow+d.unit(key+"slow phase"))) +
		0.8*math.Sin(2*math.Pi*(seconds/fast+d.unit(key+"fast phase")))
	return strength, math.Round(math.Max(12, math.Min(50, strength+wander))*10) / 10
}

// gnssDOP is how the satellites used in the fix are spread over the sky.
type gnssDOP struct {
	position, horizontal, vertical float64
}

// dilutionOfPrecision of the satellites used in the fix, one receiver clock
// for every system; zero with fewer than four.
func dilutionOfPrecision(sky []gnssSatellite) gnssDOP {
	type key struct {
		constellation gnssConstellation
		svid          int
	}
	seen := map[key]bool{}
	var normal [4][4]float64
	for _, satellite := range sky {
		k := key{satellite.constellation, satellite.svid}
		if !satellite.usedInFix() || seen[k] {
			continue
		}
		seen[k] = true
		row := [4]float64{-satellite.toward[0], -satellite.toward[1], -satellite.toward[2], 1}
		for i := range 4 {
			for j := range 4 {
				normal[i][j] += row[i] * row[j]
			}
		}
	}
	if len(seen) < 4 {
		return gnssDOP{}
	}
	inverse, ok := invert4(normal)
	if !ok {
		return gnssDOP{}
	}
	return gnssDOP{
		position:   math.Sqrt(inverse[0][0] + inverse[1][1] + inverse[2][2]),
		horizontal: math.Sqrt(inverse[0][0] + inverse[1][1]),
		vertical:   math.Sqrt(inverse[2][2]),
	}
}

// invert4 inverts a 4×4 matrix by Gauss-Jordan elimination.
func invert4(m [4][4]float64) ([4][4]float64, bool) {
	var inverse [4][4]float64
	for i := range 4 {
		inverse[i][i] = 1
	}
	for column := range 4 {
		pivot := column
		for row := column + 1; row < 4; row++ {
			if math.Abs(m[row][column]) > math.Abs(m[pivot][column]) {
				pivot = row
			}
		}
		if math.Abs(m[pivot][column]) < 1e-12 {
			return inverse, false
		}
		m[column], m[pivot] = m[pivot], m[column]
		inverse[column], inverse[pivot] = inverse[pivot], inverse[column]
		scale := m[column][column]
		for j := range 4 {
			m[column][j] /= scale
			inverse[column][j] /= scale
		}
		for row := range 4 {
			if row == column {
				continue
			}
			factor := m[row][column]
			for j := range 4 {
				m[row][j] -= factor * m[column][j]
				inverse[row][j] -= factor * inverse[column][j]
			}
		}
	}
	return inverse, true
}
