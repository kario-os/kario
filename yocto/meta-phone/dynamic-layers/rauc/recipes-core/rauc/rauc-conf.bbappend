# The keyring (ca.cert.pem) comes from the update keys, outside git.
FILESEXTRAPATHS:prepend := "${THISDIR}/files:${PHONE_OTA_KEYS}:"

# A release's version (kario-internal's make release): its image refuses
# bundles older than itself, whatever an update server offers. So a release
# that has to undo a bad one goes out under a higher version number.
PHONE_VERSION ??= ""

# Who keeps the A/B slot and the slots' partitions, from the board file
# (tools/sku.py writes them into the SKU's kas overlay): phone-initramfs in
# the state partition (system.conf, the emulator's disk), or the bootloader
# (system-abl.conf.in, 43-phone-partitions.md).
PHONE_SLOT_KEEPER ??= "state"
PHONE_ROOT_SLOTS ??= "rootfs_a rootfs_b"
PHONE_BOOT_SLOTS ??= ""
SRC_URI += "file://system-abl.conf.in"

# Bundles are compatible per machine: an update for one board never installs
# on another.
do_install:append() {
    if [ "${PHONE_SLOT_KEEPER}" = abl ]; then
        set -- ${PHONE_ROOT_SLOTS} ${PHONE_BOOT_SLOTS}
        if [ $# != 4 ]; then
            bbfatal "the bootloader keeps the slot: PHONE_ROOT_SLOTS and PHONE_BOOT_SLOTS name two partitions each (tools/sku.py gen)"
        fi
        sed -e "s/@ROOT_A@/$1/" -e "s/@ROOT_B@/$2/" -e "s/@BOOT_A@/$3/" -e "s/@BOOT_B@/$4/" \
            ${UNPACKDIR}/system-abl.conf.in > ${D}${nonarch_libdir}/rauc/system.conf
    fi
    sed -i 's/@COMPATIBLE@/kario-${MACHINE}/' ${D}${nonarch_libdir}/rauc/system.conf
    if [ -n "${PHONE_VERSION}" ]; then
        sed -i '/^compatible=/a min-bundle-version=${PHONE_VERSION}' ${D}${nonarch_libdir}/rauc/system.conf
    fi
}
do_install[vardeps] += "PHONE_SLOT_KEEPER PHONE_ROOT_SLOTS PHONE_BOOT_SLOTS"
PACKAGE_ARCH = "${MACHINE_ARCH}"
