#!/usr/bin/env python3
"""Whether every service an image starts runs as a user it can run as and
the system bus takes.

    phone-svcusers-check.py ROOTFS

The services: those its units want, require or uphold (the .wants,
.requires and .upholds directories), those its sockets, timers and paths
of these start, and those D-Bus starts on demand (SystemdService= of
share/dbus-1/system-services), each with its drop-ins as systemd reads
them. A User= or Group= must be in the image's /etc/passwd or /etc/group,
unless it is a specifier or a number.

No DynamicUser= while the system bus is dbus-daemon (dbus.service): systemd
starts that with SYSTEMD_NSS_DYNAMIC_BYPASS=1, so that the bus and PID 1
never wait on each other, and nss-systemd in the bus then leaves out
PID 1's dynamic users; systemd 259 reads its direct: symlinks for them
nowhere either. dbus-daemon looks up the groups of every peer's UID before
it takes the connection (bus_policy_allow_unix_user) and closes it when
the lookup fails, which libdbus reports as "Did not receive a reply":
q6voiced on the OnePlus 6T (39-oneplus-6t.md §14.20). dbus-broker reads
the peer's groups from the socket instead and takes such services.

Exit status 1 when a service breaks one of these.
"""
import glob
import os
import sys

UNIT_DIRS = ("/etc/systemd/system", "/usr/lib/systemd/system", "/lib/systemd/system")
BOOLEAN_TRUE = ("1", "yes", "y", "true", "t", "on")


def real(root, path):
    """The path an absolute path of the image names, symlinks followed
    inside the image (absolute ones too); None for a loop."""
    parts = [p for p in path.split("/") if p]
    out = []
    hops = 0
    while parts:
        name = parts.pop(0)
        if name == ".":
            continue
        if name == "..":
            if out:
                out.pop()
            continue
        full = os.path.join(root, *out, name)
        if os.path.islink(full):
            hops += 1
            if hops > 40:
                return None
            target = os.readlink(full)
            if target.startswith("/"):
                out = []
            parts = [p for p in target.split("/") if p] + parts
            continue
        out.append(name)
    return "/" + "/".join(out)


def unit_lines(path):
    """(section, key, value) of a unit file or drop-in, in order."""
    with open(path, encoding="utf-8", errors="replace") as f:
        text = f.read()
    out, section, cont = [], "", ""
    for raw in text.splitlines():
        s = raw.strip()
        if not cont and (not s or s[0] in "#;"):
            continue
        if s.endswith("\\"):
            cont += s[:-1] + " "
            continue
        s, cont = cont + s, ""
        if s.startswith("[") and s.endswith("]"):
            section = s[1:-1]
        elif "=" in s:
            k, v = s.split("=", 1)
            out.append((section, k.strip(), v.strip()))
    return out


class Image:
    def __init__(self, root):
        self.root = root

    def file(self, path):
        """The host path of the image's file at path, None when it is not
        a regular file there; "masked" for /dev/null."""
        p = real(self.root, path)
        if p == "/dev/null":
            return "masked"
        if p is None or not os.path.isfile(os.path.join(self.root, p.lstrip("/"))):
            return None
        return os.path.join(self.root, p.lstrip("/"))

    def unit_file(self, name):
        for d in UNIT_DIRS:
            f = self.file(d + "/" + name)
            if f:
                return f
        if "@" in name and not name.split("@", 1)[1].startswith("."):
            return self.unit_file(name.split("@", 1)[0] + "@." + name.rsplit(".", 1)[1])
        return None

    def dropins(self, name):
        """The drop-ins of the unit, in the order systemd applies them: by
        file name, a name in /etc taking the place of the same in /usr/lib."""
        base, kind = name.rsplit(".", 1)
        dirs = [kind + ".d"]
        stem = base.split("@", 1)[0]
        pieces = stem.split("-")
        dirs += ["-".join(pieces[:i]) + "-." + kind + ".d" for i in range(1, len(pieces))]
        if "@" in base:
            dirs.append(stem + "@." + kind + ".d")
        dirs.append(name + ".d")
        found = {}
        for d in reversed(UNIT_DIRS):
            for sub in dirs:
                for f in sorted(glob.glob(os.path.join(self.root, d.lstrip("/"), sub, "*.conf"))):
                    host = self.file(d + "/" + sub + "/" + os.path.basename(f))
                    if host and host != "masked":
                        found[os.path.basename(f)] = host
                    elif host == "masked":
                        found.pop(os.path.basename(f), None)
        return [found[k] for k in sorted(found)]

    def settings(self, name):
        """The unit's assignments with its drop-ins, None when it has no
        file or is masked."""
        f = self.unit_file(name)
        if not f or f == "masked":
            return None
        lines = unit_lines(f)
        for d in self.dropins(name):
            lines += unit_lines(d)
        return lines

    def started(self):
        names = set()
        for d in UNIT_DIRS:
            for kind in ("wants", "requires", "upholds"):
                for w in glob.glob(os.path.join(self.root, d.lstrip("/"), "*." + kind)):
                    if os.path.isdir(w):
                        names.update(os.listdir(w))
        for f in glob.glob(os.path.join(self.root, "usr/share/dbus-1/system-services/*.service")):
            for section, k, v in unit_lines(f):
                if section == "D-BUS Service" and k == "SystemdService" and v:
                    names.add(v)
        services = set(n for n in names if n.endswith(".service"))
        for n in names:
            base, _, kind = n.rpartition(".")
            if kind not in ("socket", "timer", "path"):
                continue
            lines = self.settings(n) or []
            key = "Service" if kind == "socket" else "Unit"
            target = [v for s, k, v in lines if k == key and v]
            accept = [v.lower() in BOOLEAN_TRUE for s, k, v in lines if k == "Accept"]
            if target:
                services.add(target[-1])
            elif accept and accept[-1]:
                services.add(base.split("@", 1)[0] + "@.service")
            else:
                services.add(base + ".service")
        return sorted(s for s in services if s.endswith(".service"))

    def names(self, table):
        f = self.file("/etc/" + table)
        if not f or f == "masked":
            return set()
        with open(f, encoding="utf-8", errors="replace") as fp:
            return {l.split(":", 1)[0] for l in fp if ":" in l}

    def bus_is_dbus_daemon(self):
        lines = self.settings("dbus.service") or []
        start = [v for s, k, v in lines if s == "Service" and k == "ExecStart" and v]
        return bool(start) and os.path.basename(start[-1].lstrip("@-:+!").split()[0]) == "dbus-daemon"


def effective(lines, key, default=""):
    v = default
    for s, k, val in lines:
        if s == "Service" and k == key:
            v = val if val else default
    return v


def check(root):
    img = Image(root)
    users, groups = img.names("passwd"), img.names("group")
    dbus_daemon = img.bus_is_dbus_daemon()
    problems = []
    for name in img.started():
        lines = img.settings(name)
        if lines is None:
            continue
        if effective(lines, "DynamicUser", "no").lower() in BOOLEAN_TRUE:
            # A static user of the name (the unit's without User=) is used as it is.
            if dbus_daemon and (effective(lines, "User") or name.rsplit(".", 1)[0]) not in users:
                problems.append(f"{name}: DynamicUser= with dbus-daemon as the system bus: the bus closes its "
                                "connection; give it a system user of its own (DynamicUser=no, User=)")
            continue
        for key, known, table in (("User", users, "passwd"), ("Group", groups, "group")):
            v = effective(lines, key)
            if v and "%" not in v and not v.isdigit() and v not in known:
                problems.append(f"{name}: {key}={v} is not in the image's /etc/{table}")
    return problems


def main():
    if len(sys.argv) != 2:
        print(__doc__.strip().splitlines()[2].strip(), file=sys.stderr)
        return 2
    problems = check(sys.argv[1])
    for p in problems:
        print(p)
    if not problems:
        print("every service runs as a user it can run as and the system bus takes")
    return 1 if problems else 0


if __name__ == "__main__":
    sys.exit(main())
