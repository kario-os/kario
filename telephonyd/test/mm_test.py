#!/usr/bin/env python3
"""telephonyd's ModemManager backend against mock_mm.py on a private bus.

    mm_test.py PHONE_TELEPHONYD

Starts a dbus-daemon, the mock and phone-telephonyd --modem modemmanager, then
drives telephonyd over its socket the way the shell does while the mock plays
the network. Prints MM-SELFTEST: PASS n/n (exit 0) or the failed cases.
"""

import json
import os
import signal
import socket
import subprocess
import sys
import tempfile
import time

import dbus

HERE = os.path.dirname(os.path.abspath(__file__))
# telephonyd's simulated network, which the mock plays too.
with open(os.path.join(HERE, "..", "sim-network.json"), encoding="utf-8") as f:
    NET = json.load(f)
STORED = "A message kept on the SIM"
ROOT = "/org/freedesktop/ModemManager1"
LTE, NR = 1 << 14, 1 << 15

BUS_CONFIG = """<!DOCTYPE busconfig PUBLIC "-//freedesktop//DTD D-BUS Bus Configuration 1.0//EN"
 "http://www.freedesktop.org/standards/dbus/1.0/busconfig.dtd">
<busconfig>
  <type>system</type>
  <listen>unix:path=%s</listen>
  <auth>EXTERNAL</auth>
  <policy context="default">
    <allow user="*"/>
    <allow own="*"/>
    <allow send_destination="*" eavesdrop="true"/>
    <allow eavesdrop="true"/>
  </policy>
</busconfig>
"""


class Telephonyd:
    def __init__(self, path):
        self.sock = socket.socket(socket.AF_UNIX)
        for _ in range(100):
            try:
                self.sock.connect(path)
                break
            except OSError:
                time.sleep(0.05)
        else:
            raise RuntimeError("telephonyd socket did not appear")
        self.buf = b""
        self.events = []
        self.next = 0
        self.rpc("subscribe")

    def _line(self, timeout):
        self.sock.settimeout(timeout)
        while b"\n" not in self.buf:
            chunk = self.sock.recv(65536)
            if not chunk:
                raise RuntimeError("telephonyd closed the socket")
            self.buf += chunk
        line, self.buf = self.buf.split(b"\n", 1)
        return json.loads(line)

    def rpc(self, method, **params):
        self.next += 1
        self.sock.sendall((json.dumps({"id": self.next, "method": method, "params": params}) + "\n").encode())
        while True:
            msg = self._line(5)
            if msg.get("event"):
                self.events.append(msg)
            elif msg.get("id") == self.next:
                return msg

    def ok(self, method, **params):
        r = self.rpc(method, **params)
        if not r.get("ok"):
            raise AssertionError("%s failed: %s" % (method, r.get("err")))
        return r.get("result")

    def wait(self, what, pred, timeout=5):
        deadline = time.time() + timeout
        while True:
            for i, ev in enumerate(self.events):
                if pred(ev):
                    del self.events[: i + 1]
                    return ev
            left = deadline - time.time()
            if left <= 0:
                raise AssertionError("no %s within %ss (events: %s)" % (what, timeout, self.events[-5:]))
            try:
                self.events.append(self._line(left))
            except socket.timeout:
                pass

    def call(self, call_id, state, timeout=5):
        return self.wait("call %s %s" % (call_id, state),
                         lambda e: e.get("event") == "call" and e["call"]["state"] == state
                         and (call_id is None or e["call"]["id"] == call_id), timeout)["call"]

    def status(self, pred, what, timeout=5):
        return self.wait("status " + what, lambda e: e.get("event") == "status" and pred(e["status"]), timeout)["status"]

    def sms(self, pred, what, timeout=5):
        return self.wait("sms " + what, lambda e: e.get("event") == "sms" and pred(e["sms"]), timeout)["sms"]


class World:
    def __init__(self, telephonyd):
        self.tmp = tempfile.mkdtemp(prefix="mm-test-")
        cfg = os.path.join(self.tmp, "bus.conf")
        with open(cfg, "w") as f:
            f.write(BUS_CONFIG % os.path.join(self.tmp, "bus"))
        self.bus_proc = subprocess.Popen(["dbus-daemon", "--config-file=" + cfg, "--nofork", "--print-address"],
                                         stdout=subprocess.PIPE, text=True)
        self.address = self.bus_proc.stdout.readline().strip()
        os.environ["DBUS_SYSTEM_BUS_ADDRESS"] = self.address
        self.mock = None
        self.start_mock()
        self.control().Store("10010", STORED)
        self.telephonyd = telephonyd
        self.start_tel()

    # No --state-dir: what phone-smsd has not acknowledged survives a
    # restart in the modem's storage alone.
    def start_tel(self):
        self.tel_proc = subprocess.Popen([self.telephonyd, "--run-dir", self.tmp, "--modem", "modemmanager"])
        self.tel = Telephonyd(os.path.join(self.tmp, "telephonyd.sock"))

    def restart_tel(self):
        self.tel_proc.kill()
        self.tel_proc.wait(5)
        os.remove(os.path.join(self.tmp, "telephonyd.sock"))
        self.start_tel()
        return self.tel

    def start_mock(self):
        self.mock = subprocess.Popen([sys.executable, os.path.join(HERE, "mock_mm.py")], stdout=subprocess.PIPE, text=True)
        if "ready" not in self.mock.stdout.readline():
            raise RuntimeError("mock ModemManager did not start")

    def stop_mock(self):
        self.mock.send_signal(signal.SIGTERM)
        self.mock.wait(5)

    def control(self):
        bus = dbus.bus.BusConnection(self.address)
        return dbus.Interface(bus.get_object("org.freedesktop.ModemManager1", ROOT), "org.kario.MockModem")

    def log(self):
        return [str(s) for s in self.control().Log()]

    def mm_calls(self):
        return [str(p) for p in self.control().Calls()]

    def close(self):
        for p in (self.tel_proc, self.mock, self.bus_proc):
            if p and p.poll() is None:
                p.terminate()
                p.wait(5)


def expect(cond, what):
    if not cond:
        raise AssertionError(what)


def until(get, pred, what, timeout=5):
    deadline = time.time() + timeout
    while True:
        v = get()
        if pred(v):
            return v
        if time.time() > deadline:
            raise AssertionError("no %s within %ss (last: %s)" % (what, timeout, v))
        time.sleep(0.05)


def main():
    w = World(sys.argv[1])
    t, ctl = w.tel, w.control()
    cases = []

    def case(fn):
        cases.append(fn)
        return fn

    # The first two happen while the test connects, so they are polled.
    @case
    def enables_and_registers():
        st = until(lambda: t.ok("status"), lambda s: s["registered"], "registered")
        expect(st["operator"] == NET["operator"] and st["rat"] == "4G" and st["signal"] == 3, st)
        expect(st["own"] == "+8613012345678" and st["backend"] == "modemmanager", st)
        expect("Enable True" in w.log(), w.log())

    @case
    def country_and_network_time():
        st = until(lambda: t.ok("status"), lambda s: s["sim_mcc"] and s.get("nitz", {}).get("time"), "the SIM's country")
        expect(st["sim_mcc"] == NET["sim_mcc"] and st["net_mcc"] == NET["sim_mcc"], st)
        expect(st["nitz"]["offset"] == NET["nitz_offset"] and st["nitz"]["time"] == 1794190830, st)
        ctl.Roam("33402", -360, "2026-11-08T20:20:30-06:00")
        st = until(lambda: t.ok("status"), lambda s: s["net_mcc"] == "334", "roaming in Mexico")
        expect(st["sim_mcc"] == NET["sim_mcc"] and st["nitz"]["offset"] == -360 and st["nitz"]["time"] == 1794190830, st)
        ctl.Roam(NET["sim_mcc"] + "00", NET["nitz_offset"], "2026-11-09T10:20:30+08:00")
        until(lambda: t.ok("status"), lambda s: s["net_mcc"] == NET["sim_mcc"], "home again")

    def pending(pred):
        return [e for e in t.ok("sms.pending") if pred(e)]

    @case
    def stored_sms_waits_for_smsd():
        msgs = until(lambda: pending(lambda e: e["sms"]["peer"] == "10010"), lambda m: m, "the SIM's message")
        s = msgs[0]["sms"]
        expect(len(msgs) == 1 and s["dir"] == "in" and msgs[0]["key"] == s["id"], msgs)
        expect(s["text"] == STORED and s["at"] == 1794190830, msgs)
        time.sleep(0.3)
        expect(len(ctl.Messages()) == 1 and "Delete " + STORED not in w.log(), w.log())
        expect(t.ok("sms.ack", keys=[s["id"]]) == {"acked": 1}, "acked")
        until(lambda: ctl.Messages(), lambda m: not m, "the SIM emptied")
        expect("Delete " + STORED in w.log() and not t.ok("sms.pending"), w.log())
        t.ok("status")
        t.events.clear()

    @case
    def imei():
        expect(t.ok("status")["imei"] == "351234567890124", t.ok("status"))

    @case
    def outgoing_call():
        cid = t.ok("dial", number="10010")["call_id"]
        t.call(cid, "dialing")
        t.call(cid, "alerting")
        expect("CreateCall 10010" in w.log() and "Start 10010" in w.log(), w.log())
        ctl.Remote(w.mm_calls()[0], "answer")
        t.call(cid, "active")
        expect(t.ok("calls")[0]["state"] == "active", t.ok("calls"))
        expect(not t.rpc("dial", number="10086").get("ok"), "a second call while one is active")
        t.ok("hangup", call_id=cid)
        c = t.call(cid, "ended")
        expect(c["reason"] == "hangup" and c["connected"] > 0, c)
        time.sleep(0.3)
        expect(not w.mm_calls() and "DeleteCall" in w.log(), (w.mm_calls(), w.log()))
        expect(t.ok("log")[0]["result"] == "outgoing", t.ok("log")[0])

    @case
    def busy():
        cid = t.ok("dial", number="10086000")["call_id"]
        c = t.call(cid, "ended")
        expect(c["reason"] == "busy", c)
        expect(t.ok("log")[0]["result"] == "busy", t.ok("log")[0])

    @case
    def incoming_answered_then_remote_hangs_up():
        path = str(ctl.Ring("13900001111"))
        c = t.call(None, "incoming")
        expect(c["dir"] == "in" and c["number"] == "13900001111", c)
        t.ok("answer", call_id=c["id"])
        t.call(c["id"], "active")
        ctl.Remote(path, "hangup")
        c = t.call(c["id"], "ended")
        expect(c["reason"] == "remote", c)
        expect(t.ok("log")[0]["result"] == "answered", t.ok("log")[0])

    @case
    def incoming_rejected():
        ctl.Ring("13900002222")
        c = t.call(None, "incoming")
        t.ok("hangup", call_id=c["id"])
        expect(t.call(c["id"], "ended")["reason"] == "rejected", "rejected")
        expect(t.ok("log")[0]["result"] == "rejected", t.ok("log")[0])

    @case
    def incoming_missed():
        path = str(ctl.Ring("13900003333"))
        c = t.call(None, "incoming")
        ctl.Remote(path, "hangup")
        expect(t.call(c["id"], "ended")["reason"] == "missed", "missed")
        expect(t.ok("log")[0]["result"] == "missed", t.ok("log")[0])

    @case
    def hold_and_resume():
        cid = t.ok("dial", number="10010")["call_id"]
        t.call(cid, "alerting")
        ctl.Remote(w.mm_calls()[0], "answer")
        t.call(cid, "active")
        t.ok("hold", call_id=cid, on=True)
        t.call(cid, "held")
        t.ok("hold", call_id=cid, on=False)
        t.call(cid, "active")
        t.ok("mute", call_id=cid, on=True)
        expect(t.wait("muted", lambda e: e.get("event") == "call" and e["call"]["muted"])["call"]["id"] == cid, "mute")
        t.ok("hangup", call_id=cid)
        t.call(cid, "ended")

    @case
    def call_waiting():
        first = t.ok("dial", number="10010")["call_id"]
        t.call(first, "alerting")
        ctl.Remote(w.mm_calls()[0], "answer")
        t.call(first, "active")
        ctl.Ring("13900004444")
        second = t.call(None, "incoming")
        expect(second["id"] != first, second)
        t.ok("answer", call_id=second["id"])
        t.call(first, "held")
        t.call(second["id"], "active")
        expect("HoldAndAccept" in w.log(), w.log())
        t.ok("hangup", call_id=second["id"])
        t.call(second["id"], "ended")
        t.ok("hangup", call_id=first)
        t.call(first, "ended")

    @case
    def send_sms():
        sid = t.ok("sms.send", to="10010", text="What is my balance?", id="o1")["id"]
        expect(sid == "o1", sid)
        s = t.sms(lambda s: s["id"] == sid, "outgoing")
        expect(s["dir"] == "out" and s["read"] and s["state"] == "sending", s)
        t.sms(lambda s: s["id"] == sid and s["state"] == "sent", "sent")
        t.sms(lambda s: s["id"] == sid and s["state"] == "delivered", "delivered (a delivery report)")
        keys = [e["key"] for e in t.ok("sms.pending")]
        expect(keys == [sid + "@sent", sid + "@delivered"], keys)
        expect(t.ok("sms.ack", keys=keys) == {"acked": 2}, "acked")
        time.sleep(0.3)
        log = w.log()
        expect("Create 10010 What is my balance?" in log and "Send 10010" in log and "Delete What is my balance?" in log, log)
        expect(not ctl.Messages(), ctl.Messages())

    @case
    def send_sms_never_delivered():
        sid = t.ok("sms.send", to="10086998", text="Nobody home")["id"]
        t.sms(lambda s: s["id"] == sid and s["state"] == "sent", "sent")
        time.sleep(0.5)
        expect(len(ctl.Messages()) == 1 and "Delete Nobody home" not in w.log(), "kept for its report")
        t.ok("sms.ack", keys=[sid + "@sent"])

    @case
    def send_sms_rejected_by_network():
        sid = t.ok("sms.send", to="10086999", text="This one fails")["id"]
        t.sms(lambda s: s["id"] == sid and s["state"] == "failed", "failed")
        time.sleep(0.3)
        log = w.log()
        expect("Send 10086999" in log and "Delete This one fails" in log and len(ctl.Messages()) == 1, log)
        expect(t.ok("sms.ack", keys=[sid + "@failed"]) == {"acked": 1}, "acked")

    @case
    def receive_sms_in_parts():
        ctl.Receive("10086", NET["sms_replies"]["10086"], True)
        s = t.sms(lambda s: s["dir"] == "in" and s["peer"] == "10086", "multipart")
        expect(s["text"] == NET["sms_replies"]["10086"], s)
        expect([e["key"] for e in pending(lambda e: e["event"] == "sms" and e["sms"]["dir"] == "in")] == [s["id"]], "pending")
        t.ok("sms.ack", keys=[s["id"]])
        until(lambda: ctl.Messages(), lambda m: len(m) == 1, "the modem's copy deleted")
        expect(not [e for e in t.events if e.get("event") == "sms"], "delivered once")

    @case
    def unacknowledged_sms_survives_a_restart():
        nonlocal t
        ctl.Receive("10010", "Keep me", False)
        s = t.sms(lambda s: s["text"] == "Keep me", "received")
        t = w.restart_tel()
        again = until(lambda: pending(lambda e: e["key"] == s["id"]), lambda m: m, "the same message after the restart")
        expect(again[0]["sms"]["text"] == "Keep me", again)
        t.ok("sms.ack", keys=[s["id"]])
        until(lambda: w.log(), lambda log: "Delete Keep me" in log, "the modem's copy deleted")

    @case
    def cell_broadcast():
        until(lambda: w.log(), lambda log: "SetChannels 4352-4359,4370-4400" in log, "the alert channels chosen")
        ctl.Broadcast(4370, 12, "Earthquake drill")
        ev = t.wait("cell broadcast", lambda e: e.get("event") == "cb")
        cb = ev["cb"]
        expect(cb["channel"] == 4370 and cb["serial"] == 12 << 4 and cb["text"] == "Earthquake drill" and ev["key"] == cb["id"], ev)
        expect(len(ctl.Broadcasts()) == 1 and [e["key"] for e in t.ok("sms.pending")] == [cb["id"]], "kept until smsd has it")
        t.ok("sms.ack", keys=[cb["id"]])
        until(lambda: ctl.Broadcasts(), lambda b: not b, "the modem's copy deleted")
        expect("DeleteCbm Earthquake drill" in w.log(), w.log())

    @case
    def radio_changes():
        ctl.Radio(10, NR | LTE)
        st = t.status(lambda s: s["rat"] == "5G", "5G")
        expect(st["signal"] == 1, st)
        ctl.Radio(90, LTE)
        st = t.status(lambda s: s["signal"] == 4, "full signal")
        expect(st["rat"] == "4G" and t.ok("status")["signal"] == 4, st)

    def ussd_event(what):
        return t.wait("ussd " + what, lambda e: e.get("event") == "ussd")

    def no_ussd_events():
        time.sleep(0.2)
        t.ok("status")
        expect(not [e for e in t.events if e.get("event") == "ussd"], t.events)

    @case
    def ussd_done():
        r = t.ok("ussd.initiate", code="*100#")
        expect(r == {"state": "done", "text": NET["ussd_answers"]["*100#"]}, r)
        r = t.ok("ussd.initiate", code="*123#")
        expect(r == {"state": "done", "text": NET["ussd_unknown"]}, r)
        expect("Initiate *100#" in w.log() and "Initiate *123#" in w.log(), w.log())
        r = t.rpc("ussd.initiate", code="100#")
        expect(not r["ok"] and r["err"] == "invalid code", r)
        r = t.rpc("ussd.respond", text="1")
        expect(not r["ok"] and r["err"] == "no ussd session", r)
        no_ussd_events()

    @case
    def ussd_menu():
        r = t.ok("ussd.initiate", code="*101#")
        expect(r == {"state": "input", "text": NET["ussd_menu"]}, r)
        r = t.rpc("ussd.initiate", code="*100#")
        expect(not r["ok"] and r["err"] == "ussd session open", r)
        r = t.ok("ussd.respond", text="9")
        expect(r == {"state": "input", "text": NET["ussd_menu_retry"] + "\n" + NET["ussd_menu"]}, r)
        r = t.ok("ussd.respond", text="2")
        expect(r == {"state": "done", "text": NET["ussd_menu_answers"]["2"]}, r)
        expect("Respond 9" in w.log() and "Respond 2" in w.log(), w.log())
        no_ussd_events()

    @case
    def ussd_network():
        ctl.UssdNetwork("Your plan renews tomorrow.", False)
        ev = ussd_event("notification")
        expect(ev["state"] == "notify" and ev["text"] == "Your plan renews tomorrow.", ev)
        ctl.UssdNetwork("Reply 1 to renew your plan", True)
        ev = ussd_event("request")
        expect(ev["state"] == "input" and ev["text"] == "Reply 1 to renew your plan", ev)
        r = t.ok("ussd.respond", text="1")
        expect(r == {"state": "done", "text": NET["ussd_received"]}, r)
        expect("Respond 1" in w.log(), w.log())
        no_ussd_events()

    @case
    def ussd_cancel():
        expect(t.ok("ussd.initiate", code="*101#")["state"] == "input", "the menu")
        expect(t.ok("ussd.cancel") == {}, "cancel")
        until(lambda: w.log(), lambda log: "Cancel" in log, "Cancel on the modem")
        r = t.rpc("ussd.respond", text="1")
        expect(not r["ok"] and r["err"] == "no ussd session", r)
        expect(t.ok("ussd.cancel") == {}, "cancel without a session")
        expect(t.ok("ussd.initiate", code="*100#")["state"] == "done", "a new session")
        no_ussd_events()

    @case
    def ussd_ended_by_network():
        expect(t.ok("ussd.initiate", code="*101#")["state"] == "input", "the menu")
        ctl.UssdEnd()
        ev = ussd_event("end")
        expect(ev["state"] == "done" and ev["text"] == "", ev)
        r = t.rpc("ussd.respond", text="1")
        expect(not r["ok"] and r["err"] == "no ussd session", r)

    @case
    def sim_pin():
        ctl.LockSim("1234")
        st = t.status(lambda s: s["sim"] == "pin", "the SIM asking for its PIN")
        expect(st["sim_tries"] == 3 and not st["registered"], st)
        expect(not t.rpc("dial", number="10010").get("ok"), "dial with a locked SIM")
        expect(not t.rpc("sim.unlock", pin="12").get("ok"), "a two-digit PIN")
        t.ok("sim.unlock", pin="0000")
        ev = t.wait("unlock", lambda e: e.get("event") == "unlock")
        expect(not ev["ok"] and "Incorrect" in ev["err"], ev)
        expect(t.ok("status")["sim_tries"] == 2, t.ok("status"))
        t.ok("sim.unlock", pin="1234")
        expect(t.wait("unlock", lambda e: e.get("event") == "unlock")["ok"], "unlocked")
        st = t.status(lambda s: s["registered"], "registered after the PIN")
        expect(st["sim"] == "ready" and st["sim_tries"] == 0, st)
        log = w.log()
        last_pin = len(log) - 1 - log[::-1].index("SendPin")
        expect("Enable True" in log[last_pin:], log)

    @case
    def sim_puk():
        ctl.LockSim("1234")
        t.status(lambda s: s["sim"] == "pin", "locked again")
        for _ in range(3):
            t.ok("sim.unlock", pin="0000")
            t.wait("unlock", lambda e: e.get("event") == "unlock")
        st = until(lambda: t.ok("status"), lambda s: s["sim"] == "puk", "the SIM asking for its PUK")
        expect(st["sim_tries"] == 10, st)
        t.ok("sim.unlock", puk="12345678", pin="4321")
        expect(t.wait("unlock", lambda e: e.get("event") == "unlock")["ok"], "PUK accepted")
        t.status(lambda s: s["registered"], "registered after the PUK")

    @case
    def sim_pin_settings():
        # Settings › SIM lock on ModemManager: Sim.EnablePin and Sim.ChangePin; the
        # lock's state is the 3GPP interface's EnabledFacilityLocks.
        t.status(lambda s: s["registered"] and s["pin_lock"], "the PIN lock on after the PUK")
        t.ok("sim.change_pin", pin="4321", new_pin="2468")
        expect(t.wait("changed", lambda e: e.get("event") == "pin")["ok"], "PIN changed")
        t.ok("sim.pin_lock", pin="4321", on=False)
        ev = t.wait("wrong PIN", lambda e: e.get("event") == "pin")
        expect(not ev["ok"] and "Incorrect" in ev["err"], ev)
        t.ok("sim.pin_lock", pin="2468", on=False)
        expect(t.wait("lock off", lambda e: e.get("event") == "pin")["ok"], "lock off")
        # The property change may come before the method's answer.
        until(lambda: t.ok("status"), lambda st: not st["pin_lock"], "the PIN lock off")
        expect(not t.rpc("sim.change_pin", pin="2468", new_pin="1357").get("ok"), "change with the lock off")
        expect("ChangePin" in w.log() and "EnablePin False" in w.log(), w.log())

    @case
    def mobile_data():
        st = until(lambda: t.ok("status"), lambda s: s["data_state"] == "connected", "the data link up")
        expect(st["data"] and "wwan0" in st["data_ifaces"] and "cdc-wdm0" not in st["data_ifaces"], st)
        expect("Connect apn=" in w.log(), w.log())
        t.ok("data.set", on=False)
        st = until(lambda: t.ok("status"), lambda s: s["data_state"] == "off", "the data link down")
        until(lambda: w.log(), lambda log: "Disconnect" in log, "Simple.Disconnect")
        expect(st["registered"] and not st["data"] and "wwan0" in st["data_ifaces"], st)
        t.ok("data.set", on=True)
        until(lambda: t.ok("status"), lambda s: s["data_state"] == "connected", "the data link up again")

    # Airplane mode: Enable(false) ends the call, then low power; only an
    # emergency number is placed, powering the modem up first, and the
    # radio stays on during that call.
    @case
    def airplane_mode():
        cid = t.ok("dial", number="10010")["call_id"]
        t.call(cid, "alerting")
        t.ok("radio.set", on=False)
        t.call(cid, "ended")
        until(lambda: t.ok("status"), lambda s: not s["registered"] and not s["radio"] and s["data_state"] == "off", "the radio off")
        until(lambda: w.log(), lambda log: "SetPowerState 2" in log, "low power")
        log = w.log()
        expect(log.index("Enable False") < log.index("SetPowerState 2"), log)
        r = t.rpc("dial", number="10010")
        expect(not r["ok"] and r["err"] == "the radio is off", r)
        sos = t.ok("dial", number="112")["call_id"]
        expect(t.call(sos, "dialing")["emergency"], "an emergency call")
        until(lambda: w.log(), lambda log: "CreateCall 112" in log, "the emergency call placed")
        log = w.log()
        expect(log.index("SetPowerState 3") < log.index("CreateCall 112") and t.ok("status")["radio"], log)
        r = t.rpc("radio.set", on=False)
        expect(not r["ok"] and r["err"] == "an emergency call is in progress", r)
        t.ok("hangup", call_id=sos)
        t.call(sos, "ended")
        # The emergency call turned the radio on and registered (events the
        # waits above passed over); the shell's radio.set changes nothing.
        t.ok("radio.set", on=True)
        until(lambda: t.ok("status"), lambda s: s["registered"] and s["radio"], "registered again")

    @case
    def modem_removed_mid_call_and_back():
        cid = t.ok("dial", number="10010")["call_id"]
        t.call(cid, "alerting")
        ctl.RemoveModem()
        c = t.call(cid, "ended")
        expect(c["reason"] == "dropped", c)
        t.status(lambda s: not s["registered"], "unregistered")
        expect(not t.rpc("dial", number="10010").get("ok"), "dial without a modem")
        ctl.AddModem()
        t.status(lambda s: s["registered"], "registered again")
        expect(w.log().count("Enable True") >= 2, w.log())

    @case
    def modemmanager_restarts():
        w.stop_mock()
        t.status(lambda s: not s["registered"], "ModemManager gone")
        w.start_mock()
        t.status(lambda s: s["registered"] and s["operator"] == NET["operator"], "ModemManager back")

    @case
    def errors():
        expect(not t.rpc("answer", call_id="c999").get("ok"), "answer unknown")
        expect(not t.rpc("dial", number="12").get("ok"), "invalid number")
        expect(not t.rpc("sms.send", to="10010", text="").get("ok"), "empty SMS")
        r = t.rpc("sim.ring", number="10010")
        expect(not r.get("ok") and "not available" in r.get("err", ""), r)
        r = t.rpc("sim.ussd", text="notice", request=False)
        expect(not r.get("ok") and "not available" in r.get("err", ""), r)

    passed, failed = 0, []
    try:
        for fn in cases:
            try:
                fn()
                passed += 1
                print("ok   " + fn.__name__, flush=True)
            except Exception as e:
                failed.append(fn.__name__)
                print("FAIL %s: %s" % (fn.__name__, e), flush=True)
        expect(w.tel_proc.poll() is None, "telephonyd still running")
    finally:
        w.close()
    print("MM-SELFTEST: %s %d/%d" % ("PASS" if not failed else "FAIL", passed, len(cases)))
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
