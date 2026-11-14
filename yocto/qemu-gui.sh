#!/bin/sh
# Run inside `kas-container shell`: boot with phone.gui=1, so phone-shell drives
# The slot's renderer on the virtio-gpu DRM display, then press keys and screendump the
# display from the QEMU monitor (see qemu-gui-drive.py).
set -u
# The helper scripts next to this one (the tree is /repo, or /kario in the internal workspace).
here=$(cd "$(dirname "$0")" && pwd)
cd /work/build
log=/work/build/qemu-gui.log
mon=/tmp/qemu-gui-monitor.sock
out=/work/build/gui-shots
rm -rf "$out" "$mon"
mkdir -p "$out"
echo "booting qemuarm64 with phone.gui=1; log: $log"
timeout 1500 runqemu qemuarm64 nographic slirp snapshot bootparams="phone.gui=1" \
	qemuparams="-monitor unix:$mon,server,nowait" </dev/null >"$log" 2>&1 &
qemu=$!
python3 "$here"/qemu-gui-drive.py "$log" "$mon" "$out"
rc=$?
wait $qemu
sed 's/\x1b\[[0-9;]*m//g' "$log" | grep -a -E 'PHONE-GUI|phone-gui|phone-paint|phone-shell' | tail -20
ls -la "$out"
exit $rc
