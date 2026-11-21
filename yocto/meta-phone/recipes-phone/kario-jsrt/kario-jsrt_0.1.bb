SUMMARY = "kario-jsrt: the runtime of TypeScript apps"
DESCRIPTION = "QuickJS-ng with the kario module: pages, events, capabilities, \
translations, storage and timers over the phone.Ui protocol through the C \
interface (sdk/c), and the app's own native modules (kario_native.h, \
dlopen'ed from its package). The Shell starts it for apps whose manifest \
says \"runtime\": \"js\" (29-app-development.md, 22-developer-api.md)."
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
# fetch goes through libcurl (the image has it already, for RAUC's streaming);
# the build host's runtime only compiles bytecode and goes without.
DEPENDS:append:class-target = " curl"
EXTRA_OECMAKE:append:class-native = " -DKARIO_JSRT_NET=OFF"

inherit cmake phone-src

FILES:${PN} += "${libdir}/phone/bin/kario-jsrt"

# phone-fw compiles the system's TypeScript apps to bytecode with the build
# host's: the same QuickJS writes the same bytecode for x86-64 and arm64
# (both 64-bit little-endian), and the runtime checks the engine version
# before it uses any.
BBCLASSEXTEND = "native"
