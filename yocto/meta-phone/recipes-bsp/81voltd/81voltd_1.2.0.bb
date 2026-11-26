SUMMARY = "81voltd: the IMS data service a Qualcomm modem asks the system for VoLTE"
DESCRIPTION = "Nura's daemon for Qualcomm phones whose modem runs the IMS stack (SIP, RTP, \
the voice codecs): it serves the IMS data service (QMI service 770) on QRTR and opens the \
bearer the modem asks for (APN, IP version, 3GPP profile) through ModemManager. In the image \
of a SKU whose board has ims = \"81voltd\" (the machine's PHONE_IMS)."
HOMEPAGE = "https://gitlab.nura.eco/modem/81voltd"
LICENSE = "GPL-2.0-or-later"
LIC_FILES_CHKSUM = "file://LICENSE;md5=b234ee4d69f5fce4486a80fdaf4a4263 \
                    file://main.c;beginline=1;endline=1;md5=7226e442a172bcf25807246d7ef1eba1"

SRC_URI = "git://gitlab.nura.eco/modem/81voltd.git;protocol=https;branch=main;tag=v${PV} \
           file://81voltd.service"
SRCREV = "7c0cd9442d71b55260c9917c89ebc4bd20e283a8"

# qmi_imsd.c and .h come pre-generated: without qmic-native meson copies them.
DEPENDS = "glib-2.0 modemmanager libqmi qrtr"

inherit meson pkgconfig systemd

# Upstream's unit is ordered after ModemManager.target, which no unit
# provides, and its OpenRC script is of no use here: Kario's unit instead.
do_install:append() {
    rm -r ${D}${sysconfdir}/init.d
    rm ${D}${prefix}/lib/systemd/system/81voltd.service
    install -d ${D}${systemd_system_unitdir}
    install -m 0644 ${UNPACKDIR}/81voltd.service ${D}${systemd_system_unitdir}/
}

SYSTEMD_SERVICE:${PN} = "81voltd.service"
FILES:${PN} += "${systemd_system_unitdir}"
RDEPENDS:${PN} = "modemmanager"
