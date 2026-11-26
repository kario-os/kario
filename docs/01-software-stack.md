# 软件栈

56-product-strategy.md（内部文档） · [05-implementation.md](05-implementation.md) · [06-gui-architecture.md](06-gui-architecture.md) · [09-ui-feasibility-review.md](09-ui-feasibility-review.md)

## 分层

```
┌─────────────────────────────────────────────────┐
│  应用（独立进程）── phone.Ui 语义树 + 动作列表   │
├─────────────────────────────────────────────────┤
│  Shell：焦点 · 按键 · 菜单 · 软键 · 返回栈 · 任务 │
│    phone-paint（Rust，像素）· 文本渲染器（Go）  │
├──────────────────────┬──────────────────────────┤
│  phone-displayd      │  媒体服务（按需启动）     │
│  未实现，120Hz 才做  │  图片 / 视频 / PDF / 相机 │
├──────────────────────┴──────────────────────────┤
│  telephonyd (C++23)  →  JSON 行（Unix 套接字）  │
├─────────────────────────────────────────────────┤
│  telephonyd 后端（按芯片）· PipeWire/Pulse      │
├─────────────────────────────────────────────────┤
│  内核: 基带 panel evdev codec GPU V4L2 …         │
└─────────────────────────────────────────────────┘
```

## Shell 与应用

应用只描述内容和每个元素的动作；按键映射、选项菜单、软键、返回由 Shell 统一负责，所以任何应用都能用按键完整操作。语法与节点表见 [10-keyboard-interaction-model.md](10-keyboard-interaction-model.md)。

## TUI

Shell（Go + Bubble Tea）是通用的 `phone.Ui` 文本渲染器：列表、软键、浮层（来电全屏），图片降级为 `[图]`，用于 SSH / 串口 / 调试；同一套逻辑经帧协议驱动像素渲染器（[06](06-gui-architecture.md)）。

不在 DRM 上直接跑 TUI：Linux 控制台显示不了中文，Notcurses 也没有 DRM 后端（[09](09-ui-feasibility-review.md) §3.2）。

## GUI

phone-paint（自研，Rust，Apache-2.0）画 `phone.Ui` 的帧（渲染器协议 `phone.frame/1`，[23](23-renderer-protocol.md)），现在是软件渲染，GPU 渲染**未实现**；对照用的 phone-gui（Slint，GPL-3.0-only）不进设备镜像。**phone-displayd** 集中管刷新率（[08-display-adaptive-refresh.md](08-display-adaptive-refresh.md)；60Hz 基线，120Hz 可选）**未实现**，只有选了 120Hz 屏才做。Shell 和 Kario 自己的系统服务之间是 Unix 套接字上的 JSON 行（`wire` 包），没有 D-Bus 的 `phone.*` 接口；D-Bus 只用来连 iwd、BlueZ、ModemManager 这些外部服务。

## 媒体服务

独立进程、按需启动、用完销毁（采纳自新稿）。为 `Image` / `MediaSurface` 节点出画面；视频与相机可走 DRM 硬件图层直通以省电，图层不够时回落到 GPU 合成。
