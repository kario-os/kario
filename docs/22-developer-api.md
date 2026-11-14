# 开发者文档：能力与接口

> 面向在本平台上开发应用的厂商和个人开发者（系统名 Kario，见 58（内部文档） §1）。
> - 本文和代码一致：能力目录对应 `ui/caps.go` 和 Shell 的权限表，`shell/apidoc_test.go` 会检查每个能力都在本文里出现，新增能力不写文档，测试就会失败。
> - 标「**未实现**」的是计划。
> - 协议版本 `phone.ui/0`，还没有冻结，兼容策略见 [11](11-platform-architecture.md) §11。

## 0. 为什么在这个平台上开发

| 你得到什么 | 说明 |
|------------|------|
| **写一次界面，所有形态都能用** | 应用只描述「有什么内容、能做什么」（语义界面），焦点、菜单、软键、返回、输入法、排版都由系统负责。同一个应用不改一行，就能跑在竖屏九宫格、直板全键盘、横屏宽键盘三种机型上，也能在终端里运行 |
| **任何语言都能写** | 应用和系统之间是 fd 3 上的 JSON 行协议。Go 有 SDK；Python 不用 SDK 也只要几十行（`apps/hello-py`）；小程序用 JS 或 TypeScript |
| **省资源** | 同一个聊天应用，语义界面版在设备上占内存 66 MB、启动 1.1 秒；Web 版是 413 MB、6.7 秒（[15](15-web-vs-semantic-benchmark.md)）。低价机型也跑得动 |
| **难做的事系统替你做好** | 可信支付确认（所见即所签）、设备认证证书链、扫码只给结果、系统来电界面、推送（应用不用常驻）、共享内存音频、相册选择器。你的服务端可以用系统给的验证库核对签名 |
| **安全默认就有** | 每个应用独立 uid 和沙箱。你的数据别的应用碰不到，系统界面也不会被别的应用仿冒 |
| **好测试** | 同一份按键脚本能在开发机终端、像素渲染器和真机上跑完整链路（§10） |
| **开源** | 平台和 SDK 以 Apache-2.0 开源（58（内部文档） §3.1），可以在上面做闭源应用，不会被一家公司卡住 |

## 1. 应用由什么组成

一个应用是一个目录：`manifest.json`、可执行文件（TypeScript 应用是编好的 `main.js`，§9.2）、可选的图标 `icon.png`。

| 清单字段 | 说明 |
|----------|------|
| `id` | 反向域名，全局唯一，如 `com.example.notes`；`system.` 开头的留给系统 |
| `name` / `icon` / `icon_image` | 名称；文本界面用的单字图标；像素界面用的 PNG（应用目录内，≤ 512 KB，≤ 256×256） |
| `description` | 一句话说明 |
| `exec`、`args` | 启动的程序和参数 |
| `runtime`、`main` | `"runtime": "js"` 是 TypeScript 应用（§9.2）：不写 `exec`，系统用 `kario-jsrt` 运行包根目录下的模块 `main`（缺省 `main.js`）；包里不能有可执行文件和字节码。不写 `runtime` 是自带程序的应用 |
| `permissions` | 要用的权限（§6），没声明的能力一律拒绝 |
| `purposes` | 每个运行时权限的用途：`{"camera": "扫码付款"}`，1–80 个字，授权框里显示；声明了运行时权限却没写用途的应用装不上（§6） |
| `privacy` | 隐私标签：哪些数据发出手机、做什么用（`collects`），哪些只在手机上用（`local`），商店的「详情」页显示；写了就要和权限对得上，不写商店显示「没有提供」（[33](33-app-guidelines.md) §3） |
| `background` | 后台模式：`voip`（要有 `voip` 权限）、`audio`（正在播放时不冻结） |
| `requires` | 运行依赖，如 `["python3"]`，缺了就不显示这个应用 |
| `order` | 在主菜单里的位置 |
| `share` | 能从分享面板接收什么：`text`、`image`、`link`、`file` 的组合（§4.9） |
| `intents` | 能替别的应用做的事：`[{"action", "label"}]`，最多 8 个；`action` 是小写的名字（如 `pay`），`label` 1–8 个字（§4.10） |
| `commands` | 贡献给命令面板的入口：`[{"id", "label", "arg"}]`，最多 8 个，标签 1–16 个字。用户在命令面板里选中，系统就用 `arg` 作深链接打开应用（如电话的「最近通话」是 `log`） |
| `settings` | 应用的设置：`[{"key", "title", "type", "default", ...}]`，`type` 是 `toggle`（开关）、`choice`（选项，带 `choices`）、`number`（数字，带 `min`、`max`、可选 `step`，系统给 +/− 两个动作）或 `text`（一行文字，`max_len` 默认 64 个字，不能有换行和制表符）。由系统在「设置 › 应用设置」里统一呈现、保存并核对类型和范围，应用不用自己画设置页；用户改了以后应用收到 `settings` 事件，用 `app.settings` 读当前值 |
| `widget` | `true` 时应用可以用 `widget.set` 在待机界面放一行字（最多 32 个字，显示为「应用名：内容」，最多 3 个应用）；用户在待机界面按 ↓ 选中这一行、按 OK，系统用这一行的 `arg` 作深链接打开应用 |
| `version` | 版本号，打包时写进签名（§12） |
| `notify_actions` | 系统替应用显示推送通知（只带标题、正文的推送）时挂在通知上的动作：`[{"id", "label", "key", "input"}]`，如鸿雁的「回复」。用户在通知中心选中后，系统在后台把 `notif_action` 交给应用，不把它带到前台；带 `input` 的先弹系统的输入界面（快捷回复），文字放在消息的 `text` 里。应用自己发的通知（`notify`）在 `actions` 里带同样的动作 |
| `nfc` | 应用的非接触卡（NFC 卡模拟，[30](30-nfc.md) §6）：`[{"id", "category", "description", "aids", "requires_unlock", "express", "locales"}]`，最多 8 张；`category` 是 `payment`（只给默认付款应用、只在解锁时应答）或 `other`；`aids` 是十六进制的 AID（5–16 字节，一个 AID 只在一张卡里，PPSE 只在付款卡里）；`express: true` 的 `other` 卡可以被用户设成快捷交通卡（息屏、锁着也能刷）；`locales` 按语言给卡名。要声明 `nfc` 权限 |
| `filing` | APP 备案号（如 `京ICP备2026001234号-1A`）。商店后台检查格式，在应用页公示；在国内公开的商店里没有备案号的应用不能上架（[07](07-ecosystem-and-store.md) §4.1） |
| `lang`、`locales` | `lang`：清单里这些文字是用哪种语言写的（`zh-Hans`、`zh-Hant`、`en`、`es`，不写是 `zh-Hans`）。`locales`：同样的文字在其他语言里怎么写，按语言给出，没译的项不写：`{"es": {"name": "Notas", "description": "…", "settings": {"sort": {"title": "Orden", "choices": ["Fecha", "Nombre"]}}, "purposes": {"camera": "…"}, "commands": {"log": "…"}, "intents": {"pay": "…"}, "notify_actions": {"reply": "…"}}}`。`settings` 按设置键给 `title` 和与 `choices` 一一对应的显示名（应用存的值不变），`commands`、`notify_actions` 按 `id`，`intents` 按 `action`。系统按设备语言的回退链（§9.1）取用，主菜单、切换应用、权限框、设置 › 应用设置都显示设备语言的名字，选项也显示译名（`settings.apps` 的 `labels`）。装应用时和商店后台都核对：只能是这四种语言，长度限制和清单本身一样（名字 1–32 个字），不能翻译清单里没有声明的设置、权限、命令、意图或通知动作；商店目录的条目带上名字、简介和用途的译文 |
| `granted` | 预授权，**只对系统镜像里的应用有效**，安装器会拒绝这个字段 |

## 2. 协议

系统启动应用时把一个 Unix 套接字放在 fd 3（环境变量 `PHONE_UI_FD`），另外给出 `PHONE_APP_ID`、`PHONE_DATA_DIR`（应用自己的数据目录，也是 `HOME`）、`PHONE_LANG`（界面语言，§9.1）和 `TZ`（时区，IANA 名称，如 `America/Mexico_City`）。设备的时钟本身是 UTC；时区在应用启动时给出，应用运行期间用户换时区，应用要到下次启动才拿到新的（**未实现**：运行中通知应用）。每条消息是一行 JSON，`t` 字段区分类型：

| 方向 | `t` | 作用 |
|------|-----|------|
| 应用 → 系统 | `hello` | 第一条消息，带 `proto: "phone.ui/0"` |
| | `reset` / `push` / `replace` / `update` / `pop` | 管理页面栈：重置、压入、替换栈顶、按 `id` 更新、弹出 |
| | `toast` / `notify` | 提示条；通知（`kind: "call"` 是来电，需要 `voip` 权限） |
| | `req` | 请求能力：`req`（编号）、`cap`、`args` |
| | `pong` | 回答 `ping`，带同样的 `value` |
| | `state` | `data`：重建页面要的东西（至多 64 KB 的 JSON）。系统在内存紧张时结束了后台的应用，下次启动时原样交回（`start` 的 `data`，[17](17-app-sandbox.md) §5.3）；Go SDK 在每次离开前台时调 `OnSave` 发出 |
| 系统 → 应用 | `start` | 启动，`value` 是启动参数（深链接）；`data` 是上一个进程交来的 `state`（只在系统结束了它、用户又回来时才有；Go SDK 的 `Restore`） |
| | `action` | 用户触发了动作：`screen`、`node`、`action`、`value`、`fields`（页面上所有输入框的值） |
| | `popped` / `lifecycle` | 页面被返回键关掉；前台 / 后台 |
| | `res` | 能力的结果：`req`、`ok`、`data` 或 `err`；带文件时有 `fds`（§7） |
| | `event` / `notif_action` | 系统事件（§5）；用户在通知上的操作 |
| | `ping` | 系统检查应用有没有卡住：用户操作之后发出，应用要在处理动作的同一个循环里回 `pong`（Go SDK 自动回）。前台应用 5 秒没回，系统弹框「没有响应」，用户可以等待或关闭它 |
| | `resync` | 接管这条连接的新 Shell 发出（前一个 Shell 崩溃或重启了，应用进程没动）：应用要把页面栈重新发一遍，先 `reset` 第一页，再逐页 `push`。之前发出、还没收到回复的请求不会再有回复。Go SDK 自动处理（它记着页面栈；页面以外的状态可在 `OnResync` 里刷新），C SDK 把它交给应用 |

最小的例子（Python，不用 SDK）：

```python
send({"t": "hello", "app": os.environ["PHONE_APP_ID"], "proto": "phone.ui/0"})
for line in rx:
    msg = json.loads(line)
    if msg["t"] == "ping":
        send({"t": "pong", "value": msg.get("value", "")})
    elif msg["t"] == "start":
        send({"t": "reset", "screen": {"id": "main", "title": "你好", "body": [
            {"kind": "button", "id": "inc", "text": "点我",
             "actions": [{"id": "inc", "label": "点我", "primary": True}]}]}})
```

## 3. 界面：描述内容，不画像素

**页面（Screen）**：

| 字段 | 说明 |
|------|------|
| `id`、`title`、`body` | 页面编号、标题、节点列表 |
| `actions` | 页面动作，出现在「选项」菜单和命令面板里；`key` 是快捷键 |
| `right` | 右软键执行的页面动作 |
| `center` | 没有焦点时 OK 执行的页面动作 |
| `focus` | 初始焦点；`bottom: true` 表示从底部开始（聊天） |
| `columns` | 连续的 `tile` 项排成宫格，方向键在宫格里二维移动；`src` 是照片或视频（`media:<id>`）的格子，由系统铺满图片、不显示标题（图片应用的网格） |
| `layout` | 内容比屏幕短时放在哪：缺省靠上，`center` 居中稍偏下（音乐的正在播放、看图），`bottom` 贴底（计算器）。系统按人眼舒适的位置摆，应用不算坐标 |

**节点（Node）**：

| 种类 `kind` | 用途 | 主要字段 |
|-------------|------|----------|
| `text`、`section` | 文字、分组标题 | `text`，`style`：`dim` / `accent` / `title` / `ok` / `warn` / `big` / `display`（计算器的结果：按屏宽放到最大、右对齐） |
| `item` | 列表项 | `title`、`subtitle`、`trailing`、`badge`、`icon`；`style: bubble-in / bubble-out` 是聊天气泡，`tile` 是宫格项；`src` 可带图 |
| `input` | 输入框 | `placeholder`、`value`、`secret`、`submit`（OK 执行的动作）、`keyboard`（`number` / `phone` / `email`）、`live`（每次编辑都通知应用） |
| `button` | 按钮 | `text`，动作放在 `actions` |
| `image` | 图片 | `src`：`media:<id>`（相册照片）、`qr:<文本>`（二维码）；`rows` 是高度 |
| `media` | 实时画面（视频通话、视频播放） | `stream`、`pip`：只能写自己的通话和播放器给的流名，写别的应用的流名系统不显示 |
| `doc` | Markdown 文档（含 Mermaid 图、代码高亮、`$…$` 公式），系统排版 | `text`，链接自动变成菜单动作 |
| `web` | 网页（存量 H5），由系统的网页视图在独立进程里渲染（一个应用的所有网页共用一个进程）；方向键在链接间移动，文本框用系统输入法输入，下拉框和 `alert` / `confirm` 显示成系统界面，返回键先在页面里后退（[14](14-tui-route-and-app-framework.md) §5）；应用拿不到页面内容 | `src`：http / https 网址 |
| `progress` | 进度条 | `fraction`（0–1），`text`，`trailing` |
| `canvas` | 应用给图形、系统来画（游戏、地图、图表；[07](07-ecosystem-and-store.md) §0 的例外档）：系统把图形画成像素（一个单位一个像素），像素界面里是一张不插值放大的图，文本界面里是半块字符：一格一个单位宽、两个单位高，画布比屏幕宽时按比例取样——40 列的屏上，76 个单位宽、高是 4 的倍数的画布正好横竖都隔一个单位取一个（贪吃蛇按这个尺寸画，要在文本界面里看得见的点都落在偶数坐标上）；要读的文字放在旁边的普通节点里（各个渲染器按文字排版，搜索和自测也认得），画布上没有文字绘制，游戏的分数这类像素字由应用自己用矩形拼 | `canvas`：`w`、`h`（自己的单位，各至多 320）、`bg`、`ops`（至多 4000 个：`rect` 的 `x y w h`、`line` 的 `x y x2 y2 width`、`circle` 的圆心 `x y` 和 `r`、`dot`、`poly` 的 `pts`；`color` 是 `#rrggbb`，`fill` 填充）、`keys`：为 true 时画布聚焦后 OK、方向键、数字、字母、`*` `#` 交给应用，是 `key` 动作，`value` 是键名（`enter`、`up`、`5`…），返回、软键、挂断仍归系统 |

**动作（Action）**：
- `id`、`label`、`key`；
- `primary`（OK 执行它）；
- `group: "danger"` 或 `confirm`（系统先弹确认框）；
- `hidden`（只响应快捷键，不进菜单）；
- `input`（只用于通知上的动作）：先让用户输入一段文字，值是输入框里的提示，文字随 `notif_action` 交给应用（Go SDK 的 `OnNotifText`）。

**方向键左右**：焦点元素或页面上 `key` 为 `left` / `right` 的动作由 ← → 触发（音量的调小 / 调大、播放器的上一首 / 下一首、看图的上一张 / 下一张）；没有绑定时，← 返回上一级，→ 打开焦点项（同 OK）。宫格和输入框里方向键照旧移动。

**动作的命名约定**：几个动作 ID 绑定实体键，应用不用自己处理按键。
- `call` / `dial`：通话键（绿键）触发。先看焦点元素上的动作，再看页面动作；拨号框的 `submit` 是 `dial` 时，通话键直接拨出。
- `hangup`：挂断键（红键）触发页面上的这个动作；页面上没有时，挂断键回待机。
- 来电界面由系统画，通话键接听、挂断键拒接也由系统处理。

应用从不画像素，也拿不到别的应用的画面。图片由系统解码显示：`media:<id>` 只对能访问这张照片的应用显示（§4.6）。

**网页里的能力**：`web` 节点里的页面有 `phone.call(能力, 参数)`，返回 Promise，结果和错误与应用的 `req` 相同。请求以宿主应用的身份、来源「网页 <主机>」经 broker 处理：授权记在这个来源名下，系统界面写明是哪个网页（与小程序的 `origin` 同一机制）。只开放 `share.open`、`clipboard.set`、`location.get`、`auth.confirm`，其余返回错误。

## 4. 能力目录

请求格式：`{"t": "req", "req": 编号, "cap": 能力名, "args": {...}}`。

权限一栏：
- 「—」表示不需要权限；
- 带「运行时」的，第一次使用时会弹出系统授权框。

### 4.1 支付与安全

| 能力 | 参数 | 结果 | 权限 |
|------|------|------|------|
| `attest.get` | `{nonce, alg?}` | 设备认证：设备号、设备证书、应用、启动状态、签名；服务端用 `keystore.VerifyAttestation` 核对。`alg: "sm2"` 用设备的 SM2 密钥签、附 SM2 设备证书（国密链）；不填是 Ed25519。启动状态 `boot` 为 `unlocked` 表示机主解锁了设备（可能换过系统组件），支付类服务可以拒绝。服务端也可以把收到的认证交给信任服务核对（`POST /v1/attest/verify`，`{app, nonce, attestation}` → `{valid, model, certified, provisioned, unlocked}`：证书链连到认证方的根，并且是哪个已认证型号、在产线上哪天灌装的） | `attest` |
| | | 小程序发起的 `attest.get`、`auth.confirm`、`keystore.sign` 用的是**它自己的身份和密钥**：身份是「宿主 ID/小程序名」（如 `com.phone.miniapp/晨光咖啡点餐`），写进证书和签名摘要，服务端核对时用这个身份。宿主不能用小程序的密钥签名，小程序之间、小程序和宿主之间也不能 | |
| `auth.confirm` | `{key, prompt, payload, alg?, bio_key?}` | 系统可信确认框，用户输入密码后，用应用密钥对「显示的文字 + 载荷」签名，返回签名和证书链（所见即所签）。`alg: "sm2"` 在第一次创建这把密钥时选国密 SM2（摘要用 SM3），之后密钥一直用这个算法；不填是 Ed25519。`bio_key` 再给一把绑定指纹的密钥：用户在设置里打开了「指纹用于支付」并录了指纹时，确认框也收指纹，匹配后用 `bio_key` 签名（证明里 `key_alias` 是它、`bio: true`，证书写明指纹绑定，`keystore.VerifyConfirm` 照样验）；支付密码照旧用 `key` 签名，并顺带生成 `bio_key`（只在这时生成）；录入或删除指纹后 `bio_key` 作废，确认框退回支付密码，确认后重新生成（[31](31-lock-and-biometrics.md) §5） | `keystore` |
| `keystore.sign` | `{key, payload, alg?}` | 不需要确认的签名（如付款码）；`alg` 同上 | `keystore` |
| `scan.qr` | — | 系统扫码界面，只返回识别出的文本，应用拿不到画面 | `camera`（运行时） |

### 4.2 通信

| 能力 | 参数 | 结果 | 权限 |
|------|------|------|------|
| `push.register` | — | `{token}`：交给你的服务器，通过网关给这个设备上的你的应用发推送（[21](21-push.md)）。网关回 410 表示应用已被卸载，服务器应删掉这个令牌。数据部分建议端到端加密，网关只转发密文：用 `push.key` 取公钥交给服务器，服务器用 `push` 的 `Seal` 加密，应用收到后用 `push.open` 解开 | — |
| `push.key` | `{rotate}`（可选） | `{key}`：本应用的端到端推送公钥（X25519）。私钥由系统密钥服务保管，应用拿不到，以后放进 TEE。`rotate: true` 换一把新密钥：上一把还能解开换钥前加密的数据，再早的就解不开了 | — |
| `push.open` | `{data}`：`e1.` 开头的密文 | `{data}`：base64 的明文。系统用本应用的私钥解密；别的应用的密文解不开 | — |
| `media.call.start` | `{peer, video}` | `{call_id, local, remote}`：音视频通话的画面流名称 | `microphone`（运行时），视频另需 `camera` |
| `media.call.control` | `{call_id, mic, cam}` | 开关麦克风、摄像头 | — |
| `media.call.end` | `{call_id}` | 结束通话 | — |
| `tel.dial` | `{number}` | 拨打电话；商店安装的应用每次都由系统写明打给谁、请用户确认，声明了 `phone` 也一样（付费号码是最老的骗术）；系统镜像里的电话应用不问 | — |
| `tel.answer` / `tel.hangup` / `tel.hold` / `tel.mute` | `{call_id, on}` | 接听、挂断、保持、静音 | `phone` |
| `tel.calls` / `tel.log` | — | 进行中的通话；通话记录 | `phone` |
| `sms.send` / `sms.list` / `sms.read` | `{to, text}`（默认短信应用还能用 `{id}` 重发没发出去的）/ `{peer?, query?, folder?}` / `{peer}` | 发短信、列短信（一个会话的，号码怎么写都行；或内容、号码含 `query` 的；或 `folder: "blocked"` 拦截文件夹的；都不给就是拦截文件夹以外的全部）、把一个会话标为已读。短信存在 phone-smsd（[32](32-messaging.md)）；每条带 `thread`（会话号码）、`state`（`sending` / `sent` / `delivered` 送达 / `failed`）、`code`（识别出的验证码）。商店安装的应用发的每一条，系统都写明发给谁、内容是什么，用户确认才发 | `sms` |
| `sms.threads` / `sms.alerts` | `{folder?}` / — | 会话列表（最后一条、条数、未读数，新的在前）；收到过的紧急警报（小区广播：级别、频道、内容、时间） | `sms` |
| `sms.delete` / `sms.block` / `sms.blocked` | `{ids}` 或 `{peer}` / `{number, on}` / — | 删短信或整个会话；拦截或取消拦截号码（回拦截名单）；拦截名单。只给默认短信应用，也就是系统镜像里的短信应用（`com.phone.messages`）：别的应用有 `sms` 权限也不行（Android 只让默认短信应用写短信库） | `sms` |
| `tel.ussd` | `{code}` / `{text}` / `{cancel}` | 发 USSD 码（`*100#`）、回复网络的菜单或提问、结束会话；回 `{state, text}`：`done` 是网络的最后答复，`input` 表示网络在等回复。`*#06#` 由手机自己答 IMEI，不发给网络。网络主动发来的 USSD 作为 `tel.ussd` 事件交给电话应用。只给系统镜像里的应用：这类码能开通收费业务 | `phone` |
| `contacts.list` | — | 联系人（姓名、号码、拼音） | `contacts`（运行时） |

### 4.3 声音（[20](20-audio.md)）

| 能力 | 参数 | 结果 | 权限 |
|------|------|------|------|
| `media.list` | `{kind}`（缺省为 `audio`） | 媒体库里的条目；`kind: "image"` 列照片，需要 `photos`（运行时） | — / `photos` |
| `audio.play` | `{media}` | 播放媒体库里的一项（类别 `media`） | — |
| `audio.control` | `{id, op}` | `pause` / `resume` / `stop`，只能控制自己的流 | — |
| `audio.state` | — | 自己的流 | — |
| `audio.route` | `{route}` | `earpiece` / `speaker` | `phone` |
| `audio.stream` | `{name}` | 一路自己的声音：回复带一块共享内存环形缓冲区（§7） | — |
| `audio.record` | `{name}` | 录音：回复带环形缓冲区，麦克风的声音写进去；只在前台或通话中可用，离开前台即停止，状态栏有指示 | `microphone`（运行时） |

### 4.4 时间

| 能力 | 参数 | 结果 | 权限 |
|------|------|------|------|
| `alarm.set` | `{id, time: "HH:MM", label, daily, on}` | 系统闹钟：应用没运行也会按时响 | — |
| `alarm.cancel` / `alarm.list` | `{id}` / — | 取消；列出自己的闹钟 | — |

### 4.5 系统设置（仅系统应用）

| 能力 | 说明 | 权限 |
|------|------|------|
| `perm.list` / `perm.set` | 列出、修改所有应用的运行时权限（设置应用用）；每一项带 `last_used`（毫秒时间戳）和 `uses`：这项授权最近一次被用到的时间和次数；`reset`：应用 90 天没打开，系统哪天收回了这项授权（[17](17-app-sandbox.md) §1） | `system` |
| `wifi.state` / `wifi.set` / `wifi.connect` / `wifi.forget` | Wi-Fi（iwd）：状态、附近的网络（信号强的在前，`signal` 是 0–4 格，`known` 已保存，`connected`）、连上后的地址；`set` 的 `{enabled}` 开关；`connect` 的 `{ssid, passphrase}`：没保存过的加密网络要带密码，不带时回 `the network needs its passphrase`；没保存过的企业网络（802.1X）要带 `{identity, passphrase, domain}`，`eap` 是 `peap`（默认）或 `ttls`（内层都是 MSCHAPv2），或 `ttls-pap`（TTLS，内层 PAP），缺了回 `the network needs a user name, password and domain`；密码只交给 iwd 保存（`/var/lib/iwd`），用来自动重连；`forget` 的 `{ssid}` 删掉保存的网络。开发机上是模拟的网络 | `system` |
| `bt.state` / `bt.set` / `bt.pair` / `bt.forget` | 蓝牙（BlueZ）：本机名称、开关、是否在搜索、已配对和附近的设备（已配对的设备带 `phonebook`：`allowed` / `denied`，它能不能经 PBAP 读通讯录和通话记录，没问过时没有）；`set` 的 `{enabled, discover}`，或 `{address, phonebook}` 允许、收回这台设备读通讯录；`pair` 的 `{address}`：对方有屏幕（手机、车载）时系统弹框给出配对码，用户核对和对方设备上的一样才配上，回复等用户答完；耳机这类没有屏幕的设备直接配上（Just Works）；`forget` 取消配对（连同读通讯录的许可）。开发机上是模拟的设备 | `system` |
| `bt.send` | `{address}`：把一个文件经蓝牙（OBEX Object Push）发给已配对的设备。系统列出「文件」里的文件，用户选一个，调用的应用看不到它；回复 `{sent}`（文件名）等对方接收完；对方拒收回 `“…” declined the file`，用户不选回 `cancelled`。开发机上是模拟的设备 | `system` |
| `radio.state` / `radio.set` | 飞行模式和移动数据：`{airplane, mobile_data, cellular, data}`，`cellular` 是基带的射频 `on` / `off`、`data` 是移动数据链路 `connected` / `idle` / `off`（没有 telephonyd 时都是空）；`set` 的 `{airplane?, mobile_data?}`。飞行模式关掉射频（telephonyd）、Wi-Fi 和蓝牙，记住用户原来的开关，结束时恢复；飞行模式下用户可以再打开 Wi-Fi、蓝牙，下次飞行模式就留着它们；拨紧急号码时基带自己打开射频，系统随之退出飞行模式；紧急呼叫中不能打开飞行模式。移动数据关掉时 telephonyd 断开数据连接，防火墙挡住它报的移动网卡。两个开关都重启后保持 | `system` |
| `settings.get` / `settings.set` | 系统设置（铃声、媒体、提示音音量，自动键盘锁，`silent` 静音模式，`ime_fuzzy` 模糊音，`large_text` 大字号，`developer_mode`，`device_unlocked` 解锁设备、`crash_send` 把一条崩溃记录发给开发者——这两项由系统弹框确认；`theme` 主题（值是 `themes` 里的序号），`wallpaper_clear` 恢复默认壁纸（1 待机、2 锁屏、3 都恢复）；`sim_pin_lock`（1 开、0 关）和 `sim_pin_change`：系统弹出自己的 PIN 码界面，PIN 码只交给 SIM 卡，设置应用拿不到，回复等用户在那个界面上办完；`language`、`region`、`time_zone`：`languages`、`regions`、`zones` 里从 1 起的序号，0 是自动；`auto_time`（1 网络时间，0 手动）、`time`（手动时间：时区里的日期和时间，按「这个钟面从 1970-01-01 00:00 起的秒数」给，应用不用知道时区）、`clock_24h`（1 是 24 小时制）；`usb_mode`（信任的电脑拿到什么：1 文件传输 MTP，0 网络共享，默认；从下一台电脑起生效，[27](27-usb-typec.md) §3.3）；安全（[31](31-lock-and-biometrics.md) §6）：`lock_pin`（1 设置或修改，0 删除）、`fingerprint_auth`（管理指纹前先确认 PIN 码）、`fingerprint_enroll`（录入）——这三项弹系统自己的 PIN 码或录入界面，设置应用拿不到 PIN 码和指纹，回复等用户办完；`lock_grace`（息屏后多久锁定，秒）、`lock_show_content`、`fingerprint_unlock`、`fingerprint_pay`（开关）、`fingerprint_rename`（值是指纹 ID，`text` 是新名字）、`fingerprint_delete`（值是指纹 ID）；紧急警报（[32](32-messaging.md)）：`alert_extreme`、`alert_severe`、`alert_amber`、`alert_test`（1 显示、0 不显示；最高级别没有开关，总是显示））、`alerts`（各级别的名字、开关和是否锁定）、`usb`（`mode`：`mtp` 或 `network`；`sharing`：正和电脑共享）、`security`（是否设了 PIN 码、宽限时间和选项、传感器、指纹的 ID 和名字、开关、指纹暂时不能用的原因）、「关于本机」的信息、最近 10 条应用崩溃记录、`themes` 和 `theme_index`、`sim`（`state`、`pin_lock`）、`languages`（四种语言，名字用它自己的文字写）、`language` 和 `language_auto`（生效的和自动会选的）、`regions`（按当前语言排序的地区名）、`region` 和 `region_auto`（`code`、`from`：`sim` / `sku` / `default`）、`zones`（每个时区的城市名和现在的 UTC 偏移）、`region_zones`、`time_zone` 和 `time_zone_auto`（`id`、`city`、`from`：`user` / `network` / `region`）、`clock`（当前日期时间的文字、能不能手动设时间、NTP 用的是什么）；`set` 的参数是 `{key, value, text?}` | `system` |
| `system.update` | 系统更新（A/B 系统槽）：参数 `{action}`，`status` 返回当前版本、正在运行和下次启动的系统槽、安装状态；`check` 读更新服务器的 `latest.json`（分批推送：`rollout` 是已推给的设备比例（%），`paused` 暂停推送；每台设备按自己的随机分桶决定，分桶不上传），没轮到时 `held` 写明原因；`install` 把更新包流式写进没在运行的那个槽（签名不可信、不是给这台设备的都拒装；只下载两个槽里都没有的数据块），装完才回复；`reboot` 重启进新系统 | `system` |
| `settings.apps` / `settings.app.set` | 各应用在清单里声明的设置和当前值；改其中一项（`{app, key, value}`，类型和选项由系统核对），应用随即收到 `settings` 事件 | `system` |
| `pkg.install` | `{file, sha256?, auto?}`：调用方数据目录里的 `.phoneapp` 包，或从已装版本起的增量包（系统用已安装的文件补齐）。系统验签、检查清单（给了 `sha256` 还核对整包摘要），再弹框列出开发者和权限，用户确认后安装或更新（§12）。`auto: true` 是商店在后台装的更新：不改变用户同意过的东西（已安装、同一开发者、版本更高、不多要权限、应用不在前台也没在后台工作）时不弹框就装，否则回 `the update needs the user` | `install` |
| `pkg.revocations` | `{data}`：商店签名的下架清单（JSON）。系统按信任的商店根验签，比已有的旧就忽略；清单上的已安装应用不再能启动，正在运行的结束 | `install` |
| `pkg.catalog` | `{data}`：商店签名的目录（JSON）→ `{store, issued, apps}`。系统按信任的商店根验签，签发超过 7 天、或比上次从同一商店收到的旧就拒绝；`apps` 是目录里的应用列表，分批发布中的应用按这台设备在不在这一批换成它该拿的版本（[07](07-ecosystem-and-store.md) §4.1） | `install` |
| `pkg.uninstall` / `pkg.list` | `{id}` / —：卸载（系统确认，数据一起删除）；列出已安装的包 | `install` |

### 4.6 照片与视频

| 能力 | 参数 | 结果 | 权限 |
|------|------|------|------|
| `media.list` | `{kind: "image"}` / `{kind: "video"}` | 照片 / 视频（视频带 `len_ms`、`mime`：`video/webm`） | `photos`（运行时） |
| `video.open` | `{media, at_ms?}` | 系统解码播放一段视频（WebM：VP8 / VP9 视频，Opus / Vorbis 声音），返回 `{player, stream, state, pos_ms, duration_ms, w, h, sound}`：把 `stream` 写进 `media` 节点。声音走系统音频（类别 `media`，听从音频焦点），画面跟着声音走。应用拿不到文件和像素 | 能看到这段视频（同照片） |
| `video.control` | `{player, op, at_ms?}` | `pause` / `resume` / `seek`（到 `at_ms`）/ `state` / `close`，返回同上的状态（`state`：`playing` / `paused` / `ended`）。流不在应用的前台界面上时系统自动暂停，回来时继续；最后一个写着它的界面关掉时系统关掉播放器 | — |
| `wallpaper.set` | `{media, target}` | 把一张照片设为待机（`idle`）、锁屏（`lock`）或两者（`both`）的壁纸；系统存一份副本，删掉照片壁纸还在。系统镜像外的应用先由系统问用户 | 能看到这张照片 |
| `camera.capture` | — | 系统取景框，用户按 OK 拍照；照片存进系统相册，返回这张照片的条目（`id` 形如 `media:p4`） | `camera`（运行时） |
| `media.pick` | `{kind: "image"}` | 系统选择器，用户选中的那一张以**只读文件**随回复交给应用（§7），同时返回条目（含 `mime`：`image/png` 或 `image/jpeg`） | — |
| `media.save` | `{data?, src?, name?}` | 把一张图存进系统相册：`data` 是应用手里的 PNG / JPEG（base64，如下载来的图），`src` 是系统替它显示的图（`gen:` 图片）。系统先解码校验，再弹框写明来源和尺寸，用户同意才存；返回条目。应用之后看得到自己存的这一张，看不到相册里的其他照片 | — |
| `app.settings` | — | 读自己在清单里声明的设置的当前值（没改过的是默认值） | — |
| `widget.set` | `{text, arg}` | 设置待机界面上自己那一行；空字符串去掉。`arg`（最多 256 字节）是用户选中这一行按 OK 时的深链接，应用收到的 `start` / `open` 就带它。清单里要有 `"widget": true` | — |
| `media.delete` | `{id}` | 删除一张照片：系统先弹确认框，应用跳不过这一步 | `photos`（运行时） |

- 照片和视频属于系统，不属于拍它的应用。
- 应用能看到的只有：自己拍的、用户在选择器里交给它的；持有 `photos` 权限的，可以看全部。
- 图片节点写 `src: "media:<id>"`，由系统解码显示。

### 4.6.1 网页节点

| 能力 | 参数 | 结果 | 权限 |
|------|------|------|------|
| `web.control` | `{screen, node, op, text?, value?}` | 控制自己的 `web` 节点：`reload`、`back`、`forward`、`stop`，`zoom`（`value` 0.5–3），页内查找 `find`（`text`）、`find_next`、`find_prev`、`find_end`，`clear_data`（清掉本应用的网站数据：Cookie、缓存、本地存储），`sites`（系统界面列出存着数据的网站，用户选一个删掉；应用拿不到列表）。页面的状态经 `web` 事件来 | — |

页面里的下载完成后进系统的「文件」区（最大 16 MB），系统提示并发通知，`<input type=file>` 由系统的文件选择器交出一个文件，`prompt` 显示成系统界面；应用清单里的 `content_blocking` 设置打开时，网页进程按系统的规则拦截广告和跟踪脚本（`share/phone/web/blocklist.json`）。参考：浏览器应用（`apps/browser`）。

### 4.7 位置与后台任务

| 能力 | 参数 | 结果 | 权限 |
|------|------|------|------|
| `location.get` | — | `{lat, lon, accuracy_m, at}`：只有坐标，地名请应用自己换算；只给前台应用，使用时状态栏显示「● 位置」。当前是模拟定位（北京），读调制解调器的卫星定位**未实现** | `location`（运行时） |
| `job.schedule` | `{id, every_min, charging?}` | 后台定时任务：每隔 `every_min` 分钟（至少 15）系统唤醒或在后台启动应用，发 `job` 事件。每次运行有个时间窗口：从到期起，到半个周期后（最多 30 分钟）截止。系统不会为了没到截止的任务唤醒手机：有任务到截止时，所有窗口已开的任务在同一次唤醒里一起跑；刚收到推送（射频醒着）时也顺带跑。`charging: true` 的任务只在接着电源时跑，从不单独唤醒，插上电源后运行；它们可以做重活（商店的自动更新），每次有 1 分钟才被再冻结，一般的唤醒是 3 秒。和推送共用每小时的唤醒预算，超出预算的等应用回到前台再给。同一个 `id` 再登记就替换旧的；重启后仍然有效 | — |
| `job.cancel` | `{id}` | 取消自己的任务 | — |

后台任务里拿不到位置（定位只给前台）。参考天气应用（`apps/weather`）：前台定位，后台按上次的位置刷新。

### 4.8 剪贴板

| 能力 | 参数 | 结果 | 权限 |
|------|------|------|------|
| `clipboard.set` | `{text}` | 复制文字（最多 16 KB），系统提示「已复制」 | — |

- 剪贴板**只能写，不能读**，所以应用没法在后台偷看剪贴板。
- 粘贴由系统完成：输入框获得焦点时，系统菜单的第一项是「粘贴「…」」（按 `v`），文字直接进输入框，应用只收到输入的结果。
- 10 分钟后剪贴板自动清空。

### 4.9 分享

| 能力 | 参数 | 结果 | 权限 |
|------|------|------|------|
| `share.open` | `{kind: "text", text}`、`{kind: "image", media}`、`{kind: "link", url, text}`（`text` 是标题）或 `{kind: "file", file}` | 系统的「分享到…」面板，列出清单里声明能接收这类内容的应用；用户选中后返回 `{shared: true}`，取消则报 `cancelled`。接收方用深链接 `share:<kind>:<内容>` 打开；分享的照片和文件只授权那一个给它 | — |
| `file.save` | `{name, data}`（base64，最大 16 MB） | 把一个文件存进系统的「文件」区（文档、附件），系统先问用户；返回条目 `{id, name, mime, size}` | — |
| `file.pick` | `{types?}`（扩展名或 MIME 前缀） | 系统列出「文件」区里的文件，用户选中的那一个以只读文件随回复交给应用 | — |
| `file.open` | `{id}` | 打开交给过这个应用的文件（分享给它的、它选过或存过的），只读文件随回复交出 | — |

- 发起方只知道分享成功与否，不知道用户装了哪些应用、选了哪个。
- 分享的照片必须是发起方自己能看到的；分享出去时，系统把这张照片授权给目标应用。
- 目标应用被打开（或收到 `open` 事件），深链接是 `share:text:<文字>` 或 `share:image:media:<编号>`；文字最多 4 KB。
- 参考：图片应用分享照片给鸿雁（用户再选会话）；天气分享文字给短信（用户自己填收件人）。

### 4.10 应用间调起

| 能力 | 参数 | 结果 | 权限 |
|------|------|------|------|
| `intent.invoke` | `{action, payload?, what?}`：`payload` 是给处理方的数据（最多 4 KB），`what` 是一句说明（最多 64 字） | 清单里声明了这个 `action` 的应用（不含自己）只有一个时直接打开它，有几个时系统列出来让用户选（选择菜单上标着「<发起方>请求<what>」）。处理方答复后返回 `{app, ok, result}`，并把发起方带回前台；用户取消、处理方退出或 5 分钟没答复报 `cancelled`，没有应用能处理报 `no app handles this` | — |
| `intent.reply` | `{id, ok, result?}`（`result` 最多 4 KB） | 处理方的答复，只有请求交给的那个应用能答，答一次 | — |

- 处理方被打开（或收到 `open` 事件），深链接是 `intent:` 加上 `{id, action, payload, from, from_name}` 的 JSON：`from` 是发起方的应用 ID，系统填的，冒充不了。
- 要不要做、怎么跟用户确认，是处理方自己的事：付款这类要用系统的可信确认（§4.1），用户看到的金额和收款方来自处理方，不是发起方。
- 发起方只知道哪个应用答了、答了什么，不知道用户还装了哪些能处理的应用。
- 参考：鸿雁的「转账」请飞钱付款（`action: "pay"`，`payload: {to, amount, memo}`，金额以分计），飞钱显示「<发起方>请求付款」，经可信确认付款后答复 `{bill, amount}`，鸿雁在对话里记下「已转账」。

### 4.11 NFC 卡模拟和读卡（[30](30-nfc.md)）

| 能力 | 参数 | 结果 | 权限 |
|------|------|------|------|
| `nfc.reply` | `{session, apdu}`：十六进制的回答，以状态字结尾（`9000` 成功） | 回答 `nfc.apdu` 事件里最近的那条命令；只有命令交给的那个应用能答，读卡器走了以后再答报错 | `nfc` |
| `nfc.state` / `nfc.set` | `set` 的 `{key, on?, app?, card?, by?, name?, seconds?}`：`enabled`（`on`）、`default_payment`（`app`）、`foreground_payment`（`on`，用打开的付款应用付款）、`express`（`card`，空为无）、`move`（`card`、`by` 为 -1 或 1）、`locked`（`card`、`on`：这张卡息屏、锁着时能不能用；付款卡和 `requires_unlock` 的卡不能开）；卡包的门禁卡：`read`（读卡模式等 `seconds` 秒，至多 60）、`cancel_read`（马上停掉正在等卡的 `read`，它答 `none`）、`add_door`（`name`，加刚读到的卡）、`rename`（`card`、`name`）、`delete`（`card`）；`present`（`card`，由系统出示一分钟并显示倒计时，空为停止）、`open_cards`（打开卡包） | 控制器、是不是模拟的、卡按顺序的列表（门禁卡只给 UID 的末两位，带常用时段 `usual`：`{weekend, from, to}`，一天里的分钟，`weekend` 是休息日：周末和地区节假日日历里的节假日，调休上班的周末不算；每张卡的 `locked`、`locked_fixed`：息屏、锁着时能不能用，能不能改）、此刻门禁读卡器先拿到的门禁卡 `door_first`、默认付款应用和有付款卡的应用、快捷交通卡；`read` 的回答另有 `read`：`{result, uid?, name?}`，`result` 是 `ok`、`none`、`encrypted`、`smart`（CPU 卡）或 `duplicate`（`name` 是它在卡包里的名字） | `system`（卡包、设置） |
| `nfc.read` | `{seconds?}`：等卡多久，默认 20 秒，至多 60 | 读卡模式（Android 的 `enableReaderMode`、iOS 的 Core NFC）：系统弹出读卡界面（「把卡贴在手机背面」，写着是哪个应用要读，返回键停止），读到卡就答 `{uid, atqa, sak, iso_dep, session?, until?}`（十六进制）；ISO-DEP 的卡（CPU 卡）在控制器能留住它时有 `session`，`until`（Unix 毫秒）之前可以用 `nfc.transceive` 和它交换命令，界面这时写「请让卡停在手机背面」。没有卡报 `no card was held to the phone`，用户按返回或应用关掉会话报 `cancelled`；只有前台应用能读，同一时间只有一个读卡 | `nfc_reader`（运行时） |
| `nfc.transceive` | `{session, apdu?, close?}`：给会话里的卡发一条命令（十六进制，4 到 4096 字节）；`close` 结束会话（有命令时发完再结束） | `{apdu}`：卡的回答，以状态字结尾。会话最长 30 秒（`ui.NFCReaderHold`），卡拿开、用户按返回、应用 `close` 或离开前台时结束，读卡界面随之关掉；之后再发报错 | `nfc_reader`（运行时） |

- 读卡器选中应用某张卡的 AID 以后，每一条命令（包括 SELECT）都作为 `nfc.apdu` 事件交给它；冻结的应用被解冻几秒，不在运行的在后台启动，不占推送的唤醒预算。读卡器每条命令只等一会儿，应用要马上答。
- 读卡器用完应用的卡（场没了、休眠或 DESELECT，或者选了别的 AID）时，还在运行的应用收到 `nfc.deactivated` 事件（Android 的 `onDeactivated`），可以在这里重置自己的命令状态；已经结束的应用不会为它启动。
- 应用看不到卡包里的其他卡，也看不到门禁卡的 UID；哪张卡什么时候应答由系统决定（[30](30-nfc.md) §3、§4）。
- 读卡模式和卡模拟分开：读到的是贴在手机上的实体卡，应用拿到的 UID 就是那张卡的（卡包复制的门禁卡的 UID 从不给应用）。只有模拟控制器能留住 ISO-DEP 的卡做会话；NCI 控制器上读卡只给 UID，没有 `session`（**未实现**，[30](30-nfc.md) §8）。
- 参考：飞钱的交通卡（闸机读余额、扣费）和付款卡（收银台选 PPSE、付款）；飞钱用读卡模式读实体交通卡的余额（`SELECT` 卡的 AID，再 `80 5C 00 02 04`）。

## 5. 事件（`event`）

| 名称 | 值 | 什么时候 |
|------|----|----------|
| `open` | 深链接，同 `start` 的参数 | 应用已在运行，又被要求打开某处（如待机时按数字，电话应用收到 `dial:<号码>`） |
| `push` | `{id, kind, data, sent, notified, collapse_id}` | 推送的数据部分；系统可能为此把冻结的应用恢复几秒，或在后台启动它。`notified` 为 true 表示系统已经显示了这条推送的通知（应用在前台、或用户关了它的通知时系统不显示），应用就不要再发自己的通知 |
| `push.key_changed` | — | 设备解锁或重新上锁，密钥服务清空了全部应用密钥：推送密钥是新的，旧公钥加密的数据解不开。用了端到端加密推送的应用要重新 `push.key`，把新公钥交给自己的服务器。冻结的应用会为此恢复几秒，没在运行的不会被启动（它们启动时本来就取钥） |
| `focus` | `lost` / `gained` | 系统的菜单或对话框盖到应用的页面上、或者息屏了（`lost`），按键又归应用了（`gained`）；对应安卓的 `onWindowFocusChanged`。应用还在前台，生命周期不变；游戏在 `lost` 时暂停（贪吃蛇就这样做） |
| `audio` | 流的状态 `{id, state, pos_ms, …}` | 自己的流变化（播完是 `ended`） |
| `media` | `next` / `previous` | 用户按了媒体键（耳机线控、键盘），发给正在播放的应用；播放 / 暂停由系统直接处理 |
| `settings` | `{key, value}` | 用户在「设置 › 应用设置」里改了这个应用声明的设置 |
| `web` | `{screen, node, url, title, loading, can_back, can_forward, zoom}` | 自己的网页节点的状态变了（打开了新页面、加载完、缩放） |
| `alarm.fire` | 闹钟编号 | 自己的闹钟响了（系统已经显示全屏闹钟） |
| `job` | 任务编号 | 后台定时任务到期（§4.7）；应用可能刚被唤醒，几秒后会再次冻结 |
| `sms` | 短信 | 收到短信，或发出的短信状态变了（`state`：`sending` / `sent` / `delivered` / `failed`，同一个 `id`），或短信被删、被标为已读（值为 `{}`）（`sms` 权限） |
| `tel.call` / `tel.open` | 通话 / 通话编号 | 电话应用专用 |
| `call.resume` | 通话编号 | 用户从系统菜单「返回通话」 |
| `nfc.apdu` | `{session, card, aid, apdu}` | 读卡器对自己的卡发来一条命令（十六进制），用 `nfc.reply` 回答（§4.11，`nfc` 权限） |
| `nfc.deactivated` | `{session, card, reason}` | 读卡器用完了这张卡：`reason` 是 `link_loss`（场没了、休眠或 DESELECT）或 `deselected`（选了别的 AID）（§4.11，`nfc` 权限） |

## 6. 权限与后台

| 类别 | 权限 | 说明 |
|------|------|------|
| 运行时（第一次使用时询问，用户可在设置里撤销） | `camera`、`microphone`、`contacts`、`photos`、`location`、`nfc_reader`（读贴在手机上的 NFC 卡） | 授权框由系统画，应用无法仿冒；商店安装的应用 90 天没打开，这些授权由系统收回，下次再问 |
| 声明即有 | `keystore`、`attest`、`notifications`、`voip`、`net`、`phone`、`sms`、`nfc` | 商店自动审核只拒绝 `system`、`install` 和预先授权，新应用和多要权限的更新进人工审核；`phone`、`sms` 也不让商店应用自己打电话、发短信：每次都由系统请用户确认（§4.2）。运行时权限要在清单的 `purposes` 里写明用途，授权框里显示，审核台核对 |
| 仅系统应用 | `system`、`install`（应用商店） | 安装器拒绝声明了它们的包；已安装的应用即使声明了也拿不到 |

后台规则（[17](17-app-sandbox.md) §5）：
- 离开前台 10 秒后进程被暂停（冻结）。
- 内存紧张时系统可能结束后台的应用（不算崩溃）。用户回来时它在原来的页面上：系统先显示原来的页面，新进程启动时收到离开前台时交上来的 `state`（Go SDK：`OnSave` 返回要存的东西，`Restore` 读回来），照着重建页面；屏幕 ID 不变的页面，输入框里没发出的字和焦点由系统还原。
- 例外：`voip` 后台模式；正在播放的 `audio` 模式；通话中。
- 要在后台收消息，请用推送：只带标题、正文的推送由系统显示，不唤醒应用；带数据的推送按预算短暂唤醒应用（每个应用每小时最多 20 次）。服务器发推送时带 `collapse_id`，同一个键的新推送会替换还没送到的旧推送和它的通知（未读数、比分这类只要最新值的内容）。
- VoIP 推送唤醒应用后，应用要在 10 秒内报告来电（`kind: "call"` 的通知）；24 小时内 3 次没报告，之后的 VoIP 推送按普通数据推送处理。
- 推送带 `priority: "normal"` 时，数据不立刻唤醒冻结的应用，等应用因为别的原因醒来（回到前台、别的唤醒）时一起交付，最迟 15 分钟后系统统一唤醒一次。
- 应用在通话中（VoIP 或电话）开的音频流和录音都算「通话」类别：通话时有优先权，离开前台也不会被停（[20](20-audio.md)）。
- 通知：每个应用每分钟最多 5 条带横幅和提示音，超出的只进通知中心；通知中心每个应用最多留 24 条，最旧的先删。用户可以在「设置 › 应用权限」里关掉一个应用的通知（`notifications` 和网络一样只有「允许」「关闭」两档），关掉后它的通知直接丢弃，来电不受影响。
- 通知上的动作（快捷回复）：通知中心里选中应用的动作，系统在后台把它交给应用（冻结的先解冻，不在运行的在后台启动，不占推送的唤醒预算），然后清掉这条通知。
- 要定时同步，请用后台任务（§4.7），它和推送共用同一份唤醒预算。
- 参考鸿雁的做法（[21](21-push.md) §4）：不常驻；上线时把推送令牌交给自己的服务器；对方的消息作为「通知 + 数据」推送下来，并带会话 `thread`；来电用 VoIP 推送。

## 7. 文件与共享内存（fd 传递）

有些能力的回复会附带打开的文件，消息的 `fds` 字段写明有几个：

| 能力 | 附带的文件 | 你能做什么 |
|------|------------|------------|
| `audio.stream`、`audio.record` | 一块 memfd，48 kHz 单声道 16 位的环形缓冲区，大小已经封住 | 映射后读写样本；格式见 `audio/ring.go`，Go SDK 是 `app.GoAudio` |
| `media.pick` | 选中照片的只读文件（PNG 或 JPEG，看条目的 `mime`） | 读取、上传；写不了，也接触不到相册里的其他文件。Go SDK 是 `app.CallFile` |

规则：
- **不限制每次只能带一个 fd。** 一条消息最多带 8 个，这只是防止单条消息过大的上限。真正要管住的是下面几条：
- **方向**：文件只从系统流向应用。应用发给系统的 fd 会被立即关闭，所以应用没法用塞 fd 的方式耗尽系统的文件描述符表。
- **数量**：每个应用最多同时开 4 路音频流，其中录音只能 1 路；系统服务另有总上限。应用进程自己的打开文件数受沙箱限制（1024）。
- **类型**：系统只发它自己验证过的对象，比如封住大小的 memfd、只读打开的文件。收到的一方也会再检查，比如环形缓冲区没封印就拒绝映射。

## 8. 沙箱里的限制

- 只能读写 `PHONE_DATA_DIR`。没有共享的 `/tmp`，临时文件放在 `$TMPDIR`（数据目录下）。
- 不能执行数据目录里的文件，也就是不能下载代码来运行。小程序请用宿主提供的 JS 运行时。
- 没有 `net` 权限就没有网络；有权限也不能监听端口。
- 看不到其他进程，不能调试别的进程，危险的系统调用被拦截。
- 内存上限 192 MB（不用交换），进程和线程合计最多 128 个。超过内存上限时，系统只结束你的应用，并提示用户「超出内存上限」；数据要及时存盘。Go 程序会收到 `GOMEMLIMIT=144MiB`，垃圾回收在硬上限之前就会加紧。
- 系统内存紧张时，先结束在后台的应用，最久没用的先结束；前台应用、通话中、正在后台播放的应用不会被选中。被结束的应用下次打开时冷启动，所以收到 `lifecycle: background` 时就要存盘。
- 一个页面最多 1000 个节点，多的截掉；页面栈最多 32 层，再压入就替换栈顶。标题里有「系统」一词的页面（四种界面语言里的都算，比如西班牙语的「…del sistema」），系统会在前面加上应用名，应用冒充不了系统对话框。
- 有 `net` 权限的应用，用户可以在「设置 › 应用权限 › 网络」里关掉它的网络：系统结束应用，下次在没有网络的环境里启动。
- 应用自己退出并且退出码不是 0（崩溃、panic、被信号结束）时，系统记下时间、原因和日志最后 20 行，「设置 › 关于本机 › 应用崩溃记录」里能看到。10 分钟内崩溃 3 次，系统暂时不在后台启动它（推送和后台任务留到用户打开它时再交付）。
- 离开前台约 10 秒后，整个应用连同子进程一起被冻结；在后台时 CPU 份额也比前台低。

## 9. SDK 与语言

| 语言 | 状态 | 说明 |
|------|------|------|
| Go | ✓ `sdk` | `sdk.New`、`Run`；页面：`Reset` / `Push` / `Update` / `Pop` / `Toast` / `Notify`；能力：`Call`（阻塞）、`Go`（异步，结果在应用的派发协程里解码并回调）、`GoAudio`、`CallFile`；回调：`OnStart` / `OnAction` / `OnEvent` / `OnLifecycle` / `OnPopped` / `OnNotif` / `OnSave`（离开前台时返回重建页面要的状态），`Restore` 在 `OnStart` 里读回系统结束前存的状态。能力的结果会**整体替换**传进去的变量，不和旧值合并。能力名在 `ui/caps.go`，动作 ID、生命周期、媒体种类、播放状态和操作、事件名、设置键、`ui.Cancelled(err)`（用户取消）在 `ui/names.go`：用常量，不要自己写字符串 |
| Python 或任何语言 | ✓ 直接用协议 | `apps/hello-py/hello.py` |
| 小程序（JS / TypeScript） | ✓ 宿主 `apps/miniapp`；运行时授权按「宿主 + 小程序」分开记账，每个小程序第一次使用都要用户同意，宿主的授权和预授权不会传给小程序，设置里能单独撤销。除了内置的，宿主也装签名的小程序包（同 §12 的格式，由宿主厂商的小程序商店签发），装之前和每次启动前都验签；每个小程序的存储在自己的目录里，最多 1 MB | `Page({data, render, onAction})`、`setData`；`phone.toast / navigateTo / navigateBack / getStorage / setStorage / scanCode / requestPayment / t`（界面文字，§9.1）；`ui.text / section / doc / item / input / image / button / progress`；类型声明在 `types/phone.d.ts` |
| TypeScript | ✓ 开发机（单元测试、自测）；QEMU 待跑 | 主力语言（[29](29-app-development.md)），§9.2：`kario new` / `build` / `package`；`import { reset, on, call, tr, storage } from "kario"`；类型 `@kario/sdk`（`sdk/ts`）；示例 `examples/hello-ts` |
| C / C++ | ✓ `sdk/c`（`phoneui.h`，C11，没有依赖） | `phoneui_open` / `phoneui_next`（自动回答 `ping`）/ `phoneui_send` / `phoneui_call`（同步调用能力，期间到达的消息排队）；自己有事件循环的程序用 `phoneui_try_next`（不阻塞，没有完整消息时返回 0，照样回答 `ping`）和 `phoneui_pending`（库里已经收下、`poll` 看不到的消息）；JSON 写入器 `phoneui_json_*`；`phoneui_get_string` 取输入框的值。示例 `sdk/c/examples/hello-cpp`（C++20）；镜像里的 `phone-capi` 包带 `libphoneui.so`，头文件和静态库在 `-dev` 包 |

### 9.1 界面文字和翻译

代码里只写英文（注释也是，`scripts/no-chinese.py` 在 `make test` 里检查）；界面上的字写成英文消息，英文是原文，不用目录；译文放在旁边的 `locale/<语言>.json`（`{"英文": "译文"}`）。系统提供四种界面语言：

| 语言 | 标签 | 缺的消息从哪里来 |
|------|------|------|
| 简体中文 | `zh-Hans` | 英文 |
| 繁體中文 | `zh-Hant` | 简体中文，再英文 |
| English | `en` | （原文） |
| Español | `es` | 英文 |

语言由 `PHONE_LANG` 定：Shell 交给每个应用（只有这四个值；进程没拿到时是 `zh-Hans`）。一种语言的目录缺哪条消息，就按上表的回退链往下找，最后显示英文原文，所以只译了一部分的目录也能用。带参数的消息用 `%s`、`%d`，每种语言的译文都保留同样的、顺序相同的占位符；日期这种各语言语序不同的文字，Shell 用不含占位符的版式消息（如 `{weekday}, {month} {day}`，西班牙语译成 `{weekday}, {day} de {month}`）。

用户在「设置 › 语言」里选（或选「自动」：按地区，CN 简体中文，TW、HK、MO 繁體中文，西班牙语国家西班牙语，其他英文；开发机上 Shell 自己的 `PHONE_LANG` 代替自动）。换语言时 Shell 自己的文字马上换（`i18n.SetLang`，Go 包里 `i18n.New` 的目录都跟着变），应用则像 iOS 那样重新启动：系统带着墓碑（[17](17-app-sandbox.md) §5.3）结束它们，前台的应用马上回来、回到原来的页面，后台的在用户打开时回来，新进程拿到新的 `PHONE_LANG`；正在通话或在后台工作（放音乐、VoIP）的应用不打断，到它停下后再启动时才换。渲染器和网页进程也重新启动。应用想回到原来的页面，就在 `OnSave` 里交出页面状态（设置应用就是这样回到语言页的）。

`scripts/i18n-check.py` 核对：每个调用都有简体中文译文；每种语言的每条译文都还在用、占位符一致；逐个目录报告繁体中文和西班牙语译了多少、缺多少（缺的暂不算错，`--strict zh-Hant` 才算）。Shell、设置应用、终端前端（`programs/shellcmd`）和其余系统应用、小程序宿主和内置小程序、示例、渲染器、网页视图、输入法、沙箱自检、USB 共享、兼容测试应用、飞钱（`apps/wallet`）、TypeScript 的计算器、示例和 `kario` 的项目模板的目录都已译全；只有 `jsrt` 的测试应用（测试数据）还缺繁体中文和西班牙语，以 `i18n-check.py` 的报告为准。繁体中文和西班牙语译文是机器翻译，**还没有母语者审校**。

| 语言 | 写法 | 目录 |
|------|------|------|
| Go | `var tr = i18n.New(localeFS)`（`//go:embed locale`），`tr.T("Saved %d photos", n)`；固定一种语言用 `tr.In("es")` | 包旁边的 `locale/` |
| 小程序（JS / TS） | `phone.t("Order placed: %s", id)` | 小程序目录（或包）里的 `locale/`，随包签名 |
| Python | `T("Clicked %d times", n)`（`apps/hello-py/hello.py` 里的小函数） | 脚本旁边的 `locale/` |
| C / C++ | `tr("Hello")`（`sdk/c/examples/hello-cpp`：从应用目录读 `locale/`） | 应用目录的 `locale/` |
| TypeScript | `tr("Saved %d notes", n)`（`kario` 模块，按回退链） | 代码旁边的 `src/locale/`，`kario build` 拷进应用目录的 `locale/` |
| 清单 | 名字、描述、设置项、权限用途、命令和意图的标签是数据，用 `lang` 写的语言写，其他语言放在 `locales` 里（§1） | `manifest.json` |

小程序宿主（每个小程序自己的目录）、Python 和 C/C++ 示例、渲染器（`paint/`、`gui/`）和网页视图（`webview/`）读目录时也按这条回退链（小程序宿主和渲染器单元测试 ✓；Python、C++ 示例和网页视图用缺几条消息的目录在开发机上核对过；都未上真机）。小程序 `app.json` 里的名字和简介只有一种写法，宿主不按语言换（**未实现**）。

### 9.2 TypeScript 应用

TypeScript 是应用的主力语言（[29](29-app-development.md)）。应用只写 TypeScript：`kario` 命令行（Go 写，内嵌 esbuild，不用装 Node）把它编成一个 ES2023 模块，系统的 `kario-jsrt`（QuickJS-ng 加 `kario` 模块，经 C 接口 `sdk/c` 讲 §2 的协议）在应用的沙箱里运行它。代码只能 `import` 一个模块 `"kario"`：没有 QuickJS 的 `std`、`os` 模块，没有文件、套接字和进程；能力照旧经 broker（权限、用途、系统界面），应用的数据只经 `storage` 存取。开发机上单元测试和自测通过；QEMU 上还没跑（[29](29-app-development.md) §5）。

```bash
kario new notes            # manifest.json、src/main.ts、src/locale/zh-Hans.json、tsconfig.json、types/kario/（类型）
kario build notes          # → notes/build/：manifest.json、main.js、locale/、图标；先按安装器的规则检查清单
kario package -key dev.key -cert dev.cert -o notes.phoneapp notes   # 先编再签名打包，密钥和证书同 phone-pkg（§12）
kario package -devstore DIR -o notes.phoneapp notes                 # 用开发商店签名，开发者模式的手机认它
```

| 项目里的 | 说明 |
|----------|------|
| 清单 `"runtime": "js"`、`"main": "main.js"` | 见 §1；不写 `exec`，包里不带可执行文件 |
| `src/main.ts` | 入口。它 import 的本地文件和纯 JS 的 npm 包一起打进 `main.js`，`"kario"` 留给运行时；`fs` 这类 Node 模块找不到，构建失败 |
| `src/locale/<语言>.json` | 译文，放在代码旁边（`scripts/i18n-check.py` 在这里核对）；也可以放在项目顶层的 `locale/`，二选一。`kario build` 拷进应用目录的 `locale/` |
| `tsconfig.json`、`types/kario/` | 编辑器用的类型：`"kario"` 映射到 `@kario/sdk`（`sdk/ts`），`lib` 只有 ES2023，没有 DOM 和 Node 的接口。`kario build` 不做类型检查（那要 Node），类型错误由编辑器（VS Code 自带的 TypeScript）报 |

`kario` 模块（`sdk/ts/index.d.ts`）：

| 接口 | 说明 |
|------|------|
| `reset` / `push` / `replace` / `update` / `pop` | 页面栈，参数是 `Screen`（§3）的普通对象。运行时记着页面栈，新 Shell 接管连接（`resync`）时自动重发 |
| `toast` / `notify` | 提示条、通知 |
| `call(cap, args)` | 能力调用（§4），返回 Promise。失败时 reject 一个 `Error`：`message` 是系统给的原文，`cap` 是能力名；`cancelled(err)` 判断是不是用户取消。随回复交出的文件（`media.pick`、`audio.stream`）到不了 JavaScript（**未实现**） |
| `on(name, handler)` | `start(arg, restored)`、`action(e)`、`key(key, e)`（聚焦的画布收到的按键）、`input(e)`（`live` 输入框的每次编辑）、`popped`、`foreground`、`background`、`save()`（离开前台时返回重建页面要的状态，至多 64 KB；系统结束了进程，新进程在 `start` 的 `restored` 里拿回）、`event(name, value)`（§5）、`notif(n)`、`resync`。每种一个处理函数，新的替换旧的；没有 `key`、`input` 处理函数时，按键和编辑照常作为动作交给 `action` |
| `tr(msg, ...args)` | 界面文字：按设备语言的回退链（§9.1）查 `locale/`，`%s`、`%v`、`%d` 按顺序替换 |
| `storage.get` / `set` / `remove` / `keys` / `clear` | 键值存储，值是 JSON，合计至多 1 MB。运行时把它存在应用数据目录里（先写新文件再改名），JavaScript 看不到路径；进程被结束后还在 |
| `appId`、`lang` | 应用 ID、设备语言 |
| 全局 | `setTimeout` / `setInterval` / `clearTimeout` / `clearInterval`（跑在运行时的事件循环上，和协议的套接字一起 poll）、`console.log` / `info` / `warn` / `error` / `debug`（写到 stderr，就是系统给应用留的日志）、`queueMicrotask`、`atob` / `btoa`、`performance.now`；ECMAScript 本身的全部（`Promise`、`JSON`、`Date`、`RegExp`、`Map`……） |

- 类型 `@kario/sdk` 里的 `Screen`、`Node`、`Action`、节点种类和样式、能力名和各自的参数与结果、事件名都由 `tools/tsgen` 从 `ui/` 生成（`sdk/ts/ui.d.ts`，`make tsgen`），接口只有一个来源；Go 那边改了而没有重新生成，`make test` 就失败。
- 字节码：`kario build -bytecode` 另写一份 `main.jsc`（`kario-jsrt --compile` 预编译，带引擎的版本），运行时先用它，版本不对时用 `main.js`。只给系统镜像用（`scripts/stage.sh`）：QuickJS 的字节码格式防不住恶意的写入方，所以安装器和商店都拒收带字节码的包；装好以后在设备上预编译（像安卓的 dex2oat）**未实现**。
- 示例：`examples/hello-ts`（列表、输入框、软键、剪贴板、存储、墓碑、定时器）；开发机上 `stage.sh` 把它编好放进系统应用目录，自测有它的用例。
- 声明式框架 KarioUI（`@kario/ui`，`kario build` 打进 `main.js`）、在 Node 下跑和调试（`kario run`）、测试（`kario test`）、上架（`kario publish`）：教程 [34](34-app-tutorial.md)；系统的计算器（`apps/calc`）就是用它写的。

## 10. 测试

Shell 自带无界面的脚本运行器，同一份脚本能跑在终端、像素渲染器（`--gui`）和设备开机自测上：

```bash
make build
build/usr/bin/phone-shell --script my-app.script        # 你的用例
build/usr/bin/phone-shell --selftest                    # 系统自带的用例
build/usr/bin/phone-shell --selftest --only '视频' --junit report.xml   # 只跑名字匹配的用例，写 JUnit 报告
```

常用命令：
- 按键与输入：`key <键> [次数]`（含 `call` 通话键、`end` 挂断键）、`type <文字>`；
- 等待：`wait <文字> [毫秒]`、`wait-re <正则> [毫秒]`（会往前走的文字，如播放位置）、`gone <文字>`、`at <界面编号>`；
- 截图：`snap <名字>`；
- 系统状态：`appstate <应用> frozen|running|stopped`、`audio <类别>`、`frames <帧数>`；
- 模拟外部事件：`push <应用> <标题> | <正文> | <数据>`（扮演你的服务器）、`tel ring <号码>`（模拟来电）、`timeshift <秒>`。

完整列表在 `shell/headless.go` 文件开头。

## 11. 兼容测试（给设备厂商）

```bash
make cts     # = phone-shell --cts build/cts-report.json
```

- **跑什么**：整套自测，加上兼容测试用例。兼容测试应用 `com.phone.ctsprobe`（`cts/probe`）只在这个模式下安装，它检查：
  - 没声明权限的能力一律被拒绝；
  - 后台任务的间隔不能少于 15 分钟，能登记和取消；
  - 有电话权限的应用能自己接听和控制通话；
  - 沙箱自检的每一项（和设置应用同一份 `sandbox.SelfChecks`）；
  - 超出内存上限时只结束它自己，并提示原因；
  - 卡住时系统提示「没有响应」，可以等它恢复，也可以关闭它；
  - 崩溃后系统留下记录，设置里能看到；
  - 没有对应权限时，不能装包、不能给自己授权、不能改系统设置、不能看相册里别人的照片，不能应答 NFC 读卡器（`nfc`）、不能看和改卡包（`nfc.state` / `nfc.set` 要 `system`）、不能读 NFC 卡或给它发命令（`nfc.read` / `nfc.transceive` 要 `nfc_reader`）、不能看和开关飞行模式、移动数据，不能经蓝牙发文件（`radio.state` / `radio.set` / `bt.send` 要 `system`）；
  - 冒充不了系统界面（标题里有任何一种界面语言的「系统」一词，前面会加上应用名）。
- **灌装**：兼容测试开跑前，先模拟产线给设备灌装证书：一个代表认证方的 CA（`phone-provision`）签发设备证书，装进密钥服务（`csr` / `install_cert`），报告因此链到认证方的根，而不是设备自己生成的开发根。
- **报告**：JSON 格式，包括设备信息、每个用例的结果、每个能力的调用次数和成功、失败次数，以及从没被调用过的能力。
- **通过的条件**：所有用例通过，并且 §4 里的每个能力都至少被调用过一次。新增能力而没有补用例，兼容测试就不会通过。
- **报告有签名**：报告的 `attestation` 字段是设备密钥服务对报告摘要（去掉签名后的 JSON 的 SHA-256）做的设备认证，证书链连到根证书。厂商改动报告里的任何一项，签名就核对不过。认证方这样核对：

  ```bash
  phone-cts-verify --root root.pub cts-report.json   # 签名、证书链、结果都要通过
  ```

  `make cts` 开跑前用一个临时的 CA 模拟灌装（上一条），`phone-shell --cts` 把它的根另存为 `<报告>.root`，只用于演示；设备开机自测不灌装，根是设备第一次开机时自己生成的。量产时 CA 在认证方手里（HSM），认证方用自己手里的根核对，不信任报告里带来的任何东西。
- **认证按型号做一次**：厂商用这个型号的样机跑兼容测试，在认证方的门户上填企业名称、统一社会信用代码、型号，交上签了名的报告；认证方的后台当场核对签名连到它的根、结果是 PASS、设备没有解锁，工作人员审批后这个型号进公开的已认证名单（这部分服务不开源）。
- **每台设备只要灌装**：量产的每一台不再跑整套兼容测试，在产线工站上跑厂商自己的出厂测试，通过后联网领设备证书。工站读出设备的公钥申请（密钥服务的 `csr`），让设备对一个只用于这次请求的随机数做设备认证（应用名 `system.provision`，证明设备手里有这把密钥、没有解锁），交给认证方的灌装服务；服务核对工站的令牌和配额、型号已经认证，用 CA 签发证书，工站装回设备（`install_cert`）。CA 的私钥只在认证方那里，工站拿不到；每一台的签发都有记录。开源的这一侧是 `keystore` 包的 `ReadCSR`、`AttestFor`、`VerifyPossession`、`(*CA).Certify`、`InstallCerts`（`phone-provision` 是 CA 在手边时的本地版，测试用）。真机上工站经 USB 连到设备、密钥放进 TEE 要随硬件做（**未实现**）。工作人员在审核台看到申请（报告里的系统版本、用例数、能力覆盖、渲染器、测试设备号，可下载原报告），认证或带理由驳回，记进审计日志；厂商凭申请编号查进度（`GET /v1/certifications/<编号>`）；已认证的型号公开（`GET /v1/certified`：厂商、信用代码、型号、系统版本、认证时间）。没给 `--cert-root` 的后台不收申请。
- 设备开机自测也是这个模式，报告从串口取回（`build/cts-device.json`，根公钥在 `cts-device.json.root`），`make yocto-selftest` 最后在宿主机上核对它的签名。认证计划见 59（内部文档） §2。

## 12. 打包与发布

签名包、安装器、增量更新和下架已实现；商店后台是模拟的（[07](07-ecosystem-and-store.md) §4.1：开发者身份核验、人工审核、增量包、吊销清单、崩溃报告鉴权）。

**包的格式（`phoneapp/1`）**：一个 tar，里面是应用目录的全部文件，再加一份 `META/signature.json`：
- 列出每个文件的路径、权限（0644 或 0755）、大小和 SHA-256；
- 附开发者证书：开发者名字和公钥，由商店的根密钥签发；
- 整份清单由开发者密钥签名（Ed25519）。

打包和核验都用 `pkg`：

```go
dev, _ := pkg.DevDeveloper(keyDir, "我的工作室", store) // 开发者密钥 + 商店签发的证书
raw, _ := pkg.Build("notes/", dev)                    // notes/ 下有 manifest.json 和程序
p, err := pkg.Open(raw, roots)                        // 设备上安装前的核验
```

**安装时系统检查什么**：

| 检查 | 不通过时 |
|------|----------|
| 签名、证书链连到设备信任的商店根；文件和签名清单一一对应，不多不少 | 拒收 |
| 只有普通文件；路径不能跳出包外 | 拒收 |
| 清单：`id` 是反向域名（不以 `system.` 开头），有 `name` 和 `exec`，`exec` 是包根目录下的可执行文件 | 拒收 |
| 清单的 `nfc`：有卡就要声明 `nfc` 权限，卡的编号、类别、AID 合法（`ui.CheckNFCCards`，商店后台也这样查） | 拒收 |
| 清单的 `privacy`：类别和用途合法；没有 `net` 不能声明发出数据；有 `net` 时每个运行时权限打开的数据都写在 `collects` 或 `local` 里（`ui.CheckPrivacy`，商店后台和 `kario build` 也这样查；[33](33-app-guidelines.md) §3） | 拒收 |
| TypeScript 应用（`"runtime": "js"`）：没有 `exec`，不带可执行文件和字节码（`.jsc`），`main` 是包根目录下的 `.js` 文件；商店审核用同一条检查（`jsrt.Check`） | 拒收 |
| 不能有 `granted`，不能声明仅系统权限（`system`、`install`），不能声明不认识的权限 | 拒收 |
| 不能和系统镜像里的应用同名 | 拒收 |
| 更新必须由同一个开发者密钥签名 | 拒收 |
| 以上都通过：系统弹框列出名称、版本、开发者、签发的商店和权限 | 用户取消则不安装 |

**装好以后**：
- 代码放在系统目录 `packages/<id>`，应用自己只能读和执行，不能改；
- 应用只运行包里的程序，不能借用系统目录里同名的程序；
- 签名清单随代码一起保存（`META/signature.json`）。每次开机后第一次启动、开发者模式变化之后，系统都按当时信任的商店根重新核对一遍：文件被改过、多出没签名的文件、商店不再受信任，都不让启动（`pkg.VerifyDir`）；
- 卸载时删除代码和数据，并清掉运行时授权和使用记录、后台任务、闹钟、照片授权、密钥服务里的全部密钥（应用自己的、它的小程序的、推送密钥），推送令牌作废。以后别的开发者用同一个 ID 发布的应用拿不到这些东西。

设备信任哪些商店：镜像里的 `share/phone/trust/<商店>.pub`；打开开发者模式（「设置 › 开发者模式」）时，再加上状态目录里的 `trust/`。原型的自测用状态目录里的「开发商店」签名（`pkg.DevStore`），它扮演商店后台。开发者模式关掉以后，开发商店签名的包装不上，已经装好的也不能启动。正式商店的根随产品镜像或产线灌装进来，开发者在商店后台注册（[07](07-ecosystem-and-store.md) §4.1）。

不打包时，也可以把应用目录直接放进 `share/phone/apps/`，这相当于系统镜像里的应用，只在开发机上这样做。
