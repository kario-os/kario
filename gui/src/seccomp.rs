//! The last layer of phone-gui's sandbox: a seccomp filter. It refuses what
//! the app sandbox refuses (fw/sandbox/seccomp_linux.go) and running programs
//! (execve). Unlike phone-paint it keeps sockets: libinput watches udev over
//! netlink. The empty network namespace still keeps it off the network.

#[cfg(target_arch = "aarch64")]
const AUDIT_ARCH: u32 = 0xC000_00B7;
#[cfg(target_arch = "x86_64")]
const AUDIT_ARCH: u32 = 0xC000_003E;

fn denied() -> Vec<libc::c_long> {
    let mut v = vec![
        libc::SYS_ptrace, libc::SYS_process_vm_readv, libc::SYS_process_vm_writev, libc::SYS_pidfd_getfd,
        libc::SYS_mount, libc::SYS_umount2, libc::SYS_pivot_root, libc::SYS_chroot,
        libc::SYS_fsopen, libc::SYS_fsconfig, libc::SYS_fsmount, libc::SYS_fspick,
        libc::SYS_move_mount, libc::SYS_open_tree, libc::SYS_mount_setattr,
        libc::SYS_unshare, libc::SYS_setns,
        libc::SYS_init_module, libc::SYS_finit_module, libc::SYS_delete_module,
        libc::SYS_kexec_load, libc::SYS_kexec_file_load, libc::SYS_reboot,
        libc::SYS_bpf, libc::SYS_perf_event_open, libc::SYS_userfaultfd,
        libc::SYS_keyctl, libc::SYS_add_key, libc::SYS_request_key,
        libc::SYS_io_uring_setup, libc::SYS_io_uring_enter, libc::SYS_io_uring_register,
        libc::SYS_swapon, libc::SYS_swapoff, libc::SYS_acct, libc::SYS_quotactl,
        libc::SYS_settimeofday, libc::SYS_clock_settime, libc::SYS_clock_adjtime, libc::SYS_adjtimex,
        libc::SYS_open_by_handle_at, libc::SYS_name_to_handle_at, libc::SYS_syslog,
        libc::SYS_fanotify_init, libc::SYS_vhangup,
        // Beyond the app sandbox: a renderer runs nothing.
        libc::SYS_execve, libc::SYS_execveat,
    ];
    #[cfg(target_arch = "x86_64")]
    v.extend([libc::SYS_fork, libc::SYS_vfork, libc::SYS_iopl, libc::SYS_ioperm, libc::SYS_modify_ldt]);
    v
}

const NAMESPACES: u32 = (libc::CLONE_NEWNS | libc::CLONE_NEWCGROUP | libc::CLONE_NEWUTS | libc::CLONE_NEWIPC
    | libc::CLONE_NEWUSER | libc::CLONE_NEWPID | libc::CLONE_NEWNET) as u32;

fn stmt(code: u32, k: u32) -> libc::sock_filter {
    libc::sock_filter { code: code as u16, jt: 0, jf: 0, k }
}

fn jump(code: u32, k: u32, jt: u8, jf: u8) -> libc::sock_filter {
    libc::sock_filter { code: code as u16, jt, jf, k }
}

pub fn apply() -> Result<(), String> {
    const LD: u32 = (libc::BPF_LD | libc::BPF_W | libc::BPF_ABS) as u32;
    const JEQ: u32 = (libc::BPF_JMP | libc::BPF_JEQ | libc::BPF_K) as u32;
    const JSET: u32 = (libc::BPF_JMP | libc::BPF_JSET | libc::BPF_K) as u32;
    const RET: u32 = (libc::BPF_RET | libc::BPF_K) as u32;
    let eperm = libc::SECCOMP_RET_ERRNO | libc::EPERM as u32;
    // struct seccomp_data: nr at 0, arch at 4, args[0] at 16 (low half on
    // little-endian, which both supported architectures are).
    let mut p = vec![stmt(LD, 4), jump(JEQ, AUDIT_ARCH, 1, 0), stmt(RET, libc::SECCOMP_RET_KILL_PROCESS), stmt(LD, 0)];
    for nr in denied() {
        p.push(jump(JEQ, nr as u32, 0, 1));
        p.push(stmt(RET, eperm));
    }
    // clone3 passes its flags in memory the filter cannot read; ENOSYS makes
    // libc fall back to clone, whose flags are checked here.
    p.push(jump(JEQ, libc::SYS_clone3 as u32, 0, 1));
    p.push(stmt(RET, libc::SECCOMP_RET_ERRNO | libc::ENOSYS as u32));
    p.push(jump(JEQ, libc::SYS_clone as u32, 0, 3));
    p.push(stmt(LD, 16));
    p.push(jump(JSET, NAMESPACES, 0, 1));
    p.push(stmt(RET, eperm));
    p.push(stmt(RET, libc::SECCOMP_RET_ALLOW));
    let prog = libc::sock_fprog { len: p.len() as u16, filter: p.as_mut_ptr() };
    // SAFETY: prog points at p, which outlives the call; no_new_privs is set.
    let rc = unsafe {
        libc::syscall(libc::SYS_seccomp, libc::SECCOMP_SET_MODE_FILTER, libc::SECCOMP_FILTER_FLAG_TSYNC, &prog as *const _)
    };
    if rc != 0 {
        return Err(format!("seccomp: {}", std::io::Error::last_os_error()));
    }
    // ptrace is refused now: proof the filter is in force.
    // SAFETY: PTRACE_TRACEME on ourselves; refused with EPERM under the filter.
    if unsafe { libc::ptrace(libc::PTRACE_TRACEME, 0, 0, 0) } == 0 {
        return Err("seccomp does not hold: ptrace succeeded".into());
    }
    Ok(())
}
