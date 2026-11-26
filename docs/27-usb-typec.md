# USB-C：充电、键盘鼠标、连接电脑

> 状态：**原型 ✓（QEMU）**。键盘鼠标插拔、连接电脑（信任 → 密码 → 拷文件 → 拔线停止）在模拟器上端到端通过（dummy_hcd 让一台虚拟机同时是手机和电脑）；文件共享搬进单独的沙箱进程（§3.2）以后在模拟器上也通过了（2026-11-14）；PD 快充的识别有单元测试，**未上真机**（要等骁龙板子，R16）。文件传输（MTP，§3.3）有单元测试，开发机自测 ✓，模拟器 ✓（经 dummy_hcd，自己的发起端和 libmtp 的工具都试过），真的 Windows、macOS、Linux 电脑**未上真机**。开发者通道对着系统的工具（§4、§4.1：推拉文件、截屏、按键打字、系统日志、端口转发、重启、旁加载系统更新、问题报告）开发机上单元测试和自测通过，经真的 SSH 的模拟器用例**还没跑过**，**未上真机**；非 root 的开发者账号**未实现**（§4.2）。手机上的硬件层见 [11](11-platform-architecture.md) §「那 hal/ 是什么」。

## 0. 结论

- 一个 USB-C 口做三件事：充电（PD 协商由 Type-C 口控制器的内核驱动做，我们只读结果）、接键盘鼠标（手机是 USB 主机）、连电脑（手机是 USB 设备，经 USB 网卡给电脑共享文件）。
- 连电脑学 iPhone：电脑插上只能充电，**用户在手机上点「信任」以后**电脑才拿得到东西；每次连接一个密码，拔线就结束，信任不保存。
- 不装驱动：手机对电脑是一块 CDC-NCM 网卡，macOS、Linux、Windows 11 都自带驱动；电脑上用浏览器或「连接网络驱动器」（WebDAV）打开，不需要我们的电脑端软件。也可以在设置 › USB 改成文件传输（MTP，§3.3），电脑的文件管理器把手机认成一台设备。

## 1. 充电：USB PD

| 部分 | 做法 |
|------|------|
| 协商 | Type-C 口控制器（骁龙 662 上是 PMIC 的 Type-C 模块）的内核驱动和充电器协商电压电流（PD、PPS），手机软件不参与 |
| 读结果 | `hal/power` 读电源类（`/sys/class/power_supply`）：USB 供电的 `usb_type` 里带方括号的就是当前类型，`[PD]`、`[PD_PPS]` 是 PD 快充，`[DCP]` 等是专用充电器；老驱动直接报类型 `USB_PD` |
| 提示 | 插上电源时屏幕亮起，提示「正在快速充电」或「正在充电」（[28](28-battery.md)）；状态栏照旧是 ⚡ 加电量 |
| 手机吸多少电 | 接电脑时手机作为 USB 设备向电脑要 500 mA（USB 2 的上限，`hal/usbgadget` 的 `MaxPower`）；充电器给的电流由口控制器另外协商，和这个无关 |

真机上要确认的（**未实现**）：主线内核里骁龙 662（PMI632）的 Type-C / PD 驱动支持到哪一步，充电曲线、温度保护由充电驱动做。

## 2. 键盘和鼠标：USB HID

- 内核的 USB HID 驱动把键盘鼠标变成 evdev 设备（内核配置 `phone-usb.cfg`）。键盘和机身键盘一样走渲染器 → Shell 的按键路线（[11](11-platform-architecture.md)），全键盘的字母直接打字。
- **开机后插上的也认**：渲染器降权后打不开 `/dev/input`，由降权前分出的输入助手打开新设备、把描述符交给它（[23](23-renderer-protocol.md) §5）。接上、拔掉时渲染器发 `input` 消息，Shell 记日志。
- **鼠标**：这是按键优先的系统，鼠标首先当按键用：滚轮上下移动焦点（横向滚轮左右），右键返回，中键选项。鼠标一动就出现指针，指着某一项点一下，焦点移过去并选中它（同 OK）；点在软键栏上是对应的软键；打字时指针隐去，没指针时左键就是 OK。渲染器把指针位置换成节点（`tap`，[23](23-renderer-protocol.md) §3），Shell 只认它自己发出的那一帧里的节点。

## 3. 连接电脑：像 iPhone 一样先信任

| 步骤 | 手机上 | 电脑上 |
|------|--------|--------|
| 开机 | 手机一直是一块 USB 网卡（configfs 里的 CDC-NCM，`hal/usbgadget`），MAC 地址由序列号算出，电脑每次看到的是同一台设备；但手机这一侧**没有地址** | — |
| 插线 | 设备控制器的状态变成 `configured`（电脑配置了它；充电器不会），屏幕亮起，问「信任这台电脑吗？」 | 系统认出一块 USB 网卡，拿不到地址，什么也访问不了 |
| 信任 | 给链路配上 `10.71.0.1/24` 和 DHCP 服务（systemd-networkd 的运行时配置，重启就没了），起文件共享，通知里显示地址和 8 位一次性密码 | DHCP 拿到地址；浏览器打开 `http://10.71.0.1`，或者把它当网络驱动器（WebDAV）连接，输入密码 |
| 拷文件 | `Photos`、`Videos`、`Music`、`Files` 四个文件夹（名字固定是英文），都能读能加；加进来的经系统自己的库（相册、音乐库、文件库）保存，和在手机上保存的一样出现在图片、音乐和「文件」里（§3.1） | 下载、上传，「音乐」里能建文件夹、拷整个文件夹 |
| 拔线 | 停止共享，撤掉地址和 DHCP，忘掉信任，提示「已停止与电脑共享」 | 连接断开 |

- 密码比较用常量时间，错 10 次这次连接就锁住，要在手机上重新共享。
- USB 总线复位不算拔线（Windows 出错后会复位；libmtp 放开一台它当成安卓的手机时也复位，DeviceInfo 里有 `android.com` 它就这么看）：设备经过 default、addressed 再回到 configured，共享和信任都留着，还在问「信任这台电脑吗？」时问题也留着（电脑第一次配好手机后马上复位一次很常见，模拟器上就是这样：以前问题被撤掉又弹出，用户这时按的确认落了空）；15 秒内没回到 configured 才当拔线。
- 防火墙给 USB 链路单独一条链（`netfw.Link`，在按应用的规则之前）：信任前什么都进不来；信任后只放行 DHCP 和文件共享（80 端口），开发者模式下再放行调试终端（22 端口）；插着线打开或关掉开发者模式，这条链当场跟着改，不等拔线：关掉时 22 马上不接新的连接，已经开着的连接排空后再关（下一条）。所以手机上别的服务即使监听在所有网卡上，数据线也连不到。设备上的自测核对 80 通、22 不通，打开开发者模式后 22 通，插着线关掉后 22 又不通（新的连接进不来；单元测试 ✓，QEMU ✓）。
- 关掉开发者模式时，已经开着的 SSH 连接排空，手机不重置它们：Shell 一秒内结束开发者通道上的请求，会话的结束、电脑对手机数据的确认、电脑关掉转发通道的消息都要能传到；以前 22 当场对它们也关上，会话上还有数据在走（转发的连接、跟着看的日志）时，手机没被确认的数据发不出去，会话的结束到不了电脑，电脑那头的 ssh 要等 TCP 放弃（约 15 分钟）才退出。现在这条链对 22 换成一条只放行已有连接的规则（`netfw.Link` 的 `Drain`：conntrack 两个方向都见过的连接的包，`ct state established,related`，新的 SYN 照样丢），带一个计数器；Shell 每半秒读一次（`LinkDrained`），3 秒没有包进来、或者最多 30 秒以后去掉这条规则，22 对它们也关上。时长是 `shell/usbfw.go` 的常量 `usbDrainIdle`、`usbDrainMax`，`drainLimits` 先看 `usbState` 的 `drainIdle`、`drainMax`（以后的设置填它们）。按连接状态匹配要 conntrack 和 `nft_ct`，按应用的规则设、读连接标记本来就要它们（镜像装了 `nft_ct`）；Shell 的内核探测（`shell/kernel.go`）不查 netfilter，内核不接受这条规则时 22 当场关上（以前的做法，日志里一行）；连接若是在 conntrack 开始跟踪以前建的，手机发出下一个包时被认出来（`nf_conntrack_tcp_loose`），之后电脑的包照样放行。限制：SSH 会话里转发的连接还开着时会话不结束（dbclient、OpenSSH 都等转发的通道关掉），电脑一直经它送数据的话，30 秒后照样被切断，电脑的 ssh 还是要等 TCP 放弃；`kario device shell` 的交互终端不经 `phone-dev`，Shell 不结束它，安静 3 秒后 22 对它关上，手机上的 shell 留着、电脑那头挂着（**未处理**）。另外，内核建不起按应用的那张表时（没有具名计数器，Linux 4.10 以前，[17](17-app-sandbox.md) §1）Shell 没有防火墙可用，数据线的这条链也不建，信任后 22 不管开发者模式都连得上（**未处理**，**未上真机**）。
- 文件共享只听 USB 链路上的地址，从 Wi-Fi 连不到；没信任时链路上连 IPv6 链路本地地址都没有，手机上的任何服务从数据线都到不了。这靠 Shell 开机时写的一个运行时 systemd-networkd 配置：信任之前、拔线以后 networkd 不管这条链路，系统里有匹配所有网卡的 `.network` 时也一样（QEMU 内核参数里的 `ip=dhcp` 会让 systemd-network-generator 写一个）；模拟器上电脑那一端同样不让 networkd 碰，留给自测在自己的网络命名空间里打开。
- 不信任：只充电。
- 共享的服务器（`usbshare/`）在单独的沙箱进程 `phone-usbshared` 里，文件仍经 Shell 的相册、音乐库和文件库读写，所以文件的索引和权限规则不变（§3.2）。
- 设置 › USB 选了文件传输（MTP）时，信任后手机重新枚举成一台 MTP 设备，在 Windows 资源管理器里直接显示成手机；规矩和这里一样，只是没有密码（§3.3）。苹果没用 MTP，Windows 上要装「Apple 设备」应用。

### 3.1 文件夹

电脑看到的是按人拷东西的习惯分的几个文件夹，名字固定是英文 `Photos`、`Videos`、`Music`、`Files`，不随手机的语言变（和安卓的 `DCIM`、`Music` 一样：电脑上的脚本、同步工具按名字找文件夹，名字一变就找不到了）；在网页里、挂成网络驱动器（WebDAV）时和网址里都是这个名字（小写的 `photos` 等也认）。网页上的其他文字跟手机的语言。

| 文件夹 | 里面是 | 电脑能加的 | 一个文件最大 | 加进来以后 |
|--------|--------|------------|--------------|------------|
| Photos | 系统相册里的照片 | JPEG、PNG | 8 MB（边长 4096 以内） | 和相机拍的一样进相册，生成缩略图 |
| Videos | 系统相册里的视频 | WebM（VP8 / VP9） | 4 GB | 进相册，由 mediad 读出尺寸、时长和封面帧 |
| Music | 音乐库（`media/music`），**文件夹原样保留**：一张专辑的文件夹拷进来还是一个文件夹 | 音乐文件（FLAC、WAV、AIFF、ALAC、APE、WavPack、MP3、Ogg、DSF、DFF）、CUE 分轨表、SACD 镜像（`.iso`），以及专辑里常带的封面图、歌词（`.lrc`）、抓轨日志、播放列表 | 9 GB（双层 SACD 镜像） | 下次打开音乐（或回到它）就在曲库里；CUE 分成曲目，整轨文件不单列（[20](20-audio.md) §9） |
| Files | 用户的文件（文档、附件） | 任何文件 | 16 MB | 进「文件」，和在手机上保存的一样 |

- 重名时改成「名字 (2)」，不覆盖手机上已有的文件；电脑不能删除或改动手机上的文件。
- `Music` 里能建文件夹（WebDAV 的 `MKCOL`），网页上能选整个文件夹上传，最多 8 层；名字不能以点开头，不能有控制字符或 `..`。大文件边收边写进同一个文件夹里的临时文件，收完再改名，不经过 Shell 的事件循环。
- 录音、下载没有单独的文件夹：手机上还没有收它们的系统库（录音在各应用自己的数据目录里，浏览器下载的进「文件」）。

### 3.2 共享服务在单独的沙箱进程里

电脑发来的一切（HTTP、WebDAV、浏览器上传的 multipart）都由 `phone-usbshared` 解析。它是用户信任电脑后 Shell 启动的单独进程，就像安卓把 MTP 放在 MediaProvider 进程里、不放在 system_server 里：这段代码里有漏洞，插上来的任何电脑都碰得到，被利用了拿到的也只是这个什么都碰不到的进程，而不是掌管一切的 Shell。代码：`programs/usbshared`（助手）、`usbshare/remote.go`（两端的协议）、`shell/usbhelper.go`（启动、重启、自测）。

| 方面 | Shell | phone-usbshared |
|------|-------|-----------------|
| 网络 | 建监听套接字（链路地址的 80 端口要特权），共享期间一直留着，助手重启时电脑的连接在队列里等 | 只继承这个监听套接字（fd 3），在上面 accept；设备上在空网络命名空间里；自己建不了套接字，也不能 bind、connect、listen |
| 文件 | 相册、音乐库、文件库（`shell/usb.go` 的 `usbLibrary`），加进来的照旧经系统的库保存 | 一个路径都打不开：下载时 Shell 打开文件，只读 fd 经 `SCM_RIGHTS` 交过来；上传时 Shell 给一根管道的写端，库从读端边收边存 |
| 和对方说话 | socketpair 的一端（`usbshare.Helper`），助手的每个请求都当不可信输入 | 另一端（fd 4，`usbshare.Remote`）：列文件夹、列文件、建文件夹、打开、保存、日志；Shell 发给它的只有自测的探测 |
| 身份 | root | 设备上：自己的 uid（`uids.json` 里的 `system/usbshared`），新的 mount、pid、ipc、uts 命名空间和空的 net 命名空间，没有能力，no_new_privs |
| 资源 | 在委派给它的 cgroup 下给助手建组 `app.system.usbshared`（开了应用作用域时是 systemd 作用域），直接在组里启动它（`CLONE_INTO_CGROUP`）；助手退出后看 `memory.events` 记下是不是超了内存，结束、删掉这个组；没有 cgroup 委派的机器（开发机）上和应用一样留在 Shell 的组里 | 自己的组：内存 64 MB、不用交换、进程和线程 64 个，Go 的软上限 `GOMEMLIMIT=48MiB`；文件经管道流过，助手自己只要几 MB。超了上限内核只结束助手，Shell 照常重启它（算进 3 次） |
| 内核层 | — | 经 `phone-sandbox` 启动（策略 `helper`）：Landlock 只许读、执行它自己的程序（开发版动态链接时加上系统库目录），TCP 的 bind、connect 也禁止；seccomp 是应用那份；拿到 fd 以后自己再加一层（`sandbox.DescriptorsOnly`，层名 `fd-only`）：exec、新建套接字和一切按路径的文件操作（打开、新建、删除、改名、链接、改权限 / 属主 / 时间 / 扩展属性、inotify）都返回 EPERM，不需要 root，也不需要 Landlock |

Shell 对助手请求的检查：

- 文件夹只认自己列出的四个（按 ID）。
- 名字：合法的 UTF-8，没有控制字符，每一段不空、不是 `.` 或 `..`；`/`（和 Windows 的 `\`）只在「音乐」里当文件夹分隔符；音乐库自己再查一遍（不以点开头、最多 8 层），再经 `os.Root` 打开自己：名字解析不到库外，库里指向库外的链接也不行。不合格的建文件夹、上传得到「手机上的文件或文件夹不能用这个名字」。
- 大小：每个文件夹的上限在 Shell 这边再数一遍。上传读到管道的头时，助手要说这份「完整」，字节数还要和管道里来的一样，库才保存，不然丢掉；超过上限的电脑照旧看到 413。
- 交出去的只能是只读打开的普通文件。
- 助手发来的 fd 一律关掉；同时处理的请求最多 256 个、上传 32 个；日志每秒最多 100 行，每行最多 512 字节，控制字符换成空格。助手的日志行（「电脑拷走了……」「电脑加了……」）照旧进 Shell 的日志，「加了几个文件」由 Shell 自己在保存成功时数。

生命周期：信任后启动；拔线、Shell 退出（父进程退出信号，socketpair 断开）时结束，一秒内没走就被杀掉；共享期间崩溃了由 Shell 重启，一次共享里最多重启 3 次，再崩溃就停止共享（提示「已停止与电脑共享」）。密码错误的计数在助手里，重启后从零数起，所以重启次数有上限。语言跟着 Shell：每次列文件夹时 Shell 告诉它当前的语言。

开发机上 Shell 不是 root，没有独立 uid 和命名空间，开发机的内核（4.18）也没有 Landlock：生效的是 rlimit、seccomp 和 `fd-only`。自测核对的是：助手按路径打开、新建文件、建 IPv4 和 Unix 套接字、exec、ptrace 都得到 EPERM。设备上（QEMU，root）还要求独立 uid、没有能力、命名空间和 Landlock 都生效，这部分和经真 USB 链路的用例在模拟器上通过了（2026-11-14）。

| 环境 | 结果 |
|------|------|
| 单元测试（`usbshare`、`sandbox`、`programs/usbshared`） | ✓（§5） |
| 开发机自测（非 root，内核 4.18） | ✓：原有的 USB 用例（「USB 连接电脑……」「音乐：电脑经 USB……」；防火墙那个要 netfw，开发机上跳过）一步没改照样通过，新用例「USB 文件共享在单独的沙箱进程里……」通过 |
| 模拟器（QEMU，root，dummy_hcd） | ✓：上面这些 USB 用例都通过，助手在自己的 cgroup 里，带内存和进程上限 |

### 3.3 文件传输（MTP）

设置 › USB 里选信任的电脑拿到什么：网络共享（默认）或文件传输（MTP）。MTP（媒体传输协议，建在 PTP 的容器格式上）是安卓手机连电脑的方式：Windows 资源管理器、Linux 的文件管理器（gvfs、KDE，经 libmtp）直接把手机显示成一台设备；macOS 自己不带，要装 Android File Transfer 或 OpenMTP。代码：`usbshare/mtp`（协议，手机一端和电脑一端）、`hal/usbgadget/ffs.go`（FunctionFS 函数）、`shell/usbmtp.go`（切换、自测）、`programs/usbshared/mtp.go`（助手）、`apps/settings/usb.go`（设置页）。

默认还是网络共享，理由就是 §0：电脑上什么都不用装（macOS、Linux、Windows 11 自带 NCM 驱动和浏览器），每次连接有一个只在手机上显示的密码，开发者模式的调试终端（SSH）也只走它；MTP 在 Mac 上要第三方软件，也没有密码。MTP 换来的是 Windows 资源管理器里的拖放，以及没有 NCM 驱动的 Windows 11 以前的版本。

| 步骤 | 手机上 | 电脑上 |
|------|--------|--------|
| 插线 | 和 §3 一样：先是一块没有地址的 USB 网卡，屏幕问「信任这台电脑吗？」。设了锁屏 PIN 码、手机锁着时先不问，提示先解锁，解锁后再问：MTP 没有密码，信任是唯一的门槛（安卓也是解锁后才给 MTP 存储） | 一块拿不到地址的网卡 |
| 信任 | Shell（root）在 configfs 里建 FunctionFS 函数 `ffs.mtp`，挂载它，往 ep0 写描述符和字符串，打开三个端点；再把配置里的网卡函数换成它、重新绑定控制器（产品号 `0x0105`，打开微软 OS 描述符），起沙箱助手，通知「文件传输已开启」 | 网卡没了，出来一台 MTP 设备「Kario phone」，四个存储：Photos、Videos、Music、Files |
| 拷文件 | 助手经 `usbshare.Remote` 用网络共享那同一个库：下载拿 Shell 打开的只读 fd，上传写进管道，Shell 的检查照旧（§3.2） | 拖进、拖出；Music 里能建文件夹 |
| 拔线 | 停止共享，配置换回网卡函数，忘掉信任 | 设备消失 |

- 规矩和 §3.1 一样：每个文件夹是一个存储，名字固定英文；电脑能读全部，能加文件，重名改成「名字 (2)」（再发 ObjectInfoChanged 告诉电脑新名字）；只在 Music 里能建文件夹。手机上的文件对电脑只读（ProtectionStatus 只读）：删除、改名、移动都回 Object_WriteProtected。电脑用 SendObjectInfo 说了要发的文件还没发时可以改名或删掉，它还不在手机上。库给的只读文件夹（`Writable` 为假，网页上没有上传表单的那种）是只读存储（AccessCapability 只读），往里拷文件、建文件夹、复制都回 Store_Read_Only；现在四个文件夹都可写。
- 文件夹收不下的，数据来之前就拒：往 Photos 拷不是 JPEG、PNG 的（Invalid_ObjectFormatCode），超过文件夹的上限（Object_Too_Large），比剩余空间大（Store_Full）；数据比说好的长或短回 Incomplete_Transfer，Shell 不会留下截断的文件。4 GB 以上的文件照规范声明成 0xFFFFFFFF，数据到这次传输结束为止。
- 操作：GetDeviceInfo、OpenSession、CloseSession、GetStorageIDs、GetStorageInfo、GetNumObjects、GetObjectHandles、GetObjectInfo、GetObject、GetPartialObject 和安卓的 GetPartialObject64（libmtp、gvfs 分段读）、SendObjectInfo、SendObject、DeleteObject、MoveObject、CopyObject（在手机上复制到收得下它的文件夹）、GetObjectPropsSupported、GetObjectPropDesc、GetObjectPropValue、GetObjectPropList（Windows 按属性列表读，照安卓的理解：深度 0 一个对象，深度 1 一个文件夹和它的内容）、SetObjectPropValue、GetDevicePropDesc 和 GetDevicePropValue（设备名）。事件：ObjectAdded、ObjectRemoved（电脑看过的文件夹每 5 秒再列一遍，手机上新拍的照片、删掉的文件这样告诉电脑）、ObjectInfoChanged、StorageInfoChanged。控制端点上的类请求：Cancel、Device Reset、Get Device Status。
- 电脑怎么认出 MTP：接口是静态图像类 6/1/1（PTP），接口名「MTP」（libmtp 看它）；Windows 读微软 OS 描述符（0xEE 字符串，再用里面的厂商请求读扩展兼容 ID「MTP」），这两个请求由内核的 configfs 替函数回答；DeviceInfo 里的厂商扩展是 `microsoft.com: 1.0`。
- 沙箱：还是 `phone-usbshared`，同样经 `phone-sandbox` 启动、同样加 `fd-only`；fd 3 是 ep0，fd 4 是 Shell，fd 5、6、7 是批量入、批量出和中断端点，助手一个路径也不开。Shell 一直开着这些文件（FunctionFS 在 ep0 最后一次关掉时撤掉函数），助手崩了再起一个，拿同样的文件；电脑那边的会话断了，再开一个（Windows 也这样）。
- 开发机上没有 USB 设备控制器：模拟的数据线（`mtp.NewSim`）在管道上照 USB 的分包传（一包 512 字节，短包或零长包结束一次传输），自测扮演电脑。FunctionFS 这一层在单元测试里不用 root 测：SOCK_SEQPACKET 套接字对代替函数的几个文件（一次读写是一条记录，像 FunctionFS 的一次请求；写空是零长包），`OpenMTP` 往代替挂载点的普通文件里写描述符。批量出端点总是按整包读，一包里读不下的留到下一次：FunctionFS 把请求凑成整包，老内核上数据比读的多就报 EOVERFLOW。模拟器上的电脑是 dummy_hcd 的主机一侧：自测经 usbfs（`/dev/bus/usb`）用自己的 Go 发起端，再用 libmtp 自己的工具（mtp-detect、mtp-files、mtp-getfile；Linux 的文件管理器 gvfs、KIO 用的就是 libmtp）认设备、列文件、拷文件。libmtp-bin 只进模拟器的镜像，手机的镜像里没有；libmtp、libusb 是 LGPL-2.1，镜像的许可证规则（`INCOMPATIBLE_LICENSE`）只禁 GPLv3、LGPLv3、AGPLv3，`scripts/license-check.py` 只查 Go 模块和 Rust crate，不管镜像里的包。
- 内核：`phone-usb.cfg` 打开 `CONFIG_USB_CONFIGFS_F_FS`，模拟器的 `phone-usb-sim.cfg` 打开 `CONFIG_USB_DUMMY_HCD`。内核配置里没有它们时构建停下（内核配方的 `do_phone_usb_kconfig`；Yocto 自己的配置检查只警告）；自测的 MTP 用例第一步 `usb-mtp kernel` 在 `require-usb` 之前，不会被跳过，再查运行着的内核：有设备控制器时它的配置（`/proc/config.gz`）里要有 `CONFIG_USB_CONFIGFS_F_FS`（functionfs 文件系统要等第一个 FunctionFS 函数建出来才注册，查不了），模拟器（QEMU 的 virt 板子）上还要有 dummy_hcd 的控制器，没有它 USB 的用例会全被跳过而看不出来。

| 环境 | 结果 |
|------|------|
| 单元测试（`usbshare/mtp`、`hal/usbgadget`、`programs/usbshared`、`shell`） | ✓（§5） |
| 开发机自测（模拟的数据线，真的沙箱助手） | ✓ |
| 模拟器（QEMU，dummy_hcd，FunctionFS） | ✓（2026-11-14，`make yocto-build` 加 `make yocto-selftest`）：两个 MTP 用例都通过；libmtp 的 mtp-detect 看到 `microsoft.com: 1.0; android.com: 1.0;`，mtp-files 列出、mtp-getfile 拷走电脑放的文件 |
| 真电脑（Windows 资源管理器、macOS 的 Android File Transfer 和 OpenMTP、Linux 的 gvfs） | **未上真机**：Windows、macOS、Linux 都没试过 |

## 4. 对照：iPhone 怎么用数据线连电脑调试

| 环节 | iPhone | Kario |
|------|--------|-------|
| 信任 | 插上后手机问「要信任此电脑吗？」，要输入锁屏密码；双方交换配对记录（lockdownd 生成，电脑存在 `/var/db/lockdown`、Windows 的 `Apple\Lockdown`），以后免问；「设置 › 通用 › 传输或还原 › 还原位置与隐私」清掉 | 每次插线都问，不存配对记录；拔线即失效 |
| 链路 | 一直是 usbmux：一个 USB 接口上复用多条 TCP 式的连接（电脑端 usbmuxd）；iOS 17 起另有一块 USB-NCM 网卡，开发服务走它上面的 IPv6 隧道 | USB-NCM 网卡，普通的 TCP/IP |
| 服务发现 | lockdownd（端口 62078）按名字开服务；iOS 17 起是 RemoteXPC / RSD（远程服务发现），隧道（CoreDevice）建好后服务列在那里 | 只有文件共享（80 端口）；开发者模式下另有调试终端，开发者的操作经它上的 `phone-dev` 交给 Shell（下面） |
| 文件 | AFC（Apple File Conduit）给「访达」的文件共享用，只能访问声明了文件共享的应用的沙箱；照片走 PTP | 浏览器 / WebDAV，相册、视频、音乐、文件四个文件夹（§3.1） |
| 调试 | 先在「设置 › 隐私与安全性 › 开发者模式」打开（要重启、再确认一次，iOS 16 起）；Xcode 挂载个人化的开发者磁盘映像（DDI，iOS 17 起按设备签名），然后经隧道连 debugserver、instruments 等服务；也能经 Wi-Fi 配对后无线调试 | 开发者模式（设置里打开，[07](07-ecosystem-and-store.md)）下，信任过的数据线能连调试终端（SSH，开发镜像里的 dropbear）；电脑上的 `kario device` 经它装应用、看日志、重新载入、重启（下面）；不在开发者模式时数据线上只连得上文件共享，按链路过滤端口（`netfw.Link`） |
| 电脑端软件 | macOS 自带，Windows 要「Apple 设备」或 iTunes；开源的 libimobiledevice / pymobiledevice3 实现了同样的协议 | 不要：浏览器、文件管理器、`ssh` |

所以「怎么用手机连调试接口」的答案是：iPhone 没有单独的调试口，就是这一根数据线；先信任（配对），再开开发者模式，Xcode 经 usbmux（iOS 17 起是 USB 网卡上的隧道）找到 lockdownd / RSD 列出的开发服务。我们照搬了「先信任、开发者模式才开调试」这两条规矩，链路换成了标准的 USB 网卡，省掉 usbmux 和专用的电脑端软件。

**开发者通道（`kario device`）**：Xcode 经隧道连的那些开发服务，我们这边是 SSH 上的一个命令。电脑上 `kario device install|logs|reload|restart|uninstall|shell`，以及对着系统的 `push|pull|screenshot|key|type|syslog|forward|reverse|reboot|update|bugreport`（`cmd/kario/device.go`；和安卓 adb、fastboot 的对照见 §4.1）跑 `ssh root@10.71.0.1 phone-dev …`（开发镜像的 dropbear；`-ssh`、`-to` 换别的 ssh 和地址）；手机上的 `phone-dev`（`programs/devcmd`，`kario-system` 的一个名字）只把请求连同 SSH 会话的 `SSH_CONNECTION` 交给 Shell 运行目录里的 `dev.sock`（`devctl` 包是两边的协议），由 Shell 决定（`shell/devlink.go`）：

| 规矩 | 怎么做 |
|------|--------|
| 只在开发者模式下 | 每个请求都先查（日志跟随时每秒再查一次）；关着就拒绝：「developer mode is off on the phone」 |
| 只从信任过的数据线来 | 电脑要已信任、用的是网络共享（文件传输 MTP 时没有网络）；设备上 SSH 会话的本端地址要是数据线上的 `10.71.0.1`（从 Wi-Fi 进来的 SSH 不算；dropbear 的连接由 systemd 的双栈套接字接进来，`SSH_CONNECTION` 里写成 `::ffff:10.71.0.1`，按解析出来的 IPv4 地址比，模拟器上真的 SSH 头一回就是卡在逐字比较上）；拔线、取消信任后正在跟随的日志也停 |
| 只有 Shell 自己的用户 | 套接字 0600，再按 `SO_PEERCRED` 核对对方的 uid（设备上是 root，应用都有自己的 uid） |
| 只碰装上去的应用 | 看日志、重新载入、重启、卸载只认商店装的（含经这条通道装的），系统应用一律拒绝 |
| 安装走安装器 | 包的签名要是手机信任的商店（开发者模式下的开发商店）、过同一套 `checkPackage`、同一个 `unpack`，只是不弹确认框（开发者模式和信任这台电脑已经是用户点的），手机上提示「已从电脑安装「…」」；工程目录由 `kario device` 先用开发商店签名，并先 `trust` 这个开发商店的根（只有一个开发商店：换成电脑的以后，别的开发商店签的已装应用下次启动前验签不过） |
| 文件只到两个地方 | `push`、`pull` 的文件在 SSH 会话命令的标准输入输出里传（手机上不用 scp、sftp 服务），先说好字节数，少一个字节都算失败；Shell 只让碰开发者目录（`/var/lib/phone/dev`，像安卓的 `/data/local/tmp`）和开发者模式装的应用的数据（`应用ID:路径`，像 `run-as`），路径经 `os.Root` 解析，`..` 和链接都出不去；推送先写临时文件、收齐再改名，传一半断了什么也不留；写进应用数据的文件归应用的 uid；一次一个文件 |
| 注入的按键分得清 | `key`、`type` 的按键标着「电脑发来的」，一个一个经 Shell 的事件循环按下去（系统对话框刚弹出的那一下照样等过），状态栏写「● 电脑在按键」，停下 3 秒后消失；授予权限、可信确认（付款）、输 PIN 或设 PIN（锁屏、SIM 卡）、录指纹、解锁设备、信任电脑、蓝牙配对、设默认短信应用、应用要发短信或打电话、选照片和拍照交给应用、分享、快速选卡（会付款）这些系统对话框，以及键盘锁，都不接受它（只有取消可以，方向键也不行：不能替人把选项挪到「允许」），手机上提示要用手机自己的按键，电脑上报错。安卓同样不让注入的输入碰安全窗口。恢复出厂设置的 PIN 码界面和最后确认、防盗锁的界面也不接受（[31](31-lock-and-biometrics.md) §11） |
| 截屏遮住安全画面 | 面板上的渲染器不把画面交给别的进程（[23](23-renderer-protocol.md) §5），所以 Shell 另起同一个渲染器的无头实例（设备上以 nobody、没有网络运行，PNG 经 `os.Root` 读回），把面板那一帧画成 PNG，和自测的 `snap` 一样；正在输 PIN、可信确认、蓝牙配对码、页面上有安全内容（付款码、换机的二维码和热点密码）时，那一帧只留状态栏、标题和软键，内容换成一块 `secure` 的图，渲染器的截图一律遮住它，共享内存的画面也不交过去，电脑上说「masked: …」。没有像素渲染器（开发机的文字界面）时拒绝 |
| 端口转发跟着门禁 | `forward`、`reverse` 是电脑那个 SSH 会话自己的端口转发（`ssh -L`、`-R`），手机这头连的是回环地址；转发的连接都在 22 端口里，数据线的防火墙（`netfw.Link`）只在开发者模式下放行 22（关掉时已开着的连接排空后再关，§3），Shell 也查这一条；回环地址在按应用的规则之前就放行（没有网络权限的应用在空的网络命名空间里，没有手机的回环地址，连不到反向转发的端口）；反向转发只用 1024 以上的端口。`phone-dev forward` 在会话里一直挂着，Shell 每秒查一次门禁，关了就结束请求，会话的命令一结束 SSH 就断，转发跟着没了；电脑上停掉 `kario device` 也一样 |
| 系统更新照常验签 | `update` 把 `.raucb` 收到数据分区，走设置里同一条更新路径：RAUC 按镜像的密钥环验签、查是不是这台机器的，没签名或签错的拒装，电脑上看到 RAUC 的原话；装进另一个槽，重启后试运行，起不来健康就退回原来的槽；版本号由 `rauc info` 读出时照常记下，等结果交更新服务器的报告（[25](25-system-update.md)）；进度一行行回给电脑；装完包就删掉 |
| 重启 | `reboot` 走 systemd（`systemctl reboot --reboot-argument=bootloader\|recovery`），等着的系统更新照设置里那样重启进去；开发机不重启 |
| 问题报告不含私人数据 | `bugreport` 是一个 zip：版本、`sku.json`、应用列表、崩溃记录（不带应用的日志尾巴）、Shell 和渲染器的日志、systemd 的日志（开发机上是服务的日志）、`dmesg`、RAUC 的状态；短信和聊天内容、通讯录、照片和文件、各种密钥、Wi-Fi 密码、账号令牌、应用的数据和应用自己的日志都不在里面，zip 里的 README.txt 写明 |

验证：`devctl`、`phone-dev`、Shell 一端在开发机上有单元测试（`shell` 的 `TestDevLink`：开发者模式关、没信任、文件传输、设备上不是数据线来的会话都拒绝，信任后装、看日志、跟随中拔线就停、重启、重新载入、卸载，系统应用拒绝；`TestDevLinkRefusesBeforeThePackage`：装应用时 Shell 先答应了才传包，拒绝在包发出去以前到，8 MB 的包也不会写到一半断管（先确认过旧的「请求后面直接跟包」会这样失败，开发机自测也偶尔这样失败过）；`cmd/kario` 的 `TestDevice`：假的 ssh 和假的手机）；开发机自测「开发者模式：电脑经信任过的数据线……」✓（`phone-dev` 直接跑，没有 SSH）。模拟器上经真的 SSH（电脑一端在自己的网络命名空间里用 dropbear 的 `dbclient`）的用例「开发者模式在模拟器上：……」QEMU 上 ✓（2026-11-15；头一回卡在地址的逐字比较上，见上表「只从信任过的数据线来」）；真电脑上的 OpenSSH 对着真机**未上真机**。

对着系统的工具：`devctl` 的 `TestCheck`、`TestCallBinary`、`TestCallBodyAndProgress`、`TestCallForward`；`shell` 的 `TestDevToolsGate`（每个工具都过同一道门禁）、`TestDevInjectedKeys`（信任对话框和键盘锁拒绝注入的按键，取消可以，人自己按的照常，状态栏的提示）、`TestDevTypedText`、`TestDevPushPull`（开发者目录、开发者模式装的应用的数据；别处、`..`、系统应用、商店应用都拒绝；传一半断了不留文件）、`TestDevSyslog`（开发机上服务的日志和 Shell 的日志，应用的日志不给，`-u`、`-n`；设备上给 journalctl 的参数；跟随中关掉开发者模式就停）、`TestDevForward`（防火墙不放行 22 时拒绝，关掉开发者模式转发就断，电脑走了也断）、`TestDevReboot`、`TestDevUpdate`（进度、装进的槽，没签名的被 RAUC 拒，包不留在手机上）、`TestDevScreenshot`（假渲染器：截图那一帧里没有配对码、付款提示，浮层换成安全图）、`TestDevBugreport`（应用的日志和崩溃记录里的日志尾巴不进报告）；`cmd/kario` 的 `TestDeviceTools`、`TestRemoteWord`（远端命令行里的引号）。开发机自测的四个用例 ✓：按键打字、推拉文件、问题报告和系统日志和旁加载更新（模拟的 A/B 手机）、截屏（要像素渲染器，用 phone-paint 跑过）。模拟器上经真的 SSH 推拉文件、按键、看 systemd 的日志、端口转发的用例（`dev-forward`：电脑的网络命名空间里 `dbclient -L`，数据真的经 22 端口到手机回环地址上的回显服务再回来，关掉开发者模式时会话当场结束）只在 QEMU 上跑，QEMU 上通过（2026-11-20，见 [13](13-prototype.md)）。关掉开发者模式时会话上还有数据在走（§3 的排空）：`netfw` 的 `TestLinkRules`（排空那条规则只认已有连接、带计数器，没有端口排空时不碰 conntrack），`shell` 的 `TestUSBDrainDone`（安静 3 秒或到 30 秒算完）、`TestUSBFirewallDrains`（关掉后 22 不接新连接、开着的照样进，忙着时不关、安静了或到时限才关，开发者模式再打开、拔线都结束排空，内核不接受这条规则时当场关上；去掉排空先确认过它失败）；`dev-forward` 现在在转发的连接还双向传着数据时关掉开发者模式，QEMU 上 ✓（2026-11-21：新的 SSH 连接被拒，之后开着的会话又回来 53 次回显，电脑关掉转发的连接后 dbclient 当场退出、写着原因，22 对开着的连接 3.7 秒后关上）；同一步在只少了排空的镜像上失败：关掉开发者模式 83 秒后 dbclient 还在，之后一次回显也没回来，手机上有 336 字节没被确认，Shell 那边早已结束了请求。真机**未上真机**。

### 4.1 对照安卓：要不要 adb 和 fastboot

adb 要，由 `kario device` 做（下表）；fastboot 那部分 Kario 不做：Kario 不实现 bootloader，刷机、解锁 bootloader 用板子自己的：一加 6T 上是 ABL 自带的 fastboot（`yocto/fajita-flash.sh` 就是用它刷的，[39](39-oneplus-6t.md)），出厂刷机走 ODM 的工具（高通的 EDL，即 9008 下载模式；展锐的下载工具）。安卓 recovery 做的事大多由 A/B 槽加 RAUC 的回退代替（[25](25-system-update.md)）。

| 安卓 | Kario | 说明 |
|------|-------|------|
| `adb devices`、`adb connect` | 信任过的数据线上的 `10.71.0.1` | 一根线一台手机；`-to` 换地址 |
| `adb install`、`adb uninstall` | `kario device install`、`uninstall` | 开发商店签名，走安装器 |
| `adb shell` | `kario device shell` | 现在是 root 的 shell（§4.2） |
| `adb push`、`adb pull` | `kario device push`、`pull` | 只到开发者目录和开发者模式装的应用的数据，一次一个文件 |
| `adb shell run-as 应用` | `应用ID:路径` | 只给开发商店签名、开发者模式装的应用 |
| `adb exec-out screencap` | `kario device screenshot` | 安全画面遮住 |
| `adb shell input keyevent`、`input text` | `kario device key`、`type` | 系统的信任对话框和键盘锁不接受 |
| `adb logcat` | `kario device syslog`（系统）、`logs`（一个应用） | |
| `adb forward`、`adb reverse` | `kario device forward`、`reverse` | SSH 的端口转发，挂到门禁关上为止 |
| `adb reboot`、`reboot bootloader`、`reboot recovery` | `kario device reboot [bootloader\|recovery]` | 每块板子不一样，见下 |
| `adb sideload` | `kario device update 包.raucb` | 有 RAUC 的 A/B 槽才行 |
| `adb bugreport` | `kario device bugreport` | 不含私人数据 |
| `adb shell am force-stop`、`am start` | `kario device restart`、`reload` | |
| 调试（JDWP） | `kario dap`（[29](29-app-development.md) §5.5） | |
| `adb root`、`remount`、`disable-verity` | 没有 | 系统分区只读、有 dm-verity；自己编的组件只在解锁的设备上换（设置 › 设备解锁） |
| `fastboot flash boot`、`userdata` | 板子的 bootloader：6T 上 `fajita-flash.sh flash` | Kario 不实现 bootloader |
| `fastboot boot` | 6T 上 `fajita-flash.sh test` | 只在内存里启动一次 |
| `fastboot flashing unlock`（`oem unlock`） | 板子的 bootloader（6T：`fastboot oem unlock`，[39](39-oneplus-6t.md) §3.3） | Kario 设置里的「设备解锁」是另一回事：清掉应用密钥，好换自己编的系统组件 |
| `fastboot set_active`、`--slot` | RAUC 写另一个槽、设下次启动，phone-ota 试运行、起不来就退回；6T 上由 `phone-qcom-abslot` 标「启动成功」 | 没有单独的命令 |
| EDL（9008）、展锐的下载工具 | ODM 的工具 | 出厂刷机、救砖 |
| recovery：装 OTA、清除数据 | A/B 加 RAUC 的回退；`kario device update`；设置 › 更多设置 › 恢复出厂设置，锁屏上忘了 PIN 码时也能清除（[31](31-lock-and-biometrics.md) §11） | 没有开机时按键进的清除界面（**未实现**）；不经设置的清除由防盗锁管 |

`reboot` 的目标按板子：

- 一加 6T（ABL）：`bootloader` 由内核写进 PMIC（pm8998 的 PON，主线设备树里 bootloader 是 0x2、recovery 是 0x1，要内核的 qcom-pon 重启模式驱动，Nura 的配置里有），ABL 读到后停在 fastboot；`recovery` 让 ABL 按 recovery 方式启动当前槽的 boot 分区，Kario 的启动镜像不分这个，照常进 Kario。**未上真机**。
- QEMU（qemuarm64）：没有 bootloader 读这个原因（QEMU 自己加载内核），就是普通的重启。
- 开发机：不重启，报「not a phone」。

还缺的（**未实现**）：Wi-Fi 上调试（`adb tcpip`、`adb pair`：只认数据线）；目录和多个文件的 `push`、`pull`，`adb sync`；录屏（`screenrecord`）；`dumpsys` 那样经 `kario device` 看系统服务的状态（手机上有 `phone-shell --dump`）；开机时的清除界面（recovery 的清除数据，[31](31-lock-and-biometrics.md) §11.2）；非 root 的开发者账号（§4.2）。一加 6T 没有 RAUC 的 A/B 系统槽（根文件系统是整个 userdata），`update` 在它上面拒绝，系统靠 `fajita-flash.sh` 从 fastboot 刷；`reboot recovery` 没有 recovery 可进。截屏的无头渲染器没有面板上那个渲染器的 Landlock 和 seccomp（只是 nobody、没有网络）。

### 4.2 root，还是只给开发者一个普通账号

现在：镜像只有一种（没有安卓那样单独的 user 版），一直带着 dropbear，允许 root 空密码登录（kas 的 `allow-empty-password empty-root-password allow-root-login`），所以开发者模式下信任过的数据线就是 root 的 shell，`kario device` 也以 root 登录。安卓的 user 版上 adbd 以 `shell` 用户运行，`adb root` 被拒，只有 userdebug、eng 版才有 root。

评估：§4 的工具都不要 root：`phone-dev` 只连 Shell 的套接字，什么能碰由 Shell 决定。所以普通账号要的只有能登录、能运行 `phone-dev`、能连 `dev.sock`。但这不是小改动：镜像里要加用户；dropbear 要拒绝 root、不再空密码，换成密钥，就要像安卓那样在信任电脑时把电脑的公钥交给手机、请用户核对指纹（新的信任步骤和界面）；QEMU 上现有经 SSH 的用例都以 root 登录，要跟着改；这些都得构建镜像、在模拟器上验。所以这次**未实现**，设计如下：

| 部分 | 做法 |
|------|------|
| 账号 | `dev`，uid 由镜像固定（比如 2000，同安卓的 `shell`），家目录是开发者目录，不在任何特权组里 |
| 登录 | dropbear 加 `-w`（不许 root）、`-s`（不许密码），`dev` 只认密钥；开发者模式下信任电脑时，手机显示电脑公钥的指纹请用户确认，Shell 把它写进 `dev` 的 `authorized_keys`，拔线就忘（或者像安卓那样记住，设置里能撤销） |
| 套接字 | `dev.sock` 改成 0660、属组 `dev`，Shell 按 `SO_PEERCRED` 认 Shell 自己的 uid 和 `dev` 的 uid |
| 工具 | 都经 Shell，不用改；`kario device shell` 是 `dev` 的 shell：能看 `/proc`、跑 `phone-dev`，碰不到别人的数据 |
| root | 只在解锁的设备上（设置 › 设备解锁，那时应用密钥已经清掉）才给，相当于安卓的 userdebug |

## 5. 测试

| 用例 | 在哪 | 测什么 |
|------|------|--------|
| USB 连接电脑 | 自测（`shell/selftest.script`），开发机上用模拟的线，模拟器上用 dummy_hcd | 插线 → 问信任 → 信任 → 通知里有密码 → 电脑拿到照片列表、往「文件」里放一个文件、读回来 → 拔线 → 提示停止 → 电脑连不上。模拟器上电脑一侧（cdc_ncm 的网卡）放进一个单独的网络命名空间，插线后就在那里打开（像电脑的网络管理器那样：要在手机这一端信任后打开之前开着，usbnet 在打开的过程中收到的连接通知会丢，链路就一直没有载波），从手机的 DHCP 拿地址，请求真的走过 USB 链路 |
| 鼠标、键盘插拔 | `make yocto-gui`：开机后经 QEMU 监视器插上 USB 鼠标、键盘（`device_add`），再拔掉 | 渲染器认出后插上的设备（控制台上的 `PHONE-GUI-INPUT`）；鼠标左键、滚轮各让画面变一次；拔掉后渲染器看到它们走了 |
| 文件共享服务 | `usbshare` 单元测试 | 密码、错 10 次锁住、列表、下载、WebDAV PROPFIND、只读文件夹拒绝写入、浏览器上传（Windows 路径只留文件名）、出库路径；按显示名的网址、文件夹里的文件夹（`MKCOL`、`PUT`、`PROPFIND`、上传整个文件夹、想出文件夹的路径只留文件名）、超过文件夹上限的文件拒收（413，不会截断着存下） |
| 往 `Music` 里拷专辑文件夹 | 自测（「音乐：电脑经 USB……」），开发机 ✓ | 电脑看到 `Music` 和 `Photos`；往「Music/电脑拷来的专辑/」里放一个歌词文件，文件夹出现在列表里，按 `Music` 和小写 `music` 的网址都读得回来 |
| 共享分成两个进程 | `usbshare` 单元测试 | Shell 一端和助手一端经真的 socketpair：列表、下载（只读 fd）、上传（管道）、`MKCOL`、上传整个文件夹；超过上限的 413（说了长度和没说长度两种），刚好等于上限的收下；不是只读打开的文件不交出去；扮演越界助手（`usbshare.Misbehave`）的每个请求都被拒绝，什么也没存下；Shell 一端关掉后助手一端结束 |
| 共享助手的沙箱 | `sandbox`、`programs/usbshared` 单元测试 | 子进程里施加 `DescriptorsOnly`：按路径打开、新建、删除，建套接字、连 Unix 套接字，exec、ptrace 都是 EPERM，继承的管道照样读、继承的监听套接字照样 accept；真的启动 phone-usbshared：它报告的沙箱层、探测结果，经它下载、上传、413，Shell 一端关掉后它以 0 退出 |
| 共享在单独的沙箱进程里 | 自测（「USB 文件共享在单独的沙箱进程里……」），开发机 ✓，模拟器 ✓ | 信任后助手的沙箱层和探测（打不开文件、建不了套接字、运行不了程序；设备上还要求独立 uid、没有能力、命名空间、Landlock）；有 cgroup 时助手在自己的组里，`memory.max`、`memory.swap.max`、`pids.max` 是上面的值（`usb-helper limited`；开发机上没有 cgroup，只记一行日志，组和上限模拟器上 ✓）；扮演越界助手的请求对着真的相册、音乐库、文件库全被拒绝；往「相册」拷一个比上限多 1 KB、不说长度的文件得到 413，也没存下；杀掉助手后 Shell 起一个新的，照常下载、往「音乐」里上传；拔线后助手进程不在了，电脑连不上 |
| PD | `hal/power` 单元测试 | `[PD]` 是快充，电脑的口不是；老驱动的 `USB_PD`；没插电、没电池 |
| USB 设备 | `hal/usbgadget` 单元测试 | configfs 的内容、绑定控制器、MAC 稳定且是本地地址 |
| MTP 协议 | `usbshare/mtp` 单元测试：模拟的数据线，Go 写的发起端 | Windows 的读法：DeviceInfo、存储、属性描述、深度 0 和 1 的属性列表；下载、GetPartialObject 和 GetPartialObject64、正好落在包边界上的大小（零长包）；上传、重名改成「(2)」和随后的事件、Music 里建文件夹、CopyObject；拒绝删除、改名、移动、往 Photos 拷文本、超过上限、说小发大、发得比说的少、错的父对象和存储、关掉会话以后的操作；只读文件夹照样能读，拷文件、建文件夹、复制进去都被拒；4 GB 以上的长度写法；手机上增删文件的事件；类请求；经真的 socketpair 走 `usbshare.Remote` |
| MTP 经 FunctionFS | `usbshare/mtp` 单元测试：SOCK_SEQPACKET 套接字对代替 ep0 到 ep3 | 响应端原样跑在 `FFS()` 上：ep0 一次读到几个事件，Get Device Status，Cancel 带着数据，不认识的请求两个方向都 stall 以后 ep0 照样对得上；几块大的下载和上传、正好落在包边界上的（两个方向的零长包）、分段读，中断端点上的事件；函数被停用后会话结束，电脑再开一个；端点读出错时 `Serve` 返回 |
| MTP 函数 | `hal/usbgadget` 单元测试 | FunctionFS 描述符（三种速度、三个端点、微软 OS 描述符）和字符串的字节；`OpenMTP` 对着代替挂载点的普通文件：configfs 里的函数、ep0 先收描述符再收字符串、端点的顺序和方向，少一个端点就失败；没有 FunctionFS 时错误里写出要的内核选项；configfs 换成 MTP 再换回网卡；Shell 中途退出留下的 MTP 函数由 `Setup` 拿掉 |
| MTP 助手 | `programs/usbshared` 单元测试 | 真的启动 phone-usbshared 的 MTP 模式（模拟的数据线）：沙箱层、探测，经它下载、上传，超过上限、删除被拒，Shell 一端关掉后以 0 退出 |
| 锁着时的文件传输 | `shell` 单元测试 | 有锁屏 PIN 码时 MTP 等解锁才问信任；网络共享照旧锁着也问 |
| 总线复位 | `shell` 单元测试 | 信任后经过 default、addressed 回到 configured，共享不停；还在问要不要信任时复位，问题不撤（先确认过旧代码会撤）；复位中拔线照样停；超过等待时间没回来也停 |
| 文件传输（MTP） | 自测（「USB 文件传输（MTP）……」），开发机 ✓，模拟器 ✓ | 先查内核（`usb-mtp kernel`）；设置 › USB 选文件传输；信任后四个存储，Windows 式的属性列表，下载、分段读、上传、在 Music 里建文件夹；删除、改名、往 Photos 拷文本都被拒，文件原样；杀掉助手后电脑重开会话照常；拔线后电脑看不到 MTP 设备 |
| MTP 在模拟器上 | 自测（「USB 文件传输（MTP）在模拟器上……」），只在模拟器上跑，✓ | 经 usbfs 读微软 OS 描述符里的扩展兼容 ID 和接口名，都是「MTP」；文件经 dummy_hcd 真的过线；libmtp 的 mtp-detect 看到微软的扩展，mtp-files 列出、mtp-getfile 拷走电脑刚放进 Files 的文件（镜像里要有 libmtp-bin） |

## 6. 没做的

- MTP 没在真的 Windows、macOS（Android File Transfer、OpenMTP）、Linux（gvfs、KIO）电脑上试过，**未上真机**；Windows 读属性列表、上传的流程照规范和安卓的做法写成，测试里的发起端是我们自己的；模拟器上另有 libmtp 的工具（✓），Windows 的 MTP 驱动（WPD）、macOS 上的软件没有替身。
- MTP 传到一半取消只在两块数据之间停下（电脑停着不收或不发时，FunctionFS 上卡住的那次读写停不下来），没和 Windows 对过，**未验证**；助手在电脑枚举完之前启动，包大小按高速（512 字节）算，全速电脑**未验证**。缩略图（GetThumb）、SendObjectPropList、对象引用（播放列表）**未实现**；手机上的变化最多晚 5 秒才发事件。
- 真机上的 PD 驱动、口的数据角色切换（手机当主机还是设备由口控制器决定，`CONFIG_TYPEC`、`USB_ROLE_SWITCH` 已打开）。
