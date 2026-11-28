#include "service.hpp"

#include <algorithm>
#include <cstdlib>
#include <filesystem>
#include <optional>
#include <print>
#include <ranges>
#include <string>

#include "statefile.hpp"

namespace tel {

namespace fs = std::filesystem;

// max_pending: what phone-smsd has not taken while it is down; past it the
// oldest goes (a modem's own storage holds a few dozen). max_import: the
// entries one log.import takes (the switch protocol's batch).
constexpr size_t max_log = 200, max_pending = 1000, max_import = 500;

// What numbers in requests may be: the simulated network's latencies
// (sim.slow), a zone's minutes, and integers a double holds exactly.
constexpr long long max_sim_latency_ms = 60'000, max_zone_minutes = 24 * 60, max_exact = 1LL << 53;

// A number from a request as an integer in [lo, hi], within ±2^53 where
// doubles are exact, its fraction dropped; none outside them or for NaN,
// which no integer holds.
static std::optional<long long> integer(double d, long long lo, long long hi) {
    if (!(d >= static_cast<double>(lo) && d <= static_cast<double>(hi))) return std::nullopt;
    return static_cast<long long>(d);
}

json::Value LogEntry::to_json() const {
    json::Object o{{"number", number}, {"dir", dir}, {"result", result}, {"at", at}, {"duration", duration}};
    if (imported) {
        o["id"] = id;
        o["imported"] = true;
    }
    return o;
}

Modem::Events Service::events() {
    return Modem::Events{
        .call = [this](const Call& c) { on_call(c); },
        .sms = [this](const Sms& s) { on_sms(s); },
        .cb = [this](const Cb& c) { on_cb(c); },
        .status = [this](const Status& s) { on_status(s); },
        .unlock = [this](const std::string& e) { emit_(json::Object{{"event", "unlock"}, {"ok", e.empty()}, {"err", e}}); },
        .pin = [this](const std::string& e) { emit_(json::Object{{"event", "pin"}, {"ok", e.empty()}, {"err", e}}); },
        .ussd = [this](const Ussd& u) { emit_(json::Object{{"event", "ussd"}, {"state", u.state}, {"text", u.text}}); },
    };
}

void Service::on_call(const Call& c) {
    if (c.state == "ended") {
        bool blocked = std::erase(blocked_, c.id) > 0;
        std::string result = c.dir == "out" ? (c.reason == "busy" ? "busy" : "outgoing")
                           : c.connected ? "answered"
                           : blocked ? "blocked"
                           : c.reason == "missed" || c.reason == "failed" ? "missed" : "rejected";
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

void Service::on_status(const Status& s) {
    check(s);
    emit_(json::Object{{"event", "status"}, {"status", s.to_json()}});
    sync_apn(s);
}

void Service::check(const Status& s) {
    for (auto& p : contract_problems(s))
        if (broken_.insert(p).second) std::println(stderr, "phone-telephonyd: the {} backend breaks the Modem contract: {}", s.backend, p);
}

// Every status may change the SIM; the modem ignores a plan it has.
void Service::sync_apn(const Status& s) {
    if (!modem_ || s.sim != "ready") return;
    SimId id{s.sim_mcc, s.sim_mnc, s.spn, s.gid1};
    if (!s.registered && id.carrier().empty()) return;
    modem_->set_apn(apns_.list(db_, id).plan());
}

// apn.list: the SIM's APNs; apn.set: op save (apn, with its id to change
// one), delete (id), select (id: the internet APN to use) or reset (the
// database's list again); both answer the list. apn.mms: the first of the
// SIM's MMS APNs with an MMSC, for phone-smsd.
std::expected<json::Value, std::string> Service::apn_call(const std::string& method, const json::Value& p) {
    Status s = modem_->status();
    SimId id{s.sim == "ready" ? s.sim_mcc : "", s.sim_mnc, s.spn, s.gid1};
    if (method == "apn.mms") {
        for (const auto& a : apns_.list(db_, id).apns)
            if (a.has("mms") && !a.mmsc.empty()) return a.to_json();
        return std::unexpected("no MMS APN for this SIM");
    }
    if (method == "apn.set") {
        std::string op = p.str("op");
        ApnStore::Done r;
        if (op == "save") {
            const json::Value* in = p.find("apn");
            if (!in) return std::unexpected("apn: missing");
            auto apn = Apn::from_json(*in);
            if (!apn) return std::unexpected(apn.error());
            auto saved = apns_.save(db_, id, *apn, in->find("password") != nullptr);
            if (!saved) r = std::unexpected(saved.error());
        } else if (op == "delete") {
            r = apns_.remove(db_, id, p.str("id"));
        } else if (op == "select") {
            r = apns_.select(db_, id, p.str("id"));
        } else if (op == "reset") {
            r = apns_.reset(id);
        } else {
            return std::unexpected("op: save, delete, select or reset");
        }
        if (!r) return std::unexpected(r.error());
        sync_apn(modem_->status());
    }
    json::Value out = apns_.list(db_, id).to_json();
    auto& o = std::get<json::Object>(out);
    o["mcc"] = id.mcc;
    o["mnc"] = id.mcc.empty() ? "" : id.mnc;
    o["spn"] = id.mcc.empty() ? "" : id.spn;
    return out;
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

// Entries another phone kept (the switch receiver, docs/36): each goes in
// by its time, so the log stays newest first; one the log has (the same
// number, direction and time) is left out, and so is one past the cap,
// which still holds. The answer gives each entry's id by place, "" for one
// left out.
std::expected<json::Value, std::string> Service::import_log(const json::Value* entries) {
    if (!entries || !entries->is_array()) return std::unexpected("entries: an array of calls");
    const auto& in = std::get<json::Array>(*entries);
    if (in.size() > max_import) return std::unexpected("at most " + std::to_string(max_import) + " entries an import");
    std::vector<std::string> ids(in.size());
    bool changed = false;
    for (size_t i = 0; i < in.size(); ++i) {
        const json::Value& e = in[i];
        if (!e.is_object()) continue;
        auto at = integer(e.num("at"), 0, max_exact), duration = integer(e.num("duration"), 0, max_exact);
        if (!at || !duration) continue;
        LogEntry x{e.str("number"), e.str("dir"), e.str("result"), *at, *duration};
        bool in_call = x.dir == "in" && (x.result == "answered" || x.result == "missed" || x.result == "rejected");
        bool out_call = x.dir == "out" && (x.result == "outgoing" || x.result == "busy");
        if (!valid_number(x.number) || !(in_call || out_call) || x.at <= 0 || x.duration < 0) continue;
        if (std::ranges::any_of(log_, [&](const LogEntry& o) { return o.number == x.number && o.dir == x.dir && o.at == x.at; })) continue;
        x.id = "i" + std::to_string(next_import_++);
        x.imported = true;
        log_.insert(std::ranges::find_if(log_, [&](const LogEntry& o) { return o.at < x.at; }), x);
        ids[i] = x.id;
        changed = true;
    }
    if (log_.size() > max_log) log_.resize(max_log);
    json::Array out;
    for (auto& id : ids) {
        if (!id.empty() && std::ranges::none_of(log_, [&](const LogEntry& o) { return o.id == id; })) id.clear();
        out.push_back(id);
    }
    if (changed) save();
    return json::Object{{"ids", std::move(out)}};
}

// Deletes the imported entries among ids (an import undone).
json::Value Service::delete_log(const json::Value* ids) {
    size_t n = 0;
    if (ids && ids->is_array())
        for (const auto& id : std::get<json::Array>(*ids))
            if (id.is_string())
                n += std::erase_if(log_, [&](const LogEntry& e) { return e.imported && e.id == std::get<std::string>(id); });
    if (n) save();
    return json::Object{{"deleted", static_cast<long long>(n)}};
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
    if (method == "phonebook.read")
        return modem_->sim_contacts([reply = std::move(reply)](std::expected<std::vector<SimContact>, std::string> r) {
            if (!r) return reply(std::unexpected(r.error()));
            json::Array a;
            for (const auto& c : *r) a.push_back(c.to_json());
            reply(json::Object{{"contacts", std::move(a)}});
        });
    if (method == "dial" && sim_ && sim_->dial_latency() > std::chrono::milliseconds::zero())
        return sim_->after(sim_->dial_latency(), [this, p, reply = std::move(reply)] { reply(call("dial", p)); });
    // mms.connect: phone-smsd brings up the MMS access point for a transfer
    // and lets it go with mms.disconnect. The answer is the link's
    // interface and addresses (as data_ip) with shared false, or shared
    // true alone when MMS goes over the data link.
    if (method == "mms.connect")
        return modem_->mms_connect([reply = std::move(reply)](std::expected<std::optional<DataLink>, std::string> r) {
            if (!r) return reply(std::unexpected(r.error()));
            json::Value link = *r ? (*r)->to_json() : json::Object{};
            auto& o = std::get<json::Object>(link);
            o["shared"] = !r->has_value();
            reply(std::move(link));
        });
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
    if (method == "status") {
        Status s = modem_->status();
        check(s);
        return ok(s.to_json());
    }
    if (method == "radio.set" || method == "data.set") {
        bool on = p.flag("on");
        Result r = method == "radio.set" ? modem_->set_radio(on) : modem_->set_data(on);
        if (!r) return err(r.error());
        (method == "radio.set" ? radio_on_ : data_on_) = on;
        save_radio();
        return ok();
    }
    // roaming.set: on, the data roaming switch; national (kept when not
    // given), the national roaming switch.
    if (method == "roaming.set") {
        bool on = p.flag("on"), national = p.flag("national", national_on_);
        if (Result r = modem_->set_data_roaming(on, national); !r) return err(r.error());
        roaming_on_ = on, national_on_ = national;
        save_radio();
        return ok();
    }
    if (method == "apn.list" || method == "apn.set" || method == "apn.mms") return apn_call(method, p);
    if (method == "mms.disconnect") {
        modem_->mms_release();
        return ok();
    }
    if (method == "calls") {
        json::Array a;
        for (const auto& c : modem_->calls()) a.push_back(c.to_json());
        return ok(std::move(a));
    }
    if (method == "dial") {
        auto id = modem_->dial(p.str("number"));
        if (!id) {
            // Not a number: the user's typing stays out of the journal.
            if (valid_number(p.str("number"))) std::println(stderr, "phone-telephonyd: dial refused: {}", id.error());
            return err(id.error());
        }
        return ok(json::Object{{"call_id", *id}});
    }
    if (method == "answer" || method == "hangup" || method == "hold" || method == "mute") {
        std::string id = p.str("call_id");
        // A ringing call from a number on the block list: declined, logged as blocked.
        if (method == "hangup" && p.flag("blocked")) {
            blocked_.push_back(id);
            if (blocked_.size() > 16) blocked_.erase(blocked_.begin());
        }
        Result r = method == "answer" ? modem_->answer(id)
                 : method == "hangup" ? modem_->hangup(id)
                 : method == "hold"   ? modem_->hold(id, p.flag("on"))
                                      : modem_->mute(id, p.flag("on"));
        if (!r) {
            if (method == "answer") std::println(stderr, "phone-telephonyd: answering call {} refused: {}", id, r.error());
            return err(r.error());
        }
        return ok();
    }
    if (method == "dtmf") {
        if (Result r = modem_->send_dtmf(p.str("call_id"), p.str("tones")); !r) return err(r.error());
        return ok();
    }
    if (method == "log") {
        json::Array a;
        for (const auto& e : log_) a.push_back(e.to_json());
        return ok(std::move(a));
    }
    if (method == "log.import") return import_log(p.find("entries"));
    if (method == "log.delete") return ok(delete_log(p.find("ids")));
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
    if (method == "sim.ring" || method == "sim.sms" || method == "sim.cb" || method == "sim.lock" || method == "sim.ussd" || method == "sim.network" ||
        method == "sim.hangup" || method == "sim.slow" || method == "sim.roam" || method == "sim.ims") {
        if (!sim_) return err("not available with this modem backend");
        if (method == "sim.hangup") {
            if (Result r = sim_->remote_hangup(p.str("number")); !r) return err(r.error());
            return ok();
        }
        if (method == "sim.slow") {
            auto dial = integer(p.num("dial_ms"), 0, max_sim_latency_ms);
            auto answer = p.find("answer_ms") ? integer(p.num("answer_ms"), 0, max_sim_latency_ms) : SimModem::answer_after.count();
            if (!dial || !answer) return err("dial_ms and answer_ms: 0 to " + std::to_string(max_sim_latency_ms));
            sim_->set_latency(std::chrono::milliseconds(*dial), std::chrono::milliseconds(*answer));
            return ok();
        }
        if (method == "sim.cb") {
            auto channel = integer(p.num("channel"), 0, 0xffff), serial = integer(p.num("serial"), 0, 0xffff);
            if (!channel || !serial) return err("channel and serial are 16-bit");
            Result r = sim_->deliver_cb(static_cast<int>(*channel), static_cast<int>(*serial), p.str("text"), p.str("wac"));
            if (!r) return err(r.error());
            return ok();
        }
        if (method == "sim.network") {
            SimModem::Place place{.sim_mcc = p.str("sim_mcc"), .net_mcc = p.str("net_mcc")};
            if (p.find("nitz_offset")) {
                auto offset = integer(p.num("nitz_offset"), -max_zone_minutes, max_zone_minutes), dst = integer(p.num("nitz_dst"), -max_zone_minutes, max_zone_minutes);
                if (!offset || !dst) return err("nitz_offset and nitz_dst: minutes within a day");
                place.nitz = Nitz{.offset = static_cast<int>(*offset), .dst = static_cast<int>(*dst)};
            }
            for (auto [key, field] : {std::pair{"sim_mnc", &place.sim_mnc}, std::pair{"spn", &place.spn}, std::pair{"gid1", &place.gid1}})
                if (p.find(key)) *field = p.str(key);
            Result r = sim_->set_network(std::move(place));
            if (!r) return err(r.error());
            return ok();
        }
        if (method == "sim.roam") {
            Result r = sim_->roam(p.str("net_mcc"), p.str("net_mnc"), p.str("operator"));
            if (!r) return err(r.error());
            return ok();
        }
        if (method == "sim.ims") {
            if (Result r = sim_->set_ims(p.str("state")); !r) return err(r.error());
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
            auto cls = p.find("class") ? integer(p.num("class"), -1, 3) : -1;
            if (!cls) return err("class: 0-3, or -1 for none");
            unannounced_ = p.flag("held");
            Result r = sim_->deliver_sms(p.str("from"), p.str("text"), p.str("data"), static_cast<int>(*cls));
            unannounced_ = false;
            if (!r) return err(r.error());
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
    std::string path = (fs::path(dir_) / "telephony.json").string();
    auto r = statefile::read(path);
    statefile::say(path, r, "starting with no call log and nothing pending");
    if (r.found != statefile::Found::kWhole && r.found != statefile::Found::kCopy) return;
    const json::Value* v = &r.value;
    if (auto* log = v->find("log"); log && log->is_array())
        for (const auto& e : std::get<json::Array>(*log)) {
            log_.push_back(LogEntry{e.str("number"), e.str("dir"), e.str("result"),
                                    static_cast<long long>(e.num("at")), static_cast<long long>(e.num("duration")), e.str("id"), e.flag("imported")});
            if (const std::string& id = log_.back().id; id.size() > 1 && id[0] == 'i')
                next_import_ = std::max(next_import_, std::atoll(id.c_str() + 1) + 1);
        }
    if (auto* sms = v->find("sms"); sms && sms->is_array()) legacy_ = std::get<json::Array>(*sms);
    if (auto* pending = v->find("pending"); pending && pending->is_array())
        for (const auto& e : std::get<json::Array>(*pending))
            if (e.is_object() && e.find("key") && e.find("key")->is_string()) pending_.push_back(std::get<json::Object>(e));
}

// A received message is only on disk here until phone-smsd acknowledges it.
void Service::save() const {
    if (dir_.empty()) return;
    json::Array log, pending(pending_.begin(), pending_.end());
    for (const auto& e : log_) log.push_back(e.to_json());
    json::Object state{{"log", std::move(log)}, {"pending", std::move(pending)}};
    if (!legacy_.empty()) state["sms"] = legacy_;
    statefile::write((fs::path(dir_) / "telephony.json").string(), json::dump(state));
}

// Neither radio.json nor its copy whole: the radio stays off until the
// Shell sets it (syncRadio), as the switch kept may have been airplane mode;
// an emergency number still powers it up.
void Service::restore_radio() {
    if (!dir_.empty()) {
        std::string path = (fs::path(dir_) / "radio.json").string();
        auto r = statefile::read(path);
        statefile::say(path, r, "the radio off until the Shell sets it");
        if (r.found == statefile::Found::kWhole || r.found == statefile::Found::kCopy) {
            radio_on_ = r.value.flag("radio", true);
            data_on_ = r.value.flag("data", true);
            roaming_on_ = r.value.flag("roaming", false);
            national_on_ = r.value.flag("national", true);
        } else if (r.found == statefile::Found::kDamaged) {
            radio_on_ = false;
        }
    }
    if (!modem_) return;
    (void)modem_->set_data_roaming(roaming_on_, national_on_);
    (void)modem_->set_data(data_on_);
    (void)modem_->set_radio(radio_on_);
}

void Service::save_radio() const {
    if (dir_.empty()) return;
    statefile::write((fs::path(dir_) / "radio.json").string(),
                     json::dump(json::Object{{"radio", radio_on_}, {"data", data_on_}, {"roaming", roaming_on_}, {"national", national_on_}}));
}

}  // namespace tel
