SUMMARY = "phone-capi: the C ABI SDK for phone.Ui apps (libphoneui) and its C++ sample app"
DESCRIPTION = "libphoneui lets C and C++ apps speak the phone.Ui protocol (fw/sdk/c, \
22-developer-api.md §9). The image carries the shared library and the sample app; \
the header and the static library go to the -dev and -staticdev packages."
LICENSE = "Apache-2.0"
LIC_FILES_CHKSUM = "file://${PHONE_KARIO_LICENSE};md5=89aea4e17d99a7cacdbeed46a0096b10"

# sdk/c: the tarball `make yocto-src` packs (as capi/, with the LICENSE), or
# the commit (phone-src.bbclass).
SRC_URI = "${@phone_src(d, 'file://phone-capi-src.tar.gz')}"
S = "${@phone_dir(d, 'capi', 'sdk/c')}"

inherit cmake phone-src

FILES:${PN} += "${libdir}/phone/bin/phone-app-hellocpp"
# The C++ sample's UI text (its message catalog).
FILES:${PN} += "${datadir}/phone/apps/hello-cpp/locale"
