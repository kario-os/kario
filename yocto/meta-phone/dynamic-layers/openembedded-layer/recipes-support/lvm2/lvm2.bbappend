# Only the libdevmapper package of this recipe goes into an image (veritysetup
# in phone-initramfs needs it). lvm2's own tools are bash scripts and stay out
# of every image, which keep no GPLv3 (bash, GNU coreutils) at all; without
# this their dependencies would fail the build, or build a substitute
# coreutils, even though nothing installs them.
RDEPENDS:${PN}:remove = "bash"
RDEPENDS:${PN}-scripts:remove = "bash coreutils"
INSANE_SKIP:${PN} += "file-rdeps"
INSANE_SKIP:${PN}-scripts += "file-rdeps"
