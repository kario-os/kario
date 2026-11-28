#!/bin/sh
# Semantic-vs-web comparison on the device, then power off (phone.bench=1).
# It passes when both routes were measured, each in at least one of its
# runs: bench.py needs python3 and the web route cog, which only the web
# image has (make yocto-bench builds it). The report is a console file
# printed as it grows, then its end (yocto/qemu-bench.sh, qemu-console.py
# report): the kernel's lines can land inside its lines.
export HOME=/tmp
B=/usr/share/phone/bench/bench.py
report=/tmp/bench-report.txt
# route <name> <bench.py's arguments>: bench.py's summary line, or a
# PHONE-BENCH-FAIL line saying why there is none.
route() {
	name=$1
	shift
	out=$(python3 $B "$@" --name "$name" 2>/tmp/bench-"$name".err)
	rc=$?
	[ -z "$out" ] || echo "$out"
	case $out in
	*'"ok_runs": '[1-9]*) ;;
	*)
		echo "PHONE-BENCH-FAIL: $name: not measured (exit code $rc): $(tail -c 400 /tmp/bench-"$name".err | tr '\n' ' ')"
		ok=0
		;;
	esac
}
{
	echo "PHONE-BENCH-BEGIN"
	ok=1
	route phone-shell tui --runs 3 --timeout 120 --cmd 'phone-shell --services spawn --sim-dir {tmp}'
	if command -v cog >/dev/null; then
		route wpe web --root /usr/share/phone/web --runs 3 --timeout 180 --cmd 'cog -P headless {url}'
	else
		echo "PHONE-BENCH-FAIL: wpe: no cog in this image (the web image has it: make yocto-bench)"
		ok=0
	fi
	# phone-shell and most services are links to kario-system (du: 0).
	du -sk /usr/bin/kario-system /usr/bin/phone-* /usr/lib/phone/bin 2>/dev/null | awk '{s+=$1} END {print "PHONE-BENCH-DISK semantic_kb=" s + 0}'
	du -skL /usr/lib/libWPEWebKit* /usr/lib/wpe-webkit* /usr/libexec/wpe-webkit* /usr/bin/cog /usr/lib/libcog* 2>/dev/null | awk '{s+=$1} END {print "PHONE-BENCH-DISK wpe_kb=" s + 0}'
	echo "PHONE-BENCH-END"
	[ "$ok" = 1 ] && echo "PHONE-BENCH-RESULT: PASS" || echo "PHONE-BENCH-RESULT: FAIL"
} | tee "$report" | /usr/libexec/phone/console-file bench-report.txt /dev/stdin 1 > /dev/console
/usr/libexec/phone/console-file bench-report.txt "$report" end > /dev/console
systemctl poweroff --no-block
