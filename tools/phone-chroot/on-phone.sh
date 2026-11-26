#!/bin/sh
# Runs as root on a phone, for the two ways a phone is tested
# (24-sku-framework.md §6.2, 39-oneplus-6t.md §13): KARIO_TARGET=chroot (the
# default), our image's root file system in a chroot on Nura's kernel,
# panel, modem and sound; KARIO_TARGET=whole, the phone flashed with Kario,
# its own kernel and image. The steps, the results and the facts are the
# same for both. kario-phone.sh copies it to the phone with the SKU's files
# and calls it.
#
#   on-phone.sh [--dry-run | --plan] [--force] [--env FILE] <step> [args]
#
#   check              is this the SKU's board, can it run the step (reads only)
#   facts              what the phone has, for the board's unverified values
#   setup              set the clock, note the kit and the facts; in the chroot
#                      also mount the image, bind /dev /proc /sys /run, stop
#                      what competes, set the sound card up for media (UCM)
#                      when the image cannot
#   selftest           the compatibility run (selftest and CTS), text only
#   pixel              the selftest drawn by phone-paint, headless: the SKU's
#                      panel and the three form factors
#   bench              the semantic-vs-web benchmark (an image with python3)
#   perf               the gate's run: the Shell on the panel playing a fixed
#                      sequence (perf.play) for each GPU path at the panel's
#                      size and the product's; one table against the gate
#   shell-start [--stay-on] [--no-haptics]   (chroot) the Shell on the panel,
#                      keys from the keyboards, the modem through ModemManager,
#                      with the Shell's bring-up switches; shell-stop ends it
#   mem                the memory (PSS) of each of our processes
#   power-start, power-stop   the battery before and after a standby period
#   power-sample [s]   the current every second for s seconds (60)
#   bg <step>          run a step detached; status <step>: running, done <rc>,
#                      or gone and what the phone shows; log <step>: the end
#                      of its output
#   scrub              stdin to stdout without the phone's serial number, the
#                      modem's identities and hardware addresses (as facts
#                      writes them)
#   partitions         the phone's partitions with their sizes and the roles
#                      the board file gives them (reads only; facts has it too)
#   teardown [--purge] stop ours, take the sound card's set-up down, unmount,
#                      start what setup or a step stopped
#
# setup sets the phone's clock to KARIO_HOST_TIME (the build machine's, in
# seconds) when the phone's is 10 minutes or more behind. KARIO_ONLY is a
# regular expression of the cases selftest and pixel run (phone-shell's
# --only). KARIO_FIRMWARE=image has Nura's kernel load what it loads from now
# on from the image's /lib/firmware (chroot). KARIO_QUIET is the level
# selftest and pixel turn the speaker down to, or off (quiet).
#
# On the whole phone selftest, pixel and bench stop the Shell on the panel
# and the audio service for their run, as the boot selftest runs without
# them, and start them again after, once nothing they left runs (settle);
# they refuse a login over the developer link (the trusted cable), which
# the selftest's own Shell takes over.
#
# --dry-run prints what would change instead of changing it; --plan does so
# without looking at the machine (the build machine's dry run). KARIO_BASE
# and KARIO_STATE move the phone's directories, KARIO_ROOT the whole phone's
# system files the steps read, KARIO_QUIESCE the units they stop,
# KARIO_INIT names the init system, KARIO_WITHOUT the commands a plan's
# phone lacks (image:NAME, the image's; the tests'). It first ran on a
# phone (a 6T on Nura v26.06) on 2026-11-21: 41-design-gaps.md §11.
# The step_* functions are called by name ("$@"), which shellcheck cannot see.
# shellcheck disable=SC2317,SC2329
set -u

TARGET=${KARIO_TARGET:-chroot}
case $TARGET in
chroot)
	BASE=${KARIO_BASE:-/var/lib/kario-chroot}
	STATE=${KARIO_STATE:-/run/kario-chroot}
	ROOT=$BASE/root
	;;
whole)
	BASE=${KARIO_BASE:-/var/lib/kario-test}
	STATE=${KARIO_STATE:-/run/kario-test}
	# The image is the system itself: $ROOT/usr/... are its files.
	ROOT=${KARIO_ROOT:-}
	;;
*)
	echo "kario: KARIO_TARGET is chroot or whole, not $TARGET" >&2
	exit 2
	;;
esac
IMG=$BASE/rootfs.ext4
KIT=$BASE/kit
RESULTS=$BASE/results
CG=/sys/fs/cgroup/kario
SCALE=${KARIO_TIMEOUT_SCALE:-2}
FWPATH=/sys/module/firmware_class/parameters/path
# Nura's services that would compete for the panel, the sound card, the
# power key or the modem's messages (each stopped only if it runs), and the
# session daemons that do (killed; they come back with the next login).
# ModemManager, rmtfs, pd-mapper, tqftpserv, D-Bus, NetworkManager,
# bluetoothd and sshd keep running: our telephonyd is ModemManager's client.
STOP=${KARIO_STOP:-"tinydm greetd sddm lightdm gdm"}
KILL=${KARIO_KILL:-"pipewire wireplumber pipewire-pulse pulseaudio callaudiod feedbackd chatty gnome-calls mmsdtng"}
# The whole phone's units a selftest takes the place of, as the boot
# selftest (phone.selftest=1) runs without them: the Shell on the panel and
# the audio service, which holds the sound card the selftest's own plays to.
QUIESCE=${KARIO_QUIESCE-"phone-display.service phone-audiod.socket phone-audiod.service"}
# A string only a Shell with whole-phone support has (shell/usb.go): older
# ones take the USB controller from Nura's USB network and cut it off.
MARK="belongs to gadget"
# The address of the developer link on the phone (27-usb-typec.md §4).
DEVLINK=10.71.0.1

DRY=0
PLAN=0
ENV=$KIT/phone.env

say() { echo "kario: $*"; }
die() {
	echo "kario: $*" >&2
	exit 1
}

# run CMD...: a command that changes the phone.
run() {
	if [ "$DRY" = 1 ]; then
		echo "phone# $*"
		return 0
	fi
	"$@"
}

# put FILE / append FILE: stdin into a file of the phone.
put() {
	if [ "$DRY" = 1 ]; then
		cat > /dev/null
		echo "phone# > $1"
		return 0
	fi
	cat > "$1"
}
append() {
	if [ "$DRY" = 1 ]; then
		cat > /dev/null
		echo "phone# >> $1"
		return 0
	fi
	cat >> "$1"
}

# The probes: under --plan they answer so that every action shows, but for
# the commands KARIO_WITHOUT names (the plan of a phone without them).
have() {
	if [ "$PLAN" = 1 ]; then
		case " ${KARIO_WITHOUT:-} " in *" $1 "*) return 1 ;; esac
		return 0
	fi
	command -v "$1" > /dev/null 2>&1
}
# image_has NAME: the image has /usr/bin/NAME (on the whole phone, the system).
image_has() {
	if [ "$PLAN" = 1 ]; then
		case " ${KARIO_WITHOUT:-} " in *" image:$1 "*) return 1 ;; esac
		return 0
	fi
	[ -x "$ROOT/usr/bin/$1" ]
}
running() { [ "$PLAN" = 0 ] && pidof "$1" > /dev/null 2>&1; }
mounted() { [ "$PLAN" = 0 ] && awk -v m="$1" '$2 == m { f = 1 } END { exit !f }' /proc/mounts; }
# KARIO_INIT names the init system where looking cannot (the plan).
init() {
	if [ -n "${KARIO_INIT:-}" ]; then
		echo "$KARIO_INIT"
	elif [ "$PLAN" = 0 ] && [ -d /run/systemd/system ]; then
		echo systemd
	else
		echo openrc
	fi
}
service_running() {
	[ "$PLAN" = 1 ] && return 0
	case $(init) in
	systemd) systemctl is-active -q "$1" 2> /dev/null ;;
	*) command -v rc-service > /dev/null && rc-service -e "$1" 2> /dev/null && rc-service "$1" status > /dev/null 2>&1 ;;
	esac
}
service() {
	case $(init) in
	systemd) run systemctl "$1" "$2" ;;
	*) run rc-service "$2" "$1" ;;
	esac
}

# The run's results directory, R on the phone and RR as the tests see it
# (in the chroot, where the results are bound).
results() {
	[ -n "${R:-}" ] && return 0
	if [ "$PLAN" = 1 ]; then
		R=$RESULTS/'<run>'
		if [ "$TARGET" = whole ]; then RR=$R; else RR=/var/lib/kario-results/'<run>'; fi
		return 0
	fi
	RUN=$(cat "$STATE/run" 2> /dev/null || true)
	if [ -z "$RUN" ]; then
		RUN=$(date +%Y%m%d-%H%M%S)
		run mkdir -p "$STATE"
		echo "$RUN" | put "$STATE/run"
	fi
	R=$RESULTS/$RUN
	if [ "$TARGET" = whole ]; then RR=$R; else RR=/var/lib/kario-results/$RUN; fi
	run mkdir -p "$R"
}

# kario_here: this system is Kario itself (a flashed phone), not Nura.
kario_here() { [ -x "${KARIO_ROOT:-}/usr/bin/phone-shell" ] && [ -f "${KARIO_ROOT:-}/usr/share/phone/sku.json" ]; }

ready() {
	[ "$PLAN" = 1 ] && return 0
	if [ "$TARGET" = whole ]; then
		kario_here || die "this phone does not run Kario: flash it (yocto/fajita-flash.sh flash), or drop --whole for the chroot on Nura"
	else
		mounted "$ROOT" || die "the chroot is not set up (setup first)"
	fi
}

# note_kit [again]: kit.txt in the run's results, what was tested: the
# build machine's tree and image (kario-phone.sh writes it at push), the
# image as the phone has it and the commit the image was built from.
note_kit() {
	if [ "$PLAN" = 1 ]; then
		echo "phone# (the kit's kit.txt: the tree's commit, the image; the image's stamp, os-release and source.txt) > $R/kit.txt"
		return 0
	fi
	[ "${1:-}" = again ] || [ ! -f "$R/kit.txt" ] || return 0
	{
		cat "$KIT/kit.txt" 2> /dev/null || echo "kit: (pushed without kit.txt)"
		if [ "$TARGET" = whole ]; then
			echo "path: the whole phone (Kario's kernel and image)"
		else
			echo "path: a chroot on $(sed -n 's/^PRETTY_NAME=//p' /etc/os-release 2> /dev/null | tr -d '"'), kernel $(uname -r)"
			echo "image on the phone: $(cat "$BASE/rootfs.stamp" 2> /dev/null || echo '(no stamp)')"
		fi
		if [ "$TARGET" = whole ] || mounted "$ROOT"; then
			image_source
			for f in "$ROOT/etc/os-release" "$ROOT/usr/lib/os-release"; do
				if [ -f "$f" ]; then
					sed -n -E 's/^(NAME|VERSION|VERSION_ID|BUILD_ID|IMAGE_ID|IMAGE_VERSION)=/image os-release \1=/p' "$f"
					break
				fi
			done
			if [ -f "$ROOT/etc/timestamp" ]; then echo "image built: $(cat "$ROOT/etc/timestamp")"; fi
		fi
	} | put "$R/kit.txt"
}

# image_source: the commit the image was built from (share/phone/source.txt,
# which tools/sku.py gen writes for the build), and whether the kit's tree
# is another one.
image_source() {
	src=$ROOT/usr/share/phone/source.txt
	if [ ! -f "$src" ]; then
		echo "image built from: (not recorded: an image built before source.txt)"
		return 0
	fi
	sed '1s/^/image built from: /; 2,$s/^/ /' "$src"
	mine=$(sed -n '1s/^kit: \([0-9a-f]*\).*/\1/p' "$KIT/kit.txt" 2> /dev/null)
	theirs=$(sed -n '1s/^\([0-9a-f]*\).*/\1/p' "$src")
	if [ -n "$mine" ] && [ -n "$theirs" ] && [ "$mine" != "$theirs" ]; then
		echo "note: the kit's tree is not the commit the image was built from: the scripts and the image differ"
	fi
}

# The build machine's clock (KARIO_HOST_TIME, seconds since 1970) when the
# phone's is far behind: the 6T's RTC is read-only on mainline, so without a
# network the phone starts at 1970, and so would the results' names.
set_clock() {
	CLOCK=
	host=${KARIO_HOST_TIME:-}
	case $host in '' | *[!0-9]*) return 0 ;; esac
	if [ "$PLAN" = 1 ]; then
		echo "phone# date -u -s @$host (the build machine's clock), when the phone's is 10 minutes or more behind"
		return 0
	fi
	[ "$(date +%s)" -le $((host - 600)) ] || return 0
	old=$(date -u '+%Y-%m-%d %H:%M:%S')
	if ! run date -u -s "@$host" > /dev/null; then
		say "the phone's clock says $old UTC; setting it to the build machine's failed"
		return 0
	fi
	[ "$DRY" = 0 ] || return 0
	CLOCK="clock: the phone's said $old UTC, set to the build machine's: $(date -u '+%Y-%m-%d %H:%M:%S') UTC"
	say "$CLOCK"
}

# enter CMD...: this process becomes CMD with a clean environment (Nura's
# names its own paths): in the chroot, in our cgroup's shell group when
# there is one (the Shell takes a delegated group it starts in
# <group>/shell, shell/cgroup.go, and the group itself may hold no process
# once a Shell has given it controllers for its children); on the whole
# phone in the scope bg made for the step. The clock is the system's, not
# the test Shell's (PHONE_CLOCK=host, hal/rtc): it sets no time, starts no
# NTP and keeps no RTC offset; in the chroot setup sets Nura's from the
# build machine's.
step_enter() {
	set -- /usr/bin/env -i HOME=/root PATH=/usr/sbin:/usr/bin:/sbin:/bin TERM=linux LANG=C.UTF-8 PHONE_CLOCK=host "$@"
	[ "$TARGET" = chroot ] || exec "$@"
	if [ -w "$CG/shell/cgroup.procs" ]; then
		echo 0 > "$CG/shell/cgroup.procs"
	fi
	exec chroot "$ROOT" "$@"
}

# inroot LOG CMD...: CMD in the image (the chroot, or the whole phone's
# system), its output into LOG.
inroot() {
	log=$1
	shift
	if [ "$DRY" = 1 ]; then
		if [ "$TARGET" = whole ]; then echo "phone# $* > $log"; else echo "phone# chroot $ROOT $* > $log"; fi
		return 0
	fi
	sh "$SELF" enter "$@" > "$log" 2>&1
}

# Our processes: in the chroot those whose root is the chroot, everything
# in our cgroup, and their descendants (a sandboxed app has a root of its
# own); on the whole phone Kario's (its services, the Shell, the apps: the
# phone-* units and scopes) and the kit's steps.
ours() {
	{
		if [ "$TARGET" = whole ]; then
			grep -l -e '/phone-' -e '/app\.' /proc/[0-9]*/cgroup 2> /dev/null | cut -d / -f 3
			jobs_of_kit
		else
			for p in /proc/[0-9]*; do
				case $(readlink "$p/root" 2> /dev/null) in
				"$ROOT" | "$ROOT"/*) echo "${p#/proc/}" ;;
				esac
			done
			if [ -d "$CG" ]; then
				find "$CG" -name cgroup.procs -exec cat {} \; 2> /dev/null
			fi
		fi
	} | descendants
}

# jobs_of_kit: the steps bg started (on-phone.sh --job).
jobs_of_kit() {
	for p in /proc/[0-9]*; do
		case "$(tr '\0' ' ' < "$p/cmdline" 2> /dev/null) " in
		*"on-phone.sh --job "*) echo "${p#/proc/}" ;;
		esac
	done
}

descendants() {
	seeds=$(tr '\n' ' ')
	for st in /proc/[0-9]*/stat; do
		cat "$st" 2> /dev/null
		echo
	done | awk -v seeds="$seeds" '
		BEGIN { n = split(seeds, s, " "); for (i = 1; i <= n; i++) keep[s[i]] = 1 }
		NF { t = $0; sub(/.*\) /, "", t); split(t, f, " "); parent[$1] = f[2] }
		END {
			do { more = 0; for (p in parent) if (!(p in keep) && (parent[p] in keep)) { keep[p] = 1; more = 1 } } while (more)
			for (p in keep) if (p in parent) print p
		}' | sort -n
}

# stop_ours: in the chroot every process of ours; on the whole phone only
# the kit's steps and what they started (the system's services are
# systemd's, started again by restore).
stop_ours() {
	if [ "$PLAN" = 1 ]; then
		if [ "$TARGET" = whole ]; then
			echo "phone# kill -TERM <the kit's steps and their processes>; after 10 s kill -KILL what is left"
		else
			echo "phone# kill -TERM <our processes>; after 10 s kill -KILL what is left"
		fi
		return 0
	fi
	pids=$(stoppable)
	[ -n "$pids" ] || return 0
	# shellcheck disable=SC2086 # one argument per process
	run kill -TERM $pids 2> /dev/null
	i=0
	while [ "$i" -lt 10 ] && [ -n "$(stoppable)" ]; do
		sleep 1
		i=$((i + 1))
	done
	pids=$(stoppable)
	# shellcheck disable=SC2086
	[ -z "$pids" ] || run kill -KILL $pids 2> /dev/null
}
stoppable() {
	if [ "$TARGET" = whole ]; then jobs_of_kit | grep -v -x "$$" | descendants; else ours; fi
}

step_check() {
	if [ "$PLAN" = 1 ]; then
		if [ "$TARGET" = whole ]; then
			say "check: root, aarch64, device tree compatible $KARIO_COMPATIBLE, Kario with the SKU $KARIO_SKU"
		else
			say "check: root, aarch64, device tree compatible $KARIO_COMPATIBLE, loop devices, Nura (not Kario) on the phone"
		fi
		return 0
	fi
	bad=0
	[ "$(id -u)" = 0 ] || {
		say "check: not root"
		bad=1
	}
	[ "$(uname -m)" = aarch64 ] || {
		say "check: an $(uname -m) phone, the image is aarch64"
		bad=1
	}
	compat=$(tr '\0' ' ' 2> /dev/null < /proc/device-tree/compatible)
	case " $compat " in
	*" $KARIO_COMPATIBLE "*) ;;
	*)
		if [ "${KARIO_FORCE:-0}" = 1 ]; then
			say "check: this phone is '$compat', not $KARIO_COMPATIBLE; going on (--force)"
		else
			say "check: this phone is '$compat', not $KARIO_COMPATIBLE ($KARIO_SKU's board; --force to go on)"
			bad=1
		fi
		;;
	esac
	if [ "$TARGET" = whole ]; then
		check_whole || bad=1
	else
		[ -e /dev/loop-control ] || [ -e /dev/loop0 ] || {
			say "check: no loop devices"
			bad=1
		}
		if kario_here; then
			say "check: this phone runs Kario itself (flashed): --whole tests it as it is"
			bad=1
		fi
	fi
	[ "$bad" = 0 ] && say "check: $KARIO_SKU on $(tr -d '\0' 2> /dev/null < /proc/device-tree/model) ($TARGET)"
	return "$bad"
}

# check_whole: Kario runs, built for this SKU (its own sku.json).
check_whole() {
	if ! kario_here; then
		say "check: this phone does not run Kario (no /usr/bin/phone-shell): flash it (yocto/fajita-flash.sh flash), or drop --whole for the chroot on Nura"
		return 1
	fi
	id=$(sed -n 's/^ *"id": *"\([^"]*\)".*/\1/p' "$ROOT/usr/share/phone/sku.json" | head -n 1)
	[ "$id" = "$KARIO_SKU" ] && return 0
	if [ "${KARIO_FORCE:-0}" = 1 ]; then
		say "check: Kario on this phone was built for the SKU '$id', not $KARIO_SKU; going on (--force)"
		return 0
	fi
	say "check: Kario on this phone was built for the SKU '$id', not $KARIO_SKU (flash this SKU's build, or --force)"
	return 1
}

# fact TITLE CMD...: one section of the facts.
fact() {
	echo "== $1"
	shift
	"$@" 2>&1 || true
	echo
}

each() {
	for d in "$@"; do
		[ -e "$d" ] || continue
		printf '%s:' "$d"
		for f in $FIELDS; do
			[ -r "$d/$f" ] && printf ' %s=%s' "$f" "$(attr "$d/$f")"
		done
		echo
	done
}

# attr FILE: an attribute of sysfs or procfs on one line. Reading some
# blocks: /sys/power/wakeup_count waits for as long as a wake-up event is
# in progress, so while a wake lock is held (phone-bringup-awake's on the
# whole phone), and facts hung there and setup with it (2026-11-24). A
# read gives up after KARIO_READ_TIMEOUT seconds (2) and says so; the
# reader is killed, not waited for (one stuck in the kernel stays). The
# images' busybox has no timeout.
attr() {
	out=$STATE/attr.$$
	cat "$1" > "$out" 2> /dev/null &
	p=$!
	n=$((${KARIO_READ_TIMEOUT:-2} * 100))
	while [ "$n" -gt 0 ] && alive "$p"; do
		sleep 0.01
		n=$((n - 1))
	done
	if alive "$p"; then
		kill -KILL "$p" 2> /dev/null
		printf '<no answer in %s s>' "${KARIO_READ_TIMEOUT:-2}"
	else
		wait "$p"
		tr '\n' ' ' < "$out" | sed 's/ *$//'
	fi
	rm -f "$out"
}

f_system() {
	cat /etc/os-release
	grep -h codename /usr/share/deviceinfo/deviceinfo /etc/deviceinfo 2> /dev/null
	echo "model: $(tr -d '\0' < /proc/device-tree/model)"
	echo "compatible: $(tr '\0' ' ' < /proc/device-tree/compatible)"
	uname -a
	cat /proc/cmdline
	grep -E 'MemTotal|MemAvailable' /proc/meminfo
	grep 'CPU part' /proc/cpuinfo | sort | uniq -c
	df -h "$BASE" 2> /dev/null || df -h /
}

f_kernel() {
	echo "lsm: $(cat /sys/kernel/security/lsm 2> /dev/null)"
	grep cgroup /proc/mounts
	grep Seccomp /proc/self/status
	if [ -r /proc/config.gz ]; then
		zcat /proc/config.gz | grep -E '^CONFIG_(SECURITY_LANDLOCK|SECCOMP_FILTER|NF_TABLES|NFT_CT|USB_CONFIGFS_NCM|INPUT_UINPUT|LEDS_USER|DRM_MSM|MEMCG|CGROUP_PIDS|PSI|SUSPEND)='
	fi
}

# The capability bitmaps' words with 0x: scrub takes a run of 14 or more
# digits for an identity (KEY_POWER alone is 10000000000000).
f_input() {
	for e in "${KARIO_SYS:-/sys}"/class/input/event*; do
		d=$e/device
		echo "${e##*/}: $(cat "$d/name") | ev $(bits "$d/capabilities/ev") | key $(bits "$d/capabilities/key") | abs $(bits "$d/capabilities/abs") | ff $(bits "$d/capabilities/ff")"
	done
}
bits() { sed 's/[0-9a-f][0-9a-f]*/0x&/g' "$1"; }

f_drm() {
	FIELDS="status enabled dpms modes" each /sys/class/drm/card*-*
	FIELDS="name" each /sys/class/drm/card*/device/driver
	if command -v modetest > /dev/null; then modetest -c; fi
}

f_sleep() {
	sys=${KARIO_SYS:-/sys}
	FIELDS="state mem_sleep wakeup_count pm_wakeup_irq" each "$sys/power"
	FIELDS="success fail last_failed_dev last_failed_errno last_failed_step" each "$sys/power/suspend_stats"
	FIELDS="name since_epoch hctosys wakealarm" each "$sys"/class/rtc/rtc*
	echo "system clock: $(date -u +%s)"
	echo "wake locks: $(attr "$sys/power/wake_lock")"
	# The bring-up image's lock, held while a computer has its gadget
	# configured: whether it should be now, and why.
	if [ -x "${KARIO_BRINGUP_AWAKE:-/usr/libexec/phone-bringup/awake}" ]; then
		echo "phone-bringup-awake: $("${KARIO_BRINGUP_AWAKE:-/usr/libexec/phone-bringup/awake}" check)"
	fi
	# wakeup_count answers once none of these is active.
	printf 'wake-up sources active:'
	for w in "$sys"/class/wakeup/wakeup*; do
		[ -r "$w/active_time_ms" ] && [ "$(attr "$w/active_time_ms")" != 0 ] && printf ' %s' "$(attr "$w/name")"
	done
	echo
	# Every wake-up source with its device's power/wakeup and how often it
	# woke the system: whether the charger's (pmic@2:charger@1000, its cable
	# interrupt usb-plugin) can wake the phone from s2idle (28-battery.md §1).
	for w in "$sys"/class/wakeup/wakeup*; do
		[ -r "$w/name" ] || continue
		echo "wake-up source $(cat "$w/name"): wakeup=$(cat "$w/device/power/wakeup" 2> /dev/null) wakeup_count=$(cat "$w/wakeup_count" 2> /dev/null)"
	done
	grep -E 'usb-plugin' /proc/interrupts 2> /dev/null | tr -s ' '
	# The keys that can wake the phone: the device each input device belongs
	# to (the PMIC's power key is a platform device of its own, pon@800's
	# pwrkey), its power/wakeup with how often it woke the system or called
	# a suspend off, and its line in /proc/interrupts.
	for w in /sys/class/input/input*/device/power/wakeup; do
		[ -r "$w" ] || continue
		i=${w%/device/power/wakeup}
		echo "key wake-up source $(cat "$i/name"):"
		FIELDS="wakeup wakeup_count wakeup_abort_count wakeup_active_count" each "$(readlink -f "$i/device")/power"
		grep -F "$(cat "$i/name")" /proc/interrupts | tr -s ' '
	done
}

# The hardware watchdog, and how the boot before this one ended: systemd's
# lines about the watchdog, the power key, the reboot or power-off in its
# journal, and the kernel's last lines, which pstore keeps across a reset.
f_watchdog() {
	ls -l /dev/watchdog* 2> /dev/null
	FIELDS="identity timeout state bootstatus" each /sys/class/watchdog/watchdog*
	journalctl -b -1 --no-pager -o short-monotonic 2> /dev/null |
		grep -iE 'watchdog|power key|systemd-shutdown|reboot|power-off|powering off' | tail -n 20
	for f in /sys/fs/pstore/console-ramoops* /var/lib/systemd/pstore/*/console-ramoops*; do
		[ -r "$f" ] || continue
		echo "$f:"
		tail -n 5 "$f"
	done
}

f_usb() {
	FIELDS="state current_speed" each /sys/class/udc/*
	FIELDS="UDC" each /sys/kernel/config/usb_gadget/*
	ip -br addr 2> /dev/null || ip addr
}

# alsaucm_cmd: how this system runs the alsaucm that sets the card up: the
# image's (in the chroot through chroot), else Nura's; empty with neither.
alsaucm_cmd() {
	if image_has alsaucm; then
		if [ "$TARGET" = whole ]; then echo alsaucm; else echo "chroot $ROOT /usr/bin/alsaucm"; fi
	elif [ "$TARGET" = chroot ] && have alsaucm; then
		echo alsaucm
	fi
}

f_sound() {
	cat /proc/asound/cards
	if [ -z "${KARIO_AUDIO_VERB:-}" ]; then
		echo "the board names no UCM set-up ([audio]): phone-audiod plays to the card it finds"
		return 0
	fi
	if [ "$TARGET" = whole ]; then
		who="phone-audiod sets it up itself (the system's, and a selftest's own from sku.json)"
	elif image_has alsaucm; then
		who="phone-audiod sets it up itself with the image's alsaucm"
	else
		who="setup sets it up with Nura's alsaucm (the image has none)"
	fi
	echo "the board: phone-audiod plays to $KARIO_AUDIO_PCM; ${KARIO_AUDIO_VERB}${KARIO_AUDIO_DEVICE:+ with $KARIO_AUDIO_DEVICE} on $KARIO_AUDIO_CARD: $who"
	tool=$(alsaucm_cmd)
	if [ -n "$tool" ]; then
		# shellcheck disable=SC2086 # the command's words
		$tool -c "$KARIO_AUDIO_CARD" list _verbs
		# shellcheck disable=SC2086
		$tool -c "$KARIO_AUDIO_CARD" list "_devices/$KARIO_AUDIO_VERB"
	else
		echo "no alsaucm in the image or on Nura: no UCM verb set, phone-audiod cannot play (the board's own image has it; or $(install_hint alsa-utils))"
	fi
	# The mixer's controls, and those a test turns down (the board's
	# quiet_controls) as they are now.
	tool=$(amixer_cmd)
	[ -n "$tool" ] || return 0
	# shellcheck disable=SC2086 # the command's words
	$tool -c "${KARIO_AUDIO_CARD#hw:}" scontrols
	old=$IFS
	IFS='|'
	# shellcheck disable=SC2086 # the controls, split at |
	set -- ${KARIO_AUDIO_QUIET:-}
	IFS=$old
	for c in "$@"; do
		# shellcheck disable=SC2086
		$tool -c "${KARIO_AUDIO_CARD#hw:}" sget "$c"
	done
}

# The sound card's UCM set-up for media, the board's [audio] verb and
# device. An image with alsaucm (the board's own, as the whole phone) leaves
# it to the Shell's own phone-audiod, which has the board's values from
# sku.json; for one without (the QEMU image) setup does it with Nura's
# alsaucm, as the kernel keeps the mixer as it sets it. A board without
# [audio] gets none. Noted for teardown.
ucm_setup() {
	verb=${KARIO_AUDIO_VERB:-} dev=${KARIO_AUDIO_DEVICE:-} card=${KARIO_AUDIO_CARD:-}
	if [ -z "$verb" ]; then
		say "the board names no UCM set-up ([audio]): phone-audiod plays to the card it finds"
		return 0
	fi
	what="UCM verb $verb${dev:+ and device $dev} on $card"
	if image_has alsaucm; then
		say "the image's alsaucm: phone-audiod sets the $what up itself, as on the whole phone"
		printf '%s\n' "$card" "$verb" "$dev" image | put "$STATE/ucm"
		return 0
	fi
	if ! have alsaucm; then
		say "no alsaucm in the image or on Nura: the sound card gets no $what, so phone-audiod cannot play (${KARIO_AUDIO_PCM:-the card}: aplay: Invalid argument); use the board's own image, or install Nura's ($(install_hint alsa-utils)) and run setup again"
		return 0
	fi
	set -- set _verb "$verb"
	if [ -n "$dev" ]; then set -- "$@" set _enadev "$dev"; fi
	if run alsaucm -c "$card" "$@"; then
		printf '%s\n' "$card" "$verb" "$dev" nura | put "$STATE/ucm"
		say "the image has no alsaucm: Nura's set the $what up"
	else
		say "could not set the $what: phone-audiod plays to the card as it is"
	fi
}

# What ucm_setup noted, taken down with the alsaucm it named: alsaucm keeps
# nothing between runs, so it names the set-up first (its controls to the
# values they have), then disables the device and leaves the verb (its
# disable sequence).
ucm_teardown() {
	if [ "$PLAN" = 1 ]; then
		card=${KARIO_AUDIO_CARD:-} verb=${KARIO_AUDIO_VERB:-} dev=${KARIO_AUDIO_DEVICE:-} by=nura
		if image_has alsaucm; then by=image; fi
	elif [ -f "$STATE/ucm" ]; then
		{
			read -r card
			read -r verb
			read -r dev
			read -r by || by=nura
		} < "$STATE/ucm"
	else
		return 0
	fi
	[ -n "$verb" ] || return 0
	if [ "$by" = image ]; then
		set -- chroot "$ROOT" /usr/bin/alsaucm
	elif have alsaucm; then
		set -- alsaucm
	else
		return 0
	fi
	set -- "$@" -c "$card" set _verb "$verb"
	if [ -n "$dev" ]; then set -- "$@" set _enadev "$dev" set _disdev "$dev"; fi
	run "$@" set _verb Inactive || say "could not take the UCM verb $verb down on $card: a reboot does"
}

# amixer_cmd: the amixer that sets the card's mixer: this system's (on the
# whole phone the image's), else the image's in the chroot; empty with
# neither.
amixer_cmd() {
	if have amixer; then
		echo amixer
	elif [ "$TARGET" = chroot ] && image_has amixer; then
		echo "chroot $ROOT /usr/bin/amixer"
	fi
}

# The selftest's and the pixel run's media cases play through the phone's
# speaker. quiet turns each mixer control the board's [audio] names for
# that (KARIO_AUDIO_QUIET) down to its level (KARIO_AUDIO_QUIET_LEVEL), or
# to KARIO_QUIET (kario-phone.sh --quiet: a level, or off to leave the
# mixer alone), and notes the values it had for unquiet, which sets them
# back after the run.
quiet() {
	level=${KARIO_QUIET:-${KARIO_AUDIO_QUIET_LEVEL:-}}
	[ -n "${KARIO_AUDIO_QUIET:-}" ] && [ -n "$level" ] || return 0
	if [ "$level" = off ]; then
		say "the speaker plays at the mixer's level (--quiet off)"
		return 0
	fi
	tool=$(amixer_cmd)
	if [ -z "$tool" ]; then
		say "no amixer: the speaker plays at the mixer's level ($(install_hint alsa-utils))"
		return 0
	fi
	card=${KARIO_AUDIO_CARD#hw:}
	# A run that was stopped before it set the mixer back left its note:
	# that one has the values from before.
	noted=0
	if [ "$PLAN" = 0 ] && [ -f "$STATE/quiet" ]; then noted=1; fi
	old=$IFS
	IFS='|'
	# shellcheck disable=SC2086 # the controls, split at |
	set -- $KARIO_AUDIO_QUIET
	IFS=$old
	for c in "$@"; do
		# The raw value of each channel, which sset takes back as they were.
		if [ "$PLAN" = 1 ]; then
			was="<as it is>"
		else
			# shellcheck disable=SC2086 # the command's words
			was=$($tool -c "$card" sget "$c" 2> /dev/null | sed -n 's/^ *[^:]*: \(Playback \)\{0,1\}\(-\{0,1\}[0-9][0-9]*\) \[.*/\2/p' | tr '\n' , | sed 's/,$//')
		fi
		if [ -z "$was" ]; then
			say "card $card has no volume control $c: it plays as it is"
			continue
		fi
		# shellcheck disable=SC2086 # the command's words
		run $tool -c "$card" -q sset "$c" "$level" || continue
		if [ "$noted" = 0 ]; then printf '%s\t%s\n' "$c" "$was" | append "$STATE/quiet"; fi
	done
	say "the speaker turned down to $level for the run; it is set back after it"
}
unquiet() {
	if [ "$PLAN" = 1 ]; then
		[ -n "${KARIO_AUDIO_QUIET:-}" ] && [ "${KARIO_QUIET:-}" != off ] || return 0
		echo "phone# $(amixer_cmd) -c ${KARIO_AUDIO_CARD#hw:} -q sset <each control quiet turned down> -- <the value it had> ($STATE/quiet)"
		return 0
	fi
	[ -f "$STATE/quiet" ] || return 0
	tool=$(amixer_cmd)
	card=${KARIO_AUDIO_CARD#hw:}
	tab=$(printf '\t')
	while IFS=$tab read -r c was; do
		# shellcheck disable=SC2086 # the command's words
		[ -n "$tool" ] && run $tool -c "$card" -q sset "$c" -- "$was"
	done < "$STATE/quiet"
	run rm -f "$STATE/quiet"
}

# install_hint PACKAGE: how this base installs it.
install_hint() {
	if have apk; then
		echo "apk add $1"
	elif have apt-get; then
		echo "apt-get install $1"
	else
		echo "the package $1"
	fi
}

# KARIO_FIRMWARE=image: what Nura's kernel loads from now on (the GPU's
# firmware, when the card is first opened; a radio's, when it starts again)
# it looks for in the image's /lib/firmware first, as the whole phone has
# it; what is not there it still finds in Nura's. The path before is noted
# for teardown.
firmware_setup() {
	[ "${KARIO_FIRMWARE:-nura}" = image ] || return 0
	if [ "$PLAN" = 0 ]; then
		[ -d "$ROOT/lib/firmware" ] || die "--firmware image: the image has no /lib/firmware (the board's own image has its firmware)"
		[ -w "$FWPATH" ] || die "--firmware image: Nura's kernel has no $FWPATH"
		[ -f "$STATE/firmware" ] || put "$STATE/firmware" < "$FWPATH"
	fi
	if [ "$DRY" = 1 ]; then
		echo "phone# printf %s $ROOT/lib/firmware > $FWPATH"
	else
		printf '%s' "$ROOT/lib/firmware" > "$FWPATH"
	fi
	say "the kernel looks for firmware in the image's /lib/firmware first"
}
firmware_teardown() {
	if [ "$PLAN" = 1 ]; then
		[ "${KARIO_FIRMWARE:-nura}" = nura ] || echo "phone# (the firmware path setup found) > $FWPATH"
		return 0
	fi
	[ -f "$STATE/firmware" ] || return 0
	put "$FWPATH" < "$STATE/firmware"
}

f_backlight() { FIELDS="type max_brightness brightness" each /sys/class/backlight/*; }
f_leds() { FIELDS="max_brightness multi_index trigger" each /sys/class/leds/*; }
f_power() { FIELDS="type status capacity voltage_now current_now charge_now charge_full charge_counter usb_type" each /sys/class/power_supply/*; }
f_iio() { FIELDS="name" each /sys/bus/iio/devices/*; }
f_rfkill() { FIELDS="type name state" each /sys/class/rfkill/*; }
f_dmesg() { dmesg | grep -iE 'panel|dsi|mdss|remoteproc|q6v5|rmtfs|ipa|wcn|bluetooth|firmware|haptic|touch|rmi4|edt|goodix|a630|adreno|gmu' | tail -120; }

f_modem() {
	for p in ModemManager rmtfs pd-mapper tqftpserv qrtr-ns hexagonrpcd q6voiced 81voltd; do
		echo "$p: $(pidof "$p" || echo none)"
	done
	if command -v mmcli > /dev/null; then
		mmcli -L
		mmcli -m any
		mmcli -m any --location-status
		mmcli -m any --list-bearers
	fi
	# The modem's carrier configurations and IMS registration (VoLTE,
	# 39-oneplus-6t.md §11.1): queries only, nothing is changed.
	if command -v qmicli > /dev/null; then
		qmicli -d qrtr://0 --pdc-list-configs=software
		qmicli -d qrtr://0 --imsa-get-ims-registration-status
	fi
}

f_services() {
	case $(init) in
	systemd) systemctl list-units --type=service --state=running --no-pager ;;
	*) rc-status -a ;;
	esac
}

f_image() {
	if [ "$TARGET" = whole ]; then
		echo "the whole phone: the system is the image"
	elif ! mounted "$ROOT"; then
		echo "not mounted"
		return 0
	fi
	if grep -q "$MARK" "$ROOT/usr/bin/kario-system"; then
		echo "the image's Shell has whole-phone support"
	else
		echo "the image's Shell predates whole-phone support: rebuild the image"
	fi
	image_source
	cat "$ROOT/usr/share/phone/sku.json" 2> /dev/null
}

# The phone's partitions: name, device, bytes, and the role the board file
# gives it (KARIO_PARTITIONS, name:role each; 43-phone-partitions.md);
# then what the board names and the phone does not have. KARIO_SYS moves
# /sys (the tests').
f_partitions() {
	sys=${KARIO_SYS:-/sys}
	for u in "$sys"/class/block/*/uevent; do
		[ -f "$u" ] || continue
		n=$(sed -n 's/^PARTNAME=//p' "$u")
		[ -n "$n" ] || continue
		d=${u%/uevent}
		role=
		for e in ${KARIO_PARTITIONS:-}; do
			if [ "${e%%:*}" = "$n" ]; then role=${e#*:}; fi
		done
		echo "$n ${d##*/} $(($(cat "$d/size") * 512))${role:+ $role}"
	done
	for e in ${KARIO_PARTITIONS:-}; do
		grep -qx "PARTNAME=${e%%:*}" "$sys"/class/block/*/uevent 2> /dev/null || echo "${e%%:*}: in the board file, not on this phone"
	done
}

step_partitions() { f_partitions; }

# What runs as on the whole phone and what does not, in the chroot: Nura's
# kernel (its modules, its firmware unless --firmware image), Nura's modem
# stack in place of the image's units, which do not run here; what the
# image brings for the board (alsaucm and the UCM profile, Mesa's drivers).
f_chroot() {
	r=$(uname -r)
	echo "kernel: Nura's $r, which loads its own modules (/lib/modules/$r) itself; the image's: $(names "$ROOT/lib/modules")"
	echo "firmware: the kernel looks in '$(cat "$FWPATH" 2> /dev/null)' first, then Nura's /lib/firmware; the image's /lib/firmware: $(find "$ROOT/lib/firmware" -type f 2> /dev/null | wc -l) files"
	echo "the image's alsaucm: $(if [ -x "$ROOT/usr/bin/alsaucm" ]; then echo yes; else echo no; fi); its UCM profiles: $(names "$ROOT/usr/share/alsa/ucm2/conf.d")"
	echo "the image's GPU drivers (Mesa): $(names "$ROOT/usr/lib/dri" "$ROOT/usr/lib/gbm")"
	echo "the image's units, which do not run here (the Shell starts its own services; Nura's run the modem):"
	for w in "$ROOT"/etc/systemd/system/*.wants; do
		[ -d "$w" ] || continue
		echo "  ${w##*/}: $(names "$w")"
	done
}

# names DIR...: the names in each DIR, on one line.
names() {
	for d in "$@"; do
		for f in "$d"/*; do
			if [ -e "$f" ] || [ -L "$f" ]; then printf '%s ' "${f##*/}"; fi
		done
	done
}

# What leaves the phone for a report carries none of its identities: the
# serial number, which the bootloader puts on the kernel's command line
# (androidboot.serialno) and so into its log, the modem's IMEI, IMSI,
# ICCID and number (a run of 14 to 20 digits is taken for one of those),
# and hardware addresses (the whole phone makes its Bluetooth and USB
# addresses from the serial number).
step_scrub() {
	sn=$(tr ' ' '\n' < "${KARIO_CMDLINE:-/proc/cmdline}" 2> /dev/null | sed -n 's/^androidboot\.serialno=//p' | head -n 1)
	set -- -e 's/(serialno|serialnumber|serial_number)=[^ ]*/\1=<removed>/g' \
		-e 's/(^|[^A-Za-z])(equipment id|imei|imsi|iccid|own)( *: *)[^ ]+/\1\2\3<removed>/g' \
		-e 's/(^|[^0-9A-Za-z])[0-9]{14,20}($|[^0-9A-Za-z])/\1<removed>\2/g' \
		-e 's/(^|[^0-9A-Fa-f:])([0-9A-Fa-f]{2}:){5}[0-9A-Fa-f]{2}($|[^0-9A-Fa-f:])/\1<mac>\3/g'
	case $sn in
	*[!0-9A-Za-z]*) ;;
	*) if [ "${#sn}" -ge 6 ]; then set -- "$@" -e "s/$sn/<serial>/g"; fi ;;
	esac
	sed -E "$@"
}

step_facts() {
	results
	if [ "$DRY" = 1 ]; then
		echo "phone# (system, kernel, input, drm, backlight, leds, power supplies, iio, sleep, watchdog and the last boot's end, usb, rfkill, sound, modem, services, image$(if [ "$TARGET" = chroot ]; then echo ", Nura's and the image's"; fi), partitions, dmesg; the serial number, the modem's identities and hardware addresses removed) > $R/facts.txt"
		return 0
	fi
	{
		echo "facts of $KARIO_SKU ($KARIO_BOARD), $TARGET, $(date)"
		echo
		fact "unverified values of the board, and where the facts answer them" f_unverified
		fact system f_system
		fact kernel f_kernel
		fact "input devices" f_input
		fact drm f_drm
		fact backlight f_backlight
		fact leds f_leds
		fact "power supplies" f_power
		fact iio f_iio
		fact sleep f_sleep
		fact "watchdog and the last boot's end" f_watchdog
		fact usb f_usb
		fact rfkill f_rfkill
		fact sound f_sound
		fact modem f_modem
		fact services f_services
		fact image f_image
		if [ "$TARGET" = chroot ]; then fact "chroot: what is Nura's and what is the image's" f_chroot; fi
		fact partitions f_partitions
		fact dmesg f_dmesg
	} 2>&1 | step_scrub > "$R/facts.txt"
	say "facts in $R/facts.txt"
}

# Where the facts above answer each value the board marks unverified.
f_unverified() {
	for u in $KARIO_UNVERIFIED; do
		case $u in
		gnss) w="== modem (location status); then an app asking for the position" ;;
		ims) w="== modem (81voltd, the bearers, the configurations, IMS registration); then a VoLTE call (39-oneplus-6t.md §14.6)" ;;
		leds.*) w="== leds" ;;
		display.inset) w="look at the panel: the status bar clear of the notch, the soft keys of the corners" ;;
		display.panel | display.touchscreen) w="== dmesg (the drivers that probed), == input" ;;
		display.width_mm | display.height_mm) w="== drm (modetest), or measure the panel" ;;
		input.sound_switch) w="== input ($KARIO_SOUND_SWITCH with abs bit 0x22, ABS_SND_PROFILE); then move the slider (39-oneplus-6t.md §14.18)" ;;
		input.*) w="== input (names with key bits)" ;;
		partitions.*) w="== partitions (names, sizes, what the board names and the phone lacks)" ;;
		rtc.*) w="== sleep (rtc0's since_epoch); again after a reboot and after a power-off: it kept counting (39-oneplus-6t.md)" ;;
		watchdog.*) w="== watchdog after a reboot: systemd-shutdown's timeout in the journal of the boot before, no second boot (39-oneplus-6t.md §14.10)" ;;
		*) w="" ;;
		esac
		echo "$u: $w"
	done
}

cgroup_setup() {
	if [ "$PLAN" = 0 ] && ! grep -q '^[^ ]* /sys/fs/cgroup cgroup2 ' /proc/mounts; then
		say "no unified cgroup hierarchy at /sys/fs/cgroup: no per-app groups (the selftest skips their cases)"
		return 0
	fi
	run mkdir -p "$CG/shell"
	for c in memory pids cpu; do
		run sh -c "echo +$c > /sys/fs/cgroup/cgroup.subtree_control" 2> /dev/null || true
	done
	# The Shell takes only a group marked delegated, as systemd marks
	# Delegate=yes (shell/cgroup.go).
	if have setfattr; then
		run setfattr -n trusted.delegate -v 1 "$CG"
	elif [ -x "$ROOT/usr/bin/setfattr" ]; then
		run chroot "$ROOT" /usr/bin/setfattr -n trusted.delegate -v 1 "$CG"
	else
		say "no setfattr (apk add attr): the Shell runs without per-app cgroups"
	fi
}

# bind FROM TO [r]: the host's tree in the chroot; slave, so that unmounting
# in the chroot never unmounts the host's.
bind() {
	mounted "$2" && return 0
	run mkdir -p "$2"
	if [ "${3:-}" = r ]; then
		run mount --rbind "$1" "$2"
		run mount --make-rslave "$2"
	else
		run mount --bind "$1" "$2"
	fi
}

scratch() {
	mounted "$1" && return 0
	run mkdir -p "$1"
	run mount -t tmpfs -o "mode=${2:-755},nosuid,nodev" tmpfs "$1"
}

step_setup() {
	step_check || die "check failed"
	set_clock
	results
	if [ -n "$CLOCK" ]; then echo "$CLOCK" | append "$R/clock.txt"; fi
	if [ "$TARGET" = whole ]; then
		setup_whole
	else
		setup_chroot
	fi
	step_facts
	[ "$DRY" = 1 ] || say "set up: $TARGET ($KARIO_SKU), results in $R"
}

# The whole phone keeps running as it is; what a step needs it changes for
# the step alone (quiesce).
setup_whole() {
	run mkdir -p "$STATE"
	note_kit again
	if [ "$PLAN" = 1 ] || systemctl is-active -q phone-bringup-awake.service 2> /dev/null; then
		say "phone-bringup-awake holds its wake lock while a computer has the USB console configured (facts: == sleep): the phone does not suspend while the kit is on the cable (the selftest's suspend case cannot pass; systemctl stop phone-bringup-awake lets it); unplugged, or on a charger, it sleeps"
	fi
}

setup_chroot() {
	run mkdir -p "$STATE" "$ROOT" "$BASE/state/phone" "$BASE/state/home"
	[ "$PLAN" = 1 ] || [ -f "$IMG" ] || die "no $IMG: push it first"
	for s in $STOP; do
		if service_running "$s"; then
			service stop "$s" && echo "$s" | append "$STATE/stopped"
		fi
	done
	for p in $KILL; do
		if [ "$PLAN" = 1 ] || pidof "$p" > /dev/null 2>&1; then
			run pkill -x "$p"
			echo "$p" | append "$STATE/killed"
		fi
	done
	if ! running ModemManager; then
		case $(init) in
		systemd) start="systemctl start ModemManager" ;;
		*) start="rc-service modemmanager start" ;;
		esac
		say "ModemManager is not running: telephonyd needs it for the modem (start Nura's: $start)"
	fi
	mounted "$ROOT" || run mount -t ext4 -o loop "$IMG" "$ROOT"
	if [ "$PLAN" = 0 ] && ! grep -q "$MARK" "$ROOT/usr/bin/kario-system"; then
		msg="this image's Shell predates whole-phone support: it would take the USB controller and cut Nura's USB network off, and draw at the form factor's scale; rebuild the image from this tree (make yocto-build)"
		[ "${KARIO_FORCE:-0}" = 1 ] || die "$msg (--force to go on, over Wi-Fi)"
		say "$msg"
	fi
	note_kit again
	bind /dev "$ROOT/dev" r
	mounted "$ROOT/proc" || run mount -t proc proc "$ROOT/proc"
	bind /sys "$ROOT/sys" r
	# Nura's /run: its system bus (/run/dbus), which ModemManager and BlueZ are on.
	bind /run "$ROOT/run" r
	scratch "$ROOT/tmp" 1777
	scratch "$ROOT/var/volatile"
	bind "$BASE/state/phone" "$ROOT/var/lib/phone"
	bind "$BASE/state/home" "$ROOT/root"
	bind "$RESULTS" "$ROOT/var/lib/kario-results"
	run mkdir -p "$ROOT/usr/share/phone/profile.d"
	run install -m 0644 "$KIT/sku.json" "$ROOT/usr/share/phone/sku.json"
	run install -m 0644 "$KIT/profile.json" "$ROOT/usr/share/phone/profile.d/20-product.json"
	# The image's resolv.conf points at systemd-resolved, which does not run here.
	run rm -f "$ROOT/etc/resolv.conf"
	run cp -L /etc/resolv.conf "$ROOT/etc/resolv.conf"
	cgroup_setup
	# Keys typed for the Shell would also reach a login prompt on the
	# console; an unused console gets them instead.
	if have fgconsole && have chvt; then
		if [ "$PLAN" = 1 ]; then echo "phone# fgconsole > $STATE/vt"; else fgconsole 2> /dev/null | put "$STATE/vt"; fi
		run chvt 8
	fi
	ucm_setup
	firmware_setup
}

# quiesce: on the whole phone the units a step takes the place of stop for
# it (noted, restore starts them again), and what they leave is gone.
quiesce() {
	[ "$TARGET" = whole ] || return 0
	for u in $QUIESCE; do
		if [ "$PLAN" = 1 ] || systemctl is-active -q "$u" 2> /dev/null; then
			run systemctl stop "$u" && echo "$u" | append "$STATE/stopped"
			settle "$u"
		fi
	done
}
restore() {
	[ "$TARGET" = whole ] || return 0
	if [ "$PLAN" = 1 ]; then
		settle '<each unit stopped for the step>'
		echo "phone# systemctl start <each unit stopped for the step> ($STATE/stopped)"
		return 0
	fi
	[ -f "$STATE/stopped" ] || return 0
	while read -r u; do settle "$u" < /dev/null; done < "$STATE/stopped"
	# shellcheck disable=SC2046 # one argument per unit
	run systemctl start $(cat "$STATE/stopped") || say "could not start $(tr '\n' ' ' < "$STATE/stopped") again: a reboot does"
	run rm -f "$STATE/stopped"
}

# left_in UNIT: the processes in UNIT's group, "pid (name)" each
# (KARIO_PROC moves /proc, the tests').
left_in() {
	for c in "${KARIO_PROC:-/proc}"/[0-9]*/cgroup; do
		grep -q "^0::.*/$1\$" "$c" 2> /dev/null || continue
		d=${c%/cgroup}
		printf '%s (%s) ' "${d##*/}" "$(cat "$d/comm" 2> /dev/null)"
	done
}

# settle UNIT: a unit stopped, the processes it left in its group go
# before it starts again. An image whose phone-display has KillMode=process
# (before 2026-11-24) leaves the panel renderer's input opener there, which
# keeps the display's DRM master: the next Shell's renderer cannot open the
# panel (Permission denied) and phone-display fails over and over. They
# get KARIO_SETTLE seconds (10), then systemd kills them as KillMode=mixed
# would.
settle() {
	t=${KARIO_SETTLE:-10}
	if [ "$PLAN" = 1 ]; then
		echo "phone# (up to $t s until nothing is left in $1's group; then systemctl kill --kill-whom=all --signal=SIGKILL $1, and up to $t s more)"
		return 0
	fi
	i=0
	while [ -n "$(left_in "$1")" ] && [ "$i" -lt "$t" ]; do
		sleep 1
		i=$((i + 1))
	done
	left=$(left_in "$1")
	[ -n "$left" ] || return 0
	say "$1 left ${left}running after it stopped (its image's KillMode leaves them): killing them, or it could not start again"
	run systemctl kill --kill-whom=all --signal=SIGKILL "$1" > /dev/null 2>&1
	i=0
	while [ -n "$(left_in "$1")" ] && [ "$i" -lt "$t" ]; do
		sleep 1
		i=$((i + 1))
	done
	left=$(left_in "$1")
	[ -z "$left" ] || say "$1 still has ${left}after systemctl kill: it cannot start again until they are gone (kill -KILL them, or reboot)"
}

# own_link: a step whose own Shell takes the USB controller (the selftest,
# the benchmark) needs a link that is not the developer link: that Shell
# sets the cable's link up as an untrusted computer's (shell/usb.go).
own_link() {
	[ "$TARGET" = whole ] || return 0
	at=$(echo "${SSH_CONNECTION:-}" | awk '{ print $3 }')
	[ "${at#::ffff:}" != "$DEVLINK" ] && return 0
	die "this login came over the developer link ($DEVLINK, the trusted cable): the $1's own Shell takes the USB controller and cuts it off; reach the phone over phone-bringup's USB network (root@172.16.42.1, an image with PHONE_BRINGUP = \"1\")"
}

summary() {
	grep -ah -E 'PHONE-SELFTEST-(FAILED|RESULT)|PHONE-CTS:|PHONE-GUI-FRAMES' "$@" 2> /dev/null
}

# timed NAME LOG CMD...: CMD in the image, its seconds and exit code noted.
timed() {
	name=$1
	shift
	t0=$(date +%s)
	inroot "$@"
	rc=$?
	echo "$name: exit $rc in $(($(date +%s) - t0)) s" | append "$R/times.txt"
	return "$rc"
}

# The selftest and pixel steps go on after a failed case (--keep-going: the
# Shell and its services start over for the next one), so that one failure
# leaves the rest of the run to report.
step_selftest() {
	ready
	own_link selftest
	results
	note_kit
	quiesce
	quiet
	# TLCP for the push link, as the image's boot selftest (phone-selftest.service).
	timed selftest "$R/selftest.log" PHONE_PUSH_TLCP=1 /usr/bin/phone-shell --cts "$RR/cts.json" \
		--junit "$RR/selftest-junit.xml" --timeout-scale "$SCALE" --keep-going ${KARIO_ONLY:+--only "$KARIO_ONLY"}
	rc=$?
	unquiet
	restore
	[ "$DRY" = 1 ] || summary "$R/selftest.log"
	return "$rc"
}

step_pixel() {
	ready
	own_link pixel
	results
	note_kit
	quiesce
	quiet
	worst=0
	for d in sku bar-t9 bar-qwerty communicator; do
		if [ "$d" = sku ]; then set --; else set -- --device "$d"; fi
		timed "pixel $d" "$R/pixel-$d.log" /usr/bin/phone-shell --selftest --gui auto "$@" --pixel-dir "$RR/pixel/$d" \
			--junit "$RR/pixel-$d-junit.xml" --timeout-scale "$SCALE" --keep-going ${KARIO_ONLY:+--only "$KARIO_ONLY"} || worst=1
		[ "$DRY" = 1 ] || summary "$R/pixel-$d.log"
	done
	unquiet
	restore
	return "$worst"
}

step_bench() {
	ready
	results
	note_kit
	if [ "$PLAN" = 0 ] && [ ! -x "$ROOT/usr/bin/python3" ]; then
		echo "skipped: the image has no python3; the benchmark needs the web image (make yocto-web-build)" | put "$R/bench.log"
		return 0
	fi
	own_link benchmark
	quiesce
	b=/usr/share/phone/bench/bench.py
	timed bench "$R/bench.log" /usr/bin/python3 "$b" tui --name phone-shell --runs 3 --timeout 120 --cmd 'phone-shell --services spawn --sim-dir {tmp}'
	if [ "$PLAN" = 1 ] || [ -x "$ROOT/usr/bin/cog" ]; then
		timed "bench web" "$R/bench-web.log" /usr/bin/python3 "$b" web --name wpe --root /usr/share/phone/web --runs 3 --timeout 180 --cmd 'cog -P headless {url}'
	fi
	restore
}

# perf: the gate's run (39-oneplus-6t.md §8.1). The Shell on the panel,
# started afresh for each run (the apps start cold), its renderer playing
# the kit's fixed sequence (perf.play, PHONE_PAINT_PLAY) as keys and touches
# and writing its numbers when it ends (paint-stats.json, kept as
# perf-<run>.json); then one table of the runs against the gate (perf.txt,
# phone-paint --stats). A run is <gpu>-<size>: cpu, compose (PHONE_PAINT_GPU=1)
# or raster, at the panel's size (full) or the product's in its middle
# (target, PHONE_PAINT_TARGET: KARIO_PERF_TARGET, 480x640). KARIO_PERF names
# the runs (all six), KARIO_PERF_WAIT the seconds one may take (600). On the
# whole phone the Shell is phone-display with a drop-in under /run for the
# run's environment, taken out after; in the chroot it is started as
# shell-start starts it.
PERF_RUNS="cpu-full cpu-target compose-full compose-target raster-full raster-target"
PERF_DROPIN=$ROOT/run/systemd/system/phone-display.service.d/kario-perf.conf
step_perf() {
	ready
	results
	note_kit
	[ "$PLAN" = 1 ] || [ -f "$KIT/perf.play" ] || die "no $KIT/perf.play: push the kit again"
	if [ "$TARGET" = chroot ] && [ "$PLAN" = 0 ] && [ -n "$(ours)" ]; then
		die "something runs in the chroot already (shell-stop first)"
	fi
	run cp "$KIT/perf.play" "$R/perf.play"
	was=inactive
	if [ "$TARGET" = whole ] && service_running phone-display.service; then was=active; fi
	worst=0
	files=
	for r in ${KARIO_PERF:-$PERF_RUNS}; do
		case ${r%-*} in
		cpu) gpu=0 ;;
		compose) gpu=1 ;;
		raster) gpu=raster ;;
		*) die "perf: $r: the GPU path is cpu, compose or raster" ;;
		esac
		case ${r#*-} in
		full) size= ;;
		target) size=${KARIO_PERF_TARGET:-480x640} ;;
		*) die "perf: $r: the size is full or target" ;;
		esac
		perf_run "$r" "PHONE_PAINT_GPU=$gpu PHONE_PAINT_TARGET=$size PHONE_PAINT_PLAY=$RR/perf.play PHONE_PAINT_TRACE=1" || worst=1
		if [ "$PLAN" = 1 ] || [ -f "$R/perf-$r.json" ]; then files="$files $RR/perf-$r.json"; fi
	done
	if [ "$TARGET" = whole ]; then
		run rm -f "$PERF_DROPIN"
		run systemctl daemon-reload
		if [ "$was" = active ]; then
			run systemctl restart phone-display.service
		fi
	fi
	[ -n "$files" ] || {
		echo "perf: no run left its numbers" | put "$R/perf.txt"
		return 1
	}
	# The renderer's own table: inroot runs it from the system (ROOT on
	# the whole phone) or in the chroot.
	paint=/usr/lib/phone/bin/phone-paint
	if [ "$TARGET" = whole ]; then paint=$ROOT$paint; fi
	# shellcheck disable=SC2086 # a file an argument
	timed "perf table" "$R/perf.txt" "$paint" --stats $files || worst=1
	[ "$DRY" = 1 ] || cat "$R/perf.txt"
	return "$worst"
}

# perf_run RUN ENV: one run, the Shell's renderer in ENV; its numbers to
# perf-RUN.json, the Shell's output to perf-RUN.log.
perf_run() {
	name=$1
	envs=$2
	if [ "$TARGET" = whole ]; then
		stats=$ROOT/run/phone/paint-stats.json
		run mkdir -p "${PERF_DROPIN%/*}"
		[ "$DRY" = 0 ] || echo "phone# (the drop-in: Environment=$envs)"
		printf '[Service]\nEnvironment=%s\n' "$envs" | put "$PERF_DROPIN"
		run systemctl daemon-reload
		run systemctl stop phone-display.service
		settle phone-display.service
		run rm -f "$stats"
		t0=$(date +%s)
		run systemctl start phone-display.service
	else
		# The chroot's /root is the kit's state/home (setup's binds).
		stats=$BASE/state/home/.local/state/phone-sim/run/paint-stats.json
		run rm -f "$stats"
		# shellcheck disable=SC2086 # a word for each variable
		set -- sh "$SELF" enter $envs /usr/bin/phone-shell --services spawn --gui auto --stay-on --sim-dir /root/.local/state/phone-sim
		# shellcheck disable=SC2086 # the scope's words
		if scope; then set -- $SCOPE "$@"; fi
		if [ "$DRY" = 1 ]; then
			echo "phone# setsid $* > $R/perf-$name.log &"
		else
			setsid "$@" > "$R/perf-$name.log" 2>&1 < /dev/null &
			echo $! > "$STATE/shell.pid"
		fi
	fi
	if [ "$PLAN" = 1 ]; then
		echo "phone# (up to ${KARIO_PERF_WAIT:-600} s until $stats says the sequence ended: \"state\": \"done\" or \"failed\") ; cp $stats $R/perf-$name.json"
	else
		i=0
		while [ "$i" -lt "${KARIO_PERF_WAIT:-600}" ] && ! grep -q -E '"state": "(done|failed)"' "$stats" 2> /dev/null; do
			if [ "$TARGET" = whole ] && systemctl is-failed -q phone-display.service; then break; fi
			if [ "$TARGET" = chroot ] && ! kill -0 "$(cat "$STATE/shell.pid")" 2> /dev/null; then break; fi
			sleep 2
			i=$((i + 2))
		done
		if [ -f "$stats" ]; then cp "$stats" "$R/perf-$name.json"; fi
		echo "perf $name: $i s" | append "$R/times.txt"
	fi
	if [ "$TARGET" = whole ]; then
		if [ "$PLAN" = 1 ]; then
			echo "phone# journalctl -u phone-display --since <the run's start> > $R/perf-$name.log"
		else
			journalctl -b -u phone-display --since "@$t0" --no-pager > "$R/perf-$name.log" 2>&1
		fi
	else
		stop_ours
		run rm -f "$STATE/shell.pid"
	fi
	if [ "$PLAN" = 0 ] && ! grep -q -E '"state": "done"' "$R/perf-$name.json" 2> /dev/null; then
		say "perf $name: the sequence did not get to its end (perf-$name.json, perf-$name.log)"
		return 1
	fi
}

# scope: whether a detached step also goes into a systemd scope of its own,
# outside the SSH login's: logind ending the login (KillUserProcesses=yes)
# kills what is left in its scope, setsid or not. On the whole phone the
# scope is delegated, as the boot selftest's unit is (Delegate=yes): the
# Shell gives each app a group of its own in it.
scope() {
	SCOPE="systemd-run --scope --quiet --collect --no-ask-password"
	if [ "$TARGET" = whole ]; then SCOPE="$SCOPE -p Delegate=yes"; fi
	[ "$(init)" = systemd ] && have systemd-run || return 1
	[ "$PLAN" = 1 ] || systemd-run --scope --quiet --collect --no-ask-password true > /dev/null 2>&1
}

# shell-start [--stay-on] [--no-haptics]: the bring-up switches, handed to
# the Shell (phone-shell's own).
step_shell_start() {
	whole_has_its_shell
	ready
	results
	note_kit
	for a in "$@"; do
		case $a in
		--stay-on | --no-haptics) ;;
		*) die "shell-start: $a: the switches are --stay-on and --no-haptics" ;;
		esac
	done
	switches=$*
	if [ "$PLAN" = 0 ] && [ -n "$(ours)" ]; then
		die "something runs in the chroot already (shell-stop first)"
	fi
	date +%s | put "$R/shell.start"
	# logind would act on the power key (power off, suspend) and the Hall
	# sensor; the Shell handles them.
	if have elogind-inhibit; then
		set -- elogind-inhibit --what=handle-power-key:handle-suspend-key:handle-lid-switch --who=Kario --why="the Shell handles them" --mode=block
	elif [ "$(init)" = systemd ] && have systemd-inhibit; then
		set -- systemd-inhibit --what=handle-power-key:handle-suspend-key:handle-lid-switch --who=Kario --why="the Shell handles them" --mode=block
	else
		set --
	fi
	# shellcheck disable=SC2086 # the scope's words
	if scope; then set -- $SCOPE "$@"; fi
	# shellcheck disable=SC2086 # a word for each switch
	set -- "$@" sh "$SELF" enter PHONE_PAINT_TRACE=1 /usr/bin/phone-shell --services spawn --gui auto $switches \
		--sim-dir /root/.local/state/phone-sim
	echo "$*" | put "$R/shell.args"
	if [ "$DRY" = 1 ]; then
		echo "phone# setsid $* > $R/shell.log &"
		return 0
	fi
	setsid "$@" > "$R/shell.log" 2>&1 < /dev/null &
	echo $! > "$STATE/shell.pid"
	i=0
	while [ "$i" -lt 90 ] && ! grep -q PHONE-GUI-READY "$R/shell.log"; do
		sleep 1
		i=$((i + 1))
	done
	if grep -q PHONE-GUI-READY "$R/shell.log"; then
		say "the Shell is up after $i s: $(grep -a PHONE-GUI-READY "$R/shell.log")"
		echo "shell: up after $i s" | append "$R/times.txt"
	else
		say "the Shell is not up after $i s; see $R/shell.log"
		tail -20 "$R/shell.log"
		return 1
	fi
}

# The whole phone's Shell is phone-display.service, on the panel already.
whole_has_its_shell() {
	[ "$TARGET" = whole ] || return 0
	die "the whole phone's Shell is phone-display.service, on the panel already: systemctl stop or start it; its environment (PHONE_PAINT_TRACE=1) goes into a drop-in under /run (39-oneplus-6t.md §14.1), its log is journalctl -u phone-display"
}

step_shell_stop() {
	whole_has_its_shell
	results
	stop_ours
	run rm -f "$STATE/shell.pid"
	# The Shell's own services log into its run directory, in /root.
	run mkdir -p "$R/shell-logs"
	for f in "$BASE"/state/home/.local/state/phone-sim/run/*.log; do
		if [ -f "$f" ]; then run cp "$f" "$R/shell-logs/"; fi
	done
	[ "$DRY" = 1 ] || say "stopped; logs in $R/shell-logs"
}

step_mem() {
	results
	if [ "$PLAN" = 1 ]; then
		echo "phone# (PSS of each of our processes, 60 s after shell-start) > $R/mem-<time>.txt"
		return 0
	fi
	since=$(cat "$R/shell.start" 2> /dev/null || echo 0)
	wait=$((since + 60 - $(date +%s)))
	[ "$wait" -le 0 ] || sleep "$wait"
	out=$R/mem-$(date +%H%M%S).txt
	{
		grep -E 'MemTotal|MemAvailable' /proc/meminfo
		for p in $(ours); do
			pss=$(awk '/^Pss:/ { print $2 }' "/proc/$p/smaps_rollup" 2> /dev/null)
			echo "$p $(cat "/proc/$p/comm" 2> /dev/null) ${pss:-0}"
		done | sort -k3 -n -r | awk '{ print; n++; kb += $3 } END { printf "%d processes, %.1f MB PSS\n", n, kb / 1024 }'
	} > "$out"
	cat "$out"
}

battery() {
	for d in /sys/class/power_supply/*; do
		if [ "$(cat "$d/type" 2> /dev/null)" = Battery ]; then
			echo "$d"
			return 0
		fi
	done
	return 1
}

val() { cat "$1" 2> /dev/null || printf '%s\n' -; }

# One line: time, capacity %, charge_now uAh, energy_now uWh, voltage_now uV,
# current_now uA, suspends so far, status (one word: "Not_charging").
snapshot() {
	b=$(battery) || return 1
	echo "$(date +%s) $(val "$b/capacity") $(val "$b/charge_now") $(val "$b/energy_now") $(val "$b/voltage_now") $(val "$b/current_now") $(val /sys/power/suspend_stats/success) $(val "$b/status" | tr ' ' _)"
}

step_power_start() {
	results
	if [ "$PLAN" = 1 ]; then
		echo "phone# (battery: time capacity charge_now energy_now voltage current suspends) > $STATE/power.start"
		return 0
	fi
	snapshot > "$STATE/power.start" || die "no battery (power supply of type Battery)"
	cp "$STATE/power.start" "$R/power.start"
	say "noted: $(cat "$STATE/power.start"); unplug USB, turn the screen off, come back in an hour or more"
}

step_power_stop() {
	results
	if [ "$PLAN" = 1 ]; then
		echo "phone# (average current, energy and suspends since power-start) > $R/power.txt"
		return 0
	fi
	[ -f "$STATE/power.start" ] || die "no power-start"
	now=$(snapshot) || die "no battery"
	{
		echo "start: $(cat "$STATE/power.start")"
		echo "stop:  $now"
		echo "$(cat "$STATE/power.start") $now" | awk '{
			h = ($9 - $1) / 3600
			printf "%.2f h, capacity %s%% -> %s%%, %d suspends\n", h, $2, $10, $15 - $7
			if (h > 0 && $3 != "-" && $11 != "-") printf "average current %.1f mA (charge)\n", ($3 - $11) / 1000 / h
			if (h > 0 && $4 != "-" && $12 != "-") printf "average power %.1f mW (energy)\n", ($4 - $12) / 1000 / h
		}'
		if [ -r /sys/kernel/debug/wakeup_sources ]; then
			echo "wake-up sources by wakeup_count:"
			sort -k4 -n -r /sys/kernel/debug/wakeup_sources | head -12
		fi
	} | tee "$R/power.txt"
}

step_power_sample() {
	results
	n=${1:-60}
	if [ "$PLAN" = 1 ]; then
		echo "phone# (current_now and voltage_now every second for $n s) > $R/current-<time>.csv"
		return 0
	fi
	b=$(battery) || die "no battery"
	out=$R/current-$(date +%H%M%S).csv
	echo "t,current_ua,voltage_uv" > "$out"
	i=0
	while [ "$i" -lt "$n" ]; do
		echo "$i,$(val "$b/current_now"),$(val "$b/voltage_now")" >> "$out"
		sleep 1
		i=$((i + 1))
	done
	awk -F, 'NR > 1 && $2 != "-" { s += $2; n++ } END { if (n) printf "average current_now %.1f mA over %d s\n", s / n / 1000, n }' "$out" | tee -a "$out.txt"
}

# bg STEP: the step as a job (--job), which notes its own pid when it
# starts: setsid forks when it is handed a process group's leader, and $!
# is then a process that is gone at once.
step_bg() {
	[ $# -ge 1 ] || die "bg <step>"
	results
	job=$1
	shift
	set -- sh "$SELF" --job "$job" "$@"
	# shellcheck disable=SC2086 # the scope's words
	if scope; then set -- $SCOPE "$@"; fi
	# Without setsid the job stays in the login's process group, whose
	# hang-up it notes and outlives (--job).
	if have setsid; then set -- setsid "$@"; fi
	if [ "$DRY" = 1 ]; then
		echo "phone# $* > $R/$job.out &"
		return 0
	fi
	rm -f "$R/$job.rc"
	"$@" > "$R/$job.out" 2>&1 < /dev/null &
	echo $! > "$STATE/$job.pid"
	say "$job running (pid $!), output in $R/$job.out"
}

# current_run: R is the run's results directory, empty without a run; status
# and log never start one.
current_run() {
	RUN=$(cat "$STATE/run" 2> /dev/null || true)
	R=
	if [ -n "$RUN" ]; then R=$RESULTS/$RUN; fi
}

step_log() {
	if [ "$PLAN" = 1 ]; then
		echo "phone# tail -n 40 $RESULTS/<run>/$1.out"
		return 0
	fi
	current_run
	[ -n "$R" ] || die "no run ($STATE/run is gone)"
	tail -n 40 "$R/$1.out" 2> /dev/null
}

# alive PID: the process runs (a zombie has ended).
alive() {
	s=$(sed 's/.*) //' "/proc/$1/stat" 2> /dev/null | cut -d ' ' -f 1)
	[ -n "$s" ] && [ "$s" != Z ]
}

# job_alive STEP: its pid files name a running process, or a process runs
# it as a job.
job_alive() {
	for f in "$STATE/$1.pid" "${R:-/nonexistent}/$1.pid"; do
		p=$(cat "$f" 2> /dev/null) || continue
		if [ -n "$p" ] && alive "$p"; then return 0; fi
	done
	for p in /proc/[0-9]*; do
		case "$(tr '\0' ' ' < "$p/cmdline" 2> /dev/null) " in
		*"on-phone.sh --job $1 "*) alive "${p#/proc/}" && return 0 ;;
		esac
	done
	return 1
}

# gone_why: what a job that left no exit code left behind, for the report.
gone_why() {
	echo "what the phone shows ($(init), $TARGET):"
	ls -la "$STATE" 2>&1
	echo "$STATE/run: $(cat "$STATE/run" 2>&1)"
	for f in "$STATE"/*.pid "${R:-/nonexistent}"/*.pid; do
		[ -f "$f" ] || continue
		p=$(cat "$f")
		if alive "$p"; then s=running; elif [ -d "/proc/$p" ]; then s="a zombie"; else s="no /proc/$p"; fi
		echo "$f: $p ($s)"
	done
	for f in "${R:-/nonexistent}"/*.signals; do
		if [ -f "$f" ]; then echo "$f: $(tr '\n' ' ' < "$f")"; fi
	done
	echo "results, newest first:"
	# shellcheck disable=SC2012 # the runs' names are dates
	ls -t "$RESULTS" 2>&1 | head -n 8
	echo "processes (pid ppid pgid sid, cgroup, command):"
	for p in /proc/[0-9]*; do
		c=$(tr '\0' ' ' < "$p/cmdline" 2> /dev/null)
		case $c in
		*on-phone.sh* | *phone-shell*)
			echo "${p#/proc/} $(sed 's/.*) //' "$p/stat" 2> /dev/null | cut -d ' ' -f 2-4), $(tr '\n' ' ' < "$p/cgroup" 2> /dev/null), $c"
			;;
		esac
	done
	if have busctl; then
		echo "logind KillUserProcesses: $(busctl get-property org.freedesktop.login1 /org/freedesktop/login1 org.freedesktop.login1.Manager KillUserProcesses 2>&1)"
	fi
}

step_status() {
	[ $# -ge 1 ] || die "status <step>"
	if [ "$PLAN" = 1 ]; then
		echo "phone# running, done <exit code>, or gone and what the phone shows (the state, each pid file, the results, the processes)"
		return 0
	fi
	current_run
	if [ -n "$R" ] && [ -f "$R/$1.rc" ]; then
		echo "done $(cat "$R/$1.rc")"
	elif job_alive "$1"; then
		echo running
	else
		echo gone
		gone_why
	fi
}

remove_gadget() {
	g=/sys/kernel/config/usb_gadget/kario
	if [ "$PLAN" = 1 ]; then
		echo "phone# remove $g if a Shell made it and it is not bound"
		return 0
	fi
	[ -d "$g" ] || return 0
	if [ -n "$(cat "$g/UDC" 2> /dev/null)" ]; then
		say "$g is bound to a controller: left as it is"
		return 0
	fi
	for l in "$g"/configs/*/* "$g"/os_desc/*; do
		if [ -L "$l" ]; then run rm -f "$l"; fi
	done
	for d in "$g"/configs/*/strings/* "$g"/configs/* "$g"/functions/* "$g"/strings/* "$g"; do
		if [ -d "$d" ]; then run rmdir "$d" 2> /dev/null; fi
	done
}

step_teardown() {
	stop_ours
	unquiet
	if [ "$TARGET" = whole ]; then
		restore
		run rm -rf "$STATE"
		if [ "${1:-}" = --purge ]; then run rm -rf "$BASE"; fi
		[ "$DRY" = 1 ] || say "torn down; Kario runs as before"
		return 0
	fi
	ucm_teardown
	firmware_teardown
	# What a Shell leaves in Nura's kernel: its nftables table, a USB gadget,
	# networkd files in Nura's /run.
	if have nft && { [ "$PLAN" = 1 ] || nft list table inet phone > /dev/null 2>&1; }; then
		run nft delete table inet phone
	fi
	remove_gadget
	for f in /run/systemd/network/*kario*; do
		if [ -e "$f" ]; then run rm -f "$f"; fi
	done
	if [ "$PLAN" = 1 ]; then
		echo "phone# umount every mount under $ROOT, the deepest first, then $ROOT"
	else
		awk -v r="$ROOT" '$2 == r || index($2, r "/") == 1 { print $2 }' /proc/mounts | sort -r | while read -r m; do
			run umount "$m" 2> /dev/null || run umount -l "$m"
		done
	fi
	if [ "$PLAN" = 1 ] || [ -d "$CG" ]; then
		if [ "$PLAN" = 1 ]; then
			echo "phone# rmdir $CG and its groups"
		else
			find "$CG" -depth -type d | while read -r d; do run rmdir "$d"; done
		fi
	fi
	if [ "$PLAN" = 1 ]; then
		echo "phone# chvt <the console setup left>; start each service setup stopped ($STATE/stopped)"
	fi
	if [ -s "$STATE/vt" ]; then run chvt "$(cat "$STATE/vt")"; fi
	if [ -f "$STATE/stopped" ]; then
		while read -r s; do service start "$s"; done < "$STATE/stopped"
	fi
	if [ -f "$STATE/killed" ]; then
		say "ended at setup, back at the next login (or reboot): $(tr '\n' ' ' < "$STATE/killed")"
	fi
	run rm -rf "$STATE"
	if [ "${1:-}" = --purge ]; then
		run rm -rf "$BASE"
	fi
	[ "$DRY" = 1 ] || say "torn down; Nura runs as before (a reboot restores the rest: the sound card's other mixer controls, the console)"
}

usage() {
	sed -n '2,/^set -u/p' "$0" | sed '$d; s/^# \{0,1\}//' >&2
	exit 2
}

SELF=$0
JOB=0

while [ $# -gt 0 ]; do
	case $1 in
	--dry-run) DRY=1 ;;
	--plan) DRY=1 PLAN=1 ;;
	--force) KARIO_FORCE=1 ;;
	--job) JOB=1 ;;
	--env)
		[ $# -ge 2 ] || usage
		ENV=$2
		shift
		;;
	*) break ;;
	esac
	shift
done
if [ -f "$ENV" ]; then
	# shellcheck source=/dev/null
	. "$ENV"
fi
# The plan runs from the build machine's copy; it shows the phone's.
if [ "$PLAN" = 1 ]; then SELF=$KIT/on-phone.sh; fi
: "${KARIO_SKU:=unknown}" "${KARIO_BOARD:=unknown}" "${KARIO_COMPATIBLE:=unknown}" "${KARIO_UNVERIFIED:=}"
[ $# -ge 1 ] || usage
step=$1
shift
if [ "$JOB" = 1 ]; then
	echo $$ > "$STATE/$step.pid"
	results
	echo $$ > "$R/$step.pid"
	# The exit code also when the step dies; a hang-up or a stop is noted
	# and the step goes on (stopping ours is teardown's).
	trap 'echo $? > "$R/$step.rc"' EXIT
	trap 'echo "$(date -u +%T) hang-up" >> "$R/$step.signals"' HUP
	trap 'echo "$(date -u +%T) terminate" >> "$R/$step.signals"' TERM
fi
case $step in
check | facts | setup | selftest | pixel | bench | perf | mem | teardown | bg | status | log | enter | scrub | partitions)
	"step_$step" "$@"
	;;
shell-start | shell-stop | power-start | power-stop | power-sample)
	"step_$(echo "$step" | tr - _)" "$@"
	;;
*) usage ;;
esac
rc=$?
case $step in
selftest | pixel | bench | perf)
	echo "$rc" | put "$R/$step.rc"
	;;
esac
exit $rc
