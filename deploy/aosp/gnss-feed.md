# Runtime location feed: node ↔ GNSS HAL contract

Written 2026-10-24. This is the agreement between `virtnoded` and the GNSS
HAL of the custom Android image ([README.md](README.md), item 2 and
milestone 5).

## Status

| Part | State |
| :--- | :--- |
| Sealed region (city or coordinates), chosen in the app | Done. Sealed on the phone, opened only by the runtime's node. |
| Timezone from the region | Done, works on today's stock ReDroid images. |
| Node side of the location feed (this contract) | Done in `backend/cmd/virtnoded/gnss.go`. Ships with the next VM image. |
| Android side: a GNSS HAL reading the feed | **Not built.** Needs the custom image. |
| Apps seeing the runtime's place as GPS | **Not yet.** Only once a runtime runs the custom image. |

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

- **With a place:** one record, then the terminator `\n\n\n\n`:

  ```
  Fix,GPS,<lat>,<lon>,0.0,0.0,<accuracy m>,0.0,<unix ms>,0.5,90.0,0
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
- **No place, or any other command** (including `CMD_GET_RAWMEASUREMENT`):
  no data. The HAL reads that as "no fix".

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

## How it's checked

- **Node side (now):** `backend/cmd/virtnoded/gnss_test.go` and
  `region_test.go`:
  - the record matches the HAL's parser, stays on the globe, and wanders
    only a few metres;
  - only `CMD_GET_LOCATION` with a place gets a fix;
  - runtimes are kept apart, and a stopped runtime's feed is gone;
  - a stale socket is replaced;
  - the socket is closed to other users;
  - coordinates never reach the logs;
  - coordinates are checked: the globe's edges pass, out-of-range, overflowing
    or non-numeric values fail.
- **End to end (milestone 5, needs the custom image):** extend
  `deploy/aleph/test/boot-test.sh`:
  - with a sealed region, `dumpsys location` shows a `gps` fix within a few
    metres of the place, not marked mock;
  - after a change of region, the new place;
  - without a region, no fix;
  - a map app shows the place.

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
- **Whether the fix should wander at all** is a product choice; it is one
  function in `gnss.go`.
- If the socket patch to the HAL proves awkward, the alternative is a
  pseudo-terminal the HAL can `open()` unchanged. It was not chosen because
  its device numbers don't survive a node restart, which risks one runtime
  reaching another's feed.
