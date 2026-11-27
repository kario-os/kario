# phone-initramfs mounts the root file system: read-only through dm-verity on
# an A/B disk, read-write on a single-root one. Without a "/" line systemd
# leaves it as mounted instead of remounting it read-write (25-system-update.md).
do_install:append() {
    sed -i '\#^/dev/root[[:space:]]#d' ${D}${sysconfdir}/fstab
}
