# The boot image of the A/B layout (43-phone-partitions.md), for a phone
# whose bootloader keeps the A/B slot and loads an Android boot image
# (header version 0) from boot_a or boot_b: the kernel with the machine's
# device tree appended (as phone-bootimg), phone-initramfs, and this image's
# signed verity descriptor (phone-verity) as /etc/phone/rootfs.verity in
# the initramfs. A boot slot is always written together with its root slot
# (the flash script, RAUC's parent and child slots), so the initramfs
# checks the root slot of the slot the bootloader started against the
# descriptor of the root file system built with this kernel, whose modules
# are signed for it. The descriptor is a small uncompressed cpio archive
# before the compressed initramfs; the kernel unpacks both, as it does an
# early microcode archive.
#
#   IMAGE_CLASSES += "phone-verity phone-abboot"
#   IMAGE_FSTYPES += "ext4.verity abboot"
#
# PHONE_ABBOOT_CMDLINE is the machine's (with root= ABL adds no
# skip_initramfs, ro init=/init or slot suffix, though it still appends
# its own root=, 39-oneplus-6t.md §1; phone.layout=ab tells the system the
# layout).

PHONE_ABBOOT_CMDLINE ??= ""
PHONE_ABBOOT_INITRAMFS ??= "phone-initramfs"
PHONE_BOOTIMG_DTB ??= "${@(d.getVar('KERNEL_DEVICETREE') or '').split()[0] if d.getVar('KERNEL_DEVICETREE') else ''}"
PHONE_BOOTIMG_PAGESIZE ??= "4096"
PHONE_BOOTIMG_BASE ??= "0x00000000"
PHONE_BOOTIMG_KERNEL_OFFSET ??= "0x00008000"
PHONE_BOOTIMG_RAMDISK_OFFSET ??= "0x01000000"
PHONE_BOOTIMG_SECOND_OFFSET ??= "0x00f00000"
PHONE_BOOTIMG_TAGS_OFFSET ??= "0x00000100"

IMAGE_TYPES += "abboot"
IMAGE_TYPEDEP:abboot = "ext4.verity"
do_image_abboot[depends] += "mkbootimg-native:do_populate_sysroot ${PHONE_ABBOOT_INITRAMFS}:do_image_complete virtual/kernel:do_deploy"
do_image_abboot[vardeps] += "PHONE_ABBOOT_CMDLINE PHONE_BOOTIMG_DTB PHONE_BOOTIMG_PAGESIZE PHONE_BOOTIMG_BASE \
                             PHONE_BOOTIMG_KERNEL_OFFSET PHONE_BOOTIMG_RAMDISK_OFFSET PHONE_BOOTIMG_SECOND_OFFSET \
                             PHONE_BOOTIMG_TAGS_OFFSET"

IMAGE_CMD:abboot() {
	if [ -z "${PHONE_BOOTIMG_DTB}" ] || [ -z "${PHONE_ABBOOT_CMDLINE}" ]; then
		bbfatal "phone-abboot: no device tree (KERNEL_DEVICETREE) or no command line (PHONE_ABBOOT_CMDLINE)"
	fi
	work=${WORKDIR}/phone-abboot
	rm -rf $work
	mkdir -p $work/desc/etc/phone
	cp ${IMGDEPLOYDIR}/${IMAGE_NAME}.ext4.verity.desc $work/desc/etc/phone/rootfs.verity
	cp ${IMGDEPLOYDIR}/${IMAGE_NAME}.ext4.verity.desc.sig $work/desc/etc/phone/rootfs.verity.sig
	(cd $work/desc && find . | sort | cpio --quiet -o -H newc -R 0:0) > $work/ramdisk
	cat ${DEPLOY_DIR_IMAGE}/${PHONE_ABBOOT_INITRAMFS}-${MACHINE}.${INITRAMFS_FSTYPES} >> $work/ramdisk
	cat ${DEPLOY_DIR_IMAGE}/${KERNEL_IMAGETYPE} ${DEPLOY_DIR_IMAGE}/$(basename ${PHONE_BOOTIMG_DTB}) > $work/kernel-dtb
	mkbootimg --header_version 0 --kernel $work/kernel-dtb --ramdisk $work/ramdisk \
		--cmdline "${PHONE_ABBOOT_CMDLINE}" \
		--base ${PHONE_BOOTIMG_BASE} --pagesize ${PHONE_BOOTIMG_PAGESIZE} \
		--kernel_offset ${PHONE_BOOTIMG_KERNEL_OFFSET} --ramdisk_offset ${PHONE_BOOTIMG_RAMDISK_OFFSET} \
		--second_offset ${PHONE_BOOTIMG_SECOND_OFFSET} --tags_offset ${PHONE_BOOTIMG_TAGS_OFFSET} \
		-o ${IMGDEPLOYDIR}/${IMAGE_NAME}.abboot
}
