//! libgbm, opened at run time (the renderer does not link Mesa, so it
//! starts where there is none): a surface of scan-out buffers on the
//! GPU's DRM device that EGL renders into (gpu.rs); each frame's buffer
//! becomes a framebuffer shown by a page flip (kms.rs), on the same device
//! or, exported as a dmabuf, on the display's (node.rs).

use std::ffi::c_void;
use std::os::fd::{FromRawFd, OwnedFd, RawFd};

use libloading::Library;

/// DRM_FORMAT_XRGB8888, as the dumb buffers (kms.rs).
pub const XRGB8888: u32 = u32::from_le_bytes(*b"XR24");
/// DRM_FORMAT_MOD_INVALID: the layout is the driver's own (no modifier).
pub const MOD_INVALID: u64 = 0x00ff_ffff_ffff_ffff;
const USE_SCANOUT: u32 = 1;
const USE_RENDERING: u32 = 1 << 2;
const USE_LINEAR: u32 = 1 << 4;

type Ptr = *mut c_void;

struct Fns {
    device_destroy: unsafe extern "C" fn(Ptr),
    surface_create: unsafe extern "C" fn(Ptr, u32, u32, u32, u32) -> Ptr,
    surface_destroy: unsafe extern "C" fn(Ptr),
    lock_front: unsafe extern "C" fn(Ptr) -> Ptr,
    release: unsafe extern "C" fn(Ptr, Ptr),
    // union gbm_bo_handle: 8 bytes, returned in a register like a u64.
    bo_handle: unsafe extern "C" fn(Ptr) -> u64,
    bo_stride: unsafe extern "C" fn(Ptr) -> u32,
    bo_width: unsafe extern "C" fn(Ptr) -> u32,
    bo_height: unsafe extern "C" fn(Ptr) -> u32,
    bo_fd: unsafe extern "C" fn(Ptr) -> i32,
    // Mesa 17.3 on.
    bo_modifier: Option<unsafe extern "C" fn(Ptr) -> u64>,
}

/// A buffer the GPU rendered a frame into, locked until released.
#[derive(Clone, Copy, Debug, PartialEq)]
pub struct Bo {
    pub ptr: Ptr,
    pub handle: u32,
    pub size: (u32, u32),
    pub pitch: u32,
    pub modifier: u64,
}

pub struct Gbm {
    f: Fns,
    dev: Ptr,
    surf: Ptr,
    _lib: Library,
}

impl Gbm {
    /// A GBM device on the DRM device fd (kept open by the caller) and a
    /// surface of w × h scan-out buffers on it; linear: laid out for
    /// another device to scan out (the display's, which knows nothing of
    /// this GPU's tiling).
    pub fn open(fd: RawFd, w: u32, h: u32, linear: bool) -> Result<Gbm, String> {
        let lib = crate::gpu::library("libgbm.so.1")?;
        // SAFETY: the symbols are read with the C signatures of gbm.h, from
        // a library never unloaded.
        unsafe {
            macro_rules! sym {
                ($name:literal) => {
                    *lib.get(concat!($name, "\0").as_bytes()).map_err(|e| format!("{}: {e}", $name))?
                };
            }
            let create: unsafe extern "C" fn(i32) -> Ptr = sym!("gbm_create_device");
            let f = Fns {
                device_destroy: sym!("gbm_device_destroy"),
                surface_create: sym!("gbm_surface_create"),
                surface_destroy: sym!("gbm_surface_destroy"),
                lock_front: sym!("gbm_surface_lock_front_buffer"),
                release: sym!("gbm_surface_release_buffer"),
                bo_handle: sym!("gbm_bo_get_handle"),
                bo_stride: sym!("gbm_bo_get_stride"),
                bo_width: sym!("gbm_bo_get_width"),
                bo_height: sym!("gbm_bo_get_height"),
                bo_fd: sym!("gbm_bo_get_fd"),
                bo_modifier: lib.get(b"gbm_bo_get_modifier\0").ok().map(|s| *s),
            };
            let dev = create(fd);
            if dev.is_null() {
                return Err("gbm_create_device failed (no GPU driver for this device)".into());
            }
            let flags = USE_SCANOUT | USE_RENDERING | if linear { USE_LINEAR } else { 0 };
            let surf = (f.surface_create)(dev, w, h, XRGB8888, flags);
            if surf.is_null() {
                (f.device_destroy)(dev);
                return Err(format!("gbm_surface_create {w}x{h} XRGB8888{} failed", if linear { " linear" } else { "" }));
            }
            Ok(Gbm { f, dev, surf, _lib: lib })
        }
    }

    /// The device, EGL's native display.
    pub fn device(&self) -> Ptr {
        self.dev
    }

    /// The surface, EGL's native window.
    pub fn surface(&self) -> Ptr {
        self.surf
    }

    /// The buffer eglSwapBuffers just finished, locked.
    pub fn lock_front(&self) -> Option<Bo> {
        // SAFETY: a live surface; the buffer stays valid while locked.
        unsafe {
            let bo = (self.f.lock_front)(self.surf);
            if bo.is_null() {
                return None;
            }
            let modifier = self.f.bo_modifier.map_or(MOD_INVALID, |f| f(bo));
            Some(Bo { ptr: bo, handle: (self.f.bo_handle)(bo) as u32, size: ((self.f.bo_width)(bo), (self.f.bo_height)(bo)), pitch: (self.f.bo_stride)(bo), modifier })
        }
    }

    /// A locked buffer as a dmabuf, for another device to import.
    pub fn export(&self, bo: Bo) -> Result<OwnedFd, String> {
        // SAFETY: bo is locked on this surface; the descriptor returned is
        // a new one, ours to close.
        unsafe {
            let fd = (self.f.bo_fd)(bo.ptr);
            if fd < 0 {
                return Err("gbm_bo_get_fd failed (the driver cannot export its buffers)".into());
            }
            Ok(OwnedFd::from_raw_fd(fd))
        }
    }

    /// The buffer is off the screen: the surface may render into it again.
    pub fn release(&self, bo: Bo) {
        // SAFETY: bo was locked on this surface and is released once.
        unsafe { (self.f.release)(self.surf, bo.ptr) }
    }
}

impl Drop for Gbm {
    fn drop(&mut self) {
        // SAFETY: created in open, destroyed once, the surface first.
        unsafe {
            (self.f.surface_destroy)(self.surf);
            (self.f.device_destroy)(self.dev);
        }
    }
}
