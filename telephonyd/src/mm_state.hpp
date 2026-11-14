// ModemManager's values (ModemManager-enums.h, the D-Bus API of 1.20+) in
// telephonyd's terms. Kept apart from the D-Bus code so the host tests cover
// them without libsystemd.
#pragma once

#include <cstdint>
#include <string>
#include <string_view>

namespace tel::mm {

enum ModemState : std::int64_t {
    ModemFailed = -1, ModemLocked = 2, ModemDisabled = 3, ModemDisabling = 4, ModemEnabling = 5, ModemEnabled = 6, ModemRegistered = 8
};
enum PowerState : std::int64_t { PowerLow = 2, PowerOn = 3 };
enum PortType : std::int64_t { PortNet = 2 };
enum ModemLock : std::int64_t { LockNone = 1, LockSimPin = 2, LockSimPuk = 4 };
enum FailedReason : std::int64_t { FailedSimMissing = 2, FailedSimError = 3 };
enum CallState : std::int64_t {
    CallUnknown = 0, CallDialing, CallRingingOut, CallRingingIn, CallActive, CallHeld, CallWaiting, CallTerminated
};
enum CallReason : std::int64_t { ReasonRefusedOrBusy = 5, ReasonError = 6, ReasonAudioSetupFailed = 7 };
enum CallDirection : std::int64_t { DirIncoming = 1, DirOutgoing = 2 };
enum SmsState : std::int64_t { SmsReceiving = 2, SmsReceived = 3 };
enum SmsPduType : std::int64_t { PduStatusReport = 3 };
enum CbmState : std::int64_t { CbmReceiving = 1, CbmReceived = 2 };
enum UssdState : std::int64_t { UssdIdle = 1, UssdActive = 2, UssdUserResponse = 3 };

// "2G" / "3G" / "4G" / "5G" from Modem.AccessTechnologies, "" when unknown.
std::string rat(std::uint64_t access);
// Signal bars 0-4 from Modem.SignalQuality (percent).
int bars(std::int64_t quality);
// Whether Modem3gpp.RegistrationState means the network takes calls or SMS.
bool registered(std::int64_t reg);
// Status.sim from Modem.State, UnlockRequired and StateFailedReason.
std::string sim_state(std::int64_t state, std::int64_t lock, std::int64_t failed_reason);
// telephonyd's call state, "" for states it does not show (unknown).
std::string call_state(std::int64_t state);
// Why a call ended: hangup | rejected | busy | missed | failed | remote.
std::string end_reason(std::string_view dir, bool connected, bool hung_up, std::int64_t reason);
// Unix seconds from an ISO 8601 time with an optional offset (Sms.Timestamp,
// Time.GetNetworkTime); `fallback` when it does not parse.
long long sms_time(std::string_view iso, long long fallback);
// An outgoing message's state from its delivery report (Sms.DeliveryState):
// delivered, failed, or "" while the network still tries.
std::string delivery(std::int64_t state);
// An id for a message from what it says: ModemManager numbers its objects
// anew when it or telephonyd restarts, and phone-smsd must recognise a
// message it already stored when the modem reports it again.
std::string stable_id(char prefix, std::string_view what);

}  // namespace tel::mm
