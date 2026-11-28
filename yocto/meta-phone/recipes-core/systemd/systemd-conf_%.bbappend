FILESEXTRAPATHS:prepend := "${THISDIR}/files:"

# logind leaves the keys and the lid switch to the Shell (phone-logind.conf);
# after oe-core's own drop-in (00-systemd-conf.conf), so it wins.
SRC_URI += "file://phone-logind.conf"

do_install:append() {
	install -D -m0644 ${UNPACKDIR}/phone-logind.conf ${D}${systemd_unitdir}/logind.conf.d/50-phone.conf
}
