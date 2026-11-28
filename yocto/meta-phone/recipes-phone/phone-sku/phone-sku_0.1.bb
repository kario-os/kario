SUMMARY = "phone-sku: the hardware variant the image is built for"
DESCRIPTION = "The SKU's description for phone-shell (share/phone/sku.json) and its product's \
composition layer; on a real board also the board's device tree with the SKU's display and \
keypad applied; source.txt, the commit the image is built from. The files come from tools/sku.py \
(make sku SKU=...), 24-sku-framework.md."
LICENSE = "Apache-2.0"
LIC_FILES_CHKSUM = "file://${COMMON_LICENSE_DIR}/Apache-2.0;md5=89aea4e17d99a7cacdbeed46a0096b10"

# Generated into files/ by tools/sku.py before the build. partitions.conf
# is the board's partitions by role for phone-initramfs
# (43-phone-partitions.md), empty for a board without them; watchdog.conf
# the board's [watchdog] for systemd (a system.conf drop-in, systemd's own
# where the board gives none); source.txt the commit the build is from
# (kit.txt names it).
SRC_URI = "file://sku.json file://profile.json file://partitions.conf file://watchdog.conf file://source.txt ${@'file://overlay.dts' if d.getVar('PHONE_SKU_DTB') else ''}"
S = "${UNPACKDIR}"

PHONE_SKU ??= ""
PHONE_SKU_DTB ??= ""
# phone-paint's GPU path by default in this image, over the SKU's paint_gpu
# (42-gpu-rendering.md §3): off, compose or raster; empty keeps the SKU's.
# yocto/kas/paint-gpu.yml sets compose. Only where the SKU's kas overlay
# says the board has a GPU (PHONE_SKU_GPU).
PHONE_PAINT_GPU_DEFAULT ??= ""
PHONE_SKU_GPU ??= ""

B = "${WORKDIR}/build"

inherit deploy

PACKAGE_ARCH = "${MACHINE_ARCH}"

python () {
    if d.getVar('PHONE_SKU_DTB'):
        d.appendVar('DEPENDS', ' dtc-native')
        d.appendVarFlag('do_deploy', 'depends', ' virtual/kernel:do_deploy')
    paint = d.getVar('PHONE_PAINT_GPU_DEFAULT')
    if paint not in ('', 'off', 'compose', 'raster'):
        bb.fatal(f"PHONE_PAINT_GPU_DEFAULT = {paint!r} is not off, compose or raster")
    if paint not in ('', 'off') and d.getVar('PHONE_SKU_GPU') != '1':
        bb.fatal(f"PHONE_PAINT_GPU_DEFAULT = {paint!r}: the board of SKU {d.getVar('PHONE_SKU')} has no GPU (gpu = false)")
}

# The image's sku.json: the SKU's as tools/sku.py wrote it, byte for byte
# unless PHONE_PAINT_GPU_DEFAULT sets its paint_gpu (absent for off, as
# tools/sku.py writes it).
python do_compile() {
    import json
    paint = d.getVar('PHONE_PAINT_GPU_DEFAULT')
    with open(os.path.join(d.getVar('S'), 'sku.json'), encoding='utf-8') as f:
        text = f.read()
    if paint:
        sku = json.loads(text)
        sku.pop('paint_gpu', None)
        if paint != 'off':
            sku['paint_gpu'] = paint
        text = json.dumps(sku, ensure_ascii=False, indent=2) + '\n'
    with open(os.path.join(d.getVar('B'), 'sku.json'), 'w', encoding='utf-8') as f:
        f.write(text)
}

do_install() {
    install -d ${D}${datadir}/phone/profile.d
    install -m 0644 ${B}/sku.json ${D}${datadir}/phone/sku.json
    install -m 0644 ${S}/profile.json ${D}${datadir}/phone/profile.d/20-product.json
    install -d ${D}${sysconfdir}/phone
    install -m 0644 ${S}/partitions.conf ${D}${sysconfdir}/phone/partitions.conf
    install -m 0644 ${S}/source.txt ${D}${datadir}/phone/source.txt
    install -D -m 0644 ${S}/watchdog.conf ${D}${systemd_unitdir}/system.conf.d/50-phone-watchdog.conf
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

PACKAGES =+ "${PN}-partitions"
FILES:${PN}-partitions = "${sysconfdir}/phone/partitions.conf"
FILES:${PN} = "${datadir}/phone/sku.json ${datadir}/phone/profile.d/20-product.json ${datadir}/phone/source.txt \
               ${systemd_unitdir}/system.conf.d/50-phone-watchdog.conf"
