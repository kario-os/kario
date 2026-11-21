#!/bin/sh
# Run inside `kas-container shell` with the web overlay: boot with
# phone.bench=1, the image measures both routes and powers off.
set -u
cd /work/build
log=/work/build/qemu-bench.log
echo "booting qemuarm64 for the semantic-vs-web benchmark; log: $log"
timeout 3000 runqemu qemuarm64 nographic slirp snapshot bootparams="phone.bench=1" </dev/null >"$log" 2>&1
echo "qemu exited with $?"
sed -n '/PHONE-BENCH-BEGIN/,/PHONE-BENCH-END/p' "$log" | sed 's/\x1b\[[0-9;]*m//g; s/^\[ *[0-9.]*\] bench-boot.sh\[[0-9]*\]: //'
grep -aq "PHONE-BENCH-END" "$log"
