// MmModem drives the baseband through ModemManager (org.freedesktop.ModemManager1
// on the system bus). It follows ModemManager and the first modem it reports
// coming and going, enables a disabled modem, places and answers calls through
// the modem's Voice interface, sends SMS through Messaging and USSD through
// Modem3gpp.Ussd. A received message, including one that arrived while
// telephonyd was not running, stays in the modem's storage until phone-smsd
// has stored it (ack_sms); so do cell broadcasts (CellBroadcast, ModemManager
// 1.24). Of the modem's bearers it manages the data link and the MMS
// access point's, its own; those other programs bring up (81voltd's for
// IMS) it leaves alone.
#pragma once

#include <systemd/sd-bus.h>

#include <cstdint>
#include <map>
#include <optional>
#include <set>
#include <string>
#include <variant>
#include <vector>

#include "mm_state.hpp"
#include "modem.hpp"

namespace tel {

class MmModem : public Modem {
public:
    MmModem(Loop& loop, Events events);
    ~MmModem() override;
    MmModem(const MmModem&) = delete;
    MmModem& operator=(const MmModem&) = delete;

    // Connects to the system bus ($DBUS_SYSTEM_BUS_ADDRESS when set).
    Result start();
    // The bus connection broke; the loop has been stopped.
    bool failed() const { return failed_; }

    Status status() const override;
    std::vector<Call> calls() const override;
    std::expected<std::string, std::string> dial(std::string_view number) override;
    Result answer(std::string_view id) override;
    Result hangup(std::string_view id) override;
    Result hold(std::string_view id, bool on) override;
    Result mute(std::string_view id, bool on) override;
    Result send_dtmf(std::string_view id, std::string_view tones) override;
    std::expected<std::string, std::string> send_sms(std::string_view to, std::string_view text, std::string_view id) override;
    void ack_sms(std::string_view id) override;
    void ack_cb(std::string_view id) override;
    Result unlock(std::string_view pin, std::string_view puk) override;
    Result set_pin_lock(std::string_view pin, bool on) override;
    Result change_pin(std::string_view pin, std::string_view new_pin) override;
    void ussd_initiate(std::string_view code, UssdReply done) override;
    void ussd_respond(std::string_view response, UssdReply done) override;
    Result ussd_cancel() override;
    // ModemManager has no phone book interface: AT commands through
    // Modem.Command, which it takes only when it runs with --debug.
    void sim_contacts(SimContactsReply done) override;
    // Radio off: Modem.Enable(false), then SetPowerState(low); on:
    // SetPowerState(on), then Enable. Data on: Simple.Connect with the
    // internet APN (set_apn) once registered, unless roaming where the
    // switches keep it down; off: Simple.Disconnect of that bearer alone.
    // Another APN (or the same with other settings), or roaming where it
    // may not be up, disconnects first. Bearers others brought up for IMS
    // or emergency calls (mm::bearer_role) are neither the data link nor
    // disconnected. The plan's attach APN goes to
    // Modem3gpp.SetInitialEpsBearerSettings where the modem's differs.
    Result set_radio(bool on) override;
    Result set_data(bool on) override;
    Result set_data_roaming(bool on, bool national) override;
    void set_apn(const ApnPlan& plan) override;
    // A second bearer, Simple.Connect on the MMS APN with apn-type mms.
    void mms_connect(MmsReply done) override;
    void mms_release() override;

    // D-Bus property values telephonyd reads: integers (and booleans),
    // strings and object paths (byte arrays as hex), string and path
    // lists, integer maps (a{uu}), dictionaries of integers and strings
    // (a{sv}, its other members left out), and the modem's ports (a(su):
    // name -> type, as an integer map).
    using Pairs = std::vector<std::pair<long long, long long>>;
    using Ints = std::map<std::string, long long>;
    using Prop = std::variant<std::monostate, long long, std::string, std::vector<std::string>, Pairs, Ints, mm::Dict>;
    using Props = std::map<std::string, Prop>;
    using Ifaces = std::map<std::string, Props>;
    using Reply = std::function<void(sd_bus_message* reply, const sd_bus_error* error)>;
    using Args = std::function<int(sd_bus_message* m)>;

private:
    struct Device {
        std::string path;
        long long state = 0, quality = 0, access = 0, reg = 0;
        long long lock = 0, failed_reason = 0;
        long long facility_locks = 0;  // Modem3gpp EnabledFacilityLocks: bit 0, the SIM's PIN
        Pairs retries;
        std::string op, sim, imei;
        std::vector<std::string> own;
        bool voice = false, messaging = false, ussd = false, cell_broadcast = false;
        bool enabling = false, gave_up = false;
        long long ussd_state = 0;
        bool ussd_busy = false;     // an Initiate or Respond is waiting for the network
        bool ussd_waiting = false;  // the network waits for a Respond
        // The SIM's home network (Sim.OperatorIdentifier, read once it is
        // unlocked), its SPN and GID1 (Sim.OperatorName, Sim.Gid1), the
        // network's (Modem3gpp.OperatorCode), the network's zone and time
        // (Modem.Time).
        std::string sim_mcc, net_mcc, sim_mnc, net_mnc, spn, gid1;
        std::vector<std::string> sim_emergency;  // Sim.EmergencyNumbers
        bool sim_asked = false, sim_read = false;
        std::optional<Nitz> nitz;
        // Modem.PowerState and Ports; its bearers (Bearer.Connected,
        // BearerType, Interface, Ip4Config, Ip6Config and Properties, by
        // path; known once read); a Disable, Connect or Disconnect waiting
        // for its answer; a Connect that failed (tried again when the
        // registration, a switch or the APN changes).
        long long power = 0;
        Ints ports;
        struct Bearer {
            bool known = false, connected = false;
            long long type = 0;
            std::string iface;
            mm::Dict props;
            std::optional<IpConfig> ip4, ip6;
            int mtu = 0;
        };
        std::map<std::string, Bearer> bearers;
        bool disabling = false, connecting = false, disconnecting = false, connect_failed = false;
        // The bearer telephonyd's last Simple.Connect made and the APN it
        // asked with: what that bearer is, whatever its Properties say.
        std::string own_bearer;
        Apn own_apn;
        // The same for the MMS bearer, with its Connect or Disconnect
        // waiting for an answer.
        std::string mms_bearer;
        Apn mms_apn;
        bool mms_connecting = false, mms_disconnecting = false;
        // Modem3gpp.InitialEpsBearerSettings (none before it is read or
        // without LTE) and the initial EPS bearer (InitialEpsBearer), with
        // its Properties once read; a SetInitialEpsBearerSettings waiting
        // for its answer and the APN it last asked with, not asked again.
        std::optional<mm::Dict> eps, eps_props;
        std::string eps_bearer;
        bool eps_reading = false, attaching = false;
        std::optional<Apn> eps_asked;
    };
    struct Tracked {
        Call call;
        std::string path;  // empty until CreateCall answers
        bool hung_up = false;
        bool answering = false;
        // Start, Accept or HoldAndAccept sent for this call, not yet answered,
        // and the reason of the end ModemManager announced before its reply.
        bool replying = false;
        std::optional<std::int64_t> ended_by;
    };

    template <void (MmModem::*F)(sd_bus_message*)>
    static int on_signal(sd_bus_message* m, void* self, sd_bus_error*);

    void invoke(const std::string& path, const char* iface, const char* member, const Args& args, Reply done,
                std::uint64_t timeout_usec = 0);
    Reply logged(std::string what);
    void pump();
    void tick();
    void fail(const std::string& why);

    void discover();
    void adopt(const std::string& path, const Ifaces& ifaces);
    void apply(const std::string& iface, const Props& props, bool initial);
    void drop_calls(const std::string& why);
    void drop_modem(const std::string& why);
    void maybe_enable();
    void follow_radio();
    void power_down();
    void maybe_connect();
    void maybe_attach();
    void maybe_mms();
    // Why the MMS bearer may not be up now, "" when it may.
    std::string mms_blocked() const;
    void mms_answer(const std::expected<std::optional<DataLink>, std::string>& r);
    // Simple.Disconnect of one bearer; busy is the Device's flag for it.
    void disconnect(const std::string& bearer, bool Device::*busy, std::string what);
    // What the bearer at path is for: the data link and the MMS bearer
    // telephonyd made, else mm::bearer_role.
    mm::BearerRole role(const std::string& path, const Device::Bearer& b) const;
    // Whether the bearer at path is the link the APN asks for (set_apn).
    bool current(const std::string& path, const Device::Bearer& b) const;
    void fetch_bearer(const std::string& path);
    void fetch_eps_bearer();
    void bearer_props(Device::Bearer& b, const Props& p);
    void sim_props(const Props& p);
    // roaming_kind of the registration.
    std::string roaming() const;
    void place(const std::string& id);
    void publish();

    void on_owner(sd_bus_message* m);
    void on_added(sd_bus_message* m);
    void on_removed(sd_bus_message* m);
    void on_props(sd_bus_message* m);
    void on_call_added(sd_bus_message* m);
    void on_call_deleted(sd_bus_message* m);
    void on_call_state(sd_bus_message* m);
    void on_sms_added(sd_bus_message* m);
    void on_cbm_added(sd_bus_message* m);
    void on_network_time(sd_bus_message* m);
    void fetch_sim();
    void fetch_network_time();
    void network_time(std::string_view iso);

    Tracked* find(std::string_view id);
    void fetch_call(const std::string& path);
    void end(const std::string& id, const std::string& reason, bool delete_on_modem, const std::string& error = {});
    // Ends the call as failed with why, which also goes into the journal.
    void fail_call(const std::string& id, const std::string& why, bool delete_on_modem);
    // The reply to the call's Start, Accept or HoldAndAccept: `what` failed
    // when e is set.
    void settle_call(const std::string& id, std::string_view what, const sd_bus_error* e);
    void call_ended(const std::string& id, std::int64_t reason, bool delete_on_modem);
    void emit(const Call& c);
    void fetch_sms(const std::string& path);
    void delete_sms(const std::string& path);
    void delivery_report(const std::string& path, const Props& props);
    void fetch_cbm(const std::string& path);
    void set_cb_channels();
    void ussd_request(const char* member, std::string arg, UssdReply done);
    using AtReply = std::function<void(std::expected<std::string, std::string>)>;
    void at(const std::string& path, std::string command, unsigned seconds, AtReply done);
    void ussd_changed(const Props& p);
    void ussd_ended();

    Loop& loop_;
    Events ev_;
    sd_bus* bus_ = nullptr;
    bool failed_ = false, warned_absent_ = false;
    bool mm_running_ = false;  // ModemManager answered or owns its name
    std::optional<Device> modem_;
    std::map<std::string, Tracked> calls_;
    std::map<std::string, std::string> by_path_;
    std::set<std::string> receiving_, delivered_;
    // Received messages and cell broadcasts phone-smsd has not
    // acknowledged: id -> object path, deleted on the modem on its ack.
    std::map<std::string, std::string> unacked_sms_, unacked_cbm_;
    std::set<std::string> cbm_receiving_, cbm_seen_;
    // Sent messages waiting for their delivery report, by object path.
    std::map<std::string, Sms> reports_;
    std::string last_status_;
    long long boot_ = unix_now();
    int next_call_ = 0, next_sms_ = 0;
    bool radio_ = true, data_ = true, data_roaming_ = false, national_ = true;
    // The APNs to connect with; none until the service chose them for the
    // SIM in the modem.
    std::optional<ApnPlan> plan_;
    std::string sos_;  // an emergency call waiting for the radio to come up
    // mms.connect: what waits for the MMS bearer, and until when it is held.
    std::vector<MmsReply> mms_waiting_;
    Loop::Clock::time_point mms_until_{};
};

}  // namespace tel
