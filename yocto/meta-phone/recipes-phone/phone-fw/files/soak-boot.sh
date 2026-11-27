#!/bin/sh
# The soak run on the device (phone.soak=<rounds> on the kernel command
# line; make yocto-soak): one Shell runs that many rounds of soak.script
# (the rounds of make soak: app launches, chat over push, video calls,
# notifications, a tombstone, a cellular call, SMS, music, the calculator)
# with its services and the pixel renderer, while the Shell's and each of
# its child processes' resident memory and open descriptors are sampled
# every 30 seconds. It fails when a round fails, a service is restarted, or
# a process grows past its limit: the lowest sample of the last third above
# the highest of the first third (the rule of shell/soak_test.go). Then it
# powers off.
export HOME=/tmp
rounds=$(sed -n 's/.*phone\.soak=\([0-9][0-9]*\).*/\1/p' /proc/cmdline)
[ -n "$rounds" ] || rounds=20
src=/usr/share/phone/soak.script
script=/tmp/soak.script
: > "$script"
i=0
while [ "$i" -lt "$rounds" ]; do
	cat "$src" >> "$script"
	i=$((i + 1))
done

# sample: the Shell and its children but the apps (they come and go):
# seconds, name, pid, resident kB, open descriptors.
sample() {
	shell=$1
	while :; do
		t=$(cut -d. -f1 /proc/uptime)
		for p in /proc/[0-9]*; do
			pid=${p#/proc/}
			ppid=$(awk '/^PPid:/ {print $2}' "$p/status" 2>/dev/null)
			[ "$pid" = "$shell" ] || [ "$ppid" = "$shell" ] || continue
			name=$(cat "$p/comm" 2>/dev/null)
			case "$name" in phone-app-*|kario-jsrt|phone-sandbox|phone-webview*) continue ;; esac
			rss=$(awk '/^VmRSS:/ {print $2}' "$p/status" 2>/dev/null)
			fds=$(ls "$p/fd" 2>/dev/null | wc -l)
			echo "$t $name $pid ${rss:-0} $fds"
		done
		sleep 30
	done
}

start=$(cut -d. -f1 /proc/uptime)
/usr/bin/phone-shell --script "$script" --gui auto --timeout-scale 4 > /tmp/soak.out 2>&1 &
shell=$!
sample "$shell" > /tmp/soak.samples &
sampler=$!
wait "$shell"
rc=$?
end=$(cut -d. -f1 /proc/uptime)
kill "$sampler" 2>/dev/null

# growth: per process (name and pid; a restarted service is a new series),
# leaving out the first sample. Limits in kB and descriptors.
report=$(awk '
	$1 != "" { key = $2 " " $3; n[key]++; i = n[key]; if (i == 1) next
		rss[key, i] = $4; fds[key, i] = $5; last[key] = i }
	END {
		bad = 0
		for (key in last) {
			m = last[key] - 1; if (m < 6) { print key ": " m " samples"; continue }
			third = int(m / 3); hi_r = 0; hi_f = 0; lo_r = -1; lo_f = -1
			for (j = 2; j < 2 + third; j++) { if (rss[key, j] > hi_r) hi_r = rss[key, j]; if (fds[key, j] > hi_f) hi_f = fds[key, j] }
			for (j = last[key] - third + 1; j <= last[key]; j++) {
				if (lo_r < 0 || rss[key, j] < lo_r) lo_r = rss[key, j]; if (lo_f < 0 || fds[key, j] < lo_f) lo_f = fds[key, j] }
			split(key, k, " "); shell = (k[1] == "phone-shell")
			lim_r = shell ? 49152 : 16384; lim_f = shell ? 16 : 8
			g_r = lo_r - hi_r; g_f = lo_f - hi_f
			verdict = (g_r > lim_r || g_f > lim_f) ? "LEAK" : "ok"
			if (verdict == "LEAK") bad = 1
			printf "%s: rss %d -> %d kB (growth %+d, limit %d), fds %d -> %d (growth %+d, limit %d) %s\n", key, rss[key, 2], rss[key, last[key]], g_r, lim_r, fds[key, 2], fds[key, last[key]], g_f, lim_f, verdict
		}
		if (bad) print "GROWTH: FAIL"
	}' /tmp/soak.samples)

ok=1
[ "$rc" = 0 ] || ok=0
echo "$report" | grep -q 'GROWTH: FAIL' && ok=0
restarted=$(grep -c 'exited (.*); restarting' /tmp/soak.out)
[ "$restarted" = 0 ] || ok=0
{
	echo "PHONE-SOAK-BEGIN"
	echo "rounds: $rounds in $((end - start)) s; script exit code $rc; service restarts $restarted"
	echo "$report"
	grep -E 'FAIL|line [0-9]+' /tmp/soak.out | tail -20
	if [ "$ok" = 0 ]; then
		sed -n '/^  line [0-9]*:/,/^  processes:/p' /tmp/soak.out | tail -60
		tail -40 /tmp/soak.out
	fi
	echo "PHONE-SOAK-END"
	[ "$ok" = 1 ] && echo "PHONE-SOAK-RESULT: PASS" || echo "PHONE-SOAK-RESULT: FAIL"
} > /tmp/soak-report.txt
# A console file: the kernel's lines can land inside the report's
# (yocto/qemu-soak.sh, qemu-console.py report).
/usr/libexec/phone/console-file soak-report.txt /tmp/soak-report.txt > /dev/console
systemctl poweroff --no-block
