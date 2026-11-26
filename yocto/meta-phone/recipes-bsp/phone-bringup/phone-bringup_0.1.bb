SUMMARY = "Bring-up aids for a phone without a serial port: USB console and SSH, kept kernel logs, no suspend while a computer is on the USB link"
DESCRIPTION = "A USB gadget with a serial console (ttyGS0, logged in as root) and a network \
link (dbg0, 172.16.42.1, DHCP for the computer, SSH through the image's dropbear), a wake \
lock that keeps the phone out of suspend while a computer has that gadget configured (a \
charger alone, or no cable, lets it sleep), and bringup-log, which prints what pstore kept \
of the previous boot. In the image while PHONE_BRINGUP is 1 (the machine's)."
LICENSE = "Apache-2.0"
LIC_FILES_CHKSUM = "file://${COMMON_LICENSE_DIR}/Apache-2.0;md5=89aea4e17d99a7cacdbeed46a0096b10"

SRC_URI = "file://usb-console file://bringup-awake file://bringup-log file://phone-bringup-usb.service file://phone-bringup-awake.service \
           file://10-phone-bringup.network file://autologin.conf"
S = "${UNPACKDIR}"

inherit systemd allarch

do_install() {
    install -d ${D}${libexecdir}/phone-bringup ${D}${bindir} ${D}${systemd_system_unitdir} \
        ${D}${sysconfdir}/systemd/network ${D}${sysconfdir}/systemd/system/serial-getty@ttyGS0.service.d
    install -m 0755 ${S}/usb-console ${D}${libexecdir}/phone-bringup/
    install -m 0755 ${S}/bringup-awake ${D}${libexecdir}/phone-bringup/awake
    install -m 0755 ${S}/bringup-log ${D}${bindir}/
    install -m 0644 ${S}/phone-bringup-usb.service ${S}/phone-bringup-awake.service ${D}${systemd_system_unitdir}/
    install -m 0644 ${S}/10-phone-bringup.network ${D}${sysconfdir}/systemd/network/
    sed 's,@SBINDIR@,${base_sbindir},' ${S}/autologin.conf > ${D}${sysconfdir}/systemd/system/serial-getty@ttyGS0.service.d/10-phone-bringup.conf
}

SYSTEMD_SERVICE:${PN} = "phone-bringup-usb.service phone-bringup-awake.service"
FILES:${PN} += "${systemd_system_unitdir} ${sysconfdir}/systemd"
RDEPENDS:${PN} = "util-linux-agetty"
