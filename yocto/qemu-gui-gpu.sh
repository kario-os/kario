#!/bin/sh
# Run inside `kas-container shell` (make yocto-gui-gpu): phone-paint's GPU path on
# the emulator's display (42-gpu-rendering.md §7), four phone.gui=1 boots one after
# the other with the same keys (qemu-gui.sh): drawing on the CPU (gui-shots-cpu),
# composing on the GPU through Mesa's softpipe (PHONE_PAINT_GPU=soft: QEMU's 2D
# virtio-gpu has no GPU, so only the test mode takes it; gui-shots-gpu), the same
# rendering on another DRM file than the display's, as on a board whose GPU is a
# device of its own (PHONE_PAINT_GPU_RENDER=$PHONE_GUI_GPU_RENDER, the buffers
# imported into the display's file through PRIME and flipped there; 42 §4;
# gui-shots-gpu-split), and the GPU failing on purpose at frame
# $PHONE_GUI_GPU_FAIL, so the CPU takes over (gui-shots-gpu-fail). Then each GPU
# run's screendumps against the CPU run's (gui-compare.py). softpipe under TCG
# takes half a minute a frame: the GPU runs wait for each screen to settle
# (PHONE_GUI_WAIT), the first two and a half hours each. PHONE_GUI_GPU_RUNS
# (make yocto-gui-gpu GUI_GPU_RUNS=): which of gpu, gpu-split and gpu-fail run
# after the CPU's, all by default.
set -u
here=$(cd "$(dirname "$0")" && pwd)
fail_at=${PHONE_GUI_GPU_FAIL:-8}
runs=${PHONE_GUI_GPU_RUNS:-gpu gpu-split gpu-fail}
for run in $runs; do
	case $run in
	gpu | gpu-split | gpu-fail) ;;
	*)
		echo "PHONE_GUI_GPU_RUNS: no run $run (gpu, gpu-split, gpu-fail)"
		exit 2
		;;
	esac
done
# virtio-gpu has a render node, but Mesa renders on none of its: its driver
# for one is virgl (QEMU's 3D device), and kms_swrast needs dumb buffers,
# which a render node refuses (42 §7.4). A second open of the card is a file
# of its own all the same.
render=${PHONE_GUI_GPU_RENDER:-/dev/dri/card0}
# Every boot's clock starts at the same time, noon in the SKU's region
# (China): the boots take from five minutes to hours and may start on either
# side of a midnight, and the calendar's today (where its focus starts)
# follows the date.
PHONE_GUI_RTC=${PHONE_GUI_RTC:-$(date -u +%Y-%m-%dT04:00:00)}
export PHONE_GUI_RTC
# The screen stays on between two keys however long a frame takes.
keep=PHONE_SCREEN_OFF=3600
rc=0
boot() {
	PHONE_GUI_RUN=$1 PHONE_GUI_ENV="$keep${2:+ $2}" PHONE_GUI_WAIT=$3 sh "$here"/qemu-gui.sh || rc=1
}
boot cpu "" 1
for run in $runs; do
	case $run in
	gpu) boot gpu "PHONE_PAINT_GPU=soft" 10 ;;
	gpu-split) boot gpu-split "PHONE_PAINT_GPU=soft PHONE_PAINT_GPU_RENDER=$render" 10 ;;
	gpu-fail) boot gpu-fail "PHONE_PAINT_GPU=soft PHONE_PAINT_GPU_FAIL=$fail_at" 10 ;;
	esac
done
cd /work/build || exit 1
# The date each boot's kernel took from the RTC: one for all.
clock() {
	sed 's/\x1b\[[0-9;]*m//g' "qemu-gui-$1.log" | grep -a -o 'setting system clock to [0-9-]*' | head -1 | sed 's/.* //'
}
day=$(clock cpu)
for run in $runs; do
	if [ "$(clock "$run")" != "$day" ]; then
		echo "PHONE-GUI-CLOCK: $run booted on $(clock "$run"), the CPU run on $day"
		rc=1
	fi
done
echo "PHONE-GUI-CLOCK: every boot from $PHONE_GUI_RTC UTC ($day)"
# What changes by itself on a screen (a clock, the scan line) counts only
# where it is: the drive script names it (PHONE-GUI-VOLATILE).
volatile=$(sed -n 's/^PHONE-GUI-VOLATILE: /--volatile /p' gui-shots-cpu/drive.txt)
for run in $runs; do
	echo "== gui-shots-$run against gui-shots-cpu"
	# shellcheck disable=SC2086 # an option and its value a line
	python3 "$here"/gui-compare.py gui-shots-cpu "gui-shots-$run" $volatile || rc=1
done
for run in cpu $runs; do
	echo "== $run"
	grep -E '^(PHONE-GUI|push|mouse)' "gui-shots-$run/drive.txt"
	sed 's/\x1b\[[0-9;]*m//g' "qemu-gui-$run.log" | grep -a -e 'phone-paint: gpu' -e 'phone-paint: page flip'
done
exit $rc
