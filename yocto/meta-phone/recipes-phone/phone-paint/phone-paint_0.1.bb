SUMMARY = "phone-paint: the pixel renderer for the renderer slot (no Slint)"
DESCRIPTION = "Draws the frames phone-shell sends (renderer protocol phone.frame/1, \
23-renderer-protocol.md) with tiny-skia and ab_glyph, straight to DRM/KMS, keys from evdev; \
headless it writes the PNG snapshots of the boot selftest."
LICENSE = "Apache-2.0"
LIC_FILES_CHKSUM = "file://${PHONE_KARIO_LICENSE};md5=89aea4e17d99a7cacdbeed46a0096b10"

# paint/: the tarball `make yocto-src` packs (without target/, with the
# LICENSE), or the commit (phone-src.bbclass).
SRC_URI = "${@phone_src(d, 'file://phone-paint-src.tar.gz')}"
S = "${@phone_dir(d, 'paint', 'paint')}"

require ${BPN}-crates.inc

inherit cargo phone-src

export CARGO_PROFILE_RELEASE_STRIP = "false"

do_install() {
    install -d ${D}${libdir}/phone/bin
    install -m 0755 ${B}/target/${CARGO_TARGET_SUBDIR}/phone-paint ${D}${libdir}/phone/bin/phone-paint
    ln -s bin/phone-paint ${D}${libdir}/phone/renderer
}

# The renderer slot (see phone-gui): one provider per image.
RPROVIDES:${PN} += "phone-renderer"
RCONFLICTS:${PN} += "phone-renderer"

FILES:${PN} = "${libdir}/phone/bin/phone-paint ${libdir}/phone/renderer"
# DejaVu Sans covers what Noto CJK lacks: sub/superscripts and math symbols in formulas.
RDEPENDS:${PN} += "fonts-noto-cjk ttf-dejavu-sans"
