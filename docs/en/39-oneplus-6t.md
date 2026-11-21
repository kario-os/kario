> English translation of [the Chinese original](../39-oneplus-6t.md). The Chinese text is authoritative; keep both in sync.

# OnePlus 6T (fajita): Kario's first real phone

> Kario on the OnePlus 6T (Snapdragon 845, codename fajita): what to buy, how to prepare the phone, how to build the image, trying it with `fastboot boot` before flashing it, debugging without a serial port, what to check first after boot, how to return to Android, and what is not done yet. The device support follows what Nura (postmarketOS until it was renamed in 2026-09) does: the kernel, its configuration, the firmware and the sound configuration are the same ones Nura uses, see §12.
> Status: the boot image and the userdata image built on the developer machine (2026-11-18, RHEL 8, x86-64, kas-container; §4, SKU `oneplus-fajita-t9`, the image's `sku.json` holding this phone's screen at 4.5 times). Checked: the header as `unpack_bootimg` reads it (version 0, page size 4096, the offsets, the command line); the kernel in the boot image is byte for byte `Image.gz` followed by the 6T's device tree (`dtc` reads "OnePlus 6T", the S6E3FC2X01 panel); all 134 options of Kario's configuration fragments are in the final kernel configuration; the root file system has every service that should be enabled enabled and every firmware file the device tree names; `e2fsck` finds no problem; the sparse image restores to the ext4; `phone-abslot` tested on disk images (`yocto/abslot-test.py`), `fajita-flash.sh` run against a fake fastboot. The phone has not arrived: everything this document says about behaviour on the phone is **not tried on a phone** (未上真机).

## 1. Overview

| Item | How |
|----|------|
| Boot | The phone's own bootloader (ABL) loads an Android boot image directly (header version 0, page size 4096, kernel offset 0x8000, ramdisk 0x01000000, tags 0x100, as in Nura's deviceinfo), written to `boot_a` and `boot_b` |
| Kernel | sdm845-mainline's `sdm845-7.1-rc1-r0` (the tag Nura's linux-postmarketos-qcom-sdm845 builds), Nura's configuration plus Kario's configuration fragments; `Image.gz` with `sdm845-oneplus-fajita.dtb` appended |
| Root file system | The whole `userdata` partition is one ext4 (4 KiB blocks, not partitioned further), which the kernel mounts itself through `root=PARTLABEL=userdata`: **no initramfs** (the boot image carries an empty cpio); it grows to the whole partition at the first boot |
| A/B slots | After boot `phone-qcom-abslot` marks the slot it booted from as "successful" in the GPT attributes, so the bootloader does not count down its tries and fall back to the other slot |
| Debugging | A serial console and SSH over the USB cable (§7); the previous boot's kernel log kept in ramoops |
| Names | Yocto machine `oneplus-fajita` (`yocto/meta-phone/conf/machine/oneplus-fajita.conf`), kas file `yocto/kas/phone-fajita.yml`, SKU `oneplus-fajita-t9` (board `sku/boards/oneplus-fajita.toml`, [24](24-sku-framework.md)) |

Why no initramfs: when the boot image's own command line has `root=`, ABL takes it for a development image and appends neither the system partition as `root=` nor `skip_initramfs`, `ro init=/init` and the slot suffix (Qualcomm's ABL source, the copy SHIFT6mq publishes, also a Snapdragon 845; OnePlus does not publish its ABL, so the root file system still has an `/init` link and a `/` line in fstab, and boots should it append `ro init=/init` after all). UFS, GPT and ext4 are built into the kernel: mounting the root needs no module.

Why not U-Boot with systemd-boot: Nura now installs systemd-boot for sdm845 as well, for those who also flash U-Boot (UEFI); with the stock bootloader Nura flashes the same Android boot image. U-Boot in between is one more thing to flash and debug; the first real phone starts with the fewest.

Two kinds of test, one after the other, **both after the backup of §3.5** (before anything is written to the phone for the first time):

1. chroot: the phone runs Nura, and our root file system runs on its kernel (`tools/phone-chroot/kario-phone.sh --sku oneplus-fajita-t9`, [24](24-sku-framework.md) §6.2): the apps, the shell, the selftest, the compatibility run, the pixel selftest, memory and power, on Nura's kernel, panel, modem and sound.
2. The whole phone: the phone boots our own image (§5 to try it, §6 to flash it), and the kernel, firmware, boot and A/B slots are ours.

Back to OxygenOS: §9; in the worst case (no fastboot) the MSM download tool of §3.4, after which the backup of §3.5 writes the IMEI and the calibration back.

## 2. What to buy

- A OnePlus 6T; the China model A6010 is fine (the same hardware as the international one, only shipped with HydrogenOS). 8 GB or 6 GB of memory both work. The screen should be good: a broken or replaced screen may not be the original panel.
- Keyboard: a **Bluetooth keyboard**. A USB-C OTG keyboard does not work today: in the mainline device tree the 6T's USB is device only (`dr_mode = "peripheral"`) and the phone supplies no VBUS; host mode would lose the USB console of §7.
- A USB-C cable that carries data, and a Linux computer with `fastboot` and `adb` (android-tools), and a TWRP image for the 6T for the backup of §3.5.
- A SIM card. Most Chinese carriers need VoLTE for calls, and VoLTE is not done (§11.1); try SMS, data and 2G calls (where 2G remains) first.

## 3. Preparing the phone

### 3.1 The system: OxygenOS 11.1.2.2

Nura and the community say to update to the 6T's last OxygenOS (11.1.2.2) first: the mainline kernel uses the firmware in the phone's partitions (modem, DSPs and so on), and this version is the one tested most.

The China model ships with HydrogenOS. The A6010 hardware is the same, so OxygenOS can be flashed on it:

1. Unlock as in §3.3, or flash the international OxygenOS straight away with the MSM download tool of §3.4 (the community's international MSM packages for fajita are OxygenOS 9 or 10).
2. In OxygenOS, install the full 11.1.2.2 package under Settings › System › System updates › Local upgrade (downloaded from OnePlus or a community mirror).

These steps are the community's; Kario has not tried them (**not tried on a phone**).

### 3.2 The same firmware in both slots

The 6T has A/B slots, and the other slot may hold older firmware. Kario's boot image goes to both slots (§6), and when the bootloader falls back to the other slot it uses that slot's modem and other firmware, so both slots must match. Two ways:

- Install the same full 11.1.2.2 package twice through Local upgrade (each install goes to the slot not in use).
- Or, following LineageOS's install guide for fajita, `adb sideload` its copy-partitions package in its recovery, which copies slot a's partitions to slot b.

### 3.3 Unlocking the bootloader

1. Settings › About phone, tap "Build number" seven times to get the developer options; in them turn on "OEM unlocking" and "USB debugging".
2. `adb reboot bootloader` (or hold Volume up + Power with the phone off) to get into fastboot.
3. `fastboot oem unlock`, then choose the confirmation with the volume keys and press Power. **This erases all data on the phone.**
4. Reboot into the system, go through the setup wizard, then back to fastboot: `fastboot getvar unlocked` should say `yes` (or `fastboot oem device-info` shows `Device unlocked: true`).

### 3.4 When it is bricked: the MSM download tool

When a 6T is truly bricked (no fastboot), Qualcomm's EDL mode and OnePlus's MSM download tool (Windows, the community's fajita packages) restore it: with the phone off, hold Volume up and Volume down while plugging in the USB cable, the phone enters EDL (9008), and the tool writes every partition back to the factory state, relocking the bootloader too. Kario writes only `dtbo`, `boot` and `userdata` (§6), so it is seldom needed; but it is the last way back, so find the package for your model before you start. It wipes or replaces `persist` (the fingerprint's and the sensors' calibration) and the partitions holding the IMEI, and the factory package's are not this phone's: after using it, write them back from the backup of §3.5 with `fajita-flash.sh restore-efs`.

### 3.5 Backup: the partitions only this phone has

After unlocking and before anything is written to the phone for the first time (installing Nura counts), copy these to the computer: `persist` (the fingerprint's and the sensors' calibration; another phone's does not work), `modemst1`, `modemst2`, `fsg`, `fsc` (the IMEI and the modem's configuration: without them there is no IMEI and no calls), `boot` and `dtbo` of both slots (to go back to OxygenOS), and each LUN's GPT.

```
yocto/fajita-flash.sh backup twrp-fajita.img
```

`twrp-fajita.img` is a TWRP for the 6T that `fastboot boot` starts on OxygenOS 11 (twrp.me's fajita page, or the community's build on XDA). The script starts it with `fastboot boot`, so it runs from RAM and nothing is written to the phone; it copies each partition with `dd` through TWRP's adb and checks each against the phone's SHA-256; it writes `SHA256SUMS` and `phone.txt` (the serial number, the time, whether `boot` is OxygenOS's); and it ends in fastboot mode. The default place is `~/kario-phone-backup/oneplus-fajita-<serial>/`, outside the repository; **copy it to another disk or a cloud drive too**: it is this phone's only copy of its IMEI and calibration. With a backup already there the script does nothing, so a phone changed later never replaces it.

`test`, `flash` and `restore` first check this backup (the same serial number, each file's SHA-256) and refuse, writing nothing to the phone, when it does not check out.

Tested against a simulated phone (`yocto/fajita-flash-test.py`, in `make test` and CI), **not tried on a phone**.

## 4. Building

```
make yocto-build WORK=$HOME/yocto/fajita-work KASFILE=yocto/kas/phone-fajita.yml SKU=oneplus-fajita-t9
```

- The SKU `oneplus-fajita-t9` is this phone as a 9-key bar phone ([24](24-sku-framework.md) §7.4): it generates `phone-sku`'s `sku.json` (the shell draws for the 1080×2340 screen at 4.5 times, 80 rows left dark at the top and the bottom for the notch and the rounded corners) and the kas overlay (machine `oneplus-fajita`; a whole phone uses the kernel's own device tree, no overlay). A whole phone has no firmware package from `make sku`: the build's images are flashed as they are (§5, §6).
- The kas file keeps `phone-qemuarm64.yml`'s distribution and packages, drops the A/B system slots, dm-verity and the wic disk (not on this phone yet), adds meta-qcom. The upstream layers' commits are pinned in `phone-qemuarm64.yml`, the same as the QEMU images' (later wrynose makes udev depend on iproute2-devlink, which ends up needing bash, GPL-3.0). Parallelism 16 (`BB_NUMBER_THREADS`, `PARALLEL_MAKE`).
- `PHONE_BRINGUP = "0"` (local.conf or a kas overlay) builds the image without the debugging aids of §7.

The results are in `$WORK/build/tmp/deploy/images/oneplus-fajita/`:

| File | Written to | Size (the 2026-11-18 build) |
|------|--------|------|
| `boot-oneplus-fajita.img` | `boot_a`, `boot_b` | 15 892 480 B (15.2 MiB) |
| `phone-image-oneplus-fajita.rootfs.ext4.sparse` | `userdata` (a run of zeros travels as 4 bytes) | 464 700 388 B (443 MiB) |
| `phone-image-oneplus-fajita.rootfs.ext4` | The same file system, raw | 717 559 808 B (684 MiB) |

AOSP's mkbootimg makes the boot image (`mkbootimg-native`); `yocto/meta-phone/files/phone-img2simg.py` makes the sparse image: blocks of zeros, or of one word repeated, become fill chunks, never "don't care", so the partition's old data cannot show through where the file system expects zeros. Checking the boot image: `unpack_bootimg --boot_img boot-oneplus-fajita.img` (the same AOSP tools, in the build under `tmp/sysroots-components/x86_64/mkbootimg-native/usr/bin/`) should list header version 0, page size 4096, the offsets above and the command line.

## 5. Trying it first: fastboot boot

```
yocto/fajita-flash.sh -n test     # only print the fastboot commands it would run
yocto/fajita-flash.sh test
```

It needs the backup of §3.5 first. `test` erases `dtbo_a` and `dtbo_b`, then boots the boot image once with `fastboot boot`: neither `boot` nor `userdata` is written, and after a reboot the bootloader starts from its own `boot` partition again. The dtbo has to go: otherwise ABL applies Android's device tree overlays to the mainline device tree and the phone stays in fastboot (sdm845-mainline's install notes). With the dtbo erased, OxygenOS does not start either until the dtbo is back (§9).

At this point userdata still holds Android's data and Kario's root file system cannot be mounted: what you see is the kernel's output on the screen (`console=tty0`), the devices appearing on USB, and the kernel stopping where mounting the root fails. Check that the screen lights up and the kernel runs, then flash. The script first checks that `fastboot getvar product` says `sdm845` and refuses anything else; the bootloader reports the SoC, and a OnePlus 6 says `sdm845` too, so before writing it also asks you to confirm it is a 6T (`-y` skips the question).

## 6. Flashing

```
yocto/fajita-flash.sh flash
```

It needs the backup of §3.5 first. `boot` and `userdata` must come from the same build: Nura's configuration forces lockdown (integrity), so the kernel loads only signed modules, signed with the key that build generated.

In order: erase `dtbo_a` and `dtbo_b`; write Kario's boot image to both `boot_a` and `boot_b` (should the bootloader fall back to the other slot, it is still Kario: a 6T user saw a slot never marked "successful" fall back to OxygenOS on a cold boot); write the sparse image with `fastboot -S 512M flash userdata` (the bootloader takes at most 512 MiB at a time, fastboot sends it in chunks); reboot. `-s` picks the phone, `-d` another image directory, `-n` only prints.

At the first boot systemd grows the ext4 to the whole of userdata (`x-systemd.growfs`), `phone-qcom-abslot` marks the slot, and then Kario's UI comes up.

## 7. The debugging console

There is no serial port (the 6T's debug UART is only on test points on the board), so debugging goes over the USB cable. With `PHONE_BRINGUP = "1"` (the machine's default) the image has `phone-bringup`:

- Early in boot it binds a USB device (`kario-bringup`, 1d6b:0104) with two functions: a CDC-ACM serial port (`ttyGS0` on the phone, root logged in automatically) and a CDC-NCM network interface (`dbg0` on the phone, address 172.16.42.1, handing the computer 172.16.42.11–20). It takes the USB controller first, so the shell's own USB sharing does not work meanwhile; it is not called `usb0`, so the firewall the shell sets on its USB link leaves it alone.
- A wake lock keeps the phone out of suspend, so the USB console does not drop.
- Kernel logs: the device tree has a ramoops region (0xac300000, 4 MiB), so the previous boot's console and crash logs survive a reboot; systemd-pstore moves them to `/var/lib/systemd/pstore` at every boot; the journal is kept on userdata too.

```
picocom /dev/ttyACM0                 # serial: ttyACM0 appears on the computer (screen /dev/ttyACM0 works too)
ssh root@172.16.42.1                 # network: DHCP on the computer's new interface; otherwise ip addr add 172.16.42.2/24 dev <interface>
journalctl -b -1                     # the previous boot's log
bringup-log                          # the previous kernel log pstore kept (-a: all of it)
phone-abslot status                  # the state of both slots
systemctl stop phone-bringup-awake   # release the wake lock, to try suspend
```

Under lockdown (integrity) debugfs and unsigned modules are refused: looking into `/sys/kernel/debug` takes a kernel built without `CONFIG_LOCK_DOWN_KERNEL_FORCE_INTEGRITY`.

root has no password, so whoever has the cable is root: this is a development image, for debugging only. An image with `PHONE_BRINGUP = "0"` has none of this (dropbear and the empty password are `phone-qemuarm64.yml`'s settings, in every development image).

## 8. What to check first

| Item | How to look | On Nura |
|----|--------|---------|
| Screen | First the kernel's and systemd's text, then Kario's UI; `dmesg \| grep -i -e dsi -e dpu` | Works |
| Keys | Power key, volume keys: `cat /proc/bus/input/devices` (pm8941_pwrkey, gpio-keys), the UI responds | Works |
| Slot | `phone-abslot status`: this boot's slot `successful` | — |
| Wi-Fi | `wlan0` in `ip link`; join a network in Settings › Wi-Fi | Works |
| Bluetooth | `journalctl -u phone-qcom-btaddr` shows the address given; pair the keyboard in Settings › Bluetooth | Works |
| Modem | `cat /sys/class/remoteproc/*/state` (running); `systemctl status rmtfs tqftpserv phone-qcom-uim`; `mmcli -L` | Works |
| SIM | `mmcli -m 0`; `qmicli -d qrtr://0 --uim-get-card-status` | Works |
| SMS | Send one, receive one (Messages); `mmcli -m 0 --messaging-list-sms` | Works |
| Data | Turn mobile data on in Settings; the bearer in `mmcli -m 0`, the rmnet interface in `ip addr` | Works |
| Calls | Call out, answer; is there sound in the earpiece, does the other party hear you, speaker, mute (the list at the end of [20](20-audio.md) §10); `cat /run/phone/audiod-ucm.json` is the current UCM set-up, look for `call routing` in `journalctl -u phone-audiod` | Works (Nura switches the audio routing with callaudiod) |
| Sound | `aplay -l`; `systemctl status phone-audiod` (at its first start after boot it runs the UCM profile's boot sequence through alsaucm and sets up HiFi and the speaker, playing to `hw:0,2`); key tones and the ringtone come out of the speaker; `aplay -D plughw:0,2 <wav file>` | Works |
| Battery | `cat /sys/class/power_supply/*/capacity`; `status` while charging | Works (no Dash fast charging) |
| Suspend | `systemctl stop phone-bringup-awake`, wait for it to sleep with the screen off, wake it with the power key; `cat /sys/power/suspend_stats/success` | Works |

The "On Nura" column is what Nura's device page says, not Kario's result; for Kario all of it is **not tried on a phone**.

## 9. Returning to Android

Kario writes only `dtbo_a/b`, `boot_a/b` and `userdata`; `system`, `vendor` and the firmware partitions are still OxygenOS's. The ways back, lightest first:

- `yocto/fajita-flash.sh restore`: writes both slots' `boot` and `dtbo` back from the backup of §3.5, clears userdata with `fastboot -w` and reboots into OxygenOS (its first start after the wipe takes a few minutes). When the backup was taken after Nura or Kario was installed (`stock-boot=no` in `phone.txt`), take `boot.img` and `dtbo.img` from the full OxygenOS 11.1.2.2 package instead (it holds a `payload.bin`; a tool such as payload-dumper unpacks it) and name their directory with `--stock`.
- No IMEI, or no fingerprint can be enrolled (the MSM tool or something else touched those partitions): `yocto/fajita-flash.sh restore-efs twrp-fajita.img` writes the backup's `persist`, `modemst1`, `modemst2`, `fsg` and `fsc` back through TWRP, reading each back to check it.
- No fastboot: the MSM download tool (§3.4), then `restore-efs`.

Not to do: **do not lock the bootloader again** (`fastboot oem lock` / `flashing lock`) while the phone is not on its factory system: that bricks it for good; do not `fastboot erase` or flash `modem`, `persist`, `modemst*`, `fsg` or `fsc` (the script touches only `dtbo`, `boot` and `userdata`, and `restore-efs` writes back only a backup that checks out).

`restore` and `restore-efs` are tested against a simulated phone (`yocto/fajita-flash-test.py`), **not tried on a phone**.

## 10. Components and licences

| Part | Source (pinned version) | Licence |
|------|------|------|
| Kernel `linux-sdm845` | gitlab.com/sdm845-mainline/linux tag `sdm845-7.1-rc1-r0` (85f1df2a); `config-postmarketos-qcom-sdm845.aarch64` from Nura's pmaports 02c676bc as the base configuration (downloaded at build time); Kario's `phone-*.cfg` | GPL-2.0-only |
| Firmware `firmware-oneplus-sdm845` | gitlab.com/sdm845-mainline/firmware-oneplus-sdm845 (3e31a0c3, the files Nura's package 19 installs), plus linux-firmware's Adreno 630, WCN3990 and Bluetooth files | `CLOSED`: downloaded at build time, never in git; an image with it is for the owner's own testing, not to be published or given to others |
| Sound configuration `alsa-ucm-conf-sdm845` | The OnePlus 6/6T files of gitlab.com/sdm845-mainline/alsa-ucm-conf (1b8d290e), installed beside alsa-ucm-conf; the 6T's `Voice Call` gets Kario's call speaker `Speaker1` appended (`fajita-voicecall-speaker.conf`, [20](20-audio.md) §10) | BSD-3-Clause (Kario's added device Apache-2.0) |
| Call audio `q6voiced` 0.3.1 | gitlab.nura.eco/nura/q6voiced (21061751) | MIT |
| `qrtr` 1.2, `rmtfs` 1.3, `tqftpserv` 1.2 | meta-qcom wrynose (c791f2b7, with the meta-lts-mixins wrynose/linux-firmware d0fd372d it requires) | BSD-3-Clause |
| ModemManager 1.24.2, libqmi 1.38 (QRTR on) | meta-oe | GPL-2.0+ / LGPL-2.1+ |
| `mkbootimg-native` (on the build machine only) | android.googlesource.com/platform/system/tools/mkbootimg (d2bb0af5) | Apache-2.0 |
| `phone-qcom`: `phone-abslot`, `phone-btaddr`, `uim-select`, phone-audiod's UCM flags (`PHONE_AUDIO_*`: the verbs, devices and PCMs for media and calls) | Kario (`yocto/meta-phone/recipes-bsp/phone-qcom`) | Apache-2.0 |
| `phone-bringup` | Kario | Apache-2.0 |

Not used: qbootctl (GPL-3.0; Kario's image takes no GPLv3, `phone-abslot` stands in), hexagonrpcd (GPL-3.0+, hence no sensors), msm-modem's UIM selection (GPL-3.0+, `uim-select` stands in), bootmac (GPL-3.0+, `phone-btaddr` stands in), callaudiod (GPL-3.0+, phone-audiod's UCM call routing stands in). Kario's stand-ins were written anew from what those tools have to do, without their code.

`scripts/license-check.py` covers the platform's Go modules and the renderers' crates, not what the image installs; the image's licence boundary is the kas file's `INCOMPATIBLE_LICENSE` (no GPLv3), which does not stop `CLOSED` firmware, so keeping such an image unpublished is up to you (previous row).

## 11. Not done yet

- Call audio: written, **not tried on a phone**. phone-audiod switches the sound card's UCM profile ([20](20-audio.md) §10): its first start after boot runs the profile's boot sequence and sets up `HiFi` + `Speaker`; while telephonyd has a call (dialing, ringing at the other end, connected, held) it moves to `Voice Call` + `Earpiece` + `Mic`, the mix playing to the earpiece's `hw:0,0`; with the speaker on in the call screen it uses `Speaker1` instead (missing from the profile, added by Kario, playing to `hw:0,2`); muting leaves `Mic` out; when the last call ends it goes back to `HiFi`, and the next call starts on the earpiece; an audiod restarted mid-call carries on with the set-up it left. Still missing: headphones (the 6T has no 3.5 mm jack, mainline USB is device only so USB-C headsets cannot be attached, and audiod does not know a jack's state anyway), Bluetooth calls, echo cancellation for the speaker (mainline q6voice has no ACDB calibration as on Android). The earpiece, speaker, mute and the boot sequence's volumes all have to be listened to on the phone: see the list at the end of 20 §10.
- VoLTE: not done; the research is in §11.1.
- Sensors (hexagonrpcd is GPL-3.0), camera (vivid stands in), the in-screen fingerprint reader, NFC, USB host, Dash fast charging, the alert slider, the vibration motor.
- A/B system slots, dm-verity and OTA ([25](25-system-update.md)) are not on this phone yet: userdata holds a single root file system.
- phone-paint draws on the CPU (dumb buffers); the Adreno 630 is not used.

### 11.1 VoLTE (research, no code)

Most Chinese carriers need VoLTE for calls (§2), which makes it the biggest gap in using the 6T as a phone. What follows comes from reading sources and public discussions on 2026-11-18, all **not tried on a phone**. On the Snapdragon 845 the IMS stack (SIP, RTP, voice codecs) runs in the modem firmware; what Linux has to do is pick the right carrier configuration for the modem and connect the IMS data network for it:

| Part | What it does | State |
|------|--------|------|
| Carrier configuration (MBN) | The modem turns IMS and VoLTE on, with their parameters, per carrier. The files are in the `modem` partition (vfat) under `image/modem_pr/mcfg/configs/mcfg_sw/…`; on Android the vendor's service picks one by SIM card and loads it into the modem over QMI PDC | Kario's firmware package, like Nura's, does not install `modem_pr`; whether the modem reads one itself through tqftpserv, and which, is **unknown** |
| IMS data (QMI service 770) | The modem asks the application processor to open a data connection for IMS (the APN is usually `ims`) | Nura uses 81voltd: on the request it opens a bearer through ModemManager's `CreateBearer` (APN, IP family, 3GPP profile id) |
| ModemManager | Open a second bearer besides the internet one (multiplexed rmnet), with a profile id | The image has 1.24.2, which has `profile-id` and multiplexing; a second bearer over QRTR is **not verified** |
| telephonyd | Not take the IMS bearer for mobile data | It does today, see below |
| Call audio | VoLTE calls also use the DSP's voice path, so q6voiced and audiod's UCM set-ups apply as they are | Whether an IMS call is an ordinary Call object in ModemManager is **unknown** |

**81voltd**: version 1.2.0 of gitlab.nura.eco/modem/81voltd, packaged in Alpine community; Nura's `soc-qcom-modem` depends on it. The licence is **GPL-2.0-or-later** (LICENSE holds the GPLv2 text, and every source file is marked `GPL-2.0-or-later`), not GPL-3.0, so it may go into the image. It is about two thousand lines of C depending on libqmi (`qmi-glib`), ModemManager's `mm-glib` and libqrtr, built with meson; all three are in Yocto (meta-oe, meta-qcom), and a recipe would be about twenty lines. The problems it states itself: stopping it does not disconnect the bearers it opened; ModemManager's errors are not passed on to the modem. The pmaports merge request that added it calls it a stopgap, to be merged into ModemManager in the long run.

**Where it clashes with telephonyd** (so it is more than adding a package):

- telephonyd takes any connected bearer for "mobile data connected": once the IMS bearer is up it no longer opens a bearer for the internet, and there is no internet with the data switch on.
- Turning the data switch off (and airplane mode) makes it call `Simple.Disconnect("/")`, which disconnects **every** bearer, the IMS one included, and VoLTE goes with it.
- `status`'s `data_ifaces` would include the IMS bearer's interface, and the shell's firewall and routing would take it for an internet interface.

What has to change: recognise the IMS bearer by its APN (`ims`) or the bearer's `apn-type`, keep the data logic away from it, and disconnect only the bearer telephonyd opened.

**How to try it once the phone is here** (in order, each step can be undone):

1. Make the backup of §3.5 first: a wrong PDC configuration makes the modem misbehave (one of Nura's developers ran into that), and the backup of `modemst1/2` and `fsg` is the way back.
2. In OxygenOS, check that the SIM card does VoLTE on Android; copy `image/modem_pr` out of the `modem` partition and look for the Chinese carriers' directories (`…/generic/china/…`).
3. On Nura (test one, §13.4) install 81voltd: `qmicli -d qrtr://0 --pdc-list-configs=software` lists the configurations in the modem and which one is active; if needed load the matching `mcfg_sw.mbn` with `--pdc-load-config` and activate it with `--pdc-activate-config`, then place a VoLTE call. What the community reports: on Nura the 6T and the Pixel 3a make VoLTE calls with 81voltd, with some carriers (Telstra in Australia) failing; someone switching configurations with qmicli on a 6T running LineageOS got a German card working with "Volte_OEM_Lab".
4. Once that works, do Kario's part: a recipe for 81voltd, telephonyd recognising the IMS bearer, and whether Kario should pick the MBN by the SIM's MCC/MNC.

**Effort**: the 81voltd recipe and telephonyd telling the bearers apart, a day or two; picking an MBN and loading it into the modem (with qmicli, or sending QMI PDC ourselves), a few days; hardest to estimate is carrier compatibility, which can only be tried carrier by carrier with real cards.

**Risks and unknowns** (none can be answered without the phone):

- Whether OxygenOS 11.1.2.2's `modem_pr` has VoLTE configurations for China Mobile, China Unicom and China Telecom, and whether the modem picks one by SIM card itself.
- Whether loading a PDC configuration can mess up the EFS (configurations are kept in the modem's persistent storage).
- Whether ModemManager 1.24 can open a second bearer over QRTR, and whether the profile id 81voltd passes is right.
- Whether an IMS call is an ordinary Call object in ModemManager (telephonyd, q6voiced and audiod all rely on that), and what happens when a call moves to 2G or 3G (SRVCC).
- How emergency calls and SMS over IMS go.

## 12. Sources

- Nura's pmaports (gitlab.nura.eco/nura/pmaports, read at 02c676bc, 2026-11-17): `device/community/device-oneplus-fajita` (deviceinfo: the boot image format, offsets, command line, flashing method), `linux-postmarketos-qcom-sdm845` (the tag and the configuration), `firmware-oneplus-sdm845` (the file list), `alsa-ucm-conf-sdm845`, `soc-qcom` (which modules and services go together).
- sdm845-mainline (gitlab.com/sdm845-mainline): the kernel, firmware and UCM, and pmtools' install notes (why the dtbo is erased). For call routing: alsa-ucm-conf 1b8d290e's `OnePlus/fajita`, `Xiaomi/beryllium` and commit history (why `HiFi` has q6voice routes too, why `Mic` became `Mic1`), and `q6voice-dai.c` of the kernel tag `sdm845-7.1-rc1-r0` (which voice mixer controls exist, and that there is no mute control).
- Calls: q6voiced 0.3.1's source (which ModemManager call states open the voice path); alsa-utils' alsaucm and alsa-lib's `src/ucm/main.c` (one state per run, a new verb tears the old devices down first, disabling a device that is not on does nothing); the OnePlus 6T having no headphone jack: the commit message of the mainline device tree (Caleb Connolly, 2020).
- VoLTE: 81voltd 1.2.0's source and Alpine's APKBUILD; `soc-qcom` and `firmware-oneplus-sdm845` of Nura's pmaports f2795e91 (2026-11-18); pmaports merge requests !5387 (81voltd) and !4674 (the `modem_pr` files) and issue #1878 (VoLTE on the OnePlus 6); openimsd.com's "Activating Voice over LTE on Android" (switching PDC configurations on a 6T with qmicli).
- linux-msm's (github.com/linux-msm) qrtr, rmtfs and tqftpserv, and the recipes in Qualcomm's meta-qcom.
- How Qualcomm's ABL builds the command line: SHIFTPHONES/android_bootable_bootloader_edk2 (SHIFT6mq, Snapdragon 845).
- `fastboot getvar product` saying `sdm845` on a 6T: a 6T user's `getvar all` posted on the XDA forums; a slot never marked "successful" falling back to OxygenOS: the write-up of a user who put Arch on a 6T.

## 13. When the phone arrives: step by step

On your own Linux computer, with no need to log in to the build machine. Stop at any step whose output differs from what is said here, and send me the output. The whole of this is **not tried on a phone**.

### 13.1 The computer

1. Install `adb`, `fastboot` (android-tools), `python3`, `zstd`, `ssh` and `git`.
2. Get the code: `git clone https://github.com/kario-os/kario && cd kario` (every command below runs in this directory).
3. Copy three images from the build machine (the firmware is `CLOSED`: for your own testing only, pass it on to no one):

   ```
   mkdir -p ~/kario-images
   B=<build machine>
   rsync -aL --progress \
     $B:yocto/fajita-work/build/tmp/deploy/images/oneplus-fajita/boot-oneplus-fajita.img \
     $B:yocto/fajita-work/build/tmp/deploy/images/oneplus-fajita/phone-image-oneplus-fajita.rootfs.ext4.sparse \
     $B:yocto/kario-work/build/tmp/deploy/images/qemuarm64/phone-image-qemuarm64.rootfs.ext4.zst \
     ~/kario-images/
   ```

4. Download the 6T's TWRP (§3.5, saved as `~/kario-images/twrp-fajita.img`) and the MSM unbrick package (§3.4, used on Windows; keep it for now).
5. The key the chroot kit reaches the phone with: `ssh-keygen -t ed25519 -f ~/.ssh/kario-board-test -N ''`.

### 13.2 The phone: system and unlocking

6. A China model gets OxygenOS first, then the local upgrade to 11.1.2.2, installed into both slots (§3.1, §3.2).
7. Unlock the bootloader (§3.3, which wipes the phone).

### 13.3 Backup: before anything is written to the phone

8. With the phone in fastboot mode (off, then Volume up + Power held) and the USB cable in:

   ```
   yocto/fajita-flash.sh backup ~/kario-images/twrp-fajita.img
   ```

   It is done when the last line says `backup in .../kario-phone-backup/oneplus-fajita-<serial>`; the phone is back in fastboot mode.
9. Copy the whole of `~/kario-phone-backup` to another disk or a cloud drive too.

### 13.4 Test one: the chroot on Nura

10. Flash Nura from its install page ([24](24-sku-framework.md) §6.2, step 3: the console interface, with SSH; the OnePlus needs `fastboot erase dtbo`).
11. Once it is up, with the USB cable to the computer, the phone is `172.16.42.1`: add `~/.ssh/kario-board-test.pub` to the phone root's `authorized_keys` (or use the default user and add `--become doas` to each command below).
12. Pair the Bluetooth keyboard in Nura with `bluetoothctl`.
13. The automatic tests (some hours; the backup of §13.3 is checked first, and without it nothing is pushed to the phone):

    ```
    K="tools/phone-chroot/kario-phone.sh --sku oneplus-fajita-t9 --image $HOME/kario-images/phone-image-qemuarm64.rootfs.ext4.zst"
    $K push setup selftest pixel collect
    ```

14. By hand: `$K shell-start`, Kario on the screen, then call, text and join Wi-Fi with the Bluetooth keyboard; `$K mem`; `$K shell-stop`. Power: `$K power-start`, 30 minutes with the screen off, `$K power-stop`.
15. Send me the results: `tar -czf fajita-chroot.tgz -C build/phone/oneplus-fajita-t9 results`.
16. Nura as it was: `$K teardown`.

### 13.5 Test two: the whole phone

17. With the phone in fastboot mode, try it once (neither `boot` nor `userdata` written, §5):

    ```
    yocto/fajita-flash.sh -d ~/kario-images test
    ```

    Look for the kernel's output on the screen, and for a USB serial port and network interface on the computer.
18. Flash it (§6): `yocto/fajita-flash.sh -d ~/kario-images flash`.
19. Check the first boot as in §8. Fetch the logs even with a dark screen (the USB console does not need it):

    ```
    ssh root@172.16.42.1 'bringup-log; journalctl -b --no-pager' > fajita-boot-1.log
    ```

    Send me `fajita-boot-1.log`.

### 13.6 Going back

20. Back to OxygenOS: with the phone in fastboot mode, `yocto/fajita-flash.sh restore` (§9; a backup taken before Nura was installed is used as it is).
21. No fastboot: the MSM tool (§3.4), then `yocto/fajita-flash.sh restore-efs ~/kario-images/twrp-fajita.img` to write the IMEI and the calibration back.
