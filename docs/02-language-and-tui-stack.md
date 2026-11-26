# 语言与框架

| 层 | 技术 | 状态 |
|----|------|------|
| telephonyd | **C++23** | 已定 |
| phone-shell（原 phone-tui） | **Go** + Bubble Tea：文本渲染器 + broker + 系统 UI | 已定；原型 ✓（[13](13-prototype.md)） |
| phone-mediad / phone-keystored | 原型 Go；量产 mediad 用 Rust 或 C（libcamera / V4L2），keystored 改为 OP-TEE 可信应用（C） | 原型 ✓；量产建议见 [11](11-platform-architecture.md) §12 |
| phone-displayd | **Rust**（DRM 刷新率策略） | **未实现**；只有选了 120Hz 屏才做，60Hz 是基线 |
| 像素渲染器 phone-paint | **Rust**（tiny-skia，软件渲染），Apache-2.0；对照用的 phone-gui 是 Rust + Slint，GPL-3.0-only，不进设备镜像 | 原型 ✓（[06](06-gui-architecture.md)、[23](23-renderer-protocol.md)） |
| 语义 UI 模型 `phone.Ui` | 数据结构 + JSON 行协议（fd 3） | v0 原型 ✓（[03](03-protocols-and-ui-path.md) §5） |
| 应用 SDK | TypeScript（主力语言，kario-jsrt 上的 KarioUI，[29](29-app-development.md)）✓；C ABI（`sdk/c`，给厂商复用 C++ 核心）✓；Go ✓；任意语言直连（Python 示例 ✓）；Rust 用于原生模块，整个应用用 Rust 写的 SDK **未实现** | 原型 ✓ |
| 第三方应用 | 语义应用（默认）；画布应用（例外）用 `canvas` 节点给图形，由系统画 | 语义应用原型 ✓；画布应用原型 ✓ |
| 应用脚本 | QuickJS-ng：TypeScript 应用和小程序的引擎（[29](29-app-development.md)） | 已定；原型 ✓ |
| 蜂窝 | **ModemManager** | 已定 |
| 产品镜像 | **Yocto** Wrynose 6.0 LTS + kas，层 `yocto/meta-phone` | 原型 ✓（qemuarm64 开机自测通过，[13](13-prototype.md) §5.1） |

不采用：**Notcurses** 作为 DRM 上的界面层——它没有 DRM 后端，Linux 控制台也显示不了中文（[09](09-ui-feasibility-review.md) §3.2）。

IPC：[03-protocols-and-ui-path.md](03-protocols-and-ui-path.md)。GUI：[06-gui-architecture.md](06-gui-architecture.md)。实施：[05-implementation.md](05-implementation.md)。
