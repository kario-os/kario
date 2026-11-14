#include "service.hpp"

#include <fcntl.h>
#include <unistd.h>

#include <algorithm>
#include <filesystem>
#include <fstream>
#include <ranges>
#include <sstream>

namespace tel {

namespace fs = std::filesystem;

// max_pending: what phone-smsd has not taken while it is down; past it the
// oldest goes (a modem's own storage holds a few dozen).
constexpr size_t max_log = 200, max_pending = 1000;

json::Value LogEntry::to_json() const {
    return json::Object{{"number", number}, {"dir", dir}, {"result", result}, {"at", at}, {"duration", duration}};
}

Modem::Events Service::events() {
    return Modem::Events{
        .call = [this](const Call& c) { on_call(c); },
        .sms = [this](const Sms& s) { on_sms(s); },
        .cb = [this](const Cb& c) { on_cb(c); },
        .status = [this](const Status& s) { emit_(json::Object{{"event", "status"}, {"status", s.to_json()}}); },
        .unlock = [this](const std::string& e) { emit_(json::Object{{"event", "unlock"}, {"ok", e.empty()}, {"err", e}}); },
        .pin = [this](const std::string& e) { emit_(json::Object{{"event", "pin"}, {"ok", e.empty()}, {"err", e}}); },
        .ussd = [this](const Ussd& u) { emit_(json::Object{{"event", "ussd"}, {"state", u.state}, {"text", u.text}}); },
    };
}

void Service::on_call(const Call& c) {
    if (c.state == "ended") {
        std::string result = c.dir == "out" ? (c.reason == "busy" ? "busy" : "outgoing")
                           : c.connected ? "answered"
                           : c.reason == "missed" ? "missed" : "rejected";
        long long dur = c.connected ? c.ended - c.connected : 0;
        log_.insert(log_.begin(), LogEntry{c.number, c.dir, result, c.started, dur});
        if (log_.size() > max_log) log_.resize(max_log);
        save();
    }
    emit_(json::Object{{"event", "call"}, {"call", c.to_json()}});
}

// smsd set "sending" itself; every later outcome waits for its sms.ack.
void Service::on_sms(const Sms& s) {
    json::Object ev{{"event", "sms"}, {"sms", s.to_json()}};
    if (s.dir == "in") ev["key"] = s.id;
    else if (s.state != "sending") ev["key"] = s.id + "@" + s.state;
    if (ev.contains("key")) queue(ev);
    if (!unannounced_) emit_(ev);
}

void Service::on_cb(const Cb& c) {
    json::Object ev{{"event", "cb"}, {"key", c.id}, {"cb", c.to_json()}};
    queue(ev);
    emit_(ev);
}

// A message the modem reports again (ModemManager lists its storage after
// a restart, with the same id) is queued once.
void Service::queue(json::Object event) {
    const std::string key = std::get<std::string>(event.at("key"));
    if (std::ranges::any_of(pending_, [&](const json::Object& e) { return std::get<std::string>(e.at("key")) == key; })) return;
    pending_.push_back(std::move(event));
    if (pending_.size() > max_pending) pending_.erase(pending_.begin());
    save();
}

json::Value Service::ack(const json::Value& keys) {
    int n = 0;
    if (keys.is_array())
        for (const auto& k : std::get<json::Array>(keys)) {
            if (!k.is_string()) continue;
            auto it = std::ranges::find_if(pending_, [&](const json::Object& e) { return std::get<std::string>(e.at("key")) == std::get<std::string>(k); });
            if (it == pending_.end()) continue;
            const json::Value ev = *it;
            pending_.erase(it);
            ++n;
            if (!modem_) continue;
            if (const json::Value* c = ev.find("cb")) modem_->ack_cb(c->str("id"));
            else if (const json::Value* s = ev.find("sms"); s && s->str("dir") == "in") modem_->ack_sms(s->str("id"));
        }
    if (n) save();
    return json::Object{{"acked", n}};
}

void Service::handle(const std::string& method, const json::Value& p, Reply reply) {
    if (method != "ussd.initiate" && method != "ussd.respond") return reply(call(method, p));
    UssdReply done = [reply = std::move(reply)](std::expected<Ussd, std::string> r) {
        if (!r) return reply(std::unexpected(r.error()));
        reply(r->to_json());
    };
    if (method == "ussd.initiate") modem_->ussd_initiate(p.str("code"), std::move(done));
    else modem_->ussd_respond(p.str("text"), std::move(done));
}

std::expected<json::Value, std::string> Service::handle(const std::string& method, const json::Value& p) {
    auto out = std::make_shared<std::expected<json::Value, std::string>>(std::unexpected("no answer yet"));
    handle(method, p, [out](std::expected<json::Value, std::string> r) { *out = std::move(r); });
    return *out;
}

std::expected<json::Value, std::string> Service::call(const std::string& method, const json::Value& p) {
    auto ok = [](json::Value v = json::Object{}) -> std::expected<json::Value, std::string> { return v; };
    auto err = [](std::string e) -> std::expected<json::Value, std::string> { return std::unexpected(std::move(e)); };
    if (method == "status") return ok(modem_->status().to_json());
    if (method == "radio.set" || method == "data.set") {
        bool on = p.flag("on");
        Result r = method == "radio.set" ? modem_->set_radio(on) : modem_->set_data(on);
        if (!r) return err(r.error());
        (method == "radio.set" ? radio_on_ : data_on_) = on;
        save_radio();
        return ok();
    }
    if (method == "calls") {
        json::Array a;
        for (const auto& c : modem_->calls()) a.push_back(c.to_json());
        return ok(std::move(a));
    }
    if (method == "dial") {
        auto id = modem_->dial(p.str("number"));
        if (!id) return err(id.error());
        return ok(json::Object{{"call_id", *id}});
    }
    if (method == "answer" || method == "hangup" || method == "hold" || method == "mute") {
        std::string id = p.str("call_id");
        Result r = method == "answer" ? modem_->answer(id)
                 : method == "hangup" ? modem_->hangup(id)
                 : method == "hold"   ? modem_->hold(id, p.flag("on"))
                                      : modem_->mute(id, p.flag("on"));
        if (!r) return err(r.error());
        return ok();
    }
    if (method == "log") {
        json::Array a;
        for (const auto& e : log_) a.push_back(e.to_json());
        return ok(std::move(a));
    }
    if (method == "sms.send") {
        auto id = modem_->send_sms(p.str("to"), p.str("text"), p.str("id"));
        if (!id) return err(id.error());
        return ok(json::Object{{"id", *id}});
    }
    if (method == "sms.pending") {
        json::Array a;
        for (const auto& e : pending_) a.push_back(e);
        return ok(std::move(a));
    }
    if (method == "sms.ack") {
        const json::Value* keys = p.find("keys");
        return ok(ack(keys ? *keys : json::Value{}));
    }
    if (method == "sms.legacy") return ok(legacy_);
    if (method == "sms.legacy_drop") {
        legacy_.clear();
        save();
        return ok();
    }
    if (method == "sim.unlock") {
        Result r = modem_->unlock(p.str("pin"), p.str("puk"));
        if (!r) return err(r.error());
        return ok();
    }
    if (method == "sim.pin_lock" || method == "sim.change_pin") {
        Result r = method == "sim.pin_lock" ? modem_->set_pin_lock(p.str("pin"), p.flag("on")) : modem_->change_pin(p.str("pin"), p.str("new_pin"));
        if (!r) return err(r.error());
        return ok();
    }
    if (method == "ussd.cancel") {
        Result r = modem_->ussd_cancel();
        if (!r) return err(r.error());
        return ok();
    }
    // Test hooks: only the simulated baseband can be told to ring.
    if (method == "sim.ring" || method == "sim.sms" || method == "sim.cb" || method == "sim.lock" || method == "sim.ussd" || method == "sim.network") {
        if (!sim_) return err("not available with this modem backend");
        if (method == "sim.cb") {
            Result r = sim_->deliver_cb(static_cast<int>(p.num("channel")), static_cast<int>(p.num("serial")), p.str("text"));
            if (!r) return err(r.error());
            return ok();
        }
        if (method == "sim.network") {
            std::optional<Nitz> nitz;
            if (p.find("nitz_offset")) nitz = Nitz{.offset = static_cast<int>(p.num("nitz_offset")), .dst = static_cast<int>(p.num("nitz_dst"))};
            Result r = sim_->set_network(p.str("sim_mcc"), p.str("net_mcc"), nitz);
            if (!r) return err(r.error());
            return ok();
        }
        if (method == "sim.lock") {
            if (!valid_pin(p.str("pin"))) return err("a PIN is 4-8 digits");
            sim_->lock(p.str("pin"));
            return ok();
        }
        if (method == "sim.ussd") {
            Result r = sim_->network_ussd(p.str("text"), p.flag("request"));
            if (!r) return err(r.error());
            return ok();
        }
        if (method == "sim.sms") {
            unannounced_ = p.flag("held");
            sim_->deliver_sms(p.str("from"), p.str("text"));
            unannounced_ = false;
            return ok();
        }
        auto id = sim_->ring(p.str("number"));
        if (!id) return err(id.error());
        return ok(json::Object{{"call_id", *id}});
    }
    return err("unknown method '" + method + "'");
}

void Service::load() {
    if (dir_.empty()) return;
    std::ifstream in(fs::path(dir_) / "telephony.json");
    if (!in) return;
    std::stringstream ss;
    ss << in.rdbuf();
    auto v = json::parse(ss.str());
    if (!v || !v->is_object()) return;
    if (auto* log = v->find("log"); log && log->is_array())
        for (const auto& e : std::get<json::Array>(*log))
            log_.push_back(LogEntry{e.str("number"), e.str("dir"), e.str("result"),
                                    static_cast<long long>(e.num("at")), static_cast<long long>(e.num("duration"))});
    if (auto* sms = v->find("sms"); sms && sms->is_array()) legacy_ = std::get<json::Array>(*sms);
    if (auto* pending = v->find("pending"); pending && pending->is_array())
        for (const auto& e : std::get<json::Array>(*pending))
            if (e.is_object() && e.find("key") && e.find("key")->is_string()) pending_.push_back(std::get<json::Object>(e));
}

// Written whole and synced before it replaces the old file: a received
// message is only on disk here until phone-smsd acknowledges it.
void Service::save() const {
    if (dir_.empty()) return;
    json::Array log, pending(pending_.begin(), pending_.end());
    for (const auto& e : log_) log.push_back(e.to_json());
    json::Object state{{"log", std::move(log)}, {"pending", std::move(pending)}};
    if (!legacy_.empty()) state["sms"] = legacy_;
    std::error_code ec;
    fs::create_directories(dir_, ec);
    fs::path tmp = fs::path(dir_) / "telephony.json.tmp";
    std::string out = json::dump(state);
    int fd = ::open(tmp.c_str(), O_WRONLY | O_CREAT | O_TRUNC | O_CLOEXEC, 0600);
    if (fd < 0) return;
    bool ok = ::write(fd, out.data(), out.size()) == static_cast<ssize_t>(out.size()) && ::fsync(fd) == 0;
    ::close(fd);
    if (ok) fs::rename(tmp, fs::path(dir_) / "telephony.json", ec);
}

void Service::restore_radio() {
    if (!dir_.empty()) {
        std::ifstream in(fs::path(dir_) / "radio.json");
        std::stringstream ss;
        ss << in.rdbuf();
        if (auto v = json::parse(ss.str()); v && v->is_object()) {
            radio_on_ = v->flag("radio", true);
            data_on_ = v->flag("data", true);
        }
    }
    if (!modem_) return;
    (void)modem_->set_data(data_on_);
    (void)modem_->set_radio(radio_on_);
}

void Service::save_radio() const {
    if (dir_.empty()) return;
    std::error_code ec;
    fs::create_directories(dir_, ec);
    fs::path tmp = fs::path(dir_) / "radio.json.tmp";
    {
        std::ofstream out(tmp);
        out << json::dump(json::Object{{"radio", radio_on_}, {"data", data_on_}});
    }
    fs::rename(tmp, fs::path(dir_) / "radio.json", ec);
}

}  // namespace tel
