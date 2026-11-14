# SKU 框架：一份硬件配置生成一个固件包

> 状态：**原型 ✓**（`sku/`、`tools/sku.py`、`phone-sku` 配方、`make sku`）。两块主线骁龙板（骁龙 662、骁龙 460）上的三个 SKU 都能构建出固件包，**未上真机**；QEMU 的九宫格、全键盘两个 SKU 开机自测通过（57（内部文档） 验证基线）。斐讯 N1 盒子上的真机测试是计划（§4，**未实现**）。KaiOS 手机是从引导开始替换还是对接它的底座，见 §7（调研，**未上真机**）。硬件选型见 61（内部文档），键盘与屏幕见 52（内部文档）。

## 0. 结论

- 每个 SKU 一份配置，只写「哪块主板 + 哪块屏 + 哪个键盘 + 内存档 + 地区 + 产品层」；生成器检查它们彼此对得上，展开成 Yocto 配置、设备树叠加层和运行时的设备描述；`make sku SKU=<名字>` 构建镜像并收集固件包，`sku/skus/` 里的每个 SKU 也自动是一个目标 `make sku-<名字>`（`make skus` 列出全部）。Shell、服务、应用的二进制在所有 SKU 上相同。
- 键码表只有一份（`sku/keys.toml`）：设备树的 `linux,keymap` 由它生成，测试核对 phone-paint 的 evdev 表认识其中每个键码、键码数值和内核头文件一致。
- 同一块主板的 SKU 共用内核和根文件系统，差别只在设备树叠加层、`sku.json` 和产品层；以后改成开机按板号选叠加层，一个镜像就能覆盖一个主板家族（**未实现**）。

## 1. 文件

```
sku/
  keys.toml            按键名 → Linux 键码
  boards/*.toml        主板：Yocto 机型、设备树、基带后端、有无 GPU、模块的接线（设备树标签）、通知灯和背光（[leds]、backlight：内核设备类的名字，hal/）
  displays/*.toml      屏：面板驱动的 compatible、内核配置、分辨率、DSI 通道数
  keypads/*.toml       键盘：扫描芯片、地址、行列数、矩阵位置 → 按键名、形态（九宫格 / 全键盘）、全键盘的 Fn 层（[fn]：键 → 键帽上的第二个字符）
  products/*.toml      产品层：并进配置层（profile.d）的内容
  skus/*.toml          SKU：以上各取一个，加内存档、地区，可选 rotate（竖屏横装）
```

现有的 SKU：

| SKU | 主板 | 屏 | 键盘 | 内存 |
|-----|------|----|------|------|
| `qemu-t9`（默认镜像） | QEMU | 虚拟屏 480×640 | QEMU 的 USB 键盘，九宫格形态 | 2 GB |
| `qemu-qwerty` | QEMU | 虚拟屏 640×480 | QEMU 的 USB 键盘，全键盘形态 | 4 GB |
| `qemu-t9-1g` | QEMU | 虚拟屏 480×640 | QEMU 的 USB 键盘，九宫格形态 | 1 GB（低内存压力测试，`make yocto-stress`） |
| `kario-t9-sd662-2g` | 骁龙 662（F(x)tec Pro1 X 的主线设备树） | 2.8 寸 480×640（ST7701） | 九宫格 v1（TCA8418，5×5） | 2 GB |
| `kario-t9-sd460-2g` | 骁龙 460（一加 Nord N100 的主线设备树） | 同上 | 同上 | 2 GB |
| `kario-qwerty-sd662-4g` | 骁龙 662 | 同一块屏横装（rotate 90） | 全键盘 v1（TCA8418，8×8） | 4 GB |

开发板上「模块接在哪」是评估板的接线位置（键盘的中断脚选一个空闲 GPIO），产品主板的接线来自原理图。

## 2. 生成器检查什么

`tools/sku.py check`（`make test` 的一部分）展开每个 SKU：

- QEMU 主板只配 QEMU 的屏和键盘，真主板只配真的；键盘的形态和 SKU 的形态（`bar-t9` / `bar-qwerty`）一致；屏的分辨率（横装时宽高互换）等于形态的尺寸；内存档合法。
- 键盘矩阵：每个位置在行列范围内、每个键只出现一次、键都在 `keys.toml` 里；必需的键齐全（左右软键、OK、返回、清除、通话、挂断、方向键，九宫格的 0–9 / * / #，全键盘的 26 个字母、空格、Shift）。Fn 层只给矩阵里的字母键和逗号、句号，每个只打一个字符，有 Fn 层就必须有 Fn 键。
- 屏和键盘芯片的内核驱动在 kario-qcom 机型的内核配置片段里。
- 通知灯（`[leds] notify`）和背光（`backlight`）是合法的设备类名字，多色灯的 `notify_color` 每个通道 0–255。
- 能否从挂起里叫醒（`suspend`）：真机的主板默认能，虚拟的主板（`virtual = true`，模拟器）默认不能，不能的板子从不挂起（[28](28-battery.md) §3）。
- 独立的电源键（`power_key = true`，侧键）：有的板子挂断键不兼当电源键；没有的照诺基亚的做法，挂断键就是电源键（[18](18-nokia-form-factors.md)）。
- NFC 控制器（`nfc`）：`sim`（模拟器）或 `nci:/dev/<节点>`；不写就是没有 NFC，phone-nfcd 直接退出（[30](30-nfc.md)）。
- 指纹传感器（`fingerprint`）是 `sim` 或 `none`：模拟器写 `sim`，真机在传感器有驱动之前不写（[31](31-lock-and-biometrics.md)）。
- 有内核源码时（Yocto 工作目录里的那份，或 `PHONE_KERNEL_SRC`），沿着主板 dts 的 `#include` 收集标签，核对接线引用的标签都存在——一加 Nord N100 的稳压器叫 `vreg_l9a`，骁龙 662 那块叫 `pm6125_l9a`，写错在生成阶段就报出来。
- `tools/sku_test.py` 另外核对：形态表和 `shell/device.go` 一致；phone-paint 认识每个键码；键码数值和 `/usr/include/linux/input-event-codes.h` 一致。

## 3. 生成什么

| 产物 | 内容 |
|------|------|
| `yocto/kas/generated/sku.yml` | 机型（`qemuarm64` / `kario-qcom`）、`PHONE_SKU`；QEMU 的屏幕尺寸和内存；真主板的设备树文件名。Makefile 的每个 kas 配置后面都接上它 |
| `share/phone/sku.json`（`phone-sku` 包） | SKU、主板、形态、屏、键盘、内存、`web_gpu`、基带后端、指纹传感器后端（phone-keystored 按它选）、地区、产品，全键盘另有 `fn`（Fn 层）。phone-shell 没给 `--device` 时按它选形态，握手时把 Fn 层交给渲染器，phone-paint 在按住 Fn 时按它出字（[23](23-renderer-protocol.md) §2）；网页进程的内存上限按内存档定，是否用 GPU 按 `web_gpu`；主板配了通知灯和背光时另有 `leds`（`notify`：名字、颜色）和 `backlight`，Shell 用它们慢闪提示、息屏前调暗（[11](11-platform-architecture.md) §「那 hal/ 是什么」） |
| `profile.d/20-product.json` | 产品层（关于本机里的产品名等），叠在镜像自己的配置层上 |
| 设备树叠加层（真主板） | 屏接在 DSI 上（compatible、复位脚、供电）、键盘接在 I2C 上（中断、行列数、键码表、能唤醒）；`phone-sku` 配方用 dtc 编译，`fdtoverlay` 合进主板的设备树（内核构建时带符号 `-@`），得到 `phone-sku-<SKU>.dtb` |
| 固件包 `build/sku/<SKU>/firmware/` | 内核（打包了 phone-initramfs，由它挂根文件系统，[25](25-system-update.md)）、合成后的设备树（真主板）、根文件系统、`sku.json` 和清单（各文件的 SHA-256 与大小）；先核对所有文件都在，再替换旧包 |

`kario-qcom` 机型：一个内核带两块板的设备树；内核用 qemuarm64 的同一份源码，配置从 arm64 的 defconfig 出发，加上平台需要的（Landlock、seccomp、命名空间、cgroup、nftables 连接标记、系统更新要的 dm-verity / squashfs / nbd）和 SKU 用到的模块驱动。软件包沿用 qemuarm64 的指令集调优（ARMv8.0 + CRC，A73 / A53 都能跑），直接复用共享缓存；量产时换成 A73 + A53 的调优。

全键盘 SKU 把同一块 2.8 寸竖屏横装：phone-paint 发现屏的模式正好是形态尺寸横竖互换时，按形态的横屏尺寸画，上屏时顺时针转 90°（脏区域跟着换算）。

## 4. 斐讯 N1 盒子上的真机测试（计划，**未实现**：等构建机和盒子之间的网络打通）

**为什么用它**：
- N1 是 Amlogic S905D：四核 Cortex-A53、2 GB 内存、8 GB eMMC，有 HDMI、两个 USB 2.0、千兆网口。
- 主线内核有它的设备树（`meson-gxl-s905d-phicomm-n1.dts`），HDMI 走 meson 的 DRM 驱动，硬件视频解码有 meson 的 V4L2 驱动（在 staging 里，能解哪些格式要实测）。
- A53 正是骁龙 460、662 的小核。在我们手里的设备中，它是最便宜的真 ARM 硬件。

**测不了的**：基带、手机屏（MIPI DSI）、矩阵键盘（TCA8418）、电池和功耗，以及高通特有的启动方式和用户态（ABL、QRTR）。这些要等骁龙的机器（R16）。

**要先准备的**：
- N1 刷 Armbian：社区常用 ophub 的 amlogic-s9xxx-armbian，选 s905d 的镜像。
- 接上 HDMI 显示器、USB 键盘和网线。
- root 的 `authorized_keys` 里加上构建机连开发板的专用公钥（`kario-board-test`）。
- 构建机能连到盒子的一条路：两边都装 Tailscale 或 ZeroTier，或者经一台两边都能到的机器做 SSH 反向隧道。

**第一步：不做 BSP，在 Armbian 上用我们的根文件系统**。把 Yocto 构建的 arm64 根文件系统拷到 N1：指令集调优是 ARMv8.0 + CRC，A53 能直接跑。chroot 进去，把宿主的 `/dev/dri`、`/dev/input` 和 cgroup 交给它，然后测：

| # | 测什么 | 怎么测 | 对照 |
|---|--------|--------|------|
| 1 | 文本自测 | `phone-shell --selftest`：用例全过，记下每个用例的耗时 | QEMU 上的开机自测 |
| 2 | 像素自测上真屏 | phone-paint 画到 HDMI（DRM/KMS，不用 GPU），USB 键盘当按键；三种形态各跑一遍 `--gui`，记帧耗时和 CPU 占用。屏的模式比形态尺寸大时，phone-paint 按形态尺寸画、放在屏幕正中（1080p 上 480×640 的画面在中间），看到的就是手机上的样子（单元测试 ✓，**未上真屏**） | 开发机的 `make pixel-selftest` |
| 3 | 内存 | 开机后 23 个进程的 PSS；真实 2 GB 内存下的内存紧张用例：先结束后台应用的网页，再结束后台应用 | QEMU 上 23 个进程共约 217 MB |
| 4 | 网页视图 | 用网页视图镜像的根文件系统，在 A53 上跑 WPE WebKit：页面渲染耗时、网页进程内存 | [15](15-web-vs-semantic-benchmark.md) 的实测 |
| 5 | 基准 | `yocto/qemu-bench.sh` 里的语义页面和网页的对比，在真硬件上再跑一遍 | 同上 |

**第二步：加一个 N1 的 SKU，从 U 盘启动我们自己的镜像**：
- 要加的东西：一个新机型（meson，内核打开 meson 的 DRM、eMMC、视频解码），主板 `sku/boards/` 里的 N1，屏「HDMI」，键盘「USB 键盘」。
- 启动：经 Armbian 用的 U-Boot 从 U 盘起我们的内核和 initramfs。
- 然后测：
  - 真存储上的 A/B 更新、dm-verity 和回滚：`yocto-ota-test` 那套步骤在真机上手工走一遍（[25](25-system-update.md)）；
  - 硬件视频解码：经 V4L2 的内存到内存解码器播放 H.264（R24）。

**结果记到哪**：
- 测出的数字写进验证基线；
- 发现的问题写进本节和对应的主题文档。

## 5. 还没做的

- 真机：启动方式（高通的 ABL 要 boot.img）、刷机包格式、各子系统固件（GPU、基带、音频 DSP 的签名固件要从设备厂拿）都**未实现**；固件包现在是内核、设备树、根文件系统和清单。
- 开机按板号选叠加层（一个镜像覆盖一个主板家族）**未实现**。
- 展锐 T610：主线内核里没有显示、基带、USB，要等厂商 BSP（61（内部文档） §2.1），暂无 SKU。

## 6. 拿现成的手机做真机测试（2026-11 调研）

N1 盒子只能测计算和存储；要测基带、声音、显示、电源，得用一台主线内核支持得好的手机。Nura（原 postmarketOS，2026 年 9 月改名）有现成的镜像，可以先装上它，在上面用 chroot 跑我们的根文件系统做和 §4 一样的测试，再做这台手机的 SKU。下表按 Nura 的设备页和社区的说法，**没有亲测**。

| 手机 | Nura 的支持 | 适不适合 |
|------|------------|---------|
| 小米 9 Pro 5G（crux，骁龙 855+） | 没有 | 不适合：没有移植 |
| 小米 9（cepheus，骁龙 855） | 「testing」类：主线内核只有帧缓冲显示、实体按键、USB，没有基带、声音、GPU | 不适合：要测的几样都没有 |
| 一加 6 / 6T（骁龙 845） | 「community」类，维护得最好的一档：通话、短信、移动数据、声音、GPU（freedreno）、挂起都能用 | 适合，二手便宜；推荐 |
| 小米 Poco F1（骁龙 845） | 「community」类，和一加 6 同一个平台 | 适合 |
| Pixel 3a（骁龙 670） | 「community」类 | 适合 |
| 红米 Note 4X 高通版（mido，骁龙 625 / MSM8953） | 「community」类，走通用的「Generic MSM8953」镜像（v26.06）：基带固件、Wi-Fi、蓝牙、GPU（Adreno 506）、声音（UCM）都有；已知电池和充电状态时好时坏、充电时挂起有问题，部分屏和触摸的供应商版本驱动不全 | 适合，最便宜；8 个 A53 核和骁龙 460 / 662 的小核一样，比骁龙 845 更像我们的目标机。买原装屏的「mido」，别买联发科版的红米 Note 4 |

**红米 Note 4X 的细节**（2026-11-12 查了内核源码）：我们用的主线内核（6.18）里有它的设备树 `msm8953-xiaomi-mido.dts`，屏幕却只是引导程序留下的帧缓冲（simple-framebuffer，1080×1920），没有 DSI 面板驱动——能显示，但关不了屏、调不了亮度，息屏省电在这台机上测不了；面板驱动（五家供应商）在 Nura 用的 msm8953-mainline 内核分支里。通知灯是 AW2013 的红、绿、蓝三个灯（`red:indicator` 等，驱动能自己闪），实体键只有音量上（GPIO）和电源、音量下（PMIC）。所以分三步：先装 Nura 确认通话、短信、Wi-Fi；再在 Nura 上 chroot 跑我们的根文件系统（§4 第一步的做法）；最后做这台手机的 SKU（**未实现**），内核换 msm8953-mainline 分支，SKU 框架要加「整机自带屏和触摸、键盘接 USB」这一类主板。

骁龙 845 和我们选的骁龙 662 / 460 都走主线内核 + ModemManager + QRTR（R16），telephonyd 的 ModemManager 后端、phone-paint 的 DRM、按键、挂起和电流都能在上面先测。这些手机都是触屏、没有实体键盘，测按键用 USB-C 的键盘（OTG）或者手机的音量键和电源键。测试内容和步骤记在内部待办（R46）；买手机、解锁 bootloader、刷 Nura、把手机连到构建机的网络**需要你本人**。

## 7. KaiOS 手机：从引导开始替换，还是对接系统底座（2026-11 调研）

拿到一台 KaiOS 手机，有两条路：**(a) 对接底座**：留着厂商的引导程序、内核和厂商分区，只把 Gecko 和 Gaia 换成我们的用户态，经安卓的 HAL 用硬件（Ubuntu Touch、Sailfish OS 在安卓手机上用 Halium / libhybris 就是这么做的）；**(b) 从引导开始替换**：主线内核加我们整套系统，和骁龙 662 / 460 的做法一样（R16）。下面来自公开资料和本地的主线内核源码（6.18），**没有上过 KaiOS 真机**。

**结论：走 (b)，只做已经有主线移植的机型；(a) 不做。** 按 Nura 的设备列表，KaiOS 手机现在都没有维护中的主线移植（第 4 条），所以近期不在 KaiOS 手机上做，真机测试先用 §6 的手机。理由：
1. **(a) 也省不掉刷引导分区**。Halium 要按 systemd 的要求改内核配置、重编内核、刷自己的 boot 镜像；KaiOS 手机的引导程序锁着，只认厂商签名的 boot。能写 boot 的办法（EDL、解锁、2780 Flip 的 `fastboot oem sudo`）两条路是同一个：最难的一步都要走，(a) 走完还多背一层厂商用户态。
2. **厂商内核太老，我们的系统在上面起不来**。KaiOS 2.5 的诺基亚 8110 4G 是 3.10 内核；我们镜像里的 systemd 259 要 5.4 起（它的 README 叫「最低基线」），沙箱、声音环、媒体表面用的 Landlock、cgroup 冻结器、memfd 也都比 3.10 新（§7.5）。
3. **没有现成的 Halium**。Halium 只有对应安卓 7.1、9–13 的版本，Ubuntu Touch 20.04 起的新移植要 9.0 以上；KaiOS 2.5 的底座是安卓 6。libhybris 还要先按补丁重编安卓的 bionic 和 init，KaiOS 手机没有 LineageOS 的设备树可编。
4. **主线在这几台机上有过移植，但现在没人维护**。按 Nura 的设备列表（[Devices](https://wiki.nura.eco/wiki/Devices)，2026-11-14 查）：诺基亚 8110 4G、6300 4G、8000 4G、2780 Flip 的移植都在「已归档」（archived）里，不再支持；还在支持类别里的 KaiOS 手机只有 2720 Flip，属于「downstream」，用厂商的 3.10.49 内核，功能很少；通用包 `qcom-msm8909`（骁龙 210）在「testing」类，它能不能起这几台诺基亚**未核实**。2022 年移植时按键、Wi-Fi、蓝牙、充电、屏幕、背光都能用，基带经 QRTR 和 rmtfs 起来，设备树还留在社区的 msm8916-mainline 内核里；2780 Flip 的设备树在 msm89x7-mainline 内核里（§7.2）。telephonyd 的 ModemManager 后端、phone-paint 的 DRM 和 evdev 正好接在这条路上，但在 KaiOS 手机上走 (b) 要我们自己接着做这些已归档的移植。
5. **安全更新**。(a) 的内核和 HAL 停在厂商最后一版，我们的系统更新（[25](25-system-update.md)）只能更新用户态；(b) 跟着主线和长期支持版本。

(a) 只留作退路：某一类硬件在主线上始终没有驱动时，在管它的那个服务里加一个厂商后端（[11](11-platform-architecture.md) §16 的做法），而不是把整个系统放到厂商底座上。

不管走哪条路，KaiOS 手机都只当测试机：内存都是 512 MB，比我们最小的内存档还小一半，放不下网页视图（§7.2）；分区表也没有 A/B，我们的系统更新要重新安排分区（§7.3）。

### 7.1 KaiOS 的结构

KaiOS 从 Firefox OS（B2G）分出来，分三层：Gaia 是 HTML / JS 写的界面和应用；Gecko 是网页运行时，兼做设备接口；Gonk 是底座：Linux 内核加安卓的 HAL、厂商的二进制库和 rild 这类守护进程。没有 ART，跑不了安卓应用；Gecko 自己就是「system_server」，直接调安卓的 HAL。

| 层 | KaiOS 2.5 | KaiOS 3.x | KaiOS 4.0 |
|----|-----------|-----------|-----------|
| 运行时 | Gecko 48 | Gecko 84 | Gecko 123 |
| Gonk 的安卓底座 | 安卓 6.0：厂商库和系统在同一个 system 分区 | 安卓 10：厂商库在单独的 vendor 分区（Treble） | 安卓 14 |
| 内核 | 3.10（诺基亚 8110 4G） | 4.x（**未核实**） | 安卓 14 支持的 GKI 内核是 5.10–6.1，这一代机型用哪个**未核实** |
| 基带 | rild 的套接字，Firefox OS 的做法（KaiOS 2.5 原样沿用**未核实**） | Gecko 的 `nsRilWorker` 经 HIDL 调 `android.hardware.radio@1.1::IRadio` | 安卓 13 起改调 `IRadio` 1.6（同一个文件） |
| 显示 | Firefox OS 的 `GonkDisplay`：帧缓冲或 hwcomposer（**未核实**） | `GonkDisplay`：帧缓冲（fbdev）、DRM 或 hwcomposer 加 gralloc，按机型选 | 同左 |
| 按键 | Firefox OS 起 Gecko 里就带着一份安卓的 InputReader（**未核实**） | Gecko 里带着一份安卓的 EventHub、InputReader、InputDispatcher，读 evdev | 同左 |
| 第一台机 | 诺基亚 8110 4G | 诺基亚 2760 Flip | TCL Flip 4（高通 SM4635，5G） |

所以按键这一项两条路一样：厂商内核也把键盘报成 evdev。

### 7.2 KaiOS 手机的芯片，主线做到了哪一步

| 芯片 | 机型（举例） | CPU | 主线内核（6.18，本地源码查过） | 社区移植 | 刷机的门 |
|------|-------------|-----|------------------------------|---------|---------|
| 骁龙 205（MSM8905） | 诺基亚 8110 4G、2720 Flip、800 Tough，CAT B35 | 2 × Cortex-A7，只有 32 位 | MSM8909 一族的驱动在主线里（引脚、时钟、互连、基带的 PIL、调频、温控），SoC 和整机的设备树没有 | Nura 的 nokia-argon（8110 4G，2022 年合入：按键、Wi-Fi、蓝牙、充电、屏（SPI 屏，`panel-mipi-dbi`）、背光、基带，引导用 lk2nd）现在「已归档」，不再支持；msm8916-mainline 内核（6.12）里还有 `qcom-msm8905-nokia-argon.dts`。2720 Flip（nokia-beatles）在「downstream」类，用厂商的 3.10.49 内核 | EDL：BananaHackers 收集了签名的 firehose 加载器；8110 4G 经 EDL 刷解锁包后能 `fastboot oem unlock-go` |
| 骁龙 210（MSM8909） | 诺基亚 6300 4G、8000 4G | 4 × Cortex-A7，只有 32 位 | 同上 | Nura 的 nokia-leo（6300 4G）、nokia-sparkler（8000 4G）2022 年合入，现在都「已归档」，不再支持；社区内核里还有两台的设备树；Nura 的通用包 `qcom-msm8909` 在「testing」类，能不能起它们**未核实** | EDL 加载器有（美版除外） |
| 骁龙 215（QM215，和 MSM8917 几乎一样） | 诺基亚 2780 Flip，Alcatel Go Flip 4（KaiOS 3.x） | 4 × Cortex-A53，64 位 | MSM8917 的 SoC 设备树、时钟、引脚、显示（mdp5）都在，QM215 整机的设备树没有 | Nura 原来的 nokia-weeknd 用移植者自己的 6.4-rc4 分支，内核源码没了，准备 v25.12 时从软件仓库删掉，设备列表里是「已归档」；现在 msm89x7-mainline 内核（v7.1.3，2026 年 7 月）里有 `qm215-nokia-weeknd.dts`，写上了矩阵键盘、两块 SPI 屏（`panel-mipi-dbi-spi`）、背光、基带、Wi-Fi 和蓝牙（wcnss）、声卡、PMIC 的充电和电量计、振动器，Nura 的通用包 `device-qcom-msm89x7`（arm64，「testing」类）认 `qcom/qm215-*` 的设备树；哪些真能用**未核实** | 2780 Flip（美版 TA-1420）：fastboot 下先 `fastboot oem sudo`，就能刷 `avb_custom_key`、vbmeta、boot、system，社区的教程把 lk2nd 刷进 recovery；HMD Device Kit 也能刷大部分分区。有人解锁成功，说按理能起「arm32-binder64」的安卓通用系统镜像（也就是厂商用户态是 32 位的），但他没能起到正常系统（**未核实**） |
| 展锐 SC9820E、SC7731E | JioPhone 系列、Orange Sanza 2 | — | 没有 | 没有找到 | **未核实** |
| 联发科 MT6731（另有 3G 的 MT6572） | 南亚、非洲的一些机型 | 4 × Cortex-A53（MT6731） | MT6731 没有；MT6572 只有最基本的 dtsi | 没有找到 | **未核实** |
| 高通 SM4635（KaiOS 4.0） | TCL Flip 4 | — | **未核实** | 没有找到 | **未核实** |

有过主线移植的是两族，但 Nura 现在都不维护这些机型的移植（只有 2720 Flip 有厂商内核的 downstream 移植）。MSM8905 / 8909（诺基亚的五台、CAT B35）：社区内核里有整机的设备树，2022 年就打通过基带，刷机有 EDL 这条路；它们是 32 位的 Cortex-A7，而我们的镜像现在只有 arm64（ARMv8.0 调优，§3），沙箱和渲染器的 seccomp 也只写了 aarch64 和 x86_64 的架构号，所以先要加一个 armv7 的构建（§7.4）。QM215 的诺基亚 2780 Flip 是 64 位的 A53，我们的 arm64 镜像直接能跑，用 fastboot 就能刷，但它的主线移植各部分能不能用还没人写清楚（**未核实**）。屏是 2.4–2.8 寸 240×320，正好是 `bar-t9` 形态的逻辑尺寸（480×640 按 2 倍画，`shell/device.go`），按 1 倍画就是同一个版面。

**内存是硬伤**：这四台诺基亚（8110 4G、6300 4G、8000 4G、2780 Flip）都只有 512 MB 内存、4 GB 存储。我们最小的内存档是 1 GB（`tools/sku.py` 的 `MEMORY` 只认 1g–4g）；QEMU 上开机后 23 个进程约 217 MB（§4）；网页进程的内存上限最低 384 MB，因为 WPE 打开一个普通页面就要约 250 MB（`shell/webview.go`）。按这几个数估，512 MB 的手机上放不下网页视图，只能跑语义界面的应用（估算，**未核实**）。所以它们只当测试机，测基带、按键、屏、挂起和电流；要加一个 512 MB 的内存档，在这一档上关掉网页节点（**未实现**）。

### 7.3 两条路逐项对比

| 项 | (a) 对接底座 | (b) 从引导开始替换 |
|----|-------------|------------------|
| 换掉什么 | Gecko、Gaia；boot 也要换（内核按 systemd 的要求重配，initramfs 换成我们的） | 内核、initramfs、根文件系统 |
| 留着什么 | 引导程序、各子系统的固件、厂商库（KaiOS 2.5 在 system 分区，3.x 在 vendor 分区）、安卓的 init 和 rild 这些厂商服务 | 引导程序（或在它后面链式引导 lk2nd）、各子系统的固件分区：基带、Wi-Fi 的固件开机时从手机自己的分区读 |
| 刷机的前提 | 能写 boot：EDL 加载器或解锁（2780 Flip：`fastboot oem sudo`） | 同左 |
| 内核 | 厂商的 3.10 / 4.x，停在最后一版 | 主线加这台机的设备树和少量补丁 |
| 怎么用硬件 | libhybris 把链接 bionic 的厂商库加载进我们的进程；Treble 的机型（KaiOS 3.x）上，做成独立服务的 HAL 也可以用 libgbinder 直接讲 HIDL，不经 libhybris；两种都要先起一个精简的安卓（Halium 的做法） | 主线驱动：DRM、evdev、ALSA、电源类、LED 类、QRTR |
| 基带 | KaiOS 2.5：rild 套接字上的 RIL 报文；3.x：HIDL 的 `IRadio` | QMI 经 QRTR，ModemManager 管（rmtfs 替基带读写它的分区） |
| 显示 | 帧缓冲（`/dev/graphics/fb0`）或 hwcomposer 加 gralloc | DRM/KMS；SPI 屏是 `panel-mipi-dbi` |
| 声音 | 安卓的音频 HAL（`audio.primary.*.so`）经 libhybris | ALSA；这几台机的声卡在主线上齐不齐**未核实** |
| 按键 | evdev（厂商内核也这样报） | evdev |
| 振动 | 安卓内核的 LED 类振动器，Shell 现在写的就是它（`/sys/class/leds/vibrator`，[20](20-audio.md) §4.1） | 主线上振动器是输入设备的力反馈（`FF_RUMBLE`），要加这一种 |
| 沙箱 | 缺 Landlock 和 cgroup v2 的大部分，3.10 上还缺 memfd（§7.5） | 七层全有，和 QEMU 上一样 |
| init | systemd 259 起不来（要 5.4 起），得换 init 或用老版本 | systemd 现成 |
| 安全更新 | 内核和 HAL 都没有 | 跟着主线和长期支持版本 |
| 系统更新的分区 | 厂商的分区表没有 A/B：8110 4G 用安卓的 recovery 装签名的 update.zip，2780 Flip 只有一个 boot（另有 recovery、vbmeta、dtbo）。我们的 A/B 由 initramfs 选槽，不靠引导程序（[25](25-system-update.md) §0），但要两个系统槽加状态、数据两个分区：要么重写分区表，要么把 system 当一个槽、从 userdata 里切出另一个（**未核实**）；内核留厂商的，也就没有内核更新 | 照 Nura 的做法，lk2nd 写进 boot（2780 Flip 上是 recovery），系统镜像写进 userdata，里面用 msdos 分区表再分；我们的两个槽、状态、数据分区都能放在那里，一个槽约 460–490 MB（[25](25-system-update.md) §3），4 GB 存储放得下。lk2nd 按 extlinux.conf 起内核，换内核和 initramfs 这一步还没有 A/B（**未实现**） |
| 内存 | 512 MB（§7.2），还要多养一个精简的安卓（安卓的 init、servicemanager、HAL 服务、rild），多占多少**未核实** | 512 MB，只有我们自己的进程 |
| 每台机的活 | 构建 Halium（没有现成的设备树）、调 libhybris、写 RIL 后端、渲染器的帧缓冲输出、沙箱降级 | 设备树、屏的初始化序列、SKU |

### 7.4 我们的代码各要加什么

| 部分 | (a) 对接底座 | (b) 从引导开始替换 |
|------|-------------|------------------|
| `hal/` | 灯、背光、电量在厂商内核上也是 sysfs 的设备类，大多能用；高通的厂商内核常把背光做成 LED 类的 `lcd-backlight`，`hal/backlight` 要多认这一种（**未核实**）；3.10 内核的 USB 设备模式是安卓自己的 `android_usb`，不是 configfs，`hal/usbgadget` 要另写（**未核实**）。要用 HAL 库的那几类，后端不能把厂商库加载进 Shell：起一个单独的 HAL 宿主进程（libhybris 在它里面），`hal/` 的后端是它的客户端，宿主像 `phone-usbshared` 那样降权 | 不用新接口；主板配置写上 LED、背光的名字；振动收进 `hal/`，加输入设备力反馈的后端 |
| telephonyd | 新后端，实现 `Modem` 接口（和 `SimModem`、`MmModem` 并列）：KaiOS 2.5 讲 rild 套接字（oFono 的 rilmodem、Jolla 的 ril 驱动是先例）；3.x 讲 HIDL 的 `IRadio`（libgbinder-radio，Sailfish 的 ofono-binder-plugin 是先例；[11](11-platform-architecture.md) §16 留的就是这个后端）；主板配置的 `modem` 加一个值 | 不用加，ModemManager 后端现成；要在真机上验证 QRTR、rmtfs 和基带启动（Nura 现在用 `soc-qcom-msm8916` 包里的基带部分：`msm-modem`、通话声音的 `q6voiced`；QM215 用 `soc-qcom-msm89x7-modem`） |
| 渲染器（phone-paint） | 新输出：帧缓冲（mmap `/dev/fb0`，翻页用 `FBIOPAN_DISPLAY`），或者经 HAL 宿主进程走 hwcomposer；按键照旧读 evdev | DRM dumb buffer 现成；240×320 的屏按 1 倍画（形态里加缩放为 1 的一档，**未实现**） |
| 沙箱 | 降级策略（§7.5） | 不用改；armv7 要补 `sandbox` 的 `seccomp_arm.go`（`AUDIT_ARCH_ARM`、32 位的系统调用号）、`helper_arm.go`（32 位 ARM 和 x86_64 一样还有不带 at 的路径调用，要另外挡住）和渲染器 `seccomp.rs` 的 32 位 ARM；2780 Flip 是 arm64，不用补 |
| 构建 | armv7 的用户态，加上安卓那一半（Halium） | armv7（MSM8905 / 8909）或 arm64（QM215）的 Yocto 机型；Go、Rust 都能编 armv7 |
| SKU | 「整机」类主板（屏、键盘是手机自带的，键在设备树里），`modem` 写 RIL 的后端；`MEMORY` 加 512 MB 一档；形态加按 1 倍画的一档 | 同样是「整机」类主板（§6 的红米 Note 4X 也要它），键码来自整机的设备树；内存档、形态同左 |

### 7.5 老内核上沙箱少了什么

| 机制 | 内核要求 | 我们用在哪 | 3.10（KaiOS 2.5） | 4.x（KaiOS 3.x，版本**未核实**） | 现在的代码缺了它会怎样 |
|------|---------|-----------|------------------|------------------------------|---------------------|
| systemd 259 | 5.4 起 | init、服务单元、socket 激活、fd 存储（[11](11-platform-architecture.md) §14） | 不行 | 不行 | 不支持更老的内核 |
| Landlock | 5.13 起；限制 TCP 6.7 起，限制信号和抽象套接字 6.12 起 | 应用、助手、渲染器的文件白名单（[17](17-app-sandbox.md) §3） | 没有 | 没有 | 启动器按内核的 ABI 版本加规则，没有就不加这一层，自检把对应项标「跳过」；只有自测里检查 USB 共享助手的那一步要求它 |
| seccomp | 过滤器 3.5 起；`seccomp()` 系统调用和 `TSYNC` 3.17 起；`SECCOMP_RET_KILL_PROCESS` 4.14 起 | 应用、助手、渲染器（[17](17-app-sandbox.md) §4） | 没有 `seccomp()`（除非厂商移植过，**未核实**） | 有；4.14 之前「杀进程」退化成只杀线程 | 启动器调 `seccomp()` 带 `TSYNC`，失败就报错，应用起不来；要加 `prctl(PR_SET_SECCOMP)` 的回退（启动器 exec 前锁在一个线程上，exec 后只剩它） |
| cgroup v2 | 统一层级 4.5 起；cpu 控制器 4.15；PSI 4.20；冻结器 5.2；`CLONE_INTO_CGROUP` 5.7；`cgroup.kill` 5.14 | 每个应用的内存上限、CPU 权重、冻结、低内存清理（[17](17-app-sandbox.md) §5） | 只有 v1 | 部分 | 拿不到委派的 v2 组时 Shell 不建应用组：冻结退回 SIGSTOP，没有每应用的内存上限。有 v2 组时只检查是不是统一层级、是不是委派的，不检查冻结器和 `CLONE_INTO_CGROUP` 在不在，4.x 上要按功能探测（**未实现**） |
| `/proc/meminfo` 的 `MemAvailable` | 3.14 起 | 低内存清理（[17](17-app-sandbox.md) §5.1） | 没有（除非厂商移植过，**未核实**） | 有 | 读不到时可用内存算成 0，系统一直「紧张」，会接连结束后台应用 |
| memfd 和封印 | 3.17 起 | audiod 的声音环、mediad 和 Shell 的表面（[11](11-platform-architecture.md) §14） | 没有（安卓 6 用 ashmem） | 有 | 没有回退：声音和媒体帧都传不了；换成 ashmem 或 tmpfs 文件就没有封印，应用能截短共享内存，让服务因 SIGBUS 崩溃 |
| dm-verity | 3.4 起 | 系统更新的根文件系统校验（[25](25-system-update.md)） | 有 | 有 | initramfs 自己设 verity、核对签名，和 QEMU 上一样能做；但 boot 是我们刷的，厂商引导程序不认我们的密钥，信任链从 boot 才开始，这一条 (b) 也一样 |

**降级策略（建议，未实现）**：
- 缺了就不启动第三方应用（和 Shell 找不到 `phone-sandbox` 时拒绝启动一样）：独立 uid、pid 和 mount 命名空间、no_new_privs、seccomp（含 `prctl` 的回退）、memfd 的封印。
- 可以缺、照常运行，但要如实报出来：Landlock、cgroup v2（冻结退回 SIGSTOP，没有每应用的内存上限）。「设置 › 沙箱自检」已经把缺的层标「跳过」；兼容测试报告和设备认证再写上内核版本和缺了哪几层，支付类服务可以像看到 `unlocked` 那样拒绝（[11](11-platform-architecture.md) §7）。
- systemd 的门槛降不了：要么换 init（Shell 的 fd 存储、应用作用域都靠 systemd，要重写），要么走 (b)。

### 7.6 商业上

- **KaiOS 科技公司**：系统免费给手机厂商，靠服务费（比如 FOTA）、广告（KaiAds）、应用商店和搜索挣钱（2018、2019 年公开的说法，现在是否还这样**未核实**）。两条路都不用 Gecko、Gaia 和 KaiStore，所以都不需要它的许可；(a) 用到的是 Gonk 下面的 BSP，那部分属于芯片厂和手机厂。和它合作能得到的是它在低端芯片上集成 BSP、过运营商认证的经验；它的商店、广告和我们的商店、推送是竞争关系，合作的空间**未核实**。
- **厂商的二进制**（HAL、rild、基带和 Wi-Fi 的固件）：芯片厂许可给手机厂，没有给第三方再分发的权利（UBports 的移植说明也把它们当成要从设备上取的「blob」）。两条路都只能用手机上已有的那份，镜像里不带：(b) 只在开机时从固件分区读协处理器的固件；(a) 还要把厂商的用户态库加载进我们的进程，出了问题没有源码可查。
- **社区的刷机路子**（EDL 加载器、解锁包）只适合开发和爱好者版本，不能作产品的渠道。产品走自己的硬件（61（内部文档）、R16）；KaiOS 手机的用处是便宜的测试机，和 §6 的红米 Note 4X 一样。

### 7.7 要做的（都**未实现**）

1. Nura 现在没有维护中的 KaiOS 手机移植（§7.2），§6 那种「先刷 Nura 确认通话」在 KaiOS 手机上做不了；近期的真机测试用 §6 的「community」类手机（红米 Note 4X、一加 6 / 6T）。要在 KaiOS 手机上做，得自己接着做已归档的移植，两种选一：诺基亚 2780 Flip（QM215，arm64，fastboot 就能刷，不用加 armv7，msm89x7 内核里有设备树，哪些能用**未核实**）；或者 6300 4G / 8000 4G（MSM8909，有 EDL 加载器，2022 年打通过基带，要先加 armv7）。工作量**未评估**。
2. 在这台机自己的主线内核上 chroot 跑我们的根文件系统（§4 第一步的做法；MSM8909 先加 armv7 的构建）：phone-paint 在 SPI 屏上画 240×320，telephonyd 的 ModemManager 后端打一个电话。
3. 做这台机的 SKU（「整机」类主板），经 lk2nd 起我们的内核和 initramfs。
4. 买手机、刷机、把手机连到构建机的网络**需要你本人**。

**来源**：
- KaiOS：[架构](https://developer.kaiostech.com/docs/sfp-3.0/)、[版本历史](https://en.wikipedia.org/wiki/KaiOS)、[3.x 以安卓 10 为底座](https://groups.google.com/g/bananahackers/c/sAhUnL_3o4I)（也是 2780 Flip 解锁的讨论）、[4.0](https://kaios.dev/2024/12/whats-coming-in-kaios-4.0/)、[8110 4G 的固件和引导链](https://garbsch.eu/posts/dumping-the-nokia-8110-4g-firmware/)；四台诺基亚的规格（[GSMArena](https://www.gsmarena.com/compare.php3?idPhone1=9089&idPhone2=9836&idPhone3=10576)、[6300 4G / 8000 4G 的发布](https://www.fonearena.com/blog/327241/nokia-8000-4g-nokia-6300-4g-price-specifications.html)、[2780 Flip](https://www.smartprix.com/mobiles/nokia_6300_4g_vs_nokia_2780_flip-cpd1nwjij3xe_pd19v2hm2fy.php)）；gecko-b2g 的[基带](https://github.com/kaiostech/gecko-b2g/blob/gonk/dom/system/gonk/radio/nsRilWorker.h)、[显示](https://github.com/kaiostech/gecko-b2g/tree/gonk/widget/gonk/libdisplay)、[按键](https://github.com/kaiostech/gecko-b2g/tree/gonk/widget/gonk/libui)；Firefox OS 的 [RIL](https://wiki.mozilla.org/B2G/RIL)。
- 刷机：BananaHackers 的 [EDL](https://wiki.bananahackers.net/guides/boot/edl) 和[加载器表](https://edl.bananahackers.net/)、[8110 4G 解锁](https://hmd.fandom.com/wiki/Nokia_8110_4G)、[2780 Flip 的刷机教程](https://github.com/gogogoghost/nokia-2780-flip-jailbreak-tutorial)。
- Nura 支持哪些设备：[设备列表](https://wiki.nura.eco/wiki/Devices)（2026-11-14 查：8110 4G、6300 4G、8000 4G、2780 Flip、Pro1X 在「已归档」，2720 Flip 在「downstream」，`qcom-msm8909`、`msm89x7` 的通用包在「testing」，红米 Note 4X、一加 6 / 6T、Poco F1 和通用的 `qcom-msm8953` 在「community」）。
- 主线和 Nura：[8110 4G](https://gitlab.com/postmarketOS/pmaports/-/merge_requests/3527)、[6300 4G / 8000 4G 和 MSM8909 基带](https://gitlab.com/postmarketOS/pmaports/-/merge_requests/3768)、[2780 Flip](https://gitlab.com/postmarketOS/pmaports/-/merge_requests/4257) 和[它的删除](https://git.mrlokop.dev/MRLokop/pmaports/commit/84303cbe008417776bee368a4854e48e9e376fd7)；现在的通用包 [`device-qcom-msm8909`](https://gitlab.nura.eco/nura/pmaports/-/tree/main/device/testing/device-qcom-msm8909)、[`device-qcom-msm89x7`](https://gitlab.nura.eco/nura/pmaports/-/tree/main/device/testing/device-qcom-msm89x7)，内核里[三台诺基亚的设备树](https://github.com/msm8916-mainline/linux/tree/v6.12.1-msm8916/arch/arm/boot/dts/qcom)和 [2780 Flip 的设备树](https://github.com/msm89x7-mainline/linux/blob/v7.1.3-r1/arch/arm64/boot/dts/qcom/qm215-nokia-weeknd.dts)；[MediaTek 和 KaiOS](https://www.mediatek.com/press-room/kaios-technologies-and-mediatek-collaborate-to-deliver-affordable-3g-4g-smart-feature-phones)。
- Halium 一路：[libhybris](https://github.com/libhybris/libhybris)、[Halium 的构建](https://docs.halium.org/en/latest/porting/build-sources.html)、[UBports 移植说明](https://docs.ubports.com/en/latest/porting/introduction/Intro.html)、oFono 的 [ril 驱动](https://github.com/sailfishos/ofono/blob/0f4cdba/ofono/drivers/ril/ril_subscription.conf)和 [binder 插件](https://github.com/mer-hybris/ofono-binder-plugin)。
- 内核要求：[systemd 259](https://github.com/systemd/systemd/blob/v259/README)、[Landlock](https://man7.org/linux/man-pages/man7/landlock.7.html)、[seccomp](https://man7.org/linux/man-pages/man2/seccomp.2.html)、[memfd](https://man7.org/linux/man-pages/man2/memfd_create.2.html)、[cgroup v2](https://docs.kernel.org/admin-guide/cgroup-v2.html)、[AOSP 通用内核](https://source.android.com/docs/core/architecture/kernel/android-common)。
- 商业：[developingtelecoms（2018）](https://developingtelecoms.com/telecom-technology/telecom-devices-platforms/7595-kaios-emerging-os.html)、[ITWeb（2019）](https://www.itweb.co.za/article/mwc-kaios-to-bring-more-smart-feature-phones-to-africa/KzQenMj892PvZd2r)。
