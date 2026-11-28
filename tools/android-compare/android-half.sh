#!/bin/sh
# The Android half of the comparison with Kario (40-testing.md §9): the
# same tasks on an Android keypad phone (or a stripped Android on the same
# hardware as Kario's), each number from Android's own tools over adb, into
# a directory that compare.py reads. Nothing here needs root.
#
#   tools/android-compare/android-half.sh [options] <phase>...
#
# Phases:
#   facts            the phone: model, SoC, Android, patch level, A/B, kernel,
#                    RAM, CPUs, screen, the installed apps of the roles
#   idle             reboot, wait for the boot and --idle seconds, then the
#                    memory (mem-idle.txt)
#   start            each role's app started cold (force-stopped first) and
#                    warm (after Home), --reps times: am start -W
#   task NAME        one timed task, --reps times: gfxinfo reset, getevent
#                    (and with --perfetto a trace) while a person does it by
#                    keys, filmed at 240 fps; Enter here when it is done
#   tasks            task for each of: dial sms chat pay camera scroll switch
#   mem              the memory with the switch task's apps open (mem-apps.txt)
#   standby-start    battery and batterystats reset; then unplug, screen off
#   standby-stop     (plugged in again) the battery and batterystats
#   table            compare.py android: results-<side>.csv, then the table of
#                    every side in the directory above (table.md)
# With no phase: facts start mem table.
#
# Options:
#   --serial S       the phone (adb -s)
#   --side NAME      the table's column (android); --out DIR (build/android-compare/NAME)
#   --pkg ROLE=PKG   a role's app; roles phone sms messenger camera contacts
#                    payment (com.android.dialer com.android.mms com.tencent.mm
#                    com.android.camera2 com.android.contacts
#                    com.eg.android.AlipayGphone); facts lists what is installed
#   --reps N         runs of each task and start (5)
#   --pause S        between a start and the next step (3)
#   --idle S         idle after the reboot (600)
#   --perfetto       a Perfetto trace of each task run too (perfetto.cfg)
#   --dry-run        print what would run; run nothing, connect nowhere
set -u

here=$(cd "$(dirname "$0")" && pwd)
FW=$(cd "$here/../.." && pwd)
SERIAL=
SIDE=android
OUT=
REPS=5
PAUSE=3
IDLE=600
PERFETTO=0
DRY=0
ROLES="phone sms messenger camera contacts payment"
TASKS="dial sms chat pay camera scroll switch"
# shellcheck disable=SC2034 # read by pkg ROLE
PKG_phone=com.android.dialer PKG_sms=com.android.mms PKG_messenger=com.tencent.mm \
	PKG_camera=com.android.camera2 PKG_contacts=com.android.contacts PKG_payment=com.eg.android.AlipayGphone

say() { echo "android-half: $*"; }
die() {
	echo "android-half: $*" >&2
	exit 1
}
usage() {
	sed -n '2,/^set -u/p' "$0" | sed '$d; s/^# \{0,1\}//' >&2
	exit 2
}

while [ $# -gt 0 ]; do
	case $1 in
	--serial | --side | --out | --pkg | --reps | --pause | --idle)
		[ $# -ge 2 ] || usage
		case $1 in
		--serial) SERIAL=$2 ;;
		--side) SIDE=$2 ;;
		--out) OUT=$2 ;;
		--reps) REPS=$2 ;;
		--pause) PAUSE=$2 ;;
		--idle) IDLE=$2 ;;
		--pkg)
			role=${2%%=*}
			case " $ROLES " in *" $role "*) ;; *) die "--pkg $2: the roles are $ROLES" ;; esac
			eval "PKG_$role=\${2#*=}"
			;;
		esac
		shift
		;;
	--perfetto) PERFETTO=1 ;;
	--dry-run) DRY=1 ;;
	-h | --help) usage ;;
	-*) usage ;;
	*) break ;;
	esac
	shift
done
case $SIDE in *[!A-Za-z0-9._-]* | "") die "--side $SIDE: letters, digits, . _ -" ;; esac
case $REPS in "" | *[!0-9]*) usage ;; esac
case $PAUSE in "" | *[!0-9]*) usage ;; esac
case $IDLE in "" | *[!0-9]*) usage ;; esac
OUT=${OUT:-$FW/build/android-compare/$SIDE}
if [ $# -eq 0 ]; then set -- facts start mem table; fi
mkdir -p "$OUT" || die "cannot make $OUT"

pkg() { eval "printf '%s' \"\$PKG_$1\""; }

# adb ARGS: adb on the phone; a dry run prints it.
adb_() {
	if [ "$DRY" = 1 ]; then
		echo "+ adb${SERIAL:+ -s $SERIAL} $*" >&2
		return 0
	fi
	if [ -n "$SERIAL" ]; then adb -s "$SERIAL" "$@"; else adb "$@"; fi
}

# on CMDLINE: a command line on the phone, its output without the CRs old
# adb adds.
on() { adb_ shell "$*" | tr -d '\r'; }

prop() { on getprop "$1"; }

# wait_enter: a person says the run is over.
wait_enter() {
	if [ "$DRY" = 1 ]; then
		echo "+ (waits for Enter)" >&2
		return 0
	fi
	read -r _ || true
}

phase_facts() {
	{
		echo "model: $(prop ro.product.model)"
		echo "manufacturer: $(prop ro.product.manufacturer)"
		soc=$(prop ro.soc.model)
		[ -n "$soc" ] || soc=$(prop ro.board.platform)
		echo "soc: $soc"
		echo "android: $(prop ro.build.version.release)"
		echo "sdk: $(prop ro.build.version.sdk)"
		echo "patch: $(prop ro.build.version.security_patch)"
		echo "build: $(prop ro.build.display.id)"
		echo "ab: $(prop ro.build.ab_update)"
		echo "kernel: $(on uname -r)"
		echo "mem_kb: $(on "sed -n 's/^MemTotal: *\([0-9]*\) kB/\1/p' /proc/meminfo")"
		echo "cpus: $(on "grep -c ^processor /proc/cpuinfo")"
		echo "cpu_max_khz: $(on "cat /sys/devices/system/cpu/cpu*/cpufreq/cpuinfo_max_freq | sort -n | tail -n 1")"
		echo "screen: $(on wm size)"
		echo "packages of the roles (--pkg ROLE=PKG):"
		on pm list packages | grep -i -E 'dialer|contacts|mms|messag|camera|tencent\.mm|alipay' | sed 's/^/  /'
	} > "$OUT/facts.txt"
	cat "$OUT/facts.txt"
}

mem_snapshot() {
	{
		on cat /proc/meminfo
		on dumpsys meminfo
		if [ "$1" = apps ]; then
			for role in sms messenger contacts; do
				p=$(pkg "$role")
				pid=$(on pidof "$p")
				echo "alive $p ${pid:--}"
			done
		fi
	} > "$OUT/mem-$1.txt"
	grep -E '^(MemTotal|MemAvailable|alive)' "$OUT/mem-$1.txt"
}

phase_idle() {
	adb_ reboot
	adb_ wait-for-device
	if [ "$DRY" = 0 ]; then
		i=0
		while [ "$(prop sys.boot_completed)" != 1 ]; do
			[ "$i" -lt 300 ] || die "no boot in 300 s"
			sleep 2
			i=$((i + 2))
		done
	fi
	say "booted; $IDLE s idle (screen off, leave it alone)"
	[ "$DRY" = 1 ] || sleep "$IDLE"
	mem_snapshot idle
}

pause() { [ "$DRY" = 1 ] || sleep "$PAUSE"; }

# launcher PKG: the activity the launcher starts.
launcher() {
	c=$(on cmd package resolve-activity --brief "$1" | tail -n 1)
	case $c in */*) echo "$c" ;; *) echo "$1/<launcher activity>" ;; esac
}

phase_start() {
	for role in $ROLES; do
		p=$(pkg "$role")
		comp=$(launcher "$p")
		f=$OUT/start-$role.txt
		: > "$f"
		i=1
		while [ "$i" -le "$REPS" ]; do
			on am force-stop "$p"
			pause
			echo "== cold $i" >> "$f"
			on am start -W -n "$comp" >> "$f"
			pause
			on input keyevent KEYCODE_HOME
			pause
			echo "== warm $i" >> "$f"
			on am start -W -n "$comp" >> "$f"
			pause
			on input keyevent KEYCODE_HOME
			pause
			i=$((i + 1))
		done
		say "$role ($p): $(grep -c '^TotalTime' "$f") starts in $f"
	done
}

# The apps whose frames count: the roles', the home screen's, the system UI's.
frame_pkgs() {
	home=$(on cmd package resolve-activity --brief -a android.intent.action.MAIN -c android.intent.category.HOME | tail -n 1)
	{
		for r in $ROLES; do
			pkg "$r"
			echo
		done
		echo "${home%%/*}"
		echo com.android.systemui
	} | sed '/^$/d' | sort -u
}

phase_task() {
	name=$1
	case " $TASKS " in *" $name "*) ;; *) die "task $name: the tasks are $TASKS" ;; esac
	pkgs=$(frame_pkgs)
	i=1
	while [ "$i" -le "$REPS" ]; do
		for p in $pkgs; do on dumpsys gfxinfo "$p" reset > /dev/null; done
		keys=$OUT/keys-$name-$i.txt
		# A terminal on the phone (-tt): getevent's output line by line, none
		# of it left in a buffer when it is stopped.
		gev=
		if [ "$DRY" = 1 ]; then
			echo "+ adb${SERIAL:+ -s $SERIAL} shell -tt getevent -lt > $keys &" >&2
		elif [ -n "$SERIAL" ]; then
			adb -s "$SERIAL" shell -tt getevent -lt > "$keys" 2>&1 < /dev/null &
			gev=$!
		else
			adb shell -tt getevent -lt > "$keys" 2>&1 < /dev/null &
			gev=$!
		fi
		trace=/data/misc/perfetto-traces/kario-cmp-$name-$i.pftrace
		if [ "$PERFETTO" = 1 ]; then
			if [ "$DRY" = 1 ]; then
				echo "+ adb${SERIAL:+ -s $SERIAL} shell perfetto --background --txt -c - -o $trace < $here/perfetto.cfg" >&2
			else
				adb_ shell perfetto --background --txt -c - -o "$trace" < "$here/perfetto.cfg" > /dev/null
			fi
		fi
		say "task $name, run $i of $REPS: start the 240 fps video, do the task by keys only, stop the video, then press Enter here"
		wait_enter
		if [ -n "$gev" ]; then
			# The last key's release reaches the file first.
			sleep 1
			kill "$gev" 2> /dev/null
			wait "$gev" 2> /dev/null
		fi
		on "pkill -x getevent" > /dev/null
		for p in $pkgs; do on dumpsys gfxinfo "$p" framestats > "$OUT/gfx-$name-$i-$p.txt"; done
		if [ "$PERFETTO" = 1 ]; then
			on "pkill -TERM -x perfetto" > /dev/null
			[ "$DRY" = 1 ] || sleep 2
			adb_ pull "$trace" "$OUT/" > /dev/null
		fi
		n=$(grep -c 'EV_KEY.*DOWN' "$keys" 2> /dev/null || true)
		say "task $name, run $i: ${n:-0} key-down events"
		i=$((i + 1))
	done
}

battery_snapshot() {
	{
		echo "time: $(on date +%s)"
		on dumpsys battery
	} > "$OUT/battery-$1.txt"
	grep -E '^(time|  level|  Charge counter|  USB powered|  AC powered)' "$OUT/battery-$1.txt"
}

phase_standby_start() {
	on dumpsys batterystats --reset
	battery_snapshot start
	say "now: unplug USB, screen off, Wi-Fi off, the messenger logged in; traffic.sh running; come back in 24 h, plug in and run standby-stop at once"
}

phase_standby_stop() {
	battery_snapshot stop
	on dumpsys batterystats > "$OUT/batterystats.txt"
	say "batterystats in $OUT/batterystats.txt"
}

phase_table() {
	set -- python3 "$here/compare.py" android "$OUT" --out "$OUT/results-$SIDE.csv" --hand "$OUT/hand-$SIDE.csv"
	if [ "$DRY" = 1 ]; then
		echo "+ $*" >&2
		return 0
	fi
	"$@" || return 1
	top=$(dirname "$OUT")
	# shellcheck disable=SC2046 # a file an argument
	python3 "$here/compare.py" table "$here/results-template.csv" $(ls "$top"/*/results-*.csv) > "$top/table.md" &&
		say "table of every side in $top/table.md"
}

worst=0
while [ $# -gt 0 ]; do
	case $1 in
	facts) phase_facts || worst=1 ;;
	idle) phase_idle || worst=1 ;;
	start) phase_start || worst=1 ;;
	task)
		[ $# -ge 2 ] || usage
		phase_task "$2" || worst=1
		shift
		;;
	tasks)
		for t in $TASKS; do phase_task "$t" || worst=1; done
		;;
	mem) mem_snapshot apps || worst=1 ;;
	standby-start) phase_standby_start || worst=1 ;;
	standby-stop) phase_standby_stop || worst=1 ;;
	table) phase_table || worst=1 ;;
	*) usage ;;
	esac
	shift
done
exit $worst
