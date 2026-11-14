SUMMARY = "phone-bundle: a system update of phone-image (a signed RAUC bundle)"
DESCRIPTION = "The system slot image (ext4 plus its dm-verity hash tree, phone-verity.bbclass) \
as a verity-format RAUC bundle, signed with the update key (yocto/ota-keys.sh; products sign \
in an HSM). The machine's phones accept it only if their keyring trusts the key and its \
compatible matches; a hook puts the slot's signed verity descriptor into the boot state (R37). \
Adaptive: an install downloads only the blocks neither slot holds (R40)."
LICENSE = "Apache-2.0"
LIC_FILES_CHKSUM = "file://${COMMON_LICENSE_DIR}/Apache-2.0;md5=89aea4e17d99a7cacdbeed46a0096b10"

inherit bundle

PHONE_OTA_KEYS ??= "${TOPDIR}/../ota-keys"
PHONE_UPDATE_KEY ??= "dev"

RAUC_BUNDLE_COMPATIBLE = "kario-${MACHINE}"
PHONE_VERSION ??= ""
RAUC_BUNDLE_VERSION = "${@d.getVar('PHONE_VERSION') or d.getVar('DISTRO_VERSION')}"
RAUC_BUNDLE_DESCRIPTION = "Kario system update"
RAUC_BUNDLE_FORMAT = "verity"
RAUC_BUNDLE_SLOTS = "rootfs"
RAUC_SLOT_rootfs = "phone-image"
RAUC_SLOT_rootfs[fstype] = "ext4.verity"
RAUC_SLOT_rootfs[rename] = "rootfs.img"
RAUC_SLOT_rootfs[hooks] = "post-install"
# Adaptive updates: the bundle carries a hash per 4 KiB block of the slot
# image; streaming an install, RAUC takes the blocks either slot already
# holds from the disk and downloads only the rest (R40).
RAUC_SLOT_rootfs[adaptive] = "block-hash-index"
SRC_URI = "file://verity-hook"
RAUC_BUNDLE_HOOKS[file] = "verity-hook"
PHONE_SLOT_IMAGE = "phone-image${IMAGE_MACHINE_SUFFIX}${IMAGE_NAME_SUFFIX}.ext4.verity"
RAUC_BUNDLE_EXTRA_FILES = "${PHONE_SLOT_IMAGE}.desc ${PHONE_SLOT_IMAGE}.desc.sig"
RAUC_KEY_FILE = "${PHONE_OTA_KEYS}/${PHONE_UPDATE_KEY}.key.pem"
RAUC_CERT_FILE = "${PHONE_OTA_KEYS}/${PHONE_UPDATE_KEY}.cert.pem"
