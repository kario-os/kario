# The Android boot image a phone's stock bootloader loads in the userdata
# layout (header version 0, made by mkbootimg when the kernel is deployed):
# the kernel with the machine's device tree appended, phone-initramfs and
# the command line, at the offsets and page size the bootloader expects
# (the machine's PHONE_BOOTIMG_*). The initramfs finds the root file system
# by the board's partition names (its slot-init): OnePlus's bootloader
# appends a root= naming Android's system partition after the image's own
# (39-oneplus-6t.md §1), and a kernel mounting its root itself takes the
# last root=: it started Android's init, which rebooted to fastboot. A
# small uncompressed cpio archive before the compressed initramfs names
# the layout (/etc/phone/layout), as the A/B boot image's carries the
# root's verity descriptor (phone-abboot); the kernel unpacks both.

PHONE_BOOTIMG_CMDLINE ??= ""
PHONE_BOOTIMG_INITRAMFS ??= "phone-initramfs"
PHONE_BOOTIMG_DTB ??= "${@(d.getVar('KERNEL_DEVICETREE') or '').split()[0] if d.getVar('KERNEL_DEVICETREE') else ''}"
PHONE_BOOTIMG_PAGESIZE ??= "4096"
PHONE_BOOTIMG_BASE ??= "0x00000000"
PHONE_BOOTIMG_KERNEL_OFFSET ??= "0x00008000"
PHONE_BOOTIMG_RAMDISK_OFFSET ??= "0x01000000"
PHONE_BOOTIMG_SECOND_OFFSET ??= "0x00f00000"
PHONE_BOOTIMG_TAGS_OFFSET ??= "0x00000100"
PHONE_BOOTIMG_NAME ??= "boot-${MACHINE}.img"

do_deploy[depends] += "mkbootimg-native:do_populate_sysroot ${PHONE_BOOTIMG_INITRAMFS}:do_image_complete"

do_deploy:append() {
	if [ -z "${PHONE_BOOTIMG_DTB}" ]; then
		bbfatal "phone-bootimg: no device tree to append (KERNEL_DEVICETREE, PHONE_BOOTIMG_DTB)"
	fi
	work=${WORKDIR}/phone-bootimg
	rm -rf $work && mkdir -p $work/layout/etc/phone
	cat ${B}/${KERNEL_OUTPUT_DIR}/${KERNEL_IMAGETYPE} "$(get_real_dtb_path_in_kernel ${PHONE_BOOTIMG_DTB})" > $work/kernel-dtb
	echo layout=userdata > $work/layout/etc/phone/layout
	(cd $work/layout && find . | sort | cpio --quiet -o -H newc -R 0:0) > $work/ramdisk
	cat ${DEPLOY_DIR_IMAGE}/${PHONE_BOOTIMG_INITRAMFS}-${MACHINE}.${INITRAMFS_FSTYPES} >> $work/ramdisk
	mkbootimg --header_version 0 --kernel $work/kernel-dtb --ramdisk $work/ramdisk \
		--cmdline "${PHONE_BOOTIMG_CMDLINE}" \
		--base ${PHONE_BOOTIMG_BASE} --pagesize ${PHONE_BOOTIMG_PAGESIZE} \
		--kernel_offset ${PHONE_BOOTIMG_KERNEL_OFFSET} --ramdisk_offset ${PHONE_BOOTIMG_RAMDISK_OFFSET} \
		--second_offset ${PHONE_BOOTIMG_SECOND_OFFSET} --tags_offset ${PHONE_BOOTIMG_TAGS_OFFSET} \
		-o ${DEPLOYDIR}/${PHONE_BOOTIMG_NAME}
}
do_deploy[vardeps] += "PHONE_BOOTIMG_CMDLINE PHONE_BOOTIMG_INITRAMFS PHONE_BOOTIMG_DTB PHONE_BOOTIMG_PAGESIZE PHONE_BOOTIMG_BASE \
                       PHONE_BOOTIMG_KERNEL_OFFSET PHONE_BOOTIMG_RAMDISK_OFFSET PHONE_BOOTIMG_SECOND_OFFSET \
                       PHONE_BOOTIMG_TAGS_OFFSET PHONE_BOOTIMG_NAME"
