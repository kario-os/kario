//! Media surfaces (feature "shm"): the shell hands over shared memory, one
//! memfd per media stream, on the side channel (a socket it passes as a
//! file descriptor at start). mediad writes the frames there; a frame names
//! the surface and the pixels never travel through the shell.
//!
//! A frame that stops naming a surface is followed by {"t":"drop","id"}
//! (no descriptor): the mapping goes, so photo thumbnails scrolled away
//! cost nothing.
//!
//! Layout as media/surface.go writes it: a 32-byte header ("PHSURF1\0",
//! width, height, latest slot, the two slots' frame numbers, little
//! endian), then two slots of width × height × 3 bytes of RGB.

use std::collections::HashMap;
use std::sync::atomic::{AtomicI32, AtomicU32, Ordering};
use std::sync::{Mutex, OnceLock};

use serde::Deserialize;

const MAGIC: &[u8; 8] = b"PHSURF1\0";
const HEADER: usize = 32;

struct Surface {
    base: usize, // the mapping's address (read-only, shared)
    len: usize,
    w: u32,
    h: u32,
}

fn table() -> &'static Mutex<HashMap<String, Surface>> {
    static T: OnceLock<Mutex<HashMap<String, Surface>>> = OnceLock::new();
    T.get_or_init(Default::default)
}

#[derive(Deserialize)]
struct Hello {
    t: String,
    id: String,
    w: u32,
    h: u32,
}

static SIDE: AtomicI32 = AtomicI32::new(-1);

// Held while messages are taken off the side channel and registered, so a
// pump finds each one either still queued or registered.
fn reading() -> &'static Mutex<()> {
    static R: OnceLock<Mutex<()>> = OnceLock::new();
    R.get_or_init(Default::default)
}

/// Receives surfaces on fd until the shell closes the channel.
pub fn listen(fd: i32) {
    SIDE.store(fd, Ordering::Release);
    std::thread::spawn(move || loop {
        let mut p = libc::pollfd { fd, events: libc::POLLIN, revents: 0 };
        // SAFETY: one pollfd, alive for the call.
        if unsafe { libc::poll(&mut p, 1, -1) } < 0 {
            if std::io::Error::last_os_error().kind() == std::io::ErrorKind::Interrupted {
                continue;
            }
            return;
        }
        if !drain(fd) {
            return;
        }
    });
}

/// Registers what the side channel holds now: a frame names a surface
/// that is not registered yet. The shell sends a surface before the first
/// frame that names it, so it is queued there or being registered; without
/// this the frame (or a snapshot right after it) shows the tile without it,
/// and nothing draws it again.
fn pump() {
    let fd = SIDE.load(Ordering::Acquire);
    if fd >= 0 {
        drain(fd);
    }
}

// drain takes every message queued on fd; false once the shell closed it.
fn drain(fd: i32) -> bool {
    let _g = reading().lock().unwrap();
    loop {
        match recv(fd) {
            Recv::Msg(msg, Some(sfd)) => register(&msg, sfd),
            Recv::Msg(msg, None) => drop_surface(&msg),
            Recv::Empty => return true,
            Recv::Closed => return false,
        }
    }
}

enum Recv {
    Msg(Vec<u8>, Option<i32>),
    Empty,
    Closed,
}

fn recv(fd: i32) -> Recv {
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
        let n = loop {
            let n = libc::recvmsg(fd, &mut m, libc::MSG_CMSG_CLOEXEC | libc::MSG_DONTWAIT);
            if n >= 0 {
                break n;
            }
            match std::io::Error::last_os_error().kind() {
                std::io::ErrorKind::Interrupted => continue,
                std::io::ErrorKind::WouldBlock => return Recv::Empty,
                _ => return Recv::Closed,
            }
        };
        if n == 0 {
            return Recv::Closed;
        }
        let mut got = None;
        let c = libc::CMSG_FIRSTHDR(&m);
        if !c.is_null() && (*c).cmsg_level == libc::SOL_SOCKET && (*c).cmsg_type == libc::SCM_RIGHTS {
            got = Some(std::ptr::read_unaligned(libc::CMSG_DATA(c) as *const i32));
        }
        Recv::Msg(buf[..n as usize].to_vec(), got)
    }
}

fn register(msg: &[u8], fd: i32) {
    let close = || unsafe { libc::close(fd) };
    let Ok(h) = serde_json::from_slice::<Hello>(msg) else {
        close();
        return;
    };
    let len = HEADER + 2 * (h.w as usize) * (h.h as usize) * 3;
    // SAFETY: a fresh read-only shared mapping of the memfd; the shell sealed
    // its size, so the mapping stays valid after the fd is closed.
    let base = unsafe {
        let mut st: libc::stat = std::mem::zeroed();
        if h.t != "surface" || libc::fstat(fd, &mut st) != 0 || (st.st_size as usize) < len {
            close();
            return;
        }
        let p = libc::mmap(std::ptr::null_mut(), len, libc::PROT_READ, libc::MAP_SHARED, fd, 0);
        close();
        if p == libc::MAP_FAILED {
            return;
        }
        p as usize
    };
    let head = unsafe { std::slice::from_raw_parts(base as *const u8, 16) };
    if &head[..8] != MAGIC || u32::from_le_bytes(head[8..12].try_into().unwrap()) != h.w || u32::from_le_bytes(head[12..16].try_into().unwrap()) != h.h {
        unsafe { libc::munmap(base as *mut _, len) };
        return;
    }
    let old = table().lock().unwrap().insert(h.id, Surface { base, len, w: h.w, h: h.h });
    if let Some(o) = old {
        unsafe { libc::munmap(o.base as *mut _, o.len) };
    }
}

#[derive(Deserialize)]
struct Drop {
    t: String,
    id: String,
}

fn drop_surface(msg: &[u8]) {
    let Ok(d) = serde_json::from_slice::<Drop>(msg) else { return };
    if d.t != "drop" {
        return;
    }
    if let Some(o) = table().lock().unwrap().remove(&d.id) {
        unsafe { libc::munmap(o.base as *mut _, o.len) };
    }
}

/// The newest complete frame of the surface a frame names, registering
/// what the side channel holds first if it is not registered yet.
pub fn get(id: &str) -> Option<(u32, u32, Vec<u8>)> {
    rgb(id).or_else(|| {
        pump();
        rgb(id)
    })
}

/// The newest complete frame of a surface: width, height, RGB.
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

#[cfg(test)]
mod tests {
    use super::*;

    fn pair() -> (i32, i32) {
        let mut fds = [0i32; 2];
        // SAFETY: fds has room for the two descriptors.
        assert_eq!(unsafe { libc::socketpair(libc::AF_UNIX, libc::SOCK_SEQPACKET | libc::SOCK_CLOEXEC, 0, fds.as_mut_ptr()) }, 0);
        (fds[0], fds[1])
    }

    // send is the shell's side: a memfd laid out as media/surface.go writes
    // it, slot 0 filled with v, and its hello.
    fn send(sock: i32, id: &str, w: u32, h: u32, v: u8) {
        let size = (w * h * 3) as usize;
        let mut img = vec![0u8; HEADER + 2 * size];
        img[..8].copy_from_slice(MAGIC);
        img[8..12].copy_from_slice(&w.to_le_bytes());
        img[12..16].copy_from_slice(&h.to_le_bytes());
        img[HEADER..HEADER + size].fill(v);
        let hello = format!(r#"{{"t":"surface","id":"{id}","w":{w},"h":{h}}}"#).into_bytes();
        // SAFETY: a fresh memfd written and passed once; the buffers outlive the calls.
        unsafe {
            let fd = libc::memfd_create(c"surface-test".as_ptr(), libc::MFD_CLOEXEC);
            assert!(fd >= 0);
            assert_eq!(libc::write(fd, img.as_ptr().cast(), img.len()), img.len() as isize);
            let mut iov = libc::iovec { iov_base: hello.as_ptr() as *mut _, iov_len: hello.len() };
            let space = libc::CMSG_SPACE(std::mem::size_of::<i32>() as u32) as usize;
            let mut ctl = vec![0u8; space];
            let mut m: libc::msghdr = std::mem::zeroed();
            m.msg_iov = &mut iov;
            m.msg_iovlen = 1;
            m.msg_control = ctl.as_mut_ptr().cast();
            m.msg_controllen = space as _;
            let c = libc::CMSG_FIRSTHDR(&m);
            (*c).cmsg_level = libc::SOL_SOCKET;
            (*c).cmsg_type = libc::SCM_RIGHTS;
            (*c).cmsg_len = libc::CMSG_LEN(std::mem::size_of::<i32>() as u32) as _;
            std::ptr::write_unaligned(libc::CMSG_DATA(c).cast::<i32>(), fd);
            assert!(libc::sendmsg(sock, &m, libc::MSG_NOSIGNAL) > 0);
            libc::close(fd);
        }
    }

    // A frame drawn right after the shell sent the surface it names finds
    // it, whether or not the listener got to it yet (the snapshot of a
    // picker row whose thumbnail had just been made showed its icon).
    #[test]
    fn a_surface_sent_before_the_frame_is_there_for_it() {
        let (shell, side) = pair();
        SIDE.store(side, Ordering::Release);
        send(shell, "t-before", 4, 2, 7);
        let (w, h, px) = get("t-before").expect("the surface the shell sent first");
        assert_eq!((w, h, px.len(), px[0]), (4, 2, 24, 7));

        let (shell, side) = pair();
        listen(side);
        for i in 0..200 {
            let id = format!("t-{i}");
            send(shell, &id, 2, 2, i as u8);
            assert_eq!(get(&id).map(|(_, _, px)| px[0]), Some(i as u8), "surface {id} right after it was sent");
        }
        let drop = br#"{"t":"drop","id":"t-0"}"#;
        // SAFETY: the buffer outlives the call.
        assert!(unsafe { libc::send(shell, drop.as_ptr().cast(), drop.len(), libc::MSG_NOSIGNAL) } > 0);
        pump();
        assert!(rgb("t-0").is_none(), "a dropped surface is gone");
        // SAFETY: the shell's ends, not used after.
        unsafe { libc::close(shell) };
    }
}
