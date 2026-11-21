# The Android boot image a phone's stock bootloader loads (header version 0,
# made by mkbootimg when the kernel is deployed): the kernel with the
# machine's device tree appended, an empty initramfs and the command line,
# at the offsets and page size the bootloader expects (the machine's
# PHONE_BOOTIMG_*). The initramfs is an empty cpio archive: the kernel finds
# no /init in it and mounts root= itself, while a bootloader that insists on
# a ramdisk still gets one.

PHONE_BOOTIMG_CMDLINE ??= ""
PHONE_BOOTIMG_DTB ??= "${@(d.getVar('KERNEL_DEVICETREE') or '').split()[0] if d.getVar('KERNEL_DEVICETREE') else ''}"
PHONE_BOOTIMG_PAGESIZE ??= "4096"
PHONE_BOOTIMG_BASE ??= "0x00000000"
PHONE_BOOTIMG_KERNEL_OFFSET ??= "0x00008000"
PHONE_BOOTIMG_RAMDISK_OFFSET ??= "0x01000000"
PHONE_BOOTIMG_SECOND_OFFSET ??= "0x00f00000"
PHONE_BOOTIMG_TAGS_OFFSET ??= "0x00000100"
PHONE_BOOTIMG_NAME ??= "boot-${MACHINE}.img"

do_deploy[depends] += "mkbootimg-native:do_populate_sysroot"

do_deploy:append() {
	if [ -z "${PHONE_BOOTIMG_DTB}" ]; then
		bbfatal "phone-bootimg: no device tree to append (KERNEL_DEVICETREE, PHONE_BOOTIMG_DTB)"
	fi
	work=${WORKDIR}/phone-bootimg
	rm -rf $work && mkdir -p $work
	cat ${B}/${KERNEL_OUTPUT_DIR}/${KERNEL_IMAGETYPE} "$(get_real_dtb_path_in_kernel ${PHONE_BOOTIMG_DTB})" > $work/kernel-dtb
	(cd $work && printf '' | cpio --quiet -o -H newc > ramdisk.cpio)
	mkbootimg --header_version 0 --kernel $work/kernel-dtb --ramdisk $work/ramdisk.cpio \
		--cmdline "${PHONE_BOOTIMG_CMDLINE}" \
		--base ${PHONE_BOOTIMG_BASE} --pagesize ${PHONE_BOOTIMG_PAGESIZE} \
		--kernel_offset ${PHONE_BOOTIMG_KERNEL_OFFSET} --ramdisk_offset ${PHONE_BOOTIMG_RAMDISK_OFFSET} \
		--second_offset ${PHONE_BOOTIMG_SECOND_OFFSET} --tags_offset ${PHONE_BOOTIMG_TAGS_OFFSET} \
		-o ${DEPLOYDIR}/${PHONE_BOOTIMG_NAME}
}
do_deploy[vardeps] += "PHONE_BOOTIMG_CMDLINE PHONE_BOOTIMG_DTB PHONE_BOOTIMG_PAGESIZE PHONE_BOOTIMG_BASE \
                       PHONE_BOOTIMG_KERNEL_OFFSET PHONE_BOOTIMG_RAMDISK_OFFSET PHONE_BOOTIMG_SECOND_OFFSET \
                       PHONE_BOOTIMG_TAGS_OFFSET PHONE_BOOTIMG_NAME"
