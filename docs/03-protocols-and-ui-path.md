# 协议与 UI 路径

实施：[05-implementation.md](05-implementation.md)。GUI：[06-gui-architecture.md](06-gui-architecture.md)。

## 1. 技术栈

| 组件 | 技术 |
|------|------|
| telephonyd | **C++23**，ModemManager D-Bus |
| phone-tui | **Go** + Bubble Tea |
| phone-gui | **Rust** + **Slint**（GPU，闭源） |
| IPC（系统服务） | **D-Bus `phone.*`** |
| IPC（界面） | **`phone.Ui`**：应用 → Shell 的语义树，Unix socket（草案） |

## 2. 边界

- TUI / GUI / 第三方应用 **只调 `phone.*`**，不直连 Modem。  
- telephonyd 独占 ModemManager。  
- 音频：PipeWire/Pulse；UI 经 `phone.Audio`。  
- 语义应用不自己画界面：经 `phone.Ui` 交出内容与动作，由 Shell 渲染并处理按键（[10](10-keyboard-interaction-model.md)）。

## 3. Linux 映射

| 能力 | 上游 |
|------|------|
| 蜂窝 | ModemManager |
| 音频 | PipeWire 或 PulseAudio |
| 电源 | power_supply / upower |
| 输入 | evdev → phone-tui / phone-gui |
| 显示 | DRM/KMS；**org.phone.Display**（phone-displayd）；60Hz 基线，120Hz 可选 |

## 4. `phone.*` D-Bus v0

```
phone.Telephony v1
  properties: registration, signal, operator, sim_state
  methods:    dial, hangup, answer, reject, send_dtmf
  signals:    incoming_call, call_state_changed, sim_changed

phone.Power v1
  properties: percent, charging
  signals:    low_battery

phone.Audio v1
  methods:    set_call_volume, set_route (earpiece | speaker)
  signals:    route_changed

phone.Display v1
  properties: CurrentRefreshRate, MaxRefreshRate, Policy
  methods:    HintInteractionBegin, HintInteractionEnd, HintAnimationUntil
  signals:    RefreshRateChanged
```

详情：[08-display-adaptive-refresh.md](08-display-adaptive-refresh.md)。版本：major 破坏性变更；minor 只加字段。

## 5. `phone.Ui` v0（原型已实现）

定义：[ui/proto.go](../ui/proto.go)（界面）、[ui/caps.go](../ui/caps.go)（能力）。节点：`text / section / item / input / button / image / media / doc / progress`（`doc` 为 Markdown，系统渲染；`progress` 是进度条，`fraction` 0–1）；每个可操作节点带 `actions[]`（`id / label / key / primary / group / confirm`）。`input` 可带 `keyboard`（`number / phone / email`），告诉系统输入法该用哪种键盘（[16](16-input-method.md) §4）；带 `live` 的输入框每次编辑都会发 `changed` 动作（边输边搜）。`Screen.columns` 让连续的 `tile` 项排成宫格（主屏、计算器），方向键在宫格内二维移动。动作可设 `hidden`：只响应快捷键，不进菜单。语法与示例见 [10](10-keyboard-interaction-model.md) §4。

`req` 可带 `origin`：宿主应用代其承载的内容（如小程序）请求能力时填写，系统对话框会标注；授权与责任仍归宿主应用。

**连接**：Shell 启动应用时创建 socketpair，子端作为 fd 3 交给应用（环境变量 `PHONE_UI_FD=3`）。应用没有其他途径接触系统服务，身份即这条连接。

| 方向 | 消息 |
|------|------|
| 应用 → Shell | `hello`（协议版本）、`push / replace / update / reset / pop`、`toast`、`notify`、`req`（能力请求） |
| Shell → 应用 | `start`、`action`（含该页所有输入框的值）、`popped`、`lifecycle`、`res`、`event`、`notif_action` |

**清单（manifest）**：`permissions`（如 `camera`、`keystore`、`voip`、`phone`、`sms`、`contacts`；`net` 才能联网）、`background`（后台模式，目前只有 `voip`：离开前台后不冻结）、`granted`（预授权运行时权限，只对系统镜像里的应用有效）。沙箱与后台规则见 [17](17-app-sandbox.md)。

**能力**（`req.cap`，由 Shell broker 检查 manifest 权限与运行时授权后转发）：`media.call.start / control / end`、`scan.qr`、`auth.confirm`、`keystore.sign`、`attest.get`、`perm.list / set`（仅系统应用）；电话 `tel.dial / answer / hangup / hold / mute / calls / log` 与短信 `sms.send / list / read / threads / alerts / delete / block / blocked`（经 phone-smsd，[32](32-messaging.md)；收发在 telephonyd，[04](04-ui-language-telephonyd.md)）；`contacts.list`（运行时权限「通讯录」）；`alarm.set / cancel / list`（系统按时触发）。完整目录与规划见 [12](12-super-app-capabilities.md)。

| 约束 | 原型 | 量产 |
|------|------|------|
| 序列化 | JSON 行 | 紧凑二进制 + 增量更新（D-Bus 不适合频繁的大消息，仍不用） |
| 图片与视频 | 节点只写 `src` / `stream` 引用，像素走媒体平面 | 同，dmabuf |
| 版本 | 握手 `phone.ui/0` | major 破坏性变更；minor 只加字段 |

## 5.1 渲染器协议（Shell ↔ phone-gui，原型已实现）

Shell 与像素渲染器之间是另一条 JSON 行连接（phone-gui 的 stdin / stdout），与应用无关，应用接触不到：

| 方向 | 消息 |
|------|------|
| Shell → phone-gui | `hello`（设备形态：像素、缩放、键盘）、`frame`（当前屏幕：状态栏、标题、节点、焦点、滚动意图、软键、候选栏、提示、最上层系统界面）、`snap`（写 PNG）、`quit` |
| phone-gui → Shell | `ready`、`key`（按键名或文字，`long` 表示长按）、`snapped` |

定义见 `shell/frame.go`、`shell/gui.go`；设计说明见 [06](06-gui-architecture.md) §8。

## 6. 第三方

- **语义应用**（默认）：经 `phone.Ui` 描述界面，能力经 `phone-runtime` → D-Bus。  
- **画布应用**（例外）：页面里放 `canvas` 节点，交出图形，由系统画；画布聚焦时按键交给应用，应用必须处理按键事件（[22](22-developer-api.md) §3）。  
- **JS / TypeScript 小程序**（D5）：由宿主应用承载，JS 经宿主的 `phone.*` 桥请求能力，走同一个 broker，请求带 `origin`；视图同样是 `phone.Ui` 节点（[14](14-tui-route-and-app-framework.md) §8.3）。没有面向网页的通用 JS 桥；Web 视图组件（设计中）复用同一套 `phone.*` 接口。包格式见 [07-ecosystem-and-store.md](07-ecosystem-and-store.md)。
