SUMMARY = "AOSP's mkbootimg and unpack_bootimg, for Android boot images"
DESCRIPTION = "The Android boot image tools (Python, every header version), for the boot images \
of phones whose stock bootloader loads one (phone-bootimg)."
HOMEPAGE = "https://android.googlesource.com/platform/system/tools/mkbootimg"
LICENSE = "Apache-2.0"
LIC_FILES_CHKSUM = "file://mkbootimg.py;beginline=3;endline=15;md5=4b298fb007f09b49f4fd3c8bf859805e"

SRC_URI = "git://android.googlesource.com/platform/system/tools/mkbootimg;protocol=https;branch=main"
SRCREV = "d2bb0af5ba6d3198a3e99529c97eda1be0b5a093"
PV = "0+git"
BB_GIT_SHALLOW = "1"

inherit native

do_configure[noexec] = "1"
do_compile[noexec] = "1"

# The scripts import gki/ from their own directory.
do_install() {
    install -d ${D}${libexecdir}/mkbootimg/gki ${D}${bindir}
    install -m 0755 ${S}/mkbootimg.py ${S}/unpack_bootimg.py ${D}${libexecdir}/mkbootimg/
    install -m 0644 ${S}/gki/generate_gki_certificate.py ${D}${libexecdir}/mkbootimg/gki/
    ln -s ../libexec/mkbootimg/mkbootimg.py ${D}${bindir}/mkbootimg
    ln -s ../libexec/mkbootimg/unpack_bootimg.py ${D}${bindir}/unpack_bootimg
}
