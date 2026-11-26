# 显示基础设施：60–120Hz 自适应刷新

> 现状（2026-11-21）：**phone-displayd ✓**（Go，`programs/displayd`，和别的系统服务一样是 kario-system 里的一个程序：开发机上由 Shell 启动，设备上由 systemd 的 `phone-displayd.service` 启动）。它独占刷新率的决定：按 §5 的策略在 60 ⟷ 120Hz 之间自动切换，听 Shell 转来的按键、渲染器的动画、前台应用的 `display.hint`、正在播放的视频的帧率，电量低和手机发热时封顶，设置 › 显示与亮度 › 刷新率可以固定标准或高刷。像素渲染器 phone-paint（Rust，Apache-2.0）持有面板（DRM master）：在 CPU 上栅格化，默认也在 CPU 上合成；设 `PHONE_PAINT_GPU=1` 时用 GLES 2 合成、经 GBM 翻页上屏（[37](37-motion.md) §5 第 8 条，**未上真机**，Adreno、Mali、PowerVR 都没试过）。两条路都按 displayd 选的刷新率切换显示模式（另一档刷新率的模式尺寸一样，GPU 的缓冲照旧能用），动效每一步按面板的周期走，不画面板显示不了的帧。开发机和 QEMU 的自测里是模拟的 60/120Hz 屏，单元测试和自测 ✓；**真机上的模式切换未上真机**（§9）。本文写于用 Slint 的 phone-gui 当设备渲染器的时候；phone-gui 现在只用来对照（它不切换刷新率），下文的「渲染器」指 phone-paint。

> **D3（已定）：60Hz 基线；选到合适的 120Hz 屏时，再按本文启用 60↔120 自适应策略。** 理由：据目前了解，消费产品里的高刷屏基本从 5.9 寸左右起步，3.5 寸以下几乎找不到现成的 120Hz DSI 屏（需采购核实），定制屏起订量很高；按键交互是离散步进，高刷收益远小于手指拖动。`phone-displayd` 独占刷新率、应用只能发 hint 的设计照常适用：只有一种刷新率的屏上 displayd 读到一个模式，什么都不切换，设置里也没有刷新率这一行。所以 §1 / §2 里的「120Hz 峰值」只适用于选了 120Hz 屏的机型，60Hz 屏可以量产。详见 [09-ui-feasibility-review.md](09-ui-feasibility-review.md) §6。

与主流平台对齐的 **显示基础设施**：面板 **最高 120Hz**，系统 **在 60Hz 与 120Hz 间自动切换**——静态省电、交互与动效满刷、体验与旗舰同语义（ProMotion / 自适应高刷）。

关联：52-hardware-architecture.md（内部文档）、[06-gui-architecture.md](06-gui-architecture.md)、[05-implementation.md](05-implementation.md)、[23-renderer-protocol.md](23-renderer-protocol.md)（`rate`、`motion`）、[37-motion.md](37-motion.md)（动效按面板的周期走）。

## 1. 产品定义

| 项 | 要求 |
|----|------|
| 面板能力 | **120Hz 峰值**；至少支持 **60Hz 与 120Hz 两种 DRM mode**（或 **VRR 48–120Hz**，见 §2.1） |
| 系统策略 | **自动**：交互/动效 → **120Hz**；静态阅读/待机界面 → **60Hz**（屏有更低的模式时更低，最低 30Hz） |
| 120Hz 下 UI | 基准场景 **1% low ≥ 110fps**（§7） |
| 60Hz 下 UI | 基准场景 **1% low ≥ 55fps**；**禁止** 在 60Hz 模式仍按 120fps 空转 GPU |
| 触控到首帧 | 按键/触摸触发时 **立即请求 120Hz**（见 §4） |
| 来电全屏 | **120Hz** + Incoming 首帧 **< 100ms**（120Hz **未实现**：来电的脉动现在由 Shell 每半秒推进一次，升刷新率没有用；改由渲染器推进后随它的动画升，[37](37-motion.md) §3） |

TUI 阶段可用 60Hz 开发板；**选 120Hz 屏的机型，渲染器与 BOM 必须支持 60/120 双 mode（或 VRR）**，§2 是这类机型的约束；只有 60Hz 屏的机型可以量产（D3）。

## 2. 硬件（BOM 硬约束）

### 2.1 面板

| 组件 | 要求 |
|------|------|
| 接口 | **MIPI DSI**；SPI/I8080 彩屏 **不进入量产 GUI** |
| 刷新 | 规格书 **120Hz**；并明确 **60Hz 时序** 或 **VRR 范围**；面板驱动要能**无缝切换**（DSI 的 seamless mode switch），否则每次切换整屏闪一下 |
| LTPO | v1 **不强制**；有则 v2 在 `phone-displayd` 扩展细粒度档位（策略已按屏的全部模式挑：有 30Hz、48Hz 模式时静止和 24 帧视频会用上） |
| 背光 | PWM 不与当前刷新率产生可见拍频（60/120 切换时测） |

### 2.2 SoC / GPU

| 组件 | 要求 |
|------|------|
| DRM/KMS | **atomic modesetting**；`modetest` 可见 **60Hz 与 120Hz** mode（或单一 VRR mode） |
| GPU | **GLES 3.x / Vulkan**；两种刷新下均可稳定合成 |
| 带宽 | **120Hz 峰值** 满足 §6 帧预算 |

### 2.3 禁止

- 选 120Hz 屏时用只有 **120Hz** 一个 mode 的面板（静态时降不到 60Hz，省不了电）。  
- 无 **phone-displayd**、由应用随意 `ioctl` 改刷新率（策略必须集中）：应用碰不到显卡，只能经 broker 发 `display.hint`（§4.2）。

## 3. 软件栈

```
evdev ──► phone-paint ──key / motion──► Shell ──hint──► phone-displayd (Go)
             ▲  │                         │  ▲              │
             │  │ DRM master              │  └── rate ──────┘
             │  ▼                         │
             │ 面板（显示模式）            ├── 前台应用的 display.hint（限频）
             └──── rate ──────────────────┤── 播放中的视频的帧率
                                          └── 刷新率设置、电量低的封顶
phone-displayd ── 读 DRM 卡的模式（不当 master）；温控区发热时封顶
```

| 进程 | 职责 |
|------|------|
| **phone-displayd** | **唯一** 决定刷新率的进程；执行 §5 策略表；设备上读面板的模式（连上的连接器在当前尺寸下的全部模式和 CRTC 正在用的模式），开发机和自测里是模拟的 60/120Hz 屏 |
| **phone-paint** | 像素渲染器（CPU 上栅格化；合成默认在 CPU 上，可选 GPU 合成，只在 QEMU 上用软件 GPU 验证过，**未上真机**，[42](42-gpu-rendering.md)），持有面板：按 displayd 选的刷新率切换模式（`rate`），按面板的周期节流重绘，动画开始和结束时告诉 Shell（`motion`） |
| **Shell** | displayd 唯一的客户端：把按键、渲染器的动画、前台应用的 hint、视频、设置、电量转给它，把它选的刷新率转给渲染器（`shell/display.go`） |
| **telephonyd** | 来电/通话状态通知 displayd：**未实现**（§1） |
| **内核** | panel driver + 双 mode 或 VRR |

**为什么渲染器切换、displayd 决定**：显卡只能有一个 master，切换显示模式要 master；渲染器开机就打开显卡当 master，又在沙箱里（没有网络、不能新建套接字，[23](23-renderer-protocol.md) §5），只和 Shell 说话。所以决定在 displayd，执行在渲染器，经 Shell 转达——和安卓 DisplayManager 定策略、SurfaceFlinger 切模式是一个分工。displayd 读模式时也打开显卡：没有 master 时第一个打开的进程会成为 master，所以它等 Shell 说渲染器已经驱动着面板（`panel {kms}`）才去读，读一次就关上；万一抢到了 master 立刻放掉（`DROP_MASTER`）。

**phone-paint：** 能翻页时跟着翻页完成的事件走（面板 120Hz 时一秒 120 步，60Hz 时 60 步）；不能翻页时每步等一个面板周期，帧到得比面板快时合成一帧再画；无交互时不提交空帧（只画脏矩形，[37](37-motion.md) §5）。

## 4. 接口：`phone-displayd` 的 JSON 行套接字

> 早期草案是 D-Bus 的 `org.phone.Display`；系统服务都改用了 Unix 套接字上的 JSON 行（`wire` 包，[03](03-protocols-and-ui-path.md) §1），displayd 也一样：`<运行目录>/displayd.sock`，只有 Shell 连（0600）。方法和字段在 `svc/displayd.go`。

```
方法（请求的参数 → 都回答当前状态）:
  state                                     状态
  setting  {setting}                        auto | standard | high（设置 › 刷新率）
  hint     {source, kind, ms?, fps?}        kind: interaction | animating | end | content | clear
  cap      {source, hz}                     battery：最高 hz，0 取消
  panel    {kms?, applied?, err?}           渲染器驱动着面板（读模式）/ 切到了哪个刷新率 / 为什么没切成
  sim.thermal {hot}                         模拟的温控区（测试）
状态 / 事件 rate:
  {seq, panel, rates, hz, want, reason, setting, caps, switches}
```

- `hz` 是面板正在用的刷新率，`want` 是 displayd 选的：模拟屏上两者马上一样；真屏上等渲染器回答它切到了哪个模式。`reason` 是为什么：`idle`、`setting`、`cap:battery`、`cap:thermal`、`content:<帧率>`，或决定它的那个 hint 的来源（`input`、`renderer`、`app:<应用>`）。`seq` 每变一次加一：Shell 收到比手里旧的回答不用（回答可能晚于后来的事件）。
- `PHONE_DISPLAY_TRACE=1`：displayd 每次决定打一行（时间、选的刷新率、原因、面板的刷新率），和渲染器的 `PHONE_PAINT_TRACE` 对着看。

### 4.1 Shell 转来的 hint

| 来源 | 什么时候 | hint |
|------|----------|------|
| `input` | 每次按键（同一个 100ms 里的只转一次） | `interaction`：高刷，保持到 400ms 后 |
| `renderer` | 渲染器的 `motion`：动画开始 / 停下（滚动缓动、焦点滑动、转场、横幅和提示条滑动） | `animating` / `end`：开始到停下之间高刷，停下后保持 400ms；渲染器崩溃没说停下时 10 秒后自己结束 |
| `app:<应用>` | 前台应用的 `display.hint`（§4.2） | `animating` 或 `content` |
| `video:<播放器>` | `video.open` 的视频在前台应用的屏幕上播放时（暂停、离开屏幕就收回） | `content`：mediad 送画面的帧率（原型里固定 15 帧） |

Shell 连上 displayd 时先收回所有应用和视频的 hint，再交设置和电量；displayd 重启后照样。

### 4.2 应用：`display.hint`

应用碰不到显卡，只能说自己在显示什么（[22](22-developer-api.md) §4.12）：`{animating_ms}`（自己画的动画，最多 3000ms，要继续就再发）、`{fps}`（内容的帧率，比如自己解码的视频、游戏的固定帧率，直到收回）、`{clear}`。只有前台应用（在屏幕最前面、屏幕亮着、没锁；上面弹着系统菜单时照收，应用不知道菜单什么时候关，没法再发）才收；每个应用一次最多 8 个、之后每秒 4 个（令牌桶），多了回 `too many display hints`；应用离开前台（换应用、回待机、锁屏、息屏、退出）时 Shell 收回它的 hint，回到前台后由应用再发。回答是面板现在的刷新率和它有的刷新率。系统界面的滚动和转场不用应用管：渲染器自己说。

## 5. 自动策略（设置为「自动」）

`programs/displayd/policy.go`，单元测试按时间逐条核对（`policy_test.go`）：

| 条件 | 刷新率 |
|------|--------|
| 按键、列表滚动、UI 动效进行中（`input`、`renderer`），前台应用说在动画 | **屏的最高刷新率**（120） |
| 来电振铃、通话中 UI、从休眠唤醒后 **3s** 内 | **120**（**未实现**，§1） |
| 有内容 hint（视频、应用的帧率），没在动 | 能被所有内容帧率整除的最低刷新率：24 帧在 60/120 的屏上用 120，30 帧用 60，有 48Hz 模式时 24 帧用 48；都不能整除时用不低于最快帧率的最低一档 |
| 静态界面且无动效 **≥ 400ms** | 不低于 30Hz 的最低一档：60/120 的屏是 **60**，有 30Hz 模式的屏是 30 |
| 电量 **< 15%** 且没在充电（和动画效果自动减弱是同一个门槛） | 最高 **60**（`cap:battery`，固定「高」时也封顶）；省电模式系统里还没有 |
| 温控区达到上限（板子配置的温控区类型和温度，`--thermal-zones`、`--thermal-cap`，降回上限以下 3 度才解除） | 最高 **60**（`cap:thermal`）；还没有板子配置温控区，**未上真机** |
| 设置「标准 60 Hz」/「高 120 Hz」 | 锁定（仍受上面两条封顶） |
| 灭屏 / 深度休眠 | 面板 off，不讨论 Hz |

**升频** 立即；**降频** 延迟 **400ms**，避免边界抖动。几个封顶同时在时取最低的。

相机预览：预览纹理 **30fps** 更新；浏览 UI chrome 随 §5 表升降（滚动相册升 120）。

## 6. 帧预算

| 模式 | 帧周期 | 绘制预算（示意） |
|------|--------|---------------------------|
| **120Hz** | 8.33ms | ≤ 5ms 绘制 + 2ms commit |
| **60Hz** | 16.67ms | ≤ 10ms 绘制 + 3ms commit |

软件渲染下的实测和估算见 [37](37-motion.md) §2：开发机上动画的一步多在 1ms 以内，手机上估计整屏都变的步 2.5–8.5ms（**估算，未上真机**），所以 120Hz 下整页转场可能赶不上每个周期，赶不上的步晚一个周期，按键不等。telephonyd **不得**占用 UI 核导致 120Hz 掉帧。

## 7. 验收

**120Hz 生效时**（策略强制或交互中），§7 场景 **1% low ≥ 110fps**：

1. 通讯录惯性滚动 5s  
2. 设置页连按方向键 3s  
3. Idle ↔ Dialer 转场 ×20  
4. 来电 Incoming 弹出  

**60Hz 生效时**（静态 Idle 5s 后测）：Idle 界面 **平均功耗** 相对同亮度固定 120Hz **下降**（具体百分比记入 `LOG.md` 实测）；帧率 **≥ 55fps** 于单次按键 redraw。

**切换：** 60→120 在首键按下后 **≤ 2 帧** 内进入 120Hz mode（用 displayd 的 `rate` 事件、渲染器切完模式的回答 `rate` 的时间与输入事件对比）。

工具：`PHONE_DISPLAY_TRACE=1`（displayd 的每次决定）和 `PHONE_PAINT_TRACE`（渲染器每段动画的步数、耗时和晚到的帧，[37](37-motion.md) §5）✓；`kario/tools/fps-bench/` **未实现**。以上验收都要 120Hz 的真屏，**未上真机**。

现在验证了的（开发机，模拟屏）：

| 测的东西 | 怎么测 | 结果 |
|----------|--------|------|
| 策略 | `go test ./programs/displayd`：按键升到 120、最后一次按键 400ms 后回 60；渲染器的动画到停下为止再保持 400ms；24、30、60、15、25 帧的内容在各种屏上选哪一档；标准、高、电量和温控的封顶；应用离开前台收回 | ✓ |
| DRM 模式 | 结构体和 ioctl 号对内核的布局；刷新率按内核的 `drm_mode_vrefresh` 取整；只取当前尺寸的逐行模式 | ✓（读真的显卡**未上真机**） |
| Shell | `go test ./shell -run TestDisplay`：按键、渲染器的动画、应用的 hint（前台、限频、离开收回）、电量和设置、选的刷新率交给渲染器、旧回答不用 | ✓ |
| 渲染器 | `cargo test`：周期、`motion` 只说一次、帧不比面板快、按刷新率走的动效步数（60Hz 下 180ms 的推入 11 步，120Hz 下 22 步）、按刷新率挑模式 | ✓ |
| 自测 | 「刷新率」「刷新率设置」两个用例（[13](13-prototype.md) §5） | 见 [13](13-prototype.md) |

自测脚本的命令（`shell/display_headless.go`）：`require-display`（没有 phone-displayd 时跳过）、`display <hz> [ms]`（等到面板是这个刷新率）、`display went <hz> [ms]`（上一个 display 步骤以来面板到过这个刷新率：按键的 400 毫秒很短，模拟器上慢的时候也抓得到）、`display stayed <hz> [ms]`（这段时间里面板一直是这个刷新率）、`display reason <原因> [ms]`、`display thermal hot|cool`、`display report`（打出 `PHONE-DISPLAY`：面板、刷新率、原因、切换次数）。

## 8. 与主流平台对齐

| 基础设施 | 主流旗舰 | 本项目 |
|----------|----------|--------|
| 自适应高刷 | ProMotion / LTPO / 安卓自适应 | **60 ⟷ 120 自动** + `phone-displayd` ✓（模拟屏；**未上真机**） |
| GPU UI | 原生 | **phone-paint**（CPU 上栅格化；GPU 合成可选（GLES 2，[37](37-motion.md) §5，[42](42-gpu-rendering.md)），QEMU 上用软件 GPU 验证过；GPU 栅格化也可选（[37](37-motion.md) §5 第 10 条），开发机的 llvmpipe 上验证过；都**未上真机**，Adreno、Mali、PowerVR 都没试过） |
| 策略集中 | 系统 compositor / SF | **phone-displayd** 决定、渲染器切换 |
| 应用的帧率意图 | 安卓 `Surface.setFrameRate`、iOS `CADisplayLink.preferredFrameRateRange` | `display.hint` 的 `fps` / `animating_ms`，只在前台、限频 |
| 电话独立服务 | 是 | **telephonyd** |

对齐 **刷新率策略 + GPU UI + 服务化**，不对齐应用商店规模。

## 9. 实施（见 [05](05-implementation.md) 阶段 E）

1. 内核确认 **60/120** dual mode 或 VRR（要选定的屏和板子，**未上真机**）。  
2. **phone-displayd** 决定刷新率 + 发 `rate` 事件 ✓；读 DRM 卡的模式 ✓（只在单元测试里对过结构，读真的显卡**未上真机**）。  
3. **渲染器** 订阅刷新率（经 Shell 的 `rate`）、按它切换模式（`set_crtc`）、按周期节流 + 发 `motion` ✓；真屏上的切换（会不会闪、几帧生效）**未上真机**。QEMU 的 virtio-gpu 只报一个模式（QEMU 给的尺寸，刷新率由 QEMU 的 EDID 定，60 或 75Hz，**未验证**），displayd 读到一种刷新率就不切换，设置里也没有这一行；所以 QEMU 能看到的是：开机自测里模拟屏上的策略（和开发机一样，自测用私有的 displayd）、设备模式下 displayd 读 virtio-gpu 的模式这条路。  
4. telephonyd 来电钩子 → displayd 升 120（**未实现**）。  
5. §7 验收写入 CI 或发布 checklist（要真屏）。

## 10. 决策记录

| 项 | 决策 |
|----|------|
| 刷新率 | **60Hz 基线**；选 120Hz 屏的机型 60↔120 自动切换（D3） |
| 策略守护进程 | **phone-displayd**：原计划用 Rust，实际和别的系统服务一样用 Go（kario-system 里的一个程序），渲染器之外不需要 Rust |
| 谁切换模式 | 渲染器（它是 DRM master）；displayd 只决定，经 Shell 转达 |
| 固定 120 仅 | 设置「高」或交互中 |
| 仅 60Hz 屏 | 可以量产（D3）；displayd 照跑，不切换，设置里没有这一行 |
