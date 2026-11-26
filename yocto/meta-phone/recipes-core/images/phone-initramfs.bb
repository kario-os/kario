SUMMARY = "phone-initramfs: starts the system in the slot the boot state chooses (R37), or on the userdata layout's root"
LICENSE = "Apache-2.0"

# cfg80211 is built in (phone-radio.cfg) and reads the Wi-Fi regulatory
# database once, while the kernel starts, from the initramfs; without it
# every boot kept the world domain's rules ("failed to load regulatory.db").
PACKAGE_INSTALL = "busybox phone-ota-init wireless-regdb-static ${PHONE_INITRAMFS_EXTRA}"
# Where the bootloader keeps the A/B slot: the board's partitions by role
# (phone-sku-partitions: the root slots, or the data partition that is all
# of the userdata layout's root) and what reads the slot
# (43-phone-partitions.md). Without them the initramfs takes the names of
# the emulator's disk.
PHONE_SLOT_KEEPER ??= "state"
PHONE_INITRAMFS_EXTRA ??= "${@'phone-sku-partitions phone-qcom-abslot' if d.getVar('PHONE_SLOT_KEEPER') == 'abl' else ''}"
IMAGE_FEATURES = ""
IMAGE_LINGUAS = ""
IMAGE_FSTYPES = "${INITRAMFS_FSTYPES}"
IMAGE_NAME_SUFFIX ?= ""
IMAGE_ROOTFS_SIZE = "8192"
IMAGE_ROOTFS_EXTRA_SPACE = "0"
USE_DEVFS = "0"
IMAGE_DEVICE_TABLES = "files/phone-initramfs-devices.txt"
do_rootfs[file-checksums] += "${THISDIR}/../../files/phone-initramfs-devices.txt:True"

inherit image
