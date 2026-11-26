#!/bin/sh
# Tests a whole phone's SKU on the phone, driven from the build machine over
# SSH, two ways with the same steps and the same results
# (24-sku-framework.md §6.2, 39-oneplus-6t.md §13):
#   the chroot (the default): the phone runs Nura, and our image's root file
#     system runs in a chroot on its kernel, panel, modem and sound;
#   --whole: the phone runs Kario itself, flashed (yocto/fajita-flash.sh
#     flash), its own kernel and image.
# The chroot first ran on a phone (a 6T on Nura v26.06) on 2026-11-21:
# 41-design-gaps.md §11.
#
#   tools/phone-chroot/kario-phone.sh --sku <sku> [--whole] [options] <step>...
#
# Steps, in the order a first run takes them:
#   push              the SKU's files (in the chroot also the root file system
#                     image) to the phone, with kit.txt: this tree's commit
#                     and the image, for the results
#   setup             set the phone's clock from this machine's when it is far
#                     behind, note what the phone has (facts); in the chroot
#                     also mount the image, bind /dev /proc /sys /run, stop
#                     what competes
#   selftest          the compatibility run (selftest and CTS), text only; a
#                     failed case does not end it
#   pixel             the selftest drawn by phone-paint (headless): the SKU's
#                     panel and the three form factors
#   bench             the semantic-vs-web benchmark (an image with python3: the web image)
#   perf              the gate's run (39-oneplus-6t.md §8.1): the Shell on the
#                     panel with a fixed sequence of keys and touches
#                     (tools/perf/gate.play) for each GPU path at the panel's
#                     size and the product's; one table against the gate
#   collect           fetch every run's results into --out
#   all               push setup selftest pixel bench collect
#   shell-start, shell-stop   (chroot) the Shell on the panel; keys, calls, SMS by hand
#   mem               the memory (PSS) of each of our processes (60 s after shell-start)
#   power-start, power-stop   the battery around a standby period
#   power-sample[=S]  the current every second for S seconds (60): the screen-on draw
#   check, facts      the board check, the facts again
#   teardown          stop ours, start what setup or a step stopped; in the
#                     chroot also unmount: Nura as before
#
# Options:
#   --sku SKU          a whole phone's SKU (oneplus-fajita-t9, oneplus-enchilada-t9, xiaomi-mido-t9)
#   --whole            the phone flashed with Kario, not the chroot on Nura
#   --phone DEST       the SSH destination (root@172.16.42.1: Nura's USB network,
#                      or on the whole phone phone-bringup's)
#   --ssh-key FILE     its key (~/.ssh/kario-board-test when there)
#   --become CMD       doas or sudo for the phone's side, when DEST is not root
#   --image FILE       (chroot) the root file system: an ext4 (.ext4), its zstd
#                      (.ext4.zst) or its Android sparse form (.ext4.sparse,
#                      what the flash writes), unpacked on the way; default the
#                      newest phone-image of --machine under $WORK
#   --machine M        (chroot) the image's machine: the board's own (the
#                      default; its GPU driver, sound card profile and services,
#                      as flashed), or a generic one (qemuarm64: the same
#                      binaries without them)
#   --firmware image   (chroot) Nura's kernel loads what it loads from then on
#                      (the GPU's firmware) from the image's /lib/firmware first
#   --only REGEXP      selftest and pixel run only the cases whose names match
#   --perf RUNS        perf makes only these runs (comma-separated, of cpu-full
#                      cpu-target compose-full compose-target raster-full
#                      raster-target: all six by default)
#   --quiet LEVEL      selftest and pixel turn the speaker down to LEVEL (a
#                      percentage) for their run, not the board's quiet_level
#                      ([audio]); off leaves the mixer alone. Set back after
#                      the run and at teardown
#   --out DIR          where collect puts the results (build/phone/<sku>)
#   --timeout-scale N  the selftest's timeouts times N (2)
#   --wait S           how long a step may run on the phone (10800)
#   --force            push the image again; go on with another board, another
#                      board's image or SKU, an old image, or a phone without a
#                      backup in ~/kario-phone-backup (39-oneplus-6t.md §3.5)
#   --purge            with teardown: delete the kit, the image, state and results on the phone
#   --stay-on, --no-haptics   with shell-start: the Shell's bring-up switches,
#                      the screen never turned off (backlight and panel stay
#                      on), no vibration (39-oneplus-6t.md §13.4)
#   --dry-run          print what would run here and on the phone; change
#                      nothing, connect nowhere
set -u

here=$(cd "$(dirname "$0")" && pwd)
FW=$(cd "$here/../.." && pwd)
WORK=${WORK:-$HOME/yocto/kario-work}
TARGET=chroot
SKU=
PHONE=root@172.16.42.1
KEY=
BECOME=
IMAGE=
MACHINE=
OUT=
SCALE=2
WAIT=10800
FORCE=0
PURGE=0
DRY=0
SWITCHES=
ONLY=
PERF=
QUIET=
FIRMWARE=nura

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
	--sku | --phone | --ssh-key | --become | --image | --machine | --out | --timeout-scale | --wait | --only | --perf | --quiet | --firmware)
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
		--only) ONLY=$2 ;;
		--perf) PERF=$(printf '%s' "$2" | tr ',' ' ') ;;
		--quiet) QUIET=$2 ;;
		--firmware) FIRMWARE=$2 ;;
		esac
		shift
		;;
	--whole) TARGET=whole ;;
	--force) FORCE=1 ;;
	--purge) PURGE=1 ;;
	--stay-on | --no-haptics) SWITCHES="$SWITCHES $1" ;;
	--dry-run) DRY=1 ;;
	-h | --help) usage ;;
	-*) usage ;;
	*) break ;;
	esac
	shift
done
[ -n "$SKU" ] || usage
[ $# -ge 1 ] || usage
case $FIRMWARE in nura | image) ;; *) usage ;; esac
case $QUIET in "" | off | [0-9]% | [1-9][0-9]% | 100%) ;; *) usage ;; esac
OUT=${OUT:-$FW/build/phone/$SKU}
if [ -z "$KEY" ] && [ -f "$HOME/.ssh/kario-board-test" ]; then
	KEY=$HOME/.ssh/kario-board-test
fi
if [ "$TARGET" = whole ]; then
	BASE=/var/lib/kario-test
	[ -z "$IMAGE$MACHINE" ] || die "--whole tests what the phone was flashed with: --image and --machine are the chroot's (yocto/fajita-flash.sh flash writes an image)"
	[ "$FIRMWARE" = nura ] || die "--firmware is the chroot's: the whole phone loads its own"
else
	BASE=/var/lib/kario-chroot
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
cp "$FW/tools/perf/gate.play" "$STAGE/perf.play"

# board NAME: a value of the board's phone.env.
board() {
	# shellcheck source=/dev/null
	(. "$STAGE/phone.env" && eval "printf '%s' \"\${$1:-}\"")
}
MACHINE=${MACHINE:-$(board KARIO_MACHINE)}
MACHINE=${MACHINE:-qemuarm64}

# Phones are flashed again and share their USB address: their host keys
# are kept apart from the user's, Nura's from the flashed Kario's.
SSH_OPTS="-o BatchMode=yes -o ConnectTimeout=10 -o ServerAliveInterval=15 -o ServerAliveCountMax=4"
if [ "$TARGET" = whole ]; then KNOWN=$HOME/.ssh/known_hosts.kario-whole; else KNOWN=$HOME/.ssh/known_hosts.kario-phone; fi
SSH_OPTS="$SSH_OPTS -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile=$KNOWN"

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

# quote WORD: WORD as one word of the phone's shell.
quote() { printf "'%s'" "$(printf '%s' "$1" | sed "s/'/'\\\\''/g")"; }

# onphone STEP [ARGS]: the phone's script, with this machine's clock (setup
# sets the phone's from it) and the run's options. A dry run shows its plan.
onphone() {
	now=$(date +%s)
	vars="KARIO_TIMEOUT_SCALE=$SCALE KARIO_FORCE=$FORCE KARIO_HOST_TIME=$now"
	if [ "$TARGET" = whole ]; then vars="KARIO_TARGET=whole $vars"; fi
	if [ "$FIRMWARE" = image ]; then vars="$vars KARIO_FIRMWARE=image"; fi
	if [ -n "$ONLY" ]; then vars="$vars KARIO_ONLY=$(quote "$ONLY")"; fi
	if [ -n "$PERF" ]; then vars="$vars KARIO_PERF=$(quote "$PERF")"; fi
	if [ -n "$QUIET" ]; then vars="$vars KARIO_QUIET=$QUIET"; fi
	line="$BECOME env $vars sh $BASE/kit/on-phone.sh $*"
	if [ "$DRY" = 1 ]; then
		echo "+ ssh $PHONE '$line'"
		KARIO_TARGET=$TARGET KARIO_BASE=$BASE KARIO_TIMEOUT_SCALE=$SCALE KARIO_FORCE=$FORCE KARIO_HOST_TIME=$now \
			KARIO_FIRMWARE=$FIRMWARE KARIO_ONLY=$ONLY KARIO_PERF=$PERF KARIO_QUIET=$QUIET sh "$STAGE/on-phone.sh" --plan --env "$STAGE/phone.env" "$@" | sed 's/^/    /'
		return 0
	fi
	phone "$line"
}

# kit_note IMAGE: kit.txt, what is tested: this tree's commit and whether it
# has changes (a report from the phone names both), and the image.
kit_note() {
	{
		echo "kit: $(sh "$here/tree-note.sh")"
		echo "sku: $SKU"
		echo "image: $1"
		echo "pushed: $(date -u +%Y-%m-%dT%H:%M:%SZ)"
	} > "$STAGE/kit.txt"
}

# The phone's serial in what this prints: the last two characters, enough to
# tell two phones apart and nothing a report should not carry.
masked() {
	if [ -n "$1" ]; then echo "(serial ending $(printf '%s' "$1" | tail -c 2))"; else echo "(no serial)"; fi
}

find_image() {
	if [ -n "$IMAGE" ]; then
		[ -f "$IMAGE" ] || return 1
		echo "$IMAGE"
		return 0
	fi
	d=$WORK/build/tmp/deploy/images/$MACHINE
	for f in "$d/phone-image-$MACHINE.rootfs.ext4" "$d/phone-image-$MACHINE.rootfs.ext4.zst" "$d/phone-image-$MACHINE.rootfs.ext4.sparse"; do
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

# hex FILE OFFSET COUNT: COUNT bytes of FILE at OFFSET, in hex.
hex() { dd if="$1" bs=1 skip="$2" count="$3" 2> /dev/null | od -An -tx1 | tr -d ' \n'; }

# unpack KIND FILE: the root file system FILE holds, raw, on stdout.
unpack() {
	case $1 in
	zst) zstd -dc "$2" ;;
	sparse) python3 "$here/unsparse.py" "$2" ;;
	*) cat "$2" ;;
	esac
}

# check_image FILE: KIND is what it holds (ext4, zst, sparse), told by its
# bytes, not its name; anything that does not unpack to an ext4 is refused.
check_image() {
	case $(hex "$1" 0 4) in
	3aff26ed) KIND=sparse ;;
	28b52ffd) KIND=zst ;;
	*) KIND=ext4 ;;
	esac
	case $KIND in
	zst) command -v zstd > /dev/null || die "no zstd here to unpack $1" ;;
	sparse) python3 "$here/unsparse.py" --size "$1" > /dev/null || die "$1 is not an Android sparse image the kit reads (above)" ;;
	esac
	if [ "$(unpack "$KIND" "$1" 2> /dev/null | dd bs=1 skip=1080 count=2 2> /dev/null | od -An -tx1 | tr -d ' \n')" != 53ef ]; then
		case $1 in
		*.wic*) why="a .wic is a whole disk: give its system slot's .ext4" ;;
		*.tar*) why="the kit loop-mounts an ext4, it does not unpack a tar" ;;
		*) why="no ext4 superblock in it" ;;
		esac
		die "$1 is not a root file system the kit takes ($why): an ext4 (.ext4), its zstd (.ext4.zst) or its Android sparse form (.ext4.sparse, what the flash writes)"
	fi
	check_machine "$1"
}

# check_machine FILE: the image is the board's own (its name says
# phone-image-<machine>), or a generic machine's; another board's is refused.
check_machine() {
	name=$(basename "$1")
	m=$(basename "$(readlink -f "$1")" | sed -n 's/^phone-image-\(.*\)\.rootfs.*/\1/p')
	m=${m:-$(printf '%s' "$name" | sed -n 's/^phone-image-\(.*\)\.rootfs.*/\1/p')}
	own=$(board KARIO_MACHINE)
	generic=$(board KARIO_GENERIC_MACHINES)
	is_generic=no
	for g in $generic; do
		if [ "$g" = "$m" ]; then is_generic=yes; fi
	done
	if [ -z "$m" ]; then
		say "$name: its name does not say its machine (phone-image-<machine>.rootfs...); going on"
	elif [ "$m" = "$own" ]; then
		say "$name: the board's own image ($m), as the phone runs it flashed"
	elif [ "$is_generic" = yes ]; then
		has=
		if [ -n "$own" ]; then has=": $own's image has them"; fi
		say "$name: $m's image, the same binaries without the board's own packages (its GPU driver, sound card profile and services$has): the GPU path and the sound card's set-up are not the flashed phone's"
	elif [ "$FORCE" = 1 ]; then
		say "$name: $m's image, not the board's (${own:-it has no machine yet}); going on (--force)"
	else
		die "$name is $m's image: neither the board's (${own:-it has no machine yet}) nor a generic one ($generic); --force to go on"
	fi
}

# The partitions only this phone has (its IMEI, its calibration) are on the
# build machine before anything of ours runs on it (39-oneplus-6t.md §3.5):
# the bootloader hands the kernel the serial the backup is named after.
check_backup() {
	board_id=$(board KARIO_BOARD)
	sn=$(phone "tr ' ' '\n' < /proc/cmdline | sed -n 's/^androidboot.serialno=//p'") ||
		die "cannot reach $PHONE (flashed again, it has a new host key: ssh-keygen -R ${PHONE#*@} -f $KNOWN)"
	dir=$HOME/kario-phone-backup/$board_id-$sn
	shown="$HOME/kario-phone-backup/$board_id-<serial>"
	if [ -n "$sn" ] && [ -f "$dir/SHA256SUMS" ] && (cd "$dir" && sha256sum -c --quiet SHA256SUMS > /dev/null 2>&1); then
		say "backup of phone $(masked "$sn") in $shown checks out"
		return 0
	fi
	[ "$FORCE" = 1 ] || die "no backup of phone $(masked "$sn") that checks out in $shown: make it first (yocto/fajita-flash.sh backup TWRP.img, 39-oneplus-6t.md §3.5), or --force"
	say "no backup of phone $(masked "$sn") in $shown: going on (--force)"
}

# push_kit: the kit's files into the phone's kit directory.
push_kit() {
	if [ "$DRY" = 1 ]; then
		echo "+ tar -C <kit: on-phone.sh perf.play sku.json profile.json phone.env kit.txt> -cf - . | ssh $PHONE '$BECOME sh -c \"mkdir -p $BASE/kit && tar -xf - -C $BASE/kit\"'"
		return 0
	fi
	tar -C "$STAGE" -cf - . | phone "$BECOME sh -c 'mkdir -p $BASE/kit && tar -xf - -C $BASE/kit'" || die "cannot reach $PHONE"
}

do_push() {
	if [ "$DRY" = 1 ]; then
		echo "+ ssh $PHONE 'androidboot.serialno from /proc/cmdline': its backup must be in ~/kario-phone-backup/<board>-<serial> (or --force)"
	else
		check_backup
	fi
	if [ "$TARGET" = whole ]; then
		kit_note "the phone's own: what it was flashed with (on the phone: the commit it was built from)"
		[ "$DRY" = 0 ] || echo "+ kit.txt: this tree's commit and changes (tools/phone-chroot/tree-note.sh), the SKU"
		push_kit
		return 0
	fi
	if ! img=$(find_image); then
		[ "$DRY" = 1 ] || die "no image: build one (make yocto-build) or give --image; none of phone-image-$MACHINE.rootfs.ext4, .ext4.zst, .ext4.sparse in $WORK/build/tmp/deploy/images/$MACHINE (WORK= names another build)"
		img="<no image yet: make yocto-build, or --image>"
		KIND=ext4
	else
		check_image "$img"
	fi
	if [ "$DRY" = 1 ]; then
		case $KIND in
		zst) unpack="zstd -dc $img" ;;
		sparse) unpack="python3 $here/unsparse.py $img" ;;
		*) unpack="cat $img" ;;
		esac
		[ ! -f "$img" ] || kit_note "$(basename "$(readlink -f "$img")")"
		echo "+ kit.txt: this tree's commit and changes (tools/phone-chroot/tree-note.sh), the SKU, the image"
		push_kit
		echo "+ (unless the phone has this image already, or it is mounted) $unpack | ssh $PHONE '$BECOME sh -c \"cat > $BASE/rootfs.ext4.part\"'; when it unpacked whole, mv ... $BASE/rootfs.ext4"
		return 0
	fi
	real=$(readlink -f "$img")
	size=$(wc -c < "$real" | tr -d ' ')
	stamp="$(basename "$real"):$size"
	kit_note "$(basename "$real"), $size bytes"
	push_kit
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
	# The copy is kept only once the whole image came through: an unpacking
	# that stops half-way ends the phone's side as a whole copy would.
	st=$(mktemp)
	{
		unpack "$KIND" "$real"
		echo $? > "$st"
	} | phone "$BECOME sh -c 'cat > $BASE/rootfs.ext4.part'" || {
		rm -f "$st"
		die "the copy failed"
	}
	rc=$(cat "$st")
	rm -f "$st"
	if [ "$rc" != 0 ]; then
		phone "$BECOME rm -f $BASE/rootfs.ext4.part"
		die "unpacking $(basename "$real") failed (above): nothing of it is kept on the phone"
	fi
	phone "$BECOME sh -c 'mv $BASE/rootfs.ext4.part $BASE/rootfs.ext4 && echo $stamp > $BASE/rootfs.stamp'" || die "the copy failed"
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
		sleep "${KARIO_POLL:-20}"
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
		*)
			printf '%s\n' "$st" >&2
			onphone log "$1" >&2
			die "$1 ended without its exit code: above is what the phone showed and the end of the step's output (for the report)"
			;;
		esac
	done
}

worst=0
step() {
	case $1 in
	push) do_push ;;
	collect) do_collect ;;
	selftest | pixel | bench | perf) long "$1" ;;
	teardown)
		if [ "$PURGE" = 1 ]; then onphone teardown --purge; else onphone teardown; fi
		;;
	shell-start)
		# shellcheck disable=SC2086 # a word for each switch
		onphone shell-start $SWITCHES
		;;
	check | facts | setup | shell-stop | mem | power-start | power-stop | power-sample) onphone "$1" ;;
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
