#!/bin/sh
# Run inside `kas-container shell` with the web overlay and a 1 GB SKU
# (make yocto-stress): boots with phone.stress=<rounds>; the image runs the
# stress rounds (stress-boot.sh), prints its report and powers off.
set -u
cd /work/build
log=/work/build/qemu-stress.log
rounds=${PHONE_STRESS_ROUNDS:-5}
echo "booting qemuarm64 for the low-memory stress run ($rounds rounds); log: $log"
timeout 5400 runqemu qemuarm64 nographic slirp snapshot bootparams="phone.stress=$rounds" </dev/null >"$log" 2>&1
echo "qemu exited with $?"
sed -n '/PHONE-STRESS-BEGIN/,/PHONE-STRESS-RESULT/p' "$log" | sed 's/\x1b\[[0-9;]*m//g; s/^.*phone-stress: //'
grep -aq "PHONE-STRESS-RESULT: PASS" "$log"
