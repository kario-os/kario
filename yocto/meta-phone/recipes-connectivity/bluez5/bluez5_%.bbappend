# btvirt (BlueZ's controller emulator, in bluez5-noinst-tools) for the
# emulator's selftest: two virtual controllers, the phone's and the device
# it pairs with. No readline (GPLv3): no bluetoothctl. No obexd: the shell
# speaks OBEX itself (file push, phone book access) on the sockets BlueZ
# hands it; obexd needs libical, and libical ICU (39 MB in the image).
PACKAGECONFIG:append = " testing"
PACKAGECONFIG:remove = "readline obex-profiles"
NOINST_TOOLS_TESTING = "emulator/btvirt"
