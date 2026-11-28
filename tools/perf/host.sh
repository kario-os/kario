#!/bin/sh
# The gate's run on this machine (39-oneplus-6t.md §8.1): the Shell in its
# device mode with phone-paint headless (PHONE_PAINT_HEADLESS=1), the fixed
# sequence (gate.play) played into it once a run, each run a GPU path (cpu,
# compose, raster; soft for Mesa's software GPU) at the SKU's screen size
# (full) or the product's (target, PHONE_PAINT_TARGET); each run's numbers
# (paint-stats.json) and one table against the gate. Each run starts the
# Shell afresh, so the apps start cold. Nothing is shown, so there are no
# page flips: the times are to the frame drawn, on this machine's CPU and
# GPU (the table's gl lines say which). It tests the run, the metrics and
# the sequence; the phone's numbers come from the kit's perf step.
#
#   tools/perf/host.sh [--sku SKU] [--runs "cpu-full cpu-target ..."]
#                      [--target WxH] [--wait S] [--out DIR] [--play FILE]
#
# --play plays another sequence (the comparison's tasks,
# tools/android-compare/tasks.play). Needs make build and make paint. Exit
# status: 0 when every run passed the gate, 1 when one did not, 2 when a run
# could not be made.
set -eu

here=$(cd "$(dirname "$0")" && pwd)
FW=$(cd "$here/../.." && pwd)
SKU=oneplus-fajita-t9
RUNS="cpu-full cpu-target compose-full compose-target raster-full raster-target"
TARGET=480x640
WAIT=600
OUT=$FW/build/perf
PLAY=$here/gate.play

die() {
	echo "perf: $*" >&2
	exit 2
}

while [ $# -gt 0 ]; do
	case $1 in
	--sku) SKU=$2 ;;
	--runs) RUNS=$2 ;;
	--target) TARGET=$2 ;;
	--wait) WAIT=$2 ;;
	--out) OUT=$2 ;;
	--play) PLAY=$2 ;;
	*) die "usage: $0 [--sku SKU] [--runs \"cpu-full ...\"] [--target WxH] [--wait S] [--out DIR] [--play FILE]" ;;
	esac
	[ $# -ge 2 ] || die "$1 needs a value"
	shift 2
done
[ -f "$PLAY" ] || die "no $PLAY"
PLAY=$(cd "$(dirname "$PLAY")" && pwd)/$(basename "$PLAY")

shell=$FW/build/usr/bin/phone-shell
paint=$FW/paint/target/release/phone-paint
[ -x "$shell" ] || die "no $shell: make build"
[ -x "$paint" ] || die "no $paint: make paint"
rm -rf "$OUT"
mkdir -p "$OUT/sku"
python3 "$FW/tools/sku.py" files "$SKU" "$OUT/sku" > /dev/null || die "no SKU $SKU"
sim=$OUT/sim
stats=$sim/run/paint-stats.json

# played FILE: whether the sequence got to its end (or stopped on an error).
played() {
	grep -q -E '"state": "(done|failed)"' "$1" 2> /dev/null
}

files=
for r in $RUNS; do
	case ${r%-*} in
	cpu) gpu=0 ;;
	compose) gpu=1 ;;
	raster | soft) gpu=${r%-*} ;;
	*) die "$r: the GPU path is one of cpu compose raster soft" ;;
	esac
	case ${r#*-} in
	full) target= ;;
	target) target=$TARGET ;;
	*) die "$r: the size is full or target" ;;
	esac
	rm -f "$stats" "$sim/run/shell.log" "$sim/run/gui.log"
	PHONE_PAINT_HEADLESS=1 PHONE_PAINT_GPU=$gpu PHONE_PAINT_TARGET=$target PHONE_PAINT_PLAY=$PLAY \
		"$shell" --services spawn --sim-dir "$sim" --gui "$paint" --sku "$OUT/sku/sku.json" --stay-on \
		> "$OUT/$r.log" 2>&1 < /dev/null &
	pid=$!
	t=0
	while ! played "$stats" && kill -0 "$pid" 2> /dev/null && [ "$t" -lt "$WAIT" ]; do
		sleep 1
		t=$((t + 1))
	done
	if [ -f "$stats" ]; then
		cp "$stats" "$OUT/$r.json"
	fi
	kill "$pid" 2> /dev/null || true
	wait "$pid" 2> /dev/null || true
	# The services it started for this run (their run directory is ours).
	pkill -f -- "$sim/" 2> /dev/null || true
	cp "$sim/run/shell.log" "$OUT/$r.shell.log" 2> /dev/null || true
	cp "$sim/run/gui.log" "$OUT/$r.gui.log" 2> /dev/null || true
	[ -f "$OUT/$r.json" ] || die "$r: no numbers (the Shell's output: $OUT/$r.log)"
	played "$OUT/$r.json" || echo "perf: $r: the sequence did not end in $WAIT s" >&2
	echo "perf: $r: $OUT/$r.json"
	files="$files $OUT/$r.json"
done
rc=0
# shellcheck disable=SC2086 # a file an argument
"$paint" --stats $files > "$OUT/perf.txt" || rc=$?
cat "$OUT/perf.txt"
exit "$rc"
