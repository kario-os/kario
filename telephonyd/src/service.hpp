// The telephony service: RPC methods over the modem, the call log, and
// events for subscribers (the shell, phone-smsd, and phone-audiod where it
// routes calls through a UCM profile). Messages are phone-smsd's
// (32-messaging.md): telephonyd only holds what smsd has not acknowledged.
#pragma once

#include <expected>
#include <functional>
#include <memory>
#include <string>
#include <vector>

#include "json.hpp"
#include "modem.hpp"

namespace tel {

struct LogEntry {
    std::string number, dir, result;  // result: answered | missed | rejected | blocked | busy | outgoing
    long long at = 0, duration = 0;
    // An entry another phone kept (log.import) has an id and is imported;
    // the phone's own have neither.
    std::string id;
    bool imported = false;
    json::Value to_json() const;
};

class Service {
public:
    using Emit = std::function<void(const json::Value& event)>;
    using Reply = std::function<void(std::expected<json::Value, std::string>)>;

    // state_dir may be empty (nothing persisted); apns: the carriers' APNs
    // (apns.json), the user's changes on top kept in state_dir.
    Service(std::string state_dir, Emit emit, ApnDb apns = {})
        : dir_(std::move(state_dir)), emit_(std::move(emit)), db_(std::move(apns)), apns_(dir_.empty() ? "" : dir_ + "/apns.json") {
        load();
    }

    // Hands the modem the radio, data and data roaming switches kept from
    // the last run (radio.json), before it reaches the network: a phone in
    // airplane mode must not transmit while it starts.
    void attach(Modem* modem, SimModem* sim) {
        modem_ = modem, sim_ = sim;
        restore_radio();
        sync_apn(modem_->status());
    }
    Modem::Events events();

    // `reply` may run after this returns: USSD waits for the network.
    void handle(const std::string& method, const json::Value& params, Reply reply);
    // For requests answered at once, which with SimModem is all of them.
    std::expected<json::Value, std::string> handle(const std::string& method, const json::Value& params);

private:
    std::expected<json::Value, std::string> call(const std::string& method, const json::Value& params);
    void on_call(const Call& c);
    void on_sms(const Sms& s);
    void on_cb(const Cb& c);
    void on_status(const Status& s);
    // Gives the modem the SIM's internet APN once the SIM is unlocked and
    // either known or on the network (the network's default APN for a SIM
    // the database lacks).
    void sync_apn(const Status& s);
    std::expected<json::Value, std::string> apn_call(const std::string& method, const json::Value& params);
    void queue(json::Object event);
    json::Value ack(const json::Value& keys);
    std::expected<json::Value, std::string> import_log(const json::Value* entries);
    json::Value delete_log(const json::Value* ids);
    void load();
    void save() const;
    void restore_radio();
    void save_radio() const;

    std::string dir_;
    Emit emit_;
    Modem* modem_ = nullptr;
    SimModem* sim_ = nullptr;
    // Newest first, at most max_log; next_import_ numbers imported entries.
    std::vector<LogEntry> log_;
    long long next_import_ = 1;
    // The events phone-smsd must not miss, oldest first, each with its
    // "key", until smsd acknowledges them (sms.ack): received messages and
    // cell broadcasts, outgoing messages' outcomes. Kept across restarts;
    // after one of either side, smsd gets them again (sms.pending).
    std::vector<json::Object> pending_;
    // The messages telephonyd kept before phone-smsd, until smsd has taken
    // them over (sms.legacy, sms.legacy_drop).
    json::Array legacy_;
    // sim.sms "held": the message is queued but not announced, as if
    // telephonyd had died between storing it and telling anyone.
    bool unannounced_ = false;
    bool radio_on_ = true, data_on_ = true, roaming_on_ = false;
    ApnDb db_;
    ApnStore apns_;
    // Calls the shell declined for the block list (hangup "blocked"): the
    // log says blocked rather than rejected.
    std::vector<std::string> blocked_;
};

}  // namespace tel
