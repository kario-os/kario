#!/bin/sh
# Run inside `kas-container shell`: boot with phone.gui=1, so phone-shell drives
# The slot's renderer on the virtio-gpu DRM display, then press keys and screendump the
# display from the QEMU monitor (see qemu-gui-drive.py). PHONE_PAINT_TRACE (set for
# every service through systemd.setenv) has phone-paint print what each burst of
# motion cost on that display, and PHONE_PAINT_STEPS each burst's step times, on
# the console; the drive script prints the sums at the end (PHONE-GUI-MOTION,
# docs/37-motion.md §2.3: emulated, not a phone's).
# PHONE_GUI_ENV (make yocto-gui GUI_ENV=): NAME=value words set for every
# service the same way, e.g. PHONE_PAINT_GPU=soft (42-gpu-rendering.md §7);
# PHONE_GUI_RUN: a name for the run, whose log and screendumps then go to
# qemu-gui-<name>.log and gui-shots-<name>; PHONE_GUI_WAIT: a whole number,
# above 1 for a renderer that takes many seconds a frame (qemu-gui-drive.py),
# and the run's time limit grows with it; PHONE_GUI_RTC: a UTC time
# (2026-11-24T04:00:00) the guest's clock starts at instead of the host's (QEMU's
# -rtc base=; the image syncs no clock), so that boots compared with each other
# show the same date (qemu-gui-gpu.sh).
set -u
times=${PHONE_GUI_WAIT:-1}
# The helper scripts next to this one (the tree is /repo, or /kario in the internal workspace).
here=$(cd "$(dirname "$0")" && pwd)
cd /work/build || exit 1
run=${PHONE_GUI_RUN:+-$PHONE_GUI_RUN}
log=/work/build/qemu-gui$run.log
mon=/tmp/qemu-gui-monitor.sock
qmp=/tmp/qemu-gui-qmp.sock
out=/work/build/gui-shots$run
setenv=
for kv in ${PHONE_GUI_ENV:-}; do
	case $kv in
	[A-Z_]*=*) setenv="$setenv systemd.setenv=$kv" ;;
	*)
		echo "PHONE_GUI_ENV: $kv is not NAME=value"
		exit 2
		;;
	esac
done
rtc=
case ${PHONE_GUI_RTC:-} in
'') ;;
[0-9][0-9][0-9][0-9]-[0-1][0-9]-[0-3][0-9]T[0-2][0-9]:[0-5][0-9]:[0-5][0-9]) rtc=" -rtc base=$PHONE_GUI_RTC" ;;
*)
	echo "PHONE_GUI_RTC: $PHONE_GUI_RTC is not YYYY-MM-DDThh:mm:ss"
	exit 2
	;;
esac
rm -rf "$out" "$mon" "$qmp"
mkdir -p "$out"
echo "booting qemuarm64 with phone.gui=1${PHONE_GUI_ENV:+ and ${PHONE_GUI_ENV}}${PHONE_GUI_RTC:+, the clock from $PHONE_GUI_RTC UTC}; log: $log"
# The monitor presses keys and screendumps; QMP touches the SKU's virtio
# multitouch device (QB_GRAPHICS), which the renderer reads as a phone's
# touchscreen.
timeout $((1500 * times)) runqemu qemuarm64 nographic slirp snapshot bootparams="phone.gui=1 systemd.setenv=PHONE_PAINT_TRACE=1 systemd.setenv=PHONE_PAINT_STEPS=/dev/console$setenv" \
	qemuparams="-monitor unix:$mon,server,nowait -qmp unix:$qmp,server,nowait$rtc" </dev/null >"$log" 2>&1 &
qemu=$!
python3 "$here"/qemu-gui-drive.py "$log" "$mon" "$out" "$qmp"
rc=$?
wait $qemu
sed 's/\x1b\[[0-9;]*m//g' "$log" | grep -a -E 'PHONE-GUI|phone-gui|phone-paint|phone-shell' | grep -av -e 'so far:' -e '{"draw"' | tail -20
ls -la "$out"
exit $rc
