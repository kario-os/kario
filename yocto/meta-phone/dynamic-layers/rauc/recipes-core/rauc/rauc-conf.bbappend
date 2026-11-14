# The keyring (ca.cert.pem) comes from the update keys, outside git.
FILESEXTRAPATHS:prepend := "${THISDIR}/files:${PHONE_OTA_KEYS}:"

# A release's version (kario-internal's make release): its image refuses
# bundles older than itself, whatever an update server offers. So a release
# that has to undo a bad one goes out under a higher version number.
PHONE_VERSION ??= ""

# Bundles are compatible per machine: an update for one board never installs
# on another.
do_install:append() {
    sed -i 's/@COMPATIBLE@/kario-${MACHINE}/' ${D}${nonarch_libdir}/rauc/system.conf
    if [ -n "${PHONE_VERSION}" ]; then
        sed -i '/^compatible=/a min-bundle-version=${PHONE_VERSION}' ${D}${nonarch_libdir}/rauc/system.conf
    fi
}
PACKAGE_ARCH = "${MACHINE_ARCH}"
