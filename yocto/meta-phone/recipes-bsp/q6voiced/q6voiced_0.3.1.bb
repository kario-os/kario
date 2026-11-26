SUMMARY = "q6voiced: the QDSP6 voice path open while ModemManager has a call"
DESCRIPTION = "Nura's daemon for Qualcomm phones: during a call it opens the sound card's \
q6voice PCM (Q6VOICED_PCM, the machine's), which routes the call's audio in the DSP."
HOMEPAGE = "https://gitlab.nura.eco/nura/q6voiced"
LICENSE = "MIT"
LIC_FILES_CHKSUM = "file://LICENSE;md5=ac46aeca56dd41012d179d9ce50be8e2"

SRC_URI = "git://gitlab.nura.eco/nura/q6voiced.git;protocol=https;branch=master;tag=${PV} \
           file://q6voiced-wait file://10-kario.conf"
SRCREV = "21061751b80850e37de371d61a039cc2297999e0"

DEPENDS = "alsa-lib dbus"

inherit meson pkgconfig systemd useradd

EXTRA_OEMESON = "-Dopenrc=disabled -Dsystemd=enabled"

# hw:<card>,<device> of the q6voice PCM; the service starts only with it.
Q6VOICED_PCM ??= ""
PACKAGE_ARCH = "${MACHINE_ARCH}"

# The user the service runs as instead of upstream's DynamicUser
# (10-kario.conf): the system bus is dbus-daemon, which knows it from
# /etc/passwd.
USERADD_PACKAGES = "${PN}"
USERADD_PARAM:${PN} = "--system --no-create-home --home-dir / --shell /bin/false --user-group q6voiced"

do_install:append() {
    pcm="${Q6VOICED_PCM}"
    if [ -n "$pcm" ]; then
        card=${pcm#hw:}
        install -d ${D}${datadir}/q6voiced
        printf 'q6voice_card=%s\nq6voice_device=%s\n' "${card%,*}" "${card#*,}" > ${D}${datadir}/q6voiced/q6voiced.conf
    fi
    install -d ${D}${libexecdir}/q6voiced ${D}${systemd_system_unitdir}/q6voiced.service.d
    install -m 0755 ${UNPACKDIR}/q6voiced-wait ${D}${libexecdir}/q6voiced/
    install -m 0644 ${UNPACKDIR}/10-kario.conf ${D}${systemd_system_unitdir}/q6voiced.service.d/
}

SYSTEMD_SERVICE:${PN} = "q6voiced.service"
FILES:${PN} += "${systemd_system_unitdir} ${datadir}/q6voiced ${libexecdir}/q6voiced"
