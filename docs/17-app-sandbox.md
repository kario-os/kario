# 应用行为限制：沙箱、权限、后台

> 状态：**原型 ✓**。启动器 `phone-sandbox`、能力代理（broker）和后台冻结都已实现。设备（QEMU，内核 6.18）上沙箱自检 13/13 符合预期（§7）。剩余工作见 57（内部文档） R47（§8）、R55（§9）。

## 0. 结论

Android 和 iOS 的思路一样：应用默认什么都不能做，能做的每件事都要经过系统。差别在实现方式：

- **Android**：每个应用一个 uid，加 SELinux 强制访问控制、seccomp 系统调用过滤、运行时权限、后台执行限制。
- **iOS**：沙箱配置文件（Seatbelt）、entitlement（签名里写明的特权）、代码签名加审核，应用离开前台后被挂起。

本平台把应用关在七层里。应用和系统打交道的**唯一通道**，是 Shell 启动它时交给它的那条连接（fd 3）；除此之外的一切，都在内核层被拒绝。

| 层 | 做什么 | 对应 |
|----|--------|------|
| 1. 通道 | 每个应用一个进程，只拿到 fd 3；所有能力经 Shell 的 broker 检查清单权限、运行时授权后再转发 | iOS XPC + entitlement；Android Binder + 权限 |
| 2. 独立 uid | 每个应用一个 uid（10000 起），只拥有自己的数据目录；系统服务的 socket 和密钥文件属于 root、权限 0600 | Android 应用 uid |
| 3. 命名空间 | 独立 PID 空间（看不到其他进程）、私有 `/proc`、IPC/UTS 隔离；没有 `net` 权限的应用在空网络命名空间里（连本机回环都没有） | Android 进程隔离；iOS 无网络 entitlement |
| 4. Landlock | 文件白名单：只读系统代码，只能读写自己的数据目录；**不能执行自己写入的文件**（W^X）；不能监听 TCP 端口；不能给沙箱外的进程发信号或连接沙箱外的抽象 unix socket | iOS Seatbelt；Android SELinux 的应用域；Android 10 起禁止从数据目录执行 |
| 5. seccomp | 拦截调试其他进程、挂载、内核模块、eBPF、perf、内核密钥环、io_uring、创建新命名空间等，返回 EPERM | Android 应用 seccomp 过滤器 |
| 6. 资源上限 | 禁止 core dump；文件描述符 1024、进程数 256。设备上每个应用还有自己的 cgroup v2 组：内存 192 MB、不用交换、进程数 128；CPU 权重前台 400、后台 50；Go 程序另有 `GOMEMLIMIT` 软上限（144 MB）。超过内存上限时内核只结束这一个应用，系统提示「超出内存上限」。系统内存紧张时，Shell 先结束后台应用（§5.1） | Android 的 memcg 与 lmkd；iOS 的 jetsam |
| 7. 后台冻结 | 离开前台约 10 秒后冻结，回到前台时恢复；声明 VoIP 后台模式并持有权限的应用例外。设备上用 cgroup 冻结器，整棵进程树一起停；开发机没有 cgroup 委派、或内核没有冻结器（5.2 以前）时退回 SIGSTOP（发给应用的整个进程组） | iOS 后台挂起；Android cached apps freezer（也是 cgroup v2 冻结器） |

在这七层之外还有三层：

- **界面层**（已有）：应用只能提交语义节点，画不出系统对话框；读不到原始按键（输入法归系统，[16](16-input-method.md)），读不到屏幕，也读不到其他应用；安全画面在截图和录屏时被遮挡。
- **分发层**：应用包签名，清单里的权限就相当于 entitlement，安装时验签并由系统弹框确认；开机后首次启动前再核对一次文件，改过的不让运行；开发商店签名的包只在开发者模式下能装、能运行；卸载时连同密钥一起删除（[22](22-developer-api.md) §12）。
- **协议层**：应用发来的每条消息最多 4 MB，一个页面最多 1000 个节点，页面栈最多 32 层；通知每分钟最多 5 条带横幅和提示音；卡住的前台应用 5 秒后由系统提示「没有响应」（§5.2）。

## 1. 与 Android / iOS 逐项对照

| 方面 | Android | iOS | 本平台 |
|------|---------|-----|--------|
| 身份 | 每应用 uid | 每应用容器 | 每应用 uid ✓ |
| 文件 | uid 权限 + SELinux | 沙箱容器 | uid 权限 + Landlock ✓ |
| 执行下载的代码 | Android 10 起禁止从数据目录执行 | 禁止（只有系统 JIT） | Landlock 禁止执行可写位置的文件 ✓（`execve`）；带原生模块的 TypeScript 应用和自带程序的应用一样，机器码映射可执行页 Landlock 管不到，靠审核（[22](22-developer-api.md) §9.3） |
| 看到其他进程 | 不能 | 不能 | 独立 PID 命名空间 ✓ |
| 网络 | INTERNET 权限（uid 级防火墙）；按应用限制移动数据、按应用统计流量 | 默认允许；按应用关蜂窝数据 | 无 `net` 权限时在空网络命名空间 ✓；有权限的应用，用户能在设置里关掉它的网络（应用重启后在空网络命名空间里）或设成只用 Wi-Fi（立即生效）✓；「设置 › 流量」按应用、按 Wi-Fi / 移动网络统计收发字节 ✓（见下） |
| 内核攻击面 | seccomp | 沙箱配置 | seccomp ✓ |
| 后台运行 | Doze、后台限制、freezer | 挂起，少数后台模式例外 | 10 秒后冻结，VoIP、正在播放的音频例外 ✓；推送按预算唤醒 ✓（[21](21-push.md)）；内存紧张时被结束的后台应用留墓碑，回去时恢复原来的页面 ✓（§5.3） |
| 能力 | 权限 + Binder 服务 | entitlement + XPC | 清单权限 + 运行时授权 + broker ✓ |
| 权限用途 | 理由由应用自己在授权前说明，系统授权框里没有 | Info.plist 的 usage description，授权框里显示，没写就用不了这个权限 | 清单的 `purposes` 给每个运行时权限（相机、麦克风、通讯录、相册、位置）写明为什么要（1–80 个字）：系统的授权框在权限后面显示它，安装确认里也列出；没写的应用装不上、商店收不进，审核台在待审列表里列出权限和用途，由审核员核对（`shell/install.go`、`appstore/store.go`）。小程序借宿主的权限时不显示宿主的用途（那不是小程序的理由），而显示小程序自己的：它的 `app.json` 按系统权限名写 `purposes`（英文，由它自己的翻译表翻译），宿主验签加载后用 `SetPurposes` 登记，替它发的请求带着这些用途，授权框只显示被申请权限的那几条、超长的不显示；宿主为它们负责，和来源标注一样（`apps/miniapp`、`sdk.App.SetPurposes`）✓ |
| 系统界面 | 悬浮窗权限可覆盖 | 不能覆盖 | 不能覆盖，系统界面只能由 Shell 画 ✓ |
| 锁屏 | 锁屏凭据由 Gatekeeper 校验，锁着时应用的界面和通知内容被挡住；指纹模板在 TEE | 密码由 Secure Enclave 校验，同左 | 锁屏 PIN 码由 phone-keystored 校验；锁着时前台应用藏到锁屏后面，通知只显示应用名和条数，按键只到锁屏（通话中只到通话界面）；应用拿不到 PIN 码和指纹，只能经 `auth.confirm` 得到指纹确认的签名 ✓（[31](31-lock-and-biometrics.md)；TEE **未实现**） |
| 输入 | 第三方输入法能看到所有按键 | 第三方键盘受限 | 输入法归系统，应用只收上屏文字 ✓ |
| 截屏、录屏 | FLAG_SECURE | 系统控制 | 安全画面遮挡 ✓；没有给应用的截屏接口 |
| 剪贴板 | Android 10 起后台不可读 | 粘贴时提示 | 应用只能写不能读；粘贴是系统菜单项，文字由系统填进输入框；10 分钟后清空 ✓ |
| 位置 | 运行时权限，后台位置另要授权；Android 12 起授权框可选「大致位置」；有位置总开关，「位置」设置里列出最近 24 小时访问过位置的应用 | 运行时权限，状态栏指示；可关掉「精确位置」 | 运行时权限 `location`，默认只给前台应用：持续定位（`location.watch`）在应用到后台时暂停、回来接着发；后台位置另要 `location_background`，授权框不问，只能由用户在设置里改成「始终允许」，在后台用了会被记下并定期提醒 ✓；授权框可选精确、大致（约 2 公里的格子）或拒绝，之后可在设置里改 ✓；有应用在持续定位、有一次定位在等、或 3 秒内交出过定位时状态栏显示位置图标；系统设置里的总开关关掉后所有应用都拿不到，接收机立即关；「设置 › 位置」列出最近 24 小时谁用过（含后台）✓；网络定位只在总开关开着、有授权的应用要位置时问，隐藏和 `_nomap` 网络不发；开发机和 QEMU 上的接收机和网络定位服务是模拟的（NMEA 走真的解析器），真机的 GNSS **未上真机**（[22](22-developer-api.md) §4.7） |
| 短信、拨号 | SEND_SMS、CALL_PHONE 是运行时权限，发往付费短号另要确认；Play 只许默认短信应用申请短信权限 | 应用发不了短信（只能打开写好的短信让用户发），拨号先弹确认 | 商店安装的应用每发一条短信、每拨一次号，系统都弹框写明发给谁（和内容）、打给谁，用户确认才发，声明了 `sms` / `phone` 也一样；系统镜像里的短信、电话应用不问，通讯录拨号也不问 ✓ |
| 通讯录 | READ_CONTACTS、WRITE_CONTACTS 两个运行时权限（同一组，一起授予）；不管通讯录的应用加联系人常用 `ACTION_INSERT` 交给通讯录应用 | 读写一个授权（iOS 18 起可只给部分联系人）；`CNContactViewController` 让用户自己存 | 读要运行时权限 `contacts`；写不另设权限，每次由系统弹框写明改什么、请用户确认：新建不要权限，修改、删除、导出还要 `contacts`，导入由用户在系统列表里选文件；系统的通讯录应用例外（用户就在它里面编辑）；常用的是 `intent.invoke` 的 `contact.save`，交给通讯录让用户填好再存 ✓（[22](22-developer-api.md) §4.2）；只交出一个联系人的系统选择器**未实现** |
| 长期不用的应用 | Android 11 起几个月不用，自动收回运行时权限 | — | 商店安装的应用 90 天没打开，运行时授权（摄像头、麦克风、通讯录、相册、位置，连同它的小程序的）收回，下次用时再问；设置发一条通知，「应用权限」里注明哪天收回的 ✓ |
| 定时后台任务 | WorkManager / JobScheduler，最短 15 分钟，有时间窗口和充电约束 | Background App Refresh，系统决定时机 | `job.schedule`，最短 15 分钟；时间窗口内合并成一次唤醒、跟着推送顺带跑、可要求只在充电时；和推送共用唤醒预算 ✓ |
| 进程间传文件 | Binder 传 fd | XPC 传 fd | 只从系统传给应用；应用发来的 fd 立即关闭 ✓ |
| 系统界面进程 | SystemUI 是系统进程 | SpringBoard 是系统进程 | 两个渲染器都降权运行：空网络命名空间、nobody、no_new_privs、Landlock、seccomp（禁止运行程序）；照片缩略图由 Shell 内嵌在帧里，渲染器读不到相册；崩溃后由 Shell 重启 ✓（第十、十一轮） |
| 连电脑的文件共享 | MTP 在 MediaProvider 进程里，不在 system_server 里 | 「访达」的文件共享（AFC）只能访问声明了文件共享的应用的沙箱 | 解析电脑请求的服务器在单独的进程 `phone-usbshared` 里，经 `phone-sandbox` 用助手策略启动（§2）：自己的 uid、空网络命名空间、Landlock 只许运行它自己的程序、seccomp 禁止 exec、新建套接字和按路径的文件操作；监听套接字和文件都由 Shell 打开后交 fd，Shell 检查它的每个请求；崩溃后由 Shell 重启（[27](27-usb-typec.md) §3.2）✓（开发机和 QEMU） |
| 小程序 | WebView 内容用宿主的权限 | 同左 | 小程序的运行时授权单独记账（宿主 + 小程序），宿主的授权不传给小程序，可单独撤销 ✓；每个小程序在自己的 kario-jsrt 进程里，宿主旁边有 `phone-sandbox` 时再套一层沙箱（[29](29-app-development.md) §5.4） ✓ |
| 网页 | WebView 在应用进程里，页面用宿主的权限 | WKWebView 的网页内容在独立进程 | 每个应用的网页在一个单独的进程里，用单独的 uid（`<应用>/web`）和自己的 cgroup（内存上限按机型），数据在自己的目录，宿主和页面互相读不到；默认打不开 GPU 设备，机型允许时只放行渲染节点；联网跟宿主的网络权限，流量记在宿主名下；页面的 `phone.call` 只开放白名单能力，授权按网页来源单独记账 ✓（[14](14-tui-route-and-app-framework.md) §5） |
| 分享 | Intent 选择器 | 分享表单 | 系统面板，目标由清单声明；发起方看不到用户装了什么；分享的照片随之授权 ✓ |
| 分发 | 签名 + Play 审核，分批发布 | 签名 + App Store 审核，分阶段发布 | `.phoneapp` 签名包：验签、证书链、文件清单，系统弹框确认；更新要同一开发者签名；商店后台的自动检查、人工审核、下架和设备定时取的吊销清单；更新可分批发布，崩溃多时自动叫停 ✓（[07](07-ecosystem-and-store.md) §4.1） |
| 系统权限 | `signature` 级权限只给平台签名的应用 | 私有 entitlement | `system`、`install` 只给系统镜像里的应用：安装器拒绝，已安装的应用声明了也拿不到 ✓ |
| NFC 卡模拟 | HCE 服务要 `BIND_NFC_SERVICE`，系统按 AID 把读卡器的命令交给它；付款类只给默认付款应用；`requireDeviceUnlock` | 卡在安全芯片里，Wallet 管快捷交通卡；iOS 18 起部分地区开放 HCE | 声明即有的 `nfc` 权限；读卡器的命令只交给声明了它所选 AID 的卡的应用，付款卡只给默认付款应用、只在解锁时；系统复制的门禁卡的 UID 在 phone-nfcd 里，应用拿不到，也看不到卡包里的其他卡；管卡的是系统应用「卡包」（`nfc.state` / `nfc.set` 要 `system` 权限）✓（[30](30-nfc.md)，模拟控制器） |

**按应用流量与只用 Wi-Fi**（包 `netfw`，设备上需要 root）：每个应用有自己的 uid，Shell 在 nftables 里建一张表 `inet phone`，出方向按套接字的 uid 匹配，入方向按连接标记匹配（出方向的规则把连接标记设成 uid，回包就能认出是谁的）。每个应用、每类网络、每个方向一个具名计数器；只用 Wi-Fi 的应用从非 Wi-Fi 网卡发出的包被丢弃，应用收到 EPERM，马上失败，不用等超时；移动数据被挡住的应用（关了后台数据又不在屏幕上，或它自己的流量上限用完了，[19](19-core-apps.md) §1.3）用的是同一条规则，挡不挡一变就换。规则整体替换在一个事务里完成，计数器在规则之外，所以改规则、Shell 重启都不清零。Shell 每 30 秒、打开「流量」页、卸载应用和正常关机时把计数器原子地读出并清零（`Harvest`），按天、按应用累加进流量的记录（`data-usage.json`，[19](19-core-apps.md) §1.3，那里还有流量套餐）；Shell 崩溃不丢（计数还在内核里），只有断电会丢掉上次读数之后的那一段。内核没有具名计数器（nf_tables 的有状态对象，Linux 4.10）时这张表起不来：没有按应用的流量和只用 Wi-Fi，后台数据和应用自己的上限只是 Shell 的策略（告诉应用、后台任务等 Wi-Fi，连接不挡），流量页只有各网络的总量（网卡的计数器）。几个约定：回环（本机服务之间）不计；名字以 `wl` 开头的网卡算 Wi-Fi，其余都算移动网络，认不出的网卡按计费网络处理；用的是纯 Go 的 netlink 库（`google/nftables`），镜像里不需要 `nft` 命令行（它会拉进 GPLv3 的 readline）。内核要有 conntrack 和 `nft_ct`：镜像装上 `nft_ct` 模块包，规则第一次用到连接标记时内核自动加载它（连带 conntrack）。个人热点（[22](22-developer-api.md) §4.5）开着时同一张表里多两条链：`share-nat-hotspot`（nat，postrouting）把热点子网发出默认路由网卡的包伪装成手机的地址；`share-fwd-hotspot`（forward）只放热点到默认路由网卡的包和回来的回包，热点上的设备经手机到不了别的网卡；它放行的包照样被移动数据的切断挡住（哪条链丢包都算数）。热点关掉时两条链删掉。伪装要 `nft_masq`、`nft_chain_nat` 和 `nf_nat`，镜像装上这几个模块包，第一次加规则时内核自动加载（模拟器上 ✓，自测「个人热点（模拟器）」）。开发机上 Shell 不是 root，没有这项功能，设置里如实显示。

**VPN 和按应用防火墙**：VPN（[11](11-platform-architecture.md) §16）连着时，应用发出的包从隧道的网卡 `kario-vpn` 出去，防火墙照样按应用的 uid 认出它们、计数；隧道本身封装好的 UDP 包是内核的 WireGuard 发的，不是哪个应用的 uid，没有哪条按应用的规则挡它。隧道按它底下的网络算：Shell 每 5 秒查一次隧道自己的包（带它的防火墙标记）从哪块网卡出去，是 `wl` 开头的就把 `kario-vpn` 也算 Wi-Fi，否则算移动网络，于是「只用 Wi-Fi」的应用在隧道走 Wi-Fi 时能用它，走移动网络时发不出去，「设置 › 流量」也按底下的网络记。关掉移动数据时防火墙挡住移动网卡，隧道若走移动网络也就断了。始终开启的 VPN 打开「屏蔽未使用 VPN 的连接」时，同一张表里多一条输出链 `vpn-lock`（`netfw.Lock`，排在按应用的规则前面，只会多丢包）：隧道连着断着都在，除了回环、隧道的网卡 `kario-vpn`、带隧道防火墙标记的包（隧道自己的，和按应用排除在外的）、发往 VPN 服务器的 UDP、DHCP（68 到 67、546 到 547）、IPv6 的路由器请求和邻居发现，以及只通到身边设备的网卡（USB 线的、热点开着时热点的），别的包一律丢掉，应用马上收到 EPERM；服务器写的是域名时再放 uid 0 查 DNS（Shell 自己问上游 DNS 服务器解析，systemd-resolved 和应用的查询照样挡住）。按应用走或不走 VPN（`netfw.Apps`）：一条 route 类型的输出链 `vpn-apps` 按套接字的 uid 给包打上隧道的防火墙标记（「只有选中的应用」时给没选中的、连同系统的包打，「除选中的应用外的全部」时给选中的打；应用的网页视图跟着应用），内核随即重新选路，带标记的包不查隧道的表；套接字连接时按没标记的路由选了隧道的源地址，这样的包从别的网卡出去时由 nat 链 `vpn-apps-nat` 伪装成那块网卡的地址，回包才回得来（依赖宽松的反向路径过滤，镜像里 systemd 的默认值 `rp_filter=2`）。没覆盖的：隧道换了底下的网络，最多 5 秒后规则才跟上；没有网络权限的应用在空网络命名空间里，VPN 和它无关；没开屏蔽时，隧道断开或连接中的那一会儿应用的流量直接走底下的网络；热点上的设备经手机转发的流量不走 VPN，屏蔽也不管它；全隧道只接管 AllowedIPs 里的地址族（只有 0.0.0.0/0 时 IPv6 不走隧道，和 wg-quick 一样）；屏蔽和按应用在模拟器上的用例**还没跑过**，**未上真机**。

## 2. 启动流程

```
Shell（root）
  │ clone(NEWNS | NEWPID | NEWIPC | NEWUTS [| NEWNET，无 net 权限时])
  ▼
phone-sandbox（在新命名空间里，此时仍是 root）
  │ 1. 挂载私有 /proc（只看得到自己）
  │ 2. rlimit：core 0、fd 1024、进程 256
  │ 3. setgroups([]) → setresgid / setresuid(应用 uid)：丢掉全部特权
  │ 4. 建立 TMPDIR（数据目录下的 tmp/，没有共享 /tmp）
  │ 5. no_new_privs：之后任何 exec 都不能再获得特权
  │ 6. Landlock 规则集 → restrict_self
  │ 7. seccomp 过滤器（所有线程；内核没有 seccomp() 时用 prctl 装在这个线程上）
  │ 8. execve(应用)，环境变量 PHONE_SANDBOX 列出实际生效的层
  ▼
应用进程：只剩 fd 3 一条路
```

- 限制一旦施加就不能撤销，exec 之后由应用继承。这和 Android 的 minijail、Chrome 的沙箱做法相同。
- 开发机上 Shell 不是 root，只能施加不需要特权的几层：rlimit、seccomp，以及内核支持时的 Landlock（这台开发机内核是 4.18，没有 Landlock）。设置里的「沙箱自检」会把不适用的检查项标成「跳过」。
- Shell 以 root 运行却找不到 `phone-sandbox` 时，会**拒绝启动应用**，不会退回到不受限的状态。
- **系统助手**（现在只有连电脑的文件共享 `phone-usbshared`，[27](27-usb-typec.md) §3.2）走同一个启动器，策略里写 `helper`：命名空间、uid 和上面一样，但没有数据目录，也不放行任何设备，Landlock 只许读、执行它自己的程序。助手拿到继承的 fd 以后再给自己加一层 seccomp（`sandbox.DescriptorsOnly`，层名 `fd-only`）：exec、新建套接字（socket、socketpair、bind、connect、listen）和按路径的文件操作都返回 EPERM。这一层不需要 root 也不需要 Landlock，所以开发机上也有：那里生效的是「rlimit · seccomp · fd-only」。Shell 以 root 运行却找不到启动器时同样拒绝启动助手。

## 3. Landlock 规则

| 路径 | 权限 |
|------|------|
| `/usr` `/lib` `/lib64` `/bin` `/sbin`、应用安装目录 | 读、执行 |
| `/etc` `/proc` | 读 |
| 有 `net` 权限时：`/etc/resolv.conf` 最终链到的目录（镜像上是 systemd-resolved 的 `/run/systemd/resolve`） | 读。放行整个目录是因为 resolved 随网络变化整个换掉这个文件；没有这一条时应用读不到解析器的配置，去问 `127.0.0.1:53`，那里没有服务，域名一个也解析不了（Go 应用、网页进程一样）。自测「沙箱里的 DNS」在设备上核对有 `net` 的鸿雁读得到、没有的设置读不到 |
| 厂商根公钥（单个文件） | 读 |
| 自己的数据目录（含 `tmp/`） | 读写、建删文件；**不能执行** |
| `/dev/null` `/dev/zero` `/dev/urandom` `/dev/random` | 读写 |
| 其他一切 | 拒绝 |
| TCP | 不能监听；没有 `net` 权限时也不能连接 |
| 作用域 | 不能给沙箱外的进程发信号，不能连接沙箱外的抽象 unix socket |

内核的 Landlock ABI 版本越新，能管的就越多。启动器会按版本启用对应的规则，旧内核自动降级。

## 4. seccomp 拦截

返回 EPERM 的系统调用：
- 调试与读写其他进程：`ptrace`、`process_vm_readv/writev`、`pidfd_getfd`。
- 改变文件系统视图：`mount`、`umount2`、`pivot_root`、`chroot`，以及新挂载接口（`fsopen` 等）。
- 新建命名空间：`unshare`、`setns`、带命名空间标志的 `clone`。`clone3` 返回 ENOSYS，让 libc 回退到可检查标志的 `clone`。
- 内核模块与内核替换：`init_module`、`finit_module`、`delete_module`、`kexec_*`、`reboot`。
- 内核攻击面大的接口：`bpf`、`perf_event_open`、`userfaultfd`、`keyctl` 等密钥环调用、`io_uring_*`。
- 系统状态：`swapon/off`、`acct`、`quotactl`、改系统时间（`settimeofday` 等）、`syslog`、`open_by_handle_at`。

架构不符（比如在 AArch64 上执行 32 位调用）时直接杀掉进程。

## 5. 后台冻结

- 应用离开前台时，先收到 `lifecycle: background` 消息，有 10 秒时间保存状态（SDK 这时调 `OnSave`，把重建页面要的东西交给系统，§5.3），之后被 SIGSTOP 暂停；回到前台前先 SIGCONT 恢复。
- 例外：
  - 清单里声明 `"background": ["voip"]` 并持有 `voip` 权限的应用（聊天应用要接来电）；
  - 正在通话的应用；
  - 声明 `"background": ["audio"]` 且正在播放的应用（音乐）。暂停或放完后，照常冻结。
- 冻结期间，Shell 发给应用的消息排在连接缓冲里，恢复后再处理；Shell 自身不会因此阻塞。
- 推送（[21](21-push.md)）：
  - 只带标题和正文的消息由系统显示，不唤醒应用；
  - 带数据的消息把冻结的应用恢复 3 秒，进程不在时在后台启动它，每个应用每小时最多 20 次；
  - VoIP 推送不限次数。
- 后台定时任务（[22](22-developer-api.md) §4.7）：应用登记「每 N 分钟（至少 15）刷新一次」，到期时系统按和推送相同的规则恢复或后台启动它，发 `job` 事件，同样计入每小时 20 次的预算。天气应用用它每小时刷新。
- 有了推送，鸿雁不声明 `voip` 后台模式，离开前台照样冻结，消息和来电都靠推送（[21](21-push.md) §4）。`voip` 后台模式仍然保留给确实需要的应用，但不推荐使用。
- cgroup，两种做法：
  - **应用作用域**（显示服务用，`PHONE_APP_SCOPES=1`）：每个应用一个 systemd 临时作用域 `phone-apps.slice/app-<id>-<pid>.scope`，上限作为作用域的属性（`MemoryMax`、`TasksMax`、`CPUWeight`）交给 systemd。Shell 启动应用后经 D-Bus 调用 `StartTransientUnit` 把它放进去，沙箱启动器在这之前一直停在一个管道上等，所以应用的代码不会在 Shell 的组里跑。Shell 自己的服务组里没有应用，崩溃后 systemd 能重启 Shell，应用留在各自的作用域里等它接管（[11](11-platform-architecture.md) §14）。
  - **委派子树**（开机自测服务用）：systemd 用 `Delegate=yes` 把服务的组交给 Shell（Shell 看 systemd 打的 `trusted.delegate` 标记确认，root 对任何组都有写权限，看写权限没用）。Shell 先把自己和服务里的其他进程移进子组 `shell`，再给每个应用建 `app.<id>` 组，应用进程直接在自己的组里启动（`CLONE_INTO_CGROUP`）；内核没有它（5.7 以前）时启动器先停在管道上，Shell 把它移进组再放行，和应用作用域一样，应用的代码不会在 Shell 的组里跑。
  - 冻结写 `cgroup.freeze`，连子进程一起停，别的进程发 SIGCONT 也解不开。
  - 老内核上 Shell 开机时逐项探测（冻结器、`cgroup.kill`、`CLONE_INTO_CGROUP`、内存、进程、CPU 控制器、PSI），缺了的用老办法做或者不做，并如实报出来（[24](24-sku-framework.md) §7.5）。
  - 应用退出后，Shell 看 `memory.events` 判断是不是因为内存超限，然后结束组里剩下的进程（`cgroup.kill`；5.14 以前没有它，就逐个 SIGKILL 组里的进程，直到组空），等它们真的退出（`cgroup.kill` 不等）再删掉这个组；再启动时，上次留下的组先结束、删掉再新建。死掉的进程树留下的页缓存还记在旧组上，留着组就算进新进程的用量：低内存压力测试里，浏览器从墓碑恢复后的网页进程就这样一起来就超了 WebKit 的上限，下一个卡在页面上。网页进程的组也一样。
  - 代码在 `shell/cgroup.go`、`scopes.go`。

### 5.1 内存紧张时：低内存清理

- 每秒看一次系统内存：`/proc/meminfo` 里可用内存（3.14 以前的内核没有 `MemAvailable`，按 MemFree + Buffers + Cached − Shmem 估算）低于总量的 10%，或者内存压力（PSI，应用组的 `memory.pressure`，没有 cgroup 时用 `/proc/pressure/memory`）的 `some avg10` 超过 20%，就算紧张。
- **先结束后台应用的网页**：一个应用的网页进程能用到几百 MB（按机型的上限，2 GB 的机型 512 MB），所以紧张时先结束一个后台应用的网页进程，应用本身照常留着，系统应用（浏览器）的也算；回到那一页时页面在新进程里重新载入，和浏览器丢弃后台标签页一样。选哪个和下面选应用的顺序相同，同一类里先结束网页用内存最多的（网页 cgroup 的 `memory.current`）。前台应用和在后台工作的应用的网页不动。
- 后台已经没有网页可结束时，一次结束一个后台应用，之后等 2 秒再判断。按类别：先是从没到过前台的（推送、后台任务在后台启动的），再是较早用过的，最后是最近用过的两个；同一类里先结束占内存最多的（cgroup 的 `memory.current`，没有 cgroup 时看常驻内存），一次腾出的最多。
- 前台应用、通话中的、正在后台播放的、系统应用都不会被选中。被结束的应用不算崩溃，留下墓碑，下次打开时回到原来的页面（§5.3）。
- 代码在 `shell/lowmem.go`；自测用 `mem-low` 模拟内存紧张，网页视图的用例检查浏览器和鸿雁的网页先被结束、应用还在、回去后页面重新载入（`pages-ended`）。

### 5.2 卡住和崩溃

- **没有响应**：用户在前台应用里操作之后，系统发 `ping`，SDK 在处理动作的同一个循环里回 `pong`。5 秒没回，系统弹框「没有响应」：「等待」再给 5 秒，「关闭应用」结束它；应用恢复后对话框自动消失。应用正在等系统回复它的能力请求（比如授权框还开着）时不算没有响应；后台和冻结的应用不检查（`shell/anr.go`）。
- **崩溃记录**：应用自己退出且退出码不是 0 时，系统记下时间、版本、原因和日志最后 20 行，最多留 50 条（`shell/crash.go`）。系统结束的（低内存清理、内存超限、用户在「没有响应」里关闭、卸载）不算崩溃。
- **崩溃循环**：10 分钟内崩溃 3 次的应用，系统暂时不在后台启动它；推送和后台任务的数据留到用户打开它时再交付。

### 5.3 墓碑：被结束的后台应用回来时恢复原样

- 低内存清理结束一个后台应用时（§5.1），系统留下它的墓碑：应用最后一次交给系统的状态（离开前台时 SDK 调 `OnSave`，经 `state` 消息交上来，至多 64 KB 的 JSON，[22](22-developer-api.md) §3）和它当时的页面栈。应用留在「切换应用」里。
- 回到它时（从「切换应用」、主菜单、通知都一样；推送或定时任务先在后台启动它也一样），系统马上显示原来的页面，同时启动新进程，`start` 消息带上那份状态（SDK 的 `Restore`），应用照着重建页面。重建的页面屏幕 ID 和原来一样时，输入框里打了一半的字、焦点在哪，由系统原样还回去，应用不用自己存。新页面到之前，旧页面只显示（它们的动作属于已经结束的进程）：这期间按的键先存着（至多 32 个），新页面到齐后再交给它，返回照常回待机；冷启动时在第一页出来之前按的键也一样。按键始终只投给前台（有焦点的）应用：存着的是给它的键，只是它的新进程还没准备好；它不在前台了（用户回了待机、切到别的应用），这些键就扔掉，不会交给别人。这和 Android 的 InputDispatcher 一样：焦点窗口还没准备好接收时，按键在队列里等它（等太久报「应用无响应」），而不是丢掉或交给别的窗口。
- 墓碑只留一天；用户在「切换应用」里结束它、应用更新或卸载、应用自己退出或崩溃，都不留。
- 系统刚结束一个应用（不管为什么）、还没收到它的退出时就又要打开它，系统等旧进程退出后马上在前台启动新进程，不会把正在死掉的那个放到屏幕上、等它退出时又掉回待机（`shell/relaunch_test.go`）。
- 和 iOS 的做法一样（快照加状态恢复）；Android 是系统留着 Activity 栈，各页面从保存的 Bundle 重建。
- 自测：鸿雁打开和张三的会话，输入框里写了「明天见」没发，回待机；`lowmem-kill` 像低内存清理那样结束它，从「切换应用」回去，还在这个会话里，草稿还在，按 OK 就发出去。代码在 `shell/tomb.go`。

## 6. 自检（设置 → 沙箱自检）

设置应用扮演一个想作恶的应用，逐项尝试：

| 检查 | 期望 | 依赖的层 |
|------|------|----------|
| 读设备私钥 | 拒绝 | 独立 uid |
| 读其他应用数据 | 拒绝 | 独立 uid |
| 直连密钥服务 | 拒绝 | 独立 uid |
| 执行自己写入的代码 | 拒绝 | Landlock |
| 写公共临时目录 /tmp、/var/tmp | 拒绝 | Landlock |
| 看到其他进程 | 拒绝 | 命名空间 |
| 访问网络 | 拒绝 | 无网络命名空间 |
| 监听 TCP 端口 | 拒绝 | 无网络命名空间 / Landlock |
| 危险系统调用（如 keyctl） | 拒绝 | seccomp |
| 写自己的数据目录 | 允许 | — |
| 读厂商根公钥 | 允许 | — |
| 打开显示设备 /dev/dri | 拒绝 | 独立 uid |
| 读键盘设备 /dev/input | 拒绝 | 独立 uid |

这份检查表（`sandbox/selfcheck_linux.go`）同时被兼容测试使用：厂商削弱了沙箱，兼容测试就过不了。页面上方显示实际生效的层。没有生效的层，其检查项标为「跳过」，不计入结果。

## 7. 验证

| 环境 | 结果 |
|------|------|
| 单元测试（`sandbox`） | 在子进程里施加沙箱：`keyctl`、`unshare` 返回 EPERM，普通系统调用、写数据目录、执行系统程序正常；内核有 Landlock 时还检查写数据目录外和执行数据目录内的文件被拒绝 |
| 开发机自测（非 root，内核 4.18） | 生效层为「seccomp · 资源上限」，自检 3/3 符合预期（8 项跳过）；后台冻结用例：离开钱包 1 秒后进程状态为 T（已暂停），回来后恢复并响应按键，聊天应用不冻结 |
| 设备（QEMU，内核 6.18，root） | **沙箱自检 13/13 符合预期**：独立 uid、命名空间、Landlock、seccomp、资源上限、cgroup 全部生效；后台冻结用例通过；兼容测试应用在沙箱里跑同一份检查。每个应用用数据目录下的 `tmp/`，没有共享的 `/tmp`（私有 `/tmp` 会盖住放在 `/tmp` 下的服务状态） |

## 8. 未做 / 量产

| 项 | 说明 |
|----|------|
| cgroup 细化 | 按设备内存调整上限；I/O 权重 |
| 流量规则的规模 | 现在每个应用几条线性规则，几十个应用没问题；应用上百时改成按 uid 查表（nftables 的 verdict map） |
| 按应用限额 | 按应用的每月流量上限、超额提醒（整机的流量套餐已有，[19](19-core-apps.md) §1.3）；计数器已在，缺策略和界面 |
| JIT 策略 | 目前所有 JS 引擎都是解释器；以后引入 JIT 时，可写可执行内存只给系统 JS 引擎。自带程序的应用拿得到可写可执行内存，审核要求说明；原生模块加载完以后由 seccomp 封住可执行内存 ✓（[22](22-developer-api.md) §9.3） |
| 后台耗电 | 不被冻结的应用（音频、VoIP、通话）在后台用了多少 CPU，按应用统计、用得多时提醒（Android 的后台耗电提示），设置里列出；✓（[28](28-battery.md) §3） |
| 持续模糊测试 | `fuzz` 工作流（只手动触发，[38](38-contributing.md) §2）每个目标一个作业，跑 `make fuzz` 和 `make fuzz-libfuzzer` 的全部目标（默认每个 10 分钟），语料按目标留在缓存里接着跑，失败的输入作为产物上传、运行失败 ✓（2026-11-19 在 GitHub 上手动跑过一次（`targets` 为 `libfuzzer|cfuzz`，每个 1 分钟），通过；Go 的长时间运行还没在 GitHub 上跑过）；C/C++ 的三个测试程序另有 libFuzzer 版，按覆盖率变异 ✓（§9.2）；给系统服务的 RPC 处理、Shell 的消息分发、kario-jsrt 的其余部分加目标（它的 WebSocket 帧已有，§9.1），第三方解码器的 panic 报给上游（§9.4）：**未实现** |

## 9. 不可信输入与模糊测试

应用之外，系统还读很多别人写的东西：USB 线另一头的电脑、蓝牙对端、NFC 场里的读卡器和卡、任何人发来的短信、商店和更新服务器下来的文件（验签之前的那部分）、拷进来的音乐。读这些的代码都按「输入是恶意的」写：大小先设上限、循环有次数上限、递归有深度上限，失败就是一个错误，不是崩溃、卡死或者把内存吃光。解析放在能放的沙箱进程里（USB 共享的 `phone-usbshared`、各个系统服务），Shell 自己读的（音乐库扫描、装包、OBEX、应用发来的页面）更要守住，因为它一崩整个界面就没了。第三方的解码器（go-mp3、oggvorbis）在损坏的文件上会 panic，我们在调用它们的边界上 `recover`，当成「文件损坏」，不让 phone-audiod 退出。

每个这样的解析器都有 Go 原生的模糊测试（`go test -fuzz`）：不准 panic、不准卡住（异步的部分 10 秒没完就算失败）、输入先限大小，能便宜地检查的不变式也查（解码再编码再解码一致、解析成功的结果有上界、超大的输入被拒绝、库只拿到文件夹里面的名字等）。种子语料来自真实的消息（已有测试里的数据、`audio/testdata` 的文件、代码里按协议拼出来的会话）；跑出来的失败输入留在各包的 `testdata/fuzz/<目标>/` 作回归，`make test` 每次都跑种子和这些回归。C 和 C++ 的解析器（libphoneui 的行读取、telephonyd 的 JSON、kario-jsrt 的 WebSocket 帧）用地址和未定义行为检查器（ASan、UBSan）编译成小程序，由 Go 的模糊测试经套接字或管道喂进去（`tools/cfuzz`）；同样的测试程序也用 clang 编成 libFuzzer 的目标，在进程里按代码的覆盖率变异（`make fuzz-libfuzzer`）。

### 9.1 清单

按暴露程度排列：USB 主机、蓝牙对端、NFC 和短信、应用、网络、文件。

| 输入 | 来自 | 解析代码 | 模糊目标 | 状态 |
|------|------|----------|----------|------|
| MTP/PTP 容器和操作、控制端点的类请求 | 电脑（USB） | `usbshare/mtp` 的 Responder（`phone-usbshared`） | `usbshare/mtp`：`FuzzResponder`、`FuzzControl`、`FuzzObjectInfo` | 25 分钟，无问题 |
| HTTP / WebDAV 请求、多文件上传 | 电脑（USB 网络） | `usbshare.Server`（`phone-usbshared`） | `usbshare`：`FuzzServer` | 16 分钟，无问题 |
| 助手发给 Shell 的请求（被攻破的助手） | `phone-usbshared` | `usbshare.Helper`（Shell） | `usbshare`：`FuzzHelper`、`FuzzCheckName` | 修复 1 处（§9.3）；不变式：库只拿到文件夹里面的名字 |
| OBEX 包：推送、电话本的 GET 和 SETPATH、应用参数 | 蓝牙对端 | `obex.Server`（Shell） | `obex`：`FuzzServe`、`FuzzDecode`、`FuzzParams` | 18 分钟，无问题 |
| OBEX 应答（手机发文件时） | 蓝牙对端 | `obex.Client` | `obex`：`FuzzClient` | 无问题 |
| 旧手机的电话本和短信服务的应答（从旧手机经蓝牙迁移，[36](36-device-switch.md) §8）：OBEX 包、vCard、通话记录、短信列表的 XML、bMessage 和彩信的 MIME | 蓝牙对端（旧手机） | `obex.PhoneBook`、`obex.Messages`、`ParseCall`、`ParseListing`、`ParseBMessage`（Shell） | `obex`：`FuzzOldPhone`、`FuzzParsers` | 5 分钟、6 分钟（4 个进程），无问题；不变式：拉取都结束，拿到的不超过上限，短信正文是合法的 UTF-8 |
| vCard 名片：2.1 / 3.0 / 4.0，折行、quoted-printable、base64、字符集（GB 18030、Big5）、UTF-16 | 蓝牙对端（推送名片）、「文件」里的 `.vcf` | `contacts.Parse`（Shell） | `contacts`：`FuzzParse` | 1 分钟，无问题；不变式：结果在通讯录的上限之内，写出再读回不变 |
| CardDAV 服务器上的名片（手机改了联系人以后打上补丁写回去），服务器的 PROPFIND、REPORT 应答（multistatus） | 账号的服务器（或冒充它的） | `contacts.PatchCard`、`carddav` 读应答（Shell 的同步） | `contacts`：`FuzzPatchCard`；`carddav`：`FuzzMultistatus` | 修复 4 处（2026-11-18，都在 `PatchCard`，修好以前的版本没合进来）：不是 UTF-8 的号码改类型时折行死循环（同步卡住、手机不休眠）；没有 FN 的名片名字随号码变；只有备注的名片清不掉备注；单独的 CR 换行、quoted-printable 软换行和 `Parse` 拆得不一样；之后 `FuzzPatchCard` 5 分钟（7700 万次）无问题，`FuzzMultistatus` 1 分钟无问题；不变式：名片显示的就是要写的联系人时原样不动，打了补丁读回来名字和备注就是要写的 |
| NCI 包、射频激活、命令分段 | NFC 控制器、场里的读卡器 | `hal/nfc` 的 NCI（`phone-nfcd`） | `hal/nfc`：`FuzzNCI`、`FuzzPacket`、`FuzzActivation` | 修复 1 处（§9.3） |
| 卡的应答：BER-TLV、状态字、SELECT | 读卡模式下的卡、读卡器的命令 | `hal/nfc` | `hal/nfc`：`FuzzCard` | 无问题 |
| 短信正文（找验证码） | 任何人 | `programs/smsd` | `programs/smsd`：`FuzzFindCode` | 无问题 |
| 8 位短信里的 WAP 推送（WSP：推送头、内容类型、参数，前面带 UDH 的），里面的彩信通知和送达报告、服务指示（WBXML） | 任何人（伪造的 WAP 推送） | `mms` 的 `ParsePush`、`Decode`、`ParseSI`（`phone-smsd`） | `mms`：`FuzzPush` | 2 分钟（2100 万次），无问题；不变式：正文在输入里面，彩信的部分不超过上限，服务指示的文字有上界 |
| 彩信 PDU：头（编码字符串和字符集、地址、时间、长整数）、多部分正文、SMIL | 彩信中心、通知里的地址上答话的任何服务器 | `mms` 的 `Decode`、`Order`（`phone-smsd`） | `mms`：`FuzzPDU` | 写的时候跑出 5 处，都是编码器读回不一致（空的多部分、编码字符串里的 NUL、控制字符和 127 开头的文字），已改，失败输入留在 `mms/testdata/fuzz`；之后 2 分钟（2300 万次）无问题；不变式：部分和收件人不超过上限，写出再读回类型、收件人、主题、各部分不变 |
| 电话号码（发送人、来电号码、彩信的地址） | 任何人 | `phonenum` 的 `Parse`、`Key`、`Format`（Shell、`phone-smsd`、应用） | `phonenum`：`FuzzParse` | 写的时候跑出 2 处（规整的结果再规整一次会变），已改；之后 1.5 分钟（4200 万次）无问题；不变式：规整的结果读回是它自己，有效号码的 E.164 在任何地区读都一样 |
| 紧急警报的警报区域（WAC：圆、多边形、等待时间） | 有小区广播发射机的任何人 | `ui` 的 `ParseWAC`（`phone-smsd`）、`InArea`（Shell） | `ui`：`FuzzWAC` | 1 分钟（1100 万次），无问题；不变式：点都在地球上，多边形至少三个点，形状不超过 16 个 |
| 相机画面里的二维码：像素，和码里写的东西（模式、字符集、汉字、结构链接、写错的长度） | 任何人举到相机前的图 | `media/qrscan` 的 `Decode`（`phone-mediad`；第三方的 gozxing 在它下面） | `media/qrscan`：`FuzzDecode`（任意的帧；任意数据码字配上正确纠错码画成的码，让码流解析读到） | 修复 1 处（§9.3）；之后 5 分钟（150 万次）无问题；不变式：超界的帧都拒绝、界内的不拒绝，读出的文字非空、合法 UTF-8、至多 8 KB，gozxing 的 panic 算失败，一帧不超过 2 秒 |
| 定位接收机的 NMEA 0183 语句（GGA、RMC、GLL、GSA、GSV、VTG） | GNSS 接收机、串口、调制解调器（经 ModemManager） | `location` 的 `Parse` 和 `Decoder`（Shell） | `location`：`FuzzDecode` | 5 分钟，无问题；不变式：交出的位置在地球上、数值有限且在合理范围，解析通过的语句重新组帧不变 |
| WireGuard 配置文件（wg-quick 的格式） | 电脑（经 USB 拷进「文件」）、下载、别人发来的文件 | `vpn` 的 `Parse`（Shell） | `vpn`：`FuzzParse` | 3 分钟（1600 万次），无问题；不变式：解析通过的配置写成 systemd-networkd 的文件后，每一行都是已知的段，或已知的键加上只含该有字符的值（文件里的文字写不出新的段或键） |
| 连接上的 JSON 行 | 应用、服务的客户端 | `wire.Conn` | `wire`：`FuzzRecv` | 无问题 |
| 页面：字段、动作、画布的上限 | 应用 | `shell` 的 `limitContent` | `shell`：`FuzzScreen` | 无问题 |
| 文档节点：Markdown、TeX 公式、Mermaid 图 | 应用 | `shell` 的 `renderDoc`、`texToUnicode`、`renderMermaid` | `shell`：`FuzzDoc`、`FuzzMermaid` | 修复 2 处（§9.3） |
| 画布的图形 | 应用 | `shell` 的 `paintCanvas` | `shell`：`FuzzCanvas` | 无问题 |
| 清单：译文、NFC 卡、隐私标签、版本号 | 开发者（商店审核） | `appstore`、`i18n`、`ui` 的检查 | `appstore`：`FuzzManifest` | 修复 1 处（§9.3） |
| 小程序的 `app.json`、翻译表，它的运行时发给宿主的页面 | 小程序商店 | `apps/miniapp` | `apps/miniapp`：`FuzzProgram` | 无问题 |
| 短信备份文件（安卓「SMS Backup & Restore」的 XML）：元素、属性、引用（含代理对）、注释、CDATA、DOCTYPE | 「文件」里的文件（从安卓手机拷来的、下载的、分享来的） | `smsbackup` 的 `Parse`（Shell，`sms.restore`） | `smsbackup`：`FuzzParse` | 1.5 分钟（约 41 万次），无问题；不变式：结果在上限之内（条数、文字、主题、号码、群聊成员）、文字是 UTF-8、写出再读回不变 |
| iCalendar 文件（`.ics`）：VEVENT、时区、重复规则、提醒，折行和转义 | 「文件」里的文件、分享来的文件、换机传来的日历 | 日历应用的 `parseICS`（TypeScript，`apps/calendar/src/ics.ts`） | `apps/calendar`：`FuzzICS`（Go 的模糊测试把输入经管道交给 Node.js 里跑的解析器，esbuild 现打包；没有 Node.js 时跳过） | 1 分钟（110 万次），无问题；不变式：不抛异常、5 秒内答完、结果在日历的上限之内（2000 个事件、标题 200 字、备注 2000 字、4 个提醒、200 个 EXDATE），写出再读回不变 |
| Shell 发给 C/C++ 应用的行 | Shell | `sdk/c` 的 libphoneui | `tools/cfuzz`：`FuzzPhoneUI`（ASan） | 修复 1 处 |
| `.phoneapp` 包和增量包（验签前的 tar 和签名文件） | 商店下载、侧载 | `pkg` | `pkg`：`FuzzOpen` | 修复 1 处 |
| 商店清单、下架清单、开发者证件号 | 商店 | `pkg` | `pkg`：`FuzzSignedLists`、`FuzzIDs` | 无问题 |
| `holidays.json` | 更新服务器（先验签） | `ota` | `ota`：`FuzzHolidays` | 无问题 |
| 增量包的头和清单 | 更新包 | `ota/delta` | `ota/delta`：`FuzzManifest` | 无问题 |
| bsdiff 补丁 | 更新包 | `ota/delta/bsdiff` | `ota/delta/bsdiff`：`FuzzApply`、`FuzzMake` | 修复 1 处 |
| xz 数据 | 更新包 | `ota/delta/xz` | `ota/delta/xz`：`FuzzDecode` | 修复 1 处 |
| ext4 镜像、GPT 磁盘镜像 | 源槽、状态分区、构建机上的镜像 | `ota/delta/ext4`、`ota/delta` 的 `LoadSlot` | `ota/delta/ext4`：`FuzzImage`；`ota/delta`：`FuzzSlot` | 修复 2 处 |
| verity 描述 | 槽的分区 | `ota/verity` | `ota/verity`：`FuzzDescriptor` | 无问题 |
| 加密的推送数据、推送令牌 | 推送网关、应用服务器 | `push` | `push`：`FuzzSealed`、`FuzzToken` | 无问题 |
| 换机协议的请求：信封，hello、条目、文件报告的 JSON，文件块 | 热点或 Wi-Fi 上的任何设备、配对的旧手机 | `switchrecv`（Shell，[36](36-device-switch.md)） | `switchrecv`：`FuzzEnvelope` | 5 分钟（41 万次），无问题；不变式：不是会话密钥加密的正文都得到明文的 401；配对的应用加密的任何明文都得到下一个下行计数加密的回复，是 JSON 对象，不是 200 时带 `code`；加密再解开不变 |
| WebSocket 帧：长度、分片、控制帧、关闭码、文本的 UTF-8 | 应用连的任何服务器（网络上） | kario-jsrt 的 `jsrt/src/wsframe.c`（应用自己的进程里，[22](22-developer-api.md) §9.4） | `tools/cfuzz`：`FuzzWebSocketFrames`（ASan、UBSan） | 5 分钟（13 万次），解析器无问题；修复测试程序自己 1 处（空输入时漏放内存）；不变式：消息不超过上限、文本是 UTF-8、关闭码合法、出错或关闭后不再读、消息照服务器的样子重新组帧再读一样 |
| 设备证明、签名凭证、`root.pub`、SM2 公钥和签名 | 服务器端核对、配发 | `keystore`、`gm` | `keystore`：`FuzzVerify`、`FuzzRoots`；`gm`：`FuzzSM2` | 无问题 |
| telephonyd 控制套接字上的请求 | Shell、smsd | `telephonyd/src/json.cpp`（C++） | `tools/cfuzz`：`FuzzTelephonydJSON`（ASan） | 修复 1 处 |
| 音乐文件的头和标签：WAV、AIFF、FLAC、M4A、APE、WavPack、MP3、Ogg、DSF、DFF、SACD 镜像，ID3、APE、Vorbis 注释，cue | 电脑、蓝牙、下载 | `audio/musicfile`、`audio/tags`、`audio/cue`、`audio/dsd`（Shell 扫描音乐库） | `audio/musicfile`：`FuzzProbe`；`audio/tags`：`FuzzID3v2`、`FuzzVorbisComment`、`FuzzTail`；`audio/cue`：`FuzzParse` | 修复 5 处，其中 3 处会让 Shell 崩溃 |
| 解码播放：FLAC、DSD、SACD、MP3、Ogg Vorbis 和 Opus、WAV | 同上 | `audio/flac`、`audio/dsd`、`audio`（`phone-audiod`） | `audio/flac`：`FuzzDecoder`；`audio/dsd`：`FuzzFile`；`audio`：`FuzzPlay` | 修复 6 处 |
| 语音消息 | 聊天对端 | `audio/voice` | `audio/voice`：`FuzzDecode` | 修复 1 处 |
| 旧编码的文字（GBK、UTF-16、Shift-JIS、Big5…） | 同音乐文件 | `audio/internal/textenc` | `audio/internal/textenc`：`FuzzDecode` | 修复 1 处 |

### 9.2 怎么跑

`make test` 跑所有目标的种子和回归（C 和 C++ 的三个目标在没编译检查器版程序时跳过）。`make fuzz` 一个一个地跑每个目标，每个 `FUZZTIME`（默认 30 秒）、`FUZZPAR` 个进程（默认 8），先把 C/C++ 的检查器版程序编译到 `FUZZBIN`（默认 `/tmp/fuzz/bin`）。`make fuzz-libfuzzer` 用 clang（`LF_CC`、`LF_CXX`；至少 clang 19：telephonyd 的 JSON 用到的 `std::expected`，libstdc++ 14 只对 clang 19 起声明）把这三个测试程序编成 libFuzzer 的目标（加 `-DKARIO_LIBFUZZER`，`-fsanitize=fuzzer,address,undefined`），各跑 `FUZZTIME`（秒、分或小时），从 `tools/cfuzz/testdata/libfuzzer/<目标>/` 的种子和 `FUZZCORPUS`（默认 `/tmp/fuzz/corpus`）里攒下的语料接着变异，出错的输入写进 `FUZZOUT`（默认 `/tmp/fuzz/out`）；内存泄漏只在 WebSocket 帧里算错，和 Go 那一边一样。`make fuzz-list` 列出两种目标（`ONLY` 照样起作用）：

```
make fuzz                                   # 68 个目标，每个 30 秒
make fuzz ONLY='usbshare|obex' FUZZTIME=10m # 只跑名字匹配的，每个 10 分钟
make fuzz-libfuzzer FUZZTIME=10m            # 三个 libFuzzer 目标，每个 10 分钟
go test ./usbshare/mtp -run '^$' -fuzz '^FuzzResponder$' -fuzztime 5m
```

在 GitHub 上：Actions › fuzz › Run workflow，或 `gh workflow run fuzz -f targets='usbshare|obex' -f minutes=30`。`targets` 是对 `make fuzz-list` 的名字的正则（空着是全部），`minutes` 是每个目标的分钟数（默认 10，至多 300）。每个目标一个作业：Go 的目标的语料在 Go 的构建缓存里（`$(go env GOCACHE)/fuzz/<包>/<目标>`），libFuzzer 的在 `FUZZCORPUS` 里，按目标存进 Actions 的缓存，下次从最新的一份接着跑。失败的输入（Go 写进包的 `testdata/fuzz/<目标>/`，修好以后提交成回归；libFuzzer 的是原始字节，`/tmp/fuzz/bin/lf-<目标> <文件>` 重现）作为产物 `crashers-<目标>` 上传，这次运行失败。最后和 `platform` 一样只留每个工作流最新的 3 次运行。开发机上 `make fuzz`、`make fuzz-libfuzzer` 的目标都跑过（2026-11-18，各 5–10 秒）；工作流本身 actionlint 通过，2026-11-19 在 GitHub 上手动跑过一次（`targets` 为 `libfuzzer|cfuzz`，每个 1 分钟），通过。

### 9.3 找到并修好的问题

都是模糊测试、写目标时读代码或代码扫描（CodeQL）发现的；每处都有回归（单元测试或 `testdata/fuzz` 里的输入），先确认改之前失败。

| 位置 | 问题 | 修复 |
|------|------|------|
| `audio/musicfile/wav.go` | `LIST/INFO` 的最后一个子块长度是奇数时切片越界：**Shell 扫描音乐库时崩溃** | 截在块尾 |
| `audio/musicfile/mp3.go` | 只有一个帧头的文件，读 Xing/VBRI 头时切片越界：**Shell 崩溃** | 偏移不超过已读的长度 |
| `audio/musicfile/apewv.go` | WavPack 块长小于 32 时按负长度分配：**Shell 崩溃** | 不小于 0 |
| `audio/tags/ape.go` | APE 标签和文件一样大且带头时，标签起点是负数 | 不小于 0 |
| `audio/tags/tags.go`、`audio/internal/textenc`、`audio/dsd/dff.go` | 标签里不是 UTF-8 的文字原样传出（ID3 的 UTF-8、Vorbis、APE、UTF-8 BOM 后、DFF 的标题），和包的约定不符 | 在 `Tags.Set` 和 BOM 分支换成合法 UTF-8；DFF 标题走 `textenc` |
| `audio/dsd/dff.go` | DST 帧的块长是负数时，`Next` 和 `SeekFrame` 原地打转：**phone-audiod 卡死**；DSTI 索引按声称的大小先分配 | 块长不合理就报错；索引至多 24 小时的帧 |
| `audio/dsd/sacd.go` | 跳转时的回退量是 `uint32`，乘到溢出变成 0 后死循环：**卡死** | 用 `int64` |
| `audio/dsd/dsf.go` | 头里声称的采样比文件多时，读到文件尾以后一直重复最后一块（播不完） | 读不到就结束 |
| `audio/sources.go` | WAV 的块长从头里来，读之前先分配（最多 4 GB）：phone-audiod 内存耗尽 | 边读边长 |
| `audio/vorbisfile`、`audio/mp3.go` | 第三方解码器（oggvorbis、go-mp3）在损坏的文件上 panic：**phone-audiod 退出** | 在调用边界 `recover`，当成损坏的文件 |
| `audio/voice` | 一两个字节的 Opus 包能解出 120 毫秒：小文件解出几个 GB | 一条至多 10 分钟（`MaxSamples`） |
| `hal/nfc/nci.go` | 读卡器一直发「还有下一段」，重组命令的缓冲没有上限 | 超过扩展 APDU 的最大长度（`MaxAPDU`）就丢掉整条命令 |
| `pkg/pkg.go`、`pkg/delta.go` | PAX 头里的稀疏文件：3 KB 的条目解出 64 MB 的零，在验签之前，1000 个条目就要 64 GB：**装包时 Shell 内存耗尽** | 解出来的总字节数也按 `MaxSize` 算 |
| `appstore/store.go` | 商店后台拿开发者签名的版本号当包的文件名（`packages/<ID>/<版本>.phoneapp`），却不查它的样子：已核验的开发者用带 `../` 的版本号能把包写到 `packages/` 外面，或者盖掉别的开发者的包 | 版本号只能是点分数字（至多 8 段、每段至多 9 位，`newer` 比较的就是这个）；包的路径只由合规的 ID 和版本号拼出（`packagePath`） |
| `ota/delta/xz` | 期望大小是负数时 `make` panic；按调用方给的大小预先分配 | 拒绝负数；缓冲按解出的数据增长 |
| `ota/delta/bsdiff` | 头里的输出大小是负数也接受（「成功」做出 0 字节；负的流长度再传给 xz 会 panic） | `Size` 拒绝负数 |
| `ota/delta/ext4` | 组描述符按超级块声称的组数先分配（可达 TB 级）；每个索引项都指向下一层同一块的 extent 树要读 84⁵ 块，间接块同理：卡死、内存耗尽 | 先确认描述符的最后一个字节在镜像里再分配，描述符至多 1024 字节；每个 inode 至多读 65536 个树块或间接块、列 2²⁰ 个 extent，长度为 0 的 extent 不算 |
| `ota/delta/image.go` | GPT 表项起始 LBA 乘 512 回绕后切片越界 | 先限 LBA |
| `media/qrscan`（gozxing 的 `FinderPatternFinder.Find`） | 定位图案的候选全部留下，再三个一组地全部试一遍（立方级）：一张 163×174 的斜纹图解了 7.6 秒，640×480 的 60 秒还没解完，**举到相机前就能让 mediad 的扫码卡住、占满一个核** | 自己的查找循环（同样的扫描，用 gozxing 公开的函数），候选超过 64 个就当没有码：0.3 ms（`TestManyFinderMarks`） |
| `sdk/c/phoneui.c` | 跳过 JSON 值的递归没有深度上限：嵌套很深的一行就栈溢出（ASan） | 至多 10000 层，和 Shell 的 JSON 解码器一样 |
| `telephonyd/src/json.cpp` | 同样的递归：一行 1 MB 的 `[` 让 telephonyd 栈溢出 | 至多 512 层 |
| `telephonyd/test/telephonyd_test.cpp` | 测试自己拿着临时对象里的指针（释放后使用），ASan 下验证改动时发现 | 先存下状态 |
| `shell/doc.go` | 嵌套的引用和列表每层都加缩进：41 KB 的文档嵌套 1000 层，排一次版就分配 1.3 GB，而页面每帧都要重排：**应用能让 Shell 内存耗尽** | 缩进至多到宽度的一半，更深的和最后一层对齐 |
| `shell/doc.go` | 图画不出来时的说明行、代码块的语言名不折行，窄屏上被截掉 | 按宽度折行 |
| `shell/music.go` | 音乐库里有指向库外的符号链接时，电脑经 USB 在链接下建文件夹、拷文件，Shell 先在库外建好文件夹，才查出路径出了库而拒绝 | 电脑一侧的列出、读、建文件夹、拷入都经 `os.Root` 打开音乐库：名字解析不到库外，经链接也不行 |

### 9.4 没有覆盖的

- kario-jsrt（C，QuickJS 上的 TypeScript 运行时）和原生模块：只有服务器发来的 WebSocket 帧有目标（§9.1）；fetch 的回应交给 libcurl，随能力回复的文件只按普通文件读、有大小上限（[22](22-developer-api.md) §9.2）；QuickJS、goja、libcurl 本身的健壮性靠上游。
- Shell 的应用消息分发和能力处理（`appMsg`、broker）：另一条工作线在做中间件加固，这里只测了页面上限 `limitContent`；系统服务（`programs/*`）的 RPC 处理是状态机，要带上服务的状态才测得有意义，没做；它们的 JSON 解码是标准库。
- 只跑在电脑一侧或只读本机数据的：`usbshare/mtp` 的 usbfs 描述符解析、MTP 的 Initiator、输入法词库、系统自带的翻译表。
- 标准库和第三方的解码器本身（PNG、JPEG、goldmark、goja、go-mp3、oggvorbis、opus-go）：只经过我们的调用路径测。mediad 给的视频预览图和相机照片一样，先限 8 MB、读文件头查格式和边长（≤ 4096）再解码（`readPhoto`，`TestReadPhotoBounds`）：mediad 是沙箱里的服务，被攻破时也不能用一张声称几十亿像素的图让 Shell 分配内存。
- telephonyd 读基带（AT 命令、ModemManager）的部分、mediad 的视频、网页视图（WPE）、两个 Rust 渲染器读的渲染协议：没做（Rust 要引入 cargo-fuzz，不加新依赖）。
- C/C++ 的覆盖率：Go 的模糊测试看不到 C 代码里的分支，`tools/cfuzz` 的三个目标是盲目变异；它们的 libFuzzer 版按覆盖率走（§9.2），但 libFuzzer 的崩溃要手工换成 Go 的回归格式（`go test fuzz v1`）才进 `make test`。
