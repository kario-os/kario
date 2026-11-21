#!/bin/sh
# Runs as root on a phone with Nura: our image's root file system in a chroot
# on Nura's kernel, panel, modem and sound (24-sku-framework.md §6.2).
# kario-phone.sh copies it to the phone with the SKU's files and calls it.
#
#   on-phone.sh [--dry-run | --plan] [--force] [--env FILE] <step> [args]
#
#   check              is this the SKU's board, can it run the chroot (reads only)
#   facts              what the phone has, for the board's unverified values
#   setup              mount the image, bind /dev /proc /sys /run, stop what competes
#   selftest           the compatibility run (selftest and CTS), text only
#   pixel              the selftest drawn by phone-paint, headless: the SKU's
#                      panel and the three form factors
#   bench              the semantic-vs-web benchmark (an image with python3)
#   shell-start        the Shell on the panel, keys from the keyboards, the modem
#                      through ModemManager; shell-stop ends it
#   mem                the memory (PSS) of each of our processes
#   power-start, power-stop   the battery before and after a standby period
#   power-sample [s]   the current every second for s seconds (60)
#   bg <step>          run a step detached; status <step>: running, done <rc>;
#                      log <step>: the end of its output
#   teardown [--purge] stop ours, unmount, start what setup stopped
#
# --dry-run prints what would change instead of changing it; --plan does so
# without looking at the machine (the build machine's dry run). Nothing of
# this has run on a phone yet.
# The step_* functions are called by name ("$@"), which shellcheck cannot see.
# shellcheck disable=SC2317,SC2329
set -u

BASE=${KARIO_BASE:-/var/lib/kario-chroot}
ROOT=$BASE/root
IMG=$BASE/rootfs.ext4
KIT=$BASE/kit
RESULTS=$BASE/results
STATE=/run/kario-chroot
CG=/sys/fs/cgroup/kario
SCALE=${KARIO_TIMEOUT_SCALE:-2}
# Nura's services that would compete for the panel, the sound card, the
# power key or the modem's messages (each stopped only if it runs), and the
# session daemons that do (killed; they come back with the next login).
# ModemManager, rmtfs, pd-mapper, tqftpserv, D-Bus, NetworkManager,
# bluetoothd and sshd keep running: our telephonyd is ModemManager's client.
STOP=${KARIO_STOP:-"tinydm greetd sddm lightdm gdm"}
KILL=${KARIO_KILL:-"pipewire wireplumber pipewire-pulse pulseaudio callaudiod feedbackd chatty gnome-calls mmsdtng"}
UCM_VERB=${KARIO_UCM_VERB:-HiFi}
# A string only a Shell with whole-phone support has (shell/usb.go): older
# ones take the USB controller from Nura's USB network and cut it off.
MARK="belongs to gadget"

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

# The probes: under --plan they answer so that every action shows.
have() { [ "$PLAN" = 1 ] || command -v "$1" > /dev/null 2>&1; }
mounted() { [ "$PLAN" = 0 ] && awk -v m="$1" '$2 == m { f = 1 } END { exit !f }' /proc/mounts; }
init() {
	if [ "$PLAN" = 0 ] && [ -d /run/systemd/system ]; then echo systemd; else echo openrc; fi
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

# The run's results directory, R on the phone and RR in the chroot.
results() {
	[ -n "${R:-}" ] && return 0
	if [ "$PLAN" = 1 ]; then
		R=$RESULTS/'<run>'
		RR=/var/lib/kario-results/'<run>'
		return 0
	fi
	RUN=$(cat "$STATE/run" 2> /dev/null || true)
	if [ -z "$RUN" ]; then
		RUN=$(date +%Y%m%d-%H%M%S)
		run mkdir -p "$STATE"
		echo "$RUN" | put "$STATE/run"
	fi
	R=$RESULTS/$RUN
	RR=/var/lib/kario-results/$RUN
	run mkdir -p "$R"
}

ready() {
	[ "$PLAN" = 1 ] || mounted "$ROOT" || die "the chroot is not set up (setup first)"
}

# enter CMD...: this process becomes CMD in the chroot, with a clean
# environment (Nura's names its own paths), in our cgroup's shell group when
# there is one: the Shell takes a delegated group it starts in <group>/shell
# (shell/cgroup.go), and the group itself may hold no process once a Shell
# has given it controllers for its children.
step_enter() {
	if [ -w "$CG/shell/cgroup.procs" ]; then
		echo 0 > "$CG/shell/cgroup.procs"
	fi
	exec chroot "$ROOT" /usr/bin/env -i HOME=/root PATH=/usr/sbin:/usr/bin:/sbin:/bin TERM=linux LANG=C.UTF-8 "$@"
}

# inroot LOG CMD...: CMD in the chroot, its output into LOG.
inroot() {
	log=$1
	shift
	if [ "$DRY" = 1 ]; then
		echo "phone# chroot $ROOT $* > $log"
		return 0
	fi
	sh "$SELF" enter "$@" > "$log" 2>&1
}

# Our processes: those whose root is the chroot, everything in our cgroup,
# and their descendants (a sandboxed app has a root of its own).
ours() {
	{
		for p in /proc/[0-9]*; do
			case $(readlink "$p/root" 2> /dev/null) in
			"$ROOT" | "$ROOT"/*) echo "${p#/proc/}" ;;
			esac
		done
		if [ -d "$CG" ]; then
			find "$CG" -name cgroup.procs -exec cat {} \; 2> /dev/null
		fi
	} | descendants
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

stop_ours() {
	if [ "$PLAN" = 1 ]; then
		echo "phone# kill -TERM <our processes>; after 10 s kill -KILL what is left"
		return 0
	fi
	pids=$(ours)
	[ -n "$pids" ] || return 0
	# shellcheck disable=SC2086 # one argument per process
	run kill -TERM $pids 2> /dev/null
	i=0
	while [ "$i" -lt 10 ] && [ -n "$(ours)" ]; do
		sleep 1
		i=$((i + 1))
	done
	pids=$(ours)
	# shellcheck disable=SC2086
	[ -z "$pids" ] || run kill -KILL $pids 2> /dev/null
}

step_check() {
	if [ "$PLAN" = 1 ]; then
		say "check: root, aarch64, device tree compatible $KARIO_COMPATIBLE, loop devices"
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
	[ -e /dev/loop-control ] || [ -e /dev/loop0 ] || {
		say "check: no loop devices"
		bad=1
	}
	[ "$bad" = 0 ] && say "check: $KARIO_SKU on $(tr -d '\0' 2> /dev/null < /proc/device-tree/model)"
	return "$bad"
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
			[ -r "$d/$f" ] && printf ' %s=%s' "$f" "$(tr '\n' ' ' < "$d/$f" | sed 's/ *$//')"
		done
		echo
	done
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

f_input() {
	for e in /sys/class/input/event*; do
		d=$e/device
		echo "${e##*/}: $(cat "$d/name") | ev $(cat "$d/capabilities/ev") | key $(cat "$d/capabilities/key") | abs $(cat "$d/capabilities/abs") | ff $(cat "$d/capabilities/ff")"
	done
}

f_drm() {
	FIELDS="status enabled dpms modes" each /sys/class/drm/card*-*
	FIELDS="name" each /sys/class/drm/card*/device/driver
	if command -v modetest > /dev/null; then modetest -c; fi
}

f_sleep() {
	FIELDS="state mem_sleep wakeup_count" each /sys/power
	FIELDS="success fail" each /sys/power/suspend_stats
	FIELDS="name wakealarm" each /sys/class/rtc/rtc*
}

f_usb() {
	FIELDS="state current_speed" each /sys/class/udc/*
	FIELDS="UDC" each /sys/kernel/config/usb_gadget/*
	ip -br addr 2> /dev/null || ip addr
}

f_sound() {
	cat /proc/asound/cards
	if command -v alsaucm > /dev/null; then
		alsaucm -c hw:0 list _verbs
		alsaucm -c hw:0 list "_devices/$UCM_VERB"
	fi
}

f_backlight() { FIELDS="type max_brightness brightness" each /sys/class/backlight/*; }
f_leds() { FIELDS="max_brightness multi_index trigger" each /sys/class/leds/*; }
f_power() { FIELDS="type status capacity voltage_now current_now charge_now charge_full charge_counter usb_type" each /sys/class/power_supply/*; }
f_iio() { FIELDS="name" each /sys/bus/iio/devices/*; }
f_rfkill() { FIELDS="type name state" each /sys/class/rfkill/*; }
f_dmesg() { dmesg | grep -iE 'panel|dsi|mdss|remoteproc|q6v5|rmtfs|ipa|wcn|bluetooth|firmware|haptic|touch|rmi4|edt|goodix' | tail -120; }

f_modem() {
	for p in ModemManager rmtfs pd-mapper tqftpserv qrtr-ns hexagonrpcd; do
		echo "$p: $(pidof "$p" || echo none)"
	done
	if command -v mmcli > /dev/null; then
		mmcli -L
		mmcli -m any
		mmcli -m any --location-status
	fi
}

f_services() {
	case $(init) in
	systemd) systemctl list-units --type=service --state=running --no-pager ;;
	*) rc-status -a ;;
	esac
}

f_image() {
	if ! mounted "$ROOT"; then
		echo "not mounted"
	elif grep -q "$MARK" "$ROOT/usr/bin/kario-system"; then
		echo "the image's Shell has whole-phone support"
	else
		echo "the image's Shell predates whole-phone support: rebuild the image"
	fi
	cat "$ROOT/usr/share/phone/sku.json" 2> /dev/null
}

# Where the facts above answer each value the board marks unverified.
f_unverified() {
	for u in $KARIO_UNVERIFIED; do
		case $u in
		gnss) w="== modem (location status); then an app asking for the position" ;;
		leds.*) w="== leds" ;;
		display.inset) w="look at the panel: the status bar clear of the notch, the soft keys of the corners" ;;
		display.panel | display.touchscreen) w="== dmesg (the drivers that probed), == input" ;;
		display.width_mm | display.height_mm) w="== drm (modetest), or measure the panel" ;;
		input.*) w="== input (names with key bits)" ;;
		*) w="" ;;
		esac
		echo "$u: $w"
	done
}

step_facts() {
	results
	if [ "$DRY" = 1 ]; then
		echo "phone# (system, kernel, input, drm, backlight, leds, power supplies, iio, sleep, usb, rfkill, sound, modem, services, image, dmesg) > $R/facts.txt"
		return 0
	fi
	{
		echo "facts of $KARIO_SKU ($KARIO_BOARD), $(date)"
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
		fact usb f_usb
		fact rfkill f_rfkill
		fact sound f_sound
		fact modem f_modem
		fact services f_services
		fact image f_image
		fact dmesg f_dmesg
	} > "$R/facts.txt" 2>&1
	say "facts in $R/facts.txt"
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
	results
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
	if [ "$PLAN" = 0 ] && ! pidof ModemManager > /dev/null; then
		say "ModemManager is not running: telephonyd needs it for the modem (start Nura's: rc-service modemmanager start)"
	fi
	mounted "$ROOT" || run mount -t ext4 -o loop "$IMG" "$ROOT"
	if [ "$PLAN" = 0 ] && ! grep -q "$MARK" "$ROOT/usr/bin/kario-system"; then
		msg="this image's Shell predates whole-phone support: it would take the USB controller and cut Nura's USB network off, and draw at the form factor's scale; rebuild the image from this tree (make yocto-build)"
		[ "${KARIO_FORCE:-0}" = 1 ] || die "$msg (--force to go on, over Wi-Fi)"
		say "$msg"
	fi
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
	if [ -n "$UCM_VERB" ] && have alsaucm; then
		run alsaucm -c hw:0 set _verb "$UCM_VERB" || say "no UCM verb $UCM_VERB on card 0: phone-audiod plays to the card as it is"
	fi
	step_facts
	[ "$DRY" = 1 ] || say "set up: $ROOT ($KARIO_SKU), results in $R"
}

summary() {
	grep -ah -E 'PHONE-SELFTEST-RESULT|PHONE-CTS:|PHONE-GUI-FRAMES|^(PASS|FAIL) ' "$@" 2> /dev/null | tail -20
}

# timed NAME LOG CMD...: CMD in the chroot, its seconds and exit code noted.
timed() {
	name=$1
	shift
	t0=$(date +%s)
	inroot "$@"
	rc=$?
	echo "$name: exit $rc in $(($(date +%s) - t0)) s" | append "$R/times.txt"
	return "$rc"
}

step_selftest() {
	ready
	results
	# TLCP for the push link, as the image's boot selftest (phone-selftest.service).
	timed selftest "$R/selftest.log" PHONE_PUSH_TLCP=1 /usr/bin/phone-shell --cts "$RR/cts.json" \
		--junit "$RR/selftest-junit.xml" --timeout-scale "$SCALE"
	rc=$?
	[ "$DRY" = 1 ] || summary "$R/selftest.log"
	return "$rc"
}

step_pixel() {
	ready
	results
	worst=0
	for d in sku bar-t9 bar-qwerty communicator; do
		if [ "$d" = sku ]; then set --; else set -- --device "$d"; fi
		timed "pixel $d" "$R/pixel-$d.log" /usr/bin/phone-shell --selftest --gui auto "$@" --pixel-dir "$RR/pixel/$d" \
			--junit "$RR/pixel-$d-junit.xml" --timeout-scale "$SCALE" || worst=1
		[ "$DRY" = 1 ] || summary "$R/pixel-$d.log"
	done
	return "$worst"
}

step_bench() {
	ready
	results
	if [ "$PLAN" = 0 ] && [ ! -x "$ROOT/usr/bin/python3" ]; then
		echo "skipped: the image has no python3; the benchmark needs the web image (make yocto-web-build)" | put "$R/bench.log"
		return 0
	fi
	b=/usr/share/phone/bench/bench.py
	timed bench "$R/bench.log" /usr/bin/python3 "$b" tui --name phone-shell --runs 3 --timeout 120 --cmd 'phone-shell --services spawn --sim-dir {tmp}'
	if [ "$PLAN" = 1 ] || [ -x "$ROOT/usr/bin/cog" ]; then
		timed "bench web" "$R/bench-web.log" /usr/bin/python3 "$b" web --name wpe --root /usr/share/phone/web --runs 3 --timeout 180 --cmd 'cog -P headless {url}'
	fi
}

step_shell_start() {
	ready
	results
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
	if [ "$DRY" = 1 ]; then
		echo "phone# setsid $* chroot $ROOT PHONE_PAINT_TRACE=1 phone-shell --services spawn --gui auto > $R/shell.log &"
		return 0
	fi
	setsid "$@" sh "$SELF" enter PHONE_PAINT_TRACE=1 /usr/bin/phone-shell --services spawn --gui auto \
		--sim-dir /root/.local/state/phone-sim > "$R/shell.log" 2>&1 < /dev/null &
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

step_shell_stop() {
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

step_bg() {
	[ $# -ge 1 ] || die "bg <step>"
	results
	job=$1
	shift
	if [ "$DRY" = 1 ]; then
		echo "phone# setsid sh $SELF $job $* > $R/$job.out &"
		return 0
	fi
	rm -f "$R/$job.rc"
	setsid sh "$SELF" "$job" "$@" > "$R/$job.out" 2>&1 < /dev/null &
	echo $! > "$STATE/$job.pid"
	say "$job running (pid $!), output in $R/$job.out"
}

step_log() {
	results
	tail -n 40 "$R/$1.out" 2> /dev/null
}

step_status() {
	results
	if [ -f "$R/$1.rc" ]; then
		echo "done $(cat "$R/$1.rc")"
	elif [ -f "$STATE/$1.pid" ] && kill -0 "$(cat "$STATE/$1.pid")" 2> /dev/null; then
		echo running
	else
		echo gone
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
	[ "$DRY" = 1 ] || say "torn down; Nura runs as before (a reboot restores the rest: the sound card's mixer, the console)"
}

usage() {
	sed -n '2,/^set -u/p' "$0" | sed '$d; s/^# \{0,1\}//' >&2
	exit 2
}

SELF=$0

while [ $# -gt 0 ]; do
	case $1 in
	--dry-run) DRY=1 ;;
	--plan) DRY=1 PLAN=1 ;;
	--force) KARIO_FORCE=1 ;;
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
case $step in
check | facts | setup | selftest | pixel | bench | mem | teardown | bg | status | log | enter)
	"step_$step" "$@"
	;;
shell-start | shell-stop | power-start | power-stop | power-sample)
	"step_$(echo "$step" | tr - _)" "$@"
	;;
*) usage ;;
esac
rc=$?
case $step in
selftest | pixel | bench)
	echo "$rc" | put "$R/$step.rc"
	;;
esac
exit $rc
