SUMMARY = "phone-initramfs: starts the system in the slot the boot state chooses (R37)"
LICENSE = "Apache-2.0"

PACKAGE_INSTALL = "busybox phone-ota-init"
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
