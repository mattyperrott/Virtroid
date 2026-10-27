# Runtime location feed: node ↔ GNSS HAL contract

Written 2026-10-24. This is the agreement between `virtnoded` and the GNSS
HAL of the custom Android image ([README.md](README.md), item 2 and
milestone 5).

## Status

| Part | State |
| :--- | :--- |
| Sealed region (city or coordinates), chosen in the app | Done. Sealed on the phone, opened only by the runtime's node. |
| Timezone from the region | Done, works on today's stock ReDroid images. |
| Node side of the location feed (this contract) | Done in `backend/cmd/virtnoded/gnss.go`, with the sky, NMEA and receiver (`gnss_sky.go`, `gnss_nmea.go`, `gnss_receiver.go`). Ships with the next VM image. |
| Android side: a GNSS HAL reading the feed | Built into the custom image (`patches/hardware/interfaces` 0001 and 0002, `device/virtroid`). |
| Apps seeing the runtime's place as GPS | Checked end to end on the node's own feed (below). Reaches owners once a VM image carries the custom image and this node. |

Until a runtime runs the custom image, the node serves the feed and nothing
reads it: stock ReDroid has no HAL that does. Location for apps comes from
the custom image, not from this change alone.

## How it fits

```
phone ──sealed region──▶ control plane ──(still sealed)──▶ virtnoded in the VM
                                                              │ opens it
                                         timezone ◀───────────┤ (region.go)
                                                              ▼
                                  /opt/virtroid/gnss/gnss.sock (gnss.go)
                                                              ▲
                              GNSS HAL in the runtime ── connects about once a second
                                                              │
                                     Android location framework ──▶ apps (ordinary GPS fix)
```

## The socket

| | |
| :--- | :--- |
| Path in the container | `/opt/virtroid/gnss/gnss.sock` |
| Path on the node | `<NODE_RUNTIME_ROOT>/<runtime id>/gnss/gnss.sock`, beside the runtime's data, not in it |
| Mount | the node's directory, bind-mounted **read-only** at `/opt/virtroid/gnss` |
| Type | Unix stream socket; the node listens |
| Owner and mode | uid 1021, gid 1021 (Android's `gps`), mode `0660` |

Each runtime has its own directory and socket; a runtime's container mounts
only its own. The guest can't replace the socket or its directory (the mount
is read-only and the node owns them), and apps, which run as their own users,
can't connect, so they still need location permission to learn the place.

## The protocol

It is the fixed-location protocol of AOSP's default GNSS HAL
(`hardware/interfaces/gnss/common/utils/default`: `DeviceFileReader`,
`FixLocationParser`, `GnssReplayUtils`), so the HAL's own parser reads it
unchanged.

For each fix the HAL:

1. connects to the socket;
2. writes `CMD_GET_LOCATION` (16 bytes, no terminator);
3. reads what arrives within its wait (20 ms in AOSP), until the node closes.

The node answers once and closes:

- **With a place:** one record, then the terminator `\n\n\n\n`, with no
  newline before it (the HAL would keep it for the next record):

  ```
  Fix,GPS,<lat>,<lon>,0.0,0.0,<accuracy m>,0.0,<unix ms>,0.5,90.0,0
  Sv,<constellation>,<svid>,<C/N0>,<baseband C/N0>,<elevation>,<azimuth>,<carrier Hz>,<flags>
  ...
  Nmea,$GNGGA,...*hh
  ...
  ```

  The fields are `Fix, Provider, LatitudeDegrees, LongitudeDegrees,
  AltitudeMeters, SpeedMps, AccuracyMeters, BearingDegrees, UnixTimeMillis,
  SpeedAccuracyMps, BearingAccuracyDegrees, ElapsedRealtimeNanos`.
  - Latitude and longitude have 7 decimals (about 1 cm).
  - Each fix wanders up to 3 m from the chosen point and claims 3.5–8 m
    accuracy, as an open-sky phone fix does. A fix that never moved would
    mark the runtime as not a phone.
  - The node knows no altitude, speed or bearing, so they are 0.
  - The HAL stamps elapsed realtime itself, so the last field is 0.
  - The first line is all AOSP's parser reads, so an image without patch
    0002 still gets its fix.
  - Each `Sv` line is one signal the device tracks ([the sky](#the-sky)):
    Android's `GnssConstellationType` and satellite number, signal strength
    (dB-Hz), elevation and azimuth (degrees), carrier frequency and
    `GnssSvFlags`. The HAL reports them with every fix, so a fix carries the
    satellites used in it (`satellites` in its extras) whether or not an app
    listens for the satellite status.
  - Each `Nmea` line is an NMEA 0183 sentence describing the same fix and
    sky (GGA, GSA and GSV for each system, RMC, VTG), which the HAL hands to
    apps listening for NMEA.
- **`CMD_GET_RECEIVER`**, which the HAL asks once as Android starts it: the
  receiver of the phone the persona claims, then the terminator:

  ```
  Receiver,<year>,<flags>,<hardware model name>
  Signal,<constellation>,<carrier Hz>,<code type>
  ...
  ```

  The name comes last, as it can hold commas; flag 1 is carrier phase. The
  HAL reports the name and year (`LocationManager.getGnssHardwareModelName`
  and `getGnssYearOfHardware`), the signals (`GnssCapabilities`), and only
  the capabilities it backs with real data. Without an answer it reports no
  name at all, rather than AOSP's own, which names an emulator.
- **No place, or any other command** (including `CMD_GET_RAWMEASUREMENT`):
  no data. The HAL reads that as "no fix".

## The sky

The satellites a phone at the place would track now, so the HAL never
reports the default HAL's fixed list (13 satellites that never move,
NavIC's among them everywhere).

- **Orbits.** The real ones, built into the node
  (`backend/cmd/virtnoded/assets/gnss-orbits.csv`): CelesTrak's mean
  elements for the GNSS satellites, joined with the IGS satellite metadata
  for each satellite's PRN, GLONASS slot and frequency channel, and the
  signals its block sends. `deploy/aleph/refresh-gnss-orbits.py` rewrites
  the file; run it at each VM image release. The node moves the orbits on
  with Kepler's equation and the Earth's oblateness. Against the US Coast
  Guard's GPS almanac (2026-10-27) the worst satellite was 0.5° off in the
  sky on the day, 1.2° a week on and about 4° a month on, so every runtime
  at a place sees about the sky a phone there would.
- **The receiver.** Each persona's phone tracks what its chip does
  (`gnss_receiver.go`, from the GPSTest database as each phone reported it
  on Android 16): GPS, GLONASS, Galileo, BeiDou and QZSS; the L5 band on
  dual-frequency chips; BeiDou B1C on Samsung LSI and Qualcomm; SBAS
  satellites on Qualcomm, never used in the fix; NavIC only on chips that
  have it, and only where its beam reaches (India and around).
- **Signal strength.** About 26 dB-Hz near the horizon to 45 overhead, less
  on the L5 band (held only on satellites strong enough) and from GLONASS,
  with an offset of its own for the device's antenna and each satellite, and
  a slow wander. Satellites are tracked from 5° above the horizon and used
  in the fix from 10°.
- **Fixed per device.** The strengths and which L5 signals are held follow
  from the persona (its serial and model): another start, another device.
- Without a place there is no sky: the HAL reports no satellites.

## When the place changes

| Event | Feed |
| :--- | :--- |
| Runtime starts | Opens before the container is created (the container mounts its directory). No place yet. |
| Node opens the sealed region | Gets the place, at the moment the timezone is applied. |
| Owner picks another place | The new place, at the node's next reconcile (every 10 s by default). |
| Region removed | No fix from then on. |
| Runtime stops, is wiped, deleted or discarded | Closes; socket and directory removed. |
| Node restarts while the runtime runs | Reopens at the next reconcile; the place comes back when the region is opened again. A socket left by the old process is replaced. |

## What the image must do

1. **Include AOSP's default GNSS HAL** (`android.hardware.gnss-service.example`,
   from `hardware/interfaces/gnss/aidl/default`), built from source in our
   tree.
2. **Let it connect to a socket.** It `open()`s its location path, which a
   Unix socket can't be. Patch `DeviceFileReader::getDataFromDeviceFile` to
   `connect()` with `socket(AF_UNIX, SOCK_STREAM | SOCK_NONBLOCK | SOCK_CLOEXEC)`
   when `stat()` says the path is a socket; the write, wait and read that
   follow stay the same.
3. **Point it at the feed:**
   `debug.location.fixedlocation.devname=/opt/virtroid/gnss/gnss.sock`
   (`GnssReplayUtils::getFixedLocationPath` reads it on every fix).
4. **Report no fix without one.** `Gnss::start` reports
   `Utils::getMockLocation()` (a fixed default position) when there is no
   record. Patch it to report nothing, or a runtime without a region shows
   that position.
5. **Run it as `gps`.** The example's own `.rc` runs it as `nobody`; ship an
   `.rc` with `user gps` and `group gps`, so it can reach the socket.
6. **Have location on by default** (`location_mode`), as a phone ships.
7. **Report the source's sky, NMEA and receiver** (patch 0002), with the sky
   on every fix.
8. **Make up nothing.** The default HAL fills in wherever it has no data of
   its own, and each made-up value marks an emulator: the satellite list,
   the "$TEST" NMEA line, the hardware name "Google, Cuttlefish, AIDL v3",
   raw measurements, navigation messages, batched fixes at a fixed
   position, and a debug position in Mountain View. Patch 0002 reports none
   of them, and advertises only the capabilities it backs (no antenna info,
   satellite PVT or correlation vectors, which none of the personas' phones
   report either).

## How it's checked

- **Node side:** `backend/cmd/virtnoded/gnss_test.go`, `gnss_sky_test.go`
  and `region_test.go`:
  - the record matches the HAL's parser, stays on the globe, and wanders
    only a few metres;
  - only `CMD_GET_LOCATION` with a place gets a fix;
  - runtimes are kept apart, and a stopped runtime's feed is gone;
  - a stale socket is replaced;
  - the socket is closed to other users;
  - coordinates never reach the logs;
  - coordinates are checked: the globe's edges pass, out-of-range, overflowing
    or non-numeric values fail;
  - the built-in orbits cover every system, and every persona's phone has
    its receiver;
  - the sky looks like a phone outdoors at six places through the day (5 to
    14 GPS satellites, 15 or more used, believable strengths and DOP),
    follows the receiver and the place (QZSS high over Tokyo, none from
    London; NavIC only near India), is the same sky for every device with
    strengths of their own, and moves and wanders without jumps;
  - the NMEA sentences carry valid checksums and fit in 82 characters, and
    describe the fix and every tracked signal.
- **Image side (2026-10-27, by hand, on a boot test's kept runtime):** a
  stand-in server on a socket, with the property pointed at it, and a small
  app asking for GPS:
  - with a place, the app got a fix a second (9 in 8 s), ordinary `gps`
    fixes at the place, and `dumpsys location` kept the last one;
  - answering empty, then no socket at all: no fix, and no default position.

  The HAL waits 20 ms for an answer, so a feed that answers later gives no
  fix: a stand-in that ran a command per request was too slow. Those fixes
  carried `satellites=0`; patch 0002 has the HAL report the node's sky with
  every fix.
- **End to end:** `deploy/aleph/test/boot-test.sh` with
  `BOOT_TEST_GNSS_PROBE` (the probe app in `deploy/aosp/test/gnss-probe`,
  built with its `build.sh` on the build host). A stand-in control plane
  (`backend/cmd/standin-control-plane`) gives the node a runtime with a
  region sealed as the phone seals it; the node starts the runtime, opens
  the region and serves the feed, and the probe app in the runtime must see:
  - a GPS fix within about 50 m of the place, not mock, with 8 or more
    satellites used in it;
  - a GNSS hardware name and year that are a phone's;
  - a sky of 20 or more signals, and NMEA with GGA, GSA, GSV and RMC;
  - after the owner picks another place, a fix there and its timezone;
  - without a region, no fix.

  Passed 2026-10-27 with build 16 and this node (`BOOT_TEST_NODE`) on an
  existing VM image. The runtime's persona was a Galaxy A52s; apps saw its
  chip (`qcom,SM_KODIAK,MPSS.HI.4.3.3.c3-…`, 2019), fixes 3 m from the
  place, not mock, with `satellites=45` (53 signals from GPS, SBAS, GLONASS,
  QZSS, BeiDou and Galileo over Tokyo), NMEA with GGA, GSA, GSV, RMC and VTG,
  then fixes in London with its timezone, and none once the region was gone.
  Not yet checked: a map app showing the place.

## Privacy

- The control plane only ever holds the sealed region. The place exists in
  plaintext only inside the attested VM: in the node's memory and on the
  runtime's socket.
- Neither the node's logs nor the runtime logs (which reach the control
  plane) name the place or its timezone.
- Apps need location permission to read it, as on a phone.

## Open

- **Altitude:** the node sends 0. A real phone at a city's altitude reports
  more; a terrain lookup on the phone could add it to the sealed region.
- **Measurement corrections:** the default HAL's corrections interface
  still advertises line-of-sight, excess path length and reflecting plane
  support, while the HAL itself doesn't claim corrections; a phone shows
  either both or neither. Have that interface report no capabilities.
- **Raw measurements:** the HAL advertises them, as every phone does, but
  reports none, rather than the default HAL's made-up ones. The node could
  derive pseudoranges and Doppler from the sky and send them in the format
  AOSP's replay parser reads (`CMD_GET_RAWMEASUREMENT`).
- **Whether the fix should wander at all** is a product choice; it is one
  function in `gnss.go`.
- If the socket patch to the HAL proves awkward, the alternative is a
  pseudo-terminal the HAL can `open()` unchanged. It was not chosen because
  its device numbers don't survive a node restart, which risks one runtime
  reaching another's feed.
