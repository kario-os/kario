> English translation of [the Chinese original](../24-sku-framework.md). The Chinese text is authoritative; keep both in sync.

# SKU framework: one hardware configuration produces one firmware package

> Status: **prototype ✓** (`sku/`, `tools/sku.py`, the `phone-sku` recipe, `make sku`). The three SKUs on the two mainline Snapdragon boards (Snapdragon 662, Snapdragon 460) all build firmware packages, **not yet run on real hardware**; the two QEMU SKUs, T9 keypad and QWERTY keyboard, pass the boot selftest (57 (internal document) verification baseline). Real-hardware testing on a Phicomm N1 box is planned (§4, **not implemented**). Whether to replace a KaiOS phone from boot or interface with its base is in §7 (a survey, **not tried on a phone**). For hardware selection see 61 (internal document); for keypads and displays see 52 (internal document).

## 0. Summary

- Each SKU has one configuration that only says "which board + which display + which keypad + memory tier + region + product layer"; the generator checks that these fit together and expands them into the Yocto configuration, device tree overlays and the runtime device description; `make sku SKU=<name>` builds the image and collects the firmware package, and every SKU in `sku/skus/` is also a target of its own, `make sku-<name>` (`make skus` lists them all). The binaries of the Shell, the services and the apps are identical on all SKUs.
- There is only one key code table (`sku/keys.toml`): the device tree's `linux,keymap` is generated from it, and tests check that phone-paint's evdev table knows every key code in it and that the key code values match the kernel headers.
- SKUs on the same board share the kernel and the root filesystem and differ only in the device tree overlay, `sku.json` and the product layer; later the overlay will be selected at boot by board ID, so that one image can cover a whole board family (**not implemented**).

## 1. Files

```
sku/
  keys.toml            key name → Linux key code
  boards/*.toml        boards: Yocto machine, device tree, baseband backend, GPU or not, module wiring (device tree labels), notification light and backlight ([leds], backlight: kernel device class names, hal/)
  displays/*.toml      displays: panel driver compatible, kernel config, resolution, number of DSI lanes
  keypads/*.toml       keypads: scan chip, address, rows and columns, matrix position → key name, form factor (T9 keypad / QWERTY keyboard), the QWERTY Fn layer ([fn]: key → second character on the keycap)
  products/*.toml      product layer: content merged into the configuration layers (profile.d)
  skus/*.toml          SKUs: one of each of the above, plus memory tier, region, optional rotate (portrait panel mounted landscape)
```

Existing SKUs:

| SKU | Board | Display | Keypad | Memory |
|-----|------|----|------|------|
| `qemu-t9` (default image) | QEMU | Virtual screen 480×640 | QEMU's USB keyboard, T9 keypad form factor | 2 GB |
| `qemu-qwerty` | QEMU | Virtual screen 640×480 | QEMU's USB keyboard, QWERTY keyboard form factor | 4 GB |
| `qemu-t9-1g` | QEMU | Virtual screen 480×640 | QEMU's USB keyboard, T9 form factor | 1 GB (the low-memory stress test, `make yocto-stress`) |
| `kario-t9-sd662-2g` | Snapdragon 662 (mainline device tree of the F(x)tec Pro1 X) | 2.8-inch 480×640 (ST7701) | T9 keypad v1 (TCA8418, 5×5) | 2 GB |
| `kario-t9-sd460-2g` | Snapdragon 460 (mainline device tree of the OnePlus Nord N100) | Same as above | Same as above | 2 GB |
| `kario-qwerty-sd662-4g` | Snapdragon 662 | The same display mounted landscape (rotate 90) | QWERTY keyboard v1 (TCA8418, 8×8) | 4 GB |

On development boards, "where a module is connected" is its wiring position on the evaluation board (the keypad's interrupt pin uses a free GPIO); on product boards the wiring comes from the schematic.

## 2. What the generator checks

`tools/sku.py check` (part of `make test`) expands every SKU:

- QEMU boards only get QEMU displays and keypads, real boards only real ones; the keypad's form factor matches the SKU's form factor (`bar-t9` / `bar-qwerty`); the display resolution (with width and height swapped when mounted landscape) equals the form factor's dimensions; the memory tier is valid.
- Keypad matrix: every position is within the row and column range, every key appears only once, and all keys are in `keys.toml`; the required keys are all present (left and right soft keys, OK, Back, Clear, Call, End, arrow keys; 0–9 / * / # on the T9 keypad; the 26 letters, Space and Shift on the QWERTY keyboard). The Fn layer is only for letter keys in the matrix plus comma and period, each producing a single character, and a keypad with an Fn layer must have an Fn key.
- The kernel drivers for the display and keypad chips are in the kernel configuration fragment of the kario-qcom machine.
- The notification light (`[leds] notify`) and the backlight (`backlight`) are valid device class names, and a multicolor light's `notify_color` is 0–255 per channel.
- Whether the board can be woken from suspend (`suspend`): real boards can by default, virtual ones (`virtual = true`, the emulator) cannot, and a board that cannot never suspends ([28](28-battery.md) §3).
- A power key of its own (`power_key = true`, a side key): there the end key is not also the power key; without one it is, the Nokia way ([18](18-nokia-form-factors.md)).
- The NFC controller (`nfc`): `sim` (the emulator) or `nci:/dev/<node>`; left out, the board has no NFC and phone-nfcd exits ([30](30-nfc.md)).
- The fingerprint sensor (`fingerprint`) is `sim` or `none`: the emulator says `sim`, a real phone leaves it out until its sensor has a driver ([31](31-lock-and-biometrics.md)).
- When kernel sources are available (the copy in the Yocto work directory, or `PHONE_KERNEL_SRC`), it collects labels by following the `#include`s of the board dts and checks that every label the wiring references exists. The regulator on the OnePlus Nord N100 is called `vreg_l9a`, while on the Snapdragon 662 board it is `pm6125_l9a`, so a mistake is reported at generation time.
- `tools/sku_test.py` additionally checks that the form factor table matches `shell/device.go`, that phone-paint knows every key code, and that the key code values match `/usr/include/linux/input-event-codes.h`.

## 3. What it generates

| Artifact | Content |
|------|------|
| `yocto/kas/generated/sku.yml` | Machine (`qemuarm64` / `kario-qcom`), `PHONE_SKU`; QEMU's screen size and memory; the device tree file name for real boards. The Makefile appends it after every kas configuration |
| `share/phone/sku.json` (`phone-sku` package) | SKU, board, form factor, display, keypad, memory, `web_gpu`, baseband backend, fingerprint sensor backend (phone-keystored picks by it), region, product, plus `fn` (the Fn layer) on QWERTY keyboards. When phone-shell is not given `--device`, it picks the form factor from this file and hands the Fn layer to the renderer during the handshake, and phone-paint uses it to produce characters while Fn is held ([23](23-renderer-protocol.md) §2); the memory limit of the web process is set by the memory tier, and whether it uses the GPU by `web_gpu`; when the board has a notification light and a backlight configured there are also `leds` (`notify`: name, colour) and `backlight`, which the Shell uses for the slow blink and for dimming before the screen goes off ([11](11-platform-architecture.md) §"Then what is hal/") |
| `profile.d/20-product.json` | Product layer (the product name in About phone, etc.), stacked on top of the image's own configuration layers |
| Device tree overlay (real boards) | The display on DSI (compatible, reset pin, power supplies) and the keypad on I2C (interrupt, rows and columns, keymap, wake-up capable); the `phone-sku` recipe compiles it with dtc, and `fdtoverlay` merges it into the board's device tree (the kernel is built with symbols, `-@`), giving `phone-sku-<SKU>.dtb` |
| Firmware package `build/sku/<SKU>/firmware/` | Kernel (with phone-initramfs bundled, which mounts the root filesystem, [25](25-system-update.md)), the merged device tree (real boards), the root filesystem, `sku.json` and a manifest (SHA-256 and size of each file); it first checks that all files are present and only then replaces the old package |

The `kario-qcom` machine: one kernel carries the device trees of both boards; the kernel uses the same source as qemuarm64, with its configuration starting from the arm64 defconfig plus what the platform needs (Landlock, seccomp, namespaces, cgroup, nftables connection marks, and dm-verity / squashfs / nbd for system updates) and the module drivers the SKUs use. Packages keep qemuarm64's instruction set tuning (ARMv8.0 + CRC, which runs on both the A73 and the A53), so the shared cache is reused directly; mass production will switch to A73 + A53 tuning.

The QWERTY SKU mounts the same 2.8-inch portrait display in landscape: when phone-paint finds that the display's mode is exactly the form factor's dimensions with width and height swapped, it draws at the form factor's landscape size and rotates the picture 90° clockwise when putting it on the screen (dirty regions are converted accordingly).

## 4. Real-hardware testing on the Phicomm N1 box (planned, **not implemented**: waiting for network connectivity between the build machine and the box)

**Why this box**:
- The N1 is an Amlogic S905D: quad-core Cortex-A53, 2 GB RAM, 8 GB eMMC, with HDMI, two USB 2.0 ports and gigabit Ethernet.
- The mainline kernel has its device tree (`meson-gxl-s905d-phicomm-n1.dts`); HDMI goes through meson's DRM driver, and for hardware video decoding there is meson's V4L2 driver (in staging; which formats it can decode has to be measured).
- The A53 is exactly the little core of the Snapdragon 460 and 662. Of the devices we have, it is the cheapest real ARM hardware.

**What it cannot test**: baseband, the phone display (MIPI DSI), the matrix keypad (TCA8418), battery and power consumption, and Qualcomm's specific boot method and user space (ABL, QRTR). These have to wait for the Snapdragon devices (R16).

**What to prepare first**:
- Flash Armbian onto the N1: the community commonly uses ophub's amlogic-s9xxx-armbian; pick the s905d image.
- Connect an HDMI monitor, a USB keyboard and an Ethernet cable.
- Add the dedicated public key that the build machine uses to reach development boards (`kario-board-test`) to root's `authorized_keys`.
- A path from the build machine to the box: install Tailscale or ZeroTier on both sides, or use an SSH reverse tunnel through a machine both sides can reach.

**Step one: no BSP, our root filesystem on top of Armbian**. Copy the arm64 root filesystem built by Yocto to the N1: its instruction set tuning is ARMv8.0 + CRC, which runs directly on the A53. chroot into it, hand it the host's `/dev/dri`, `/dev/input` and cgroups, then test:

| # | What to test | How | Compare with |
|---|--------|--------|------|
| 1 | Text selftest | `phone-shell --selftest`: all cases pass; record the time of each case | The boot selftest on QEMU |
| 2 | Pixel selftest on a real screen | phone-paint draws to HDMI (DRM/KMS, no GPU), with the USB keyboard as the keys; run `--gui` once for each of the three form factors, recording frame times and CPU usage. When the display mode is larger than the form factor's dimensions, phone-paint draws at the form factor's size and centers it on the screen (a 480×640 picture in the middle of 1080p), so what you see is what the phone would show (unit tests ✓, **not yet on a real screen**) | `make pixel-selftest` on the dev machine |
| 3 | Memory | PSS of the 23 processes after boot; the memory-pressure case with a real 2 GB of RAM: first end the web pages of background apps, then end background apps | 23 processes totaling about 217 MB on QEMU |
| 4 | Web view | With the root filesystem of the web view image, run WPE WebKit on the A53: page rendering time, web process memory | The measurements in [15](15-web-vs-semantic-benchmark.md) |
| 5 | Benchmark | Rerun the semantic page vs web page comparison from `yocto/qemu-bench.sh` on real hardware | Same as above |

**Step two: add an N1 SKU and boot our own image from a USB stick**:
- What to add: a new machine (meson, with the kernel enabling meson DRM, eMMC and video decoding), the N1 board in `sku/boards/`, the display "HDMI", the keypad "USB keyboard".
- Boot: through the U-Boot that Armbian uses, boot our kernel and initramfs from the USB stick.
- Then test:
  - A/B updates, dm-verity and rollback on real storage: walk through the `yocto-ota-test` steps by hand on real hardware ([25](25-system-update.md));
  - hardware video decoding: play H.264 through the V4L2 memory-to-memory decoder (R24).

**Where the results go**:
- measured numbers go into the verification baseline;
- problems found go into this section and the corresponding topic documents.

## 5. Not done yet

- Real hardware: the boot method (Qualcomm's ABL needs a boot.img), the flashing package format, and the firmware of the various subsystems (signed firmware for the GPU, baseband and audio DSP has to come from the device maker) are all **not implemented**; the firmware package is currently the kernel, device tree, root filesystem and manifest.
- Selecting the overlay at boot by board ID (one image covering a board family) is **not implemented**.
- UNISOC T610: the mainline kernel has no display, baseband or USB support, so it has to wait for the vendor BSP (61 (internal document) §2.1); no SKU for now.

## 6. Real-hardware testing on an off-the-shelf phone (survey of 2026-11)

The N1 box can only test compute and storage; to test baseband, sound, display and power, we need a phone with good mainline kernel support. Nura (formerly postmarketOS, renamed in September 2026) has ready-made images, so we can install it first, run our root filesystem on top of it with chroot for the same tests as in §4, and then build an SKU for that phone. The table below is based on Nura's device pages and what the community says; **not tested by us**.

| Phone | Nura support | Suitable? |
|------|------------|---------|
| Xiaomi Mi 9 Pro 5G (crux, Snapdragon 855+) | None | Not suitable: no port |
| Xiaomi Mi 9 (cepheus, Snapdragon 855) | "testing" category: the mainline kernel only has framebuffer display, physical buttons and USB; no baseband, sound or GPU | Not suitable: none of the things we want to test are there |
| OnePlus 6 / 6T (Snapdragon 845) | "community" category, the best-maintained tier: calls, SMS, mobile data, sound, GPU (freedreno) and suspend all work | Suitable, cheap second-hand; recommended |
| Xiaomi Poco F1 (Snapdragon 845) | "community" category, the same platform as the OnePlus 6 | Suitable |
| Pixel 3a (Snapdragon 670) | "community" category | Suitable |
| Redmi Note 4X, Qualcomm version (mido, Snapdragon 625 / MSM8953) | "community" category, through the common "Generic MSM8953" image (v26.06): modem firmware, Wi-Fi, Bluetooth, GPU (Adreno 506) and sound (UCM) are there; battery and charging status are known to be unreliable, suspend while charging has problems, and some display and touch vendors' versions lack drivers | Suitable and the cheapest; its eight A53 cores are the little cores of the Snapdragon 460 / 662, so it is closer to our target than the Snapdragon 845. Buy a "mido" with its original screen, not the MediaTek Redmi Note 4 |

**The Redmi Note 4X in detail** (kernel sources checked on 2026-11-12): the mainline kernel we use (6.18) has its device tree, `msm8953-xiaomi-mido.dts`, but its screen there is only the framebuffer the boot loader leaves (simple-framebuffer, 1080×1920), with no DSI panel driver: it displays, but cannot turn the panel off or change the brightness, so screen-off power saving cannot be tested on this phone; the panel drivers (five vendors) are in the msm8953-mainline kernel branch Nura uses. The notification light is the AW2013's red, green and blue LEDs (`red:indicator` and so on, which the driver blinks by itself), and the only keys are volume up (GPIO) and power and volume down (PMIC). Hence three steps: install Nura first to confirm calls, SMS and Wi-Fi; then run our root filesystem in a chroot on Nura (as in step one of §4); finally make this phone's SKU (**not implemented**), with the kernel switched to the msm8953-mainline branch and a new kind of board in the SKU framework: a handset with its own screen and touch, and a keyboard on USB.

The Snapdragon 845 and the Snapdragon 662 / 460 we chose all use the mainline kernel + ModemManager + QRTR (R16), so telephonyd's ModemManager backend, phone-paint's DRM, keys, suspend and current draw can all be tested on them first. These phones are all touchscreen phones without a physical keyboard; to test keys, use a USB-C keyboard (OTG) or the phone's volume and power keys. The test content and steps are recorded in the internal backlog (R46); buying the phone, unlocking the bootloader, flashing Nura and connecting the phone to the build machine's network **needs the owner**.

## 7. KaiOS phones: replace from boot, or interface with the system base (survey of 2026-11)

With a KaiOS phone in hand there are two paths: **(a) interface with the base**: keep the vendor's boot loader, kernel and vendor partition, replace only Gecko and Gaia with our user space, and reach the hardware through Android's HALs (this is what Ubuntu Touch and Sailfish OS do on Android phones with Halium / libhybris); **(b) replace from boot**: the mainline kernel plus our whole system, the same as on the Snapdragon 662 / 460 (R16). What follows comes from public sources and the local mainline kernel sources (6.18); **not tried on a KaiOS phone**.

**Conclusion: take (b), only on models that already have a mainline port; do not do (a).** By Nura's device list, no KaiOS phone has a maintained mainline port today (reason 4), so we do not work on KaiOS phones for now, and real-phone testing uses the phones of §6 first. Reasons:
1. **(a) does not spare flashing the boot partition either**. Halium has to change the kernel configuration for systemd, rebuild the kernel and flash its own boot image; the boot loader of KaiOS phones is locked and accepts only a boot image signed by the vendor. The ways to write boot (EDL, unlocking, `fastboot oem sudo` on the 2780 Flip) are the same for both paths: both have to take the hardest step, and after it (a) still carries a layer of vendor user space.
2. **The vendor kernels are too old for our system to start on**. The Nokia 8110 4G on KaiOS 2.5 has a 3.10 kernel; the systemd 259 in our image needs 5.4 or later (its README calls this the "minimum baseline"), and Landlock, the cgroup freezer and memfd, which the sandbox, the sound rings and the media surfaces use, are all newer than 3.10 too (§7.5).
3. **There is no ready Halium**. Halium only has versions for Android 7.1 and 9–13, and since 20.04 new Ubuntu Touch ports need 9.0 or later; the base of KaiOS 2.5 is Android 6. libhybris also first needs Android's bionic and init rebuilt with its patches, and KaiOS phones have no LineageOS device tree to build from.
4. **Mainline was ported to these phones, but nobody maintains the ports now**. By Nura's device list ([Devices](https://wiki.nura.eco/wiki/Devices), checked 2026-11-14), the ports of the Nokia 8110 4G, 6300 4G, 8000 4G and 2780 Flip are all "archived", no longer supported; the only KaiOS phone still in a supported category is the 2720 Flip, in "downstream", on the vendor's 3.10.49 kernel with very few features; the generic package `qcom-msm8909` (Snapdragon 210) is in "testing", and whether it boots these Nokias is **unverified**. When they were ported in 2022, keys, Wi-Fi, Bluetooth, charging, the display and the backlight worked, and the modem came up through QRTR and rmtfs; the device trees are still in the community msm8916-mainline kernel, and the 2780 Flip's is in the msm89x7-mainline kernel (§7.2). telephonyd's ModemManager backend and phone-paint's DRM and evdev plug straight into this path, but taking (b) on a KaiOS phone means carrying on these archived ports ourselves.
5. **Security updates**. On (a) the kernel and the HALs stay at the vendor's last release, and our system update ([25](25-system-update.md)) can update only user space; (b) follows mainline and the long-term support releases.

(a) stays only as a fallback: when one kind of hardware never gets a mainline driver, add a vendor backend in the service that manages it (the approach of [11](11-platform-architecture.md) §16), rather than putting the whole system on the vendor base.

Whichever path, KaiOS phones are test phones only: they all have 512 MB of memory, half our smallest memory tier, with no room for the web view (§7.2); their partition tables have no A/B either, so our system update needs the partitions rearranged (§7.3).

### 7.1 How KaiOS is built

KaiOS was forked from Firefox OS (B2G) and has three layers: Gaia is the UI and the apps, written in HTML / JS; Gecko is the web runtime and doubles as the device interface; Gonk is the base: the Linux kernel plus Android's HALs, the vendor's binary libraries and daemons such as rild. There is no ART, so Android apps cannot run; Gecko itself is the "system_server" and calls Android's HALs directly.

| Layer | KaiOS 2.5 | KaiOS 3.x | KaiOS 4.0 |
|----|-----------|-----------|-----------|
| Runtime | Gecko 48 | Gecko 84 | Gecko 123 |
| Android base of Gonk | Android 6.0: vendor libraries and the system in the same system partition | Android 10: vendor libraries in a vendor partition of their own (Treble) | Android 14 |
| Kernel | 3.10 (Nokia 8110 4G) | 4.x (**unverified**) | The GKI kernels Android 14 supports are 5.10–6.1; which one this generation of phones uses is **unverified** |
| Modem | The rild socket, the Firefox OS way (that KaiOS 2.5 kept it as it was is **unverified**) | Gecko's `nsRilWorker` calls `android.hardware.radio@1.1::IRadio` over HIDL | From Android 13 it calls `IRadio` 1.6 instead (in the same file) |
| Display | Firefox OS's `GonkDisplay`: framebuffer or hwcomposer (**unverified**) | `GonkDisplay`: framebuffer (fbdev), DRM, or hwcomposer plus gralloc, chosen per model | Same as left |
| Keys | Since Firefox OS, Gecko has carried a copy of Android's InputReader (**unverified**) | Gecko carries a copy of Android's EventHub, InputReader and InputDispatcher, reading evdev | Same as left |
| First phone | Nokia 8110 4G | Nokia 2760 Flip | TCL Flip 4 (Qualcomm SM4635, 5G) |

So keys are the same on both paths: the vendor kernels report the keypad as evdev too.

### 7.2 The chips of KaiOS phones, and how far mainline has got

| Chip | Models (examples) | CPU | Mainline kernel (6.18, checked in local sources) | Community port | The door to flashing |
|------|-------------|-----|------------------------------|---------|---------|
| Snapdragon 205 (MSM8905) | Nokia 8110 4G, 2720 Flip, 800 Tough; CAT B35 | 2 × Cortex-A7, 32-bit only | The MSM8909 family's drivers are in mainline (pins, clocks, interconnect, the modem PIL, frequency scaling, thermal); the device trees of the SoC and the phones are not | Nura's nokia-argon (8110 4G, merged in 2022: keys, Wi-Fi, Bluetooth, charging, display (an SPI panel, `panel-mipi-dbi`), backlight, modem, booting through lk2nd) is now "archived", no longer supported; the msm8916-mainline kernel (6.12) still has `qcom-msm8905-nokia-argon.dts`. The 2720 Flip (nokia-beatles) is in "downstream", on the vendor's 3.10.49 kernel | EDL: BananaHackers has collected signed firehose loaders; the 8110 4G can `fastboot oem unlock-go` after an unlock package is flashed through EDL |
| Snapdragon 210 (MSM8909) | Nokia 6300 4G, 8000 4G | 4 × Cortex-A7, 32-bit only | Same as above | Nura's nokia-leo (6300 4G) and nokia-sparkler (8000 4G), merged in 2022, are both "archived" now, no longer supported; the community kernel still has both phones' device trees; whether Nura's generic package `qcom-msm8909` (in "testing") boots them is **unverified** | EDL loaders exist (except for the US models) |
| Snapdragon 215 (QM215, almost the same as MSM8917) | Nokia 2780 Flip, Alcatel Go Flip 4 (KaiOS 3.x) | 4 × Cortex-A53, 64-bit | MSM8917's SoC device tree, clocks, pins and display (mdp5) are there; device trees of QM215 phones are not | Nura's former nokia-weeknd used the porter's own 6.4-rc4 branch; the kernel sources disappeared, and it was removed from the package repository while preparing v25.12, and is "archived" in the device list; now the msm89x7-mainline kernel (v7.1.3, July 2026) has `qm215-nokia-weeknd.dts`, describing the matrix keypad, two SPI panels (`panel-mipi-dbi-spi`), backlight, modem, Wi-Fi and Bluetooth (wcnss), sound card, the PMIC's charger and fuel gauge, and the vibrator, and Nura's generic package `device-qcom-msm89x7` (arm64, "testing" category) takes `qcom/qm215-*` device trees; which of these really work is **unverified** | 2780 Flip (US model TA-1420): in fastboot, after `fastboot oem sudo`, `avb_custom_key`, vbmeta, boot and system can be flashed, and the community tutorial flashes lk2nd into recovery; HMD Device Kit can also flash most partitions. Someone unlocked it and said it should in principle boot an "arm32-binder64" Android generic system image (i.e. the vendor user space is 32-bit), but he could not get it to boot into the normal system (**unverified**) |
| UNISOC SC9820E, SC7731E | The JioPhone series, Orange Sanza 2 | — | None | None found | **Unverified** |
| MediaTek MT6731 (plus the 3G MT6572) | Some models in South Asia and Africa | 4 × Cortex-A53 (MT6731) | MT6731: none; MT6572 has only the most basic dtsi | None found | **Unverified** |
| Qualcomm SM4635 (KaiOS 4.0) | TCL Flip 4 | — | **Unverified** | None found | **Unverified** |

Two families had mainline ports, but Nura maintains none of these phones' ports now (only the 2720 Flip has a downstream port on the vendor kernel). MSM8905 / 8909 (the five Nokias and the CAT B35): the community kernel has the phones' device trees, the modem was brought up back in 2022, and EDL is a way to flash them; they are 32-bit Cortex-A7 chips, while our image is arm64 only today (ARMv8.0 tuning, §3), and the seccomp filters of the sandbox and the renderer know only the aarch64 and x86_64 architecture numbers, so an armv7 build comes first (§7.4). The Nokia 2780 Flip with QM215 has 64-bit A53 cores, so our arm64 image runs as it is and fastboot is enough to flash it, but nobody has yet written down which parts of its mainline port work (**unverified**). The screens are 2.4–2.8-inch 240×320, exactly the logical size of the `bar-t9` form factor (480×640 drawn at 2×, `shell/device.go`), so drawn at 1× they get the same layout.

**Memory is the hard limit**: these four Nokias (8110 4G, 6300 4G, 8000 4G, 2780 Flip) all have only 512 MB of memory and 4 GB of storage. Our smallest memory tier is 1 GB (`MEMORY` in `tools/sku.py` accepts only 1g–4g); on QEMU the 23 processes after boot take about 217 MB (§4); the web process's memory limit is at least 384 MB, because WPE needs about 250 MB for an ordinary page (`shell/webview.go`). Going by these numbers, a 512 MB phone has no room for the web view and can run only semantic-UI apps (an estimate, **unverified**). So they serve only as test phones, for the modem, keys, display, suspend and current draw; a 512 MB memory tier is needed, with web nodes turned off on it (**not implemented**).

### 7.3 The two paths item by item

| Item | (a) Interface with the base | (b) Replace from boot |
|----|-------------|------------------|
| What is replaced | Gecko, Gaia; boot too (the kernel reconfigured for systemd, the initramfs replaced by ours) | Kernel, initramfs, root filesystem |
| What is kept | The boot loader, the subsystems' firmware, the vendor libraries (in the system partition on KaiOS 2.5, in the vendor partition on 3.x), Android's init and vendor services such as rild | The boot loader (or lk2nd chain-loaded after it), the subsystems' firmware partitions: the modem's and Wi-Fi's firmware is read at boot from the phone's own partitions |
| Precondition for flashing | Being able to write boot: an EDL loader or unlocking (2780 Flip: `fastboot oem sudo`) | Same as left |
| Kernel | The vendor's 3.10 / 4.x, stuck at its last release | Mainline plus this phone's device tree and a few patches |
| How the hardware is used | libhybris loads the vendor libraries, linked against bionic, into our processes; on Treble models (KaiOS 3.x), HALs built as separate services can also be spoken to over HIDL with libgbinder directly, without libhybris; both need a stripped-down Android started first (the Halium way) | Mainline drivers: DRM, evdev, ALSA, the power supply class, the LED class, QRTR |
| Modem | KaiOS 2.5: RIL messages on the rild socket; 3.x: HIDL `IRadio` | QMI over QRTR, managed by ModemManager (rmtfs reads and writes the modem's partitions for it) |
| Display | Framebuffer (`/dev/graphics/fb0`) or hwcomposer plus gralloc | DRM/KMS; the SPI panels are `panel-mipi-dbi` |
| Sound | Android's audio HAL (`audio.primary.*.so`) through libhybris | ALSA; whether these phones' sound cards are complete in mainline is **unverified** |
| Keys | evdev (the vendor kernels report them that way too) | evdev |
| Vibration | The LED-class vibrator of Android kernels, which is exactly what the Shell writes today (`/sys/class/leds/vibrator`, [20](20-audio.md) §4.1) | In mainline a vibrator is an input device's force feedback (`FF_RUMBLE`); this kind has to be added |
| Sandbox | Lacks Landlock and most of cgroup v2, and on 3.10 memfd as well (§7.5) | All seven layers, as on QEMU |
| init | systemd 259 cannot start (it needs 5.4 or later); another init or an old version is needed | systemd as it is |
| Security updates | None for the kernel or the HALs | Follow mainline and the long-term support releases |
| Partitions for system updates | The vendor partition table has no A/B: the 8110 4G installs signed update.zip packages with Android's recovery, and the 2780 Flip has a single boot (plus recovery, vbmeta, dtbo). Our A/B has the initramfs pick the slot, independent of the boot loader ([25](25-system-update.md) §0), but it needs two system slots plus two partitions for state and data: either rewrite the partition table, or use system as one slot and carve the other out of userdata (**unverified**); the kernel stays the vendor's, so there are no kernel updates | As Nura does it, lk2nd goes into boot (into recovery on the 2780 Flip) and the system image goes into userdata, divided again with an msdos partition table; our two slots and the state and data partitions all fit there, a slot being about 460–490 MB ([25](25-system-update.md) §3), which 4 GB of storage holds. lk2nd starts the kernel from extlinux.conf, and replacing the kernel and initramfs has no A/B yet (**not implemented**) |
| Memory | 512 MB (§7.2), and a stripped-down Android has to run as well (Android's init, servicemanager, the HAL services, rild); how much more that takes is **unverified** | 512 MB, only our own processes |
| Work per phone | Build Halium (with no existing device tree), tune libhybris, write a RIL backend, a framebuffer output for the renderer, sandbox degradation | Device tree, the panel's init sequence, SKU |

### 7.4 What our code needs for each

| Part | (a) Interface with the base | (b) Replace from boot |
|------|-------------|------------------|
| `hal/` | Lights, backlight and battery are sysfs device classes on vendor kernels too, and mostly work; Qualcomm's vendor kernels often make the backlight the LED-class `lcd-backlight`, which `hal/backlight` would have to recognize as well (**unverified**); on 3.10 kernels USB device mode is Android's own `android_usb`, not configfs, so `hal/usbgadget` would need another implementation (**unverified**). For the classes that need HAL libraries, a backend must not load vendor libraries into the Shell: a separate HAL host process runs them (with libhybris inside it), the `hal/` backend is its client, and the host drops its privileges the way `phone-usbshared` does | No new interface; the board configuration names the LEDs and the backlight; vibration moves into `hal/` with a backend for an input device's force feedback |
| telephonyd | A new backend implementing the `Modem` interface (next to `SimModem` and `MmModem`): on KaiOS 2.5 it speaks the rild socket (oFono's rilmodem and Jolla's ril driver are prior art); on 3.x HIDL `IRadio` (libgbinder-radio, with Sailfish's ofono-binder-plugin as prior art; this is the backend [11](11-platform-architecture.md) §16 keeps a place for); one more value for the board configuration's `modem` | Nothing to add, the ModemManager backend exists; QRTR, rmtfs and the modem's startup have to be verified on the phone (Nura now uses the modem part of the `soc-qcom-msm8916` package: `msm-modem`, and `q6voiced` for call audio; QM215 uses `soc-qcom-msm89x7-modem`) |
| Renderer (phone-paint) | A new output: framebuffer (mmap `/dev/fb0`, page flips with `FBIOPAN_DISPLAY`), or hwcomposer through the HAL host process; keys still read from evdev | DRM dumb buffer as it is; a 240×320 panel drawn at 1× (a form factor tier with scale 1, **not implemented**) |
| Sandbox | The degradation policy (§7.5) | No change; armv7 needs `seccomp_arm.go` in `sandbox` (`AUDIT_ARCH_ARM`, the 32-bit system call numbers), `helper_arm.go` (32-bit ARM, like x86_64, still has the path calls without "at", which have to be blocked as well) and 32-bit ARM in the renderer's `seccomp.rs`; the 2780 Flip is arm64 and needs none of this |
| Build | An armv7 user space, plus the Android half (Halium) | A Yocto machine for armv7 (MSM8905 / 8909) or arm64 (QM215); Go and Rust both build for armv7 |
| SKU | A "whole phone" kind of board (display and keypad come with the phone, the keys are in its device tree), with `modem` naming the RIL backend; a 512 MB tier in `MEMORY`; a form factor tier drawn at 1× | The same "whole phone" kind of board (the Redmi Note 4X in §6 needs it too), key codes from the phone's device tree; memory tier and form factor as on the left |

### 7.5 What the sandbox loses on old kernels

| Mechanism | Kernel requirement | Where we use it | 3.10 (KaiOS 2.5) | 4.x (KaiOS 3.x, version **unverified**) | What the current code does without it |
|------|---------|-----------|------------------|------------------------------|---------------------|
| systemd 259 | 5.4 or later | init, service units, socket activation, the fd store ([11](11-platform-architecture.md) §14) | No | No | Older kernels are not supported |
| Landlock | 5.13 or later; limiting TCP from 6.7, limiting signals and abstract sockets from 6.12 | The file allow-lists of apps, helpers and the renderer ([17](17-app-sandbox.md) §3) | None | None | The launcher adds rules by the kernel's ABI version and leaves the layer out when there is none; the self-check marks the matching items "skipped"; only the selftest step that checks the USB sharing helper requires it |
| seccomp | Filters from 3.5; the `seccomp()` system call and `TSYNC` from 3.17; `SECCOMP_RET_KILL_PROCESS` from 4.14 | Apps, helpers, the renderer ([17](17-app-sandbox.md) §4) | No `seccomp()` (unless the vendor backported it, **unverified**) | Yes; before 4.14 "kill the process" degrades to killing the thread | The launcher calls `seccomp()` with `TSYNC` and reports an error when it fails, so the app does not start; a fallback to `prctl(PR_SET_SECCOMP)` is needed (before exec the launcher is locked to one thread, and after exec only that one is left) |
| cgroup v2 | Unified hierarchy from 4.5; the cpu controller 4.15; PSI 4.20; the freezer 5.2; `CLONE_INTO_CGROUP` 5.7; `cgroup.kill` 5.14 | Per-app memory limits, CPU weights, freezing, the low-memory killer ([17](17-app-sandbox.md) §5) | v1 only | Partly | Without a delegated v2 group the Shell creates no app groups: freezing falls back to SIGSTOP and there is no per-app memory limit. With a v2 group it checks only that the hierarchy is unified and the group delegated, not whether the freezer and `CLONE_INTO_CGROUP` exist; on 4.x it has to probe for the features (**not implemented**) |
| `MemAvailable` in `/proc/meminfo` | 3.14 or later | The low-memory killer ([17](17-app-sandbox.md) §5.1) | Missing (unless the vendor backported it, **unverified**) | Yes | When it cannot be read, available memory counts as 0, the system is always "low" and background apps are ended one after another |
| memfd and seals | 3.17 or later | audiod's sound rings, the surfaces of mediad and the Shell ([11](11-platform-architecture.md) §14) | None (Android 6 uses ashmem) | Yes | No fallback: neither sound nor media frames can be passed; with ashmem or tmpfs files instead there are no seals, and an app can truncate the shared memory and crash a service with SIGBUS |
| dm-verity | 3.4 or later | Root filesystem verification for system updates ([25](25-system-update.md)) | Yes | Yes | The initramfs sets up verity and checks the signature itself, which works as on QEMU; but boot is flashed by us and the vendor's boot loader does not know our key, so the chain of trust only starts at boot, and that holds for (b) too |

**Degradation policy (proposed, not implemented)**:
- Missing these, third-party apps do not start (as when the Shell cannot find `phone-sandbox`): separate uids, pid and mount namespaces, no_new_privs, seccomp (including the `prctl` fallback), memfd seals.
- May be missing and things keep running, but it is reported honestly: Landlock, cgroup v2 (freezing falls back to SIGSTOP, no per-app memory limit). 「设置 › 沙箱自检」 (Settings › Sandbox self-check) already marks missing layers "skipped"; the compatibility test report and device attestation would also state the kernel version and which layers are missing, so payment services can refuse as they can on seeing `unlocked` ([11](11-platform-architecture.md) §7).
- The systemd threshold cannot be degraded: either replace init (the Shell's fd store and app scopes depend on systemd and would have to be rewritten), or take (b).

### 7.6 The commercial side

- **KaiOS Technologies**: the system is free for handset makers, and the company earns from service fees (FOTA, for example), advertising (KaiAds), the app store and search (public statements from 2018 and 2019; whether this still holds is **unverified**). Neither path uses Gecko, Gaia or KaiStore, so neither needs its license; what (a) uses is the BSP under Gonk, which belongs to the chip maker and the handset maker. What working with it could bring is its experience integrating BSPs on low-end chips and passing carrier certification; its store and advertising compete with our store and push, and the room for cooperation is **unverified**.
- **The vendor binaries** (HALs, rild, the modem's and Wi-Fi's firmware): licensed by the chip maker to the handset maker, with no right for third parties to redistribute them (UBports' porting guide also treats them as "blobs" to be taken from the device). Both paths can only use the copy already on the phone, and the image does not carry them: (b) only reads the coprocessors' firmware from the firmware partitions at boot; (a) also loads the vendor's user-space libraries into our processes, with no source to look at when something goes wrong.
- **The community's ways of flashing** (EDL loaders, unlock packages) are fit only for development and enthusiast builds, not as a product channel. Products go on our own hardware (61 (internal document), R16); the use of KaiOS phones is as cheap test phones, like the Redmi Note 4X in §6.

### 7.7 To do (all **not implemented**)

1. Nura has no maintained port for any KaiOS phone now (§7.2), so §6's "flash Nura first and confirm calls" cannot be done on a KaiOS phone; near-term real-phone testing uses §6's "community" phones (Redmi Note 4X, OnePlus 6 / 6T). Working on a KaiOS phone means carrying on an archived port ourselves, one of two: the Nokia 2780 Flip (QM215, arm64, flashed with plain fastboot, no armv7 needed, its device tree in the msm89x7-mainline kernel, which parts work **unverified**); or the 6300 4G / 8000 4G (MSM8909, with an EDL loader and a modem brought up in 2022, armv7 to be added first). The effort is **not estimated**.
2. Run our root filesystem in a chroot on that phone's own mainline kernel (as in step one of §4; on MSM8909 add the armv7 build first): phone-paint draws 240×320 on the SPI panel, and telephonyd's ModemManager backend makes a call.
3. Make this phone's SKU (a "whole phone" kind of board) and boot our kernel and initramfs through lk2nd.
4. Buying the phone, flashing it and connecting it to the build machine's network **needs the owner**.

**Sources**:
- KaiOS: [architecture](https://developer.kaiostech.com/docs/sfp-3.0/), [release history](https://en.wikipedia.org/wiki/KaiOS), [3.x is based on Android 10](https://groups.google.com/g/bananahackers/c/sAhUnL_3o4I) (also the thread on unlocking the 2780 Flip), [4.0](https://kaios.dev/2024/12/whats-coming-in-kaios-4.0/), [the 8110 4G's firmware and boot chain](https://garbsch.eu/posts/dumping-the-nokia-8110-4g-firmware/); the four Nokias' specifications ([GSMArena](https://www.gsmarena.com/compare.php3?idPhone1=9089&idPhone2=9836&idPhone3=10576), [the 6300 4G / 8000 4G launch](https://www.fonearena.com/blog/327241/nokia-8000-4g-nokia-6300-4g-price-specifications.html), [2780 Flip](https://www.smartprix.com/mobiles/nokia_6300_4g_vs_nokia_2780_flip-cpd1nwjij3xe_pd19v2hm2fy.php)); gecko-b2g's [modem](https://github.com/kaiostech/gecko-b2g/blob/gonk/dom/system/gonk/radio/nsRilWorker.h), [display](https://github.com/kaiostech/gecko-b2g/tree/gonk/widget/gonk/libdisplay) and [keys](https://github.com/kaiostech/gecko-b2g/tree/gonk/widget/gonk/libui); Firefox OS's [RIL](https://wiki.mozilla.org/B2G/RIL).
- Flashing: BananaHackers' [EDL](https://wiki.bananahackers.net/guides/boot/edl) and [loader table](https://edl.bananahackers.net/), [unlocking the 8110 4G](https://hmd.fandom.com/wiki/Nokia_8110_4G), [the 2780 Flip flashing tutorial](https://github.com/gogogoghost/nokia-2780-flip-jailbreak-tutorial).
- Which devices Nura supports: the [device list](https://wiki.nura.eco/wiki/Devices) (checked 2026-11-14: the 8110 4G, 6300 4G, 8000 4G, 2780 Flip and Pro1X are "archived", the 2720 Flip is "downstream", the generic packages `qcom-msm8909` and `msm89x7` are "testing", and the Redmi Note 4X, OnePlus 6 / 6T, Poco F1 and the generic `qcom-msm8953` are "community").
- Mainline and Nura: [8110 4G](https://gitlab.com/postmarketOS/pmaports/-/merge_requests/3527), [6300 4G / 8000 4G and the MSM8909 modem](https://gitlab.com/postmarketOS/pmaports/-/merge_requests/3768), [2780 Flip](https://gitlab.com/postmarketOS/pmaports/-/merge_requests/4257) and [its removal](https://git.mrlokop.dev/MRLokop/pmaports/commit/84303cbe008417776bee368a4854e48e9e376fd7); today's generic packages [`device-qcom-msm8909`](https://gitlab.nura.eco/nura/pmaports/-/tree/main/device/testing/device-qcom-msm8909) and [`device-qcom-msm89x7`](https://gitlab.nura.eco/nura/pmaports/-/tree/main/device/testing/device-qcom-msm89x7), and in the kernels [the three Nokias' device trees](https://github.com/msm8916-mainline/linux/tree/v6.12.1-msm8916/arch/arm/boot/dts/qcom) and [the 2780 Flip's device tree](https://github.com/msm89x7-mainline/linux/blob/v7.1.3-r1/arch/arm64/boot/dts/qcom/qm215-nokia-weeknd.dts); [MediaTek and KaiOS](https://www.mediatek.com/press-room/kaios-technologies-and-mediatek-collaborate-to-deliver-affordable-3g-4g-smart-feature-phones).
- The Halium route: [libhybris](https://github.com/libhybris/libhybris), [building Halium](https://docs.halium.org/en/latest/porting/build-sources.html), [UBports' porting guide](https://docs.ubports.com/en/latest/porting/introduction/Intro.html), oFono's [ril driver](https://github.com/sailfishos/ofono/blob/0f4cdba/ofono/drivers/ril/ril_subscription.conf) and [binder plugin](https://github.com/mer-hybris/ofono-binder-plugin).
- Kernel requirements: [systemd 259](https://github.com/systemd/systemd/blob/v259/README), [Landlock](https://man7.org/linux/man-pages/man7/landlock.7.html), [seccomp](https://man7.org/linux/man-pages/man2/seccomp.2.html), [memfd](https://man7.org/linux/man-pages/man2/memfd_create.2.html), [cgroup v2](https://docs.kernel.org/admin-guide/cgroup-v2.html), [AOSP common kernels](https://source.android.com/docs/core/architecture/kernel/android-common).
- Commercial: [developingtelecoms (2018)](https://developingtelecoms.com/telecom-technology/telecom-devices-platforms/7595-kaios-emerging-os.html), [ITWeb (2019)](https://www.itweb.co.za/article/mwc-kaios-to-bring-more-smart-feature-phones-to-africa/KzQenMj892PvZd2r).
