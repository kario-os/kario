SUMMARY = "Linux for Snapdragon 845 phones (sdm845-mainline), configured as Nura configures it"
DESCRIPTION = "The sdm845-mainline kernel at the tag Nura's linux-postmarketos-qcom-sdm845 \
builds, with Nura's configuration as the base and Kario's fragments on top; deployed \
with the Android boot image the phone's bootloader loads (phone-bootimg)."
SECTION = "kernel"
LICENSE = "GPL-2.0-only"
LIC_FILES_CHKSUM = "file://COPYING;md5=6bc538ed5bd9a7fc9398086aedcd7e46"

# Nura's pmaports at the commit read (2026-11-17): linux-postmarketos-qcom-sdm845
# 7.1_rc1-r4 builds this tag with this configuration.
SDM845_TAG = "sdm845-7.1-rc1-r0"
NURA_PMAPORTS = "02c676bc2ab9044fd4b50426486e68e302d627f4"
SRC_URI = "git://gitlab.com/sdm845-mainline/linux.git;protocol=https;nobranch=1;tag=${SDM845_TAG};name=machine \
           https://gitlab.nura.eco/nura/pmaports/-/raw/${NURA_PMAPORTS}/device/community/linux-postmarketos-qcom-sdm845/config-postmarketos-qcom-sdm845.aarch64;name=defconfig;downloadfilename=nura-02c676bc-sdm845-defconfig \
          "
SRCREV_machine = "85f1df2a4ec71d7a91dd95a7a49f889d1595ffa8"
SRC_URI[defconfig.sha256sum] = "3ad5a18d99f322f635de3c9f31b54836b2995527e01d813e8acb557ea7374aed"
# The tag's tree alone, not the kernel's history.
BB_GIT_SHALLOW = "1"

require phone-kernel.inc

# The Qualcomm platform's needs, the USB console of a board without a
# serial port, and the phone's own; merged after the generic fragments.
SRC_URI += "file://phone-qcom.cfg file://phone-usb-console.cfg file://phone-fajita.cfg"

PV = "7.1~rc1"
LINUX_VERSION = "7.1-rc1"
LINUX_VERSION_EXTENSION = "-sdm845"
# A package version writes the release candidate as ~rc1 (sorting before
# 7.1), which never equals the Makefile's -rc1.
KERNEL_VERSION_SANITY_SKIP = "1"
KCONFIG_MODE = "alldefconfig"
COMPATIBLE_MACHINE = "^oneplus-fajita$"

inherit kernel kernel-yocto phone-bootimg

DEPENDS += "xz-native bc-native openssl-native util-linux-native elfutils-native gmp-native libmpc-native"

# The kernel is in the boot partition, not in the root file system.
RDEPENDS:${KERNEL_PACKAGE_NAME}-base = ""
RRECOMMENDS:${KERNEL_PACKAGE_NAME}-base = ""

# Packaging leaves signed modules unstripped, so modules_install strips
# their debug information before it signs them.
do_install:prepend() {
	export INSTALL_MOD_STRIP=1
}
DEPENDS:append:aarch64 = " libgcc"
KERNEL_CC:append:aarch64 = " ${TOOLCHAIN_OPTIONS}"
KERNEL_LD:append:aarch64 = " ${TOOLCHAIN_OPTIONS}"

# Every option Kario's fragments set must be in the configuration that is
# built (a module may become built in): one a dependency drops would only
# fail on the phone, where nothing can be seen, so it stops here.
python do_phone_kconfig() {
    import os, re
    with open(os.path.join(d.getVar('B'), '.config')) as f:
        have = {}
        for line in f:
            m = re.match(r'^(CONFIG_\w+)=(.*)$', line.strip())
            if m:
                have[m.group(1)] = m.group(2)
            m = re.match(r'^# (CONFIG_\w+) is not set$', line.strip())
            if m:
                have[m.group(1)] = 'n'
    missing = []
    for frag in sorted(f for f in os.listdir(d.getVar('UNPACKDIR')) if f.startswith('phone-') and f.endswith('.cfg')):
        with open(os.path.join(d.getVar('UNPACKDIR'), frag)) as f:
            for line in f:
                line = line.strip()
                m = re.match(r'^(CONFIG_\w+)=(.*)$', line) or re.match(r'^# (CONFIG_\w+) is (not) set$', line)
                if not m:
                    continue
                opt, want = m.group(1), 'n' if m.group(2) == 'not' else m.group(2)
                got = have.get(opt, 'n')
                if got != want and not (want == 'm' and got == 'y'):
                    missing.append('%s=%s (%s: %s)' % (opt, want, frag, got))
    if missing:
        bb.fatal("the kernel configuration lacks what Kario's fragments set:\n  " + "\n  ".join(missing))
}
addtask phone_kconfig after do_configure before do_compile
