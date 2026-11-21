#!/bin/sh
# Puts the Kario image on a OnePlus 6T in fastboot mode, and takes it off
# again (39-oneplus-6t.md).
#
#   fajita-flash.sh [options] backup TWRP.img   copy what cannot be made again off the phone (nothing written)
#   fajita-flash.sh [options] test              boot Kario once, writing neither boot nor userdata
#   fajita-flash.sh [options] flash             write boot_a, boot_b and userdata, then reboot
#   fajita-flash.sh [options] restore           back to OxygenOS: boot and dtbo from the backup, userdata wiped
#   fajita-flash.sh [options] restore-efs TWRP.img   write the backup's IMEI and calibration partitions back
#
#   -n, --dry-run     print the fastboot commands, run none (no phone needed)
#   -d, --dir DIR     the build's images (default: $WORK/build/tmp/deploy/images/oneplus-fajita,
#                     WORK defaulting to ~/yocto/fajita-work)
#   -b, --backup DIR  the phone's backup (default: ~/kario-phone-backup/oneplus-fajita-<serial>)
#   --stock DIR       restore: OxygenOS's boot.img and dtbo.img (from its package's payload.bin)
#                     instead of the backup's, when the backup was taken after another system
#   -s, --serial SN   the phone, when more than one is connected
#   -y, --yes         do not ask before writing
#
# test and flash refuse a phone without a backup that checks out: the
# IMEI (modemst1, modemst2, fsg, fsc) and the sensors' and fingerprint's
# calibration (persist) exist on this phone only, and the MSM download tool
# that unbricks it may wipe them. backup runs TWRP from RAM (fastboot boot),
# so it writes nothing, and it never replaces a backup already there.
# test and flash erase dtbo_a and dtbo_b first: the bootloader would apply
# Android's device tree overlays from them to the mainline device tree and
# stay in fastboot. The bootloader names the SoC, not the phone (product
# sdm845, as on the OnePlus 6), so anything else is refused and the rest
# asked.
set -eu

PARTS="persist modemst1 modemst2 fsg fsc boot_a boot_b dtbo_a dtbo_b"
EFS="persist modemst1 modemst2 fsg fsc"
LUNS="sda sdb sdc sdd sde sdf"

dry=0 yes=0 serial='' dir='' backup='' stock=''
usage() {
	awk 'NR > 1 && !/^#/ { exit } NR > 1 { sub(/^# ?/, ""); print }' "$0" >&2
	exit 2
}
die() {
	echo "fajita-flash: $*" >&2
	exit 1
}
while [ $# -gt 0 ]; do
	case $1 in
	-n | --dry-run) dry=1 ;;
	-y | --yes) yes=1 ;;
	-d | --dir) [ $# -ge 2 ] || usage; dir=$2; shift ;;
	-b | --backup) [ $# -ge 2 ] || usage; backup=$2; shift ;;
	--stock) [ $# -ge 2 ] || usage; stock=$2; shift ;;
	-s | --serial) [ $# -ge 2 ] || usage; serial=$2; shift ;;
	-h | --help) usage ;;
	-*) usage ;;
	*) break ;;
	esac
	shift
done
[ $# -ge 1 ] || usage
cmd=$1
twrp=''
case $cmd in
test | flash | restore) [ $# -eq 1 ] || usage ;;
backup | restore-efs) [ $# -eq 2 ] || usage; twrp=$2 ;;
*) usage ;;
esac

dir=${dir:-${WORK:-$HOME/yocto/fajita-work}/build/tmp/deploy/images/oneplus-fajita}
boot=$dir/boot-oneplus-fajita.img
userdata=$dir/phone-image-oneplus-fajita.rootfs.ext4.sparse

fb() {
	if [ -n "$serial" ]; then
		set -- -s "$serial" "$@"
	fi
	if [ "$dry" = 1 ]; then
		echo "fastboot $*"
	else
		fastboot "$@"
	fi
}

ad() {
	if [ -n "$serial" ]; then
		set -- -s "$serial" "$@"
	fi
	adb "$@"
}

getvar() {
	fb getvar "$1" 2>&1 | sed -n "s/^$1: *//p" | head -n 1
}

need() {
	[ -f "$1" ] || die "no $1 (built? -d names another directory)"
}

confirm() {
	[ "$yes" = 1 ] && return 0
	printf 'This must be a OnePlus 6T (the bootloader cannot tell it from a 6). %s. Type yes: ' "$1"
	read -r answer
	[ "$answer" = yes ] || { echo "fajita-flash: nothing done"; exit 1; }
}

# A boot image that is not OxygenOS's: Kario's (phone.gui=) or one from
# postmarketOS or Nura (pmos_ in its command line).
foreign_boot() {
	head -c 4096 "$1" | grep -a -q -e 'phone\.gui=' -e 'pmos_'
}

# Whether $backup holds every partition of this phone, unchanged since it
# was taken.
backup_ok() {
	[ -f "$backup/phone.txt" ] && [ -f "$backup/SHA256SUMS" ] || return 1
	grep -qx "serial=$serial" "$backup/phone.txt" || return 1
	for p in $PARTS; do
		grep -q "  $p\.img\$" "$backup/SHA256SUMS" || return 1
	done
	(cd "$backup" && sha256sum -c --quiet SHA256SUMS) >/dev/null 2>&1
}

# The phone in fastboot mode: an SDM845, unlocked; its serial number.
fastboot_phone() {
	command -v fastboot >/dev/null || die "no fastboot on PATH"
	[ -n "$(fb devices)" ] || die "no phone in fastboot mode (power and volume up held; fastboot devices)"
	product=$(getvar product)
	[ "$product" = sdm845 ] || die "the phone says product '$product', a OnePlus 6T says sdm845: refusing"
	case $(getvar unlocked) in
	yes) ;;
	no) die "the bootloader is locked (fastboot oem unlock, which wipes the phone)" ;;
	*) echo "fajita-flash: the bootloader does not say whether it is unlocked (fastboot oem device-info does)" >&2 ;;
	esac
	sn=$(getvar serialno)
	[ -n "$sn" ] || die "the phone gives no serial number (fastboot getvar serialno)"
	[ -z "$serial" ] || [ "$serial" = "$sn" ] || die "the phone says serial $sn, -s says $serial"
	serial=$sn
	slot=$(getvar current-slot)
	backup=${backup:-$HOME/kario-phone-backup/oneplus-fajita-$serial}
	echo "fajita-flash: SDM845 phone $serial, unlocked, booting slot ${slot:-?}"
}

# TWRP from RAM, then its adb; the device paths of the partitions.
twrp_up() {
	[ -f "$twrp" ] || die "no $twrp (TWRP for the OnePlus 6T, fajita)"
	[ "$(head -c 8 "$twrp")" = "ANDROID!" ] || die "$twrp is not an Android boot image"
	command -v adb >/dev/null || die "no adb on PATH"
	fb boot "$twrp"
	echo "fajita-flash: waiting for TWRP's adb (2 minutes at most)"
	timeout 120 adb -s "$serial" wait-for-recovery || die "TWRP did not come up (adb devices); the phone is unchanged"
	# shellcheck disable=SC2016 # the phone's shell expands $d
	byname=$(ad shell 'for d in /dev/block/bootdevice/by-name /dev/block/by-name; do if [ -e $d/persist ]; then echo $d; break; fi; done' | tr -d '\r')
	[ -n "$byname" ] || die "TWRP shows no partition named persist"
}

remote_sum() {
	ad shell "sha256sum $1" | tr -d '\r' | awk '{ print $1 }'
}

do_backup() {
	fastboot_phone
	if [ -e "$backup/SHA256SUMS" ]; then
		if backup_ok; then
			echo "fajita-flash: $backup already holds this phone's backup; nothing done (it is never replaced)"
			exit 0
		fi
		die "$backup holds a backup that does not check out (another phone, or changed): move it away first"
	fi
	twrp_up
	mkdir -p "$backup"
	for p in $PARTS; do
		ad exec-out "dd if=$byname/$p bs=4096 2>/dev/null" >"$backup/$p.img"
		want=$(remote_sum "$byname/$p")
		got=$(sha256sum "$backup/$p.img" | awk '{ print $1 }')
		if [ -z "$want" ] || [ "$want" != "$got" ]; then
			die "$p: the copy differs from the phone ($got, the phone $want)"
		fi
		echo "fajita-flash: $p $(wc -c <"$backup/$p.img") bytes"
	done
	for l in $LUNS; do
		ad exec-out "dd if=/dev/block/$l bs=4096 count=6 2>/dev/null" >"$backup/gpt-$l.bin"
	done
	stock_boot=yes
	foreign_boot "$backup/boot_a.img" && stock_boot=no
	{
		echo "serial=$serial"
		echo "slot=${slot:-}"
		echo "taken=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
		echo "stock-boot=$stock_boot"
	} >"$backup/phone.txt"
	(cd "$backup" && sha256sum ./*.img ./*.bin | sed 's,  \./,  ,' >SHA256SUMS)
	ad reboot bootloader
	echo "fajita-flash: backup in $backup; copy it to another disk too: it is this phone's only copy of its IMEI and calibration"
	[ "$stock_boot" = yes ] || echo "fajita-flash: boot_a is not OxygenOS's: restore will want --stock (OxygenOS's boot.img and dtbo.img)" >&2
}

need_backup() {
	backup_ok || die "no backup of phone $serial that checks out in $backup: run fajita-flash.sh backup TWRP.img first (39-oneplus-6t.md §3)"
	echo "fajita-flash: backup of $serial in $backup checks out"
}

do_restore() {
	fastboot_phone
	need_backup
	if [ -n "$stock" ]; then
		rboot=$stock/boot.img rdtbo=$stock/dtbo.img
		if [ ! -f "$rboot" ] || [ ! -f "$rdtbo" ]; then
			die "--stock $stock wants boot.img and dtbo.img"
		fi
	else
		grep -qx "stock-boot=yes" "$backup/phone.txt" || die "the backup's boot is not OxygenOS's: --stock DIR with OxygenOS's boot.img and dtbo.img"
		rboot=$backup/boot_a.img rdtbo=$backup/dtbo_a.img
	fi
	foreign_boot "$rboot" && die "$rboot is not OxygenOS's boot image"
	confirm "write OxygenOS's boot and dtbo to both slots and wipe userdata (everything on it is lost)"
	for s in a b; do
		if [ -z "$stock" ]; then
			rboot=$backup/boot_$s.img rdtbo=$backup/dtbo_$s.img
		fi
		fb flash "boot_$s" "$rboot"
		fb flash "dtbo_$s" "$rdtbo"
	done
	fb -w
	fb reboot
	echo "fajita-flash: OxygenOS should start (its first start after the wipe takes a few minutes)"
}

do_restore_efs() {
	fastboot_phone
	need_backup
	confirm "write the backup's $EFS back (only after they were lost: no IMEI, no fingerprint)"
	twrp_up
	for p in $EFS; do
		ad push "$backup/$p.img" "/tmp/$p.img" >/dev/null
		ad shell "dd if=/tmp/$p.img of=$byname/$p bs=4096 2>/dev/null && rm /tmp/$p.img"
		want=$(sha256sum "$backup/$p.img" | awk '{ print $1 }')
		[ "$(remote_sum "$byname/$p")" = "$want" ] || die "$p: the phone does not read back the backup"
		echo "fajita-flash: $p written back"
	done
	ad reboot bootloader
}

case $cmd in
backup)
	[ "$dry" = 0 ] || die "backup has no dry run (it writes nothing anyway)"
	do_backup
	exit 0
	;;
restore)
	[ "$dry" = 0 ] || die "restore has no dry run"
	do_restore
	exit 0
	;;
restore-efs)
	[ "$dry" = 0 ] || die "restore-efs has no dry run"
	do_restore_efs
	exit 0
	;;
esac

need "$boot"
[ "$cmd" = test ] || need "$userdata"
[ "$(head -c 8 "$boot")" = "ANDROID!" ] || die "$boot is not an Android boot image"

if [ "$dry" = 1 ]; then
	echo "# dry run: the checks would read product, unlocked, serialno and current-slot, and the phone's backup"
else
	fastboot_phone
	need_backup
	if [ "$cmd" = flash ]; then
		confirm "erase dtbo_a and dtbo_b, write boot_a, boot_b and userdata (everything on it is lost)"
	else
		confirm "erase dtbo_a and dtbo_b, then boot Kario once"
	fi
fi

fb erase dtbo_a
fb erase dtbo_b
case $cmd in
test)
	fb boot "$boot"
	;;
flash)
	fb flash boot_a "$boot"
	fb flash boot_b "$boot"
	fb -S 512M flash userdata "$userdata"
	fb reboot
	;;
esac
