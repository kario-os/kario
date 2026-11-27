> English translation of [the Chinese original](../43-phone-partitions.md). The Chinese text is authoritative; keep both in sync.

# A phone's partitions: what Kario writes, what it reads, what it never touches

> An Android phone has dozens of partitions. This document sorts them by what they do, settles what Kario does with each kind, then describes the two layouts on the OnePlus 6T (the userdata layout used so far and the new A/B layout), the partition table in the board file, what keeps the flash script from writing the wrong partition, and how other phones fit in.
> Status: the design is settled; what can be checked on a computer is implemented: the board file's partition table and its checks (`tools/sku.py`), the flash script flashing either layout from that table (sizes read before writing, never a partition of the phone's own or the firmware's, a dry run listing what it would write), `phone-abslot` and RAUC's backend `rauc-abl`, the initramfs mounting the root by slot; unit tests and tests against a simulated phone ✓. The A/B layout's images were built once on 2026-11-24 (in the build machine's bt-work, never booted), and all of it is **not tried on a phone**; what to check on the phone first is in [39](39-oneplus-6t.md) §15. The userdata layout stays the default; since 2026-11-24 its boot image carries phone-initramfs too, which mounts userdata by its partition name ([39](39-oneplus-6t.md) §1 says why), ✓ on QEMU (`make yocto-userdata-test`), **not tried on a phone**. Which of Android's newer partition features (AVB, dynamic partitions, Virtual A/B, dlkm partitions) Kario takes is decided in §7, none of it **implemented**.

## 0. Summary

- An Android phone's partitions fall into a few kinds by what they do (§1). Kario writes only its own roles: the boot image (`boot`), the root file system (`root`), the data (`data`) and the persistent area (`state`), and it erases Android's device tree overlays (`dtbo`); the verification metadata (`vbmeta`), `misc`, the firmware, Android's own partitions and the data only this phone has (the EFS, `persist`) are never written.
- Each board has a partition table in its board file (`[partitions]`: partition name → role, §2); the flash script, the initramfs, RAUC, the update bundles and the chroot kit take their names from it. Another phone writes its own names, and the code stays the same.
- The OnePlus 6T has two layouts (§3): the **userdata layout** (how it has been flashed so far, the default: all of userdata is the root file system) and the **A/B layout** (the root file system in `system_a` and `system_b`, read-only through dm-verity; the data on userdata; RAUC updates the slot not running, and ABL falls back to the other slot when it does not come up).
- The 6T's persistent area (factory reset requests, the anti-theft record) stays at `/var/lib/slots` on userdata (why: §3.4); a product board with its own partition table gets a small `state` partition, as the emulator's disk has.
- The flash script never writes the EFS, `persist`, the modem's or the boot chain's partitions, in any path and whatever the board file says; before writing it reads the size of every partition it writes and writes not one byte when an image does not fit; a dry run lists every partition it would write (§4).
- Android's newer partition features (§7): AVB yes, at product time (a vbmeta signed by Kario verifies the boot image, the root slot is chained to the descriptor in the boot image, rollback is guarded, the bootloader is locked with Kario's key); dynamic partitions (`super`) only to fit phones launched with Android 10 on; Virtual A/B later, for phones with little storage; `vendor_dlkm` and `system_dlkm` no. All **not implemented**.

## 1. The partitions of an Android phone

The partition names and device numbers checked on a 6T are in [39](39-oneplus-6t.md) §9.1; the names below are the usual ones of an A/B phone like the Snapdragon 845 ones, and the 6T's full list is to be read on the phone (§3.2). The "Kario" column is what it does on the 6T, "Role" is the role the board file gives it (§2).

| Partition (example) | What it does | Kario | Role |
|------|------|------|------|
| `boot_a`, `boot_b` | The Android boot image the bootloader loads: kernel, ramdisk, command line; on a system-as-root phone the ramdisk is recovery's | Writes Kario's boot image, with phone-initramfs in either layout: the userdata layout's ramdisk names the layout, the A/B layout's carries the root's signed verity descriptor (§3.3) | `boot` |
| `dtbo_a`, `dtbo_b` | Android's device tree overlays, which ABL lays over the kernel's device tree | Erased: otherwise ABL lays them over the mainline device tree and the phone stays in fastboot ([39](39-oneplus-6t.md) §5) | `dtbo` |
| `vbmeta_a`, `vbmeta_b` | The metadata of Android Verified Boot (AVB): the hashes and hash tree descriptors of boot, system and vendor, signed | Not written. An unlocked bootloader boots on a mismatch (orange state; the 6T with Nura in `boot_a` boots that way); for phones that need verification turned off see §3.3; a product phone, the other way round, gets a vbmeta signed by Kario (§7.1, **not implemented**) | `vbmeta` |
| `system_a`, `system_b` | Android's system (system-as-root on the 6T) | Untouched in the userdata layout; the A/B layout writes Kario's root slots there: ext4 plus the dm-verity hash tree | `root` |
| `vendor_a`, `vendor_b`, `odm` | The vendor's HALs, libraries, part of the firmware | Neither written nor read: Kario brings its own firmware package ([39](39-oneplus-6t.md) §10); kept, so going back to OxygenOS does not have to write them | `android` |
| `userdata` | Android's user data (encrypted) | Userdata layout: all of it is the root file system; A/B layout: `/var/lib`, the data and the persistent area | `data` |
| `metadata` | The keys of metadata encryption, Android 10 on | Not on the 6T (**unverified**) | — |
| `misc` | The bootloader message (boot into recovery, the factory reset command); on some phones the A/B slot metadata too (Unisoc, §5) | Not written; the 6T keeps its slots in GPT attributes, not here | `misc` |
| `persist` | The sensors' and the fingerprint's calibration, factory data for Wi-Fi and more (ext4), only this phone has it | Never written; in the backup ([39](39-oneplus-6t.md) §3.5); a read-only copy once the calibration is used (**not implemented**) | `efs` |
| `modemst1`, `modemst2`, `fsg`, `fsc` | The modem's EFS: IMEI, RF calibration, carrier settings; `fsg` is the factory copy, `fsc` a marker | Kario's code never writes them: rmtfs hands them to the modem, which reads and writes its EFS through it; in the backup | `efs` |
| `oem_dycnvbk`, `oem_stanvbk` | OnePlus's copies of the modem's settings (by their names, **unverified**) | Never written (on the never list); not in the script's backup until the phone's partition map confirms them | `android` |
| `modem_a/b`, `dsp_a/b`, `bluetooth_a/b` | The firmware of the modem, the DSPs and Bluetooth (`modem` is vfat, with the carriers' MBNs in it too) | Neither written nor read: the firmware comes from Kario's firmware package; the MBNs VoLTE needs may later be taken from `modem` mounted read-only ([39](39-oneplus-6t.md) §11.1) | `firmware` |
| `xbl`, `abl`, `tz`, `hyp`, `aop`, `keymaster`, `cmnlib`, `devcfg`, `qupfw`, `storsec` | The SoC's boot chain and TrustZone: the first-stage bootloader (`xbl`, one copy on each of two UFS LUNs, switched by slot), ABL (fastboot lives in it), the secure world | Never written; Kario implements no bootloader ([27](27-usb-typec.md) §4.1) | `firmware` |
| `devinfo`, `frp`, `keystore`, `cdt`, `ddr`, `ssd`, `param`, `op2`, `reserve*` | The bootloader's unlock state, Android's factory reset protection, the board's data, the memory's training results, OnePlus's own | Never written (`devinfo`, `cdt` and `ddr` are on the never list); the rest are Android's and kept | `android` |
| `logdump`, `minidump`, `rawdump` | Crash dumps (written by the crash dump mode, `900e`) | Not written; could later be read to look into a crash ([41](41-design-gaps.md) §13, **not implemented**) | — |
| `super` | Dynamic partitions: `system`, `vendor` and `product` inside it (phones launched with Android 10 on) | Not on the 6T; phones with it: §5, §7.2 | — |
| `recovery`, `cache` | The recovery system and the cache of phones without A/B | Not on the 6T | — |
| `vendor_boot`, `init_boot` | GKI phones (launched with Android 12, 13 on) move the ramdisk and the vendor's modules out of `boot` | Not on the 6T | — |
| `vendor_dlkm`, `odm_dlkm`, `system_dlkm` | Kernel modules split by who builds them: the vendor's in `vendor_dlkm` and `odm_dlkm` (Android 11 on), GKI's in `system_dlkm` (Android 13 on) | Not on the 6T; Kario's modules all live in the root slot, not split (§7.4) | — |
| RPMB (a UFS LUN, not a partition) | Replay-protected secure storage for the TEE | Where a product's anti-theft record belongs (**depends on hardware**, [31](31-lock-and-biometrics.md) §11.4) | — |

## 2. The partition table in the board file

`[partitions]` in `sku/boards/<board>.toml`: `layout` is the default layout (`userdata` or `ab`), `slots` says who keeps the A/B slot (`abl`: the bootloader, in the GPT attributes of `boot_a` and `boot_b`, Qualcomm; `state`: phone-initramfs, in the state partition, the emulator's disk), and `part` has one entry per partition: its name and role, `ab = true` for a pair `_a`, `_b`, `size` for its size in bytes once confirmed (optional). Partitions the table does not list are never touched. The 6T's (an excerpt):

```toml
[partitions]
layout = "userdata"
slots = "abl"
part = [
    { name = "boot", ab = true, role = "boot" },
    { name = "dtbo", ab = true, role = "dtbo" },
    { name = "vbmeta", ab = true, role = "vbmeta" },
    { name = "system", ab = true, role = "root" },
    { name = "userdata", role = "data" },
    { name = "misc", role = "misc" },
    { name = "persist", role = "efs" },
    { name = "modemst1", role = "efs" },
    { name = "modem", ab = true, role = "firmware" },
    { name = "vendor", ab = true, role = "android" },
]
```

The roles, and what a flash does with them:

| Role | What it is | Userdata layout | A/B layout |
|------|------|------|------|
| `boot` | Kario's boot image | Writes `boot-<machine>.img` | Writes the A/B boot image (`phone-image-<machine>.rootfs.abboot`) |
| `dtbo` | Android's device tree overlays | Erased | Erased |
| `root` | Kario's root file system, a pair of slots | Untouched | Writes the root slot image (`phone-image-<machine>.rootfs.ext4.verity.sparse`) |
| `data` | Kario's data | Writes the root file system (`phone-image-<machine>.rootfs.ext4.sparse`) | Erased; the initramfs makes an ext4 at the first boot |
| `state` | Kario's persistent area (on boards with such a partition) | Untouched | Erased (`slots = "abl"`); the state partition's factory contents (`slots = "state"`) |
| `vbmeta`, `misc`, `firmware`, `android` | Android's | Untouched | Untouched |
| `efs` | Only this phone has it | Untouched; in the backup, written back only by `restore-efs` from a backup that checks out | The same |

`tools/sku.py check` (part of `make test`) checks:

- `slots`, `layout` and the roles are known ones; a name is letters, digits and underscores, and no name repeats once the slot suffixes are added; a pair is named without `_a`, `_b`; at most one each of `boot`, `dtbo`, `root`, `data`, `state`, `vbmeta`, `misc`; the root is a pair; `slots = "abl"` needs a `boot` pair, `slots = "state"` a `state`; the default layout is one these partitions allow (the userdata layout needs `boot` and `data`, the A/B layout a `root` pair and `data`).
- The never list (`NEVER`): the partition names under which Qualcomm, Unisoc and MediaTek phones keep one phone's own data, the bootloader's state, the board's data and the firmware (`persist`, `modemst1`, `modemst2`, `fsg`, `fsc`, `oem_dycnvbk`, `oem_stanvbk`, `devinfo`, `cdt`, `ddr`, `xbl`, `abl`, `tz`, `modem`, `dsp`, `bluetooth`…; Unisoc's `prodnv`, `miscdata`, `l_fixnv1/2`, `l_runtimenv1/2`, `l_modem`, `uboot`, `sml`, `trustos`…; MediaTek's `nvram`, `nvdata`, `protect1/2`, `proinfo`, `seccfg`, `preloader`, `lk`…), with or without a slot suffix: such a partition cannot have a role that is written or erased, and a partition with the role `efs` must be on the list. The flash script keeps the same list of its own, and a test checks the two are equal.
- The emulator's board (`slots = "state"`): its partitions are the A/B disk's (`phone-ab.wks.in`) and its root pair RAUC's slots (`system.conf`); a change to either side is refused.

Who takes what from the table:

| Who | How |
|------|------|
| `yocto/fajita-flash.sh` | `tools/sku.py partitions oneplus-fajita [layout]`: a first line with the layout and who keeps the slot, then a line per partition: name, role, what a flash does (write, erase, keep, efs), the image it writes |
| phone-initramfs | `/etc/phone/partitions.conf` (the `phone-sku-partitions` package, written by `sku.py gen`; in the initramfs of boards whose bootloader keeps the slot): `slots=`, `root=`, `data=`, `state=`, parsed, never run; without the file, the names of the emulator's disk |
| RAUC | `PHONE_SLOT_KEEPER`, `PHONE_ROOT_SLOTS` and `PHONE_BOOT_SLOTS` in the SKU's kas overlay: `rauc-conf` writes `system.conf` from them (§3.5) |
| The update bundles | The same: where the bootloader keeps the slot, both the full and the incremental bundles carry the boot slot |
| The chroot kit | `KARIO_PARTITIONS` in `phone.env`: `on-phone.sh partitions` (also a section of `facts`) lists every partition on the phone with its size in bytes and the role the board file gives it, then what the board file names and the phone does not have |

## 3. The OnePlus 6T

### 3.1 Two layouts

| Partition | Userdata layout (default) | A/B layout (**not tried on a phone**) |
|------|------|------|
| `boot_a`, `boot_b` | Kario's boot image: the kernel followed by the device tree, phone-initramfs (mounting userdata read-write by its partition name), `layout=userdata` in the ramdisk | The A/B boot image: the kernel followed by the device tree, phone-initramfs, the root's signed verity descriptor, `phone.layout=ab` on the command line |
| `dtbo_a`, `dtbo_b` | Erased | Erased |
| `system_a`, `system_b` | OxygenOS's, untouched | Kario's root slots: ext4 plus the dm-verity hash tree, read-only |
| `userdata` | The root file system, read-write, grown to the whole partition at the first boot | `/var/lib`: the data and the persistent area, an ext4 the initramfs makes at the first boot |
| `vbmeta`, `vendor`, `misc`, firmware, EFS, `persist` | Untouched | Untouched |
| System updates | None (flash userdata again) | RAUC writes the slot not running (the root slot with its boot slot), marked good after the health check; a new slot that does not come up is left by ABL once its tries are spent |

The same flash script does both: `fajita-flash.sh flash` is the userdata layout, `fajita-flash.sh --layout ab flash` the A/B layout, with the images of a build with `KASFILE=yocto/kas/phone-fajita-ab.yml` ([39](39-oneplus-6t.md) §4, §15). Until the A/B layout has been tried on the phone and every item of [39](39-oneplus-6t.md) §15 checked, the userdata layout stays the default.

### 3.2 How big the partitions are

The documents have no sizes of the 6T's partitions ([39](39-oneplus-6t.md) §9.1 records only names and device numbers), so no numbers here; read them on the phone:

```
fastboot getvar partition-size:system_a        # in fastboot: bytes in hex; fastboot getvar all lists all of them
blockdev --getsize64 /dev/disk/by-partlabel/system_a     # on a system with udev (Nura)
for d in /sys/class/block/sd*[0-9]; do echo "$(sed -n 's/^PARTNAME=//p' $d/uevent) ${d##*/} $(( $(cat $d/size) * 512 ))"; done   # any kernel; size counts 512 bytes
```

The chroot kit's `on-phone.sh partitions` (also a section of `facts`) is the third one, with the role the board file gives each partition, and what the board file names and the phone does not have. The flash script reads every partition it writes or erases itself before writing (the first one) and stops when an image does not fit.

What has to fit: the root slot image is ext4 plus its hash tree, the ext4 of the 2026-11-18 build 684 MiB and the hash tree about 0.8% more (a 32-byte hash per 4 KiB, plus a few upper levels); the emulator gives each slot 1 GiB to leave room. The A/B boot image is the 15.2 MiB boot image plus the initramfs (about 16 MB unpacked; packed **not measured**). `system_a` held OxygenOS's system and `boot_a` OxygenOS's boot image, so both should be big enough, **unverified**.

### 3.3 Booting: ABL picks the slot, the initramfs mounts the root

- ABL reads the GPT attributes of `boot_a` and `boot_b` (byte 6 of each entry's attributes: priority, active, tries left, successful, unbootable) and boots the active slot; a slot not marked successful spends a try at each boot (7 of them), and once they are spent it is marked unbootable and the other slot boots. This is read from Qualcomm's published ABL source; OnePlus's ABL is not published, **unverified**.
- The A/B boot image's command line has a `root=` too (`root=/dev/dm-0`, the very dm-verity device the initramfs opens): with it, OnePlus's ABL adds no `skip_initramfs`, `ro init=/init` or slot suffix, but appends its own `root=PARTUUID=<Android's system partition>` all the same (read on a 6T on 2026-11-24, [39](39-oneplus-6t.md) §1); the initramfs does not read `root=`. With no `androidboot.slot_suffix`, `phone-abslot booted` reads the slot active in the GPT (kario-bsp's `tests/abslot-test.py` tests it with the command line ABL gave that time).
- The userdata layout (since 2026-11-24): the boot image's ramdisk starts with a small cpio saying `layout=userdata` (`/etc/phone/layout`, phone-bootimg), followed by the same phone-initramfs. The initramfs looks the board's `data=` in `partitions.conf` (`userdata` on the 6T) up by partition name in sysfs (waiting up to 30 s), mounts the ext4 read-write without fsck and `switch_root`s to `/sbin/init`; the command line's `root=`, `init=`, `ro` and `skip_initramfs` are never used, and the whole command line and what it decided go to the kernel log. When the partition holds no ext4 or no `/sbin/init` (still Android's or Nura's data) it reboots after 10 s, never formatting it.
- The initramfs (`slot-init`; the board's `partitions.conf` says `slots=abl`) takes that slot's root partition (`system_<slot>`), checks the signature of the descriptor in the boot image and mounts the slot read-only through dm-verity. A root slot and its boot image are always written together (the flash script, RAUC's parent and child slots), so the descriptor is the one of the root file system built with this kernel, and the kernel loads only modules signed with that build directory's key ([39](39-oneplus-6t.md) §8.3): each slot agrees with itself.
- When it does not verify or mount: a reboot after 10 seconds, ABL spends a try of that slot and turns to the other once they are spent; the initramfs never changes the GPT attributes itself, so trying an A/B boot image from RAM with `fastboot boot` writes nothing to the phone either.
- The data: on a userdata without a file system (a flash erased it, or `fastboot -w`) it makes an ext4; an ext4 there it mounts; another file system (Android's f2fs) it leaves alone, and `/var/lib` stays read-only. It touches userdata only after the root slot verified: while the slots still hold Android's system, trying an A/B boot image with `fastboot boot` leaves userdata alone.
- Then it writes the slots' state in the emulator disk's format into `/var/lib/slots/boot` (`phone-abslot slots`: which slot boots next, which is good, how many tries are left, which one booted), and copies the slot's descriptor to `/var/lib/slots/rootfs_<slot>.verity`: the shell's system update page and the incremental bundles' hook read them there as before.
- `vbmeta`: Kario does not write it on an unlocked 6T; Android's vbmeta does not match Kario's boot, and ABL boots anyway (Nura's installation leaves it alone too). For a phone whose bootloader refuses, flash a vbmeta with verification turned off (`fastboot --disable-verity --disable-verification flash vbmeta_a <an empty vbmeta>`, made with avbtool): **not implemented**; when needed, the partition table gives `vbmeta` a role that is written. A product phone is the opposite: it gets a vbmeta signed by Kario and a locked bootloader (§7.1).

### 3.4 The persistent area: on userdata

Decided: the 6T's persistent area (`/var/lib/slots`: factory reset requests, the anti-theft record, RAUC's status, the incremental installs' checkpoints, the slots' state) is a directory on userdata, not a partition of its own. Why:

1. Anyone can unlock the 6T's bootloader, and any partition can be erased with `fastboot erase`: a partition of its own stops only `fastboot -w` and flashing userdata again, not a thief who means it ([31](31-lock-and-biometrics.md) §11.4's threat table; really stopping one takes a locked bootloader and RPMB, **depends on hardware**).
2. The 6T's partition table has no free partition Kario could take: either rewrite LUN 0's GPT and cut a piece out of userdata (one more risk for the only test phone, for the MSM tool and for going back to OxygenOS), or take one of Android's (`frp`, `misc`, `vendor_b`…, which going back to OxygenOS needs).
3. Booting no longer depends on it: the root slot's descriptor is in the boot image (§3.3), so a wiped userdata loses only the data and the persistent area's records; the phone still comes up and makes the data partition again.

What follows: flashing the 6T's userdata again or `fastboot -w` takes the anti-theft record with it (as now, [31](31-lock-and-biometrics.md) §11.5); Kario's own factory reset leaves `/var/lib/slots` alone (`phone-wipe.service` empties only `phone`, `iwd` and `bluetooth`). A board with its own partition table gives its table a `state` partition (16 MB, ext4, as the emulator's): the initramfs mounts it on `/var/lib/slots`, and a factory reset makes the whole data partition anew (`wipe_format`); the code has both paths.

### 3.5 System updates

The whole of [25](25-system-update.md), with two things changed: ABL keeps the slot, and a root slot takes its boot slot along.

| What RAUC asks | What `rauc-abl` (the `phone-qcom-rauc` package) does |
|------|------|
| `get-primary` | `slot` of `phone-abslot slots`: the active, bootable slot |
| `set-primary <slot>` | `phone-abslot set-active <slot>`: as ABL's `fastboot set_active`, the slot active, priority 3, 7 tries, not successful; the other inactive, priority 2, its successful mark kept |
| `get-state <slot>` | `phone-abslot get-state <slot>`: successful and bootable is good |
| `set-state <slot> good` | `phone-abslot mark-good <slot>` (after the health check, `phone-ota-good`) |
| `set-state <slot> bad` | `phone-abslot mark-unbootable <slot>` (before the slot is written) |
| `get-current` | The `booted` the initramfs wrote down: once `set-primary` moved the active flag, the GPT no longer tells which slot is running |

- After each change `rauc-abl` writes the slots' state into `/var/lib/slots/boot` again.
- `system.conf` is made from `system-abl.conf.in`: `rootfs.0` and `rootfs.1` are the board's root pair (`system_a`, `system_b` on the 6T), `boot.0` and `boot.1` the boot pair, each with its root slot as parent. Both the full and the incremental bundles carry the A/B boot image, which RAUC writes as it is (whole in an incremental bundle too; its size **not measured**).
- In the userdata layout `phone-qcom-abslot.service` marks the slot successful right at boot (the slot active in the GPT: ABL gives no slot suffix, §3.3; it waits for udev to make `/dev/disk/by-partlabel/boot_a` first); in the A/B layout it does not start (`phone.layout=ab`), and the health check (`phone-ota-good`) marks it: only so does ABL leave a new slot that does not come up.
- Unlike the emulator's: the tries are ABL's 7 (the emulator's 3); a slot that fails verification cannot be swapped for the other right away by the initramfs (another slot means another boot image, which is ABL's business), so it reboots and lets ABL spend a try.

### 3.6 What stays Android's

- The EFS (`modemst1`, `modemst2`, `fsg`, `fsc`): rmtfs hands them to the modem by name, and the modem reads and writes its EFS through it; neither Kario's own code nor the flash script writes them. tqftpserv hands firmware files to the modem and the DSPs, from Kario's firmware package; it does not read the `modem` partition.
- `persist`: the calibration. Kario does not read it yet (sensors and the fingerprint are not done, [39](39-oneplus-6t.md) §11); later mounted read-only and copied ([41](41-design-gaps.md) §15, item 3).
- The firmware partitions, `vendor`, `vbmeta`, `misc`, `frp`, `devinfo`: Android's, kept; going back to OxygenOS writes back only `boot` and `dtbo`, and `system` too after the A/B layout ([39](39-oneplus-6t.md) §9).

## 4. What the flash script does not write

- It writes and erases only the partitions whose role the partition table writes (`boot`, `root`, `data`) or erases (`dtbo`; `userdata` in the A/B layout). The script's fastboot wrapper (`fb`) checks every `flash` and `erase` again and refuses anything the table does not let it change or that is on the never list; `fastboot -w` only in `restore`.
- The never list has a copy of its own in the script (`NEVER`, the same as `tools/sku.py`'s; a test compares them): when a partition table gives a partition like `persist`, `modemst1`, `fsg` or `modem_a` a role that is written or erased, the script refuses before writing anything, a dry run too. `restore-efs` writes only partitions whose role in the table is `efs` and that are on the list, and only from a backup that checks out.
- Before writing it reads the size of every partition it writes or erases (`fastboot getvar partition-size:<name>`): a partition the phone does not have, or an image that does not fit (a sparse image by its unpacked size), and not one byte is written.
- A dry run (`-n`) needs no phone: it first lists each partition to be written, the file, its size and what it is checked against, the partitions erased, untouched and of the phone's own, then each fastboot command.
- After flashing either layout, before `test` boots and after `restore` has written, `fastboot --set-active=<the current slot>`: what ABL counted against the slot goes (on that 6T on 2026-11-24, once a slot that kept failing was marked unbootable, even `fastboot boot` answered `Load Error`), the slot counts its 7 tries again and loses its "successful" mark, and ABL turns to the other slot when it does not come up; the current slot, so that the firmware's slot does not change.
- A flash notes the layout in the backup directory (`kario.txt`, not in `SHA256SUMS`): from it `restore` knows whether to write `system` back; `restore` after the A/B layout wants `--stock` with OxygenOS's `system.img`.

## 5. Other phones

| Phone | Boot | Who keeps the slot | Where the root slots go | Status |
|------|------|------|------|------|
| OnePlus 6T (Snapdragon 845, launched with Android 9) | ABL, Android boot image | ABL (GPT attributes) | `system_a`, `system_b` | Written, **not tried on a phone** |
| OnePlus 6 (Snapdragon 845) | The same | The same | The same | The board has no Yocto machine and no partition table yet, **not implemented** |
| Snapdragon 662 / 460 phones (launched with Android 10, 11) | ABL | ABL | No physical `system_a`: `system` is inside `super` (dynamic partitions). Either two slots at offsets in `super` (the initramfs takes them out with dm-linear or a loop device with an offset), or `super`'s metadata rewritten (§7.2) | **Not implemented**: `tools/sku.py` takes only a pair of physical partitions now |
| Unisoc T610 (Android 10, 11) | SPL and U-Boot, Android boot image | U-Boot: AOSP's `bootloader_control` in `misc` | The same (`super`) | **Not implemented**: `slots` takes only `abl` and `state`; Unisoc's NV partitions (`prodnv`, `l_fixnv1`…) are on the never list already |
| KaiOS phones ([24](24-sku-framework.md) §7) | LK or lk2nd | No A/B | Divided again inside userdata ([24](24-sku-framework.md) §7.3) | **Not implemented** |
| Our own product board | The vendor's bootloader | The vendor's (ABL, U-Boot) or the initramfs (`state`) | Our choice: `rootfs_a/b` or `system_a/b`, plus `state` and `data`; at product time `vbmeta_a/b` signed by Kario (§7.1) | The emulator's disk is like this ✓ (QEMU); the vbmeta **not implemented** |

## 6. Verification

| Test | What it tests |
|------|------|
| `tools/sku_test.py` (`PartitionsTest`) | What the 6T writes and erases in either layout; an example breaking each rule of the partition table (a partition on the never list as the data, the root, erased; `efs` not on the list; a name twice; a pair with a suffix…); the values in kas, `partitions.conf` and `phone.env`; the emulator's board agreeing with wic's and RAUC's names; `system-abl.conf.in` filled with the 6T's names; the script's never list the same as `sku.py`'s |
| `yocto/fajita-flash-test.py` | A simulated 6T (a fake fastboot reports partition sizes): the userdata layout as before; the A/B layout writing and erasing exactly its partitions, then `--set-active`; every partition it changes sized before the first write; a slot too small, a partition missing, a userdata too small, all refused before writing; a partition table listing `persist`, `modem_a` or `fsg` to be written or erased, refused (in a dry run too); a dry run listing what it would write; a layout and images that do not match, refused; `flash`, `test` and `restore` making the current slot active in both layouts, so a slot ABL marked unbootable, refusing `fastboot boot` (`Load Error`), comes up again; a boot image without the initramfs (before 2026-11-24), or whose ramdisk names no userdata layout or carries no A/B root descriptor, refused; every image downloaded in pieces of `-S 64M` (`--chunk`), so a simulated phone dropping any download over 100M takes them, while `--chunk 512M` fails; `restore` from the A/B layout wanting OxygenOS's `system.img`; a backup without `boot` or `dtbo` warned about, `restore` then wanting `--stock`, an erased `dtbo` never written back |
| kario-bsp's `tests/abslot-test.py` | `phone-abslot`'s `booted`, `slots`, `set-active`, `mark-unbootable`, `get-state` and `mark-good <slot>` on GPT disk images with 512 and 4096-byte sectors: both GPTs changed, CRCs valid, no other byte changed, a dry run writing nothing, arguments it does not know refused; with the command line a 6T's ABL gave Kario (no slot suffix), `booted` and `mark-good` taking the active slot |
| `tools/slots_test.py` (`SlotAbl`, `UserdataLayout`), kario-bsp's `tests/rauc-abl-test.py` (`AblSlots`) | RAUC through `rauc-abl` on a GPT disk image: installed to B, healthy, staying on B; B never healthy, back to A after 7 tries (a model of ABL as read from its source); a slot marked bad before it is written never booting; `get-current` still the running slot after the active flag moved. The initramfs reading `partitions.conf` (parsed, never run), telling whether a data partition holds a file system, making an ext4 only on a blank one. The userdata layout: the data partition looked up by name in sysfs (waiting for it), mounted read-write, another partition (the one `root=` names) ignored even with a root file system on it; one without ext4 or `/sbin/init` not mounted, never formatted; the whole command line logged in pieces (no line over 1024 bytes) |
| `make yocto-userdata-test` (QEMU) | A SCSI disk with 4096-byte sectors carrying the 6T's partition names, the image's root file system in `userdata` (with the 6T's `/init` and fstab `/` line added), the command line ending with simulated ABL `root=` additions: through the initramfs it always comes up and passes the selftest's first case; without the initramfs the appended `root=` makes the kernel mount the wrong partition or wait for ever ([39](39-oneplus-6t.md) §8.2) |
| `tools/phone_chroot_test.py` | `on-phone.sh partitions`: the phone's partitions, their sizes in bytes and the roles the board file gives them, what the board file names and the phone does not have |
| `scripts/yocto-check.py` | The new recipes, class and scripts (shellcheck) |

Not run by any test: the A/B part of `slot-init` as a whole (only its syntax and shellcheck; the pieces it uses have tests each; the userdata layout's part ran on QEMU); the boot slot in the update bundles (never built; `phone-abboot.bbclass`, `phone-fajita-ab.yml` and the `system.conf` that `rauc-conf` makes were built on 2026-11-24, the image never booted); everything on the phone.

## 7. Android's newer partition features: which ones Kario takes

> Status: decided (2026-11-26, after the owner asked whether Kario needs these features of modern Android); all four **not implemented**, and what the 6T does (§3) is unchanged.

| Feature | What Android uses it for | Kario |
|------|------|------|
| AVB (`vbmeta`, chained verification) | The bootloader checks vbmeta's signature, then boot, dtbo, system… by the descriptors in it; rollback indexes stop downgrades | **Yes**, at product time (§7.1) |
| Dynamic partitions (`super`) | Logical partitions such as `system`, `vendor` and `product` inside one `super`, giving each other room | **Only for compatibility**: on phones launched with Android 10 on, Kario's slots go into `super`; our own product board does without (§7.2) |
| Virtual A/B | One copy of each partition, the update written into a snapshot and merged once the new system is up | **Later, for phones with little storage** (§7.3) |
| `vendor_dlkm`, `system_dlkm` | Kernel modules put in separate partitions by who builds and signs them | **No** (§7.4) |

### 7.1 AVB: yes, at product time

The chain today: the boot image carries the root slot's signed verity descriptor and the CA certificate that checks it, phone-initramfs checks the descriptor's signature, and dm-verity guards the root slot (§3.3, [25](25-system-update.md) §0). But nothing verifies the boot image itself: the 6T's bootloader is unlocked (orange state), and a boot of one's own (with its own CA certificate inside) gets around the whole chain ([25](25-system-update.md) §4, "Boot chain"). On a product the root of trust is the vendor's bootloader (Qualcomm's ABL, MediaTek's LK), and what it speaks is AVB, so Kario follows it instead of making a scheme of its own:

- **vbmeta**: a product build makes a `vbmeta` signed with Kario's product AVB key, holding a hash descriptor of the boot image, and one of `dtbo` where the board uses `dtbo` (the 6T has it erased, §1; whether a locked bootloader boots without a `dtbo` is **unverified**; if it does not, an empty dtbo image with its hash descriptor). When locked, the bootloader checks vbmeta's signature, then the boot image's hash, and does not boot on a mismatch.
- **The root slot: chained to Kario's own descriptor, not an AVB hash tree descriptor in vbmeta**. The boot image's hash covers the initramfs, the CA certificate and the root slot's signed descriptor, so once the bootloader has verified boot, the root slot is verified through the same chain: vbmeta → the boot hash → the initramfs → the root slot's descriptor → dm-verity. Why this choice:
  1. One path all the way: QEMU, the unlocked 6T and a locked product use the same initramfs and the same kind of descriptor, and RAUC's hooks, the incremental bundles' applier (which checks against this very signed descriptor, [25](25-system-update.md) §5.3) and the shell's system update page need no second variant.
  2. A hash tree descriptor is turned into dm-verity either by the bootloader writing a command line (how early AVB did it, and bootloaders differ) or by user space reading vbmeta and setting it up itself (what Android's first-stage init does): the initramfs would have to parse vbmeta and carry libavb. Chaining to our own descriptor needs neither.
  3. A root slot is already written together with its boot slot (the flash script, RAUC's parent and child slots, §3.3), so the root hash is fixed in the boot image, and the boot hash covers it.
  The cost: the bootloader does not know the root slot, so what happens on a verification error (AVB's `androidboot.veritymode`: restart or an I/O error) is not its business but Kario's ([25](25-system-update.md) §4, `restart_on_corruption`). The initramfs still checks the descriptor's signature: on a locked phone that is one check more, which does no harm.
- **One more vbmeta per update**: vbmeta holds the boot image's hash, so a new boot needs a new vbmeta for that slot. RAUC gets another pair of child slots for `vbmeta_a` and `vbmeta_b` (the root slot their parent, like the boot slots, §3.5), and both the full and the incremental bundles carry it (a few KB). vbmeta could instead give boot a chain partition descriptor (boot carrying a signed footer of its own, so vbmeta need not change with it), but that takes one more key and one more rollback index location, and whether the vendor's bootloader accepts it is to be seen: not chosen.
- **Anti-rollback**: vbmeta carries a rollback index (`rollback_index`); the bootloader keeps the highest value seen in replay-protected storage (RPMB on Qualcomm) and does not boot a vbmeta with a smaller one. A locked bootloader refuses `fastboot flash` anyway; what the index stops is an old, correctly signed image written to storage around it (EDL flashing, writing the flash chip directly, a process with root writing the other slot). AVB recommends that the bootloader raise the stored value only once the new slot is marked successful, so that before the health check the old slot still boots (§3.5); whether the vendor's bootloader does so is **unverified**. It and RAUC's no-downgrade rule (`min-bundle-version`, [25](25-system-update.md) §0) each cover one side: one the online updates, the other flashing and changed storage. Whether every release raises it, or only releases that fix a security problem, is **not decided**.
- **The bootloader locked, trusting Kario's key**, three ways, the earlier the better:
  1. Our own product board: the design house builds the bootloader with Kario's AVB public key in it (Qualcomm's ABL is itself verified by the chip's secure boot, down from the OEM public key hash in its fuses); locked, it is in green state.
  2. A bootloader that takes a user's AVB key (`fastboot flash avb_custom_key`, as Pixels do; the Nokia 2780 Flip of [24](24-sku-framework.md) §7 can flash it too): flash Kario's public key, then `fastboot flashing lock`; it boots showing yellow state and the key's fingerprint. Whether the 6T's ABL takes one is **unverified**.
  3. The vendor's signing service: each release's vbmeta goes to the vendor to be signed with their key.
  A locked phone should report a verified boot state in device attestation: keystored's boot state is simulated today (`verified-boot:simulated`, or `unlocked` when the unlock flag is set, which the signed update reports of [25](25-system-update.md) §0 use); a product has to take it from the `androidboot.verifiedbootstate` and `androidboot.vbmeta.digest` the bootloader passes (through the TEE at best), **not implemented**.
- **avbtool**: a Python script in AOSP's `external/avb`; `avbtool.py` and libavb are under the MIT license (the repository also has Apache-2.0 and BSD files; the header of each file says which). It runs only on the build machine and is not in the image. How it enters the build: a native recipe (`avbtool-native`) fetches `external/avb` at a fixed commit and installs only the script (it needs python3 and openssl); after making the A/B boot image, `phone-abboot.bbclass` runs `avbtool add_hash_footer` (which needs the boot partition's size in bytes: a board using AVB must give `size` in its partition table, §2), then `avbtool make_vbmeta_image --include_descriptors_from_image <boot image> --rollback_index <index>`; the product's private key stays in an HSM, and avbtool's `--signing_helper` has the HSM sign, the key never leaving it (as with the module key, [24](24-sku-framework.md) §8.3). `vbmeta` in the partition table must be able to have a role that is written (today it is never touched, §2), and `tools/sku.py` and the flash script change with it. All **not implemented**.

### 7.2 Dynamic partitions: only for compatibility

- `super` lets Android's `system`, `vendor`, `product` and `odm` share one space and change their sizes on an update: liblp's metadata records which extents make up each logical partition, and first-stage init puts them together with dm-linear; Virtual A/B's snapshots go into its free space too.
- Our own product board has no use for it: each slot holds a single root file system (Kario brings its own firmware and needs no `vendor`, §1), so there are no several partitions to make room for each other; we choose the partition table, `rootfs_a/b` (or `system_a/b`) are real GPT partitions, and the boot path has no liblp to parse (the last row of §5).
- But phones launched with Android 10 on all use dynamic partitions and have no physical `system_a/b`: the Snapdragon 662 `sm6115-fxtec-pro1x` and the Snapdragon 460 `sm4250-oneplus-billie2` in `sku/boards` (used by `kario-t9-sd662-2g`, `kario-t9-sd460-2g` and `kario-qwerty-sd662-4g`) are such phones. On them Kario's two slots can only go inside `super`, the two ways §5 lists:
  - Writing liblp metadata: the build makes a whole `super` image with AOSP's `lpmake`, its logical partitions being Kario's `system_a` and `system_b`, written in one go with `fastboot flash super`; the vendor's fastbootd, recovery and `lpdump` can still read it.
  - dm-linear by offset: the initramfs parses no liblp; it makes dm-linear devices (or loop devices with an offset) from offsets and lengths worked out at build time, and opens dm-verity on them.
  The two can go together: the metadata written as liblp's, and the initramfs taking the slots at offsets worked out at build time from the same layout, with no liblp parser in the boot path; should the offsets not match (someone changed `super`), dm-verity's root hash does not match, the slot does not boot, and no wrong data gets mounted. Which one is chosen on that board. Going back to Android means flashing the stock `super` back.
- What changes: `tools/sku.py` learns logical partitions (which ones are in `super`, and their sizes), `partitions.conf` carries the offsets, the flash script writes `super` instead of sizing `system_a` by name, and RAUC's slots are dm-linear devices. The two boards' files have no `[partitions]` yet. **Not implemented**.

### 7.3 Virtual A/B: later, for phones with little storage

[25](25-system-update.md) §7.1 has weighed it: one copy of each partition saves a root slot (1 GB now, [25](25-system-update.md) §1), at the cost of snapshots and merging. What was decided there was "not done"; it is now "not done now, later on phones with little storage":

- Whether the saving is worth it depends on the storage: 1 GB is under 2% of a 64 GB phone; on an 8 GB phone it is an eighth, and the two slots together a quarter, taken from the user's photos and apps. The low-end T9 SKUs (`kario-t9-sd460-2g`, `kario-t9-sd662-2g`) are just the tier that gets little storage, but their storage is not fixed yet: the SKU files record only the memory (`memory = "2g"`), with no storage entry. Once the T9 SKUs' storage is fixed, it decides whether to do it; the SKU files should record storage too (**not implemented**).
- An update needs room too, but only for a while: the snapshot (COW) holds the changed blocks, about 118 MB uncompressed for one round of development ([25](25-system-update.md) §3: 30270 blocks of a 457.1 MB new slot that the phone did not have), given back once merged, not a 1 GB held for good.
- The cost ([25](25-system-update.md) §7.1): no further update until the merge is done, and once merged the old system is gone (before the slot is marked good it can still fall back); after a power cut the merge has to go on; the initramfs has to put the snapshot together first (dm-snapshot; Android does it in first-stage init) before it opens dm-verity, and the slot choice and rollback change with it; RAUC has no Virtual A/B of its own, so writing a slot becomes writing the snapshot device. Where the snapshot goes is to be decided too: on phones with dynamic partitions in `super`'s free space (as Android does), elsewhere in a file on userdata; but today the initramfs touches userdata only after the root slot has verified (§3.3), and a snapshot on userdata means mounting userdata first, so that order changes.
- **Not implemented**.

### 7.4 dlkm partitions: no

- Android puts kernel modules into several partitions because different parties build and sign them: `system_dlkm` holds GKI's modules, built and signed by Google together with the GKI kernel; `vendor_dlkm` (and `odm_dlkm`) holds the modules the chip vendor and the phone maker build themselves against the kernel's KMI, vouched for by a partition AVB verified (dm-verity); each side updates its own.
- Kario's kernel and all its modules come out of one build and are signed at installation with one key (per build directory in development, one per release line in an HSM for a product, [24](24-sku-framework.md) §8.3, §8.7.6); the modules live in the root slot's `/lib/modules`, and a root slot and its boot slot are always written together (§3.3): the kernel loads only modules signed in the same build, and they ship together. Putting them apart would only add one more thing to keep in step.
- When to look again: if a vendor insists on a GKI kernel with binary-only modules (the last resort of [24](24-sku-framework.md) §8.3), and those modules have to be updated on the vendor's own schedule. Even then, the unsigned modules a vendor hands over are signed by Kario at build time ([24](24-sku-framework.md) §8.3: the key is not given to the vendor) and can still go into the root slot; only when the modules cannot follow Kario's releases do they get a partition of their own.

## 8. Not done yet

- The A/B layout's images were built only once (2026-11-24), and none of it is **tried on a phone**; what to check on the phone first is in [39](39-oneplus-6t.md) §15.
- How the 6T's ABL treats the GPT attributes (7 tries, the fallback, whether `set_active` clears the successful mark, whether it switches `xbl`'s LUN) is read from Qualcomm's published source, **unverified**.
- Dynamic partitions (`super`), Unisoc's slot metadata in `misc`, an empty vbmeta with verification turned off: **not implemented** (§5, §7.2, §3.3).
- AVB for a product: the vbmeta signed by Kario, the rollback index, the bootloader locked with Kario's key, the avbtool recipe, the boot state in attestation: **not implemented** (§7.1).
- Virtual A/B: **not implemented**, waiting for the low-end T9 SKUs' storage to be fixed (§7.3).
- Reading the calibration from `persist`, crash dumps like `logdump`, the MBNs in the `modem` partition: **not implemented**.
- The boot image in an incremental bundle is whole, not a block diff.

## 9. Sources

- [39](39-oneplus-6t.md) §9.1: the 9 partition names and device numbers read on a 6T; §1: how ABL treats a `root=` on the command line (SHIFT6mq's published ABL source).
- Qualcomm's ABL (SHIFTPHONES/android_bootable_bootloader_edk2), `PartitionTableUpdate.c`: the bits of the A/B attributes, `SetActiveSlot`, `FindBootableSlot`; Qualcomm's boot control HAL (`gpt-utils.h`).
- AOSP's documentation: A/B system updates, dynamic partitions (`super`), AOSP's implementation of `boot_control` (`bootloader_control`), AVB's vbmeta and `avbtool`, Virtual A/B, `vendor_dlkm` and `system_dlkm`.
- AOSP's `external/avb`: avbtool, libavb and its README (hash and hash tree descriptors, chain partitions, rollback indexes, the boot state colours); each file's license is in its header (read on 2026-11-26).
- rmtfs and tqftpserv from linux-msm.
