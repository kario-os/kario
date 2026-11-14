SUMMARY = "phone-bundle-delta: an incremental system update of phone-image from one earlier version"
DESCRIPTION = "The payload phone-ota-delta made from an earlier slot image to this build's, and \
phone-ota-delta built for the machine to apply it (both in PHONE_DELTA_DIR; the applier is left \
out for an earlier version whose own applier reads the payload), with this build's \
signed verity descriptor, as a verity-format RAUC bundle signed like phone-bundle. Its \
slot-install hook rebuilds the inactive slot from the running one; the slot boots only if the \
rebuilt slot's root hash is the signed one (25-system-update.md)."
LICENSE = "Apache-2.0"
LIC_FILES_CHKSUM = "file://${COMMON_LICENSE_DIR}/Apache-2.0;md5=89aea4e17d99a7cacdbeed46a0096b10"

inherit bundle

# As phone-bundle: the same keys, machine and version.
PHONE_OTA_KEYS ??= "${TOPDIR}/../ota-keys"
PHONE_UPDATE_KEY ??= "dev"
RAUC_BUNDLE_COMPATIBLE = "kario-${MACHINE}"
PHONE_VERSION ??= ""
RAUC_BUNDLE_VERSION = "${@d.getVar('PHONE_VERSION') or d.getVar('DISTRO_VERSION')}"
RAUC_BUNDLE_DESCRIPTION = "Kario system update (incremental)"
RAUC_BUNDLE_FORMAT = "verity"
RAUC_KEY_FILE = "${PHONE_OTA_KEYS}/${PHONE_UPDATE_KEY}.key.pem"
RAUC_CERT_FILE = "${PHONE_OTA_KEYS}/${PHONE_UPDATE_KEY}.cert.pem"

# rootfs.kdelta and phone-ota-delta come from outside bitbake
# (yocto/qemu-ota-delta.sh, which make yocto-bundle OTA_DELTA_FROM= and
# make yocto-ota-delta run): the payload against this build's slot image,
# and the applier unless the earlier version's own reads the payload.
PHONE_DELTA_DIR ??= "${TOPDIR}/ota-delta"
FILESEXTRAPATHS:prepend := "${PHONE_DELTA_DIR}:"
SRC_URI = "file://delta-hook file://rootfs.kdelta"

# Only when a payload is there: without one every other build would stop
# at parsing, unable to checksum it. Parsed afresh each time, as the
# payload appears between two bitbake runs (qemu-ota-delta.sh).
BB_DONT_CACHE = "1"
python () {
    import os
    if not os.path.exists(os.path.join(d.getVar('PHONE_DELTA_DIR'), 'rootfs.kdelta')):
        raise bb.parse.SkipRecipe("no delta payload in %s (make yocto-bundle OTA_DELTA_FROM=...)" % d.getVar('PHONE_DELTA_DIR'))
    if os.path.exists(os.path.join(d.getVar('PHONE_DELTA_DIR'), 'phone-ota-delta')):
        d.appendVar('SRC_URI', ' file://phone-ota-delta')
        d.appendVar('RAUC_BUNDLE_EXTRA_FILES', ' phone-ota-delta')
}

RAUC_BUNDLE_SLOTS = "rootfs"
RAUC_SLOT_rootfs = "phone-image"
RAUC_SLOT_rootfs[type] = "file"
RAUC_SLOT_rootfs[file] = "rootfs.kdelta"
RAUC_SLOT_rootfs[rename] = "rootfs.kdelta"
RAUC_SLOT_rootfs[hooks] = "install"
RAUC_SLOT_rootfs[depends] = "phone-image:do_image_complete"
RAUC_BUNDLE_HOOKS[file] = "delta-hook"
PHONE_SLOT_IMAGE = "phone-image${IMAGE_MACHINE_SUFFIX}${IMAGE_NAME_SUFFIX}.ext4.verity"
RAUC_BUNDLE_EXTRA_FILES = "${PHONE_SLOT_IMAGE}.desc ${PHONE_SLOT_IMAGE}.desc.sig"

# The payload must rebuild this build's slot: the root hash its manifest
# ends at is the descriptor's. An applier in the bundle runs from it.
python do_configure:append() {
    import json, os, struct
    bundle = d.getVar('BUNDLE_DIR')
    if os.path.exists(os.path.join(bundle, 'phone-ota-delta')):
        os.chmod(os.path.join(bundle, 'phone-ota-delta'), 0o755)
    manifest = {}
    with open(os.path.join(bundle, 'rootfs.kdelta'), 'rb') as f:
        magic, _, n = struct.unpack('>4sIQ', f.read(16))
        if magic == b'KDLT' and n < 64 << 20:
            manifest = json.loads(f.read(n))
    desc = {}
    with open(os.path.join(bundle, d.getVar('PHONE_SLOT_IMAGE') + '.desc')) as f:
        for line in f:
            key, _, value = line.strip().partition('=')
            desc[key] = value
    if manifest.get('verity', {}).get('root_hash') != desc.get('root_hash'):
        bb.fatal("%s/rootfs.kdelta was not made for this build's slot image: generate it again" % d.getVar('PHONE_DELTA_DIR'))
}
