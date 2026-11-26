#!/bin/sh
# Puts the Kario image on a OnePlus 6T in fastboot mode, and takes it off
# again (39-oneplus-6t.md).
#
#   fajita-flash.sh [options] backup TWRP.img   copy what cannot be made again off the phone (nothing written)
#   fajita-flash.sh [options] test              boot Kario once, writing neither boot nor the system's partitions
#   fajita-flash.sh [options] flash             write the layout's partitions, then reboot
#   fajita-flash.sh [options] restore           back to OxygenOS: boot and dtbo (the A/B layout: and system) from the backup or --stock, userdata wiped
#   fajita-flash.sh [options] restore-efs TWRP.img   write the backup's IMEI and calibration partitions back
#
#   -n, --dry-run     print what would be written and the fastboot commands, run none (no phone needed)
#   -L, --layout L    userdata: the root file system is all of userdata (the board's default);
#                     ab: it goes into system_a and system_b, userdata holds the data, the
#                     images are phone-fajita-ab.yml's (43-phone-partitions.md; not tried on a
#                     phone). restore takes the layout the last flash noted in the backup
#   -d, --dir DIR     the build's images (default: $WORK/build/tmp/deploy/images/oneplus-fajita,
#                     WORK defaulting to ~/yocto/fajita-work)
#   -b, --backup DIR  the phone's backup (default: ~/kario-phone-backup/oneplus-fajita-<serial>)
#   --stock DIR       restore: OxygenOS's boot.img and dtbo.img (the A/B layout: and system.img),
#                     from its package's payload.bin, instead of the backup's
#   --chunk SIZE      the most fastboot sends in one download (fastboot -S; default 64M):
#                     a bigger image goes in pieces of this size
#   -s, --serial SN   the phone, when more than one is connected
#   -y, --yes         do not ask before writing
#
# The partitions are the board file's (sku/boards/oneplus-fajita.toml, as
# tools/sku.py partitions prints them): a flash writes those whose role
# the layout gives an image (boot, root, data), erases those it empties
# (dtbo; userdata in the A/B layout) and changes nothing else: never a
# partition the table keeps, and never one that holds a phone's own data,
# the bootloader's or the firmware (NEVER), whatever the table says. Before
# it writes anything it reads the size of each partition it writes from
# the phone and refuses an image that does not fit. Every image goes in
# downloads of at most --chunk: on a 6T, the 512 MiB download of the
# userdata image failed after 471 MB (39-oneplus-6t.md §6), in pieces of
# 64 MiB it went through.
# test and flash refuse images that are not Kario's for this phone (the
# boot image's command line and the device tree appended to its kernel,
# the layout its command line names, the sparse format of the rest), and a
# boot image whose ramdisk is not phone-initramfs behind the archive that
# names its layout (a kernel mounting the root itself takes the last root=,
# and the 6T's bootloader appends one naming Android's system partition,
# 39-oneplus-6t.md §1), dry runs too.
# test and flash refuse a phone without a backup that checks out: the
# IMEI (modemst1, modemst2, fsg, fsc) and the sensors' and fingerprint's
# calibration (persist) exist on this phone only, and the MSM download tool
# that unbricks it may wipe them. backup runs TWRP from RAM (fastboot boot),
# so it writes nothing, and it never replaces a backup already there.
# boot and dtbo may be missing from a backup taken by hand after the phone
# had lost them (39-oneplus-6t.md §9.1): restore then wants --stock.
# test and flash erase dtbo_a and dtbo_b first: the bootloader would apply
# Android's device tree overlays from them to the mainline device tree and
# stay in fastboot. The bootloader names the SoC, not the phone (product
# sdm845, as on the OnePlus 6), so anything else is refused and the rest
# asked.
set -eu

LUNS="sda sdb sdc sdd sde sdf"
BOARD=oneplus-fajita
# As tools/sku.py's NEVER (tools/sku_test.py compares).
NEVER="persist persistbak modemst1 modemst2 fsg fsc oem_dycnvbk oem_stanvbk devinfo cdt ddr \
xbl xbl_config abl tz hyp aop devcfg keymaster cmnlib cmnlib64 qupfw storsec modem dsp bluetooth \
prodnv miscdata l_fixnv1 l_fixnv2 l_runtimenv1 l_runtimenv2 l_modem l_ldsp l_gdsp pm_sys \
uboot sml trustos teecfg wcnmodem \
nvram nvdata nvcfg protect1 protect2 proinfo seccfg preloader lk md1img"
here=$(dirname "$0")

dry=0 yes=0 serial='' dir='' backup='' stock='' layout='' chunk=64M
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
	-L | --layout) [ $# -ge 2 ] || usage; layout=$2; shift ;;
	-d | --dir) [ $# -ge 2 ] || usage; dir=$2; shift ;;
	-b | --backup) [ $# -ge 2 ] || usage; backup=$2; shift ;;
	--stock) [ $# -ge 2 ] || usage; stock=$2; shift ;;
	--chunk) [ $# -ge 2 ] || usage; chunk=$2; shift ;;
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
case $layout in
'' | userdata | ab) ;;
*) die "--layout is userdata or ab, not '$layout'" ;;
esac
case $chunk in
'' | [!1-9]* | *[!0-9KMG]* | *[KMG]?*) die "--chunk is a size such as 64M (bytes, or K, M or G), not '$chunk'" ;;
esac

dir=${dir:-${WORK:-$HOME/yocto/fajita-work}/build/tmp/deploy/images/oneplus-fajita}

# image <name>: the build's file a partition table row writes.
image() {
	case $1 in
	boot) echo "$dir/boot-oneplus-fajita.img" ;;
	rootfs) echo "$dir/phone-image-oneplus-fajita.rootfs.ext4.sparse" ;;
	abboot) echo "$dir/phone-image-oneplus-fajita.rootfs.abboot" ;;
	slot) echo "$dir/phone-image-oneplus-fajita.rootfs.ext4.verity.sparse" ;;
	*) die "the partition table names an image '$1' this script does not know" ;;
	esac
}

# The board's partitions in a layout, a line each: name, role, action,
# image (tools/sku.py partitions; PHONE_PARTITIONS names a file with such
# a table instead).
load_table() {
	if [ -n "${PHONE_PARTITIONS:-}" ]; then
		TABLE=$(cat "$PHONE_PARTITIONS")
	else
		command -v python3 > /dev/null || die "no python3 (tools/sku.py reads the board's partitions)"
		TABLE=$(python3 "$here/../tools/sku.py" partitions "$BOARD" ${layout:+"$layout"}) ||
			die "tools/sku.py gives no partitions of $BOARD${layout:+ for the $layout layout}"
	fi
	have=$(printf '%s\n' "$TABLE" | sed -n 's/^# .*: layout \([a-z]*\),.*/\1/p')
	[ -z "$layout" ] || [ "$have" = "$layout" ] || die "the partition table is the ${have:-unnamed} layout's, not $layout's"
	layout=$have
	case $layout in
	userdata | ab) ;;
	*) die "the partition table names no layout this script knows ('$layout')" ;;
	esac
	EFS=$(rows efs)
	WRITES=$(rows write)
	ERASES=$(rows erase)
	ALLOWED="$WRITES $ERASES"
	PARTS="$EFS $(rows write boot) $(rows erase dtbo)"
	for p in $ALLOWED; do
		if never "$p"; then
			die "refusing: the partition table lets a flash change $p, which holds what no flash may write (a phone's own data, the bootloader's or the firmware)"
		fi
	done
	[ -n "$(rows write boot)" ] || die "the partition table has no boot partition to write"
	[ -n "$EFS" ] || die "the partition table names none of the phone's own partitions (role efs): nothing to back up"
}

# rows <action> [role]: the names of the table's partitions with that
# action (and role).
rows() {
	printf '%s\n' "$TABLE" | awk -v a="$1" -v r="${2:-}" '!/^#/ && NF == 4 && $3 == a && (r == "" || $2 == r) { printf "%s%s", s, $1; s = " " }'
}

# image_of <name>: the file the table writes into a partition.
image_of() {
	image "$(printf '%s\n' "$TABLE" | awk -v n="$1" '!/^#/ && $1 == n { print $4 }')"
}

# never <name>: whether it is one of NEVER, with or without a slot suffix.
never() {
	n=$(printf '%s' "$1" | tr '[:upper:]' '[:lower:]')
	case $n in
	*_a | *_b) b=${n%_?} ;;
	*) b=$n ;;
	esac
	case " $NEVER " in
	*" $n "* | *" $b "*) return 0 ;;
	esac
	return 1
}

# guard <flash|erase> <partition>: refuses what this layout does not write.
guard() {
	if never "$2"; then
		die "refusing to $1 $2: it holds what no flash may write (a phone's own data, the bootloader's or the firmware)"
	fi
	case " $ALLOWED " in
	*" $2 "*) ;;
	*) die "refusing to $1 $2: the board's partition table does not let the $layout layout change it" ;;
	esac
}

fb() {
	case $1 in
	flash | erase) guard "$1" "$2" ;;
	-S) guard "$3" "$4" ;;
	-w) [ "$cmd" = restore ] || die "refusing fastboot -w outside restore" ;;
	esac
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

sparse() {
	[ "$(head -c 4 "$1" | od -An -tx1 | tr -d ' \n')" = 3aff26ed ]
}

# size <file>: the bytes an image writes: an Android sparse image's
# unpacked size (block size times blocks, at bytes 12 and 16).
size() {
	if sparse "$1"; then
		f=$1
		# shellcheck disable=SC2046
		set -- $(od -An -tu4 -j12 -N8 "$1")
		[ $# = 2 ] || die "$f is a truncated sparse image"
		echo $(($1 * $2))
	else
		wc -c < "$1" | tr -d ' '
	fi
}

# part_size <name>: the partition's bytes as the bootloader reports them
# (fastboot getvar partition-size:<name>, in hex), or nothing.
part_size() {
	v=$(getvar "partition-size:$1")
	case $v in
	0x* | 0X*) printf '%d\n' "$v" ;;
	esac
}

# fits <partition> <file>: the phone has the partition and the image fits
# it; in a dry run what would be checked.
fits() {
	want=$(size "$2")
	if [ "$dry" = 1 ]; then
		echo "# write $1: ${2##*/}, $want bytes (partition-size:$1 must be at least that)"
		return 0
	fi
	got=$(part_size "$1")
	[ -n "$got" ] || die "the phone reports no partition $1 (fastboot getvar partition-size:$1): nothing written"
	[ "$got" -ge "$want" ] || die "$1 is $got bytes, ${2##*/} needs $want: nothing written"
	echo "fajita-flash: $1: $got bytes, ${2##*/} $want"
}

# exists <partition>: the phone has it (what is erased).
exists() {
	if [ "$dry" = 1 ]; then
		echo "# erase $1"
		return 0
	fi
	[ -n "$(part_size "$1")" ] || die "the phone reports no partition $1 (fastboot getvar partition-size:$1): nothing written"
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

# u32 <file> <offset>: the little-endian 32-bit number there.
u32() {
	od -An -tu4 -j"$2" -N4 "$1" | tr -d ' '
}

# ramdisk_of <boot image>: where its ramdisk starts and how big it is
# (header version 0: its size at byte 16, the page size at 36; it starts
# on the page after the kernel's): rd_at, rd_size.
ramdisk_of() {
	page=$(u32 "$1" 36)
	[ "${page:-0}" -gt 0 ] || die "$1 has no page size in its header"
	rd_at=$((page + ($(u32 "$1" 8) + page - 1) / page * page))
	rd_size=$(u32 "$1" 16)
}

# initramfs <boot image>: whether its ramdisk has a gzip-compressed
# initramfs behind the small uncompressed archive that phone-bootimg and
# phone-abboot put first (cpio pads it to 512 bytes); rd_head is that
# archive's size.
initramfs() {
	ramdisk_of "$1"
	rd_head=512
	while [ "$rd_head" -lt "$rd_size" ] && [ "$rd_head" -le 65536 ]; do
		[ "$(od -An -tx1 -j$((rd_at + rd_head)) -N3 "$1" | tr -d ' \n')" = 1f8b08 ] && return 0
		rd_head=$((rd_head + 512))
	done
	return 1
}

# ramdisk_head <boot image>: the small archive before the initramfs.
ramdisk_head() {
	tail -c +$((rd_at + 1)) "$1" | head -c "$rd_head"
}

# The images in -d are Kario's for the 6T and the layout: the build appends
# the machine's device tree to the kernel (phone-bootimg, phone-abboot),
# whose compatible names the phone (the 6's says oneplus,enchilada), and
# puts phone.gui= in the command line (Nura's says pmos_), and the A/B
# layout's phone.layout=ab; the ramdisk is phone-initramfs behind an
# archive naming the userdata layout (/etc/phone/layout) or holding the
# A/B layout's root descriptor; what goes to the other partitions is an
# Android sparse image.
board_images() {
	head -c 4096 "$bimg" | grep -a -q 'phone\.gui=' ||
		die "$bimg is not Kario's boot image (no phone.gui= in its command line): -d names another system's images"
	if ! grep -a -q -F 'oneplus,fajita' "$bimg"; then
		found=$(grep -a -o -E 'oneplus,[a-z0-9]+' "$bimg" | head -n 1)
		die "$bimg is not the OnePlus 6T's: its device tree says ${found:-no oneplus,...}, not oneplus,fajita (-d names another board's build)"
	fi
	if head -c 4096 "$bimg" | grep -a -q 'phone\.layout=ab'; then
		[ "$layout" = ab ] || die "$bimg is the A/B layout's boot image: --layout ab"
	elif [ "$layout" = ab ]; then
		die "$bimg is not the A/B layout's boot image (no phone.layout=ab in its command line): built with phone-fajita-ab.yml?"
	fi
	initramfs "$bimg" ||
		die "$bimg has no initramfs (its ramdisk is $rd_size bytes): its kernel would mount the root= the bootloader appends, Android's system partition (39-oneplus-6t.md §1); built from a tree before 2026-11-24?"
	if [ "$layout" = ab ]; then
		ramdisk_head "$bimg" | grep -a -q -F 'etc/phone/rootfs.verity' ||
			die "$bimg's ramdisk holds no verity descriptor of the root (etc/phone/rootfs.verity): -d names another build's images"
	else
		ramdisk_head "$bimg" | grep -a -q -F 'layout=userdata' ||
			die "$bimg's ramdisk does not name the userdata layout (etc/phone/layout): -d names another build's images"
	fi
	[ "$cmd" = flash ] || return 0
	for p in $WRITES; do
		f=$(image_of "$p")
		[ "$f" = "$bimg" ] || sparse "$f" || die "$f is not an Android sparse image (phone-sparse makes it)"
	done
}

# Whether $backup is this phone's, holds its own partitions (EFS: the IMEI,
# the calibration), and is unchanged since it was taken. boot and dtbo may
# be missing: a phone may have lost them before it was backed up (Nura's
# boot in boot_a, dtbo erased, 39-oneplus-6t.md §9.1), and restore can take
# OxygenOS's from its package instead (--stock).
backup_ok() {
	[ -f "$backup/phone.txt" ] && [ -f "$backup/SHA256SUMS" ] || return 1
	grep -qx "serial=$serial" "$backup/phone.txt" || return 1
	for p in $EFS; do
		in_backup "$p" || return 1
	done
	(cd "$backup" && sha256sum -c --quiet SHA256SUMS) >/dev/null 2>&1
}

in_backup() {
	grep -q "  $1\.img\$" "$backup/SHA256SUMS"
}

# stock_image <boot|dtbo> <file>: what restore may write back: an Android
# boot image that is neither Kario's nor Nura's, or Android's table of
# device tree overlays (magic d7b7ab1e). A copy of a partition the phone
# had already lost (erased, or Nura's boot) is neither.
stock_image() {
	[ -s "$2" ] || return 1
	case $1 in
	boot) [ "$(head -c 8 "$2")" = "ANDROID!" ] && ! foreign_boot "$2" ;;
	dtbo) [ "$(head -c 4 "$2" | od -An -tx1 | tr -d ' \n')" = d7b7ab1e ] ;;
	esac
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
	for p in $PARTS; do
		in_backup "$p" ||
			echo "fajita-flash: the backup has no $p.img (the phone had lost it before, 39-oneplus-6t.md §9.1): restore will want --stock" >&2
	done
}

do_restore() {
	fastboot_phone
	# The layout the last flash noted beside the backup (kario.txt).
	if [ -z "$layout" ] && [ -f "$backup/kario.txt" ]; then
		layout=$(sed -n 's/^layout=userdata$/userdata/p; s/^layout=ab$/ab/p' "$backup/kario.txt")
	fi
	load_table
	need_backup
	roots=$(rows write root)
	if [ -n "$stock" ]; then
		rboot=$stock/boot.img rdtbo=$stock/dtbo.img rsystem=$stock/system.img
		if [ ! -f "$rboot" ] || [ ! -f "$rdtbo" ]; then
			die "--stock $stock wants boot.img and dtbo.img"
		fi
		stock_image boot "$rboot" || die "$rboot is not OxygenOS's boot image"
		stock_image dtbo "$rdtbo" || die "$rdtbo is not a device tree overlay image (dtbo.img from OxygenOS's payload.bin)"
		if [ -n "$roots" ] && [ ! -f "$rsystem" ]; then
			die "--stock $stock wants system.img too: the A/B layout put Kario into $roots"
		fi
	else
		[ -z "$roots" ] || die "the A/B layout put Kario into $roots, which the backup does not hold: --stock DIR with OxygenOS's boot.img, dtbo.img and system.img (its package's payload.bin)"
		grep -qx "stock-boot=yes" "$backup/phone.txt" || die "the backup's boot is not OxygenOS's: --stock DIR with OxygenOS's boot.img and dtbo.img"
		# Every image checked before the first is written.
		for s in a b; do
			stock_image boot "$backup/boot_$s.img" ||
				die "the backup has no OxygenOS boot_$s.img (the phone had lost it before?): --stock DIR with OxygenOS's boot.img and dtbo.img; nothing written"
			stock_image dtbo "$backup/dtbo_$s.img" ||
				die "the backup's dtbo_$s.img is missing or not a device tree overlay image (erased before the backup?): --stock DIR with OxygenOS's boot.img and dtbo.img; nothing written"
		done
	fi
	for p in $roots; do
		fits "$p" "$rsystem"
	done
	confirm "write OxygenOS's boot and dtbo${roots:+ and system} to both slots and wipe userdata (everything on it is lost)"
	for s in a b; do
		if [ -z "$stock" ]; then
			rboot=$backup/boot_$s.img rdtbo=$backup/dtbo_$s.img
		fi
		fb -S "$chunk" flash "boot_$s" "$rboot"
		fb -S "$chunk" flash "dtbo_$s" "$rdtbo"
	done
	for p in $roots; do
		fb -S "$chunk" flash "$p" "$rsystem"
	done
	# As flash does: Kario's failed boots may have left the slot unbootable.
	fb --set-active="${slot:-a}"
	fb -w
	rm -f "$backup/kario.txt"
	fb reboot
	echo "fajita-flash: OxygenOS should start (its first start after the wipe takes a few minutes)"
}

do_restore_efs() {
	fastboot_phone
	need_backup
	confirm "write the backup's $EFS back (only after they were lost: no IMEI, no fingerprint)"
	twrp_up
	for p in $EFS; do
		never "$p" || die "refusing to write $p back: restore-efs writes only the phone's own partitions"
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
	load_table
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
	load_table
	do_restore_efs
	exit 0
	;;
esac

load_table
bimg=$(image_of "$(rows write boot | cut -d ' ' -f 1)")
need "$bimg"
if [ "$cmd" = flash ]; then
	for p in $WRITES; do
		need "$(image_of "$p")"
	done
fi
[ "$(head -c 8 "$bimg")" = "ANDROID!" ] || die "$bimg is not an Android boot image"
board_images

if [ "$dry" = 1 ]; then
	slot=a
	echo "# dry run of $cmd, the $layout layout: the checks would read product, unlocked, serialno and current-slot (a here), the phone's backup, and the size of each partition below"
else
	fastboot_phone
	need_backup
	if [ "$cmd" = flash ]; then
		confirm "erase $ERASES, write $WRITES (everything on userdata is lost), make slot ${slot:-a} active"
	else
		confirm "erase $(rows erase dtbo), make slot ${slot:-a} active, then boot Kario once"
	fi
fi

# Every partition checked before the first is written.
for p in $(rows erase dtbo); do
	exists "$p"
done
if [ "$cmd" = flash ]; then
	for p in $WRITES; do
		fits "$p" "$(image_of "$p")"
	done
	for p in $ERASES; do
		case " $(rows erase dtbo) " in
		*" $p "*) ;;
		*) exists "$p" ;;
		esac
	done
	if [ "$dry" = 1 ]; then
		echo "# kept, never written: $(rows keep)"
		echo "# the phone's own, never written (in the backup): $EFS"
	fi
fi

for p in $(rows erase dtbo); do
	fb erase "$p"
done
# set_active, in both layouts and before a test boot too: it clears what
# ABL counted against the slot (on a 6T, once failed boots had spent a
# slot's tries, ABL marked it unbootable and refused even fastboot boot
# with "Load Error", 39-oneplus-6t.md §8.2), and the slot counts its tries
# again until its system marks it successful (phone-qcom-abslot,
# phone-ota-good; Android its own). The slot it boots now, so the
# firmware's slot stays.
case $cmd in
test)
	fb --set-active="${slot:-a}"
	fb boot "$bimg"
	;;
flash)
	for role in boot root data state; do
		for p in $(rows write "$role"); do
			fb -S "$chunk" flash "$p" "$(image_of "$p")"
		done
		for p in $(rows erase "$role"); do
			fb erase "$p"
		done
	done
	fb --set-active="${slot:-a}"
	if [ "$dry" = 0 ]; then
		printf 'layout=%s\nflashed=%s\n' "$layout" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" > "$backup/kario.txt"
	fi
	fb reboot
	;;
esac
# test brings Kario up only where flash wrote its userdata before.
echo "fajita-flash: once Kario is up (ssh root@172.16.42.1), test it as it is:" \
	"tools/phone-chroot/kario-phone.sh --whole --sku oneplus-fajita-t9 push setup selftest pixel collect (39-oneplus-6t.md §13.5)"
