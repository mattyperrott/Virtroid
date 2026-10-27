# Virtroid's release keys (written by deploy/aosp/make-release-keys.sh).
PRODUCT_DEFAULT_DEV_CERTIFICATE := vendor/lineage-priv/keys/releasekey
# No OTA keys: a runtime never installs an update package.
PRODUCT_EXTRA_RECOVERY_KEYS :=
PRODUCT_CERTIFICATE_OVERRIDES += \
    AdServicesApk:virtroid.com.android.adservices.api \
    FederatedCompute:virtroid.com.android.federatedcompute \
    HealthConnectBackupRestore:virtroid.com.android.health.connect.backuprestore \
    HealthConnectController:virtroid.com.android.healthconnect.controller \
    OsuLogin:virtroid.com.android.hotspot2.osulogin \
    SafetyCenterResources:virtroid.com.android.safetycenter.resources \
    ServiceConnectivityResources:virtroid.com.android.connectivity.resources \
    ServiceUwbResources:virtroid.com.android.uwb.resources \
    ServiceWifiResources:virtroid.com.android.wifi.resources \
    WifiDialog:virtroid.com.android.wifi.dialog \
    com.android.appsearch.apk:virtroid.com.android.appsearch.apk \
    com.android.adbd:virtroid.com.android.adbd \
    com.android.adservices:virtroid.com.android.adservices \
    com.android.appsearch:virtroid.com.android.appsearch \
    com.android.art:virtroid.com.android.art \
    com.android.bt:virtroid.com.android.bt \
    com.android.configinfrastructure:virtroid.com.android.configinfrastructure \
    com.android.conscrypt:virtroid.com.android.conscrypt \
    com.android.crashrecovery:virtroid.com.android.crashrecovery \
    com.android.devicelock:virtroid.com.android.devicelock \
    com.android.extservices:virtroid.com.android.extservices \
    com.android.hardware.cas:virtroid.com.android.hardware \
    com.android.hardware.gnss:virtroid.com.android.hardware \
    com.android.healthfitness:virtroid.com.android.healthfitness \
    com.android.i18n:virtroid.com.android.i18n \
    com.android.ipsec:virtroid.com.android.ipsec \
    com.android.media:virtroid.com.android.media \
    com.android.media.swcodec:virtroid.com.android.media.swcodec \
    com.android.mediaprovider:virtroid.com.android.mediaprovider \
    com.android.neuralnetworks:virtroid.com.android.neuralnetworks \
    com.android.nfcservices:virtroid.com.android.nfcservices \
    com.android.os.statsd:virtroid.com.android.os.statsd \
    com.android.permission:virtroid.com.android.permission \
    com.android.profiling:virtroid.com.android.profiling \
    com.android.resolv:virtroid.com.android.resolv \
    com.android.rkpd:virtroid.com.android.rkpd \
    com.android.runtime:virtroid.com.android.runtime \
    com.android.scheduling:virtroid.com.android.scheduling \
    com.android.sdkext:virtroid.com.android.sdkext \
    com.android.telephonycore:virtroid.com.android.telephonycore \
    com.android.tzdata:virtroid.com.android.tzdata \
    com.android.uprobestats:virtroid.com.android.uprobestats \
    com.android.uwb:virtroid.com.android.uwb \
    com.android.virt:virtroid.com.android.virt \
    com.android.wifi:virtroid.com.android.wifi \

