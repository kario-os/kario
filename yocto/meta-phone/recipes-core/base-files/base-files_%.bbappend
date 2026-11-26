# phone-initramfs mounts the root file system: read-only through dm-verity on
# an A/B disk, read-write on a single-root one. Without a "/" line systemd
# leaves it as mounted instead of remounting it read-write (25-system-update.md).
do_install:append() {
    sed -i '\#^/dev/root[[:space:]]#d' ${D}${sysconfdir}/fstab
}

# The OnePlus 6T's userdata layout: phone-initramfs mounts the root
# read-write, and its "/" line (PHONE_ROOT_FSTAB, the machine's) grows it to
# the partition at the first boot. /init is for a kernel mounting it
# itself and told init=/init, which a 6T's bootloader never lets mount
# userdata (its own root= wins, 39-oneplus-6t.md §1). In the A/B layout
# the root is read-only (PHONE_ROOT_FSTAB is empty).
# /init is a package of its own, which the machine puts into the root file
# system (MACHINE_ESSENTIAL_EXTRA_RDEPENDS): phone-initramfs may pull
# base-files in through its dependencies, and has its own /init.
do_install:append:oneplus-fajita() {
    if [ -n "${PHONE_ROOT_FSTAB}" ]; then
        echo "${PHONE_ROOT_FSTAB}" >> ${D}${sysconfdir}/fstab
    fi
    ln -sf ${base_sbindir}/init ${D}/init
}
PACKAGES:prepend:oneplus-fajita = "${PN}-init "
FILES:${PN}-init:oneplus-fajita = "/init"
