SUMMARY = "phone-initramfs: starts the system in the slot the boot state chooses (R37), or on the userdata layout's root"
LICENSE = "Apache-2.0"

# cfg80211 is built in (phone-radio.cfg) and reads the Wi-Fi regulatory
# database once, while the kernel starts, from the initramfs; without it
# every boot kept the world domain's rules ("failed to load regulatory.db").
PACKAGE_INSTALL = "busybox phone-ota-init wireless-regdb-static ${PHONE_INITRAMFS_EXTRA}"
# Where the bootloader keeps the A/B slot: the board's partitions by role
# (phone-sku-partitions: the root slots, or the data partition that is all
# of the userdata layout's root) and what reads the slot
# (43-phone-partitions.md). Without them the initramfs takes the names of
# the emulator's disk. phone-abslot names no vendor: the machine's BSP
# layer RPROVIDES it (24-sku-framework.md §8.7.3, tools/sku.py checks).
PHONE_SLOT_KEEPER ??= "state"
PHONE_INITRAMFS_EXTRA ??= "${@'phone-sku-partitions phone-abslot' if d.getVar('PHONE_SLOT_KEEPER') == 'abl' else ''}"
# Firmware a built-in driver loads while the kernel starts, before the root
# is mounted: the files, as the kernel asks for them (under
# ${nonarch_base_libdir}/firmware), and the packages that have them. Of
# what those packages and the packages they need install there, only the
# files and the license files of the packages that have them stay (a
# vendor's firmware package is tens of MB), and what the initramfs's other
# packages install (regulatory.db). The machine sets them; the OnePlus 6T's
# GPU (42-gpu-rendering.md §5).
PHONE_INITRAMFS_FIRMWARE ??= ""
PHONE_INITRAMFS_FIRMWARE_PACKAGES ??= ""
PACKAGE_INSTALL += "${PHONE_INITRAMFS_FIRMWARE_PACKAGES}"
ROOTFS_POSTPROCESS_COMMAND += "phone_initramfs_firmware"

python phone_initramfs_firmware() {
    import json
    import oe.packagedata
    keep = d.getVar('PHONE_INITRAMFS_FIRMWARE').split()
    pkgs = d.getVar('PHONE_INITRAMFS_FIRMWARE_PACKAGES').split()
    if bool(keep) != bool(pkgs):
        bb.fatal("PHONE_INITRAMFS_FIRMWARE and PHONE_INITRAMFS_FIRMWARE_PACKAGES go together: the files and the packages that have them")
    if not keep:
        return
    fw = d.getVar('nonarch_base_libdir') + '/firmware/'

    def closure(todo):
        """The packages todo names and those they need: their files under
        fw, their RDEPENDS."""
        files, deps, todo = {}, {}, list(todo)
        while todo:
            rdep = todo.pop()
            for pkg, _ in oe.packagedata.foreach_runtime_provider_pkgdata(d, rdep, include_rdep=True):
                if pkg in files or not oe.packagedata.has_subpkgdata(pkg, d):
                    continue
                data = oe.packagedata.read_subpkgdata_dict(pkg, d)
                files[pkg] = [f[len(fw):] for f in json.loads(data.get('FILES_INFO') or '{}') if f.startswith(fw)]
                deps[pkg] = list(bb.utils.explode_deps(data.get('RDEPENDS') or ''))
                todo += deps[pkg]
        return files, deps

    files, deps = closure(pkgs)
    others, _ = closure(p for p in d.getVar('PACKAGE_INSTALL').split() if p not in pkgs)
    owners = {f: pkg for pkg in files for f in files[pkg]}
    stay = set(keep).union(*others.values())
    for f in keep:
        if f not in owners:
            bb.fatal(f"PHONE_INITRAMFS_FIRMWARE: no package of PHONE_INITRAMFS_FIRMWARE_PACKAGES and those they need has {fw}{f}")
        stay.update(l for dep in deps[owners[f]] if dep.endswith('-license') for l in files.get(dep, []))
    root = d.getVar('IMAGE_ROOTFS') + fw
    for f in sorted(set(owners) - stay):
        if os.path.lexists(root + f):
            os.remove(root + f)
    for top, dirs, names in os.walk(root, topdown=False):
        if top != root and not os.listdir(top):
            os.rmdir(top)
    for f in keep:
        if not os.path.isfile(root + f):
            bb.fatal(f"PHONE_INITRAMFS_FIRMWARE: {fw}{f} is not in the initramfs")
}

IMAGE_FEATURES = ""
IMAGE_LINGUAS = ""
IMAGE_FSTYPES = "${INITRAMFS_FSTYPES}"
IMAGE_NAME_SUFFIX ?= ""
IMAGE_ROOTFS_SIZE = "8192"
IMAGE_ROOTFS_EXTRA_SPACE = "0"
USE_DEVFS = "0"
IMAGE_DEVICE_TABLES = "files/phone-initramfs-devices.txt"
do_rootfs[file-checksums] += "${THISDIR}/../../files/phone-initramfs-devices.txt:True"

inherit image
