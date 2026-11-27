#!/usr/bin/env python3
"""Test yocto/qemu-console.py on made-up consoles of a machine handing over
its report, the report's root, its results and a PNG through the journal
(the image's console-file run here under a POSIX awk, as busybox's is; the
PNG in phone-shell --print-png's format, which the Go test ties to it),
with a kernel line printed inside their lines: as in vm5's report in the
night chain's d94, at the start, in the middle and at the end of a line,
inside the mark, the numbers, the journal's prefix and a PNG's base64,
inside a UTF-8 character, between the carriage return and the newline, two
in a row, one inside the rest of a line already cut, inside the end line.
Every file is kept as the machine had it, the mended lines named. A line
damaged otherwise, cut with its rest gone, missing, a file without its end
or with another count: named with the machine's console and its line,
nothing kept of that file, exit status 1. A kernel line quoting a file's
line (the heartbeat's "last:") holds none; terminal controls and getty's
prompt before a line do not hide it. The selftest's requests to the host
(.ask, qemu-buttons.py's) are no file: none kept or named, however cut.
The text mode (the update tests' PHONE-OTA lines, the cloud test's
PHONE-CLOUD lines, the charger test's results) gives each line whole,
with the same failures. The report mode (the soak's, the stress run's and
the bench's reports) gives a report's lines whole and its verdict, with a
kernel line inside its first line, its result, its end or a number; a
failed report, a line damaged or missing, a report cut off before its end
or none at all: exit status 1; an older image's plain lines read as they
are.

    qemu-console-test.py
"""
import base64
import hashlib
import json
import os
import shutil
import subprocess
import sys
import tempfile

HERE = os.path.dirname(os.path.abspath(__file__))
TOOL = os.path.join(HERE, "qemu-console.py")
CONSOLE_FILE = os.path.join(HERE, "meta-phone", "recipes-phone", "phone-fw", "files", "console-file")

# vm5's console in d94 (2026-11-27): selftest-heartbeat.sh's kernel line
# printed at the end of a report line, the report going on after it. The
# night's notes kept the line up to "up 329.21 s,"; the rest as the
# heartbeat prints it (r74's console).
D94 = [
    b'PHONE-CTS-REPORT:     "audio.route",[  329.315920] phone-heartbeat: up 329.21 s, MemAvailable: 1690748 kB, last: PHONE-PNG-END 27-music',
    b'PHONE-CTS-REPORT:     "audio.state",',
]
HEARTBEAT = b"[  329.315920] phone-heartbeat: up 329.21 s, MemAvailable: 1690748 kB, last: PHONE-FILE 27-music.png 87 4b1d2c3e iVBORw0KGgoAAAANSUhEUgAAAtAAAAUAAAAAA"
SYSRQ = b"[  331.002113] sysrq: Show Blocked State"
JOURNAL = b"[  315.719402] selftest-boot.sh[503]: "
BOOT = [
    b"[    0.000000] Booting Linux on physical CPU 0x0000000000 [0x000f0510]",
    b"[    3.104311] EXT4-fs (vda): mounted filesystem with ordered data mode.",
    b"\x1b[!p\x1b]104\x1b\\\x1b[?7h\x1b[6n[   25.322830] selftest-boot.sh[439]: PHONE-MODULES: PASS: lockdown integrity",
    b"\x1b]3008;start=0e1f;type=boot\x1b\\[   25.400012] selftest-boot.sh[439]: phone-shell selftest: 41 cases",
]
LOGIN = b"\x1b[0m\x1b]3008;end=0e1f\x1b\\qemuarm64 login: "


def report():
    caps = ["alarm.cancel", "alarm.list", "audio.play", "audio.route", "audio.state", "bt.forget"]
    rep = {
        "system": "Kario", "version": "0.6", "result": "PART",
        "cases": [{"name": f"case {i}: caf\u00e9 \u00fcber", "status": "PASS", "seconds": 0.3} for i in range(30)],
        "uncovered": caps,
        "attestation": {"chain": [base64.b64encode(bytes(range(256)) * 3).decode()]},
    }
    return json.dumps(rep, indent=2, ensure_ascii=False).encode()


def png():
    data = b"\x89PNG\r\n\x1a\n"
    while len(data) < 3000:
        data += hashlib.sha256(data).digest()
    return data


class Machine:
    """Writes the files to hand over and runs console-file on them, under
    busybox's awk (the image's) where there is one, else a POSIX awk."""

    def __init__(self, d):
        self.d = d
        self.env = dict(os.environ)
        self.awk = "awk"
        for path, run in ((shutil.which("busybox"), "busybox awk"), (shutil.which("gawk"), "gawk --posix")):
            if path:
                bin_dir = os.path.join(d, "bin")
                os.makedirs(bin_dir)
                with open(os.path.join(bin_dir, "awk"), "w") as f:
                    f.write(f'#!/bin/sh\nexec {path} {run.split(" ", 1)[1]} "$@"\n')
                os.chmod(os.path.join(bin_dir, "awk"), 0o755)
                self.env["PATH"] = bin_dir + os.pathsep + self.env["PATH"]
                self.awk = run
                break

    def put(self, name, data):
        path = os.path.join(self.d, name)
        with open(path, "wb") as f:
            f.write(data)
        return path

    def hand(self, name, data, *mode):
        p = subprocess.run(["sh", CONSOLE_FILE, name, self.put("in-" + name, data), *mode],
                           capture_output=True, env=self.env)
        assert p.returncode == 0, p.stderr
        return p.stdout.split(b"\n")[:-1]

    def png(self, name, data):
        enc = base64.b64encode(data)
        lines = self.hand(name, b"".join(enc[i:i + 76] + b"\n" for i in range(0, len(enc), 76)))
        return [JOURNAL + line for line in lines[:-1]] + [JOURNAL + lines[-1] + b" base64"]


def console(lines):
    return b"".join(line + b"\r\n" for line in lines)


def splice(lines, which, at, kernel=(HEARTBEAT,)):
    """lines with the kernel's lines printed at byte at of line which
    (negative: from its end), its rest on a line after them."""
    out = list(lines)
    line = out[which]
    k = at if at >= 0 else len(line) + at
    out[which:which + 1] = [line[:k] + kernel[0]] + list(kernel[1:]) + [line[k:]]
    return out


def find(lines, part, after=0):
    return next(i for i, line in enumerate(lines) if i >= after and part in line)


def main():
    failures = []

    def check(what, cond, out=""):
        if not cond:
            failures.append(f"{what}\n{out}")

    with tempfile.TemporaryDirectory() as d:
        m = Machine(d)
        rep, image = report(), png()
        root = b"-----BEGIN PUBLIC KEY-----\nMFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAE\n-----END PUBLIC KEY-----\n"
        results = b"PHONE-MODULES: PASS: lockdown integrity\nPHONE-CTS: PASS, capabilities 25/85 exercised\nPHONE-SELFTEST-RESULT: PASS (41/41 cases)\nphone-selftest: exit code 0\n"
        want = {"cts-device.json": rep + b"\n", "cts-device.json.root": root, "results.txt": results, "27-music.png": image}
        pngs = m.png("27-music.png", image)
        files = m.hand("cts-device.json", rep) + m.hand("cts-device.json.root", root) + m.hand("results.txt", results)
        files[0] = LOGIN + files[0]
        sound = BOOT + pngs + [b"[  316.000001] selftest-boot.sh[503]: PHONE-GUI-FRAMES: 812"] + files + [SYSRQ]
        vm = os.path.join(d, "vm5")
        os.makedirs(vm)
        log = os.path.join(vm, "console.log")

        def files_mode(lines, raw=None):
            with open(log, "wb") as f:
                f.write(raw if raw is not None else console(lines))
            out = os.path.join(vm, "console")
            shutil.rmtree(out, ignore_errors=True)
            p = subprocess.run([sys.executable, TOOL, "files", log, out], capture_output=True, text=True)
            kept = {}
            for n in os.listdir(out) if os.path.isdir(out) else []:
                with open(os.path.join(out, n), "rb") as f:
                    kept[n] = f.read()
            return p.returncode, p.stdout + p.stderr, kept

        def kept_whole(what, lines, raw=None, mended=None):
            rc, out, kept = files_mode(lines, raw)
            check(f"{what}: every file kept as the machine had it", rc == 0 and kept == want,
                  out + f"\nkept {sorted(kept)}, differing: {[n for n in want if kept.get(n) != want[n]]}")
            if mended is not None:
                check(f"{what}: the mended line named", f"vm5/console.log: line {mended + 1}: " in out and " mended, " in out, out)

        def refused(what, lines, file, says, raw=None):
            rc, out, kept = files_mode(lines, raw)
            check(f"{what}: exit status 1, {file} not kept", rc == 1 and file not in kept, out)
            for s in says:
                check(f"{what}: says {s!r}", s in out, out)

        def text_mode(lines):
            with open(log, "wb") as f:
                f.write(console(lines))
            p = subprocess.run([sys.executable, TOOL, "text", log], capture_output=True)
            return p.returncode, p.stdout.decode("utf-8", "replace").split("\n"), p.stderr.decode()

        kept_whole("no kernel line inside", sound)
        route = find(sound, b'"audio.route",')
        kept_whole("d94: the heartbeat after vm5's \"audio.route\",", splice(sound, route, len(sound[route])), mended=route)
        rc, out, kept = files_mode(D94)
        check("d94's lines as they were (no console file): nothing kept from them", "cts-device.json" not in kept, out)
        state = find(sound, b'"audio.state"')
        kept_whole("at the start of a line", splice(sound, state, 0))
        kept_whole("at the start of the line after getty's prompt", splice(sound, find(sound, LOGIN), len(LOGIN)))
        kept_whole("inside the mark", splice(sound, state, 5), mended=state)
        kept_whole("inside the line's number", splice(sound, state, len(b"PHONE-FILE cts-device.json 1")), mended=state)
        kept_whole("inside the sum", splice(sound, state, len(b"PHONE-FILE cts-device.json 12 ab")), mended=state)
        text_at = sound[state].index(b'"audio.state"')
        kept_whole("at the start of the text", splice(sound, state, sound[state].index(b"    \"audio.state\"")), mended=state)
        kept_whole("in the middle of the text", splice(sound, state, text_at + 4), mended=state)
        kept_whole("at the end of the line", splice(sound, state, -1), mended=state)
        accent = find(sound, b"caf\xc3\xa9")
        kept_whole("inside a UTF-8 character", splice(sound, accent, sound[accent].index(b"\xc3\xa9") + 1), mended=accent)
        raw = console(sound[:route]) + sound[route] + b"\r" + HEARTBEAT + b"\r\n\n" + console(sound[route + 1:])
        kept_whole("between the carriage return and the newline", None, raw=raw, mended=route)
        kept_whole("two kernel lines in a row", splice(sound, state, text_at, (HEARTBEAT, SYSRQ)), mended=state)
        twice = splice(sound, state, text_at)
        twice = splice(twice, state + 1, 3, (SYSRQ,))
        kept_whole("a kernel line inside the rest of a line already cut", twice, mended=state)
        twice = splice(sound, state, text_at)
        twice = splice(twice, state, 8, (SYSRQ,))
        kept_whole("two kernel lines at two places of one line", twice, mended=state)
        chain = find(sound, b'"chain"', state) + 1
        kept_whole("inside a long line (the attestation's certificate)", splice(sound, chain, 500), mended=chain)
        end = find(sound, b"PHONE-FILE-END cts-device.json ")
        kept_whole("inside the end line", splice(sound, end, -3), mended=end)
        b64 = find(sound, b"PHONE-FILE 27-music.png 12 ")
        kept_whole("inside a PNG's base64 (through the journal)", splice(sound, b64, len(sound[b64]) - 30), mended=b64)
        kept_whole("inside the journal's prefix", splice(sound, b64, 20))
        kept_whole("inside the mark after the journal's prefix", splice(sound, b64, len(JOURNAL) + 8), mended=b64)
        quoted = list(sound)
        quoted.insert(end + 1, b"[  340.000000] phone-heartbeat: up 340.00 s, MemAvailable: 1690748 kB, last: " + sound[state][:80])
        quoted.insert(b64 + 1, HEARTBEAT)
        kept_whole("the heartbeat quoting a file's line", quoted)

        bad = list(sound)
        bad[state] = bad[state].replace(b'"audio.state"', b'"audio.stbte"')
        refused("a line damaged otherwise", bad, "cts-device.json",
                [f"vm5/console.log: line {state + 1}: cts-device.json line ", " damaged", "cts-device.json: not kept"])
        cut = splice(sound, state, text_at)
        del cut[state + 1]
        refused("a kernel line inside a line whose rest is gone", cut, "cts-device.json",
                [f"vm5/console.log: line {state + 1}: cts-device.json line ", " damaged"])
        n = sound[state].split(b" ")[2].decode()
        gone = sound[:state] + sound[state + 1:]
        refused("a line missing", gone, "cts-device.json", [f"cts-device.json: not kept, line {n} of ", " missing"])
        b64_gone = sound[:b64] + sound[b64 + 1:]
        refused("a PNG's line missing (the old extraction decoded the rest)", b64_gone, "27-music.png",
                ["27-music.png: not kept, line 12 of ", " missing"])
        short = sound[:end] + sound[end + 1:]
        refused("a file without its end", short, "cts-device.json", ["cts-device.json: no end after its line ", "cut short"])
        more = list(sound)
        more[end] = more[end].replace(b"cts-device.json ", b"cts-device.json 9", 1)
        refused("an end with another count", more, "cts-device.json", ["cts-device.json: not kept, line ", " missing"])
        rc, out, kept = files_mode(sound)
        check("the machine named in what is kept", "vm5/console.log: 4 files kept" in out, out)

        # The selftest's requests to the host (qemu-buttons.py's): line n
        # request n, said again until answered, without an end; one cut by
        # a kernel line, one damaged (qemu-buttons.py's to judge).
        ask = [JOURNAL + line for line in m.hand("press-0a1b2c3d.ask", b"PHONE-SELFTEST-PRESS: power 1\nPHONE-SELFTEST-PRESS: power 2 x5 120ms\n")]
        asked = sound[:state] + [ask[0], ask[0]] + sound[state:end] + splice([ask[1]], 0, len(JOURNAL) + 40) + sound[end:]
        asked.insert(asked.index(ask[0]) + 2, ask[1].replace(b"x5", b"x6"))
        kept_whole("requests among the files, cut, damaged, said again", asked)
        rc, out, kept = files_mode(asked)
        check("requests: no file of theirs named", ".ask" not in out, out)
        rc, text, err = text_mode(asked)
        check("text: a request's text, cut by a kernel line, whole", rc == 0 and
              "PHONE-SELFTEST-PRESS: power 2 x5 120ms" in text and "PHONE-SELFTEST-PRESS: power 1" in text and err == "",
              "\n".join(text) + err)

        # The update tests: ota-test's lines as it says them, one more each
        # time (a say given two lines prints both), a power cut before the
        # end, the next boot starting the file anew.
        said, ota = b"", []
        for s in [b"PHONE-OTA: booted=a primary=a good=a tries=0 root=/dev/mapper/root_a:ro health=none",
                  b"PHONE-OTA: FAIL: the untrusted bundle was installed\nPHONE-OTA:   line 3: wait",
                  b"PHONE-OTA: installed"]:
            n = said.count(b"\n") + 1
            said += s + b"\n"
            ota += m.hand("ota-test.txt", said, str(n))
        ota += m.hand("ota-test.txt", said, "end")
        boot2 = m.hand("ota-test.txt", b"PHONE-OTA: power cut after 64 MB written to slot b\n", "1")
        base = [b"=== boot: phone.ota-test=install"] + BOOT + ota + [b"=== boot: phone.ota-test=install-cut"] + boot2
        fail = find(base, b"FAIL: the untrusted")
        rc, text, err = text_mode(splice(base, fail, base[fail].index(b"untrusted") + 3, (HEARTBEAT, SYSRQ)))
        check("text: a kernel line inside a FAIL line taken out, the line whole", rc == 0 and
              "PHONE-OTA: FAIL: the untrusted bundle was installed" in text and HEARTBEAT.decode() in text and err == "",
              "\n".join(text) + err)
        check("text: a line said with two in it", "PHONE-OTA:   line 3: wait" in text, "\n".join(text))
        check("text: the next boot's file after a power cut", "PHONE-OTA: power cut after 64 MB written to slot b" in text, "\n".join(text))
        check("text: the console's other lines, without terminal controls",
              "[   25.322830] selftest-boot.sh[439]: PHONE-MODULES: PASS: lockdown integrity" in text, "\n".join(text))
        hurt = list(base)
        hurt[fail] = hurt[fail].replace(b"untrusted", b"untrustel")
        rc, text, err = text_mode(hurt)
        check("text: a damaged line left out, named, exit status 1",
              rc == 1 and f"vm5/console.log: line {fail + 1}: ota-test.txt line 2 damaged" in err and
              not [t for t in text if "untrustel" in t and t.startswith("PHONE-OTA")], err)
        rc, text, err = text_mode(base[:fail] + base[fail + 1:])
        check("text: a line missing named, exit status 1", rc == 1 and "ota-test.txt: line 2 of 4 missing before its end" in err, err)

        def report_mode(lines, name):
            with open(log, "wb") as f:
                f.write(console(lines))
            p = subprocess.run([sys.executable, TOOL, "report", log, name], capture_output=True)
            return p.returncode, p.stdout.decode("utf-8", "replace").split("\n")

        def reported(what, lines, name, want, rc_want=0, says=()):
            rc, out = report_mode(lines, name)
            check(f"report: {what}: exit status {rc_want}, the report's lines whole",
                  rc == rc_want and all(w in out for w in want), "\n".join(out))
            for s in says:
                check(f"report: {what}: says {s!r}", any(s in o for o in out), "\n".join(out))

        # The soak's and the stress run's reports, handed whole at the end
        # (soak-boot.sh, stress-boot.sh).
        soak_lines = [
            "PHONE-SOAK-BEGIN",
            "rounds: 3 in 1830 s; script exit code 0; service restarts 0",
            "phone-shell 412: rss 41200 -> 43100 kB (growth +1900, limit 49152), fds 31 -> 31 (growth +0, limit 16) ok",
            "PHONE-SOAK-END",
            "PHONE-SOAK-RESULT: PASS",
        ]
        down = b"[ 1903.220101] reboot: Power down"
        soak = BOOT + [LOGIN + b"phone-soak starting"] + m.hand("soak-report.txt", "".join(s + "\n" for s in soak_lines).encode()) + [down]
        result = find(soak, b"PHONE-SOAK-RESULT")
        reported("soak, nothing inside", soak, "PHONE-SOAK", soak_lines)
        reported("soak, a kernel line inside its result (the old grep found no PASS)",
                 splice(soak, result, soak[result].index(b"PASS") + 2), "PHONE-SOAK", soak_lines,
                 says=[f"line {result + 1}: soak-report.txt line 5 mended"])
        begin = find(soak, b"PHONE-SOAK-BEGIN")
        reported("soak, a kernel line inside its begin (the old sed printed nothing)",
                 splice(soak, begin, soak[begin].index(b"BEGIN") + 1, (HEARTBEAT, SYSRQ)), "PHONE-SOAK", soak_lines)
        growth = find(soak, b"growth +1900")
        reported("soak, a kernel line inside a number (the old report showed it cut)",
                 splice(soak, growth, soak[growth].index(b"+1900") + 3), "PHONE-SOAK", soak_lines)
        failed = [s.replace("PASS", "FAIL") for s in soak_lines]
        reported("soak failed", BOOT + m.hand("soak-report.txt", "".join(s + "\n" for s in failed).encode()), "PHONE-SOAK",
                 failed, 1)
        bad = list(soak)
        bad[growth] = bad[growth].replace(b"+1900", b"+1800")
        reported("soak, a line damaged otherwise", bad, "PHONE-SOAK", soak_lines[:2], 1,
                 [f"line {growth + 1}: soak-report.txt line 3 damaged"])
        reported("soak, a line missing", soak[:growth] + soak[growth + 1:], "PHONE-SOAK", soak_lines[:2], 1,
                 ["soak-report.txt: line 3 of 5 missing before its end"])
        old = BOOT + [LOGIN + b"phone-soak: " + s.encode() for s in soak_lines[:1]] + [b"phone-soak: " + s.encode() for s in soak_lines[1:]]
        reported("soak, an older image's plain lines", old, "PHONE-SOAK", ["phone-soak: " + s for s in soak_lines[1:]])
        reported("soak, no report", BOOT + [down], "PHONE-SOAK", [], 1, ["no PHONE-SOAK-BEGIN"])
        stress_lines = ["PHONE-STRESS-BEGIN", "rounds: 2 in 980 s; script exit code 0", "renderer restarts: 0",
                        "PHONE-STRESS-END", "PHONE-STRESS-RESULT: PASS"]
        stress = BOOT + m.hand("stress-report.txt", "".join(s + "\n" for s in stress_lines).encode())
        result = find(stress, b"PHONE-STRESS-RESULT")
        reported("stress, a kernel line inside its result", splice(stress, result, -2), "PHONE-STRESS", stress_lines)

        # The bench's report, printed as it grows, then its end
        # (bench-boot.sh); no result line: its end is the bench done.
        bench_lines = [
            "PHONE-BENCH-BEGIN",
            '{"name": "phone-shell", "runs": 3, "start_ms": [412.5, 398.1, 401.7], "rss_kb": [38120, 38004, 38200]}',
            "PHONE-BENCH-DISK semantic_kb=48213",
            "PHONE-BENCH-DISK wpe_kb=0",
            "PHONE-BENCH-END",
        ]
        data = "".join(s + "\n" for s in bench_lines).encode()
        bench = BOOT + m.hand("bench-report.txt", data, "1") + m.hand("bench-report.txt", data, "end") + [down]
        number = find(bench, b"398.1")
        reported("bench, a kernel line inside a number (the old report showed it cut)",
                 splice(bench, number, bench[number].index(b"398.1") + 2), "PHONE-BENCH", bench_lines)
        end = find(bench, b"PHONE-BENCH-END")
        reported("bench, a kernel line inside its end (the old grep found none)",
                 splice(bench, end, bench[end].index(b"END") + 1), "PHONE-BENCH", bench_lines)
        reported("bench, cut off before its end", bench[:number + 1], "PHONE-BENCH", bench_lines[:2], 1)
        old = BOOT + [JOURNAL + s.encode() for s in bench_lines]
        reported("bench, an older image's plain lines", old, "PHONE-BENCH", [JOURNAL.decode() + s for s in bench_lines])

        # The cloud test's lines, a console file growing as it says them, its
        # end before the power-off (cloud-test); the host checks its PASS, no
        # FAIL and the two services not started (qemu-cloud.sh).
        said, cloud = b"", []
        for s in [b"PHONE-CLOUD: store=http://10.0.2.2:8893 push=https://10.0.2.2:8890 update=http://10.0.2.2:8894",
                  b"PHONE-CLOUD: phone-pushgw: not started\nPHONE-CLOUD: phone-storesrv: not started",
                  b"PHONE-CLOUD: script: PASS", b"PHONE-CLOUD: pushd: connected to the gateway"]:
            n = said.count(b"\n") + 1
            said += s + b"\n"
            cloud += m.hand("cloud-test.txt", said, str(n))
        cloud = BOOT + cloud + m.hand("cloud-test.txt", said, "end") + [down]
        cut = splice(cloud, find(cloud, b"phone-storesrv: not"), -10)
        cut = splice(cut, find(cut, b"script: PASS"), -2, (HEARTBEAT, SYSRQ))
        rc, text, err = text_mode(cut)
        check("text: the cloud test's PASS and a service not started, kernel lines inside, whole", rc == 0 and
              "PHONE-CLOUD: script: PASS" in text and "PHONE-CLOUD: phone-storesrv: not started" in text and
              len([t for t in text if t.endswith(": not started")]) == 2 and err == "", "\n".join(text) + err)

        # The charger test's results, a console file growing as it says them,
        # without an end (charger-test.sh), among its requests through the
        # journal; the host takes the last result line (qemu-charger.sh).
        said, charger = b"", []
        for s in [b"PHONE-CHARGER-TEST: screen: PASS", b"PHONE-CHARGER-TEST: no-start: PASS", b"PHONE-CHARGER-TEST-RESULT: PASS",
                  b"PHONE-CHARGER-TEST: unplugging the chargers: the phone powers off"]:
            said += s + b"\n"
            charger += m.hand("charger-test.txt", said, str(said.count(b"\n")))
        charger = BOOT + charger[:1] + [JOURNAL + line for line in m.hand("charger-0a1b2c3d.ask", b"PHONE-SELFTEST-PRESS: power 1\n", "1")] + charger[1:]
        charger.append(b"[   80.551200] reboot: Power down")
        result = find(charger, b"PHONE-CHARGER-TEST-RESULT")
        rc, text, err = text_mode(splice(charger, result, charger[result].index(b"RESULT") + 3))
        check("text: the charger test's result, a kernel line inside, whole", rc == 0 and
              "PHONE-CHARGER-TEST-RESULT: PASS" in text and "PHONE-SELFTEST-PRESS: power 1" in text and
              "[   80.551200] reboot: Power down" in text and err == "", "\n".join(text) + err)
        hurt = list(charger)
        hurt[result] = hurt[result].replace(b"PASS", b"PAST")
        rc, text, err = text_mode(hurt)
        check("text: the charger test's result damaged, named, exit status 1",
              rc == 1 and "charger-test.txt line 3 damaged" in err and "PHONE-CHARGER-TEST-RESULT: PAST" not in text, err)

        # Every byte but the newline (and the carriage return and escape,
        # which the console's own get taken out with), an empty line, a
        # last line without its newline.
        every = bytes(range(1, 256)).replace(b"\n", b"")
        lines = m.hand("x.txt", b"\n" + every + b"\nlast")
        rc, out, kept = files_mode(lines)
        check("console-file: every byte, an empty line, no last newline",
              rc == 0 and lines[0] == b"PHONE-FILE x.txt 1 00000001 " and
              kept.get("x.txt") == b"\n" + every.replace(b"\r", b"").replace(b"\x1b", b"") + b"\nlast\n", out)

    for f in failures:
        print("FAIL:", f)
    print(f"qemu-console-test: {'FAIL' if failures else 'PASS'} (console-file under {m.awk})")
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())
