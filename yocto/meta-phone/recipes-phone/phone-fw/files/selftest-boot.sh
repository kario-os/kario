#!/bin/sh
# Runs the same end-to-end scenario as `make selftest` on the host, then powers
# the machine off so an automated QEMU run terminates by itself. When the pixel
# renderer is installed it renders every snap step too, and a few PNGs are
# printed (base64) so the host can extract what the device drew. The
# renderer is whatever fills the image's slot (/usr/lib/phone/renderer).
export HOME=/tmp
gui=""
if [ -x /usr/lib/phone/renderer ]; then
	gui="--gui auto --pixel-dir /tmp/pixel --print-png 00-boot,01-home,22-call-active,23-cell-incoming,25-alarm,26-calc,27-music,29-push-bill,45-web-focus"
fi
# Two virtual Bluetooth controllers: the phone's and the device it pairs
# with in the Wi-Fi and Bluetooth case (the Wi-Fi radios are mac80211_hwsim's).
if [ -x /usr/bin/btvirt ]; then
	/usr/bin/btvirt -l2 > /tmp/btvirt.log 2>&1 &
fi
# The compatibility run: the selftest plus the CTS cases and the capability
# coverage report, printed between markers for the host to keep.
/usr/bin/phone-shell --cts /tmp/cts.json --timeout-scale 4 $gui
rc=$?
echo "phone-selftest: exit code $rc"
# A failure leaves what the radio daemons said, the run directory being gone.
if [ "$rc" != 0 ]; then
	journalctl -b -u bluetooth -u iwd --no-pager -o short-monotonic 2>&1 | tail -30 | sed 's/^/phone-radios: /'
	tail -10 /tmp/btvirt.log 2>/dev/null | sed 's/^/phone-btvirt: /'
	tail -20 /run/phone-eap-test/hostapd.log 2>/dev/null | sed 's/^/phone-hostapd: /'
fi
# Straight to the console, a line per write: through the journal the report
# can still be on its way when power-off starts, and systemd's own console
# lines then land inside its lines.
{ echo "PHONE-CTS-REPORT-BEGIN"; cat /tmp/cts.json; echo; echo "PHONE-CTS-REPORT-END"; } |
	sed 's/^/PHONE-CTS-REPORT: /' > /dev/console
# The crash case: phone-shell as a service with an fd store crashes on
# purpose; the instance systemd restarts takes the running apps over.
rm -f /tmp/fdstore-resume.json /tmp/fdstore.out
# (phone-fdstore-check.service restarts it once, after that crash.)
systemctl start phone-fdstore-check.service
# Done when the restarted shell prints its result (the first run only
# crashes); the unit's state flickers through failed on the way.
for i in $(seq 1 300); do
	grep -q 'PHONE-SELFTEST-RESULT' /tmp/fdstore.out 2>/dev/null && break
	sleep 1
done
if grep -q 'PHONE-SELFTEST-RESULT: PASS' /tmp/fdstore.out && grep -q 'shell-crash' /tmp/fdstore.out; then
	sed 's/^/phone-fdstore: /' /tmp/fdstore.out | grep -E 'PASS|line |RESULT'
	echo "PHONE-FDSTORE: PASS"
else
	# The failing step with the whole screen, then the end of the shell's log.
	sed -n '/^  line [0-9]*:/,/^  processes:/p' /tmp/fdstore.out | sed 's/^/phone-fdstore: /'
	grep -a 'shell: ' /tmp/fdstore.out | tail -60 | sed 's/^/phone-fdstore-shell: /'
	systemctl status --no-pager phone-fdstore-check 2>&1 | sed 's/^/phone-fdstore-status: /' | head -30
	journalctl -u phone-fdstore-check --no-pager -o short-monotonic 2>&1 | sed 's/^/phone-fdstore-journal: /' | tail -40
	systemctl stop phone-fdstore-check 2>/dev/null
	echo "PHONE-FDSTORE: FAIL"
fi
systemctl poweroff --no-block
