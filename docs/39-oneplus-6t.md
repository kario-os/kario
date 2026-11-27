# OnePlus 6T（fajita）：Kario 的第一台真机

> 一加 6T（骁龙 845，代号 fajita）上跑 Kario：买什么，手机怎么准备，镜像怎么构建，先用 `fastboot boot` 试、再刷入，没有串口怎么调试，开机后先查什么，怎么回到 Android，还有什么没做。设备支持照 Nura（原 postmarketOS，2026-09 改名）的做法：内核、配置、固件、声音配置都取自 Nura 用的同一份，见 §12。
> 状态：在开发机上（2026-11-18，RHEL 8，x86-64，kas-container）构建出了启动镜像和 userdata 镜像（§4，SKU `oneplus-fajita-t9`，镜像里的 `sku.json` 是这台手机的屏幕和 4.5 倍）。检查过：`unpack_bootimg` 读出的头（版本 0、页 4096、偏移、命令行）；启动镜像里的内核逐字节就是 `Image.gz` 接上 6T 的设备树（`dtc` 读出 “OnePlus 6T”、S6E3FC2X01 面板）；Kario 配置片段的 134 项在最终的内核配置里一项不少；根文件系统里该启用的服务都启用了，设备树点名的每个固件文件都在；`e2fsck` 没有问题；sparse 镜像还原后与 ext4 一样；`phone-abslot` 在磁盘镜像上测过（kario-bsp 的 `tests/abslot-test.py`），`fajita-flash.sh` 对着假的 fastboot 跑过。2026-11-21 一台 6T 在另一台电脑上做了准备（§9.1：分区备份，OxygenOS 11 上 TWRP 用不了，`boot_a` 里是 Nura 的启动镜像），2026-11-22 起 Kario 在它上面跑过：先是 Nura 上的 chroot，2026-11-24 第一次刷整机（开不了机，原因查清了：一加的 ABL 总追加它自己的 `root=`），记在 §16。除了 §9.1、§16 记下的，本文关于手机上行为的说法都**未上真机**；要到手机上核对的事列在 §14。2026-11-24 第一次在它上面刷了整机固件（`0befb13` 的镜像）：刷入成功，开机每次都回到 fastboot。原因当天在手机上查明：ABL 追加的 `root=` 让内核挂上了 Android 的 system 分区；改法在 §1「为什么要 initramfs」，怎么查的（下次起不来照做）在 §8.2。

## 1. 一览

| 项 | 做法 |
|----|------|
| 启动 | 手机自带的 bootloader（ABL）直接加载 Android 启动镜像（头版本 0，页 4096，内核偏移 0x8000，ramdisk 0x01000000，tags 0x100，与 Nura 的 deviceinfo 一样），写进 `boot_a` 和 `boot_b` |
| 内核 | sdm845-mainline 的 `sdm845-7.1-rc1-r0`（Nura 的 linux-postmarketos-qcom-sdm845 用的标签），Nura 的配置加 Kario 的配置片段；`Image.gz` 后面接 `sdm845-oneplus-fajita.dtb`；强制 lockdown（integrity），只加载用构建目录的密钥签名的模块（§8.3） |
| 根文件系统 | 整个 `userdata` 分区就是一个 ext4（4 KiB 块，不再分区），由启动镜像里的 phone-initramfs 按分区名找到、读写挂上，不看命令行的 `root=`；第一次开机长到整个分区。这是默认的 userdata 布局；A/B 布局（根文件系统在 `system_a`、`system_b`，数据在 userdata）见 [43](43-phone-partitions.md) 和 §15，**未上真机** |
| A/B 槽 | 开机后 `phone-qcom-abslot` 把这次启动的槽在 GPT 属性里标成“启动成功”，bootloader 就不会数完次数退回另一个槽（A/B 布局里改由健康检查以后标，[43](43-phone-partitions.md) §3.5） |
| 调试 | USB 线上的串口控制台和 SSH（§7）；上一次开机的内核日志留在 ramoops 里 |
| 机型 | Yocto 机器 `oneplus-fajita`（kario-bsp 的 `meta-kario-sdm845/conf/machine/oneplus-fajita.conf`），kas 文件 `yocto/kas/phone-fajita.yml`，SKU `oneplus-fajita-t9`（板子 `sku/boards/oneplus-fajita.toml`，[24](24-sku-framework.md)） |

为什么要 initramfs（2026-11-24 改）：原来的启动镜像里只有一个空的 cpio，由内核按 `root=PARTLABEL=userdata` 自己挂根。这靠的是高通 ABL 的源码（SHIFT6mq 公开的那份，同为骁龙 845）：启动镜像自己的命令行里有 `root=` 时，ABL 不再追加系统分区的 `root=`、`skip_initramfs`、`ro init=/init` 和槽后缀。一加的 ABL 不是这样，2026-11-24 在那台 6T 上查明（做法在 §8.2）：

- ABL **总是**在它那一串 `androidboot.*` 里追加 `root=PARTUUID=<Android 的 system 分区>`，启动镜像自己的命令行里有没有 `root=` 都一样；内核以最后一个 `root=` 为准。
- 启动镜像的命令行里有 `root=` 时，ABL 不加 `skip_initramfs rootwait ro init=/init`，也不加 `androidboot.slot_suffix=_a`；没有时（Nura 的命令行）全都加。
- 失败那次的控制台（ramoops，热重启后读出来的）：内核挂上了 Android 的 system 分区，`/etc/init` 执行不了，跑的是 `/bin/init`（Android 的 init），它在第一阶段挂不上 selinuxfs，`InitFatalReboot: signal 6` 后重启进 bootloader：开机 17 秒回到 fastboot。
- 连续失败用完 a 槽的次数以后，ABL 把它标成不可启动（`slot-unbootable:a: yes`、`slot-retry-count:a: 0`），之后连 `fastboot boot` 任何镜像都是 `Load Error`，直到 `fastboot set_active a`（次数回到 7）。

Kario 的内核、设备树、UFS 和根文件系统都没问题：Kario 的内核换上 Nura 的 initramfs 就起来了，看得到 UFS（`sda1`…`sda17`）和完好的 userdata（`sda17`，`PARTLABEL=userdata`）。那次 busybox 的 `mount PARTLABEL=userdata` 失败说明不了内核：busybox 的 `mount` 本来就不认 `PARTLABEL=`，内核的挂根代码认（QEMU 上没有追加时内核照样按它挂上，§8.2）。

现在 userdata 布局的启动镜像也带 phone-initramfs（QEMU 和 A/B 布局用的同一个，[43](43-phone-partitions.md)），不再靠 ABL 怎么处理命令行：它从 ramdisk 里的 `/etc/phone/layout` 知道是 userdata 布局，按板子的分区表（`partitions.conf`，数据分区是 `userdata`）在 sysfs 里按分区名找（不要 udev；UFS 的几个单元先后出来，最多等 30 秒），不跑 fsck、读写挂上 ext4，`switch_root` 到 `/sbin/init`；命令行里的 `root=`、`init=`、`ro`、`skip_initramfs` 一概不用。它把整条 `/proc/cmdline`（ABL 追加的也在里面）和它做了什么写进内核日志（`phone-initramfs:` 开头的几行），下次开机在 pstore 和 `bringup-log` 里看得到（§8.2）。分区找不到、上面不是 ext4（Android 或 Nura 的数据）、没有 `/sbin/init` 时，写明原因，10 秒后重启（热重启，pstore 留着这次的日志）；它从不格式化 userdata。启动镜像自己的命令行还留着 `root=PARTLABEL=userdata`，谁也不读它：有它，ABL 就不加 `skip_initramfs`、`ro init=/init` 和 `androidboot.slot_suffix`（自己的 `root=` 照加）。没有槽后缀，`phone-abslot` 就把 GPT 里标成活动的那个槽当作这次启动的槽：ABL 启动的就是活动的槽，`fajita-flash.sh` 的 `flash`、`test` 也先把当前槽设为活动，所以 `phone-qcom-abslot` 照样标对槽（kario-bsp 的 `tests/abslot-test.py` 用那次 ABL 给的命令行测过）。`rootfstype`、`rootwait`、`rw` 是给自己挂根的内核的，去掉了。根文件系统里照旧有 fstab 的 `/` 行，让 systemd 第一次开机把 ext4 长到整个分区；`/init` 链接也还在，可它是给自己挂根的内核的，6T 上那样的内核挂的总是 ABL 给的 Android 分区，用不上了。UFS、GPT、ext4 都编进内核，initramfs 不需要模块。QEMU 上证过（`make yocto-userdata-test`：4096 字节扇区的 SCSI 盘上 6T 的分区名，命令行末尾照那次的样子模拟 ABL 追加的参数，§8.2），**未上真机**。

为什么不用 U-Boot + systemd-boot：Nura 现在给 sdm845 同时装 systemd-boot，那是给另刷了 U-Boot（UEFI）的人用的；用原厂 bootloader 时 Nura 刷的也是同样的 Android 启动镜像。多一层 U-Boot 就多一个要刷、要调的东西，第一台真机先用最少的。

两种测试，先后做，**都要先做 §3.5 的备份**（第一次往手机写任何东西之前），用同一个套件（`tools/phone-chroot/kario-phone.sh --sku oneplus-fajita-t9`，[24](24-sku-framework.md) §6.2）、同样的步骤、同样的结果；§13 是两者共同的步骤：

1. chroot：手机装 Nura，这台手机自己的镜像（§6 刷的那个）的根文件系统在它的内核上跑：应用、Shell、自测、兼容测试、像素自测、内存和耗电，用 Nura 的内核、屏幕、调制解调器和声音。
2. 整机（`--whole`）：手机启动我们自己的镜像（§5 先试、§6 刷入），内核、固件、启动和 A/B 槽都是我们的；同样的自测、兼容测试、像素自测、事实和结果，经它的 USB 控制台的网络（§7）。

回到 OxygenOS 见 §9；最坏的情况（进不了 fastboot）有 §3.4 的 MSM 下载工具，它之后用 §3.5 的备份把 IMEI 和校准数据写回去。

## 2. 买什么

- 一加 6T，国行 A6010 就行（和国际版是同一种硬件，只是出厂的系统是氢 OS）。8 GB 或 6 GB 内存都行。屏幕要好，碎屏或换过的屏可能不是原装面板。
- 键盘：**蓝牙键盘**。USB-C OTG 键盘现在用不了：主线设备树里 6T 的 USB 只做设备（`dr_mode = "peripheral"`），手机也不给 VBUS 供电；改成主机模式又会丢掉 §7 的 USB 控制台。Shell 一侧已经会当主机（放行、VBUS 预算、锁着时插上的键盘只输 PIN 码，[27](27-usb-typec.md) §2.1），6T 还缺（**未实现**，内核和板子文件在另一个仓库里改）：dwc3 改成 `dr_mode = "otg"` 加 `usb-role-switch`，PMI8998 的 Type-C 口驱动（主线的 `qcom_pmic_typec`，`CONFIG_TYPEC_QCOM_PMIC`；它在用的内核版本里认不认 PMI8998 要核对，**未验证**）和设备树里的 `usb-c-connector`（`data-role`、`power-role` 都是 `dual`，端点连到 dwc3），PMI8998 的 VBUS 稳压器（`CONFIG_REGULATOR_QCOM_USB_VBUS`，compatible 同样要核对）当它的 `vbus-supply`，内核另开 `CONFIG_USB_DWC3_DUAL_ROLE`、`CONFIG_USB_XHCI_PLATFORM`、`CONFIG_USB_HID`、`CONFIG_HID_GENERIC`（U 盘还要 `CONFIG_USB_STORAGE`）；之后板子文件写 `[usb] host = true`、`vbus_ma = 500`，记进 `unverified`。§7 的 USB 控制台占着设备控制器的时候插键盘会怎样（口当主机时控制台断开，拔掉后要回来）要在手机上看（**未上真机**）。
- 一根能传数据的 USB-C 线，一台装了 `fastboot` 和 `adb`（android-tools）的 Linux 电脑（§3.5 的备份还要一个 6T 的 TWRP 镜像；一台 OxygenOS 11 的 6T 上 TWRP 起不来，改用 Nura 的启动镜像，见 §9.1）。
- 一张 SIM 卡。中国的运营商大多要 VoLTE 才能打电话（联通、电信关了 2G、3G），VoLTE 写好了、**未上真机**（§11.1，试法在 §14.6）；先试短信和数据。试通话另要第二部手机当对方。

## 3. 准备手机

### 3.1 系统：OxygenOS 11.1.2.2

Nura 和社区的说法是先升到 6T 的最后一版 OxygenOS（11.1.2.2）：主线内核用的是手机分区里的固件（调制解调器、DSP 等），这一版测得最多。

国行出厂是氢 OS。A6010 的硬件相同，可以改刷 OxygenOS：

1. 先按 §3.3 解锁，或者直接用 §3.4 的 MSM 下载工具刷国际版 OxygenOS（社区的 fajita 国际版 MSM 包是 OxygenOS 9 或 10）。
2. 进 OxygenOS 后，在“设置 › 系统 › 系统更新 › 本地升级”装 11.1.2.2 的完整升级包（OnePlus 官网或社区镜像下载）。

这些步骤是社区的做法，Kario 没有试过（**未上真机**）。

### 3.2 两个槽的固件一致

6T 是 A/B 双槽，另一个槽里可能是旧固件。Kario 的启动镜像写两个槽（§6），bootloader 退回另一个槽时用的就是那一槽的调制解调器等固件，所以两个槽要一样。两种做法：

- 用“本地升级”把同一个 11.1.2.2 完整包装两次（每次装进不在用的那个槽）。
- 或者照 LineageOS 的 fajita 安装说明，在它的 recovery 里 `adb sideload` 它的 copy-partitions 包，把 a 槽的分区拷到 b 槽。

### 3.3 解锁 bootloader

1. 设置 › 关于手机，连点“版本号”七次打开开发者选项；在开发者选项里打开“OEM 解锁”和“USB 调试”。
2. `adb reboot bootloader`（或关机后按住音量上 + 电源）进 fastboot。
3. `fastboot oem unlock`，在手机上用音量键选确认、电源键确定。**这会清空手机上的所有数据。**
4. 重启进系统走一遍开机向导，再回 fastboot：`fastboot getvar unlocked` 应该是 `yes`（或 `fastboot oem device-info` 里 `Device unlocked: true`）。

### 3.4 变砖了：MSM 下载工具

一加 6T 真变砖（进不了 fastboot）时，用高通 EDL 模式加 OnePlus 的 MSM 下载工具（Windows，社区发布的 fajita 包）恢复：关机，按住音量上和音量下插 USB 线，手机进 EDL（9008），工具把所有分区写回出厂状态，bootloader 也会重新上锁。Kario 只写 `dtbo`、`boot`、`userdata` 三处（§6；A/B 布局另写 `system`，§15），一般用不到它；但它是最后的退路，动手前先找好对应型号的包。它会清掉或换掉 `persist`（指纹和传感器的校准）和存 IMEI 的分区，出厂包里的不是这台手机的：用过它以后，用 §3.5 的备份 `fajita-flash.sh restore-efs` 写回去。**§3.5 的备份做完以前不要用它**：那时手机上的这几个分区是唯一的一份。手机进了 EDL、又不想刷时怎么出来，见 §9.1。

### 3.5 备份：只有这台手机有的分区

解锁之后、第一次往手机写任何东西之前（装 Nura 也算），把这几样拷到电脑上：`persist`（指纹和传感器的校准，换一台手机的不能用）、`modemst1`、`modemst2`、`fsg`、`fsc`（IMEI 和基带的配置，丢了就没有 IMEI、打不了电话），两个槽的 `boot` 和 `dtbo`（回 OxygenOS 用），还有每个 LUN 的 GPT。

```
yocto/fajita-flash.sh backup twrp-fajita.img
```

`twrp-fajita.img` 是能在 OxygenOS 11 上 `fastboot boot` 起来的 6T 的 TWRP（twrp.me 的 fajita 页，或 XDA 上社区维护的版本）。脚本 `fastboot boot` 它，只在内存里跑，手机上什么也不写；经 TWRP 的 adb 逐个 `dd` 出分区，每个都和手机上的 SHA-256 对一遍；写 `SHA256SUMS` 和 `phone.txt`（序列号、时间、`boot` 是不是 OxygenOS 的）；最后回到 fastboot。默认放在 `~/kario-phone-backup/oneplus-fajita-<序列号>/`，不在仓库里；**再拷一份到别的盘或网盘**：它是这台手机的 IMEI 和校准数据唯一的副本。已经有备份时脚本什么也不做，不会拿后来改过的手机覆盖它。

`test`、`flash`、`restore` 都先核对这份备份（同一个序列号、本机独有的五个分区都在、每个文件的 SHA-256），对不上就拒绝，手机上什么也不写；缺 `boot`、`dtbo`（手机那时已经丢了它们，§9.1）只提醒，回 OxygenOS 时要 `--stock`（§9）。

这一步对着模拟的手机测过（`yocto/fajita-flash-test.py`，在 `make test` 和 CI 里），**未上真机**。

一台 6T（OxygenOS 11.1.2.2）上这一步做不了：TWRP 3.7.0_9 `fastboot boot` 以后崩溃进了 EDL，别的镜像被 bootloader 拒绝。那台手机的备份改在 Nura 启动镜像的调试 shell 里用 `dd` 做，见 §9.1；脚本还不会走这条路（**未实现**）。

## 4. 构建

```
make yocto-doctor WORK=$HOME/yocto/fajita-work
make yocto-build WORK=$HOME/yocto/fajita-work KASFILE=yocto/kas/phone-fajita.yml SKU=oneplus-fajita-t9
```

- 先 `make yocto-doctor`：构建缺了就不行的（podman 或 docker、kas-container、rootless podman 的 subuid 和 subgid、不比 go.mod 旧的 Go、GCC 14、cargo、openssl、磁盘）缺一样就标 MISS、以 1 退出，见 [13](13-prototype.md) §5.1。go.mod、go.sum 改过时，`make yocto-build` 先重新生成 Go 模块清单（要联网）。

- SKU `oneplus-fajita-t9` 是这台手机当九宫格直板用（[24](24-sku-framework.md) §7.4）：它生成 `phone-sku` 的 `sku.json`（Shell 按 1080×2340 的屏幕、4.5 倍画，上下各留 80 行给刘海和圆角）和 kas 覆盖（机器 `oneplus-fajita`；整机用内核自己的设备树，不叠加覆盖层）。整机没有 `make sku` 的固件包，构建出来的镜像直接刷（§5、§6）。
- kas 文件沿用 `phone-qemuarm64.yml` 的发行版和软件包，去掉 A/B 系统槽、dm-verity 和 wic 磁盘（userdata 布局里没有；A/B 布局见这一串的最后一条），加 meta-qcom。上游各层的提交固定在 `phone-qemuarm64.yml` 里，和 QEMU 镜像用同一套（之后的 wrynose 让 udev 依赖 iproute2-devlink，最后要 bash，GPL-3.0）。并发（`BB_NUMBER_THREADS`、`PARALLEL_MAKE`）不再固定为 16，按这台机器的处理器和内存算（`make yocto-doctor` 打出来，`YOCTO_JOBS=` 可改，[13](13-prototype.md) §5.1）；8 核 15 GB 的机器上另加的 ncurses、zstd 单线程和不编 pzstd 没有采用，原因也在那里。
- `PHONE_BRINGUP = "0"`（local.conf 或 kas 覆盖）构建不带 §7 调试手段的镜像。
- GPU 的测试镜像：再加 `KASEXTRA=yocto/kas/paint-gpu.yml`，phone-paint 开机就在 GPU 上合成（[42](42-gpu-rendering.md) §3；起不来或用着出错时照旧用 CPU 画），别的和这个 SKU 的镜像一样（只多 `sku.json` 的 `"paint_gpu": "compose"`），SKU 不变；手机上照 §8.1 查。
- 开机时屏幕上没有内核和 systemd 的输出、也没有光标（2026-11-26 起，报告 007，§16.13）：从引导程序的画面一直到 Shell 的开机动画。要看开机日志就加 `KASEXTRA=yocto/kas/boot-log.yml`（设 `PHONE_BOOT_LOG = "1"`，命令行多一个 `console=tty0`），它和 `PHONE_BRINGUP` 无关；串口（`console=ttyMSM0`）、pstore 和 journal 两种镜像里都有完整的日志。
- 内核模块的签名密钥：`make yocto-build` 先跑 `make ota-keys`，没有时在 `$WORK/ota-keys/` 里生成 `kernel-modules.pem`（不进 git，和更新密钥放在一起）；内核只认它，镜像构建时核对每个模块（§8.3）。
- A/B 布局（[43](43-phone-partitions.md)，2026-11-24 构建过一次，**没开过机**）：`KASFILE=yocto/kas/phone-fajita-ab.yml`（包含 `phone-fajita.yml`，设 `PHONE_LAYOUT = "ab"`，加上 RAUC、dm-verity 和更新密钥），SKU 不变。

产物在 `$WORK/build/tmp/deploy/images/oneplus-fajita/`：

| 文件 | 写到哪 | 大小（2026-11-24 的构建） |
|------|--------|------|
| `boot-oneplus-fajita.img` | `boot_a`、`boot_b`：内核接设备树、phone-initramfs | 23 101 440 B (22.0 MiB) |
| `phone-image-oneplus-fajita.rootfs.ext4.sparse` | `userdata`（全零的一段只传 4 个字节） | 494 244 860 B (471 MiB) |
| `phone-image-oneplus-fajita.rootfs.ext4` | 同一个文件系统，原始格式 | 756 830 208 B (722 MiB) |
| `phone-image-oneplus-fajita.rootfs.abboot`（A/B 布局） | `boot_a`、`boot_b`：内核、设备树、phone-initramfs、根的签名 verity 描述 | 23 101 440 B (22.0 MiB) |
| `phone-image-oneplus-fajita.rootfs.ext4.verity.sparse`（A/B 布局） | `system_a`、`system_b`：ext4 加 dm-verity 的哈希树 | 502 748 180 B（展开 766 509 056 B） |

启动镜像由 AOSP 的 mkbootimg 做（`mkbootimg-native`）；sparse 镜像由 `yocto/meta-phone/files/phone-img2simg.py` 做：全零和重复同一个字的块写成 fill 块，不写成 “don't care”，分区里原来的数据不会在文件系统以为是零的地方露出来。检查启动镜像：`unpack_bootimg --boot_img boot-oneplus-fajita.img`（同一套 AOSP 工具，构建里在 `tmp/sysroots-components/x86_64/mkbootimg-native/usr/bin/`）应该列出头版本 0、页 4096、上面的偏移和命令行。2026-11-24 的构建（在构建机的 `bt-work` 里）读出来是：内核 15 885 968 B（就是 `Image.gz` 接 `sdm845-oneplus-fajita.dtb`，逐字节），ramdisk 7 204 986 B：前 1024 字节是只装着 `etc/phone/layout`（`layout=userdata`）的 cpio，后面逐字节就是 `phone-initramfs-oneplus-fajita.cpio.gz`（7 203 962 B，解开约 18 MB：busybox、`/init` 即 `slot-init`、板子的 `partitions.conf`，还有 A/B 布局要的 veritysetup、openssl、mke2fs）；命令行 `root=PARTLABEL=userdata console=ttyMSM0,115200 phone.gui=1 console=tty0`（2026-11-26 起默认是 `… phone.gui=1 vt.global_cursor_default=0`，`console=tty0` 只在 `PHONE_BOOT_LOG = "1"` 时有，上面一条）；以前那个（`0befb13`）的 ramdisk 是 512 字节的空 cpio。`boot_a`、`boot_b` 多大文档里还没有（§15.1）：那台 6T 的 `boot_a` 装过 Nura 的启动镜像（它自己构建的同一版内核加 14 MB 的 initramfs），比这个大；`flash` 写之前照旧读 `partition-size` 核对。`fajita-flash.sh` 的 `test`、`flash` 拒绝 ramdisk 里没有 initramfs 或没写布局的启动镜像（§5）。

## 5. 先试：fastboot boot

```
yocto/fajita-flash.sh -n test     # 只打印要执行的 fastboot 命令
yocto/fajita-flash.sh test
```

先要有 §3.5 的备份。`test` 擦掉 `dtbo_a`、`dtbo_b`，然后 `fastboot boot` 启动镜像一次：`boot` 和 `userdata` 都不写，重启后 bootloader 又从自己的 `boot` 分区启动。dtbo 必须擦：不擦的话 ABL 会把 Android 的设备树覆盖层套到主线设备树上，手机停在 fastboot（sdm845-mainline 的安装说明）。擦了 dtbo，OxygenOS 在 dtbo 刷回去之前也起不来（§9）。

这一步 userdata 里还是 Android 或 Nura 的数据，不是 Kario 的根文件系统：用 `KASEXTRA=yocto/kas/boot-log.yml` 构建的启动镜像（§4）能在屏幕上看到内核的输出（`console=tty0`）和 initramfs 的几行（`phone-initramfs: … no ext4 on /dev/sda17` 或 `has no /sbin/init`）；默认的镜像屏幕上不写这些，引导程序的画面停到显示驱动接手（约 6 秒），之后黑着，所以这一步要用带开机日志的镜像。10 秒后它重启，手机起来的是 `boot` 分区里原来的系统（擦了 dtbo 的 OxygenOS 起不来，§9）。确认屏幕亮、内核起来、initramfs 找到了 userdata，再刷入。脚本先检查 `fastboot getvar product` 是 `sdm845`，不是就拒绝；bootloader 报的是芯片，一加 6 也是 `sdm845`，所以写之前还要你确认是 6T（`-y` 跳过确认）。启动镜像不带 initramfs（2026-11-24 以前的构建）、ramdisk 里没写 userdata 布局的，`test` 和 `flash` 都拒绝。`test` 在 `fastboot boot` 以前先把当前槽设为活动（`fastboot --set-active`，只改 GPT 里槽的属性）：ABL 把连续起不来的槽标成不可启动以后，连 `fastboot boot` 都拒绝（`Load Error`，§8.2），设为活动就清掉这个标记、重给 7 次；它也清掉那个槽的“成功”标记，装在 `boot` 里的系统下次起来自己再标（Kario 的 `phone-qcom-abslot`、OxygenOS 自己的）。

## 6. 刷入

```
yocto/fajita-flash.sh flash
```

先要有 §3.5 的备份。`boot` 和 `userdata` 要来自同一个构建目录：Kario 的内核强制 lockdown（integrity），只加载用这个构建目录的密钥签名的模块（§8.3）；镜像构建时已经核对过 userdata 里的模块和启动镜像里的内核是一对。

依次：擦 `dtbo_a`、`dtbo_b`；`boot_a` 和 `boot_b` 都写 Kario 的启动镜像（bootloader 退回另一个槽时还是 Kario，一位 6T 用户就遇到过没标“成功”的槽冷启动退回了 OxygenOS）；写 userdata 的 sparse 镜像；把当前槽设为活动（两种布局都是：ABL 清掉不可启动的标记、重给 7 次，Kario 起来后 `phone-qcom-abslot` 标成功，§1）；重启。每个镜像都按 `fastboot -S 64M` 分块发，一次下载最多 64 MiB（`--chunk` 改大小，比如 `--chunk 16M`）：2026-11-24 那台 6T 上，原来的 `-S 512M`（bootloader 一次最多收 512 MiB）在第一块传到 471 MB 时 USB 断了（`Write to device failed in SendBuffer()`），`-S 64M` 分 8 块、22.5 秒就写完了。小块每次只多一个来回，几十毫秒的事；比 64M 更小也行，64M 是那台手机上试成了的。`-s` 选手机，`-d` 换镜像目录，`-n` 只打印。

要写、要擦的分区来自板子文件（`sku/boards/oneplus-fajita.toml` 的 `[partitions]`，[43](43-phone-partitions.md) §2）。写之前脚本先用 `fastboot getvar partition-size:<名字>` 读出每个要写、要擦的分区多大，没有这个分区或镜像放不下就什么也不写；演练（`-n`）先列出要写的每个分区、哪个文件、多大，再列出 fastboot 命令。`persist`、`modemst*`、`fsg`、`fsc`、基带和引导链的分区在任何路径上都不写，板子文件怎么写都一样。刷完在备份目录里记下布局（`kario.txt`）。`--layout ab` 刷 A/B 布局：先照 §15 查过再用。

第一次开机：initramfs 按名字找到 userdata、读写挂上（§1），systemd 把 ext4 长到整个 userdata（`x-systemd.growfs`），`phone-qcom-abslot` 标槽，然后 Kario 的界面出来。

## 7. 调试控制台

没有串口（6T 的调试串口只在主板测试点上），调试走 USB 线。`PHONE_BRINGUP = "1"`（机器的默认值）时镜像里有 `phone-bringup`：

- 开机早期绑定一个 USB 设备（`kario-bringup`，1d6b:0104），带两个功能：CDC-ACM 串口（手机上是 `ttyGS0`，root 自动登录）和 CDC-NCM 网卡（手机上是 `dbg0`，地址 172.16.42.1，给电脑分 172.16.42.11–20）。它先占了 USB 控制器，Shell 自己的 USB 共享这时不工作；它不叫 `usb0`，Shell 给 USB 链路设的防火墙不管它。
- 电脑配置了这个 USB 设备时（插着电脑、电脑认了它）持着唤醒锁 `phone-bringup`，手机不休眠，USB 控制台不会断；拔掉线、只插充电器时放开，手机照常睡（`phone-bringup-awake`，2026-11-26 改，**未上真机**；以前开机就持着、一直不放，一夜没电，§16.12）。`/usr/libexec/phone-bringup/awake check` 说现在该不该持、为什么。
- 屏幕上的开机日志不在这里：它另由 `PHONE_BOOT_LOG` 管（§4），bring-up 镜像默认也不在屏幕上写日志。
- 内核日志：设备树里有 ramoops 区（`sdm845-oneplus-common.dtsi` 的 `ramoops@ac300000`，4 MiB：控制台 256 KiB、崩溃记录每条 256 KiB、ftrace 256 KiB、pmsg 2 MiB，带 16 字节的 ECC；2026-11-24 照 `sdm845-7.1-rc1-r0` 读的），内核开着 `CONFIG_PSTORE_RAM` 和 `CONFIG_PSTORE_CONSOLE`，上一次开机的控制台和崩溃日志热重启后还在（6T 上**未核实**）；systemd-pstore 每次开机把它们挪到 `/var/lib/systemd/pstore`；journal 也存在 userdata 上。initramfs 的那几行（`phone-initramfs:`，§1）也在里面。

```
picocom /dev/ttyACM0                 # 串口：电脑上出现 ttyACM0（screen /dev/ttyACM0 也行）
ssh root@172.16.42.1                 # 网络：电脑的新网卡用 DHCP；没有就 ip addr add 172.16.42.2/24 dev <网卡>
journalctl -b -1                     # 上一次开机的日志
bringup-log                          # pstore 留下的上一次内核日志（-a：全部）
phone-abslot status                  # 两个槽的状态
systemctl stop phone-bringup-awake   # 连着电脑也放开唤醒锁，试休眠
```

lockdown（integrity）下没签名的模块加载不了；debugfs 只有只读的文件读得到（比如 `wakeup_sources`），可写的、带 ioctl 或 mmap 的都不行：要用它们得换一个不带 `phone-lockdown.cfg` 的内核（§8.3）。

root 没有密码，谁有线谁就是 root：这是开发镜像，只在调试时用。`PHONE_BRINGUP = "0"` 的镜像里没有这些（dropbear 和空密码是 `phone-qemuarm64.yml` 的设定，所有开发镜像都有）。

## 8. 先查什么

| 项 | 怎么看 | Nura 上 |
|----|--------|---------|
| 启动 | `dmesg \| grep phone-initramfs`：`command line:`（整条命令行，ABL 追加的在后面）、`the root file system: /dev/sdaNN, the partition userdata`、`starting /sbin/init`；`findmnt /` 是那个 `sdaNN`、`rw`；`sed 's/androidboot.serialno=[^ ]*/androidboot.serialno=<serial>/' /proc/cmdline` 记下来（ABL 追加了什么）。起不来时照 §8.2 | — |
| 模块 | `cat /sys/kernel/security/lockdown` 是 `[integrity]`；`/usr/libexec/phone/module-check.sh` 打出 `PHONE-MODULES: PASS`；`dmesg \| grep Lockdown:` 里没有 `unsigned module loading is restricted`；`lsmod` 里有 `qcom_q6v5_mss`、`ath10k_snoc`、`snd_soc_sdm845`（§8.3） | — |
| 屏幕 | 开机先是内核和 systemd 的文字，然后 Kario 的界面；`dmesg \| grep -i -e dsi -e dpu` | 能用 |
| 按键 | 电源键、音量键：`cat /proc/bus/input/devices`（pm8941_pwrkey、gpio-keys），界面有反应 | 能用 |
| 触摸屏 | `cat /proc/bus/input/devices` 里有 Synaptics 的触摸屏（RMI4，编进了内核）；`journalctl -u phone-display \| grep -e touchscreen -e "input:"`：phone-paint 的 `touchscreen …: x …, y … on a 1080x2340 panel` 和 Shell 的 `input: touchscreen added`；点、拖、划照 §14.17 | 能用 |
| 槽 | `phone-abslot status`：这次的槽 `successful` | — |
| Wi-Fi | `ip link` 里有 `wlan0`；设置 › Wi-Fi 连一个网络；`dmesg \| grep regulatory` 里没有 `failed to load regulatory.db`（cfg80211 编进了内核，开机时只读一次，从 initramfs 读，现在它在 initramfs 里） | 能用 |
| 蓝牙 | `journalctl -u phone-qcom-btaddr` 给了地址；设置 › 蓝牙配对键盘 | 能用 |
| 调制解调器 | `cat /sys/class/remoteproc/*/state`（running）；`systemctl status rmtfs tqftpserv phone-qcom-uim`；`mmcli -L` | 能用 |
| SIM | `mmcli -m 0`；`qmicli -d qrtr://0 --uim-get-card-status` | 能用 |
| 短信 | 发一条、收一条（信息应用）；`mmcli -m 0 --messaging-list-sms` | 能用 |
| 数据 | 设置里开移动数据；`mmcli -m 0` 的承载、`ip addr` 里的 rmnet 接口 | 能用 |
| 通话 | 打出、接听；听筒有没有声音、对方听不听得见，免提、静音（[20](20-audio.md) §10 末尾的清单）；`cat /run/phone/audiod-ucm.json` 是当前的 UCM 设置，`journalctl -u phone-audiod` 里看 `call routing` | 能用（Nura 用 callaudiod 切换声音路由） |
| 声音 | `aplay -l`；`systemctl status phone-audiod`（开机后第一次启动时它经 alsaucm 跑 UCM 配置的引导序列、设好 HiFi 和扬声器，放到 `hw:0,2`）；按键音、铃声从扬声器出来；`aplay -D plughw:0,2 <wav 文件>` | 能用 |
| 电池 | `cat /sys/class/power_supply/*/capacity`；充电时 `status`（电量计和充电器编进了内核，开机就在，§8.3）；充满时拔插充电器，插上时屏幕亮（充电器的 `online` 说了算，§16.11） | 能用（没有 Dash 快充） |
| 休眠 | 拔掉 USB 线（或 `systemctl stop phone-bringup-awake`），息屏后等它睡，按电源键唤醒；`cat /sys/power/suspend_stats/success`；一夜的耗电照 §14.19 | 能用 |
| 关机、重启 | 拔着 USB 线 `systemctl poweroff`，手机关住；插着线关机，手机由充电器开起来，进关机充电（只有电池的画面，拔线就关，[28](28-battery.md) §10）；`reboot` 以后 `journalctl -b -1 \| grep -i watchdog` 写着 30 秒，`journalctl --list-boots` 里没有多出来的短启动（§14.10） | — |
| 时钟 | `phone-clock show`（RTC 的 `since_epoch`、存着的偏移、RTC 加偏移离系统时钟多远）；`cat /sys/class/rtc/rtc0/hctosys` 是 1（RTC 编进了内核，§8.3）；`journalctl -u phone-clock`；关着 Wi-Fi、不插 SIM 卡重启，时间还对（§14.10，[28](28-battery.md) §6） | 不联网开机是 1970（chroot 套件那次看到的，[41](41-design-gaps.md) §11） |

“Nura 上”一栏是 Nura 设备页的说法，不是 Kario 的结果；Kario 这一栏全部**未上真机**。

### 8.1 GPU：phone-paint 在 Adreno 630 上合成

**未上真机**（GPU 测试镜像 009 试过一次：GPU 没起来，退回 CPU 画；沙箱和固件 2026-11-27 改了，[42](42-gpu-rendering.md) §4、§5）。phone-paint 默认用 CPU 画（板子文件写着 `paint_gpu = "off"`，[42](42-gpu-rendering.md) §3）；设 `PHONE_PAINT_GPU=1` 后在 GPU 上合成（[42](42-gpu-rendering.md)；QEMU 上只用软件 GPU 验证过）。用 `phone-fajita.yml` 构建的镜像带 freedreno 版的 Mesa（42 §5）。经 USB 控制台（§7），在上面那些不开它都查过以后。打开它（连同下面几项要用的逐帧记录）：

```
mkdir -p /etc/systemd/system/phone-display.service.d
printf '[Service]\nEnvironment=PHONE_PAINT_GPU=1 PHONE_PAINT_TRACE=1 PHONE_PAINT_STEPS=/run/phone/steps.jsonl PHONE_PAINT_DUMP=/var/lib/phone/frames.jsonl\n' \
  > /etc/systemd/system/phone-display.service.d/gpu.conf
systemctl daemon-reload && systemctl restart phone-display
```

GPU 的测试镜像（§4 的 `KASEXTRA=yocto/kas/paint-gpu.yml`）开机就在 GPU 上合成，不用这个 drop-in 打开：drop-in 里去掉 `PHONE_PAINT_GPU=1`，只留逐帧记录的三个变量；要和 CPU 路径比（§14.3、§14.4 的量法）就写 `PHONE_PAINT_GPU=0`，量完删掉。

| 要确认的 | 怎么看 | 算确认了 |
|----|--------|---------|
| 镜像默认走 GPU（只对 GPU 的测试镜像） | `grep paint_gpu /usr/share/phone/sku.json`；没有 drop-in 时开机，`grep 'phone-paint: gpu' /run/phone/gui.log`；再用 drop-in 写 `PHONE_PAINT_GPU=0`、重启 phone-display，看同一行和 `PHONE-GUI-READY` | `"paint_gpu": "compose"`；没有 drop-in 时有 `composing with`；`PHONE_PAINT_GPU=0` 时没有 `gpu:` 一行，features 里没有 `gpu`（环境变量优先） |
| 镜像里有带 freedreno 的 Mesa，GPU 固件在开机时加载 | `ls /usr/lib/gbm/dri_gbm.so`；`grep -c FD_MESA_DEBUG /usr/lib/libgallium-*.so`（构建时已经查过，[42](42-gpu-rendering.md) §5）；`dmesg \| grep -i -e a630 -e gmu -e zap -e adreno` | 文件在，计数不是 0；开机五六秒时（msm 绑定、内核的 fbdev 第一次打开 card，还在 initramfs 里）就有 `loaded qcom/a630_sqe.fw from new location`、`loaded qcom/a630_gmu.bin`，整个开机没有 `failed`、`-2` 或 zap 的错误。2026-11-27 起 GPU 的固件也在 initramfs 里（[42](42-gpu-rendering.md) §5）；到 009 为止的镜像开机那次是 `a630_sqe.fw failed with error -2`，到渲染器打开 card 才加载上 |
| EGL 和 GBM 在 card 的 fd 上起来 | `grep 'phone-paint: gpu' /run/phone/gui.log`；`grep -e MESA-LOADER -e 'EGL is not initialized' /run/phone/gui.log`；`journalctl -b -u phone-display \| grep PHONE-GUI-READY` | `composing with` 后面是 freedreno 给 Adreno 630 的名字（比如 `FD630`），不是 softpipe 或 llvmpipe；没有 `MESA-LOADER: failed to retrieve device information` 和 `EGL is not initialized`（009 就是这样退回 CPU 的：沙箱挡住了 Mesa 找设备，[42](42-gpu-rendering.md) §4）；features 里有 `gpu` |
| 按面板的刷新率翻页 | 打开主菜单、移动焦点、打开再关掉一个应用、滚动设置；然后 `grep 'motion:' /run/phone/gui.log \| tail -3` 和 `tail -1 /run/phone/steps.jsonl` | 转场和滑动里 `a frame every p50` 约 16.7 ms（60 Hz），`later than a frame and a half` 很少；最后一行的 `flips` 里完成的翻页最多比要求的少一个 |
| 带 freedreno 的沙箱 | 上面那行 `PHONE-GUI-READY`；`journalctl -b -k \| grep -i -e seccomp -e landlock`；`journalctl -b -u phone-display \| grep -e 'renderer restarted' -e crashed` | features 里有 `sandboxed landlock seccomp gpu`；没有把它杀掉的拒绝，也没有重启 |
| 设 `PHONE_PAINT_GPU=1` 后没有退步 | 再走一遍 §8 的屏幕和按键，再加：来短信的横幅、选项菜单（Tab）、相机取景、电源键灭屏再点亮、设置 › 辅助功能的颜色反转和颜色校正开了再关、放大（同时按两个音量键）开了再关、通话界面；然后 `grep -e 'drawing on the CPU' -e 'gpu:' /run/phone/gui.log` | 看起来都和 CPU 路径一样（滤镜和放大镜也在 GPU 路径上，[42](42-gpu-rendering.md) §4.2）；只有 `composing with`，从来没有 `drawing on the CPU from now on` |
| 出错时回到 CPU | 上面几项做完以后，drop-in 加 `PHONE_PAINT_GPU_FAIL=300`，重启 phone-display，打开主菜单、进出几个应用，直到下面那行出现；`grep -e 'failing on purpose' -e 'drawing on the CPU' /run/phone/gui.log`；然后从 drop-in 删掉它 | `failing on purpose at frame 300 (PHONE_PAINT_GPU_FAIL); drawing on the CPU from now on`；之后屏幕照常画、按键照常有反应，渲染器没有重启（[42](42-gpu-rendering.md) §7 的故意失败，这次在真的 GPU 上） |
| 翻页没被拒 | 上面几项做完以后 `grep 'page flip' /run/phone/gui.log` | 什么都没有；有的话是第一次被拒时内核看到的（`likely:` 几行，[42](42-gpu-rendering.md) §4.3），照 §14.4 再查 |
| `phone-paint --bench`：GPU 的数字和画面 | 用几分钟以后：`systemctl stop phone-display; /usr/lib/phone/renderer --bench /var/lib/phone/frames.jsonl --size 1080x2180@4.5 > /var/lib/phone/bench.txt; systemctl start phone-display`（1080×2180：面板去掉上下各 80 行暗区；[37](37-motion.md) §5 第 6 条） | `gpu:` 那行是 Adreno；最后一行说 GPU 每步都正好是拆开的图层之和、和 CPU 路径最多差 2 级（超过就让这次运行失败） |

把 `/run/phone/gui.log`、`/run/phone/steps.jsonl`、`/var/lib/phone/bench.txt` 和 `journalctl -b -u phone-display` 发回来。删掉 `gpu.conf`（还有每帧都在变大的 `frames.jsonl`）就回到 CPU 路径。

**门槛：量出来再定（`$W perf`，未上真机）。** 6T 现在在 CPU 上画 1080×2340，机主觉得不够流畅；走哪条路照下面三条门槛定，数字从 `$W perf` 来：

- 在产品的屏幕大小上（480×640，放在 6T 面板正中），帧时间（画加上交给显示）的 p95 ≤ 16.7 ms；
- 按键到上屏、触摸到上屏的 p95 ≤ 50 ms：软件这一段，从内核给输入事件打的时间戳到显示它效果的那次翻页完成（[23](23-renderer-protocol.md) §3.1）；
- 每个应用冷启动（进程启动到第一页上屏，和 Shell 日志里 `ready <n>ms after start` 一样从进程启动算）≤ 300 ms。

phone-paint 一直在量（每帧几次读时钟，[23](23-renderer-protocol.md) §3.1）。`$W perf` 让它放一套固定的操作（`tools/perf/gate.play`，`PHONE_PAINT_PLAY`：打开主菜单、滚联系人、打开短信、用九宫格打一条短信再删掉、进出设置的几层、回待机、图库），CPU、`PHONE_PAINT_GPU=1`、`PHONE_PAINT_GPU=raster` 三条路各在整个面板上和产品的屏幕上（`PHONE_PAINT_TARGET=480x640`）跑一次，一共六次。每次都经 `/run` 下的 drop-in（`kario-perf.conf`）重启 `phone-display`，应用都是冷启动；跑完删掉 drop-in、照原样再起 `phone-display`。最后排成一张表，写明过没过：

```
$W push                                   # 套件带上 perf.play
$W perf                                   # 每次一两分钟；--perf cpu-target,compose-target 只跑这两次
$W collect                                # 取回 build/phone/oneplus-fajita-t9/results/<这次>/
```

前提：手机没设锁屏密码、屏幕开着（操作从待机界面开始）。按键和触摸是渲染器自己放的，从放出的时刻算，不含内核和蓝牙键盘那一段，也不用接键盘。结果在手机的 `/var/lib/kario-test/results/<这次>/`：`perf.txt` 是表，`perf-<路>-<大小>.json` 是每次的数字（[23](23-renderer-protocol.md) §3.1），`perf-<路>-<大小>.log` 是那次的 `journalctl -u phone-display`，`times.txt` 有每次的秒数。表的每一行一次：`drawn` 是实际走的路（要了 GPU 却没起来时写 `cpu`）和画面大小；`frame ms`、`key ms`、`touch ms` 是 p50/p95/p99；`late/drop` 是动效里晚了、丢了的帧；`start ms` 是最慢的冷启动；`gate` 是这一行过没过。过不了的项、没放完的操作另起一行说明，`gl:` 一行是 GL 的渲染器（应该是 Adreno，不是 llvmpipe），最后一行 `PASS` 或 `FAIL`（退出码 0 或 1）。没有样本的项（比如没有触摸屏的板子）算没量到，这一行不算过。表和 JSON 记到 §16 新的一节（日期、提交、镜像），数字填进 [42](42-gpu-rendering.md) §10 的表；照它们定 `paint_gpu`（上一段）。

平时用手机时也能看：`phone-shell --dump` 让渲染器把从这次 Shell 启动以来的数字（真的按键和触摸，带内核的时间戳）写进 `/run/phone/paint-stats.json`，`/usr/lib/phone/renderer --stats` 排成表。只想换个大小看一眼：照上面 `gpu.conf` 那样在 drop-in 里加 `PHONE_PAINT_TARGET=480x640`（或 `640x480`；`@2` 是缩放，默认 2；比面板大时不管它，日志里说一句）。chroot 的套件也有这一步（`$K perf`，Shell 照 `shell-start` 那样起），GPU 固件的问题见 §14.4。

开发机：`tools/perf/host.sh`（先 `make build`、`make paint`）跑同样的六次：Shell 的设备模式，phone-paint 无头（`PHONE_PAINT_HEADLESS=1`），像面板那样画、没有翻页，上屏是画完的时刻，GPU 是这台机器的（常是 llvmpipe），表和 JSON 在 `build/perf/`。它只证明这一套能跑、数字齐全，数字不代表手机。QEMU 的整机镜像上：把 `tools/phone-chroot/on-phone.sh` 和 `tools/perf/gate.play`（改名 `perf.play`）放进 `/var/lib/kario-test/kit/`，`KARIO_TARGET=whole sh /var/lib/kario-test/kit/on-phone.sh perf`（**未验证**：没在 QEMU 上跑过）。放另一套操作：`$W --play 文件 perf`（套件先带上它）、`tools/perf/host.sh --play 文件`；和安卓按键机比较时放的是 `tools/android-compare/tasks.play`（[40](40-testing.md) §8）。

**每一项都过了以后**（要 `raster` 的还要 §14.4 的 L2 那几项），把它定成这块板子的默认，不再靠 drop-in 或 `paint-gpu.yml`：`sku/boards/oneplus-fajita.toml` 里改成 `paint_gpu = "compose"`（或 `"raster"`），注释里写上日期和量到的数；`tools/sku_test.py` 的 `test_paint_gpu` 现在要求所有 SKU 默认都不开，跟着改；[42](42-gpu-rendering.md) §3、§10 和 [37](37-motion.md) §5 第 8 条说「所有板子都是 `off`」的地方一起改。然后 `make sku-gen SKU=oneplus-fajita-t9`、照 §4 构建、§6 刷入；chroot 的套件由 `tools/sku.py files` 生成 `sku.json`，`$K push` 带过去。`PHONE_PAINT_GPU=0` 仍然能在一台手机上关掉它（环境变量优先）。

### 8.2 起不来时：失败那次的日志和 ABL 的命令行

2026-11-24 那台 6T 上就是这样查出来的（§1），下次起不来照做。除了第 3 步的 `set_active`（只改 GPT 里槽的属性）都只在内存里跑，不写手机。先在电脑上打一个 `test-kb.img`：Kario 的内核加 Nura 的 initramfs，命令行用 Kario 自己的。Nura 的 initramfs 不管 `root=`，起来进它的调试 shell，所以 Kario 的根文件系统起不来时它也起得来。要 Nura v26.06 给 6T 的启动镜像（`~/kario-images/nura-boot.img`）和 AOSP 的 `unpack_bootimg`、`mkbootimg`（构建里的 `mkbootimg-native`，§4）：

```
unpack_bootimg --boot_img boot-oneplus-fajita.img --out kario   # kario/kernel：Image.gz 接 6T 的设备树
unpack_bootimg --boot_img nura-boot.img --out nura               # nura/ramdisk：Nura 的 initramfs
mkbootimg --header_version 0 --kernel kario/kernel --ramdisk nura/ramdisk \
  --cmdline 'root=PARTLABEL=userdata console=ttyMSM0,115200 phone.gui=1 console=tty0' \
  --base 0 --pagesize 4096 --kernel_offset 0x8000 --ramdisk_offset 0x01000000 \
  --second_offset 0x00f00000 --tags_offset 0x100 -o ~/kario-images/test-kb.img
```

1. 看现象，记下从开机到出事的秒数和屏幕上的字（拍照）：
   - 屏幕上有内核的字和 `phone-initramfs:` 几行，过十几秒重启，一遍又一遍：initramfs 没找到或没挂上 userdata，最后几行写着为什么（也在 pstore 里）。重启时按住音量上进 fastboot（槽没标过“成功”时，ABL 数完次数也会停在 fastboot）。
   - 十几秒就回到 fastboot：2026-11-24 那次就是这样（17 秒），自己挂根的内核挂上了 ABL 给的 Android 分区、跑了 Android 的 init（§1）。带 initramfs 的镜像不该再这样：取 pstore（第 2 步）。
   - 内核的字停在 `Waiting for root device`：那是不带 initramfs 的旧镜像（`fajita-flash.sh` 现在拒绝它）。
   - systemd 起来了、Kario 的界面没出来：已经过了挂根这一层，照 §7 连 USB 控制台。
2. 失败那次的内核日志和 ABL 的命令行：**不要断电**（ramoops 在内存里，热重启才留得住），回到 fastboot 后紧接着 `fastboot boot ~/kario-images/test-kb.img`，在它的调试 shell 里（`telnet 172.16.42.1 23`）：

   ```
   mount -t pstore pstore /sys/fs/pstore 2>/dev/null; ls -l /sys/fs/pstore
   for f in /sys/fs/pstore/*; do echo "== $f"; tail -c 60000 "$f"; done | sed 's/androidboot.serialno=[^ ]*/androidboot.serialno=<serial>/g' > /tmp/pstore.txt
   sed 's/androidboot.serialno=[^ ]*/androidboot.serialno=<serial>/' /proc/cmdline > /tmp/cmdline.txt
   ```

   `console-ramoops-0` 是失败那次的控制台：`Kernel command line:`、`phone-initramfs:` 那几行、最后是怎么结束的。2026-11-24 那次它有 55 562 字节，ECC 改正了 5 个字节、434 块改不回来，有乱码，可 `Kernel command line:`、`Run /bin/init` 和 Android 的 init 的报错都读得出来。`dmesg-ramoops-*` 只在 oops、panic 以后才有（内核是 `CONFIG_PANIC_TIMEOUT=0`：panic 以后停住，不自己重启）。`ls` 是空的：要么那次 Kario 的内核根本没跑，要么 ramoops 没留下来（长按电源键的复位会不会清掉它**未核实**）。`/tmp/cmdline.txt` 里 Kario 那一段后面的都是 ABL 加的（2026-11-24 的结果在 §1）；新启动镜像的 initramfs 也把整条命令行写进日志（`command line:` 那一行）。两个文件照 §9.1 的办法用 `nc` 传回电脑。系统起得来时同样的东西在 `bringup-log -a` 里。
3. `fastboot boot` 什么镜像都是 `Load Error`：多半是 ABL 把槽标成了不可启动（连续失败用完了次数）。`fastboot getvar current-slot`，再 `fastboot getvar slot-unbootable:a` 和 `fastboot getvar slot-retry-count:a`（当前槽是 b 就问 b）；是 `yes` 和 `0` 就 `fastboot set_active a`：ABL 清掉标记、给它 7 次，`fastboot boot` 又能用（2026-11-24 就是这样恢复的）。`fajita-flash.sh` 的 `test`、`flash`、`restore` 每次都先把当前槽设为活动。
4. QEMU 上的对照（`make yocto-userdata-test`，2026-11-24 在构建机上跑过，2026-11-25 又跑过，约 3.5 分钟）：4096 字节扇区的 SCSI 盘上摆着 6T 的分区名（`boot_a/b`；`system_a/b` 上没有文件系统；`userdata` 是镜像的根文件系统，加上 6T 的 `/init` 链接和 fstab 的 `/` 行），命令行末尾照那次的样子模拟 ABL：`androidboot.*` 里夹着指向这块盘的 `system_a` 的 `root=PARTUUID=…`；另一次是不带 `root=` 的命令行，再加上 `skip_initramfs rootwait ro init=/init androidboot.slot_suffix=_a`。经 initramfs 两次都找到 `/dev/sda5`、读写挂上，systemd 按 fstab 的 `/` 行把文件系统长满分区，自测的第一个用例过了；不带 initramfs（以前的启动镜像）时，没有追加的那次内核自己按 `root=PARTLABEL=userdata` 挂上、自测也过了，追加了的那次内核去挂 `system_a`，`VFS: Unable to mount root fs`（这块盘上它是空的；手机上是 Android 的系统，跑起了它的 init），这次开机在内核 panic 时就结束。

### 8.3 内核模块：lockdown 下只加载签了名的

Kario 的内核都强制 lockdown（integrity，`phone-lockdown.cfg`；6T 的基础配置是 Nura 的，本来就这样，原来 Kario 的片段没有定下它）：内核只加载带签名、签名的密钥的证书它自己带着的模块。模块在安装那一步签名（先去掉调试信息：打包不动签了名的模块），用构建目录的密钥 `$WORK/ota-keys/kernel-modules.pem`：`yocto/ota-keys.sh` 生成（`make ota-keys`，`make yocto-build` 先跑它），不进 git；`phone-kernel.inc` 把它放到 `certs/kario-modules.pem`（`CONFIG_MODULE_SIG_KEY`），内核编进去的就是它的证书。

2026-11-24 第一次整机开机（`d4856a3` 的镜像，[41](41-design-gaps.md) §18）一个模块也没加载，每次都是 `Lockdown: modprobe: unsigned module loading is restricted`：没有 RTC、电池、充电器、声卡；`qrtr` 是模块，`tqftpserv` 报 `socket(AF_QIPCRTR): Address family not supported`，调制解调器的 remoteproc 也是模块，`rmtfs` 报 `Failed to get rprocfd`；`uim-select` 每两秒要一次 qrtr、等满 60 秒，排在它后面的 Shell 晚了一分钟才上屏；「VPN（设备）」的 `netlink receive: no such file or directory` 也是它：Shell 的按应用防火墙要 `nft_ct`，加载不了时 nf_tables 回 `ENOENT`（WireGuard 本来就编进了内核）。原因（读构建机上的文件，**实测**）：1094 个模块都签了名，签名的却是 bt-work 那次构建时内核自己生成的密钥（序列号 `53:01:12:ad…`），刷进去的内核是 fajita-work 编的，只带它自己生成的那把（`5b:c1:aa:4a…`）。两个构建目录共用 sstate：fajita-work 的模块包（`do_package_write_rpm`）是从 bt-work 留下的 sstate 取的，启动镜像却用 fajita-work 自己编的内核做；内核自己生成的密钥每次构建都不一样，又不在任务签名里，sstate 分不出来。内核把「没签名」「没有这把密钥」「算法不认识」报成同一句话，所以看起来像没签名。打包没有去掉签名（OE 不 strip 签了名的模块）。

现在（构建过、QEMU 上验过，整机**未上真机**）：

- 密钥固定在构建目录里，它的校验和进了内核任务的签名：密钥不同，sstate 里就是不同的内核和模块，不会再配错。代价：各个构建目录的密钥不同，内核（QEMU 的也是）不再在构建目录之间共用 sstate，每个构建目录自己编一次。
- 镜像构建时核对（`phone-modsig.bbclass`，`files/phone-modsig-check.py`）：根文件系统里每个 `.ko` 都在启动镜像里那个内核的版本目录下、带签名、用构建目录的密钥签的、验得过，那个内核也带着这把密钥的证书；不然 `do_image_qa` 失败（`modules-signed`）。`d4856a3` 的镜像过不了：1094 个模块都是 `signed by a key the kernel does not have`。`yocto/modsig-test.py` 测它（`make test`、CI）；`scripts/yocto-check.py` 另外拦住让内核自己生成签名密钥的配置。
- 编进内核的（`phone-fajita.cfg`）：6T 一直有、用户态一开始就要、又不带固件的。IPC 路由 `qrtr`、它到 DSP 的通道 `qrtr-smd` 和 DSP 启动时要问的 PD mapper（rmtfs、tqftpserv、基带的 QMI 客户端一起来就开 qrtr 套接字，是模块时每开一个都要加载一次）；PMIC 的 RTC `rtc-pm8xxx`（内核在用户态之前按它设时钟，`hctosys` 是 1，phone-clock 不用等）；电量计 bq27411（`bq27xxx_battery` 和它的 I2C 部分）、PMI8998 的充电器（`qcom_smbx`）和它读的 ADC（`qcom-spmi-rradc`），Shell 一起来电池就在。
- 照旧是模块（签了名）：要从根文件系统加载固件的，调制解调器和各个 DSP（remoteproc）、IPA、Wi-Fi（ath10k）、视频（venus）、声卡：编进内核的话，它们会在根文件系统挂上以前、从 initramfs 里要固件，只要一次。还有用到时才加载的 `nft_ct`、NAT（Shell 的防火墙、热点，nf_tables 自己加载）。WireGuard 本来就编进了内核（`phone-vpn.cfg`）。
- 查（整机）：`cat /sys/kernel/security/lockdown` 是 `[integrity]`；`/usr/libexec/phone/module-check.sh` 重新加载一个模块（默认 `nft_masq`）和它依赖的，内核验过签名（没有 `E` 污染），它的副本后面多一个字节（内核找不到签名）就被拒绝（`Operation not permitted`），打出 `PHONE-MODULES: PASS`，模块留成原来的样子；`modinfo -F signer nft_masq` 是 `Kario kernel modules (development)`。QEMU 的开机自测每次先跑它（`make yocto-selftest`）。
- QEMU 的内核（linux-yocto 6.18）原来既没有模块签名，也没有 lockdown，连 securityfs 都没有（读不出 lockdown 的级别，`module-check.sh` 第一次就因此失败）；`phone-lockdown.cfg` 给所有 Kario 的内核，QEMU 的开机自测现在也在 lockdown 下跑（2026-11-25：`PHONE-MODULES: PASS`，整套自测通过）。

## 9. 回到 Android

Kario 只写了 `dtbo_a/b`、`boot_a/b` 和 `userdata`；`system`、`vendor` 和各固件分区还是 OxygenOS 的（A/B 布局另写了 `system_a/b`，[43](43-phone-partitions.md) §3.1）。回去的办法，从轻到重：

- `yocto/fajita-flash.sh restore`：把 §3.5 备份里两个槽的 `boot`、`dtbo` 写回去，把当前槽设为活动（Kario 起不来时 ABL 可能已经把它标成不可启动，那样 OxygenOS 也不会启动），`fastboot -w` 清空 userdata，重启进 OxygenOS（清空后第一次开机要几分钟）。备份是装了 Nura 或 Kario 以后才做的（`phone.txt` 里 `stock-boot=no`），或者备份里缺 `boot`、`dtbo`（手机那时已经丢了它们，§9.1：脚本核对备份时说一句「restore will want --stock」），就从 OxygenOS 11.1.2.2 完整包里取 `boot.img`、`dtbo.img`（包里是 `payload.bin`，用 payload-dumper 一类工具解开），`--stock 目录` 用它们。写任何东西以前，脚本先核对要写回的四个文件：`boot` 是 Android 的启动镜像而且不是 Kario 或 Nura 的，`dtbo` 开头是 Android 设备树覆盖层表的标记（`d7b7ab1e`，擦过的分区拷出来的没有）；缺了或不对就停下、说要 `--stock`，一个字节也不写。`--stock` 的两个文件也照样核对。刷过 A/B 布局的（备份目录里 `kario.txt` 写着 `layout=ab`）一定要 `--stock`，目录里还要有同一个包的 `system.img`：脚本把它写进 `system_a`、`system_b`（写之前核对大小）。
- IMEI 没了、指纹录不进（MSM 工具或别的东西动了那几个分区）：`yocto/fajita-flash.sh restore-efs twrp-fajita.img`，经 TWRP 把备份里的 `persist`、`modemst1`、`modemst2`、`fsg`、`fsc` 写回去，每个写完再读一遍核对。TWRP 起不来时改在 Nura 的调试 shell 里写回，见 §9.1。
- 进不了 fastboot：MSM 下载工具（§3.4），重新解锁（§3.3：MSM 工具会重新上锁），然后 `restore-efs`（或 §9.1）。

不要做的：手机上不是原厂系统时**不要重新上锁 bootloader**（`fastboot oem lock` / `flashing lock`），会锁死成砖；不要 `fastboot erase` 或刷写 `modem`、`persist`、`modemst*`、`fsg`、`fsc` 这些分区（脚本只碰 `dtbo`、`boot`、`userdata`，A/B 布局另加 `system`；这些分区在脚本的禁写名单里，`restore-efs` 只在核对过的备份上写回）。

`restore`、`restore-efs` 对着模拟的手机测过（`yocto/fajita-flash-test.py`），**未上真机**。

### 9.1 一台 6T 上的实际记录（2026-11-21）

一台国行 6T（A6010，OxygenOS 11.1.2.2，`ONEPLUS A6010_41_210805`）在另一台电脑上走了一遍准备和备份。序列号、IMEI 这类只属于这台手机的号码不写进文档，下面写作 `<序列号>`。和上面说的不一样的地方：

- **TWRP 用不了**：TWRP 3.7.0_9 能 `fastboot boot`，可在 OxygenOS 11 上起来后崩溃，第一次直接进了 EDL（USB 上是 `05c6:900e`，QUSB_BULK）；TWRP 3.6.2 和 LineageOS 22.2 的 `boot.img` 被 bootloader 拒绝（`Failed to load/authenticate boot image: Load Error`；那时 `dtbo_a`、`dtbo_b` 已经擦了，是不是这个原因没有查清）。所以这台手机上 §3.5 的 `backup TWRP.img` 和 §9 的 `restore-efs TWRP.img` 都**用不了**；别的 TWRP 版本没有试。
- **`fastboot fetch` 不支持**（`Unable to get max-fetch-size`）：不启动另一个内核，就读不出分区。
- **能用的是 Nura 的启动镜像**：`fastboot boot` Nura v26.06 给 6T 的 `boot.img`（主线内核 `7.1.0-rc1-sdm845`，头版本 0），只在内存里跑，进了 postmarketOS initramfs 的调试 shell：USB 网卡上手机是 `172.16.42.1`，电脑分到 `172.16.42.2/24`，telnet 23 端口，root，没有密码。在这个 shell 里用 `dd` 读分区、`nc` 传到电脑，做完了备份；同样的办法能写回去。userdata 上装着 Nura 时，同一个启动镜像起来的是 Nura 本身，用它的 SSH 也一样（**未试过**）。
- 这个 initramfs 里没有 `/dev/block/by-name/`，分区是内核编号的 `sdX`，这台手机上是：`persist`=`/dev/sda2`、`modemst1`=`/dev/sdf2`、`modemst2`=`/dev/sdf3`、`fsg`=`/dev/sdf4`、`fsc`=`/dev/sdf5`、`boot_a`=`/dev/sde11`、`boot_b`=`/dev/sde39`、`dtbo_a`=`/dev/sde18`、`dtbo_b`=`/dev/sde46`。编号可能随内核变：每次读写以前先核对 `grep PARTNAME /sys/class/block/<名字>/uevent`。每个 LUN（`sda`…`sdf`）的 GPT 是开头 6 个 4096 字节的扇区（`dd bs=4096 count=6`，和脚本拷的一样）。
- **这台手机现在**：`boot_a` 是 Nura 的启动镜像；`boot_b` 还是 OxygenOS 原厂的（备份成了 `boot_b.img`）；`dtbo_a`、`dtbo_b` 已经擦了（装 Nura 要擦），**原厂的 dtbo 没有备份**，回 OxygenOS 时从 OxygenOS 11.1.2.2 完整包的 `payload.bin` 里取。
- **备份在哪**：那台电脑的 `~/kario-phone-backup/oneplus-fajita-<序列号>/`，在仓库外面；每个分区的 SHA-256 都和手机上的对过。手机的备份、它的固件和钥匙都**不放进仓库**（也不在仓库里另建 `device/` 一类的目录）；再拷一份到别的盘或网盘。

手工做的备份要让脚本认（`test`、`flash`、`restore` 和 chroot 套件的 `push` 都先核对它，§3.5）：文件名照脚本的来，一定要有的是这台手机自己的五个分区 `persist.img`、`modemst1.img`、`modemst2.img`、`fsg.img`、`fsc.img`；`boot_a.img`、`boot_b.img`、`dtbo_a.img`、`dtbo_b.img` 只放还是原厂的（这台手机是 `boot_b.img`），缺的脚本只提醒一句（`the backup has no boot_a.img … restore will want --stock`），照样刷；GPT 是 `gpt-sda.bin`…`gpt-sdf.bin`；`phone.txt` 里有 `serial=<序列号>` 一行（`fastboot getvar serialno` 报的那个）；这台手机的 `boot_a` 已经不是原厂的，所以写 `stock-boot=no`；最后在这个目录里 `sha256sum *.img *.bin > SHA256SUMS`。擦了以后才拷的 `dtbo_a.img`、`dtbo_b.img` 是擦过的内容，不要放进去（`restore` 也认得出来：没有覆盖层表的标记就拒绝）；`stock-boot=no` 让 `restore` 一定要 `--stock`。2026-11-24 那位测试者照这样做了备份，`flash` 前的核对和演练都过了（那时的脚本是他们自己改的，现在的脚本照这里说的做，`yocto/fajita-flash-test.py` ✓）。

在调试 shell 里备份一个分区（电脑先听，手机再发；两边的 `sha256sum` 要一样）：

```
nc -l -p 9999 > persist.img                       # 电脑上（OpenBSD 的 nc 写 nc -l 9999）
grep PARTNAME /sys/class/block/sda2/uevent        # 手机上：PARTNAME=persist
dd if=/dev/sda2 bs=4096 | nc 172.16.42.2 9999     # 手机上
sha256sum /dev/sda2                               # 手机上；电脑上 sha256sum persist.img
```

**回 OxygenOS（这台手机）**：从 OxygenOS 11.1.2.2 完整包的 `payload.bin` 里取出 `boot.img` 和 `dtbo.img`（payload-dumper 一类的工具），放进仓库外的一个目录。备份照上面的样子让脚本认了，就 `yocto/fajita-flash.sh restore --stock <那个目录>`：两个文件写进两个槽，`fastboot -w`，重启。脚本不认这份备份时，同样的事手工做（`boot_b.img` 是原厂 OxygenOS 的 boot，也能代替包里的 `boot.img`）：

```
fastboot flash boot_a boot.img && fastboot flash boot_b boot.img
fastboot flash dtbo_a dtbo.img && fastboot flash dtbo_b dtbo.img
fastboot -w
fastboot reboot
```

**写回 EFS（IMEI、指纹和传感器的校准），不用 TWRP**：只在这几个分区真丢了时做（没有 IMEI、指纹录不进，§9）。在 Nura 的调试 shell 里写，不在开着的 Kario 或 Nura 里写：那里 rmtfs 开着 `modemst1`、`modemst2`、`fsg`、`fsc`，正替基带读写它们。

```
fastboot boot ~/kario-images/nura-boot.img   # Nura v26.06 给 6T 的启动镜像，只在内存里跑
telnet 172.16.42.1 23                        # USB 网卡起来、电脑有了 172.16.42.2/24 以后
```

每个备份写回同名的分区，**只写** `persist`、`modemst1`、`modemst2`、`fsg`、`fsc` 这五个：先传到手机的 `/tmp`（电脑上 `nc -l -p 9999 < persist.img`，手机上 `nc 172.16.42.2 9999 > /tmp/persist.img`），`sha256sum /tmp/persist.img` 和备份的 `SHA256SUMS` 对上，核对目标分区的 `PARTNAME`，`dd if=/tmp/persist.img of=/dev/sda2 bs=4096`，再 `sha256sum /dev/sda2` 读回来核对，最后 `sync`。

**从 EDL 出来**：手机停在 `05c6:900e` 时，电脑这边的 Sahara、Firehose、USB 复位都没用；**拔掉 USB 线，按住电源键 40 秒**（不行再按住电源 + 音量上 20 秒），手机就复位了。备份做完以前不要拿 MSM 下载工具（§3.4）脱困：它重写 `persist`、`modemst*`、`fsg`，那时它们是唯一的一份。

这一节是那台电脑上的记录，命令照这个仓库的脚本核对过。回 OxygenOS 和写回 EFS 在这台手机上都还**没做过**。

## 10. 组成与许可

| 部分 | 来源（固定的版本） | 许可 |
|------|------|------|
| 内核 `linux-sdm845` | gitlab.com/sdm845-mainline/linux 标签 `sdm845-7.1-rc1-r0`（85f1df2a）；Nura pmaports 02c676bc 的 `config-postmarketos-qcom-sdm845.aarch64` 做基础配置（构建时下载）；Kario 的 `phone-*.cfg` | GPL-2.0-only |
| 固件 `firmware-oneplus-sdm845` | gitlab.com/sdm845-mainline/firmware-oneplus-sdm845（3e31a0c3，Nura 的 19 版装的那些文件），加 linux-firmware 的 Adreno 630、WCN3990、蓝牙文件 | `CLOSED`：构建时下载，不进 git；装了它的镜像只给机主自己测试，不能发布或转给别人 |
| 声音配置 `alsa-ucm-conf-sdm845` | gitlab.com/sdm845-mainline/alsa-ucm-conf（1b8d290e）里一加 6/6T 的文件，放在 alsa-ucm-conf 旁边；6T 的 `Voice Call` 后面接上 Kario 的通话扬声器 `Speaker1`（`fajita-voicecall-speaker.conf`，[20](20-audio.md) §10） | BSD-3-Clause（Kario 补的设备 Apache-2.0） |
| 通话声音 `q6voiced` 0.3.1 | gitlab.nura.eco/nura/q6voiced（21061751） | MIT |
| IMS 数据（VoLTE）`81voltd` 1.2.0 | gitlab.nura.eco/modem/81voltd 标签 `v1.2.0`（7c0cd944），Kario 自己的 systemd 单元（§11.1）；只装进板子写了 `ims` 的 SKU | GPL-2.0-or-later |
| `qrtr` 1.2、`rmtfs` 1.3、`tqftpserv` 1.2 | meta-qcom wrynose（c791f2b7，另加它要求的 meta-lts-mixins wrynose/linux-firmware d0fd372d） | BSD-3-Clause |
| ModemManager 1.24.2、libqmi 1.38（开了 QRTR） | meta-oe | GPL-2.0+ / LGPL-2.1+ |
| Mesa 26.0.5：EGL、GLES 2、GBM，gallium 的 freedreno 和 softpipe，给 phone-paint 的 GPU 路径（§8.1） | openembedded-core wrynose（[42](42-gpu-rendering.md) §5） | MIT |
| `mkbootimg-native`（只在构建机上） | android.googlesource.com/platform/system/tools/mkbootimg（d2bb0af5） | Apache-2.0 |
| `phone-qcom`：`phone-abslot`、`phone-btaddr`、`uim-select`、`pdc-select`（按 SIM 卡激活基带里的运营商配置，§11.1）、phone-audiod 的 UCM 参数（`PHONE_AUDIO_*`：放音和通话的 verb、设备、PCM） | Kario（kario-bsp 的 `meta-kario-qcom/recipes-bsp/phone-qcom`） | Apache-2.0 |
| `phone-bringup` | Kario | Apache-2.0 |

没有用的：qbootctl（GPL-3.0，Kario 的镜像不收 GPLv3，由 `phone-abslot` 代替）、hexagonrpcd（GPL-3.0+，所以没有传感器）、msm-modem 的 UIM 选择（GPL-3.0+，由 `uim-select` 代替）、bootmac（GPL-3.0+，由 `phone-btaddr` 代替）、callaudiod（GPL-3.0+，由 phone-audiod 的 UCM 通话路由代替）。Kario 的这些代替品是照它们要做的事重新写的，没有用它们的代码。

`scripts/license-check.py` 只管平台的 Go 模块和渲染器的 crate，不看镜像里装了什么；镜像的许可边界是 kas 里的 `INCOMPATIBLE_LICENSE`（不收 GPLv3），`CLOSED` 的固件不受它限制，所以发布时要自己挡住（见上一行）。

## 11. 还没做的

- 通话的声音：写好了，**未上真机**。phone-audiod 按声卡的 UCM 配置切换（[20](20-audio.md) §10）：开机后第一次启动跑配置的引导序列、设 `HiFi` + `Speaker`；telephonyd 有通话时（拨号中、对方振铃、接通、保持）换到 `Voice Call` + `Earpiece` + `Mic`，混音放到听筒的 `hw:0,0`；通话界面开免提时换成 `Speaker1`（配置里没有，Kario 补的，放到 `hw:0,2`）；静音时去掉 `Mic`；最后一路通话结束回到 `HiFi`，下一通从听筒开始；audiod 通话中重启会接着原来的设置。还缺：耳机（6T 没有 3.5 mm 插孔，主线 USB 只做设备，USB-C 耳机接不上；audiod 也不知道插孔状态）、蓝牙通话、免提的回声消除（主线 q6voice 没有 Android 的 ACDB 校准）。听筒、免提、静音和引导序列的音量都要在手机上听，见 20 §10 末尾的清单。
- VoLTE：镜像里有 81voltd 和按 SIM 卡激活基带配置的 `phone-qcom-pdc`，构建过，**未上真机**；状态栏的 VoLTE 标志和设置里的 IMS 注册状态做了，**未上真机**（§14.21）；VoLTE 开关、Wi-Fi 通话**未实现**，见 §11.1。
- 传感器（hexagonrpcd 是 GPL-3.0）、相机（vivid 代替）、屏下指纹、NFC、USB 主机（内核和设备树缺的见 §2 的键盘一条）、Dash 快充、马达。
- 提醒滑块：当声音开关读，定响铃、振动、静音三种模式，写好了，**未上真机**（§14.18）。
- A/B 系统槽、dm-verity 和 OTA（[25](25-system-update.md)）在默认的 userdata 布局里没有：userdata 只有一个根文件系统。A/B 布局（[43](43-phone-partitions.md)：根文件系统在 `system_a`、`system_b`，RAUC 经 `rauc-abl` 更新）写好了，镜像 2026-11-24 构建过一次、没开过机，**未上真机**（§15）。
- phone-paint 默认用 CPU 画（dumb buffer）；经 freedreno 在 Adreno 630 上合成（`PHONE_PAINT_GPU=1`）和栅格化（`=raster`）已配进镜像，构建**没跑**，**未上真机**（§8.1）。Nura 的内核加载 GPU 固件 `a630_sqe.fw` 失败（ELOOP，符号链接成环，[41](41-design-gaps.md)）：在那个底座上 GPU 起不来。

### 11.1 VoLTE（81voltd、运营商配置和 telephonyd 这边都做了，**未上真机**）

中国的运营商大多要 VoLTE 才能打电话（§2），这是 6T 当电话用的最大缺口。下面是 2026-11-18 读源码和公开讨论的结果，全部**未上真机**。骁龙 845 上 IMS 的协议栈（SIP、RTP、语音编码）跑在基带固件里，Linux 这边要做的是给基带选对运营商配置、替它连上 IMS 的数据网：

| 部分 | 做什么 | 现状 |
|------|--------|------|
| 运营商配置（MBN） | 基带按运营商打开 IMS、VoLTE 和它们的参数。文件在 `modem` 分区（vfat）的 `image/modem_pr/mcfg/configs/mcfg_sw/…`，Android 上由厂商的服务按 SIM 卡选，经 QMI PDC 装进基带 | Kario 的固件包和 Nura 的一样，不装 `modem_pr`；基带自己会不会经 tqftpserv 去读、读哪一个，**未知**。基带里已经有的配置（Android 装进去的，留在基带的持久存储里）由 `phone-qcom-pdc` 按 SIM 卡的运营商激活，把文件装进基带只手工做（下文），**未上真机** |
| IMS 数据（QMI 服务 770） | 基带请应用处理器为 IMS 开一个数据连接（APN 一般是 `ims`） | Nura 用 81voltd：收到请求就经 ModemManager 的 `CreateBearer`（APN、IP 版本、3GPP 配置号）开承载。Kario 的镜像也装了它（板子写了 `ims` 的 SKU，下文），构建过，**未上真机** |
| ModemManager | 在已有的上网承载之外再开一个（多路复用的 rmnet），带配置号 | 镜像里是 1.24.2，有 `profile-id` 和多路复用；QRTR 上开第二个承载**未验证** |
| telephonyd | 不把 IMS 承载当成移动数据，也不断开它 | 2026-11-25 做了，对着模拟的 ModemManager 测过，**未上真机**，见下 |
| 通话的声音 | VoLTE 通话也走 DSP 的语音通路，q6voiced 和 audiod 的 UCM 设置照用 | IMS 通话在 ModemManager 里是不是普通的 Call 对象，**未知** |

**81voltd**：gitlab.nura.eco/modem/81voltd 的 1.2.0，Alpine community 里有这个包，Nura 的 `soc-qcom-modem` 依赖它。许可是 **GPL-2.0-or-later**（LICENSE 是 GPLv2 原文，每个源文件都标 `GPL-2.0-or-later`），不是 GPL-3.0，可以进镜像。大约两千行 C，依赖 libqmi（`qmi-glib`）、ModemManager 的 `mm-glib` 和 libqrtr，meson 构建；三样在 Yocto 里都有（meta-oe、meta-qcom）。它自己写明的问题：停掉它不会断开它开的承载；ModemManager 的错误不告诉基带。加进它的 pmaports 合并请求说它是临时办法，长远要并进 ModemManager。读它的源码（`qvd-modem.c`、`qvd-server.c`）：它按端口 `qrtr0` 认 ModemManager 的 QRTR 基带，基带还没出来时请求先排着；`CreateBearer` 只给 APN、IP 版本和配置号（基带给了才给），不给 `apn-type`；承载被别人断开（飞行模式、射频重启）时它告诉基带连接断了，基带会再要。

**和 telephonyd 冲突的地方**（2026-11-25 改了，见 [04](04-ui-language-telephonyd.md)「别的程序开的承载」，对着模拟的 ModemManager 测过，**未上真机**）：

- 以前 telephonyd 把任何一个连上的承载都当成「移动数据已连接」：IMS 承载一连上，它就不再为上网开承载。现在按承载的 `BearerType`、`apn-type` 和 APN 认：81voltd 1.2.0 的 `CreateBearer` 只给 APN、IP 版本和配置号，不给 `apn-type`（读过它的源码），telephonyd 按这张卡的 IMS 接入点和通用的 `ims`、`sos` 认出它，不当数据。
- 以前关数据开关时调 `Simple.Disconnect("/")`，断开**所有**承载，IMS 的一起断。现在只断自己的数据承载（给它的路径）；飞行模式照旧由 `Modem.Enable(false)` 断掉所有承载（射频本来就要关）。
- `status` 的 `data_ifaces` 不再带 IMS 承载的接口，Shell 的防火墙关移动数据时不挡它。
- 还要注意：telephonyd 头一次给基带设 LTE 附着接入点时（[04](04-ui-language-telephonyd.md)「LTE 初始附着的接入点」），ModemManager 把射频关了再开，IMS 承载跟着断一次，81voltd 要能再开起来。

**镜像里的 81voltd**（2026-11-25，构建过，**未上真机**）：配方 kario-bsp 的 `meta-kario-qcom/recipes-bsp/81voltd/81voltd_1.2.0.bb` 取 gitlab.nura.eco/modem/81voltd 的标签 `v1.2.0`，提交固定为 7c0cd944（`SRCREV`）；许可的校验和核对 `LICENSE` 和 `main.c` 的 SPDX 行；meson 构建，`qmi_imsd.c` 用仓库里生成好的（不要 qmic）。它要的 glib、ModemManager 1.24.2 的 `libmm-glib`、libqmi 1.38（开了 QRTR）、meta-qcom 的 libqrtr 本来就在 6T 的镜像里。上游的单元写的是 `After=ModemManager.target`（没有这个目标，等于不排序），还装一个 OpenRC 脚本，两样都不要，换成 Kario 的 `81voltd.service`：排在 ModemManager、`phone-qcom-uim`、`phone-qcom-pdc` 之后，失败 5 秒后重启，设 `G_MESSAGES_DEBUG=all`（它记基带要了什么、开了哪个承载用的是 GLib 的 info，不设就不写）。只装进板子写了 `ims = "81voltd"` 的 SKU（[24](24-sku-framework.md) §2：`tools/sku.py` 把它写成 kas 覆盖里的 `PHONE_IMS`，机器 `oneplus-fajita` 按它装 `81voltd` 和 `phone-qcom-pdc`），现在只有 `oneplus-fajita-t9`；一加 6 的板子还没有 Yocto 机器，不写。81voltd 停掉或重启不断开它开的承载，基带再要时它另开一个：要清掉旧的就开关一次飞行模式（`Modem.Enable(false)` 断掉所有承载）。

**运营商配置：`phone-qcom-pdc`**（2026-11-25，对着模拟的 qmicli 测过，**未上真机**）：Android 上厂商的服务按 SIM 卡激活基带里这家运营商的软件配置（MCFG），IMS、VoLTE 的参数在里面。Kario 的 `pdc-select`（`phone-qcom` 配方，开机时的一次性单元 `phone-qcom-pdc.service`，排在 `phone-qcom-uim` 之后、ModemManager 之前，因为激活会让基带重启；`phone-qcom-uim` 失败（没插 SIM 卡）时不跑）：

1. `qmicli --nas-get-home-network` 读 SIM 卡的归属网络（MCC、MNC；SIM 卡还没准备好时 2 秒一次，最多 10 次，读不到就什么也不做），在 `/usr/share/phone-qcom/pdc-carriers` 里找这家运营商：中国移动（460 00、02、04、07、08、13）、联通（01、06、09）、电信（03、05、11）、广电（15），每家写着配置名字里用来叫它的词（`cmcc`；`cu`、`unicom`；`ct`、`telecom`；`cbn`）。表里没有的运营商什么也不做。
2. 这家运营商处理过（有 `/var/lib/phone-qcom/pdc-<MCC>-<MNC>`）就什么也不做：一家只选一次，之后手工换的配置一直留着。
3. `qmicli --pdc-list-configs=software` 列出基带里的配置（每个的名字和状态写进日志），挑名字里有这家的词、又不是实验室或测试版（名字里有 `lab`、`test`、`cta`、`ptcrb`）的：带 VoLTE 的（`volte`、`hvolte`）先，再是商用的（`commercial`）、给不锁网手机的（`openmkt`），都一样时取版本新的。一个也没有就什么也不做，也不记下。
4. 挑中的已经是 Active 或 Pending：记下 `kept`，不动。不是：先记下 `activated`（激活没成也不会每次开机再试、每次让基带重启），`qmicli --pdc-activate-config=software,<ID>`（60 秒没回答也往下走：基带可能已经在重启），等它在列表里变成 Active（Pending 不算：那时跑的还是旧的基带；最多等一分钟，一直是 Pending 就写明「基带下次启动时生效」），再跑一次 `uim-select`（基带重启以后 SIM 卡的会话要重开）。

它从不 `--pdc-load-config`（把文件写进基带的闪存）、`--pdc-deactivate-config`、`--pdc-delete-config`，不碰 platform 配置。`pdc-select --dry-run` 只说它会做什么；关掉它：`systemctl disable --now phone-qcom-pdc`；重新选一次：删掉 `/var/lib/phone-qcom/pdc-*`。最慢的一次（第一次插这家的卡、要激活）在 ModemManager 前面多等两三分钟，平常是读一次 SIM 卡的几秒。名字里用的词照的是 Quectel 模块上看到的中国三家的配置（`Volte_OpenMkt-Commercial-CMCC`、`OpenMkt-Commercial-CU`、`OpenMkt-Commercial-CT`）和高通 MCFG 的目录（`China/CT/Commercial/hVoLTE_OpenMkt`）；6T 的基带里有哪些、叫什么（一台 LineageOS 的 6T 上列出 25 个，有 `TGL_Comb_Attach-Lab-CMCC`，至少有中国移动的实验室版）要在手机上看（§14.6）。测试：kario-bsp 的 `tests/pdc-select-test.py`（`make test`、CI；假的 qmicli 照 libqmi 1.38 的输出答）。

**把配置文件装进基带只手工做**：基带里没有这家的配置时，要从 `modem` 分区的 `image/modem_pr/mcfg/configs/mcfg_sw/generic/china/…` 拷出 `mcfg_sw.mbn`、`qmicli --pdc-load-config` 装进去。这会写基带的持久存储（EFS），装错过的基带行为异常，只能由测试的人在做了 §3.5 的备份以后照 §14.6 做，Kario 不自动装。

**通话、短信、紧急呼叫经 IMS**（读 ModemManager 1.24.2 和 libqmi 1.38 的源码，**未上真机**）：

- 通话：ModemManager 经 QMI Voice 的 `Dial Call` 拨号，只给号码、不给呼叫类型，走 CS 还是 IMS 由基带按它的配置定；`All Call Status` 报所有的通话，不分域。VoLTE 通话在 ModemManager 里就是普通的 Call 对象，telephonyd、q6voiced（按 Call 的状态打开语音通路）和 phone-audiod 的 UCM 通话设置都不用改，声音照样走 DSP 的语音通路（IMS 的媒体在基带和 DSP 里）；按键音（`Call.SendDtmf`）也由基带发。Nura 上 6T 用 81voltd 打通 VoLTE 的记录走的也是这条路。
- IMS 注册状态：ModemManager 1.24 没有这个接口。telephonyd 经 phone-qcom-ims 的 `ims-status`（QMI 的 IMSA 服务）问基带，状态写进日志；4G、5G 上没响铃就结束的拨出，IMS 没注册时屏上说「通话失败：VoLTE 未注册」（§14.21，对着模拟的 ModemManager 和 qmicli 测过，**未上真机**）。最后问到的状态在 `status` 的 `ims` 里：在 4G、5G 上注册着、IMS 也注册了时，状态栏在网络类型左边写「VoLTE」，「设置 › 移动网络」最后一行写出注册状态（§14.21，**未上真机**）；高清语音（HD）的标志**未实现**。测试时也可以直接问基带，要先绑定（§14.21 第 2 步）；不绑定时 IMSA 只回 `InvalidOperation`（008）。
- 短信：ModemManager 经 QMI WMS 收发，不指定走不走 IMS；没有 CS 域时（只有 4G 的电信卡）短信是不是经 IMS 走，由基带和网络定，**未上真机**。
- 紧急呼叫：基带要紧急的数据连接（APN 一般是 `sos`）时也经 IMS 数据服务要，81voltd 照样开；中国三家在 4G 上的紧急呼叫怎么走**未上真机**，也不拨真的紧急号码去试（§14.1）。
- 设置里的 VoLTE 开关（安卓的「增强型 4G LTE 模式」）、Wi-Fi 通话、视频通话、通话中切到 2G、3G（SRVCC）：**未实现**。联通、电信关了 2G、3G，关掉 VoLTE 就打不了电话，所以先不给开关。

**手机到了怎么试**（按顺序，每一步都能退回）：

1. 先做 §3.5 的备份：装错 PDC 配置会让基带行为异常（Nura 的开发者自己碰到过），`modemst1/2`、`fsg` 的备份是退路。
2. 在 OxygenOS 里看这张 SIM 卡在 Android 上能不能 VoLTE；把 `modem` 分区里的 `image/modem_pr` 拷出来，看有没有中国运营商的目录（`…/generic/china/…`）。
3. 在 Nura 上（§13.4 的测试一）装上 81voltd：`qmicli -d qrtr://0 --pdc-list-configs=software` 看基带里有哪些配置、哪个在用，需要时 `--pdc-load-config` 装对应的 `mcfg_sw.mbn`、`--pdc-activate-config` 激活，然后打 VoLTE 电话。社区的记录：Nura 上 6T 和 Pixel 3a 用 81voltd 能打 VoLTE，有的运营商（澳洲 Telstra）不行；有人在 LineageOS 的 6T 上用 qmicli 换配置，德国的卡用「Volte_OEM_Lab」能通。
4. Kario 这边已经做了（81voltd 的配方、`phone-qcom-pdc` 按 SIM 卡的 MCC、MNC 激活基带里的配置、telephonyd 认出 IMS 承载）：整机上照 §14.6 末尾的「VoLTE」一步一步试，要核对 81voltd 开的承载在 `mmcli -b` 里的属性和 telephonyd 认的一样。

**工作量**：81voltd 的配方、telephonyd 区分承载、按 SIM 卡激活基带里已有的配置都做了；把配置文件装进基带（`--pdc-load-config`）有意不自动做；最难估的是运营商兼容性，只能拿真卡一家一家试。

**风险和未知**（没有手机都答不了）：

- OxygenOS 11.1.2.2 的 `modem_pr` 里有没有中国移动、联通、电信的 VoLTE 配置，Android 装进基带的有哪几个、叫什么（`pdc-carriers` 里的词对不对得上），基带会不会按 SIM 卡自己选。
- 装 PDC 配置会不会弄乱 EFS（配置存在基带的持久存储里）；激活一个已有的配置以后基带是不是自己重启、要多久，重启以后 rmtfs、tqftpserv 和 ModemManager 跟不跟得上。
- `--nas-get-home-network` 在 ModemManager 启用基带以前（基带还在低功耗）读不读得到 SIM 卡的归属网络。
- ModemManager 1.24 在 QRTR 上开第二个承载行不行，81voltd 给的配置号对不对。
- IMS 通话在 ModemManager 里是普通的 Call 对象（读源码是这样，见上），在手机上要核对；通话中切到 2G、3G（SRVCC）时怎么样。
- 紧急呼叫和经 IMS 收发的短信怎么走。

### 11.2 只读的 RTC：内核那边的办法（调研，不改内核）

6T 的 RTC 是 PM8998 里的（`pm8998.dtsi` 的 `rtc@6000`，`qcom,pm8941-rtc`，驱动 `rtc-pm8xxx`）。2026-11-23 读了 `linux-sdm845` 那个标签（`sdm845-7.1-rc1-r0`）的源码和 Nura 的配置（就是配方下载、校验的那一份）：

- 驱动设时间有三条路：设备树写了 `allow-set-time`，直接改 RTC 的计数器；有名为 `offset` 的 nvmem 单元（`nvmem-cells`、`nvmem-cell-names = "offset"`，一般是 PMIC 的 SDAM 里的 4 个字节），计数器不动，偏移写进那个单元，读时间时加上（30 秒以内的漂移攒到关机才写，省闪存）；有 `qcom,uefi-rtc-info`，偏移存在 UEFI 变量里（骁龙的 Windows 笔记本，和 Windows 共用一份）。三样都没有时 `set_time` 返回 `-ENODEV`。6T 的设备树（`pm8998.dtsi`、`sdm845-oneplus-common.dtsi`）三样都没有，`rtc@6000` 连标签都没有。
- Nura 的配置：`CONFIG_RTC_DRV_PM8XXX=m`（模块）、`CONFIG_RTC_HCTOSYS=y`、`CONFIG_RTC_SYSTOHC=y`（都是 `rtc0`）、`CONFIG_NVMEM_SPMI_SDAM=m`。RTC 注册时（模块由 udev 加载以后）内核按它的数设一次时钟，所以 `phone-clock` 等它（[28](28-battery.md) §6）；NTP 同步时每 11 分钟写一次 RTC，在只读的 RTC 上失败，不碍事。

**`allow-set-time`**：给设备树打一个补丁（节点没有标签，要按路径写），RTC 就能设，内核的 `hctosys`、hwclock、timedated 也就都对了。不用：
- 高通手机上基带和 TrustZone 的时间服务把 PMIC 的 RTC 当作单调的时基，Android 不写它，偏移由高通的 time-services 守护进程存在 `/data` 里（Android 内核的 qpnp-rtc 驱动只在设备树写了 `qcom,qpnp-rtc-write = <1>` 时才写 RTC）。这一条和 Linux 改了计数器、基带那边按它算的时间会不会跟着错，都**未核实**（没找到公开的说明）。
- PMIC 外设的访问权限是 TrustZone 给 SPMI 仲裁器配的：HLOS 能不能写 RTC 的写寄存器，在 6T 上**没试过**。

**nvmem 单元**（主线上 sc8280xp 的参考板 CRD 这样做：PMK8280 的 SDAM 6 里的 `rtc-offset@bc`；联想 X13s 这个标签里已经改用 `qcom,uefi-rtc-info`）：要一块 Linux 能写、别人都不用的 SDAM。PM8998 的设备树里没有 SDAM 的节点（这个标签的 `pm8998.dtsi` 里没有；SDAM 是不是新一代 PMIC 才有，**未核实**），哪几个字节空着只有高通的固件文档知道，写错了会改掉引导程序或基带存在那里的东西。不做。

**UEFI 变量**：6T 由 Android 的 ABL 引导，没有 UEFI 运行时服务，用不了。

**决定**：Kario 自己的 6T 内核（`linux-sdm845`，§10）不让 RTC 能设（不打设备树补丁，也不用偏移单元），RTC 照旧只读，偏移由用户态存（板子的 `[rtc] read_only = true`、`offset = true`，[28](28-battery.md) §6）：哪块板子都一样做，不靠固件里的空字节，也不碰基带的时基。以后哪块板子的内核给了能设的 RTC（设备树里有 `allow-set-time` 或偏移单元），把它的板子文件改成 `read_only = false`、去掉 `offset` 就行。另一个小改动 2026-11-24 做了：`phone-fajita.cfg` 写 `CONFIG_RTC_DRV_PM8XXX=y`（依赖的 `MFD_SPMI_PMIC`、`RTC_CLASS` 在 Nura 的配置里都是 `y`），RTC 在用户态起来之前就注册好，内核按它设时钟，`phone-clock` 不用等模块（§8.3；`linux-sdm845` 的配置检查确认它进了最终的配置，**未上真机**）。

## 12. 来源

- Nura 的 pmaports（gitlab.nura.eco/nura/pmaports，读的是 02c676bc，2026-11-17）：`device/community/device-oneplus-fajita`（deviceinfo：启动镜像格式、偏移、命令行、刷机方法），`linux-postmarketos-qcom-sdm845`（标签和配置），`firmware-oneplus-sdm845`（文件清单），`alsa-ucm-conf-sdm845`，`soc-qcom`（模块和服务的组合）。
- sdm845-mainline（gitlab.com/sdm845-mainline）：内核、固件、UCM，以及 pmtools 的安装说明（为什么要擦 dtbo）。通话路由读的是 alsa-ucm-conf 1b8d290e 的 `OnePlus/fajita`、`Xiaomi/beryllium` 和提交历史（`HiFi` 里也有 q6voice 路由的原因、`Mic1` 改名的原因），内核 `sdm845-7.1-rc1-r0` 的 `q6voice-dai.c`（有哪些语音混音控件、没有静音控件）。
- 通话：q6voiced 0.3.1 的源码（哪些 ModemManager 通话状态打开语音通路）；alsa-utils 的 alsaucm 和 alsa-lib 的 `src/ucm/main.c`（一次运行一个状态、换 verb 先拆旧设备、关没开的设备什么也不做）；OnePlus 6T 没有耳机插孔：主线设备树的提交说明（Caleb Connolly，2020）。
- VoLTE：81voltd 1.2.0 的源码和 Alpine 的 APKBUILD；Nura pmaports f2795e91（2026-11-18）的 `soc-qcom`、`firmware-oneplus-sdm845`；pmaports 的合并请求 !5387（81voltd）、!4674（`modem_pr` 文件）和 issue #1878（OnePlus 6 的 VoLTE）；openimsd.com 的「Activating Voice over LTE on Android」（6T 上用 qmicli 换 PDC 配置，那台 6T 的配置列表）。2026-11-25 加读：ModemManager 1.24.2 的 `mm-call-qmi.c`、`mm-broadband-modem-qmi.c`（拨号不给呼叫类型、所有通话一个列表）；libqmi 1.38 的 `qmicli-pdc.c`、`qmicli-nas.c`（`pdc-select` 解析的输出）、`qmicli-imsa.c`；Quectel EC20 上中国三家配置的名字（blog.inyes.net，2025）、高通 MCFG 的目录（jishuzhan.net，2026）、`mbn_ota.txt` 列出基带装哪些配置（telegra.ph 上 crok 的文章，2019）。
- linux-msm（github.com/linux-msm）的 qrtr、rmtfs、tqftpserv，Qualcomm 的 meta-qcom 里的配方。
- 高通 ABL 的命令行处理：SHIFTPHONES/android_bootable_bootloader_edk2（SHIFT6mq，骁龙 845）；一加的 ABL 不公开，它实际追加什么是 2026-11-24 在 6T 上读出来的（§1、§8.2）。Android 的 init 出致命错误时重启进 bootloader：AOSP `system/core` 的 `init/reboot_utils.cpp`（`init_fatal_reboot_target` 默认是 `bootloader`），那次的 ramoops 里就是 `InitFatalReboot`。ramoops 区：`sdm845-7.1-rc1-r0` 的 `arch/arm64/boot/dts/qcom/sdm845-oneplus-common.dtsi`。
- `fastboot getvar product` 在 6T 上报 `sdm845`：XDA 论坛上 6T 用户贴的 `getvar all`；没标“成功”的槽会退回 OxygenOS：一位在 6T 上装 Arch 的用户的记录。

## 13. 手机到了：一步一步

在你自己的 Linux 电脑上做，不用登构建机。哪一步的输出和这里说的不一样就停下，把输出发给我。两种测试（§1）用的是同一个套件 `tools/phone-chroot/kario-phone.sh`（[24](24-sku-framework.md) §6.2）、同样的步骤、同样的结果：测试一把这台手机自己的镜像放在 Nura 上的 chroot 里跑（§13.4），测试二把同一个镜像刷进去跑（§13.5，`--whole`）；不同的只是手机上跑的是什么。整个过程**未上真机**。

### 13.1 电脑

1. 装 `adb`、`fastboot`（android-tools）、`python3`、`zstd`、`ssh`、`git`。
2. 取代码：`git clone https://github.com/kario-os/kario && cd kario`（下面的命令都在这个目录里跑）。
3. 从构建机拷这台手机的两个镜像（固件是 `CLOSED` 的，只给你自己测，不要传到别处）。userdata 镜像是两种测试共同的根文件系统：`flash` 把它写进去，chroot 套件往手机送的时候边送边解开：

   ```
   mkdir -p ~/kario-images
   B=<构建机>
   rsync -aL --progress \
     $B:yocto/fajita-work/build/tmp/deploy/images/oneplus-fajita/boot-oneplus-fajita.img \
     $B:yocto/fajita-work/build/tmp/deploy/images/oneplus-fajita/phone-image-oneplus-fajita.rootfs.ext4.sparse \
     ~/kario-images/
   ```

   QEMU 的镜像（`phone-image-qemuarm64.rootfs.ext4.zst`）在 chroot 里照样能跑，拿来对比：二进制一样，但没有 GPU 的驱动，也没有 alsaucm 和声卡的配置，所以在它上面 GPU 路径、声卡的设置和通话路由都不是刷机后的样子。
4. 下载 6T 的 TWRP（§3.5，存成 `~/kario-images/twrp-fajita.img`）、Nura v26.06 给 6T 的启动镜像（存成 `~/kario-images/nura-boot.img`：TWRP 起不来时用它，§9.1）和 MSM 救砖包（§3.4，Windows 上用，先放着）。
5. 套件连 Nura 用的钥匙：`ssh-keygen -t ed25519 -f ~/.ssh/kario-board-test -N ''`（刷进去的镜像让 root 不用钥匙就能进，§7）。

### 13.2 手机：系统和解锁

6. 国行先刷成 OxygenOS，再本地升级到 11.1.2.2，两个槽都装一遍（§3.1、§3.2）。
7. 解锁 bootloader（§3.3，会清空手机）。

### 13.3 备份：第一次往手机写东西之前

8. 手机进 fastboot（关机后按住音量上 + 电源），插 USB 线：

   ```
   yocto/fajita-flash.sh backup ~/kario-images/twrp-fajita.img
   ```

   最后一行是 `backup in .../kario-phone-backup/oneplus-fajita-<序列号>` 才算完成，手机回到 fastboot。TWRP 起不来（崩溃、进 EDL、被拒绝）就停下：照 §9.1 从 EDL 出来，改在 Nura 的调试 shell 里备份。
9. 把 `~/kario-phone-backup` 整个再拷一份到别的盘或网盘（不放进仓库）。

### 13.4 测试一：Nura 上的 chroot

10. 按 Nura 的安装页刷 Nura（[24](24-sku-framework.md) §6.2 第 3 步：界面选 console，装 SSH；一加要 `fastboot erase dtbo`）。
11. 开机后 USB 线接电脑，手机在 `172.16.42.1`：把 `~/.ssh/kario-board-test.pub` 加进手机 root 的 `authorized_keys`（或者用默认用户，下面每条命令加 `--become doas`）。
12. 在 Nura 里用 `bluetoothctl` 配对蓝牙键盘。
13. 自动测试（要几个小时；先核对 §13.3 的备份，没有就不往手机推任何东西）：

    ```
    K="tools/phone-chroot/kario-phone.sh --sku oneplus-fajita-t9 --image $HOME/kario-images/phone-image-oneplus-fajita.rootfs.ext4.sparse"
    $K push setup selftest pixel collect
    ```

    `push` 会说这是板子自己的镜像（QEMU 的镜像也收，并说它缺什么）；`setup` 在手机的时钟比电脑慢 10 分钟以上时照电脑的设（6T 的 RTC 在主线上只能读，不联网开机是 1970）；`selftest`、`pixel` 一个用例失败后接着跑完（`--keep-going`），跑之前把扬声器调小（板子 `[audio]` 的 `quiet_controls`：`RX0 Digital`–`RX4 Digital` 到 15%），跑完调回原来的值（`--quiet 30%` 换个音量，`--quiet off` 不调，§14.5）；步骤前面加 `--only '<正则>'` 就只跑那些用例（§14.1）。每次的结果目录里 `kit.txt` 记着脚本的提交、镜像和镜像是从哪个提交构建的。和刷机后不一样的地方，`facts.txt` 在 `== chroot` 下面列出来：镜像里的 systemd 单元不跑（Shell 自己起服务，基带那一套由 Nura 跑：ModemManager、rmtfs、pd-mapper、tqftpserv、q6voiced）；内核和它的模块是 Nura 的；固件也是 Nura 的，除非步骤前面加 `--firmware image`，那样从 `setup` 起内核先到镜像的固件里找（GPU 的：Nura v26.06 的 `a630_sqe.fw` 加载不了，[41](41-design-gaps.md)）。声卡：Shell 自己起的 audiod 用镜像里的 alsaucm，照 `sku.json` 里板子的 `[audio]` 设好 `HiFi` 和 `Speaker`，和刷机后一样，放到 `plughw:0,2`（扬声器）；`teardown` 再拆掉（[20](20-audio.md) §10）。用 QEMU 的镜像时由 `setup` 用 Nura 的 alsaucm 设，Nura 也没有时它说声音放不出来（`apk add alsa-utils`）。声卡打不开时 audiod 照样走时间，`phone-audiod.log` 里留一行原因（[20](20-audio.md) §5）。只有刷机后的手机才有的，chroot 里的用例跳过，都写明原因（SKIP，原因在 `selftest.out` 和报告里）：「内核特性」（内核是 Nura 的，`require-own-kernel`，原因里列着它缺什么）、VPN（Nura 的总线上没有 systemd-networkd）、「恢复出厂设置（设备）」（chroot 里的 `systemctl` 什么都不回答，`require-systemd`）、要架测试接入点的用例（那里 Shell 不管 Wi-Fi，`require-wifi-ap`），以及探针的包从 Nura 的 Wi-Fi 出去时按应用流量的用例（`require-mobile-route`：移动数据的限制在 Wi-Fi 上不生效）。「后台任务」随它接着往下做的天气用例一起跳过（没有脚本能移动的位置接收机），只有这类用例调用的能力，兼容测试报告算作跳过并写明原因，不算从未调用。充电用例先拔掉模拟的充电器：插着线的手机本来就在充电。
14. 手动试：`$K shell-start`，屏幕上出 Kario，用蓝牙键盘打电话、发短信、连 Wi-Fi（通话时 Shell 的 audiod 把声卡切到 `Voice Call`，和刷机后一样）；`$K mem`；`$K shell-stop`。耗电：`$K power-start`，熄屏放 30 分钟，`$K power-stop`。一台 6T 上 `shell-start` 之后卡死过一次，先照 [41](41-design-gaps.md) §13 的隔离步骤；调试开关（bring-up switches，只在这一个 Shell 里有效、不存进设置）：`$K --stay-on --no-haptics shell-start`，Shell 不熄屏（背光和显示控制器一直开着，也就不休眠）、不振动。
15. 把结果发给我：`tar -czf ~/fajita-chroot.tgz -C build/phone/oneplus-fajita-t9 results`（`facts.txt` 里的序列号、基带的身份和硬件地址已经抹掉，结果仍放在仓库外，不要提交）。
16. 让 Nura 回到原样：`$K teardown`。

### 13.5 测试二：整机

17. 手机进 fastboot，先试一次（`boot` 和 `userdata` 都不写，§5）：

    ```
    yocto/fajita-flash.sh -d ~/kario-images test
    ```

    看屏幕上有没有内核的输出和 `phone-initramfs:` 那几行（§1），电脑上有没有出现 USB 串口和网卡。userdata 上还是 Nura 或 Android 时，initramfs 写明那里不是 Kario 的根文件系统，10 秒后重启进 `boot` 分区里的系统；userdata 上是以前另一次构建的 Kario 时，它照样挂上、systemd 起来，可那些模块是那次构建的密钥签的，这个内核不加载（USB 控制台、Wi-Fi 这类做成模块的起不来）：要等 `flash` 写进同一次构建的 userdata。还是回到 fastboot、屏幕上什么也没有：照 §8.2 取失败那次的日志；`fastboot boot` 报 `Load Error` 时照 §8.2 第 3 步（脚本先设当前槽为活动，一般遇不到）。
18. 刷入（§6）：`yocto/fajita-flash.sh -d ~/kario-images flash`。这会把 userdata 从 Nura 手里拿走：之后要再做测试一得重装 Nura（§13.4 第 10 步），手机上它的状态也随之没了；已经取回的结果留在电脑上。
19. 第一次开机按 §8 查。屏幕不亮也照样取日志（USB 控制台不靠屏幕）：

    ```
    ssh root@172.16.42.1 'bringup-log; journalctl -b --no-pager' > ~/fajita-boot-1.log
    ```

    把 `~/fajita-boot-1.log` 发给我（它也带着序列号，不放进仓库）。`grep phone-initramfs` 那几行里有整条命令行，ABL 追加的在后面。起不来（又回到 fastboot、一遍遍重启）时照 §8.2：先取失败那次的 pstore 和命令行，`Load Error` 时先 `set_active`。
20. 在刷好的手机上用同一个套件跑同样的自动测试（要几个小时；先核对备份）：

    ```
    W="tools/phone-chroot/kario-phone.sh --whole --sku oneplus-fajita-t9"
    $W push setup selftest pixel collect
    ```

    它经 USB 控制台的网络连手机（`root@172.16.42.1`，§7；root 没有密码，不用钥匙），这也是 Nura 的地址：它的主机密钥存在 `~/.ssh/known_hosts.kario-whole`，镜像第一次开机时生成新的主机密钥，所以每次刷完 `ssh-keygen -R 172.16.42.1 -f ~/.ssh/known_hosts.kario-whole`。`push` 只推套件（到 `/var/lib/kario-test/`），并核对刷进去的是为这个 SKU 构建的 Kario；`setup` 只改时钟；`selftest`、`pixel`、`bench` 跑的时候停掉面板上的 Shell 和声音服务（这时屏幕上是控制台），等停掉的单元在自己的控制组里留下的进程都退出，跑完再启动：2026-11-24 以前构建的镜像里 `phone-display` 是 `KillMode=process`，停掉以后渲染器 fork 出来读键盘的那个进程还在，它占着屏幕的 DRM master，再启动的渲染器打不开屏幕（`display: /dev/dri/card0: Permission denied`），`phone-display` 一遍遍失败、黑屏；套件等 10 秒，还在就让 systemd 结束它们（`systemctl kill --kill-whom=all`），并写明是哪几个进程（[11](11-platform-architecture.md) §14）；`selftest`、`pixel` 照测试一调小扬声器、跑完调回。结果和测试一的放在一起，在 `build/phone/oneplus-fajita-t9/results/`，格式一样；`kit.txt` 写着 `path: the whole phone` 和镜像是从哪个提交构建的（带 `share/phone/source.txt` 之前构建的镜像写「not recorded」）。现在还做不到的，以及原因（[24](24-sku-framework.md) §6.2）：
    - 开发者通道（开发者模式加信任的数据线，`10.71.0.1`，`kario device` 连的地方，[27](27-usb-typec.md) §4）只能走 `push`、`setup`、`collect`：自测自己的 Shell 会接管 USB 控制器、把它断掉，所以套件在这条通道上拒绝 `selftest`、`pixel`、`bench`；没有 USB 控制台的镜像（`PHONE_BRINGUP = "0"`）没有别的有线连接；
    - 自测的 Wi-Fi、蓝牙用例是照模拟器的模拟射频写的，在这台手机上动的是真的射频（**未核实**）：用 `--only` 排除它们（§14.1）；要架测试接入点的用例自己会跳过（它要第二个 Wi-Fi 设备，`require-wifi-ap`），探针的包从 Wi-Fi 出去时按应用流量的用例也跳过（`require-mobile-route`）；USB 的用例会跳过（USB 控制台占着控制器）；
    - `phone-bringup-awake` 在电脑配置着 USB 控制台时持着唤醒锁，套件连着线就一直持着，「挂起」用例过不了（`setup` 会说；`systemctl stop phone-bringup-awake` 让手机能睡，USB 连接也随之睡；拔掉线它自己放开，§16.12）；`facts` 的 `== sleep` 一节有一行 `phone-bringup-awake: held: …` 写着为什么持着；这时内核的 `/sys/power/wakeup_count` 读不出来：有唤醒事件在处理（持着的唤醒锁就是一个）时读它一直等。`facts` 读每个属性最多等 2 秒，等不到就写 `wakeup_count=<no answer in 2 s>`，下一行 `wake-up sources active:` 列出正在起作用的唤醒源；2026-11-24 以前的套件在这里卡住，`setup` 停在唤醒锁那句话之后，`facts.txt` 是空的；
    - `shell-start`、`shell-stop` 是测试一的：这里的 Shell 是 `phone-display.service`，它的环境照 §14.1 设；`$W mem` 量的是正在跑的系统。
21. 照第 15 步把结果发给我：两种测试的运行都在同一个目录里，每个 `kit.txt` 都写着是哪一种。

### 13.6 回去

22. 回 OxygenOS：手机进 fastboot，`yocto/fajita-flash.sh restore`（§9；备份是装 Nura 之前做的，就直接用它，之后做的要 `--stock`）。TWRP 用不了、备份是手工做的手机照 §9.1。
23. 进不了 fastboot：MSM 工具（§3.4，备份做完以后才用），重新解锁（§3.3），然后 `yocto/fajita-flash.sh restore-efs ~/kario-images/twrp-fajita.img` 写回 IMEI 和校准数据。TWRP 起不来时改照 §9.1 在 Nura 的调试 shell 里写回；TWRP 3.7.0_9 在 OxygenOS 11 上崩溃进 EDL，不要再拿它试。

## 14. 真机待验证清单

> 给连着这台 6T 的电脑上的人或 agent。别的文档里标着 **未上真机**、**依赖硬件**、6T 答得了的事，每项写跑什么、应该看到什么、结果记到哪、不要做什么。先走完 §13 的第 1–9 步（备份），chroot 的项在 §13.4 以后做，整机的项在 §13.5 以后做。整个清单**未跑**。

### 14.1 先读：怎么跑、记在哪、不要做什么

两条路（§1）：

- **chroot**（§13.4：Nura 的内核、屏、基带和声音）：`K="tools/phone-chroot/kario-phone.sh --sku oneplus-fajita-t9 --image $HOME/kario-images/phone-image-oneplus-fajita.rootfs.ext4.sparse"`，先 `$K push setup`。镜像是这台手机自己的，就是 §6 刷的那个：Shell 的 audiod 照板子的 `[audio]` 设声卡，和整机一样（放音；`$K shell-start` 里还有通话的设置），GPU 的驱动也在（固件是 Nura 的，除非 `--firmware image`，§13.4）；Wi-Fi 归 Nura 的 NetworkManager，设置里的 Wi-Fi 用不了（[24](24-sku-framework.md) §6.2）。
- **整机**（§5、§6：Kario 自己的内核和镜像）：`W="tools/phone-chroot/kario-phone.sh --whole --sku oneplus-fajita-t9"`，先 `$W push setup`（§13.5）；手动就 `ssh root@172.16.42.1`（§7）。屏上的 Shell 是 `phone-display.service`（日志 `journalctl -u phone-display`），渲染器的日志在 `/run/phone/gui.log`，服务各是各的单元（`phone-displayd`、`phone-audiod`、`phone-telephonyd`、`phone-keystored`、`phone-smsd`……）。

两种都用 `$K collect` 或 `$W collect` 把结果取回 `build/phone/oneplus-fajita-t9/results/`，每次一个目录，两种一样：`kit.txt`（走的哪条路、脚本的提交、镜像和镜像是从哪个提交构建的）、`facts.txt`（chroot 里有一节写哪些是 Nura 的）、`selftest.log`、`selftest-junit.xml`、`cts.json`、`pixel-<形态>.log` 和 `-junit.xml`，PNG 在 `pixel/` 里，`times.txt`，设过时钟时有 `clock.txt`；chroot 里还有 `shell.log`，`shell-logs/` 里是各服务和渲染器的日志，`gui.log` 是渲染器的。

只跑自测里的几个用例：`--only` 是用例名的正则表达式，用例名是 `shell/selftest.script` 里 `== ` 开头的行（[13](13-prototype.md) §5 的表）。自测是无头的，总用它自己起的服务和模拟基带（`programs/shellcmd` 的 `privateModem`），不会拨真的号码；基带的项要在交互的 Shell 里手动试（chroot 的 `$K shell-start`，或者整机屏上的 Shell）。整机上不要不带 `--only` 跑整套自测：它的 Wi-Fi、蓝牙、USB 用例是照模拟器写的，在真的射频上会怎样**未验证**（要模拟器的虚拟蓝牙控制器当对端的用例，在手机自己的控制器旁边跳过，写着 `needs bt-peer`；自测的 Shell 用模拟基带，不碰手机自己的移动数据，§16.10，**未上真机**）。

```
# 两种测试一样；结果照上面，单独一次运行
$K --only '<正则>' selftest collect
$W --only '<正则>' selftest collect
```

整机上给服务加环境变量（比如 §14.3 的 `PHONE_PAINT_TRACE`、§14.2 的 `PHONE_DISPLAY_TRACE`）用 `/run` 里的 drop-in，重启手机就没了；Shell 起的渲染器继承 Shell 的环境：

```
mkdir -p /run/systemd/system/phone-display.service.d
printf '[Service]\nEnvironment=PHONE_PAINT_TRACE=1\n' > /run/systemd/system/phone-display.service.d/trace.conf
systemctl daemon-reload && systemctl stop phone-display && systemctl start phone-display
```

2026-11-24 以前构建的镜像停掉 `phone-display` 时留下渲染器读键盘的进程（§13.5 第 20 步），`start` 以后屏幕黑着、`journalctl -u phone-display` 里是 `Permission denied`：先 `systemctl kill --kill-whom=all --signal=SIGKILL phone-display`（单元停着也行）、`systemctl reset-failed phone-display`，再 `start`。

记结果：

- 在下面每项点名的那一句里，把 **未上真机** 换成结果：`一加 6T 上 ✓（日期，整机或 chroot）`、量到的数，或者 `一加 6T 上 ✗：现象`；别的板子和 QEMU 的状态照留。中文和 `docs/en/` 的英文一起改（`scripts/docs-en-check.py`）。跑了的自测用例，结果另写进 [13](13-prototype.md) §5 那一行（几个人一起改的表，改动要小）。
- 写数和现象，不贴原始日志；不写序列号、IMEI、ICCID、IMSI、手机号、蓝牙和 Wi-Fi 的 MAC 地址（`phone-btaddr` 的地址就是从序列号算的），要提到时写 `<序列号>` 一类的占位。`results/`、日志和 `cts.json`（带这台手机的设备认证签名）放在仓库外或 `build/` 里，不提交。
- 板子文件 `sku/boards/oneplus-fajita.toml` 的 `unverified`：核实了的值从列表里删掉（[24](24-sku-framework.md) §6.3 第 1 项），[24](24-sku-framework.md) §7.4 表里的「待核实」跟着改。
- 量出来的耗时、电流交给机主记进内部的验证基线（[24](24-sku-framework.md) §6.3 末尾），公开的文档里只改它们说过的那几句。

不要做的（每一节另有自己的）：

- 备份（§3.5 或 §9.1）做完、另拷一份以前，不往手机写任何东西，不用 MSM 下载工具（§3.4）。
- 不碰 `persist`、`modem`、`modemst1`、`modemst2`、`fsg`、`fsc`：不擦、不刷、不 `dd` 写；只有它们真丢了时，照 §9.1 写回备份。
- 不重新上锁 bootloader（§9）。
- 不拨紧急号码（112、110、119、120）来试：真的会接到急救、报警中心。
- SIM 卡的 PIN 最多故意输错一次（错三次要 PUK）。
- 不往基带装 PDC 配置（§14.6「VoLTE」第 8 步另算，先要有 EFS 的备份；激活基带里已有的配置由 `phone-qcom-pdc` 按 SIM 卡做，§11.1）。
- 不把备份、固件（`CLOSED`，§10）、钥匙、结果放进仓库，装了固件的镜像不给别人。

### 14.2 屏幕、面板和刷新率

- 跑：chroot 的 `facts`（`== drm`、`== dmesg`、`== input`）；整机上 `cat /sys/class/drm/card*-*/modes`、`dmesg | grep -i -e dsi -e dpu`、`journalctl -u phone-displayd | grep 'panel '`；`phone-displayd.service` 加 `PHONE_DISPLAY_TRACE=1`（§14.1 的办法）后翻一翻设置，看 displayd 的每次决定；设置 › 显示与亮度；电源键息屏、亮屏，等它超时调暗、息屏。
- 应该：一个 DSI 连接器，只有 1080x2340 一种模式，60 Hz（6T 的屏规格就是 60 Hz）；displayd 报 `panel …, rates [60]`，一直在 60 Hz；设置里没有「刷新率」一行（屏只有一种刷新率时不显示）。面板驱动 `samsung,s6e3fc2x01`、触摸屏 `syna,rmi4-i2c` 探测到了；状态栏避开刘海、软键避开圆角（`inset = 80`）。息屏时面板和背光关掉，亮屏时先是关屏前的最后一帧；调暗、熄屏、亮屏和换亮度是渐变，不跳。
- 记到：[08](08-display-adaptive-refresh.md) §9 第 2 条和 §7 表「DRM 模式」一行（读真的显卡）；板子文件的 `display.panel`、`display.inset`、`display.touchscreen`；背光渐变：[37](37-motion.md) §3 表里背光那一行、[28](28-battery.md) §1 表「超时没有按键」一行；本文 §8 的「屏幕」一行。
- 不要：拿 6T 验 120 Hz 和切换模式（§14.16）。
- 跑，开机画面（2026-11-26 以后、不带 `boot-log.yml` 构建的镜像，§16.13，**未上真机**）：拍一段从按电源键到 Shell 的开机动画的视频（或用秒表），记下每一段屏幕上是什么、到几秒：引导程序的画面（解锁警告、一加的标志）、显示驱动接手以后（约 6 秒）面板是黑、是冻住的最后一幅还是花屏、Shell 的开机动画出来。应该：从头到尾屏幕上没有字、没有闪动的光标。`journalctl -b -k -o short-monotonic | grep -e 'Console: switching' -e 'fb0:'`：应该有 simplefb 和 msmdrmfb 的 `fb0:`，`Console: switching to colour frame buffer device` 不再出现（`dummy device` 那行还在）；`journalctl -b | grep PHONE-GUI-READY` 的时间和视频里开机动画出来的时间对一下。记到 §16.12。

### 14.3 渲染和动效的耗时（`PHONE_PAINT_TRACE`）

- 跑，屏上：chroot 的 `$K shell-start`（已经带 `PHONE_PAINT_TRACE=1`），或者整机用 §14.1 的 drop-in；用蓝牙键盘照 [13](13-prototype.md) §4.1 的「建议走一遍」走一遍（主菜单、设置首页上下滚动、打开再返回、弹出选项菜单、通讯录），然后 `$K shell-stop`、`$K collect`（整机看 `/run/phone/gui.log`）。渲染器每段动画写一行：每步和转场每步的 50%、95% 分位，每步重画了屏幕的多少，交给面板、等翻页和两帧之间的时间，晚了一个半周期以上的帧（[37](37-motion.md) §5 第 6 条）。
- 跑，无头（按实时显示的画法计时，不上屏）：chroot 里 `on-phone.sh enter PHONE_PAINT_STEPS=/var/lib/kario-results/motion.jsonl /usr/bin/phone-shell --selftest --gui auto --pixel-dir /var/lib/kario-results/pixel-motion --timeout-scale 2`，最后一行是 `PHONE-GUI-MOTION:`。
- 跑，帧文件：开发机上用 `PHONE_PAINT_DUMP=<文件>` 跑像素自测记下帧（[37](37-motion.md) §2.1），拷到手机，`/usr/lib/phone/renderer --bench <文件>` 跑一遍（chroot 里经 `on-phone.sh enter`，文件放在 `/var/lib/kario-results/` 下），`--size 1080x2180@4.5`（6T 画的面积）再跑一遍。
- 应该：目标是转场期间每步的 95% 分位不超过 16.7 ms；[37](37-motion.md) §2 估的是整屏的步 2.5–8.5 ms、部分的步 1–2.5 ms。从 `shell-start` 到 `PHONE-GUI-READY` 的秒数在 `times.txt`。6T 是命令模式的 DSI 面板：翻页完成的事件带不带帧号、出帧是不是按周期，看 `gap` 和晚了的帧。
- 记到：[37](37-motion.md) §2（加一小节「一加 6T」，和开发机、QEMU 的数并列，换掉「估算，未上真机」）、§5 第 1、2、6、9 条和 §6 第 6 步的「未上真机」「依赖硬件」；[08](08-display-adaptive-refresh.md) §6 的「估算，未上真机」；[24](24-sku-framework.md) §6.3 第 3、4 项。
- 不要：这一节设 `PHONE_PAINT_GPU=1`（GPU 见 §14.4；在 GPU 的测试镜像上，§4，这一节要用 drop-in 设 `PHONE_PAINT_GPU=0`，量的才是 CPU 路径）；在手机忙的时候量（chroot 里 Nura 的服务在跑、自测在跑）。

### 14.4 GPU

- 步骤在 §8.1（L1：GPU 合成）。测试镜像是用 §4 的 GPU 开关构建的时，§8.1 的各项不用设 `PHONE_PAINT_GPU=1`，先查它表里的第一行；chroot 里套件换上自己的 `sku.json`（§13.4），开关在那里不起作用，要设环境变量。先确认 GPU 起得来：`dmesg | grep -i -E "a630|adreno|sqe"` 里不该有固件加载失败。2026-11-21 那台 6T 上 Nura v26.06 的内核加载 `a630_sqe.fw` 报 -40（ELOOP，符号链接成环）：在这样的底座上 GPU 起不来，§8.1 只能在 Kario 自己的整机镜像上做，或者在 chroot 里用这台手机自己的镜像加 `--firmware image`（§13.4，**未上真机**），或者等 Nura 的固件包修好。
- 翻页被拒（`page flip: Invalid argument`，2026-11-21 在 Nura 的 chroot 里见过，[42](42-gpu-rendering.md) §4.3）：渲染器现在第一次被拒时自己在日志里写出内核看到的 CRTC、平面、帧缓冲和 `likely:` 几行。chroot 里：`$K shell-start`，走几下，`$K shell-stop`、`$K collect`，看 `results/<这次>/gui.log` 里 `phone-paint: page flip:` 开头的几行。`likely:` 说「nothing here breaks a rule」时让内核说出原因（只这一会儿打开，drm 调试很吵）：
  ```
  # 手机上，root
  echo 0x14 > /sys/module/drm/parameters/debug && dmesg -C
  # 电脑上 $K shell-start，等 gui.log 里出现 page flip，再 $K shell-stop
  dmesg > /var/lib/kario-results/drm-debug.txt; echo 0 > /sys/module/drm/parameters/debug
  grep -i -e 'full modeset' -e 'rejecting' -e 'but off' -e 'PLANE:' -e dpu /var/lib/kario-results/drm-debug.txt | tail -40
  # 有 debugfs 时，Shell 开着的时候再存一份原子状态（平面的 rotation、CRTC 的 active、帧缓冲）
  mount -t debugfs none /sys/kernel/debug 2>/dev/null; cat /sys/kernel/debug/dri/*/state > /var/lib/kario-results/drm-state.txt
  ```
  GPU 固件：`readlink -f /lib/firmware/qcom/a630_sqe.fw`、`ls -l /lib/firmware/qcom/ | grep -i a630`、`dmesg | grep -i -e a630 -e gmu -e zap`。整机上（Kario 自己的镜像，lockdown 下没有 debugfs）：`grep 'page flip' /run/phone/gui.log`，drm 调试同上（root 能写就写）。`gui.log` 的那几行、`drm-debug.txt` 里 `grep` 出来的几行和 `drm-state.txt` 发回来。
- 门槛（§8.1 末尾）：GPU 起得来以后 `$W perf`（chroot 是 `$K perf`），六次的表在 `results/<这次>/perf.txt`；`drawn` 一栏是 `compose`、`raster` 的那几行才是 GPU 的（写着 `cpu` 的是它没起来，`perf-<路>-<大小>.log` 里有原因）。**未上真机。**
- L2（`PHONE_PAINT_GPU=raster`，[37](37-motion.md) §5 第 10 条）：先在手机上跑渲染器的 L2 测试和 `phone-paint --bench <帧文件>`（freedreno 有没有 framebuffer fetch、GLSL ES 3.00 的着色器编不编得过、GPU 画的和 CPU 是否逐字节相同）；再照 §8.1 上屏，把 `PHONE_PAINT_TRACE`、`PHONE-GUI-MOTION` 和功耗在不设、`=1`、`=raster` 三种下各量一遍。
- 记到：§8.1 的各条和门槛的表（`perf.txt`，§16 新的一节），[37](37-motion.md) §2.4、§2.5，[42](42-gpu-rendering.md) §7、§4.3（翻页被拒的原因）、§10 的表。都过了以后照 §8.1 末尾把板子的 `paint_gpu` 打开。
- 不要：拿 `PHONE_PAINT_GPU=soft` 的结果当 GPU 的（那是 Mesa 的软件实现，只给 QEMU 测这条路用）。

### 14.5 声音：放音、通话路由和 UCM（整机）

- 跑：`aplay -l`；`systemctl status phone-audiod`，`journalctl -u phone-audiod` 里开机后第一次起来时跑了 UCM 的引导序列、设了 `HiFi` + `Speaker`；按键音、铃声、音乐从扬声器出来（`aplay -D plughw:0,2 <wav 文件>`）。然后照 [20](20-audio.md) §10 末尾的清单和第二部手机打一通电话：接通（`Voice Call` + `Earpiece` + `Mic`，听筒）、开免提（`Speaker1`）、静音（去掉 `Mic`）、通话中 `systemctl restart phone-audiod`（接着原来的设置）、挂断（回到 `HiFi`）；每一步 `cat /run/phone/audiod-ucm.json`，`journalctl -u phone-audiod | grep 'call routing'`。
- 应该：设置照 [20](20-audio.md) §10 的表换；对方听得见，静音时听不见，取消静音又听得见；免提有没有回声（主线的 q6voice 没有 ACDB 校准）；引导序列设的音量合不合适；2G、3G 网络自己送的回铃音和本地放的叠不叠。
- 记到：[20](20-audio.md) 开头的状态、§10 的状态行和末尾的清单（每项写结果）、§7 表「真实通话音频」「蓝牙、耳机、听筒 / 扬声器真路由」两行；[11](11-platform-architecture.md) §16 表 AudioFlinger 一行；[12](12-super-app-capabilities.md) 表「音频路由」一行；本文 §8 的「通话」「声音」两行、§11 第一条。静音以后恢复不了，先记下现象（要改的代码见 [20](20-audio.md) §10 末尾）。
- 跑，测试时的音量（两条路一样）：`selftest`、`pixel` 跑之前套件把板子的 `quiet_controls` 调到 `quiet_level`，跑完调回（[24](24-sku-framework.md) §6.2）。6T 写的是 WCD934x 编解码器各 RX 通路的数字增益（`RX0 Digital`–`RX4 Digital`，2026-11-23 测试时手工调的），可扬声器的功放（TFA9894）按 UCM 配置接在 QUAT_MI2S 上，不在这些通路上：先听它们管不管得到扬声器。`$W --only '^音乐：后台播放' selftest` 时听一下，再 `$W --quiet off --only '^音乐：后台播放' selftest` 比一下；`facts.txt` 的 `== sound` 列出声卡的混音控件（`amixer scontrols`）和那五个的当前值。小不了的话，从列表里找扬声器功放的音量控件，`aplay -D plughw:0,2 <wav 文件>` 放着时 `amixer -c 0 sset '<控件>' 15%` 试，管用的名字告诉我（写进板子文件的 `quiet_controls`，从 `unverified` 里删掉 `audio.quiet_controls`）。跑完 `amixer -c 0 sget 'RX0 Digital'` 回到跑之前的值。
- chroot 里先听放音（**未上真机**）：`$K push setup` 之后 `ssh root@172.16.42.1 'chroot /var/lib/kario-chroot/root alsaucm -c hw:0 set _verb HiFi set _enadev Speaker && aplay -D plughw:0,2 <wav 文件>'`（镜像的 alsaucm；QEMU 镜像用 Nura 的，去掉 `chroot …`），扬声器要响，`dmesg` 里没有新的 `no backend DAIs`；再照 §14.1 只跑四个用例 `$K --only '^(音乐：后台播放|鸿雁：语音消息|录音：应用离开|视频：图片里)' selftest`，都要过，`selftest.log` 里打出的 `phone-audiod.log` 没有 `sink` 或 `alsaucm` 的错。整机上用 `$W` 跑同样的四个。记到 [20](20-audio.md) §10 末尾的清单。
- 不要：把 chroot 里用 QEMU 镜像看到的通话路由当成手机的（它没有 alsaucm，audiod 切不了声卡；用这台手机自己的镜像时，chroot 的 `shell-start` 和整机一样切，**未上真机**）；手工 `alsaucm` 改了声卡以后不复原就接着测（重启 audiod 或手机）。

### 14.6 基带：电话、短信、彩信、移动数据

真的基带只在交互的 Shell 里：整机屏上的 Shell，或者 chroot 的 `$K shell-start`（Shell 起的 telephonyd 用 SKU 的 `modemmanager`）。对方用自己的第二部手机。

- 跑：`cat /sys/class/remoteproc/*/state`；整机上 `systemctl status rmtfs tqftpserv phone-qcom-uim ModemManager phone-telephonyd`、`journalctl -u phone-qcom-uim`；`mmcli -L`、`mmcli -m 0`（制式、信号、运营商）。然后：
  - 电话：拨出、接听、挂断；保持了再拨第二路、来回切换（[19](19-core-apps.md) §5.4）；通话中按数字给运营商客服的语音菜单发 DTMF；通话转到后台再回来；通话中息屏上锁、按通话键回到通话（[31](31-lock-and-biometrics.md) §2.2）。
  - 短信：收、发、超过 70 个汉字的长短信、送达回执；smsd 存好以后基带里的那条删了（`mmcli -m 0 --messaging-list-sms`，[32](32-messaging.md) §3）。
  - 彩信（**MVP**）：开着移动数据，给第二部手机发一张图、收一张（[32](32-messaging.md) §11）。三家运营商的彩信都在单独的接入点上（cmwap、3gwap、ctwap）：收发时 `mmcli -m 0 --list-bearers` 多一条承载（`mmcli -b <n>` 的 APN 是 cmwap、APN 类型是 mms，网卡和上网的不同），`ip rule` 有 `29999: from <它的地址> lookup 2049`，`ip route show table 2049` 有经它网关的默认路由，`/run/systemd/network/` 里有它网卡的 `10-kario-cell-<网卡>.network`；收发完这条承载断开（最晚三分钟）、文件改成 Unmanaged；连不上时 `journalctl -u phone-telephonyd` 有 `MMS: cannot connect`，smsd 的日志有 `the MMS access point`。
  - LTE 附着接入点：插卡后第一次开机，`journalctl -u phone-telephonyd | grep 'LTE attach APN'` 有一行（中国移动的卡是 cmnet），`mmcli -m 0` 的初始 EPS 承载设置跟着变成它，之后注册在 LTE 上；再开机不再有这一行（基带记住了）。每次开机都有说明基带没记住、射频每次开机都重启一次，要记下来。
  - IMS 承载（装了 81voltd 时，§11.1）：`mmcli -m 0 --list-bearers` 里 81voltd 开的承载，`mmcli -b <n>` 看 APN、IP 类型、有没有 APN 类型；在设置里关移动数据，IMS 承载还连着、只有上网的断了，再打开后上网承载回来、IMS 的一直在；状态栏和设置里的移动数据跟着上网承载走。
  - 移动数据：设置里打开，`mmcli -m 0` 的承载，`ip addr`、`networkctl status` 里基带网卡的地址、路由和 DNS（整机上 Shell 经 systemd-networkd 配，[04](04-ui-language-telephonyd.md)）；用的哪个接入点（设置 › 移动网络 › 接入点名称）；下载一个大小已知的文件，看设置 › 流量记的数。chroot 里看 Nura 的 NetworkManager 会不会也去连基带的承载（[24](24-sku-framework.md) §6.2：两边会不会抢**未核实**）。
  - 自测不碰手机自己的移动数据（整机，§16.10，**未上真机**）：开着移动数据跑 `$W --only '^VPN.*（设备）' selftest`，跑之前、跑的时候（另开一个 SSH）、跑完各 `ip route show default` 一次，都有经 `qmapmux0.0` 的默认路由，`networkctl status qmapmux0.0` 是 `configured`；三个用例都过（没有 Wi-Fi、也没开移动数据时后两个跳过，写着 `needs direct-route`）；跑完 `ip link show kario-vpn` 报没有这个设备，`ip rule` 里没有 30000、30001 两条。结果记到 §16.10 那两条。
  - SIM 卡锁：开了 PIN 的卡，在 Shell 的 PIN 界面输对一次。
  - 时间：关着 Wi-Fi 开机，时间是不是跟着网络（NITZ，[19](19-core-apps.md) §2）；跟上以后 Shell 的日志（`grep rtc: /run/phone/shell.log`）有一行 `offset … kept (network)`（§14.10）。
  - 定位（板子的 `gnss`）：`mmcli -m 0 --location-status`；`mmcli -m 0 --location-enable-gps-nmea` 以后在室外等一会儿，`mmcli -m 0 --location-get` 有没有位置。
- 应该：`phone-qcom-uim` 打开了 SIM 卡的会话，或者说它本来就开着；`mmcli -L` 有一个调制解调器；每项通还是不通。中国的运营商没有 VoLTE 时要有 2G、3G 的语音网才打得了电话（§2、§11.1）：记下哪家、什么制式。附着接入点只设一次；关移动数据时 IMS 承载不断；彩信承载只在收发时连着。
- 记到：[04](04-ui-language-telephonyd.md) 的状态行、「真机」一条和各条的「未上真机」（「彩信承载」「别的程序开的承载」「LTE 初始附着的接入点」三条也在内）；本文 §11.1；[32](32-messaging.md) 的状态行、§1 表、§10 表「设备自测」一行、§11；[19](19-core-apps.md) §1 表电话、短信两行和 §5；[11](11-platform-architecture.md) §16 表短信、彩信、移动数据、流量几行；[12](12-super-app-capabilities.md) 表「短信验证码自动填充」一行（真的收到验证码短信时）；板子文件的 `gnss`；本文 §8 的调制解调器、SIM、短信、数据、通话几行。
- 不要：拨紧急号码；故意输错 PIN 超过一次；改 APN 以后不恢复默认；试小区广播和数据漫游（§14.16）；手工用 `mmcli --3gpp-set-initial-eps-bearer-settings` 改附着接入点（和 telephonyd 设的打架，每次都让射频重启）。

#### VoLTE：联通、电信的卡和第二部手机（整机）

§11.1 写的都**未上真机**，这里是在手机上核对的步骤。要：整机镜像（§13.5，2026-11-25 以后构建的，里面有 81voltd），§3.5 的备份另拷了一份，一张联通或电信的卡（先在第二部手机上看它打电话时有 VoLTE 或 HD 的标志，没有就先找运营商开通），第二部手机当对方，蓝牙键盘。命令都在 USB 控制台的 SSH 里跑（`ssh root@172.16.42.1`，§7）。只读的命令（列配置、读状态）随时可以跑；会改基带的（激活、装配置）先停 ModemManager 和 telephonyd，照第 7、8 步做。

1. 镜像里有它们：`systemctl status 81voltd phone-qcom-pdc --no-pager`。应该：`81voltd` 是 `active (running)`，`phone-qcom-pdc` 是 `status=0/SUCCESS` 退出；`ls /usr/bin/81voltd /usr/libexec/phone-qcom/pdc-select /usr/share/phone-qcom/pdc-carriers` 三个都在。
2. 基带里有哪些配置（只读）：`qmicli -d qrtr://0 --pdc-list-configs=software`、`qmicli -d qrtr://0 --pdc-list-configs=platform`；`qmicli -d qrtr://0 --nas-get-home-network`（联通 MCC `460`、MNC `1`，电信 `460`、`11`）。把每个配置的 Description、Status、Version 抄下来（ID 不用）：有没有名字里带 `CU`、`Unicom`、`CT`、`Telecom` 的，叫什么。
3. `pdc-select` 做了什么：`journalctl -b -u phone-qcom-pdc --no-pager`、`cat /var/lib/phone-qcom/pdc-*`。应该是三种之一：`… is Active already`（`kept`）；`activating …`，接着 `… is active`、`uim-select: …`（`activated`，基带重启过一次：`dmesg | grep -i remoteproc` 里有 mpss 停下又起来，`mmcli -m any` 里 SIM 卡和注册都在）；`holds no configuration for MCC 460 MNC …`（基带里没有这家的：做第 7 步试别的，或第 8 步装）。挑中的名字和第 2 步的列表对不对，要是挑错了（比如该挑的名字里没有表里的词），记下名字，改 `pdc-carriers` 的词。
4. 注册上 IMS：`mmcli -m any` 的制式是 `lte`、已注册；IMS 注册照 §14.21 第 2 步问（先绑定），应该是 `Status: 'registered'`、`Technology: 'wwan'`；服务同样先绑定，再 `qmicli -p -d qrtr://0 --client-cid=<数> --imsa-get-ims-services-status`，Voice service 是 `available`。`journalctl -b -u 81voltd --no-pager | grep -i -e 'start connection' -e bearer` 有一行 `Req: Start connection, … apn: ims`（记下 APN、IPv4 还是 IPv6）、没有 `Failed to create bearer`。`mmcli -m any --list-bearers`：上网的一条，加 81voltd 开的一条；`mmcli -b <它的号>` 记下 APN、IP 类型、有没有 APN 类型（`apn-type`）、配置号（`profile-id`）、`connected: yes`、网卡。一分钟还没注册上就记下 `--imsa-get-ims-registration-status` 和 81voltd 的日志，做第 7 步。
5. 打电话（Shell 的电话应用，上面「电话」那一项照做一遍）：拨第二部手机，响铃、接通、两边都听得见（听筒）、免提、静音、保持再恢复、挂断；第二部手机打过来，接听、对方挂断；拨 10010（联通）或 10000（电信）的语音菜单按数字（按键音）。通话时 `mmcli -m any --voice-list-calls`、`mmcli -o <号>` 的状态是 `active`，`cat /run/phone/audiod-ucm.json` 是 `Voice Call`，`journalctl -u q6voiced -b --no-pager` 打开了语音 PCM。联通、电信只有 4G 时能打通就是 VoLTE；第 4 步的注册状态是旁证。每项记通不通、声音怎样（有没有断续、回声）。
6. 和移动数据、飞行模式一起：设置里关掉移动数据，`mmcli -m any --list-bearers` 里 81voltd 的承载还连着，IMS 还是 `registered`，再打一个电话能通；打开移动数据，上网回来。开关一次飞行模式：关的时候承载都断，开了以后 81voltd 的日志里又有一行 `Start connection`，一分钟内 IMS 重新注册，电话又能打。重启手机：`journalctl -b -u phone-qcom-pdc` 是 `dealt with at an earlier boot`，IMS 照样注册上。短信：和第二部手机互发一条（电信的卡只有 4G，看短信是不是经 IMS 走得通），记下结果。
7. 手工试别的配置（第 3 步没挑到、或挑到的注册不上）：`systemctl stop phone-telephonyd ModemManager 81voltd`（telephonyd 也停，免得它经 D-Bus 又把 ModemManager 叫起来；这中间不跑 `mmcli`），从第 2 步的列表里挑一个（另一个中国运营商的 VoLTE 配置、`Volte_OEM_Lab`：社区记录里一张德国的卡用它通了、`ROW_Commercial`），`qmicli -d qrtr://0 --pdc-activate-config=software,<ID>`；等半分钟，`qmicli -d qrtr://0 --pdc-list-configs=software` 直到它是 `Active`；`/usr/libexec/phone-qcom/uim-select qrtr://0`；`systemctl start ModemManager 81voltd phone-telephonyd`，回到第 4 步。每试一个记下名字和结果。`pdc-select` 不会在下次开机换回去（有 `/var/lib/phone-qcom/pdc-<MCC>-<MNC>` 时它不动；没有时它只会激活名字里有这家的词的那个）。回到原来的：照样激活第 2 步记下的那个 Active 的。
8. 装配置文件（写基带的持久存储，只在第 7 步都不行、§3.5 的备份另拷了一份以后做）：在电脑上用 payload-dumper 一类的工具从 OxygenOS 11.1.2.2 完整包的 `payload.bin` 解出 `modem.img`（就是 `modem` 分区，vfat），`mount -o loop,ro`，找 `image/modem_pr/mcfg/configs/mcfg_sw/generic/china/cu/…` 或 `…/ct/…` 下的 `mcfg_sw.mbn`（`commerci`、`openmkt` 目录下的），拷到手机的 `/tmp`；手机上照第 7 步停掉三个服务，`qmicli -d qrtr://0 --pdc-load-config=/tmp/mcfg_sw.mbn`，`--pdc-list-configs=software` 里多了一个（Inactive），照第 7 步激活它。只装一个、记下装了哪个文件（目录名和 SHA-256）；装坏了照 §9.1 用备份写回 `modemst1`、`modemst2`、`fsg`。
- 记到：本文 §11.1 表的「运营商配置」「IMS 数据」两行和后面各段的「未上真机」（基带里有哪些配置、叫什么、`pdc-select` 挑没挑对、激活以后基带重启了多久；81voltd 开的承载的属性；通话、短信、紧急数据连接怎么走），`pdc-carriers` 的词（名字对不上时），板子文件的 `unverified` 里删掉 `ims`（通了以后）；[04](04-ui-language-telephonyd.md)「VoLTE」一条；[11](11-platform-architecture.md) §16 表 IMS 一行。
- 不要：拨紧急号码试紧急呼叫经 IMS；不停 ModemManager 就激活、装配置；一次装好几个配置文件；删基带里的配置（`--pdc-delete-config`）。

### 14.7 Wi-Fi、蓝牙、热点（整机）

- 第一个蓝牙键盘：Kario 的设置要键盘才走得到，手机自己只有电源键和音量键（§14.11）。先在 USB 控制台里 `command -v bluetoothctl`，看镜像里有没有它（**未核实**）：有就用它配对（`scan on`、`pair <地址>`、在键盘上打配对码、`trust`、`connect`）；没有就记下来（[41](41-design-gaps.md) §6），要键盘的项改在 chroot 里做。
- Wi-Fi：设置 › Wi-Fi 连一个 WPA2 的网络（`ip link` 里有 `wlan0`）；再连一部安卓手机的热点，设置里应该写「按流量计费：手机热点」（DHCP 带的 `ANDROID_METERED`）。
- 热点：设置 › 个人热点打开，电脑或另一部手机连上，经移动数据上网，「已连接的设备」里有它。
- 蓝牙：`systemctl status phone-qcom-btaddr` 是成功退出的，`journalctl -b -u phone-qcom-btaddr` 最后一行是 `hci0 has <地址>`（控制器还在设时前面先有一行 `is not set up yet; waiting`；地址不要抄进文档），`busctl tree org.bluez` 里有 `/org/bluez/hci0`，设置 › 蓝牙打得开；`$W --only '^(飞行模式|蓝牙搜索跟着页面)' selftest` 两个都过，要虚拟对端的四个蓝牙用例跳过、写着 `needs bt-peer: the phone's adapter hci0 is a controller of its own`（§16.10，**未上真机**）；配对一部真的安卓手机，给它发文件，它读通讯录以前先问（[13](13-prototype.md) §5「蓝牙传文件和电话本」）；可选：设置 › 我的设备 › 从旧手机迁移的「经蓝牙迁移」读那部安卓手机（[36](36-device-switch.md) §8）。
- 记到：[11](11-platform-architecture.md) §16 表 Wi-Fi、计费网络、热点和蓝牙（OBEX）几行；[25](25-system-update.md) §4 和 §6.1 的「问 DHCP 服务器未上真机」；[19](19-core-apps.md) §1 表设置一行里热点那一段；[36](36-device-switch.md) 的状态行和 §8；本文 §8 的 Wi-Fi、蓝牙两行。
- 不要：不带 `--only` 在整机上跑整套自测（§14.1）。

### 14.8 USB：连接电脑、文件传输、开发者模式（整机）

`phone-bringup` 先占了 USB 控制器，这时 Shell 自己的 USB 不工作（§7），Shell 只在起来时看一次控制器。要试这一节：先连上 Wi-Fi，改用 Wi-Fi 上的 SSH（`ssh root@<手机在 Wi-Fi 上的地址>`），再停掉 USB 控制台、重新起 Shell；`journalctl -u phone-display | grep usb:` 看它拿到了控制器。试完重启，USB 控制台就回来了。

```
systemctl stop phone-bringup-usb
systemctl stop phone-display && systemctl start phone-display
```

- 跑：插上电脑，手机问信不信任；信任后浏览器开 `http://10.71.0.1`，用通知里的密码拷走照片、往「文件」里拷；拔线停止共享（[27](27-usb-typec.md) §3）。设置 › USB 选「文件传输」：Linux 电脑上 `mtp-detect`、文件管理器（gvfs）里有 Photos、Videos、Music、Files（§3.3）。开发者模式打开后，电脑上用这个仓库的 `kario device`（[27](27-usb-typec.md) §4）装应用、看日志、推拉文件、截屏；插着线关掉开发者模式，22 端口当场关上。
- 应该：和 QEMU 上那几个用例一样（[13](13-prototype.md) §5「USB 连接电脑」「USB 文件传输（MTP）」「开发者模式……」）。记下电脑是哪种系统，Windows、macOS 有就各试一次。
- 记到：[27](27-usb-typec.md) 的状态行、§3.3 表「真电脑」一行、§4、§6；[11](11-platform-architecture.md) §16 表 MTP 那一行；[13](13-prototype.md) §5 那几个用例的行。
- 不要：只连着 USB 控制台的时候停 `phone-bringup-usb`（SSH 当场就断了）。

### 14.9 传感器、指纹、振动

- 跑：chroot 的 `facts`（`== iio`、`== input` 里的 `ff`）；整机上 `ls /sys/bus/iio/devices/`、`journalctl -u phone-keystored`；设置 › 显示与亮度 › 自动亮度，设置 › 安全；静音模式下让第二部手机打进来；设置 › 辅助功能 › 按键反馈选「声音与振动」再按键。
- 应该：没有测光照的 IIO 设备（光线和距离传感器在 SLPI 后面，要 hexagonrpcd，GPL-3.0，§10），自动亮度的页面写明是模拟的；keystored 没有指纹传感器（板子不写 `fingerprint`），设置里写「没有指纹传感器」（[31](31-lock-and-biometrics.md) §4.1）；有带力反馈（`FF_RUMBLE`）的输入设备时，来电每 1.5 秒振一次（[20](20-audio.md) §4.1），没有就记下来。
- 记到：[28](28-battery.md) §1 表「自动亮度」一行、[11](11-platform-architecture.md) §16 表环境光那一行；[31](31-lock-and-biometrics.md) §4.1 表「没有可用传感器的机型」一行；[20](20-audio.md) §4.1 的「在真的力反馈设备上振**未上真机**」、[11](11-platform-architecture.md) §16 表振动那一行、[24](24-sku-framework.md) §7.3 表「振动」一行、本文 §11 的马达。
- 不要：为了传感器装 hexagonrpcd（GPL-3.0，进不了镜像）。

### 14.10 电池、充电、挂起和唤醒

- 先（整机）：§8.3 的 `module-check.sh` 打出 PASS。RTC、电量计、充电器编进了内核（§8.3），`lsmod` 里没有它们：开机就该有 `/sys/class/rtc/rtc0`（`hctosys` 是 1）、`/sys/class/power_supply/` 里的 bq27411 和 PMI8998 的充电器；`dmesg | grep -i -e rtc -e bq27 -e smb` 里是它们探测的那几行（2026-11-24 那次整机这两处都是空的，§8.3）。
- 跑：`cat /sys/class/power_supply/*/type`，Battery 那一个的 `status`、`capacity`、`current_now`，USB 那一个的 `usb_type`（chroot 的 `facts` 里也有）；息屏时插上、拔下充电器：屏幕亮起、提示、状态栏的闪电、待机界面的大电池（[28](28-battery.md) §1）；电池充满（`Full`）时也拔插一次：拔下时屏幕不亮，插上时 1 秒内亮（`journalctl -b -u phone-display -o short-precise | grep 'screen on (charger)'`），提示在认出充电器以后出（2026-11-25 以前构建的镜像充满以后拔插都不亮，§16.11）；息屏时让第二部手机发一条短信。
- 跑，插充电器叫醒（整机，[28](28-battery.md) §1）：`$W facts` 的 `== sleep` 一节每个唤醒源一行，`wake-up source c440000.spmi:pmic@2:charger@1000: wakeup=enabled …` 说明充电器能叫醒手机（它的插线中断 `usb-plugin` 在下面一行 `/proc/interrupts` 里）。`systemctl stop phone-bringup-awake`，拔着线等它睡（`sleep: mem until …`，USB 断开），插上充电器：应该几秒内醒来、屏幕亮、出提示；`cat /sys/power/pm_wakeup_irq` 是 `usb-plugin` 那一行开头的号（2026-11-25 是 121），日志有 `sleep: woke after … (irq 121)` 和 `screen on (charger)`。记下插上到亮屏几秒；不醒就是充电器不是唤醒源，记下 facts 那一行。
- Shell 停住时（屏幕亮着，电源键、超时都没反应，SSH 通；2026-11-25 见过一次，§16.11）：先别重启。`journalctl -b -u phone-display -o short-precise | grep -E 'loop:|power key|screen' | tail -30`：2026-11-25 以后构建的镜像，循环 5 秒没转就有一行 `loop: has not turned for …`，写着循环的 goroutine 停在哪些调用、什么状态（`[IO wait]` 等一个文件，`[chan receive]`、`[select]` 等别的 goroutine，`[syscall]` 卡在内核里）；`power key: from the renderer: …` 还在一行行来，说明键到了 Shell、渲染器没事。再照下面「连按电源键」第 4 步抓线程的 `wchan`，加上 `ls -l /proc/$(pidof phone-shell)/fd`（开着的 sysfs 属性就是它正在读的）。然后 `systemctl restart phone-display`，记下恢复没有。
- 跑，按住电源键不放（整机，[28](28-battery.md) §7；2026-11-25 按住会重启，§16.11）：
  1. 先读 PMIC 的设置（**未验证**：寄存器照 Qualcomm 下游 qpnp-power-on 驱动的表，目录名照主线的 SPMI 命名）：`mount -t debugfs none /sys/kernel/debug`，`ls /sys/kernel/debug/regmap/` 里找 pm8998（pmic@0，主线上叫 `0-00`），`dd if=/sys/kernel/debug/regmap/0-00/registers bs=9 skip=$((0x840)) count=4 2>/dev/null`（每行 9 个字节，只读这四个寄存器）。四行是 PON 的 `KPDPWR_S1_TIMER`（0x840，低 4 位是 0、32、56、80、138、184、272、408、608、904、1352、2048、3072、4480、6720、10256 ms 里的第几个）、`KPDPWR_S2_TIMER`（0x841，低 3 位是 0、10、50、100、250、500、1000、2000 ms 里的第几个）、`KPDPWR_S2_CNTL`（0x842，低 4 位是复位方式：1 热复位、4 关机、7 硬复位）、`KPDPWR_S2_CNTL2`（0x843，最高位是 1 就开着）；按住到复位是 S1 加 S2。
  2. 屏上的 Shell 里按住电源键，别的不按，用秒表记到屏幕灭、重新开机几秒。
  3. 回来以后 `journalctl -b -1 -o short-precise | tail -20`：最后几行是 `phone-paint: power key down`、`power key: from the renderer: long press …`、`power key: long press …: the power menu`，之后没有 `Reached target Reboot`、`systemd-shutdown` 这类行，就是 PMIC 复位（不是 Kario）；有的话记下它前面几行。`grep -o 'androidboot.startupmode=[^ ]*' /proc/cmdline`，和一次 `reboot` 以后的比一比。
  4. 记到：[28](28-battery.md) §7「按住电源键不放会重启」那一条（几秒、S1、S2、复位方式）。
- 跑，挂起（整机）：`systemctl stop phone-bringup-awake`（2026-11-26 以后构建的镜像拔掉 USB 线也行；一夜耗电、来电、短信、闹钟、亮屏耗时照 §14.19）；息屏 30 秒以后 `journalctl -u phone-display | grep sleep:`（`sleep: mem after …`，或者 `sleep: off (…)` 写着为什么不挂起；2026-11-25 以前构建的镜像这几行只在 `/run/phone/shell.log`），`cat /sys/power/suspend_stats/success`；分别用电源键、第二部手机打来的电话和短信、3 分钟以后的闹钟叫醒它（`sleep: woke after … (…)`）；自测 `--only '^挂起'`（`phone-bringup-awake` 没停时它跳过，写着 `needs sleep: the wake lock phone-bringup is held`，§16.10；停了以后要过）。
- 跑，电源键叫醒（整机，[28](28-battery.md) §7；2026-11-23 在 Nura 的 chroot 里 RTC 叫得醒、电源键叫不醒，那时 `pm_wakeup_irq` 还不存在，事后也没有记下中断数）。用这棵树构建的镜像（它的内核有 `CONFIG_PM_DEBUG`，才有 `/sys/power/pm_wakeup_irq` 和 `pm_debug_messages`）。电源键的设备是 PON 下面的平台设备 `c440000.spmi:pmic@0:pon@800:pwrkey`：它的属性在 `/sys/bus/platform/devices/c440000.spmi:pmic@0:pon@800:pwrkey/power/`，也就是输入设备 `pm8941_pwrkey` 的 `device/power/`；`/sys/class/wakeup/wakeupN` 是它的唤醒源，那里没有 `power/wakeup`（要看 `wakeupN/device/power/wakeup`）。
  1. 挂起前（USB 控制台里）：

     ```
     echo 1 > /sys/power/pm_debug_messages
     P=/sys/bus/platform/devices/c440000.spmi:pmic@0:pon@800:pwrkey
     snap() { date; cat /sys/power/pm_wakeup_irq; grep -H . /sys/power/suspend_stats/success /sys/power/suspend_stats/fail /sys/power/suspend_stats/last_failed_*; grep -H . $P/power/wakeup*; grep pm8941_pwrkey /proc/interrupts; }
     snap > /var/lib/kario-test/wake-before.txt 2>&1
     nohup sh -c 'while :; do sleep 2; { date; cat /sys/power/suspend_stats/success; grep pm8941_pwrkey /proc/interrupts; } >> /var/lib/kario-test/wake-loop.txt; sync; done' > /dev/null 2>&1 &
     ```

     `$W facts` 也行：`== sleep` 一节有同样的东西（`key wake-up source pm8941_pwrkey:` 一行下面）。应该：`$P/power/wakeup` 是 `enabled`；`pm_wakeup_irq` 还没有唤醒时报 `No data available`。
  2. 等它睡（日志 `sleep: mem until …`，USB 随之断开），过一分钟按一下电源键（短按，不到半秒）。
  3. 屏幕亮了、USB 回来以后（约 10 秒）：`snap > /var/lib/kario-test/wake-after.txt 2>&1`，`dmesg | grep -E 'PM: suspend (entry|exit)|Triggering wakeup|pwrkey|dsi|dpu' | tail -30`，`journalctl -b -u phone-display -o short-precise | grep -E 'sleep:|screen (on|off)|panel'`（2026-11-25 以前构建的镜像，Shell 的这些行只在 `/run/phone/shell.log`）。
  4. 应该：`pm_wakeup_irq` 是 `/proc/interrupts` 里 `pm8941_pwrkey` 那一行开头的号（上次是 90）；`suspend_stats/success` 加 1；`pm8941_pwrkey` 的中断数加 1 或 2（按下、松开各一次）；dmesg 有 `PM: suspend exit` 和 `Triggering wakeup from IRQ 90`；Shell 的日志有 `sleep: woke after …s (irq 90)` 和 `screen on (key)`。`$P/power/wakeup_count`、`/sys/class/wakeup/wakeupN/event_count` 还是 0 是正常的：pm8941-pwrkey 只把它的中断设成唤醒中断（`enable_irq_wake`），从不向自己的唤醒源报事件，所以这两个数说明不了按键叫没叫醒手机。
  5. 屏幕没亮：`screen on (key)` 有、`panel: the renderer could not turn it on …`、`phone-paint: panel on in … ms, but …` 或 dmesg 里有 DSI、DPU 的错，是显示在恢复以后起不来（§9.1 的显示链路，不是唤醒）；没有 `sleep: woke after`，就等 15 分钟（Shell 最迟 15 分钟让 RTC 叫醒一次），USB 回来了再照第 3 步取；还不回来就长按电源键重启，`journalctl -b -1 -u phone-display | grep -E 'sleep:|screen'` 和 `wake-loop.txt` 的最后几行（睡着时它不写，醒来又写）告诉我们醒没醒过。
- 跑，关机（整机，[28](28-battery.md) §7）：这块内核的关机走 PSCI 的 `SYSTEM_OFF`，`pm_power_off` 不是空的；上次 `systemctl poweroff` 2 秒后又开机时插着 USB 线，6T 插着充电器时 PMIC 可能自己又开机（ABL 的 `off-mode-charge:1`）。分开试：
  1. 拔着线：`systemd-run --on-active=20 systemctl poweroff`，20 秒内拔掉 USB 线，等两分钟：手机一直关着才算关机成功。按电源键开机以后，`$W facts` 的 `== watchdog and the last boot's end` 一节：上一次启动 journal 的最后几行（`systemd-shutdown[1]: Powering off.`），pstore 里上一次的内核控制台最后一行：`reboot: Power down` 是内核调了 PSCI 关机，`reboot: System halted` 是内核没有关机的办法（那样手机停在原地，不会重启）。
  2. 插着线：同样关机，记下几秒以后又亮、屏幕上是充电的画面还是开机，`journalctl --list-boots` 多了几次启动。2026-11-26 以后构建的镜像应该进关机充电（下一条），不进 Shell。
  3. 记下：拔线、插线各自关没关住，几秒后又开机，pstore 最后一行。
- 跑，关机充电（整机，[28](28-battery.md) §10，**未上真机**；2026-11-26 以后构建的镜像）：
  1. 插着线 `systemctl poweroff`，用秒表记：关机到屏幕重新亮几秒，亮起来时屏幕上是什么（应该：引导程序的画面以后是待机界面式的大电池和百分比、「正在充电，N%」、下面一行充电电流（如「1520 mA」，bq27411 报 `current_now` 才有）、「长按电源键开机」，上面一条状态栏，别的没有，没有 Shell）。
  2. 等它熄屏（应该 5 秒），短按电源键：亮 5 秒又熄。按住电源键：约 2 秒后提示「正在开机…」，手机重启进 Shell（按住太久会碰上 PMIC 的复位，§14.10 上面「按住电源键不放」；记下两者谁先到）。
  3. 进 Shell 以后：`journalctl -b -1 -o short-precise | grep -E 'phone-charger|androidboot.mode'`：应该有 `phone-charger-generator: androidboot.mode=charger: booting phone-charger.target`，`phone-charger` 每按一次电源键一行（`power key: pressed`、`power key: held`），最后一行 `reboot`；`journalctl -b -1 -u ModemManager -u phone-display -u iwd -u bluetooth` 是空的（没起过），`journalctl -b -1 -k | grep -e q6v5 -e ath10k` 也是空的（基带、Wi-Fi 没加载）。
  4. 再插着线关机，进了充电画面以后拔线：屏幕写「充电器已拔出，即将关机」，3 秒后关机；等两分钟，手机一直关着。按电源键开机，`journalctl -b -1 | tail`：有 `charger unplugged: powering off in 3s unless it comes back`、`poweroff`，最后是 `Powering off.`。
  5. 充电画面亮着时 USB 控制台在不在（bring-up 镜像的 `phone-bringup-usb` 挂在 sysinit.target 上，应该在；唤醒锁 `phone-bringup-awake` 挂在 multi-user.target 上，不在）：在的话 `cat /run/phone/charger` 是一行 JSON（屏幕、面板、电量、`charging`、`status`、`current_ua`、`sleep`），`systemctl list-units --state=active | grep -c service` 记下数，`cat /sys/class/power_supply/*/current_now` 记下充电电流和 Shell 里同样插着线时比。充电电流（2026-11-26 以后构建的镜像，**未上真机**）：屏幕上那一行的 mA 和 bq27411 的 `current_now`（µA）除以 1000 一样；记下 `current_now` 充电时是正还是负（sysfs 的规定是正；屏幕只写大小）、电脑的 USB 口和原装充电器各多少 mA。
  6. 熄屏挂起（2026-11-26 以后构建的镜像，**未上真机**）：USB 控制台里 `journalctl -b -u phone-charger -o short-precise | grep sleep:`：开头一行 `sleep: mem 5s after the screen goes off; woken by the power key, the RTC every 10m0s and the chargers …`（列着 PMI8998 的充电器；写的是 `sleep: off (…)` 就记下为什么）。熄屏 10 秒以后 USB 控制台跟着手机睡了（断开）。按一下电源键：屏幕亮、USB 回来，`sleep: woke after … (irq …)`，中断号是 `pm8941_pwrkey` 那一行的（§14.10 上面「电源键叫醒」）。再等它睡，拔下充电器：应该几秒内醒来、写「充电器已拔出，即将关机」、3 秒后关机（`journalctl -b -1 -u phone-charger` 里 `sleep: woke after … (irq 121)` 一类、`charger unplugged`）；不醒的话，最迟 10 分钟后 RTC 叫醒它再关机，记下用了多久。插着线放半小时，`cat /run/phone/charger` 的 `sleeps` 大约是 3（每 10 分钟一次 RTC），电量在涨。充到 100% 以后屏幕上写「已充满」时 bq27411 的 `status` 是 `Full`。
  7. 电量太低不开机（可选，要把电池用到 3% 以下，**未上真机**）：Shell 里用到 2% 左右关机，插上线进关机充电：那一行是「正在充电，2%，电量过低，无法开机」；按住电源键 2 秒：不重启，提示「电量到 3% 时开机」；接着充，到 3% 自己重启进 Shell（`journalctl -b -1 -u phone-charger | grep battery`：`battery at 2%: too low to start; starting at 3%`、`battery at 3%: starting`）。记下从 2% 到 3% 用了几分钟、按住前和重启前 bq27411 的 `voltage_now`（USB 控制台里 `cat /sys/class/power_supply/bq27411-0/voltage_now`）、开机途中有没有掉电。
  8. 从电源菜单关机（2026-11-27 以后构建的镜像，报告 008，§16.14，**未上真机**）：不接键盘，插着线按住电源键打开电源菜单，用手指点「关机」：应该先出「要关机吗？」，点「关机」。手机关掉、又被充电器开起来，进的是关机充电（第 1 步那样），不是 Shell。按住电源键开机进 Shell 以后：`journalctl -b -2 | grep 'phone-shell: power'` 有 `power menu: poweroff tapped; asking again` 和 `power: poweroff`（出的是「要重启吗？」、记的是 `reboot` 就是点到了「重启」：记下手指点在哪一行）；`journalctl -b -1 -k | grep phone-charger-generator` 有 `booting phone-charger.target`；上一次启动的命令行（`journalctl -b -1 -k | grep 'Kernel command line'`）里 `androidboot.mode=charger` 后面还有 `androidboot.mode=normal` 时也照样进了关机充电。
  9. 记到：[28](28-battery.md) §10 的「未上真机」几条（亮屏几秒、熄屏、长按、拔线关机、充电电流和它的正负、挂起和插拔叫醒、低电量不开机、从电源菜单关机），本文 §8 表「关机、重启」一行。
- 跑，重启和看门狗（整机，[28](28-battery.md) §7）：板子的 `[watchdog]` 让重启时 systemd 把 `qcom_wdt` 设成 30 秒（它最多 32 秒，systemd 默认的 10 分钟它不收）。`reboot`，回来以后 `journalctl -b -1 | grep -iE 'watchdog|systemd-shutdown'`：应该有 `Using hardware watchdog /dev/watchdog0: 'qcom_wdt'` 和 `Watchdog running with a hardware timeout of 30s`，没有 `Failed to set watchdog hardware timeout`；`journalctl --list-boots` 里两次启动之间没有一次只活了几秒的启动。那次多出来的启动在 `fastboot reboot` 以后也有（那时 Linux 和 systemd 都不在跑），所以不会全是看门狗的事：还有的话记下来，再在 `/run/systemd/system.conf.d/90-test.conf` 里写 `[Manager]` 和 `RebootWatchdogSec=off`、`systemctl daemon-reexec` 以后重启一次对比；`fastboot reboot` 也照样数一次。
- 跑，电源键和保护套归 Shell（整机，[28](28-battery.md) §7）：`busctl get-property org.freedesktop.login1 /org/freedesktop/login1 org.freedesktop.login1.Manager HandlePowerKey HandleLidSwitch` 是 `s "ignore"` 两次；屏上的 Shell 里按一下电源键熄屏，再按一下亮屏，按住长按时间（默认 600 ms）出电源菜单，手机不关机；电源菜单开着时短按一下电源键关掉它、屏幕还亮着，紧接着再按一下熄屏，亮屏后菜单不在了；电源菜单开着不动，等屏幕到时间熄灭，再亮屏时菜单也不在了（[28](28-battery.md) §7，**未上真机**）；磁铁或磁吸保护套盖上霍尔传感器（`Hall effect sensor`，开关事件，logind 把它当盖子），手机不挂起（`journalctl -u systemd-logind` 里只有 `Lid closed.`，没有挂起）。
- 跑，连按电源键（整机，[28](28-battery.md) §7；2026-11-24 那次 `d4856a3` 上连按几下以后黑屏、没有反应，过一会儿再按，花屏一下又亮了，§16.9）。用这棵树 2026-11-25 以后构建的镜像：Shell 的每一下电源键、屏幕和面板的每次变化、`sleep:` 都进 journal，渲染器写电源键的按下、松开、判断和面板每次开关用了多久；以前的镜像只有 `/run/phone/shell.log`，强制重启就没了。
  1. 先（USB 控制台里）：`mkdir -p /var/lib/kario-test`，`dmesg -w > /var/lib/kario-test/power-dmesg-live.txt &`。
  2. 屏上的 Shell 在待机界面，尽量快地连按电源键十下，停手看屏幕；再五下；再一下；再按住约一秒（出电源菜单，短按一下电源键关掉）。每次停手后数一下屏幕是亮是灭。
  3. 应该：十下以后亮着，五下以后灭，一下以后亮；短按从不出电源菜单，按住一秒出；屏幕不会黑着不动。日志里每一下一行 `power key: short press (held … ms): …`，没有少、没有多；面板一次只换一回（`panel: asking the renderer to turn it …` 后面紧跟 `panel: … in … ms`，之后才有下一个 `asking`）。
  4. 黑屏又出现（黑着、按键没反应）时**先别重启**，USB 控制台里依次抓（不要连着按电源键）：

     ```
     T=/var/lib/kario-test
     journalctl -b -u phone-display -o short-precise | grep -E 'power key|screen (on|off)|panel|sleep:|renderer|drawing in place|page flip' | tail -100 > $T/power-journal.txt
     cp /run/phone/shell.log $T/power-shell.log; cp /run/phone/gui.log $T/power-gui.log
     dmesg | grep -iE 'dsi|dpu|drm|backlight|panel|pwrkey|flip_done|timed out' | tail -100 > $T/power-dmesg.txt
     grep -H . /sys/class/backlight/*/bl_power /sys/class/backlight/*/brightness /sys/class/backlight/*/actual_brightness /sys/class/graphics/fb0/blank /sys/class/drm/card*-*/dpms /sys/class/drm/card*-*/enabled /sys/class/drm/card*-*/status > $T/power-sysfs.txt 2>&1
     for p in $(pidof phone-shell renderer phone-paint); do echo "== $p $(tr '\0' ' ' < /proc/$p/cmdline)"; for t in /proc/$p/task/*; do echo "${t##*/} $(cat $t/wchan)"; done; cat /proc/$p/stack; done > $T/power-ps.txt 2>&1
     ```

     然后按一下电源键，记下屏幕怎样（亮了、花屏一下再亮、还黑）和过了几秒，再跑一遍第一行，写到 `power-journal-after.txt`。还黑就照 §16.5 的办法手动恢复（`echo 0 > /sys/class/graphics/fb0/blank`），记下哪一步管用。
  5. 看什么：Shell 和渲染器对不对得上（Shell 最后的 `screen on|off`、`panel: on|off`，渲染器最后的 `phone-paint: panel … in … ms`）；面板每次开关用了多久；有没有 `panel: the renderer has not answered in 2s`（渲染器卡在内核里：`power-ps.txt` 里它各个线程的 `wchan` 和内核栈）、`could not turn it on`、`but no page flip completed in 500 ms after the panel came on`（面板没起来）、`a page flip was still on its way`、`drawing in place`、`page flip: …`；dmesg 里同一时刻有没有 `DSI PLL(0) lock failed`、`dpu_encoder_phys_cmd_ctl_start_irq` 超时、`flip_done timed out`；`bl_power` 是 0 还是 4（4：面板驱动没开背光，面板没开成）、`fb0/blank`、连接器的 `dpms` 和 `enabled`。
  6. 记到：[28](28-battery.md) §7 的状态行（「连按电源键……**未上真机**」）；本文 §8 的「屏幕」「按键」两行；量到的面板开关耗时交给机主（§14.1）。
- 跑，时钟（整机，[28](28-battery.md) §6，**未上真机**）：
  1. RTC 一直在走：`cat /sys/class/rtc/rtc0/since_epoch` 和 `date -u +%s` 各记一次；`reboot`，回来再记一次：两边加的秒数一样（差不到 2 秒）。关机（不是重启）放 10 分钟以上再开，再比一次：RTC 关机时也在走。`cat /sys/class/rtc/rtc0/hctosys` 是 1（内核按它设过时钟）。
  2. 偏移文件：先连上 Wi-Fi（或者等 NITZ），`cat /var/lib/slots/rtc-offset.json`：`offset_ms` 约等于 `date +%s%3N` 减 `since_epoch` 乘 1000，`by` 是 `ntp`、`network` 或 `user`；`phone-clock show` 最后一行「RTC 加偏移」离系统时钟不到 1 秒。设置 › 日期和时间关掉自动时间、手动设一个时间，文件的 `by` 变成 `user`，再打开自动时间。
  3. 不联网重启：关掉 Wi-Fi、拔掉 SIM 卡（或飞行模式）重启：`journalctl -b -u phone-clock` 有 `the clock set from rtc0 plus the offset kept: …`，开机后的时间和电脑的差几秒以内（不是 1970，也不是上次关机的时间）；`systemd-analyze critical-chain phone-display.service` 里 phone-clock 在 Shell 之前。
  4. 掉过电的 RTC（可选，放到最后）：电池用到自己关机、再充电开机，RTC 有没有从头数（`since_epoch` 变小）；从头数了的话 `phone-clock` 写着 `started over` 不动时钟，联网后时间又对、偏移重存。
  5. 唤醒闹钟：挂起前 `cat /sys/class/rtc/rtc0/wakealarm` 是 RTC 自己的时间加几分钟（`+N` 写进去的），闹钟准时响（上面挂起那条）。
- 跑，电流（chroot，[24](24-sku-framework.md) §6.3 第 7、8 项）：`$K power-start`，拔掉 USB、息屏一小时以上，`$K power-stop`；开着 Wi-Fi 和飞行模式各一次，Nura 自己待机一次做对照；亮屏电流 `$K power-sample=60`。
- 应该：bq27411 电量计是 Battery 类的第一个；6T 快充用 Dash，不是 USB PD，Kario 只该说「正在充电」（[27](27-usb-typec.md) §1，Dash **未实现**）；6T 没有通知灯，息屏时来短信屏幕亮起（[28](28-battery.md) §2）；`/sys/power/state` 里有 `mem`；电源键、基带的来电和短信（2026-11-26 以后构建的镜像，§16.12）、RTC 都叫得醒它，闹钟照常响。
- 记到：[28](28-battery.md) 的状态行、§1 表「来电、闹钟、插上电源」一行、§2 表「没有灯的板」一行、§5 表「挂起」一行、§6（时钟）的状态、§7（电源键、看门狗、关机）的状态、§8 的前两条和最后一条；板子文件的 `rtc.offset`（RTC 关机、重启都在走就删掉）、`watchdog.reboot`（30 秒收下了、没有多出来的启动就删掉）；[41](41-design-gaps.md) §11 的时钟那一条；[27](27-usb-typec.md) §1；[37](37-motion.md) §3 表里充电的电池（「QEMU 上模拟的电池不充电，没看过」）；本文 §8 的「电池」「休眠」两行。
- 不要：挂起时以为 USB 控制台断了就是死机（它跟着手机睡了，按电源键或等它醒）；插着 USB 线量待机电流；拿 `wakeup_count`、`event_count` 是 0 当作电源键没叫醒（第 4 步）。

### 14.11 按键：6T 没有九宫格

- 跑：蓝牙 HID 键盘当九宫格，照 [24](24-sku-framework.md) §7.4 的表逐个按：F1 或 Tab 左软键、F2 右软键、Enter OK、Esc 返回、Backspace 清除、F5 通话、F4 挂断、F3 切换应用、F6 中英、F7 符号、F8 F9 音量、数字和 `*` `#` 是九宫格的键（按住 600 ms 是长按）；手机自己的电源键（短按亮屏、息屏，按住长按时间，默认 600 ms，是电源菜单）、音量键（调音量，通话中调通话音量）；chroot 的 `facts` 里 `== input` 有 `pm8941_pwrkey` 和 `Volume keys`。键盘断开再连上以后照样认（[27](27-usb-typec.md) §2）。
- 跑，只有电源键和音量键的电源菜单（[28](28-battery.md) §7，**未上真机**）：用这棵树 2026-11-25 以后构建的镜像。先断开蓝牙键盘（`journalctl -b -u phone-display | grep 'phone-shell: input:'` 里它最后是 `removed`；`pm8941_pwrkey` 和音量键那几行写着 `no OK key`，没有的话把这几行交给机主）。
  1. 按住电源键出电源菜单：第一行写着「音量键移动 · 电源键选择」，选着「取消」；短按电源键，菜单关掉，屏幕还亮着。
  2. 再按住出菜单，音量下键按一下、音量上键按一下（选中项下移又回到「取消」，状态栏下面不出「铃声音量」），短按电源键：菜单关掉，手机不关机。
  3. 再按住出菜单，音量下键按三下到「打开飞行模式」，短按电源键：飞行模式打开（状态栏 ✈）；用同样的办法（音量下键三下到「关闭飞行模式」）关掉它。
  4. 连上蓝牙键盘（日志里它是 `added`，没有 `no OK key`），按住电源键出菜单，键盘的下键移到「关机」，短按电源键：只关掉菜单，手机不关机；音量键这时调音量。键盘的 Esc 和 Enter（选着「取消」时）也关掉它。
  5. 最后（要关机时再做）：断开键盘，按住出菜单，音量下键一下到「关机」，短按电源键，手机关机。
- 应该：表里每个键都对。HID 键盘没有 Fn 层，全键盘的 Fn、Shift 点按和锁定在这里试不到。电源菜单照上面五步走，没接键盘时只用电源键和音量键就能关掉它、选中每一项，接着键盘时电源键从不选中菜单里的项。
- 记到：[24](24-sku-framework.md) §7.4「整机类主板……**未上真机**」和板子文件的 `input.buttons`；[28](28-battery.md) §7 的状态行（「电源菜单……**未上真机**」）；本文 §8 的「按键」一行。
- 做不了：整机自带键盘的 `[keymap]`（KaiOS 一类的矩阵，6T 没有）、[18](18-nokia-form-factors.md) 的全键盘矩阵（§14.16）。

### 14.12 沙箱和内核特性（兼容测试报告的 `sandbox` 字段）

- 跑：两条路各一次。chroot：`$K selftest`（整套，几个小时），或者 §14.1 的 `--only '^(内核特性|沙箱)'`；整机：§14.1 的 `--only '^(内核特性|沙箱)'`（不加 `^` 会连标题里提到沙箱的网页视图、USB 用例一起选上）。在电脑上看报告的 `sandbox`（`kernel`、`missing`、`fallback`）：`python3 -c 'import json,sys; print(json.dumps(json.load(open(sys.argv[1]))["sandbox"], ensure_ascii=False, indent=1))' <cts.json>`；Shell 的日志里有一行 `kernel: Linux …, cgroups …, Landlock ABI …; N protections missing, M done another way`（整机 `journalctl -u phone-display | grep kernel:`）；设置 › 沙箱自检（[17](17-app-sandbox.md) §6）。
- 应该：整机（Kario 的内核：Nura 的配置加 Kario 的片段，§10）上「内核特性」用例通过，`missing`、`fallback` 都是空的，「沙箱自检 13/13 符合预期」，「监听 TCP 端口」显示「拒绝（Landlock）」。chroot 里是 Nura 自己的内核配置，缺什么照实记（`facts.txt` 的 `== kernel` 有 `lsm:`、Landlock、seccomp、cgroup 的配置）；那里「内核特性」带着这份清单跳过（报告的 `sandbox.foreign` 写明内核不是镜像的），「监听 TCP 端口」显示「拒绝（无网络）」（没有 Landlock：端口绑得上，但谁也连不进来）。
- 记到：[24](24-sku-framework.md) §7.5（「QEMU 的主线内核上什么都不缺」后面加 6T 的两种内核）；[17](17-app-sandbox.md) §7 的表（加一行「一加 6T」）；[13](13-prototype.md) §5「内核特性」「沙箱：……」几行。

lockdown 和模块（整机；chroot 里的内核是 Nura 的，那边不跑）：

- 跑：`cat /sys/kernel/security/lockdown`；`ssh root@172.16.42.1 /usr/libexec/phone/module-check.sh`；`dmesg | grep -c 'Lockdown:'`、`dmesg | grep Lockdown: | sort | uniq -c`；`lsmod | wc -l`；`modinfo -F signer nft_masq`。
- 应该：`none [integrity] confidentiality`；`PHONE-MODULES: PASS`（`nft_masq` 和它依赖的加载了，副本被拒绝）；`Lockdown:` 只有 module-check.sh 的副本那一次（`insmod: unsigned module loading is restricted`），没有 `modprobe`、`(udev-worker)` 的；`lsmod` 里有调制解调器、DSP、Wi-Fi、声卡的模块；签名者是 `Kario kernel modules (development)`。
- 记到：§8.3 和 §8 表「模块」一行的「**未上真机**」；[17](17-app-sandbox.md) §7 的表（「一加 6T」那一行写上 lockdown）；[41](41-design-gaps.md) §18。
- 不要：在手机上 `echo none > /sys/kernel/security/lockdown`（降不下来，2026-11-24 试过）；拿别的构建目录的 userdata 配这个启动镜像（模块全部加载不了，§8.3）。
- 不要：把 chroot 的结果当成 Kario 内核的结果（两个内核的配置不同）。

内存上限（[17](17-app-sandbox.md) §5.4：顶在上限上卡住的应用由 Shell 按内存压力结束，阈值是在 QEMU 上定的）：

- 跑：`zcat /proc/config.gz | grep -E 'CONFIG_PSI|CONFIG_LRU_GEN'`（没有 `config.gz` 就看 `ls /proc/pressure`）；`cat /proc/cmdline` 里没有 `psi=0`；§14.1 的 `--only '^兼容测试：应用(超出内存上限|读大量文件)'` 跑 10 遍（chroot 里没有委派的 cgroup 时这两个用例跳过，那就只在整机上）。
- 应该：报告的 `fallback` 里没有 `psi`、`psi-trigger`；「应用超出内存上限」每遍都过，Shell 结束它时日志里有 `stalled at its memory limit` 一行（内核先结束的是 `killed for its memory limit` 一行）；「应用读大量文件」每遍都过，日志里没有它的 `stalled at its memory limit`。
- 记下：每遍「应用超出内存上限」的秒数，谁结束的；`stalled at its memory limit` 那一行的百分比（抖的时候 2 秒里 `full` 占多少，QEMU 上 49%–69%）；「应用读大量文件」时日志里探针的 `at its memory limit` 行的百分比（QEMU 上最多 19%）。6T 的内核开着 MGLRU（`CONFIG_LRU_GEN_ENABLED`），QEMU 的没有，回收的样子不同：两个数靠得太近，就要改 `shell/memstall.go` 的 `stallShare`、`stallFor`。
- 记到：[17](17-app-sandbox.md) §5.4 的「未上真机」；[41](41-design-gaps.md) §6 那一条。

### 14.13 系统更新和 A/B 槽

userdata 布局里没有 Kario 的 A/B 系统槽、dm-verity 和 RAUC（§11）：增量包、断点续装、后台自动更新都验不了（§14.16）。能看的是 Android 的两个启动槽。A/B 布局的检查（分区大小、回 OxygenOS、刷入、开机、系统更新和退回）在 §15。

- 跑：第一次开机以后 `phone-abslot status`；重启进 fastboot，`fastboot getvar current-slot`，`fastboot getvar all` 里两个槽的 `slot-successful`、`slot-retry-count`、`slot-unbootable`；设置 › 系统更新写着什么。可选，只在 `boot_a`、`boot_b` 都是 Kario（`flash` 写了两个槽）、两个槽的固件一致（§3.2）时：`fastboot --set-active=b`，开机后 `phone-abslot status` 里 b 槽是 `successful`。
- 应该：这次启动的槽 `successful`，bootloader 报的和 `phone-abslot` 写的一样；设置说这台手机装不了系统更新（`system.update` 的 `supported` 是 false）。
- 记到：本文 §1 表「A/B 槽」一行、§8 表「槽」一行；[41](41-design-gaps.md) §3。
- 不要：`fastboot --set-active` 到一个不是 Kario 的槽；手工改 GPT 的属性。

### 14.14 辅助功能：按键计时、放大、屏幕阅读器

- 跑：设置 › 辅助功能 › 按键（[19](19-core-apps.md) §1.4）：改了重复延迟和速率以后按住方向键，看重复的快慢（渲染器自己重复，内核的重复丢掉）；改了长按时间以后长按数字键；慢速键（按住够久才算按下）；粘滞键（Shift 点一下管下一个字母，Ctrl、Alt 点一下管下一个键；Alt + 键是长按）；电源键的长按跟着长按时间，但不短于 500 ms（设成 0.4 秒时是 0.5 秒）。两个音量键一起按：先按下的那个调了一格音量，之后是辅助功能快捷方式（默认是放大）；一起按住到长按时间是 `a11y` 带 `long`（[23](23-renderer-protocol.md) §3）。视觉：高对比度文字、粗体文字、颜色反转、颜色校正、放大，在面板上看（CPU 路径，[23](23-renderer-protocol.md) §4）。屏幕阅读器：打开以后移动焦点，audiod 的日志（整机 `journalctl -u phone-audiod | grep tts`，chroot 在 `shell-logs/` 里）每句一行 `tts (simulated, no voice): …`，打断和排队跟得上按键；「按需朗读」时按住两个音量键才读。
- 应该：每个设置在真键盘、真音量键上的效果和 [19](19-core-apps.md) §1.4 写的一样；屏幕阅读器在手机上**听不到声音**（语音引擎**未实现**，[20](20-audio.md) §11），只看日志。
- 记到：[23](23-renderer-protocol.md) 的状态行和 §3「phone-paint 这样做……**未上真机**」；[19](19-core-apps.md) §1.4 的状态和「按键的设置在真机的键盘上**未上真机**」；[20](20-audio.md) §11 的状态行（屏幕阅读器那一半）。
- 做不了：单声道和左右声道平衡听不出来（§14.16）。

### 14.15 恢复出厂设置和防盗锁（整机，放在最后做）

- 跑：先在手机上记下 `sha256sum /var/lib/phone/keystore/device.key`（只用来前后比对，不写进文档）；设一个锁屏 PIN 码，设置 › 更多设置 › 恢复出厂设置，输入 PIN 码，最后确认；手机重启。然后 `journalctl -b -u phone-wipe`、`dmesg | grep phone-wipe`、`ls -A /var/lib/phone /var/lib/iwd /var/lib/bluetooth`、`ls /var/lib/slots`，再算一次 `device.key` 的 SHA-256。自测 `--only '^恢复出厂设置（设备）'`（在临时目录里跑镜像的 `data-wipe`，不动真的数据）。
- 应该：`phone-wipe.service` 在 `sysinit.target` 以前跑了：`phone-wipe: factory reset: emptying the data in /var/lib`、`phone-wipe: factory reset: done`；Shell 的数据、Wi-Fi 网络、蓝牙配对都没了，`keystore/device.key`、`device-sm2.key`、`factory/` 还在、SHA-256 不变，有一份新的 `keystore/data.salt`，`/var/lib/slots/wipe` 删掉了；开机像新手机（[31](31-lock-and-biometrics.md) §11.1）。蓝牙键盘要重新配对（§14.7）。
- 防盗锁：账号服务器**没有部署**，手机绑不上真的 Kario 账号，锁也就不会真的开；能跑的只有自测的「防盗锁：……」（Shell 里模拟的账号服务器，无头），它不碰硬件。6T 的持久区就在 userdata 上（A/B 布局也是，[43](43-phone-partitions.md) §3.4），重刷 userdata（§6 的 `flash`）连防盗锁的记录一起没了（[31](31-lock-and-biometrics.md) §11.5 末尾）。
- 记到：[31](31-lock-and-biometrics.md) §11 的状态行、§11.5 表（加「一加 6T」一行）；[19](19-core-apps.md) §1 表设置一行里的「恢复出厂设置……**未上真机**」。
- 不要：手工建 `/var/lib/slots/wipe/request`，或者手工跑 `/usr/lib/phone/data-wipe`（有请求时它真的清空 `/var/lib/phone`）；在 chroot 里试（那里没有清除要的开机步骤 `phone-wipe.service`；自测的这个用例在那里自己跳过，`require-systemd`）。

### 14.16 6T 答不了的

- 120 Hz 和切换刷新率（[08](08-display-adaptive-refresh.md) §7 的验收、§9 第 1、3 条）：6T 的屏只有 60 Hz。
- A/B 系统槽、dm-verity、RAUC、增量包、自动更新（[25](25-system-update.md)）：userdata 布局里没有（§11）；A/B 布局照 §15 做。
- NFC（控制器归内核的 NFC 子系统，phone-nfcd 打不开原始的 NCI 节点，板子不写 `nfc`）、相机（vivid 代替）、屏下指纹（没有驱动）、光线和距离传感器（hexagonrpcd 是 GPL-3.0）。
- 耳机、单声道和平衡（[20](20-audio.md) §11：6T 只有一个扬声器，没有耳机插孔）、蓝牙通话和蓝牙音频（**未实现**）、USB 主机和 OTG 键盘（USB 只做设备）、USB 解码器和 DSD（[20](20-audio.md) §9）。
- 整机自带键盘的 `[keymap]`、全键盘矩阵和它的 Fn 层（[18](18-nokia-form-factors.md)）。
- 小区广播（[32](32-messaging.md) §7）：自己发不出来；数据漫游：要出境。
- TEE、锁住的 bootloader、量产的设备密钥（[31](31-lock-and-biometrics.md) §11）：**依赖硬件**，6T 上不做。
- 防盗锁、家长远程管理对着真的账号服务器：服务器**没有部署**。

### 14.17 触摸屏：第二种输入（**未上真机**）

触摸屏换成按键（[10](10-keyboard-interaction-model.md) §2.1）：测试时可以不接蓝牙键盘翻页、打开、返回。驱动（Synaptics RMI4 的 I2C 和 F12）编进了内核（`phone-fajita.cfg`，`tools/sku.py` 核对），不用加载模块；chroot 里是 Nura 的内核加载它自己的 RMI4 模块，`sku.json` 的 `touch` 一样，phone-paint 一样读。

- 跑：不接蓝牙键盘，按电源键亮屏；待机界面点软键栏中间（主菜单），点一格打开应用，点标题栏、从左边缘往右划各返回一次；设置里上下拖（正文跟着手指滚，焦点留在看得见的一行上），主菜单上拖（都看得见：什么也不动），点一行打开，长按一行（选项菜单出来），点菜单外面（关掉）；来一个电话（另一台手机打来），点「接听」（接通以后贴着耳朵时点屏幕没反应，挂断和唤醒触摸见 §14.23），再来一个点「拒接」；息屏后点屏幕。2026-11-26 以后构建的镜像（报告 007 改的）另外：从主菜单打开设置，点标题栏（回到主菜单，不是待机界面）；通知中心和相机的取景框里从左边缘往右划（返回）；图片里左右划、拇指随手一划（换一张，带滑动）；放一段视频，点进度条的中间、四分之三处（跳到 50%、70%）。看 `journalctl -u phone-display | grep -e touchscreen -e "input:"`（chroot：Shell 的 `gui.log`）、`grep touch: /run/phone/shell.log`（每个拖动和划一行：手指走了多远、多久、从哪里开始、认成了什么）和 `dmesg | grep -i rmi4`。
- 应该：手指落在哪就是哪：左上角点的是左上角，没有左右、上下颠倒或 x、y 对调（触摸屏的坐标照内核报的范围铺满 1080×2340 的面板，phone-paint 去掉上下各 80 行留黑的，落在那些行上的不理）；`dmesg` 里 RMI4 的 F01、F12 都探测到，没有 `Failed to read` 一类的错；`/sys/kernel/security/lockdown` 照旧 `[integrity]`；长的页面拖动时正文跟着手指，松开不弹回；整页都看得见时拖动焦点不动；从边缘划的手指在日志里起点 x 小于 20、横着划到 24 以上就是返回，认不出时日志的那一行说为什么（太短、不够横、不是从正文开始）；长按 600 ms 出菜单；息屏时点屏幕不亮屏，亮屏以后第一下就算数；键盘锁着时点了没反应，来电照样能点；双指同时按只跟第一根。
- 记到：板子文件的 `unverified` 删掉 `display.touchscreen`；本节和 [10](10-keyboard-interaction-model.md) §2.1 的「**未上真机**」。坐标颠倒或对调：在设备树的 RMI4 节点上改（`touchscreen-inverted-x` 一类的属性），不在渲染器里补；没有触摸设备：`dmesg | grep -i rmi` 看探测失败在哪（供电 `vreg_l28a_3p0`、`ts_1p8_supply`，复位）。
- 屏幕上的九宫格（2026-11-27 以后构建的镜像，[10](10-keyboard-interaction-model.md) §2.1）：不接蓝牙键盘，打开闹钟，新建闹钟：时间框有焦点，屏幕下方出只有数字的键盘，点 0 7 3 0；点标签框换成九宫格，点 `#` 到 Abc，点 2 两下、← →、退格；点 `#` 回到拼音，点 9 4 6 6 4，候选栏出在键盘上面，点 OK 选字；点标题栏（先关掉联想，再收起键盘，页面还在），再点标签框（键盘回来），点 OK（焦点到保存按钮，键盘收起），点软键栏中间保存；再接上蓝牙键盘，点一个输入框：不出键盘。看 `grep keypad: /run/phone/shell.log`（每次换样子一行）。应该：键的字看得清，按得准（一个键约 15 × 9.5 mm），点哪个键打的是哪个；连按同一个键在 1 秒内换字母；按住 0 在号码框里是 `+`；页面的焦点行没被键盘挡住。记到本节和 [10](10-keyboard-interaction-model.md) §2.1 的「**未上真机**」；键太小、太挤或点偏了，记下哪个键、点在哪里，在 `paint/src/paint.rs` 的 `KEY_H` 和 `keypad` 改，不在这里补。
- 不要：chroot 里 Nura 的显示管理器还在跑时测（它的界面也读触摸屏；`setup` 停掉它）；拿蓝牙键盘的键和触摸混着测同一个问题（分不清是谁的）。

### 14.18 提醒滑块：声音开关（**未上真机**）

侧面的三段式提醒滑块是主线设备树里的 gpio-keys「Alert slider」，报 `ABS_SND_PROFILE`（[24](24-sku-framework.md) §6.1）；板子的 `[input] sound_switch` 写着它，phone-paint 启动时读它的位置，之后每拨一下报给 Shell，Shell 照它定声音模式：上静音（来电和通知不响不振），中振动，下响铃；三档闹钟都照常响（[20](20-audio.md) §4.1）。gpio-keys 编进了内核（`CONFIG_KEYBOARD_GPIO=y`，`phone-fajita.cfg`，`tools/sku_test.py` 核对）；chroot 里是 Nura 的内核，设备树一样。

- 跑：`facts` 看 `== input`；`cat /proc/bus/input/devices` 找 `N: Name="Alert slider"`。滑块拨到中间开机（或重启 phone-display，chroot 里重开 Shell），再拨到上、下、中各停几秒；每一档：看状态栏、提示条；打开「设置 › 声音与振动」看那一行；用另一台手机打进来听、摸，挂掉；滑块在上（静音）时用「时钟」定一个一两分钟后的闹钟，等它响。来电响着时把滑块从下拨到中、再拨回下。最后在 Shell 的自测里跑这一项：`--only 声音开关`（它拨的是 Shell 里的位置，不动滑块；跑完放回滑块所在的那档）。看 `journalctl -u phone-display | grep -e "sound switch" -e "vibrate "`（chroot：Shell 的 `gui.log`）。
- 应该：`facts` 的 `== input` 有「Alert slider」，abs 位里有 `0x22`（`ABS_SND_PROFILE`），没有键；开机后日志第一行 `sound switch: vibrate`（开机时的位置，没有提示条），之后每拨一档一行、一个提示条（「静音」「振动」「响铃」），拨的方向和档对得上（上静音、中振动、下响铃，和手机侧面印的一样）；状态栏在网络旁边：振动是一部手机两边带抖动的线，静音是一个划掉的铃铛，响铃什么也没有；「设置 › 声音与振动」里是「声音模式」一行，写着当前的档，下面写「由手机侧面的声音开关设定」，没有开关可按，开着这一页拨滑块它跟着变；来电：响铃档响，振动档只振，来电界面写「静音模式 · 振动」，静音档不响不振，写「静音模式 · 不振动」（6T 的马达没做，§11：摸不到振动时看日志，振动档来电每 1.5 秒一行 `vibrate 800 ms`，静音档没有）；闹钟在静音档照常响、照常振；响着的来电拨到中，铃声停、开始振，拨回下又响；拨得快（来回拨几次）最后停在哪一档就是哪一档。
- 记到：板子文件的 `unverified` 删掉 `input.sound_switch`；本节、[20](20-audio.md) §4.1、[24](24-sku-framework.md) §6.1 的「**未上真机**」。档和位置对反了：改 `sound_switch.rs` 之前先看内核报的值（`evtest` 或 `cat /dev/input/eventN | xxd`），设备树写的是上 `SND_PROFILE_SILENT`；没有这个设备：`dmesg | grep -i gpio-keys`；拨了不报：看 `/sys/kernel/debug/gpio` 里 126、52、24 三个脚的电平变不变。
- 不要：chroot 里 Nura 自己管滑块的服务还开着时测（分不清谁定的模式）；拿 Shell 自测的结果当滑块好用（自测不动硬件）。

### 14.19 睡眠和一夜的耗电（整机，报告 007 以后；**未上真机**，整项**未跑**）

用 2026-11-26 以后这棵树构建的整机镜像（`phone-bringup-awake` 只在电脑配置着 USB 控制台时持锁，Shell 记 `sleep: awake (…)`，基带的通道能叫醒手机，§16.12）。手机插着 SIM 卡、连着 Wi-Fi，移动数据开着。下面「USB 控制台里」都是 `ssh root@172.16.42.1`；手机睡着时控制台不通，醒来以后约几秒回来。结果写进 `/var/lib/kario-test/`，取回时照 §14.1，序列号、IMEI、地址不要抄进报告。

1. 插线和锁（先做，几分钟）：
   - 插着电脑：`/usr/libexec/phone-bringup/awake check` 打出 `held: a computer has configured kario-bringup on a600000.usb`；`cat /sys/power/wake_lock` 是 `phone-bringup`；`journalctl -b -u phone-bringup-awake` 有 `holding the wake lock phone-bringup`。
   - `$W facts`（§13.5）照样跑完，`== sleep` 一节有 `phone-bringup-awake: held: …`，`wakeup_count` 那一行是 `<no answer in 2 s>` 是正常的（锁持着）。
   - 拔掉线，等 10 秒，插回电脑：`journalctl -b -u phone-bringup-awake -o short-precise` 里拔线后 2 秒内一行 `not holding the wake lock phone-bringup, the phone may sleep: …`，插回以后一行 `holding …`（拔着线时若已经睡了，插回时手机未必醒：插线中断会叫醒它，也可能等下一次唤醒，记下哪一种）。
   - 插充电器（不是电脑）：亮屏、提示照 §14.10；30 秒以后它该睡：`awake check` 那时打的是 `not held: … is powered (no computer has configured it)`（换回电脑看日志）。
   - 记下：每一行对不对，拔线到放开、插上到持锁各几秒。
2. 一夜的耗电（拔着线，8 小时左右）：
   - 拔线前（USB 控制台里）：

     ```
     T=/var/lib/kario-test
     snap() { date; grep -H . /sys/class/power_supply/*/capacity /sys/class/power_supply/*/status /sys/power/suspend_stats/*; cat /sys/kernel/debug/wakeup_sources 2>/dev/null || for w in /sys/class/wakeup/wakeup*; do echo "$(cat $w/name) $(cat $w/event_count) $(cat $w/wakeup_count) $(cat $w/active_time_ms)"; done; }
     snap > $T/night-before.txt 2>&1
     ```

     `wakeup_sources` 要 debugfs（`mount -t debugfs none /sys/kernel/debug`，lockdown 下只读的它读得到，§7），没有就用 `/sys/class/wakeup` 那几列（名字、事件数、叫醒数、持有的毫秒数）。
   - 拔掉线，按电源键熄屏，屏幕朝下放一夜，不碰它。记下拔线的时间和电量（状态栏）。
   - 早上：先看电量，再插回电脑，`snap > $T/night-after.txt 2>&1`，`journalctl -b -u phone-display -o short-precise | grep -E 'sleep:|screen (on|off)' > $T/night-shell.txt`，`journalctl -b -u phone-bringup-awake -o short-precise > $T/night-awake.txt`。
   - 应该：`suspend_stats/success` 多了几十次（最迟 15 分钟一次例行唤醒，加上推送、来电），`fail` 不多；`night-shell.txt` 里一串 `sleep: mem until …` 和 `sleep: woke after …s (…)`，醒着的时间短；没有长时间的 `sleep: awake (…)`（有的话括号里就是不睡的原因：`wake lock …`、`app … awake`、`sound`……）；一夜掉电只有几个百分点（**没有量过**，记下实际的数）。`wake-up sources` 里谁叫醒得多（比如基带的 `…IPCRTR…` 每隔几分钟一次）也记下来：这是下一步省电要看的。
   - 掉电还是很多：看 `night-after.txt` 里 `success` 有没有动；没动就是没睡，看 `night-shell.txt` 最后的 `sleep: awake (…)` 和 `cat /sys/power/wake_lock`。
3. 睡着时来电、短信、闹钟（拔着线，每项单独做）：
   - 先确认基带的通道能叫醒（USB 控制台里）：`cat /sys/bus/rpmsg/devices/4080000.remoteproc:glink-edge.IPCRTR.-1.-1/power/wakeup` 是 `enabled`（不是的话这一项一定不行，记下，先不做下面的）。
   - 熄屏，拔线，等 1 分钟以上（睡着了；30 秒息屏加上几秒）。
   - 来电：用第二部手机打过来。应该几秒内响铃、亮屏，接得起来。记下从对方拨出到响铃几秒。
   - 短信：再睡 1 分钟以上，让第二部手机发一条。应该几秒内亮屏、出通知。记下几秒。
   - 闹钟：设一个 3 分钟以后的闹钟，熄屏、拔线。应该准时响（差不到几秒）。
   - 每项之后插回电脑：`journalctl -b -u phone-display -o short-precise | grep -E 'sleep:|screen on|call|sms|alarm' | tail -20`：`sleep: woke after …s (irq N; wake-up sources …)`，来电、短信那次的唤醒源里应该有 `4080000.remoteproc:glink-edge.IPCRTR.-1.-1`（或同名的一项），闹钟那次有 `alarmtimer` 或 RTC；括号里只有 `irq N` 时 `grep -E "^ *N:" /proc/interrupts` 是哪个中断，记下来。
   - 不响、不亮：记下过了多久才响（15 分钟内响了，多半是等到了例行唤醒：基带没叫醒它）。
4. 电源键到亮屏（拔着线做）：
   - 先（USB 控制台里）：`echo 1 > /sys/power/pm_print_times`。
   - 拔线、熄屏、等它睡（1 分钟以上），用手机秒表或另一部手机录像，按一下电源键，记到屏幕亮（有画面）几秒。做 3 次。
   - 插回电脑：`dmesg | grep -E 'PM: suspend (entry|exit)|returned 0 after|flip_done|timed out' | tail -60 > /var/lib/kario-test/wake-times.txt`（`pm_print_times` 让每个设备的恢复写一行用了多少微秒，挑大的看），`journalctl -b -u phone-display -o short-precise | grep -E 'sleep: woke|power key: from the renderer|panel: |screen on' | tail -30 >> /var/lib/kario-test/wake-times.txt`。
   - 看：`PM: suspend exit` 到 `power key: from the renderer` 隔多久（内核恢复加渲染器读到键），到 `panel: on in N ms`（面板），再到亮屏；有没有 `flip_done timed out`（正好 10 秒的那种，显示链路的问题）；有 `turning it off and on again` 的话写下来（面板第一次没出画面，Shell 又开了一次，§16.12）。
   - 记下：按下到亮屏几秒（3 次），大头在哪一段。§16.9 的「约 10 秒」量的是 USB 和 SSH 回来，这一次量的是屏幕。
5. 霍尔传感器（有磁吸保护套或磁铁时）：盖上，手机照样会睡（不因为盖上就不睡，也不因为盖上就马上睡）；睡着以后打开保护套：屏幕亮不亮、几秒（gpio-keys 设备树里是唤醒源，打开保护套亮不亮屏看 Shell，**未核实**）。
6. 记到：[28](28-battery.md) §5 表「挂起」一行和 §1 表「来电、闹钟、插上电源」一行的状态；本文 §8 的「休眠」一行；§16.12 的唤醒源表（实测的写「实测」）；一夜掉了几个百分点、几小时，交给机主。
- 不要：插着 USB 线量一夜的耗电（锁持着，手机不睡）；把 `<no answer in 2 s>` 当作坏了（第 1 项）；睡着时连按电源键（第 4 项一次按一下）。

### 14.20 接不了、打不出：q6voiced、服务的顺序和一通电话的日志（**未上真机**）

007 镜像（main）上来电界面出来了，接不了，拨出也不通。日志里 q6voiced 开机时退出（`Connection error: Did not receive a reply`），systemd 重启五次以后放弃；没有它，DSP 里通话的声音通路（q6voice PCM）不开，基带接通了两边也听不见。原因（QEMU 上复现了，见下一段）：上游的 unit 是 `DynamicUser=yes`，只有 `Requires=dbus.service` 没有 `After=`。Kario 的系统总线是 dbus-daemon，systemd 启动它时给 `SYSTEMD_NSS_DYNAMIC_BYPASS=1`（免得总线和 nss-systemd 互相等），所以 dbus-daemon 查不到任何动态用户；认证时它要查连接者的组，查不到就拒绝、断开，libdbus 报 `Did not receive a reply`，总线起来以后的重启也一样。不是缺 nss-systemd：镜像的 `nsswitch.conf` 是 `passwd: compat systemd`，root 和 `messagebus` 都查得到动态用户，只有总线进程查不到。以前 postmarketOS 的镜像用 dbus-broker，它从套接字拿连接者的组、不查用户，q6voiced 在跑。现在：q6voiced 用自己的系统用户 `q6voiced`（`audio` 组）跑，排在总线和 ModemManager 后面，由 `q6voiced-wait` 等到 ModemManager 在总线上再启动（等的是什么写进日志，变了才写一行），失败后一直重启，越来越慢（2 秒到 60 秒），不放弃。ModemManager、rmtfs、tqftpserv、phone-telephonyd 也改成不放弃。接听、拨号失败不再没声音：telephonyd 的日志有 `phone-telephonyd: call cN failed: <原因>`，屏上是「通话失败：<原因>」（不在通话界面时 Shell 说），接了还没接通时电话应用写「正在接听…」。

QEMU 上（qemuarm64 的镜像加测试用的 unit，内核参数 `systemd.wants=` 开机启动）：`DynamicUser=yes` 的服务（加不加 `Group=audio` 都一样）连系统总线报 `Did not receive a reply`，静态用户 `messagebus` 连得上；dbus-daemon 的环境里有 `SYSTEMD_NSS_DYNAMIC_BYPASS=1`。换成 6T 镜像里的 q6voiced 程序（`-cpu max`）：上游的 unit 重启五次以后放弃，错误和 007 一样；加上 Kario 的 drop-in（`q6voiced` 用户）一直在跑，没有重启，等着 ModemManager。以后的镜像不会再带这种服务：构建时 `phone-svcusers.bbclass` 查开机会启动的每个服务（`.wants`、`.requires`、D-Bus 激活，套接字、定时器、路径单元带起来的），系统总线是 dbus-daemon 时用 `DynamicUser=` 的（有同名静态用户的除外，systemd 直接用那个用户），和 `User=`、`Group=` 不在镜像的 `/etc/passwd`、`/etc/group` 里的，都让 `do_image_qa` 失败（`service-users`；`files/phone-svcusers-check.py`，`yocto/svcusers-test.py` 测它，`make test`、CI）。修好以前的代码构建的三个 6T rootfs 上它只报 q6voiced，QEMU 和其他镜像的 rootfs 都通过。

008 镜像上 q6voiced 一直在跑，但开机后的第一通电话两边都听不见；以后的电话、重启过 q6voiced 以后都有声音（报告 008）。第一通的日志是 `Unable to set hw params for PLAYBACK: Invalid argument` 和 `Failed to open rx`。原因（读内核、UCM 配置和 q6voiced 的代码推出来的，**未在真机上核对**）：q6voiced 打开语音 PCM（`hw:0,6`）时，DSP 把声音送到听筒走的 SLIMbus 口，这个口用 codec 的哪条通道由 `SLIM RX0 MUX` 定；6T 的 UCM 配置里只有 `Voice Call` 把它设成 `AIF1_PB`，`HiFi` 不设，谁的关闭序列也不改回去。开机后声卡只进过 `HiFi`，通道是空的，DSP 拒绝，alsa-lib 报 `Invalid argument`。q6voiced 每次 ModemManager 说通话变成拨号中、对方振铃或接通时打开一次，失败了关掉，不再试（来电只有接通那一次）；它听 ModemManager，audiod 听 telephonyd（telephonyd 再听 ModemManager），所以 q6voiced 总比 audiod 换到 `Voice Call` 早一步。第一通里 audiod 换过 `Voice Call` 以后通道一直设着，所以以后的电话和重启了的 q6voiced 都打得开。现在：audiod 开机后第一次启动时经过 `Voice Call` 再设 `HiFi`（[20](20-audio.md) §10），第一通电话之前通道已经设好；单元测试 ✓（旧代码上会失败），**未上真机**。q6voiced 失败了不再试这一点没改（上游的程序，kario-bsp 里打包；给它加重试要在 kario-bsp 里打补丁）：以后别的原因让它打开失败，还是一通没声音，日志里还是那两行。

开机时谁等谁，对方晚了或不在会怎样：

| 服务 | 顺序 | 对方晚了、不在时 |
|---|---|---|
| rmtfs | 没有（`ConditionPathExists=/dev/qcom_rmtfs_mem1`） | 基带开机要读它给的文件系统；退出了每秒重启，现在不放弃，越来越慢（到 30 秒）。设备节点开机时还没有就不启动，基带起不来（007 上有） |
| tqftpserv | 没有 | 同 rmtfs；007 上一直在跑，找不到 `mcfg_hw`、`mcfg_sw` 的 `.dig` 文件是基带在问，不是错 |
| phone-qcom-uim | 在 rmtfs、tqftpserv 后，ModemManager 前 | 自己每 2 秒问一次基带，共 30 次；基带更晚就失败退出，SIM 卡的会话没开（ModemManager 读不到卡），这次开机不再试：`systemctl restart phone-qcom-uim ModemManager` |
| phone-qcom-pdc（只在 dev） | 要 uim，在它后，ModemManager 前 | uim 失败时不跑 |
| ModemManager | 在 uim（和 pdc）后 | 基带在 QRTR 上出现以前 `mmcli -L` 是空的，它自己等；原来只在崩溃时重启、五次放弃，现在失败就重启、不放弃（2 秒到 60 秒）。007 上开关飞行模式时、008 的试用镜像开机时（两次）它崩过（`SEGV`），重启后好了；原因和补丁见下面的「ModemManager 的崩溃」 |
| phone-telephonyd | 在 ModemManager 后（`Wants=`） | 跟着 ModemManager 的总线名字：不在时日志 `ModemManager is not available …; waiting for it`，没有基带时 `ModemManager has no modem yet`，拨号回 `ModemManager is not running` 或 `ModemManager has no modem yet`；ModemManager 回来了自己接上；失败后重启不放弃 |
| q6voiced | 在总线、ModemManager 后 | `q6voiced-wait` 每 2 秒问一次总线 ModemManager 在不在；q6voiced 只听通话的信号，ModemManager 重启以后照样收得到；失败后重启不放弃。语音 PCM 在通话状态变了时打开一次，打不开不再试，所以声卡要在通话之前就能打开它（上一段） |
| 81voltd（只在 dev） | 在 ModemManager、uim、pdc 后 | 失败 5 秒后重启（碰不到 10 秒五次的限制） |
| phone-audiod 的通话路由 | 套接字激活（Shell 启动时第一次连它） | 开机后第一次启动经过 `Voice Call` 设 `HiFi`，设不了时写 `phone-audiod: call verb: …`；自己连 telephonyd，连不上每 0.5 秒再连；telephonyd 晚了只是晚切 UCM；切的错误写 `phone-audiod: call routing: …` |

phone-qcom-abslot、phone-qcom-btaddr 和基带无关。

在手机上（USB 控制台的 SSH，§7）：

1. 先记下时间：`date '+%F %T'`。
2. 打电话之前：
   - `systemctl status q6voiced ModemManager phone-telephonyd --no-pager`：三个都是 `active (running)`。新镜像里 q6voiced 下面是 `q6voiced-wait` 和它启动的 `/usr/bin/q6voiced hw:0,6`；日志里等过的话有 `q6voiced-wait: … waiting`，接着 `q6voiced-wait: ModemManager is up`。
   - `mmcli -m 0`：`state: registered`，`access tech` 是 `lte` 还是 `gsm`；3GPP 一段的 `ue mode of operation`：`csps-1`、`csps-2` 是联合附着，打电话时能回落到 2G（CSFB），`ps-1`、`ps-2` 只有数据，没有 IMS 时 LTE 上打不了电话。007 上中国移动的卡是 `csps-1`、LTE、本地网络。
   - `qmicli -d qrtr://0 --nas-get-serving-system`：`CS: 'attached'` 才能走 CS 语音（2G 或回落），`detached` 时打不了；`Radio interfaces` 是哪个制式。`qmicli -d qrtr://0 --nas-get-system-info`：LTE 那段的 `Domain` 是 `cs-ps`（CS 也注册了）还是只有 `ps`。只读，ModemManager 开着也可以跑。
   - 开机后第一通电话之前：`amixer -c0 cget name='SLIM RX0 MUX' | tail -1` 是 `: values=1`（`AIF1_PB`；上面几行列着各项：0 是 `ZERO`）。新镜像上应该是 1；是 0 时第一通会没声音，`journalctl -b -u phone-audiod --no-pager` 抄下来（有没有 `call verb` 的错）。
3. 打电话时另开一个 SSH：`journalctl -f -u ModemManager -u phone-telephonyd -u q6voiced -u phone-audiod`。
   - 拨第二部手机：telephonyd 的日志依次是 `call c1 out: dialing`、`alerting`、`active`；失败时是 `call c1 failed: …`，屏上「通话失败：…」，把这一行和同一秒 ModemManager 的几行抄下来。中国移动只有 LTE 时，接通前后 `mmcli -m 0` 的 `access tech` 应该变成 `gsm`、挂了以后回到 `lte`（回落）。
   - 第二部手机打过来、接听：`call cN in: incoming`、`call cN: answering`、`call cN in: active`；接不了时是 `call cN failed: answering failed: <ModemManager 的话>`。
   - 接通时：`mmcli -m 0 --voice-list-calls`，`mmcli -o <号>` 的 `state: active`；`cat /proc/asound/card0/pcm6p/sub0/status` 是 `state: RUNNING`（q6voiced 开着语音 PCM，没有它两边都听不见）；`cat /run/phone/audiod-ucm.json` 是 `Voice Call`。
   - 开机后的第一通也要听得见：q6voiced 的日志里没有 `Unable to set hw params` 和 `Failed to open rx`。有的话把这两行、同一秒 phone-audiod 的几行和第 2 步的 `SLIM RX0 MUX` 一起抄下来。
4. 之后：`journalctl -b -u ModemManager -u phone-telephonyd -u q6voiced -u phone-audiod --since "<第 1 步的时间>" --no-pager > /tmp/call.log`。telephonyd 的日志里没有号码；ModemManager 的可能有，发出来之前删掉号码、IMEI、IMSI。
5. 现在的 007 镜像上手工试 q6voiced（重启手机就没了）：
   - `systemctl reset-failed q6voiced; systemctl restart q6voiced; sleep 3; systemctl status q6voiced --no-pager`。还在跑：原因只是开机时排在总线前面；又是 `Did not receive a reply` 退出：是动态用户，接着做下面两步。
   - 证实：`systemd-run --pipe --wait -p DynamicUser=yes -p Group=audio busctl --system status | head -3` 报连接的错，`systemd-run --pipe --wait busctl --system status | head -3` 不报。
   - 不用动态用户跑一次：`mkdir -p /run/systemd/system/q6voiced.service.d && printf '[Service]\nDynamicUser=no\nUser=root\n' > /run/systemd/system/q6voiced.service.d/50-test.conf && systemctl daemon-reload && systemctl reset-failed q6voiced && systemctl restart q6voiced`，`systemctl status q6voiced` 是 `active (running)`，然后照第 3 步打一个电话，两边听不听得见。试完 `rm -r /run/systemd/system/q6voiced.service.d && systemctl daemon-reload`。
6. LTE 上没有 IMS（main 没有 81voltd；VoLTE 见 §14.21）时，只用 2G 试一次：`mmcli -m 0 --set-allowed-modes=2g`，等注册上再照第 3 步打；试完一定改回：`mmcli -m 0 --set-allowed-modes='2g|3g|4g' --set-preferred-mode=4g`。2G 通、4G 不通：回落没有发生，记下第 2 步的 `ue mode`、`CS` 和失败那一行。
7. 008 镜像上证实第一通没声音的原因（不用新镜像）：重启手机，打电话之前 `amixer -c0 cget name='SLIM RX0 MUX' | tail -1` 应该是 `: values=0`；然后 `alsaucm -c hw:0 set _verb "Voice Call" set _verb HiFi set _enadev Speaker`（新镜像的 audiod 开机时做的就是这个），再看一次是 `: values=1`；照第 3 步打第一通，两边听得见、q6voiced 的日志里没有那两行，原因就是这个。不做 alsaucm 那一步时第一通没声音，可以再重启一次对照。
- 应该：q6voiced 开机后一直在跑；拨出、接听都通，两边听得见，开机后的第一通也是；失败时屏上和日志里有原因，不会停在来电或「正在接听…」上；中国移动的卡只有 LTE 时打电话回落到 2G。
- 记到：本节的「未上真机」和上表；§8 的「通话」一行；§16 的实测记录（哪一步、原因那一行）；[04](04-ui-language-telephonyd.md) 「通话」一条。
- 不要：拨紧急号码；改了允许的制式不改回去；把 `/run` 下的试用设置当成修好了（新镜像里是 `q6voiced` 用户，不是 root）。

#### ModemManager 的崩溃（报告 007、008）

007 上开关飞行模式时，008 的试用镜像开机时（两次，相隔 21 秒），ModemManager 死于 `SEGV`（`code=dumped, status=11/SEGV`），systemd 重启它以后好了；它重启时 telephonyd 说 `modem … went away with ModemManager`，81voltd 跟着断言失败退出（008）。镜像里是 meta-oe 的 ModemManager 1.24.2（`mm-1-24` 分支）和 libqmi 1.38.0。交来的日志里没有 ModemManager 自己的话，也没有调用栈；三次都在承载连上、断开的时候：`rmnet_ipa0` 起来、`qmapmux0.N` 出现或消失的同一秒。上游 1.24.2 以后修了两处，都只在 main 分支上，没有进 1.24 的发布：

| 上游提交 | 问题 | 6T 上 |
|---|---|---|
| [9317cbc4](https://gitlab.freedesktop.org/mobile-broadband/ModemManager/-/commit/9317cbc4702c7d6283e42a498cd9812e19eeccf3)（问题 [#1110](https://gitlab.freedesktop.org/mobile-broadband/ModemManager/-/issues/1110)） | netlink 请求的回复回来时，`transaction_complete` 先把请求从表里删掉（释放了），再调它的回调：用了释放过的内存，那块内存被别人用过就是 `SEGV` | 每个承载连上时 ModemManager 经 netlink 把数据口（`rmnet_ipa0`）设成 up、设 MTU；开机时 IMS 和数据两个承载几乎同时连，请求叠在一起。和三次崩溃的时间对得上 |
| [0edcb916](https://gitlab.freedesktop.org/mobile-broadband/ModemManager/-/commit/0edcb916ad2b7267caf73bbd9a31e46da0727a00) | 停下时清理承载，`mm_iface_modem_count_bearers` 数「活动的」承载只拿非复用承载的上限比，连着的复用承载一多就 `g_assert` 失败，`SIGABRT` | 6T 的 IPA 基带只有复用承载（非复用的上限是 0）：有承载连着时停 ModemManager（关机、`systemctl restart`）就会断言失败。日志里没见到（关机时的日志没留下），是读代码推的 |

现在：meta-phone 的 `modemmanager_1.24.%.bbappend` 打上这两个补丁（`Upstream-Status: Backport`）。在 1.24.2 的源码上打得上、没有偏差（`patch -F0`）；**没构建过**（不跑 bitbake），**未上真机**。OE 换到带这两处的 ModemManager 时，这个 append 跟着删掉（文件名只配 1.24）。main 上 1.24.2 以后还修了一处 polkit 在挂起以后的问题（f994d606），只在 ModemManager 自己处理 logind 的挂起时有关；Kario 挂起是直接写 `/sys/power/state`（`hal/pm`），没拿。

在手机上（崩溃的镜像和新镜像各做一次）：

1. `ModemManager --version`：`1.24.2`。
2. `journalctl -b -u ModemManager --no-pager | grep -E 'code=(dumped|killed)'`：新镜像开机、开关两次飞行模式以后应该没有；`systemctl show ModemManager -p NRestarts` 是 `NRestarts=0`。
3. 有崩溃时要调用栈：`coredumpctl list ModemManager`；列得出来就 `coredumpctl info ModemManager --no-pager > /tmp/mm-core.txt`，把 `Stack trace` 一段发过来（删掉号码、IMEI、IMSI 和地址）。`coredumpctl` 没有，或者 `No coredumps found`：`cat /proc/sys/kernel/core_pattern` 和 `systemctl show ModemManager -p LimitCORE` 抄下来。
4. 再崩一次时要它自己的日志：开机后 `mmcli -G DEBUG`（ModemManager 重启以后回到默认），开关飞行模式，`journalctl -b -u ModemManager --no-pager > /tmp/mm.log`，崩溃前 200 行发过来（同样删掉号码和标识）。
- 应该：新镜像上 ModemManager 不再崩，开机和开关飞行模式时 telephonyd 不说 `went away with ModemManager`。
- 记到：本节的「**没构建过**」「**未上真机**」；上表 ModemManager 一行；崩了的话调用栈交给机主，和上表两处对一对是哪一处。

### 14.21 只有 LTE 的中国移动：VoLTE 和「VoLTE 未注册」（**未上真机**）

只有 LTE 的网络上，电话要么走 VoLTE（IMS），要么回落到 2G、3G（CSFB）；没有 2G、3G 时只剩 VoLTE。镜像里有什么：

- dev 的 6T 板子文件是 `ims = "81voltd"`（`sku/boards/oneplus-fajita.toml`），机器的 `PHONE_IMS_PACKAGES` 因此装 81voltd 和 phone-qcom-pdc，两个都开机启动（dev 构建的 6T rootfs 里看过）；main 从 dev 升上来以后一样。pdc-select 按 `pdc-carriers` 的 `460 0,2,4,7,8,13 cmcc chinamobile` 认中国移动的卡，激活基带里描述带 `CMCC` 或 `ChinaMobile` 的配置，VoLTE 的优先（kario-bsp 的 `tests/pdc-select-test.py` 测过：MNC 0 的卡选 `Volte_OpenMkt-Commercial-CMCC` 的最新版本，不选实验室的）。
- 每个 6T 镜像（有没有 81voltd 都一样）装 phone-qcom-ims：`/usr/libexec/phone-telephonyd/ims-status` 用 `qmicli -d qrtr://0 --imsa-get-ims-registration-status` 问基带的 IMS 注册，只打一个词：`registered`、`registering`、`not-registered`、`limited-registered`；基带不答时退出 1，原因写在一行里（kario-bsp 的 `tests/ims-status-test.py`，模拟的 qmicli）。
- 008 上它每次都退出 1：`QMI protocol error (70): 'InvalidOperation'`。IMSA 只答绑定了订阅的客户端：先发 Bind（libqmi 1.36 起有，`qmicli --imsa-bind=<值>`；libqmi 加它的提交说，没绑定时 IMSA 的每个请求都回 InvalidOperation），上面那个脚本（kario-bsp `b5ab9d3` 里的）没绑定。qmicli 一次只发一种请求，QRTR 上的客户端跟着进程的套接字走，进程退出就没了，绑定也跟着没了；所以要两次 qmicli 都经 qmi-proxy（`-p`）：第一次 `--client-no-release-cid --imsa-bind=0` 绑定并留住客户端，第二次 `--client-cid=<它> --imsa-get-ims-registration-status` 问，退出时放掉它。ModemManager 也经 qmi-proxy 连基带（libqmi 1.38 的 QRTR 设备带 proxy 标志打开时走它），它开着时 proxy 里这个设备一直开着，两次之间留住的客户端和绑定都还在；ModemManager 不在时第二次照样是 InvalidOperation，telephonyd 过一会儿再问。这样改的 `ims-status` 和测试（模拟的 qmicli 像 6T 一样拒绝没绑定的请求，旧脚本上失败）在 kario-bsp `f228abe` 里，kario 钉的就是它：**未上真机**。值 0 当作第一张卡的订阅（libqmi 只说它是个 32 位的值），**未验证**。
- telephonyd 找到它就用（日志 `IMS registration from /usr/libexec/phone-telephonyd/ims-status`）：注册在 4G、5G 上时，注册、制式、IMS 承载变了问一次，每次在 4G、5G 上拨号问一次，`registering` 时 5 秒后再问，越来越慢，到 5 分钟为止；状态变了才写一行：`IMS (VoLTE) registration: <状态>`，问不到是 `IMS (VoLTE) registration unknown: <原因>`。
- 拨出的电话在 4G、5G 上没响铃就结束了（不是自己挂的、不是占线、不是紧急呼叫），而 IMS 没注册：这一路以 `failed` 结束，带 `ims`（注册状态）。屏上是「通话失败：VoLTE 未注册」（电话应用里，或者不在通话界面时 Shell 说），telephonyd 的日志是 `call cN failed on 4G with IMS (VoLTE) not-registered: <原因>`；拨号时已经知道没注册的，先写 `call cN out on 4G with IMS (VoLTE) not-registered: it goes through only if the modem falls back to 2G or 3G`。IMS 注册了、在 2G 或 3G 上（回落了）、对方响过铃的，结束原因照旧。模拟的 ModemManager 上 `mm_test.py` 的 `volte_not_registered` 测过；6T 的基带在 QRTR 上答不答 IMSA、81voltd 在中国移动上注册不注册得上，**未上真机**。
- 状态栏和设置：`status` 的 `ims` 是最后问到的状态（没问到、不在 4G 或 5G 上注册时是 `unknown`）。在 4G、5G 上注册着、IMS 注册了时，状态栏在 4G（或 5G）左边写「VoLTE」，用常规字重，比 4G 细；12 键直板机的窄屏中间写着通话时放不下，不画。「设置 › 移动网络」最后一行「VoLTE」写着已注册、正在注册…、未注册、受限或未知。模拟的 ModemManager 上 `mm_test.py` 的 `volte_status` 测过 `ims` 的变化，画面在开发机上看过（phone-paint 的单元测试、自测脚本的「VoLTE 标志」用例），**未上真机**。

在手机上（008 镜像，USB 控制台的 SSH，§7）：

1. `systemctl status phone-qcom-pdc 81voltd --no-pager`：pdc 跑完一次就退出（`inactive (dead)`，上次是 `status=0/SUCCESS`），81voltd 是 `active (running)`。`journalctl -u phone-qcom-pdc --no-pager`：中国移动的卡第一次开机有 `pdc-select: … is active`（或 `… is Active already`），是带 `CMCC` 的配置；以后的开机是 `… was dealt with at an earlier boot (…)`，括号里是当时激活的配置。`the modem holds …` 几行列着基带里有哪些配置。
2. `/usr/libexec/phone-telephonyd/ims-status; echo $?`：`registered` 和 `0`。008 镜像上是 `InvalidOperation` 和 `1`（没绑定，见上），手工绑定了问：`qmicli -p -d qrtr://0 --client-no-release-cid --imsa-bind=0` 应该说 `IMSA bind successful`，下面有 `CID: '<数>'`；接着 `qmicli -p -d qrtr://0 --client-cid=<那个数> --imsa-get-ims-registration-status`，`Status:` 一行就是注册状态（这一次退出时放掉客户端）。绑定被拒时先放掉：`qmicli -p -d qrtr://0 --client-cid=<那个数> --imsa-noop`，再用 `--imsa-bind=1` 试一次；两次的输出都原样抄下来，记下哪个值行。问不到注册时照 §14.6 第 4 步查 81voltd 的日志和它开的承载。
3. `journalctl -b -u phone-telephonyd --no-pager | grep -i ims`：有 `IMS registration from …` 和 `IMS (VoLTE) registration: registered`。
4. 拨第二部手机和 10086：IMS 注册了应该接通，`mmcli -m 0` 的 `access tech` 一直是 `lte`（走的 VoLTE，不是回落）；两边听得见（§14.20 第 3 步）。没注册时：屏上「通话失败：VoLTE 未注册」，日志里有上面那两行，把它们和 `ims-status` 的输出抄下来。
5. 状态栏和设置：第 2 步是 `registered` 时，状态栏 4G 左边有「VoLTE」，「设置 › 移动网络」最后一行「VoLTE」写「已注册」；不是时没有这个标志，那一行写着同一个状态（未注册、正在注册…、受限）。拍一张状态栏的照片。
- 应该：IMS 注册上；LTE 上打得通、接得了；注册不上时屏上说「VoLTE 未注册」，不是光说失败；状态栏的 VoLTE 标志和设置里的那一行跟 `ims-status` 说的一样。
- 记到：本节的「未上真机」；板子文件 `unverified` 里的 `ims`（注册上、打通了才拿掉）；§8 的「通话」一行；[04](04-ui-language-telephonyd.md) 「通话」一条。
- 不要：拨紧急号码；手工换过配置以后把结果当作 pdc-select 选的（它对一张卡只处理一次，`/var/lib/phone-qcom` 里记着）。

### 14.22 和安卓按键机并排比（整机；**未上真机**）

这台 6T 跑 Kario，对面是一台现在卖的安卓按键机（多亲 J36）和第二台刷了 LineageOS 的 6T：同一个人做同样的七个任务，另一部手机以每秒 240 帧录下来；帧时间、启动、内存各用各的工具量；24 小时待机两台并排；数排进同一张表。要买的、公平的规则、每个数怎么取都在 [40](40-testing.md) §9。Kario 这一半是一条命令：`tools/android-compare/kario-half.sh --sku oneplus-fajita-t9 --whole`（经套件跑 `facts`、`--play tools/android-compare/tasks.play perf`、重启静置以后的 `mem`，再写表），人做的任务是 `… tasks mem-apps table`，待机是 `… standby-start`、`… standby-stop table`。

- 记到：`build/android-compare/` 整个目录（每一边一个子目录，`table.md` 是并排的表）交给机主；本节和 [40](40-testing.md) §9 的「**未上真机**」。
- 不要：在 chroot 里量待机（Nura 管电源）；插着 USB 线量待机；待机时让鸿雁连手机自己身上的模拟服务器（消息不走射频，[40](40-testing.md) §9.7）。

### 14.23 通话中挂断和触摸：电源键结束通话（报告 008 §1.2；**未上真机**）

报告 008：不接键盘的 6T 上通话中所有触摸都不算，只能等对方挂断。现在照安卓（[19](19-core-apps.md) §5.6）：6T 上「按电源键挂断电话」默认开（板子有电源键、整机没有自己的键盘、板子没有距离传感器）；贴着耳朵（听筒出声）时触摸不算，按一下音量键或按住屏幕一秒再松开，触摸打开 10 秒；开了免提触摸照常；通话界面「挂断」按钮下面写着怎么挂断。不改板子、机型和内核文件：`sku.json` 由 `tools/sku.py` 生成，6T 的 `keypad = "hid"`、没有 `proximity`，默认就对。

- 跑：用这棵树 2026-11-27 以后构建的镜像，不接蓝牙键盘。另一台手机打来，点「接听」。
  1. 通话界面「挂断」按钮下面有「按电源键挂断」和「贴着耳朵时触摸不起作用：……」两行。
  2. 手机贴到耳边说几句话，脸颊碰着屏幕：通话不断、不静音、不保持，屏幕到时间照常暗掉。
  3. 屏幕亮着时按一下音量键：音量不变，提示「触摸已打开 10 秒」，第二行换成「触摸已打开，最后一次触摸 10 秒后关闭」；点「挂断」，通话结束。
  4. 再打来接通，手指不动按住屏幕一秒左右再松开：同样提示「触摸已打开 10 秒」；什么也不碰等十几秒，又回到「贴着耳朵时……」。
  5. 选项里打开免提（「免提」），直接点「挂断」：通话结束，不用先按音量键。
  6. 再打来接通，按电源键点亮屏幕（屏幕暗着时），再按一下电源键：通话结束，屏幕还亮着。设置 › 辅助功能里关掉「按电源键挂断电话」，再打来：电源键照旧关屏幕，通话还在（挂断用另一台手机）；最后把它打开。
  看 `grep -e 'touch:' -e 'power key' -e 'hanging up' /run/phone/shell.log`：被挡住的触摸是 `touch: … nothing (in a call touch is off at the ear)`，唤醒是 `touch: on in the call (a volume key)` 或 `(a finger held on the screen)`，关上是 `touch: off again in the call`，电源键挂断是 `power key: short press: the call hung up`。
- 应该：贴着脸时通话一次也不被挂断、静音或保持，shell.log 里贴脸那段只有 `nothing (in a call touch is off at the ear)`；音量键、按住一秒都能唤醒；10 秒后关上。
- 记到：本节、[19](19-core-apps.md) §5.6 和 [10](10-keyboard-interaction-model.md) §2.1 的「**未上真机**」。贴脸时 shell.log 里出现 `touch: on in the call (a finger held on the screen)`（脸颊按住一到四秒，把触摸唤醒了）：把那几行交给机主，按住的时间窗要改（`shell/calltouch.go` 的 `callHoldMin`、`callHoldMax`）。
- 不要：在 chroot 里测（Nura 的电话栈不在 Kario 手里）；拨紧急号码。
- 板子文件：6T 不用改。以后读得到距离传感器时（hexagonrpcd，§10），板子文件加一个种类为 `proximity` 的 `[[part]]`，再接上 Shell 的 `setProximity`（**未实现**）；那时电源键结束通话的默认变成关。

## 15. A/B 布局：上手机之前要查的

> 给连着这台 6T 的电脑上的人或 agent。A/B 布局（[43](43-phone-partitions.md)：根文件系统在 `system_a`、`system_b`，经 dm-verity 只读；数据在 userdata；RAUC 更新，ABL 管槽）的脚本和逻辑在电脑上测过，镜像 2026-11-24 构建过一次（§4），整个**未上真机**。下面 15.1、15.2 都对了以前不要用 `--layout ab` 刷这台手机，默认的 userdata 布局照旧能用。先走完 §13 的第 1–9 步（备份），结果照 §14.1 记，不要做的照 §14.1。

### 15.1 不写手机就能查的

1. 构建：`make yocto-build WORK=$HOME/yocto/fajita-work KASFILE=yocto/kas/phone-fajita-ab.yml SKU=oneplus-fajita-t9`。产物里要有 `phone-image-oneplus-fajita.rootfs.abboot` 和 `phone-image-oneplus-fajita.rootfs.ext4.verity.sparse`（§4），记下两个的大小。`unpack_bootimg --boot_img …rootfs.abboot` 应该列出头版本 0、页 4096、§1 的偏移，命令行有 `root=/dev/dm-0` 和 `phone.layout=ab`；ramdisk（前面是一个不压缩的 cpio，后面是 gzip 的 initramfs）里有 `etc/phone/rootfs.verity` 和 `.sig`、`etc/phone/partitions.conf`（`slots=abl`、`root=system`、`data=userdata`、`state=`）、`usr/bin/phone-abslot`。
2. 分区的大小：手机进 fastboot，下面第一条。应该：`system_a`、`system_b`、`boot_a`、`boot_b`、`dtbo_a`、`dtbo_b`、`userdata` 都有 `partition-size`，十六进制（`0x…`）；`system_a`、`system_b` 比根槽映像展开后的大小（演练里 `# write system_a:` 那行的字节数）大，`boot_a`、`boot_b` 比 `….abboot` 大。Nura 上（§13.4）`ssh root@172.16.42.1 sh /var/lib/kario-chroot/kit/on-phone.sh partitions` 列出整个分区图、每个的字节数、板子文件给的角色，最后是板子文件里有、手机上没有的。记到：板子文件 `sku/boards/oneplus-fajita.toml` 的 `[partitions]`（核实过的分区写上 `size`，不在手机上的删掉，手机上有而表里该有的补上；都核实了就从 `unverified` 删掉 `partitions.part`），[43](43-phone-partitions.md) §3.2 的「**未核实**」。分区大小不要抄进别的地方：它们不是这台手机的身份，但分区图的设备号会变。
3. 槽的属性：`fastboot getvar current-slot`，`fastboot getvar all` 里两个槽的 `slot-successful`、`slot-retry-count`、`slot-unbootable`，记下来，刷完要比。
4. 回 OxygenOS 要的都在：OxygenOS 11.1.2.2 完整包的 `payload.bin` 解出 `boot.img`、`dtbo.img`、`system.img`，放在仓库外的一个目录里；`system.img` 不比 `system_a` 大。
5. 演练：下面第二条，不用手机。应该：只写 `boot_a`、`boot_b`（`….abboot`）、`system_a`、`system_b`（`….ext4.verity.sparse`），擦 `dtbo_a`、`dtbo_b`、`userdata`，最后 `fastboot --set-active=a`、`fastboot reboot`；`# kept, never written:` 和 `# the phone's own, never written` 两行列出不碰的和本机独有的分区。

```
fastboot getvar all 2>&1 | grep -e partition-size -e partition-type -e slot- -e current-slot
yocto/fajita-flash.sh -n --layout ab -d ~/kario-images flash
```

### 15.2 先把回 OxygenOS 走一遍

A/B 布局要写 `system_a`、`system_b`，回去时要从 OxygenOS 的包里写回来（§9）。先在现在的样子上走一遍，确认 OxygenOS 起得来：

1. `yocto/fajita-flash.sh --stock <15.1 第 4 步的目录> restore`：写回两个槽的 `boot`、`dtbo`，清空 userdata，重启。OxygenOS 起来，走完开机向导；拨 `*#06#` 看 IMEI 还在（不要抄下来）；Wi-Fi、电话能用。
2. 回到要测的样子：§13.4 重装 Nura，或 §6 刷 userdata 布局。
记到：§9.1 末尾「回 OxygenOS 和写回 EFS 在这台手机上都还**没做过**」那句（只改回 OxygenOS 的一半），§9 的「`restore`……**未上真机**」。

### 15.3 刷入和第一次开机

1. `yocto/fajita-flash.sh --layout ab -d <镜像目录> flash`：先打出每个要写的分区多大、镜像多大，再写；最后重启。
2. 第一次开机，经 USB 控制台（§7）看：`dmesg | grep phone-initramfs` 有 `userdata: no file system on …, making it anew` 和 `slot a (…, verified)`；`findmnt /` 是 `/dev/mapper/root_a`、只读，`findmnt /var/lib` 是 userdata、读写；`cat /var/lib/slots/boot` 有 `booted=a`；`grep -o 'androidboot.slot_suffix=[^ ]*' /proc/cmdline`（[43](43-phone-partitions.md) §3.3 说没有）；`journalctl -b -u phone-ota-good`：健康检查过了、`rauc status mark-good`；之后 `phone-abslot status` 里 a 是 `successful`；`journalctl -b -u phone-qcom-abslot` 说它没起（`phone.layout=ab`）。
3. `rauc status`：根槽是 `system_a`、`system_b`，各带一个启动槽，正在运行的是 a、好的。
4. 重启进 fastboot，和 15.1 第 3 步比：a 是 `slot-successful: yes`；刷完到标成功之间 `slot-retry-count` 有没有从 7 往下数（ABL 怎么数是照源码读的，[43](43-phone-partitions.md) §3.3）。
5. 再照 §8 查一遍屏、按键、基带、声音：和 userdata 布局应该一样。
记到：本文 §1 表「根文件系统」「A/B 槽」两行、§8 表「槽」一行；[43](43-phone-partitions.md) 的状态和 §3.3、§7。

### 15.4 系统更新和退回

只在 15.3 都对了以后做：

1. 装一个更新包（同一次构建的 `make yocto-bundle`，[25](25-system-update.md) §2）：设置 › 系统更新，或 `rauc install <包的网址或路径>`。装完 `rauc status`：b 是下次启动的槽；`phone-abslot status`：b 活动、7 次、未成功；`cat /var/lib/slots/boot`：`slot=b`、`booted=a`。
2. 重启：从 b 起来（`cat /var/lib/slots/boot` 有 `booted=b`），健康检查以后 b 是 `successful`；`rauc status` 说 b 好。
3. 退回：再装一次（这次写 a），重启以前把 a 的根槽开头的一个块写坏（下面那条命令，只在 A/B 布局、只对 `system_a`），再重启。a 验不过，initramfs 每次 10 秒后重启；ABL 数完次数以后应该把 a 标成不可启动、从 b 起来。数一数重启了几次（应该是 7 次），b 起来以后 `phone-abslot status`、`rauc status` 里 a 是坏的。之后再装一次就把 a 写好了。
4. 回 OxygenOS：15.2 那样 `restore --stock`（备份目录的 `kario.txt` 写着 `layout=ab`，脚本要 `system.img`）。

```
dd if=/dev/zero of=/dev/disk/by-partlabel/system_a bs=4096 count=1 conv=notrunc,fsync   # 只在退回的测试里，只对 system_a
```

记到：[25](25-system-update.md) §8；[43](43-phone-partitions.md) §3.5、§7；[41](41-design-gaps.md) §3。

不要做的（另加 §14.1 的）：15.1、15.2 没做完以前用 `--layout ab`；对 `system_a`、`system_b` 以外的分区 `dd`；`fastboot --set-active` 到一个不是 Kario 的槽（A/B 布局里两个槽都是 Kario 时可以）；除了 `phone-abslot`，手工改 GPT 的属性。`fastboot -w` 清掉数据和持久区（防盗锁的记录也在里面），手机照样起来，initramfs 重做数据分区。

## 16. 真机实测记录（2026-11-22 到 11-25）

§9.1 那台国行 6T，在另一台电脑上照 §13、§14 又跑了几轮：先是 Nura 上的 chroot（§13.4，2026-11-22、11-23），2026-11-24 第一次刷整机（§13.5），2026-11-25 第二次（§16.10）。下面按主题记下手机上实际看到的事：哪天、看到了什么、是**实测**还是**推断**、Kario 现在怎么做。序列号这类只属于这台手机的号码不写。和本文别处说的不一样时，以这里为准，别处标着出处。

### 16.1 引导程序（ABL）和开机

- **ABL 总是追加自己的 `root=`**（2026-11-24，实测）：ABL 交给内核的命令行末尾那一串 `androidboot.*` 里总有一个 `root=PARTUUID=<GUID>`，指向 Android 的 system 分区，启动镜像自己的命令行里已经有 `root=` 时也照加；内核以最后一个 `root=` 为准。镜像的命令行有 `root=` 时，ABL 不再加 `skip_initramfs rootwait ro init=/init` 和 `androidboot.slot_suffix=_a`；没有时（Nura 的命令行）这些都加。高通公开的 ABL 源码（§12，SHIFT6mq）有 `root=` 时连 `root=` 也不加，一加的 ABL 不是这样。
- **后果**（2026-11-24，实测，从 ramoops 读出）：`0befb13` 的整机镜像（空 ramdisk，`root=PARTLABEL=userdata`，靠内核自己挂根，见本文开头「为什么不用 initramfs」）每次开机，内核都挂上了 Android 的 system 分区，跑起 `/system/bin/init`；它在第一阶段找不到 selinuxfs，`InitFatalReboot: signal 6`，重启，约 17 秒后回到 fastboot。内核、设备树、UFS 驱动和刷进 userdata 的根文件系统都没问题：Kario 的内核配上 Nura 的 initramfs 能起来，看得到 `sda1`…`sda17`，userdata（这台是 `sda17`）是完好的 ext4。
- **Kario 的改法**：userdata 布局也走 initramfs（phone-initramfs，2026-11-24 起）：它从启动镜像的 ramdisk 知道是 userdata 布局，在 sysfs 里按板子的分区名找 userdata，读写挂上，命令行里的 `root=` 一概不管，整条命令行和它的决定写进内核日志（pstore 里看得到）；启动镜像的命令行仍留着 `root=PARTLABEL=userdata`，只为让 ABL 不加 `skip_initramfs`、`ro init=/init` 和槽后缀（本文开头、[43](43-phone-partitions.md)）。QEMU 上照这次的命令行模拟 ABL 证过（`make yocto-userdata-test`），2026-11-24 在 6T 上第一次整机开机就是这样起来的（§16.9）。
- **槽的重试次数会耗光**（2026-11-24，实测）：开机连续失败时 ABL 每次把当前槽的重试数减一，减到 0 就把它标成不可启动（`fastboot getvar slot-unbootable:a` 是 `yes`、`slot-retry-count:a` 是 `0`）；这以后 `fastboot boot` 任何镜像都报 `Load Error`，以前能起来的也一样。`fastboot set_active a` 以后恢复（重试数回到 7）。所以碰到 `Load Error` 先查这两个 `getvar`。Kario 开机成功后要把槽标成成功，不然每次开机都扣一次：`phone-qcom-abslot.service` 的 `phone-abslot mark-good`，命令行里没有 `androidboot.slot_suffix` 时按标为活动的那个槽算。`fajita-flash.sh` 的 `test`、`flash`、`restore` 现在每次都先 `fastboot --set-active=<当前槽>`（两种布局）；没有槽后缀时 `phone-abslot` 取 GPT 里活动的槽，用这次 ABL 给的命令行测过（kario-bsp 的 `tests/abslot-test.py`），`phone-qcom-abslot.service` 等 udev 建好 `by-partlabel/boot_a` 再标（2026-11-24 整机上实测标上了，§16.9）。
- **读开机失败那次的内核日志**（2026-11-24，实测可用）：6T 的设备树有 `ramoops@ac300000`，Kario 的内核开着 `CONFIG_PSTORE_RAM` 和控制台记录，`CONFIG_PANIC_TIMEOUT=0`（崩溃后不自己重启）。失败以后**热重启**（不断电），`fastboot boot` 一个能起来的镜像（下一条），`mount -t pstore pstore /sys/fs/pstore`，读 `console-ramoops-0`。那次有 55562 字节，ECC 报 5 个可纠正字节、434 个坏块，有乱码，关键行能读。
- **诊断用的启动镜像**（2026-11-24）：Kario 的内核（启动镜像里的，接着设备树）配 Nura 的 initramfs，按 Android 启动镜像头版本 0 重新打包（偏移和页大小照 `oneplus-fajita.conf` 的 `PHONE_BOOTIMG_*`），`fastboot boot` 只在内存里跑：进 postmarketOS initramfs 的调试 shell（§9.1 那样连），跑的是 Kario 的内核。把它的命令行换成别的（比如 Kario 自己的）再 `cat /proc/cmdline`，就看得到 ABL 追加了什么。这个 shell 不是 1 号进程，`switch_root` 用不了。
- **busybox 的 `mount` 不认 `PARTLABEL=`**：那个调试 shell 里 `mount PARTLABEL=userdata /mnt` 报 `No such file or directory`，`mount /dev/sda17 /mnt` 能挂。这说明不了内核：内核挂根的代码认 `PARTLABEL=`。
- `androidboot.bootcount=1919247457`：ABL 每次都给这个很大的数，原因不明，看不出影响。

### 16.2 刷机和 fastboot

- **大镜像要分块传**（2026-11-24，实测）：`fastboot flash userdata`（约 470 MB 的稀疏镜像）传到 471 MB 时 USB 断开（`Write to device failed in SendBuffer()`）；`fastboot -S 64M` 分块后成功（8 块，22.5 秒）。fastboot 的 USB 有时几秒后自己断开（`usb 1-1: USB disconnect`），重插再来。`fajita-flash.sh` 现在每个镜像都按 `fastboot -S 64M` 分块（`--chunk` 可改），对着丢掉 100M 以上下载的模拟手机测过。
- **备份里缺 `boot_a`、`dtbo`**（2026-11-24）：这台手机装 Nura 时 `boot_a` 被覆盖、`dtbo` 被擦，备份是之后做的（§9.1），`fajita-flash.sh` 核对备份时因此拒绝刷机；EFS 的五个分区和 6 个 GPT 头都在。已改：本机独有的五个分区必须在，缺 `boot`、`dtbo` 只提醒；`restore` 写回以前核对每个文件（Android 启动镜像、DTBO 标记 `d7b7ab1e`），缺的、擦过的要 `--stock`，不对就一个字节也不写（`yocto/fajita-flash-test.py`）。
- **fastboot 里没有软件关机**（2026-11-23，实测）：`fastboot oem poweroff`、`oem shutdown`、`oem help` 在 `18d1:d001` 和 `18d1:d00d` 下都是 `unknown command`，`getvar all` 里只有充电的设置（`off-mode-charge:1`、`charger-screen-enabled:1`）。关机只能长按电源键 15–20 秒。
- **两种 `Load Error`**：擦了 dtbo 以后 TWRP 3.6.2、LineageOS 22.2 的 `boot.img` 被拒（§9.1）；2026-11-24 的那次是槽不可启动（§16.1）。碰到时先查 `getvar slot-unbootable:a`（§8.2）；脚本先 `set_active`，第二种一般遇不到。
- 重启以后 `lsusb` 有时是 `18d1:d001`、`fastboot devices` 却是空的：它还要自己再复位一次（§16.3），等 60–90 秒再动，不要以为刷坏了。

### 16.3 重启、关机和看门狗

- **重启后多启动一次**（2026-11-23，实测，原因不明）：`reboot` 或 `fastboot reboot` 以后手机先起一次、约 2 秒后复位，第三次才正常（`journalctl --list-boots` 里有一次只活了 2 秒的启动）。Nura 关机时 systemd 记着 `Using hardware watchdog /dev/watchdog0: 'qcom_wdt'` 和 `Failed to set watchdog hardware timeout to 10min: Invalid argument`：sdm845 的 `qcom_wdt` 最多约 32 秒（计数上限 0xFFFFF，时钟 32764 Hz），systemd 默认的 10 分钟它不收；systemd 收到 EINVAL 后是解除看门狗。`fastboot reboot` 时 Linux 不在跑，也多启动一次，所以不全是看门狗的事。Kario 板子的 `[watchdog]` 让重启时的看门狗设 30 秒（**未上真机**，对比的做法在 §14.10）。
- **关机 2 秒后又开机**（2026-11-23，实测；原因是推断）：Nura 上 `systemctl poweroff` 走到 `Reached target System Power Off`，2 秒后手机又起来了。测试的一方当时以为内核没有关机的办法（`pm_power_off` 是空的）；看内核：sdm845 有 PSCI 1.0、`ARM_PSCI_FW=y`，关机走 PSCI 的 `SYSTEM_OFF`，没有办法时内核会停住而不是重启。当时插着 USB 线，ABL 又是 `off-mode-charge:1`，更可能是 PMIC 见到充电器又开了机（§14.10 分拔线、插线试）。
- 那几次 `pstore` 是空的，`ramoops` 只有 `error in header`（PMIC 里的旧数据）：没有崩溃。

### 16.4 挂起和唤醒

- **RTC 从 1970 数起、只能读**（2026-11-22，实测）：`hwclock -w` 报 `ioctl(RTC_SET_TIME) failed: No such device`；开机时 `rtc-pm8xxx … setting system clock to 1970-01-01…`，联网以后系统时间才对，两边差约 56.9 年。`wakealarm` 写绝对秒数是按 RTC 自己的钟算，写 `+N` 才是从现在起 N 秒。Kario 原来写系统时间的绝对值，闹钟落到 57 年后，挂起后就醒不来；现在写 `+N`（`hal/pm`，[11](11-platform-architecture.md)）。2026-11-22 `echo +30 > wakealarm` 叫醒了手机，2026-11-23 自测的「挂起」用例通过。时间怎么保住：板子的 `[rtc]`（[28](28-battery.md) §6）。
- **挂起以后 USB 整个消失**（实测）：s2idle 时 USB 控制器不是唤醒源（唤醒源只有电源键、充电器、霍尔传感器、remoteproc、IPA、RTC、alarmtimer），电脑上 `lsusb` 里没有手机，USB 网卡没了，醒来以后才回来。所以经 USB 跑测试时手机不能睡：整机的 bring-up 镜像在电脑配置着 USB 控制台时持着 `phone-bringup-awake` 唤醒锁（§13.5；2026-11-26 以前的镜像开机就持着、一直不放，一夜没电，§16.12）。
- **Nura 上拦不住挂起**（2026-11-22，实测）：最后一个 SSH 会话一断，3 秒后就 `PM: suspend entry (s2idle)`。`systemctl mask sleep.target …` 挡不住（`systemd-suspend.service` 是 static 的）；mask 了服务本身、`autosleep` 关着、logind 的 `IdleAction=ignore`，也还是睡了；Nura 的 `sleep-inhibitor` 开着 3 个 SSH 会话也不持锁；`systemd-inhibit --mode=block` 能让 `systemctl suspend` 被拒，手机还是睡了。最后查出是自测自己的「挂起」用例让手机真的挂起，又因为上面的 RTC 问题醒不来；RTC 修好以后这些都不用再拦。
- **chroot 里电源键叫不醒**（2026-11-23，实测；原因没查清）：`shell-start` 后约 40 秒熄屏挂起，按电源键没有反应，只能长按重启；同一天 RTC 叫得醒。那时 logind 被套件的 `systemd-inhibit --what=handle-power-key` 挡着，Shell 也睡着。`/sys/class/wakeup/wakeupN/event_count` 是 0、那里没有 `power/wakeup`，都说明不了什么：pm8941-pwrkey 从不向自己的唤醒源报事件，属性在 `/sys/bus/platform/devices/c440000.spmi:pmic@0:pon@800:pwrkey/power/`。整机上照 §14.10 抓。
- **Kario 镜像里的 logind**（2026-11-23 查出，QEMU 上实测）：没配置时按电源键就关机、盖上磁吸保护套就挂起，绕过 Shell；镜像现在带 `logind.conf.d/50-phone.conf`，这些都交给 Shell（[28](28-battery.md) §7）。

### 16.5 显示

- 开机时 `msm_dsi_phy: DSI PLL(0) lock failed, status=0x00000000`（2026-11-22、11-23 都有）。
- **挂起以后显示没回来**（2026-11-22，实测）：黑屏、按电源键不亮，系统还活着；`bl_power` 是 4、`fb0/blank` 是 4，dmesg 有 `PM: suspend exit`、上面那行 DSI 的错和 `dpu_encoder_phys_cmd_ctl_start_irq` 超时。手动 `echo 0 > /sys/class/graphics/fb0/blank`、`echo 0` 写进背光的 `bl_power`、写回 `brightness`、`chvt 1` 都能恢复。背光调到 0 再调回、控制台熄屏再亮屏，连做 5 次都正常：面板本身断电上电没问题。Kario 的 Shell 在这条路上怎么做、修没修好：§16.12。
- **屏幕状态对不上**（2026-11-23，`off true, panel on true`）：一部分是 Shell 的 bug（重启的 Shell 碰到灭着的背光，以为屏幕亮着，之后也不让面板关；已修），其余可能是上面的显示链路。
- `page flip: Invalid argument`（2026-11-21 见过）：2026-11-23 开着 drm 调试抓了 15 秒没有复现（[42](42-gpu-rendering.md) §4.3）。
- **GPU 固件**：Nura v26.06 加载 `a630_sqe.fw` 报 -40（ELOOP，符号链接成环），`msm_dpu: failed to load a630_sqe.fw`；Kario 的镜像自带 a630 的固件（§8.1）。

### 16.6 声音

- HiFi 的 UCM 里扬声器是 MultiMedia3，也就是 `hw:0,2`；MultiMedia1（`hw:0,0`）接听筒，HiFi 下没有路由，往它放音内核报 `no backend DAIs enabled for MultiMedia1`；话筒在 MultiMedia2、MultiMedia4。`alsaucm -c hw:0 set _verb HiFi set _enadev Speaker` 以后 `speaker-test -D plughw:0,2` 响了（2026-11-23，人耳确认）。板子的 `[audio]` 照这个写（[20](20-audio.md) §10）。
- **扬声器很响**（2026-11-23）：自测和像素自测的媒体用例真从扬声器放，默认音量太大，像素自测那轮因此中止。测试的一方把 `RX0 Digital`…`RX4 Digital` 调到 15%（`RX0 Digital` 19/124，-65 dB）；扬声器功放 TFA9894 在 QUAT_MI2S 上，这几个增益管不管得到它**没确认**。套件在 `selftest`、`pixel` 之前调小、跑完调回（板子 `[audio]` 的 `quiet_controls`，§14.5）。

### 16.7 Nura 底座（chroot 的环境）

- Nura v26.06 console，内核 `7.1.0-rc1-sdm845`，systemd；USB 网络上手机是 `172.16.42.1`，用户 `user` 加免密的 `sudo`。
- LSM 只有 `capability,bpf`，没编 Landlock；Wi-Fi 归 NetworkManager，没有 systemd-networkd；基带那一套（ModemManager、rmtfs、pd-mapper、tqftpserv、q6voiced）由 Nura 跑。chroot 里内核特性、VPN、恢复出厂、要移动网络出口的用例因此跳过，跳过时写明原因（[40](40-testing.md)）。
- 新的网络命名空间里 `lo` 是 DOWN 的，这时 `bind` 照样成功、连自己失败。沙箱的自检原来只看 `bind`，在没有 Landlock 网络规则的内核上都误报「允许」（已修，[17](17-app-sandbox.md)）。
- Nura 的 postmarketOS initramfs 自己按分区找根、不管 `root=`，所以 ABL 追加的 `root=`（§16.1）碰不到它。
- 相机和视频：`/dev/video7: no streaming video capture`，`qcom-venus aa00000.video-codec: non legacy binding`；6T 的相机没做（§11）。
- 一遍自测加兼容测试（`--keep-going`）在 chroot 里跑了约 18 分钟（2026-11-23，1107 秒）。

### 16.8 其他

- 电量计：`bq27xxx-battery 10-0055: missing battery:energy-full-design-microwatt-hours` 和 `invalid … -22`，不影响开机，`capacity` 可能不准（设备树的 `monitored-battery` 缺这一项，没改）。

### 16.9 第一次整机开机（2026-11-24，`d4856a3`）

`d4856a3` 的整机镜像（回复 005）照 §13.5 刷入（备份核对缺 `boot_a`、`dtbo` 只提醒；`-S 64M` 分 8 块写 userdata；`--set-active=a`）以后**第一次就起来了**：

- **挂根**（实测）：`phone-initramfs: … not used from the command line: root=PARTLABEL=userdata root=PARTUUID=92049492-…`，`the root file system: /dev/sda17, the partition userdata (found after 0 s)`，`starting /sbin/init`。ABL 照样追加它的 `root=`（§16.1），initramfs 不管它。
- **槽**（实测）：`phone-qcom-abslot` 把 `boot_a` 标成成功，几次重启、一次关机以后重试次数没有往下扣。
- **自测**（实测）：完整一遍 193/194（chroot 那轮 183/194），兼容测试的能力 132/137（没调用的 5 个是跟着要模拟接收机的定位用例一起跳过的）；内核特性和沙箱 3/4、四项媒体 4/4。失败的：「VPN（设备）」（`netlink receive: no such file or directory`）、「沙箱里的 DNS」（`chown …/state/apps/com.phone.chat: no such file or directory`：只在「内核特性 + 沙箱」那一小段里失败，那时鸿雁从没启动过、数据目录还没有；完整一遍里它是通过的。已修：这一步先照应用第一次启动那样建好目录，[40](40-testing.md) §6 加了一条规矩：用例单独跑也要能过）。
- **电源键叫醒**（实测，§14.10）：`echo mem > /sys/power/state`，USB 断开后短按电源键，约 10 秒醒来（量的是 USB 和 SSH 回来，不是亮屏，§16.12）；`pm_wakeup_irq` 从 `No data available` 变成 90，`suspend_stats/success` 0 → 1，`pm8941_pwrkey` 的中断 16 → 17，dmesg 有 `PM: Triggering wakeup from IRQ 90`。chroot 里叫不醒（§16.4）是 logind 和套件的事，不是内核。
- **关机**（实测）：拔着线 `systemctl poweroff` 关住了两分钟以上；插回线，手机被充电器开机（命令行里 `androidboot.mode=charger`），和 `off-mode-charge:1` 对得上（§16.3 的推断成立）。Kario 现在认这个参数，进关机充电而不是整个系统（[28](28-battery.md) §10，2026-11-26，**未上真机**）。pstore 是空的，看不到内核最后一行。
- **重启和看门狗**（实测）：`Watchdog running with a hardware timeout of 30s`（systemd 和 systemd-shutdown 都是），没有 `Failed to set`，`journalctl --list-boots` 里**没有**多出来的短启动；`watchdog0: watchdog did not stop!` 是有意留着兜底的。
- **电源键归 Shell**（实测）：logind 的 `HandlePowerKey`、`HandleLidSwitch` 都是 `ignore`；短按熄屏、再按亮屏，按住约 600 ms 出电源菜单，手机不关机。保护套没有磁铁，**没测**。
- **连按电源键以后黑屏**（实测，用户在手机上看到的）：单按熄屏、亮屏都对；连按几下以后屏幕黑了、按键没反应；过一会儿再按，花屏一闪又亮了。没取到日志：这个镜像的 Shell 只把熄屏、面板的记录写在 `/run/phone/shell.log`。**原因**（推断，**未在真机上核实**）：Kario 自己的四处：每次按键都直接交给渲染器，面板的开关一个接一个排队（面板换一次要 2 秒时，按 6 下要换 10 秒左右）；「打开面板」失败了不重试，Shell 以为屏幕亮着；背光在面板还没上电时就点亮（多半就是那一下花屏）；面板开关前后迟到的翻页完成事件被算给下一帧，帧画进了还在屏上的缓冲区。**已修**（2026-11-25，**未上真机**）：面板一次只换一个、最后停在最后一下按的状态，背光等面板，失败重试一次，每次按键和面板的变化都进 journal；下一轮照 §14.10「连按电源键」取证。
- **内核 lockdown 挡住了所有模块**（实测，最要紧）：`Lockdown: modprobe: unsigned module loading is restricted`，`/sys/kernel/security/lockdown` 是 `[integrity]`、降不下来；`rtc-pm8xxx`、`bq27xxx_battery` 都是模块，于是 `/sys/class/rtc/`、`/sys/class/power_supply/` 都空，`phone-clock` 报 `no rtc0 after 15s`，跨启动时间倒退了 3 秒，时钟那一步（§14.10）做不了。VPN 用例（防火墙要的 `nft_ct` 是模块）、基带那一串（qrtr 是模块：`tqftpserv: socket(AF_QIPCRTR): Address family not supported`，`rmtfs: Failed to get rprocfd`）、没有声卡、Shell 晚一分钟才上屏（`uim-select` 每 2 秒 modprobe 一次、等满 60 秒）都是这个原因。**原因**（构建机上查出来的）：模块都签了名，但签名的钥匙不是刷进去的内核认的那一把：几个构建目录共用 sstate，内核每次构建自己随机生成签名钥匙，又不在任务的哈希里，于是 `fajita-work` 的启动镜像配上了 `bt-work` 编出来的模块（内核报的「unsigned」对没签名、钥匙不认、算法不认都是同一句）。lockdown 本身来自 Nura 的 defconfig（`CONFIG_LOCK_DOWN_KERNEL_FORCE_INTEGRITY=y`）。**已修**：lockdown 留着；每个构建目录一把固定的模块签名钥匙（`$WORK/ota-keys/kernel-modules.pem`，不进 git），它的校验和进了任务哈希；RTC、电量计、充电器、qrtr 和 PD mapper 编进内核；镜像构建时核对每个模块的签名，QEMU 开机时实际加载一个模块、改过一个字节的必须被拒（[17](17-app-sandbox.md)；**未上真机**）。
- **套件读 `/sys/power/wakeup_count` 卡死**（实测）：有唤醒锁持着的时候（整机的 bring-up 镜像一直持着 `phone-bringup-awake` 那把），内核让这个读一直等，`setup` 的 facts 停住、`facts.txt` 是空的；Nura 上没有人持唤醒锁，chroot 那几轮碰不到。已修：套件读每个属性最多等 2 秒，写 `<no answer in 2 s>`，另列出持着的唤醒源；Shell 本来就在后台读它（**未上真机**）。
- **自测后面板起不来**（实测）：套件停掉 `phone-display` 以后，留下的「renderer」（1004）是面板渲染器替自己打开输入设备的子进程：它降权（setuid）时把「父进程没了就退出」清掉了，又是在扫描输出的缓冲区映射好以后才 fork 的，于是一直攥着 DRM master；新渲染器 `/dev/dri/card0: Permission denied`，重启五次后放弃、`bl_power` 是 4。机理在 QEMU 上复现了，手机上是照同样的代码和报错推断的。已修：子进程在打开显示设备以前 fork，降权以后重新设好父进程退出信号、渲染器那头一关就退；`phone-display` 改成 `KillMode=mixed`（`yocto-check` 拒绝跑 phone-shell 的单元用 `process`）；套件停单元以后等它的组空掉，10 秒还有人就点名杀掉（**未上真机**）。
- 其他：`a630_sqe.fw` 开机时报 -2，85 秒后才从新位置加载上；`DSI PLL(0) lock failed`、`crtc85 event 1 overflow`（显示链路，老问题）；缺 `regulatory.db`。

### 16.10 第二次整机自测的失败（2026-11-25，`86fae7a`，报告 006）

插着中国移动的 SIM 卡、移动数据连着（`qmapmux0.0` 有地址），完整自测 186/195，像素自测四个形态各 181/188。下面是那 9 个失败和跑完留下的 `kario-vpn` 网卡，都**已修，未上真机**；下次照 §14.6、§14.7、§14.10 看。

- **蓝牙控制器一直没有地址**（实测）：开机日志里 `phone-btaddr: hci0: reading its configuration failed (17)` 和内核的 `Bluetooth: hci0: QCA setup on UART is completed` 在同一秒，`phone-qcom-btaddr.service` 失败后没有谁再试，控制器一直没配置好，BlueZ 没有适配器；「飞行模式」（`bt is on: false`）、「蓝牙搜索跟着页面」（`配对新设备` 不出现）因此失败。**原因**（日志和内核的管理接口对得上）：hci0 一出现服务就起了，那时 WCN3990 的固件还在经 UART 下载；控制器设好以前内核对它的管理命令一律答 17（`MGMT_STATUS_INVALID_INDEX`），程序一收到就退出。**改法**：`phone-btaddr` 等控制器设好（订阅内核的控制器通告，没有通告就每秒再读一次，最多 `--wait 60` 秒）再设地址，地址照旧从序列号算；kario-bsp 的 `tests/btaddr-test.py` 用一对套接字扮内核测，旧代码报的正是 `failed (17)`。
- **要虚拟对端的蓝牙用例**（实测）：「换机（模拟器，蓝牙）」（`0 Bluetooth adapters, need two`）、「蓝牙传文件和电话本」「通讯录：蓝牙收名片」（`通过蓝牙给你发来` 不出现）。**原因**：它们的对端是 btvirt 的虚拟控制器，只有 QEMU 的内核有 hci_vhci（`phone-radio-sim.cfg`）；虚拟控制器也只连得到别的虚拟控制器，够不着手机自己的射频，所以整机上本来就跑不了。**改法**：这几个用例和配对耳机的「Wi-Fi 和蓝牙」加了 `require-bt-peer`，手机的适配器是它自己的控制器、或者内核没有 hci_vhci 时跳过并写明原因；Shell 选手机的适配器时先选手机自己的控制器，虚拟的排在后面，自测的对端只能是虚拟控制器。
- **VPN 的两个设备用例**（实测）：「VPN 始终开启和按应用（设备）」（`write udp4 10.77.7.2:…->192.0.2.1:9: network is unreachable`）、「VPN 屏蔽未使用 VPN 的连接（设备）」（`dial udp4 192.0.2.1:9: network is unreachable`）；同一轮里要移动网络出口的用例都跳过了，写着 `no route for the packets to 192.0.2.1:9`。**原因**（推断：日志和代码对得上，路由表没有记下）：自测的 Shell 用模拟基带，它管基带网卡的代码一起来就接管了 `/run/systemd/network` 里所有 `10-kario-cell-*.network`，也包括手机自己的 Shell 给 `qmapmux0.0` 写的那个（开机日志里 networkd 正是照它配的网卡），又因为模拟基带报的是 `simwwan0`，把 `qmapmux0.0` 当成旧网卡拿下（`Unmanaged`、清地址、关网卡），默认路由跟着没了。不走隧道的包带着隧道的标记、只查主表，主表没有默认路由就是 network unreachable；分隧道时 `connect` 就过不去，轮不到防火墙报 EPERM。`vpn` 包的 `TestMarkedPacketsNeedTheMainTablesRoute` 在网络命名空间里复现了这个路由（`unshare -rn`）。**改法**：模拟基带的 Shell 只接管模拟网卡的文件；这两个用例加了 `require-direct-route`，隧道外没有路由（没有 Wi-Fi、也没有移动数据）时跳过并写明原因。
- **跑完还留着 `kario-vpn` 网卡**（实测）：VPN 用例失败后手机从用例开始前的状态重来，新的 Shell 没有生效的配置，就不管还起着的隧道，全隧道的策略路由把后面用例的包都送进那条死隧道；自测以后手机自己的 Shell 也一样。**改法**：Shell 起来时没有生效的配置、却有隧道（网卡、networkd 的文件或那两条策略路由）在，就拆掉。
- **「设置：撤销权限后能力调用被拒绝」**（实测）：`wait 无法通话` 没等到。**原因**（从日志确认，和基带、时序无关）：前一个用例失败，手机重来，鸿雁新起来停在会话列表，列表上 F2 是「发起聊天」；只有在会话里 F2 才是视频通话，摄像头被拒才有「无法通话」。QEMU 和开发机上前面的用例都通过，鸿雁一直停在张三的会话里。**改法**：用例自己进张三的会话（停在会话里时先返回列表）再按 F2，单独跑也能过。
- **「挂起：息屏后没事可做就整机挂起」**（实测）：`slept 0 times, not 1 (awake: )`，`setup` 也提示过它跑不过。**原因**：bring-up 镜像的 `phone-bringup-awake` 持着唤醒锁 `phone-bringup`，内核不挂起，读 `wakeup_count` 也一直等；Shell 还是去睡，卡在读计数上，不说为什么醒着。**改法**：别人持着唤醒锁（`/sys/power/wake_lock`）时 Shell 不去睡，醒着的原因写成 `wake lock phone-bringup`；这个用例加了 `require-sleep`，这时跳过，写明 `systemctl stop phone-bringup-awake` 能让手机睡。

### 16.11 第二次整机（2026-11-25，`86fae7a`，报告 006）：充电、Shell 停住、按住电源键

同一份报告里的这三件事；手机插着 SIM 卡，持着 bring-up 唤醒锁（一直没挂起过，`suspend_stats` 的 `success=0`）。

- **充满以后拔插充电器屏幕不亮**（实测）：充电器驱动看到了（手机上每 0.5 秒采一次：`pmi8998-charger` 的 `online` 1→0→1，它的 `status` 跟着是 `Full`、`Discharging`、`Not charging`），电量计 bq27411 一直报 `Full`；`bl_power` 一直是 4，没有提示，日志里没有 `screen on (charger)`。**原因**（读代码查出，单元测试复现）：`hal/power` 把电池的 `status` 是 `Charging` 或 `Full` 都算插着电，bq27411 在 100% 拔掉电源以后照样报 `Full`，Shell 眼里一直插着电，插回去也不算「插上」。息屏时的例行检查每 5 秒查一次，手机又没睡，拔插是看得见的：挂起不是原因。**已修**（**未上真机**）：看充电器的 `online`，电池的 `status` 只在没有充电器报 `online` 时看、只认 `Charging`；内核的 power_supply uevent 让 Shell 马上查（以前息屏时最多晚 5 秒）；提示等认出充电器再出（[28](28-battery.md) §1）。挂起时插充电器能不能叫醒：这个内核（sdm845-7.1-rc1）的充电器驱动把插线中断 `usb-plugin` 设成唤醒中断（读的内核源码，推断）；那次的 facts 只列按键的唤醒源，没看到，facts 现在列出每个唤醒源，下一轮照 §14.10 试。
- **Shell 停住一次**（实测，08:37）：屏幕亮着，电源键不能熄屏，系统活着（SSH 通，logind 照记 `Power key pressed short.`）；`/run/phone/shell.log` 最后一行是 `08:37:03 screen on (key)`，之后什么都没写，30 秒的超时也没熄屏；phone-shell（728）和两个渲染器都在，phone-shell 和面板渲染器（764）的主线程在 `futex_do_wait`，另一个渲染器（770，输入助手）在 `poll`，`bl_power` 是 0，dmesg 没有错；`systemctl restart phone-display` 恢复了。**原因**（推断，单元测试复现）：
  - phone-shell 的 fd 10 开着 `pmi8998-charger/usb_type`，08:40 抓的时候还开着。Shell 读它只是打开、读、关上（`hal/power` 的 `os.ReadFile`），开着就是这个读没返回。
  - 这个内核的充电器驱动在认出充电器以前对 `usb_type` 答 `EAGAIN`（`smb_apsd_get_charger_type` 的 `Apsd not ready`，插上以后的几百毫秒到一两秒）；内核的 sysfs 不为 `EAGAIN` 记日志，所以 dmesg 是干净的。
  - Go 打开文件时把 epoll 收的文件设成非阻塞、挂到自己的轮询器上，sysfs 的属性 epoll 也收（构建机上用 strace 看到 `EPOLL_CTL_ADD` 成功）；读到 `EAGAIN`，Go 就等这个文件「可读」，sysfs 的属性从不通知，goroutine 一直停着。Go 程序这时的样子就是主线程在 `futex_do_wait`、内核里什么错都没有。
  - Shell 只有一个循环，画帧和例行检查都在里面读电源（状态栏的电池、`checkScreen`、低电量时减弱动效），所以按键、超时、日志一起停了；渲染器那边没事，它的主线程在等消息（`futex_do_wait` 是它空闲时的样子）。那几分钟用户在做插拔线的关机试验（报告 §4.8），亮屏时正碰上认充电器那一会儿是说得通的（插拔的时刻没有记）。
  - 电源键修正以后的 `b67dbc4` 这条路没变，同样会停：同一个单元测试在 `86fae7a` 和 `b67dbc4` 上都卡住（「the loop is stuck reading usb_type」），在这棵树上通过。

  **已修**（**未上真机**）：`hal/` 读写设备属性不经 Go 的文件对象（`hal.ReadAttr`、`hal.WriteAttr`，直接系统调用），`EAGAIN` 马上返回，`usb_type` 读不出时算「正在认」；设备暂时不让挂起、`/sys/power/state` 答 `EAGAIN` 时，挂起的 goroutine 也不再一直等。加了循环的看门狗（5 秒没转就记一行，写明停在哪）和电源键一到就记的一行（[28](28-battery.md) §7）。Shell 往渲染器写不会这样停：渲染器有一个线程一直读它的输入，排进不限长的队列，主线程卡住时也照读。
- **按住电源键不放会重启**（实测，用户看到的，按了多久没记）：长按出电源菜单（菜单里的项目当时用按键选不了，另行修），再按着不放手机重启（`journalctl --list-boots`：上一次最后一条 08:35:45，这一次最早一条 08:35:46）。**原因**（推断）：PMIC 的 KPDPWR S2 复位，时间是引导程序设的；Kario 这边没有这样重启的路（[28](28-battery.md) §7）。两次启动「只差 1 秒」量不出按了多久：上一次的最后一条只是最后一条日志（logind 按下时就记 `Power key pressed short.`，不记按了多久），不是复位的时刻；从复位到 journald 起来要过 ABL、内核、initramfs，开机最早的日志时间在 phone-clock 设好时钟以前不对（这次报告附的开机日志开头是 `Mar 13`）。不改：手机卡死时靠它重启，Android 也是。下一轮照 §14.10 量时间、读 PMIC 的设置。

### 16.12 一夜没电（报告 007，2026-11-26）：bring-up 的唤醒锁和整条睡眠路径

`48026c7` 的整机镜像，用户息屏放了一夜，早上电池没电了。

- **原因**（读镜像的单元和 Shell 的代码查出，和报告的日志对得上）：bring-up 的 `phone-bringup-awake` 是个 oneshot，开机就 `echo phone-bringup > /sys/power/wake_lock`（开机日志里的 `Bring-up: keep the phone out of suspend …`），不带超时、谁也不放，内核一直不挂起；Shell 见别人持着唤醒锁就不去睡（§16.10 的改法），醒着的原因只记在内存里。报告的 Shell 日志里只有开机时的 `sleep: mem after 30s with the screen off`，之后一行 `sleep:` 也没有。屏幕关着，CPU、Wi-Fi、基带照常跑一整夜。
- **改法**（2026-11-26，**未上真机**）：
  - `phone-bringup-awake` 改成一个一直跑的小脚本（`/usr/libexec/phone-bringup/awake`）：只在电脑配置了 bring-up 的 USB 设备时持锁：`kario-bringup/UDC` 绑的控制器，它的 `/sys/class/udc/<udc>/state` 是 `configured`；电脑把链路挂起时是 `suspended`，线插着、还供着电就也持着。拔掉线、只插充电器（`powered`、`default`，没有电脑来配置）、或者 USB 那个 power_supply 的 `online` 是 0（控制器没有 VBUS 检测时拔线以后可能还停在 `configured`）都放开。电池供电时从不持锁。
  - 锁带 6 秒超时、每 2 秒续一次：脚本死了（哪怕是 `kill -9`）锁自己过期；`systemctl stop` 时放开（`ExecStopPost`）。
  - 每 2 秒读一次这两个属性，不用 udev：UDC 的 `state` 变化只有 `sysfs_notify`，没有 uevent（内核的 `usb_gadget_set_state`），udev 看不到它；读一次只是两个 sysfs 属性，不碰 USB 本身，2 秒一次的开销小到量不出（**推断**）；挂起时它和别的进程一起冻住，不会叫醒手机，醒来以后 2 秒内跟上。电脑配置好到持锁最多晚 2 秒：Shell 要息屏 30 秒以上才睡，这 2 秒里正好要睡的话手机睡一次，像没有锁时一样，醒来电脑重新配置时再持（**推断**）。
  - 变一次记一行：`phone-bringup-awake: holding the wake lock phone-bringup: a computer has configured kario-bringup on a600000.usb`，或者 `… not holding the wake lock phone-bringup, the phone may sleep: …`；`awake check` 打出现在该不该持和原因，`$W facts` 的 `== sleep` 里也有这一行。
  - 套件照旧走 USB：连着就持锁，`wakeup_count` 读不出时照 §13.5 等 2 秒写 `<no answer in 2 s>`；「挂起」用例照旧跳过，`systemctl stop phone-bringup-awake` 以后能跑。
  - Shell 醒着的原因一变就记一行 `sleep: awake (…)`（屏幕亮着、刚熄屏、刚醒、马上有事这几种几秒内自己过去的不记），一夜的日志说得出它为什么没睡；报告 007 那样会是 `sleep: awake (wake lock phone-bringup)`。
  - 测试：`yocto/bringup-awake-test.py`（假的 sysfs：UDC 的状态、充电器的 `online`、`wake_lock`、`wake_unlock`；旧单元在这里失败），`shell` 的 `TestForeignWakeLockKeepsThePhoneUp`（旧代码不记这一行，失败）。`bitbake phone-bringup phone-qcom` 在 fajita 的构建目录里构建过，包里是新的脚本、单元和填好基带名字的 udev 规则。
- **Shell 什么时候去睡**（`shell/sleep.go`，[28](28-battery.md) §5）：每次例行检查（息屏时每 5 秒）看一遍：屏幕关了 30 秒以上、醒来 10 秒以上；没有通话或来电响铃、没有放音、没在安装更新、没在同步通讯录、信任的电脑没连着 Shell 自己的 USB（bring-up 镜像里控制器归 bring-up，这一条不起作用）、没有 NFC 读卡器、没有被推送或任务叫醒还在跑的应用；下一件事（闹钟、定时通知、任务截止、最迟 15 分钟一次的例行唤醒）在 13 秒以后；别人没持唤醒锁。然后读 `wakeup_count`、再查一遍、写回计数、把 RTC 闹钟设在下一件事前 3 秒、写 `/sys/power/state`。计数写不回去或内核中途放弃（`EBUSY`）时记 `sleep: suspend aborted by a wake-up event`，2026-11-26 起括号里写着哪些唤醒源报了事件。Shell 自己写 `/sys/power/state`，logind 不知道这次挂起，ModemManager 也就不在挂起前关基带（**推断**），来电才叫得醒它。
- **6T 的唤醒源**（读 `sdm845-7.1-rc1` 的内核源码和设备树，**推断**，照 §14.19 核对）：

| 唤醒源 | 怎么叫醒 | 默认 |
|---|---|---|
| 电源键 `pm8941_pwrkey` | 唤醒中断（`enable_irq_wake`）；只看到松开时驱动补报一次按下 | 开（2026-11-24 实测叫得醒，§16.9） |
| 音量键、霍尔传感器（`gpio-keys`） | 设备树的 `wakeup-source` | 开；打开保护套亮不亮屏**未上真机** |
| 充电器插线 `usb-plugin` | 唤醒中断（`devm_pm_set_wake_irq`） | 开 |
| RTC 闹钟（`rtc-pm8xxx`）、`alarmtimer` | 唤醒中断 | 开（2026-11-22 实测叫得醒，§16.4） |
| IPA（移动数据的包） | 唤醒中断 | 开 |
| 基带：QRTR 走的 GLINK `IPCRTR` 通道（来电、短信、QMI 的通告） | GLINK 给每个通道 `device_set_wakeup_capable`，但不打开 | **关**：睡着时来电、短信要等下一次 RTC 唤醒（最迟 15 分钟）；2026-11-26 加了 udev 规则打开基带的这一个（下面） |
| ADSP、CDSP（`qcom_q6v5_pas`，不是基带） | `device_init_wakeup` | 开 |
| USB 控制器、触摸屏、Wi-Fi | 不是唤醒源（§16.4） | — |

  - 基带的通道在 `/sys/bus/rpmsg/devices/4080000.remoteproc:glink-edge.IPCRTR.-1.-1/power/wakeup`；新包 `phone-qcom-wake`（`60-phone-qcom-wake.rules`，6T 的机器文件带上它）在通道出现时写 `enabled`。通道能叫醒以后，GLINK 收到基带的消息时就报一次唤醒事件（读的 `qcom_glink_native.c`，**推断**）；醒来以后 Shell 照来电、新短信亮屏。代价：基带的每条 QMI 通告（信号、注册状态）也会叫醒手机，一夜叫醒几次要量（§14.19 第 2 项）。
  - Shell 醒来写 `sleep: woke after …s (irq N; wake-up sources …)`：除了 `pm_wakeup_irq`，2026-11-26 起还写挂起期间 `wakeup_count` 涨了的唤醒源（`/sys/class/wakeup/wakeup*/`），分得出闹钟、基带的通道和充电器（`hal/pm` 的 `TestWokenByNamesTheWakeUpSources`，旧代码失败；**未上真机**）。
- **挂起以后显示没回来**（§16.5，2026-11-22）：那是在 Nura 上，Kario 的整机以前；面板是怎么关、怎么开的没有记下（**推断**：内核自己在挂起、恢复时关开，恢复时 DSI 没起来），手动再做一次模式设置（`fb0/blank=0`）就好了。Kario 的 Shell 挂起前总是先关面板（屏幕灭了 30 秒才睡），醒来亮屏时渲染器做一次完整的模式设置（`phone-paint` 的 `power(on)`），就是那次手动做的那一步；2026-11-24 第一次整机上电源键叫醒以后屏幕亮了（§16.9），只看过这一次。还会出事的一种：面板上了电，500 ms 内没有一次翻页完成（渲染器的 `but no page flip completed in 500 ms after the panel came on`，指令模式的 DSI 面板没起来就是这样）。以前 Shell 只记下这句，背光照亮、屏是黑的；2026-11-26 起 Shell 碰到它就把面板关掉再开一次（只一次，背光等第二次开好再亮，日志 `panel: on …; turning it off and on again`；`TestPanelOnWithAWarningOffAndOnAgain`，旧代码失败；**未上真机**）。
- **按电源键到亮屏约 10 秒**（§16.9）：那次量的是按下到 USB 和 SSH 回来，不是到屏幕亮，而且是控制台里 `echo mem`，不是 Shell 让它睡的。USB 回来要过：控制器重新上电，电脑重新枚举（一两秒），电脑那头重新 DHCP（NetworkManager 常要几秒），SSH 重连。屏幕要过：内核恢复各设备（UFS、DSI、GPU……，`pm_print_times` 会一个个写用了多久），渲染器读到电源键（日志 `power key: from the renderer`），Shell 让面板开（`panel: on in N ms`；报告 007 里插充电器亮屏时面板是 188 ms），然后背光亮。内核恢复里会凑出 10 秒的只有一处像：DRM 等 `flip_done` 的超时正好 10 秒（`flip_done timed out`，§16.5 那种显示链路的问题）。所以先量（§14.19 第 4 项），没量到以前不改。能缩短的：电脑那头给 USB 网卡设静态地址（`172.16.42.2/24`，§7），省掉 DHCP，只影响测试；内核里慢的设备照 `pm_print_times` 找；真是 `flip_done` 的 10 秒，那是显示链路没起来，要修的是它，不是等的时间。

### 16.13 开机时屏幕上是内核日志（2026-11-26，报告 007）

- **现象**（实测，报告 007 的照片和 `fajita-boot-1.log`）：从引导程序的画面以后到 Shell 出来，屏幕上滚着内核和 systemd 的输出，左上角一个闪动的光标。用户的手机不该这样。
- **原因**：命令行里的 `console=tty0`（§4），加上内核的 fbcon 一有帧缓冲就接管：0.214 s 接管引导程序留下的 simplefb（`Console: switching to colour frame buffer device 135x146`），5.99 s 再接管 msm 的 `msmdrmfb`，之后内核和 systemd 往 tty0 写的都画在面板上。
- **改法**（**未上真机**）：
  - 默认的命令行不再有 `console=tty0`，改加 `vt.global_cursor_default=0`（不画光标）。机器文件里的 `PHONE_BOOT_LOG ??= "0"`；`KASEXTRA=yocto/kas/boot-log.yml` 设成 1，命令行就是原来的 `console=tty0`（§4）。启动镜像和 A/B 布局的 `abboot` 用同一个开关。串口的 `console=ttyMSM0`、pstore、journal 不变，`quiet` 没有加：日志一行不少，只是不画在屏幕上。
  - 内核加 `CONFIG_FRAMEBUFFER_CONSOLE_DEFERRED_TAKEOVER=y`（`phone-console.cfg`，所有用 `phone-kernel.inc` 的内核）：没有 `console=tty0` 时 tty1 照样是 VT，systemd 的 vconsole-setup 往上写转义序列，不开这个 fbcon 照样接管、把屏幕刷黑；开了以后 fbcon 等到真有字要画才接管，没人往 tty1 写字（getty@tty1 和 phone-display 互斥，渲染器直接用 DRM），就一直不接管。内核配置里有 fbcon 而没有这一项，构建就失败（`do_phone_console_kconfig`）。
  - 2026-11-26 构建过（`linux-sdm845`、`phone-fw`、`phone-qcom`，没刷机）：内核的 `.config` 里有 `CONFIG_FRAMEBUFFER_CONSOLE_DEFERRED_TAKEOVER=y`，`boot-oneplus-fajita.img` 的命令行读出来是 `root=PARTLABEL=userdata console=ttyMSM0,115200 phone.gui=1 vt.global_cursor_default=0`；`yocto/boot-console-test.py` 在构建机上查两种布局、开关 `PHONE_BOOT_LOG` 的命令行。
  - 别的机器：QEMU 的命令行由 runqemu 照 `SERIAL_CONSOLES` 拼（`console=ttyAMA0 console=hvc0`），本来就没有 `console=tty0`；`kario-qcom` 的机器文件不写命令行。两者的内核（linux-yocto，也用 `phone-kernel.inc`）同样带上 `DEFERRED_TAKEOVER`，virtio-gpu 上渲染器起来以前也不再有 fbcon 的空控制台和光标。
- **测量**（`fajita-boot-1.log`，内核时间）：simplefb 0.21 s；msm 接手显示 5.75 s（`switching to colour dummy device`）；initramfs 7.12 s 切到 `/sbin/init`；`Reached target Basic System` 约 11 s（这一行只记到秒）；`phone-display` 15.7 s 才起（`Startup finished … = 15.659s`）；`PHONE-GUI-READY` 约 17.3 s（GPU 固件 17.21 s 加载，之后才是第一帧）。改了以后引导程序的画面留到 5.75 s，**从 5.75 s 到第一帧约 11.5 s 屏幕上没有 Kario 的东西**；这段时间面板是黑的、停在最后一幅还是花屏，要看面板驱动探测时有没有复位面板，只有手机上看得到（§14.2「开机画面」）。
- **黑的那 11.5 秒大半是用户空间的顺序**：`phone-display.service` 排在 `phone-telephonyd.service` 后面，telephonyd 又等基带那一串（`phone-qcom-uim` 15:35:16 才结束，紧接着 telephonyd、Shell 同一秒起来）。basic.target 到 Shell 起来有 5 秒。
- **要不要在 phone-initramfs 里画一个静态的标志**：现在不做。能盖住的是 6.75 s（initramfs 起来）到第一帧，约 10.5 s；但是：
  - initramfs 里只有 busybox，要另写一个静态链接的小程序。它不能当 DRM 主控一直开着卡（程序一退出，它的帧缓冲被删掉，CRTC 关掉，又黑了；要留着就得像 plymouth 那样跨过 `switch_root` 活到渲染器接手）；能用的办法是往 `msmdrmfb` 写图、再用 `FBIOPUT_VSCREENINFO` 让内核的 fbdev 做一次模式设置，图留在 fbdev 的帧缓冲上，渲染器第一次模式设置时换掉。
  - 这一次模式设置就是以前 fbcon 在 5.99 s 做的那次，6T 的命令模式面板开机时报过 DSI 的锁相环锁不上（`DSI PLL(0) lock failed`，§16.5、§16.9），多一个开屏的时刻多一份风险；开机日志模式下它还要和 fbcon 抢屏。
  - 标志和 Shell 开机动画的第一帧要对齐，否则 17 秒时跳一下。
  - 先挪顺序更值：让 `phone-display` 不等 telephonyd（Shell 本来就要能在 telephonyd 晚到、重启时工作），开机动画能早约 5 秒出来，剩下的黑屏约 5 秒，再看要不要标志。这两件都没做，记在这里。

### 16.14 插着线关机，起来的是整个系统（2026-11-27，报告 008）

- **现象**（实测，报告 008 的 journal）：插着线在电源菜单里点「关机」：打开菜单、电源键松开以后约 4 秒，上一次启动走的是 `systemd-reboot.service`（`Reached target System Reboot`），不是关机；起来的是整个系统（Shell 和画面都起来了），不是关机充电。再打开电源菜单关机，这次走了 `systemd-poweroff.service`；充电器把手机开起来，命令行里有两个 mode：前面 `androidboot.mode=charger`，后面 `androidboot.mode=normal`，生成器取最后一个，又进了整个系统，充电画面一直没出来。用户看到的是「关机要很久」「没有充电画面」。
- **命令行**：ABL 在充电器开机时照样写 `androidboot.mode=charger`（§16.9 那次只看到它一个），这次后面多了一个 `androidboot.mode=normal`；为什么有时多一个**不知道**（ABL 的源码不在手边）。**改了**：生成器只要命令行里有 `androidboot.mode=charger` 就进关机充电，前后有别的 `androidboot.mode=` 都一样；`systemd.unit=` 仍然优先（[28](28-battery.md) §10，`yocto/charger-generator-test.py` 有报告 008 那样的命令行，旧的生成器在它上面失败；**未上真机**）。
- **关机变成重启**：Kario 里没有把关机改成重启的地方（「关机」一项只调 `systemctl poweroff`）；Shell 的 `power: …` 一行只写在 `/run`，看不出选了哪一项。推断（**未验证**）是手指点到了「关机」下面紧挨着的「重启」：6T 上菜单一行约 5 mm 高。**改了**：点屏幕选「关机」「重启」要再确认一次，这一行进 journal（[28](28-battery.md) §7、§10）。下一轮照 §14.10「关机充电」第 8 步取证；要是那时记的是 `power: poweroff`、上一次启动却还是 `systemd-reboot`，就不是点错，要接着查关机的那一段。
- 这几处改动都不在 OnePlus 6T 的板级文件里（`recipes-bsp`、fajita 的机器和内核文件、`phone-fajita*.yml` 都没动）：生成器在 phone-fw，触屏的确认在 Shell。
