# What the custom Android image keeps and cuts

Draft, 2026-10-21, for the build in [README.md](README.md). The aim is the
lightest Android that still runs the apps users install. That makes the image
smaller (every VM downloads it), boots it faster (the viewer waits for boot),
and leaves less to patch and less for apps to probe.

The starting point is LineageOS 23.2 built on ReDroid's device tree
(`remote-android/device_redroid`, branch `redroid-16.0.0`, product
`redroid_x86_64_only`), which inherits AOSP's `aosp_base`. Names below are
what the source calls them; confirm each against `vendor/lineage/config/` and
ReDroid's `redroid.mk` at the first sync.

**Decisions:** *Keep*: Virtroid or ordinary apps need it. *Cut*: nothing
needs it here. *Test*: cut it, then boot-test, because the framework may
expect it. *Decide*: a product choice (listed again at the end).

## What Virtroid itself relies on (keep)

| Component | Why |
| :--- | :--- |
| Framework, `SystemUI`, `Settings`, `Shell`/`adbd` | The node installs apps and runs commands through ADB and `docker exec`; users change permissions in Settings. |
| `PackageInstaller`, `PermissionController` | F-Droid installs and runtime permission prompts. |
| F-Droid and its privileged extension | The app store. |
| Virtroid runtime agent (system app) | Notification forwarding. |
| `DocumentsUI`, `MediaProvider`, `DownloadProvider` | File and photo imports land in Downloads and the gallery; apps' file pickers. `DocumentsUI` is also the Files app. |
| Audio service and `r_submix` audio HAL | Viewer audio and microphone injection. |
| Software codecs (Codec2) | The viewer's H.264 encoding (x264, our `c2.virtroid.x264.encoder`) and apps' media playback. |
| Graphics: SwiftShader/Mesa, ANGLE, gralloc, composer | Rendering without a GPU. |
| Software keymaster and gatekeeper | Apps' keystore and screen-lock keys. |
| Health, power and thermal stub HALs | The framework refuses to boot without them. |
| `tzdata`, ICU | The runtime's region sets its timezone. |
| WebView, a keyboard, a launcher | Apps embed WebView; input; home screen. |
| Contacts, Calendar, Telephony and BlockedNumber *providers* (not their apps) | Many apps query them and crash when they are absent. |
| `NetworkStack`, Conscrypt, other mainline modules | Core networking and TLS; mainline modules are not optional. |

## Cut: LineageOS's own apps

| Component | Decision | Why |
| :--- | :--- | :--- |
| Updater | Cut | Android is updated by shipping a new VM image. |
| Seedvault (backup) | Cut | Runtimes are saved as encrypted snapshots. |
| Aperture (camera) | Cut | No camera hardware; photos are imported from the phone. |
| Trebuchet (launcher) | Cut | Replaced by the chosen launcher. |
| Jelly (browser) | Decide | The owner asked about Vanadium or another light browser; see the browser decision below. |
| Recorder | Cut | Rarely wanted; the microphone is on demand. |
| Glimpse (gallery), Calculator | Keep | Part of the small built-in set (with a browser and Files). |
| Twelve (music), Camelot (PDF), Etar (calendar), DeskClock | Cut | Not in the built-in set; F-Droid has them. |
| Contacts, Dialer, Messaging | Cut | No phone or SIM. |
| AudioFX, LiveDisplay | Cut | No speakers or display hardware to tune. |
| Lineage Health (charging control), FlipFlap | Cut | No battery, no cover. |
| LineageParts and Trust | Cut | Lineage's Settings pages; Settings runs without them. |
| Lineage themes and wallpapers | Decide | Depends on the design. |

## Cut: AOSP's phone apps and services

| Component | Decision | Why |
| :--- | :--- | :--- |
| Phone service (`TeleService`), `rild`, `Stk`, `CarrierConfig`, `SimAppDialog` | Test | No SIM. ReDroid keeps `rild` for the Phone app; drop both together. |
| Cell broadcast receiver | Test | Emergency alerts need a cellular network. A mainline module, so check what removing it breaks. |
| Emergency info, call log backup | Cut | No calls. |
| Bluetooth stack and ReDroid's simulated Bluetooth HAL | Test | No Bluetooth. |
| Wi-Fi HAL stub | Test | ReDroid keeps it because SystemUI expects Wi-Fi; try without. |
| NFC | Cut | No NFC. |
| Print spooler, built-in print service | Cut | No printers. |
| Screensavers (`BasicDreams`, `PhotoTable`), live wallpapers | Cut | Nobody sees them. |
| `ManagedProvisioning` (work profiles) | Cut | Work profiles add a second user's worth of apps. |
| `Provision` / setup wizard | Cut | Runtimes start ready to use. |
| `QuickSearchBox`, `EasterEgg`, `MusicFX`, `Traceur`, `Gallery2`, `Music` | Cut | Unused. |
| `CaptivePortalLogin` | Cut | The VM's network has no captive portal. |
| ClearKey DRM HAL | Cut | Test-only DRM. |
| OMX codecs | Cut | `REDROID_DISABLE_OMX=true`; Codec2 replaces them. |
| Boot animation | Cut | Nobody sees boot; shorter boot. |
| 32-bit libraries | Cut | Build the `_x86_64_only` product, as the current image already is. |

## Size and speed, not removal

| Item | Proposal |
| :--- | :--- |
| ARM app translation (native bridge) | Keep (owner, 2026-10-22): Google's for now, Digitalis if it passes our tests (see Decisions). Checked 2026-10-21: the stock ReDroid 14 image every runtime uses today already includes Google's `libndk_translation` and advertises `x86_64,arm64-v8a`, in about 22 MiB of its 1,574 MiB. Virtroid's own catalogue only lists apps with x86_64 code (2,408 of its 4,473 apps have native code), but F-Droid inside the runtime installs ARM-only apps through translation. SimpleX Chat is one: it ships ARM builds only, so it is not in the catalogue and runs today only because of translation. Leaving it out would break those apps for current users. The cost is a proprietary binary, not size. |
| Fonts | Cut the Noto CJK fonts (owner, 2026-10-21): the largest font files; Chinese, Japanese and Korean text then shows as boxes. |
| Ahead-of-time compilation of system apps | Keep full dexpreopt: a slightly bigger image, but apps start faster on every fresh VM. |
| Dalvik heap settings | Match the runtime's memory limit (3 GiB today) instead of ReDroid's 6 GiB phone profile. |
| Window animation scale | Decide, with the design: shorter animations feel quicker through the viewer. |

## Decisions

Made 2026-10-21:

- **Built-in apps:** a small set: a browser, Files (`DocumentsUI`), a gallery
  (Glimpse) and a calculator. Everything else comes from F-Droid.
- **CJK fonts:** left out.
- **ARM translation:** kept (owner, 2026-10-22); SimpleX and other ARM-only apps need it. Google's `libndk_translation` for now, as in image 16; try the open-source Digitalis on the build host first (see the [plan](README.md), milestone 4).

Still open:

1. **Browser and WebView.** Every app that shows web content uses the system
   WebView, so its engine and how often it is updated matter more than the
   browser's own UI. Options: Vanadium (hardened Chromium, but GrapheneOS
   publishes no x86_64 build, so it means building Chromium ourselves);
   Cromite (hardened Chromium with published x86_64 browser and WebView
   builds, large); or a thin browser on the system WebView, such as Jelly or
   Privacy Browser.
2. **The look:** launcher, wallpaper, theme colours, icons and fonts, from
   the owner's design. Applied so far (2026-10-28, [README.md](README.md),
   look): Android's own, with LineageOS's launcher (without its blur) and
   the notification shade as Android 16 first shipped it; since 2026-10-30
   AOSP's default wallpaper (stock ReDroid's), a five-app dock and squircle
   icons, the owner's picks from side-by-side screenshots.

## Applied in the image (2026-10-26)

`device/virtroid` applies this list; each round is boot-tested.

- **Cut** (`Android.mk`, by overriding the packages): every *Cut* above that
  the image had (Updater, DeskClock, Contacts, Messaging, Gallery2, the print
  services, screensavers, live wallpapers, ManagedProvisioning, EasterEgg,
  MusicFX, CaptivePortalLogin, the CJK fonts), plus LineageOS's command-line
  tools (vim, nano, htop, rsync), which nothing here needs. *Test* cuts,
  second round: the phone stack (TeleService, rild, Stk, SimAppDialog),
  Bluetooth (ReDroid's simulated HAL, the MIDI service, and the feature no
  longer declared, so its service never starts; the Bluetooth APEX stays,
  as it carries the android.bluetooth classes apps link against),
  LineageParts, and the boot animation. Cell broadcast
  and the Wi-Fi HAL stub were already absent. Without LineageParts,
  LineageOS's Trust service had nowhere to send its notifications ("Discover
  Trust" on every new runtime led nowhere), so it no longer starts
  (`overlay/lineage-sdk`, 2026-10-28).
- **Cut with the release keys (2026-10-27):** the CTS shims, an APEX and two
  apps that only Android's compatibility tests use, which come prebuilt with
  Google's test keys (README.md, release keys).
- **Kept, unlike the list:** Provision. It is what marks a runtime set up at
  first boot, in place of LineageOS's setup wizard (left out). The
  *Decide* items (Jelly, LineageOS's themes and wallpapers) stay until
  decided.
- **Built in:** F-Droid 1.23.2 as F-Droid signed it, its privileged
  extension built from source, and the runtime agent the node pins. The
  keyboard is AOSP's LatinIME.
- **No Google endpoints by default:** captive portal probes off, network
  time from the NTP pool, the browser on DuckDuckGo. What remains of Google
  is the ARM translator's binary (Digitalis would replace it).
- **Checked on a running runtime (2026-10-27):** none of the cut packages
  installed; Bluetooth and printing not declared; fonts 82 → 26 MB; F-Droid's
  extension holds the install permission; LatinIME is the default keyboard;
  captive portal mode 0, automatic time off, NTP from the pool. The runtime
  used 1,348 MiB after boot, against 2,136 MiB for the image before the cuts
  (one boot test each).
- **Trade-off to review:** a runtime without Bluetooth declares fewer
  features than any phone, which apps can read, while personas claim real
  phone models.
