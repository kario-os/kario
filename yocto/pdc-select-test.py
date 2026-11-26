#!/usr/bin/env python3
"""Test pdc-select (meta-phone's phone-qcom-pdc) against a simulated modem,
without a phone: a qmicli stand-in answers as libqmi 1.38's does over QRTR
(the SIM's home network, the software configurations, their activation)
and logs every call.

Checked: the configuration for the SIM's carrier is activated (one for
VoLTE before one without, commercial, OpenMkt, then the newest version;
never a lab or test build, never another carrier's), the script waits for
the modem's restart until the configuration is active (pending is not
enough: the old modem may still be up) and then opens the SIM's session
again; a carrier is dealt with once, so what is active afterwards (chosen
by hand) stays; an active or pending one is kept; a SIM of a carrier the
table does not know, no SIM, a modem without a matching configuration do
nothing; a refused activation fails and is not tried again; an activation
qmicli never sees answered is given up after 60 s (the phone's busybox has
no timeout) and the modem still waited for; --dry-run changes nothing. No call ever loads, deactivates or
deletes a configuration.

    pdc-select-test.py
"""
import json
import os
import shutil
import subprocess
import sys
import tempfile
import time

HERE = os.path.dirname(os.path.abspath(__file__))
FILES = os.path.join(HERE, "meta-phone", "recipes-bsp", "phone-qcom", "files")
SCRIPT = os.path.join(FILES, "pdc-select")
CARRIERS = os.path.join(FILES, "pdc-carriers")

# The qmicli stand-in: FAKE_MODEM is the modem's state (JSON), FAKE_LOG
# gets one line per call. After an activation each list shows the next of
# "after" (pending: the old modem still up; down: restarting), then the
# activated configuration active (pending for good when "applies" is false).
FAKE_QMICLI = r'''#!/usr/bin/env python3
import json, os, sys, time
path = os.environ["FAKE_MODEM"]
with open(path) as f:
    m = json.load(f)
args = sys.argv[1:]
with open(os.environ["FAKE_LOG"], "a") as f:
    f.write("qmicli " + " ".join(args) + "\n")
dev = args[args.index("-d") + 1]
action = [a for a in args if a.startswith("--")][0]

def save():
    with open(path, "w") as f:
        json.dump(m, f)

def show(pending=None):
    cs = m["configs"]
    print(f"Total configurations: {len(cs)}")
    for i, c in enumerate(cs, 1):
        status = c["status"]
        if pending is not None and c["id"] == pending:
            status = "Pending"
        print(f"Configuration {i}:")
        print(f"\tDescription: {c['desc']}")
        print("\tType:        software")
        print(f"\tSize:        {c.get('size', 70000)}")
        print(f"\tStatus:      {status}")
        print(f"\tVersion:     {c['version']}")
        print(f"\tID:          {c['id']}\n")

if action == "--nas-get-home-network":
    if m.get("home_fail", 0) > 0 or m.get("home") is None:
        m["home_fail"] = m.get("home_fail", 0) - 1
        save()
        sys.stderr.write("error: couldn't get home network: QMI protocol error (16): 'NotProvisioned'\n")
        sys.exit(1)
    h = m["home"]
    print(f"[{dev}] Successfully got home network:")
    print(f"\tHome network:\n\t\tMCC: '{h['mcc']}'\n\t\tMNC: '{h['mnc']}'\n\t\tDescription: '{h['desc']}'")
    print("\tNetwork name source: unknown")
    if "mcc2" in h:
        print(f"\t3GPP2 Home network (extended):\n\t\tMCC: '{h['mcc2']}'\n\t\tMNC: '{h['mnc2']}'\n\t\tDescription: ''")
elif action == "--pdc-list-configs=software":
    if m.get("list_fails"):
        sys.stderr.write("error: couldn't list configs: QMI protocol error (94): 'NotSupported'\n")
        sys.exit(1)
    if m.get("after"):
        step = m["after"].pop(0)
        save()
        if step == "down":
            sys.stderr.write(f"error: couldn't open the QmiDevice: Couldn't open QRTR node: {dev}\n")
            sys.exit(1)
        show(pending=m["activated"])
    elif m.get("activated"):
        if m.get("applies", True):
            for c in m["configs"]:
                c["status"] = "Active" if c["id"] == m["activated"] else "Inactive"
            save()
            show()
        else:
            show(pending=m["activated"])
    else:
        show()
elif action.startswith("--pdc-activate-config=software,"):
    how = m.get("activate", "ok")
    if how == "refuse":
        sys.stderr.write("error: couldn't activate config: QMI protocol error (25): 'InvalidId'\n")
        sys.exit(1)
    m["activated"] = action.split(",", 1)[1]
    m["after"] = list(m.get("after_activation", []))
    save()
    if how == "hang":
        time.sleep(60)
    print(f"[{dev}] Successfully requested config activation")
else:
    sys.stderr.write(f"qmicli stand-in: {action} not simulated\n")
    sys.exit(2)
'''

ROW = {"desc": "ROW_Commercial", "version": "0x8010809", "id": "81:8A:AB:E9:B5:CD:F9:5C:89:FB:04:06:ED:E4:6C:6B:85:D7:20:3A", "status": "Active"}
LAB = {"desc": "Volte_OEM_Lab", "version": "0x801F221", "id": "D8:EF:7F:E2:16:24:38:15:AB:FD:BA:CE:2F:B6:A8:A3:49:30:C4:62", "status": "Inactive"}
CU = {"desc": "OpenMkt-Commercial-CU", "version": "0x5011510", "id": "C1:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:01", "status": "Inactive"}
CU_LAB = {"desc": "Volte_OpenMkt-Lab-CU", "version": "0x5019999", "id": "C1:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:02", "status": "Inactive"}
CT = {"desc": "hVoLTE_OpenMkt-Commercial-CT", "version": "0x501131C", "id": "C7:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:01", "status": "Inactive"}
CT_TEST = {"desc": "VoLTE_Conf-Test-CT", "version": "0x5019999", "id": "C7:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:02", "status": "Inactive"}
CM = {"desc": "OpenMkt-Commercial-CMCC", "version": "0x5012000", "id": "CC:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:01", "status": "Inactive"}
CM_VOLTE_OLD = {"desc": "Volte_OpenMkt-Commercial-CMCC", "version": "0xF00000", "id": "CC:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:02", "status": "Inactive"}
CM_VOLTE = {"desc": "Volte_OpenMkt-Commercial-CMCC", "version": "0x1000000", "id": "CC:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:03", "status": "Inactive"}
CM_SUB = {"desc": "Volte_Subsidiz-Commercial-CMCC", "version": "0x9000000", "id": "CC:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:04", "status": "Inactive"}
CM_LAB = {"desc": "TGL_Comb_Attach-Lab-CMCC", "version": "0x9000000", "id": "CC:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:00:05", "status": "Inactive"}
UNICOM = {"mcc": 460, "mnc": 1, "desc": "CHN-UNICOM"}
TELECOM = {"mcc": 460, "mnc": 11, "desc": "CHN-CT"}
MOBILE = {"mcc": 460, "mnc": 0, "desc": "CHINA MOBILE"}
RESTART = ["pending", "down", "down"]


def configs(*cs, active=None):
    out = [dict(c, status="Inactive") for c in cs]
    for c in out:
        if c["id"] == (active or ROW)["id"]:
            c["status"] = "Active"
    return out


class Phone:
    """A temporary directory with pdc-select, uim-select's stand-in beside
    it, the stand-ins on PATH, the modem's state and the call log."""

    def __init__(self):
        self.dir = tempfile.mkdtemp(prefix="pdc-select-test-")
        self.bin = os.path.join(self.dir, "bin")
        self.libexec = os.path.join(self.dir, "libexec")
        self.state = os.path.join(self.dir, "state")
        self.log = os.path.join(self.dir, "log")
        self.modem = os.path.join(self.dir, "modem.json")
        os.makedirs(self.bin)
        os.makedirs(self.libexec)
        shutil.copy(SCRIPT, os.path.join(self.libexec, "pdc-select"))
        stand_ins = {
            os.path.join(self.bin, "qmicli"): FAKE_QMICLI,
            # The phone's busybox has no timeout applet.
            os.path.join(self.bin, "timeout"): '#!/bin/sh\necho "sh: timeout: not found" >&2\nexit 127\n',
            # A second of the script is 50 ms here.
            os.path.join(self.bin, "sleep"): f"#!/bin/sh\nexec {shutil.which('sleep')} 0.05\n",
            os.path.join(self.libexec, "uim-select"): '#!/bin/sh\necho "uim-select $*" >> "$FAKE_LOG"\n',
        }
        # The phone's awk is busybox's: held to POSIX where the host's is GNU's.
        awk = shutil.which("awk")
        if "GNU Awk" in subprocess.run([awk, "--version"], capture_output=True, text=True).stdout:
            stand_ins[os.path.join(self.bin, "awk")] = f'#!/bin/sh\nexec {awk} --posix "$@"\n'
        for path, text in stand_ins.items():
            with open(path, "w") as f:
                f.write(text)
            os.chmod(path, 0o755)

    def set(self, **modem):
        with open(self.modem, "w") as f:
            json.dump(modem, f)

    def run(self, *args):
        open(self.log, "w").close()
        env = dict(os.environ, PATH=self.bin + os.pathsep + os.environ["PATH"], FAKE_MODEM=self.modem, FAKE_LOG=self.log)
        r = subprocess.run(["sh", os.path.join(self.libexec, "pdc-select"), "--carriers", CARRIERS,
                            "--state", self.state, *args, "qrtr://0"], env=env, capture_output=True, text=True, timeout=60)
        return r.returncode, r.stdout + r.stderr

    def calls(self):
        with open(self.log) as f:
            return f.read().splitlines()

    def activations(self):
        return [c.split(",", 1)[1] for c in self.calls() if "--pdc-activate-config=software," in c]

    def recorded(self, mcc, mnc):
        path = os.path.join(self.state, f"pdc-{mcc}-{mnc}")
        if not os.path.exists(path):
            return None
        with open(path) as f:
            return f.read()

    def remove(self):
        shutil.rmtree(self.dir)


def main():
    failed = []
    phone = Phone()
    every_call = []

    def check(ok, what):
        print(("ok   " if ok else "FAIL ") + what)
        if not ok:
            failed.append(what)

    def run(*args, **kw):
        rc, out = phone.run(*args, **kw)
        every_call.extend(phone.calls())
        return rc, out

    phone.set(home=UNICOM, configs=configs(ROW, LAB, CM_VOLTE, CU_LAB, CU), after_activation=RESTART)
    rc, out = run()
    calls = phone.calls()
    check(rc == 0 and phone.activations() == [CU["id"]], f"a Unicom SIM: OpenMkt-Commercial-CU activated, not the lab one ({rc}, {phone.activations()})")
    check("pdc-select: OpenMkt-Commercial-CU is active" in out and calls[-1] == "uim-select qrtr://0",
          "waited through pending and the restart until active, then the SIM's session opened again")
    lists = [c for c in calls if "--pdc-list-configs" in c]
    check(len(lists) == 1 + len(RESTART) + 1, f"a list before, then until it is active ({len(lists)} lists)")
    check(phone.recorded(460, 1) == f"activated {CU['id']} OpenMkt-Commercial-CU\n", f"recorded: {phone.recorded(460, 1)!r}")
    check("the modem holds ROW_Commercial (Active)" in out and "the modem holds Volte_OEM_Lab (Inactive)" in out,
          "what the modem holds is in the journal")

    # The tester then activates another by hand: the next boot keeps it.
    rc, out = run()
    check(rc == 0 and phone.calls() == ["qmicli -d qrtr://0 --nas-get-home-network"] and "dealt with at an earlier boot" in out,
          f"a carrier dealt with is left alone at the next boot ({phone.calls()})")

    phone.set(home=MOBILE, configs=configs(ROW, CM, CM_VOLTE_OLD, CM_LAB, CM_SUB, CM_VOLTE), after_activation=["down"])
    rc, out = run()
    check(rc == 0 and phone.activations() == [CM_VOLTE["id"]],
          f"a China Mobile SIM (MNC 0): VoLTE, OpenMkt, then the newest version 0x1000000 over 0xF00000 ({phone.activations()})")

    phone.set(home=TELECOM, configs=configs(ROW, CT_TEST, CT, active=CT))
    rc, out = run()
    check(rc == 0 and phone.activations() == [] and "is Active already" in out and "uim-select qrtr://0" not in phone.calls(),
          f"a Telecom SIM whose configuration is active: nothing changed ({out.strip().splitlines()[-1]!r})")
    check(phone.recorded(460, 11) == f"kept {CT['id']} hVoLTE_OpenMkt-Commercial-CT\n", "kept, recorded: later changes by hand stay")

    pending = configs(ROW, CU)
    pending[1]["status"] = "Pending"
    phone.set(home=dict(UNICOM, mnc=6), configs=pending)
    rc, out = run()
    check(rc == 0 and phone.activations() == [] and "is Pending already" in out, "pending already: not activated again")

    phone.set(home=dict(UNICOM, mnc=9), configs=configs(ROW, LAB, CM_VOLTE))
    rc, out = run()
    check(rc == 0 and phone.activations() == [] and "holds no configuration for MCC 460 MNC 9" in out and phone.recorded(460, 9) is None,
          "no configuration of the carrier: nothing done, nothing recorded")

    phone.set(home={"mcc": 262, "mnc": 1, "desc": "Telekom.de"}, configs=configs(ROW, LAB))
    rc, out = run()
    check(rc == 0 and phone.calls() == ["qmicli -d qrtr://0 --nas-get-home-network"] and "not in" in out,
          "a carrier the table does not know: not even the list read")

    phone.set(home=None, configs=configs(ROW, CU))
    rc, out = run()
    homes = [c for c in phone.calls() if "--nas-get-home-network" in c]
    check(rc == 0 and len(homes) == 10 and len(phone.calls()) == 10 and "no home network" in out,
          f"no SIM: asked ten times, then nothing to do ({len(homes)})")

    phone.set(home=dict(UNICOM, mnc=1, mcc2=310, mnc2=0), home_fail=3, configs=configs(ROW, CU), after_activation=["down"])
    shutil.rmtree(phone.state, ignore_errors=True)
    rc, out = run()
    check(rc == 0 and phone.activations() == [CU["id"]], "the SIM read on the fourth try; the 3GPP home network, not the 3GPP2 one")

    phone.set(home=dict(UNICOM, desc="China Unicom"), configs=configs(ROW, dict(CU, desc="China Unicom VoLTE Commercial")),
              after_activation=["down"])
    shutil.rmtree(phone.state, ignore_errors=True)
    rc, out = run()
    check(rc == 0 and phone.activations() == [CU["id"]], "a description with spaces names the carrier by a word")

    phone.set(home=UNICOM, configs=configs(ROW, CU), activate="refuse")
    shutil.rmtree(phone.state, ignore_errors=True)
    rc, out = run()
    check(rc == 1 and "refuses OpenMkt-Commercial-CU" in out and "uim-select qrtr://0" not in phone.calls(),
          f"a refused activation fails ({rc})")
    rc, out = run()
    check(rc == 0 and phone.activations() == [], "and is not tried again at the next boot")

    phone.set(home=UNICOM, configs=configs(ROW, CU), activate="hang", after_activation=["down", "down"])
    shutil.rmtree(phone.state, ignore_errors=True)
    t0 = time.monotonic()
    rc, out = run()
    took = time.monotonic() - t0
    check(rc == 0 and "no answer to the activation in 60 s" in out and "OpenMkt-Commercial-CU is active" in out
          and phone.calls()[-1] == "uim-select qrtr://0" and took < 20,
          f"the activation never answered: given up after 60 steps ({took:.1f} s), the modem still waited for")

    phone.set(home=UNICOM, configs=configs(ROW, CU), applies=False)
    shutil.rmtree(phone.state, ignore_errors=True)
    rc, out = run()
    lists = [c for c in phone.calls() if "--pdc-list-configs" in c]
    check(rc == 0 and "pending: the modem applies it when it next starts" in out and len(lists) == 31
          and phone.calls()[-1] == "uim-select qrtr://0", f"a modem that never restarts: pending after a minute ({len(lists)} lists)")

    phone.set(home=UNICOM, configs=configs(ROW, CU), list_fails=True)
    shutil.rmtree(phone.state, ignore_errors=True)
    rc, out = run()
    check(rc == 1 and "does not list its configurations" in out and phone.recorded(460, 1) is None, "a modem that lists nothing: fails, nothing recorded")

    phone.set(home=UNICOM, configs=configs(ROW, CU))
    rc, out = run("--dry-run")
    check(rc == 0 and phone.activations() == [] and "would activate OpenMkt-Commercial-CU" in out and phone.recorded(460, 1) is None,
          "--dry-run: nothing activated or recorded")

    never = [c for c in every_call if any(a in c for a in ("--pdc-load-config", "--pdc-deactivate-config", "--pdc-delete-config", "platform"))]
    check(not never, f"nothing loaded, deactivated or deleted, no platform configuration touched ({never})")

    phone.remove()
    print("pdc-select-test: " + (f"{len(failed)} failed" if failed else "all passed"))
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
