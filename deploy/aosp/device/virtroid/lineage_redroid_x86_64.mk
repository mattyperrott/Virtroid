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
# from the NTP pool, the browser on DuckDuckGo), and the look: AOSP's
# default wallpaper, the home screen's layout and the icon shape
# (overlay/). release/ turns off the bp4a
# release's blurred, Compose notification shade and the launcher's blur,
# which a runtime draws on the CPU (README.md, look).
PRODUCT_PACKAGE_OVERLAYS += device/virtroid/overlay

$(call inherit-product, device/redroid/redroid_x86_64_only.mk)
$(call inherit-product, vendor/lineage/config/common_mini_phone.mk)

# The product's name goes into every build.prop and fingerprint, which apps
# can read: it names ReDroid's device, as the image's other properties do,
# and not Virtroid.
PRODUCT_NAME := lineage_redroid_x86_64
PRODUCT_DEVICE := redroid_x86_64_only

# adb: the node drives each runtime over adb (installs, shell, the viewer),
# so adbd runs from boot, as a debuggable build's does by default; a user
# build would leave it off. It still accepts only authorized keys
# (ro.adb.secure=0 applies to debuggable builds alone): the node puts its
# public key in the runtime's /data/misc/adb/adb_keys before each start.
PRODUCT_SYSTEM_DEFAULT_PROPERTIES += persist.sys.usb.config=adb

# No kernel audit: with SELinux off there is nothing to audit, and in a
# container the audit configuration is the host's (patches/system/logging).
PRODUCT_SYSTEM_DEFAULT_PROPERTIES += ro.logd.auditd=false

# ART's concurrent copying garbage collector, which is what a runtime gets:
# the newer one needs the kernel's userfaultfd, which the VM kernel leaves
# out, and code compiled for one can't run under the other. With no kernel
# in the build to go by, the build compiled for the newer one, so ART threw
# away every odex in the image at boot and ran SystemUI, the launcher and
# Settings interpreted.
PRODUCT_ENABLE_UFFD_GC := false

# components.md's cuts (Android.mk).
PRODUCT_PACKAGES += VirtroidRemovePackages

# Built in: F-Droid with its privileged extension (local_manifests/fdroid.xml),
# and the runtime agent (Android.bp). The keyboard is AOSP's LatinIME, which
# LineageOS's configuration already includes.
PRODUCT_PACKAGES += \
    F-Droid \
    F-DroidPrivilegedExtension \
    VirtroidRuntimeAgent

# Location: AOSP's default GNSS HAL reads each fix from the socket the node
# serves for the runtime (deploy/aosp/gnss-feed.md, patches/hardware/
# interfaces), so apps see an ordinary GPS fix.
PRODUCT_PACKAGES += com.android.hardware.gnss
PRODUCT_COPY_FILES += \
    frameworks/native/data/etc/android.hardware.location.gps.xml:$(TARGET_COPY_OUT_VENDOR)/etc/permissions/android.hardware.location.gps.xml
PRODUCT_VENDOR_PROPERTIES += \
    debug.location.fixedlocation.devname=/opt/virtroid/gnss/gnss.sock
