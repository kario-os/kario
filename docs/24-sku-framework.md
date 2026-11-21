# SKU 框架：一份硬件配置生成一个固件包

> 状态：**原型 ✓**（`sku/`、`tools/sku.py`、`phone-sku` 配方、`make sku`）。两块主线骁龙板（骁龙 662、骁龙 460）上的三个 SKU 都能构建出固件包，**未上真机**；QEMU 的九宫格、全键盘两个 SKU 开机自测通过（57（内部文档） 验证基线）。斐讯 N1 盒子上的真机测试是计划（§4，**未实现**）。整机类主板（屏和按键是手机自带的，键盘接 USB 或蓝牙：一加 6 / 6T、红米 Note 4X）和在 Nura 上 chroot 跑我们根文件系统的套件（`tools/phone-chroot/`）是原型：单元测试和演练（dry run）✓，**未上真机**（§6、§7.4）。KaiOS 手机是从引导开始替换还是对接它的底座，见 §7（调研，**未上真机**）；整机自带键盘的键码表（`[keymap]`，§7.4）和老内核上按探测结果降级（§7.5）：单元测试 ✓，**未上真机**。硬件选型见 61（内部文档），键盘与屏幕见 52（内部文档）。

## 0. 结论

- 每个 SKU 一份配置，只写「哪块主板 + 哪块屏 + 哪个键盘 + 内存档 + 地区 + 产品层」；生成器检查它们彼此对得上，展开成 Yocto 配置、设备树叠加层和运行时的设备描述；`make sku SKU=<名字>` 构建镜像并收集固件包，`sku/skus/` 里的每个 SKU 也自动是一个目标 `make sku-<名字>`（`make skus` 列出全部）。Shell、服务、应用的二进制在所有 SKU 上相同。
- 键码表只有一份（`sku/keys.toml`）：设备树的 `linux,keymap` 由它生成，测试核对 phone-paint 的 evdev 表认识其中每个键码、键码数值和内核头文件一致。
- 同一块主板的 SKU 共用内核和根文件系统，差别只在设备树叠加层、`sku.json` 和产品层；以后改成开机按板号选叠加层，一个镜像就能覆盖一个主板家族（**未实现**）。

## 1. 文件

```
sku/
  keys.toml            按键名 → Linux 键码
  boards/*.toml        主板：Yocto 机型、设备树、基带后端、有无 GPU、模块的接线（设备树标签）、通知灯和背光（[leds]、backlight、keyboard_backlight：内核设备类的名字，hal/）、光线传感器（light_sensor：IIO 设备的名字）、定位接收机（gnss）；整机（class = "phone"，§7.4）另有自带的屏（[display]）和键（[input]）、自带键盘的键码表（[keymap]）、设备树根节点的 compatible、要上真机确认的值（unverified）
  displays/*.toml      屏：面板驱动的 compatible、内核配置、分辨率、DSI 通道数
  keypads/*.toml       键盘：扫描芯片、地址、行列数、矩阵位置 → 按键名、形态（九宫格 / 全键盘）、全键盘的 Fn 层（[fn]：键 → 键帽上的第二个字符）、没有 Ctrl 键时什么当 Ctrl（ctrl = "fn+sym"）
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
| `oneplus-fajita-t9` | 一加 6T（整机，骁龙 845） | 自带的 6.41 寸 1080×2340 AMOLED，按 4.5 倍画 | USB / 蓝牙键盘和手机自带的键，九宫格形态 | 2 GB（手机有 6 / 8 GB） |
| `oneplus-enchilada-t9` | 一加 6（整机，骁龙 845） | 自带的 6.28 寸 1080×2280 AMOLED，4.5 倍 | 同上 | 2 GB |
| `xiaomi-mido-t9` | 红米 Note 4X 高通版（整机，骁龙 625） | 自带的 5.5 寸 1080×1920 LCD，4.5 倍 | 同上 | 2 GB |

开发板上「模块接在哪」是评估板的接线位置（键盘的中断脚选一个空闲 GPIO），产品主板的接线来自原理图。

## 2. 生成器检查什么

`tools/sku.py check`（`make test` 的一部分）展开每个 SKU：

- QEMU 主板只配 QEMU 的屏和键盘，真主板只配真的；键盘的形态和 SKU 的形态（`bar-t9` / `bar-qwerty`）一致；屏的分辨率（横装时宽高互换）等于形态的尺寸；内存档合法。
- 键盘矩阵：每个位置在行列范围内、每个键只出现一次、键都在 `keys.toml` 里；必需的键齐全（左右软键、OK、返回、清除、通话、挂断、方向键，九宫格的 0–9 / * / #，全键盘的 26 个字母、空格、Shift）。Fn 层只给矩阵里的字母键和逗号、句号，每个只打一个字符，有 Fn 层就必须有 Fn 键。`ctrl` 写成 `fn+<键>`，两个键都在矩阵里，矩阵里没有 `ctrl` 键（`ctrl` 在 `keys.toml` 里是 `KEY_LEFTCTRL`）。
- 屏和键盘芯片的内核驱动在 kario-qcom 机型的内核配置片段里。
- 通知灯（`[leds] notify`）、背光（`backlight`）、键盘背光（`keyboard_backlight`）和光线传感器（`light_sensor`）是合法的设备类名字，多色灯的 `notify_color` 每个通道 0–255。
- 能否从挂起里叫醒（`suspend`）：真机的主板默认能，虚拟的主板（`virtual = true`，模拟器）默认不能，不能的板子从不挂起（[28](28-battery.md) §3）。
- 独立的电源键（`power_key = true`，侧键）：有的板子挂断键不兼当电源键；没有的照诺基亚的做法，挂断键就是电源键（[18](18-nokia-form-factors.md)）。
- NFC 控制器（`nfc`）：`sim`（模拟器）或 `nci:/dev/<节点>`；不写就是没有 NFC，phone-nfcd 直接退出（[30](30-nfc.md)）。
- 指纹传感器（`fingerprint`）是 `sim` 或 `none`：模拟器写 `sim`，真机在传感器有驱动之前不写（[31](31-lock-and-biometrics.md)）。
- 定位接收机（`gnss`，`location/`）：`modemmanager`（经 ModemManager 读调制解调器的 GPS，两块骁龙板这样写，**未上真机**）、`nmea:/dev/<节点>[@<波特率>]`（内核 GNSS 子系统的 `/dev/gnssN` 或串口，读 NMEA 0183）、`fifo:<名字>`（Shell 运行目录里的命名管道，第一次有应用要定位时由 Shell 建；QEMU 写 `fifo:gnss.nmea`，自测往里写 NMEA，模拟器上走的也是真的解析路径；交互运行时可以自己写，如 `cat 轨迹.nmea > /run/phone/gnss.nmea`）、`sim`（模拟接收机）。不写就是没有接收机，`location.get` 返回 `this phone has no location receiver`；开发机（没有 SKU 的 id）用模拟接收机，`kario run -sku` 模拟的表里不带它（[22](22-developer-api.md) §4.7）。
- 有内核源码时（Yocto 工作目录里的那份，或 `PHONE_KERNEL_SRC`），沿着主板 dts 的 `#include` 收集标签，核对接线引用的标签都存在——一加 Nord N100 的稳压器叫 `vreg_l9a`，骁龙 662 那块叫 `pm6125_l9a`，写错在生成阶段就报出来。
- 整机类主板（`class = "phone"`，§7.4）：SKU 不写屏和键盘（是手机自己的），必须写形态（`device`）；面板去掉留黑的行（`inset`，上下各一份）以后要和形态朝向一致（横屏形态写 `rotate = 90`，有留黑的行就不能转）；`scale` 在 1–8，一个逻辑像素的物理尺寸在 0.15–0.36 mm（目标机 2.8 寸 480×640 按 2 倍是 0.178 mm；下限是 2.4 寸 240×320 按 1 倍，上限是目标机的两倍）；物理尺寸和分辨率的长宽比相差不超过 5%（毫米数写反了逃不过上一条）；有 `compatible`；`unverified` 列的每一项都是这块板子里有的值；写了键码表（`[keymap]`）的，键码在 1–767、键名在 `keys.toml` 里、形态要的键都有（§7.4），不是整机的板子不能写。板子写了 Yocto 机器（`machine`，一加 6T 的 `oneplus-fajita`，[39](39-oneplus-6t.md)）的整机，`gen` 写 `sku.json` 和 kas 覆盖（机器、SKU 名；内核自己的设备树 `dtb`，不生成覆盖层）；整机没有固件包，`package`（也就是 `make sku-<名字>`）报错，指向板子的刷机脚本（`flash`）。还没有 Yocto 机器的整机，`gen`、`package` 直接报错，指向 chroot 套件（§6.2）；`sku.py files <SKU> <目录>` 只写 `sku.json`、产品层和给套件的 `phone.env`；`kario run -sku` 模拟的表不列整机（它们是测试机，不是写应用的目标）。
- `tools/sku_test.py` 另外核对：形态表和 `shell/device.go` 一致；phone-paint 认识每个键码；键码数值和 `/usr/include/linux/input-event-codes.h` 一致；整机的规则（每条都有一个写错的例子）。`tools/phone_chroot_test.py` 演练 chroot 套件：每一步在构建机上都能打出计划，演练不改任何东西，Nura 的基带一套不在要停的里面；两个脚本 shellcheck 干净。

## 3. 生成什么

| 产物 | 内容 |
|------|------|
| `yocto/kas/generated/sku.yml` | 机型（`qemuarm64` / `kario-qcom`）、`PHONE_SKU`；QEMU 的屏幕尺寸和内存；真主板的设备树文件名。Makefile 的每个 kas 配置后面都接上它 |
| `share/phone/sku.json`（`phone-sku` 包） | SKU、主板、形态、屏、键盘、内存、`web_gpu`、基带后端、指纹传感器后端（phone-keystored 按它选）、定位接收机（`gnss`，Shell 按它选）、地区、产品，全键盘另有 `fn`（Fn 层）和 `ctrl`（什么当 Ctrl，[18](18-nokia-form-factors.md) §4.2）；整机另有 `width`、`height`（画的面积：面板减去留黑的行）和 `scale`，Shell 按它们排版（文档的列数跟着逻辑宽度）、握手时交给渲染器（§7.4）；自带键盘的整机另有 `keymap`（键码 → 键名）和 `keypad = "builtin"`，Shell 换成键码对键码的表、握手时交给渲染器（§7.4）。phone-shell 没给 `--device` 时按它选形态，握手时把 Fn 层交给渲染器，phone-paint 在按住 Fn 时按它出字（[23](23-renderer-protocol.md) §2）；网页进程的内存上限按内存档定，是否用 GPU 按 `web_gpu`；主板配了通知灯和背光时另有 `leds`（`notify`：名字、颜色）、`backlight` 和 `keyboard_backlight`，Shell 用它们慢闪提示、按设置的亮度点亮背光和键盘背光（设置 › 显示与亮度）、息屏前调暗；配了光线传感器时另有 `light_sensor`，自动亮度读它；没有背光的机器上 Shell 用模拟的背光，设置页写明（[11](11-platform-architecture.md) §「那 hal/ 是什么」） |
| `profile.d/20-product.json` | 产品层（关于本机里的产品名等），叠在镜像自己的配置层上 |
| 设备树叠加层（真主板） | 屏接在 DSI 上（compatible、复位脚、供电）、键盘接在 I2C 上（中断、行列数、键码表、能唤醒）；`phone-sku` 配方用 dtc 编译，`fdtoverlay` 合进主板的设备树（内核构建时带符号 `-@`），得到 `phone-sku-<SKU>.dtb` |
| 固件包 `build/sku/<SKU>/firmware/` | 内核（打包了 phone-initramfs，由它挂根文件系统，[25](25-system-update.md)）、合成后的设备树（真主板）、根文件系统、`sku.json` 和清单（各文件的 SHA-256 与大小）；先核对所有文件都在，再替换旧包 |

`kario-qcom` 机型：一个内核带两块板的设备树；内核用 qemuarm64 的同一份源码，配置从 arm64 的 defconfig 出发，加上平台需要的（Landlock、seccomp、命名空间、cgroup、nftables 连接标记和个人热点的 NAT、系统更新要的 dm-verity / squashfs / nbd）和 SKU 用到的模块驱动。软件包沿用 qemuarm64 的指令集调优（ARMv8.0 + CRC，A73 / A53 都能跑），直接复用共享缓存；量产时换成 A73 + A53 的调优。

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
| 3 | 内存 | 开机后 23 个进程的 PSS；真实 2 GB 内存下的内存紧张用例：先结束后台应用的网页，再结束后台应用 | QEMU 上 23 个进程共 234.0 MB，其中 44.6 MB 是量产不在手机上的模拟云端服务（57（内部文档） 验证基线） |
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

## 6. 拿现成的手机做真机测试（2026-11 调研，11-17 更新）

N1 盒子只能测计算和存储；要测基带、声音、显示、电源，得用一台主线内核支持得好的手机。Nura（原 postmarketOS，2026 年 9 月改名）有现成的镜像：先装上它，在上面 chroot 跑我们的根文件系统做和 §4 一样的测试（§6.2 的套件），以后再做这台手机的 BSP。下表按 Nura 的安装页、设备页和社区的说法，**没有亲测**。

| 手机 | Nura 的支持 | 适不适合 |
|------|------------|---------|
| 小米 9 Pro 5G（crux，骁龙 855+） | 没有 | 不适合：没有移植 |
| 小米 9（cepheus，骁龙 855） | 「testing」类：主线内核只有帧缓冲显示、实体按键、USB，没有基带、声音、GPU | 不适合：要测的几样都没有 |
| 一加 6 / 6T（骁龙 845） | 「community」类，维护得最好的一档（安装页列着两台的 v26.06 稳定版和 edge）：通话、短信、移动数据、Wi-Fi、蓝牙、声音、GPS、GPU（freedreno）、挂起都能用；基带经 QRTR、rmtfs 和 ModemManager（`soc-qcom-modem`），内核 `linux-postmarketos-qcom-sdm845`，固件 `firmware-oneplus-sdm845`，声卡配置 `alsa-ucm-conf-sdm845`；NFC 用不了 | 适合，二手便宜；选定（6T 为主，6 替补） |
| 小米 Poco F1（骁龙 845） | 「community」类，和一加 6 同一个平台 | 适合 |
| Pixel 3a（骁龙 670） | 「community」类 | 适合 |
| 红米 Note 4X 高通版（mido，骁龙 625 / MSM8953） | 「community」类，走通用的「Generic MSM8953」镜像（v26.06）：基带固件、Wi-Fi、蓝牙、GPU（Adreno 506）、声音（UCM）都有；移动数据要 IPA 2 的驱动（Nura 的 msm8953 内核里有，在 Fairphone 3 上通了），这台机上**没人确认**；已知电池和充电状态时好时坏、充电时挂起有问题，部分屏和触摸的供应商版本驱动不全；NFC 没有 | 适合，最便宜；8 个 A53 核和骁龙 460 / 662 的小核一样，比骁龙 845 更像我们的目标机；选定为低端性能的对照。买原装屏的「mido」，别买联发科版的红米 Note 4 |

**选定（R46）**：主测机一加 6T（fajita），一加 6（enchilada）是同一个 SoC 和移植，可以替换；红米 Note 4X 高通版做低端性能的对照。三台都装 Nura，都没有实体键盘：按键用蓝牙键盘（一加的主线设备树把 USB 定死在设备模式，OTG 键盘用不了，[39](39-oneplus-6t.md)；4X 的 micro-USB OTG 见下文）。骁龙 845 和我们选的骁龙 662 / 460 都走主线内核 + ModemManager + QRTR（R16），telephonyd 的 ModemManager 后端、phone-paint 的 DRM、按键、挂起和电流都能在上面先测。第一步不做 BSP：在 Nura 上 chroot 跑我们的根文件系统，用手机自己的内核、屏、基带和声音（§6.2、§6.3）。三台的整机类主板和 SKU 已经写好（§7.4），要上真机确认的值标在板子文件的 `unverified` 里。买手机、解锁、刷 Nura、开 SSH、把手机连到构建机的网络**需要你本人**（§6.2 末尾）。

### 6.1 三台手机的细节

**一加 6 / 6T**（2026-11-17 查了主线 6.18.54 的源码）：
- 屏：6 是 6.28 寸 1080×2280，6T 是 6.41 寸 1080×2340，都是三星的 DSI 命令模式 AMOLED。6.18 的 `panel-samsung-sofef00` 只认 6 的 `samsung,sofef00`（模式里写着 68 × 145 mm）；6T 的设备树写的 `samsung,s6e3fc2x01` 在 6.18 里没有驱动认（android-mainline 里 6T 的设备树已经改写成 `samsung,s6e3fc2x01-ams641rw`），Nura 的内核能驱动。所以以后做 BSP，主线 6.18 上只有 6 的屏能亮，6T 要换内核或补驱动。背光由面板驱动注册（DCS 亮度 0–1023），名字是 DSI 设备的 `ae94000.dsi.0`，系统里只有这一个背光，`hal/backlight` 自己找得到。
- 键：电源键是 PMIC 的 `pm8941_pwrkey`，音量上下是 gpio-keys「Volume keys」；另有一个霍尔传感器（SW_LID，给翻盖皮套用），没有键，渲染器不管它。一加的三段式提示键在主线设备树里只有引脚配置，没有输入设备。
- 声音：6 的扬声器功放（MAX98927）主线能驱动，6T 的（TFA9894）不能，设备树里删掉了它的链路：主线上 6T 只有听筒和耳机，Nura 的内核是否补上**未核实**。
- 通知灯：6 有 PMI8998 LPG 的 RGB 灯（多色 LED 类设备 `rgb:status`），6T 没有。
- 电池是 bq27411 电量计（第一个 Battery 类型的电源）；振动是 PMI8998 的触觉驱动，在 Nura 的内核里（输入设备的力反馈），不在主线 6.18。
- 传感器（光线、距离）在 SLPI 这颗 DSP 后面，Nura 经 hexagonrpcd 和 libssc 读，不是 IIO：`hal/sensor` 看不到，自动亮度和通话时贴脸息屏在这两台上测不了。
- NFC：芯片是 NQ330 里的 NXP PN553。2023 年第一次提交设备树时作者说能认出卡的类型、读不全内容，撤回了；sdm845-next 2025-11 的提交说明还写着「没有上游驱动，只有难看的 hack」；2026-04 设备树节点进了主线（`nxp-nci-i2c`）。这个驱动挂在内核的 NFC 子系统上（netlink，neard 那一路），没有 phone-nfcd 能打开的原始 NCI 节点（`nci:/dev/…`），所以板子不写 `nfc`；Nura 上也列为用不了。
- USB：手机不给 OTG 的外设供电，键盘要带供电的转接头（社区的说法，**未核实**），所以先用蓝牙键盘。

**红米 Note 4X**（2026-11-12、11-17 查了内核源码）：主线 6.18 里有它的设备树 `msm8953-xiaomi-mido.dts`，屏幕却只是引导程序留下的帧缓冲（simple-framebuffer，1080×1920），没有 DSI 面板驱动——能显示，但关不了屏、调不了亮度，息屏省电在主线上测不了；面板驱动（五家供应商：BOE ILI9885、EBBG R63350、NT35532、OTM1911、天马 NT35596）和 PMI8950 的背光在 Nura 用的 msm8953-mainline 内核里。通知灯是 AW2013 的红、绿、蓝三个灯（`red:indicator` 等，驱动能自己闪），实体键只有音量上（GPIO）和电源、音量下（PMIC 的 `pm8941_pwrkey`、`pm8941_resin`）。触摸在主线设备树里是 FocalTech（`edt,edt-ft5406`），有的机器是 Goodix GT917D。主线设备树把 USB 控制器定死在设备模式（没有 OTG），也没有电量计和振动器；在 msm8953 内核上，OTG 要关掉 g_serial 或者开机前就插好（社区的说法）。移动数据靠 IPA 2 的驱动（`CONFIG_QCOM_IPA2_LITE`，2024 年合进 Nura 的 msm8953 内核），4X 上没人确认。所以这台主要测 CPU、内存、渲染的耗时和通话、短信。

**来源**：
- Nura 的[安装页](https://nura.eco/install/)（2026-11-17 查：一加 6、6T 和 Generic MSM8953 在「community」类，有 v26.06 和 edge），设备包 [`device-oneplus-enchilada`](https://pkgs.nura.eco/package/main/postmarketos/aarch64/device-oneplus-enchilada)（依赖 `linux-postmarketos-qcom-sdm845`、`firmware-oneplus-sdm845`、`alsa-ucm-conf-sdm845`、`soc-qcom-modem`、`hexagonrpcd`）和 [`device-oneplus-fajita`](https://pkgs.nura.eco/package/main/postmarketos/aarch64/device-oneplus-fajita)。
- linux-mobile.com 的[设备表](https://linux-mobile.com/devices/)（一加 6、6T：声音、通话、短信、数据、相机、蓝牙、Wi-Fi、GPS 都打了勾，内存 6–8 GB）。
- NFC：2023 年的[设备树补丁和撤回的说明](https://www.spinics.net/lists/devicetree/msg582210.html)、sdm845-next 的[提交说明](https://gitlab.com/sdm845/sdm845-next/-/commit/eeb765bb1166e020a771ed712fec76b56da229ee)、2026-04 进主线的[设备树提交](https://github.com/torvalds/linux/commit/50fc93244973f5ebc97eb06857fb6bed0e3cbd39)。
- msm8953 的 IPA 2：pmaports 的[合并请求 !4764](https://gitlab.com/postmarketOS/pmaports/-/merge_requests/4764)（「net: add driver for Qualcomm IPA 2.X」「arm64: dts: qcom: msm8953: add IPA」，`CONFIG_QCOM_IPA2_LITE=m`）。
- OTG：一加 6T 的[社区讨论](https://sh.itjust.works/post/58188098)，红米 Note 4X 的 [debian-on-mido](https://github.com/calico-cat-3333/debian-on-mido)（也记着电池、充电的问题）。
- 主线 6.18.54 的源码（Yocto 工作目录里的那份）：`sdm845-oneplus-common.dtsi`、`sdm845-oneplus-enchilada.dts`、`sdm845-oneplus-fajita.dts`、`msm8953-xiaomi-mido.dts`、`panel-samsung-sofef00.c`、`pm8941-pwrkey.c`、`qcom-wled.c`。

### 6.2 chroot 套件（`tools/phone-chroot/`，原型，**未上真机**）

放在 `tools/` 而不放 `yocto/`：它不构建东西，用的是构建好的镜像，在构建机上经 SSH 指挥手机。两个 POSIX sh 脚本，shellcheck 干净：`kario-phone.sh`（构建机：`--sku` 选整机的 SKU，按步骤推镜像、在手机上起 chroot、跑测试、取回结果、复原）和 `on-phone.sh`（手机上以 root 跑，busybox 的 sh 也行：每一步的实际动作）。`--dry-run` 在构建机上把每一步在这边和手机上要做的事打出来，不连手机、不改任何东西；手机上 `on-phone.sh --dry-run <步骤>` 按手机的实际情况打出要做的事。

**镜像**：Yocto 构建的 `phone-image-<机型>.rootfs.ext4`（或 `.ext4.zst`），qemuarm64（默认）或 kario-qcom 的都行：两者指令集调优相同（ARMv8.0 + CRC，A75、A55、A53 都能跑），二进制在所有 SKU 上一样，套件只把 `sku.json` 和产品层换成整机 SKU 的（`tools/sku.py files`）。qemuarm64 的构建里本来就有 ext4（A/B 磁盘的槽）和 ext4.zst，kario-qcom 的 `IMAGE_FSTYPES` 里有 ext4，**不用加新的镜像类型**。在手机上回环挂载 ext4，而不是解开 tar：属主、扩展属性（文件能力）、设备节点原样保留，构建机不需要 root，chroot 的根还是个挂载点（应用沙箱的 pivot_root 要这个）。手机上的东西都在 `/var/lib/kario-chroot/`：镜像、挂载点、我们的状态（绑定进 chroot 的 `/var/lib/phone` 和 `/root`，镜像本身几乎不写）、每一次的结果目录。**这次改动之前构建的镜像不能用**：它的 Shell 会去抢 USB 控制器，在 `usb0` 上加丢弃一切的规则，把 Nura 的 USB 网络（也就是 SSH）断掉，也不认 `scale`；套件发现镜像里没有这次的改动就拒绝（`--force` 硬上，要经 Wi-Fi 连手机）。所以先用这棵树 `make yocto-build`。

**步骤**（`tools/phone-chroot/kario-phone.sh --sku oneplus-fajita-t9 <步骤>…`）：

| 步骤 | 做什么 |
|------|--------|
| `push` | 先核对构建机上有这台手机的备份（`~/kario-phone-backup/<板子>-<序列号>`，序列号取自内核命令行的 `androidboot.serialno`，SHA-256 对得上；没有就拒绝，`--force` 才继续，[39](39-oneplus-6t.md) §3.5）；生成 SKU 的文件，连同 `on-phone.sh` 拷到手机；镜像解压后经 SSH 写过去（手机上已经是同一个镜像就跳过，挂载着就拒绝，要约 2 GB 空间） |
| `setup` | 核对是不是这块板（设备树的 compatible）、aarch64、有回环设备；停掉会抢东西的 Nura 服务（显示管理器 tinydm、greetd、sddm、lightdm、gdm：在跑才停，记下来），结束会话里的 pipewire、pulseaudio、callaudiod、feedbackd、chatty、gnome-calls、mmsdtng；回环挂载镜像，`/dev`、`/sys`、`/run` 递归绑定并设成 slave（chroot 里卸载不会卸掉 Nura 的），新挂 `/proc`，`/tmp`、`/var/volatile` 用 tmpfs；写进 SKU 的 `sku.json`、产品层和 Nura 的 `resolv.conf`；有统一的 cgroup 层级时建 `/sys/fs/cgroup/kario`，打上 `trusted.delegate`（Shell 只接手委派的组，每个应用一个子组）；切到空闲的 8 号控制台（不然打给 Shell 的键也进了 1 号控制台的登录提示）；用 Nura 的 alsaucm 设 UCM 的 `HiFi`；记下事实（`facts`） |
| `selftest` | chroot 里跑兼容性测试（`phone-shell --cts`：自测加 CTS 用例，推送走 TLCP，和镜像的开机自测一样），超时乘 `--timeout-scale`（默认 2）；在手机上分离运行，构建机每 20 秒问一次，中途连接断了不要紧（自测的「挂起」用例在能挂起的板子上让手机真的挂起一次） |
| `pixel` | 自测由 phone-paint 无头画四遍：SKU 自己的面板（1080×2180 按 4.5 倍）和三种形态（`--device`），留下 PNG 和每遍的耗时 |
| `bench` | 语义页面和网页的对比（`bench/bench.py`，和 `yocto/qemu-bench.sh` 一样的命令，只是不关机）；要 python3，只有网页视图的镜像有（`make yocto-web-build`），默认镜像跳过并写明 |
| `collect` | 把手机上所有的结果取回 `build/phone/<SKU>/results/` |
| `all` | `push setup selftest pixel bench collect`：无人值守的那部分 |
| `shell-start`、`shell-stop` | Shell 上真屏（`--gui auto`，`PHONE_PAINT_TRACE=1` 记动画的帧耗时），键来自键盘和手机的键，Shell 自己起的 telephonyd 用 SKU 的基带后端（ModemManager）；有 elogind-inhibit 时拦下 logind 对电源键和霍尔传感器的处理；停的时候把各服务的日志一起留下 |
| `mem` | `shell-start` 60 秒以后，每个我们的进程（根在 chroot 里的、cgroup 里的、它们的子孙）的 PSS 和总数 |
| `power-start`、`power-stop` | 待机前后记电池（电量计的 `charge_now`、`energy_now`、电量百分比、挂起次数），算平均电流和功率，列出唤醒最多的源（debugfs 挂着时） |
| `power-sample[=秒]` | 每秒记一次 `current_now`（亮屏时的电流） |
| `teardown` | 结束我们的所有进程，删掉 Shell 留在 Nura 内核里的 nftables 表、USB gadget 和 networkd 文件，卸载全部挂载，删 cgroup，切回原来的控制台，启动 `setup` 停掉的服务；`--purge` 连镜像、状态、结果一起删。会话里结束掉的程序下次登录或重启时回来，声卡的混音设置重启才复原 |

**Nura 留着什么，我们的服务怎么连上**：Nura 的 D-Bus、ModemManager、rmtfs、pd-mapper、tqftpserv（基带的那一套）、NetworkManager（SSH 经 USB 网络或 Wi-Fi）、bluetoothd、sshd 都留着。我们的镜像里没有 ModemManager（telephonyd 是它的客户端，BSP 再带上），所以不能停 Nura 的：`/run` 绑定进 chroot，我们的进程就在 Nura 的系统总线上（`/run/dbus/system_bus_socket`），telephonyd 以 root 调 ModemManager。chroot 里没有 systemd，Shell 自己起服务（`--services spawn`）：交互运行时它起的 telephonyd 用镜像 SKU 的基带后端（`modemmanager`），自测和脚本照旧用模拟基带（它们拨的是模拟网络里的号码；`programs/shellcmd` 的 `privateModem`，单元测试 ✓）。Wi-Fi 归 Nura 的 NetworkManager，我们的 iwd 不起，设置里的 Wi-Fi 在 chroot 里用不了；移动数据：telephonyd 照样用接入点数据库经 ModemManager 连承载，但 chroot 里没有 systemd-networkd，Shell 不给移动网卡配地址，Nura 的 NetworkManager 可能也会自己去连基带的承载、配网卡，两边会不会抢**未核实**；蓝牙的设置连的是 Nura 的 bluetoothd（**未核实**）。Shell 发现 USB 控制器已经被别的 gadget 占着（Nura 的 USB 网络）就不碰 USB：不建自己的 gadget，不加 USB 的防火墙（`shell/usb.go`，单元测试 ✓，去掉这层测试会失败）。

**你本人要先做的**：
1. 买手机：一加 6T（或 6）；红米 Note 4X 高通版（mido，原装屏，3 GB 或 4 GB 内存都行）。
2. 解锁：一加先把 OxygenOS 升级两次（两个槽都到最新），开发者选项里打开 OEM 解锁，`fastboot oem unlock`；红米申请小米的解锁，用 Mi Unlock 解。
3. 刷 Nura 之前先备份（[39](39-oneplus-6t.md) §3.5：TWRP 只在内存里跑，拷出 IMEI 和校准数据）。刷 Nura：按[安装页](https://nura.eco/install/)下载 v26.06 的镜像（一加 6、6T 各用自己的，4X 用 Generic MSM8953），或者用 pmbootstrap 自己做；界面选「console」（没有图形界面，就不用停显示管理器），装上 SSH 服务；一加还要 `fastboot erase dtbo`。
4. 开 SSH：把构建机连开发板的公钥（`kario-board-test`）加到手机 root 的 `authorized_keys`，`PermitRootLogin prohibit-password`；或者用默认用户加 `--become doas`（doas 要免密）。
5. 连网：USB 线插构建机，Nura 的 USB 网络里手机是 `172.16.42.1`（套件的默认）；再在 Nura 里用 nmcli 连上 Wi-Fi：键盘占了 USB 口、或者量待机电流要拔掉 USB 时，经 Wi-Fi 用 `--phone root@<地址>`。
6. 键盘：蓝牙键盘在 Nura 里用 bluetoothctl 配对（之后 chroot 里的 phone-paint 直接读到它）；一加上 OTG 键盘用不了（同一份设备树，USB 只能当设备），4X 的 OTG **未核实**。
7. 构建：用这棵树 `make yocto-build`；要跑网页视图的基准再 `make yocto-web-build`。

### 6.3 测试计划和要记的数（都**未跑**）

| # | 测什么 | 怎么测 | 记什么 | 对照 |
|---|--------|--------|--------|------|
| 1 | 事实 | `setup` 里的 `facts` | 每台的 `facts.txt`：输入设备的名字和键位、DRM 的连接器和模式、背光、LED、电源、IIO、`/sys/power`、RTC 的唤醒、UDC、声卡和 UCM、基带那一套的进程、内核的 Landlock、seccomp、cgroup；逐项核对板子文件里 `unverified` 的值，确认了就从列表里删掉 | 板子文件 |
| 2 | 文本自测 | `selftest` | 通过、失败、跳过的用例数（JUnit）和总耗时；chroot 里跳过或失败的（没有 systemd 的 fd 存储和应用作用域，没有 iwd，VPN 没有 networkd）逐个列出 | QEMU 上的开机自测（约 20 分钟、180 个用例）、开发机（约 100 秒） |
| 3 | 像素自测 | `pixel` | 四遍各自的耗时和 `PHONE-GUI-FRAMES`；拿 SKU 面板那遍的 PNG 看排版（240 宽的逻辑尺寸，行更多） | 开发机的 `make pixel-selftest` |
| 4 | 真屏 | `shell-start`，用键盘走 [13](13-prototype.md) §4.1 的建议路线 | 从启动到 `PHONE-GUI-READY` 的秒数；`gui.log` 里动画的帧耗时（p50、p95、晚了的帧）；留黑的行够不够（状态栏避开刘海、软键避开圆角） | 4X 对 6T：A53 对骁龙 845 |
| 5 | 内存 | `mem` | 进程数、每个进程的 PSS、总数、`MemAvailable` | QEMU 上 23 个进程共 234.0 MB（其中模拟的云端服务 44.6 MB） |
| 6 | 打电话、发短信 | `shell-start` 以后拨出、接听、挂断、发收短信、SIM 卡 PIN、紧急号码 | 每项通不通；telephonyd 的日志里对 ModemManager 的调用和错误；通话的声音（要在 Nura 里把 UCM 切到「Voice Call」，我们的 audiod 还不会切） | `make mm-selftest` 的 35 个用例（模拟的 ModemManager） |
| 7 | 挂起和待机电流 | 拔掉 USB，`power-start`，息屏一小时以上，`power-stop`；开着 Wi-Fi 和飞行模式各一次；Nura 自己待机一次做对照 | 平均电流（mA）和功率、电量的变化、挂起次数、唤醒源；来电、闹钟能不能叫醒 | Nura 自己的待机；电池 3300 / 3700 / 4100 mAh |
| 8 | 亮屏电流 | `power-sample=60`，待机画面亮着，亮度 50% | 平均电流 | 同一台上 Nura 的控制台 |
| 9 | 基准 | 网页视图的镜像上 `bench` | 冷启动、内存、空闲时的 CPU | [15](15-web-vs-semantic-benchmark.md) 的实测 |

数字写进验证基线（57（内部文档）），发现的问题写进本节、§7.4 和对应的主题文档。

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

**内存是硬伤**：这四台诺基亚（8110 4G、6300 4G、8000 4G、2780 Flip）都只有 512 MB 内存、4 GB 存储。我们最小的内存档是 1 GB（`tools/sku.py` 的 `MEMORY` 只认 1g–4g）；QEMU 上 23 个进程共 234.0 MB，去掉模拟的云端服务约 190 MB（§4）；网页进程的内存上限最低 384 MB，因为 WPE 打开一个普通页面就要约 250 MB（`shell/webview.go`）。按这几个数估，512 MB 的手机上放不下网页视图，只能跑语义界面的应用（估算，**未核实**）。所以它们只当测试机，测基带、按键、屏、挂起和电流；要加一个 512 MB 的内存档，在这一档上关掉网页节点（**未实现**）。

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
| 振动 | 安卓内核的 LED 类振动器（`/sys/class/leds/vibrator`，[20](20-audio.md) §4.1） | 主线上振动器是输入设备的力反馈（`FF_RUMBLE`）；`hal/vibrator` 两种都认，力反馈这一种**未上真机** |
| 沙箱 | 缺 Landlock 和 cgroup v2 的大部分，3.10 上还缺 memfd（§7.5） | 七层全有，和 QEMU 上一样 |
| init | systemd 259 起不来（要 5.4 起），得换 init 或用老版本 | systemd 现成 |
| 安全更新 | 内核和 HAL 都没有 | 跟着主线和长期支持版本 |
| 系统更新的分区 | 厂商的分区表没有 A/B：8110 4G 用安卓的 recovery 装签名的 update.zip，2780 Flip 只有一个 boot（另有 recovery、vbmeta、dtbo）。我们的 A/B 由 initramfs 选槽，不靠引导程序（[25](25-system-update.md) §0），但要两个系统槽加状态、数据两个分区：要么重写分区表，要么把 system 当一个槽、从 userdata 里切出另一个（**未核实**）；内核留厂商的，也就没有内核更新 | 照 Nura 的做法，lk2nd 写进 boot（2780 Flip 上是 recovery），系统镜像写进 userdata，里面用 msdos 分区表再分；我们的两个槽、状态、数据分区都能放在那里，一个槽约 460–490 MB（[25](25-system-update.md) §3），4 GB 存储放得下。lk2nd 按 extlinux.conf 起内核，换内核和 initramfs 这一步还没有 A/B（**未实现**） |
| 内存 | 512 MB（§7.2），还要多养一个精简的安卓（安卓的 init、servicemanager、HAL 服务、rild），多占多少**未核实** | 512 MB，只有我们自己的进程 |
| 每台机的活 | 构建 Halium（没有现成的设备树）、调 libhybris、写 RIL 后端、渲染器的帧缓冲输出、沙箱降级 | 设备树、屏的初始化序列、SKU |

### 7.4 我们的代码各要加什么

| 部分 | (a) 对接底座 | (b) 从引导开始替换 |
|------|-------------|------------------|
| `hal/` | 灯、背光、电量在厂商内核上也是 sysfs 的设备类，大多能用；高通的厂商内核常把背光做成 LED 类的 `lcd-backlight`，`hal/backlight` 没有背光类设备时认它（单元测试 ✓，厂商内核上**未上真机**）；3.10 内核的 USB 设备模式是安卓自己的 `android_usb`，不是 configfs，`hal/usbgadget` 要另写（**未核实**）。要用 HAL 库的那几类，后端不能把厂商库加载进 Shell：起一个单独的 HAL 宿主进程（libhybris 在它里面），`hal/` 的后端是它的客户端，宿主像 `phone-usbshared` 那样降权 | 不用新接口；主板配置写上 LED、背光的名字；振动已收进 `hal/vibrator`（LED 类和输入设备力反馈两种） |
| telephonyd | 新后端，实现 `Modem` 接口（和 `SimModem`、`MmModem` 并列）：KaiOS 2.5 讲 rild 套接字（oFono 的 rilmodem、Jolla 的 ril 驱动是先例）；3.x 讲 HIDL 的 `IRadio`（libgbinder-radio，Sailfish 的 ofono-binder-plugin 是先例；[11](11-platform-architecture.md) §16 留的就是这个后端）；主板配置的 `modem` 加一个值 | 不用加，ModemManager 后端现成；要在真机上验证 QRTR、rmtfs 和基带启动（Nura 现在用 `soc-qcom-msm8916` 包里的基带部分：`msm-modem`、通话声音的 `q6voiced`；QM215 用 `soc-qcom-msm89x7-modem`） |
| 渲染器（phone-paint） | 新输出：帧缓冲（mmap `/dev/fb0`，翻页用 `FBIOPAN_DISPLAY`），或者经 HAL 宿主进程走 hwcomposer；按键照旧读 evdev | DRM dumb buffer 现成；240×320 的屏按 1 倍画：整机类主板写 `[display] scale = 1`（下文，单元测试 ✓，**未上真机**） |
| 沙箱 | 按探测结果降级（§7.5，单元测试 ✓，老内核上**未上真机**） | 不用改；armv7 要补 `sandbox` 的 `seccomp_arm.go`（`AUDIT_ARCH_ARM`、32 位的系统调用号）、`helper_arm.go`（32 位 ARM 和 x86_64 一样还有不带 at 的路径调用，要另外挡住）和渲染器 `seccomp.rs` 的 32 位 ARM；2780 Flip 是 arm64，不用补 |
| 构建 | armv7 的用户态，加上安卓那一半（Halium） | armv7（MSM8905 / 8909）或 arm64（QM215）的 Yocto 机型；Go、Rust 都能编 armv7 |
| SKU | 「整机」类主板（下文；KaiOS 机的键在设备树里），`modem` 写 RIL 的后端；`MEMORY` 加 512 MB 一档；按 1 倍画写 `scale = 1` | 「整机」类主板已经有了（下文，原型）；KaiOS 机的键是整机设备树里的矩阵键盘，板子文件的 `[keymap]` 把它的键码对到 Kario 的键（下文，单元测试 ✓，**未上真机**）；内存档同左 |

**整机类主板（`class = "phone"`，原型：生成器、Shell、渲染器的单元测试 ✓，**未上真机**）**：主板就是整部手机，屏、触摸屏和按键是它自己的，键盘接 USB 或蓝牙。三台的板子文件是 `sku/boards/oneplus-fajita.toml`、`oneplus-enchilada.toml`、`xiaomi-mido.toml`，SKU 是 `oneplus-fajita-t9`、`oneplus-enchilada-t9`、`xiaomi-mido-t9`（九宫格形态，2 GB 内存档，标准版）。板子的字段：

| 字段 | 意思 | 谁用 |
|------|------|------|
| `class = "phone"` | 整机：SKU 不写屏和键盘；没有接线、叠加层和 Yocto 机型 | `tools/sku.py` |
| `compatible` | 设备树根节点的 compatible | 套件核对手机是不是这块板 |
| `nura` | Nura 的设备名（`oneplus-fajita`、`qcom-msm8953`） | 文档、`phone.env` |
| `[display]` 的 `width`、`height`、`width_mm`、`height_mm` | 面板的像素和物理尺寸（毫米数来自设备树或驱动的模式） | 生成器核对长宽比和字的大小 |
| `[display]` 的 `scale` | 每个逻辑像素几个物理像素 | `sku.json` 的 `scale`：Shell 排版、渲染器画 |
| `[display]` 的 `inset` | 上下各留黑多少行（刘海、圆角） | `sku.json` 的 `height` 减去两份；phone-paint 把画面放在面板正中，留黑的行就在上下（`kms::letterbox`） |
| `[display]` 的 `id`、`name`、`panel`、`touchscreen` | 屏的名字，面板、触摸屏的 compatible | `sku.json` 的 `display`、文档、`facts` |
| `[input]` 的 `buttons` | 手机自带键的输入设备名 | `facts` 核对 |
| `[keymap]` | 自带键盘（KaiOS 机的矩阵）的键码 → `keys.toml` 的键名（下文） | `sku.json` 的 `keymap`：Shell 核对，握手时交给 phone-paint |
| `unverified` | 要上真机确认的值（字段或「表.字段」） | 生成器核对它们都存在；`facts` 指出去哪里看 |
| `modem`、`gnss`、`gpu`、`power_key`、`[leds]`、`backlight`、`light_sensor` | 和其他主板一样 | 同 §2、§3 |

三台的值（「源码」是从主线 6.18 的设备树或驱动里读的，不是在手机上看的；**待核实** 的都在 `unverified` 里）：

| | 一加 6T | 一加 6 | 红米 Note 4X |
|---|---|---|---|
| compatible | `oneplus,fajita`（源码） | `oneplus,enchilada`（源码） | `xiaomi,mido`（源码） |
| 屏 | 1080×2340，68 × 148 mm（源码：设备树里触摸屏的尺寸） | 1080×2280，68 × 145 mm（源码：驱动的模式） | 1080×1920，68 × 122 mm（按 5.5 寸算，**待核实**） |
| 缩放、留黑 | 4.5，上下各 80 行（**待核实**） | 4.5，上下各 80 行（**待核实**） | 4.5，不留 |
| 画的面积（逻辑尺寸） | 1080×2180（240 × 484.4） | 1080×2120（240 × 471.1） | 1080×1920（240 × 426.7） |
| 面板驱动 | `samsung,s6e3fc2x01`：Nura 的内核，主线 6.18 没有（**待核实**） | `samsung,sofef00`：主线有（源码） | 五家之一，Nura 的内核 |
| 触摸 | `syna,rmi4-i2c`（源码，**待核实**） | 同左 | `edt,edt-ft5406` 或 Goodix（**待核实**） |
| 键 | `pm8941_pwrkey`、`Volume keys`（源码，**待核实**） | 同左 | `pm8941_pwrkey`、`pm8941_resin`、`gpio-keys`（源码，**待核实**） |
| 通知灯 | 没有 | `rgb:status`，绿（源码，**待核实**） | `green:indicator`（**待核实**） |
| 背光、电池、振动 | 自己找（主线里叫 `ae94000.dsi.0`、bq27411；振动只在 Nura 的内核） | 同左 | 自己找（主线 6.18 都没有，靠 Nura 的内核） |
| 基带、定位 | `modemmanager`，定位经 ModemManager（**待核实**） | 同左 | 同左；移动数据**未确认** |
| NFC、指纹、光线传感器 | 不写 | 不写 | 不写 |

**缩放为什么是 4.5**：我们的版面按 480×640 按 2 倍设计，也就是 240 × 320 个逻辑像素。三台都是 1080 宽、约 400 ppi：1080 / 4.5 = 240，横向和九宫格直板一模一样——同样的列、同样的折行、文档的 32 列、主菜单的 3 列——只是竖向多了行（6T 去掉留黑后 484 行，4X 427 行，目标机 320 行）。一个逻辑像素在这几块屏上是 0.28 mm，在目标机的 2.8 寸屏上是 0.178 mm：正文（11 个逻辑像素）的字高 3.1 mm，约是目标机的 1.6 倍，接近安卓的最大字号，伸直手臂也看得清。比过的另外三种：按 2 倍放在中间（渲染器现成的做法）只占屏宽的 44%，一个逻辑像素 0.126 mm，正文 1.4 mm、最小的字 1.1 mm，太小；3 倍（安卓 xxhdpi 一类）是 360 × 780 个逻辑像素，字和目标机一样大（0.189 mm），但横向是另一个版面，测不出目标机上的排版；4 倍的线更锐，可 270 宽，同样换了版面。生成器要求一个逻辑像素在 0.15–0.36 mm（§2），2 倍在这几台上过不了。要看按目标机的像素数画的耗时（渲染器的开销和目标机一样），用 `--device bar-t9`：480×640 按 2 倍放在中间。开发机上也能看整机的样子：`phone-shell --device bar-t9:1080x2180@4.5`（`DeviceByID` 认 `@缩放`）。

**HID 键盘和手机自带的键**：phone-paint 读 evdev，映射和开发机上 `make run`、`make preview` 一样（[13](13-prototype.md) §4）：

| 键 | Kario 的键 |
|----|-----------|
| F1 或 Tab | 左软键（选项） |
| F2 | 右软键 |
| Enter（含小键盘的） | OK |
| Esc | 返回 |
| Backspace | 清除 |
| F5 | 通话（绿键） |
| F4 | 挂断、回待机（红键；手机有电源键，所以它不兼电源键） |
| F3 | 切换应用 |
| 方向键 | 方向 |
| F6 或 Ctrl+空格 | 输入法中 / 英 |
| F7 | 符号表 |
| F8、F9；F10–F12 | 音量下、上；播放暂停、上一首、下一首 |
| 数字、`*`、`#`（Shift+8、Shift+3 或小键盘） | 九宫格的键，按住 600 ms 是长按 |
| 字母、标点 | 直接打字，按住 Shift 是大写（HID 键盘没有我们全键盘的 Fn 层） |
| 鼠标 | 滚轮移动焦点，左键 OK，右键返回，中键选项 |
| 电源键（`pm8941_pwrkey`） | 短按亮屏、息屏，按住 600 ms 是电源菜单 |
| 音量上、下（gpio-keys、`pm8941_resin`） | 音量（通话中是通话音量） |
| 霍尔传感器、触摸屏 | 不用 |

**两个决定**：
- 音量键不兼作上下方向：电源键的短按要留着亮屏、息屏（待机电流就靠它），只有上下、没有 OK 和返回，什么也操作不了；通话时音量键要调听筒音量；蓝牙键盘闲一会儿就断开、一按又连上，键的意思跟着键盘在不在变，测试时更乱。交互测试接键盘，无人值守的那部分（自测、基准、待机）经 SSH，不用键。
- 触摸屏在，但不用：Kario 是键盘优先的系统，点哪是哪会盖住键盘导航的毛病，那正是要测的；phone-paint 只认键盘和鼠标，触摸屏两样都不是，不读它。

**整机自带的键盘（`[keymap]`，单元测试 ✓，**未上真机**）**：上面的映射假定键盘是 HID 键盘，手机自己只有电源键和音量键。KaiOS 一类的手机的键是整机设备树里的矩阵键盘，键码由这块板子自己定（比如左右软键报 `KEY_MENU`、`KEY_BACK`，绿键报 `KEY_SEND`，红键报 `KEY_POWER`，数字报数字行的 `KEY_1`…），phone-paint 的表不认识它们。板子文件用 `[keymap]` 写出内核报的键码（十进制）对应 `keys.toml` 里的哪个键，把这台手机键盘上的键列全，键码本来就是我们的那几个也列上：

```toml
[keymap]
139 = "soft-left"   # KEY_MENU
158 = "soft-right"  # KEY_BACK
231 = "call"        # KEY_SEND
116 = "end"         # KEY_POWER: the red key is the power key too (no power_key)
352 = "ok"          # KEY_OK
2 = "1"             # KEY_1
```

- `tools/sku.py` 核对：只有整机能写（模块键盘的键码由它的矩阵生成）；键码是 1–767（`KEY_MAX`）的十进制数；键名在 `keys.toml` 的 `[keys]` 里（为此加了 `power`，`KEY_POWER`）；形态要的键都在表里（和模块键盘一样：左右软键、OK、返回、清除、通话、挂断、方向键，九宫格另要 0–9、`*`、`#`，全键盘另要 26 个字母、空格、Shift）；`unverified` 可以标某个键码（`keymap.139`）。写了表的整机，`sku.json` 里是 `keypad = "builtin"` 和按键码排好的 `keymap`；`tools/sku_test.py` 每条规则各有一个写错的例子。
- Shell 读 `sku.json` 时再核对一遍（坏的表让 phone-shell 起不来，和不认识的形态一样），把它换成「报的键码 → 那个键在我们这里的键码」（`keys.toml` 的键码，`shell/sku.go` 的 `keyCodes`，`tools/sku_test.py` 核对两边一样），握手时放在 `device` 的 `keymap` 里交给渲染器（[23](23-renderer-protocol.md) §1）；`--device` 换形态时也带上（手机的键不跟着画法变）。单元测试：`TestBoardKeymap`、`TestKeymapHandshake`。
- phone-paint 读到每个按键先查这张表，换成我们的键码再走原来的路：九宫格的长按、软键、挂断键兼电源键都和模块键盘一样（`kms.rs` 的 `a_boards_keymap_reads_its_keys_as_ours`）。只换表里列的键码，别的照原样读；接上的 USB、蓝牙键盘也查同一张表，所以表里只写这台手机键盘的键码。输入设备只要有表里的任何一个键就当键盘读：矩阵键盘可能没有 Enter，原来只认有 Enter、电源键或音量键的设备（`input.rs` 的 `a_matrix_keypad_is_a_keyboard_by_its_keymap`）。
- phone-gui 不读这张表（**未实现**）。现在还没有哪块整机板子写了 `[keymap]`：没有 KaiOS 机的板子（§7.7），键码要从那台机的设备树里抄，再上真机核对。

**Shell 怎么用新字段**：`sku.json` 多了 `width`、`height`、`scale`（`shell.SKU`）。`SKUDevice` 用面板的尺寸和缩放替换形态自己的，文档的列数按逻辑宽度换算（`Device.WithScale`）；握手时交给渲染器，phone-paint 照着画，面板比它大就放在正中（留黑的行）。单元测试：`TestWholePhoneSKU`、`TestWholePhoneHandshake`（握手里的尺寸、主菜单 3 列、网页的面积按 4.5 倍算），`kms.rs` 的 `letterbox` 加了整机的一条。另外两处是为 chroot 改的（§6.2）：Shell 不碰别的 gadget 占着的 USB 控制器（`TestUSBLeavesAForeignGadgetAlone`），交互运行时自己起的 telephonyd 用 SKU 的基带后端（`TestPrivateTelephonyFollowsTheSKU`）。

**整机还缺的**（都**未实现**）：
- audiod 切 UCM：手机的声卡要按场景（放音、通话、耳机）设混音。一加 6T 的整机镜像里写好了（[20](20-audio.md) §10，**未上真机**），别的手机要在设备配置里给它们的 `--ucm-*`；chroot 用的 QEMU 镜像里没有 alsaucm，也不带这些参数，还是靠 Nura 的 alsaucm 先设好 `HiFi`，通话的声音要手动切「Voice Call」。
- 距离传感器（通话时贴脸息屏）：一加的在 SLPI 后面，`hal/sensor` 只有光线、只读 IIO。
- 留黑只能上下一样多：只有刘海、下面没有圆角的机器会白丢下面的行；要渲染器接受一个偏移（协议加字段）。有留黑的面板也不能转（横屏形态加 `rotate`）。
- 512 MB 内存档（§7.2）。
- 整机的 Yocto 机型和 BSP（内核、引导、固件分区）。

### 7.5 老内核上沙箱少了什么

| 机制 | 内核要求 | 我们用在哪 | 3.10（KaiOS 2.5） | 4.x（KaiOS 3.x，版本**未核实**） | 缺了它现在的代码怎么做 |
|------|---------|-----------|------------------|------------------------------|---------------------|
| systemd 259 | 5.4 起 | init、服务单元、socket 激活、fd 存储（[11](11-platform-architecture.md) §14） | 不行 | 不行 | 不支持更老的内核 |
| Landlock | 5.13 起；限制 TCP 6.7 起，限制信号和抽象套接字 6.12 起 | 应用、助手、渲染器的文件白名单（[17](17-app-sandbox.md) §3） | 没有 | 没有 | 启动器按内核的 ABI 版本加规则，没有就不加这一层，自检把对应项标「跳过」；只有自测里检查 USB 共享助手的那一步要求它。Shell 记为缺 `landlock`（编进了内核却没在 `lsm=` 里启用也一样，原因里写明）；ABI 4 以前另缺 `landlock-tcp`，6 以前缺 `landlock-scope` |
| seccomp | 过滤器 3.5 起；`seccomp()` 系统调用和 `TSYNC` 3.17 起；`SECCOMP_RET_KILL_PROCESS` 4.14 起 | 应用、助手、渲染器（[17](17-app-sandbox.md) §4） | 没有 `seccomp()`（除非厂商移植过，**未核实**） | 有；4.14 之前「杀进程」退化成只杀线程 | 启动器先调 `seccomp()` 带 `TSYNC`；内核没有这个系统调用（ENOSYS）就用 `prctl(PR_SET_SECCOMP)` 装在自己的线程上：启动器 exec 前锁在这个线程上，exec 后只剩它，应用照样在过滤器下（`TestSeccompWithoutTSync` 真的 exec 一次核对）。USB 共享助手是起来以后给自己所有的线程加过滤器，没有 `TSYNC` 做不到，就不启动。连过滤器都没有（`CONFIG_SECCOMP_FILTER` 没开）时不启动应用 |
| cgroup v2 | 统一层级 4.5 起；cpu 控制器 4.15；PSI 4.20；冻结器 5.2；`CLONE_INTO_CGROUP` 5.7；`cgroup.kill` 5.14 | 每个应用的内存上限、CPU 权重、冻结、低内存清理（[17](17-app-sandbox.md) §5） | 只有 v1 | 部分 | 开机时按功能探测。只有 v1 或混合层级、拿不到委派的组：不建应用组，冻结退回 SIGSTOP，没有每应用的内存、进程上限和 CPU 权重。有组时逐项看：没有 `cgroup.freeze` 就 SIGSTOP 应用的整个进程组；内存、进程、CPU 控制器缺哪个就没有哪一项；没有 `CLONE_INTO_CGROUP`，启动器停在管道上，Shell 把它移进组再放行；没有 `cgroup.kill` 就逐个 SIGKILL 组里的进程，直到组空；没有 `memory.pressure` 就只看可用内存 |
| `/proc/meminfo` 的 `MemAvailable` | 3.14 起 | 低内存清理（[17](17-app-sandbox.md) §5.1） | 没有（除非厂商移植过，**未核实**） | 有 | 按 MemFree + Buffers + Cached − Shmem 估算（以前算成 0，系统一直「紧张」，接连结束后台应用） |
| memfd 和封印 | 3.17 起 | audiod 的声音环、mediad 和 Shell 的表面（[11](11-platform-architecture.md) §14） | 没有（安卓 6 用 ashmem） | 有 | 没有回退：声音和媒体帧都传不了。不拿 ashmem 或 tmpfs 文件顶替：那样没有封印，应用能截短共享内存，让服务因 SIGBUS 崩溃 |
| pidfd | 5.3 起 | Go 运行时给子进程发信号、等它退出 | 没有 | 没有 | Go 自己退回按 pid 做；Shell 接管的上一个 Shell 的应用不是它的子进程，那个 pid 在应用退出以后可能被重用，有很小的窗口 |
| 命名空间（mount、pid、ipc、uts、net）、no_new_privs | 3.8 起、3.5 起 | 应用、助手、网页进程（[17](17-app-sandbox.md) §2） | 有 | 有 | 缺了就不启动应用 |
| dm-verity | 3.4 起 | 系统更新的根文件系统校验（[25](25-system-update.md)） | 有 | 有 | initramfs 自己设 verity、核对签名，和 QEMU 上一样能做；但 boot 是我们刷的，厂商引导程序不认我们的密钥，信任链从 boot 才开始，这一条 (b) 也一样 |

**降级（已实现：单元测试 ✓；老内核上**未上真机**）**：Shell 开机时探测一次（`sandbox/probe_linux.go`、`shell/cgroup.go`、`shell/kernel.go`），不按内核版本猜：Landlock 问 ABI 版本，seccomp 用不装过滤器的调用试，cgroup 看委派的组里有哪些文件，`CLONE_INTO_CGROUP` 真的在 Shell 自己的组里起一个什么也不做的 sh。结果写进 Shell 的日志（一行总的，缺的和退而求其次的每项一行：`kernel: missing …`、`kernel: fallback …`）、诊断转储（`phone-shell --dump` 的 `kernel`）和兼容测试报告（`sandbox` 字段：内核版本，`missing` 缺的保护、`fallback` 换了做法的，各带原因；报告的签名覆盖它，[22](22-developer-api.md) §11）。不说没有的保护：比如没有内存控制器时，日志里的 cgroup 一行就不写内存上限。每一项是下面三种之一：

| 处理 | 哪几项 | 为什么 |
|------|-------|-------|
| 不启动应用（和 Shell 以 root 运行却找不到 `phone-sandbox` 时一样：Shell 照常运行，打开应用时报出原因） | seccomp 过滤器、no_new_privs、应用要的命名空间（mount、pid、ipc、uts、net）；只在 Shell 以 root 运行（设备）时；网页进程和 USB 共享助手也不启动 | 这几层没有替身：没有它们，应用能调试别的进程、换掉文件系统视图、经 setuid 程序拿到特权。系统应用一样处理：它们也读不可信的输入（浏览器、短信） |
| 照常运行，如实报出来（报告的 `missing`） | `landlock`、`landlock-tcp`、`landlock-scope`、`cgroups`（没有每应用的组）、`cgroup-freezer`、`cgroup-memory`、`cgroup-pids`、`cgroup-cpu`、`memfd-seal`；开发机上另有 `uid`（Shell 不是 root，应用和它同一个用户、没有命名空间） | 少了一层，别的层还在（独立 uid、命名空间、seccomp）；用户照样用手机，认证方和支付类服务看得到少了什么，可以像看到 `unlocked` 那样拒绝 |
| 换一种做法，什么也没少，只记下来（报告的 `fallback`） | `seccomp-tsync`、`seccomp-kill-process`、`clone-into-cgroup`、`cgroup-kill`、`psi`、`pidfd`、`mem-available` | 同样的保护用老办法做到；`seccomp-tsync` 的代价是 USB 文件共享用不了（它的助手不启动） |

- 兼容测试照旧按用例和能力判 PASS / FAIL：缺了保护的设备照样可以 PASS，认证方看报告的 `sandbox.missing` 决定收不收；`phone-cts-verify` 把它们一项项列出来。没有这个字段的旧报告照样核对得过；旧版本的 `phone-cts-verify` 读新报告时丢掉这个字段，摘要对不上，核对不过，要用这一版。
- 和原来的建议不同：memfd 的封印归「如实报出来」，不归「不启动应用」：我们本来就不拿没封印的内存顶替，缺了它，声音、视频、相机和网页直接用不了，应用碰不到没封印的共享内存，不启动应用也换不来什么。
- 设备认证（应用调的 `attest`，钱包、支付用）还不带缺了哪几层（**未实现**）：签名的消息里只有启动状态（`verified-boot:…` / `unlocked`），加一项要换消息的版本，依赖方（钱包比的就是 `unlocked`）要一起改；现在带它的只有签了名的兼容测试报告。「设置」里也不显示（**未实现**），沙箱自检照旧把缺的层的检查项标「跳过」。
- systemd 的门槛降不了：要么换 init（Shell 的 fd 存储、应用作用域都靠 systemd，要重写），要么走 (b)。
- 验证：假的 cgroupfs 树（4.19、5.4、5.10、6.18 和厂商的 v1 内存控制器）、注入老内核的系统调用错误（ENOSYS、EINVAL、EOPNOTSUPP）的单元测试，每个修了的行为都先确认改之前失败（`TestFreezeWithoutFreezer`、`TestStartWithoutCloneInto`、`TestKillWithoutCgroupKill`、`TestMeminfoWithoutMemAvailable`、`TestSeccompWithoutTSync`）；开发机自己的 4.18 内核没有 Landlock、pidfd 和 `clone3`，上面跑的是真的探测。QEMU 的主线内核上什么都不缺，自测用例「内核特性」核对（QEMU 上**未验证**）。真的 4.x、5.x 厂商内核上**未上真机**。

### 7.6 商业上

- **KaiOS 科技公司**：系统免费给手机厂商，靠服务费（比如 FOTA）、广告（KaiAds）、应用商店和搜索挣钱（2018、2019 年公开的说法，现在是否还这样**未核实**）。两条路都不用 Gecko、Gaia 和 KaiStore，所以都不需要它的许可；(a) 用到的是 Gonk 下面的 BSP，那部分属于芯片厂和手机厂。和它合作能得到的是它在低端芯片上集成 BSP、过运营商认证的经验；它的商店、广告和我们的商店、推送是竞争关系，合作的空间**未核实**。
- **厂商的二进制**（HAL、rild、基带和 Wi-Fi 的固件）：芯片厂许可给手机厂，没有给第三方再分发的权利（UBports 的移植说明也把它们当成要从设备上取的「blob」）。两条路都只能用手机上已有的那份，镜像里不带：(b) 只在开机时从固件分区读协处理器的固件；(a) 还要把厂商的用户态库加载进我们的进程，出了问题没有源码可查。
- **社区的刷机路子**（EDL 加载器、解锁包）只适合开发和爱好者版本，不能作产品的渠道。产品走自己的硬件（61（内部文档）、R16）；KaiOS 手机的用处是便宜的测试机，和 §6 的红米 Note 4X 一样。

### 7.7 要做的（都**未实现**）

1. Nura 现在没有维护中的 KaiOS 手机移植（§7.2），§6 那种「先刷 Nura 确认通话」在 KaiOS 手机上做不了；近期的真机测试用 §6 的「community」类手机（红米 Note 4X、一加 6 / 6T）。要在 KaiOS 手机上做，得自己接着做已归档的移植，两种选一：诺基亚 2780 Flip（QM215，arm64，fastboot 就能刷，不用加 armv7，msm89x7 内核里有设备树，哪些能用**未核实**）；或者 6300 4G / 8000 4G（MSM8909，有 EDL 加载器，2022 年打通过基带，要先加 armv7）。工作量**未评估**。
2. 在这台机自己的主线内核上 chroot 跑我们的根文件系统（§4 第一步的做法；MSM8909 先加 armv7 的构建）：phone-paint 在 SPI 屏上画 240×320，telephonyd 的 ModemManager 后端打一个电话。
3. 做这台机的 SKU（「整机」类主板，键盘写进 `[keymap]`，§7.4），经 lk2nd 起我们的内核和 initramfs。
4. 买手机、刷机、把手机连到构建机的网络**需要你本人**。

**来源**：
- KaiOS：[架构](https://developer.kaiostech.com/docs/sfp-3.0/)、[版本历史](https://en.wikipedia.org/wiki/KaiOS)、[3.x 以安卓 10 为底座](https://groups.google.com/g/bananahackers/c/sAhUnL_3o4I)（也是 2780 Flip 解锁的讨论）、[4.0](https://kaios.dev/2024/12/whats-coming-in-kaios-4.0/)、[8110 4G 的固件和引导链](https://garbsch.eu/posts/dumping-the-nokia-8110-4g-firmware/)；四台诺基亚的规格（[GSMArena](https://www.gsmarena.com/compare.php3?idPhone1=9089&idPhone2=9836&idPhone3=10576)、[6300 4G / 8000 4G 的发布](https://www.fonearena.com/blog/327241/nokia-8000-4g-nokia-6300-4g-price-specifications.html)、[2780 Flip](https://www.smartprix.com/mobiles/nokia_6300_4g_vs_nokia_2780_flip-cpd1nwjij3xe_pd19v2hm2fy.php)）；gecko-b2g 的[基带](https://github.com/kaiostech/gecko-b2g/blob/gonk/dom/system/gonk/radio/nsRilWorker.h)、[显示](https://github.com/kaiostech/gecko-b2g/tree/gonk/widget/gonk/libdisplay)、[按键](https://github.com/kaiostech/gecko-b2g/tree/gonk/widget/gonk/libui)；Firefox OS 的 [RIL](https://wiki.mozilla.org/B2G/RIL)。
- 刷机：BananaHackers 的 [EDL](https://wiki.bananahackers.net/guides/boot/edl) 和[加载器表](https://edl.bananahackers.net/)、[8110 4G 解锁](https://hmd.fandom.com/wiki/Nokia_8110_4G)、[2780 Flip 的刷机教程](https://github.com/gogogoghost/nokia-2780-flip-jailbreak-tutorial)。
- Nura 支持哪些设备：[设备列表](https://wiki.nura.eco/wiki/Devices)（2026-11-14 查：8110 4G、6300 4G、8000 4G、2780 Flip、Pro1X 在「已归档」，2720 Flip 在「downstream」，`qcom-msm8909`、`msm89x7` 的通用包在「testing」，红米 Note 4X、一加 6 / 6T、Poco F1 和通用的 `qcom-msm8953` 在「community」）。
- 主线和 Nura：[8110 4G](https://gitlab.com/postmarketOS/pmaports/-/merge_requests/3527)、[6300 4G / 8000 4G 和 MSM8909 基带](https://gitlab.com/postmarketOS/pmaports/-/merge_requests/3768)、[2780 Flip](https://gitlab.com/postmarketOS/pmaports/-/merge_requests/4257) 和[它的删除](https://git.mrlokop.dev/MRLokop/pmaports/commit/84303cbe008417776bee368a4854e48e9e376fd7)；现在的通用包 [`device-qcom-msm8909`](https://gitlab.nura.eco/nura/pmaports/-/tree/main/device/testing/device-qcom-msm8909)、[`device-qcom-msm89x7`](https://gitlab.nura.eco/nura/pmaports/-/tree/main/device/testing/device-qcom-msm89x7)，内核里[三台诺基亚的设备树](https://github.com/msm8916-mainline/linux/tree/v6.12.1-msm8916/arch/arm/boot/dts/qcom)和 [2780 Flip 的设备树](https://github.com/msm89x7-mainline/linux/blob/v7.1.3-r1/arch/arm64/boot/dts/qcom/qm215-nokia-weeknd.dts)；[MediaTek 和 KaiOS](https://www.mediatek.com/press-room/kaios-technologies-and-mediatek-collaborate-to-deliver-affordable-3g-4g-smart-feature-phones)。
- Halium 一路：[libhybris](https://github.com/libhybris/libhybris)、[Halium 的构建](https://docs.halium.org/en/latest/porting/build-sources.html)、[UBports 移植说明](https://docs.ubports.com/en/latest/porting/introduction/Intro.html)、oFono 的 [ril 驱动](https://github.com/sailfishos/ofono/blob/0f4cdba/ofono/drivers/ril/ril_subscription.conf)和 [binder 插件](https://github.com/mer-hybris/ofono-binder-plugin)。
- 内核要求：[systemd 259](https://github.com/systemd/systemd/blob/v259/README)、[Landlock](https://man7.org/linux/man-pages/man7/landlock.7.html)、[seccomp](https://man7.org/linux/man-pages/man2/seccomp.2.html)、[memfd](https://man7.org/linux/man-pages/man2/memfd_create.2.html)、[cgroup v2](https://docs.kernel.org/admin-guide/cgroup-v2.html)、[AOSP 通用内核](https://source.android.com/docs/core/architecture/kernel/android-common)。
- 商业：[developingtelecoms（2018）](https://developingtelecoms.com/telecom-technology/telecom-devices-platforms/7595-kaios-emerging-os.html)、[ITWeb（2019）](https://www.itweb.co.za/article/mwc-kaios-to-bring-more-smart-feature-phones-to-africa/KzQenMj892PvZd2r)。
