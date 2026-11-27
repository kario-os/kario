# btvirt (BlueZ's controller emulator, in bluez5-noinst-tools) for the
# emulator's selftest: two virtual controllers, the phone's and the device
# it pairs with. No readline (GPLv3): no bluetoothctl. No obexd: the shell
# speaks OBEX itself (file push, phone book access) on the sockets BlueZ
# hands it; obexd needs libical, and libical ICU (39 MB in the image).
PACKAGECONFIG:append = " testing"
PACKAGECONFIG:remove = "readline obex-profiles"
NOINST_TOOLS_TESTING = "emulator/btvirt"
# btvirt's fixes (13-prototype.md, yocto/btvirt-test.py): its main loop ran
# callbacks of timers already removed and so died of SIGBUS on QEMU at the
# end of an inquiry (0001-0003, from BlueZ after 5.86), and a second
# pairing of the same two controllers left the kernel's connection
# reference one short (0004, ours).
FILESEXTRAPATHS:prepend := "${THISDIR}/files:"
SRC_URI:append = " \
    file://0001-emulator-btdev-fix-inquiry_timeout-inquiry-destroy.patch \
    file://0002-shared-mainloop-Skip-removed-mainloop-events.patch \
    file://0003-shared-timeout-Fix-use-after-free-when-a-timeout-rem.patch \
    file://0004-emulator-btdev-Ask-for-the-IO-capability-on-every-pairing.patch \
"
