# 设计不足与未验证的地方

> 2026-11-21 在一加 6T（A6010，OxygenOS 11.1.2.2）上准备真机时（备份、构建 Nura、准备 chroot 测试，[39](39-oneplus-6t.md) §9.1）整理的**架构层面的缺口**，同一天按当前的文档和代码核对过。这里只记设计上还没解决、或者靠着不可靠前提的地方；细节在各主题文档里，每条都链过去。
> 状态：清单，不是计划。**未实现**、**未上真机**、**未验证** 照各主题文档；标 **正在做** 的是别人此刻在改的，不算已经好了。

## 1. 真机验证几乎为零

- 真机上的测试**还没有开始**（[40](40-testing.md) §7）；[13](13-prototype.md) 的结果都来自开发机和 QEMU。
- 2026-11-21 一台 6T 在另一台电脑上做了准备：分区备份走的是 Nura 启动镜像的调试 shell，`boot_a` 里是 Nura 的启动镜像（[39](39-oneplus-6t.md) §9.1）。同一天 chroot 套件（`tools/phone-chroot/`）第一次在它上面跑：自测停在第一个失败的用例（§11），`shell-start` 之后手机卡死、进了崩溃转储（§13，**未查明**）。Kario 自己的镜像（整机）还没有在它上面跑过。要到手机上核对的事列在 [39](39-oneplus-6t.md) §14。

**影响**：所有「能用」的结论都只覆盖模拟的环境。通话的声音、射频、功耗、真屏的刷新、传感器、温度这些只能在真机上看的，现在都没有数据。

## 2. 渲染与显示

- **GPU**：phone-paint 默认在 CPU 上栅格化、在 CPU 上合成。可选的 GPU 合成已经有了（`PHONE_PAINT_GPU=1`：GLES 2 合成，经 GBM 翻页上屏，[37](37-motion.md) §5 第 8 条），在开发机的 llvmpipe 上测过；GBM 和翻页那部分在 QEMU 上用软件 GPU 验证过（Mesa 的 softpipe 进了 QEMU 镜像，[42](42-gpu-rendering.md) §7）。Adreno 630 上**未上真机**。在 GPU 上栅格化也有了，同样可选（`PHONE_PAINT_GPU=raster`，[37](37-motion.md) §5 第 10 条、§2.5）：llvmpipe 上 GPU 画的每种操作和 CPU 逐字节相同，值不值得、帧时间、功耗**未上真机**。1080×2340、60 Hz 在骁龙 845 上的 CPU 占用和功耗没有数据（[37](37-motion.md) §2 是估算）。
- **DRM 独占**：phone-paint 自己持有面板（DRM master，[08](08-display-adaptive-refresh.md)），和任何显示管理器都冲突：chroot 套件的 `setup` 先停掉 tinydm、greetd、sddm、lightdm、gdm（`tools/phone-chroot/on-phone.sh`），Nura 要装成 console 界面（[24](24-sku-framework.md) §6.2）。也就是说，底座上不能同时跑着另一个图形界面，Kario 也不能当一个窗口跑在别的桌面里。
- **文本界面显示不了设备屏上的中文**：Linux 控制台最多 512 个字形（[09](09-ui-feasibility-review.md) §3.2），所以设备的屏幕只能走 phone-paint；文本界面只给 SSH、串口和调试用。
- **像素基线还没录**：快照比对有了（[40](40-testing.md) §2）：文本界面的基线在 git 里，`make selftest` 逐张比对；像素快照比的是每张解码后像素的哈希，按渲染器、形态和字体分开存，基线还没录（要在开发机上整份跑 `make pixel-baselines`，CI 另要一份），**未验证**。还在动的画面（播放中的进度条、摄像头画面、视频）列在 `shell/testdata/snapshots/unstable.txt`，写着原因。
- **没有覆盖率统计**（**未实现**，[40](40-testing.md) §7）。
- **两种界面的一致没有自动比对**：[06](06-gui-architecture.md) §3 说文档按字符格排版，断行、禁则、Mermaid 图在文本和像素两种界面下一样；有单元测试的是 Mermaid 的 9 种图（[14](14-tui-route-and-app-framework.md)），没有文本界面和 phone-paint 逐格对比的测试。

## 3. 系统更新与 A/B 槽

- 6T 刷的一直是 userdata 布局：**没有 A/B 系统槽、dm-verity 和 OTA**（[39](39-oneplus-6t.md) §11），userdata 是唯一的根文件系统，`system.update` 说不支持。[25](25-system-update.md) 的整套（RAUC、增量包、断点续装、自动更新）只在 QEMU 的 A/B 磁盘上验证过。
- A/B 布局设计好、写好了（[43](43-phone-partitions.md)）：根文件系统在 `system_a`、`system_b`，经 dm-verity 只读，initramfs 按 ABL 选的槽挂根，RAUC 经 `rauc-abl` 更新根槽和它的启动槽，数据在 userdata；板子文件里有分区表，刷机脚本写之前量大小、绝不写 EFS 和固件的分区。脚本和逻辑单元测试 ✓，镜像 2026-11-24 构建过一次、没开过机，**未上真机**；上手机前要查的在 [39](39-oneplus-6t.md) §15（分区多大、回 OxygenOS 还行不行）。
- `phone-abslot` 只在磁盘镜像上测过（`yocto/abslot-test.py`，加上 `tools/slots_test.py` 里照源码读的 ABL 模型）；真机的 GPT 属性、bootloader 数完次数退回另一个槽的行为**未上真机**（[39](39-oneplus-6t.md) §14.13、§15）。
- 6T 的持久区（`/var/lib/slots`：清除的请求、防盗锁的记录）和数据在同一个 userdata 上，A/B 布局里也是（[43](43-phone-partitions.md) §3.4 的理由）：重刷 userdata 或 `fastboot -w`，两样一起没了（[31](31-lock-and-biometrics.md) §11.5）；启动不再依赖它，手机照样起来。

## 4. 通信：中国市场的硬缺口

- **VoLTE 写好了，没上过真机**（[39](39-oneplus-6t.md) §11.1）。中国的运营商大多要 VoLTE 才能打电话（联通、电信关了 2G、3G）。一加 6T 的镜像里有 81voltd（替基带开 IMS 承载）和按 SIM 卡激活基带里运营商配置的 `phone-qcom-pdc`，telephonyd 不把 IMS 承载当移动数据、不断开它；基带里没有这家的配置时要手工装（写基带的持久存储，有意不自动做），运营商兼容只能拿真卡一家一家试（[39](39-oneplus-6t.md) §14.6）；IMS 注册状态、VoLTE 开关、Wi-Fi 通话**未实现**。
- 通话的声音路由（听筒、免提、静音、UCM 的引导序列）写好了，**未上真机**（[20](20-audio.md) §10）。
- 蓝牙通话的音频、免提的回声消除没做（主线的 q6voice 没有 Android 的 ACDB 校准，[20](20-audio.md) §10）。
- **耳机**：6T 没有 3.5 mm 插孔，主线的 USB 只做设备，USB-C 耳机接不上；audiod 也不知道插孔的状态。
- 彩信是 **MVP**，只对着假的彩信中心跑过（[32](32-messaging.md) §11）。
- 传感器、相机、NFC、USB 主机、Dash 快充、马达没做（[39](39-oneplus-6t.md) §11）；提醒滑块写好了，**未上真机**（[39](39-oneplus-6t.md) §14.18）。

## 5. 排斥 GPLv3 的代价

镜像不收 GPLv3（kas 的 `INCOMPATIBLE_LICENSE`，[39](39-oneplus-6t.md) §10），这几样只能重写或者不要：

| GPLv3 的那一个 | 许可 | Kario 的做法 |
|------|------|------|
| qbootctl | GPL-3.0 | `phone-abslot`：在磁盘镜像上测过 |
| hexagonrpcd | GPL-3.0+ | 没有替代：没有传感器 |
| msm-modem 的 UIM 选择 | GPL-3.0+ | `uim-select`：没有自己的测试 |
| bootmac | GPL-3.0+ | `phone-btaddr`：对着扮内核的套接字测过（`yocto/btaddr-test.py`：等控制器设好再设地址，[39](39-oneplus-6t.md) §16.10），**未上真机** |
| callaudiod | GPL-3.0+ | phone-audiod 的 UCM 通话路由：单元测试 |
| eSpeak NG（屏幕阅读器的语音） | GPL-3.0+ | 没有替代：屏幕阅读器在手机上听不到声音（[20](20-audio.md) §11） |

这些替代品都是照要做的事重写的，没有用原来的代码，全部**未上真机**。

## 6. 测试与工程

- **`-race` 只有 Shell 包通过**：`go test -race ./shell` 通过（48 核上 418 秒，限 4 核 436 秒）：时区改成显式传递，不再改写进程全局的 `time.Local`；原来说的 Contacts「落盘约 600 毫秒」其实是测试里 Shell 在自己的循环上建输入法词库，加上无响应检测把 Shell 自己让应用等的时间算成应用没回答，两处都改了（[40](40-testing.md) §7）。CI 的 `race` 作业只跑 Shell 包，在 GitHub 上**未验证**。还没解决：`apps/settings` 和 `jsrt` 的测试里有数据竞争，opus-go 过不了 `-race` 打开的指针检查，SDK 的 `setZone` 在应用进程里仍改写 `time.Local`，持久化的写盘仍在 Shell 的循环上。
- **模拟的边界很宽**：基带、Wi-Fi、蓝牙、NFC、定位、相机、指纹、更新服务器都有模拟的一侧（[40](40-testing.md) §3），真机那部分都**未上真机**。
- **备份的前提在一台 6T 上不成立**：`fajita-flash.sh backup`、`restore-efs` 要一个能在 OxygenOS 11 上 `fastboot boot` 起来的 TWRP，那台手机上 TWRP 3.7.0_9 崩溃进了 EDL（[39](39-oneplus-6t.md) §9.1）；`test`、`flash`、`restore` 和 chroot 套件的 `push` 又只认脚本自己格式的备份（`fajita-flash.sh` 要 `phone.txt` 和 `SHA256SUMS`，`push` 要 `SHA256SUMS`）。经 Nura 调试 shell 的手工备份写在 [39](39-oneplus-6t.md) §9.1，脚本不会走这条路（**未实现**）。
- **整机上第一个蓝牙键盘没法从界面配对**：Kario 的设置要键盘才走得到；6T 自己只有电源键和音量键（音量键故意不兼方向键，[24](24-sku-framework.md) §7.4），USB 只做设备；开发者通道只认 Shell 自己信任过的数据线（[27](27-usb-typec.md) §4），而 USB 控制器这时归 `phone-bringup`。镜像里有没有 `bluetoothctl`**未核实**（[39](39-oneplus-6t.md) §14.7）。
- **构建流程补齐自己（未经真的镜像构建验证）**：`make yocto-src`（`yocto-build` 等都经过它）发现 `phone-fw-go-mods.inc` 过期时自己重新生成，生成不了（没网、Go 比 `go.mod` 要的旧）才停下并说明原因；`go mod tidy` 不再让它算过期。`make yocto-doctor` 把设备镜像离不了的东西（podman 或 docker、kas-container、无根 podman 的 subuid/subgid、Go、GCC 14、磁盘空间等）判成 MISS，`make doctor` 的退出码照旧只管宿主机的目标。bitbake 的线程数按机器算（处理器数和内存 ÷ 2 GiB 取小，`YOCTO_JOBS=` 可改，[13](13-prototype.md)）。这几样都还没在真的镜像构建里跑过；`yocto-bundle`、`yocto-ota-test` 不重新核对模块清单，构建前也不自动跑 `yocto-doctor`。
- **顶在内存上限上卡住的应用**：QEMU 上查明了，是组里的文件页（应用自己的程序）被回收又读回来，内核迟迟不判 OOM；现在 Shell 照安卓 lmkd 按内存压力结束它（[17](17-app-sandbox.md) §5.4）。还缺的：阈值（2 秒里一半的时间）是在 QEMU 上定的，6T 的内核开着 MGLRU、存储也快得多，那里的数**未上真机**（[39](39-oneplus-6t.md) §14.12）；写文件的应用在上限上没测（自测的数据目录在 tmpfs 上）；网页进程的组不看（**未实现**）。

## 7. 文档与流程

- [39](39-oneplus-6t.md) §3.5、§9 和 [24](24-sku-framework.md) §6.2 第 3 步原来都假定 TWRP 能只在内存里跑；现在都指向 [39](39-oneplus-6t.md) §9.1，脚本没有跟上（§6）。
- 构建要的东西现在由 `make yocto-doctor` 查（§6），它和真的镜像构建对不对得上**未验证**。

## 8. 性能与资源

- 第一次构建镜像要约 150 GB 磁盘和几个小时（`scripts/doctor.sh` 的提示；8 核、16 GB 内存以下的机器更久）。
- phone-paint 的软件渲染在真机上的 CPU 占用和待机功耗没有数据（[37](37-motion.md) §2 是估算；量法见 [39](39-oneplus-6t.md) §14.3、§14.10）。
- 1 GB 内存的压力测试、浸泡测试都只在 QEMU 和开发机上跑过（[40](40-testing.md) §1）。

## 9. 安全与生态

- 应用沙箱、能力代理、网页视图（WPE WebKit）、商店签名和第三方应用上架只在开发机和 QEMU 上验证过；6T 的两种内核上沙箱少了什么，要看兼容测试报告的 `sandbox` 字段（[39](39-oneplus-6t.md) §14.12，**未上真机**）。
- 可信支付确认、设备认证、指纹支付要 TEE，TEE **未实现**（[31](31-lock-and-biometrics.md) §4，R15）：现在由 phone-keystored 代替，设备密钥和数据盐在数据分区上（[31](31-lock-and-biometrics.md) §11.3）。
- 6T 的 bootloader 谁都能解锁，持久区又在 userdata 上：防盗锁挡不住重刷（[31](31-lock-and-biometrics.md) §11.5；锁住的 bootloader **依赖硬件**）。账号服务器**没有部署**，防盗锁和家长远程管理在手机上都还没对着真的服务器跑过。
- 能力版本和平台级别 2（`media.list@2`，[22](22-developer-api.md) §2.3）只在开发机上验证过：还没有发布过级别 2 的系统，也没有真的移除过一个版本（移除只在测试的表上走过）；商店目录不按清单的 `capabilities` 筛，要到手机上安装时才拒；系统自带的音乐、图片应用还用版本 1 的 `media.list`，没改成分页（库大到一个回答超过 4 MB 时会怎样**未验证**）；TypeScript 应用读不到平台级别（**未实现**）。渲染器协议换主版本时的协商只用测试里编出来的 `phone.frame/0` 走过（[23](23-renderer-protocol.md) §2）。

## 10. 这次真机过程中额外暴露的（那台电脑上的记录）

- Nura edge 的 usr-merge（`usr/sbin -> bin`）和 apk-tools 3.0.10 冲突，`systemd` 包解不开；切到 v26.06（Alpine 3.24、apk 3.0.9）才过。那时 Nura 的 edge 不能直接用 pmbootstrap 构建（只是那一次的记录，**未在这里核实**）。
- `fastboot fetch` 在 6T 的 bootloader 上不支持（`Unable to get max-fetch-size`）：没有「不写手机、只读分区」的捷径，要启动一个临时的内核（[39](39-oneplus-6t.md) §9.1）。
- dtbo 擦了以后，TWRP 3.6.2 和 LineageOS 22.2 的 `boot.img` 被 bootloader 拒绝（`Load Error`），是不是 dtbo 的缘故没有查清。`test`、`flash` 一上来就擦 dtbo（[39](39-oneplus-6t.md) §5），擦了以后要用 Android 内核的恢复镜像，可能得先把原厂的 dtbo 写回去：这也是备份要在第一次往手机写东西以前做的一个理由。

## 11. 第一次在真机上跑 chroot 套件（2026-11-21）

那台电脑上的 agent 在一台 6T 上跑了 `tools/phone-chroot/kario-phone.sh`：Nura v26.06 的 console 界面（systemd），内核 `7.1.0-rc1-sdm845`，SKU `oneplus-fajita-t9`。测的是哪个提交、哪个镜像那次没有记下；套件现在把它们写进每次结果的 `kit.txt`（[24](24-sku-framework.md) §6.2）。下面照那次的报告和结果目录写，按代码核对过；更正了的、改过的都写明。

- **自测 38/39 通过，停在第 39 个用例。** 自测遇到第一个失败就停：整份 227 个用例只跑了 39 个，停在「沙箱：已生效的限制层全部符合预期」，后面的一百八十多个没跑。兼容测试报告只调用到 33/134 个能力（101 个 `never called`），**主要是因为没跑完**；缺基带、Wi-Fi、蓝牙、NFC、相机、传感器只造成其中一部分跳过（`require-*`）。报告由此说「chroot 不适合当兼容测试的门禁」，这一点要等跑完整份再看。现在自测可以失败后接着跑（`--keep-going`，[40](40-testing.md) §2），套件的 `selftest`、`pixel` 用它；`make selftest`、`make cts` 和 QEMU 照旧第一个失败就停。2026-11-22 在手机上跑过，见本节最后。
- **「监听 TCP 端口」被允许**（沙箱自检 10/11，3 项跳过）。报告归到缺 Landlock；按代码这一项属于「无网络」那一层（`sandbox/selfcheck_linux.go`）：没有网络权限的应用跑在自己的网络命名空间里，新命名空间的 `lo` 默认关着，绑不上 127.0.0.1；在 Nura 的内核上绑上了，说明那个命名空间里 `lo` 是开着的。每个应用有自己的命名空间，所以不是跨应用的漏洞，但沙箱在不同的内核上表现不一样。原因由第三次运行查明（下面）：`lo` 是关着的，照样绑得上，是检查本身不对。Landlock 也拦 TCP 监听（ABI 4 起，`sandbox/landlock_linux.go`），而 Nura 的内核没有 Landlock（下面）。
- **没有 NFC 的板子上 Shell 起不来。** phone-nfcd 在没有 NFC 控制器的板子上正常退出（「this board has no NFC controller」），`--services spawn`（chroot 里和开发机上 Shell 自己起服务）却把它当成崩溃：每秒重启一次，又一直等它的套接字，最后 Shell 失败退出；那边在 chroot 的 `sku.json` 里加 `"nfc": "sim"` 绕过去。整机上 phone-nfcd 由 systemd 起（`Restart=on-failure`），没有这个问题。现在服务在套接字出现之前以 0 退出，算作这块板子上没有它要服务的东西：记一行日志，不重启，不等；非 0 退出照旧算崩溃、重启（`programs/shellcmd`）。没有 nfcd 时应用的 `nfc.*` 立刻答「this phone has no NFC」（原来等 2 秒、报套接字的错），设置和卡包照旧显示「这台手机没有 NFC」（[30](30-nfc.md) §8）。单元测试 ✓（拿掉这次的改动就失败），**未上真机**；chroot 的 `sku.json` 不用再改。
- **声音**：phone-audiod 一直报 `aplay: audio open error: Invalid argument`（`plughw:0,0`）。facts 里没有 UCM 的 verb：Nura 上没装 `alsaucm`，`setup` 就没设 `HiFi`，声卡的通路没接上，而 `setup` 原来不说。现在没有 `alsaucm` 时它明说 phone-audiod 放不出声音、怎么装（Nura、postmarketOS、Alpine 上 `apk add alsa-utils`），facts 里也写一行。装上以后能不能放音**未验证**。
- **相机**：phone-mediad 报 `/dev/video7: no streaming video capture`，chroot 里没有能用的相机（6T 的相机没做，[39](39-oneplus-6t.md) §11）。
- **ModemManager 没在跑**：`setup` 提示的是 OpenRC 的 `rc-service modemmanager start`，Nura v26.06 却是 systemd。现在按底座的 init 系统提示（`systemctl start ModemManager`）。那次 telephonyd 的 ModemManager 后端没有对端。

- **空闲挂起后按电源键「没反应」**（2026-11-23，同一台 6T）。`shell-start`（不加 `--stay-on`）后约 40 秒熄屏挂起，主机侧看到 USB gadget **整个消失**：`lsusb` 里连 `18d1:d001` 都不在，`enx…` 网卡消失，ping 不通；按电源键没有反应，只能长按重启。
  - **报告 003 之后由回复 004 更正了两点**：
    1. 这更像「按电源键把手机**关机**了」，不是「唤不醒」：Kario 的镜像里有 systemd-logind，却从没配置过它，默认 `HandlePowerKey=poweroff`——按电源键就关机，绕过了 Shell；霍尔传感器同理（盖上就挂起）。整机镜像现在带 `logind.conf.d/50-phone.conf`，电源键、挂起键、休眠键、重启键、盖子开关和 `IdleAction` 全是 `ignore`，交给 Shell（QEMU 上验证过：没有这个配置时按 virt 机器的电源按钮会当场关机）。
    2. `/sys/class/wakeup/wakeupN/event_count` 为 0 说明不了什么：pm8941-pwrkey 只把中断设成唤醒中断，从不向自己的唤醒源报事件；`/sys/class/wakeup/wakeupN/power/wakeup` 在这个驱动下本来就不存在，要看平台设备 `/sys/bus/platform/devices/c440000.spmi:pmic@0:pon@800:pwrkey/power/`。
  - 电源键和 RTC 闹钟走同一个 SPMI 仲裁器中断；同一天自测的「挂起」用例 PASS（RTC 相对闹钟唤醒），所以电源键原则上也该能。整机上按回复 004 §4 第 7 步抓：挂起前后的 `snap`、`pm_wakeup_irq`、`/proc/interrupts`、dmesg、`journalctl -u phone-display`。

**facts 里看出来的**（报告没有单独写）：

- Nura 内核的 LSM 只有 `capability,bpf`，没有 Landlock：沙箱的 Landlock 那一层在这个底座上不生效。Kario 自己的内核上要再看（[39](39-oneplus-6t.md) §14.12）。
- 开机时 `msm_dsi_phy … DSI PLL(0) lock failed`，带调用栈：面板能亮，但 DSI 的 PLL 重新上锁这一步不稳；和 §13 有没有关系**未查明**。
- `a630_sqe.fw` 加载失败，错误 -40（ELOOP，符号链接成环）：Nura 打包的 GPU 固件有问题，这个底座上 Adreno 630 起不来，chroot 里试不了 GPU 合成（§2）。
- 时钟是 1970：骁龙的 pm8xxx RTC 在主线内核里只能读，不联网手机就从 1970 开始，结果目录名也成了 `19700101-061440`。套件的 `setup` 现在在手机的时钟比电脑慢 10 分钟以上时照电脑的设，记进结果的 `clock.txt`（**未上真机**）。整机上原来没有解决：RTC 写不进去，联网（NTP）或者基带给出网络时间以前时钟是错的；Kario 还按绝对时间写 RTC 的唤醒闹钟，闹钟落到了 57 年以后（现在写 `+N`）。教训是 RTC 怎么走是主板的参数，代码要对整机和 chroot 都成立。现在（[28](28-battery.md) §6）：主板配置的 `[rtc]` 说 RTC 能不能设、系统时间是否存成相对它的偏移、唤醒闹钟怎么写；6T 是只读、存偏移：Shell 设了时间（手动、NITZ）以后、NTP 同步着时把偏移存进持久区（`/var/lib/slots/rtc-offset.json`），开机时 `phone-clock.service` 在 Shell 和 timesyncd 之前按 RTC 加偏移设时钟，关机时再存一次；能设的 RTC（QEMU 的 pl031）照 hwclock 的做法写进去。NITZ 原来经 timedated 设时钟，而 NITZ 只在自动时间开着时来，那时 NTP 开着，timedated 拒绝 SetTime，镜像上网络时间就设不上；现在直接设（单元测试，拿掉这次的改动就失败）。chroot 里时钟是 Nura 的：套件给每个命令 `PHONE_CLOCK=host`，Shell 在 chroot 里也认得出来，不设时间、不开关 NTP、不碰 RTC。单元测试 ✓，**未上真机**（要查的见 [39](39-oneplus-6t.md) §14.10）。还缺：没有偏移时（新手机、RTC 掉过电）照旧等网络；内核那边的办法（设备树的 `allow-set-time`、SDAM 里的偏移单元）不用，理由见 [39](39-oneplus-6t.md) §11.2；两块骁龙模块板（PM6125）的 RTC 多半也只读，没写 `[rtc]`，到了板子上再定；chroot 里设置的「自动设置时间」下面写的还是开发机的说法（「只有手机自己设时钟」）。
- 跑着 `81voltd.service`（QMI/QRTR 上的 IMS 数据服务）：postmarketOS 已经有一条 IMS 的路，VoLTE 的调研（§4、[39](39-oneplus-6t.md) §11.1）要看它。
- facts 收的内核命令行带着序列号（`androidboot.serialno`）：现在 facts 写盘前抹掉序列号（命令行、内核日志里的都算）和基带的 IMEI、IMSI、ICCID、号码，`kario-phone.sh` 自己的输出里序列号只留最后两位。

**第二次，带 `--keep-going` 跑完整份（2026-11-22，那边报告 002 §8.19–8.22）**：FAIL 104/207，几乎都是连带出来的。

- 最先的几个失败来自底座：Nura 的内核没有 Landlock（两个沙箱用例），当时没有按应用的 cgroup（没装 `setfattr`）。每个失败之后从头起都成功了，可下一个用例接着的是失败的用例留下的状态。「锁屏指纹：设置 › 安全 › 指纹…」失败，是因为这台手机没有系统能用的指纹传感器（板子没写，phone-keystored 就没有，自测的 `finger` 没东西可碰），而前面的「锁屏 PIN 码」已经设了锁屏 PIN 码；删除 PIN 码的用例再也没跑成，之后每次从头起都是锁着开机，后面九十来个用例卡在 PIN 码界面（`at …: the screen is system/sys.idle`、「输入 PIN 码」）。靠前一个用例留下的页面的用例每次从头起之后也失败（「鸿雁：发图走系统选择器」以为鸿雁还在会话里，「推送：应用进程不在时…」以为飞钱还在跑），移动定位接收机的用例报 `the receiver is "modemmanager": the script cannot move it`。不带 `--keep-going`、去掉沙箱用例时，同一个镜像 85/86 通过（第一个真失败是声音，镜像里没有 `alsaucm`，那边 §8.23）。
- 现在：从头起时从失败的用例开始时的状态起（每个用例开始时给状态目录做个副本，从头起之前放回，[40](40-testing.md) §2；开发机上每个用例 6.5 毫秒）；手机缺的模拟硬件，用到它的用例在手机上跳过（指纹 `require-fingerprint`，移动定位接收机的和要天气位置的 `require-gnss sim fifo`，自动亮度 `require-lux`；只是锁上手机的锁屏用例用 PIN 码解锁，照跑）；内核没有 Landlock 时「沙箱：独立 uid、命名空间、Landlock」跳过并写明原因（报告的 `sandbox.missing` 里有，「内核特性」照样失败、写明缺什么）；用例自己打开自己的应用和页面，接着前一个用例做的写 `require-previous`。单元测试 ✓、开发机上 ✓，**还没再上真机**。

**第三次，带 `--keep-going` 跑完整份（2026-11-23，那边报告 003）**：FAIL 183/194，像素那一轮的 `pixel-sku` 176/187，兼容测试 137 个能力调用了 132 个。原因（从日志和代码查出）和改了什么（单元测试 ✓，改之前失败；开发机上 ✓；**未上真机**）：

- 「沙箱：已生效的限制层…」和「兼容测试」（它唯一失败的一项就是这个）：「监听 TCP 端口」被允许。那边的实验定了案（`unshare -n`：`lo` 关着，绑定照样成功，连它却连不上）：绑定不需要网卡起来，这一项只绑定，在没有 Landlock 网络规则的内核上都说「允许」；QEMU 上通过是因为 Landlock（ABI 4）拒绝绑定。现在这一项绑定后再连自己：被 Landlock 拒绝，或在应用自己的命名空间里连不进来，并写明是哪一层；启动器施加了 Landlock ABI 4 时，绑得上照样算失败（[17](17-app-sandbox.md) §6）。
- 「内核特性」：内核是 Nura 的。Shell 分得出系统自带的内核（根目录里有它的 `/lib/modules/<版本>`，facts 的 `== chroot` 也是这样分的）和别的系统的；别的系统的内核上这个用例跳过（`require-own-kernel`），原因里列出那个内核缺什么，报告的 `sandbox.foreign` 也写明；镜像自己的内核上照旧失败。
- 「应用的流量上限」「流量节省」「按应用流量与只用 Wi-Fi」：探针的包从 Nura 的 Wi-Fi 出去（`wlan0`，NetworkManager 管的）：`PHONE-DATA-USAGE` 把商店的 384 字节记成 `WifiTx`，限制只在算作移动网络的网卡上生效（`netfw.WifiPrefix`），放过它们是对的。规则装在有网络的应用所在的命名空间里（Shell 的）；「mobile data: the firewall closes []」是移动数据开关的封锁，移动数据开着时就是空的，没错。用例里的「wait ↑384 B」碰上的是 Wi-Fi 那一行，前一半什么都没记成移动数据也过了。现在这几个用例写 `require-mobile-route`：有防火墙时，探针的包得从算作移动网络的网卡出去，否则跳过并写明从哪块网卡出去。
- 「后台任务」：`require-previous` 往回看越过了 `#part` 那一行：它前面的天气用例跳过了（没有脚本能移动的位置接收机），它接着上一份的最后一个用例做，天气的任务一次也没跑过。现在用例只接着自己这一份的用例做，接不上就和它要接着的用例一起跳过。
- 「VPN」：设置里写着「这台手机不能用 VPN：没有 systemd-networkd」（`vpn: none`；Nura 的总线上没有 systemd-networkd），用例的第一个等待找不到导入那一行；不是前面用例留下的状态，也不是「文件」里的文件。用例写 `require-vpn sim networkd`，日志写明为什么没有后端。
- 「充电」：手机插着 USB 线，本来就在充电（第一个用例起状态栏就是 ⚡97%），`power 40 charging fast` 什么都没改，也就没有插上充电器这回事：用例现在先拔掉。像素那一轮里，这个失败之后从头起（屏幕熄着）发现背光是关的，`bl_power` 还是前一个 Shell 留下的 4，新的 Shell 却以为屏幕亮着：背光一直黑着，之后屏幕熄灭时，以「请渲染器关面板」结束的那段渐暗没有跑，「系统更新」的 `screen off` 就失败了，`off true, panel on true`（`off` 是 Shell 的屏幕，`panel on` 是渲染器最后报的面板；渲染器管面板电源时这一步两个都等）。现在 Shell 发现背光关着就点亮，背光已经关了的渐暗照样收尾，这一步写明面板在等什么（渐暗、渲染器的回答，或渲染器做不到什么）。DSI PLL 锁不上是另一回事（§13）。
- 「恢复出厂设置（设备）」：在 systemd 不在其中运行的根目录里，`systemctl` 什么都不打印、退出码 0（「Running in chroot, ignoring command」），所以是 `"" <nil>`。这一步现在写明 systemctl 说了什么、或者这个单元哪里不对，用例写 `require-systemd`：镜像自己的 systemd 回答时（QEMU、整机）照跑。
- 「应用商店后台」：那里 Shell 不管 Wi-Fi（射频归 Nura 的 NetworkManager）。要架测试接入点的用例写 `require-wifi-ap`（模拟的 Wi-Fi，或 iwd 管的第二个设备）。
- 兼容测试「never called」的五个能力（location.stop、location.watch、settings.app.set、settings.apps、widget.set）只有要脚本能移动的位置接收机的用例调用；这些用例写明它们（`covers`），报告把它们算作跳过并写明原因（[40](40-testing.md) §6）。

## 12. 真机测试的使用问题与整机刷机（2026-11-21）

### 12.1 chroot 里的自测不画屏，是预期的

`selftest`（`phone-shell --cts`）和 `pixel`（`phone-shell --selftest --gui auto --pixel-dir …`，PNG 写进结果目录）都是无头的；`setup` 还 `chvt 8` 把控制台切走，所以手机屏幕（1 号控制台的 getty）只剩光标。要看 Kario 的界面用 `shell-start`（`phone-shell --gui auto`，chroot 里直接驱动面板）；那次的结果见 §13。

### 12.2 整机刷 Kario：步骤和风险

构建照 [39](39-oneplus-6t.md) §4（`make yocto-build … KASFILE=yocto/kas/phone-fajita.yml SKU=oneplus-fajita-t9`，第一次要几个小时），产物 `boot-oneplus-fajita.img` 和 `phone-image-oneplus-fajita.rootfs.ext4.sparse`。先 `yocto/fajita-flash.sh -d <目录> test`（`fastboot boot`，不写 `boot` 和 `userdata`），看过内核和屏幕再 `flash`（擦 `dtbo_a/b`，写 `boot_a/b` 和 `userdata`，[39](39-oneplus-6t.md) §5、§6）。风险都**未上真机**：

- A/B 槽（`phone-abslot`）、屏、触摸、按键、声音、基带、休眠在 6T 上都没跑过。
- `flash` 原来不核对 `-d` 里的镜像是不是这块板的，指错目录就刷错。现在 `test`、`flash`（演练也算）先核对：启动镜像的命令行里有 Kario 的 `phone.gui=`（Nura 的写着 `pmos_`），内核后面附的设备树是 6T 的（`oneplus,fajita`；一加 6 的是 `oneplus,enchilada`），ramdisk 是写明布局的 phone-initramfs（2026-11-24 起，§17）；`flash` 还要 `userdata` 是 Android 的稀疏镜像；不对就拒绝，什么也不写（`yocto/fajita-flash-test.py` ✓）。没有另做戳记文件：设备树就在启动镜像里，是内核真正用的那一份，戳记文件却可能跟着别的镜像一起被拷错。`userdata` 里是哪块板的根文件系统没有核对（要解开几百 MB 的镜像），靠同一个构建目录和固定的文件名。
- 回 OxygenOS 用 `fajita-flash.sh restore`；原厂的 dtbo 已经擦了，要从 OxygenOS 11.1.2.2 完整包的 `payload.bin` 里取 `dtbo.img`（`--stock`，[39](39-oneplus-6t.md) §9）。
- 刷整机后 `userdata` 是 Kario 的 ext4，chroot 测试用的 Nura 和它的状态都没了。

### 12.3 后台步骤误报 gone

`selftest`、`pixel` 在手机上分离运行，`kario-phone.sh` 每 20 秒问一次；有时它报 `ended without its exit code (gone)`，测试其实还在跑。这是脚本的问题，不是 Kario 运行时的。从报告看不出是哪一种；查代码找出、改了的：

- 分离的步骤中途 `die`（比如 chroot 没挂上）时不写退出码，状态只能说 gone。现在不管怎么退出都写。
- 状态检查在 `/run/kario-chroot/run` 不在时自己新开一次运行，再到新的结果目录里找退出码。现在状态和日志从不新开。
- 记下的 pid 是启动它的那一层的 `$!`；`setsid` 拿到进程组的组长时会再 fork 一次，`$!` 就成了马上退出的进程。现在步骤一开始自己记下自己的 pid（`/run` 和结果目录里各一份），状态也按命令行找它。
- systemd 上，SSH 登录结束时 logind（`KillUserProcesses=yes` 时）杀掉登录 scope 里剩下的进程，`setsid` 也逃不掉；phone-shell 已经搬进了我们自己的 cgroup，照样在跑：正好是「报 gone，测试还在跑」。现在有 systemd 时，分离的步骤和 `shell-start` 放进自己的 scope（`systemd-run --scope`）。是不是那次的原因**未查明**。
- 再报 gone 时，状态把手机上看到的都打出来：状态目录、`run` 文件、每个 pid 文件和它的进程在不在、结果目录、相关进程的父进程、进程组、会话和 cgroup、logind 的 `KillUserProcesses`；分离的步骤收到的挂断（SIGHUP）和终止（SIGTERM）也记下来（它不因此退出）。`kario-phone.sh` 把这些和步骤输出的最后几行一起打出来，贴进报告就行。

开发机上的测试 ✓（`tools/phone_chroot_test.py`），**未上真机**。

## 13. `shell-start` 之后黑屏、崩溃转储（2026-11-21，**未解决**）

那边的记录：`shell-start` 后出现过 Kario 的开机画面，随后屏幕全黑，按电源键也点不亮；之后手机在 USB 上是 `05c6:900e`（高通的崩溃转储模式，QUSB_BULK），内核卡死、看门狗复位后常见这样。这是一次内核级的卡死，不是脚本的问题，原因**未查明**。

猜测，最怀疑的在前：

1. **熄屏和亮屏。** 和自测比，`shell-start` 多的正是真屏和真硬件：phone-paint 直接驱动面板，读电源键和触摸屏，按超时熄屏、调背光，按键时可能振动。Shell 默认空闲 30 秒熄屏：先把背光渐暗到 0，再让 phone-paint 关掉显示控制器的输出（`set_crtc` 不带帧缓冲），面板随之断电；按电源键时重新设模式、给面板上电。这条路在 6T 上从没跑过；6T 是 DSI 命令模式的面板（`dsi_samsung_s6e3fc2x01_cmd_display`），开机时 DSI 的 PLL 就锁定失败过一次（§11）。过程对得上：画面出现后黑了，按电源键点不亮，之后进了崩溃转储。报告里「背光由 sde/dsi 单独控制、fbcon 和 DRM 抢显示控制器把背光关了」一条归在这里一起查。
2. **phone-paint 的设模式本身**（`drmModeSetCrtc`、dumb buffer）在 6T 的 msm DRM 上也没验证过。
3. **USB gadget**：不大像。镜像里的 Shell 有整机支持（facts 里写着），USB 控制器被 Nura 的 gadget 占着时不碰 USB（[24](24-sku-framework.md) §6.2）；`shell-start` 之后、卡死之前 SSH 若还通，就能排除。
4. **`chvt 8` 和 1 号控制台的 getty**：会让按键和显示的状态乱，但不大会让内核卡死。

隔离的步骤（它们有结果以前不再跑 `shell-start`）：

1. 每一步另开一个 SSH 会话，把 `dmesg -w` 实时存到电脑上：手机卡死时最后几行能留下来。
2. 收崩溃那次留下的：结果目录里的 `shell.log`、`/var/lib/kario-chroot/state/home/.local/state/phone-sim/run/` 下各服务的日志、`/sys/fs/pstore`、上一次开机的内核日志（`journalctl -b -1 -k`）。`shell.log` 最后几十行里熄屏（`screen off`、背光）前后 Shell 和 phone-paint 在做什么最要紧。
3. 不用 Kario，在 Nura 的控制台上单独试：背光调暗、调到 0、再调回来（`/sys/class/backlight/ae94000.dsi.0/brightness`），再经 DRM 的 fbdev 关掉、打开显示控制器的输出（`/sys/class/graphics/fb0/blank` 写 4，再写 0：和 phone-paint 熄屏走同一个驱动的同一段），能回来就连做 5 次。卡死或进 `900e`：问题在内核的面板断电和上电，Kario 要给这块板加「熄屏只关背光、不关显示控制器」的选项（**未实现**）。都正常：更可能在 Kario 里，用下面的开关一步步缩小。
4. `shell-start` 的调试开关（这次加的）：`--stay-on`，Shell 从不熄屏（超时和电源键都不关屏、不调暗，背光和显示控制器一直开着，手机也就不休眠）；`--no-haptics`，不振动。只在这一个 Shell 里有效，不存进设置（`kario-phone.sh --stay-on --no-haptics shell-start`，[39](39-oneplus-6t.md) §13.4）。单元测试 ✓，**未上真机**。

报告建议先从崩溃转储里读内存（`05c6:900e` 用 qdl 或 edl，要这台手机的 firehose 引导程序），放在上面几步之后。

## 14. 硬件测试计划（那边的建议，**未跑**）

| 阶段 | 测什么 | 怎么做 | 通过标准 |
|------|--------|--------|----------|
| H0 引导 | 内核、DRM、背光、按键 | `yocto/fajita-flash.sh test`（`fastboot boot`，不写手机） | 屏幕亮、有 `/dev/dri/card0`、内核日志里没有 panic |
| H1 显示 | 面板、背光、刷新 | phone-paint 画全屏色块、渐变；先过 §13 的隔离步骤 | 不黑屏、背光可调、不撕裂 |
| H2 输入 | 电源键、音量键、触摸 | evdev 读键（facts 的 `== input`） | 每个键、每个触点都有事件 |
| H3 声音 | 扬声器、听筒、麦克风 | `aplay`、`alsaucm`、通话 | 有声音、没有爆音、路由对 |
| H4 基带 | SIM、通话、短信、数据 | `mmcli`、拨号、发短信 | 注册上网络，能通话、收发短信、上网 |
| H5 休眠 | 息屏、唤醒、功耗 | `suspend_stats`、电流 | 能睡能醒，待机电流达标 |
| H6 传感器、相机、NFC、指纹 | | `iio`、`v4l2`、nfcd | 都没做：传感器在 SLPI 后面（hexagonrpcd 是 GPLv3，§5），相机、指纹没有驱动，NFC 控制器归内核的 NFC 子系统、不是 nfcd 能开的 NCI 设备（`sku/boards/oneplus-fajita.toml`） |
| H7 稳定性 | 浸泡、温度、充电 | 连续跑 24 小时 | 不 panic、温度可控、充电正常 |

顺序：H0 → H1 → H2 先确认能看能按，再做 H3、H4；H5–H7 等功能稳定以后。逐项的做法和记法在 [39](39-oneplus-6t.md) §14。

## 15. 分区：要不要像 Android 一样拆分

Android 在 6T 上把这台手机独有的数据放在单独的分区里：`persist`（指纹、传感器的校准）、`modemst1`、`modemst2`（IMEI 等基带的数据）、`fsg`、`fsc`（基带的配置），还有 `frp`、`apdp` 等。Kario 只写 `dtbo_a/b`、`boot_a/b` 和 `userdata`（整个根文件系统），**不碰这些分区**：刷 Kario 不会丢 IMEI 和校准；`modemst`、`fsg` 照旧由 rmtfs 交给基带，Kario 不写（[39](39-oneplus-6t.md) §3.5、§14.1）。报告的建议和当时的设计一致（下面四条）；之后定下的整套分区设计在 [43](43-phone-partitions.md)：前三条照旧，第 4 条改了，A/B 布局把根文件系统放进 `system_a`、`system_b`（`vendor` 照旧不碰），持久区为什么仍在 userdata 上见 [43](43-phone-partitions.md) §3.4。

1. Android 的 `persist`、`modemst1/2`、`fsg`、`fsc` 留着不动，Kario 不写。
2. Kario 自己的持久数据在 `userdata` 的 `/var/lib/phone`，不另拆一个 `persist` 去和 Android 的抢；持久区 `/var/lib/slots` 也在 `userdata` 上（§3）。
3. 指纹模板、传感器校准如果要接过来，从 Android 的 `persist` 拷一份到 `/var/lib/phone` 下、只读用，不改原分区（**未实现**；指纹和传感器在 6T 上都还没做）。
4. A/B 槽只用于 `boot`、`dtbo`；`userdata` 是唯一的根文件系统，暂不照 Android 加 `system_a/b`、`vendor_a/b`。要系统更新能回退时，再看 `userdata` 上的 dm-verity 和 A/B（[39](39-oneplus-6t.md) §11），不照搬 Android 的整张分区表。

## 16. 第二次真机测试：整机固件要带上的（2026-11-23，报告 003）

那次在 Nura 的 chroot 里跑，下面几条是整机固件（自己的内核和镜像）要先解决或先查清的。

- **logind 拿电源键关机、拿保护套挂起**（已解决，QEMU 上 ✓）：镜像里有 systemd-logind 却没配置它，按它的默认，按电源键关机，6T 的霍尔传感器（`Hall effect sensor`，磁吸保护套）一盖就挂起，都绕过了 Shell。镜像现在带一个 logind 的 drop-in，把各种键和盖子都设成 `ignore`；自测加了按真电源键的用例，QEMU 上去掉 drop-in 时按下就关机（[28](28-battery.md) §7）。
- **重启时的看门狗**（已改，**未上真机**）：systemd 默认在重启时把 `qcom_wdt` 设成 10 分钟，驱动最多收 32 秒，`EINVAL` 以后 systemd 解除看门狗。看门狗改成板子的 `[watchdog]`，6T 写 30 秒（[24](24-sku-framework.md) §2、[28](28-battery.md) §7）。那份报告把「重启后多出来的一次 2 秒的启动」归到它，但 systemd 259 和 261 在 `EINVAL` 以后都是解除看门狗，不是留着它，而且 `fastboot reboot` 以后也有这次启动：原因**未查明**，[39](39-oneplus-6t.md) §14.10 有对比的步骤。
- **关机**（查过代码，**未上真机**）：报告说 `pm_power_off` 是空的。6T 的内核有 PSCI（`psci` 节点、`CONFIG_ARM_PSCI_FW`），`pm_power_off` 就是 PSCI 的 `SYSTEM_OFF`，pm8941-pwrkey 事先把 PMIC 的 PS_HOLD 设成关机；真是空的，内核会停机，不会重启。那次插着 USB 线，更可能是 PMIC 见充电器在又开机。要在 6T 上拔线、插线各试一次（[39](39-oneplus-6t.md) §14.10）。
- **电源键叫不醒**（查过代码，**未上真机**）：电源键在内核里默认是唤醒源（驱动 `device_init_wakeup`，挂起时 `enable_irq_wake`），中断和叫得醒手机的 RTC 闹钟走同一个 SPMI 仲裁器的中断；Shell 收到醒后的那一下电源键会点亮屏幕（`TestPowerKeyAroundAWake`）。那次的 `event_count` 是 0 说明不了什么：这个驱动从不向它的唤醒源报事件。真正缺的是证据：`/sys/power/pm_wakeup_irq` 只在 `CONFIG_PM_SLEEP_DEBUG` 时有，Kario 的内核现在都开了 `CONFIG_PM_DEBUG`，Shell 的日志从此写出是哪个中断叫醒的。抓取的步骤在 [39](39-oneplus-6t.md) §14.10；屏幕不亮也可能是显示在恢复以后起不来（那份报告 §9.1 第 4 条），不是唤醒。
- **测试时扬声器太响**（已解决，套件里，**未上真机**）：套件跑 `selftest`、`pixel` 前把板子 `[audio]` 的 `quiet_controls` 调到 `quiet_level`，跑完调回；`--quiet` 换音量或不调（[24](24-sku-framework.md) §6.2）。6T 写的是那次手工调的 `RX0 Digital`–`RX4 Digital`，它们在不在扬声器的通路上**未核实**（[39](39-oneplus-6t.md) §14.5）。
- **facts 抹掉了电源键的键位图**（已解决）：`== input` 里 `pm8941_pwrkey` 的 `key` 是 `10000000000000`，抹除把 14 位以上的数字串当作 IMEI 一类的身份删掉了；键位图现在每个字写成 `0x…`。`facts` 另外记下按键的唤醒源（设备的 `power/wakeup*` 和中断数）、`pm_wakeup_irq`、上一次启动怎么结束的（看门狗、关机的日志和 pstore 的最后一行）、声卡的混音控件。

## 17. 第一次刷整机固件：开机回到 fastboot（2026-11-24，报告 004）

那位测试者在一台 6T 上刷了 `0befb13` 构建的整机固件（userdata 布局）：`dtbo_a/b` 擦了，`boot_a/b` 和 `userdata` 都写进去了，可每次开机都回到 fastboot，`fastboot boot` 这个启动镜像也一样。Kario 的内核换上 Nura 的 initramfs 就起来了，看得到 UFS 和完好的 userdata。当天又在手机上读了失败那次的 ramoops 和 ABL 给的命令行，原因查明（[39](39-oneplus-6t.md) §1、§8.2）。

- **启动镜像不带 initramfs，靠 ABL 不追加自己的 `root=`**（已改，QEMU ✓，**未上真机**）：一加的 ABL 总是追加 `root=PARTUUID=<Android 的 system 分区>`，命令行里已有 `root=` 也一样，内核以最后一个为准，挂上了 Android 的系统、跑了它的 init，init 出错后重启进 bootloader。现在 userdata 布局的启动镜像也带 phone-initramfs，按分区名挂 userdata，命令行的 `root=` 不管；`make yocto-userdata-test` 在 QEMU 上照那次的样子模拟 ABL 的参数：经 initramfs 起来，以前的路子去挂 Android 的分区。教训：一个设计靠的是不公开的固件怎么做（这里是一加的 ABL 怎么改命令行），要么先在手机上核实，要么做成不依赖它；QEMU 一直用 initramfs，「内核自己挂根」这条路在哪里都没跑过。
- **连续起不来，ABL 把槽标成不可启动，之后 `fastboot boot` 也是 `Load Error`**（已改，**未上真机**）：`fastboot set_active a` 才恢复。刷机脚本的 `test`、`flash`、`restore` 现在每次都先把当前槽设为活动（两种布局），模拟手机上测过（`yocto/fajita-flash-test.py`）；查法在 [39](39-oneplus-6t.md) §8.2 第 3 步。
- **命令行里有 `root=` 时 ABL 不给 `androidboot.slot_suffix`**：`phone-abslot` 就按 GPT 里标成活动的槽算这次启动的槽，`phone-qcom-abslot` 照样标对（`yocto/abslot-test.py` 用那次的命令行测过，**未上真机**）。为了不加 `skip_initramfs`、`ro init=/init`，启动镜像的命令行仍留着 `root=`。
- **fastboot 一次下载 512M 时 USB 断了**（已改）：userdata 传到 471 MB 时断开，`-S 64M` 就写完了。刷机脚本现在每个镜像都按 `-S 64M` 分块（`--chunk` 可改），[39](39-oneplus-6t.md) §6。
- **备份里缺 `boot_a`、`dtbo_a`、`dtbo_b`，脚本就拒绝刷机**（已改）：那台手机刷 Nura 时就丢了它们，测试者自己改了脚本。现在这台手机独有的五个分区必须在，`boot`、`dtbo` 缺了只提醒一句；`restore` 写回以前核对要写的每个文件，缺的、擦过的都要 `--stock`，不对就一个字节也不写（[39](39-oneplus-6t.md) §9、§9.1，`yocto/fajita-flash-test.py` ✓）。

## 18. 第一次整机开机以后：套件和自测的三处问题（2026-11-24，报告 005）

`d4856a3` 的整机固件在 6T 上开机成功，完整自测 193/194。下面三条是那次碰到、已在这棵树里改了的（内核拒绝加载模块和 VPN（设备）另说）。

- **`facts` 卡在 `/sys/power/wakeup_count`，`setup` 不结束**（已改，**未上真机**）：内核在有唤醒事件正在处理时读这个属性一直等，持着的唤醒锁就是一个，整机的 bring-up 镜像里 `phone-bringup-awake` 一直持着；`setup` 停在唤醒锁那句话之后，`facts.txt` 是空的（测试者在手机上去掉了这一项才往下跑）。Nura 上没有持着的唤醒锁，所以 chroot 那几轮没碰到。套件现在读每个属性最多等 2 秒，写 `<no answer in 2 s>` 并杀掉读的进程（镜像的 busybox 没有 `timeout`），另列出正在起作用的唤醒源；`facts` 读的别的属性、命令看过，不会这样一直等（`tools/phone_chroot_test.py` 拿一个 FIFO 代替它，旧脚本在这里卡住）。Shell 的挂起读同一个属性，是在事件循环之外的 goroutine 里读的，卡住的只是那次挂起（[39](39-oneplus-6t.md) §13.5）。
- **自测以后 `phone-display` 起不来，黑屏**（已改，QEMU 上复现了旧的，新的**未验证**、**未上真机**）：套件为自测停掉 `phone-display`，`KillMode=process` 留下了渲染器分出的输入助手；它降权时父进程退出信号被内核清掉，又只在有新设备时才发现渲染器不在了，fork 时继承的两块扫描缓冲区映射让显示的 DRM master 一直在它手里，再起来的渲染器打不开屏幕（`Permission denied`），服务重启 5 次以后放弃。报告里的 `Unit process 1004 (renderer)` 就是它（渲染器和助手都叫 `renderer`），不是自测的渲染器：自测在套件自己的作用域里跑，不画屏。现在助手在打开显卡之前分出，渲染器和助手降权以后重设父进程退出信号，助手在渲染器那端的套接字关了也结束（`paint/` 的测试，要 root 的两个用 `podman unshare` 跑过，旧代码上都失败）；两个跑 phone-shell 的单元改成 `KillMode=mixed`，`scripts/yocto-check.py` 查这一条；套件停掉单元以后等它留下的进程退出，10 秒后还在就让 systemd 结束它们并写明（[11](11-platform-architecture.md) §14、[23](23-renderer-protocol.md) §5、[39](39-oneplus-6t.md) §13.5）。
- **「沙箱里的 DNS」整机上失败**（已改）：只跑「内核特性 + 沙箱」那一轮里鸿雁从没启动过，`sandbox-read` 要把鸿雁还不存在的数据目录交给它的 uid，chown 失败；完整的那轮鸿雁先启动过，就通过了。这一步现在像应用第一次启动那样先建目录（`TestSandboxReadBeforeTheAppStarted`，旧代码上失败；[40](40-testing.md) §6）。

## 19. 第一次整机开机：lockdown 拒绝了每一个内核模块（2026-11-24，报告 005 §6.1）

同一台 6T 刷了 `d4856a3` 的整机固件，第一次开起来了（initramfs 绕开了 ABL 的 `root=`，§17），自测 193/194。最严重的问题：内核一个模块也没加载（`Lockdown: modprobe: unsigned module loading is restricted`），连带没有 RTC（`phone-clock` 等不到 `rtc0`，跨启动时间倒退）、电池和充电器、声卡、调制解调器（`tqftpserv`、`rmtfs`、`q6voiced`、SIM 会话都失败，Shell 晚了一分钟上屏），「VPN（设备）」也因为 `nft_ct` 加载不了而失败。

- **原因**（构建机上查明，**实测**）：模块都签了名，签名的却是另一个构建目录那次构建时内核自己生成的密钥：共用的 sstate 把那边打包好的模块给了这边，启动镜像的内核却是这边编的，只认这边生成的那把。打包没有去掉签名，lockdown 也该开着（[39](39-oneplus-6t.md) §8.3）。
- **改法**（已改，构建过、QEMU 上验过，**未上真机**）：模块用构建目录固定的密钥签名（`make ota-keys` 生成，不进 git），它的校验和进了任务签名；镜像构建时核对每个模块和启动镜像里的内核对得上（`d4856a3` 的镜像：1094 个全部对不上）；6T 一直要、不带固件的驱动编进内核（qrtr、PD mapper、RTC、电量计、充电器）；QEMU 的内核也开了模块签名和 lockdown，开机自测先跑 `module-check.sh`（[39](39-oneplus-6t.md) §8.3）。
- **另外两个开机错误**：`regulatory.db` 缺失（已改：它进了 initramfs，cfg80211 开机时就是从那里读的，QEMU 上也一样缺过）；`a630_sqe.fw` 开机时的 `-2` 是预期的，msm 在渲染器打开 card 时又加载上了，不改（[39](39-oneplus-6t.md) §8.1）。
- 教训：内核和模块是一对，可构建系统把它们当成两个产物分别缓存；一对之间的约束（同一把密钥）要么进缓存的键，要么在出镜像前核对。这次两样都补上了。
