SUMMARY = "DejaVu Sans 2.37, the renderers' fallback for glyphs Noto Sans CJK lacks (sub/superscripts, math symbols)"
HOMEPAGE = "https://dejavu-fonts.github.io/"
LICENSE = "Bitstream-Vera"
LIC_FILES_CHKSUM = "file://LICENSE;md5=449b2c30bfe5fa897fe87b8b70b16cfa"

SRC_URI = "https://github.com/dejavu-fonts/dejavu-fonts/releases/download/version_2_37/dejavu-fonts-ttf-${PV}.tar.bz2"
SRC_URI[sha256sum] = "fa9ca4d13871dd122f61258a80d01751d603b4d3ee14095d65453b4e846e17d7"

S = "${UNPACKDIR}/dejavu-fonts-ttf-${PV}"

inherit allarch

do_configure[noexec] = "1"
do_compile[noexec] = "1"

do_install() {
    install -d ${D}${datadir}/fonts/truetype
    install -m 0644 ${S}/ttf/DejaVuSans.ttf ${D}${datadir}/fonts/truetype/DejaVuSans.ttf
}

FILES:${PN} = "${datadir}/fonts/truetype"
