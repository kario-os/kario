//! The GPU's render node when it is not the display's device
//! (42-gpu-rendering.md §4): a display controller beside a Mali, PowerVR or
//! Vivante GPU (Mesa's kmsro boards) is two DRM devices, so the GPU path
//! renders on the GPU's render node and shows the buffers on the display's
//! card, which imports them through PRIME (gpu.rs, kms.rs). Found and
//! opened with the card, before the sandbox: neither /dev nor sysfs is
//! reachable inside it.

use std::fs::{File, OpenOptions};
use std::os::fd::{AsFd, AsRawFd, BorrowedFd, RawFd};
use std::os::unix::fs::OpenOptionsExt;

/// A DRM node: its path, its kernel driver, and the device it belongs to
/// (its sysfs path; a card and a render node of one device share it).
#[derive(Clone, Debug, Default, PartialEq)]
pub struct Info {
    pub path: String,
    pub driver: String,
    pub device: String,
}

/// The render node the GPU path renders on, open.
pub struct Node {
    file: File,
    pub info: Info,
}

impl Node {
    pub fn fd(&self) -> RawFd {
        self.file.as_raw_fd()
    }
}

struct Fd<'a>(BorrowedFd<'a>);

impl AsFd for Fd<'_> {
    fn as_fd(&self) -> BorrowedFd<'_> {
        self.0
    }
}
impl drm::Device for Fd<'_> {}

/// What the DRM node open as fd at path is.
pub fn info(path: &str, fd: BorrowedFd) -> Info {
    let driver = drm::Device::get_driver(&Fd(fd)).map(|d| d.name().to_string_lossy().into_owned()).unwrap_or_default();
    Info { path: path.into(), driver, device: device(fd).unwrap_or_default() }
}

fn device(fd: BorrowedFd) -> Option<String> {
    // SAFETY: fstat into a zeroed stat on the stack.
    let st = unsafe {
        let mut st: libc::stat = std::mem::zeroed();
        (libc::fstat(fd.as_raw_fd(), &mut st) == 0).then_some(st)?
    };
    let (major, minor) = (libc::major(st.st_rdev), libc::minor(st.st_rdev));
    std::fs::canonicalize(format!("/sys/dev/char/{major}:{minor}/device")).ok().map(|p| p.display().to_string())
}

/// Render nodes of no GPU (vgem shares buffers between processes).
const NOT_A_GPU: &[&str] = &["vgem"];

/// Which of the render nodes the GPU path renders on instead of the
/// display's card: the one of the board's driver (gpu_render), or else,
/// where the card's device has no render node (a display controller only),
/// the first of another device. None: the card renders, its device being
/// the GPU too (msm, virtio_gpu) or there being no other GPU.
pub fn choose(card: &Info, renders: &[Info], board: &str) -> Result<Option<usize>, String> {
    if !board.is_empty() {
        return match renders.iter().position(|r| r.driver == board) {
            Some(i) => Ok(Some(i)),
            None => Err(format!("no render node of {board} (the board's gpu_render) among {}", names(renders))),
        };
    }
    if card.device.is_empty() || renders.iter().any(|r| r.device == card.device) {
        return Ok(None);
    }
    Ok(renders.iter().position(|r| !NOT_A_GPU.contains(&r.driver.as_str())))
}

fn names(renders: &[Info]) -> String {
    if renders.is_empty() {
        return "none".into();
    }
    renders.iter().map(|r| format!("{} ({})", r.path, r.driver)).collect::<Vec<_>>().join(", ")
}

fn open_rw(path: &str) -> std::io::Result<File> {
    OpenOptions::new().read(true).write(true).custom_flags(libc::O_CLOEXEC).open(path)
}

/// The render node to render on (`choose`), open; None: the card renders.
/// PHONE_PAINT_GPU_RENDER=<node> takes that node whatever it is: a test of
/// this path on a device that renders itself, where a second open of the
/// card is a DRM file of its own whose buffers reach the display's file
/// only through PRIME, as another device's would.
pub fn open(card: &Info, board: &str) -> Result<Option<Node>, String> {
    if let Some(path) = std::env::var("PHONE_PAINT_GPU_RENDER").ok().filter(|p| !p.is_empty()) {
        if !path.starts_with("/dev/dri/") || path.contains("..") {
            return Err(format!("PHONE_PAINT_GPU_RENDER={path} is not a node in /dev/dri"));
        }
        let file = open_rw(&path).map_err(|e| format!("{path}: {e}"))?;
        let info = info(&path, file.as_fd());
        return Ok(Some(Node { file, info }));
    }
    let mut paths: Vec<String> = std::fs::read_dir("/dev/dri")
        .map(|d| d.flatten().filter_map(|e| e.file_name().into_string().ok()).filter(|n| n.starts_with("renderD")).map(|n| format!("/dev/dri/{n}")).collect())
        .unwrap_or_default();
    paths.sort_by_key(|p| (p.len(), p.clone()));
    let mut nodes: Vec<(File, Info)> = paths
        .iter()
        .filter_map(|p| {
            let f = open_rw(p).ok()?;
            let i = info(p, f.as_fd());
            Some((f, i))
        })
        .collect();
    let infos: Vec<Info> = nodes.iter().map(|(_, i)| i.clone()).collect();
    Ok(choose(card, &infos, board)?.map(|i| {
        let (file, info) = nodes.swap_remove(i);
        Node { file, info }
    }))
}

#[cfg(test)]
mod tests {
    use super::*;

    fn node(path: &str, driver: &str, device: &str) -> Info {
        Info { path: path.into(), driver: driver.into(), device: device.into() }
    }

    // A display controller beside its GPU (an Allwinner, Rockchip or
    // Unisoc display, a Mali on panfrost) renders on the GPU's node; the
    // board may name it; a device that is both (msm, virtio-gpu) renders
    // on its card; vgem is no GPU.
    #[test]
    fn the_gpus_render_node_when_the_display_cannot_render() {
        let sun4i = node("/dev/dri/card0", "sun4i-drm", "/sys/devices/platform/display-engine");
        let panfrost = node("/dev/dri/renderD128", "panfrost", "/sys/devices/platform/soc/1800000.gpu");
        let vgem = node("/dev/dri/renderD129", "vgem", "/sys/devices/platform/vgem");
        assert_eq!(choose(&sun4i, std::slice::from_ref(&panfrost), ""), Ok(Some(0)));
        assert_eq!(choose(&sun4i, &[vgem.clone(), panfrost.clone()], ""), Ok(Some(1)), "not vgem");
        assert_eq!(choose(&sun4i, std::slice::from_ref(&vgem), ""), Ok(None));
        assert_eq!(choose(&sun4i, &[], ""), Ok(None), "no GPU at all: the card, and Mesa's software rasterizer is refused");
        let msm = node("/dev/dri/card0", "msm", "/sys/devices/platform/soc/ae00000.display-subsystem");
        let msm_render = node("/dev/dri/renderD128", "msm", "/sys/devices/platform/soc/ae00000.display-subsystem");
        assert_eq!(choose(&msm, std::slice::from_ref(&msm_render), ""), Ok(None), "the display's device renders");
        let virtio = node("/dev/dri/card0", "virtio_gpu", "/sys/devices/pci0000:00/0000:00:05.0/virtio1");
        let virtio_render = node("/dev/dri/renderD128", "virtio_gpu", "/sys/devices/pci0000:00/0000:00:05.0/virtio1");
        assert_eq!(choose(&virtio, std::slice::from_ref(&virtio_render), ""), Ok(None));
        let sprd = node("/dev/dri/card0", "sprd", "/sys/devices/platform/soc/dpu");
        let pvr = node("/dev/dri/renderD129", "powervr", "/sys/devices/platform/soc/gpu");
        assert_eq!(choose(&sprd, &[vgem.clone(), pvr.clone()], "powervr"), Ok(Some(1)), "named by the board");
        assert_eq!(choose(&msm, &[msm_render, pvr.clone()], "powervr"), Ok(Some(1)), "the board's word over the card's own node");
        let e = choose(&sprd, &[vgem], "panfrost").unwrap_err();
        assert!(e.contains("no render node of panfrost") && e.contains("renderD129 (vgem)"), "{e}");
        assert_eq!(choose(&Info::default(), &[pvr], ""), Ok(None), "sysfs unread: the card");
    }
}
