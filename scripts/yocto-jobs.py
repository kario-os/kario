#!/usr/bin/env python3
"""How many bitbake tasks the image build runs at once, and make jobs in
each (BB_NUMBER_THREADS, PARALLEL_MAKE; the Makefile's YOCTO_JOBS), unless
whoever runs make sets them: one per processor this process may run on, at
most one per 2 GiB of memory. The memory is the machine's or its control
group's limit, whichever is less (a user slice's MemoryMax): kas-container
under rootless podman builds inside that group.

  yocto-jobs.py            the number
  yocto-jobs.py --explain  the number and what it comes from
"""
import os
import sys

GIB = 1 << 30
PER_JOB = 2 * GIB


def processors():
    try:
        return len(os.sched_getaffinity(0))
    except AttributeError:
        return os.cpu_count() or 1


def mem_total(root="/"):
    with open(os.path.join(root, "proc", "meminfo")) as f:
        for line in f:
            if line.startswith("MemTotal:"):
                return int(line.split()[1]) * 1024
    return None


def cgroup_limit(root="/"):
    """The least memory limit of this process's control group and the groups
    above it (cgroup v2's memory.max, v1's memory.limit_in_bytes), or None."""
    try:
        with open(os.path.join(root, "proc", "self", "cgroup")) as f:
            lines = f.read().splitlines()
    except OSError:
        return None
    limits = []
    for line in lines:
        parts = line.split(":", 2)
        if len(parts) != 3:
            continue
        if parts[1] == "":
            base, name = os.path.join("sys", "fs", "cgroup"), "memory.max"
        elif "memory" in parts[1].split(","):
            base, name = os.path.join("sys", "fs", "cgroup", "memory"), "memory.limit_in_bytes"
        else:
            continue
        group = parts[2].strip("/")
        while True:
            try:
                with open(os.path.join(root, base, group, name)) as f:
                    value = f.read().strip()
                if value != "max":
                    limits.append(int(value))
            except (OSError, ValueError):
                pass
            if not group:
                break
            group = os.path.dirname(group)
    return min(limits) if limits else None


def jobs(cpus, memory):
    return max(1, min(cpus, memory // PER_JOB))


def gib(n):
    return f"{n / GIB:.1f}".rstrip("0").rstrip(".") + " GiB"


def explain(root="/"):
    cpus, total, limit = processors(), mem_total(root), cgroup_limit(root)
    known = [m for m in (total, limit) if m]
    memory = min(known) if known else PER_JOB
    n = jobs(cpus, memory)
    where = f"the control group's limit; the machine has {gib(total)}" if total and memory < total else "the machine's"
    return n, f"{n} from {cpus} processors and {gib(memory)} of memory ({where}): one job per processor, at most one per 2 GiB"


def main():
    n, why = explain()
    print(why if sys.argv[1:] == ["--explain"] else n)


if __name__ == "__main__":
    main()
