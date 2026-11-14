SUMMARY = "phone-ota: A/B system slots for system updates"
DESCRIPTION = "What phone-initramfs does at boot (phone-ota-init): choose the slot, check its \
signed verity descriptor, open it through dm-verity, mount the state and data partitions. \
Also RAUC's boot loader backend over the same state, the health check that marks a slot \
good, and the QEMU update test's steps (R37)."
LICENSE = "Apache-2.0"
LIC_FILES_CHKSUM = "file://${COMMON_LICENSE_DIR}/Apache-2.0;md5=89aea4e17d99a7cacdbeed46a0096b10"

# The update CA (yocto/ota-keys.sh, outside git) is what the initramfs checks
# slot verity descriptors against, as RAUC checks bundles.
PHONE_OTA_KEYS ??= "${TOPDIR}/../ota-keys"
FILESEXTRAPATHS:prepend := "${PHONE_OTA_KEYS}:"
SRC_URI = "file://slot-init file://slot-choose file://rauc-boot file://ota-healthy file://ota-test file://ota.script file://ota-held.script \
           file://cloud-test file://cloud.script \
           file://phone-ota-good.service file://phone-ota-test.service file://phone-cloud-test.service file://ca.cert.pem"
S = "${UNPACKDIR}"

inherit systemd allarch

SYSTEMD_SERVICE:${PN} = "phone-ota-good.service phone-ota-test.service phone-cloud-test.service"

do_install() {
    install -d ${D}${nonarch_libdir}/phone ${D}${systemd_system_unitdir}
    install -m 0755 ${S}/rauc-boot ${S}/ota-healthy ${S}/ota-test ${S}/cloud-test ${D}${nonarch_libdir}/phone/
    install -m 0644 ${S}/slot-choose ${D}${nonarch_libdir}/phone/
    install -d ${D}${datadir}/phone
    install -m 0644 ${S}/ota.script ${S}/ota-held.script ${S}/cloud.script ${D}${datadir}/phone/
    install -m 0644 ${S}/phone-ota-good.service ${S}/phone-ota-test.service ${S}/phone-cloud-test.service ${D}${systemd_system_unitdir}/
    install -d ${D}${localstatedir}/lib/slots ${D}${localstatedir}/lib/phone
    install -m 0755 ${S}/slot-init ${D}/init
    install -d ${D}${sysconfdir}/phone
    install -m 0644 ${S}/ca.cert.pem ${D}${sysconfdir}/phone/update-ca.pem
}

# The slot choice is shared by the initramfs (phone-ota-init) and the running
# system (RAUC's backend, the test); one package, as /lib is /usr/lib here.
PACKAGES =+ "${PN}-init ${PN}-choose"
FILES:${PN}-init = "/init ${sysconfdir}/phone/update-ca.pem"
FILES:${PN}-choose = "${nonarch_libdir}/phone/slot-choose"
RDEPENDS:${PN}-init = "${PN}-choose cryptsetup openssl-bin"
FILES:${PN} = "${nonarch_libdir}/phone ${systemd_system_unitdir} ${datadir}/phone/ota.script ${datadir}/phone/ota-held.script ${datadir}/phone/cloud.script \
               ${localstatedir}/lib/slots ${localstatedir}/lib/phone"
RDEPENDS:${PN} = "rauc ${PN}-choose"
