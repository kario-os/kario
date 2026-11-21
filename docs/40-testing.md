# 测试方法与策略

> Kario 怎么测：分哪几层、各用什么命令、哪些硬件是模拟的、一份场景脚本怎么在开发机和模拟器上都跑、改了什么跑什么、发布前跑什么，以及写测试的规则。提交和 CI 的流程见 [38](38-contributing.md)，每轮的结果记在 [13](13-prototype.md) §5 和内部的验证基线（57（内部文档））。
> 状态：本文写的命令和流程在开发机（RHEL 8，x86-64，48 核）和 QEMU（qemuarm64，TCG）上用过（2026-11-19）；**未上真机**：真机上的测试（§7）还没有开始。

## 0. 结论

- **一份场景脚本跑在各处**：`shell/selftest.script`（208 个用例）在开发机上无头跑（文本界面）、经像素渲染器跑三种形态、在 QEMU 的两个镜像里开机跑；兼容测试（CTS）是它加上能力覆盖和签名报告。真机上跑的也会是它（§7）。
- **硬件用模拟的替代，模拟的边界写清楚**：基带、ModemManager、Wi-Fi、蓝牙、NFC、卫星定位、传感器、指纹、相机、更新服务器都有模拟的一侧（§3），各自的真机部分标 **未上真机**。
- **每个 bug 修复带一个在旧代码上失败的测试**，先确认它失败再修；**每个能力至少被调用一次**，没被调用 CTS 就不过；**每个解析不可信输入的地方有模糊测试**（67 个目标）。
- **改了什么跑什么**：CI 按改动挑（[38](38-contributing.md) §3）；发布前在开发机和 QEMU 上跑全套（§5）。
- **结果如实写进文档**：✓ 只写跑过的环境和日期，模拟的写「模拟」，没跑的写 **未验证**。

## 1. 分层

| 层 | 测什么 | 命令 | 时间（开发机） |
|----|--------|------|----------------|
| 静态检查 | 格式、`go vet`、clippy、代码里没有中文、翻译齐全、共享常量、中英文档结构、文档链接、许可证、Yocto 配方、工作流（actionlint）、DCO | `make test` 的前半段；CI 的 `checks` | 1 分钟 |
| 单元测试 | Go 的 126 个包（976 个测试函数）、Rust（`paint/`、`gui/`）、C++（telephonyd）、C SDK 的示例应用、TypeScript（`tsc`、Kario Studio 的 `npm test`）、Python 脚本 | `make test`、`cargo test --release`、`make telephonyd`、`make capi` | 5 分钟（含下一行） |
| 端到端（开发机） | 真的进程：Shell、各系统服务、应用，场景脚本分六份并行 | `go test ./shell`（`TestSelftestScenario` 等，`make test` 里最后单独跑） | 3 分钟 |
| 自测（开发机） | 同一份脚本串行跑一遍，存快照和 JUnit 报告 | `make selftest`，`ONLY='正则'` 只跑一部分 | 约 10 分钟 |
| 兼容测试 | 自测 + 探测应用（`cts/probe`）+ 能力覆盖（每个能力至少调用一次）+ 带设备签名的报告 | `make cts` | 约 12 分钟 |
| 像素自测 | 同一份脚本由 phone-paint 画，九宫格、全键盘、整机三种形态，每个 `snap` 存 PNG，最后报告实时显示每步的耗时（不判定，[37](37-motion.md) §5） | `make pixel-selftest`（对照：`make gui-selftest`） | 约 30 分钟 |
| 基带后端 | telephonyd 的 ModemManager 后端对着模拟的 ModemManager 1.24（D-Bus，在 Podman 容器里） | `make mm-selftest` | 1 分钟 |
| 模糊测试 | 67 个目标（Go 原生模糊测试；C / C++ 的带 ASan、UBSan），每个默认 30 秒 | `make fuzz`、`make fuzz-libfuzzer`；CI 手动：`fuzz` 工作流 | 约 5 分钟（并行 8 个） |
| QEMU 开机自测 | 设备镜像和产品镜像开机跑同一份脚本，再查声音、CTS 和 fd store | `make yocto-selftest`（产品镜像在 kario-internal 里加 `KARIO=`） | 构建 4 分钟 + 20 分钟 |
| QEMU 专项 | 系统更新（A/B、回滚、分批、自动更新）、跨版本增量和断点续装、发布版之间升级、云端服务、1 GB 内存压力、浸泡、语义和网页的对比、像素界面上屏 | `make yocto-ota-test`、`yocto-ota-base` + `yocto-ota-delta`、`yocto-ota-release`、`yocto-cloud-test`、`yocto-stress`、`yocto-soak`、`yocto-bench`、`yocto-gui` | 每项 30–75 分钟 |
| 浸泡、兼容旧应用 | 开发机上连续跑 30 分钟看泄漏；旧版本编出的应用在新 Shell 上照常用 | `make soak`、`make compat-old OLD_REV=<提交>` | 30 分钟、10 分钟 |
| 真机 | 一加 6T：chroot 跑应用、整机刷机（[39](39-oneplus-6t.md)） | **未上真机** | — |

## 2. 场景脚本

`shell/selftest.script` 是按键脚本：`== 标题` 开始一个用例，下面一行一条命令，由 `shell/headless.go` 的 `Runner` 执行。命令有三类：

- **按键和等待**：`key`、`type`、`long`、`tap`（只在像素渲染器下），`wait 文字 [毫秒]`、`wait-re 正则`、`gone 文字`、`at 页面`、`key-if 键 文字 毫秒`。
- **模拟外界**：来电和短信（`tel`、`sms`、`mms`）、Wi-Fi 接入点（`wifi-ap`、`wifi-join`）、蓝牙对端（`bt-peer`、`bt-push`、`bt-oldphone`）、读卡器（`nfc`）、位置（`location`）、光线（`lux`）、指纹（`finger`）、时间（`timeshift`）、服务器（`fetch-server`、`carddav-server`、`parent-server`、`store-publish`）等，每条在 `headless.go` 文件头有说明。
- **检查**：`saved`、`sealed`、`perm-state`、`vibrated`、`appstate` 这类直接查系统状态的命令，和 `snap 名字`（存一张快照）。

`require-*` 行让用例在缺东西的环境里跳过（158 行：没有基带 `require-tel`、没有短信服务 `require-sms`、只在 BlueZ 上 `require-bt bluez`、只在像素渲染器下 `require-gui`……）；跳过的在报告里写 SKIP，不算失败。

**同一份脚本的四种跑法**：

| 跑法 | 怎么跑 | 等待放慢 |
|------|--------|----------|
| `go test ./shell` | `#part 名字` 把脚本分成六份（chat、apps、keys、phone、location、power），每份在自己的一套服务上开机后只跑自己的用例，六份同时跑 | 1 倍 |
| `make selftest`、`make cts` | 整份串行，前面用例留下的状态后面能看到 | 1 倍 |
| `make pixel-selftest` | 整份串行，每种形态一遍，像素渲染器画 | 1 倍 |
| `make yocto-selftest` | 镜像开机时 `phone.selftest=1`，整份串行 | `--timeout-scale` 放大 |

**失败时打出来的**：失败的行和原因、当时的整屏文字、各应用进程的状态（冻结没有、在等什么）、Shell 日志的最后几十行；QEMU 上另有蓝牙和 iwd 的日志、快照的 PNG（base64 打在串口上）。系统更新的测试（`ota-test`）失败时也打出当时的屏幕和 Shell 关于系统更新的最后几行日志。

**写用例的规则**（都是踩过的坑）：

- 用例结束时把手机还原：删掉加的图片、联系人、设置、配对；后面的用例会数东西。
- 新用例放在它所属的 `#part` 下面；不要依赖别的部分留下的状态（并行时看不到）。
- 等要操作的那一样东西，不等随便一段字：`wait X` 只要 X 出现在屏幕任何地方就过（提示条、状态栏也算）。要移到列表里某一行时，等那一行自己的字（它的副标题），或用 `wait-re`。
- 换页的键之后先 `at 页面` 或 `wait`，再按下一个键；不要隔着换页连按。
- 列表可能循环：到第一项、最后一项用 `key first`、`key last`，不用 `key up 5`。
- 不靠加长等待掩盖竞态：等不到时先查是不是有两个请求的回答顺序不定、旧状态盖了新状态（§6 的例子）。

## 3. 模拟了什么

| 部分 | 开发机 | QEMU | 真的那一侧 |
|------|--------|------|------------|
| 基带、通话、短信、小区广播 | telephonyd 的模拟基带 | 同左 | ModemManager 后端对着模拟的 ModemManager（`make mm-selftest`）；真基带 **未上真机** |
| 移动数据 | 模拟的承载 | veth 顶替基带的网卡，systemd-networkd 配地址 | **未上真机** |
| Wi-Fi、热点 | 模拟的射频 | mac80211_hwsim 上真的 iwd，测试接入点 192.168.80.1/24 | **未上真机** |
| 蓝牙 | 模拟的射频 | btvirt 的两个虚拟控制器上真的 BlueZ（耳机、车机、安卓旧手机由第二个扮） | **未上真机** |
| NFC | 模拟的控制器和读卡器 | 同左 | NCI 后端 **未上真机** |
| 卫星定位 | 模拟接收机 | QEMU 管道上的 NMEA，走真的解析器 | **未上真机** |
| 相机、扫码 | 合成的画面 | vivid（虚拟 V4L2 设备），真的解码 | **未上真机** |
| 指纹、光线 | 模拟的传感器 | 同左 | **未上真机** |
| 系统更新 | 模拟的 A/B 手机 | 真的 RAUC、dm-verity、A/B 磁盘，`ota-www.py`（能模拟断网）或正式的更新服务器 | 真机引导 **未上真机** |
| 云端（商店、推送、账号、CardDAV） | 进程内的模拟服务器 | 构建容器里的服务（`yocto-cloud-test`） | 正式服务另测（kario-internal） |
| 电脑（USB） | 模拟的 MTP 和网络共享对端 | dummy_hcd 上的 FunctionFS | 真电脑 **未上真机** |

各项模拟到什么程度、哪些没做，见 [13](13-prototype.md) §6。

## 4. 改了什么跑什么

CI 的规则在 `scripts/ci-plan.py` 的 `RULES` 表里（[38](38-contributing.md) §3）：只改文档只跑检查；改了 Go 包跑这个包和所有依赖它的包；进 `make build` 的改动加跑自测、CTS、像素自测；telephonyd 的 ModemManager 后端加跑 `mm-selftest`；`yocto/` 跑配方检查和两个不开机的脚本测试；不认识的文件跑全部。唯一的门禁是 `pr-ok`。往 main 发布后手动跑一次全部（`gh workflow run ci.yml --ref main`）；每个工作流只留最近 3 次的记录（`scripts/ci-prune.sh`）。

CI 不跑的（太慢或要特权）：QEMU 的所有项、`make fuzz` 的长时间运行、浸泡；这些在开发机上按 §5 跑。

## 5. 发布前跑什么

按顺序，开发机上：

1. 检查脚本和 `go vet`（各几秒）。
2. `make test`（约 6 分钟）。
3. `make cts`（约 12 分钟）。
4. `make pixel-selftest`（约 30 分钟）。
5. QEMU：设备镜像和产品镜像的 `make yocto-selftest`，两条链错开一分钟同时跑（各约 25 分钟）。
6. 改了 telephonyd：`make mm-selftest`。改了系统更新：`make yocto-ota-test`（产品镜像，约 45 分钟）和 `make yocto-ota-delta`（约 75 分钟，基础由 `make yocto-ota-base` 在上一个发布版上存）。
7. 全部通过才发布（`make publish`），结果写进 [13](13-prototype.md) §5 和内部的验证基线。

**共用的一把锁**：开发机上的自测、CTS、`go test ./shell`、像素自测都要拿 `/tmp/kario-selftest.lock`（`flock /tmp/kario-selftest.lock make cts`）：它们用同一个固定端口（换机 47800）和同一套状态目录，同时跑会互相干扰。`go test ./shell` 要给 `-timeout 30m`：默认的 10 分钟不够时测试被杀，它起的服务进程会留下来、还拿着锁。

## 6. 规则

- **回归测试先失败**：修 bug 时先写出能复现的测试，在旧代码上跑一次看它失败（失败的样子和现场一样），再修，再看它通过。
- **能力全覆盖**：新能力要有用例调用它；`make cts` 报「never called」就不过（例：账号的「使用移动数据」开关，补了用例才过）。
- **不可信输入有模糊测试**：网络、文件、蓝牙、NFC、短信、更新包、应用发来的消息。模糊测试找出的问题留下最小输入做回归用例（`testdata/fuzz/`）。
- **偶发的失败当真的 bug 查**：先单独重跑一次分清是偶发还是必现，再找根因。例子：
  - 设置里蓝牙刚打开就按搜索，Shell 把两个改动并发交给 BlueZ，晚回来的旧状态又把找到的设备冲掉。修法是改动按先后执行、设置丢掉过期的刷新，而不是加长等待。
  - 命令面板的「转到」只列打开时页面上已有的行，真的 iwd 上网络那一行晚到。用例改成等那一行出现再按 `/`。
- **机器忙时的偶发**：QEMU 用软件模拟（TCG），三个模拟器同时跑时比单独跑慢得多；判断偶发要在机器不忙时重跑。CI 的机器核少，Shell 包的六份并行会挤掉别的包里对时间敏感的测试，所以 `make test` 和 CI 都让 Shell 包最后单独跑。
- **结果写进文档**：状态行写环境和日期（开发机、QEMU、真机），没跑的写 **未验证**，模拟的写「模拟」；评估类的数字写「估算」。

## 7. 还没有的

- **真机**：一加 6T 的 chroot 应用测试和整机刷机测试的步骤已写好（[39](39-oneplus-6t.md) §13），手机还没到，**未上真机**。真机上要跑同一份场景脚本，加上模拟器测不了的：通话音质和路由、射频、功耗和待机电流、温度、真屏的刷新。
- **快照比对**：自测和像素自测存下的 PNG 只做存档，不和基准图逐像素比对（**未实现**）；渲染器自己的单元测试比对增量画法和整屏重画逐字节相同。
- **覆盖率**：没有统计代码覆盖率（**未实现**）。
- **`-race`**：改动的包在 `-race` 下跑；场景测试在 `-race` 下过不了：时区切换时改写进程全局的 `time.Local` 是一处数据竞争，`-race` 的运行时报告它时自己崩溃；Contacts 落盘约 600 毫秒占着 Shell 给它的循环预算，被误报「没有响应」。两处都还没改。
- **产线测试**：工站领设备证书的流程有了（[22](22-developer-api.md) §11），产线的硬件测试（按键、屏、声音、射频）随整机做，**未实现**。
- **入网和认证测试**（入网许可、CCC、SRRC）由有资质的整机厂做，不在本仓库。
