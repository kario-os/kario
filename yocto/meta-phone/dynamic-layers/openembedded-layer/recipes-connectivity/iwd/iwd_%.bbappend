FILESEXTRAPATHS:prepend := "${THISDIR}/files:"

# No iwctl: it needs readline, which is GPLv3; the shell speaks D-Bus.
PACKAGECONFIG:remove = "client"

# iwd configures the addresses itself (DHCP as a station, a DHCP server as
# an access point); systemd-networkd keeps to the wired interfaces.
SRC_URI += "file://main.conf"

do_install:append() {
	install -d ${D}${sysconfdir}/iwd
	install -m 0644 ${UNPACKDIR}/main.conf ${D}${sysconfdir}/iwd/main.conf
}

FILES:${PN} += "${sysconfdir}/iwd/main.conf"

# The emulator: iwd leaves the third simulated radio to the selftest's
# hostapd (its 802.1X access point).
SRC_URI:append:qemuarm64 = " file://phone-hwsim.conf"

do_install:append:qemuarm64() {
	install -d ${D}${systemd_system_unitdir}/iwd.service.d
	install -m 0644 ${UNPACKDIR}/phone-hwsim.conf ${D}${systemd_system_unitdir}/iwd.service.d/phone-hwsim.conf
}

FILES:${PN}:append:qemuarm64 = " ${systemd_system_unitdir}/iwd.service.d"
