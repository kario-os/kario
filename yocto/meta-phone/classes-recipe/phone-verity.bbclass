# dm-verity for the system slots (25-system-update.md). The "verity"
# conversion turns an ext4 image into a slot image: the file system with its
# hash tree appended (veritysetup, no superblock), plus the slot's verity
# descriptor (data blocks, hash offset, salt, root hash) signed with the update
# key as CMS. phone-initramfs checks the signature against the update CA and
# opens the slot read-only through dm-verity. <image>.state/ is the factory
# state partition: the descriptor for both slots, which start as this image.
#
#   IMAGE_CLASSES += "phone-verity"
#   IMAGE_FSTYPES += "ext4.verity"

PHONE_OTA_KEYS ??= "${TOPDIR}/../ota-keys"
PHONE_VERITY_KEY ??= "dev"

CONVERSIONTYPES += "verity"
CONVERSION_DEPENDS_verity = "cryptsetup-native openssl-native"
CONVERSION_CMD:verity = "phone_verity ${IMAGE_NAME}.${type} ${IMAGE_LINK_NAME}.${type}"

phone_verity() {
	img=$1
	out=$1.verity
	link=$2.verity
	cp --sparse=always "$img" "$out"
	truncate --size=%4096 "$out"
	size=$(stat -c %s "$out")
	blocks=$(expr $size / 4096)
	veritysetup format --no-superblock --format=1 --hash=sha256 --data-block-size=4096 --hash-block-size=4096 \
		--data-blocks=$blocks --hash-offset=$size "$out" "$out" > "$out.info"
	salt=$(sed -n 's/^Salt:[[:space:]]*\([0-9a-f]*\)$/\1/p' "$out.info")
	hash=$(sed -n 's/^Root hash:[[:space:]]*\([0-9a-f]*\)$/\1/p' "$out.info")
	if [ -z "$salt" ] || [ ${#hash} -ne 64 ]; then
		bbfatal "veritysetup did not report a salt and root hash: $(cat $out.info)"
	fi
	rm "$out.info"
	printf 'data_blocks=%s\nhash_offset=%s\nsalt=%s\nroot_hash=%s\n' $blocks $size $salt $hash > "$out.desc"
	openssl cms -sign -binary -nosmimecap -md sha256 -in "$out.desc" -outform DER -out "$out.desc.sig" \
		-signer "${PHONE_OTA_KEYS}/${PHONE_VERITY_KEY}.cert.pem" -inkey "${PHONE_OTA_KEYS}/${PHONE_VERITY_KEY}.key.pem"
	ln -sf "$out.desc" "$link.desc"
	ln -sf "$out.desc.sig" "$link.desc.sig"
	rm -rf "${IMAGE_NAME}.state"
	mkdir "${IMAGE_NAME}.state"
	for s in a b; do
		cp "$out.desc" "${IMAGE_NAME}.state/rootfs_$s.verity"
		cp "$out.desc.sig" "${IMAGE_NAME}.state/rootfs_$s.verity.sig"
	done
}
