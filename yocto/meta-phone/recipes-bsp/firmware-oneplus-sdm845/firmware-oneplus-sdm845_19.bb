SUMMARY = "Firmware of the OnePlus 6 and 6T: modem, DSPs, Wi-Fi, GPU, video, Bluetooth, speaker amplifier"
DESCRIPTION = "The signed firmware the OnePlus 6/6T's remote processors and GPU run, from \
sdm845-mainline's firmware-oneplus-sdm845 at the commit Nura's package 19 installs, \
fetched at build time (none of it is in git), plus linux-firmware's Adreno 630, WCN3990 \
Wi-Fi and Bluetooth files."
# Proprietary and not redistributable: the files are fetched from where Nura
# gets them, for the owner's own test phone. An image with this package is
# not to be published or given away.
LICENSE = "CLOSED"

SRC_URI = "git://gitlab.com/sdm845-mainline/firmware-oneplus-sdm845.git;protocol=https;branch=prepackaged-release"
SRCREV = "3e31a0c3e5a061645c09f805387b49fa9d35acbf"
BB_GIT_SHALLOW = "1"

inherit allarch

# Nura's selection: what the device tree's firmware-name entries and the
# Bluetooth driver ask for (the 6 and 6T share the enchilada directory),
# and the domain lists of the DSPs. Not the sensor DSP's files for
# hexagonrpcd (GPL-3.0, not in Kario) nor the modem's carrier
# configurations (modem_pr).
FW_QCOM = "a630_zap.mbn adsp.mbn adspr.jsn adspua.jsn cdsp.mbn cdspr.jsn ipa_fws.mbn mba.mbn \
           modem.mbn modemr.jsn modemuw.jsn slpi.mbn slpir.jsn slpius.jsn venus.mbn wlanmdsp.mbn"

do_install() {
    fw=${D}${nonarch_base_libdir}/firmware
    install -d $fw/qcom/sdm845/OnePlus/enchilada $fw/qca/OnePlus/enchilada $fw/updates/qca
    for f in ${FW_QCOM}; do
        install -m 0644 ${S}/lib/firmware/qcom/sdm845/OnePlus/enchilada/$f $fw/qcom/sdm845/OnePlus/enchilada/
    done
    install -m 0644 ${S}/lib/firmware/qca/OnePlus/enchilada/crnv21.bin $fw/qca/OnePlus/enchilada/
    # Nura puts these in a search path of its own, ahead of linux-firmware's
    # Bluetooth firmware; the kernel reads updates/ first.
    install -m 0644 ${S}/lib/firmware/postmarketos/qca/crbtfw21.tlv $fw/updates/qca/
    install -m 0644 ${S}/lib/firmware/postmarketos/tfa98xx.cnt $fw/updates/
}

FILES:${PN} = "${nonarch_base_libdir}/firmware"
RDEPENDS:${PN} = "linux-firmware-qcom-adreno-a630 linux-firmware-ath10k-wcn3990 linux-firmware-qca-wcn399x"

# Signed Hexagon ELF files: stripped they would no longer load.
INHIBIT_PACKAGE_STRIP = "1"
INHIBIT_PACKAGE_DEBUG_SPLIT = "1"
INHIBIT_SYSROOT_STRIP = "1"
INSANE_SKIP:${PN} += "arch"
