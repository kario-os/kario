#!/bin/sh
# Factory mode on the emulator (phone-factory-qemu.service, run by
# yocto/qemu-factory.sh, docs/40-testing.md §10). Checks, each reported as
# "PHONE-FACTORY-TEST: <case>: PASS|FAIL", the reports' checks as
# "PHONE-FACTORY-CHECK: <run> <id> <result> <required> <value or reason>",
# then "PHONE-FACTORY-TEST-RESULT: PASS|FAIL" and the power-off.
# kario.factory-test=1 (an unprovisioned phone booted with kario.factory=1):
# factory mode entered, smsd and pushd left out, the runner's automatic
# checks (exit 0), the sound card taken away from its driver (audio.card
# fails, exit 1) and given back, the phone provisioned through the
# runner's API as the station does, after which the runner refuses
# (exit 3). kario.factory-test=again (the same disk, provisioned, still
# booted with kario.factory=1): the gate refused, no mark, no runner, smsd
# as ever, and the runner refusing on its own.
mark=/run/phone/factory/on
sock=/run/phone/factory.sock
w=/run/factory-test
mkdir -p $w
failed=0
said=$w/said.txt
: > $said
tell() {
	from=$(($(wc -l < $said) + 1))
	echo "$1" >> $said
	/usr/libexec/phone/console-file factory-test.txt $said $from > /dev/console
}
say() { tell "PHONE-FACTORY-TEST: $*"; }
check() {
	name=$1
	why=$2
	shift 2
	if "$@"; then
		say "$name: PASS ($why)"
	else
		say "$name: FAIL ($why)"
		failed=1
	fi
}
# within <s> <command>...: the command succeeds within s seconds.
within() {
	n=$1
	shift
	while [ "$n" -gt 0 ]; do
		"$@" && return 0
		sleep 1
		n=$((n - 1))
	done
	"$@"
}
# report <run> <file>: every check of the report, one line each.
report() {
	awk -v run="$1" '
	/^      "id": / { id = $2 }
	/^      "required": / { req = ($2 == "true,") ? "required" : "optional" }
	/^      "result": / { res = $2 }
	/^      "(value|reason)": / { sub(/^ *"(value|reason)": /, ""); why = why " " $0 }
	/^      "duration_ms": / {
		gsub(/[",]/, "", id); gsub(/[",]/, "", res); gsub(/(^ *"|",?$)/, "", why); gsub(/" "/, "; ", why)
		print "PHONE-FACTORY-CHECK: " run " " id " " res " " req " " why
		id = ""; res = ""; why = ""
	}' "$2" | while IFS= read -r l; do tell "$l"; done
}
# auto <run> <exit wanted> [args]: the runner's automatic checks.
auto() {
	run=$1
	want=$2
	shift 2
	phone-factory-test --auto --out $w/$run.json "$@" > $w/$run.out 2> $w/$run.err
	code=$?
	report "$run" $w/$run.out
	grep -h -m 3 . $w/$run.err | while IFS= read -r l; do tell "PHONE-FACTORY-LOG: $run: $l"; done
	check "$run" "exit $code, wanted $want" [ "$code" = "$want" ]
}
# result <report> <id>: the check's result in the report.
result() {
	awk -v id="\"$2\"," '$1 == "\"id\":" { on = ($2 == id) } on && $1 == "\"result\":" { gsub(/[",]/, "", $2); print $2; exit }' "$1"
}
# dumped <text>: the Shell's state (phone-shell --dump) holds text.
dumped() {
	pid=$(cat /run/phone/shell.pid 2> /dev/null) && kill -USR1 "$pid" && sleep 1 && grep -q "$1" /run/phone/shell-dump.json
}
# normal: the Shell runs, not in factory mode.
normal() { dumped '"keylock"' && ! grep -q '"factory": {' /run/phone/shell-dump.json; }
is() { [ "$(systemctl show -P "$2" "$1")" = "$3" ]; }
active() { systemctl is-active -q "$1"; }
has() { grep -q "$1" "$2"; }

mode=$(sed -n 's/.*kario\.factory-test=\([^ ]*\).*/\1/p' /proc/cmdline)
if [ "$mode" = again ]; then
	check gate-refused "a provisioned keystore: no mark" [ ! -e $mark ]
	check gate-status "phone-factory-gate.service exited 3" is phone-factory-gate.service ExecMainStatus 3
	check no-runner "phone-factory.service's condition failed" is phone-factory.service ConditionResult no
	check smsd "phone-smsd.service runs as ever" within 60 active phone-smsd.service
	check shell-normal "the Shell is not in factory mode" within 90 normal
	auto refused-at-boot 3
	phone-factory-test --gate > $w/gate.out 2>&1
	check gate-again "phone-factory-test --gate exits 3" [ $? = 3 ]
else
	check factory-mode "the mark written" within 60 test -e $mark
	check runner "phone-factory.service's socket" within 90 test -S $sock
	check no-smsd "phone-smsd.service left out" is phone-smsd.service ConditionResult no
	check no-pushd "phone-pushd.service left out" is phone-pushd.service ConditionResult no
	auto auto 0
	check device-id "the report names the unit" has '"device_id": "PH-[0-9a-f]\{12\}"' $w/auto.json
	check shell-factory "the Shell shows the factory screen with the runner's plan" within 60 dumped '"plan": true'
	tell "PHONE-FACTORY-LOG: shell: $(grep -m 2 'factory mode' /run/phone/shell.log | tr '\n' ' ')"
	check no-imei "the report holds no IMEI" sh -c "! grep -qi imei $w/auto.json"
	# The sound card away from its driver: the card is gone as if the part
	# were missing; it comes back bound again.
	snd=$(basename "$(readlink -f /sys/class/sound/card0/device)")
	drv=/sys/bus/virtio/drivers/virtio_snd
	if [ -n "$snd" ] && [ -d $drv ] && echo "$snd" > $drv/unbind; then
		auto no-sound-card 1
		check no-sound-card-check "audio.card failed" [ "$(result $w/no-sound-card.json audio.card)" = fail ]
		echo "$snd" > $drv/bind
		check sound-card-back "the card bound again" within 10 test -e /sys/class/sound/card0
	else
		say "no-sound-card: FAIL (no virtio sound card to unbind: ${snd:-none})"
		failed=1
	fi
	mkdir -p $w/ca
	phone-factory-test --unit unix:$sock --provision $w/ca > $w/provision.out 2>&1
	code=$?
	tell "PHONE-FACTORY-LOG: provision: $(tr '\n' ' ' < $w/provision.out | cut -c 1-300)"
	check provision "exit $code, wanted 0" [ "$code" = 0 ]
	auto refused 3
	phone-factory-test --unit unix:$sock --provision $w/ca > $w/provision2.out 2>&1
	check provision-again "a second provisioning refused (exit 3)" [ $? = 3 ]
	check shell-provisioned "the Shell saw the provisioning and powers off" within 30 has 'factory mode: provisioned' /run/phone/shell.log
fi
if [ $failed = 0 ]; then
	tell "PHONE-FACTORY-TEST-RESULT: PASS"
else
	tell "PHONE-FACTORY-TEST-RESULT: FAIL"
fi
/usr/libexec/phone/console-file factory-test.txt $said end > /dev/console
# A provisioned phone in factory mode is powered off by the Shell
# (yocto/qemu-factory.sh waits for it); this is for a run where it is not.
sleep 60
systemctl poweroff
