SUMMARY = "Noto Sans CJK 2.004 (OTC: SC/TC/JP/KR, including the Mono faces phone-gui uses for documents)"
HOMEPAGE = "https://github.com/notofonts/noto-cjk"
LICENSE = "OFL-1.1"
LIC_FILES_CHKSUM = "file://${UNPACKDIR}/noto-cjk-2.004-LICENSE;md5=55719faa0112708e946b820b24b14097"

BASE = "https://github.com/notofonts/noto-cjk/raw/Sans2.004"
SRC_URI = "${BASE}/Sans/OTC/NotoSansCJK-Regular.ttc;name=regular;downloadfilename=NotoSansCJK-Regular-2.004.ttc \
           ${BASE}/Sans/OTC/NotoSansCJK-Bold.ttc;name=bold;downloadfilename=NotoSansCJK-Bold-2.004.ttc \
           ${BASE}/LICENSE;name=license;downloadfilename=noto-cjk-2.004-LICENSE"
SRC_URI[regular.sha256sum] = "b76b0433203017ca80401b2ee0dd69350349871c4b19d504c34dbdd80541690a"
SRC_URI[bold.sha256sum] = "faa5f3656a78b2e2d450d27fe8382c778bc2b6bb5ea29c986664a6a435056ceb"
SRC_URI[license.sha256sum] = "6a73f9541c2de74158c0e7cf6b0a58ef774f5a780bf191f2d7ec9cc53efe2bf2"

S = "${UNPACKDIR}"

inherit allarch

do_configure[noexec] = "1"
do_compile[noexec] = "1"

do_install() {
    install -d ${D}${datadir}/fonts/noto-cjk
    install -m 0644 ${UNPACKDIR}/NotoSansCJK-Regular-2.004.ttc ${D}${datadir}/fonts/noto-cjk/NotoSansCJK-Regular.ttc
    install -m 0644 ${UNPACKDIR}/NotoSansCJK-Bold-2.004.ttc ${D}${datadir}/fonts/noto-cjk/NotoSansCJK-Bold.ttc
}

FILES:${PN} = "${datadir}/fonts/noto-cjk"
