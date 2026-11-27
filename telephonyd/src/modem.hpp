// What telephonyd needs from the baseband. SimModem implements it for
// development and the boot selftest; a ModemManager backend (D-Bus
// org.freedesktop.ModemManager1) implements the same interface on hardware.
#pragma once

#include <expected>
#include <functional>
#include <map>
#include <optional>
#include <string>
#include <string_view>
#include <utility>
#include <vector>

#include "apn.hpp"
#include "json.hpp"
#include "loop.hpp"

namespace tel {

// The most DTMF tones a call keeps, the last ones sent: a voice menu asks
// one call for a card number (19 digits), an ID number (18) and a PIN at most.
inline constexpr size_t kMaxDtmf = 64;

struct Call {
    std::string id, number;
    std::string dir;    // "in" | "out"
    std::string state;  // dialing | alerting | incoming | active | held | ended
    std::string reason; // why it ended: hangup | remote | busy | rejected | missed | failed | dropped
    std::string error;  // with reason failed: what the modem or ModemManager said, "" when nothing
    // With reason failed: the modem's IMS registration the call's end is
    // put down to (ims_blame), "" when none.
    std::string ims;
    long long started = 0, connected = 0, ended = 0;  // unix seconds
    bool muted = false;
    bool emergency = false;  // an emergency number: placed even with the radio off
    std::string dtmf;        // the last kMaxDtmf tones sent during the call (voice menus)
    void add_dtmf(std::string_view tones);
    json::Value to_json() const;
};

struct Sms {
    std::string id, peer;
    std::string dir;  // "in" | "out"
    std::string text;
    long long at = 0;
    bool read = false;
    // outgoing: sending | sent | delivered (the recipient's phone took it:
    // a delivery report) | failed; a later event with the same id updates it
    std::string state;
    // An 8-bit message's user data, hex (a WAP push: phone-smsd reads it),
    // and the message class the network gave (0: flash, shown at once),
    // -1 for none.
    std::string data;
    int sms_class = -1;
    json::Value to_json() const;
};

// A cell broadcast: an emergency alert the network sends every phone in an
// area. channel is its message identifier (3GPP TS 23.041: 4352-4359
// ETWS, 4370-4400 CMAS / EU-Alert, the level phone-smsd reads from it),
// serial its serial number (message code and update number: a repeat of
// the same alert keeps it).
struct Cb {
    std::string id;
    int channel = 0, serial = 0;
    std::string text;
    long long at = 0;
    std::string wac;  // warning area coordinates (WEA 3.0), hex; "" none
    json::Value to_json() const;
};

// What the network said about its time (NITZ): its zone's offset from UTC
// in minutes, daylight saving included, and the daylight saving part; its
// time when that came and the phone's clock then (Unix seconds, 0 when it
// sent only its zone).
struct Nitz {
    int offset = 0, dst = 0;
    long long time = 0, received = 0;
    json::Value to_json() const;
};

// One address family of the data link as the network gave it: static (the
// address, its prefix and the gateway are these) or dhcp (the interface
// asks: DHCP for IPv4, router advertisements for IPv6); ppp is a link
// nothing here configures.
struct IpConfig {
    std::string method;  // static | dhcp | ppp
    std::string address, gateway;
    int prefix = 0;
    std::vector<std::string> dns;
    bool operator==(const IpConfig&) const = default;
    json::Value to_json() const;
};

// The connected data link's interface and its addressing.
struct DataLink {
    std::string iface;
    int mtu = 0;  // 0: the interface's own
    std::optional<IpConfig> ipv4, ipv6;
    bool operator==(const DataLink&) const = default;
    json::Value to_json() const;
};

struct Status {
    bool registered = false;
    std::string op, rat, own, backend;
    int signal = 0;  // bars, 0-4
    std::string sim = "ready";  // ready | pin | puk (asks for it) | locked (by the operator) | absent | error
    int sim_tries = 0;          // PIN or PUK attempts left, 0 when unknown
    bool pin_lock = false;      // the SIM asks for its PIN when the phone starts
    std::string imei;           // the modem's, "" until it is known
    // Mobile country and network codes: the SIM's home network's and the
    // network's the phone is on (roaming, they differ), "" while unknown;
    // the SIM's service provider name and GID1 (hex), which tell virtual
    // operators apart.
    std::string sim_mcc, net_mcc, sim_mnc, net_mnc, spn, gid1;
    // roaming_kind of the registration: "" | national | international.
    std::string roaming;
    std::optional<Nitz> nitz;   // the zone and time the network last sent
    // The radio (off: airplane mode) and the cellular data link: its
    // switch, the data roaming switch (other countries' networks), the
    // national roaming switch (other networks of the SIM's country), its
    // state (connected | idle: on, no link yet | roaming: roaming where a
    // switch keeps the link down | off) and the network interfaces it can
    // use, which the shell's firewall closes while the link may not be up.
    bool radio = true, data = true, data_roaming = false, national_roaming = true;
    std::string data_state = "off";
    std::vector<std::string> data_ifaces;
    // The access point the link uses ("" the network's default) and,
    // connected, its interface's addressing; the MMS access point's link
    // while it is up beside it (mms.connect), which the Shell routes only
    // what leaves from its address through.
    std::string data_apn;
    std::optional<DataLink> data_link, mms_link;
    // The emergency numbers the SIM and the network add to emergency_number's.
    std::vector<std::string> emergency;
    // The modem's IMS (VoLTE) registration as last read (ims.hpp):
    // registered | registering | not-registered | limited-registered, or
    // unknown: nothing reads it, the reading failed or has not come yet,
    // or the phone is not registered on 4G or 5G, where alone it is read.
    std::string ims = "unknown";
    json::Value to_json() const;
};

// A USSD message from the network.
struct Ussd {
    std::string state;  // done (the session is over) | input (the network waits for a reply) | notify (nothing to reply)
    std::string text;
    json::Value to_json() const;
};

// An entry of the SIM's phone book (EF_ADN, 3GPP TS 31.102): the contacts
// an older phone saved on the card. index is its record, from 1.
struct SimContact {
    int index = 0;
    std::string name, number;
    json::Value to_json() const;
};

using Result = std::expected<void, std::string>;
using UssdReply = std::function<void(std::expected<Ussd, std::string>)>;
using SimContactsReply = std::function<void(std::expected<std::vector<SimContact>, std::string>)>;
// The MMS access point's link, nothing when MMS goes over the data link,
// or why it cannot be up.
using MmsReply = std::function<void(std::expected<std::optional<DataLink>, std::string>)>;

class Modem {
public:
    struct Events {
        std::function<void(const Call&)> call;
        std::function<void(const Sms&)> sms;
        std::function<void(const Cb&)> cb;
        std::function<void(const Status&)> status;
        std::function<void(const std::string& error)> unlock;  // how a PIN / PUK attempt went, "" when it worked
        std::function<void(const std::string& error)> pin;     // how turning the PIN lock on or off, or changing the PIN, went
        // USSD the network sent by itself (notify, input), and the end of a
        // session waiting for a reply that the network closed (done).
        std::function<void(const Ussd&)> ussd;
    };
    virtual ~Modem() = default;
    virtual Status status() const = 0;
    virtual std::vector<Call> calls() const = 0;
    // With a call connected, it is put on hold first and the new one is a
    // second call (as ATD does in GSM); second_call says when that is refused.
    virtual std::expected<std::string, std::string> dial(std::string_view number) = 0;
    // Answering a call that waits holds the connected one.
    virtual Result answer(std::string_view id) = 0;
    virtual Result hangup(std::string_view id) = 0;
    // Resuming a held call while another is connected swaps them.
    virtual Result hold(std::string_view id, bool on) = 0;
    virtual Result mute(std::string_view id, bool on) = 0;
    // DTMF tones (valid_dtmf) into a connected call: a voice menu's choices.
    virtual Result send_dtmf(std::string_view id, std::string_view tones) = 0;
    // `id` names the message in its later events (phone-smsd's id); "" gets
    // a new one. A delivery report is asked for.
    virtual std::expected<std::string, std::string> send_sms(std::string_view to, std::string_view text, std::string_view id) = 0;
    // phone-smsd has stored a received message or cell broadcast: the
    // modem's copy (its storage holds the message until then) can go.
    virtual void ack_sms(std::string_view) {}
    virtual void ack_cb(std::string_view) {}
    // Enters the SIM's PIN, or with `puk` its PUK and a new PIN; the outcome is an unlock event.
    virtual Result unlock(std::string_view pin, std::string_view puk) = 0;
    // Turns the SIM's PIN lock on or off (`pin`: its PIN now), or changes the
    // PIN (the lock must be on); the outcome is a pin event. A wrong PIN uses
    // up the SIM's tries as at unlocking.
    virtual Result set_pin_lock(std::string_view pin, bool on) = 0;
    virtual Result change_pin(std::string_view pin, std::string_view new_pin) = 0;
    // Sends a USSD code, or the reply to the network's prompt; `done` gets the
    // network's answer, possibly after these return. One session at a time.
    virtual void ussd_initiate(std::string_view code, UssdReply done) = 0;
    virtual void ussd_respond(std::string_view text, UssdReply done) = 0;
    virtual Result ussd_cancel() = 0;
    // Reads the SIM's phone book; the SIM must be unlocked. `done` may run
    // after this returns.
    virtual void sim_contacts(SimContactsReply done) = 0;
    // Off powers the radio down (airplane mode) and ends the calls; it is
    // refused during an emergency call. Dialing an emergency number powers
    // it up again by itself. The data link needs the radio and the switch.
    virtual Result set_radio(bool on) = 0;
    virtual Result set_data(bool on) = 0;
    // The roaming switches (roaming_allowed): on off, no data link while
    // roaming_kind is international; national off, none while it is
    // national.
    virtual Result set_data_roaming(bool on, bool national) = 0;
    // The SIM's APNs (ApnList::plan): the data link uses the internet APN
    // and a link up with another one connects again; the modem attaches
    // to LTE with the attach APN. Until the first call the modem connects
    // no data link.
    virtual void set_apn(const ApnPlan& plan) = 0;
    // mms.connect: where the plan has an MMS APN of its own, a bearer on
    // it, up while the data link may be (the switches, roaming) and held
    // for three minutes from each call; `done` (possibly after this
    // returns) gets its link once it has addresses. mms_release lets it go.
    virtual void mms_connect(MmsReply done) = 0;
    virtual void mms_release() {}
};

bool valid_number(std::string_view n);
// Whether a new outgoing call may join the calls up now: the id of the
// connected call to hold for it ("" for none), or why not: a call still
// being set up or ringing, two calls already, an emergency call.
std::expected<std::string, std::string> second_call(const std::vector<Call>& calls);
// 1 to 32 of the sixteen DTMF keys: 0-9, *, #, A-D.
bool valid_dtmf(std::string_view tones);
// The numbers placed as emergency calls, as the shell's emergencyNumbers,
// and extra ones (the SIM's and the network's): with the radio off, with no
// SIM and with a locked one.
bool emergency_number(std::string_view n, const std::vector<std::string>& extra = {});
// *100#, *#21#: ^[*#][0-9*#]{1,30}#$
bool valid_ussd(std::string_view code);
// 4-8 digits; a PUK is 8.
bool valid_pin(std::string_view pin);
// A mobile country code: three digits, or "" for none.
bool valid_mcc(std::string_view mcc);
// A mobile network code: two or three digits, or "" for none.
bool valid_mnc(std::string_view mnc);
// Whether two mobile country codes are one country's (the United States,
// India, Japan and the United Kingdom have several).
bool same_country(std::string_view mcc, std::string_view other);
// What a registration says about roaming: "" at home (or off the network),
// "national" on another network of the SIM's own country (China
// Broadnet's SIMs on China Mobile's network, say), which the national
// roaming switch governs (on by default), "international" on another
// country's network or when either country is unknown, which the data
// roaming switch governs (off by default).
std::string roaming_kind(bool roaming, std::string_view sim_mcc, std::string_view net_mcc);
// Whether the data link may be up on a registration of that roaming_kind.
bool roaming_allowed(std::string_view kind, bool data_roaming, bool national_roaming);
// A network interface name as the Shell writes it into systemd-networkd's
// files and the firewall (shell/cellnet.go cellIfaceName): 1 to 15 of
// letters, digits, _ . -, not starting with a dot.
bool valid_iface(std::string_view name);
// What a backend's status breaks of what the Shell relies on whatever the
// baseband (docs/24-sku-framework.md §8.8): the backend named; the data
// link exactly while data_state is connected, off while the radio or the
// switch is (and while there is no modem); the links' interfaces among data_ifaces (the firewall
// closes those) and named as valid_iface; static addressing with an address
// and prefix; a roaming kind only while registered, and data_state roaming
// only where roaming_allowed refuses it; signal bars 0-4, none while not
// registered; PIN tries only while the SIM asks; an IMS registration of
// Status::ims's words, unknown unless registered on 4G or 5G. Empty when
// it breaks none.
std::vector<std::string> contract_problems(const Status& s);
// Hex digits in pairs, at most max_bytes bytes' worth.
bool valid_hex(std::string_view hex, size_t max_bytes);
// Bytes as lower-case hex.
std::string to_hex(std::string_view bytes);
long long unix_now();

// The phone book's AT commands (3GPP TS 27.007), for a modem that only
// reads it that way. cpbr_range reads the record range from the answer to
// AT+CPBR=?; parse_cpbr the entries from the answer to AT+CPBR=<first>,<last>,
// decoding names and numbers sent as UCS2 hex (after AT+CSCS="UCS2").
// Empty records are left out.
std::optional<std::pair<int, int>> cpbr_range(std::string_view reply);
std::vector<SimContact> parse_cpbr(std::string_view reply, bool ucs2);
// UTF-16BE as hex (AT+CSCS="UCS2") to UTF-8; nullopt unless it is that.
std::optional<std::string> ucs2_decode(std::string_view hex);
// The current character set from the answer to AT+CSCS?, "" unknown.
std::string cscs_charset(std::string_view reply);

// What the simulated network says: its operator's name and the texts of its
// SMS replies and USSD services. sim-network.json carries a Chinese
// operator's, which the boot selftest expects; without it the network speaks
// these English ones.
struct SimNetwork {
    using Texts = std::map<std::string, std::string, std::less<>>;
    std::string op = "Kario Mobile";
    Texts sms_replies = {{"10086", "Your balance is 88.88."}};  // by the number texted
    std::string sms_echo = "Received: ";                         // any other number replies with this and the text
    Texts ussd_answers = {{"*100#", "Balance 25.80, data left this month 3.2 GB."}};
    std::string ussd_menu_code = "*101#";
    std::string ussd_menu = "1 Balance\n2 Data\n3 Exit";
    Texts ussd_menu_answers = {{"1", "Balance 25.80."}, {"2", "Data left this month 3.2 GB."}, {"3", "Goodbye."}};
    std::string ussd_menu_retry = "Invalid choice, choose again:";  // followed by the menu
    std::string ussd_unknown = "The network does not support this code.";
    std::string ussd_received = "Received.";  // the answer to a reply to the network's own prompt
    // Where the SIM and the network are from (mobile country and network
    // codes; the network is the SIM's home one unless net_mcc and net_mnc
    // say), the SIM's service provider name and GID1 (hex), and the zone
    // the network sends (nitz_offset, nitz_dst minutes; none without).
    std::string sim_mcc, net_mcc, sim_mnc, net_mnc, spn, gid1;
    std::optional<Nitz> nitz;
    // The SIM's emergency numbers (its EF_ECC; Sim.EmergencyNumbers).
    std::vector<std::string> emergency;
    // The SIM's phone book; sim_contacts: [{"name", "number"}, ...], records from 1.
    std::vector<SimContact> contacts = {{1, "Alice", "+8613800138001"}, {2, "Bob", "13800138002"}};

    // A JSON object with members named as in sim-network.json; a member it
    // lacks keeps its default (a table is replaced whole), one it does not
    // know is an error.
    static std::expected<SimNetwork, std::string> parse(std::string_view json);
    static std::expected<SimNetwork, std::string> load(const std::string& path);
};

// SimModem behaves like a registered phone on a network: outgoing calls ring
// and connect (numbers ending in 000 are busy), every SMS gets a delivery
// report and a reply (SMS to numbers ending in 999 fail, to numbers ending
// in 998 are sent but never delivered), USSD *100# answers with the balance
// and *101# with a menu, and tests can make it ring, hang up as the other
// party, answer slowly, deliver an SMS, a cell broadcast or USSD or lock
// its SIM.
class SimModem : public Modem {
public:
    SimModem(Loop& loop, Events events, SimNetwork net = {})
        : loop_(loop), ev_(std::move(events)), net_(std::move(net)) {}

    Status status() const override;
    std::vector<Call> calls() const override;
    std::expected<std::string, std::string> dial(std::string_view number) override;
    Result answer(std::string_view id) override;
    Result hangup(std::string_view id) override;
    Result hold(std::string_view id, bool on) override;
    Result mute(std::string_view id, bool on) override;
    Result send_dtmf(std::string_view id, std::string_view tones) override;
    std::expected<std::string, std::string> send_sms(std::string_view to, std::string_view text, std::string_view id) override;
    Result unlock(std::string_view pin, std::string_view puk) override;
    Result set_pin_lock(std::string_view pin, bool on) override;
    Result change_pin(std::string_view pin, std::string_view new_pin) override;
    void ussd_initiate(std::string_view code, UssdReply done) override;
    void ussd_respond(std::string_view text, UssdReply done) override;
    Result ussd_cancel() override;
    void sim_contacts(SimContactsReply done) override;
    // Its data link is up while the radio and the switch are on, the SIM is
    // unlocked, it has an APN and roams only where the switches allow, on a
    // made-up interface (kSimDataIface) with the addresses a network gives
    // for the APN's IP type (sim_link). An emergency call with the radio
    // off rings once the radio has found the network. Its MMS goes over
    // the data link.
    Result set_radio(bool on) override;
    Result set_data(bool on) override;
    Result set_data_roaming(bool on, bool national) override;
    void set_apn(const ApnPlan& plan) override;
    void mms_connect(MmsReply done) override { done(std::optional<DataLink>{}); }

    std::expected<std::string, std::string> ring(std::string_view number);
    // The other party of the call with that number hangs up.
    Result remote_hangup(std::string_view number);
    // A slower network: dial requests are answered `dial` later (the modem
    // acknowledges late; the call does not exist until then) and the
    // called party answers `answer` after it starts ringing.
    void set_latency(std::chrono::milliseconds dial, std::chrono::milliseconds answer);
    static constexpr std::chrono::milliseconds answer_after{900};
    std::chrono::milliseconds dial_latency() const { return dial_latency_; }
    void after(std::chrono::milliseconds delay, Loop::Task task) { loop_.after(delay, std::move(task)); }
    // A message from the network: text, or 8-bit user data (hex, a WAP
    // push), with a message class (-1 none, 0 flash).
    Result deliver_sms(std::string_view from, std::string_view text, std::string_view data = {}, int sms_class = -1);
    // wac: the alert's warning area coordinates, hex ("" none).
    Result deliver_cb(int channel, int serial, std::string_view text, std::string_view wac = {});
    // The SIM asks for `pin` until it is entered: three tries, then the PUK
    // 12345678. (A SIM's PIN is 1234 until then, its lock off.)
    void lock(std::string_view pin);
    // USSD from the network: a notification, or with `request` a prompt that
    // any reply answers.
    Result network_ussd(std::string_view text, bool request);
    // Another SIM or network: their mobile country codes ("" unknown) and
    // the zone the network sends, with its time now; the SIM's network
    // code, SPN and GID1 when given (else they stay).
    struct Place {
        std::string sim_mcc, net_mcc;
        std::optional<std::string> sim_mnc, spn, gid1;
        std::optional<Nitz> nitz;
    };
    Result set_network(Place p);
    // Registered roaming on another network (its codes and name; the name
    // "" shows the codes), or at home again with an empty net_mcc.
    Result roam(std::string_view net_mcc, std::string_view net_mnc, std::string_view name);
    // The IMS registration it says while registered (one of Status::ims's
    // words); unknown until told, as nothing reads a simulated modem's.
    Result set_ims(std::string_view state);

private:
    Call* find(std::string_view id);
    void set_state(Call& c, std::string state, std::string reason = {});
    void hold_active(std::string_view except);
    void end_ussd();

    Loop& loop_;
    Events ev_;
    SimNetwork net_;
    std::map<std::string, Call> calls_;
    int next_call_ = 0, next_sms_ = 0, next_cb_ = 0;
    long long boot_ = unix_now();  // SMS ids stay unique across restarts; the store keys on them
    bool check(std::string_view pin);
    void settle_pin(bool ok);

    std::string pin_;               // non-empty while the SIM is locked
    int tries_ = 0;
    bool puk_ = false;
    std::string code_ = "1234";     // the SIM's PIN
    bool lock_on_ = false;
    int checks_ = 3;                // PIN tries left outside unlocking
    std::string ussd_;              // the session waiting for a reply: "" | "menu" | "network"
    bool radio_ = true, data_ = true, data_roaming_ = false, national_ = true;
    std::optional<Apn> apn_;
    // The network roamed onto (roam): its name, "" at home.
    std::string roaming_op_;
    bool roaming_ = false;
    std::string ims_ = "unknown";
    std::chrono::milliseconds dial_latency_{0}, answer_latency_ = answer_after;
};

// The simulated data link's interface: a name no real link has, so the
// firewall rule closing it touches nothing on a developer machine or the
// emulator.
inline constexpr const char* kSimDataIface = "simwwan0";

// The simulated data link's addressing for an APN's IP type: what a
// network gives a phone (carrier-grade NAT space for IPv4, the
// documentation prefix for IPv6), static as ModemManager reports a QMI
// modem's bearer.
DataLink sim_link(std::string_view ip_type);

}  // namespace tel
