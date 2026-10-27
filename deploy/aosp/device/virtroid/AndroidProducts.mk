#
# Virtroid's runtime image: LineageOS on ReDroid's container device.
#

PRODUCT_MAKEFILES := \
    $(LOCAL_DIR)/lineage_redroid_x86_64.mk

COMMON_LUNCH_CHOICES := \
    lineage_redroid_x86_64-bp4a-user \
    lineage_redroid_x86_64-bp4a-userdebug
