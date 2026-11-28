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
| [29-app-development.md](29-app-development.md) | 应用开发体验的路线：系统中间件是唯一的入口，主力语言定为 TypeScript（QuickJS-ng 引擎）、声明式框架 KarioUI、命令行和 VS Code 扩展、模拟器、规范和上架；P1–P5 在开发机上完成，QEMU 上自测通过，**未上真机** |
| [30-nfc.md](30-nfc.md) | NFC 卡模拟：门禁卡（复制 UID 卡）、交通卡（快捷交通卡息屏可刷）、付款卡（默认付款应用、要解锁）；系统应用「卡包」管卡；息屏锁着时门禁卡、快捷交通卡自己就能刷，按刷卡的时间先出示最可能的那张，读卡器不认就换下一张；双击电源键手动选卡；应用经 `nfc.apdu` / `nfc.reply` 答读卡器（模拟控制器，真机未实现） |
| [31-lock-and-biometrics.md](31-lock-and-biometrics.md) | 锁屏与生物识别：锁屏 PIN 码（息屏即锁、锁着能打紧急电话、通知只显示条数）、指纹解锁、指纹支付（绑定指纹的密钥，录新指纹即作废）；模拟传感器，TEE 未实现 |
| [32-messaging.md](32-messaging.md) | 短信：系统服务 phone-smsd 存短信（会话、已读未读、删除、搜索、拦截文件夹、送达回执、验证码），telephonyd 只做无线一侧，收到的短信等 smsd 存好确认后才放手；紧急警报（小区广播）在锁屏上也弹出，国家级不能关；只有默认短信应用能删和拦截（模拟基带；对着模拟的 ModemManager 测过（33 个用例），QEMU 上自测通过，未上真机） |
| [33-app-guidelines.md](33-app-guidelines.md) | 应用规范：界面怎么设计（带正反例截图）、商店怎么审（自动检查和人工审核的标准）、隐私标签 |
| [34-app-tutorial.md](34-app-tutorial.md) | 教程：用 KarioUI 一步一步写一个应用（含推送和后台任务），`kario run` 调试、`kario test` 测试、打包上架、Kario Studio 扩展 |
| [35-quickstart.md](35-quickstart.md) | 十分钟上手：`make install-kario`，TypeScript 应用在终端里和在 Cursor 里（Kario Studio），整个用 C++ 或 Go 写的应用，带 C / C++ / Rust 原生模块的 TypeScript 应用；每条命令带实际的输出 |
| [36-device-switch.md](36-device-switch.md) | 换机协议 kario-switch/1：旧手机上的「Kario 换机」应用经热点把通讯录、通话记录、短信、日历、照片传到 Kario，二维码配对、端到端加密；手机一侧的接收端（设置 › 我的设备 › 从旧手机迁移，可撤销）在开发机和 QEMU 上测过（经热点那条路），**未上真机** |
| [37-motion.md](37-motion.md) | 动效：哪些动画适合 Kario（焦点滑动、整页推入推回、菜单升起、背光渐变……），实测整屏重画和合成的耗时，「画一次、只挪」的图层合成和按垂直同步翻页；整页推入推回、菜单升起、焦点滑动、设置里的开关已做，真屏幕上**还没看过** |
| [38-contributing.md](38-contributing.md) | 贡献与 PR 流水线：按改动挑选的检查和测试（`scripts/ci-plan.py`）、唯一的门禁检查 `pr-ok`、CI 通过后的 AI 评审报告（参考意见，不跑 PR 的代码）、CodeQL 代码扫描、在本机跑同样的检查；工作流在 GitHub 上**未验证** |
| [39-oneplus-6t.md](39-oneplus-6t.md) | 一加 6T（fajita，骁龙 845）：第一台真机。买什么、手机怎么准备（OxygenOS 11.1.2.2、两个槽的固件一致、解锁、MSM 救砖）、构建、先 `fastboot boot` 再刷入、没有串口时的 USB 控制台、开机后先查什么、回到 Android；一台 6T 上的实际记录（OxygenOS 11 上 TWRP 用不了，改在 Nura 的调试 shell 里备份、写回 EFS，出 EDL）；真机待验证清单（每项跑什么、应该看到什么、记到哪、不要做什么）；内核、配置和固件取自 Nura（原 postmarketOS），镜像构建过，Kario 在手机上**未上真机** |
| [40-testing.md](40-testing.md) | 测试方法与策略：分层（静态检查、单元、端到端、自测、兼容测试、像素自测、基带后端、模糊测试、QEMU 开机自测和专项、浸泡）、一份场景脚本四种跑法、快照和基线比对、写用例的规则、模拟了什么、改了什么跑什么、发布前跑什么、还没有的（真机、像素基线、覆盖率） |
| [41-design-gaps.md](41-design-gaps.md) | 设计不足与未验证的地方（2026-11-21 准备一加 6T 时整理，2026-11-28 按当前代码和夜间链逐条重新核对）：真机验证刚开始（整机开过四回，之后的改动大多**未上真机**）、渲染与显示（Adreno 上的 GPU 合成**未上真机**、DRM 独占、CI 没有像素基线）、6T 上没有 A/B 和 OTA、VoLTE 和通话音频等通信缺口、排斥 GPLv3 的代价、测试与构建的脆弱点（`-race`、gomods、备份脚本要 TWRP、整机上第一个蓝牙键盘）；已关上的列在 §20，写着日期和证据；标着谁**正在做** |
| [42-gpu-rendering.md](42-gpu-rendering.md) | GPU 渲染：三个层次（上屏、合成、栅格化）；phone-paint 的 GPU 合成（L1，含上屏）实现了、可选（`PHONE_PAINT_GPU=1`），怎么打开、为什么在沙箱里启动、镜像里的 Mesa（softpipe、freedreno）和大小，QEMU 上用软件 GPU 的验证（`make yocto-gui-gpu`）；GPU 上栅格化（L2）也实现了、可选（`PHONE_PAINT_GPU=raster`），开发机的 llvmpipe 上逐字节核对过；都**未上真机** |
| [43-phone-partitions.md](43-phone-partitions.md) | 手机的分区：Android 的每一类分区 Kario 写不写、读不读、绝不碰哪些；板子文件里的分区表（分区名 → 角色），刷机脚本、initramfs、RAUC 都从它取名字；一加 6T 的两种布局（整个 userdata 是根文件系统，默认；根文件系统在 `system_a/b`、数据在 userdata 的 A/B 布局），持久区留在 userdata 上的理由，ABL 管槽时的 RAUC（`rauc-abl`）；刷机脚本写之前量大小、绝不写 EFS 和固件的分区；别的手机（动态分区、展锐）；Android 的新分区机制要哪些（AVB 量产时要，动态分区只为兼容，Virtual A/B 以后给存储小的手机做，dlkm 分区不要，都**未实现**）。脚本和单元测试 ✓，A/B 布局的镜像 2026-11-24 起构建过多次、都没开过机，**未上真机** |
