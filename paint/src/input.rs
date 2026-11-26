//! Input devices, there at boot or plugged in later (a USB keyboard or
//! mouse on the USB-C port). The renderer gives up its privileges after
//! start and can then open nothing under /dev, so a helper process forked
//! before that opens the devices and passes them over a socket, as seatd
//! and logind do for compositors. The helper keeps only the capability to
//! read device nodes, runs as nobody with no network and with Landlock
//! allowing nothing but /dev/input, and takes nothing from the renderer: a
//! compromised renderer cannot make it open anything else.

use std::collections::{HashMap, HashSet};
use std::os::fd::{AsRawFd, FromRawFd, OwnedFd, RawFd};

use evdev::{KeyCode, RelativeAxisCode};

const DIR: &str = "/dev/input";

#[derive(Clone, Copy, PartialEq, Debug)]
pub enum Kind {
    Keyboard,
    Mouse,
    Touchscreen,
    SoundSwitch,
}

impl Kind {
    pub fn name(self) -> &'static str {
        match self {
            Kind::Keyboard => "keyboard",
            Kind::Mouse => "mouse",
            Kind::Touchscreen => "touchscreen",
            Kind::SoundSwitch => "sound_switch",
        }
    }
    fn byte(self) -> u8 {
        match self {
            Kind::Keyboard => b'k',
            Kind::Mouse => b'm',
            Kind::Touchscreen => b't',
            Kind::SoundSwitch => b's',
        }
    }
    fn from_byte(b: u8) -> Option<Kind> {
        match b {
            b'k' => Some(Kind::Keyboard),
            b'm' => Some(Kind::Mouse),
            b't' => Some(Kind::Touchscreen),
            b's' => Some(Kind::SoundSwitch),
            _ => None,
        }
    }
}

/// What a device is to the phone: a keyboard has Enter (or is the board's
/// power key, a device of its own, or has a key of the board's keymap), a
/// mouse a left button or a wheel (a keyboard with a touchpad is a
/// keyboard). A touchscreen is one only where the phone has one (touch,
/// the SKU's); elsewhere it is nothing, not a mouse for its buttons
/// (QEMU's virtio-multitouch reports BTN_LEFT). The sound switch is the
/// device the SKU names (sound_switch.rs).
pub fn kind(d: &evdev::Device, keypad: &[u16], touch: bool, sound_switch: &str) -> Option<Kind> {
    if crate::sound_switch::is_switch(d, sound_switch) {
        return Some(Kind::SoundSwitch);
    }
    let keys = d.supported_keys();
    if keys.is_some_and(|k| keyboard(|c| k.contains(c), keypad)) {
        return Some(Kind::Keyboard);
    }
    if crate::touch::is_touchscreen(d) {
        return touch.then_some(Kind::Touchscreen);
    }
    if keys.is_some_and(|k| k.contains(KeyCode::BTN_LEFT))
        || d.supported_relative_axes().is_some_and(|r| r.contains(RelativeAxisCode::REL_WHEEL))
    {
        return Some(Kind::Mouse);
    }
    None
}

/// A phone's own keys come as several devices: a power key, the volume
/// keys (gpio-keys, the PMIC's) each may be a device of its own, and a
/// matrix keypad (keypad: its keymap's codes) may have no Enter.
fn keyboard(has: impl Fn(KeyCode) -> bool, keypad: &[u16]) -> bool {
    [KeyCode::KEY_ENTER, KeyCode::KEY_POWER, KeyCode::KEY_VOLUMEUP, KeyCode::KEY_VOLUMEDOWN].into_iter().any(&has)
        || keypad.iter().any(|&c| has(KeyCode::new(c)))
}

/// Whether a device can press OK (the shell's "ok" of an input device): a
/// key read as Enter, its own or by the board's keymap (code -> key), or a
/// mouse's left button. Without one, as with the power and volume keys,
/// the shell lets the power key answer its power menu.
pub fn presses_ok(has: impl Fn(KeyCode) -> bool, keymap: &HashMap<u16, u16>) -> bool {
    [KeyCode::KEY_ENTER, KeyCode::KEY_KPENTER, KeyCode::BTN_LEFT].into_iter().any(&has)
        || keymap.iter().any(|(&code, &key)| key == KeyCode::KEY_ENTER.code() && has(KeyCode::new(code)))
}

fn err(what: &str) -> String {
    format!("{what}: {}", std::io::Error::last_os_error())
}

/// Starts the opener; the returned socket delivers each device (keypad:
/// the codes of the board's keymap, touch: the phone has a touchscreen,
/// sound_switch: its sound switch's device name, all from the handshake). Must run while the process is
/// single-threaded and, to fork the privileged helper, before privileges
/// are dropped and before the panel is opened: a child
/// forked later keeps the panel's scan-out buffers mapped, and with them
/// the display's DRM master, for as long as it lives. Without root (a
/// developer machine) the opener is a thread of the renderer.
pub fn start(keypad: Vec<u16>, touch: bool, sound_switch: String) -> Result<OwnedFd, String> {
    let mut sv = [0 as RawFd; 2];
    // SAFETY: plain system calls; the descriptors are owned right after.
    let (ours, theirs) = unsafe {
        if libc::socketpair(libc::AF_UNIX, libc::SOCK_SEQPACKET | libc::SOCK_CLOEXEC, 0, sv.as_mut_ptr()) != 0 {
            return Err(err("socketpair"));
        }
        (OwnedFd::from_raw_fd(sv[0]), OwnedFd::from_raw_fd(sv[1]))
    };
    let watch = Watch::new(DIR)?;
    // SAFETY: geteuid has no preconditions.
    if unsafe { libc::geteuid() } != 0 {
        std::thread::spawn(move || opener(watch, theirs, &keypad, touch, &sound_switch));
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
            if !die_with(libc::SIGKILL, parent) {
                // SAFETY: ends the child.
                unsafe { libc::_exit(0) };
            }
            opener(watch, theirs, &keypad, touch, &sound_switch);
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

/// Sets the signal this process gets when parent, which forked it, ends:
/// the kernel clears it when the process changes its uid or gid
/// (credentials(7)), as both sandboxes do. False when parent has ended
/// already.
pub fn die_with(sig: libc::c_int, parent: libc::pid_t) -> bool {
    // SAFETY: plain system calls.
    unsafe {
        libc::prctl(libc::PR_SET_PDEATHSIG, sig);
        libc::getppid() == parent
    }
}

/// Closes every descriptor but stdio and keep.
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

/// An inotify watch on the devices' directory (/dev/input), made before
/// Landlock applies.
struct Watch {
    fd: OwnedFd,
    dir: String,
}

impl Watch {
    fn new(dir: &str) -> Result<Watch, String> {
        // SAFETY: plain system calls; the descriptor is owned right after.
        unsafe {
            let fd = libc::inotify_init1(libc::IN_CLOEXEC);
            if fd < 0 {
                return Err(err("inotify"));
            }
            let fd = OwnedFd::from_raw_fd(fd);
            let path = std::ffi::CString::new(dir).unwrap();
            if libc::inotify_add_watch(fd.as_raw_fd(), path.as_ptr(), libc::IN_CREATE | libc::IN_ATTRIB | libc::IN_DELETE) < 0 {
                return Err(err(&format!("inotify_add_watch {dir}")));
            }
            Ok(Watch { fd, dir: dir.to_string() })
        }
    }

    /// The next names created, changed (udev set their mode) or deleted;
    /// None once the renderer's end of sock is closed: the renderer sends
    /// nothing on it, so anything there is its end.
    fn next(&self, sock: &OwnedFd) -> Option<Vec<(String, bool)>> {
        let mut fds = [
            libc::pollfd { fd: self.fd.as_raw_fd(), events: libc::POLLIN, revents: 0 },
            libc::pollfd { fd: sock.as_raw_fd(), events: libc::POLLIN, revents: 0 },
        ];
        loop {
            // SAFETY: two pollfds that live through the call.
            if unsafe { libc::poll(fds.as_mut_ptr(), 2, -1) } < 0 {
                if std::io::Error::last_os_error().kind() == std::io::ErrorKind::Interrupted {
                    continue;
                }
                return None;
            }
            if fds[1].revents != 0 || fds[0].revents & !libc::POLLIN != 0 {
                return None;
            }
            if fds[0].revents != 0 {
                break;
            }
        }
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

fn opener(watch: Watch, sock: OwnedFd, keypad: &[u16], touch: bool, sound_switch: &str) {
    let mut seen = HashSet::new();
    let mut names: Vec<String> = std::fs::read_dir(&watch.dir)
        .map(|d| d.filter_map(|e| e.ok()?.file_name().into_string().ok()).collect())
        .unwrap_or_default();
    names.sort();
    for name in names {
        if offer(&watch.dir, &name, &mut seen, &sock, keypad, touch, sound_switch).is_err() {
            return;
        }
    }
    while let Some(events) = watch.next(&sock) {
        for (name, deleted) in events {
            if deleted {
                seen.remove(&name);
            } else if offer(&watch.dir, &name, &mut seen, &sock, keypad, touch, sound_switch).is_err() {
                return;
            }
        }
    }
}

/// Opens an event device of dir not offered yet and sends it if it is a
/// keyboard, a mouse, a touchscreen or the sound switch; Err when the
/// renderer is gone.
fn offer(dir: &str, name: &str, seen: &mut HashSet<String>, sock: &OwnedFd, keypad: &[u16], touch: bool, sound_switch: &str) -> Result<(), ()> {
    if !name.starts_with("event") || seen.contains(name) {
        return Ok(());
    }
    let path = std::ffi::CString::new(format!("{dir}/{name}")).unwrap();
    // SAFETY: opening a path; the descriptor is owned right after.
    let fd = unsafe { libc::open(path.as_ptr(), libc::O_RDONLY | libc::O_CLOEXEC) };
    if fd < 0 {
        return Ok(());
    }
    // SAFETY: fd was just opened and is owned by nothing else.
    let Ok(dev) = evdev::Device::from_fd(unsafe { OwnedFd::from_raw_fd(fd) }) else { return Ok(()) };
    seen.insert(name.to_string());
    let Some(k) = kind(&dev, keypad, touch, sound_switch) else { return Ok(()) };
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
    /// The finger a touchscreen follows, at this pixel of the frame
    /// (touch.rs), and the report's time (perf.rs).
    Touch(crate::touch::Phase, f32, f32, crate::perf::Stamp),
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
    fn a_matrix_keypad_is_a_keyboard_by_its_keymap() {
        // KEY_MENU, KEY_NUMERIC_1 and KEY_OK, no Enter: a KaiOS phone's keypad.
        let pad = [KeyCode::KEY_MENU, KeyCode::KEY_NUMERIC_1, KeyCode::KEY_OK];
        let has = |c: KeyCode| pad.contains(&c);
        assert!(!keyboard(has, &[]));
        assert!(keyboard(has, &[KeyCode::KEY_MENU.code()]));
        assert!(!keyboard(has, &[KeyCode::KEY_SEND.code()]));
        assert!(keyboard(|c| c == KeyCode::KEY_ENTER, &[]));
        assert!(keyboard(|c| c == KeyCode::KEY_POWER, &[]));
    }

    #[test]
    fn the_phones_buttons_cannot_press_ok() {
        let none = HashMap::new();
        // The OnePlus 6T's: pm8941_pwrkey, the volume keys.
        assert!(!presses_ok(|c| c == KeyCode::KEY_POWER, &none));
        assert!(!presses_ok(|c| c == KeyCode::KEY_VOLUMEUP || c == KeyCode::KEY_VOLUMEDOWN, &none));
        assert!(presses_ok(|c| c == KeyCode::KEY_ENTER || c == KeyCode::KEY_A, &none));
        assert!(presses_ok(|c| c == KeyCode::KEY_KPENTER, &none));
        assert!(presses_ok(|c| c == KeyCode::BTN_LEFT, &none));
        // A KaiOS keypad's KEY_OK, read as Enter by the board's keymap.
        let keymap = HashMap::from([(KeyCode::KEY_OK.code(), KeyCode::KEY_ENTER.code())]);
        assert!(!presses_ok(|c| c == KeyCode::KEY_OK, &none));
        assert!(presses_ok(|c| c == KeyCode::KEY_OK, &keymap));
        assert!(!presses_ok(|c| c == KeyCode::KEY_POWER, &keymap));
    }

    fn pair() -> (OwnedFd, OwnedFd) {
        let mut sv = [0 as RawFd; 2];
        // SAFETY: a socket pair owned right after.
        unsafe {
            assert_eq!(libc::socketpair(libc::AF_UNIX, libc::SOCK_SEQPACKET | libc::SOCK_CLOEXEC, 0, sv.as_mut_ptr()), 0);
            (OwnedFd::from_raw_fd(sv[0]), OwnedFd::from_raw_fd(sv[1]))
        }
    }

    #[test]
    fn descriptors_cross_the_socket() {
        let (a, b) = pair();
        let f = std::fs::File::open("/dev/null").unwrap();
        send_fd(&a, f.as_raw_fd(), b'k').unwrap();
        drop(a);
        // /dev/null is no event device: recv skips it and ends with the socket.
        assert!(recv(&b).is_none());
    }

    // The opener ends once the renderer's end of the socket is closed, with
    // no device coming or going: it used to wait for the next device, and
    // on the phone outlived the renderer holding the panel.
    #[test]
    fn the_opener_ends_with_the_renderer() {
        let dir = std::env::temp_dir().join(format!("paint-opener-{}", std::process::id()));
        std::fs::create_dir_all(&dir).unwrap();
        let watch = Watch::new(dir.to_str().unwrap()).unwrap();
        let (ours, theirs) = pair();
        let (done, ended) = std::sync::mpsc::channel();
        std::thread::spawn(move || {
            opener(watch, theirs, &[], false, "");
            let _ = done.send(());
        });
        std::thread::sleep(std::time::Duration::from_millis(100));
        drop(ours);
        let gone = ended.recv_timeout(std::time::Duration::from_secs(5)).is_ok();
        let _ = std::fs::remove_dir_all(&dir);
        assert!(gone, "the opener still waits for devices with the renderer gone");
    }

    fn alive(pid: i32) -> bool {
        std::fs::read_to_string(format!("/proc/{pid}/stat")).is_ok_and(|s| s.rsplit(") ").next().is_some_and(|t| !t.starts_with('Z')))
    }

    // As root the opener is a process of its own that becomes nobody, which
    // clears the parent-death signal it set: set again, it ends with the
    // renderer though the renderer's end of the socket lives on in another
    // process. Root only: the phone, QEMU, or a user namespace that maps
    // nobody (podman unshare cargo test --release).
    #[test]
    fn the_opener_dies_with_the_renderer() {
        // SAFETY: geteuid has no preconditions.
        if unsafe { libc::geteuid() } != 0 {
            eprintln!("not root: skipped");
            return;
        }
        if std::env::var_os("PAINT_OPENER_CHILD").is_some() {
            let sock = start(Vec::new(), false, String::new()).expect("start");
            // SAFETY: getpid has no preconditions.
            let me = unsafe { libc::getpid() };
            let opener = std::fs::read_dir("/proc")
                .unwrap()
                .filter_map(|e| e.ok()?.file_name().to_str()?.parse::<i32>().ok())
                .find(|p| std::fs::read_to_string(format!("/proc/{p}/stat")).is_ok_and(|s| s.rsplit(") ").next().and_then(|t| t.split(' ').nth(1)) == Some(&me.to_string())))
                .expect("the opener");
            // SAFETY: the holder only sleeps, keeping sock open (and not
            // the test's output).
            let holder = unsafe { libc::fork() };
            if holder == 0 {
                unsafe {
                    libc::close(1);
                    libc::sleep(30);
                    libc::_exit(0)
                };
            }
            drop(sock);
            println!("\nopener {opener} holder {holder}");
            std::process::exit(0);
        }
        // The opener's output is the stand-in's stderr: not the pipe read
        // here, which an opener left over would keep open.
        let out = std::process::Command::new(std::env::current_exe().unwrap())
            .args(["--exact", "input::tests::the_opener_dies_with_the_renderer", "--nocapture", "--test-threads=1"])
            .env("PAINT_OPENER_CHILD", "1")
            .stderr(std::process::Stdio::null())
            .output()
            .unwrap();
        let text = String::from_utf8_lossy(&out.stdout);
        let Some(line) = text.lines().find(|l| l.starts_with("opener ")) else {
            panic!("the renderer's stand-in: {text}");
        };
        let f: Vec<i32> = line.split(' ').filter_map(|w| w.parse().ok()).collect();
        let (opener, holder) = (f[0], f[1]);
        let end = std::time::Instant::now() + std::time::Duration::from_secs(5);
        while alive(opener) && std::time::Instant::now() < end {
            std::thread::sleep(std::time::Duration::from_millis(50));
        }
        let left = alive(opener);
        // SAFETY: signals the two processes the stand-in left.
        unsafe {
            libc::kill(holder, libc::SIGKILL);
            libc::kill(opener, libc::SIGKILL);
        }
        assert!(!left, "the opener {opener} outlived the renderer");
    }
}
