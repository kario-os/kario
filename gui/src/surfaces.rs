//! Media surfaces (feature "shm"): the shell hands over shared memory, one
//! memfd per stream, on the side channel (the socket named by hello's
//! side_fd). mediad, phone-webview and the shell itself write the pixels
//! there; a frame only names the surface and the frame number to show.
//!
//! A frame that stops naming a surface is followed by {"t":"drop","id"}
//! without a descriptor: the mapping goes with it.
//!
//! Layout as media/surface.go writes it: a 32-byte header ("PHSURF1\0",
//! width, height, latest slot, the two slots' frame numbers, little
//! endian), then two slots of width × height × 3 bytes of RGB.

use std::collections::HashMap;
use std::sync::atomic::{AtomicU32, Ordering};
use std::sync::{Mutex, OnceLock};

use serde::Deserialize;

const MAGIC: &[u8; 8] = b"PHSURF1\0";
const HEADER: usize = 32;

struct Surface {
    base: usize,
    len: usize,
    w: u32,
    h: u32,
}

fn table() -> &'static Mutex<HashMap<String, Surface>> {
    static T: OnceLock<Mutex<HashMap<String, Surface>>> = OnceLock::new();
    T.get_or_init(Default::default)
}

#[derive(Deserialize)]
struct Msg {
    t: String,
    id: String,
    #[serde(default)]
    w: u32,
    #[serde(default)]
    h: u32,
}

/// Receives surfaces on fd until the shell closes the channel.
pub fn listen(fd: i32) {
    std::thread::spawn(move || {
        while let Some((msg, sfd)) = recv(fd) {
            let Ok(m) = serde_json::from_slice::<Msg>(&msg) else {
                if let Some(sfd) = sfd {
                    unsafe { libc::close(sfd) };
                }
                continue;
            };
            match sfd {
                Some(sfd) => register(m, sfd),
                None if m.t == "drop" => forget(&m.id),
                None => {}
            }
        }
    });
}

fn recv(fd: i32) -> Option<(Vec<u8>, Option<i32>)> {
    let mut buf = [0u8; 512];
    let mut ctl = [0u64; 8];
    let mut iov = libc::iovec { iov_base: buf.as_mut_ptr().cast(), iov_len: buf.len() };
    // SAFETY: msghdr points at the buffers above, alive for the call.
    unsafe {
        let mut m: libc::msghdr = std::mem::zeroed();
        m.msg_iov = &mut iov;
        m.msg_iovlen = 1;
        m.msg_control = ctl.as_mut_ptr().cast();
        m.msg_controllen = std::mem::size_of_val(&ctl) as _;
        let n = libc::recvmsg(fd, &mut m, libc::MSG_CMSG_CLOEXEC);
        if n <= 0 {
            return None;
        }
        let mut got = None;
        let c = libc::CMSG_FIRSTHDR(&m);
        if !c.is_null() && (*c).cmsg_level == libc::SOL_SOCKET && (*c).cmsg_type == libc::SCM_RIGHTS {
            got = Some(std::ptr::read_unaligned(libc::CMSG_DATA(c) as *const i32));
        }
        Some((buf[..n as usize].to_vec(), got))
    }
}

fn register(m: Msg, fd: i32) {
    let len = HEADER + 2 * (m.w as usize) * (m.h as usize) * 3;
    // SAFETY: a fresh read-only shared mapping of the memfd; the writer
    // sealed its size, so the mapping stays valid after the fd is closed.
    let base = unsafe {
        let mut st: libc::stat = std::mem::zeroed();
        let ok = m.t == "surface" && m.w > 0 && m.h > 0 && libc::fstat(fd, &mut st) == 0 && st.st_size as usize >= len;
        let p = if ok { libc::mmap(std::ptr::null_mut(), len, libc::PROT_READ, libc::MAP_SHARED, fd, 0) } else { libc::MAP_FAILED };
        libc::close(fd);
        if p == libc::MAP_FAILED {
            return;
        }
        p as usize
    };
    let head = unsafe { std::slice::from_raw_parts(base as *const u8, 16) };
    if &head[..8] != MAGIC || u32::from_le_bytes(head[8..12].try_into().unwrap()) != m.w || u32::from_le_bytes(head[12..16].try_into().unwrap()) != m.h {
        unsafe { libc::munmap(base as *mut _, len) };
        return;
    }
    if let Some(o) = table().lock().unwrap().insert(m.id, Surface { base, len, w: m.w, h: m.h }) {
        unsafe { libc::munmap(o.base as *mut _, o.len) };
    }
}

fn forget(id: &str) {
    if let Some(o) = table().lock().unwrap().remove(id) {
        unsafe { libc::munmap(o.base as *mut _, o.len) };
    }
}

/// The newest complete frame of a surface: width, height, RGB. A slot being
/// rewritten while it is copied (its frame number changes) is copied again.
pub fn rgb(id: &str) -> Option<(u32, u32, Vec<u8>)> {
    let t = table().lock().unwrap();
    let s = t.get(id)?;
    let word = |at: usize| unsafe { &*((s.base + at) as *const AtomicU32) };
    let size = (s.w as usize) * (s.h as usize) * 3;
    for _ in 0..3 {
        let slot = (word(16).load(Ordering::Acquire) & 1) as usize;
        let seq = word(20 + 4 * slot).load(Ordering::Acquire);
        let px = unsafe { std::slice::from_raw_parts((s.base + HEADER + slot * size) as *const u8, size) }.to_vec();
        if word(20 + 4 * slot).load(Ordering::Acquire) == seq {
            return Some((s.w, s.h, px));
        }
    }
    None
}
