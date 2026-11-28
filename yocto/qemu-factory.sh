#!/bin/sh
# Run inside `kas-container shell`: factory mode on QEMU (40-testing.md §10,
# 31-lock-and-biometrics.md §13). The image boots as the line's fixture
# boots a phone (kario.factory=1) with the Shell on the virtio-gpu display
# (phone.gui=1) and phone-factory-qemu.service (kario.factory-test=1):
# factory mode must be entered, the runner's automatic checks pass, the
# sound card taken from its driver fail the test, the phone be
# provisioned through the runner as the station does, everything after
# that be refused, and the Shell power the phone off. A second boot of the
# same disk (kario.factory-test=again) must refuse factory mode and boot
# as ever. A boot that gets nowhere is stopped after a while.
set -u
here=$(cd "$(dirname "$0")" && pwd)
cd /work/build || exit 1
deploy=tmp/deploy/images/qemuarm64
w=/work/build/factory-test
log=/work/build/qemu-factory.log
rm -rf "$w" && mkdir -p "$w"
rootfs=$w/phone-image.rootfs.ext4
zstd -q -d -o "$rootfs" "$(readlink -f $deploy/phone-image-qemuarm64.rootfs.ext4.zst)" || exit 2
conf=$(readlink -f $deploy/phone-image-qemuarm64.rootfs.qemuboot.conf)
export QB_SLIRP_OPT="-netdev user,id=net0"
: > "$log"

failed=0
result() {
	echo "PHONE-FACTORY: $1: $2 ($3)" | tee -a "$log"
	[ "$2" = PASS ] || failed=1
}

# boot <name> <kario.factory-test=>: one boot of the disk (kept between
# the boots: the second sees the first's provisioning), its console in
# $w/<name>/console.log; sets code, took and clean (the console's text,
# the test's console file's lines, kernel lines taken out of them).
boot() {
	d=$w/$1
	mkdir -p "$d"
	params="kario.factory=1 kario.factory-test=$2 phone.gui=1 mac80211_hwsim.radios=3 printk.devkmsg=on"
	echo "booting qemuarm64 in factory mode ($1): $params; console: $d/console.log"
	start=$(date +%s)
	timeout 1800 runqemu "$conf" "$rootfs" nographic slirp bootparams="$params" \
		qemuparams="-audiodev none,id=snd0 -device virtio-sound-pci,audiodev=snd0 -monitor unix:$d/monitor.sock,server,nowait" < /dev/null > "$d/console.log" 2>&1
	code=$?
	took=$(($(date +%s) - start))
	clean=$(python3 "$here"/qemu-console.py text "$d/console.log" 2> "$d/console.wrong")
	{
		echo "== $1: qemu exited with $code after $took s"
		echo "$clean" | grep -a -e 'phone-factory-test' -e 'PHONE-FACTORY-' -e 'reboot: '
	} >> "$log"
	[ ! -s "$d/console.wrong" ] || result "$1-console" FAIL "$(tr '\n' ';' < "$d/console.wrong")"
}
verdict() {
	v=$(echo "$clean" | grep -a -o 'PHONE-FACTORY-TEST-RESULT: [A-Z]*' | tail -n 1)
	if [ "$v" = "PHONE-FACTORY-TEST-RESULT: PASS" ]; then
		result "$1" PASS "$2"
	else
		result "$1" FAIL "${v:-no result}: $(echo "$clean" | grep -a -o 'PHONE-FACTORY-TEST: [^:]*: FAIL.*' | tr '\n' ';')"
	fi
}

boot factory 1
verdict factory "factory mode entered, the automatic checks, a missing sound card failing them, the provisioning, the refusals after it"
if [ "$code" = 0 ] && echo "$clean" | grep -aq 'reboot: Power down'; then
	result power-off PASS "the machine powered off after $took s"
else
	result power-off FAIL "qemu exited with $code after $took s"
fi
boot again again
verdict again "a provisioned phone refuses factory mode and boots as ever"
rm -f "$rootfs"
cat "$log"
exit $failed
