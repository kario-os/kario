# 十分钟上手：第一个 Kario 应用

> 从一台装好工具的开发机到在模拟器里跑起自己的应用，最短的路：TypeScript 应用在终端里和在 Cursor 里（Kario Studio）、整个用 C++ 或 Go 写的应用、带 C / C++ / Rust 原生模块的 TypeScript 应用。一步一步的写法见 [34](34-app-tutorial.md)，接口见 [22](22-developer-api.md) §9。
> 状态：下面每条命令都在开发机上跑过（2026-11-16，RHEL 8，x86-64；Cursor 3.29 经 SSH 远程连着这台机器），输出照抄，只把 `/home/<用户>` 写成 `~`。Cursor 本地那一侧（端口转发、面板里的画面）从远程这边看不到，**未验证**；Kario Studio 的命令在 VSCodium 里由端到端测试点过一遍（[29](29-app-development.md) §5.2）。都在开发机的模拟器里，QEMU、真机**未跑**。

## 0. 准备（一次）

Kario 的源码树在 `~/code/kario-os/kario`，`make doctor` 的主机部分没有 MISS（[README](../README.md) §1）。

```bash
cd ~/code/kario-os/kario
make install-kario     # make build、make paint，再把 build/bin/kario 链到 ~/.local/bin/kario
make doctor            # 看「writing apps」这一节
```

```
install-kario: ~/.local/bin/kario -> ~/code/kario-os/kario/build/bin/kario
writing apps (kario run, test, preview; Kario Studio; docs/35-quickstart.md):
  ok    node v24.14.0
  ok    phone-paint (kario run -preview, kario preview)
  ok    kario ~/.local/bin/kario (this tree's build/bin/kario)
```

- 装的是链接，不是拷贝：`kario` 从它真正所在的地方找开发机的 Shell（`build/usr/bin/phone-shell`）和 phone-paint，以后源码树里 `make build` 就是更新。`~/.local/bin` 要在 `PATH` 里（不在时 `make install-kario` 会说；`BINDIR=` 换地方）。
- Node.js 22.15 或更新：`kario run`、`kario test`、`kario preview` 要它（`kario build`、`kario package` 不要）。CI 在两个大版本上跑 Node 运行时和 `kario` 命令的测试：22（支持的最老的）和 24（[38](38-contributing.md) §2）。这台机器上交互的 shell 里是 nvm 的 Node 24，`/usr/local/bin/node` 是 18；用到旧的时 `kario` 直说是哪个、多旧（§5），`-node` 或 `KARIO_NODE` 指定别的。

## 1. TypeScript 应用：在终端里

```bash
mkdir -p ~/kario-apps && cd ~/kario-apps
kario new hello
kario test hello
kario run -preview 127.0.0.1:8790 -watch hello
```

```
hello: hello (com.example.hello). Next:
  in this terminal:  kario run hello
  in a browser:      kario run -preview 127.0.0.1:8790 hello   (http://127.0.0.1:8790)
  its tests:         kario test hello
✔ OK counts, the right soft key starts again (12.949449ms)
✔ the count outlives the process (10.628844ms)
…
ℹ pass 2
ℹ fail 0
PHONE-PREVIEW: http://127.0.0.1:8790  (phone-paint 0.1.0 · Apache-2.0, bar-qwerty 640x480; over SSH: ssh -L 8790:127.0.0.1:8790 <this machine>)
```

- 浏览器打开 http://127.0.0.1:8790 就是像素界面，网页的键盘就是手机的键（Enter = OK、F2 = 右软键、Esc = 返回、Tab = 选项）。在 Cursor 的远程会话里，Cursor 看到端口开了会自己转发（「端口」面板里有 8790，终端里的地址按住 Ctrl 点开）；没有的话在「端口」面板里「转发端口」填 8790，或者在自己的电脑上 `ssh -L 8790:127.0.0.1:8790 <这台机器>`。
- `-watch`：改 `hello/src/main.ts`（比如标题 `tr("Counter")` 改成 `tr("My counter")`）存盘，终端里出 `kario run: built again (main.js 7.4 KB), restarting the app`，应用带着原来的状态回到原来的页。Ctrl-C 停下，Shell 和应用一起结束。
- 不带 `-preview` 是终端里的文本界面：`kario run hello`。

不跑应用，出静态的预览图（`src/previews.ts` 里的页面），再签名打包：

```bash
kario preview -device bar-t9 hello
kario package -devstore ~/kario-devstore -o hello.phoneapp hello
```

```
bar-t9: ~/kario-apps/hello/.kario/preview/bar-t9/new.png (new, 480x640)
bar-t9: ~/kario-apps/hello/.kario/preview/bar-t9/tapped.png (tapped, 480x640)
hello.phoneapp: 12800 bytes
```

`-devstore` 是开发商店，开发者模式的手机认它；正式的密钥和证书、上架、装到真机见 [34](34-app-tutorial.md) §11 和 [22](22-developer-api.md) §9.2。

## 2. TypeScript 应用：在 Cursor 里（Kario Studio）

在 Cursor 的终端里（远程会话时 `cursor` 是 Cursor 服务端带的命令，扩展装到 SSH 那台机器上）：

```bash
cd ~/code/kario-os/kario/ide/vscode
npm ci
npm run vsix
cursor --install-extension "$PWD/kario-studio-0.1.0.vsix"
```

```
~/code/kario-os/kario/ide/vscode/kario-studio-0.1.0.vsix: 8 files
正在 SSH: d15 上安装扩展…
已成功安装扩展“kario-studio-0.1.0.vsix”。
```

后两行是 Cursor 的界面语言说的；扩展装在 `~/.cursor-server/extensions/kario-os.kario-studio-0.1.0`。不在 Cursor 的终端里时：扩展视图右上角的「…」›「从 VSIX 安装…」。然后在命令面板（Ctrl+Shift+P）里：

| 命令 | 做什么 |
|------|--------|
| Kario: New App | 选一个文件夹（如 `~/kario-apps`）、填名字：`kario new`，然后在新窗口里打开 |
| Kario: Run in the Preview Panel | 编辑器旁边的面板里是像素界面，面板里的键盘就是手机的键；存盘就重新载入 |
| Kario: Run Tests | 终端里的 `kario test` |
| Kario: Preview the Views | `kario preview` 的图显示在面板里，存盘重画 |
| Kario: Debug (Node.js) | 预览面板加调试器：在 `src/main.ts` 里下断点 |
| Kario: Choose the Device | 状态栏上的机型：一款 SKU 或一种形态 |

- 设置 `kario.cli` 不用改：缺省的 `kario` 就是 `make install-kario` 放进 `~/.local/bin` 的那个（Cursor 远程的扩展宿主的 `PATH` 里有它）。找不到时扩展说 `no kario command (the setting kario.cli): run make install-kario in the Kario tree, or set kario.cli to its build/bin/kario`。
- 面板的地址经 Cursor 转发（`asExternalUri`），远程会话里不用自己转发端口。
- 扩展只认 TypeScript 应用（清单 `"runtime": "js"` 加 `src/main.ts`）；下面的原生应用在 Cursor 的终端里用 `kario run`。

## 3. 整个应用用 C++ 或 Go 写

应用就是一个目录：`manifest.json`、它的 `exec` 指的程序（就在清单旁边）、可选的 `locale/`。程序用自己的工具编，`kario run` 不编，照原样交给开发机的 Shell，Shell 在应用目录里启动它（[22](22-developer-api.md) §1、§9）。

### 3.1 C++（`sdk/c`）

```bash
cd ~/kario-apps
mkdir hello-cpp && cd hello-cpp
cp ~/code/kario-os/kario/sdk/c/examples/hello-cpp/main.cpp . && cp -r ~/code/kario-os/kario/sdk/c/examples/hello-cpp/locale .
c++ -std=c++17 -O2 -I ~/code/kario-os/kario/sdk/c -o hello main.cpp ~/code/kario-os/kario/build/capi/libphoneui.a
cat > manifest.json <<'EOF'
{ "id": "com.example.hellocpp", "name": "Hello C++", "lang": "en", "exec": "hello" }
EOF
cd ..
kario run -preview 127.0.0.1:8790 hello-cpp
```

```
PHONE-PREVIEW: http://127.0.0.1:8790  (phone-paint 0.1.0 · Apache-2.0, bar-qwerty 640x480; over SSH: ssh -L 8790:127.0.0.1:8790 <this machine>)
```

- `libphoneui.a` 是 `make build` 编的（`build/capi/`）；系统自带的 GCC 8 就编得了（C++17）。
- 程序从应用目录启动，`locale/<语言>.json` 按相对路径读得到；系统里有同名的程序也是跑你的这个。
- 改了代码重新编、再 `kario run`；`-watch`、`-inspect`、`-engine` 只对 TypeScript 应用，`kario` 会这样说，调试用 `gdb -p`。应用的日志在 `~/.local/state/phone-sim/run/com.example.hellocpp.log`。

### 3.2 Go（`sdk`）

```bash
cd ~/kario-apps
mkdir gonotes && cd gonotes
cp ~/code/kario-os/kario/examples/notes/main.go ~/code/kario-os/kario/examples/notes/locale.go . && cp -r ~/code/kario-os/kario/examples/notes/locale .
go mod init example.com/gonotes
go mod edit -require=github.com/kario-os/kario@v0.0.0 -replace=github.com/kario-os/kario=$HOME/code/kario-os/kario
go mod tidy
go build -o notes .
cat > manifest.json <<'EOF'
{ "id": "com.example.gonotes", "name": "Go notes", "lang": "en", "exec": "notes" }
EOF
cd ..
kario run -preview 127.0.0.1:8790 gonotes
```

`go mod init` 说 `go: creating new go.mod: module example.com/gonotes`；`replace` 让 `github.com/kario-os/kario` 用本机的源码树；`go mod tidy` 在这台机器上从模块缓存取齐依赖，没有输出（缓存空着时它先下载 `golang.org/x/sys` 等三个模块）。`kario run` 的输出同上。

### 3.3 Rust

整个应用用 Rust 写，现在没有 SDK（`sdk/rust/kario-native` 只给原生模块用，§4）：照 [22](22-developer-api.md) §2 在 fd 3 上收发 JSON 行（像 `apps/hello-py`），或者经 FFI 调 `libphoneui`。**未验证**。

## 4. TypeScript 应用带原生模块（C、C++、Rust）

TypeScript 写界面，慢的部分写成共享库，TypeScript 里 `import` 它（[22](22-developer-api.md) §9.3；完整的示例是 `examples/imgproc`）。从 `kario new` 开始：

```bash
cd ~/kario-apps
kario new fast > /dev/null && cd fast
mkdir -p native/x86_64
```

`native/fast.c`：

```c
#include "kario_native.h"

static void add(const kario_api *api, kario_call *c, void *data)
{
	api->ret_number(c, api->number(c, 0) + api->number(c, 1));
}

static int init(const kario_api *api, kario_module *m)
{
	return api->export_fn(m, "add", add, NULL, 0);
}

KARIO_NATIVE_MODULE(init)
```

`src/native.d.ts`（类型自己写，运行时只知道名字）：

```ts
declare module "native:fast" {
  export function add(a: number, b: number): number;
}
```

`src/main.ts` 是模板的计数器，改三行：导入模块，按钮用 `add` 加一，再显示一行它算的：

```ts
import { add } from "native:fast";
// …
    Button(tr("Tap me"), act("tap", tr("Tap me"), () => count.update((n) => add(n, 1)))),
    Text(`native:fast add(2, 3) = ${add(2, 3)}`, { style: "dim" }),
```

编库，在清单里声明它，测试，在设备上的引擎 kario-jsrt 里跑：

```bash
cc -shared -fPIC -O2 -I ~/code/kario-os/kario/sdk/c/include -o native/x86_64/fast.so native/fast.c
sed -i 's/"permissions": \[\]/"native": ["fast"], "permissions": []/' manifest.json
cd ..
kario test fast
kario run -preview 127.0.0.1:8790 -engine jsrt -device bar-t9 fast
```

```
✔ OK counts, the right soft key starts again (15.661373ms)
✔ the count outlives the process (12.038909ms)
…
ℹ pass 2
ℹ fail 0
kario run: native module "fast" has no arm64 build: phones cannot load it, kario package refuses it
PHONE-PREVIEW: http://127.0.0.1:8790  (phone-paint 0.1.0 · Apache-2.0, bar-t9 480x640; over SSH: ssh -L 8790:127.0.0.1:8790 <this machine>)
```

- 页面上是 `native:fast add(2, 3) = 5`，按 OK 经 C 的 `add` 加一。不带 `-engine jsrt` 是在 Node 下跑（`kario test` 也是），`kario` 用本机的 C 编译器现编一个加载器，断点打在 TypeScript 里。
- 手机是 arm64：另用交叉编译器编一份 `native/arm64/fast.so`，`kario package` 才收（这台机器上没有交叉编译器，**未验证**）。

同一个模块用 C++ 写（`kario_native.hpp`），编出的还是 `native/x86_64/fast.so`：

```cpp
#include "kario_native.hpp"

static double add(kario::call &c) { return c.arg<double>(0) + c.arg<double>(1); }

KARIO_NATIVE_CPP_MODULE([](kario::module &m) { m.function("add", add); })
```

```bash
cd ~/kario-apps/fast
c++ -std=c++17 -shared -fPIC -O2 -I ~/code/kario-os/kario/sdk/c/include -o native/x86_64/fast.so native/fast.cpp
```

用 Rust 写（crate `sdk/rust/kario-native`，没有依赖，不用联网），`native/rust/src/lib.rs`：

```rust
use kario_native::{Call, Error, Module};

fn add(c: &mut Call) -> Result<f64, Error> {
    Ok(c.arg::<f64>(0)? + c.arg::<f64>(1)?)
}

kario_native::module!(|m: &mut Module| m.function("add", add));
```

```bash
cd ~/kario-apps/fast
mkdir -p native/rust/src     # lib.rs 放在这里
cat > native/rust/Cargo.toml <<EOF
[package]
name = "fast"
version = "0.1.0"
edition = "2021"

[lib]
crate-type = ["cdylib"]

[dependencies]
kario-native = { path = "$HOME/code/kario-os/kario/sdk/rust/kario-native" }
EOF
cargo build --release --manifest-path native/rust/Cargo.toml
cp native/rust/target/release/libfast.so native/x86_64/fast.so
```

换成这两种的库以后，`kario test .` 照样是 `ℹ pass 2`，`kario run -engine jsrt` 里按 OK 照样加一。

## 5. 出了问题

| 看到的 | 怎么办 |
|--------|--------|
| `bash: kario: command not found` | `make install-kario`；`~/.local/bin` 要在 `PATH` 里 |
| `kario: /usr/local/bin/node is Node.js v18.0.0: apps run on Node.js 22.15 or newer (-node PATH or $KARIO_NODE)` | `PATH` 上先找到的是旧的 Node：换上 22.15 或更新的，或者照它说的指定 |
| `-preview needs a pixel renderer (make paint in kario, or -gui PATH)` | `make paint`（`make install-kario` 已经包括） |
| 远程机器上浏览器打不开 127.0.0.1:8790 | Cursor 的「端口」面板里转发 8790，或者 `ssh -L 8790:127.0.0.1:8790 <这台机器>` |
| 新建的应用一打开就不是初始状态 | 开发机的 Shell 按应用 ID 留着数据：删掉 `~/.local/state/phone-sim/state/apps/<应用 ID>` 就从头开始 |
| 全键盘形态的输入框里按 Enter，字没有发出去 | 中文输入法下（状态栏「中」）第一次 Enter 把字母上屏，再按一次才提交；或者 `-device bar-t9` |
| `kario: no ~/kario-apps/hello-cpp/hello: build the app's program first (exec "hello" in manifest.json)` | 原生应用的程序还没编，或者不在清单旁边 |
| Kario Studio：`no kario command (the setting kario.cli)` | 同第一行，或者把设置 `kario.cli` 设成 `~/code/kario-os/kario/build/bin/kario` |

## 6. 接着看

- 一步一步写一个完整的应用（列表、导航、存储、能力、推送、后台任务、翻译、测试、上架）：[34](34-app-tutorial.md)。
- 界面怎么设计、商店审什么：[33](33-app-guidelines.md)。
- 能力目录、协议、原生模块的规则：[22](22-developer-api.md)。
