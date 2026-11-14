# 显示基础设施：60–120Hz 自适应刷新

> **D3（已定）：60Hz 基线；选到合适的 120Hz 屏时，再按本文启用 60↔120 自适应策略。** 理由：据目前了解，消费产品里的高刷屏基本从 5.9 寸左右起步，3.5 寸以下几乎找不到现成的 120Hz DSI 屏（需采购核实），定制屏起订量很高；按键交互是离散步进，高刷收益远小于手指拖动。`phone-displayd` 独占刷新率、应用只能发 hint 的设计照常适用。所以 §1 / §2 里的「120Hz 峰值」只适用于选了 120Hz 屏的机型，60Hz 屏可以量产。详见 [09-ui-feasibility-review.md](09-ui-feasibility-review.md) §6。

与主流平台对齐的 **显示基础设施**：面板 **最高 120Hz**，系统 **在 60Hz 与 120Hz 间自动切换**——静态省电、交互与动效满刷、体验与旗舰同语义（ProMotion / 自适应高刷）。

关联：52-hardware-architecture.md（内部文档）、[06-gui-architecture.md](06-gui-architecture.md)、[05-implementation.md](05-implementation.md)。

## 1. 产品定义

| 项 | 要求 |
|----|------|
| 面板能力 | **120Hz 峰值**；至少支持 **60Hz 与 120Hz 两种 DRM mode**（或 **VRR 48–120Hz**，见 §2.1） |
| 系统策略 | **自动**：交互/动效 → **120Hz**；静态阅读/待机界面 → **60Hz** |
| 120Hz 下 UI | 基准场景 **1% low ≥ 110fps**（§7） |
| 60Hz 下 UI | 基准场景 **1% low ≥ 55fps**；**禁止** 在 60Hz 模式仍按 120fps 空转 GPU |
| 触控到首帧 | 按键/触摸触发时 **立即请求 120Hz**（见 §4） |
| 来电全屏 | **120Hz** + Incoming 首帧 **< 100ms** |

TUI 阶段可用 60Hz 开发板；**选 120Hz 屏的机型，phone-gui 与 BOM 必须支持 60/120 双 mode（或 VRR）**，§2 是这类机型的约束；只有 60Hz 屏的机型可以量产（D3）。

## 2. 硬件（BOM 硬约束）

### 2.1 面板

| 组件 | 要求 |
|------|------|
| 接口 | **MIPI DSI**；SPI/I8080 彩屏 **不进入量产 GUI** |
| 刷新 | 规格书 **120Hz**；并明确 **60Hz 时序** 或 **VRR 范围** |
| LTPO | v1 **不强制**；有则 v2 在 `phone-displayd` 扩展细粒度档位 |
| 背光 | PWM 不与当前刷新率产生可见拍频（60/120 切换时测） |

### 2.2 SoC / GPU

| 组件 | 要求 |
|------|------|
| DRM/KMS | **atomic modesetting**；`modetest` 可见 **60Hz 与 120Hz** mode（或单一 VRR mode） |
| GPU | **GLES 3.x / Vulkan**；两种刷新下均可稳定合成 |
| 带宽 | **120Hz 峰值** 满足 §6 帧预算 |

### 2.3 禁止

- 选 120Hz 屏时用只有 **120Hz** 一个 mode 的面板（静态时降不到 60Hz，省不了电）。  
- 无 **phone-displayd**、由应用随意 `ioctl` 改刷新率（策略必须集中）。

## 3. 软件栈

```
evdev ──► phone-gui (Slint) ──hint──► phone-displayd (Rust)
              │                              │
              │ GPU                          │ DRM atomic
              ▼                              ▼
         Slint 按当前 Hz 调度渲染        60Hz ⟷ 120Hz
                                              │
                                         面板
telephonyd ──来电──► phone-displayd (强制 120Hz)
phone.Power ──低电──► phone-displayd (可封顶 60Hz)
```

| 进程 | 职责 |
|------|------|
| **phone-displayd** | **唯一** 有权切换 DRM 刷新率；执行 §5 策略表 |
| **phone-gui** | Slint 绘制；上报 **交互/滚动/动效** hint；按当前 Hz **节流** 重绘 |
| **telephonyd** | 来电/通话状态通知 displayd（D-Bus 或内部信号） |
| **内核** | panel driver + 双 mode 或 VRR |

**Slint：** 120Hz 时 vsync 跟满；60Hz 时 **仅 damage 区域重绘**，无交互时不提交空帧。

## 4. D-Bus：`org.phone.Display` v1

由 **phone-displayd** 持有。

```
properties:
  CurrentRefreshRate   u32   # 60 或 120
  MaxRefreshRate       u32   # 120
  Policy               s     # auto | fixed120 | fixed60

methods:
  HintInteractionBegin()
  HintInteractionEnd()
  HintAnimationUntil(u64 deadline_ms_monotonic)

signals:
  RefreshRateChanged(u32 hz)
```

**phone-gui** 在按键/触摸/滚动开始时 `HintInteractionBegin`；空闲 **≥ 400ms** 无输入且无动效后 `HintInteractionEnd`。

**phone-runtime**（第三方）：只允许 `HintInteractionBegin/End`；**禁止** 直接改 `Policy`（设置页内置应用除外）。

## 5. 自动策略（`Policy=auto`）

| 条件 | 刷新率 |
|------|--------|
| 触摸/键按下、列表滚动、UI 动效进行中 | **120** |
| 来电振铃、通话中 UI、从休眠唤醒后 **3s** 内 | **120** |
| 静态界面且无动效 **≥ 400ms** | **60** |
| 用户设置「省电」或电量 **< 15%**（可配置） | **60**（可禁止升 120 直至充电） |
| 设置项 `Policy=fixed120` / `fixed60` | 锁定 |
| 灭屏 / 深度休眠 | 面板 off，不讨论 Hz |

**升频** 立即；**降频** 延迟 **400ms**，避免边界抖动。

相机预览：预览纹理 **30fps** 更新；浏览 UI chrome 随 §5 表升降（滚动相册升 120）。

## 6. 帧预算

| 模式 | 帧周期 | Slint+GPU 绘制预算（示意） |
|------|--------|---------------------------|
| **120Hz** | 8.33ms | ≤ 5ms 绘制 + 2ms commit |
| **60Hz** | 16.67ms | ≤ 10ms 绘制 + 3ms commit |

telephonyd **不得**占用 UI 核导致 120Hz 掉帧。

## 7. 验收

**120Hz 生效时**（策略强制或交互中），§7 场景 **1% low ≥ 110fps**：

1. 通讯录惯性滚动 5s  
2. 设置页连按方向键 3s  
3. Idle ↔ Dialer 转场 ×20  
4. 来电 Incoming 弹出  

**60Hz 生效时**（静态 Idle 5s 后测）：Idle 界面 **平均功耗** 相对同亮度固定 120Hz **下降**（具体百分比记入 `LOG.md` 实测）；帧率 **≥ 55fps** 于单次按键 redraw。

**切换：** 60→120 在首键按下后 **≤ 2 帧** 内进入 120Hz mode（用 `RefreshRateChanged` 时间戳与输入事件对比）。

工具：`kario/tools/fps-bench/`、`PHONE_DISPLAY_TRACE=1`（实现阶段）。

## 8. 与主流平台对齐

| 基础设施 | 主流旗舰 | 本项目 |
|----------|----------|--------|
| 自适应高刷 | ProMotion / LTPO / 安卓自适应 | **60 ⟷ 120 自动** + `phone-displayd` |
| GPU UI | 原生 | **Slint + GPU** |
| 策略集中 | 系统 compositor / SF | **phone-displayd** |
| 电话独立服务 | 是 | **telephonyd** |

对齐 **刷新率策略 + GPU UI + 服务化**，不对齐应用商店规模。

## 9. 实施（见 [05](05-implementation.md) 阶段 E）

1. 内核确认 **60/120** dual mode 或 VRR。  
2. **phone-displayd** 切换 mode + 发 `RefreshRateChanged`。  
3. **phone-gui** 订阅 Hz，调整 Slint 重绘策略 + 发 hint。  
4. telephonyd 来电钩子 → displayd 升 120。  
5. §7 验收写入 CI 或发布 checklist。

## 10. 决策记录

| 项 | 决策 |
|----|------|
| 刷新率 | **60Hz 基线**；选 120Hz 屏的机型 60↔120 自动切换（D3） |
| 策略守护进程 | **phone-displayd**（Rust） |
| 固定 120 仅 | 开发者 `fixed120` 或交互中 |
| 仅 60Hz 屏 | 可以量产（D3） |
