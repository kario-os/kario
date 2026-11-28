# 平台架构：超级应用级的按键优先系统

> 状态：架构设计 + 代码原型。原型（[kario 仓库](../)，说明见 [13](13-prototype.md)）已实现的部分标 **原型 ✓**，其余为设计，**未实现**。
> 能力逐项清单见 [12-super-app-capabilities.md](12-super-app-capabilities.md)；交互语法见 [10](10-keyboard-interaction-model.md)。

## 0. 愿景与边界

**愿景**：一个按键优先、文字优先（「TUI 原生」）的移动平台。平台层提供超级应用（微信、支付宝这一级）需要的全部系统能力——通信、音视频通话、推送、扫码、可信支付、设备认证、小程序承载——让这类厂商可以用我们的 SDK 发布 `phone.Ui` 版应用。

**边界（诚实地说）**：能力齐全是厂商发布的**必要条件，不是充分条件**。厂商是否投入取决于用户规模、金融级安全认证和商务条件。架构能做的是两件事：把厂商的**技术移植成本降到最低**，并把**认证路径提前设计进去**（TEE、国密、生物认证标准）。近期定位仍是第二台 / 专注手机（D1），平台愿景是 D4（见 [09](09-ui-feasibility-review.md) §8）。

## 1. 术语：什么是「TUI 应用」

**TUI 应用 = 用 `phone.Ui` 写的应用**：应用只声明内容和每个元素能做什么，由系统渲染。同一个应用二进制：

| 运行环境 | 渲染器 | 媒体（视频 / 相机 / 二维码） |
|----------|--------|------------------------------|
| 开发机终端、SSH、串口 | 文本渲染器 | 半块字符像素（每格上下两像素，真彩色），低分辨率但真实 |
| 量产设备 | 像素渲染器（GPU） | 全分辨率；视频走 DRM 硬件图层直通 |

所以「TUI 系统里能视频通话」成立：视频是**系统合成的媒体面**，不是应用画出来的字符。原型已在终端里跑通视频通话（见 [13](13-prototype.md)）。

## 2. 架构原则

1. **应用描述，系统渲染。** 焦点、按键、菜单、返回栈、系统对话框全部归 Shell；任何应用天然可用按键完整操作。
2. **敏感交互一律走系统 UI。** 权限、支付确认、扫码、来电由系统绘制；应用拿到结果，拿不到输入和画面。
3. **身份由连接决定。** 应用的身份 = Shell 启动它时交给它的那条连接，无法伪造；所有服务调用都由 broker 带上调用方身份。
4. **四个平面分离**：UI 平面、能力平面、媒体平面、信任平面（§4）。
5. **最小能力。** 扫码只给结果不给画面；通话只给流句柄不给像素；签名只给证明不给私钥。
6. **可降级。** 同一应用在文本渲染器下仍可用，只是媒体分辨率降低。
7. **稳定窄腰。** `phone.Ui` 与能力目录独立版本化（§11），沿用 56（内部文档） 的 major / minor 规则。
8. **不做大而全 HAL。** 只支持自家 BOM（07 原则不变）。

## 3. 总体架构

```
┌────────────────────────── 应用（每个一个进程，沙箱）──────────────────────────┐
│ 系统应用：电话 短信 设置   第三方：聊天 钱包 …   厂商超级应用：phone.Ui 版（未来） │
│ SDK：TypeScript ✓（主力）· Go ✓ · C ABI ✓ · Rust（原生模块）· 任意语言          │
└───────────────┬──────────────────────────────┬──────────────────────────────┘
        UI 平面 │ phone.Ui 语义树 + 动作         │ 能力平面 req/res
┌───────────────▼──────────────────────────────▼──────────────────────────────┐
│ phone-shell：渲染器（文本 / 像素）· 焦点 / 菜单 / 返回栈 · 任务 · 通知        │
│              系统 UI（权限、可信确认、扫码、来电）· 能力 broker               │
└──────┬─────────────────────┬──────────────────────┬────────────────┬───────┘
  媒体平面│ 帧（dmabuf）         │ 控制 RPC              │ 信任平面        │
┌──────▼───────┐  ┌──────────▼────────┐  ┌──────────▼─────────┐  ┌───▼──────────┐
│ mediad       │  │ telephonyd (C++23) │  │ keystored → TEE TA  │  │ pushd · netd │
│ 相机 编解码    │  │ ModemManager       │  │ 设备证书 应用密钥    │  │ 统一推送 网络  │
│ 通话 扫码      │  │ 蜂窝 短信 VoLTE    │  │ 认证 可信确认 国密   │  │ pushd ✓       │
└──────────────┘  └───────────────────┘  └────────────────────┘  └──────────────┘
        Linux + BSP（Yocto Wrynose）· systemd · DRM/KMS · V4L2 · OP-TEE · 验证启动
```

## 4. 四个平面

| 平面 | 传输（原型 → 量产） | 内容 | 状态 |
|------|---------------------|------|------|
| UI | JSON 行，走 Shell 交给应用的 socketpair（fd 3）→ 二进制序列化 + 增量更新 | Screen / Node / Action；push / replace / update / pop | 原型 ✓ |
| 能力 | 同一连接上的 req / res，经 Shell broker 中转 | 能力名 + 参数；broker 做 manifest 声明检查、运行时授权、调用方身份 | 原型 ✓ |
| 媒体 | Unix socket 传 RGB 帧 → dmabuf 交 DRM 平面或 GPU 纹理 | 相机、通话远端、扫码预览；**只为屏幕上可见的面订阅** | 原型 ✓（合成画面源） |
| 信任 | keystored 进程 → OP-TEE 可信应用 + 安全存储 + 可信输入 | 设备证书链、应用密钥、PIN、设备认证、签名 | 原型 ✓（纯软件模拟） |

为什么把媒体单独成一个平面：像素量比界面树大几个数量级，而且出于隐私不应经过应用。应用在界面里放一个 `media` 节点，写上流句柄；帧从 mediad 直接到合成器。原型里应用进程从头到尾没见过一帧画面。

## 5. 进程与身份

- 每个应用一个进程，由 Shell 启动，**只继承一个 fd**（UI + 能力连接）。系统服务的 socket 是 0600，量产时再加独立 uid，应用无法绕过 broker 直连 mediad / keystored。
- 应用身份 = 签名包里的 manifest id（量产由商店签名）→ 绑定到这条连接 → broker 调用服务时带上 app id。keystored 按 app id 隔离密钥，钱包拿不到聊天的密钥。
- **沙箱**：每应用独立 uid、mount / pid / ipc / uts 命名空间、Landlock、seccomp、rlimit、cgroup，网络按应用开关（**原型 ✓**，[17](17-app-sandbox.md)）。开发机上 Shell 不是 root 时只有不需要 root 的几层。
- **系统助手**：解析外来数据、又不需要特权的代码不放在 Shell 里。连电脑的文件共享（HTTP、WebDAV、MTP）在 `phone-usbshared` 里，像安卓的 MTP 在 MediaProvider 进程里、不在 system_server 里：Shell 信任电脑后启动它，它有自己的 uid、空网络命名空间，Landlock 只许它运行自己的程序，seccomp 禁止 exec、新建套接字和按路径的文件操作；监听套接字和文件都由 Shell 打开后交 fd（§14），拔线、Shell 退出时它也结束（[27](27-usb-typec.md) §3.2，开发机、模拟器 ✓）。
- **进程归属**：应用、渲染器和 Shell 自己拉起的服务都设了父进程退出信号（`SIGKILL`），Shell 被强杀时一起结束，被冻结的也一样；设备上 systemd 停掉 Shell 时还会清掉它的整个 cgroup。Shell 自己拉起的服务退出后 1 秒重启，设备上由 systemd 负责。服务重启后，Shell 发现连着旧进程的连接已经断了，就先换新连接再发请求，所以重启后的第一个调用（比如紧急呼叫）不会因为旧连接而失败；请求已经发出、等回答时断开的，照旧报错，不会重发（`TestServiceRestartFirstCall`）。
- **内存**：Go 程序在 Yocto 里编成静态 PIE，不链接共享的 `libstd.so`，否则每个进程要为它的指针表多付约 6 MB 私有内存（[15](15-web-vs-semantic-benchmark.md) §5）。Shell 和它启动的全部进程不用透明大页（`prctl(PR_SET_THP_DISABLE)` 加 `GODEBUG=disablethp=1`），systemd 启动的 Go 服务在单元文件里设同样的变量：内核开着 THP 时，小堆碰到 2 MB 区域的一角就会占满整页。自测最后一步打印每个进程的 PSS（`mem-report`）。

## 6. 应用模型

| 方面 | 设计 | 原型 |
|------|------|------|
| 包 | `.phoneapp`（签名的 tar）；manifest：id、版本、权限、入口、`ui_mode`（semantic / canvas）、`min_platform` | 签名的 tar（`phoneapp/1`）✓；`min_platform` ✓，清单另有 `capabilities`（离不了的能力版本，[22](22-developer-api.md) §2.3） |
| 应用框架四档 | 原生语义应用 → 系统渲染文档 → JS 小程序 → Web 视图组件（D5 已定，[14](14-tui-route-and-app-framework.md) §5） | 四档都 ✓ |
| 生命周期 | 启动 → 前台 → 后台（可挂起）→ 终止；后台执行预算；推送唤醒 | 前后台事件 ✓；应用崩溃不影响 Shell ✓；冻结 ✓；推送唤醒与后台启动 ✓（[21](21-push.md)） |
| 导航 | 返回栈归 Shell：Back 即时响应、回到原位置；应用 push / replace / update / pop | ✓ |
| 通知 | 普通通知（横幅 + 通知中心）；通话类（系统全屏来电，CallKit 式） | ✓ |
| 后台通话 | 离开通话界面后通话继续，状态栏显示通话指示，一键返回 | ✓ |
| 权限 | manifest 声明 + 运行时授权（相机、麦克风）+ 设置里可撤销 | ✓ |

## 7. 安全与信任（支付级）

**信任链（设计）**：

```
验证启动：BootROM → bootloader → kernel + initramfs → rootfs（dm-verity，root hash 由更新密钥签名；QEMU 上已实现，[25](25-system-update.md)）
   → TEE（OP-TEE）
      → 设备密钥（工厂注入；证书由厂商根签发）
         → 应用密钥（TEE 内生成；证书由设备密钥签发，标明是否需要用户验证）
            → 可信确认（系统 UI 采集 PIN / 指纹，TEE 内校验；签名覆盖显示的文字）
               → 服务端逐级验证：厂商根 → 设备证书 → 应用密钥证书 → 签名 → 内容核对
```

已实现的部分（软件模拟，接口按 TEE 设计）：
- **产线灌装**：密钥服务给出设备 ID 和两把设备公钥（Ed25519 和 SM2，`csr`），认证方的 CA 用各自的根签发两张设备证书（`phone-provision`，`keystore.Provision`），连同根公钥装回设备（`install_cert`，先核对证书确实是给这台设备的）。装上以后，设备不再用开机时自己生成的开发根。
- **两条证书链**：Ed25519 链（根 → 设备密钥 → 应用密钥）是默认；国密链从根到签名全是 SM2：认证方的 SM2 根签设备的 SM2 密钥，设备的 SM2 密钥签 SM2 应用密钥的证书，应用密钥签 SM3 摘要。`root.pub` 第一行是 Ed25519 根，第二行 `sm2:<公钥>` 是 SM2 根；`keystore.LoadRootPub` 读出两者，核对时按证明里的 `alg` 选链。设备认证也可以走 SM2 链（`attest.get` 带 `alg: "sm2"`）。
- **解锁**：机主在「设置 › 解锁设备」确认后，密钥服务清空全部应用密钥（推送密钥也在内，Shell 给在运行的应用发 `push.key_changed`，让它们把新公钥交给服务器，[21](21-push.md) §4.1），设备认证的启动状态改报 `unlocked`；这时可以换上自己编译的渲染器（状态目录里的 `renderer`），镜像自带的渲染器留作备用。重新上锁再清空一次。支付类服务看到 `unlocked` 可以拒绝。

| 环节 | 原型 | 量产还需要 |
|------|------|-----------|
| 设备密钥与证书 | ✓ 首次启动生成 Ed25519 和 SM2 两把，工厂各签一张证书；恢复出厂设置留下它们，换掉数据区的盐（[31](31-lock-and-biometrics.md) §11.3） | 工厂注入，根密钥在厂商离线 HSM |
| 应用密钥与证书 | ✓ 按 app id 隔离，标明 `user_auth`；算法 Ed25519 或国密 SM2（`alg: "sm2"`，摘要用 SM3），算法写进证书；SM2 应用密钥由设备的 SM2 密钥签证书，整条链都是 SM2 | 在 TEE 内生成与使用 |
| 设备认证（风控用） | ✓ nonce 防重放，服务端可验；可选 SM2 链 | 加入验证启动的度量值 |
| 可信确认 | ✓ 系统 overlay 采集 PIN，应用读不到；签名摘要覆盖「提示文字 + 订单」；也可以用指纹确认：`bio_key` 是绑定指纹的密钥，匹配后才签名，录入或删除指纹即作废（[31](31-lock-and-biometrics.md) §5） | TEE 控制键盘与屏幕的可信输入路径 |
| PIN 防暴力 | ✓ 支付密码 5 次失败锁定；锁屏 PIN 码存慢哈希，连错 5 次起按次数等待（30 秒起），重启不清零（[31](31-lock-and-biometrics.md) §3） | TEE 内计数与时间退避 |
| 服务端验证 | ✓ `keystore.VerifyConfirm` 等 | 厂商自己的服务端实现 |
| 国密 | ✓ SM2 签名、SM3 摘要、SM4 分组密码（`cipher.Block`，配 GCM 即 SM4-GCM）。`gm` 是 `github.com/emmansun/gmsm`（MIT）上的一层薄封装：曲线运算和分组密码是恒定时间的（amd64、arm64 有汇编），签名随机数是对冲式的（随机数和私钥、消息一起派生，随机数发生器坏了也不泄露私钥）；测试用国标示例和独立实现 gmssl 核对。SM2 设备证书链 ✓；国密 TLS（TLCP，GB/T 38636）✓，推送通道可选（[21](21-push.md) §2），和独立实现 GmSSL 3.1 的 `tlcp_client` 互通。兼容测试检查 SM2 签名、SM2 设备认证和整条 SM2 证书链 | 在 TEE 或安全芯片里实现 |
| 生物识别 | ✓ 指纹解锁、指纹支付：模板和比对在 keystored 里，匹配只给认证令牌（照 Android 的 HardwareAuthToken），Shell 只转交；传感器是模拟的（[31](31-lock-and-biometrics.md)） | 放进 TEE（R15）；手机上的 FPC、汇顶传感器没有主线驱动，要厂商 TEE，**未实现** |
| IFAA（支付宝牵头）/ 腾讯 SOTER 类标准对接、金融级安全检测 | ✗ | 全部未实现，需要硬件 |

## 8. 视频通话端到端

| 步骤 | 原型里发生了什么 | 量产差异 |
|------|------------------|----------|
| 1. 应用请求 `media.call.start{peer, video}` | 聊天应用发 req | — |
| 2. broker 检查 manifest（麦克风、摄像头）并首次弹系统授权 | 系统权限框 ✓ | — |
| 3. mediad 建立会话，返回本地流、远端流句柄 | 本地画面来自 V4L2 摄像头（mediad 启动时找 `/dev/video*`，模拟器上是内核的 vivid 虚拟摄像头，YUYV 经 mmap 缓冲区采集），没有摄像头时用合成的「自拍」；远端是合成的人物 + 运动小球 | libcamera 管 ISP 和传感器；硬件编解码（V4L2 M2M）；回声消除 / 降噪；网络传输由厂商自带协议，或平台提供 WebRTC 栈 |
| 3a. 应用开两路「通话」类别的音频：对方的声音、麦克风 | 鸿雁 ✓（对方的声音是模拟的） | 编码后经网络发送 |
| 4. 应用把 `media{stream: 远端, pip: 本地}` 放进界面 | 应用只拿句柄 | 厂商自研解码器可走「应用自产帧」：解码输出写共享缓冲，仍由系统合成 |
| 5. Shell 只为可见的面订阅帧并合成 | 终端里半块字符真彩色；15fps | DRM 平面直通、零拷贝 |
| 6. 离开通话界面 | 取消订阅、通话继续、状态栏「● 通话」 | 同 |
| 7. 来电 | 应用后台时由系统全屏接听界面接管 | VoIP 推送唤醒或启动应用 ✓（[21](21-push.md)） |

## 9. 扫码付款与付款码端到端

1. `scan.qr` → 系统扫码界面（应用看不到画面）→ 返回识别文本。
2. 应用展示订单 → `auth.confirm{key, prompt, payload}`。
3. 系统可信确认界面（状态栏显示「安全模式」，应用无法覆盖或读取输入）→ PIN → keystore 校验并签名，摘要覆盖 `prompt + payload`。
4. 应用把证明交给服务端 → 服务端验证证书链，并核对签名内容就是确认框里显示的文字，才扣款。
5. 付款码：`keystore.sign`（无需每次确认的 `paycode` 密钥）+ **安全画面**：截屏、录屏、投屏时被系统遮挡。原型的自动化快照中付款码就是被遮挡的。

原型全部跑通（[13](13-prototype.md) 的自测用例）。识别是真的：mediad 每秒四次从相机画面（640×480 的灰度）里解码，读出第一个码就交给应用；解码器是 gozxing（ZXing 的 Go 移植，MIT，ZXing 原有的部分 Apache-2.0），放在拥有相机、本来就在沙箱里的 mediad 里，坏画面最多弄崩 mediad，伤不到 Shell，画面也不出 mediad（帧的大小先设上限、解码器 panic 当作读不出，[17](17-app-sandbox.md) §9）。开发机上的合成相机拍的是木桌上一张带透视、抖动、失焦和噪声的二维码卡片，模拟器上同一张卡片叠在 vivid 的测试图上，都要从像素里认出来；自测用 `scan-code` 换卡片上的码（只对合成相机和 vivid 这类测试驱动有效，真的传感器拒绝；开发机上 phone-shell 自己起的 mediad 带 `--test-codes`，笔记本的摄像头前也能叠）。真机相机上的识别**未上真机**。

## 10. 小程序与 H5 的位置

`phone.Ui` 是**数据，不是代码**。超级应用可以把服务端下发的界面树（小程序页面）直接转交 Shell 渲染，等价于「服务端驱动 UI」，而且天然具备按键可用性。

| 部分 | 状态 |
|------|------|
| JS 小程序：逻辑在 JS 引擎（无 DOM），视图是 `phone.Ui`，由宿主应用承载 | **原型 ✓**（`apps/miniapp`，每个小程序一个 kario-jsrt 进程，和 TypeScript 应用同一个引擎 QuickJS-ng，[29](29-app-development.md) §5.4；结构与微信小程序的逻辑层 / 视图层分离一致，见 [14](14-tui-route-and-app-framework.md) §4） |
| 来源标注：宿主代小程序请求能力时，系统对话框写明小程序名称 | **原型 ✓**（`req.origin`） |
| 子树委托：授权、密钥按「宿主 + 小程序」记账，小程序不能冒用宿主身份 | **原型 ✓**：运行时授权和使用记录按「宿主/小程序」记，宿主的授权不传给小程序；`keystore.sign`、`auth.confirm`、`attest.get` 用小程序自己的密钥和身份（`com.phone.miniapp/晨光咖啡点餐`），商户服务端据此核对是哪个小程序 |
| 小程序分发：签名的小程序包（同应用包格式，由宿主厂商的小程序商店签发），宿主装之前和每次启动前验签，改过的不加载；每个小程序的存储在自己的目录里，限 1 MB | **原型 ✓**（`apps/miniapp/install.go`） |
| 文档节点：文章类富文本 | **原型 ✓**（`doc` 节点，Markdown） |
| H5 网页 | 原型 ✓：第 4 档 Web 视图（`web` 节点，WPE WebKit 在独立进程、宿主的网页 uid 下渲染，经共享内存上屏），文本端降级为阅读模式（[14](14-tui-route-and-app-framework.md) §5） |

## 11. 版本与兼容

- `phone.Ui`：握手时交换 `"phone.ui/0"`；次版本只做增加，不认识的字段和消息类型都忽略；换主版本时过渡期里系统同时说前一个主版本，两边取都说的最新的一个，没有共同的主版本才不接受；要删的东西提前一个主版本标成弃用；规则和验证（录下的消息、旧提交的应用程序）见 [22](22-developer-api.md) §2.1。
- 健壮性：每个应用的限额（消息大小、速率、队列、未回复的请求）和对系统服务每次调用的期限见 [22](22-developer-api.md) §8.1。
- 能力目录：每个能力独立版本（`media.list` 就是 `media.list@1`，不兼容的改动是 `media.list@2`，旧版本至少再提供 3 个平台级别），见 [22](22-developer-api.md) §2.3；开发机上 ✓。
- 平台级别（现在是 2）+ manifest `min_platform`；安装器、商店后台和商店应用按它分发（[22](22-developer-api.md) §2.2）；开发机上 ✓。
- 渲染器协议 `phone.frame/1`：换主版本时同样协商，见 [23](23-renderer-protocol.md) §2。

## 12. 关键架构决策与取舍

| # | 决策 | 放弃的方案 | 理由 | 代价与后续 |
|---|------|-----------|------|-----------|
| A1 | 语义界面：应用交内容 + 动作，系统渲染 | 应用自己画（Android / iOS 模式） | 按键可用性、系统 UI 可信、同一应用文本 / 像素两种渲染、小程序可直接复用 | 自由界面受限 → 画布应用作例外（[07](07-ecosystem-and-store.md)） |
| A2 | 每应用一条连接，身份 = 连接 | 全局总线（D-Bus）+ 应用自报身份 | 身份无法伪造；应用碰不到其他服务；天然适合沙箱 | 所有能力经 Shell 中转，Shell 要稳；后续可把 broker 拆成独立进程 |
| A3 | broker 先放在 Shell 里 | 独立 broker 守护进程 | 系统 UI（授权、确认）和 broker 必须同步，放一起最简单 | Shell 崩溃影响面大；接口已按「可拆分」设计 |
| A4 | 媒体单独成平面 | 帧走 UI 协议 | 像素量大、隐私（应用看不到画面）、可走硬件图层 | 需要句柄生命周期管理（原型：只订阅可见面） |
| A5 | 敏感交互由系统绘制 | 应用内自绘支付框 + SDK | 防覆盖、防读取、所见即所签 | 厂商品牌表达受限；可开放有限的样式参数 |
| A6 | v0 协议用 JSON 行 | 一开始就上 protobuf / Cap'n Proto | 任意语言零依赖可写（Python 示例证明）、调试直观 | 体积与解析开销 → 量产换二进制 + 增量，握手协商版本 |
| A7 | Linux + 自有运行时 | AOSP 底座 / Android 容器 | 精简、可控、按键优先从底层贯彻（D1、D2） | 没有现成应用生态 → D4：用能力目录与低移植成本争取厂商 |
| A8 | 信任链先软件模拟、接口按 TEE 设计 | 先做 TEE 再做上层 | 先验证端到端流程与服务端验证模型 | 安全性在 TEE 落地前不成立（原型只作演示） |
| A9 | 语义优先，浏览器只作第 4 档受控组件（D5 已定，实测见 [15](15-web-vs-semantic-benchmark.md)） | 浏览器引擎作应用底座（KaiOS / Firefox OS 模式） | 平台能力（推送、可信支付、来电、按键）浏览器不提供；文本渲染器需要语义；资源与仿冒风险（[14](14-tui-route-and-app-framework.md) §4） | 存量 H5 要等 Web 视图组件；富排版先由 `doc` 节点覆盖 |
| A10 | 第三方应用进程隔离；进程内插件只给 OEM 系统扩展 | DSH 式进程内插件生态 | 进程内插件完全信任，无法承载支付与陌生代码 | 第三方扩展不能改 Shell 行为；系统扩展走产品配置层和能力提供者服务（[14](14-tui-route-and-app-framework.md) §2.1），不进 Shell 进程 |
| A11 | 输入法归系统（与安全确认同一层），应用只收到上屏文字；输入框声明键盘类型（[16](16-input-method.md)） | 应用自带输入法；Android 式可替换的第三方输入法 | 第三方输入法能看到所有按键（含聊天、搜索）；小屏上候选栏要和焦点、软键统一布局；九宫格与全键盘要在所有应用里一致 | 初期没有搜狗、讯飞这类第三方输入法；以后可作为系统服务接入：输入法本来就是独立进程 `phone-imed`（[16](16-input-method.md)），换实现只换这个服务 |
| A12 | Shell 核心只有一份（Go），像素渲染器只是投影：Shell 发「帧」，它回按键；渲染器是可替换的插槽，默认 phone-paint，备选 phone-gui（[06](06-gui-architecture.md)、[23](23-renderer-protocol.md)） | 在 Rust 里再写一个带焦点、菜单、broker 的 Shell | 系统界面行为、安全规则、自测在两种渲染器下天然一致；只维护一份 | 渲染器进程间多一跳；视频帧目前经 Shell 转发（量产改 dmabuf） |
| A13 | 应用沙箱用启动器 `phone-sandbox`：命名空间 + 独立 uid + Landlock + seccomp + rlimit，后台 10 秒冻结（[17](17-app-sandbox.md)） | SELinux 策略；容器运行时（bubblewrap / runc） | 全部是内核自带机制，不需要编写和维护 MAC 策略；按版本降级 | 每个应用一个 cgroup（资源限制与冻结器）；网络策略和流量计数靠 nftables 按 uid 区分 |
| A14 | 电话、闹钟、通讯录是系统服务：来电与闹钟由系统全屏接管，应用被冻结或没运行也照常；第三方应用拨号需用户在系统对话框确认（[19](19-core-apps.md)） | 由各应用自己常驻后台处理 | 和后台冻结（A13）一致；来电、闹钟是不能漏的事件；拨号属于付费 / 骚扰风险操作 | 电话、闹钟的界面细节由系统定，应用不能完全自定义 |

## 13. 实现语言：原型与量产

| 组件 | 原型 | 量产（建议） |
|------|------|--------------|
| phone-shell 文本渲染器 + broker | Go（Bubble Tea） | Go，保持开源 |
| 像素渲染器 | Rust：phone-paint（tiny-skia）、phone-gui（Slint） | phone-paint，Apache-2.0；CPU 上栅格化，GPU 合成可选（GLES 2 加 GBM，[37](37-motion.md) §5，[42](42-gpu-rendering.md)；QEMU 上用软件 GPU 验证过），GPU 栅格化也可选（[37](37-motion.md) §5 第 10 条），都**未上真机** |
| mediad | Go，合成画面源 | Rust 或 C，基于 libcamera / V4L2 / GStreamer |
| keystored | Go，软件密钥 | OP-TEE 可信应用（C）+ 用户态代理 |
| telephonyd | — | C++23（[04](04-ui-language-telephonyd.md)） |
| SDK | TypeScript（kario-jsrt）✓、C ABI（`sdk/c`）✓、Go；Python 无 SDK 直连示例 | C ABI（厂商复用 C++ 核心）、Rust、Go |
| 小程序逻辑层 | QuickJS-ng（kario-jsrt，每个小程序一个进程，[29](29-app-development.md) §5.4；原来是 goja） | 性能不够时再换 Hermes 或 V8；接口保持 `Page` / `setData` / `phone.*` |
| Markdown | goldmark | 同（像素渲染器复用同一 AST） |
| 镜像 | Yocto Wrynose `meta-phone` | 同，加 BSP 层与签名 / OTA |

## 14. 文件描述符传递：权限在建立时检查，数据直达

Linux 可以把一个打开的文件描述符（文件、套接字、共享内存、设备）通过 Unix 套接字交给另一个进程（`SCM_RIGHTS`）。收到的一方得到的是「已经打开的对象」，不需要也不能再按路径去打开它。

这正好适合我们的结构：
- Shell 是唯一的仲裁者，但不应该成为数据通道。
- 应用在沙箱里，Landlock 不让它按路径访问任何系统资源，但交到它手里的 fd 可以正常使用。所以 fd 就是一张「能力凭证」：Shell 检查权限后发出去，数据从此在两端之间直接流动。

`wire` 层的实现（`wire/wire.go`）：
- 消息里写明自己带了几个 fd（`fds` 字段）。接收方按到达顺序排队，处理到这条消息时取走对应数量。即使 JSON 解码器预读了后面的消息，fd 也不会错配（有单元测试）。
- 收到的 fd 一律设为执行时关闭（CLOEXEC），不会泄漏给 Shell 之后启动的应用。Shell 发出后关掉自己手上的那份。

| 场景 | 做法 | 状态 |
|------|------|------|
| 应用的界面连接 | Shell 建 socketpair，作为 fd 3 由启动的应用进程继承 | ✓ 已有 |
| 应用自己的声音、录音（[20](20-audio.md)） | audiod 建一块共享内存（memfd）做环形缓冲区，经 Shell 交给应用；之后 PCM 不经过 Shell。memfd 封住大小，应用截短不了它，没法让 audiod 因 SIGBUS 崩溃 | ✓ |
| 媒体画面直通 | mediad 把每路视频画进一块共享内存（memfd，两格轮换，封住大小），经帧套接字把 fd 交给 Shell；Shell 经渲染器的侧通道（启动时继承的 fd 3，`SOCK_SEQPACKET`）原样转交，之后每帧只转一个帧号，像素不经过 Shell（[23](23-renderer-protocol.md) §4.2）。量产换成 dmabuf，直接上 DRM 平面 | memfd ✓（phone-paint）；dmabuf 依赖硬件 |
| 相册选择器（`media.pick`） | 用户在系统选择器里选中，系统打开只读 fd 交给应用。应用看不到其他文件，这就是 powerbox 模式，和 Flatpak 的文档门户、Android 的照片选择器同一思路。用户亲手选择本身就是授权，所以不需要相册权限 | ✓ |
| 拍照结果 | mediad 把照片编码成 PNG 写进 memfd，交给 Shell 存入系统相册 | ✓ |
| 视频播放（`video.open`） | 媒体库归 Shell，mediad 读不到：Shell 打开视频文件、向 audiod 要一块声音环，两个 fd 随请求交给 mediad（只有 mediad 的服务接收请求里带 fd，它唯一的客户端是 Shell；别的服务照旧拒收）。mediad 的 ffmpeg 读这个 fd 解码，画面进 mediad 的表面，声音写进环。应用只拿到流名 | ✓ |
| 连电脑的文件共享（[27](27-usb-typec.md) §3.2） | 共享助手按路径什么都打不开：80 端口的监听套接字由 Shell 建好让它继承；电脑下载时 Shell 打开文件交只读 fd，上传时交一根管道的写端，Shell 的库从读端边收边存，助手说完整、字节数对得上才留下；助手发来的 fd 一律关掉 | ✓（开发机和模拟器） |
| 照片缩略图、壁纸 | Shell 自己建表面（裁成格子或屏幕大小，写一次）交给渲染器；宫格里只有焦点附近几行的照片带表面，一帧不再用的表面 Shell 关掉、渲染器解除映射（[23](23-renderer-protocol.md) §4.2） | ✓ |
| 文件选择器（任意文件） | 同相册选择器：文件属于用户，放在系统的「文件」区；`file.pick` 在系统列表里由用户选，交出那一个文件的只读 fd；`file.save` 先问用户；分享文件只把那一个授权给接收方（`file.open`） | ✓ |
| 按应用联网 | 没有 `net` 权限（或被用户关掉网络）的应用进自己的空网络命名空间；只用 Wi-Fi 和按应用流量由 Shell 用 nftables 按 uid 管（[17](17-app-sandbox.md) §1）。Android 的 netd 也是按 uid 打标记、计数 | ✓ |
| 服务按需启动 | systemd 先打开服务的监听套接字，有连接时才启动服务（socket activation），节省开机时间和内存。keystored、mediad、audiod 已这样启动（`phone-*.socket`）；pushd、telephonyd 要一直连着网关和基带，开机就启动 | ✓ |
| Shell 崩溃、重启不连累应用 | 见下文 | ✓ |
| 显示和键盘不留 root | phone-paint 以 root 起来，打开 `/dev/dri` 后降权（nobody、空网络命名空间、Landlock、seccomp）；`/dev/input` 的设备由降权前、打开屏幕之前分出的输入助手打开（nobody，只留读设备节点的能力，Landlock 只放行 `/dev/input`），经套接字交 fd；没用 logind。变成 nobody 会清掉父进程退出信号（credentials(7)），两者降权以后各自重新设上：渲染器随 Shell 结束，输入助手随渲染器结束，渲染器那端的套接字关了它也结束（`paint/src/input.rs`、`kms.rs` 的测试，要 root 的那两个在用户命名空间里跑过：`podman unshare cargo test --release`） | ✓（模拟器） |

**Shell 重启，应用不重启。** 应用的界面连接（Shell 那一端）交给下一个 Shell，它接管连接、让应用重发页面：

- **交出连接**：服务开了 fd 存储时（`PHONE_FDSTORE=1`），Shell 每启动一个应用，就把它的连接存进 systemd 的 fd 存储（`FDSTORE=1`，名字 `app-<应用 id>-<pid>`）。Shell 崩溃后 systemd 重启它，经 `LISTEN_FDS` 交还这些连接。Shell 升级或自测时原地重启：exec 自己，连接留着不关，编号写在 `PHONE_HANDOFF` 里。应用自己的长连接（`net.keepalive`）在系统手里的那份套接字也这样交（名字 `keep-<应用 id>-<pid>`），它的心跳安排和计数在状态目录里，交不过来时应用收到 `lost` 自己重连（[21](21-push.md) §8.2）。
- **接管**：新 Shell 按名字找到应用，被冻结的先解冻，然后发 `resync`。Go SDK 记着 Shell 那边的页面栈，收到后重发（reset，再逐页 push）；旧 Shell 没回复的调用返回错误。C SDK 把 `resync` 交给应用自己重绘。接管来的应用不是新 Shell 的子进程，它的退出看连接断开（`/proc` 里成了僵尸也算退出）。
- **进程不能跟着死**：父进程退出信号是在「fork 出子进程的那个线程」结束时发的，而 exec 会结束调用线程以外的所有线程。exec 之后，调用线程就成了新程序的主线程。所以 Shell 的子进程都从主线程启动，原地重启也从主线程 exec：只有主线程在每一代程序里都是同一个线程。换成别的线程的话，第一次重启后它就成了另一个线程，第二次重启会把第一代程序启动的子进程（开发机上 Shell 自己起的服务也在内）全部杀掉（`selftest.script`「Shell 重启」连着重启两次）。用 fd 存储时应用不设这个信号（沙箱启动器切换 uid 后本来会重设它，策略里的 `survive` 关掉这一步）；单元是 `KillMode=mixed`：停止时 SIGTERM 只发给 Shell，由它自己先解冻再结束全部应用，它退出以后服务的控制组里剩下的（渲染器、它的输入助手、别的辅助进程）被 SIGKILL，崩溃后重启时也一样；应用都在各自的作用域里，碰不到它们。以前是 `KillMode=process`：一加 6T 上停掉服务以后，渲染器 fork 出的输入助手留了下来（降权清掉了它的父进程退出信号，它又只在有新设备时才发现渲染器不在了），它 fork 时继承了屏幕两块扫描缓冲区的映射，一直占着显示的 DRM master，下一个 Shell 的渲染器打不开屏幕（`Permission denied`），服务一遍遍失败（2026-11-24；在 QEMU 上照样复现，内核的 DRM 客户端列表里那个已经退出的渲染器的 master 一直在，杀掉输入助手就好）。`scripts/yocto-check.py` 不让跑 phone-shell 的单元用 `process` 或 `none`。
- **单元配置**：`FileDescriptorStoreMax=512`（每个运行中的应用一个位置，应用自己的长连接最多再占四分之一，从不挤掉应用的连接；满了 systemd 悄悄关掉多出来的，所以 Shell 按 `$FDSTORE` 自己守着，[28](28-battery.md) §8）、`NotifyAccess=main`（不写的话 systemd 不收 fd 存储消息）、`KillMode=mixed`、崩溃后重启。
- **应用放进各自的作用域**：systemd 重启服务时，总是先把新进程放进服务的主 cgroup（systemd 259 的源码里写明了，`DelegateSubgroup=` 也是之后才移进去）。如果应用的组建在 Shell 的服务组下面，主 cgroup 要为子组打开控制器，按 cgroup v2 的规则就不能再放进程，重启失败（`Device or resource busy`）。所以显示服务里每个应用在自己的 systemd 作用域里（`phone-apps.slice/app-<id>-<pid>.scope`，[17](17-app-sandbox.md) §5），Shell 的服务组只有 Shell 自己。
- **验证**：自测里 Shell 原地重启后，计算器的进程号不变、页面原样恢复，之后的用例和整套兼容测试都在新 Shell 上跑（开发机和设备都通过）。fd 存储这条路：设备开机自测起一个和显示服务同样配置的检查服务（`phone-fdstore-check.service`，应用作用域 + fd 存储），Shell 故意崩溃，systemd 重启它，新 Shell 从 fd 存储接管计算器，计算器仍在自己的作用域里，内存和进程上限照旧，后台冻结照常（`fdstore.script`）。检查服务的存储只有 4 个位置，同一个脚本里还有两次崩溃：两个应用自己的连接争 1 个位置、没放进存储的那个崩溃后收到 `lost`（`full`），服务器断开过的那条 systemd 不留、崩溃后是 `dropped`；查的是 systemd 存储里真有的（[13](13-prototype.md) 的用例表，[40](40-testing.md) §2），journal 里不能有 systemd 放不下的记录。

**不必限制「一次只能带一个 fd」**，真正需要管住的是这几条：
- **方向**：文件只从系统流向应用。Shell 对应用的连接、各系统服务对 Shell 的连接，都会立即关闭对端发来的 fd（`Conn.RefuseFiles`）。否则应用不停地塞 fd，就能耗尽 Shell 的文件描述符表，所有应用都会受影响。有单元测试覆盖。
- **数量**：一条消息最多 8 个 fd；每个应用最多 4 路音频流、1 路录音；audiod 另有 32 路的总上限，防止应用反复申请共享内存。
- **类型**：系统只发自己验证过的对象，比如封住大小的 memfd、只读打开的文件。接收方也再检查一遍，比如没封印的环形缓冲区拒绝映射。

另有一个 `pidfd_getfd` 可以直接从别的进程「拿」fd，它需要调试权限。我们的 seccomp 对应用禁止了这个调用（[17](17-app-sandbox.md) §4）。

## 15. 云端服务：设备连到哪里

商店、推送网关、更新服务器在云端，地址和信任都由产品层给出，平台不写死任何地址。产品层用 Yocto 变量 `PHONE_SERVICES` 指向一个目录，`phone-services` 配方把它装进镜像：

| 文件 | 装到 | 内容 |
|------|------|------|
| `services.env` | `/usr/share/phone/services.env` | `PHONE_STORE_SERVER`（商店后台）、`PHONE_PUSH_GATEWAY`（推送网关）和 `PHONE_PUSH_GATEWAY_CERT`（要钉住的网关证书）、`PHONE_UPDATE_URL`（更新服务器）和 `PHONE_UPDATE_KEY`（`latest.json` 的签名公钥，[25](25-system-update.md) §0）、`PHONE_ACCOUNT_SERVER`（Kario 账号服务器，通讯录经它的 CardDAV 同步，[19](19-core-apps.md) §1.1；不写就没有 Kario 账号，只能加别的 CardDAV 服务器）和 `PHONE_FAMILY_KEY`（账号服务器给未成年人模式的远程策略签名的公钥，[19](19-core-apps.md) §1.2；不写就没有家长远程管理）；哪一项都可以不写 |
| `*.pem`、`*.pub` | `/usr/share/phone/services/` | 网关证书、`latest.json` 和未成年人模式策略的签名公钥；换钥时一个 `.pub` 有几把钥、一个 `.pem` 有几张证书（§15.1） |
| `trust/<商店>.pub` | `/usr/share/phone/trust/` | 设备信任的商店根，文件名是商店证书里的名字（`phone-storesrv` 的是 `store`），一行一把钥（§15.1） |

- phone-pushd、鸿雁的模拟服务器和显示服务（Shell）的单元先用本机的默认值，再读 `services.env` 覆盖（systemd 的 `EnvironmentFile`）。
- 写了哪一项，配方就留一个标记（`services/store`、`services/push`、`services/update`）。设备上模拟的商店后台和推送网关只在没有对应标记时启动，所以可以只把商店和更新服务器放到云端，推送还用设备上的模拟网关。
- 没有 `PHONE_SERVICES` 时这个包是空的，镜像和以前一样，全部用设备上的模拟服务。
- **验证**：`make yocto-cloud-test` 在构建容器里起商店后台、推送网关和一个更新服务器（QEMU 里看是 10.0.2.2），按它们生成的商店根和网关证书写出这个目录，带着它构建镜像，在 A/B 磁盘上开机。设备上的 `cloud-test` 先确认模拟的商店和网关没有启动，再跑三步：开发者经云端商店发布的应用从目录装上；鸿雁的回复经云端网关推到手机；「设置 › 系统更新」读到云端的 `latest.json`。最后不带这个目录再构建一次镜像。
- **未实现**：云端网关还不要求设备的证书连到认证方的根：内测手机没有灌装，根是第一次开机时自己生成的；量产的手机在产线灌装以后（[22](22-developer-api.md) §11）才能打开。

### 15.1 信任点和换钥

手机里有四处只认产品层给的钥（信任点）。每一处都可以同时认几把，所以一把钥能换成下一把，中间不会有一段时间手机两把都不认：

| 信任点 | 镜像里的文件 | 签的是什么 | 签名怎么说是哪把钥 | 手机怎么验 |
|------|------|------|------|------|
| 商店根 | `trust/<商店>.pub` | 开发者证书、应用目录、吊销清单（[07](07-ecosystem-and-store.md) §4.1） | 证书、目录、清单里的 `key` | `pkg.LoadRoots`、`pkg.Open`、`pkg.VerifyCatalog`、`pkg.VerifyRevocations` |
| 更新服务器 | `PHONE_UPDATE_KEY` 指的 `.pub` | `latest.json`、`holidays.json`（[25](25-system-update.md) §0） | 响应头 `X-Offer-Key` | `ota.VerifyHeaders` |
| 账号服务器 | `PHONE_FAMILY_KEY` 指的 `.pub` | 未成年人模式的策略、加时、家长 PIN 码重置（[19](19-core-apps.md) §1.2） | 签好的策略、加时、重置里的 `key` | `family.Verify`、`family.VerifyGrant`、`family.VerifyReset` |
| 推送网关 | `PHONE_PUSH_GATEWAY_CERT` 指的 `.pem` | TLS 1.3 或 TLCP 的握手（[21](21-push.md) §2） | 网关出示的证书本身 | `push.Client` |

- **几把钥怎么写**（`keyset`）：`.pub` 一行一把 Ed25519 公钥（base64；空行和 `#` 开头的行不算，同一把写两遍算一把），钉的 `.pem` 里可以有几张证书，都是一种：都是 SM2 的（TLCP 的 CA），或者都不是（TLS 的网关证书）；两种混在一起不认。平时只有一把；换钥时是签名的那一把加上下一把，或者加上刚换下来的那一把。同一个商店的钥在镜像里、产线灌装的 `factory/stores` 里和开发者模式的 `trust` 里有几份，合在一起都认。
- **钥号**：一把 Ed25519 公钥的钥号是它的 SHA-256 的前 8 个字节，写成 16 个十六进制字符（`keyset.ID`）。签名带着签它的钥的钥号，手机只用文件里这一把验，不一把一把试；文件里没有这把钥就不认（错误里写着钥号）。不带钥号的签名（还没有钥号时的服务器签的）拿文件里的每一把试，有一把对得上就行。推送网关没有钥号：TLS 是网关出示的证书要和钉的某一张一字不差，TLCP 是网关的证书要连到钉的某一个 CA。
- **旧镜像照常用**：钥号都在签名的内容之外（开发者证书的 `key` 不在开发者签的那部分里，吊销清单算签名时把 `key` 和 `sig` 一起去掉），旧系统读到不认识的字段就忽略，所以还没更新的手机照样验得过用现在那把钥签的东西（`pkg` 的 `TestOldSystems` 照旧系统的读法核对）。
- **签名的一方拿 `crypto.Signer`**：`pkg.Store.Key`（商店根）、`keystore.CA.Key`（认证方的 Ed25519 CA）、`family.Sign`、`family.SignGrant`、`family.SignReset` 的钥、`ota.SignHeaders` 都接受 `ed25519.PrivateKey`，也接受任何持有 Ed25519 钥的 `crypto.Signer`（比如 HSM 里的钥）：按 Ed25519 的规矩签消息本身（`crypto.Hash(0)`），签完先用公钥验一遍，HSM 出错时在服务器上报错，不让手机收到坏签名。`keystore.CA.SM2` 仍然要 SM2 私钥本身：PKCS#11 没有标准的 SM2 机制。
- **换一把钥**（服务器一侧怎么做在服务器的仓库里）：
  1. 做出下一把钥，先不签名；
  2. 发一版镜像，信任点的文件里是两把：签名的那一把和下一把；
  3. 等手机都装上这一版（更新服务器收到的安装报告里看版本，[25](25-system-update.md) §0）；
  4. 让下一把开始签名：签名带新的钥号，装过第 2 步那一版的手机认，没装的不认；
  5. 再发一版镜像时文件里去掉旧的那一把，旧钥退役，它签过的东西从此不认。

  第 4 步之前还没装上第 2 步那一版的手机会断档：商店根换了，它验不过新的目录和新签的开发者证书；更新服务器的钥换了，它验不过 `latest.json`，只能刷机才能再收到更新；推送网关的证书换了，它连不上网关（更新还能收到）。所以第 3 步要等够。
- **商店根多一步**：装上的应用每次开机后第一次启动前都按信任的商店根重新验一遍，开发者证书是旧根签的应用，旧根退役以后就不能启动了。所以商店后台（`appstore`）启动时，凡是核验过的开发者的证书不是现在签名的那把钥签的，就用它重签一张（再 `phone-pkg register` 一次就拿到），开发者下一版的包就带着新证书；在旧根退役之前，商店也照收旧证书签的包（`pkg.Store.Trusted`）。旧根要等商店里和手机上都没有它的证书的包了才能从镜像里去掉。
- **测过的**：单元测试用旧钥和新钥各签一份，过渡期两把都认，旧钥退役后它签的不认：`keyset`，`pkg` 的 `TestStoreRootRotation`、`TestOldSystems`，`appstore` 的 `TestRootRotation`（开发者重签、旧证书的包过渡期收、退役后不收），`family` 和 `ota` 的 `TestRotation`、`TestOfferKeyRotation`，`push` 的 `TestPinRotation`（TLS 和 TLCP 真的握手），`shell` 的 `TestOfferKeyRotation`（`PHONE_UPDATE_KEY` 文件）。**未上真机**，还没有真的换过一次钥。
- **未实现**：认证方的根（`root.pub`：支付和银行后台、更新服务器、推送网关用它核对设备证书）还是一个 Ed25519 根加一个 SM2 根，设备证书也不带钥号，所以工厂 CA 换钥还要依赖方同时信新旧两个根；更新服务器不会按手机信哪几把钥挑签名的那一把，第 4 步之前没更新的手机只能刷机。

## 16. 和安卓各层的对应

安卓有一整套独立的栈。我们不一层层照搬，每一层都有一个更小的对应物：应用不跑虚拟机、界面是语义描述、只有一个渲染器、直接用主线内核的接口。这样省掉了几层，但也意味着有些安卓已有的东西我们要自己补。

| 安卓 | Kario 里对应的 | 状态 |
|------|---------------|------|
| Binder（进程间调用、调用方身份） | 每个系统服务一个 Unix 套接字上的 `wire` RPC，能传文件描述符（§14）；应用只连 Shell，能力代理按套接字认出是谁（像 Binder 的调用方 UID） | ✓ |
| ART / Dalvik、Zygote | 不需要：应用是原生进程（Go、C、C++、Rust、Python），小程序是 JS，跑在宿主替它起的 kario-jsrt 进程里，网页在 WPE 里；没有虚拟机就没有 Zygote，系统服务合成一个程序 `kario-system` 共用代码（[25](25-system-update.md) §3） | ✓（设计取舍） |
| SurfaceFlinger、HWC、Gralloc | 渲染器插槽（phone-paint）把 Shell 给的语义帧画到 DRM/KMS 上；视频帧经共享内存直通渲染器（[23](23-renderer-protocol.md)） | 软件渲染 ✓；GPU 合成可选（GLES 2，经 GBM 翻页上屏；在开发机的 llvmpipe 上和经 softpipe 在 QEMU 的 virtio-gpu 上测过，**未上真机**，[37](37-motion.md) §5，[42](42-gpu-rendering.md)）；dmabuf 上屏**未实现**（R21，要真机） |
| WindowManager、ActivityManager | Shell：一个前台应用、页面栈、系统界面和弹层、生命周期、后台冻结、内存不足时结束后台应用并留墓碑（回去时恢复原来的页面，[17](17-app-sandbox.md) §5.3） | ✓ |
| AudioFlinger、AudioPolicy | phone-audiod：共享内存环形缓冲区的流、音频焦点、类别、静音和振动（[20](20-audio.md)） | ✓；听筒 / 扬声器路由：一加 6T 上经 ALSA UCM（[20](20-audio.md) §10，**未上真机**）；蓝牙**未实现**（R29） |
| 权限模型、SELinux | 清单权限 + 运行时授权（能力代理）+ 沙箱：独立 uid、命名空间、Landlock、seccomp、每应用 cgroup、按应用防火墙（[17](17-app-sandbox.md)）；用 Landlock 代替 SELinux 的强制访问控制 | ✓ |
| HAL（HIDL / AIDL） | 不做通用的 HAL 层：直接用主线内核的接口（DRM、V4L2、ALSA、evdev、IIO、ModemManager），`hal/` 只是设备类上的一层薄封装，用哪一个由 SKU 配置；只给基带留了接厂商 Android BSP 的后端（libgbinder 讲无线接口 HAL） | 主线接口 ✓（QEMU）；厂商后端**未实现**（R2、R16） |
| PackageManager、安装器 | Shell 的安装器：签名的 `.phoneapp`、信任的商店根、吊销清单、每次开机后首次启动前重新验签（[07](07-ecosystem-and-store.md)） | ✓ |
| Intent、分享 | `intent.invoke`（调起别的应用做事并拿回结果，如付款）、分享面板、深链接（[22](22-developer-api.md) §4.9、§4.10） | ✓ |
| 应用生态（SDK、Play 商店） | 语义界面协议和 Go / C SDK、小程序、网页节点、商店（[22](22-developer-api.md)、[07](07-ecosystem-and-store.md)） | 平台 ✓；生态本身要靠时间和厂商，不兼容安卓应用是有意的 |
| 输入（InputFlinger、输入法框架） | evdev 按键经渲染器进 Shell，只到前台应用（§16.1）；输入法是独立进程 phone-imed（拼音、九宫格、笔画、联想，[16](16-input-method.md)） | ✓；触屏不是主要输入 |
| 显示 | DRM/KMS | QEMU 的 virtio-gpu ✓；真屏**未实现**（R16） |
| 传感器（SensorService） | `hal/sensor` 经 IIO 读环境光（`in_illuminance_input`，或 `raw` 乘 `scale`），Shell 用它做自动亮度、日光下关键盘背光（[28](28-battery.md) §1）；没有传感器的机器上是模拟的，自测用 `lux` 步骤给读数。通话时的距离传感器（贴脸息屏）、给应用的传感器接口**未实现** | 环境光：单元测试和自测 ✓，**未上真机** |
| 基带（RIL、Telephony 框架） | phone-telephonyd（C++23）加电话、短信应用；ModemManager 后端（[04](04-ui-language-telephonyd.md)），只管自己连的和上网用的承载，别的程序开的 IMS、紧急承载（VoLTE 的 81voltd）不当数据、不断开；可移植的核心、`Modem` 后端（按名字登记在 `backend.cpp`，板子的 `modem` 选）和每种 SoC 的胶水分开，契约见 [24](24-sku-framework.md) §8.8 | 模拟的 ModemManager 上 39 个用例 ✓（含后端契约）；一加 6T 的真基带上移动数据连上过，电话没打通，修了和其余的都**未上真机**（[04](04-ui-language-telephonyd.md)「真机」一条）；接厂商安卓 BSP 的后端**未实现**（R2）；联发科的三条路（ModemManager、经 libgbinder 的无线接口 HAL、MIPC）只是设计，**未实现**（[24](24-sku-framework.md) §8.9） |
| 短信（`InboundSmsHandler`、SmsManager、送达回执、class 0） | telephonyd 收发、要送达回执，收到的短信（含 8 位数据短信和消息类别）放进待确认队列，phone-smsd 存好确认后才放手（ModemManager 上这时才从基带删）；闪信由 Shell 弹系统对话框，显示过才放手（[32](32-messaging.md) §3、§12） | 模拟基带 ✓；ModemManager 一侧对着模拟的 ModemManager 测过，数据短信和类别**未验证**，**未上真机** |
| 彩信（MmsService、`WapPushOverSms`） | smsd 读 WAP 推送里的彩信通知，经彩信接入点从彩信中心取回、解析（`mms` 包），图片进相册；发出 m-send.req、收送达报告、群聊；接入点问 telephonyd，问不到用三家运营商的设置；运营商把彩信放在单独的接入点上（cmwap）时，每次收发前经 telephonyd 的 `mms.connect` 另连一条承载，Shell 只把从它的地址出去的连接路由过去（[32](32-messaging.md) §11） | **MVP**：对着假彩信中心 ✓；单独的彩信承载对着模拟的 ModemManager 测过；**未上真机** |
| TelephonyProvider（`mmssms.db`）、默认短信应用（`ROLE_SMS`）、BlockedNumberProvider | phone-smsd：只追加的日志加快照（不用数据库）、会话（按 E.164，`phonenum`）、已读未读、搜索、来电和短信共用的拦截名单（号码、号段、隐藏号码、通讯录以外）和拦截文件夹、验证码；删和拦截只给默认短信应用，这是个角色，设置 › 应用 › 默认应用里能换（[32](32-messaging.md)） | ✓ |
| CellBroadcastService、CellBroadcastReceiver | telephonyd 收小区广播（模拟网络的 `sim.cb`；ModemManager 1.24 的 `CellBroadcast`），smsd 去重、按频道分级、存，Shell 在锁屏上也弹全屏警报，audiod 的 `alert` 类别；国家级不能关；带警报区域的按最近的位置决定弹不弹，不知道在哪就弹（[32](32-messaging.md) §7） | 模拟基带 ✓；ModemManager 一侧对着模拟的 ModemManager 测过，**未上真机** |
| IMS（ImsService、VoLTE） | 骁龙基带自己跑 IMS；Kario 替它开 IMS 承载（81voltd 经 ModemManager，板子写了 `ims` 的 SKU），按 SIM 卡激活基带里这家运营商的配置（`phone-qcom-pdc`，只激活基带里已有的，不往基带装文件），telephonyd 不把 IMS 承载当移动数据；VoLTE 通话在 ModemManager 里是普通的通话，telephonyd、q6voiced、phone-audiod 照旧（[39](39-oneplus-6t.md) §11.1、[04](04-ui-language-telephonyd.md)） | 一加 6T 的镜像构建过，配置的选择对着模拟的 qmicli 测过；**未上真机**（[39](39-oneplus-6t.md) §14.6）；IMS 注册状态、VoLTE 开关、Wi-Fi 通话、视频通话**未实现** |
| 媒体编解码、相机 | phone-mediad：软件解码（WebM、MP3、Opus、Vorbis）、V4L2 采集、相册 | ✓；硬件编解码、libcamera **未实现**（R14、R24） |
| 推送（FCM） | pushd 和推送网关：一条长连接、端到端加密、合并唤醒（[21](21-push.md)）；协议和模拟网关开源，正式的推送服务不开源 | 设备侧和模拟网关 ✓；正式服务**未实现** |
| NFC（NfcService、HostApduService、默认付款应用） | phone-nfcd：`hal/nfc` 上的控制器（模拟的，和 NCI 后端），按刷卡时间选卡、刷不开就换卡，息屏锁着照样刷；系统应用「卡包」管卡，Shell 只有双击电源键的快速选卡；控制器按主板配置（`sku.json` 的 `nfc`）选；应用经 `nfc.apdu` / `nfc.reply` 答读卡器（[30](30-nfc.md)） | 模拟控制器 ✓；NCI 后端上真机、安全芯片里的卡**未实现** |
| Keystore、StrongBox、Key Attestation | phone-keystored：应用密钥（Ed25519 / SM2）、设备认证、可信确认、推送密钥；产线灌装设备证书（[22](22-developer-api.md) §11） | ✓；放进 TEE **未实现**（R15） |
| 锁屏（Keyguard、Gatekeeper、指纹 HAL） | Shell 的锁屏（息屏即锁、紧急呼叫、隐藏通知内容、锁屏快捷键）；phone-keystored 校验锁屏 PIN 码、保存指纹模板、出认证令牌；`hal/fingerprint`（[31](31-lock-and-biometrics.md)） | 模拟传感器 ✓；TEE、真机传感器**未实现**（R15） |
| 系统更新（A/B、update_engine） | RAUC 的 A/B 槽、dm-verity、自适应增量、分批推送、回报（[25](25-system-update.md)） | QEMU ✓；真机引导**未实现**（R16） |
| 电源管理（Doze、唤醒锁） | 后台冻结（cgroup 冻结器）、按预算唤醒、后台任务合并、推送顺带跑任务；息屏后整机挂起，RTC 按闹钟和任务截止叫醒（Shell 自己掌握全部「唤醒锁」：通话、放音、更新、USB 上的电脑、被唤醒的应用，[28](28-battery.md) §3） | ✓；挂起在开发机和模拟器上模拟（模拟器真挂起后叫不醒：它的 RTC 不给挂起到空闲设唤醒中断）；真机上的挂起、唤醒源和电流**未验证**（要真机） |
| 挂起（SystemSuspend、Power HAL） | `hal/pm`：经 `/sys/power/state` 挂起到内存（没有就挂起到空闲）；先读 `wakeup_count`、挂起时写回，其间来了唤醒事件内核就拒绝挂起；RTC 唤醒闹钟设在下一个闹钟或任务前（写成从现在起的秒数 `+N`：RTC 的时钟不一定是系统的，一加 6T 的 RTC 在主线内核上只能读、从 1970 数起，写绝对时间时闹钟落在 57 年后，手机挂起后醒不过来）；有没有能叫醒系统的按键看输入设备的 `power/wakeup`（[28](28-battery.md) §3） | 单元测试 ✓，开发机和模拟器上模拟；真机**未验证**；安卓的 SystemSuspend 用的也是 `wakeup_count`，厂商内核上同样有 |
| 电池（BatteryService、Health HAL） | `hal/power`：电源类（`/sys/class/power_supply`）的电量、在不在充电、充电器是不是 USB PD 快充（[27](27-usb-typec.md) §1） | ✓（模拟）；真机**未验证**；安卓的 Health HAL 读的也是电源类，厂商内核上同样有 |
| 灯（Lights HAL：通知灯、背光） | `hal/led`（LED 类：定时器触发的慢闪、多色灯的各通道强度）、`hal/backlight`（背光类；LED 类的键盘背光，和高通厂商内核做成 LED 类 `lcd-backlight` 的屏幕背光）；用哪个灯、哪个背光由主板配置给（下文「那 `hal/` 是什么」） | ✓（QEMU 上用 uleds 造一个灯）；真机**未验证**；`lcd-backlight` 只按名字认出，厂商内核上**未上真机**（[24](24-sku-framework.md) §7） |
| 振动（Vibrator HAL） | `hal/vibrator`：安卓内核 LED 类的振动器 `/sys/class/leds/vibrator`（先写时长再激活），或主线内核力反馈（`FF_RUMBLE`）的输入设备；静音模式下来电、闹钟响时每 1.5 秒振一次（[20](20-audio.md) §4.1） | 写法、查找 ✓（单元测试；模拟器上只计数）；**未上真机** |
| USB 设备模式（UsbDeviceManager、USB gadget HAL） | `hal/usbgadget`：configfs 里一个 CDC-NCM 网卡，电脑被信任后经它共享文件（[27](27-usb-typec.md) §3） | QEMU ✓；真机**未验证**；3.10 一代的安卓内核用安卓自己的 `android_usb`，不是 configfs（**未核实**，[24](24-sku-framework.md) §7） |
| MTP（MtpServer、MtpDatabase，在 MediaProvider 进程里经 FunctionFS） | `phone-usbshared` 的 MTP 模式（`usbshare/mtp`）：Shell 在 configfs 里建 FunctionFS 函数、写描述符、打开端点，把这些文件交给沙箱里的助手；四个存储 Photos、Videos、Music、Files 走网络共享那同一个库和检查，电脑能读能加，不能删改；设置 › USB 在它和网络共享之间选（[27](27-usb-typec.md) §3.3） | 单元测试、开发机自测、模拟器 ✓；真电脑**未上真机**（Windows、macOS、Linux 都没试过）；缩略图、SendObjectPropList、PTP（相机模式）**未实现** |
| 音频 HAL（`audio.primary`、tinyalsa） | 不经 `hal/`：phone-audiod 直接用 ALSA，现在经 `aplay` 写 `plughw`（自动模式下插上 USB 声卡就换过去）；通话的声音在真机上由基带和声卡直连（[20](20-audio.md) §4、§7） | 开发机和模拟器 ✓；听筒、扬声器的切换（ALSA UCM）一加 6T 上有（[20](20-audio.md) §10，**未上真机**），耳机**未实现**；厂商内核上要经 libhybris 加载 `audio.primary.*.so`（[24](24-sku-framework.md) §7.3） |
| 位置（LocationManager、GNSS HAL、FusedLocationProvider） | `location/`：NMEA 0183 解析器和接收机——内核 GNSS 子系统的 `/dev/gnssN` 或串口、ModemManager 的 Location 接口（连同 A-GNSS：SUPL 服务器、预测星历注入）、命名管道（QEMU）、模拟接收机，按 SKU 的 `gnss` 选；网络定位（`location.Locator`：Wi-Fi 接入点和基站经可配置的 Geolocation API 服务，结果存在手机上、限次，隐藏和 `_nomap` 网络不发）；大致位置的 2 公里格子（`location.Coarse`）。Shell 只在有人要定位时打开接收机，卫星定位不到时才问网络。`location.get`、`location.watch`：给前台应用，或用户在设置里「始终允许」的应用（后台定位，定期提醒）；授权框可选精确或大致位置；状态栏提示，总开关、网络定位开关、最近访问（含后台）、精度和「始终允许」在「设置 › 位置」（[22](22-developer-api.md) §4.7） | 模拟接收机和 QEMU 的管道 ✓（NMEA 走真的解析器）；网络定位走模拟的服务 ✓；A-GNSS 用模拟的 ModemManager 测过 ✓；真机的 GNSS、A-GNSS、iwd 的接入点和 ModemManager 的基站**未上真机**（R25），真的 SUPL / XTRA 服务器和网络定位服务**未验证** |
| Wi-Fi、蓝牙管理 | Shell 经系统总线管 iwd 和 BlueZ，设置里有 Wi-Fi、蓝牙页（`wifi.*`、`bt.*`）：连加密网络时密码只交给 iwd；企业网络（802.1X：PEAP 内层 MSCHAPv2，TTLS 内层 MSCHAPv2 或 PAP）要填用户名、密码和服务器的域名，Shell 写成 iwd 的配置（服务器证书按系统的根证书和这个域名核对，外层身份只给匿名加域），不填域名不连（照安卓 11 起的做法：否则任何服务器都能骗走密码）；和有屏幕的设备配对时用户在系统对话框里核对配对码；和键盘配对时系统的界面显示要在键盘上输入的配对码（BlueZ 的 `DisplayPasskey`，2.1 以前的键盘是 `DisplayPinCode`），标出键盘上已打的位数，配对结束时自己关上，按返回取消配对（`Device1.CancelPairing`）；对方要手机这边输入 PIN 或 passkey 时（`RequestPinCode`、`RequestPasskey`）在系统的认证码界面输入，码不写进日志；都只对用户选了要配对的那台设备（代理照旧注册为 `DisplayYesNo`，这时 BlueZ 不会要手机输入 passkey）；蓝牙传文件和电话本由 Shell 自己讲 OBEX（`obex` 包；BlueZ 的外部 profile 把 RFCOMM / L2CAP 连接交给它，不装 obexd：它要的 libical 带进 39 MB 的 ICU，实测见 [13](13-prototype.md) §5）：已配对设备发来的文件每个都问用户，收进「文件」；设置里把「文件」里的文件发给已配对的设备（`bt.send`）；车机读通讯录和通话记录（PBAP 服务端）前问用户一次，按设备记住，设置里能收回；从旧手机迁移时经蓝牙读它的电话本、通话记录（PBAP 客户端）和短信（MAP 客户端，[36](36-device-switch.md) §8）；开发机上是模拟的网络和设备；有线网仍由 systemd-networkd 管 | ✓（开发机模拟）；模拟器上用模拟射频（mac80211_hwsim、btvirt）跑同一组用例：企业 Wi-Fi 的接入点是 hostapd 自带的 EAP 服务器（第三个模拟射频，证书在测试时生成；它的 MSCHAPv2 要的 MD4、DES 来自只装在模拟器镜像里的 OpenSSL legacy 模块；它不发地址，测试网络用静态 IPv4），蓝牙传文件和电话本的对方是 btvirt 的第二个控制器，这两部分在模拟器上通过（虚拟控制器只连得到虚拟的：Shell 选手机的适配器时先选手机自己的控制器，要这个对端的用例在手机自己的控制器旁边跳过，[39](39-oneplus-6t.md) §16.10）；OBEX 只和自己的实现互通过，和别家手机、车机**未验证**（**未上真机**）；L2CAP 上用 GOEP 2.0 的单响应模式（也只和自己的实现互通过）；OBEX 认证（IrOBEX 1.2 §3.5）：迁移时回答旧手机的挑战、车机读电话本时回答车机的挑战、发文件（`bt.send`）时回答收文件的设备的挑战（只回答，不反过来挑战它），码由用户在系统的认证码界面输入（[36](36-device-switch.md) §8.1），也只和自己的实现互通过（**未上真机**）；和键盘配对（显示配对码，输入 PIN 或 passkey）有单元测试（`shell/btpasskey_test.go`）和开发机上模拟的键盘（自测「蓝牙键盘配对」）；btvirt 的控制器当不了键盘，模拟器上没有这个用例，和真的键盘**未上真机**；蓝牙耳机的声音（A2DP、HFP，要 PipeWire）**未实现** |
| 计费网络（`ConnectivityManager` 的 `NET_CAPABILITY_NOT_METERED`、Wi-Fi 网络的「按流量计费」、`JobScheduler` 的 `NETWORK_TYPE_UNMETERED`） | Wi-Fi 网络默认自动判断是否计费：Shell 连上后自己在包套接字上发一个续租的 DHCPREQUEST，回答的选项 43 里有安卓热点的 `ANDROID_METERED` 就算计费（iwd 是 DHCP 客户端，不交出厂商选项）；地址在 iPhone 热点的 172.20.10.0/28 或 192.168.43.0/24 里的也算；用户可以在「设置 › Wi-Fi」里给每个网络选是或否（`wifi.set` 的 `metered`），存在 Shell 的设置里；移动数据总算计费。自动的系统更新、商店的自动更新和带 `unmetered` 的后台任务只在不计费的 Wi-Fi 上跑，用户自己开始的下载不管（`metered` 包、`shell/metered.go`，[25](25-system-update.md) §6.1）；跑到一半手机离开了不计费的 Wi-Fi 就停：系统更新自己停下（[25](25-system-update.md) §6），后台任务由系统停掉（应用收到 `job.stop`，5 秒后这次唤醒结束，回到不计费的 Wi-Fi 后重跑，[22](22-developer-api.md) §4.7），商店收到就断开后台的下载 | 宿主机单元测试和自测 ✓（模拟的热点，「后台任务遇到计费网络」在设置里把网络改成计费）；问 DHCP 服务器**未上真机**，没对着安卓热点试过；Kario 自己的热点不发 `ANDROID_METERED`（iwd 的 DHCP 服务器不支持）；`charging` 的任务跑着时拔掉电源也由系统停掉（`job.stop` 的原因 `unplugged`，插上电源后重跑），宿主机单元测试和自测「后台任务拔掉电源」✓，**未上真机** |
| 流量统计与流量套餐（`NetworkStatsService`、`NetworkPolicyManager` 的周期、提醒和上限） | Shell 按天、按应用、按网络（Wi-Fi、移动数据、国外漫游）累加，留到上个周期的第一天（`shell/datausage.go`，[19](19-core-apps.md) §1.3）：应用的用量来自防火墙按 uid 的计数器（安卓的 netd 也按 uid 计），总量来自网卡自己的计数器（`wl` 开头的算 Wi-Fi，telephonyd 报的网卡算移动数据），两者之差记作系统；内核没有 nftables 的具名计数器（Linux 4.10）时只有网卡的总量；热点上的设备按接入点网卡的计数器另记一行。流量套餐：每月流量、起算日、提醒（默认套餐的 80%，每个周期一次）、用完时挡住移动数据（不动开关，再打开先确认，到周期结束或调高套餐前不再挡）；热点有设备每天的上限。每 30 秒看一次计数器 | 单元测试和开发机自测 ✓（计数是记上去的）；QEMU 上的用例 ✓（2026-11-27 夜间链 r86；热点每天的上限的用例只在开发机的模拟热点上跑）；真的基带上的计数**未上真机**；在内核里按字节挡的配额**未实现**；双卡分开计**未实现**（电话的模型只有一张 SIM 卡） |
| 后台数据、流量节省和应用自己的流量上限（`NetworkPolicyManager` 按 uid 限制后台数据、Data Saver 和它的白名单，`ConnectivityManager.getRestrictBackgroundStatus`） | 应用的后台数据关掉（或开着流量节省、它不在不受限制的应用里）时，只在屏幕上或系统给的后台模式让它跑着时能用移动数据；应用自己的上限用满后到周期结束或调高上限为止都挡（`shell/netpolicy.go`，[19](19-core-apps.md) §1.3）。设备上是按 uid 的防火墙规则（安卓是 netd 按 uid 的规则），连接马上得到 EPERM；内核没有按应用防火墙的老手机和开发机只有 Shell 的策略。应用经 `net.state` 和同名事件知道（安卓的 `getRestrictBackgroundStatus`、网络被挡的状态），要网络的后台任务（`job.schedule` 的 `network`，安卓 JobScheduler 的网络条件）等 Wi-Fi | 单元测试和开发机自测 ✓（挡不挡是 Shell 的策略）；QEMU 上经防火墙的用例 ✓（2026-11-27 夜间链 r86）；**未上真机**；按流量计费的 Wi-Fi 上也挡、流量节省的状态栏图标、内核里按应用的配额**未实现** |
| 个人热点 | Shell 让 iwd 把手机的 Wi-Fi 变成 WPA2 接入点（`hotspot.*`，[22](22-developer-api.md) §4.5）：手机在热点上的地址是 192.168.43.1/24（手机连着的网络占了这一段就顺延到 .44、.45……），iwd 自己的 DHCP 服务器发地址、DNS 服务器，并把手机作为网关发给客户端（iwd 3.12 的接入点配置里不写网关就不发，客户端有地址却出不去），Wi-Fi 网卡的地址只归 iwd 管：镜像里的 `05-kario-wlan.network` 让 systemd-networkd 不碰 Wi-Fi 网卡，系统里有匹配所有网卡的 `.network` 时也一样（QEMU 的 `ip=dhcp` 会让 systemd-network-generator 写一个，networkd 就会在链路一变时把 iwd 配的地址当外来的删掉，客户端连得上热点却 ping 不通手机），客户端的流量经 NAT（nftables 伪装，[17](17-app-sandbox.md) §1）从默认路由的网卡出去，只给这两块网卡开 IPv4 转发，默认路由变了跟着换；只有一个射频，开着时 Wi-Fi 关着，关掉后恢复；交给 iwd 的是 PSK（iwd 的接入点配置读完就删，不落盘），密码只存在 Shell 的设置里，不写日志；状态栏显示热点图标。设置里列出连着的设备：MAC 和连上的时间问内核（nl80211 的站点列表），名称和地址由 Shell 在接入点网卡上用包套接字读 DHCP 请求里的主机名和服务器确认的地址（iwd 的 DHCP 服务器不交出租约；内核里的过滤器只放 DHCP 进来）；按接入点网卡的计数器记本次和每天的流量（网卡收到的是设备的上传，发出的是下载），存在 Shell 的状态里，留一个月；设备数上限由 Shell 守（iwd 没有这一项）：超出的新设备一连上（nl80211 的 mlme 事件，收不到时每秒查一次）就用 iwd 自己请走设备的同一个请求（`DEL_STATION`，解除关联，原因码 5「接入点容不下更多设备」）请走，iwd 跟着这个事件忘掉它和它的租约，已连着的设备不受影响；5 GHz 写进接入点配置的 `Channel`（iwd 把 36 起的频道当 5 GHz，先试 36–48，再 149–161），射频和所在国家允许时才给选（nl80211 报的频道没被禁用、不是 no-IR、不是雷达频道）；iwd 3.12 的接入点只做 WPA2-Personal（只认 PSK，没有 SAE，所以没有 WPA3，也没有 WPA2/WPA3 过渡模式），也不能隐藏网络名称（hidden SSID 写死为不用），设置里如实写明、不给选；模拟的热点做得到全部选项（只认 WPA2 的设备连不上只用 WPA3 的热点、没有 5 GHz 的设备连不上 5 GHz 的、满了以后再来的被拒绝、隐藏后扫不到） | ✓（开发机模拟）；模拟器上的用例已备好（iwd 把 wlan0 变成接入点，空闲的第三个模拟射频移进自己的网络命名空间，wpa_supplicant 连上、udhcpc 拿地址、ping 通手机和 NAT 后面的网关），模拟器上 ✓；**未上真机**；移动网卡的地址由 Shell 经 systemd-networkd 配置（[04](04-ui-language-telephonyd.md)），真机上还没试过，所以热点经移动数据分享**未上真机**；流量统计、设备列表、设备数上限和选频段：开发机的模拟热点上 ✓（单元测试、自测），模拟器上的用例另查设备列表里报的名称和记下的流量（模拟器上 ✓）；设备数上限在模拟器上测不到（只有一个空闲的模拟射频），5 GHz 在模拟器上多半不给选（没设国家时是 no-IR），**未上真机**；WPA3（含过渡模式）和隐藏网络在 iwd 上**未实现**（iwd 3.12 不支持；要做得换成 hostapd，它现在只在模拟器镜像里）；Shell 不按 SIM 卡设 Wi-Fi 的国家，所以手机上 5 GHz 多半选不了，**未实现**；设备每天的流量上限（用到 80% 提醒、用满关掉热点，[19](19-core-apps.md) §1.3）在开发机的模拟热点上 ✓，模拟器上没测 |
| 飞行模式、移动数据、数据漫游、接入点 | 设置首页和电源菜单里的开关（`radio.*`）：飞行模式让 telephonyd 关掉基带的射频（ModemManager：`Modem.Enable(false)` 后 `SetPowerState` 到低功耗），同时关掉 Wi-Fi 和蓝牙、记住用户原来的开关，结束时恢复；飞行模式下用户可以再打开 Wi-Fi、蓝牙，下次飞行模式就留着它们（照安卓、iOS）；拨紧急号码时 telephonyd 先给射频上电再拨，Shell 随之退出飞行模式（照安卓，方便对方回拨）；移动数据关掉时 telephonyd 断开数据承载（`Simple.Disconnect` 只断它，IMS 承载不动），防火墙挡住它报的移动网卡（进、出和转发）；开着时用 SIM 卡运营商的接入点连上（`Simple.Connect` 的 `apn`、`ip-type`、`apn-type`、`allowed-auth`、`user`、`password`、`allow-roaming`）：接入点数据库是 telephonyd 的 `apns.json`，有中国移动、联通、电信、广电的上网、彩信和 IMS 接入点，按 SIM 卡的 MCC、MNC 选，虚拟运营商按 SPN 或 GID1 区分；「设置 › 移动网络 › 接入点名称」查看、添加、编辑、删除、选用、恢复默认（`apn.list`、`apn.set`，[04](04-ui-language-telephonyd.md)）；LTE 初始附着的接入点（`ia`）和基带的不同时经 `Modem3gpp.SetInitialEpsBearerSettings` 交给它（通话中不设）；Shell 按承载报的地址（静态的，或 IPv4 用 DHCP、IPv6 听路由通告）给移动网卡写 systemd-networkd 的运行时配置，DNS 交给 systemd-resolved 管这块网卡，路由排在 Wi-Fi 和有线后面，数据断开时拆掉；数据漫游开关默认关，国际漫游时不连数据承载，打开时先提醒漫游费用，国际漫游时状态栏显示 R；漫游按注册状态判断，在 SIM 卡本国别的网络上（国内漫游，MCC 相同）归「国内漫游」开关管（默认开：照常上网、不显示 R）；漫游开关让移动数据断开时防火墙也挡住移动网卡，并以「设置」的名义通知、写明网络和要打开的开关；改了使用中的接入点（只改 IP 类型、认证也算）时断开重连；开关重启后保持，telephonyd 自己也记一份，开机时 Shell 还没起来基带也按原样 | 模拟基带 ✓（开发机自测：接入点、编辑接入点、数据漫游关着和打开、国内漫游关着、漫游的通知，telephonyd 单元测试）；ModemManager 后端对着模拟的 ModemManager 的用例通过（`make mm-selftest`，在容器里，含接入点、改接入点（只改 IP 类型、认证也重连）、数据漫游和国内漫游）；networkd 配置的单元测试 ✓，模拟器上的用例（veth 顶替模拟基带的网卡）模拟器上 ✓；一加 6T 上中国移动的卡连上过移动数据（[39](39-oneplus-6t.md) §16.10），飞行模式、数据漫游、改接入点**未上真机**（接入点的值来自 AOSP、LineageOS 的表，**未在真网上核对**）；防火墙的设备用例在模拟器上通过，漫游时挡住移动网卡的设备用例 QEMU ✓（2026-11-27 夜间链 r86）；初始附着的接入点、给彩信接入点另连的承载和不碰 IMS 承载对着模拟的 ModemManager 测过，**未上真机**；漫游时另用一种 IP 类型**未实现** |
| VPN（`VpnService`、`ConnectivityService` 里的 VPN、设置 › VPN） | Shell 的 VPN（`vpn.*`，设置 › VPN）：WireGuard 的配置是 wg-quick 的格式，从系统的文件列表导入「文件」里的 `.conf`（键盘上打不了密钥，所以主要是经 USB 从电脑拷来），逐项严格检查，不运行配置里的脚本；也可以在手机上生成密钥对、只显示公钥给服务器的管理员，私钥只在 Shell 的状态里（0600），日志里只有配置名。连接时 Shell 往 `/run/systemd/network` 写一个 WireGuard 的 `.netdev` 和它的 `.network`（密钥在单独的文件里，只给 systemd-network 组读），经 D-Bus 让 systemd-networkd 重新载入，等网卡配置好；全隧道（AllowedIPs 有 `0.0.0.0/0` 或 `::/0`）照 wg-quick：AllowedIPs 的路由放进自己的路由表 51820，没带隧道防火墙标记的包都查这张表，隧道自己的包带标记、照常从底下的网络出去，主表先查（去掉默认路由），局域网照样通；分隧道把 AllowedIPs 加进主表。配置的 DNS 交给 systemd-resolved 管隧道的网卡，连着时所有名字都问它（和 wg-quick 一样）；镜像里 `/etc/resolv.conf` 指向 resolved 的本地存根 127.0.0.53，所以按网卡分派对每个程序都成立，名字查询不会绕过隧道。断开时删掉文件、删掉网卡（networkd 不删）和两条策略路由规则；Shell 起来时没有生效的配置、却有隧道（网卡、文件或这两条规则）留着，也这样拆掉（失败的自测用例留下的，[39](39-oneplus-6t.md) §16.10）。握手时间和收发字节经 netlink 问内核的 WireGuard 和网卡的计数器；同时只连一个；状态栏有钥匙图标。始终开启（`vpn.set`，设置 › VPN 的配置详情里）：一个配置开着就一直连着，Shell 启动时就连，隧道断了、连接失败（过 2 秒、5 秒、15 秒、30 秒，之后每分钟再试）、底下换了网络（隧道自己的包换了出去的网卡，或断网后又有了）时重连；有它时别的配置连不上，用户断开它就关掉始终开启。屏蔽未使用 VPN 的连接（照安卓，只在始终开启时有）：防火墙里一条输出链，隧道连着断着都在，只放隧道、VPN 服务器、DHCP、IPv6 邻居发现和只通到身边设备的网卡（[17](17-app-sandbox.md) §1）；服务器写的是域名时，屏蔽开着由 Shell 自己问上游 DNS 服务器解析，把地址交给 networkd（networkd 和 systemd-resolved 的查询会被挡住）。按应用走或不走 VPN（全部应用、只有选中的应用、除选中的应用外的全部）：防火墙按应用的 uid 给包打上隧道的防火墙标记，带标记的包不查隧道的路由表；分隧道这时把 AllowedIPs 的路由也放进隧道自己的表（同一条规则，查不到的回主表），Shell 重建隧道；「只有选中的应用」时 systemd-resolved 也走隧道，所有应用的名字都问 VPN 的 DNS；不走 VPN 的应用不受屏蔽（和安卓一样）。和按应用防火墙怎么配合见 [17](17-app-sandbox.md) §1 | 开发机上模拟的隧道 ✓；设备上的后端（内核配置 `phone-vpn.cfg`，内核配置里没有 WireGuard 时内核配方的构建停下）在 QEMU 两个镜像上的用例 ✓；始终开启、屏蔽和按应用：单元测试、开发机自测 ✓（模拟的隧道，应用的包去哪儿按 Shell 的策略判断，Shell 重启后自己连上），模拟器上的两个用例（真的 networkd、nftables 和网络命名空间里的服务器：删掉隧道的网卡后连回来，商店的包进隧道或绕开，屏蔽时 EPERM）模拟器上 ✓；屏蔽下按域名解析服务器**未验证**；**未上真机**；给应用的 VPN 接口（`VpnService`）、IKEv2/IPsec **未实现** |

所以「对应的实现」不是一层层都做一份：虚拟机、Zygote、通用 HAL 这几层是有意不要的；窗口、权限、包管理、调起、推送、密钥、更新、Wi-Fi 和蓝牙管理这些都已经有了；剩下的缺口是传感器、GPU 合成在真机上的验证（代码有了）、真机上的电源策略和厂商后端，都要真机才能做。

**为什么不做 HAL。** 安卓的 HAL 是为了让一个系统版本跑在几千种厂商内核和驱动上：厂商在 HAL 后面放自己的闭源实现，系统升级不用等厂商。我们只支持主线内核的硬件（R16 选型的前提），内核已经把硬件抽象成了标准接口，再加一层没有东西可以放进去。以按键为例，投递的路是：键盘芯片的驱动（矩阵键盘 TCA8418、GPIO 按键）按设备树里的 `linux,keymap` 报标准的 `KEY_*` 码 → evdev → 渲染器（只有它能读 `/dev/input`，应用读不到）→ 渲染器协议的 `key` 消息 → Shell。机型之间的差别在 SKU 的键盘配置里（`sku/keypads/*.toml`、`keys.toml` 列出每个键的码，测试核对渲染器认得每一个，[24](24-sku-framework.md)），不同厂商的扫描码由设备树（或 udev hwdb 写进内核的键码表）改成标准码，不需要用户态的 HAL。其他硬件同理，每类硬件有一个系统服务管：显示和按键归渲染器（DRM、evdev），声音归 audiod（ALSA），相机归 mediad（V4L2），基带归 telephonyd（ModemManager），网络归 iwd / BlueZ，以后的传感器归一个读 IIO 的服务。需要接厂商 Android BSP 的只有基带这类没有主线驱动的东西，做法是在那个服务里加一个后端（telephonyd 用 libgbinder 讲无线接口 HAL，**未实现**，R2），而不是在整个系统下面垫一层。

**那 `hal/` 是什么。** 设备类（LED、背光、电源、USB 设备模式）的读写散在各服务里会各写一遍 sysfs，也没法在开发机上模拟，所以收成一层薄封装：`hal/led`（LED 类：定时器触发的慢闪、多色 LED 的各通道强度；QEMU 上用 uleds 造一个）、`hal/backlight`（背光类，和当背光用的 LED：键盘背光、厂商内核的 `lcd-backlight`）、`hal/vibrator`（振动马达：安卓内核的 LED 类，主线内核的输入设备力反馈）、`hal/power`（电源类：电量、充电器是不是 USB PD 快充）、`hal/usbgadget`（USB 设备模式：configfs 里的 CDC-NCM 网卡）、`hal/pm`（整机挂起：`/sys/power/state`、`wakeup_count` 握手、RTC 唤醒，[28](28-battery.md) §3）、`hal/rtc`（实时时钟：读、写、只读的 RTC 上系统时间的偏移，开机按它设时钟；时钟归不归这个系统，[28](28-battery.md) §6）、`hal/nfc`（NFC 控制器：模拟的和 NCI 后端；NCI 后端讲安卓厂商驱动 `nxpnfc`、`pn5xx_i2c` 给的原始 NCI 设备节点，主线的 `nxp-nci` 把 NCI 留在内核里、不给用户态做卡模拟，[30](30-nfc.md)）、`hal/fingerprint`（指纹传感器：读取、录入、比对；只在 phone-keystored 里用，开发机和 QEMU 上是模拟的；手机上的 FPC、汇顶传感器没有主线驱动，要厂商的 TEE，**未实现**，[31](31-lock-and-biometrics.md) §4.1）。它不定义新接口，只是内核设备类的 Go 写法（`hal/fingerprint`、`hal/nfc` 例外：内核没有能直接用的设备类，接口是自己定的），每类有一个模拟后端；机型之间的差别只有「用哪一个」：主板配置写名字（`sku/boards/*.toml` 的 `[leds]`、`backlight`、`keyboard_backlight`），经 `sku.json` 交给 Shell，同一份二进制驱动每块板（[24](24-sku-framework.md)）。比如通知灯：F(x)tec Pro1 X 是 PMIC 上的 RGB 灯 `rgb:status`（驱动自己会闪，慢闪不唤醒 CPU），一加 N100 的主线设备树里没有灯，就不配，新通知改为点亮屏幕。按键不经过它：渲染器直接读 evdev，上面说的路不变。

**在只有厂商内核的手机上。** 上面这些后端都对着主线内核的设备类。要在 KaiOS 这类只有厂商内核的手机上用安卓的 HAL（libhybris 加载链接 bionic 的厂商库，或者 libgbinder 直接讲 HIDL），后端分别加在：`hal/` 的各类（灯、背光、电量在厂商内核上多半还是 sysfs 的设备类，直接能用；要加载厂商库的，放进一个单独降权的 HAL 宿主进程，`hal/` 的后端是它的客户端，厂商代码不进 Shell）、telephonyd（实现 `Modem` 接口的 RIL 后端，讲 rild 套接字或 HIDL 的 `IRadio`）、渲染器（帧缓冲输出，或经宿主进程走 hwcomposer）、audiod（一个经宿主进程调音频 HAL 的输出）。按键不用改：厂商内核也报 evdev。NFC 的 NCI 后端本来就对着厂商驱动的原始设备节点，厂商内核上不用另写（**未上真机**）。我们不推荐这条路，理由和逐项的代价见 [24](24-sku-framework.md) §7。

### 16.1 按键怎么到应用（和安卓对照）

安卓：InputReader 从 evdev 读事件，InputDispatcher 只投给有焦点的窗口；后台的、被冻结的应用没有焦点窗口，收不到键。应用刚启动、还没有窗口时（有焦点应用、没有焦点窗口），键在 InputDispatcher 的队列里等，最多等这个应用的分发超时（默认 5 秒），到时报「应用无响应」，键丢掉；键按顺序一个一个投，前一个事件弹出的新窗口收到下一个键（[InputDispatcher.cpp](https://android.googlesource.com/platform/frameworks/native/+/refs/heads/main/services/inputflinger/dispatcher/InputDispatcher.cpp) 的 `findFocusedWindowTargetLocked`）。KaiOS 的 Gecko 里带着同一份代码（[24](24-sku-framework.md) §7.1）。

| 步骤 | 安卓 | Kario |
|------|------|-------|
| 读设备 | system_server 里的 InputReader 读 `/dev/input` | 渲染器读：phone-paint 降权前分出的输入助手打开 `/dev/input` 下的键盘、鼠标，把 fd 交给它（[23](23-renderer-protocol.md) §5）；应用打不开 `/dev/input`（[17](17-app-sandbox.md) §6） |
| 谁决定 | InputDispatcher 和窗口管理的策略 | Shell：渲染器把键原样经渲染器协议的 `key` 消息报上来（[23](23-renderer-protocol.md) §3），自己不解释 |
| 系统先拿 | 电源键、音量键这些由 `PhoneWindowManager` 先处理 | Shell 的 `Key`：屏幕关着时这一下只点亮屏幕；音量键、媒体键任何时候都归系统；系统浮层（菜单、对话框、来电、闹钟）在最上面时键只给它；键盘锁着、锁屏通话中只到锁屏或通话界面；挂断、通话、任务键由 Shell 解释；电源键另走 `PowerKey` |
| 给哪个应用 | 有焦点的窗口 | 「当前」应用：前台应用，没有前台应用时是 Shell 自己的待机和菜单（`current()`）。后台的、冻结的、墓碑里的应用都收不到 |
| 应用收到什么 | 原始的 `KeyEvent` | 一般不是键：焦点移动、返回栈、输入框里的字都在 Shell 里处理，应用收到的是动作（哪个节点的哪个动作）和输入框的 `changed`；只有声明了要键的画布节点收到键名，网页节点的方向键和 OK 交给网页进程（[22](22-developer-api.md) §3） |
| 应用还没准备好 | 在队列里等，最多 5 秒，之后报「应用无响应」并丢掉 | 存着，见下 |

**存着的键**（`shell/tomb.go`、`shell/core.go`）：
- 什么时候存：当前应用还没有能用的页面时，也就是冷启动后第一页还没到（页面栈是空的），或者从墓碑恢复、旧页面只显示（它们的动作属于已经结束的进程，[17](17-app-sandbox.md) §5.3）。这时的键（上面系统先拿的那几步之后剩下的）存进这个应用的 `heldKeys`，至多 32 个（`maxHeldKeys`），再多的丢掉；返回键不存，直接回待机。Shell 自己的待机、菜单一直有页面，不存。
- 什么时候交：应用每交一页（`push`、`replace`、`reset`）都重新计时；最后一页到了 150 ms（`keysSettle`）以后，应用还在运行、页面不再是旧的，就把存着的键按按下的顺序重新走一遍 `Key`，和刚按下的键是同一条路。这期间弹出的浮层照常先收到它们（命令面板接着收打的字），和安卓「前一个事件弹出的窗口收到下一个键」是一个意思；但要用户做决定的系统框不收：它出现以前按的键不算回答（见下面「对话框只认看见以后的键」）。
- 什么时候扔：回放时这个应用已经不在前台（用户回了待机、切到别的应用），存着的键全部扔掉，不交给别的应用；进程退出（崩溃、被结束）时清空。离开前台时并不清空：新页面到之前又切回来，之前存的键回放时照样交给它。
- 没有超时：新进程一直不交页面，键就一直存着；存键时 Shell 不发 ping，也就不报「没有响应」（[17](17-app-sandbox.md) §5.2），用户按返回回待机。这一点和安卓不同。
- 解冻不用存键：切到前台时 Shell 就解冻它（写 `cgroup.freeze` 或发 SIGCONT，`shell/freeze.go`）；页面一直在 Shell 手里，焦点移动、打字马上生效，要交给应用的动作排进它的发送队列（每个应用 256 条，满了丢掉）和套接字缓冲，应用恢复运行后按顺序处理。

**一次一个动作**（`shell/input.go`）：安卓的 InputDispatcher 等窗口处理完一个事件才投下一个。Kario 的焦点和返回栈在 Shell 里，键本来不用等应用；但动作的结果是应用发来的：选了一项之后马上按返回，Shell 先退了页，应用晚到的「替换最上面一页」就换掉了退回去的那一页（模拟器上设置里的键盘锁就这样出过错）。所以动作交给前台应用以后，Shell 跟一个 ping，回 pong 之前这个应用的键先存着（`input.held`，至多 32 个），pong 到了按顺序回放；应用按顺序处理消息，pong 晚于这个动作的处理（kario-jsrt 一次只取一条消息，跑完它的处理和排下的任务再取下一条：C 接口读到 ping 就答，原来一次把已到的消息都读进来，pong 早于动作的处理，QEMU 上日历按 # 换月后马上按 OK，OK 落在了旧的月上）。等的上限 500 ms（`inputHoldMax`），和安卓 11 以后的 InputDispatcher 一样（`KEY_WAITING_FOR_EVENTS_TIMEOUT`：还有没处理完、可能换焦点的事件时，键至多等 500 ms，到时照常投给当前窗口）：慢的应用只会让键晚到，不会卡住输入，再慢由「没有响应」接手（5 秒）；脚本在模拟器上跑时按它的放慢倍数放长。这 500 ms 算的是应用自己的时间，和「没有响应」的算法一样：钟一次至多走 50 ms，Shell 自己的循环停住的时间（等忙的磁盘的落盘、抢不到处理器）不算，Shell 按占用循环的限额让它歇的时间（`guard.go`）也不算；算上歇的时间，循环的时间到 2.5 秒（500 ms 的 1+4 倍）也放行，一直让 Shell 歇着的应用压不住键。以前按墙上的钟算：Shell 在 `-race` 下慢，待机界面打 153 按 OK，电话应用要先取三样列表才发出「1」的号码页，Shell 答得晚，500 ms 到了，5 和 3 落在旧的号码页上，号码少了开头的 1（CI 的 `race` 作业，2026-11-26；`TestHeldKeysWaitThroughTheLoopsStall`、`TestHeldKeysWaitThroughARest` 在改之前的代码上失败）。正常情况下应用几毫秒就回 pong，人按两个键之间远不止这么久，所以平时什么也不存；只有应用卡住时才存，回放时每个键走的是正常的路，回放到的键又交出一个动作，后面的键重新存着等它，所以不是一下全倒出去。系统浮层在上面、应用有可能弹框的请求（授权框、可信确认）还没回答时不存，键得给那个框；别的请求不算，发短信要等网络回执，算上的话整个等待期间键都直接过去（模拟器上信息应用发完短信马上按返回就因此退出了应用，`TestKeysWaitForTheAction` 的「request out」一例先确认过旧条件会失败）。音量键和媒体键不存，回放时应用不在前台就扔掉。命令面板里选的应用命令发给已经在运行的应用（`open` 事件）也算一个动作：设置收到后当场换到那一项的页面，紧跟着按的键落在新页面上，而不是换页前的旧页面。

**对话框只认看见以后的键**（`shell/input.go` 的 `overlayTakes`）：要用户做决定的系统框（授权框、应用要发短信或打电话的确认、可信确认的 PIN、给应用选照片或拍照、分享到哪、来电）只拿它出现以后按的键当回答，出现后的头 500 ms（`overlayArmDelay`）里的确认键也不算。每个键记着按下的时刻（`Key.At`，渲染器、网页预览收到时就记，存着再回放的键保留原来的），所以存着的键、应用启动时攒下的键、接着打的字都不会替用户点了「允许」、发出短信、填进 PIN 或接起电话：这正是存键可能带来的「一堆奇怪的逻辑」里危险的那种。方向键和 Fn + ◀ ▶（第一项、最后一项）当时就移动选择；返回（拒绝、取消）当时就算，只有来电时响铃以前按的返回不算，免得把电话挂掉。被扔掉的键记一行日志。浏览器对权限和安装提示也这样做（Firefox 的 `security.dialog_enable_delay`）。自测脚本的 `key`、`type` 像人一样等框能回答了再按。

## 17. 状态文件：整份写下，坏了往安全的一边

> 2026-11-28 实现；同一天第二轮：phone-keystored 的 `lock.json`、`parent.json` 留副本，写的时候删掉被杀的写入留下的临时文件，装应用时改名前 fsync；第三轮：换应用的代码时旧版本先挪开、新的就位再删，开机时收拾装到一半留下的目录。第四轮：Shell 和这五个服务以外的程序（phone-telephonyd、phone-pushgw、phone-clock、phone-imed、位置缓存、phone-storesrv 和商店的钥匙、自带应用），见下面「别的程序」。单元测试 ✓；**未上真机**：真机上写到一半断电（eMMC / UFS 的写缓存）没试过。

Shell 和系统服务的状态在状态目录（设备上是 `/var/lib/phone`，Shell 的在 `shell/` 下，phone-keystored 的在 `keystore/` 下）。以前多数文件直接 `os.WriteFile`：写到一半断电或进程被杀，文件只剩一半；读坏文件时有的从默认开始，有的让 Shell 起不来，锁屏、授权这几样从默认开始就是放开。

**怎么写**（`statefile` 包，商店后台的 `appstore.WriteAtomic` 也用它）：在同一个目录里建临时文件（`.<名字>.*.tmp`），设好权限（照给的设，不受 umask 影响），写、fsync、改名成正式的名字，再 fsync 目录。原来是符号链接就换掉，不顺着它写。写的过程中出错或被杀，原来的文件还是整份的，最多留下一个临时文件（出错时当场删掉）。被杀留下的，下一次写同一个文件时删掉：写完以后，同一个目录里名字正好是这个文件的临时文件名（`.<名字>.<一串数字>.tmp`）的普通文件，只要不是本进程正在写的，都删掉；别的文件、别的文件的临时文件、目录都不碰。不放在开机时扫：Shell 写的目录很多（`shell/`、`apps/`、`media/`、`web/`、`factory/`……），它又会单独重启，开机时扫整个状态目录会删掉还在运行的服务正写着的临时文件。用它的：Shell 在 `shell/` 下的全部状态文件（设置、应用设置、锁屏、授权、闹钟、相册和文件的索引、通讯录、日历同步、账号、流量、后台任务、更新、崩溃记录、壁纸……）和开发者目录里的密钥；phone-keystored（`keystore/` 下除了推送密钥的全部，见「还没整份写的」）；phone-smsd 的快照、发件箱和彩信；phone-pushd 的身份和心跳；phone-nfcd 的状态（`nfc/state.json`）。

**留副本的**（`statefile.WriteKeep`）：写完正式的文件，再照样整份写一次 `<名字>.bak`（不是硬链接，副本总是最后一次整份写下的内容）。读时（`statefile.ReadJSON`）先读正式的；它坏了或没了而副本是整份的，就用副本，并把正式的照副本写回，日志里说一次。两份都坏了就是「坏了」，文件原样留着，下次开机还是「坏了」，不会因为看到「没有」又放开。

| 文件 | 正式的坏了（读副本） | 两份都坏了 |
|------|------|------|
| `shell/lock.json`（锁屏的设置） | 照副本 | 设着锁屏 PIN 码就锁：phone-keystored 的 `keystore/lock.json` 或它的副本在（设着 PIN 码时才有），Shell 就当设着，不等它回话（[31](31-lock-and-biometrics.md) §2.1）。删掉、改写 Shell 的这个文件也一样 |
| `keystore/lock.json`（phone-keystored：锁屏 PIN 码的慢哈希、输错次数） | 照副本，PIN 码照常 | phone-keystored 照常启动，当作设着 PIN 码，什么 PIN 码都不对，设、改、删都不行，不改写这两个文件：手机一直锁着，锁屏上写明记录坏了，马上给出「按 # 清除这台手机」（和忘了 PIN 码时一样，防盗锁照旧，[31](31-lock-and-biometrics.md) §3、§11.2）。以前 phone-keystored 起不来，锁屏解不开 |
| `keystore/parent.json`（phone-keystored：家长 PIN 码） | 照副本 | 当作设着家长 PIN 码，未成年人模式开着，什么 PIN 码都不对；经关联的家长账号重设写一份新的，没关联只能刷机（[31](31-lock-and-biometrics.md) §10）。以前 phone-keystored 起不来 |
| `shell/perms.json`（运行时授权） | 副本里的拒绝留着，授予都不算（再用时重新问） | 什么都不算授予；装上的应用（商店或电脑装的）的网络和通知算拒绝，系统自带的照旧。以前 Shell 起不来 |
| `shell/revocations.json`（商店下架的应用） | 照副本 | 装上的应用都不启动（「商店下架应用的清单丢了：请打开商店重新取一份」），等商店下一次给的清单；系统自带的照常 |
| `/var/lib/slots/antitheft.json`（防盗锁的记录）、`shell/antitheft.json`（数据区的令牌） | 照副本，不把机主的手机锁上 | 记录坏了照样锁（账号写作「?」）；令牌坏了也锁，和数据区换过一样（[31](31-lock-and-biometrics.md) §11.4） |
| `shell/screen-time.json`（屏幕使用时间） | 照副本：限额和停用时间都在 | 从默认开始，日志里说（**没解决**：要两份都坏） |
| `shell/minor.json`（未成年人模式） | 照副本 | phone-keystored 的 `keystore/parent.json` 在就开着，用一开始允许的应用 |
| `shell/uids.json`（每个应用的 uid） | 照副本，防火墙的规则也照它 | 不给 uid，应用不启动：新表可能把别的应用的 uid 连同它的数据给出去 |
| `shell/catalog-issued.json`、`shell/update-issued`（商店目录、系统更新的签发时间，防重放） | 照副本 | 从零算，日志里说；更新另有签发后多久不收的上限 |

别的状态文件坏了：从默认开始，日志里说一次（`state: <路径>: damaged: …; starting from the defaults`），下次写的时候整份换掉。节假日日历（`shell/holidays.json`）不留副本：它是更新服务器那份的缓存，坏了当作没有（不记日志），例行检查看到没有就马上重新取（有更新服务器、连着网时；失败一小时后再试）；没有它门禁卡只是按周一到周五算工作日（[30](30-nfc.md) §4），不碰安全。它的签发时间（防重放）也跟着丢，但签发太久的日历本来就不收。

**别的程序**（第四轮）。Go 写的用 `statefile`；phone-telephonyd 是 C++，`telephonyd/src/statefile.cpp` 照同样的办法写：同一个目录里的临时文件（`<名字>.tmp`，只有一个线程写，被杀留下的下一次写时截断重写）、权限 0600、写、fsync、改名、fsync 目录，每个文件都再写一份 `<名字>.bak`；读时正式的坏了或没了用副本并写回，日志里说一次（`phone-telephonyd: state: <路径>: …`）。留副本的：

| 文件 | 正式的坏了（读副本） | 两份都坏了 | 以前 |
|------|------|------|------|
| `telephony/radio.json`（phone-telephonyd：射频、数据、漫游的开关） | 照副本 | 射频关着，等 Shell 连上来照它的设置设（`syncRadio`）：存着的可能是飞行模式；打紧急号码照样打开射频 | 射频照默认开着：开着飞行模式重启时文件坏了，射频就开了 |
| `telephony/telephony.json`（通话记录；收到了还没交给 phone-smsd 的短信） | 照副本 | 没有通话记录也没有待交的短信，日志里说 | 从空的开始，没有副本 |
| `telephony/apns.json`（用户改过的 APN，带密码，0600） | 照副本 | 用数据库里运营商的 APN，日志里说 | 一样，没有副本 |
| `pushgw-state.json`（phone-pushgw：网关的钥匙、登记的设备） | 照副本 | 新钥匙、没有设备（设备重新登记），日志里说 | phone-pushgw 起不来，systemd 一直重启 |
| `/var/lib/slots/rtc-offset.json`（phone-clock：实时时钟和真实时间差多少） | 照副本，`phone-clock restore` 说一次 | 和以前一样：restore 失败（不挡开机），时间是实时时钟的，等 NTP | 坏了 restore 就失败 |
| `ime/user.json`（phone-imed：用户词库） | 照副本 | 空的用户词库，日志里说 | 从空的开始 |
| 飞钱的 `transit.json`（交通卡余额和行程） | 照副本 | 余额为零，充值照常：丢了的余额不补，不当新卡给 ¥20.00 | 当新卡，余额 ¥20.00 |
| 浏览器的 `browser.json`（书签、去过的页） | 照副本 | 没有书签和历史，日志里说 | 原地写，写到一半就全没了 |

用户词库每选一个词都写（临时文件再改名，进程被杀不丢），带 fsync 和副本的整份写一分钟最多一次：整本词库写一次几十毫秒，选词要等它。断电最多丢最后一分钟选的词。

整份写、不留副本的（`statefile.Write`）：
- phone-pushgw 的 TLS / TLCP 证书和钥匙（`pushgw-tls/` 下的 `key.pem`、`cert.pem`、`sign.*`、`enc.*`）：哪一个读出来不对（剩一半）、没了，或者 `cert.pem` 签不出 `sign.pem`，就把在的改名成 `<名字>.damaged` 留着，整套重新做，日志里说。以前起不来，一直重启。读不了（权限不对、挂载没了）不算坏：照旧起不来，文件不动，免得把运维给的钥匙换掉。phone-pushd、phone-chatsrv 启动时读一次钉的 `cert.pem`，要重启以后才认新的，在那之前连不上（**没解决**，不会放开）。
- phone-storesrv：`store.key`、`store.pub`、`dev.pub`（`appstore.Open`、`pkg.DevStore`），`--root-out`（`factory/stores/store.pub`：Shell 信任的商店根）和 `--admin-file`。以前每次启动都原地重写，启动时断电或磁盘满就只剩半个，Shell 读不出这个根，商店的应用就装不了。`store.key` 坏了还是和以前一样换一把新的，`--root-out` 跟着换。
- 音乐的 `settings.json`、贪吃蛇的 `snake.json`：以前原地写；坏了从默认开始（和以前一样）。
- 小程序宿主把旧的 `storage.json` 搬成 kario-jsrt 的存储：新的整份写下（fsync）以后才删旧的。以前没 fsync 就删，断电可能两头都没了。
- 位置缓存（在 Shell 里）：`shell/netloc.json`（网络定位）、`shell/gnss/assist-*.bin`（GNSS 辅助数据）。以前已经是临时文件再改名，只差 fsync；坏了当作没有。

原来就整份写的（没改）：恢复出厂设置的请求（`wipe`）、增量更新的断点（`ota/delta`，带 SHA-256）、kario-jsrt 的小程序存储（`jsrt/src/store.c`）。

不算状态的（没改）：`/run` 下的（phone-audiod 的 UCM 状态、phone-charger 的状态、工厂模式的标记、phone-pushgw / phone-chatsrv / phone-storesrv 的地址文件）；构建时做的输入法词典和 kario-jsrt 字节码；phone-audiod 调试用的录音文件；商店应用下载的包（核对 SHA-256，装时再验签名）、小程序的安装（加载时 `pkg.VerifyDir` 验签名，坏了不运行）、换机收下的暂存文件（核对 SHA-256）；电脑上的工具（`kario`、`phone-pkg`、`phone-ota-delta`、模拟器的推送集群、自测）。

还没整份写的：
- phone-keystored 的推送密钥（`keystore/push/<应用>.key`，`push.KeyFile`）还是 `os.WriteFile`：这一轮没碰那五个服务。坏了这个应用就解不开推送，等 `RotatePush` 换新的（**没解决**）。
- 聊天的语音消息原地写一次：录下的媒体文件，开机不读，坏了只是这一条放不全。
- GNSS 辅助数据不核对内容：半截的非空文件还是交给调制解调器。现在写时 fsync，只有存储本身坏了才会有；调制解调器收到坏数据怎样**未验证**。

**不经它写的**（还是 `os.WriteFile`，都不是状态目录里的状态）：`/run` 下的（每次开机重来：转储、健康检查、自测的输出、热点客户端、自动更新的停止标记、networkd 的文件）；`/proc` 和 sysfs；交给别的程序的配置（iwd 在它自己目录里的网络配置，它靠 inotify 读；hostapd 的测试配置）；自测、基准和快照的输出；安装时解包的临时目录里的文件（整个目录改名换上，下面「换应用的代码」；改名之前 `statefile.SyncTree` 把里面每个文件和目录都 fsync 一遍，有文件 fsync 失败就不装）和开发者示例的构建目录；自测里模拟断电、恢复出厂设置和更新的文件；浏览器上传、拦截名单拷给网页进程的副本。

**换应用的代码**（`shell/install.go` 的 `unpack`、`recoverPackages`，第三轮）：新版本先解到旁边的 `<id>.new-<n>`，写上安装记录，整个 fsync（上面）；结束旧进程、删掉它的字节码缓存以后，旧目录改名挪到 `<id>.old-<n>`，新目录改名成 `<id>`，fsync `packages/`，然后才删 `<id>.old-<n>`。fsync 放在删之前：删掉的哪一部分都不能比两次改名先落盘。所以断电或 Shell 崩溃落在哪一步，盘上总有一个整份的版本，名字是 Shell 认得的。Shell 每次起来、扫 `packages/` 之前先收拾：`<id>` 不在而有 `<id>.old-*`，放回去（这次更新没装上，商店会再给；有几个时放回编号最大的，别的删掉）；`<id>` 在，`<id>.old-*` 删掉；`<id>.new-*` 都删掉（解包没完）。只动这两种名字、前面是合规的应用 ID 的目录，别的名字、同名的文件和符号链接都不碰；每收拾一样日志里记一行（`packages: <id>: …`）。以前是先删旧目录再改名：两步之间断电，应用不在应用列表里（数据还在），要重新安装；删旧目录删到一半断电，剩下的半个旧版本校验不过（清单也删了就不在列表里）；解到一半的目录一直留着。**未上真机**：没在真机上断过电，只在测试里让安装停在每一步之后（`unpackStep`），再在它留下的状态上起 Shell。

**测过的**：`statefile` 的单元测试：写到一半出错（磁盘满）、写的进程被 SIGKILL，原来的文件和副本都还是整份的，下一次照常写；权限不受 umask 影响、符号链接被换掉；正式的坏了读副本并写回、两份都坏了不改读出的值也不动文件。`shell` 的 `TestLockStartsLockedWithLockFileDamaged`、`TestGrantsFromADamagedFile`、`TestScreenTimeLimitsSurviveADamagedFile`、`TestTakedownsSurviveADamagedFile`、`TestTheftRecordFromItsCopy`、`TestMinorModeSurvivesADamagedFile`、`TestAppUIDsFromTheirCopy`：在改之前的代码上都失败（防盗锁「两份都坏了照样锁」以前就是这样）。第二轮：`keystore` 的 `TestPINFilesFromTheirCopies`、`TestPINFilesNeitherWholeStayLocked`（`TestPINFilesWithoutCopies` 查没有副本的旧文件照常打开），`shell` 的 `TestLockWithThePINsRecordDamaged`：在改之前的代码上失败（`keystore.Open` 出错，phone-keystored 起不来）；`shell` 的 `TestInstallSyncsTheCodeBeforeTheRename`：每个文件（安装记录也算）在改名之前 fsync、fsync 失败不装而旧版本留着，在改之前的代码上失败（改名前什么都没 fsync）；`statefile` 的 `TestWriteSweepsAKilledWritersTemporaryFiles`（被杀留下的临时文件删掉，别的文件、长得像的名字、目录、本进程正写着的都留着）、`TestSyncTree`，`TestKilledWriterLeavesTheOldFile` 加查被 SIGKILL 的子进程留下的临时文件在下一次写时没了（在改之前的代码上失败）。第三轮：`shell` 的 `TestUpdateCutShortLeavesAVersionWhole`：更新停在每一步之后，还有解包、删旧版本各做到一半，下一次起来应用在、是旧版本或新版本、每个文件都和签名对得上、数据还在、打得开，`packages/` 里别的都没了，日志一样一行，新版本就位以后再断电也不会回到旧版本；`TestFirstInstallCutShortLeavesNothing`：第一次安装停在改名之前什么都不留，之后就是装好的；`TestPackagesLeftoversAtStart`：放回编号最大的旧版本、删掉多余的，别的名字、同名的文件和链接不碰。三个在改之前的代码上都失败：更新停在删旧目录和改名之间时应用不在应用列表里，删旧目录做到一半时校验不过，停在改名之前的每一步都留下 `<id>.new-*`，开机时什么都不收拾。第四轮，在改之前的代码上都失败：`telephonyd_test` 的 `test_damaged_state`（`radio.json`、`telephony.json` 剩一半照副本，两份都坏了射频关着、Shell 设了才开；改过的 APN 剩一半照副本，两份都坏了用数据库的）；`push` 的 `TestDamagedCredentialsAreMadeAgain`（TLS、TLCP 每个文件剩一半或没了，网关照常起来，剩一半的留成 `.damaged`，客户端钉新的证书连得上；`TestUnreadableCredentialsAreKept` 查读不了的钥匙不换，它在改之前的代码上也过）；`pushgw` 的 `TestDamagedStateFromItsCopy`；`hal/rtc` 的 `TestOffsetFromItsCopy`；`programs/clockcmd` 的 `TestRestoreFromTheOffsetsCopy`；`ime` 的 `TestUserDictFromItsCopy`；`appstore` 的 `TestOpenKeepsItsFilesWhole`（启动时写到一半出错，商店的钥匙和根都还是整份的）；飞钱的 `TestTransitCardFromItsCopy`、浏览器的 `TestBookmarksKeptWhole`、音乐的 `TestSettingsKeptWhole`、贪吃蛇的 `TestPrefsKeptWhole`。「写到一半出错」靠限制进程能写的文件大小（`statefile/statefiletest`）。位置缓存只是加了 fsync，单元测试看不出来，没有在改之前的代码上失败的测试。

**没做的**：能删状态目录里文件的人把 `keystore/lock.json` 和副本一起删掉就是删掉了 PIN 码，这要靠 TEE（R15）。锁屏 PIN 码的记录两份都坏了，机主要清除手机才能再用，数据保不住（PIN 码的哈希没了，没有不绕过锁屏又能换掉它的路）；在未成年人模式里家长 PIN 码的记录也坏了，只能刷机。被杀的写入留下的临时文件要等再写同一个文件时才删，再也不写的文件的留着。卸载时删代码目录删到一半断电，下一次起来剩下的还在 `packages/<id>`：清单还在，应用就在列表里、校验不过（再卸载一次就好）；清单没了，剩下的和应用的数据都留着（再装这个应用时代码换掉，数据接着用）（**没解决**）。第四轮没做的见上面「还没整份写的」。
