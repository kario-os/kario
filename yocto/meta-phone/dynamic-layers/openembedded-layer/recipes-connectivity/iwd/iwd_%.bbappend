FILESEXTRAPATHS:prepend := "${THISDIR}/files:"

# No iwctl: it needs readline, which is GPLv3; the shell speaks D-Bus.
PACKAGECONFIG:remove = "client"

# iwd configures the addresses itself (DHCP as a station, a DHCP server as
# an access point); systemd-networkd keeps off the Wi-Fi interfaces.
SRC_URI += "file://main.conf file://05-kario-wlan.network"

do_install:append() {
	install -d ${D}${sysconfdir}/iwd
	install -m 0644 ${UNPACKDIR}/main.conf ${D}${sysconfdir}/iwd/main.conf
	install -d ${D}${systemd_unitdir}/network
	install -m 0644 ${UNPACKDIR}/05-kario-wlan.network ${D}${systemd_unitdir}/network/05-kario-wlan.network
}

FILES:${PN} += "${sysconfdir}/iwd/main.conf ${systemd_unitdir}/network/05-kario-wlan.network"

# The emulator: iwd leaves the third simulated radio to the selftest's
# hostapd (its 802.1X access point), and systemd-networkd the radios'
# monitor.
SRC_URI:append:qemuarm64 = " file://phone-hwsim.conf file://05-kario-hwsim.network"

do_install:append:qemuarm64() {
	install -d ${D}${systemd_system_unitdir}/iwd.service.d
	install -m 0644 ${UNPACKDIR}/phone-hwsim.conf ${D}${systemd_system_unitdir}/iwd.service.d/phone-hwsim.conf
	install -d ${D}${systemd_unitdir}/network
	install -m 0644 ${UNPACKDIR}/05-kario-hwsim.network ${D}${systemd_unitdir}/network/05-kario-hwsim.network
}

FILES:${PN}:append:qemuarm64 = " ${systemd_system_unitdir}/iwd.service.d ${systemd_unitdir}/network/05-kario-hwsim.network"
