#!/usr/bin/env python3
"""Writes the satellite orbits virtnoded draws a runtime's sky from.

    deploy/aleph/refresh-gnss-orbits.py [CELESTRAK_JSON IGS_METADATA_SNX]

CelesTrak's GNSS group (mean elements, refreshed daily) gives each
satellite's orbit; the IGS satellite metadata gives the number a receiver
calls it by (PRN, or GLONASS slot and frequency channel) and its block,
which says which signals it sends. Without arguments both are fetched.
The result, backend/cmd/virtnoded/assets/gnss-orbits.csv, is built into the
node: refresh it at each VM image release, as the sky drifts from the real
one by about a degree a month (backend/cmd/virtnoded/gnss_sky.go).
"""
import csv
import datetime
import io
import json
import os
import re
import sys
import urllib.request

CELESTRAK = "https://celestrak.org/NORAD/elements/gp.php?GROUP=gnss&FORMAT=json"
IGS = "https://files.igs.org/pub/station/general/igs_satellite_metadata.snx"
OUT = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "..",
                   "backend", "cmd", "virtnoded", "assets", "gnss-orbits.csv")

CONSTELLATIONS = {"G": "gps", "R": "glonass", "E": "galileo", "C": "beidou", "J": "qzss", "I": "irnss"}
# The numbers Android uses for each system's satellites.
SVID_RANGES = {"gps": (1, 32), "glonass": (1, 24), "galileo": (1, 36), "beidou": (1, 63),
               "qzss": (193, 200), "irnss": (1, 14), "sbas": (120, 158)}


def fetch(url):
    with urllib.request.urlopen(url, timeout=60) as response:
        return response.read().decode()


def sinex_blocks(text):
    blocks, name = {}, None
    for line in text.splitlines():
        if line.startswith("+"):
            name = line[1:].split()[0]
            blocks[name] = []
        elif line.startswith("-"):
            name = None
        elif name and line.startswith(" "):
            blocks[name].append(line)
    return blocks


def sinex_time(value):
    year, day, seconds = (int(part) for part in value.split(":"))
    if year == 0:
        return None
    return datetime.datetime(year, 1, 1, tzinfo=datetime.timezone.utc) + datetime.timedelta(days=day - 1, seconds=seconds)


def current(rows, now):
    """The rows (SVN, from, to, rest) valid now, by SVN."""
    out = {}
    for line in rows:
        fields = line.split(None, 3)
        start, end = sinex_time(fields[1]), sinex_time(fields[2])
        if start and start <= now and (end is None or end > now):
            out[fields[0]] = fields[3] if len(fields) > 3 else ""
    return out


def signals(constellation, block):
    """Whether the satellite sends the L5-band civil signal (GPS and QZSS L5,
    Galileo E5a, BeiDou B2a, NavIC L5), and, for BeiDou, B1C beside B1I."""
    if constellation == "gps":
        return block in ("GPS-IIF", "GPS-IIIA", "GPS-IIIF"), False
    if constellation in ("galileo", "qzss", "irnss"):
        return True, False
    if constellation == "beidou":
        # BeiDou-3's MEO and IGSO satellites; its GEOs send B1I and B3I only.
        modern = block.startswith("BDS-3M") or block == "BDS-3I"
        return modern, modern
    return False, False


def main():
    now = datetime.datetime.now(datetime.timezone.utc)
    if len(sys.argv) == 3:
        celestrak = open(sys.argv[1]).read()
        igs = open(sys.argv[2]).read()
    else:
        celestrak, igs = fetch(CELESTRAK), fetch(IGS)
    release = re.search(r"release (\d+)", igs)
    blocks = sinex_blocks(igs)
    identity = {}
    for line in blocks["SATELLITE/IDENTIFIER"]:
        svn, cospar, _, block = line.split()[:4]
        identity[cospar] = (svn, block, line)
    prns = current(blocks["SATELLITE/PRN"], now)
    channels = current(blocks["SATELLITE/FREQUENCY_CHANNEL"], now)

    rows = []
    for gp in json.loads(celestrak):
        name = gp["OBJECT_NAME"]
        # SBAS satellites, which the metadata leaves out: "(WAAS/PRN 131)".
        sbas = re.search(r"\(([A-Z]+)/PRN (\d+)\)", name)
        if sbas and sbas.group(1) != "QZSS":
            constellation, svid, channel, block, l5, b1c = "sbas", int(sbas.group(2)), "", "SBAS", False, False
        else:
            known = identity.get(gp["OBJECT_ID"])
            if not known:
                continue
            svn, block, line = known
            prn = prns.get(svn)
            # Experimental BeiDou-3 satellites and NavIC's stranded NVS-02.
            if not prn or block.startswith("BDS-3S") or "GTO" in line or "GTO" in prn:
                continue
            constellation = CONSTELLATIONS[prn[0]]
            svid = int(prn[1:3]) + (192 if constellation == "qzss" else 0)
            channel = ""
            if constellation == "glonass":
                if svn not in channels:
                    continue
                channel = channels[svn].split()[0]
            l5, b1c = signals(constellation, block)
        low, high = SVID_RANGES[constellation]
        if not low <= svid <= high:
            continue
        rows.append([constellation, svid, channel, int(l5), int(b1c), block, gp["NORAD_CAT_ID"],
                     gp["EPOCH"] + "Z", gp["MEAN_MOTION"], gp["ECCENTRICITY"], gp["INCLINATION"],
                     gp["RA_OF_ASC_NODE"], gp["ARG_OF_PERICENTER"], gp["MEAN_ANOMALY"], gp["MEAN_MOTION_DOT"]])
    order = list(SVID_RANGES)
    rows.sort(key=lambda row: (order.index(row[0]), row[1], row[7]))
    # One orbit per satellite: the newest, should two share a number.
    unique = {}
    for row in rows:
        unique[(row[0], row[1])] = row
    rows = sorted(unique.values(), key=lambda row: (order.index(row[0]), row[1]))

    out = io.StringIO()
    out.write("# GNSS orbits for virtnoded's sky (gnss_sky.go): CelesTrak's GNSS mean elements\n")
    out.write(f"# joined with the IGS satellite metadata (release {release.group(1) if release else '?'}) by COSPAR ID.\n")
    out.write(f"# Written by deploy/aleph/refresh-gnss-orbits.py on {now:%Y-%m-%d}; refresh at each VM image release.\n")
    writer = csv.writer(out, lineterminator="\n")
    writer.writerow(["constellation", "svid", "glonass_channel", "l5", "b1c", "block", "norad", "epoch",
                     "mean_motion", "eccentricity", "inclination", "raan", "arg_perigee", "mean_anomaly",
                     "mean_motion_dot"])
    writer.writerows(rows)
    with open(OUT, "w") as f:
        f.write(out.getvalue())
    counts = {}
    for row in rows:
        counts[row[0]] = counts.get(row[0], 0) + 1
    print(f"{OUT}: {len(rows)} satellites {counts}")


if __name__ == "__main__":
    main()
