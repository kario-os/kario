#!/bin/sh
# The Kario half of the comparison with an Android keypad phone
# (40-testing.md §9), on a phone the kit tests (tools/phone-chroot,
# 39-oneplus-6t.md §13): its steps run the tasks and take the numbers,
# compare.py turns them into this side's rows and the table of every side.
# One command with no phase runs what needs no person and writes the table.
#
#   tools/android-compare/kario-half.sh --sku SKU [options] [phase...]
#
# Phases:
#   facts            the kit's facts (kernel, system, RAM, CPUs, battery)
#   perf             the kit's perf with tasks.play: the tasks' cold starts,
#                    frame times and key to screen, at 480x640 (the Android
#                    phone's screen) and the panel's size, CPU drawing
#   mem-idle         the memory after a reboot and 10 minutes idle (asks
#                    first, unless --yes): mem-idle.txt
#   task NAME        one timed task, --reps times: a dump, a person does the
#                    task by keys, filmed at 240 fps; Enter here; a dump.
#                    The keys and touches in between (tasks.log)
#   tasks            task for each of: dial sms chat pay camera scroll switch
#   mem-apps         the memory with the switch task's apps open, and a dump
#   standby-start    the kit's power-start; then unplug, screen off
#   standby-stop     (plugged in again) the kit's power-stop: power.txt
#   table            collect, compare.py kario: results-<side>.csv; the table
#                    of every side in the directory above (table.md)
# With no phase: facts perf mem-idle table.
#
# Options:
#   --sku SKU        the phone's SKU (oneplus-fajita-t9)
#   --whole          the phone flashed with Kario (the standby needs it)
#   --phone, --ssh-key, --become   the kit's (kario-phone.sh)
#   --side NAME      the table's column (kario; a hardware rung: kario-2g)
#   --out DIR        build/android-compare/NAME
#   --reps N         runs of each task (5)
#   --runs RUNS      the perf runs (cpu-target,cpu-full)
#   --yes            no questions (mem-idle)
#   --dry-run        the kit's dry run; nothing changes, no phone
set -u

here=$(cd "$(dirname "$0")" && pwd)
FW=$(cd "$here/../.." && pwd)
SKU=
SIDE=kario
OUT=
REPS=5
RUNS=cpu-target,cpu-full
YES=0
DRY=0
TASKS="dial sms chat pay camera scroll switch"
KIT_ARGS=

say() { echo "kario-half: $*"; }
die() {
	echo "kario-half: $*" >&2
	exit 1
}
usage() {
	sed -n '2,/^set -u/p' "$0" | sed '$d; s/^# \{0,1\}//' >&2
	exit 2
}

# quote WORD: WORD as one word for eval.
quote() { printf "'%s'" "$(printf '%s' "$1" | sed "s/'/'\\\\''/g")"; }

while [ $# -gt 0 ]; do
	case $1 in
	--sku | --phone | --ssh-key | --become | --side | --out | --reps | --runs)
		[ $# -ge 2 ] || usage
		case $1 in
		--sku) SKU=$2 ;;
		--side) SIDE=$2 ;;
		--out) OUT=$2 ;;
		--reps) REPS=$2 ;;
		--runs) RUNS=$2 ;;
		*) KIT_ARGS="$KIT_ARGS $1 $(quote "$2")" ;;
		esac
		shift
		;;
	--whole) KIT_ARGS="$KIT_ARGS --whole" ;;
	--yes) YES=1 ;;
	--dry-run)
		DRY=1
		KIT_ARGS="$KIT_ARGS --dry-run"
		;;
	-h | --help) usage ;;
	-*) usage ;;
	*) break ;;
	esac
	shift
done
[ -n "$SKU" ] || usage
case $SIDE in *[!A-Za-z0-9._-]* | "") die "--side $SIDE: letters, digits, . _ -" ;; esac
case $REPS in "" | *[!0-9]*) usage ;; esac
OUT=${OUT:-$FW/build/android-compare/$SIDE}
KIT_OUT=$OUT/kit
if [ $# -eq 0 ]; then set -- facts perf mem-idle table; fi
mkdir -p "$OUT" || die "cannot make $OUT"

# kit ARGS: a step of the kit, its results under $KIT_OUT.
kit() {
	eval "set -- $KIT_ARGS \"\$@\""
	sh "$FW/tools/phone-chroot/kario-phone.sh" --sku "$SKU" --out "$KIT_OUT" "$@"
}

ask() {
	if [ "$YES" = 1 ] || [ "$DRY" = 1 ]; then return 0; fi
	say "$1; Enter when it is"
	read -r _ || true
}

# dump: a dump on the phone; its time (HHMMSS), as the kit names it.
dump() {
	if [ "$DRY" = 1 ]; then
		kit dump >&2
		echo 000000
		return 0
	fi
	t=$(kit dump | sed -n 's/.*dump-\([0-9]\{6\}\)\.json.*/\1/p' | tail -n 1)
	[ -n "$t" ] || return 1
	echo "$t"
}

phase_task() {
	name=$1
	case " $TASKS " in *" $name "*) ;; *) die "task $name: the tasks are $TASKS" ;; esac
	i=1
	while [ "$i" -le "$REPS" ]; do
		t0=$(dump) || die "task $name: no dump from the phone (is the Shell running?)"
		say "task $name, run $i of $REPS: start the 240 fps video, do the task by keys only, stop the video, then press Enter here"
		[ "$DRY" = 1 ] || read -r _ || true
		t1=$(dump) || die "task $name: no dump from the phone"
		echo "task $name $i $t0 $t1" >> "$OUT/tasks.log"
		i=$((i + 1))
	done
}

# The kit's runs: a reboot (mem-idle's) starts another on the whole phone;
# compare.py takes each number from the newest run that has it.
phase_table() {
	kit collect || return 1
	if [ "$DRY" = 1 ]; then
		echo "+ python3 $here/compare.py kario $OUT $KIT_OUT/results/*/ --out $OUT/results-$SIDE.csv --hand $OUT/hand-$SIDE.csv" >&2
		return 0
	fi
	ls -d "$KIT_OUT"/results/*/ > /dev/null 2>&1 || die "no results in $KIT_OUT/results"
	python3 "$here/compare.py" kario "$OUT" "$KIT_OUT"/results/*/ --out "$OUT/results-$SIDE.csv" --hand "$OUT/hand-$SIDE.csv" || return 1
	top=$(dirname "$OUT")
	# shellcheck disable=SC2046 # a file an argument
	python3 "$here/compare.py" table "$here/results-template.csv" $(ls "$top"/*/results-*.csv) > "$top/table.md" &&
		say "table of every side in $top/table.md"
}

worst=0
while [ $# -gt 0 ]; do
	case $1 in
	facts) kit facts || worst=1 ;;
	perf) kit --play "$here/tasks.play" --perf "$RUNS" perf || worst=1 ;;
	mem-idle)
		ask "the phone rebooted, then 10 minutes idle with the screen off"
		kit mem > "$OUT/mem-idle.txt" || worst=1
		cat "$OUT/mem-idle.txt"
		;;
	task)
		[ $# -ge 2 ] || usage
		phase_task "$2"
		shift
		;;
	tasks)
		for t in $TASKS; do phase_task "$t"; done
		;;
	mem-apps)
		kit mem > "$OUT/mem-apps.txt" || worst=1
		cat "$OUT/mem-apps.txt"
		dump > /dev/null || worst=1
		;;
	standby-start)
		kit power-start || worst=1
		say "now: unplug USB, screen off, Wi-Fi off, Hongyan logged in; traffic.sh running; come back in 24 h, plug in and run standby-stop at once"
		;;
	standby-stop)
		kit power-stop > "$OUT/power.txt" || worst=1
		cat "$OUT/power.txt"
		;;
	table) phase_table || worst=1 ;;
	*) usage ;;
	esac
	shift
done
exit $worst
