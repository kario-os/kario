#!/usr/bin/env python3
"""Test yocto/selftest-plan.py on made-up scenarios: every part on exactly
one machine, the heaviest first to the least loaded, the fd store check
packed among them; weights from the last run's reports, else the runs'
before it, a case none has at their pace; as many machines as parts,
processors and memory allow, or as PHONE_SELFTEST_VMS says, and of those
the fewest that end as soon (one running several parts with some slack),
all of them where the parts must share more; one machine is the whole
scenario.

    selftest-plan-test.py
"""
import importlib.util
import json
import os
import subprocess
import sys
import tempfile

HERE = os.path.dirname(os.path.abspath(__file__))
SCRIPT = os.path.join(HERE, "selftest-plan.py")
_spec = importlib.util.spec_from_file_location("selftest_plan", SCRIPT)
plan = importlib.util.module_from_spec(_spec)
_spec.loader.exec_module(plan)

SELFTEST = """# the boot
sleep 800
snap 00-boot

#part a
== a1
key enter
key f4
#part b
== b1
key enter
#part a
== a2
key enter
key enter
key f4
#part c
== c1
key f4
"""
CTS = "#part cts\n== x1\nkey f4\n"


def run(d, *args, env=None):
    p = subprocess.run([sys.executable, SCRIPT, os.path.join(d, "selftest.script"), os.path.join(d, "cts.script"), os.path.join(d, "runs"), *args],
                       capture_output=True, text=True, env={**os.environ, **(env or {})})
    return p.returncode, p.stdout, p.stderr


def main():
    failures = []

    def check(what, cond, out=""):
        if not cond:
            failures.append(f"{what}\n{out}")

    with tempfile.TemporaryDirectory() as d:
        for name, text in (("selftest.script", SELFTEST), ("cts.script", CTS)):
            with open(os.path.join(d, name), "w", encoding="utf-8") as f:
                f.write(text)
        order, cases = plan.parts_of([os.path.join(d, "selftest.script"), os.path.join(d, "cts.script")])
        check("parts in their order", order == ["a", "b", "c", "cts"], order)
        check("a case's part and lines", cases["a2"] == ["a", 3] and cases["x1"] == ["cts", 1], cases)

        w = plan.weights(order, cases, {})
        check("weights by lines", abs(w["a"] - 5 * plan.PACE) < 1e-9 and abs(w["cts"] - plan.PACE) < 1e-9, w)
        os.makedirs(os.path.join(d, "runs", "vm1"))
        with open(os.path.join(d, "runs", "vm1", "cts-device.json"), "w") as f:
            json.dump({"cases": [{"name": "a1", "status": "PASS", "seconds": 40}, {"name": "b1", "status": "PASS", "seconds": 300},
                                 {"name": "c1", "status": "SKIP", "seconds": 0, "message": "not selected"},
                                 {"name": "x1", "status": "SKIP", "seconds": 0, "message": "needs webview"}]}, f)
        took = plan.last_seconds(os.path.join(d, "runs"))
        check("the last run's seconds, cases left out by ONLY= left out", took == {"a1": 40.0, "b1": 300.0, "x1": 0.0}, took)
        w = plan.weights(order, cases, took)
        # 340 s over the 4 lines of a1, b1 and x1: a2's 3 lines and c1's 1 at that pace.
        check("a case the last run lacks at its pace", abs(w["a"] - (40 + 3 * 340 / 4)) < 1e-6 and abs(w["c"] - 340 / 4) < 1e-6 and w["cts"] == 0, w)
        took = plan.known_seconds(os.path.join(d, "runs"))
        check("the runs' seconds: the last run's", took == {"a1": 40.0, "b1": 300.0, "x1": 0.0}, took)
        with open(os.path.join(d, "runs", "vm1", "cts-device.json"), "w") as f:
            json.dump({"cases": [{"name": "a1", "status": "PASS", "seconds": 50}, {"name": "b1", "status": "SKIP", "seconds": 0, "message": "not selected"}]}, f)
        took = plan.known_seconds(os.path.join(d, "runs"))
        check("after a run of a few cases the others' as the run before had them", took == {"a1": 50.0, "b1": 300.0, "x1": 0.0}, took)

        bins, fd, load = plan.plan(order, {"a": 400, "b": 300, "c": 200, "cts": 100}, 2)
        check("heaviest first to the least loaded", bins == [["a", "cts"], ["b", "c"]], bins)
        check("the fd store check on the lightest", fd == 0 and load == [500 + plan.FDSTORE_S, 500], (fd, load))
        bins, fd, _ = plan.plan(order, {"a": 400, "b": 300, "c": 200, "cts": 100}, 4)
        check("one part a machine", bins == [["a"], ["b"], ["c"], ["cts"]] and fd == 3, (bins, fd))
        bins, fd, load = plan.plan(order, {"a": 100, "b": 70, "c": 30, "cts": 10}, 2)
        check("the fd store check packed among the parts", bins == [["a", "c", "cts"], ["b"]] and fd == 1 and load == [140, 70 + plan.FDSTORE_S], (bins, fd, load))
        bins, fd, load = plan.fewest(order, {"a": 400, "b": 300, "c": 200, "cts": 40}, 4)
        check("light parts share a machine that ends no later", bins == [["a"], ["b"], ["c", "cts"]] and fd == 2 and max(load) == 400, (bins, fd, load))
        bins, _, _ = plan.fewest(order, {"a": 400, "b": 390, "c": 380, "cts": 100}, 4)
        check("a machine each when sharing would end later", len(bins) == 4, bins)
        bins, _, _ = plan.fewest(order, {"a": 400, "b": 300, "c": 300, "cts": 40}, 4)
        check("a shared machine keeps its slack", len(bins) == 4, bins)
        got = plan.fewest(order, {"a": 400, "b": 300, "c": 300, "cts": 40}, 2)
        check("fewer machines than the slack wants: all of them", got is not None and got[0] == [["a", "cts"], ["b", "c"]] and got[1] == 0, got)

        check("machines: the parts", plan.machines(4, 48, 251 << 30) == 4)
        check("machines: the processors", plan.machines(8, 8, 251 << 30) == 2)
        check("machines: the memory", plan.machines(8, 48, 10 << 30) == 3)
        check("machines: at least one", plan.machines(8, 2, 1 << 30) == 1)

        rc, out, err = run(d, "--vms", "3")
        lines = out.splitlines()
        check("three machines", rc == 0 and len(lines) == 3, out + err)
        ran = sorted(p for l in lines for p in l.split()[0].split(","))
        check("every part once", ran == ["a", "b", "c", "cts"], out)
        check("one fd store check", [l.split()[1] for l in lines].count("1") == 1, out)
        rc, out, err = run(d, "--vms", "2")
        lines = out.splitlines()
        check("two machines, the parts sharing them beyond the slack", rc == 0 and len(lines) == 2, out + err)
        check("every part once on two", sorted(p for l in lines for p in l.split()[0].split(",")) == ["a", "b", "c", "cts"], out)
        rc, out, err = run(d, env={"PHONE_SELFTEST_VMS": "1"})
        check("one machine: the whole scenario", rc == 0 and out == "\n" and "whole scenario" in err, repr(out) + err)
        rc, out, err = run(d, env={"PHONE_SELFTEST_VMS": "9"})
        check("no more machines than parts, the fewest that end as soon", rc == 0 and len(out.splitlines()) == 3, out + err)
        rc, out, err = run(d, env={"PHONE_SELFTEST_VMS": "x"})
        check("a bad PHONE_SELFTEST_VMS", rc != 0, out + err)
        p = subprocess.run([sys.executable, SCRIPT, "--machines", os.path.join(d, "selftest.script"), os.path.join(d, "cts.script")],
                           capture_output=True, text=True)
        check("--machines: a number, at most the parts", p.returncode == 0 and p.stdout.strip() in ("1", "2", "3", "4"), p.stdout + p.stderr)
        with open(os.path.join(d, "selftest.script"), "w", encoding="utf-8") as f:
            f.write("== early\nkey f4\n#part a\n== a1\n")
        rc, out, err = run(d, "--vms", "2")
        check("a case before the first part", rc != 0 and "before the first" in err, out + err)

    for f in failures:
        print("FAIL:", f)
    print(f"selftest-plan-test: {'FAIL' if failures else 'PASS'}")
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())
