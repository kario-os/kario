# 测试方法与策略

> Kario 怎么测：分哪几层、各用什么命令、哪些硬件是模拟的、一份场景脚本怎么在开发机和模拟器上都跑、改了什么跑什么、发布前跑什么，以及写测试的规则。提交和 CI 的流程见 [38](38-contributing.md)，每轮的结果记在 [13](13-prototype.md) §5 和内部的验证基线（57（内部文档））。
> 状态：本文写的命令和流程在开发机（RHEL 8，x86-64，48 核）和 QEMU（qemuarm64，TCG）上用过（2026-11-19；QEMU 和开发机上的分段跑法、各项时间：2026-11-25）；**未上真机**：真机上的测试（§7）还没有开始。

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
| 单元测试 | Go 的 126 个包（976 个测试函数）、Rust（`paint/`、`gui/`）、C++（telephonyd）、C SDK 的示例应用、TypeScript（`tsc`、Kario Studio 的 `npm test`）、Python 脚本 | `make test`、`cargo test --profile checked`（`paint/`）、`make telephonyd`、`make capi` | 5 分钟（含下一行） |
| 端到端（开发机） | 真的进程：Shell、各系统服务、应用，场景脚本分二十五份并行，起 Shell 的测试大多各在一个子进程里并行 | `go test ./shell`（`TestSelftestScenario` 等，`make test` 里最后单独跑） | 约 2 分钟 |
| 自测（开发机） | 同一份脚本跑一遍，各段同时跑（16 个处理器以下的机器上整份串行），快照和基线比对（§2），JUnit 报告 | `make selftest`（`SELFTEST_PARTS=0`：整份串行），`ONLY='正则'` 只跑一部分 | 约 3 分钟（整份串行约 18 分钟） |
| 兼容测试 | 自测 + 探测应用（`cts/probe`）+ 能力覆盖（每个能力至少调用一次）+ 带设备签名的报告；各段同时跑，每段一份报告（§2） | `make cts`（`CTS_PARTS=0`：整份串行、一份报告） | 约 3 分钟（整份串行约 19 分钟） |
| 像素自测 | 同一份脚本由 phone-paint 画，九宫格、全键盘、整机三种形态同时跑，每种的各段也同时跑（32 个处理器以下的机器上每种整份串行），每个 `snap` 存 PNG、和像素基线比对（§2），最后报告实时显示每步的耗时（不判定，[37](37-motion.md) §5） | `make pixel-selftest`（`PIXEL_PARTS=0`：每种整份串行；对照：`make gui-selftest`） | 约 3 分钟（每种整份串行约 18 分钟，三种形态再先后跑约 58 分钟） |
| 基带后端 | telephonyd 的 ModemManager 后端对着模拟的 ModemManager 1.24（D-Bus，在 Podman 容器里） | `make mm-selftest` | 1 分钟 |
| 模糊测试 | 67 个目标（Go 原生模糊测试；C / C++ 的带 ASan、UBSan），每个默认 30 秒 | `make fuzz`、`make fuzz-libfuzzer`；CI 手动：`fuzz` 工作流 | 约 5 分钟（并行 8 个） |
| Sanitizer 下的 kario-jsrt | `jsrt` 包的全部测试，对着带 ASan + UBSan、带 TSan 编的 kario-jsrt 和 QuickJS-ng，有报告就失败，泄漏也算（§8） | `make jsrt-asan`、`make jsrt-tsan`；CI：`jsrt-sanitize` 作业 | 冷构建时约 4 分钟（ASan）、3 分钟（TSan），构建过时各约 1.9 分钟 |
| 覆盖率 | Go 的语句覆盖率：单元测试、整份文本自测（系统带 `-cover` 编译，每个进程边跑边写计数）和两者合并，按包；phone-paint、telephonyd、C SDK 的测试和 Node.js 下的 JavaScript 按行（§7） | `flock /tmp/kario-selftest.lock make coverage`；CI 手动：`coverage` 工作流 | 约 30 分钟 |
| QEMU 开机自测 | 设备镜像和产品镜像开机跑同一份脚本，脚本分段在几个模拟器里同时跑（至多按处理器和内存定的个数，用其中能同样早跑完的最少几个：48 核的构建机上 9 个），再一起查声音、各段的 CTS 报告和 fd store | `make yocto-selftest`（`SELFTEST_VMS=` 定最多几个，1 是整份脚本一个模拟器；产品镜像在 kario-internal 里加 `KARIO=`） | 构建 4–18 分钟 + 约 6.5 分钟（一个模拟器：约 35 分钟） |
| QEMU 专项 | 系统更新（A/B、回滚、分批、自动更新）、一加 6T 的 userdata 布局（initramfs 按分区名挂根，命令行末尾带着 bootloader 追加的 `root=`）、跨版本增量和断点续装、发布版之间升级、云端服务、1 GB 内存压力、浸泡、语义和网页的对比、像素界面上屏、GPU 路径上屏（软件 GPU） | `make yocto-ota-test`、`yocto-userdata-test`、`yocto-ota-base` + `yocto-ota-delta`、`yocto-ota-release`、`yocto-cloud-test`、`yocto-stress`、`yocto-soak`、`yocto-bench`、`yocto-gui`、`yocto-gui-gpu` | `yocto-ota-test` 约 22 分钟，`yocto-userdata-test` 约 3.5 分钟；其余每项 30–75 分钟（`yocto-gui-gpu` 约三小时） |
| 浸泡、兼容旧应用 | 开发机上连续跑 30 分钟看泄漏；旧版本编出的应用在新 Shell 上照常用 | `make soak`、`make compat-old OLD_REV=<提交>` | 30 分钟、10 分钟 |
| 真机 | 一加 6T 上同一份场景脚本和兼容测试，两条路用同一个套件、同样的步骤和结果：手机自己的镜像在 Nura 上的 chroot 里跑，同一个镜像刷进去整机跑（[24](24-sku-framework.md) §6.2，[39](39-oneplus-6t.md) §13） | `tools/phone-chroot/kario-phone.sh --sku oneplus-fajita-t9 [--whole] push setup selftest pixel collect`；**未上真机** | 几个小时 |

**QEMU 的时间花在哪**（2026-11-25 在构建机上量的，同时还有别人的模拟器和构建在跑，负载 6–28）：x86 主机上的 qemuarm64 没有 KVM，全靠 TCG 模拟。

- 开机自测：开机到第一个用例单独跑约 35 秒，八个模拟器一起开机约 50 秒；剩下的是用例本身，整份串行 1913 秒（各部分：`chat` 435、`apps` 428、`power` 325、`keys` 276、`location` 195、`phone` 149、`cts` 97、`reset` 7）。所以各部分同时跑：`yocto/selftest-plan.py` 定几个模拟器（每 3 个处理器、每 3 GiB 内存至多一个，由 Makefile 在容器外数）、每个跑哪几部分（重的先分，权重是上一次各用例的秒数；fd store 放在最轻的那个里），`yocto/qemu-selftest.sh` 给每个模拟器各自的串口日志、声卡、监视器和用户网络，都用解压一次的同一个镜像（`snapshot`）。`make yocto-selftest`：八个模拟器 559 和 565 秒，一个模拟器 2081 秒（`SELFTEST_VMS=1`，同一个镜像、同一天下午），最长的部分决定总时间：`chat` 和 `apps`，用例 446 到 459 秒，比串行时至多长 7%（之前机器更忙的一次 `chat` 用了 519 秒），再加上模拟器到第一个用例的 48 秒和关机的 20 秒。于是按用例之间的依赖把这两段和 `power` 拆开（§2 的规则）：`chat` 拆出 `radio`，`apps` 拆出 `carddav`，`power` 拆出 `calendar`，一共十一段；计划只用能同样早跑完的最少几个模拟器，几段合用一个时给最长的留两成余量（权重只是上一次的：第一次合到和上一回最长的一样长，上一回慢了的那段这回快了，合用的那个反倒最长，398 秒）。现在九个模拟器，`make yocto-selftest` 393 秒（同一个镜像，傍晚，负载 7–14）：各段的用例 `apps` 302、`keys` 300、`radio` 267、`location` 217、`power` 215、`chat` 207、`carddav` 166、`phone` 166、`calendar` 143、`cts` 97、`reset` 8 秒（合计 2089 秒，比串行多 9%），最长的模拟器是 `keys`，375 秒；要再快就该拆 `keys` 和 `apps`。于是再拆成二十二段（`switch`、`btsearch`、`media`、`display`、`data`、`store`、`net`、`accounts`、`dev`、`nfc`、`update`、`family` 从原来的段里分出来；`nfc` 开头自己把电量设成 40%，它的快照是在充电用例之后记的）。计划把各用例上次的秒数留在运行目录旁边（`selftest.seconds.json`），只跑几个用例（`ONLY=`）的一回不会把别的用例的秒数冲掉；分不开时用上全部模拟器（48 个处理器至多 16 个）。同一个镜像（2026-11-26 清晨，负载 4–12）：拆之前九个模拟器 465、495 秒（最长的 `apps` 约 417 秒）；拆之后第一次权重多半按行数（上一回只跑了一个用例），九个模拟器 410 秒全过，最长的 364 秒；第二次按上一回的秒数，十六个模拟器 296 秒（两个用例失败：天气的设置那个是这个基础上已知的毛病，后来修了；另一个是下面这个）。分段时找到 Shell 的一个毛病：开机后第一件事若是装 TypeScript 应用，预编译字节码的编译器（以应用的 uid 运行）进不了状态目录（只在应用第一次运行时设成 0711），没有字节码；现在编译前就设（`dev` 段开头的「开发者模式」用例，改之前失败）。
- `yocto-ota-test`：每次开机都等 `network-online.target`，它等满 systemd-networkd-wait-online 的 2 分钟，等的是 mac80211_hwsim 的 `hwsim0`（它永远没有地址；现在不归 networkd 管，`05-kario-hwsim.network`），还等 `multi-user.target`，它被 `hvc0` 上的 getty 拖了 90 秒（runqemu 的 nographic 开机给内核 `console=hvc0`，却不给 virtio 控制台；现在 `SERIAL_CONSOLES` 只有串口）；没有显示的那几次开机还要多等 60 秒，等一份 Shell 只为标记好的槽才发的更新回报。现在 1330 秒（以前 2726 秒）：13 次开机模拟时间共 1194 秒（以前 2656 秒），其中三次没有显示的 474 秒（各是健康检查自己的 2 分钟），五次安装 531 秒（RAUC 写一个槽并算校验）；bitbake 打更新包约 2 分钟。
- `yocto-userdata-test`：去挂 `system_a` 的那次开机 3 秒就 panic，以前要等满 300 秒的超时才结束；现在 panic 时就结束：198 秒（以前 500 秒）。
- TCG 的设置（开机加 `phone` 部分，同一个镜像、八种设置并排跑；再是开机加最长的 `apps` 部分，五种）：`-cpu cortex-a57`（机器配置里的）不比任何一种慢：`neoverse-n1`（ARMv8.2，带 LSE 原子指令）、`-smp 8`、2 GiB 的翻译缓存跑 `phone` 时和它差不到 2%（cortex-a57 241–244 秒），跑 `apps` 时差不到 3%（519–528 秒；521、525、539）；`-cpu max` 更慢（指针认证 `pauth-impdef=on` 慢 13%，`pauth=off` 慢 3%），而且会打开 Kario 的内核在 CPU 支持时就用的东西（指针认证、BTI、MTE、SVE），手机的核心（骁龙 845、662、460）都没有；`-smp 2` 时开机里 Shell 自己的服务起得太慢（`phone-shell` 等它们的套接字现在是 3 秒乘 `--timeout-scale`）。所以设置不变。

**开发机上的时间花在哪**（2026-11-25，同一个下午和傍晚，负载 4–24）：这些运行都拿着共用的锁（§5），一个人跑整套时别人的几分钟的子集要等它；所以一次运行里能同时跑的都同时跑。

- `make pixel-selftest`：三种形态以前先后跑，57.5 分钟（每种 1126 秒的用例）；后来同时跑，1086 秒（18 分钟，每种 1065–1070 秒的用例，三种的 128 张像素快照都和基线一样）。同一个子集（`ONLY='^日历'`，`calendar` 一段的 7 个用例）先后跑 377 秒，同时跑 126 秒。现在每种形态的各段也同时跑（三种形态共三十个 phone-shell，各带一个 phone-paint）：171 秒（每种最长的 `keys` 153–154 秒），每种 232 个用例全过，比的 137 张像素快照都和基线一样；三种同时、每种整份串行（`PIXEL_PARTS=0`）1107 秒，128 张都一样（2026-11-25 深夜）。
- `make cts`：各段同时跑 159 秒（十一段，最长的 `keys`），整份串行约 19 分钟（1142 秒的用例）；合起来 237 个用例全过，`phone-cts-verify` 一起查十一份报告：272 个用例，137 个能力都调用过。
- `make selftest`：整份串行 1060 秒（229 个用例，133 张文字快照和基线一样）。现在各段同时跑：161 秒（十段，最长的 `keys` 151 秒），229 个用例全过，比的 142 张文字快照（`00-boot` 每段一张）都和基线一样；同一个构建整份串行（`SELFTEST_PARTS=0`）1090 秒，133 张都一样，`phone-shell --parts` 单独跑 `power`、`chat`、`keys`、`phone` 四段也都一样（2026-11-25 深夜）；各段的快照和整份串行时一样（§2）。拆成二十二段以后（上面「开机自测」那条）：240 个用例全过，比的 154 张文字快照都一样，最长的 `phone` 136 秒（`calendar` 125、`location` 120；2026-11-26 清晨）。
- `go test ./shell`：285 秒（编译 13 秒；拆段以前 353 秒）。先是包里 455 个不并行的测试，107 秒，`TestSelftestScenario` 等它们跑完才开始；它编译、布置所有程序 8 秒，然后十段同时跑，最长的 `keys` 157 秒（`carddav` 147、`location` 135、`calendar` 124；`carddav` 里一个用例就要 92 秒）。还能省的：让场景不等那些测试先跑，约 100 秒，可那些测试里不少对时间敏感（应用无响应、面板），旁边十套服务会挤着它们，没有改。后来（2026-11-26 上午）改到约 105 秒：同一时段改之前 305 秒（负载 6–25），其中包里 535 个不并行的测试 152 秒，场景等它们跑完才编译、布置程序（17 秒），再开始各段（最长的 `phone` 136 秒）。现在起 Shell 的测试里 92 个各在测试程序的一个子进程里再跑一遍，彼此并行（`shell/e2e_test.go` 的 `alone`）：一个进程里起的 Shell 会设整个进程的语言（`i18n.SetLang`），测试还改包变量（时钟、钩子、设备根目录），所以各占一个进程；`-cover` 时子进程把计数写进 `go test` 合并的同一个目录（`-test.gocoverdir`）。子进程同时至多 `GOMAXPROCS` 的四分之一个（至少 2 个，48 核上 12 个）：串行的测试一完，并行的同时开始，一百个子进程一起起来会挤掉其中对时间敏感的（`-race` 下第一次整包跑时 `TestAccountSignInWithCode` 等同步超时，`TestHostileApps/flood` 的假应用发得太慢、没被限流）。输入法词库在测试进程里编一次、写成文件，测试进程和子进程都映射它（`testDict`，像 phone-shell 映射镜像里的 `ime.dict`）：每个子进程自己编的话，`-race` 下要 3–4 秒，算在第一个要给名字注音的测试的等待里（`TestAccountSignInWithCode` 等同步的 5 秒）。假应用（测试程序自己扮的应用，`TestFakeApp`）不编：Shell 给应用的环境里没有词库的路径，假应用也不打字；以前每个假应用都自己编一遍，`-race` 下每次启动 4.7 秒而不是 1.1 秒，CI 的 `race` 作业（2026-11-26，4 核）里 `TestProtoTransition` 在 10 秒里等不到假应用的首页。开发机上把 6 份它同时放在 2 个核上跑（`taskset -c 0,1`）复现，改之前 6 份里 4 份、12 份里 12 份失败，改之后都通过。等 Shell 状态的 `until` 在 `-race` 下也放大 `raceScale` 倍。留在进程里串行的 443 个共 27 秒，其中对延迟有断言的（`endapp_test.go`、`endapp_grace_test.go`、`shutdown_test.go`）故意留着：并行的测试等串行的都跑完才开始，它们跑时旁边没有别的测试。场景在它们跑的时候就以最低优先级（`nice -n 19`）编译、布置程序，它们一完各段就开始。场景分成二十五段：`phone` 拆出 `sms`，`location` 拆出 `positioning`，`calendar` 拆出 `caldav`（通讯录的用例和「拦截名单」留在 `phone`：它们打的电话留在电话的最近通话里，电话里删不掉一条记录，`47-sim-pin` 的对话框后面画着这张表，拆出去以后整份脚本在一个 Shell 里跑和分段跑的这张快照就不一样了）；账号在本机改了以后过 20 秒才同步（`shell/accounts.go` 的 `accountAfterEdit`），`carddav`、`accounts`、`caldav` 里等服务器收到改动的用例以前真等这 20 秒，现在先 `timeshift 20`，等的还是同一件事。通讯录的用例也拆出去时整包约 105 秒；它们留在 `phone` 里，整包约 130 秒（同一天中午，负载约 4），最长的是 `phone`（`make selftest` 里 99 秒，33 个用例），然后是 `chat`（76 秒，35 个用例）和 `positioning`（74 秒，4 个用例）：`chat` 后面用例的快照里有前面用例留下的消息和账单（`17-ime-pinyin` 里的「已转账 ¥10.00」），拆开就和基线不一样了；`positioning` 里的 `sleep` 是在查某件事不发生。
- `make test`：2026-11-26 上午同一台开发机，改之前 821 秒（负载 3–9），改之后 337 秒（负载 6–9，`GOFLAGS=-count=1`，不用测试结果的缓存）。省下的在两步：除 Shell 外的 Go 包 335 秒到 110 秒，Shell 包 295 秒到 107 秒（上一条）。前一步里 `apps/miniapp` 332 秒到 18 秒：`TestSignedProgramCarriesItsCatalog` 关掉的程序还热着（`warmFor`），后面重新加载把它从 `h.progs` 里换掉，`stopAll` 停不到它的运行时，测试程序要等它 300 秒后自己退出，现在测试结束时停掉它；`ota/delta` 112 秒到 29 秒：重的 14 个测试并行。这一步现在卡在 `jsrt` 的 107 秒，它的测试对时间敏感、还改环境变量，没有动。`cargo test`、`cargo build`、`go build` 几步改之后也快，只是因为构建缓存热着；按同样的缓存算，改之后约 385 秒。

## 2. 场景脚本

`shell/selftest.script` 是按键脚本：`== 标题` 开始一个用例，下面一行一条命令，由 `shell/headless.go` 的 `Runner` 执行。命令有三类：

- **按键和等待**：`key`、`type`、`long`、`tap`（只在像素渲染器下），`wait 文字 [毫秒]`、`wait-re 正则`、`gone 文字`、`at 页面`、`key-if 键 文字 毫秒`。`wait` 和 `wait-re` 也认上一次输入（按键、打字、触摸）之后出现过的提示条，哪怕它已经消失：提示条只停 2.5 秒，机器忙时跑脚本的一方可能晚看一步，看着屏幕的人却看到了；`gone` 只看现在的屏幕。`audio 类别` 和 `audio paused 类别` 同样也认 phone-audiod 在上一次输入或上一条 `audio` 之后报过的、处在那个状态的流，哪怕它已经结束：鸿雁的语音消息只有 1 秒，整段一下写进环形缓冲区就关掉，QEMU 的开机自测里虚拟机卡住时跑脚本的一方有一次一眼也没看到它，听的人却听到了；也从上一条 `audio` 算起，免得第二条 `audio call` 拿拨号时的回铃音充数。从没进过那个状态的流照样不过，`audio idle` 只看现在。振动（`vibrated`）、通知灯的闪（`light-blinks`）、紧急警报（`alerts`）数的是次数，读屏（`spoken`）查的是说过的话，都不会漏掉短的；`light`、`backlight`、`keylight` 查的是会保持的状态。深链接进应用（命令面板的「设置 · 应用权限」这类）以后、按下一个键以前，先等它打开的页面（`at g.privacy`），首页上的一行、或者那一页可能本来就在最上面时，等那一行得到焦点（`wait ▌ 指纹与密码`）：Shell 替动作扣住按键只扣 0.5 秒（按 `--timeout-scale` 放大），机器忙时应用答得更晚，下一个键就落在旧页面的焦点上（2026-11-27 主线 r85：设置首页的「我的设备」）。组里的行不用 `wait ▌`：首页上一组得到焦点时，它的副标题那一行也以 ▌ 开头，开头就是组里第一行的名字。在命令面板里找一项、再按两下 OK（`type /`、`type 湿地`、`key enter`、`key enter`）以前，先把焦点放到不会是这一项的地方（`key first`、`key last`）：焦点不在它上面时，第一下是 Shell 自己「转到」它，第二下打开；焦点已经在它上面时，面板最前面的是它自己的动作，第一下就打开了，第二下落在打开的页面上（2026-11-26 主线：「视频跳到哪里」接着上一个用例，焦点还在「湿地」上，第二下把刚开始播放的视频暂停在 0:00）。要看应用的存储重开以后还在的用例，用 `appterm` 结束它（SIGTERM，留时间写完），不用 `appstop`：存储在后台写，屏幕上的数比磁盘上的早，SIGKILL 丢掉正在写的那一次（[29](29-app-development.md) §5.4；2026-11-26 dev：计数器重开后又是 0）。等一个横幅以前，先等前面发出去、还没有结果的事落定（重发的短信又发不出去）：新的通知顶掉旧的横幅，它晚到就顶掉要等的那个（2026-11-26 dev：重发的失败在「流量提醒」之后才到）。
- **模拟外界**：来电和短信（`tel`、`sms`、`mms`）、Wi-Fi 接入点（`wifi-ap`、`wifi-join`）、蓝牙对端（`bt-peer`、`bt-push`、`bt-oldphone`）、读卡器（`nfc`）、位置（`location`）、光线（`lux`）、指纹（`finger`）、时间（`timeshift`）、服务器（`fetch-server`、`carddav-server`、`parent-server`、`store-publish`）等，每条在 `headless.go` 文件头有说明。
- **检查**：`saved`、`sealed`、`perm-state`、`vibrated`、`appstate` 这类直接查系统状态的命令，和 `snap 名字`（存一张快照）。

`require-*` 行让用例在缺东西的环境里跳过（277 行：没有基带 `require-tel`、没有短信服务 `require-sms`、只在 BlueZ 上 `require-bt bluez`、只在像素渲染器下 `require-gui`……）；跳过的在报告里写 SKIP 和原因，不算失败。脚本驱动的模拟硬件，手机上有的没有（板子用自己的，或者根本没有），这些用例在手机上也是跳过，不算失败：`require-fingerprint`（`finger` 碰的模拟指纹传感器；板子在 SKU 的 `fingerprint` 里写传感器，几台手机都没写，phone-keystored 就没有传感器），`require-gnss sim fifo`（`location at` 能移动的接收机：模拟的，或模拟器的管道；手机上是 ModemManager 的；要天气应用的位置的用例也算），`require-lux`（`lux` 设的模拟光线传感器；板子自己的由系统自己找到）。只是锁上手机的锁屏用例用 PIN 码解锁，没有指纹传感器也照跑，最后照样删掉 PIN 码。`require-kernel <保护>...` 在开机探测发现内核缺这项保护时跳过（`shell/kernel.go`，兼容测试报告的 `sandbox.missing`）；`require-previous` 用于接着前一个用例往下做的用例（前一个发的消息、打的电话、数的数），同一份里前一个跑过的用例没通过就跳过（中间因为环境或 `--only` 跳过的不算；只有快照不一样的算跑完了）。用例从不接着别的份往下做：同一份里它前面没有跑过的用例、而有因为环境跳过的，它也跳过，写明是哪一个、为什么（后台任务接着天气往下做，天气要脚本能移动的位置接收机；6T 上它接着了上一份的用例，第一步就失败）。手机可能不满足的其他条件，跳过时都写明原因：`require-own-kernel`（正在跑的内核是这个系统自带的：根目录里有 `/lib/modules/<版本>`；chroot 里是 Nura 的），`require-systemd`（这个系统的服务管理器回答 `systemctl`；在 systemd 不在其中运行的根目录里，`systemctl` 什么都不打印、退出码 0），`require-vpn sim networkd`（两种 VPN 后端都行；chroot 里总线上没有 systemd-networkd），`require-wifi-ap`（`wifi-ap` 能架起接入点：模拟的 Wi-Fi，或 iwd 管着的第二个 Wi-Fi 设备，即模拟器的模拟射频；不拿手机自己的射频来架），`require-mobile-route`（有按应用防火墙时，`net-send` 的包从算作移动网络的网卡出去：移动数据的限制只在那里生效，Nura 上它们从 Nura 的 Wi-Fi 出去），`require-bt-peer`（自测的蓝牙对端是虚拟控制器 btvirt，只连得到虚拟的：手机的适配器是它自己的控制器、或者内核没有 hci_vhci 时跳过），`require-direct-route`（设备的包在隧道外有路由：没有 Wi-Fi、也没有移动数据时，VPN 用例的 `direct`、`blocked` 和内核的 network unreachable 分不开），`require-sleep`（没有别人持着唤醒锁：bring-up 镜像的 `phone-bringup` 让内核不挂起）。`require-button power` 只在有人照请求按板子自己的电源键的运行里跑（环境变量 `PHONE_SELFTEST_BUTTONS` 列着 `power`）：`button power` 一步在输出上打请求 `PHONE-SELFTEST-PRESS: power <n>` 请人按（控制台文件的一行，没按下就再说一遍，见下面「模拟器经控制台交给主机的东西」），自己读内核的输入设备等它按下又松开（无头的渲染器不读键），照渲染器的规则把一次短按或长按交给 Shell（`button-burst power <次数> <间隔 ms>` 请人连按，每一下来了就交）；QEMU 的开机自测由 `yocto/qemu-buttons.py` 从 QEMU 监视器按 virt 机器的电源按钮（`system_powerdown`，[28](28-battery.md) §7），开发机和真机套件的运行不设它，用例跳过。这些步骤都列在 `shell/headless.go` 文件头。

**内核没有 Landlock 时**（6T 用的 Nura 的内核，[41](41-design-gaps.md) §11），「沙箱：独立 uid、命名空间、Landlock」跳过（`require-kernel landlock`，写明原因），不算失败：沙箱按设计少了这一层也照跑，并且说出来（[24](24-sku-framework.md) §7.5），兼容测试报告的 `sandbox.missing` 给认证方写明了缺哪一层；还生效的那些层由「沙箱：已生效的限制层全部符合预期」检查，它在哪里都跑，有一项 ✗ 就失败；DNS 那个用例本来就跳过它的 Landlock 那一半。「内核特性」是唯一把缺保护算作失败的用例，有意如此，Kario 自己的内核（QEMU、整机）什么都不能缺。在别的系统的内核上（Nura 上的 chroot：镜像里没有这个内核的 `/lib/modules/<版本>`）它跳过（`require-own-kernel`），原因里写着那个内核缺什么，兼容测试报告的 `sandbox.foreign` 写明内核不是这台设备的；在镜像自带的内核上它照样失败，写明缺什么（[39](39-oneplus-6t.md) §14.12）。

**同一份脚本的四种跑法**：

| 跑法 | 怎么跑 | 等待放慢 |
|------|--------|----------|
| `go test ./shell` | `#part 名字` 把脚本分成二十五份（chat、apps、radio、switch、btsearch、phone、sms、keys、media、display、location、data、positioning、store、net、carddav、accounts、dev、power、nfc、update、calendar、caldav、family、reset；一个名字可以出现在几处，算一份），每份在自己的一套服务上开机后只跑自己的用例，各份同时跑 | 1 倍；`-race` 下 4 倍（`raceScale`：竞争检测让 Shell 慢几倍，像模拟器） |
| `make selftest` | 快照和基线比对；16 个处理器以上的机器上各段同时跑，分法和 `go test` 一样，每段一个 phone-shell（`phone-shell --parallel`，和 `make cts` 一样），快照、JUnit 报告合在一起；16 个以下（CI）、`SELFTEST_PARTS=0` 和记基线（`UPDATE=1`）时整份串行，前面用例留下的状态后面能看到 | 1 倍 |
| `make cts` | 不比快照，所以各段同时跑：分法和 `go test` 一样再加 `cts` 一段，每段一个 phone-shell（`phone-shell --parallel`：各自的服务、状态目录和端口，从开机起），每段一份签了名的报告，`phone-cts-verify` 一起查；16 个处理器以下的机器（CI）和 `CTS_PARTS=0` 时整份串行、一份报告 | 1 倍 |
| `make pixel-selftest` | 三种形态同时跑，像素渲染器画，每种像 `make selftest` 那样：32 个处理器以上的机器上各段同时跑，以下、`PIXEL_PARTS=0` 和记基线时整份串行；每种形态的输出写进 `build/pixel/<形态>.log`，都跑完后按形态打出来，各自带结果 | 1 倍 |
| `make yocto-selftest` | 几个模拟器同时开机，`phone.selftest=1`，各自的 `phone.selftest-parts=` 写着跑哪几段（兼容用例是 `cts` 一段）：分法和 `go test` 一样，都从开机起，同一个模拟器里的几段在一个 Shell 里先后跑（`phone-shell --parts`）；`SELFTEST_VMS=1` 或 `ONLY=` 时整份串行、一个模拟器 | `--timeout-scale` 放大 |

**几段在一个 Shell 里先后跑时**（整份串行、模拟器里的 `--parts`），每段像单独跑那样从开机时的样子起：Runner 在 `#part` 行上收起前一段留在 Shell 里的这几样，给下一段它自己的：Shell 的钟（`timeshift`、模拟的挂起、`clock-at` 拨开的偏差；应用读到的钟跟着它，[22](22-developer-api.md) §2）、模拟的电量（`power`）、模拟的光线（`lux`）、冻结前的等待（`freeze-after`），还有 Runner 开头就关上的 `screen-auto`、`system-sleep`、`nfc picker`。一段第一次开始时这些是开机时的（钟是机器的钟），脚本回到前面的一段时还给它离开时的（`shell/partstate.go`）；原地重启和 `--keep-going` 的从头起带着钟和各段收起的这些（`Resume`）。2026-11-27 以前不是这样：夜间链 d96 的 vm3 在一个 Shell 里先跑 `power` 段再跑 `family` 段，`power` 的 `timeshift` 一共把钟拨快了 995 秒，`family` 的「屏幕使用时间」第二次看贪吃蛇时就打开了它（下面「写用例的规则」里时刻那一条）。`TestPartState` 在旧的 Runner 上失败（下一段接着前一段拨快的一小时、40% 的电量和 5 lux）。钟因此会往回拨（`location` 段拨快了两天多），Shell 里自己定时、跑了才重新计时的检查以前遇到钟往回拨就停下，直到钟走回那里：作业看网络是否计费的上次查看、防盗锁的两项检查、后台用位置的提醒、长期没用的应用收回权限、后台 CPU、系统的自动更新、账号等网络。现在晚于此刻的读数算作很久以前（`ago`，`shell/alarm.go`），检查照常跑；手机上用户或网络把钟往回拨时也一样（息屏后挂起那两个计时本来就这样处理）。整份在一个 Shell 里跑时「应用商店后台」因此失败过（2026-11-27：前一段把钟拨快了两天，上次查看在将来，商店的自动更新一直等不计费的网络）；`TestJobLooksAgainAfterClockSetBack` 在旧代码上失败。它还撞上了作业的下次运行：商店的进程在前面的段里起来、登记了自动更新，下次运行按那一段的钟排，换段把钟往回拨后要多等那么久。现在换段时作业（应用的和系统的）的下次运行跟着钟挪（`shiftJobs`），各自还要等多久不变；`TestPartState` 查这一点，不挪时失败。别的按钟排好的时刻没有挪：账号的下次同步、闹钟和定时通知，换段时钟往回拨多少就晚多少；手机上用户或网络把钟往回拨时作业也一样（应用启动时重新登记自己的作业）。别的留下的东西由用例结束时自己还原（下面的规则）：`TestPartsLeaveSwitchesAtRest` 查 Runner 不收的那些开关（键盘、Fn 层、电源键、模拟的服务器、`tel` 的慢速和漫游……）在脚本换段的地方都回到原样。不收也不查的：前台的应用和它停在哪一页、应用进程里的东西（用例自己从待机界面打开要用的应用和页面，见下面的规则；段与段之间停掉应用的话，整份串行跑时同一段里接着往下做的用例也接不上了），`scan-code` 举到摄像头前的码（开发机的合成摄像头一开始就摆着那个码），`fetch-server` 起的服务器（留着无害）。`radio` 段的热点用例把热点密码改成 `kario-hotspot-1`、没有改回（原来的是随机生成的）：之后在同一个 Shell 里跑的「换机」快照 `switch-qr` 里密码遮住了，长度却从对齐的空格看得出来，现在连空格一起遮住（`unstable.txt`）。

比快照的两种（`make selftest`、`make pixel-selftest`）也分段同时跑，因为每段的快照只看得到这一段的用例做的事：一张快照不管这一段单独跑（`phone-shell --parts`）、各段同时跑还是整份串行都一样，基线照旧由整份串行记（`UPDATE=1`；`--parallel`、`--parts` 不接 `--update-baselines`）。2026-11-25 以前不是这样：各段同时跑时 147 张比的文字快照里有 28 张不一样（像素每种形态 29 张），都是串行时排在前面的别的段的用例留下的东西。为了逐个找出来，Runner 临时在每个用例开始时记下手机的状态（通知、系统设置、应用的设置和待机小组件、装了哪些应用、「文件」、通讯录、蓝牙和 Wi-Fi、状态目录里的文件），把整份串行和各段单独跑逐个用例对比，每样不同的东西都对得到串行时留下它的用例。找到的和改法：

- 待机界面、锁屏和主菜单上别的段留下的通知（「还有 12 条通知」、菜单上的「鸿雁·1 飞钱·4」、锁屏上各应用的条数；像素的状态栏里也画着）：`phone` 段七个用例（未接来电、几条短信、定时短信、验证码短信）、`chat` 段推送的三个用例、`apps` 段鸿雁回复和两条息屏短信的三个用例，现在各自结束时到通知中心「全部清除」。反过来，`chat` 段主题和壁纸那几张以前靠 `keys` 段的「锁屏通知」清掉了通知才是空的，现在 `chat` 自己的用例清了，基线不用改。商店装的默认短信应用卸载以后它的通知还在，是 Shell 的毛病：卸载时一起拿掉它的通知、应用设置和待机小组件（`removeApp`；`TestUninstallDropsNotificationsAndPrefs`，改之前失败）。
- 设置首页的「已连接「Kario 耳机」」：`radio` 段配对的耳机和两台旧手机。现在「换机（蓝牙）」结束时取消它配对的两台旧手机，最后一个用耳机的用例（「通讯录：蓝牙收名片」）结束时取消耳机的配对。
- 锁屏壁纸（只有像素画）：「主题与壁纸」只把待机壁纸恢复默认，锁屏的照片一直留着，`power` 段键盘锁着时快速选卡那两张后面就是它；现在两张都恢复默认。
- 待机界面上天气的一行：`location` 段的天气每次启动都设它的待机小组件，手机上没有地方去掉。「应用贡献的设置和待机小组件」改成华氏、4 天以后又改回来；用天气的最后一个用例（NMEA 管道，开发机上一个、QEMU 上一个）结束时停掉天气、取消它的后台任务，用 runner 的 `widget com.phone.weather off`（像应用自己用 widget.set 设一行空文字）去掉那一行。
- 应用商店「可安装」里的 `notes.phoneapp`、`notes-bad.phoneapp`：`location` 段放进商店目录的，手机上没有删它们的地方；现在用 runner 的 `app-file-drop`（拿走 `pkg-build`、`mp-build` 放进应用目录的文件）删掉，`apps` 段自己的两个包也是。
- 应用的个数和屏幕使用时间的列表（「12 / 23 个应用」、「图片滤镜」）：`apps` 段装的图片滤镜和 `location` 段装的便签，现在用完就卸载。
- 鸿雁的会话：鸿雁的消息只在它的进程里，`phone` 段的「墓碑」为了测墓碑结束了它，`chat` 段先前的消息就没了，换成「墓碑」发的「明天见」，还有一条「/张三」（鸿雁开在会话里时，命令面板的字打进了输入框）。别的段的消息「墓碑」还不回来，所以 `chat` 段在推送那组用例开头让鸿雁从头起（段开头自己准备要的状态）；「墓碑」也从头打开鸿雁，不再往会话里打字。
- 电话的最近通话（只有像素画出来，`47-sim-pin`）：「SIM 卡 PIN」在电话应用里拨号，PIN 码对话框后面是电话的最近通话，串行时那里有 `phone` 段打的电话。通话记录在 telephonyd 里，手机上没有删的地方；按脚本开头的规则（要用前面用例留下的东西的用例，比如通话记录里的电话，放进那一段），「SIM 卡 PIN」和接着它的「设置 › SIM 卡锁」从 `keys` 移到紧挨在前面的 `phone` 段末尾：整份串行时的顺序没变，这张的基线也不用改。

重记基线的是 20 张文字快照和每种形态 20 张像素快照（像素的是其中在这种形态上画得不一样的，九宫格上没有 `70-usb-trust`、另两种没有 `97-parent-pin-reset`，再加上 `24-flash`：像素画出闪信对话框后面的待机界面，上面原来有 `phone` 段前面的短信用例自己留下的通知，现在它清掉了）。逐张看过，都是少了别的段（`24-flash` 是前面用例）留下的东西：待机、键盘锁、锁屏、充电、USB、西班牙语和快速选卡那几张上没有别的段的通知和天气一行；鸿雁的会话从它自己的开头起；设置首页的蓝牙是「开」，锁屏壁纸「默认」；商店的「可安装」只剩图片滤镜的包；应用 21 个。

**失败时打出来的**：失败的行和原因、当时的整屏文字、各应用进程的状态（冻结没有、在等什么）、Shell 日志的最后几十行；QEMU 上另有蓝牙和 iwd 的日志、快照的 PNG（base64 打在串口上）。系统更新的测试（`ota-test`）失败时也打出当时的屏幕和 Shell 关于系统更新的最后几行日志。结果那一行之前，`PHONE-SELFTEST-FAILED:` 每个失败的用例一行，带着原因。

**模拟器经控制台交给主机的东西**（`yocto/qemu-console.py`；QEMU 开机自测、userdata 测试、系统更新的测试）：主机留下、据以判定的东西都作为「控制台文件」走串口：兼容性报告和它的根（`cts-device.json`、`cts-device.json.root`）、结果（`results.txt`：`PHONE-MODULES:`、`PHONE-CTS:`、`PHONE-SELFTEST-RESULT:` 这几行和退出码）、fd store 检查的结果（`fdstore.txt`）、PNG（`<名字>.png`，base64，经 journal，`phone-shell --print-png`）和 `ota-test` 的 `PHONE-OTA:` 行（`ota-test.txt`，说一行打一行）；机器运行中说的按键和触摸请求也这样走（见下）。每一行一次 write 写成 `PHONE-FILE <名字> <行号> <这行的 Adler-32> <行>`，文件以 `PHONE-FILE-END <名字> <行数> <全部的 Adler-32> [base64]` 结束（镜像里的 `console-file`，和 busybox 的一样只用 POSIX awk）。内核绕过 tty 往同一个控制台打印，一次一整行（selftest-heartbeat.sh 每 30 秒经 `/dev/kmsg` 打的一行，内核的任何消息），这一行可以落在正在写的一行的任意字节处，那行剩下的接在它后面。2026-11-27 之前这些行没有行号也没有校验和，主机留下前缀后面的一切：夜间链 d94 的 280 个用例全部通过，之后 CTS-VERIFY 失败，`invalid character 'p' after array element`，vm5 报告的一行 `    "audio.route",` 后面接上了心跳那一行；内核的行落在前缀里时报告少一行、仍是合法的 JSON，落在 PNG 的 base64 里时那一行丢了，少 57 字节的 PNG 一声不响地写了出来。现在主机先去掉控制台里的终端控制序列和回车；文件的一行要在控制台一行的开头、终端留下的东西（getty 的提示）之后，或 journal 的 `[<秒>] <名字>[<pid>]: ` 之后，内核的一行里引用的（心跳的 `last:`）不算。校验和不对的一行，在其中每个内核行的 `[<秒>] ` 处切开，剩下的从后面一行接上（越过更多内核行；剩下的那部分里再切，至多三层），校验和对上了才留下，并说 `... mended, 1 kernel line taken out of it: ...`。修不好的一行（`damaged`）、缺了的行、没有结束行或行数不符的文件，都报出是哪台机器的控制台日志、其中第几行，文件不留下，`qemu-selftest.sh` 在 CTS-VERIFY 之前就失败；userdata 测试从 `results.txt` 取自测结果，系统更新的测试从去掉了内核行的控制台取 `PHONE-OTA:` 行（`qemu-console.py text`），修不好的一行让它失败。打印这些文件的时候心跳照常：卡住的运行靠它看出来，内核的其他行也一样会落进来。单元测试 ✓（`yocto/qemu-console-test.py`：d94 的拼接和它原样的两行，内核的行在报告一行的开头、中间、末尾，在标记、数字、一个 UTF-8 字符、journal 前缀和 PNG 的 base64 里，在回车和换行之间，两行连着，在已切开的一行剩下的部分里；坏了的、缺行的、没结束的文件；系统更新测试的行。同样的控制台按旧代码的格式交给旧的提取：JSON 不合法、少一行的合法 JSON，或那个短了的 PNG。还有 `programs/shellcmd` 的 `TestSnapshotsAreConsoleFiles`）。QEMU 开机自测 ✓（2026-11-27 夜间链 r86，设备镜像和产品镜像各 8 台机器）：CTS-VERIFY 核对了每台机器的报告（签名对、各自的用例 PASS），每台机器的控制台文件都留下了（`vmN/console.log: N files kept`），PNG 进了 `device-pixel/`，日志里没有 `mended`、`damaged`、`missing`、`cut short` 的行，所以修补那条路仍只有单元测试证明；同一条链上设备镜像的 userdata 测试和系统更新的测试也通过（`PHONE-USERDATA: … PASS`、`PHONE-OTA-RESULT: PASS`），日志里同样没有这几种行。运行的报告、云测试和充电测试的行、GUI 驱动读的行也是控制台文件，见下面「运行的报告和 GUI 驱动读的行」。

**按键和触摸请求**（`yocto/qemu-buttons.py`）：自测的 `button`、`button-burst`、`touchscreen` 几步（`shell/ask_headless.go`）和充电测试的电源键（`charger-test.sh`）在运行中请主机按、点，所以每个请求是一个没有结束行、名字以 `.ask` 结尾的控制台文件里的一行：`PHONE-FILE <press|touch|charger>-<id>.ask <n> <它的 Adler-32> PHONE-SELFTEST-PRESS: ...`（或 `PHONE-SELFTEST-TOUCH: ...`），第 n 个请求在第 n 行，一次 write。id 是随机的，跑脚本的每个 Shell 一个（重启后接手的 Shell 又从 1 数），充电测试每次开机一个。主机照上面的办法修好被内核的行切开的请求，每个名字和行号只回答一次；可能是剩下部分还没来的请求的一行，最多等 10 秒；只读整行（QEMU 一个字节一个字节地写控制台）。还没得到回答（键没按下、手指没落下）的请求再说一遍，同样的一行：Shell 每 3 秒（按倍率放大），充电测试每 10 秒；一行可能被 journal 的限速丢掉，也可能坏得修不好。修不好的、换了说法再说一遍的、要的东西不认识的请求都不回答：`qemu-buttons.py` 报出机器的控制台日志和其中第几行（`buttons.log` 里的 `PHONE-SELFTEST-ASK: <console.log>: line <n>: ...`，修好的和再说一遍的也写在那里），退出码 1，`qemu-selftest.sh`、`qemu-charger.sh` 随之失败。`qemu-console.py files` 不留 `.ask` 名字的文件，也不评判它的行。2026-11-27 之前请求是普通的行，来一行答一行：内核的行落在里面时没人答，那个用例等不到而失败，或者把一个数截短，照截短的数按、点；QEMU 还没写完的一行也这样读。单元测试 ✓（`yocto/qemu-buttons-test.py`：d94 的心跳那一行落在一次按键和一次触摸请求的开头、中间、末尾，各自里面两行连着，一个接一个被切开的请求，正写着的请求，剩下部分在内核的行之后 1.5 秒才来的请求；坏了的、换了说法再说的、要的东西不认识的、剩下部分丢了的请求；再说一遍的请求只回答一次。同样的控制台按旧格式交给旧的读法：要按 4、5、2 下的各只按了一下，有两次没按，照截短的数点了一下，坏了的请求和它的重说都按了，退出码 0。`yocto/qemu-console-test.py`：控制台文件中间的请求，切开的、坏了的、再说一遍的；`shell` 的 `TestButtonStep`、`TestButtonBurst`、`TestButtonAsksAgain`、`TestTouchscreenAsksAgain`）。QEMU 上 ✓ 2026-11-27（`make yocto-selftest` 跑「电源键」「电源键连按（实体电源键）」「触摸屏」：请求是 `press-<id>.ask`、`touch-<id>.ask` 的行，每个只回答了一次，心跳引用的那个没有回答，`qemu-console.py` 没有把 `.ask` 当文件报出来；那次没有内核的行落进请求里，修补只有单元测试证明）。充电测试的请求 QEMU 上**未验证**。

**运行的报告和 GUI 驱动读的行**（2026-11-27 起是控制台文件，同样的格式；没有这些的旧镜像打的普通行照旧读）：

- soak 和 stress 的报告在最后作为一个文件打出来（`soak-report.txt`、`stress-report.txt`），bench 的报告边写边打、最后打结束行（`bench-report.txt`）。`qemu-soak.sh`、`qemu-stress.sh`、`qemu-bench.sh` 用 `qemu-console.py report <日志> PHONE-SOAK|PHONE-STRESS|PHONE-BENCH`：从去掉了内核行的文本里取最后一个 `-BEGIN` 到它后面的 `-RESULT`（bench 没有结果行，到 `-END`）；说 `-RESULT: PASS`（bench：有 `-END`）、文本里也没有修不好或缺了的行才算通过。以前是对原始的控制台 `sed -n '/BEGIN/,/RESULT/p'` 再 `grep 'RESULT: PASS'`：内核的行落在结果行里是假失败，落在 `BEGIN` 里报告一行也不打、照样通过，落在一个数里打出来的数是断的，落在 bench 的 `END` 里是假失败。
- 云测试的 `PHONE-CLOUD:` 行（`cloud-test.txt`，和 `ota-test` 一样说一行打一行，关机前打结束行；`PHONE-CLOUD-SCREEN:` 也在里面）和充电测试的结果（`charger-test.txt`：`PHONE-CHARGER-TEST:` 和 `PHONE-CHARGER-TEST-RESULT:` 行；没有结束行，手机在它结束前就关机或重启了）直接写到控制台；`qemu-cloud.sh`、`qemu-charger.sh` 从 `qemu-console.py text` 取，修不好的一行让它们失败。以前内核的行落在 `script: PASS`、两行 `not started` 之一或 `RESULT: PASS` 里都是假失败（充电测试判成 `PHONE-CHARGER-TEST-RESULT: PA`）。
- GUI 驱动：`qemu-gui.sh` 在内核命令行上加 `phone.gui-console`，镜像里的生成器 `phone-gui-console-generator` 就给 `phone-display.service` 加一个 drop-in：Shell 和渲染器的输出只进 journal，journal 不再把它们抄到控制台上；`PHONE_PAINT_STEPS` 写到 `/run/phone/gui-steps.jsonl`（盖过命令行上 `systemd.setenv=` 给的 `/dev/console`）；再拉起 `phone-gui-console.service`。它的 `gui-console` 用 `journalctl -f` 读 phone-display 和它自己的 journal（`tail -f` 把 steps 文件的行写进它自己的 journal：渲染器把一行分几次写，journal 拼成整行），经 `console-file` 作为一个边长边打、没有结束行的文件 `gui-<boot id 的前 8 位>.log` 打到控制台。`qemu-gui-drive.py` 从这个文件读 `PHONE-GUI-READY`、`PHONE-GUI-INPUT`、电源键和屏幕、面板的行、`gpu:` 和回退到 CPU 的行、`PHONE_PAINT_TRACE` 的合计和 `PHONE_PAINT_STEPS` 的每一段；最后的 `PHONE-GUI-CONSOLE:` 说这个文件有几行、修好了几行、几处不对，修不好的、缺了的行都报出来，并让运行失败。没有 gui-console 的镜像照旧读普通的行（去掉 journal 的前缀）。以前：`PHONE-GUI-READY` 里落一行，驱动等满 900 秒失败；`PHONE-GUI-INPUT` 的 added、removed 或触摸屏那行里落一行，热插拔或触摸判成失败；电源键的行里落一行，数出来的次数不对、判成失败；`PHONE_PAINT_STEPS` 的一行里落一行，那一段就丢了（翻页数、帧时间、各类动画的分位数都少算一段）；`gpu:` 那行坏了，GPU 的判定也跟着错。落进来的不只是内核的行：渲染器把 steps 的一行分几次写到 `/dev/console`，journal 抄过来的行也会插进去。渲染器的错误输出也是一行分几次写（Rust 的 `eprintln!`），Shell 把它转到自己的 stderr 上时，Shell 自己的行就插在中间，还没到控制台，journal 里那一行就已经坏了：2026-11-27 改之前的 GUI 运行里一行 `phone-paint: panel phone-shell: power key: ...`，下一行 `on in 2 ms`，驱动说按了 1 次、0 次到了 Shell，是假失败。现在 Shell 只把整行转过去（`shell/gui.go`，真机上也一样）。gui-work 里 2026-11-24、25 的五次 GUI 运行，其中四次各有 6–8 行 steps（每次 194–262 行）不是合法的 JSON，每次都有 7–21 行控制台的行中间夹着时间戳。
- 单元测试 ✓：`yocto/qemu-console-test.py`（soak、stress、bench 的报告：内核的行在第一行、结果、结束行和一个数里；失败的报告，坏了的、缺了的行，打到一半就断了的报告，没有报告，旧镜像的普通行；云测试的 `script: PASS` 和 `not started`、充电测试的结果里落了内核的行，坏了的结果行），`yocto/qemu-gui-drive-test.py`（内核的行在 READY、一行 INPUT、一行电源键和一行 steps 里，坏了的、缺了的行，旧镜像的普通行；生成器在有、没有 `phone.gui-console` 时写什么），`shell` 的 `TestRendererStderrWholeLines`（渲染器分两次写一行，中间 Shell 写了自己的一行）。同样的拼接交给旧代码：旧的驱动看不到 READY、只数到一个鼠标、电源键的行读成乱码、少一段 steps；旧的 `sed`、`grep` 判出上面那些假失败。
- QEMU 上 ✓ 2026-11-27（gui-work 里，`make yocto-gui` 和其他几项的短运行，同样的几步在改之前的代码做的镜像和这次改动做的镜像上各跑一遍）：改之前 GUI 驱动假失败（`PHONE-GUI-POWER: 1 presses 150 ms apart: 0 reached the Shell ... WRONG`，渲染器的一行被 Shell 的一行切开，见上），控制台上 277 行 steps 里 11 行不是合法的 JSON；改之后通过，503 行都从 `gui-11ba54d7.log` 读（`0 mended, 0 wrong`），278 行 steps 都是整行。soak（1 轮）、stress（1 轮）、bench、充电测试和云测试改之前、改之后都通过，报告和行现在是控制台文件（`soak-report.txt` 5 行，`stress-report.txt` 16 行，`bench-report.txt` 4 行，`charger-test.txt` 两次开机各 15、7 行，`cloud-test.txt` 5 行），bench 的报告里不再夹着落在它的普通行之间的 systemd 状态行。这几次运行里没有内核的行落进这些文件的行里：修补只有单元测试证明。bench 和 stress 用的是默认的镜像，不是 `make yocto-bench`、`make yocto-stress` 做的 web 叠加层的镜像（也不是 stress 的 1 GB SKU）：没有 `python3` 也没有 WPE，bench 打出了报告的首尾和磁盘的行，什么也没测（**未验证**：bench 本身，stress 在它的 SKU 上带新报告的运行，soak 的 20 轮和 stress 的 5 轮）。
- 还是普通的行，因为不需要或做不到：内核自己的整行（`phone-initramfs`、VFS、`setting system clock`、`reboot:`、`phone-charger-generator` 经 `/dev/kmsg` 打的那行）：内核一次打一整行，用户空间的写和别的内核行都落不进去；充电测试里 phone-charger 自己说的 `battery at 3%: starting`（journal 抄到控制台的一行，说完手机就重启，来不及作为文件打出来；内核的行落进去还是假失败）；soak、stress、bench、云测试的 stderr 和其他进 journal 的行，主机不据以判定。

**失败以后接着跑**（`phone-shell --selftest --keep-going`，`--cts`、`--script` 一样）：默认第一个失败就停。加了 `--keep-going`，失败的用例之后像重启手机的用例那样从头起（Shell、它的服务和应用就地重来；`Runner` 的 `KeepGoing`，`shell/headless.go`），从下一个用例接着跑；结果仍是失败，`PHONE-SELFTEST-FAILED:` 和 JUnit 报告列出每个失败，快照比对的计数和兼容测试的能力覆盖算上跑过的所有用例。`make selftest`、`make cts` 和 QEMU 照旧第一个失败就停；真机套件的 `selftest`、`pixel` 用它，chroot 和整机都是（[24](24-sku-framework.md) §6.2）。下一个用例从失败的用例开始时手机的状态起，不是从它留下的状态起：每个用例开始时 `Runner` 把状态目录和持久区的副本更新到当下（`StateCopy`，`shell/keepstate.go`，放在这次运行的目录里），等 Shell、它的服务和应用都停了，phone-shell 把副本放回去，再让手机从头起。Shell 的钟（`timeshift`、`clock-at` 拨过的）也回到那个用例开始时的样子，各段收起的东西一起带过去（上面「几段在一个 Shell 里先后跑时」）；2026-11-27 以前从头起的 Shell 总是机器的钟。只拷不一样的：大小、修改时间、权限或属主和副本不同的文件，或者 inode 在上次拷贝前不久以后变过的（内核给文件盖的是粗粒度的时钟，写的人也可能把时间改回去）；拷贝，不用硬链接，因为 Shell、服务和应用都是原地改写文件的。开销在最后打出来（`PHONE-SELFTEST-KEPT:`，从头起前后的加在一起）：开发机上整份脚本（227 个用例，16.5 分钟）平均每个用例 6.5 毫秒，中位数 2.9 毫秒，最多 195 毫秒（拷得最多的一次是导入音乐的 12 MB，用了 13 毫秒），一共拷了 41 MB，占这次运行大约 1.5 秒；状态最多时 102 个目录里 134 个文件、16.5 MB（副本再占一份）。不放回的：不在手机盘上的东西，即模拟服务器的状态（应用商店后台、推送网关；鸿雁的服务器每次本来就从空的开始）、屏幕和应用进程（从头起以后应用从第一页打开，只给了上一个进程的权限会再问一次：用例自己打开自己的应用和页面，见下面的规则），还有模拟射频的设备和配对（BlueZ 自己保存它的；开发机上从头起以后，用前面配对的耳机的用例会失败，比如「通讯录：蓝牙收名片」，**未解决**）。设了锁屏 PIN 码的手机开机是锁着的，和真手机一样，所以锁屏那组用例开头都有 `unlock 2468`（只在锁着时在锁屏上输 PIN 码）。撤销前面用例设下的东西的用例（删除 PIN 码那个）自己失败时，那样东西就留给后面的用例了。单元测试 ✓（`shell/keepstate_test.go`；`shell/keepgoing_test.go`：一个用例设了锁屏 PIN 码然后失败，下一个开始时没有 PIN 码；一个用例在前面用例设的 PIN 码下失败，下一个开始时锁着，用 PIN 码解开；不放回状态时下一个开始时锁着而失败，和手机上一样）；开发机上，锁屏那组用例在「锁屏通知」里塞一个失败、phone-keystored 不带指纹传感器：只有这一个失败（改之前：设 PIN 码的用例之后的十二个里失败十个，PIN 码一直没删掉）。**未上真机**。

**快照比对**（`shell/snapcmp.go`，`make selftest`、`make pixel-selftest`）：`snap 名字` 存下的快照和 `shell/testdata/snapshots/` 里的基线比，不一样就算这个用例失败（报告里一行 `SNAPSHOT-DIFF:`，最后 `PHONE-SNAPSHOTS:` 计数）；`make cts`、`go test ./shell` 和 QEMU 上不比。

- **定住会变的**：画要比的快照的那一刻，状态栏和待机界面的时钟停在 2030-01-09 09:41（按当时的时区），电量是模拟的 82%（脚本用 `power` 设过的照设的），信号三格，实时网速 0，转场和开机动画停在固定的一帧。脚本开头的开机快照（`sleep` 加 `snap 00-boot`）晚于 1.5 秒的动画才拍到（机器忙，各段同时起来）也照样画开机动画的那一帧；脚本在它之前有别的步骤或已到第一个用例就不再这样。只在自测画快照时这样，产品的显示不变。帧和文字里剩下的时刻（`9:41`、`01-09 09:41`）、本机的端口、设备编号（`PH-…`）、热点名（`Kario-…`）里的字母数字换成 0 再比。比快照的运行里，应用拿到固定的随机种子（`PHONE_TEST_SEED`，贪吃蛇的食物每次落在同一格），phone-mediad 带 `--test-stills`，模拟摄像头拍下的照片都是同一张（取景框照样是活的）。自测每按一个键之前先把当前的画面交给像素渲染器，和人看着屏幕按键一样：列表滚到哪里取决于渲染器排过版的那些帧，以前最多每 100 毫秒送一帧，键按得快时渲染器没见过按键前的那一屏，同一张快照这次比上次多滚一行。
- **文字**：每种形态一份，`text/<形态>/<名字>.txt`，全部约 110 KB；不一样时逐行的差异写在 `build/snapshots/diff/<名字>.diff`。
- **像素**：PNG 一种形态约 120 张，三种约 31 MB，每改一次界面又多一份，不进 git；基线是每张解码后像素的 SHA-256（取前 128 位），按渲染器、形态和字体分文件（`pixel/<渲染器>/<形态>[-<字体>].json`）。phone-paint 用系统里找得到的字体，字体不同画出来就不同，所以文件名带上它会用的那几个字体文件的哈希；这台机器的字体没有基线时只提示「没有像素基线，不比」，不算失败。不一样的那张，本机留着基线的 PNG（`build/pixel-baseline/<形态>/`，`make pixel-baselines` 留下，CI 放在缓存里）时，`build/pixel/<形态>/diff/<名字>.png` 画三栏：基线、这一次、红色标出不一样的像素。每次运行都把这次的哈希写进 `build/pixel/<形态>/snapshots.json`。
- **还是会变的**：`unstable.txt` 一行一条：`名字 skip`（不比）、`skip-text`、`skip-pixel`，或 `名字 mask 正则`（匹配的部分，有分组时是第一组，换成 `#` 再比）；名字可以带 `*`。天气、日历的月和日（跟着真的日期）、原生模块的比较（写着耗时）整张不比，待机界面上的天气那一行（充电、西班牙语，以及只有像素渲染器才画出来的、盖在 USB 信任和快速选卡对话框后面的待机界面）、西班牙语的日期、家长的配对码、飞钱的订单号（按下单的时刻编号，宽屏上在确认框旁边露出来：可信确认和两张指纹支付）、教程示例的推送令牌（base64url，带 `-` 和 `_`，文本界面截短显示）、关于本机里电脑的内核版本和 Shell 已运行的时间只遮住会变的那一段；SIM 卡 PIN 对话框后面电话的最近通话（`47-sim-pin`，只有像素）里已拨电话的副标题整个遮住：通讯录的用例接通就挂，telephonyd 按整秒记通话时长，赶上秒数跳过去是 1 秒（显示时长），没赶上是 0（不显示），机器忙时连着两次不一样；视频和音乐是边放边拍的，进度条遮住、像素不比，模拟摄像头的画面是活的（视频通话的角上、相机的取景框），像素不比；拍下的照片是固定的一张，照常比。`TestUnstableNamesSnapshots` 查每一条都对得上脚本里的快照。
- **更新基线**：改了界面后跑 `make selftest-baselines`、`make pixel-baselines`（就是加 `UPDATE=1`），看过 `git diff` 再提交；记基线总是整份脚本在一个 Shell 里串行跑（各段同时跑时也一样，§2）；整份跑的时候，脚本里已经没有的快照的基线一起删掉。

**写用例的规则**（都是踩过的坑）：

- 用例结束时把手机还原：删掉加的图片、联系人、设置、配对、连过的网络、装的应用、留下的通知（通知中心里「全部清除」）、放进应用目录的文件（`app-file-drop`）、应用的待机小组件（`widget <应用> off`）；后面的用例会数东西，串行时别的段的快照也看得到这些（§2：那时各段同时跑和整份串行的快照就不一样了）。手机上本来去不掉、只能结束进程才没的（鸿雁的会话），由要用它的段在开头准备（从头起）。还原的不只是看得见的：「企业 Wi-Fi」最后连着 `Kario-Corp`，模拟器上 iwd 的另一个射频就每 5 秒去连一次这个记住的网络、配不上地址又断开，Wi-Fi 页的列表每次重建，后面始终开启的 VPN 每次都重连，一个 VPN 用例从 28 秒拖到 87 秒（2026-11-25）；现在它最后 `wifi-forget Kario-Corp`。那个射频还会在用例自己连之前抢先连上：测试网络用的是同一个静态地址，后连的 wlan0 因地址冲突失败，Shell 删掉配置，两个射频一起断开（main 的 r82、r83，机器忙时）；所以测试网络的 iwd 配置里关掉自动连接（`AutoConnect=false`，只对这个测试接入点）。
- 新用例放在它所属的 `#part` 下面；不要依赖别的部分留下的状态（并行时看不到：`go test ./shell`、开发机上的 `make selftest`、`make pixel-selftest`、`make cts` 和 QEMU 开机自测都是分段并行）。模拟出来的状态也算：`keys` 部分的两个指纹支付用例扫的是 `chat` 部分一个用例举到 QEMU 摄像头（vivid）前的商户码，分段跑就失败了（2026-11-25），现在它们自己举；开发机上合成摄像头的桌上一开始就摆着这个码，所以以前没发现。
- 步骤读一天中的时刻的用例（今天和明天、停用时间这类计划的边、按天算的限额和用量、日历的今天）把钟定在离边远的时刻：开头 `clock-at 12:00`（这一天的中午，可往前也可往后拨），结尾 `clock-reset`；`TestPartsLeaveSwitchesAtRest` 也查每个 `clock-at` 的用例以 `clock-reset` 结束。应用读到的是同一个钟（Go 的 `sdk.Now`，kario-jsrt 和 Node.js 下的 `Date`，[22](22-developer-api.md) §2）。定住的有：「屏幕使用时间」（「现在开始停用时间」只到计划的结束 07:00）、「问家长要时间」和「个人热点每天的上限」（按手机的这一天）、日历的月视图（今天、明天）。夜间链 d96 的「屏幕使用时间」在手机的 06:5x 跑，停用时间在用例中间到点结束，贪吃蛇第二次就打开了；`TestScreenTimeCaseBeforeSeven` 在 06:59:50 跑这个用例的停用时间那几步（中间多拨 20 秒，像慢的模拟器），用例不定时刻时失败，定住后通过。不用定的：只按相对时刻算的（闹钟的「+2」、新日程的提醒、每周重复的近 30 天、定时短信的 `sms due`），`nfc clock`、`ota clock` 本来就拨到指定的时刻。没有定住的：「设置 › 位置」的快照里，天气上次用位置是手机的今天就只写时刻（快照 `41-settings-location`；以前设置用机器的钟，比 Shell 的慢两天多，写的是日期），前一个用例最后一次定位和这次拍快照之间跨过手机的半夜（这一段的钟快 50 小时 33 分，即机器的 21:26:40 前后约 20 秒）时写成日期；要定住得跨两个用例，并撤掉这一段拨快的两天。
- 一台机器上同时跑好几份自测（三种形态、各段）：脚本自己起的服务器用空闲端口，不用固定的（`fetch-server 127.0.0.1:0`，地址由 `fill {fetch}` 填进去；以前固定的 47123 在同时跑的第二份里绑不上，`TestFetchServerOfTwoSelftestsAtOnce`）。
- 要撑过 Shell 重启的服务器放在 Shell 的进程外：脚本自己起的服务器（`fetch-server`、`keep-server` 等）都在 Shell 的进程里，`shell-restart` 的 exec 一来就没了；真的服务器在网络那头，Shell 重启时连接不断。所以测连接撑过重启的用例用 `keep-server <地址> apart`：同一个程序再起一份当这个服务器（`shell/keepalive_apart.go`，phone-shell 和测试程序见到 `PHONE_KEEP_SERVER` 就只当服务器），和别的子进程一样从进程线程起、带父进程死亡信号（§6「测试起的进程随测试结束」），phone-shell 不管怎么结束它都随着死；原地重启从同一个线程 exec，它留着。Runner 和它之间的控制连接（一对套接字，一行问一行答）像应用的连接一样跟到下一个 Shell（`Resume` 的 `keep_server`：描述符在 exec 时不关，下一个 Shell 读到续跑文件时马上设回 close-on-exec），`{keepalive}`、`keep-send`、`keep-got` 和进程里的那个一样用；别的 exec（`--keep-going` 从头起、恢复出厂设置的重启）关掉这条连接，它也就结束，`keep-server off` 让它结束并等它退出。`keep-conns <n>` 查服务器一共只接受过 n 条连接、都还开着（应用没有重连）。`timeshift` 拨过的钟不跟过 `shell-restart`（拨的是 Shell 自己的时钟），重启以后的步骤不要靠它：「应用自己的长连接，Shell 重启后」过了期限的心跳用 `keepalive-due`，不快进时间等推送心跳。崩溃的重启（`shell-crash`）`apart` 的服务器不跟过去：Shell 的进程结束了，它随着死。要撑过崩溃用 `keep-server <地址> outside`：同样再起一份，不带父进程死亡信号，放进 systemd 里一个自己的作用域（和应用一样，要应用作用域），因为 systemd 重启服务以前会结束服务的组里剩下的进程；控制连接是运行目录里的一个套接字（`keep-server.sock`，它在上面监听），每个 Shell 连一次（`Resume` 的 `keep_server` 里是路径）；一条控制连接断了它等下一条，1 分钟（乘 `--timeout-scale`）没等到就结束，套接字一起删掉。`keep-reset` 让服务器把开着的连接都以 RST 断开（应用读到错误）；不带 `ws` 的服务器见到以 `GET` 开头的连接就说 WebSocket，C 应用的行连接和 TypeScript 应用的 WebSocket 可以连同一个服务器。单元测试 `TestKeepServerLines`（三种服务器数行、数连接、断开一样）、`TestKeepServerApartCarried`（控制连接经续跑文件交给下一个 Runner 后还是同一个服务器、同一条连接；`keep-server off` 和控制连接关掉时它都结束；`apart` 的不跟过崩溃）、`TestKeepServerOutsideCarried`（控制连接一声不吭地断了，下一个 Runner 照续跑文件里的路径连上，还是同一个服务器、同一条连接）、`TestKeepServerOutsideEnds`（没有下一个 Shell 时等完就结束）、`TestKeepServerTellsWebSocket`。这条用例开发机 ✓（2026-11-27，和相邻的长连接、Shell 重启用例一起跑）；把 `keepaliveAdopt` 改成不接管连接，它在 `keep-conns 1` 失败（服务器接受了 2 条连接）：应用收到 `lost` 重连以后，推送照样收到、数据唤醒的次数也对得上，只有服务器看得出重连。QEMU ✓（2026-11-27，单独跑，`ONLY=`）。
- fd store 检查的用例（`shell/fdstore.script`，只在 QEMU 镜像里：`phone-fdstore-check.service`，应用作用域加 systemd 254 起的 fd 存储，开发机的 systemd 239 没有，[11](11-platform-architecture.md) §14）可以崩溃好几次：每次 `shell-crash` 后 systemd 重启的 Shell 接着在原来的目录里跑（续跑文件的 `base`），像手机的 Shell 崩溃后那样，状态目录（装的应用、`keepalive.json`）和应用的日志都还在，服务重新起；以前它换一个新的临时目录，接管来的应用的日志、装的示例、Shell 存下的状态都不见了。检查单元的存储只有 4 个位置（`FileDescriptorStoreMax=4`，产品的 `phone-display.service` 是 512），应用自己的连接只分到 1 个，两个应用就能争。`fdstore <应用> app|keep yes|no` 问 systemd 它的存储里有没有这个应用（现在的进程）的协议连接或者它自己的连接（`DumpFileDescriptorStore`，打印 `PHONE-FDSTORE-HOLDS`）：看的是存储里真有的，不是 Shell 以为放进去的；`shell-log <文字>` 等 Shell 自己的日志（运行目录的 `shell.log`）里有这段字的一行。`selftest-boot.sh` 还查 journal 里没有 systemd 的 `Cannot store more fds`（放进去的比存储的位置多，systemd 悄悄关掉多的）。`make yocto-selftest ONLY=` 也只跑这份脚本里名字匹配的用例（`/tmp/fdstore.only`），一个都不匹配就不查。
- 一段太长就按用例之间的依赖拆开（2026-11-25：`chat` 拆出 `radio`，Wi-Fi、蓝牙配对、飞行模式、移动数据、热点、换机、蓝牙搜索，连同用配好的耳机收名片的那个用例；`apps` 拆出 `carddav`，三个 CardDAV 用例，第二个要第一个之后的账号编号 `a2`；`power` 拆出 `calendar`，它自己 `clock-reset`、自己装节假日）：拆之前查清每个用例读前面用例留下的什么（联系人、消息、图片、设置、装的应用、通知、电量、摄像头前的码、记住的网络、配对），拆开后各段单独跑都要过。
- 用例从待机界面自己打开要用的应用和页面：失败的用例之后手机会从头起（`--keep-going`），应用只有进程还在时才回到原来的地方（鸿雁在会话里，从头起的在会话列表；飞钱在账单里，从头起的在首页）：用 `wait-re` 等两者之一，再用 `key-if <键> <文字> 0` 从到了的那一个接着走，不用等；新进程再问一次的权限也这样（鸿雁的麦克风：`wait-re 录音中|请求使用`、`key-if enter 请求使用 0`）。接着前一个用例停下的地方往下做、又没法用几行自己准备好的（前一个发的消息、它的通话、它数的数），写 `require-previous`。
- 等要操作的那一样东西，不等随便一段字：`wait X` 只要 X 出现在屏幕任何地方就过（提示条、状态栏也算）。要移到列表里某一行时，等那一行自己的字（它的副标题），或用 `wait-re`；页面上别处（说明文字）没有的字才算：VPN 页的说明里就有「全部流量」，所以等的是行的「· 全部流量」。命令面板列的是它打开那一刻页面上有的行：提示条比行先到（应用先提示「已导入」，再去取新的列表），只等提示条就打开面板，输入名字是「无匹配」，接下来的键落进面板里。
- 等一个值（数、百分比、时长）时等它所在的那一行：`wait-re 预报天数 +4\b`、`wait-re 媒体 +60%`，不写 `wait 4`、`wait 60%`。状态栏一直有 `4G` 和电量（`82%`），时钟有 `10:01`（里面就有 `0:01`），同一页别的行也可能正好是这个数（声音页的「通知提示音」本来就是 60%），这样的等待一下就过，什么也没等到（2026-11-27 夜间第 30 轮：天气的预报天数）。`wait`、`wait-re` 的最后一个词是整数（`+2` 也算）时，它是等待的毫秒数：`wait = 42` 是只等 42 毫秒的「=」（计算器的按键上就有），`wait-re 通讯录 +2` 是只等 2 毫秒的「通讯录」。文字以数结尾时把毫秒数写在它后面（`wait = 42 8000`），正则在数后面加 `\b`（顺带不让 2 匹配上 23）；`TestScriptWaitTimeouts` 查出各脚本里不到 300 毫秒的等待。
- 换页的键之后先 `at 页面` 或 `wait`，再按下一个键；不要隔着换页连按。
- 列表可能循环：到第一项、最后一项用 `key first`、`key last`，不用 `key up 5`。
- 不靠加长等待掩盖竞态：等不到时先查是不是有两个请求的回答顺序不定、旧状态盖了新状态（§6 的例子）。
- 要比的快照每次都得一样：提示条只停 2.5 秒，快照前用 `gone` 等它走；自己会动的（贪吃蛇）在第一步之前停住再拍，继续后马上拍；由耗时决定的数（屏幕使用时间按毫秒给应用排序）先从已知的状态开始（`screen-use clear`）；通知的横幅停 4 秒，什么时候来要看发通知的应用什么时候醒：快照前用 `notified` 等通知进了通知中心，再 `gone` 等横幅走（换机结束后日历导入的通知）；用例结束时自己的横幅还在的，也用 `gone` 等它走，不然下一个用例来得快时它进了那里的快照（日历从「文件」导入的用例里系统导入的横幅：整份跑时有时在紧接着的日历同步的快照顶上，单跑时没有）；等对方回复时要等的是这一条，不是会话里早先那条一样的（鸿雁的机器人回复分享的照片，以前等到的是上一张照片的回复，这次的回复有时在用例离开后才到，成了后面待机界面快照里的横幅和通知）；用例留下的通知要清掉，尤其是带着当天日期的（换机导入的日历文件名），不然进了后面用例的快照；数据还没到的页面写「加载中…」，不写「没有」，用例等它走了再拍（应用商店的装包对话框盖在商店的列表上，以前列表到之前商店写着「还没有安装应用」「目录是空的」，快照四周露出的有时是这个、有时是到了的列表，原来遮住、像素不比，现在修了）。不要用 `unstable.txt` 盖住这类真 bug。

## 3. 模拟了什么

| 部分 | 开发机 | QEMU | 真的那一侧 |
|------|--------|------|------------|
| 基带、通话、短信、小区广播 | telephonyd 的模拟基带 | 同左 | ModemManager 后端对着模拟的 ModemManager（`make mm-selftest`）；真基带：一加 6T 上注册了网络，来电界面出来了，电话没打通，修了**未上真机**，短信、小区广播**未上真机**（[04](04-ui-language-telephonyd.md)「真机」一条） |
| 移动数据 | 模拟的承载 | veth 顶替基带的网卡，systemd-networkd 配地址 | 一加 6T 上中国移动的卡连上过（[39](39-oneplus-6t.md) §16.10）；改接入点、漫游**未上真机** |
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

CI 的规则在 `scripts/ci-plan.py` 的 `RULES` 表里（[38](38-contributing.md) §3）：只改文档只跑检查；改了 Go 包跑这个包和所有依赖它的包；进 `make build` 的改动加跑自测、CTS、像素自测；要跑 `jsrt` 包时，它的测试再在 sanitizer 下跑（`jsrt-sanitize`，§8）；telephonyd 的 ModemManager 后端加跑 `mm-selftest`；`yocto/` 跑配方检查和两个不开机的脚本测试；不认识的文件跑全部。唯一的门禁是 `pr-ok`。往 main 发布后手动跑一次全部（`gh workflow run ci.yml --ref main`）；每个工作流只留最近 3 次的记录（`scripts/ci-prune.sh`）。

CI 不跑的（太慢或要特权）：QEMU 的所有项、`make fuzz` 的长时间运行、浸泡；这些在开发机上按 §5 跑。

## 5. 发布前跑什么

按顺序，开发机上：

1. 检查脚本和 `go vet`（各几秒）。
2. `make test`（约 6 分钟；先 `make build` 和 `make paint`，§6）。
3. `make cts`（约 3 分钟）。
4. `make pixel-selftest`（约 3 分钟）。
5. QEMU：设备镜像和产品镜像各跑一次 `make yocto-selftest`（构建机上九个模拟器，各约 6.5 分钟；两条同时跑会分用处理器，改成并行以后没量过）。
6. 改了 telephonyd：`make mm-selftest`。改了系统更新：`make yocto-ota-test`（产品镜像；设备镜像约 22 分钟）和 `make yocto-ota-delta`（约 75 分钟，基础由 `make yocto-ota-base` 在上一个发布版上存）。改了 initramfs（`slot-init`、`slot-abl`）或 6T 的启动镜像：`make yocto-userdata-test`（四次开机，约 3.5 分钟）和 `make yocto-ota-test`。
7. 全部通过才发布（`make publish`），结果写进 [13](13-prototype.md) §5 和内部的验证基线。

**共用的一把锁**：开发机上的自测、CTS、`go test ./shell`、像素自测都要拿 `/tmp/kario-selftest.lock`（`flock /tmp/kario-selftest.lock make cts`）。一次运行里的各段、各形态同时跑没事：每个 phone-shell 有自己的临时状态目录和服务，脚本的服务器用空闲端口，模拟射频上的换机也是（只有交互的 `make run` 用固定的 47800）；几个人、几棵树的运行叠在一起时，对时间敏感的等待会互相拖慢、偶发失败，所以一次只跑一个，各段同时跑也是为了少占这把锁。`go test ./shell` 要给 `-timeout 30m`：默认的 10 分钟不够时测试被杀（它起的进程随它结束，§6）。

## 6. 规则

- **回归测试先失败**：修 bug 时先写出能复现的测试，在旧代码上跑一次看它失败（失败的样子和现场一样），再修，再看它通过。
- **用例单独跑也要过**：一步要的东西（一个应用的数据目录、一条记录）它自己备好或等到，不靠前面的用例留下：`--only` 只跑一个用例时，它前面只有开机。例：「沙箱里的 DNS」的 `sandbox-read` 给鸿雁算沙箱时要把鸿雁的数据目录交给它的 uid，整机上单独跑「内核特性 + 沙箱」那一轮时鸿雁从没启动过，目录还不存在，chown 失败（2026-11-24）；这一步现在像应用第一次启动那样先建目录（`TestSandboxReadBeforeTheAppStarted`）。真要接着前一个用例往下做的写 `require-previous`（§2）。
- **不拿比源码旧的程序测**：运行树里构建出的程序的 Go 测试经 `tools/prebuilt` 找程序——phone-paint（`make paint`）：`shell` 的 `TestTouchStepsOnPhonePaint`；`make build` 布置的 `build/usr`：`apps/miniapp` 的 `TestNestedSandbox` 和手动的 `TestMeasureHost`；两者都要：`cmd/kario` 的 `TestPreviewImages`、`TestRunProfile`、`TestRunNativeApp`。程序没有、或者比它的源码旧时测试跳过，说出哪个文件更新、该跑什么：phone-paint 比 `paint/src/*.rs`、`paint/locale/` 里最新的旧，`build/usr/bin/kario-system`（`scripts/stage.sh` 每次都重新装它）比模块里测试以外的 Go 文件、`go.mod`、`go.sum`、`stage.sh` 里最新的旧。`make test` 先 `make paint`、`make build`（已经构建过时约 10 秒），并设 `KARIO_PREBUILT_REQUIRED=1`：这些测试在那里程序缺了或旧了就失败，不跳过。起因：`make test` 原来不构建渲染器，核对用的副本里 `TestTouchStepsOnPhonePaint` 跑了更早的树留下的 phone-paint，报「the renderer takes no touch」。比的是修改时间，和 make、cargo 一样：内容换了、时间却更早的源码（带着原来的时间拷进来的）看不出来。`Cargo.toml`、`Cargo.lock` 不算 phone-paint 的源码：只有它们变新时 cargo 不重新链接，算上的话 `make paint` 之后程序也一直显得旧。kario-jsrt（`build/jsrt`）、telephonyd 和 C SDK 的示例应用在 `make test` 里由前置目标先用 ninja 构建，按确切的依赖重建；单独 `go test ./shell` 时端到端测试自己构建这三样（下面「新的工作树」那条），别的包的测试仍不查是否比源码旧（**未实现**）。
- **测试起的进程随测试结束**：端到端测试（`shell/e2e_test.go` 的 `e2eStackWith`）自己起私有的系统服务（不经 Shell，也不经 phone-shell），场景的各段和起 Shell 的测试又各在测试程序的一个子进程里跑（`runChild`、`alone`）。以前服务只在测试的 Cleanup 里被结束，子进程不跑 Cleanup 就没了时它们就留下，父进程成了 init，工作目录在那棵树的 `shell/` 里：go test 的超时（计时器的 goroutine 里 panic）、Shell 的哪个 goroutine 里 panic、子进程被杀（父测试的 context、手动、调用方的超时），还有父测试进程被杀以后子进程往断了的输出里写、被 SIGPIPE 结束。2026-11-26 开发机上一次运行就留下 1525 个（每段 11–12 个服务，一次留下十四段，都是被杀时还在跑的段；只是失败的段跑了 Cleanup，不留）。现在服务和子测试进程都带父进程死亡信号（`Pdeathsig: SIGKILL`），从那个永不退出的线程起（`StartProcess`：Linux 在起它的线程结束时发这个信号，不是在进程结束时，而 `ownThread` 等会结束线程）：起它的进程不管怎么结束，内核都杀掉它们，不靠谁的 Cleanup。没用进程组或扫运行目录：两者都要有人在 Cleanup 里去杀、去扫，正是不跑 Cleanup 的时候才留下。Shell 起应用本来就这样（应用随 Shell 死）；手机上的服务由 systemd 起，`phone-shell --services spawn` 起服务本来就带这个信号，都没改。子进程还把运行目录写进父进程给的文件（`KARIO_E2E_RUN_FILE`），父进程等它结束后删掉它的临时目录，从那里起的还在跑的程序一并杀掉（`reapChild`）：不跑 Cleanup 时那份程序的硬链接（约 190 MB）会留在 `/tmp`。`TestScenarioPartEnds` 起一段（开机加一个用例），让它失败、在别的 goroutine 里 panic（go test 超时就这样）、被杀、起它的测试进程被杀，然后查没有进程跑着它目录下的程序、命令行里有它的目录、环境里有它的文件名；改之前后三种各留下 11 个服务（最后一种还有子测试进程），失败那种不留。mediad 起的 ffprobe、ffmpeg 以前不带这个信号：mediad 被杀时正在探测或解码的那个留下，带着测试的环境，负载高时 `TestScenarioPartEnds` 因此偶尔失败（2026-11-27，留下一个 ffprobe：Shell 第一次启动时往媒体库里放示例视频，mediad 要探测它、截封面，而 `TestScenarioPartEnds` 起的段只跑 1–2 秒）；现在它的解码器也带上（`TestDecodersDieWithMediad`；经 `subproc` 起，见下面「服务起的辅助进程随服务结束」）。go 命令自己被杀时测试程序照样跑完、跑 Cleanup，最晚到它自己的 `-timeout` 结束，它起的都随它死。测试程序自己被杀时不跑 `TestMain`，它布置程序的目录 `/tmp/kario-e2e-*`（约 190 MB）以前就留下，各段 `t.TempDir()` 里那份程序的硬链接也留下，只删前者空间也回不来（2026-11-27 开发机的 `/tmp` 里有 228 个 `TestSelftestScenario*`，最早的是 11-18 的）。现在这个目录按它的主人起名（`kario-e2e-<pid>-<启动时刻>-…`：pid 会被重用，所以带上 `/proc/<pid>/stat` 里的启动时刻），各段的那份程序和运行目录也都在它里面（`stack-*`）；下一次运行布置程序前（`stagedPrefix`）删掉主人已经不在的这种目录，从里面起的还在跑的程序一并杀掉（`removeStaleStaged`）。主人还在的（别的正在跑的运行）不动，名字里没写主人的（旧代码起的、Kario Studio 的 `ide/vscode/scripts/e2e.mjs` 起的）也不动。`TestStaleStagedRemoved` 测这几种：活着的别的进程的、本进程的、主人已结束的、pid 被重用的、名字里没写主人的，还有从死掉那次运行的目录里还在跑的程序。场景跑到一半时用 SIGKILL 杀掉测试程序（2026-11-27，开发机）：改之前留下 23 个目录，共 612 MB（布置程序的目录和 22 个 `TestSelftestScenario*`），改之后留下一个 625 MB 的目录，都没有进程留下；下一次运行开始 9 秒后它被删掉。
- **新的工作树里 `go test ./shell` 也全跑**：场景要 kario-jsrt（TypeScript 写的计算器和日历两个系统应用、示例应用要它：没有它 `scripts/stage.sh` 装的这几个应用没有模块，起不来）、C SDK 的 C++ 示例应用、原生示例的库，还有 phone-telephonyd（`require-tel` 的用例要它，它跑自己的模拟基带），由 `make jsrt`、`make capi`、`make native-example`、`make telephonyd` 构建在 `build/` 里。新的工作树里还没构建过时 `go test ./shell` 以前有 11 段失败。现在端到端测试布置程序前自己跑 `make jsrt capi native-example telephonyd`（同一棵树里一次一个：`flock build/.e2e-make.lock`）：新工作树里第一次多约 60 秒（QuickJS 的源码包从 `~/.cache/kario` 拿，没有时从 GitHub 下载），telephonyd 再多约 30 秒，之后 ninja 无事可做，约 1 秒（telephonyd 每次还跑一遍它的单元测试），改了 `jsrt/`、`sdk/c/`、`telephonyd/` 的源码也按依赖重建。没选跳过：计算器和日历出现在脚本的两百多行里，跳过的话新工作树里差不多一半的段不跑，而 `go test ./shell` 不加 `-v` 只说 ok，看不出没跑。`make test` 先构建它们并设 `KARIO_PREBUILT_REQUIRED=1`，这时测试不跑 make，这条路不变。构建要的和 `make build` 一样（cmake、ninja、GCC 14）；缺了时场景在布置那一步失败一次，带着 make 的输出，不是十几段各自失败。telephonyd 是 2026-11-27 才加进来的：以前 `scripts/stage.sh` 只在有 `build/telephonyd` 时装它，场景就随那棵树构建过什么而不同，有它时 287 个用例里 65 个 `require-tel` 的照跑，没有时都跳过；合并的人在开发树里跑 `make telephonyd`、`make mm-selftest`，开发树的 `go test ./shell` 和一份干净副本的跑的就不是同一套。那时报告的「有 `build/telephonyd` 时 `phone`、`switch` 两段失败」（从 `/` 打不开计算器；换机后日历没发导入的通知）是那棵树有 telephonyd、没有 kario-jsrt（端到端测试那时还不构建它）：这两段里要 telephonyd 的用例这才跑起来，要用的计算器和日历是 TypeScript 写的，没有模块，起不来；不是 telephonyd 或 Shell 的错，在现在的代码上用只有 telephonyd、没有 kario-jsrt 的前缀跑这两段，在同样的地方同样失败，有 kario-jsrt 时有没有 telephonyd 都全过。选总带 telephonyd、不选总不带：`make test` 和 CI 本来就先构建它再跑 `go test ./shell`，总不带的话这 65 个用例在开发机上就没人跑了。布置完还查前缀里有 phone-telephonyd、kario-jsrt 和 C++ 示例应用（`stagedLacks`），缺一个就在布置那一步失败，写明要 make 什么，设了 `KARIO_PREBUILT_REQUIRED` 不跑 make 时也查。场景和 `TestScenarioPartEnds` 在一个测试进程里只编译、布置一次（`stagedPrefix`）。整包（开发机，2026-11-26 晚上，负载 4–12）：新工作树里 173 秒全过（改之前 121 秒，11 段失败），都构建过时 124 秒（改之前 130 秒）。带上 telephonyd 以后（2026-11-27 凌晨）：有 `build/telephonyd` 时 136 秒，删掉它再跑时 144 秒（其中构建它约 30 秒），都全过，每个用例两次的结果都一样（287 个里 241 个通过，46 个跳过，跳过的都是主机上没有的设备和功能，没有一个是缺 telephonyd）。
- **服务起的辅助进程随服务结束**：手机上 systemd 结束服务的整个 cgroup，但没有 systemd 管的地方（`phone-shell --services spawn`、`programs/shellcmd` 的看护、测试），服务崩了或被杀时它起的辅助进程就留下，带着旧的状态：audiod 的 `aplay` 占着声卡，重启的 audiod 用不了；`alsaucm` 等着声卡；Shell 的一次编译。现在由 `subproc` 包起这些辅助进程：`subproc.Command` 设父进程死亡信号（`SIGKILL`），`subproc.Start`（还有 `Output`、`CombinedOutput`）从它自己的一个线程起，那个线程一直锁着、永不退出，所以信号在程序结束或 exec 时才来，不是在 goroutine 碰巧所在的线程结束时（goroutine 锁着线程退出时 Go 结束那个线程，没锁的 goroutine 起子进程用过的线程以后也可能变成这样的：Shell 里的 `ownThread`、VPN 和流量统计的代码）。要活过 Shell 原地重启的（应用、渲染器、服务）照旧经 `shell.StartProcess`。现在带上的：audiod 放音的 `aplay` 和读声卡参数的 `aplay`（`programs/audiod`）、`alsaucm`（`audio/ucm`）；mediad 的 ffprobe、ffmpeg（`programs/mediad`，以前用它的 `decoder`）；Shell 不进沙箱的 `kario-jsrt --compile`（`shell/jscache.go`；进沙箱的本来带信号，但从 goroutine 碰巧所在的线程起）和自测的企业 Wi-Fi 接入点的 hostapd（`shell/radio_hostapd.go`，同样；它只在 QEMU 和手机上跑，**这次改动没在那里跑过**）；`kario run` 起的 Shell（`cmd/kario/run.go`，用 SIGTERM，让它正常结束）：手动杀掉 kario，或测试跑着时被杀，Shell 就留下，占着预览的端口；telephonyd 的 `run_program` 跑的程序（查 IMS 状态，`telephonyd/src/run.cpp`）：从 telephonyd 唯一的线程 fork 再 `PR_SET_PDEATHSIG`，不用设不了它的 `posix_spawn`；这个信号只结束程序本身，所以程序的进程组里还有一个看守子进程，等同一个父进程结束，到时杀掉整个进程组（程序起的进程：查 IMS 状态脚本的 `qmicli`），每次运行在程序退出或超时时也连同它的进程组一起结束。以前就带、没改的：Shell 经 `StartProcess` 起的（应用、渲染器、webview、usbhelper、`--services spawn`）、audio/hifi 的 ffmpeg、充电界面的渲染器、shellcmd 并行的各段、小程序的运行时、端到端测试的服务。有意不管的：自己会结束的命令（systemctl、ip、networkctl、udhcpc、`rauc info`、`kario build` 的编译器）、只在手机上由 systemd 管着跑的（`journalctl -f`、rauc 安装的客户端，都在 `KillMode=mixed` 下）、QEMU 里 `phone-selftest.service` 下自测的辅助程序，还有 `kario` 交互用的子进程（设备 shell 和调试器的 ssh、测试和预览的 node），随它们的管道或终端结束。jsrt 和 C SDK 不起进程。测试都在主机上跑，不要声卡：测试在测试程序的一个子进程里把自己再跑一遍，子进程起一个替身（写下自己的 pid 然后睡着的脚本）后被杀（`subproc/subproctest`）：`TestAplayDiesWithAudiod`、`TestAlsaucmDiesWithAudiod`、`TestCompilerDiesWithTheShell`、`TestRunShellEndsWithKario`、telephonyd 的 `test_run_program`；线程那条规则，从一个随后结束的线程起辅助进程：`TestAplayOutlivesTheThreadThatStartedIt`、`TestPlayerDecoderOutlivesTheThreadThatStartedIt`；`subproc` 自己的 `TestChildOutlivesTheThreadThatStartedIt` 在 `Start` 从调用它的线程起子进程时失败。每个都不到一秒，其余的在旧代码上都失败（替身活过了它的父进程，或辅助进程随线程被杀），只有 `TestAplayDiesWithAudiod/playing` 例外：那个 `aplay` 本来就带信号。布置时的构建（上面的 `e2eBuild`）：测试进程构建时被杀，`go build`、`flock … make` 和它们的任务就在那棵树里接着跑，锁也占着。现在每条命令都在再跑一遍的测试程序（`leadSession`）下跑，它是自己那个会话的首进程，带父进程死亡信号（SIGTERM）：测试进程结束时它给会话里每个进程发 SIGTERM，3 秒后还在的发 SIGKILL。按会话，不按进程组：ninja 把它的任务各放进自己的进程组。离开了会话的程序（`setsid`：别的树的构建可能共用的守护进程）不动，别的树的构建在别的会话里。make 往共用的 `~/.cache/kario` 里拷 QuickJS 源码包时被结束，会在那里留下半个包，所有树的构建都因此失败：Makefile 现在先拷成临时的名字再改名。`TestStagingBuildEndsWithTheTest`（不到一秒）起两个布置的进程，两棵树各有自己的锁，用假的 `go` 和假的 `make`（它的任务在自己的进程组里），杀掉一个：它的构建什么都不剩，另一个接着跑；在旧代码上 flock、make 和它的任务都活过了它。
- **Shell 停下时结束它起的辅助进程**：`Core.Shutdown` 以前只结束应用和媒体订阅。Shell 在一个进程里停下又接着跑的地方（端到端测试、`kario test` 在真的 Shell 上跑、以后的原地重启），它起的辅助进程就留到进程结束：插着线时的 USB 共享助手（phone-usbshared），测试杀掉它以后 Shell 还又起一个（`usbHelperEnded` 不知道 Shell 在停）；正在跑的 `kario-jsrt --compile`；应用的 web 进程（phone-webview：结束它的是应用退出的消息，那要等事件循环，`Shutdown` 正占着循环）；自测的企业 Wi-Fi 接入点的 hostapd 和热点上的模拟设备（wpa_supplicant、udhcpc 这些，和它的线程、网络命名空间）；电脑跟随着的系统日志（`journalctl -f`，测试里是替身）。从主线程起的 USB 助手和 web 进程在 phone-shell 重新执行自己来重启手机时也留下：exec 的就是起它们的那个线程，父进程退出信号不来（按这个信号的规则推断，没有单独复现）。现在它们启动时都记进 Shell 的辅助进程表（`shell/shutdown.go`），`Shutdown` 按各自本来的停法结束它们：USB 助手关掉 socketpair 的一端，编译和 web 进程杀掉（web 进程在应用之后，和它随应用结束一样），hostapd 发 SIGTERM（它的父进程退出信号），模拟设备结束它的线程，跟随的日志关掉连接（和电脑走了一样）；都在应用的宽限期（3 秒）内等着，到时还在的杀掉，`Shutdown` 返回时它们都不在了，也收过尸；Shell 停下以后这些都不再启动。phone-shell 收到 SIGTERM 时在信号的 goroutine 里调用 `Shutdown`，事件循环还在跑，所以这张表有自己的锁，`Shutdown` 不碰它们别的状态，收尾（`usbHelperEnded`、`webProcEnded`）照旧在循环里做。没改的：渲染器归起它的一方（调用 `StartGUI` 的都 `Close`，`Close` 会等）；一个自测步骤或一次请求里跑完的命令（ip、udhcpc、networkctl、btmon、`rauc info`、systemctl、sandbox-read、截屏用的无头渲染器）；rauc 安装的客户端（装是 RAUC 服务在装，杀掉客户端就成了装失败；手机上由 systemd 管，见上一条）；不是进程的（VPN 和保活的模拟、`fetch-server` 这些测试服务器都在 Shell 的进程里，模拟器上模拟的电脑是一个线程和它的网络命名空间）；mediad 的解码器（是 mediad 的，Shell 关掉的是自己的订阅）。`TestShutdownEndsHelpers` 在自己的子测试进程里逐个起这些（替身是写下 pid 的脚本），`Shutdown` 返回时查测试进程没有剩下的子进程（按 pid，僵尸也算）：USB 的网络共享、文件传输、不理会 socketpair 的助手（宽限期满被杀），编译，web 进程，企业接入点（还查它收到的是 SIGTERM），热点上的设备，跟随的日志（还查电脑那头的请求结束了），以及在事件循环以外 `Shutdown`（像 SIGTERM 时那样，`-race` 下也过）；在旧代码上每个子测试都失败，USB 的三个还在测试杀掉助手后又起了一个。hostapd 和模拟设备只在模拟器上跑，这里只用替身测过。2026-11-27 夜间链 r86 的两个镜像都带着这次改动（`shell/shutdown.go` 和 `radio_hostapd.go` 的 `stopEAP` 和这里的一样），模拟器上「企业 Wi-Fi（802.1X）」「个人热点（模拟器）」「换机（模拟器，同一 Wi-Fi）」都过：模拟设备由用例自己撤掉；企业接入点的 hostapd 一直开到那台虚拟机的用例跑完（那里的内存报告里还有它），Shell 最后的 `Shutdown` 照上面给它发 SIGTERM，然后 phone-shell 正常退出（退出码 0）。`Shutdown` 返回时 hostapd 真的不在了，模拟器上**未验证**：没有用例查，接着就关机了。
- **渲染器的测试带上调试检查**：`make test` 和 CI 用 `cargo test --profile checked` 跑 phone-paint 的测试：`checked` 是 release 加上 `debug-assertions` 和 `overflow-checks`（依赖的 crate 也一样，`paint/Cargo.toml`），发出去的渲染器和各种自测用的 release 版从不检查这些。耗时和 `--release` 差不多（开发机，2026-11-26：138 个测试 14.8 秒对 17.0 秒，冷构建都约 45 秒）。tiny-skia 自己的调试断言也在里面，所以 `a_rect_between_two_columns_paints_both` 在这里不拿 tiny-skia 的原样填充对照（那正是它断言的那种矩形），只看我们的画法不触发它。像素自测用 `checked` 版的 phone-paint 画过一次九宫格（244 个用例全过，137 张像素快照都和基线一样，渲染器没有 panic），没有接进 `make pixel-selftest`：渲染器崩了看门狗会重启它，要另外收集它的标准错误才看得出来。
- **能力全覆盖**：新能力要有用例调用它；`make cts` 报「never called」就不过（例：账号的「使用移动数据」开关，补了用例才过）。手机上可能跳过的用例用 `covers <能力>...`（放在用例开头、要求之前）写明它调用哪些能力：别处都没调用的能力就和这个用例一起算作跳过，带着它的原因（报告的 `skipped`、`PHONE-CTS-SKIPPED:`），不算从未调用，正如用例本身跳过不算失败。6T 上 location.watch、location.stop、settings.apps、settings.app.set、widget.set 只有要脚本能移动的位置接收机的用例调用。
- **不可信输入有模糊测试**：网络、文件、蓝牙、NFC、短信、更新包、应用发来的消息。模糊测试找出的问题留下最小输入做回归用例（`testdata/fuzz/`）。
- **偶发的失败当真的 bug 查**：先单独重跑一次分清是偶发还是必现，再找根因。例子：
  - 设置里蓝牙刚打开就按搜索，Shell 把两个改动并发交给 BlueZ，晚回来的旧状态又把找到的设备冲掉。修法是改动按先后执行、设置丢掉过期的刷新，而不是加长等待。
  - 命令面板的「转到」只列打开时页面上已有的行，真的 iwd 上网络那一行晚到。用例改成等那一行出现再按 `/`。
  - 「问家长要时间」只在晚上 22:38 以后失败：前面日历提醒的用例 `timeshift 4900` 把手机的钟拨快了 1 小时 22 分，之后没有拨回（`timeshift` 是累加的，只有 `clock-reset` 归零），快到半夜时手机已经是明天，家长的服务器按它自己的时间不收这个请求。和服务器对时间的用例开头先 `clock-reset`（日历的月视图也这样；2026-11-27 起每段从机器的钟起，这两个用例改成用 `clock-at 12:00` 定住时刻，§2）。钟拨回后「恢复出厂设置」又失败了：前面用例里登录的 Kario 账号绑定没成功，下次再试的时刻按拨快的钟算，退出账号后也留着，钟拨回 1 小时 22 分，新登录的账号就要多等这么久才绑定。修法是退出账号时忘掉这次重试（`shell/antitheft.go`），新登录的账号马上绑定。
  - 换回简体中文 0.44 秒后打开卡包，卡名还是西班牙语（约百次整份运行里一次）：Shell 随整理（亮屏每 0.5 秒）才把新语言的策略交给 nfcd，卡包开着时不会再问。修法是 `nfc.state` 先等 nfcd 拿到此刻的策略（`shell/nfc.go` 的 `nfcFresh`），而不是让用例多等。
- **机器忙时的偶发**：QEMU 用软件模拟（TCG），很多个模拟器同时跑时比单独跑慢（开机自测的八个：最长的部分随负载 446 到 519 秒，串行时 435 秒，2026-11-25）；判断偶发要在机器不忙时重跑，或者少开几个模拟器（`SELFTEST_VMS=`）。分成十一段以后的四次运行里「视频」用例失败过一次（播放停在 0:00；负载约 11），另外三次都过，原因**未查**（2026-11-25）。那次日志里 phone-mediad 的 ffmpeg 报的 “Error parsing Opus packet header.” 指不出原因：ffmpeg 8.0 每次读到 WebM 文件末尾都打这一行，正常播完的也一样；层里已带上游的修复（[20](20-audio.md) §9.1）。CI 的机器核少，Shell 包的十份并行会挤掉别的包里对时间敏感的测试，所以 `make test` 和 CI 都让 Shell 包最后单独跑。超时要看超的是哪一步：`TestHostileApps/noread` 在忙的开发机上十次里失败过一次，用了 5.04 秒；它查的那一步（应用不读消息时 Shell 结束它）从队列满到收尸只要 10–40 毫秒（负载 70 时也是），等它的 5 秒超时的话整个子测试至少 5.33 秒，超时的是开头等假应用说 hello 的 5 秒。假应用是测试程序本身的新进程（约 50 MB），机器忙时启动可能要几秒（推断，没有复现出来）；等启动的上限改成 30 秒（`shell/hostile_test.go` 的 `fakeAppHello`），起不来的照样失败。`TestInstallProxy`（代理停下后下载不再继续）在负载约 50 时失败过一次：1 MiB 的包在停下之前已经整个进了回环上的套接字缓冲，`go test -cpu 1` 时 300 次失败 299 次（改之前的代码上也是）；现在测试的服务器把整个下载第一个八分之一以后的部分扣到请求开了 10 秒才发，停下的代理在那之前就结束了请求，停不下的照样失败（2026-11-26）。`TestDevLink` 在 `make test` 里失败过一次（「已停止与电脑共享」）：测试直接设了「电脑已信任」，模拟的线却没插上，Shell 起来约半秒后第一次例行检查 USB 时看到线「拔了」，就结束了共享；测试跑得慢、那次检查落在信任之后才出现。现在测试把模拟的线随信任一起插上（`TestDevLinkDebug`、`TestDevLinkAttach`、`TestUSBFirewallFollowsDeveloperMode` 也是，`devOpen` 和 `TestUSBFirewallDrains` 本来就这样）；在信任之后插一次检查，改之前同样失败，改之后通过（2026-11-26）。
- **结果写进文档**：状态行写环境和日期（开发机、QEMU、真机），没跑的写 **未验证**，模拟的写「模拟」；评估类的数字写「估算」。

## 7. 还没有的

- **真机**：一加 6T 的两种测试用同一个套件（`tools/phone-chroot/`，[24](24-sku-framework.md) §6.2）、同样的步骤（push、setup、selftest、pixel、collect）和同样的结果目录（`kit.txt` 记着测的提交和镜像的提交，`facts.txt` 抹掉序列号，JUnit、日志）：chroot 里跑这台手机自己的镜像（刷机用的 userdata 镜像，边推边解开；QEMU 的镜像也行，套件说它缺什么），整机上（`--whole`）经 USB 控制台的网络跑刷进去的同一个镜像；一套步骤写在 [39](39-oneplus-6t.md) §13，要在手机上核对的事列在 [39](39-oneplus-6t.md) §14；一台 6T 在另一台电脑上做了准备（备份，[39](39-oneplus-6t.md) §9.1），chroot 套件在上面跑过一次（2026-11-21：自测停在第 39 个用例，`shell-start` 之后手机卡死，[41](41-design-gaps.md) §11、§13）；整机的镜像和整机的测试还没有在上面跑过，**未上真机**，整机上还做不到的（开发者通道上跑自测、真射频上的 Wi-Fi 和蓝牙用例、持唤醒锁时的挂起用例）见 [24](24-sku-framework.md) §6.2。真机上要跑同一份场景脚本，加上模拟器测不了的：通话音质和路由、射频、功耗和待机电流、温度、真屏的刷新。
- **像素基线**：比对有了（§2），基线还没记下：开发机上要整份跑一次 `make pixel-baselines`（约 30 分钟），再整份跑一次 `make pixel-selftest`，每张都应当又一样，不一样的写进 `unstable.txt` 并写明原因，然后提交 `shell/testdata/snapshots/pixel/`；CI 的 Ubuntu 上 phone-paint 找到的字体和开发机的不同，CI 的基线要从 `pixel` 作业的产物里拿 `snapshots.json`，放到日志里写的那个路径（**未验证**）。目前只拿脚本的前 18 个用例（开发机上跑其中 14 个，连开机动画共 14 张快照）在三种形态上各记一次、隔一分钟再比（开发机，2026-11-21）：第一回视频通话角上的摄像头画面、宽屏上飞钱的订单号不一样，写进 `unstable.txt` 后重记，比的 13 张都一样；相机和视频两个用例单独记、比，取景框、拍下的照片、视频画面每次都不一样，也写进了 `unstable.txt`。文字的基线在开发机上记下（2026-11-21），之后隔 1.5 小时、2.5 小时各整份比了一次：视频的进度条（边放边拍）、关于本机里的内核版本和已运行时间、应用商店装包对话框四周露出的还在载入的列表不一样，都遮住（对话框那张像素不比）；第三次又找出音乐的进度条（同样边放边拍），以及右对齐的已运行时间变长时位置跟着挪，也都改了；第四次整份比，比的 133 张都一样（另有 5 张按 `unstable.txt` 不比，5 张所在的用例开发机上跳过）。
- **覆盖率**（`make coverage`，`scripts/coverage.py`）：Go 分三列，按语句数算：单元测试（每个包的 `go test`，`-coverpkg` 是整个模块）、整份文本自测（`phone-shell --selftest --keep-going`，程序带 `-cover -covermode=atomic -tags kariocover` 编译，像 `make build` 那样布置）和两者合并。Go 运行时只在进程正常退出时写计数，系统的进程却很少正常退出（应用和服务被 SIGTERM、SIGKILL 结束，沙箱启动器和 Shell 会 exec 别的程序），所以覆盖率构建里每个程序每秒把计数写进 `GOCOVERDIR`（`tools/covflush`，由每个程序都链接的几个包里的 `cover.go` 启动），启动器和 Shell 在 exec 之前再写一次，锁到自己描述符上的辅助进程改写它开着的那个文件；Shell 把 `GOCOVERDIR` 交给应用和 USB 共享的辅助进程，只在覆盖率构建里 Landlock 放行这个目录（`sandbox/cover.go`：能读写，不能执行）。别的语言按行算：phone-paint 的测试（`-C instrument-coverage`，用和 rustc 同一版 LLVM 的 `llvm-profdata`、`llvm-cov`：`rustup component add llvm-tools`，或者给 `LLVM_PROFDATA=`、`LLVM_COV=`），telephonyd 和 C SDK 的测试（`--coverage`，编译器自带的 gcov），Node.js 下的 JavaScript（单元测试里跑的 `sdk/ts/node`，`NODE_V8_COVERAGE`，`kario` 写到别处的那份运行时按文件名和长度认回来；Kario Studio 的测试，`node --test --experimental-test-coverage`，先在 `ide/vscode` 里 `npm ci`）。缺工具的那部分跳过并写明原因；有测试失败时照样出报告，退出码是 1。它跑自测和 `go test ./shell`，要拿锁：`flock /tmp/kario-selftest.lock make coverage`；`PARTS='go-e2e rust'` 只跑几部分（别的沿用上次的结果，报告里写着各自的时间；单元测试和自测两次之间改过的 Go 文件只按后跑的那次算，摘要里列出），`ONLY='正则'` 只跑部分自测用例。报告在 `build/coverage/`：`summary.txt`、`summary.md`、`report/index.html`（合并后的 Go 剖面、phone-paint 的 llvm-cov 页面、逐行标注的 C/C++ 和 JavaScript 源码）。CI 里是手动触发的 `coverage` 工作流（[38](38-contributing.md) §2），报告作为产物上传、摘要写进运行摘要，在 GitHub 上**未验证**。
  - 第一次测量（开发机，2026-11-23，`c653133` 加上这次的改动；48 核，同时有别人的 QEMU 和测试在跑）：单元测试 11.3 分钟，自测 16.8 分钟（含编译和布置），其余 1 分钟，一共约 30 分钟。Go 合并 **78.3%**（72 709 条语句；单元测试 63.1%，自测 56.3%）；自测 227 个用例全过，比的 133 张文字快照和基线一样，288 个进程写了计数。重点的几块（单元测试 / 自测 / 合并）：Shell 53.7% / 62.3% / 75.1%（28 486 条）；能力 broker（`shell/broker.go`、`broker_phone.go`）50.1% / 85.0% / 90.5%；沙箱（`sandbox`、`programs/sandboxcmd`）34.2% / 41.3% / **43.8%**，最弱；keystore（连 phone-keystored）76.2% / 65.4% / 84.6%；`wire` 88.5%；`pkg` 87.8%；系统更新（`ota/...`、phone-ota-delta）81.9%，自测里只有 1.3%。telephonyd 的测试 85.2%（1 790 行），C SDK 75.8%（`phoneui.c` 89.0%，示例应用 0%），phone-paint 的测试 75.1%（9 485 行，函数 76.9%），`sdk/ts/node` 69.2%（2 895 行），Kario Studio 62.5%（`extension.ts` 0%）。
  - 没测到的最大的十块（按没跑到的语句或行数和它的分量挑的）：
    1. 沙箱在 root 和 Landlock 下的路径：`sandbox/landlock_linux.go` 8.5%，`apply_linux.go` 35.7%（命名空间、独立 uid），`selfcheck_linux.go` 12.5%，`shell/sandbox.go` 41.4%，`shell/cgroup.go` 在自测里 7.9%。开发机不是 root，RHEL 8 的 4.18 内核没有 Landlock；这些只在 QEMU 和手机上跑，覆盖率量不到。
    2. 网络上的强制：每个应用的防火墙 `netfw` 37.1%（`netfw.go` 25.3%），VPN 的 netlink（`vpn/netlink_linux.go` 17.8%）：要 root、nftables 和 WireGuard。
    3. 真的射频后端：`shell/radio_bluez.go` 0%（293 条），`radio_iwd.go` 13.5%，`radio_hostapd.go` 0%，`hotspot_client.go` 6.9%：只在 QEMU 的 BlueZ、iwd 上跑。
    4. 网页视图和浏览器：`shell/webview.go` 11.0%（405 条没跑），`webbrowse.go` 5.2%，`apps/browser` 0%：开发机上没有 phone-webview（WPE，`make web-selftest`）。
    5. 像素渲染器那条路：Go 这边 `shell/gui.go` 52.9%、`frame.go` 50.5%（`make coverage` 不跑像素自测）；phone-paint 只量了它自己的测试：`main.rs` 17.7%（786 行），`input.rs` 34.2%，`kms.rs` 68.2%，`bench.rs`、`skin.rs`、`surfaces.rs` 0%。
    6. telephonyd 作为服务进程：`server.cpp`、`main.cpp` 0%，`loop.cpp` 43.8%：它的测试直接调库；自测跑的 phone-telephonyd 没带 `--coverage`，而且是被 SIGKILL 结束的，gcov 只在正常退出时写。ModemManager 后端 `mm.cpp` 开发机上没编（没有 libsystemd 的开发包），它的 D-Bus 测试（`make mm-selftest`）在容器里，没量。
    7. kario-jsrt（C，QuickJS-ng 上 TypeScript 应用的运行时，计算器和日历用它）：完全没量；`jsrt` 包的 90.5% 只是它 Go 那边的测试驱动。
    8. USB 共享：`usbshare/mtp/usbfs.go` 13.3%（FunctionFS，只在 QEMU 的 dummy_hcd 上），`shell/usbpc.go` 7.0%（电脑一侧的网络共享），`usbmtp.go` 53.1%；辅助进程 `programs/usbshared` 自己是 76.9%。
    9. phone-shell 自己的启动：`programs/shellcmd/main.go` 54.7%（273 条没跑：systemd 的 fd store、渲染器、`--services system`），`preview.go`、`pages.go` 0%（`make preview`），`shell/devbench.go` 45.6%。
    10. 命令行工具和探测应用：`cmd/phone-pkg` 0%（给包签名），`cmd/kario` 67.5%（`preview.go` 21.1%，`main.go` 16.7%，`run.go` 51.8%），phone-ota-delta 的 `main.go` 44.9%、`bundle_linux.go` 26.2%；兼容测试的探测应用 `cts/probe` 0%（`make coverage` 不跑 `make cts`）。
  - 单元测试里失败的，都和覆盖率无关，在改动之前的 `c653133` 上不带覆盖率照样失败（开发机，2026-11-23），**未解决**：`TestHostileApps/fds`（应用发 2000 条消息、每条带 100 个描述符，15 秒内没被处理完）时好时坏，单独跑 6 次失败 3 次（负载 3.5–4.7），每次不是 6.05 秒过就是 18 秒超时，像是卡住了而不是慢；`TestFamilyGrantWithAClockOff`（`asked: []`）21:25 还通过，23:09 起每次都失败，`TestScreenTimeCountsTheAppOnScreen`（离半夜只剩 4 分钟，10 分钟只算进 4 分钟）和 `TestLimitHoldsTheAppBack` 快到半夜时失败：都跟着墙上的钟。
- **`-race`**：`go test -race ./shell` 通过（开发机，2026-11-21：48 核 418 秒，4 核 436 秒）；CI 的 `race` 作业在自己的机器上把 Shell 包在 `-race` 下再跑一遍（[38](38-contributing.md) §2，在 GitHub 上**未验证**）。别的包在 `-race` 下还过不了（开发机，2026-11-21，除 Shell 外全部的包）：`apps/settings` 和 `jsrt` 的测试里有数据竞争（`apn_test.go`、`wellbeing_test.go`，`net_test.go` 的 WebSocket 服务端），`audio/opusfile`、`audio/voice` 用的 opus-go（从 C 转写的 Go）过不了 `-race` 自带的指针检查（checkptr）；所以 CI 只跑 Shell 包。原来过不了的几处：时区切换时 Shell 改写进程全局的 `time.Local`，和所有读时间的 goroutine 竞争（现在生效的时区放在 Shell 自己那里，`Core.zone`，读墙上时间的地方都带着它）；测试替身的数据竞争；Contacts 被误报「没有响应」，场景随后等不到联系人的详情页。这一处不是磁盘：测试里的 Shell 没像 phone-shell 那样映射编好的输入法词库（`ime.dict`），Contacts 第一次存联系人要给名字注音，就在 Shell 的循环里现编词库（开发机上 0.6 秒，`-race` 下 3 秒；写文件本身约 1 毫秒），Shell 按占用循环的限额（[22](22-developer-api.md) §8.1）让 Contacts 歇 4 倍的时间才读它的 `pong`，没有响应的检查把这段时间算成它没回。现在端到端测试映射词库，没有响应的检查不算 Shell 让它歇的时间，也不算 Shell 自己的循环停住的时间（[17](17-app-sandbox.md) §5.2）。查的时候还找出换机的一处真问题：旧手机发 `done` 后 Shell 马上关掉服务器，回答可能还没发出，旧手机收到的是断开（`switchrecv` 的 `TestDoneAnsweredBeforeTheServerCloses`）。还没改：落盘还在 Shell 的循环里做，磁盘慢时所有应用都等着，为谁写的那个应用还要多歇 4 倍的时间（真机上**未测**）；应用进程里 SDK 的 `setZone` 还改写 `time.Local`。起 Shell 的测试改成各在子进程里并行以后（上面 `go test ./shell` 那条）：252 秒（2026-11-26，48 核，负载 5–12），全过。改的时候 `-race` 还查出 `rtc_test.go` 的两处数据竞争，改之前的代码上也有：`keepRTC` 在新起的 goroutine 里才读写 RTC 的钩子，测试结束时把钩子换回去，两者竞争（现在起 goroutine 之前就读好）；测试替身在 Shell 的 goroutine 里改测试的切片（现在交给测试的 goroutine 去改）。`ota/delta` 的 `TestBigFilePatchMemory` 在 `-race` 下峰值内存超过它 24 MiB 的上限，改之前也一样（**未改**）。CI 的 `race` 作业（2026-11-26，4 核）在「基带迟迟不应答拨号」的用例里等不到「正在呼叫 153…」（400 ms），号码页上只有「5」。400 ms 是测试的数，要求是通话界面不等基带（[19](19-core-apps.md) §5，用例里基带 3 秒才答），4 倍放慢后的 1.6 秒照样分得出；可号码少了 1 不是等得不够：Shell 在 `-race` 下慢，电话应用为「1」的号码页取列表，Shell 晚答了，存着的键按墙上的钟 500 ms 就放了出去，落在旧的号码页上。现在存键的钟像「没有响应」那样不算 Shell 自己停住和让应用歇着的时间（[11](11-platform-architecture.md) 的「一次一个动作」），场景在 `-race` 下的放慢倍数是 4（`raceScale`）。开发机上 4 核跑整个场景复现不出（应用的请求快）；Shell 为「1」答电话应用的第一个请求时多占 150 ms 循环（随后按限额让它歇 600 ms）就复现：改之前 1 倍和 `-race` 下屏幕都和 CI 一样只剩「5」，改之后 `-race` 下通过，1 倍下号码不再丢，只是 400 ms 内等不到。开发机上（2026-11-27）`TestHostileApps/linger` 在 `-race` 下过不了，CI 的 `race` 作业却能过：四核上四个同时跑（`taskset -c 0-3`）8 次全挂，单独跑 3 次挂 1 次，另两次到 7.98 和 7.99 秒才结束（上限 8 秒）。假应用是 Shell 再起一遍的这个测试程序，每个都在打招呼之前在 `TestMain` 里编一份输入法词库：Shell 起应用不带测试的环境，也就没有 `KARIO_TEST_IME_DICT`。这在 `-race` 下要 3.1 秒（映射现成的词库 75 毫秒；不带 `-race` 0.56 秒对 19 毫秒），而 `linger` 在 `connGoneGrace` 之外只给 3 秒；`TestFakeApp` 以 `os.Exit` 结束，每个假应用还把 6.5 MB 的词库文件留在 `/tmp`（这台机器上有 4729 个）。现在假应用不编词库，应用本来就不读它（`TestFakeAppBuildsNoDictionary`，改之前的代码上失败）：`linger` 5.1 秒结束，次次通过，每次的时间减半（四核上四个同时跑 48 秒，原来 92 秒；单独跑 47 秒，原来 87 秒）。`TestHostileApps/flood` 看机器快慢：应用比每秒 250 条多出 500 条（`appMsgBurst`）以后限速才拦它，`-race` 下假应用每秒发约 700 条（四核上四个同时跑；单独跑 1000 条），一秒左右就拦；每秒不到 417 条就要过了攻击的 3 秒才拦，测试便一条也没看到被拦（四核上一轮 4 次里有 2 次，那时假应用还在编词库；两核上四个同时跑 8 次里 2 次）。现在它最多等 15 秒等限速拦下，像 `reqloop`、`fds` 等它们的数落定那样：两核上 8 次全过，最晚在发起 4.6 秒后才拦。
- **产线测试**：工站领设备证书的流程有了（[22](22-developer-api.md) §11），产线的硬件测试（按键、屏、声音、射频）随整机做，**未实现**。
- **入网和认证测试**（入网许可、CCC、SRRC）由有资质的整机厂做，不在本仓库。

## 8. Sanitizer 下的 kario-jsrt

`make jsrt-asan`、`make jsrt-tsan`（开发机，2026-11-27 ✓）把 kario-jsrt 连同 QuickJS-ng 带 AddressSanitizer 和 UBSan（`-fsanitize=address,undefined`）、带 ThreadSanitizer（`-fsanitize=thread`）编进 `build/jsrt-asan`、`build/jsrt-tsan`（`RelWithDebInfo`，`-fno-omit-frame-pointer`；从不覆盖 `build/jsrt/kario-jsrt`），然后对着它跑 `go test ./jsrt`（`KARIO_JSRT`，`KARIO_JSRT_REQUIRED=1`）：`jsrt` 包的每个测试起的运行时都在 sanitizer 下。编译器是第一个带这个 sanitizer 运行库的 GCC：开发机上 ASan 用 gcc-toolset-14，TSan 用 gcc-toolset-13（14 没带 libtsan）；Ubuntu 上是系统的 gcc。

**报告怎么进测试输出**（`jsrt/harness_test.go`）：

- 只有这两个目标设 `KARIO_JSRT_SANITIZER`，值是报告目录 `build/jsrt-<sanitizer>/reports`（每次先清空）。没设时测试不给任何 sanitizer 选项：`make test`、`go test ./jsrt` 照旧。
- 设了时，测试起的每个 kario-jsrt（经 Shell 的 `launchIn`、直接运行的 `jsrtCommand`、协议测试的）各拿一个报告前缀 `<测试名>.<序号>`，`log_path` 指向它，报告写进 `<前缀>.<pid>`，不进标准错误。只用 pid 分不开：应用作为自己 pid 命名空间里的第一个进程时是 1。进程结束后（Shell 的读循环收完尸；至多等 10 秒，等不到也算失败），起它的测试读这些文件，有一个就失败，把它整个打进测试输出。别的方式起的进程的报告（环境由 `TestMain` 设好）写成 `unclaimed.<pid>`，`TestMain` 在全部测试之后打出来，整次运行失败。
- 测试用完一个运行时（`launchIn` 和协议测试的清理）先关掉连接，等它自己退出，至多 5 秒（`leaveGrace`），然后才杀：拆运行时和泄漏检查都在退出时（下面），杀掉就都跳过了。等不到时测试输出里有一行 `did not leave within 5s of its connection closing: killed, without a leak check`，现在的测试一次也没有。没设 `KARIO_JSRT_SANITIZER` 时关掉连接就杀，和以前一样。
- 栈在进程里就符号化：GCC 的 sanitizer 运行库自己读调试信息（libbacktrace），不起外部的符号化程序。运行时装了原生模块以后的 seccomp 过滤器不许 execve，外部的 llvm-symbolizer 起不来，clang 的运行库要它，所以只用 GCC；普通构建的 seccomp 不变。kario-jsrt 和 QuickJS-ng 的帧带文件和行号（`strip_path_prefix` 去掉仓库所在的路径），libcurl、libc 的帧只有库名和偏移（没有它们的调试信息）。
- ASan 在第一个错误处结束进程，测试看到应用退出，外加报告；UBSan 和 TSan 报完接着跑，测试本身过了也因为有报告而失败（下面旧的 `body_cancel` 就是这样）。

**选项和原因**（`sanitizerOptions`）：

- 泄漏检查开着（ASan 的默认；以前的 `detect_leaks=0` 去掉了），没有 suppressions：现在的代码一份泄漏报告也没有，libcurl、OpenSSL、libc 也没有报出来的（它们到退出时还拿着的内存从它们的全局变量可达，不算泄漏）。
- `verify_asan_link_order=0`：存储的测试用 `LD_PRELOAD` 预载 `testdata/slowfs`（只包 `fsync`、`rename`），它排在 ASan 的运行库前面，ASan 默认拒绝启动。
- `atexit_sleep_ms=0`（TSan）：除主线程外还有线程时，TSan 默认在退出时睡 1 秒；SIGTERM 的测试要运行时 900 毫秒内退出。
- `TestNet` 洪水之后峰值内存的上限（96 MB）在 sanitizer 下不查：影子内存和隔离区也常驻，ASan 下 233 MB。

**退出时拆掉运行时**（`JSRT_TEARDOWN`，`jsrt/src/jsrt.h`）：带 ASan 或 TSan 编的 kario-jsrt（GCC 定义了 `__SANITIZE_ADDRESS__` 或 `__SANITIZE_THREAD__`；别的构建要拆可以加 `-DJSRT_TEARDOWN=1`）在退出时把自己拿着的都放掉，请 LeakSanitizer 查一遍（`__lsan_do_leak_check`，TSan 构建里没有它），然后 `_exit`。

- 为什么要拆才查得出：LeakSanitizer 只报不可达的内存。`main` 返回以后它的 `jsrt r` 不在栈上了，运行时、计时器表、应用目录都成了「泄漏」（以前打开检查时 7 个测试的 9 份报告都是这种：`phoneui_open` 的句柄、`main.c` 里 `strdup` 的应用目录、QuickJS 的 arena、原生模块的状态、计时器表、页面栈）；而 `_exit` 出去的（用过原生模块或网络的应用、SIGTERM）和被测试杀掉的根本不查。另一面，QuickJS 的对象少了一次 `JS_FreeValue` 也一直挂在运行时的对象表上，运行时在就可达；`JS_FreeRuntime` 以后漏掉的对象才不可达（`RelWithDebInfo` 带 `-DNDEBUG`，QuickJS 在这里检查对象表为空的断言不编进来），才报得出来。
- 每个出口都走 `main.c` 的 `end`：Shell 关了连接（主循环结束）、主模块失败（退出码 1）、原生模块或调试器起不来、SIGTERM。这种构建里 SIGTERM 不结束进程：处理函数写一个 eventfd，主循环在下一次 poll 时结束，然后和关了连接一样等写完、拆、查，退出码 0。
- 拆的顺序：没处理的拒绝和主模块的 promise；调试器（detach）；`kario` 模块拿着的事件处理函数、能力调用、计时器、页面栈、翻译、存储（`jsrt_kario_free`）；fetch 和 WebSocket（`jsrt_net_stop`：每个请求、每个连接都取消，放掉它们拿着的 JS 值，等取数线程和 WebSocket 线程结束，结束了的连接放掉对象，由它的 finalizer 随运行时释放）；原生模块（`jsrt_native_free`：还没开始的调用丢掉，等正在跑的，放掉模块的处理函数）；存储的写线程（`jsrt_store_free`：写完剩下的，join）；`JS_FreeContext`、`JS_FreeRuntime`；最后是请求和网络状态的 C 部分（`jsrt_net_free`：这时响应体的 finalizer 都跑过了）。
- 线程的规则不变（`jsrt_net_drain` 的）：取数线程还碰得到的请求不放，线程还在的 WebSocket 连接不放；原生模块的库不卸，它的线程可能一直在跑，模块的状态留着。等线程至多 200 毫秒（`JSRT_TEARDOWN_WAIT_MS`，从拆开始算，网络和原生模块共用）；到时还在的线程碰得到的都留着（可达，不算泄漏）。
- 最后 `_exit`，不跑 atexit 和析构函数：OpenSSL 在 atexit 里的清理、原生模块库的析构函数在还在跑的线程脚下不安全；LeakSanitizer 已经查过了。
- 只在 sanitizer 构建里拆：普通构建退出时什么也不放，内核收回内存，拆只会让退出变慢，还得等线程；设备上 SIGTERM 也照旧在写完以后马上 `_exit(0)`，一个停不下来的 turn 拖不住它。普通构建里也改了的只有几处错误路径上本该放掉的（`--compile` 失败、主模块失败时的报告、没有 `PHONE_UI_FD`、下面被拒绝的原生模块的导出表），存储的写线程从 detached 改成可以 join（它在普通构建里从不返回，没有区别）。
- 代价：SIGTERM 到进程退出，ASan 构建里 10–12 毫秒，以前 2–3 毫秒（`TestStorageSIGTERMFirstProcess` 的 nothing stored、written，各 3 次，没拿锁）。

**为 TSan 的一处改动**：`native.c` 的 `on_crash` 在 `raise` 之前解除对这个信号的屏蔽。TSan 包了 `sigaction`，处理函数运行时所有信号都被屏蔽，`raise` 出的 SIGABRT 一直挂着，进程从处理函数返回后以 134 退出，不是被 SIGABRT 杀死：`TestNativeCrashFirstProcess/abort` 在 TSan 下改之前失败，改之后通过。普通构建的处理函数用 `SA_NODEFER` 装、屏蔽集为空，这一步什么也不做。

**找得出什么**（开发机，2026-11-26、27，在临时副本里把修复倒回去或者故意弄坏）：

- 旧的 `jsrt_net_drain`：在锁里一次清掉整张完成表的 `posted`，取数线程随即可以把一个请求再挂上完成表，`settle` 却把它释放了，下一次 drain 写已释放的内存。`make jsrt-asan` 失败：`TestFetchAbortRace`（0.34 秒）的报告是 `heap-use-after-free ... WRITE of size 4`，`#0 jsrt_net_drain jsrt/src/net.c:974`、`#1 loop jsrt/src/main.c:400`、`#2 main jsrt/src/main.c:495`；释放在 `req_free net.c:728` ← `settle net.c:958` ← `jsrt_net_drain net.c:985`，分配在 `jsrt_fetch net.c:1246`。
- 旧的 `body_cancel`：不拿锁就写 `q->mode`。`make jsrt-tsan` 失败：`TestNet` 的检查都过了，失败只因为报告，`data race`：主线程在 `body_cancel jsrt/src/net.c:1073` 写（经 QuickJS 的帧回到 `jsrt_net_drain net.c:998`、`loop`、`main`），取数线程 T3 拿着锁在 `on_body jsrt/src/net.c:402` 读（libcurl 的 `Curl_client_write` 调它）。
- 少一次 `free`（2026-11-27，临时副本，拿着锁跑 `make jsrt-asan`）：去掉 `settle` 里整个读完的响应体（`text()`、`json()`、`arrayBuffer()`）的 `free(q->buf.s)`（`net.c:966`），`make jsrt-asan` 失败（111 秒）：`TestFetch`、`TestFetchWeb`、`TestFetchAbortRace`、`TestNet` 的 5 份报告都是 `LeakSanitizer: detected memory leaks`，分配的栈都是 `#0 realloc`、`#1 dstr_put jsrt/src/kario.c:43`、`#2 on_body jsrt/src/net.c:409`、`#3 Curl_client_write (libcurl.so.4+0x1c6d3)`，`TestFetch` 的是 `Direct leak of 16779264 byte(s) in 9 object(s)`。临时副本已经改回去。
- 打开检查后找到的一个真泄漏：原生模块的 `kario_native_init` 跑过、随后被拒绝时（`TestNativeRefused`），模块的导出表、导出名和字符串值没有放掉：`Direct leak of 1120 byte(s)`，`add_export jsrt/src/native.c:585` ← `api_export_number` ← `testdata/native/probe.c` 的 `init`，另有 21 处间接的。以前查不到：用了原生模块的进程 `_exit`。现在放掉了（`native.c`）：改之前 `make jsrt-asan` 在 `TestNativeRefused` 失败，改之后通过。
- 每个出口都查：另一个临时副本在 `main` 开头丢掉一块 4242 字节，`TestStorageSIGTERMFirstProcess`（应用是自己 pid 命名空间里的 1 号进程，SIGTERM）、`TestStorageDurable`（关连接、SIGTERM、主模块失败）、`TestNet`、`TestNative`、`TestDebugger`、`TestBudget`、`TestNotUnderShell` 起的 17 个进程，每个的报告里都有它。
- 现在的代码两个目标都通过，报告目录是空的；没有一份 UBSan 的报告。

**时间**（开发机，48 核，2026-11-27，拿着共用的锁量的，泄漏检查开着；冷构建是一共减去测试，含 cmake 和编译 Go 的测试程序；已经构建过时一共约是测试加 7 秒）：

| 运行 | 冷构建 | 测试 | 一共 |
|------|--------|------|------|
| `make jsrt-asan` | 约 2.5 分钟 | 105 秒 | 4 分 12 秒 |
| `make jsrt-tsan` | 约 1 分钟 | 102 秒 | 2 分 45 秒 |
| `go test ./jsrt`（对照，不带 sanitizer） | — | 90 秒 | — |

打开泄漏检查之前（2026-11-26）测试是 95、98、86 秒。多出来的是每个进程退出时的拆和检查，以及测试等进程自己退出（以前直接杀掉）。

**CI**：`jsrt-sanitize` 作业（[38](38-contributing.md) §2），ASan 和 TSan 各一台机器，和 `go` 作业同时跑；计划要跑 `jsrt` 包时就跑（改了 kario-jsrt 的 C 源码、`jsrt` 包的测试、它们读的 `sdk/ts/node`、`sdk/c/include`，构建文件，手动运行）。每个这样的 PR 都跑，不定时：

- 代价小：测试只比不带 sanitizer 多约 15 秒（这些测试的时间多半花在等待上，不在计算上；推断），多的主要是冷构建；两台机器和 `go` 作业同时跑，`go` 作业要构建和测试的多得多，整个 CI 不会变长。
- 在合并之前才有用：上面两个毛病在普通的测试里都过了，ASan 那个 1 秒内必现；定时跑的话，毛病合进去以后才看见，还得再找是哪个提交带进来的。
- `platform` 工作流只为 PR 和手动运行而跑，没有定时的触发；加上定时，计划会按「定时运行跑全部」把整个 CI 跑一遍。

GitHub 上还没跑过：**未验证**。作业先把 `vm.mmap_rnd_bits` 设成 28：内核的地址随机化开得更大时，sanitizer 的影子内存可能放不下。

**还没有的**：clang（要外部的符号化程序）、MemorySanitizer（整条依赖链都要带插桩，libcurl 没有）、arm64 上跑（只在 x86-64 的开发机上跑过）：**未实现**。泄漏检查查不到的：TSan 构建（没有 LeakSanitizer）；被信号结束的进程（原生模块崩溃的测试）；拆的时候 200 毫秒内没结束的线程拿着的；原生模块自己的内存（库不卸）；Node.js 跑的测试（不是 kario-jsrt）。sanitizer 构建里 SIGTERM 要等主循环回到 poll，一个停不下来的 turn、停在断点上的调试器、阻塞着的同步原生调用都让它不退出（普通构建不受影响）。

## 9. 和安卓按键机并排比（**未上真机**）

投资人会问：「同样的硬件装一个精简的安卓也能做这些，你们的优势只是屏幕小吗？」（投资人简报，66（内部文档）§7.1）回答要的是数，从一次公平的并排比较里来，分四条轴：只用按键操作、同样流畅要多少硬件、待机、长期更新。本节是做法。工具在 `tools/android-compare/`：两边各一个脚本跑各自的一半（`kario-half.sh`、`android-half.sh`）；`compare.py` 数视频的帧、读两边留下的文件、把各边的数排进同一张表（`results-template.csv`，每一行写着两边各从哪来）；`traffic.sh` 在待机时给两边发同样的消息；`tasks.play` 是 Kario 这边不用人的那一套操作。工具的测试在 `tools/android_compare_test.py`（`make sku-check` 里；假的 adb、套件的空跑，`compare.py frames` 对着 ffmpeg 画出的一段视频；开发机 ✓ 2026-11-26）；`tasks.play` 在开发机上经 `tools/perf/host.sh --play` 放完了（2026-11-26）。手机上的数一个都还没有。

### 9.1 四条轴和表里的行

| 轴 | 表里的行 | 看什么 |
|----|----------|--------|
| 只用按键 | `task.*.keys`、`task.*.s`、`task.*.touches`、`video.response_ms` | 七个任务（§9.4）各按几下、几秒；触摸必须是 0：安卓机上不碰屏做不完的，用触摸做完，记下碰了几下，时间照算 |
| 同样流畅要多少硬件 | `frames.*`、`play.*`、`start.*`、`mem.*`，加上 §9.6 的阶梯（每一级一列） | Kario 降到和安卓机同一档的核与内存以后，帧时间、启动还够不够 |
| 待机 | `standby.*` | 同样的网络、同样的消息流量下每天耗多少 mAh，按各自的电池能待几天，谁叫醒手机 |
| 长期更新 | `update.*` | 内核版本、安全补丁（或镜像）的日期、A/B 更新、厂商承诺多久、最近一次更新 |

数有两层。头一层是人用起来的感受：同一个人照 §9.4 在每台手机上做同样的任务，另一部手机以每秒 240 帧录下来，`compare.py frames` 从视频里数第一个键按下到画面定住隔了几帧；两边同一个定义、同一个工具，这一层可以直接比。第二层是各系统自己的计数（帧时间、启动、内存、待机时的唤醒），每边用自己的工具量，定义不完全一样，表的 `kario`、`android` 两栏写着各自的来源；这一层看数量级和趋势，不拿小数点比。

### 9.2 买什么

| 角色 | 手机 | 处理器 | 内存 | 系统 | 屏和键 | 电池 |
|------|------|--------|------|------|--------|------|
| Kario | 一加 6T（已有，[39](39-oneplus-6t.md)） | 骁龙 845：4×A75 2.8 GHz + 4×A55 1.77 GHz，Adreno 630 | 6 或 8 GB（`facts` 记下手头这台的） | Kario 整机镜像（[39](39-oneplus-6t.md) §13） | 6.41 英寸 1080×2340，没有键盘：蓝牙九宫格键盘（[39](39-oneplus-6t.md) §14.11） | 3700 mAh |
| 现在卖的安卓按键机 | 多亲 Qin J36 带摄像头的版本，约 ¥659（[IT之家](https://www.ithome.com/0/994/484.htm)） | 联发科 MT8766：4×A53 2.0 GHz | 3 GB，存储 32 GB | Android 13 | 2.8 英寸 480×640 触摸屏，九宫格键盘 | 2600 mAh |
| 同一硬件上的精简安卓 | 第二台一加 6T（二手） | 同 Kario 那台 | 同 Kario 那台 | LineageOS 22.2（Android 15，内核 4.9；[fajita 的页面](https://wiki.lineageos.org/devices/fajita/variant1/)），不装 GApps，微信、支付宝从官网下载 | 同一个蓝牙九宫格键盘 | 3700 mAh |

J36 回答「现在就有的精简安卓按键机」：同一个价位、同一类用户，它的 480×640 正是 Kario 的目标屏（`PHONE_PAINT_TARGET=480x640`，`play.*` 里 `cpu-target` 的那一次）。第二台 6T 一字一句地回答「同样的硬件装精简的安卓」。J36 的规格来自发布报道，到手后用 `android-half.sh facts` 核对（**待核实**）；买不到时换多亲 F22 Pro（联发科 Helio G85，同样**待核实**）。安卓那两台都要能用 `adb`（开发者选项里打开 USB 调试）；J36 让不让 `adb shell` 跑 `getevent`、`dumpsys`（有的厂商关掉了）到手先查（**待核实**）。

**要买的**：J36 带摄像头的版本；第二台一加 6T（二手，能解锁 bootloader）；蓝牙九宫格键盘（只有一个就两台 6T 轮流用）；一部能录每秒 240 帧的手机（很多中端手机的慢动作就是）和三脚架；一部装微信的发送机（任何能用 `adb` 的安卓手机，登第二个微信账号）；第二张同一运营商、同一套餐的 SIM 卡；一台有公网地址的云主机（用一两天，最小的配置就够，§9.7）；照度计或手机上的测光应用。

**哪些地方对谁有利**，写进结果，不藏起来：

- 6T 的硬件比 J36 强得多（大核、Adreno 630、UFS 2.1）：在第二台 6T 上它帮的是安卓（安卓用 GPU 画，Kario 默认用 CPU 画）；所以「要多少硬件」看 §9.6 的阶梯，不看 6T 对 J36。
- Kario 在 6T 上画整个 1080×2340 的面板，像素是 480×640 的 8 倍多：人做任务时的帧时间对 Kario 不利；`play.*` 另有一次只画 480×640。
- 6T 上用蓝牙键盘：每个键多一段蓝牙的延迟（对 Kario 和第二台 6T 一样不利；J36 用自己的键）。
- 6T 上的 Kario 没有真的相机（vivid 代替，[39](39-oneplus-6t.md) §14.16）：相机和扫码只比按键数和到取景框出来的时间（不用起传感器，对 Kario 有利）；对准付款码到出付款页只有安卓量得到（`task.pay.confirm_s`）。
- 鸿雁、飞钱是 Kario 的演示应用，比微信、支付宝小得多：它们的冷启动和内存对 Kario 有利，表里照列，结论里只拿系统自带的应用（电话、短信、通讯录、相机）比启动。

### 9.3 公平的规则

- **同样的网络**：两张同一运营商、同一套餐的 SIM 卡，在同一个地方测；待机分两轮（§9.7），第二轮两张卡互换。记下每台状态栏的信号格数。
- **同样的联系人**：`compare.py vcf --zhangsan <号码> --lisi <号码>` 写一份 202 人的名片（张三、李四和 200 个常见的名字，同姓的多，搜索才有难度），每台都导入；张三的号码填发送机的号码，拨号和短信才真的打得通、发得到。Kario：放进「文件」，通讯录 › 选项 › 从文件导入；安卓：`adb push contacts.vcf /sdcard/Download/`，在通讯录里导入。
- **同样的消息流量**：第二个账号从发送机以固定的间隔同时给两台发同一条（`traffic.sh`，默认 15 分钟一条，24 小时 96 条）；Kario 的鸿雁经云主机上的模拟服务器收（§9.7），安卓的微信经微信自己的服务器收。
- **同样的亮度**：关掉自动亮度，屏幕中央一样亮（照度计或测光应用贴着白色的页面量，差 10% 以内），记下各自设的档。
- **同样的开头**：每一轮都先重启，开机后静置 10 分钟再测（`android-half.sh idle` 等满这 10 分钟才记内存；`kario-half.sh mem-idle` 先问重启、静置过没有）；两边的自动更新都关掉；蓝牙只在要用键盘的任务里开；定位关掉。
- **安卓机的样子**：恢复出厂设置以后只加装微信和支付宝，都登录、通知打开，电池优化保持默认（和普通用户一样，不给微信加白名单）；厂商自带的应用不卸，`facts` 记下各角色是哪个应用。
- **同一个人、同样的遍数**：每个任务每台 5 遍（`--reps`），先在每台上各练 3 遍；两台交替做（Kario、安卓、Kario……），抵消越做越熟；报中位数。
- **各用最快的按法**：每台用它自己最少的键做完（先找出来），不照抄另一台的路径；表里写的是按了几下，不是规定按几下。

### 9.4 七个任务

| 任务 | 开始 | 结束（定住的那一帧） | Kario 上的按法（参考） | 安卓上 |
|------|------|----------------------|------------------------|--------|
| 拨号 | 待机界面亮着 | 通话界面出来，写着张三 | `9` `4`（张三排第一）、`↓`、通话键 | 拨号盘上打张三的数字（智能拨号）、选中、拨出；J36 的拨号盘是不是按拼音首字母搜，**待核实** |
| 短信 | 待机界面 | 发给张三的「我到了」显示已发出 | 主菜单、短信、`F2` 新建；收件人打 `94` 选张三；正文 `96` OK `326` OK `53` OK（九宫格拼音）；发送 | 短信里新建、选张三、九宫格输入法打「我到了」、发送 |
| 回消息 | 待机界面，李四刚发来的一条在通知里（`traffic.sh --count 1`） | 回给李四的「好的」显示已发出 | 主菜单、鸿雁、第一个会话 OK、`42633` OK、OK 发出 | 微信里李四的会话、打「好的」、发送；只用按键做不完时用触摸做完，记下碰了几下 |
| 扫码付款 | 待机界面，打印的付款码放在固定的地方 | 扫码的取景框出来（两边比这一帧）；安卓再量到「付款给……」那一页（`task.pay.confirm_s`），不付，返回 | 主菜单、飞钱、扫码 | 支付宝首页的「扫一扫」，扫机主自己的收款码 |
| 相机 | 待机界面 | 取景框里有活的画面 | 主菜单、相机 | 相机（J36 有没有相机键，**待核实**） |
| 滚动 | 通讯录的列表停在第一个人 | 往下 30 个人再回到第一个，画面定住 | `↓` 30 下、`↑` 30 下 | 同样往下 30 个再回来 |
| 切换 | 短信、聊天、通讯录按这个顺序打开过，通讯录在前面 | 第三次切回来的应用画面定住 | 三次：`F3`（最近用过的应用）、`↓` `↓`、OK | 最近任务键（J36 上是哪个键，**待核实**；没有就从主屏重新打开） |

付款码：飞钱演示认的码是 `PAY|M10086|晨光咖啡|1800`（`qrencode -o feiqian.png 'PAY|M10086|晨光咖啡|1800'`），和支付宝的收款码打印成一样大，贴在离手机一样远、一样亮的地方；6T 上的 Kario 扫不到它（没有相机），印着留给有相机的 Kario 手机。拨号和短信打到发送机上，打通、收到以后挂断、删掉，不算在任务里。按法一栏是 Kario 这边找到的最少的键（`tasks.play` 放的就是这些），手机上以实际按的为准。

### 9.5 每个数怎么取

| 数 | Kario | 安卓 | 注意 |
|----|-------|------|------|
| 每个任务的键数、触摸数 | `kario-half.sh task 名字`：任务前后各一次 `dump`（[24](24-sku-framework.md) §6.2），渲染器的输入计数之差：键是交给 Shell 的输入减去触摸，触摸是画面答了的触摸 | `android-half.sh task 名字`：做任务时开着 `getevent -lt`，数键按下（不算触摸屏的 `BTN_TOUCH`）和手指落下 | 两边都数真的按下；视频里也数得出来，对一遍 |
| 每个任务的时间 | 240 帧的视频：`compare.py frames` | 同左 | 从第一个键按下的那一帧到画面定住（之后 300 毫秒没有变化）的那一帧；按下的帧由人在播放器里逐帧找（`--key` 先给出候选），画面由工具量 |
| 按键到画面 | 视频的 `video.response_ms`；渲染器自己量的 `keys.p50`、`keys.p95`（内核的事件到翻页） | 视频的 `video.response_ms`；不 root 没有同样的数 | 跨系统只比视频的 |
| 帧时间、掉帧 | 渲染器的 `frame_ms`（画一帧加交出去），掉帧是动效里晚了的翻页占动着的翻页的比例；人做任务时的取自最后一次 `dump`，`tasks.play` 的是 perfgate 的数（`play.*`，[39](39-oneplus-6t.md) §9.1） | `dumpsys gfxinfo <包> framestats`：每个任务前 `reset`，任务后各应用（连桌面和系统界面）的直方图相加，掉帧是 Janky frames 占 Total frames rendered；`--perfetto` 另录一份 Perfetto（`perfetto.cfg`），在 [ui.perfetto.dev](https://ui.perfetto.dev) 看 | 定义不同（安卓的是 IntendedVsync 到 FrameCompleted），都对着 60 Hz 的 16.7 毫秒 |
| 冷启动 | 渲染器记的进程启动到第一页上屏（`tasks.play` 里每个应用都是冷的） | `am start -W` 的 TotalTime，先 `am force-stop` | 演示应用那两行不拿来下结论（§9.2） |
| 热启动 | 视频：按下打开的键到页面定住 | `am start -W`（先按主页键）；也录视频 | 跨系统比视频的 |
| 内存 | `/proc/meminfo` 的 MemTotal − MemAvailable：重启静置以后（`mem-idle`）和开着切换任务的三个应用时（`mem-apps`）；套件的 `mem` 另给各进程 PSS 之和；`dump` 给出三个应用还在不在、因内存结束过几个应用 | 同样的 `/proc/meminfo`；`dumpsys meminfo` 的 used pss；三个应用的进程还在不在 | 比 MemTotal − MemAvailable；PSS 两边算进去的进程不一样，只作参考 |
| 待机 | 套件的 `power-start`、`power-stop`：电量、电量计算出的平均电流、挂起次数、`suspend_stats`、Shell 每次醒来的原因（`sleep: woke after …`，按原因数） | `dumpsys batterystats --reset`，之后的 `batterystats`：Discharge（mAh）、各个 Wakeup reason；`dumpsys battery` 的电量和 Charge counter | 每天的 mAh 和按电池容量换算的天数；唤醒的原因两边叫法不同，只比次数和最多的那个 |
| 更新 | 套件的 `facts`（内核、`os-release`）；镜像的日期、A/B（[39](39-oneplus-6t.md) §14.13）手填 | `uname -r`、`ro.build.version.security_patch`、`ro.build.ab_update`；厂商的承诺、最近一次更新手填（设置 › 系统更新，厂商的页面） | Kario 用主线内核（[39](39-oneplus-6t.md) §12），LineageOS 22.2 在 6T 上是 4.9 |

手填的数用 `compare.py set hand-<列名>.csv <行> <值> --source <从哪来>` 记进各边目录里的 `hand-<列名>.csv`，`table` 时和量到的放在一起。

### 9.6 同样流畅要多少硬件：Kario 的阶梯

把 6T 上的 Kario 降到 J36 那一档：只开 4 个小核（A55 1.77 GHz，J36 是 A53 2.0 GHz），先留 3 GB、再留 2 GB 内存，每一级重跑 `tasks.play`（`kario-half.sh perf`）和七个任务，每一级是表里的一列（`--side kario-3g`、`--side kario-2g`）。比的是 `play.*`、`frames.*`、`start.*`、`task.*.s` 对 J36 那一列：在这一档上不比 J36 差，「要多少硬件」的回答就是「不比现在卖的安卓按键机多」；再降一级还过得去，就是更少。阶梯里内存的那几行不看（占住的内存也算在用掉的里）。

运行时降（先试这个；重启、静置以后在 USB 控制台里做，**未验证**）：

```
for c in 4 5 6 7; do echo 0 > /sys/devices/system/cpu/cpu$c/online; done
keep=3072                                           # 留给系统的 MB：这一级 3072，下一级 2048
total=$(awk '/MemTotal/ {print int($2 / 1024)}' /proc/meminfo)
mkdir -p /run/hold && mount -t tmpfs -o size=$((total - keep))m hold /run/hold
dd if=/dev/zero of=/run/hold/fill bs=1M count=$((total - keep))
```

Kario 没有交换区，tmpfs 里的页收不回来，等于把这些内存拿走了；做完 `umount /run/hold`，重启让大核回来。大核下不了线（内核不支持 CPU 热插拔）时改开机参数：照 [39](39-oneplus-6t.md) §8.2 的办法用 `unpack_bootimg`、`mkbootimg` 重打启动镜像，内核和 ramdisk 用 Kario 自己的，命令行在原来的后面加 `maxcpus=4 mem=3G`（或 `mem=2G`），`fastboot boot` 它（只在内存里跑，不写手机；`mem=` 截掉的那段里有没有基带要的保留内存**未核实**，起不来就只用运行时的办法）。同样的限制也能加在第二台 6T 的 LineageOS 上（`adb root`，开发者选项里的 Rooted debugging）；它有 zram，占内存要写随机数（`/dev/urandom`），**未验证**。

### 9.7 待机 24 小时

分两轮，每轮 24 小时，两台并排同时测：第一轮 Kario 的 6T 和 J36，第二轮 Kario 的 6T 和 LineageOS 的 6T（微信账号 A 换到它上面：一个微信账号同时只能登一台手机）。屏幕关、Wi-Fi 关、蓝牙关，插着 SIM 卡、开着移动数据，聊天应用登录着。Kario 要整机（`--whole`：chroot 里是 Nura 管电源）。

Kario 的鸿雁默认连的是手机自己身上的模拟服务器和推送网关（`127.0.0.1`），消息不走射频，那样对安卓不公平。所以这两样搬到一台云主机上，手机经移动网络连它，和微信连自己的服务器一样。云主机的公网 IPv4 要在它自己的网卡上（网关第一次启动时自己签的证书只写它监听的地址和 `localhost`；公网地址靠 NAT 转进来的云主机不行）：

```
GOOS=linux GOARCH=amd64 go build -o cloud/ ./cmd/phone-pushgw ./cmd/phone-chatsrv   # 在电脑上编，拷到云主机
H=203.0.113.7                                                                     # 云主机的公网地址
./phone-pushgw --listen $H:8790 --tls-dir ~/pushgw-tls --tlcp --state ~/pushgw.state &
./phone-chatsrv --listen $H:8791 --gateway https://$H:8790 --gateway-cert ~/pushgw-tls/cert.pem &
```

把云主机上的 `~/pushgw-tls/cert.pem` 拷到手机的 `/var/lib/kario-test/cloud-pushgw.pem`，然后在手机的 USB 控制台里（drop-in 里的 `EnvironmentFile=` 排在产品层的 `services.env` 后面，盖过它）：

```
H=203.0.113.7
printf 'PHONE_PUSH_GATEWAY=https://%s:8790\nPHONE_PUSH_GATEWAY_CERT=/var/lib/kario-test/cloud-pushgw.pem\nPHONE_CHAT_SERVER=http://%s:8791\n' $H $H > /etc/kario-compare.env
for u in phone-pushd phone-display; do mkdir -p /etc/systemd/system/$u.service.d; printf '[Service]\nEnvironmentFile=/etc/kario-compare.env\n' > /etc/systemd/system/$u.service.d/compare.conf; done
systemctl daemon-reload && systemctl restart phone-pushd phone-display
```

打开一次鸿雁（它用新的推送令牌登录云主机上的服务器），`traffic.sh --chat http://$H:8791 --count 1` 发一条，手机上应该出通知。模拟服务器没有认证（演示用的）：知道地址的谁都能发，只在测试的这两天开着，测完就停，上面不放真的数据；测完删掉两个 drop-in 和 `/etc/kario-compare.env`，`systemctl daemon-reload`，重启这两个服务。推送的心跳是学出来的（从 30 秒起，一条连接撑过 3 次就加倍，最长 28 分钟，[21](21-push.md) §2），微信也有自己的心跳：两台都先在这个网络上连满 3 小时，再重启、静置 10 分钟，然后开始（学到的间隔重启后还在）。

一轮的做法：两台都插着电脑时开始，开始后马上拔线、熄屏，屏幕朝下并排放着；发送机一直插着电脑，微信停在和账号 A 的会话里，输入框有焦点，屏幕常亮（`adb shell svc power stayon usb`）；`--tap` 是它发送按钮的位置（开发者选项里的「指针位置」量），不给就按回车发送；`input text` 只打得出 ASCII，所以消息是 `cmp-<序号>-<时分秒>`。白天抽查五次：消息发出（`traffic.log` 的时刻）到每台出通知（亮屏、响或亮灯）几秒，记进各边的 `standby.delivery_s`；24 小时后看两边的会话里 96 条是不是都到了，缺的写进结果。

### 9.8 一次完整的做法

1. 准备（一次）：第一台 6T 刷 Kario 的整机镜像（[39](39-oneplus-6t.md) §13）；第二台 6T 照 LineageOS 的安装说明刷 22.2，装微信、支付宝；J36 恢复出厂设置，装微信、支付宝；三台导入同一份联系人（§9.3）；打印两个付款码（§9.4）；架好 240 帧的手机：三脚架正对屏幕和键盘，两样都在画面里，灯不闪（交流供电的 LED 灯在 240 帧下会闪，用直流的灯或日光）。
2. 按下面的命令跑，每台一列：`kario-half.sh` 默认跑 `facts`、`perf`（`tasks.play`，480×640 和整个面板各一次）、`mem-idle` 和 `table`；`task`、`tasks` 每一遍先说开始，人做完、按回车；`android-half.sh` 的 `facts` 列出各角色找到的应用，不对就用 `--pkg 角色=包` 改（LineageOS 的相机是 `org.lineageos.aperture`、短信是 `com.android.messaging`，**待核实**）。
3. 每一遍的视频用 `compare.py frames` 量，`--screen` 是视频里屏幕的那一块（x、y、宽、高，像素），`--key` 是键盘的那一块（给出按下的候选帧），找好按下的帧以后用 `--marks` 量，`--task` 和 `--into` 把时间记进那一边的 `hand-<列名>.csv`。
4. 待机照 §9.7；更新那几行手填。
5. 任何一边的 `table` 都把 `build/android-compare/` 下所有列排进 `table.md`；`compare.py table … --csv` 另存一份 CSV。

```
K="tools/android-compare/kario-half.sh --sku oneplus-fajita-t9 --whole"
$K                                                    # facts、tasks.play、重启静置后的内存、表
$K tasks mem-apps table                               # 七个任务各 5 遍（录像），开着三个应用的内存
A="tools/android-compare/android-half.sh --side j36"
$A idle facts start                                   # 重启、静置 10 分钟、内存；手机的情况；冷热启动各 5 遍
$A --perfetto tasks mem table
C=tools/android-compare/compare.py
$C frames dial-j36-1.mp4 --screen 210,380,300,400 --key 200,800,320,420
$C frames dial-j36-1.mp4 --screen 210,380,300,400 --marks 118,171,240,302 --task dial --into build/android-compare/j36/hand-j36.csv
$C set build/android-compare/j36/hand-j36.csv update.promise "没有公开的承诺" --source "厂商的页面"
$K standby-start                                      # 待机：然后拔线、熄屏，§9.7
$A standby-start
tools/android-compare/traffic.sh --chat http://203.0.113.7:8791 --sender <发送机> --log build/android-compare/traffic-1.log
$K standby-stop table                                 # 24 小时以后，插回电脑
$A standby-stop table
```
