#!/bin/sh
# The low-memory stress run (phone.stress=<rounds> on the kernel command
# line; make yocto-stress boots the web image in 1 GB): stress.script's
# rounds of SMS, chat, photos, a video and the browser over it, with music
# playing in the background, through the shell and the pixel renderer,
# while memory is sampled every two seconds. It passes when every step
# finished in its time, the shell's renderer never had to be restarted, no
# system service was ended or restarted, and the kernel's OOM killer never
# had to act outside an app's own memory limit: the shell's low-memory
# killer ends background pages and apps first. Then it powers off.
export HOME=/tmp
rounds=$(sed -n 's/.*phone\.stress=\([0-9][0-9]*\).*/\1/p' /proc/cmdline)
[ -n "$rounds" ] || rounds=5
src=/usr/share/phone/stress.script
script=/tmp/stress.script
{
	sed '/^#round/,$d' "$src"
	i=1
	while [ "$i" -le "$rounds" ]; do
		sed '1,/^#round/d' "$src" | sed "s/{n}/$i/g"
		i=$((i + 1))
	done
} > "$script"

units="phone-telephonyd phone-pushd phone-pushgw phone-audiod phone-mediad phone-keystored phone-imed phone-chatsrv phone-storesrv"
restarts() {
	for u in $units; do
		printf '%s=%s ' "$u" "$(systemctl show -p NRestarts --value "$u.service" 2>/dev/null)"
	done
}
before=$(restarts)
oom_before=$(dmesg | grep -c 'Out of memory')

# snapshot: where the memory and the pressure are, every group using more
# than 20 MB (its limit, how often it hit it, its pressure) and the biggest
# processes; the sampler keeps the latest for a failure's report.
snapshot() {
	echo "snapshot at $(cut -d' ' -f1 /proc/uptime) s"
	find /sys/fs/cgroup -name memory.current | while read -r f; do
		d=${f%/memory.current}
		cur=$(cat "$f")
		[ "$cur" -gt 20971520 ] || continue
		echo "cgroup ${d#/sys/fs/cgroup}: $((cur / 1048576)) MB of $(cat "$d/memory.max" 2>/dev/null); $(tr '\n' ' ' < "$d/memory.events" 2>/dev/null); $(sed -n 's/^full //p' "$d/memory.pressure" 2>/dev/null)"
	done
	for p in /proc/[0-9]*; do
		echo "$(awk '/^VmRSS:/ {print $2}' "$p/status" 2>/dev/null) kB $(cat "$p/comm" 2>/dev/null) $(cut -d: -f3 "$p/cgroup" 2>/dev/null)"
	done | sort -rn | head -12
}

sample() {
	n=0
	while :; do
		avail=$(awk '/^MemAvailable:/ {print $2}' /proc/meminfo)
		some=$(sed -n 's/^some avg10=\([0-9.]*\).*/\1/p' /proc/pressure/memory 2>/dev/null)
		full=$(sed -n 's/^full avg10=\([0-9.]*\).*/\1/p' /proc/pressure/memory 2>/dev/null)
		echo "$(cut -d' ' -f1 /proc/uptime) ${avail:-0} ${some:--} ${full:--}"
		n=$((n + 1))
		if [ $((n % 5)) = 0 ]; then
			snapshot >> /tmp/mem.snaps 2>&1
		fi
		sleep 2
	done
}
sample > /tmp/mem.log &
sampler=$!

start=$(cut -d. -f1 /proc/uptime)
/usr/bin/phone-shell --script "$script" --gui auto --pixel-dir /tmp/pixel --timeout-scale 4 > /tmp/stress.out 2>&1
rc=$?
end=$(cut -d. -f1 /proc/uptime)
kill "$sampler" 2>/dev/null

after=$(restarts)
total=$(awk '/^MemTotal:/ {print $2}' /proc/meminfo)
min_avail=$(awk 'NR == 1 || $2 < m { m = $2 } END { print m + 0 }' /tmp/mem.log)
max_some=$(awk '$3 != "-" && $3 + 0 > m { m = $3 + 0 } END { print m + 0 }' /tmp/mem.log)
max_full=$(awk '$4 != "-" && $4 + 0 > m { m = $4 + 0 } END { print m + 0 }' /tmp/mem.log)
pages=$(grep -c 'ending the pages of background app' /tmp/stress.out)
apps=$(grep -c 'ending background app' /tmp/stress.out)
limits=$(grep -c 'killed for its memory limit' /tmp/stress.out)
renderer=$(grep -c 'renderer restarted:' /tmp/stress.out)
oom=$(( $(dmesg | grep -c 'Out of memory') - oom_before ))
# The kernel's kills inside an app's cgroup are that app's own limit.
oom_system=$(dmesg | grep 'Out of memory' | tail -n "$oom" 2>/dev/null | grep -vc 'memory cgroup out of memory')
[ "$oom" -gt 0 ] || oom_system=0

ok=1
[ "$rc" = 0 ] || ok=0
[ "$renderer" = 0 ] || ok=0
[ "$before" = "$after" ] || ok=0
[ "$oom_system" = 0 ] || ok=0
{
	echo "PHONE-STRESS-BEGIN"
	echo "rounds: $rounds in $((end - start)) s; script exit code $rc"
	echo "memory: total $((total / 1024)) MB, lowest available $((min_avail / 1024)) MB"
	echo "pressure (avg10, %): some up to $max_some, full up to $max_full"
	echo "low-memory killer: $pages background pages and $apps background apps ended"
	echo "apps killed at their own memory limit: $limits; kernel OOM kills outside them: $oom_system"
	echo "renderer restarts: $renderer"
	echo "service restarts before: $before"
	echo "service restarts after:  $after"
	grep -E 'PASS|FAIL|line [0-9]+' /tmp/stress.out | tail -40
	if [ "$ok" = 0 ]; then
		# The failing step with its screen, then the end of the logs.
		sed -n '/^  line [0-9]*:/,/^  processes:/p' /tmp/stress.out
		tail -60 /tmp/stress.out
		# Every app and web process started and ended, and why.
		grep -a -E 'spawned|exited|ended|stopped|killed|tombstone|low memory|memory limit' /tmp/stress.out | tail -80
		dmesg | grep -A3 'Out of memory' | tail -20
		# The services behind messages and pushes log to the journal.
		journalctl -b --no-pager -o short-monotonic -u phone-chatsrv -u phone-pushgw -u phone-pushd 2>&1 | tail -40
		awk '/^snapshot at/ { last = "" } { last = last $0 "\n" } END { printf "%s", last ? last : "no memory snapshot\n" }' /tmp/mem.snaps 2>/dev/null || echo 'no memory snapshot'
	fi
	echo "PHONE-STRESS-END"
	[ "$ok" = 1 ] && echo "PHONE-STRESS-RESULT: PASS" || echo "PHONE-STRESS-RESULT: FAIL"
} | sed 's/^/phone-stress: /' > /dev/console
systemctl poweroff --no-block
