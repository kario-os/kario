#include "modem.hpp"

#include <algorithm>
#include <cctype>
#include <chrono>
#include <format>
#include <fstream>
#include <sstream>
#include <utility>

#include "ims.hpp"

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

std::expected<std::string, std::string> second_call(const std::vector<Call>& calls) {
    std::string active;
    int connected = 0;
    for (const auto& c : calls) {
        if (c.state == "dialing" || c.state == "alerting") return std::unexpected("another call is being set up");
        if (c.state == "incoming") return std::unexpected("a call is ringing");
        if (c.state == "active" && c.emergency) return std::unexpected("an emergency call is in progress");
        if (c.state == "active") active = c.id;
        if (c.state == "active" || c.state == "held") ++connected;
    }
    if (connected >= 2) return std::unexpected("two calls already");
    return active;
}

namespace {

// A phone book number: digits, * and #, + first, as many as an ADN record
// with its extension records holds.
bool valid_sim_number(std::string_view n) {
    if (n.empty() || n.size() > 40) return false;
    for (size_t i = 0; i < n.size(); ++i) {
        char c = n[i];
        if (!((c >= '0' && c <= '9') || c == '*' || c == '#' || (c == '+' && i == 0))) return false;
    }
    return true;
}

}  // namespace

bool valid_dtmf(std::string_view tones) {
    if (tones.empty() || tones.size() > 32) return false;
    for (char c : tones)
        if (!((c >= '0' && c <= '9') || c == '*' || c == '#' || (c >= 'A' && c <= 'D'))) return false;
    return true;
}

bool emergency_number(std::string_view n, const std::vector<std::string>& extra) {
    for (const auto& e : extra)
        if (n == e) return true;
    for (std::string_view e : {"112", "110", "119", "120", "122", "911", "999"})
        if (n == e) return true;
    return false;
}

bool valid_ussd(std::string_view c) {
    if (c.size() < 3 || c.size() > 32 || (c[0] != '*' && c[0] != '#') || c.back() != '#') return false;
    return std::ranges::all_of(c, [](char x) { return (x >= '0' && x <= '9') || x == '*' || x == '#'; });
}

void Call::add_dtmf(std::string_view tones) {
    dtmf += tones;
    if (dtmf.size() > kMaxDtmf) dtmf.erase(0, dtmf.size() - kMaxDtmf);
}

json::Value Call::to_json() const {
    json::Object o{{"id", id}, {"number", number}, {"dir", dir}, {"state", state}, {"reason", reason},
                   {"started", started}, {"connected", connected}, {"ended", ended}, {"muted", muted}, {"emergency", emergency}};
    if (!dtmf.empty()) o["dtmf"] = dtmf;
    if (!error.empty()) o["error"] = error;
    if (!ims.empty()) o["ims"] = ims;
    return o;
}

json::Value Sms::to_json() const {
    json::Object o{{"id", id}, {"peer", peer}, {"dir", dir}, {"text", text}, {"at", at}, {"read", read}};
    if (!state.empty()) o["state"] = state;
    if (!data.empty()) o["data"] = data;
    if (sms_class >= 0) o["class"] = sms_class;
    return o;
}

json::Value Cb::to_json() const {
    json::Object o{{"id", id}, {"channel", channel}, {"serial", serial}, {"text", text}, {"at", at}};
    if (!wac.empty()) o["wac"] = wac;
    return o;
}

bool valid_hex(std::string_view hex, size_t max_bytes) {
    return hex.size() % 2 == 0 && hex.size() / 2 <= max_bytes && std::ranges::all_of(hex, [](char c) {
        return (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F');
    });
}

std::string to_hex(std::string_view bytes) {
    static constexpr char digits[] = "0123456789abcdef";
    std::string out;
    out.reserve(bytes.size() * 2);
    for (unsigned char c : bytes) {
        out += digits[c >> 4];
        out += digits[c & 0xf];
    }
    return out;
}

bool valid_pin(std::string_view pin) {
    return pin.size() >= 4 && pin.size() <= 8 && std::ranges::all_of(pin, [](char c) { return c >= '0' && c <= '9'; });
}

bool valid_mcc(std::string_view mcc) {
    return mcc.empty() || (mcc.size() == 3 && std::ranges::all_of(mcc, [](char c) { return c >= '0' && c <= '9'; }));
}

bool valid_mnc(std::string_view mnc) {
    return mnc.empty() || ((mnc.size() == 2 || mnc.size() == 3) && std::ranges::all_of(mnc, [](char c) { return c >= '0' && c <= '9'; }));
}

bool same_country(std::string_view a, std::string_view b) {
    if (a.empty() || b.empty()) return false;
    if (a == b) return true;
    struct Range {
        std::string_view lo, hi;
    };
    for (auto [lo, hi] : {Range{"310", "316"}, Range{"404", "406"}, Range{"440", "441"}, Range{"234", "235"}})
        if (a >= lo && a <= hi && b >= lo && b <= hi) return true;
    return false;
}

std::string roaming_kind(bool roaming, std::string_view sim_mcc, std::string_view net_mcc) {
    if (!roaming) return "";
    return same_country(sim_mcc, net_mcc) ? "national" : "international";
}

bool roaming_allowed(std::string_view kind, bool data_roaming, bool national_roaming) {
    if (kind == "national") return national_roaming;
    return kind.empty() || data_roaming;
}

bool valid_iface(std::string_view name) {
    return !name.empty() && name.size() <= 15 && name[0] != '.' && std::ranges::all_of(name, [](unsigned char c) {
        return std::isalnum(c) || c == '_' || c == '.' || c == '-';
    });
}

namespace {

void link_problems(std::vector<std::string>& out, const char* what, const DataLink& l, const std::vector<std::string>& ifaces) {
    if (!valid_iface(l.iface)) out.push_back(std::format("{}: interface '{}' is not a network interface name", what, l.iface));
    else if (std::ranges::find(ifaces, l.iface) == ifaces.end()) out.push_back(std::format("{}: interface {} is not in data_ifaces", what, l.iface));
    for (const auto* ip : {&l.ipv4, &l.ipv6}) {
        if (!*ip) continue;
        const IpConfig& c = **ip;
        if (c.method != "static" && c.method != "dhcp" && c.method != "ppp") out.push_back(std::format("{}: method '{}'", what, c.method));
        int max = ip == &l.ipv4 ? 32 : 128;
        if (c.method == "static" && (c.address.empty() || c.prefix < 1 || c.prefix > max))
            out.push_back(std::format("{}: static without an address and a prefix of 1-{}", what, max));
    }
}

}  // namespace

std::vector<std::string> contract_problems(const Status& s) {
    std::vector<std::string> out;
    if (s.backend.empty()) out.push_back("backend: not named");
    static constexpr std::string_view sims[] = {"ready", "pin", "puk", "locked", "absent", "error"};
    if (std::ranges::find(sims, s.sim) == std::end(sims)) out.push_back(std::format("sim: '{}'", s.sim));
    if (s.sim_tries != 0 && s.sim != "pin" && s.sim != "puk") out.push_back("sim_tries: while the SIM asks for no PIN or PUK");
    if (s.signal < 0 || s.signal > 4) out.push_back(std::format("signal: {} bars", s.signal));
    if (!s.registered && s.signal != 0) out.push_back("signal: while not registered");
    if (s.roaming != "" && s.roaming != "national" && s.roaming != "international") out.push_back(std::format("roaming: '{}'", s.roaming));
    if (!s.roaming.empty() && !s.registered) out.push_back("roaming: while not registered");
    static constexpr std::string_view states[] = {"connected", "idle", "roaming", "off"};
    if (std::ranges::find(states, s.data_state) == std::end(states)) out.push_back(std::format("data_state: '{}'", s.data_state));
    if (s.data_state != "off" && (!s.radio || !s.data)) out.push_back(std::format("data_state: {} with the radio {} and data {}", s.data_state, s.radio ? "on" : "off", s.data ? "on" : "off"));
    if (s.data_state == "roaming" && roaming_allowed(s.roaming, s.data_roaming, s.national_roaming))
        out.push_back("data_state: roaming where the roaming switches allow the link");
    if ((s.data_state == "connected") != s.data_link.has_value()) out.push_back(std::format("data_ip: {} while data_state is {}", s.data_link ? "given" : "none", s.data_state));
    for (const auto& i : s.data_ifaces)
        if (!valid_iface(i)) out.push_back(std::format("data_ifaces: '{}' is not a network interface name", i));
    if (s.data_link) link_problems(out, "data_ip", *s.data_link, s.data_ifaces);
    if (s.mms_link) link_problems(out, "mms_link", *s.mms_link, s.data_ifaces);
    if (!valid_ims(s.ims)) out.push_back(std::format("ims: '{}'", s.ims));
    else if (s.ims != "unknown" && !(s.registered && (s.rat == "4G" || s.rat == "5G")))
        out.push_back(std::format("ims: {} while not registered on 4G or 5G", s.ims));
    return out;
}

json::Value IpConfig::to_json() const {
    json::Array d;
    for (const auto& x : dns) d.push_back(x);
    json::Object o{{"method", method}, {"dns", std::move(d)}};
    if (!address.empty()) {
        o["address"] = address;
        o["prefix"] = prefix;
    }
    if (!gateway.empty()) o["gateway"] = gateway;
    return o;
}

json::Value DataLink::to_json() const {
    json::Object o{{"iface", iface}, {"mtu", mtu}};
    if (ipv4) o["ipv4"] = ipv4->to_json();
    if (ipv6) o["ipv6"] = ipv6->to_json();
    return o;
}

DataLink sim_link(std::string_view ip_type) {
    DataLink l{.iface = kSimDataIface, .mtu = 1400};
    if (ip_type != "ipv6")
        l.ipv4 = IpConfig{.method = "static", .address = "100.80.12.34", .gateway = "100.80.12.33", .prefix = 30, .dns = {"100.80.0.53", "100.80.0.54"}};
    if (ip_type != "ipv4")
        l.ipv6 = IpConfig{.method = "static", .address = "2001:db8:4600:12::34", .gateway = "fe80::1", .prefix = 64, .dns = {"2001:db8:4600::53"}};
    return l;
}

json::Value Nitz::to_json() const {
    return json::Object{{"offset", offset}, {"dst", dst}, {"time", time}, {"received", received}};
}

json::Value Status::to_json() const {
    json::Object o{{"registered", registered}, {"operator", op}, {"rat", rat}, {"own", own},
                   {"backend", backend}, {"signal", signal}, {"sim", sim}, {"sim_tries", sim_tries}, {"pin_lock", pin_lock},
                   {"imei", imei}, {"sim_mcc", sim_mcc}, {"net_mcc", net_mcc}, {"sim_mnc", sim_mnc}, {"net_mnc", net_mnc},
                   {"spn", spn}, {"gid1", gid1}, {"roaming", roaming}, {"ims", ims}};
    if (nitz) o["nitz"] = nitz->to_json();
    json::Array ifaces;
    for (const auto& i : data_ifaces) ifaces.push_back(i);
    o["radio"] = radio;
    o["data"] = data;
    o["data_roaming"] = data_roaming;
    o["national_roaming"] = national_roaming;
    o["data_state"] = data_state;
    o["data_ifaces"] = std::move(ifaces);
    o["data_apn"] = data_apn;
    if (data_link) o["data_ip"] = data_link->to_json();
    if (mms_link) o["mms_link"] = mms_link->to_json();
    if (!emergency.empty()) {
        json::Array e;
        for (const auto& n : emergency) e.push_back(n);
        o["emergency_numbers"] = std::move(e);
    }
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
        {"sim_mnc", &n.sim_mnc},
        {"net_mnc", &n.net_mnc},
        {"spn", &n.spn},
        {"gid1", &n.gid1},
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
        } else if (key == "emergency_numbers") {
            if (!val.is_array()) return std::unexpected("emergency_numbers: not an array");
            for (const auto& e : std::get<json::Array>(val)) {
                if (!e.is_string() || !valid_number(std::get<std::string>(e)))
                    return std::unexpected("emergency_numbers: numbers of 3 to 20 digits");
                n.emergency.push_back(std::get<std::string>(e));
            }
        } else if (key == "sim_contacts") {
            if (!val.is_array()) return std::unexpected("sim_contacts: not an array");
            n.contacts.clear();
            for (const auto& e : std::get<json::Array>(val)) {
                if (!e.is_object() || !e.find("name") || !e.find("number") || !e.find("name")->is_string() ||
                    !valid_sim_number(e.str("number")))
                    return std::unexpected("sim_contacts: objects with a name and a number");
                n.contacts.push_back(SimContact{static_cast<int>(n.contacts.size()) + 1, e.str("name"), e.str("number")});
            }
        } else if (auto z = zone.find(key); z != zone.end()) {
            if (!std::holds_alternative<double>(val)) return std::unexpected(std::format("{}: not a number", key));
            if (!n.nitz) n.nitz = Nitz{};
            (*n.nitz).*(z->second) = static_cast<int>(std::get<double>(val));
        } else {
            return std::unexpected(std::format("unknown member '{}'", key));
        }
    }
    if (!valid_mcc(n.sim_mcc) || !valid_mcc(n.net_mcc)) return std::unexpected("sim_mcc, net_mcc: three digits");
    if (!valid_mnc(n.sim_mnc) || !valid_mnc(n.net_mnc)) return std::unexpected("sim_mnc, net_mnc: two or three digits");
    if (!std::ranges::all_of(n.gid1, [](unsigned char c) { return std::isxdigit(c); })) return std::unexpected("gid1: hex");
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
        s = Status{.own = "13800138000", .backend = "sim", .pin_lock = lock_on_, .imei = kSimImei, .sim_mcc = net_.sim_mcc,
                   .sim_mnc = net_.sim_mnc, .spn = net_.spn, .gid1 = net_.gid1};
    } else {
        s = Status{.registered = true, .op = net_.op, .rat = "4G", .own = "13800138000", .backend = "sim", .signal = 3,
                   .pin_lock = lock_on_, .imei = kSimImei, .sim_mcc = net_.sim_mcc,
                   .net_mcc = net_.net_mcc.empty() ? net_.sim_mcc : net_.net_mcc, .sim_mnc = net_.sim_mnc,
                   .net_mnc = net_.net_mnc.empty() ? net_.sim_mnc : net_.net_mnc, .spn = net_.spn, .gid1 = net_.gid1, .nitz = net_.nitz};
        if (s.nitz && s.nitz->time == 0) s.nitz->time = s.nitz->received = boot_;
        s.roaming = roaming_kind(roaming_, s.sim_mcc, s.net_mcc);
        if (roaming_) s.op = roaming_op_.empty() ? s.net_mcc + s.net_mnc : roaming_op_;
        s.ims = ims_;
    }
    s.radio = radio_;
    s.data = data_;
    s.data_roaming = data_roaming_;
    s.national_roaming = national_;
    s.data_state = !radio_ || !data_                                     ? "off"
                 : !s.registered                                         ? "idle"
                 : !roaming_allowed(s.roaming, data_roaming_, national_) ? "roaming"
                 : !apn_                                                 ? "idle"
                                                                         : "connected";
    s.data_ifaces = {kSimDataIface};
    if (apn_) s.data_apn = apn_->apn;
    if (s.data_state == "connected") s.data_link = sim_link(apn_->ip_type);
    s.emergency = net_.emergency;
    return s;
}

Result SimModem::set_data_roaming(bool on, bool national) {
    if (on == data_roaming_ && national == national_) return {};
    data_roaming_ = on;
    national_ = national;
    if (ev_.status) ev_.status(status());
    return {};
}

void SimModem::set_apn(const ApnPlan& plan) {
    if (apn_ && *apn_ == plan.internet) return;
    apn_ = plan.internet;
    if (ev_.status) ev_.status(status());
}

Result SimModem::roam(std::string_view net_mcc, std::string_view net_mnc, std::string_view name) {
    if (!valid_mcc(net_mcc) || !valid_mnc(net_mnc) || net_mcc.empty() != net_mnc.empty())
        return std::unexpected("a network is its mobile country code (three digits) and network code (two or three)");
    roaming_ = !net_mcc.empty();
    net_.net_mcc = net_mcc;
    net_.net_mnc = net_mnc;
    roaming_op_ = name;
    if (ev_.status) ev_.status(status());
    return {};
}

Result SimModem::set_ims(std::string_view state) {
    if (!valid_ims(state)) return std::unexpected("an IMS registration is registered, registering, not-registered, limited-registered or unknown");
    if (state == ims_) return {};
    ims_ = state;
    if (ev_.status) ev_.status(status());
    return {};
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

Result SimModem::set_network(Place p) {
    if (!valid_mcc(p.sim_mcc) || !valid_mcc(p.net_mcc)) return std::unexpected("a mobile country code is three digits");
    if (p.sim_mnc && !valid_mnc(*p.sim_mnc)) return std::unexpected("a mobile network code is two or three digits");
    if (p.gid1 && !std::ranges::all_of(*p.gid1, [](unsigned char c) { return std::isxdigit(c); })) return std::unexpected("gid1 is hex");
    net_.sim_mcc = p.sim_mcc;
    net_.net_mcc = p.net_mcc;
    if (p.sim_mnc) net_.sim_mnc = *p.sim_mnc;
    if (p.spn) net_.spn = *p.spn;
    if (p.gid1) net_.gid1 = *p.gid1;
    if (p.nitz) p.nitz->time = p.nitz->received = unix_now();
    net_.nitz = p.nitz;
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
    bool sos = emergency_number(number, net_.emergency);
    if (!pin_.empty() && !sos) return std::unexpected("the SIM is locked");
    if (!radio_ && !sos) return std::unexpected("the radio is off");
    auto hold = second_call(calls());
    if (!hold) return std::unexpected(hold.error());
    if (Call* other = find(*hold)) set_state(*other, "held");
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
    loop_.after(up + 300ms + answer_latency_, [this, id, busy] {
        Call* c = find(id);
        if (!c || c->state != "alerting") return;
        if (busy) set_state(*c, "ended", "busy");
        else set_state(*c, "active");
    });
    return id;
}

void SimModem::set_latency(std::chrono::milliseconds dial, std::chrono::milliseconds answer) {
    dial_latency_ = std::max(dial, 0ms);
    answer_latency_ = std::max(answer, 0ms);
}

Result SimModem::remote_hangup(std::string_view number) {
    for (auto& [_, c] : calls_)
        if (c.number == number && c.state != "ended") {
            set_state(c, "ended", c.state == "incoming" ? "missed" : "remote");
            return {};
        }
    return std::unexpected("no call with that number");
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
    hold_active(id);
    set_state(*c, "active");
    return {};
}

void SimModem::hold_active(std::string_view except) {
    for (auto& [k, o] : calls_)
        if (k != except && o.state == "active") set_state(o, "held");
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
    if (on == (c->state == "held")) return {};
    if (!on) hold_active(id);
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

Result SimModem::send_dtmf(std::string_view id, std::string_view tones) {
    if (!valid_dtmf(tones)) return std::unexpected(std::format("invalid DTMF '{}'", tones));
    Call* c = find(id);
    if (!c || c->state != "active") return std::unexpected("call is not connected");
    c->add_dtmf(tones);
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
    loop_.after(800ms, [this, peer = std::string(to), reply] { (void)deliver_sms(peer, reply); });
    return id;
}

// A WAP push of several concatenated parts stays well under this.
constexpr size_t max_sms_data = 4096, max_wac = 1024;

Result SimModem::deliver_sms(std::string_view from, std::string_view text, std::string_view data, int sms_class) {
    if (!valid_hex(data, max_sms_data)) return std::unexpected("data: hex, at most 4096 bytes");
    if (sms_class < -1 || sms_class > 3) return std::unexpected("class: 0-3, or -1 for none");
    std::string id = std::format("s{}-{}", boot_, ++next_sms_);
    if (ev_.sms)
        ev_.sms(Sms{.id = id, .peer = std::string(from), .dir = "in", .text = std::string(text), .at = unix_now(), .data = std::string(data),
                    .sms_class = sms_class});
    return {};
}

Result SimModem::deliver_cb(int channel, int serial, std::string_view text, std::string_view wac) {
    if (channel < 0 || channel > 0xffff || serial < 0 || serial > 0xffff) return std::unexpected("channel and serial are 16-bit");
    if (text.empty()) return std::unexpected("empty message");
    if (!valid_hex(wac, max_wac)) return std::unexpected("wac: hex, at most 1024 bytes");
    if (ev_.cb)
        ev_.cb(Cb{.id = std::format("b{}-{}", boot_, ++next_cb_), .channel = channel, .serial = serial, .text = std::string(text),
                  .at = unix_now(), .wac = std::string(wac)});
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

void SimModem::sim_contacts(SimContactsReply done) {
    if (!pin_.empty()) return done(std::unexpected("the SIM is locked"));
    done(net_.contacts);
}

json::Value SimContact::to_json() const {
    return json::Object{{"index", index}, {"name", name}, {"number", number}};
}

namespace {

void append_utf8(std::string& out, char32_t c) {
    if (c < 0x80) {
        out += static_cast<char>(c);
    } else if (c < 0x800) {
        out += static_cast<char>(0xC0 | (c >> 6));
        out += static_cast<char>(0x80 | (c & 0x3F));
    } else if (c < 0x10000) {
        out += static_cast<char>(0xE0 | (c >> 12));
        out += static_cast<char>(0x80 | ((c >> 6) & 0x3F));
        out += static_cast<char>(0x80 | (c & 0x3F));
    } else {
        out += static_cast<char>(0xF0 | (c >> 18));
        out += static_cast<char>(0x80 | ((c >> 12) & 0x3F));
        out += static_cast<char>(0x80 | ((c >> 6) & 0x3F));
        out += static_cast<char>(0x80 | (c & 0x3F));
    }
}

// The fields of "+CPBR: 1,"10086",129,"Name"": numbers as they are, quoted
// strings without their quotes (a quote inside one is doubled or escaped as \22).
std::vector<std::string> at_fields(std::string_view s) {
    std::vector<std::string> out;
    size_t i = 0;
    while (i <= s.size()) {
        while (i < s.size() && s[i] == ' ') ++i;
        std::string f;
        if (i < s.size() && s[i] == '"') {
            for (++i; i < s.size(); ++i) {
                if (s[i] == '"' && i + 1 < s.size() && s[i + 1] == '"') {
                    f += '"';
                    ++i;
                } else if (s[i] == '"') {
                    ++i;
                    break;
                } else if (s[i] == '\\' && i + 2 < s.size() && s.substr(i + 1, 2) == "22") {
                    f += '"';
                    i += 2;
                } else {
                    f += s[i];
                }
            }
            while (i < s.size() && s[i] != ',') ++i;
        } else {
            size_t end = s.find(',', i);
            if (end == std::string_view::npos) end = s.size();
            f = std::string(s.substr(i, end - i));
            while (!f.empty() && (f.back() == ' ' || f.back() == '\r')) f.pop_back();
            i = end;
        }
        out.push_back(std::move(f));
        if (i >= s.size()) break;
        ++i;
    }
    return out;
}

}  // namespace

std::optional<std::string> ucs2_decode(std::string_view hex) {
    if (hex.empty() || hex.size() % 4) return std::nullopt;
    auto unit = [&](size_t at) -> std::optional<char32_t> {
        char32_t v = 0;
        for (size_t k = at; k < at + 4; ++k) {
            char c = hex[k];
            int d = c >= '0' && c <= '9' ? c - '0' : c >= 'a' && c <= 'f' ? c - 'a' + 10 : c >= 'A' && c <= 'F' ? c - 'A' + 10 : -1;
            if (d < 0) return std::nullopt;
            v = v << 4 | static_cast<char32_t>(d);
        }
        return v;
    };
    std::string out;
    for (size_t i = 0; i < hex.size(); i += 4) {
        auto u = unit(i);
        if (!u) return std::nullopt;
        char32_t c = *u;
        if (c >= 0xD800 && c <= 0xDBFF) {
            if (i + 8 > hex.size()) return std::nullopt;
            auto lo = unit(i + 4);
            if (!lo || *lo < 0xDC00 || *lo > 0xDFFF) return std::nullopt;
            c = 0x10000 + ((c - 0xD800) << 10) + (*lo - 0xDC00);
            i += 4;
        } else if (c >= 0xDC00 && c <= 0xDFFF) {
            return std::nullopt;
        }
        if (c == 0) return std::nullopt;
        append_utf8(out, c);
    }
    return out;
}

std::optional<std::pair<int, int>> cpbr_range(std::string_view reply) {
    size_t at = reply.find("+CPBR:");
    if (at == std::string_view::npos) return std::nullopt;
    size_t open = reply.find('(', at), dash = reply.find('-', at), close = reply.find(')', at);
    if (open == std::string_view::npos || dash == std::string_view::npos || close == std::string_view::npos || !(open < dash && dash < close))
        return std::nullopt;
    try {
        int lo = std::stoi(std::string(reply.substr(open + 1, dash - open - 1)));
        int hi = std::stoi(std::string(reply.substr(dash + 1, close - dash - 1)));
        if (lo < 1 || hi < lo || hi > 2000) return std::nullopt;
        return std::pair{lo, hi};
    } catch (...) {
        return std::nullopt;
    }
}

std::vector<SimContact> parse_cpbr(std::string_view reply, bool ucs2) {
    std::vector<SimContact> out;
    size_t pos = 0;
    while (pos < reply.size()) {
        size_t end = reply.find('\n', pos);
        if (end == std::string_view::npos) end = reply.size();
        std::string_view line = reply.substr(pos, end - pos);
        pos = end + 1;
        size_t at = line.find("+CPBR:");
        if (at == std::string_view::npos) continue;
        auto f = at_fields(line.substr(at + 6));
        if (f.size() < 4) continue;
        SimContact c;
        try {
            c.index = std::stoi(f[0]);
        } catch (...) {
            continue;
        }
        std::string number = f[1], name = f[3];
        // Some modems send the number in UCS2 too, which is all hex digits:
        // a plain number decodes to no number.
        if (ucs2) {
            if (auto n = ucs2_decode(number); n && valid_sim_number(*n)) number = *n;
            if (auto n = ucs2_decode(name)) name = *n;
        }
        if (f[2] == "145" && !number.empty() && number[0] != '+') number = "+" + number;
        if (!valid_sim_number(number) || c.index < 1) continue;
        c.number = std::move(number);
        c.name = std::move(name);
        out.push_back(std::move(c));
    }
    return out;
}

std::string cscs_charset(std::string_view reply) {
    size_t at = reply.find("+CSCS:");
    if (at == std::string_view::npos) return {};
    auto f = at_fields(reply.substr(at + 6));
    if (f.empty()) return {};
    std::string cs = f[0];
    while (!cs.empty() && (cs.back() == '\n' || cs.back() == '\r' || cs.back() == ' ')) cs.pop_back();
    return cs;
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
