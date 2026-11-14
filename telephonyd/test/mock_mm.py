#!/usr/bin/env python3
"""A stand-in for ModemManager on a private bus, for mm_test.py.

It owns org.freedesktop.ModemManager1 and exports what telephonyd's backend
uses of ModemManager's D-Bus API (1.20+): the object manager with one modem
(Modem, Modem3gpp, Modem3gpp.Ussd, Voice, Messaging, CellBroadcast), Call, Sms
and Cbm objects with their properties and signals, the SIM's home network and
the network's time (Modem.Time). org.kario.MockModem on the root object lets the
test play the network: ring, answer or hang up remotely, deliver SMS, cell
broadcasts or USSD,
end a USSD session, change the signal, roam to another country's network,
take the modem away. Log() returns the
requests telephonyd made, in order. Its network is telephonyd's simulated one
(sim-network.json): the same operator, USSD menu and texts.
"""

import json
import os
import sys

import dbus
import dbus.mainloop.glib
import dbus.service
from gi.repository import GLib

MM = "org.freedesktop.ModemManager1"
ROOT = "/org/freedesktop/ModemManager1"
MODEM = MM + ".Modem"
GPP = MM + ".Modem.Modem3gpp"
USSD = MM + ".Modem.Modem3gpp.Ussd"
VOICE = MM + ".Modem.Voice"
MESSAGING = MM + ".Modem.Messaging"
CELLBROADCAST = MM + ".Modem.CellBroadcast"
TIME = MM + ".Modem.Time"
SIMPLE = MM + ".Modem.Simple"
BEARER = MM + ".Bearer"
CALL = MM + ".Call"
SMS = MM + ".Sms"
CBM = MM + ".Cbm"
SIM = MM + ".Sim"
PROPS = "org.freedesktop.DBus.Properties"
OM = "org.freedesktop.DBus.ObjectManager"
CTL = "org.kario.MockModem"

DIALING, RINGING_OUT, RINGING_IN, ACTIVE, HELD, WAITING, TERMINATED = 1, 2, 3, 4, 5, 6, 7
R_OUTGOING, R_INCOMING, R_ACCEPTED, R_TERMINATED, R_BUSY = 1, 2, 3, 4, 5
LOCKED, DISABLED, ENABLING, REGISTERED, CONNECTED = 2, 3, 5, 8, 11
POWER_LOW, POWER_ON = 2, 3
PORT_NET, PORT_QMI = 2, 6
LOCK_NONE, LOCK_PIN, LOCK_PUK = 1, 2, 4
LTE = 1 << 14
USSD_IDLE, USSD_ACTIVE, USSD_USER_RESPONSE = 1, 2, 3

with open(os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "sim-network.json"), encoding="utf-8") as f:
    NET = json.load(f)

log = []


# The network's time as Modem.Time gives it (NITZ).
NETWORK_TIME = "2026-11-09T10:20:30+08:00"


def zone(minutes):
    return dbus.Dictionary({"offset": dbus.Int32(minutes), "dst-offset": dbus.Int32(0), "leap-seconds": dbus.Int32(0)},
                           signature="sv")


class Failed(dbus.DBusException):
    _dbus_error_name = MM + ".Error.Core.Failed"


class WrongState(dbus.DBusException):
    _dbus_error_name = MM + ".Error.Core.WrongState"


class IncorrectPassword(dbus.DBusException):
    _dbus_error_name = MM + ".Error.MobileEquipment.IncorrectPassword"


class Obj(dbus.service.Object):
    def __init__(self, bus, path, props):
        super().__init__(bus, path)
        self.path = dbus.ObjectPath(path)
        self.props = props

    @dbus.service.method(PROPS, in_signature="ss", out_signature="v")
    def Get(self, iface, name):
        return self.props[iface][name]

    @dbus.service.method(PROPS, in_signature="s", out_signature="a{sv}")
    def GetAll(self, iface):
        return dbus.Dictionary(self.props.get(iface, {}), signature="sv")

    @dbus.service.signal(PROPS, signature="sa{sv}as")
    def PropertiesChanged(self, iface, changed, invalidated):
        pass

    def set(self, iface, **changed):
        self.props[iface].update(changed)
        self.PropertiesChanged(iface, dbus.Dictionary(changed, signature="sv"), dbus.Array([], signature="s"))


class CallObj(Obj):
    def __init__(self, bus, path, modem, number, direction, state):
        super().__init__(bus, path, {CALL: {
            "State": dbus.Int32(state), "StateReason": dbus.Int32(0), "Direction": dbus.Int32(direction),
            "Number": dbus.String(number), "Multiparty": dbus.Boolean(False),
        }})
        self.modem = modem

    @property
    def state(self):
        return int(self.props[CALL]["State"])

    def to(self, new, reason):
        old = self.state
        self.set(CALL, State=dbus.Int32(new), StateReason=dbus.Int32(reason))
        self.StateChanged(dbus.Int32(old), dbus.Int32(new), dbus.UInt32(reason))

    @dbus.service.signal(CALL, signature="iiu")
    def StateChanged(self, old, new, reason):
        pass

    @dbus.service.method(CALL)
    def Start(self):
        log.append("Start " + self.props[CALL]["Number"])
        if self.state != 0:
            raise Failed("call already started")
        self.to(DIALING, R_OUTGOING)
        number = str(self.props[CALL]["Number"])
        GLib.timeout_add(100, lambda: self.state == DIALING and self.to(RINGING_OUT, R_OUTGOING) and False)
        if number.endswith("000"):
            GLib.timeout_add(300, lambda: self.state == RINGING_OUT and self.to(TERMINATED, R_BUSY) and False)

    @dbus.service.method(CALL)
    def Accept(self):
        log.append("Accept " + self.props[CALL]["Number"])
        if self.state != RINGING_IN:
            raise Failed("not ringing")
        self.to(ACTIVE, R_ACCEPTED)

    @dbus.service.method(CALL)
    def Hangup(self):
        log.append("Hangup " + self.props[CALL]["Number"])
        if self.state == TERMINATED:
            raise Failed("already ended")
        self.to(TERMINATED, R_TERMINATED)


class SmsObj(Obj):
    def __init__(self, bus, path, number, text, state, timestamp=""):
        super().__init__(bus, path, {SMS: {
            "State": dbus.UInt32(state), "PduType": dbus.UInt32(1 if state in (2, 3) else 2),
            "Number": dbus.String(number), "Text": dbus.String(text), "Timestamp": dbus.String(timestamp),
        }})

    @dbus.service.method(SMS)
    def Send(self):
        log.append("Send " + self.props[SMS]["Number"])
        number = str(self.props[SMS]["Number"])
        if number.endswith("999"):
            raise Failed("network rejected the message")
        self.set(SMS, State=dbus.UInt32(4))
        self.set(SMS, State=dbus.UInt32(5))
        # The recipient's phone took it (a status report); 998 never does.
        if self.props[SMS].get("DeliveryReportRequest") and not number.endswith("998"):
            GLib.timeout_add(200, lambda: self.set(SMS, DeliveryState=dbus.UInt32(0)) and False)


class CbmObj(Obj):
    def __init__(self, bus, path, channel, code, text):
        super().__init__(bus, path, {CBM: {
            "State": dbus.UInt32(2), "Text": dbus.String(text), "Channel": dbus.UInt32(channel),
            "MessageCode": dbus.UInt32(code), "Update": dbus.UInt32(0),
        }})


class BearerObj(Obj):
    """A data bearer Simple.Connect made, up on wwan0."""

    def __init__(self, bus, path):
        super().__init__(bus, path, {BEARER: {"Connected": dbus.Boolean(True), "Interface": dbus.String("wwan0")}})


class SimObj(Obj):
    """Asks for its PIN when locked: three tries, then the PUK 12345678 (ten
    tries). Its PIN is 1234 and its lock off until LockSim or EnablePin."""

    def __init__(self, bus, path, modem):
        super().__init__(bus, path, {SIM: {"SimIdentifier": dbus.String("89860000000000000000"),
                                           "OperatorIdentifier": dbus.String(NET["sim_mcc"] + "00")}})
        self.modem = modem
        self.pin = None
        self.code = "1234"

    def tries(self, lock, left):
        self.modem.set(MODEM, UnlockRequired=dbus.UInt32(lock),
                       UnlockRetries=dbus.Dictionary({dbus.UInt32(lock): dbus.UInt32(left)}, signature="uu"))

    def unlocked(self):
        self.pin = None
        self.modem.set(MODEM, UnlockRequired=dbus.UInt32(LOCK_NONE),
                       UnlockRetries=dbus.Dictionary({}, signature="uu"))
        self.modem.set(MODEM, State=dbus.Int32(DISABLED))

    @dbus.service.method(SIM, in_signature="s")
    def SendPin(self, pin):
        log.append("SendPin")
        lock = int(self.modem.props[MODEM]["UnlockRequired"])
        if lock != LOCK_PIN:
            raise Failed("no PIN asked for")
        if pin == self.pin:
            return self.unlocked()
        left = int(self.modem.props[MODEM]["UnlockRetries"][dbus.UInt32(LOCK_PIN)]) - 1
        if left:
            self.tries(LOCK_PIN, left)
        else:
            self.tries(LOCK_PUK, 10)
        raise IncorrectPassword("Incorrect password")

    @dbus.service.method(SIM, in_signature="ss")
    def SendPuk(self, puk, pin):
        log.append("SendPuk")
        if puk != "12345678":
            raise IncorrectPassword("Incorrect password")
        self.code = str(pin)
        self.unlocked()

    def facility(self, on):
        self.modem.set(GPP, EnabledFacilityLocks=dbus.UInt32(1 if on else 0))

    @dbus.service.method(SIM, in_signature="sb")
    def EnablePin(self, pin, enabled):
        log.append("EnablePin %s" % bool(enabled))
        if pin != self.code:
            raise IncorrectPassword("Incorrect password")
        self.facility(enabled)

    @dbus.service.method(SIM, in_signature="ss")
    def ChangePin(self, old, new):
        log.append("ChangePin")
        if not int(self.modem.props[GPP]["EnabledFacilityLocks"]) & 1:
            raise Failed("the PIN lock is off")
        if old != self.code:
            raise IncorrectPassword("Incorrect password")
        self.code = str(new)


class CellBroadcastIface(dbus.service.Object):
    """The modem's CellBroadcast interface. Its List, Delete and Added share
    their names with Messaging's: dbus-python finds a method by its Python
    name, then the interface, along the class hierarchy."""

    @dbus.service.method(CELLBROADCAST, in_signature="a(uu)")
    def SetChannels(self, channels):
        log.append("SetChannels " + ",".join("%d-%d" % (int(a), int(b)) for a, b in channels))
        self.set(CELLBROADCAST, Channels=dbus.Array(channels, signature="(uu)"))

    @dbus.service.method(CELLBROADCAST, out_signature="ao")
    def List(self):
        return dbus.Array(list(self.cbms), signature="o")

    @dbus.service.method(CELLBROADCAST, in_signature="o")
    def Delete(self, path):
        log.append("DeleteCbm " + str(self.cbms[path].props[CBM]["Text"]) if path in self.cbms else "DeleteCbm ?")
        if path not in self.cbms:
            raise Failed("no such message")
        self.cbms.pop(path).remove_from_connection()
        self.set(CELLBROADCAST, CellBroadcasts=dbus.Array(list(self.cbms), signature="o"))

    @dbus.service.signal(CELLBROADCAST, signature="o")
    def Added(self, path):
        pass


class ModemObj(Obj, CellBroadcastIface):
    def __init__(self, bus, path, root):
        super().__init__(bus, path, {
            MODEM: {"State": dbus.Int32(DISABLED), "SignalQuality": dbus.Struct((dbus.UInt32(0), True), signature="ub"),
                    "AccessTechnologies": dbus.UInt32(0), "OwnNumbers": dbus.Array(["+8613012345678"], signature="s"),
                    "Manufacturer": dbus.String("Kario mock"), "UnlockRequired": dbus.UInt32(LOCK_NONE),
                    "UnlockRetries": dbus.Dictionary({}, signature="uu"), "StateFailedReason": dbus.UInt32(0),
                    "Sim": dbus.ObjectPath(path.replace("/Modem/", "/SIM/")),
                    "EquipmentIdentifier": dbus.String("351234567890124"), "PowerState": dbus.UInt32(POWER_ON),
                    "Ports": dbus.Array([("cdc-wdm0", dbus.UInt32(PORT_QMI)), ("wwan0", dbus.UInt32(PORT_NET))], signature="(su)"),
                    "Bearers": dbus.Array([], signature="o")},
            GPP: {"RegistrationState": dbus.UInt32(0), "OperatorName": dbus.String(""), "EnabledFacilityLocks": dbus.UInt32(0)},
            USSD: {"State": dbus.UInt32(USSD_IDLE), "NetworkNotification": dbus.String(""),
                   "NetworkRequest": dbus.String("")},
            VOICE: {"Calls": dbus.Array([], signature="o"), "EmergencyOnly": dbus.Boolean(False)},
            MESSAGING: {"Messages": dbus.Array([], signature="o")},
            CELLBROADCAST: {"CellBroadcasts": dbus.Array([], signature="o"),
                            "Channels": dbus.Array([], signature="(uu)")},
            TIME: {"NetworkTimezone": zone(NET["nitz_offset"])},
        })
        self.root = root
        self.calls = {}
        self.sms = {}
        self.cbms = {}
        self.sim = SimObj(bus, path.replace("/Modem/", "/SIM/"), self)
        self.ussd = None  # the session waiting for Respond: None | "menu" | "network"
        self.bearers = {}

    def state(self):
        return int(self.props[MODEM]["State"])

    @dbus.service.method(MODEM, in_signature="b")
    def Enable(self, on):
        log.append("Enable %s" % bool(on))
        if not on:
            # Like ModemManager: disabling ends the calls and the bearers.
            for c in list(self.calls.values()):
                if c.state != TERMINATED:
                    c.to(TERMINATED, R_TERMINATED)
            self.disconnect()
            self.set(GPP, RegistrationState=dbus.UInt32(0), OperatorName=dbus.String(""))
            self.set(MODEM, State=dbus.Int32(DISABLED), AccessTechnologies=dbus.UInt32(0),
                     SignalQuality=dbus.Struct((dbus.UInt32(0), True), signature="ub"))
            return
        self.set(MODEM, State=dbus.Int32(ENABLING), PowerState=dbus.UInt32(POWER_ON))

        def registered():
            self.set(GPP, RegistrationState=dbus.UInt32(1), OperatorName=dbus.String(NET["operator"]),
                     OperatorCode=dbus.String(NET["sim_mcc"] + "00"))
            self.set(MODEM, State=dbus.Int32(REGISTERED), AccessTechnologies=dbus.UInt32(LTE),
                     SignalQuality=dbus.Struct((dbus.UInt32(70), True), signature="ub"))
            return False
        GLib.timeout_add(100, registered)

    @dbus.service.method(MODEM, in_signature="u")
    def SetPowerState(self, state):
        log.append("SetPowerState %d" % int(state))
        if self.state() > DISABLED:
            raise WrongState("the modem is not disabled")
        self.set(MODEM, PowerState=dbus.UInt32(state))

    @dbus.service.method(SIMPLE, in_signature="a{sv}", out_signature="o")
    def Connect(self, props):
        log.append("Connect apn=%s" % props.get("apn", "-"))
        if self.state() < REGISTERED:
            raise WrongState("not registered")
        path = "%s/Bearer/%d" % (ROOT, self.root.next_id())
        self.bearers[path] = BearerObj(self.connection, path)
        self.set(MODEM, Bearers=dbus.Array(list(self.bearers), signature="o"), State=dbus.Int32(CONNECTED))
        return dbus.ObjectPath(path)

    def disconnect(self):
        for b in self.bearers.values():
            if b.props[BEARER]["Connected"]:
                b.set(BEARER, Connected=dbus.Boolean(False))
        if self.state() == CONNECTED:
            self.set(MODEM, State=dbus.Int32(REGISTERED))

    @dbus.service.method(SIMPLE, in_signature="o")
    def Disconnect(self, path):
        log.append("Disconnect")
        self.disconnect()

    @dbus.service.method(TIME, out_signature="s")
    def GetNetworkTime(self):
        return NETWORK_TIME

    @dbus.service.signal(TIME, signature="s")
    def NetworkTimeChanged(self, time):
        pass

    def new_call(self, number, direction, state):
        path = "%s/Call/%d" % (ROOT, self.root.next_id())
        c = CallObj(self.connection, path, self, number, direction, state)
        self.calls[path] = c
        self.set(VOICE, Calls=dbus.Array(list(self.calls), signature="o"))
        self.CallAdded(c.path)
        return c

    def drop_call(self, path):
        c = self.calls.pop(path)
        c.remove_from_connection()
        self.set(VOICE, Calls=dbus.Array(list(self.calls), signature="o"))
        self.CallDeleted(c.path)

    @dbus.service.method(VOICE, in_signature="a{sv}", out_signature="o")
    def CreateCall(self, props):
        log.append("CreateCall " + props["number"])
        return self.new_call(str(props["number"]), 2, 0).path

    @dbus.service.method(VOICE, in_signature="o")
    def DeleteCall(self, path):
        log.append("DeleteCall")
        if path not in self.calls:
            raise Failed("no such call")
        self.drop_call(path)

    @dbus.service.method(VOICE, out_signature="ao")
    def ListCalls(self):
        return dbus.Array(list(self.calls), signature="o")

    @dbus.service.method(VOICE)
    def HoldAndAccept(self):
        log.append("HoldAndAccept")
        active = [c for c in self.calls.values() if c.state == ACTIVE]
        rest = [c for c in self.calls.values() if c.state == WAITING] + \
               [c for c in self.calls.values() if c.state == HELD]
        for c in active:
            c.to(HELD, R_ACCEPTED)
        if rest:
            rest[0].to(ACTIVE, R_ACCEPTED)

    @dbus.service.signal(VOICE, signature="o")
    def CallAdded(self, path):
        pass

    @dbus.service.signal(VOICE, signature="o")
    def CallDeleted(self, path):
        pass

    def new_sms(self, number, text, state, timestamp="", announce=True, received=False):
        path = "%s/SMS/%d" % (ROOT, self.root.next_id())
        s = SmsObj(self.connection, path, number, text, state, timestamp)
        self.sms[path] = s
        self.set(MESSAGING, Messages=dbus.Array(list(self.sms), signature="o"))
        if announce:
            self.Added(s.path, received)
        return s

    @dbus.service.method(MESSAGING, in_signature="a{sv}", out_signature="o")
    def Create(self, props):
        log.append("Create %s %s" % (props["number"], props["text"]))
        s = self.new_sms(str(props["number"]), str(props["text"]), 0)
        s.props[SMS]["DeliveryReportRequest"] = dbus.Boolean(bool(props.get("delivery-report-request", False)))
        return s.path

    @dbus.service.method(MESSAGING, in_signature="o")
    def Delete(self, path):
        log.append("Delete " + str(self.sms[path].props[SMS]["Text"]) if path in self.sms else "Delete ?")
        if path not in self.sms:
            raise Failed("no such message")
        self.sms.pop(path).remove_from_connection()
        self.set(MESSAGING, Messages=dbus.Array(list(self.sms), signature="o"))
        self.Deleted(dbus.ObjectPath(path))

    @dbus.service.method(MESSAGING, out_signature="ao")
    def List(self):
        return dbus.Array(list(self.sms), signature="o")

    @dbus.service.signal(MESSAGING, signature="ob")
    def Added(self, path, received):
        pass

    @dbus.service.signal(MESSAGING, signature="o")
    def Deleted(self, path):
        pass

    def new_cbm(self, channel, code, text):
        path = "%s/CBM/%d" % (ROOT, self.root.next_id())
        c = CbmObj(self.connection, path, channel, code, text)
        self.cbms[path] = c
        self.set(CELLBROADCAST, CellBroadcasts=dbus.Array(list(self.cbms), signature="o"))
        CellBroadcastIface.Added(self, c.path)

    def ussd_state(self):
        return int(self.props[USSD]["State"])

    def ussd_answer(self, text, session):
        """Like ModemManager: the session's State changes before the call returns."""
        self.ussd = session
        self.set(USSD, State=dbus.UInt32(USSD_USER_RESPONSE if session else USSD_IDLE))
        return dbus.String(text)

    @dbus.service.method(USSD, in_signature="s", out_signature="s")
    def Initiate(self, command):
        log.append("Initiate " + command)
        if self.ussd_state() != USSD_IDLE:
            raise WrongState("a USSD session is already active")
        self.set(USSD, State=dbus.UInt32(USSD_ACTIVE))
        if command in NET["ussd_answers"]:
            return self.ussd_answer(NET["ussd_answers"][command], None)
        if command == NET["ussd_menu_code"]:
            return self.ussd_answer(NET["ussd_menu"], "menu")
        return self.ussd_answer(NET["ussd_unknown"], None)

    @dbus.service.method(USSD, in_signature="s", out_signature="s")
    def Respond(self, response):
        log.append("Respond " + response)
        if self.ussd_state() != USSD_USER_RESPONSE:
            raise WrongState("the network is not waiting for a response")
        session = self.ussd
        self.set(USSD, State=dbus.UInt32(USSD_ACTIVE))
        if session == "network":
            return self.ussd_answer(NET["ussd_received"], None)
        if response in NET["ussd_menu_answers"]:
            return self.ussd_answer(NET["ussd_menu_answers"][response], None)
        return self.ussd_answer(NET["ussd_menu_retry"] + "\n" + NET["ussd_menu"], session)

    @dbus.service.method(USSD)
    def Cancel(self):
        log.append("Cancel")
        self.ussd = None
        self.set(USSD, State=dbus.UInt32(USSD_IDLE))


class Root(dbus.service.Object):
    def __init__(self, bus):
        super().__init__(bus, ROOT)
        self.bus = bus
        self.ids = 0
        self.modems = 0
        self.modem = None
        self.add_modem()

    def next_id(self):
        self.ids += 1
        return self.ids

    def add_modem(self):
        path = "%s/Modem/%d" % (ROOT, self.modems)
        self.modems += 1
        self.modem = ModemObj(self.bus, path, self)
        self.InterfacesAdded(self.modem.path, self.modem.props)

    @dbus.service.method(OM, out_signature="a{oa{sa{sv}}}")
    def GetManagedObjects(self):
        if not self.modem:
            return dbus.Dictionary({}, signature="oa{sa{sv}}")
        return dbus.Dictionary({self.modem.path: self.modem.props}, signature="oa{sa{sv}}")

    @dbus.service.signal(OM, signature="oa{sa{sv}}")
    def InterfacesAdded(self, path, ifaces):
        pass

    @dbus.service.signal(OM, signature="oas")
    def InterfacesRemoved(self, path, ifaces):
        pass

    def call(self, path):
        if path not in self.modem.calls:
            raise Failed("no such call " + path)
        return self.modem.calls[path]

    @dbus.service.method(CTL, in_signature="s", out_signature="o")
    def Ring(self, number):
        busy = any(c.state in (ACTIVE, HELD) for c in self.modem.calls.values())
        return self.modem.new_call(number, 1, WAITING if busy else RINGING_IN).path

    @dbus.service.method(CTL, in_signature="os")
    def Remote(self, path, what):
        c = self.call(path)
        if what == "answer":
            c.to(ACTIVE, R_ACCEPTED)
        elif what == "hangup":
            c.to(TERMINATED, R_TERMINATED)
        else:
            raise Failed("unknown remote action " + what)

    @dbus.service.method(CTL, out_signature="ao")
    def Calls(self):
        return dbus.Array(list(self.modem.calls), signature="o")

    @dbus.service.method(CTL, in_signature="o", out_signature="i")
    def CallState(self, path):
        return self.call(path).state

    @dbus.service.method(CTL, out_signature="ao")
    def Messages(self):
        return dbus.Array(list(self.modem.sms), signature="o")

    @dbus.service.method(CTL, in_signature="ssb")
    def Receive(self, number, text, multipart):
        ts = "2026-11-09T10:20:30+08:00"
        if not multipart:
            self.modem.new_sms(number, text, 3, ts, received=True)
            return
        half = len(text) // 2
        s = self.modem.new_sms(number, text[:half], 2, ts, received=True)

        def rest():
            s.set(SMS, Text=dbus.String(text), State=dbus.UInt32(3))
            return False
        GLib.timeout_add(200, rest)

    @dbus.service.method(CTL, in_signature="ss")
    def Store(self, number, text):
        self.modem.new_sms(number, text, 3, "2026-11-09T10:20:30+08:00", announce=False)

    @dbus.service.method(CTL, in_signature="uus")
    def Broadcast(self, channel, code, text):
        """A cell broadcast (an emergency alert) on a channel."""
        self.modem.new_cbm(channel, code, text)

    @dbus.service.method(CTL, out_signature="ao")
    def Broadcasts(self):
        return dbus.Array(list(self.modem.cbms), signature="o")

    @dbus.service.method(CTL, in_signature="sb")
    def UssdNetwork(self, text, request):
        """Network-initiated USSD: a notification, or a request waiting for Respond."""
        m = self.modem
        if not request:
            m.set(USSD, NetworkNotification=dbus.String(text))
            return
        m.ussd = "network"
        m.set(USSD, NetworkRequest=dbus.String(text), State=dbus.UInt32(USSD_USER_RESPONSE))

    @dbus.service.method(CTL)
    def UssdEnd(self):
        """The network releases the session (a timeout, say)."""
        m = self.modem
        m.ussd = None
        m.set(USSD, State=dbus.UInt32(USSD_IDLE))

    @dbus.service.method(CTL, in_signature="uu")
    def Radio(self, quality, access):
        self.modem.set(MODEM, SignalQuality=dbus.Struct((dbus.UInt32(quality), True), signature="ub"),
                       AccessTechnologies=dbus.UInt32(access))

    @dbus.service.method(CTL, in_signature="s")
    def LockSim(self, pin):
        """Like a restart with a PIN-protected SIM: locked, off the network."""
        m = self.modem
        m.sim.pin = m.sim.code = pin
        m.sim.facility(True)
        m.set(GPP, RegistrationState=dbus.UInt32(0), OperatorName=dbus.String(""))
        m.set(MODEM, State=dbus.Int32(LOCKED), AccessTechnologies=dbus.UInt32(0),
              SignalQuality=dbus.Struct((dbus.UInt32(0), True), signature="ub"))
        m.sim.tries(LOCK_PIN, 3)

    @dbus.service.method(CTL, in_signature="sis")
    def Roam(self, code, minutes, time):
        """On another country's network: its code (MCC and MNC), zone and time."""
        m = self.modem
        m.set(GPP, OperatorCode=dbus.String(code))
        m.set(TIME, NetworkTimezone=zone(minutes))
        m.NetworkTimeChanged(time)

    @dbus.service.method(CTL)
    def RemoveModem(self):
        m, self.modem = self.modem, None
        for path in list(m.calls):
            m.calls.pop(path).remove_from_connection()
        for path in list(m.sms):
            m.sms.pop(path).remove_from_connection()
        for path in list(m.cbms):
            m.cbms.pop(path).remove_from_connection()
        for path in list(m.bearers):
            m.bearers.pop(path).remove_from_connection()
        m.sim.remove_from_connection()
        self.InterfacesRemoved(m.path, dbus.Array(list(m.props), signature="s"))
        m.remove_from_connection()

    @dbus.service.method(CTL)
    def AddModem(self):
        self.add_modem()

    @dbus.service.method(CTL, out_signature="as")
    def Log(self):
        return dbus.Array(log, signature="s")


def main():
    dbus.mainloop.glib.DBusGMainLoop(set_as_default=True)
    bus = dbus.SystemBus()
    root = Root(bus)
    name = dbus.service.BusName(MM, bus)  # owned while referenced
    print("mock ModemManager ready", flush=True)
    GLib.MainLoop().run()


if __name__ == "__main__":
    sys.exit(main())
