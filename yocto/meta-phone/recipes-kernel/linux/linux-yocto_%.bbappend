# What every Kario kernel carries (phone-kernel.inc, shared with the phones'
# own kernels such as linux-sdm845).
require phone-kernel.inc

# The Qualcomm boards (machine kario-qcom): qemuarm64's kernel source and
# metadata, configured from arm64's defconfig, which carries the SoCs'
# drivers, plus what the platform and the SKUs' modules need.
COMPATIBLE_MACHINE:kario-qcom = "kario-qcom"
KMACHINE:kario-qcom = "qemuarm64"
KBRANCH:kario-qcom = "${KBRANCH:qemuarm64}"
SRCREV_machine:kario-qcom = "${SRCREV_machine:qemuarm64}"
KBUILD_DEFCONFIG:kario-qcom = "defconfig"
KCONFIG_MODE:kario-qcom = "alldefconfig"
SRC_URI:append:kario-qcom = " file://phone-qcom.cfg"
