# 语言与框架

| 层 | 技术 | 状态 |
|----|------|------|
| telephonyd | **C++23** | 已定 |
| phone-shell（原 phone-tui） | **Go** + Bubble Tea：文本渲染器 + broker + 系统 UI | 已定；原型 ✓（[13](13-prototype.md)） |
| phone-mediad / phone-keystored | 原型 Go；量产 mediad 用 Rust 或 C（libcamera / V4L2），keystored 改为 OP-TEE 可信应用（C） | 原型 ✓；量产建议见 [11](11-platform-architecture.md) §12 |
| phone-displayd | **Rust**（DRM 刷新率策略） | 已定；60Hz 基线，120Hz 可选 |
| phone-gui（闭源） | **Rust** + **Slint**（GPU） | 已定 |
| 语义 UI 模型 `phone.Ui` | 数据结构 + JSON 行协议（fd 3） | v0 原型 ✓（[03](03-protocols-and-ui-path.md) §5） |
| 应用 SDK | Go（原型 ✓）；任意语言直连（Python 示例 ✓）；C ABI（给厂商复用 C++ 核心）与 Rust 计划中 | 部分 |
| 第三方应用 | 语义应用（默认）；画布应用（例外）用 `canvas` 节点给图形，由系统画 | 语义应用原型 ✓；画布应用原型 ✓ |
| 应用脚本（QuickJS / Lua 等） | 未定 | 推迟到 v2 之后 |
| 蜂窝 | **ModemManager** | 已定 |
| 产品镜像 | **Yocto** Wrynose 6.0 LTS + kas，层 `yocto/meta-phone` | 原型 ✓（qemuarm64 开机自测通过，[13](13-prototype.md) §5.1） |

不采用：**Notcurses** 作为 DRM 上的界面层——它没有 DRM 后端，Linux 控制台也显示不了中文（[09](09-ui-feasibility-review.md) §3.2）。

IPC：[03-protocols-and-ui-path.md](03-protocols-and-ui-path.md)。GUI：[06-gui-architecture.md](06-gui-architecture.md)。实施：[05-implementation.md](05-implementation.md)。
