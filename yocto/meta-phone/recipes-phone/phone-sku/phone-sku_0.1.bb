SUMMARY = "phone-sku: the hardware variant the image is built for"
DESCRIPTION = "The SKU's description for phone-shell (share/phone/sku.json) and its product's \
composition layer; on a real board also the board's device tree with the SKU's display and \
keypad applied. The files come from tools/sku.py (make sku SKU=...), 24-sku-framework.md."
LICENSE = "Apache-2.0"
LIC_FILES_CHKSUM = "file://${COMMON_LICENSE_DIR}/Apache-2.0;md5=89aea4e17d99a7cacdbeed46a0096b10"

# Generated into files/ by tools/sku.py before the build.
SRC_URI = "file://sku.json file://profile.json ${@'file://overlay.dts' if d.getVar('PHONE_SKU_DTB') else ''}"
S = "${UNPACKDIR}"

PHONE_SKU ??= ""
PHONE_SKU_DTB ??= ""

inherit deploy

PACKAGE_ARCH = "${MACHINE_ARCH}"

python () {
    if d.getVar('PHONE_SKU_DTB'):
        d.appendVar('DEPENDS', ' dtc-native')
        d.appendVarFlag('do_deploy', 'depends', ' virtual/kernel:do_deploy')
}

do_install() {
    install -d ${D}${datadir}/phone/profile.d
    install -m 0644 ${S}/sku.json ${D}${datadir}/phone/sku.json
    install -m 0644 ${S}/profile.json ${D}${datadir}/phone/profile.d/20-product.json
}

# The board's tree (built with symbols, KERNEL_DTC_FLAGS = "-@") with the
# SKU's overlay applied: what the bootloader hands the kernel.
do_deploy() {
    if [ -n "${PHONE_SKU_DTB}" ]; then
        dtc -@ -I dts -O dtb -o ${B}/overlay.dtbo ${S}/overlay.dts
        fdtoverlay -i ${DEPLOY_DIR_IMAGE}/${PHONE_SKU_DTB} -o ${DEPLOYDIR}/phone-sku-${PHONE_SKU}.dtb ${B}/overlay.dtbo
        install -m 0644 ${B}/overlay.dtbo ${DEPLOYDIR}/phone-sku-${PHONE_SKU}.dtbo
    fi
}
addtask deploy after do_install before do_build

FILES:${PN} = "${datadir}/phone/sku.json ${datadir}/phone/profile.d/20-product.json"
