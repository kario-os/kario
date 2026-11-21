#!/bin/sh
# Runs our image's root file system on a phone with Nura, in a chroot on its
# kernel, panel, modem and sound (24-sku-framework.md §6.2), driven from the
# build machine over SSH. Nothing of this has run on a phone yet.
#
#   tools/phone-chroot/kario-phone.sh --sku <sku> [options] <step>...
#
# Steps, in the order a first run takes them:
#   push              the SKU's files and the root file system image to the phone
#   setup             mount it, bind /dev /proc /sys /run, stop what competes,
#                     note what the phone has (facts)
#   selftest          the compatibility run (selftest and CTS), text only
#   pixel             the selftest drawn by phone-paint (headless): the SKU's
#                     panel and the three form factors
#   bench             the semantic-vs-web benchmark (an image with python3: the web image)
#   collect           fetch every run's results into --out
#   all               push setup selftest pixel bench collect
#   shell-start, shell-stop   the Shell on the panel; keys, calls, SMS by hand
#   mem               the memory (PSS) of each of our processes, 60 s after shell-start
#   power-start, power-stop   the battery around a standby period
#   power-sample[=S]  the current every second for S seconds (60): the screen-on draw
#   check, facts      the board check, the facts again
#   teardown          stop ours, unmount, start what setup stopped: Nura as before
#
# Options:
#   --sku SKU          a whole phone's SKU (oneplus-fajita-t9, oneplus-enchilada-t9, xiaomi-mido-t9)
#   --phone DEST       the SSH destination (root@172.16.42.1, Nura's USB network)
#   --ssh-key FILE     its key (~/.ssh/kario-board-test when there)
#   --become CMD       doas or sudo for the phone's side, when DEST is not root
#   --image FILE       the root file system, .ext4 or .ext4.zst (the newest
#                      phone-image of --machine under $WORK)
#   --machine M        qemuarm64 (default) or kario-qcom: the binaries are the same
#   --out DIR          where collect puts the results (build/phone/<sku>)
#   --timeout-scale N  the selftest's timeouts times N (2)
#   --wait S           how long a step may run on the phone (10800)
#   --force            push the image again; go on with another board, an old image,
#                      or a phone without a backup in ~/kario-phone-backup (39-oneplus-6t.md §3.5)
#   --purge            with teardown: delete the image, state and results on the phone
#   --dry-run          print what would run here and on the phone; change
#                      nothing, connect nowhere
set -u

here=$(cd "$(dirname "$0")" && pwd)
FW=$(cd "$here/../.." && pwd)
WORK=${WORK:-$HOME/yocto/kario-work}
BASE=/var/lib/kario-chroot
SKU=
PHONE=root@172.16.42.1
KEY=
BECOME=
IMAGE=
MACHINE=qemuarm64
OUT=
SCALE=2
WAIT=10800
FORCE=0
PURGE=0
DRY=0

say() { echo "kario-phone: $*"; }
die() {
	echo "kario-phone: $*" >&2
	exit 1
}
usage() {
	sed -n '2,/^set -u/p' "$0" | sed '$d; s/^# \{0,1\}//' >&2
	exit 2
}

while [ $# -gt 0 ]; do
	case $1 in
	--sku | --phone | --ssh-key | --become | --image | --machine | --out | --timeout-scale | --wait)
		[ $# -ge 2 ] || usage
		case $1 in
		--sku) SKU=$2 ;;
		--phone) PHONE=$2 ;;
		--ssh-key) KEY=$2 ;;
		--become) BECOME=$2 ;;
		--image) IMAGE=$2 ;;
		--machine) MACHINE=$2 ;;
		--out) OUT=$2 ;;
		--timeout-scale) SCALE=$2 ;;
		--wait) WAIT=$2 ;;
		esac
		shift
		;;
	--force) FORCE=1 ;;
	--purge) PURGE=1 ;;
	--dry-run) DRY=1 ;;
	-h | --help) usage ;;
	-*) usage ;;
	*) break ;;
	esac
	shift
done
[ -n "$SKU" ] || usage
[ $# -ge 1 ] || usage
OUT=${OUT:-$FW/build/phone/$SKU}
if [ -z "$KEY" ] && [ -f "$HOME/.ssh/kario-board-test" ]; then
	KEY=$HOME/.ssh/kario-board-test
fi

# The kit: the phone's script and the SKU's files (sku.json, the product's
# layer, phone.env); a dry run makes them in a directory it removes.
if [ "$DRY" = 1 ]; then
	STAGE=$(mktemp -d)
	trap 'rm -rf "$STAGE"' EXIT
else
	STAGE=$OUT/kit
	mkdir -p "$STAGE" || die "cannot make $STAGE"
fi
python3 "$FW/tools/sku.py" files "$SKU" "$STAGE" > /dev/null || die "no SKU $SKU"
[ -f "$STAGE/phone.env" ] || die "$SKU is not a whole phone's SKU (its board has no class = \"phone\")"
cp "$here/on-phone.sh" "$STAGE/on-phone.sh"

# Phones are flashed again and share Nura's USB address: their host keys
# are kept apart from the user's.
SSH_OPTS="-o BatchMode=yes -o ConnectTimeout=10 -o ServerAliveInterval=15 -o ServerAliveCountMax=4"
SSH_OPTS="$SSH_OPTS -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile=$HOME/.ssh/known_hosts.kario-phone"

# phone CMDLINE: a command line on the phone; stdin and stdout pass through.
phone() {
	if [ "$DRY" = 1 ]; then
		echo "+ ssh $PHONE '$*'"
		return 0
	fi
	# The options are words; the command line is the phone's to expand.
	if [ -n "$KEY" ]; then
		# shellcheck disable=SC2086,SC2029
		ssh $SSH_OPTS -i "$KEY" "$PHONE" "$*"
	else
		# shellcheck disable=SC2086,SC2029
		ssh $SSH_OPTS "$PHONE" "$*"
	fi
}

# onphone STEP [ARGS]: the phone's script. A dry run shows its plan.
onphone() {
	line="$BECOME env KARIO_TIMEOUT_SCALE=$SCALE KARIO_FORCE=$FORCE sh $BASE/kit/on-phone.sh $*"
	if [ "$DRY" = 1 ]; then
		echo "+ ssh $PHONE '$line'"
		KARIO_BASE=$BASE KARIO_TIMEOUT_SCALE=$SCALE KARIO_FORCE=$FORCE sh "$STAGE/on-phone.sh" --plan --env "$STAGE/phone.env" "$@" | sed 's/^/    /'
		return 0
	fi
	phone "$line"
}

find_image() {
	if [ -n "$IMAGE" ]; then
		[ -f "$IMAGE" ] || return 1
		echo "$IMAGE"
		return 0
	fi
	d=$WORK/build/tmp/deploy/images/$MACHINE
	for f in "$d/phone-image-$MACHINE.rootfs.ext4" "$d/phone-image-$MACHINE.rootfs.ext4.zst"; do
		if [ -e "$f" ]; then
			echo "$f"
			return 0
		fi
	done
	# Without the link, the newest build: the names sort by their time.
	last=
	for f in "$d/phone-image-$MACHINE".rootfs-*.ext4; do
		if [ -f "$f" ]; then last=$f; fi
	done
	[ -n "$last" ] || return 1
	echo "$last"
}

# The partitions only this phone has (its IMEI, its calibration) are on the
# build machine before anything of ours runs on it (39-oneplus-6t.md §3.5):
# the bootloader hands the kernel the serial the backup is named after.
check_backup() {
	board=$(sed -n 's/^KARIO_BOARD=//p' "$STAGE/phone.env")
	sn=$(phone "tr ' ' '\n' < /proc/cmdline | sed -n 's/^androidboot.serialno=//p'") || die "cannot reach $PHONE"
	dir=$HOME/kario-phone-backup/$board-$sn
	if [ -n "$sn" ] && [ -f "$dir/SHA256SUMS" ] && (cd "$dir" && sha256sum -c --quiet SHA256SUMS > /dev/null 2>&1); then
		say "backup of phone $sn in $dir checks out"
		return 0
	fi
	[ "$FORCE" = 1 ] || die "no backup of phone ${sn:-(no serial)} that checks out in $dir: make it first (yocto/fajita-flash.sh backup TWRP.img, 39-oneplus-6t.md §3.5), or --force"
	say "no backup of phone ${sn:-(no serial)} in $dir: going on (--force)"
}

do_push() {
	if ! img=$(find_image); then
		[ "$DRY" = 1 ] || die "no image: build one (make yocto-build) or give --image"
		img="<no image yet: make yocto-build, or --image>"
	fi
	if [ "$DRY" = 1 ]; then
		echo "+ ssh $PHONE 'androidboot.serialno from /proc/cmdline': its backup must be in ~/kario-phone-backup/<board>-<serial> (or --force)"
		case $img in
		*.zst) unpack="zstd -dc $img" ;;
		*) unpack="cat $img" ;;
		esac
		echo "+ tar -C <kit: on-phone.sh sku.json profile.json phone.env> -cf - . | ssh $PHONE '$BECOME sh -c \"mkdir -p $BASE/kit && tar -xf - -C $BASE/kit\"'"
		echo "+ (unless the phone has this image already, or it is mounted) $unpack | ssh $PHONE '$BECOME sh -c \"cat > $BASE/rootfs.ext4.part && mv ... $BASE/rootfs.ext4\"'"
		return 0
	fi
	check_backup
	real=$(readlink -f "$img")
	stamp="$(basename "$real"):$(wc -c < "$real" | tr -d ' ')"
	tar -C "$STAGE" -cf - . | phone "$BECOME sh -c 'mkdir -p $BASE/kit && tar -xf - -C $BASE/kit'" || die "cannot reach $PHONE"
	if [ "$(phone "cat $BASE/rootfs.stamp 2>/dev/null")" = "$stamp" ] && [ "$FORCE" = 0 ]; then
		say "the phone has $(basename "$real") already"
		return 0
	fi
	if phone "grep -q ' $BASE/root ' /proc/mounts"; then
		die "the phone's image is in use: teardown first"
	fi
	free=$(phone "df -k /var/lib | awk 'NR == 2 { print \$4 }'")
	[ "${free:-0}" -gt 2000000 ] || die "the phone has ${free:-?} KB free in /var/lib, the image needs some 2 GB"
	say "copying $(basename "$real") to $PHONE"
	case $real in
	*.zst) zstd -dc "$real" ;;
	*) cat "$real" ;;
	esac | phone "$BECOME sh -c 'cat > $BASE/rootfs.ext4.part && mv $BASE/rootfs.ext4.part $BASE/rootfs.ext4 && echo $stamp > $BASE/rootfs.stamp'" ||
		die "the copy failed"
}

do_collect() {
	if [ "$DRY" = 1 ]; then
		echo "+ ssh $PHONE '$BECOME tar -C $BASE/results -cf - .' | tar -xf - -C $OUT/results"
		return 0
	fi
	mkdir -p "$OUT/results"
	phone "$BECOME tar -C $BASE/results -cf - ." | tar -xf - -C "$OUT/results" || die "collect failed"
	say "results in $OUT/results"
}

# long STEP: the step detached on the phone, polled until it ends; the link
# may drop meanwhile (the selftest's suspend case suspends a phone that can).
long() {
	onphone bg "$1" || return 1
	if [ "$DRY" = 1 ]; then
		echo "+ (every 20 s: on-phone.sh status $1, until done, at most $WAIT s; then on-phone.sh log $1); the step runs:"
		onphone "$1"
		return 0
	fi
	t0=$(date +%s)
	fails=0
	while :; do
		sleep 20
		if ! st=$(onphone status "$1" 2> /dev/null); then
			fails=$((fails + 1))
			[ "$fails" -lt 45 ] || die "lost $PHONE for 15 minutes during $1"
			continue
		fi
		fails=0
		case $st in
		"done "*)
			onphone log "$1"
			say "$1: exit ${st#done }"
			return "${st#done }"
			;;
		running)
			[ $(($(date +%s) - t0)) -lt "$WAIT" ] || die "$1 runs longer than $WAIT s"
			;;
		*) die "$1 ended without its exit code ($st); see on-phone.sh log $1" ;;
		esac
	done
}

worst=0
step() {
	case $1 in
	push) do_push ;;
	collect) do_collect ;;
	selftest | pixel | bench) long "$1" ;;
	teardown)
		if [ "$PURGE" = 1 ]; then onphone teardown --purge; else onphone teardown; fi
		;;
	check | facts | setup | shell-start | shell-stop | mem | power-start | power-stop | power-sample) onphone "$1" ;;
	power-sample=*) onphone power-sample "${1#power-sample=}" ;;
	*) usage ;;
	esac
}

for s in "$@"; do
	if [ "$s" = all ]; then
		step push || exit 1
		step setup || exit 1
		for t in selftest pixel bench; do
			step "$t" || worst=1
		done
		step collect || worst=1
	else
		step "$s" || worst=1
	fi
done
exit $worst
