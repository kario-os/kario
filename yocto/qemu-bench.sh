#!/bin/sh
# Run inside `kas-container shell` with the web overlay: boot with
# phone.bench=1, the image measures both routes and powers off.
set -u
# The helper scripts next to this one (the tree is /repo, or /kario in the internal workspace).
here=$(cd "$(dirname "$0")" && pwd)
cd /work/build
log=/work/build/qemu-bench.log
echo "booting qemuarm64 for the semantic-vs-web benchmark; log: $log"
timeout 3000 runqemu qemuarm64 nographic slirp snapshot bootparams="phone.bench=1" </dev/null >"$log" 2>&1
echo "qemu exited with $?"
# The report is a console file (an older image's plain lines are read as
# they are, their prefixes on them).
python3 "$here"/qemu-console.py report "$log" PHONE-BENCH
