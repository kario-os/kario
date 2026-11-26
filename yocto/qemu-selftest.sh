#!/bin/sh
# Run inside `kas-container shell`: boot the image with phone.selftest=1, let the
# image run the end-to-end scenario at boot and power off, then check the logs
# and keep what the device played. The scenario's parts ("#part" lines) run
# on several machines at once, each a QEMU of its own on the same image
# (snapshot) with its own console log, sound card, monitor and user network,
# at most as many as the processors and memory allow, and no more than end
# as soon (selftest-plan.py; PHONE_SELFTEST_VMS= sets the most, 1 runs the
# scenario whole in one boot); their results are put together.
set -u
# The helper scripts next to this one (the tree is /repo, or /kario in the internal workspace).
here=$(cd "$(dirname "$0")" && pwd)
cd /work/build
deploy=tmp/deploy/images/qemuarm64
# Each machine's console.log, audio.wav, cts-device.json (and .root) in
# selftest/vm<n>/; qemu-selftest.log is their console logs one after another.
runs=/work/build/selftest
log=/work/build/qemu-selftest.log
# The timeout only catches a hung run: in parts the longest machine takes
# some 6.5 minutes, the whole scenario in one some 35 (2026-11-25); the
# emulated clock falls behind on a busy shared host (915 s of guest time in
# 1200 s, 2026-11-16).
# PHONE_SELFTEST_ONLY (make yocto-selftest ONLY=, development only): the
# cases' regular expression in hex (selftest-boot.sh decodes it), run in one
# machine on the whole scenario; such a run is judged by its cases alone.
# PHONE_SELFTEST_ENV (make yocto-selftest SELFTEST_ENV=, development only):
# NAME=value words in hex, which selftest-boot.sh sets for phone-shell.
only="${PHONE_SELFTEST_ONLY:-}"
extra_env="${PHONE_SELFTEST_ENV:-}"
case $only$extra_env in
*[!0-9a-f]*)
	echo "PHONE_SELFTEST_ONLY and PHONE_SELFTEST_ENV must be hex"
	exit 2
	;;
esac
plan=$(python3 "$here"/selftest-plan.py "$here"/../shell/selftest.script "$here"/../shell/cts.script "$runs" ${only:+--vms 1}) || exit 2
# (cts-device.json and qemu-audio.wav: where a run in one machine kept them
# before the parts.)
rm -rf "$runs" "$log" /work/build/device-pixel /work/build/cts-device.json /work/build/cts-device.json.root /work/build/qemu-audio.wav && mkdir -p "$runs"
# runqemu would unpack the compressed image into the deploy directory for
# each machine, at one path, and remove it as the first one ends.
rootfs=$runs/phone-image.rootfs.ext4
zstd -q -d -o "$rootfs" "$(readlink -f $deploy/phone-image-qemuarm64.rootfs.ext4.zst)" || exit 2
conf=$(readlink -f $deploy/phone-image-qemuarm64.rootfs.qemuboot.conf)
# No ports forwarded to the machines: nothing logs in, and the machines
# would only take turns for them.
export QB_SLIRP_OPT="-netdev user,id=net0"
echo "booting qemuarm64 (TCG, no KVM for aarch64 on x86); logs: $runs/vm*/console.log"

# boot <dir> <parts> <fd store>: one machine. A third simulated Wi-Fi radio
# for hostapd, the enterprise case's access point (iwd leaves phy2 alone on
# the emulator). The cases that need a button press it through the button
# step, which asks on the console; qemu-buttons.py answers from the monitor
# with the virt machine's power button (PHONE_SELFTEST_BUTTONS names what
# it presses); the touchscreen step asks the same way, and qemu-buttons.py
# touches the SKU's virtio multitouch device (QB_GRAPHICS) through QMP
# (PHONE_SELFTEST_TOUCH says it does). What the device plays goes through
# a virtio sound card into a WAV file, which yocto/audio-check.py analyses
# on the host.
boot() {
	params="phone.selftest=1 mac80211_hwsim.radios=3 printk.devkmsg=on systemd.setenv=PHONE_SELFTEST_BUTTONS=power systemd.setenv=PHONE_SELFTEST_TOUCH=1${only:+ phone.selftest-only=$only}${extra_env:+ phone.selftest-env=$extra_env}"
	if [ -n "$2" ]; then
		params="$params phone.selftest-parts=$2"
		[ "$3" = 1 ] && params="$params phone.selftest-fdstore=1"
	fi
	start=$(date +%s)
	timeout 2400 runqemu "$conf" "$rootfs" nographic slirp snapshot bootparams="$params" \
		qemuparams="-audiodev wav,id=snd0,path=$1/audio.wav -device virtio-sound-pci,audiodev=snd0 -monitor unix:$1/monitor.sock,server,nowait -qmp unix:$1/qmp.sock,server,nowait" </dev/null >"$1/console.log" 2>&1 &
	qemu=$!
	python3 "$here"/qemu-buttons.py "$1/console.log" "$1/monitor.sock" "$qemu" "$1/qmp.sock" > "$1/buttons.log" &
	buttons=$!
	wait $qemu
	echo "qemu exited with $? after $(($(date +%s) - start)) s" > "$1/qemu.exit"
	wait $buttons
}

n=0
while read -r parts fdstore; do
	n=$((n + 1))
	mkdir -p "$runs/vm$n"
	echo "$parts" > "$runs/vm$n/parts"
	boot "$runs/vm$n" "$parts" "${fdstore:-1}" &
done <<EOF
$plan
EOF
wait

# The device writes the report straight to the console, every line prefixed
# (selftest-boot.sh); other console lines can only come between them.
ok=1 pass=0 cases=0 fdstore=0
for i in $(seq 1 $n); do
	vm=$runs/vm$i
	parts=$(cat "$vm/parts")
	clean=$(sed 's/\x1b\[[0-9;]*m//g' "$vm/console.log" | tr -d '\r')
	echo "=== vm$i: ${parts:-the whole scenario}" | tee -a "$log"
	cat "$vm/console.log" >> "$log"
	cat "$vm/qemu.exit" "$vm/buttons.log"
	echo "$clean" | sed -n '/phone-shell selftest/,/PHONE-SELFTEST-RESULT/p'
	python3 "$here"/extract-png.py "$vm/console.log" /work/build/device-pixel
	echo "$clean" | sed -n 's/^.*PHONE-CTS-REPORT: //p' |
		sed -n '/^PHONE-CTS-REPORT-BEGIN$/,/^PHONE-CTS-REPORT-END$/p' | grep -av '^PHONE-CTS-REPORT-' > "$vm/cts-device.json"
	echo "$clean" | grep -a 'PHONE-CTS-ROOT:' | tail -1 | sed 's/.*PHONE-CTS-ROOT: *//' > "$vm/cts-device.json.root"
	for what in PHONE-MODULES PHONE-CTS PHONE-FDSTORE; do
		echo "$clean" | grep -a "$what:" | tail -1
	done
	# The scenario's own result, not the fd store check's (phone-fdstore: ...).
	result=$(echo "$clean" | grep -a -o 'selftest-boot\.sh\[[0-9]*\]: PHONE-SELFTEST-RESULT: [A-Z]* ([0-9]*/[0-9]* cases)' | tail -1)
	case $result in
	*PASS*) ;;
	*) ok=0 ;;
	esac
	read -r p c <<-EOF
	$(echo "$result" | sed -n 's/.*(\([0-9]*\)\/\([0-9]*\) cases)/\1 \2/p')
	EOF
	pass=$((pass + ${p:-0})) cases=$((cases + ${c:-0}))
	[ -n "$only" ] && continue
	echo "$clean" | grep -aq "PHONE-CTS: PASS" && echo "$clean" | grep -aq "PHONE-MODULES: PASS" && [ -s "$vm/cts-device.json" ] || ok=0
	if echo "$clean" | grep -aq "PHONE-FDSTORE:"; then
		fdstore=$((fdstore + 1))
		echo "$clean" | grep -aq "PHONE-FDSTORE: PASS" || ok=0
	fi
done
[ -n "$only" ] || [ $fdstore = 1 ] || ok=0
verdict=FAIL
[ $ok = 1 ] && verdict=PASS
echo "PHONE-SELFTEST-RESULT: $verdict ($pass/$cases cases)$([ $n -gt 1 ] && echo ", in $n machines")"
[ $ok = 1 ]
