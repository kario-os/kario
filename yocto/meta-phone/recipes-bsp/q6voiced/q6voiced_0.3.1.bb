SUMMARY = "q6voiced: the QDSP6 voice path open while ModemManager has a call"
DESCRIPTION = "Nura's daemon for Qualcomm phones: during a call it opens the sound card's \
q6voice PCM (Q6VOICED_PCM, the machine's), which routes the call's audio in the DSP."
HOMEPAGE = "https://gitlab.nura.eco/nura/q6voiced"
LICENSE = "MIT"
LIC_FILES_CHKSUM = "file://LICENSE;md5=ac46aeca56dd41012d179d9ce50be8e2"

SRC_URI = "git://gitlab.nura.eco/nura/q6voiced.git;protocol=https;branch=master;tag=${PV}"
SRCREV = "21061751b80850e37de371d61a039cc2297999e0"

DEPENDS = "alsa-lib dbus"

inherit meson pkgconfig systemd

EXTRA_OEMESON = "-Dopenrc=disabled -Dsystemd=enabled"

# hw:<card>,<device> of the q6voice PCM; the service starts only with it.
Q6VOICED_PCM ??= ""
PACKAGE_ARCH = "${MACHINE_ARCH}"

do_install:append() {
    pcm="${Q6VOICED_PCM}"
    if [ -n "$pcm" ]; then
        card=${pcm#hw:}
        install -d ${D}${datadir}/q6voiced
        printf 'q6voice_card=%s\nq6voice_device=%s\n' "${card%,*}" "${card#*,}" > ${D}${datadir}/q6voiced/q6voiced.conf
    fi
}

SYSTEMD_SERVICE:${PN} = "q6voiced.service"
FILES:${PN} += "${systemd_system_unitdir} ${datadir}/q6voiced"
