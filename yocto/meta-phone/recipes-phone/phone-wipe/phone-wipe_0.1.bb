SUMMARY = "phone-wipe: a factory reset's wipe of the data area at boot"
DESCRIPTION = "data-wipe, which the initramfs (phone-ota-init) sources to make the data \
partition anew and phone-wipe.service (phone-fw) runs to empty the data directories where \
there is no such partition, keeping the device's keys and the factory's certificates \
(docs/31-lock-and-biometrics.md §11)."
LICENSE = "Apache-2.0"
LIC_FILES_CHKSUM = "file://${COMMON_LICENSE_DIR}/Apache-2.0;md5=89aea4e17d99a7cacdbeed46a0096b10"

SRC_URI = "file://data-wipe"
S = "${UNPACKDIR}"

inherit allarch

do_install() {
    install -d ${D}${nonarch_libdir}/phone
    install -m 0755 ${S}/data-wipe ${D}${nonarch_libdir}/phone/
}

FILES:${PN} = "${nonarch_libdir}/phone/data-wipe"
