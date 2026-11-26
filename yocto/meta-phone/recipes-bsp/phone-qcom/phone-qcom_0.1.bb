SUMMARY = "Kario's glue for Qualcomm phones: the SIM's provisioning session, the modem's carrier configuration, the Bluetooth address, the A/B boot slot, the sound card's routing"
DESCRIPTION = "What Android's vendor stack does on a Qualcomm phone and the mainline system \
has to do itself: open the SIM's provisioning session for ModemManager (phone-qcom-uim), \
activate the configuration the modem holds for the SIM's carrier (phone-qcom-pdc), give \
WCN3990's Bluetooth an address (phone-qcom-btaddr), mark the boot slot successful so the \
bootloader keeps booting it (phone-qcom-abslot), let RAUC keep the system slots in the \
bootloader's A/B slots (phone-qcom-rauc), have phone-audiod set the sound card up \
through its UCM profile for media and for calls (phone-qcom-audio), let the modem's calls \
and messages wake a suspended phone (phone-qcom-wake), keep the modem and \
Wi-Fi off in charger mode (phone-qcom-charger). phone-qcom-uim also has rmtfs, \
tqftpserv and ModemManager restarted however often they fail. Written for Kario; Nura's \
equivalents (msm-modem-uim-selection, bootmac, qbootctl, callaudiod) are GPL-3.0."
LICENSE = "Apache-2.0"
LIC_FILES_CHKSUM = "file://${COMMON_LICENSE_DIR}/Apache-2.0;md5=89aea4e17d99a7cacdbeed46a0096b10"

SRC_URI = "file://abslot.c file://btaddr.c file://uim-select file://pdc-select file://pdc-carriers file://rauc-abl \
           file://phone-qcom-abslot.service file://phone-qcom-uim.service file://phone-qcom-pdc.service \
           file://phone-qcom-btaddr.service file://phone-audiod-ucm.conf.in file://60-phone-qcom-wake.rules.in \
           file://charger-modprobe.conf file://modem-restart.conf file://modemmanager-restart.conf"
S = "${UNPACKDIR}"

inherit systemd

# The machine's sound card as phone-audiod sets it up (phone-qcom-audio):
# the UCM card; for media the verb, the device and the PCM they route;
# during a call the verb, the earpiece and the speaker with the PCMs they
# route, and the microphone, left out while the call is muted. Without a
# call verb calls do not change the set-up; without a call speaker the
# call stays on the earpiece.
PHONE_AUDIO_UCM_CARD ??= "hw:0"
PHONE_AUDIO_UCM_VERB ??= "HiFi"
PHONE_AUDIO_UCM_DEVICE ??= "Speaker"
PHONE_AUDIO_PCM ??= "plughw:0,0"
PHONE_AUDIO_UCM_CALL_VERB ??= ""
PHONE_AUDIO_UCM_EARPIECE ??= ""
PHONE_AUDIO_EARPIECE_PCM ??= ""
PHONE_AUDIO_UCM_CALL_SPEAKER ??= ""
PHONE_AUDIO_CALL_SPEAKER_PCM ??= ""
PHONE_AUDIO_UCM_CALL_MIC ??= ""
# The modem's remoteproc, whose QRTR channel wakes the phone
# (phone-qcom-wake): the platform device's name, sdm845's MSS.
PHONE_QCOM_MODEM_RPROC ??= "4080000.remoteproc"
PACKAGE_ARCH = "${MACHINE_ARCH}"

do_compile() {
    ${CC} ${CFLAGS} ${LDFLAGS} -std=c11 -Wall -Wextra -o ${B}/phone-abslot ${S}/abslot.c
    ${CC} ${CFLAGS} ${LDFLAGS} -std=c11 -Wall -Wextra -o ${B}/phone-btaddr ${S}/btaddr.c
}

do_install() {
    install -d ${D}${bindir} ${D}${libexecdir}/phone-qcom ${D}${systemd_system_unitdir}
    install -m 0755 ${B}/phone-abslot ${B}/phone-btaddr ${D}${bindir}/
    install -m 0755 ${S}/uim-select ${S}/pdc-select ${D}${libexecdir}/phone-qcom/
    install -d ${D}${datadir}/phone-qcom
    install -m 0644 ${S}/pdc-carriers ${D}${datadir}/phone-qcom/
    install -d ${D}${nonarch_libdir}/phone
    install -m 0755 ${S}/rauc-abl ${D}${nonarch_libdir}/phone/
    install -d ${D}${nonarch_libdir}/phone/charger-modprobe.d
    install -m 0644 ${S}/charger-modprobe.conf ${D}${nonarch_libdir}/phone/charger-modprobe.d/qcom.conf
    install -m 0644 ${S}/phone-qcom-abslot.service ${S}/phone-qcom-uim.service ${S}/phone-qcom-pdc.service \
        ${S}/phone-qcom-btaddr.service ${D}${systemd_system_unitdir}/
    for unit in rmtfs tqftpserv; do
        install -d ${D}${systemd_system_unitdir}/$unit.service.d
        install -m 0644 ${S}/modem-restart.conf ${D}${systemd_system_unitdir}/$unit.service.d/10-phone-qcom.conf
    done
    install -d ${D}${systemd_system_unitdir}/ModemManager.service.d
    install -m 0644 ${S}/modemmanager-restart.conf ${D}${systemd_system_unitdir}/ModemManager.service.d/10-phone-qcom.conf
    install -d ${D}${systemd_system_unitdir}/phone-audiod.service.d
    sed -e 's|@CARD@|${PHONE_AUDIO_UCM_CARD}|' -e 's|@VERB@|${PHONE_AUDIO_UCM_VERB}|' \
        -e 's|@DEVICE@|${PHONE_AUDIO_UCM_DEVICE}|' -e 's|@PCM@|${PHONE_AUDIO_PCM}|' \
        -e 's|@CALL_VERB@|${PHONE_AUDIO_UCM_CALL_VERB}|' -e 's|@EARPIECE@|${PHONE_AUDIO_UCM_EARPIECE}|' \
        -e 's|@EARPIECE_PCM@|${PHONE_AUDIO_EARPIECE_PCM}|' -e 's|@CALL_SPEAKER@|${PHONE_AUDIO_UCM_CALL_SPEAKER}|' \
        -e 's|@CALL_SPEAKER_PCM@|${PHONE_AUDIO_CALL_SPEAKER_PCM}|' -e 's|@CALL_MIC@|${PHONE_AUDIO_UCM_CALL_MIC}|' \
        ${S}/phone-audiod-ucm.conf.in > ${D}${systemd_system_unitdir}/phone-audiod.service.d/10-phone-qcom.conf
    install -d ${D}${nonarch_base_libdir}/udev/rules.d
    sed 's|@MODEM@|${PHONE_QCOM_MODEM_RPROC}|' ${S}/60-phone-qcom-wake.rules.in > ${D}${nonarch_base_libdir}/udev/rules.d/60-phone-qcom-wake.rules
}

PACKAGES =+ "${PN}-abslot ${PN}-rauc ${PN}-uim ${PN}-pdc ${PN}-btaddr ${PN}-audio ${PN}-wake ${PN}-charger"
SYSTEMD_PACKAGES = "${PN}-abslot ${PN}-uim ${PN}-pdc ${PN}-btaddr"
SYSTEMD_SERVICE:${PN}-abslot = "phone-qcom-abslot.service"
SYSTEMD_SERVICE:${PN}-uim = "phone-qcom-uim.service"
SYSTEMD_SERVICE:${PN}-pdc = "phone-qcom-pdc.service"
SYSTEMD_SERVICE:${PN}-btaddr = "phone-qcom-btaddr.service"
FILES:${PN}-abslot = "${bindir}/phone-abslot ${systemd_system_unitdir}/phone-qcom-abslot.service"
FILES:${PN}-rauc = "${nonarch_libdir}/phone/rauc-abl"
RDEPENDS:${PN}-rauc = "${PN}-abslot"
FILES:${PN}-uim = "${libexecdir}/phone-qcom/uim-select ${systemd_system_unitdir}/phone-qcom-uim.service \
                   ${systemd_system_unitdir}/rmtfs.service.d ${systemd_system_unitdir}/tqftpserv.service.d \
                   ${systemd_system_unitdir}/ModemManager.service.d"
FILES:${PN}-pdc = "${libexecdir}/phone-qcom/pdc-select ${datadir}/phone-qcom ${systemd_system_unitdir}/phone-qcom-pdc.service"
RDEPENDS:${PN}-pdc = "${PN}-uim libqmi"
FILES:${PN}-btaddr = "${bindir}/phone-btaddr ${systemd_system_unitdir}/phone-qcom-btaddr.service"
FILES:${PN}-audio = "${systemd_system_unitdir}/phone-audiod.service.d"
FILES:${PN}-charger = "${nonarch_libdir}/phone/charger-modprobe.d"
RDEPENDS:${PN}-uim = "libqmi rmtfs tqftpserv modemmanager"
RDEPENDS:${PN}-audio = "alsa-utils-alsaucm"
FILES:${PN}-wake = "${nonarch_base_libdir}/udev/rules.d/60-phone-qcom-wake.rules"
ALLOW_EMPTY:${PN} = "1"
