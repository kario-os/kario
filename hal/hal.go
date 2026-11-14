// Package hal is the hardware layer under the system services: a thin
// layer over the mainline kernel's device classes (LED, backlight,
// power supply, USB gadget), not Android's HAL. The kernel's drivers already
// turn each vendor's hardware into the class interface; what differs between
// devices is which instance to use (the notification LED's name, the panel's
// backlight), and that comes from the SKU (sku/boards, 24-sku-framework.md)
// through sku.json, so the same binaries drive every board. Each class has a
// simulated backend for machines without the hardware (the developer
// machine; QEMU, where the selftest also creates virtual instances).
package hal

// SysRoot is where the device classes are; tests point it at a tree of
// their own.
var SysRoot = "/sys"
