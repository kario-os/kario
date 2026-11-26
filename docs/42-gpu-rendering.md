# GPU 渲染：上屏、合成、光栅

> 状态：GPU 合成（L1，包括 L0：经 EGL/GBM 上屏）已实现，可选：设了 `PHONE_PAINT_GPU=1`，或者没设而板子的 `paint_gpu` 是 `compose` 时用（§3；现在所有板子都是 `off`）；CPU 路径仍是默认和回退；构建开关 `PHONE_PAINT_GPU_DEFAULT`（`yocto/kas/paint-gpu.yml`）出一个默认在 GPU 上合成的镜像，板子不变（§3，给一加 6T 的测试镜像用）。在 QEMU 上用软件 GPU 验证过（Mesa 的 softpipe，经 kms_swrast，QEMU 的 2D virtio-gpu）：GBM surface、翻页、在沙箱里、和 CPU 路径一样的画面、中途退回 CPU（§7）。**未上真机**（Adreno、Mali、PowerVR 都没试过；一加 6T 上要确认的见 [39](39-oneplus-6t.md) §8.1）。GPU 光栅化（L2）也已实现，可选（`PHONE_PAINT_GPU=raster`）：开发机的 llvmpipe 上 GPU 画的每种操作和 CPU 逐字节相同，**未上真机**（§9）。显示和 GPU 是两个 DRM 设备时在 GPU 的 render node 上渲染、交给显示设备翻页（§4.1；QEMU 上用 card 的第二个文件验证，真的两个设备**未上真机**）；颜色滤镜和放大镜在 GPU 路径上做，不再让 GPU 让开（§4.2）；翻页被拒时日志写出内核看到的 CRTC、平面和帧缓冲（§4.3）。上真机前做了什么、每类芯片还要什么：§10。
> 范围：phone-paint 的内部（[23](23-renderer-protocol.md) §2：协议 `phone.frame/1` 不变；GPU 合成时 `ready` 的 `features` 里有 `gpu`）。具体做法和在 llvmpipe 上的耗时见 [37](37-motion.md) §5 第 8 条和 §2.4。

## 1. 现状

| 部分 | 状态 | 在哪里 |
|------|------|------|
| L0：经 GBM surface、EGL 和 KMS 翻页上屏 | ✓ 是 L1 的最后一遍；QEMU 上验证过（softpipe），**未上真机** | `paint/src/gpu.rs`（`show`）、`paint/src/gbm.rs`、`paint/src/kms.rs`（`scan_out`） |
| L1：CPU 画各图层，GPU 上传变了的部分、合成、跑转场、上屏 | ✓ 可选（`PHONE_PAINT_GPU=1`）；画面在开发机上用 llvmpipe 无头验证过，在 QEMU 的屏幕上用 softpipe 验证过 | [37](37-motion.md) §5 第 8 条 |
| 软件光栅器当试验台 | ✓ `PHONE_PAINT_GPU=soft`（只用于测试，§3） | `gpu::Ask` |
| 在沙箱里启动 | ✓ 已定，QEMU 上验证过（Landlock、seccomp、nobody），§4 | `paint/src/main.rs` |
| 镜像里的 Mesa | ✓ 开源 QEMU 镜像构建过（softpipe，不带 LLVM，17.5 MB）；产品镜像用同一段 kas，这一批**没有构建**；一加 6T 的配置加了 freedreno，2026-11-25 构建过（§5），**未上真机** | `yocto/kas/phone-qemuarm64.yml`（`gpu`）、`yocto/kas/phone-fajita.yml` |
| QEMU 检查 | ✓ `make yocto-gui-gpu`（§7） | `yocto/qemu-gui-gpu.sh`、`yocto/gui-compare.py` |
| virtio-gpu-gl（virgl） | **没跑**：这台构建机没有 render node（§7.3） | — |
| L2：GPU 光栅化 | ✓ 可选（`PHONE_PAINT_GPU=raster`）；开发机的 llvmpipe 上逐字节核对过，**未上真机**（§9） | `paint/src/raster.rs`、`paint/src/scan.rs` |
| 按板子定默认 | ✓ `sku.json` 的 `paint_gpu`（`off`、`compose`、`raster`），环境变量优先；所有板子都是 `off`（§3） | `sku/boards/*.toml`、`tools/sku.py`、`shell/sku.go`、`gpu::Ask::choose` |
| 按镜像定默认（构建开关） | ✓ `PHONE_PAINT_GPU_DEFAULT` 盖过板子写进镜像的 `sku.json`，`yocto/kas/paint-gpu.yml` 设成 `compose`；一加 6T 的 `phone-sku` 两种都构建过（§3），**未上真机** | `yocto/kas/paint-gpu.yml`、`phone-sku_0.1.bb` |
| 显示和 GPU 是两个设备 | ✓ 沙箱之前打开 GPU 的 render node，在那里渲染，缓冲经 PRIME 交给显示设备翻页；QEMU 上用 card 的第二个文件验证（§7），**未上真机**（§4.1） | `paint/src/node.rs`、`paint/src/gbm.rs`、`kms::Display::import_fb` |
| 颜色滤镜、放大镜 | ✓ 滤镜是上屏那一遍的着色器，放大镜的画面由 CPU 画、GPU 上屏；llvmpipe 上和 CPU 路径比过（§4.2），**未上真机** | `paint/src/gpu.rs`（`PRESENT`）、`paint/src/main.rs`（`looks`） |
| 翻页被拒 | ✓ 第一次被拒时日志写出内核看到的；设一次 CRTC 再试一次翻页；挂着的翻页等它完成再试（§4.3） | `paint/src/kms.rs`（`describe`） |

## 2. 三个层次

| 层次 | 做什么 | 这里 |
|------|------|------|
| L0 上屏 | CPU 照旧画整张图，GPU 把它放上屏：竖屏转四分之一圈、大显示器上居中都在一遍 GPU 里做，不用 CPU 逐像素 | 在 L1 里完成：它的最后一遍（`PRESENT`）把屏幕画进 GBM surface 的缓冲，翻页显示 |
| L1 合成 | 图层（页面、顶层、正文分段、滑动的横幅）成为纹理；GPU 只上传画过的矩形，一遍合成整屏，转场是着色器 | 已做，可选（37 §5 第 8 条）；光栅化仍全在 CPU（tiny-skia、ab_glyph） |
| L2 光栅 | 每个绘制操作（`Op`）都在 GPU 上：矩形、圆角和圆按 tiny-skia 的覆盖率算，文字用字形图集；多边形和被裁到的形状由 CPU 画了再叠上 | 已做，可选（37 §5 第 10 条，§9） |

和最初的设计（同一批里在另一台机器上写的）不同的地方：直接做了 L1，没有单独的 L0；库在运行时打开，用 khronos-egl 的动态加载和 glow（都是 MIT/Apache），libgbm 用一个小的 dlopen（`gbm.rs`）；`features` 里有 `gpu`，Shell 和测试能看出是哪条路在画；`PHONE_PAINT_GPU=1` 时 GPU 起不来不退出，而是回退；GPU 在降权之后启动，而不是之前（§4）。

## 3. 怎么打开

| `PHONE_PAINT_GPU` | phone-paint 做什么 |
|------|------|
| 不设 | 按板子的 `paint_gpu`（下面）：`compose` 同 `1`，`raster` 同 `raster`，`off` 或没写就用 CPU 画 |
| `0`、空、其他值 | 用 CPU 画（dumb buffer），和以前一样，板子写什么都一样 |
| `1` | 能在 GPU 上启动就用 GPU 合成；驱动只是软件光栅器（llvmpipe、softpipe、swrast）时不用：在它上面 GPU 路径比 CPU 路径慢（37 §2.4） |
| `soft` | 同 `1`，软件光栅器也用：**只是测试模式**，给没有 GPU 的 QEMU 2D virtio-gpu 用（Mesa 在那里退到 kms_swrast 和 softpipe）；产品上不用 |
| 和 `1` 或 `soft` 一起设 `PHONE_PAINT_GPU_FAIL=<n>` | GPU 在它上屏的第 n 帧故意失败：测回退（之后由 CPU 画） |
| 和 `1` 或 `soft` 一起设 `PHONE_PAINT_GPU_RENDER=<节点>` | 在 `/dev/dri` 下的这个节点上渲染，不管它是什么，缓冲经 PRIME 交给显示设备（§4.1）：测分开的设备这条路；QEMU 上是 `/dev/dri/card0`（§7.4） |

比较几条路的数字（帧时间、输入到上屏、冷启动）时另有两个变量，和上表的任何一个一起设（[39](39-oneplus-6t.md) §8.1 末尾的门槛，[23](23-renderer-protocol.md) §3.1）：`PHONE_PAINT_TARGET=480x640`（或 `640x480`，`@<缩放>`，默认 2）按产品的屏幕大小画、放在面板正中，比面板大时不管它；`PHONE_PAINT_PLAY=<文件>` 放一套固定的按键和触摸（`tools/perf/gate.play`），放完把数字写进 Shell 给的文件（`/run/phone/paint-stats.json`）。`$W perf` 把三条路各在两种大小上跑一遍、排成一张表。

**按板子定默认**：板子文件（`sku/boards/*.toml`）的 `paint_gpu`，`off`（默认）、`compose`、`raster`，SKU 文件里也能写、盖过板子的；只有 `gpu = true` 的板子能写不是 `off` 的值（`tools/sku.py` 核对，`tools/sku_test.py` 的 `test_paint_gpu`）。不是 `off` 时写进 `sku.json`，Shell 再核对一遍（`shell/sku.go`，`TestPaintGPU`），握手时放进 `device` 的 `gpu` 交给渲染器（[23](23-renderer-protocol.md) §2；`--device` 换形态时也带上），phone-paint 没设 `PHONE_PAINT_GPU` 时照它走（`gpu::Ask::choose`）。环境变量只要设了就算数：板子写 `compose` 时 `PHONE_PAINT_GPU=0` 照样用 CPU。现在**所有板子都是 `off`**：一加 6T 的板子文件明写 `paint_gpu = "off"`，等 [39](39-oneplus-6t.md) §8.1、§14.4 在手机上都过了再改（怎么改见 39 §8.1 末尾），别的板子没写（也是 `off`），它们的 GPU 都没在手机上试过。显示设备不能渲染的板子另写 `gpu_render`（GPU 的 render node 的内核驱动名，比如 `panfrost`，§4.1）。

**按镜像定默认（构建开关）**：板子还是 `off`、又要一个默认在 GPU 上画的镜像（给 [39](39-oneplus-6t.md) §8.1 的测试镜像）时，构建加上覆盖层 `yocto/kas/paint-gpu.yml`：

```sh
make yocto-build WORK=$HOME/yocto/fajita-work KASFILE=yocto/kas/phone-fajita.yml SKU=oneplus-fajita-t9 KASEXTRA=yocto/kas/paint-gpu.yml
```

它设 `PHONE_PAINT_GPU_DEFAULT = "compose"`；`phone-sku` 按它把镜像的 `sku.json` 的 `paint_gpu` 写成 `compose`（`raster` 写成 `raster`，`off` 去掉板子的），之后和板子写 `compose` 走同一条路：Shell 核对、握手时交给渲染器，phone-paint 在屏上启动 GPU 合成，起不来或用着出错就用 CPU 画（§2 末尾、§7 的故意失败），`PHONE_PAINT_GPU` 照样优先（`PHONE_PAINT_GPU=0` 回到 CPU）。只有板子 `gpu = true` 的 SKU 能用：SKU 的 kas 覆盖这时写 `PHONE_SKU_GPU = "1"`，没有它或值不是 `off`、`compose`、`raster` 时 bitbake 解析就报错。不设时镜像的 `sku.json` 和 `tools/sku.py` 写的逐字节相同，SKU 的编号也不变（整机测试的套件按编号核对镜像）。只改这一个文件：Mesa 的包、固件在不在镜像里由机型的 kas 定（§5），和开关无关。`tools/sku_test.py` 的 `test_paint_gpu_default` 用配方自己的 Python 核对这些。chroot 的测试（[39](39-oneplus-6t.md) §13.4）换成套件自己的 `sku.json`，在那里开关不起作用，要用环境变量。2026-11-26 在 6T 的配置上构建了 `phone-sku`：不加覆盖层时和 `tools/sku.py` 写的一样，加了只多一行 `"paint_gpu": "compose"`；Mesa 两次都取自共享的 sstate，没有变。

渲染器继承 Shell 的环境，Shell 继承显示服务的。在开发镜像上：

```sh
# 只给 phone-display.service（重启后还在）：
mkdir -p /etc/systemd/system/phone-display.service.d
printf '[Service]\nEnvironment=PHONE_PAINT_GPU=1 PHONE_PAINT_TRACE=1\n' > /etc/systemd/system/phone-display.service.d/gpu.conf
systemctl daemon-reload && systemctl restart phone-display
# 只这一次开机、所有服务：写在内核命令行上
#   systemd.setenv=PHONE_PAINT_GPU=1
grep -e 'phone-paint: gpu' -e 'drawing on the CPU' /run/phone/gui.log
```

`phone-paint: gpu: composing with <渲染器>` 表示在 GPU 上合成；`...; drawing on the CPU` 说明为什么没起来；`...; drawing on the CPU from now on` 表示用着用着放弃了。

## 4. 在沙箱里启动

决定：GPU 在 `kms::drop_privileges()` **之后**启动，是渲染器发 `ready` 之前的最后一步，而不是最初设计说的之前。原因：

- `drop_privileges` 必须在进程只有一个线程时调用：Landlock 只约束调用它的线程和这个线程之后启动的线程。Mesa 初始化 EGL 后就启动线程（llvmpipe 几十个）；在沙箱之后启动，它们都在 Landlock 和 seccomp 里。
- GPU 需要的、得在沙箱之前拿到的，已经有了：card 在沙箱之前打开（GBM 设备建在它的 fd 上，不再打开第二个 card fd）；要固件的内核驱动在打开 card 时加载固件（msm：第一次打开设备时），所以 GPU 固件也在沙箱之前加载好。Mesa 的库和 drirc 在 `/usr` 下，Landlock 让它可读。
- 代价：在沙箱里 Mesa 读不到 sysfs 里的设备信息（Landlock），就按内核驱动名（`drmGetVersion`）找驱动：msm 本来就对应 freedreno；QEMU 上是 `virtio_gpu`，镜像里没有它的驱动，于是退到 kms_swrast。显示和 GPU 是两个设备的板子（kmsro：显示控制器加一块有自己 render node 的 Mali 或 PowerVR）要在沙箱之前打开 GPU 的 render node：和 card 一起打开（§4.1）。着色器不缓存到磁盘（什么都不能写）；每次启动重新编译 phone-paint 的四个着色器，在 GPU 上是几毫秒（softpipe 上**没测**：这发生在 `ready` 之前，§7.2 没有计这段时间）。
- QEMU 上看到的（§7）：Mesa 在 Landlock、seccomp、nobody 用户下启动；控制台上三行 `MESA-LOADER: failed to retrieve device information`（sysfs 被拒），一行 `virtio_gpu: driver missing`，之后 kms_swrast 接手。

### 4.1 显示和 GPU 是两个设备

问题：Mali（Panfrost）、PowerVR、Vivante（etnaviv）这些 GPU 的内核驱动只管渲染，有自己的 render node（`/dev/dri/renderD*`）；显示控制器是另一个 DRM 设备（Rockchip 的 VOP、全志的 DE、联发科、紫光展锐的 DPU、i.MX 的 LCDIF），只有 card。Mesa 的 kmsro 在 card 上起 GBM 时自己去打开 render node：在沙箱里 Landlock 不让打开 `/dev`，kmsro 就失败；而且 Mesa 只认它列表里的显示驱动（展锐的 `sprd` 在不在要看那一版 Mesa，**未查**），不认的退到 kms_swrast（软件，GPU 路径不要）。

做法（`paint/src/node.rs`）：

- 沙箱之前，只在要走 GPU 路径时：读出 card 和每个 `/dev/dri/renderD*` 的内核驱动名（`DRM_IOCTL_VERSION`）和所属的设备（sysfs 的 `/sys/dev/char/<主>:<次>/device`）。card 所属的设备自己有 render node（msm、virtio_gpu：显示和 GPU 是一个设备），就照旧在 card 上渲染；没有（只管显示），就用别的设备的第一个 render node（`vgem` 不算）；板子写了 `gpu_render` 就用那个驱动的，找不到就说出来、用 CPU 画。选中的 render node 和 card 一样一直开着，别的关掉。
- GBM 设备和 EGL 都建在 render node 上；GBM 表面要线性的缓冲（`GBM_BO_USE_LINEAR`：显示控制器不认识 GPU 的分块、压缩格式）。每块缓冲第一次出来时经 `gbm_bo_get_fd` 导出成 dmabuf，显示设备经 PRIME 导入（`DRM_IOCTL_PRIME_FD_TO_HANDLE`），建好帧缓冲就关掉导入的句柄（帧缓冲拿着缓冲），之后翻页和单设备一样（`kms::Display::import_fb`）。启动时的第一帧就走一遍导入和建帧缓冲，导入不了就退回 CPU 并写原因。
- 每帧翻页以前 `glFinish`：显示是另一个驱动，不一定等 GPU 在它导入的缓冲上的 fence。代价是上屏那一遍 CPU 和 GPU 不并行。
- GBM 给的缓冲带 modifier（分块、压缩）时建帧缓冲用 `ADDFB2` 带上它，显示设备不支持 modifier 就说出来；单设备也一样（以前不带 modifier 建，显示会按线性读）。线性和没有 modifier 的照旧用 `ADDFB`。
- 日志：`phone-paint: gpu: composing with <渲染器> on /dev/dri/renderD128 (panfrost), shown on /dev/dri/card0 (rockchip) through PRIME`。
- 测试：选哪个节点的规则（`node::tests`）；`/dev/null` 上线性和不线性的 GBM 表面都说出原因（`gpu::tests`）；整条路在 QEMU 上验证（§7，用 card 的第二个文件，§7.4）。真的两个设备**未上真机**。
- 没做的：显示控制器没有 IOMMU、只能扫描物理连续内存的板子（全志 DE2、i.MX8M 的 LCDIF 一类），GPU 驱动从 shmem 分出来的缓冲导不进去（导入或建帧缓冲时出错，GPU 路径退回 CPU）；那里要反过来，在显示设备上分配 dumb buffer、导出给 GPU 当渲染目标（Mesa 的 kmsro 就这样做），**未实现**。

### 4.2 辅助功能的外观

颜色反转、颜色校正和放大镜（设置 › 辅助功能，`paint/src/look.rs`）以前作用在 CPU 画好的画面上，开着其中一样时 GPU 路径关掉、交回 CPU。现在 GPU 路径不关：

- 颜色滤镜是上屏那一遍（`PRESENT`）的着色器：合成好的屏幕取出来以后、叠指针以前，照 `look.rs` 的 `Filter` 的算法改颜色：校正先转到线性 sRGB、乘校正矩阵（f32，和 CPU 用的同一个），量化到 `LIN_STEPS`（16384 级）再编码回 sRGB；反转按 BT.601 的亮度。指针和屏幕外的黑边不改（和 CPU 路径一样）。只换了滤镜、屏幕没变时也重新上屏一帧。
- 放大镜：画面照旧由 CPU 按放大后的比例重画、剪出焦点周围（`main.rs` 的 `magnified`，和 CPU 路径同一段代码：字按放大后的大小栅格化，是清楚的，不是把画面拉大），作为一张纹理代替合成的屏幕上屏，只传和上一张不同的那块；滤镜照样在上屏时加。开着时图层照旧合成，关掉以后不用整屏重传。没有做「GPU 把合成的屏幕放大」：那样快，但字是糊的或成块的，和 CPU 路径的画面不一样。
- 转场：开着任何一样时照旧不做（两条路一样）。
- 和 CPU 路径比（开发机的 llvmpipe）：反转逐字节相同；校正在 32768 个颜色里有 1 个差 1 级（GPU 的 `pow` 和 CPU 的在舍入的边上不同）；测试要求最多差 1 级、不同的像素不到 0.5%（`gpu::tests::the_filters_are_the_cpus`）。放大镜的画面和 CPU 路径逐字节相同，加滤镜最多差 1 级（`main.rs` 的 `the_gpu_shows_a_look_as_the_cpu_path`，图层在 GPU 上合成）。手机 GPU 上 `pow` 的精度**未上真机**。

### 4.3 翻页被拒（一加 6T 上的 EINVAL）

一台 6T 在 Nura 上跑 chroot 时（[41](41-design-gaps.md) §11），phone-paint 的 dumb buffer 翻页报 `page flip: Invalid argument (os error 22); drawing in place`，原因没查出来；Nura 的内核还加载不了 `a630_sqe.fw`（[39](39-oneplus-6t.md) §14.4，Kario 自己的 6T 镜像带 a630 的固件）。照内核的 legacy 翻页（`drm_mode_page_flip_ioctl`，原子驱动经 `drm_atomic_helper_page_flip`）的要求核对了 `kms.rs`：

| 要求 | `kms.rs` |
|------|------|
| 只带认识的标志；带 `DRM_MODE_PAGE_FLIP_EVENT` 就要读事件 | 只带 `EVENT`，`wait_flip` 读事件 ✓ |
| 帧缓冲不比 CRTC 当前的模式小（`drm_crtc_check_viewport`，主平面转了 90°/270° 时按转过的比；不够是 ENOSPC） | dumb buffer 按 `set_crtc` 用的模式建 ✓ |
| 格式和 CRTC 上现在的帧缓冲一样（不一样是 EINVAL） | 两块都是 XRGB8888 ✓；GPU 的第一帧用 `set_crtc`，可以换 ✓ |
| CRTC 开着（关着时原子驱动报 EINVAL：`disabled, rejecting legacy flip`、`requesting event but off`） | 灭屏时不翻页 ✓ |
| 一次只能挂一个翻页（没完成又翻是 EBUSY） | 等事件最多 100 ms，超时以后再翻撞上 EBUSY 就不再翻页 ✗ → 现在等它完成（最多 0.5 s；超时放弃的翻页欠着它的完成事件，来晚的事件只算它的，[28](28-battery.md) §7）再翻一次 |
| 不能要求整个重设模式（原子驱动的 check 认为要 modeset 时 EINVAL：`requires full modeset`） | 用户态看不见，要看内核的 drm 调试日志 |
| 命令模式的面板（6T 是）：翻页完成的事件在一帧传完时来；原地写要 `DIRTYFB` 才刷新 | 原地写以后调 `dirty_framebuffer` ✓ |
| CRTC 是这个连接器的编码器能用的（`possible_crtcs`） | 连接器没有当前编码器时取了设备的第一个 CRTC ✗ → 现在取编码器能用的第一个 |

改了的：CRTC 照 `possible_crtcs` 选（`the_crtc_is_one_the_connector_can_use`，旧代码上失败）；EBUSY 时等挂着的翻页完成再翻一次；第一次被拒时用 `set_crtc` 把这一帧放上去、下一帧再翻，连着第二次被拒才不再翻页（原地写，GPU 路径每帧 `set_crtc`；`a_refused_flip_is_tried_once_more`，旧代码上失败）；GPU 的缓冲带 modifier 时用 `ADDFB2`（§4.1）。这些不一定就是 6T 上的原因：上表里看得见的要求 `kms.rs` 本来就满足，最可能的是 msm 的原子 check（7.1-rc1 的内核，命令模式、带 DSC 的面板）认为要整个重设，或者 Nura 的控制台（fbdev）留下的平面状态；在 6T 上**未上真机**。

诊断：第一次被拒时 phone-paint 在日志里写几行 `phone-paint: page flip: ...`（`kms::Display::flip_facts`、`describe`），不用打开内核嘈杂的 drm 调试：错误、驱动和几项能力（异步翻页、vblank 事件带不带 CRTC、单调时间戳、ADDFB2 modifier）；CRTC 现在的模式（和 phone-paint 设的比）、帧缓冲、`ACTIVE`；连接器、`DPMS`、`panel orientation`；CRTC 上的帧缓冲和要翻到的帧缓冲的尺寸、格式、modifier、pitch（`GETFB2`）；CRTC 的主平面和在它上面的平面：类型、所在的 CRTC、帧缓冲、`rotation`、认不认 XRGB8888；翻页要求了几次、完成几次、几个超时放弃了还欠着完成事件、有没有挂着的。然后几行 `likely:`：违反了上表哪条（尺寸、格式、CRTC 关着、模式被别的程序改了、CRTC 上是别人的帧缓冲、主平面转了 90°、主平面在别的 CRTC 上、挂着翻页），都没有就说是驱动自己的 check 拒的、怎么让内核说出原因（[39](39-oneplus-6t.md) §14.4 的命令）；`also:` 一行列出别的程序留在这个 CRTC 上的平面（光标、叠加层，会盖在画面上）。为了看见主平面和 `ACTIVE`，这时临时打开 universal planes 和 atomic 两个客户端能力，看完关掉。单元测试 `a_refused_flip_says_why` 用编出来的情况核对每条 `likely`。

## 5. 镜像

`yocto/kas/phone-qemuarm64.yml` 的 `gpu` 一段把 Mesa（MIT）配成 `gallium opengl egl gles gbm expat xmlconfig zlib`：DRM 上的 EGL、GLES 2 和 GBM，不要 X11、Wayland、Vulkan，不要 LLVM。它的 gallium 驱动是 softpipe（总有），显示没有 GPU 驱动时经 kms_swrast 用它；手机的机型再加自己 GPU 的驱动（6T 的 `phone-fajita.yml`：freedreno）。phone-paint 推荐 Mesa 的包（`RRECOMMENDS`：libegl-mesa、libgles2-mesa、libgbm、mesa-megadriver），而不是依赖：没有它们就用 CPU 画。

| QEMU 镜像多出来的（2026-11-21 构建，Mesa 26.0.5） | 字节 |
|------|------|
| `libgallium`（softpipe、kms_swrast 和着色器编译器） | 16 724 600 |
| `libegl-mesa` | 330 207 |
| `libgbm` | 200 823 |
| `libdrm`（镜像里原来没有别的用它） | 133 265 |
| `mesa-megadriver`（GBM 后端 `dri_gbm.so`） | 74 855 |
| `libgles2-mesa` | 67 474 |
| 合计 | 17 531 224（17.5 MB） |

- 为什么不要 llvmpipe：光它的 `libLLVM` 就约 116 MB，是 Mesa 其余部分加起来的好几倍，而且默认本来就不用 llvmpipe（§3）。softpipe 慢（QEMU 的显示上 TCG 下 480×640 一帧约半分钟，§7.2），只对测试模式有影响。
- 许可：Mesa 和 libdrm 是 MIT；没有 GPLv3（`INCOMPATIBLE_LICENSE` 让构建通过了；`scripts/yocto-check.py` 通过）。
- 产品镜像：`kario-internal` 的 `kas/product.yml` 包含这个 kas 文件，所以 Mesa 一样；这一批**没有构建**那个镜像（它的构建目录不归这一批）。
- 网页覆盖层（`yocto/kas/web.yml`）：WPE 经 Mesa 的 EGL 在 Wayland 平台上渲染，所以它把 `gpu` 一段去掉的加回去（Wayland、X11、Vulkan、Zink、virgl、视频编解码：这个发行版下菜谱的默认值）。**未验证**：这一批没有构建网页镜像。
- 一加 6T：`phone-fajita.yml` 追加 `freedreno`。2026-11-25 构建的 6T 镜像（kas 和现在的一样）的 manifest 里有 `libgallium`、`libegl-mesa`、`libgles2-mesa`、`libgbm1`、`mesa-megadriver`（Mesa 26.0.5）和 `libdrm2`；`libgallium-26.0.5.so` 19 091 256 字节（比 QEMU 的多出来的是 freedreno），它认的 GPU 名字里有 `FD630`；`/usr/lib/gbm/dri_gbm.so` 在。GPU 固件：`linux-firmware-qcom-adreno-a630` 的 `qcom/a630_sqe.fw`、`qcom/a630_gmu.bin`，`firmware-oneplus-sdm845` 的 `qcom/sdm845/OnePlus/enchilada/a630_zap.mbn`（就是 6T 设备树 `gpu_zap_shader` 的 `firmware-name`）。内核：`CONFIG_DRM_MSM=y`（编进内核，没有要签名的模块），msm 的 `separate_gpu_kms` 默认关，显示和 GPU 是一个 DRM 设备（card 和 `renderD128`），phone-paint 在 card 的 fd 上渲染（§4.1 的 `node::choose`）。沙箱：渲染器由以 root 运行的 `phone-display.service` 起，单元不限设备，card 在降权以前打开；seccomp 只拦列出来的系统调用（不拦 ioctl、mmap），freedreno 要的 msm 的 ioctl 在 card 上不用认证（内核里只带 `DRM_RENDER_ALLOW`）。都**未上真机**。它的 tune（cortexa75-cortexa55）和 qemuarm64 的（cortexa57）不同，所以两份 Mesa 在共用的 sstate 里不会互相覆盖包。机主构建：[39](39-oneplus-6t.md) §4，然后在镜像的 manifest 里 `grep -e mesa -e gallium`。
- SM6115 / SM4250 的板子（`kario-qcom`，Adreno 610）和 qemuarm64 用同一个 tune：只在那个机型的配置里加 freedreno，会出现同一架构的两个不同 Mesa 包，所以那里要按机型构建 Mesa（`PACKAGE_ARCH`）；**未做**。

## 6. 在手机上

**未上真机。** 一加 6T 上要确认的（freedreno 加载、在 card 的 fd 上起 EGL/GBM、按面板刷新率翻页、带 freedreno 的沙箱、设 `PHONE_PAINT_GPU=1` 后没有退步、用录下的帧跑 `phone-paint --bench`）和命令见 [39](39-oneplus-6t.md) §8.1。测试镜像用 §3 的构建开关构建时，开机就走 GPU 路径，不用 drop-in 设 `PHONE_PAINT_GPU=1`。某个机型要不要默认用 GPU 路径，按那些数字定（帧时间和功耗，对比 CPU 路径的脏矩形，[37](37-motion.md) §0），定了就写进它的板子文件的 `paint_gpu`（§3）。

## 7. QEMU

### 7.1 怎么跑

```sh
make yocto-gui-gpu WORK=$HOME/yocto/gui-work      # 依次开机四次，约五个小时
make yocto-gui GUI_ENV='PHONE_PAINT_GPU=soft PHONE_SCREEN_OFF=3600' GUI_WAIT=10   # 只跑 GPU 那次
make yocto-gui-gpu WORK=$HOME/yocto/gui-work GUI_GPU_RUNS=gpu-split   # CPU 那次（参照）加分开渲染那次
```

`yocto/qemu-gui-gpu.sh` 用 `phone.gui=1` 把镜像开机四次，每次按同样的键（`yocto/qemu-gui-drive.py`，[13](13-prototype.md) §5.1）：用 CPU 画（`gui-shots-cpu`）、经 softpipe 在 GPU 上合成（`PHONE_PAINT_GPU=soft`，`gui-shots-gpu`）、同样合成但在显示的那个 DRM 文件以外渲染（`PHONE_PAINT_GPU_RENDER=/dev/dri/card0`：card 的第二个文件，缓冲经 PRIME 交给显示的文件翻页，§4.1、§7.4；`gui-shots-gpu-split`）、GPU 在第 8 帧故意失败（`PHONE_PAINT_GPU_FAIL=8`，`gui-shots-gpu-fail`）；这些变量经内核命令行上的 `systemd.setenv=` 传给渲染器（`GUI_ENV`）。然后 `yocto/gui-compare.py` 拿每次 GPU 开机的截屏和 CPU 那次比：某个通道差超过 2 级（gpu.rs 的 `LEVELS`）的像素才算；状态栏那几行（时钟）、屏幕上跟着这次运行的时间走的东西周围的框里（待机的时间和日期、通话的时长、相机合成画面上的文字、聊天演示消息的时间：`--volatile 名字:X0,Y0-X1,Y1`）、框外文字光标大小的框（闪烁）和扫描线那张（按时钟跑）里的差别只报告、不算失败。GPU 那两次还要在 `ready` 的 features 里有 `gpu`（`PHONE-GUI-GPU`）、要求过也完成过翻页（`PHONE-GUI-FLIPS`，取自每行 `PHONE_PAINT_STEPS` 的 `flips`）、至少合成过一步转场、没有回退；分开渲染那次的 `gpu:` 一行还要说在那个节点上渲染、经 PRIME 上屏；故意失败那次必须回退。

TCG 下的 softpipe 一帧约半分钟，而渲染器只画它画的时候到的那些帧里的最后一帧（37 §5 第 9 条）：隔一秒半按的键合成了几帧，翻页后面跟着按键时它的转场也被合掉。所以 `GUI_WAIT` 大于 1 时驱动脚本每个画面都轮询截屏，等到状态栏以下的画面在渲染器的四帧时间里没变过、或者只在两张之间闪（文字光标）（帧的时间看控制台上它的 `PHONE_PAINT_STEPS` 行）；自己会变或者跟着运行时间走的框（时钟、相机的文字）不算，和比对不算的是同一些框，比对从驱动脚本的 `PHONE-GUI-VOLATILE` 行拿；整屏都在变的扫描线等十帧。`PHONE_SCREEN_OFF=3600` 让屏幕在两次按键之间一直亮着（否则 Shell 的超时会让它变暗，下一个键只把它点亮）。

每次开机的时钟都从同一个时刻开始（`PHONE_GUI_RTC`，QEMU 的 `-rtc base=`；默认是脚本开始那天 UTC 的 04:00，SKU 所在地区（中国）的中午）：镜像不同步时钟，客户机的时间就是开机时主机的时间，而一次完整运行五个多小时，前后几次开机可能隔着午夜，跟着日期走的画面（日历的「今天」，焦点从那里开始滑）就不一样了（§7.5）。最后核对每次开机时内核从 RTC 拿到的日期都一样（`PHONE-GUI-CLOCK`）。`GUI_GPU_RUNS=` 只跑 `gpu`、`gpu-split`、`gpu-fail` 里的几次，CPU 那次总跑（是参照）。

### 7.2 结果（2026-11-21）

在开源 QEMU 镜像上跑 `make yocto-gui-gpu WORK=$HOME/yocto/gui-work`（qemuarm64：TCG、Cortex-A57、4 个 CPU、2 GB、`virtio-gpu-pci` 480×640），同时构建机上还有别的任务的 QEMU 在跑：通过，用了 2 小时 47 分。这些时间是模拟器里软件光栅器的，**不是手机 GPU 的**：说明这条路走得通，说明不了它多快。

| | CPU | GPU（softpipe） | GPU 在第 8 帧失败 |
|---|---|---|---|
| 谁在画 | CPU | `gpu: composing with softpipe`（Mesa 的加载器找不到 `virtio_gpu` 驱动，用 kms_swrast），`ready` 的 features 有 `… sandboxed landlock seccomp gpu` | 到第 8 帧都一样，开机后 384 秒：`failing on purpose at frame 8 (PHONE_PAINT_GPU_FAIL); drawing on the CPU from now on` |
| 要求的、完成的翻页 | 1162、1161 | 242、241 | 1216、1215 |
| 转场的步 | 488（p50 1.5 ms） | 7（p50 20.2 s，p95 21.0 s） | 491（p50 1.5 ms） |
| 交出一帧（合成并上屏） | p50 6.8 ms，p95 11.0 ms | p50 31.0 s，p95 32.1 s | p50 7.0 ms，p95 11.6 ms |
| CPU 画各层 | p50 4.8 ms | p50 5.9 ms | p50 5.2 ms |
| 推入页面的滑动（截 30 张） | 1.3 秒里 6 张不同的 | 224 秒里 6 张 | 10.7 秒里 3 张 |
| 鼠标点击和滚轮、键盘插拔 | ✓ | ✓ | ✓ |
| 截屏和 CPU 那次比 | — | 17 张都对上：10 张一样，5 张在各自的易变框里，1 张是光标加它的框，扫描线 | 17 张都对上：10 张一样，6 张在各自的框里，扫描线 |
| 用时 | 5 分半 | 2 小时 23 分 | 17 分半 |

它前面那次完整运行（同一天，数字差不多：GPU 那次 248 次翻页、9 步转场）在一张上没过，两次比对都是 `16-chat-candidates`：聊天的演示消息带的是应用启动时间减二十分钟，每次开机都不一样。现在驱动脚本给它划了一个框，比对先去掉画面的框再找光标（`yocto/gui-compare-test.py`）。再之前那几次跑出来的是驱动脚本自己的毛病（一个名字被遮住、从内核时间戳里读 ready 行的 features、半分钟一帧还没出来就截屏、画面还没追上按键就截屏）；有一次故意失败的开机是被外面杀掉的（主机上所有 QEMU 同时没了），不是检查本身。

### 7.3 virtio-gpu-gl（virgl）

**没跑。** QEMU 的 `virtio-gpu-gl-pci` 经 virglrenderer 把客户机的 GL 交给主机的 GPU；镜像就要 Mesa 的 virgl 驱动（`PACKAGECONFIG:append:pn-mesa = " virtio"`）和 runqemu 的 `egl-headless`（或者带 SDL、GTK 的 `gl`）。这次构建的 qemu-system-native 已经带 virglrenderer 和 epoxy，不用多构建；失败在主机上：这台构建机没有 DRM render node（唯一的显示芯片是服务器的 mgag200，card0，没有 render node），构建容器里没有 `/dev/dri`，`egl-headless` 要 render node（runqemu 会检查），`gl` 要显示器。在有 GPU 的机器上，一个覆盖层写这两行，再加 `QB_GRAPHICS:forcevariable = "-device virtio-gpu-gl-pci,xres=480,yres=640"` 和 runqemu 的 `egl-headless`，就能让 phone-paint 在主机的 GPU 上跑（`PHONE_PAINT_GPU=1`：除非主机的 GL 是软件的，virgl 不算软件光栅器）；它同样说明不了手机的 GPU。

### 7.4 QEMU 上的 render node

virtio-gpu 在 6.18 的内核里总有 render node：`virtgpu_drv.c` 的 `driver_features` 无条件带 `DRIVER_RENDER`，2D 的设备（`-virgl`）也一样，所以镜像里有 `card0` 和 `renderD128`，都是 `virtio_gpu`。但在 render node 上 Mesa 什么都画不了，2026-11-23 在镜像里试过（`make yocto-gui GUI_ENV='PHONE_PAINT_GPU=soft PHONE_PAINT_GPU_RENDER=/dev/dri/renderD128 PHONE_SCREEN_OFF=3600'`）：phone-paint 在沙箱之前打开了 `renderD128`，Mesa 退到 kms_swrast，控制台上两行 `KMS: DRM_IOCTL_MODE_CREATE_DUMB failed: Permission denied`，然后 `phone-paint: gpu: EGL: EGL failed to allocate resources for the requested operation.; drawing on the CPU`，之后照常由 CPU 画、翻页。原因：

- Mesa 给 `virtio_gpu` 的驱动是 virgl，要 QEMU 的 3D 设备（`virtio-gpu-gl`，主机上要 virglrenderer 和 render node，§7.3），镜像里也没有这个驱动；
- 找不到驱动时 GBM 退到 kms_swrast，它用 dumb buffer 分配和映射缓冲（`DRM_IOCTL_MODE_CREATE_DUMB`、`MAP_DUMB`），而内核不让 render node 用这两个（`drm_ioctl.c` 里它们没有 `DRM_RENDER_ALLOW`，render node 上返回 EACCES）；再退的纯 swrast 也在设备上分配 dumb buffer；
- softpipe 不能导入 dmabuf，所以反过来（在 card 上分配、导入 render node 上的 EGL）也不行；llvmpipe 能（经 udmabuf），但镜像里没有它（§5）。

所以测试模式在 QEMU 上用 card 的第二个文件（`PHONE_PAINT_GPU_RENDER=/dev/dri/card0`）当「另一个设备」：两个文件的 GEM 句柄互不相通，缓冲只能导出成 dmabuf、经 PRIME 导进显示的那个文件才能建帧缓冲、翻页，和真的两个设备走同一段代码（导出、导入、建帧缓冲、在显示的文件上翻页、读它的事件，缓冲的来回）。不同的地方：内核那边是同一个驱动，导入时认出是自己导出的、拿回同一个对象（`virtgpu_gem_prime_import`），不经过两个驱动之间的 dma-buf 映射、也没有连续内存和分块格式的问题（§4.1 末尾）。要在 QEMU 上真的用 render node，就要 virgl（§7.3）。

### 7.5 结果（2026-11-23、24：分开渲染）

这一批的镜像跑了 `make yocto-gui-gpu WORK=$HOME/yocto/gui-work`，四次开机共 5 小时 33 分（CPU 5 分半，GPU 2 小时 29 分，分开渲染 2 小时 40 分，故意失败 17 分），构建机上同时还有别的任务的 QEMU。三次 GPU 开机都做到了要求的（下表），比对却没过：分开渲染和故意失败两次的 `12-calendar-glide` 各有 5649 个像素和 CPU 那次不同。原因不在 GPU 路径：CPU 那次 22:52 开机，那两次在午夜以后，日历的「今天」从 23 号变成了 24 号，焦点从今天出发滑，停在 26 号而不是 25 号；故意失败那次走到这个画面时早已退回 CPU 画，差得一模一样。镜像不同步时钟，客户机的时间就是每次开机时主机的时间。改法：每次开机的时钟从同一个时刻开始，脚本核对日期一样（§7.1，`PHONE_GUI_RTC`、`PHONE-GUI-CLOCK`），不是把日历划成易变的框。改了以后重跑了要重跑的两组：CPU 加故意失败（17 张都对上，日历一样），CPU 加分开渲染（17 张都对上，日历一样；分开渲染那次 251 次翻页、完成 250，9 步转场，交出一帧 p50 31.5 s，用了 2 小时 31 分）。GPU 那次的比对本来就过了（它和 CPU 那次都在午夜以前）。

| | GPU（softpipe） | 分开渲染（softpipe，card 的第二个文件） | GPU 在第 8 帧失败 |
|---|---|---|---|
| 谁在画 | `gpu: composing with softpipe` | `gpu: composing with softpipe on /dev/dri/card0 (virtio_gpu), shown on /dev/dri/card0 (virtio_gpu) through PRIME` | 到第 8 帧都一样，开机后 375 秒：`failing on purpose at frame 8 …; drawing on the CPU from now on` |
| 要求的、完成的翻页 | 244、243 | 247、246 | 1236、1235 |
| 转场的步 | 7（p50 20.9 s） | 6（p50 21.4 s） | 489（p50 1.5 ms） |
| 交出一帧（合成并上屏） | p50 31.6 s，p95 37.8 s | p50 32.2 s，p95 52.5 s | p50 6.5 ms，p95 11.6 ms |
| 推入页面的滑动（截 30 张） | 224.9 秒里 6 张不同的 | 106.7 秒里 4 张 | 10.7 秒里 3 张 |
| 鼠标点击和滚轮、键盘插拔 | ✓ | ✓ | ✓ |
| 截屏和 CPU 那次比 | 17 张都对上 | 16 张（日历，见上）；时钟固定以后 17 张都对上 | 16 张（日历）；时钟固定以后 17 张都对上 |

CPU 那次 1156 次翻页、完成 1155。四次开机都没有翻页被拒（日志里没有 `page flip` 的行）。分开渲染那次每帧多一次导出、导入（只在缓冲第一次出来时）和一次 `glFinish`，在 softpipe 上看不出差别；这说明不了两个真的设备之间怎么样（§7.4 末尾）。

## 8. 风险与未决

- Mesa 和 freedreno 在 6T 主线内核上怎么样：试了才知道（[39](39-oneplus-6t.md) §8.1）。
- 功耗：唤醒 GPU、每帧合成整屏的内存流量，可能比 CPU 只画脏矩形更费电；什么都没变时不合成，但每个变了的帧都合成并上屏整屏（两遍全屏）；把它们限制在脏矩形里**未实现**。
- 显示和 GPU 是两个设备（kmsro）：在 GPU 的 render node 上渲染（§4.1），只在 QEMU 上用一个设备的两个文件验证过；显示控制器要连续内存的板子还要反过来分配，**未实现**。
- 设置 › 辅助功能的外观：滤镜在 GPU 上，放大镜的画面还是 CPU 按放大的比例重画（§4.2），开着放大镜时 CPU 的活和 CPU 路径一样多。
- 翻页被拒：6T 上 EINVAL 的原因没查出来，第一次被拒时的日志该说清楚（§4.3）。
- 视频帧当 dmabuf 纹理：**未实现**（[23](23-renderer-protocol.md) §9）。

## 9. L2：GPU 光栅化

> 状态：已实现，可选（设 `PHONE_PAINT_GPU=raster`；默认仍是 CPU 光栅，`PHONE_PAINT_GPU=1` 仍只是 GPU 合成）。正确性在开发机的 llvmpipe 上验证：GPU 画的每种操作和 CPU 逐字节相同；**未上真机**，值不值得、帧时间、功耗要在一加 6T 上测（[39](39-oneplus-6t.md) §14.4）。详见 [37](37-motion.md) §5 第 10 条、§2.5。

| `Op` | GPU 实现（`paint/src/raster.rs`） | 和 CPU 比 |
|------|----------|------|
| `Clear` | scissor 内 `glClear` | 逐字节相同 |
| `Rect` | 每个操作一个四边形，片元着色器按 tiny-skia 的定点覆盖率（1/256 像素）算边 | 逐字节相同 |
| `Round`、`Circle` | CPU 按 tiny-skia 的边算出每个 1/4 像素行的跨度（`paint/src/scan.rs`，每行几次整数运算），片元着色器按 4×4 超采样算覆盖率；不用 SDF：真实形状和 tiny-skia 拆成的折线差 1–4 个采样 | 逐字节相同；伸出图层、tiny-skia 会裁剪的交给 CPU |
| `Polygon` | CPU 画在透明底上，传成纹理叠上去（界面里只有充电的闪电） | 最多差 3 级 |
| `Fade` | 每行一个四边形，用 CPU 算的每行透明度 | 逐字节相同 |
| `Text` | 8 位字形图集（GLES 2 用 `LUMINANCE`，1024×1024），覆盖率就是 ab_glyph 给 CPU 的，按字体、字形、字号和笔、基线的小数部分存；第一次显示时栅格化（中日韩字也是），满了按 LRU 腾出相邻的几排架子；每个文字操作一批四边形（GLES 2 没有实例化） | 逐字节相同；比图集大的字交给 CPU |
| `Image` | CPU 缩放好的像素（`Patch`）当纹理，NEAREST，按键缓存 | 逐字节相同 |
| `Layer` | 那几行当纹理，按键缓存 | 逐字节相同 |
| `Body` | 正文的段是 FBO，按段画，合成照 L1 | 逐字节相同 |

- 混合不用 `glBlendFunc(GL_ONE, GL_ONE_MINUS_SRC_ALPHA)`：它和 tiny-skia 的 lowp 整数在半透明、抗锯齿的像素上差 1 级。片元着色器读下面的像素，照 tiny-skia 和 `canvas.rs` 的整数算：有 `GL_EXT_shader_framebuffer_fetch` 时用 GLSL ES 3.00 的 `inout`，没有时每个操作先拷一份下面的像素（`glCopyTexSubImage2D`）。
- 不开 `GL_FRAMEBUFFER_SRGB`；四边形就是像素矩形，覆盖率按像素中心用定点算，不靠插值。
- 作业：脏矩形算法不变（`Layers::raster`），要重画的每个矩形一个作业，GPU 用 scissor 画；CPU 不留图层的像素。
- 测试：每种操作随机比对（fetch 和拷贝两条路），跨度和 tiny-skia 的覆盖率逐像素比，字形图集的换出，成对的帧和动画每一步逐层比；`phone-paint --bench` 对帧文件每一步逐层比，超过容差报错，并列出每种操作的数量、GPU 画了多少、CPU 路径画它们的时间。快照和无头自测照旧走 CPU。开发机上 Mesa 23.1 的 llvmpipe 在两个上下文同时拷贝纹理时会崩，测试里的 GPU 上下文因此轮流用（渲染器本身只有一个上下文）。
- llvmpipe 上 L2 比 L1 和 CPU 路径都慢，说明不了手机（[37](37-motion.md) §2.5）。
- `scan.rs` 移植自 tiny-skia 的边的计算，保留了 tiny-skia 的 BSD-3-Clause 声明。

## 10. 上真机前的清单

> 状态：下面「做了」的都只在开发机的 llvmpipe 和 QEMU 上验证过（QEMU 是软件光栅器），**没有一项上过真机**。

做了的：

| 项 | 怎么验证的 |
|------|------|
| L0、L1、L2 | llvmpipe 上逐字节或在 2 级以内比对（§2、§9）；L0、L1 在 QEMU 的显示上（§7） |
| 在沙箱里起 Mesa | QEMU 上 Landlock、seccomp、nobody（§4） |
| 按板子定默认（`paint_gpu`），环境变量优先 | `tools/sku_test.py`、`shell` 的 `TestPaintGPU`、`gpu::tests`（§3） |
| 显示和 GPU 是两个设备：沙箱之前开 render node，经 PRIME 上屏，线性缓冲，`glFinish` | 选节点的单元测试；QEMU 上 card 的第二个文件（§4.1、§7.4、§7.5） |
| 带 modifier 的 GPU 缓冲用 `ADDFB2` | 单元测试（判断）；没有真的带 modifier 的缓冲试过 |
| 颜色滤镜、放大镜不让 GPU 让开 | llvmpipe 上和 CPU 路径比（§4.2） |
| 翻页被拒：再试一次、挂着的等完、第一次被拒时的诊断 | 单元测试（§4.3）；QEMU 上没有被拒过 |
| 中途出错交回 CPU | QEMU（`PHONE_PAINT_GPU_FAIL`，§7） |
| 门槛的计量：帧时间、按键和触摸到上屏（翻页完成）、冷启动，三条路各在整屏和产品屏幕上跑一套固定操作（[39](39-oneplus-6t.md) §8.1 末尾） | 用合成时间戳的单元测试（`paint/src/perf.rs`、`play.rs`，`shell/latency_test.go`）；开发机上无头跑过六次（`tools/perf/host.sh`，llvmpipe，2026-11-26：只说明能跑，数字不是手机的）；套件的 `perf` 一步只做过计划和假的 systemctl 的测试（`tools/phone_chroot_test.py`） |

每类芯片还要什么：

| GPU（Mesa 驱动） | 显示 | 这里还缺的 | 状态 |
|------|------|------|------|
| Adreno 6xx/5xx（freedreno）：一加 6T 的 630、SM6115/SM4250 的 610、红米 Note 4X 的 506 | msm，和 GPU 是一个设备（card 上渲染） | 镜像里的 freedreno（6T 的镜像有，2026-11-25 构建过；SM6115/SM4250 要按机型构建 Mesa，§5）；GPU 固件（`a630_sqe.fw`、`a630_gmu.bin`、zap shader：Kario 自己的 6T 镜像带，Nura 的坏了，[39](39-oneplus-6t.md) §14.4）；6T 上翻页的 EINVAL（§4.3） | **未上真机** |
| Mali Midgard/Bifrost/Valhall（Panfrost）：展锐 T610 的 G52、Rockchip、联发科、全志 H6 | 另一个设备（sprd、rockchip、mediatek、sun4i-drm） | 板子写 `gpu_render = "panfrost"` 或靠自动选；镜像要 Mesa 的 `panfrost`；显示控制器没有 IOMMU（全志）要在显示设备上分配缓冲（§4.1 末尾，**未实现**）；主线的展锐显示驱动（`sprd`，为 SC9863A 写的）支不支持 T610 **未查** | **未上真机** |
| Mali Utgard（Lima）：Mali-400、450 | 另一个设备 | 片元着色器只有 fp16 的 mediump、没有 20 位的 highp，GPU 路径起不来，用 CPU（§2：`HIGHP_BITS`） | 不支持（设计如此） |
| PowerVR（展锐 T606/T612 的 GE8322、联发科一些） | 另一个设备 | Mesa 里只有 Vulkan 驱动（`powervr`，早期），GLES 要经 Zink：phone-paint 的 EGL/GLES 2 能不能在它上面起来**未试**；厂商的闭源驱动不在考虑里 | **未上真机** |
| Vivante（etnaviv）：i.MX | 另一个设备（imx-drm、imx-lcdif） | `gpu_render = "etnaviv"`；i.MX8M 的 LCDIF 要连续内存（同 Lima 那行的分配问题）；highp 够不够**未查** | **未上真机** |
| virtio-gpu（QEMU） | 一个设备 | 只有软件光栅器（测试模式）；virgl 要有 GPU 的主机（§7.3） | QEMU ✓ |

没试过的（都要真机）：任何一块手机 GPU 上的帧时间、功耗、合成和 CPU 路径比的数；L2 在分块渲染的 GPU 上值不值得（[37](37-motion.md) §5 第 10 条）；真的两个设备之间的 PRIME（连续内存、分块格式、隐式同步，§4.1）；命令模式面板上的翻页（§4.3）；GBM 缓冲底下切刷新率（[37](37-motion.md) §5 第 8 条）；放大镜开着时 CPU 重画放大画面的耗时（和 CPU 路径一样）。在 6T 上怎么查、查完怎么把 `paint_gpu` 打开：[39](39-oneplus-6t.md) §8.1、§14.4。

6T 上门槛的数（`$W perf` 的 `perf.txt`，[39](39-oneplus-6t.md) §8.1 末尾）：**未上真机**。量了以后在这里写一行：日期、每条路整屏和产品屏幕的帧时间、按键、触摸的 p95，最慢的冷启动，过没过。
