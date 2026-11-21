#!/bin/sh
# Semantic-vs-web comparison on the device, then power off (phone.bench=1).
export HOME=/tmp
B=/usr/share/phone/bench/bench.py
echo "PHONE-BENCH-BEGIN"
python3 $B tui --name phone-shell --runs 3 --timeout 120 --cmd 'phone-shell --services spawn --sim-dir {tmp}'
if command -v cog >/dev/null; then
	python3 $B web --name wpe --root /usr/share/phone/web --runs 3 --timeout 180 --cmd 'cog -P headless {url}'
fi
du -sk /usr/bin/phone-* /usr/lib/phone/bin 2>/dev/null | awk '{s+=$1} END {print "PHONE-BENCH-DISK semantic_kb=" s}'
du -skL /usr/lib/libWPEWebKit* /usr/lib/wpe-webkit* /usr/libexec/wpe-webkit* /usr/bin/cog /usr/lib/libcog* 2>/dev/null | awk '{s+=$1} END {print "PHONE-BENCH-DISK wpe_kb=" s}'
echo "PHONE-BENCH-END"
systemctl poweroff --no-block
