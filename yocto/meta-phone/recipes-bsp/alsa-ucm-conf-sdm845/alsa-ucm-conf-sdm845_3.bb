SUMMARY = "ALSA UCM profiles of the OnePlus 6 and 6T"
DESCRIPTION = "The OnePlus profiles (HiFi and Voice Call) of sdm845-mainline's alsa-ucm-conf at \
the commit Nura's alsa-ucm-conf-sdm845 3 packages, and Kario's speaker in the 6T's Voice Call \
verb (fajita-voicecall-speaker.conf). Only these files are installed, beside alsa-ucm-conf, \
whose common files they include; the rest of that fork would replace it."
LICENSE = "BSD-3-Clause & Apache-2.0"
LIC_FILES_CHKSUM = "file://LICENSE;md5=20d74d74db9741697903372ad001d3b4 \
                    file://${COMMON_LICENSE_DIR}/Apache-2.0;md5=89aea4e17d99a7cacdbeed46a0096b10"

SRC_URI = "git://gitlab.com/sdm845-mainline/alsa-ucm-conf.git;protocol=https;branch=sdm845-phones \
           file://fajita-voicecall-speaker.conf"
SRCREV = "1b8d290e5aa2ca16b7f2fa8d74910ad19ef88b3a"
BB_GIT_SHALLOW = "1"

inherit allarch

do_install() {
    ucm=${D}${datadir}/alsa/ucm2
    install -d $ucm/conf.d/sdm845
    for phone in fajita enchilada; do
        install -d $ucm/OnePlus/$phone
        install -m 0644 ${S}/ucm2/OnePlus/$phone/*.conf $ucm/OnePlus/$phone/
    done
    cat ${UNPACKDIR}/fajita-voicecall-speaker.conf >> $ucm/OnePlus/fajita/VoiceCall.conf
    # The sound card's names (driver sdm845) lead to the profiles.
    ln -s ../../OnePlus/fajita/fajita.conf "$ucm/conf.d/sdm845/OnePlus 6T.conf"
    ln -s "OnePlus 6T.conf" $ucm/conf.d/sdm845/oneplus-OnePlus6T.conf
    ln -s "OnePlus 6T.conf" $ucm/conf.d/sdm845/oneplus-OnePlus6T-.conf
    ln -s ../../OnePlus/enchilada/enchilada.conf "$ucm/conf.d/sdm845/OnePlus 6.conf"
    ln -s "OnePlus 6.conf" $ucm/conf.d/sdm845/oneplus-OnePlus6.conf
    ln -s "OnePlus 6.conf" $ucm/conf.d/sdm845/oneplus-OnePlus6-.conf
}

FILES:${PN} = "${datadir}/alsa/ucm2"
RDEPENDS:${PN} = "alsa-ucm-conf"
