# 应用开发体验：TypeScript 框架和系统中间件

> 状态：**P1–P4 在开发机上完成，P5 在开发机上完成**（§5.1–§5.3）：引擎和运行时、声明式框架 KarioUI、Node 下的运行时、`kario` 命令行（`new`、`build`、`run`、`test`、`package`、`publish`、`device`）、用 KarioUI 重写的计算器（换下了 Go 版）、Kario Studio 扩展、界面和审核规范（[33](33-app-guidelines.md)）、教程（[34](34-app-tutorial.md)）；单元测试和自测在开发机上通过，QEMU 上整套自测通过（用例数见 57（内部文档）「当前验证基线」，含 TypeScript 计算器、KarioUI 示例、原生模块、小程序和对着设备开发），**未上真机**。隐私标签（[33](33-app-guidelines.md) §3）做了，`kario publish` 在开发机上对官方商店后端跑过。原生模块（P5，§5.3）在开发机上做完：C 接口、kario-jsrt 加载和拒收、打包和审核、Node 下的加载、示例和测量；QEMU 上测过（§5.3），**未上真机**。对着真机开发（`kario device`：经信任过的数据线装应用、看日志、重新载入、重启，§3）、商店装的 TypeScript 应用在设备上预编译成字节码（§5.1）、TypeScript 和 Go 应用启动时间的自测步骤（§5.1）在开发机上做完，单元测试和自测通过，QEMU 上也通过（经真的 SSH 的用例、设备上的字节码、启动时间，§5.1），**未上真机**。规范有了正反例截图（脚本生成），教程写到了推送和后台任务，开发者文档在开发者门户（不开源）里能读到（§4）；小程序迁到同一个引擎在开发机上做完（§5.4），QEMU 上小程序的自测用例通过。在设备上调试（断点（可带条件、命中次数）、日志点、函数断点、单步、调用栈、变量、在帧里求值、`debugger` 语句、未捕获的异常时暂停，附加到正在跑的应用上而不重启，经开发者链路和 Kario Studio，§5.5）和符号文件（`kario package` 把源码映射另写一个文件、不进包，商店收下后替开发者符号化崩溃报告，§5.6）在开发机上做完，单元测试通过，**未上真机**。2026-11 决定应用的主力语言改为 TypeScript，替换先前的 Swift 方案。现在怎么写应用见 [34](34-app-tutorial.md) 和 [22](22-developer-api.md)；这篇写的是路线和做到了哪里。

## 0. 结论

- **中间件已经有了，这次把它定成唯一的入口**。Shell 的 broker、系统服务（音频、媒体、密钥、推送、电话、输入法）和 phone.Ui 协议合起来就是 Kario 运行时：应用拿不到设备，界面交给系统画，每个能力都要经它申请（权限、用途、沙箱、审计，[17](17-app-sandbox.md)）。TypeScript 应用只看得到 `kario.*` 接口，没有文件系统、网络套接字和进程，比安卓的 Java 层更严：连 JNI 那样的后门都没有。
- **应用的主力语言定为 TypeScript**，上面做声明式框架 KarioUI，写法照 SwiftUI 和鸿蒙的 ArkUI（状态变了界面自己更新）。设备上用 QuickJS-ng 引擎（MIT，几百 KB）跑预编译好的字节码；开发机上同一份代码跑在 Node.js 里，VS Code 自带的 JS 调试器直接下断点。
- **开发工具只做一个 VS Code / Cursor 扩展**，加一个 `kario` 命令行（Go 写，内嵌 esbuild 编译 TypeScript，打包不要求装 Node）。
- **TypeScript 是推荐的路，不是唯一的路**：微信、支付宝这类大应用不用重写成 TypeScript。安卓上的微信本来就是 Java / Kotlin 的界面加大量 C++（网络库 Mars、数据库 WCDB、音视频编解码、小程序引擎）。Kario 给它们两条路：TypeScript 写界面、性能要紧的部分做成**原生模块**（C 接口，像鸿蒙的 NAPI、React Native 的 TurboModules，P5：开发机上做完，**未上真机**，§5.3）；或者整个应用用 C / C++ / Rust / Go 写，直接讲 phone.Ui 协议（今天就能做，`sdk/c`，示例 hello-cpp）。两种都在同一个进程沙箱里、只能经中间件要能力，原生代码只是多一道审核。
- Go 留给系统服务和现有的系统应用，Rust / C++ 留给渲染器和要性能的部分；小程序也跑在同一个引擎上（§5.4）。
- 学苹果学的是「一家定下整条工具链、规范和审核标准」，不是它的语言。

## 1. 现状和差距

| 环节 | 苹果 | Kario 现在 | 差距 |
|------|------|------------|------|
| 语言 | Swift（以前 Objective-C） | TypeScript（`kario-jsrt`，KarioUI；计算器已改写）、Go（其余的系统应用）、C / C++（`sdk/c`）、Python（直接讲协议）、JS / TS（小程序，也在 kario-jsrt 上，§5.4） | 系统应用大多还是 Go |
| 界面框架 | SwiftUI：声明式，状态变了界面自己更新 | KarioUI（§5.2）：页面是状态的函数，状态变了只重发变了的页；Go SDK 还是自己收发页面 | 增量只到整页（协议的 `update` 是整页）；没有可复用的自定义组件库 |
| 开发工具 | Xcode：模板、代码补全、实时预览、模拟器、调试器、Instruments、签名、上传 | `kario` 命令行、Kario Studio 扩展（编译、预览面板、调试（Node 下和真机上）、测试、打包、上传、清单编辑器）、不跑应用的视图预览（`kario preview`）、性能时间线（`kario run -profile`）、LSP、自测脚本、开发者门户、商店替开发者符号化崩溃报告 | 存盘后重新载入要重启应用进程（经墓碑回到原来的页）；视图预览只在 Node 下求值、只出静态图；性能时间线只在开发机上，渲染器的帧是开发机上按真屏幕的画法量的；真机上的调试比 Node 下少几样（§5.5 的**未实现**） |
| 模拟器 | iOS Simulator，按机型 | 开发机上的 Shell：`kario run` 一条命令起来，`-preview` 在浏览器或扩展的面板里看像素界面，`-device` 选形态，`-sku` 按 SKU 选屏幕、键盘、内存档（扩展里有选单） | 预览面板里没有机身皮肤（`kario preview -skin` 的静态图有）；SKU 的硬件（灯、基带、NFC、指纹）不模拟 |
| 文档和规范 | Developer Documentation、Human Interface Guidelines、示例代码、WWDC | 开发者接口文档（22）、应用规范（33，带正反例截图）、教程（34）、键盘交互（10）、设计系统（26）、示例应用；开发者门户里有这些文档的网页版（§4） | 截图只有一种机型；门户里的文档是静态页，没有搜索，不带 WWDC 那样的视频 |
| 测试 | XCTest、UI 测试、TestFlight | `kario test`（Node 里对着假 Shell 跑应用自己的用例）、自测脚本（按键 + 等文字）、兼容测试、分批发布 | 假 Shell 的焦点和按键是简化的；没有 TestFlight 那样的测试版渠道 |
| 上架 | App Store Connect、审核指南、隐私标签 | 开发者门户、`kario publish`、人工审核、权限用途、备案号、审核指南（33 §2）、隐私标签（33 §3，能上网的应用必填，网页目录也显示） | 不靠权限拿到的数据（`usage`、`identifiers`）系统核实不了，只能审核员核对 |

## 2. 为什么是 TypeScript

- **人最多**：国内会 TS / JS 的开发者最多，写过微信小程序的人更是现成的（我们的小程序本来就是 TS）；鸿蒙的 ArkTS 已经证明这条路在手机上走得通。
- **工具现成**：TypeScript 的补全、跳转、诊断是 VS Code 自带的，只要给出类型定义（`@kario/sdk`）；调试器也是自带的。一个扩展就能装好全部工具，不用另装 1 GB 的 Swift 工具链。
- **安全更好讲**：两层沙箱。引擎里只有 `kario.*`（没有 `fs`、`net`、`child_process`），进程外面还是现在的沙箱（命名空间、seccomp、Landlock、按应用的防火墙）。只用 TypeScript 的应用不带本地代码，审核只看 JS 和清单；带原生模块的应用（P5，§5.3）照原生应用审，每个版本都审，沙箱一样。
- **和协议对得上**：KarioUI 不用自己排版——界面是语义节点，系统来画——所以比 SwiftUI 和 ArkUI 都简单：视图函数求值出节点树，和上一次比较，只发变化的部分（协议已经有 `Update`，渲染器有增量帧）。
- 设备上的引擎比较过：

| 引擎 | 大小 | 每个应用的内存 | 启动 | 调试 | 许可 |
|------|------|----------------|------|------|------|
| QuickJS-ng | 约 1 MB | 1–2 MB 起 | 快（预编译字节码） | 设备上：给引擎打的调试补丁（断点、单步、栈、变量，§5.5）；开发机上换 Node 调试 | MIT |
| Hermes | 约 3 MB | 几 MB | 很快（字节码，React Native 在用） | Chrome DevTools 协议 | MIT |
| V8 | 10 MB 以上 | 每个实例 10 MB 以上 | 快（JIT） | 完整 | BSD |
| goja（小程序原来的引擎，已换下） | 编进 Go 程序 | 跟着 Go 程序 | 要解析源码 | 无 | MIT |

  先用 QuickJS-ng：最小，好进 Yocto，2 GB 内存的手机上开几十个应用也撑得住；实测性能不够再换 Hermes。V8 太重。

一个 KarioUI 应用是这样（系统的计算器 `apps/calc/src/main.ts`，省略了按键表；一步一步的写法见 [34](34-app-tutorial.md)）：

```ts
import { Text, Tile, act, app, page, state, tr } from "@kario/ui";
import { empty, press, type Calc } from "./calc";

const calc = state<Calc>(empty, { save: "calc" });
const key = (k: string) => calc.set(press(calc.value, k));

const Main = page("calc", () => ({
  title: tr("Calculator"),
  columns: 4,
  layout: "bottom",
  focus: "k:=",
  body: [
    Text(calc.value.expr || "0", { style: "display" }),
    status(),
    KEYS.map((k) => Tile(k, { id: "k:" + k, onOk: act("press", k, () => key(k)) })),
  ],
  actions: [act("clear", tr("Clear"), () => key("C"), { key: "C" })],
}));

app(Main);
```

## 3. 开发工具：做成扩展和命令行，不另做 IDE

| 部件 | 做什么 | 对应苹果的 |
|------|--------|-----------|
| `kario` 命令行 | `kario new`（模板）、`kario run`（编、在模拟器里开起来，Node 下可以调试，`-sku` 选机型，`-profile` 录时间线）、`kario preview`（不跑应用的视图预览）、`kario manifest check`（清单按安装器的规则查）、`kario skus`、`kario test`、`kario package`（签名打包，即今天的 `phone-pkg`）、`kario publish`（交给商店审核）、`kario device`（对着开发者模式的真机：装、看日志、重新载入、重启、卸载、开终端；对着系统：推拉文件、截屏、按键打字、系统日志、端口转发、重启、旁加载系统更新、问题报告，和安卓 adb、fastboot 的对照见 [27](27-usb-typec.md) §4.1）；Go 写，内嵌 esbuild 把 TypeScript 编成一个文件，再出字节码。都有了（§5.1、§5.2） | `xcodebuild`、`xcrun` |
| Kario Studio（VS Code / Cursor 扩展） | 新建项目、编译（错误进问题面板）、运行、模拟器面板、调试、测试、打包、上传、日志（`ide/vscode`，§5.2）；选机型：按 SKU 或形态的选单（状态栏）；清单编辑器：权限、用途、隐私标签和常用字段的表单，规则只在 `kario manifest check -json` 里，扩展不另写一份；视图预览面板；带性能时间线的运行；类型定义 `@kario/sdk` 从 `ui/caps.go` 生成，接口只有一个来源 | Xcode 的界面 |
| 实时预览 | 存盘后重新载入：`kario run -preview -watch`（扩展的面板缺省开着）重新编译，Shell 像换系统语言那样重启应用，应用经墓碑回到原来的页、带着交出的状态，编不过就留着旧的（§5.2）。不跑应用的视图预览：`kario preview` 在 Node 下求值应用 `src/previews.ts` 里用 `preview()` 起名的页面（带示例状态），交给开发机的 Shell 和 phone-paint 的无头模式，按每个形态或 SKU 出 PNG（§5.2）；在 kario-jsrt 下求值**未实现** | SwiftUI Preview |
| 模拟器 | 开发机上的 Shell、服务和 phone-paint 打成一个程序，界面就是 `make preview` 那页加机身皮肤（`make skins`）；按 SKU 选屏幕、键盘（含 Fn 层）、内存档、地区（`kario run -sku`，§5.2） | iOS Simulator |
| 调试 | 模拟器里应用跑在 Node.js 下（`kario run -inspect`），VS Code 自带的 JS 调试器附加上去（开发机 ✓）；真机上：开发者模式下经信任过的数据线（[27](27-usb-typec.md) §4），`kario device install`（包或工程目录，工程用开发商店签名，先把这个开发商店的根交给手机）、`logs [-f]`、`reload`（像 `-watch` 那样经墓碑回到原来的页）、`restart`、`uninstall`、`shell`：电脑上跑 `ssh`，到手机在数据线上的地址（开发镜像的 dropbear），手机那头的 `phone-dev` 经 Shell 运行目录里的 `dev.sock` 交给 Shell；开发者模式关着、数据线没信任（或在用文件传输）、设备上不是从数据线来的 SSH 会话，一律拒绝，只碰装上去的应用，不碰系统应用；装、卸走安装器的同一套检查（不弹确认框，手机上提示一句）。开发机 ✓（单元测试，自测「开发者模式：电脑经信任过的数据线……」）；模拟器上经真的 SSH 的自测用例 ✓（QEMU）；**未上真机**。真机上下断点：Kario Studio 的「Kario: Debug on the Phone」（调试类型 `kario`，适配器 `kario dap`）经同一条数据线让开发商店签名的应用在 kario-jsrt 的调试器下重启，「Debug the App Already on the Phone」接到正在跑的应用上、不重启，断点、单步、调用栈、变量、求值都按源码映射落在 TypeScript 里（§5.5）；开发机 ✓（单元测试），**未上真机** | Xcode 调试、开发者模式 |
| 性能 | 开发机上：`kario run -profile` 录下应用的 CPU、内存、发来的页面、能力调用（带耗时）、按键到页面更新的延迟、ANR 探测、渲染器的每一帧（无头的 phone-paint 像真屏幕那样画一遍，各步的时间），写成 Chrome Trace Event JSON，在 Perfetto（ui.perfetto.dev）里看；Node 下另有 JS 的 CPU profile（§5.2）。真机上**未实现** | Instruments |

## 4. 规范、文档、测试、上架

- **人机界面规范**：[33](33-app-guidelines.md) §1，从键盘交互（10）、设计系统（26）、权限和用途（17）整理；§1.3 的前五行配了正反例截图：`examples/guidelines` 的页面（KarioUI 写的，反例故意做错）经开发机上的 Shell 和 phone-paint 无头出图，简体中文、英文各一套，缩到 240×320、16 色，每张 1–5 KB（`make guideline-shots`，`scripts/guideline-shots.sh`；时钟定住，同一份代码出的图逐字节相同）。只有竖屏九宫格一种机型。
- **教程和示例**：[34](34-app-tutorial.md) 从零写一个应用（列表、输入、导航、确认、存储和墓碑、能力、推送、后台任务、翻译、测试、调试、上架），成品是 `examples/karioui-notes`：推送（§7.1：`push.register`、`push` 事件、按 `id` 去重、`notified`）和后台任务（§7.2：每天提醒，`job.schedule` / `job.cancel`、`job` 事件里发通知）由 `kario test` 的用例和开发机上的自测用例「教程的示例（KarioUI 便签）」验证（从商店装、冻结时经真的网关推送、拨快时钟让任务在后台跑）。令牌用 `fetch` 交给自己的服务器（[22](22-developer-api.md) §9.4），自测对着本机回环上的测试服务器核对。
- **文档上门户**：开发者门户（kario-internal 的 `portal/`，不开源）构建时把这个仓库的 `docs/` 里的 22、29、33、34 和它们链接到的文档（共 18 篇）连同英文版转成网页，带目录、中英文切换，文档之间的链接改到门户里的页面，其余的链接到 GitHub 上的源码树，图片一起拷过去；门户首页链过去，按门户的语言打开中文或英文版。门户的浏览器测试打开教程、跟着链接到 22、切到英文、看规范的截图都加载了。门户里没有搜索。
- **测试框架**：`kario test`（[34](34-app-tutorial.md) §9）在 Node 里对着假 Shell 跑应用自己工程里的用例，按键加读屏幕，和自测脚本一个思路；同一个应用在两个引擎上跑自测：计算器在 kario-jsrt 和 Node 下都通过（§5.2）。
- **上架**：审核指南 [33](33-app-guidelines.md) §2；隐私标签 33 §3（商店应用的「详情」页、审核台和官方商店的网页目录显示；能上网的应用必填，已上架的应用有宽限期）；测试版渠道用分批发布和开发者模式。

## 5. 分阶段

| 阶段 | 内容 | 完成标准 |
|------|------|----------|
| P1 | 引擎和运行时：QuickJS-ng 进构建（开发机和 Yocto 配方）；`kario-jsrt`（引擎加协议客户端加 `kario.*` 能力接口，经 C 接口 `sdk/c`）；`kario` 命令行编 TypeScript 出包；一个 TypeScript 示例应用 | 示例应用在开发机和 QEMU 上通过自测；量出包大小、内存、启动时间，和 Go 应用比。开发机：✓（§5.1）；QEMU：自测 ✓、内存 ✓（§5.1），启动时间 ✓（开发机和 QEMU，§5.1） |
| P2 | KarioUI：视图、状态、列表、输入框、动作、软键、导航栈，增量更新；翻译走 `tr`；同一个应用能在 Node 下跑（模拟器） | 用 KarioUI 重写一个系统应用（计算器或记事本），两个引擎上自测都通过。开发机：✓（§5.2）；QEMU：✓（随整套自测） |
| P3 | 工具：Kario Studio 扩展（新建、模拟器面板、运行和调试、预览、打包上传） | 新人从安装到在模拟器里跑起自己的应用不超过 30 分钟。扩展编好、单元测试通过、`.vsix` 装得上（§5.2）；在 VSCodium 里经 @vscode/test-electron 从头点了一遍（新建、编译、清单编辑器、预览面板和按键、视图预览、性能时间线、打包，§5.2）；在真机上调试（经假的手机下断点、停住、按 TypeScript 求值、继续）也在点的过程里，量这 30 分钟**未做** |
| P4 | 规范、教程、测试框架、审核指南、隐私标签；小程序迁到同一个引擎 | 开发者门户里能看到全部文档；第三方应用用测试框架跑测试。规范、教程、测试框架、审核指南、隐私标签 ✓（33、34）；上门户 ✓（§4，门户的浏览器测试）；小程序迁移：开发机上 ✓（§5.4，每个小程序一个 kario-jsrt 进程），QEMU 上小程序的自测用例 ✓ |
| P5 | 原生模块：TypeScript 应用带的 C 接口库（arm64，随包签名），在同一个进程里给 JS 提供函数（编解码、加密、数据库、自己的网络库），审核照原生应用；要不要给大应用一个带 JIT 的引擎（V8、Hermes）按实测再定 | 一个示例应用用原生模块做一件 QuickJS 做不快的事（比如图片处理），在模拟器上量出比纯 TypeScript 快多少。开发机：✓（§5.3：`examples/imgproc` 的模糊，C 比 QuickJS 上的 TypeScript 快约 47 倍）；模拟器：✓（同一张图 C 21 ms、TypeScript 4764 ms，TCG 模拟下 229 倍）；JIT 引擎**未定** |

### 5.1 P1：开发机上做到了什么

P1 在开发机上做完了，单元测试和自测通过；QEMU 上的自测随整套通过，内存量过（§5.1）；启动时间在开发机和 QEMU 上都量过（本节末）；**未上真机**。

| 部件 | 在哪里 | 验证 |
|------|--------|------|
| QuickJS-ng 0.18.0（MIT） | 源码不进仓库：`make jsrt` 按版本和 SHA-256 下载到 `build/`，另存一份在 `~/.cache/kario`（`QJS_CACHE`）；`make build`、`make test` 有这份源码时顺带编运行时，没有（离线又没缓存）时说一句就跳过运行时和 TypeScript 示例；`make jsrt` 离线时下载失败，摘要不对时拒收。Yocto：`recipes-phone/quickjs`（meta-openembedded 没有 QuickJS 的配方），只出静态库，镜像不装它的包 | 开发机 ✓；进了镜像（静态链进 kario-jsrt），QEMU 上运行 ✓ |
| `kario-jsrt` | `jsrt/`（C），经 `sdk/c` 讲协议；`fetch` 和 `WebSocket` 链 libcurl（开发机要它的开发文件，`make doctor` 会查；镜像里本来就有，RAUC 的流式安装用它；WebSocket 只用它连上，握手和分帧自己做，[22](22-developer-api.md) §9.4）；能力回复带的文件经 SCM_RIGHTS 收下，成为 `KarioFile`、`AudioRing`（[22](22-developer-api.md) §9.2）；镜像里 `recipes-phone/kario-jsrt` 把它装到 `lib/phone/bin`，`PHONE_IMAGE_PACKAGES` 已加上 | 单元测试 ✓（假 Shell 驱动：页面、动作、按键、输入、能力调用的 Promise 和错误原文、定时器、墓碑、resync、存储及上限、存储在自己的线程上写（`src/store.c`：fsync 再慢也不耽误答复和 pong、合并、发状态和退出前等写完、SIGTERM、写失败，[22](22-developer-api.md) §9.2）、翻译回退、字节码、`std` 和 `os` 拿不到；随回复的文件和环形缓冲区，流式读 body、取消、Cookie、WebSocket 和捣乱的服务器）；自测 ✓ |
| `@kario/sdk` | `sdk/ts/`：`index.d.ts` 手写（`kario` 模块），`ui.d.ts` 由 `tools/tsgen` 从 `ui/` 生成，过期时 `make test` 失败 | ✓（用 tsc 5.9 strict 检查过示例和模板；构建里不用 tsc，不要 Node） |
| `kario` 命令行 | `cmd/kario`：`new`、`build`（esbuild 的 Go 接口，ES2023）、`package`（`pkg.Build`，密钥同 phone-pkg，或开发商店）；`run`、`test`、`publish` 是后面几个阶段加的（§5.2） | 单元测试 ✓ |
| 启动和安装 | 清单的 `runtime: "js"`、`main`；Shell 在应用的沙箱里启动 `kario-jsrt <应用目录>/main.js`（`shell/jsapp.go`）；安装器和商店审核用同一个 `jsrt.Check` | 单元测试 ✓ |
| 商店应用的字节码 | `shell/jscache.go`：装好（或更新）一个 TypeScript 应用后，系统在后台把它的模块编成字节码，放在 `<状态目录>/jscache/<应用>/`（系统的目录，0711，文件 0644，应用只能读）；编译器 `kario-jsrt --compile main.js -` 像应用一样受限（应用自己的 uid、Landlock、seccomp、没有数据目录、没有网络），字节码经管道交给 Shell 写下，旁边一份记录：模块在包签名里的 SHA-256、引擎（`kario-jsrt --version`）、字节码自己的 SHA-256。每次启动前 Shell 核对这三样，还要求文件是系统写的普通文件（不是链接，别人不能写）；对上了就 `kario-jsrt --bytecode <缓存> <应用目录>/main.js`，沙箱只多放行这一个文件；对不上（改过、旧版本的、别的引擎的）就不用，这次跑源码，后台重新编；更新时先删再编，卸载时删掉。运行时这边：`--bytecode FILE` 跑那份字节码，引擎版本不对时退回源码 | 单元测试 ✓（`shell` 的 `TestJSCache`：装好后有、用的是它（源码改坏了照样起来）、改一个字节、旧版本的记录、别人可写、换成链接都不用并重新编出一样的、别的引擎的不用、更新换新、卸载删掉；`jsrt` 的 `TestBytecodeFile`）；自测 ✓（「开发者模式：……」里装好后从字节码启动、改过的不用、重编后再用、卸载后没了）；QEMU 自测 ✓ |
| 示例 | `examples/hello-ts`：便签列表、输入框、右软键「全部删除」、剪贴板、经系统选择器选一张照片（随回复的只读文件）、存储、墓碑、3 秒后消失的提示 | 自测「TypeScript 应用」✓（文本界面和 phone-paint 像素界面） |

测量：开发机（x86-64，Xeon E5-2670 v3）上照 Shell 的方式启动进程（fd 3 上的协议，`jsrt/measure_test.go`，没套沙箱），每项 20 次取中位数。内存是第一页出来 300 ms 后 `/proc/<pid>/smaps_rollup` 的 Rss 和 Pss（`status` 里的 VmRSS 对多线程的 Go 程序偏低：内核按线程攒着计数，隔一阵才合进去）。Go 应用用设备上的编法（`CGO_ENABLED=0`，`-trimpath -ldflags='-s -w'`，静态）。

| 应用 | 包 | 启动到第一页 | Rss | Pss |
|------|----|------------|-----|-----|
| hello-ts，`main.js`（1.9 KB） | 14 KB（gzip 后 7.5 KB） | 1.9 ms | 3.1 MB | 1.4 MB |
| hello-ts，`main.jsc`（3.0 KB） | （系统镜像里在模块旁边；商店应用的在系统的缓存里） | 1.8 ms | 3.0 MB | 1.3 MB |
| examples/notes（Go） | 2.5 MB（gzip 后 1.1 MB） | 3.3 ms | 4.7 MB | 4.7 MB |
| apps/calc（Go） | — | 3.3 ms | 4.8 MB | 4.8 MB |
| 合成的大应用，`main.js` 294 KB | — | 42 ms | 9.1 MB | 7.3 MB |
| 同上，`main.jsc` 558 KB | — | 6.7 ms | 5.5 MB | 3.7 MB |

- 运行时本身：`kario-jsrt` 去掉符号后 1.2 MB（x86-64，`-O3`，引擎静态链入），所有 TypeScript 应用共用一份；Go 应用每个都带自己的运行时（notes 的程序 2.5 MB）。TypeScript 应用的 Pss 小，是因为 libc 和 `kario-jsrt` 的代码页在进程之间共享，Go 的静态程序只有自己用。
- 按大小优化（`-Os`，`CMAKE_BUILD_TYPE=MinSizeRel`）：`kario-jsrt` 0.68 MB，hello-ts 启动一样快（2.2 ms / 1.7 ms），Pss 0.9 MB；只有大包的源码解析慢一些（294 KB：52 ms，字节码 7.0 ms）。设备上用哪一档等真机的数再定（QEMU 是 TCG 模拟，耗时不代表真机）；Yocto 的配方现在用它的缺省编译参数。
- 在真的 Shell 里（自测最后的内存报告，套着沙箱，开发版编法）：`kario-jsrt` 运行 hello-ts 1.3 MB PSS（私有脏页 0.3 MB），`phone-app-calc` 4.5 MB。
- 小应用的源码解析不到 1 ms，字节码的好处到几百 KB 的包才看得出：294 KB 的包从 42 ms 降到 6.7 ms，内存也少了一半（不用留下解析时的结构）。字节码是源码的 1.5–1.9 倍大，又防不住恶意的写入方，所以包里不许带：系统镜像的在构建时编（开发机上用 `build/jsrt/kario-jsrt`；Yocto 里用构建机版本的 `kario-jsrt-native`：同一个 QuickJS 给 x86-64 和 arm64 写出一样的字节码，运行时用之前还核对引擎版本。以前 Yocto 里没有它，镜像里的计算器和 hello-ts 一直跑源码，是模拟器上的启动时间步骤看出来的），商店装的装好后由系统在设备上编（上表「商店应用的字节码」），都不是开发者写的。
- QEMU（aarch64，TCG 模拟）上：自测的内存用例里，一个在跑的 TypeScript 应用（`kario-jsrt` 进程）Pss 1.6 MB，同时在跑的 Go 应用天气、卡包各 4.4 MB、设置 8.0 MB；启动时间见下面的 `app-bench`。

商店应用在设备上预编译的效果（2026-11-15，同一台开发机，负载 3–4）。照 Shell 的方式起进程（`jsrt/measure_test.go`，20 次中位数，`--bytecode` 是商店应用从系统缓存启动的那条路）：

| 应用 | 模块 | 启动到第一页 | Rss | Pss |
|------|------|------------|-----|-----|
| examples/karioui-notes，源码 | `main.js` 7.8 KB | 3.0 ms | 3.5 MB | 1.6 MB |
| 同上，`--bytecode`（13.7 KB） | — | 2.1 ms | 3.4 MB | 1.5 MB |
| examples/notes（Go，设备上的编法） | — | 3.6 ms | 4.7 MB | 4.7 MB |
| 合成的大应用，源码 | `main.js` 473 KB | 56 ms | 9.1 MB | 6.6 MB |
| 同上，`--bytecode`（691 KB） | — | 6.7 ms | 5.6 MB | 3.7 MB |

- 编译本身在后台、装好以后：7.8 KB 的模块不到 10 ms，473 KB 的 60 ms、峰值 9.9 MB；启动前的核对（读记录和签名、对字节码的 SHA-256）在启动时间里，小应用看不出来。
- 在真的 Shell 里（自测用例「启动时间：……」的 `app-bench`，从 Shell 打开到收到第一页，套着沙箱启动器，5 次中位数；开发机，非 root）：karioui-notes 源码 14.6 ms、字节码 14.0 ms，Pss 1.6 / 1.5 MB；系统镜像的 hello-ts（字节码）13.2 ms、Pss 0.9 MB；Go 的 notes 15.5 ms、Pss 4.0 MB。比上表多出的十来毫秒是 Shell 这边：经 `phone-sandbox`（`kario-system`，一个 Go 程序）起进程、建套接字、等 hello，两种运行时一样。
- QEMU 上怎么看：`make yocto-selftest` 跑到「启动时间：……」时控制台打印四行 `PHONE-APP-BENCH: <标签> <应用>: start <中位数> ms (<最小>–<最大>, 5 runs) Rss … kB Pss … kB bytecode yes|no`（`ts-store-source`、`ts-store-bytecode`、`ts-system-bytecode`、`go-store`），日志在 `$(WORK)/build/qemu-selftest.log`，`grep PHONE-APP-BENCH` 就是。QEMU（aarch64，TCG 模拟，2026-11-15）上：karioui-notes 源码 390 ms、字节码 374 ms，Pss 0.88 / 0.81 MB；系统镜像的 hello-ts（字节码）337 ms、Pss 0.66 MB；Go 的 notes 382 ms、Pss 3.97 MB。TCG 下几种都在 340–390 ms，大头是模拟出来的进程启动和沙箱，不代表真机；内存的差别和开发机上一样。真机的数**未量**。

### 5.2 P2–P4：开发机上做到了什么

单元测试和自测在开发机上通过；QEMU 上的自测通过（计算器换成 TypeScript 以后，设备上的自测靠 `kario-jsrt` 跑它），**未上真机**。

| 部件 | 在哪里 | 验证 |
|------|--------|------|
| KarioUI（`@kario/ui`） | `sdk/ts/karioui/index.ts`：组件（`Text`、`Item`、`Tile`、`Input`、`Button`、`Progress`、`Image`、`Doc`、`Media`、`Web`、`Drawing`、`List`）、动作 `act`、页面 `page`、导航栈 `nav`、状态 `state`（`store` 进存储，`save` 交给墓碑）、生命周期和事件（`app`）、`tr`；状态一变重画栈里的每一页，只把 JSON 变了的页发出去（`update`）；离开前台时把页面栈、页面参数和要保存的状态交给墓碑，回来时照原样重建。`kario build` 用 esbuild 插件把它打进 `main.js`（树摇，只留用到的），项目里不装 npm 包 | 计算器、教程示例、模板的 `kario test` ✓；tsc 5.9 strict ✓ |
| Node 下的 `kario` 模块 | `sdk/ts/node/`：`runtime.mjs` 照 `jsrt/src/kario.c`、`dispatch.c`、`store.c` 写（页面栈的镜像和 resync、能力的 Promise 和系统的错误原文、存储和 1 MB 上限、存储在后台写和合并、`flush()`、墓碑的 64 KB 等写完才发、翻译的回退、ping、一条消息跑完它引出的微任务才接下一条）；`kario.mjs` 讲 fd 3 上的套接字（有 `kario-native.node` 时经它的读线程，回复带的文件才到得了），存储经 libuv 的线程写，连接关掉和 SIGTERM 时等写完再退出，`console` 写进日志也给调试器；`net.mjs` 是 fetch 和 WebSocket，`files.mjs` 是随回复的文件；`run.mjs` 装导入钩子，`main.js` 只能导入 `kario`；语言表由 `tools/tsgen` 生成 | `jsrt/node_test.go`：`jsrt` 的运行时用例在 Node 下同样通过（逃逸用例除外：Node 不是沙箱；随回复的文件要 `kario-native.node`） |
| `kario run` | `cmd/kario/run.go`：编（带源码映射）、写出 Node 运行时，启动 `phone-shell --dev-app <build> --dev-runner [...]`；`-engine jsrt` 换成 kario-jsrt；`-preview`、`-inspect`、`-brk`、`-device`、`-sku`（下面「按 SKU 选机型」）、`-profile`（下面「性能时间线」）、`-lang`；`-watch`（要 `-preview`）存盘后重新编译，发 SIGUSR2，Shell 重启开发者应用（`shell/devapp.go` 的 `ReloadDev`）；清单写 `exec` 的应用（C、C++、Go 写的）不编，目录照原样交给 Shell，Shell 跑目录里的那个程序（`appExec`，系统里同名的程序不顶替它）；找 Shell 时 `make build` 摆好的 `build/usr/bin/phone-shell` 在 `build/bin/phone-shell`（`go build` 顺带编出的，前缀下没有系统应用和渲染器）之前；Node.js 旧于 22.15 时直说版本 | 单元测试 ✓（`TestNativeProgram`、`TestLookShell`、`TestFindNodeVersion`，`shell` 的 `TestDevAppProgram`）；`TestRunNativeApp`：Go 写的应用经真的 `kario run -preview -profile`，时间线里有它的页面 ✓；C++、Go 应用和带 C / C++ / Rust 原生模块的 TypeScript 应用照 [35](35-quickstart.md) 在开发机上跑过 ✓；`-watch` 在开发机的 Shell 里手动验证过：计算器输到「1+2」，改了标题存盘，约 40 ms 后新版本带着「1+2」回来；编不过时旧的照跑 |
| Shell 里的开发者应用 | `shell/devapp.go`：`--dev-app` 的目录排在系统应用后面（ID 相同就顶替它）、开机就打开；`--dev-runner` 在沙箱外启动它，调试器才连得上；用设备的服务时 `phone-shell` 不接受 `--dev-runner` | 自测（下一行）✓ |
| 计算器 | `apps/calc`：TypeScript + KarioUI 重写，换下了 Go 版（Go 版删了，git 里还有）；算到一半的式子交给墓碑（算完的不留；Go 版没有）；出错后式子留着可以改（Go 版接着按键就被清掉）；数字照 Go 的 `strconv.FormatFloat` 格式化（精确值舍入到偶数）；镜像里有字节码，Yocto 用 `KARIO_CLI` 编它 | `kario test` 8 个用例 ✓；自测里用到计算器的用例在 kario-jsrt 下 ✓、换成 Node（`--dev-runner`）也 ✓；新用例「KarioUI 计算器」（墓碑）在两个引擎下都 ✓ |
| `kario test`、`@kario/test` | `cmd/kario/test.go`、`sdk/ts/kariotest/index.ts`、`sdk/ts/node/test.mjs`、`fake.mjs`：应用的 `main.js` 跑在 Node 下同一个运行时里，另一头是假 Shell；用 node:test 跑（一个进程，报错的位置映射回 `.ts`）。应用的 WebSocket 连到测试自己扮演的服务器（`sdk/ts/node/wstest.mjs`，[22](22-developer-api.md) §9.4） | `go test ./cmd/kario` ✓（模板的用例通过、失败时报错、`-run`；`TestKarioTestNetwork`：WebSocket 的握手、两个方向的消息、两边先关、被拒、断开，`FormData` 经 `Request` 发出） |
| `kario publish` | `cmd/kario/publish.go`：`POST /v1/submissions`（`?rollout=`），和 `phone-pkg submit` 一样 | 单元测试 ✓：假的商店，加上商店后端的开源核心 `appstore`（`cmd/kario/store_test.go`：新应用待审、目录带隐私标签、分批更新、多发一类数据又进审核）；开发机上对官方商店 `kario-storesrv` 手动跑过一遍（注册、核验、`kario package`、`kario publish`、审核、目录） |
| 隐私标签 | `ui/privacy.go`：类别、用途、`CheckPrivacy`，`kario build`、`shell/install.go`、`appstore` 都调它；目录条目带 `privacy`，分批发布时旧版本带旧标签（`pkg.OfferedApps`）；商店应用的「详情」页 `apps/store/privacy.go`；审核台（不开源）加了一列 | `ui`、`pkg`、`appstore`、`apps/store`、`cmd/kario` 的单元测试 ✓；门户的浏览器测试 ✓；设备上的「详情」页**未跑**（自测里没有商店后端） |
| 教程示例 | `examples/karioui-notes`（[34](34-app-tutorial.md)）：便签，加推送（服务器发来的便签）和后台任务（每天提醒） | `kario test` 10 个用例 ✓；自测「教程的示例（KarioUI 便签）」✓（开发机） |
| Kario Studio | `ide/vscode`：命令（新建、编译、终端里运行、预览面板、调试、停止、测试、打包、上传、写调试配置、选机型、编辑清单、视图预览、带性能时间线运行、在真机上调试）、调试类型 `kario`（真机上，适配器是 `kario dap`，§5.5）、设置 `kario.*`，只调 `kario` 命令行；开发依赖 typescript（Apache-2.0）、@types/vscode 和 @types/node（MIT）、@vscode/test-electron（MIT，只给编辑器里的端到端测试用；它连带的包是 MIT、ISC、Apache-2.0，jszip 是 MIT 或 GPL-3.0 二选一、pako 是 MIT 加 Zlib），带 lockfile，都不进 `.vsix`；`.vsix` 由 `scripts/vsix.mjs` 写，不用 @vscode/vsce（它依赖的 @vscode/vsce-sign 不是开源许可）。端到端测试 `npm run e2e`（`scripts/e2e.mjs`、`test/e2e/index.ts`）：编辑器用 VSCodium（VS Code 源码的 MIT 许可构建；微软的 VS Code 二进制不是开源许可，所以不用），按版本下载到 `~/.cache/kario/vscodium`、核对发布页的 SHA-256，只给测试用、不随任何东西发出去；没有显示器时自己在 xvfb-run 下重跑；用扩展自己的命令走一遍，新建项目时的对话框由测试代答 | 不靠 VS Code 的节点测试 16 个 ✓；`.vsix` 用 Cursor 的服务端程序装进一个空的扩展目录 ✓，也经 SSH 远程装进了开发机上在用的 Cursor 3.29（`cursor --install-extension`，扩展宿主的 `PATH` 里找得到 `make install-kario` 放的 `kario`）✓；端到端：开发机（RHEL 8，Xvfb）上 VSCodium 1.143.17232 里 8 步全过 ✓（新建、编译、清单编辑器打开、预览面板出帧且按 OK 后帧变了、视图预览出图、性能时间线写出、在真机上调试、打包）。在真机上调试这一步用扩展自己的「Debug on the Phone」：手机是 `cmd/kario` 测试里的假手机（`TestE2EPhone`，`e2e.mjs` 用 `go test` 起它，把 `kario dap` 要的 `KARIO_SSH` 等环境交给编辑器的扩展进程），装上的应用跑在这棵树编的 kario-jsrt 的调试器下；在 `src/main.ts` 的 `app(Main);` 下断点，停在那一行，在帧里求值 `(count.value as number) + 1` 得 1，继续后应用出了页面；清单编辑器只查到打开，表单里的点选没在编辑器里点 |
| 按 SKU 选机型 | `tools/sku.py sims` 从 `sku/` 生成 `cmd/kario/skus.json`（过期时 `tools/sku_test.py` 失败），`kario` 内嵌它；`kario skus [-json]` 列出；`kario run -sku <id>` 写一份 sku.json 交给 `phone-shell --sku`（`shell.SimulatedSKU`），Shell 照镜像里的 sku.json 用它：形态的屏幕和键盘、全键盘的 Fn 层、内存档（网页视图的内存上限和缓存档）、地区（自动选语言）。不带 SKU 的 `id`（有 `id` 的 Shell 当自己在手机上，关机会真的关掉开发机；`--sku` 读进来的文件就算带着 `id` 也去掉），也不带灯、背光、基带、NFC、指纹这些板子上的硬件。扩展的「Choose the Device」列出 SKU 和三种形态，存进工作区的 `kario.sku` / `kario.device`，状态栏显示 | `go test ./cmd/kario`（`TestSKUs`、`TestRunSKU`、`TestListSKUsJSON`）、`shell` 的 `TestLoadSKU`、`tools/sku_test.py`、扩展的节点测试 ✓ |
| 视图预览（`kario preview`） | `cmd/kario/preview.go`：应用在 `src/previews.ts` 里用 KarioUI 的 `preview(名字, 页面, { params, state })` 给页面配示例状态（`kario build` 不打包这个文件，模板里有一份）；`kario preview` 把 `src/main.ts` 和它打成一个模块，在 Node 下对着一个没人驱动的假 Shell 求值（`sdk/ts/node/preview.mjs`，应用不启动，页面只求值），得到每个预览的页面；再按每个形态或 SKU（`-device`、`-sku`，缺省三种形态）起一次 `phone-shell --dev-app … --render-pages pages.json --gui phone-paint --pixel-dir …`：Shell 把页面当开发者应用的页面显示（`shell/viewpreview.go` 的 `ShowDevPage`，像墓碑里的旧页面那样只显示，没有进程），phone-paint 无头出图，写到 `.kario/preview/<形态或 SKU>/<预览>.png`（`-skin` 另出带机身的）；`-json` 列给扩展。扩展的「Preview the Views」面板显示这些图，存盘就重画。取舍：预览靠应用导出页面和状态；预览依次跑，状态不自动复原，每个预览自己设好页面读的全部状态；在 kario-jsrt 下求值**未实现** | `TestPreviewEval`（两个示例状态出两个不同的页面，出错的预览报名字和原因，应用自己的包里没有预览）；`TestPreviewImages`（用这棵树编的 phone-shell 和 phone-paint：bar-t9 出 480×640、qemu-qwerty 出 640×480，图不是空白，两个状态的图差好几百个像素）✓ |
| 性能时间线（`kario run -profile`） | `shell/trace.go`：`phone-shell --profile FILE` 把开发者应用和 Shell 之间的事按发生的顺序写成 Chrome Trace Event 的 JSON 数组：每 250 ms 一次进程的 CPU（`appCPUTime`，电量提醒用的那个数）和内存（`appMemory`，低内存回收用的那个数）计数；应用发来的每一页（push、update……，带 Shell 处理它的时间、节点数、字节数）；能力调用从请求到回答（异步事件，带成败）；Shell 发给应用的动作到应用下一页之间的延迟；ANR 探测的 ping 到 pong（超过 ANR 时限另记一个 ANR 事件）；生命周期；渲染器的帧：录时间线时 Shell 在给渲染器的 `hello` 里要 `timing`（[23](23-renderer-protocol.md) §3），无头的 phone-paint 每收到一帧就像真屏幕那样再画一遍（只画变了的部分、帧里的动效每 16 ms 一步、转场的每一步，就是像素自测量动效的那套 `paint/src/timing.rs`），回一条 `drawn`，时间线里另一个进程「renderer」的「frames」上一帧一条，从 Shell 发出到渲染器回报，里面依次是读入、排版、画的每一步、转场的每一步，带字节数、是不是增量、当时的界面。Shell 这边只加了两行钩子（`guard.go` 收消息处、`core.go` 发消息处），帧在 `gui.go` 记。事件一条一写，Shell 被杀也留下能读的文件；`kario run` 结束时把它整理成 JSON 对象格式（`cmd/kario/profile.go`），说出文件在哪。Node 下还给应用加 `--cpu-prof`，Shell 停下时应用照常退出、写出 `.cpuprofile`（VS Code、Chrome DevTools 能开）；带 `-preview` 时 Shell 不在终端的进程组里，Ctrl-C 由 kario 转成 SIGTERM，好让它们收尾。扩展的「Run with the Performance Timeline」停下后给出打开 Perfetto、文件、CPU profile 的按钮。渲染器的帧的时间是开发机上的，不是手机的；画真屏幕的 phone-paint 不回报帧，真机上**未实现** | `shell` 的 `TestTracer`（各种事件、在处理请求时就回答了的能力调用、没收尾的文件）；`TestTracerFrames`（录时间线时才要 `timing`，渲染器回报的帧在自己的轨道上，各步依次在帧里，没回报的帧不写）；`TestFinishTrace`；`TestRunProfile`（真的 `kario run -preview -profile`：按三次 OK，按 Ctrl-C 那样停下，时间线里有 CPU、内存、页面、动作到更新、`clipboard.set` 调用、ping、启动、phone-paint 的帧和它们的排版、画的步骤，Node 写出了 CPU profile）✓。在 Perfetto 里打开**未验证**：Perfetto 的 `trace_processor` 预编译程序要 glibc 2.34，这台开发机（RHEL 8）是 2.28 |
| 清单检查和编辑器 | `cmd/kario/manifest.go`：`kario build` 的清单检查改成列出全部问题、每个带字段（`id`、`permissions.<权限>`、`purposes.<权限>`、`privacy`、`locales`……），build 仍报第一个；`kario manifest check [-json] 目录\|文件\|-`（`-` 读标准输入，扩展拿没存盘的内容来查）出这些问题，`-json` 另带表单要的表（可以声明的权限和标签、哪些要用途、各打开哪类数据、用途的长度上限、隐私标签的数据类别和用途、语言）。扩展的「Edit the Manifest」是 `manifest.json` 的自定义编辑器（可选，不顶替文本编辑器）：id、名字、版本、描述、图标、语言、权限的勾选和用途、隐私标签的表格；每改一处写回 `manifest.json`（表单不认识的字段原样留着），再跑 `kario manifest check -json -`，问题显示在对应的字段旁边 | `TestManifestCheck`（模板通过、坏清单的每个问题落到对的字段、build 照旧拒收、图标查文件）；扩展的节点测试（表单读写、问题归位、页面），其中一个用这棵树编出的 `kario manifest check -json` 查表单写出的清单 ✓ |
| 规范、教程 | [33](33-app-guidelines.md)、[34](34-app-tutorial.md)；规范的截图 `examples/guidelines`、`scripts/guideline-shots.sh`、`tools/pngshrink`，图在 `docs/img/33/` | 教程里的代码摘自测试过的示例；截图脚本连跑两遍出的图逐字节相同 |

测量：和 §5.1 同一台机器、同一个测法（`jsrt/measure_test.go`，20 次取中位数），这次机器上同时在跑别的构建和测试，起伏比 §5.1 大（hello-ts 这次 2.0 ms，§5.1 是 1.9 ms）。

| 应用 | 包 | 启动到第一页 | Rss | Pss |
|------|----|------------|-----|-----|
| 计算器（TypeScript + KarioUI），`main.js`（7.2 KB） | 21 KB（gzip 后 11 KB） | 3.6 ms | 3.3 MB | 1.6 MB |
| 同上，`main.jsc`（12.4 KB） | （只在系统镜像里） | 2.6 ms | 3.2 MB | 1.5 MB |
| 同上，Node.js 24（`kario run`、`kario test` 用的） | — | 51 ms | 53 MB | 46 MB |
| 计算器（Go，换下来之前的） | 程序 2.5 MB | 3.7 ms | 4.8 MB | 4.8 MB |
| hello-ts（不用 KarioUI），`main.js`，同一次 | 17 KB | 2.0 ms | 3.1 MB | 1.4 MB |

- 换成 TypeScript 以后计算器的 Pss 少了约 3.2 MB（Go 程序的代码页只有自己用，`kario-jsrt` 的是共享的），启动差不多，字节码快 1 ms。
- KarioUI 的代价：计算器的 `main.js` 里约 5 KB 是 KarioUI；比 hello-ts 慢约 1.6 ms（源码）、Pss 多 0.2 MB，字节码把一半追回来。
- Node 只在开发机上用：几十 MB 的内存和 50 ms 的启动对调试无所谓，所以设备上一直是 kario-jsrt。
- 真机上的数**未量**；QEMU 上只量了内存（§5.1）。

### 5.3 P5：原生模块

开发机上做完了，单元测试和自测通过；QEMU 上的自测和测量（完成标准）✓（下面的表），**未上真机**。TypeScript 写界面和逻辑，慢的部分（编解码、图片处理、加密、数据库、自己的网络协议）用 C、C++ 或 Rust 写成共享库，随包签名，在同一个进程里给 JS 调用。怎么写见 [22](22-developer-api.md) §9.3。后来又加了四样（下表最后四行）：C++ 和 Rust 的封装、模块加载完以后封住可执行内存、符号化的原生调用栈、一个包带几种架构而商店按设备瘦身；这四样在开发机上测过（x86_64）；QEMU 上只有封住可执行内存随「原生模块」用例跑过（arm64 上过滤器装上了，封不上时运行时不跑应用，模块照常工作；被封的那些调用在 arm64 上没专门测），另外三样 QEMU 上**未跑**，都**未上真机**。

| 方面 | 做法（和先前的设计不同的地方在后面说） |
|------|------|
| 接口 | `sdk/c/include/kario_native.h`，带版本（1.0）：库导出 `kario_native_abi` 和 `kario_native_init`（`KARIO_NATIVE_MODULE`）；运行时把一张函数表（`kario_api`，开头是表的大小，以后的次版本只往后加）交给 `init`，模块从它登记名字、取参数、设结果。模块不链接运行时的任何符号，碰不到 QuickJS 的对象和 `JSValue`：QuickJS-ng 的 C 接口在版本之间会变（0.18 的 `JS_NewArrayBuffer` 已经换成 realloc 回调、多了最大长度，不是 QuickJS 原来的签名），`JSValue` 的布局随引擎的编译选项变，签了名的包却要在系统升级引擎以后照样能用；同一个 `.so` 也因此能在 Node 里加载（下面「开发机」），以后换 Hermes 也不用重编。代价是每个参数多一次函数表调用，值只有数字、布尔、字符串、字节和 JSON，原生代码不回调 JavaScript |
| JS 这边 | `import { blur } from "native:imgproc"`：普通的 ES 模块，名字就是模块导出的名字，外加每个模块自带的 `on(event, handler)`；`kario build` 把 `native:*` 留给运行时；要降级就 `await import("native:…")` 接住错误；类型由开发者写 `declare module` |
| 线程 | 普通函数在引擎线程上同步执行；`KARIO_ASYNC` 的在 `kario-jsrt` 的工作线程上执行（每个应用至多 4 个，按需开，多的排队），结果经一个 eventfd 回到消息循环，和协议的套接字一起 poll，在引擎线程上兑现 Promise；模块自己的线程只能 `emit`（复制、排队至多 1024 个，同一个 eventfd），交给模块的 `on` 处理函数。进程和线程合计仍是 cgroup 的 128，CPU 照旧由 cgroup 管 |
| 内存 | 同步调用借给 C 的是 JavaScript 的内存本身（就地改，不复制）；异步调用拿副本（JavaScript 同时在跑）；结果的字节由运行时分配、成为 `ArrayBuffer`，不再复制。模块从不卸载 |
| 包和清单 | 清单 `"native": ["imgproc"]`，库在 `lib/<架构>/imgproc.so`，架构 `arm64`、`x86_64`，权限 0644；`jsrt.Check`（安装器、商店、`kario build` 同一份）核对名字、位置、ELF（位数、字节序、机器、共享库、两个入口、接口版本），`lib/` 下别的文件和包里别处的 ELF 都拒收，要可执行栈的库也拒收（下面「封住可执行内存」）；安装器要本机架构，商店要 `arm64`，`kario package` 也要 `arm64`（`-arch arm64,x86_64` 指定放进哪几种，每种都得有）；一个包可以带几种架构，商店按设备只发它那种（下面「瘦身」） |
| 加载和拒收 | `kario-jsrt`（`jsrt/src/native.c`）：名字合法、清单声明了、`realpath` 后还在包里（符号链接、包外的文件不行）、普通文件、ELF 是本机架构、不要可执行栈，再 `dlopen`（`RTLD_NOW`）、核对接口版本、调 `init`；每一种不对都有一句说清原因的错误（单元测试逐条覆盖，Node 下同样的消息）。清单声明的模块在应用的代码跑之前按清单的顺序全部加载（`init` 在那时跑），加载不了的记下原因，`import` 它时抛同一句（静态 import 照旧让应用起不来，`import()` 照旧能接住）；Node 下仍是 `import` 时加载 |
| 沙箱和审核 | 同一个进程、同一个沙箱，能力照旧只经 `kario.*`：原生代码能做的不超过今天用 `sdk/c` 写的应用（hello-cpp）。带 `native` 的应用每个版本都进人工审核，照原生应用审（33 §2.2）；安装确认框写明含原生代码和模块名，商店「详情」页和目录（`native`）也写；更新新带了原生模块时不在后台自动装 |
| 开发机 | 两种都能跑：`kario run`（缺省 Node）和 `kario test` 经 `kario-native.node`（`sdk/ts/node/native.c`，Node-API；Node-API 是 node 程序的稳定 C 接口，函数自己声明，不要 Node 的头文件，`kario` 用本机 C 编译器现编、按源码摘要缓存；它也替 Node 读应用的套接字，随回复的文件才到得了，所以本机有 C 编译器时 `kario run`、`kario test` 总编它）加载同一个 `.so`，TypeScript 照样在 VS Code 里断点；`kario run -engine jsrt` 在 kario-jsrt 里跑，和设备一样，C 代码用 gdb 附加。取舍：Node 下能调 TypeScript，但 V8 的 JIT 让 TypeScript 快得多（下表），量性能、看崩溃行要用 kario-jsrt；Node 下原生代码崩溃没有调用栈（V8 自己占着 SIGSEGV），不封可执行内存（V8 一直在生成机器码），也没有沙箱 |
| 编译 | `kario build` 不编 C：开发者用自己的工具编（开发机 `cc`，手机用交叉编译器或 Yocto 导出的 SDK），放进项目的 `native/<架构>/`（`-native DIR` 指别处）；`kario build` 缺 `arm64` 时提醒 |
| 设备 | `phone-fw` 配方用目标的 `${CC}` 编示例的模块，`scripts/stage.sh` 把示例连同 `lib/arm64/imgproc.so` 放进 `share/phone/examples/imgproc`（包的源，像 examples/notes）；自测用例「原生模块」用开发商店签名、从商店装、比较两边，并把应用日志里的数打印成 `PHONE-APP-LOG` 一行；`phone-capi` 装 `kario_native.h`，`sdk/c` 的 CMake 现在也把 `kario_native.hpp` 装进 `-dev` 包（镜像还没重建，**未上真机**） |
| C++ 和 Rust | C++ 是 `sdk/c/include/kario_native.hpp`（C++17，只有头文件，底下还是 `kario_native.h`）：拿 `kario::call&` 的任何函数或 lambda 都能导出（`m.function("add", add)`，异步的加 `kario::async`），返回值就是结果（`void`、`bool`、数字、字符串、`kario::json`、`std::vector<uint8_t>`）；`call.arg<T>(i)` 按类型取参数（`double`、整数类型要在范围内的整数、`bool`、`std::string_view`、`kario::bytes`、`kario::json`、`std::optional<T>`），不对就抛 `kario::error`（「`typed: argument 1 is a string, not a number`」）；函数抛出的任何异常都在边界上接住，`what()` 成为 JavaScript 拿到的 `Error`（异步的就是 reject），不是 `std::exception` 的也接住，异常不进运行时；`KARIO_NATIVE_CPP_MODULE` 里抛异常就拒收模块，原因写进日志。Rust 是 crate `sdk/rust/kario-native`（Apache-2.0，没有依赖，`Cargo.lock` 提交了，`cargo build --locked --offline` 就能编）：`kario_native::module!`，`Module::function` / `function_async`，`Call::arg::<T>()`，函数返回 `Result<R, Error>`，`Err` 和 panic 都在边界上变成 `Error`（panic 不跨 FFI 展开），`Emitter` 给模块自己的线程用；模块是 `cdylib`。两边各有一个和 C 的探针一样的模块（`jsrt/testdata/native/probe.cpp`、crate 的 `examples/probe.rs`），在 kario-jsrt 和 Node 里跑和 C 同一套用例，再加封装自己的（`TestNativeCpp`、`TestNativeRust`、`TestNodeNativeCpp`、`TestNodeNativeRust`）。为 arm64 交叉编译这两种都没试过（**未上真机**） |
| 封住可执行内存 | 模块都加载完、应用的代码还没跑，`kario-jsrt` 给所有线程装上一个 seccomp 过滤器（`SECCOMP_FILTER_FLAG_TSYNC`；叠在 Shell 沙箱 `sandbox/` 的过滤器上，两者各管各的）：带 `PROT_EXEC` 的 `mmap`、`mprotect`、`pkey_mprotect`，带 `SHM_EXEC` 的 `shmat`，`remap_file_pages`，带 `READ_IMPLIES_EXEC` 的 `personality`，`execve` 和 `execveat`（memfd 或下载的文件就是包外的代码）都返回 `EPERM`，应用接着跑；x86_64 上 x32 的系统调用一律拒绝。封之后 glibc 的 `dlopen` 也失败，所以模块先全部加载，模块链接的库随它一起加载。封之前查一遍 `/proc/self/maps`：模块的 `init` 留下的可执行内存要是以后还能变（可写又可执行、共享、匿名、文件已删、在数据目录里），应用起不来。封了以后照样能开线程、`pthread_exit`（kario-jsrt 链接了 libgcc_s，glibc 不用再加载它）、C++ 抛异常、Rust panic，单元测试都覆盖（`TestNativeSealed`；Node 下同一个探针都能做到，`TestNodeNativeUnsealed` 是对照）；名字解析只剩 libc 自带的（glibc 2.34 以后的 files 和 DNS），NSS 模块加载不了。没声明原生模块的应用不封（`/proc/<pid>/status` 的 `Seccomp: 0`，测试核对）。挡不住的：经 `/proc/self/mem` 写自己的代码页（seccomp 看不到路径；设备上 Landlock 让 `/proc` 只读，挡得住，开发机上没有 Landlock）；封之前模块映射好的只读文件页里是什么，审核看。aarch64 的过滤器**未上真机**，开发机只测了 x86_64 |
| 崩溃和调用栈 | 有原生模块的应用遇到致命信号（SIGSEGV、SIGBUS、SIGILL、SIGFPE、SIGABRT），处理函数在备用信号栈上（引擎线程和每个工作线程各一个）写进日志：模块和函数（或「它自己的线程」）、信号和地址，再每帧一行（所在的库、相对库文件的偏移、build-id，至多 48 帧），最后一行收尾，然后照样死于那个信号。处理函数只用 `write(2)`，库的地址和 build-id 在加载时记好；回溯用 libgcc 的 `_Unwind_Backtrace`（按每个库的展开表，帧指针不用留），这是唯一不在 POSIX 信号安全清单上的一步，封之前先跑过一次，不会再加载东西。Shell 的崩溃记录多一项 `backtrace`：日志末尾、信号和进程的死因对得上的那一段，整段留下，不受「最后 20 行」的限制；发给开发者时一起发（确认框多一句），商店的 `/v1/crashes` 照收（至多 52 行）。`kario symbolize -so <没剥符号的库或目录> [崩溃]` 读日志、崩溃记录或商店的列表，按 build-id 找库（库没有 build-id 时按名字），用 Go 的 `debug/elf` 和 `debug/dwarf` 把偏移变成函数和 `文件:行`（内联的给最里层的函数），不要外部工具；build-id 对不上的库不拿来用，会说缺哪个。测试：探针故意崩溃，引擎线程和工作线程上都核对栈的头两帧落在 `probe.c` 的哪两行、第三帧是 kario-jsrt（`TestNativeCrash`）。模块自己的线程没有备用栈，栈溢出时没有这几行；aarch64 上的回溯**未上真机** |
| 瘦身 | 一个包可以带 `lib/arm64/` 和 `lib/x86_64/`。商店审核时算出每种架构的设备拿到的包（`pkg.Thin`：同样的条目、同样的顺序，只去掉别的架构的 `lib/<架构>/`，同一个包每次一样）的大小和摘要，写进目录的 `thinned`（分阶段发布的上一版也带）；`GET /v1/packages/<应用>/<版本>?arch=<架构>`（增量也一样）只发这一种，商店应用按本机架构请求、用那一份的摘要核对。签名照旧是开发者签的那份：`pkg.OpenFor` 只放过别的架构的 `lib/<架构>/` 缺席，在的照样核对，别的一律不行（本机架构的库缺了、别的文件缺了或变了都拒收）；安装器只解出本机架构的文件，开机核对（`pkg.VerifyDirFor`）和增量重建（`pkg.ApplyDeltaFor`，结果和商店瘦身的那份逐字节相同）也按本机架构。单元测试在 `pkg`（`TestThin`）和 `appstore`（`TestThinnedDownload`）；带两种架构的包没有自测用例，QEMU 上**未跑** |

和先前的设计不同的地方：JS 这边从 `native("imgproc")` 改成 ES 模块 `native:imgproc`（静态可查、能树摇、类型好写）；库从 `native/<架构>/lib<名字>.so` 改成 `lib/<架构>/<名字>.so`，架构名用 `arm64` 而不是 `aarch64`（和 Apple、Android 的叫法一致）；模块不调运行时导出的符号，改成拿函数表（`kario-jsrt` 是 `-Wl,--gc-sections` 链出来的，不导出符号；同一个库还要能进 Node）；加了布尔、`emit` 和 `on`（模块自己线程的结果回到 JS）；Node 下的加载现在就做了，不是「以后」；kario-jsrt 里的模块不再在第一次 `import` 时加载，而是应用启动时全部加载，好在那以后封住可执行内存（封了以后 `dlopen` 就用不了）。

测量：开发机（同 §5.1，x86-64，Xeon E5-2670 v3；这次机器上同时在跑别的构建，负载 23–35），`examples/imgproc` 的「比较」：320×240 RGBA、半径 4 的方框模糊（两遍，边缘夹住），C 用 `-O2`，两边像素逐字节相同。原生的取 200 ms 内的平均（约 39 次），TypeScript 取 2 次的平均。

| 怎么跑 | 原生（C） | TypeScript | 倍数 |
|------|------|------|------|
| kario-jsrt（`jsrt/measure_test.go` 的 `TestMeasureNative`，照 Shell 的方式起进程，没套沙箱） | 5.18 ms | 243 ms | 47× |
| kario-jsrt，在真的 Shell 里、套着沙箱（自测用例「原生模块」，从商店装的包） | 5.21 ms | 249 ms | 48× |
| Node.js 24（`kario run`、`kario test` 用的） | 5.37 ms | 13 ms | 2.4× |
| kario-jsrt，QEMU（aarch64，TCG 模拟），自测用例「原生模块」，封住可执行内存以后 | 21 ms（10 次） | 4764 ms（2 次） | 229× |

- 带原生模块的应用启动到「模块已加载」那一页 3.2 ms，Rss 3.4 MB、Pss 1.6 MB（10 次中位数），和计算器差不多（§5.2）：16 KB 的 `.so` 和 `dlopen` 几乎不花什么。同一个应用在 Node 下 55 ms、Pss 47 MB。
- QuickJS 是解释器：逐像素的循环比 C 慢约 47 倍，而 V8 的 JIT 只慢约 2.5 倍，所以开发机上 Node 里「够快」不说明设备上够快。热循环交给原生模块，界面和逻辑留在 TypeScript，大应用先不用换带 JIT 的引擎；要不要换等真机的数。
- QEMU 上倍数更大（229×）：TCG 把每条 arm64 指令翻译成 x86-64 指令来跑，解释器的分支和间接跳转比 C 的紧循环吃亏更多，所以这个倍数不代表真机。数来自 `make yocto-selftest` 跑到「原生模块」时打印的 `PHONE-APP-LOG: com.example.imgproc: imgproc bench …`（快照 `50-native-compare` 也有）；真机的数**未量**。

还要定的、没做的：要不要给大应用一个带 JIT 的引擎，等真机的测量（封住可执行内存以后，模块自己带 JIT 也不行了）；商店收符号文件、替开发者符号化崩溃报告见 §5.6（`kario package` 不剥库的符号：要手机拿到不带调试信息的库，开发者自己剥，没剥的交给 `-native-symbols`）；`kario symbolize` 给的 C++ 函数名是 DWARF 里的短名，不带命名空间，也不还原修饰过的符号名；后加的四样里 C++ 和 Rust、调用栈、瘦身在 QEMU 上**未跑**，封住可执行内存在 QEMU 上随「原生模块」用例跑过，都**未上真机**。

### 5.4 P4：小程序迁到 kario-jsrt

开发机上做完（单元测试、小程序的 5 个自测用例通过），QEMU 上 5 个小程序用例也通过，日志里没有「只在宿主的沙箱里跑」的退路提示；Landlock 那一层、每个运行时 64 MB 的上限在 QEMU 上没逐项看，**未上真机**。小程序原来在宿主进程里的 goja（Go 写的 JS 引擎）上跑；现在和 TypeScript 应用用同一个引擎 kario-jsrt（QuickJS-ng 0.18），三个内置小程序和签名安装的示例一行没改。

| 项 | 做法 | 状态 |
|---|---|---|
| 进程 | 每个小程序一个 `kario-jsrt` 进程（像微信每个小程序一个 JS 上下文），打开时由宿主启动（`apps/miniapp/runtime.go`），关掉后（「退出小程序」、在它最下面一页按返回）留一会儿再结束（下一行）；结束时宿主发 SIGTERM，运行时先把正在后台写的存储写完（[22](22-developer-api.md) §9.2），5 秒后还在就杀掉；宿主自己退出时也等它们写完（至多 5 秒）。宿主是这个进程的「Shell」：运行时讲的还是 phone.Ui 协议，只是连到宿主，不连系统 | 单元测试 ✓（`TestStoredBeforeStop`：每次 fsync 慢 1 秒，退出后留的时间一到就结束，存储照样在） |
| 关掉后留着 | 像微信那样（微信留 5 分钟）：关掉的小程序的运行时接着跑，页面离开屏幕（`apps/miniapp/warm.go`）；再打开时宿主发 `resync`，运行时把它的页面栈原样重发（就是 Shell 重启后接管连接时的那条路），回到关掉时的那一页、状态都在，不重新启动。关掉 5 分钟后结束；同时至多留 3 个，它们的常驻内存（`/proc/<pid>/statm`）合计至多 32 MB，超了先结束关得最早的。留着的小程序什么也显示不了：提示和报错只进日志，扫码、支付这类要用户看的调用直接失败（`the mini-program is not open`），它打开、关掉的页面只记在它自己那里；运行时收到 `lifecycle` 的 background 和 foreground，页面有 `onShow`、`onHide`（打开新页、关掉上面的页时也调，照微信）和 `onUnload`。留着时运行时死了不提示，下次打开从头启动 | 单元测试 ✓（`TestWarmReopen`：第二页上点过的数、关掉时的 onHide、留着时的提示被丢掉、扫码被拒，再打开时同一个进程回到第二页，onShow 又调了一次；在最下面一页按返回也留着；`TestWarmLimits`：时间到、超过个数、超过内存时结束关得最早的，留着时运行时被杀不提示、再打开从首页起）；自测「小程序关掉后留着」✓（开发机） |
| 小程序接口 | 编进宿主的 `runtime.js` 在 `kario` 模块上实现 `Page`、`setData`、`phone.*`、`ui.*` 和 JSX 的 `h()`，`phone.t` 的规则照旧（[22](22-developer-api.md) §9.1）；宿主把小程序的代码、翻译表和 `prelude.js` 写进它的模块，代码等 `start` 到了才执行，这时内存上限已经设好 | 原有单元测试改到新引擎上 ✓ |
| 宿主核对 | 小程序的代码和 `runtime.js` 在同一个上下文里，也拿得到 `kario` 本身，所以 `runtime.js` 不算边界，规则都在宿主：页面 ID 必须带它自己的前缀，最多 10 层，只能弹自己的页；能力只有桥上的三个调用（扫码、支付、报错），按小程序声明的权限查，以小程序的名字作 origin、带它自己的用途发给系统；`reset`、`notify` 之类别的消息不转发 | 单元测试 ✓ |
| 冒充不了宿主 | 宿主到 Shell 的连接（fd 3）和其余描述符都设成 close-on-exec，运行时只有自己那条连接 | 单元测试 ✓（去掉这一条，测试就失败） |
| 数据 | 存储用 kario-jsrt 的（`mp-data/<小程序>/kario-storage.json`），没声明 `storage` 的小程序没有数据目录；原来的 `storage.json` 第一次启动时搬过去。宿主旁边有 `phone-sandbox` 时再套一层沙箱（同样的 seccomp、rlimit；有 Landlock 时只能读写自己的目录、读自己的模块），引擎被攻破也读不到别的小程序的数据 | 单元测试 ✓；开发机的内核没有 Landlock，这一层**未验证** |
| 时限 | `jsrt/src/main.c` 新加 `KARIO_JSRT_BUDGET_MS`：JS 的一轮（一条消息的处理、到期的定时器、它们留下的任务）超过 300 ms 由引擎中断，小程序接着能用，和 goja 时一样；宿主另外在有活动 1 秒后发 ping，3 秒没回就结束它（卡在引擎管不到的地方时） | 单元测试 ✓ |
| 资源 | 内存 64 MB（`RLIMIT_DATA`，引擎起来以后、小程序代码执行以前设）；一条消息最多 2 MB，一页最多 1 MB；`setData` 每 20 ms 合成一页；提示连发 5 条以后每 0.5 秒一条；没答复的调用最多 8 个，积压的消息 64 条，宿主用不了的消息 16 条，超了就结束运行时 | 单元测试 ✓（死循环、刷屏、超大页、崩溃、耗尽内存） |

测量（开发机 x86-64，`apps/miniapp` 的 `TestMeasureHost`，照 Shell 的方式起宿主，打开「便签」，15 次中位数）：

| | 宿主启动到首页 | 打开到第一页 | 关掉再打开 | 打开时的 Pss（宿主 + 运行时） | 宿主程序 |
|---|---|---|---|---|---|
| goja（迁移前） | 12.5 ms | 1.5 ms | 0.6 ms | 22.8 MB | 24.9 MB |
| kario-jsrt | 11.1 ms | 3.4 ms | 3.3 ms | 18.3 + 1.5 = 19.8 MB | 13.0 MB |
| kario-jsrt，套 `phone-sandbox` | 11.5 ms | 15.0 ms | 14.8 ms | 18.2 + 1.5 = 19.7 MB | 13.0 MB |

- 内存少了约 3 MB：宿主不再带 goja，一个小程序的运行时 Pss 1.5 MB（代码页和别的 kario-jsrt 进程共享）。
- 打开慢了：要起一个进程（约 2 ms），套沙箱时先起一次 `kario-system`（Go 程序，约 12 ms，瞬时 Rss 约 25 MB）。goja 再打开快，是因为它一直留着运行时；kario-jsrt 现在关掉后也留 5 分钟（本节第一张表的「关掉后留着」），这段时间里再打开不起进程。上表「关掉再打开」是留着以前量的（关掉就结束、再打开从头起），留着以后的再打开**未量**。
- QEMU 上的数**未量**。

还没做的：
- 小程序每次打开都从源码起（`--no-bytecode`）：QuickJS 的字节码不防恶意写入，只能由系统自己编（§5.2），宿主不给小程序编；小程序小，解析不到 1 ms，**未实现**，暂时也不需要。
- QuickJS-ng 的 InternalError（内存不够）在 JS 里接不住，宿主从运行时的 stderr 认出来再提示；kario-jsrt 本身的这个行为（TypeScript 应用也一样）没有改。存储写失败不再抛错（在后台写，只在日志里记一行，`flush()` reject），`setStorage` 只在超过配额时抛错。
- 宿主收到 SIGTERM（Shell 关机、换语言时停它）先停各个小程序，等它们把正在后台写的存储写完（至多 5 秒；Shell 给每个应用 3 秒，`stopGrace`）再退出（`TestTerminatedHostLetsRuntimesSave`）。宿主被 SIGKILL 时运行时随之被 SIGKILL（`Pdeathsig`），正在写的那次丢掉，文件还是上一个完整版本。
- 关掉的小程序留 5 分钟、至多 3 个、合计 32 MB（上表「关掉后留着」）：按系统内存紧张的程度调这几个数**未实现**（系统没有给应用的内存压力事件；宿主自己在后台被结束时，留着的运行时随它结束）。
- goja 还在仓库里：Shell 画 Mermaid 图还用它（`shell/mermaid.go`），依赖删不掉。

### 5.5 在设备上调试

开发机上做完，单元测试通过（本节末），**未上真机**；QEMU 上**未跑**（没有自测用例：电脑那头要有调试器）。QuickJS-ng 0.18 没有调试器协议，引擎里现成的只有：中断回调（`JS_SetInterruptHandler`，大约每一万次跳转或调用才来一次，下不了断点、走不了单步）、pc 到行列的表（`pc2line`，只记调用、属性访问、运算和 throw 的位置）、栈帧链（`JSStackFrame`，内部结构，不导出）；`debugger` 语句被跳过。quickjs-debugger 那一支（koush/quickjs）在解释器循环里每条指令前调一次检查、按函数缓存断点、经 TCP 讲自己的协议、另由 VS Code 扩展转成 DAP；这里照这个思路，补丁更小，没在调试的应用一条指令也不多花，协议走已有的开发者链路。

| 部分 | 做法 |
|------|------|
| 引擎补丁 | `yocto/meta-phone/recipes-phone/quickjs/files/kario-debugger.patch`（MIT，和 QuickJS-ng 一样；`make jsrt` 和 Yocto 的 quickjs 配方打同一个文件）：`JS_SetDebugHandler` 设了回调以后进入的字节码帧换一张分发表，每条指令先过回调（没设时还是原来的表）；新操作码 `OP_debugger`；抛出异常、展开栈之前回调一次，带「栈上有没有 try 接住它」（本帧看栈上的 catch 位置，下面的帧按字节码里 try 的范围，到异步函数为止：它的异常成了 Promise 的拒绝）；读写帧的函数：帧数、位置、行表、参数和局部变量、闭包变量、改变量；设了回调时编译的代码每条语句都记位置（平时只记读变量、调用、属性、运算、throw 的），`JS_SetDebugPositions` 打开以后没设回调时编的也记（调试器以后可能附加上来的应用用）。操作码变了，字节码版本和版本号（`0.18.0+kario.1`）跟着变：没打补丁的引擎写的字节码不会被用上，系统的字节码缓存按引擎版本重编 |
| kario-jsrt 的调试器 | `jsrt/src/debug.c`：Shell 给了 `KARIO_JSRT_DEBUG_FD` 时，运行时先打招呼，等电脑设好断点、发来 `run` 再跑模块。Shell 给了 `KARIO_JSRT_DEBUG_ATTACH_FD`（一个 `SOCK_SEQPACKET` 套接字）时应用照常跑，Shell 以后可以经它递来一个调试套接字（`SCM_RIGHTS`），调试器就接到正在跑的进程上：不重启，状态都在，打招呼时带 `attached`，`run` 之后接着跑；同一时间一个，第二个递来的当场关掉；电脑走了还能再接。这种应用一启动就让引擎给编出的代码记每条语句的位置。每行一个 JSON 的请求和事件（文件开头有清单）：行断点（可带条件、命中次数、日志消息）、函数断点（按函数名，`Counter.bump` 这样带点的名字比最后一段；函数一进入就停）、`pause`、`continue`、`next`、`stepIn`、`stepOut`、调用栈（每帧函数名、`main.js` 的行列，原生帧标出）、作用域（局部、闭包、全局）和对象按引用展开（Map、Set、数组的项分页取，getter 点开时才读）、在帧里求值（帧的变量放进 `with` 的作用域，求值改了的变量写回帧里，`const` 不写）、`setVariable`、`debugger` 语句、未捕获（或全部）异常时暂停，console 输出和错误报告也发过去。命中次数数的是条件成立的次数：`N` 或 `>=N` 从第 N 次起停，`=N` 或 `==N` 只在第 N 次，`>N`、`<N`、`<=N`，`%N` 每第 N 次；同一组（TypeScript 的一行落在 `main.js` 的几行上）的断点一起数。日志点不停：在当时的帧里求出每个 `{表达式}`，写成一行 console 输出，带 `main.js` 里的位置。断点和单步按「行事件」：同一帧里换了行、往回跳（循环的下一圈）、新进的帧；只有含断点行的函数逐条看，别的函数走快路。暂停时只读调试的套接字，界面的消息留在套接字里；电脑走了（套接字关了）就去掉断点接着跑 |
| Shell 和开发者链路 | `devctl` 新操作 `debug`，`phone-dev debug APP` 把标准输入输出接到连接上；Shell（`shell/devlink.go` 的 `devDebug`）照别的操作过闸（开发者模式、信任的数据线、从数据线来的 SSH 会话），只给装上去的、开发商店签名的 TypeScript 应用（安装记录里的商店是开发商店：开发者模式下电脑装的），商店装的和系统应用都拒绝，同一时间一个调试器；它用一对套接字重启应用（不用缓存的字节码：要每条语句的位置），把连接和套接字两头对接，每秒查一次闸；调试中的应用不算 ANR，暂停时排满的消息只记一行、不当违规结束它。开发商店签名的 TypeScript 应用每次启动都多拿一个附加用的套接字（`KARIO_JSRT_DEBUG_ATTACH_FD`；Shell 只往里写，应用写不回来），字节码缓存也按每条语句的位置编（`kario-jsrt --positions --compile`，记录里写 `positions`，对不上就不用、重编；计算器的字节码大约多 0.7%）。`phone-dev debug -attach APP`（devctl 请求的 `attach`）：应用在跑、收得下，就经它递过去一对新套接字的一头，不重启，回「attached to …」；不在跑、或者没有这个套接字（比如 Shell 重启后接管过来的），照旧在调试器下重启，回「debugging …」。Shell 这头的调试套接字是非阻塞的：电脑一走马上关上，应用当场去掉断点接着跑（阻塞的文件在别的 goroutine 读着时关不掉，闲着的应用会一直以为调试器还在，下一个附加的就被拒） |
| `kario dap` | `cmd/kario/dap.go`，Debug Adapter Protocol（标准输入输出）：`launch` 把工程不压缩地编一遍、源码映射留在电脑上，用开发商店签名、照 `kario device install` 装上，再 `ssh … phone-dev debug`；`attach` 不装，`ssh … phone-dev debug -attach`，接到正在跑的应用上（不在跑就在调试器下启动它，调试控制台里说一句），Shell 回的 `main.js` 摘要和这次编的不一样时也说一句。断点从 `.ts` 的行经源码映射变成 `main.js` 的行（没有代码的行顺延到下一个有代码的，至多 20 行，编辑器里断点挪过去），调用栈和单步停的位置映射回 `.ts`，SDK 的源码（`kario-sdk:@kario/ui`）从映射里的副本给编辑器；条件断点、命中次数（手机读不懂的显示为未生效，带原因）、日志点（输出带 `.ts` 里的位置）、函数断点、悬停求值、改变量、异常信息；要求值的都按 TypeScript 写、用源码里的名字（下面「未实现」里的说明） |
| Kario Studio | 调试类型 `kario`（`package.json` 的 `debuggers`：launch 和 attach 的配置和片段），适配器就是 `kario dap`；命令「Kario: Debug on the Phone (Developer Mode)」和「Debug the App Already on the Phone」（附加，不重启）；没写 launch.json 时按 F5 也是 launch 当前工程；开发商店取设置 `kario.signing.devStore` |

验证：`jsrt` 的 `TestDebugger`（真的 kario-jsrt：断点、条件成立才停、调用栈三帧的函数名和行、参数和局部变量（还没初始化的不显示）、闭包变量、对象和数组按引用展开、在本帧和上一帧求值、求值里的赋值改了程序的变量、`setVariable`（`const` 拒绝）、单步跳过（含从 return 出到调用者）、步入、步出、`debugger` 语句、被接住的异常不停而未捕获的停在抛出的那行、console 输出、空闲时暂停停在下一段代码、运行中只在全局求值、电脑走了应用照跑）、`TestDebuggerEntry`（停在模块第一行）、`TestNoDebugger`（不给套接字时 `debugger` 语句什么也不做）、`TestDebuggerBreakpointKinds`（命中次数的各种写法、一组断点一起数、读不懂的命中次数不生效、日志点的值和位置、求值出错的写成错误、函数断点带条件和命中次数、按 `Class.name`、停下时参数已在、单步进函数体）、`TestDebuggerAttach`（应用跑着时递过去套接字：状态还在，断点停在一条只有字面量的语句上（平时的编译不记它的位置，这个用例在不记位置时失败），第二个调试器被关掉，走了以后再接、`stopOnEntry` 停在下一段代码）；`shell` 的 `TestDevLinkDebug`（真的 Shell 和 kario-jsrt：开发者模式关着、程序、系统应用、第二个调试器、不是开发商店装的都拒绝；重启后经连接停在 `debugger`，暂停时不弹 ANR，在帧里求值，继续后页面出来，电脑走了会话结束）、`TestDevLinkAttach`（从带位置的字节码起的应用跑着时附加：同一个进程、状态还在，只有字面量的行上的断点停住；电脑走了应用接着跑，闲着时走了的也是，再附加还能接上（旧的阻塞套接字在这里失败）；应用没在跑时附加就在调试器下启动它）、`TestJSCache`（多了：没按位置编的缓存不用）；`cmd/kario` 的 `TestDAP`（编辑器那样的一整段 DAP：launch 编译、装上、经假的 ssh 和真的 `phone-dev` 到假 Shell 起的 kario-jsrt；`.ts` 里的断点（空行上的挪到下一行）、调用栈在 `src/main.ts`、局部变量、求值、单步到下一行、继续后应用接着跑、断开）、`TestDAPAttach`（attach 只问手机 `debug -attach`、不装，应用的状态还在；命中次数从第二次起停，日志点写出值、位置在 `src/main.ts`，函数断点在第三次进入时停，读不懂的命中次数在编辑器里显示为未生效；断开后应用照跑）；`jsrt` 的 `TestDebuggerLazyAndPages`（类的 getter 在对象上显示为要点开的，点开时读到值、出错时值是错误；1200 项的 Map、Set、数组从第 1000 项、1150 项、1100 项起的页，数组的 `length` 在别的属性里）；`cmd/kario` 的 `TestDAPTypeScript`（带类型断言的条件和日志点、`as`、`<number>`、`satisfies` 的求值、改过名的 `count` 按 `count` 显示、求值、改值；旧的 `kario dap` 上条件是语法错误，不停）；`jsrt` 的 `TestRenames`（esbuild 编出的包里的改名、UTF-16 的列）；扩展的节点测试（配置和补全）。调试中每条指令多一次函数调用，没有断点的函数里只是几次比较；没在调试的应用不受影响，`jsrt` 原有的用例在打了补丁的引擎上照样通过。

**未实现**：
- 编辑器里的端到端测试（上面 §5.2 的「Kario Studio」）调的是假的手机（`cmd/kario` 的 `TestE2EPhone`：真的 `kario dap`、`phone-dev` 和 kario-jsrt，Shell 是假的）；对着开发机或模拟器的真 Shell 调、QEMU 上的自测用例、真机都**未做**。在多行的表达式中间（对象、数组字面量里的一行）下的断点，停住时报的位置可能是下一行（行事件按那一行第一条记了位置的指令，这里没有改）。
- 附加只对这个 Shell 起的进程：Shell 重启后接管过来的应用没有附加用的套接字，attach 会在调试器下重启它（经墓碑回到原来的页）。
- 局部变量是整个函数的（QuickJS 运行时不知道当前位置哪个块作用域生效）：离开了的块里的变量还显示，没初始化的不显示，同名的显示里层的。
- 闭包变量只有这个函数用到的外层变量（QuickJS 只捕获用到的），模块顶层别的变量在别的函数里看不到；`this` 只有函数用到时才有。
- 异常算不算「未捕获」：异步函数里没被 try 接住的都算（成了 Promise 的拒绝，以后有没有人 `catch` 不预判），原生代码接住的不算；Node 的判断更细。
- 读 getter 要用户点开（「(...)」，像 Node 下那样调用应用的代码，`this` 是那个对象；类在原型上的 getter 也列在对象上）；Map、Set、数组的项按编辑器要的分页取（`indexedVariables`，过了 500 项也取得到），别的属性另取。Proxy 的陷阱、`Symbol` 键的属性不列。
- 函数断点只按名字比：带点的名字只比最后一段（叫这个名字的函数都算），停在函数刚进入时，位置是函数开头那一行；命中次数在一个文件的断点重设时归零；日志点的 `{}` 按花括号配对，没有转义。
- 数据断点、重启帧、单步进原生模块的 C 代码（C 代码用 gdb，§5.3）都没有。
- 条件、日志点里的表达式、求值和改变量的新值都按 TypeScript 写：`kario dap` 先用 esbuild 去掉类型（`as`、`<T>`、`!`、`satisfies`），再把 esbuild 在包里改过名的变量（同名冲突时加的后缀，如 `count2`）换成改后的名字（esbuild 的 `Define`，只换表达式自己没声明的名字），帧里的变量也按源码里的名字显示（那个名字被占着时照旧显示改后的）。改名表从源码映射的 `names` 和编出的 `main.js` 来，按源文件分：同一个文件里一个名字改成了两个不同的名字时不换；函数断点的条件不知道在哪个文件，只去掉类型。esbuild 读不懂的照原样交给手机，手机报错。

### 5.6 符号文件和商店里的崩溃报告

开发机上做完，单元测试和门户的浏览器测试通过（本节末），**未上真机**。手机上跑的 `main.js` 是压缩过的，崩溃报告里的栈是它的行列；现在 `kario package` 把源码映射另写一个文件，商店收下后替开发者把栈映射回 TypeScript。

| 环节 | 做法 |
|------|------|
| `kario package` | 模块的源码映射（外置，`main.js` 里不留链接，`sources` 相对工程目录：`src/main.ts`、`kario-sdk:@kario/ui`）写进包旁边的符号文件 `<包名>.phonesym`（`-symbols` 改地方，`-` 不写），从不进包；给 `kario build -sourcemap` 编好的目录打包时，映射也拿出来放进符号文件。符号文件（`jsrt.SymbolFile`）是 zip：`symbols.json`（应用、版本、包的 SHA-256、模块和它的 SHA-256）、`main.js.map`、`main.js` 本身（压缩过的，和包里的一样，读的时候核对摘要；栈里的函数靠它找回源码里的名字，下面「`kario symbolize`」）、可选的 `lib/<架构>/<名字>.so`（`-native-symbols DIR` 给的没剥符号的库，build-id 必须和包里的一样） |
| `kario publish` | 包交审核以后接着传符号文件（同目录的 `.phonesym`，`-symbols` 指别的）：`POST /v1/symbols/<应用>/<版本>`，开发者的密钥（`-key`）签 `phone-symbols/1\|<名字>\|<时间>\|<应用>\|<版本>\|<符号文件的 SHA-256>`（`pkg.SymbolsMsg`）。别的包的符号文件在本地就拒绝；不收符号文件的商店（开源核心、模拟商店）照实说一句；商店拒收时说包已经交了、符号文件没有 |
| `kario symbolize` | `-map main.js.map` 或 `-symbols app.phonesym`：日志、崩溃记录或商店的列表里 `main.js` 的 JavaScript 栈映射回 TypeScript；符号文件里的库也拿来符号化原生调用栈。函数名也换回源码里的（`jsrt.SourceMap.SymbolizeStackNamed`）：压缩后叫 `a` 的帧，找它位置之前最近的一处 `a` 的声明（`function a`、`class a`、`let`/`const`/`var a =`），用源码映射在那儿记的原名（esbuild 压缩时给改了名的标识符都记 `names`）；esbuild 压缩时里层的作用域从不用外层作用域的名字，所以函数开头和函数体里的位置之间不会有别的 `a` 的声明。要有模块本身：符号文件里带着（以前写的符号文件没有，就只映射位置），`-map` 时用映射旁边的 `main.js`（`kario build -sourcemap` 就这么摆）。方法、属性的名字压缩时本来就不改，照原样；匿名函数还是 `<anonymous>`；赋给已有变量的函数（`a = function…`）找不回 |
| 商店后台（不开源） | kario-internal 的 `kario-storesrv`：核对签名、版本是这个开发者交的（已上架的、分批中的上一版或待审的）、符号文件就是这个包的（包的摘要、模块的摘要、库的 build-id），存在商店的数据目录里，只有开发者和工作人员读得到；开发者经 `GET /v1/developer/crashes`（签名同读崩溃报告）读符号化过的崩溃报告，工作人员经 `/v1/admin/crashes`，审核时可以下载符号文件；门户的崩溃报告显示映射过的调用栈，提交包时可以一起选符号文件 |

验证：`jsrt` 的 `TestSourceMap`、`TestSymbolFile`、`TestFunctionNames`（esbuild 压缩的模块在真的 kario-jsrt 上抛出的栈：声明的函数、`const` 里的箭头函数回到原名，方法照旧、匿名的还是匿名，行里有中文时列照 UTF-16 数）；`cmd/kario` 的 `TestPackageSymbols`（包里没有 `.map`，符号文件对得上包和模块、带着模块；打出的包里压缩过的 `main.js` 交给真的 kario-jsrt 跑出异常，它写的栈经符号文件落到 `src/main.ts` 抛异常的那一行，函数名是 `explode` 而不是压缩后的名字）、`TestPublishSymbols`（签名、别的包的符号文件、密钥不对）、`TestPublishToStore`（开源核心不收符号文件）；`appstore` 给商店后台的取数接口（`Crashes`、`Submitted`、`Authenticate`）；商店后台和门户的测试在内部仓库。

**未实现**：`kario package` 不替开发者剥库的符号（要手机拿到不带调试信息的库，开发者自己剥、没剥的交给 `-native-symbols`）；符号文件不随下架清理；设备上报的崩溃只带日志最后 20 行，更早的栈帧没有。商店后台（不开源）用 `jsrt.SymbolFile.Symbolize` 符号化，新写的符号文件带着模块，那里也就有原名了，在商店后台上**未验证**。

## 6. 风险

- **两个引擎的差别**：开发机上是 Node（V8），设备上是 QuickJS-ng。办法是只许用标准 ECMAScript 和 `kario.*`，类型定义里没有 Node 和浏览器的接口，Node 下 `main.js` 也只能导入 `kario`；`jsrt` 的运行时用例在两个引擎上都跑（`jsrt/node_test.go`），计算器的自测在两个引擎上都通过。Node 里 `process` 这类全局变量还是摸得到，Node 也不是沙箱：开发机上能跑不等于设备上能跑，以 kario-jsrt 的自测为准。
- **性能**：QuickJS 是解释器，算得比 V8 慢得多。界面由系统画，应用只管状态，一般够用；重活（图片解码、加密、媒体）交给系统服务，应用自己的热循环交给原生模块（§5.3：开发机上图片模糊 C 比 QuickJS 快约 47 倍）。实测不够再换 Hermes。
- **生态**：npm 上的包大多依赖 Node 或浏览器的接口，不能直接用；纯逻辑的库可以打包进来。
- **人手**：类型定义从 `ui/caps.go` 生成，接口改一处两边都跟上。Go SDK 不废弃，系统应用可以慢慢迁移。
