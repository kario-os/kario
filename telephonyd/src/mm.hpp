// MmModem drives the baseband through ModemManager (org.freedesktop.ModemManager1
// on the system bus). It follows ModemManager and the first modem it reports
// coming and going, enables a disabled modem, places and answers calls through
// the modem's Voice interface, sends SMS through Messaging and USSD through
// Modem3gpp.Ussd. A received message, including one that arrived while
// telephonyd was not running, stays in the modem's storage until phone-smsd
// has stored it (ack_sms); so do cell broadcasts (CellBroadcast, ModemManager
// 1.24).
#pragma once

#include <systemd/sd-bus.h>

#include <cstdint>
#include <map>
#include <optional>
#include <set>
#include <string>
#include <variant>
#include <vector>

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
    std::expected<std::string, std::string> send_sms(std::string_view to, std::string_view text, std::string_view id) override;
    void ack_sms(std::string_view id) override;
    void ack_cb(std::string_view id) override;
    Result unlock(std::string_view pin, std::string_view puk) override;
    Result set_pin_lock(std::string_view pin, bool on) override;
    Result change_pin(std::string_view pin, std::string_view new_pin) override;
    void ussd_initiate(std::string_view code, UssdReply done) override;
    void ussd_respond(std::string_view response, UssdReply done) override;
    Result ussd_cancel() override;
    // Radio off: Modem.Enable(false), then SetPowerState(low); on:
    // SetPowerState(on), then Enable. Data on: Simple.Connect with the
    // network's default APN once registered; off: Simple.Disconnect.
    Result set_radio(bool on) override;
    Result set_data(bool on) override;

    // D-Bus property values telephonyd reads: integers (and booleans),
    // strings and object paths, string and path lists, integer maps (a{uu}),
    // dictionaries of integers (a{sv}, its other members left out), and
    // the modem's ports (a(su): name -> type, as an integer map).
    using Pairs = std::vector<std::pair<long long, long long>>;
    using Ints = std::map<std::string, long long>;
    using Prop = std::variant<std::monostate, long long, std::string, std::vector<std::string>, Pairs, Ints>;
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
        // unlocked), the network's (Modem3gpp.OperatorCode), the network's
        // zone and time (Modem.Time).
        std::string sim_mcc, net_mcc;
        bool sim_asked = false;
        std::optional<Nitz> nitz;
        // Modem.PowerState and Ports; its bearers (Bearer.Connected and
        // Interface, by path); a Disable, Connect or Disconnect waiting for
        // its answer; a Connect that failed (tried again when the
        // registration or a switch changes).
        long long power = 0;
        Ints ports;
        struct Bearer {
            bool connected = false;
            std::string iface;
        };
        std::map<std::string, Bearer> bearers;
        bool disabling = false, connecting = false, disconnecting = false, connect_failed = false;
    };
    struct Tracked {
        Call call;
        std::string path;  // empty until CreateCall answers
        bool hung_up = false;
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
    void fetch_bearer(const std::string& path);
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
    void end(const std::string& id, const std::string& reason, bool delete_on_modem);
    void emit(const Call& c);
    void fetch_sms(const std::string& path);
    void delete_sms(const std::string& path);
    void delivery_report(const std::string& path, const Props& props);
    void fetch_cbm(const std::string& path);
    void set_cb_channels();
    void ussd_request(const char* member, std::string arg, UssdReply done);
    void ussd_changed(const Props& p);
    void ussd_ended();

    Loop& loop_;
    Events ev_;
    sd_bus* bus_ = nullptr;
    bool failed_ = false, warned_absent_ = false;
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
    bool radio_ = true, data_ = true;
    std::string sos_;  // an emergency call waiting for the radio to come up
};

}  // namespace tel
