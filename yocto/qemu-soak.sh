#!/bin/sh
# Run inside `kas-container shell` (make yocto-soak): boots with
# phone.soak=<rounds>; the image runs the soak (soak-boot.sh), prints its
# report and powers off.
set -u
# The helper scripts next to this one (the tree is /repo, or /kario in the internal workspace).
here=$(cd "$(dirname "$0")" && pwd)
cd /work/build
log=/work/build/qemu-soak.log
rounds=${PHONE_SOAK_ROUNDS:-20}
echo "booting qemuarm64 for the soak run ($rounds rounds); log: $log"
timeout $((rounds * 600 + 1800)) runqemu qemuarm64 nographic slirp snapshot bootparams="phone.soak=$rounds" </dev/null >"$log" 2>&1
echo "qemu exited with $?"
# The report is a console file (an older image's plain lines are read as
# they are, their prefixes on them).
python3 "$here"/qemu-console.py report "$log" PHONE-SOAK
