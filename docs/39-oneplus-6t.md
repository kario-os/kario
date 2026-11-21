# OnePlus 6T（fajita）：Kario 的第一台真机

> 一加 6T（骁龙 845，代号 fajita）上跑 Kario：买什么，手机怎么准备，镜像怎么构建，先用 `fastboot boot` 试、再刷入，没有串口怎么调试，开机后先查什么，怎么回到 Android，还有什么没做。设备支持照 Nura（原 postmarketOS，2026-09 改名）的做法：内核、配置、固件、声音配置都取自 Nura 用的同一份，见 §12。
> 状态：在开发机上（2026-11-18，RHEL 8，x86-64，kas-container）构建出了启动镜像和 userdata 镜像（§4，SKU `oneplus-fajita-t9`，镜像里的 `sku.json` 是这台手机的屏幕和 4.5 倍）。检查过：`unpack_bootimg` 读出的头（版本 0、页 4096、偏移、命令行）；启动镜像里的内核逐字节就是 `Image.gz` 接上 6T 的设备树（`dtc` 读出 “OnePlus 6T”、S6E3FC2X01 面板）；Kario 配置片段的 134 项在最终的内核配置里一项不少；根文件系统里该启用的服务都启用了，设备树点名的每个固件文件都在；`e2fsck` 没有问题；sparse 镜像还原后与 ext4 一样；`phone-abslot` 在磁盘镜像上测过（`yocto/abslot-test.py`），`fajita-flash.sh` 对着假的 fastboot 跑过。手机还没到：本文所有关于手机上行为的说法都**未上真机**。

## 1. 一览

| 项 | 做法 |
|----|------|
| 启动 | 手机自带的 bootloader（ABL）直接加载 Android 启动镜像（头版本 0，页 4096，内核偏移 0x8000，ramdisk 0x01000000，tags 0x100，与 Nura 的 deviceinfo 一样），写进 `boot_a` 和 `boot_b` |
| 内核 | sdm845-mainline 的 `sdm845-7.1-rc1-r0`（Nura 的 linux-postmarketos-qcom-sdm845 用的标签），Nura 的配置加 Kario 的配置片段；`Image.gz` 后面接 `sdm845-oneplus-fajita.dtb` |
| 根文件系统 | 整个 `userdata` 分区就是一个 ext4（4 KiB 块，不再分区），内核按 `root=PARTLABEL=userdata` 自己挂上，**没有 initramfs**（启动镜像里是一个空的 cpio）；第一次开机长到整个分区 |
| A/B 槽 | 开机后 `phone-qcom-abslot` 把这次启动的槽在 GPT 属性里标成“启动成功”，bootloader 就不会数完次数退回另一个槽 |
| 调试 | USB 线上的串口控制台和 SSH（§7）；上一次开机的内核日志留在 ramoops 里 |
| 机型 | Yocto 机器 `oneplus-fajita`（`yocto/meta-phone/conf/machine/oneplus-fajita.conf`），kas 文件 `yocto/kas/phone-fajita.yml`，SKU `oneplus-fajita-t9`（板子 `sku/boards/oneplus-fajita.toml`，[24](24-sku-framework.md)） |

为什么不用 initramfs：ABL 在启动镜像自己的命令行里看到 `root=` 时，把它当开发镜像，不再追加系统分区的 `root=`、`skip_initramfs`、`ro init=/init` 和槽后缀（高通 ABL 的源码，SHIFT6mq 公开的那份，同为骁龙 845；一加的 ABL 不公开，所以根文件系统里还留了 `/init` 链接和 fstab 的 `/` 行，万一它还是追加了 `ro init=/init` 也能起来）。UFS、GPT、ext4 都编进内核，挂根不需要模块。

为什么不用 U-Boot + systemd-boot：Nura 现在给 sdm845 同时装 systemd-boot，那是给另刷了 U-Boot（UEFI）的人用的；用原厂 bootloader 时 Nura 刷的也是同样的 Android 启动镜像。多一层 U-Boot 就多一个要刷、要调的东西，第一台真机先用最少的。

两种测试，先后做，**都要先做 §3.5 的备份**（第一次往手机写任何东西之前）：

1. chroot：手机装 Nura，我们的根文件系统在它的内核上跑（`tools/phone-chroot/kario-phone.sh --sku oneplus-fajita-t9`，[24](24-sku-framework.md) §6.2）：应用、Shell、自测、兼容测试、像素自测、内存和耗电，用 Nura 的内核、屏幕、调制解调器和声音。
2. 整机：手机启动我们自己的镜像（§5 先试、§6 刷入），内核、固件、启动和 A/B 槽都是我们的。

回到 OxygenOS 见 §9；最坏的情况（进不了 fastboot）有 §3.4 的 MSM 下载工具，它之后用 §3.5 的备份把 IMEI 和校准数据写回去。

## 2. 买什么

- 一加 6T，国行 A6010 就行（和国际版是同一种硬件，只是出厂的系统是氢 OS）。8 GB 或 6 GB 内存都行。屏幕要好，碎屏或换过的屏可能不是原装面板。
- 键盘：**蓝牙键盘**。USB-C OTG 键盘现在用不了：主线设备树里 6T 的 USB 只做设备（`dr_mode = "peripheral"`），手机也不给 VBUS 供电；改成主机模式又会丢掉 §7 的 USB 控制台。
- 一根能传数据的 USB-C 线，一台装了 `fastboot` 和 `adb`（android-tools）的 Linux 电脑（§3.5 的备份还要一个 6T 的 TWRP 镜像）。
- 一张 SIM 卡。中国的运营商大多要 VoLTE 才能打电话，VoLTE 还没做（§11.1）；先试短信、数据和 2G 通话（还有 2G 的地方）。

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

一加 6T 真变砖（进不了 fastboot）时，用高通 EDL 模式加 OnePlus 的 MSM 下载工具（Windows，社区发布的 fajita 包）恢复：关机，按住音量上和音量下插 USB 线，手机进 EDL（9008），工具把所有分区写回出厂状态，bootloader 也会重新上锁。Kario 只写 `dtbo`、`boot`、`userdata` 三处（§6），一般用不到它；但它是最后的退路，动手前先找好对应型号的包。它会清掉或换掉 `persist`（指纹和传感器的校准）和存 IMEI 的分区，出厂包里的不是这台手机的：用过它以后，用 §3.5 的备份 `fajita-flash.sh restore-efs` 写回去。

### 3.5 备份：只有这台手机有的分区

解锁之后、第一次往手机写任何东西之前（装 Nura 也算），把这几样拷到电脑上：`persist`（指纹和传感器的校准，换一台手机的不能用）、`modemst1`、`modemst2`、`fsg`、`fsc`（IMEI 和基带的配置，丢了就没有 IMEI、打不了电话），两个槽的 `boot` 和 `dtbo`（回 OxygenOS 用），还有每个 LUN 的 GPT。

```
yocto/fajita-flash.sh backup twrp-fajita.img
```

`twrp-fajita.img` 是能在 OxygenOS 11 上 `fastboot boot` 起来的 6T 的 TWRP（twrp.me 的 fajita 页，或 XDA 上社区维护的版本）。脚本 `fastboot boot` 它，只在内存里跑，手机上什么也不写；经 TWRP 的 adb 逐个 `dd` 出分区，每个都和手机上的 SHA-256 对一遍；写 `SHA256SUMS` 和 `phone.txt`（序列号、时间、`boot` 是不是 OxygenOS 的）；最后回到 fastboot。默认放在 `~/kario-phone-backup/oneplus-fajita-<序列号>/`，不在仓库里；**再拷一份到别的盘或网盘**：它是这台手机的 IMEI 和校准数据唯一的副本。已经有备份时脚本什么也不做，不会拿后来改过的手机覆盖它。

`test`、`flash`、`restore` 都先核对这份备份（同一个序列号、每个文件的 SHA-256），对不上就拒绝，手机上什么也不写。

这一步对着模拟的手机测过（`yocto/fajita-flash-test.py`，在 `make test` 和 CI 里），**未上真机**。

## 4. 构建

```
make yocto-build WORK=$HOME/yocto/fajita-work KASFILE=yocto/kas/phone-fajita.yml SKU=oneplus-fajita-t9
```

- SKU `oneplus-fajita-t9` 是这台手机当九宫格直板用（[24](24-sku-framework.md) §7.4）：它生成 `phone-sku` 的 `sku.json`（Shell 按 1080×2340 的屏幕、4.5 倍画，上下各留 80 行给刘海和圆角）和 kas 覆盖（机器 `oneplus-fajita`；整机用内核自己的设备树，不叠加覆盖层）。整机没有 `make sku` 的固件包，构建出来的镜像直接刷（§5、§6）。
- kas 文件沿用 `phone-qemuarm64.yml` 的发行版和软件包，去掉 A/B 系统槽、dm-verity 和 wic 磁盘（这台手机上还没有），加 meta-qcom。上游各层的提交固定在 `phone-qemuarm64.yml` 里，和 QEMU 镜像用同一套（之后的 wrynose 让 udev 依赖 iproute2-devlink，最后要 bash，GPL-3.0）。并发 16（`BB_NUMBER_THREADS`、`PARALLEL_MAKE`）。
- `PHONE_BRINGUP = "0"`（local.conf 或 kas 覆盖）构建不带 §7 调试手段的镜像。

产物在 `$WORK/build/tmp/deploy/images/oneplus-fajita/`：

| 文件 | 写到哪 | 大小（2026-11-18 的构建） |
|------|--------|------|
| `boot-oneplus-fajita.img` | `boot_a`、`boot_b` | 15 892 480 B (15.2 MiB) |
| `phone-image-oneplus-fajita.rootfs.ext4.sparse` | `userdata`（全零的一段只传 4 个字节） | 464 700 388 B (443 MiB) |
| `phone-image-oneplus-fajita.rootfs.ext4` | 同一个文件系统，原始格式 | 717 559 808 B (684 MiB) |

启动镜像由 AOSP 的 mkbootimg 做（`mkbootimg-native`）；sparse 镜像由 `yocto/meta-phone/files/phone-img2simg.py` 做：全零和重复同一个字的块写成 fill 块，不写成 “don't care”，分区里原来的数据不会在文件系统以为是零的地方露出来。检查启动镜像：`unpack_bootimg --boot_img boot-oneplus-fajita.img`（同一套 AOSP 工具，构建里在 `tmp/sysroots-components/x86_64/mkbootimg-native/usr/bin/`）应该列出头版本 0、页 4096、上面的偏移和命令行。

## 5. 先试：fastboot boot

```
yocto/fajita-flash.sh -n test     # 只打印要执行的 fastboot 命令
yocto/fajita-flash.sh test
```

先要有 §3.5 的备份。`test` 擦掉 `dtbo_a`、`dtbo_b`，然后 `fastboot boot` 启动镜像一次：`boot` 和 `userdata` 都不写，重启后 bootloader 又从自己的 `boot` 分区启动。dtbo 必须擦：不擦的话 ABL 会把 Android 的设备树覆盖层套到主线设备树上，手机停在 fastboot（sdm845-mainline 的安装说明）。擦了 dtbo，OxygenOS 在 dtbo 刷回去之前也起不来（§9）。

这一步 userdata 里还是 Android 的数据，Kario 的根文件系统挂不上：能看到的是内核在屏幕上的输出（`console=tty0`）、USB 上出现的设备，挂根失败时内核停在那里。确认屏幕亮、内核起来，再刷入。脚本先检查 `fastboot getvar product` 是 `sdm845`，不是就拒绝；bootloader 报的是芯片，一加 6 也是 `sdm845`，所以写之前还要你确认是 6T（`-y` 跳过确认）。

## 6. 刷入

```
yocto/fajita-flash.sh flash
```

先要有 §3.5 的备份。`boot` 和 `userdata` 要来自同一次构建：Nura 的配置强制 lockdown（integrity），内核只加载签了名的模块，签名用的是那次构建自己生成的密钥。

依次：擦 `dtbo_a`、`dtbo_b`；`boot_a` 和 `boot_b` 都写 Kario 的启动镜像（bootloader 退回另一个槽时还是 Kario，一位 6T 用户就遇到过没标“成功”的槽冷启动退回了 OxygenOS）；`fastboot -S 512M flash userdata` 写 sparse 镜像（bootloader 一次最多收 512 MiB，fastboot 分块发）；重启。`-s` 选手机，`-d` 换镜像目录，`-n` 只打印。

第一次开机：systemd 把 ext4 长到整个 userdata（`x-systemd.growfs`），`phone-qcom-abslot` 标槽，然后 Kario 的界面出来。

## 7. 调试控制台

没有串口（6T 的调试串口只在主板测试点上），调试走 USB 线。`PHONE_BRINGUP = "1"`（机器的默认值）时镜像里有 `phone-bringup`：

- 开机早期绑定一个 USB 设备（`kario-bringup`，1d6b:0104），带两个功能：CDC-ACM 串口（手机上是 `ttyGS0`，root 自动登录）和 CDC-NCM 网卡（手机上是 `dbg0`，地址 172.16.42.1，给电脑分 172.16.42.11–20）。它先占了 USB 控制器，Shell 自己的 USB 共享这时不工作；它不叫 `usb0`，Shell 给 USB 链路设的防火墙不管它。
- 一个唤醒锁让手机不休眠，USB 控制台不会断。
- 内核日志：设备树里有 ramoops 区（0xac300000，4 MiB），上一次开机的控制台和崩溃日志重启后还在；systemd-pstore 每次开机把它们挪到 `/var/lib/systemd/pstore`；journal 也存在 userdata 上。

```
picocom /dev/ttyACM0                 # 串口：电脑上出现 ttyACM0（screen /dev/ttyACM0 也行）
ssh root@172.16.42.1                 # 网络：电脑的新网卡用 DHCP；没有就 ip addr add 172.16.42.2/24 dev <网卡>
journalctl -b -1                     # 上一次开机的日志
bringup-log                          # pstore 留下的上一次内核日志（-a：全部）
phone-abslot status                  # 两个槽的状态
systemctl stop phone-bringup-awake   # 放开唤醒锁，试休眠
```

lockdown（integrity）下 debugfs 和没签名的模块都不行：要看 `/sys/kernel/debug` 得换一个关掉 `CONFIG_LOCK_DOWN_KERNEL_FORCE_INTEGRITY` 的内核。

root 没有密码，谁有线谁就是 root：这是开发镜像，只在调试时用。`PHONE_BRINGUP = "0"` 的镜像里没有这些（dropbear 和空密码是 `phone-qemuarm64.yml` 的设定，所有开发镜像都有）。

## 8. 先查什么

| 项 | 怎么看 | Nura 上 |
|----|--------|---------|
| 屏幕 | 开机先是内核和 systemd 的文字，然后 Kario 的界面；`dmesg \| grep -i -e dsi -e dpu` | 能用 |
| 按键 | 电源键、音量键：`cat /proc/bus/input/devices`（pm8941_pwrkey、gpio-keys），界面有反应 | 能用 |
| 槽 | `phone-abslot status`：这次的槽 `successful` | — |
| Wi-Fi | `ip link` 里有 `wlan0`；设置 › Wi-Fi 连一个网络 | 能用 |
| 蓝牙 | `journalctl -u phone-qcom-btaddr` 给了地址；设置 › 蓝牙配对键盘 | 能用 |
| 调制解调器 | `cat /sys/class/remoteproc/*/state`（running）；`systemctl status rmtfs tqftpserv phone-qcom-uim`；`mmcli -L` | 能用 |
| SIM | `mmcli -m 0`；`qmicli -d qrtr://0 --uim-get-card-status` | 能用 |
| 短信 | 发一条、收一条（信息应用）；`mmcli -m 0 --messaging-list-sms` | 能用 |
| 数据 | 设置里开移动数据；`mmcli -m 0` 的承载、`ip addr` 里的 rmnet 接口 | 能用 |
| 通话 | 打出、接听；听筒有没有声音、对方听不听得见，免提、静音（[20](20-audio.md) §10 末尾的清单）；`cat /run/phone/audiod-ucm.json` 是当前的 UCM 设置，`journalctl -u phone-audiod` 里看 `call routing` | 能用（Nura 用 callaudiod 切换声音路由） |
| 声音 | `aplay -l`；`systemctl status phone-audiod`（开机后第一次启动时它经 alsaucm 跑 UCM 配置的引导序列、设好 HiFi 和扬声器，放到 `hw:0,2`）；按键音、铃声从扬声器出来；`aplay -D plughw:0,2 <wav 文件>` | 能用 |
| 电池 | `cat /sys/class/power_supply/*/capacity`；充电时 `status` | 能用（没有 Dash 快充） |
| 休眠 | `systemctl stop phone-bringup-awake`，息屏后等它睡，按电源键唤醒；`cat /sys/power/suspend_stats/success` | 能用 |

“Nura 上”一栏是 Nura 设备页的说法，不是 Kario 的结果；Kario 这一栏全部**未上真机**。

## 9. 回到 Android

Kario 只写了 `dtbo_a/b`、`boot_a/b` 和 `userdata`；`system`、`vendor` 和各固件分区还是 OxygenOS 的。回去的办法，从轻到重：

- `yocto/fajita-flash.sh restore`：把 §3.5 备份里两个槽的 `boot`、`dtbo` 写回去，`fastboot -w` 清空 userdata，重启进 OxygenOS（清空后第一次开机要几分钟）。备份是装了 Nura 或 Kario 以后才做的（`phone.txt` 里 `stock-boot=no`），就从 OxygenOS 11.1.2.2 完整包里取 `boot.img`、`dtbo.img`（包里是 `payload.bin`，用 payload-dumper 一类工具解开），`--stock 目录` 用它们。
- IMEI 没了、指纹录不进（MSM 工具或别的东西动了那几个分区）：`yocto/fajita-flash.sh restore-efs twrp-fajita.img`，经 TWRP 把备份里的 `persist`、`modemst1`、`modemst2`、`fsg`、`fsc` 写回去，每个写完再读一遍核对。
- 进不了 fastboot：MSM 下载工具（§3.4），然后 `restore-efs`。

不要做的：手机上不是原厂系统时**不要重新上锁 bootloader**（`fastboot oem lock` / `flashing lock`），会锁死成砖；不要 `fastboot erase` 或刷写 `modem`、`persist`、`modemst*`、`fsg`、`fsc` 这些分区（脚本只碰 `dtbo`、`boot`、`userdata`，`restore-efs` 只在核对过的备份上写回）。

`restore`、`restore-efs` 对着模拟的手机测过（`yocto/fajita-flash-test.py`），**未上真机**。

## 10. 组成与许可

| 部分 | 来源（固定的版本） | 许可 |
|------|------|------|
| 内核 `linux-sdm845` | gitlab.com/sdm845-mainline/linux 标签 `sdm845-7.1-rc1-r0`（85f1df2a）；Nura pmaports 02c676bc 的 `config-postmarketos-qcom-sdm845.aarch64` 做基础配置（构建时下载）；Kario 的 `phone-*.cfg` | GPL-2.0-only |
| 固件 `firmware-oneplus-sdm845` | gitlab.com/sdm845-mainline/firmware-oneplus-sdm845（3e31a0c3，Nura 的 19 版装的那些文件），加 linux-firmware 的 Adreno 630、WCN3990、蓝牙文件 | `CLOSED`：构建时下载，不进 git；装了它的镜像只给机主自己测试，不能发布或转给别人 |
| 声音配置 `alsa-ucm-conf-sdm845` | gitlab.com/sdm845-mainline/alsa-ucm-conf（1b8d290e）里一加 6/6T 的文件，放在 alsa-ucm-conf 旁边；6T 的 `Voice Call` 后面接上 Kario 的通话扬声器 `Speaker1`（`fajita-voicecall-speaker.conf`，[20](20-audio.md) §10） | BSD-3-Clause（Kario 补的设备 Apache-2.0） |
| 通话声音 `q6voiced` 0.3.1 | gitlab.nura.eco/nura/q6voiced（21061751） | MIT |
| `qrtr` 1.2、`rmtfs` 1.3、`tqftpserv` 1.2 | meta-qcom wrynose（c791f2b7，另加它要求的 meta-lts-mixins wrynose/linux-firmware d0fd372d） | BSD-3-Clause |
| ModemManager 1.24.2、libqmi 1.38（开了 QRTR） | meta-oe | GPL-2.0+ / LGPL-2.1+ |
| `mkbootimg-native`（只在构建机上） | android.googlesource.com/platform/system/tools/mkbootimg（d2bb0af5） | Apache-2.0 |
| `phone-qcom`：`phone-abslot`、`phone-btaddr`、`uim-select`、phone-audiod 的 UCM 参数（`PHONE_AUDIO_*`：放音和通话的 verb、设备、PCM） | Kario（`yocto/meta-phone/recipes-bsp/phone-qcom`） | Apache-2.0 |
| `phone-bringup` | Kario | Apache-2.0 |

没有用的：qbootctl（GPL-3.0，Kario 的镜像不收 GPLv3，由 `phone-abslot` 代替）、hexagonrpcd（GPL-3.0+，所以没有传感器）、msm-modem 的 UIM 选择（GPL-3.0+，由 `uim-select` 代替）、bootmac（GPL-3.0+，由 `phone-btaddr` 代替）、callaudiod（GPL-3.0+，由 phone-audiod 的 UCM 通话路由代替）。Kario 的这些代替品是照它们要做的事重新写的，没有用它们的代码。

`scripts/license-check.py` 只管平台的 Go 模块和渲染器的 crate，不看镜像里装了什么；镜像的许可边界是 kas 里的 `INCOMPATIBLE_LICENSE`（不收 GPLv3），`CLOSED` 的固件不受它限制，所以发布时要自己挡住（见上一行）。

## 11. 还没做的

- 通话的声音：写好了，**未上真机**。phone-audiod 按声卡的 UCM 配置切换（[20](20-audio.md) §10）：开机后第一次启动跑配置的引导序列、设 `HiFi` + `Speaker`；telephonyd 有通话时（拨号中、对方振铃、接通、保持）换到 `Voice Call` + `Earpiece` + `Mic`，混音放到听筒的 `hw:0,0`；通话界面开免提时换成 `Speaker1`（配置里没有，Kario 补的，放到 `hw:0,2`）；静音时去掉 `Mic`；最后一路通话结束回到 `HiFi`，下一通从听筒开始；audiod 通话中重启会接着原来的设置。还缺：耳机（6T 没有 3.5 mm 插孔，主线 USB 只做设备，USB-C 耳机接不上；audiod 也不知道插孔状态）、蓝牙通话、免提的回声消除（主线 q6voice 没有 Android 的 ACDB 校准）。听筒、免提、静音和引导序列的音量都要在手机上听，见 20 §10 末尾的清单。
- VoLTE：没有做，调研见 §11.1。
- 传感器（hexagonrpcd 是 GPL-3.0）、相机（vivid 代替）、屏下指纹、NFC、USB 主机、Dash 快充、提醒滑块、马达。
- A/B 系统槽、dm-verity 和 OTA（[25](25-system-update.md)）在这台手机上还没有：userdata 只有一个根文件系统。
- phone-paint 用 CPU 画（dumb buffer），没用 Adreno 630。

### 11.1 VoLTE（调研，没有代码）

中国的运营商大多要 VoLTE 才能打电话（§2），这是 6T 当电话用的最大缺口。下面是 2026-11-18 读源码和公开讨论的结果，全部**未上真机**。骁龙 845 上 IMS 的协议栈（SIP、RTP、语音编码）跑在基带固件里，Linux 这边要做的是给基带选对运营商配置、替它连上 IMS 的数据网：

| 部分 | 做什么 | 现状 |
|------|--------|------|
| 运营商配置（MBN） | 基带按运营商打开 IMS、VoLTE 和它们的参数。文件在 `modem` 分区（vfat）的 `image/modem_pr/mcfg/configs/mcfg_sw/…`，Android 上由厂商的服务按 SIM 卡选，经 QMI PDC 装进基带 | Kario 的固件包和 Nura 的一样，不装 `modem_pr`；基带自己会不会经 tqftpserv 去读、读哪一个，**未知** |
| IMS 数据（QMI 服务 770） | 基带请应用处理器为 IMS 开一个数据连接（APN 一般是 `ims`） | Nura 用 81voltd：收到请求就经 ModemManager 的 `CreateBearer`（APN、IP 版本、3GPP 配置号）开承载 |
| ModemManager | 在已有的上网承载之外再开一个（多路复用的 rmnet），带配置号 | 镜像里是 1.24.2，有 `profile-id` 和多路复用；QRTR 上开第二个承载**未验证** |
| telephonyd | 不把 IMS 承载当成移动数据 | 现在会当成，见下 |
| 通话的声音 | VoLTE 通话也走 DSP 的语音通路，q6voiced 和 audiod 的 UCM 设置照用 | IMS 通话在 ModemManager 里是不是普通的 Call 对象，**未知** |

**81voltd**：gitlab.nura.eco/modem/81voltd 的 1.2.0，Alpine community 里有这个包，Nura 的 `soc-qcom-modem` 依赖它。许可是 **GPL-2.0-or-later**（LICENSE 是 GPLv2 原文，每个源文件都标 `GPL-2.0-or-later`），不是 GPL-3.0，可以进镜像。大约两千行 C，依赖 libqmi（`qmi-glib`）、ModemManager 的 `mm-glib` 和 libqrtr，meson 构建；三样在 Yocto 里都有（meta-oe、meta-qcom），配方二十行上下。它自己写明的问题：停掉它不会断开它开的承载；ModemManager 的错误不告诉基带。加进它的 pmaports 合并请求说它是临时办法，长远要并进 ModemManager。

**和 telephonyd 冲突的地方**（所以不只是加一个包）：

- telephonyd 把任何一个连上的承载都当成「移动数据已连接」：IMS 承载一连上，它就不再为上网开承载，数据开关开着也没网。
- 关数据开关（和飞行模式）时它调 `Simple.Disconnect("/")`，断开**所有**承载，IMS 的一起断，VoLTE 跟着没了。
- `status` 的 `data_ifaces` 会带上 IMS 承载的接口，Shell 的防火墙和路由就把它当成上网的接口。

要改的：按 APN（`ims`）或承载的 `apn-type` 认出 IMS 承载，数据的逻辑不管它，断开时只断自己开的那个。

**手机到了怎么试**（按顺序，每一步都能退回）：

1. 先做 §3.5 的备份：装错 PDC 配置会让基带行为异常（Nura 的开发者自己碰到过），`modemst1/2`、`fsg` 的备份是退路。
2. 在 OxygenOS 里看这张 SIM 卡在 Android 上能不能 VoLTE；把 `modem` 分区里的 `image/modem_pr` 拷出来，看有没有中国运营商的目录（`…/generic/china/…`）。
3. 在 Nura 上（§13.4 的测试一）装上 81voltd：`qmicli -d qrtr://0 --pdc-list-configs=software` 看基带里有哪些配置、哪个在用，需要时 `--pdc-load-config` 装对应的 `mcfg_sw.mbn`、`--pdc-activate-config` 激活，然后打 VoLTE 电话。社区的记录：Nura 上 6T 和 Pixel 3a 用 81voltd 能打 VoLTE，有的运营商（澳洲 Telstra）不行；有人在 LineageOS 的 6T 上用 qmicli 换配置，德国的卡用「Volte_OEM_Lab」能通。
4. 通了再做 Kario 这边：81voltd 的配方、telephonyd 认出 IMS 承载、要不要由 Kario 按 SIM 的 MCC/MNC 选 MBN。

**工作量**：81voltd 的配方和 telephonyd 区分承载，一两天；选 MBN、装进基带（用 qmicli，或者自己发 QMI PDC），几天；最难估的是运营商兼容性，只能拿真卡一家一家试。

**风险和未知**（没有手机都答不了）：

- OxygenOS 11.1.2.2 的 `modem_pr` 里有没有中国移动、联通、电信的 VoLTE 配置，基带会不会按 SIM 卡自己选。
- 装 PDC 配置会不会弄乱 EFS（配置存在基带的持久存储里）。
- ModemManager 1.24 在 QRTR 上开第二个承载行不行，81voltd 给的配置号对不对。
- IMS 通话在 ModemManager 里是不是普通的 Call 对象（telephonyd、q6voiced、audiod 都靠它），通话中切到 2G、3G（SRVCC）时怎么样。
- 紧急呼叫和经 IMS 收发的短信怎么走。

## 12. 来源

- Nura 的 pmaports（gitlab.nura.eco/nura/pmaports，读的是 02c676bc，2026-11-17）：`device/community/device-oneplus-fajita`（deviceinfo：启动镜像格式、偏移、命令行、刷机方法），`linux-postmarketos-qcom-sdm845`（标签和配置），`firmware-oneplus-sdm845`（文件清单），`alsa-ucm-conf-sdm845`，`soc-qcom`（模块和服务的组合）。
- sdm845-mainline（gitlab.com/sdm845-mainline）：内核、固件、UCM，以及 pmtools 的安装说明（为什么要擦 dtbo）。通话路由读的是 alsa-ucm-conf 1b8d290e 的 `OnePlus/fajita`、`Xiaomi/beryllium` 和提交历史（`HiFi` 里也有 q6voice 路由的原因、`Mic1` 改名的原因），内核 `sdm845-7.1-rc1-r0` 的 `q6voice-dai.c`（有哪些语音混音控件、没有静音控件）。
- 通话：q6voiced 0.3.1 的源码（哪些 ModemManager 通话状态打开语音通路）；alsa-utils 的 alsaucm 和 alsa-lib 的 `src/ucm/main.c`（一次运行一个状态、换 verb 先拆旧设备、关没开的设备什么也不做）；OnePlus 6T 没有耳机插孔：主线设备树的提交说明（Caleb Connolly，2020）。
- VoLTE：81voltd 1.2.0 的源码和 Alpine 的 APKBUILD；Nura pmaports f2795e91（2026-11-18）的 `soc-qcom`、`firmware-oneplus-sdm845`；pmaports 的合并请求 !5387（81voltd）、!4674（`modem_pr` 文件）和 issue #1878（OnePlus 6 的 VoLTE）；openimsd.com 的「Activating Voice over LTE on Android」（6T 上用 qmicli 换 PDC 配置）。
- linux-msm（github.com/linux-msm）的 qrtr、rmtfs、tqftpserv，Qualcomm 的 meta-qcom 里的配方。
- 高通 ABL 的命令行处理：SHIFTPHONES/android_bootable_bootloader_edk2（SHIFT6mq，骁龙 845）。
- `fastboot getvar product` 在 6T 上报 `sdm845`：XDA 论坛上 6T 用户贴的 `getvar all`；没标“成功”的槽会退回 OxygenOS：一位在 6T 上装 Arch 的用户的记录。

## 13. 手机到了：一步一步

在你自己的 Linux 电脑上做，不用登构建机。哪一步的输出和这里说的不一样就停下，把输出发给我。整个过程**未上真机**。

### 13.1 电脑

1. 装 `adb`、`fastboot`（android-tools）、`python3`、`zstd`、`ssh`、`git`。
2. 取代码：`git clone https://github.com/kario-os/kario && cd kario`（下面的命令都在这个目录里跑）。
3. 从构建机拷三个镜像（固件是 `CLOSED` 的，只给你自己测，不要传到别处）：

   ```
   mkdir -p ~/kario-images
   B=<构建机>
   rsync -aL --progress \
     $B:yocto/fajita-work/build/tmp/deploy/images/oneplus-fajita/boot-oneplus-fajita.img \
     $B:yocto/fajita-work/build/tmp/deploy/images/oneplus-fajita/phone-image-oneplus-fajita.rootfs.ext4.sparse \
     $B:yocto/kario-work/build/tmp/deploy/images/qemuarm64/phone-image-qemuarm64.rootfs.ext4.zst \
     ~/kario-images/
   ```

4. 下载 6T 的 TWRP（§3.5，存成 `~/kario-images/twrp-fajita.img`）和 MSM 救砖包（§3.4，Windows 上用，先放着）。
5. chroot 套件连手机用的钥匙：`ssh-keygen -t ed25519 -f ~/.ssh/kario-board-test -N ''`。

### 13.2 手机：系统和解锁

6. 国行先刷成 OxygenOS，再本地升级到 11.1.2.2，两个槽都装一遍（§3.1、§3.2）。
7. 解锁 bootloader（§3.3，会清空手机）。

### 13.3 备份：第一次往手机写东西之前

8. 手机进 fastboot（关机后按住音量上 + 电源），插 USB 线：

   ```
   yocto/fajita-flash.sh backup ~/kario-images/twrp-fajita.img
   ```

   最后一行是 `backup in .../kario-phone-backup/oneplus-fajita-<序列号>` 才算完成，手机回到 fastboot。
9. 把 `~/kario-phone-backup` 整个再拷一份到别的盘或网盘。

### 13.4 测试一：Nura 上的 chroot

10. 按 Nura 的安装页刷 Nura（[24](24-sku-framework.md) §6.2 第 3 步：界面选 console，装 SSH；一加要 `fastboot erase dtbo`）。
11. 开机后 USB 线接电脑，手机在 `172.16.42.1`：把 `~/.ssh/kario-board-test.pub` 加进手机 root 的 `authorized_keys`（或者用默认用户，下面每条命令加 `--become doas`）。
12. 在 Nura 里用 `bluetoothctl` 配对蓝牙键盘。
13. 自动测试（要几个小时；先核对 §13.3 的备份，没有就不往手机推任何东西）：

    ```
    K="tools/phone-chroot/kario-phone.sh --sku oneplus-fajita-t9 --image $HOME/kario-images/phone-image-qemuarm64.rootfs.ext4.zst"
    $K push setup selftest pixel collect
    ```

14. 手动试：`$K shell-start`，屏幕上出 Kario，用蓝牙键盘打电话、发短信、连 Wi-Fi；`$K mem`；`$K shell-stop`。耗电：`$K power-start`，熄屏放 30 分钟，`$K power-stop`。
15. 把结果发给我：`tar -czf fajita-chroot.tgz -C build/phone/oneplus-fajita-t9 results`。
16. 让 Nura 回到原样：`$K teardown`。

### 13.5 测试二：整机

17. 手机进 fastboot，先试一次（`boot` 和 `userdata` 都不写，§5）：

    ```
    yocto/fajita-flash.sh -d ~/kario-images test
    ```

    看屏幕上有没有内核的输出，电脑上有没有出现 USB 串口和网卡。
18. 刷入（§6）：`yocto/fajita-flash.sh -d ~/kario-images flash`。
19. 第一次开机按 §8 查。屏幕不亮也照样取日志（USB 控制台不靠屏幕）：

    ```
    ssh root@172.16.42.1 'bringup-log; journalctl -b --no-pager' > fajita-boot-1.log
    ```

    把 `fajita-boot-1.log` 发给我。

### 13.6 回去

20. 回 OxygenOS：手机进 fastboot，`yocto/fajita-flash.sh restore`（§9；备份是装 Nura 之前做的，就直接用它）。
21. 进不了 fastboot：MSM 工具（§3.4），然后 `yocto/fajita-flash.sh restore-efs ~/kario-images/twrp-fajita.img` 写回 IMEI 和校准数据。
