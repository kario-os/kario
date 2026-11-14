# Kario 文档

Kario 是按键优先的手机系统：应用描述语义界面（`phone.Ui`），系统负责渲染、输入法、权限和安全界面。这里是给开发者和设备厂商的文档，代码在上一级目录。从 [05](05-implementation.md)（实施指南）和 [13](13-prototype.md)（原型怎么跑）开始，开发应用看 [22](22-developer-api.md)。

许可：CC BY 4.0。标着「内部文档」的引用指向不公开的商业和规划文档。英文版在 [en/](en/README.md)，以中文为准；两边的结构（标题、表格行、代码块）由 `scripts/docs-en-check.py` 核对。

编号按仓库分区段：这里是 01–50，内部文档是 51–99（90 起是草稿和附录），所以一个编号只指一份文档；新文档接着本区段的下一个号。

| 文档 | 内容 |
|------|------|
| [01-software-stack.md](01-software-stack.md) | 软件栈 |
| [02-language-and-tui-stack.md](02-language-and-tui-stack.md) | 语言与框架 |
| [03-protocols-and-ui-path.md](03-protocols-and-ui-path.md) | 协议与 UI 路径 |
| [04-ui-language-telephonyd.md](04-ui-language-telephonyd.md) | telephonyd · UI |
| [05-implementation.md](05-implementation.md) | 实施指南（从这里开始） |
| [06-gui-architecture.md](06-gui-architecture.md) | GUI 架构（按键优先的像素界面） |
| [07-ecosystem-and-store.md](07-ecosystem-and-store.md) | 生态与应用商城 |
| [08-display-adaptive-refresh.md](08-display-adaptive-refresh.md) | 显示基础设施：60–120Hz 自适应刷新 |
| [09-ui-feasibility-review.md](09-ui-feasibility-review.md) | 综合评审：TUI 能否撑起复杂 App · 两版方案合并 |
| [10-keyboard-interaction-model.md](10-keyboard-interaction-model.md) | 按键交互语法与语义 UI 模型 |
| [11-platform-architecture.md](11-platform-architecture.md) | 平台架构：超级应用级的按键优先系统 |
| [12-super-app-capabilities.md](12-super-app-capabilities.md) | 超级应用能力目录：微信 / 支付宝用到的能力 → 平台能力 |
| [13-prototype.md](13-prototype.md) | 代码原型：phone.Ui + TUI Shell + 超级应用样例 |
| [14-tui-route-and-app-framework.md](14-tui-route-and-app-framework.md) | 复评：TUI 技术路线与应用框架（对照 DeepSeek Harness / dsh-TUI） |
| [15-web-vs-semantic-benchmark.md](15-web-vs-semantic-benchmark.md) | 实测对比：当前实现（语义 UI）vs Web 路线 |
| [16-input-method.md](16-input-method.md) | 输入法：九宫格与全键盘、中英文、候选与联想 |
| [17-app-sandbox.md](17-app-sandbox.md) | 应用行为限制：沙箱、权限、后台 |
| [18-nokia-form-factors.md](18-nokia-form-factors.md) | 诺基亚形态与按键：九宫格、全键盘、输入切换对齐 |
| [19-core-apps.md](19-core-apps.md) | 必备应用与平台能力核对 |
| [20-audio.md](20-audio.md) | 音频服务（R23） |
| [21-push.md](21-push.md) | 推送服务（R6） |
| [22-developer-api.md](22-developer-api.md) | 开发者文档：能力与接口 |
| [23-renderer-protocol.md](23-renderer-protocol.md) | 渲染器协议（phone.frame/1）：可替换的像素界面 |
| [24-sku-framework.md](24-sku-framework.md) | SKU 框架：一份硬件配置生成一个固件包 |
| [25-system-update.md](25-system-update.md) | 系统更新（OTA）：A/B 系统槽与 dm-verity |
| [26-design-system.md](26-design-system.md) | phone-paint 的设计系统：四套主题的配色、字号、间距、圆角、对比度规则 |
| [27-usb-typec.md](27-usb-typec.md) | USB-C：PD 快充、键盘鼠标（插拔、鼠标当按键）、像 iPhone 一样先信任再给电脑共享文件（网络共享或 MTP 文件传输）；对照 iPhone 怎么连电脑调试 |
| [28-battery.md](28-battery.md) | 续航：息屏和按键唤醒、通知灯慢闪（不点亮屏幕）、Shell 少醒 |
| [29-app-development.md](29-app-development.md) | 应用开发体验的路线：系统中间件是唯一的入口，主力语言定为 TypeScript（QuickJS-ng 引擎）、声明式框架 KarioUI、命令行和 VS Code 扩展、模拟器、规范和上架；P1–P3 在开发机上完成，P4 做了一部分 |
| [30-nfc.md](30-nfc.md) | NFC 卡模拟：门禁卡（复制 UID 卡）、交通卡（快捷交通卡息屏可刷）、付款卡（默认付款应用、要解锁）；系统应用「卡包」管卡；息屏锁着时门禁卡、快捷交通卡自己就能刷，按刷卡的时间先出示最可能的那张，读卡器不认就换下一张；双击电源键手动选卡；应用经 `nfc.apdu` / `nfc.reply` 答读卡器（模拟控制器，真机未实现） |
| [31-lock-and-biometrics.md](31-lock-and-biometrics.md) | 锁屏与生物识别：锁屏 PIN 码（息屏即锁、锁着能打紧急电话、通知只显示条数）、指纹解锁、指纹支付（绑定指纹的密钥，录新指纹即作废）；模拟传感器，TEE 未实现 |
| [32-messaging.md](32-messaging.md) | 短信：系统服务 phone-smsd 存短信（会话、已读未读、删除、搜索、拦截文件夹、送达回执、验证码），telephonyd 只做无线一侧，收到的短信等 smsd 存好确认后才放手；紧急警报（小区广播）在锁屏上也弹出，国家级不能关；只有默认短信应用能删和拦截（模拟基带；ModemManager 一侧未运行测试、未上真机） |
| [33-app-guidelines.md](33-app-guidelines.md) | 应用规范：界面怎么设计、商店怎么审（自动检查和人工审核的标准）、隐私标签（未实现，设计） |
| [34-app-tutorial.md](34-app-tutorial.md) | 教程：用 KarioUI 一步一步写一个应用，`kario run` 调试、`kario test` 测试、打包上架、Kario Studio 扩展 |
