LOCAL_PATH := $(call my-dir)

# What deploy/aosp/components.md cuts. A product can't drop a package an
# inherited makefile adds, so this module, never installed itself, overrides
# each one and the build leaves it out.
include $(CLEAR_VARS)
LOCAL_MODULE := VirtroidRemovePackages
LOCAL_MODULE_CLASS := APPS
LOCAL_MODULE_TAGS := optional
LOCAL_OVERRIDES_PACKAGES := \
    BasicDreams \
    BuiltInPrintService \
    CaptivePortalLogin \
    Contacts \
    DeskClock \
    EasterEgg \
    Gallery2 \
    LiveWallpapersPicker \
    ManagedProvisioning \
    messaging \
    MusicFX \
    NotoSansCJK-Regular.ttc \
    NotoSerifCJK-Regular.ttc \
    PhotoTable \
    PrintRecommendationService \
    PrintSpooler \
    Updater \
    htop \
    nano \
    rsync \
    vim
# components.md's "Test" cuts: the phone stack (no SIM, no telephony
# feature), Bluetooth (handheld_core_hardware.xml no longer declares it, so
# its service never starts), LineageOS's Settings pages, and the boot
# animation nobody sees. The Bluetooth APEX stays: it also carries the
# android.bluetooth classes apps link against.
LOCAL_OVERRIDES_PACKAGES += \
    rild \
    SimAppDialog \
    Stk \
    TeleService \
    android.hardware.bluetooth@1.1-service.sim \
    BluetoothMidiService \
    LineageParts \
    bootanimation.zip \
    bootanimation-dark.zip
LOCAL_UNINSTALLABLE_MODULE := true
LOCAL_CERTIFICATE := PRESIGNED
LOCAL_SRC_FILES := /dev/null
include $(BUILD_PREBUILT)
