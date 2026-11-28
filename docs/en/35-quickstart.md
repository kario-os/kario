> English translation of [the Chinese original](../35-quickstart.md). The Chinese text is authoritative; keep both in sync.

# Quickstart: your first Kario app in ten minutes

> The shortest path from a developer machine with the tools installed to your own app running in the simulator: a TypeScript app in the terminal and in Cursor (Kario Studio), an app written entirely in C++ or Go, and a TypeScript app with a native module in C, C++ or Rust. The step-by-step tutorial is [34](34-app-tutorial.md), the interfaces are in [22](22-developer-api.md) §9.
> Status: every command below was run on the developer machine (2026-11-16, RHEL 8, x86-64; Cursor 3.29 connected to this machine over SSH), and its output is copied as printed, with `/home/<user>` written as `~`. Cursor's local side (port forwarding, what the panel shows) cannot be seen from the remote side: **not verified**; Kario Studio's commands were clicked through in VSCodium by the end-to-end test ([29](29-app-development.md) §5.2). Everything runs in the developer machine's simulator; QEMU and real phones **not run**.

## 0. Getting ready (once)

The Kario source tree is at `~/code/kario-os/kario`, and the host part of `make doctor` shows no MISS ([README](../../README.md) §1).

```bash
cd ~/code/kario-os/kario
make install-kario     # make build, make paint, then a link from ~/.local/bin/kario to build/bin/kario
make doctor            # see the "writing apps" part
```

```
install-kario: ~/.local/bin/kario -> ~/code/kario-os/kario/build/bin/kario
writing apps (kario run, test, preview; Kario Studio; docs/35-quickstart.md):
  ok    node v24.14.0
  ok    phone-paint (kario run -preview, kario preview)
  ok    kario ~/.local/bin/kario (this tree's build/bin/kario)
```

- What is installed is a link, not a copy: `kario` finds the developer machine's Shell (`build/usr/bin/phone-shell`) and phone-paint from where it really is, and `make build` in the source tree updates it from then on. `~/.local/bin` has to be on `PATH` (`make install-kario` says so when it is not; `BINDIR=` puts the link elsewhere).
- Node.js 22.15 or newer: `kario run`, `kario test` and `kario preview` need it (`kario build` and `kario package` do not). CI runs the tests of the Node.js runtime and of the `kario` command on two majors: 22, the oldest supported, and 24 ([38](38-contributing.md) §2). On this machine the interactive shell has nvm's Node 24, and `/usr/local/bin/node` is 18; when it gets an older one `kario` says which and how old (§5), and `-node` or `KARIO_NODE` names another.

## 1. A TypeScript app in the terminal

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

- Open http://127.0.0.1:8790 in a browser: the pixel UI, the page's keyboard being the phone's keys (Enter = OK, F2 = right soft key, Esc = Back, Tab = Options). In a Cursor remote session Cursor forwards the port by itself once it is open (8790 is in the Ports panel; Ctrl-click the address in the terminal); otherwise use "Forward a Port" in the Ports panel with 8790, or `ssh -L 8790:127.0.0.1:8790 <this machine>` on your own computer.
- `-watch`: change `hello/src/main.ts` (say the title `tr("Counter")` to `tr("My counter")`) and save; the terminal says `kario run: built again (main.js 7.4 KB), restarting the app`, and the app comes back on its page with its state. Ctrl-C stops it: the Shell and the app end together.
- Without `-preview` it is the text UI in the terminal: `kario run hello`.

Pictures of the pages in `src/previews.ts` without running the app, then a signed package:

```bash
kario preview -device bar-t9 hello
kario package -devstore ~/kario-devstore -o hello.phoneapp hello
```

```
bar-t9: ~/kario-apps/hello/.kario/preview/bar-t9/new.png (new, 480x640)
bar-t9: ~/kario-apps/hello/.kario/preview/bar-t9/tapped.png (tapped, 480x640)
hello.phoneapp: 12800 bytes
```

`-devstore` is a development store, which phones in developer mode trust; the real key and certificate, publishing and installing on a phone are in [34](34-app-tutorial.md) §11 and [22](22-developer-api.md) §9.2.

## 2. A TypeScript app in Cursor (Kario Studio)

In Cursor's terminal (in a remote session `cursor` is the command Cursor's server brings, and the extension goes onto the SSH machine):

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

The last two lines are in Cursor's interface language here ("Installing extensions on SSH: d15…", "Successfully installed the extension"); the extension is in `~/.cursor-server/extensions/kario-os.kario-studio-0.1.0`. Outside Cursor's terminal: "…" at the top of the Extensions view › "Install from VSIX…". Then, in the command palette (Ctrl+Shift+P):

| Command | What it does |
|------|--------|
| Kario: New App | pick a folder (say `~/kario-apps`) and a name: `kario new`, then the app opens in a new window |
| Kario: Run in the Preview Panel | the pixel UI in a panel beside the editor, the panel's keyboard being the phone's keys; saving reloads it |
| Kario: Run Tests | `kario test` in a terminal |
| Kario: Preview the Views | `kario preview`'s pictures in a panel, drawn again on save |
| Kario: Debug (Node.js) | the preview panel with the debugger: breakpoints in `src/main.ts` |
| Kario: Choose the Device | the device in the status bar: a SKU or a form factor |

- The setting `kario.cli` needs no change: its default `kario` is the one `make install-kario` put into `~/.local/bin` (on the `PATH` of Cursor's remote extension host). When it is not found the extension says `no kario command (the setting kario.cli): run make install-kario in the Kario tree, or set kario.cli to its build/bin/kario`.
- The panel's address is forwarded by Cursor (`asExternalUri`): no port to forward yourself in a remote session.
- The extension takes TypeScript apps only (a manifest with `"runtime": "js"` and `src/main.ts`); run the native apps below with `kario run` in Cursor's terminal.

## 3. An app written entirely in C++ or Go

An app is a directory: `manifest.json`, the program its `exec` names (beside the manifest), and optionally `locale/`. You build the program with your own tools; `kario run` builds nothing and hands it as it is to the developer machine's Shell, which starts it in the app's directory ([22](22-developer-api.md) §1, §9).

### 3.1 C++ (`sdk/c`)

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

- `libphoneui.a` is what `make build` built (`build/capi/`); the system's own GCC 8 compiles the app (C++17).
- The program starts in the app's directory, so it finds `locale/<language>.json` by a relative path; a system program of the same name never runs in its place.
- After a change, build again and `kario run` again; `-watch`, `-inspect` and `-engine` are for TypeScript apps, as `kario` says, and you debug with `gdb -p`. The app's log is `~/.local/state/phone-sim/run/com.example.hellocpp.log`.

### 3.2 Go (`sdk`)

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

`go mod init` says `go: creating new go.mod: module example.com/gonotes`; `replace` points `github.com/kario-os/kario` at the local source tree; on this machine `go mod tidy` found the dependencies in the module cache and printed nothing (with an empty cache it first downloads three modules, `golang.org/x/sys` among them). `kario run` prints the same as above.

### 3.3 Rust

There is no SDK for a whole app in Rust yet (`sdk/rust/kario-native` is for native modules, §4): send and receive JSON lines on fd 3 as [22](22-developer-api.md) §2 says (as `apps/hello-py` does), or call `libphoneui` through FFI. **Not verified.**

## 4. A TypeScript app with a native module (C, C++, Rust)

TypeScript for the interface, the slow part as a shared library the TypeScript `import`s ([22](22-developer-api.md) §9.3; the full sample is `examples/imgproc`). From `kario new`:

```bash
cd ~/kario-apps
kario new fast > /dev/null && cd fast
mkdir -p native/x86_64
```

`native/fast.c`:

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

`src/native.d.ts` (you write the types: the runtime knows only the names):

```ts
declare module "native:fast" {
  export function add(a: number, b: number): number;
}
```

`src/main.ts` is the template's counter with three lines changed: the module imported, the button adding one with `add`, and a line showing what it computes:

```ts
import { add } from "native:fast";
// …
    Button(tr("Tap me"), act("tap", tr("Tap me"), () => count.update((n) => add(n, 1)))),
    Text(`native:fast add(2, 3) = ${add(2, 3)}`, { style: "dim" }),
```

Build the library, declare it in the manifest, test, and run on kario-jsrt, the device's engine:

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

- The page shows `native:fast add(2, 3) = 5`, and OK adds one through the C `add`. Without `-engine jsrt` it runs under Node.js (as `kario test` does): `kario` builds a loader with this machine's C compiler, and breakpoints go in the TypeScript.
- Phones are arm64: build a `native/arm64/fast.so` too with a cross compiler before `kario package` takes the app (this machine has no cross compiler: **not verified**).

The same module in C++ (`kario_native.hpp`), built into the same `native/x86_64/fast.so`:

```cpp
#include "kario_native.hpp"

static double add(kario::call &c) { return c.arg<double>(0) + c.arg<double>(1); }

KARIO_NATIVE_CPP_MODULE([](kario::module &m) { m.function("add", add); })
```

```bash
cd ~/kario-apps/fast
c++ -std=c++17 -shared -fPIC -O2 -I ~/code/kario-os/kario/sdk/c/include -o native/x86_64/fast.so native/fast.cpp
```

In Rust (the crate `sdk/rust/kario-native`, with no dependencies, so no network), `native/rust/src/lib.rs`:

```rust
use kario_native::{Call, Error, Module};

fn add(c: &mut Call) -> Result<f64, Error> {
    Ok(c.arg::<f64>(0)? + c.arg::<f64>(1)?)
}

kario_native::module!(|m: &mut Module| m.function("add", add));
```

```bash
cd ~/kario-apps/fast
mkdir -p native/rust/src     # lib.rs goes here
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

With either library in place, `kario test .` still says `ℹ pass 2`, and OK still adds one under `kario run -engine jsrt`.

## 5. When something goes wrong

| What you see | What to do |
|--------|--------|
| `bash: kario: command not found` | `make install-kario`; `~/.local/bin` has to be on `PATH` |
| `kario: /usr/local/bin/node is Node.js v18.0.0: apps run on Node.js 22.15 or newer (-node PATH or $KARIO_NODE)` | the first Node.js on `PATH` is an old one: put 22.15 or newer first, or name it as it says |
| `-preview needs a pixel renderer (make paint in kario, or -gui PATH)` | `make paint` (`make install-kario` includes it) |
| On a remote machine the browser cannot open 127.0.0.1:8790 | forward 8790 in Cursor's Ports panel, or `ssh -L 8790:127.0.0.1:8790 <this machine>` |
| A new app does not open in its initial state | the developer machine's Shell keeps data by app id: delete `~/.local/state/phone-sim/state/apps/<app id>` to start over |
| Enter in an input on the full-keyboard form factor does not send the text | under the Chinese input method ("中" in the status bar) the first Enter commits the letters and the second submits; or `-device bar-t9` |
| `kario: no ~/kario-apps/hello-cpp/hello: build the app's program first (exec "hello" in manifest.json)` | the native app's program is not built yet, or not beside the manifest |
| Kario Studio: `no kario command (the setting kario.cli)` | as in the first row, or set `kario.cli` to `~/code/kario-os/kario/build/bin/kario` |

## 6. Where next

- A whole app step by step (list, navigation, storage, capabilities, push, background jobs, translations, tests, publishing): [34](34-app-tutorial.md).
- How to design the interface and what the store reviews: [33](33-app-guidelines.md).
- The capabilities, the protocol, the rules of native modules: [22](22-developer-api.md).
