#include "modem.hpp"

#include <algorithm>
#include <chrono>
#include <format>
#include <fstream>
#include <sstream>
#include <utility>

namespace tel {

using namespace std::chrono_literals;

long long unix_now() {
    return std::chrono::duration_cast<std::chrono::seconds>(std::chrono::system_clock::now().time_since_epoch()).count();
}

bool valid_number(std::string_view n) {
    if (n.size() < 3 || n.size() > 20) return false;
    for (size_t i = 0; i < n.size(); ++i) {
        char c = n[i];
        bool ok = (c >= '0' && c <= '9') || c == '*' || c == '#' || (c == '+' && i == 0);
        if (!ok) return false;
    }
    return true;
}

bool emergency_number(std::string_view n) {
    for (std::string_view e : {"112", "110", "119", "120", "122", "911", "999"})
        if (n == e) return true;
    return false;
}

bool valid_ussd(std::string_view c) {
    if (c.size() < 3 || c.size() > 32 || (c[0] != '*' && c[0] != '#') || c.back() != '#') return false;
    return std::ranges::all_of(c, [](char x) { return (x >= '0' && x <= '9') || x == '*' || x == '#'; });
}

json::Value Call::to_json() const {
    return json::Object{{"id", id}, {"number", number}, {"dir", dir}, {"state", state}, {"reason", reason},
                        {"started", started}, {"connected", connected}, {"ended", ended}, {"muted", muted}, {"emergency", emergency}};
}

json::Value Sms::to_json() const {
    json::Object o{{"id", id}, {"peer", peer}, {"dir", dir}, {"text", text}, {"at", at}, {"read", read}};
    if (!state.empty()) o["state"] = state;
    return o;
}

json::Value Cb::to_json() const {
    return json::Object{{"id", id}, {"channel", channel}, {"serial", serial}, {"text", text}, {"at", at}};
}

bool valid_pin(std::string_view pin) {
    return pin.size() >= 4 && pin.size() <= 8 && std::ranges::all_of(pin, [](char c) { return c >= '0' && c <= '9'; });
}

bool valid_mcc(std::string_view mcc) {
    return mcc.empty() || (mcc.size() == 3 && std::ranges::all_of(mcc, [](char c) { return c >= '0' && c <= '9'; }));
}

json::Value Nitz::to_json() const {
    return json::Object{{"offset", offset}, {"dst", dst}, {"time", time}, {"received", received}};
}

json::Value Status::to_json() const {
    json::Object o{{"registered", registered}, {"operator", op}, {"rat", rat}, {"own", own},
                   {"backend", backend}, {"signal", signal}, {"sim", sim}, {"sim_tries", sim_tries}, {"pin_lock", pin_lock},
                   {"imei", imei}, {"sim_mcc", sim_mcc}, {"net_mcc", net_mcc}};
    if (nitz) o["nitz"] = nitz->to_json();
    json::Array ifaces;
    for (const auto& i : data_ifaces) ifaces.push_back(i);
    o["radio"] = radio;
    o["data"] = data;
    o["data_state"] = data_state;
    o["data_ifaces"] = std::move(ifaces);
    return o;
}

json::Value Ussd::to_json() const {
    return json::Object{{"state", state}, {"text", text}};
}

std::expected<SimNetwork, std::string> SimNetwork::parse(std::string_view text) {
    auto v = json::parse(text);
    if (!v) return std::unexpected(v.error());
    if (!v->is_object()) return std::unexpected("not a JSON object");
    SimNetwork n;
    const std::map<std::string_view, std::string*> strings{
        {"operator", &n.op},
        {"sms_echo", &n.sms_echo},
        {"ussd_menu_code", &n.ussd_menu_code},
        {"ussd_menu", &n.ussd_menu},
        {"ussd_menu_retry", &n.ussd_menu_retry},
        {"ussd_unknown", &n.ussd_unknown},
        {"ussd_received", &n.ussd_received},
        {"sim_mcc", &n.sim_mcc},
        {"net_mcc", &n.net_mcc},
    };
    const std::map<std::string_view, int Nitz::*> zone{{"nitz_offset", &Nitz::offset}, {"nitz_dst", &Nitz::dst}};
    const std::map<std::string_view, Texts*> tables{
        {"sms_replies", &n.sms_replies},
        {"ussd_answers", &n.ussd_answers},
        {"ussd_menu_answers", &n.ussd_menu_answers},
    };
    for (const auto& [key, val] : std::get<json::Object>(*v)) {
        if (auto s = strings.find(key); s != strings.end()) {
            if (!val.is_string()) return std::unexpected(std::format("{}: not a string", key));
            *s->second = std::get<std::string>(val);
        } else if (auto t = tables.find(key); t != tables.end()) {
            if (!val.is_object()) return std::unexpected(std::format("{}: not an object", key));
            Texts texts;
            for (const auto& [k, s] : std::get<json::Object>(val)) {
                if (!s.is_string()) return std::unexpected(std::format("{}.{}: not a string", key, k));
                texts.emplace(k, std::get<std::string>(s));
            }
            *t->second = std::move(texts);
        } else if (auto z = zone.find(key); z != zone.end()) {
            if (!std::holds_alternative<double>(val)) return std::unexpected(std::format("{}: not a number", key));
            if (!n.nitz) n.nitz = Nitz{};
            (*n.nitz).*(z->second) = static_cast<int>(std::get<double>(val));
        } else {
            return std::unexpected(std::format("unknown member '{}'", key));
        }
    }
    if (!valid_mcc(n.sim_mcc) || !valid_mcc(n.net_mcc)) return std::unexpected("sim_mcc, net_mcc: three digits");
    return n;
}

std::expected<SimNetwork, std::string> SimNetwork::load(const std::string& path) {
    std::ifstream in(path);
    if (!in) return std::unexpected(std::format("{}: cannot read it", path));
    std::stringstream ss;
    ss << in.rdbuf();
    auto n = parse(ss.str());
    if (!n) return std::unexpected(std::format("{}: {}", path, n.error()));
    return n;
}

// A test IMEI: TAC 86012345, serial 678901, Luhn check digit 4.
constexpr const char* kSimImei = "860123456789014";

Status SimModem::status() const {
    Status s;
    if (!pin_.empty()) {
        s = Status{.backend = "sim", .sim = puk_ ? "puk" : "pin", .sim_tries = tries_, .pin_lock = lock_on_, .imei = kSimImei};
    } else if (!radio_) {
        s = Status{.own = "13800138000", .backend = "sim", .pin_lock = lock_on_, .imei = kSimImei, .sim_mcc = net_.sim_mcc};
    } else {
        s = Status{.registered = true, .op = net_.op, .rat = "4G", .own = "13800138000", .backend = "sim", .signal = 3,
                   .pin_lock = lock_on_, .imei = kSimImei, .sim_mcc = net_.sim_mcc,
                   .net_mcc = net_.net_mcc.empty() ? net_.sim_mcc : net_.net_mcc, .nitz = net_.nitz};
        if (s.nitz && s.nitz->time == 0) s.nitz->time = s.nitz->received = boot_;
    }
    s.radio = radio_;
    s.data = data_;
    s.data_state = !radio_ || !data_ ? "off" : s.registered ? "connected" : "idle";
    s.data_ifaces = {kSimDataIface};
    return s;
}

Result SimModem::set_radio(bool on) {
    if (on == radio_) return {};
    if (!on) {
        for (const auto& [_, c] : calls_)
            if (c.emergency) return std::unexpected("an emergency call is in progress");
        std::vector<std::string> ids;
        for (const auto& [id, _] : calls_) ids.push_back(id);
        for (const auto& id : ids)
            if (Call* c = find(id)) set_state(*c, "ended", c->dir == "in" && !c->connected ? "missed" : "dropped");
        end_ussd();
    }
    radio_ = on;
    if (ev_.status) ev_.status(status());
    return {};
}

Result SimModem::set_data(bool on) {
    if (on == data_) return {};
    data_ = on;
    if (ev_.status) ev_.status(status());
    return {};
}

Result SimModem::set_network(std::string_view sim_mcc, std::string_view net_mcc, std::optional<Nitz> nitz) {
    if (!valid_mcc(sim_mcc) || !valid_mcc(net_mcc)) return std::unexpected("a mobile country code is three digits");
    net_.sim_mcc = sim_mcc;
    net_.net_mcc = net_mcc;
    if (nitz) nitz->time = nitz->received = unix_now();
    net_.nitz = nitz;
    if (ev_.status) ev_.status(status());
    return {};
}

void SimModem::lock(std::string_view pin) {
    pin_ = code_ = pin;
    lock_on_ = true;
    tries_ = 3;
    puk_ = false;
    end_ussd();
    if (ev_.status) ev_.status(status());
}

Result SimModem::unlock(std::string_view pin, std::string_view puk) {
    if (pin_.empty()) return std::unexpected("the SIM is not locked");
    if (!valid_pin(pin) || (puk_ && (puk.size() != 8 || !valid_pin(puk)))) return std::unexpected("a PIN is 4-8 digits, a PUK 8");
    if (tries_ == 0) return std::unexpected("the SIM is blocked");
    bool ok = puk_ ? puk == "12345678" : pin == pin_;
    if (ok) {
        if (puk_) code_ = pin;  // the PUK sets a new PIN
        pin_.clear();
        puk_ = false;
        checks_ = 3;
    } else if (--tries_ == 0 && !puk_) {
        puk_ = true;
        tries_ = 10;
    }
    loop_.after(200ms, [this, ok] {
        if (ev_.unlock) ev_.unlock(ok ? "" : "incorrect PIN or PUK");
        if (ev_.status) ev_.status(status());
    });
    return {};
}

bool SimModem::check(std::string_view pin) {
    if (pin == code_) {
        checks_ = 3;
        return true;
    }
    if (--checks_ == 0) {
        // Out of tries: the SIM locks itself and wants its PUK.
        pin_ = code_;
        puk_ = true;
        tries_ = 10;
    }
    return false;
}

void SimModem::settle_pin(bool ok) {
    loop_.after(200ms, [this, ok] {
        if (ev_.pin) ev_.pin(ok ? "" : "incorrect PIN");
        if (ev_.status) ev_.status(status());
    });
}

Result SimModem::set_pin_lock(std::string_view pin, bool on) {
    if (!pin_.empty()) return std::unexpected("the SIM is locked");
    if (!valid_pin(pin)) return std::unexpected("a PIN is 4-8 digits");
    bool ok = check(pin);
    if (ok) lock_on_ = on;
    settle_pin(ok);
    return {};
}

Result SimModem::change_pin(std::string_view pin, std::string_view new_pin) {
    if (!pin_.empty()) return std::unexpected("the SIM is locked");
    if (!lock_on_) return std::unexpected("turn the PIN lock on first");
    if (!valid_pin(pin) || !valid_pin(new_pin)) return std::unexpected("a PIN is 4-8 digits");
    bool ok = check(pin);
    if (ok) code_ = new_pin;
    settle_pin(ok);
    return {};
}

std::vector<Call> SimModem::calls() const {
    std::vector<Call> out;
    for (const auto& [_, c] : calls_)
        if (c.state != "ended") out.push_back(c);
    return out;
}

Call* SimModem::find(std::string_view id) {
    auto it = calls_.find(std::string(id));
    return it == calls_.end() || it->second.state == "ended" ? nullptr : &it->second;
}

void SimModem::set_state(Call& c, std::string state, std::string reason) {
    c.state = std::move(state);
    if (c.state == "active" && c.connected == 0) c.connected = unix_now();
    if (c.state != "ended") {
        if (ev_.call) ev_.call(c);
        return;
    }
    c.ended = unix_now();
    c.reason = std::move(reason);
    Call done = std::move(c);
    calls_.erase(done.id);
    if (ev_.call) ev_.call(done);
}

std::expected<std::string, std::string> SimModem::dial(std::string_view number) {
    if (!valid_number(number)) return std::unexpected(std::format("invalid number '{}'", number));
    if (!pin_.empty()) return std::unexpected("the SIM is locked");
    bool sos = emergency_number(number);
    if (!radio_ && !sos) return std::unexpected("the radio is off");
    for (const auto& c : calls()) {
        if (c.state != "held") return std::unexpected("another call is in progress");
    }
    std::string id = std::format("c{}", ++next_call_);
    Call& c = calls_[id] = Call{.id = id, .number = std::string(number), .dir = "out", .state = "dialing", .started = unix_now(),
                                .emergency = sos};
    if (ev_.call) ev_.call(c);
    std::chrono::milliseconds up{0};
    if (!radio_) {
        up = 500ms;
        loop_.after(up, [this] {
            if (radio_) return;
            radio_ = true;
            if (ev_.status) ev_.status(status());
        });
    }
    bool busy = number.ends_with("000") && !sos;
    loop_.after(up + 300ms, [this, id] {
        if (Call* c = find(id); c && c->state == "dialing") set_state(*c, "alerting");
    });
    loop_.after(up + 1200ms, [this, id, busy] {
        Call* c = find(id);
        if (!c || c->state != "alerting") return;
        if (busy) set_state(*c, "ended", "busy");
        else set_state(*c, "active");
    });
    return id;
}

std::expected<std::string, std::string> SimModem::ring(std::string_view number) {
    if (!valid_number(number)) return std::unexpected(std::format("invalid number '{}'", number));
    if (!radio_) return std::unexpected("the radio is off");
    std::string id = std::format("c{}", ++next_call_);
    Call& c = calls_[id] = Call{.id = id, .number = std::string(number), .dir = "in", .state = "incoming", .started = unix_now()};
    if (ev_.call) ev_.call(c);
    // Unanswered calls stop ringing after 30 s, like a network would.
    loop_.after(30s, [this, id] {
        if (Call* c = find(id); c && c->state == "incoming") set_state(*c, "ended", "missed");
    });
    return id;
}

Result SimModem::answer(std::string_view id) {
    Call* c = find(id);
    if (!c || c->state != "incoming") return std::unexpected("no incoming call with that id");
    set_state(*c, "active");
    return {};
}

Result SimModem::hangup(std::string_view id) {
    Call* c = find(id);
    if (!c) return std::unexpected("no such call");
    set_state(*c, "ended", c->state == "incoming" ? "rejected" : "hangup");
    return {};
}

Result SimModem::hold(std::string_view id, bool on) {
    Call* c = find(id);
    if (!c || (c->state != "active" && c->state != "held")) return std::unexpected("call is not connected");
    set_state(*c, on ? "held" : "active");
    return {};
}

Result SimModem::mute(std::string_view id, bool on) {
    Call* c = find(id);
    if (!c) return std::unexpected("no such call");
    c->muted = on;
    if (ev_.call) ev_.call(*c);
    return {};
}

std::expected<std::string, std::string> SimModem::send_sms(std::string_view to, std::string_view text, std::string_view ref) {
    if (!valid_number(to)) return std::unexpected(std::format("invalid number '{}'", to));
    if (!pin_.empty()) return std::unexpected("the SIM is locked");
    if (!radio_) return std::unexpected("the radio is off");
    if (text.empty()) return std::unexpected("empty message");
    std::string id = ref.empty() ? std::format("s{}-{}", boot_, ++next_sms_) : std::string(ref);
    bool fails = to.ends_with("999");
    Sms out{.id = id, .peer = std::string(to), .dir = "out", .text = std::string(text), .at = unix_now(), .read = true,
            .state = fails ? "failed" : "sent"};
    if (ev_.sms) ev_.sms(out);
    if (fails) return id;
    if (!to.ends_with("998"))
        loop_.after(500ms, [this, out]() mutable {
            out.state = "delivered";
            if (ev_.sms) ev_.sms(out);
        });
    auto canned = net_.sms_replies.find(to);
    std::string reply = canned != net_.sms_replies.end() ? canned->second : net_.sms_echo + std::string(text);
    loop_.after(800ms, [this, peer = std::string(to), reply] { deliver_sms(peer, reply); });
    return id;
}

void SimModem::deliver_sms(std::string_view from, std::string_view text) {
    std::string id = std::format("s{}-{}", boot_, ++next_sms_);
    if (ev_.sms) ev_.sms(Sms{.id = id, .peer = std::string(from), .dir = "in", .text = std::string(text), .at = unix_now()});
}

Result SimModem::deliver_cb(int channel, int serial, std::string_view text) {
    if (channel < 0 || channel > 0xffff || serial < 0 || serial > 0xffff) return std::unexpected("channel and serial are 16-bit");
    if (text.empty()) return std::unexpected("empty message");
    if (ev_.cb)
        ev_.cb(Cb{.id = std::format("b{}-{}", boot_, ++next_cb_), .channel = channel, .serial = serial, .text = std::string(text),
                  .at = unix_now()});
    return {};
}

void SimModem::ussd_initiate(std::string_view code, UssdReply done) {
    if (!valid_ussd(code)) return done(std::unexpected("invalid code"));
    if (!pin_.empty()) return done(std::unexpected("the SIM is locked"));
    if (!radio_) return done(std::unexpected("the radio is off"));
    if (!ussd_.empty()) return done(std::unexpected("ussd session open"));
    if (auto a = net_.ussd_answers.find(code); a != net_.ussd_answers.end()) return done(Ussd{"done", a->second});
    if (code == net_.ussd_menu_code) {
        ussd_ = "menu";
        return done(Ussd{"input", net_.ussd_menu});
    }
    done(Ussd{"done", net_.ussd_unknown});
}

void SimModem::ussd_respond(std::string_view text, UssdReply done) {
    if (ussd_.empty()) return done(std::unexpected("no ussd session"));
    std::string session = std::exchange(ussd_, {});
    if (session == "network") return done(Ussd{"done", net_.ussd_received});
    if (auto a = net_.ussd_menu_answers.find(text); a != net_.ussd_menu_answers.end()) return done(Ussd{"done", a->second});
    ussd_ = session;
    done(Ussd{"input", net_.ussd_menu_retry + "\n" + net_.ussd_menu});
}

Result SimModem::ussd_cancel() {
    ussd_.clear();
    return {};
}

Result SimModem::network_ussd(std::string_view text, bool request) {
    if (!pin_.empty()) return std::unexpected("the SIM is locked");
    if (request && !ussd_.empty()) return std::unexpected("ussd session open");
    if (request) ussd_ = "network";
    if (ev_.ussd) ev_.ussd(Ussd{request ? "input" : "notify", std::string(text)});
    return {};
}

void SimModem::end_ussd() {
    if (ussd_.empty()) return;
    ussd_.clear();
    if (ev_.ussd) ev_.ussd(Ussd{"done", ""});
}

}  // namespace tel
