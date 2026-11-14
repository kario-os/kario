// The telephony service: RPC methods over the modem, the call log, and
// events for subscribers (the shell, phone-smsd). Messages are phone-smsd's
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
    std::string number, dir, result;  // result: answered | missed | rejected | busy | outgoing
    long long at = 0, duration = 0;
    json::Value to_json() const;
};

class Service {
public:
    using Emit = std::function<void(const json::Value& event)>;
    using Reply = std::function<void(std::expected<json::Value, std::string>)>;

    // state_dir may be empty (nothing persisted).
    Service(std::string state_dir, Emit emit) : dir_(std::move(state_dir)), emit_(std::move(emit)) { load(); }

    // Hands the modem the radio and data switches kept from the last run
    // (radio.json), before it reaches the network: a phone in airplane
    // mode must not transmit while it starts.
    void attach(Modem* modem, SimModem* sim) {
        modem_ = modem, sim_ = sim;
        restore_radio();
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
    void queue(json::Object event);
    json::Value ack(const json::Value& keys);
    void load();
    void save() const;
    void restore_radio();
    void save_radio() const;

    std::string dir_;
    Emit emit_;
    Modem* modem_ = nullptr;
    SimModem* sim_ = nullptr;
    std::vector<LogEntry> log_;
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
    bool radio_on_ = true, data_on_ = true;
};

}  // namespace tel
