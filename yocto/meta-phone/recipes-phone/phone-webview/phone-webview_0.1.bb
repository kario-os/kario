SUMMARY = "phone-webview: the web view process behind web nodes (WPE WebKit)"
DESCRIPTION = "Renders an app's pages into shared memory media surfaces for the pixel \
renderer, with spatial navigation, form editing through the system input method, system \
dialogs and the phone.call bridge to the shell's broker (21-web-and-js.md §5). phone-shell \
starts one per host app, sandboxed under the app's web uid. Links WPE WebKit's legacy API \
(libwpe + WPEBackend-fdo, shared memory buffers), so wpewebkit needs no extra PACKAGECONFIG."
LICENSE = "Apache-2.0"
LIC_FILES_CHKSUM = "file://${PHONE_KARIO_LICENSE};md5=89aea4e17d99a7cacdbeed46a0096b10"

# webview/: read from the working tree kas mounts, or the commit
# (phone-src.bbclass).
FILESEXTRAPATHS:prepend := "${THISDIR}/../../../../webview:${THISDIR}/../../../..:"
SRC_URI = "${@phone_src(d, 'file://phone-webview.c file://view.c file://view.h file://nav.js file://bridge.js file://scripts.sh file://Makefile file://locale/zh-Hans.json file://locale/zh-Hant.json file://locale/es.json file://LICENSE')}"
S = "${@phone_dir(d, '', 'webview')}"

DEPENDS = "wpewebkit libwpe wpebackend-fdo wayland glib-2.0"

inherit pkgconfig phone-src

do_compile() {
    oe_runmake -C ${S} O=${B}
}

do_install() {
    install -d ${D}${libdir}/phone/bin
    install -m 0755 ${B}/phone-webview ${D}${libdir}/phone/bin/phone-webview
}

# WebKit picks fonts through fontconfig: the same CJK face as the renderer.
RDEPENDS:${PN} += "wpebackend-fdo fonts-noto-cjk"

FILES:${PN} = "${libdir}/phone/bin/phone-webview"
