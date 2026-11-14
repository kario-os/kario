//! On the device phone-gui gives up root before Slint starts (while the
//! process is still single-threaded): an empty network namespace, user nobody
//! with only the video and input groups (the panel and the keyboards are
//! 0660 root:video / root:input, and the first opener of the panel becomes
//! its DRM master without privileges), no_new_privs, and with Landlock no
//! files but fonts, icons, the device nodes and what libinput reads.

use std::ffi::CString;
use std::time::Duration;

fn group(name: &str) -> Option<libc::gid_t> {
    let c = CString::new(name).ok()?;
    // SAFETY: getgrnam returns a pointer into static storage or null.
    let g = unsafe { libc::getgrnam(c.as_ptr()) };
    if g.is_null() { None } else { Some(unsafe { (*g).gr_gid }) }
}

pub fn enter() -> Result<Vec<&'static str>, String> {
    const NOBODY: libc::uid_t = 65534;
    let err = |what: &str| format!("{what}: {}", std::io::Error::last_os_error());
    let groups: Vec<libc::gid_t> = ["video", "input"].iter().filter_map(|g| group(g)).collect();
    // SAFETY: plain system calls while the process is single-threaded.
    unsafe {
        if libc::geteuid() != 0 {
            return Ok(Vec::new());
        }
        if libc::unshare(libc::CLONE_NEWNET) != 0 {
            return Err(err("unshare(CLONE_NEWNET)"));
        }
        if libc::setgroups(groups.len(), groups.as_ptr()) != 0 {
            return Err(err("setgroups"));
        }
        if libc::setgid(NOBODY) != 0 || libc::setuid(NOBODY) != 0 {
            return Err(err("setuid nobody"));
        }
        if libc::prctl(libc::PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0) != 0 {
            return Err(err("no_new_privs"));
        }
        if libc::setuid(0) == 0 {
            return Err("could become root again".into());
        }
    }
    let mut got = vec!["sandboxed"];
    if restrict_files()? {
        got.push("landlock");
    }
    if std::fs::File::open("/etc/shadow").is_ok() {
        return Err("can still read /etc/shadow".into());
    }
    if got.contains(&"landlock") && std::fs::File::open("/etc/passwd").is_ok() {
        return Err("Landlock does not hold: /etc/passwd is readable".into());
    }
    if std::net::TcpStream::connect_timeout(&"127.0.0.1:22".parse().unwrap(), Duration::from_millis(300)).is_ok() {
        return Err("can still reach the network".into());
    }
    crate::seccomp::apply()?;
    got.push("seccomp");
    Ok(got)
}

fn restrict_files() -> Result<bool, String> {
    use landlock::{path_beneath_rules, Access, AccessFs, Ruleset, RulesetAttr, RulesetCreatedAttr, RulesetStatus, ABI};
    let s = |e: landlock::RulesetError| e.to_string();
    let abi = ABI::V3;
    let read = ["/usr", "/var/lib/phone/packages", "/sys", "/run/udev", "/etc/udev"];
    let devices = ["/dev/dri", "/dev/input"];
    let status = Ruleset::default()
        .handle_access(AccessFs::from_all(abi))
        .map_err(s)?
        .create()
        .map_err(s)?
        .add_rules(path_beneath_rules(read, AccessFs::from_read(abi)))
        .map_err(s)?
        .add_rules(path_beneath_rules(devices, AccessFs::from_read(abi) | AccessFs::WriteFile))
        .map_err(s)?
        .restrict_self()
        .map_err(s)?;
    Ok(status.ruleset != RulesetStatus::NotEnforced)
}
