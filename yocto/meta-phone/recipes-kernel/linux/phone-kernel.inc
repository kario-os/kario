FILESEXTRAPATHS:prepend := "${THISDIR}/files:"

# Sound for phone-audiod: ALSA plus the virtio sound card QEMU provides.
SRC_URI += "file://phone-sound.cfg"

# The camera path of phone-mediad (V4L2); vivid stands in for the sensor.
SRC_URI += "file://phone-camera.cfg"

# System updates: RAUC bundles (squashfs, dm-verity, nbd for streaming).
SRC_URI += "file://phone-ota.cfg"

# Wi-Fi and Bluetooth (iwd, BlueZ); the emulator gets simulated radios.
SRC_URI += "file://phone-radio.cfg"
SRC_URI:append:qemuarm64 = " file://phone-radio-sim.cfg"

# Memory pressure, the notification LED, the backlight (hal); the emulator
# gets a userspace LED in place of the board's.
SRC_URI += "file://phone-power.cfg"
SRC_URI:append:qemuarm64 = " file://phone-power-sim.cfg"

# USB: the phone as a device (file sharing over a USB network link), USB-C,
# keyboards and mice; the emulator gets both ends of the cable.
SRC_URI += "file://phone-usb.cfg"
SRC_URI:append:qemuarm64 = " file://phone-usb-sim.cfg"
# File transfer (MTP) needs FunctionFS and the emulator's USB cases
# dummy_hcd; an option the configuration drops is only a warning in
# do_kernel_configcheck (and the selftest would skip), so it stops here.
PHONE_USB_KCONFIG = "CONFIG_USB_CONFIGFS_F_FS=y"
PHONE_USB_KCONFIG:append:qemuarm64 = " CONFIG_USB_DUMMY_HCD=y"
python do_phone_usb_kconfig() {
    with open(d.getVar('B') + '/.config') as f:
        have = set(f.read().splitlines())
    missing = [c for c in d.getVar('PHONE_USB_KCONFIG').split() if c not in have]
    if missing:
        bb.fatal("the kernel configuration lacks %s (phone-usb.cfg, phone-usb-sim.cfg)" % " ".join(missing))
}
addtask phone_usb_kconfig after do_configure before do_compile

# The VPN: WireGuard and policy routing (phone-vpn.cfg). Without them the
# tunnel would only fail on the device, so a configuration that drops one
# stops here.
SRC_URI += "file://phone-vpn.cfg"
PHONE_VPN_KCONFIG = "CONFIG_WIREGUARD=y CONFIG_IP_MULTIPLE_TABLES=y CONFIG_VETH=y"
python do_phone_vpn_kconfig() {
    with open(d.getVar('B') + '/.config') as f:
        have = set(f.read().splitlines())
    missing = [c for c in d.getVar('PHONE_VPN_KCONFIG').split() if c not in have]
    if missing:
        bb.fatal("the kernel configuration lacks %s (phone-vpn.cfg)" % " ".join(missing))
}
addtask phone_vpn_kconfig after do_configure before do_compile
