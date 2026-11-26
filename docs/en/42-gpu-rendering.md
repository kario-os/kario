> English translation of [the Chinese original](../42-gpu-rendering.md). The Chinese text is authoritative; keep both in sync.

# GPU rendering: frames to the panel, compositing, rasterizing

> Status: compositing on the GPU (L1, which includes L0, frames to the panel through EGL/GBM) is implemented and optional: with `PHONE_PAINT_GPU=1`, or without it where the board's `paint_gpu` is `compose` (§3; every board is `off` for now); the CPU path stays the default and the fallback; the build switch `PHONE_PAINT_GPU_DEFAULT` (`yocto/kas/paint-gpu.yml`) makes an image that composes on the GPU by default, the board unchanged (§3, for the OnePlus 6T's test image). On QEMU it was verified with a software GPU (Mesa's softpipe behind kms_swrast, QEMU's 2D virtio-gpu): the GBM surface, page flips, inside the sandbox, the same pictures as the CPU path, falling back to the CPU mid-run (§7). **Not tried on a phone** (no Adreno, Mali or PowerVR tried; the OnePlus 6T's list is in [39](39-oneplus-6t.md) §8.1). Rasterizing on the GPU (L2) is implemented too, optional (`PHONE_PAINT_GPU=raster`): on the developer machine's llvmpipe every kind of op the GPU paints is byte for byte the CPU's, **not tried on a phone** (§9). Where the display and the GPU are two DRM devices it renders on the GPU's render node and the display's device flips to the buffers (§4.1; verified on QEMU with a second file of the card, two real devices **not tried on a phone**); the colour filters and the magnifier are done on the GPU path, which no longer steps aside for them (§4.2); a refused page flip logs the CRTC, planes and framebuffers the kernel shows (§4.3). What was done before trying a phone, and what each family of chips still needs: §10.
> Scope: the inside of phone-paint ([23](23-renderer-protocol.md) §2: the protocol `phone.frame/1` does not change; while the GPU composes, `ready`'s `features` has `gpu`). How it works in detail and its costs on llvmpipe: [37](37-motion.md) §5 item 8 and §2.4.

## 1. Where it stands

| Part | State | Where |
|------|------|------|
| L0: frames to the panel through a GBM surface, EGL and KMS page flips | ✓ the last pass of L1; verified on QEMU (softpipe), **not tried on a phone** | `paint/src/gpu.rs` (`show`), `paint/src/gbm.rs`, `paint/src/kms.rs` (`scan_out`) |
| L1: the CPU draws the layers, the GPU uploads what changed, composes, runs transitions and puts the frame on the panel | ✓ optional (`PHONE_PAINT_GPU=1`); pictures verified headless on llvmpipe and on QEMU's display through softpipe | [37](37-motion.md) §5 item 8 |
| A software rasterizer as a test bed | ✓ `PHONE_PAINT_GPU=soft` (tests only, §3) | `gpu::Ask` |
| Starting inside the sandbox | ✓ decided and verified on QEMU (Landlock, seccomp, nobody), §4 | `paint/src/main.rs` |
| Mesa in the images | ✓ open-source QEMU image built (softpipe, no LLVM, 17.5 MB); the product image takes the same kas section, **not built** in this batch; the OnePlus 6T's configuration adds freedreno, built on 2026-11-25 (§5), **not tried on a phone** | `yocto/kas/phone-qemuarm64.yml` (`gpu`), `yocto/kas/phone-fajita.yml` |
| The QEMU check | ✓ `make yocto-gui-gpu` (§7) | `yocto/qemu-gui-gpu.sh`, `yocto/gui-compare.py` |
| virtio-gpu-gl (virgl) | **not run**: this build machine has no render node (§7.3) | — |
| L2: rasterizing on the GPU | ✓ optional (`PHONE_PAINT_GPU=raster`); checked byte for byte on the developer machine's llvmpipe, **not tried on a phone** (§9) | `paint/src/raster.rs`, `paint/src/scan.rs` |
| The default by board | ✓ `sku.json`'s `paint_gpu` (`off`, `compose`, `raster`), the environment over it; every board is `off` (§3) | `sku/boards/*.toml`, `tools/sku.py`, `shell/sku.go`, `gpu::Ask::choose` |
| The default by image (the build switch) | ✓ `PHONE_PAINT_GPU_DEFAULT` goes into the image's `sku.json` over the board's, `yocto/kas/paint-gpu.yml` sets it to `compose`; the OnePlus 6T's `phone-sku` built both ways (§3), **not tried on a phone** | `yocto/kas/paint-gpu.yml`, `phone-sku_0.1.bb` |
| The display and the GPU on two devices | ✓ the GPU's render node opened before the sandbox, rendered on, the buffers flipped to by the display's device through PRIME; verified on QEMU with a second file of the card (§7), **not tried on a phone** (§4.1) | `paint/src/node.rs`, `paint/src/gbm.rs`, `kms::Display::import_fb` |
| Colour filters, the magnifier | ✓ the filters are a shader of the pass to the panel, the magnifier's picture is drawn by the CPU and put on the panel by the GPU; compared with the CPU path on llvmpipe (§4.2), **not tried on a phone** | `paint/src/gpu.rs` (`PRESENT`), `paint/src/main.rs` (`looks`) |
| A refused page flip | ✓ the first refusal logs what the kernel shows; the CRTC is set once and the flip tried again; a pending flip is waited for and asked again (§4.3) | `paint/src/kms.rs` (`describe`) |

## 2. The three levels

| Level | What | Here |
|------|------|------|
| L0 frames to the panel | The CPU draws the picture as before, the GPU puts it on the panel: turning a portrait panel a quarter and centring on a big monitor go into one GPU pass, no CPU loop over the pixels | Done inside L1: its last pass (`PRESENT`) draws the screen into a GBM surface's buffer, which a page flip shows |
| L1 compositing | The layers (the page, the top layer, the body's bands, sliding banners) become textures; the GPU uploads only the rectangles drawn, composes one screen in one pass, runs transitions as shaders | Done, optional (37 §5 item 8); the CPU still rasterizes everything (tiny-skia, ab_glyph) |
| L2 rasterizing | Every drawing operation (`Op`) on the GPU: rects, rounded rects and circles with tiny-skia's coverage, text from a glyph atlas; polygons and clipped shapes painted by the CPU and laid over | Done, optional (37 §5 item 10, §9) |

Where we differ from the first design (written on another machine in the same batch): it went to L1 at once, without an L0 of its own; the libraries are opened at run time through khronos-egl's dynamic loading and glow (both MIT/Apache) and a small dlopen of libgbm (`gbm.rs`); `features` has `gpu` so the Shell and the tests can see which path draws; `PHONE_PAINT_GPU=1` does not exit when the GPU cannot start, it falls back; the GPU starts after the privileges are dropped, not before (§4).

## 3. Turning it on

| `PHONE_PAINT_GPU` | What phone-paint does |
|------|------|
| Not set | As the board's `paint_gpu` (below): `compose` as `1`, `raster` as `raster`, `off` or none draws on the CPU |
| `0`, empty, anything else | Draws on the CPU (dumb buffers), as always, whatever the board says |
| `1` | Composes on the GPU if it can start there; a driver that is only a software rasterizer (llvmpipe, softpipe, swrast) is refused: on it the GPU path is slower than the CPU's (37 §2.4) |
| `soft` | As `1`, and a software rasterizer is taken too: **a test mode only**, for QEMU's 2D virtio-gpu, which has no GPU (Mesa falls back to kms_swrast and softpipe there); never for a product |
| `PHONE_PAINT_GPU_FAIL=<n>` beside `1` or `soft` | The GPU fails on purpose at the n-th frame it puts on the panel: the test of the fallback (the CPU draws from then on) |
| `PHONE_PAINT_GPU_RENDER=<node>` beside `1` or `soft` | Renders on that node under `/dev/dri`, whatever it is, the buffers going to the display's device through PRIME (§4.1): the test of the path for two devices; on QEMU `/dev/dri/card0` (§7.4) |

Comparing the paths' numbers (frame time, input to photon, cold start) takes two more variables, set beside any of the above (the gate at the end of [39](39-oneplus-6t.md) §8.1, [23](23-renderer-protocol.md) §3.1): `PHONE_PAINT_TARGET=480x640` (or `640x480`, `@<scale>`, 2 by default) draws at the product's screen size in the middle of the panel, and is left out when bigger than the panel; `PHONE_PAINT_PLAY=<file>` plays a fixed sequence of keys and touches (`tools/perf/gate.play`) and, when it has played out, writes the numbers to the file the Shell names (`/run/phone/paint-stats.json`). `$W perf` runs each of the three paths at both sizes and lays them out as one table.

**The default by board**: the board file's (`sku/boards/*.toml`) `paint_gpu`, `off` (the default), `compose` or `raster`, which an SKU file may set over the board's; only a board with `gpu = true` may have one that is not `off` (`tools/sku.py` checks, `tools/sku_test.py`'s `test_paint_gpu`). When it is not `off` it goes into `sku.json`, which the Shell checks again (`shell/sku.go`, `TestPaintGPU`) and hands to the renderer in the handshake as `device`'s `gpu` ([23](23-renderer-protocol.md) §2; also when `--device` changes the form factor), and phone-paint follows it when `PHONE_PAINT_GPU` is not set (`gpu::Ask::choose`). The environment counts as soon as it is set: with a board's `compose`, `PHONE_PAINT_GPU=0` still draws on the CPU. **Every board is `off`** for now: the OnePlus 6T's board file says `paint_gpu = "off"` in so many words, to be changed once [39](39-oneplus-6t.md) §8.1 and §14.4 pass on the phone (how: the end of 39 §8.1); the other boards say nothing (`off` too), none of their GPUs having been tried on a phone. A board whose display's device cannot render also names `gpu_render` (the kernel driver of the GPU's render node, `panfrost` say, §4.1).

**The default by image (the build switch)**: for an image that draws on the GPU by default while the board is still `off` (the test image of [39](39-oneplus-6t.md) §8.1), the build takes the overlay `yocto/kas/paint-gpu.yml`:

```sh
make yocto-build WORK=$HOME/yocto/fajita-work KASFILE=yocto/kas/phone-fajita.yml SKU=oneplus-fajita-t9 KASEXTRA=yocto/kas/paint-gpu.yml
```

It sets `PHONE_PAINT_GPU_DEFAULT = "compose"`; by it `phone-sku` writes `compose` as the image's `sku.json`'s `paint_gpu` (`raster` as `raster`, `off` takes the board's out), and from there it is the path of a board saying `compose`: the Shell checks it and hands it to the renderer in the handshake, phone-paint starts compositing on the GPU on the panel and draws on the CPU where that does not start or fails later (the end of §2, §7's failure on purpose), and `PHONE_PAINT_GPU` still comes first (`PHONE_PAINT_GPU=0`: back to the CPU). Only for an SKU whose board has `gpu = true`: the SKU's kas overlay then says `PHONE_SKU_GPU = "1"`, and without it, or with a value not `off`, `compose` or `raster`, bitbake stops at parsing. Unset, the image's `sku.json` is byte for byte the one `tools/sku.py` writes, and the SKU's id does not change (the whole-phone test kit checks the image by it). It changes that file only: whether Mesa's packages and the firmware are in the image is the machine's kas configuration's (§5), not the switch's. `tools/sku_test.py`'s `test_paint_gpu_default` checks this with the recipe's own Python. The chroot tests ([39](39-oneplus-6t.md) §13.4) put the kit's own `sku.json` in, so there the switch does nothing and the environment is the way. On 2026-11-26 `phone-sku` was built for the 6T's configuration: without the overlay it is what `tools/sku.py` writes, with it one line more, `"paint_gpu": "compose"`; Mesa came from the shared sstate both times, unchanged.

The renderer inherits the Shell's environment, and the Shell the display service's. On a development image:

```sh
# For phone-display.service only (kept across reboots):
mkdir -p /etc/systemd/system/phone-display.service.d
printf '[Service]\nEnvironment=PHONE_PAINT_GPU=1 PHONE_PAINT_TRACE=1\n' > /etc/systemd/system/phone-display.service.d/gpu.conf
systemctl daemon-reload && systemctl restart phone-display
# For one boot, every service: on the kernel command line
#   systemd.setenv=PHONE_PAINT_GPU=1
grep -e 'phone-paint: gpu' -e 'drawing on the CPU' /run/phone/gui.log
```

`phone-paint: gpu: composing with <renderer>` means it composes on the GPU; `...; drawing on the CPU` says why it could not start; `...; drawing on the CPU from now on` that it gave up while in use.

## 4. Starting inside the sandbox

Decision: the GPU starts **after** `kms::drop_privileges()`, as the renderer's last step before `ready`, not before as the first design proposed. Why:

- `drop_privileges` must run while the process has one thread: Landlock binds only the thread that calls it and the threads that thread starts later. Mesa starts threads once EGL is initialized (llvmpipe dozens); started after the sandbox, every one of them is inside Landlock and seccomp too.
- What the GPU needs from before is already there: the card was opened before the sandbox (the GBM device is created on its fd, a second card fd is never opened), and a kernel driver that needs firmware loads it when the card is opened (msm: at the first open of the device), so the GPU's firmware is loaded before the sandbox too. Mesa's libraries and its drirc are under `/usr`, which Landlock leaves readable.
- What it costs: inside the sandbox Mesa cannot read sysfs's device details (Landlock), so it finds the driver by the kernel driver's name (`drmGetVersion`): for msm that is freedreno anyway, on QEMU `virtio_gpu`, whose driver the image does not have, so it falls back to kms_swrast. A board where the display and the GPU are two devices (kmsro: a display controller plus a Mali or PowerVR on a render node of its own) needs the GPU's render node opened before the sandbox: it is opened with the card (§4.1). Shaders are not cached on disk (nothing can be written); each start compiles phone-paint's four shaders again, milliseconds on a GPU (**not measured** on softpipe: it happens before `ready`, which §7.2 does not time).
- Seen on QEMU (§7): Mesa started inside Landlock, seccomp and user nobody; the console shows three lines `MESA-LOADER: failed to retrieve device information` (sysfs refused) and one `virtio_gpu: driver missing`, then kms_swrast takes over.

### 4.1 The display and the GPU on two devices

The problem: the kernel drivers of GPUs such as Mali (Panfrost), PowerVR and Vivante (etnaviv) only render and have a render node of their own (`/dev/dri/renderD*`); the display controller is another DRM device (Rockchip's VOP, Allwinner's DE, MediaTek's, Unisoc's DPU, i.MX's LCDIF) with only a card. Mesa's kmsro opens the render node itself when GBM starts on the card: inside the sandbox Landlock refuses `/dev`, so kmsro fails; and Mesa knows only the display drivers on its list (whether Unisoc's `sprd` is on it depends on the Mesa version, **not checked**), falling back to kms_swrast (software, which the GPU path refuses) for the others.

What it does (`paint/src/node.rs`):

- Before the sandbox, and only when the GPU path is asked for: the kernel driver (`DRM_IOCTL_VERSION`) and the device (sysfs's `/sys/dev/char/<major>:<minor>/device`) of the card and of each `/dev/dri/renderD*`. If the card's device has a render node of its own (msm, virtio_gpu: the display and the GPU are one device), it renders on the card as before; if not (a display controller only), it takes the first render node of another device (`vgem` does not count); a board's `gpu_render` names the driver to take, and if there is none of it, it says so and draws on the CPU. The chosen render node stays open like the card, the others are closed.
- The GBM device and EGL are made on the render node; the GBM surface asks for linear buffers (`GBM_BO_USE_LINEAR`: the display controller does not know the GPU's tiled and compressed layouts). The first time each buffer comes it is exported as a dmabuf (`gbm_bo_get_fd`) and imported by the display's device through PRIME (`DRM_IOCTL_PRIME_FD_TO_HANDLE`), the import's handle closed once the framebuffer is made (the framebuffer holds the buffer); from then on page flips are as for one device (`kms::Display::import_fb`). The first frame at start goes through the import and the framebuffer, and if the import fails it falls back to the CPU and says why.
- Before each flip, `glFinish`: the display is another driver, which may not wait for the GPU's fence on a buffer it imported. The cost: no overlap of the CPU and the GPU in the pass to the panel.
- A buffer GBM lays out by a modifier (tiled, compressed) becomes a framebuffer by `ADDFB2` with it, and a display that takes no modifiers is named as the reason; for one device too (before, the framebuffer was made without the modifier, and the display would read the buffer as linear). Linear buffers and those without a modifier still go through `ADDFB`.
- The log: `phone-paint: gpu: composing with <renderer> on /dev/dri/renderD128 (panfrost), shown on /dev/dri/card0 (rockchip) through PRIME`.
- Tests: the rules for the node (`node::tests`); linear and other GBM surfaces on `/dev/null` say why they fail (`gpu::tests`); the whole path verified on QEMU (§7, with a second file of the card, §7.4). Two real devices **not tried on a phone**.
- Not done: on boards whose display controller has no IOMMU and scans out physically contiguous memory only (Allwinner's DE2, i.MX8M's LCDIF and the like), buffers the GPU's driver allocates from shmem cannot be imported (the import or the framebuffer fails and the GPU path falls back to the CPU); there it has to go the other way round, dumb buffers allocated on the display's device and exported to the GPU as render targets (as Mesa's kmsro does), **not implemented**.

### 4.2 Settings › Accessibility's looks

Colour inversion, colour correction and the magnifier (Settings › Accessibility, `paint/src/look.rs`) used to work on the picture the CPU drew, and the GPU path was closed, the CPU taking over, while one was on. Now the GPU path stays:

- The colour filters are a shader of the pass to the panel (`PRESENT`): after the composed screen is read and before the pointer is laid over, the colours change as look.rs's `Filter` changes them: a correction goes to linear sRGB, takes the correction matrix (f32, the same the CPU uses), is quantized to `LIN_STEPS` (16384 levels) and encoded back to sRGB; inversion goes by BT.601's luma. The pointer and the black around the screen are not recoloured (as on the CPU path). A filter changed with the screen unchanged puts a frame on the panel again.
- The magnifier: its picture is still drawn again by the CPU at the zoomed scale and cut around the focus (`main.rs`'s `magnified`, the CPU path's code: text rasterized at the zoomed size, sharp, not a picture scaled up), and goes to the panel as a texture in place of the composed screen, only what differs from the picture before uploaded; the filters still apply on the way. The layers are composed meanwhile, so nothing is uploaded whole once it is off. Not done: the GPU scaling the composed screen up, which is fast but makes text blurred or blocky, not the CPU path's picture.
- Transitions: none while any of them is on, as before (on both paths).
- Against the CPU path (llvmpipe on the developer machine): inversion byte for byte; a correction 1 level off for 1 colour in 32768 (the GPU's `pow` and the CPU's differ at the edge of a rounding); the test allows 1 level and under 0.5% of the pixels (`gpu::tests::the_filters_are_the_cpus`). The magnifier's picture byte for byte the CPU path's, within a level with a filter (`main.rs`'s `the_gpu_shows_a_look_as_the_cpu_path`, the layers composed on the GPU). The precision of `pow` on a phone's GPU: **not tried on a phone**.

### 4.3 A refused page flip (the OnePlus 6T's EINVAL)

On a 6T running the chroot on Nura ([41](41-design-gaps.md) §11), phone-paint's page flips of dumb buffers failed with `page flip: Invalid argument (os error 22); drawing in place`, cause unknown; Nura's kernel also failed to load `a630_sqe.fw` ([39](39-oneplus-6t.md) §14.4; Kario's own 6T image ships the a630 firmware). `kms.rs` checked against the kernel's legacy page flip (`drm_mode_page_flip_ioctl`, through `drm_atomic_helper_page_flip` on atomic drivers):

| Rule | `kms.rs` |
|------|------|
| Only known flags; with `DRM_MODE_PAGE_FLIP_EVENT` the events must be read | only `EVENT`, `wait_flip` reads the events ✓ |
| The framebuffer no smaller than the CRTC's mode (`drm_crtc_check_viewport`, against the mode turned when the primary plane is turned 90°/270°; ENOSPC otherwise) | the dumb buffers are made for the mode `set_crtc` sets ✓ |
| The format of the framebuffer on the CRTC (EINVAL otherwise) | both XRGB8888 ✓; the GPU's first frame goes by `set_crtc`, which may change it ✓ |
| The CRTC on (EINVAL from atomic drivers otherwise: `disabled, rejecting legacy flip`, `requesting event but off`) | no flips while the panel is off ✓ |
| One flip pending at a time (EBUSY otherwise) | waited for its event at most 100 ms, and a flip after that time-out hitting EBUSY stopped flips ✗ → now it waits for it to complete (at most 0.5 s; a flip given up on is owed its completion event, and a late event pays for that flip only, [28](28-battery.md) §7) and asks again once |
| No full modeset needed (EINVAL when an atomic driver's check wants one: `requires full modeset`) | invisible to userspace: the kernel's drm debug log tells |
| Command-mode panels (the 6T's is one): the completion event comes when a frame is transferred; drawing in place needs `DIRTYFB` to refresh | `dirty_framebuffer` after drawing in place ✓ |
| A CRTC the connector's encoders can drive (`possible_crtcs`) | the device's first CRTC where the connector had no encoder ✗ → now the first its encoders can drive |

Changed: the CRTC chosen by `possible_crtcs` (`the_crtc_is_one_the_connector_can_use`, which fails on the old code); on EBUSY the pending flip is waited for and the flip asked again once; the first refusal shows the frame by `set_crtc` and the next frame flips again, and only a second refusal in a row stops flips (drawing in place, `set_crtc` each frame on the GPU path; `a_refused_flip_is_tried_once_more`, which fails on the old code); GPU buffers with a modifier by `ADDFB2` (§4.1). None of these need be the 6T's cause: `kms.rs` met the visible rules of the table already; most likely msm's atomic check (a 7.1-rc1 kernel, a command-mode panel with DSC) wants a full modeset, or Nura's console (fbdev) left a plane's state behind; on the 6T: **not tried on a phone**.

Diagnostics: at the first refusal phone-paint writes lines `phone-paint: page flip: ...` to the log (`kms::Display::flip_facts`, `describe`), without the kernel's noisy drm debug: the error, the driver and a few capabilities (async flips, the CRTC in vblank events, monotonic timestamps, ADDFB2 modifiers); the CRTC's mode now (against the one phone-paint set), its framebuffer, `ACTIVE`; the connector, `DPMS`, `panel orientation`; the size, format, modifier and pitch of the framebuffer on the CRTC and of the one to flip to (`GETFB2`); the CRTC's primary plane and the planes on it: type, CRTC, framebuffer, `rotation`, whether it takes XRGB8888; the flips asked for and completed, those given up on and still owed their events, whether one is pending. Then lines `likely:`: which rule of the table is broken (the size, the format, the CRTC off, the mode changed by another program, another's framebuffer on the CRTC, the primary plane turned 90°, the primary plane on another CRTC, a flip pending), or, with none, that a check of the driver's own refused it and how to have the kernel say which ([39](39-oneplus-6t.md) §14.4's commands); and a line `also:` for planes another program left on the CRTC (a cursor, an overlay over the frames). To see the primary plane and `ACTIVE`, the universal planes and atomic client capabilities are set for this and cleared again. The unit test `a_refused_flip_says_why` checks each `likely` on made-up cases.

## 5. Images

`yocto/kas/phone-qemuarm64.yml`'s `gpu` section configures Mesa (MIT) as `gallium opengl egl gles gbm expat xmlconfig zlib`: EGL, GLES 2 and GBM on DRM, nothing for X11, Wayland or Vulkan, no LLVM. Its gallium drivers are softpipe (always) behind kms_swrast where the display has no GPU driver; a phone's machine adds its GPU's (the 6T's `phone-fajita.yml`: freedreno). phone-paint recommends Mesa's packages (`RRECOMMENDS`: libegl-mesa, libgles2-mesa, libgbm, mesa-megadriver), not requires them: without them it draws on the CPU.

| Added to the QEMU image (2026-11-21 build, Mesa 26.0.5) | Bytes |
|------|------|
| `libgallium` (softpipe, kms_swrast and the shader compiler) | 16 724 600 |
| `libegl-mesa` | 330 207 |
| `libgbm` | 200 823 |
| `libdrm` (nothing else in the image used it) | 133 265 |
| `mesa-megadriver` (the GBM backend `dri_gbm.so`) | 74 855 |
| `libgles2-mesa` | 67 474 |
| In all | 17 531 224 (17.5 MB) |

- Why no llvmpipe: its `libLLVM` alone is about 116 MB, several times the rest of Mesa together, and llvmpipe is refused by default anyway (§3). softpipe is slow (about half a minute a 480×640 frame on QEMU's display under TCG, §7.2), which only matters for the test mode.
- Licences: Mesa and libdrm are MIT; nothing GPLv3 (`INCOMPATIBLE_LICENSE` let the build through; `scripts/yocto-check.py` passes).
- The product image: `kario-internal`'s `kas/product.yml` includes this kas file, so it gets the same Mesa; that image was **not built** in this batch (its build directory is not this batch's).
- The web overlay (`yocto/kas/web.yml`): WPE renders through Mesa's EGL on its Wayland platform, so it puts back what the `gpu` section leaves out (Wayland, X11, Vulkan, Zink, virgl, video codecs: the recipe's defaults for this distribution). **Not verified**: the web image was not built in this batch.
- The OnePlus 6T: `phone-fajita.yml` appends `freedreno`. The manifest of the 6T image built on 2026-11-25 (with today's kas configuration) has `libgallium`, `libegl-mesa`, `libgles2-mesa`, `libgbm1`, `mesa-megadriver` (Mesa 26.0.5) and `libdrm2`; `libgallium-26.0.5.so` is 19 091 256 bytes (what it has beyond QEMU's is freedreno), and the GPU names it knows include `FD630`; `/usr/lib/gbm/dri_gbm.so` is there. The GPU's firmware: `linux-firmware-qcom-adreno-a630`'s `qcom/a630_sqe.fw` and `qcom/a630_gmu.bin`, `firmware-oneplus-sdm845`'s `qcom/sdm845/OnePlus/enchilada/a630_zap.mbn` (the `firmware-name` of the 6T device tree's `gpu_zap_shader`). The kernel: `CONFIG_DRM_MSM=y` (built in, no module to sign), msm's `separate_gpu_kms` off by default, so the display and the GPU are one DRM device (the card and `renderD128`) and phone-paint renders on the card's fd (§4.1's `node::choose`). The sandbox: the renderer is started by `phone-display.service`, which runs as root with no device restrictions, and opens the card before dropping privileges; seccomp blocks only the system calls it lists (not ioctl or mmap), and the msm ioctls freedreno uses need no authentication on the card (the kernel gives them `DRM_RENDER_ALLOW` only). All **not tried on a phone**. Its tune (cortexa75-cortexa55) is not qemuarm64's (cortexa57), so the two Mesa builds do not overwrite each other's packages in a shared state cache. The owner's build: [39](39-oneplus-6t.md) §4, then `grep -e mesa -e gallium` in the image's manifest.
- The SM6115 / SM4250 boards (`kario-qcom`, Adreno 610) share qemuarm64's tune: freedreno for them only in that machine's configuration would make two different Mesa packages of one architecture, so it needs Mesa built per machine there (`PACKAGE_ARCH`); **not done**.

## 6. On a phone

**Not tried on a phone.** What to confirm on the OnePlus 6T (freedreno loads, EGL/GBM on the card's fd, page flips at the panel's rate, the sandbox with freedreno, nothing broken with `PHONE_PAINT_GPU=1`, `phone-paint --bench` with recorded frames), with the commands: [39](39-oneplus-6t.md) §8.1. A test image built with §3's build switch takes the GPU path from boot, without a drop-in setting `PHONE_PAINT_GPU=1`. Whether the GPU path should be the default on a model is to be decided by those figures (frame time and power against the CPU path's damage rectangles, [37](37-motion.md) §0), and once decided goes into its board file's `paint_gpu` (§3).

## 7. QEMU

### 7.1 How it runs

```sh
make yocto-gui-gpu WORK=$HOME/yocto/gui-work      # four boots one after the other, about five hours
make yocto-gui GUI_ENV='PHONE_PAINT_GPU=soft PHONE_SCREEN_OFF=3600' GUI_WAIT=10   # only the GPU boot
make yocto-gui-gpu WORK=$HOME/yocto/gui-work GUI_GPU_RUNS=gpu-split   # the CPU boot (the reference) and the split one
```

`yocto/qemu-gui-gpu.sh` boots the image four times with `phone.gui=1` and drives the same keys each time (`yocto/qemu-gui-drive.py`, [13](13-prototype.md) §5.1): drawing on the CPU (`gui-shots-cpu`), composing on the GPU through softpipe (`PHONE_PAINT_GPU=soft`, `gui-shots-gpu`), composing the same way but rendering on another DRM file than the display's (`PHONE_PAINT_GPU_RENDER=/dev/dri/card0`: a second file of the card, whose buffers go to the display's file through PRIME to be flipped to, §4.1, §7.4; `gui-shots-gpu-split`), and the GPU failing on purpose at frame 8 (`PHONE_PAINT_GPU_FAIL=8`, `gui-shots-gpu-fail`); the variables reach the renderer through `systemd.setenv=` on the kernel command line (`GUI_ENV`). Then `yocto/gui-compare.py` compares each GPU boot's screendumps with the CPU boot's: a pixel differing by more than 2 levels in a channel (gpu.rs's `LEVELS`) counts; differences in the status bar's rows (its clock), within a box around what follows the time of the run on the screen (standby's time and date, a call's time, the text on the camera's made-up picture, the chat's demo message's time: `--volatile NAME:X0,Y0-X1,Y1`), outside it in a box the size of a text cursor (blinking), and in the scan line's picture (it runs by the clock) are reported and do not fail. The two GPU boots also have to report `gpu` among `ready`'s features (`PHONE-GUI-GPU`), page flips asked for and completed (`PHONE-GUI-FLIPS`, from the `flips` of each `PHONE_PAINT_STEPS` line), at least one transition step composed, and no fallback; the split boot's `gpu:` line must also say it renders on that node and shows through PRIME; the failing boot has to fall back.

softpipe under TCG takes about half a minute a frame, and the renderer draws only the last of the frames that came while it drew (37 §5 item 9): keys pressed a second and a half apart are merged into a few frames, and a page's transition is merged away when keys follow it. So with `GUI_WAIT` above 1 the drive script waits for each screen until the screen below the status bar has not changed, or only blinked between two pictures (a text cursor), for four of the renderer's frames (their time from its `PHONE_PAINT_STEPS` lines on the console), polling screendumps; a box that changes by itself (a clock, the camera's text) or follows the time of the run is left out, the same boxes the comparison takes from the drive script's `PHONE-GUI-VOLATILE` lines, and the scan line, which changes all over, gets ten frames. `PHONE_SCREEN_OFF=3600` keeps the screen on between two keys (the Shell's timeout would otherwise darken it, and the next key would only light it).

Every boot's clock starts at the same time (`PHONE_GUI_RTC`, QEMU's `-rtc base=`; by default 04:00 UTC of the day the script starts, noon in the SKU's region, China): the image syncs no clock, so the guest's time is the host's when it boots, and in a full run of over five hours the boots may fall on either side of a midnight, which changes screens that follow the date (the calendar's today, where its focus starts gliding; §7.5). At the end the script checks that the kernel took the same date from the RTC in every boot (`PHONE-GUI-CLOCK`). `GUI_GPU_RUNS=` runs only some of `gpu`, `gpu-split` and `gpu-fail`; the CPU boot always runs (it is the reference).

### 7.2 Results (2026-11-21)

`make yocto-gui-gpu WORK=$HOME/yocto/gui-work` on the open-source QEMU image (qemuarm64: TCG, Cortex-A57, 4 CPUs, 2 GB, `virtio-gpu-pci` at 480×640), other jobs' QEMU boots sharing the build machine: passed, 2 h 47 min. The times are a software rasterizer's under an emulator, **not a phone GPU's**: they show that the path works, not how fast it is.

| | CPU | GPU (softpipe) | GPU failing at frame 8 |
|---|---|---|---|
| What draws | the CPU | `gpu: composing with softpipe` (Mesa's loader finds no `virtio_gpu` driver and takes kms_swrast), `ready`'s features `… sandboxed landlock seccomp gpu` | the same until frame 8, 384 s after boot: `failing on purpose at frame 8 (PHONE_PAINT_GPU_FAIL); drawing on the CPU from now on` |
| Page flips asked for, completed | 1162, 1161 | 242, 241 | 1216, 1215 |
| Transition steps | 488 (p50 1.5 ms) | 7 (p50 20.2 s, p95 21.0 s) | 491 (p50 1.5 ms) |
| A frame handed over (composed and put on the panel) | p50 6.8 ms, p95 11.0 ms | p50 31.0 s, p95 32.1 s | p50 7.0 ms, p95 11.6 ms |
| The layers drawn on the CPU | p50 4.8 ms | p50 5.9 ms | p50 5.2 ms |
| The push slide (30 screendumps) | 6 pictures in 1.3 s | 6 pictures in 224 s | 3 pictures in 10.7 s |
| Mouse click and wheel, keyboard plugged in and out | ✓ | ✓ | ✓ |
| Screens against the CPU boot's | — | 17 of 17: 10 the same, 5 within their volatile boxes, 1 a cursor and its box, the scan line | 17 of 17: 10 the same, 6 within their boxes, the scan line |
| Time | 5½ min | 2 h 23 min | 17½ min |

The full run before it (the same day, much the same numbers: 248 flips and 9 transition steps on the GPU) failed on one screen, `16-chat-candidates`, in both comparisons: the chat's demo message carries the app's start time less twenty minutes, which differs from boot to boot. The drive script now names a box around it, and the comparison leaves a screen's box out before it looks for a cursor (`yocto/gui-compare-test.py`). The runs before those found the drive script's own bugs (a shadowed name, the ready line's features read from a kernel timestamp, screendumps taken before a half-minute frame had come or while a screen was still catching up with the keys); one failing boot was killed from outside (every QEMU on the host vanished at once), not by the check.

### 7.3 virtio-gpu-gl (virgl)

**Not run.** QEMU's `virtio-gpu-gl-pci` hands the guest's GL to the host's GPU through virglrenderer; the image would then need Mesa's virgl driver (`PACKAGECONFIG:append:pn-mesa = " virtio"`) and runqemu's `egl-headless` (or `gl` with SDL or GTK). This build's qemu-system-native already has virglrenderer and epoxy, so it would not cost a build; it fails on the host: this build machine has no DRM render node (its only display chip is the server's mgag200, card0 without a render node), the build container has no `/dev/dri`, and `egl-headless` needs a render node (runqemu checks for one), `gl` a display. On a machine with a GPU, an overlay with those two lines plus `QB_GRAPHICS:forcevariable = "-device virtio-gpu-gl-pci,xres=480,yres=640"` and runqemu's `egl-headless` would run phone-paint on the host's GPU (`PHONE_PAINT_GPU=1`: virgl is not a software rasterizer unless the host's GL is); it says nothing about a phone's GPU either.

### 7.4 The render node on QEMU

In the 6.18 kernel virtio-gpu always has a render node: `virtgpu_drv.c`'s `driver_features` carry `DRIVER_RENDER` unconditionally, for the 2D device (`-virgl`) too, so the image has `card0` and `renderD128`, both `virtio_gpu`. But Mesa can draw nothing on that render node, as tried in the image on 2026-11-23 (`make yocto-gui GUI_ENV='PHONE_PAINT_GPU=soft PHONE_PAINT_GPU_RENDER=/dev/dri/renderD128 PHONE_SCREEN_OFF=3600'`): phone-paint opened `renderD128` before the sandbox, Mesa fell back to kms_swrast, the console shows two lines `KMS: DRM_IOCTL_MODE_CREATE_DUMB failed: Permission denied`, then `phone-paint: gpu: EGL: EGL failed to allocate resources for the requested operation.; drawing on the CPU`, and the CPU drew and flipped as always. Why:

- Mesa's driver for `virtio_gpu` is virgl, which needs QEMU's 3D device (`virtio-gpu-gl`, with virglrenderer and a render node on the host, §7.3), and the image does not have it either;
- without a driver GBM falls back to kms_swrast, which allocates and maps its buffers as dumb buffers (`DRM_IOCTL_MODE_CREATE_DUMB`, `MAP_DUMB`), and the kernel refuses those two on a render node (in `drm_ioctl.c` they lack `DRM_RENDER_ALLOW`: EACCES there); the plain swrast after it allocates dumb buffers on the device too;
- softpipe cannot import dmabufs, so the other way round (allocating on the card and importing into EGL on the render node) does not work either; llvmpipe can (through udmabuf), but the image does not have it (§5).

So on QEMU the test mode takes a second file of the card (`PHONE_PAINT_GPU_RENDER=/dev/dri/card0`) for "another device": the two files' GEM handles mean nothing to each other, so a buffer becomes a framebuffer to flip to only exported as a dmabuf and imported into the display's file through PRIME, the same code as for two real devices (the export, the import, the framebuffer, flipping on the display's file and reading its events, the buffers' round trip). Where it differs: on the kernel's side it is one driver, which recognises its own export on import and returns the same object (`virtgpu_gem_prime_import`), with no dma-buf mapping between two drivers and none of the trouble with contiguous memory or tiled layouts (the end of §4.1). Really using a render node on QEMU takes virgl (§7.3).

### 7.5 Results (2026-11-23 and 24: rendering on another file)

This batch's image ran `make yocto-gui-gpu WORK=$HOME/yocto/gui-work`, four boots in 5 h 33 min (the CPU 5½ min, the GPU 2 h 29 min, the split boot 2 h 40 min, the failing one 17 min), other jobs' QEMU boots sharing the build machine. The three GPU boots did what is asked of them (the table below), but the comparison failed: the split and the failing boots' `12-calendar-glide` each had 5649 pixels unlike the CPU boot's. Not because of the GPU path: the CPU boot started at 22:52, those two after midnight, so the calendar's today had moved from the 23rd to the 24th, and the focus, gliding from today, stopped on the 26th instead of the 25th; the failing boot had long been drawing on the CPU when it came to that screen, and differed in exactly the same way. The image syncs no clock, so the guest's time is the host's when each boot starts. The fix: every boot's clock starts at the same time, and the script checks they have one date (§7.1, `PHONE_GUI_RTC`, `PHONE-GUI-CLOCK`); the calendar was not made a volatile box. Then the two groups that needed it ran again: the CPU and the failing boot (17 of 17, the calendar the same), the CPU and the split boot (17 of 17, the calendar the same; the split boot 251 page flips, 250 completed, 9 transition steps, a frame handed over p50 31.5 s, 2 h 31 min). The GPU boot's comparison had passed already (it and the CPU boot were both before midnight).

| | GPU (softpipe) | Split (softpipe, a second file of the card) | GPU failing at frame 8 |
|---|---|---|---|
| What draws | `gpu: composing with softpipe` | `gpu: composing with softpipe on /dev/dri/card0 (virtio_gpu), shown on /dev/dri/card0 (virtio_gpu) through PRIME` | the same until frame 8, 375 s after boot: `failing on purpose at frame 8 …; drawing on the CPU from now on` |
| Page flips asked for, completed | 244, 243 | 247, 246 | 1236, 1235 |
| Transition steps | 7 (p50 20.9 s) | 6 (p50 21.4 s) | 489 (p50 1.5 ms) |
| A frame handed over (composed and put on the panel) | p50 31.6 s, p95 37.8 s | p50 32.2 s, p95 52.5 s | p50 6.5 ms, p95 11.6 ms |
| The push slide (30 screendumps) | 6 pictures in 224.9 s | 4 pictures in 106.7 s | 3 pictures in 10.7 s |
| Mouse click and wheel, keyboard plugged in and out | ✓ | ✓ | ✓ |
| Screens against the CPU boot's | 17 of 17 | 16 (the calendar, above); with the clock fixed 17 of 17 | 16 (the calendar); with the clock fixed 17 of 17 |

The CPU boot: 1156 page flips, 1155 completed. No page flip was refused in any of the four boots (no `page flip` line in the logs). The split boot does an export and an import (only the first time each buffer comes) and a `glFinish` a frame more, which makes no visible difference on softpipe; it says nothing of two real devices (the end of §7.4).

## 8. Risks and open questions

- Mesa and freedreno on the 6T's mainline kernel: unknown until tried ([39](39-oneplus-6t.md) §8.1).
- Power: waking the GPU and the memory traffic of composing the whole screen every frame may cost more than the CPU drawing only the damage; nothing is composed when nothing changed, but every changed frame composes and presents the whole screen (two full-screen passes); limiting them to the damage is **not implemented**.
- A display and a GPU on two devices (kmsro): rendered on the GPU's render node (§4.1), verified only on QEMU with two files of one device; boards whose display controller needs contiguous memory need the allocation the other way round, **not implemented**.
- Settings › Accessibility's looks: the filters are on the GPU, the magnifier's picture is still drawn again by the CPU at the zoomed scale (§4.2), so with the magnifier on the CPU does as much as on the CPU path.
- A refused page flip: the cause of the 6T's EINVAL is not known; the log of the first refusal should tell (§4.3).
- Video frames as dmabuf textures: **not implemented** ([23](23-renderer-protocol.md) §9).

## 9. L2: rasterizing on the GPU

> Status: implemented, optional (`PHONE_PAINT_GPU=raster`; the CPU raster stays the default, `PHONE_PAINT_GPU=1` is still only GPU compositing). Correctness verified on llvmpipe on the developer machine: every kind of op the GPU paints is byte for byte the CPU's; **not tried on a phone**: whether it pays, frame times and power must be measured on the OnePlus 6T ([39](39-oneplus-6t.md) §14.4). See [37](37-motion.md) §5 item 10 and §2.5.

| `Op` | On the GPU (`paint/src/raster.rs`) | Against the CPU |
|------|----------|------|
| `Clear` | `glClear` in the scissor | byte for byte |
| `Rect` | a quad per op, tiny-skia's fixed-point edge coverage (1/256 pixel) in the fragment shader | byte for byte |
| `Round`, `Circle` | the CPU finds each quarter-pixel row's span along tiny-skia's edges (`paint/src/scan.rs`, a few integer operations per row), the shader covers 4×4 as tiny-skia's supersampler; not SDF: the true shapes are 1–4 samples off tiny-skia's flattened curves | byte for byte; shapes tiny-skia clips at a layer's edge go to the CPU |
| `Polygon` | painted by the CPU over transparent pixels, uploaded and laid over (only the charging bolt in the UI) | within 3 levels |
| `Fade` | a quad per row with the CPU's row alphas | byte for byte |
| `Text` | an 8-bit glyph atlas (`LUMINANCE` on GLES 2, 1024×1024) holding ab_glyph's coverage, the CPU's, keyed by font, glyph, size and the pen's and baseline's fractions; rasterized when first shown (CJK too), LRU by runs of adjacent shelves; a batch of quads per text op (GLES 2 has no instancing) | byte for byte; glyphs larger than the atlas go to the CPU |
| `Image` | the CPU's scaled pixels (`Patch`) as a NEAREST texture, kept by key | byte for byte |
| `Layer` | its rows as a texture, kept by key | byte for byte |
| `Body` | the body's bands are FBOs, painted band by band, composed as at L1 | byte for byte |

- Blending is not `glBlendFunc(GL_ONE, GL_ONE_MINUS_SRC_ALPHA)`, which is 1 level off tiny-skia's lowp integers on translucent and antialiased pixels. The fragment shader reads the pixel under and does tiny-skia's and `canvas.rs`'s integer sums: through GLSL ES 3.00 `inout` where `GL_EXT_shader_framebuffer_fetch` exists, otherwise each op first copies the pixels under it (`glCopyTexSubImage2D`).
- No `GL_FRAMEBUFFER_SRGB`; quads are pixel rectangles and coverage is computed in fixed point at pixel centres, not interpolated.
- Jobs: the damage rectangles are computed as before (`Layers::raster`), each rectangle to repaint is a job the GPU paints under a scissor; the CPU keeps no layer pixels.
- Tests: every kind of op compared at random (with fetch and with copies), the spans against tiny-skia's coverage pixel by pixel, the glyph atlas's eviction, frame pairs and every step of the animations layer by layer; `phone-paint --bench` compares every step of a frame file layer by layer, fails beyond the tolerance, and lists for each kind of op how many there were, how many the GPU painted and the CPU path's time painting them. Snapshots and the headless selftest stay on the CPU. Mesa 23.1's llvmpipe on the developer machine crashes when two contexts copy textures at once, so the tests' GPU contexts take turns (the renderer itself has one context).
- On llvmpipe L2 is slower than both L1 and the CPU path, which says nothing about phones ([37](37-motion.md) §2.5).
- `scan.rs` ports tiny-skia's edge code and keeps tiny-skia's BSD-3-Clause notice.

## 10. Ready for hardware: a checklist

> Status: everything under "done" below is verified only on the developer machine's llvmpipe and on QEMU (where it is a software rasterizer); **none of it has been tried on a phone**.

Done:

| Item | How it was verified |
|------|------|
| L0, L1, L2 | compared on llvmpipe byte for byte or within 2 levels (§2, §9); L0 and L1 on QEMU's display (§7) |
| Mesa starting inside the sandbox | Landlock, seccomp, nobody on QEMU (§4) |
| The default by board (`paint_gpu`), the environment over it | `tools/sku_test.py`, `shell`'s `TestPaintGPU`, `gpu::tests` (§3) |
| The display and the GPU on two devices: the render node opened before the sandbox, frames shown through PRIME, linear buffers, `glFinish` | unit tests of the choice of node; a second file of the card on QEMU (§4.1, §7.4, §7.5) |
| GPU buffers with a modifier made framebuffers by `ADDFB2` | a unit test (of the decision); no buffer with a real modifier tried |
| The GPU no longer stepping aside for colour filters and the magnifier | compared with the CPU path on llvmpipe (§4.2) |
| A refused page flip: tried again, a pending one waited for, the first refusal's report | unit tests (§4.3); no flip was refused on QEMU |
| Giving up mid-run to the CPU | QEMU (`PHONE_PAINT_GPU_FAIL`, §7) |
| The gate's measures: frame time, key and touch to photon (the flip completed), cold start, each path on the whole panel and at the product's size playing a fixed sequence (the end of [39](39-oneplus-6t.md) §8.1) | Unit tests with synthetic timestamps (`paint/src/perf.rs`, `play.rs`, `shell/latency_test.go`); six runs headless on a development machine (`tools/perf/host.sh`, llvmpipe, 2026-11-26: it shows they run, the numbers are not a phone's); the kit's `perf` step only planned and tested against a fake systemctl (`tools/phone_chroot_test.py`) |

What each family of chips still needs:

| GPU (Mesa driver) | Display | Still missing here | State |
|------|------|------|------|
| Adreno 6xx/5xx (freedreno): the OnePlus 6T's 630, SM6115/SM4250's 610, the Redmi Note 4X's 506 | msm, one device with the GPU (rendered on the card) | freedreno in the image (the 6T's image has it, built on 2026-11-25; SM6115/SM4250 need Mesa built per machine, §5); the GPU's firmware (`a630_sqe.fw`, `a630_gmu.bin`, the zap shader: Kario's own 6T image has them, Nura's are broken, [39](39-oneplus-6t.md) §14.4); the 6T's EINVAL on page flips (§4.3) | **not tried on a phone** |
| Mali Midgard/Bifrost/Valhall (Panfrost): Unisoc T610's G52, Rockchip, MediaTek, Allwinner H6 | another device (sprd, rockchip, mediatek, sun4i-drm) | the board's `gpu_render = "panfrost"` or the automatic choice; Mesa's `panfrost` in the image; a display controller without an IOMMU (Allwinner) needs the buffers allocated on the display's device (the end of §4.1, **not implemented**); whether mainline's Unisoc display driver (`sprd`, written for the SC9863A) drives the T610 is **not checked** | **not tried on a phone** |
| Mali Utgard (Lima): Mali-400, 450 | another device | fragment shaders have only an fp16 mediump, no 20-bit highp: the GPU path does not start, the CPU draws (§2: `HIGHP_BITS`) | not supported (by design) |
| PowerVR (Unisoc T606/T612's GE8322, some MediaTek) | another device | Mesa has only a Vulkan driver (`powervr`, early), GLES only through Zink: whether phone-paint's EGL/GLES 2 starts on it is **not tried**; vendors' closed drivers are not considered | **not tried on a phone** |
| Vivante (etnaviv): i.MX | another device (imx-drm, imx-lcdif) | `gpu_render = "etnaviv"`; i.MX8M's LCDIF needs contiguous memory (the allocation of the Panfrost row); whether its highp suffices is **not checked** | **not tried on a phone** |
| virtio-gpu (QEMU) | one device | a software rasterizer only (the test mode); virgl needs a host with a GPU (§7.3) | QEMU ✓ |

Not tried (all need a phone): frame times, power, and the comparison with the CPU path on any phone GPU; whether L2 pays on a tiling GPU ([37](37-motion.md) §5 item 10); PRIME between two real devices (contiguous memory, tiled layouts, implicit sync, §4.1); page flips on a command-mode panel (§4.3); switching the refresh rate under GBM buffers ([37](37-motion.md) §5 item 8); the CPU's time drawing the magnified picture again while the magnifier is on (as on the CPU path). How to check on the 6T and how to turn `paint_gpu` on once checked: [39](39-oneplus-6t.md) §8.1, §14.4.

The gate's numbers on the 6T (`$W perf`'s `perf.txt`, the end of [39](39-oneplus-6t.md) §8.1): **not tried on a phone**. Once measured, a line here: the date, each path's p95 frame time, key and touch on the whole panel and at the product's size, the slowest cold start, and whether it passed.
