# An image whose phone-paint takes its GPU path by default (sku.json's
# paint_gpu not off: the board's, or PHONE_PAINT_GPU_DEFAULT,
# yocto/kas/paint-gpu.yml) must have the libraries that path loads and a
# Mesa driver for the board's GPU: without them the renderer draws on the
# CPU, which no build step would notice (42-gpu-rendering.md §5).
# files/phone-gpu-check.py reads the image; PHONE_SKU_GPU_DRIVER (the SKU's
# kas overlay, tools/sku.py) is the GPU's kernel driver.
#
#   inherit phone-gpu

PHONE_GPU_CHECK = "${@bb.utils.which(d.getVar('BBPATH'), 'files/phone-gpu-check.py')}"
PHONE_SKU_GPU_DRIVER ??= ""

ERROR_QA:append = " gpu-driver"
do_image_qa[file-checksums] += "${PHONE_GPU_CHECK}:True"
IMAGE_QA_COMMANDS += "phone_gpu_check"

python phone_gpu_check() {
    import subprocess
    r = subprocess.run(['python3', d.getVar('PHONE_GPU_CHECK'), d.getVar('IMAGE_ROOTFS'), d.getVar('PHONE_SKU_GPU_DRIVER')],
                       stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)
    bb.note(r.stdout)
    if r.returncode != 0:
        oe.qa.handle_error("gpu-driver", "phone-paint's GPU path has no usable Mesa driver in this image:\n" + r.stdout, d)
}
