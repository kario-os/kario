//! Input devices, there at boot or plugged in later (a USB keyboard or
//! mouse on the USB-C port). The renderer gives up its privileges after
//! start and can then open nothing under /dev, so a helper process forked
//! before that opens the devices and passes them over a socket, as seatd
//! and logind do for compositors. The helper keeps only the capability to
//! read device nodes, runs as nobody with no network and with Landlock
//! allowing nothing but /dev/input, and takes nothing from the renderer: a
//! compromised renderer cannot make it open anything else.

use std::collections::HashSet;
use std::os::fd::{AsRawFd, FromRawFd, OwnedFd, RawFd};

use evdev::{KeyCode, RelativeAxisCode};

const DIR: &str = "/dev/input";

#[derive(Clone, Copy, PartialEq, Debug)]
pub enum Kind {
    Keyboard,
    Mouse,
}

impl Kind {
    pub fn name(self) -> &'static str {
        match self {
            Kind::Keyboard => "keyboard",
            Kind::Mouse => "mouse",
        }
    }
    fn byte(self) -> u8 {
        match self {
            Kind::Keyboard => b'k',
            Kind::Mouse => b'm',
        }
    }
    fn from_byte(b: u8) -> Option<Kind> {
        match b {
            b'k' => Some(Kind::Keyboard),
            b'm' => Some(Kind::Mouse),
            _ => None,
        }
    }
}

/// What a device is to the phone: a keyboard has Enter (or is the board's
/// power key, a device of its own), a mouse a left button or a wheel (a
/// keyboard with a touchpad is a keyboard).
pub fn kind(d: &evdev::Device) -> Option<Kind> {
    let keys = d.supported_keys();
    // A phone's own keys come as several devices: a power key, the volume
    // keys (gpio-keys, the PMIC's) each may be a device of its own.
    if keys.is_some_and(|k| {
        k.contains(KeyCode::KEY_ENTER) || k.contains(KeyCode::KEY_POWER) || k.contains(KeyCode::KEY_VOLUMEUP) || k.contains(KeyCode::KEY_VOLUMEDOWN)
    }) {
        return Some(Kind::Keyboard);
    }
    if keys.is_some_and(|k| k.contains(KeyCode::BTN_LEFT))
        || d.supported_relative_axes().is_some_and(|r| r.contains(RelativeAxisCode::REL_WHEEL))
    {
        return Some(Kind::Mouse);
    }
    None
}

fn err(what: &str) -> String {
    format!("{what}: {}", std::io::Error::last_os_error())
}

/// Starts the opener; the returned socket delivers each device. Must run
/// while the process is single-threaded and, to fork the privileged
/// helper, before privileges are dropped. Without root (a developer
/// machine) the opener is a thread of the renderer.
pub fn start() -> Result<OwnedFd, String> {
    let mut sv = [0 as RawFd; 2];
    // SAFETY: plain system calls; the descriptors are owned right after.
    let (ours, theirs) = unsafe {
        if libc::socketpair(libc::AF_UNIX, libc::SOCK_SEQPACKET | libc::SOCK_CLOEXEC, 0, sv.as_mut_ptr()) != 0 {
            return Err(err("socketpair"));
        }
        (OwnedFd::from_raw_fd(sv[0]), OwnedFd::from_raw_fd(sv[1]))
    };
    let watch = Watch::new()?;
    // SAFETY: geteuid has no preconditions.
    if unsafe { libc::geteuid() } != 0 {
        std::thread::spawn(move || opener(watch, theirs));
        return Ok(ours);
    }
    // SAFETY: the process is single-threaded here (the caller's promise),
    // so the child may run ordinary code.
    let parent = unsafe { libc::getpid() };
    match unsafe { libc::fork() } {
        -1 => Err(err("fork")),
        0 => {
            drop(ours);
            // SAFETY: system calls on the child's only thread.
            unsafe {
                libc::prctl(libc::PR_SET_PDEATHSIG, libc::SIGKILL);
                if libc::getppid() != parent {
                    libc::_exit(0);
                }
                // stdout is the shell's protocol channel.
                libc::dup2(2, 1);
            }
            close_others(&[theirs.as_raw_fd(), watch.fd.as_raw_fd()]);
            if let Err(e) = confine() {
                eprintln!("phone-paint: input opener: {e}");
                // SAFETY: ends the child.
                unsafe { libc::_exit(1) };
            }
            opener(watch, theirs);
            // SAFETY: ends the child.
            unsafe { libc::_exit(0) }
        }
        _ => {
            drop(theirs);
            drop(watch);
            Ok(ours)
        }
    }
}

/// Closes every descriptor but stdio and keep (the panel's included).
fn close_others(keep: &[RawFd]) {
    let fds: Vec<RawFd> = std::fs::read_dir("/proc/self/fd")
        .map(|d| d.filter_map(|e| e.ok()?.file_name().to_str()?.parse().ok()).collect())
        .unwrap_or_default();
    for fd in fds {
        if fd > 2 && !keep.contains(&fd) {
            // SAFETY: closing a descriptor this process no longer uses.
            unsafe { libc::close(fd) };
        }
    }
}

/// nobody, with only CAP_DAC_READ_SEARCH (reading the root-owned device
/// nodes), no network, no new privileges, Landlock: /dev/input read-only.
fn confine() -> Result<(), String> {
    const NOBODY: libc::uid_t = 65534;
    const CAP_DAC_READ_SEARCH: u32 = 2;
    #[repr(C)]
    struct CapHeader {
        version: u32,
        pid: i32,
    }
    #[repr(C)]
    struct CapData {
        effective: u32,
        permitted: u32,
        inheritable: u32,
    }
    // SAFETY: system calls on the child's only thread, with valid pointers.
    unsafe {
        if libc::unshare(libc::CLONE_NEWNET) != 0 {
            return Err(err("unshare(CLONE_NEWNET)"));
        }
        if libc::prctl(libc::PR_SET_KEEPCAPS, 1, 0, 0, 0) != 0 {
            return Err(err("keepcaps"));
        }
        if libc::setgroups(0, std::ptr::null()) != 0 || libc::setgid(NOBODY) != 0 || libc::setuid(NOBODY) != 0 {
            return Err(err("setuid nobody"));
        }
        let mut hdr = CapHeader { version: 0x2008_0522, pid: 0 };
        let bit = 1 << CAP_DAC_READ_SEARCH;
        let data = [CapData { effective: bit, permitted: bit, inheritable: 0 }, CapData { effective: 0, permitted: 0, inheritable: 0 }];
        if libc::syscall(libc::SYS_capset, &mut hdr as *mut CapHeader, data.as_ptr()) != 0 {
            return Err(err("capset"));
        }
        if libc::prctl(libc::PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0) != 0 {
            return Err(err("no_new_privs"));
        }
    }
    use landlock::{path_beneath_rules, Access, AccessFs, Ruleset, RulesetAttr, RulesetCreatedAttr, ABI};
    let abi = ABI::V3;
    Ruleset::default()
        .handle_access(AccessFs::from_all(abi))
        .and_then(|r| r.create())
        .and_then(|r| r.add_rules(path_beneath_rules([DIR], AccessFs::from_read(abi))))
        .and_then(|r| r.restrict_self())
        .map_err(|e| e.to_string())?;
    if std::fs::File::open("/etc/passwd").is_ok() && std::fs::File::open("/etc/shadow").is_ok() {
        return Err("still reads /etc/shadow".into());
    }
    Ok(())
}

/// An inotify watch on /dev/input, made before Landlock applies.
struct Watch {
    fd: OwnedFd,
}

impl Watch {
    fn new() -> Result<Watch, String> {
        // SAFETY: plain system calls; the descriptor is owned right after.
        unsafe {
            let fd = libc::inotify_init1(libc::IN_CLOEXEC);
            if fd < 0 {
                return Err(err("inotify"));
            }
            let fd = OwnedFd::from_raw_fd(fd);
            let dir = std::ffi::CString::new(DIR).unwrap();
            if libc::inotify_add_watch(fd.as_raw_fd(), dir.as_ptr(), libc::IN_CREATE | libc::IN_ATTRIB | libc::IN_DELETE) < 0 {
                return Err(err("inotify_add_watch /dev/input"));
            }
            Ok(Watch { fd })
        }
    }

    /// The next names created, changed (udev set their mode) or deleted.
    fn next(&self) -> Option<Vec<(String, bool)>> {
        let mut buf = [0u8; 4096];
        // SAFETY: reading into a buffer of the given size.
        let n = unsafe { libc::read(self.fd.as_raw_fd(), buf.as_mut_ptr().cast(), buf.len()) };
        if n <= 0 {
            return None;
        }
        let (mut out, mut off) = (Vec::new(), 0usize);
        let head = std::mem::size_of::<libc::inotify_event>();
        while off + head <= n as usize {
            // SAFETY: the kernel wrote a whole event at off.
            let ev: libc::inotify_event = unsafe { std::ptr::read_unaligned(buf[off..].as_ptr().cast()) };
            let name = &buf[off + head..off + head + ev.len as usize];
            let name = String::from_utf8_lossy(name.split(|&b| b == 0).next().unwrap_or_default()).into_owned();
            out.push((name, ev.mask & libc::IN_DELETE != 0));
            off += head + ev.len as usize;
        }
        Some(out)
    }
}

fn opener(watch: Watch, sock: OwnedFd) {
    let mut seen = HashSet::new();
    let mut names: Vec<String> = std::fs::read_dir(DIR)
        .map(|d| d.filter_map(|e| e.ok()?.file_name().into_string().ok()).collect())
        .unwrap_or_default();
    names.sort();
    for name in names {
        if offer(&name, &mut seen, &sock).is_err() {
            return;
        }
    }
    while let Some(events) = watch.next() {
        for (name, deleted) in events {
            if deleted {
                seen.remove(&name);
            } else if offer(&name, &mut seen, &sock).is_err() {
                return;
            }
        }
    }
}

/// Opens an event device not offered yet and sends it if it is a keyboard
/// or a mouse; Err when the renderer is gone.
fn offer(name: &str, seen: &mut HashSet<String>, sock: &OwnedFd) -> Result<(), ()> {
    if !name.starts_with("event") || seen.contains(name) {
        return Ok(());
    }
    let path = std::ffi::CString::new(format!("{DIR}/{name}")).unwrap();
    // SAFETY: opening a path; the descriptor is owned right after.
    let fd = unsafe { libc::open(path.as_ptr(), libc::O_RDONLY | libc::O_CLOEXEC) };
    if fd < 0 {
        return Ok(());
    }
    // SAFETY: fd was just opened and is owned by nothing else.
    let Ok(dev) = evdev::Device::from_fd(unsafe { OwnedFd::from_raw_fd(fd) }) else { return Ok(()) };
    seen.insert(name.to_string());
    let Some(k) = kind(&dev) else { return Ok(()) };
    send_fd(sock, dev.as_raw_fd(), k.byte()).map_err(|_| ())
}

fn send_fd(sock: &OwnedFd, fd: RawFd, tag: u8) -> std::io::Result<()> {
    let mut data = [tag];
    let mut iov = libc::iovec { iov_base: data.as_mut_ptr().cast(), iov_len: 1 };
    // SAFETY: a control buffer big enough for one descriptor, filled
    // through the CMSG macros.
    unsafe {
        let space = libc::CMSG_SPACE(std::mem::size_of::<RawFd>() as u32) as usize;
        let mut ctl = vec![0u8; space];
        let mut msg: libc::msghdr = std::mem::zeroed();
        msg.msg_iov = &mut iov;
        msg.msg_iovlen = 1;
        msg.msg_control = ctl.as_mut_ptr().cast();
        msg.msg_controllen = space as _;
        let c = libc::CMSG_FIRSTHDR(&msg);
        (*c).cmsg_level = libc::SOL_SOCKET;
        (*c).cmsg_type = libc::SCM_RIGHTS;
        (*c).cmsg_len = libc::CMSG_LEN(std::mem::size_of::<RawFd>() as u32) as _;
        std::ptr::write_unaligned(libc::CMSG_DATA(c).cast::<RawFd>(), fd);
        if libc::sendmsg(sock.as_raw_fd(), &msg, libc::MSG_NOSIGNAL) < 0 {
            return Err(std::io::Error::last_os_error());
        }
    }
    Ok(())
}

/// The next device from the opener; None once it is gone.
pub fn recv(sock: &OwnedFd) -> Option<(evdev::Device, Kind)> {
    loop {
        let mut data = [0u8; 1];
        let mut iov = libc::iovec { iov_base: data.as_mut_ptr().cast(), iov_len: 1 };
        // SAFETY: as in send_fd; a descriptor that arrives is owned at once.
        let fd = unsafe {
            let space = libc::CMSG_SPACE(std::mem::size_of::<RawFd>() as u32) as usize;
            let mut ctl = vec![0u8; space];
            let mut msg: libc::msghdr = std::mem::zeroed();
            msg.msg_iov = &mut iov;
            msg.msg_iovlen = 1;
            msg.msg_control = ctl.as_mut_ptr().cast();
            msg.msg_controllen = space as _;
            if libc::recvmsg(sock.as_raw_fd(), &mut msg, libc::MSG_CMSG_CLOEXEC) <= 0 {
                return None;
            }
            let c = libc::CMSG_FIRSTHDR(&msg);
            if c.is_null() || (*c).cmsg_type != libc::SCM_RIGHTS {
                continue;
            }
            OwnedFd::from_raw_fd(std::ptr::read_unaligned(libc::CMSG_DATA(c).cast::<RawFd>()))
        };
        let Some(kind) = Kind::from_byte(data[0]) else { continue };
        if let Ok(dev) = evdev::Device::from_fd(fd) {
            return Some((dev, kind));
        }
    }
}

/// What reaches the renderer's main loop besides the keys (those go to the
/// shell directly): the shell's lines, and what moves the pointer.
pub enum Event {
    Line(String),
    /// The shell closed our input.
    Eof,
    /// The mouse moved by this many counts (one count, one panel pixel).
    Move(i32, i32),
    /// The left button went down: a click where the pointer is, or OK.
    Click,
    /// A key was typed: the pointer hides until the mouse moves again.
    Typed,
}

/// What a mouse does on a phone without a pointer: the wheel moves the
/// focus, the left button is OK, the right one Back, the middle one the
/// options key.
pub fn button(code: KeyCode) -> Option<&'static str> {
    match code {
        KeyCode::BTN_LEFT => Some("enter"),
        KeyCode::BTN_RIGHT => Some("esc"),
        KeyCode::BTN_MIDDLE => Some("tab"),
        _ => None,
    }
}

pub fn wheel(code: RelativeAxisCode, value: i32) -> Option<(&'static str, u32)> {
    let steps = value.unsigned_abs().min(10);
    match code {
        RelativeAxisCode::REL_WHEEL if value > 0 => Some(("up", steps)),
        RelativeAxisCode::REL_WHEEL if value < 0 => Some(("down", steps)),
        RelativeAxisCode::REL_HWHEEL if value > 0 => Some(("right", steps)),
        RelativeAxisCode::REL_HWHEEL if value < 0 => Some(("left", steps)),
        _ => None,
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_mouse_is_a_wheel_and_three_keys() {
        assert_eq!(wheel(RelativeAxisCode::REL_WHEEL, -2), Some(("down", 2)));
        assert_eq!(wheel(RelativeAxisCode::REL_WHEEL, 1), Some(("up", 1)));
        assert_eq!(wheel(RelativeAxisCode::REL_HWHEEL, 1), Some(("right", 1)));
        assert_eq!(wheel(RelativeAxisCode::REL_X, 5), None);
        assert_eq!(button(KeyCode::BTN_LEFT), Some("enter"));
        assert_eq!(button(KeyCode::BTN_RIGHT), Some("esc"));
    }

    #[test]
    fn descriptors_cross_the_socket() {
        let mut sv = [0 as RawFd; 2];
        // SAFETY: a socket pair owned right after.
        let (a, b) = unsafe {
            assert_eq!(libc::socketpair(libc::AF_UNIX, libc::SOCK_SEQPACKET | libc::SOCK_CLOEXEC, 0, sv.as_mut_ptr()), 0);
            (OwnedFd::from_raw_fd(sv[0]), OwnedFd::from_raw_fd(sv[1]))
        };
        let f = std::fs::File::open("/dev/null").unwrap();
        send_fd(&a, f.as_raw_fd(), b'k').unwrap();
        drop(a);
        // /dev/null is no event device: recv skips it and ends with the socket.
        assert!(recv(&b).is_none());
    }
}
