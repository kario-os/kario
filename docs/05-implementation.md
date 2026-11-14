# 实施指南（从这里开始）

## 已定技术栈

| 组件 | 技术 |
|------|------|
| telephonyd | **C++23**，CMake，sdbus-c++ 或 GIO D-Bus |
| phone-tui | **Go** + Bubble Tea + godbus |
| 蜂窝 | **telephonyd**，后端按芯片：ModemManager / Android 无线接口 HAL / 厂商接口（61（内部文档） §3.2） |
| 开发验证 | 芯片厂或方案公司的 **4G 开发板 / 公板手机** + SIM（先不做定制 PCB，61（内部文档） §7） |
| 协议 | `phone.*` D-Bus v0（[03-protocols-and-ui-path.md](03-protocols-and-ui-path.md)） |
| phone-gui（TUI 之后） | **Rust + Slint**，[06-gui-architecture.md](06-gui-architecture.md) |

telephonyd 使用 **C++23** 与 `example/hello` 一致；交叉编译时复用同一套 gcc-toolset / preset 习惯。

---

## 仓库目录（创建顺序）

在仓库 `kario/` 下：

```
kario/
├── ipc/
│   └── phone-ipc-v0.md          # 从 09 复制并细化字段类型
├── telephonyd/                  # C++23
│   ├── CMakeLists.txt
│   ├── src/main.cpp
│   ├── src/mm_client.cpp        # ModemManager D-Bus
│   └── src/telephony_service.cpp # 导出 phone.Telephony
├── phone-tui/                   # Go module
│   ├── go.mod
│   ├── main.go
│   └── internal/ui/             # Bubble Tea screens
└── systemd/
    ├── telephonyd.service
    └── phone-tui.service
```

---

## 阶段 0 — 桌面交互原型（不买硬件，可与阶段 A 并行）

1. 桌面上开目标分辨率窗口（320×240 横屏、480×640 竖屏），只接受键盘。
2. 按 [10-keyboard-interaction-model.md](10-keyboard-interaction-model.md) 实现最小 Shell：焦点、OK、菜单键、选项软键、返回栈、打字即搜索。
3. 假数据样本应用：「B站式」推荐流 → 视频页 → 评论；「IM 式」会话 → 聊天 → 消息菜单 → 看图；电话三屏。
4. 像素版（Slint）与文本版（Bubble Tea）各一套。

**验收：** 连用一周，样本应用全部动作都能用按键完成、没有死角（[09](09-ui-feasibility-review.md) §7）。通过后冻结 `phone.Ui` v0。

**现状**：文本版和像素版都已完成，并扩展为平台原型（[13](13-prototype.md)：`make run / make selftest`；像素渲染器见 [06](06-gui-architecture.md)；Yocto 镜像在 QEMU 开机自测通过，见 13 §5.1）。一周自用记录还没做（57（内部文档） R9）。

---

## 阶段 A — 开发板能打电话（无自研代码）

**环境：** 开发板厂商的系统（展锐 Yocto BSP、厂商 Android，或高通主线上的 postmarketOS），按芯片厂文档完成一次 VoLTE 主叫 / 接听，确认基带、IMS 和通话音频在这块板上本身是通的。

**验收：** 注册网络 + 一次 VoLTE 主叫 / 接听成功。记录板子、BSP 版本和基带控制接口到 `phone/LOG.md`。

---

## 阶段 B — telephonyd v0（C++23）

1. **CMake 工程**  
   - `cmake_minimum_required` 3.25+，`CXX_STANDARD 23`。  
   - 依赖：`sdbus-c++` 或 `pkg-config` 找 `gio-2.0`。  
   - 链接并监听 **system D-Bus**。

2. **实现 `phone.Telephony` v0**（可先硬编码 object path `/org/phone/Telephony`）  
   - 启动时连接 ModemManager，缓存 modem index。  
   - **properties**（只读）：`Registration`、`SignalQuality`（从 MM 映射）。  
   - **methods**：`Dial(s number)`、`Hangup()`、`Answer()`、`Reject()`。  
   - **signals**：`CallStateChanged`、`IncomingCall(number)`。

3. **本地测试**（开发机或板子）  
   ```bash
   cmake -B build -DCMAKE_BUILD_TYPE=Debug
   cmake --build build
   sudo ./build/telephonyd
   busctl introspect org.phone /org/phone/Telephony
   ```

**验收：** `busctl call` 能触发拨号/挂断；来电时 signal 能在 `busctl monitor` 看到。

---

## 阶段 C — phone-tui v0（Go）

1. `go mod init`（路径按 monorepo 实际填写）。  
2. **godbus** 连接 `phone.Telephony`；订阅 `IncomingCall`。  
3. **Bubble Tea** 三屏：  
   - **Idle**：状态栏（注册、信号）、输入号码、软键「拨打」  
   - **InCall**：通话时长、挂断  
   - **Incoming**：全屏 overlay、接听/拒绝  

4. 与 telephonyd 同机运行；systemd 先起 telephonyd，再起 phone-tui。

**验收：** 键盘/SSH 下操作 TUI 完成拨号与接听；来电 overlay 弹出。

现状：Shell 已是通用的 `phone.Ui` 渲染器，电话三屏是普通的语义应用（`apps/dialer`，来电界面是系统界面）。

---

## 阶段 D — 音频与电源（仍可在开发板）

1. 通话时把基带的语音路由到听筒 / 扬声器（ALSA UCM 或厂商音频 HAL，按芯片厂文档）。  
2. 读 `power_supply` 或 upower，在 TUI 状态栏显示电量（可先不经 D-Bus，后在 telephonyd 加 `phone.Power`）。

**验收：** 通话音质可接受；电量显示正确。

---

## 阶段 E — phone-displayd + phone-gui（Slint）

**硬件：** 与量产同类的 **DSI 屏，60Hz 基线**（D3）。下面的 60↔120 步骤只在选到 60/120 双 mode（或 VRR）屏时执行，规范见 [08-display-adaptive-refresh.md](08-display-adaptive-refresh.md)。

1. `modetest` 确认 **60Hz 与 120Hz** mode（或 VRR）。  
2. `kario/phone-displayd/`：DRM 切换 + **org.phone.Display**；来电升 **120Hz**。  
3. 空 Slint 全屏：验 **60↔120** 与 `RefreshRateChanged`。  
4. `phone-client/` + `phone-gui/`：Idle / Incoming / InCall；interaction hint。  
5. **fps-bench** + `design-system/`（08 §7）；相机纹理（08 §5）。

**验收：** [08-display-adaptive-refresh.md](08-display-adaptive-refresh.md) §7；来电 < 100ms。

Slint 许可：[06-gui-architecture.md](06-gui-architecture.md)。

---

## 阶段 F — 定制硬件

按 53-phased-roadmap.md（内部文档）：矩阵键盘、LCD、Codec；**不改 IPC**。GPU 驱动与 Slint 后端在 BSP 中固定。

---

## 每周检查清单

| 周 | 目标 |
|----|------|
| 0（约 4 周，可并行） | 阶段 0 桌面交互原型 |
| 1 | 阶段 A；`kario/` + `phone-ipc-v0.md` |
| 2 | telephonyd Dial/Hangup |
| 3 | phone-tui Idle + InCall |
| 4 | 来电 overlay + systemd |
| 5–6 | 音频、电量 |
| 7+ | phone-displayd + phone-gui；外设矩阵与 [07](07-ecosystem-and-store.md) |

---

## 与 `example/hello` 的关系

- telephonyd 的 CMake 可单独构建，也可日后把 `kario/telephonyd` 挂到 hello 式 preset（非必须）。  
- 电话 **状态机** 可参考 `example/fsm` 的枚举与转移表风格，放在 `telephonyd` 内。

---

## 文档索引

| 文件 | 内容 |
|------|------|
| [09-ui-feasibility-review.md](09-ui-feasibility-review.md) | 综合评审、决策 D1–D3 |
| [10-keyboard-interaction-model.md](10-keyboard-interaction-model.md) | 按键交互语法、`phone.Ui` |
| [03-protocols-and-ui-path.md](03-protocols-and-ui-path.md) | D-Bus 接口 |
| [04-ui-language-telephonyd.md](04-ui-language-telephonyd.md) | telephonyd 角色 |
| 56-product-strategy.md（内部文档） | 开源核心 / 闭源上层 |
