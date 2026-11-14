#!/bin/sh
# Run inside `kas-container shell`: the phone against cloud services (R42).
# The store backend, the push gateway and an update server start here,
# where QEMU's user network shows them as 10.0.2.2; the product layer's
# directory for phone-services is written from what they made (the store
# root the phones trust, the gateway certificate they pin, services.env)
# and the image is built with it. The A/B disk then boots with
# phone.cloud-test (phone-ota's cloud-test): the simulated services stay
# off, the store app installs an app published to the cloud's store, a
# Hongyan reply comes through the cloud's gateway, Settings › System update
# reads the cloud's latest.json. At the end the image is built again
# without it.
set -u
here=$(cd "$(dirname "$0")" && pwd)
bin=$here/../build/bin
cd /work/build
deploy=tmp/deploy/images/qemuarm64
cloud=/work/build/cloud
log=$cloud/qemu.log
rm -rf "$cloud" && mkdir -p "$cloud/services/trust" "$cloud/ota"
"$bin"/phone-storesrv --listen 0.0.0.0:8893 --data "$cloud/store" --admin-token-file "$cloud/staff-token" > "$cloud/storesrv.log" 2>&1 &
store=$!
"$bin"/phone-pushgw --listen 0.0.0.0:8890 --state "$cloud/pushgw.json" --tls-dir "$cloud/pushgw-tls" > "$cloud/pushgw.log" 2>&1 &
gateway=$!
printf '{"version": "cloud-1", "notes": "Cloud update server", "size": 1, "url": "phone.raucb"}\n' > "$cloud/ota/latest.json"
python3 "$here"/ota-www.py 8894 "$cloud/ota" > "$cloud/ota.log" 2>&1 &
ota=$!
stop() { kill $store $gateway $ota 2>/dev/null; }
for i in $(seq 1 100); do
	[ -s "$cloud/store/store.pub" ] && [ -s "$cloud/staff-token" ] && [ -s "$cloud/pushgw-tls/cert.pem" ] && break
	sleep 0.1
done
# Named for the store: its certificates name it "store" (phone-storesrv).
cp "$cloud/store/store.pub" "$cloud/services/trust/store.pub"
cp "$cloud/pushgw-tls/cert.pem" "$cloud/services/push-gateway.pem"
cat > "$cloud/services/services.env" <<EOF
PHONE_STORE_SERVER=http://10.0.2.2:8893
PHONE_PUSH_GATEWAY=https://10.0.2.2:8890
PHONE_PUSH_GATEWAY_CERT=/usr/share/phone/services/push-gateway.pem
PHONE_UPDATE_URL=http://10.0.2.2:8894
EOF
echo "PHONE_SERVICES = \"$cloud/services\"" > "$cloud/cloud.conf"
if ! bitbake -R "$cloud/cloud.conf" phone-image > "$cloud/bitbake.log" 2>&1; then
	tail -n 20 "$cloud/bitbake.log"
	stop
	echo "PHONE-CLOUD-RESULT: FAIL"
	exit 1
fi
conf=$(readlink -f $deploy/phone-image-qemuarm64.rootfs.qemuboot.conf)
disk=$cloud/disk.rootfs.wic
cp "$(readlink -f $deploy/phone-image-qemuarm64.rootfs.wic)" "$disk"
timeout 900 runqemu "$conf" "$disk" nographic slirp \
	bootparams="phone.cloud-test phone.cloud-staff=$(cat "$cloud/staff-token")" </dev/null > "$log" 2>&1
stop
out=$(sed 's/\x1b\[[0-9;]*m//g' "$log" | tr -d '\r' | grep -a -o 'PHONE-CLOUD: .*')
echo "$out"
ok=1
echo "$out" | grep -q 'script: PASS' || ok=0
echo "$out" | grep -q FAIL && ok=0
[ "$(echo "$out" | grep -c ': not started')" = 2 ] || ok=0
# The cloud's side: the app was published and downloaded, the update
# server was asked.
grep -q 'published com.example.notes 1.0\|pending_review com.example.notes 1.0' "$cloud/storesrv.log" || ok=0
grep -a -q '/latest.json' "$cloud/ota.log" || ok=0
echo "store: $(grep -c . "$cloud/storesrv.log") log lines; update server: $(grep -a -c 'latest.json' "$cloud/ota.log") requests"
# The image without the cloud services again (what the other tests boot).
bitbake phone-image > "$cloud/bitbake-restore.log" 2>&1 || { echo "the image could not be rebuilt without the cloud services"; ok=0; }
[ $ok = 1 ] && echo "PHONE-CLOUD-RESULT: PASS" || echo "PHONE-CLOUD-RESULT: FAIL"
[ $ok = 1 ]
