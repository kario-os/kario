# 应用开发体验：TypeScript 框架和系统中间件

> 状态：**P1–P3 在开发机上完成，P4 做了一部分**（§5.1、§5.2）：引擎和运行时、声明式框架 KarioUI、Node 下的运行时、`kario` 命令行（`new`、`build`、`run`、`test`、`package`、`publish`）、用 KarioUI 重写的计算器（换下了 Go 版）、Kario Studio 扩展、界面和审核规范（[33](33-app-guidelines.md)）、教程（[34](34-app-tutorial.md)）；单元测试和自测在开发机上通过，QEMU 和真机上都**未跑**。隐私标签（[33](33-app-guidelines.md) §3）做了，`kario publish` 在开发机上对官方商店后端跑过。文档上开发者门户、小程序迁到同一个引擎、P5（§5.3 是设计）**未实现**。2026-11 决定应用的主力语言改为 TypeScript，替换先前的 Swift 方案。现在怎么写应用见 [34](34-app-tutorial.md) 和 [22](22-developer-api.md)；这篇写的是路线和做到了哪里。

## 0. 结论

- **中间件已经有了，这次把它定成唯一的入口**。Shell 的 broker、系统服务（音频、媒体、密钥、推送、电话、输入法）和 phone.Ui 协议合起来就是 Kario 运行时：应用拿不到设备，界面交给系统画，每个能力都要经它申请（权限、用途、沙箱、审计，[17](17-app-sandbox.md)）。TypeScript 应用只看得到 `kario.*` 接口，没有文件系统、网络套接字和进程，比安卓的 Java 层更严：连 JNI 那样的后门都没有。
- **应用的主力语言定为 TypeScript**，上面做声明式框架 KarioUI，写法照 SwiftUI 和鸿蒙的 ArkUI（状态变了界面自己更新）。设备上用 QuickJS-ng 引擎（MIT，几百 KB）跑预编译好的字节码；开发机上同一份代码跑在 Node.js 里，VS Code 自带的 JS 调试器直接下断点。
- **开发工具只做一个 VS Code / Cursor 扩展**，加一个 `kario` 命令行（Go 写，内嵌 esbuild 编译 TypeScript，打包不要求装 Node）。
- **TypeScript 是推荐的路，不是唯一的路**：微信、支付宝这类大应用不用重写成 TypeScript。安卓上的微信本来就是 Java / Kotlin 的界面加大量 C++（网络库 Mars、数据库 WCDB、音视频编解码、小程序引擎）。Kario 给它们两条路：TypeScript 写界面、性能要紧的部分做成**原生模块**（C 接口，像鸿蒙的 NAPI、React Native 的 TurboModules，**未实现**，P5）；或者整个应用用 C / C++ / Rust / Go 写，直接讲 phone.Ui 协议（今天就能做，`sdk/c`，示例 hello-cpp）。两种都在同一个进程沙箱里、只能经中间件要能力，原生代码只是多一道审核。
- Go 留给系统服务和现有的系统应用，Rust / C++ 留给渲染器和要性能的部分；小程序以后并到同一个引擎上。
- 学苹果学的是「一家定下整条工具链、规范和审核标准」，不是它的语言。

## 1. 现状和差距

| 环节 | 苹果 | Kario 现在 | 差距 |
|------|------|------------|------|
| 语言 | Swift（以前 Objective-C） | TypeScript（`kario-jsrt`，KarioUI；计算器已改写）、Go（其余的系统应用）、C / C++（`sdk/c`）、Python（直接讲协议）、JS / TS（小程序，还在 goja 上） | 系统应用大多还是 Go；小程序没并到同一个引擎 |
| 界面框架 | SwiftUI：声明式，状态变了界面自己更新 | KarioUI（§5.2）：页面是状态的函数，状态变了只重发变了的页；Go SDK 还是自己收发页面 | 增量只到整页（协议的 `update` 是整页）；没有可复用的自定义组件库 |
| 开发工具 | Xcode：模板、代码补全、实时预览、模拟器、调试器、Instruments、签名、上传 | `kario` 命令行、Kario Studio 扩展（编译、预览面板、调试、测试、打包、上传）、LSP、自测脚本、开发者门户 | 存盘后重新载入要重启应用进程（经墓碑回到原来的页），没有不跑应用的视图预览；没有性能分析；扩展没在编辑器里从头点过一遍 |
| 模拟器 | iOS Simulator，按机型 | 开发机上的 Shell：`kario run` 一条命令起来，`-preview` 在浏览器或扩展的面板里看像素界面，`-device` 选形态 | 没有按 SKU 选机型的界面（只有形态），面板里没有机身皮肤 |
| 文档和规范 | Developer Documentation、Human Interface Guidelines、示例代码、WWDC | 开发者接口文档（22）、应用规范（33）、教程（34）、键盘交互（10）、设计系统（26）、示例应用 | 没有正反例截图；文档没上开发者门户 |
| 测试 | XCTest、UI 测试、TestFlight | `kario test`（Node 里对着假 Shell 跑应用自己的用例）、自测脚本（按键 + 等文字）、兼容测试、分批发布 | 假 Shell 的焦点和按键是简化的；没有 TestFlight 那样的测试版渠道 |
| 上架 | App Store Connect、审核指南、隐私标签 | 开发者门户、`kario publish`、人工审核、权限用途、备案号、审核指南（33 §2）、隐私标签（33 §3） | 隐私标签还不是必填，网页目录不显示 |

## 2. 为什么是 TypeScript

- **人最多**：国内会 TS / JS 的开发者最多，写过微信小程序的人更是现成的（我们的小程序本来就是 TS）；鸿蒙的 ArkTS 已经证明这条路在手机上走得通。
- **工具现成**：TypeScript 的补全、跳转、诊断是 VS Code 自带的，只要给出类型定义（`@kario/sdk`）；调试器也是自带的。一个扩展就能装好全部工具，不用另装 1 GB 的 Swift 工具链。
- **安全更好讲**：两层沙箱。引擎里只有 `kario.*`（没有 `fs`、`net`、`child_process`），进程外面还是现在的沙箱（命名空间、seccomp、Landlock、按应用的防火墙）。只用 TypeScript 的应用不带本地代码，审核只看 JS 和清单；带原生模块的应用（P5）照原生应用审，沙箱一样。
- **和协议对得上**：KarioUI 不用自己排版——界面是语义节点，系统来画——所以比 SwiftUI 和 ArkUI 都简单：视图函数求值出节点树，和上一次比较，只发变化的部分（协议已经有 `Update`，渲染器有增量帧）。
- 设备上的引擎比较过：

| 引擎 | 大小 | 每个应用的内存 | 启动 | 调试 | 许可 |
|------|------|----------------|------|------|------|
| QuickJS-ng | 约 1 MB | 1–2 MB 起 | 快（预编译字节码） | 设备上只有日志；开发机上换 Node 调试 | MIT |
| Hermes | 约 3 MB | 几 MB | 很快（字节码，React Native 在用） | Chrome DevTools 协议 | MIT |
| V8 | 10 MB 以上 | 每个实例 10 MB 以上 | 快（JIT） | 完整 | BSD |
| goja（今天的小程序） | 编进 Go 程序 | 跟着 Go 程序 | 要解析源码 | 无 | MIT |

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
| `kario` 命令行 | `kario new`（模板）、`kario run`（编、在模拟器里开起来，Node 下可以调试）、`kario test`、`kario package`（签名打包，即今天的 `phone-pkg`）、`kario publish`（交给商店审核）；Go 写，内嵌 esbuild 把 TypeScript 编成一个文件，再出字节码。都有了（§5.1、§5.2） | `xcodebuild`、`xcrun` |
| Kario Studio（VS Code / Cursor 扩展） | 新建项目、编译（错误进问题面板）、运行、模拟器面板、调试、测试、打包、上传、日志（`ide/vscode`，§5.2）；选机型只按形态，按 SKU 选**未实现**；权限和用途的清单编辑器**未实现**；类型定义 `@kario/sdk` 从 `ui/caps.go` 生成，接口只有一个来源 | Xcode 的界面 |
| 实时预览 | 存盘后重新载入：`kario run -preview -watch`（扩展的面板缺省开着）重新编译，Shell 像换系统语言那样重启应用，应用经墓碑回到原来的页、带着交出的状态，编不过就留着旧的（§5.2）。不跑应用、只求值视图交给 phone-paint 的无头模式出图的预览**未实现** | SwiftUI Preview |
| 模拟器 | 开发机上的 Shell、服务和 phone-paint 打成一个程序，界面就是 `make preview` 那页加机身皮肤（`make skins`）；按 SKU 选屏幕、键盘、内存档 | iOS Simulator |
| 调试 | 模拟器里应用跑在 Node.js 下（`kario run -inspect`），VS Code 自带的 JS 调试器附加上去（开发机 ✓）；真机上经信任过的数据线、开发者模式（[27](27-usb-typec.md) §4）看日志、重新加载（**未实现**） | Xcode 调试、开发者模式 |
| 性能 | 每个应用的 CPU、内存（cgroup 已有数字）、帧、能力调用次数，按时间线显示（**未实现**） | Instruments |

## 4. 规范、文档、测试、上架

- **人机界面规范**：[33](33-app-guidelines.md) §1，从键盘交互（10）、设计系统（26）、权限和用途（17）整理；正反例截图**未做**。
- **教程和示例**：[34](34-app-tutorial.md) 从零写一个应用（列表、输入、导航、确认、存储和墓碑、能力、翻译、测试、调试、上架），成品是 `examples/karioui-notes`；推送和后台任务那几步**未写**。
- **测试框架**：`kario test`（[34](34-app-tutorial.md) §9）在 Node 里对着假 Shell 跑应用自己工程里的用例，按键加读屏幕，和自测脚本一个思路；同一个应用在两个引擎上跑自测：计算器在 kario-jsrt 和 Node 下都通过（§5.2）。
- **上架**：审核指南 [33](33-app-guidelines.md) §2；隐私标签 33 §3（商店应用的「详情」页和审核台显示，还不是必填）；测试版渠道用分批发布和开发者模式。

## 5. 分阶段

| 阶段 | 内容 | 完成标准 |
|------|------|----------|
| P1 | 引擎和运行时：QuickJS-ng 进构建（开发机和 Yocto 配方）；`kario-jsrt`（引擎加协议客户端加 `kario.*` 能力接口，经 C 接口 `sdk/c`）；`kario` 命令行编 TypeScript 出包；一个 TypeScript 示例应用 | 示例应用在开发机和 QEMU 上通过自测；量出包大小、内存、启动时间，和 Go 应用比。开发机：✓（§5.1）；QEMU：待跑 |
| P2 | KarioUI：视图、状态、列表、输入框、动作、软键、导航栈，增量更新；翻译走 `tr`；同一个应用能在 Node 下跑（模拟器） | 用 KarioUI 重写一个系统应用（计算器或记事本），两个引擎上自测都通过。开发机：✓（§5.2）；QEMU：待跑 |
| P3 | 工具：Kario Studio 扩展（新建、模拟器面板、运行和调试、预览、打包上传） | 新人从安装到在模拟器里跑起自己的应用不超过 30 分钟。扩展编好、单元测试通过、`.vsix` 装得上（§5.2）；在编辑器里从头点一遍、量这 30 分钟**未做** |
| P4 | 规范、教程、测试框架、审核指南、隐私标签；小程序迁到同一个引擎 | 开发者门户里能看到全部文档；第三方应用用测试框架跑测试。规范、教程、测试框架、审核指南、隐私标签 ✓（33、34）；上门户、小程序迁移**未实现** |
| P5 | 原生模块：TypeScript 应用带的 C 接口库（arm64，随包签名），在同一个进程里给 JS 提供函数（编解码、加密、数据库、自己的网络库），审核照原生应用；要不要给大应用一个带 JIT 的引擎（V8、Hermes）按实测再定。设计见 §5.3（**未实现**） | 一个示例应用用原生模块做一件 QuickJS 做不快的事（比如图片处理），在模拟器上量出比纯 TypeScript 快多少 |

### 5.1 P1：开发机上做到了什么

P1 在开发机上做完了，单元测试和自测通过；QEMU 上的自测和测量要等首领编镜像后跑（还没在 QEMU 上跑过，**未上真机**）。

| 部件 | 在哪里 | 验证 |
|------|--------|------|
| QuickJS-ng 0.18.0（MIT） | 源码不进仓库：`make jsrt` 按版本和 SHA-256 下载到 `build/`，另存一份在 `~/.cache/kario`（`QJS_CACHE`）；`make build`、`make test` 有这份源码时顺带编运行时，没有（离线又没缓存）时说一句就跳过运行时和 TypeScript 示例；`make jsrt` 离线时下载失败，摘要不对时拒收。Yocto：`recipes-phone/quickjs`（meta-openembedded 没有 QuickJS 的配方），只出静态库，镜像不装它的包 | 开发机 ✓；配方写好，镜像未编 |
| `kario-jsrt` | `jsrt/`（C），经 `sdk/c` 讲协议；镜像里 `recipes-phone/kario-jsrt` 把它装到 `lib/phone/bin`，`PHONE_IMAGE_PACKAGES` 已加上 | 单元测试 ✓（假 Shell 驱动：页面、动作、按键、输入、能力调用的 Promise 和错误原文、定时器、墓碑、resync、存储及上限、翻译回退、字节码、`std` 和 `os` 拿不到）；自测 ✓ |
| `@kario/sdk` | `sdk/ts/`：`index.d.ts` 手写（`kario` 模块），`ui.d.ts` 由 `tools/tsgen` 从 `ui/` 生成，过期时 `make test` 失败 | ✓（用 tsc 5.9 strict 检查过示例和模板；构建里不用 tsc，不要 Node） |
| `kario` 命令行 | `cmd/kario`：`new`、`build`（esbuild 的 Go 接口，ES2023）、`package`（`pkg.Build`，密钥同 phone-pkg，或开发商店）；`run`、`test`、`publish` **未实现** | 单元测试 ✓ |
| 启动和安装 | 清单的 `runtime: "js"`、`main`；Shell 在应用的沙箱里启动 `kario-jsrt <应用目录>/main.js`（`shell/jsapp.go`）；安装器和商店审核用同一个 `jsrt.Check` | 单元测试 ✓ |
| 示例 | `examples/hello-ts`：便签列表、输入框、右软键「全部删除」、剪贴板、存储、墓碑、3 秒后消失的提示 | 自测「TypeScript 应用」✓（文本界面和 phone-paint 像素界面） |

测量：开发机（x86-64，Xeon E5-2670 v3）上照 Shell 的方式启动进程（fd 3 上的协议，`jsrt/measure_test.go`，没套沙箱），每项 20 次取中位数。内存是第一页出来 300 ms 后 `/proc/<pid>/smaps_rollup` 的 Rss 和 Pss（`status` 里的 VmRSS 对多线程的 Go 程序偏低：内核按线程攒着计数，隔一阵才合进去）。Go 应用用设备上的编法（`CGO_ENABLED=0`，`-trimpath -ldflags='-s -w'`，静态）。

| 应用 | 包 | 启动到第一页 | Rss | Pss |
|------|----|------------|-----|-----|
| hello-ts，`main.js`（1.9 KB） | 14 KB（gzip 后 7.5 KB） | 1.9 ms | 3.1 MB | 1.4 MB |
| hello-ts，`main.jsc`（3.0 KB） | （只在系统镜像里） | 1.8 ms | 3.0 MB | 1.3 MB |
| examples/notes（Go） | 2.5 MB（gzip 后 1.1 MB） | 3.3 ms | 4.7 MB | 4.7 MB |
| apps/calc（Go） | — | 3.3 ms | 4.8 MB | 4.8 MB |
| 合成的大应用，`main.js` 294 KB | — | 42 ms | 9.1 MB | 7.3 MB |
| 同上，`main.jsc` 558 KB | — | 6.7 ms | 5.5 MB | 3.7 MB |

- 运行时本身：`kario-jsrt` 去掉符号后 1.2 MB（x86-64，`-O3`，引擎静态链入），所有 TypeScript 应用共用一份；Go 应用每个都带自己的运行时（notes 的程序 2.5 MB）。TypeScript 应用的 Pss 小，是因为 libc 和 `kario-jsrt` 的代码页在进程之间共享，Go 的静态程序只有自己用。
- 按大小优化（`-Os`，`CMAKE_BUILD_TYPE=MinSizeRel`）：`kario-jsrt` 0.68 MB，hello-ts 启动一样快（2.2 ms / 1.7 ms），Pss 0.9 MB；只有大包的源码解析慢一些（294 KB：52 ms，字节码 7.0 ms）。设备上用哪一档等 QEMU 和真机的数再定；Yocto 的配方现在用它的缺省编译参数。
- 在真的 Shell 里（自测最后的内存报告，套着沙箱，开发版编法）：`kario-jsrt` 运行 hello-ts 1.3 MB PSS（私有脏页 0.3 MB），`phone-app-calc` 4.5 MB。
- 小应用的源码解析不到 1 ms，字节码的好处到几百 KB 的包才看得出：294 KB 的包从 42 ms 降到 6.7 ms，内存也少了一半（不用留下解析时的结构）。字节码是源码的 1.9 倍大，又防不住恶意的写入方，所以只在系统镜像里预编译；商店装的应用跑源码，装好后在设备上预编译**未实现**。
- QEMU（aarch64，TCG 模拟）上同样的测量要等首领编出镜像后跑（**未实现**）。

### 5.2 P2–P4：开发机上做到了什么

单元测试和自测在开发机上通过；QEMU 和真机上都**未跑**（要首领编镜像：计算器换成 TypeScript 以后，设备上的自测靠 `kario-jsrt` 跑它）。

| 部件 | 在哪里 | 验证 |
|------|--------|------|
| KarioUI（`@kario/ui`） | `sdk/ts/karioui/index.ts`：组件（`Text`、`Item`、`Tile`、`Input`、`Button`、`Progress`、`Image`、`Doc`、`Media`、`Web`、`Drawing`、`List`）、动作 `act`、页面 `page`、导航栈 `nav`、状态 `state`（`store` 进存储，`save` 交给墓碑）、生命周期和事件（`app`）、`tr`；状态一变重画栈里的每一页，只把 JSON 变了的页发出去（`update`）；离开前台时把页面栈、页面参数和要保存的状态交给墓碑，回来时照原样重建。`kario build` 用 esbuild 插件把它打进 `main.js`（树摇，只留用到的），项目里不装 npm 包 | 计算器、教程示例、模板的 `kario test` ✓；tsc 5.9 strict ✓ |
| Node 下的 `kario` 模块 | `sdk/ts/node/`：`runtime.mjs` 照 `jsrt/src/kario.c`、`dispatch.c` 写（页面栈的镜像和 resync、能力的 Promise 和系统的错误原文、存储和 1 MB 上限、翻译的回退、墓碑的 64 KB、ping、一条消息跑完它引出的微任务才接下一条）；`kario.mjs` 讲 fd 3 上的套接字，`console` 写进日志也给调试器；`run.mjs` 装导入钩子，`main.js` 只能导入 `kario`；语言表由 `tools/tsgen` 生成 | `jsrt/node_test.go`：`jsrt` 的运行时用例在 Node 下同样通过（逃逸用例除外：Node 不是沙箱） |
| `kario run` | `cmd/kario/run.go`：编（带源码映射）、写出 Node 运行时，启动 `phone-shell --dev-app <build> --dev-runner [...]`；`-engine jsrt` 换成 kario-jsrt；`-preview`、`-inspect`、`-brk`、`-device`、`-lang`；`-watch`（要 `-preview`）存盘后重新编译，发 SIGUSR2，Shell 重启开发者应用（`shell/devapp.go` 的 `ReloadDev`） | 单元测试 ✓；`-watch` 在开发机的 Shell 里手动验证过：计算器输到「1+2」，改了标题存盘，约 40 ms 后新版本带着「1+2」回来；编不过时旧的照跑 |
| Shell 里的开发者应用 | `shell/devapp.go`：`--dev-app` 的目录排在系统应用后面（ID 相同就顶替它）、开机就打开；`--dev-runner` 在沙箱外启动它，调试器才连得上；用设备的服务时 `phone-shell` 不接受 `--dev-runner` | 自测（下一行）✓ |
| 计算器 | `apps/calc`：TypeScript + KarioUI 重写，换下了 Go 版（Go 版删了，git 里还有）；算到一半的式子交给墓碑（算完的不留；Go 版没有）；出错后式子留着可以改（Go 版接着按键就被清掉）；数字照 Go 的 `strconv.FormatFloat` 格式化（精确值舍入到偶数）；镜像里有字节码，Yocto 用 `KARIO_CLI` 编它 | `kario test` 8 个用例 ✓；自测里用到计算器的用例在 kario-jsrt 下 ✓、换成 Node（`--dev-runner`）也 ✓；新用例「KarioUI 计算器」（墓碑）在两个引擎下都 ✓ |
| `kario test`、`@kario/test` | `cmd/kario/test.go`、`sdk/ts/kariotest/index.ts`、`sdk/ts/node/test.mjs`、`fake.mjs`：应用的 `main.js` 跑在 Node 下同一个运行时里，另一头是假 Shell；用 node:test 跑（一个进程，报错的位置映射回 `.ts`） | `go test ./cmd/kario` ✓（模板的用例通过、失败时报错、`-run`） |
| `kario publish` | `cmd/kario/publish.go`：`POST /v1/submissions`（`?rollout=`），和 `phone-pkg submit` 一样 | 单元测试 ✓：假的商店，加上商店后端的开源核心 `appstore`（`cmd/kario/store_test.go`：新应用待审、目录带隐私标签、分批更新、多发一类数据又进审核）；开发机上对官方商店 `kario-storesrv` 手动跑过一遍（注册、核验、`kario package`、`kario publish`、审核、目录） |
| 隐私标签 | `ui/privacy.go`：类别、用途、`CheckPrivacy`，`kario build`、`shell/install.go`、`appstore` 都调它；目录条目带 `privacy`，分批发布时旧版本带旧标签（`pkg.OfferedApps`）；商店应用的「详情」页 `apps/store/privacy.go`；审核台（不开源）加了一列 | `ui`、`pkg`、`appstore`、`apps/store`、`cmd/kario` 的单元测试 ✓；门户的浏览器测试 ✓；设备上的「详情」页**未跑**（自测里没有商店后端） |
| 教程示例 | `examples/karioui-notes`（[34](34-app-tutorial.md)） | `kario test` 6 个用例 ✓ |
| Kario Studio | `ide/vscode`：命令（新建、编译、终端里运行、预览面板、调试、停止、测试、打包、上传、写调试配置）、设置 `kario.*`，只调 `kario` 命令行；开发依赖只有 typescript（Apache-2.0）、@types/vscode 和 @types/node（MIT），带 lockfile；`.vsix` 由 `scripts/vsix.mjs` 写，不用 @vscode/vsce（它依赖的 @vscode/vsce-sign 不是开源许可） | 不靠 VS Code 的单元测试 9 个 ✓；`.vsix` 用 Cursor 的服务端程序装进一个空的扩展目录 ✓；在编辑器里点一遍（面板、断点）**未做** |
| 规范、教程 | [33](33-app-guidelines.md)、[34](34-app-tutorial.md) | 教程里的代码摘自测试过的示例 |

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
- QEMU 和真机上的数**未量**。

### 5.3 P5：原生模块（设计，未实现）

TypeScript 写界面和逻辑，慢的部分（编解码、图片处理、加密、数据库、自己的网络协议）用 C、C++ 或 Rust 写成共享库，随包签名，在同一个进程里给 JS 调用。下表都**未实现**。

| 方面 | 设计 |
|------|------|
| 接口 | 一个和引擎无关的 C 接口（`sdk/c/kario_native.h`，照 Node-API 的思路）：库导出 `kario_native_init`，用它登记函数；参数和返回值只有数字、字符串、`ArrayBuffer`（不复制，借给 C 用到函数返回）和 JSON。C 代码摸不到 QuickJS 的对象，以后换 Hermes 不用重编模块；接口带版本号，加载时核对 |
| JS 这边 | `import { native } from "kario"`，`native("imgproc").blur(pixels, w, h, 3)`；模块的类型由开发者自己写 `.d.ts` |
| 线程 | 缺省在 JS 线程上同步执行（短小的函数）；登记成异步的函数在 `kario-jsrt` 的工作线程上执行，返回 Promise，结果回到消息循环；每个应用的工作线程有上限，CPU 照旧由 cgroup 管 |
| 包和清单 | 清单写 `"native": ["imgproc"]`，库放在 `native/<架构>/libimgproc.so`（设备是 `aarch64`，开发机是 `x86_64`）；`jsrt.Check` 现在拒收一切可执行文件，改成声明了 `native` 时只许这些位置上、架构对的共享库，别处照旧不许。`kario-jsrt` 动态链接 libc，`dlopen` 用得上 |
| 沙箱和审核 | 同一个进程、同一个沙箱（命名空间、seccomp、Landlock、按应用的防火墙），能力照旧只经 `kario.*`：原生代码能做的不超过今天用 `sdk/c` 写的应用（hello-cpp）。带 `native` 的应用每个版本都进人工审核，照原生应用审（33 §2.2） |
| 开发机 | 用 `kario run -engine jsrt` 跑（kario-jsrt 加载 `x86_64` 的库，C 代码用 gdb 附加调试）；Node 下要一层把这个接口接到 Node-API 的适配，以后再做 |
| 编译 | `kario build` 不编 C；开发者用 Yocto 导出的 SDK，或 `clang --target=aarch64-linux-gnu` 加系统的 sysroot 编，`kario package` 把 `native/` 打进包 |

还要定的：包按架构分开还是一个包带两种架构（商店只给设备下它那种）；原生代码崩溃（SIGSEGV）时崩溃报告里没有 JS 的调用栈；要不要给大应用一个带 JIT 的引擎，等完成标准里的测量出来再定。

## 6. 风险

- **两个引擎的差别**：开发机上是 Node（V8），设备上是 QuickJS-ng。办法是只许用标准 ECMAScript 和 `kario.*`，类型定义里没有 Node 和浏览器的接口，Node 下 `main.js` 也只能导入 `kario`；`jsrt` 的运行时用例在两个引擎上都跑（`jsrt/node_test.go`），计算器的自测在两个引擎上都通过。Node 里 `process` 这类全局变量还是摸得到，Node 也不是沙箱：开发机上能跑不等于设备上能跑，以 kario-jsrt 的自测为准。
- **性能**：QuickJS 是解释器，算得比 V8 慢得多。界面由系统画，应用只管状态，一般够用；重活（图片解码、加密、媒体）交给系统服务。实测不够再换 Hermes。
- **生态**：npm 上的包大多依赖 Node 或浏览器的接口，不能直接用；纯逻辑的库可以打包进来。
- **人手**：类型定义从 `ui/caps.go` 生成，接口改一处两边都跟上。Go SDK 不废弃，系统应用可以慢慢迁移。
