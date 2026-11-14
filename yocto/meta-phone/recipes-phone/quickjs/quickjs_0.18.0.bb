SUMMARY = "QuickJS-ng: a small embeddable JavaScript engine"
DESCRIPTION = "The engine of kario-jsrt, the runtime of TypeScript apps \
(29-app-development.md). meta-openembedded has no QuickJS recipe; this is \
QuickJS-ng (the maintained fork), the release make jsrt pins."
HOMEPAGE = "https://github.com/quickjs-ng/quickjs"
LICENSE = "MIT"
LIC_FILES_CHKSUM = "file://LICENSE;md5=7d9f1f9af65f3cca7f8bcc065cc4c4e3"

SRC_URI = "git://github.com/quickjs-ng/quickjs.git;protocol=https;branch=master;tag=v${PV}"
SRCREV = "d7215b0ad3fc2c562e8317fd9bca058fd4863804"

inherit cmake

# A static library only: kario-jsrt links the engine in, without
# quickjs-libc (the std and os modules). The image takes no package of
# this recipe; qjs and qjsc, which have std and os, stay in the build.
EXTRA_OECMAKE = " \
    -DBUILD_SHARED_LIBS=OFF \
    -DQJS_BUILD_EXAMPLES=OFF \
"

PACKAGES =+ "${PN}-compiler"
FILES:${PN}-compiler = "${bindir}/qjsc"

BBCLASSEXTEND = "native nativesdk"
