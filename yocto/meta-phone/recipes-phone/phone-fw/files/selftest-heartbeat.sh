#!/bin/sh
# The boot selftest's heartbeat (selftest-boot.sh starts it as a unit of its
# own, outside phone-selftest.service's cgroup, so it still beats when what
# runs there is frozen or ended). Through the kernel's log: its console
# output is polled, not the tty's, so it still comes when writes to
# /dev/console hang. Eight minutes without a new line from the run (the
# longest step waits two): the run is stuck; the service's groups and
# processes, the blocked tasks (SysRq w) and, from SIGQUIT, every goroutine
# of phone-shell go to the log, which needs printk.devkmsg=on
# (qemu-selftest.sh). The run's output is in $1.
out=$1
svc=/sys/fs/cgroup/system.slice/phone-selftest.service
say() { echo "$1" > /dev/kmsg; }
last="" same=0
while sleep 30; do
	cur=$(tail -n 1 "$out" 2>/dev/null | cut -c1-160)
	say "phone-heartbeat: up $(cut -d' ' -f1 /proc/uptime) s, $(grep MemAvailable /proc/meminfo | tr -s ' '), last: $cur"
	if [ "$cur" = "$last" ]; then same=$((same + 1)); else same=0 last=$cur; fi
	[ "$same" = 16 ] || continue
	say "phone-stuck: no new line for 8 minutes"
	find "$svc" -type d 2>/dev/null | while IFS= read -r g; do
		say "phone-stuck: ${g#"$svc"}/ $(tr '\n' ' ' < "$g/cgroup.events" 2>/dev/null)"
		while IFS= read -r p; do
			say "phone-stuck:   $p $(cut -d' ' -f2,3 "/proc/$p/stat" 2>/dev/null) wchan $(cat "/proc/$p/wchan" 2>/dev/null)"
		done < "$g/cgroup.procs"
	done
	echo w > /proc/sysrq-trigger 2>/dev/null
	killall -QUIT phone-shell
	sleep 10
	sed -n '/^SIGQUIT/,$p' "$out" | head -n 600 | while IFS= read -r l; do say "phone-stuck: $l"; done
done
