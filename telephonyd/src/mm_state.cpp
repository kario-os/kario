#include "mm_state.hpp"

#include <cctype>
#include <charconv>
#include <cstdio>
#include <ctime>

namespace tel::mm {

std::string rat(std::uint64_t a) {
    if (a & (1u << 15)) return "5G";
    if (a & ((1u << 14) | (1u << 16) | (1u << 17))) return "4G";
    if (a & ((0x1fu << 5) | (0x7u << 11))) return "3G";
    if (a & ((0xfu << 1) | (1u << 10))) return "2G";
    return "";
}

int bars(std::int64_t q) {
    if (q <= 0) return 0;
    if (q < 25) return 1;
    if (q < 50) return 2;
    if (q < 75) return 3;
    return 4;
}

bool registered(std::int64_t reg) {
    switch (reg) {
    case 1: case 5: case 6: case 7: case 9: case 10: return true;  // home, roaming, SMS-only, CSFB not preferred
    default: return false;
    }
}

bool roaming(std::int64_t reg) {
    return reg == 5 || reg == 7 || reg == 10;
}

std::uint32_t ip_family(std::string_view ip_type) {
    if (ip_type == "ipv4") return 1u << 0;
    if (ip_type == "ipv6") return 1u << 1;
    return 1u << 2;
}

std::uint32_t allowed_auth(std::string_view auth) {
    if (auth == "pap") return 1u << 1;
    if (auth == "chap") return 1u << 2;
    if (auth == "pap-chap") return (1u << 1) | (1u << 2);
    return 1u << 0;
}

namespace {

std::string lower(std::string_view s) {
    std::string out(s);
    for (char& c : out) c = static_cast<char>(std::tolower(static_cast<unsigned char>(c)));
    return out;
}

// ModemManager leaves out what was not given: allowed-auth 0 (unknown),
// no user without authentication.
bool same_auth(const Dict& props, const Apn& a) {
    long long auth = dict_num(props, "allowed-auth");
    if (a.auth == "none") return (auth == 0 || auth == allowed_auth("none")) && dict_text(props, "user").empty();
    return (auth == 0 || auth == allowed_auth(a.auth)) && dict_text(props, "user") == a.user;
}

}  // namespace

// Simple.Connect gives no ip-type for the network's default APN.
bool bearer_matches(const Dict& props, const Apn& a) {
    if (dict_text(props, "apn") != a.apn) return false;
    long long family = dict_num(props, "ip-type");
    if (family && !a.apn.empty() && family != ip_family(a.ip_type)) return false;
    return same_auth(props, a);
}

bool apn_named(std::string_view asked, std::string_view existing) {
    std::string a = lower(asked), e = lower(existing);
    return e == a || (!a.empty() && e.starts_with(a + ".mnc"));
}

BearerRole bearer_role(const Dict& props, std::int64_t bearer_type, const ApnPlan& plan) {
    if (bearer_type == BearerDefaultAttach) return BearerRole::Attach;
    auto type = static_cast<std::uint32_t>(dict_num(props, "apn-type"));
    if (type & (ApnTypeIms | ApnTypeEmergency)) return BearerRole::Ims;
    if (type & ApnTypeDefault) return BearerRole::Data;
    if (type & ApnTypeMms) return BearerRole::Mms;
    if (type) return BearerRole::Other;
    std::string apn = dict_text(props, "apn");
    if (!apn.empty() && apn_named(plan.internet.apn, apn)) return BearerRole::Data;
    for (std::string_view known : {"ims", "sos"})
        if (apn_named(known, apn)) return BearerRole::Ims;
    for (const auto& ims : plan.ims)
        if (apn_named(ims, apn)) return BearerRole::Ims;
    if (plan.mms && apn_named(plan.mms->apn, apn)) return BearerRole::Mms;
    // A program that picked one of the modem's profiles (81voltd passes the
    // one the modem asked with, its APN possibly empty); telephonyd never does.
    if (props.contains("profile-id")) return BearerRole::Other;
    return BearerRole::Data;
}

bool eps_matches(const Dict& settings, const Apn& a) {
    long long family = dict_num(settings, "ip-type");
    return apn_named(a.apn, dict_text(settings, "apn")) && (!family || family == ip_family(a.ip_type)) && same_auth(settings, a);
}

long long dict_num(const Dict& d, const std::string& key, long long def) {
    auto it = d.find(key);
    const long long* v = it == d.end() ? nullptr : std::get_if<long long>(&it->second);
    return v ? *v : def;
}

std::string dict_text(const Dict& d, const std::string& key) {
    auto it = d.find(key);
    const std::string* v = it == d.end() ? nullptr : std::get_if<std::string>(&it->second);
    return v ? *v : "";
}

std::optional<IpConfig> ip_config(const Dict& d) {
    IpConfig c;
    switch (dict_num(d, "method")) {
    case 1: c.method = "ppp"; break;
    case 2: c.method = "static"; break;
    case 3: c.method = "dhcp"; break;
    default: return std::nullopt;
    }
    c.address = dict_text(d, "address");
    c.prefix = static_cast<int>(dict_num(d, "prefix"));
    c.gateway = dict_text(d, "gateway");
    for (const char* k : {"dns1", "dns2", "dns3"})
        if (auto s = dict_text(d, k); !s.empty()) c.dns.push_back(s);
    return c;
}

int ip_mtu(const Dict& d) {
    return static_cast<int>(dict_num(d, "mtu"));
}

std::string sim_state(std::int64_t state, std::int64_t lock, std::int64_t failed_reason) {
    if (state == ModemFailed) {
        if (failed_reason == FailedSimMissing) return "absent";
        if (failed_reason == FailedSimError) return "error";
        return "ready";
    }
    if (state != ModemLocked) return "ready";
    if (lock == LockSimPin) return "pin";
    if (lock == LockSimPuk) return "puk";
    return "locked";
}

std::string call_state(std::int64_t s) {
    switch (s) {
    case CallDialing: return "dialing";
    case CallRingingOut: return "alerting";
    case CallRingingIn: case CallWaiting: return "incoming";
    case CallActive: return "active";
    case CallHeld: return "held";
    case CallTerminated: return "ended";
    default: return "";
    }
}

std::string end_reason(std::string_view dir, bool connected, bool hung_up, std::int64_t why, bool answering) {
    bool failed = why == ReasonError || why == ReasonAudioSetupFailed;
    if (hung_up) return dir == "in" && !connected ? "rejected" : "hangup";
    if (dir == "out" && !connected && why == ReasonRefusedOrBusy) return "busy";
    if (dir == "in" && !connected && !(answering && failed)) return "missed";
    if (failed) return "failed";
    return "remote";
}

std::string end_error(std::int64_t why) {
    switch (why) {
    case ReasonError: return "the network or the modem ended the call with an error";
    case ReasonAudioSetupFailed: return "the modem could not set up the call's audio";
    default: return "";
    }
}

namespace {

bool digits(std::string_view s, size_t at, size_t n, int& out) {
    if (at + n > s.size()) return false;
    auto [p, ec] = std::from_chars(s.data() + at, s.data() + at + n, out);
    return ec == std::errc() && p == s.data() + at + n;
}

}  // namespace

long long sms_time(std::string_view s, long long fallback) {
    std::tm tm{};
    if (s.size() < 19 || s[4] != '-' || s[7] != '-' || s[10] != 'T' || s[13] != ':' || s[16] != ':' ||
        !digits(s, 0, 4, tm.tm_year) || !digits(s, 5, 2, tm.tm_mon) || !digits(s, 8, 2, tm.tm_mday) ||
        !digits(s, 11, 2, tm.tm_hour) || !digits(s, 14, 2, tm.tm_min) || !digits(s, 17, 2, tm.tm_sec))
        return fallback;
    tm.tm_year -= 1900;
    tm.tm_mon -= 1;
    long long t = timegm(&tm);
    size_t i = 19;
    if (i < s.size() && s[i] == '.')
        for (++i; i < s.size() && s[i] >= '0' && s[i] <= '9'; ++i) {}
    if (i == s.size() || s[i] == 'Z') return t;
    if (s[i] != '+' && s[i] != '-') return fallback;
    int sign = s[i] == '-' ? -1 : 1, hh = 0, mm = 0;
    if (!digits(s, i + 1, 2, hh)) return fallback;
    size_t j = i + 3;
    if (j < s.size() && s[j] == ':') ++j;
    if (j < s.size() && !digits(s, j, 2, mm)) return fallback;
    return t - sign * (hh * 3600LL + mm * 60LL);
}

// MMSmsDeliveryState: 0x00-0x1f completed, 0x20-0x3f the network still
// tries, 0x40-0x7f it gave up; 0x2xx and 0x3xx the same for CDMA.
std::string delivery(std::int64_t s) {
    if (s >= 0 && s < 0x20) return "delivered";
    if ((s >= 0x40 && s < 0x80) || (s >= 0x200 && s < 0x300)) return "failed";
    return "";
}

// FNV-1a: the same in every build, unlike std::hash.
std::string stable_id(char prefix, std::string_view what) {
    std::uint64_t h = 0xcbf29ce484222325ULL;
    for (unsigned char c : what) h = (h ^ c) * 0x100000001b3ULL;
    char out[17];
    std::snprintf(out, sizeof out, "%016llx", static_cast<unsigned long long>(h));
    return prefix + std::string(out);
}

}  // namespace tel::mm
