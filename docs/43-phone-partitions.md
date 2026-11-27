# 手机的分区：Kario 写哪些、读哪些、绝不碰哪些

> Android 手机上有几十个分区。这里按作用把它们分类，定下 Kario 在每一类上做什么，再说一加 6T 上的两种布局（现在的 userdata 布局和新的 A/B 布局）、板子文件里的分区表、刷机脚本的保护，以及别的手机怎么接上。
> 状态：设计已定；能在电脑上查的部分已实现：板子文件的分区表和它的检查（`tools/sku.py`）、刷机脚本按分区表刷两种布局（写之前量大小、绝不写本机独有和固件的分区、演练列出要写的）、`phone-abslot` 和 RAUC 的后端 `rauc-abl`、initramfs 按槽挂根，单元测试和对着模拟手机的测试 ✓。A/B 布局的镜像 2026-11-24 构建过一次（构建机的 bt-work，没开过机），全部**未上真机**；上手机前要查的在 [39](39-oneplus-6t.md) §15。userdata 布局照旧是默认；2026-11-24 起它的启动镜像也带 phone-initramfs，按分区名挂 userdata（[39](39-oneplus-6t.md) §1 为什么），QEMU 上 ✓（`make yocto-userdata-test`），**未上真机**。Android 的新分区机制（AVB、动态分区、Virtual A/B、dlkm 分区）Kario 要哪些，决定在 §7，都**未实现**。

## 0. 结论

- Android 手机的分区按作用分几类（§1）。Kario 只写自己的几种角色：启动镜像（`boot`）、根文件系统（`root`）、数据（`data`）、持久区（`state`），另外擦掉 Android 的设备树覆盖层（`dtbo`）；验证元数据（`vbmeta`）、`misc`、固件、Android 自己的分区和这台手机独有的数据（EFS、`persist`）一律不写。
- 每块板子在板子文件里有一张分区表（`[partitions]`：分区名 → 角色，§2）；刷机脚本、initramfs、RAUC、更新包、chroot 套件都从它取名字。别的手机写自己的名字，代码不变。
- 一加 6T 有两种布局（§3）：**userdata 布局**（现在的做法，默认：整个 userdata 是根文件系统）和 **A/B 布局**（根文件系统在 `system_a`、`system_b`，经 dm-verity 只读；数据在 userdata；RAUC 更新不在运行的那个槽，起不来时 ABL 退回另一个槽）。
- 6T 的持久区（清除的请求、防盗锁的记录）留在 userdata 上的 `/var/lib/slots`（理由见 §3.4）；自己定分区表的产品板另给一个小的 `state` 分区，和 QEMU 的磁盘一样。
- 刷机脚本在任何路径上都不写 EFS、`persist`、基带和引导链的分区，板子文件怎么写都一样；写之前先读每个要写的分区多大，镜像放不下就一个字节也不写；演练列出要写的每一个分区（§4）。
- Android 的新分区机制（§7）：AVB 量产时要（Kario 签的 vbmeta 验启动镜像，根槽接在启动镜像里的描述后面，防回滚，bootloader 用 Kario 的钥匙锁上）；动态分区（`super`）只为兼容 Android 10 起出厂的手机；Virtual A/B 以后给存储小的手机做；`vendor_dlkm`、`system_dlkm` 不要。都**未实现**。

## 1. Android 手机上有哪些分区

6T 上核对过的分区名和设备号在 [39](39-oneplus-6t.md) §9.1；下表的名字是骁龙 845 一类 A/B 手机常见的，6T 的完整清单要到手机上读（§3.2）。「Kario」一列是 6T 上的做法，「角色」是板子文件里给它的（§2）。

| 分区（例） | 作用 | Kario | 角色 |
|------|------|------|------|
| `boot_a`、`boot_b` | bootloader 加载的 Android 启动镜像：内核、ramdisk、命令行；系统即根（system-as-root）的手机上 ramdisk 是 recovery 的 | 写 Kario 的启动镜像，两种布局都带 phone-initramfs：userdata 布局的 ramdisk 里写着布局，A/B 布局的带根的签名 verity 描述（§3.3） | `boot` |
| `dtbo_a`、`dtbo_b` | Android 的设备树覆盖层，ABL 叠到内核的设备树上 | 擦掉：不擦，ABL 会把它们叠到主线的设备树上，手机停在 fastboot（[39](39-oneplus-6t.md) §5） | `dtbo` |
| `vbmeta_a`、`vbmeta_b` | Android 验证启动（AVB）的元数据：boot、system、vendor 的哈希和哈希树的描述，带签名 | 不写。解锁的 bootloader 对不上也启动（橙色状态；`boot_a` 里装着 Nura 的那台 6T 就是这样起来的）；别的手机要关掉验证时见 §3.3；量产的手机反过来写 Kario 签名的 vbmeta（§7.1，**未实现**） | `vbmeta` |
| `system_a`、`system_b` | Android 的系统（6T 上是系统即根） | userdata 布局不碰；A/B 布局写 Kario 的根槽：ext4 加 dm-verity 的哈希树 | `root` |
| `vendor_a`、`vendor_b`、`odm` | 厂商的 HAL、库、一部分固件 | 不写、不读：Kario 自带固件包（[39](39-oneplus-6t.md) §10）；留着，回 OxygenOS 时就不用再写 | `android` |
| `userdata` | Android 的用户数据（加密的） | userdata 布局：整个是根文件系统；A/B 布局：`/var/lib`，数据和持久区 | `data` |
| `metadata` | Android 10 起元数据加密的密钥 | 6T 上没有（**未核实**） | — |
| `misc` | bootloader 消息（进 recovery、恢复出厂的命令）；有的手机的 A/B 槽元数据也在这里（展锐，§5） | 不写；6T 的槽在 GPT 属性里，不在这里 | `misc` |
| `persist` | 传感器、指纹的校准，Wi-Fi 等的出厂数据（ext4），只有这台手机有 | 绝不写；在备份里（[39](39-oneplus-6t.md) §3.5）；以后要用校准时只读拷一份（**未实现**） | `efs` |
| `modemst1`、`modemst2`、`fsg`、`fsc` | 基带的 EFS：IMEI、射频校准、运营商配置；`fsg` 是出厂的一份，`fsc` 是标记 | Kario 的代码绝不写：rmtfs 把它们交给基带，基带经它读写自己的 EFS；在备份里 | `efs` |
| `oem_dycnvbk`、`oem_stanvbk` | 一加给基带设置做的备份（从名字看，**未核实**） | 绝不写（在禁写名单里）；不在脚本的备份里，手机上的分区图核实以后再加 | `android` |
| `modem_a/b`、`dsp_a/b`、`bluetooth_a/b` | 基带、DSP、蓝牙的固件（`modem` 是 vfat，里面还有运营商的 MBN） | 不写、不读：固件来自 Kario 的固件包；VoLTE 要的 MBN 以后可能只读挂上 `modem` 去取（[39](39-oneplus-6t.md) §11.1） | `firmware` |
| `xbl`、`abl`、`tz`、`hyp`、`aop`、`keymaster`、`cmnlib`、`devcfg`、`qupfw`、`storsec` | SoC 的引导链和 TrustZone：一阶段 bootloader（`xbl` 在两个 UFS LUN 上各一份，按槽切换）、ABL（fastboot 就在里面）、安全世界 | 绝不写；Kario 不做 bootloader（[27](27-usb-typec.md) §4.1） | `firmware` |
| `devinfo`、`frp`、`keystore`、`cdt`、`ddr`、`ssd`、`param`、`op2`、`reserve*` | bootloader 的解锁状态、Android 的恢复出厂保护、板子的数据、内存的训练结果、一加自己的 | 绝不写（`devinfo`、`cdt`、`ddr` 在禁写名单里）；其余是 Android 的，留着 | `android` |
| `logdump`、`minidump`、`rawdump` | 崩溃转储（崩溃转储模式 `900e` 写的） | 不写；以后可以只读取出来查崩溃（[41](41-design-gaps.md) §13，**未实现**） | — |
| `super` | 动态分区：`system`、`vendor`、`product` 都在它里面（Android 10 起出厂的手机） | 6T 上没有；有它的手机见 §5、§7.2 | — |
| `recovery`、`cache` | 不是 A/B 的手机的恢复系统和缓存 | 6T 上没有 | — |
| `vendor_boot`、`init_boot` | GKI 的手机（Android 12、13 起出厂）把 ramdisk 和厂商的模块从 `boot` 里分出来 | 6T 上没有 | — |
| `vendor_dlkm`、`odm_dlkm`、`system_dlkm` | 内核模块按谁构建分开放：厂商的在 `vendor_dlkm`、`odm_dlkm`（Android 11 起），GKI 的在 `system_dlkm`（Android 13 起） | 6T 上没有；Kario 的模块都在根槽里，不分（§7.4） | — |
| RPMB（UFS 的一个 LUN，不是分区） | 防重放的安全存储，TEE 用 | 量产时防盗锁的记录该放在这里（**依赖硬件**，[31](31-lock-and-biometrics.md) §11.4） | — |

## 2. 板子文件里的分区表

`sku/boards/<板子>.toml` 的 `[partitions]`：`layout` 是默认的布局（`userdata` 或 `ab`），`slots` 是谁管 A/B 槽（`abl`：bootloader，记在 `boot_a`、`boot_b` 的 GPT 属性里，高通；`state`：phone-initramfs，记在状态分区里，QEMU 的磁盘），`part` 每个分区一项：名字、角色，`ab = true` 是一对 `_a`、`_b`，`size` 是核实过的字节数（可写可不写）。表里没列的分区一律不碰。6T 的（节选）：

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

角色和刷机时做什么：

| 角色 | 是什么 | userdata 布局 | A/B 布局 |
|------|------|------|------|
| `boot` | Kario 的启动镜像 | 写 `boot-<机器>.img` | 写 A/B 启动镜像（`phone-image-<机器>.rootfs.abboot`） |
| `dtbo` | Android 的设备树覆盖层 | 擦 | 擦 |
| `root` | Kario 的根文件系统，一对槽 | 不碰 | 写根槽映像（`phone-image-<机器>.rootfs.ext4.verity.sparse`） |
| `data` | Kario 的数据 | 写根文件系统（`phone-image-<机器>.rootfs.ext4.sparse`） | 擦；第一次开机 initramfs 做 ext4 |
| `state` | Kario 的持久区（有这个分区的板子） | 不碰 | 擦（`slots = "abl"`）；状态分区的出厂内容（`slots = "state"`） |
| `vbmeta`、`misc`、`firmware`、`android` | Android 的 | 不碰 | 不碰 |
| `efs` | 这台手机独有的 | 不碰；在备份里，只有 `restore-efs` 从核对过的备份写回 | 同左 |

`tools/sku.py check`（`make test` 的一部分）核对：

- `slots`、`layout`、角色都是认得的；名字是字母、数字、下划线，加上槽后缀以后不重名；一对的名字不带 `_a`、`_b`；`boot`、`dtbo`、`root`、`data`、`state`、`vbmeta`、`misc` 各至多一个；根是一对；`slots = "abl"` 要有一对 `boot`，`slots = "state"` 要有 `state`；默认的布局是这些分区放得下的（userdata 布局要 `boot` 和 `data`，A/B 布局要一对 `root` 和 `data`）。
- 禁写名单（`NEVER`）：高通、展锐、联发科手机上放本机独有数据、bootloader 状态、板子数据和固件的分区名（`persist`、`modemst1`、`modemst2`、`fsg`、`fsc`、`oem_dycnvbk`、`oem_stanvbk`、`devinfo`、`cdt`、`ddr`、`xbl`、`abl`、`tz`、`modem`、`dsp`、`bluetooth`……；展锐的 `prodnv`、`miscdata`、`l_fixnv1/2`、`l_runtimenv1/2`、`l_modem`、`uboot`、`sml`、`trustos`……；联发科的 `nvram`、`nvdata`、`protect1/2`、`proinfo`、`seccfg`、`preloader`、`lk`……），带不带槽后缀都算：这样的分区不能有要写、要擦的角色；角色是 `efs` 的必须在名单里。刷机脚本另有一份同样的名单，测试核对两份相同。
- QEMU 的板子（`slots = "state"`）：分区就是 A/B 磁盘的（`phone-ab.wks.in`），根的一对就是 RAUC 的槽（`system.conf`），改了任一边都报错。

谁从表里拿什么：

| 谁用 | 怎么拿到 |
|------|------|
| `yocto/fajita-flash.sh` | `tools/sku.py partitions oneplus-fajita [布局]`：第一行是布局和谁管槽，以后每行一个分区：名字、角色、刷机做什么（write、erase、keep、efs）、写哪个镜像 |
| phone-initramfs | `/etc/phone/partitions.conf`（`phone-sku-partitions` 包，`sku.py gen` 写；bootloader 管槽的板子装进 initramfs）：`slots=`、`root=`、`data=`、`state=`，只解析不执行；没有这个文件就是 QEMU 磁盘的名字 |
| RAUC | SKU 的 kas 覆盖里的 `PHONE_SLOT_KEEPER`、`PHONE_ROOT_SLOTS`、`PHONE_BOOT_SLOTS`：`rauc-conf` 按它们写 `system.conf`（§3.5） |
| 更新包 | 同上：bootloader 管槽时整包和增量包里都带启动槽 |
| chroot 套件 | `phone.env` 的 `KARIO_PARTITIONS`：`on-phone.sh partitions`（`facts` 里也有一节）列出手机上的每个分区、字节数和板子文件给的角色，再列出板子文件里有、手机上没有的 |

## 3. 一加 6T

### 3.1 两种布局

| 分区 | userdata 布局（默认） | A/B 布局（**未上真机**） |
|------|------|------|
| `boot_a`、`boot_b` | Kario 的启动镜像：内核接设备树、phone-initramfs（按分区名读写挂上 userdata），ramdisk 里写着 `layout=userdata` | A/B 启动镜像：内核接设备树、phone-initramfs、根的签名 verity 描述，命令行带 `phone.layout=ab` |
| `dtbo_a`、`dtbo_b` | 擦 | 擦 |
| `system_a`、`system_b` | OxygenOS 的，不碰 | Kario 的根槽：ext4 加 dm-verity 的哈希树，只读 |
| `userdata` | 根文件系统，读写，第一次开机长满整个分区 | `/var/lib`：数据和持久区，第一次开机由 initramfs 做 ext4 |
| `vbmeta`、`vendor`、`misc`、固件、EFS、`persist` | 不碰 | 不碰 |
| 系统更新 | 没有（重刷 userdata） | RAUC 写不在运行的那个槽（根槽和它的启动槽），健康检查以后标好；起不来的新槽，ABL 数完次数退回 |

两种布局用同一个刷机脚本：`fajita-flash.sh flash` 是 userdata 布局，`fajita-flash.sh --layout ab flash` 是 A/B 布局，镜像来自 `KASFILE=yocto/kas/phone-fajita-ab.yml` 的构建（[39](39-oneplus-6t.md) §4、§15）。A/B 布局在手机上试过、[39](39-oneplus-6t.md) §15 的每一项都核对过以前，默认仍是 userdata 布局。

### 3.2 分区多大

文档里没有 6T 各分区的大小（[39](39-oneplus-6t.md) §9.1 只记了名字和设备号），所以这里不写数，到手机上读：

```
fastboot getvar partition-size:system_a        # fastboot 下：十六进制的字节数；fastboot getvar all 列出全部
blockdev --getsize64 /dev/disk/by-partlabel/system_a     # 有 udev 的系统里（Nura）
for d in /sys/class/block/sd*[0-9]; do echo "$(sed -n 's/^PARTNAME=//p' $d/uevent) ${d##*/} $(( $(cat $d/size) * 512 ))"; done   # 哪个内核都行，size 按 512 字节计
```

chroot 套件的 `on-phone.sh partitions`（`facts` 里也有一节）就是第三条，再标上板子文件给的角色、列出板子文件里有而手机上没有的。刷机脚本在写之前自己读要写、要擦的每个分区（第一条），镜像放不下就停。

要放下的：根槽映像是 ext4 加哈希树，2026-11-18 的构建 ext4 是 684 MiB，哈希树约多 0.8%（每 4 KiB 一个 32 字节的哈希，加上几层上级），QEMU 给每个槽 1 GiB 留余量；A/B 启动镜像是 15.2 MiB 的启动镜像加上 initramfs（解压后约 16 MB，压缩后多大**没量**）。`system_a` 原来放 OxygenOS 的系统，`boot_a` 放 OxygenOS 的启动镜像，应该都够，**未核实**。

### 3.3 启动：ABL 选槽，initramfs 挂根

- ABL 看 `boot_a`、`boot_b` 的 GPT 属性（每项属性的第 6 个字节：优先级、活动、剩余次数、成功、不可启动），启动活动的那个槽；没标成功的每次启动扣一次（共 7 次），用完标为不可启动、换另一个槽。这是照高通公开的 ABL 源码读的，一加的 ABL 不公开，**未核实**。
- A/B 启动镜像的命令行也带 `root=`（`root=/dev/dm-0`，正是 initramfs 打开的 dm-verity 设备）：有它，一加的 ABL 不加 `skip_initramfs`、`ro init=/init` 和槽后缀，可照样追加自己的 `root=PARTUUID=<Android 的 system 分区>`（2026-11-24 在 6T 上读到的，[39](39-oneplus-6t.md) §1）；initramfs 不看 `root=`。没有 `androidboot.slot_suffix`，`phone-abslot booted` 读 GPT 里活动的那个槽（kario-bsp 的 `tests/abslot-test.py` 用那次 ABL 给的命令行测过）。
- userdata 布局（2026-11-24 起）：启动镜像的 ramdisk 前面是一个写着 `layout=userdata` 的小 cpio（`/etc/phone/layout`，phone-bootimg），后面是同一个 phone-initramfs。initramfs 按板子的 `partitions.conf` 的 `data=`（6T 是 `userdata`）在 sysfs 里按分区名找（最多等 30 秒），不跑 fsck、读写挂上 ext4，`switch_root` 到 `/sbin/init`；命令行的 `root=`、`init=`、`ro`、`skip_initramfs` 都不用，整条命令行和它的决定写进内核日志。分区上不是 ext4、没有 `/sbin/init`（还是 Android 或 Nura 的数据）时 10 秒后重启，从不格式化它。
- initramfs（`slot-init`；板子的 `partitions.conf` 写着 `slots=abl`）取这个槽的根分区（`system_<槽>`），用启动镜像里的描述验签名、经 dm-verity 只读挂上。根槽和它的启动镜像总是一起写（刷机脚本、RAUC 的父子槽），描述就是和这个内核一起构建的根文件系统的，内核也只加载用那个构建目录的密钥签名的模块（[39](39-oneplus-6t.md) §8.3），所以每个槽自己是一致的。
- 验不过、挂不上：10 秒后重启，ABL 扣这个槽的次数，用完换另一个槽；initramfs 自己不改 GPT 属性，所以 `fastboot boot` 只在内存里试一个 A/B 启动镜像时也不会写手机。
- 数据：userdata 上没有文件系统（刷机擦了，或者 `fastboot -w`）时做一个 ext4；有 ext4 就挂上；是别的文件系统（Android 的 f2fs）就不碰，`/var/lib` 只读。根槽验过以后才碰 userdata：槽里还是 Android 的系统时，`fastboot boot` 试 A/B 启动镜像不会动 userdata。
- 然后把槽的状态照 QEMU 磁盘的格式写进 `/var/lib/slots/boot`（`phone-abslot slots`：下次启动哪个槽、哪个是好的、剩几次、这次启动的是哪个），把这个槽的描述拷成 `/var/lib/slots/rootfs_<槽>.verity`：Shell 的「系统更新」和增量包的钩子照旧从那里读。
- `vbmeta`：解锁的 6T 上 Kario 不写它，Android 的 vbmeta 和 Kario 的 boot 对不上，ABL 照样启动（Nura 的安装也不碰它）。别的手机的 bootloader 不肯时，刷一个关掉验证的 vbmeta（`fastboot --disable-verity --disable-verification flash vbmeta_a <空的 vbmeta>`，用 avbtool 做）：**未实现**，要的时候在分区表里给 `vbmeta` 一个要写的角色。量产的手机正相反：写 Kario 签名的 vbmeta、锁上 bootloader（§7.1）。

### 3.4 持久区：留在 userdata 上

决定：6T 的持久区（`/var/lib/slots`：清除的请求、防盗锁的记录、RAUC 的状态、增量包的检查点、槽的状态）是 userdata 上的一个目录，不另占分区。理由：

1. 6T 的 bootloader 谁都能解锁，任何分区都能 `fastboot erase`：单独的分区只挡得住 `fastboot -w` 和重刷 userdata，挡不住认真的小偷（[31](31-lock-and-biometrics.md) §11.4 的威胁表；真要挡得靠锁住的 bootloader 和 RPMB，**依赖硬件**）。
2. 6T 的分区表里没有 Kario 能占的空分区：要么改 LUN 0 的 GPT、从 userdata 切一块出来（这台唯一的测试机、MSM 工具和回 OxygenOS 都多一层风险），要么占 Android 的分区（`frp`、`misc`、`vendor_b`……回 OxygenOS 要用它们）。
3. 启动不再依赖它：根槽的描述在启动镜像里（§3.3），userdata 清掉了只丢数据和持久区里的记录，手机照样起来、重新做数据分区。

后果：重刷 6T 的 userdata 或 `fastboot -w`，防盗锁的记录跟着没了（和现在一样，[31](31-lock-and-biometrics.md) §11.5）；Kario 自己的恢复出厂设置不碰 `/var/lib/slots`（`phone-wipe.service` 只清空 `phone`、`iwd`、`bluetooth`）。自己定分区表的板子在表里给一个 `state` 分区（16 MB，ext4，和 QEMU 的一样）：initramfs 把它挂在 `/var/lib/slots`，恢复出厂时重做整个数据分区（`wipe_format`），两条路代码里都有。

### 3.5 系统更新

照 [25](25-system-update.md) 的整套做，换了两样：槽由 ABL 管，根槽带着它的启动槽。

| RAUC 要的 | `rauc-abl`（`phone-qcom-rauc` 包）做的 |
|------|------|
| `get-primary` | `phone-abslot slots` 的 `slot`：活动且可启动的那个槽 |
| `set-primary <槽>` | `phone-abslot set-active <槽>`：照 ABL 的 `fastboot set_active`，这个槽活动、优先级 3、7 次、未成功；另一个不活动、优先级 2，成功的标记留着 |
| `get-state <槽>` | `phone-abslot get-state <槽>`：成功且可启动是 good |
| `set-state <槽> good` | `phone-abslot mark-good <槽>`（健康检查以后，`phone-ota-good`） |
| `set-state <槽> bad` | `phone-abslot mark-unbootable <槽>`（写槽之前） |
| `get-current` | initramfs 写下的 `booted`：`set-primary` 改了活动标记以后，GPT 就说不清正在运行的是哪个了 |

- 每次改完，`rauc-abl` 把槽的状态重新写进 `/var/lib/slots/boot`。
- `system.conf` 由 `system-abl.conf.in` 生成：`rootfs.0`、`rootfs.1` 是板子的根对（6T 是 `system_a`、`system_b`），`boot.0`、`boot.1` 是启动对，父槽是对应的根槽。整包和增量包里都带 A/B 启动镜像，RAUC 原样写（增量包里的启动镜像也是整个的，多大**没量**）。
- userdata 布局里 `phone-qcom-abslot.service` 一开机就标成功（标 GPT 里活动的那个槽：ABL 不给槽后缀，§3.3；它等 udev 建好 `/dev/disk/by-partlabel/boot_a` 再跑）；A/B 布局里它不起（`phone.layout=ab`），由健康检查（`phone-ota-good`）标：起不来的新槽才会被 ABL 退回。
- 和 QEMU 的不同：次数是 ABL 的 7 次（QEMU 是 3 次）；验不过时 initramfs 不能当场换槽（换槽就要换启动镜像，那是 ABL 的事），而是重启，让 ABL 扣次数。

### 3.6 留给 Android 的

- EFS（`modemst1`、`modemst2`、`fsg`、`fsc`）：rmtfs 按名字把它们交给基带，基带经它读写自己的 EFS；Kario 自己的代码和刷机脚本都不写。tqftpserv 把固件文件交给基带和 DSP，文件来自 Kario 的固件包，不读 `modem` 分区。
- `persist`：校准。Kario 现在不读（传感器、指纹没做，[39](39-oneplus-6t.md) §11）；以后只读挂上、拷一份（[41](41-design-gaps.md) §15 第 3 条）。
- 固件分区、`vendor`、`vbmeta`、`misc`、`frp`、`devinfo`：Android 的，留着；回 OxygenOS 时只写回 `boot`、`dtbo`，A/B 布局另加 `system`（[39](39-oneplus-6t.md) §9）。

## 4. 刷机脚本不写什么

- 写、擦的只是分区表里角色要写（`boot`、`root`、`data`）和要擦（`dtbo`；A/B 布局的 `userdata`）的分区。脚本的 fastboot 包装（`fb`）对每个 `flash`、`erase` 再核对一遍，表里不让改的、禁写名单里的一律拒绝；`fastboot -w` 只在 `restore` 里。
- 禁写名单在脚本里另有一份（`NEVER`，和 `tools/sku.py` 的相同，测试核对）：分区表把 `persist`、`modemst1`、`fsg`、`modem_a` 这类分区写成要写、要擦的角色时，脚本在写任何东西以前拒绝，演练也拒绝。`restore-efs` 只写表里角色是 `efs` 而且在名单里的分区，只从核对过的备份写。
- 写之前先读要写、要擦的每个分区的大小（`fastboot getvar partition-size:<名字>`）：手机上没有这个分区，或者镜像（sparse 镜像按展开后的大小算）放不下，一个字节也不写。
- 演练（`-n`）不用手机：先列出每个要写的分区、写哪个文件、多大、要和什么比，要擦的、不碰的、本机独有的分区，再列出每条 fastboot 命令。
- 两种布局刷完后、`test` 启动前、`restore` 写完后都 `fastboot --set-active=<现在的槽>`：ABL 记下的不可启动清掉（2026-11-24 那台 6T 上，连续起不来的槽被标成不可启动以后，连 `fastboot boot` 都是 `Load Error`），这个槽重新数 7 次、「成功」标记清掉，起不来时 ABL 会换到另一个槽；用现在的槽，固件的槽就不换。
- 刷机在备份目录里记下布局（`kario.txt`，不在 `SHA256SUMS` 里）：`restore` 据此知道要不要写回 `system`；A/B 布局的 `restore` 要 `--stock` 带上 OxygenOS 的 `system.img`。

## 5. 别的手机

| 机型 | 引导 | 槽谁管 | 根槽放哪 | 现状 |
|------|------|------|------|------|
| 一加 6T（骁龙 845，Android 9 出厂） | ABL，Android 启动镜像 | ABL（GPT 属性） | `system_a`、`system_b` | 写好了，**未上真机** |
| 一加 6（骁龙 845） | 同上 | 同上 | 同上 | 板子还没有 Yocto 机器和分区表，**未实现** |
| 骁龙 662 / 460 的手机（Android 10、11 出厂） | ABL | ABL | 没有物理的 `system_a`：`system` 在 `super` 里（动态分区）。要么在 `super` 里按偏移放两个槽（initramfs 用 dm-linear 或带偏移的 loop 设备取出来），要么改写 `super` 的元数据（§7.2） | **未实现**：`tools/sku.py` 现在只认一对物理分区 |
| 展锐 T610（Android 10、11） | SPL 和 U-Boot，Android 启动镜像 | U-Boot：AOSP 的 `bootloader_control` 在 `misc` 里 | 同上（`super`） | **未实现**：`slots` 只认 `abl`、`state`；展锐的 NV 分区（`prodnv`、`l_fixnv1`……）已在禁写名单里 |
| KaiOS 手机（[24](24-sku-framework.md) §7） | LK 或 lk2nd | 没有 A/B | userdata 里再分（[24](24-sku-framework.md) §7.3） | **未实现** |
| 自己的产品板 | 厂商的 bootloader | 厂商的（ABL、U-Boot）或 initramfs（`state`） | 自己定：`rootfs_a/b` 或 `system_a/b`，再加 `state`、`data`；量产时加 Kario 签名的 `vbmeta_a/b`（§7.1） | QEMU 的磁盘就是这个样子 ✓（QEMU）；vbmeta **未实现** |

## 6. 验证

| 测试 | 测什么 |
|------|------|
| `tools/sku_test.py`（`PartitionsTest`） | 6T 在两种布局里写、擦什么；分区表的每条规则各有一个写错的例子（禁写名单里的分区当数据、当根、被擦，`efs` 不在名单里，重名，带后缀的一对……）；kas、`partitions.conf`、`phone.env` 里的值；QEMU 的板子和 wic、RAUC 的名字一致；`system-abl.conf.in` 填上 6T 的名字；脚本里的禁写名单和 `sku.py` 的相同 |
| `yocto/fajita-flash-test.py` | 模拟的 6T（假的 fastboot 报分区大小）：userdata 布局照旧；A/B 布局写的、擦的正好是那几个，最后 `--set-active`；每个要改的分区在第一次写以前量过；槽太小、少一个分区、userdata 太小都在写以前拒绝；分区表把 `persist`、`modem_a`、`fsg` 列成要写或要擦的，拒绝（演练也拒绝）；演练列出要写什么；布局和镜像对不上拒绝；两种布局的 `flash`、`test`、`restore` 都把当前槽设为活动，ABL 标成不可启动、拒绝 `fastboot boot`（`Load Error`）的槽之后又起得来；启动镜像不带 initramfs（2026-11-24 以前的）、ramdisk 里没写 userdata 布局或没有 A/B 的根描述，拒绝；每个镜像按 `-S 64M`（`--chunk`）分块下载，丢掉 100M 以上一次下载的模拟手机照样写得进，`--chunk 512M` 写不进；从 A/B 布局 `restore` 要 OxygenOS 的 `system.img`；备份里缺 `boot`、`dtbo` 时提醒，`restore` 要 `--stock`，擦过的 `dtbo` 不写回 |
| kario-bsp 的 `tests/abslot-test.py` | `phone-abslot` 的 `booted`、`slots`、`set-active`、`mark-unbootable`、`get-state`、`mark-good <槽>`，在 512 和 4096 字节扇区的 GPT 磁盘镜像上：两份 GPT 都改、CRC 对、别的字节不变、演练不写、不认的参数拒绝；用 6T 的 ABL 给 Kario 的命令行（没有槽后缀），`booted`、`mark-good` 取活动的槽 |
| `tools/slots_test.py`（`SlotAbl`、`UserdataLayout`）、kario-bsp 的 `tests/rauc-abl-test.py`（`AblSlots`） | RAUC 经 `rauc-abl` 在 GPT 磁盘镜像上：装到 B、健康以后留在 B；B 从没健康过，7 次以后（照源码读的 ABL 模型）退回 A；写之前标坏的槽不启动；改了活动标记以后 `get-current` 仍是正在运行的槽。initramfs 读 `partitions.conf`（只解析不执行），判断数据分区上有没有文件系统、只在空的分区上做 ext4。userdata 布局：在 sysfs 里按分区名找数据分区（等它出来），读写挂上，另一个分区（`root=` 指的）有根文件系统也不管；不是 ext4、没有 `/sbin/init` 的不挂，从不格式化；整条命令行分段写进日志（每行不过 1024 字节） |
| `make yocto-userdata-test`（QEMU） | 4096 字节扇区的 SCSI 盘上摆着 6T 的分区名，`userdata` 里是镜像的根文件系统（加上 6T 的 `/init` 和 fstab 的 `/` 行），命令行末尾模拟 ABL 追加的 `root=`：经 initramfs 都起来、过了自测的第一个用例；不带 initramfs 时追加的 `root=` 让内核挂错分区或一直等（[39](39-oneplus-6t.md) §8.2） |
| `tools/phone_chroot_test.py` | `on-phone.sh partitions`：手机上的分区、字节数、板子文件给的角色，板子文件里有、手机上没有的 |
| `scripts/yocto-check.py` | 新的配方、类、脚本（shellcheck） |

没有测试跑过的：`slot-init` 里 A/B 的那一段整体（只查了语法和 shellcheck，它用到的几样各有测试；userdata 布局的那一段在 QEMU 上跑过）；更新包里的启动槽（没构建过；`phone-abboot.bbclass`、`phone-fajita-ab.yml`、`rauc-conf` 生成的 `system.conf` 2026-11-24 构建过，镜像没开过机）；手机上的一切。

## 7. Android 的新分区机制：Kario 要哪些

> 状态：决定了（2026-11-26，负责人问 Kario 要不要现代安卓的这几样）；四条都**未实现**，6T 上的做法（§3）不变。

| 机制 | 安卓用它做什么 | Kario |
|------|------|------|
| AVB（`vbmeta`，链式验证） | bootloader 验 vbmeta 的签名，再按里面的描述验 boot、dtbo、system……；回滚号防降级 | **要**，量产时做（§7.1） |
| 动态分区（`super`） | `system`、`vendor`、`product` 这些逻辑分区放在一个 `super` 里，大小可以互相让 | **只为兼容**：Android 10 起出厂的手机上把 Kario 的槽放进 `super`；自己的产品板不用（§7.2） |
| Virtual A/B | 每个分区只留一份，更新写进快照，新系统起来以后合并 | **以后给存储小的手机做**（§7.3） |
| `vendor_dlkm`、`system_dlkm` | 内核模块按谁构建、谁签名分进不同的分区 | **不要**（§7.4） |

### 7.1 AVB：量产时要

现在的链：启动镜像里带着根槽的签名 verity 描述和验它的 CA 证书，phone-initramfs 验描述的签名，dm-verity 守着根槽（§3.3，[25](25-system-update.md) §0）。可启动镜像本身没人验：6T 的 bootloader 解锁着（橙色状态），换一个自己的 boot（连同里面的 CA 证书）就绕过了整条链（[25](25-system-update.md) §4 的「引导链」）。产品上信任的根是厂商的 bootloader（高通的 ABL、联发科的 LK），它们讲的就是 AVB，所以 Kario 照它做，不另做一套：

- **vbmeta**：产品构建出一个用 Kario 的产品 AVB 密钥签名的 `vbmeta`，里面是启动镜像的哈希描述；板子用 `dtbo` 时还有 `dtbo` 的（6T 上擦掉了它，§1；锁着的 bootloader 在没有 `dtbo` 时肯不肯启动，**未核实**，不肯就给一个空的 dtbo 映像和它的哈希描述）。锁着时 bootloader 先验 vbmeta 的签名、再验启动镜像的哈希，对不上就不启动。
- **根槽：接在 Kario 自己的描述后面，不写成 vbmeta 里的哈希树描述**。启动镜像的哈希管住了 initramfs、CA 证书和根槽的签名描述，bootloader 验过 boot，根槽就经同一条链验过了：vbmeta → boot 的哈希 → initramfs → 根槽的描述 → dm-verity。为什么这样选：
  1. 一条路走到底：QEMU、解锁的 6T 和锁着的产品用同一个 initramfs、同一种描述，RAUC 的钩子、增量包的应用器（它核对的就是这份签名描述，[25](25-system-update.md) §5.3）、Shell 的「系统更新」都不用分两套。
  2. 哈希树描述要么由 bootloader 生成 dm-verity 的命令行（早期 AVB 的做法，各家 bootloader 不一样），要么由用户态读 vbmeta 自己建（安卓第一阶段 init 的做法）：initramfs 就要会解析 vbmeta、带上 libavb。接在自己的描述后面这些都不用。
  3. 根槽本来就和它的启动槽一起写（刷机脚本、RAUC 的父子槽，§3.3），根的 root hash 固定在启动镜像里，boot 的哈希就把它管住了。
  代价：bootloader 不知道根槽，校验出错时怎么办（AVB 的 `androidboot.veritymode`：重启还是报 I/O 错误）不归它管，由 Kario 自己定（[25](25-system-update.md) §4 的 `restart_on_corruption`）。initramfs 照旧验描述的签名：锁着的手机上多验一次，不碍事。
- **一次更新多写一个 vbmeta**：vbmeta 里有启动镜像的哈希，换了 boot 就要换这个槽的 vbmeta。RAUC 给 `vbmeta_a`、`vbmeta_b` 再加一对子槽（父槽是根槽，和启动槽一样，§3.5），整包和增量包都带上它（几 KB）。也可以让 vbmeta 给 boot 一个链式分区描述（boot 自己带签名的尾部，vbmeta 就不用跟着换），可那要多一把钥匙和一个回滚号的位置，厂商的 bootloader 认不认也要看：不选。
- **防回滚**：vbmeta 里写回滚号（`rollback_index`），bootloader 把见过的最大值记在防重放的存储里（高通是 RPMB），比它小的 vbmeta 不启动。锁住的 bootloader 本来就不让 `fastboot flash`，回滚号挡的是绕过它写进存储的旧的、签名对的镜像（EDL 刷机、直接写闪存、拿到 root 的进程写另一个槽）。AVB 建议新槽标为成功以后 bootloader 才调高存着的值，这样健康检查以前退回旧槽照样起得来（§3.5）；厂商的 bootloader 是不是这样做，**未核实**。它和 RAUC 的不降级（`min-bundle-version`，[25](25-system-update.md) §0）各管一边：一个管在线更新，一个管刷机和改存储。每个发布版都加一，还是只在修了安全问题的发布版才加，**未定**。
- **bootloader 锁上，认 Kario 的钥匙**，三种做法，越靠前越好：
  1. 自己的产品板：方案公司编 bootloader 时把 Kario 的 AVB 公钥编进去（高通的 ABL 自己由芯片的安全启动从熔丝里的 OEM 公钥哈希一路验下来），锁上以后是绿色状态。
  2. bootloader 认用户的 AVB 钥匙（`fastboot flash avb_custom_key`，Pixel 这样；[24](24-sku-framework.md) §7 的诺基亚 2780 Flip 也能刷）：刷上 Kario 的公钥再 `fastboot flashing lock`，开机显示黄色状态和钥匙的指纹。6T 的 ABL 认不认，**未核实**。
  3. 厂商的签名服务：每个发布版的 vbmeta 交给厂商用他们的钥匙签。
  锁着的手机在设备认证里该报验证过的启动状态：现在 keystored 的启动状态是模拟的（`verified-boot:simulated`，设了解锁标记时是 `unlocked`，[25](25-system-update.md) §0 的回报签名用它）；量产时要从 bootloader 给的 `androidboot.verifiedbootstate`、`androidboot.vbmeta.digest` 取（最好经 TEE），**未实现**。
- **avbtool**：AOSP 的 `external/avb` 里的 Python 脚本，`avbtool.py` 和 libavb 是 MIT 许可（这个仓库里还有 Apache-2.0、BSD 的文件，按文件看）；只在构建机上跑，不进镜像。进构建的做法：一个原生配方（`avbtool-native`）按固定的提交取 `external/avb`，只装这个脚本（要 python3、openssl）；`phone-abboot.bbclass` 做完 A/B 启动镜像后跑 `avbtool add_hash_footer`（要 boot 分区的字节数：用 AVB 的板子在分区表里必须写 `size`，§2），再 `avbtool make_vbmeta_image --include_descriptors_from_image <启动镜像> --rollback_index <号>`；产品的私钥在 HSM 里，avbtool 的 `--signing_helper` 让 HSM 签，私钥不出 HSM（和模块密钥一样，[24](24-sku-framework.md) §8.3）。分区表里 `vbmeta` 的角色要能是「写」（现在一律不碰，§2），`tools/sku.py` 和刷机脚本跟着改。都**未实现**。

### 7.2 动态分区：只为兼容

- `super` 让安卓的 `system`、`vendor`、`product`、`odm` 共用一块空间，更新时可以改各自的大小：liblp 的元数据记着每个逻辑分区由哪些区段组成，第一阶段 init 用 dm-linear 把它们拼出来；Virtual A/B 的快照也放在它的空闲处。
- Kario 自己的产品板用不着：每个槽只有一个根文件系统（Kario 自带固件，不要 `vendor`，§1），没有几个分区要互相让大小；分区表自己定，`rootfs_a/b`（或 `system_a/b`）是实在的 GPT 分区，启动路径上也不用解析 liblp（§5 的最后一行）。
- 可 Android 10 起出厂的手机都用动态分区，没有物理的 `system_a/b`：`sku/boards` 里骁龙 662 的 `sm6115-fxtec-pro1x` 和骁龙 460 的 `sm4250-oneplus-billie2`（`kario-t9-sd662-2g`、`kario-t9-sd460-2g`、`kario-qwerty-sd662-4g` 用它们）就是。在这些手机上 Kario 的两个槽只能放进 `super`，§5 列的两种做法：
  - 写 liblp 的元数据：构建时用 AOSP 的 `lpmake` 做一个整个的 `super` 映像，逻辑分区就是 Kario 的 `system_a`、`system_b`，`fastboot flash super` 一次写进去；厂商的 fastbootd、recovery 和 `lpdump` 照样读得懂。
  - 按偏移用 dm-linear：initramfs 不解析 liblp，按构建时算出的偏移和长度建 dm-linear 设备（或带偏移的 loop 设备），再在上面开 dm-verity。
  两样可以一起用：元数据照 liblp 写，initramfs 按构建时从同一份布局算出的偏移取，启动路径上没有 liblp 的解析器；偏移要是对不上（有人改过 `super`），dm-verity 的 root hash 对不上，这个槽不启动，不会挂错数据。选哪样到那块板子上再定。回 Android 要刷回原厂的 `super`。
- 要改的：`tools/sku.py` 认逻辑分区（哪些在 `super` 里、多大），`partitions.conf` 带上偏移，刷机脚本写 `super` 而不是按名字量 `system_a`，RAUC 的槽是 dm-linear 设备。这两块板子的板子文件还没有 `[partitions]`。**未实现**。

### 7.3 Virtual A/B：以后给存储小的手机做

[25](25-system-update.md) §7.1 掂量过：每个分区只留一份，省一个根槽（现在 1 GB，[25](25-system-update.md) §1），代价是快照和合并。那里原来定的是「不做」，现在改成「现在不做，存储小的手机上以后做」：

- 省下的值不值，看存储多大：1 GB 在 64 GB 的手机上不到 2%；在 8 GB 的手机上是八分之一，两个槽加起来四分之一，挤的是用户的照片和应用。低端的九宫格 SKU（`kario-t9-sd460-2g`、`kario-t9-sd662-2g`）正是会配小存储的那一档，可它们的存储还没定：SKU 文件只记了内存（`memory = "2g"`），没有存储这一项。九宫格 SKU 的存储定下来时按它决定做不做；SKU 文件也该记下存储（**未实现**）。
- 更新的时候也要空间，但只是暂时的：快照（COW）里放的是变了的块，一轮开发约 118 MB 没压缩（[25](25-system-update.md) §3：新槽 457.1 MB 里 30270 块是手机上没有的），合并完就还回去，不是一直占着的 1 GB。
- 代价（[25](25-system-update.md) §7.1）：合并完以前不能再装更新，合并完旧系统就没了（标为好以前照样能退回）；断电后要接着合并；initramfs 要先把快照拼起来（dm-snapshot，安卓在第一阶段 init 里做）再开 dm-verity，选槽和回滚的逻辑跟着改；RAUC 没有现成的 Virtual A/B，写槽要换成写快照设备。快照放哪也要定：动态分区的手机放 `super` 的空闲处（安卓的做法），别的放 userdata 上的文件；可现在 initramfs 在根槽验过以后才碰 userdata（§3.3），快照在 userdata 上就要先挂 userdata，这个次序要改。
- **未实现**。

### 7.4 dlkm 分区：不要

- 安卓把内核模块分进几个分区，是因为构建和签名的是不同的人：`system_dlkm` 里是 GKI 的模块，和 GKI 内核一起由 Google 构建、签名；`vendor_dlkm`（和 `odm_dlkm`）里是芯片厂、手机厂按内核的 KMI 自己构建的模块，靠 AVB 验过的分区（dm-verity）担保；两边各自更新。
- Kario 的内核和全部模块在同一次构建里出来，安装时用同一把钥匙签名（开发时每个构建目录一把，产品每条发布线一把、在 HSM 里，[24](24-sku-framework.md) §8.3、§8.7.6），模块在根槽的 `/lib/modules` 里，根槽和启动槽总是一起写（§3.3）：内核只加载同一次构建签了名的模块，一起发出去。分开放只是多一样要保持一致的东西。
- 什么时候再看：厂商非要用 GKI 内核、模块只给二进制（[24](24-sku-framework.md) §8.3 的最后退路），而且这些模块要按厂商自己的节奏另外更新。就算那样，厂商交来的没签名的模块也是构建时由 Kario 签（[24](24-sku-framework.md) §8.3：密钥不交给厂商），照样能放进根槽；只有模块不能跟着 Kario 的发布走时，才另给一个分区。

## 8. 还没做的

- A/B 布局的镜像只构建过一次（2026-11-24），整个**未上真机**；上手机前要查的在 [39](39-oneplus-6t.md) §15。
- 6T 的 ABL 怎么处理 GPT 属性（7 次、退回、`set_active` 清不清成功标记、换不换 `xbl` 的 LUN）是照高通公开的源码读的，**未核实**。
- 动态分区（`super`）、展锐放在 `misc` 里的槽元数据、关掉验证的空 vbmeta：**未实现**（§5、§7.2、§3.3）。
- 量产的 AVB：Kario 签名的 vbmeta、回滚号、用 Kario 的钥匙锁上的 bootloader、avbtool 的配方、认证里的启动状态：**未实现**（§7.1）。
- Virtual A/B：**未实现**，等低端九宫格 SKU 的存储定下来（§7.3）。
- 只读取 `persist` 里的校准、`logdump` 一类的崩溃转储、`modem` 分区里的 MBN：**未实现**。
- 增量包里的启动镜像是整个的，没有按块差分。

## 9. 来源

- [39](39-oneplus-6t.md) §9.1：一台 6T 上读到的 9 个分区名和设备号；§1：ABL 怎么处理命令行里的 `root=`（SHIFT6mq 公开的 ABL 源码）。
- 高通 ABL（SHIFTPHONES/android_bootable_bootloader_edk2）的 `PartitionTableUpdate.c`：A/B 属性的各位、`SetActiveSlot`、`FindBootableSlot`；高通的 boot control HAL（`gpt-utils.h`）。
- AOSP 的文档：A/B 系统更新、动态分区（`super`）、`boot_control` 的 AOSP 实现（`bootloader_control`）、AVB 的 vbmeta 和 `avbtool`、Virtual A/B、`vendor_dlkm` 和 `system_dlkm`。
- AOSP 的 `external/avb`：avbtool、libavb 和它的 README（哈希和哈希树描述、链式分区、回滚号、启动状态的颜色）；各文件的许可写在文件头里（2026-11-26 读的）。
- linux-msm 的 rmtfs、tqftpserv。
