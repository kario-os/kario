#!/usr/bin/env python3
"""Semantic (phone.Ui) vs web comparison: cold start to the chat list, steady
memory (PSS/RSS of the whole process tree) and idle CPU, measured the same way
for every engine.

  bench.py web  --name wpe      --cmd 'cog -P headless {url}'   --root fw/web
  bench.py web  --name chromium --cmd 'chrome --headless=new ... {url}' --root fw/web
  bench.py tui  --name phone-shell --cmd 'build/usr/bin/phone-shell --sim-dir {tmp}'
"""
import argparse
import http.server
import json
import os
import pty
import re
import select
import shlex
import signal
import struct
import subprocess
import tempfile
import threading
import time
import fcntl
import termios

TICK = os.sysconf("SC_CLK_TCK")

# What the phone shows in its default language (zh-Hans), which the tui run
# waits for: the home screen's title and the group chat its flow opens.
HOME = "\u4e3b\u5c4f\u5e55"
GROUP = "\u4ea7\u54c1\u7fa4"


def children_map():
    kids = {}
    for d in os.listdir("/proc"):
        if not d.isdigit():
            continue
        try:
            with open(f"/proc/{d}/stat") as f:
                stat = f.read()
        except OSError:
            continue
        ppid = int(stat.rsplit(")", 1)[1].split()[1])
        kids.setdefault(ppid, []).append(int(d))
    return kids


def tree(root):
    kids, out, todo = children_map(), [], [root]
    while todo:
        p = todo.pop()
        out.append(p)
        todo.extend(kids.get(p, []))
    return out


def comm(pid):
    try:
        with open(f"/proc/{pid}/comm") as f:
            return f.read().strip()
    except OSError:
        return "?"


def mem_kb(pid):
    pss = rss = 0
    try:
        with open(f"/proc/{pid}/smaps_rollup") as f:
            for line in f:
                if line.startswith("Pss:"):
                    pss = int(line.split()[1])
                elif line.startswith("Rss:"):
                    rss = int(line.split()[1])
    except OSError:
        pass
    return pss, rss


def ticks(pid):
    try:
        with open(f"/proc/{pid}/stat") as f:
            fields = f.read().rsplit(")", 1)[1].split()
        return int(fields[11]) + int(fields[12])
    except OSError:
        return 0


def measure(root, settle, idle):
    time.sleep(settle)
    pids = tree(root)
    procs = []
    for p in pids:
        pss, rss = mem_kb(p)
        if rss:
            procs.append({"pid": p, "comm": comm(p), "pss_kb": pss, "rss_kb": rss})
    t0 = {p: ticks(p) for p in pids}
    time.sleep(idle)
    used = sum(max(0, ticks(p) - t0[p]) for p in pids)
    return {
        "processes": len(procs),
        "pss_mb": round(sum(x["pss_kb"] for x in procs) / 1024, 1),
        "rss_mb": round(sum(x["rss_kb"] for x in procs) / 1024, 1),
        "idle_cpu_pct": round(100.0 * used / TICK / idle, 2),
        "breakdown": sorted(procs, key=lambda x: -x["pss_kb"]),
    }


def kill_tree(root):
    for p in reversed(tree(root)):
        try:
            os.kill(p, signal.SIGKILL)
        except OSError:
            pass


def bench_web(args):
    ready = threading.Event()

    class H(http.server.SimpleHTTPRequestHandler):
        def __init__(self, *a, **k):
            super().__init__(*a, directory=args.root, **k)

        def do_GET(self):
            if self.path.startswith("/ready"):
                ready.set()
                self.send_response(204)
                self.end_headers()
                return
            super().do_GET()

        def log_message(self, *a):
            pass

    srv = http.server.ThreadingHTTPServer(("127.0.0.1", 0), H)
    threading.Thread(target=srv.serve_forever, daemon=True).start()
    url = f"http://127.0.0.1:{srv.server_address[1]}/chat/index.html"
    tmp = tempfile.mkdtemp(prefix="bench-")
    cmd = args.cmd.format(url=url, tmp=tmp)
    t0 = time.monotonic()
    proc = subprocess.Popen(shlex.split(cmd), stdout=subprocess.DEVNULL, stderr=open(os.path.join(tmp, "stderr.log"), "w"),
                            start_new_session=True)
    ok = ready.wait(args.timeout)
    startup = time.monotonic() - t0
    result = {"name": args.name, "kind": "web", "ok": ok, "startup_s": round(startup, 3)}
    if ok:
        result.update(measure(proc.pid, args.settle, args.idle))
    else:
        with open(os.path.join(tmp, "stderr.log")) as f:
            result["stderr"] = f.read()[-2000:]
    kill_tree(proc.pid)
    srv.shutdown()
    return result


def bench_tui(args):
    tmp = tempfile.mkdtemp(prefix="bench-")
    cmd = shlex.split(args.cmd.format(tmp=tmp))
    t0 = time.monotonic()
    pid, fd = pty.fork()
    if pid == 0:
        os.environ["TERM"] = "xterm-256color"
        os.execvp(cmd[0], cmd)
    fcntl.ioctl(fd, termios.TIOCSWINSZ, struct.pack("HHHH", 40, 100, 0, 0))
    buf = b""

    def pump(until, pattern):
        nonlocal buf
        while time.monotonic() < until:
            r, _, _ = select.select([fd], [], [], 0.02)
            if r:
                try:
                    chunk = os.read(fd, 65536)
                except OSError:
                    return False
                buf += chunk
                # Answer the terminal probes a real terminal would answer.
                if b"\x1b]11;?" in chunk:
                    os.write(fd, b"\x1b]11;rgb:0000/0000/0000\x1b\\")
                if b"\x1b[6n" in chunk:
                    os.write(fd, b"\x1b[1;1R")
                text = re.sub(r"\x1b\[[0-9;?]*[A-Za-z]", "", buf.decode("utf-8", "replace"))
                if pattern in text:
                    return True
        return False

    ok = pump(time.monotonic() + args.timeout, HOME)
    t_home = time.monotonic() - t0
    # Flow tokens: a key name or literal text, optionally "=text to wait for".
    keys = {"enter": b"\r", "down": b"\x1b[B", "up": b"\x1b[A", "esc": b"\x1b", "tab": b"\t"}
    for tok in args.flow.split(","):
        send, _, want = tok.partition("=")
        os.write(fd, keys.get(send, send.encode()))
        if want:
            ok = ok and pump(time.monotonic() + args.timeout, want)
        else:
            pump(time.monotonic() + 0.1, "\x00")
    startup = time.monotonic() - t0
    result = {"name": args.name, "kind": "tui", "ok": ok, "startup_s": round(startup, 3), "home_s": round(t_home, 3)}
    if ok:
        stop = threading.Event()

        def drain():
            while not stop.is_set():
                r, _, _ = select.select([fd], [], [], 0.1)
                if r:
                    try:
                        os.read(fd, 65536)
                    except OSError:
                        return

        th = threading.Thread(target=drain, daemon=True)
        th.start()
        result.update(measure(pid, args.settle, args.idle))
        stop.set()
    kill_tree(pid)
    return result


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("mode", choices=["web", "tui"])
    ap.add_argument("--name", required=True)
    ap.add_argument("--cmd", required=True)
    ap.add_argument("--root", default=os.path.join(os.path.dirname(__file__), "..", "web"))
    ap.add_argument("--runs", type=int, default=3)
    ap.add_argument("--settle", type=float, default=3)
    ap.add_argument("--idle", type=float, default=5)
    ap.add_argument("--timeout", type=float, default=60)
    ap.add_argument("--flow", default="enter=" + GROUP, help="tui: keys after the home screen")
    args = ap.parse_args()
    runs = [(bench_web if args.mode == "web" else bench_tui)(args) for _ in range(args.runs)]
    good = [r for r in runs if r["ok"]]
    summary = {"name": args.name, "runs": len(runs), "ok_runs": len(good)}
    if good:
        for k in ("startup_s", "pss_mb", "rss_mb", "idle_cpu_pct", "processes"):
            vals = sorted(r[k] for r in good)
            summary[k] = vals[len(vals) // 2]
        summary["breakdown"] = good[-1]["breakdown"]
    else:
        summary["error"] = runs[-1].get("stderr", "not ready")
    print(json.dumps(summary, ensure_ascii=False))


if __name__ == "__main__":
    main()
