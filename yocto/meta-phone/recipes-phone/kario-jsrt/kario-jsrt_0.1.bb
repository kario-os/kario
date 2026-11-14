SUMMARY = "kario-jsrt: the runtime of TypeScript apps"
DESCRIPTION = "QuickJS-ng with the kario module: pages, events, capabilities, \
translations, storage and timers over the phone.Ui protocol through the C \
interface (sdk/c). The Shell starts it for apps whose manifest says \
\"runtime\": \"js\" (29-app-development.md, 22-developer-api.md)."
LICENSE = "Apache-2.0"
LIC_FILES_CHKSUM = "file://${@phone_dir(d, 'kario/LICENSE', 'LICENSE')};md5=89aea4e17d99a7cacdbeed46a0096b10"

# jsrt/ and sdk/c/ of the tarball `make yocto-src` packs for phone-fw (the
# working tree as kario/), or the commit (phone-src.bbclass).
FILESEXTRAPATHS:prepend := "${THISDIR}/../phone-fw/files:"
SRC_URI = "${@phone_src(d, 'file://phone-fw-src.tar.gz')}"
# S is the whole tree, so the debug info's paths of sdk/c (beside jsrt/) are
# mapped too (the buildpaths check).
S = "${@phone_dir(d, 'kario', '')}"
OECMAKE_SOURCEPATH = "${S}/jsrt"

DEPENDS = "quickjs"

inherit cmake phone-src

FILES:${PN} += "${libdir}/phone/bin/kario-jsrt"
