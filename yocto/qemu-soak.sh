#!/bin/sh
# Run inside `kas-container shell` (make yocto-soak): boots with
# phone.soak=<rounds>; the image runs the soak (soak-boot.sh), prints its
# report and powers off.
set -u
cd /work/build
log=/work/build/qemu-soak.log
rounds=${PHONE_SOAK_ROUNDS:-20}
echo "booting qemuarm64 for the soak run ($rounds rounds); log: $log"
timeout $((rounds * 600 + 1800)) runqemu qemuarm64 nographic slirp snapshot bootparams="phone.soak=$rounds" </dev/null >"$log" 2>&1
echo "qemu exited with $?"
sed -n '/PHONE-SOAK-BEGIN/,/PHONE-SOAK-RESULT/p' "$log" | sed 's/\x1b\[[0-9;]*m//g; s/^.*phone-soak: //'
grep -aq "PHONE-SOAK-RESULT: PASS" "$log"
