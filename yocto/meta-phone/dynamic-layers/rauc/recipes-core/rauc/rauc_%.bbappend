FILESEXTRAPATHS:prepend := "${THISDIR}/files:"

# rauc.service at a low CPU and I/O priority (files/phone-priority.conf):
# updates install in the background (the shell's automatic updates). And
# the network block device a streamed install reads its bundle through is
# not probed by udev (files/59-rauc-nbd.rules).
SRC_URI += "file://phone-priority.conf file://59-rauc-nbd.rules"

do_install:append() {
	install -d ${D}${systemd_system_unitdir}/rauc.service.d
	install -m 0644 ${UNPACKDIR}/phone-priority.conf ${D}${systemd_system_unitdir}/rauc.service.d/phone-priority.conf
	install -d ${D}${nonarch_base_libdir}/udev/rules.d
	install -m 0644 ${UNPACKDIR}/59-rauc-nbd.rules ${D}${nonarch_base_libdir}/udev/rules.d/59-rauc-nbd.rules
}

FILES:${PN}-service += "${systemd_system_unitdir}/rauc.service.d"
FILES:${PN} += "${nonarch_base_libdir}/udev/rules.d/59-rauc-nbd.rules"
