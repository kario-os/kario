SUMMARY = "Kario's glue for Qualcomm phones: the SIM's provisioning session, the Bluetooth address, the A/B boot slot, the sound card's routing"
DESCRIPTION = "What Android's vendor stack does on a Qualcomm phone and the mainline system \
has to do itself: open the SIM's provisioning session for ModemManager (phone-qcom-uim), give \
WCN3990's Bluetooth an address (phone-qcom-btaddr), mark the boot slot successful so the \
bootloader keeps booting it (phone-qcom-abslot), have phone-audiod set the sound card up \
through its UCM profile for media and for calls (phone-qcom-audio). Written for Kario; Nura's \
equivalents (msm-modem-uim-selection, bootmac, qbootctl, callaudiod) are GPL-3.0."
LICENSE = "Apache-2.0"
LIC_FILES_CHKSUM = "file://${COMMON_LICENSE_DIR}/Apache-2.0;md5=89aea4e17d99a7cacdbeed46a0096b10"

SRC_URI = "file://abslot.c file://btaddr.c file://uim-select \
           file://phone-qcom-abslot.service file://phone-qcom-uim.service file://phone-qcom-btaddr.service \
           file://phone-audiod-ucm.conf.in"
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
PACKAGE_ARCH = "${MACHINE_ARCH}"

do_compile() {
    ${CC} ${CFLAGS} ${LDFLAGS} -std=c11 -Wall -Wextra -o ${B}/phone-abslot ${S}/abslot.c
    ${CC} ${CFLAGS} ${LDFLAGS} -std=c11 -Wall -Wextra -o ${B}/phone-btaddr ${S}/btaddr.c
}

do_install() {
    install -d ${D}${bindir} ${D}${libexecdir}/phone-qcom ${D}${systemd_system_unitdir}
    install -m 0755 ${B}/phone-abslot ${B}/phone-btaddr ${D}${bindir}/
    install -m 0755 ${S}/uim-select ${D}${libexecdir}/phone-qcom/
    install -m 0644 ${S}/phone-qcom-abslot.service ${S}/phone-qcom-uim.service ${S}/phone-qcom-btaddr.service ${D}${systemd_system_unitdir}/
    install -d ${D}${systemd_system_unitdir}/phone-audiod.service.d
    sed -e 's|@CARD@|${PHONE_AUDIO_UCM_CARD}|' -e 's|@VERB@|${PHONE_AUDIO_UCM_VERB}|' \
        -e 's|@DEVICE@|${PHONE_AUDIO_UCM_DEVICE}|' -e 's|@PCM@|${PHONE_AUDIO_PCM}|' \
        -e 's|@CALL_VERB@|${PHONE_AUDIO_UCM_CALL_VERB}|' -e 's|@EARPIECE@|${PHONE_AUDIO_UCM_EARPIECE}|' \
        -e 's|@EARPIECE_PCM@|${PHONE_AUDIO_EARPIECE_PCM}|' -e 's|@CALL_SPEAKER@|${PHONE_AUDIO_UCM_CALL_SPEAKER}|' \
        -e 's|@CALL_SPEAKER_PCM@|${PHONE_AUDIO_CALL_SPEAKER_PCM}|' -e 's|@CALL_MIC@|${PHONE_AUDIO_UCM_CALL_MIC}|' \
        ${S}/phone-audiod-ucm.conf.in > ${D}${systemd_system_unitdir}/phone-audiod.service.d/10-phone-qcom.conf
}

PACKAGES =+ "${PN}-abslot ${PN}-uim ${PN}-btaddr ${PN}-audio"
SYSTEMD_PACKAGES = "${PN}-abslot ${PN}-uim ${PN}-btaddr"
SYSTEMD_SERVICE:${PN}-abslot = "phone-qcom-abslot.service"
SYSTEMD_SERVICE:${PN}-uim = "phone-qcom-uim.service"
SYSTEMD_SERVICE:${PN}-btaddr = "phone-qcom-btaddr.service"
FILES:${PN}-abslot = "${bindir}/phone-abslot ${systemd_system_unitdir}/phone-qcom-abslot.service"
FILES:${PN}-uim = "${libexecdir}/phone-qcom/uim-select ${systemd_system_unitdir}/phone-qcom-uim.service"
FILES:${PN}-btaddr = "${bindir}/phone-btaddr ${systemd_system_unitdir}/phone-qcom-btaddr.service"
FILES:${PN}-audio = "${systemd_system_unitdir}/phone-audiod.service.d"
RDEPENDS:${PN}-uim = "libqmi rmtfs tqftpserv"
RDEPENDS:${PN}-audio = "alsa-utils-alsaucm"
ALLOW_EMPTY:${PN} = "1"
