# USB-C：充电、键盘鼠标、连接电脑

> 状态：**原型 ✓（QEMU）**。键盘鼠标插拔、连接电脑（信任 → 密码 → 拷文件 → 拔线停止）在模拟器上端到端通过（dummy_hcd 让一台虚拟机同时是手机和电脑）；文件共享搬进单独的沙箱进程（§3.2）以后在模拟器上也通过了（2026-11-14）；PD 快充的识别有单元测试，**未上真机**（要等骁龙板子，R16）。文件传输（MTP，§3.3）有单元测试，开发机自测 ✓，模拟器 ✓（经 dummy_hcd，自己的发起端和 libmtp 的工具都试过），真的 Windows、macOS、Linux 电脑**未上真机**。手机上的硬件层见 [11](11-platform-architecture.md) §「那 hal/ 是什么」。

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
- USB 总线复位不算拔线（Windows 出错后会复位；libmtp 放开一台它当成安卓的手机时也复位，DeviceInfo 里有 `android.com` 它就这么看）：设备经过 default、addressed 再回到 configured，共享和信任都留着；15 秒内没回到 configured 才当拔线。
- 防火墙给 USB 链路单独一条链（`netfw.Link`，在按应用的规则之前）：信任前什么都进不来；信任后只放行 DHCP 和文件共享（80 端口），开发者模式下再放行调试终端（22 端口）。所以手机上别的服务即使监听在所有网卡上，数据线也连不到。设备上的自测核对 80 通、22 不通，打开开发者模式后 22 通。
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
- 名字：合法的 UTF-8，没有控制字符，每一段不空、不是 `.` 或 `..`；`/`（和 Windows 的 `\`）只在「音乐」里当文件夹分隔符；音乐库自己再查一遍（不以点开头、最多 8 层）。不合格的建文件夹、上传得到「手机上的文件或文件夹不能用这个名字」。
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
| 服务发现 | lockdownd（端口 62078）按名字开服务；iOS 17 起是 RemoteXPC / RSD（远程服务发现），隧道（CoreDevice）建好后服务列在那里 | 只有文件共享（80 端口）；开发者模式下另有调试终端 |
| 文件 | AFC（Apple File Conduit）给「访达」的文件共享用，只能访问声明了文件共享的应用的沙箱；照片走 PTP | 浏览器 / WebDAV，相册、视频、音乐、文件四个文件夹（§3.1） |
| 调试 | 先在「设置 › 隐私与安全性 › 开发者模式」打开（要重启、再确认一次，iOS 16 起）；Xcode 挂载个人化的开发者磁盘映像（DDI，iOS 17 起按设备签名），然后经隧道连 debugserver、instruments 等服务；也能经 Wi-Fi 配对后无线调试 | 开发者模式（设置里打开，[07](07-ecosystem-and-store.md)）下，信任过的数据线能连调试终端（SSH，开发镜像里的 dropbear）；不在开发者模式时数据线上只连得上文件共享，按链路过滤端口（`netfw.Link`） |
| 电脑端软件 | macOS 自带，Windows 要「Apple 设备」或 iTunes；开源的 libimobiledevice / pymobiledevice3 实现了同样的协议 | 不要：浏览器、文件管理器、`ssh` |

所以「怎么用手机连调试接口」的答案是：iPhone 没有单独的调试口，就是这一根数据线；先信任（配对），再开开发者模式，Xcode 经 usbmux（iOS 17 起是 USB 网卡上的隧道）找到 lockdownd / RSD 列出的开发服务。我们照搬了「先信任、开发者模式才开调试」这两条规矩，链路换成了标准的 USB 网卡，省掉 usbmux 和专用的电脑端软件。

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
| 总线复位 | `shell` 单元测试 | 信任后经过 default、addressed 回到 configured，共享不停；复位中拔线照样停；超过等待时间没回来也停 |
| 文件传输（MTP） | 自测（「USB 文件传输（MTP）……」），开发机 ✓，模拟器 ✓ | 先查内核（`usb-mtp kernel`）；设置 › USB 选文件传输；信任后四个存储，Windows 式的属性列表，下载、分段读、上传、在 Music 里建文件夹；删除、改名、往 Photos 拷文本都被拒，文件原样；杀掉助手后电脑重开会话照常；拔线后电脑看不到 MTP 设备 |
| MTP 在模拟器上 | 自测（「USB 文件传输（MTP）在模拟器上……」），只在模拟器上跑，✓ | 经 usbfs 读微软 OS 描述符里的扩展兼容 ID 和接口名，都是「MTP」；文件经 dummy_hcd 真的过线；libmtp 的 mtp-detect 看到微软的扩展，mtp-files 列出、mtp-getfile 拷走电脑刚放进 Files 的文件（镜像里要有 libmtp-bin） |

## 6. 没做的

- MTP 没在真的 Windows、macOS（Android File Transfer、OpenMTP）、Linux（gvfs、KIO）电脑上试过，**未上真机**；Windows 读属性列表、上传的流程照规范和安卓的做法写成，测试里的发起端是我们自己的；模拟器上另有 libmtp 的工具（✓），Windows 的 MTP 驱动（WPD）、macOS 上的软件没有替身。
- MTP 传到一半取消只在两块数据之间停下（电脑停着不收或不发时，FunctionFS 上卡住的那次读写停不下来），没和 Windows 对过，**未验证**；助手在电脑枚举完之前启动，包大小按高速（512 字节）算，全速电脑**未验证**。缩略图（GetThumb）、SendObjectPropList、对象引用（播放列表）**未实现**；手机上的变化最多晚 5 秒才发事件。
- 真机上的 PD 驱动、口的数据角色切换（手机当主机还是设备由口控制器决定，`CONFIG_TYPEC`、`USB_ROLE_SWITCH` 已打开）。
