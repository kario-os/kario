#!/bin/sh
# Run inside `kas-container shell`: boot the image with phone.selftest=1, let the
# image run the end-to-end scenario at boot and power off, then check the log
# and keep what the device played (qemu-audio.wav).
set -u
# The helper scripts next to this one (the tree is /repo, or /kario in the internal workspace).
here=$(cd "$(dirname "$0")" && pwd)
cd /work/build
log=/work/build/qemu-selftest.log
echo "booting qemuarm64 (TCG, no KVM for aarch64 on x86); log: $log"
# What the device plays goes through a virtio sound card into a WAV file,
# which yocto/audio-check.py analyses on the host.
wav=/work/build/qemu-audio.wav
rm -f "$wav"
# A third simulated Wi-Fi radio for hostapd, the enterprise case's access
# point (iwd leaves phy2 alone on the emulator).
timeout 1200 runqemu qemuarm64 nographic slirp snapshot bootparams="phone.selftest=1 mac80211_hwsim.radios=3" \
	qemuparams="-audiodev wav,id=snd0,path=$wav -device virtio-sound-pci,audiodev=snd0" </dev/null >"$log" 2>&1
echo "qemu exited with $?"
sed -n '/phone-shell selftest/,/PHONE-SELFTEST-RESULT/p' "$log" | sed 's/\x1b\[[0-9;]*m//g'
python3 "$here"/extract-png.py "$log" /work/build/device-pixel
# The device writes the report straight to the console, every line prefixed
# (selftest-boot.sh); other console lines can only come between them.
sed 's/\x1b\[[0-9;]*m//g' "$log" | tr -d '\r' | sed -n 's/^.*PHONE-CTS-REPORT: //p' |
	sed -n '/^PHONE-CTS-REPORT-BEGIN$/,/^PHONE-CTS-REPORT-END$/p' | grep -av '^PHONE-CTS-REPORT-' > /work/build/cts-device.json
sed 's/\x1b\[[0-9;]*m//g' "$log" | grep -a 'PHONE-CTS-ROOT:' | tail -1 | sed 's/.*PHONE-CTS-ROOT: *//' | tr -d '\r' > /work/build/cts-device.json.root
grep -a "PHONE-CTS:" "$log" | tail -1
grep -a "PHONE-FDSTORE:" "$log" | tail -1
grep -aq "PHONE-SELFTEST-RESULT: PASS" "$log" && grep -aq "PHONE-CTS: PASS" "$log" && grep -aq "PHONE-FDSTORE: PASS" "$log"
