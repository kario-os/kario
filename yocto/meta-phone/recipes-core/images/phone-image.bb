SUMMARY = "Phone platform prototype image: TUI shell, system services, sample apps"
LICENSE = "Apache-2.0"

inherit core-image

IMAGE_FEATURES += "ssh-server-dropbear"

IMAGE_INSTALL = "packagegroup-core-boot ${CORE_IMAGE_EXTRA_INSTALL} ${PHONE_IMAGE_PACKAGES}"
PHONE_IMAGE_PACKAGES ?= ""

IMAGE_LINGUAS = ""
IMAGE_ROOTFS_EXTRA_SPACE = "65536"
