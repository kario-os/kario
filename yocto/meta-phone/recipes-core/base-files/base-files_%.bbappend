# phone-initramfs mounts the root file system: read-only through dm-verity on
# an A/B disk, read-write on a single-root one. Without a "/" line systemd
# leaves it as mounted instead of remounting it read-write (25-system-update.md).
do_install:append() {
    sed -i '\#^/dev/root[[:space:]]#d' ${D}${sysconfdir}/fstab
}

# The OnePlus 6T's kernel mounts the root itself (no initramfs): its "/" line
# (PHONE_ROOT_FSTAB, the machine's), and /init, so that a bootloader that
# appends "ro init=/init" still starts systemd, which remounts it read-write.
do_install:append:oneplus-fajita() {
    echo "${PHONE_ROOT_FSTAB}" >> ${D}${sysconfdir}/fstab
    ln -sf ${base_sbindir}/init ${D}/init
}
FILES:${PN}:append:oneplus-fajita = " /init"
