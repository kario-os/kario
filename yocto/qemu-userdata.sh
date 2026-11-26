#!/bin/sh
# Run inside `kas-container shell`: the OnePlus 6T's userdata layout on QEMU
# (39-oneplus-6t.md §1). The image's root file system, with the 6T's /init
# link and fstab "/" line added, fills a GPT partition named userdata on a
# SCSI disk with 4096-byte sectors and the phone's partition names
# (qemu-userdata.py); the command line ends with what the phone's
# bootloader appends (39-oneplus-6t.md §1): a root= naming Android's
# system partition by PARTUUID among its androidboot.* words, and, to a
# command line without root=, skip_initramfs, rootwait, ro, init=/init and
# the slot suffix too. Through phone-initramfs with the 6T's ramdisk
# (partitions by role, the layout from the boot image) every boot must
# start the system and pass the first selftest case. Without an
# initramfs, as the 6T's boot image was until 2026-11-24, the kernel
# mounts userdata itself only while nothing is appended: the appended
# root= wins, as on the phone. A boot that cannot get anywhere is stopped
# after a while.
# USERDATA_ONLY: the selftest's cases (a regular expression of their names);
# by default the first one, the standby screen after the boot animation.
set -u
# The helper scripts next to this one (the tree is /repo, or /kario in the internal workspace).
here=$(cd "$(dirname "$0")" && pwd)
cd /work/build || exit 1
deploy=tmp/deploy/images/qemuarm64
w=/work/build/userdata-test
log=/work/build/qemu-userdata.log
rm -rf "$w" && mkdir -p "$w"
: > "$log"
sysroot=tmp/work/qemuarm64-poky-linux/phone-image/1.0/recipe-sysroot-native
debugfs=$(command -v debugfs || ls $sysroot/sbin/debugfs)
zstd=$(command -v zstd || ls $sysroot/usr/bin/zstd)

"$zstd" -q -d -f "$(readlink -f $deploy/phone-image-qemuarm64.rootfs.ext4.zst)" -o "$w/rootfs.ext4" || exit 1
# The 6T's image has these (base-files' bbappend, PHONE_ROOT_FSTAB), the
# emulator's not.
"$debugfs" -R "dump /etc/fstab $w/fstab" "$w/rootfs.ext4" 2> /dev/null
echo "/dev/root / auto defaults,x-systemd.growfs 1 1" >> "$w/fstab"
printf 'rm /etc/fstab\nwrite %s /etc/fstab\nsif /etc/fstab uid 0\nsif /etc/fstab gid 0\nsif /etc/fstab mode 0100644\nsymlink /init /sbin/init\n' \
	"$w/fstab" > "$w/debugfs.cmds"
"$debugfs" -w -f "$w/debugfs.cmds" "$w/rootfs.ext4" > /dev/null 2>&1 || exit 1
disk=$w/disk.rootfs.wic
python3 "$here/qemu-userdata.py" disk "$disk" "$w/rootfs.ext4" 256 > "$w/disk.txt" || exit 1
cat "$w/disk.txt" >> "$log"
system_a=$(sed -n 's/^system_a: .*, partuuid //p' "$w/disk.txt")
rm -f "$w/rootfs.ext4"
python3 "$here/qemu-userdata.py" ramdisk "$w/ramdisk" "$(readlink -f $deploy/phone-initramfs-qemuarm64.cpio.gz)" >> "$log" || exit 1
conf=$w/disk.rootfs.qemuboot.conf
python3 "$here/qemu-userdata.py" conf "$conf" "$(readlink -f $deploy/phone-image-qemuarm64.rootfs.qemuboot.conf)" || exit 1
kernel=$(readlink -f $deploy/Image-qemuarm64.bin)

only=5ee5be85e69cba
if [ -n "${USERDATA_ONLY:-}" ]; then
	only=$(printf %s "$USERDATA_ONLY" | od -An -tx1 | tr -d ' \n')
fi
# What a 6T's ABL appended to Kario's boot image on 2026-11-24, shortened
# (its system partition is this disk's system_a, with no file system), and
# what it appends to a command line without root= (Nura's).
abl="androidboot.verifiedbootstate=orange androidboot.keymaster=1 root=PARTUUID=$system_a androidboot.bootdevice=1d84000.ufshc androidboot.serialno=0123456789abcdef androidboot.mode=normal androidboot.platform_name=SDM845"
abl_noroot="$abl androidboot.slot_suffix=_a skip_initramfs rootwait ro init=/init"
failed=0 stop_at=

# boot <name> <initramfs|initramfs-noroot|kernel> <seconds> <appended>:
# one boot, its console in $w/<name>.log, the boot image's own command line
# first (initramfs-noroot: without root=); with $stop_at set, ended once its
# console says that (a kernel panic, after which nothing more comes).
boot() {
	case $2 in
	initramfs) own="root=PARTLABEL=userdata" ;;
	initramfs-noroot) own="" ;;
	kernel) own="root=PARTLABEL=userdata rootfstype=ext4 rootwait rw" ;;
	esac
	if [ "$2" = kernel ]; then
		set -- "$1" "$3" "$4"
	else
		set -- "$1" "$3" "$4" "qemuparams=-initrd $w/ramdisk"
	fi
	name=$1 secs=$2 appended=$3
	shift 3
	echo "=== $name: $own phone.selftest=1 ... $appended" >> "$log"
	timeout "$secs" runqemu "$conf" "$kernel" "$disk" nographic slirp snapshot "$@" \
		bootparams="$own phone.selftest=1 phone.selftest-only=$only $appended" < /dev/null > "$w/$name.log" 2>&1 &
	q=$!
	while [ -n "$stop_at" ] && kill -0 $q 2>/dev/null; do
		if grep -aq "$stop_at" "$w/$name.log"; then
			sleep 2
			kill $q
			break
		fi
		sleep 1
	done
	wait $q
	sed 's/\x1b\[[0-9;]*m//g' "$w/$name.log" | tr -d '\r' > "$w/$name.txt"
	cat "$w/$name.txt" >> "$log"
}

# result <name> <PASS|FAIL> <what>
result() {
	echo "PHONE-USERDATA: $1: $2 ($3)" | tee -a "$log"
	[ "$2" = PASS ] || failed=1
}

# started <name>: the initramfs found userdata by its name and mounted it,
# and the selftest passed on it.
started() {
	t=$w/$1.txt
	how=$(grep -a -o 'phone-initramfs: the root file system: [^ ]*, the partition userdata' "$t" | head -n 1)
	if [ -n "$how" ] && grep -aq 'PHONE-SELFTEST-RESULT: PASS' "$t"; then
		result "$1" PASS "${how#phone-initramfs: }; selftest passed"
	else
		result "$1" FAIL "${how:-the initramfs did not find userdata}; $(grep -a -o 'PHONE-SELFTEST-RESULT: [A-Z]*' "$t" | tail -n 1)"
	fi
}

boot initramfs-6t initramfs 1800 "$abl"
started initramfs-6t
boot initramfs-noroot initramfs-noroot 1800 "$abl_noroot"
started initramfs-noroot

# The 6T's boot image until 2026-11-24 (no initramfs): the kernel's own
# PARTLABEL= lookup works while nothing is appended.
boot kernel-alone kernel 1800 ""
if grep -aq 'PHONE-SELFTEST-RESULT: PASS' "$w/kernel-alone.txt"; then
	result kernel-alone PASS "the kernel mounted userdata by its name; selftest passed"
else
	result kernel-alone FAIL "$(grep -a -m 1 -e 'VFS:' -e 'Waiting for root' "$w/kernel-alone.txt")"
fi
stop_at="Kernel panic"
boot kernel-6t kernel 300 "$abl"
if grep -aq 'unknown-block(8,3)' "$w/kernel-6t.txt" && ! grep -aq 'systemd\[1\]' "$w/kernel-6t.txt"; then
	result kernel-6t PASS "as on the phone the kernel goes for system_a: $(grep -a -m 1 -o 'VFS: [^.]*' "$w/kernel-6t.txt")"
else
	result kernel-6t FAIL "the kernel was expected to mount system_a (sda3), the appended root="
fi
grep -a -h -e 'phone-initramfs:' -e 'EXT4-fs (sda5)' "$w/initramfs-6t.txt" | sed 's/^/  /' | tee -a "$log"
rm -f "$disk"
exit $failed
