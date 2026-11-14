SUMMARY = "phone-gui: a pixel renderer for the phone.Ui semantic tree (Rust + Slint)"
DESCRIPTION = "Draws the frames phone-shell sends (renderer protocol phone.frame/1, \
23-renderer-protocol.md) with Slint's software renderer, straight to DRM/KMS, and forwards keys \
from libinput back to the shell. Also runs headless to write PNG snapshots during the boot selftest."
# GPL-3.0-only, Slint under its GPL (gui/COPYING): in the renderer slot only
# of an image built to compare it with phone-paint (kas/phone-gui.yml).
LICENSE = "GPL-3.0-only"
LIC_FILES_CHKSUM = "file://COPYING;md5=c79ff39f19dfec6d293b95dea7b07891"

# gui/: the tarball `make yocto-src` packs (without target/), or the commit
# (phone-src.bbclass).
SRC_URI = "${@phone_src(d, 'file://phone-gui-src.tar.gz')}"
S = "${@phone_dir(d, 'gui', 'gui')}"

require ${BPN}-crates.inc

inherit cargo pkgconfig phone-src

DEPENDS += "libinput libxkbcommon udev"

# fontconfig is loaded at run time if present; fonts come from SLINT_FONT_PATH.
export RUST_FONTCONFIG_DLOPEN = "on"

CARGO_BUILD_FLAGS:append = " --features kms"
# Leave stripping to the packaging step (debug symbols go to -dbg).
export CARGO_PROFILE_RELEASE_STRIP = "false"

do_install() {
    install -d ${D}${libdir}/phone/bin
    install -m 0755 ${B}/target/${CARGO_TARGET_SUBDIR}/phone-gui ${D}${libdir}/phone/bin/phone-gui
    ln -s bin/phone-gui ${D}${libdir}/phone/renderer
}

# Exactly one package in an image fills the renderer slot (the shell runs
# ${libdir}/phone/renderer); every provider rprovides and rconflicts it.
RPROVIDES:${PN} += "phone-renderer"
RCONFLICTS:${PN} += "phone-renderer"

FILES:${PN} = "${libdir}/phone/bin/phone-gui ${libdir}/phone/renderer"
# xkbcommon needs the keymap data to turn evdev key codes into keys.
RDEPENDS:${PN} += "fonts-noto-cjk ttf-dejavu-sans xkeyboard-config"
