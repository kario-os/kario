#include "mm_state.hpp"

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

std::string end_reason(std::string_view dir, bool connected, bool hung_up, std::int64_t why) {
    if (hung_up) return dir == "in" && !connected ? "rejected" : "hangup";
    if (dir == "out" && !connected && why == ReasonRefusedOrBusy) return "busy";
    if (dir == "in" && !connected) return "missed";
    if (why == ReasonError || why == ReasonAudioSetupFailed) return "failed";
    return "remote";
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
