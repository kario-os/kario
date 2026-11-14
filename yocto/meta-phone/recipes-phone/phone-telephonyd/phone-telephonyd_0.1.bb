SUMMARY = "phone-telephonyd: calls, SMS and network status for the shell (C++23)"
DESCRIPTION = "Telephony daemon behind the shell's tel.* and sms.* capabilities. \
Two backends: the simulated baseband and ModemManager (D-Bus, through sd-bus); \
the SKU's sku.json (phone-sku) picks one."
LICENSE = "Apache-2.0"
LIC_FILES_CHKSUM = "file://${PHONE_KARIO_LICENSE};md5=89aea4e17d99a7cacdbeed46a0096b10"

# telephonyd/: the tarball `make yocto-src` packs (with the LICENSE), or the
# commit (phone-src.bbclass).
SRC_URI = "${@phone_src(d, 'file://phone-telephonyd-src.tar.gz')} \
           file://phone-telephonyd.service \
          "
S = "${@phone_dir(d, 'telephonyd', 'telephonyd')}"

DEPENDS = "systemd"

inherit cmake pkgconfig systemd phone-src

EXTRA_OECMAKE = "-DTEL_MODEMMANAGER=ON"

do_install:append() {
    install -d ${D}${systemd_system_unitdir}
    install -m 0644 ${UNPACKDIR}/phone-telephonyd.service ${D}${systemd_system_unitdir}/
}

# The simulated network's operator, messages and USSD menus (data, not code).
FILES:${PN} += "${datadir}/phone/sim-network.json"

SYSTEMD_SERVICE:${PN} = "phone-telephonyd.service"
