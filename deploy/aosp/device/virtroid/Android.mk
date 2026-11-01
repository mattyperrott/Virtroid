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
# The CTS shims, prebuilts that only Android's compatibility tests use. They
# come signed with Google's test keys, which our release keys can't replace
# in a prebuilt.
LOCAL_OVERRIDES_PACKAGES += \
    com.android.apex.cts.shim.v1_prebuilt \
    CtsShimPrebuilt \
    CtsShimPrivPrebuilt
LOCAL_UNINSTALLABLE_MODULE := true
LOCAL_CERTIFICATE := PRESIGNED
LOCAL_SRC_FILES := /dev/null
include $(BUILD_PREBUILT)

# LibreOffice Viewer (MPL-2.0), F-Droid's x86_64 build, as F-Droid signed it.
# It keeps its native code compressed for Android to extract, which Android
# never does for a built-in app, so build-lineage.sh extracts the x86_64
# libraries into prebuilt/LibreOfficeViewer-lib and they're installed as the
# app's lib/x86_64. The APK goes in untouched (LOCAL_REPLACE_PREBUILT_APK_
# INSTALLED skips the uncompressing and aligning a PRESIGNED app otherwise
# gets, which would break its signature).
include $(CLEAR_VARS)
LOCAL_MODULE := LibreOfficeViewer
LOCAL_MODULE_CLASS := APPS
LOCAL_MODULE_TAGS := optional
LOCAL_PRODUCT_MODULE := true
LOCAL_CERTIFICATE := PRESIGNED
LOCAL_SRC_FILES := prebuilt/LibreOfficeViewer.apk
LOCAL_REPLACE_PREBUILT_APK_INSTALLED := $(LOCAL_PATH)/prebuilt/LibreOfficeViewer.apk
LOCAL_DEX_PREOPT := false
LOCAL_MODULE_TARGET_ARCH := x86_64
LOCAL_PREBUILT_JNI_LIBS := $(patsubst $(LOCAL_PATH)/%,%,$(wildcard $(LOCAL_PATH)/prebuilt/LibreOfficeViewer-lib/*.so))
include $(BUILD_PREBUILT)
