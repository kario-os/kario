# 短信：phone-smsd、和 telephonyd 的交接、紧急警报

> 状态：**原型 ✓（模拟基带）**。系统服务 `phone-smsd`（`programs/smsd`：短信存储、会话、已读未读、删短信和会话、搜索、拦截名单和拦截文件夹、送达状态、验证码识别、紧急警报记录）、telephonyd 的待确认队列和 `sms.ack`、模拟网络的送达回执和小区广播（`sim.cb`）、旧短信一次迁移、系统的紧急警报界面和提示音、设置 › 紧急警报、短信应用（会话、送达、分条计数、搜索、删除、拦截、复制验证码、紧急警报记录）都已实现；单元测试 ✓，开发机自测 8 个用例 ✓（§10）。ModemManager 一侧（收到的短信等 smsd 确认后才从调制解调器删、送达回执、ModemManager 1.24 的 `CellBroadcast` 接口）已写好，`telephonyd/test/mm_test.py` 也跟着改了，但这次**没有运行**（要 `make mm-selftest`），**未上真机**。彩信、class 0（闪信）、短信备份**未实现**（§11）。

## 0. 结论

- **短信的存储从 telephonyd 拿出来，交给 phone-smsd。** telephonyd 只做无线一侧：发、收、送达回执、小区广播；smsd 管存下来以后的一切。这和 Android 的分层一样：RIL / 电话框架收短信、拼长短信、给网络回确认，再交给 TelephonyProvider（`mmssms.db`）存。
- **一条也不丢。** telephonyd 收到的每条短信（和每条小区广播、每条发出短信的结果）先放进自己的待确认队列（落盘），smsd 存好、同步到盘上以后用 `sms.ack` 确认，telephonyd 才放手；用 ModemManager 时，调制解调器存储里的那份也到这时才删。任何一边重启后，没确认的会再交一次，smsd 认得已经存过的（§3）。
- **不用数据库。** smsd 的存储是一个只追加的日志加一个快照：每次改动写一行、同步，攒到 256 行压成新快照；崩溃写坏的最后一行读的时候丢掉。文件 0600、目录 0700（§4）。
- **只有默认短信应用能删、能拦截。** 和 Android 只让默认短信应用写短信库一样，`sms.delete`、`sms.block`、`sms.blocked` 只给系统镜像里的短信应用；别的应用有 `sms` 权限也只能读和发，而且每条都要用户确认（§8）。
- **紧急警报压在一切之上。** 小区广播按频道分级（国家级、特别严重、严重、安珀、测试），在锁屏上也弹出系统界面，用 audiod 里单独的 `alert` 类别响（静音模式也响）并振动；国家级不能关，其他级别在设置里关（§7）。

## 1. 和 Android 对照

| Android | 做什么 | Kario | 状态 |
|---------|--------|-------|------|
| RIL / 电话框架（`InboundSmsHandler`） | 收短信、拼长短信、给网络回确认（RP-ACK），存进去才确认 | telephonyd：模拟基带，或 ModemManager（它拼长短信、回确认）；待确认队列，smsd 确认后才放手 | ✓ 模拟；ModemManager **未运行测试、未上真机** |
| TelephonyProvider（`mmssms.db`：`sms`、`threads`） | 短信和会话 | phone-smsd：日志加快照，会话按规整后的号码分 | ✓ |
| 默认短信应用（`Telephony.Sms.getDefaultSmsPackage`） | 只有它能写短信库 | 系统镜像里的短信应用（`com.phone.messages`）；不能换 | ✓（不能换成别的应用） |
| `SmsManager.sendTextMessage` + 发送 / 送达 `PendingIntent` | 发短信、结果、送达回执 | `sms.send`；`state`：`sending` → `sent` → `delivered` / `failed` | ✓ 模拟；ModemManager **未运行测试** |
| BlockedNumberProvider | 拦截名单；默认短信应用和电话应用能读写 | smsd 的拦截名单；拦截号码的短信进「已拦截」，不通知 | ✓（只管短信，电话不拦） |
| CellBroadcastService + CellBroadcastReceiver | 收小区广播、去重、分级、全屏警报、警报记录、设置 | telephonyd 收（`cb` 事件），smsd 去重、分级、存，Shell 弹系统界面，短信应用看记录，设置里开关 | ✓ 模拟；ModemManager **未运行测试、未上真机** |
| Class 0 短信（闪信） | 直接弹出、默认不存 | 不区分，当普通短信存 | **未实现** |
| 彩信（MMS） | 经数据连接收发 | — | **未实现** |
| 短信验证码（`SmsRetriever`、输入法建议） | 应用自动读取验证码 | smsd 识别验证码，通知上「复制验证码」，会话里也能复制；不交给别的应用 | ✓（只复制） |

## 2. 架构

```
 网络 ──► 调制解调器 ──► telephonyd（C++，无线一侧）
                          │ 待确认队列（telephony.json 的 pending，落盘；
                          │ ModemManager：调制解调器存储里的那份）
                          │ 事件 sms / cb（带 key）     ▲ sms.ack {keys}
                          ▼                            │
                        phone-smsd（Go，存储一侧）──────┘
                          │ 日志 + 快照（state/smsd/）
                          │ 事件 message / changed / alert；unnotified + notified
                          ▼
                        Shell：通知（复制验证码）、紧急警报界面、broker（sms.*）
                          │
                          ▼
                        短信应用（默认短信应用）、其他有 sms 权限的应用
```

smsd 是 telephonyd 的第二个客户端（另一个是 Shell）；应用只经 broker 到 smsd。

## 3. 交接：一条短信不丢

1. telephonyd 收到短信，生成事件 `{"event":"sms","key":…,"sms":{…}}` 放进待确认队列，写盘（先写临时文件、`fsync`、再改名），再广播。用 ModemManager 时 id 从号码、短信中心时间戳和内容算出来（FNV-1a），ModemManager 重启后给的同一条短信 id 不变，队列里也只放一次。
2. smsd 订阅 telephonyd，每次连上先要 `sms.pending`：smsd 不在时收到的、上次没来得及确认的都在里面。
3. smsd 存好（日志同步到盘）以后才发 `sms.ack`。存过的 id 记在最近 2000 条的名单里，再交来的只确认不再存。
4. telephonyd 收到确认才把它从队列删掉；用 ModemManager 时这时才调 `Messaging.Delete` 删调制解调器存储里的那份（SIM 卡上只有几十个位置，满了就收不了新短信）。
5. 发出短信的结果（`sent`、`delivered`、`failed`）和小区广播走同样的确认；`sending` 是 smsd 自己记的，不进队列。
6. telephonyd 以前自己存的短信（`telephony.json` 里的 `sms`）由 smsd 每次连上时用 `sms.legacy` 取走、存好，再用 `sms.legacy_drop` 让 telephonyd 删掉；做到一半崩溃也没关系，再来一次时按 id 跳过已有的。

| 谁重启 | 结果 |
|--------|------|
| smsd 崩溃，短信这时到 | 留在 telephonyd 的队列里；smsd 起来后从 `sms.pending` 拿到、存、确认（自测 §10） |
| smsd 存好、还没确认就崩溃 | telephonyd 再交一次；smsd 认得 id，只确认 |
| telephonyd 重启（模拟基带） | 队列从 `telephony.json` 读回来；smsd 重连时从 `sms.pending` 拿到（自测 §10：测试钩子 `sim.sms` 带 `held` 时只入队、不发事件，像 telephonyd 存下后就崩溃，再杀掉 telephonyd） |
| telephonyd 重启（ModemManager） | 没确认的还在调制解调器存储里，重新列出来，id 不变 |
| Shell 不在（或重连前） | smsd 照样存；没显示过的新短信和警报留在 smsd 的「未通知」名单里，Shell 一订阅就用 `unnotified` 取走，显示后用 `notified` 确认 |

## 4. 存储

- 目录 `<state>/smsd/`（0700）：`snapshot.json`（0600）和 `log`（0600，一行一个改动，带序号）。读的时候先读快照，再按序号重放日志里更新的行；最后一行没写完就截掉。
- 压缩：日志攒到 256 行就写新快照（临时文件、`fsync`、改名、同步目录），再清空日志；两步之间崩溃，重放时按序号跳过快照里已有的。
- 上限：短信 10000 条（多了丢最早的）、警报 500 条、记住的已收 id 2000 个、未通知 200 个。
- 会话按规整后的号码分：去掉空格、横线、点、括号，中国号码去掉 `+86` / `0086`（通讯录里的号码不带国家码）；带字母的发送方（公司名）不分大小写。其他国家的国家码不去，**未实现**。
- 搜索在内存里做：内容（不分大小写）、号码；联系人的名字由短信应用自己匹配（smsd 不认识通讯录）。

## 5. 发送和送达回执

- `sms.send` 先存成 `sending`，再交给 telephonyd（带 smsd 的 id，之后的事件用同一个 id）；telephonyd 不在就直接记成 `failed`。
- 送达回执：模拟网络发出 0.5 秒后报 `delivered`（号码以 999 结尾的发不出去，以 998 结尾的发出去但永远收不到回执）；ModemManager 创建短信时要回执（`delivery-report-request`），收到 `DeliveryState` 后按类别报 `delivered` 或 `failed`，网络还在重试时不报；等回执的短信最多留 64 条，多了最早的放弃。
- 状态只往前走（`sending` < `sent` < `delivered` / `failed`），晚到的、重复的回执不会把它退回去；重发（只有默认短信应用，`sms.send {id}`）从 `failed` 回到 `sending`。
- 发了 3 分钟还是 `sending`（telephonyd 中途退出，ModemManager 2 分钟就放弃），smsd 记成 `failed`。
- 分条计数（短信应用和 smsd 共用 `ui.SMSSegments`）：GSM 7 位字母表 160 字一条、拆开后每条 153；有字母表以外的字（中文、表情）用 UCS-2，70 / 67；smsd 不发超过 10 条的。

## 6. 拦截和验证码

- 拦截名单按规整后的号码记；拦截号码的短信直接进「已拦截」文件夹，标为已读，不发通知、不进未通知名单。取消拦截以后的短信照常；以前的还在「已拦截」里。
- 验证码：4–8 位数字（或用一个空格 / 横线隔开的两段，如 `123 456`），附近要有说明验证码的词，取离词最近的一个，最多隔 40 个字。金额（前面是货币符号、后面是「元」或小数）、年份、时间（冒号）、数量单位、超过 8 位的号码不算。这些词和符号是中文、英文、西班牙文的数据，放在 `programs/smsd/otp.json`，不写在代码里。
- 识别出的验证码随短信存（`code`）：通知上有「复制验证码」（Shell 自己复制，短信应用不用启动），会话里对着那条短信按 `c` 也能复制；剪贴板照旧只能由用户在输入框里粘贴。

## 7. 紧急警报（小区广播）

| 频道（3GPP TS 23.041） | 级别 | 能关吗 |
|------------------------|------|--------|
| 4370、4383 | 国家级（`presidential`） | 不能 |
| 4371–4372、4384–4385；ETWS 4352–4354、4356 | 特别严重（`extreme`） | 能，默认开 |
| 4373–4378、4386–4391、4396–4397 | 严重（`severe`） | 能，默认开 |
| 4379、4392 | 安珀（`amber`） | 能，默认开 |
| 其他（每月测试、演习、运营商、ETWS 测试等） | 测试（`test`） | 能，默认关 |

- telephonyd：模拟网络用测试钩子 `sim.cb {channel, serial, text}` 发一条；ModemManager 1.24（Yocto 层里 meta-openembedded wrynose 的 `modemmanager_1.24.2`）有 `org.freedesktop.ModemManager1.Modem.CellBroadcast`：telephonyd 设频道 4352–4359、4370–4400（`SetChannels`），收 `Added` 和 `Cbm` 对象（`State`、`Text`、`Channel`、`MessageCode`、`Update`），smsd 确认后 `Delete`。ModemManager 一侧**未运行测试、未上真机**；调制解调器的小区广播支持因型号而异。
- smsd 去重：同一频道、同一序列号（消息码 + 更新号）24 小时内只算一条；序列号变了（警报更新）算新的。全部警报都存，不管级别开没开。
- Shell：级别开着就弹系统界面 `alertOverlay`：锁屏上也压在最上面（锁屏关对话框时它留着），挂断键不关它，按 OK 或返回才关；屏幕亮起；audiod 新类别 `alert`（优先级最高、音量 1、静音模式也响）播 853 Hz + 960 Hz 的提示音（2 秒、半秒、半秒，循环到关掉）；振动三下。级别关着就只记录。
- 设置 › 紧急警报：每个级别一行，国家级写着「总是显示」；`settings.set` 的键 `alert_extreme`、`alert_severe`、`alert_amber`、`alert_test`。
- 短信应用 › 紧急警报：收到过的警报（级别、内容、时间、频道）。

## 8. 能力和默认短信应用

| 能力 | 谁能用 |
|------|--------|
| `sms.send` | `sms` 权限；商店应用每条都由系统写明发给谁、发什么，用户确认才发；只有默认短信应用能 `{id}` 重发 |
| `sms.list`、`sms.read`、`sms.threads`、`sms.alerts` | `sms` 权限 |
| `sms.delete`、`sms.block`、`sms.blocked` | `sms` 权限，而且只给默认短信应用（系统镜像里的 `com.phone.messages`） |

参数和结果见 [22](22-developer-api.md) §4.2；没有 `sms` 权限的应用调这些都被拒（兼容测试探针逐个检查）。

## 9. 短信应用

- 会话列表：联系人名字或号码、最后一条（发出的写「我：」，没发出去的写「未发出：」）、时间、未读数；每个会话的菜单里有「删除会话」（先确认）、「拦截此号码」（先确认）。右软键写新信息；页面菜单：搜索（`s`）、已拦截（`k`）、紧急警报（`e`）。
- 会话：两边的气泡，发出的写「发送中…」「已发出」「已送达」「未发出」；对着一条短信：重发（没发出去时）、复制验证码（`c`）、复制内容（`y`）、删除（`d`，先确认）；页面菜单：打电话、删除会话、拦截 / 取消拦截。输入框下面是分条计数（「还可输入 68 字 · 1 条」）。
- 新信息：收件人、内容、分条计数、发送；分享来的文字已经填好。
- 搜索：边打边搜内容、号码和联系人名字。
- 已拦截：拦截的号码（OK 取消拦截）、「拦截号码」、拦截号码发来的会话。
- 紧急警报：记录和每条的详情。

## 10. 验证

| 测试 | 结果 |
|------|------|
| `go test ./programs/smsd/`（存储：重启、写坏的最后一行、压缩、权限、去重、拦截、未通知；验证码；警报分级；和假 telephonyd 的交接：重启后再交、不重复、旧短信迁移） | ✓（含 `-race`） |
| `go test ./ui -run TestSMSSegments`、`go test ./shell` 里的默认短信应用、警报级别、复制验证码 | ✓ |
| telephonyd 自己的测试（待确认队列、重启后还在、旧短信交出、`sim.cb`、送达回执、`delivery()`、`stable_id()`） | ✓ |
| 自测：应用没开时来短信 → 通知 → 会话、回复、分条计数、已送达；复制验证码；删短信和会话；拦截；smsd 崩溃时到的短信；telephonyd 存下后、交给 smsd 前崩溃的短信 | ✓（开发机） |
| 自测：锁屏上的国家级警报（提示音、振动），设置里关掉特别严重后不弹，短信应用里两条都有 | ✓（开发机） |
| `make mm-selftest`（ModemManager 一侧：等确认才删、重启后还在、送达回执、小区广播） | 已改，**未运行** |
| 设备自测（`make yocto-selftest` 一类） | **未运行**；**未上真机** |

## 11. 未实现

- 彩信、群发、定时发送、草稿保存、短信备份和导出。
- Class 0（闪信）单独弹出；SIM 卡上的短信管理（只是收进来以后删掉）。
- 拦截电话（BlockedNumberProvider 也管来电）；按关键词拦截、骚扰短信识别。
- 其他国家的号码规整（只去掉 `+86`）。
- 默认短信应用不能换成别的应用。
- 紧急警报的地理围栏（4400）、多语言版本的选择、重复提醒、警报的语音朗读。
