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
│ SDK：Go（原型 ✓）· C ABI（计划，复用厂商 C++ 核心）· Rust（计划）· 任意语言     │
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
│ 通话 扫码      │  │ 蜂窝 短信 VoLTE    │  │ 认证 可信确认 国密   │  │ （设计）      │
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
- **进程归属**：应用、渲染器和 Shell 自己拉起的服务都设了父进程退出信号（`SIGKILL`），Shell 被强杀时一起结束，被冻结的也一样；设备上 systemd 停掉 Shell 时还会清掉它的整个 cgroup。Shell 自己拉起的服务退出后 1 秒重启，设备上由 systemd 负责。
- **内存**：Go 程序在 Yocto 里编成静态 PIE，不链接共享的 `libstd.so`，否则每个进程要为它的指针表多付约 6 MB 私有内存（[15](15-web-vs-semantic-benchmark.md) §5）。Shell 和它启动的全部进程不用透明大页（`prctl(PR_SET_THP_DISABLE)` 加 `GODEBUG=disablethp=1`），systemd 启动的 Go 服务在单元文件里设同样的变量：内核开着 THP 时，小堆碰到 2 MB 区域的一角就会占满整页。自测最后一步打印每个进程的 PSS（`mem-report`）。

## 6. 应用模型

| 方面 | 设计 | 原型 |
|------|------|------|
| 包 | `.phoneapp`（签名 tar.zst）；manifest：id、版本、权限、入口、`ui_mode`（semantic / canvas）、`min_platform` | manifest ✓（未签名） |
| 应用框架四档 | 原生语义应用 → 系统渲染文档 → JS 小程序 → Web 视图组件（D5 已定，[14](14-tui-route-and-app-framework.md) §5） | 前三档 ✓ |
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
| 设备密钥与证书 | ✓ 首次启动生成 Ed25519 和 SM2 两把，工厂各签一张证书 | 工厂注入，根密钥在厂商离线 HSM |
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

原型全部跑通（[13](13-prototype.md) 的自测用例）。识别是模拟的：扫码画面里画的就是那段文本，识别结果直接返回它。

## 10. 小程序与 H5 的位置

`phone.Ui` 是**数据，不是代码**。超级应用可以把服务端下发的界面树（小程序页面）直接转交 Shell 渲染，等价于「服务端驱动 UI」，而且天然具备按键可用性。

| 部分 | 状态 |
|------|------|
| JS 小程序：逻辑在 JS 引擎（无 DOM），视图是 `phone.Ui`，由宿主应用承载 | **原型 ✓**（`apps/miniapp`，goja；结构与微信小程序的逻辑层 / 视图层分离一致，见 [14](14-tui-route-and-app-framework.md) §4） |
| 来源标注：宿主代小程序请求能力时，系统对话框写明小程序名称 | **原型 ✓**（`req.origin`） |
| 子树委托：授权、密钥按「宿主 + 小程序」记账，小程序不能冒用宿主身份 | **原型 ✓**：运行时授权和使用记录按「宿主/小程序」记，宿主的授权不传给小程序；`keystore.sign`、`auth.confirm`、`attest.get` 用小程序自己的密钥和身份（`com.phone.miniapp/晨光咖啡点餐`），商户服务端据此核对是哪个小程序 |
| 小程序分发：签名的小程序包（同应用包格式，由宿主厂商的小程序商店签发），宿主装之前和每次启动前验签，改过的不加载；每个小程序的存储在自己的目录里，限 1 MB | **原型 ✓**（`apps/miniapp/install.go`） |
| 文档节点：文章类富文本 | **原型 ✓**（`doc` 节点，Markdown） |
| H5 网页 | 原型 ✓：第 4 档 Web 视图（`web` 节点，WPE WebKit 在独立进程、宿主的网页 uid 下渲染，经共享内存上屏），文本端降级为阅读模式（[14](14-tui-route-and-app-framework.md) §5） |

## 11. 版本与兼容

- `phone.Ui`：握手时交换 `"phone.ui/0"`；minor 只加字段（未知字段忽略），major 变更提前一版弃用。
- 能力目录：每个能力独立版本（如 `media.call.start@1`），SDK 封装版本差异。
- 平台版本 + manifest `min_platform`；商店按此分发。

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
| 像素渲染器 | Rust：phone-paint（tiny-skia）、phone-gui（Slint） | 同左，闭源；GPU 渲染（[06](06-gui-architecture.md)） |
| mediad | Go，合成画面源 | Rust 或 C，基于 libcamera / V4L2 / GStreamer |
| keystored | Go，软件密钥 | OP-TEE 可信应用（C）+ 用户态代理 |
| telephonyd | — | C++23（[04](04-ui-language-telephonyd.md)） |
| SDK | Go；Python 无 SDK 直连示例 | C ABI（厂商复用 C++ 核心）、Rust、Go |
| 小程序逻辑层 | goja（纯 Go JS 引擎） | 视性能选 QuickJS 或 V8；接口保持 `Page` / `setData` / `phone.*` |
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
| 连电脑的文件共享（[27](27-usb-typec.md) §3.2） | 共享助手按路径什么都打不开：80 端口的监听套接字由 Shell 建好让它继承；电脑下载时 Shell 打开文件交只读 fd，上传时交一根管道的写端，Shell 的库从读端边收边存，助手说完整、字节数对得上才留下；助手发来的 fd 一律关掉 | ✓（开发机）；模拟器上**待验证** |
| 照片缩略图、壁纸 | Shell 自己建表面（裁成格子或屏幕大小，写一次）交给渲染器；宫格里只有焦点附近几行的照片带表面，一帧不再用的表面 Shell 关掉、渲染器解除映射（[23](23-renderer-protocol.md) §4.2） | ✓ |
| 文件选择器（任意文件） | 同相册选择器：文件属于用户，放在系统的「文件」区；`file.pick` 在系统列表里由用户选，交出那一个文件的只读 fd；`file.save` 先问用户；分享文件只把那一个授权给接收方（`file.open`） | ✓ |
| 按应用联网 | 没有 `net` 权限（或被用户关掉网络）的应用进自己的空网络命名空间；只用 Wi-Fi 和按应用流量由 Shell 用 nftables 按 uid 管（[17](17-app-sandbox.md) §1）。Android 的 netd 也是按 uid 打标记、计数 | ✓ |
| 服务按需启动 | systemd 先打开服务的监听套接字，有连接时才启动服务（socket activation），节省开机时间和内存。keystored、mediad、audiod 已这样启动（`phone-*.socket`）；pushd、telephonyd 要一直连着网关和基带，开机就启动 | ✓ |
| Shell 崩溃、重启不连累应用 | 见下文 | ✓ |
| 显示和键盘不需要 root | phone-gui 通过 logind 的 `TakeDevice` 拿到 `/dev/dri`、`/dev/input` 的 fd | 设计 |

**Shell 重启，应用不重启。** 应用的界面连接（Shell 那一端）交给下一个 Shell，它接管连接、让应用重发页面：

- **交出连接**：服务开了 fd 存储时（`PHONE_FDSTORE=1`），Shell 每启动一个应用，就把它的连接存进 systemd 的 fd 存储（`FDSTORE=1`，名字 `app-<应用 id>-<pid>`）。Shell 崩溃后 systemd 重启它，经 `LISTEN_FDS` 交还这些连接。Shell 升级或自测时原地重启：exec 自己，连接留着不关，编号写在 `PHONE_HANDOFF` 里。
- **接管**：新 Shell 按名字找到应用，被冻结的先解冻，然后发 `resync`。Go SDK 记着 Shell 那边的页面栈，收到后重发（reset，再逐页 push）；旧 Shell 没回复的调用返回错误。C SDK 把 `resync` 交给应用自己重绘。接管来的应用不是新 Shell 的子进程，它的退出看连接断开（`/proc` 里成了僵尸也算退出）。
- **进程不能跟着死**：父进程退出信号是在「fork 出子进程的那个线程」结束时发的，而 exec 会结束调用线程以外的所有线程。所以 Shell 的子进程都从同一个锁定、永不退出的系统线程上启动，原地重启也从这个线程 exec。用 fd 存储时应用不设这个信号（沙箱启动器切换 uid 后本来会重设它，策略里的 `survive` 关掉这一步）；单元是 `KillMode=process`，重启只结束 Shell 本身，正常停止时由 Shell 自己先解冻再结束全部应用。
- **单元配置**：`FileDescriptorStoreMax`、`NotifyAccess=main`（不写的话 systemd 不收 fd 存储消息）、`KillMode=process`、崩溃后重启。
- **应用放进各自的作用域**：systemd 重启服务时，总是先把新进程放进服务的主 cgroup（systemd 259 的源码里写明了，`DelegateSubgroup=` 也是之后才移进去）。如果应用的组建在 Shell 的服务组下面，主 cgroup 要为子组打开控制器，按 cgroup v2 的规则就不能再放进程，重启失败（`Device or resource busy`）。所以显示服务里每个应用在自己的 systemd 作用域里（`phone-apps.slice/app-<id>-<pid>.scope`，[17](17-app-sandbox.md) §5），Shell 的服务组只有 Shell 自己。
- **验证**：自测里 Shell 原地重启后，计算器的进程号不变、页面原样恢复，之后的用例和整套兼容测试都在新 Shell 上跑（开发机和设备都通过）。fd 存储这条路：设备开机自测起一个和显示服务同样配置的检查服务（`phone-fdstore-check.service`，应用作用域 + fd 存储），Shell 故意崩溃，systemd 重启它，新 Shell 从 fd 存储接管计算器，计算器仍在自己的作用域里，内存和进程上限照旧，后台冻结照常（`fdstore.script`）。

**不必限制「一次只能带一个 fd」**，真正需要管住的是这几条：
- **方向**：文件只从系统流向应用。Shell 对应用的连接、各系统服务对 Shell 的连接，都会立即关闭对端发来的 fd（`Conn.RefuseFiles`）。否则应用不停地塞 fd，就能耗尽 Shell 的文件描述符表，所有应用都会受影响。有单元测试覆盖。
- **数量**：一条消息最多 8 个 fd；每个应用最多 4 路音频流、1 路录音；audiod 另有 32 路的总上限，防止应用反复申请共享内存。
- **类型**：系统只发自己验证过的对象，比如封住大小的 memfd、只读打开的文件。接收方也再检查一遍，比如没封印的环形缓冲区拒绝映射。

另有一个 `pidfd_getfd` 可以直接从别的进程「拿」fd，它需要调试权限。我们的 seccomp 对应用禁止了这个调用（[17](17-app-sandbox.md) §4）。

## 15. 云端服务：设备连到哪里

商店、推送网关、更新服务器在云端，地址和信任都由产品层给出，平台不写死任何地址。产品层用 Yocto 变量 `PHONE_SERVICES` 指向一个目录，`phone-services` 配方把它装进镜像：

| 文件 | 装到 | 内容 |
|------|------|------|
| `services.env` | `/usr/share/phone/services.env` | `PHONE_STORE_SERVER`（商店后台）、`PHONE_PUSH_GATEWAY`（推送网关）和 `PHONE_PUSH_GATEWAY_CERT`（要钉住的网关证书）、`PHONE_UPDATE_URL`（更新服务器）和 `PHONE_UPDATE_KEY`（`latest.json` 的签名公钥，[25](25-system-update.md) §0）；哪一项都可以不写 |
| `*.pem`、`*.pub` | `/usr/share/phone/services/` | 网关证书、`latest.json` 的签名公钥 |
| `trust/<商店>.pub` | `/usr/share/phone/trust/` | 设备信任的商店根，文件名是商店证书里的名字（`phone-storesrv` 的是 `store`） |

- phone-pushd、鸿雁的模拟服务器和显示服务（Shell）的单元先用本机的默认值，再读 `services.env` 覆盖（systemd 的 `EnvironmentFile`）。
- 写了哪一项，配方就留一个标记（`services/store`、`services/push`、`services/update`）。设备上模拟的商店后台和推送网关只在没有对应标记时启动，所以可以只把商店和更新服务器放到云端，推送还用设备上的模拟网关。
- 没有 `PHONE_SERVICES` 时这个包是空的，镜像和以前一样，全部用设备上的模拟服务。
- **验证**：`make yocto-cloud-test` 在构建容器里起商店后台、推送网关和一个更新服务器（QEMU 里看是 10.0.2.2），按它们生成的商店根和网关证书写出这个目录，带着它构建镜像，在 A/B 磁盘上开机。设备上的 `cloud-test` 先确认模拟的商店和网关没有启动，再跑三步：开发者经云端商店发布的应用从目录装上；鸿雁的回复经云端网关推到手机；「设置 › 系统更新」读到云端的 `latest.json`。最后不带这个目录再构建一次镜像。
- **未实现**：云端网关还不要求设备的证书连到认证方的根：内测手机没有灌装，根是第一次开机时自己生成的；量产的手机在产线灌装以后（[22](22-developer-api.md) §11）才能打开。

## 16. 和安卓各层的对应

安卓有一整套独立的栈。我们不一层层照搬，每一层都有一个更小的对应物：应用不跑虚拟机、界面是语义描述、只有一个渲染器、直接用主线内核的接口。这样省掉了几层，但也意味着有些安卓已有的东西我们要自己补。

| 安卓 | Kario 里对应的 | 状态 |
|------|---------------|------|
| Binder（进程间调用、调用方身份） | 每个系统服务一个 Unix 套接字上的 `wire` RPC，能传文件描述符（§14）；应用只连 Shell，能力代理按套接字认出是谁（像 Binder 的调用方 UID） | ✓ |
| ART / Dalvik、Zygote | 不需要：应用是原生进程（Go、C、C++、Rust、Python），小程序是 JS 跑在宿主里，网页在 WPE 里；没有虚拟机就没有 Zygote，系统服务合成一个程序 `kario-system` 共用代码（[25](25-system-update.md) §3） | ✓（设计取舍） |
| SurfaceFlinger、HWC、Gralloc | 渲染器插槽（phone-paint）把 Shell 给的语义帧画到 DRM/KMS 上；视频帧经共享内存直通渲染器（[23](23-renderer-protocol.md)） | 软件渲染 ✓；GPU 合成、dmabuf 上屏**未实现**（R21，要真机） |
| WindowManager、ActivityManager | Shell：一个前台应用、页面栈、系统界面和弹层、生命周期、后台冻结、内存不足时结束后台应用并留墓碑（回去时恢复原来的页面，[17](17-app-sandbox.md) §5.3） | ✓ |
| AudioFlinger、AudioPolicy | phone-audiod：共享内存环形缓冲区的流、音频焦点、类别、静音和振动（[20](20-audio.md)） | ✓；听筒 / 扬声器路由（ALSA UCM）、蓝牙**未实现**（R29） |
| 权限模型、SELinux | 清单权限 + 运行时授权（能力代理）+ 沙箱：独立 uid、命名空间、Landlock、seccomp、每应用 cgroup、按应用防火墙（[17](17-app-sandbox.md)）；用 Landlock 代替 SELinux 的强制访问控制 | ✓ |
| HAL（HIDL / AIDL） | 不做通用的 HAL 层：直接用主线内核的接口（DRM、V4L2、ALSA、evdev、IIO、ModemManager），`hal/` 只是设备类上的一层薄封装，用哪一个由 SKU 配置；只给基带留了接厂商 Android BSP 的后端（libgbinder 讲无线接口 HAL） | 主线接口 ✓（QEMU）；厂商后端**未实现**（R2、R16） |
| PackageManager、安装器 | Shell 的安装器：签名的 `.phoneapp`、信任的商店根、吊销清单、每次开机后首次启动前重新验签（[07](07-ecosystem-and-store.md)） | ✓ |
| Intent、分享 | `intent.invoke`（调起别的应用做事并拿回结果，如付款）、分享面板、深链接（[22](22-developer-api.md) §4.9、§4.10） | ✓ |
| 应用生态（SDK、Play 商店） | 语义界面协议和 Go / C SDK、小程序、网页节点、商店（[22](22-developer-api.md)、[07](07-ecosystem-and-store.md)） | 平台 ✓；生态本身要靠时间和厂商，不兼容安卓应用是有意的 |
| 输入（InputFlinger、输入法框架） | evdev 按键经渲染器进 Shell，只到前台应用（§16.1）；输入法是独立进程 phone-imed（拼音、九宫格、笔画、联想，[16](16-input-method.md)） | ✓；触屏不是主要输入 |
| 显示 | DRM/KMS | QEMU 的 virtio-gpu ✓；真屏**未实现**（R16） |
| 传感器（SensorService） | 还没有：键盘机最要紧的是通话时的距离传感器和环境光，打算经 IIO 读，做成系统服务 | **未实现**（要真机） |
| 基带（RIL、Telephony 框架） | phone-telephonyd（C++23）加电话、短信应用；ModemManager 后端（[04](04-ui-language-telephonyd.md)） | 模拟的 ModemManager 上 19 个用例 ✓；真机基带**未实现**（R2） |
| 短信（`InboundSmsHandler`、SmsManager、送达回执） | telephonyd 收发、要送达回执，收到的短信放进待确认队列，phone-smsd 存好确认后才放手（ModemManager 上这时才从基带删）（[32](32-messaging.md) §3） | 模拟基带 ✓；ModemManager 一侧未运行测试、**未上真机** |
| TelephonyProvider（`mmssms.db`）、默认短信应用、BlockedNumberProvider | phone-smsd：只追加的日志加快照（不用数据库）、会话、已读未读、搜索、拦截名单和拦截文件夹、验证码；删和拦截只给系统的短信应用（[32](32-messaging.md)） | ✓；默认短信应用不能换，拦截只管短信 |
| CellBroadcastService、CellBroadcastReceiver | telephonyd 收小区广播（模拟网络的 `sim.cb`；ModemManager 1.24 的 `CellBroadcast`），smsd 去重、按频道分级、存，Shell 在锁屏上也弹全屏警报，audiod 的 `alert` 类别；国家级不能关（[32](32-messaging.md) §7） | 模拟基带 ✓；ModemManager 一侧未运行测试、**未上真机** |
| 媒体编解码、相机 | phone-mediad：软件解码（WebM、MP3、Opus、Vorbis）、V4L2 采集、相册 | ✓；硬件编解码、libcamera **未实现**（R14、R24） |
| 推送（FCM） | pushd 和推送网关：一条长连接、端到端加密、合并唤醒（[21](21-push.md)）；协议和模拟网关开源，正式的推送服务不开源 | 设备侧和模拟网关 ✓；正式服务**未实现** |
| NFC（NfcService、HostApduService、默认付款应用） | phone-nfcd：`hal/nfc` 上的控制器（模拟的，和 NCI 后端），按刷卡时间选卡、刷不开就换卡，息屏锁着照样刷；系统应用「卡包」管卡，Shell 只有双击电源键的快速选卡；控制器按主板配置（`sku.json` 的 `nfc`）选；应用经 `nfc.apdu` / `nfc.reply` 答读卡器（[30](30-nfc.md)） | 模拟控制器 ✓；NCI 后端上真机、安全芯片里的卡**未实现** |
| Keystore、StrongBox、Key Attestation | phone-keystored：应用密钥（Ed25519 / SM2）、设备认证、可信确认、推送密钥；产线灌装设备证书（[22](22-developer-api.md) §11） | ✓；放进 TEE **未实现**（R15） |
| 锁屏（Keyguard、Gatekeeper、指纹 HAL） | Shell 的锁屏（息屏即锁、紧急呼叫、隐藏通知内容、锁屏快捷键）；phone-keystored 校验锁屏 PIN 码、保存指纹模板、出认证令牌；`hal/fingerprint`（[31](31-lock-and-biometrics.md)） | 模拟传感器 ✓；TEE、真机传感器**未实现**（R15） |
| 系统更新（A/B、update_engine） | RAUC 的 A/B 槽、dm-verity、自适应增量、分批推送、回报（[25](25-system-update.md)） | QEMU ✓；真机引导**未实现**（R16） |
| 电源管理（Doze、唤醒锁） | 后台冻结（cgroup 冻结器）、按预算唤醒、后台任务合并、推送顺带跑任务；息屏后整机挂起，RTC 按闹钟和任务截止叫醒（Shell 自己掌握全部「唤醒锁」：通话、放音、更新、USB 上的电脑、被唤醒的应用，[28](28-battery.md) §3） | ✓；挂起在开发机和模拟器上模拟（模拟器真挂起后叫不醒：它的 RTC 不给挂起到空闲设唤醒中断）；真机上的挂起、唤醒源和电流**未验证**（要真机） |
| 挂起（SystemSuspend、Power HAL） | `hal/pm`：经 `/sys/power/state` 挂起到内存（没有就挂起到空闲）；先读 `wakeup_count`、挂起时写回，其间来了唤醒事件内核就拒绝挂起；RTC 唤醒闹钟设在下一个闹钟或任务前；有没有能叫醒系统的按键看输入设备的 `power/wakeup`（[28](28-battery.md) §3） | 单元测试 ✓，开发机和模拟器上模拟；真机**未验证**；安卓的 SystemSuspend 用的也是 `wakeup_count`，厂商内核上同样有 |
| 电池（BatteryService、Health HAL） | `hal/power`：电源类（`/sys/class/power_supply`）的电量、在不在充电、充电器是不是 USB PD 快充（[27](27-usb-typec.md) §1） | ✓（模拟）；真机**未验证**；安卓的 Health HAL 读的也是电源类，厂商内核上同样有 |
| 灯（Lights HAL：通知灯、背光） | `hal/led`（LED 类：定时器触发的慢闪、多色灯的各通道强度）、`hal/backlight`（背光类）；用哪个灯、哪个背光由主板配置给（下文「那 `hal/` 是什么」） | ✓（QEMU 上用 uleds 造一个灯）；真机**未验证**；高通的厂商内核常把背光做成 LED 类的 `lcd-backlight`，要多认这一种（**未核实**，[24](24-sku-framework.md) §7） |
| 振动（Vibrator HAL） | Shell 直接写 LED 类的振动器 `/sys/class/leds/vibrator`（先写时长再激活），静音模式下来电每 1.5 秒振一次（[20](20-audio.md) §4.1）；还没收进 `hal/` | 写法 ✓（模拟器上只计数）；主线内核的振动器是输入设备的力反馈（`FF_RUMBLE`），这一种**未实现** |
| USB 设备模式（UsbDeviceManager、USB gadget HAL） | `hal/usbgadget`：configfs 里一个 CDC-NCM 网卡，电脑被信任后经它共享文件（[27](27-usb-typec.md) §3） | QEMU ✓；真机**未验证**；3.10 一代的安卓内核用安卓自己的 `android_usb`，不是 configfs（**未核实**，[24](24-sku-framework.md) §7） |
| MTP（MtpServer、MtpDatabase，在 MediaProvider 进程里经 FunctionFS） | `phone-usbshared` 的 MTP 模式（`usbshare/mtp`）：Shell 在 configfs 里建 FunctionFS 函数、写描述符、打开端点，把这些文件交给沙箱里的助手；四个存储 Photos、Videos、Music、Files 走网络共享那同一个库和检查，电脑能读能加，不能删改；设置 › USB 在它和网络共享之间选（[27](27-usb-typec.md) §3.3） | 单元测试、开发机自测、模拟器 ✓；真电脑**未上真机**（Windows、macOS、Linux 都没试过）；缩略图、SendObjectPropList、PTP（相机模式）**未实现** |
| 音频 HAL（`audio.primary`、tinyalsa） | 不经 `hal/`：phone-audiod 直接用 ALSA，现在经 `aplay` 写 `plughw`（自动模式下插上 USB 声卡就换过去）；通话的声音在真机上由基带和声卡直连（[20](20-audio.md) §4、§7） | 开发机和模拟器 ✓；听筒、扬声器、耳机的切换（ALSA UCM）**未实现**；厂商内核上要经 libhybris 加载 `audio.primary.*.so`（[24](24-sku-framework.md) §7.3） |
| 位置（LocationManager） | `location.get`：只给前台应用、状态栏提示 | 模拟定位 ✓；卫星定位**未实现**（R25） |
| Wi-Fi、蓝牙管理 | Shell 经系统总线管 iwd 和 BlueZ，设置里有 Wi-Fi、蓝牙页（`wifi.*`、`bt.*`）：连加密网络时密码只交给 iwd；企业网络（802.1X：PEAP 内层 MSCHAPv2，TTLS 内层 MSCHAPv2 或 PAP）要填用户名、密码和服务器的域名，Shell 写成 iwd 的配置（服务器证书按系统的根证书和这个域名核对，外层身份只给匿名加域），不填域名不连（照安卓 11 起的做法：否则任何服务器都能骗走密码）；和有屏幕的设备配对时用户在系统对话框里核对配对码；蓝牙传文件和电话本由 Shell 自己讲 OBEX（`obex` 包；BlueZ 的外部 profile 把 RFCOMM / L2CAP 连接交给它，不装 obexd：它要的 libical 带进 39 MB 的 ICU，实测见 [13](13-prototype.md) §5）：已配对设备发来的文件每个都问用户，收进「文件」；设置里把「文件」里的文件发给已配对的设备（`bt.send`）；车机读通讯录和通话记录（PBAP 服务端）前问用户一次，按设备记住，设置里能收回；开发机上是模拟的网络和设备；有线网仍由 systemd-networkd 管 | ✓（开发机模拟）；模拟器上用模拟射频（mac80211_hwsim、btvirt）跑同一组用例：企业 Wi-Fi 的接入点是 hostapd 自带的 EAP 服务器（第三个模拟射频，证书在测试时生成；它的 MSCHAPv2 要的 MD4、DES 来自只装在模拟器镜像里的 OpenSSL legacy 模块；它不发地址，测试网络用静态 IPv4），蓝牙传文件和电话本的对方是 btvirt 的第二个控制器，这两部分在模拟器上通过；OBEX 只和自己的实现互通过，和别家手机、车机**未验证**（**未上真机**）；GOEP 2.0 的单响应模式、OBEX 认证**未实现**；蓝牙耳机的声音（A2DP、HFP，要 PipeWire）**未实现** |
| 飞行模式、移动数据 | 设置首页和电源菜单里的开关（`radio.*`）：飞行模式让 telephonyd 关掉基带的射频（ModemManager：`Modem.Enable(false)` 后 `SetPowerState` 到低功耗），同时关掉 Wi-Fi 和蓝牙、记住用户原来的开关，结束时恢复；飞行模式下用户可以再打开 Wi-Fi、蓝牙，下次飞行模式就留着它们（照安卓、iOS）；拨紧急号码时 telephonyd 先给射频上电再拨，Shell 随之退出飞行模式（照安卓，方便对方回拨）；移动数据关掉时 telephonyd 断开数据承载（`Simple.Disconnect`），防火墙挡住它报的移动网卡（进、出和转发）；开着时用网络默认的 APN 连上（`Simple.Connect`）；两个开关重启后保持，telephonyd 自己也记一份，开机时 Shell 还没起来基带也按原样 | 模拟基带 ✓（开发机自测、telephonyd 单元测试）；ModemManager 后端对着模拟的 ModemManager 的用例通过（`make mm-selftest`，在容器里）；**未上真机**；防火墙的设备用例在模拟器上通过；选 APN、移动网卡的地址配置（DHCP 或承载给的地址）、数据漫游开关**未实现** |

所以「对应的实现」不是一层层都做一份：虚拟机、Zygote、通用 HAL 这几层是有意不要的；窗口、权限、包管理、调起、推送、密钥、更新、Wi-Fi 和蓝牙管理这些都已经有了；剩下的缺口是传感器、GPU 合成、真机上的电源策略和厂商后端，都要真机才能做。

**为什么不做 HAL。** 安卓的 HAL 是为了让一个系统版本跑在几千种厂商内核和驱动上：厂商在 HAL 后面放自己的闭源实现，系统升级不用等厂商。我们只支持主线内核的硬件（R16 选型的前提），内核已经把硬件抽象成了标准接口，再加一层没有东西可以放进去。以按键为例，投递的路是：键盘芯片的驱动（矩阵键盘 TCA8418、GPIO 按键）按设备树里的 `linux,keymap` 报标准的 `KEY_*` 码 → evdev → 渲染器（只有它能读 `/dev/input`，应用读不到）→ 渲染器协议的 `key` 消息 → Shell。机型之间的差别在 SKU 的键盘配置里（`sku/keypads/*.toml`、`keys.toml` 列出每个键的码，测试核对渲染器认得每一个，[24](24-sku-framework.md)），不同厂商的扫描码由设备树（或 udev hwdb 写进内核的键码表）改成标准码，不需要用户态的 HAL。其他硬件同理，每类硬件有一个系统服务管：显示和按键归渲染器（DRM、evdev），声音归 audiod（ALSA），相机归 mediad（V4L2），基带归 telephonyd（ModemManager），网络归 iwd / BlueZ，以后的传感器归一个读 IIO 的服务。需要接厂商 Android BSP 的只有基带这类没有主线驱动的东西，做法是在那个服务里加一个后端（telephonyd 用 libgbinder 讲无线接口 HAL，**未实现**，R2），而不是在整个系统下面垫一层。

**那 `hal/` 是什么。** 设备类（LED、背光、电源、USB 设备模式）的读写散在各服务里会各写一遍 sysfs，也没法在开发机上模拟，所以收成一层薄封装：`hal/led`（LED 类：定时器触发的慢闪、多色 LED 的各通道强度；QEMU 上用 uleds 造一个）、`hal/backlight`（背光类）、`hal/power`（电源类：电量、充电器是不是 USB PD 快充）、`hal/usbgadget`（USB 设备模式：configfs 里的 CDC-NCM 网卡）、`hal/pm`（整机挂起：`/sys/power/state`、`wakeup_count` 握手、RTC 唤醒，[28](28-battery.md) §3）、`hal/nfc`（NFC 控制器：模拟的和 NCI 后端；NCI 后端讲安卓厂商驱动 `nxpnfc`、`pn5xx_i2c` 给的原始 NCI 设备节点，主线的 `nxp-nci` 把 NCI 留在内核里、不给用户态做卡模拟，[30](30-nfc.md)）、`hal/fingerprint`（指纹传感器：读取、录入、比对；只在 phone-keystored 里用，开发机和 QEMU 上是模拟的；手机上的 FPC、汇顶传感器没有主线驱动，要厂商的 TEE，**未实现**，[31](31-lock-and-biometrics.md) §4.1）。它不定义新接口，只是内核设备类的 Go 写法（`hal/fingerprint`、`hal/nfc` 例外：内核没有能直接用的设备类，接口是自己定的），每类有一个模拟后端；机型之间的差别只有「用哪一个」：主板配置写名字（`sku/boards/*.toml` 的 `[leds]`、`backlight`），经 `sku.json` 交给 Shell，同一份二进制驱动每块板（[24](24-sku-framework.md)）。比如通知灯：F(x)tec Pro1 X 是 PMIC 上的 RGB 灯 `rgb:status`（驱动自己会闪，慢闪不唤醒 CPU），一加 N100 的主线设备树里没有灯，就不配，新通知改为点亮屏幕。按键不经过它：渲染器直接读 evdev，上面说的路不变。

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
- 什么时候交：应用每交一页（`push`、`replace`、`reset`）都重新计时；最后一页到了 150 ms（`keysSettle`）以后，应用还在运行、页面不再是旧的，就把存着的键按按下的顺序重新走一遍 `Key`，和刚按下的键是同一条路。所以这期间弹出的系统对话框（比如应用一启动就请求的权限框）先收到它们，和安卓「前一个事件弹出的窗口收到下一个键」是一个意思。
- 什么时候扔：回放时这个应用已经不在前台（用户回了待机、切到别的应用），存着的键全部扔掉，不交给别的应用；进程退出（崩溃、被结束）时清空。离开前台时并不清空：新页面到之前又切回来，之前存的键回放时照样交给它。
- 没有超时：新进程一直不交页面，键就一直存着；存键时 Shell 不发 ping，也就不报「没有响应」（[17](17-app-sandbox.md) §5.2），用户按返回回待机。这一点和安卓不同。
- 解冻不用存键：切到前台时 Shell 就解冻它（写 `cgroup.freeze` 或发 SIGCONT，`shell/freeze.go`）；页面一直在 Shell 手里，焦点移动、打字马上生效，要交给应用的动作排进它的发送队列（每个应用 256 条，满了丢掉）和套接字缓冲，应用恢复运行后按顺序处理。
