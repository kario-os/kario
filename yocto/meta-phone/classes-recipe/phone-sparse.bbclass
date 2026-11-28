# Android sparse images for fastboot (IMAGE_FSTYPES ext4.sparse), made by
# files/phone-img2simg.py. Zero runs become fill chunks, never "don't care":
# the partition's old contents must not show through where the file system
# expects zeros.
PHONE_IMG2SIMG = "${@bb.utils.which(d.getVar('BBPATH'), 'files/phone-img2simg.py')}"
CONVERSIONTYPES += "sparse"
CONVERSION_CMD:sparse = "python3 ${PHONE_IMG2SIMG} ${IMAGE_NAME}.${type} ${IMAGE_NAME}.${type}.sparse"
do_image_ext4[file-checksums] += "${PHONE_IMG2SIMG}:True"
