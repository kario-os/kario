"""Tests for tools/android-compare (40-testing.md §9) without phones: a
task timed from a video (frames worked out of a clip ffmpeg makes here,
when it is installed), each half's numbers read from the files its tools
write, the sides' table, the contacts both phones import, and the scripts'
plans (the Android half also against a stand-in adb)."""
import csv
import io
import json
import os
import shutil
import subprocess
import sys
import tempfile
import unittest
from contextlib import redirect_stderr, redirect_stdout

FW = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
DIR = os.path.join(FW, "tools", "android-compare")
sys.path.insert(0, DIR)
import compare  # noqa: E402


def write(path, text):
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "w", encoding="utf-8") as f:
        f.write(text)


def slurp(path):
    with open(path, encoding="utf-8") as f:
        return f.read()


def rows_of(path):
    with open(path, newline="", encoding="utf-8") as f:
        return list(csv.DictReader(f))


def values(rows, rid):
    return [r["value"] for r in rows if r["id"] == rid]


def main(*argv):
    out, err = io.StringIO(), io.StringIO()
    with redirect_stdout(out), redirect_stderr(err):
        compare.main(list(argv))
    return out.getvalue(), err.getvalue()


PERF = {
    "frames": {"frame_ms": {"n": 2000, "p50": 4.2, "p95": 9.8, "p99": 14.1, "max": 30.0}},
    "motion": {"interval_ms": {"n": 400}, "late": 6, "dropped": 9},
    "key": {"n": 150, "p50": 21.5, "p95": 38.0},
    "touch": {"n": 0},
    "inputs": {"kernel": 0, "read": 0, "play": 150},
    "start": [{"app": "com.phone.chat", "ms": 120.5}, {"app": "com.phone.wallet", "ms": 95.0}, {"app": "phone.other", "ms": 1.0}],
    "play": {"state": "done", "line": 0},
}

FACTS = """facts of oneplus-fajita-t9 (oneplus-fajita), whole, Thu Nov 26

== unverified values of the board, and where the facts answer them
display.inset: look at the panel

== system
NAME="Kario"
VERSION_ID=0.6
PRETTY_NAME="Kario 0.6"
model: OnePlus 6T
Linux kario 6.12.4-sdm845 #1 SMP PREEMPT aarch64 GNU/Linux
MemTotal:        7841232 kB
      4 CPU part	: 0x803
      4 CPU part	: 0x802

== power supplies
/sys/class/power_supply/battery: type=Battery charge_full_design=3700000 capacity=90
"""

POWER = """start: 1000 90 3000000 - 4100000 -50000 10 Discharging
stop:  87400 80 2760000 - 4000000 -9000 106 Discharging
24.00 h, capacity 90% -> 80%, 96 suspends
average current 10.0 mA (charge)
suspend_stats:
  success: 106
the Shell's sleeps since power-start:
phone-shell: sleep: mem after 30s with the screen off
phone-shell: sleep: woke after 15m0s (rtc alarm)
phone-shell: sleep: woke after 15m0s (rtc alarm)
phone-shell: sleep: woke after 2s (irq 23; wake-up sources pushd)
"""

GFX = """Applications Graphics Acceleration Info:
** Graphics info for pid 1234 [com.tencent.mm] **

Stats since: 1000000ns
Total frames rendered: 40
Janky frames: 4 (10.00%)
50th percentile: 6ms
HISTOGRAM: 5ms=10 6ms=20 7ms=5 18ms=3 40ms=2
GPU HISTOGRAM: 1ms=40

---PROFILEDATA---
Flags,FrameTimelineVsyncId,IntendedVsync,Vsync,HandleInputStart,FrameCompleted,
0,1,1000000000,1000000000,1000100000,1006000000,
1,2,1016000000,1016000000,1016100000,1060000000,
0,3,1033000000,1033000000,1033100000,1053000000,
---PROFILEDATA---
"""

BATTERYSTATS = """Statistics since last charge:
  System starts: 0, currently on battery: false
  Time on battery: 1d 0h 0m 0s 0ms (99.0%) realtime, 1h 0m 0s 0ms (4.0%) uptime
  Discharge: 240 mAh
  Screen off discharge: 230 mAh
  Wakeup reason 200:qcom,smd-modem: 2m 3s 0ms realtime (40 times)
  Wakeup reason Abort:Pending Wakeup Sources: 1s 0ms realtime (5 times)
  Estimated power use (mAh):
    Capacity: 2600, Computed drain: 250, actual drain: 230-250
"""


class VideoTest(unittest.TestCase):
    # A key at frame 10, the screen changing at 14, 20 and 26 (an animation)
    # and still after: the response is 4 frames, it settled at 26.
    def test_a_key_s_response_and_settling(self):
        diffs = [0.2] * 200
        for i in (14, 20, 26):
            diffs[i] = 40.0
        r = compare.detect(diffs, [10], 240, settle_ms=50)
        self.assertEqual((r[0]["first"], r[0]["settled"]), (14, 26))
        self.assertAlmostEqual(r[0]["response_ms"], 4 * 1000 / 240)
        # A shorter stillness counts the first change as the end.
        self.assertEqual(compare.detect(diffs, [10], 240, settle_ms=20)[0]["settled"], 14)

    # Each key is answered before the next goes down; a key that changes
    # nothing has no response and no settling.
    def test_keys_in_a_row(self):
        diffs = [0.1] * 120
        diffs[30] = diffs[61] = 25.0
        r = compare.detect(diffs, [20, 40, 55], 240)
        self.assertEqual([(x["first"], x["settled"]) for x in r], [(30, 30), (None, None), (61, 61)])

    # The noise of a filmed screen is not a change; a key area's motion
    # after a pause is a suggested key-down.
    def test_noise_and_onsets(self):
        diffs = [1.0, 1.2, 0.9, 1.1, 1.0] * 20
        self.assertGreater(compare.noise_floor(diffs), 1.2)
        diffs[30] = diffs[31] = diffs[90] = 30.0
        self.assertEqual(compare.onsets(diffs, 240, gap_ms=100), [30, 90])

    # The whole way through ffmpeg: a clip at 240 fps whose screen area
    # changes at frames 48, 60 and 72 (boxes appear), the key down at 40.
    @unittest.skipUnless(shutil.which("ffmpeg") and shutil.which("ffprobe"), "no ffmpeg")
    def test_a_task_timed_from_a_clip(self):
        tmp = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, tmp)
        clip = os.path.join(tmp, "task.nut")
        boxes = ",".join(f"drawbox=x={x}:y=10:w=12:h=12:color=white:t=fill:enable='gte(n,{n})'"
                         for x, n in ((4, 48), (24, 60), (44, 72)))
        subprocess.run(["ffmpeg", "-nostdin", "-v", "error", "-f", "lavfi", "-i", "color=c=black:s=64x48:r=240:d=0.6",
                        "-vf", boxes, "-c:v", "rawvideo", "-pix_fmt", "gray", clip], check=True)
        hand = os.path.join(tmp, "hand-kario.csv")
        out, _ = main("frames", clip, "--screen", "0,0,64,48", "--marks", "40", "--settle-ms", "100", "--task", "dial", "--into", hand)
        self.assertIn("key 1 at frame 40: first change +33.3 ms, settled +133.3 ms", out)
        self.assertIn("1 keys, 0.133 s from the first key down to the screen settled (240 fps)", out)
        rows = rows_of(hand)
        self.assertEqual(values(rows, "task.dial.s"), ["0.133"])
        self.assertEqual(values(rows, "video.response_ms"), ["33.3"])
        # Outside the boxes nothing changes: no response.
        with self.assertRaises(SystemExit):
            main("frames", clip, "--screen", "0,30,64,18", "--marks", "40")


class KarioTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.tmp)

    def test_the_renderer_s_numbers(self):
        path = os.path.join(self.tmp, "perf-cpu-target.json")
        write(path, json.dumps(PERF))
        nums, starts, play = compare.kario_perf(path)
        got = {k: v for k, v, _ in nums}
        self.assertEqual((got["frames.p95"], got["keys.p95"], got["frames.n"]), (9.8, 38.0, 2000))
        self.assertEqual(got["frames.jank_pct"], 1.5)
        self.assertEqual(starts, [("messenger", 120.5), ("payment", 95.0)])
        self.assertEqual(play["state"], "done")

    def test_power_and_wakes(self):
        got = {k: v for k, v, _ in compare.kario_power(POWER)}
        self.assertEqual((got["standby.hours"], got["standby.drop_pct"], got["standby.suspends"]), (24.0, 10, 96))
        self.assertEqual((got["standby.avg_ma"], got["standby.mah_day"]), (10.0, 240.0))
        self.assertEqual(got["standby.wakeups"], 3)
        self.assertEqual(got["standby.top_wake"], "rtc alarm (2x)")

    def test_facts(self):
        got = {k: v for k, v, _ in compare.kario_facts(FACTS)}
        self.assertEqual(got["update.os"], "Kario 0.6")
        self.assertEqual(got["update.kernel"], "6.12.4-sdm845")
        self.assertEqual((got["facts.ram_mb"], got["facts.battery_mah"]), (7657, 3700))
        self.assertEqual(got["facts.cpus"], "4x 0x803 + 4x 0x802")

    # The Kario half end to end: the kit's two runs (a reboot between),
    # kario-half.sh's notes, the dumps around two runs of a task; the rows
    # measured by hand come after.
    def test_the_kario_half_s_rows(self):
        out, kit = os.path.join(self.tmp, "kario"), os.path.join(self.tmp, "kario", "kit", "results")
        old, new = os.path.join(kit, "20261126-090000"), os.path.join(kit, "20261126-101500")
        write(os.path.join(old, "perf-cpu-target.json"), json.dumps(PERF))
        write(os.path.join(old, "facts.txt"), FACTS)
        write(os.path.join(out, "mem-idle.txt"), "MemTotal:        7841232 kB\nMemAvailable:    7000000 kB\n")
        write(os.path.join(out, "mem-apps.txt"), "MemTotal:        7841232 kB\nMemAvailable:    6800000 kB\n"
              "812 phone-shell 60000\n900 phone-paint 40000\n2 processes, 97.7 MB PSS\n")
        write(os.path.join(out, "power.txt"), POWER)
        write(os.path.join(out, "tasks.log"), "task dial 1 101600 101610\ntask dial 2 101620 101630\n")

        def paint(name, keys, touches, p95=11.0):
            write(os.path.join(new, f"paint-{name}.json"), json.dumps({
                "inputs": {"kernel": keys, "read": 0}, "touch": {"n": touches},
                "frames": {"frame_ms": {"n": 300, "p50": 5.0, "p95": p95, "p99": 13.0}}, "key": {"p50": 30.0, "p95": 44.0}}))
        paint("101600", 10, 0)
        paint("101610", 15, 0)
        paint("101620", 15, 0)
        paint("101630", 22, 1, p95=12.5)
        write(os.path.join(new, "dump-101630.json"), json.dumps({"low_memory_kills": 0, "apps": [
            {"ID": "com.phone.messages", "Running": True}, {"ID": "com.phone.chat", "Running": True},
            {"ID": "com.phone.contacts", "Running": False}]}))
        hand = os.path.join(out, "hand-kario.csv")
        compare.append(hand, [compare.row("task.dial.s", "1.9", "s", "video")])
        res = os.path.join(out, "results-kario.csv")
        for _ in range(2):
            main("kario", out, old + "/", new + "/", "--out", res, "--hand", hand)
        rows = rows_of(res)
        self.assertEqual(values(rows, "task.dial.keys"), ["5", "6"])
        self.assertEqual(values(rows, "task.dial.touches"), ["0", "1"])
        self.assertEqual(values(rows, "start.cold.messenger"), ["120.5"])
        self.assertEqual(values(rows, "play.frames.p95"), ["9.8"])
        self.assertEqual(values(rows, "frames.p95"), ["12.5"])
        self.assertEqual(values(rows, "mem.used_idle_mb"), ["822"])
        self.assertEqual(values(rows, "mem.used_apps_mb"), ["1017"])
        self.assertEqual(values(rows, "mem.pss_mb"), ["97.7"])
        self.assertEqual(values(rows, "mem.kept"), ["2"])
        self.assertEqual(values(rows, "standby.days"), ["15.4"])
        self.assertEqual(values(rows, "update.kernel"), ["6.12.4-sdm845"])
        self.assertEqual(values(rows, "task.dial.s"), ["1.9"])
        self.assertEqual(rows[-1]["id"], "task.dial.s")


class AndroidTest(unittest.TestCase):
    def test_am_start(self):
        text = ("== cold 1\nStarting: Intent { cmp=com.tencent.mm/.ui.LauncherUI }\nStatus: ok\nLaunchState: COLD\n"
                "TotalTime: 1834\nWaitTime: 1850\nComplete\n== warm 1\nStatus: ok\nLaunchState: HOT\nTotalTime: 142\n"
                "== warm 2\nStatus: ok\nThisTime: 150\nTotalTime: 150\n== cold 2\nStatus: timeout\n")
        self.assertEqual(compare.am_start(text), [("cold", 1834), ("warm", 142), ("warm", 150)])

    # The histogram has every frame since the reset, the framestats rows
    # only the last 120 (a frame flagged as not drawn normally left out).
    def test_gfxinfo(self):
        times, hist, total, janky = compare.framestats(GFX)
        self.assertEqual(times, [6.0, 20.0])
        self.assertEqual(sum(hist.values()), 40)
        self.assertEqual((total, janky), (40, 4))
        self.assertEqual(compare.hist_rank(hist, 0.5), 6)
        self.assertEqual(compare.hist_rank(hist, 0.95), 18)
        self.assertEqual(compare.hist_rank(hist, 0.99), 40)

    def test_getevent(self):
        text = ("[   10.000000] /dev/input/event1: EV_KEY       KEY_2                DOWN\n"
                "[   10.080000] /dev/input/event1: EV_KEY       KEY_2                UP\n"
                "[   11.000000] /dev/input/event1: EV_KEY       KEY_ENTER            DOWN\r\n"
                "[   11.000500] /dev/input/event1: EV_KEY       KEY_ENTER            REPEAT\n"
                "[   12.000000] /dev/input/event2: EV_ABS       ABS_MT_TRACKING_ID   0000002a\n"
                "[   12.000000] /dev/input/event2: EV_KEY       BTN_TOUCH            DOWN\n"
                "[   12.000000] /dev/input/event2: EV_KEY       BTN_TOOL_FINGER      DOWN\n"
                "[   12.100000] /dev/input/event2: EV_ABS       ABS_MT_TRACKING_ID   ffffffff\n")
        self.assertEqual(compare.getevent(text), (2, 1))
        self.assertEqual(compare.getevent(text.replace("BTN_TOUCH", "BTN_OTHER_X")), (3, 1))

    def test_battery(self):
        stats = compare.batterystats(BATTERYSTATS)
        self.assertEqual((stats["hours"], stats["discharge_mah"], stats["capacity_mah"]), (24.0, 240.0, 2600.0))
        self.assertEqual(stats["wakes"].most_common(1), [("200:qcom,smd-modem", 40)])
        b = compare.battery("time: 1700000000\nCurrent Battery Service state:\n  level: 85\n  Charge counter: 2100000\n")
        self.assertEqual((b["time"], b["level"], b["charge_uah"]), (1700000000, 85, 2100000))


FAKE_ADB = r"""#!/bin/sh
# adb as a keypad phone answers android-half.sh.
st=__STATE__
[ "$1" = -s ] && shift 2
echo "$*" >> "$st/calls"
case $1 in
reboot | wait-for-device | pull) exit 0 ;;
shell) shift ;;
esac
[ "$1" = -tt ] && shift
cmd="$*"
case $cmd in
"getprop ro.product.model") echo "Qin J36" ;;
"getprop ro.soc.model") echo "MT8766" ;;
"getprop ro.build.version.release") echo 13 ;;
"getprop ro.build.version.security_patch") echo 2026-07-05 ;;
"getprop ro.build.ab_update") echo false ;;
"getprop sys.boot_completed") echo 1 ;;
getprop*) echo ;;
"uname -r") printf '4.19.191+\r\n' ;;
*MemTotal*) echo 2873920 ;;
*processor*) echo 4 ;;
*cpuinfo_max_freq*) echo 2001000 ;;
"wm size") echo "Physical size: 480x640" ;;
"pm list packages") printf 'package:com.tencent.mm\npackage:com.mediatek.camera\n' ;;
"cmd package resolve-activity --brief -a"*) printf 'priority=0\ncom.android.launcher3/.Launcher\n' ;;
"cmd package resolve-activity --brief "*) printf 'priority=0\n%s/.Main\n' "${cmd##* }" ;;
"am force-stop "*) touch "$st/stopped-${cmd##* }" ;;
"am start -W -n "*)
	p=${cmd##* }; p=${p%%/*}
	if [ -f "$st/stopped-$p" ]; then s=COLD; t=1500; rm "$st/stopped-$p"; else s=HOT; t=120; fi
	printf 'Starting: Intent { cmp=%s }\nStatus: ok\nLaunchState: %s\nTotalTime: %s\nWaitTime: %s\nComplete\n' "$p" "$s" "$t" "$t" ;;
"input keyevent"*) ;;
"cat /proc/meminfo") printf 'MemTotal:        2873920 kB\nMemAvailable:    1200000 kB\n' ;;
"dumpsys meminfo") printf 'Total RAM: 2,873,920K (status normal)\n Used RAM: 1,600,000K (1,300,000K used pss + 300,000K kernel)\n' ;;
"pidof com.tencent.mm") echo 4321 ;;
pidof*) ;;
"date +%s") n=$(cat "$st/date" 2>/dev/null || echo 1700000000); echo "$n"; echo $((n + 86400)) > "$st/date" ;;
"dumpsys battery")
	if [ -f "$st/battery" ]; then l=80; c=2010000; else l=90; c=2250000; touch "$st/battery"; fi
	printf 'Current Battery Service state:\n  USB powered: true\n  level: %s\n  Charge counter: %s\n' "$l" "$c" ;;
"dumpsys batterystats --reset") echo "Battery stats reset." ;;
"dumpsys batterystats") cat "$st/batterystats" ;;
"dumpsys gfxinfo "*" reset") ;;
"dumpsys gfxinfo com.tencent.mm framestats") cat "$st/gfx" ;;
"dumpsys gfxinfo "*) ;;
"getevent -lt") printf '[ 1.0] /dev/input/event1: EV_KEY       KEY_2                DOWN\r\n' ;;
*) echo "fake adb: $cmd" >&2 ;;
esac
"""


class ScriptTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, self.tmp)

    def sh(self, *args, env=None, **kw):
        return subprocess.run(["sh", *args], capture_output=True, text=True, cwd=FW, env={**os.environ, **(env or {})}, **kw)

    def fake_adb(self):
        st, bindir = os.path.join(self.tmp, "adb-state"), os.path.join(self.tmp, "bin")
        os.makedirs(st)
        os.makedirs(bindir)
        write(os.path.join(st, "batterystats"), BATTERYSTATS)
        write(os.path.join(st, "gfx"), GFX)
        adb = os.path.join(bindir, "adb")
        write(adb, FAKE_ADB.replace("__STATE__", st))
        os.chmod(adb, 0o755)
        return st, {"PATH": bindir + os.pathsep + os.environ["PATH"]}

    # The Android half against a stand-in adb: the facts, cold and warm
    # starts, the memory with the apps open, a standby period; then its
    # rows and the table with a Kario column beside it.
    def test_the_android_half(self):
        st, env = self.fake_adb()
        out = os.path.join(self.tmp, "cmp", "j36")
        write(os.path.join(self.tmp, "cmp", "kario", "results-kario.csv"),
              "id,value,unit,source\nstart.cold.messenger,120.5,ms,renderer\nupdate.kernel,6.12.4,,kit facts\n")
        r = self.sh(os.path.join(DIR, "android-half.sh"), "--side", "j36", "--out", out, "--reps", "2",
                    "--pause", "0", "--pkg", "camera=com.mediatek.camera", "facts", "start", "mem", "standby-start", "standby-stop", "table", env=env)
        self.assertEqual(r.returncode, 0, r.stderr + r.stdout)
        facts = slurp(os.path.join(out, "facts.txt"))
        self.assertIn("model: Qin J36\n", facts)
        self.assertIn("kernel: 4.19.191+\n", facts)
        self.assertIn("  package:com.tencent.mm\n", facts)
        calls = slurp(os.path.join(st, "calls"))
        self.assertIn("shell am start -W -n com.mediatek.camera/.Main", calls)
        self.assertIn("shell dumpsys batterystats --reset", calls)
        rows = rows_of(os.path.join(out, "results-j36.csv"))
        self.assertEqual(values(rows, "facts.model"), ["Qin J36"])
        self.assertEqual(values(rows, "update.ab"), ["no"])
        self.assertEqual(values(rows, "start.cold.messenger"), ["1500", "1500"])
        self.assertEqual(values(rows, "start.warm.messenger"), ["120", "120"])
        self.assertEqual(values(rows, "mem.used_apps_mb"), ["1635"])
        self.assertEqual(values(rows, "mem.kept"), ["1"])
        self.assertEqual(values(rows, "standby.drop_pct"), ["10"])
        self.assertEqual(values(rows, "standby.avg_ma"), ["10.0"])
        self.assertEqual(values(rows, "standby.days"), ["10.8"])
        self.assertEqual(values(rows, "standby.top_wake"), ["200:qcom,smd-modem (40x)"])
        table = slurp(os.path.join(self.tmp, "cmp", "table.md"))
        self.assertIn("| id | what | unit | j36 | kario |", table)
        self.assertIn("| start.cold.messenger | Cold start: Hongyan / WeChat | ms | 1500 (n=2) | 120.5 |", table)
        self.assertIn("| update.kernel | Kernel version | - | 4.19.191+ | 6.12.4 |", table)

    # A timed task: gfxinfo reset for the apps whose frames count, getevent
    # through a terminal on the phone, then their framestats; the dry run
    # plans the same without a phone.
    def test_a_task_on_android(self):
        st, env = self.fake_adb()
        out = os.path.join(self.tmp, "j36")
        r = self.sh(os.path.join(DIR, "android-half.sh"), "--out", out, "--reps", "1", "--dry-run", "task", "chat", env=env)
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertIn("shell -tt getevent -lt > " + os.path.join(out, "keys-chat-1.txt"), r.stderr)
        self.assertIn("(waits for Enter)", r.stderr)
        self.assertFalse(os.path.exists(os.path.join(st, "calls")))
        r = self.sh(os.path.join(DIR, "android-half.sh"), "--out", out, "--reps", "1", "task", "chat", env=env, input="\n")
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        self.assertIn("task chat, run 1: 1 key-down events", r.stdout)
        calls = slurp(os.path.join(st, "calls"))
        for want in ("shell dumpsys gfxinfo com.tencent.mm reset", "shell dumpsys gfxinfo com.android.launcher3 reset",
                     "shell -tt getevent -lt", "shell dumpsys gfxinfo com.tencent.mm framestats"):
            self.assertIn(want, calls)
        self.assertIn("EV_KEY", slurp(os.path.join(out, "keys-chat-1.txt")))
        self.assertIn("HISTOGRAM", slurp(os.path.join(out, "gfx-chat-1-com.tencent.mm.txt")))
        self.assertNotEqual(self.sh(os.path.join(DIR, "android-half.sh"), "--dry-run", "task", "nap").returncode, 0)

    # The Kario half plans the kit's steps: tasks.play through perf at the
    # two sizes, a dump around each task run, the standby's power steps.
    def test_the_kario_half_plans(self):
        out = os.path.join(self.tmp, "kario")
        r = self.sh(os.path.join(DIR, "kario-half.sh"), "--sku", "oneplus-fajita-t9", "--whole", "--out", out, "--reps", "1",
                    "--dry-run", "facts", "perf", "mem-idle", "task", "dial", "standby-start", "standby-stop", "table",
                    env={"KARIO_INIT": "systemd"})
        self.assertEqual(r.returncode, 0, r.stderr)
        both = r.stdout + r.stderr
        for want in ("on-phone.sh facts", "KARIO_PERF='cpu-target cpu-full'", "on-phone.sh bg perf", "on-phone.sh mem",
                     "kill -USR1 $(cat /run/phone/shell.pid)", "on-phone.sh power-start", "on-phone.sh power-stop",
                     "tar -C /var/lib/kario-test/results -cf - .", f"compare.py kario {out}"):
            self.assertIn(want, both)
        self.assertEqual(slurp(os.path.join(out, "tasks.log")), "task dial 1 000000 000000\n")
        self.assertNotEqual(self.sh(os.path.join(DIR, "kario-half.sh"), "--dry-run", "facts").returncode, 0)

    # The traffic: each message to both phones at once, logged.
    def test_traffic(self):
        log = os.path.join(self.tmp, "traffic.log")
        r = self.sh(os.path.join(DIR, "traffic.sh"), "--chat", "http://chat.example:8791/", "--sender", "S1", "--tap", "440,600",
                    "--every", "0", "--count", "2", "--log", log, "--dry-run")
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertEqual(r.stderr.count("""curl -d '{"from":"lisi","to":"me","text":"cmp-"""), 2)
        self.assertIn("http://chat.example:8791/v1/deliver", r.stderr)
        self.assertIn("&& input tap 440 600'", r.stderr)
        lines = slurp(log).splitlines()
        self.assertEqual(len(lines), 2)
        self.assertRegex(lines[1], r"^\d+ cmp-2-\d{6} kario=ok wechat=ok$")
        self.assertEqual(self.sh(os.path.join(DIR, "traffic.sh"), "--count", "1").returncode, 1)

    # host.sh plays another sequence when asked, and refuses one that is not there.
    def test_host_plays_another_sequence(self):
        r = self.sh(os.path.join(FW, "tools", "perf", "host.sh"), "--play", os.path.join(self.tmp, "none.play"))
        self.assertEqual(r.returncode, 2)
        self.assertIn("none.play", r.stderr)

    def test_shellcheck(self):
        if not shutil.which("shellcheck"):
            self.skipTest("no shellcheck")
        scripts = [os.path.join(DIR, n) for n in ("android-half.sh", "kario-half.sh", "traffic.sh")]
        r = subprocess.run(["shellcheck", "-s", "sh", *scripts], capture_output=True, text=True)
        self.assertEqual(r.returncode, 0, r.stdout)


class TableTest(unittest.TestCase):
    # The sides next to each other in the template's order: a median and
    # its count for repeated numbers, words as they are, ids the template
    # lacks at the end and named.
    def test_the_table(self):
        tmp = tempfile.mkdtemp()
        self.addCleanup(shutil.rmtree, tmp)
        k, a = os.path.join(tmp, "results-kario.csv"), os.path.join(tmp, "results-j36.csv")
        compare.append(k, [compare.row("task.dial.keys", v) for v in ("4", "4", "5")] + [compare.row("update.ab", "no")])
        compare.append(a, [compare.row("task.dial.keys", "7"), compare.row("odd.one", "1")])
        out, err = main("table", os.path.join(DIR, "results-template.csv"), k, a, "--csv", os.path.join(tmp, "t.csv"))
        self.assertIn("| task.dial.keys | Dial Zhang San by T9 search: key presses | presses | 4 (n=3) | 7 |", out)
        self.assertIn("| update.ab | A/B updates (install in the background; roll back) | - | no | - |", out)
        self.assertTrue(out.rstrip().endswith("| odd.one | - | - | - | 1 |"))
        self.assertIn("not in results-template.csv: odd.one", err)
        self.assertEqual(rows_of(os.path.join(tmp, "t.csv"))[0]["kario"], "4 (n=3)")

    # Every row of the template names its axis and how each side gets it;
    # the ids the halves write are in it.
    def test_the_template(self):
        t = compare.load(os.path.join(DIR, "results-template.csv"))
        ids = [r["id"] for r in t]
        self.assertEqual(len(ids), len(set(ids)))
        for r in t:
            self.assertIn(r["axis"], ("keys", "hardware", "standby", "updates", "facts"), r["id"])
            self.assertTrue(r["what"] and r["kario"] and r["android"], r["id"])
        for task in compare.TASKS:
            for m in ("keys", "s", "touches"):
                self.assertIn(f"task.{task}.{m}", ids)
        for role in compare.ROLES:
            self.assertIn(f"start.cold.{role}", ids)
            self.assertIn(f"start.warm.{role}", ids)


class VcfTest(unittest.TestCase):
    # Two hundred contacts, each name once, Zhang San first with the second
    # phone's number.
    def test_the_contacts(self):
        out, _ = main("vcf", "--zhangsan", "+86 139 0000 0000")
        cards = out.split("BEGIN:VCARD")[1:]
        self.assertEqual(len(cards), 202)
        names = [c.split("FN:")[1].split("\r\n")[0] for c in cards]
        self.assertEqual(len(set(names)), 202)
        self.assertEqual(names[0], compare.ZHANG_SAN)
        self.assertIn("TEL;TYPE=CELL:+86 139 0000 0000\r\n", cards[0])
        with self.assertRaises(SystemExit):
            main("vcf", "--count", "201")


if __name__ == "__main__":
    unittest.main()
