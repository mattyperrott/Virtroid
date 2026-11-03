# Fauxx

[Fauxx](https://github.com/digital-grease/fauxx) (AGPL-3.0-only) poisons
ad-tech and data-broker profiles: in the background it generates plausible
decoy activity from the phone (searches, page visits, DNS lookups) for a
rotating synthetic persona, so trackers can't tell the user's real interests
from the decoys. The owner had it built into the image on 2026-11-02, with a
home-screen widget of its own.

Fauxx has no widget, and every component of it is private to the app, so a
widget can't be a separate app: it is a change to Fauxx. The image carries
Fauxx 0.9.3 with that change (`patches/`), built by `build.sh` and signed
with Virtroid's release key, so F-Droid doesn't update this copy; a new
version comes with a new image. If upstream takes the widget, the image can
go back to F-Droid's own build.

## The change

`patches/0001` against tag `v0.9.3`, described in the `VIRTROID.md` it adds:

- **The widget** (`com.fauxx.widget`), the full width of the home screen and
  two rows tall: a "Fauxx" header whose "?" opens the FAQ; a status card
  saying whether the engine is generating decoys, why it is paused, or
  that it is off, with a switch that starts it as Fauxx's own notification
  action does and stops it as the dashboard's switch does; and two tiles,
  every decoy action Fauxx has made and its hourly rate over the last day
  (the dashboard's figure). Resized to one row (Android 12+), it keeps the
  status and switch, with the counts under the headline while it runs.
  Before the user has accepted Fauxx's consent dialog the switch opens the
  app instead, so the widget never skips it. It shows no persona,
  categories or log, and never polls: the engine's service publishes each
  state it shows in its notification, the engine counts each action it
  records (Settings' "Clear All Data" forgets the count), and the rate is
  read from the action log when the widget draws. System fonts and
  Virtroid's palette (dark, with a lime accent), so it pairs with Shield's
  widget on the home screen; on Android 12+ the switch is the system's
  own, drawn in those colours.
- **About** says the build is modified, when and by whom, and links here,
  as the AGPL asks of a modified version that people use over a network.
- The version name is `0.9.3-virtroid.2`; the version code stays 903.

`WidgetStatusTest` covers what the widget shows in each case.

## Building

    deploy/aosp/apps/fauxx/build.sh OUT_DIR

It fetches upstream at the pinned commit, applies the patches with fixed
dates (so the source tarball is the same every time), checks the version,
writes `fauxx-source-<version>.tar.gz` (the complete source: upstream's tree
with the change), builds `assembleFullRelease` and signs it. Put `Fauxx.apk`
in `$LINEAGE_ROOT/prebuilts` and the tarball in
`$LINEAGE_ROOT/prebuilt-sources`, pinned in `device/virtroid/prebuilt.sha256`
and `prebuilt-sources.sha256`; the tarball goes out with the VM image like
the other apps' source (`../../README.md`).

To move to a new Fauxx version: change `commit` and `version` in `build.sh`,
rebase the patch onto the new tag (its version line too), and rebuild.
