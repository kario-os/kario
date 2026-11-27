// ModemManager's values (ModemManager-enums.h, the D-Bus API of 1.20+) in
// telephonyd's terms. Kept apart from the D-Bus code so the host tests cover
// them without libsystemd.
#pragma once

#include <cstdint>
#include <map>
#include <optional>
#include <string>
#include <string_view>
#include <variant>

#include "modem.hpp"

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
enum BearerApnType : std::uint32_t {
    ApnTypeInitial = 1u << 0, ApnTypeDefault = 1u << 1, ApnTypeIms = 1u << 2, ApnTypeMms = 1u << 3, ApnTypeEmergency = 1u << 6
};
enum BearerType : std::int64_t { BearerDefaultAttach = 2 };

// An a{sv} of integers and strings (Bearer.Ip4Config, Bearer.Properties,
// Time.NetworkTimezone); its other members are left out.
using Dict = std::map<std::string, std::variant<long long, std::string>>;

// "2G" / "3G" / "4G" / "5G" from Modem.AccessTechnologies, "" when unknown.
std::string rat(std::uint64_t access);
// Signal bars 0-4 from Modem.SignalQuality (percent).
int bars(std::int64_t quality);
// Whether Modem3gpp.RegistrationState means the network takes calls or SMS.
bool registered(std::int64_t reg);
// Whether it is a roaming registration (roaming, SMS only or CSFB not preferred).
bool roaming(std::int64_t reg);
// Simple.Connect's ip-type (MMBearerIpFamily) for an APN's IP type and
// allowed-auth (MMBearerAllowedAuth) for its authentication.
std::uint32_t ip_family(std::string_view ip_type);
std::uint32_t allowed_auth(std::string_view auth);
// Whether a bearer's Properties are what Simple.Connect asks for the APN:
// its APN and, where the bearer says, the IP type, the authentication and
// the user (Properties hold no password worth comparing).
bool bearer_matches(const Dict& props, const Apn& a);
// What a bearer telephonyd did not make is for. The LTE attach's
// (BearerType), else what its Properties' apn-type says (IMS or emergency
// first), else its APN: data for the internet APN, IMS for the plan's IMS
// and emergency APNs and the well-known ims and sos (GSMA IR.92, 3GPP TS
// 23.003), MMS for the plan's MMS APN; else other with a profile-id, data
// without. ModemManager leaves apn-type out where none was asked for, and
// 81voltd asks for none.
enum class BearerRole { Data, Ims, Mms, Attach, Other };
BearerRole bearer_role(const Dict& props, std::int64_t bearer_type, const ApnPlan& plan);
// Whether an existing APN is the one asked for: any case, and with the
// operator identifier (".mnc000.mcc460.gprs") some networks report after it.
bool apn_named(std::string_view asked, std::string_view existing);
// Whether Modem3gpp.InitialEpsBearerSettings, or the initial EPS bearer's
// Properties, are what SetInitialEpsBearerSettings asks for the attach
// APN: the APN (apn_named) and, where they say, the IP type, the
// authentication and the user.
bool eps_matches(const Dict& settings, const Apn& a);
// A bearer's Ip4Config or Ip6Config (MMBearerIpMethod; dns1 to dns3), none
// while its method is unknown (that family is not up); ip_mtu its "mtu", 0
// without.
std::optional<IpConfig> ip_config(const Dict& d);
int ip_mtu(const Dict& d);
long long dict_num(const Dict& d, const std::string& key, long long def = 0);
std::string dict_text(const Dict& d, const std::string& key);
// Status.sim from Modem.State, UnlockRequired and StateFailedReason.
std::string sim_state(std::int64_t state, std::int64_t lock, std::int64_t failed_reason);
// telephonyd's call state, "" for states it does not show (unknown).
std::string call_state(std::int64_t state);
// Why a call ended: hangup | rejected | busy | missed | failed | remote.
// `answering`: the user answered the incoming call, which then failed
// rather than went unanswered.
std::string end_reason(std::string_view dir, bool connected, bool hung_up, std::int64_t reason,
                       bool answering = false);
// Call.error for a call ModemManager ended with `reason`, "" for reasons
// that are not failures.
std::string end_error(std::int64_t reason);
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
