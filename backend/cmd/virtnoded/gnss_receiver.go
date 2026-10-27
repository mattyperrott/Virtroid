package main

import "slices"

// The GNSS receiver of each phone a persona can claim, as the phone reports
// it to apps: its hardware name and year (LocationManager's
// getGnssHardwareModelName and getGnssYearOfHardware), the systems and
// signals it tracks, and whether it measures carrier phase. From the GPSTest
// database (crowd-sourced from the GPSTest app), as each phone reported it on
// the Android 16 build its persona claims (persona.go), or the nearest one
// the database has.

// gnssSignal is one signal a receiver tracks from a satellite.
type gnssSignal struct {
	carrierHz float64
	// l5Band is the 1176.45 MHz band (GPS and QZSS L5, Galileo E5a, BeiDou
	// B2a, NavIC L5); every other signal is near 1575 MHz.
	l5Band bool
	// code is GnssSignalType's code type; nmeaID the NMEA 4.11 signal ID.
	code   string
	nmeaID int
}

const (
	gnssL1Hz      = 1575.42e6
	gnssL5Hz      = 1176.45e6
	gnssB1IHz     = 1561.098e6
	gnssG1Hz      = 1602e6
	gnssG1Channel = 0.5625e6
)

// gnssReceiver is a phone's GNSS chip and firmware.
type gnssReceiver struct {
	name string
	year int
	// systems are the constellations it tracks; SBAS among them only if it
	// shows SBAS satellites.
	systems []gnssConstellation
	// dualFrequency adds the L5 band; beidouB1C, BeiDou-3's B1C beside B1I.
	dualFrequency bool
	beidouB1C     bool
	carrierPhase  bool
}

var (
	gnssMainSystems = []gnssConstellation{gnssGPS, gnssGLONASS, gnssGalileo, gnssBeiDou, gnssQZSS}
	withIRNSS       = append(slices.Clone(gnssMainSystems), gnssIRNSS)
	withSBAS        = append(slices.Clone(gnssMainSystems), gnssSBAS)

	gnssBroadcomBCM4776 = gnssReceiver{name: "Broadcom, BCM4776, GLL ver. 154.20.24 650028", year: 2020,
		systems: withIRNSS, dualFrequency: true, carrierPhase: true}
	gnssPixel8a = gnssReceiver{name: "S.LSI,K041,SPOTNAV_4.15.4_13_251118_R1_299506", year: 2023,
		systems: gnssMainSystems, beidouB1C: true, carrierPhase: true}
	gnssPixel9 = gnssReceiver{name: "S.LSI,K042,SPOTNAV_4.15.1_62_251117_R1_299155", year: 2023,
		systems: gnssMainSystems, dualFrequency: true, beidouB1C: true, carrierPhase: true}
	gnssQualcommGen9 = gnssReceiver{name: "Qualcomm GNSS GEN9", year: 2022,
		systems: withSBAS, dualFrequency: true, beidouB1C: true}

	gnssReceivers = map[string]gnssReceiver{
		"Pixel 6":     gnssBroadcomBCM4776,
		"Pixel 7":     gnssBroadcomBCM4776,
		"Pixel 8":     gnssBroadcomBCM4776,
		"Pixel 8 Pro": gnssBroadcomBCM4776,
		"Pixel 8a":    gnssPixel8a,
		"Pixel 9":     gnssPixel9,
		"Pixel 9 Pro": gnssPixel9,
		"SM-S901B": {name: "S.LSI,K040,SPOTNAV_4.6.2_7_E23", year: 2021,
			systems: gnssMainSystems, carrierPhase: true},
		"SM-S918B": gnssQualcommGen9,
		"SM-S928B": func() gnssReceiver { r := gnssQualcommGen9; r.year = 2023; return r }(),
		"SM-A556B": {name: "S.LSI,K044,SPOTNAV_4.14.16_99_MR_Rs", year: 2021,
			systems: gnssMainSystems, beidouB1C: true, carrierPhase: true},
		"2312DRA50G": {name: "qcom;MPSS.DE.3.1.1.c1-00021-NETRANI_GENALL_PACK-1.79677.71;", year: 2016,
			systems: withSBAS, beidouB1C: true},
		"CPH2581": {name: "qcom;MPSS.DE.5.0-02708-LANAI_GEN_PACK-1;", year: 2023,
			systems: withSBAS, dualFrequency: true, beidouB1C: true},
		"CPH2653": {name: "qcom;MPSS.DE.7.0-02698-PAKALA_GEN_PACK-1;", year: 2023,
			systems: withSBAS, dualFrequency: true, beidouB1C: true},
		"motorola edge 60 pro": {name: "MTK_MNLD_default,MNL_VER_default", year: 2020,
			systems: withSBAS, dualFrequency: true, beidouB1C: true, carrierPhase: true},
		// Qualcomm's AIDL HAL as Sony ships it, reporting no L5 band.
		"XQ-EC54": {name: "aidl-impl", year: 2023,
			systems: withSBAS, beidouB1C: true},
		"A065": {name: "qcom;MPSS.DE.2.0-01026.2-WAIPIO_GEN_PACK-1.134215.10.142897.3;", year: 2019,
			systems: gnssMainSystems, dualFrequency: true, beidouB1C: true},
	}
)

// gnssReceiverFor is the receiver of the phone model a persona claims; a
// model with none listed gets the most common chip among them.
func gnssReceiverFor(model string) gnssReceiver {
	if receiver, ok := gnssReceivers[model]; ok {
		return receiver
	}
	return gnssQualcommGen9
}

func (r gnssReceiver) tracks(system gnssConstellation) bool {
	return slices.Contains(r.systems, system)
}

// NavIC beams its signals at India and the region around it.
func navICReaches(place gnssPlace) bool {
	return place.latitude >= -30 && place.latitude <= 50 && place.longitude >= 30 && place.longitude <= 130
}

// signals are what the receiver takes from a satellite: its main signal
// near 1575 MHz, BeiDou-3's B1C, and the L5 band on a dual-frequency chip.
// NavIC sends nothing near 1575 MHz, so only dual-frequency chips see it.
func (r gnssReceiver) signals(orbit gnssOrbit) []gnssSignal {
	if !r.tracks(orbit.constellation) {
		return nil
	}
	var signals []gnssSignal
	switch orbit.constellation {
	case gnssGPS, gnssQZSS, gnssSBAS:
		signals = append(signals, gnssSignal{carrierHz: gnssL1Hz, code: "C", nmeaID: 1})
	case gnssGLONASS:
		signals = append(signals, gnssSignal{carrierHz: gnssG1Hz + float64(orbit.glonassChannel)*gnssG1Channel, code: "C", nmeaID: 1})
	case gnssGalileo:
		signals = append(signals, gnssSignal{carrierHz: gnssL1Hz, code: "C", nmeaID: 7})
	case gnssBeiDou:
		signals = append(signals, gnssSignal{carrierHz: gnssB1IHz, code: "I", nmeaID: 1})
		if r.beidouB1C && orbit.b1c {
			signals = append(signals, gnssSignal{carrierHz: gnssL1Hz, code: "P", nmeaID: 3})
		}
	}
	if r.dualFrequency && orbit.l5 {
		switch orbit.constellation {
		case gnssGPS, gnssQZSS:
			signals = append(signals, gnssSignal{carrierHz: gnssL5Hz, l5Band: true, code: "Q", nmeaID: 8})
		case gnssGalileo:
			signals = append(signals, gnssSignal{carrierHz: gnssL5Hz, l5Band: true, code: "Q", nmeaID: 1})
		case gnssBeiDou:
			signals = append(signals, gnssSignal{carrierHz: gnssL5Hz, l5Band: true, code: "P", nmeaID: 5})
		case gnssIRNSS:
			signals = append(signals, gnssSignal{carrierHz: gnssL5Hz, l5Band: true, code: "A", nmeaID: 1})
		}
	}
	return signals
}

// gnssSignalType is a signal a receiver can track (GnssSignalType).
type gnssSignalType struct {
	constellation gnssConstellation
	signal        gnssSignal
}

// signalTypes are the signals the receiver can track, which the HAL reports
// as its capabilities (LocationManager's GnssCapabilities).
func (r gnssReceiver) signalTypes() []gnssSignalType {
	var types []gnssSignalType
	for _, system := range r.systems {
		orbit := gnssOrbit{constellation: system, l5: true, b1c: true}
		for _, signal := range r.signals(orbit) {
			types = append(types, gnssSignalType{constellation: system, signal: signal})
		}
	}
	return types
}
