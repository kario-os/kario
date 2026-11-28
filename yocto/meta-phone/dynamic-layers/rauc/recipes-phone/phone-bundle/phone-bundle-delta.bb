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
SRC_URI = "file://delta-hook file://rootfs.kdelta file://phone-verity-pad.py"

# Only when a payload is there: without one every other build would stop
# at parsing, unable to checksum it. Parsed afresh each time, as the
# payload appears between two bitbake runs (qemu-ota-delta.sh).
BB_DONT_CACHE = "1"
python () {
    import os
    if not os.path.exists(os.path.join(d.getVar('PHONE_DELTA_DIR'), 'rootfs.kdelta')):
        raise bb.parse.SkipRecipe("no delta payload in %s (make yocto-bundle OTA_DELTA_FROM=...)" % d.getVar('PHONE_DELTA_DIR'))
    # The update test's payload to another signed slot image than this
    # build's (qemu-ota-delta.sh's power cut lane) brings that image's
    # descriptor.
    if os.path.exists(os.path.join(d.getVar('PHONE_DELTA_DIR'), 'target.verity.desc')):
        d.appendVar('SRC_URI', ' file://target.verity.desc file://target.verity.desc.sig')
        d.setVar('PHONE_TARGET_DESC', 'target.verity.desc')
    if os.path.exists(os.path.join(d.getVar('PHONE_DELTA_DIR'), 'phone-ota-delta')):
        d.appendVar('SRC_URI', ' file://phone-ota-delta')
        d.appendVar('RAUC_BUNDLE_EXTRA_FILES', ' phone-ota-delta')
}

RAUC_BUNDLE_SLOTS = "rootfs ${PHONE_BUNDLE_BOOT}"
# As phone-bundle: the boot slot, whole, where the bootloader keeps the slot.
PHONE_SLOT_KEEPER ??= "state"
PHONE_BUNDLE_BOOT = "${@'boot' if d.getVar('PHONE_SLOT_KEEPER') == 'abl' else ''}"
RAUC_SLOT_boot = "phone-image"
RAUC_SLOT_boot[type] = "file"
RAUC_SLOT_boot[file] = "phone-image${IMAGE_MACHINE_SUFFIX}${IMAGE_NAME_SUFFIX}.abboot"
RAUC_SLOT_boot[rename] = "boot.img"
RAUC_SLOT_boot[depends] = "phone-image:do_image_complete"
RAUC_SLOT_rootfs = "phone-image"
RAUC_SLOT_rootfs[type] = "file"
RAUC_SLOT_rootfs[file] = "rootfs.kdelta"
RAUC_SLOT_rootfs[rename] = "rootfs.kdelta"
RAUC_SLOT_rootfs[hooks] = "install"
RAUC_SLOT_rootfs[depends] = "phone-image:do_image_complete"
RAUC_BUNDLE_HOOKS[file] = "delta-hook"
PHONE_SLOT_IMAGE = "phone-image${IMAGE_MACHINE_SUFFIX}${IMAGE_NAME_SUFFIX}.ext4.verity"
PHONE_TARGET_DESC ?= "${PHONE_SLOT_IMAGE}.desc"
RAUC_BUNDLE_EXTRA_FILES = "${PHONE_TARGET_DESC} ${PHONE_TARGET_DESC}.sig"

# The payload must rebuild the slot the bundle's descriptor describes (this
# build's): the root hash its manifest ends at is the descriptor's. An
# applier in the bundle runs from it.
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
    with open(os.path.join(bundle, d.getVar('PHONE_TARGET_DESC'))) as f:
        for line in f:
            key, _, value = line.strip().partition('=')
            desc[key] = value
    if manifest.get('verity', {}).get('root_hash') != desc.get('root_hash'):
        bb.fatal("%s/rootfs.kdelta was not made for the slot image of %s: generate it again" % (d.getVar('PHONE_DELTA_DIR'), d.getVar('PHONE_TARGET_DESC')))
}

# dm-verity reads the hash tree's leaves ahead in clusters of
# dm_verity.prefetch_cluster (256 KiB), aligned on the device, which reach
# back from the tree into the squashfs image unless the image ends where
# they do not: a streamed install downloaded what they took again, the
# payload's last blocks (25-system-update.md §5.5). Whatever the payload's
# size, phone-verity-pad.py measures the bundle do_bundle made and, where
# they reach into it, pads the image to end where they do not (a file after
# the payload by name, which nothing reads) and bundles it again the same way.
do_bundle:append() {
	python3 ${UNPACKDIR}/phone-verity-pad.py ${B}/bundle.raucb ${BUNDLE_DIR}/zz-verity-pad -- \
		${STAGING_BINDIR_NATIVE}/rauc bundle --debug --cert="${RAUC_CERT_FILE}" --key="${RAUC_KEY_FILE}" \
		${BUNDLE_ARGS} ${BUNDLE_DIR} ${B}/bundle.raucb
}
