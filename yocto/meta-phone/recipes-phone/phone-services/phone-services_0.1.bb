SUMMARY = "Where the product's cloud services are: store, push gateway, update server"
DESCRIPTION = "PHONE_SERVICES names a directory of the product layer: services.env \
(the store, push gateway and update server URLs, read by phone-shell, phone-pushd \
and the Hongyan stand-in; any of them may be left out), the push gateway's certificate the phones pin (*.pem), \
the key latest.json is signed with (*.pub, PHONE_UPDATE_KEY), and \
trust/<store>.pub, store roots the phones trust. The simulated store and push \
gateway run on the device for what it does not name; without PHONE_SERVICES the \
package is empty."
LICENSE = "Apache-2.0"
LIC_FILES_CHKSUM = "file://${COMMON_LICENSE_DIR}/Apache-2.0;md5=89aea4e17d99a7cacdbeed46a0096b10"

PHONE_SERVICES ??= ""
SRC_URI = "${@'file://%s;subdir=services' % d.getVar('PHONE_SERVICES') if d.getVar('PHONE_SERVICES') else ''}"

do_install() {
    [ -d ${UNPACKDIR}/services ] || return 0
    env=$(find ${UNPACKDIR}/services -name services.env | head -n 1)
    [ -n "$env" ] || bbfatal "PHONE_SERVICES (${PHONE_SERVICES}) has no services.env"
    dir=$(dirname "$env")
    install -d ${D}${datadir}/phone/services
    install -m 0644 "$env" ${D}${datadir}/phone/services.env
    # One mark per service the product names: the simulated one on the device
    # starts only without it (phone-pushgw, phone-storesrv).
    for s in STORE_SERVER:store PUSH_GATEWAY:push UPDATE_URL:update; do
        if grep -q "^PHONE_${s%%:*}=" "$env"; then
            touch ${D}${datadir}/phone/services/${s##*:}
        fi
    done
    for f in "$dir"/*.pem "$dir"/*.pub; do
        if [ -e "$f" ]; then
            install -m 0644 "$f" ${D}${datadir}/phone/services/
        fi
    done
    for f in "$dir"/trust/*.pub; do
        if [ -e "$f" ]; then
            install -d ${D}${datadir}/phone/trust
            install -m 0644 "$f" ${D}${datadir}/phone/trust/
        fi
    done
}

FILES:${PN} = "${datadir}/phone/services.env ${datadir}/phone/services ${datadir}/phone/trust"
ALLOW_EMPTY:${PN} = "1"
PACKAGE_ARCH = "${MACHINE_ARCH}"
