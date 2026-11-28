# 续航：屏幕、通知灯、不必要的唤醒

> 状态：**原型 ✓（开发机、QEMU）**。息屏、按键唤醒、通知灯慢闪、看过后熄灭、来电闹钟和充电亮屏在自测里通过；模拟器上的通知灯是用户态 LED，内核的定时器触发器按 5 秒的周期让它闪（自测量了周期）。真机上的功耗没有测过，**要等骁龙板子**（R16）。

## 0. 结论

- 屏幕是手机最大的耗电，所以：没有按键就息屏（默认 30 秒，设置里 15 秒到 5 分钟），最后 5 秒先调暗；息屏时渲染器关掉面板，Shell 不再发帧。
- 新短信、新消息、漏接电话**不点亮屏幕**：主板配了通知灯时，它慢闪（5 秒亮 0.2 秒），直到用户在通知中心或对应的应用里看过。
- 软件本身少醒：息屏后 Shell 的例行检查从每秒两次放慢到 5 秒一次；画帧改成有变化才画，空闲的手机不会每秒醒 60 次。
- 哪块板有什么灯、什么背光，写在 SKU 的主板配置里（[24](24-sku-framework.md)），代码经 `hal/` 驱动（[11](11-platform-architecture.md) §「那 hal/ 是什么」）。

## 1. 屏幕

| 情况 | 屏幕 |
|------|------|
| 超时没有按键 | 先调暗到 20%（有背光设备时），键盘背光同时关掉，再息屏；调暗、熄屏、亮屏和换亮度都是渐变，不跳（[37](37-motion.md) §1，**未上真机**） |
| 键盘背光 | 有的话跟着屏幕亮灭，亮度和屏幕一样（设置 › 显示与亮度）；调暗时先关。主板配置的 `keyboard_backlight` 给它的 LED 名字，不给时用第一个叫 `<设备>::kbd_backlight` 的 LED（内核的命名惯例）；`hal/backlight` 的单元测试和 Shell 的测试 ✓，**未上真机** |
| 自动亮度 | 设置 › 显示与亮度 › 自动亮度（默认关）：背光跟着环境光传感器（`hal/sensor`，IIO），设置的亮度是普通室内（300 勒克斯）时的亮度，暗处降到三成、日光下升到两倍半，在 10% 到 100% 之间。读数按对数平滑，变亮快（约 3 秒）、变暗慢（约 7 秒）；光线在原来的四分之一以内变动不调，免得手挡一下就闪；日光下（1000 勒克斯以上，回到 600 以下才恢复）键盘背光关掉，开不开自动亮度都这样。主板配置的 `light_sensor` 给传感器的 IIO 名字，不给时用第一个测光照的 IIO 设备；没有传感器的机器上是模拟的（设置页写明）。单元测试和自测 ✓，**未上真机** |
| 屏幕上有视频、相机取景、视频通话，或者正在响铃 | 不息屏 |
| 息屏时按任意键 | 点亮；**这一下只点亮**，不做别的（口袋里误按不会打开菜单） |
| 来电、闹钟、插上电源、电脑要求信任 | 自己亮起；插着电源时状态栏的电池旁有一道闪电，没充满时电池从电量处一格格充满、再从头来，待机界面在日期下面另画一个大电池和百分比，一起动（phone-paint 自己计时，只重画电池那一小块，[37](37-motion.md) §3；**未上真机**） |
| 新短信、新消息、漏接电话 | 有通知灯时不亮，通知灯慢闪；没有通知灯的机型点亮屏幕，之后照常超时。通知照常进通知中心、照常响提示音 |

息屏时：

- Shell 告诉渲染器关掉面板（协议的 `power`，[23](23-renderer-protocol.md)）：phone-paint 关掉显示控制器的扫描输出（CRTC），面板驱动随之给面板和背光断电；亮屏时显示关着期间最后一帧，背光等面板开了再亮（§7）。
- Shell 不画帧也不发帧；例行检查（冻结后台应用、内存、闹钟、任务、USB）5 秒一次，闹钟最多晚 5 秒响。
- 键盘锁照旧由自己的设置决定（设置 › 键盘锁），和息屏是两件事。

插上电源（`hal/power`、`shell/screen.go` 的 `checkCharger`）：

- **算不算插着电**：看充电器（Mains、USB 类的 power_supply）的 `online`；电池自己的 `status` 只在机器上没有一个充电器报 `online` 时才看，也只认 `Charging`。电量计在 100% 拔掉电源以后照样报 `Full`（6T 的 bq27411），以前 `Full` 也算插着电，6T 充满以后拔插充电器屏幕都不亮（[39](39-oneplus-6t.md) §16.11）。快充照旧看 `usb_type`（`[PD]`、`[PD_PPS]`、`[DCP]` 这类，或者 `USB_PD` 类的电源）。
- **多快发现**：内核说电源变了（power_supply 的 uevent，插上、拔掉充电器时充电器驱动发）Shell 马上查一次；收不到 uevent 时跟着例行检查（亮屏 0.5 秒、息屏 5 秒一次）。挂起时：6T 的 PMI8998 充电器驱动把插线中断（`usb-plugin`）设成唤醒中断，插上充电器应该叫醒手机，醒来时 uevent 让 Shell 马上亮屏（**未上真机**，[39](39-oneplus-6t.md) §14.10）。
- **提示等充电器认出来**：PMI8998 插上以后要几百毫秒才知道是哪种充电器，这期间 `usb_type` 答 `EAGAIN`（`Power.Detecting`）；屏幕先亮，「正在充电」或「正在快速充电」等认出来再出，最多等 3 秒。

## 2. 通知灯

| 部分 | 做法 |
|------|------|
| 什么时候闪 | 屏幕关着，并且通知中心里有用户还没看过的通知 |
| 什么算看过 | 打开通知中心（全部算看过），或者那个应用到了前台（它的算看过）；应用在前台时它自己的新通知直接算看过 |
| 怎么闪 | `hal/led` 用 LED 类的定时器触发器：`trigger` 写 `timer`，`delay_on` 200、`delay_off` 4800（毫秒），亮度用最大值（短而亮比长而暗省电）。驱动能自己闪的（手机 PMIC 的 LED，比如高通的 LPG）由硬件闪，CPU 不醒；不能的由内核定时器切换，也没有用户态进程参与 |
| 多色灯 | 主板配置的 `notify_color` 给每个通道的强度（F(x)tec Pro1 X 的 `rgb:status`：绿色） |
| 没有灯的板 | 不配 `[leds]`（一加 N100）：息屏时来了没看过的通知就点亮屏幕，之后照常超时息屏 |
| 模拟器 | 内核打开 uleds（`CONFIG_LEDS_USER`），Shell 发现板上该有的灯不存在时自己造一个同名的用户态 LED；定时器触发器照样工作，Shell 读得到内核每次点亮、熄灭它的时刻 |
| 开发机 | 内存里的模拟灯，自测检查它的状态 |

## 3. 软件少醒

| 以前 | 现在 |
|------|------|
| 设备上的 Shell 每 16 毫秒醒一次，看要不要画帧 | 有事件（按键、应用消息、媒体帧、例行检查）后 16 毫秒画一帧，没有就不醒；开机动画期间照旧按帧画 |
| 例行检查固定每 0.5 秒 | 亮屏 0.5 秒，息屏 5 秒 |
| 后台应用 | 已有：离开前台就冻结（cgroup 冻结器），推送和任务按预算统一唤醒（[21](21-push.md)、[22](22-developer-api.md)） |
| 后台应用用了多少 CPU，用户不知道 | Shell 每 30 秒读一次每个应用的 CPU（cgroup 的 `cpu.stat`，没有 cgroup 时读 `/proc/<pid>/stat`），分前台、后台记一天。一个应用不在通话、没在放音乐，却在后台 10 分钟里平均用掉一个核的 5% 以上，就发一条系统通知点名它（一天一次），通知上的「停止」（`s` 键）结束它；设置 › 电池显示电量，并按后台用量列出各应用最近一天的后台、前台 CPU（`settings.get` 的 `energy`） |
| 息屏后整机还醒着 | 息屏 30 秒后，没有通话、没在放音、没在装更新、USB 上没有电脑、也没有应用被推送或任务唤醒着，整机就挂起：板子有挂起到内存（`/sys/power/state` 有 `mem`）就用它，没有就挂起到空闲（`freeze`）。挂起前把 RTC 的唤醒闹钟设在下一个闹钟、定时通知（`notify.schedule`，[22](22-developer-api.md) §4.4）或任务截止前 3 秒，最迟 15 分钟后（维护唤醒：只叫醒 CPU，不叫醒射频，心跳不搭它，§8）；基带的来电、短信和按键随时叫醒它（6T 上基带的 GLINK 通道默认不是唤醒源，`phone-qcom-wake` 的 udev 规则打开它，**未上真机**，[39](39-oneplus-6t.md) §16.12）。醒来后先留 10 秒给唤醒带来的事（一条推送、一条短信）。决定挂起前先读内核的 `wakeup_count`，挂起时写回去：其间来了唤醒事件（按了一下键），内核拒绝挂起，事件不会丢（`hal/pm`、`shell/sleep.go`）。别人持着唤醒锁（`/sys/power/wake_lock`，bring-up 镜像的 `phone-bringup`）时内核不挂起、读这个计数也一直等，Shell 就不去睡，醒着的原因写成 `wake lock …`（[39](39-oneplus-6t.md) §16.10）。醒着的原因一变，日志记一行 `sleep: awake (…)`（屏幕亮着、刚熄屏、刚醒、马上有事这几种几秒就过去的不记），一夜没睡的日志说得出为什么；醒来记 `sleep: woke after …s (irq N; wake-up sources …)`，除了 `pm_wakeup_irq`，还写挂起期间 `/sys/class/wakeup` 里 `wakeup_count` 涨了的唤醒源（闹钟、基带、充电器分得开）。bring-up 镜像的 `phone-bringup` 只在电脑配置着 USB 控制台时持着（2026-11-26 以前一直持着，6T 一夜没电，[39](39-oneplus-6t.md) §16.12；**未上真机**）。Shell 的时钟改用墙上时间，睡着的时间也算进闹钟和任务的等待里（Go 的单调时钟在挂起时停着）。只在主板配置说能从挂起里叫醒的手机上启用（`sku.json` 的 `suspend`：真机的主板默认能，模拟器的主板不能），开发机上永远不会挂起；能挂起的板子上没有能唤醒系统的按键时（输入设备的 `power/wakeup` 都不是 `enabled`）也不挂起，否则用户叫不醒它 |

翻盖改两处（[24](24-sku-framework.md) §9.10.10 的预算要的，`shell/sleep.go`、`shell/outer.go`、`shell/lid.go`；单元测试 ✓，旧代码失败；**未上真机**）：

- **外屏的分钟唤醒**：SKU 的外屏睡着也保持画面（`outer.kind` 是 `memory-lcd` 或 `e-paper`）时，RTC 在每分钟的第 1 秒叫醒手机（RTC 从自己的相位数整秒，最多早响 1 秒，这样也响在新的一分钟里）；这次唤醒把外屏的时间改成新的一分钟，马上再挂起，不留 10 秒，也不跑例行检查。它要是碰上别的事（13 秒内到期的事、通话、醒着的应用、别人的唤醒锁），或者手机在那一分钟前 1 秒以上就醒了（按键、基带），就是一次普通的唤醒，照留 10 秒。维护唤醒照旧在例行检查上次跑过 15 分钟后来，中间的分钟唤醒不推迟它。日志不给每次分钟唤醒记一行：下一次记下的唤醒后面写 `; N short wakes for the outer display's clock before it`。LCD、OLED 的外屏睡着不显示，没有分钟唤醒。
- **合着盖只有一种周期唤醒**：SKU 有盖子、盖子合着时，维护唤醒等推送的下一次心跳（网关发来的那一行反正要叫醒手机，最长 28 分钟，[21](21-push.md) §2），RTC 定在心跳后 10 秒，防它来晚；推送连接靠不上时（断着、还没听到心跳、心跳过了时候）照旧 15 分钟。闹钟、定时通知、任务截止、应用自己连接的心跳照样到时叫醒，什么都不会过期限；多等的只是例行检查里周期性的活（重试、每天的更新检查），从最多 15 分钟变成最多约 28 分钟。
- 盖子的位置由渲染器报（[24](24-sku-framework.md) §9.10.8，2026-11-27，**未上真机**），不报盖子的渲染器下盖子算开着，维护唤醒还是 15 分钟；外屏还没人画（[24](24-sku-framework.md) §9.10.11，**未实现**），分钟唤醒只更新 Shell 记的那份时间。

## 4. 设置

设置 › 键盘锁：上面是「自动锁定」，下面是「息屏」（15 秒、30 秒、1 分钟、2 分钟、5 分钟，默认 30 秒）。设置项 `screen_off`（秒，0 是默认值）。

## 5. 测试

| 用例 | 测什么 |
|------|--------|
| 没有通知灯的机型 | 去掉通知灯，息屏时来短信，屏幕亮起；再过超时又息屏 |
| 挂起 | 设一个 2 分钟后的闹钟，打开挂起，息屏 30 秒后整机挂起，RTC 叫醒它，闹钟照常响（定时通知的唤醒时间由 Shell 的单元测试 `TestScheduledFiresWhileFrozen` 查，挂起的自测里没有它）。开发机和模拟器上都是模拟的挂起（把 Shell 的时钟拨到唤醒时间）：模拟器真挂起到空闲后叫不醒（它的 RTC，pl031，没给挂起到空闲设唤醒中断；虚拟键盘也不是唤醒源），所以那里默认不挂起；真的挂起和唤醒**要在真机上验证** |
| 后台耗电 | 计算器在后台被记上 40 秒 CPU（`cpu-charge`），系统通知点名它，在通知里按「停止」结束它 |
| 设置 › 电池 | 上一条之后打开电池页，计算器列在里面，后台 40 秒 |
| 应用自己的长连接（[21](21-push.md) §8） | 设置 › 电池的「自己的连接」里有它：系统为它醒 1 次、约 0.9 mAh；`push` 包的单元测试 `TestBatchedHeartbeats`、`TestPowerTable` 核对 §8 的表 |
| 屏幕省电 | 打开超时，时间快进 31 秒后息屏；息屏时来短信，屏幕不亮，灯慢闪；按一下键只点亮（还在待机界面）；再息屏灯又闪；打开通知中心看过后再息屏，灯不闪 |
| 通知灯（模拟器） | 用户态 LED：内核的定时器触发器按 5 秒的周期（上下差五分之一以内）点亮了至少 2 次 |
| 充电 | 息屏时插上 PD 充电器，屏幕亮起，提示「正在快速充电」；像素渲染器的截图 `78-charging`：待机界面上充电的电池（快照里停在电量处）、状态栏的闪电 |
| `hal/power` 单元测试，Shell 的 `TestPluggingInAtAFullBatteryLightsTheScreen`、`TestTheHousekeepingGoesOnWhileTheChargerIsDetected`、`TestAChargerUeventChecksTheChargerAtOnce`（假的 sysfs 目录） | 6T 那样电池 `Full`、充电器离线算没插电，插回去屏幕亮、出提示；充电器在线时不管电池说什么都算插着；USB 充电、笔记本的 Mains、没有充电器只有电池（只认 `Charging`）；`usb_type` 答 `EAGAIN` 时马上返回、算正在认（一个空的 FIFO 代替它），例行检查照走，提示等认出来；充电器的 uevent 让 Shell 马上查 |
| `hal` 单元测试 | 读写设备属性（`ReadAttr`、`WriteAttr`）：答 `EAGAIN` 的读和写马上报错（`os.ReadFile` 在同样的文件上一直等，§7） |
| `hal/led`、`hal/backlight`、`hal/pm` 单元测试 | 定时器触发器的写法、多色灯的强度、关灯；背光的百分比和开关、渐变（沿 CIE 明度、中途换目标不跳、渐暗到最暗再关、渐暗途中亮回来，假的 sysfs 目录），LED 类的背光（键盘的 `::kbd_backlight`、厂商内核的 `lcd-backlight`：关是亮度 0，再开回到原来的亮度）；挂起的握手（`wakeup_count` 写回）、唤醒闹钟设上又清掉（`+N`，板子说 `absolute` 时写唤醒时刻，向上取整到秒）、有 `mem` 用 `mem`，没有 RTC 唤醒闹钟就不挂起 |
| `hal/rtc`、`programs/clockcmd`（`phone-clock`）、Shell 的时钟单元测试（§6） | 读 RTC（假的 sysfs）、在它跳秒时量偏移、存取偏移文件；开机按 RTC 加偏移设时钟，RTC 从头数了、时钟已经对了就不设；时钟不归自己（chroot、`PHONE_CLOCK=host`、不是 root）时不设、不存、不写；关机只在 NTP 同步着、偏移变了时存；只读的 RTC 上手动设的时间和 NITZ 存成偏移、不写 RTC，NTP 同步着时跟上漂移，量偏移时又设了时间就再量一次；能设的 RTC 写进去（经 timedated 设的不重写；NITZ 不经 timedated）；自测的 `timeshift` 不碰系统时钟和 RTC；phone-fw 装上并启用 `phone-clock.service`，排在 `sysinit.target`、`time-set.target`、systemd-timesyncd 之前 |

| 关机充电的单元测试（`programs/chargercmd`、Shell 的 `TestStandbyFollowsTheSettings`）和 `yocto/charger-generator-test.py`（§10） | 亮 5 秒熄屏，短按电源键亮、灭，按住重启进系统（之后什么都不理；重启失败下一次按住再试），拔掉充电器 3 秒后关机、屏幕亮着说明，3 秒内插回去接着充，起来时没有充电器就关机，没有电池永远不关机，提示跟着电量（充满、快充），熄屏时不画；整个程序接一个假的渲染器和假的 sysfs（握手、`keys`、帧、按住以后的「正在开机…」、状态文件）；语言、时区、12/24 小时制、主题和亮度照 Shell 的设置；生成器在 6T 正常开机、充电开机的命令行上（报告 007 的原样）、模拟器的命令行、6T 充电开机时 `androidboot.mode=charger` 后面又跟着 `androidboot.mode=normal`（报告 008）、`charger` 在 `normal` 前后都算、两个 `normal`、`systemd.unit=` 留给 systemd、空命令行；单元文件的依赖和顺序，黑名单里的模块在 6T 的内核里是模块 |
| QEMU 上的关机充电（`make yocto-charger-test`，`yocto/qemu-charger.sh`，§10） | 带 `androidboot.mode=charger` 开机：生成器选了 `phone-charger.target`；`test_power` 的电池 50%、充着电，充电画面亮着；Shell、telephonyd、ModemManager、iwd、蓝牙、multi-user.target 都没起；30 秒内自己熄屏，按电源键亮；拔掉模拟的充电器以后模拟器自己关机。2026-11-26 ✓：开机约 18 秒出充电画面，5 秒后熄屏，电源键亮屏（面板 2 ms），拔掉充电器 3.05 秒后 `poweroff`，再 3.3 秒 `reboot: Power down`，整个运行 35 秒；检查第一次没过是检查自己错了（测试服务也是目标的一员，目标在它结束前还在等启动，不是 active），改成认排着的启动任务 |

自测开头把超时关掉（脚本的按键会被用来点亮屏幕），只在上面这些用例里打开（`screen-auto on`）。

## 6. 时钟：RTC 的唤醒闹钟和关机期间的时间

> 状态：单元测试 ✓，**未上真机**（6T 上要查的见 [39](39-oneplus-6t.md) §14.10）。

RTC（实时时钟）是 PMIC 或 SoC 里关机时也在走的计数器：开机时内核按它设系统时钟（`CONFIG_RTC_HCTOSYS`），挂起时它的唤醒闹钟叫醒手机（§3）。各块板子的 RTC 不一样，所以写在主板配置的 `[rtc]` 里（[24](24-sku-framework.md) §1、§2），经 `sku.json` 的 `rtc` 交给 Shell、`hal/pm` 和 `phone-clock`（`hal/rtc`）：

| 字段 | 默认 | 意思 |
|------|------|------|
| `read_only` | `false` | 设不进去：`RTC_SET_TIME` 失败（一加 6T 的 PMIC RTC 在主线上报 `ENODEV`）。设时间时不写它 |
| `offset` | `false` | 系统时间存成相对这个只读 RTC 的偏移，开机按 RTC 加偏移设时钟；只能和 `read_only = true` 一起写 |
| `alarm` | `relative` | 唤醒闹钟怎么写：`relative` 是 `+N`（从 RTC 自己的时间起 N 秒，哪种 RTC 都对）；`absolute` 是唤醒时刻本身，只在 RTC 和系统时钟一致（能设、设时间时一起写）时对，只读的 RTC 不能这样写（6T 上它曾让闹钟落到 57 年以后） |

现有的板子：一加 6T 是 `read_only = true`、`offset = true`；一加 6（同一颗 PMIC）和红米 Note 4X（PM8953，同一个驱动）照它写，标在 `unverified` 里；QEMU 的 pl031 能设，写明 `read_only = false`；两块骁龙模块板没写（默认能设），到了板子上再定（[41](41-design-gaps.md) §11）。偏移在能设的 RTC 上也对，拿不准的板子写 `offset` 不会错。

**能设的 RTC**（hwclock 的做法）：Shell 设了时间（手动、网络时间 NITZ）以后写 RTC，经 systemd-timedated 设的由 timedated 自己写（手动设的经它；NITZ 只在自动时间开着时来，这时 NTP 也开着，timedated 不接 SetTime，Shell 就直接设系统时钟、自己写 RTC）；NTP 同步时内核每 11 分钟自己写（`CONFIG_RTC_SYSTOHC`）；关机时 `phone-clock save --if-synced` 在 NTP 同步着时再写一次。开机时内核按它设时钟。

**只读的 RTC**，照 Android 的做法存偏移：

- 偏移是系统时间减 RTC 的时间（毫秒），存在持久区的 `/var/lib/slots/rtc-offset.json`（`offset_ms`；量的时刻 `at`；谁设的时间 `by`：`user`、`network`、`ntp`、`save`），恢复出厂设置不清它：时钟是手机的，不是用户的数据。RTC 只按整秒走，量的时候一直读到它跳秒，那一刻它正好是那一秒，误差是轮询间隔（5 毫秒），不是一秒。
- 什么时候量：Shell 设了时间（设置里手动设、telephonyd 报的 NITZ）以后马上量；自动时间开着、NTP 同步着（timesyncd 的 `/run/systemd/timesync/synchronized`）时每 5 分钟核对一次，和存着的差出 0.5 秒以上才重存（RTC 的晶振和 NTP 之间的漂移）；量的时候又设了时间，就再量一次。关机时 `phone-clock save --if-synced` 在内核说 NTP 同步着时（`adjtimex`：`STA_UNSYNC` 清着，误差界小于 16 秒；手动设过时间就不算）再核对一次。别的办法改的时间（SSH 上 `date -s`）不存，要存就跑 `phone-clock save`。
- 开机：`phone-clock.service`（kario-system 里的 `phone-clock restore`）在 `sysinit.target`、`time-set.target` 和 systemd-timesyncd 之前跑，带默认依赖的服务（Shell、telephonyd）起来时时间已经设好：时钟是 RTC 的秒加半秒（不知道读在这一秒的哪儿）加偏移。RTC 的驱动可能是模块（Nura 的 6T 内核里 rtc-pm8xxx 是 `=m`；Kario 的 6T 内核把它编进了内核，[39](39-oneplus-6t.md) §8.3），udev 加载它、它注册时内核按 RTC 的数设一次时钟（`CONFIG_RTC_HCTOSYS`），在那之前设的时钟会被拨回 1970：所以它排在 udev 的冷插（systemd-udev-trigger）之后，先等 RTC 出现、内核按它设过时钟（`hctosys` 是 1；最多等 15 秒，内核不做这一步时 1 秒后接着走），再设。RTC 加偏移早于量偏移的时刻，说明 RTC 掉过电、从头数了，偏移不再对，时钟不动（交给 timesyncd 存的时间和网络），日志写明；已经对了（差不到 1 秒）也不动。
- 没有偏移时（新手机、RTC 掉过电）：开机是 RTC 自己的数（6T 是 1970 年加几天），systemd-timesyncd 把它拨到上次存的时间，等 NTP 或 NITZ 给出时间。
- 自测的时间步进（`timeshift`、模拟的挂起）动的是 Shell 自己的时钟偏差（`clockSkew`），不是系统时钟，碰不到 RTC 和偏移。

**时钟归谁**：只有拥有时钟的系统设它（`hal/rtc` 的 `Ours`）：root、不在 chroot 里（根目录和 1 号进程的不是同一个）、环境里没有 `PHONE_CLOCK=host`。chroot 套件（[24](24-sku-framework.md) §6.2）进 chroot 的每个命令都带 `PHONE_CLOCK=host`：那里的时钟是 Nura 的，Shell 不设时间、不开关 NTP（镜像里有 timedated，系统总线却是 Nura 的）、不写 RTC、不存偏移，设置里也不出「设置日期和时间」，日志里写一行；套件的 `setup` 照构建机的时钟设手机的。刷了整机，Kario 拥有时钟：是它自己的服务（`phone-clock`、面板上的 Shell）的；套件在整机上跑测试（`--whole`）时同样给每个命令带 `PHONE_CLOCK=host`，测试里的 Shell 不动它。

`phone-clock show` 列出板子的 RTC、它的时间（`since_epoch`）、内核开机时按没按它设时钟、系统时钟、NTP 同步没有、存着的偏移和 RTC 加偏移离系统时钟多远。内核那边的办法（设备树的 `allow-set-time`，或者 PMIC 的 SDAM 里放偏移的 nvmem 单元）和 6T 为什么不用，见 [39](39-oneplus-6t.md) §11.2。

## 7. 电源键、系统的 logind、看门狗和关机

> 状态：电源键归 Shell 在 QEMU 上 ✓（「电源键」用例；镜像不带下面的 logind drop-in 时，按下后 logind 让模拟器关机，2026-11-24）；连按电源键（面板一次换一回、最后跟着最后一下）在开发机（单元测试、自测）和 QEMU 上 ✓（2026-11-25），**未上真机**；电源菜单（电源键短按关掉它，只有电源键和音量键时用它们回答，熄屏时关掉）在开发机上 ✓（单元测试、自测，2026-11-25），**未上真机**；设备属性不经 Go 的文件对象、循环的看门狗：单元测试 ✓（2026-11-25），**未上真机**；看门狗、关机和电源键唤醒**未上真机**（6T 上要查的见 [39](39-oneplus-6t.md) §14.10）。

- **电源键和盖子归 Shell**：电源键按一下熄屏或点亮、长按开电源菜单，整机什么时候挂起，都由 Shell 定（§1、§3，`shell/keys.go`、`shell/sleep.go`）。键由渲染器读：phone-paint 的输入助手打开每个有回车、电源键或音量键的输入设备（gpio-keys、PMIC 的电源键），电源键交给 Shell（[23](23-renderer-protocol.md)）。systemd-logind 也读这些设备（udev 给有键或开关的输入设备打 `power-switch` 标签），它默认按电源键关机、合上盖子（霍尔传感器的 `SW_LID`，6T 的磁吸保护套）挂起。镜像里的 drop-in（`systemd-conf` 的 bbappend 装的 `/usr/lib/systemd/logind.conf.d/50-phone.conf`）把 systemd 259 认的电源、重启、挂起、休眠键（短按和长按）和三种盖子的处理都设成 `ignore`，`IdleAction=ignore`；Shell 不用拿 logind 的抑制锁。chroot 套件跑在 Nura 上，那里的 logind 是 Nura 的，`shell-start` 照旧用 `systemd-inhibit`/`elogind-inhibit` 挡住它（[24](24-sku-framework.md) §6.2）。
- **短按和长按**：渲染器读电源键（phone-paint 的 `PowerKey`）：没到长按时间就松开是短按，松开时才报（带按了多少毫秒 `ms`）；按住到长按时间、内核也说它还按着，就是长按（电源菜单），之后松开不再报；自动重复只是「还按着」，不算别的。没看到松开（设备重新打开、事件缓冲溢出）时，到了长按时间先问内核（`EVIOCGKEY`）：内核说已经松开，就算一次短按，不出电源菜单；再按下就从头算，键不会一直算「按着」。长按时间是设置 › 辅助功能 › 按键 › 长按（默认 600 ms，别的键也是它），电源键不短于 500 ms：Shell 在 `keys` 消息里给 `power_long`（[23](23-renderer-protocol.md) §3）。600 ms 照旧：和九宫格、挂断键（没有电源键的机型它就是电源键）的长按一样；比安卓的 500 ms（`config_globalActionsKeyTimeout`，长按出电源菜单）稍长，慢一点的点按也还是点按；现在的手机大多让长按侧键唤起语音助手（iPhone、近几年的 Pixel 和三星默认如此），关机菜单改成侧键加音量键一起按住，Kario 没有语音助手，长按就是电源菜单。
- **电源菜单**（`shell/keys.go` 的 `openPowerMenu`，**未上真机**）：依次是「取消」「关机」（数字键 1）「重启」（2）「打开/关闭飞行模式」（3），打开时选着「取消」，所以 OK 不会一下关机。菜单开着时短按电源键：
  - **只有电源键和音量键**（接着的输入设备都按不出 OK，渲染器在 `input` 消息里报的 `ok`，[23](23-renderer-protocol.md)；一加 6T 没接键盘时就是这样）：音量键移动选中项（上一项、下一项，不调音量），短按电源键选中选着的那一项：没动过就是「取消」，菜单关掉；音量下键一下到「关机」再短按就关机。菜单的第一行写着「音量键移动 · 电源键选择」。
  - **接着能按 OK 的键盘或鼠标、带键盘的机型**（QEMU、九宫格和全键盘的板子、接了蓝牙键盘的 6T）：短按电源键只关掉菜单，不管选着哪一项，所以从菜单里电源键永远关不了机；用 OK、数字键选，返回或挂断键关掉；音量键照常调音量。没有单独电源键的机型（挂断键兼作电源键）按一下挂断键也只关掉菜单，不回待机（通话中照旧关掉菜单并挂断）。
  - **点屏幕选「关机」「重启」**（2026-11-27，报告 008，**未上真机**）：点了不马上关，再问一次「要关机吗？」「要重启吗？」，对话框的按钮大，点「关机」（「重启」）才做，「取消」或返回不做；屏幕熄灭时这个对话框和菜单一起关掉。用键选（音量键加电源键、OK、数字键 1、2）照旧马上做。为什么：6T 上菜单一行约 5 mm 高，「重启」紧挨在「关机」下面，点一下就选中；报告 008 里点了「关机」，手机却走了 `systemd-reboot`（§10「从电源菜单关机」）。Shell 记的 `power: poweroff`、`power: reboot`、`power menu: … tapped; asking again` 现在也进 journal（`journalctl -b -1 | grep 'phone-shell: power'`），以前只写在 `/run` 的 `shell.log`，关机就没了。单元测试 `TestTouchPowerMenuAsksAgain`（拿掉这次的改动失败）。
  - 两种情况下关掉菜单的那一下都不算连按的第一下：紧接着再按一下就熄屏，不会打开快速选卡；屏幕还亮着。快速选卡（连按两下打开的卡片菜单）同样短按一下电源键就关掉。
  - 屏幕不管为什么熄灭（超时、电源键、面板没开成），电源菜单和快速选卡都关掉，亮屏时不再在那儿。盖子（霍尔传感器）logind 不管；只有 SKU 写了 `lid` 的翻盖，Shell 照渲染器报的盖子熄屏、亮屏（[24](24-sku-framework.md) §9.10.8），6T 保护套的霍尔传感器 SKU 不写，没人读。
  - 渲染器重启后 Shell 把输入设备从头数起（新的渲染器重新报每一个）。单元测试 `TestPowerMenu*`（6T 的 `input` 消息原样喂给 Shell）、`the_phones_buttons_cannot_press_ok`（phone-paint），自测「电源菜单」四个用例（`buttons-only on` 模拟没接键盘的 6T，`power-key off` 模拟挂断键兼作电源键的板子）。
  - 每种机型（有电源键、挂断键兼作电源键、只有电源键和音量键；九宫格和全键盘）怎么打开电源菜单、选「关机」，见 [18](18-nokia-form-factors.md) §5.1 的「怎么关机」表。按住不会直接关机（决定：安卓长按也是出菜单，口袋里压住一个键不能把手机关掉）；按住十来秒的复位是 PMIC 的（下面「按住电源键不放会重启」）。
- **闹钟响着时**（`shell/keys.go` 的 `powerPressed`、`shell/cards.go` 的 `cardPickKey`、`shell/alarm.go` 的 `snooze`，2026-11-26）：短按电源键是稍后提醒，像安卓：闹钟声停，5 分钟后再响，提示「5 分钟后再响」，待机界面写「闹钟 HH:MM（稍后提醒）」；这一下不算连按的第一下（紧接着再按一下照常熄屏）。按住照旧打开电源菜单，闹钟在下面接着响，短按一下电源键关掉菜单，再短按才稍后提醒。没有单独电源键的板子挂断键就是电源键，按一下、按住都一样；板子有单独电源键时挂断键不动闹钟（以前在哪种板子上挂断键都不动闹钟，按住也不出电源菜单）。闹钟只能用 OK（或触屏点「停止」）停止，电源键、挂断键都不停止它。以前短按电源键走一般的路：亮着就熄屏，闹钟接着响（6T 没接键盘时停不了也稍后提醒不了）。音量键不变：闹钟和铃声用同一个音量（`applyVolumes`），响着时按音量键调的就是正在响的闹钟的音量；安卓的时钟让用户选音量键调音量、稍后提醒还是关闭（默认调音量），Kario 没有这个设置，不加。单元测试 `TestPowerKeySnoozesTheAlarm`、`TestEndKeyAsPowerKeySnoozesTheAlarm`（拿掉这次的改动都失败），自测「闹钟响时短按电源键」「闹钟响时按挂断键（没有电源键的机型）」；**未上真机**。
- **屏幕的开关一次一个**（`shell/screen.go` 的 `panelSync`）：屏幕亮不亮以 Shell 为准，按键改的是屏幕要的状态；面板的开关一次只向渲染器要一个，等它回答（`power` 的回答，带用了多少毫秒），再过 250 ms 才要下一个，中间按了多少下都只要最后要的那个，面板已经是那样就不要。所以连按十下，面板最多换一两回，最后和最后一下一致；以前每一下都直接发给渲染器，它排着队一个一个开关面板（面板每次要 2 秒时，停手以后还要开关 10 秒）。渲染器收到排在一起的几个 `power` 只照最后一个做，每个都回答；关、开面板以前等正在翻的页完成（最多 0.5 s），来晚的翻页完成事件只算那一页的（以前算成下一页的：下一帧画进了还在屏上的缓冲，下一次翻页报 `EBUSY`，之后一直原地画）；开了面板以后翻一页验证，0.5 s 里没完成，回答里带 `warn`（命令模式的 DSI 面板没起来时不完成翻页），Shell 把面板关掉再开一次（只一次，背光等第二次开好再亮，日志 `panel: on …; turning it off and on again`；第二次还是这样就记下、照样亮背光）：2026-11-22 挂起以后黑屏，手动再做一次模式设置就好了，这是自动做的同一步（`TestPanelOnWithAWarningOffAndOnAgain`，**未上真机**，[39](39-oneplus-6t.md) §16.12）。背光等面板开了再亮：渲染器回答以前不写背光（6T 的背光归面板驱动，面板关着、正在上电时写它，和面板的上电过程抢 DSI，也会先照出面板里的旧内容）；2 秒没回答只记一行日志，照样不碰背光（渲染器卡在面板的上电里时，写背光会把 Shell 也卡住），回答来了再亮，渲染器重启了也亮。面板没开成（渲染器报 `set_crtc` 的错），Shell 再要一次；还不行，Shell 的屏幕也算灭（和面板一致），日志写明，下一次按键从头再试。关不成同样再要一次，还不行就不再要，等屏幕要亮。渲染器卡在内核里不回答时 Shell 照常答键，它从不等渲染器。单元测试 `TestPanel*`、`TestBacklightWaitsForThePanel`（假的渲染器每次开关 0.02–2 秒，按要求失败），自测「电源键连按」。
- **日志**：每一下电源键（`power key: short press (held 95 ms): screen off (the panel on)`）、屏幕的开关（`screen off (power key)`）、面板的每次请求和回答（`panel: asking the renderer to turn it off`、`panel: off in 180 ms`）和 `sleep:` 那几行，Shell 写进 `/run/phone/shell.log`，整机上也写进 journal（`journalctl -b -u phone-display -o short-precise`，强制重启以后还在）；渲染器写按下、松开和判断（`phone-paint: power key down`、`phone-paint: power key up: short press (95 ms)`）、面板每次开关用了多久（`phone-paint: panel off in 180 ms`），在 `/run/phone/gui.log` 和同一个 journal 里。真机上怎么抓见 [39](39-oneplus-6t.md) §14.10。
- **Shell 的循环不卡在 sysfs 上**：`hal/` 读写设备属性（电源、背光、通知灯、光线传感器、振动、挂起）都经 `hal.ReadAttr`、`hal.WriteAttr`，直接用系统调用，不经 Go 的文件对象。Go 打开文件时把 epoll 收的文件都设成非阻塞、挂到自己的轮询器上，sysfs 的属性 epoll 也收；驱动对读或写答 `EAGAIN`（PMI8998 充电器认充电器那几百毫秒里的 `usb_type`；设备拒绝挂起时的 `/sys/power/state`），Go 就等这个文件「可读」，sysfs 的属性从不这样通知，于是一直等下去。2026-11-25 6T 上 Shell 就这样停住了：屏幕亮着，按键、超时都没反应，日志停在 `screen on (key)`（[39](39-oneplus-6t.md) §16.11）。现在 `EAGAIN` 和别的错一样马上返回。
- **循环的看门狗**（`shell/loopwatch.go`，面板上的 phone-shell）：Shell 做的事都在一个循环里，循环里一个调用不返回，一切都停，以前一行日志也没有。现在循环每秒跑一次心跳，5 秒没跑到就记一行，写明循环的 goroutine 停在哪（运行时报的状态和调用，里层在前）和有多少键在等它，例如 `loop: has not turned for 5s; the loop's goroutine [IO wait] poll.(*FD).Read < … < power.Read < shell.(*Core).power < …; 2 keys from the renderer waiting; the last power key 3s ago`；转回来再记 `loop: turned again after 12s`。电源键一到（循环处理以前）就记一行 `power key: from the renderer: short press (held 95 ms)`，循环停着也看得出键到了 Shell。别的键只计数（打字的内容不进日志）。这些行和上面的一样也进 journal。
- **按住电源键不放会重启**（6T，2026-11-25 用户看到的，[39](39-oneplus-6t.md) §16.11）：这是 PMIC 自己的复位（PON 的 KPDPWR S2 复位：按住超过 S1 加 S2 的时间就复位），不是 Kario 做的（推断）：logind 不管电源键（上面的 drop-in），渲染器每次按住只报一次长按、之后不再报，Shell 只在电源菜单里选了「重启」才重启，运行时看门狗关着（板子只设了重启时的）。时间是引导程序设的：主线的 pm8941-pwrkey 和设备树都不设 S1、S2（Android 的下游驱动按设备树设）。Kario 不改它：手机卡死时靠它重启，和 Android 的强制重启（按住约 10 秒）一样。按多久没量到；怎么量见 [39](39-oneplus-6t.md) §14.10。
- **自测怎么按真的电源键**：自测的渲染器是无头的，不读键，所以 `button power` 这一步自己读内核里有 `KEY_POWER` 的输入设备（`hal/input`），在输出上打请求 `PHONE-SELFTEST-PRESS: power <n>` 请人按（控制台文件的一行，没按下就隔 3 秒再说一遍，[40](40-testing.md) §2），照渲染器的规则判断短按、长按（`powerHold`，Go 的单元测试 `TestPowerHold`）交给 Shell，等它松开；`button-burst power <次数> <间隔 ms>` 请人照间隔连按，每一下来了就交给 Shell。QEMU 上由 `yocto/qemu-selftest.sh` 起的 `yocto/qemu-buttons.py` 看着控制台，每个请求从 QEMU 监视器发一次 `system_powerdown`，再说一遍的不再发（连按的请求照次数和间隔发；内核的行落在里面时拼回来，拼不回来的不按、报出那一行；只认自测自己打的行，别的行引用它不算，比如开机自测的心跳每 30 秒引用一次运行的最后一行，`yocto/qemu-buttons-test.py`）：virt 机器的电源按钮在它的 PL061 GPIO 上，设备树里是 gpio-keys 的 `KEY_POWER`（QEMU 的内核为此加了 `CONFIG_GPIO_PL061`、`CONFIG_KEYBOARD_GPIO`，`phone-power-sim.cfg`），每次按下 100 ms。用例「电源键」「电源键连按（实体电源键）」只在 `PHONE_SELFTEST_BUTTONS` 列着 `power` 的运行里跑（`require-button power`，QEMU 的开机自测设它）：待机时按一下熄屏，5 秒后系统还在，再按一下亮屏；150 ms 一下连按十下、五下、一下。像素界面的 QEMU 检查（`make yocto-gui`，`yocto/qemu-gui-drive.py`）用面板上真的 phone-paint：电源按钮按住 0.9 s 出电源菜单，150 ms 一下连按十下、五下、一下，每一下 Shell 都只收到一次、都是短按，屏幕和面板最后是亮、灭、亮（`PHONE-GUI-POWER` 几行）。
- **看门狗**：systemd 默认在重启时用硬件看门狗（`RebootWatchdogSec=10min`）：PID 1 交给 systemd-shutdown 时设上它，systemd-shutdown 一路喂到最后的 `reboot()`，不解除。驱动收不下这个超时时 `WDIOC_SETTIMEOUT` 返回 `EINVAL`，systemd（259 和 261 都是这样）随即解除看门狗，这次重启就没有看门狗。所以看门狗按板子写（`[watchdog]`，[24](24-sku-framework.md) §2），phone-sku 装成 `system.conf` 的 drop-in（`/usr/lib/systemd/system.conf.d/50-phone-watchdog.conf`），没写的板子照 systemd 自己的。骁龙 845 的 APSS 看门狗（`qcom_wdt`，`qcom,kpss-wdt`，接 32764 Hz 的睡眠时钟）计数器是 20 位，最长 `0xfffff / 32764` = 32 秒，内核只拒绝比这长的（`watchdog_timeout_invalid`），一加 6T、6 写 `max_timeout = 32`、`reboot = "30s"`。重启是 SoC 的硬复位（pm8941-pwrkey 在固件拉低 PS_HOLD 之前把 PMIC 的 PS_HOLD 复位设成硬复位），设着的看门狗不会带进下一次启动。
- **关机**：6T 的内核走 PSCI 的 `SYSTEM_OFF`（`sdm845.dtsi` 有 `psci` 节点，`CONFIG_ARM_PSCI_FW=y`，`pm_power_off = psci_sys_poweroff`），pm8941-pwrkey 事先把 PS_HOLD 设成关机；没有 `qcom,pshold` 节点，msm-poweroff 不用。内核要是不能关机，`reboot()` 会改成停机（CPU 停在原地），不会重启。插着充电器关机，PMIC 见到充电器又把手机开起来，引导程序在命令行里写 `androidboot.mode=charger`：这时进关机充电，不进系统（§10）。
- **是什么叫醒了手机**：`hal/pm` 读 `/sys/power/pm_wakeup_irq`，Shell 记成「sleep: woke after …（irq N）」；内核只在 `CONFIG_PM_SLEEP_DEBUG`（`PM_DEBUG` 加 `PM_SLEEP`）时有这个文件，6T 的内核原来没有，日志里总是「no wake reason」。每个 Kario 内核都加 `CONFIG_PM_DEBUG=y`（`phone-power.cfg`）。
- **电源键唤醒以后**：叫醒手机的那一下键在解冻以后才到渲染器（内核在恢复时补上中断，键已经松开时补报按下和松开），Shell 收到时屏幕是关着的，就点亮它；醒来时别的东西都不点亮屏幕（不然这一下键会把它关掉）。键先到、Shell 后听说醒了，或者反过来，结果一样（`TestPowerKeyAroundAWake`）。

## 8. 长连接的耗电（估算）

应用自己的长连接（`net.keepalive`，[21](21-push.md) §8）每发一次心跳，蜂窝射频都要从空闲升到连接态（RRC 建立），收发几百字节，然后网络还让它在连接态停一段尾巴时间（运营商设的不活动定时器）才放回空闲；挂起的手机醒来还要醒着 10 秒（`awakeAfterWake`）。一次唤醒的能量按下面算（`push.WakeCost`、`push.LTEWake`）：

E = 升连接态 0.26 s × 1.2 W + 尾巴 T × 0.5 W + 醒着 10 s × 0.1 W（比挂起多出的部分），电池标称 3.85 V。

这些都是**假设的数**，量级取自公开的 LTE 测量（较早的 LTE 手机尾巴功率在 1 W 上下，现在的基带低一些），**没在 Kario 的手机上量过**；尾巴时间各运营商不同，所以当参数，取 5、10、20 秒。一次唤醒：T = 5 秒 0.275 mAh，10 秒 0.455 mAh，20 秒 0.816 mAh。

| 心跳间隔 | 唤醒次数/天 | mAh/天（T = 5 s） | mAh/天（T = 10 s） | mAh/天（T = 20 s） | 一加 6T（3700 mAh）一天的电，T = 10 s |
|------|------:|------:|------:|------:|------:|
| 1 分钟 | 1440 | 396.1 | 655.8 | 1175.3 | 17.7% |
| 5 分钟 | 288 | 79.2 | 131.2 | 235.1 | 3.5% |
| 10 分钟 | 144 | 39.6 | 65.6 | 117.5 | 1.8% |
| 28 分钟 | 51.4 | 14.1 | 23.4 | 42.0 | 0.6% |
| 系统推送（应用没有自己的连接） | 0（另加） | 0 | 0 | 0 | 0 |

- **系统推送**：整台手机只有 pushd 的一条连接，它学到的心跳最长 28 分钟（[21](21-push.md) §2），也就是上表 28 分钟那一行：每天约 51 次唤醒、23.4 mAh（T = 10 s），**所有**应用一起分摊；应用接入推送不多一次唤醒，只有真正来消息时才多（数据推送每个应用每小时最多 20 次，§3）。
- **合并唤醒**：一个应用自己的心跳若和推送心跳各醒各的，一天的唤醒是两者相加：1 分钟、5 分钟、10 分钟、28 分钟的心跳加上 28 分钟的推送心跳是 1491、339、195、103 次；Kario 让心跳搭推送心跳（半个间隔以后的任何一次唤醒都能搭），系统为它单独醒时 pushd 的心跳也搭这一次（`radio_active`，[21](21-push.md) §4.3，挂起的时间也算进 pushd 的限次），合起来是 1440、288、144、51 次，也就是两者里更频繁的那个（`push.Beat`，单元测试 `TestBatchedHeartbeats` 模拟一天）。心跳间隔不比推送心跳短的应用不多花电。
- **维护唤醒不算窗口**（`shell/sleep.go`）：挂起的手机最迟 15 分钟由 RTC 叫醒一次（§3），叫醒的只是 CPU。基带自己管 RRC 状态，应用处理器醒来不往空口发东西，射频照旧空闲，直到有东西要发（上行）或网络寻呼它（下行）。所以这样一次唤醒只花上面公式里「醒着」那一项（1 J，约 0.07 mAh）；心跳搭它，就得为心跳把射频升上来（升连接态加尾巴 5.3 J，约 0.38 mAh，T = 10 s），比心跳到期时自己醒一次只省那 0.07 mAh，心跳却提前了，以后的也跟着提前。按半个间隔开窗、15 分钟一次维护唤醒，28 分钟的心跳（pushd 的或应用的）就成了每 15 分钟一次：一天 96 次射频唤醒、43.7 mAh；不搭是 51 次射频唤醒加 51 次只醒 CPU 的维护唤醒，23.4 + 3.7 = 27.1 mAh。提前一个间隔的 f 去搭，只有 f 小于「醒着」占一次唤醒的比例才划算（T = 10 s 约 16%，5 s 约 26%，20 s 约 9%）；维护唤醒总在上一次唤醒 15 分钟后，离 28 分钟心跳的期限还有约 13 分钟（f 约 46%），中间没有别的唤醒就碰不到划算的时候。所以心跳只搭带了射频的唤醒：推送连接上的一行、一条推送、一批后台任务、某条连接来了数据、系统为某个心跳单独醒的那次；维护唤醒、闹钟、按键、充电器把手机叫醒都不算。基带为来电、短信叫醒时射频是醒着的，但 Shell 从唤醒本身分不出是谁叫醒的，也不算（等调制解调器报告射频状态，[21](21-push.md) §7）。以前 Shell 每次醒来都告诉 pushd 射频醒着（`radio_active`），只是被 pushd 按单调时钟算的限次挡掉了大半；限次改用算上挂起时间的时钟后，它会在每次维护唤醒都把射频叫起来，所以去掉了（单元测试 `TestMaintenanceWakeIsNoRadioWindow`，旧代码失败）。
- **每个应用的上限**：系统单为一个应用醒每小时最多 6 次，所以一个应用自己的心跳一天最多多出 144 次唤醒、约 65.6 mAh（T = 10 s）；要求 1 分钟心跳的应用，没有别的唤醒时心跳就晚了（它的服务器或 NAT 会断开它，它该改用更长的间隔或系统推送）。数据把冻结的应用唤醒另算，每小时最多 20 次，和数据推送一样。
- **看得见**：设置 › 电池的「自己的连接」一节按应用列出最近一天系统为它醒了几次、几次心跳搭了别的唤醒、因数据唤醒几次，和按上面 T = 10 s 估的电量（系统单为它醒和因数据醒的次数 × 0.455 mAh；搭车的不算）。这些次数（每小时的预算就是从里面算的）存在状态目录的 `shell/keepalive.json`：Shell 原地重启或崩溃后重启，接着数，不重算、不丢，重启后应用要是重新连了，也还是同一个应用的一行（[21](21-push.md) §8.2；单元测试 `TestKeepaliveSurvivesHandoff`、`TestKeepaliveLostAcrossRestart`，旧代码失败）。原地重启这条路也有自测用例「应用自己的长连接，Shell 重启后」（[40](40-testing.md) §2）：测试服务器在 Shell 的进程外，重启前冻结的应用因数据被唤醒 1 次，重启后应用进程不变、新的 Shell 接管系统那份连接，冻结后服务器再发来的消息照样唤醒它，次数接着数成 2 次，过了期限系统为它的心跳单独唤醒 1 次，服务器从头到尾只有这一条连接（没有重连）；开发机、QEMU ✓（2026-11-27），**未上真机**。崩溃后的重启（systemd 的 fd store）还是只有单元测试 `TestKeepaliveFDStore`。
- **fd 存储的预算**：为了熬过 Shell 崩溃，自己的连接在 systemd 的 fd 存储里留一份（[21](21-push.md) §8.2），和每个运行中应用的协议连接共用这个存储。存储有 512 个位置（`phone-display.service` 的 `FileDescriptorStoreMax=`，以前是 64；满了以后 systemd 只记一行日志，悄悄关掉多出来的，发的人不知道）。Shell 按 systemd 给的 `$FDSTORE` 算位置，systemd 254 以前不给，就按 64 算。分法：应用的协议连接总有位置；自己的连接每个应用最多一条，一共最多占四分之一（512 里 128 个），只用协议连接剩下的位置，它的应用的协议连接不在存储里时它也不进。放不下时，最久没用过的那条自己的连接让出位置（用过：连上、冻结时来了数据、应用在屏幕上打开过；在前台的应用算现在），从不挤掉协议连接，有了空位再放回去。它让出和拿回位置时，Shell 各记一行日志，不是每次都记。这时 Shell 崩溃，重启后的 Shell 给这个应用发 `keepalive` 事件 `lost`，文字是 `full`（`ui.KeepaliveLostFull`），应用重连，日志一行。别的原因：套接字没跟过来是 `dropped`，跟过来了但没有存下的心跳安排是 `unsaved`。为什么是 512：每个运行中的应用最多两个位置，没有手机同时跑 256 个应用（内存紧时后台应用先被结束）；一个位置 systemd 只花几百字节，512 也在服务能开的 1024 个文件以内（单元测试 `TestKeepaliveFDStoreBudget`，旧代码失败；**未上真机**）。
- **上一次推送心跳的时间**：自己连接的心跳搭不搭推送心跳，要看 pushd 上一次心跳的时间（`lastPush`），它和上面的次数存在同一个文件里。每次推送心跳都写，忙的推送连接上就是每次心跳一次闪存写入；所以只在它比存下的值晚了 10 秒（`push.KeepaliveSlack`）以上时才写，最多 10 秒写一次；没有自己的连接时不写，也不读。崩溃后恢复的就是那次推送的时间，最多早不到 10 秒。推送心跳预计能搭上一条心跳时，系统为这条心跳单独醒，最早也在预计的推送心跳 10 秒以后，所以早不到 10 秒的估计下真的推送心跳仍先到，心跳照样搭它，不多一次唤醒；最坏一次心跳晚到 10 秒，这本来就允许（`push.Beat.OwnWake`；单元测试 `TestKeepaliveLastPushAfterACrash`，旧代码失败）。

## 9. 没做的

- 长连接的功耗（§8）是估算：真机上量一次唤醒（升连接态、尾巴、醒着）的电流，换掉假设的数；射频真的醒着时（调制解调器报告），也让心跳搭它（[21](21-push.md) §7）。推送连接两端还开着内核的 TCP keep-alive（Go 的默认值，空闲 15 秒就探测），§8 没算它（[21](21-push.md) §4.3，**未实现**）。
- 真机上的功耗测量：息屏电流、待机一天掉多少电（要板子）。
- 翻盖合着盖待机 7 天（[24](24-sku-framework.md) §9.10.10 的预算，**估计**）要 §3 改的两处，Shell 里做了（§3 表后，单元测试 ✓，旧代码失败）：外屏的分钟唤醒改完时间马上再睡；合着盖时维护唤醒搭推送的心跳。**未上真机**：还没有翻盖机。在真机上起作用前还缺画外屏（§9.10.11，**未实现**）；渲染器报盖子 2026-11-27 做了（[24](24-sku-framework.md) §9.10.8，**未上真机**）。要在真机上看：RTC 的分钟唤醒都响在那一分钟开始后 1 秒内（日志里 `short wakes` 一小时约 60 次，外屏的时间整分钟变）；一次分钟唤醒 SoC 醒多久（预算按 0.2–0.3 秒算）；来电、短信正赶上分钟唤醒前那 1 秒时是不是马上响（Shell 只按时间把这 1 秒里的唤醒当成 RTC 的，要靠内核的唤醒事件拦住挂起，直到 telephonyd 拿到它）；合着盖一夜只剩心跳那一种周期唤醒（`suspend_stats` 和日志）。
- 真机上的挂起：基带（来电、短信）和按键是不是板子的唤醒源、深睡时通知灯还闪不闪（PMIC 的灯自己闪就闪，内核的定时器触发器随 CPU 停下），要板子。6T 的唤醒源是读内核源码列的、一夜掉多少电、睡着时来电和短信几秒响、电源键到亮屏几秒，都没量过（[39](39-oneplus-6t.md) §14.19，**未上真机**）。
- 6T 上插上充电器能不能把手机从挂起里叫醒、叫醒后多快亮屏（§1「插上电源」），**未上真机**；按住电源键多少秒复位（§7），没量过。
- 接近传感器（通话时贴脸息屏）**未实现**（[11](11-platform-architecture.md)）。
- 只读 RTC 的偏移和开机设时钟（§6）**未上真机**：RTC 关机、重启时是不是一直在走，偏移存得对不对，不联网重启后时间对不对，都要在 6T 上看。
- 关机充电（§10）**未上真机**（QEMU 上 ✓，熄屏挂起 QEMU 上没有）：低电量不让开机、熄屏挂起、充电电流都没在 6T 上看过。
- 只有电源键和音量键时（没接键盘的 6T），除了电源菜单和快速选卡（§7），别的系统界面都答不了，**未实现**：来电（电源键、音量键只让它静音，接不了也挂不了）、闹钟（短按电源键能稍后提醒了（§7），停止还要触屏或键盘）、紧急警报和闪信、SIM 卡 PIN 码、锁屏 PIN 码（设了 PIN 就解不了锁）、防盗锁、USB 信任电脑和蓝牙配对的确认、应用无响应的对话框。触屏（另做）或按键约定（像闹钟响时短按电源键算稍后提醒那样）可以补上。
## 10. 关机充电（充电器把关着的手机开起来）

> 状态：开发机上单元测试 ✓、生成器的测试 ✓，QEMU 上 ✓（`make yocto-charger-test`，2026-11-26：生成器选了充电目标、充电画面、系统没起、5 秒熄屏、电源键亮屏、拔掉模拟的充电器 3 秒后关机）；低电量不让开机、到电量自己开机、「已充满」和充电电流：开发机上单元测试 ✓（假的 sysfs），QEMU 上 ✓（同一个检查的两次启动，见下面「测试」）；熄屏挂起：开发机上单元测试 ✓（假的 sysfs 和假的挂起），QEMU 照 Shell 不挂起 ✓，真的挂起**未上真机**；以上都**未上真机**（6T 上要查的见 [39](39-oneplus-6t.md) §14.10「关机充电」）。

**为什么**：关着的手机插上充电器，PMIC 把它开起来（插上充电器是一个开机原因），由引导程序决定起什么。一加 6T 的 ABL 设着 `off-mode-charge:1`、`charger-screen-enabled:1`，这时在命令行里加 `androidboot.mode=charger`（实测，[39](39-oneplus-6t.md) §16.9；正常开机是 `androidboot.mode=normal`），Android 照它只起充电画面。Kario 以前不看这个参数，插着线关机、关着插线，起来的都是整个系统：用户看到的是「关机以后又开机」。

**怎么认出来**：`phone-charger-generator`（systemd 的生成器，`/usr/lib/systemd/system-generators/`，phone-fw 装）。开机时 systemd 在加载任何单元以前跑它，它读 `/proc/cmdline`，里面有 `androidboot.mode=charger` 就在生成器的早期目录里把 `default.target` 指向 `phone-charger.target`，把 `bluetooth.target` 指向 `/dev/null`，往内核日志写一行 `phone-charger-generator: androidboot.mode=charger: booting phone-charger.target`。它前后还有别的 `androidboot.mode=` 也一样算：报告 008 里充电器开的那次开机，6T 的 ABL 先写了 `androidboot.mode=charger`、后面又写了 `androidboot.mode=normal`，生成器以前取最后一个，起来的就是整个系统（2026-11-27 改，**未上真机**）。命令行里的 `systemd.unit=` 仍然优先（systemd 自己的规矩：生成器只改 `default.target`，有 `systemd.unit=` 时 systemd 不看它；开发者要进别的目标时）。不放在 initramfs 里：initramfs（`slot-init`）只负责找到根、挂上，QEMU 和 A/B 布局各自不同；起哪个目标是 systemd 的事，生成器在每种布局、每块板子上都一样，也能在开发机上直接测（`SYSTEMD_PROC_CMDLINE` 代替 `/proc/cmdline`，和 systemd 自己的生成器一样）。

**从电源菜单关机**（报告 008，2026-11-27，**未上真机**）：插着线在电源菜单里点了「关机」，上一次启动的 journal 里走的却是 `systemd-reboot.service`，起来的是整个系统；再关一次才真的关机，充电器又把它开起来，这次命令行里两个 `androidboot.mode=`，照上面的老规矩又进了整个系统。查过：「关机」那一项只调 `systemctl poweroff`，镜像里没有把关机改成重启的东西（logind 的 drop-in 不管电源菜单，没有 `systemd-poweroff` 的 drop-in，`phone-charger` 只在关机充电时按住电源键才重启，那次没在关机充电里）；Shell 记的 `power: poweroff` 或 `power: reboot` 只在 `/run/phone/shell.log`，关机就没了，所以报告里看不出选的是哪一项。最像的原因（推断，**未验证**）：没接键盘的 6T 用手指点菜单，一行约 5 mm 高，「重启」紧挨在「关机」下面，点一下就选中，手指落得低一点就是「重启」；触摸屏的坐标准不准报告里还等用户确认。改了三处：点屏幕选「关机」「重启」要在对话框里再确认一次（§7「电源菜单」）；这一行现在进 journal，下次看得出选了什么、怎么选的；真关机以后充电器开机时 ABL 写两个 `androidboot.mode=`，生成器认 `charger`（上面），进关机充电。

**起什么**：`phone-charger.target` 只要 basic.target（udev、journald、挂载、电源和输入的驱动）加三个服务，不要 multi-user.target，所以 Shell、telephonyd、ModemManager、iwd、应用、推送都不起：

| 单元 | 做什么 |
|------|--------|
| `phone-charger.service` | `/usr/bin/phone-charger`（kario-system 里的 `programs/chargercmd`），挂了 2 秒后重起 |
| `phone-charger-radios.service` | 在 udev 的冷插以前把 `/usr/lib/phone/charger-modprobe.d/` 的黑名单拷进 `/run/modprobe.d/`：6T 上是 `phone-qcom-charger` 装的 `qcom_q6v5_mss`（基带的远程处理器，驱动一加载就启动基带）和 `ath10k_snoc`（WCN3990 的 Wi-Fi）。目录空着（QEMU）就不跑 |
| `phone-charger-test.service` | 只在命令行有 `phone.charger-test=1` 时跑（QEMU 的检查） |

蓝牙：控制器的设备要 `bluetooth.target`，它被屏蔽了，bluetoothd 不起；`phone-qcom-btaddr` 也不跑（`ConditionKernelCommandLine=!androidboot.mode=charger`）。GPS 在基带里，跟着基带不起。所以没有一个无线电开着。

**屏幕和按键**（`programs/chargercmd`）：

- 画面用面板上的同一个渲染器（phone-paint，协议 `phone.frame/1`，[23](23-renderer-protocol.md)）：一帧待机界面，`status.charging` 让它画状态栏的闪电和日期下面充电的大电池和百分比（§1、[37](37-motion.md) §3），下面一行写电池在做什么：「正在充电，47%」「正在快速充电，47%」、电池自己说充满（`status` 是 `Full`）时「已充满」（100% 但电池还说 `Charging` 时照旧是「正在充电，100%」：电量计还在补最后一点）、「正在充电，2%，电量过低，无法开机」（下一条）、「未检测到电池」「充电器已拔出，即将关机」；读不出电量（没有 `capacity`）时不写百分比。电池报 `current_now` 时第二行是充电电流，如「1520 mA」：只写大小，不写正负（sysfs 的规定是充进电池为正，旧驱动和 `test_power` 反过来，6T 的 bq27411 是哪种**未上真机**），每秒随电源重读，亮着时跟着变；拔了线不写。提示「长按电源键开机」。语言、时区、12/24 小时制、主题、亮度照 Shell 存着的设置（`shell.LoadStandby`，和 Shell 的待机界面一样）。
- 亮 5 秒熄屏；短按电源键亮 5 秒，亮着时短按熄屏。面板、背光照 Shell 的规矩（§7）：熄屏先关背光再关面板，亮屏等渲染器回答面板开了再开背光，一次只要一个开关。
- 按住电源键 2 秒（`keys` 消息的 `power_long`，渲染器还按着时就报）：提示「正在开机…」，`systemctl reboot`，引导程序这次见到的开机原因是重启，起来的是整个系统。重启失败就留在充电画面，再按住再试。2 秒远短于 PMIC 的强制复位（§7「按住电源键不放会重启」）。
- **电量太低不开机**（`bootMinLevel`）：电量低于 3%，或者电量计自己说快没电（`capacity_level` 是 `Critical`，bq27411 的 SOCF 标志，默认约 2%），按住电源键不重启：屏幕亮起，那一行写「正在充电，2%，电量过低，无法开机」，提示改成「电量到 3% 时开机」，接着充；电量到了 3% 自己重启进系统（用户已经按住要开机了，不用再按一次）。等着时拔掉充电器照样 3 秒后关机。没有电池的板子（接着电源的开发板）从不挡；有电池却读不出电量的，只看 `Critical`，不因为读成 0 就一直不让开。为什么是 3%：电量计的 0% 已经是它在负载下的截止电压（bq27411 的 Terminate Voltage），不用再另看电压；整个系统开机（基带启动、面板、所有服务）要的电流比电脑的 USB 口给的多，差的由电池出，0–2% 的电池在开机路上就会掉到截止电压关机，PMIC 见到充电器又开进关机充电，一次次循环；3% 是 6T 的 3700 mAh 里约 110 mAh，远多于开机那几十秒用的（推断），而电量计在 0% 附近的误差也有一两个百分点（所以不用 1%）。Android 的充电程序（AOSP healthd 的充电模式）同样在电量低于机型设的门槛（`ro.charger.boot_min_cap`）时不理按住电源键，Kario 用一个数，不按板子设；6T 上开机时的电流和电压降**未上真机**，量过以后再定要不要按板子写。
- **熄屏时挂起**（`programs/chargercmd/sleep.go`，用 Shell 的 `hal/pm` 和同一个判断 `shell.OpenSleep`）：板子能挂起（主板配置的 `suspend`、内核有 `mem` 或 `freeze`、有 RTC 唤醒闹钟、有能叫醒的键）时，屏幕熄了、面板关好了（渲染器回答过、背光已关）、离上一次按键和熄屏都过了 5 秒、离上一次醒来过了 6 秒、没有正在拔线关机、充电器不在认的途中（`usb_type` 答 `EAGAIN`）、没有别人拿着唤醒锁，就挂起；RTC 每 10 分钟叫醒一次重读电量（充满了、拔了线而充电器的中断没叫醒手机，最迟 10 分钟也知道），等着电量开机时每分钟一次。电源键、充电器的插拔叫醒它：起来时把每个能叫醒手机的充电器设备的 `power/wakeup` 打开（6T 的 PMI8998 充电器的插线中断 `usb-plugin`，已经开着就不动），日志写明哪些（`sleep: mem 5s after the screen goes off; woken by the power key, the RTC every 10m0s and the chargers …`）；power_supply 的每次变化都是一个唤醒事件，所以判断要睡和真的睡之间来的插拔会让内核取消这次挂起（`wakeup_count` 的握手，§3）。醒来 6 秒内不再睡：叫醒它的那一下键、插拔的 uevent 在解冻以后才到；bq27xxx 的驱动读属性时用 5 秒内（按内核的节拍，挂起时节拍停着）读过的数。叫醒它的是电源键时，那一下短按照常点亮屏幕。QEMU（主板配置没有 `suspend`）走 Shell 的同一条路，不挂起，日志 `sleep: off (the board cannot be woken from suspend)`。挂起失败（不是被唤醒事件取消）就不再挂起，日志写明。状态文件写着 `sleep`（`mem`、`freeze` 或 `off: …`）、`sleeps`（睡了几次）、`awake`（现在为什么醒着）。
- 拔掉充电器：屏幕亮起写「充电器已拔出，即将关机」，3 秒内没插回去就 `systemctl poweroff`（线松了一下不算）。起来时就没有充电器（开机途中拔了）也一样。没有电池的板子（接着电源的开发板）从不因为拔线关机。
- 电源每变一次（power_supply 的 uevent）马上重读；收不到 uevent 时每秒读一次；能挂起的板子醒着时也每秒读一次（醒着的时间很短）。
- 日志进 journal（`journalctl -b -u phone-charger`）：每次按电源键、拔插、电量太低不开机（`battery at 2%: too low to start; starting at 3%`）、到了电量开机（`battery at 3%: starting`）、重启或关机一行，挂起和醒来各一行（`sleep: mem until …`、`sleep: woke after … (irq …)`）；当前的状态在 `/run/phone/charger`（一行 JSON：屏幕、面板、有没有电池、电量、充没充、电池自己的 `status`、`current_ua`（报了才有）、那一行字、`too_low`、`pending`（按住过、在等电量）、正在重启还是关机、`sleep`、`sleeps`、`awake`），USB 控制台上看得到（bring-up 镜像的 `phone-bringup-usb` 在 sysinit.target 里，照样起）。
- **测试**：`programs/chargercmd` 的状态机测试每条规则一个（低电量等电量开机、`Critical`、读不出电量、没有电池、等着时拔线、挂起的时机、「已充满」、电流），`sleep_test.go` 用假的挂起测握手（数过以后来的键赢）、面板没关好和唤醒锁时不睡、被取消和挂不起来；`main_test.go` 在假的 sysfs（6T 的 bq27411 和 PMI8998、RTC、`/sys/power`、能叫醒的键）上跑整个程序：2% 时按住等、到 3% 开机，「已充满」和电流，主板配置有 `suspend` 时熄屏 5 秒后往 `/sys/power/state` 写 `mem`、充电器的 `power/wakeup` 被打开，没有时照 Shell 不睡。拿掉这次的改动这些都失败。`make yocto-charger-test` 起两次：第一次在原来的检查以后看 QEMU 不挂起（`sleep` 是 `off: …`）、`test_power` 设成 `full` 和 1500000 µA 时画面写「已充满」和电流、电量设成 2% 时那一行和按住电源键（模拟的电源按钮按住 2.4 秒）以后不重启，再拔线关机；第二次（`phone.charger-test=start`，QEMU 带 `-no-reboot`）2% 时按住、改成 3%，手机自己重启。它请主机按电源键的请求和自测的一样是控制台文件的行，没按就每 10 秒再说一遍（[40](40-testing.md) §2），这样改了以后 QEMU 上**未验证**。

**另一个办法：`fastboot oem off-mode-charge 0`**，不采用。照 AOSP 对 fastboot 的规定（[Off-mode charging](https://source.android.com/docs/core/architecture/bootloader/fastbootd#off-mode_charging)），关掉以后引导程序跳过充电模式，「像用户按了电源键一样」开机：开机是 PMIC 见到充电器做的，ABL 只决定起什么，关掉它手机照样开，只是起来的是整个系统、命令行里也不再有 `androidboot.mode=charger`，Kario 连认都认不出来。2026-11-23 Nura 上「关机 2 秒后又开机」就是这样子。另外它是每台手机各自在 fastboot 里敲的一步，换手机、刷回 OxygenOS 再刷回来都可能丢，6T 的 ABL 认不认这条命令也没试过（`oem help` 是 `unknown command`，[39](39-oneplus-6t.md) §16.3）。关机充电不用动引导程序的设置，关着充电时屏幕上看得到电量，拔线就关。

**没做的**：

- 电量的大电池仍画在待机界面的位置上，只是那几行字变了，没有专门的充电画面设计（充电动画、按电量变色的大电池）。
- 电量太低时不让开机的门槛是一个数（3%），不按板子设；6T 上开机要多少电**未上真机**。
- 挂起只在 6T 这样的板子上有，**未上真机**：充电器的插拔能不能把手机从挂起里叫醒、醒来多快、bq27411 醒来以后的电量是不是新的、`current_now` 的正负，都要在 6T 上看。
- 只有 6T 的引导程序这样说；别的板子的引导程序（骁龙模块板、QEMU）不写 `androidboot.mode`，生成器什么都不做。
