# telephonyd · UI

## telephonyd

> 状态：**原型 ✓**（`telephonyd`，C++23）。两个后端：模拟基带（开发机、QEMU）和 ModemManager（对着模拟的 ModemManager 测过，**未上真机基带**，随 R16）。实现见 [19](19-core-apps.md) §2。

自研电话守护进程，**C++23**，负责通话、短信的收发（存储在 phone-smsd，[32](32-messaging.md)）和网络状态。

```
应用 ──phone.Ui req（tel.* / sms.*）──► Shell broker ──JSON 行──► phone-telephonyd ──► Modem 接口
                                          ▲                          │   ├─ SimModem（开发机、QEMU）
          系统来电界面 · 状态栏信号 · 短信通知 ◄── 事件订阅 ──────────┘   └─ MmModem ──D-Bus──► ModemManager
```

| 项 | 实现 |
|----|------|
| 语言与构建 | C++23（`std::expected`、`std::print`、`std::format`、指定初始化）；CMake；开发机用 GCC 14，Yocto 用 GCC 15 |
| 结构 | 单线程事件循环（`poll` + 定时器）：`loop`、`server`（Unix socket）、`service`（方法与存储）、`modem`（接口与模拟基带）、`mm`（ModemManager 后端）、`json`（自带的小型 JSON 编解码） |
| 选后端 | `--modem sim` / `modemmanager`；不给时读 `--sku` 指向的 `sku.json` 里主板配置的 `modem`（[24](24-sku-framework.md)），设备上的服务就这样启动：QEMU 用模拟基带，骁龙板用 ModemManager |
| 协议 | 与其他系统服务相同：每行一个 JSON，`{id, method, params}` → `{id, ok, err, result}`；客户端调用 `subscribe` 后还会收到事件行。`ussd.initiate`、`ussd.respond` 等网络答复后才回，可能排在之后请求的回复后面，按 `id` 对应 |
| 方法 | `status`、`calls`、`dial`、`answer`、`hangup`、`hold`、`mute`、`log`、`sms.send`（`to`、`text`，`id` 可选：之后的事件用这个 id）、`sms.pending`（phone-smsd 还没确认的事件）、`sms.ack`（`keys`：确认这些事件，telephonyd 放手）、`sms.legacy` / `sms.legacy_drop`（交出、删掉以前自己存的短信）、`sim.unlock`（PIN，或 PUK 加新 PIN）、`sim.pin_lock`（`pin`、`on`：开关 PIN 锁）、`sim.change_pin`（`pin`、`new_pin`，PIN 锁开着时）、`ussd.initiate`、`ussd.respond`、`ussd.cancel`（见 USSD 一行）；测试钩子 `sim.ring`、`sim.sms`（`held`：只放进待确认队列、不发事件，像存下后就崩溃）、`sim.cb`（小区广播：`channel`、`serial`、`text`）、`sim.lock`、`sim.ussd`（只有模拟基带支持） |
| 事件 | `call`（状态变化）、`sms`（收到的短信；发出的短信先报「发送中」，发完再用同一个 id 报「已发送」或「发送失败」，收到送达回执再报「已送达」；除「发送中」外都带 `key`，等 smsd 确认）、`cb`（小区广播，带 `key`）、`status`（注网、信号、SIM 卡状态与剩余次数、`pin_lock` PIN 锁开没开、`imei` 基带的 IMEI，未知时为空）、`unlock`（PIN / PUK 对不对）、`pin`（开关 PIN 锁、改 PIN 成没成）、`ussd`（见 USSD 一行） |
| USSD | `ussd.initiate`（`code`，须符合 `^[*#][0-9*#]{1,30}#$`，如 `*100#`）发码，`ussd.respond`（`text`）回复网络的菜单或提问，都返回 `{state, text}`：`state` 为 `done` 时网络结束了会话，`text` 是最后的答复；为 `input` 时网络在等回复，`text` 是提示或菜单。`ussd.cancel` 结束会话，返回 `{}`，没有会话时什么也不做。同一时间只有一个会话。错误：`invalid code`（码不合格式）、`ussd session open`（已有会话）、`no ussd session`（没有等回复的会话）、`the SIM is locked`（与拨号相同）；ModemManager 后端还有 `no modem that can send USSD`、`not registered on the network` 和 ModemManager 给的错误。事件 `{"event":"ussd","state":…,"text":…}`：网络主动发来的通知（`notify`）或提问（`input`，用 `ussd.respond` 回复），以及网络结束了等回复的会话（`done`，`text` 为空） |
| 射频与移动数据 | `radio.set`（`on`）开关射频（关就是飞行模式），`data.set`（`on`）开关移动数据，都返回 `{}`；两个开关存在 `<state-dir>/telephony/radio.json`，telephonyd 启动时在基带上网之前先用上，所以飞行模式下开机不会先注网。`status` 多了 `radio`、`data`（两个开关）、`data_state`（`connected` / `idle`：开着、还没连上 / `off`：开关或射频关着）和 `data_ifaces`（数据连接能用的网卡，Shell 关移动数据时在防火墙里挡住它们）。射频关着时拨号、发短信、USSD 回 `the radio is off`，没有来电；关射频时进行中的通话以「断开」结束，紧急呼叫中拒关，回 `an emergency call is in progress`。拨紧急号码（112、110、119、120、122、911、999，与 Shell 锁屏能拨的一样）不受射频开关限制：射频关着时 telephonyd 先给它上电再拨，通话的 `emergency` 为 true，Shell 见到就退出飞行模式。紧急号码表是写死的，SIM 卡和网络给的号码表**未实现** |
| 存储 | 通话记录（200 条）和 phone-smsd 还没确认的事件（最多 1000 条）存在 `<state-dir>/telephony/telephony.json`（0600），先写临时文件、`fsync` 再改名；以前存的短信留在里面，直到 smsd 取走（[32](32-messaging.md) §3） |
| 权限 | socket 权限 0600，只有 Shell（root）能连；应用只能经 broker |
| 模拟基带 | 已注册「中国移动 4G」，信号 3 格；呼叫中 → 振铃（0.3 秒）→ 通话中（1.2 秒）；号码以 000 结尾返回占线；来电 30 秒无人接听记为未接；每条短信 0.5 秒后报送达、0.8 秒后收到回复（10086 回话费余额），发给 999 结尾号码的发送失败，发给 998 结尾号码的发出去但没有送达回执；`sim.cb` 发一条小区广播；`sim.lock` 让 SIM 卡要 PIN，输错三次转 PUK（12345678）；卡的 PIN 起初是 1234、PIN 锁关着，开关和修改时输错也扣次数，三次后同样转 PUK；IMEI 为 860123456789014（测试用，校验位按 Luhn 算）；USSD 立即答复：`*100#` 答「话费余额 25.80 元，本月剩余流量 3.2 GB。」，`*101#` 给三行菜单「1 查话费」「2 查流量」「3 退出」并等回复，回 1 答「话费余额 25.80 元。」、回 2 答「本月剩余流量 3.2 GB。」、回 3 答「已退出。」，回别的再给一次菜单、前面加一行「输入有误，请重新选择：」，其他码答「网络不支持这个代码。」；`sim.ussd`（`text`、`request`）模拟网络主动发来的 USSD，`request` 为 true 时等回复，回什么都以「已收到。」结束；SIM 卡锁上时结束等回复的会话。运营商名、短信的自动回复、USSD 的菜单和答复是数据，不在代码里：`telephonyd/sim-network.json`，由 `--network FILE` 指定，不给时找安装前缀下的 `share/phone/sim-network.json`（设备上 `phone-telephonyd` 包装在 `/usr/share/phone/`），都没有时用英文的默认值（运营商 Kario Mobile）；C++ 测试和 ModemManager 的模拟读同一份 |
| 测试 | `telephonyd_test`：JSON 编解码、拨号状态机、占线、接听、未接、通话记录、短信回复和送达回执、待确认队列（重启后还在、确认后放手、同一条只排一次）、旧短信交出、小区广播、模拟 IMEI 的 Luhn 校验位、USSD（查询、菜单与回复、不合格式的码、没有会话时回复、取消、网络主动发来、SIM 卡锁上结束会话）、ModemManager 取值的换算；用事件循环的假时钟推进，`make build` 时运行。`make mm-selftest`：ModemManager 后端对着模拟的 ModemManager（下文） |

Shell 一侧（`shell/telephony.go`、`broker_phone.go`）：
- 保持一条订阅连接，telephonyd 重启后自动重连；没有 telephonyd 时状态栏显示「无服务」，其他功能照常。
- SIM 来电不管前台是什么，都由系统全屏接管；接听后交给电话应用；未接来电进通知中心。
- 短信不经这条订阅：phone-smsd 从 telephonyd 收下、存好，Shell 从 smsd 订阅，收到的短信以「短信」应用的名义发通知，应用在运行时还会收到 `sms` 事件（`shell/messaging.go`，[32](32-messaging.md)）。
- 没有 `phone` 权限的应用请求拨号时，系统先弹窗让用户确认；通话界面始终归电话应用。
- 发不出去的短信，短信应用标「未发出」、可以重发，系统也以短信应用的名义发通知。
- SIM 卡要 PIN（或 PUK 加新 PIN）时，系统画一个和支付确认同类的界面要码，数字只经 Shell 交给 telephonyd 和 SIM 卡，应用拿不到；输错显示还剩几次，返回键可以稍后再输（SIM 卡状态变了或有人拨号时再出现）；状态栏和待机界面显示「SIM 已锁」「无 SIM 卡」。
- 飞行模式和移动数据由 Shell 做主（`shell/airplane.go`，[11](11-platform-architecture.md) §16）：Shell 每次连上 telephonyd 都把它的两个开关对齐到系统设置；飞行模式下拨非紧急号码由 Shell 直接拒绝（「飞行模式已打开，关闭后才能打电话」）。
- 「设置 › SIM 卡锁」开关 PIN 锁、修改 PIN 码：设置应用只请求（`settings.set` 的 `sim_pin_lock`、`sim_pin_change`），PIN 码在同一类系统界面上输入（修改时输现在的、新的、再输一次新的），只交给 telephonyd 和 SIM 卡；设置应用等到结果（成了、输错了、用户取消）才收到回复。输错三次 SIM 卡自己锁上，转到要 PUK 的解锁界面。

**ModemManager 后端**（`src/mm.cpp`）：实现同一个 `Modem` 接口，经 sd-bus 调系统总线上的 `org.freedesktop.ModemManager1`（1.20 起的接口），只多依赖 libsystemd。Yocto 构建强制编进（缺 libsystemd 就构建失败，不会悄悄只剩模拟基带）；开发机没有 libsystemd 时不编。
- **跟随**：ModemManager 起来、退出（`NameOwnerChanged`），基带出现、移除（ObjectManager）；用报上来的第一个基带。基带处于禁用状态时由 telephonyd 启用它（ModemManager 自己不启用）。基带或 ModemManager 消失时，进行中的通话以「断开」结束，状态变为未注册。
- **状态**：取自 Modem 与 Modem3gpp 的属性——本地、漫游、仅短信都算已注册；运营商名；制式按 AccessTechnologies 归成 2G–5G；信号百分比折成 0–4 格；本机号码；IMEI 取 Modem 的 `EquipmentIdentifier`。有变化就发 `status` 事件。
- **通话**：`Voice.CreateCall` 加 `Call.Start` 拨出，`Accept` 接听，`Hangup` 挂断。ModemManager 没有单路保持，保持和恢复都用 `HoldAndAccept`，对单路通话它正好就是保持和恢复；通话中接第二路来电（呼叫等待）也用它。`Call.StateChanged` 换成 telephonyd 的状态；结束原因按谁挂的、接通过没有、网络给的原因，归成挂断 / 拒接 / 占线 / 未接 / 失败 / 对方挂断。结束的通话从 ModemManager 删掉。静音属于音频通路（音频服务，R29），后端只记状态。
- **SIM 卡**：卡锁着时 ModemManager 停在 LOCKED，`UnlockRequired` 说要 PIN 还是 PUK、`UnlockRetries` 给剩余次数；`sim.unlock` 调 Sim 对象的 `SendPin` / `SendPuk`，解锁后基带回到禁用状态，telephonyd 接着启用它。卡锁着时拨号、发短信直接拒绝。开关 PIN 锁调 `EnablePin`，修改调 `ChangePin`；PIN 锁开没开读 3GPP 接口的 `EnabledFacilityLocks`（第 0 位是 SIM 卡）。
- **短信**：`Messaging.Create`（要送达回执）加 `Sms.Send` 发送，先报「发送中」，`Send` 返回后报「已发送」或「发送失败」；之后 `DeliveryState` 变了报「已送达」或「发送失败」（网络还在重试时不报），再从基带删掉，等回执的最多 64 条。收到的短信要等分段收齐（状态 RECEIVED）才算到，id 由号码、短信中心时间戳和内容算出（重启后不变）；它留在基带里，phone-smsd 确认（`sms.ack`）后才删，SIM 卡那几十条的空间不会占满，任何一边重启也丢不了；telephonyd 启动前就存在基带或 SIM 卡里的短信也这样交出去。没对上已发短信的送达报告直接删掉。时间用短信中心的时间戳。
- **小区广播**：ModemManager 1.24 的 `Modem.CellBroadcast`：设频道 4352–4359、4370–4400，收 `Added` 和 `Cbm` 对象（收齐才算到），报 `cb` 事件，smsd 确认后 `Delete`（[32](32-messaging.md) §7）；**未运行测试、未上真机**。
- **USSD**：基带对象上 `Modem3gpp.Ussd` 的 `Initiate`、`Respond`、`Cancel`。`Initiate` / `Respond` 返回网络的答复后，再读一次会话的 `State`（它的 PropertiesChanged 可能晚于答复到），为 3（等用户回复）报 `input`，否则报 `done`。网络主动发来的 USSD 是 `NetworkNotification`、`NetworkRequest` 属性的变化，报 `ussd` 事件 `notify`、`input`；等回复时 `State` 回到空闲（网络结束会话），或基带、ModemManager 消失，报 `done`。基带没有 Ussd 接口、卡锁着、没注册时直接拒绝。
- **射频与移动数据**：关射频先 `Modem.Enable(false)`（ModemManager 随之结束通话和数据承载），再 `SetPowerState` 到低功耗（2）；开射频先 `SetPowerState` 到开（3），再照常启用。射频关着时 telephonyd 不再自动启用基带。射频关着拨紧急号码时先这样开，基带启用后再 `CreateCall`。移动数据开着、已注册、没有连着的承载时，`Modem.Simple.Connect` 用网络默认的 APN（`apn` 为空）连上，失败了等注册状态或开关变了再试；关掉时 `Simple.Disconnect`（全部承载）。`data_ifaces` 取基带的网络口（`Ports` 里类型为 net 的）和连着的承载的 `Interface`。模拟的 ModemManager 上的两个用例（`mobile_data`、`airplane_mode`）通过（`make mm-selftest`）；选 APN、给移动网卡配地址（DHCP 或承载给的地址）**未实现**，所以真机上开着移动数据也还上不了网（**未上真机**）。
- **不被基带拖住**：所有 D-Bus 请求都异步，事件循环不会因此阻塞；等网络的请求（发短信、接听、启用、USSD）超时 120 秒；系统总线断开时 telephonyd 退出，由 systemd 重启。
- **测试**：`make mm-selftest` 在 Debian 容器里起一条私有 D-Bus 和一个 Python 写的模拟 ModemManager（`test/mock_mm.py`，导出同样的对象、属性和信号，另带一个控制接口扮演网络），通过 telephonyd 的 socket 跑 31 个用例：启用并注册、SIM 卡里的旧短信等确认才删、IMEI、去电、占线、来电接听 / 拒接 / 未接、保持与恢复、静音、呼叫等待、发短信和送达回执、没有回执的短信留着等、网络拒发、分段短信只投递一次、没确认的短信 telephonyd 重启后还在、小区广播、信号与制式变化、USSD 查询（一次答完、菜单与回复、网络主动发来的通知与提问、取消、网络结束会话）、SIM 卡 PIN（输错、输对）、PUK、在设置里改 PIN 和关 PIN 锁（输错、改完）、通话中基带消失又回来、ModemManager 重启、错误参数。
- **未实现**：没有 SIM 卡或卡锁着时拨紧急号码（R30）；VoLTE / IMS 注册状态；真机上 ModemManager 与高通用户态（QRTR、rmtfs、基带固件）的集成，随 R16。

## UI

| 表面 | 技术 |
|------|------|
| 开源 TUI | Go + Bubble Tea（`phone.Ui` 文本渲染器） |
| 闭源 GUI | **Rust + Slint**（`phone.Ui` 像素渲染器，原型 ✓，[06](06-gui-architecture.md) §8） |
| 商店应用 | 语义应用经 `phone.Ui`（默认）；画布应用用 `canvas` 节点交图形，由系统画（例外） |

不自研 UI 语言：`phone.Ui` 是数据结构（内容 + 动作），不是新语言；系统界面用 **Slint 声明式** + 设计系统。架构见 [06-gui-architecture.md](06-gui-architecture.md)，交互语法见 [10-keyboard-interaction-model.md](10-keyboard-interaction-model.md)。
