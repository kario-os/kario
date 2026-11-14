# Kario — 按键优先的手机系统

应用描述语义界面（`phone.Ui`），系统负责渲染、输入法、权限和安全界面。这个仓库是开源的平台：Shell（界面、能力 broker、系统界面）、系统服务、SDK、系统应用、鸿雁 / 飞钱参考实现、telephonyd、兼容测试、Yocto 层、像素渲染器 phone-paint（设备上用它）和 phone-gui（对照用），以及文档。设计与验证结果见 [docs/13-prototype.md](docs/13-prototype.md)，开发应用看 [docs/22-developer-api.md](docs/22-developer-api.md)。

## 开发者指南

### 1. 准备

Linux 开发机，Go 1.25、GCC 14（C++23，telephonyd）、CMake、Ninja、Rust（两个渲染器）、Noto Sans CJK 字体；看视频要 ffmpeg。构建设备镜像另要 podman 和 kas（`pip install kas`，默认找 `~/.local/kas-venv/bin/kas-container`，`KAS=` 可改）。

```bash
git clone git@github.com:kario-os/kario.git && cd kario
make doctor          # 查这台机器缺什么：宿主机目标要的标 MISS，设备镜像要的标 warn
make vendor          # Go 依赖进 vendor/（不进 git）
make run             # 在终端里用：文本界面
```

另一台机器复现：照上面装好工具、`make doctor` 没有 MISS，就能跑宿主机上的全部目标（`make test`、`make selftest`、`make preview`、`make pixel-selftest`）。设备镜像第一次构建要约 150 GB 磁盘和几个小时，之后增量几分钟；下载缓存和 sstate 在 `~/yocto/cache`（`YOCTO=` 可改），从已有的机器拷过去能省掉大部分时间。代理按连接限速时，大文件用 `sh yocto/fetch-mirror.sh <网址> <文件>` 分段并行下载，放进下载缓存（[docs/13-prototype.md](docs/13-prototype.md) §5.1）。要构建某个提交而不是工作树：`make yocto-build SRC=git REV=<标签>`。

### 2. 看像素界面：`make preview`

```bash
make preview                       # 默认直板九宫格；DEVICE=bar-qwerty / communicator 换形态
# 打开 http://127.0.0.1:8790；远程机器上：ssh -L 8790:127.0.0.1:8790 <机器>
```

渲染器（phone-paint）在后台画，画面变了就截一张显示在网页上；网页的键盘和按钮就是手机按键：方向键、Enter = OK、Esc = 返回、Tab / F1 = 选项、F2 = 右软键、F5 = 通话、F4 = 挂断、F3 = 切换应用、`/` = 命令面板，按住 Alt 是长按。不需要显示器、不需要模拟器。要换渲染器：`make preview RENDERER=gui/target/release/phone-gui`（先 `make gui`）。

### 3. 加一个应用

用 TypeScript 写照 `apps/calc`（KarioUI，教程见 [docs/34](docs/34-app-tutorial.md)）：`src/main.ts`、`manifest.json`（`"runtime": "js"`）、`test/`（`kario test`），在 `scripts/stage.sh` 里和它一样编进系统应用目录。用 Go 写照 `apps/weather`（后台任务、定位、应用设置）或 `examples/notes`（最小的界面）：`apps/<名字>/main.go`（`sdk.New`、`OnStart`、`OnAction`，返回 `ui.Screen`）、`manifest.json`（ID、名称、权限、`folder`）、`icon.png`，在 `scripts/stage.sh` 的应用列表里加上名字。`make run` 或 `make preview` 里就能打开。能力和事件的名字都在 `ui/`：`ui/caps.go`（能力）、`ui/names.go`（动作、状态、事件、设置键），不要自己写字符串；系统服务的接口在 `svc/`。`make test` 里的 `scripts/const-check.py` 会拦下重写这些字符串的代码。

C/C++ 应用用 `sdk/c`（示例 `apps/hello-cpp`），JS / TypeScript 小程序见 `apps/miniapp`；第三方应用打包、签名、安装见 [docs/22-developer-api.md](docs/22-developer-api.md) §12。

### 4. 调试

- **日志**：`make run` 和 `make preview` 的服务和应用都在 `~/.local/state/phone-sim/run/` 下：`shell.log`、每个服务一个 `phone-<服务>.log`、每个应用一个 `<应用 ID>.log`（应用的 `log.Print` 都在里面）。
- **看状态**：另开一个终端 `build/usr/bin/phone-shell --dump`，打印正在运行的 Shell 的应用、进程、队列和设置。
- **断点**：应用是普通进程，`dlv attach $(pgrep -f phone-app-<名字>)`；Shell 本身 `dlv exec build/usr/bin/phone-shell`。
- **重放一段操作**：把按键写成脚本（语法同 `shell/selftest.script`，指令列表在 `shell/headless.go` 开头），`build/usr/bin/phone-shell --script my.script`；加 `--gui paint/target/release/phone-paint --pixel-dir out` 每个 `snap` 出一张图。

### 5. 测试

```bash
make test                 # 许可证与常量检查、go vet、单元测试和端到端、telephonyd 的 C++ 测试、SKU 与 A/B 槽逻辑
make selftest             # 整套端到端用例（文本界面），JUnit 报告在 build/selftest-junit.xml
make selftest ONLY='视频|音乐'   # 只跑名字匹配的用例（个别用例要前面用例留下的状态）
make pixel-selftest       # 同一套用例由像素渲染器画，三种形态，每个 snap 一张 PNG
make cts                  # 兼容测试：自测加越权检查，签名的报告
make web-selftest         # 网页视图（在容器里编译 phone-webview）
make mm-selftest          # telephonyd 对模拟的 ModemManager
```

用例写在 `shell/selftest.script`：`== 名字` 开始一个用例，`key`、`type`、`wait`、`wait-re`、`gone`、`snap`、`at` 等指令；设备上开机自测跑的是同一份。

### 6. 设备镜像（QEMU）

```bash
make yocto-build          # 构建 qemuarm64 镜像（渲染器插槽里是 phone-paint）；构建目录 ~/yocto/kario-work
make yocto-selftest       # QEMU 里开机自测加兼容测试，自动关机
make yocto-vnc            # 开机进像素界面，经 VNC（127.0.0.1:5900）操作
make yocto-ota-test       # A/B 系统更新测试（11 次开机）
make sku SKU=kario-t9-sd662-2g   # 一个 SKU 的固件包
make yocto-build SRC=git  # 从提交（默认 HEAD，REV= 指定）而不是工作树构建，发布就这样构建；GIT=github 从 GitHub 取
make gomods               # 改了 go.mod / go.sum 之后：重新生成配方取 Go 模块的清单（要联网，make test 会提醒）
```

配方的源码默认是 `make yocto-src` 从工作树打的包，没提交的改动也在镜像里；Go 模块和 Rust crate 由 bitbake 按清单下载并校验，`vendor/` 不进镜像构建。

镜像里没有 GPLv3 组件。对照用的 phone-gui 镜像：`make yocto-build KASEXTRA=yocto/kas/phone-gui.yml`（phone-gui 是 GPL-3.0-only，只用来测试）。产品的版本发布、更新密钥和开发者门户在私有的 kario-internal 里，它把这个仓库放在旁边（`../kario`），用同一份配置构建。

### 7. 提交

贡献要签 DCO（`git commit -s`，`CONTRIBUTING.md`）。维护者发布：`make publish`（在第一个版本之前，仓库一直是一个完整的提交）；版本发布见 [docs/25-system-update.md](docs/25-system-update.md) 和 Actions 里的 `release`。

## 仓库里有什么

| 目录 | 内容 | 许可 |
|------|------|------|
| 根目录 | Shell（`shell/`）、系统服务（`cmd/`）、SDK（`sdk/`）、协议（`ui/`、`svc/`）、系统应用（`apps/`）、telephonyd、兼容测试（`cts/`） | Apache-2.0（`LICENSE`） |
| `yocto/` | Yocto 层 `meta-phone` 和 kas 配置 | Apache-2.0 |
| `paint/` | 像素渲染器 phone-paint（Rust），设备上用它；设计系统见 `docs/26-design-system.md` | Apache-2.0 |
| `gui/` | 像素渲染器 phone-gui（Rust + Slint），用来对照 phone-paint | GPL-3.0-only（`gui/COPYING`） |
| `docs/` | 给开发者和设备厂商的文档；英文版在 `docs/en/` | CC BY 4.0 |
