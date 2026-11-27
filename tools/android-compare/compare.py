#!/usr/bin/env python3
"""Kario against a lean Android keypad phone (docs/40-testing.md §9): the
numbers of both halves, each from its own tools, in one table.

    compare.py frames VIDEO --screen X,Y,W,H (--marks F,F,... | --key X,Y,W,H)
                      [--fps N] [--settle-ms 300] [--task T --into CSV]
    compare.py kario DIR... [--run cpu-target] [--out CSV] [--hand CSV]
    compare.py android DIR [--out CSV] [--hand CSV]
    compare.py set CSV ID VALUE [--unit U] [--source S]
    compare.py table TEMPLATE CSV... [--csv OUT]
    compare.py vcf [--count 200] [--zhangsan NUMBER] [--lisi NUMBER]

frames times a task from a high-speed video of the phone (240 fps, filmed
from a second phone): from the frame its first key goes down (marked by
hand, or suggested from the key area) to the frame the screen settles. It
needs ffmpeg. kario reads what kario-half.sh left in its directories (the
kit's results of a run and its own notes), android what android-half.sh
wrote. Each writes a side's rows, id,value,unit,source, afresh, the rows
measured by hand after them (--hand: what frames --into and set add); an id
given more than once (a task's runs) is shown as its median. table puts the
sides next to each other in the template's rows (results-template.csv), a column
a side named by its file (results-<side>.csv), as Markdown. vcf writes the
contacts both phones import, so their lists and searches are the same.
"""
import argparse
import collections
import csv
import glob
import json
import math
import os
import re
import shutil
import statistics
import subprocess
import sys

ROLES = ("phone", "sms", "messenger", "camera", "contacts", "payment")
TASKS = ("dial", "sms", "chat", "pay", "camera", "scroll", "switch")
KARIO_APPS = {"com.phone.dialer": "phone", "com.phone.messages": "sms", "com.phone.chat": "messenger",
              "com.phone.camera": "camera", "com.phone.contacts": "contacts", "com.phone.wallet": "payment"}
# The switch task's three apps (its roles on Android).
SWITCH_APPS = ("com.phone.messages", "com.phone.chat", "com.phone.contacts")
SWITCH_ROLES = ("sms", "messenger", "contacts")
FIELDS = ("id", "value", "unit", "source")
FRAME_BUDGET_MS = 1000 / 60


def row(rid, value, unit="", source=""):
    return {"id": rid, "value": value, "unit": unit, "source": source}


def num(v):
    try:
        f = float(str(v).replace(",", ""))
    except (TypeError, ValueError):
        return None
    return f if math.isfinite(f) else None


def fmt(v):
    if isinstance(v, float):
        if v == int(v):
            return str(int(v))
        return f"{v:.1f}" if abs(v) >= 10 else f"{v:.2f}"
    return str(v)


def nearest_rank(values, q):
    """The q-quantile by nearest rank, as the renderer works its out."""
    s = sorted(values)
    return s[max(0, math.ceil(q * len(s)) - 1)]


def read(path):
    with open(path, encoding="utf-8", errors="replace") as f:
        return f.read()


def find(dirs, pattern):
    """Files matching pattern in any of dirs, in the order of their paths:
    the kit names a run by its time, so the newest run's come last."""
    out = set()
    for d in dirs:
        out.update(glob.glob(os.path.join(d, pattern)))
    return sorted(out)


def append(path, rows):
    new = not os.path.exists(path) or os.path.getsize(path) == 0
    with open(path, "a", newline="", encoding="utf-8") as f:
        w = csv.DictWriter(f, fieldnames=FIELDS)
        if new:
            w.writeheader()
        for r in rows:
            w.writerow({k: r.get(k, "") for k in FIELDS})


def load(path):
    with open(path, newline="", encoding="utf-8") as f:
        return [r for r in csv.DictReader(f) if r.get("id")]


def emit(rows, out=None, hand=None):
    """A side's rows to out, written afresh, with the rows measured by hand
    (hand: what frames --into and set added) after them; else to stdout."""
    if hand and os.path.exists(hand):
        rows = rows + load(hand)
    if out:
        if os.path.exists(out):
            os.remove(out)
        append(out, rows)
        print(f"compare: {len(rows)} rows in {out}")
    else:
        w = csv.DictWriter(sys.stdout, fieldnames=FIELDS)
        w.writeheader()
        for r in rows:
            w.writerow({k: r.get(k, "") for k in FIELDS})


# ---- video ---------------------------------------------------------------

def noise_floor(diffs, k=6.0, floor=0.5):
    """The level above which a frame differs from the one before: the
    median change (sensor noise while the screen is still) and k times the
    median absolute deviation, at least floor (of 255)."""
    if not diffs:
        return floor
    med = statistics.median(diffs)
    mad = statistics.median(abs(d - med) for d in diffs)
    return max(floor, med + k * mad)


def settle(diffs, start, end, still, thr):
    """The last changed frame in [start, end) that still frames without a
    change follow (or the end); None when nothing changed."""
    last = None
    for i in range(start, end):
        if diffs[i] > thr:
            last = i
        elif last is not None and i - last >= still:
            return last
    return last


def detect(diffs, marks, fps, settle_ms=300.0, thr=None):
    """For each key-down frame in marks: the first frame after it that
    changed (the response) and the frame the screen settled on before the
    next mark. diffs[i] is the change from frame i-1 to frame i."""
    thr = noise_floor(diffs) if thr is None else thr
    still = max(1, round(settle_ms * fps / 1000))
    out = []
    for n, m in enumerate(marks):
        end = marks[n + 1] if n + 1 < len(marks) else len(diffs)
        first = next((i for i in range(m + 1, end) if diffs[i] > thr), None)
        done = settle(diffs, first, end, still, thr) if first is not None else None
        out.append({"mark": m, "first": first, "settled": done,
                    "response_ms": None if first is None else (first - m) * 1000 / fps,
                    "settle_ms": None if done is None else (done - m) * 1000 / fps})
    return out


def onsets(diffs, fps, gap_ms=120.0, thr=None):
    """Suggested key-down frames from the key area: where it starts to move
    after gap_ms of stillness. A person checks them."""
    thr = noise_floor(diffs) if thr is None else thr
    gap = max(1, round(gap_ms * fps / 1000))
    out, quiet = [], gap
    for i, d in enumerate(diffs):
        if d > thr:
            if quiet >= gap:
                out.append(i)
            quiet = 0
        else:
            quiet += 1
    return out


def video_fps(path):
    out = subprocess.run(["ffprobe", "-v", "error", "-select_streams", "v:0", "-show_entries",
                          "stream=avg_frame_rate", "-of", "default=nw=1:nk=1", path],
                         capture_output=True, text=True, check=True).stdout.strip()
    a, _, b = out.partition("/")
    return float(a) / float(b or 1)


def video_diffs(path, roi):
    """diffs[i]: the mean absolute change of the area from frame i-1 to i,
    0 to 255 in gray (ffmpeg's tblend and signalstats)."""
    x, y, w, h = roi
    vf = (f"crop={w}:{h}:{x}:{y},format=gray,tblend=all_mode=difference,signalstats,"
          "metadata=mode=print:key=lavfi.signalstats.YAVG:file=-")
    out = subprocess.run(["ffmpeg", "-nostdin", "-v", "error", "-i", path, "-an", "-vf", vf, "-f", "null", "-"],
                         capture_output=True, text=True, check=True).stdout
    return [0.0] + [float(v) for v in re.findall(r"lavfi\.signalstats\.YAVG=([0-9.]+)", out)]


def parse_roi(s):
    try:
        v = [int(p) for p in s.split(",")]
    except ValueError:
        v = []
    if len(v) != 4 or min(v) < 0 or v[2] == 0 or v[3] == 0:
        raise argparse.ArgumentTypeError(f"{s}: X,Y,W,H in pixels")
    return v


def ms_or_dash(v):
    return "-" if v is None else f"+{v:.1f} ms"


def cmd_frames(a):
    if not shutil.which("ffmpeg") or not shutil.which("ffprobe"):
        sys.exit("compare: frames needs ffmpeg and ffprobe")
    fps = a.fps or video_fps(a.video)
    diffs = video_diffs(a.video, a.screen)
    marks = sorted(int(m) for m in a.marks.split(",")) if a.marks else []
    if not marks and a.key:
        marks = onsets(video_diffs(a.video, a.key), fps)
        print("suggested key-down frames (check them in a player, then give --marks): " + ",".join(map(str, marks)))
        return
    if not marks:
        sys.exit("compare: no key-down frames: --marks, or --key for suggestions")
    if marks[-1] >= len(diffs):
        sys.exit(f"compare: mark {marks[-1]} is past the video's {len(diffs)} frames")
    res = detect(diffs, marks, fps, a.settle_ms)
    for n, r in enumerate(res, 1):
        print(f"key {n} at frame {r['mark']}: first change {ms_or_dash(r['response_ms'])}, settled {ms_or_dash(r['settle_ms'])}")
    last = res[-1]["settled"]
    if last is None:
        sys.exit("compare: the screen did not change after the last key")
    secs = (last - marks[0]) / fps
    print(f"{len(marks)} keys, {secs:.3f} s from the first key down to the screen settled ({fps:g} fps)")
    if a.into and a.task:
        rows = [row(f"task.{a.task}.s", f"{secs:.3f}", "s", "video")]
        rows += [row("video.response_ms", f"{r['response_ms']:.1f}", "ms", "video") for r in res if r["response_ms"] is not None]
        append(a.into, rows)
        print(f"compare: task {a.task} into {a.into}")


# ---- Kario ---------------------------------------------------------------

def kario_perf(path):
    """The renderer's numbers of a run (perf-<run>.json, paint-stats.json)."""
    s = json.loads(read(path))
    out = []
    fr = s.get("frames", {}).get("frame_ms", {})
    for q in ("p50", "p95", "p99"):
        if fr.get(q) is not None:
            out.append((f"frames.{q}", fr[q], "ms"))
    if fr.get("n"):
        out.append(("frames.n", fr["n"], "frames"))
    motion = s.get("motion", {})
    moving = motion.get("interval_ms", {}).get("n") or 0
    if moving:
        out.append(("frames.jank_pct", round(100.0 * motion.get("late", 0) / moving, 2), "%"))
    key = s.get("key", {})
    for q in ("p50", "p95"):
        if key.get(q) is not None:
            out.append((f"keys.{q}", key[q], "ms"))
    starts = [(KARIO_APPS.get(x.get("app")), x.get("ms")) for x in s.get("start", [])]
    play = s.get("play") or {}
    return out, [(r, ms) for r, ms in starts if r and ms is not None], play


def kario_mem(text):
    m = {k: int(v) for k, v in re.findall(r"^(MemTotal|MemAvailable):\s+(\d+) kB", text, re.M)}
    pss = re.search(r"^(\d+) processes, ([0-9.]+) MB PSS", text, re.M)
    return m, (float(pss.group(2)) if pss else None)


def wake_reasons(lines):
    """The Shell's 'sleep: woke after D (why)' lines by why."""
    c = collections.Counter()
    for line in lines:
        m = re.search(r"sleep: woke after \S+ \((.*)\)\s*$", line)
        if m:
            c[m.group(1)] += 1
    return c


def kario_power(text):
    out = []
    m = re.search(r"^([0-9.]+) h, capacity (\d+)% -> (\d+)%, (-?\d+) suspends", text, re.M)
    if m:
        out += [("standby.hours", float(m.group(1)), "h"), ("standby.drop_pct", int(m.group(2)) - int(m.group(3)), "%"),
                ("standby.suspends", int(m.group(4)), "")]
    ma = re.search(r"^average current ([0-9.-]+) mA", text, re.M)
    if ma:
        out += [("standby.avg_ma", float(ma.group(1)), "mA"), ("standby.mah_day", round(float(ma.group(1)) * 24, 1), "mAh")]
    wakes = wake_reasons(text.splitlines())
    if "the Shell's sleeps" in text:
        out.append(("standby.wakeups", sum(wakes.values()), ""))
        if wakes:
            why, n = wakes.most_common(1)[0]
            out.append(("standby.top_wake", f"{why} ({n}x)", ""))
    return out


def kario_facts(text):
    out = []
    part = re.search(r"^== system\n(.*?)(?=^== |\Z)", text, re.M | re.S)
    sys_part = part.group(1) if part else text
    pretty = re.search(r'^PRETTY_NAME="?([^"\n]+)"?', sys_part, re.M)
    name = re.search(r'^NAME="?([^"\n]+)"?', sys_part, re.M)
    ver = re.search(r'^VERSION_ID="?([^"\n]+)"?', sys_part, re.M)
    if pretty:
        out.append(("update.os", pretty.group(1), ""))
    elif name:
        out.append(("update.os", name.group(1) + (" " + ver.group(1) if ver else ""), ""))
    k = re.search(r"^Linux \S+ (\S+)", text, re.M)
    if k:
        out.append(("update.kernel", k.group(1), ""))
    model = re.search(r"^model: (.+)$", text, re.M)
    if model:
        out.append(("facts.model", model.group(1).strip(), ""))
    mt = re.search(r"^MemTotal:\s+(\d+) kB", text, re.M)
    if mt:
        out.append(("facts.ram_mb", round(int(mt.group(1)) / 1024), "MB"))
    parts = re.findall(r"^\s*(\d+) CPU part\s*:\s*(\S+)", text, re.M)
    if parts:
        out.append(("facts.cpus", " + ".join(f"{n}x {p}" for n, p in parts), ""))
    cap = re.search(r"charge_full_design=(\d+)", text)
    if cap:
        out.append(("facts.battery_mah", round(int(cap.group(1)) / 1000), "mAh"))
    return out


def task_log(path):
    """kario-half.sh's tasks.log: 'task NAME REP BEFORE AFTER', the times
    (HHMMSS) of the dumps around a task."""
    out = []
    for line in read(path).splitlines():
        w = line.split()
        if len(w) == 5 and w[0] == "task":
            out.append((w[1], w[2], w[3], w[4]))
    return out


def inputs_between(before, after):
    """Keys and touches between two of the renderer's summaries."""
    def counts(s):
        i = s.get("inputs", {})
        return i.get("kernel", 0) + i.get("read", 0), s.get("touch", {}).get("n", 0)
    k0, t0 = counts(before)
    k1, t1 = counts(after)
    return max(0, (k1 - k0) - (t1 - t0)), t1 - t0


def cmd_kario(a):
    dirs = a.dirs
    rows = []
    perf = find(dirs, f"perf-{a.run}.json") or find(dirs, f"{a.run}.json")
    if perf:
        nums, starts, play = kario_perf(perf[-1])
        src = f"tasks.play {a.run}"
        rows += [row("play." + k, v, u, src) for k, v, u in nums if k in ("frames.p95", "frames.jank_pct", "keys.p95")]
        rows += [row(f"start.cold.{r}", ms, "ms", src) for r, ms in starts]
        if play and play.get("state") != "done":
            print(f"compare: {os.path.basename(perf[-1])}: the sequence stopped: {play.get('error') or play.get('state')}", file=sys.stderr)
    for phase in ("idle", "apps"):
        f = find(dirs, f"mem-{phase}.txt")
        if not f:
            continue
        m, pss = kario_mem(read(f[-1]))
        if "MemTotal" in m:
            rows.append(row("mem.total_mb", round(m["MemTotal"] / 1024), "MB", "/proc/meminfo"))
            if "MemAvailable" in m:
                rows.append(row(f"mem.used_{phase}_mb", round((m["MemTotal"] - m["MemAvailable"]) / 1024), "MB", "/proc/meminfo"))
        if pss is not None and phase == "apps":
            rows.append(row("mem.pss_mb", pss, "MB", "kit mem: PSS of the system's processes"))
    logs = find(dirs, "tasks.log")
    pairs = task_log(logs[-1]) if logs else []
    last_after = None
    for task, _rep, t0, t1 in pairs:
        p0, p1 = find(dirs, f"paint-{t0}.json"), find(dirs, f"paint-{t1}.json")
        if not p0 or not p1:
            print(f"compare: task {task}: no paint-{t0}.json or paint-{t1}.json (collect first)", file=sys.stderr)
            continue
        keys, touches = inputs_between(json.loads(read(p0[-1])), json.loads(read(p1[-1])))
        rows += [row(f"task.{task}.keys", keys, "presses", "dump: renderer inputs"),
                 row(f"task.{task}.touches", touches, "", "dump: renderer touches")]
        last_after = t1
    paints = find(dirs, "paint-*.json")
    if paints:
        session = find(dirs, f"paint-{last_after}.json") if last_after else paints[-1:]
        nums, _, _ = kario_perf(session[-1])
        rows += [row(k, v, u, "renderer: real keys since the Shell started") for k, v, u in nums]
    dumps = find(dirs, "dump-*.json")
    if dumps:
        d = json.loads(read((find(dirs, f"dump-{last_after}.json") or dumps)[-1]))
        running = {x.get("ID") for x in d.get("apps") or [] if x.get("Running")}
        rows.append(row("mem.kept", sum(1 for x in SWITCH_APPS if x in running), "of 3", "dump: apps running"))
        rows.append(row("mem.kills", d.get("low_memory_kills", 0), "", "dump: low_memory_kills"))
    power = find(dirs, "power.txt")
    if power:
        rows += [row(k, v, u, "kit power-stop") for k, v, u in kario_power(read(power[-1]))]
    facts = find(dirs, "facts.txt")
    if facts:
        fr = kario_facts(read(facts[-1]))
        rows += [row(k, v, u, "kit facts") for k, v, u in fr]
        cap = next((v for k, v, _ in fr if k == "facts.battery_mah"), None)
        day = next((r["value"] for r in rows if r["id"] == "standby.mah_day"), None)
        if cap and day:
            rows.append(row("standby.days", round(cap / day, 1), "days", "battery / drain per day"))
    if not rows:
        sys.exit(f"compare: nothing of the Kario half in {' '.join(dirs)}")
    emit(rows, a.out, a.hand)


# ---- Android -------------------------------------------------------------

def android_facts(text):
    f = dict(re.findall(r"^(\w+): ?(.*)$", text, re.M))
    out = []
    if f.get("model"):
        out.append(("facts.model", f["model"].strip(), ""))
    if f.get("soc"):
        out.append(("facts.soc", f["soc"].strip(), ""))
    if f.get("cpus"):
        mhz = num(f.get("cpu_max_khz"))
        out.append(("facts.cpus", f["cpus"].strip() + (f" up to {mhz / 1000:.0f} MHz" if mhz else ""), ""))
    if num(f.get("mem_kb")):
        out.append(("facts.ram_mb", round(num(f["mem_kb"]) / 1024), "MB"))
    if f.get("screen"):
        out.append(("facts.screen", f["screen"].replace("Physical size:", "").strip(), ""))
    if f.get("android"):
        out.append(("update.os", f"Android {f['android'].strip()}" + (f" ({f['build'].strip()})" if f.get("build") else ""), ""))
    if f.get("kernel"):
        out.append(("update.kernel", f["kernel"].strip(), ""))
    if f.get("patch"):
        out.append(("update.patch", f["patch"].strip(), ""))
    if f.get("ab"):
        out.append(("update.ab", "yes" if f["ab"].strip() == "true" else "no", ""))
    return out


def am_start(text):
    """(cold or warm, TotalTime ms) of each 'am start -W' in text; a block
    after '== cold' or '== warm' is that, unless it says its LaunchState."""
    out = []
    for block in re.split(r"^== ", text, flags=re.M)[1:]:
        kind = block.split()[0]
        state = re.search(r"^LaunchState: (\w+)", block, re.M)
        total = re.search(r"^TotalTime: (\d+)", block, re.M)
        if not total:
            continue
        if state:
            kind = "cold" if state.group(1) == "COLD" else "warm"
        out.append((kind, int(total.group(1))))
    return out


def framestats(text):
    """What 'dumpsys gfxinfo PKG framestats' says of the frames since its
    reset: their times (ms) from the framestats rows (its last 120 frames;
    those flagged as not drawn normally, Flags != 0, left out), the
    histogram of all of them (ms -> frames), the frames and the janky."""
    hist = collections.Counter()
    for line in re.findall(r"^\s*HISTOGRAM: (.*)$", text, re.M):
        for ms, n in re.findall(r"(\d+)ms=(\d+)", line):
            hist[int(ms)] += int(n)
    times = []
    for block in re.findall(r"---PROFILEDATA---\n(.*?)---PROFILEDATA---", text, re.S):
        lines = [x for x in block.strip().splitlines() if x.strip()]
        if not lines:
            continue
        head = [h.strip() for h in lines[0].split(",")]
        try:
            fl, iv, fc = head.index("Flags"), head.index("IntendedVsync"), head.index("FrameCompleted")
        except ValueError:
            continue
        for line in lines[1:]:
            v = line.split(",")
            if len(v) <= max(fl, iv, fc) or v[fl].strip() != "0":
                continue
            a, b = int(v[iv]), int(v[fc])
            if b > a > 0:
                times.append((b - a) / 1e6)
    total = sum(int(n) for n in re.findall(r"^\s*Total frames rendered: (\d+)", text, re.M))
    janky = sum(int(n) for n in re.findall(r"^\s*Janky frames: (\d+)", text, re.M))
    return times, hist, total, janky


def hist_rank(hist, q):
    """The q-quantile by nearest rank of a histogram (value -> count)."""
    n = sum(hist.values())
    want, seen = max(1, math.ceil(q * n)), 0
    for v in sorted(hist):
        seen += hist[v]
        if seen >= want:
            return v
    return None


def getevent(text):
    """(key presses, touches) in 'getevent -lt' output: keys going down,
    the touchscreen's contacts beginning."""
    keys = btn = contacts = 0
    for kind, code, value in re.findall(r"EV_(KEY|ABS)\s+(\S+)\s+(\S+)", text):
        if kind == "KEY" and value == "DOWN":
            if code == "BTN_TOUCH":
                btn += 1
            elif not code.startswith("BTN_TOOL"):
                keys += 1
        elif code == "ABS_MT_TRACKING_ID" and value != "ffffffff":
            contacts += 1
    return keys, btn or contacts


def android_mem(text):
    m = {k: int(v) for k, v in re.findall(r"^(MemTotal|MemAvailable):\s+(\d+) kB", text, re.M)}
    pss = re.search(r"Used RAM:\s*[0-9,]+K \(\s*([0-9,]+)K used pss", text)
    alive = dict(re.findall(r"^alive (\S+) (\S+)$", text, re.M))
    return m, (num(pss.group(1)) / 1024 if pss else None), alive


def duration_h(s):
    """'1d 2h 3m 4s 5ms' as hours."""
    t = 0.0
    for v, u in re.findall(r"(\d+)(ms|d|h|m|s)", s):
        t += int(v) * {"d": 86400, "h": 3600, "m": 60, "s": 1, "ms": 0.001}[u]
    return t / 3600


def battery(text):
    f = dict(re.findall(r"^\s*([A-Za-z ]+): (-?\d+)\s*$", text, re.M))
    return {"time": num(f.get("time")), "level": num(f.get("level")), "charge_uah": num(f.get("Charge counter"))}


def batterystats(text):
    out = {}
    since = text.split("Statistics since last charge:", 1)
    body = since[1] if len(since) > 1 else text
    tob = re.search(r"^\s*Time on battery: ([^(]+)\(", body, re.M)
    if tob:
        out["hours"] = duration_h(tob.group(1))
    dis = re.search(r"^\s*Discharge: ([0-9.]+) mAh", body, re.M)
    if dis:
        out["discharge_mah"] = float(dis.group(1))
    cap = re.search(r"Capacity: ([0-9.]+),", body)
    if cap:
        out["capacity_mah"] = float(cap.group(1))
    wakes = collections.Counter()
    for why, n in re.findall(r"^\s*Wakeup reason (.+?): .*\((\d+) times\)", body, re.M):
        wakes[why] += int(n)
    out["wakes"] = wakes
    return out


def cmd_android(a):
    d = a.dir
    rows = []

    def files(pattern):
        return find([d], pattern)

    facts = files("facts.txt")
    if facts:
        rows += [row(k, v, u, "getprop, /proc") for k, v, u in android_facts(read(facts[-1]))]
    for f in files("start-*.txt"):
        role = os.path.basename(f)[len("start-"):-len(".txt")]
        rows += [row(f"start.{kind}.{role}", ms, "ms", "am start -W TotalTime") for kind, ms in am_start(read(f))]
    times, hist, total, janky = [], collections.Counter(), 0, 0
    for f in files("gfx-*.txt"):
        t, h, n, j = framestats(read(f))
        times += t
        hist.update(h)
        total, janky = total + n, janky + j
    qs = (("p50", 0.5), ("p95", 0.95), ("p99", 0.99))
    if hist:
        rows += [row(f"frames.{q}", hist_rank(hist, v), "ms", "gfxinfo histogram") for q, v in qs]
        rows.append(row("frames.n", sum(hist.values()), "frames", "gfxinfo histogram"))
    elif times:
        rows += [row(f"frames.{q}", round(nearest_rank(times, v), 1), "ms", "gfxinfo framestats") for q, v in qs]
        rows.append(row("frames.n", len(times), "frames", "gfxinfo framestats"))
    if total:
        rows.append(row("frames.jank_pct", round(100.0 * janky / total, 2), "%", "gfxinfo janky frames"))
    for f in files("keys-*.txt"):
        task = os.path.basename(f).split("-")[1].split(".")[0]
        keys, touches = getevent(read(f))
        rows += [row(f"task.{task}.keys", keys, "presses", "getevent"), row(f"task.{task}.touches", touches, "", "getevent")]
    for phase in ("idle", "apps"):
        f = files(f"mem-{phase}.txt")
        if not f:
            continue
        m, pss, alive = android_mem(read(f[-1]))
        if "MemTotal" in m:
            rows.append(row("mem.total_mb", round(m["MemTotal"] / 1024), "MB", "/proc/meminfo"))
            if "MemAvailable" in m:
                rows.append(row(f"mem.used_{phase}_mb", round((m["MemTotal"] - m["MemAvailable"]) / 1024), "MB", "/proc/meminfo"))
        if phase == "apps":
            if pss is not None:
                rows.append(row("mem.pss_mb", round(pss, 1), "MB", "dumpsys meminfo: used pss"))
            if alive:
                rows.append(row("mem.kept", sum(1 for v in alive.values() if v != "-"), f"of {len(alive)}", "pidof"))
    b0, b1, bs = files("battery-start.txt"), files("battery-stop.txt"), files("batterystats.txt")
    stats = batterystats(read(bs[-1])) if bs else {}
    cap = stats.get("capacity_mah")
    if b0 and b1:
        s0, s1 = battery(read(b0[-1])), battery(read(b1[-1]))
        h = (s1["time"] - s0["time"]) / 3600 if s0["time"] and s1["time"] else stats.get("hours")
        if h:
            rows.append(row("standby.hours", round(h, 2), "h", "date"))
        if s0["level"] is not None and s1["level"] is not None:
            rows.append(row("standby.drop_pct", int(s0["level"] - s1["level"]), "%", "dumpsys battery"))
        ma, src = None, ""
        if stats.get("discharge_mah") and stats.get("hours"):
            ma, src = stats["discharge_mah"] / stats["hours"], "batterystats discharge"
        elif s0["charge_uah"] and s1["charge_uah"] and h:
            ma, src = (s0["charge_uah"] - s1["charge_uah"]) / 1000 / h, "charge counter"
        if ma is not None:
            rows += [row("standby.avg_ma", round(ma, 1), "mA", src), row("standby.mah_day", round(ma * 24, 1), "mAh", src)]
            if cap:
                rows.append(row("standby.days", round(cap / (ma * 24), 1), "days", "battery / drain per day"))
    if cap:
        rows.append(row("facts.battery_mah", round(cap), "mAh", "batterystats"))
    if stats.get("wakes") is not None and bs:
        wakes = stats["wakes"]
        rows.append(row("standby.wakeups", sum(wakes.values()), "", "batterystats wakeup reasons"))
        if wakes:
            why, n = wakes.most_common(1)[0]
            rows.append(row("standby.top_wake", f"{why} ({n}x)", "", "batterystats"))
    if not rows:
        sys.exit(f"compare: nothing of the Android half in {d}")
    emit(rows, a.out, a.hand)


# ---- the table -----------------------------------------------------------

def summary(values):
    """A side's cell: the median of numbers (with their count), else the
    values as given."""
    nums = [num(v) for v in values]
    if values and all(n is not None for n in nums):
        m = statistics.median(nums)
        return fmt(float(m)) + (f" (n={len(nums)})" if len(nums) > 1 else "")
    seen = []
    for v in values:
        if v not in seen:
            seen.append(v)
    return "; ".join(seen)


def side_name(path):
    b = os.path.basename(path)
    b = b[:-4] if b.endswith(".csv") else b
    return b[len("results-"):] if b.startswith("results-") else b


def build_table(template, sides):
    """Rows (id, what, unit, cells...) in the template's order; ids a side
    has and the template not come last."""
    vals = []
    for _name, rows in sides:
        v = collections.defaultdict(list)
        for r in rows:
            v[r["id"]].append(r["value"])
        vals.append(v)
    out, known = [], set()
    for t in template:
        known.add(t["id"])
        out.append([t["id"], t.get("what", ""), t.get("unit", "")] + [summary(v.get(t["id"], [])) for v in vals])
    extra = sorted({i for v in vals for i in v} - known)
    for i in extra:
        out.append([i, "", ""] + [summary(v.get(i, [])) for v in vals])
    return out, extra


def cmd_table(a):
    template = load(a.template)
    sides = [(side_name(p), load(p)) for p in a.csvs]
    table, extra = build_table(template, sides)
    if extra:
        print(f"compare: not in {os.path.basename(a.template)}: {', '.join(extra)}", file=sys.stderr)
    head = ["id", "what", "unit"] + [n for n, _ in sides]
    print("| " + " | ".join(head) + " |")
    print("|" + "---|" * len(head))
    for r in table:
        print("| " + " | ".join(c.replace("|", "/") or "-" for c in r) + " |")
    if a.csv:
        with open(a.csv, "w", newline="", encoding="utf-8") as f:
            w = csv.writer(f)
            w.writerow(head)
            w.writerows(table)


def cmd_set(a):
    append(a.csv, [row(a.id, a.value, a.unit, a.source)])


# ---- the contacts --------------------------------------------------------

# Ten common surnames and twenty given names: two hundred people, each
# name once, Zhang San and Li Si (the tasks' contacts) besides.
SURNAMES = "\u738b\u674e\u5f20\u5218\u9648\u6768\u9ec4\u8d75\u5434\u5468"
GIVEN = ["\u4f1f", "\u82b3", "\u5a1c", "\u654f", "\u9759", "\u4e3d", "\u5f3a", "\u78ca", "\u519b", "\u6d0b",
         "\u52c7", "\u8273", "\u6770", "\u6d9b", "\u660e", "\u8d85", "\u79c0\u82f1", "\u534e", "\u5e73", "\u521a"]
ZHANG_SAN, LI_SI = "\u5f20\u4e09", "\u674e\u56db"


def vcf(count, zhangsan, lisi):
    people = [(ZHANG_SAN, zhangsan), (LI_SI, lisi)]
    for i in range(count):
        s, g = SURNAMES[i % len(SURNAMES)], GIVEN[(i // len(SURNAMES)) % len(GIVEN)]
        people.append((s + g, f"+86 10 0000 {i:04d}"))
    cards = []
    for name, tel in people:
        cards.append(f"BEGIN:VCARD\r\nVERSION:3.0\r\nFN:{name}\r\nN:{name[0]};{name[1:]};;;\r\nTEL;TYPE=CELL:{tel}\r\nEND:VCARD\r\n")
    return "".join(cards)


def cmd_vcf(a):
    if a.count > len(SURNAMES) * len(GIVEN):
        sys.exit(f"compare: at most {len(SURNAMES) * len(GIVEN)} contacts besides the two")
    sys.stdout.write(vcf(a.count, a.zhangsan, a.lisi))


def main(argv=None):
    p = argparse.ArgumentParser(description=__doc__.split("\n\n")[0], formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = p.add_subparsers(dest="cmd", required=True)
    f = sub.add_parser("frames", help="time a task from a high-speed video")
    f.add_argument("video")
    f.add_argument("--screen", type=parse_roi, required=True, help="the screen's area X,Y,W,H")
    f.add_argument("--marks", help="key-down frames, comma-separated")
    f.add_argument("--key", type=parse_roi, help="the keys' area: suggest key-down frames")
    f.add_argument("--fps", type=float, help="the capture rate, when the file is retimed (240)")
    f.add_argument("--settle-ms", type=float, default=300.0, help="still this long: settled (300)")
    f.add_argument("--task", choices=TASKS)
    f.add_argument("--into", help="add the task's time to this CSV")
    k = sub.add_parser("kario", help="the Kario half's rows")
    k.add_argument("dirs", nargs="+")
    k.add_argument("--run", default="cpu-target", help="the perf run of tasks.play (cpu-target)")
    an = sub.add_parser("android", help="the Android half's rows")
    an.add_argument("dir")
    for x in (k, an):
        x.add_argument("--out", help="write the side's CSV here (else stdout)")
        x.add_argument("--hand", help="the rows measured by hand to add (frames --into, set)")
    s = sub.add_parser("set", help="add a row measured by hand")
    s.add_argument("csv")
    s.add_argument("id")
    s.add_argument("value")
    s.add_argument("--unit", default="")
    s.add_argument("--source", default="by hand")
    t = sub.add_parser("table", help="the sides next to each other")
    t.add_argument("template")
    t.add_argument("csvs", nargs="+")
    t.add_argument("--csv", help="also write the table as CSV")
    v = sub.add_parser("vcf", help="the contacts both phones import")
    v.add_argument("--count", type=int, default=200)
    v.add_argument("--zhangsan", default="+86 10 0000 9001", help="Zhang San's number: the second phone's")
    v.add_argument("--lisi", default="+86 10 0000 9002")
    a = p.parse_args(argv)
    {"frames": cmd_frames, "kario": cmd_kario, "android": cmd_android, "set": cmd_set,
     "table": cmd_table, "vcf": cmd_vcf}[a.cmd](a)


if __name__ == "__main__":
    main()
