> English translation of [the Chinese original](../29-app-development.md). The Chinese text is authoritative; keep both in sync.

# App development: a TypeScript framework and the system middleware

> Status: **P1–P3 are done on the developer machine, P4 in part** (§5.1, §5.2): the engine and runtime, the declarative framework KarioUI, the runtime under Node, the `kario` command line (`new`, `build`, `run`, `test`, `package`, `publish`), the calculator rewritten in KarioUI (it replaced the Go one), the Kario Studio extension, the interface and review guidelines ([33](33-app-guidelines.md)), a tutorial ([34](34-app-tutorial.md)); unit tests and the selftest pass on the developer machine, **not run** on QEMU or a real phone. Privacy labels ([33](33-app-guidelines.md) §3) are done, and `kario publish` has been run against the official store backend on the developer machine. The documents on the developer portal, moving mini-programs onto the same engine and P5 (§5.3 is a design) are **not implemented**. In 2026-11 the main language for apps was changed to TypeScript, replacing the earlier Swift plan. How to write apps today: [34](34-app-tutorial.md) and [22](22-developer-api.md); this document is the route and how far it has come.

## 0. Conclusion

- **The middleware already exists; it now becomes the only way in.** The Shell's broker, the system services (audio, media, keys, push, telephony, input method) and the phone.Ui protocol together are the Kario runtime: apps do not get the devices, the system draws their UI, and every capability is requested through it (permissions, purposes, sandbox, audit, [17](17-app-sandbox.md)). A TypeScript app sees only the `kario.*` interfaces: no file system, network sockets or processes. That is stricter than Android's Java layer, which still has a way around it in JNI.
- **The main language for apps is TypeScript**, with the declarative framework KarioUI on top, written the way SwiftUI and HarmonyOS's ArkUI are (the UI updates itself when the state changes). On the device the QuickJS-ng engine (MIT, a few hundred KB) runs precompiled bytecode; on the developer machine the same code runs in Node.js, and VS Code's built-in JS debugger sets breakpoints in it directly.
- **The tools are just one VS Code / Cursor extension** plus a `kario` command line (written in Go, with esbuild built in to compile TypeScript, so packaging does not need Node installed).
- **TypeScript is the recommended way, not the only one**: big apps such as WeChat and Alipay need not be rewritten in TypeScript. WeChat on Android is already Java / Kotlin UI on top of a lot of C++ (the Mars network library, the WCDB database, audio and video codecs, the mini-program engine). Kario gives them two ways: TypeScript for the UI with the performance-critical parts as **native modules** (a C interface, like HarmonyOS's NAPI or React Native's TurboModules, **not implemented**, P5); or the whole app in C / C++ / Rust / Go, speaking the phone.Ui protocol directly (possible today, `sdk/c`, the hello-cpp sample). Both run in the same process sandbox and reach capabilities only through the middleware; native code only means one more review step.
- Go stays for the system services and the existing system apps, Rust / C++ for the renderer and whatever needs speed; mini-programs later move onto the same engine.
- Learning from Apple means learning its way of "one company setting the whole tool chain, the guidelines and the review standard", not its language.

## 1. Where we are and the gaps

| Area | Apple | Kario today | Gap |
|------|------|------------|------|
| Language | Swift (Objective-C before) | TypeScript (`kario-jsrt`, KarioUI; the calculator rewritten), Go (the other system apps), C / C++ (`sdk/c`), Python (speaking the protocol directly), JS / TS (mini-programs, still on goja) | Most system apps are still Go; mini-programs are not on the same engine |
| UI framework | SwiftUI: declarative, the UI updates itself when the state changes | KarioUI (§5.2): pages are functions of the state, and a change resends only the pages that changed; the Go SDK still sends and receives pages itself | Increments only down to whole pages (the protocol's `update` is a whole page); no library of reusable custom components |
| Tools | Xcode: templates, completion, live preview, simulator, debugger, Instruments, signing, upload | The `kario` command line, the Kario Studio extension (build, preview panel, debugging, tests, packaging, upload), LSP, selftest scripts, developer portal | Reload on save restarts the app's process (back on its pages through the tombstone), there is no preview of the views without running the app; no profiling; the extension has not been clicked through in an editor from start to end |
| Simulator | iOS Simulator, per model | The Shell on the developer machine: one command, `kario run`; `-preview` shows the pixel UI in a browser or the extension's panel, `-device` picks the form factor | No UI to pick a model by SKU (only the form factor), no phone body skin in the panel |
| Docs and guidelines | Developer Documentation, Human Interface Guidelines, sample code, WWDC | Developer API (22), app guidelines (33), tutorial (34), keyboard interaction (10), design system (26), sample apps | No screenshots of what to do and what not to; the documents are not on the developer portal |
| Testing | XCTest, UI tests, TestFlight | `kario test` (an app's own cases in Node against a fake Shell), selftest scripts (keys + waiting for text), compatibility tests, staged rollout | The fake Shell's focus and keys are simplified; no beta channel like TestFlight |
| Publishing | App Store Connect, review guidelines, privacy labels | Developer portal, `kario publish`, human review, permission purposes, filing numbers, review guidelines (33 §2), privacy labels (33 §3) | Privacy labels are not mandatory yet, the web catalogue does not show them |

## 2. Why TypeScript

- **The most people**: TS / JS developers are the largest group in China, and people who have written WeChat mini-programs are ready-made (our mini-programs are TS already); HarmonyOS's ArkTS has shown this route works on phones.
- **The tools are there**: TypeScript's completion, navigation and diagnostics come with VS Code, given the type definitions (`@kario/sdk`); so does the debugger. One extension installs every tool, with no 1 GB Swift tool chain to install as well.
- **Security is easier to argue**: two layers of sandbox. The engine has only `kario.*` (no `fs`, `net`, `child_process`), and outside the process it is still today's sandbox (namespaces, seccomp, Landlock, the per-app firewall). An app that is TypeScript only brings no native code, so review looks only at the JS and the manifest; an app with native modules (P5) is reviewed as a native app, in the same sandbox.
- **It fits the protocol**: KarioUI does no layout of its own — the UI is semantic nodes the system draws — so it is simpler than both SwiftUI and ArkUI: view functions evaluate to a node tree, which is compared with the previous one, and only the changes are sent (the protocol already has `Update`, the renderer incremental frames).
- The engines for the device that were compared:

| Engine | Size | Memory per app | Start | Debugging | Licence |
|------|------|----------------|------|------|------|
| QuickJS-ng | about 1 MB | from 1–2 MB | fast (precompiled bytecode) | only logs on the device; on the developer machine debug in Node instead | MIT |
| Hermes | about 3 MB | a few MB | very fast (bytecode, used by React Native) | Chrome DevTools protocol | MIT |
| V8 | over 10 MB | over 10 MB per instance | fast (JIT) | complete | BSD |
| goja (today's mini-programs) | built into the Go program | follows the Go program | parses the source | none | MIT |

  QuickJS-ng first: the smallest, easy to bring into Yocto, and a 2 GB phone can hold dozens of apps with it; switch to Hermes if measurements show it is too slow. V8 is too heavy.

A KarioUI app looks like this (the system's calculator, `apps/calc/src/main.ts`, with the key tables left out; [34](34-app-tutorial.md) goes step by step):

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

## 3. Tools: an extension and a command line, not a separate IDE

| Part | What it does | Apple's counterpart |
|--------|--------|-----------|
| `kario` command line | `kario new` (templates), `kario run` (build, start in the simulator, debuggable under Node), `kario test`, `kario package` (sign and package, today's `phone-pkg`), `kario publish` (hand over to the store's review); written in Go, with esbuild built in to compile the TypeScript into one file, then bytecode. All of them exist (§5.1, §5.2) | `xcodebuild`, `xcrun` |
| Kario Studio (VS Code / Cursor extension) | New project, build (errors in the Problems view), run, simulator panel, debugging, tests, packaging, upload, logs (`ide/vscode`, §5.2); picking a model goes by form factor only, by SKU **not implemented**; a manifest editor for permissions and purposes **not implemented**; the type definitions `@kario/sdk` are generated from `ui/caps.go`, so the interface has one source | Xcode's UI |
| Live preview | Reload on save: `kario run -preview -watch` (on by default in the extension's panel) builds again, the Shell restarts the app as a language change does, and the app comes back on its pages through the tombstone with the state it handed over; a failed build leaves the old one running (§5.2). A preview that evaluates the views without running the app and has phone-paint's headless mode draw them is **not implemented** | SwiftUI Preview |
| Simulator | The Shell, the services and phone-paint of the developer machine packed into one program; its UI is the `make preview` page with the phone body skin (`make skins`); screen, keyboard and memory tier picked by SKU | iOS Simulator |
| Debugging | In the simulator the app runs under Node.js (`kario run -inspect`) and VS Code's built-in JS debugger attaches to it (developer machine ✓); on a real phone, over a trusted cable in developer mode ([27](27-usb-typec.md) §4), logs and reloading (**not implemented**) | Xcode debugging, developer mode |
| Performance | Each app's CPU, memory (the cgroup numbers exist), frames and capability calls, on a timeline (**not implemented**) | Instruments |

## 4. Guidelines, docs, testing, publishing

- **Human interface guidelines**: [33](33-app-guidelines.md) §1, gathered from keyboard interaction (10), the design system (26), permissions and purposes (17); screenshots of what to do and what not to **not done**.
- **Tutorials and samples**: [34](34-app-tutorial.md) writes an app from scratch (a list, an input, navigation, confirmation, storage and the tombstone, capabilities, translations, tests, debugging, publishing); the finished app is `examples/karioui-notes`; the steps for push and background jobs are **not written**.
- **Test framework**: `kario test` ([34](34-app-tutorial.md) §9) runs the cases in the app's own project in Node against a fake Shell, pressing keys and reading the screen, the selftest scripts' way; the same app runs the selftest on both engines: the calculator passes on kario-jsrt and on Node (§5.2).
- **Publishing**: review guidelines in [33](33-app-guidelines.md) §2; privacy labels in 33 §3 (shown on the store app's Details page and the staff desk, not mandatory yet); a beta channel through staged rollout and developer mode.

## 5. Phases

| Phase | Content | Done when |
|------|------|----------|
| P1 | Engine and runtime: QuickJS-ng in the build (developer machine and a Yocto recipe); `kario-jsrt` (the engine plus the protocol client plus the `kario.*` capability interfaces, through the C interface `sdk/c`); the `kario` command line compiling TypeScript into a package; one TypeScript sample app | The sample app passes the selftest on the developer machine and on QEMU; package size, memory and start time measured and compared with a Go app. Developer machine: ✓ (§5.1); QEMU: still to run |
| P2 | KarioUI: views, state, lists, inputs, actions, soft keys, the navigation stack, incremental updates; translations through `tr`; the same app runs under Node (the simulator) | A system app (calculator or notes) rewritten in KarioUI passes the selftest on both engines. Developer machine: ✓ (§5.2); QEMU: still to run |
| P3 | Tools: the Kario Studio extension (new project, simulator panel, run and debug, preview, package and upload) | A newcomer goes from installing to running their own app in the simulator in 30 minutes or less. The extension builds, its unit tests pass, the `.vsix` installs (§5.2); clicking through it in an editor and timing those 30 minutes **not done** |
| P4 | Guidelines, tutorials, test framework, review guidelines, privacy labels; mini-programs moved onto the same engine | Every document is on the developer portal; third-party apps run tests with the test framework. Guidelines, tutorial, test framework, review guidelines, privacy labels ✓ (33, 34); the portal, moving mini-programs **not implemented** |
| P5 | Native modules: C-interface libraries a TypeScript app brings (arm64, signed with the package) that give the JS functions in the same process (codecs, cryptography, a database, its own network library), reviewed as native apps; whether big apps get an engine with a JIT (V8, Hermes) is decided on measurements. The design is in §5.3 (**not implemented**) | A sample app does something QuickJS cannot do fast (image processing, say) in a native module, measured on the emulator against plain TypeScript |

### 5.1 P1: what is done on the developer machine

P1 is done on the developer machine; the unit tests and the selftest pass. The selftest and the measurements on QEMU wait for the lead's image build (not run on QEMU yet, **not on a real phone**).

| Part | Where | Verified |
|------|--------|------|
| QuickJS-ng 0.18.0 (MIT) | Its sources are not in the repository: `make jsrt` fetches the release by version and SHA-256 into `build/` and keeps a copy in `~/.cache/kario` (`QJS_CACHE`); `make build` and `make test` build the runtime when that copy is there, and without it (offline and no cache) they say so and leave out the runtime and the TypeScript sample; offline, `make jsrt` fails to download, and a wrong digest is refused. Yocto: `recipes-phone/quickjs` (meta-openembedded has no QuickJS recipe), a static library only, no package of it in the image | Developer machine ✓; recipe written, image not built |
| `kario-jsrt` | `jsrt/` (C), speaking the protocol through `sdk/c`; in the image `recipes-phone/kario-jsrt` installs it in `lib/phone/bin`, added to `PHONE_IMAGE_PACKAGES` | Unit tests ✓ (driven by a fake Shell: pages, actions, keys, inputs, capability calls as promises with the platform's error text, timers, tombstones, resync, storage and its limit, the translation fallback, bytecode, `std` and `os` out of reach); selftest ✓ |
| `@kario/sdk` | `sdk/ts/`: `index.d.ts` written by hand (the `kario` module), `ui.d.ts` generated from `ui/` by `tools/tsgen`; `make test` fails when it is stale | ✓ (the sample and the template checked with tsc 5.9 strict; the build uses no tsc and needs no Node) |
| `kario` command line | `cmd/kario`: `new`, `build` (esbuild's Go API, ES2023), `package` (`pkg.Build`, keys as phone-pkg's, or a development store); `run`, `test`, `publish` **not implemented** | Unit tests ✓ |
| Launch and installation | The manifest's `runtime: "js"` and `main`; the Shell starts `kario-jsrt <app dir>/main.js` in the app's sandbox (`shell/jsapp.go`); the installer and the store's review apply the same `jsrt.Check` | Unit tests ✓ |
| Sample | `examples/hello-ts`: a list of notes, an input, the right soft key "Clear all", the clipboard, storage, a tombstone, a notice that goes away after 3 seconds | Selftest "TypeScript app" ✓ (text UI and phone-paint's pixel UI) |

Measurements: on the developer machine (x86-64, Xeon E5-2670 v3), processes started the way the Shell starts them (the protocol on fd 3, `jsrt/measure_test.go`, without the sandbox), the median of 20 runs each. Memory is the Rss and Pss of `/proc/<pid>/smaps_rollup` 300 ms after the first page (`status`'s VmRSS reads low for a Go program with threads: the kernel keeps counts per thread and folds them in now and then). The Go apps are built the way the device builds them (`CGO_ENABLED=0`, `-trimpath -ldflags='-s -w'`, static).

| App | Package | Start to first page | Rss | Pss |
|------|----|------------|-----|-----|
| hello-ts, `main.js` (1.9 KB) | 14 KB (7.5 KB gzipped) | 1.9 ms | 3.1 MB | 1.4 MB |
| hello-ts, `main.jsc` (3.0 KB) | (system image only) | 1.8 ms | 3.0 MB | 1.3 MB |
| examples/notes (Go) | 2.5 MB (1.1 MB gzipped) | 3.3 ms | 4.7 MB | 4.7 MB |
| apps/calc (Go) | — | 3.3 ms | 4.8 MB | 4.8 MB |
| A synthetic large app, `main.js` 294 KB | — | 42 ms | 9.1 MB | 7.3 MB |
| The same, `main.jsc` 558 KB | — | 6.7 ms | 5.5 MB | 3.7 MB |

- The runtime itself: `kario-jsrt` is 1.2 MB stripped (x86-64, `-O3`, the engine linked in statically), one copy shared by every TypeScript app; each Go app carries its own runtime (notes' program is 2.5 MB). A TypeScript app's Pss is small because libc's and `kario-jsrt`'s code pages are shared between processes, while a static Go program uses its own alone.
- Optimised for size (`-Os`, `CMAKE_BUILD_TYPE=MinSizeRel`): `kario-jsrt` is 0.68 MB, hello-ts starts as fast (2.2 ms / 1.7 ms) with 0.9 MB Pss; only a large bundle's source parses more slowly (294 KB: 52 ms, bytecode 7.0 ms). Which one the device gets waits for numbers from QEMU and a real phone; the Yocto recipes use their default compiler flags for now.
- In the real Shell (the memory report at the end of the selftest, with the sandbox, development build): `kario-jsrt` running hello-ts 1.3 MB PSS (0.3 MB private dirty), `phone-app-calc` 4.5 MB.
- A small app's source parses in under 1 ms; bytecode pays off from bundles of a few hundred KB: 294 KB go from 42 ms to 6.7 ms, with half the memory (no parser structures left behind). Bytecode is 1.9 times the size of the source and does not resist a hostile writer, so it is precompiled in the system image only; apps from the store run their source, and precompiling on the device after installation is **not implemented**.
- The same measurements on QEMU (aarch64, TCG emulation) wait for the lead's image build (**not implemented**).

### 5.2 P2–P4: what is done on the developer machine

Unit tests and the selftest pass on the developer machine; **not run** on QEMU or a real phone (that waits for the lead's image build: with the calculator in TypeScript, the device's selftest runs it on `kario-jsrt`).

| Part | Where | Verified |
|------|--------|------|
| KarioUI (`@kario/ui`) | `sdk/ts/karioui/index.ts`: components (`Text`, `Item`, `Tile`, `Input`, `Button`, `Progress`, `Image`, `Doc`, `Media`, `Web`, `Drawing`, `List`), actions `act`, pages `page`, the navigation stack `nav`, state `state` (`store` into storage, `save` for the tombstone), lifecycle and events (`app`), `tr`; a change of state draws every page of the stack again and sends only the pages whose JSON changed (`update`); on leaving the foreground the page stack, the pages' parameters and the saved states go to the tombstone, and come back as they were. `kario build` bundles it into `main.js` with an esbuild plugin (tree-shaken, only what is used), no npm package in the project | `kario test` of the calculator, the tutorial's sample and the template ✓; tsc 5.9 strict ✓ |
| The `kario` module under Node | `sdk/ts/node/`: `runtime.mjs` written after `jsrt/src/kario.c` and `dispatch.c` (the page stack's mirror and resync, capabilities as promises with the system's own error text, storage and its 1 MB limit, the translation fallback, the tombstone's 64 KB, ping, the microtasks a message starts run before the next message); `kario.mjs` speaks the socket on fd 3, `console` goes to the log and to the debugger; `run.mjs` installs import hooks, so `main.js` can import only `kario`; the language table is generated by `tools/tsgen` | `jsrt/node_test.go`: the runtime cases of `jsrt` pass under Node too (except the escape case: Node is no sandbox) |
| `kario run` | `cmd/kario/run.go`: builds (with a source map), writes out the Node runtime, starts `phone-shell --dev-app <build> --dev-runner [...]`; `-engine jsrt` switches to kario-jsrt; `-preview`, `-inspect`, `-brk`, `-device`, `-lang`; `-watch` (needs `-preview`) builds again on save and sends SIGUSR2, and the Shell restarts the developer's app (`ReloadDev` in `shell/devapp.go`) | Unit tests ✓; `-watch` checked by hand in the developer machine's Shell: the calculator with "1+2" typed, the title changed and saved, the new build back with "1+2" about 40 ms later; a failed build leaves the old one running |
| The developer's app in the Shell | `shell/devapp.go`: the `--dev-app` directory comes after the system's apps (in place of one with the same ID) and opens at start; `--dev-runner` starts it outside the sandbox, so a debugger can reach it; with the device's services `phone-shell` refuses `--dev-runner` | Selftest (next row) ✓ |
| Calculator | `apps/calc`: rewritten in TypeScript + KarioUI, it replaced the Go one (the Go one is deleted, still in git); the expression being typed goes to the tombstone (a finished calculation does not; the Go one had none); after an error the expression stays to be fixed (the Go one cleared it on the following keys); numbers formatted as Go's `strconv.FormatFloat` does (the exact value rounded half to even); bytecode in the image, Yocto compiles it with `KARIO_CLI` | `kario test` 8 cases ✓; the selftest's cases that use the calculator ✓ on kario-jsrt and ✓ on Node (`--dev-runner`); a new case, "KarioUI calculator" (the tombstone), ✓ on both engines |
| `kario test`, `@kario/test` | `cmd/kario/test.go`, `sdk/ts/kariotest/index.ts`, `sdk/ts/node/test.mjs`, `fake.mjs`: the app's `main.js` runs on the same runtime under Node, a fake Shell at the other end; node:test runs the cases (one process, errors mapped back to the `.ts`) | `go test ./cmd/kario` ✓ (the template's cases pass, a failure is reported, `-run`) |
| `kario publish` | `cmd/kario/publish.go`: `POST /v1/submissions` (`?rollout=`), as `phone-pkg submit` | Unit tests ✓: a fake store, and the store backend's open core `appstore` (`cmd/kario/store_test.go`: a new app waits for review, the catalogue carries the privacy label, a staged update, sending one more kind of data goes back to review); run by hand on the developer machine against the official store `kario-storesrv` (register, verify, `kario package`, `kario publish`, review, catalogue) |
| Privacy labels | `ui/privacy.go`: kinds, uses, `CheckPrivacy`, called by `kario build`, `shell/install.go` and `appstore`; catalogue entries carry `privacy`, a staged rollout's old version its old label (`pkg.OfferedApps`); the store app's Details page `apps/store/privacy.go`; a column on the staff desk (not open source) | Unit tests of `ui`, `pkg`, `appstore`, `apps/store`, `cmd/kario` ✓; the portal's browser test ✓; the Details page on a device **not run** (the selftest has no store backend) |
| The tutorial's sample | `examples/karioui-notes` ([34](34-app-tutorial.md)) | `kario test` 6 cases ✓ |
| Kario Studio | `ide/vscode`: commands (new, build, run in a terminal, preview panel, debug, stop, tests, package, upload, write a debug configuration), settings `kario.*`, calling the `kario` command line only; dev dependencies only typescript (Apache-2.0), @types/vscode and @types/node (MIT), with a lockfile; the `.vsix` is written by `scripts/vsix.mjs` rather than @vscode/vsce (its dependency @vscode/vsce-sign is not under an open licence) | 9 unit tests without VS Code ✓; the `.vsix` installed into an empty extensions directory by Cursor's server program ✓; clicking through it in an editor (panel, breakpoints) **not done** |
| Guidelines, tutorial | [33](33-app-guidelines.md), [34](34-app-tutorial.md) | The tutorial's code is taken from the tested sample |

Measurements: the same machine and the same method as §5.1 (`jsrt/measure_test.go`, the median of 20 runs); this time other builds and tests ran on the machine at the same time, so the numbers vary more than in §5.1 (hello-ts 2.0 ms now, 1.9 ms in §5.1).

| App | Package | Start to first page | Rss | Pss |
|------|----|------------|-----|-----|
| Calculator (TypeScript + KarioUI), `main.js` (7.2 KB) | 21 KB (11 KB gzipped) | 3.6 ms | 3.3 MB | 1.6 MB |
| The same, `main.jsc` (12.4 KB) | (system image only) | 2.6 ms | 3.2 MB | 1.5 MB |
| The same, Node.js 24 (what `kario run` and `kario test` use) | — | 51 ms | 53 MB | 46 MB |
| Calculator (Go, the one it replaced) | program 2.5 MB | 3.7 ms | 4.8 MB | 4.8 MB |
| hello-ts (no KarioUI), `main.js`, the same run | 17 KB | 2.0 ms | 3.1 MB | 1.4 MB |

- In TypeScript the calculator's Pss is about 3.2 MB lower (a Go program's code pages are its own, `kario-jsrt`'s are shared), it starts about as fast, 1 ms faster from bytecode.
- What KarioUI costs: about 5 KB of the calculator's `main.js` is KarioUI; about 1.6 ms slower than hello-ts (source) and 0.2 MB more Pss, half of it won back by bytecode.
- Node is for the developer machine only: tens of MB and 50 ms do not matter for debugging, so the device stays on kario-jsrt.
- The numbers on QEMU and a real phone are **not measured**.

### 5.3 P5: native modules (a design, not implemented)

TypeScript for the interface and the logic; the slow parts (codecs, image processing, cryptography, databases, an app's own network protocol) written in C, C++ or Rust as shared libraries, signed with the package, which the JS calls in the same process. Everything in the table is **not implemented**.

| Aspect | Design |
|------|------|
| Interface | An engine-neutral C interface (`sdk/c/kario_native.h`, in the spirit of Node-API): the library exports `kario_native_init`, through which it registers its functions; arguments and results are only numbers, strings, `ArrayBuffer`s (not copied, lent to the C code until the function returns) and JSON. C code never touches QuickJS objects, so a move to Hermes needs no rebuilt modules; the interface has a version, checked on loading |
| The JS side | `import { native } from "kario"`, `native("imgproc").blur(pixels, w, h, 3)`; the developer writes the module's types in a `.d.ts` |
| Threads | By default a function runs synchronously on the JS thread (short functions); one registered as asynchronous runs on a `kario-jsrt` worker thread and returns a Promise, its result coming back through the message loop; an app's worker threads are capped, its CPU is the cgroup's as before |
| Package and manifest | The manifest says `"native": ["imgproc"]`, the library is `native/<arch>/libimgproc.so` (`aarch64` for devices, `x86_64` for the developer machine); `jsrt.Check`, which refuses every executable file today, then allows shared libraries of the right architecture in those places when `native` is declared, and nothing elsewhere. `kario-jsrt` links libc dynamically, so `dlopen` is available |
| Sandbox and review | The same process, the same sandbox (namespaces, seccomp, Landlock, the per-app firewall), capabilities only through `kario.*` as before: native code can do no more than an app written with `sdk/c` today (hello-cpp). Every version of an app with `native` goes to the human review and is reviewed as a native app (33 §2.2) |
| Developer machine | Run with `kario run -engine jsrt` (kario-jsrt loads the `x86_64` library; attach gdb to debug the C code); under Node it needs a layer from this interface to Node-API, later |
| Building | `kario build` compiles no C; developers build with the SDK Yocto exports, or `clang --target=aarch64-linux-gnu` with the system's sysroot, and `kario package` puts `native/` in the package |

Still to decide: one package per architecture or one package with both (the store sending a device its own); a crash report of native code (SIGSEGV) has no JS call stack; whether big apps get an engine with a JIT, once the completion criterion's measurement is in.

## 6. Risks

- **Differences between the two engines**: Node (V8) on the developer machine, QuickJS-ng on the device. Only standard ECMAScript and `kario.*` are allowed, the type definitions have no Node or browser interfaces, and under Node too `main.js` can import only `kario`; the runtime cases of `jsrt` run on both engines (`jsrt/node_test.go`), and the calculator passes the selftest on both. Globals such as `process` can still be reached under Node, and Node is no sandbox: running on the developer machine does not mean running on the device; the selftest on kario-jsrt decides.
- **Performance**: QuickJS is an interpreter and computes much more slowly than V8. The system draws the UI and apps only manage state, which is usually enough; heavy work (image decoding, cryptography, media) goes to the system services. Switch to Hermes if measurements say otherwise.
- **Ecosystem**: most npm packages rely on Node or browser interfaces and cannot be used as they are; pure-logic libraries can be bundled in.
- **People**: the type definitions are generated from `ui/caps.go`, so the interface changes in one place and both sides follow. The Go SDK is not deprecated; system apps can move over gradually.
