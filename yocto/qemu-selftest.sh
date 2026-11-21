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
# point (iwd leaves phy2 alone on the emulator). The timeout only catches a
# hung run: the whole run takes some 20 minutes (180 cases, 2026-11-17), and
# the emulated clock falls behind on a busy shared host (915 s of guest time
# in 1200 s, 2026-11-16).
# PHONE_SELFTEST_ONLY (make yocto-selftest ONLY=, development only): the
# cases' regular expression in hex (selftest-boot.sh decodes it); such a
# run is judged by its cases alone. PHONE_SELFTEST_ENV (make yocto-selftest
# SELFTEST_ENV=, development only): NAME=value words in hex, which
# selftest-boot.sh sets for phone-shell.
only="${PHONE_SELFTEST_ONLY:-}"
extra_env="${PHONE_SELFTEST_ENV:-}"
case $only$extra_env in
*[!0-9a-f]*)
	echo "PHONE_SELFTEST_ONLY and PHONE_SELFTEST_ENV must be hex"
	exit 2
	;;
esac
timeout 2400 runqemu qemuarm64 nographic slirp snapshot bootparams="phone.selftest=1 mac80211_hwsim.radios=3 printk.devkmsg=on${only:+ phone.selftest-only=$only}${extra_env:+ phone.selftest-env=$extra_env}" \
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
if [ -n "$only" ]; then
	grep -aq "PHONE-SELFTEST-RESULT: PASS" "$log"
	exit
fi
grep -aq "PHONE-SELFTEST-RESULT: PASS" "$log" && grep -aq "PHONE-CTS: PASS" "$log" && grep -aq "PHONE-FDSTORE: PASS" "$log"
