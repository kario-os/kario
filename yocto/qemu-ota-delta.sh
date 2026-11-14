#!/bin/sh
# Run inside `kas-container shell`: system updates across versions
# (25-system-update.md §3, §5), through Settings › System update on the
# emulator (phone-ota's ota-test, one step a boot, as qemu-ota.sh does).
#
# `save` keeps this build as the base: its A/B disk, its slot image, the list
# of its files and the payload formats its own phone-ota-delta applies.
# `run`, after changes and a rebuild, makes the delta bundles from the base
# (phone-bundle-delta: the payload phone-ota-delta makes from the base's slot
# to this build's, with the applier unless the base's own reads its format)
# and goes through these lanes on fresh copies of the base disk (an older
# base's own ota-test runs its steps, so the hook's log and the power cut
# come from this build's):
#   incremental: latest.json lists the deltas by the root hash they start
#     from; the base takes its own (and refuses one for another version);
#     the new slot, kept from being healthy, rolls back to the base; the base
#     installs it again, and B comes up; B then updates A with this build's
#     delta from itself (B to A, the system's own applier);
#   power cut (on, once B runs this build): the power goes while B writes
#     this build's delta into A; B comes up as before, then updates A;
#   damaged delta: an operation's data does not match; the applier stops,
#     and the shell installs the full bundle instead;
#   adaptive: the full bundle, for comparison;
# then it reports what each install downloaded (and read twice), the
# applier's time and memory, and what changed (ota-delta.py).
# `bundle` makes delta bundles from earlier slot images or A/B disks (make
# yocto-bundle OTA_DELTA_FROM=, for kario-internal's release tool);
# <label>@<formats> says the earlier system's own phone-ota-delta applies
# those payload formats (1,2), and its bundle leaves the applier out when
# the payload's is among them.
# `release` boots earlier releases' A/B disks (dist/v<version>/) and updates
# them through Settings to a later release, from its own latest.json and
# bundles: the delta for that version when the old system's shell picks
# deltas, else the full bundle.
#   qemu-ota-delta.sh save | run | bundle <out dir> <label>[@<formats>]=<slot image or disk>...
#   qemu-ota-delta.sh release <new dist dir> <old dist dir>...
set -u
here=$(cd "$(dirname "$0")" && pwd)
cd /work/build
deploy=tmp/deploy/images/qemuarm64
rootfs=tmp/work/qemuarm64-poky-linux/phone-image/1.0/rootfs
base=/work/build/ota-base
# phone-ota-delta for this machine, which makes payloads, and for the phone,
# which each delta bundle carries to apply its payload (make ota-delta).
gen=$here/../build/bin/phone-ota-delta
applier=$here/../build/ota/linux-arm64/phone-ota-delta
log=/work/build/qemu-ota-delta.log
www=/work/build/ota-delta-www
wwwlog=/work/build/ota-delta-www.log
disk=/work/build/ota-delta.rootfs.wic

# payload_format <payload>: the big-endian 32-bit number after "KDLT".
payload_format() {
	od -An -tu1 -j4 -N4 "$1" | awk 'NF == 4 { print $1 * 16777216 + $2 * 65536 + $3 * 256 + $4 }'
}

# signed_bundle <out> <formats>: /work/build/ota-delta/rootfs.kdelta
# signed into a bundle by phone-bundle-delta, with the applier unless
# <formats> (the source system's own applier's, comma-separated) has the
# payload's; whose applier the hook will run in <out>.applier (system or
# bundle).
signed_bundle() {
	fmt=$(payload_format /work/build/ota-delta/rootfs.kdelta)
	case ",${2:-}," in
	*",$fmt,"*) echo system > "$1.applier" ;;
	*)
		install -m 0755 "$applier" /work/build/ota-delta/phone-ota-delta
		echo bundle > "$1.applier"
		;;
	esac
	if ! bitbake phone-bundle-delta > "$1.bitbake.log" 2>&1; then
		echo "PHONE-OTA-DELTA: FAIL: bitbake phone-bundle-delta:"
		tail -n 20 "$1.bitbake.log"
		return 1
	fi
	cp "$(readlink -f $deploy/phone-bundle-delta-qemuarm64.raucb)" "$1"
}

# delta_bundle <label> <source> <out> [<formats>]: the payload from <source>
# to this build's slot image (kept as <out>.kdelta), signed into a bundle;
# the generator's output and report beside it (<out>.log, <out>.json).
delta_bundle() {
	if [ ! -x "$gen" ] || [ ! -x "$applier" ]; then
		echo "PHONE-OTA-DELTA: no phone-ota-delta in $here/../build (make ota-delta)"
		return 1
	fi
	rm -rf /work/build/ota-delta && mkdir -p /work/build/ota-delta
	if ! "$gen" generate --source "$2" --source-version "$1" --target "$(readlink -f $deploy/phone-image-qemuarm64.rootfs.ext4.verity)" \
		--out /work/build/ota-delta/rootfs.kdelta --report "$3.json" > "$3.log" 2>&1; then
		echo "PHONE-OTA-DELTA: FAIL: no payload from $2:"
		tail -n 5 "$3.log"
		return 1
	fi
	cp /work/build/ota-delta/rootfs.kdelta "$3.kdelta"
	signed_bundle "$3" "${4:-}"
}

# damaged_bundle <payload> <out> [<formats>]: <payload> with one byte three
# quarters in (an operation's data, after earlier operations wrote the slot)
# inverted, signed into a bundle.
damaged_bundle() {
	rm -rf /work/build/ota-delta && mkdir -p /work/build/ota-delta
	cp "$1" /work/build/ota-delta/rootfs.kdelta
	python3 - /work/build/ota-delta/rootfs.kdelta <<'EOF'
import sys
with open(sys.argv[1], "r+b") as f:
    at = f.seek(0, 2) * 3 // 4
    f.seek(at)
    b = f.read(1)
    f.seek(at)
    f.write(bytes([b[0] ^ 0xFF]))
EOF
	signed_bundle "$2" "${3:-}"
}

# trusted <descriptor>: signed with this build's update keys, the check the
# initramfs makes (slot-init). runqemu boots the base disk with this build's
# kernel and initramfs, and the base accepts only bundles signed with the
# same keys, so a base from another build directory (other ota-keys) cannot
# take part.
trusted() {
	[ -f "$1" ] && [ -f "$1.sig" ] && openssl cms -verify -binary -inform DER -in "$1.sig" -content "$1" \
		-CAfile /work/ota-keys/ca.cert.pem -purpose any -no_check_time -out /dev/null 2> /dev/null
}

# The commit, "-dirty" with changes not committed, and the image's build time.
version() {
	rev=$(git -C "$here/.." describe --always --dirty 2>/dev/null || echo unknown)
	echo "$rev $(basename "$(readlink -f $deploy/phone-image-qemuarm64.rootfs.ext4.verity)" | grep -o '[0-9]\{14\}')"
}

root_hash() { sed -n 's/^root_hash=//p' "$1"; }

# boot <label> [novga] <kernel parameters>: one step of ota-test; its
# requests to the server in $wwwlog.<label>.
boot() {
	label=$1
	shift
	vga=
	[ "$1" = novga ] && vga=novga && shift
	echo "=== boot $label: $vga $*" >> "$log"
	from=$(wc -l < "$wwwlog")
	timeout 900 runqemu "$conf" "$disk" nographic $vga slirp bootparams="$*" </dev/null >> "$log" 2>&1
	tail -n +$((from + 1)) "$wwwlog" > "$wwwlog.$label"
}

# What a boot said (PHONE-OTA lines), and what it downloaded of a file.
said() { sed 's/\x1b\[[0-9;]*m//g' "$log" | tr -d '\r' | sed -n "/^=== boot $1:/,/^=== boot /p" | grep -a -o 'PHONE-OTA: .*'; }
got() { awk -v p="/$2" '$2 == p { n += $3 } END { print n + 0 }' "$wwwlog.$1"; }
ok=1
# expect <label> <description> <extended regex>: a line the boot must have said.
expect() {
	if said "$1" | grep -a -q -E "$3"; then
		echo "  ok   $1: $2"
	else
		echo "  FAIL $1: $2"
		ok=0
	fi
}
# downloads <label> <file> <more than> [<less than>]: bytes it downloaded of <file>.
downloads() {
	n=$(got "$1" "$2")
	if [ "$n" -gt "$3" ] && { [ -z "${4:-}" ] || [ "$n" -lt "$4" ]; }; then
		echo "  ok   $1: downloaded $n bytes of $2"
	else
		echo "  FAIL $1: downloaded $n bytes of $2 (want more than $3${4:+ and less than $4})"
		ok=0
	fi
}

case "${1:-}" in
save)
	slot=$(readlink -f $deploy/phone-image-qemuarm64.rootfs.ext4.verity)
	trusted "$slot.desc" || { echo "PHONE-OTA-DELTA: FAIL: this build's slot descriptor does not check out against /work/ota-keys"; exit 1; }
	rm -rf "$base" && mkdir -p "$base"
	cp "$(readlink -f $deploy/phone-image-qemuarm64.rootfs.wic)" "$base/disk.rootfs.wic"
	cp "$slot" "$base/slot.img"
	cp "$slot.desc" "$base/slot.img.desc"
	cp "$slot.desc.sig" "$base/slot.img.desc.sig"
	python3 "$here"/ota-delta.py files "$rootfs" > "$base/files.tsv"
	# The image's own applier is this build's (make ota-delta builds both).
	if grep -q '^/usr/bin/phone-ota-delta	' "$base/files.tsv"; then
		"$gen" formats | paste -sd, - > "$base/formats"
	else
		: > "$base/formats"
	fi
	version > "$base/rev"
	echo "PHONE-OTA-DELTA: base saved ($(cat "$base/rev"), $(wc -l < "$base/files.tsv") files, its applier's payload formats: $(cat "$base/formats"))"
	exit 0
	;;
bundle)
	[ $# -ge 3 ] || { echo "usage: qemu-ota-delta.sh bundle <out dir> <label>[@<formats>]=<slot image or disk>..." >&2; exit 2; }
	out=$2
	shift 2
	mkdir -p "$out"
	for spec in "$@"; do
		label=${spec%%=*}
		formats=
		case $label in *@*) formats=${label#*@} label=${label%%@*} ;; esac
		delta_bundle "$label" "${spec#*=}" "$out/$label.raucb" "$formats" || exit 1
		rm -f "$out/$label.raucb.kdelta"
		echo "PHONE-OTA-DELTA: $out/$label.raucb, $(stat -c %s "$out/$label.raucb") bytes, the $(cat "$out/$label.raucb.applier")'s applier: $(grep -a -m1 -o 'payload .*' "$out/$label.raucb.log")"
	done
	exit 0
	;;
run | release) ;;
*)
	echo "usage: qemu-ota-delta.sh save | run | bundle <out dir> <label>=<slot image or disk>... | release <new dist dir> <old dist dir>..." >&2
	exit 2
	;;
esac
conf=$(readlink -f $deploy/phone-image-qemuarm64.rootfs.qemuboot.conf)
: > "$log"
rm -rf "$www" && mkdir -p "$www"
: > "$wwwlog"

if [ "$1" = release ]; then
	[ $# -ge 3 ] || { echo "usage: qemu-ota-delta.sh release <new dist dir> <old dist dir>..." >&2; exit 2; }
	new=$2
	shift 2
	for f in "$new"/*.raucb; do ln -s "$(readlink -f "$f")" "$www/"; done
	cp "$(readlink -f $deploy/phone-bundle-untrusted-qemuarm64.raucb)" "$www/untrusted.raucb"
	# The release's offer as it is, but to every phone (a trial release
	# goes out at 0 %).
	python3 - "$new/latest.json" "$www/latest.json" <<'EOF'
import json, sys
o = json.load(open(sys.argv[1]))
o.pop("rollout", None)
o.pop("paused", None)
json.dump(o, open(sys.argv[2], "w"))
EOF
	full=$(python3 -c 'import json, sys; print(json.load(open(sys.argv[1]))["url"])' "$www/latest.json")
	# compatible <bundle>: what its manifest (in the signature at the end of
	# a verity bundle, after which come the signature's 8 size bytes) gives.
	compatible() {
		python3 - "$1" <<'EOF'
import re, sys
with open(sys.argv[1], "rb") as f:
    size = f.seek(0, 2)
    f.seek(size - 8)
    n = int.from_bytes(f.read(8), "big")
    f.seek(size - 8 - n)
    m = re.search(rb"\[update\]\s*compatible=([^\r\n]+)", f.read(n))
print(m.group(1).decode() if m else "")
EOF
	}
	ours=$(compatible "$new/$full")
	python3 "$here"/ota-www.py 8789 "$www" > "$wwwlog" 2>&1 &
	server=$!
	for old in "$@"; do
		v=$(basename "$old")
		theirs=$(compatible "$(ls "$old"/phone-bundle-*.raucb | head -n 1)")
		if [ "$theirs" != "$ours" ]; then
			# RAUC on that system refuses every bundle of the new release.
			echo "  --   $v: its system is '$theirs', the release's bundles are for '$ours': no update path, not booted"
			continue
		fi
		gz=$(ls "$old"/phone-image-*-qemuarm64.wic.gz | head -n 1)
		dname=$(python3 -c 'import json, sys
o = json.load(open(sys.argv[1]))
print(next((d["url"] for d in o.get("deltas", []) if d.get("from") == sys.argv[2].lstrip("v")), ""))' "$www/latest.json" "$v")
		echo "=== $v: $gz, its delta ${dname:-none}" >> "$log"
		gunzip -c "$gz" > "$disk"
		boot "$v-settings" phone.ota-test=install
		boot "$v-up" phone.ota-test=status phone.gui=1
		expect "$v-settings" "the untrusted bundle refused" 'untrusted bundle refused'
		expect "$v-settings" "installed through Settings" ': installed$'
		expect "$v-up" "the release comes up healthy in B" 'booted=b .*root=/dev/mapper/root_b:ro health=shell='
		gd=0
		[ -n "$dname" ] && gd=$(got "$v-settings" "$dname")
		# The old system runs its own shell, which may know nothing of
		# deltas: then it takes the full bundle, as it should.
		echo "  $v-settings: downloaded $gd bytes of ${dname:-no delta}, $(got "$v-settings" "$full") of $full$( [ -n "$dname" ] && [ "$gd" = 0 ] && echo " (this system's shell picks no delta)")"
		for b in "$v-settings" "$v-up"; do said "$b" | sed "s/^/    $b: /"; done
	done
	kill $server
	[ $ok = 1 ] && echo "PHONE-OTA-RELEASE-RESULT: PASS" || echo "PHONE-OTA-RELEASE-RESULT: FAIL"
	[ $ok = 1 ]
	exit
fi

[ -f "$base/disk.rootfs.wic" ] || { echo "PHONE-OTA-DELTA: no base (make yocto-ota-base on the earlier build)"; exit 1; }
if ! trusted "$base/slot.img.desc"; then
	echo "PHONE-OTA-DELTA: FAIL: the base ($(cat "$base/rev")) is not signed with this build's update keys (/work/ota-keys), or was saved without its descriptor: make yocto-ota-base on an earlier build in this build directory"
	exit 1
fi
slot=$(readlink -f $deploy/phone-image-qemuarm64.rootfs.ext4.verity)
formats=$(cat "$base/formats" 2>/dev/null)
own=$("$gen" formats | paste -sd, -)
cp "$(readlink -f $deploy/phone-bundle-qemuarm64.raucb)" "$www/phone.raucb"
cp "$(readlink -f $deploy/phone-bundle-untrusted-qemuarm64.raucb)" "$www/signed-untrusted.raucb"
size=$(stat -c %s "$www/phone.raucb")
# The payload from the base (its disk: the slot and its descriptor); one
# from this build to itself, which the base must refuse although it is
# signed, and with which B later updates A; and the base's, damaged.
delta_bundle base "$base/disk.rootfs.wic" "$www/delta.raucb" "$formats" || exit 1
delta_bundle self "$slot" "$www/other.raucb" "$own" || exit 1
damaged_bundle "$www/delta.raucb.kdelta" "$www/damaged.raucb" "$formats" || exit 1
python3 "$here"/ota-www.py 8789 "$www" > "$wwwlog" 2>&1 &
server=$!

# offer [<the base's delta>]: latest.json with the full bundle and, given
# one, the deltas by the root hash they start from.
offer() {
	if [ -n "${1:-}" ]; then
		printf '{"version": "delta", "notes": "Cross-version update test", "size": %d, "url": "phone.raucb", "deltas": [{"from": "base", "from_root_hash": "%s", "size": %d, "url": "%s"}, {"from": "self", "from_root_hash": "%s", "size": %d, "url": "other.raucb"}]}\n' \
			"$size" "$(root_hash "$base/slot.img.desc")" "$(stat -c %s "$www/$1")" "$1" "$(root_hash "$slot.desc")" "$(stat -c %s "$www/other.raucb")" > "$www/latest.json"
	else
		printf '{"version": "delta", "notes": "Cross-version update test", "size": %d, "url": "phone.raucb"}\n' "$size" > "$www/latest.json"
	fi
}
fresh() { cp "$base/disk.rootfs.wic" "$disk"; }
# ota-test installs untrusted.raucb first, which must be refused.
untrusted() { cp "$www/$1" "$www/untrusted.raucb"; }

offer delta.raucb
fresh
untrusted other.raucb
boot i1 phone.ota-test=install                      # A, the base, installs the delta into B
boot r1 novga phone.ota-test=status phone.gui=1     # B without a display: the renderer never starts, never healthy
boot r2 novga phone.ota-test=status phone.gui=1
boot r3 novga phone.ota-test=status phone.gui=1
boot r4 phone.ota-test=status                       # tries spent: back to A, the base
boot i2 phone.ota-test=install                      # A installs the delta again
boot b1 phone.ota-test=status phone.gui=1           # B: this build, healthy with the shell and renderer: marked good
untrusted signed-untrusted.raucb
# B runs this build, whose ota-test can cut the power; A still holds the base.
boot c1 phone.ota-test=install-cut                  # the power goes while B writes this build's delta into A
boot c2 phone.ota-test=status                       # B comes up as before
boot i3 phone.ota-test=install                      # B updates A with this build's delta from itself
boot a1 phone.ota-test=status phone.gui=1           # A: this build, healthy
offer damaged.raucb
fresh
boot f1 phone.ota-test=install                      # the damaged delta stops; the full bundle instead
boot f2 phone.ota-test=status phone.gui=1
offer
fresh
boot d1 phone.ota-test=install                      # adaptive, the full bundle only
boot d2 phone.ota-test=status phone.gui=1
kill $server

sys=bundle
[ -n "$formats" ] && sys=system
echo "PHONE-OTA-DELTA: from $(cat "$base/rev") to $(version); the base's applier reads payload formats '${formats:-none}', this build's '$own'"
for b in i1 r1 r2 r3 r4 i2 b1 c1 c2 i3 a1 f1 f2 d1 d2; do said $b | sed "s/^/  $b: /"; done
echo "checks:"
expect i1 "the delta for another version is refused by its hook, not its signature" 'untrusted bundle failed for another reason: .*slot hook'
expect i1 "installed through Settings" ': installed$'
# An older base's ota-test does not report the hook's log.
if said i1 | grep -a -q 'delta installed:'; then
	if [ $sys = system ]; then
		expect i1 "the base's own applier ran (the bundle has none)" 'delta installed: applying payload format [0-9]+ with /usr/bin/phone-ota-delta'
	else
		expect i1 "the bundle's applier ran (the base has none)" 'delta installed: applying payload format [0-9]+ with /(mnt|run)/[^ ]*/phone-ota-delta'
	fi
else
	echo "  --   i1: the base's ota-test does not report the hook's log (an older base); the bundle carries the applier: $(cat "$www/delta.raucb.applier")"
fi
downloads i1 delta.raucb 0
downloads i1 phone.raucb -1 1
for b in r1 r2 r3; do expect $b "B is tried but never healthy" 'booted=b .*health=none'; done
expect r4 "tries spent: back to the base in A" 'booted=a primary=a good=a'
expect i2 "installed again after the rollback" ': installed$'
expect b1 "B comes up healthy" 'booted=b primary=b good=b .*root=/dev/mapper/root_b:ro health=shell='
expect i3 "the untrusted bundle refused" 'untrusted bundle refused'
expect i3 "B to A installed" ': installed$'
expect i3 "with this build's own applier" 'delta installed: applying payload format [0-9]+ with /usr/bin/phone-ota-delta'
expect i3 "the applier reports its time and memory" 'delta installed: .*PHONE-OTA-DELTA: applied .* peak memory'
downloads i3 other.raucb 0
downloads i3 phone.raucb -1 1
expect a1 "A comes up healthy with this build" 'booted=a primary=a good=a .*root=/dev/mapper/root_a:ro health=shell='
expect c1 "the power is cut while the delta is written" 'power cut after [0-9]+ MB written to slot a'
downloads c1 other.raucb 0
downloads c1 phone.raucb -1 1
expect c2 "B comes up as before after the power cut" 'booted=b primary=b good=b .*root=/dev/mapper/root_b:ro'
if said f1 | grep -a -q 'delta installed:'; then
	expect f1 "the damaged delta's applier stops" 'delta installed: .*does not match'
fi
expect f1 "installed (the full bundle)" ': installed$'
downloads f1 damaged.raucb 0
downloads f1 phone.raucb 0 "$size"
expect f2 "B comes up healthy" 'booted=b primary=b good=b .*health=shell='
expect d1 "installed adaptively" ': installed$'
downloads d1 phone.raucb 0 "$size"
expect d2 "B comes up healthy" 'booted=b primary=b good=b .*health=shell='
if sed 's/\x1b\[[0-9;]*m//g' "$log" | grep -a -o 'PHONE-OTA: .*' | grep -v 'untrusted bundle failed for another reason' | grep -q FAIL; then
	echo "  FAIL another step failed (above)"
	ok=0
fi
if [ "$(sed 's/\x1b\[[0-9;]*m//g' "$log" | grep -a -o 'PHONE-OTA: booted=.*' | grep -cv 'root=/dev/mapper/root_[ab]:ro health=')" != 0 ]; then
	echo "  FAIL a system ran outside dm-verity"
	ok=0
fi

got_delta=$(got i2 delta.raucb)
got_full=$(got d1 phone.raucb)
dsize=$(stat -c %s "$www/delta.raucb")
echo "PHONE-OTA-DELTA: incremental downloaded $got_delta of $dsize bytes of the delta bundle (the $(cat "$www/delta.raucb.applier")'s applier); adaptive downloaded $got_full of $size bytes of the full bundle"
[ "$got_full" -gt 0 ] && echo "PHONE-OTA-DELTA: incremental $((got_delta * 100 / got_full))% of adaptive, $((got_delta * 100 / size))% of the full bundle"
python3 "$here"/ota-delta.py ranges "$wwwlog.i2" /delta.raucb "$www/delta.raucb" "$www/delta.raucb.kdelta"
python3 "$here"/ota-delta.py ranges "$wwwlog.i3" /other.raucb "$www/other.raucb" "$www/other.raucb.kdelta"
for b in i1 i2 i3; do said $b | grep -a -o 'PHONE-OTA-DELTA: applied.*' | sed "s/^/  applier on $b: /"; done
grep -a 'PHONE-OTA-DELTA: ' "$www/delta.raucb.log" | sed 's/^/  generator: /'
python3 "$here"/ota-delta.py report "$base/slot.img" "$slot" "$base/files.tsv" "$rootfs"
[ $ok = 1 ] && echo "PHONE-OTA-DELTA-RESULT: PASS" || echo "PHONE-OTA-DELTA-RESULT: FAIL"
[ $ok = 1 ]
