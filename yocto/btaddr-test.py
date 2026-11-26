#!/usr/bin/env python3
"""Test phone-btaddr (meta-phone's phone-qcom) against a fake kernel, without a phone.

Builds the tool with the machine's C compiler and hands it one end of a
socket pair as its Bluetooth management socket (--mgmt-fd); the test answers
on the other end as the kernel's management interface does. On the OnePlus
6T (39-oneplus-6t.md §16, report 006) hci0 appeared while WCN3990's
firmware was still loading, the kernel answered the read of its
configuration with "invalid index" (17) until it had set the controller up,
phone-btaddr gave up at once and the controller never got its address.
Checked: it waits through the set-up, woken by the kernel's announcement of
the controller or retrying every second where none comes, then sets the
address made from the serial number (least significant octet first); a
controller with an address of its own is left alone; one never set up fails
after --wait; any other refusal fails at once; --print.

    btaddr-test.py [cc]
"""
import os
import shutil
import socket
import struct
import subprocess
import sys
import tempfile
import threading
import time

HERE = os.path.dirname(os.path.abspath(__file__))
SRC = os.path.join(HERE, "meta-phone", "recipes-bsp", "phone-qcom", "files", "btaddr.c")
READ_CONFIG_INFO, SET_PUBLIC_ADDRESS, READ_EXT_INDEX_LIST = 0x0037, 0x0039, 0x003C
CMD_COMPLETE, CMD_STATUS, EXT_INDEX_ADDED = 0x0001, 0x0002, 0x0020
INVALID_INDEX, NOT_SUPPORTED = 0x11, 0x0C
INDEX_NONE = 0xFFFF
OPTION_PUBLIC_ADDRESS = 0x2
SERIAL = "kariotest0001"


def event(code, index, params):
    return struct.pack("<HHH", code, index, len(params)) + params


def address(serial):
    """The address phone-btaddr makes, as printed (most significant first)."""
    h = 0xCBF29CE484222325
    for b in b"kario-bt-addr\n" + serial.encode():
        h = ((h ^ b) * 0x100000001B3) & 0xFFFFFFFFFFFFFFFF
    a = [(h >> (8 * i)) & 0xFF for i in range(6)]
    a[0] = (a[0] & 0xFE) | 0x02
    return a


class Kernel:
    """The management interface as the test plays it: hci0 is still being
    set up for the first `setup` reads of its configuration (always, with
    never), the kernel announcing it 0.3 s after the last of them (with
    announce); then it lacks the options in missing, or the read is refused
    with refuse."""

    def __init__(self, setup=0, announce=True, never=False, missing=OPTION_PUBLIC_ADDRESS, refuse=0):
        self.setup, self.announce, self.never, self.missing, self.refuse = setup, announce, never, missing, refuse
        self.reads, self.ops, self.set = 0, [], None

    def serve(self, sock):
        while True:
            try:
                data = sock.recv(512)
            except OSError:
                return
            if not data:
                return
            op, index, plen = struct.unpack_from("<HHH", data)
            self.ops.append(op)
            if op == READ_EXT_INDEX_LIST:
                sock.send(event(CMD_COMPLETE, INDEX_NONE, struct.pack("<HBH", op, 0, 0)))
            elif op == READ_CONFIG_INFO and self.refuse:
                sock.send(event(CMD_STATUS, index, struct.pack("<HB", op, self.refuse)))
            elif op == READ_CONFIG_INFO and (self.never or self.reads < self.setup):
                self.reads += 1
                sock.send(event(CMD_STATUS, index, struct.pack("<HB", op, INVALID_INDEX)))
                if self.announce and self.reads == self.setup:
                    t = threading.Timer(0.3, lambda: sock.send(event(EXT_INDEX_ADDED, index, bytes([1, 0]))))
                    t.daemon = True
                    t.start()
            elif op == READ_CONFIG_INFO:
                sock.send(event(CMD_COMPLETE, index, struct.pack("<HBHII", op, 0, 0x001D, OPTION_PUBLIC_ADDRESS, self.missing)))
            elif op == SET_PUBLIC_ADDRESS:
                self.set = data[6:6 + plen]
                sock.send(event(CMD_COMPLETE, index, struct.pack("<HBI", op, 0, 0)))


def main():
    cc = sys.argv[1] if len(sys.argv) > 1 else os.environ.get("CC", "cc")
    tmp = tempfile.mkdtemp(prefix="btaddr-test-")
    tool = os.path.join(tmp, "phone-btaddr")
    subprocess.run([cc, "-std=c11", "-O2", "-Wall", "-Wextra", "-Werror", "-o", tool, SRC], check=True)
    cmdline = os.path.join(tmp, "cmdline")
    with open(cmdline, "w") as f:
        f.write(f"root=PARTLABEL=userdata androidboot.serialno={SERIAL} androidboot.baseband=msm\n")
    want = address(SERIAL)
    text = ":".join(f"{b:02X}" for b in want)
    failed = []

    def check(ok, what):
        print(("ok   " if ok else "FAIL ") + what)
        if not ok:
            failed.append(what)

    def run(kernel, *args):
        mine, theirs = socket.socketpair(socket.AF_UNIX, socket.SOCK_SEQPACKET)
        t = threading.Thread(target=kernel.serve, args=(mine,), daemon=True)
        t.start()
        t0 = time.monotonic()
        try:
            r = subprocess.run([tool, "--mgmt-fd", str(theirs.fileno()), "--cmdline", cmdline, *args],
                               pass_fds=(theirs.fileno(),), capture_output=True, text=True, timeout=30)
        finally:
            theirs.close()
            t.join(5)
            mine.close()
        return r.returncode, r.stdout + r.stderr, time.monotonic() - t0

    k = Kernel(setup=1)
    rc, out, took = run(k, "hci0")
    check(rc == 0 and k.set == bytes(reversed(want)) and f"hci0 has {text}" in out,
          f"in set-up once, then announced: the address set ({rc}, {out.strip()!r})")
    check(took < 0.9 and k.ops.count(READ_CONFIG_INFO) == 2, f"woken by the announcement: {took:.2f} s, {k.ops.count(READ_CONFIG_INFO)} reads")
    check(k.ops[0] == READ_EXT_INDEX_LIST, "the index list read first (it subscribes the socket to the announcements)")

    k = Kernel(setup=2, announce=False)
    rc, out, took = run(k, "hci0")
    check(rc == 0 and k.set == bytes(reversed(want)) and 1.8 < took < 4,
          f"no announcement: read again every second, then set ({rc}, {took:.2f} s, {out.strip()!r})")

    k = Kernel(missing=0)
    rc, out, took = run(k, "hci0")
    check(rc == 0 and k.set is None and "has its address" in out, f"a controller with its own address is left alone ({out.strip()!r})")

    k = Kernel(never=True)
    rc, out, took = run(k, "--wait", "2", "hci0")
    check(rc == 1 and k.set is None and "reading its configuration failed (17)" in out and 1.8 < took < 4,
          f"never set up: fails after --wait ({rc}, {took:.2f} s, {out.strip()!r})")

    k = Kernel(refuse=NOT_SUPPORTED)
    rc, out, took = run(k, "hci0")
    check(rc == 1 and k.ops.count(READ_CONFIG_INFO) == 1 and took < 0.9, f"another refusal fails at once ({rc}, {took:.2f} s)")

    r = subprocess.run([tool, "--print", "--cmdline", cmdline], capture_output=True, text=True)
    check(r.returncode == 0 and r.stdout == text + "\n", f"--print: {r.stdout.strip()!r}")
    r = subprocess.run([tool, "--frob"], capture_output=True, text=True)
    check(r.returncode == 2 and "usage" in r.stderr, "an unknown option is refused")
    shutil.rmtree(tmp)
    print("btaddr-test: " + (f"{len(failed)} failed" if failed else "all passed"))
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
