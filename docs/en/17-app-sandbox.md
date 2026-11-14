> English translation of [the Chinese original](../17-app-sandbox.md). The Chinese text is authoritative; keep both in sync.

# Limiting app behavior: sandbox, permissions, background

> Status: **prototype ✓**. The launcher `phone-sandbox`, the capability broker and background freezing are all implemented. On the device (QEMU, kernel 6.18) the sandbox self-check gives 13/13 as expected (§7). For remaining work see 57 (internal document) R8.

## 0. Summary

Android and iOS follow the same idea: by default an app can do nothing, and everything it is able to do has to go through the system. They differ in how this is implemented:

- **Android**: one uid per app, plus SELinux mandatory access control, seccomp system-call filtering, runtime permissions and background execution limits.
- **iOS**: sandbox profiles (Seatbelt), entitlements (privileges spelled out in the signature), code signing plus review; apps are suspended after they leave the foreground.

This platform confines apps inside seven layers. The **only channel** through which an app deals with the system is the connection (fd 3) that the Shell hands it at launch; everything else is denied at the kernel level.

| Layer | What it does | Counterpart |
|----|--------|------|
| 1. Channel | One process per app, which only gets fd 3; every capability goes through the Shell's broker, which checks the manifest permissions and runtime grants before forwarding | iOS XPC + entitlement; Android Binder + permissions |
| 2. Separate uid | One uid per app (starting at 10000), which owns only its own data directory; the sockets and key files of system services belong to root with mode 0600 | Android app uid |
| 3. Namespaces | Separate PID space (cannot see other processes), private `/proc`, IPC/UTS isolation; apps without the `net` permission live in an empty network namespace (not even local loopback) | Android process isolation; iOS without a network entitlement |
| 4. Landlock | File allowlist: system code read-only, read/write only in its own data directory; **cannot execute files it wrote itself** (W^X); cannot listen on TCP ports; cannot send signals to processes outside the sandbox or connect to abstract unix sockets outside the sandbox | iOS Seatbelt; the app domain of Android SELinux; Android 10 and later forbid executing from the data directory |
| 5. seccomp | Blocks debugging other processes, mounting, kernel modules, eBPF, perf, kernel keyrings, io_uring, creating new namespaces and so on, returning EPERM | Android app seccomp filter |
| 6. Resource limits | Core dumps disabled; 1024 file descriptors, 256 processes. On the device each app also has its own cgroup v2 group: 192 MB of memory, no swap, 128 processes; CPU weight 400 in the foreground, 50 in the background; Go programs additionally get a `GOMEMLIMIT` soft limit (144 MB). When the memory limit is exceeded, the kernel kills only that one app and the system shows 「超出内存上限」 (Memory limit exceeded). When system memory is tight, the Shell ends background apps first (§5.1) | Android memcg and lmkd; iOS jetsam |
| 7. Background freezing | Frozen about 10 seconds after leaving the foreground and resumed on returning to the foreground; apps that declare the VoIP background mode and hold the permission are exempt. On the device this uses the cgroup freezer, which stops the whole process tree together; on a dev machine without cgroup delegation it falls back to SIGSTOP | iOS background suspension; Android cached apps freezer (also the cgroup v2 freezer) |

Beyond these seven layers there are three more:

- **UI layer** (already in place): apps can only submit semantic nodes and cannot draw system dialogs; they cannot read raw key presses (the input method belongs to the system, [16](16-input-method.md)), cannot read the screen and cannot read other apps; secure screens are masked in screenshots and screen recordings.
- **Distribution layer**: app packages are signed, and the permissions in the manifest play the role of entitlements; at install time the signature is verified and the system shows a confirmation dialog; before the first launch after boot the files are checked again, and modified ones are not allowed to run; packages signed by the development store can only be installed and run in developer mode; uninstalling also deletes the app's keys ([22](22-developer-api.md) §12).
- **Protocol layer**: each message an app sends is at most 4 MB, a page has at most 1000 nodes, and the page stack is at most 32 deep; at most 5 notifications per minute get a banner and an alert sound; when a foreground app is stuck, the system shows 「没有响应」 (Not responding) after 5 seconds (§5.2).

## 1. Item-by-item comparison with Android / iOS

| Aspect | Android | iOS | This platform |
|------|---------|-----|--------|
| Identity | Per-app uid | Per-app container | Per-app uid ✓ |
| Files | uid permissions + SELinux | Sandbox container | uid permissions + Landlock ✓ |
| Executing downloaded code | Android 10 and later forbid executing from the data directory | Forbidden (only the system JIT) | Landlock forbids executing files in writable locations ✓ |
| Seeing other processes | Not possible | Not possible | Separate PID namespace ✓ |
| Network | INTERNET permission (uid-level firewall); mobile data restricted per app, traffic accounted per app | Allowed by default; cellular data can be turned off per app | Without the `net` permission, the app lives in an empty network namespace ✓; for apps with the permission, the user can turn off the app's network in Settings (after the app restarts it lives in an empty network namespace) or set it to Wi-Fi only (takes effect immediately) ✓; 「设置 › 流量」 (Settings › Data usage) counts bytes sent and received per app and per Wi-Fi / mobile network ✓ (see below) |
| Kernel attack surface | seccomp | Sandbox profile | seccomp ✓ |
| Background running | Doze, background restrictions, freezer | Suspended, with a few background-mode exceptions | Frozen after 10 seconds, with VoIP and currently playing audio exempt ✓; push wakes apps within a budget ✓ ([21](21-push.md)); a background app ended when memory is tight leaves a tombstone, and going back restores its earlier pages ✓ (§5.3) |
| Capabilities | Permissions + Binder services | entitlement + XPC | Manifest permissions + runtime grants + broker ✓ |
| System UI | Can be overlaid with the overlay permission | Cannot be overlaid | Cannot be overlaid; system UI can only be drawn by the Shell ✓ |
| Lock screen | Gatekeeper checks the lock-screen credential; while locked, apps' screens and notification contents are hidden; fingerprint templates live in the TEE | The Secure Enclave checks the passcode; the rest as on Android | phone-keystored checks the lock-screen PIN; while locked the app in front is hidden behind the lock screen, notifications show only the app's name and a count, and keys reach only the lock screen (in a call, only the call screen); apps never get the PIN or a fingerprint, only a signature a fingerprint confirmed, through `auth.confirm` ✓ ([31](31-lock-and-biometrics.md); TEE **not implemented**) |
| Input | Third-party input methods see every key press | Third-party keyboards are restricted | The input method belongs to the system; apps only receive committed text ✓ |
| Screenshots, screen recording | FLAG_SECURE | Controlled by the system | Secure screens are masked ✓; there is no screenshot API for apps |
| Clipboard | Not readable in the background since Android 10 | Prompt on paste | Apps can write but not read; paste is a system menu item, and the system fills the text into the input field; cleared after 10 minutes ✓ |
| Location | Runtime permission; background location needs a separate grant | Runtime permission, status bar indicator | Runtime permission `location`, granted only to the foreground app, status bar indicator ✓ (simulated location) |
| SMS, dialing | SEND_SMS and CALL_PHONE are runtime permissions, and sending to premium short codes needs a separate confirmation; Play only lets the default SMS app request SMS permissions | Apps cannot send SMS (they can only open a prepared message for the user to send); dialing asks for confirmation first | For every SMS an app installed from the store sends and every call it places, the system shows a dialog stating whom it goes to (and the content) or whom it calls, and goes ahead only once the user confirms, even when the app declares `sms` / `phone`; the Messages and Phone apps in the system image do not ask ✓ |
| Apps not used for a long time | Since Android 11, runtime permissions are revoked automatically after a few months without use | — | When an app installed from the store has not been opened for 90 days, its runtime grants (camera, microphone, contacts, photo library, location, including those of its mini-programs) are revoked and asked for again at the next use; Settings posts a notification, and 「应用权限」 (App permissions) notes the day they were revoked ✓ |
| Scheduled background tasks | WorkManager / JobScheduler, minimum 15 minutes, with time windows and charging constraints | Background App Refresh, the system decides when | `job.schedule`, minimum 15 minutes; merged into a single wake-up within a time window, can piggyback on a push, can require charging only; shares the wake-up budget with push ✓ |
| Passing files between processes | Binder passes fds | XPC passes fds | Only from the system to apps; fds sent by apps are closed immediately ✓ |
| System UI process | SystemUI is a system process | SpringBoard is a system process | Both renderers run with reduced privileges: empty network namespace, nobody, no_new_privs, Landlock, seccomp (running programs forbidden); photo thumbnails are embedded in the frame by the Shell, so renderers cannot read the photo library; restarted by the Shell after a crash ✓ (rounds 10 and 11) |
| File sharing with a computer | MTP runs in the MediaProvider process, not in system_server | The Finder's file sharing (AFC) can reach only the sandboxes of apps that declare file sharing | The server that parses the computer's requests runs in a process of its own, `phone-usbshared`, started through `phone-sandbox` with the helper policy (§2): its own uid, an empty network namespace, Landlock allowing it only to run its own program, seccomp forbidding exec, new sockets and file operations by path; the listening socket and the files are opened by the Shell and handed over as fds, and the Shell checks each of its requests; restarted by the Shell after a crash ([27](27-usb-typec.md) §3.2) ✓ (development machine; **to be verified** on the emulator) |
| Mini-programs | WebView content uses the host's permissions | Same as Android | Runtime grants for mini-programs are tracked separately (host + mini-program); the host's grants do not pass to the mini-program and can be revoked separately ✓ |
| Web pages | WebView runs in the app process, and pages use the host's permissions | WKWebView web content runs in a separate process | Each app's web pages run in one separate process with a separate uid (`<app>/web`) and their own cgroup (memory limit set per device model); their data is in their own directory, and the host and the pages cannot read each other; by default the GPU device cannot be opened, and where the device model allows it only the render node is let through; networking follows the host's network permission and the traffic is counted under the host; the page's `phone.call` only exposes allowlisted capabilities, and grants are tracked separately per web origin ✓ ([14](14-tui-route-and-app-framework.md) §5) |
| Sharing | Intent chooser | Share sheet | System panel, targets declared in manifests; the initiating app cannot see what the user has installed; a shared photo is granted along with the share ✓ |
| Distribution | Signing + Play review, staged rollouts | Signing + App Store review, phased releases | `.phoneapp` signed package: signature verification, certificate chain, file manifest, system confirmation dialog; updates must be signed by the same developer; the store backend's automatic checks, manual review, takedowns, and the revocation list the device fetches periodically; updates can be released in stages and are halted automatically when there are many crashes ✓ ([07](07-ecosystem-and-store.md) §4.1) |
| System permissions | `signature`-level permissions only for platform-signed apps | Private entitlements | `system` and `install` only for apps in the system image: the installer rejects them, and an installed app that declares them still does not get them ✓ |
| NFC card emulation | An HCE service needs `BIND_NFC_SERVICE`; the system hands it the reader's commands by AID; payment ones only for the default payment app; `requireDeviceUnlock` | Cards in the secure element, Wallet manages express transit; HCE opened in some regions from iOS 18 | The declared `nfc` permission; a reader's commands go only to the app of the card that declares the AID it selected, payment cards only for the default payment app and only unlocked; the UIDs of the door cards the system copied stay in phone-nfcd, out of apps' reach, and apps do not see the other cards in the card pack; the system app Cards manages the cards (`nfc.state` / `nfc.set` need the `system` permission) ✓ ([30](30-nfc.md), simulated controller) |

**Per-app data usage and Wi-Fi only** (package `netfw`, needs root on the device): each app has its own uid, and the Shell builds a table `inet phone` in nftables that matches outbound traffic by the socket's uid and inbound traffic by the connection mark (the outbound rule sets the connection mark to the uid, so reply packets can be attributed). There is one named counter per app, per network type and per direction; packets a Wi-Fi-only app sends from a non-Wi-Fi interface are dropped and the app gets EPERM, so it fails immediately instead of waiting for a timeout. The rules are replaced as a whole in one transaction, and the counters live outside the rules, so changing the rules or restarting the Shell does not reset them. When the 「流量」 (Data usage) page is opened, when an app is uninstalled and at normal shutdown, the Shell atomically reads and resets the counters (`Harvest`) and adds them into `traffic.json`; a Shell crash loses nothing (the counts are still in the kernel), and only a power loss drops the portion since the last read. A few conventions: loopback (traffic between local services) is not counted; interfaces whose names start with `wl` count as Wi-Fi and all others as mobile networks, and an unrecognized interface is treated as a metered network; it uses a pure-Go netlink library (`google/nftables`), so the image does not need the `nft` command-line tool (which would pull in GPLv3 readline). The kernel needs conntrack and `nft_ct`: the image installs the `nft_ct` module package, and the kernel loads it automatically (together with conntrack) the first time a rule uses the connection mark. On a dev machine the Shell is not root, so this feature is not available, and Settings says so accurately.

## 2. Launch flow

```
Shell (root)
  │ clone(NEWNS | NEWPID | NEWIPC | NEWUTS [| NEWNET, when there is no net permission])
  ▼
phone-sandbox (in the new namespaces, still root at this point)
  │ 1. mount a private /proc (sees only itself)
  │ 2. rlimit: core 0, fd 1024, processes 256
  │ 3. setgroups([]) → setresgid / setresuid(app uid): drop all privileges
  │ 4. create TMPDIR (tmp/ under the data directory; there is no shared /tmp)
  │ 5. no_new_privs: no later exec can gain privileges again
  │ 6. Landlock ruleset → restrict_self
  │ 7. seccomp filter (all threads)
  │ 8. execve(app); the environment variable PHONE_SANDBOX lists the layers actually in effect
  ▼
App process: only the fd 3 path is left
```

- Once applied, the restrictions cannot be revoked, and the app inherits them across exec. This is the same approach as Android's minijail and Chrome's sandbox.
- On a dev machine the Shell is not root and can only apply the layers that need no privileges: rlimit, seccomp, and Landlock when the kernel supports it (this dev machine runs kernel 4.18, which has no Landlock). The 「沙箱自检」 (Sandbox self-check) in Settings marks the checks that do not apply as 「跳过」 (Skipped).
- If the Shell runs as root but cannot find `phone-sandbox`, it **refuses to launch the app** instead of falling back to an unrestricted state.
- **System helpers** (for now only file sharing with a computer, `phone-usbshared`, [27](27-usb-typec.md) §3.2) go through the same launcher with `helper` in the policy: namespaces and uid as above, but no data directory and no device let through, and Landlock lets them read and run their own program only. Once the helper holds its inherited fds it adds a seccomp layer of its own (`sandbox.DescriptorsOnly`, layer name `fd-only`): exec, new sockets (socket, socketpair, bind, connect, listen) and file operations by path return EPERM. That layer needs neither root nor Landlock, so the development machine has it too: there the layers in force are "rlimit · seccomp · fd-only". A Shell running as root that cannot find the launcher refuses to start the helper as well.

## 3. Landlock rules

| Path | Access |
|------|------|
| `/usr` `/lib` `/lib64` `/bin` `/sbin`, the app's install directory | Read, execute |
| `/etc` `/proc` | Read |
| Vendor root public key (a single file) | Read |
| Its own data directory (including `tmp/`) | Read/write, create and delete files; **no execute** |
| `/dev/null` `/dev/zero` `/dev/urandom` `/dev/random` | Read/write |
| Everything else | Denied |
| TCP | Cannot listen; without the `net` permission cannot connect either |
| Scope | Cannot send signals to processes outside the sandbox, cannot connect to abstract unix sockets outside the sandbox |

The newer the kernel's Landlock ABI version, the more it can control. The launcher enables the rules that match the version and degrades automatically on older kernels.

## 4. seccomp blocking

System calls that return EPERM:
- Debugging and reading/writing other processes: `ptrace`, `process_vm_readv/writev`, `pidfd_getfd`.
- Changing the file system view: `mount`, `umount2`, `pivot_root`, `chroot`, and the new mount API (`fsopen` and so on).
- Creating new namespaces: `unshare`, `setns`, `clone` with namespace flags. `clone3` returns ENOSYS so that libc falls back to `clone`, whose flags can be checked.
- Kernel modules and kernel replacement: `init_module`, `finit_module`, `delete_module`, `kexec_*`, `reboot`.
- Interfaces with a large kernel attack surface: `bpf`, `perf_event_open`, `userfaultfd`, keyring calls such as `keyctl`, `io_uring_*`.
- System state: `swapon/off`, `acct`, `quotactl`, changing the system time (`settimeofday` and so on), `syslog`, `open_by_handle_at`.

On an architecture mismatch (for example a 32-bit call executed on AArch64) the process is killed outright.

## 5. Background freezing

- When an app leaves the foreground, it first receives a `lifecycle: background` message and has 10 seconds to save its state (this is when the SDK calls `OnSave` and hands the system what it needs to rebuild its pages, §5.3); after that it is paused with SIGSTOP. Before it returns to the foreground it is resumed with SIGCONT.
- Exceptions:
  - Apps that declare `"background": ["voip"]` in the manifest and hold the `voip` permission (a chat app has to receive incoming calls);
  - Apps that are in a call;
  - Apps that declare `"background": ["audio"]` and are currently playing (music). Once playback is paused or finished, they are frozen as usual.
- While an app is frozen, the messages the Shell sends it queue up in the connection buffer and are handled after it resumes; the Shell itself never blocks because of this.
- Push ([21](21-push.md)):
  - Messages that carry only a title and body are displayed by the system and do not wake the app;
  - Messages that carry data resume a frozen app for 3 seconds, or launch it in the background if its process is not running, at most 20 times per app per hour;
  - VoIP pushes are not limited.
- Scheduled background tasks ([22](22-developer-api.md) §4.7): an app registers "refresh every N minutes (at least 15)"; when the time comes, the system resumes it or launches it in the background under the same rules as push, sends a `job` event, and counts it against the same budget of 20 per hour. The weather app uses this to refresh every hour.
- With push available, Hongyan does not declare the `voip` background mode; it is frozen like any other app after leaving the foreground, and both messages and incoming calls rely on push ([21](21-push.md) §4). The `voip` background mode is still kept for apps that really need it, but using it is not recommended.
- cgroup, two approaches:
  - **App scopes** (used by the display service, `PHONE_APP_SCOPES=1`): each app gets a systemd transient scope `phone-apps.slice/app-<id>-<pid>.scope`, and the limits are handed to systemd as properties of the scope (`MemoryMax`, `TasksMax`, `CPUWeight`). After launching the app, the Shell calls `StartTransientUnit` over D-Bus to move it there; until then the sandbox launcher waits on a pipe, so the app's code never runs in the Shell's group. The Shell's own service group contains no apps, so after a crash systemd can restart the Shell, and the apps stay in their own scopes waiting for it to take them over ([11](11-platform-architecture.md) §14).
  - **Delegated subtree** (used by the boot selftest service): systemd hands the service's group to the Shell with `Delegate=yes` (the Shell confirms this by checking the `trusted.delegate` mark set by systemd; root has write access to every group, so checking write access proves nothing). The Shell first moves itself and the other processes of the service into a child group `shell`, then creates an `app.<id>` group for each app, and app processes are started directly in their own group (`CLONE_INTO_CGROUP`).
  - Freezing writes `cgroup.freeze`, which stops child processes too, and a SIGCONT sent by some other process cannot undo it.
  - After an app exits, the Shell reads `memory.events` to tell whether the exit was caused by exceeding the memory limit, then kills the processes left in the group, waits until they are really gone (`cgroup.kill` does not wait) and deletes the group; on the next start, a group left from before is killed and deleted first and a new one made. The page cache a dead process tree leaves stays charged to its old group, and a kept group counts it against the new processes: in the low-memory stress test, the browser's web process after a revival from its tombstone was over WebKit's limit the moment it started, and the next one hung on its page. Web process groups are handled the same way.
  - The code is in `shell/cgroup.go` and `scopes.go`.

### 5.1 When memory is tight: low-memory cleanup

- System memory is checked once per second. It counts as tight when the available memory in `/proc/meminfo` is below 10% of the total, or when the memory pressure (PSI: the app group's `memory.pressure`, or `/proc/pressure/memory` when there is no cgroup) has `some avg10` above 20%.
- **End background apps' web pages first**: one app's web process can use several hundred MB (up to the per-model limit, 512 MB on a 2 GB model), so under memory pressure the Shell first ends the web process of one background app while the app itself stays alive; system apps (the browser) count too. When the user returns to that page, it reloads in a new process, the same way a browser discards background tabs. The choice follows the same order as for apps below, and within one category the web process using the most memory (the web cgroup's `memory.current`) is ended first. The web pages of the foreground app and of apps doing work in the background are left alone.
- When there are no background web pages left to end, background apps are ended one at a time, with a 2-second wait before re-evaluating. By category: first apps that have never been in the foreground (launched in the background by push or background tasks), then apps used a while ago, and last the two most recently used; within one category the app using the most memory goes first (the cgroup's `memory.current`, or resident memory when there is no cgroup), which frees the most at once.
- The foreground app, apps in a call, apps playing in the background and system apps are never chosen. An app ended this way does not count as a crash; it leaves a tombstone, and the next time it is opened it returns to its earlier pages (§5.3).
- The code is in `shell/lowmem.go`; the selftest uses `mem-low` to simulate memory pressure, and the web view case checks that the web pages of the browser and of Hongyan are ended first, that the apps are still there, and that the page reloads after going back to it (`pages-ended`).

### 5.2 Hangs and crashes

- **Not responding**: after the user does something in the foreground app, the system sends `ping`, and the SDK answers `pong` in the same loop that handles actions. If no answer arrives within 5 seconds, the system shows the dialog 「没有响应」 (Not responding): 「等待」 (Wait) gives the app another 5 seconds, 「关闭应用」 (Close app) ends it; if the app recovers, the dialog disappears by itself. An app waiting for the system to answer one of its capability requests (for example while a grant dialog is still open) does not count as not responding; background and frozen apps are not checked (`shell/anr.go`).
- **Crash log**: when an app exits on its own with a non-zero exit code, the system records the time, version, reason and the last 20 lines of its log, keeping at most 50 entries (`shell/crash.go`). Apps ended by the system (low-memory cleanup, exceeding the memory limit, the user choosing close in 「没有响应」, uninstall) do not count as crashes.
- **Crash loop**: an app that crashes 3 times within 10 minutes is temporarily not launched in the background by the system; push and background task data are held until the user opens it.

### 5.3 Tombstones: a background app that was ended comes back as it was

- When the low-memory cleanup ends a background app (§5.1), the system keeps its tombstone: the last state the app handed to the system (when the app leaves the foreground the SDK calls `OnSave`, and the state comes up in a `state` message, at most 64 KB of JSON, [22](22-developer-api.md) §3) and its page stack at that moment. The app stays in 「切换应用」 (Switch app).
- When the user returns to it (from 「切换应用」, the main menu or a notification alike; also when a push or a scheduled task launches it in the background first), the system immediately shows its earlier pages and at the same time starts a new process, whose `start` message carries that state (the SDK's `Restore`), and the app rebuilds its pages from it. When a rebuilt page has the same screen ID as before, the system puts back the half-typed text in its input fields and the focus exactly as they were, so the app does not have to save them itself. Until the new pages arrive, the old pages are only displayed (their actions belong to the process that has already ended): keys pressed meanwhile are kept (up to 32) and handed to the app once its new pages are all there, and Back returns to idle as usual; the same goes for keys pressed during a cold start before the first page appears. Keys always go only to the foreground (focused) app: the kept keys are its own, only its new process is not ready yet; once it is no longer in front (the user went back to idle or switched to another app) they are dropped, never handed to anyone else. Android's InputDispatcher does the same: while the focused window is not ready for input, key events wait in its queue for it (and an app that keeps them waiting too long is reported as not responding) instead of being dropped or given to another window.
- A tombstone is kept for one day only; none is kept when the user ends the app in 「切换应用」, when the app is updated or uninstalled, or when the app exits or crashes on its own.
- This is the same approach as iOS (snapshot plus state restoration); on Android the system keeps the Activity stack, and each page is rebuilt from its saved Bundle.
- Selftest: Hongyan opens the conversation with 张三 (Zhang San), writes 「明天见」 ("See you tomorrow") in the input field without sending it, and goes back to idle; `lowmem-kill` ends it the way the low-memory cleanup does; going back from 「切换应用」, it is still in this conversation, the draft is still there, and pressing OK sends it. The code is in `shell/tomb.go`.

## 6. Self-check (设置 → 沙箱自检, Settings → Sandbox self-check)

The Settings app plays an app that wants to do harm and tries each item in turn:

| Check | Expected | Layer relied on |
|------|------|----------|
| Read the device private key | Denied | Separate uid |
| Read another app's data | Denied | Separate uid |
| Connect directly to the key service | Denied | Separate uid |
| Execute code it wrote itself | Denied | Landlock |
| Write the shared temporary directories /tmp, /var/tmp | Denied | Landlock |
| See other processes | Denied | Namespaces |
| Access the network | Denied | Empty network namespace |
| Listen on a TCP port | Denied | Empty network namespace / Landlock |
| Dangerous system calls (such as keyctl) | Denied | seccomp |
| Write its own data directory | Allowed | — |
| Read the vendor root public key | Allowed | — |
| Open the display device /dev/dri | Denied | Separate uid |
| Read the keyboard device /dev/input | Denied | Separate uid |

This checklist (`sandbox/selfcheck_linux.go`) is also used by the compatibility test suite (CTS): if a vendor weakens the sandbox, the CTS fails. The top of the page shows the layers actually in effect. Checks that depend on a layer not in effect are marked 「跳过」 (Skipped) and are not counted in the result.

## 7. Verification

| Environment | Result |
|------|------|
| Unit tests (`sandbox`) | Apply the sandbox in a child process: `keyctl` and `unshare` return EPERM, while ordinary system calls, writing the data directory and executing system programs work normally; when the kernel has Landlock, they also check that writing outside the data directory and executing files inside the data directory are denied |
| Dev machine selftest (non-root, kernel 4.18) | Layers in effect: 「seccomp · 资源上限」 (seccomp · Resource limits); self-check 3/3 as expected (8 items skipped); background freezing case: 1 second after leaving the wallet the process state is T (stopped), after returning it resumes and responds to key presses, and the chat app is not frozen |
| Device (QEMU, kernel 6.18, root) | **Sandbox self-check 13/13 as expected**: separate uid, namespaces, Landlock, seccomp, resource limits and cgroup are all in effect; the background freezing case passes; the CTS app runs the same checks inside the sandbox. Each app uses `tmp/` under its data directory, and there is no shared `/tmp` (a private `/tmp` would hide the service state kept under `/tmp`) |

## 8. Not done / mass production

| Item | Notes |
|----|------|
| cgroup refinement | Adjust the limits to the device's memory; I/O weight |
| Scale of the traffic rules | Currently a few linear rules per app, which is fine for a few dozen apps; with hundreds of apps, switch to a per-uid table lookup (nftables verdict map) |
| Per-app quotas | Monthly data cap, over-quota alerts; the counters exist, the policy and UI are missing |
| JIT policy | All JS engines are currently interpreters; when a JIT is introduced later, writable and executable memory will be given only to the system JS engine |
| Usage descriptions | The manifest's `purposes` says why the app needs each runtime permission (camera, microphone, contacts, photos, location), iOS's usage description in 1–80 characters: the system's grant dialog shows it after the permission and the install confirmation lists it; an app without one does not install and the store does not take it, and the staff desk lists the permissions and purposes in the review queue for the reviewer to check (`shell/install.go`, `appstore/store.go`). When a mini-program uses its host's permission the host's purpose is not shown (it is not the mini-program's reason) but the mini-program's own: its `app.json` gives `purposes` by the system's permission names (in English, translated by its own catalog), the host registers them with `SetPurposes` once it has verified and loaded the program, requests made on its behalf carry them, and the dialog shows only the asked permissions' entries and none that are too long; the host vouches for them as it does for the origin (`apps/miniapp`, `sdk.App.SetPurposes`) |
| Background battery use | How much CPU apps that are not frozen (audio, VoIP, calls) use in the background, counted per app with a warning when it is high (Android's background battery usage warning), listed in Settings; **not implemented** |
