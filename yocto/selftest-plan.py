#!/usr/bin/env python3
"""Which parts of the boot selftest each emulator of a parallel run boots
with (yocto/qemu-selftest.sh). The scenario's parts are its "#part <name>"
lines (shell/selftest.script, then shell/cts.script); a part's weight is
how long its cases took when they last ran (their seconds in the last
run's compatibility reports, else in the runs' before it, kept beside the
run's directory: a run of a few cases, ONLY=, leaves the others' as they
were), a case without one by its lines at that pace. The parts go heaviest
first to the machine with the least so far, the fd store check among them
as a part of a minute (it runs after the cases).

As many machines as parts, at most one per 3 processors and one per 3 GiB
of memory (each is 4 emulated processors and 2 GiB; under TCG one uses a
processor and a half on average), unless PHONE_SELFTEST_VMS says; 1 is the
whole scenario in one boot, as before the parts. Of those, the fewest that
end as soon as all of them would: light parts share a machine rather than
add an emulator to the load; with fewer machines than that, all of them.

  selftest-plan.py <selftest.script> <cts.script> <last run's dir> [--vms N]

prints a line per machine: its parts, comma-separated, and 1 where the fd
store check runs (0 elsewhere); a single empty line for one machine. What it
decided goes to stderr.

  selftest-plan.py --machines <selftest.script> <cts.script>

prints how many machines this computer takes (the Makefile asks outside
kas-container, whose rootless podman hides the user's memory limit).
"""
import glob
import importlib.util
import json
import os
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
PART = "#part "
CPUS_PER_VM = 3
MEM_PER_VM = 3 << 30
# Seconds per script line where no run says better (2026-11-25: 1936 s of
# cases in 10223 lines).
PACE = 0.19
FDSTORE_S = 60
SHARE_SLACK = 0.2


def load_jobs():
    spec = importlib.util.spec_from_file_location("yocto_jobs", os.path.join(HERE, "..", "scripts", "yocto-jobs.py"))
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


def parts_of(paths):
    """The parts in the order they first appear, and each case's part and
    step lines, by case name."""
    order, cases, part, case = [], {}, None, None
    for path in paths:
        with open(path, encoding="utf-8") as f:
            for line in f:
                t = line.strip()
                if t.startswith(PART):
                    part, case = t[len(PART):].strip(), None
                    if part not in order:
                        order.append(part)
                elif t.startswith("== "):
                    if part is None:
                        sys.exit(f"{path}: a case before the first {PART.strip()} line")
                    case = t[3:]
                    cases[case] = [part, 0]
                elif t and not t.startswith("#") and case:
                    cases[case][1] += 1
    return order, cases


def last_seconds(run_dir):
    """Each case's seconds in the last run's compatibility reports; a case
    skipped for the emulator counts as it took, one left out by ONLY= or
    after a failure before it (shell/headless.go's notSelected and
    previousFailed) as if it had not run."""
    took = {}
    for path in glob.glob(os.path.join(run_dir, "*", "cts-device.json")):
        try:
            with open(path, encoding="utf-8") as f:
                report = json.load(f)
        except (OSError, ValueError):
            continue
        for c in report.get("cases") or []:
            why = c.get("message", "")
            if c.get("status") == "SKIP" and (why == "not selected" or why.startswith("needs the case before it")):
                continue
            took[c["name"]] = float(c.get("seconds", 0))
    return took


def kept_path(run_dir):
    return os.path.normpath(run_dir) + ".seconds.json"


def known_seconds(run_dir):
    """Each case's seconds when it last ran: the last run's (last_seconds),
    else those kept from the runs before it; what it returns is kept for
    the next run."""
    try:
        with open(kept_path(run_dir), encoding="utf-8") as f:
            took = {str(k): float(v) for k, v in json.load(f).items()}
    except (OSError, ValueError, AttributeError, TypeError):
        took = {}
    took.update(last_seconds(run_dir))
    try:
        with open(kept_path(run_dir), "w", encoding="utf-8") as f:
            json.dump(took, f, ensure_ascii=False, sort_keys=True)
    except OSError:
        pass
    return took


def weights(order, cases, took):
    known = [(took[n], c[1]) for n, c in cases.items() if n in took]
    lines = sum(l for _, l in known)
    pace = sum(s for s, _ in known) / lines if lines else PACE
    w = {p: 0.0 for p in order}
    for name, (part, n) in cases.items():
        w[part] += took.get(name, n * pace)
    return w


def machines(n_parts, cpus, memory):
    return max(1, min(n_parts, cpus // CPUS_PER_VM, memory // MEM_PER_VM))


def plan(order, w, n):
    """The parts of each of n machines, in the script's order, and which
    one runs the fd store check."""
    load, bins, fd = [0.0] * n, [[] for _ in range(n)], 0
    items = [(w[p], order.index(p), p) for p in order] + [(FDSTORE_S, len(order), None)]
    for s, _, p in sorted(items, key=lambda t: (-t[0], t[1])):
        i = load.index(min(load))
        if p is None:
            fd = i
        else:
            bins[i].append(p)
        load[i] += s
    return [sorted(b, key=order.index) for b in bins], fd, load


def fewest(order, w, n):
    """The plan on the fewest of n machines that ends as soon as n would:
    a machine more only adds an emulator to the processors' load. A machine
    with more than one part keeps SHARE_SLACK of the longest free: the
    weights are one run's, and on a busy host one case took three times as
    long as in the run before (2026-11-25). Where parts must share the n
    machines beyond that slack, the plan on all n."""
    longest = max(plan(order, w, n)[2])
    for m in range(1, n):
        bins, fd, load = plan(order, w, m)
        if all(l <= (longest if len(b) == 1 else (1 - SHARE_SLACK) * longest) for b, l in zip(bins, load)):
            return bins, fd, load
    return plan(order, w, n)


def main(argv):
    args = argv[1:]
    vms = os.environ.get("PHONE_SELFTEST_VMS", "")
    if "--vms" in args:
        i = args.index("--vms")
        vms = args[i + 1]
        del args[i : i + 2]
    count = "--machines" in args
    if count:
        args.remove("--machines")
    if len(args) != (2 if count else 3):
        sys.exit(__doc__)
    order, cases = parts_of(args[:2])
    jobs = load_jobs()
    cpus = jobs.processors()
    known = [m for m in (jobs.mem_total(), jobs.cgroup_limit()) if m]
    memory = min(known) if known else MEM_PER_VM
    if count:
        print(machines(len(order), cpus, memory))
        return 0
    if vms:
        if not vms.isdigit() or int(vms) < 1:
            sys.exit(f"PHONE_SELFTEST_VMS={vms}: a number of machines, 1 or more")
        n, why = min(int(vms), len(order)), f"PHONE_SELFTEST_VMS={vms}"
    else:
        n = machines(len(order), cpus, memory)
        why = f"{cpus} processors, {jobs.gib(memory)} of memory: at most one per {CPUS_PER_VM} processors and {jobs.gib(MEM_PER_VM)}"
    if n == 1:
        print(f"selftest-plan: 1 machine, the whole scenario ({why})", file=sys.stderr)
        print("")
        return 0
    took = known_seconds(args[2])
    w = weights(order, cases, took)
    bins, fd, load = fewest(order, w, n)
    fewer = f", {len(bins)} of them end as soon" if len(bins) < n else ""
    print(f"selftest-plan: {n} machines for {len(order)} parts ({why}{fewer}); weights from "
          + (f"the runs' {len(took)} cases" if took else f"the lines at {PACE} s each"), file=sys.stderr)
    for i, b in enumerate(bins):
        print(f"selftest-plan:   vm{i + 1}: {','.join(b)}{' + fd store' if i == fd else ''}, about {load[i]:.0f} s", file=sys.stderr)
        print(f"{','.join(b)} {int(i == fd)}")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
