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
| `runtime`、`main` | `"runtime": "js"` 是 TypeScript 应用（§9.2）：不写 `exec`，系统用 `kario-jsrt` 运行包根目录下的模块 `main`（缺省 `main.js`）；包里不能有可执行文件和字节码，机器码只能是 `native` 声明的模块。不写 `runtime` 是自带程序的应用 |
| `native` | TypeScript 应用带的原生模块（§9.3）：`["imgproc"]`，至多 32 个，名字 `[a-z][a-z0-9_]*`、至多 32 个字符；每个是包里的 `lib/<架构>/<名字>.so`。带它的应用照原生应用审核，每个版本都进人工审核 |
| `permissions` | 要用的权限（§6），没声明的能力一律拒绝 |
| `purposes` | 每个运行时权限的用途：`{"camera": "扫码付款"}`，1–80 个字，授权框里显示；声明了运行时权限却没写用途的应用装不上（§6） |
| `privacy` | 隐私标签：哪些数据发出手机、做什么用（`collects`），哪些只在手机上用（`local`），商店的「详情」页显示；写了就要和权限对得上；能上网（`net`）的应用不写，官方商店拒收（已上架的应用的更新有宽限期，宽限期里上架的显示「没有提供」），设备上照装（[33](33-app-guidelines.md) §3） |
| `background` | 后台模式：`voip`（要有 `voip` 权限）、`audio`（正在播放时不冻结）、`location`（用户「始终允许」位置、正在持续定位时不冻结，§4.7） |
| `requires` | 运行依赖，如 `["python3"]`，缺了就不显示这个应用 |
| `order` | 在主菜单里的位置 |
| `share` | 能从分享面板接收什么：`text`、`image`、`link`、`file` 的组合（§4.9） |
| `intents` | 能替别的应用做的事：`[{"action", "label"}]`，最多 8 个；`action` 是小写的名字（如 `pay`），`label` 1–8 个字（§4.10） |
| `commands` | 贡献给命令面板的入口：`[{"id", "label", "arg"}]`，最多 8 个，标签 1–16 个字（装应用时检查；系统镜像里的设置每一项都是一条，标签就是那一项的标题，至多 32 个字）。命令面板里打了字才列出它们（「应用名 · 标签」），空着时不列，免得盖住本页的动作。用户选中，系统就用 `arg` 作深链接打开应用（如电话的「最近通话」是 `log`）；应用已经在运行时收到 `open` 事件，这也算一个动作，应用处理完以前按的键等它（[11](11-platform-architecture.md) §「一次一个动作」） |
| `settings` | 应用的设置：`[{"key", "title", "type", "default", ...}]`，`type` 是 `toggle`（开关）、`choice`（选项，带 `choices`）、`number`（数字，带 `min`、`max`、可选 `step`，系统给 +/− 两个动作）或 `text`（一行文字，`max_len` 默认 64 个字，不能有换行和制表符）。由系统在「设置 › 应用设置」里统一呈现、保存并核对类型和范围，应用不用自己画设置页；用户改了以后应用收到 `settings` 事件，用 `app.settings` 读当前值 |
| `widget` | `true` 时应用可以用 `widget.set` 在待机界面放一行字（最多 32 个字，显示为「应用名：内容」，最多 3 个应用）；用户在待机界面按 ↓ 选中这一行、按 OK，系统用这一行的 `arg` 作深链接打开应用 |
| `version` | 版本号，打包时写进签名（§12）；商店只收点分数字（`1.0`、`2.13.1`，至多 8 段、每段至多 9 位），按它比新旧；手机上的商店应用也只列出应用 ID 和版本号合这个样子的条目（它们拼成下载的文件名和地址） |
| `min_platform` | 应用能跑的最低平台级别（§2.2），整数；不写或 `0` 是任何级别。比系统新的，安装器拒装、商店不列出；`kario new` 写上 SDK 的级别 |
| `notify_actions` | 系统替应用显示推送通知（只带标题、正文的推送）时挂在通知上的动作：`[{"id", "label", "key", "input"}]`，如鸿雁的「回复」。用户在通知中心选中后，系统在后台把 `notif_action` 交给应用，不把它带到前台；带 `input` 的先弹系统的输入界面（快捷回复），文字放在消息的 `text` 里。应用自己发的通知（`notify`）在 `actions` 里带同样的动作 |
| `nfc` | 应用的非接触卡（NFC 卡模拟，[30](30-nfc.md) §6）：`[{"id", "category", "description", "aids", "requires_unlock", "express", "locales"}]`，最多 8 张；`category` 是 `payment`（只给默认付款应用、只在解锁时应答）或 `other`；`aids` 是十六进制的 AID（5–16 字节，一个 AID 只在一张卡里，PPSE 只在付款卡里）；`express: true` 的 `other` 卡可以被用户设成快捷交通卡（息屏、锁着也能刷）；`locales` 按语言给卡名。要声明 `nfc` 权限 |
| `filing` | APP 备案号（如 `京ICP备2026001234号-1A`）。商店后台检查格式，在应用页公示；在国内公开的商店里没有备案号的应用不能上架（[07](07-ecosystem-and-store.md) §4.1） |
| `lang`、`locales` | `lang`：清单里这些文字是用哪种语言写的（`zh-Hans`、`zh-Hant`、`en`、`es`，不写是 `zh-Hans`）。`locales`：同样的文字在其他语言里怎么写，按语言给出，没译的项不写：`{"es": {"name": "Notas", "description": "…", "settings": {"sort": {"title": "Orden", "choices": ["Fecha", "Nombre"]}}, "purposes": {"camera": "…"}, "commands": {"log": "…"}, "intents": {"pay": "…"}, "notify_actions": {"reply": "…"}}}`。`settings` 按设置键给 `title` 和与 `choices` 一一对应的显示名（应用存的值不变），`commands`、`notify_actions` 按 `id`，`intents` 按 `action`。系统按设备语言的回退链（§9.1）取用，主菜单、切换应用、权限框、设置 › 应用设置都显示设备语言的名字，选项也显示译名（`settings.apps` 的 `labels`）。装应用时和商店后台都核对：只能是这四种语言，长度限制和清单本身一样（名字 1–32 个字），不能翻译清单里没有声明的设置、权限、命令、意图或通知动作；商店目录的条目带上名字、简介和用途的译文 |
| `granted` | 预授权，**只对系统镜像里的应用有效**，安装器会拒绝这个字段 |

## 2. 协议

系统启动应用时把一个 Unix 套接字放在 fd 3（环境变量 `PHONE_UI_FD`），另外给出 `PHONE_APP_ID`、`PHONE_DATA_DIR`（应用自己的数据目录，也是 `HOME`）、`PHONE_LANG`（界面语言，§9.1）、`PHONE_PLATFORM`（系统的平台级别，§2.2）和 `TZ`（时区，IANA 名称，如 `America/Mexico_City`）。设备的时钟本身是 UTC；时区在应用启动时给出；应用运行期间时区变了（用户改了、换了网络），系统发 `time.zone` 事件（§5），Go、C 的 SDK 和 TypeScript 运行时收到后先把新时区设进 `TZ`（`localtime`、`Date` 随之改变），再交给应用。每条消息是一行 JSON，`t` 字段区分类型：

| 方向 | `t` | 作用 |
|------|-----|------|
| 应用 → 系统 | `hello` | 第一条消息，带 `proto: "phone.ui/0"`；说得了几个主版本的应用另带 `protos`（§2.1，四个 SDK 都会） |
| | `reset` / `push` / `replace` / `update` / `pop` | 管理页面栈：重置、压入、替换栈顶、按 `id` 更新、弹出。`update` 只改栈里有的页面；Go SDK 对栈里没有的页面干脆不发，一次刷新许多页时不占 Shell 读这个应用的配额（§8.1），应用回 ping 不会因此变慢 |
| | `toast` / `notify` | 提示条；通知（`kind: "call"` 是来电，需要 `voip` 权限） |
| | `req` | 请求能力：`req`（编号）、`cap`、`args` |
| | `pong` | 回答 `ping`，带同样的 `value` |
| | `state` | `data`：重建页面要的东西（至多 64 KB 的 JSON）。系统在内存紧张时结束了后台的应用，下次启动时原样交回（`start` 的 `data`，[17](17-app-sandbox.md) §5.3）；Go SDK 在每次离开前台时调 `OnSave` 发出 |
| 系统 → 应用 | `start` | 启动，`value` 是启动参数（深链接）；`data` 是上一个进程交来的 `state`（只在系统结束了它、用户又回来时才有；Go SDK 的 `Restore`）；`proto` 是系统选定的协议版本（§2.1） |
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

### 2.1 兼容规则

握手：`hello` 的 `proto` 写 `phone.ui/<主版本>[.<次版本>]`，这一版系统说 `phone.ui/0`。系统接受它说的主版本的任何次版本（`phone.ui/0`、`phone.ui/0.3`）；不写版本的应用按系统自己的主版本对待（记一行日志）。说得了几个主版本的应用在 `protos` 里列出全部，`proto` 写其中最老的（不会协商的旧系统只看它），系统取两边都说的最新的一个；没有共同的主版本时系统结束应用，崩溃记录写明「protocol version」，并提示用户「与当前系统版本不兼容」。系统在 `start` 的 `proto` 里说它选了哪个（旧系统不带这个字段：那就是 `hello` 的 `proto`），此后两边都按它说话。重复的 `hello` 不理会。握手本身（`hello` 的 `proto`、`protos`，`start` 的 `proto`）在所有主版本里都一样。

| 变化 | 次版本里可以 | 只能在新的主版本里 |
|------|------|------|
| 消息类型、字段、能力、事件、节点类型、错误文字 | 新增（新字段都是可选的） | 改名、删除 |
| 字段的 JSON 类型和含义 | 不变 | 改变，或者可选改成必填 |

不认识的东西怎么处理：

- 不认识的字段：Shell 和 Go SDK 都忽略，不拒绝多出来的字段。
- 应用发来不认识的消息类型：忽略，每种类型每次运行只记一行日志，不算错误，应用不会因此被结束；新应用在旧系统上用不上新消息，但照样能跑。系统发来的消息类型应用不认识时也要忽略（Go SDK 就是这样）。
- 不认识的能力：`res` 带 `err: "unknown capability \"…\""`；不认识的节点类型：两个渲染器都不画；不认识的事件名：应用忽略。
- 不是 JSON、字段的 JSON 类型不对、嵌套超过 10000 层的行：丢掉并记日志；一次运行里到第 16 行时系统结束应用（§8.1）。

主版本：Shell 自己的形式（`ui.Msg` 和 `ui` 包里的类型）总是最新的主版本（`ui.ProtoMajor`）。过渡期里 Shell 同时说它前面的一个主版本，至少一个发布版：`shell/protover.go` 的 `protoVersions` 里那一项把应用发来的旧版消息转成 Shell 的形式、把 Shell 发出的转成旧版的（`in`、`out`，按消息的字段，JSON），转不了的应用消息当格式错误丢掉（§8.1 的计数）；说旧版的应用在日志里记一行，带差别。Shell 重启后接管应用时（systemd 的 fd store、原地重启），连接的名字带着应用说的旧主版本（`app-<应用>-<pid>.v<主版本>`），新的 Shell 照样转；新 Shell 不说那个主版本就关掉这条连接。

| 主版本 | 和 Shell 自己的形式的差别 | 系统接受 |
|------|------|------|
| `phone.ui/0` | 就是 Shell 自己的形式，不转 | 现在，唯一的主版本 |

到现在还没有过主版本变化，所以这张表只有一行。机制在开发机上用一个只在测试里编出来的 `phone.ui/1` 走过一遍（三处不同：`toast` 叫 `notice`、文字在 `body` 里；`action` 用 `page` 指页面；`resync` 叫 `resend`），见下面的「怎么验证」。

SDK 这边（Go 的 `sdk`、C 的 `sdk/c`（kario-jsrt 也经它讲协议）、Node 下的运行时 `sdk/ts/node/runtime.mjs`）都会协商：一个主版本和 SDK 自己的形式（`ui` 包的、`PHONEUI_PROTO`）差在哪，写成改名的规则（`ui.ProtoForm`：一条规则把消息类型 `t` 改名成 `as`、把这种消息（不写 `t` 时是每种消息）里的字段从自己的名字改成那个主版本的名字），四个 SDK 读的是同一种 JSON。SDK 说得了不止一个主版本时，`hello` 的 `proto` 写最老的、`protos` 从新到旧列出全部；`start` 到来以前发的消息先留着（那时还不知道系统选了哪个）；`start` 里的 `proto` 说了哪个就按哪个的规则改名，发出的从自己的形式改成它的，收到的改回来，那个主版本没有的消息类型丢掉；`start` 不带 `proto`（不会协商的旧系统）就按 `hello` 的 `proto`。只说一个主版本时什么都不变：只发 `proto`，不留消息、不改名。现在只有一个主版本，所以 SDK 里没有别的主版本的规则；测试用环境变量 `PHONE_UI_TEST_FORMS`（`ui.EnvTestForms`，系统从不设它）把那个只在测试里有的 `phone.ui/1` 交给 SDK。到真的换主版本时，SDK 把旧主版本的规则编进去（照下面第 4 步），Shell 那边的 `protoVersions` 也可以用同样的规则（`ui.ProtoForm` 的 `FromMajor`、`ToMajor` 就是 `in`、`out`）。改名表达不了的变化（字段的类型或含义变了）要各个 SDK 另写代码，**未实现**（还没有这样的变化）。

引入一个新的主版本（比如 `phone.ui/1`）时：

1. 在这份文档里写出变什么，要删的先标成「弃用」（见下面）；新主版本也是一个新的平台级别（§2.2）。
2. `ui` 包改成新主版本的形式（`ui.Proto`、`ui.ProtoMajor`、改了的类型），Shell 和系统自带的应用跟着改；`shell/protover.go` 的 `protoVersions` 第二项放旧的主版本，带 `changes`、`in`、`out`；上面的表加一行。
3. `shell/testdata/compat/` 里旧主版本的录音经 `in` 照样要被接受（`TestCompatTranscripts`），`make compat-old` 用旧提交编出的应用对着新的 Shell 跑。
4. SDK 改说新主版本，过渡期里两个都说：`hello` 带 `proto: "phone.ui/0"` 和 `protos: ["phone.ui/1", "phone.ui/0"]`，按 `start` 的 `proto` 说话（没有这个字段的是旧系统：说 `phone.ui/0`）。
5. 至少一个发布版以后，从 `protoVersions` 去掉旧的一项：只说旧版的应用从此被拒（崩溃记录和提示同上）。

弃用：要删掉的消息类型、字段或能力先在本文档标成「弃用」，至少保留一个主版本，下一个主版本才删。

怎么验证：`shell/testdata/compat/` 里是系统自带的应用在一次自测里发出的消息（每个应用一份，按消息的结构去重；`PHONE_COMPAT_RECORD=1 go test ./shell -run '^TestCompatRecord$'` 重新录），`make test` 里的 `TestCompatTranscripts` 把它们原样发给当前的 Shell：一行都不能被当成格式错误或不认识的消息，能力都要认识，请求都要有回复，应用不能被结束；`TestProtoVersions` 查握手的版本规则；`ui` 的 `TestNegotiate` 查版本名和协商；`shell` 的 `TestProtoTransition` 用上面那个只在测试里有的 `phone.ui/1`：假的应用（测试程序自己起的进程）在 `start` 里得知是 `phone.ui/1`，它的消息转成 Shell 的形式、Shell 的转成它的，它发来 Shell 形式的 `toast` 当格式错误丢掉，两个都说的应用拿到较新的，只有一个主版本时同一个应用被拒；`TestProtoAdopted`：接管过来的连接按名字里的主版本接着转，新 Shell 不说的主版本被关掉；`TestSDKProtoMajors`：四个 SDK 各一个应用（Go 的、用 `sdk/c` 现编的 C 程序、kario-jsrt 和 Node 下的同一个 `main.js`），经 `PHONE_UI_TEST_FORMS` 说 `phone.ui/0` 和 `phone.ui/1`，对着只说 `phone.ui/0` 的 Shell 讲 `phone.ui/0`、对着过渡期的 Shell 讲 `phone.ui/1`，两种情况下提示都到了、读得出动作的页面、`resync` 后重发了页面，没有一行被当成格式错误。各个 SDK 自己的测试：`ui` 的 `TestProtoForm`（改过去再改回来不变、那个主版本没有的类型报错），`sdk` 的 `TestProtoMajors`，`sdk/c` 的 `test_phoneui.c`，`jsrt` 的 `TestProtoMajors`、`TestNodeProtoMajors`（`hello` 带两个主版本、`start` 以前发的留到 `start` 以后、三种 `start` 各讲对的形式）。`make compat-old OLD_REV=<提交>` 用旧提交的源码编出应用程序，在当前的 Shell 和服务上逐个启动、按几个键：这是二进制兼容的检查（Go 应用和 Python 示例；TypeScript 和 C++ 应用不在内）。

### 2.2 平台级别

照安卓的 `minSdkVersion`：系统有一个平台级别（`ui.PlatformLevel`，这一版是 1），应用在清单里写它能跑的最低级别 `min_platform`。

- 级别怎么涨：上一个级别发布之后，第一个往协议里加东西（能力、消息类型、字段、事件、节点类型、错误文字，§2.1 里次版本可以加的）的发布版把级别加 1，同一个发布版里再加的东西都算这个级别；什么都没加的发布版不涨，级别从不降。主版本变化（`phone.ui/1`）也是新的级别。
- 每个能力记着它从哪个级别开始有（`ui/caps.go` 的 `CapLevels`，TypeScript 里是 `ui.d.ts` 的 `CapLevel` 和 `PlatformLevel`）；新加的能力写上新的级别，没写的话 `ui` 的测试（`ui` 里每个 `Cap…` 常量）和 `shell` 的测试（broker 认的每个能力）都失败，`tools/tsgen` 也不生成。这一版的全部能力都是级别 1。
- 安装：`min_platform` 比系统的级别高的包，安装器拒装（「需要更新的系统」）；已经装好的（系统回到了旧的槽）不启动。商店后台只收它认识的级别；手机上的商店应用带着本机的级别取目录（`GET /v1/catalog?platform=`），后台不发一个版本都跑不了的应用，系统（`pkg.catalog`）再去掉本机跑不了的：分批发布的新版本要的级别更高时，给这台手机上一个版本。
- 运行：broker 不按 `min_platform` 拦能力。`min_platform` 只保证安装的系统至少有那个级别的能力；更新的能力，应用在系统有的时候照样能用，先看 `PHONE_PLATFORM`（Go SDK 的 `App.Platform`；没有这个变量的系统是级别 1），或者接住旧系统对它的回答 `unknown capability`（§2.1）。和安卓的 `targetSdkVersion` 对应的东西没有：§2.1 不许在主版本之内改变已有能力的行为，所以没有要应用声明才换上的新行为。
- `kario new` 把 SDK 的级别写成新项目的 `min_platform`；`kario manifest check` 和 `kario build` 不收比 SDK 新的级别。

状态：开发机上单元测试 ✓（`ui` 的能力级别表、`shell` 的安装和启动、`pkg` 的目录、`appstore` 的 `?platform=`、`apps/store`、`cmd/kario` 的模板、`sdk`）；还没有过级别 2，涨级别的过程没有实际走过（**未验证**）。

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
| `item` | 列表项 | `title`、`subtitle`、`trailing`、`badge`、`icon`；`style: bubble-in / bubble-out` 是聊天气泡，`tile` 是宫格项（没有图标的宫格项把 `subtitle` 写在标题下面，`badge` 是角上的一个点：日历一天的农历和有没有日程）；`src` 可带图；`src` 是联系人照片（`contact:<photo>`）又有 `icon` 时，照片画在图标的位置，没有照片时画 `icon` |
| `input` | 输入框 | `placeholder`、`value`、`secret`、`submit`（OK 执行的动作）、`keyboard`（`number` / `phone` 数字不经拼音，全键盘上按键直接打出键上的数字、不用按 Fn，[18](18-nokia-form-factors.md) §4.1；`email` / `url` 输入法从英文开始；`date` / `time` 是系统的日期、时间输入框：值两边都是 `YYYY-MM-DD`、`HH:MM`（24 小时制），用户看到的是地区的年月日顺序加星期（`2026-11-17 周二`、`17/11/2026 Tue`）、自己选的 12 / 24 小时制；刚移到框上时 ↑ ↓ 照常移焦点，← → 选年、月、日或时、分（选中的写在方括号里），这时 ↑ ↓ 加减它（日跨月、跨年进位，月、年不让日超出那个月，分钟 5 分钟一档、进位到小时），数字直接打（九宫格的数字键，全键盘的数字层不用按 Fn；两位，年四位，打不出两位的第一位直接定下来，如日的 4），打完一段选下一段，最后一段打完结束编辑；OK、返回也结束编辑，退格删一位、没选时清空；系统的粘贴和输入法不进这种框；开发机上单元测试和自测 ✓，**未上真机**）、`live`（每次编辑都通知应用） |
| `button` | 按钮 | `text`，动作放在 `actions` |
| `image` | 图片 | `src`：`media:<id>`（相册照片）、`contact:<photo>`（联系人的照片，`contacts.list` 的 `photo`）、`qr:<文本>`（二维码）；`rows` 是高度 |
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

**方向键左右**：焦点元素或页面上 `key` 为 `left` / `right` 的动作由 ← → 触发（音量的调小 / 调大、播放器的上一首 / 下一首、看图的上一张 / 下一张）；没有绑定时，← 返回上一级，→ 打开焦点项（同 OK）。宫格里方向键照旧移动；输入框里有字时移动光标，到头就停（[18](18-nokia-form-factors.md) §4.2）。

**给屏幕阅读器的字段**：任何节点都可以带 `role`（`switch` 开关、`checkbox` 复选框、`radio` 单选按钮、`button` 按钮、`link` 链接、`heading` 标题）、`checked`（开关、复选框、单选按钮的状态）和 `a11y_label`（屏幕阅读器读它，不读节点上的文字：只有符号或图标的项，比如计算器上的「×」可以标成「乘」）；屏幕上不显示它们（设置 › 辅助功能的屏幕阅读器，[19](19-core-apps.md) §1.4）。没有 `role` 时阅读器按种类读（编辑框、按钮、进度条……），列表项读标题、副标题、`trailing`、`badge` 和第几项。KarioUI 的组件还没有对应的属性（**未实现**），TypeScript 应用直接写节点的这几个字段。

**动作的命名约定**：几个动作 ID 绑定实体键，应用不用自己处理按键。
- `call` / `dial`：通话键（绿键）触发。先看焦点元素上的动作，再看页面动作；拨号框的 `submit` 是 `dial` 时，通话键直接拨出。
- `hangup`：挂断键（红键）触发页面上的这个动作；页面上没有时，有通话在后台就挂断它（诺基亚的红键），否则回待机。
- `hide`（`ui.ActionHide`）：系统自己处理，不发给应用：应用连同这一页转到后台，回到它来到前台之前的那个应用（没有就回系统界面、待机）；页面上有这个动作时，返回键也这样做，不再退出这一页。给通话界面这种通话还在、页面不能关的场合（电话的通话界面用它当右软键「最小化」，[19](19-core-apps.md) §5）。安全锁锁着时不起作用。
- 页面上没有 `call` / `dial` 时，通话键回到进行中的通话（电话的通话界面、VoIP 应用的通话），没有通话才打开最近通话。
- 来电界面由系统画，通话键接听、挂断键拒接也由系统处理。

应用从不画像素，也拿不到别的应用的画面。图片由系统解码显示：`media:<id>` 只对能访问这张照片的应用显示（§4.6），`contact:<photo>` 只对有 `contacts` 权限的应用显示。

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
| `tel.dial` | `{number}` | 拨打电话；商店安装的应用每次都由系统写明打给谁、请用户确认，声明了 `phone` 也一样（付费号码是最老的骗术）；系统镜像里的电话、通讯录应用不问（通讯录拨出的通话界面归电话应用）。已经在通话时是第二路：接通的那一路先保持；还在接通或振铃、已有两路、正在紧急呼叫时不拨，错误里写明原因和「按通话键回到通话」 | — |
| `tel.answer` / `tel.hangup` / `tel.hold` / `tel.mute` | `{call_id, on}` | 接听、挂断、保持、静音；另一路接通时接听等待的来电先保持那一路，恢复保持的那一路是两路切换 | `phone` |
| `tel.dtmf` | `{call_id, tones}` | 往接通的电话里发按键音（DTMF，打语音菜单）：`tones` 是 1–32 个 `0`–`9`、`*`、`#`、`A`–`D`；发过的在通话的 `dtmf` 里 | `phone` |
| `tel.calls` / `tel.log` | — | 进行中的通话；通话记录 | `phone` |
| `sms.send` / `sms.list` / `sms.read` | `{to, text, media?, subject?, at?, zone?}`（默认短信应用还能用 `{id}` 重发没发出去的、立即发定时的）/ `{peer?, query?, folder?}` / `{peer}` | 发短信、列短信（一个会话的，号码怎么写都行；或内容、号码含 `query` 的；或 `folder: "blocked"` 拦截文件夹的；都不给就是拦截文件夹以外的全部）、把一个会话标为已读。`to` 里几个号码用逗号隔开是群发；`media` 是应用看得到的照片（`media:<id>`，至多 5 张）；带图片、主题或群发的作为彩信发。短信存在 phone-smsd（[32](32-messaging.md)）；每条带 `thread`（会话：按 E.164 规整的号码，群聊是成员号码排序后用逗号连起来）、`state`（`sending` / `sent` / `delivered` 送达 / `failed`）、`code`（识别出的验证码）；彩信还带 `kind: "mms"`、`subject`、`parts`（`{type, name, size, media}`，图片的 `media` 可放进图片节点）、`members`（群聊的号码）、`mms`（没下载完时：`pending` / `downloading` / `failed` / `expired`）；保存的闪信带 `flash`。`at`（Unix 秒，只给默认短信应用）是定时发送：短信存成 `state: "scheduled"`，带 `due`（发出的时刻）和 `zone`（选时间时的时区，只用来显示），phone-smsd 到点发出，重启后照旧；那一刻是固定的，手机之后换了时区也在这一刻发；时间已过（超过 1 分钟）或超过一年后的不收；`sms.delete` 删掉就是取消。不是默认短信应用的商店应用发的每一条，系统都写明发给谁、内容是什么，用户确认才发 | `sms` |
| `sms.threads` / `sms.alerts` | `{folder?}` / — | 会话列表（最后一条、条数、未读数、草稿 `draft` 和它的时间 `draft_at`，新的在前；只有草稿的会话条数为 0）；收到过的紧急警报（小区广播：级别、频道、内容、时间） | `sms` |
| `sms.delete` / `sms.block` / `sms.blocked` / `sms.download` | `{ids}` 或 `{peer}` / `{number, on}` / — / `{id}` | 删短信或整个会话；拦截或取消拦截号码或号段（`1065*`，回拦截名单）；拦截名单；重新下载没下载下来的彩信。只给默认短信应用（`sms` 角色：默认是系统的短信，用户可在设置里换成别的有 `sms` 权限的应用）：别的应用有 `sms` 权限也不行（Android 只让默认短信应用写短信库） | `sms` |
| `block.list` / `block.set` | — / `{number?, word?, on?, hidden?, unknown?}` | 系统的拦截名单（来电和短信共用）：`{numbers, hidden, unknown, words}`，`numbers` 里是 E.164 号码和以 `*` 结尾的号段，`hidden` 拦截隐藏号码，`unknown` 拦截通讯录以外的号码，`words` 是关键词（含有它的短信不管谁发的都进「已拦截」，只管短信；比较时只看字母和数字，不分大小写和全角半角，至多 200 个，每个至多 40 字）；`block.set` 拦截（`on`）或取消拦截一个号码、号段或关键词、设两个开关，回新的名单。拦截的来电不响铃直接拒接、通话记录里是 `blocked`。只给默认短信应用、系统的电话和设置 | — |
| `tel.ussd` | `{code}` / `{text}` / `{cancel}` | 发 USSD 码（`*100#`）、回复网络的菜单或提问、结束会话；回 `{state, text}`：`done` 是网络的最后答复，`input` 表示网络在等回复。`*#06#` 由手机自己答 IMEI，不发给网络。网络主动发来的 USSD 作为 `tel.ussd` 事件交给电话应用。只给系统镜像里的应用：这类码能开通收费业务 | `phone` |
| `contacts.list` | — | 联系人，按拼音排：`{id, name, number, numbers, emails, addresses, notes, nickname, org, title, birthday, anniversary, urls, pinyin, groups, photo, book, read_only}`（`groups` 是它所在群组的 ID）。`numbers` 是 `[{number, label}]`，`label` 是 `mobile` / `home` / `work` / `other`；`emails` 是 `[{address, label}]`，`addresses` 是 `[{label, po_box, extended, street, city, region, postcode, country}]`（vCard ADR 的七个部分），这两种的 `label` 是 `home` / `work` / `other`；`photo` 是照片的键（没有时为空，显示用图片的 `src: "contact:<photo>"`）；`book` 是它所在的通讯录（`contacts.books` 的 `id`，空的是只在手机上），`read_only` 为真时那本通讯录只读（联系人只在服务器上改，系统不让改、搬、删，也不能加进群组、合并）；`nickname`、`org`（单位，vCard ORG 的第一部分）、`title`（职位）各一行，`birthday`、`anniversary` 是 `YYYY-MM-DD`，不带年份的是 `--MM-DD`，`urls` 是网址（至多 4 个）；`number` 是第一个号码（只认一个号码的旧应用照旧能用）；`pinyin` 由系统按名字算（拉丁字母的名字就是它的词），供九宫格搜索（`ui.ContactMatches`） | `contacts`（运行时） |
| `contacts.save` | 联系人（没有 `id` 是新建；只给 `number` 也行） | 存好的联系人（带 `id`）。系统先弹框写明存什么（姓名、每个号码和类型、邮箱、地址、有没有照片、备注、昵称、单位、职位、生日、纪念日、网址）、哪个应用要存，用户确认才存；改已有的联系人（带 `id`）还要 `contacts` 权限。保存的就是给的样子：没给的邮箱、地址、照片、备注和别的字段都算没有，改联系人时把 `contacts.list` 读到的原样带回来。名字最多 64 字，号码最多 8 个、每个 40 字节，邮箱最多 8 个、每个 254 字节，地址最多 4 个、每部分 100 字，备注最多 1000 字，昵称、单位、职位各 100 字，网址最多 4 个、每个 1000 字节，多出的截掉；不是日期的生日、纪念日不存；没有名字时用第一个号码。`photo` 是 `contacts.photo` 给的键（系统没有的键拒绝）。新建的进默认的通讯录（设置 › 账号 › 新联系人存到）；`book` 为 `phone` 只存在手机上，系统的通讯录应用还可以给 `contacts.books` 里能写的一本（给只读的一本时进默认的通讯录）。只有系统的通讯录应用能把联系人搬到另一本：改的时候给另一个 `book`（能写的一本，或 `phone`），联系人的 ID、照片、群组不变，旧的那本在下次同步时从服务器上删掉它、新的那本建一张新名片；别的应用改的时候 `book` 不管用。只读通讯录里的联系人改、删都拒绝（「…在只读的通讯录里：只能在它的服务器上改」） | 新建 —；修改 `contacts`（运行时） |
| `contacts.delete` | `{id}` | 系统写明删谁、请用户确认后删除 | `contacts`（运行时） |
| `contacts.import` | — | `{file, count, skipped}`。系统列出「文件」里的 vCard 名片（`.vcf`，2.1 / 3.0 / 4.0），用户选一个，系统读出里面的联系人（姓名、号码、邮箱、地址、内嵌的照片、备注；不带群组），写明有几个新的（已在通讯录里的、没有名字也没有号码的不算）、请用户确认后加入（进默认的通讯录）。应用只知道加了几个，看不到文件 | — |
| `contacts.export` | — | `{file, count}`。系统确认后把全部联系人写成一个 vCard 3.0 文件存进「文件」（带邮箱、地址；照片也带上，除非文件会超过 4 MB，这时都不带，确认框里写明）；文件不交给发起的应用，用户自己决定给谁 | `contacts`（运行时） |
| `contacts.groups` / `contacts.group` | — / `{op, id?, name?, contacts?}` | 群组：`[{id, name, members}]`（`members` 是联系人 ID；`contacts.list` 的每个联系人也带 `groups`）。`op` 是 `create`（`name`）、`rename`（`id`、`name`）、`delete`（`id`，联系人保留）、`add` / `remove`（`id`、`contacts`），回群组。群组名至多 40 字、不重名，至多 100 个。系统先写明改什么、请用户确认；群组按名字和账号同步（[19](19-core-apps.md) §1.1） | `contacts`（运行时） |
| `contacts.photo` / `contacts.books` | `{media}` / — | `photo`：把应用能看的一张照片（`media.pick` 交给它的那张，`media:<id>`）做成联系人的照片，回 `{photo}`，再用 `contacts.save` 存到联系人上：系统取照片中间的正方形，缩到至多 256 × 256，存成至多 48 KB 的 JPEG（`ui.ContactPhotoSide`、`ui.ContactPhotoBytes`）；没交给应用的照片、不是图片的拒绝。`books`：账号里打开着的通讯录 `[{id, name, account, default, read_only}]`：`account` 是账号的名字（Kario 账号或服务器的主机名，系统应用另带用户名），`default` 是新联系人默认存进的那本（都不是时存在手机上），`read_only` 的那本只从服务器同步下来，联系人不能存进去、搬进去 | `contacts`（运行时） |
| `contacts.dates` | — | 只给系统的日历应用（别的应用一律拒绝）：有生日或纪念日的联系人 `[{id, name, birthday?, anniversary?}]`（格式同 `contacts.list`），联系人别的东西一样不给；日历拿它显示每年的生日、纪念日（[19](19-core-apps.md) §1） | — |
| `contacts.merge` | `{ids}` | 把几个联系人合并成第一个：号码（合起来超过 8 个就不合并）、邮箱、地址、备注、群组合在一起，名字用第一个的（第一个只有号码时用别的名字），照片用第一个有照片的，其余的删掉；回留下的联系人。系统先列出要合并的联系人、请用户确认 | `contacts`（运行时） |
| `contacts.pick` | — | 系统的联系人选择器：列出联系人的每个号码，边打边找（名字、拼音全拼或首字母、号码），用户选一个，应用只拿到 `{name, number, numbers}`（只有选中的这个号码）；关掉回 `cancelled`。不要权限：用户自己选就是授权 | — |
| `sms.draft` | `{peer, text}` | 存一个会话的草稿（`peer` 是号码或群聊的号码，和 `sms.send` 的 `to` 一样；`text` 为空就删掉），phone-smsd 存着，重启后还在，`sms.threads` 里带出来；往这个会话发短信时草稿自动删掉。只给默认短信应用 | `sms` |
| `sms.backup` / `sms.restore` | — | `{file, count, skipped?}`。备份：系统写明要存几条短信、文件名（`sms-年月日时分秒.xml`），用户确认后把全部短信（含「已拦截」，不含定时的和正在发的）写成安卓「SMS Backup & Restore」的 XML 存进「文件」；彩信只存主题和文字（图片在相册里）。恢复：系统列出「文件」里的 `.xml`，读出用户选的那个（短信和彩信的文字部分；草稿、只有图片的彩信不算），写明有几条，确认后交给 phone-smsd 导入，已有的（同一会话、方向、时间、内容）跳过。文件不交给应用，应用只知道数量。只给默认短信应用 | `sms` |
| `sms.vcard` | `{id, index}` | `{file, count, skipped}`。彩信里的名片（`text/vcard`、`text/x-vcard` 或 `.vcf`）：系统读出里面的联系人，和从「文件」导入 vCard 一样写明有几个新的、请用户确认后加入 | `sms` |
| `sms.otp` | `{seconds?}`（默认 120，至多 300） | `{code, from}`。等下一条带验证码的短信（发起之后到的才算），系统弹框写明哪个应用要、短信是谁发的、内容和验证码，用户同意才交出验证码，别的都不给；拒绝回 `cancelled`，等不到回错误。同一个应用再发起就替换上一次；几个应用都在等时先给前台的。锁屏设了 PIN 码时不弹（继续等）。像安卓的 SMS User Consent API，不要权限：用户同意就是授权 | — |
| | | 系统的通讯录应用（`com.phone.contacts`）写入时不弹这些框：用户就在它里面编辑，删除前的确认是它的动作的 `confirm`，同样由系统画。通讯录变了（系统确认的写入、蓝牙收到的名片）时，有 `contacts` 权限、正在运行的应用收到 `contacts` 事件（§5） | |

### 4.3 声音（[20](20-audio.md)）

| 能力 | 参数 | 结果 | 权限 |
|------|------|------|------|
| `media.list` | `{kind}`（缺省为 `audio`） | 媒体库里的条目；`kind: "image"` 列照片，需要 `photos`（运行时） | — / `photos` |
| `audio.play` | `{media}` | 播放媒体库里的一项（类别 `media`） | — |
| `audio.control` | `{id, op}` | `pause` / `resume` / `stop`，只能控制自己的流 | — |
| `audio.state` | — | 自己的流 | — |
| `audio.route` | `{route}` | `earpiece` / `speaker`（`ui.RouteEarpiece` / `ui.RouteSpeaker`）；最后一路通话结束时系统切回 `earpiece` | `phone` |
| `audio.stream` | `{name}` | 一路自己的声音：回复带一块共享内存环形缓冲区（§7） | — |
| `audio.record` | `{name}` | 录音：回复带环形缓冲区，麦克风的声音写进去；只在前台或通话中可用，离开前台即停止，状态栏有指示 | `microphone`（运行时） |

### 4.4 时间

| 能力 | 参数 | 结果 | 权限 |
|------|------|------|------|
| `alarm.set` | `{id, time: "HH:MM", label, daily, on}` | 系统闹钟：应用没运行也会按时响 | — |
| `alarm.cancel` / `alarm.list` | `{id}` / — | 取消；列出自己的闹钟 | — |
| `notify.schedule` | `{id, at, zone?, repeat?, title, body?, arg?}` | 定时通知：系统在 `at`（`YYYY-MM-DDTHH:MM`）那个钟点显示这条通知。`zone` 是 IANA 时区名：09:00 Asia/Shanghai 的会议在哪儿都是那个时刻；不写是浮动时间，跟着到点时手机所在的时区（每天 08:00 吃药，出差也是当地 08:00）。夏令时跳过的钟点（02:00–03:00 里的 02:30）在跳过之后同样远的地方来（03:30），重复的钟点取第一次。`repeat` 是 `daily` 或 `weekly`（按日历的天，不是 24 小时）。`arg`（至多 256 字节）是用户打开这条通知时应用收到的深链接。同一个 `id`（1–64 字节）再排就替换旧的；不重复的时间已经过了就拒绝。回复带 `next`：下一次的时刻（Unix 毫秒）。系统保存（重启 Shell、重启手机都在），到点自己显示，冻结的、没在运行的应用不被唤醒；挂起时 RTC 像闹钟一样在它之前 3 秒叫醒手机（[28](28-battery.md) §3）；手机关着时到点的，开机后补上，正文写明「关机期间错过」，重复的从下一次接着排。每个应用至多 64 个（`ui.MaxScheduledNotifs`，同 iOS）；用户在设置里关了它的通知时到点什么也不显示；卸载时删掉。开发机上单元测试和自测 ✓，**QEMU 上未运行**，**未上真机**（挂起时的 RTC 唤醒只在单元测试里查过） | `notifications` |
| `notify.cancel` / `notify.list` | `{id}` 或 `{all: true}` / — | 取消一个或全部（回 `{cancelled}`，个数）；列出自己的（最近的在前，带 `next`） | `notifications` |
| `locale.get` | — | 系统排日期用的：`{lang, region, zone, clock_24h, first_weekday, date_order}`，`first_weekday` 是一周的第一天（0 周日 … 6 周六，按地区，照 CLDR：中国大陆周一，美国、台湾周日），`date_order` 是 `ymd`、`dmy` 或 `mdy`（中文和东亚的地区年月日，美国月日年，其余日月年）；系统的日期输入框也照这些显示 | — |
| `holidays.get` | — | 地区的法定节假日日历：`{region, source, days_off, workdays}`，`days_off` 是放假的日子，`workdays` 是调休上班的周末（`YYYY-MM-DD`）；就是卡包门禁卡用的那份（更新服务器签名下发，每天取一次，[25](25-system-update.md) §0）；手机没有这个地区的日历时两个都是空的。仓库里只有自测用的测试日历（`source` 写着），真的日历数据**未实现** | — |
| `time.offsets` | `{zone, from, to}`（Unix 秒，至多相隔 50 年） | 一个时区在这段时间里的 UTC 偏移：`{zone, spans: [{start, offset}]}`，每段从 `start` 起是 `offset` 秒（东为正），第一段从 `from` 之前开始；不认识的时区报错。给要显示别的时区时间的应用（日历导入的事件）：TypeScript 的运行时里只有手机自己的时区，没有时区库 | — |

### 4.5 系统设置（仅系统应用）

| 能力 | 说明 | 权限 |
|------|------|------|
| `perm.list` / `perm.set` | 列出、修改所有应用的运行时权限（设置应用用）；每一项带 `last_used`（毫秒时间戳）和 `uses`：这项授权最近一次被用到的时间和次数；`reset`：应用 90 天没打开，系统哪天收回了这项授权（[17](17-app-sandbox.md) §1）；`location` 那一项的 `approximate`：只给大致位置。`location_background` 只能是 `granted` 或 `denied`（没给过的列为 `denied`），只给声明了它的应用本身（不给小程序），设成 `granted` 时连带允许 `location`；`location` 改成 `denied` 或 `ask` 时连带收回 `location_background`，改成 `ask` 时精度也回到精确（§4.7） | `system` |
| `wifi.state` / `wifi.set` / `wifi.connect` / `wifi.forget` | Wi-Fi（iwd）：状态、附近的网络（信号强的在前，`signal` 是 0–4 格，`known` 已保存，`connected`）、连上后的地址；`set` 的 `{enabled}` 开关；`connect` 的 `{ssid, passphrase}`：没保存过的加密网络要带密码，不带时回 `the network needs its passphrase`；没保存过的企业网络（802.1X）要带 `{identity, passphrase, domain}`，`eap` 是 `peap`（默认）或 `ttls`（内层都是 MSCHAPv2），或 `ttls-pap`（TTLS，内层 PAP），缺了回 `the network needs a user name, password and domain`；密码只交给 iwd 保存（`/var/lib/iwd`），用来自动重连；`forget` 的 `{ssid}` 删掉保存的网络（连同它的计费选择）。每个网络还有 `metered_choice`（用户选的 `metered` 或 `unmetered`，自动时没有）、`metered`（算不算计费，连着的那个是最后的结论）和 `metered_hint`（自动检测为什么算计费：`dhcp` 或 `hotspot`）；`set` 的 `{ssid, metered}` 改一个网络的选择（`auto`、`metered`、`unmetered`，[25](25-system-update.md) §6.1）。开发机上是模拟的网络 | `system` |
| `bt.state` / `bt.set` / `bt.pair` / `bt.forget` | 蓝牙（BlueZ）：本机名称、开关、是否在搜索、已配对和附近的设备（已配对的设备带 `phonebook`：`allowed` / `denied`，它能不能经 PBAP 读通讯录和通话记录，没问过时没有）；`set` 的 `{enabled, discover}`，或 `{address, phonebook}` 允许、收回这台设备读通讯录；搜索（`discover: true`）归发起它的应用，只有在前台、亮着屏的应用能开，不然回错误；这个应用离开前台、被冻结、退出或息屏时系统替它停掉（安卓的设置只在配对页面在前台时搜索，忘了停的搜索会让射频每隔十几秒查询一次）；`pair` 的 `{address}`：对方有屏幕（手机、车载）时系统弹框给出配对码，用户核对和对方设备上的一样才配上，回复等用户答完；耳机这类没有屏幕的设备直接配上（Just Works）；`forget` 取消配对（连同读通讯录的许可）。改动（`wifi.set`、`connect`、`forget`，`bt.set`、`pair`、`forget`）按发来的先后一个个做：先开蓝牙再搜索，搜索一定在开了以后；`wifi.state`、`bt.state` 立刻回答当时的状态。开发机上是模拟的设备 | `system` |
| `bt.send` | `{address}`：把一个文件经蓝牙（OBEX Object Push）发给已配对的设备。系统列出「文件」里的文件，用户选一个，调用的应用看不到它；回复 `{sent}`（文件名）等对方接收完；对方拒收回 `“…” declined the file`，用户不选回 `cancelled`。开发机上是模拟的设备 | `system` |
| `vpn.list` / `vpn.import` / `vpn.connect` / `vpn.disconnect` / `vpn.delete` / `vpn.keygen` / `vpn.set` | VPN（WireGuard）：`list` 回 `{backend, profiles, pending_key, always_on?, kill_switch?, blocking?, apps?, app_list}`，`backend` 是 `networkd`（设备上经 systemd-networkd）、`sim`（开发机上模拟）或空（这台手机没有 VPN），每个配置有 `{id, name, state, error, address, dns, endpoint, peers, full_tunnel, allowed_ips, public_key}`，`state` 是 `connecting` / `connected` / `disconnecting` 或空，连着时另有 `since`、`handshake`（最近一次握手，Unix 秒，0 是还没握手）、`rx_bytes`、`tx_bytes`；`pending_key` 是 `keygen` 生成、还没被配置用掉的密钥对的公钥。`import` 不带参数：系统列出「文件」里的 `.conf`，用户选一个，系统按 wg-quick 的格式严格检查（密钥是 32 字节的 base64，地址和 AllowedIPs 是 CIDR，Endpoint 是「主机:端口」，`PostUp` 之类的脚本和 `Table`、`FwMark` 不支持，不认识的键一律拒绝），错误带行号；文件名（去掉 `.conf`）是配置的名字，同名的替换（没有私钥时沿用原来的密钥对）；文件没有 `PrivateKey` 时用 `keygen` 的密钥对，没有就回 `the configuration has no PrivateKey and the phone has no key pair waiting`；回复是这个配置。`connect` / `disconnect` / `delete` 的 `{id}`，回复 `list` 的样子：同时只连一个，连另一个会替换它；删除连着的先断开。`keygen` 回 `{public_key}`。`always_on` 是始终开启的配置（Shell 启动时、隧道断了、连接失败、底下换了网络时自己重连），`kill_switch` 是屏蔽未使用 VPN 的连接，`blocking` 是屏蔽正挡着（始终开启的配置没连上），`apps` 是哪些应用走隧道：空（全部应用和系统）、`only`（只有选中的）、`except`（除选中的外的全部），`app_list` 是用网络的应用 `{id, name, chosen?}`。`set` 的 `{id, always_on}`（`true` 把这个配置设为始终开启并连上，`false` 关掉始终开启，屏蔽随之关掉）、`{kill_switch}`（要先有始终开启的配置）、`{apps}` 或 `{app, chosen}`（把一个用网络的应用选进或拿出），回复 `list` 的样子；有始终开启的配置时 `connect` 别的配置被拒，`disconnect` 或 `delete` 它会关掉始终开启。私钥和预共享密钥只在系统里（Shell 的状态文件，0600），应用只拿得到公钥（[11](11-platform-architecture.md) §16） | `system` |
| `radio.state` / `radio.set` | 飞行模式、移动数据和数据漫游：`{airplane, mobile_data, cellular, data, data_roaming, roaming, apn, address, limit_held?}`，`cellular` 是基带的射频 `on` / `off`、`data` 是移动数据链路 `connected` / `idle` / `roaming`（在国外、数据漫游关着）/ `off`（没有 telephonyd 时都是空），`data_roaming` 是数据漫游开关（默认关），`roaming` 是正在漫游时的 `national`（SIM 卡本国别的网络，不受数据漫游开关管）/ `international`，`apn` 是数据连接用的接入点，`address` 是连着时移动网卡的地址（IPv4，没有就 IPv6）；`set` 的 `{airplane?, mobile_data?, data_roaming?}`。飞行模式关掉射频（telephonyd）、Wi-Fi 和蓝牙，记住用户原来的开关，结束时恢复；飞行模式下用户可以再打开 Wi-Fi、蓝牙，下次飞行模式就留着它们；拨紧急号码时基带自己打开射频，系统随之退出飞行模式；紧急呼叫中不能打开飞行模式。移动数据关掉时 telephonyd 断开数据连接，防火墙挡住它报的移动网卡；国际漫游时数据漫游关着也不连。流量套餐的硬上限（`settings.set` 的 `data_limit_off`）用满时同样挡住移动数据，但不动开关：`limit_held` 为真、`mobile_data` 为假；这时 `set` 的 `mobile_data: true` 就是用户确认超出上限（设置应用先问），到这个周期结束或调高套餐前不再挡，新周期开始时移动数据自己回来。三个开关都重启后保持 | `system` |
| `apn.list` / `apn.set` | SIM 卡的接入点（APN）：`list` 答 `{mcc, mnc, spn, carrier, apns, selected, edited}`，SIM 卡的网络不明时 `mcc` 为空、没有接入点，`carrier` 为空表示系统里没有这个运营商的接入点（数据连接用网络默认的）；每个接入点 `{id, name, apn, types, ip_type, auth, user, password_set, mmsc, mms_proxy, mms_port, edited}`，`types` 有 `default`（上网）、`mms`、`ims`、`ia`、`supl`、`xcap`、`emergency`，`ip_type` 为 `ipv4` / `ipv6` / `ipv4v6`，`auth` 为 `none` / `pap` / `chap` / `pap-chap`；密码不出系统，`password_set` 只说有没有；`selected` 是数据连接用的上网接入点，`edited` 说列表是用户改过的。`set` 的 `{op, apn?, id?}`：`save`（`apn`，带 `id` 改那一条，不带就新加；不给 `password` 就保留原来的）、`delete`、`select`（只能选带 `default` 的）、`reset`（回到运营商的），答同样的列表；接入点换了，数据连接跟着重连 | `system` |
| `account.state` / `account.add` / `account.sync` / `account.set` / `account.remove` | 通讯录的账号（CardDAV，[19](19-core-apps.md) §1.1），给设置用：`state` 回 `{accounts, default, kario_server}`，`kario_server` 是产品的 Kario 账号服务器（没有时为空），`default` 是新联系人存进的通讯录（`books` 的 `id`，空的是只在手机上），`accounts` 是每个账号 `{id, kind, server, user, books, state, waiting, error, last_sync, next_sync, contacts, remote, pending, failed, phase, done, total, mobile_data}`：`kind` 是 `kario` 或 `carddav`，`books` 是它的通讯录 `[{id, name, on, read_only, contacts, remote, pending}]`（能写的在前；`on` 是开着同步，`read_only` 的只从服务器同步下来、手机上什么都不写），`state` 是 `idle`（同步好了）、`syncing`（`phase` 是 `pull` 或 `push`，`done` / `total` 是进度）、`waiting`（`waiting` 是 `network` 或 `wifi`）、`failed`（`error` 说为什么，`next_sync` 是什么时候再试）或 `auth`（服务器不认密码，等用户）；`contacts` 是账号里的联系人数，`remote` 是其中从服务器来的，`pending` 是手机上还没传上去的改动，`failed` 是上次没写上的名片数；`calendar` 是这个账号上日历的同步 `{on, finding, name, state, error, last_sync, events, pending, waiting, full}`（`finding`：正在打开，在找日历；`state` 同账号的；`events` 是服务器上的日程数，`pending` 是手机上还没传上去的改动，`waiting` 是日历应用还没取走的服务器的改动，`full` 是因为日历满了）。`add` 的 `{kind, server?, user, password}`：系统把密码交给 keystored 封存、找到通讯录、服务器认了密码才留下账号（第一本能写的通讯录打开，别的关着；只有只读的时打开第一本只读的），回 `state` 的样子；同一个服务器的同一个用户已登录时拒绝。`sync` 的 `{id}` 马上同步（不等退避，也试被拒过的密码）；`set` 的 `{id, mobile_data}`，或 `{id, calendar}`（`calendar` 为真时用账号的密码找到服务器上的日历（CalDAV），找到了才回答，找不到回为什么；为假时停止日历的同步，日程留在手机上；日历只和一个账号同步，另一个账号开着时拒绝），或 `{id, book, on, remove_contacts?}` 开关一本通讯录（关掉时 `remove_contacts` 为真就删掉从它来的联系人，否则联系人照原样留着），或 `{default}` 设新联系人存进的通讯录（只读的拒绝）；`remove` 的 `{id, remove_contacts?}` 退出这个账号，`remove_contacts` 为真时删掉从它来的联系人和日历里的日程（手机上新建的留着），服务器上什么都不删。密码交出去以后谁也拿不回 | `system` |
| `calendar.pull` / `calendar.push` | 这个账号上日历的同步（[19](19-core-apps.md) §1.1），只给系统的日历应用（别的应用一律拒绝）：日程在应用和系统之间是 `{uid, ics?, gone?, copy?, all?, seq?}`，一个日程连同它单独改过的那几次的 iCalendar，或者它的删除。`pull` 的 `{taken?, full?}` 回 `{account, items, more?, want_all?}`：服务器上等着应用取的改动，按顺序，每次至多 50 个、约 512 KB（`taken` 是上一次回答里取走了的 `seq`；`full` 是应用因为放不下停了：不再给，剩下的留在系统里），`copy` 标着「服务器也改了这个日程，手机的改动另存成的新日程」，`want_all` 是要应用把全部日程交出来；没有账号在同步日历时 `account` 为 `""`。`push` 的 `{items, done?}` 把应用的改动交给系统（系统一直留着直到同步写到服务器，同一 UID 后来的改动顶掉前面的），`all` 标着 `want_all` 要的那次交出，`done` 是最后一批；回 `{account, pending}`，什么都没留下时 `account` 为 `""`。有改动在等时系统用深链接 `calendar.sync:` 叫醒应用，退出账号时用 `calendar.undo:account:<账号>` 让它删掉从这个账号来的日程 | — |
| `hotspot.state` / `hotspot.set` | 个人热点：`{backend, enabled, started, ssid, passphrase, clients, upstream?, security, hidden?, band, max_clients?, securities, can_hide?, bands, devices, usage, daily_limit?}`，`backend` 是 `iwd`、`sim`（开发机）或空（没有 Wi-Fi），`enabled` 是开关、`started` 是接入点已经起来，`passphrase` 是密码（设置里照手机的做法显示出来；第一次用时随机生成 12 位，网络名称默认是 `Kario-` 加 4 位十六进制数），`clients` 是连着的设备数，`devices` 是它们 `{mac, name?, address?, since}`（`name` 是设备申请地址时报的主机名，`since` 是连上的时间，Unix 秒），`usage` 是设备用的流量 `{session_down, session_up, today_down, today_up, limit_reached?}`（字节，按接入点网卡的计数器；关着时是上一次的；`limit_reached`：今天用满了每天的上限），`daily_limit` 是设备每天能用的流量（字节，0 不限）：用到 80% 时以「设置」的名义提醒一次，用满时关掉热点并发通知说明，当天再打开就不再关，`upstream` 是分享出去的网卡（默认路由的网卡，手机上是移动数据；没有时为空）；`security`（`wpa2`、`wpa2-wpa3`、`wpa3`）、`hidden`、`band`（`2.4`、`5`）、`max_clients`（0 是不限）是设置，`securities`、`can_hide`、`bands` 是这台手机的接入点做得到的（iwd 的：只有 `wpa2`，不能隐藏，`5` 要射频和所在国家允许）；`set` 的 `{enabled?, ssid?, passphrase?, security?, hidden?, band?, max_clients?, daily_limit?}`：名称 1–32 字节、不含 `/` 和控制字符，密码 8–63 个可打印 ASCII 字符，安全性、隐藏、频段只能取接入点做得到的，设备数上限 0–10，开着时改名称、密码、安全性、隐藏或频段会重启热点，改上限不重启、只管之后来的设备（超出的被请走）；回的是 `hotspot.state` 的结果。开着时 Wi-Fi 关着（只有一个射频），关掉后 Wi-Fi 回到打开热点前的样子；打开 Wi-Fi（`wifi.set`）会关掉热点；飞行模式下打不开，打开飞行模式会关掉它。Shell 让 iwd 把手机的 Wi-Fi 变成接入点（iwd 自己的 DHCP 服务器发地址，DNS 是手机在用的上游服务器），给这两块网卡打开 IPv4 转发，在防火墙里把热点的子网伪装（masquerade）成默认路由网卡的地址，只放回包进来（[17](17-app-sandbox.md) §1）；关掉时都撤掉。开关、名称、密码是 Shell 的设置，重启后保持；密码不写进日志。开发机上是模拟的热点，不分享网络；模拟器上 ✓，**未上真机** | `system` |
| `switch.state` / `switch.start` / `switch.stop` / `switch.undo` | 从旧手机迁移（kario-switch/1，[36](36-device-switch.md)），给设置用：`start` 的 `{kinds, hotspot?}` 是用户勾选的种类（`contacts`、`call_log`、`sms`、`calendar`、`photos`、`videos`，Kario 上没有去处的去掉），Wi-Fi 连着时在 Wi-Fi 的地址上听 TCP 47800，没连上或 `hotspot` 为真时打开个人热点、在热点的地址上听（`hotspot` 为真时还在 Wi-Fi 上等旧手机的会话先结束）；带 `bluetooth`（已配对设备的地址）时不开接收端，而是经蓝牙读那台旧手机的通讯录、通话记录和短信（PBAP、MAP，[36](36-device-switch.md) §8，种类里只取这三样）；四个都回 `{state, kinds?, code?, address?, network?, passphrase?, wifi?, bluetooth?, device?, progress?, error?, lost?, last?}`：`state` 是 `idle`、`starting`（热点正在打开，或正在连旧手机）、`waiting`（等旧手机；经蓝牙时是等旧手机的用户允许）或 `receiving`，`bluetooth` 是经蓝牙迁移时旧手机的名字，`code` 是二维码里的配对 URL（设置把它画在安全界面上），`network`、`passphrase` 是旧手机要连的热点，`wifi` 是 Wi-Fi 路上旧手机也要连的那个网络（两样只有一样），`progress` 是每类的 `{kind, expected, stored, skipped, failed, note?}`（经蓝牙时没全过来的那类有 `note`：`not_offered` 旧手机不经蓝牙提供、`refused` 旧手机上没有允许、`partial` 只过来一部分），`error` 是上次开始或撤销为什么不行，或者会话为什么自己停了：`lost` 是 `wifi`（Wi-Fi 断开、换了网络、地址变了）或 `hotspot`（热点被关掉），`last` 是上一次导入 `{at, ended, device, added, failed, undone}`（`ended` 是 `done`、`timeout`、`stopped`、`network`）；`stop` 结束会话（令牌作废，热点是它打开的就关掉）；`undo` 删掉上一次导入加的联系人、短信、通话记录和文件（按记下的 ID）。开发机上走模拟的热点和模拟的 Wi-Fi ✓，经蓝牙的走模拟的旧手机 ✓，**模拟器上没跑过**，**未上真机** | `system` |
| `settings.get` / `settings.set` | 系统设置（铃声、媒体、提示音音量，自动键盘锁，`silent` 静音模式，`ime_fuzzy` 模糊音，`large_text` 大字号，`developer_mode`，`device_unlocked` 解锁设备、`crash_send` 把一条崩溃记录发给开发者——这两项由系统弹框确认；`theme` 主题（值是 `themes` 里的序号），`wallpaper_clear` 恢复默认壁纸（1 待机、2 锁屏、3 都恢复）；`sim_pin_lock`（1 开、0 关）和 `sim_pin_change`：系统弹出自己的 PIN 码界面，PIN 码只交给 SIM 卡，设置应用拿不到，回复等用户在那个界面上办完；`language`、`region`、`time_zone`：`languages`、`regions`、`zones` 里从 1 起的序号，0 是自动；`auto_time`（1 网络时间，0 手动）、`time`（手动时间：时区里的日期和时间，按「这个钟面从 1970-01-01 00:00 起的秒数」给，应用不用知道时区）、`clock_24h`（1 是 24 小时制）；`refresh_rate`（屏幕刷新率：0 自动、1 标准 60 Hz、2 屏的最高一档，[08](08-display-adaptive-refresh.md) §5）；`usb_mode`（信任的电脑拿到什么：1 文件传输 MTP，0 网络共享，默认；从下一台电脑起生效，[27](27-usb-typec.md) §3.3）；`auto_system_update`（1 系统更新自己在后台下载安装、装好后问要不要重启，0 由用户装；默认关，产品可在配置里打开，[25](25-system-update.md) §6）；辅助功能（[19](19-core-apps.md) §1.4）：`high_contrast_text`、`bold_text`、`color_inversion`、`screen_reader`、`screen_reader_on_demand`（按需朗读）、`sticky_keys`、`mono_audio`（1 开、0 关），`color_correction`、`key_feedback`、`a11y_shortcut`（`ui.ColorCorrections`、`ui.KeyFeedbacks`、`ui.A11yShortcuts` 里的序号），`magnification`（2 或 3 倍），`key_repeat_delay`、`slow_keys`、`long_press`（毫秒，取 `ui.KeyRepeatDelays`、`ui.SlowKeyDelays`、`ui.LongPressDelays` 里的一个），`key_repeat_rate`（每秒几次，`ui.KeyRepeatRates` 里的一个，0 不重复），`audio_balance`（−1 只有左边到 1 只有右边，按 0.1 取整），`settings.get` 的 `settings.accessibility` 是现在的值；流量套餐（[19](19-core-apps.md) §1.3）：`data_limit`（每月流量，字节，0 不设）、`data_cycle_day`（每个周期从每月的第几天开始，1–31，当月没有这一天时用月末）、`data_warning`（提醒的流量，字节；0 改回按套餐的比例）、`data_warning_percent`（提醒设在套餐的百分之几，1–100，0 不提醒；没设过时是 80）、`data_limit_off`（1 用完套餐时挡住移动数据，0 只提醒）；后台数据和应用自己的上限（[19](19-core-apps.md) §1.3）：`data_saver`（1 打开流量节省：应用只在屏幕上时能用移动数据，不受限制的除外）、`app_background_data`（`text` 是应用 ID；0 关掉它的后台数据，1 打开，默认）、`app_data_unrestricted`（`text` 是应用 ID；1 不受流量节省限制）、`app_data_limit`（`text` 是应用 ID；它每个周期的移动数据，字节，0 不设：用到 80% 时提醒，用满后挡住它的移动数据，到周期结束或调高为止）——应用要有 `net` 权限；安全（[31](31-lock-and-biometrics.md) §6）：`lock_pin`（1 设置或修改，0 删除）、`fingerprint_auth`（管理指纹前先确认 PIN 码）、`fingerprint_enroll`（录入）——这三项弹系统自己的 PIN 码或录入界面，设置应用拿不到 PIN 码和指纹，回复等用户办完；`lock_grace`（息屏后多久锁定，秒）、`lock_show_content`、`fingerprint_unlock`、`fingerprint_pay`（开关）、`fingerprint_rename`（值是指纹 ID，`text` 是新名字）、`fingerprint_delete`（值是指纹 ID）；恢复出厂设置（[31](31-lock-and-biometrics.md) §11）：`factory_reset`（1 清除，2 清除并保留防盗锁）——系统先弹自己的 PIN 码界面（未成年人模式下是家长 PIN 码）和最后确认，用户停在那里时回复取消，否则手机重启；紧急警报（[32](32-messaging.md)）：`alert_extreme`、`alert_severe`、`alert_amber`、`alert_test`（1 显示、0 不显示；最高级别没有开关，总是显示）；`sms_app`（默认短信应用：`text` 是应用 ID，空是系统的短信；商店装的应用系统先问，回复等用户答完）；位置（§4.7）：`location`（1 开，默认；0 关，所有应用都拿不到位置）、`location_approximate`（`text` 是应用的授权键，即应用 ID 或「应用 ID/小程序」；1 只给大致位置，0 给精确位置）、`location_network`（1 开，默认；0 不再问网络定位服务，存在手机上的结果一起删掉；没有网络定位服务的手机回错误））、`location`（`on` 总开关，`receiver` 接收机：`sim` / `fifo` / `nmea` / `modemmanager`，空是没有；`on_now` 接收机开着，`error` 它上次的错误，`using` 正在用位置的应用，`recent` 最近 24 小时用过位置的应用 `{app, name, at, bg_at?}`（毫秒；`bg_at` 是 24 小时内最近一次在后台用），`approximate` 只给大致位置的应用，`network` 网络定位 `{provider, host?, on?, error?, requests?, cached?}`（`provider` 是 `http`、`fake` 或空），接收机经 ModemManager 时还有 `assistance` `{modem, supl?, agps?, injected?, held?, error?}`：上次打开时给了它什么辅助数据）、`data_usage`（流量，[19](19-core-apps.md) §1.3：`{source, start, end, mobile, wifi, roaming?, mobile_today, prev_mobile, lines, plan, held?, override?, apps, data_saver?}`；`source` 是计数从哪来：`apps`（防火墙按应用的计数器，加上网卡的计数器算系统的）、`links`（只有网卡的计数器：内核没有按应用的计数器）、`sim`（开发机，自己不计数）；`start`、`end` 是这个周期的第一天和下个周期的第一天；`mobile` 是本国网络上的移动数据（套餐算它），`roaming` 是在国外漫游的，另记；`lines` 是各应用 `{id, name?, mobile_rx, mobile_tx, wifi_rx, wifi_tx, roam_rx?, roam_tx?}`，另有 `hotspot`（热点上的设备）、`system`（系统和应用的计数器没看到的）、`removed`（已卸载的应用），按移动数据从多到少排；`plan` 是 `{limit?, cycle_day, warning?, warning_percent?, limit_off?}`；`held`：硬上限正挡着移动数据，`override`：用户确认过超出上限；`apps` 是有 `net` 权限的应用 `{id, name, background_off?, unrestricted?, wifi_only?, limit?, used, blocked?}`，按名字排：`used` 是它本周期国内外的移动数据（它的上限算这个），`blocked` 是它的移动数据现在为什么被挡（同 `net.state`）；`data_saver`：流量节省开着）、`alerts`（各级别的名字、开关和是否锁定）、`roles`（`sms`：`app` 拿着角色的应用、`choices` 可选的应用 `{id, name, system}`）、`blocking`（拦截名单，同 `block.list`）、`usb`（`mode`：`mtp` 或 `network`；`sharing`：正和电脑共享）、`display`（刷新率：`{up, panel, rates, hz, reason, refresh_rate}`：phone-displayd 在不在、面板、它有的刷新率、现在的刷新率和原因、设置的序号）、`security`（是否设了 PIN 码、宽限时间和选项、传感器、指纹的 ID 和名字、开关、指纹暂时不能用的原因）、`reset`（`supported` 这台设备能不能在这里恢复出厂设置、`minor`、`pin`，防盗锁开着时 `theft` 是 `{email}`）、「关于本机」的信息、最近 10 条应用崩溃记录、`themes` 和 `theme_index`、`sim`（`state`、`pin_lock`）、`languages`（四种语言，名字用它自己的文字写）、`language` 和 `language_auto`（生效的和自动会选的）、`regions`（按当前语言排序的地区名）、`region` 和 `region_auto`（`code`、`from`：`sim` / `sku` / `default`）、`zones`（每个时区的城市名和现在的 UTC 偏移）、`region_zones`、`time_zone` 和 `time_zone_auto`（`id`、`city`、`from`：`user` / `network` / `region`）、`clock`（当前日期时间的文字、能不能手动设时间、NTP 用的是什么）；`set` 的参数是 `{key, value, text?}` | `system` |
| `system.update` | 系统更新（A/B 系统槽）：参数 `{action}`，`status` 返回当前版本、正在运行和下次启动的系统槽、安装状态，装的时候还有 `progress`（`percent` 是 RAUC 的百分比；增量包有 `done`、`total`、`resumed`：应用器做到第几步、共几步、从第几步接着装的）和等着重试时的 `retry`（`attempt`、`of`、`at`），自动装好的有 `automatic`，选了今晚重启的有 `tonight`，自动更新装到一半因条件没了停下、等着接着装的有 `paused`（为什么等着，给用户看的话，[25](25-system-update.md) §6）；`check` 读更新服务器的 `latest.json`（分批推送：`rollout` 是已推给的设备比例（%），`paused` 暂停推送；每台设备按自己的随机分桶决定，分桶不上传），没轮到时 `held` 写明原因；`install` 把更新包流式写进没在运行的那个槽（签名不可信、不是给这台设备的都拒装；只下载两个槽里都没有的数据块），下载断了过 30 秒、2 分钟、8 分钟再试（增量包接着装，[25](25-system-update.md) §5.6），装完或三次都不行才回复；`reboot` 重启进新系统 | `system` |
| `settings.apps` / `settings.app.set` | 各应用在清单里声明的设置和当前值；改其中一项（`{app, key, value}`，类型和选项由系统核对），应用随即收到 `settings` 事件 | `system` |
| `pkg.install` | `{file, sha256?, auto?}`：调用方数据目录里的 `.phoneapp` 包，或从已装版本起的增量包（系统用已安装的文件补齐）。系统验签、检查清单（给了 `sha256` 还核对整包摘要），再弹框列出开发者和权限，用户确认后安装或更新（§12）。`auto: true` 是商店在后台装的更新：不改变用户同意过的东西（已安装、同一开发者、版本更高、不多要权限、不新带原生模块、应用不在前台也没在后台工作）时不弹框就装，否则回 `the update needs the user` | `install` |
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
- 图片节点写 `src: "media:<id>"`（联系人照片 `contact:<photo>`），由系统解码显示。

### 4.6.1 网页节点

| 能力 | 参数 | 结果 | 权限 |
|------|------|------|------|
| `web.control` | `{screen, node, op, text?, value?}` | 控制自己的 `web` 节点：`reload`、`back`、`forward`、`stop`，`zoom`（`value` 0.5–3），页内查找 `find`（`text`）、`find_next`、`find_prev`、`find_end`，`clear_data`（清掉本应用的网站数据：Cookie、缓存、本地存储），`sites`（系统界面列出存着数据的网站，用户选一个删掉；应用拿不到列表）。页面的状态经 `web` 事件来 | — |

页面里的下载完成后进系统的「文件」区（最大 16 MB），系统提示并发通知，`<input type=file>` 由系统的文件选择器交出一个文件，`prompt` 显示成系统界面；应用清单里的 `content_blocking` 设置打开时，网页进程按系统的规则拦截广告和跟踪脚本（`share/phone/web/blocklist.json`）。参考：浏览器应用（`apps/browser`）。

### 4.7 位置与后台任务

| 能力 | 参数 | 结果 | 权限 |
|------|------|------|------|
| `location.get` | `{max_age_s?, timeout_s?, approximate?}` | 一次定位：`{lat, lon, accuracy_m, at, source}`（WGS-84 度数；水平精度，米，约一个标准差；定位的时间，Unix 秒；`source` 是 `gnss` 卫星或 `network` 网络定位），接收机给得出时还有 `altitude_m`（海拔，米）、`speed_mps`（对地速度，米/秒）、`bearing`（航向，正北起顺时针的度数）、`satellites`（参与定位的卫星数）。`max_age_s` 秒内的上一次定位直接交回（默认只要当前的，2 秒内），否则打开接收机等下一次定位，最多 `timeout_s` 秒（默认 30，最多 120），等不到返回 `no location fix in time`（括号里是接收机的错误）。`approximate: true`：应用只要大致位置（下文），问用户时授权框也只给这一种。只有坐标，地名请应用自己换算 | `location`（运行时） |
| `location.watch` | `{interval_s?, min_distance_m?, approximate?}` | 持续定位：发 `location` 事件（值同上），相隔至少 `interval_s` 秒（默认 1，1–3600），离上次发出的位置至少 `min_distance_m` 米才发；每个应用一个，再调用就替换。应用到后台时暂停（没人要了接收机就关；用户「始终允许」的应用不暂停，见下文），回到前台接着发，有当前的定位先发一次；应用退出、授权被收回时停止。大致位置的持续定位只在换了格子时发，同一个格子 10 分钟最多再发一次。回复 `{watching: true}` | `location`（运行时） |
| `location.stop` | — | 停止自己的持续定位，回复 `{watching: false}` | — |
| `job.schedule` | `{id, every_min, charging?, unmetered?, network?}` | 后台定时任务：每隔 `every_min` 分钟（至少 15）系统唤醒或在后台启动应用，发 `job` 事件。每次运行有个时间窗口：从到期起，到半个周期后（最多 30 分钟）截止。系统不会为了没到截止的任务唤醒手机：有任务到截止时，所有窗口已开的任务在同一次唤醒里一起跑；刚收到推送（射频醒着）时也顺带跑。`charging: true` 的任务只在接着电源时跑，从不单独唤醒，插上电源后运行；它们可以做重活（商店的自动更新），每次有 1 分钟才被再冻结，一般的唤醒是 3 秒。`unmetered: true` 的任务只在不计费的 Wi-Fi 上跑（大的下载；移动数据和按流量计费的 Wi-Fi 上等着，[25](25-system-update.md) §6.1），商店的自动更新两样都要。这样的任务跑着时（这次唤醒的时间里，系统每 2 秒看一次网络）手机离开了不计费的 Wi-Fi（换到移动数据、按流量计费的 Wi-Fi，或者没网了），系统照安卓 JobScheduler 的做法停掉它：应用收到 `job.stop` 事件（§5），应该停下下载、记好进度；5 秒后这次运行的唤醒结束（在后台的应用被冻结；在前台的、或者另有任务在跑的不冻结）。任务的周期和条件都不变，回到不计费的 Wi-Fi 后马上重跑（为它唤醒手机），不等下一个周期。`charging` 的任务跑着时拔掉电源也一样（系统每次例行检查时看电源，大约每秒一次）：应用收到 `job.stop`（原因 `unplugged`），5 秒后这次唤醒结束，再插上电源时马上重跑。商店收到它（不管哪种原因）就断开后台正在下载的更新包，重跑时再下。`network: true` 的任务要有网络才跑：没网时等着，应用的移动数据在后台被挡住时（关了后台数据、开着流量节省、它自己的上限用完，[19](19-core-apps.md) §1.3）等 Wi-Fi（按流量计费的也行），照安卓 JobScheduler 对受限应用的做法；跑着时手机换到它不能用的网络，同样被停掉（`job.stop` 的 `reason` 是 `blocked` 或 `offline`）。和推送共用每小时的唤醒预算，超出预算的等应用回到前台再给。同一个 `id` 再登记就替换旧的；重启后仍然有效 | — |
| `net.state` | — | 手机现在的网络和应用的移动数据是不是被挡住（[19](19-core-apps.md) §1.3）：`{network, metered?, background_data, data_saver?, blocked?, limit?, used?}`；`network` 是 `wifi`、`mobile` 或 `none`，`metered` 是移动数据或按流量计费的 Wi-Fi，`background_data` 是它在后台能不能用移动数据（用户关了它的后台数据，或开着流量节省、它不在不受限制的应用里，就是假），`blocked` 是它的移动数据现在为什么被挡，不管手机在什么网络上：`wifi_only`（应用权限里只用 Wi-Fi）、`app_limit`（它自己本周期的上限用完了）、`background_data`（它不在屏幕上，后台数据关着）；在移动数据上 `blocked` 时它的连接马上失败（设备上防火墙给 EPERM；fetch 被拒）。`limit` 是它自己每个周期的上限（字节），`used` 是用了多少（国内外的移动数据）。挡不挡一变，在运行、没被冻结的应用收到 `net.state` 事件（§5）。开发机上没有防火墙，挡不挡只是 Shell 的策略，应用的连接照样能通；内核没有按应用防火墙的手机也是这样 | `net` |
| `job.cancel` | `{id}` | 取消自己的任务 | — |

位置默认只给前台应用：后台应用的请求返回 `location is for the foreground app only`，在系统问用户要授权之前就拒绝（不会在别的应用上面弹出位置授权框），正在等定位的 `location.get` 在应用离开前台时也这样结束。系统设置里的位置总开关（`location`，§4.5）关着时，两项请求都返回 `location is off in Settings`，接收机立即关掉；关掉的那一刻正在持续定位的应用收到 `location.off` 事件，持续定位留着但不再发，打开后接着发。既没有定位接收机、也没有（或关掉了）网络定位的手机返回 `this phone has no location receiver`。有应用在持续定位、有 `location.get` 在等定位、或 3 秒内交出过定位时，状态栏显示位置图标；最近 24 小时谁用过位置（哪些是在后台用的）记在「设置 › 位置」里。

**精确和大致位置**（Android 12）：第一次要位置时，系统的授权框给三个选择：「精确」「大致」「拒绝」（默认精确，左右键切换），选的精度按应用记住；请求带 `approximate: true` 时授权框只给「大致」和「拒绝」。只给大致位置的应用（用户选的，或请求自己只要大致）拿到的是约 2 公里见方的格子的中心（Android 的 LocationFudger 用 2000 米）：格子的位置每台设备随机错开，同一个地方总落在同一个格子，所以反复定位求平均也只能得到格子；在格子边上，定位的抖动不会让它在两个格子之间来回跳（离开上一个格子不到 0.15 个格子时仍给上一个格子），否则来回跳的位置会暴露格子的边、也就暴露了人在哪；`approximate: true`，`accuracy_m` 至少 2000，没有海拔、速度、航向和卫星数。用户之后可以在「设置 › 位置」或「设置 › 应用权限」（`p` 键）里改精度。

**后台定位**（Android 10/11）：应用在清单里声明 `location_background`（运行时权限，要写 `purposes`），授权框从不问它，只问「使用时」的位置；用户在「设置 › 位置」（`b` 键）或「设置 › 应用权限」（「后台使用位置」那一行）里改成「始终允许」后，应用不在前台、但没被冻结时（后台任务的唤醒窗口里、冻结前的宽限期里）也能拿到位置，持续定位也不暂停；没给时这样的请求照旧被拒（不弹框）。声明了 `location` 后台模式的应用在「始终允许」且正在持续定位时不被冻结（iOS 的 location 后台模式）。小程序没有后台定位。每次在后台用位置都记进最近访问（`bg_at`）；系统每小时看一次，在后台用过位置的应用以「设置」的名义发通知提醒（「有应用在后台使用了你的位置」），同一个应用一周最多提醒一次、且只在上次提醒后又用过时提醒；点开通知就是「设置 › 位置」。90 天没打开的应用，这项授权和其他运行时授权一起被收回。

接收机按硬件 SKU 选（[24](24-sku-framework.md) §2 的 `gnss`）：开发机上是模拟接收机，它输出真的 NMEA 0183 语句，走和真机一样的解析器（`location/`）；QEMU 读 Shell 运行目录里的命名管道，自测往里写 NMEA 语句；真机读内核 GNSS 子系统的 `/dev/gnssN` 或串口，或经 ModemManager 的 Location 接口读调制解调器的 GPS（**未上真机**）。接收机只在有人要定位时打开，最后一个请求结束 5 秒后关掉；接收机出错时（设备不在、ModemManager 没有调制解调器）隔 5 秒到 1 分钟再试。

**辅助定位（A-GNSS）**：接收机是 ModemManager 的调制解调器时，每次打开前先给它辅助数据（`location.ModemManager`）：调制解调器支持 A-GPS（`Capabilities` 里有 `AGPS_MSB` 或 `AGPS_MSA`）且产品配置了 SUPL 服务器时，`SetSuplServer` 设好服务器（已是这个就不设），和 GPS 一起打开 A-GPS（优先 MSB）；`Setup` 不接受 A-GPS 时退回只开 GPS。调制解调器支持预测星历注入（`SupportedAssistanceData` 里有 XTRA）时，从它的 `AssistanceDataServers` 下载数据（只认 http/https，最大 1 MB），存在状态目录（`shell/gnss/`，0600），一天更新一次，下载不到时一周内的旧数据照样用，失败后一小时内不再试；`InjectAssistanceData` 注入，同一份数据一天内不重复注入。哪一步失败都不影响接收机照常开。用完把调制解调器的定位源恢复原样。上次给了什么在 `settings.get` 的 `location.assistance` 里，设置页显示。产品配置层（`profile.d`，[14](14-tui-route-and-app-framework.md) §2.1）的 `location`：`supl_server`（`host:port`，空是不用 SUPL）、`no_assistance_data`（true：不下载预测星历）。用模拟的 ModemManager 测过调用顺序和参数；**未上真机**，真的 SUPL 服务器和 XTRA 服务器**未验证**。

**网络定位**：接收机没有定位（开始要位置 5 秒后还没有，或者丢了）时，按附近的 Wi-Fi 接入点和所在的基站问网络定位服务，最多每 30 秒一次；卫星一有定位就不再问。只在总开关开着、有已经授权的应用在要位置时问；只发 `location.Observation.Usable` 允许的：隐藏网络（没有名字）、名字里有 `_nomap` 或 `_optout`（网络主人不愿被收录）的、随机或组播的 MAC 地址都不发，最多发最强的 20 个，不到 2 个就不发 Wi-Fi；基站要有完整的标识（MCC、MNC、LAC/TAC、CI）。请求不带按 IP 定位（`considerIp: false`，`fallbacks` 全关）。服务的结果存在手机上（状态目录 `shell/netloc.json`，0600，一周）：以后听到同样的网络（至少 2 个、而且至少一半相同的接入点；只有基站时是同一个基站）就直接用，「不认识」的结果记一小时；每小时最多问 60 次（产品可调低）、两次至少隔 10 秒，服务出错后隔 1 分钟起翻倍到 30 分钟再问。得到的定位 `source: "network"`，没有海拔、速度和卫星数；只给大致位置的应用同样只拿到格子。用户可以在「设置 › 位置」里关掉「Wi-Fi 和移动网络定位」，关掉时存在手机上的结果一起删掉。服务由产品配置层的 `location.network` 指定：`provider` 是 `http`（说 Geolocation API 的 JSON 的服务：Google 的 geolocate、Mozilla Ichnaea 和用它的 BeaconDB 等；`url` 必须是 https，本机回环地址可以 http）、`fake` 或 `none`；要 API 密钥的服务（国内地图厂商的定位接口和多数国外服务都要）用 `key_file`（设备上一个文件的路径）或 `key_env`（环境变量名）给出，密钥由手机的主人自己申请、放在镜像和仓库之外，系统不带任何密钥，也不把它写进日志和错误；`max_per_hour` 按服务的配额调低次数。没有配置时，模拟的手机（开发机、模拟调制解调器的 QEMU）用模拟的服务（`location.FakeProvider`：按模拟接收机的位置给，Wi-Fi ±25 米、只有基站 ±1200 米），真机没有网络定位。Wi-Fi 来自 iwd（每个网络的 `ExtendedServiceSet` 里各个 BSS 的地址，信号用网络的；已知网络的 `Hidden` 标为隐藏）或模拟的 Wi-Fi；基站来自 ModemManager Location 接口的 3GPP 源（`MCC,MNC,LAC,CI,TAC`，读之前没开就打开），模拟的手机用一个模拟基站。国内地图厂商自己的接口格式（不是 Geolocation API 的 JSON）**未实现**，要接时在 `location.Provider` 后面加一个实现；它们返回的 GCJ-02 坐标也要先换回 WGS-84。真的服务、iwd 的 BSS 地址和 ModemManager 的基站**未上真机**、**未验证**。

参考天气应用（`apps/weather`）：前台定位；后台定时刷新时试着要位置（`max_age_s` 1800），用户「始终允许」时拿得到，否则按上次的位置刷新；「我的位置」页用 `location.watch` 实时显示坐标、精度、海拔、速度、航向和卫星数，网络定位时写明来源，返回时 `location.stop`。

### 4.8 剪贴板

| 能力 | 参数 | 结果 | 权限 |
|------|------|------|------|
| `clipboard.set` | `{text}` | 复制文字（最多 16 KB），系统提示「已复制」 | — |

- 剪贴板**只能写，不能读**，所以应用没法在后台偷看剪贴板。
- 粘贴由系统完成：输入框获得焦点时，系统菜单的第一项是「粘贴「…」」（按 `v`；全键盘上也可按 Ctrl + V），文字插在光标处，应用只收到输入的结果。用户在输入框里选中、复制的字也进这个剪贴板（系统菜单的「复制」，或 Ctrl + C）。
- 10 分钟后剪贴板自动清空。

### 4.9 分享

| 能力 | 参数 | 结果 | 权限 |
|------|------|------|------|
| `share.open` | `{kind: "text", text}`、`{kind: "image", media}`、`{kind: "link", url, text}`（`text` 是标题）或 `{kind: "file", file}` | 系统的「分享到…」面板，列出清单里声明能接收这类内容的应用；用户选中后返回 `{shared: true}`，取消则报 `cancelled`。接收方用深链接 `share:<kind>:<内容>` 打开；分享的照片和文件只授权那一个给它 | — |
| `file.save` | `{name, data}`（base64，最大 16 MB） | 把一个文件存进系统的「文件」区（文档、附件），系统先问用户；返回条目 `{id, name, mime, size}` | — |
| `file.pick` | `{types?, read?}`（扩展名或 MIME 前缀） | 系统列出「文件」区里的文件，用户选中的那一个以只读文件随回复交给应用；`read: true` 时改成把内容放在回复的 `data` 里（base64，至多 1 MB，`ui.MaxFileRead`），给收不了文件的运行时（TypeScript 应用） | — |
| `file.open` | `{id, read?}` | 打开交给过这个应用的文件（分享给它的、它选过或存过的），只读文件随回复交出；`read` 同上 | — |

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
- 系统应用处理的：`contact.save`（通讯录：`payload` 是联系人，至少有 `number`；打开填好号码的新建页，用户存好后答复 `{id}`，返回则 `ok: false`；电话的号码框和最近通话、短信的会话里「保存号码」用它），`message.compose`（短信：`payload` 是联系人；打开和这个号码的会话，用户离开会话时答复；通讯录的「发短信」用它）。

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

### 4.12 屏幕刷新率（[08](08-display-adaptive-refresh.md)）

| 能力 | 参数 | 结果 | 权限 |
|------|------|------|------|
| `display.hint` | 三者之一：`{animating_ms}`（应用自己在画动画，至多 3000，要继续就再发）、`{fps}`（内容的帧率，1–240，直到收回）、`{clear: true}`（收回） | `{hz, rates}`：面板现在的刷新率和它有的刷新率（从低到高）。刷新率由系统（phone-displayd）决定：动画时用屏的最高一档，内容按帧率挑能整除它的一档（24 帧在 60/120Hz 的屏上用 120） | —；只有前台应用 |

- 只是提示，不是命令：设置里固定了「标准」或「高」、电量低、手机发热时系统照样封顶，应用不会因此报错。系统界面的滚动、焦点和转场由系统自己提示，应用不用发。
- 只收前台应用的（在屏幕最前面、屏幕亮着、键盘没锁；上面弹着系统菜单时照收），否则回 `only the app in front hints the display`；每个应用一次至多 8 个，之后每秒 4 个（`ui.DisplayHintBurst`、`ui.DisplayHintPerSecond`），多了回 `too many display hints`。应用离开前台时系统收回它的提示，回到前台（`lifecycle` 的 `foreground`）后要再发。没有 phone-displayd 的系统回 `no display service`。
- Go SDK：`App.HintDisplay(ui.DisplayHintArgs{AnimatingMS: 1000}, done)`；TypeScript：`call("display.hint", {animating_ms: 1000})`，类型 `DisplayHintArgs`、`DisplayHintRes`（`ui.d.ts`）。
- 原型里没有应用用它；单元测试（`shell` 的 `TestDisplayHintFromApps`、`sdk` 的 `TestHintDisplay`）✓。

## 5. 事件（`event`）

| 名称 | 值 | 什么时候 |
|------|----|----------|
| `time.zone` | 新时区的 IANA 名称 | 时区变了；SDK 已经把它设进 `TZ`，应用重画显示时间的地方就行 |
| `open` | 深链接，同 `start` 的参数 | 应用已在运行，又被要求打开某处（如待机时按数字，电话应用收到 `dial:<号码>`） |
| `push` | `{id, kind, data, sent, notified, collapse_id}` | 推送的数据部分；系统可能为此把冻结的应用恢复几秒，或在后台启动它。`notified` 为 true 表示系统已经显示了这条推送的通知（应用在前台、或用户关了它的通知时系统不显示），应用就不要再发自己的通知 |
| `push.key_changed` | — | 设备解锁或重新上锁，密钥服务清空了全部应用密钥：推送密钥是新的，旧公钥加密的数据解不开。用了端到端加密推送的应用要重新 `push.key`，把新公钥交给自己的服务器。冻结的应用会为此恢复几秒，没在运行的不会被启动（它们启动时本来就取钥） |
| `focus` | `lost` / `gained` | 系统的菜单或对话框盖到应用的页面上、或者息屏了（`lost`），按键又归应用了（`gained`）；对应安卓的 `onWindowFocusChanged`。应用还在前台，生命周期不变；游戏在 `lost` 时暂停（贪吃蛇就这样做） |
| `audio` | 流的状态 `{id, state, pos_ms, …}` | 自己的流变化（播完是 `ended`） |
| `media` | `next` / `previous` | 用户按了媒体键（耳机线控、键盘），发给正在播放的应用；播放 / 暂停由系统直接处理 |
| `settings` | `{key, value}` | 用户在「设置 › 应用设置」里改了这个应用声明的设置 |
| `web` | `{screen, node, url, title, loading, can_back, can_forward, zoom}` | 自己的网页节点的状态变了（打开了新页面、加载完、缩放） |
| `alarm.fire` | 闹钟编号 | 自己的闹钟响了（系统已经显示全屏闹钟） |
| `notify.fired` | `{id, missed}` | 自己的一条定时通知刚由系统显示（`missed`：关机期间错过的）；只发给正在运行的应用（冻结的解冻后读到），不为它唤醒、启动应用（§4.4） |
| `job` | 任务编号 | 后台定时任务到期（§4.7）；应用可能刚被唤醒，几秒后会再次冻结 |
| `job.stop` | `{id, reason}` | 正在跑的后台任务要的网络或电源没了：`reason` 是 `metered`（要不计费网络的任务换到了移动数据或按流量计费的 Wi-Fi）、`blocked`（要网络的任务换到了移动数据，而应用的移动数据在后台被挡住）、`offline`（没网了）或 `unplugged`（`charging` 的任务，电源拔掉了）；应用停下手里的活、记好进度，几秒后系统结束这次运行，网络或电源回来后重跑（§4.7） |
| `net.state` | `net.state` 的结果 | 应用的移动数据被挡住或放开了（关了后台数据的应用离开、回到屏幕，它自己的上限用完、调高，流量节省开关，应用权限改成只用 Wi-Fi）；只发给在运行、没被冻结的应用（§4.7） |
| `sms` | 短信 | 收到短信，或发出的短信状态变了（`state`：`sending` / `sent` / `delivered` / `failed`，同一个 `id`），或短信被删、被标为已读（值为 `{}`）（`sms` 权限） |
| `contacts` | — | 通讯录变了（别的应用经系统确认写入、蓝牙收到的名片加进来了），要显示联系人的重新 `contacts.list`（`contacts` 权限，只发给正在运行的应用） |
| `tel.call` / `tel.open` | 通话 / 通话编号 | 电话应用专用 |
| `call.resume` | 通话编号 | 用户从系统菜单「返回通话」 |
| `nfc.apdu` | `{session, card, aid, apdu}` | 读卡器对自己的卡发来一条命令（十六进制），用 `nfc.reply` 回答（§4.11，`nfc` 权限） |
| `nfc.deactivated` | `{session, card, reason}` | 读卡器用完了这张卡：`reason` 是 `link_loss`（场没了、休眠或 DESELECT）或 `deselected`（选了别的 AID）（§4.11，`nfc` 权限） |
| `location` | 一次定位（同 `location.get` 的结果） | 自己的持续定位有了新的位置（§4.7，只在前台） |
| `location.off` | — | 用户在系统设置里关掉了位置；持续定位留着，不再发，打开后接着发（§4.7） |

## 6. 权限与后台

| 类别 | 权限 | 说明 |
|------|------|------|
| 运行时（第一次使用时询问，用户可在设置里撤销） | `camera`、`microphone`、`contacts`、`photos`、`location`、`nfc_reader`（读贴在手机上的 NFC 卡）、`location_background`（后台也拿位置：从不弹框询问，只能由用户在设置里给） | 授权框由系统画，应用无法仿冒；商店安装的应用 90 天没打开，这些授权由系统收回，下次再问；`location` 的授权框可选精确或大致位置，之后也可在「设置 › 位置」「设置 › 应用权限」里改（§4.7） |
| 声明即有 | `keystore`、`attest`、`notifications`、`voip`、`net`、`phone`、`sms`、`nfc` | 商店自动审核只拒绝 `system`、`install` 和预先授权，新应用和多要权限的更新进人工审核；`phone`、`sms` 也不让商店应用自己打电话、发短信：每次都由系统请用户确认（§4.2）。运行时权限要在清单的 `purposes` 里写明用途，授权框里显示，审核台核对 |
| 仅系统应用 | `system`、`install`（应用商店） | 安装器拒绝声明了它们的包；已安装的应用即使声明了也拿不到 |

后台规则（[17](17-app-sandbox.md) §5）：
- 离开前台 10 秒后进程被暂停（冻结）。
- 内存紧张时系统可能结束后台的应用（不算崩溃）。用户回来时它在原来的页面上：系统先显示原来的页面，新进程启动时收到离开前台时交上来的 `state`（Go SDK：`OnSave` 返回要存的东西，`Restore` 读回来），照着重建页面；屏幕 ID 不变的页面，输入框里没发出的字和焦点由系统还原。
- 例外：`voip` 后台模式；正在播放的 `audio` 模式；「始终允许」位置、正在持续定位的 `location` 模式；通话中。
- 要在后台收消息，请用推送：只带标题、正文的推送由系统显示，不唤醒应用；带数据的推送按预算短暂唤醒应用（每个应用每小时最多 20 次）。服务器发推送时带 `collapse_id`，同一个键的新推送会替换还没送到的旧推送和它的通知（未读数、比分这类只要最新值的内容）。
- VoIP 推送唤醒应用后，应用要在 10 秒内报告来电（`kind: "call"` 的通知）；24 小时内 3 次没报告，之后的 VoIP 推送按普通数据推送处理。
- 推送带 `priority: "normal"` 时，数据不立刻唤醒冻结的应用，等应用因为别的原因醒来（回到前台、别的唤醒）时一起交付，最迟 15 分钟后系统统一唤醒一次。
- 应用在通话中（VoIP 或电话）开的音频流和录音都算「通话」类别：通话时有优先权，离开前台也不会被停（[20](20-audio.md)）。
- 通知：每个应用每分钟最多 5 条带横幅和提示音，超出的只进通知中心；通知中心每个应用最多留 24 条，最旧的先删。用户可以在「设置 › 应用权限」里关掉一个应用的通知（`notifications` 和网络一样只有「允许」「关闭」两档），关掉后它的通知直接丢弃，来电不受影响。定时通知（§4.4）到点时也照这些规则显示：在横幅的配额里，用户关了就不显示。
- 通知上的动作（快捷回复）：通知中心里选中应用的动作，系统在后台把它交给应用（冻结的先解冻，不在运行的在后台启动，不占推送的唤醒预算），然后清掉这条通知。
- 要定时同步，请用后台任务（§4.7），它和推送共用同一份唤醒预算。
- 参考鸿雁的做法（[21](21-push.md) §4）：不常驻；上线时把推送令牌交给自己的服务器；对方的消息作为「通知 + 数据」推送下来，并带会话 `thread`；来电用 VoIP 推送。
- TypeScript 应用的写法：教程 [34](34-app-tutorial.md) §7.1（推送）、§7.2（后台任务），示例 `examples/karioui-notes`。

## 7. 文件与共享内存（fd 传递）

有些能力的回复会附带打开的文件，消息的 `fds` 字段写明有几个：

| 能力 | 附带的文件 | 你能做什么 |
|------|------------|------------|
| `audio.stream`、`audio.record` | 一块 memfd，48 kHz 单声道 16 位的环形缓冲区，大小已经封住 | 映射后读写样本；格式见 `audio/ring.go`，Go SDK 是 `app.GoAudio`，TypeScript 是结果的 `ring`（§9.2） |
| `media.pick` | 选中照片的只读文件（PNG 或 JPEG，看条目的 `mime`） | 读取、上传；写不了，也接触不到相册里的其他文件。Go SDK 是 `app.CallFile`，TypeScript 是结果的 `files[0]`（§9.2） |
| `file.pick`、`file.open` | 用户选的、或交给过这个应用的那个文件，只读（§4.9） | 同上 |

规则：
- **不限制每次只能带一个 fd。** 一条消息最多带 8 个，这只是防止单条消息过大的上限。真正要管住的是下面几条：
- **接收**：文件随它那条消息的第一个字节到达，所以整行到了，它的文件就都到了；消息的 `fds` 写几个，就按到达的顺序取几个。Go SDK（`wire` 的 `TakeFiles`）、C SDK（`phoneui_fds` / `phoneui_take_fd`，没取走的在下一条消息时关掉）、kario-jsrt 都这样取；没有消息认领的文件至多留 32 个，多的关掉。
- **方向**：文件只从系统流向应用。应用发给系统的 fd 会被立即关闭，所以应用没法用塞 fd 的方式耗尽系统的文件描述符表。
- **数量**：每个应用最多同时开 4 路音频流，其中录音只能 1 路；系统服务另有总上限。应用进程自己的打开文件数受沙箱限制（1024）。
- **类型**：系统只发它自己验证过的对象，比如封住大小的 memfd、只读打开的文件。收到的一方也会再检查，比如环形缓冲区没封印就拒绝映射。

## 8. 沙箱里的限制

- 只能读写 `PHONE_DATA_DIR`。没有共享的 `/tmp`，临时文件放在 `$TMPDIR`（数据目录下）。
- 不能执行数据目录里的文件，也就是不能下载代码来运行。小程序请用宿主提供的 JS 运行时。TypeScript 应用的原生模块（§9.3）只从包里加载；Landlock 只管 `execve`，管不到原生代码把文件映射成可执行页，所以模块加载完以后 kario-jsrt 用 seccomp 封住可执行内存（§9.3）。
- 没有 `net` 权限就没有网络；有权限也不能监听端口。
- 看不到其他进程，不能调试别的进程，危险的系统调用被拦截。
- 内存上限 192 MB（不用交换），进程和线程合计最多 128 个。超过内存上限时，系统只结束你的应用，并提示用户「超出内存上限」；数据要及时存盘。Go 程序会收到 `GOMEMLIMIT=144MiB`，垃圾回收在硬上限之前就会加紧。
- 系统内存紧张时，先结束在后台的应用，最久没用的先结束；前台应用、通话中、正在后台播放的应用不会被选中。被结束的应用下次打开时冷启动，所以收到 `lifecycle: background` 时就要存盘。
- 一个页面最多 1000 个节点，多的截掉；页面栈最多 32 层，再压入就替换栈顶。标题里有「系统」一词的页面（四种界面语言里的都算，比如西班牙语的「…del sistema」），系统会在前面加上应用名，应用冒充不了系统对话框。
- 有 `net` 权限的应用，用户可以在「设置 › 应用权限 › 网络」里关掉它的网络：系统结束应用，下次在没有网络的环境里启动。
- 应用自己退出并且退出码不是 0（崩溃、panic、被信号结束）时，系统记下时间、原因和日志最后 20 行，「设置 › 关于本机 › 应用崩溃记录」里能看到。10 分钟内崩溃 3 次，系统暂时不在后台启动它（推送和后台任务留到用户打开它时再交付）。
- 离开前台约 10 秒后，整个应用连同子进程一起被冻结；在后台时 CPU 份额也比前台低。

### 8.1 中间件的健壮性

Shell 对每个应用都有一份限额：出了错或者有意捣乱的应用只拖慢、拖垮它自己，系统界面和别的应用照常响应，Shell 的内存有上限。超过限额的应用被放慢、被拒绝，或者被结束并留下写明原因的崩溃记录（「设置 › 关于本机 › 应用崩溃记录」）。限额都是 `shell/guard.go` 等处的具名常量。

| 限额 | 值 | 超过时 |
|------|----|--------|
| 一条消息（一行） | 4 MB | 立即结束应用，崩溃记录「protocol violation: a message over 4 MB」 |
| 格式错误的行 | 每次运行 16 行 | 丢掉、记日志；到第 16 行结束应用，崩溃记录写明 |
| 消息速率 | 平均每秒 250 条，突发 500 条 | Shell 放慢读这个应用的套接字，应用自己的写会阻塞；记一行日志，`phone-shell --dump` 里计数 |
| 等事件循环处理的消息 | 8 份，每份 64 KB（每条至少算一份） | 停止读这个应用，等循环跟上：一个应用占不满系统的事件队列，大消息也堆不满内存 |
| 占用事件循环的时间 | 约五分之一（每用掉 1 毫秒，歇 4 倍的时间） | 同上 |
| 发给应用、它没读走的消息 | 256 条（另有套接字缓冲） | Shell 从不阻塞在写上。没冻结的应用：结束，崩溃记录「not reading its connection」；冻结的应用：像低内存清理那样结束（留着墓碑） |
| 没回复的能力请求 | 每个应用 64 个 | 再来的请求立即得到 `err: "too many requests in flight"`（`ui.ErrBusy`） |
| 动作交出去、应用还没处理完时按的键 | 存着，至多 32 个、500 ms（等应用回 ping，[11](11-platform-architecture.md) §16.1） | 到时照常交出去，应用只是晚收到 |
| 要用户做决定的系统框出现以前按的键，和它头 500 ms 里的确认键 | 不算回答（[11](11-platform-architecture.md) §16.1） | 扔掉、记日志；方向键和返回当时就起作用 |
| 会弹系统对话框的请求（`auth.confirm`、`scan.qr`、`camera.capture`、`media.pick`、`file.pick`、`file.save`、`media.delete`、`share.open`、`nfc.read`、`wallpaper.set`、`tel.dial`） | 每个应用同时 1 个 | 同上 |
| 授权对话框 | 同一个应用（小程序）、同一组权限只弹一个 | 后来的请求等同一个回答 |
| 一个节点的文字 | 64 KB（doc 节点的 Markdown 也是：页面到达时解析，之后每帧重新排版，各约 0.5 毫秒 1 KB）；标题、标签、ID 等短字段 4 KB | 截断，末尾加「…」；每次运行记一次日志 |
| 动作 | 每个节点、每个页面 64 个 | 多的不要 |
| 画布 | 每页 8 个；每个画布的图形先裁到画布里，合计访问 8×320×320 个像素，多边形至多 1024 个点 | 多的画布不画；超出的图形不画 |
| 提示条、通知 | 提示条 300 字；通知标题 200 字、正文 2000 字、8 个动作 | 截断 |
| 一个应用引起的日志 | 每 10 秒 20 行 | 只计数（「N more lines not logged」） |
| 连接已关、进程还在 | 5 秒 | 杀掉进程 |
| 进程已退出、子进程还拿着套接字 | 不等 | Shell 关掉自己这一端，并在回收进程之前杀掉它的进程组（开发机；设备上靠 pid 命名空间和 cgroup） |

系统服务：Shell 对服务的每次调用都有期限，默认 15 秒；等外界的方法另有期限：拨号和 USSD 60 秒，发短信 120 秒，NFC 读卡等待 75 秒；等用户或硬件的方法（解锁时等手指、录指纹时等下一次触碰、扫码等结果）没有期限，由各自的取消方法或服务连接断开结束。超时的调用失败（`service did not answer in time`），应用收到的 `res` 带这个错误，等待计数随之减掉，「没有响应」的检测照常；连接断了（服务崩溃、答了不是 JSON 或超长的行）就丢掉，下一次调用重新连接（服务由 systemd 重启，开发机上由 phone-shell 或测试重启）。输入法（phone-imed）每次调用 2 秒；失败后内置引擎接手 2 秒，连续失败时加倍，最多 1 分钟，卡住的 imed 不会每按一个键都让系统停 2 秒。

怎么验证（都在 `make test` 里，开发机上运行）：

- `TestHostileApps` 让假应用经真实的套接字对真实的 Shell 狂发消息、发接近 4 MB 的消息、发超长的行、格式错误的行和深嵌套、不认识的消息、错的协议版本，从不读，传大量文件描述符，紧循环请求能力，发荒唐的页面，派生子进程，关掉连接不退出；同时一个正常的应用每 20 毫秒请求一次能力。界限：事件循环 p99 延迟 ≤ 250 ms，正常应用的往返 p99 ≤ 500 ms，Shell 的堆增长 ≤ 96 MB，捣乱的应用被放慢或者带着原因被结束，Shell 不崩溃。
- `TestServiceFaults` 让假服务挂住、乱答、答错编号、答超长的行、崩溃、消失再回来：每次调用在期限内失败，事件循环不受影响，没有泄漏的 goroutine，服务回来后照常使用。

浸泡测试：`make soak`（默认 `SOAK=30m`）在完整的服务栈上一轮接一轮跑 `shell/soak.script`（应用启动和退出、经推送的端到端加密聊天、视频通话打出和打进、横幅和通知中心、墓碑和复活、蜂窝电话、短信收发、后台音乐、TypeScript 计算器），每轮之后记下 Shell 的描述符、goroutine、堆、RSS 和每个系统服务的描述符、RSS，写进 `SOAK_OUT`（CSV，默认 `build/soak.csv`）。最后三分之一的最低值比开头三分之一的最高值高出界限（Shell：描述符 16、goroutine 64、堆 16 MB、RSS 48 MB；每个服务：描述符 8、RSS 16 MB）就算泄漏，测试失败；锯齿（垃圾回收、缓存第一次填满）不算。

## 9. SDK 与语言

| 语言 | 状态 | 说明 |
|------|------|------|
| Go | ✓ `sdk` | `sdk.New`、`Run`；页面：`Reset` / `Push` / `Update` / `Pop` / `Toast` / `Notify`，`Top` 是栈顶页面的 ID（Shell 说弹出了才算离开）；能力：`Call`（阻塞）、`Go`（异步，结果在应用的派发协程里解码并回调；请求按调用的顺序发出，回答可能先后不同）、`GoAudio`、`CallFile`；回调：`OnStart` / `OnAction` / `OnEvent` / `OnLifecycle` / `OnPopped` / `OnNotif` / `OnSave`（离开前台时返回重建页面要的状态），`Restore` 在 `OnStart` 里读回系统结束前存的状态。能力的结果会**整体替换**传进去的变量，不和旧值合并。能力名在 `ui/caps.go`，动作 ID、生命周期、媒体种类、播放状态和操作、事件名、设置键、`ui.Cancelled(err)`（用户取消）在 `ui/names.go`：用常量，不要自己写字符串 |
| Python 或任何语言 | ✓ 直接用协议 | `apps/hello-py/hello.py` |
| 小程序（JS / TypeScript） | ✓ 宿主 `apps/miniapp`；运行时授权按「宿主 + 小程序」分开记账，每个小程序第一次使用都要用户同意，宿主的授权和预授权不会传给小程序，设置里能单独撤销。除了内置的，宿主也装签名的小程序包（同 §12 的格式，由宿主厂商的小程序商店签发），装之前和每次启动前都验签；每个小程序的存储在自己的目录里，最多 1 MB；每个小程序跑在自己的 kario-jsrt 进程里（和 TypeScript 应用同一个引擎，[29](29-app-development.md) §5.4），一轮 JS 超过 300 ms 被中断，内存最多 64 MB，页面最多 10 层；关掉后运行时留 5 分钟（至多 3 个），再打开回到原来的页（[29](29-app-development.md) §5.4），页面有 `onShow` / `onHide` / `onUnload` | `Page({data, render, onAction})`、`setData`；`phone.toast / navigateTo / navigateBack / getStorage / setStorage / scanCode / requestPayment / t`（界面文字，§9.1）；`ui.text / section / doc / item / input / image / button / progress`；类型声明在 `types/phone.d.ts` |
| TypeScript | ✓ 开发机（单元测试、自测）；QEMU 自测 ✓ | 主力语言（[29](29-app-development.md)），§9.2：`kario new` / `build` / `package`；`import { reset, on, call, tr, storage } from "kario"`；类型 `@kario/sdk`（`sdk/ts`）；示例 `examples/hello-ts`；性能要紧的部分做成原生模块（§9.3，示例 `examples/imgproc`） |
| C / C++ | ✓ `sdk/c`（`phoneui.h`，C11，没有依赖） | `phoneui_open` / `phoneui_next`（自动回答 `ping`）/ `phoneui_send` / `phoneui_call`（同步调用能力，期间到达的消息排队）；回复带的文件（§7）用 `phoneui_fds`、`phoneui_take_fd` 取；自己有事件循环的程序用 `phoneui_try_next`（不阻塞，没有完整消息时返回 0，照样回答 `ping`）和 `phoneui_pending`（库里已经收下、`poll` 看不到的消息）；JSON 写入器 `phoneui_json_*`；`phoneui_get_string` 取输入框的值。示例 `sdk/c/examples/hello-cpp`（C++20）；镜像里的 `phone-capi` 包带 `libphoneui.so`，头文件和静态库在 `-dev` 包。TypeScript 应用的原生模块只包含 `sdk/c/include/kario_native.h`，C++ 的也可以用它的封装 `kario_native.hpp`（§9.3） |

开发机上跑自带程序的应用（Go、C / C++，或任何讲协议的程序）：应用目录里放清单（写 `exec`，不写 `runtime`）和编好的程序，`kario run [-preview ADDR] [-device ID | -sku ID] 目录` 不编译，把目录交给开发机的 Shell，Shell 在目录里启动这个程序（系统里有同名的程序也不顶替它）；`-watch`、`-inspect`、`-engine` 只对 TypeScript 应用。一步一步见 [35](35-quickstart.md) §3。

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

小程序宿主（每个小程序自己的目录）、Python 和 C/C++ 示例、渲染器（`paint/`、`gui/`）和网页视图（`webview/`）读目录时也按这条回退链（小程序宿主和渲染器单元测试 ✓；Python、C++ 示例和网页视图用缺几条消息的目录在开发机上核对过；都未上真机）。小程序的 `app.json` 和清单一样用 `lang` 说名字和简介（`name`、`description`）是哪种语言写的（不写是 `zh-Hans`），`locales` 按语言给它们的译文（`{"en": {"name": "Notes", "description": "…"}}`，只能有名字和简介，用途照旧由 `locale/` 翻译）；宿主按这条回退链（`i18n.PickText`）在小程序列表、「关于这个小程序」、没写标题的页面和支付确认的说明里显示设备语言的那个，装的时候和每次加载时都按清单的规则核对（只能是四种语言，名字 1–32 个字，简介最多 200 个字）。小程序的身份（「宿主 ID/小程序名」：授权、密钥、系统对话框里的来源，§4）一直是 `name` 原来的写法，不随语言变；所以系统的授权框、设置里的授权列表显示的还是原名。内置的三个小程序和示例 `examples/mp-counter` 都带了四种语言的名字和简介（单元测试 ✓，**未上真机**）。

### 9.2 TypeScript 应用

TypeScript 是应用的主力语言（[29](29-app-development.md)）。应用只写 TypeScript：`kario` 命令行（Go 写，内嵌 esbuild，不用装 Node）把它编成一个 ES2023 模块，系统的 `kario-jsrt`（QuickJS-ng 加 `kario` 模块，经 C 接口 `sdk/c` 讲 §2 的协议）在应用的沙箱里运行它。代码只能 `import` 一个模块 `"kario"`（和应用自己的原生模块 `"native:<名字>"`，§9.3）：没有 QuickJS 的 `std`、`os` 模块，不能自己打开文件、套接字和进程（文件只有能力的回复交给它的那几个，§7；网络只经 `fetch`、`WebSocket`，§9.4）；能力照旧经 broker（权限、用途、系统界面），应用的数据只经 `storage` 存取。开发机上单元测试和自测通过；QEMU 上自测 ✓（[29](29-app-development.md) §5），**未上真机**。

```bash
kario new notes            # manifest.json、src/main.ts、src/locale/zh-Hans.json、tsconfig.json、types/kario/（类型）
kario build notes          # → notes/build/：manifest.json、main.js、locale/、图标；先按安装器的规则检查清单
kario package -key dev.key -cert dev.cert -o notes.phoneapp notes   # 先编再签名打包，密钥和证书同 phone-pkg（§12）
kario package -devstore DIR -o notes.phoneapp notes                 # 用开发商店签名，开发者模式的手机认它
kario device install -open notes                                    # 开发者模式、信任过的数据线上：签名、装到手机上、打开
kario device logs -f com.example.notes                              # 应用写的日志（标准输出和错误输出），跟着往下看
kario device reload com.example.notes                               # 重新载入（经墓碑回到原来的页）；restart 从头开，uninstall 卸载
kario device push seed.json com.example.notes:files/seed.json       # 文件放进开发者模式装的应用的数据里；pull 取回来
kario device screenshot -o shot.png                                 # 屏幕截图（安全画面遮住）；key、type 按键打字
kario device syslog -f                                              # 系统日志（系统服务的和 Shell 的），像 logcat
kario device forward 9229 9229                                      # 电脑的端口转到手机的回环地址（ssh -L）；reverse 反过来
kario symbolize -symbols notes.phonesym crash.json                  # 崩溃里 main.js 的栈映射回 TypeScript（符号文件，§12）
```

`kario device` 经 SSH 到手机在数据线上的地址（`root@10.71.0.1`，开发镜像的 dropbear）运行 `phone-dev`，由 Shell 决定：开发者模式关着、这台电脑没被信任、不是从数据线来的，一律拒绝；只碰装上去的应用；装的包走安装器同一套检查（[29](29-app-development.md) §3、[27](27-usb-typec.md) §4）。开发机 ✓，QEMU ✓（经真的 SSH），**未上真机**。推拉文件、截屏、按键打字、系统日志、端口转发、重启、旁加载系统更新、问题报告这些对着系统的工具，和安卓 adb、fastboot 的对照在 [27](27-usb-typec.md) §4.1（开发机 ✓；经真的 SSH 的模拟器用例还没跑过；**未上真机**）。

在真机上调试：Kario Studio 的「Kario: Debug on the Phone」（调试类型 `kario`，launch.json 里写 `"type": "kario"`、`"project"`；适配器是 `kario dap`）把工程不压缩地编好、用开发商店签名装上，经同一条数据线让应用在 kario-jsrt 的调试器下重启（`phone-dev debug`）；「Debug the App Already on the Phone」（launch.json 里 `"request": "attach"`）不装、不重启，接到正在跑的应用上（`phone-dev debug -attach`，应用的状态都在；没在跑就在调试器下启动它）。断点、条件断点、命中次数、日志点、函数断点、单步跳过、步入、步出、暂停、调用栈、局部和闭包变量、在帧里求值和改变量、`debugger` 语句、未捕获（或全部）异常时暂停，像 Node 下那样，位置按源码映射在 `.ts` 里。只给开发者模式下装上的（开发商店签名的）应用，商店装的和系统应用不行；差什么见 [29](29-app-development.md) §5.5。开发机 ✓（单元测试），**未上真机**。

包里只有 JavaScript（带 `main.jsc` 的包装不上）：TypeScript 应用装好后，系统在设备上把它预编译成字节码，放在应用写不了的系统目录里，按包签名里模块的摘要记着，每次启动前核对，对不上就跑源码并重新编，更新、卸载时删掉（[29](29-app-development.md) §5.1）。开发者什么都不用做。

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
| `call(cap, args)` | 能力调用（§4），返回 Promise。失败时 reject 一个 `Error`：`message` 是系统给的原文，`cap` 是能力名；`cancelled(err)` 判断是不是用户取消。随回复交出的文件（§7）在结果上，不可枚举（`JSON.stringify`、`storage`、交给墓碑的状态都不带它们）：`media.pick`、`file.pick`、`file.open` 的在 `files` 里，每个是一个 `KarioFile`；`audio.stream`、`audio.record` 的环形缓冲区已经映射好，是 `ring`（`AudioRing`）。类型 `CallResult`、`CapFiles`；「文件」里的文件也可以用 `file.pick`、`file.open` 的 `read: true` 直接拿到内容（至多 1 MB，§4.9） |
| `KarioFile` | `size`（现在的字节数）、`read(max)`（从上次读到的地方往后，至多 `max` 字节，缺省 64 KB，1 字节到 1 MB；读完是空的 `Uint8Array`）、`readAll()`（从头读整个文件，不管 `read` 读到哪；超过 16 MB 抛 `RangeError`，大文件用 `read` 分段读）、`close()`、`closed`。只读。文件归应用，用完就 `close()`；应用丢下不管的，被回收时由运行时关掉（kario-jsrt 引用计数，马上；Node 下等垃圾回收）；应用手里同时开着 64 个时，再来的回复的文件被关掉、调用失败（`too many files open from replies (at most 64)…`），描述符表（1024）用不完。运行时只收普通文件（照片、文件、memfd），别的关掉并记日志 |
| `AudioRing` | `rate`（48000）、`capacity`、`buffered`（等着读的样本数）、`write(samples)`（`audio.stream`：`Int16Array`，能放下多少写多少，返回写了几个）、`read(samples)`（`audio.record`：读出等着的样本，返回几个）、`finish()`（播放写完了，放完就结束）、`finished`、`close()`（放开映射；录音要 `call("audio.control", { id, op: "stop" })` 才停）。映射前运行时像 `audio.MapRing` 那样查：封住了缩小、大小在 10 秒以内、头对得上，不对就让调用失败（`the audio ring is not sealed against shrinking` 等）。位置按容量取模，对面写坏了也只坏自己的声音。环里至多 10 秒：定时（20 ms 左右）写或读，写慢了的空档放成静音 |
| `fetch`、`WebSocket`、`Blob`、`File`、`FormData`、`Headers`、`Request`、`Response` | 网络（§9.4） |
| `on(name, handler)` | `start(arg, restored)`、`action(e)`、`key(key, e)`（聚焦的画布收到的按键）、`input(e)`（`live` 输入框的每次编辑）、`popped`、`foreground`、`background`、`save()`（离开前台时返回重建页面要的状态，至多 64 KB；系统结束了进程，新进程在 `start` 的 `restored` 里拿回）、`event(name, value)`（§5）、`notif(n)`、`resync`。每种一个处理函数，新的替换旧的；没有 `key`、`input` 处理函数时，按键和编辑照常作为动作交给 `action` |
| `tr(msg, ...args)` | 界面文字：按设备语言的回退链（§9.1）查 `locale/`，`%s`、`%v`、`%d` 按顺序替换 |
| `storage.get` / `set` / `remove` / `keys` / `clear` / `flush` | 键值存储，值是 JSON，合计至多 1 MB。运行时把它存在应用数据目录里，JavaScript 看不到路径；进程被结束后还在。照安卓 SharedPreferences 的 `apply()`：`set`、`remove`、`clear` 当场改内存（`get` 马上读到新值），文件由运行时自己的一个线程在后台写（先写新文件、fsync，再改名），存一次不等磁盘，闪存再慢也卡不住应用的主循环（不会因此 ANR）；正在写时又存的合并成下一次写，写的总是最新的内容，文件永远是完整的旧版本或新版本，不会倒退。超过 1 MB 的 `set` 当场抛 `RangeError`。`flush()` 返回 Promise：在它之前存的都落盘了才 resolve；写失败（磁盘满、目录只读）时 reject 一个 `message` 以 `storage: ` 开头的 `Error`，日志里也记一行 `storage not saved: …`，值还在内存里，下次再存时整份重写。离开前台时 `save()` 交给墓碑的状态，要等已存的写完才发给系统（系统收到状态就可能结束进程）；Shell 关掉连接、SIGTERM 时运行时也先写完再退出（SIGKILL 结束的进程丢掉还没写完的那次，文件还是上一个完整版本）。kario-jsrt 和 Node（`kario run`、`kario test`）一样，`jsrt/storage_test.go` 在两个引擎上用同一批用例核对（慢闪存用预加载的 `testdata/slowfs` 模拟：每次 fsync 慢 3 秒时动作和 ping 照样马上答复、合并、写完才发状态、退出和 SIGTERM 等写完、只读目录的错误）；开发机 ✓，**未上真机** |
| `appId`、`lang` | 应用 ID、设备语言 |
| 全局 | `setTimeout` / `setInterval` / `clearTimeout` / `clearInterval`（跑在运行时的事件循环上，和协议的套接字一起 poll）、`console.log` / `info` / `warn` / `error` / `debug`（写到 stderr，就是系统给应用留的日志）、`queueMicrotask`、`atob` / `btoa`、`performance.now`；网页的 `AbortController`、`AbortSignal`（`abort()`、`timeout(ms)`、`throwIfAborted()`、`onabort`、`addEventListener`）、`TextEncoder`、`TextDecoder`（手机上只有 UTF-8，`stream`、`fatal`、`ignoreBOM`；Node 认得更多编码）；ECMAScript 本身的全部（`Promise`、`JSON`、`Date`、`RegExp`、`Map`……）。拒绝了的 Promise 到这一轮的任务跑完还没有处理函数，才记一行「unhandled promise rejection」 |

- 类型 `@kario/sdk` 里的 `Screen`、`Node`、`Action`、节点种类和样式、能力名和各自的参数与结果、事件名都由 `tools/tsgen` 从 `ui/` 生成（`sdk/ts/ui.d.ts`，`make tsgen`），接口只有一个来源；Go 那边改了而没有重新生成，`make test` 就失败。
- 字节码：`kario build -bytecode` 另写一份 `main.jsc`（`kario-jsrt --compile` 预编译，带引擎的版本），运行时先用它，版本不对时用 `main.js`。只给系统镜像用（`scripts/stage.sh`）：QuickJS 的字节码格式防不住恶意的写入方，所以安装器和商店都拒收带字节码的包；装好以后由系统在设备上预编译（像安卓的 dex2oat，见上文和 `shell/jscache.go`）✓。
- 随回复的文件在开发机上：Node 下（`kario run`）它们经 `kario-native.node` 的读线程到达（Node 自己的套接字用 `read` 读，文件被内核丢掉），`kario` 在开发机有 C 编译器时就编它；没有时带文件的回复让调用失败并写明原因。Node 下 `AudioRing` 经文件读写（pread、pwrite）而不是映射，Node 读不到 memfd 的封印，只拒收不是 memfd 的环形缓冲区。`kario test` 里假能力用 `withFiles(数据, ...文件)` 交文件（字符串或字节），`app.openFiles` 是应用还开着几个。文件的情形 `jsrt` 的 `TestReplyFiles` 和 `TestNodeReplyFiles` 用同一批核对（照片、几个文件、超过 16 MB、不是普通文件、64 个的上限、丢下的结果、失败的调用、播放和录音的环形缓冲区、没封印的环），C SDK 的在 `sdk/c/test_phoneui.c`。开发机 ✓，**未上真机**。
- 示例：`examples/hello-ts`（列表、输入框、软键、剪贴板、选一张照片读出大小和文件头、存储、墓碑、定时器）；开发机上 `stage.sh` 把它编好放进系统应用目录，自测有它的用例（「TypeScript 应用」里经系统选择器选照片）。
- 声明式框架 KarioUI（`@kario/ui`，`kario build` 打进 `main.js`）、在 Node 下跑和调试（`kario run`）、测试（`kario test`）、上架（`kario publish`）：教程 [34](34-app-tutorial.md)；系统的计算器（`apps/calc`）和日历（`apps/calendar`，[19](19-core-apps.md) §1）就是用它写的；日期、时间输入框是 `DateInput`、`TimeInput`。

### 9.3 原生模块

TypeScript 应用里慢的部分（编解码、图片处理、加密、数据库、自己的网络协议）可以用 C、C++ 或 Rust 写成共享库，随包签名，在同一个进程里给 JavaScript 调用，像鸿蒙的 NAPI、React Native 的 TurboModules。开发机上单元测试和自测通过；C 接口和示例在 QEMU 上跑过，C++ 和 Rust 的封装、封住可执行内存、原生调用栈、按架构瘦身只在开发机上测过；真机上**未跑**（[29](29-app-development.md) §5.3）。

```ts
import { blur } from "native:imgproc";              // 包里的 lib/<架构>/imgproc.so
blur(pixels, 320, 240, 4);                           // 同步：就地改 pixels 的字节
const out = await blurAsync(pixels, 320, 240, 4);    // KARIO_ASYNC：在工作线程上，得到新的 ArrayBuffer
const mod = await import("native:imgproc").catch(() => undefined);   // 加载不了时降级到 TypeScript
```

```cpp
#include "kario_native.hpp"                          // C++17，只有头文件
static double add(kario::call &c) { return c.arg<double>(0) + c.arg<double>(1); }   // 参数不对就抛，JS 拿到 Error
KARIO_NATIVE_CPP_MODULE([](kario::module &m) { m.function("add", add); })
```

```rust
// crate sdk/rust/kario-native，模块的 Cargo.toml 写 crate-type = ["cdylib"]
fn add(c: &mut kario_native::Call) -> Result<f64, kario_native::Error> { Ok(c.arg::<f64>(0)? + c.arg::<f64>(1)?) }
kario_native::module!(|m: &mut kario_native::Module| m.function("add", add));
```

| 方面 | 规则 |
|------|------|
| 清单和包 | 清单写 `"native": ["imgproc"]`；库是包里的 `lib/<架构>/<名字>.so`，架构是 `arm64`（手机，商店只收带它的包）和 `x86_64`（开发机），权限 0644（是库，不是程序）；`lib/` 下不能有别的，包里别处不能有 ELF 文件。安装器、商店审核、`kario build` 用同一个 `jsrt.Check`：64 位、小端、机器对得上的共享库，导出 `kario_native_abi` 和 `kario_native_init`，接口版本对得上，不要可执行栈；安装器要本机的架构，商店要 `arm64`。一个包可以带几种架构（`kario package` 缺省放进编好的每一种，`-arch arm64,x86_64` 指定、每种都得有）：商店按设备只发它那种（`GET /v1/packages/…?arch=arm64`，目录的 `thinned` 是那一份的大小和摘要），设备用开发者原来的签名核对，只许别的架构的 `lib/<架构>/` 缺席，别的文件缺了或变了照样拒收 |
| 加载 | `kario-jsrt` 只从应用自己的目录加载：名字合法、清单里声明了、路径解析后还在包里（符号链接不行）、ELF 是本机的架构、接口主版本相同且次版本不比运行时新；不满足时 import 失败，错误信息说原因（如 `native:imgproc: lib/x86_64/imgproc.so is a library for arm64, not x86_64`）。静态 import 失败时应用起不来；要降级就用 `import()` 接住错误。kario-jsrt 在应用的代码跑之前按清单的顺序加载声明的全部模块（`init` 在那时跑），加载不了的到 `import` 时抛那句错误；然后封住可执行内存（「不能做的」）。模块的类型由开发者写在 `.d.ts` 里（`declare module "native:imgproc"`） |
| C 接口 | `sdk/c/include/kario_native.h`（镜像里在 `phone-capi` 的 `-dev` 包）：库用 `KARIO_NATIVE_MODULE(init)` 导出入口，`init` 用 `export_fn`、`export_number`、`export_string` 登记名字；函数从一张函数表（`kario_api`）取参数、设结果，不链接运行时的任何符号，也碰不到 JavaScript 的对象，所以同一个 `.so` 在 kario-jsrt 和 Node 里都能加载，换引擎版本不用重编。值只有数字、布尔、字符串、字节（`ArrayBuffer` 或 typed array）和 JSON；原生代码不回调 JavaScript |
| C++ 和 Rust | 都在同一个 C 接口上，模块还是一样的 `.so`。C++：`sdk/c/include/kario_native.hpp`（C++17，只有头文件；`c++ -std=c++17 -shared -fPIC -I sdk/c/include`），`KARIO_NATIVE_CPP_MODULE` 里用 `m.function(name, f[, kario::async])`、`m.value` 导出，`f` 是拿 `kario::call&` 的函数或 lambda，返回值就是结果；`c.arg<T>(i)` 按类型取参数，不对就抛 `kario::error`；函数抛的任何异常在边界上变成 JavaScript 的 `Error`（异步的就是 reject），`m.events()` 给自己的线程 `emit`。Rust：crate `sdk/rust/kario-native`（没有依赖，带 `Cargo.lock`），模块的 `Cargo.toml` 写 `crate-type = ["cdylib"]`、依赖它；`kario_native::module!`、`Module::function` / `function_async`、`Call::arg::<T>()`，函数返回 `Result<R, Error>`，`Err` 和 panic 都成为 `Error`，`Module::emitter()` 给自己的线程。编出来的 `lib<名字>.so` 放成 `native/<架构>/<名字>.so`。为手机交叉编译这两种还没试过 |
| 内存 | 访问器给的字符串、字节、JSON 归运行时，到函数返回为止有效，不能 `free`、不能留着；`ret_string`、`ret_json`、`fail`、`emit` 收下的会复制；`ret_bytes(len)` 给一块清零的内存，返回后成为结果的 `ArrayBuffer`。同步函数拿到的字节就是 JavaScript 的内存（可以就地改），异步函数拿到的是副本 |
| 线程 | 引擎只有一个线程。普通函数在它上面同步执行，执行期间应用不响应按键和系统（前台 5 秒不响应，系统提示关闭），要短；`KARIO_ASYNC` 的函数在运行时的工作线程上执行（每个应用至多 4 个，多的排队），JavaScript 拿到 Promise，在引擎线程上兑现；模块可以开自己的线程（算进应用的 128 个进程和线程），它们只能用 `emit(event, json)` 交事件给 JavaScript（每个模块自带的 `on(event, handler)`，至多排队 1024 个）。函数表的其他函数只能在调用里、在调用所在的线程上用 |
| 不能做的 | 原生代码在应用的进程里，受同一个沙箱限制（uid、命名空间、seccomp、Landlock、按应用的防火墙），没有多出来的权利：系统能力仍然只经 JavaScript 的 `kario.*` 和 broker 要。模块加载完以后，系统封住可执行内存：带 `PROT_EXEC` 的 `mmap` 和 `mprotect`、`dlopen`、`exec` 都返回 `EPERM`，所以不能加载、不能生成包外的代码（下载的库、数据目录里的文件、JIT），不能开别的程序；`init` 留下以后还能变的可执行内存（可写又可执行、共享、匿名）的模块，应用起不来。名字解析只剩 libc 自带的（files 和 DNS）。规范还要求（审核看，系统拦不全）：不碰运行时的描述符（fd 3 上的协议、工作线程的 eventfd）和内存，不改信号处理，不开子进程，不 `exit`。崩溃时进程结束，系统照常记录：日志和崩溃记录（`backtrace`）里有模块、函数、信号和每一帧（`kario-jsrt: #0 native:imgproc +0x1139 build-id …`），发给开发者时一起发 |
| 开发机 | `kario build` 不编 C：开发者用自己的工具编（`cc -shared -fPIC -I sdk/c/include`；手机的用 `aarch64-linux-gnu-gcc` 或 Yocto 导出的 SDK），放在项目的 `native/<架构>/<名字>.so`（`-native DIR` 指向别处）；`kario build` 拷进 `lib/<架构>/`，缺 `arm64` 时提醒，`kario package` 缺 `arm64` 就拒绝。`kario run`（Node）和 `kario test` 经 `kario-native.node`（`sdk/ts/node/native.c`，Node-API，由 `kario` 用本机的 C 编译器现编）加载同一个 `.so`，VS Code 的调试器照样断在 TypeScript 里；`kario run -engine jsrt` 在 kario-jsrt 里跑，C 代码用 gdb 附加。留着没剥符号的库（`-g`，链接时带 build-id，多数工具链缺省就带）：`kario symbolize -so native/ crash.json` 把崩溃记录（或日志、商店的崩溃列表）里的帧变成函数和 `文件:行`，按 build-id 找库，不要外部工具。Node 下不封可执行内存，崩溃也没有调用栈 |
| 审核 | 带 `native` 的应用每个版本都进人工审核，照原生应用审（[33](33-app-guidelines.md) §2.2）；商店的「详情」页和安装确认框写明含原生代码；更新新带了原生模块时不在后台自动装，要用户确认 |

示例：`examples/imgproc`（C 写的方框模糊加同样的 TypeScript 版本，`kario test` 4 个用例，自测里从商店安装、比较两边的速度）。

### 9.4 网络（`fetch`、`WebSocket`）

TypeScript 应用自己联网用 `kario` 模块的 `fetch` 和 `WebSocket`（`import { fetch, WebSocket } from "kario"`，KarioUI 也导出它们），连同 `Blob`、`File`、`FormData`、`Headers`、`Request`、`Response`（本节后面的表），是网页接口的子集。设备上在 `kario-jsrt` 里经 libcurl（`jsrt/src/net.c`、`ws.c`：每个请求、每条连接在自己的工作线程里，有了进展经 eventfd 叫醒事件循环），开发机的 Node 下用 Node 自带的 `fetch` 和套接字（`sdk/ts/node/net.mjs`），两边规则、上限和出错的文字一样，`jsrt` 的 `TestFetch` 和 `TestNodeFetch`、`TestNet` 和 `TestNodeNet` 用同一批情形核对。请求从应用自己的进程发出：用应用的 uid、网络命名空间、按应用的防火墙和流量统计，和 Go 写的应用自己连网一样，不经 Shell 转。普通的请求随自测「教程的示例」在 QEMU 上跑过；流式读 body、取消、Cookie、`WebSocket` 只在开发机上测过（单元测试 ✓），QEMU 上**还没跑**；都**未上真机**。

| 项目 | 规则 |
|------|------|
| 权限 | 清单要有 `net`（安装时给，用户能在「设置 › 应用权限 › 网络」里关掉）；没有时 `fetch` 一律拒绝（`fetch: the app's manifest does not ask for the "net" permission`），`new WebSocket` 抛同样的话；用户关掉网络时连不出去 |
| 地址 | 只许 `https://`；`http://` 只许本机回环（`localhost`、`127.x`、`[::1]`），开发调试用；不许带用户名密码、空格和控制字符，至多 8 KB |
| 证书 | 系统的 CA（镜像里的 `ca-certificates`），不能关掉校验 |
| 方法 | `GET`（缺省）、`HEAD`、`POST`、`PUT`、`PATCH`、`DELETE`、`OPTIONS`；`GET`、`HEAD` 不能带 body |
| 头 | 一个字符串对象、`Headers` 或 `[名, 值]` 对；至多 64 个，名字是 token，值不能换行；`Host`、`Content-Length`、`Transfer-Encoding`、`Connection` 这类网络自己设的不许写 |
| body | 字符串按 UTF-8 发，没写 `Content-Type` 时是 `text/plain;charset=UTF-8`；`ArrayBuffer` 或类型化数组原样发；`Blob` 原样发，没写 `Content-Type` 时用它的 `type`；`FormData` 按 `multipart/form-data` 发（`Content-Type` 带分界）；至多 4 MB。也可以整个交一个 `Request`：`fetch(request, 选项)`，选项盖过它的 |
| 重定向 | `GET`、`HEAD`、`POST` 跟着走，至多 5 次，只往 `https://` 走；`POST` 遇到 301、302、303 改成 `GET`、不带 body（和浏览器一样）；别的方法拿到 3xx 本身 |
| 回来的 | 一个 `Response`（下面的表）：`ok`、`status`、`url`、`redirected`、`headers`、`body`、`text()`、`json()`、`arrayBuffer()`、`bytes()`、`blob()`，响应头一到就兑现；`headers` 是只读的 `Headers`，名字小写，同名的用「, 」连起来，每个头照旧也是它小写名字的属性（`res.headers["content-type"]`；和 `Headers` 的方法同名的头只能 `get()`）；`statusText` 是 ""，`type` 是 `basic`，不能 `clone()`。body 二选一：整个读（`text()`、`json()`、`arrayBuffer()`，至多 8 MB，可以读多次），或者当流读（`res.body.getReader().read()`、`for await (const chunk of res.body)`，一块至多 64 KB，总长不限）；选了一种，另一种 reject。运行时最多比应用多读 256 KB，应用读得慢时不再从网络收（TCP 背压），所以多长的下载也只占这么多内存；`body.cancel()` 停掉传输。没人拿着的响应被回收时，没读完的传输也停掉 |
| 时间、并发 | `timeout`（毫秒，1 到 120000，缺省 30000）管到响应头到达，整个读 body 时一直管到读完（都从发出时算）；当流读时改管安静：应用在等下一块、这么久没来一个字节就失败（`fetch: no data for the timeout while the body was read`）。同时至多 8 个，读完、取消、出错或被回收才算完 |
| 取消 | `signal`（`AbortController` 的，或 `AbortSignal.timeout(ms)`）：任何时候都能取消，读 body 的时候也行，传输马上停；`fetch` 和读 body 的 Promise reject `signal.reason`：缺省是 `name` 为 `AbortError` 的 `Error`（`This operation was aborted`），`timeout` 的是 `TimeoutError`，应用给了原因就是它 |
| Cookie | 缺省不收不发：`credentials` 缺省是 `same-origin`（浏览器的缺省），应用不是网页来源，于是和 `omit` 一样，也和 Node 的 `fetch` 一样。`credentials: "include"` 用应用自己的 Cookie 罐：发出匹配的（域、路径、`Secure`、期限），收下 `Set-Cookie`，重定向的每一跳都照办；罐在数据目录里（`kario-cookies.txt`，libcurl 的 Netscape 格式，JavaScript 看不到路径），有期限的跨进程保留，会话 Cookie 只活到进程结束。没有清空罐的接口：要删的由服务器发过期的 `Set-Cookie` |
| 出错 | 请求不合规则、连不上、超时、证书不对、太大，都是 reject 一个 `TypeError`，消息以 `fetch: ` 开头；404 之类是正常的回应（`ok` 为 false）；取消见上 |
| 测试 | `kario test` 不连网络：`launch({ fetch: (req) => ({ status, headers, body }) })` 给回应，`body` 也可以是分块的异步迭代器（测流式读），回应的 `Set-Cookie` 进 Cookie 罐；`app.fetches` 是应用发过的请求（`FormData` 的 body 是发出去的 multipart 字节）；没给时 `fetch` 失败。WebSocket 连到测试自己的服务器（下面的 `WebSocket` 表） |

`WebSocket` 是浏览器的接口，加了一个参数：

| 项目 | 规则 |
|------|------|
| 接口 | `new WebSocket(url, protocols?, { headers?, signal? })`；`readyState`、`protocol`、`bufferedAmount`、`send()`、`close(code?, reason?)`、`onopen` / `onmessage` / `onerror` / `onclose` 和 `addEventListener`；消息是字符串或 `ArrayBuffer`（`binaryType` 只有 `arraybuffer`：收到的不是 `Blob`，`send` 也不收 `Blob`）。第三个参数浏览器没有：握手的头（fetch 的规则，不许 `Sec-WebSocket-*`，协议名走第二个参数）和取消 |
| 权限、地址 | 同 fetch：要 `net`；只许 `wss://`，`ws://` 只许本机回环；不许片段（`#`）；系统的 CA。地址、协议名（token，不重复，至多 16 个）、头不对时构造函数抛错（`SyntaxError`、`TypeError`），连不上是异步的 `error` 和 `close` |
| 怎么连 | kario-jsrt：libcurl 只负责连上（`CONNECT_ONLY`，fetch 的那套 TLS 和 CA，只说 HTTP/1.1），握手和分帧运行时自己做（`jsrt/src/wsframe.c`），所以不依赖镜像里的 libcurl 带不带 WebSocket；Node 下经 `node:tls` 和同样的分帧。不带 Cookie；服务器的 ping 自动回 pong |
| 上限 | 同时至多 4 个；收到的一条消息（分片合计）至多 4 MB，帧头一说超过就让连接失败（1009），不先分配；发出的一条至多 4 MB，等着发的至多 16 MB（`bufferedAmount`），超过时 `send` 抛 `RangeError`；收到了、还没交给应用的至多 8 MB，超过就不再读套接字（TCP 背压），应用忙的时候服务器怎么狂发也撑不爆内存；握手 30 秒，回应头至多 64 KB；关闭握手等 5 秒 |
| 服务器不守规矩 | 带掩码的帧、保留位、不认识的操作码、分片的或超过 125 字节的控制帧、不是 UTF-8 的文本、不合法的关闭码：先 `error`（`message` 写明），再 `close`（1002、1007 或 1009，`wasClean` 为 false），运行时回服务器同样的关闭码；握手不对（不是 101、`Sec-WebSocket-Accept` 不对、选了没提的协议或扩展）：`error` 再 `close` 1006 |
| 关闭、取消 | `close(code, reason)`：`code` 是 1000 或 3000 到 4999，`reason` 至多 123 字节；服务器先关时回同样的关闭码，`close` 事件带它的码和原因、`wasClean` 为 true；连接断了、服务器 5 秒不回关闭是 1006。`signal` 取消时马上断开，不等关闭握手（`close` 1006，原因 `aborted`）。连着的 WebSocket 由运行时拿着（像浏览器），关了以后应用不拿着就回收 |
| 测试 | `kario test` 里 `wss://`、`ws://` 到哪儿都连到测试自己的服务器上（`sdk/ts/node/wstest.mjs`，一对内存里的套接字，运行时对它和对真的服务器说同样的握手和分帧），不连网络：`launch({ websocket: (req) => ({ protocol }) })` 决定握手（`req` 有 `url`、`protocols`、`headers`；回 `{ status: 403 }` 就拒绝，应用得到 `error` 再 `close` 1006；不给时一律接受、不选协议），`await app.websocket()` 是应用开的下一条连接的服务器那头：`messages`、`await next()`（下一条消息）、`send(文本或字节)`、`ping()`、`close(code, reason)`（应用回关闭，`wasClean` 为 true）、`drop()`（连接断了，1006）、`sendRaw(字节)`（测不守规矩的服务器）、`await closed`（结束时的码和原因）；`app.websockets` 是全部连接。`cmd/kario` 的 `TestKarioTestNetwork` 用一个真的工程跑过：握手的地址、协议、头，两个方向的文本和二进制，ping，应用先关、服务器先关，被拒的握手，断掉的连接（以前这里的连接马上以 1006 结束，这个用例在旧代码上失败） |

服务器发来的帧由 `tools/cfuzz` 的 `FuzzWebSocketFrames` 模糊测试（[17](17-app-sandbox.md) §9）。示例：教程的便签（`examples/karioui-notes`，[34](34-app-tutorial.md) §7.1）把推送令牌 POST 给自己的服务器；自测「教程的示例」里对着本机回环上的测试服务器发、核对服务器收到了。`Blob`、`File`、`FormData`、`Headers`、`Request`、`Response`（`import { Blob, FormData, Request, Response } from "kario"`，KarioUI 也导出），网页接口的子集，两个引擎跑的是同一份 JavaScript（`sdk/ts/node/web.js`）：kario-jsrt 在应用第一次用到它们或 `fetch` 时才编译它（22 KB，开发机上约 1.9 ms），用不到的应用启动时一点不多花；在那以前模块导出的是替身，`new`、`instanceof`、继承、静态方法都转给真的，编好以后真的顶替它们（import 是活绑定）。开发机上两个引擎都测过（`jsrt` 的 `TestFetchWeb`、`TestNodeFetchWeb`：离线的各类、在第一次用之前就取到的替身、`FormData` 由 Go 的 multipart 解析器收、带类型的 `Blob`、`Request` 和盖过它的选项、`fetch` 回来的 `Response` 和照旧能用的属性写法），QEMU 上**还没跑**，**未上真机**。

| 类 | 有的 | 没有的 |
|------|------|------|
| `Headers` | 由 `Headers`、字符串对象或 `[名, 值]` 对构造；`append`、`set`、`get`、`has`、`delete`、`getSetCookie`、按名字排序的 `entries` / `keys` / `values` / `forEach` / 迭代；名字不分大小写，存成小写；名字不是 token、值带换行或 NUL 时抛 `TypeError` | 只有一种 guard：网络来的响应头只读 |
| `Blob`、`File` | 由字符串（UTF-8）、`ArrayBuffer`、类型化数组和 `Blob` 拼成；`size`、`type`（小写，不是可打印的 ASCII 就是 ""）、`slice`、`text()`、`arrayBuffer()`、`bytes()`、`stream()`（每块至多 64 KB）；`File` 多 `name`、`lastModified` | `URL.createObjectURL`；行尾转换（`endings`） |
| `FormData` | `append`、`set`、`get`、`getAll`、`has`、`delete`、迭代；`Blob` 变成 `File`（名字是给的文件名、它自己的，或 `blob`）；当 body 时按 `multipart/form-data` 发（RFC 7578，和浏览器一样：16 个随机字符的分界，字符串值里的换行变成 CRLF，名字里的 `"`、CR、LF 写成 `%22`、`%0D`、`%0A`） | 从表单元素构造；从 body 读回（`formData()`） |
| `Request` | `new Request(地址或 Request, 选项)`：fetch 的选项（方法、头、body、`signal`、`credentials`、`timeout`）；方法只许 fetch 的 7 个，`GET`、`HEAD` 不能带 body，构造时就抛；地址的规则 `fetch` 时才查；读 body（`text()` 等）、`clone()` | `mode`、`cache`、`redirect`、`referrer`、`integrity`、`keepalive`：应用不是网页来源，也不缓存 |
| `Response` | `new Response(body, { status, statusText, headers })`（`status` 200 到 599，204、205、304 不带 body）、`Response.json()`、`Response.redirect()`；`ok`、`status`、`statusText`、`headers`、`url`、`redirected`、`type`、`body`、`bodyUsed`、`text()`、`json()`、`arrayBuffer()`、`bytes()`、`blob()`；整个读可以读多次（同 fetch 的），当流读过就不能再整个读；内存里的能 `clone()` | `Response.error()`；网络来的不能 `clone()`（再整个读一次就行）；`formData()` |

没有的：WebSocket 的压缩扩展（permessage-deflate）、经 HTTP/2 的 WebSocket、WebSocket 的 `Blob` 消息；`URLSearchParams` 当 body（QuickJS 没有 `URL`）；上表各类「没有的」一列。

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
  - 前台应用能提示屏幕刷新率（`display.hint` 回答面板现在的刷新率），两种一起发被拒绝，连着发超过一次的上限被限频（§4.12）；
  - 没有对应权限时，不能装包、不能给自己授权、不能改系统设置、不能看相册里别人的照片，不能应答 NFC 读卡器（`nfc`）、不能看和改卡包（`nfc.state` / `nfc.set` 要 `system`）、不能读 NFC 卡或给它发命令（`nfc.read` / `nfc.transceive` 要 `nfc_reader`）、不能看和开关飞行模式、移动数据、个人热点，不能经蓝牙发文件，不能看、开始、停止或撤销从旧手机迁移（`radio.state` / `radio.set` / `hotspot.state` / `hotspot.set` / `bt.send` / `switch.*` 要 `system`），不能看、导入、连接、断开、删除 VPN、改它的始终开启和按应用或生成它的密钥对（`vpn.*` 要 `system`），不能排、看、取消定时通知（`notify.*` 要 `notifications`）；不要权限的 `locale.get`、`holidays.get`、`time.offsets` 答得出来，不认识的时区被拒绝；
  - 冒充不了系统界面（标题里有任何一种界面语言的「系统」一词，前面会加上应用名）。
- **灌装**：兼容测试开跑前，先模拟产线给设备灌装证书：一个代表认证方的 CA（`phone-provision`）签发设备证书，装进密钥服务（`csr` / `install_cert`），报告因此链到认证方的根，而不是设备自己生成的开发根。
- **报告**：JSON 格式，包括设备信息、每个用例的结果、每个能力的调用次数和成功、失败次数，以及从没被调用过的能力；`sandbox` 是设备的内核缺了沙箱和应用生命周期的哪几层：内核版本、`missing`（不在起作用的保护）和 `fallback`（换了做法、什么也没少的），各带原因（[24](24-sku-framework.md) §7.5）。缺了保护照样可以 PASS，由认证方决定收不收；`phone-cts-verify` 把缺的一项项列出来。
- **通过的条件**：所有用例通过，并且 §4 里的每个能力都至少被调用过一次。新增能力而没有补用例，兼容测试就不会通过。
- **报告有签名**：报告的 `attestation` 字段是设备密钥服务对报告摘要（去掉签名后的 JSON 的 SHA-256）做的设备认证，证书链连到根证书。厂商改动报告里的任何一项，签名就核对不过。认证方这样核对：

  ```bash
  phone-cts-verify --root root.pub cts-report.json   # 签名、证书链、结果都要通过
  ```

  `make cts` 开跑前用一个临时的 CA 模拟灌装（上一条），`phone-shell --cts` 把它的根另存为 `<报告>.root`，只用于演示；设备开机自测不灌装，根是设备第一次开机时自己生成的。量产时 CA 在认证方手里（HSM），认证方用自己手里的根核对，不信任报告里带来的任何东西。
- **认证按型号做一次**：厂商用这个型号的样机跑兼容测试，在认证方的门户上填企业名称、统一社会信用代码、型号，交上签了名的报告；认证方的后台当场核对签名连到它的根、结果是 PASS、设备没有解锁，工作人员审批后这个型号进公开的已认证名单（这部分服务不开源）。
- **每台设备只要灌装**：量产的每一台不再跑整套兼容测试，在产线工站上跑厂商自己的出厂测试，通过后联网领设备证书。工站读出设备的公钥申请（密钥服务的 `csr`），让设备对一个只用于这次请求的随机数做设备认证（应用名 `system.provision`，证明设备手里有这把密钥、没有解锁），交给认证方的灌装服务；服务核对工站的令牌和配额、型号已经认证，用 CA 签发证书，工站装回设备（`install_cert`）。CA 的私钥只在认证方那里，工站拿不到；每一台的签发都有记录。开源的这一侧是 `keystore` 包的 `ReadCSR`、`AttestFor`、`VerifyPossession`、`(*CA).Certify`、`InstallCerts`（`phone-provision` 是 CA 在手边时的本地版，测试用）；`keystore.CA` 的 Ed25519 钥可以是 HSM 里的（任何持有 Ed25519 钥的 `crypto.Signer`），SM2 钥还得是私钥本身（PKCS#11 没有标准的 SM2 机制）。真机上工站经 USB 连到设备、密钥放进 TEE 要随硬件做（**未实现**）。工作人员在审核台看到申请（报告里的系统版本、用例数、能力覆盖、渲染器、测试设备号，可下载原报告），认证或带理由驳回，记进审计日志；厂商凭申请编号查进度（`GET /v1/certifications/<编号>`）；已认证的型号公开（`GET /v1/certified`：厂商、信用代码、型号、系统版本、认证时间）。没给 `--cert-root` 的后台不收申请。
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
| TypeScript 应用（`"runtime": "js"`）：没有 `exec`，不带可执行文件和字节码（`.jsc`），`main` 是包根目录下的 `.js` 文件；机器码只能是 `native` 声明的模块 `lib/<架构>/<名字>.so`，是本机架构的库、接口版本对得上（§9.3）；商店审核用同一条检查（`jsrt.Check`，要 `arm64`） | 拒收 |
| 不能有 `granted`，不能声明仅系统权限（`system`、`install`），不能声明不认识的权限 | 拒收 |
| 不能和系统镜像里的应用同名 | 拒收 |
| 更新必须由同一个开发者密钥签名 | 拒收 |
| 以上都通过：系统弹框列出名称、版本、开发者、签发的商店和权限，带原生模块的写明含原生代码 | 用户取消则不安装 |

**装好以后**：
- 代码放在系统目录 `packages/<id>`，应用自己只能读和执行，不能改；
- 应用只运行包里的程序，不能借用系统目录里同名的程序；
- 签名清单随代码一起保存（`META/signature.json`）。每次开机后第一次启动、开发者模式变化之后，系统都按当时信任的商店根重新核对一遍：文件被改过、多出没签名的文件、商店不再受信任，都不让启动（`pkg.VerifyDir`）；
- 卸载时删除代码和数据，并清掉运行时授权和使用记录、后台任务、闹钟、照片授权、密钥服务里的全部密钥（应用自己的、它的小程序的、推送密钥），推送令牌作废。以后别的开发者用同一个 ID 发布的应用拿不到这些东西。

设备信任哪些商店：镜像里的 `share/phone/trust/<商店>.pub`；打开开发者模式（「设置 › 开发者模式」）时，再加上状态目录里的 `trust/`。原型的自测用状态目录里的「开发商店」签名（`pkg.DevStore`），它扮演商店后台。开发者模式关掉以后，开发商店签名的包装不上，已经装好的也不能启动。正式商店的根随产品镜像或产线灌装进来，开发者在商店后台注册（[07](07-ecosystem-and-store.md) §4.1）。

不打包时，也可以把应用目录直接放进 `share/phone/apps/`，这相当于系统镜像里的应用，只在开发机上这样做。

**符号文件**：TypeScript 应用的包里是压缩过的 `main.js`，`kario package` 把它的源码映射写进包旁边的符号文件（`app.phonesym`：zip，`symbols.json` 记着应用、版本、包和模块的 SHA-256，`main.js.map`，压缩过的 `main.js` 本身（栈里的函数靠它找回源码里的名字），可选没剥符号的原生模块），从不进包、不到手机上。`kario publish` 交了包再传它：`POST /v1/symbols/<应用>/<版本>`，开发者的密钥签 `phone-symbols/1|<名字>|<时间>|<应用>|<版本>|<符号文件的 SHA-256>`。正式商店核对它就是这个包的（包的摘要、模块的摘要、库的 build-id），只给开发者和审核人员，开发者在门户（或 `GET /v1/developer/crashes`）看到的崩溃报告里，栈已经映射回 TypeScript；自己手里的崩溃记录用 `kario symbolize -symbols app.phonesym` 映射。开源核心（模拟商店）不收符号文件。开发机 ✓（[29](29-app-development.md) §5.6），**未上真机**。
