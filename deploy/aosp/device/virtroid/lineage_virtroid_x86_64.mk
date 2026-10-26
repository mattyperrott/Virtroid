#
# ReDroid's 64-bit-only x86_64 container device (device/redroid, branch
# redroid-16.0.0) with LineageOS's configuration on top. The board stays
# ReDroid's: PRODUCT_DEVICE picks device/redroid/redroid_x86_64_only's
# BoardConfig.mk.
#
# LineageOS's mini phone configuration already leaves out the apps
# components.md cuts from the full one (Aperture, AudioFX, Camelot, Etar,
# Recorder, Seedvault, Twelve); Android.mk cuts the rest, including the
# phone stack, Bluetooth and LineageParts.
#

# Runtimes start ready to use: AOSP's Provision marks the device set up at
# first boot, as in ReDroid's image, instead of LineageOS's setup wizard
# (patches/vendor/lineage).
TARGET_EXCLUDES_LINEAGE_SETUPWIZARD := true

# Ours come before what the makefiles below inherit, so they win: the first
# copy of a file and the first overlay of a resource are the ones used.
#
# The features a runtime declares: AOSP's handheld list without Bluetooth
# and printing, which the image doesn't have.
PRODUCT_COPY_FILES += \
    device/virtroid/handheld_core_hardware.xml:$(TARGET_COPY_OUT_VENDOR)/etc/permissions/handheld_core_hardware.xml
# No Google endpoints by default (captive portal probes off, network time
# from the NTP pool, the browser on DuckDuckGo): overlay/.
PRODUCT_PACKAGE_OVERLAYS += device/virtroid/overlay

$(call inherit-product, device/redroid/redroid_x86_64_only.mk)
$(call inherit-product, vendor/lineage/config/common_mini_phone.mk)

PRODUCT_NAME := lineage_virtroid_x86_64
PRODUCT_DEVICE := redroid_x86_64_only

# adb: the node drives each runtime over adb (installs, shell, the viewer),
# so adbd runs from boot, as a debuggable build's does by default; a user
# build would leave it off. It still accepts only authorized keys
# (ro.adb.secure=0 applies to debuggable builds alone): the node puts its
# public key in the runtime's /data/misc/adb/adb_keys before each start.
PRODUCT_SYSTEM_DEFAULT_PROPERTIES += persist.sys.usb.config=adb

# components.md's cuts (Android.mk).
PRODUCT_PACKAGES += VirtroidRemovePackages

# Built in: F-Droid with its privileged extension (local_manifests/fdroid.xml),
# and the runtime agent (Android.bp). The keyboard is AOSP's LatinIME, which
# LineageOS's configuration already includes.
PRODUCT_PACKAGES += \
    F-Droid \
    F-DroidPrivilegedExtension \
    VirtroidRuntimeAgent
