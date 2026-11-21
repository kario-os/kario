#include "apn.hpp"

#include <fcntl.h>
#include <unistd.h>

#include <algorithm>
#include <cctype>
#include <filesystem>
#include <format>
#include <fstream>
#include <set>
#include <sstream>

namespace tel {

namespace fs = std::filesystem;

namespace {

const std::set<std::string_view> kTypes{"default", "mms", "ims", "ia", "supl", "xcap", "emergency"};
const std::set<std::string_view> kIpTypes{"ipv4", "ipv6", "ipv4v6"};
const std::set<std::string_view> kAuth{"none", "pap", "chap", "pap-chap"};

bool printable(std::string_view s) {
    return std::ranges::none_of(s, [](unsigned char c) { return c < 0x20 || c == 0x7f; });
}

// An APN network identifier (3GPP TS 23.003 §9.1): labels of letters,
// digits and hyphens, dot-separated, 100 octets at most.
bool valid_apn(std::string_view a) {
    if (a.empty() || a.size() > 100) return false;
    size_t label = 0;
    for (char c : a) {
        if (c == '.') {
            if (label == 0) return false;
            label = 0;
        } else if (std::isalnum(static_cast<unsigned char>(c)) || c == '-') {
            if (++label > 63) return false;
        } else {
            return false;
        }
    }
    return label > 0;
}

bool valid_id(std::string_view id) {
    return !id.empty() && id.size() <= 32 &&
           std::ranges::all_of(id, [](char c) { return (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-'; });
}

bool digits(std::string_view s, size_t lo, size_t hi) {
    return s.size() >= lo && s.size() <= hi && std::ranges::all_of(s, [](char c) { return c >= '0' && c <= '9'; });
}

std::string lower(std::string_view s) {
    std::string out(s);
    std::ranges::transform(out, out.begin(), [](unsigned char c) { return std::tolower(c); });
    return out;
}

std::expected<std::string, std::string> read_file(const std::string& path) {
    std::ifstream in(path);
    if (!in) return std::unexpected(std::format("{}: cannot read it", path));
    std::stringstream ss;
    ss << in.rdbuf();
    return ss.str();
}

}  // namespace

bool Apn::has(std::string_view type) const {
    return std::ranges::find(types, type) != types.end();
}

json::Value Apn::to_json(bool password) const {
    json::Array t;
    for (const auto& x : types) t.push_back(x);
    json::Object o{{"id", id}, {"name", name}, {"apn", apn}, {"types", std::move(t)}, {"ip_type", ip_type}, {"auth", auth}, {"user", user}};
    if (password) o["password"] = this->password;
    else o["password_set"] = !this->password.empty();
    if (!mmsc.empty()) o["mmsc"] = mmsc;
    if (!mms_proxy.empty()) o["mms_proxy"] = mms_proxy;
    if (mms_port) o["mms_port"] = mms_port;
    if (edited) o["edited"] = true;
    return o;
}

std::expected<Apn, std::string> Apn::from_json(const json::Value& v) {
    if (!v.is_object()) return std::unexpected("an APN is an object");
    Apn a;
    const std::map<std::string_view, std::pair<std::string*, size_t>> strings{
        {"id", {&a.id, 32}},        {"name", {&a.name, 64}},       {"apn", {&a.apn, 100}},          {"ip_type", {&a.ip_type, 8}},
        {"auth", {&a.auth, 8}},     {"user", {&a.user, 64}},       {"password", {&a.password, 64}}, {"mmsc", {&a.mmsc, 256}},
        {"mms_proxy", {&a.mms_proxy, 255}},
    };
    for (const auto& [key, val] : std::get<json::Object>(v)) {
        if (auto s = strings.find(key); s != strings.end()) {
            if (!val.is_string()) return std::unexpected(std::format("{}: not a string", key));
            const auto& str = std::get<std::string>(val);
            if (str.size() > s->second.second || !printable(str)) return std::unexpected(std::format("{}: at most {} bytes, no control characters", key, s->second.second));
            *s->second.first = str;
        } else if (key == "types") {
            if (!val.is_array()) return std::unexpected("types: not an array");
            for (const auto& t : std::get<json::Array>(val)) {
                if (!t.is_string() || !kTypes.contains(std::get<std::string>(t)) || a.has(std::get<std::string>(t)))
                    return std::unexpected("types: default, mms, ims, ia, supl, xcap or emergency, each once");
                a.types.push_back(std::get<std::string>(t));
            }
        } else if (key == "mms_port") {
            if (!std::holds_alternative<double>(val)) return std::unexpected("mms_port: not a number");
            double p = std::get<double>(val);
            if (p < 0 || p > 65535 || p != static_cast<int>(p)) return std::unexpected("mms_port: 0 to 65535");
            a.mms_port = static_cast<int>(p);
        } else if (key != "edited" && key != "password_set") {
            return std::unexpected(std::format("unknown member '{}'", key));
        }
    }
    if (!a.id.empty() && !valid_id(a.id)) return std::unexpected("id: lower-case letters, digits and hyphens");
    if (a.name.empty()) return std::unexpected("name: empty");
    if (!valid_apn(a.apn)) return std::unexpected("apn: letters, digits, hyphens and dots, 100 at most");
    if (a.types.empty()) return std::unexpected("types: at least one");
    if (!kIpTypes.contains(a.ip_type)) return std::unexpected("ip_type: ipv4, ipv6 or ipv4v6");
    if (!kAuth.contains(a.auth)) return std::unexpected("auth: none, pap, chap or pap-chap");
    if (a.auth != "none" && a.user.empty()) return std::unexpected("auth: needs a user");
    return a;
}

std::expected<ApnDb, std::string> ApnDb::parse(std::string_view text) {
    auto v = json::parse(text);
    if (!v) return std::unexpected(v.error());
    const json::Value* list = v->find("carriers");
    if (!v->is_object() || std::get<json::Object>(*v).size() != 1 || !list || !list->is_array())
        return std::unexpected("an object with the carriers array alone");
    ApnDb db;
    for (const auto& e : std::get<json::Array>(*list)) {
        if (!e.is_object()) return std::unexpected("a carrier is an object");
        Carrier c;
        for (const auto& [key, val] : std::get<json::Object>(e)) {
            if (key == "name" || key == "mcc" || key == "spn" || key == "gid1") {
                if (!val.is_string()) return std::unexpected(std::format("carrier {}: not a string", key));
                std::string s = std::get<std::string>(val);
                (key == "name" ? c.name : key == "mcc" ? c.mcc : key == "spn" ? c.spn : c.gid1) = s;
            } else if (key == "mnc") {
                if (!val.is_array()) return std::unexpected("carrier mnc: not an array");
                for (const auto& m : std::get<json::Array>(val)) {
                    if (!m.is_string() || !digits(std::get<std::string>(m), 2, 3)) return std::unexpected("carrier mnc: two or three digits");
                    c.mnc.push_back(std::get<std::string>(m));
                }
            } else if (key == "apns") {
                if (!val.is_array()) return std::unexpected("carrier apns: not an array");
                for (const auto& a : std::get<json::Array>(val)) {
                    auto apn = Apn::from_json(a);
                    if (!apn) return std::unexpected(std::format("{} {}: {}", c.name, a.str("id"), apn.error()));
                    if (apn->id.empty() || std::ranges::any_of(c.apns, [&](const Apn& x) { return x.id == apn->id; }))
                        return std::unexpected(std::format("{}: every APN needs an id of its own", c.name));
                    c.apns.push_back(std::move(*apn));
                }
            } else {
                return std::unexpected(std::format("carrier: unknown member '{}'", key));
            }
        }
        if (c.name.empty() || !digits(c.mcc, 3, 3) || c.mnc.empty() || c.apns.empty())
            return std::unexpected(std::format("carrier '{}': a name, an MCC, MNCs and APNs", c.name));
        if (!c.gid1.empty() && !std::ranges::all_of(c.gid1, [](unsigned char x) { return std::isxdigit(x); }))
            return std::unexpected(std::format("carrier '{}': gid1 in hex", c.name));
        c.gid1 = lower(c.gid1);
        db.carriers_.push_back(std::move(c));
    }
    return db;
}

std::expected<ApnDb, std::string> ApnDb::load(const std::string& path) {
    auto text = read_file(path);
    if (!text) return std::unexpected(text.error());
    auto db = parse(*text);
    if (!db) return std::unexpected(std::format("{}: {}", path, db.error()));
    return db;
}

const ApnDb::Carrier* ApnDb::find(const SimId& sim) const {
    if (sim.carrier().empty()) return nullptr;
    const Carrier* own = nullptr;
    std::string gid1 = lower(sim.gid1);
    for (const auto& c : carriers_) {
        if (c.mcc != sim.mcc || std::ranges::find(c.mnc, sim.mnc) == c.mnc.end()) continue;
        bool mvno = !c.spn.empty() || !c.gid1.empty();
        if (!mvno) {
            if (!own) own = &c;
            continue;
        }
        bool spn = !c.spn.empty() && !sim.spn.empty() && lower(c.spn) == lower(sim.spn);
        bool gid = !c.gid1.empty() && gid1.starts_with(c.gid1);
        if (spn || gid) return &c;
    }
    return own;
}

const Apn* ApnList::internet() const {
    for (const auto& a : apns)
        if (a.id == selected) return &a;
    return nullptr;
}

json::Value ApnList::to_json() const {
    json::Array a;
    for (const auto& x : apns) a.push_back(x.to_json());
    return json::Object{{"carrier", carrier}, {"apns", std::move(a)}, {"selected", selected}, {"edited", edited}};
}

ApnStore::ApnStore(std::string path) : path_(std::move(path)) { load(); }

ApnList ApnStore::list(const ApnDb& db, const SimId& sim) const {
    ApnList out;
    const auto* c = db.find(sim);
    if (c) out.carrier = c->name;
    auto it = carriers_.find(sim.carrier());
    const Record* r = sim.carrier().empty() || it == carriers_.end() ? nullptr : &it->second;
    if (r && r->apns) {
        out.apns = *r->apns;
        out.edited = true;
    } else if (c) {
        out.apns = c->apns;
    }
    auto usable = [&](std::string_view id) {
        return std::ranges::any_of(out.apns, [&](const Apn& a) { return a.id == id && a.has("default"); });
    };
    if (r && usable(r->selected)) {
        out.selected = r->selected;
    } else {
        for (const auto& a : out.apns)
            if (a.has("default")) {
                out.selected = a.id;
                break;
            }
    }
    return out;
}

ApnStore::Record* ApnStore::own(const ApnDb& db, const SimId& sim) {
    if (sim.carrier().empty()) return nullptr;
    Record& r = carriers_[sim.carrier()];
    if (!r.apns) r.apns = list(db, sim).apns;
    return &r;
}

std::expected<std::string, std::string> ApnStore::save(const ApnDb& db, const SimId& sim, Apn apn, bool password) {
    if (sim.carrier().empty()) return std::unexpected("the SIM's network is not known yet");
    Record* r = own(db, sim);
    apn.edited = true;
    std::string id = apn.id;
    if (id.empty()) {
        do {
            id = std::format("u{}", r->next++);
        } while (std::ranges::any_of(*r->apns, [&](const Apn& a) { return a.id == id; }));
        apn.id = id;
        r->apns->push_back(std::move(apn));
    } else {
        auto it = std::ranges::find_if(*r->apns, [&](const Apn& a) { return a.id == id; });
        if (it == r->apns->end()) return std::unexpected("no APN with that id");
        if (!password) apn.password = it->password;
        *it = std::move(apn);
    }
    persist();
    return id;
}

ApnStore::Done ApnStore::remove(const ApnDb& db, const SimId& sim, std::string_view id) {
    if (sim.carrier().empty()) return std::unexpected("the SIM's network is not known yet");
    auto before = list(db, sim);
    if (std::ranges::none_of(before.apns, [&](const Apn& a) { return a.id == id; })) return std::unexpected("no APN with that id");
    Record* r = own(db, sim);
    std::erase_if(*r->apns, [&](const Apn& a) { return a.id == id; });
    persist();
    return {};
}

ApnStore::Done ApnStore::select(const ApnDb& db, const SimId& sim, std::string_view id) {
    if (sim.carrier().empty()) return std::unexpected("the SIM's network is not known yet");
    auto l = list(db, sim);
    auto it = std::ranges::find_if(l.apns, [&](const Apn& a) { return a.id == id; });
    if (it == l.apns.end()) return std::unexpected("no APN with that id");
    if (!it->has("default")) return std::unexpected("not an internet APN");
    carriers_[sim.carrier()].selected = id;
    persist();
    return {};
}

ApnStore::Done ApnStore::reset(const SimId& sim) {
    if (sim.carrier().empty()) return std::unexpected("the SIM's network is not known yet");
    carriers_.erase(sim.carrier());
    persist();
    return {};
}

void ApnStore::load() {
    if (path_.empty()) return;
    auto text = read_file(path_);
    if (!text) return;
    auto v = json::parse(*text);
    const json::Value* cs = v ? v->find("carriers") : nullptr;
    if (!cs || !cs->is_object()) return;
    for (const auto& [key, e] : std::get<json::Object>(*cs)) {
        if (!digits(key, 5, 6) || !e.is_object()) continue;
        Record r{.selected = e.str("selected"), .next = std::max(1, static_cast<int>(e.num("next", 1)))};
        if (const json::Value* apns = e.find("apns"); apns && apns->is_array()) {
            r.apns.emplace();
            for (const auto& a : std::get<json::Array>(*apns))
                if (auto apn = Apn::from_json(a); apn && !apn->id.empty()) {
                    apn->edited = a.flag("edited");
                    r.apns->push_back(std::move(*apn));
                }
        }
        carriers_[key] = std::move(r);
    }
}

// Written whole, 0600 (passwords), and synced before it replaces the old file.
void ApnStore::persist() const {
    if (path_.empty()) return;
    json::Object cs;
    for (const auto& [key, r] : carriers_) {
        json::Object o{{"selected", r.selected}, {"next", r.next}};
        if (r.apns) {
            json::Array a;
            for (const auto& x : *r.apns) a.push_back(x.to_json(true));
            o["apns"] = std::move(a);
        }
        cs[key] = std::move(o);
    }
    std::error_code ec;
    fs::create_directories(fs::path(path_).parent_path(), ec);
    std::string tmp = path_ + ".tmp", out = json::dump(json::Object{{"carriers", std::move(cs)}});
    int fd = ::open(tmp.c_str(), O_WRONLY | O_CREAT | O_TRUNC | O_CLOEXEC, 0600);
    if (fd < 0) return;
    bool ok = ::write(fd, out.data(), out.size()) == static_cast<ssize_t>(out.size()) && ::fsync(fd) == 0;
    ::close(fd);
    if (ok) fs::rename(tmp, path_, ec);
}

}  // namespace tel
