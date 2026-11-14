#!/bin/sh
# Run inside `kas-container shell`: the system update test (R37) on a copy
# of the A/B disk image, kept across boots. Every boot does one step
# (phone.ota-test=<step>, phone-ota's ota-test) and powers off; the
# bundles are served from here, which QEMU's user network shows as
# 10.0.2.2. Between boots the disk is tampered with, as an attacker with
# the powered-off phone could: a slot's data, a slot's verity descriptor.
# Bundles are adaptive: each install reports how much of the bundle it
# downloaded. Health: a slot counts as up once the shell came up with its
# renderer and started an app; booted without a display (novga) it never
# does, and is rolled back.
set -u
# The helper scripts next to this one (the tree is /repo, or /kario in the internal workspace).
here=$(cd "$(dirname "$0")" && pwd)
cd /work/build
deploy=tmp/deploy/images/qemuarm64
log=/work/build/qemu-ota.log
www=/work/build/ota-www
: > "$log"
# runqemu takes a disk image only by a name with ".rootfs." in it; the
# qemuboot.conf (kernel, machine) comes from the deploy directory.
conf=$(readlink -f $deploy/phone-image-qemuarm64.rootfs.qemuboot.conf)
disk=/work/build/ota-disk.rootfs.wic
cp "$(readlink -f $deploy/phone-image-qemuarm64.rootfs.wic)" "$disk"
rm -rf "$www" && mkdir -p "$www"
cp "$(readlink -f $deploy/phone-bundle-qemuarm64.raucb)" "$www/phone.raucb"
cp "$(readlink -f $deploy/phone-bundle-untrusted-qemuarm64.raucb)" "$www/untrusted.raucb"
size=$(stat -c %s "$www/phone.raucb")
printf '{"version": "0.6", "notes": "Update test", "size": %d, "url": "phone.raucb"}\n' "$size" > "$www/latest.json"
# A staged rollout at 0 % and a paused one: offered to no phone.
mkdir -p "$www/staged" "$www/paused"
printf '{"version": "0.6", "size": %d, "url": "../phone.raucb", "rollout": 0}\n' "$size" > "$www/staged/latest.json"
printf '{"version": "0.6", "size": %d, "url": "../phone.raucb", "rollout": 100, "paused": true}\n' "$size" > "$www/paused/latest.json"
# OTA_SERVER (an update server binary) takes the phones' reports, checking
# their signatures; without it ota-www.py keeps them as sent and
# phone-ota-verify checks them at the end. OTA_ROLLOUT (a staged-rollout
# tool taking the directory) runs over the reports kept.
if [ -n "${OTA_SERVER:-}" ]; then
	OTA_REPORT_TO=http://127.0.0.1:8795 python3 "$here"/ota-www.py 8789 "$www" > /work/build/ota-www.log 2>&1 &
else
	python3 "$here"/ota-www.py 8789 "$www" > /work/build/ota-www.log 2>&1 &
fi
server=$!
otasrv=
debugfs=$(command -v debugfs || ls tmp/work/qemuarm64-poky-linux/phone-image/1.0/recipe-sysroot-native/sbin/debugfs)

# boot [novga] <kernel parameters>: one step; novga: no display device.
boot() {
	vga=
	[ "$1" = novga ] && vga=novga && shift
	echo "=== boot: $vga $*" >> "$log"
	from=$(wc -l < /work/build/ota-www.log)
	timeout 900 runqemu "$conf" "$disk" nographic $vga slirp bootparams="$*" </dev/null >> "$log" 2>&1
	got=$(tail -n +$((from + 1)) /work/build/ota-www.log | awk '$2 == "/phone.raucb" { n += $3 } END { print n + 0 }')
	[ "$got" -gt 0 ] && echo "PHONE-OTA: downloaded $got of $size bytes of the bundle" >> "$log"
}

# part <name>: "<first byte> <size in bytes>" of a GPT partition of the disk.
part() {
	python3 - "$disk" "$1" <<'EOF'
import struct, sys
with open(sys.argv[1], "rb") as d:
    d.seek(512)
    start, count, size = struct.unpack_from("<QII", d.read(92), 72)
    d.seek(start * 512)
    for _ in range(count):
        e = d.read(size)
        if e[56:128].decode("utf-16-le").rstrip("\0") == sys.argv[2]:
            first, last = struct.unpack_from("<QQ", e, 32)
            print(first * 512, (last - first + 1) * 512)
            break
EOF
}

# The ext4 superblock of slot A's file system, overwritten.
corrupt_data() {
	set -- $(part rootfs_a)
	dd if=/dev/urandom of="$disk" bs=1024 seek=$(($1 / 1024 + 1)) count=1 conv=notrunc 2>/dev/null
	echo "=== tampered: slot a's file system" >> "$log"
}

# Slot A's verity descriptor in the state partition, with another root hash
# (the signature stays the old one).
tamper_descriptor() {
	set -- $(part state)
	dd if="$disk" of=/work/build/ota-state.img bs=1M iflag=skip_bytes,count_bytes skip=$1 count=$2 2>/dev/null
	"$debugfs" -R "cat rootfs_a.verity" /work/build/ota-state.img 2>/dev/null |
		sed 's/^root_hash=0/root_hash=1/; t; s/^root_hash=./root_hash=0/' > /work/build/ota-bad.verity
	"$debugfs" -w -R "rm rootfs_a.verity" /work/build/ota-state.img >/dev/null 2>&1
	"$debugfs" -w -R "write /work/build/ota-bad.verity rootfs_a.verity" /work/build/ota-state.img >/dev/null 2>&1
	dd if=/work/build/ota-state.img of="$disk" bs=1M oflag=seek_bytes seek=$1 conv=notrunc 2>/dev/null
	echo "=== tampered: slot a's verity descriptor" >> "$log"
}

boot phone.ota-test=install phone.ota-held    # slot A: held back while staged or paused; the untrusted bundle is refused; the update goes to B
# Each phone's reports are checked against the root it printed (its own,
# not provisioned).
sed 's/\x1b\[[0-9;]*m//g' "$log" | tr -d '\r' | sed -n 's/.*PHONE-OTA: device root: //p' | tail -n 1 | tr ' ' '\n' > "$www/device-root.pub"
if [ -n "${OTA_SERVER:-}" ]; then
	"$OTA_SERVER" --listen 127.0.0.1:8795 --dir "$www" --device-root "$www/device-root.pub" > /work/build/ota-otasrv.log 2>&1 &
	otasrv=$!
fi
boot phone.ota-test=status phone.gui=1        # B comes up healthy, the shell with its renderer and an app: marked good
boot phone.ota-test=install                   # from B the update goes to A
boot novga phone.ota-test=status phone.gui=1  # A's renderer cannot start (no display): never healthy, three tries
boot novga phone.ota-test=status phone.gui=1
boot novga phone.ota-test=status phone.gui=1
boot phone.ota-test=status                    # tries spent: back to B
boot phone.ota-test=install                   # from B to A again
corrupt_data
boot phone.ota-test=status                    # A's data does not match its root hash: B, in the same boot
boot phone.ota-test=install                   # A rewritten
tamper_descriptor
boot phone.ota-test=status                    # A's descriptor does not verify: B, in the same boot
kill $server $otasrv
if [ -n "${OTA_ROLLOUT:-}" ]; then
	python3 "$OTA_ROLLOUT" "$www" >> "$log" 2>&1
fi
[ -n "${OTA_SERVER:-}" ] || "$here"/../build/bin/phone-ota-verify --root "$www/device-root.pub" "$www/reports.jsonl" >> "$log" 2>&1

clean=$(sed 's/\x1b\[[0-9;]*m//g' "$log" | tr -d '\r')
out=$(echo "$clean" | grep -a -o 'PHONE-OTA: .*')
echo "$out"
booted=$(echo "$out" | sed -n 's/.*booted=\([ab]\) .*/\1/p' | tr '\n' ' ')
echo "slots booted: $booted"
refused=$(echo "$clean" | grep -a -o 'phone-initramfs: slot a: .*')
echo "$refused"
# Each boot reports once on arrival; the installing ones once more after.
ok=1
[ "$booted" = "a a b b b a a a b b b b b b b " ] || ok=0
[ "$(echo "$out" | grep -c 'untrusted bundle refused')" = 4 ] || ok=0
[ "$(echo "$out" | grep -c ': installed')" = 4 ] || ok=0
[ "$(echo "$out" | grep -c 'held (staged): ')" = 1 ] || ok=0
[ "$(echo "$out" | grep -c 'held (paused): ')" = 1 ] || ok=0
# The healthy B ran the shell with its renderer and an app (the A without
# a display never did: the slots booted show it rolled back).
echo "$out" | grep 'booted=b ' | grep -q 'health=shell=' || ok=0
# B, marked good, told the update server so (the server's side: the shell
# may send it before the step's script looks, so the phone may not say),
# signed by the phone: the update server took it (it refuses unsigned
# ones), or every report kept verifies.
grep -q '"outcome":"good"' "$www/reports.jsonl" || ok=0
if [ -n "${OTA_SERVER:-}" ]; then
	grep '"outcome":"good"' "$www/reports.jsonl" | grep -q '"attested":true' || ok=0
	grep -q 'REPORT 403' /work/build/ota-www.log && ok=0
else
	grep -a -q 'OTA-VERIFY: PASS' "$log" || ok=0
fi
[ -z "${OTA_ROLLOUT:-}" ] || grep -a -q '0.6: rollout' "$log" || ok=0
# Adaptive: every install downloaded under a quarter of the bundle (the
# slots hold most blocks already).
[ "$(echo "$out" | grep -c 'downloaded ')" = 4 ] || ok=0
for n in $(echo "$out" | sed -n 's/.*downloaded \([0-9]*\) of.*/\1/p'); do
	[ $((n * 4)) -lt "$size" ] || ok=0
done
# Every system ran from its slot through dm-verity, read-only.
[ "$(echo "$out" | grep 'booted=' | grep -cv 'root=/dev/mapper/root_[ab]:ro health=')" = 0 ] || ok=0
[ "$(echo "$refused" | grep -c 'data does not match the root hash')" = 1 ] || ok=0
[ "$(echo "$refused" | grep -c 'signature does not check out')" = 1 ] || ok=0
echo "$out" | grep -q FAIL && ok=0
[ $ok = 1 ] && echo "PHONE-OTA-RESULT: PASS" || echo "PHONE-OTA-RESULT: FAIL"
[ $ok = 1 ]
