# SKU 框架：一份硬件配置生成一个固件包

> 状态：**原型 ✓**（`sku/`、`tools/sku.py`、`phone-sku` 配方、`make sku`）。两块主线骁龙板（骁龙 662、骁龙 460）上的三个 SKU 都能构建出固件包，**未上真机**；QEMU 的九宫格、全键盘两个 SKU 开机自测通过（57（内部文档） 验证基线）。整机类主板（屏和按键是手机自带的，键盘接 USB 或蓝牙：一加 6 / 6T、红米 Note 4X）和在 Nura 上 chroot 跑我们根文件系统的套件（`tools/phone-chroot/`）是原型：单元测试和演练（dry run）✓；chroot 套件 2026-11-21 第一次在一台一加 6T 上跑（[41](41-design-gaps.md) §11：自测停在第一个失败的用例，`shell-start` 之后手机卡死，**未查明**），2026-11-22、11-23 又跑了几轮（完整自测 183/194）；整机套件 2026-11-24 到 11-27 在同一台上跑过（最后一次完整自测 204/204），都记在 [39](39-oneplus-6t.md) §16；之后的改动**未上真机**（§6、§7.4）。KaiOS 手机是从引导开始替换还是对接它的底座，见 §7（调研，**未上真机**）；整机自带键盘的键码表（`[keymap]`，§7.4）和老内核上按探测结果降级（§7.5）：单元测试 ✓，**未上真机**。厂商不能开源的部件（固件、内核模块、用户态程序）放在哪、怎么进镜像、能和什么说话，见 §8（原则和边界；私有的 BSP 层仓库、制品库等**未实现**）。两部开放的参考机（全键盘、九宫格，QCM6490）的板子和 SKU 文件见 §9：`check`、`gen` 通过，叠加层合进 Fairphone 5 的主线设备树 ✓；没有机型、没有镜像，硬件**未实现**。第三部参考机是翻盖（§9.10）：板子、外屏、键盘和 SKU 的文件 ✓，框架新加的外屏（`outer_display`）、合盖开关（`[input] lid`）和 OK 键指纹的检查、`sku.json` 字段、叠加层 ✓（单元测试，叠加层合进 Fairphone 5 的树）；合盖、外屏、两块屏的渲染这些行为只是设计，**未实现**。硬件选型见 61（内部文档），键盘与屏幕见 52（内部文档）。

## 0. 结论

- 每个 SKU 一份配置，只写「哪块主板 + 哪块屏 + 哪个键盘 + 内存档 + 地区 + 产品层」；生成器检查它们彼此对得上，展开成 Yocto 配置、设备树叠加层和运行时的设备描述；`make sku SKU=<名字>` 构建镜像并收集固件包，`sku/skus/` 里的每个 SKU 也自动是一个目标 `make sku-<名字>`（`make skus` 列出全部）。Shell、服务、应用的二进制在所有 SKU 上相同。
- 键码表只有一份（`sku/keys.toml`）：设备树的 `linux,keymap` 由它生成，测试核对 phone-paint 的 evdev 表认识其中每个键码、键码数值和内核头文件一致。
- 同一块主板的 SKU 共用内核和根文件系统，差别只在设备树叠加层、`sku.json` 和产品层；以后改成开机按板号选叠加层，一个镜像就能覆盖一个主板家族（**未实现**）。
- 厂商不能开源的部件不进任何 git 仓库：每个 SoC 一个私有的 BSP 层只放配方，闭源文件在构建时按地址加校验和从制品库取，只进产品镜像；闭源的用户态部件是单独的、进沙箱的进程，只经 Linux 的标准接口或 Kario 定义的接口说话，Shell 和系统服务不链接闭源的库（§8，大部分**未实现**）。

## 1. 文件

```
sku/
  keys.toml            按键名 → Linux 键码
  boards/*.toml        主板：Yocto 机型、设备树、基带后端、有无 GPU（`gpu`；phone-paint 默认走不走 GPU：`paint_gpu`，显示设备不能渲染时 GPU 的 render node：`gpu_render`，GPU 就在显示设备上时它的内核驱动：`gpu_driver`，比如 msm，镜像检查按它核对 Mesa 有没有这块 GPU 的驱动，`paint_gpu` 不是 `off` 时两样必须写一样，[42](42-gpu-rendering.md) §3、§4、§5）、模块的接线（设备树标签）、通知灯和背光（[leds]、backlight、keyboard_backlight：内核设备类的名字，hal/）、光线传感器（light_sensor：IIO 设备的名字）、定位接收机（gnss）、声卡（[audio]：放音的 PCM，UCM 的放音和通话设置，真机测试时调小哪些混音控件、调到多少，[20](20-audio.md) §10）、实时时钟（[rtc]：能不能设、系统时间是否存成相对它的偏移、唤醒闹钟怎么写，[28](28-battery.md) §6）、硬件看门狗（[watchdog]：systemd 运行时、重启、kexec 时怎么设它，驱动收的最长超时，[28](28-battery.md) §7）；  boards/*.toml        主板：Yocto 机型、设备树、基带后端、有无 GPU、模块的接线（设备树标签）、通知灯和背光（[leds]、backlight、keyboard_backlight：内核设备类的名字，hal/）、光线传感器（light_sensor：IIO 设备的名字）、定位接收机（gnss）、基带的 IMS（ims：替基带开 VoLTE 承载的守护进程）、声卡（[audio]：放音的 PCM，UCM 的放音和通话设置，真机测试时调小哪些混音控件、调到多少，[20](20-audio.md) §10）、实时时钟（[rtc]：能不能设、系统时间是否存成相对它的偏移、唤醒闹钟怎么写，[28](28-battery.md) §6）、硬件看门狗（[watchdog]：systemd 运行时、重启、kexec 时怎么设它，驱动收的最长超时，[28](28-battery.md) §7）；分区表（[partitions]：分区名 → 角色、默认布局、谁管 A/B 槽，[43](43-phone-partitions.md)）；整机（class = "phone"，§7.4）另有自带的屏（[display]）和键（[input]）、自带键盘的键码表（[keymap]）、设备树根节点的 compatible、要上真机确认的值（unverified）；硬件清单（[[part]]：每个部件的种类、名字、总线、绑定它的主线驱动，§9）；翻盖的合盖开关（[input] lid：霍尔开关的输入设备名；[wiring] lid_gpio：叠加层给它生成 gpio-keys）和外屏的接线（[wiring] outer_spi、outer_enable、outer_vcom，§9.10）
  displays/*.toml      屏：面板驱动的 compatible、内核配置、分辨率、DSI 通道数；QEMU 的虚拟屏另可写触摸屏（touchscreen：QEMU 的设备）；可选的总线（bus：dsi / spi）、种类（kind：lcd / oled / memory-lcd / e-paper）、颜色（color：rgb / gray / mono）、有效区的毫米数、DRM 驱动名（drm_driver）、SPI 屏的时钟和片选极性（spi_max_hz、spi_cs_high）、记忆屏 VCOM 的频率范围（vcom_hz）
  keypads/*.toml       键盘：扫描芯片、地址、行列数、矩阵位置 → 按键名、形态（九宫格 / 全键盘）、全键盘的 Fn 层（[fn]：键 → 键帽上的第二个字符）、没有 Ctrl 键时什么当 Ctrl（ctrl = "fn+sym"）、指纹传感器在哪个键里（fingerprint = "ok-key"）和它的感应面、键帽的毫米数（fingerprint_mm、fingerprint_cap_mm）
  products/*.toml      产品层：并进配置层（profile.d）的内容
  skus/*.toml          SKU：以上各取一个，加内存档、地区，可选 rotate（竖屏横装）、外屏（outer_display：displays/ 里的一块）
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
| `ref-qwerty-qcm6490-4g` | QCM6490 参考机主板（§9，没有机型，不构建） | 2.8 寸 480×640（ST7701）横装（rotate 90） | 全键盘 v1 | 4 GB |
| `ref-t9-qcm6490-4g` | 同上 | 同一块屏竖装 | 九宫格 v1 | 4 GB |
| `ref-flip-qcm6490-4g` | QCM6490 翻盖参考机主板（§9.10，没有机型，不构建） | 内屏同上竖装；外屏 1.26 寸 144×168 黑白记忆屏（Sharp LS013B7DH05） | 九宫格 v1，OK 键带指纹 | 4 GB |

开发板上「模块接在哪」是评估板的接线位置（键盘的中断脚选一个空闲 GPIO），产品主板的接线来自原理图。

## 2. 生成器检查什么

`tools/sku.py check`（`make test` 的一部分）展开每个 SKU：

- QEMU 主板只配 QEMU 的屏和键盘，真主板只配真的；键盘的形态和 SKU 的形态（`bar-t9` / `bar-qwerty`）一致；屏的分辨率（横装时宽高互换）等于形态的尺寸；内存档合法。
- 键盘矩阵：每个位置在行列范围内、每个键只出现一次、键都在 `keys.toml` 里；必需的键齐全（左右软键、OK、返回、清除、通话、挂断、方向键，九宫格的 0–9 / * / #，全键盘的 26 个字母、空格、Shift）。Fn 层只给矩阵里的字母键和逗号、句号，每个只打一个字符，有 Fn 层就必须有 Fn 键。`ctrl` 写成 `fn+<键>`，两个键都在矩阵里，矩阵里没有 `ctrl` 键（`ctrl` 在 `keys.toml` 里是 `KEY_LEFTCTRL`）。
- 屏和键盘芯片的内核驱动在 kario-qcom 机型的内核配置片段里（板子的机型在本层时；别的层的机型，比如参考机的 `kario-qcm6490`，内核配置在那个层里，这里不查）。
- 通知灯（`[leds] notify`）、背光（`backlight`）、键盘背光（`keyboard_backlight`）和光线传感器（`light_sensor`）是合法的设备类名字，多色灯的 `notify_color` 每个通道 0–255。
- 能否从挂起里叫醒（`suspend`）：真机的主板默认能，虚拟的主板（`virtual = true`，模拟器）默认不能，不能的板子从不挂起（[28](28-battery.md) §3）。
- 独立的电源键（`power_key = true`，侧键）：有的板子挂断键不兼当电源键；没有的照诺基亚的做法，挂断键就是电源键（[18](18-nokia-form-factors.md)）：按住打开电源菜单，闹钟响着时按一下稍后提醒。Kario 的九宫格和全键盘产品板（sm6115、sm4250）都写了 `power_key = true`，没写的现在只有 QEMU。每种机型怎么关机见 [18](18-nokia-form-factors.md) §5.1 的「怎么关机」。
- NFC 控制器（`nfc`）：`sim`（模拟器）或 `nci:/dev/<节点>`；不写就是没有 NFC，phone-nfcd 直接退出（[30](30-nfc.md)）。
- 指纹传感器（`fingerprint`）是 `sim` 或 `none`：模拟器写 `sim`，真机在传感器有驱动之前不写（[31](31-lock-and-biometrics.md)）。传感器做在 OK 键上的键盘在键盘配置里写 `fingerprint = "ok-key"`：键帽表面下的面阵传感器，手指放上去就读、不用按；它是 `fingerprint = { at = "ok-key", sensor = "area", read = "touch" }` 的简写，两种写法都行，按下才读（`read = "press"`）、别种传感器、别的位置都拒绝。`tools/sku.py` 要求这个键盘的矩阵里有 OK 键，写进 `sku.json` 的 `fingerprint_at = "ok-key"`；主板的 `fingerprint` 还是后端。phone-keystored 按它只为系统的指纹框给传感器通电、不用来解锁（[31](31-lock-and-biometrics.md) §4.5）；现在只有翻盖参考机的键盘（`t9-fp-v1`，§9.10）写它，开发机上自测用 `fingerprint-at ok-key` 换过去。 真主板上，板子的 `[[part]]` 里要有种类是 `fingerprint` 的部件，键盘还要写传感器的大小：感应面 `fingerprint_mm` 和键帽 `fingerprint_cap_mm`（宽、高的毫米数，感应面要放得进键帽；没有传感器的键盘不能写），不进 `sku.json`。
- 外屏（SKU 的 `outer_display`，§9.10）：屏文件里别的键、`kind`、`color` 不在表里的都报错；外屏要写 `drm_driver`（渲染器按它分出两块屏），不能有触摸屏；QEMU 的板子只配虚拟的外屏，真板子只配真的。真主板上内屏要在 DSI 上、外屏要在 SPI 上（`bus = "spi"`，`spi_max_hz` 必填，不能写 `lanes`），外屏的内核配置要在 `SPI_PANELS` 里（现在只有 Sharp 记忆屏的 `CONFIG_TINYDRM_SHARP_MEMORY`），板子的 `[wiring]` 要有 `outer_spi` 和这块屏要的（记忆屏要 `outer_vcom`：VCOM 信号的 PWM），标签都在设备树里；记忆屏的屏文件要写 `vcom_hz`（数据手册允许的 VCOM 频率，`[最低, 最高]`），`outer_vcom` 的周期（PWM 的第二格，纳秒）要落在里面，DSI 屏不能写。整机（`class = "phone"`）的外屏写在板子的 `[outer_display]` 里，SKU 不能写；不是整机的板子不能有 `[outer_display]`。
- 合盖开关（`[input] lid`）：和声音开关一样，是一个输入设备名（不超过 79 字节、可打印、不含 `|`），不能是按键之一，也不能和声音开关同名；`[input]` 里别的键在任何板子上都报错。`[wiring] lid_gpio`（标签和单元）要有 `[input] lid`，叠加层的 gpio-keys 节点就叫这个名字。
- 定位接收机（`gnss`，`location/`）：`modemmanager`（经 ModemManager 读调制解调器的 GPS，两块骁龙板这样写，**未上真机**）、`nmea:/dev/<节点>[@<波特率>]`（内核 GNSS 子系统的 `/dev/gnssN` 或串口，读 NMEA 0183）、`fifo:<名字>`（Shell 运行目录里的命名管道，第一次有应用要定位时由 Shell 建；QEMU 写 `fifo:gnss.nmea`，自测往里写 NMEA，模拟器上走的也是真的解析路径；交互运行时可以自己写，如 `cat 轨迹.nmea > /run/phone/gnss.nmea`）、`sim`（模拟接收机）。不写就是没有接收机，`location.get` 返回 `this phone has no location receiver`；开发机（没有 SKU 的 id）用模拟接收机，`kario run -sku` 模拟的表里不带它（[22](22-developer-api.md) §4.7）。
- 基带的后端（`modem`，§8.8）：telephonyd 编进的后端之一（`MODEMS`：`sim`、`modemmanager`，和 `telephonyd/src/backend.cpp` 的一样，`sku_test.py` 核对），不写是 `sim`；别的值报错（不然手机开机后 telephonyd 退出，没有电话）。
- 基带的 IMS（`ims`，[39](39-oneplus-6t.md) §11.1）：基带自己跑 IMS（VoLTE），要系统替它开 IMS 的承载时，写由谁开；现在只有 `81voltd`（经 QRTR 上 QMI 的 IMS 数据服务收基带的请求，经 ModemManager 开承载），`IMS` 表写着它要的后端，所以要 `modem = "modemmanager"`，别的值都报错。`gen` 在 kas 覆盖里写 `PHONE_IMS`；板子的 Yocto 机器在本层或给的 BSP 层里时（§8.7.1），机器要按 `PHONE_IMS` 装（`oneplus-fajita.conf`：`81voltd` 和按 SIM 卡激活基带配置的 `phone-qcom-pdc`），机器的配置里在 `PHONE_IMS` 以外写着 `81voltd`（不管 SKU 都装）也报错：板子文件是唯一的来源。不写就是基带不要（或者还没人替它开），镜像里没有 81voltd。现在只有一加 6T 写了（标在 `unverified` 里）。
- 声卡（`[audio]`，[20](20-audio.md) §10）：`pcm`（audiod 放媒体的 PCM，`plughw:<卡>,<设备>`）必填；`card`（`hw:<n>`）、`verb`、`device` 是 UCM 的放音设置，`call_verb`、`earpiece`、`earpiece_pcm`、`call_speaker`、`call_speaker_pcm`、`call_mic` 是通话的。照 phone-audiod 的参数检查谁要带上谁（有 `card` 就要 `verb`，有 `call_verb` 就要 `earpiece`，通话的设备要 `call_verb`），PCM 要在 `card` 那张声卡上。板子的 Yocto 机器在本层或给的 BSP 层里时（§8.7.1），机器的 `PHONE_AUDIO_*` 要和 `[audio]` 一个不差（多、少、不同都报错）：板子文件是唯一的来源，整机镜像的 audiod 和 chroot 用的就是同一套值。不写就是 audiod 自己找声卡（`--sink auto`）。`quiet_controls`（`card` 那张声卡的简单混音控件名）和 `quiet_level`（`0%`–`100%`）两个一起写、要有 `card`：真机测试套件跑 `selftest`、`pixel` 前把这些控件调到这个音量，跑完调回原来的值（§6.2）；只给套件（`phone.env`），不进 `sku.json`、不是机器的变量。
- 硬件看门狗（`[watchdog]`，[28](28-battery.md) §7）：`runtime`、`reboot`、`kexec` 是 systemd 的 `RuntimeWatchdogSec`、`RebootWatchdogSec`、`KExecWatchdogSec`，每个是 `off`、`default`（硬件现有的超时）或时间（`30s`、`2min`、`1h`）；`max_timeout` 是板子的看门狗驱动收的最长超时（秒）：写了它，三个里是时间的（没写的按 systemd 的默认：运行时关、重启 10 分钟、kexec 关）都不能超过它，超过的驱动会以 `EINVAL` 拒绝，systemd 随即解除看门狗。别的键、格式不对的都报错。`gen` 把它写成 systemd 的 `system.conf` drop-in（`watchdog.conf`），没写的板子只有一行注释（照 systemd 自己的）。一加 6T 和 6：`max_timeout = 32`、`reboot = "30s"`（标在 `unverified` 里）。
- 分区表（`[partitions]`，[43](43-phone-partitions.md) §2）：`slots` 是 `abl`（bootloader 在 `boot_a`、`boot_b` 的 GPT 属性里管槽）或 `state`（initramfs 在状态分区里管），`layout` 是 `userdata` 或 `ab` 而且是这些分区放得下的，每个分区一个名字和角色（`boot`、`dtbo`、`root`、`data`、`state`、`vbmeta`、`misc`、`efs`、`firmware`、`android`），加上槽后缀以后不重名，根是一对；禁写名单（`NEVER`：本机独有的数据、bootloader 的状态和固件的分区名，高通、展锐、联发科的）里的分区不能有要写、要擦的角色，`efs` 的必须在名单里；`slots = "state"` 的板子（QEMU）的分区就是 A/B 磁盘的和 RAUC 的槽。有分区表的板子，`gen` 在 kas 覆盖里写 `PHONE_SLOT_KEEPER`、`PHONE_ROOT_SLOTS`、`PHONE_BOOT_SLOTS`，另写给 initramfs 的 `partitions.conf`；`sku.py partitions <板子> [布局]` 打出刷机脚本读的表；`phone.env` 带上 `KARIO_PARTITIONS`。
- 实时时钟（`[rtc]`，`hal/rtc`，[28](28-battery.md) §6）：`read_only`（设不进去，设时间时不写它；默认 `false`）、`offset`（系统时间存成相对这个只读 RTC 的偏移，开机按两者设时钟；默认 `false`，只能和 `read_only = true` 一起）、`alarm`（唤醒闹钟：`relative` 写 `+N`，默认，哪种 RTC 都对；`absolute` 写唤醒时刻，只读的 RTC 不能这样写），别的键、不是布尔值的都报错。一加 6T 是只读、存偏移（6 和红米 Note 4X 照它写，标在 `unverified` 里），QEMU 的 pl031 能设；写了 `[rtc]` 的板子在 `sku.json` 里有完整的 `rtc`（默认值补上），没写的没有（Shell 当作能设、`+N` 闹钟）。
- USB 主机（`[usb]`，`hal/usbhost`，[27](27-usb-typec.md) §2.1）：`host`（USB-C 口能不能当 USB 主机，接键盘、鼠标、U 盘；默认 `false`，Shell 不碰主机这一侧）、`vbus_ma`（当主机时口上 VBUS 最多给多少毫安，放行设备时的预算；默认 500，100 到 3000）、`controller`（只管这个 USB 控制器下的总线，设备树节点名，如 `a600000.usb`；不写是除了 dummy_hcd 以外的所有总线：板子里面接在别的控制器上的东西不用用户放行），没有 `host = true` 却写了另两个、别的键都报错。QEMU 写了（xHCI），骁龙参考板和翻盖机写了、标在 `unverified` 里，一加 6T 还没写（内核不能当主机，[39](39-oneplus-6t.md)）；`host = true` 的板子在 `sku.json` 里有 `usb`（默认值补上，空的 `controller` 不写），Shell 读进 `SKU.USB` 并核对。
- 触摸屏（屏的 `touchscreen`，2026-11-25 起）：虚拟屏上只能是 QEMU 的 `virtio-multitouch-pci`，`gen` 把它加进 kas 覆盖的 `QB_GRAPHICS`（`qemu-480x640` 写了它，`qemu-t9` 和 `qemu-t9-1g` 有触摸屏，全键盘的 `qemu-640x480` 没有）；板子的 Yocto 机器在本层或给的 BSP 层里时（§8.7.1），触摸屏的驱动要编进这台机器的内核（`TOUCHSCREENS`：`syna,rmi4-i2c` 是 `CONFIG_RMI4_CORE`、`CONFIG_RMI4_I2C`、`CONFIG_RMI4_F12`，在内核配方列的配置片段里都是 `=y`：编进内核开机就有，lockdown 下也不用加载签名的模块），表里没有的触摸屏、做成模块的、没写的都报错；机器不在本层、也不在给的 BSP 层里的整机（chroot 用 Nura 的内核）不查。写了触摸屏的屏，`sku.json` 里有 `touch`。
- 有内核源码时（Yocto 工作目录里的那份，或 `PHONE_KERNEL_SRC`），沿着主板 dts 的 `#include` 收集标签，核对接线引用的标签都存在——一加 Nord N100 的稳压器叫 `vreg_l9a`，骁龙 662 那块叫 `pm6125_l9a`，写错在生成阶段就报出来。DSI 主机的输出端点默认是 `<panel_dsi>_out`（骁龙 662 的 `mdss_dsi0_out`）；SC7280 的 DSI 主机叫 `mdss_dsi`、端点却叫 `mdss_dsi0_out`，这样的板子写 `panel_dsi_out`（参考机）。
- 唤醒脚（2026-11-26 起，有内核源码时）：要叫醒手机的 GPIO 都得是 SoC 深度睡眠时能叫醒它的脚。深度睡眠时 SoC 的 GPIO 控制器（TLMM）断电，脚上的中断只能经一直开着的唤醒控制器（SC7280、SDM845 是 PDC，SM6115 是 MPM）送到 CPU，它只看得见 pinctrl 驱动的唤醒表（`.wakeirq_map`，比如 `drivers/pinctrl/qcom/pinctrl-sc7280.c` 的 `sc7280_pdc_map`）里的脚。查的是：不是整机的板子上，叠加层给了 `wakeup-source` 的 `[wiring]`（`WAKE_WIRING`：键盘的中断 `keypad_irq`、合盖开关 `lid_gpio`）；每块板子上，板子自己的设备树里 `[input]` 点名的 gpio-keys 节点（`buttons`、`sound_switch`、`lid`，按节点的 `label` 找）下带 `wakeup-source`（或老写法 `gpio-key,wakeup`）的键。按脚所在控制器的标签（`tlmm`）找它的 `compatible`，按 compatible 找 `drivers/pinctrl` 里的驱动，按驱动里这个 compatible 的 `.data` 找唤醒表；驱动里没有唤醒表的控制器（PMIC 的 GPIO，经 PMIC 自己的中断叫醒）不查。表里没有的脚报错，报出脚、表和叫不醒的是什么（`board qcm6490-ref [wiring] keypad_irq: GPIO 64 of tlmm is not in sc7280_pdc_map (drivers/pinctrl/qcom/pinctrl-sc7280.c), ...: a key press would not wake the phone`）。没有内核源码时和标签的检查一样跳过，`sku.py check` 说出来（`no kernel tree (PHONE_KERNEL_SRC): device tree labels, part drivers and wake GPIOs not checked`）。一加 6T 是整机，唤醒脚都在它自己的设备树里：板子点名的电源键 `pm8941_pwrkey` 不是 gpio-keys（经 PMIC 叫醒），音量键在 PMIC 的 GPIO 上、也没写 `wakeup-source`，声音开关（7.1-rc1 才有这个节点，GPIO 126、52、24）不是唤醒源（126 不在 `sdm845_pdc_map` 里也不要紧：醒来时 gpio-keys 读一遍开关的位置）；所以 6T 现在没有要查的脚，`tools/sku_test.py` 在一棵假的树上验证它们要是唤醒源就会被查。SM6115、SM4250 两块产品板的键盘中断（GPIO 33、35）在 `sm6115_mpm_map` 里，可主线的 `sm6115.dtsi`（6.18、7.1-rc1）没有 MPM 节点、TLMM 也没有 `wakeup-parent`，深度睡眠时按键叫不叫得醒**未验证**。
- 硬件清单（`[[part]]`，§9）：每项有名字、种类（`kind`，`PART_KINDS` 里的一个或几个）、总线（`bus`，`PART_BUSES` 之一）、驱动（`driver`：内核源码里 `drivers/<路径>.c` 或 `sound/<路径>.c` 的一个或几个文件，或者 `"none"`：主线没有驱动）；有驱动的要有设备树的 `compatible`，`"none"` 的不能有；摄像头、也只有摄像头写朝向（`facing`：`front`、`back`）；别的键报错。有内核源码时，每个驱动文件都要在，其中一个要有带引号的这个 compatible（驱动的匹配表）。清单只给做板子的人和文档：不进 `sku.json`，也不进 `kario run -sku` 的表；`sku.py parts <板子>` 一行一个打出来（种类、总线、compatible、驱动、名字）。
- 整机类主板（`class = "phone"`，§7.4）：SKU 不写屏和键盘（是手机自己的），必须写形态（`device`）；面板去掉留黑的行（`inset`，上下各一份）以后要和形态朝向一致（横屏形态写 `rotate = 90`，有留黑的行就不能转）；`scale` 在 1–8，一个逻辑像素的物理尺寸在 0.15–0.36 mm（目标机 2.8 寸 480×640 按 2 倍是 0.178 mm；下限是 2.4 寸 240×320 按 1 倍，上限是目标机的两倍）；物理尺寸和分辨率的长宽比相差不超过 5%（毫米数写反了逃不过上一条）；有 `compatible`；`unverified` 列的每一项都是这块板子里有的值；写了键码表（`[keymap]`）的，键码在 1–767、键名在 `keys.toml` 里、形态要的键都有（§7.4），不是整机的板子不能写。板子写了 Yocto 机器（`machine`，一加 6T 的 `oneplus-fajita`，[39](39-oneplus-6t.md)）的整机，`gen` 写 `sku.json` 和 kas 覆盖（机器、SKU 名；内核自己的设备树 `dtb`，不生成覆盖层）；整机没有固件包，`package`（也就是 `make sku-<名字>`）报错，指向板子的刷机脚本（`flash`）。还没有 Yocto 机器的整机，`gen`、`package` 直接报错，指向 chroot 套件（§6.2）；`sku.py files <SKU> <目录>` 只写 `sku.json`、产品层和给套件的 `phone.env`；`kario run -sku` 模拟的表不列整机（它们是测试机，不是写应用的目标）。
- `tools/sku_test.py` 另外核对：形态表和 `shell/device.go` 一致；phone-paint 认识每个键码；键码数值和 `/usr/include/linux/input-event-codes.h` 一致；整机的规则（每条都有一个写错的例子）；触摸屏的规则（QEMU 的设备，6T 的驱动编进内核，做成模块、没写都报错）；`[audio]` 的规则，它和 6T 机器的 `PHONE_AUDIO_*` 相同、改了任一边都报错，`sku.json`、`phone.env` 和 `shell.SKU` 里都是这些值；`[[part]]` 的规则（每条一个写错的例子，在一棵临时的内核树上核对驱动文件和 compatible），`panel_dsi_out`，别的层的机型不查内核配置，参考机的三个 SKU 展开出来的东西，本文 §9 列了每个部件和每块屏的 compatible（中英文两份）；外屏、合盖开关、OK 键指纹的规则（每条一个写错的例子），QEMU 的外屏是第二个 virtio-gpu；唤醒脚的规则（在一棵假的内核树上：直板原来的 GPIO 64 和接在 64 上的合盖报错，翻盖和 6T 通过，6T 的声音开关要是唤醒源就报错，唤醒表按驱动的 `.data` 找、分不清是哪张表时报错，没有内核树时跳过；叠加层给 `wakeup-source` 的线就是 `WAKE_WIRING` 列的；有真的内核树时每个 SKU 都过、GPIO 64 报错）。`tools/phone_chroot_test.py` 演练 chroot 套件：每一步在构建机上都能打出计划，演练不改任何东西，Nura 的基带一套不在要停的里面；`setup` 按板子设好声卡、`teardown` 拆掉 `setup` 记下的；两个脚本 shellcheck 干净。

## 3. 生成什么

| 产物 | 内容 |
|------|------|
| `yocto/kas/generated/sku.yml` | 机型（`qemuarm64` / `kario-qcom`）、`PHONE_SKU`；QEMU 的屏幕尺寸和内存（有外屏的 SKU 另加一个外屏大小的 virtio-gpu 设备，§9.10.11）；真主板的设备树文件名；板子写了 `ims` 时的 `PHONE_IMS`（机器按它装 81voltd）；板子 `gpu = true` 时的 `PHONE_SKU_GPU = "1"`（有它 `phone-sku` 才接受 `PHONE_PAINT_GPU_DEFAULT`，[42](42-gpu-rendering.md) §3），板子写了 `gpu_render` 或 `gpu_driver` 时另有 `PHONE_SKU_GPU_DRIVER`（GPU 的内核驱动，镜像检查 `phone-gpu.bbclass` 按它找 Mesa 的驱动，[42](42-gpu-rendering.md) §5）。Makefile 的每个 kas 配置后面都接上它 |
| `share/phone/sku.json`（`phone-sku` 包） | SKU、主板、形态、屏、键盘、内存、`web_gpu`、基带后端、指纹传感器后端（phone-keystored 按它选）、定位接收机（`gnss`，Shell 按它选）、地区、产品，全键盘另有 `fn`（Fn 层）和 `ctrl`（什么当 Ctrl，[18](18-nokia-form-factors.md) §4.2）；整机另有 `width`、`height`（画的面积：面板减去留黑的行）和 `scale`，Shell 按它们排版（文档的列数跟着逻辑宽度）、握手时交给渲染器（§7.4）；自带键盘的整机另有 `keymap`（键码 → 键名）和 `keypad = "builtin"`，Shell 换成键码对键码的表、握手时交给渲染器（§7.4）；屏有触摸屏时另有 `touch`，Shell 握手时交给渲染器（`--device` 换形态时也带上），phone-paint 这才读触摸屏（[10](10-keyboard-interaction-model.md) §2.1）。phone-shell 没给 `--device` 时按它选形态，握手时把 Fn 层交给渲染器，phone-paint 在按住 Fn 时按它出字（[23](23-renderer-protocol.md) §2）；网页进程的内存上限按内存档定，是否用 GPU 按 `web_gpu`；板子（或 SKU）的 `paint_gpu` 不是 `off` 时另有 `paint_gpu`（`compose`、`raster`），板子写了 `gpu_render` 时另有它，Shell 核对后握手时放在 `device` 的 `gpu`、`gpu_render` 里交给渲染器（`--device` 换形态时也带上），phone-paint 没设 `PHONE_PAINT_GPU` 时按它走（[42](42-gpu-rendering.md) §3；`paint_gpu` 只有 `gpu = true` 的板子能写，现在所有板子都是 `off`）；主板配了通知灯和背光时另有 `leds`（`notify`：名字、颜色）、`backlight` 和 `keyboard_backlight`，Shell 用它们慢闪提示、按设置的亮度点亮背光和键盘背光（设置 › 显示与亮度）、息屏前调暗；配了光线传感器时另有 `light_sensor`，自动亮度读它；板子的部件（`[[part]]`）里有种类为 `proximity` 的距离传感器时另有 `proximity`，Shell 按它定通话中电源键挂断的默认（[19](19-core-apps.md) §5.6；读距离传感器**未实现**）；配了声卡时另有 `audio`（`[audio]` 原样），Shell 自己起的 audiod 放到它的 `pcm`（`--services spawn`，[20](20-audio.md) §10）；写了 `[rtc]` 时另有 `rtc`（`read_only`、`offset`、`alarm`），Shell 按它写 RTC 或存偏移，`hal/pm` 按它写唤醒闹钟，`phone-clock` 开机按它设时钟（[28](28-battery.md) §6）；没有背光的机器上 Shell 用模拟的背光，设置页写明（[11](11-platform-architecture.md) §「那 hal/ 是什么」）；翻盖另有 `lid`（合盖开关的输入设备名）、`outer`（外屏：`display`、`width`、`height`、`kind`、`color`、`drm_driver`、毫米数）和 `fingerprint_at`（`ok-key`），`shell.SKU` 读进 `Lid`、`Outer` 并核对，谁用它们见 §9.10（**未实现**） |
| `profile.d/20-product.json` | 产品层（关于本机里的产品名等），叠在镜像自己的配置层上 |
| `/etc/phone/partitions.conf`（`phone-sku-partitions` 包） | 板子的分区表给 phone-initramfs 的：谁管槽，根的一对、数据、持久区的名字（[43](43-phone-partitions.md) §2）；没有分区表的板子是空文件 |
| `/usr/lib/systemd/system.conf.d/50-phone-watchdog.conf`（`phone-sku` 包） | 板子的 `[watchdog]`，systemd 的 `RuntimeWatchdogSec`、`RebootWatchdogSec`、`KExecWatchdogSec`（[28](28-battery.md) §7）；没写的板子只有注释，照 systemd 自己的 |
| 设备树叠加层（真主板） | 屏接在 DSI 上（compatible、复位脚、供电）、键盘接在 I2C 上（中断、行列数、键码表、能唤醒）；有外屏的接在 `outer_spi` 上（时钟、片选极性、使能脚，记忆屏的 VCOM 走 `outer_vcom` 的 PWM 并打开那个 PWM 控制器）；有 `lid_gpio` 的生成合盖的 gpio-keys（`EV_SW`、`SW_LID`、能唤醒，名字是 `[input] lid`）；`phone-sku` 配方用 dtc 编译，`fdtoverlay` 合进主板的设备树（内核构建时带符号 `-@`），得到 `phone-sku-<SKU>.dtb` |
| 固件包 `build/sku/<SKU>/firmware/` | 内核（打包了 phone-initramfs，由它挂根文件系统，[25](25-system-update.md)）、合成后的设备树（真主板）、根文件系统、`sku.json` 和清单（各文件的 SHA-256 与大小）；先核对所有文件都在，再替换旧包 |

`kario-qcom` 机型：一个内核带两块板的设备树；内核用 qemuarm64 的同一份源码，配置从 arm64 的 defconfig 出发，加上平台需要的（Landlock、seccomp、命名空间、cgroup、nftables 连接标记和个人热点的 NAT、系统更新要的 dm-verity / squashfs / nbd）和 SKU 用到的模块驱动。软件包沿用 qemuarm64 的指令集调优（ARMv8.0 + CRC，A73 / A53 都能跑），直接复用共享缓存；量产时换成 A73 + A53 的调优。

全键盘 SKU 把同一块 2.8 寸竖屏横装：phone-paint 发现屏的模式正好是形态尺寸横竖互换时，按形态的横屏尺寸画，上屏时顺时针转 90°（脏区域跟着换算）。

## 4. 斐讯 N1 盒子（不再做）

2026-11-23 起不做：N1 上能测的（计算、存储、HDMI 显示）在一加 6T 上都能测，6T 还能测基带、声音、屏和电源（§6，[39](39-oneplus-6t.md)）。

## 5. 还没做的

- 真机：启动方式（高通的 ABL 要 boot.img）、刷机包格式、各子系统固件（GPU、基带、音频 DSP 的签名固件要从设备厂拿）都**未实现**；固件包现在是内核、设备树、根文件系统和清单。
- 开机按板号选叠加层（一个镜像覆盖一个主板家族）**未实现**。
- 展锐 T610：主线内核里没有显示、基带、USB，要等厂商 BSP（61（内部文档） §2.1），暂无 SKU。
- 厂商的闭源部件：私有的 BSP 层仓库、制品库、闭源配方的 `LICENSE_FLAGS`、给厂商进程的沙箱策略都**未实现**（§8.6）。
- 参考机（§9）：`kario-qcm6490` 机型、底板、它的设备树和镜像都**未实现**，`make sku-ref-…` 停在 bitbake 找不到机型；Kario 这边的摄像头、耳机、距离传感器、加速度计、NFC 都**未实现**（§9.3）。
- 翻盖（§9.10）：渲染器读 `lid`（phone-paint 报盖子合着没有，[23](23-renderer-protocol.md) §2），还不用 `outer`；Shell 照盖子熄灭、点亮内屏，合着时不算键盘的键（§9.10.8，单元测试和开发机的自测 ✓，**未上真机**），挂起时用 `lid`、`outer`（外屏的分钟唤醒、合着盖时只有一种周期唤醒，[28](28-battery.md) §3，单元测试 ✓，**未上真机**）；「合盖挂断」「开盖接听」、外屏的内容、两块屏的渲染都**未实现**，OK 键的 USB 指纹传感器没有后端，扫码没有取景和识别；外屏的叠加层节点和合盖的 gpio-keys 只合进过 Fairphone 5 的树。

## 6. 拿现成的手机做真机测试（2026-11 调研，11-17 更新）

要测基带、声音、显示、电源，得用一台主线内核支持得好的手机。Nura（原 postmarketOS，2026 年 9 月改名）有现成的镜像：先装上它，在上面 chroot 跑我们的根文件系统做和 §4 一样的测试（§6.2 的套件），以后再做这台手机的 BSP。下表按 Nura 的安装页、设备页和社区的说法，**没有亲测**。

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
- 键：电源键是 PMIC 的 `pm8941_pwrkey`，音量上下是 gpio-keys「Volume keys」；另有一个霍尔传感器（SW_LID，给翻盖皮套用），没有键，渲染器不管它。一加的三段式提醒滑块在主线设备树里是输入设备（`sdm845-oneplus-common.dtsi` 的 `alert-slider`，6 和 6T 都有）：gpio-keys「Alert slider」，三个 GPIO（126、52、24）各报 EV_ABS 的 `ABS_SND_PROFILE`，上中下三档是 `SND_PROFILE_SILENT`（0）、`SND_PROFILE_VIBRATE`（1）、`SND_PROFILE_RING`（2）；没有键。6T 的板子用 `[input] sound_switch` 写上它，渲染器读它的位置，定声音模式（§7.4、[20](20-audio.md) §4.1，**未上真机**；2026-11-26 在 7.1-rc1 的源码和 6T 构建出的 dtb 里查过）。
- 声音：6 的扬声器功放（MAX98927）主线能驱动，6T 的（TFA9894）不能，设备树里删掉了它的链路：主线上 6T 只有听筒和耳机，Nura 的内核是否补上**未核实**。
- 通知灯：6 有 PMI8998 LPG 的 RGB 灯（多色 LED 类设备 `rgb:status`），6T 没有。
- 电池是 bq27411 电量计（第一个 Battery 类型的电源）；振动是 PMI8998 的触觉驱动，在 Nura 的内核里（输入设备的力反馈），不在主线 6.18。
- 6T 板子的 `[[part]]`（2026-11-27，产线的硬件测试照它查，[40](40-testing.md) §10）：Wi-Fi 和蓝牙是 WCN3990（`qcom,wcn3990-wifi`、`qcom,wcn3990-bt`），电量计 bq27411（`ti,bq27411`），振动是 PMI8998 的触觉（主线没有驱动，`driver = "none"`），前后两个摄像头（mediad 打不开，`none`，SKU 把摄像头的检查写成可选）；SLPI 后面的传感器不写（不是 IIO，测试看不到）。
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

### 6.2 真机测试套件：chroot 和整机（`tools/phone-chroot/`，原型，chroot 在一台 6T 上跑过）

放在 `tools/` 而不放 `yocto/`：它不构建东西，用的是构建好的镜像，在构建机上经 SSH 指挥手机。它用同样的步骤、同样的结果，从两条路测整机的 SKU：**chroot**（默认：手机跑 Nura，我们镜像的根文件系统在它的内核、面板、基带和声卡上的 chroot 里跑）和**整机**（`--whole`：手机刷了 Kario，用自己的内核和镜像，[39](39-oneplus-6t.md) §6）。两个 POSIX sh 脚本，shellcheck 干净：`kario-phone.sh`（构建机：`--sku` 选整机的 SKU，按步骤把套件推上去（chroot 还推镜像）、在手机上跑各步、取回结果、复原）和 `on-phone.sh`（手机上以 root 跑，busybox 的 sh 也行：每一步的实际动作，两条路都是它，`KARIO_TARGET=chroot` 或 `whole`）。两条路共用的东西只有一份实现：这次运行的结果目录、`kit.txt`、事实和它的抹除、分离运行的步骤和对它的轮询、`collect`。`--dry-run` 在构建机上把每一步在这边和手机上要做的事打出来，不连手机、不改任何东西；手机上 `on-phone.sh --dry-run <步骤>` 按手机的实际情况打出要做的事。`--only 正则` 让 `selftest` 和 `pixel` 只跑名字匹配的用例（phone-shell 的 `--only`，不管里面有什么，到手机上都是一个词）。`selftest`、`pixel` 的媒体用例真的从扬声器放音：跑之前套件把板子 `[audio]` 的 `quiet_controls` 调到 `quiet_level`（6T：`RX0 Digital`–`RX4 Digital` 到 15%，是不是管得到扬声器**未核实**，[39](39-oneplus-6t.md) §14.10），每个控件原来的值记在状态目录里，跑完调回去，`teardown` 也调回中途停下的那次没调回的；`--quiet 30%` 换个音量，`--quiet off` 不动混音；板子没写就不动（单元测试 ✓，**未上真机**）。

**镜像**（chroot）：默认用板子自己的机型的（板子文件的 `machine`，套件 `phone.env` 里的 `KARIO_MACHINE`；6T 是 `phone-image-oneplus-fajita.rootfs…`），取 `$WORK` 下最新的；`--image` 指定文件，`--machine` 换机型。套件看文件里的字节、不看文件名来认它装的是什么，收三种：ext4（`.ext4`）、它的 zstd（`.ext4.zst`）和刷机用的 Android sparse 格式（`.ext4.sparse`，推的时候由 `tools/phone-chroot/unsparse.py` 边推边解开：6T 两条路用同一个 userdata 镜像）；别的在往手机送任何东西之前就拒绝，并说明收什么（`.wic` 是整块磁盘，tar 不能回环挂载）；解到一半失败的镜像不会留在手机上。板子自己的镜像就是刷机后手机跑的东西：6T 的有带 freedreno 的 Mesa（GPU 路径）、alsaucm 和声卡的 UCM 配置（`alsa-ucm-conf-sdm845`）、高通的那些服务和固件，按骁龙 845 调优。通用机型的镜像（不是整机的板子用的机型，`KARIO_GENERIC_MACHINES`：qemuarm64、kario-qcom；ARMv8.0 + CRC，A75、A55、A53 都能跑）照样能跑，二进制是一样的，套件会说它缺什么（QEMU 镜像里渲染器找不到 GPU 驱动，`msm: driver missing`，声卡由 Nura 的 alsaucm 设）；别的板子的镜像拒绝（`--force` 硬上）。套件只把 `sku.json` 和产品层换成这个 SKU 的（`tools/sku.py files`）。在手机上回环挂载 ext4，而不是解开 tar：属主、扩展属性（文件能力）、设备节点原样保留，构建机不需要 root，chroot 的根还是个挂载点（应用沙箱的 pivot_root 要这个）。手机上的东西都在 `/var/lib/kario-chroot/`：镜像、挂载点、我们的状态（绑定进 chroot 的 `/var/lib/phone` 和 `/root`，镜像本身几乎不写）、每一次的结果目录。**支持整机之前构建的镜像不能用**：它的 Shell 会去抢 USB 控制器，在 `usb0` 上加丢弃一切的规则，把 Nura 的 USB 网络（也就是 SSH）断掉，也不认 `scale`；套件发现镜像不支持就拒绝（`--force` 硬上，要经 Wi-Fi 连手机）。所以先用这棵树 `make yocto-build`。

**chroot 里有什么不一样，套件怎么办**（每次运行的 `facts.txt` 在 `== chroot: what is Nura's and what is the image's` 下面列出来；**未上真机**）：

- 镜像里的 systemd 单元不跑：Shell 自己起服务（`--services spawn`），基带那一套（ModemManager、rmtfs、pd-mapper、tqftpserv、q6voiced）和蓝牙由 Nura 跑；镜像里的这些从不启动，起了会和 Nura 的抢。
- 内核和它的模块是 Nura 的：它要什么模块自己从自己的 `/lib/modules` 加载；镜像里的是给 Kario 的内核的，用它的密钥签名，Nura 的内核不会接受。我们的代码不跑 `modprobe`。
- 固件是 Nura 的：`setup` 之后内核加载的固件取自 Nura 的 `/lib/firmware`，除非 `--firmware image`：它把内核的固件路径（`/sys/module/firmware_class/parameters/path`）先指到镜像的 `/lib/firmware`（GPU 的 `a630_sqe.fw`，Nura v26.06 加载不了，[41](41-design-gaps.md)）；`teardown` 把原来的路径放回去。
- 声卡：参数是 `sku.json` 里板子的 `[audio]`，和刷机后手机上的 audiod 从它的单元拿到的一样（`tools/sku.py check` 保证两边相同）。Shell 自己起的 audiod 用镜像里的 alsaucm 设声卡，和刷机后的手机一样（`programs/shellcmd` 的 `privateAudio`：放音的设置总有；通话的设置只在用 SKU 自己的基带时有，所以 `shell-start` 里有、自测里没有，自测的通话走模拟基带；单元测试 ✓，旧代码上会失败）。只有镜像里没有 alsaucm 时（QEMU 镜像）`setup` 才自己用 Nura 的 alsaucm 设；板子没写 `[audio]` 就什么都不设（以前给 0 号声卡的 `HiFi`，那是套件猜的，不是板子说的）。

**整机**（`--whole`；2026-11-24 到 11-27 在一加 6T 上跑过，[39](39-oneplus-6t.md) §16.9–§16.14；之后的改动**未上真机**）：手机跑的是 `yocto/fajita-flash.sh flash` 写进去的东西（[39](39-oneplus-6t.md) §6；`test` 从内存里启动一次，只有 `flash` 之前写过 userdata，Kario 才起得来）。套件经 phone-bringup 的 USB 网络连它（`dbg0`、`root@172.16.42.1`，开发镜像的 dropbear，root 没有密码：`PHONE_BRINGUP = "1"` 的镜像，机型的默认；[39](39-oneplus-6t.md) §7）。这也是 Nura 的地址，所以主机密钥分开存（`~/.ssh/known_hosts.kario-whole`）；镜像第一次开机时生成新的主机密钥，所以每次刷完 `ssh-keygen -R 172.16.42.1 -f ~/.ssh/known_hosts.kario-whole`。`push` 只推套件，放在 `/var/lib/kario-test/`；`check` 要求手机上的 Kario 是为这个 SKU 构建的（手机自己的 `sku.json`）；`setup` 设时钟、记 `kit.txt` 和事实，别的都不改；`selftest`、`pixel`、`bench` 在这一步里停掉面板上的 Shell 和声音服务（`phone-display.service`、`phone-audiod.socket` 和 `.service`：开机自测 `phone.selftest=1` 时它们本来就不跑，自测自己的 audiod 要用声卡），在委派给它的 systemd scope 里跑（`-p Delegate=yes`：Shell 给每个应用一个自己的组，和开机自测的单元一样），跑完再启动它们；`teardown` 结束套件的步骤、启动还停着的单元，`--purge` 再删掉 `/var/lib/kario-test`。结果和 chroot 的一样，放在同一个地方。那边现在还做不到的，以及原因：

- 开发者通道（开发者模式加信任的数据线：`10.71.0.1`，`kario device` 登录的地方，[27](27-usb-typec.md) §4）能走 `push`、`setup`、`facts`、`collect`，走不了 `selftest`、`pixel`、`bench`：它们自己的 Shell 会接管 USB 控制器，把线上的连接设成不受信任的电脑，会话就断了（停掉 `phone-display` 也会让信任失效）；`on-phone.sh` 在这条通道上拒绝它们。没有 phone-bringup 的镜像只有这条通道和 Wi-Fi。
- 自测的 Wi-Fi、蓝牙用例是照模拟器的模拟射频写的：在手机上它们经 iwd 和 BlueZ 动的是手机自己的射频，**未核实**，可能断掉 Wi-Fi 连接；USB 的用例会跳过（phone-bringup 占着控制器）。用 `--only` 把它们排除（[39](39-oneplus-6t.md) §14.1）。
- `phone-bringup-awake` 持着唤醒锁时「挂起」用例过不了（`setup` 会说）；不持锁时 USB 连接随手机一起睡，轮询等得过去。
- `shell-start`、`shell-stop` 是 chroot 的：整机的 Shell 是 `phone-display.service`，它的环境用 drop-in 设（[39](39-oneplus-6t.md) §14.1）；`mem` 量的是正在跑的系统的进程（Kario 的单元和应用的 scope）。
- 只有带 `share/phone/source.txt` 构建的镜像（`tools/sku.py gen` 写它），`kit.txt` 才写得出镜像是从哪个提交构建的；更早的镜像写「not recorded」。
- 一台手机不能同时走两条路：Nura 和 Kario 都要占满 userdata，换路就要重刷（chroot 的 `/var/lib/kario-chroot` 随 Nura 一起没了）；`collect` 取回来的留在电脑上。

**步骤**（`tools/phone-chroot/kario-phone.sh --sku oneplus-fajita-t9 [--whole] <步骤>…`）：

| 步骤 | 做什么 |
|------|--------|
| `push` | 先核对构建机上有这台手机的备份（`~/kario-phone-backup/<板子>-<序列号>`，序列号取自内核命令行的 `androidboot.serialno`，SHA-256 对得上；没有就拒绝，`--force` 才继续，[39](39-oneplus-6t.md) §3.5；打出来的序列号只留最后两位）；生成 SKU 的文件，连同 `on-phone.sh` 和 `kit.txt`（这棵树的提交、有没有改动，由 `tools/phone-chroot/tree-note.sh` 给出，SKU，镜像的文件名和大小）拷到手机；chroot 再把镜像解开后经 SSH 写过去（手机上已经是同一个镜像就跳过，挂载着就拒绝，要约 2 GB 空间，整个过来了才留下）；整机不再推别的 |
| `setup` | 核对是不是这块板（设备树的 compatible）、aarch64；chroot 还核对有回环设备、手机跑的是 Nura 而不是刷进去的 Kario，整机核对跑的是为这个 SKU 构建的 Kario。手机的时钟比构建机慢 10 分钟以上时照构建机的设（6T 的 RTC 在主线上只能读，不联网开机是 1970），记进 `clock.txt`；测试步骤的每个命令都带 `PHONE_CLOCK=host`（chroot 里时钟是 Nura 的，整机上是系统自己的服务的，不归测试的 Shell），Shell 不设时间、不开关 NTP、不碰 RTC（[28](28-battery.md) §6）；把 `kit.txt` 拷进这次的结果，加上走的是哪条路、镜像的戳记、它的 `os-release` 和它是从哪个提交构建的（`share/phone/source.txt`；套件的树是另一个提交时加一行提示）；记下事实（`facts`，抹掉手机的序列号、IMEI、IMSI、ICCID、号码和硬件地址）。chroot 另外：停掉会抢东西的 Nura 服务（显示管理器 tinydm、greetd、sddm、lightdm、gdm：在跑才停，记下来），结束会话里的 pipewire、pulseaudio、callaudiod、feedbackd、chatty、gnome-calls、mmsdtng；回环挂载镜像，`/dev`、`/sys`、`/run` 递归绑定并设成 slave（chroot 里卸载不会卸掉 Nura 的），新挂 `/proc`，`/tmp`、`/var/volatile` 用 tmpfs；写进 SKU 的 `sku.json`、产品层和 Nura 的 `resolv.conf`；有统一的 cgroup 层级时建 `/sys/fs/cgroup/kario`，打上 `trusted.delegate`（Shell 只接手委派的组，每个应用一个子组）；切到空闲的 8 号控制台（不然打给 Shell 的键也进了 1 号控制台的登录提示）；照上面说的设板子 `[audio]` 的放音设置（6T：`HiFi` 和 `Speaker`；镜像有 alsaucm 时由 Shell 的 audiod 设，没有时用 Nura 的 `alsaucm -c hw:0 set _verb HiFi set _enadev Speaker`；记下来给 `teardown`；哪里都没有 alsaucm 时说 phone-audiod 放不出声音、怎么装：`apk add alsa-utils`）；照上面说的处理 `--firmware image`；ModemManager 没在跑时按底座的 init 系统说怎么起。整机另外：`phone-bringup-awake` 不让手机挂起时说一声 |
| `selftest` | 兼容性测试（`phone-shell --cts --keep-going`：自测加 CTS 用例，推送走 TLCP，和镜像的开机自测一样；一个用例失败不会让整次停下，[40](40-testing.md) §2），在 chroot 里或整机上（这一步里停掉面板上的 Shell 和声音服务，见上；扬声器调小，跑完调回），超时乘 `--timeout-scale`（默认 2），给了 `--only` 就只跑那些用例；在手机上分离运行，构建机每 20 秒问一次，中途连接断了不要紧（自测的「挂起」用例在能挂起的板子上让手机真的挂起一次） |
| `pixel` | 自测由 phone-paint 无头画四遍（同样 `--keep-going`，扬声器同样调小）：SKU 自己的面板（1080×2180 按 4.5 倍）和三种形态（`--device`），留下 PNG 和每遍的耗时 |
| `bench` | 语义页面和网页的对比（`bench/bench.py`，和 `yocto/qemu-bench.sh` 一样的命令，只是不关机）；要 python3，只有网页视图的镜像有（`make yocto-web-build`），默认镜像跳过并写明 |
| `collect` | 把手机上所有的结果取回 `build/phone/<SKU>/results/`，两条路的运行放在一起 |
| `all` | `push setup selftest pixel bench collect`：无人值守的那部分 |
| `shell-start`、`shell-stop` | （chroot）Shell 上真屏（`--gui auto`，`PHONE_PAINT_TRACE=1` 记动画的帧耗时），键来自键盘和手机的键，Shell 自己起的 telephonyd 用 SKU 的基带后端（ModemManager），它的 audiod 用板子的通话设置；有 elogind-inhibit（systemd 上用 systemd-inhibit）时拦下 logind 对电源键和霍尔传感器的处理；停的时候把各服务的日志一起留下。调试开关（bring-up switches）`--stay-on`（Shell 从不熄屏：背光和显示控制器一直开着）、`--no-haptics`（不振动）交给 Shell，只在这一次有效、不存进设置：`kario-phone.sh --stay-on --no-haptics shell-start`（[41](41-design-gaps.md) §13） |
| `mem` | `shell-start` 60 秒以后，每个我们的进程的 PSS 和总数：chroot 里是根在 chroot 里的、cgroup 里的和它们的子孙；整机上是 Kario 的单元的、应用的 scope 的和套件步骤的 |
| `power-start`、`power-stop` | 待机前后记电池（电量计的 `charge_now`、`energy_now`、电量百分比、挂起次数），算平均电流和功率，列出唤醒最多的源（debugfs 挂着时）；`power-stop` 另打 `suspend_stats`（成功、失败、最后失败的设备和步骤）和 Shell 这段时间里的 `sleep:` 行（每次醒来的原因） |
| `dump` | Shell 和渲染器各写一份此刻的状态（`kill -USR1`），取进这次的结果（`dump-<时刻>.json`、`paint-<时刻>.json`）：和安卓按键机比较时数两次之间的按键和触摸（[40](40-testing.md) §9） |
| `power-sample[=秒]` | 每秒记一次 `current_now`（亮屏时的电流） |
| `teardown` | 两条路都先把 `selftest`、`pixel` 调小后没调回的混音调回去。chroot：结束我们的所有进程，删掉 Shell 留在 Nura 内核里的 nftables 表、USB gadget 和 networkd 文件，用设的时候那个 alsaucm 拆掉 `setup` 记下的 UCM 设置（`... set _disdev Speaker set _verb Inactive`），放回固件路径，卸载全部挂载，删 cgroup，切回原来的控制台，启动 `setup` 停掉的服务；会话里结束掉的程序下次登录或重启时回来，声卡别的混音设置重启才复原。整机：结束套件的步骤，启动步骤停掉的单元。`--purge` 连套件、状态、结果一起删（chroot 还有镜像） |

**分离运行的步骤**（`selftest`、`pixel`、`bench`；`on-phone.sh bg`、`status`、`log`；两条路一样）：步骤在自己的会话里跑，有 systemd 时还在自己的 scope 里（`systemd-run --scope`；整机上是委派的），SSH 登录结束时 logind 不会把它一起结束；它一开始就记下自己的 pid，不管怎么退出都写退出码，收到挂断或终止也记下，`status` 从不新开一次运行。`status` 既找不到退出码、也找不到步骤在跑时，说 `gone` 并打出手机上看到的（状态目录、每个 pid 文件和它的进程在不在、结果目录、相关进程的会话和 cgroup、logind 的 `KillUserProcesses`），`kario-phone.sh` 把这些和步骤输出的最后几行一起打出来（[41](41-design-gaps.md) §12.3）。`on-phone.sh scrub` 照 `facts` 的做法从任何文件里抹掉序列号、基带的身份和硬件地址。`on-phone.sh partitions` 列出手机上的分区、每个的字节数和板子文件给的角色，再列出板子文件里有、手机上没有的（`facts` 里也有这一节；A/B 布局上手机前要它，[43](43-phone-partitions.md) §3.2）。

**Nura 留着什么，我们的服务怎么连上**（chroot）：Nura 的 D-Bus、ModemManager、rmtfs、pd-mapper、tqftpserv（基带的那一套）、NetworkManager（SSH 经 USB 网络或 Wi-Fi）、bluetoothd、sshd 都留着。我们的 telephonyd 是 ModemManager 的客户端，系统总线上的 ModemManager 是 Nura 的（板子自己的镜像里也有一个，在这里不跑），所以不能停 Nura 的：`/run` 绑定进 chroot，我们的进程就在 Nura 的系统总线上（`/run/dbus/system_bus_socket`），telephonyd 以 root 调 ModemManager。chroot 里没有 systemd，Shell 自己起服务（`--services spawn`）：交互运行时它起的 telephonyd 用镜像 SKU 的基带后端（`modemmanager`），audiod 用板子的通话设置，自测和脚本照旧用模拟基带（它们拨的是模拟网络里的号码；`programs/shellcmd` 的 `privateModem` 和 `privateAudio`，单元测试 ✓）。Wi-Fi 归 Nura 的 NetworkManager，我们的 iwd 不起，设置里的 Wi-Fi 在 chroot 里用不了；移动数据：telephonyd 照样用接入点数据库经 ModemManager 连承载，但 chroot 里没有 systemd-networkd，Shell 不给移动网卡配地址，Nura 的 NetworkManager 可能也会自己去连基带的承载、配网卡，两边会不会抢**未核实**；蓝牙的设置连的是 Nura 的 bluetoothd（**未核实**）。Shell 发现 USB 控制器已经被别的 gadget 占着（Nura 的 USB 网络，整机上 phone-bringup 的）就不碰 USB：不建自己的 gadget，不加 USB 的防火墙（`shell/usb.go`，单元测试 ✓，去掉这层测试会失败）。

**你本人要先做的**：
1. 买手机：一加 6T（或 6）；红米 Note 4X 高通版（mido，原装屏，3 GB 或 4 GB 内存都行）。
2. 解锁：一加先把 OxygenOS 升级两次（两个槽都到最新），开发者选项里打开 OEM 解锁，`fastboot oem unlock`；红米申请小米的解锁，用 Mi Unlock 解。
3. 刷 Nura 之前先备份（[39](39-oneplus-6t.md) §3.5：TWRP 只在内存里跑，拷出 IMEI 和校准数据；一台 OxygenOS 11 的 6T 上 TWRP 起不来，改在 Nura 启动镜像的调试 shell 里备份，见 [39](39-oneplus-6t.md) §9.1）。刷 Nura：按[安装页](https://nura.eco/install/)下载 v26.06 的镜像（一加 6、6T 各用自己的，4X 用 Generic MSM8953），或者用 pmbootstrap 自己做；界面选「console」（没有图形界面，就不用停显示管理器），装上 SSH 服务；一加还要 `fastboot erase dtbo`。
4. 开 SSH：把构建机连开发板的公钥（`kario-board-test`）加到手机 root 的 `authorized_keys`，`PermitRootLogin prohibit-password`；或者用默认用户加 `--become doas`（doas 要免密）。
5. 连网：USB 线插构建机，Nura 的 USB 网络里手机是 `172.16.42.1`（套件的默认）；再在 Nura 里用 nmcli 连上 Wi-Fi：键盘占了 USB 口、或者量待机电流要拔掉 USB 时，经 Wi-Fi 用 `--phone root@<地址>`。
6. 键盘：蓝牙键盘在 Nura 里用 bluetoothctl 配对（之后 chroot 里的 phone-paint 直接读到它）；一加上 OTG 键盘用不了（同一份设备树，USB 只能当设备），4X 的 OTG **未核实**。
7. 构建：用这棵树构建板子自己的镜像（6T：`make yocto-build WORK=$HOME/yocto/fajita-work KASFILE=yocto/kas/phone-fajita.yml SKU=oneplus-fajita-t9`，[39](39-oneplus-6t.md) §4），chroot 和整机都用它；QEMU 的（`make yocto-build`）拿来对比；要跑网页视图的基准再 `make yocto-web-build`。

### 6.3 测试计划和要记的数（都**未跑**）

| # | 测什么 | 怎么测 | 记什么 | 对照 |
|---|--------|--------|--------|------|
| 1 | 事实 | `setup` 里的 `facts` | 每台的 `facts.txt`：输入设备的名字和键位、DRM 的连接器和模式、背光、LED、电源、IIO、`/sys/power`（加上 `pm_wakeup_irq`、挂起失败的记录、每个按键唤醒源的设备的 `power/wakeup*` 和它的中断数）、RTC 的计数（`since_epoch`、`hctosys`）和唤醒、看门狗和上一次启动怎么结束的（journal 里看门狗、关机、重启的行，pstore 里内核控制台的最后几行）、UDC、声卡和 UCM（加上混音控件和 `quiet_controls` 的当前值）、基带那一套的进程、内核的 Landlock、seccomp、cgroup；逐项核对板子文件里 `unverified` 的值，确认了就从列表里删掉 | 板子文件 |
| 2 | 文本自测 | `selftest` | 通过、失败、跳过的用例数（JUnit）和总耗时；chroot 里跳过或失败的（没有 systemd 的 fd 存储和应用作用域，没有 iwd，VPN 没有 networkd）逐个列出 | QEMU 上的开机自测（约 20 分钟、180 个用例）、开发机（约 100 秒） |
| 3 | 像素自测 | `pixel` | 四遍各自的耗时和 `PHONE-GUI-FRAMES`；拿 SKU 面板那遍的 PNG 看排版（240 宽的逻辑尺寸，行更多） | 开发机的 `make pixel-selftest` |
| 4 | 真屏 | `shell-start`，用键盘走 [13](13-prototype.md) §4.1 的建议路线 | 从启动到 `PHONE-GUI-READY` 的秒数；`gui.log` 里动画的帧耗时（p50、p95、晚了的帧）；留黑的行够不够（状态栏避开刘海、软键避开圆角） | 4X 对 6T：A53 对骁龙 845 |
| 5 | 内存 | `mem` | 进程数、每个进程的 PSS、总数、`MemAvailable` | QEMU 上 23 个进程共 234.0 MB（其中模拟的云端服务 44.6 MB） |
| 6 | 打电话、发短信 | `shell-start` 以后拨出、接听、挂断、发收短信、SIM 卡 PIN、紧急号码 | 每项通不通；telephonyd 的日志里对 ModemManager 的调用和错误；通话的声音（要在 Nura 里把 UCM 切到「Voice Call」，我们的 audiod 还不会切） | `make mm-selftest` 的 39 个用例（模拟的 ModemManager） |
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

**整机类主板（`class = "phone"`，原型：生成器、Shell、渲染器的单元测试 ✓；一加 6T 的板子 2026-11-21 起在真机上用过，chroot 和整机都有，[39](39-oneplus-6t.md) §16；另外两台**未上真机**）**：主板就是整部手机，屏、触摸屏和按键是它自己的，键盘接 USB 或蓝牙。三台的板子文件是 `sku/boards/oneplus-fajita.toml`、`oneplus-enchilada.toml`、`xiaomi-mido.toml`，SKU 是 `oneplus-fajita-t9`、`oneplus-enchilada-t9`、`xiaomi-mido-t9`（九宫格形态，2 GB 内存档，标准版）。板子的字段：

| 字段 | 意思 | 谁用 |
|------|------|------|
| `class = "phone"` | 整机：SKU 不写屏和键盘；没有接线、叠加层和 Yocto 机型 | `tools/sku.py` |
| `compatible` | 设备树根节点的 compatible | 套件核对手机是不是这块板 |
| `nura` | Nura 的设备名（`oneplus-fajita`、`qcom-msm8953`） | 文档、`phone.env` |
| `[display]` 的 `width`、`height`、`width_mm`、`height_mm` | 面板的像素和物理尺寸（毫米数来自设备树或驱动的模式） | 生成器核对长宽比和字的大小 |
| `[display]` 的 `scale` | 每个逻辑像素几个物理像素 | `sku.json` 的 `scale`：Shell 排版、渲染器画 |
| `[display]` 的 `inset` | 上下各留黑多少行（刘海、圆角） | `sku.json` 的 `height` 减去两份；phone-paint 把画面放在面板正中，留黑的行就在上下（`kms::letterbox`） |
| `[display]` 的 `id`、`name`、`panel`、`touchscreen` | 屏的名字，面板、触摸屏的 compatible | `sku.json` 的 `display`、文档、`facts`；`touchscreen` 还有 `sku.json` 的 `touch`（渲染器读触摸屏），生成器核对它的驱动编进了机器的内核（§2） |
| `[input]` 的 `buttons` | 手机自带键的输入设备名 | `facts` 核对 |
| `[input]` 的 `sound_switch` | 硬件声音开关的输入设备名：三档的滑块（响铃、振动、静音，报 `ABS_SND_PROFILE`），或两档的静音开关（gpio-keys 的 `EV_SW` `SW_MUTE_DEVICE`：置位是静音，否则响铃；要是它自己的一个 gpio-keys 设备，不和音量键放在一起，渲染器按名字认它）；没有开关的板子不写；不是整机的板子 `[input]` 里只能写它 | `sku.json` 的 `sound_switch`：握手时交给渲染器，渲染器读它的位置报给 Shell（[20](20-audio.md) §4.1），自测的 `require-sound-switch` 看它；`phone.env` 的 `KARIO_SOUND_SWITCH`，`facts` 核对；生成器核对它是设备名、不是 `buttons` 里的键 |
| `[keymap]` | 自带键盘（KaiOS 机的矩阵）的键码 → `keys.toml` 的键名（下文） | `sku.json` 的 `keymap`：Shell 核对，握手时交给 phone-paint |
| `unverified` | 要上真机确认的值（字段或「表.字段」） | 生成器核对它们都存在；`facts` 指出去哪里看 |
| `modem`、`gnss`、`ims`、`gpu`、`power_key`、`[leds]`、`backlight`、`light_sensor`、`[audio]` | 和其他主板一样 | 同 §2、§3 |

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
| 声音开关 | `Alert slider`（源码，**待核实**） | 设备树里有，板子没写 | 没有 |
| 通知灯 | 没有 | `rgb:status`，绿（源码，**待核实**） | `green:indicator`（**待核实**） |
| 背光、电池、振动 | 自己找（主线里叫 `ae94000.dsi.0`、bq27411；振动只在 Nura 的内核） | 同左 | 自己找（主线 6.18 都没有，靠 Nura 的内核） |
| 基带、定位 | `modemmanager`，定位经 ModemManager（**待核实**）；`ims = "81voltd"`（**待核实**） | `modemmanager`，定位经 ModemManager（**待核实**）；没写 `ims`（还没有 Yocto 机器） | 同左；移动数据**未确认** |
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
| 电源键（`pm8941_pwrkey`） | 短按亮屏、息屏（闹钟响着时是稍后提醒），按住 600 ms 是电源菜单；没接键盘时音量键在菜单里移动、电源键选择（[18](18-nokia-form-factors.md) §5.1 的「怎么关机」） |
| 音量上、下（gpio-keys、`pm8941_resin`） | 音量（通话中是通话音量） |
| 触摸屏（`syna,rmi4-i2c`） | 第二种输入，换成按键：点一行是焦点加 OK，点软键栏是软键，上下拖移动焦点，从左边缘往右划是返回（[10](10-keyboard-interaction-model.md) §2.1） |
| 提醒滑块（gpio-keys「Alert slider」，6T） | 声音模式：上静音，中振动，下响铃（[20](20-audio.md) §4.1，**未上真机**） |
| 霍尔传感器 | 不用 |

**两个决定**：
- 音量键不兼作上下方向：电源键的短按要留着亮屏、息屏（待机电流就靠它），只有上下、没有 OK 和返回，什么也操作不了；通话时音量键要调听筒音量；蓝牙键盘闲一会儿就断开、一按又连上，键的意思跟着键盘在不在变，测试时更乱。交互测试接键盘，无人值守的那部分（自测、基准、待机）经 SSH，不用键。
- 触摸屏是第二种输入，只换成按键（2026-11-25 起；之前不读它）：每个手势都是一个已有的键（焦点加 OK、软键、方向键、返回），键盘导航还是唯一的路，它的毛病照样测得出来；6T 上不接蓝牙键盘也能翻页、打开和返回。打字和 PIN 还要键盘，键盘锁着时除了来电、闹钟不理触摸，通话中贴着耳朵时（没有距离传感器）除了响着的来电不理触摸，按音量键或按住屏幕一秒唤醒，触摸不点亮屏幕（[10](10-keyboard-interaction-model.md) §2.1）。

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
- audiod 切 UCM：手机的声卡要按场景（放音、通话、耳机）设混音。一加 6T 的整机镜像里写好了（[20](20-audio.md) §10，**未上真机**），别的手机要在设备配置里给它们的 `--ucm-*`；chroot 用的 QEMU 镜像里没有 alsaucm，也不带这些参数，还是靠 Nura 的 alsaucm 照板子的 `[audio]` 先设好 `HiFi` 和 `Speaker`（Shell 自己起的 audiod 放到 `[audio]` 的 `pcm`），通话的声音要手动切「Voice Call」。
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
| cgroup v2 | 统一层级 4.5 起；cpu 控制器 4.15；`memory.oom.group` 4.19；PSI 4.20；冻结器、PSI 触发器 5.2；`CLONE_INTO_CGROUP` 5.7；`cgroup.kill` 5.14 | 每个应用的内存上限、CPU 权重、冻结、低内存清理、顶在上限上卡住的应用（[17](17-app-sandbox.md) §5、§5.4） | 只有 v1 | 部分 | 开机时按功能探测。只有 v1 或混合层级、拿不到委派的组：不建应用组，冻结退回 SIGSTOP，没有每应用的内存、进程上限和 CPU 权重。有组时逐项看：没有 `cgroup.freeze` 就 SIGSTOP 应用的整个进程组；内存、进程、CPU 控制器缺哪个就没有哪一项；没有 `CLONE_INTO_CGROUP`，启动器停在管道上，Shell 把它移进组再放行；没有 `cgroup.kill` 就逐个 SIGKILL 组里的进程，直到组空；没有 `memory.oom.group`，内核的 OOM 只结束组里最大的进程；没有 `memory.pressure` 就只看可用内存，顶在上限上卡住的应用只能等内核的 OOM；有它没有触发器，卡住的应用由例行检查发现（息屏时每 5 秒） |
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
| 换一种做法，什么也没少，只记下来（报告的 `fallback`） | `seccomp-tsync`、`seccomp-kill-process`、`clone-into-cgroup`、`cgroup-kill`、`psi`、`psi-trigger`、`pidfd`、`mem-available` | 同样的保护用老办法做到；`seccomp-tsync` 的代价是 USB 文件共享用不了（它的助手不启动）；`psi` 的代价是顶在内存上限上卡住的应用要等内核的 OOM（[17](17-app-sandbox.md) §5.4），`psi-trigger` 的是息屏时最多晚 5 秒发现它 |

- 兼容测试照旧按用例和能力判 PASS / FAIL：缺了保护的设备照样可以 PASS，认证方看报告的 `sandbox.missing` 决定收不收；`phone-cts-verify` 把它们一项项列出来。没有这个字段的旧报告照样核对得过；旧版本的 `phone-cts-verify` 读新报告时丢掉这个字段，摘要对不上，核对不过，要用这一版。
- 和原来的建议不同：memfd 的封印归「如实报出来」，不归「不启动应用」：我们本来就不拿没封印的内存顶替，缺了它，声音、视频、相机和网页直接用不了，应用碰不到没封印的共享内存，不启动应用也换不来什么。
- 设备认证（应用调的 `attest`，钱包、支付用）还不带缺了哪几层（**未实现**）：签名的消息里只有启动状态（`verified-boot:…` / `unlocked`），加一项要换消息的版本，依赖方（钱包比的就是 `unlocked`）要一起改；现在带它的只有签了名的兼容测试报告。「设置」里也不显示（**未实现**），沙箱自检照旧把缺的层的检查项标「跳过」。
- systemd 的门槛降不了：要么换 init（Shell 的 fd 存储、应用作用域都靠 systemd，要重写），要么走 (b)。
- 验证：假的 cgroupfs 树（4.19、5.1（有 PSI，没有触发器）、5.4、5.10、6.18 和厂商的 v1 内存控制器）、注入老内核的系统调用错误（ENOSYS、EINVAL、EOPNOTSUPP）的单元测试，每个修了的行为都先确认改之前失败（`TestFreezeWithoutFreezer`、`TestStartWithoutCloneInto`、`TestKillWithoutCgroupKill`、`TestMeminfoWithoutMemAvailable`、`TestSeccompWithoutTSync`）；开发机自己的 4.18 内核没有 Landlock、pidfd 和 `clone3`，上面跑的是真的探测。QEMU 的主线内核上什么都不缺，自测用例「内核特性」核对（QEMU ✓，2026-11-27 夜间链 r86）。真的 4.x、5.x 厂商内核上**未上真机**。

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

## 8. 厂商的闭源部件

> 状态：原则和边界。已有的：6T 的固件在构建时下载、不进 git；镜像里没有 GPLv3；平台依赖的许可检查；内核的 lockdown 和模块签名的镜像检查；应用和助手的沙箱。**未实现**的：每个 SoC 一个的私有 BSP 层仓库、放闭源文件的制品库、闭源配方的 `LICENSE_FLAGS`、给厂商进程的沙箱策略、随产品镜像给出的 SBOM 和 GPL 源码（§8.6）。仓库怎么重组（SKU 放在哪、外部驱动怎么构建、BSP 的契约、多仓库和可复现、GPLv3、最后有哪些仓库）是 §8.7，决定了的设计，**未实现**。基带分成可移植的核心、Modem 后端和 SoC 胶水（§8.8），联发科的设计在 §8.9（**未实现**）。仓库叫什么、谁能看、厂商交来的文件怎么审、迁移怎么做，在内部文档 60。

Kario 上了量产的手机，芯片厂和方案公司总有些部件不给源码：协处理器（基带、DSP、GPU、Wi-Fi）的固件，可能有内核模块，还有用户态的程序和库（VoLTE 的 IMS、相机的调校、传感器融合）。本节定它们放在哪、怎么进镜像、能和什么说话、许可怎么隔开。

### 8.1 原则

- **任何 git 仓库里都没有闭源的二进制**（本仓库和内部仓库都一样）。闭源文件放在仓库外，构建时按地址加校验和取：bitbake 核对 `SRC_URI` 的 sha256（git 源是固定的提交），对不上就停。现在的例子：6T 的固件 `firmware-oneplus-sdm845`（`LICENSE = "CLOSED"`）构建时从 sdm845-mainline 公开的 git 仓库取，固定在提交 3e31a0c3；6T 内核的基础配置从 Nura 的 pmaports 按地址加 `sha256sum` 取（[39](39-oneplus-6t.md) §10）。手机上用的固件就来自这个包，Kario 不读手机自己的固件分区（[43](43-phone-partitions.md) §1）；手机分区的备份也在仓库外（[39](39-oneplus-6t.md) §9.1）。量产时厂商交来的文件放在一个有权限控制的制品库里（**未实现**）。`scripts/binary-check.py`（`make test`，`make publish` 推送前）拦住会推出去的程序（ELF、Mach-O、PE：`.ko` 也是 ELF）和大于 512 KiB 的文件；不是这几种格式的小固件文件它认不出来（**未实现**）。
- **闭源文件分三类**，能进的镜像不一样：

| 类 | 例子 | 能进的镜像 |
|----|------|-----------|
| 有许可文本、允许再分发 | linux-firmware 里 Adreno 630、WCN3990 的文件 | 开源镜像和产品镜像，许可文本随着 |
| 按合同从厂商拿的 | 产品板的基带、DSP 固件，厂商的用户态程序（现在都还没有） | 只进产品镜像；文件在私有的 BSP 层和制品库里（§8.2），再分发的权利写进采购合同 |
| 没有给我们许可的 | 6T 的 `firmware-oneplus-sdm845`：取自社区的仓库 | 只进机主自己测试用的镜像，不发布、不转给别人（[39](39-oneplus-6t.md) §10） |

- **厂商不给再分发的权利时**还有一条路：固件留在工厂写进的固件分区里，镜像不带，开机时只读挂上那个分区、从那里加载（§7.6 说的做法）。这样每块板子的固件跟着工厂走，系统更新换不了它；**未实现**。
- **接口开放，实现在接口后面**：闭源部件是一个单独的东西（协处理器里的固件，或者一个单独的进程），Kario 只经 Linux 的标准接口或 Kario 定义的接口和它说话（§8.4）；Shell、系统服务、渲染器从不链接闭源的库。
- **少了一个闭源部件，只少它那一项功能**：照 SKU 说的有没有来降级，和现在一样：板子不写 `nfc`，phone-nfcd 直接退出；`fingerprint = "none"` 就没有指纹；6T 没有传感器的用户态（§8.3），就没有自动亮度和贴脸息屏，别的照常。
- **开源镜像不带按合同拿的部件**：本仓库自己就能构建完整的开源镜像。QEMU 上什么都不缺；主线的板子（kario-qcom）上没有各子系统固件的那几样（基带、GPU、Wi-Fi、DSP）用不了（§5）；6T 的镜像带着第三类的固件，只给机主自测。

### 8.2 三层

| 层 | 在哪 | 放什么 | 状态 |
|----|------|-------|------|
| 公开仓库 kario | 本仓库 | 接口：`hal/`、`svc/`、telephonyd 的 `Modem` 后端、渲染器协议；SKU 框架（`sku/`、`tools/sku.py`）；Yocto 层 `meta-phone`：开源的配方、Kario 自己写的胶水（`phone-qcom`）、内核配置片段（`phone-*.cfg`）、`phone-kernel.inc`（lockdown 和模块签名）；只取第三方已经公开发布的闭源文件的配方（6T 的固件，机主自测用） | ✓ |
| 每个 SoC / 厂商一个私有的 BSP 层仓库 | 内部（60（内部文档）） | 一个 Yocto 层，只有配方：机型、厂商内核的配方（源码的仓库和提交，`require` 本仓库的 `phone-kernel.inc`）、每个闭源部件一个配方（制品库里的地址加 sha256、厂商的许可文本和它的 `LIC_FILES_CHKSUM`）；没有二进制 | **未实现** |
| 产品配置 | 内部仓库的 kas 文件 | 按 SKU 选 BSP 层，固定它的提交；产品镜像只在这里构建 | 产品的 kas 文件 ✓（QEMU、6T 内测）；选 BSP 层**未实现** |

板子文件在本仓库（`sku/boards/`），它的 `machine` 可以是别的层的机型：`tools/sku.py` 在本层和给的 BSP 层（`KARIO_BSP_LAYERS`，§8.7.1）里都找不到那台机型的文件时，跳过只能对着机型文件做的检查（触摸屏的驱动编进内核、`[audio]` 和 `PHONE_AUDIO_*` 一致，§2），和 QEMU 的机型一样。6T 的高通部分现在就是这样的层，只是公开的（kario-bsp，§8.7.6）；本仓库的 kas 文件构建的机型不跳过：没给它的层时 `sku.py check` 和 `gen` 报错。

### 8.3 内核

- **内核和链进内核的模块是 GPL-2.0**：每个模块都要有源码，这一条写进采购合同（**未实现**：还没有采购合同）。我们发出去的内核按 GPL 给源码；6T 的内核是 sdm845-mainline 的公开标签加 Nura 的配置和本仓库的片段，本来就全是公开的。
- **好的情形：驱动开源，闭源的部分在协处理器的固件里**。6T 上：基带（remoteproc、QRTR；用户态的 rmtfs、tqftpserv 是 BSD）和 `modem.mbn`；Wi-Fi（ath10k）和 `wlanmdsp.mbn`；GPU（msm、freedreno）和 `a630_zap.mbn`；视频（venus）和 `venus.mbn`；音频 DSP 和 `adsp.mbn`。传感器也是这个样子：闭源的部分在 SLPI 的 `slpi.mbn` 里（固件包里有），可在用户态和它说话的 hexagonrpcd 是 GPL-3.0，不进 Kario，所以 6T 没有传感器（§6.1、[39](39-oneplus-6t.md) §11）：开源的那一半也要过我们的许可关卡（§8.5）。
- **只有二进制的模块只作最后的退路**。它只认一个内核版本（`vermagic` 和符号的 CRC）；要在 lockdown 下加载，就要用我们的模块密钥签名：镜像里每个 `.ko` 都要用构建的密钥签过，不然镜像检查 `modules-signed` 失败（[39](39-oneplus-6t.md) §8.3，✓）。密钥不交给厂商：厂商交来没签名的模块，构建时由我们签（给外来的 `.ko` 签名的配方**未实现**；本仓库内核自己的模块在内核安装那一步签）。内核每更新一次（包括长期支持版的小版本），厂商都要重新出一次，内核的安全更新就要等厂商；闭源许可的模块还会让内核带上污染标记（`P`），也用不了只导出给 GPL 的符号。这是选芯片时要算进去的风险（61（内部文档））。
- 签模块的密钥现在是每个构建目录自己的开发密钥（`yocto/ota-keys.sh`）；产品的模块密钥放进 HSM（内核的 `CONFIG_MODULE_SIG_KEY` 也收 `pkcs11:` 地址）**未实现**。签名是什么、外来的模块怎么签，见 §8.7.2。

### 8.4 用户态的闭源部件

例子：VoLTE 的 IMS（高通的在基带固件里，[39](39-oneplus-6t.md) §11.1；展锐的经它自己的 HAL 扩展，是用户态的程序）、相机的调校（3A、ISP 的参数）、传感器融合。规则：

- **单独的进程**，自己的 uid，进沙箱：命名空间、seccomp、Landlock，只给它要的设备节点，用不着网络就放进空的网络命名空间。现在有的：`phone-sandbox` 的应用策略和助手策略（[17](17-app-sandbox.md) §2）；助手策略不给任何设备、不许新建套接字，给厂商进程的策略**未实现**；系统服务自己的 systemd 单元也还没有这些（以 root 运行）。
- **只经这几种接口和 Kario 说话**：Linux 的标准接口（IIO、V4L2 和媒体控制器、ALSA 和 UCM、evdev、DRM/KMS、GNSS 的 `/dev/gnssN`、电源类、LED 类：`hal/` 和各服务现在用的就是这些）；规范公开的 D-Bus 协议（telephonyd 和 ModemManager 之间就是）；Kario 定义的 Unix 套接字协议（系统服务的 `wire` RPC，[11](11-platform-architecture.md) §14）。厂商安卓 BSP 的 HAL 经 binder：telephonyd 的后端用开源的 libgbinder 讲，不加载厂商的库（**未实现**，[11](11-platform-architecture.md) §16）；非要加载链接 bionic 的厂商库时，放进一个单独降权的 HAL 宿主进程（§7.4，**未实现**）。
- **Shell、系统服务、渲染器从不链接、也不 `dlopen` 闭源的库**：闭源部件崩了只少它那一项，它带进来的漏洞也只在它自己的进程里。GPU 的用户态驱动是装进画图进程里的库，所以闭源的（比如 Mali 的 libmali）也不用：渲染器用 CPU 画（现在的默认）或者用开源的 Mesa 驱动（[42](42-gpu-rendering.md)）。
- **先例**：libcamera 把不是随它一起构建、签名的调校模块（IPA，可以是闭源的）放进单独的隔离进程，经 IPC 调用（Kario 还没接 libcamera，**未实现**）。
- **只给它这项功能要的东西**：比如相机的调校只拿帧和统计、设传感器的控件，拿不到应用的数据和密钥。
- 现在的镜像里没有任何闭源的库或程序：6T 上闭源的只有固件。

### 8.5 许可的关卡

| 关卡 | 做什么 | 状态 |
|------|-------|------|
| `INCOMPATIBLE_LICENSE = "GPL-3.0* LGPL-3.0* AGPL-3.0*"`（`yocto/kas/phone-qemuarm64.yml`，6T 和产品的 kas 文件都引入它） | 镜像里没有 GPLv3。6T 那一套里 GPL-3.0 的部件换成我们自己写的：qbootctl → `phone-abslot`，msm-modem 的 UIM 选择 → `uim-select`，bootmac → `phone-btaddr`，callaudiod → phone-audiod 的 UCM 通话路由；hexagonrpcd 没有替代，所以没有传感器（[39](39-oneplus-6t.md) §10） | ✓ |
| `LICENSE_FLAGS` | 现在只接受 ffmpeg 的 `commercial_ffmpeg`（专利的标记，和闭源无关）；`CLOSED` 不受 `INCOMPATIBLE_LICENSE` 管，6T 的固件进镜像不用任何标记。打算：每个闭源的配方带一个自己的标记（如 `vendor-<厂商>`），只有构建那台手机的 kas 文件接受它，开源镜像就不会误收。不用 `commercial`：它说的是专利，接受了它就接受了所有带它的配方 | **未实现** |
| `scripts/license-check.py` | 平台的 Go 依赖、phone-paint 和原生模块 crate 的 Rust 依赖都是宽松许可证（`make test`、CI）；不看镜像里装了什么 | ✓ |
| `scripts/yocto-check.py` | 每个配方有 `LICENSE`，除了 `CLOSED` 和镜像都有 `LIC_FILES_CHKSUM`。BSP 层的闭源配方写厂商的许可证名和许可文本（不写 `CLOSED`），许可文本就随镜像走、也查得到 | 本层 ✓；BSP 层：`yocto-check.py --layer <层>`（2026-11-27，kario-bsp 的 `make test` 对它的层跑） |
| 镜像检查 `modules-signed` | 每个模块都用构建的密钥签过、内核认得（§8.3） | ✓（QEMU；6T **未上真机**） |
| SBOM | Yocto 构建时按默认写 SPDX（oe-core 的 `create-spdx`）；收进发布、随产品镜像给出 | **未实现** |
| 固件的再分发 | 产品要用的每个按合同拿的文件，再分发的权利写进采购合同 | **未实现**（还没有合同） |
| GPL 的源码 | 产品镜像里的内核和 GPL、LGPL 组件按许可给源码（随附或书面提供）；Kario 自己的代码在发布的标签上本来就公开 | **未实现** |

### 8.6 还没做的

- 私有的 BSP 层仓库、制品库和厂商交付的审查（60（内部文档））。
- 闭源配方的 `LICENSE_FLAGS`；`firmware-oneplus-sdm845` 也带上一个，由 `yocto/kas/phone-fajita.yml` 明确接受。
- 认得出固件文件的检查（`scripts/binary-check.py` 只认程序和大文件，§8.1）。
- 仓库的重组（§8.7）：6T 的高通部分搬进自己的 BSP 层、BSP 的契约、发布记下每个仓库的提交。
- 给厂商进程的沙箱策略和 systemd 单元（自己的 uid、seccomp、Landlock、只给要的设备）；系统服务自己的单元也降权。
- 给外来的 `.ko` 签名的配方；产品的模块密钥（HSM）。
- SBOM 收进发布；GPL 源码的提供。
- 厂商安卓 HAL 的 binder 后端和 HAL 宿主进程（§7.4、[11](11-platform-architecture.md) §16）。
- 闭源的传感器程序要先有 Kario 定义的协议才接得上（`hal/sensor` 只读 IIO）。
- 基带：联发科的后端和胶水（§8.9，**未实现**）；通话声音的胶水改成跟着 telephonyd 的通话、`phone-qcom-audio` 搬出高通的包、telephonyd 的单元不再写死 ModemManager（§8.8 末尾）。

### 8.7 仓库的组织（决定了的设计；搬家 2026-11-27 做了，**未验证**）

负责人问的七个问题的答案。每一条都是定下来的设计。做了的：SKU 的第二个搜索路径（§8.7.1，2026-11-26）；2026-11-27 的窗口里，BSP 层的搜索路径（§8.7.1）、契约的包名和 `yocto-check.py --layer`（§8.7.3），6T 的高通部分从 `yocto/meta-phone` 搬进公开的 [kario-bsp](https://github.com/kario-os/kario-bsp)（§8.7.6）。kario-bsp 2026-11-27 起发布在 GitHub 上，`phone-fajita.yml` 固定的是它的一个真的提交（`commit:` 那一行），`make test` 和 CI 也按这个提交取它（§8.7.1）。完整的构建和测试（内部文档 60 的四条链）2026-11-27 跑过，都通过（夜间链 d99），QEMU、产品和 6T（连同 A/B）镜像的包清单和搬家前的一样；之后的夜间链 r86、d100、r87、d101、d102、d103 每次都从固定的 kario-bsp 构建 6T 的镜像，模块签名的核对都通过。搬家后的 6T 镜像只构建过，**未上真机**：到报告 008 为止上过真机的都是搬家前构建的镜像。还没做的：生成的 SKU 文件放到工作树以外（§8.7.1）、发布记下每个仓库的提交和 SBOM（§8.7.4）、产品的模块钥匙（§8.7.6）；产品的构建照旧从旁边检出的 kario 取。

#### 8.7.1 SKU 放在哪

- **SKU 文件只写硬件事实和功能开关**：哪块板、屏、键盘、内存档、地区、产品层（§1），板子上有什么（`modem`、`gnss`、`nfc`、`fingerprint`、`ims`、`[audio]`、`[partitions]`……）。开关的值是 `tools/sku.py` 认识的那几个：Kario 的一个接口，或者一个开源的实现（`ims = "81voltd"`）。
- **不写进 SKU 的**：哪个厂商配方实现这个开关、制品库的地址和校验和、用哪个 BSP 层。这些归 BSP 层的机型和产品的 kas 文件：机型把开关换成自己的配方（就像 `oneplus-fajita.conf` 按 `PHONE_IMS = "81voltd"` 装 `81voltd phone-qcom-pdc`），产品的 kas 文件选 BSP 层、固定它的提交（§8.2）。闭源的实现需要一个开关值时，`tools/sku.py` 加一个不指名实现的值（「BSP 层按契约提供」，§8.7.3），不加厂商配方的名字。
- **所以产品宣布以后它的 SKU 就公开**：一个 SKU 打开了闭源的驱动，SKU 文件里也只有「有相机」「有 IMS」这样的事实，没有厂商的东西。产品宣布以后，它的板子、屏、键盘、产品层和 SKU 进本仓库的 `sku/`，和现有的一样过 `make test` 里的 `sku.py check`。
- **还没宣布的产品**的这些文件放在内部仓库里一个结构相同的 `sku/` 目录，宣布那天整体搬进本仓库。`tools/sku.py` 的第二个搜索路径（2026-11-26 做了，`tools/sku_test.py` 的 `SKUPathTest`）和构建流程要改的：
  - **环境变量 `KARIO_SKU_PATH`**：一串目录，用 `:` 隔开，每个的结构和 `sku/` 一样（`boards/`、`displays/`、`keypads/`、`products/`、`skus/`），排在本仓库的 `sku/` 后面；内部仓库的 Makefile 设它。`load`、`names` 在所有这些目录里找，SKU 和它用的板子、屏、键盘、产品层可以各在一处（内部的 SKU 用本仓库的屏和键盘）。同一个 id 两处都有就报错，不让一个遮住另一个；目录不存在、写了两次、就是本仓库的 `sku/`，或者里面有 `keys.toml`，都报错：键码表只有一份，在本仓库。
  - `sims` 写的 `cmd/kario/skus.json` 进 git、随 SDK 公开：只写本仓库的 SKU（`names("skus", own=True)`），设了 `KARIO_SKU_PATH` 也一样，测试核对写出来的和不设时一字不差。Makefile 的 `make skus`、`sku-<名字>` 目标读 `sku/skus/*.toml`，只列本仓库的。
  - 板子的设备树内核源码里没有（产品自己的树，由 BSP 层的内核配方加进去）、板子的机型又不是本层的：标签的核对跳过，由那个构建去查；本层的机型照旧报错。
  - `gen` 把 `sku.json`、`sku.yml` 写进本仓库的工作树（`yocto/meta-phone/recipes-phone/phone-sku/files/`、`yocto/kas/generated/`）。这两处都在 `.gitignore` 里（测试用 `git check-ignore` 核对），`make publish` 推不出去；可发布的构建改成按提交从 git 取 kario 以后（§8.7.4），git 里取出的那份没有这些文件，所以生成的文件要放到工作树以外（构建目录），`phone-sku` 配方和产品的 kas 文件从那里读。
  - 对着机型文件的检查（触摸屏编进内核、`[audio]` 和 `PHONE_AUDIO_*` 一致、按 `PHONE_IMS` 装 IMS、引导程序管槽时的 `phone-abslot`，§2、§8.7.3）在本层和 BSP 层里找机型文件（2026-11-27 做了）：**环境变量 `KARIO_BSP_LAYERS`** 是一串 BSP 层的目录（`:` 隔开，每个要有 `conf/layer.conf`，不能写两次、不能是 meta-phone），同一台机型两处都有就报错；BSP 层的内核配方取它旁边的和 meta-phone 的配置片段。本仓库的 kas 文件构建的机型（`yocto/kas/*.yml` 的 `machine:`，比如 6T 的）哪个层都没有时，`sku.py check` 和 `gen` 报错，不悄悄跳过；别的板子的机型找不到才跳过（§8.2）。`sku.py check` 列出查过的机型文件。Makefile 给它 kario-bsp 的层：`make test`、`make sku-check` 按 `phone-fajita.yml` 固定的提交把 kario-bsp 取进 `build/kario-bsp`（`make bsp-fetch`、`scripts/kario-bsp.py`，取不到、固定的提交 main 上没有了都失败），`KARIO_BSP=../kario-bsp` 换成自己的检出（[38](38-contributing.md) §11）。测试是 `tools/sku_test.py` 的 `BSPLayersTest`。
  - 内部仓库的 `make biz-check` 用两个 SKU 路径和 kario-bsp、私有 BSP 仓库的层跑 `sku.py check`；本仓库的 `make test` 只查自己的 SKU。
  - 还没做的：上面生成的文件放到工作树以外。

#### 8.7.2 外部的驱动：三种来源，怎么构建

都经 BSP 层进镜像：BSP 层的仓库只有配方（§8.2），源码和文件在别处，配方按固定的版本取。

| 来源 | 放在哪 | 配方 |
|------|-------|------|
| 内核的驱动，有源码 | 厂商的内核树（一个私有的内核仓库），或者厂商单独给的模块源码仓库 | 厂商内核：`SRC_URI` 指内核仓库，`SRCREV` 写全 40 位的提交，`require` 本仓库的 `phone-kernel.inc`。树外的模块：一个 `inherit module` 的配方，`SRC_URI` 指模块源码的固定提交，对着镜像里的那个内核编，版本和 `vermagic` 自然一致 |
| 用户态的程序，有源码 | 厂商的 git（我们在私有仓库里镜像一份）的固定提交 | 普通的配方（meson、cmake……），装成一个单独的、进沙箱的进程（§8.4） |
| 只有二进制 | 制品库，按 sha256 寻址（§8.1） | `SRC_URI` 是制品库的地址，`SRC_URI[sha256sum]` 核对，只有 `do_install`；只有二进制的 `.ko` 装进内核的版本目录、安装时签名（下面），它的 `vermagic` 和镜像的内核对不上就停下 |

- 每个闭源配方写厂商的许可证名和许可文本（`LIC_FILES_CHKSUM`），不写 `CLOSED`；`LICENSE_FLAGS = "vendor-<厂商>"`，只有构建那个产品的 kas 文件接受它（§8.5）。
- **模块签名是签名，不是加密**。构建时用 SHA-512（`CONFIG_MODULE_SIG_SHA512`）算出模块文件的摘要，用私钥对摘要签名，签名（一个 PKCS#7 / CMS 结构）接在 `.ko` 文件的末尾；内核里只有对应的公钥（构建时编进内核的那张证书）。加载时内核重算摘要、用公钥验签名：没有签名、签名的钥匙内核不认识、签过以后文件改了一个字节，都验不过，lockdown（integrity）下内核就拒绝加载（[39](39-oneplus-6t.md) §8.3）。模块本身原样不动：谁拿到文件都能读、能反汇编；签名只证明「有私钥的人签过它，之后没改过」，不隐藏任何东西。签名的工具是内核源码树里的 `scripts/sign-file`，碰巧用的是 OpenSSL 的 libcrypto（所以内核配方要 `openssl-native`）。私钥放进 HSM 以后，`CONFIG_MODULE_SIG_KEY` 写一个 `pkcs11:` 地址，`sign-file` 经 OpenSSL 让 HSM 签，私钥不出 HSM（我们的内核和 OpenSSL 版本上**未验证**）。
- 本仓库的内核自己的模块在内核安装那一步签（现在就这样）。树外编的模块和只有二进制的 `.ko`：内核的 `modules_install` 要私钥才签，OE 交给别的配方的那份内核构建目录里有没有私钥**未核实**，所以由 meta-phone 里一个公共的类在安装后用 `sign-file` 和同一把钥匙签，镜像检查 `modules-signed` 再核对一遍。钥匙从不交给厂商。

#### 8.7.3 BSP 的接口：要抽象，抽象在哪

**要抽象，就抽象在现在已经在用的 Linux 标准接口上**，再加几个 Linux 没有、Kario 自己定的小接口；不另造一层 HAL。Shell、服务、渲染器对着这些接口写，BSP 层负责让它们在那块板上成立。一个 BSP 层要提供的东西（「BSP 契约」）：

| 项 | BSP 层要给什么 | 谁核对 |
|----|--------------|-------|
| 机型 | `conf/machine/<机型>.conf`：调优、内核、设备树、`MACHINE_FEATURES`、镜像格式；ABL 一类的引导程序要的启动镜像参数（`phone-bootimg`） | bitbake 解析；`tools/sku.py check`（带上 BSP 层的路径，§8.7.1） |
| 内核 | `require phone-kernel.inc`：lockdown、模块签名；Kario 的配置片段一条不少（`phone-*.cfg`：沙箱要的 Landlock、seccomp、cgroup v2、memfd、命名空间，系统更新要的 dm-verity、squashfs、nbd） | 内核配方的配置检查（像 `linux-sdm845` 的 `do_phone_kconfig`）；镜像检查 `modules-signed`；开机后的 `module-check.sh` 和 Shell 的内核探测（§7.5） |
| Linux 的标准接口 | 显示 DRM/KMS，按键 evdev，声音 ALSA 和 UCM，传感器 IIO，相机 V4L2 和媒体控制器，电源类、LED 类、背光类，RTC，看门狗，GNSS 的 `/dev/gnssN`，Wi-Fi（nl80211，iwd 用）、蓝牙（HCI，BlueZ 用），USB gadget（configfs）和 Type-C 类，协处理器（remoteproc 和固件加载） | 自测和兼容测试（`phone-shell --cts`）在这块板上跑 |
| 板子的值 | 机型的变量和板子文件一致：`PHONE_AUDIO_*` 就是 `[audio]`，按 `PHONE_IMS` 装 IMS，触摸屏的驱动编进内核；分区表和刷机脚本 | `tools/sku.py check` |
| 基带后端 | telephonyd 的 `Modem` 后端要的东西 | 基带的契约另写（§8.8，安卓 HAL 的路见 [11](11-platform-architecture.md) §16），这里不重复 |
| 运营商配置 | 按 SIM 卡激活基带里的运营商配置（高通：`pdc-select`） | 同上 |
| 通话的声音通路 | 板子 `[audio]` 的通话设置（UCM 的通话 verb 和设备）；有通话时把基带的语音接到声卡的守护进程（高通：`q6voiced`） | 通话的触发同上；UCM 的值由 `tools/sku.py check` 查 |
| A/B 槽的切换 | 引导程序管槽的板子（`[partitions]` 的 `slots = "abl"`）：一个读写槽的程序（`slots`、`set-active`、`get-state`、`mark-good`、`mark-unbootable`；高通是 `phone-abslot`），RAUC 的引导程序后端 `/usr/lib/phone/rauc-abl`（`get-primary`、`set-primary`、`get-state`、`set-state`、`get-current`），initramfs 把这次启动的槽写进 `/var/lib/slots/boot`。`slots = "state"` 的板子不用 BSP 管 | kario-bsp 的 `tests/abslot-test.py`、`tests/rauc-abl-test.py`；本仓库的 `tools/slots_test.py`（initramfs 读的 `slot-abl`）；`tools/sku.py check`：`slots = "abl"` 的板子，它的机型所在的层里要有配方 `RPROVIDES` `phone-abslot`；系统更新的测试 |
| 许可 | 闭源配方写厂商的许可证名和文本、`LICENSE_FLAGS = "vendor-<厂商>"`；没有 GPLv3（§8.7.5） | `scripts/yocto-check.py --layer <层>` 对 BSP 层跑（kario-bsp 的 `make test`）；`INCOMPATIBLE_LICENSE` |
| 闭源的进程 | 自己的 uid、沙箱、只给要的设备（§8.4） | 交付审查（内部文档 60） |

- **符合契约，看的是自测和兼容测试**：同一份 `selftest.script` 和 CTS 用例在每块板上跑，加上上表构建时的检查；CTS 报告的 `sandbox` 字段写出内核少了什么（§7.5）。
- `phone-initramfs.bb` 在 `PHONE_SLOT_KEEPER = "abl"` 时要的是不指名厂商的 `phone-abslot`，高通的 `phone-qcom-abslot` 包 `RPROVIDES` 它（2026-11-27 做了；`tools/sku_test.py` 核对 initramfs 不指名厂商、6T 的层提供它）。

#### 8.7.4 多个仓库和可复现

- **现在**：kas 管所有上游的层，每个都写全 40 位的提交（`phone-qemuarm64.yml`：bitbake、openembedded-core、meta-rauc、meta-openembedded、meta-yocto；`phone-fajita.yml`：meta-qcom、meta-lts-mixins），新的构建目录拿到的是同一套。kario 自己在 kas 里只是一个本地路径（`repos.kario` 没有 `url`）；产品的构建（内部仓库）也从旁边检出的 kario 取（`KARIO=...`，kas-container 里是 `/kario`）。所以一个镜像由哪几个提交拼成，镜像里只有 `share/phone/source.txt` 记着（kario 的提交、有没有改动），发布另记两个仓库的提交（内部文档 60）。
- **计划**：
  1. 发布的构建按 URL 加 40 位的提交（发布标签的那个）取 kario，不用本地路径；平时开发照旧用本地路径。前提是生成的 SKU 文件不再写进 kario 的工作树（§8.7.1）。BSP 层同样写全提交（§8.2）。
  2. kas 的锁文件：现在用的 kas 是 5.5，按它的源码（没有运行）：`kas lock <文件>` 在第一个配置文件旁边写 `<名字>.lock.yml`，以后 kas 自动用它，`kas lock --update` 更新；`kas dump --lock` 是同一件事的旧入口。它只锁 kas 自己检出、只写了分支或标签的仓库，本地路径的仓库（kario）从不锁。我们的上游层都已经写了提交，锁文件现在一个也不多锁：**不用锁文件**，提交照旧直接写在 kas 文件里（只有一处，改它就是 PR 里看得见的一行）。
  3. **发布记下每个仓库的提交**：`kas dump --resolve-refs --resolve-local` 把展开后的整份配置写出来，每个仓库带 URL 和提交，本地路径的仓库也写上它检出的那个提交（有没提交的改动时警告）；这份文件存进发布目录，它本身就能交给 kas 再构建一次。产品镜像里的 `source.txt` 也写上所有仓库的提交。
  4. **SBOM**：oe-core 默认写的 SPDX（`create-spdx`）收进发布、随产品镜像给出（§8.5）。BSP 层的闭源配方在里面是带厂商许可证名的包；随镜像给出的那份不写制品库的地址。
- **结论：不用 Google 的 `repo`**。它按一份 XML 清单把很多 git 仓库检出到固定的版本（`repo sync`，`repo manifest -r` 记下快照），是为 AOSP 的几百个仓库和 Gerrit 设计的。我们要的 kas 都做了：按提交检出层、生成 bitbake 的配置、在容器里构建、跨仓库引用配置、展开并记下每个提交。`repo` 只会多一份清单，把 kas 的仓库表再抄一遍（两份还得保持一致），也不会配置 bitbake；它多给的只是把几个仓库并排检出来改，我们只有两到五个仓库，`git clone` 加 kas 的 `checkout`、`for-all-repos` 就够了。

#### 8.7.5 GPLv3：为什么不能「闭源管理」

- **发出去就要给源码**：产品镜像里有 GPLv3 的程序，就要随它给出「对应源码」（GPLv3 §6：源码，加上构建和安装它的脚本），厂商对它的修改也在内；GPLv3 的代码和闭源的代码链接成一个程序，整个程序都要按 GPLv3 给。所以 GPLv3 的部件不能当闭源的东西管，只能当开源的发、跟踪它的源码。
- **用户产品还要给「安装信息」**：手机是 GPLv3 说的「用户产品」，发的人还要给出在这台设备上装上、运行改过的版本所要的方法、钥匙和步骤（Installation Information，§6），除非谁都装不了（包括我们自己，比如写死在 ROM 里）。我们的根文件系统经 dm-verity 和签名核对，引导链是锁着的（[25](25-system-update.md)）：改过的 GPLv3 程序要能运行，要么交出签名的钥匙（整条信任链就没了，支付、端到端消息、设备认证都靠它），要么给一条用户能自己打开的路（下表第三种）。LGPLv3 的库也一样：它要求用户能换上改过的库，在用户产品上同样要安装信息。
- **没有问题的**：
  - 构建机上的 GPLv3 工具（GCC、binutils、make、bash、bison……）：只在构建时跑，不进镜像，不给镜像带来义务。
  - GCC 的运行时库（libgcc、libstdc++）：GPLv3 加上 GCC 运行时库例外，用 GCC 编出来的程序可以按任何许可证链接它们，镜像里有它们（`phone-qemuarm64.yml` 的说明）。
  - GPLv2 的部件（内核、81voltd）：要给源码，没有安装信息这一条（[39](39-oneplus-6t.md) §10）。
- **看起来躲不开一个 GPLv3 部件时**，有三种做法：

| 做法 | 例子 | 代价 |
|------|------|------|
| 自己写一个宽松许可证的代替品 | qbootctl → `phone-abslot`，bootmac → `phone-btaddr`，callaudiod → phone-audiod 的 UCM 通话路由，msm-modem 的 UIM 选择 → `uim-select`：照它们要做的事重写，没有用它们的代码（[39](39-oneplus-6t.md) §10） | 我们自己写、测、维护，上游修了什么要自己跟；部件大时（hexagonrpcd）工作量大，写好之前就没有这项功能（6T 没有传感器） |
| 用它还是 GPLv2 时的老版本 | GNU 工具改 GPLv3 以前的版本（Yocto 原来的 meta-gplv2 层放的就是这些，现在还有没有人维护**未核实**） | 没有安全修复，和新的依赖对不上，等于我们维护一个分叉；只适合很小、很稳定的东西 |
| 不改它，作为单独的程序发，给源码和一条用户能打开的路 | 单独的进程，不和闭源的代码链接（放在同一个镜像里不算链接）；随镜像给源码；安装信息：比如解锁引导程序后装自己的版本（设备认证随之报 `unlocked`，要求验证启动的服务可以拒绝），或者把它放在 dm-verity 管不到、机主能换掉的地方 | 产品的 kas 文件要给它开 `INCOMPATIBLE_LICENSE` 的例外；每个版本都要给源码、过法务；放在验证之外的那一份，拿到 root 的攻击者也能换掉，要靠沙箱兜住；解锁后失去设备认证算不算给了安装信息，要法务看（**未核实**） |

  默认是第一种；做不到就不要这项功能（像 6T 的传感器）；第三种要负责人和法务一起批。厂商交来的东西里有 GPLv3 的，交付审查时退回（内部文档 60）。

#### 8.7.6 最后有哪些仓库

| 仓库 | 公开吗 | 放什么 |
|------|-------|-------|
| kario | 公开 | 平台（Shell、服务、phone-paint、jsrt、SDK、应用、telephonyd、兼容测试）；公共的 Yocto 层 `meta-phone`：镜像、initramfs、系统更新、`phone-kernel.inc` 和公共的内核配置片段、Android 启动镜像的类（`phone-bootimg`、`phone-abboot`、`phone-sparse`）和 `mkbootimg-native`、`phone-bringup`、公开开发板的机型 `kario-qcom`；开源镜像的 kas 文件；SKU 框架和已经公开的板子、SKU（6T 的板子文件也留下：它是硬件事实）；`tools/phone-chroot` |
| kario-bsp（[kario-os/kario-bsp](https://github.com/kario-os/kario-bsp)） | 公开 | 只有配方（§8.2、§8.7.2）：**一个**通用的仓库，每家厂商或每颗 SoC 一个层。`meta-kario-qcom` 是任何高通板子都用的胶水，`meta-kario-sdm845` 是 6T（下表）；它也是厂商 BSP 层的样板 |
| 厂商的私有 BSP 仓库 | 私有 | 一家厂商一个仓库，每个产品一个层（机型、产品的设备树、按合同拿的固件的配方），叠在 kario-bsp 里那家的层上面；厂商内核（比如展锐）的层也在那里（内部文档 60） |
| 内部仓库 | 私有 | 云端服务、产品的 kas 文件（选 BSP 层）、发布、密钥的工具、还没宣布的产品的 `sku/`（内部文档 60） |
| 厂商的驱动源码 | 私有 | 厂商给了源码的部件（比如相机、传感器的模块和用户态程序），每个一个仓库，BSP 层的配方按提交取（内部文档 60） |

6T 的高通部分从 `yocto/meta-phone/` 搬进 kario-bsp 的两个层（2026-11-27，构建和测试过，**未上真机**，见 §8.7 开头），在层里的路径不变。任何高通板子都用的进 `meta-kario-qcom`（集合 `kario-qcom`，内部的商务键盘机也装 `phone-qcom-uim`、81voltd、`phone-qcom-pdc`），只有 6T（SDM845）用的进 `meta-kario-sdm845`（集合 `kario-sdm845`，依赖 `kario-qcom`）；两个层的 `BBFILE_PRIORITY` 都是 15，比 meta-phone 的 10 高，所以 `base-files` 的 6T 部分在 meta-phone 的那份之后跑：

| 搬走的（相对于 `yocto/meta-phone/`） | 进哪个层 | 是什么 |
|-------|-------|-------|
| `conf/machine/oneplus-fajita.conf` | `meta-kario-sdm845` | 6T 的机型 |
| `recipes-kernel/linux/linux-sdm845_7.1.bb` | `meta-kario-sdm845` | 6T 的内核；它 `require` 的 `phone-kernel.inc` 留在 meta-phone（经 `BBPATH` 找到） |
| `recipes-kernel/linux/files/phone-fajita.cfg` | `meta-kario-sdm845` | 6T 内核的配置片段 |
| `recipes-bsp/firmware-oneplus-sdm845/` | `meta-kario-sdm845` | 6T 的固件（第三类，§8.1） |
| `recipes-bsp/alsa-ucm-conf-sdm845/`（连同 `files/fajita-voicecall-speaker.conf`） | `meta-kario-sdm845` | 一加 6、6T 的 UCM 配置 |
| `recipes-bsp/phone-qcom/`（`abslot.c`、`btaddr.c`、`uim-select`、`pdc-select`、`pdc-carriers`、`ims-status`、`rauc-abl`、四个 `phone-qcom-*.service`、`phone-audiod-ucm.conf.in` 和别的 `files/`） | `meta-kario-qcom` | Kario 给高通手机写的胶水 |
| `recipes-bsp/81voltd/`（连同 `files/81voltd.service`） | `meta-kario-qcom` | 高通基带的 IMS 数据服务 |
| `recipes-bsp/q6voiced/` | `meta-kario-qcom` | 高通的通话声音通路 |
| `recipes-core/base-files/base-files_%.bbappend` 里 `:oneplus-fajita` 的部分 | `meta-kario-sdm845` | 6T 的 `/init` 和根的 fstab 行；删 `/dev/root` 那行的 `do_install:append` 留下 |

- 跟着走的测试，进 kario-bsp 的 `tests/`：`yocto/abslot-test.py`、`yocto/btaddr-test.py`、`yocto/pdc-select-test.py`、`yocto/ims-status-test.py`，`tools/slots_test.py` 里测 `rauc-abl` 的部分（`tests/rauc-abl-test.py`）。kario-bsp 的 `make test` 还跑 `scripts/layer_check.py`（没有二进制、凭据、保密的材料）、对每个层跑本仓库的 `yocto-check.py --layer` 和 `sku.py check`；它的 README 写了什么能进、什么绝不能进。
- 留在 kario 的：`phone-kernel.inc` 和公共的 `phone-*.cfg`，`phone-qcom.cfg`、`phone-usb-console.cfg` 也留下（`kario-qcom` 用前一个；`phone-kernel.inc` 把它自己的 `files/` 加进 `FILESEXTRAPATHS`，所以 6T 的内核在别的层里照样取得到这两个：`bitbake -e linux-sdm845` 看过）；`yocto/kas/phone-fajita.yml`、`phone-fajita-ab.yml`（多一个 `kario-bsp` 仓库，写全提交：`repos.kario-bsp.commit` 一行，`-ab` 经 include 拿到；同时改两个仓库用 `yocto/kas/local-bsp.yml`，[38](38-contributing.md) §11）；`yocto/fajita-flash.sh`；`sku/boards/oneplus-*.toml`。
- 跟着改了路径的：`tools/sku.py`、`tools/sku_test.py`、`scripts/yocto-check.py`、`scripts/ci-plan.py`、`phone-initramfs.bb`（§8.7.3），读这些文件的测试（`yocto/charger-generator-test.py`、`boot-console-test.py`、`svcusers-test.py` 经 `KARIO_BSP_LAYERS` 读），文档 [39](39-oneplus-6t.md)、[43](43-phone-partitions.md)、[41](41-design-gaps.md)、[38](38-contributing.md)（[20](20-audio.md) 只写文件名，不用改）。
- kario-bsp 每发布一次是一个新的根提交（`scripts/publish.sh`），旧的提交 main 上就没有了：`phone-fajita.yml` 的固定提交要跟着改，kas 和 `make test` 都拒绝 main 上没有的提交；旧的 kario 提交也就取不到当时的 kario-bsp。第一个版本以后改成在 main 上照常加提交。
- **钥匙**：产品的模块签名钥匙每条发布线一把，在 HSM 里（`CONFIG_MODULE_SIG_KEY` 写 `pkcs11:` 地址，§8.7.2）；开发用的照旧是每个构建目录自己的（`yocto/ota-keys.sh`）。

#### 8.7.7 什么时候做

迁移要在一个独占的窗口里一次做完：搬文件会让同时在改 `yocto/` 的改动都合不上。迁移要的接口改动（`sku.py` 的第二个路径、契约的包名、`yocto-check.py` 接受层的路径）提前作为普通的改动合进去，窗口里只搬文件、固定提交、跑完整的验证（实际上 2026-11-27 的窗口里除了第二个路径都是和搬家一起做的）。时间：**在第一个厂商 BSP 到手以前**（这套机制先在公开的代码上走通，厂商的东西从一开始就不进 meta-phone），**在手上这一批改动合完、发布以后**。步骤、每一步之后核对什么、要多久、怎么回退，在内部文档 60。

### 8.8 基带：可移植的核心、Modem 后端和 SoC 胶水

> 状态：契约写进代码的部分 ✓：telephonyd 的后端表（`telephonyd/src/backend.cpp`）、状态的契约（`contract_problems`，telephonyd 在标准错误上报出后端违反的每一条）、`tools/sku.py` 的 `MODEMS` 和 `IMS` 表；单元测试、`make mm-selftest`、`tools/sku_test.py` 查。实现只有高通一套（ModemManager 后端加 `phone-qcom` 等胶水），**未上真机**（[39](39-oneplus-6t.md) §14.6）。联发科见 §8.9，**未实现**。

换一种基带（比如把骁龙换成联发科的一体 SoC）只换两样：telephonyd 里的一个 **Modem 后端**和板子的 **SoC 胶水**；**可移植的核心**不动，Shell、应用、phone-smsd 看到的 telephonyd 协议也不变（[04](04-ui-language-telephonyd.md)）。三层：

| 层 | 是什么 | 现在（高通） | 换基带时 |
|----|-------|------------|---------|
| 可移植的核心 | telephonyd 的 `Service`（RPC、通话记录、给 phone-smsd 的待确认队列、接入点数据库和接入点计划、三个开关和漫游规则 `roaming_allowed`、紧急号码表、第二路的规则 `second_call`）；Shell（来电界面、状态栏、SIM 卡 PIN 界面、飞行模式、防火墙挡 `data_ifaces`、按 `data_ip` 写 networkd 的配置、彩信承载的策略路由、流量统计）；phone-smsd；phone-audiod 的 UCM 通话路由（跟着 telephonyd 的通话，板子的 `[audio]`） | 一套代码 | 不变 |
| Modem 后端 | `Modem` 接口（`telephonyd/src/modem.hpp`）的一个实现，跑在 telephonyd 进程里、它的事件循环上，只讲公开的协议；按名字登记在 `backend.cpp` | `MmModem`（ModemManager 的 D-Bus，`mm.cpp`、`mm_state.cpp`）；`SimModem`（模拟的，开发机和 QEMU） | 能用 ModemManager 的就还是 `MmModem`，否则加一个后端（§8.9） |
| SoC 胶水 | 基带起来之前和在它旁边要跑的、每种 SoC 不一样的程序和配置，各是单独的进程或配置文件，由 Yocto 机器和板子文件选 | `phone-qcom`（`uim-select`、`pdc-select`）、81voltd、q6voiced、rmtfs、tqftpserv、qrtr、`alsa-ucm-conf-sdm845`（[39](39-oneplus-6t.md) §10、§11.1） | 换成那种 SoC 的（§8.9） |

**后端要提供的**（`Modem` 接口每个方法上面的注释是准的，这里是要点）：

| 部分 | 后端要做的 | 谁靠它 |
|------|-----------|-------|
| 名字 | `backend.cpp` 里一条：名字就是板子 `modem` 的值，也是它报的 `Status.backend`；`tools/sku.py` 的 `MODEMS` 列同样的名字（`sku_test.py` 核对两边一样） | 板子选后端；Shell 只认 `sim`（自测的模拟网卡） |
| 状态 | `Status` 的每个字段，变了就发 `status` 事件；遵守 `contract_problems`：`backend` 写了名字；数据链路（`data_ip`）恰好在 `data_state` 为 `connected` 时有，射频或开关关着时 `data_state` 为 `off`（还没有基带时也是 `off`）；数据链路和彩信承载的网卡都在 `data_ifaces` 里，名字是 Shell 能写进 networkd 配置的（1–15 个字母、数字、`_.-`，不以点开头，`valid_iface`）；静态地址带地址和前缀；只在注册上时报漫游，`data_state` 为 `roaming` 只在漫游开关不让连时；信号 0–4 格、没注册时为 0；SIM 卡要 PIN 或 PUK 时才报剩余次数。地址可以晚于「已连接」到（ModemManager 就这样），Shell 等它到了再配 | Shell 的状态栏、防火墙、networkd、流量统计 |
| 通话 | 拨、接、挂、保持（恢复保持的一路时两路切换）、按键音；状态 dialing → alerting / incoming → active / held → ended 和结束原因；第二路照 `second_call`；紧急号码在射频关着、没卡、卡锁着时照拨，射频关着先上电；静音只记状态（麦克风属于声音通路） | 来电界面、通话记录、phone-audiod 跟着通话切 UCM、胶水的声音通路 |
| 短信、小区广播 | 要送达回执，一条的后续事件用同一个 id；收到的留在基带的存储里，`ack_sms` / `ack_cb` 后才删；id 由内容算、重启后不变 | phone-smsd 一条不丢 |
| USSD、SIM 卡 | 同一时间一个 USSD 会话；PIN、PUK、PIN 锁的结果发 `unlock`、`pin` 事件；读 SIM 卡电话簿 | Shell 的 PIN 界面、通讯录导入 |
| 射频和数据 | `set_radio`（关就是飞行模式，紧急呼叫中拒关）、`set_data`、`set_data_roaming`；`set_apn` 给的计划：数据只连上网接入点，附着接入点交给基带；IMS 和紧急的承载（胶水开的）不算数据、不断开、网卡不进 `data_ifaces` | Shell 的开关、防火墙；胶水的 IMS |
| 彩信承载 | 计划里有单独的彩信接入点时另连一条承载，有了地址再回；要静态的 IPv4（Shell 按源地址做策略路由）；和上网同一接入点时回「走数据连接」 | phone-smsd 的彩信 |
| 不阻塞 | 对基带的请求都异步、跑在 telephonyd 的 `Loop` 上；和基带的连接断了，`Backend::failed` 让 telephonyd 退出、systemd 重启它 | 整个 telephonyd |
| 不加载闭源库 | 后端只讲公开的协议（D-Bus、AT、MBIM、QMI、binder）；厂商的库只在单独的进程里（§8.4） | §8.1 |

**SoC 胶水要提供的**：

| 胶水 | 做什么 | 高通（现在） | 怎么选 |
|------|-------|------------|-------|
| 基带的引导和文件 | 让基带起来，给它的文件系统和固件 | remoteproc、`rmtfs`、`tqftpserv`、`qrtr`、`pd-mapper`，固件包 `firmware-oneplus-sdm845` | Yocto 机器（`oneplus-fajita.conf` 的 `MACHINE_ESSENTIAL_EXTRA_RDEPENDS`） |
| SIM 卡槽 | 打开 SIM 卡的会话、选卡槽，后端才看得见卡 | `phone-qcom-uim`：`uim-select` 经 QMI UIM 给卡槽 1 的第一个 USIM 开会话，排在 ModemManager 前 | 机器 |
| 运营商配置 | 按 SIM 卡的运营商激活基带里这家的配置（IMS、VoLTE 的参数在里面） | `phone-qcom-pdc`：`pdc-select` 经 QMI PDC 激活 MCFG，排在 ModemManager 前（激活会让基带重启），表在 `pdc-carriers` | 机器按 `PHONE_IMS` 和 IMS 一起装 |
| IMS | 基带要 IMS（和紧急）承载时开；IMS 注册状态 | 81voltd：在 QRTR 上答 QMI 的 IMS 数据服务，经 ModemManager 的 `CreateBearer` 开承载，后端按 `mm::bearer_role` 认出它不碰；注册状态由 `phone-qcom-ims` 的 `ims-status` 经 QMI 的 IMSA 问，telephonyd 跑它；2026-11-28 起钉的 kario-bsp（`f228abe`）里的版本先经 qmi-proxy 绑定订阅再问（之前的版本不绑定，报告 008 里 6T 上问不到），**未上真机**（[39](39-oneplus-6t.md) §14.21） | 板子的 `ims`；`tools/sku.py` 的 `IMS` 表写每个胶水经哪个后端开承载（81voltd 要 `modemmanager`） |
| 通话的声音 | 通话时接通基带到声卡的通路；听筒、免提、静音 | q6voiced 在通话时打开 q6voice PCM（DSP 的语音通路），**跟着 ModemManager 的通话**；phone-audiod 按 UCM 切听筒、免提和麦克风，跟着 telephonyd | 机器的 `Q6VOICED_PCM`，板子的 `[audio]`（`PHONE_AUDIO_*`） |
| 数据网卡 | 承载用的网卡 | 后端报：ModemManager 的 net 端口（IPA 的 `rmnet_ipa0`）和多路复用出来的承载网卡（`qmapmux0.N`）；名字合 `valid_iface`，Shell 不写死任何网卡名 | 后端 |
| 基带的定位 | 基带的 GPS 和辅助数据 | ModemManager 的 Location；预测星历只给高通的 XTRA 格式，按 `SupportedAssistanceData` 有才给 | 板子的 `gnss = "modemmanager"` |

胶水的规矩：不和后端抢——不连数据承载、不断开别人的承载，开的承载要让后端认得出是 IMS 或紧急的（ModemManager 上：APN 是计划里的 IMS、紧急接入点或通用的 `ims`、`sos`，或者 `apn-type` 标了）；后端起来前要做完的（卡槽、运营商配置）在 systemd 里排在后端的守护进程前面，失败时只少它那一项；跟着通话的胶水最好跟 telephonyd 的事件（`subscribe` 的 `call`），这样和后端无关，phone-audiod 就是这样；进单独的进程和沙箱（§8.4）。

**板子和 SKU 怎么选**：板子文件（`sku/boards/*.toml`）是唯一的来源。`modem` 选后端（`MODEMS` 之一，不写是 `sim`；别的值 `tools/sku.py check` 报错），经 `sku.json` 交给 telephonyd（`--sku`）；`ims` 选 IMS 的胶水（`IMS` 表，要它要的后端），写成 kas 覆盖里的 `PHONE_IMS`；`[audio]` 是声卡和通话的 UCM 设置；`gnss` 选定位接收机。别的胶水跟着 Yocto 机器走：一种 SoC 一台机器（以后是一个私有 BSP 层，§8.2），机器装这种 SoC 的胶水包；同一个 telephonyd 程序里编进所有后端，运行时按 `sku.json` 选。

**新后端怎么验**：

1. 单元测试（`make telephonyd`）：`test_backends` 核对后端表的名字和每个后端报的 `Status.backend`；`test_contract` 让模拟基带走过各种状态（连着、关数据、漫游、飞行模式、SIM 卡锁着），每个状态都不违反 `contract_problems`，每条规则各有一个违反它的例子。新后端在这里加它自己能在宿主机上造出来的状态。
2. 套接字上的用例：`mm_test.py` 像 Shell 一样经 telephonyd 的套接字调用、收事件，只有「网络」那一边（模拟的 ModemManager 和它的控制接口）是 ModemManager 特有的。新后端照它做一个自己的传输的模拟（比如 binder 上的假 radio 服务），跑同样的用例；最后一个用例 `backend_contract` 在 telephonyd 报过任何违反契约的状态时失败（`make mm-selftest` 的 39 个用例里就有它）。经 ModemManager 的后端（比如联发科走 ModemManager）telephonyd 这边已经由 `make mm-selftest` 测了，ModemManager 插件和基带那边只能上真机。
3. `make selftest`：模拟基带上 Shell 一侧的电话、短信、接入点、漫游用例，不随后端变。
4. `tools/sku_test.py` 的 `test_modem`：`MODEMS` 和 `backend.cpp` 的名字一样，别的值报错。
5. 真机：telephonyd 的日志里每种违反只报一次（`the <后端> backend breaks the Modem contract: …`），照 [39](39-oneplus-6t.md) §14.6 的清单逐项试。

**高通特有的东西在哪**（2026-11-26 查过 telephonyd、Shell、`svc/`、`hal/`、`programs/`、`location/`、`tools/sku.py` 和 `yocto/`）：

| 在哪 | 高通特有的 | 归哪层 |
|------|-----------|-------|
| kario-bsp 的 `meta-kario-qcom/recipes-bsp/phone-qcom/`：`uim-select`、`pdc-select`、`pdc-carriers` | QMI UIM 的会话、QMI PDC 的 MCFG 配置 | SoC 胶水 |
| `recipes-bsp/81voltd`、`q6voiced`、`alsa-ucm-conf-sdm845`；`oneplus-fajita.conf` 装的 `qrtr rmtfs tqftpserv libqmi` | QMI 的 IMS 数据服务、DSP 的语音 PCM、SDM845 的 UCM、基带的文件服务 | SoC 胶水 |
| `tools/sku.py` 的 `IMS`、板子 `oneplus-fajita.toml` 的 `ims` | 只有 81voltd 一种 IMS 胶水 | SKU 选胶水 |
| `telephonyd/src/mm_state.cpp` `bearer_role` | 不带 `apn-type`、带 `profile-id` 的承载当别人的（81voltd 那样开的） | Modem 后端（ModemManager） |
| `telephonyd/src/mm.cpp` | 小区广播（ModemManager 1.24 的 `CellBroadcast`）、附着接入点（`SetInitialEpsBearerSettings`）在 ModemManager 里主要是 QMI、MBIM 基带有；SIM 卡电话簿走 AT（`Modem.Command`），QRTR 上的骁龙基带没有 AT 口，会回错误 | Modem 后端 |
| `location/mm.go` | XTRA 预测星历是高通的格式，按 `SupportedAssistanceData` 有才给 | 后端的能力，按能力降级 |
| `shell/cellnet.go` 彩信承载要静态 IPv4 | QMI、MBIM 基带经 ModemManager 报的就是静态的；PPP 和 DHCP 的不配彩信承载 | 后端契约（彩信承载一行） |
| `yocto/meta-phone/recipes-phone/phone-telephonyd/files/phone-telephonyd.service` | `Wants=` / `After=ModemManager.service`：唯一的硬件后端的守护进程写在通用的单元里 | 应归胶水（下文） |
| `phone-qcom` 里的 `phone-qcom-audio` | audiod 的 UCM 设置（从 `PHONE_AUDIO_*` 来）并不是高通特有的，却放在高通的包里 | 应归核心的配方（下文） |
| `programs/audiod`、`audio/ucm` 的注释、`modem.hpp` 的 `sim_link`、`apn.hpp` 的 `ims` | 只在注释里拿 q6voiced、QMI、81voltd 作例子 | 核心，没有依赖 |

Shell、`svc/`、`hal/`（基带部分）和 telephonyd 的 `Service` 里没有写死高通的东西：网卡名、承载、接入点都来自后端。

这次改了的（不改行为）：telephonyd 选后端从 `main.cpp` 里 `#ifdef` 的一串判断改成后端表 `backend.cpp`；`contract_problems` 把 Shell 靠的东西写成代码，telephonyd 报出违反的状态；`tools/sku.py` 检查 `modem` 是 telephonyd 编进的后端（以前不查，写错了手机开机后没有电话），`IMS` 表写上每个 IMS 胶水要的后端；CI 改了后端表或 `modem.*` 时也跑 `mm-selftest`。

还没改的（要构建镜像或真机才验得了，**未实现**）：`phone-telephonyd.service` 里的 ModemManager 依赖搬进装 ModemManager 的机器给的 drop-in；`phone-qcom-audio` 搬进 phone-audiod 的配方（联发科主线的板子也用 UCM）；q6voiced 跟 ModemManager 的通话，换成跟 telephonyd 的（或者由 phone-audiod 在通话时打开语音 PCM），这样不经 ModemManager 的后端也有通话声音。

### 8.9 联发科：三条路（**未实现**）

> 状态：只是设计，**未实现**，下面联发科的细节大多来自公开的零星资料，**未核实**；没有联发科的手机和厂商资料。

联发科的手机 SoC（天玑、Helio）里基带和应用处理器经 CCCI（联发科的内核驱动，只在厂商内核里）说话：AT 和别的通道是字符设备，数据网卡是 `ccmniN`；基带的引导、NV 数据的文件服务由厂商的守护进程做（`ccci_mdinit`、`ccci_fsd` 一类，**未核实**）。主线内核只有 PCIe 模块的 `t7xx`（WWAN 子系统，ModemManager 经 MBIM 支持），不是手机 SoC 的 CCCI。和骁龙最大的不同：骁龙的 IMS 在基带固件里，联发科的 IMS 协议栈多半在应用处理器的厂商用户态（闭源的守护进程，安卓上跟 RIL 一起，**未核实**）。

| 路 | 怎么接 | 要厂商给什么 | 合不合 Kario 的规则 |
|----|-------|-------------|------------------|
| ModemManager，经 CCCI 上的 AT（或 MBIM） | 后端还是 `MmModem`，不用写；udev 把 CCCI 的 AT 口标给 ModemManager，数据走 `ccmniN`。ModemManager 现有的联发科插件是给 USB、PCIe 模块的，要给 CCCI 写插件（开源，能进上游），承载接到 `ccmni` 要联发科的私有 AT 命令（**未核实**） | 能用的厂商内核（CCCI 驱动）、AT 命令文档（数据、双卡、运营商参数、IMS 的私有命令）、基带引导和 NV 服务（闭源进程） | 最合：telephonyd、ModemManager 都开源，闭源的只有固件和引导、NV 服务（单独的进程、沙箱）。缺 VoLTE：AP 上的 IMS 栈不经 AT 就用不上，只有 4G 的运营商上打不了电话 |
| 安卓的无线接口 HAL，经 libgbinder | 新后端（比如 `android-radio`）经开源的 libgbinder 讲 `IRadio`（AIDL 的 `android.hardware.radio.*`，老的 HIDL 1.x）和联发科的扩展；厂商的 rild、`mtk-ril`、IMS 守护进程跑在一个装着厂商 `vendor` 分区的容器里（Halium 那样，有自己的 servicemanager），[11](11-platform-architecture.md) §16 计划的就是这条 | 厂商的安卓 BSP（`vendor` 分区、厂商内核）和再分发的权利；HAL 的版本；联发科扩展的接口说明（IMS、双卡、运营商配置） | 合规则：telephonyd 只链接 libgbinder，厂商代码只在容器里；但容器和厂商进程的沙箱策略**未实现**（§8.6），老内核上沙箱少的东西见 §7.5。通话的声音要经音频 HAL（`audio.primary.*`）设，又是一个 HAL 宿主进程（§7.4）。唯一能拿到 VoLTE 的路 |
| MIPC | 联发科较新的基带给它自己的 RIL 用的私有消息协议，也经 CCCI（**未核实**） | 协议文档（多半要保密协议），或者只给库 | 有文档：可以写一个开源的后端，最轻；只给库：库要放进单独的进程，经 Kario 定义的协议说话，等于第二条路的做法 |

**胶水在联发科上是什么**（都**未核实**）：基带的引导和文件——`ccci_mdinit`、`ccci_fsd` 和 NV 的守护进程；SIM 卡槽——双卡由 RIL 每张卡一个实例（`IRadio` 的 `slot1`、`slot2`），telephonyd 的模型现在只有一张卡；运营商配置——基带的运营商参数（联发科叫 SBP）由厂商的 RIL 按 SIM 卡设；IMS——AP 上厂商的 IMS 守护进程和 RIL 的 IMS 扩展；通话的声音——音频 HAL 经 CCCI 的语音通道设；数据网卡——`ccmniN`，RIL 建立数据连接时回网卡名和地址（正好是 `DataLink`）。

**选哪条**：要 VoLTE（中国、美国的很多运营商没有 2G、3G 了）就只有第二条：工作量是 libgbinder 后端、容器和厂商进程的沙箱、音频 HAL 宿主进程，外加厂商的 BSP 和合同。不要 VoLTE（还有 2G、3G 的市场）、厂商肯给 AT 文档时第一条最省：telephonyd 一行不改，工作在 ModemManager 的插件。第三条看采购合同给不给协议文档。不管哪条，Kario 这边要做的：后端表里一条和 `MODEMS` 里同名的一项（或者还是 `modemmanager`）；板子的 `modem`、`ims`（`IMS` 表加这种胶水和它要的后端）；这种 SoC 的 Yocto 机器（或 BSP 层）装胶水；一个那种传输的模拟，跑套接字上的用例和 `backend_contract`；双卡要先扩展 telephonyd 的模型（现在一张卡）。

## 9. 开放的参考机（2026-11-26 调研和设计，硬件**未实现**）

> 状态：板子 `qcm6490-ref`、两个 SKU `ref-qwerty-qcm6490-4g`、`ref-t9-qcm6490-4g` 和产品层 `ref` 的文件 ✓；`sku.py check`、`gen` 通过；两个叠加层用 dtc 编译、`fdtoverlay` 合进主线 6.18 和 7.1-rc1 编出的 Fairphone 5 设备树 ✓；每个部件的驱动文件都在 6.18 和 7.1-rc1 的内核树里，compatible 对得上 ✓。没有 Yocto 机型、没有镜像、没有做出来的板子：硬件**未实现**，下面每一项在板子上都**未验证**。第三部翻盖的状态见 §9.10。

两部照着开源仓库就能自己做出来的键盘手机（第三部翻盖见 §9.10）：一部有诺基亚 E71 的意思（全键盘直板，横屏），一部有 N73 的意思（九宫格直板，竖屏）。用现在买得到的零件，硬件尽量配齐：前后摄像头、距离和光线传感器、USB-C、3.5 mm 耳机孔。文件里的名字都是中性的（`ref`、`qcm6490-ref`），不带诺基亚的商标。

### 9.1 新的 SKU，不是把 kario-* 的补全

`kario-*` 三个 SKU 是产品线（骁龙 662、460 的主线开发板，选型见内部文档 61），参考机另起一块板、两个 SKU，原因：

- 骁龙 662（SM6115）和 460（SM4250）在主线上**没有移动数据、没有摄像头**：IPA 驱动（`drivers/net/ipa/ipa_main.c`）认的是 msm8998、sdm845、sc7180、sdx55、sm6350、sm8350、sc7280、sdx65、milos、sm8550，没有 sm6115，`sm6115.dtsi` 只为 IPA 留了内存；camss 在 6.18 和 7.1-rc1 里都没有 sm6115（7.1 加的是 qcm2290）；也没有 venus。补全它们给不了参考机要的摄像头和上网。**这一条也关系到产品线：两块产品板在主线上只能打电话、发短信，不能用蜂窝数据，负责人要知道。**
- 改 `kario-*` 的板子就是改产品的决定，不是这件事该做的。
- 参考机的板子没有本层的机型（`kario-qcm6490` 要等 kario-bsp 里它的层，§8.7.6），和产品板分开，产品板的检查、构建一点不受影响。

### 9.2 SoC：为什么是 QCM6490

| SoC | 显示、GPU | 基带、移动数据 | 摄像头 | 视频硬件编解码 | 主线声卡 | 主线描述的整机 |
|-----|----------|--------------|-------|-------------|---------|-------------|
| SM6115（骁龙 662） | DPU ✓，Adreno 610 ✓ | MPSS ✓，IPA ✗ | camss ✗ | ✗ | 只有 qrb4210-rb2 | F(x)tec Pro1 X（没有声卡节点） |
| SM4250（骁龙 460） | 同 SM6115 | 同 SM6115 | ✗ | ✗ | 同 SM6115 | 一加 Nord N100 |
| QCM6490（SC7280 家族） | DPU ✓，Adreno 643 ✓ | MPSS ✓，IPA ✓ | camss ✓（原始 Bayer，没有 ISP） | venus ✓ | `qcom,qcm6490-idp-sndcard`、Fairphone 5 的 | Fairphone 5、SHIFTphone 8、Particle Tachyon、高通 IDP；RB3 Gen 2（QCS6490） |

QCM6490 是 SC7280 家族的物联网版：以模组卖给小厂、承诺长期供货（Quectel 说 SG560D 供到 2036 年）；QCS6490 是没有基带的那一种，做不了手机。Fairphone 5 在 Nura 是 testing 一类，2026-09 起 sc7280-mainline 的 edge 内核能打电话、主摄经 libcamera 能用，声音（扬声器、麦克风）还是最大的问题；它 2026-01-12 停产了，但它的设备树是参考机的样板。骁龙 845（一加 6T）主线上什么都有，可只剩二手手机，买不到芯片或模组。

模组选 **Quectel SG560D-EM**（LGA 42.5 × 56.5 × 2.95 mm）：QCM6490、PMIC、内存和存储、5G / LTE Cat 18（含 WCDMA、GSM）的射频、GNSS、Wi-Fi 6E 和蓝牙 5.2 都在上面，模组有 CE、GCF 认证；底板上只剩外设、天线和电源。模组厂只给 Android，没有主线设备树。

### 9.3 两部共用的硬件

接口一列是部件接在哪；「主线驱动」是 compatible 和驱动源文件（6.18、7.1-rc1 里都在）；「Kario」是我们这边用不用得上。

| 部件 | 型号 | 接口 | 主线驱动 | Kario | 状态 |
|------|------|------|---------|-------|------|
| 基带和它的 GNSS | QCM6490 MPSS（模组上） | SoC | `qcom,sc7280-mpss-pas`，`drivers/remoteproc/qcom_q6v5_pas.c` | telephonyd 的 `modemmanager` 后端，`gnss = "modemmanager"`，VoLTE 由 81voltd（`ims`） | **未验证** |
| 移动数据 | QCM6490 IPA | SoC | `qcom,sc7280-ipa`，`drivers/net/ipa/ipa_main.c` | 和 6T 一样经 ModemManager | **未验证** |
| GPU | Adreno 643 | SoC | `qcom,adreno`，`drivers/gpu/drm/msm/adreno/adreno_device.c` | `gpu = true`（网页用 GPU）；phone-paint 默认不走 GPU | **未验证** |
| 显示控制器 | QCM6490 DPU、DSI | SoC | `qcom,sc7280-dpu`，`drivers/gpu/drm/msm/disp/dpu1/dpu_kms.c` | 叠加层接屏（§9.4） | 叠加层合进去 ✓，**未上真机** |
| 视频编解码 | Venus | SoC | `qcom,sc7280-venus`，`drivers/media/platform/qcom/venus/core.c` | mediad 用 ffmpeg 软件解码，硬件编解码**未实现** | — |
| Wi-Fi | WCN6750（模组上） | SoC | `qcom,wcn6750-wifi`，`drivers/net/wireless/ath/ath11k/ahb.c` | 不要板子专门的东西 | **未验证** |
| 蓝牙 | WCN6750 | UART | `qcom,wcn6750-bt`，`drivers/bluetooth/hci_qca.c` | 同上 | **未验证** |
| 声卡 | QCM6490 LPASS（AudioReach） | SoC | `qcom,qcm6490-idp-sndcard`，`sound/soc/qcom/sc8280xp.c` | 没有 UCM 配置，板子不写 `[audio]`：audiod 自己找声卡 | 板子的声卡**未实现** |
| codec 和耳机孔 | WCD9370，3.5 mm 四段插孔，MBHC 检测插拔和线控键 | SoundWire | `qcom,wcd9370-codec`，`sound/soc/codecs/wcd937x.c`、`wcd-mbhc-v2.c` | audiod 不读插孔状态，不往耳机切（[20](20-audio.md) §10） | Kario **未实现** |
| 扬声器功放 | MAX98357A | I2S | `maxim,max98357a`，`sound/soc/codecs/max98357a.c` | 经声卡 | **未验证** |
| 后摄像头 | Sony IMX258，13 MP | CSI-2，控制走 CCI | `sony,imx258`，`drivers/media/i2c/imx258.c` | mediad 只收 YUYV 的 `/dev/video*`；camss 出原始 Bayer，要 libcamera（软件 ISP） | Kario **未实现** |
| 对焦马达 | DW9714 | I2C（CCI） | `dongwoon,dw9714`，`drivers/media/i2c/dw9714.c` | 同上 | Kario **未实现** |
| 前摄像头 | OmniVision OV8858，8 MP | CSI-2 | `ovti,ov8858`，`drivers/media/i2c/ov8858.c` | 同上 | Kario **未实现** |
| 摄像头接收 | CAMSS | SoC | `qcom,sc7280-camss`，`drivers/media/platform/qcom/camss/camss.c` | 同上 | Kario **未实现** |
| ISP | Spectra | SoC | **主线没有驱动**（`driver = "none"`） | — | 没有 |
| 闪光灯 | PM8350C 的闪光灯 | SPMI | `qcom,spmi-flash-led`，`drivers/leds/flash/leds-qcom-flash.c` | 相机的闪光灯、手电筒**未实现** | Kario **未实现** |
| 光线和距离 | Vishay VCNL4040 | I2C | `vishay,vcnl4040`，`drivers/iio/light/vcnl4000.c` | 自动亮度读光线（`light_sensor = "vcnl4000"`）；通话时贴脸灭屏不读距离 | 光线**未验证**，距离 Kario **未实现** |
| 加速度计 | Bosch BMA400（带计步） | I2C | `bosch,bma400`，`drivers/iio/accel/bma400_i2c.c` | 不读 | Kario **未实现** |
| 振动 | TI DRV2605L 加线性马达 | I2C | `ti,drv2605l`，`drivers/input/misc/drv260x.c` | hal/vibrator 走输入设备的 `FF_RUMBLE` | **未验证** |
| NFC | NXP PN7150 | I2C | `nxp,nxp-nci-i2c`，`drivers/nfc/nxp-nci/i2c.c` | 主线驱动接内核的 NFC 子系统（netlink），phone-nfcd 要原始 NCI 设备（`/dev/nxpnfc`，NXP 树外的驱动）：板子不写 `nfc` | Kario **未实现** |
| 充电、电量计、USB-C | PM7250B（模组上），经 ADSP 的 pmic_glink | SoC | `qcom,qcm6490-pmic-glink`，`drivers/soc/qcom/pmic_glink.c`、`drivers/power/supply/qcom_battmgr.c`、`drivers/usb/typec/ucsi/ucsi_glink.c` | hal/power 读 `power_supply` | **未验证** |
| RTC | PMK8350 | SPMI | `qcom,pmk8350-rtc`，`drivers/rtc/rtc-pm8xxx.c` | `[rtc]` 只读、存偏移 | **未验证** |
| 通知灯、键盘背光 | PM8350C LPG：通道 1 白色通知灯，通道 2 键盘背光，PWM 3 给屏的背光，通道 3 空着 | SPMI | `qcom,pm8350c-pwm`，`drivers/leds/rgb/leds-qcom-lpg.c` | `[leds] notify = "white:status"`，`keyboard_backlight = "white:kbd_backlight"` | **未验证** |
| 屏的背光 | pwm-backlight，接一颗 PWM 调光的升压 LED 驱动芯片（自己不要驱动，型号按屏的灯串选，**未选定**） | PWM | `pwm-backlight`，`drivers/video/backlight/pwm_bl.c` | `backlight = "backlight"` | **未验证** |
| 电源键、音量减 | PMK8350 的 KPDPWR、RESIN | SPMI | `qcom,pmk8350-pwrkey`，`drivers/input/misc/pm8941-pwrkey.c` | `power_key = true`（顶上的电源键，挂断键不兼） | **未验证** |
| 音量加 | GPIO 上的按键 | GPIO | `gpio-keys`，`drivers/input/keyboard/gpio_keys.c` | `keys.toml` 的 `vol-up` | **未验证** |
| 指纹 | 不装 | — | 主线没有指纹子系统，libfprint 只认 USB 的传感器 | `fingerprint` 不写 | 没有 |
| 看门狗 | APSS 看门狗归 Gunyah 虚拟机管理器（设备树里是 `reserved`） | — | — | 不写 `[watchdog]`（systemd 的默认） | 没有 |

`sku.py parts qcm6490-ref` 打出板子文件里的这张清单。

### 9.4 两部各自的

| | 全键盘（`ref-qwerty-qcm6490-4g`） | 九宫格（`ref-t9-qcm6490-4g`） |
|-|------------------------------|---------------------------|
| 意思 | E71 一路：全键盘直板，横屏 | N73 一路：九宫格直板，竖屏 |
| 屏 | Densitron DMT028VGHMCMI-1A，2.8 寸 480×640 IPS（ST7701S，DSI 2 通道，`densitron,dmt028vghmcmi-1a`，`drivers/gpu/drm/panel/panel-sitronix-st7701.c`），横装，`rotate = 90`，画 640×480 | 同一块，竖装，480×640 |
| 键盘 | 全键盘 v1：TCA8418（`ti,tca8418`，`drivers/input/keyboard/tca8418_keypad.c`），8×8，Fn 层，Fn + Sym 当 Ctrl | 九宫格 v1：TCA8418，5×5 |
| 接线 | 键盘在 i2c9 的 0x34，中断 GPIO 77（**未验证**：在 `sc7280_pdc_map` 里，深度睡眠时按键能叫醒 SoC；在 Fairphone 5 的树里空着、不在 QUP 的脚上，和翻盖一样；原来的 GPIO 64 不在表里，§2）；屏的复位 GPIO 44，VCC 接 L11C（2.8 V），IOVCC 接 L12C（1.65 V），DSI 的 VDDA 接 L6B（Fairphone 5 的接法，都在板子文件的 `[wiring]`） | 同左 |
| 机身（**估计**） | 宽约 66 mm：没找到主线有驱动的 2.4 寸左右的 640×480 面板，2.8 寸的屏横放，比 E71（57 mm）宽，和 BlackBerry Bold 9900 差不多 | 宽约 52 mm（N73 是 49 mm），模组 42.5 mm 宽放得下 |
| 内存 | 4 GB（模组的配置） | 4 GB |

两部用同一块底板的设计、同一份板子文件，只换屏的朝向和键盘；`device` 跟着键盘是 `bar-qwerty` 或 `bar-t9`。

### 9.5 3.5 mm 耳机孔要，FM 收音不要

**耳机孔：要。** 直板的机身放得下（插孔约 5 × 12 mm）；有线耳机打电话不用配对、不用充电，线控键能接、挂电话；助听、辅助功能的设备很多还是有线的；主线的 WCD937x 有 MBHC，插拔和线控键都检测得到。要做的在 Kario：audiod 读插孔的状态、通话和媒体往耳机切（[20](20-audio.md) §10 现在写着「耳机：不做」）——**未实现**，硬件先留着。

**FM：不要。** 耳机线当 FM 天线是老办法，可是主线里还在的 FM 接收芯片驱动（`drivers/media/radio/`：Si470x、Si476x、TEA5764、TEA5777、TEF6862、SAA7706H 和老的 ISA、USB 收音机）里，适合手机的只有 Si4703（`silabs,si470x`，`radio-si470x-i2c.c`），它已经停产（Silicon Labs：最后订货 2026-06-15，最后出货 2026-12-15），新设计不能靠它；Si476x 还能不能买**未核实**；常见的 RDA5807 主线没有驱动。模组的 WCN6750 也没有主线的 FM。Kario 也没有收音机的服务和应用。所以 FM 不现实，不装。

### 9.6 物料（价格都是**估计**，美元，10 台的小批量）

| 部件 | 型号 | 单价 | 说明 |
|------|------|-----:|------|
| 模组 | Quectel SG560D-EM（QCM6490，4 GB + 64 GB） | 260 | 分销价 2026-11 约 257；资料和固件要和 Quectel 签协议 |
| 屏 | Densitron DMT028VGHMCMI-1A | 25 | 2.8 寸 480×640 IPS |
| 背光驱动 | PWM 调光的升压 LED 驱动 | 1 | 型号**未选定** |
| 键盘 | TCA8418 加锅仔片、薄膜、键帽 | 20 | 键帽开模另算 |
| 后摄像头 | IMX258 自动对焦模组（带 DW9714） | 15 | |
| 前摄像头 | OV8858 模组 | 10 | |
| 传感器 | VCNL4040、BMA400 | 3 | |
| 振动 | DRV2605L 加线性马达 | 4 | |
| NFC | PN7150 加天线 | 6 | |
| 声音 | MAX98357A、扬声器、听筒、两个麦克风、3.5 mm 插孔 | 8 | codec 在模组上还是要外接，看 Quectel 的硬件设计手册（**未核实**） |
| 电源 | 1500–2000 mAh 锂电池、USB-C 座、保护板 | 12 | |
| 射频 | 5 根蜂窝、2 根 Wi-Fi、1 根 GNSS 天线，nano-SIM 座 | 12 | |
| 底板 | 6–8 层 HDI、贴片 | 60 | |
| 外壳 | 3D 打印或 CNC | 40 | |
| 合计 | | 约 480 | 每台；不算开模、认证和人工 |

### 9.7 做一台要做的

1. 和 Quectel 拿 SG560D 的硬件设计手册、参考设计和按模组的密钥签了名的固件（MPSS、ADSP、CDSP、WPSS、IPA 的）：这是 §8 说的闭源固件，不进 git，进私有的 BSP 层（叠在 §8.7.6 的 kario-bsp 的 `meta-kario-qcom` 上面）。
2. 先确认模组的引导程序能引导主线内核（Quectel 的 ABL 要不要签名、能不能解锁、boot.img 的格式）：**未验证**，也是最大的风险，不行就得换模组。
3. 画底板：屏的 DSI（2 通道）；键盘的 TCA8418 和 VCNL4040（0x60）、BMA400（0x14）、DRV2605L（0x5a）、PN7150（0x28）都在 i2c9；两颗摄像头的 CSI 和 CCI；闪光灯；I2S 的功放；耳机孔；USB-C；八根天线；SIM 座；电池；电源键在顶上，音量键在侧面。
4. 写底板的设备树 `qcm6490-<名字>.dts`：从 Fairphone 5 的出发（同样的 SoC 和 PMIC），去掉它的 OLED 屏、AW88261 功放、PTN36502 等，按 §9.3 加上每个部件的节点；板子文件的 `dtb` 换成它，`sku.py check` 核对接线的标签还在。现在的叠加层合进 Fairphone 5 的树时，`panel@0` 里留着它 OLED 的 `vci-supply`、`dvdd-supply`（ST7701 的驱动不读），自己的树里没有这些。
5. 声卡：`sc8280xp.c` 按 compatible 认板子，要给这块板加一个（内核补丁，送上游），先可以借 `qcom,qcm6490-idp-sndcard`；写 UCM 配置，之后板子才能写 `[audio]`。
6. Yocto：在 kario-bsp 里加一个 `meta-kario-qcm6490` 层和 `kario-qcm6490` 机型（主线内核和 sc7280 的补丁、固件配方、按 `PHONE_IMS` 装 81voltd）；有了它 `make sku-ref-…` 才出固件包，之前停在 bitbake 找不到机型。
7. 在板子上逐项核对 `unverified` 列的值，核对过的删掉；§9.3 里 Kario **未实现**的各项各是一件单独的事。
8. 认证：模组有自己的认证，整机还要做无线电、SAR 等，要做什么看在哪里卖（**未核实**）。

### 9.8 SKU 框架表达不了的

这次框架加了 `[[part]]`（硬件清单，有内核树时核对驱动）、`sku.py parts`、`panel_dsi_out`，内核配置只查本层的机型（§2）。还表达不了、只写在本节和底板的设备树里的：

- 部件在总线上的地址、中断脚、供电（`[[part]]` 只记总线的种类）；
- 摄像头的 CSI 通道、安装方向、标定；
- 天线、射频、SAR；
- 电池的容量和充电参数（在 ADSP 的固件里）；
- 机械的：键帽、外壳尺寸，翻盖的铰链和排线；
- 每个部件 Kario 这边用不用得上（§9.3 的「Kario」一列）；
- 外屏显示什么、什么时候亮：这是 Shell 的策略和产品层的默认值，不是硬件的事实（§8.7.1：SKU 只放硬件事实和功能开关），SKU 只说外屏是什么屏（§9.10.9）；
- OK 键指纹传感器接在哪个 USB 口、电源开关在哪个 GPIO（只写在 `[[part]]` 的名字里，叠加层不生成，底板的设备树写）。

### 9.9 先在一加 6T 上试 Kario

做板子以前，Kario 可以先在一加 6T 上试：[39](39-oneplus-6t.md) 一步一步讲买什么、解锁、构建、`fastboot boot` 先试、刷入，基带、VoLTE（81voltd）、声音的 UCM、GPU、只读的 RTC 这些和参考机一样走高通主线的路；键盘用 USB 或蓝牙的，在 Nura 上 chroot 跑或者刷整机（§6.2）。两部参考机的形态在开发机上已经有：`qemu-qwerty`（640×480 横屏，全键盘）和 `qemu-t9`（480×640 竖屏，九宫格）就是它们的布局，不另加 QEMU 的 SKU；`kario run -sku ref-qwerty-qcm6490-4g`（或 `ref-t9-qcm6490-4g`、`ref-flip-qcm6490-4g`）按参考机的 SKU 模拟（`cmd/kario/skus.json`）。

### 9.10 翻盖：第三部参考机（2026-11-26 调研和设计）

> 状态：板子 `qcm6490-flip`、外屏 `ls013b7dh05`、键盘 `t9-fp-v1`、SKU `ref-flip-qcm6490-4g` 的文件 ✓；框架新加的外屏（`outer_display`、屏文件的 `bus`、`kind`、`color`、`drm_driver`）、合盖开关（`[input] lid`、`[wiring] lid_gpio`）和 OK 键指纹要有部件的检查 ✓，OK 键指纹的大小（键盘的 `fingerprint_mm`、`fingerprint_cap_mm`）和外屏 VCOM 的频率（屏文件的 `vcom_hz`，板子 `outer_vcom` 的周期要落在里面）的检查 ✓，`sku.json` 的 `outer`、`lid`、`fingerprint_at` ✓（`tools/sku_test.py`、`shell` 的 `TestFlipSKU`）；叠加层（外屏在 spi10 上、合盖的 gpio-keys）用 dtc 编译、`fdtoverlay` 合进 6.18 和 7.1-rc1 编出的 Fairphone 5 设备树 ✓（VCOM 的周期从 1 秒改成 16.7 ms 以后没有再编）；每个部件的驱动文件都在两棵内核树里 ✓。合盖开盖里不用外屏的部分 2026-11-27 实现（渲染器报盖子，Shell 熄灭、点亮内屏，锁上、解锁，合着时不算键盘的键，合盖静音；§9.10.8，单元测试和开发机的自测 ✓，**未上真机**）；「合盖挂断」「开盖接听」、外屏、两块屏的渲染（§9.10.8–9.10.11）只是设计，**未实现**；待机的耗电预算（§9.10.10）都是**估计**；硬件**未实现**，每一项在板子上都**未验证**。

摩托罗拉 RAZR V3 / V8 一路的翻盖：便宜、好看，里外两块屏。里面是九宫格和主屏；外面一块小屏，不开盖就能看时间、未接来电和短信的提示、天气，来电时看是谁。合盖由霍尔开关报给系统。OK 键的表面就是指纹传感器：键帽下的面积式传感器，手指放上去不用按就读，只在系统的生物识别提示开着时上电、监听。合着盖要待机 7 天（§9.10.10）。摄像头要能对焦，近距离扫付款码。以后能跑微信、支付宝那样的应用：要什么能力是另一项并行的审计（[12](12-super-app-capabilities.md)、[31](31-lock-and-biometrics.md)），本节不重复，只管硬件和 SKU。

#### 9.10.1 SoC 和内存

还用 QCM6490，和两部直板同一个模组（SG560D-EM）：

- 更便宜的高通芯片在主线上没有移动数据。7.1-rc1 的 IPA 只认 milos、msm8998、sc7180、sc7280、sdm845、sdx55、sdx65、sm6350、sm8350、sm8550；QCM2290 / QRB2210 在 7.1 有了 camss，但没有 IPA，SM6115（§9.1）也没有，msm8953 没有主线的 IPA，msm8916 的 bam-dmux 太老。不能上网的手机跑不了微信、支付宝那样的应用。
- 三部共用一个模组、一套 BSP、一份内核：便宜在这里，不在芯片单价。SG560D 有没有 2 GB 的配置、别家 QCM6490 模组（美格 SRM930、广和通 SC171）的价钱，都**未核实**。

内存 4 GB。负责人的规则（2026-11-26）：先保证用起来流畅、待机长；2 GB 只给只跑 Kario 自己应用的基础九宫格机。

- 翻盖要跑别人的应用（微信、支付宝那一类）。4 GB 放得下它们和系统，切回去不用重新启动；2 GB 能跑，但要靠 zram 压缩和杀进程，切换慢，应用重新启动也费电。
- Fairphone 5 的设备树给固件留了约 492 MiB（基带 246、removed 81、ADSP 40、开机画面 35、CDSP 30、WPSS 25、WLAN 12 等），2 GB 的机器剩给 Linux 约 1.5 GB，1 GB 的只剩约 530 MB。
- 系统自己约 190 MB（QEMU 里连模拟的云服务 234 MB）；一个网页页面约 250 MB，网页进程的上限至少 384 MB。微信那样的应用是聊天、小程序的运行时加网页，扫码时还有摄像头的缓冲。只跑 Kario 自己的应用，2 GB 绰绰有余。
- 待机的代价：多出的 2 GB 自刷新约 1.4–1.8 mW（Micron LPDDR4X Z42N 数据手册的 IDD6，每颗 16 Gb，25–45 °C），电池端约 0.4–0.5 mA。7 天的预算放得下：4 GB 约 9.2 天，2 GB 约 9.8 天（§9.10.10，**估计**）。

#### 9.10.2 硬件

只列和 §9.3 不同的；其余同 §9.3，去掉了前摄像头、NFC 和通知灯（外屏替它提示）。

| 部件 | 型号 | 接口 | 主线驱动 | Kario | 状态 |
|------|------|------|---------|-------|------|
| 内屏 | Densitron DMT028VGHMCMI-1A，2.8 寸 480×640 IPS，竖装 | DSI 2 通道，过铰链 | `densitron,dmt028vghmcmi-1a`、`sitronix,st7701`，`drivers/gpu/drm/panel/panel-sitronix-st7701.c` | 和九宫格直板一样 | 叠加层合进去 ✓，**未上真机** |
| 外屏 | Sharp LS013B7DH05，1.26 寸 144×168 黑白记忆屏（反射式，20.88 × 24.36 mm）；带前光的同一块屏是 Azumo 11655-03 | SPI（spi10，GPIO 40–43，1 MHz，片选高有效），使能 GPIO 82，VCOM 接 LPG 的第一个 PWM（64.1 Hz，数据手册要 57–66 Hz） | `sharp,ls013b7dh05`，`drivers/gpu/drm/tiny/sharp-memory.c`（`CONFIG_TINYDRM_SHARP_MEMORY`，DRM 驱动名 `sharp_memory_display`） | `sku.json` 的 `outer`；Shell 和渲染器不画它（§9.10.11） | 叠加层合进去 ✓，VCOM 频率的检查 ✓，Kario **未实现** |
| 合盖开关 | TI DRV5032 霍尔开关，磁铁在上半部 | GPIO 90（PDC 能唤醒） | `gpio-keys`，`drivers/input/keyboard/gpio_keys.c`（`EV_SW`、`SW_LID`） | `[input] lid = "Lid"`，`sku.json` 的 `lid`；phone-paint 读它、报给 Shell（§9.10.8） | 叠加层合进去 ✓；Kario 这边单元测试和开发机的自测 ✓，**未上真机** |
| OK 键的指纹 | 电容式面积传感器做 OK 键的表面（§9.10.5）：FPC1025 一类，160×160 像素、508 dpi，感应面 8 × 8 mm，12 × 12 mm 的封装就是键帽；片上比对（match-on-chip）在模组的 MCU 里 | 传感器到 MCU 是 SPI（模组里面）；模组到 SoC 是 USB 2.0 全速，接 usb_2 | 内核不用驱动（USB 设备）；比对在 libfprint（LGPL）的片上比对驱动（goodixmoc、fpcmoc、elanmoc、synaptics、egismoc），**主线没有指纹子系统**（`driver = "none"`） | 键盘的 `fingerprint = "ok-key"`、`fingerprint_mm = [8.0, 8.0]`、`fingerprint_cap_mm = [12.0, 12.0]`（`sku.py` 查感应面在键帽里），`sku.json` 的 `fingerprint_at`；keystored 没有这种传感器的后端 | 键帽大小、libfprint 认的模组买不买得到**未核实**；Kario **未实现** |
| 第二个 USB 控制器 | QCM6490 的 usb_2（只有 USB 2.0） | SoC | `qcom,snps-dwc3`，`drivers/usb/dwc3/dwc3-qcom.c` | 只接指纹传感器 | **未验证** |
| 指纹的电源开关 | GPIO 83 控制的负载开关 | GPIO | `regulator-output`，`drivers/regulator/userspace-consumer.c`（开关本身是 `regulator-fixed`，`drivers/regulator/fixed.c`） | keystored 在提示时写 sysfs 的 `state` 上电 | Kario **未实现** |
| 摄像头 | IMX258 13 MP 加 DW9714 对焦，在上半部的外面 | CSI-2 2 通道，过铰链 | `sony,imx258`、`dongwoon,dw9714`（同 §9.3） | 扫码：§9.10.6 | Kario **未实现** |
| LPG | PM8350C：通道 1 给外屏的 VCOM，通道 2 键盘背光，通道 3 外屏的前光，PWM 3 给内屏的背光 | SPMI | `qcom,pm8350c-pwm`，`drivers/leds/rgb/leds-qcom-lpg.c` | `keyboard_backlight = "white:kbd_backlight"`，没有 `[leds]` | **未验证**；VCOM 的 16.7 ms 周期，驱动选 32 kHz 时钟、9 位，出 64.1 Hz（照 `lpg_calc_freq` 算的 ✓），32 kHz 时钟挂起时还走，挂起时真出不出 PWM **未验证**；通道 3 灌 20 mA 够不够**未核实** |

接线（板子文件的 `[wiring]`）：键盘还在 i2c9 的 0x34，中断 GPIO 77（和直板一样）；合盖 GPIO 90；外屏的使能 GPIO 82；指纹的电源 GPIO 83。这四个脚在 6.18 和 7.1-rc1 的 Fairphone 5 树里都空着、不在 QUP 的脚上，77、90 在 SC7280 的 PDC 唤醒表里（`sku.py check` 核对，§2）。合盖原来在 GPIO 78：它在 7.1-rc1 的 Fairphone 5 树里是前摄像头（S5KJN1）的复位脚。`sku.py parts qcm6490-flip` 打出整张清单。

直板的键盘中断原来在 GPIO 64，不在 `sc7280_pdc_map` 里（6.18 和 7.1-rc1 都是：60、61、63、68 有，64 没有），深度睡眠时按键可能叫不醒手机；2026-11-26 换到了 GPIO 77，`sku.py check` 从此查每块板子的唤醒脚（§2）。翻盖原来打算用的 GPIO 52、54 是 Fairphone 5 触摸屏的 spi13，48 是 spi12 的，都不能用。

#### 9.10.3 外屏选哪块（按待机选）

外屏要一直显示时间，所以比的是「一直显示的时钟」要多少电。功耗是数据手册的数；折到电池端、一天多少是**估计**，按 3.85 V 的电池算，和 7 天的预算（一天约 231 mAh，§9.10.10）比：

| 屏 | 种类 | 一直显示时的功耗（数据手册） | 电池端、一天（**估计**） | 黑暗里 | 主线驱动 | 单价（**估计**） |
|----|------|-------------|-------------|-------|---------|-------------|
| Sharp LS013B7DH05，1.26 寸 144×168（选这块） | 记忆屏，黑白，反射式 | 保持画面 25 µW（最大 125 µW），每秒改一次 35 µW（最大 150 µW），在 VCOM 60 Hz 时量的（Sharp 规格书 LD-27503A，表 6-5） | 约 0.01 mA，约 0.2 mAh | 看不见；带前光的同一块屏（Azumo 11655-03）LED 20 mA、3 V 时 60 mW，按键时亮几秒 | `sharp,ls013b7dh05`，`drivers/gpu/drm/tiny/sharp-memory.c`（6.18、7.1-rc1 都有） | 10 美元，带前光的**未核实** |
| SSD1306 OLED，0.96 寸 128×64 | OLED，黑白，自己发光 | 逻辑 180 µA；屏在一半像素亮、9 V 时 7.3 mA（约 66 mW），用芯片里的升压时电池端 18.8 mA；睡眠 1 µA；亮度 10000 小时降到一半（EastRising ER-OLED0.96 数据手册） | 只亮时间那 10–20% 的像素，约 2–4 mA，50–100 mAh；常亮约 14 个月亮度掉一半，而且烧屏 | 自己发光 | `solomon,ssd1306`、`sinowealth,sh1106` 等，`drivers/gpu/drm/solomon/ssd130x.c`（加 `ssd130x-spi.c`、`ssd130x-i2c.c`） | 2–4 美元 |
| ST7735S TFT，1.44 寸 128×128 | LCD，彩色，透射式 | 背光 15 mA、3.2 V 时 48 mW（Orient AFK128128A1），20 mA 时 64 mW（Rocktech RK014CE06A）；逻辑约 10 mA（4D Systems 4DLCD-144） | 约 25 mA，约 600 mAh，一天就是预算的 2.6 倍；背光调到 10% 也还要几 mA | 有背光 | `sitronix,st7735r`，`drivers/gpu/drm/sitronix/st7735r.c`（6.18、7.1-rc1 都在这里，原来在 tiny/）；别的 MIPI DBI 小屏 `drivers/gpu/drm/tiny/panel-mipi-dbi.c` | 2–3 美元 |

选记忆屏：

- 只有它能一直显示又几乎不费电。一直显示的时钟在 OLED 上一天要 50–100 mAh，是 7 天预算的四分之一到一半，还会烧屏；TFT 一天几百 mAh，只能亮几秒就灭，那就不是「不开盖看时间」了。
- 代价是黑白、小，黑暗里看不见。前光用同一块屏带前光的模组（Azumo 11655-03），接 LPG 的通道 3，按键时亮约 3 秒：一天 30 次约 5 J，约 0.4 mAh。前光模组的价钱、厚度**未核实**。
- 电子纸也不费电，但刷新要几百毫秒到几秒，主线没有这种小尺寸的驱动。
- QCM6490 只有一个 DSI 主机（`mdss_dsi`，没有 dsi1），内屏占了它，所以外屏只能走 SPI；SPI 的小屏在主线里都是一块屏一个 DRM 设备。
- 数据手册的另外几条：SPI 最快 1.1 MHz（板子用 1 MHz）；VCOM（EXTCOMIN）57–66 Hz，上面的功耗都是 60 Hz 时量的，所以 VCOM 照它出 64.1 Hz（原来的 1 秒不合规格书）；静止的画面至少 2 小时重写一次。

#### 9.10.4 合盖开关和铰链

- 霍尔开关：TI DRV5032 是 µA 级的（数据手册的数，选哪个后缀**未核实**），推挽输出的型号不用上拉；合上时磁铁靠近、输出变低，gpio-keys 报 `SW_LID = 1`。一加 6T 的设备树就是这样接的（`sdm845-oneplus-common.dtsi` 的 “Hall Effect Sensor”）。脚要在 PDC 的唤醒表里，开盖才叫得醒手机（`sku.py check` 核对，§2）；logind 已经不管合盖（[28](28-battery.md)），合盖的事由 Shell 做（§9.10.8）。
- 铰链的排线过：内屏的 DSI（2 对数据加时钟）、外屏的 SPI 加使能和 VCOM、摄像头的 CSI（2 对加时钟）和 CCI、上半部传感器的 I2C、听筒、供电和地，约 30–40 根，差分线 100 Ω；排线要经得起 10–20 万次开合（**估计**）。上半部只有内屏、外屏、摄像头、闪光灯、听筒和光线距离传感器（朝里，挨着听筒）；天线放在下半部，离铰链的金属远一点。

#### 9.10.5 OK 键的表面就是指纹传感器

- 在哪、怎么读：导航键中间的 OK 键，键帽的表面就是传感器。它是电容式的面积传感器（不是要划过去的条形传感器），涂层封装，手指放上去就读，不用按；键帽下面还是 OK 键的锅仔片，用力按下去才算按了 OK。键帽 12 × 12 mm、圆角，方向键围一圈，整个导航键区约 28 mm（**估计**）。
- 尽量大：比对靠的是感应面上的细节点，面积越大，细节点越多，拒识越少。笔记本电源键里的汇顶传感器只有约 4 × 3.25 mm（13 mm²）；FPC1025 一类是 160×160 像素、508 dpi，感应面 8 × 8 mm（64 mm²），封装 12 × 12 mm（也有 14 mm 的圆封装），就是能做的最大键帽。再大的 FPC1020（9.6 × 9.6 mm）封装 14 × 14 mm，导航键区就太大了。
- SKU：键盘文件写 `fingerprint = "ok-key"`、`fingerprint_mm = [8.0, 8.0]`（感应面）、`fingerprint_cap_mm = [12.0, 12.0]`（键帽），都是宽、高的毫米数。`sku.py` 查感应面放得进键帽，真板子的键盘两个都要写，没有传感器的键盘不能写。大小不进 `sku.json`：运行时没人读，是给做板子的人的。
- 只在系统的生物识别提示开着时上电、监听：平时负载开关（GPIO 83）断开，传感器和它的 MCU 都不耗电；keystored 为系统的提示上电，手指放上去就采图、比对，提示关了就断电（[31](31-lock-and-biometrics.md) §4.5）。按 OK 键每次都碰到它，所以它从不用来解锁，应用也打不开它。提示开着的几秒里模组约几十 mA（**估计**）。确认框和录入界面照这个决定写的是把手指放在 OK 键上、不用按下（[31](31-lock-and-biometrics.md) §4.5）。
- 实话：主线内核没有指纹子系统。手机上常见的 SPI 指纹传感器直接接 SoC 时，比对要厂商在 TEE 里的程序，我们没有，做不了。
- 所以用片上比对的模组：传感器（SPI）接模组自己的一颗 MCU，模板存在 MCU 里、比对也在 MCU 里，主机经 USB 2.0 全速只收「对上了」。驱动是用户态的 libfprint（LGPL），它的片上比对驱动有 goodixmoc（汇顶）、fpcmoc（FPC）、elanmoc（义隆）、synaptics、egismoc（神盾）；按 §8.4 放进单独的、进沙箱的进程，Shell 和系统服务不链接它。keystored 要加这种「片上比对」的后端（**未实现**）。

候选（尺寸是数据手册的；键帽大小、libfprint 认的 USB 模组买不买得到，都**未核实**）：

| 传感器 | 感应面 | 封装 | 接口 | 比对在哪 | 合不合适 |
|------|------|------|------|------|------|
| FPC1025 加一颗 USB 的 MCU（FPC 的片上比对方案） | 8.0 × 8.0 mm（160×160，508 dpi） | 12 × 12 mm 方，或 14 mm 圆 | 传感器 SPI，模组 USB | 模组的 MCU | 选这一类；采图 5 mA，深睡 1 µA；fpcmoc 认不认这样的模组**未核实** |
| 汇顶 GF3258 / GF5288（前置电容式） | **未核实** | 12 × 12 mm | 传感器 SPI | 汇顶的片上比对方案（门锁用，带安全 MCU） | 同上，goodixmoc 认不认**未核实** |
| FPC1020 | 9.6 × 9.6 mm（192×192） | 14 × 14 mm | SPI | 要另配 MCU | 键帽要 14 mm，导航键区太大 |
| 笔记本电源键的片上比对传感器（汇顶等） | 约 4 × 3.25 mm | 按键形 | USB | 传感器里 | libfprint 认，但面积太小 |
| Toyonway TFP821 模组（FPC1025AM 加 ARM9） | 8 × 8 mm | 20 × 33 mm 的板 | 串口或 USB | 模组上 | 自己的协议，主机和模组之间没有认证（**未核实**有没有加密的版本），不能用在支付上 |

- 另一种：串口的指纹模组（Grow R502 那一类），协议是公开的，可主机和模组之间不加密、不认证，换一个模组就能说「对上了」，不能用在支付上。
- 支付和安全提示怎么用它、要什么能力：另一项并行的工作（[31](31-lock-and-biometrics.md)、[12](12-super-app-capabilities.md)），本节不重复。

#### 9.10.6 摄像头和扫码

IMX258 在两条 CSI 通道上：全尺寸 4208×3120 最多 15 帧，合并像素的 2104×1560 够扫码；DW9714 对焦，付款码要在 10 cm 左右对清楚。不装前摄像头：省钱，铰链的排线也窄一点；代价是不能视频通话时露脸。闪光灯留着，暗处扫码用。扫码要的都**未实现**：libcamera 的软件 ISP、自动曝光和对焦的循环、二维码识别（比如 quirc，ISC 许可）。

#### 9.10.7 耳机孔、电池、USB-C

- 3.5 mm 耳机孔：要，理由同 §9.5；放在下半部的底边（和电池摆不摆得下**未核实**）。FM 不要，同 §9.5。
- 电池约 1800 mAh（约 4.0 × 45 × 65 mm，**估计**），在下半部的键盘下面；合着盖待机的目标是 7 天，按 §9.10.10 的预算约 9 天（**估计**）。
- USB-C 同 §9.3（pmic_glink）。

#### 9.10.8 合盖和开盖（不用外屏的部分实现，**未上真机**；其余是设计）

- 开盖：唤醒，点亮内屏。有来电、设置了「开盖接听」时接听。没有锁屏 PIN 码时开盖就解开键盘锁；有 PIN 码时显示输入 PIN 码。
- 合盖：内屏马上熄灭，像熄屏一样锁上。正在通话时挂断（设置「合盖挂断」，默认开），但用着耳机、免提或蓝牙时不挂。响铃时合盖是静音。闹钟照响（侧面的键稍后提醒），媒体照放。
- 合着时键盘矩阵的键都不算（盖子压着）；电源键刷新外屏，有前光时亮几秒。
- 合盖开关是唤醒源；渲染器按 `sku.json` 的 `lid` 打开这个输入设备，读 `SW_LID`，交给 Shell（[23](23-renderer-protocol.md) 的 `lid`）。

做了的（2026-11-27；Rust 单元测试 `paint/src/lid.rs`，Go 单元测试 `shell/lid_test.go`，开发机的自测用例「翻盖的盖子」✓；**未上真机**）：

- 渲染器：phone-paint 按握手里 `device` 的 `lid` 打开那个输入设备（输入助手交过来，和声音开关一样），启动时用 `EVIOCGSW` 读一次，之后每动一下发 `{"t":"lid","shut":true|false}`；读不了、设备没了时报开着。它在 `ready` 里声明 `lid`；没声明的渲染器（老的、无头的、phone-gui）Shell 当盖子开着。协议是 `phone.frame/1` 的次版本增加，两边新旧搭配都不出错（[23](23-renderer-protocol.md) §2）。
- Shell（`shell/lid.go`），只照变化做事：
  - 合盖：响着的来电静音（来电还在，开盖就看到，写着「铃声已静音」）；内屏熄灭，锁和熄屏一样（有 PIN 码时过了宽限期锁上，宽限 0 就马上锁；没有 PIN 码不锁，和熄屏一样）。
  - 合着时：内屏不亮，来电、闹钟、通知、充电、自测脚本都点不亮它；按键都丢掉（日志记第一个和开盖时的总数），只有电源键、音量键、两个音量键的辅助功能快捷方式、耳机的媒体键和电脑发来的键（`kario device key`）还算；按键不说自己来自哪个设备，接着的 USB、蓝牙键盘的键也一起丢掉。电源键只做两件事：静音响着的来电、稍后提醒响着的闹钟，和开着盖时一样；别的都不做（不点亮内屏，不开电源菜单：那里没人能回答它；不开快速选卡），等外屏画出来再刷新外屏（§9.10.9）。音量键在合着时直接起作用（开着盖熄屏时第一下只点亮屏幕）。
  - 开盖：先补上合着时到期的锁，再点亮内屏，键盘的自动锁从开盖时重新计时；没有 PIN 码时解开键盘锁；有 PIN 码而且锁着时打开输入 PIN 码的界面（来电、闹钟这类浮层在前面、正在通话时不打开）。
- 闹钟和媒体不变：合着时闹钟照响，电源键稍后提醒；音量键照旧调音量，侧面的键稍后提醒闹钟**未实现**。

没做的：

- 「合盖挂断」「开盖接听」：现在没有通话的设置页（拦截号码在「隐私与安全」里，VoLTE 在「移动网络」里，拨号应用没有设置），放不进现有的页面，只有设计。另外「用着耳机、免提或蓝牙时不挂」要 Shell 知道声音走哪路：phone-audiod 不读耳机插孔（§9.3），也没有蓝牙的声音路由，Shell 不记免提开没开（只把通话应用的 `audio.route` 转给 phone-audiod）。
- 外屏（§9.10.9）、电源键刷新外屏、前光。

#### 9.10.9 外屏显示什么、什么时候亮（设计，**未实现**）

- 一直显示：时间、日期、电量、信号。
- 有了才显示：未接来电和短信（照锁屏的隐私设置，默认只显示数目）、天气（来自一个「一瞥」的数据源，**未实现**）、来电是谁、闹钟和计时器、充电。
- 开着盖时只显示时间。
- 什么时候更新：有事件时；合着时每分钟一次，只改时间那几行，走 §9.10.10 的短唤醒；每小时整个重写一次（数据手册：静止的画面至少 2 小时重写一次）；按电源键时，前光亮约 3 秒。记忆屏一直显示；OLED 和 LCD 的外屏亮 5 秒就灭。
- 默认值放在产品层（profile.d），设置在 设置 › 显示 › 外屏（**未实现**）。显示什么是 Shell 的策略，不进 SKU；SKU 只说外屏是什么屏（`kind`、`color`、大小）。

#### 9.10.10 待机：7 天的耗电预算（**估计**）

条件：合着盖、不用；驻留 LTE，寻呼周期 1.28 秒（PF=128，网络定的）；Wi-Fi 关；外屏显示时间，每分钟改一次；推送连着，学到的心跳 28 分钟（[21](21-push.md)）。电池 1800 mAh、3.85 V（约 6.9 Wh）：7 天是 168 小时，平均不能超过 10.7 mA；留 10% 给老化、低温和电量计的误差，是 9.6 mA，一天 231 mAh。每一行都是**估计**：数据手册的数是厂商在自己的系统上量的，在主线 Linux 上都**未验证**。

| 部件 | 待机时的状态 | 电池端平均（mA） | 一天（mAh） | 依据 |
|------|------|-----:|-----:|------|
| 模组睡眠：SoC 挂起、PMIC、基带 DRX 寻呼、4 GB 内存自刷新 | s2idle，SoC 进 AOSD、CXSD | 5.9 | 142 | Quectel SG560D 硬件设计手册 V2.0（2024-03-05）§6.3，USB 断开：LTE-FDD PF=128 5.80 mA，LTE-TDD 6.04 mA；PF=64 6.71，PF=256 5.34 |
| 其中内存自刷新 | 4 GB（2 GB 少约 0.45 mA） | 0.9–1.1 | 算在上一行 | Micron LPDDR4X Z42N 的 IDD6：每颗 16 Gb，VDD1 0.32 mA、VDD2 0.77 mA（25 °C） |
| 内屏和背光 | 关，DSI 和电源都断 | 约 0 | 0 | |
| 外屏保持画面，LPG 出 VCOM | 记忆屏保持，VCOM 64.1 Hz | 0.06 | 1.4 | 屏 25 µW（§9.10.3），约 0.01 mA；LPG 在 32 kHz 时钟上的电流**未核实**，按 0.05 mA 留 |
| 外屏每分钟改时间 | 一天 1440 次短唤醒，每次 50–100 mJ | 0.22–0.43 | 5–10 | 恢复、挂起约 0.2–0.3 秒，约 0.3 W；SPI 发时间那几行几毫秒（一整帧 168 行 1 MHz 约 27 ms） |
| 推送心跳 | 学到 28 分钟，走 LTE，一天 51 次，每次 0.455 mAh | 0.98 | 23.4 | [28](28-battery.md) §8 的唤醒模型（网络的尾巴 10 秒，醒着 10 秒）；LTE 的量级见 Huang 等，MobiSys 2012：升到连接态 1.2 W、0.26 秒，尾巴 1.06 W、11.6 秒 |
| 例行唤醒 | 并进心跳的唤醒 | 0 | 0 | 不并、每 15 分钟一次是一天 96 次、每次约 1 J：0.30 mA，7.3 mAh |
| 基带叫醒主机 | 来电、短信，一天约 20 次 | 0.06 | 1.4 | 每次醒着 10 秒，约 1 J |
| 传感器和小芯片 | VCNL4040 关断 0.2 µA，BMA400 睡眠 0.16 µA，DRV5032 1.3–1.6 µA，TCA8418 空闲 3 µA，DRV2605L 待机 4.1 µA；指纹断电 | 0.01 | 0.2 | 各自的数据手册 |
| Wi-Fi | 关 | 0 | 0 | |
| 合计 | | 约 7.3 | 约 176 | 1800 mAh 约 10.2 天，留 10% 约 9.2 天 |

Wi-Fi 开着（连着、省电模式、广播不叫醒主机）：多约 0.5–1.5 mA（WCN6750 的数不公开；Huang 2012 量的旧手机收信标平均约 1.6 mW）。心跳改走 Wi-Fi，网络几乎不花电，一次只剩醒着的 10 秒，约 1 J，平均 0.16 mA 而不是 0.98。合计约 7.0–8.0 mA，留 10% 约 8.4–9.6 天。基带照样驻留 LTE 等来电，模组那一行不变。

哪些情况会达不到（别的照上表）：

| 情况 | 合计（mA） | 留 10% 的天数 |
|------|-----:|-----:|
| 上表 | 7.3 | 9.2 |
| 内存 2 GB（对照） | 6.9 | 9.8 |
| 例行唤醒不并进心跳，每 15 分钟一次 | 7.6 | 8.8 |
| 心跳 10 分钟 | 9.1 | 7.4 |
| 心跳 5 分钟（运营商的 NAT 超时短） | 11.8 | 5.7 |
| 外屏每分钟醒来都留 10 秒 | 11.3 | 6.0 |
| 网络给的寻呼周期 PF=32（0.32 秒） | 9.5 | 7.1 |
| 驻留 5G SA（手册里 5G NR 的空闲多约 0.9 mA） | 8.2 | 8.2 |
| 主线的睡眠电流比手册高 2 mA | 9.3 | 7.2 |
| 基带每 5 分钟叫醒主机一次（一天 300 次） | 8.2 | 8.3 |

心跳间隔的下限：别的照上表，留 10% 要 7 天，网络的尾巴 10 秒时学到的心跳不能短于约 8.3 分钟，20 秒时不能短于约 15 分钟。余量约 2.3 mA：主线的睡眠电流比手册高出 2 mA 以上，就吃完了。

软件上必须成立的（都是**估计**出来的条件；没注明的都**未实现**）：

1. 挂起：熄屏 30 秒后挂起（[28](28-battery.md) §3 已有）。挂起时 SoC 要进 AOSD 和 CXSD：debugfs 的 `qcom_stats` 里 `aosd`、`cxsd` 的计数每次挂起都涨，`suspend_stats` 的失败不涨；每个驱动都放掉投票：MDSS 关，usb_2 和指纹的负载开关断，dwc3 挂起，ath11k 关或者进 WoW。量出来的睡眠电流要在手册的 0.5 mA 以内（**未验证**）。
2. 唤醒源只有这些：基带（GLINK 的 IPCRTR 通道，6T 的 `phone-qcom-wake` udev 规则打开它，翻盖的机型也要带上）、RTC 闹钟、合盖 GPIO 90、键盘 GPIO 77、电源键（PMK8350 的 KPDPWR）、充电（pmic_glink）。加速度计、光线距离、指纹、Wi-Fi 的广播都不能叫醒。
3. 外屏的分钟更新走短路径：RTC 每分钟叫醒，只画时间那几行（SPI 几毫秒），马上再挂起，不留 28 §3 的 10 秒；同一分钟的别的闹钟合进来；每小时整个重写一次。Shell 的睡眠策略要多一种「外屏唤醒」。Shell 这边做了（[28](28-battery.md) §3：外屏睡着保持画面时，RTC 在每分钟的第 1 秒叫醒，改完时间马上再挂起；13 秒内有别的事到期就是一次普通唤醒，照留 10 秒；单元测试 ✓，**未上真机**）；画时间那几行和每小时重写要等渲染器（§9.10.11，**未实现**）。
4. 周期唤醒只有一种：合着盖时，28 §3 的「最迟 15 分钟」例行唤醒放宽到推送学到的心跳（最长 28 分钟），任务跟着心跳的那次唤醒跑。Shell 这边做了（[28](28-battery.md) §3：RTC 定在心跳后 10 秒兜底，推送连接靠不上时照旧 15 分钟；单元测试 ✓，**未上真机**）；盖子的位置由渲染器报（§9.10.8，phone-paint 的 Rust 单元测试 ✓，**未上真机**）。
5. 心跳间隔：在移动网络上学到 15 分钟以上才稳；运营商的 NAT 短到 8 分钟以下时 7 天达不到，设置 › 电池要看得出原因。
6. 基带：熄屏时关掉或者设门限的通告（信号强度、小区信息、网络时间），只留来电、短信、数据；待机驻留 LTE（5G SA 空闲多约 1 mA）。ModemManager 怎么做到**未核实**。
7. Wi-Fi：没连上时关掉（不在后台扫描）；连上时开省电，主机只被发给它的单播叫醒（ARP、NS 卸载，WoWLAN；ath11k 在 WCN6750 上行不行**未核实**）。
8. 指纹模组和 usb_2 只在系统的生物识别提示开着时上电（§9.10.5）；前光只在按键时亮约 3 秒（§9.10.3）。
9. 退路：要是每分钟叫醒一次 SoC 量出来比估的贵得多，加一颗 µA 级的小 MCU 自己走时间、画外屏，SoC 只在有事时给它发内容（板子要改）。

#### 9.10.11 渲染器怎么驱动两块屏（设计，**未实现**）

- 手机上是两个 DRM 设备：msm（内屏）和 `sharp_memory_display`（外屏），谁是 card0 不一定。现在 phone-paint 的 `Display::open()` 依次试 card0、card1，用第一个接着屏的连接器，可能打开外屏：要改成按驱动名（`DRM_IOCTL_VERSION`）选，外屏是 `sku.json` 的 `outer.drm_driver`，内屏是另一个。
- 外屏：dumb buffer，XRGB8888（驱动转成 1 位），没有 vblank；交脏区域（`DIRTYFB`），驱动只发改了的行。按 1 位画：不抗锯齿、按阈值；中文用点阵字体（比如 12 px 的 Fusion Pixel，OFL，许可要查）。
- 协议：[23](23-renderer-protocol.md) 加一个「外屏」的目标；外屏的画面由 Shell 自己排，应用只经通知和「一瞥」的数据给内容。合盖时内屏关掉（DPMS 关或者关 CRTC）。
- 外屏的 DRM 设备会有 fbdev 模拟，fbcon 可能画到上面：要用 `fbcon=map:` 或者内核配置避开（**未验证**）。
- QEMU：有虚拟外屏的 SKU，`gen` 在 `QB_GRAPHICS` 里多加一个外屏大小的 `virtio-gpu-pci`（单元测试 ✓，没跑过：还没有这样的 QEMU SKU），像手机一样是第二个 DRM 设备；两个都是 `virtio_gpu`，渲染器按顺序把第二个当外屏。合盖用客户机里的 uinput 设备模拟（像通知灯用 uleds），因为 QMP 发不了 `EV_SW`。

#### 9.10.12 物料（价格都是**估计**，美元，10 台的小批量）

| 部件 | 型号 | 单价 | 说明 |
|------|------|-----:|------|
| 模组 | Quectel SG560D-EM（4 GB + 64 GB） | 260 | 同 §9.6 |
| 内屏 | Densitron DMT028VGHMCMI-1A | 25 | |
| 外屏 | Sharp LS013B7DH05，带前光（Azumo 11655-03） | 18 | 不带前光 10；带前光的价钱**未核实** |
| 背光驱动 | PWM 调光的升压 LED 驱动 | 1 | |
| 键盘 | TCA8418 加锅仔片、薄膜、键帽 | 15 | 比直板少了全键盘的键帽 |
| 指纹 | FPC1025 一类的面积传感器做 12 mm 的键帽，加片上比对的 USB MCU | 15 | 买不买得到**未核实** |
| 摄像头 | IMX258 自动对焦模组 | 15 | |
| 传感器 | VCNL4040、BMA400 | 3 | |
| 振动 | DRV2605L 加线性马达 | 4 | |
| 声音 | 功放、扬声器、听筒、麦克风、3.5 mm 插孔 | 8 | |
| 霍尔开关 | DRV5032 加磁铁 | 1 | |
| 铰链 | 带凸轮和弹簧的铰链 | 6 | |
| 排线 | 过铰链的柔性排线 | 5 | |
| 电源 | 约 1800 mAh 电池、USB-C 座、保护板 | 12 | |
| 射频 | 天线、nano-SIM 座 | 12 | |
| 电路板 | 下半部的底板加上半部的小板 | 70 | |
| 外壳 | 上下两半，3D 打印或 CNC | 50 | |
| 合计 | | 约 520 | 每台；不算开模、认证和人工 |

#### 9.10.13 做一台要做的

1. §9.7 的每一步（模组的资料和固件、引导主线内核、底板、设备树、声卡、Yocto 机型、认证）。
2. 铰链和排线：先买现成的翻盖铰链试开合寿命，排线按 §9.10.4 的线数和阻抗画。
3. 底板的设备树：外屏和合盖由叠加层生成（§3），自己的树里也可以直接写；另加 usb_2 和它的 PHY、指纹的负载开关（`regulator-fixed`）和 `regulator-output`。
4. BSP 层的内核配置要有 `CONFIG_TINYDRM_SHARP_MEMORY`、`CONFIG_SPI_QCOM_GENI`、`CONFIG_KEYBOARD_GPIO`、`CONFIG_REGULATOR_USERSPACE_CONSUMER`、`CONFIG_USB_DWC3_QCOM`（这台机型不是本层的，`sku.py` 不查）。
5. 找指纹传感器：12 mm 的键帽放得下、感应面 8 × 8 mm 左右、libfprint 认的片上比对 USB 模组（FPC1025 或者汇顶的前置电容式，加 MCU）；找不到就先不装，`fingerprint`、`fingerprint_mm`、`fingerprint_cap_mm` 从键盘文件里去掉。
6. 量待机（§9.10.10）：USB 断开、合着盖，在电池端接电源分析仪；看 `qcom_stats` 的 `aosd`、`cxsd` 计数涨不涨；挂起时用示波器看外屏的 EXTCOMIN 还有没有 64 Hz；照量出来的数改 §9.10.10 的表。
7. Kario 这边各是一件单独的事：渲染器两块屏（§9.10.11）、Shell 的合盖开盖（不用外屏的部分做了，「合盖挂断」「开盖接听」没做，§9.10.8）和外屏（§9.10.9）、外屏的短唤醒和合盖时的例行唤醒（§9.10.10）、keystored 的片上比对后端（§9.10.5）、摄像头扫码（§9.10.6）。
