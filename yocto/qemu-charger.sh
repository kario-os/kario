#!/bin/sh
# Run inside `kas-container shell`: charger mode on QEMU (28-battery.md §9).
# The image boots with what the OnePlus 6T's bootloader passes when its
# charger started it (androidboot.mode=charger, 39-oneplus-6t.md §16.9)
# and test_power's battery and chargers (modules-load=test_power):
# phone-charger-generator must boot phone-charger.target, and
# phone-charger-test.service (phone.charger-test=1) checks the charger's
# screen, that nothing of the system runs, that the screen goes off by
# itself and that the power key lights it (qemu-buttons.py presses the
# virt machine's power button when asked), that the emulator does not
# suspend, the battery's Full and current, and that a hold at 2% does not
# start the system; then it unplugs the chargers and the machine must
# power off by itself. A second boot (phone.charger-test=start, QEMU with
# -no-reboot) holds the key at 2% and charges to 3%: the phone must
# reboot. A boot that gets nowhere is stopped after a while.
set -u
here=$(cd "$(dirname "$0")" && pwd)
cd /work/build || exit 1
deploy=tmp/deploy/images/qemuarm64
w=/work/build/charger-test
log=/work/build/qemu-charger.log
rm -rf "$w" && mkdir -p "$w"
rootfs=$w/phone-image.rootfs.ext4
zstd -q -d -o "$rootfs" "$(readlink -f $deploy/phone-image-qemuarm64.rootfs.ext4.zst)" || exit 2
conf=$(readlink -f $deploy/phone-image-qemuarm64.rootfs.qemuboot.conf)
export QB_SLIRP_OPT="-netdev user,id=net0"
: > "$log"

# boot <name> <phone.charger-test=> <extra QEMU options>: one boot, its
# console in $w/<name>/console.log; sets code, took and clean (the console
# without colours).
boot() {
	d=$w/$1
	mkdir -p "$d"
	params="androidboot.mode=charger phone.charger-test=$2 modules-load=test_power printk.devkmsg=on"
	echo "booting qemuarm64 in charger mode ($1): $params; console: $d/console.log"
	start=$(date +%s)
	timeout 900 runqemu "$conf" "$rootfs" nographic slirp snapshot bootparams="$params" \
		qemuparams="-monitor unix:$d/monitor.sock,server,nowait $3" < /dev/null > "$d/console.log" 2>&1 &
	qemu=$!
	python3 "$here"/qemu-buttons.py "$d/console.log" "$d/monitor.sock" "$qemu" > "$d/buttons.log" &
	buttons=$!
	wait $qemu
	code=$?
	took=$(($(date +%s) - start))
	wait $buttons
	clean=$(sed 's/\x1b\[[0-9;]*m//g' "$d/console.log" | tr -d '\r')
	{
		echo "== $1: qemu exited with $code after $took s"
		cat "$d/buttons.log"
		echo "$clean" | grep -a -e 'phone-charger' -e 'PHONE-CHARGER-TEST' -e 'reboot: '
	} >> "$log"
}

failed=0
result() {
	echo "PHONE-CHARGER: $1: $2 ($3)" | tee -a "$log"
	[ "$2" = PASS ] || failed=1
}
verdict() {
	v=$(echo "$clean" | grep -a -o 'PHONE-CHARGER-TEST-RESULT: [A-Z]*' | tail -n 1)
	if [ "$v" = "PHONE-CHARGER-TEST-RESULT: PASS" ]; then
		result "$1" PASS "$2"
	else
		result "$1" FAIL "${v:-no result}: $(echo "$clean" | grep -a -o 'PHONE-CHARGER-TEST: [^:]*: FAIL.*' | tr '\n' ';')"
	fi
}

boot charger 1 ""
if echo "$clean" | grep -aq 'phone-charger-generator: androidboot.mode=charger: booting phone-charger.target'; then
	result generator PASS "phone-charger.target is the boot's target"
else
	result generator FAIL "no line of phone-charger-generator's"
fi
verdict screen "the battery shown, nothing of the system, off by itself, on by the power key, no sleep, Full and the current, no start at 2%"
if [ "$code" = 0 ] && echo "$clean" | grep -aq 'reboot: Power down'; then
	result unplug PASS "the machine powered off by itself after the chargers went"
else
	result unplug FAIL "qemu exited with $code after $took s; $(echo "$clean" | grep -a -c 'reboot: Power down') power-down lines"
fi

boot start start "-no-reboot"
verdict low-battery "a hold at 2% waits for the level"
if [ "$code" = 0 ] && echo "$clean" | grep -aq 'battery at 3%: starting' && echo "$clean" | grep -aq 'reboot: Restarting system'; then
	result start PASS "at 3% the phone rebooted to start the system"
else
	result start FAIL "qemu exited with $code after $took s; $(echo "$clean" | grep -a -c 'reboot: Restarting system') restart lines"
fi
rm -f "$rootfs"
cat "$log"
exit $failed
