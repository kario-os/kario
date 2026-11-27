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
import threading
import time

import dbus

HERE = os.path.dirname(os.path.abspath(__file__))
# telephonyd's simulated network, which the mock plays too.
with open(os.path.join(HERE, "..", "sim-network.json"), encoding="utf-8") as f:
    NET = json.load(f)
STORED = "A message kept on the SIM"
ROOT = "/org/freedesktop/ModemManager1"
GSM, LTE, NR = 1 << 1, 1 << 14, 1 << 15

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
        self.broken = []
        self.start_mock()
        self.control().Store("10010", STORED)
        self.telephonyd = telephonyd
        self.start_tel()

    # No --state-dir: what phone-smsd has not acknowledged survives a
    # restart in the modem's storage alone. The IMS registration program
    # says what ims() last set, and fails before that.
    def start_tel(self):
        prog = os.path.join(self.tmp, "ims-status")
        with open(prog, "w") as f:
            f.write("#!/bin/sh\ncat '%s' 2>/dev/null || { echo 'no IMSA service' >&2; exit 1; }\n"
                    % os.path.join(self.tmp, "ims"))
        os.chmod(prog, 0o755)
        self.tel_proc = subprocess.Popen([self.telephonyd, "--run-dir", self.tmp, "--modem", "modemmanager",
                                          "--apns", os.path.join(HERE, "..", "apns.json"), "--ims-status", prog],
                                         stderr=subprocess.PIPE, text=True)
        threading.Thread(target=self.read_stderr, args=(self.tel_proc.stderr,), daemon=True).start()
        self.tel = Telephonyd(os.path.join(self.tmp, "telephonyd.sock"))

    # telephonyd's stderr, passed on; what it says the backend breaks of
    # the Modem contract (contract_problems) is kept for the last case.
    def read_stderr(self, stream):
        for line in stream:
            sys.stderr.write(line)
            if "breaks the Modem contract" in line:
                self.broken.append(line.strip())

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

    def ims(self, state):
        with open(os.path.join(self.tmp, "ims"), "w") as f:
            f.write(state + "\n")

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

    # China Mobile's cmnet is the LTE attach APN (type ia): set once with
    # Modem3gpp.SetInitialEpsBearerSettings, as the modem asks for none.
    # ModemManager takes the radio down and up to apply it; the data link
    # waits for it rather than connecting twice.
    @case
    def attach_apn():
        want = "SetInitialEpsBearerSettings apn=cmnet ip-type=4"
        until(lambda: w.log(), lambda log: want in log, "the attach APN set")
        until(lambda: t.ok("status"), lambda s: s["registered"] and s["data_state"] == "connected", "attached again, the data link up")
        log = w.log()
        connects = [i for i, e in enumerate(log) if e.startswith("Connect")]
        expect(log.count(want) == 1 and connects and log.index(want) < connects[0], log)
        expect(dict(ctl.EpsSettings()) == {"apn": "cmnet", "ip-type": 4}, ctl.EpsSettings())

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
        # A second call holds the first (HoldAndAccept) before it goes out;
        # a third is refused while it is being set up.
        second = t.ok("dial", number="10086")["call_id"]
        t.call(cid, "held")
        t.call(second, "alerting")
        log = w.log()
        expect(log.index("HoldAndAccept") < log.index("CreateCall 10086"), log)
        expect(not t.rpc("dial", number="10087").get("ok"), "a third call while the second is set up")
        t.ok("hangup", call_id=second)
        t.call(second, "ended")
        t.ok("hold", call_id=cid, on=False)
        t.call(cid, "active")
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

    def ended_failed(cid, words):
        c = t.call(cid, "ended")
        expect(c["reason"] == "failed" and words in c.get("error", ""), c)
        until(lambda: w.mm_calls(), lambda calls: not calls, "the call deleted")
        return c

    # ModemManager ends a call it could not answer (reason error) before it
    # replies to Accept: the call failed with the modem's words, not missed.
    @case
    def answer_refused():
        ctl.Refuse("Accept", "Couldn't accept call: QMI protocol error (14): 'CallFailed'", True)
        ctl.Ring("13900005555")
        c = t.call(None, "incoming")
        t.ok("answer", call_id=c["id"])
        ended_failed(c["id"], "answering failed: Couldn't accept call")
        expect(t.ok("log")[0]["result"] == "missed", t.ok("log")[0])

    # A refused Accept that leaves the call ringing: telephonyd ends it rather
    # than leave the screen on a call that cannot be answered.
    @case
    def answer_refused_still_ringing():
        ctl.Refuse("Accept", "Couldn't accept call: modem busy", False)
        ctl.Ring("13900006666")
        c = t.call(None, "incoming")
        t.ok("answer", call_id=c["id"])
        ended_failed(c["id"], "modem busy")

    # ModemManager ends a call it could not start (reason unknown) before it
    # replies to Start: failed with the reason, not "the other side hung up".
    @case
    def dial_refused():
        ctl.Refuse("Start", "Couldn't start call: QMI protocol error (48): 'InvalidOperation'", True)
        cid = t.ok("dial", number="10010")["call_id"]
        ended_failed(cid, "the modem did not place the call: Couldn't start call")
        ctl.Refuse("CreateCall", "Cannot create call: not registered", False)
        cid = t.ok("dial", number="10010")["call_id"]
        ended_failed(cid, "ModemManager did not create the call: Cannot create call")
        expect(t.ok("log")[0]["result"] == "outgoing", t.ok("log")[0])

    # Hung up while Start is unanswered: its error is the hang-up's, and
    # the call ends as hung up, not failed.
    @case
    def hangup_while_starting():
        ctl.Slow("Start", 800)
        cid = t.ok("dial", number="10010")["call_id"]
        t.call(cid, "alerting")
        t.ok("hangup", call_id=cid)
        c = t.call(cid, "ended")
        expect(c["reason"] == "hangup" and not c.get("error"), c)
        until(lambda: w.mm_calls(), lambda calls: not calls, "the call deleted")

    @case
    def call_audio_failed():
        cid = t.ok("dial", number="10010")["call_id"]
        t.call(cid, "alerting")
        ctl.Remote(w.mm_calls()[0], "answer")
        t.call(cid, "active")
        ctl.Remote(w.mm_calls()[0], "audio-failed")
        ended_failed(cid, "audio")

    # A Hangup the modem refuses deletes the call, which hangs it up in
    # ModemManager; a call left would refuse every later dial.
    @case
    def hangup_refused():
        cid = t.ok("dial", number="10010")["call_id"]
        t.call(cid, "alerting")
        ctl.Refuse("Hangup", "Couldn't hangup call", False)
        t.ok("hangup", call_id=cid)
        expect(t.call(cid, "ended")["reason"] == "hangup", "hung up")
        until(lambda: w.mm_calls(), lambda calls: not calls, "the call deleted")
        cid = t.ok("dial", number="10010")["call_id"]
        t.call(cid, "alerting")
        t.ok("hangup", call_id=cid)
        t.call(cid, "ended")

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
    def dtmf():
        cid = t.ok("dial", number="10086")["call_id"]
        t.call(cid, "alerting")
        ctl.Remote(w.mm_calls()[0], "answer")
        t.call(cid, "active")
        t.ok("dtmf", call_id=cid, tones="1")
        t.ok("dtmf", call_id=cid, tones="0#")
        expect(t.wait("tones", lambda e: e.get("event") == "call" and e["call"].get("dtmf") == "10#")["call"]["id"] == cid, "dtmf state")
        expect("SendDtmf 1" in w.log() and "SendDtmf 0#" in w.log(), w.log())
        # The call keeps the last 64 tones (kMaxDtmf), however many were sent.
        menu = ["0123456789*#ABCD" * 2, "9" * 32, "#*" * 16]
        for tones in menu:
            t.ok("dtmf", call_id=cid, tones=tones)
        last = ("10#" + "".join(menu))[-64:]
        expect(t.wait("tones kept", lambda e: e.get("event") == "call" and e["call"].get("dtmf") == last)["call"]["id"] == cid, "dtmf kept")
        t.ok("hangup", call_id=cid)
        t.call(cid, "ended")

    @case
    def sim_contacts():
        r = t.rpc("phonebook.read")
        expect(not r["ok"] and "--debug" in r["err"], r)
        ctl.Debug(True, True)
        try:
            book = t.ok("phonebook.read")["contacts"]
            want = [(c["name"], c["number"]) for c in NET["sim_contacts"]]
            expect([(c["name"], c["number"]) for c in book] == want, book)
            expect([c["index"] for c in book] == list(range(1, len(want) + 1)), book)
            expect(ctl.Charset() == "GSM", "the character set back to GSM: " + ctl.Charset())
            expect('Command AT+CSCS="UCS2"' in w.log() and w.log()[-1] == 'Command AT+CSCS="GSM"', w.log())
            ctl.Debug(True, False)
            expect(t.ok("phonebook.read")["contacts"] == [], "an empty phone book")
        finally:
            ctl.Debug(False, True)

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
        before = len(w.log())
        t = w.restart_tel()
        again = until(lambda: pending(lambda e: e["key"] == s["id"]), lambda m: m, "the same message after the restart")
        expect(again[0]["sms"]["text"] == "Keep me", again)
        t.ok("sms.ack", keys=[s["id"]])
        until(lambda: w.log(), lambda log: "Delete Keep me" in log, "the modem's copy deleted")
        # The bearer up with the SIM's APN stays across the restart; the
        # modem's attach APN is the SIM's already.
        until(lambda: t.ok("status"), lambda st: st["data_state"] == "connected" and st["data_apn"] == "cmnet", "the data link")
        time.sleep(0.3)
        expect(not [e for e in w.log()[before:] if e.startswith(("Disconnect", "Connect", "SetInitialEpsBearerSettings"))], w.log()[before:])

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

    # On 4G a call needs IMS (VoLTE) or a fallback to 2G or 3G. Without
    # IMS, one the network ends before it rings (the mock: a number ending
    # 404) or the modem will not start fails with the registration the IMS
    # program says, which the screens show; with IMS registered, on 2G, or
    # once it rang, its end is what it was.
    @case
    def volte_not_registered():
        w.ims("not-registered")
        cid = t.ok("dial", number="10086404")["call_id"]
        c = ended_failed(cid, "the network ended the call before it rang")
        expect(c.get("ims") == "not-registered", c)
        ctl.Refuse("Start", "Couldn't start call: QMI protocol error (48): 'InvalidOperation'", True)
        cid = t.ok("dial", number="10010")["call_id"]
        c = ended_failed(cid, "Couldn't start call")
        expect(c.get("ims") == "not-registered", c)
        cid = t.ok("dial", number="10010")["call_id"]
        t.call(cid, "alerting")
        ctl.Remote(w.mm_calls()[0], "hangup")
        c = t.call(cid, "ended")
        expect(c["reason"] == "remote" and "ims" not in c, c)
        until(lambda: w.mm_calls(), lambda calls: not calls, "the call deleted")
        w.ims("registered")
        cid = t.ok("dial", number="10086404")["call_id"]
        c = t.call(cid, "ended")
        expect(c["reason"] == "remote" and "ims" not in c, c)
        until(lambda: w.mm_calls(), lambda calls: not calls, "the call deleted")
        w.ims("not-registered")
        ctl.Radio(90, GSM)
        t.status(lambda s: s["rat"] == "2G", "on 2G")
        cid = t.ok("dial", number="10086404")["call_id"]
        c = t.call(cid, "ended")
        expect(c["reason"] == "remote" and "ims" not in c, c)
        until(lambda: w.mm_calls(), lambda calls: not calls, "the call deleted")
        os.remove(os.path.join(w.tmp, "ims"))
        ctl.Radio(90, LTE)
        t.status(lambda s: s["rat"] == "4G", "4G again")

    # The status says what the IMS program last said while registered on
    # 4G or 5G (the status bar's VoLTE mark and Settings show it): asked
    # when the access technology changes, and again while registering;
    # unknown when the program fails, and off 4G and 5G.
    @case
    def volte_status():
        expect(t.ok("status")["ims"] == "unknown", "the program fails")
        w.ims("registering")
        ctl.Radio(90, NR | LTE)
        st = t.status(lambda s: s["ims"] == "registering", "registering")
        expect(st["rat"] == "5G" and t.ok("status")["ims"] == "registering", st)
        w.ims("registered")
        t.status(lambda s: s["ims"] == "registered", "registered, asked again", timeout=10)
        ctl.Radio(90, GSM)
        st = t.status(lambda s: s["rat"] == "2G", "on 2G")
        expect(st["ims"] == "unknown", st)
        ctl.Radio(90, LTE)
        t.status(lambda s: s["rat"] == "4G" and s["ims"] == "registered", "registered on 4G")
        os.remove(os.path.join(w.tmp, "ims"))
        ctl.Radio(90, NR | LTE)
        t.status(lambda s: s["rat"] == "5G" and s["ims"] == "unknown", "unknown once the program fails")
        ctl.Radio(90, LTE)
        t.status(lambda s: s["rat"] == "4G", "4G again")

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
        sos = t.ok("dial", number="911")["call_id"]
        t.call(sos, "dialing")
        t.ok("hangup", call_id=sos)
        t.call(sos, "ended")
        expect(not t.rpc("sim.unlock", pin="12").get("ok"), "a two-digit PIN")
        t.ok("sim.unlock", pin="0000")
        ev = t.wait("unlock", lambda e: e.get("event") == "unlock")
        expect(not ev["ok"] and "Incorrect" in ev["err"], ev)
        expect(t.ok("status")["sim_tries"] == 2, t.ok("status"))
        t.ok("sim.unlock", pin="1234")
        expect(t.wait("unlock", lambda e: e.get("event") == "unlock")["ok"], "unlocked")
        st = t.status(lambda s: s["registered"], "registered after the PIN")
        expect(st["sim"] == "ready" and st["sim_tries"] == 0, st)
        st = t.status(lambda s: s.get("emergency_numbers") == ["000"], "the SIM's emergency numbers")
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

    def connected(what, pred=lambda s: True):
        return until(lambda: t.ok("status"), lambda s: s["data_state"] == "connected" and "data_ip" in s and pred(s), what)

    def data_case(fn):
        """A data case leaves the data link as it found it (at home, the
        switches on, the database's APNs, no MMS link), also when it fails."""
        def run():
            try:
                fn()
            finally:
                ctl.Register(False, NET["sim_mcc"] + NET["sim_mnc"])
                for method, params in (("roaming.set", {"on": False, "national": True}), ("apn.set", {"op": "reset"}),
                                       ("mms.disconnect", {}), ("data.set", {"on": True})):
                    t.rpc(method, **params)
        run.__name__ = fn.__name__
        return case(run)

    # The SIM is China Mobile's (46000): the database's cmnet, IPv4 and
    # IPv6, through Simple.Connect; the bearer's addresses in the status.
    @data_case
    def mobile_data():
        st = connected("the data link up")
        expect(st["data"] and "wwan0" in st["data_ifaces"] and "cdc-wdm0" not in st["data_ifaces"], st)
        expect(st["data_apn"] == "cmnet" and st["sim_mnc"] == NET["sim_mnc"] and st["spn"] == NET["spn"], st)
        # The first bearer at start-up already has the SIM's APN.
        connects = [e for e in w.log() if e.startswith("Connect")]
        expect(connects and connects[0] == "Connect apn=cmnet ip-type=4 apn-type=2 allow-roaming=0", connects)
        ip = st["data_ip"]
        expect(ip["iface"] == "wwan0" and ip["mtu"] == 1400, ip)
        expect(ip["ipv4"] == {"method": "static", "address": "100.71.32.5", "prefix": 30, "gateway": "100.71.32.6",
                              "dns": ["100.71.0.53", "100.71.0.54"]}, ip)
        expect(ip["ipv6"] == {"method": "static", "address": "2001:db8:4600:71::5", "prefix": 64, "dns": ["2001:db8:4600::53"]}, ip)
        t.ok("data.set", on=False)
        st = until(lambda: t.ok("status"), lambda s: s["data_state"] == "off", "the data link down")
        until(lambda: w.log(), lambda log: "Disconnect cmnet" in log, "Simple.Disconnect of the data bearer")
        expect(st["registered"] and not st["data"] and "wwan0" in st["data_ifaces"] and "data_ip" not in st, st)
        t.ok("data.set", on=True)
        connected("the data link up again")

    def ims_bearers():
        """VoLTE's IMS bearers as 81voltd brings them up (an APN, one IP
        family each, a profile, no apn-type) and one ModemManager typed IMS."""
        return [str(ctl.AddBearer({"apn": "ims", "ip-type": dbus.UInt32(1), "profile-id": dbus.Int32(2)}, "wwan1")),
                str(ctl.AddBearer({"apn": "ims", "ip-type": dbus.UInt32(2), "apn-type": dbus.UInt32(4)}, "wwan3"))]

    def all_up(paths):
        return all(ctl.BearerConnected(p) for p in paths)

    # The IMS bearers are not the data link and stay up whatever mobile
    # data does: off (only the data bearer goes), on again (a data bearer
    # connects beside them), telephonyd restarting beside them.
    @data_case
    def ims_bearer_left_alone():
        nonlocal t
        connected("the data link")
        ims = ims_bearers()
        try:
            time.sleep(0.3)
            st = connected("the data link beside IMS")
            expect(st["data_ip"]["iface"] == "wwan0" and st["data_apn"] == "cmnet", st)
            expect("wwan1" not in st["data_ifaces"] and "wwan3" not in st["data_ifaces"], st["data_ifaces"])
            before = len(w.log())
            t.ok("data.set", on=False)
            until(lambda: t.ok("status"), lambda s: s["data_state"] == "off", "the data link down")
            until(lambda: w.log()[before:], lambda log: "Disconnect cmnet" in log, "the data bearer disconnected")
            time.sleep(0.3)
            expect(all_up(ims) and "Disconnect all" not in w.log() and w.log()[before:] == ["Disconnect cmnet"], w.log()[before:])
            t.ok("data.set", on=True)
            st = connected("a data bearer beside IMS", lambda s: s["data_ip"]["iface"] == "wwan0")
            expect(w.log()[-1] == "Connect apn=cmnet ip-type=4 apn-type=2 allow-roaming=0" and all_up(ims), w.log()[before:])
            before = len(w.log())
            t = w.restart_tel()
            st = connected("the data link after a restart beside IMS")
            time.sleep(0.3)
            st = t.ok("status")
            expect(st["data_ip"]["iface"] == "wwan0" and "wwan1" not in st["data_ifaces"] and all_up(ims), st)
            expect(not [e for e in since(before) if e.startswith(("Disconnect", "Connect", "SetInitialEpsBearerSettings"))], since(before))
        finally:
            for p in ims:
                ctl.DropBearer(p)

    def since(n):
        return w.log()[n:]

    def in_order(log, *want):
        i = 0
        for e in log:
            if i < len(want) and e == want[i]:
                i += 1
        return i == len(want)

    # Settings › Access point names: a changed APN connects again with it
    # (IPv4 only now), the database's comes back with reset; so does the
    # APN in use with only its IP type or its authentication changed. The
    # APN is the attach one (type ia) too: the modem attaches anew with it
    # first; a new name alone changes nothing.
    @data_case
    def apn_change():
        apns = t.ok("apn.list")
        expect(apns["carrier"] == NET["operator"] and apns["selected"] == "cmnet" and apns["mcc"] + apns["mnc"] == "46000", apns)
        cmnet = next(a for a in apns["apns"] if a["id"] == "cmnet")
        expect("password" not in cmnet and not cmnet["password_set"], cmnet)

        def changed(apn, what, attach, connect, pred=lambda s: True):
            n = len(w.log())
            t.ok("apn.set", **apn)
            until(lambda: w.log(), lambda log: log[-1] == connect and log[n:].count(connect) == 1, what)
            st = connected(what, pred)
            expect(in_order(since(n), attach, connect) and "Disconnect all" not in since(n), since(n))
            return st

        cmnet.update(apn="cmnet.test", ip_type="ipv4")
        st = changed({"op": "save", "apn": cmnet}, "the changed APN", "SetInitialEpsBearerSettings apn=cmnet.test ip-type=1",
                     "Connect apn=cmnet.test ip-type=1 apn-type=2 allow-roaming=0", lambda s: s["data_apn"] == "cmnet.test")
        expect("ipv6" not in st["data_ip"] and next(a for a in t.ok("apn.list")["apns"] if a["id"] == "cmnet")["edited"], st)
        changed({"op": "reset"}, "the database's APN again", "SetInitialEpsBearerSettings apn=cmnet ip-type=4",
                "Connect apn=cmnet ip-type=4 apn-type=2 allow-roaming=0", lambda s: s["data_apn"] == "cmnet" and "ipv6" in s["data_ip"])
        cmnet = next(a for a in t.ok("apn.list")["apns"] if a["id"] == "cmnet")
        cmnet.update(ip_type="ipv4")
        changed({"op": "save", "apn": cmnet}, "the same APN, IPv4 only", "SetInitialEpsBearerSettings apn=cmnet ip-type=1",
                "Connect apn=cmnet ip-type=1 apn-type=2 allow-roaming=0", lambda s: s["data_apn"] == "cmnet" and "ipv6" not in s["data_ip"])
        cmnet.update(auth="chap", user="kario", password="secret")
        chap = "Connect apn=cmnet ip-type=1 apn-type=2 allow-roaming=0 auth=4 user=kario"
        changed({"op": "save", "apn": cmnet}, "with CHAP", "SetInitialEpsBearerSettings apn=cmnet ip-type=1 auth=4 user=kario", chap)
        n = len(w.log())
        cmnet.update(name="CMNET (CHAP)")
        t.ok("apn.set", op="save", apn=cmnet)
        time.sleep(0.3)
        expect(not since(n), "a new name alone connected or attached again: %s" % since(n))
        changed({"op": "reset"}, "the database's APN once more", "SetInitialEpsBearerSettings apn=cmnet ip-type=4",
                "Connect apn=cmnet ip-type=4 apn-type=2 allow-roaming=0", lambda s: "ipv6" in s["data_ip"])

    # Data roaming: off, no bearer on another country's network (the one up
    # goes); on, connected allowing roaming; national roaming has its own
    # switch, on by default.
    @data_case
    def data_roaming():
        ctl.Register(True, "26202")
        st = until(lambda: t.ok("status"), lambda s: s["data_state"] == "roaming", "no data abroad")
        expect(st["roaming"] == "international" and st["registered"] and not st["data_roaming"] and "data_ip" not in st, st)
        time.sleep(0.3)
        expect(not w.log()[-1].startswith("Connect"), w.log()[-3:])
        t.ok("roaming.set", on=True)
        connected("data roaming on", lambda s: s["data_roaming"])
        expect(w.log()[-1] == "Connect apn=cmnet ip-type=4 apn-type=2 allow-roaming=1", w.log()[-3:])
        t.ok("roaming.set", on=False)
        until(lambda: t.ok("status"), lambda s: s["data_state"] == "roaming", "data roaming off")
        ctl.Register(True, "46001")
        st = connected("national roaming", lambda s: s["roaming"] == "national" and s["national_roaming"])
        expect(w.log()[-1] == "Connect apn=cmnet ip-type=4 apn-type=2 allow-roaming=1", w.log()[-3:])
        t.ok("roaming.set", on=False, national=False)
        st = until(lambda: t.ok("status"), lambda s: s["data_state"] == "roaming", "national roaming off")
        expect(st["roaming"] == "national" and not st["national_roaming"] and "data_ip" not in st, st)
        until(lambda: w.log(), lambda log: log[-1] == "Disconnect cmnet", "the national bearer down")
        t.ok("roaming.set", on=False, national=True)
        connected("national roaming on again", lambda s: s["national_roaming"])
        expect(w.log()[-1] == "Connect apn=cmnet ip-type=4 apn-type=2 allow-roaming=1", w.log()[-3:])
        ctl.Register(False, NET["sim_mcc"] + NET["sim_mnc"])
        until(lambda: t.ok("status"), lambda s: s["roaming"] == "" and s["data_state"] == "connected", "home again")

    # MMS on China Mobile's own access point (cmwap beside cmnet), as
    # phone-smsd sends one: mms.connect brings up a second bearer (apn-type
    # mms, on its own interface) and answers with its addresses, a second
    # transfer meanwhile gets the same one, mms.disconnect lets it go. The
    # data link and IMS stay up throughout; with mobile data off there is
    # no MMS link. Where the MMS APN is the internet one, MMS goes over the
    # data link (shared).
    @data_case
    def mms_bearer_around_a_send():
        mms = "Connect apn=cmwap ip-type=4 apn-type=8 allow-roaming=0"
        connected("the data link")
        ims = ims_bearers()
        try:
            n = len(w.log())
            link = t.ok("mms.connect")
            expect(link["shared"] is False and link["iface"] == "wwan2" and link["mtu"] == 1400, link)
            expect(link["ipv4"] == {"method": "static", "address": "10.120.5.9", "prefix": 30, "gateway": "10.120.5.10", "dns": ["10.120.0.53"]}, link)
            expect(since(n) == [mms], since(n))
            st = t.ok("status")
            expect(st["mms_link"]["iface"] == "wwan2" and st["data_ip"]["iface"] == "wwan0" and st["data_apn"] == "cmnet", st)
            expect("wwan2" in st["data_ifaces"] and "wwan1" not in st["data_ifaces"], st["data_ifaces"])
            expect(t.ok("mms.connect") == link and since(n) == [mms], since(n))
            t.ok("mms.disconnect")
            until(lambda: w.log(), lambda log: log[-1] == "Disconnect cmwap", "the MMS bearer down")
            st = until(lambda: t.ok("status"), lambda s: "mms_link" not in s, "no MMS link")
            expect(st["data_state"] == "connected" and st["data_ip"]["iface"] == "wwan0" and all_up(ims), st)
            expect(since(n) == [mms, "Disconnect cmwap"], since(n))

            expect(t.ok("mms.connect")["iface"] == "wwan2", "the MMS link again")
            t.ok("data.set", on=False)
            until(lambda: w.log()[n:], lambda log: log.count("Disconnect cmwap") == 2 and "Disconnect cmnet" in log, "both bearers down")
            r = t.rpc("mms.connect")
            expect(not r["ok"] and r["err"] == "mobile data is off", r)
            expect(all_up(ims) and "Disconnect all" not in w.log(), since(n))
            t.ok("data.set", on=True)
            connected("the data link again")

            cmwap = next(a for a in t.ok("apn.list")["apns"] if a["id"] == "cmwap")
            cmwap["apn"] = "CMNET"
            t.ok("apn.set", op="save", apn=cmwap)
            n = len(w.log())
            expect(t.ok("mms.connect") == {"shared": True}, "MMS over the data link")
            t.ok("mms.disconnect")
            t.ok("apn.set", op="reset")
            time.sleep(0.3)
            expect(not [e for e in since(n) if e.startswith(("Connect", "Disconnect", "SetInitialEpsBearerSettings"))], since(n))
        finally:
            for p in ims:
                ctl.DropBearer(p)

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

    # ModemManager after telephonyd (the 6T's boot): a dial says what it
    # waits for, and once the modem is there a call rings and is answered.
    @case
    def modemmanager_late():
        w.stop_mock()
        t.status(lambda s: not s["registered"], "ModemManager gone")
        r = t.rpc("dial", number="10010")
        expect(not r["ok"] and r["err"] == "ModemManager is not running", r)
        w.start_mock()
        late = w.control()
        late.RemoveModem()
        time.sleep(0.5)
        r = t.rpc("dial", number="10010")
        expect(not r["ok"] and r["err"] == "ModemManager has no modem yet", r)
        late.AddModem()
        t.status(lambda s: s["registered"], "the modem up")
        late.Ring("13900007777")
        c = t.call(None, "incoming")
        t.ok("answer", call_id=c["id"])
        t.call(c["id"], "active")
        t.ok("hangup", call_id=c["id"])
        t.call(c["id"], "ended")

    @case
    def errors():
        expect(not t.rpc("answer", call_id="c999").get("ok"), "answer unknown")
        expect(not t.rpc("dial", number="12").get("ok"), "invalid number")
        expect(not t.rpc("sms.send", to="10010", text="").get("ok"), "empty SMS")
        r = t.rpc("sim.ring", number="10010")
        expect(not r.get("ok") and "not available" in r.get("err", ""), r)
        r = t.rpc("sim.ussd", text="notice", request=False)
        expect(not r.get("ok") and "not available" in r.get("err", ""), r)

    # Last: every status of the cases above kept what the Shell relies on
    # whatever the backend (docs/24-sku-framework.md §8.8).
    @case
    def backend_contract():
        expect(not w.broken, "; ".join(w.broken))

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
