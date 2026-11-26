#!/bin/sh
# Charger mode on the emulator (phone-charger-test.service, run by
# yocto/qemu-charger.sh): test_power's battery (50%) and chargers stand in
# for the phone's. Checks, each reported as "PHONE-CHARGER-TEST: <case>:
# PASS|FAIL". With phone.charger-test=1: the charger's screen shows the
# battery charging; nothing of the system runs; the screen goes off by
# itself; the power key lights it (qemu-buttons.py presses it when asked);
# the emulator takes the Shell's no sleep; the battery's Full and its
# current reach the charger; at 2% a hold of the power key does not start
# the system; then, after the result line, the chargers are unplugged and
# the phone must power off, which the host sees as QEMU ending. With
# phone.charger-test=start: at 2% a hold waits, and the battery at 3% the
# phone reboots (QEMU runs with -no-reboot and ends).
st=/run/phone/charger
tp=/sys/module/test_power/parameters
failed=0
say() { echo "PHONE-CHARGER-TEST: $*"; }
check() {
	name=$1
	shift
	if "$@"; then
		say "$name: PASS"
	else
		say "$name: FAIL ($(cat $st 2> /dev/null))"
		failed=1
	fi
}
# is <field> <JSON value>...: the charger's state says all of them.
is() {
	while [ $# -ge 2 ]; do
		grep -q "\"$1\":$2[,}]" $st 2> /dev/null || return 1
		shift 2
	done
}
# within <seconds> <command>...: the command succeeds before the time is up.
within() {
	n=$1
	shift
	until "$@"; do
		n=$((n - 1))
		[ $n -gt 0 ] || return 1
		sleep 1
	done
}
# still <seconds> <command>...: the command keeps succeeding that long.
still() {
	n=$1
	shift
	while [ "$n" -gt 0 ]; do
		"$@" || return 1
		n=$((n - 1))
		sleep 1
	done
}
off() {
	for u in phone-display phone-telephonyd ModemManager iwd bluetooth multi-user.target; do
		if systemctl is-active -q "$u"; then
			echo "running: $u"
			return 1
		fi
	done
	d=$(systemctl show -P Id default.target)
	if [ "$d" != phone-charger.target ]; then
		echo "default target: $d"
		return 1
	fi
	# This test is one of the target's units, so until it ends the target
	# waits in its start job rather than being active.
	t=$(systemctl is-active phone-charger.target)
	[ "$t" = active ] && return 0
	if [ -z "$(systemctl list-jobs --no-legend phone-charger.target)" ]; then
		echo "phone-charger.target: $t, no start job"
		return 1
	fi
}
# hold: the power key held 2.4 s (each press of qemu-buttons.py keeps the
# line up 100 ms; the next comes before it drops), past the charger's 2 s.
hold() {
	echo "PHONE-SELFTEST-PRESS: power $1 x30 80ms"
}
# says <text>: the charger's line has it (the line is in the image's
# language; the unit tests check its words).
says() {
	grep -q "\"line\":\"[^\"]*$1" $st 2> /dev/null
}
# full: the line says Full, which has no percentage.
full() {
	! says %
}
# low: the battery at 2%, too low to start, and a hold that waits.
low() {
	echo 2 > $tp/battery_capacity
	check too-low within 30 is level 2 too_low true
	check too-low-line says 2%
	say "hold the power key at 2%"
	hold "$1"
	check hold-waits within 30 is pending true leaving '""'
}

[ -d $tp ] || say "no test_power: the emulator's kernel lacks it (CONFIG_TEST_POWER)"
mode=$(sed -n 's/.*phone\.charger-test=\([^ ]*\).*/\1/p' /proc/cmdline)
check screen within 180 is screen '"on"' panel '"on"' battery true level 50 charging true
if [ "$mode" = start ]; then
	low 1
	if [ $failed = 0 ]; then
		echo "PHONE-CHARGER-TEST-RESULT: PASS"
	else
		echo "PHONE-CHARGER-TEST-RESULT: FAIL"
	fi
	say "charging to 3%: the phone starts"
	echo 3 > $tp/battery_capacity
	within 30 is leaving '"reboot"' || say "at 3%: no reboot ($(cat $st 2> /dev/null))"
	exit 0
fi
check system-off off
check screen-off within 30 is screen '"off"' panel '"off"'
say "press the power key"
echo "PHONE-SELFTEST-PRESS: power 1"
check power-key within 60 is screen '"on"' panel '"on"'
check no-sleep is sleep '"off: the board cannot be woken from suspend"' sleeps 0
echo full > $tp/battery_status
echo 1500000 > $tp/battery_current
check full within 30 is status '"Full"' current_ua 1500000
check full-line full
echo charging > $tp/battery_status
low 2
check no-start still 5 is pending true leaving '""'
if [ $failed = 0 ]; then
	echo "PHONE-CHARGER-TEST-RESULT: PASS"
else
	echo "PHONE-CHARGER-TEST-RESULT: FAIL"
fi
say "unplugging the chargers: the phone powers off"
echo off > $tp/ac_online
echo off > $tp/usb_online
within 30 is leaving '"poweroff"' || say "unplugged: no power-off ($(cat $st 2> /dev/null))"
