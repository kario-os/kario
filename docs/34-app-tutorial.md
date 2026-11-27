# 教程：用 KarioUI 写一个应用

> 一步一步写出一个便签应用：列表、输入、第二页、动作和确认、存储和墓碑、剪贴板能力、推送、后台任务、翻译、测试、调试、打包上架。成品是 `examples/karioui-notes`，文中的代码都摘自它，它的测试（`kario test`，10 个用例）通过；推送和后台任务另有开发机上的自测用例（§7.1、§7.2）。
> 状态：KarioUI、`kario run`、`kario preview`、`kario test`、`kario publish`、Kario Studio 扩展在开发机上可用（[29](29-app-development.md) §5.2），这些工具只在开发机上跑；用 KarioUI 写的应用（计算器、教程的示例）在 QEMU 上自测通过；**未上真机**。界面怎么设计、商店审什么见 [33](33-app-guidelines.md)，能力和协议见 [22](22-developer-api.md)。只想先跑起来（也包括 C++、Go 写的应用和原生模块）：[35](35-quickstart.md)。

## 0. 准备

| 要什么 | 怎么来 |
|--------|--------|
| `kario` 命令行和开发机上的 Shell | 在 Kario 的源码树里 `make install-kario`：`make build`（`build/bin/kario`、`build/usr/bin/phone-shell`、`kario-jsrt`）、`make paint`，再把 `build/bin/kario` 链到 `~/.local/bin/kario`（[35](35-quickstart.md) §0） |
| Node.js 22.15 或更新 | `kario run`（在 Node 下跑、调试）、`kario test` 和 `kario preview` 要；`kario build`、`kario package` 不要 |
| 像素界面的预览（可选） | phone-paint（`make paint`，`make install-kario` 已经包括） |
| 编辑器（可选） | VS Code 或 Cursor，装 Kario Studio（`ide/vscode`，§10） |

## 1. 新建项目

```bash
kario new notes
cd notes
```

| 文件 | 是什么 |
|------|--------|
| `manifest.json` | 应用的 ID、名字、`"runtime": "js"`、权限（[22](22-developer-api.md) §1） |
| `src/main.ts` | 入口：用 KarioUI 写的页面 |
| `src/locale/<语言>.json` | 翻译表：英文原文对译文 |
| `src/previews.ts` | 预览：页面配上示例状态，`kario preview` 不跑应用就出图（§3）；`kario build` 不打包它 |
| `test/main.test.ts` | 测试，`kario test` 跑 |
| `tsconfig.json`、`types/` | 给编辑器的类型：`kario`（系统接口）、`@kario/ui`（KarioUI）、`@kario/test`（测试库）；`kario build` 用自己带的一份，不用装 npm 包 |

## 2. 第一页：状态和组件

KarioUI 里一页是一个函数，返回这一页的视图：标题、节点（`Text`、`Item`、`Input`……）、动作。函数读的状态（`state`）一变，页面就重画，只有变了的页面发给系统。完整的文件是 `examples/karioui-notes/src/main.ts`。

```ts
import { Input, Item, List, Text, act, app, nav, page, state, tr } from "@kario/ui";

interface Note {
  text: string;
  at: number;
}

const notes = state<Note[]>([], { store: "notes" });

const Main = page("main", () => ({
  title: tr("Notes"),
  focus: "new",
  body: [
    Input("new", {
      placeholder: tr("Write a note"),
      clear: true,
      onSubmit: (text) => {
        const t = text.trim();
        if (!t) return;
        notes.update((list) => [...list, { text: t, at: Date.now() }]);
      },
    }),
    List(notes.value, (n, i) =>
      Item(n.text, { id: `n${n.at}`, onOk: act("open", tr("Open"), () => nav.push(NotePage, { index: i })) }),
    ).reverse(),
    Text(notes.value.length ? tr("%d notes", notes.value.length) : tr("No notes yet"), { style: "dim" }),
  ],
}));

app(Main);
```

- 节点只说内容和动作：焦点、菜单、软键、返回、输入法、排版都是系统的。可操作的节点给稳定的 `id`，焦点跟着它走。
- `onOk` 是 OK 键做的事；`act(id, 标签, 做什么)` 是一个动作，`id` 不要变，标签在菜单里显示。
- `state(初值, { store })` 存进应用的存储，进程结束也还在；`{ save }` 交给墓碑（§6）。

## 3. 跑起来

```bash
kario run notes                               # 开发机上的 Shell，终端里的文本界面
kario run -preview 127.0.0.1:8790 notes       # 像素界面（phone-paint）在浏览器里，网页的键盘就是手机的键
kario run -preview 127.0.0.1:8790 -watch notes   # 存盘就重新编译，应用回到原来的页、状态还在
kario run -inspect 127.0.0.1:9229 -brk notes  # 等调试器连上再开始：Chrome 的 chrome://inspect 或 VS Code
kario run -engine jsrt notes                  # 换成设备上的引擎 kario-jsrt
kario run -sku qemu-t9-1g notes               # 按 SKU 跑：它的屏幕、键盘、内存档（kario skus 列出全部）
kario run -preview 127.0.0.1:8790 -profile out/trace.json notes   # 录性能时间线，停下时写出
kario preview notes                           # 不跑应用：src/previews.ts 里的页面在三种形态上各出一张图（下面）
kario run -preview 127.0.0.1:8790 hello-cpp   # C、C++、Go 写的应用：清单写 exec，程序先编好放在清单旁边（35 §3）
```

`kario run` 先编（带源码映射），再启动 `phone-shell --dev-app`：应用放在系统应用后面（ID 相同就顶替它），一开机就打开。用 Node 跑时 Shell 不给它套沙箱，好让调试器连得上；这是开发机专用的，设备上的 Shell 不接受（[29](29-app-development.md) §5.2）。应用的日志（`console.log`）在 Shell 的运行目录里（开发机上是 `~/.local/state/phone-sim/run/<应用 ID>.log`），连着调试器时也在调试控制台。`-watch` 时改了代码存盘，Shell 照被系统结束再打开那样重启应用（§6）：交给墓碑的状态和输入框里的字都在；改了 `manifest.json` 要重新 `kario run`。

**机型**：`-device` 只选形态（`bar-t9`、`bar-qwerty`、`communicator`）；`-sku` 选一款具体的手机（`kario skus` 列出 `sku/` 里的全部），Shell 照它的形态、键盘（全键盘的 Fn 层）、内存档（网页视图的内存上限）和地区跑。板子上的硬件（灯、基带、NFC、指纹）开发机上不模拟。

**不跑应用的预览**：像 SwiftUI 的预览，给页面配示例状态，`kario preview` 在 Node 下求值它（应用不启动），交给开发机的 Shell 和 phone-paint 出图。预览写在 `src/previews.ts`，页面和它读的状态要从 `main.ts` 导出。`kario new` 的模板（计数器）里就有一份（便签示例还没有，给它加的话导出 `Main` 和 `notes`）：

```ts
import { preview } from "@kario/ui";
import { Main, count } from "./main";

preview("new", Main, { state: () => count.set(0) });
preview("tapped", Main, { state: () => count.set(42) });
```

图写在 `<项目>/.kario/preview/<形态或 SKU>/<预览>.png`；`-device bar-t9,bar-qwerty`、`-sku qemu-t9-1g` 选画在哪些机型上，`-skin` 另出带机身的图，`-lang` 换语言。带参数的页面写 `preview("one", NotePage, { params: { index: 0 }, state: … })`。预览一个接一个跑，状态不会自动复原：每个预览把页面读到的状态都设好。

**性能时间线**：`-profile FILE` 让 Shell 录下应用的 CPU 和内存（每 250 ms）、发来的每一页（和 Shell 处理它花的时间）、能力调用（从请求到回答）、按键到页面更新的延迟、ANR 探测；停下 `kario run` 时写成 FILE（Chrome Trace Event JSON），拖进 [ui.perfetto.dev](https://ui.perfetto.dev) 或 `chrome://tracing` 看。用 Node 跑时旁边的 `trace.cpuprofiles/` 里还有 JS 的 CPU profile，VS Code 或 Chrome DevTools 能打开。只在开发机上有；量设备上的速度用 `-engine jsrt`，真机上以后再做。

## 4. 第二页：导航栈

页面可以有参数，`nav.push(页面, 参数)` 打开它，返回键关掉它（系统的，应用只听到 `onPop`）。参数是 JSON：系统在后台结束了应用，回来时就靠它重建这一页。

```ts
const NotePage = page("note", (p: { index: number }) => {
  const n = notes.value[p.index];
  if (!n) return { title: tr("Note"), body: [Text(tr("This note was deleted"), { style: "dim" })] };
  return {
    title: tr("Note"),
    body: [
      Text(n.text, { style: "big" }),
      Text(tr("%d characters", [...n.text].length), { style: "dim" }),
      Button(tr("Copy"), act("copy", tr("Copy"), () => copy(n.text))),
    ],
  };
});
```

一页要有自己的状态时，`page` 的函数返回渲染函数：外面这层在页面打开时跑一次，里面那层每次重画都跑。

## 5. 动作、软键和确认

列表项的其他动作进「选项」菜单，常用的给快捷键；删除标 `danger` 并写 `confirm`，系统先弹确认框。右软键放这一页最常用的事。

```ts
Item(n.text, {
  id: `n${n.at}`,
  onOk: act("open", tr("Open"), () => nav.push(NotePage, { index: i })),
  actions: [
    act("copy", tr("Copy"), () => copy(n.text), { key: "c" }),
    act("delete", tr("Delete"), () => notes.update((list) => list.filter((x) => x !== n)), {
      group: "danger",
      confirm: tr("Delete this note?"),
    }),
  ],
}),
```

```ts
right: act("clear", tr("Clear all"), () => notes.set([]), { group: "danger", confirm: tr("Delete all notes?") }),
```

## 6. 存储和墓碑

| 要留住什么 | 怎么写 | 留多久 |
|------------|--------|--------|
| 用户的数据 | `state(初值, { store: "键" })`，或者直接用 `storage` | 进程结束后还在（应用数据目录里，最多 1 MB） |
| 打开着的页面 | 页面和它的参数，KarioUI 自动交 | 系统在后台结束应用、用户回来时（墓碑，[17](17-app-sandbox.md) §5.3） |
| 界面上的临时状态 | `state(初值, { save: "键" })` | 同上 |
| 别的 | `app({ root, save() { … }, onStart(arg, restored) { … } })` | 同上，最多 64 KB |

输入框里没发出的字和焦点由系统还原（页面的 ID 不变就行）。

存储当场改内存，文件由运行时在后台写，存一次不等磁盘；写失败（磁盘满）不抛错，要知道就等 `storage.flush()`：在它之前存的都落盘了才 resolve，写失败时 reject（[22](22-developer-api.md) §9.2）。

## 7. 能力和权限

能力都是 `call(名字, 参数)`，返回 Promise；失败时的错误是系统的原话，用户取消时 `cancelled(err)` 为真。

```ts
async function copy(text: string): Promise<void> {
  try {
    await call("clipboard.set", { text });
    say(tr("Copied “%s”", text));
  } catch (err) {
    say(cancelled(err) ? tr("Not copied") : tr("Copy failed: %s", (err as Error).message));
  }
}
```

剪贴板不要权限。要运行时权限的能力（相机、位置……）在清单里声明权限，并写用途（1–80 个字，授权框里显示）：

```json
{ "permissions": ["camera"], "purposes": { "camera": "Scan a code to add a note" } }
```

`kario manifest check notes` 按安装器和商店的规则查清单（`kario build` 也查，只报第一个问题），列出全部问题；Kario Studio 的「Edit the Manifest」是同一套规则的表单（§10）。

### 7.1 推送：服务器发来的便签

应用不常驻：离开前台 10 秒就被冻结，内存紧张时被结束。服务器要给它东西，就经推送网关发推送（[21](21-push.md)），系统决定显示通知、短暂唤醒应用还是在后台启动它，再把数据作为 `push` 事件交给它（[22](22-developer-api.md) §5、§6）。便签这样收别处发来的便签：

1. 每次启动用 `push.register` 取令牌，交给自己的服务器（重装以后旧令牌作废，网关回 410）。
2. 服务器 `POST /v1/push`，带 `token`、`title`、`body`、`data`（[21](21-push.md) §2）：有标题和正文，系统显示通知，不唤醒应用（清单里要声明 `notifications`）；有 `data`，系统把它交给应用。
3. 应用在 `app({ onEvent })` 里收 `push`，值是 JSON：`{id, kind, data, sent, notified, collapse_id}`。

```ts
async function register(): Promise<void> {
  try {
    token.set((await call("push.register")).token);
  } catch (err) {
    console.warn("push.register:", (err as Error).message);
  }
}

// received adds the note a push's data carries ({"note": "..."}); the
// system may have thawed the app or started it in the background for it.
function received(p: PushEvent): void {
  let text: unknown;
  try {
    text = JSON.parse(p.data ?? "").note;
  } catch {
    return;
  }
  if (typeof text !== "string" || !text.trim() || notes.value.some((n) => n.push === p.id)) return;
  const t = text.trim();
  notes.update((list) => [...list, { text: t, at: Date.now(), push: p.id }]);
  if (!p.notified) say(tr("Received “%s”", t));
}

app({
  root: Main,
  onStart: () => void register(),
  onEvent(name, value) {
    if (name === "push") received(JSON.parse(value) as PushEvent);
    else if (name === "job" && value === DIGEST) digest();
  },
});
```

- 处理要快：冻结的应用被唤醒约 3 秒，不在运行的在后台启动（生命周期是 `background`，没有页面给用户看），做完就再被冻结；每个应用每小时至多 20 次唤醒，超出的数据等它回到前台再交。所以收到就存进 `state(…, { store })`，不要等用户。
- 同一条推送可能到两次（系统交给应用以后才向网关确认），按 `id` 去重。
- `notified` 为真说明系统已经显示了这条推送的通知，应用别再提示；应用在前台时系统不显示通知，`notified` 为假，由应用自己说（这里是一行「收到「…」」）。
- 要紧的数据端到端加密：`push.key` 取公钥交给服务器，服务器用 `push.Seal` 加密，应用收到后 `push.open` 解开（[22](22-developer-api.md) §4.2、[21](21-push.md) §4.1）；示例没加密。
- 令牌用 `fetch` 交给自己的服务器（[22](22-developer-api.md) §9.4）：清单里要有 `net`，隐私标签里申报 `identifiers`（令牌认得出这台设备上的这个应用）；地址只许 `https://`，开发时可以用本机回环上的 `http://`。示例在「推送和提醒」页（选项菜单，`p`）上填服务器地址（`keyboard: "url"`，输入法从英文开始），确定后 POST 到 `<地址>/register`：

```ts
async function sendToken(): Promise<void> {
  const base = server.value.trim().replace(/\/+$/, "");
  if (!base) return;
  if (!token.value) {
    say(tr("Not registered for push yet"));
    return;
  }
  try {
    const res = await fetch(base + "/register", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ app: appId, token: token.value }),
    });
    say(res.ok ? tr("Sent the token to %s", base) : tr("The server answered %d", res.status));
  } catch (err) {
    say(tr("Not sent: %s", (err as Error).message));
  }
}
```

  失败（连不上、超时、证书不对）都是 reject 一个消息以 `fetch: ` 开头的 `TypeError`；服务器回 4xx、5xx 不算失败，看 `res.ok`。`kario test` 不连网络，`launch({ fetch: () => ({ status: 200 }) })` 给回应，`app.fetches` 看应用发了什么（`test/notes.test.ts` 的令牌用例）。

在开发机上试：`kario test` 里 `app.event("push", JSON.stringify({ id, data, sent, notified }))` 扮演系统（`test/notes.test.ts` 的推送用例）；Shell 的自测脚本里 `push <应用> <标题> | <正文> | <数据>` 扮演服务器，经真的网关和 phone-pushd 发（[22](22-developer-api.md) §10）。自测用例「教程的示例（KarioUI 便签）」从商店装上这个应用，冻结时推一条便签，看到系统的通知、应用被唤醒、打开后便签在：

```bash
make selftest ONLY='教程的示例'
```

### 7.2 后台任务：每天提醒

要定期做点事（同步、清理、提醒），用后台任务：`job.schedule` 登记 `{id, every_min, charging?}`，到时系统唤醒应用或在后台启动它，发 `job` 事件，值是任务的 `id`（[22](22-developer-api.md) §4.7）。间隔至少 15 分钟；每次有个窗口（到期起半个周期，最多 30 分钟），系统把窗口里的任务合进一次唤醒，和推送共用每小时的唤醒预算；`charging: true` 的只在接着电源时跑，给重活用。同一个 `id` 再登记就替换旧的，重启后还在；`job.cancel` 取消。便签的「每天提醒」打开时登记一个每天一次的任务，到时发一条通知：

```ts
async function setReminder(on: boolean): Promise<void> {
  try {
    if (on) await call("job.schedule", { id: DIGEST, every_min: DAY_MIN });
    else await call("job.cancel", { id: DIGEST });
    reminder.set(on);
  } catch (err) {
    say(tr("Reminder not changed: %s", (err as Error).message));
  }
}

// digest is the background job's work: the app has a few seconds before it
// is frozen again, enough for one notification.
function digest(): void {
  const last = notes.value.at(-1);
  if (!reminder.value || !last) return;
  notify({ id: DIGEST, title: tr("%d notes", notes.value.length), body: last.text });
}
```

```ts
Item(tr("Daily reminder"), {
  id: "reminder",
  trailing: reminder.value ? tr("On") : tr("Off"),
  onOk: act("reminder", reminder.value ? tr("Turn off") : tr("Turn on"), () => setReminder(!reminder.value)),
}),
```

- 任务里只有几秒（和推送的唤醒一样），只做一件小事，结果存进存储；后台拿不到位置（定位只给前台）。
- 开关存进存储（`state(false, { store: "reminder" })`），和系统登记的任务对得上：关掉时 `job.cancel`，事件来了先看开关。系统拒绝时（间隔太短、没有 `id`）错误是系统的原话，开关不变。
- 通知要清单里的 `notifications`；同一个 `id` 的通知替换旧的，所以每天只留一条。
- 定期同步用后台任务，收消息用推送，不要用 `voip` 后台模式保活（[33](33-app-guidelines.md) §1.3、§2.2）。

测试：`kario test` 里 `app.event("job", "digest")` 扮演到期，`app.notifications` 是应用发的通知：

```ts
await app.choose("Push and reminders");
await app.key("enter");
expectEqual(app.calls.at(-1), { cap: "job.schedule", args: { id: "digest", every_min: 1440 } }, "the call");
await app.background();
await app.event("job", "digest");
expectEqual(app.notifications, [{ id: "digest", title: "1 notes", body: "Milk" }], "the notifications");
```

上面那个自测用例接着把 Shell 的时钟拨快一天（`timeshift 88200`），任务在后台跑了一次（`job com.example.kariouinotes digest runs 1`），系统显示了应用发的「1 条便签」通知；最后关掉提醒（`job.cancel`），从商店卸载。

## 8. 翻译

界面文字写成英文原文放进 `tr()`，译文放在 `src/locale/<语言>.json`（简体中文必须有，繁体中文和西班牙语缺的会回退）；整句翻译，`%s`、`%d` 的个数和顺序在每种语言里都一样。

```json
{
  "%d notes": "%d 条便签",
  "Added “%s”": "已添加「%s」",
  "Notes": "便签"
}
```

在 Kario 的源码树里，`python3 scripts/i18n-check.py` 检查每个 `tr("…")` 都有简体中文、每条译文都还在用。

## 9. 测试：kario test

`kario test` 把应用编好，在 Node 里对着一个假的 Shell 跑 `test/*.test.ts`：应用和真机上一样经协议收发（同一个 `kario` 模块的 Node 版），测试扮演用户按键、读屏幕。

```ts
import { expectEqual, launch, test } from "@kario/test";

test("OK opens a note; the clipboard is a capability; Back returns", async () => {
  const app = await launch({ capabilities: { "clipboard.set": () => ({}) } });
  await app.type("Milk");
  await app.key("enter");
  await app.key("down");
  await app.key("enter");
  app.at("note");
  app.expect("4 characters");
  await app.key("enter");
  app.expect("Copied “Milk”");
  expectEqual(app.calls.at(-1), { cap: "clipboard.set", args: { text: "Milk" } }, "the last call");
  await app.key("esc");
  app.at("main");
});
```

```bash
kario test notes                 # 全部
kario test -run tombstone notes  # 名字匹配的
kario test -lang es notes        # 应用以西班牙语启动
kario test -inspect notes        # 等调试器连上
```

用例可以分在 `test/` 下的几个文件里：每个文件在自己的 Node 进程里跑，几个文件同时跑，结果按文件名的顺序报。一个文件里每次 `launch` 都是新的应用和新的假 Shell，别的文件启动的应用、设的全局变量、失败的用例都带不过来；文件的用例跑完，它启动的应用就结束。`-inspect` 时所有文件在一个进程里依次跑，调试器只连一次。

| `@kario/test` | 做什么 |
|---------------|--------|
| `test(名字, fn)` | 一个用例（node:test） |
| `phoneZone(时区)` | 手机的时区（IANA 名称，如 `Asia/Shanghai`）：这个文件此后的代码和用例里 `Date` 的本地时间是它的，启动的应用也在这个时区（和 Shell 一样，[22](22-developer-api.md) §2）；每个用例从自己文件的时区开始；不设就是 UTC，和开发机的时区无关 |
| `launch({ lang, arg, restored, storage, capabilities })` | 在假 Shell 下启动应用；`capabilities` 按名字给能力的回答，抛错就是失败（消息写 `cancelled` 是用户取消） |
| `app.key(键)`、`app.type(文字)`、`app.fill(文字)` | 按键：`enter`、`esc`、`f2`（右软键）、方向键、`backspace`、一个字符（输入框里打字，否则是快捷键） |
| `app.press(id 或文字, 动作?)`、`app.focusOn(…)`、`app.menu()`、`app.choose(标签)` | OK 一个节点；移焦点；「选项」菜单里有什么；选一项（自动确认） |
| `app.expect(文字)`、`app.expectGone(…)`、`app.at(页面 ID)`、`app.wait(文字, 毫秒)` | 断言屏幕；等定时器、能力的回答 |
| `app.screen`、`app.stack`、`app.focus`、`app.text()`、`app.toasts`、`app.notifications`、`app.calls`、`app.confirmed`、`app.storage`、`app.log` | 应用发来的东西 |
| `app.back()`、`app.background()`、`app.kill()`、`app.relaunch()`、`app.resync()`、`app.event(名字, 值)` | 返回；离开前台（返回它交出的状态）；系统结束它；墓碑后重启；新 Shell 接管；系统事件 |

假 Shell 的焦点和按键是真 Shell 的简化（[22](22-developer-api.md) §3）：判断「在真机上对不对」以 Shell 的自测为准（[22](22-developer-api.md) §10）。系统应用计算器（`apps/calc`）两种都有：`kario test` 的用例，和 `shell/selftest.script` 里的自测。

## 10. 在 VS Code / Cursor 里

在 `ide/vscode` 里 `npm ci`、`npm run vsix` 编出 `kario-studio-0.1.0.vsix`，再 `code --install-extension …` 或 `cursor --install-extension …`（在 Cursor 经 SSH 的远程会话里，Cursor 终端里的 `cursor` 把它装到远程那台机器上；或者扩展视图的「从 VSIX 安装…」）。`make install-kario` 以后设置 `kario.cli` 用缺省的 `kario` 就行，不然指向 `build/bin/kario`（[35](35-quickstart.md) §2）：

| 命令（`Kario: …`） | 做什么 |
|--------------------|--------|
| New App | `kario new`，然后打开文件夹 |
| Build | 编；错误进「问题」面板 |
| Run in the Terminal / Run in the Preview Panel | 终端里的文本界面；编辑器旁边的面板里是像素界面，面板里的键盘就是手机的键，存盘就重新载入（设置 `kario.reloadOnSave`） |
| Debug (Node.js) | 预览面板加上调试：VS Code 的 Node 调试器附加到应用，`src/*.ts` 里下断点 |
| Debug on the Phone (Developer Mode) | 开发者模式的手机、信任过的数据线上：编、装上，应用在 kario-jsrt 的调试器下重启，同样在 `src/*.ts` 里下断点（调试类型 `kario`，[29](29-app-development.md) §5.5） |
| Run Tests | `kario test` |
| Choose the Device | 选一款 SKU（`kario skus`）或只选形态，状态栏上显示；运行、预览都用它 |
| Edit the Manifest | `manifest.json` 的表单：权限和用途、隐私标签、名字、版本这些；改了就写回文件，`kario manifest check -json` 的问题显示在字段旁边 |
| Preview the Views / … on Every Form Factor | `kario preview`：`src/previews.ts` 里的页面不跑应用就出图，显示在面板里，存盘重画 |
| Run with the Performance Timeline | 预览面板加 `-profile`；停下后可以打开 Perfetto、时间线文件、CPU profile |
| Package / Publish to the Store | 签名打包；交给商店审核 |
| Add a Debug Configuration | 往 `.vscode/launch.json` 和 `tasks.json` 里加配置，以后按 F5 |

## 11. 打包和上架

```bash
kario package -key dev.key -cert dev.cert -o notes.phoneapp notes   # 开发者的密钥和商店发的证书（phone-pkg keygen / register）
kario package -devstore ~/devstore -o notes.phoneapp notes          # 或者开发商店签名，开发者模式的手机认
kario publish -store https://store.example.com notes.phoneapp      # POST /v1/submissions：新应用、新权限进人工审核
kario publish -store https://store.example.com -rollout 10 notes.phoneapp   # 更新先给 10% 的设备
```

商店审什么见 [33](33-app-guidelines.md) §2。便签不上网，商店的「详情」页自己会写「数据不出手机」；要上网的应用在清单里加隐私标签 `privacy`（33 §3），说清哪些数据发出手机、做什么用（没有的话官方商店拒收），`kario build` 先核对它和权限对不对得上，没写时提示要补什么。

## 12. KarioUI 参考

| 名字 | 是什么 |
|------|--------|
| `Text(文字, { style, id, actions })`、`Section(文字)` | 一段文字（`style`：`dim`、`accent`、`title`、`ok`、`warn`、`big`、`display`）；分组标题 |
| `Item(标题, { id, subtitle, trailing, badge, icon, src, style, onOk, actions })`、`Tile(…)` | 列表项；宫格里的一格（页面的 `columns`） |
| `Input(id, { placeholder, value, secret, keyboard, clear, onSubmit, onChange })` | 输入框；`value` 给一个 `state` 时跟着用户输入走；`onChange` 是边打边搜 |
| `DateInput(id, { value, … })`、`TimeInput(id, { value, … })` | 系统的日期、时间输入框（`keyboard: "date"` / `"time"`）：值是 `YYYY-MM-DD`、`HH:MM`，用户看到地区的顺序加星期、自己的 12 / 24 小时制，← → 选年月日或时分、↑ ↓ 加减、数字直接打（[22](22-developer-api.md) §3）；`kario test` 的假 Shell 不模拟这套按键，用 `app.fill("2026-11-20")` 填值 |
| `Button(文字, onOk)`、`Progress(比例, { text, trailing })`、`Image(src, …)`、`Doc(markdown)`、`Media(stream)`、`Web(src, { id })`、`Drawing(画布, { id, onKey })` | 按钮、进度条、图片、文档、视频流、网页、画布 |
| `List(数组, (项, 下标) => 节点)` | 一组节点；页面的 `body` 里可以放数组和 `false` / `""`（`条件 && Text(…)`） |
| `act(id, 标签, 做什么, { key, group, confirm, hidden, primary })` | 动作 |
| `page(id, setup, { onPop, transient })` | 页面：`setup(参数)` 返回视图（或者返回渲染函数）；视图有 `title`、`body`、`actions`、`right`、`center`、`focus`、`bottom`、`columns`、`layout` |
| `nav.push`、`nav.replace`、`nav.reset`、`nav.pop`、`nav.depth`、`nav.top` | 页面栈 |
| `state(初值, { store, save })`：`value`、`set`、`update` | 状态；`update(f)` 里可以就地改，照样重画 |
| `refresh()` | 状态以外的东西变了（定时器改了变量）时重画 |
| `preview(名字, 页面, { params, state })` | `kario preview` 画的一页：`state` 设好示例状态，`params` 是页面参数（写在 `src/previews.ts`） |
| `app(根页面)` 或 `app({ root, onStart, onOpen, onEvent, onForeground, onBackground, onNotif, save })` | 运行应用 |
| `tr`、`call`、`cancelled`、`toast`、`notify`、`storage`、`lang`、`appId` | `kario` 模块里的同名接口（[22](22-developer-api.md) §9.2） |

## 13. 两个引擎

开发机上 `kario run` 和 `kario test` 用 Node.js（V8），设备上是 QuickJS-ng（`kario-jsrt`）。两边的 `kario` 模块行为一样（`jsrt/` 的运行时测试在两个引擎上都跑），差别要自己留意：

- 只用标准 ECMAScript 和 `kario`：Node 里 `process`、`Buffer` 这些全局变量摸得到，设备上没有；类型定义里没有它们，`tsc` 会报错。`main.js` 在 Node 里也只能导入 `kario`。
- Node 不是沙箱：开发机上的应用不受权限以外的限制（文件、网络），设备上有（[17](17-app-sandbox.md)）。
- 速度：QuickJS 是解释器，比 V8 慢得多；重的计算放到系统服务里，真机上量。
