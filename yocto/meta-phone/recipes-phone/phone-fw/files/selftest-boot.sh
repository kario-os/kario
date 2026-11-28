#!/bin/sh
# Runs the same end-to-end scenario as `make selftest` on the host, then powers
# the machine off so an automated QEMU run terminates by itself. When the pixel
# renderer is installed it renders every snap step too, and a few PNGs are
# printed (base64) so the host can extract what the device drew. The
# renderer is whatever fills the image's slot (/usr/lib/phone/renderer).
export HOME=/tmp
# Under lockdown the kernel loads only modules signed with its build's key:
# one of the image's must load before any case loads one.
/usr/libexec/phone/module-check.sh | tee /tmp/modules.out
gui=""
if [ -x /usr/lib/phone/renderer ]; then
	gui="--gui auto --pixel-dir /tmp/pixel --print-png 00-boot,01-home,22-call-active,23-cell-incoming,25-alarm,26-calc,27-music,29-push-bill,45-web-focus"
fi
# Two virtual Bluetooth controllers: the phone's and the device it pairs
# with in the Wi-Fi and Bluetooth case (the Wi-Fi radios are mac80211_hwsim's).
# Both controllers have been seen gone in the middle of a run (BlueZ with
# no adapter at all): the log says when btvirt ended and how.
if [ -x /usr/bin/btvirt ]; then
	{
		/usr/bin/btvirt -l2
		echo "btvirt ended with $? at $(cut -d' ' -f1 /proc/uptime) s"
	} > /tmp/btvirt.log 2>&1 &
fi
# The heartbeat, as a unit of its own (selftest-heartbeat.sh).
systemd-run --quiet --unit=phone-selftest-heartbeat /usr/libexec/phone/selftest-heartbeat.sh /tmp/selftest.out
# phone.selftest-only=<hex> (make yocto-selftest ONLY=, development only):
# only the cases whose name matches that regular expression, the fd store
# check's too. phone.selftest-env=<hex> (SELFTEST_ENV=, the
# same): NAME=value words set for phone-shell. The image has no hex or
# base64 decoder but awk; LC_ALL=C makes its %c one byte.
bootarg() {
	tr ' ' '\n' < /proc/cmdline | sed -n "s/^phone\.$1=//p" |
		LC_ALL=C awk '{ h = "0123456789abcdef"; for (i = 1; i < length($0); i += 2) printf "%c", (index(h, substr($0, i, 1)) - 1) * 16 + index(h, substr($0, i + 1, 1)) - 1 }'
}
only=$(bootarg selftest-only)
set -f
for kv in $(bootarg selftest-env); do
	case ${kv%%=*} in
	"$kv" | "" | [0-9]* | *[!A-Za-z0-9_]*) echo "phone.selftest-env: $kv is not NAME=value" ;;
	*) export "$kv" ;;
	esac
done
set +f
set --
if [ -n "$only" ]; then
	set -- --only "$only"
fi
# phone.selftest-parts=<name>,... (one machine of yocto/qemu-selftest.sh's
# parallel run): only those parts of the scenario, its report theirs; the
# fd store check only where phone.selftest-fdstore=1 says so.
parts=$(tr ' ' '\n' < /proc/cmdline | sed -n 's/^phone\.selftest-parts=//p')
case $parts in
*[!a-z0-9,_-]*)
	echo "phone.selftest-parts: $parts is not a list of part names"
	parts=""
	;;
esac
if [ -n "$parts" ]; then
	set -- "$@" --parts "$parts"
fi
# The compatibility run: the selftest plus the CTS cases and the capability
# coverage report, which the host keeps (below).
{
	/usr/bin/phone-shell --cts /tmp/cts.json --timeout-scale 4 $gui "$@" 2>&1
	echo $? > /tmp/selftest.rc
} | tee /tmp/selftest.out
rc=$(cat /tmp/selftest.rc)
echo "phone-selftest: exit code $rc"
# A failure leaves what the radio daemons said, the run directory being gone.
if [ "$rc" != 0 ]; then
	for unit in bluetooth iwd; do
		journalctl -b -u $unit --no-pager -o short-monotonic 2>&1 | tail -30 | sed 's/^/phone-radios: /'
	done
	tail -10 /tmp/btvirt.log 2>/dev/null | sed 's/^/phone-btvirt: /'
	echo "running: $(pidof btvirt || echo none); controllers: $(ls /sys/class/bluetooth 2>/dev/null | tr '\n' ' ')" | sed 's/^/phone-btvirt: /'
	dmesg | grep -i -E 'btvirt|vhci|segfault|hci[0-9]' | tail -10 | sed 's/^/phone-btvirt: /'
	tail -20 /run/phone-eap-test/hostapd.log 2>/dev/null | sed 's/^/phone-hostapd: /'
	journalctl -b -u systemd-networkd --no-pager -o short-monotonic 2>&1 | grep -E 'kario-vpn|kvpn|WireGuard' | tail -20 | sed 's/^/phone-networkd: /'
	# A VPN case that failed half-way leaves its full tunnel up for what follows.
	rm -f /run/systemd/network/*kario-vpn*
	networkctl reload 2>/dev/null
	networkctl delete kario-vpn vethkvpn0 2>/dev/null
fi
# What the host keeps and judges by goes straight to the console as console
# files (console-file): through the journal it can still be on its way when
# power-off starts, and a kernel line can land inside any line there.
hand() { /usr/libexec/phone/console-file "$1" "$2" > /dev/console; }
hand cts-device.json /tmp/cts.json
[ -e /tmp/cts.json.root ] && hand cts-device.json.root /tmp/cts.json.root
{
	grep -a 'PHONE-MODULES: ' /tmp/modules.out
	grep -a -e '^PHONE-SELFTEST-RESULT: ' -e 'PHONE-CTS: ' /tmp/selftest.out
	echo "phone-selftest: exit code $rc"
} > /tmp/results.txt
hand results.txt /tmp/results.txt
if [ -n "$parts" ] && ! grep -qw phone.selftest-fdstore=1 /proc/cmdline; then
	systemctl poweroff --no-block
	exit 0
fi
# The crash cases: phone-shell as a service with an fd store crashes on
# purpose; the instance systemd restarts takes the running apps over.
rm -f /tmp/fdstore-resume.json /tmp/fdstore.out
printf '%s' "$only" > /tmp/fdstore.only
# How the machine is doing, straight to the console: the journal's copy can
# be minutes behind after the PNGs.
state() {
	{
		echo "phone-fdstore-state: $1, up $(cut -d' ' -f1 /proc/uptime) s, $(grep MemAvailable /proc/meminfo | tr -s ' '), /tmp $(df -m /tmp | awk 'NR==2 {print $3 " MB used"}')"
		top -b -n 1 | head -n 12 | sed 's/^/phone-fdstore-state:   /'
	} > /dev/console 2>&1
}
state "before"
# (phone-fdstore-check.service restarts it after each crash.)
systemctl start phone-fdstore-check.service
state "started"
# Done when the last restarted shell prints its result (the others only
# crash); the unit's state flickers through failed on the way.
for i in $(seq 1 600); do
	grep -q 'PHONE-SELFTEST-RESULT' /tmp/fdstore.out 2>/dev/null && break
	[ $((i % 30)) = 0 ] && state "waiting $i"
	sleep 1
done
# The shell never gives the store more than FileDescriptorStoreMax: systemd
# would close what does not fit.
overfull=$(journalctl -b -u phone-fdstore-check --no-pager 2>&1 | grep -a 'Cannot store more fds')
result=$(grep -a -o '^PHONE-SELFTEST-RESULT: PASS ([0-9]*/[0-9]* cases)' /tmp/fdstore.out | tail -1)
if [ -n "$only" ] && [ "$result" = "PHONE-SELFTEST-RESULT: PASS (0/0 cases)" ]; then
	echo "phone-fdstore: ONLY matches none of its cases"
elif [ -n "$result" ] && grep -q 'shell-crash' /tmp/fdstore.out && [ -z "$overfull" ]; then
	sed 's/^/phone-fdstore: /' /tmp/fdstore.out | grep -E 'PASS|line |RESULT|PHONE-(FDSTORE-HOLDS|SHELL-LOG|APP-LOG)'
	echo "PHONE-FDSTORE: PASS (${result#*(}" | tee /tmp/fdstore.txt
else
	echo "$overfull" | sed '/^$/d; s/^/phone-fdstore-journal: /'
	# The failing step with the whole screen, then the end of the shell's log.
	sed -n '/^  line [0-9]*:/,/^  processes:/p' /tmp/fdstore.out | sed 's/^/phone-fdstore: /'
	grep -a 'shell: ' /tmp/fdstore.out | tail -60 | sed 's/^/phone-fdstore-shell: /'
	systemctl status --no-pager phone-fdstore-check 2>&1 | sed 's/^/phone-fdstore-status: /' | head -30
	journalctl -u phone-fdstore-check --no-pager -o short-monotonic 2>&1 | sed 's/^/phone-fdstore-journal: /' | tail -40
	systemctl stop phone-fdstore-check 2>/dev/null
	echo "PHONE-FDSTORE: FAIL" | tee /tmp/fdstore.txt
fi
[ -e /tmp/fdstore.txt ] && hand fdstore.txt /tmp/fdstore.txt
systemctl poweroff --no-block
