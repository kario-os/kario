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
#include <vector>

#include "json.hpp"
#include "loop.hpp"

namespace tel {

struct Call {
    std::string id, number;
    std::string dir;    // "in" | "out"
    std::string state;  // dialing | alerting | incoming | active | held | ended
    std::string reason; // why it ended: hangup | remote | busy | rejected | missed
    long long started = 0, connected = 0, ended = 0;  // unix seconds
    bool muted = false;
    bool emergency = false;  // an emergency number: placed even with the radio off
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

struct Status {
    bool registered = false;
    std::string op, rat, own, backend;
    int signal = 0;  // bars, 0-4
    std::string sim = "ready";  // ready | pin | puk (asks for it) | locked (by the operator) | absent | error
    int sim_tries = 0;          // PIN or PUK attempts left, 0 when unknown
    bool pin_lock = false;      // the SIM asks for its PIN when the phone starts
    std::string imei;           // the modem's, "" until it is known
    // Mobile country codes: the SIM's home network's and the network's the
    // phone is on (roaming, they differ), "" while unknown.
    std::string sim_mcc, net_mcc;
    std::optional<Nitz> nitz;   // the zone and time the network last sent
    // The radio (off: airplane mode) and the cellular data link: its
    // switch, its state (connected | idle: on, no link yet | off) and the
    // network interfaces it can use, which the shell's firewall closes
    // while the switch is off.
    bool radio = true, data = true;
    std::string data_state = "off";
    std::vector<std::string> data_ifaces;
    json::Value to_json() const;
};

// A USSD message from the network.
struct Ussd {
    std::string state;  // done (the session is over) | input (the network waits for a reply) | notify (nothing to reply)
    std::string text;
    json::Value to_json() const;
};

using Result = std::expected<void, std::string>;
using UssdReply = std::function<void(std::expected<Ussd, std::string>)>;

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
    virtual std::expected<std::string, std::string> dial(std::string_view number) = 0;
    virtual Result answer(std::string_view id) = 0;
    virtual Result hangup(std::string_view id) = 0;
    virtual Result hold(std::string_view id, bool on) = 0;
    virtual Result mute(std::string_view id, bool on) = 0;
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
    // Off powers the radio down (airplane mode) and ends the calls; it is
    // refused during an emergency call. Dialing an emergency number powers
    // it up again by itself. The data link needs the radio and the switch.
    virtual Result set_radio(bool on) = 0;
    virtual Result set_data(bool on) = 0;
};

bool valid_number(std::string_view n);
// The numbers placed as emergency calls, as the shell's emergencyNumbers.
bool emergency_number(std::string_view n);
// *100#, *#21#: ^[*#][0-9*#]{1,30}#$
bool valid_ussd(std::string_view code);
// 4-8 digits; a PUK is 8.
bool valid_pin(std::string_view pin);
// A mobile country code: three digits, or "" for none.
bool valid_mcc(std::string_view mcc);
long long unix_now();

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
    // Where the SIM and the network are from (mobile country codes; the
    // network is the SIM's home one unless net_mcc says) and the zone the
    // network sends (nitz_offset, nitz_dst minutes; none without).
    std::string sim_mcc, net_mcc;
    std::optional<Nitz> nitz;

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
// and *101# with a menu, and tests can make it ring, deliver an SMS, a cell
// broadcast or USSD or lock its SIM.
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
    std::expected<std::string, std::string> send_sms(std::string_view to, std::string_view text, std::string_view id) override;
    Result unlock(std::string_view pin, std::string_view puk) override;
    Result set_pin_lock(std::string_view pin, bool on) override;
    Result change_pin(std::string_view pin, std::string_view new_pin) override;
    void ussd_initiate(std::string_view code, UssdReply done) override;
    void ussd_respond(std::string_view text, UssdReply done) override;
    Result ussd_cancel() override;
    // Its data link is up while the radio and the switch are on and the
    // SIM is unlocked, on a made-up interface (kSimDataIface). An emergency
    // call with the radio off rings once the radio has found the network.
    Result set_radio(bool on) override;
    Result set_data(bool on) override;

    std::expected<std::string, std::string> ring(std::string_view number);
    void deliver_sms(std::string_view from, std::string_view text);
    Result deliver_cb(int channel, int serial, std::string_view text);
    // The SIM asks for `pin` until it is entered: three tries, then the PUK
    // 12345678. (A SIM's PIN is 1234 until then, its lock off.)
    void lock(std::string_view pin);
    // USSD from the network: a notification, or with `request` a prompt that
    // any reply answers.
    Result network_ussd(std::string_view text, bool request);
    // Another SIM or network: their mobile country codes ("" unknown) and
    // the zone the network sends, with its time now.
    Result set_network(std::string_view sim_mcc, std::string_view net_mcc, std::optional<Nitz> nitz);

private:
    Call* find(std::string_view id);
    void set_state(Call& c, std::string state, std::string reason = {});
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
    bool radio_ = true, data_ = true;
};

// The simulated data link's interface: a name no real link has, so the
// firewall rule closing it touches nothing on a developer machine or the
// emulator.
inline constexpr const char* kSimDataIface = "simwwan0";

}  // namespace tel
