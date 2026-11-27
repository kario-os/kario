#!/bin/sh
# Run inside `kas-container shell` with the web overlay and a 1 GB SKU
# (make yocto-stress): boots with phone.stress=<rounds>; the image runs the
# stress rounds (stress-boot.sh), prints its report and powers off.
set -u
# The helper scripts next to this one (the tree is /repo, or /kario in the internal workspace).
here=$(cd "$(dirname "$0")" && pwd)
cd /work/build
log=/work/build/qemu-stress.log
rounds=${PHONE_STRESS_ROUNDS:-5}
echo "booting qemuarm64 for the low-memory stress run ($rounds rounds); log: $log"
timeout 5400 runqemu qemuarm64 nographic slirp snapshot bootparams="phone.stress=$rounds" </dev/null >"$log" 2>&1
echo "qemu exited with $?"
# The report is a console file (an older image's plain lines are read as
# they are, their prefixes on them).
python3 "$here"/qemu-console.py report "$log" PHONE-STRESS
