# 教程：用 KarioUI 写一个应用

> 一步一步写出一个便签应用：列表、输入、第二页、动作和确认、存储和墓碑、剪贴板能力、翻译、测试、调试、打包上架。成品是 `examples/karioui-notes`，文中的代码都摘自它，它的测试（`kario test`）通过。
> 状态：KarioUI、`kario run`、`kario test`、`kario publish`、Kario Studio 扩展在开发机上可用（[29](29-app-development.md) §5.2）；QEMU 和真机上**未跑**。界面怎么设计、商店审什么见 [33](33-app-guidelines.md)，能力和协议见 [22](22-developer-api.md)。

## 0. 准备

| 要什么 | 怎么来 |
|--------|--------|
| `kario` 命令行和开发机上的 Shell | 在 Kario 的源码树里 `make build`：`build/bin/kario`、`build/usr/bin/phone-shell`、`kario-jsrt`；把 `build/bin` 放进 `PATH` |
| Node.js 22.15 或更新 | `kario run`（在 Node 下跑、调试）和 `kario test` 要；`kario build`、`kario package` 不要 |
| 像素界面的预览（可选） | `make paint`（phone-paint） |
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
```

`kario run` 先编（带源码映射），再启动 `phone-shell --dev-app`：应用放在系统应用后面（ID 相同就顶替它），一开机就打开。用 Node 跑时 Shell 不给它套沙箱，好让调试器连得上；这是开发机专用的，设备上的 Shell 不接受（[29](29-app-development.md) §5.2）。应用的日志（`console.log`）在 Shell 的运行目录里（开发机上是 `~/.local/state/phone-sim/run/<应用 ID>.log`），连着调试器时也在调试控制台。`-watch` 时改了代码存盘，Shell 照被系统结束再打开那样重启应用（§6）：交给墓碑的状态和输入框里的字都在；改了 `manifest.json` 要重新 `kario run`。

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
  expectEqual(app.calls, [{ cap: "clipboard.set", args: { text: "Milk" } }], "the calls");
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

| `@kario/test` | 做什么 |
|---------------|--------|
| `test(名字, fn)` | 一个用例（node:test） |
| `launch({ lang, arg, restored, storage, capabilities })` | 在假 Shell 下启动应用；`capabilities` 按名字给能力的回答，抛错就是失败（消息写 `cancelled` 是用户取消） |
| `app.key(键)`、`app.type(文字)`、`app.fill(文字)` | 按键：`enter`、`esc`、`f2`（右软键）、方向键、`backspace`、一个字符（输入框里打字，否则是快捷键） |
| `app.press(id 或文字, 动作?)`、`app.focusOn(…)`、`app.menu()`、`app.choose(标签)` | OK 一个节点；移焦点；「选项」菜单里有什么；选一项（自动确认） |
| `app.expect(文字)`、`app.expectGone(…)`、`app.at(页面 ID)`、`app.wait(文字, 毫秒)` | 断言屏幕；等定时器、能力的回答 |
| `app.screen`、`app.stack`、`app.focus`、`app.text()`、`app.toasts`、`app.calls`、`app.confirmed`、`app.storage`、`app.log` | 应用发来的东西 |
| `app.back()`、`app.background()`、`app.kill()`、`app.relaunch()`、`app.resync()`、`app.event(名字, 值)` | 返回；离开前台（返回它交出的状态）；系统结束它；墓碑后重启；新 Shell 接管；系统事件 |

假 Shell 的焦点和按键是真 Shell 的简化（[22](22-developer-api.md) §3）：判断「在真机上对不对」以 Shell 的自测为准（[22](22-developer-api.md) §10）。系统应用计算器（`apps/calc`）两种都有：`kario test` 的用例，和 `shell/selftest.script` 里的自测。

## 10. 在 VS Code / Cursor 里

装 `ide/vscode` 编出的 `kario-studio-0.1.0.vsix`（`code --install-extension …` 或 `cursor --install-extension …`），设置 `kario.cli` 指向 `kario`：

| 命令（`Kario: …`） | 做什么 |
|--------------------|--------|
| New App | `kario new`，然后打开文件夹 |
| Build | 编；错误进「问题」面板 |
| Run in the Terminal / Run in the Preview Panel | 终端里的文本界面；编辑器旁边的面板里是像素界面，面板里的键盘就是手机的键，存盘就重新载入（设置 `kario.reloadOnSave`） |
| Debug (Node.js) | 预览面板加上调试：VS Code 的 Node 调试器附加到应用，`src/*.ts` 里下断点 |
| Run Tests | `kario test` |
| Package / Publish to the Store | 签名打包；交给商店审核 |
| Add a Debug Configuration | 往 `.vscode/launch.json` 和 `tasks.json` 里加配置，以后按 F5 |

## 11. 打包和上架

```bash
kario package -key dev.key -cert dev.cert -o notes.phoneapp notes   # 开发者的密钥和商店发的证书（phone-pkg keygen / register）
kario package -devstore ~/devstore -o notes.phoneapp notes          # 或者开发商店签名，开发者模式的手机认
kario publish -store https://store.example.com notes.phoneapp      # POST /v1/submissions：新应用、新权限进人工审核
kario publish -store https://store.example.com -rollout 10 notes.phoneapp   # 更新先给 10% 的设备
```

商店审什么见 [33](33-app-guidelines.md) §2。便签不上网，商店的「详情」页自己会写「数据不出手机」；要上网的应用在清单里加隐私标签 `privacy`（33 §3），说清哪些数据发出手机、做什么用，`kario build` 先核对它和权限对不对得上。

## 12. KarioUI 参考

| 名字 | 是什么 |
|------|--------|
| `Text(文字, { style, id, actions })`、`Section(文字)` | 一段文字（`style`：`dim`、`accent`、`title`、`ok`、`warn`、`big`、`display`）；分组标题 |
| `Item(标题, { id, subtitle, trailing, badge, icon, src, style, onOk, actions })`、`Tile(…)` | 列表项；宫格里的一格（页面的 `columns`） |
| `Input(id, { placeholder, value, secret, keyboard, clear, onSubmit, onChange })` | 输入框；`value` 给一个 `state` 时跟着用户输入走；`onChange` 是边打边搜 |
| `Button(文字, onOk)`、`Progress(比例, { text, trailing })`、`Image(src, …)`、`Doc(markdown)`、`Media(stream)`、`Web(src, { id })`、`Drawing(画布, { id, onKey })` | 按钮、进度条、图片、文档、视频流、网页、画布 |
| `List(数组, (项, 下标) => 节点)` | 一组节点；页面的 `body` 里可以放数组和 `false` / `""`（`条件 && Text(…)`） |
| `act(id, 标签, 做什么, { key, group, confirm, hidden, primary })` | 动作 |
| `page(id, setup, { onPop, transient })` | 页面：`setup(参数)` 返回视图（或者返回渲染函数）；视图有 `title`、`body`、`actions`、`right`、`center`、`focus`、`bottom`、`columns`、`layout` |
| `nav.push`、`nav.replace`、`nav.reset`、`nav.pop`、`nav.depth`、`nav.top` | 页面栈 |
| `state(初值, { store, save })`：`value`、`set`、`update` | 状态；`update(f)` 里可以就地改，照样重画 |
| `refresh()` | 状态以外的东西变了（定时器改了变量）时重画 |
| `app(根页面)` 或 `app({ root, onStart, onOpen, onEvent, onForeground, onBackground, onNotif, save })` | 运行应用 |
| `tr`、`call`、`cancelled`、`toast`、`notify`、`storage`、`lang`、`appId` | `kario` 模块里的同名接口（[22](22-developer-api.md) §9.2） |

## 13. 两个引擎

开发机上 `kario run` 和 `kario test` 用 Node.js（V8），设备上是 QuickJS-ng（`kario-jsrt`）。两边的 `kario` 模块行为一样（`jsrt/` 的运行时测试在两个引擎上都跑），差别要自己留意：

- 只用标准 ECMAScript 和 `kario`：Node 里 `process`、`Buffer` 这些全局变量摸得到，设备上没有；类型定义里没有它们，`tsc` 会报错。`main.js` 在 Node 里也只能导入 `kario`。
- Node 不是沙箱：开发机上的应用不受权限以外的限制（文件、网络），设备上有（[17](17-app-sandbox.md)）。
- 速度：QuickJS 是解释器，比 V8 慢得多；重的计算放到系统服务里，真机上量。
