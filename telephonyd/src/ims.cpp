#include "ims.hpp"

#include <array>

namespace tel {

namespace {

constexpr std::array kStates{"registered", "registering", "not-registered", "limited-registered"};

std::string_view trim(std::string_view s) {
    while (!s.empty() && (s.front() == ' ' || s.front() == '\t' || s.front() == '\n' || s.front() == '\r')) s.remove_prefix(1);
    while (!s.empty() && (s.back() == ' ' || s.back() == '\t' || s.back() == '\n' || s.back() == '\r')) s.remove_suffix(1);
    return s;
}

}  // namespace

ImsReading ims_reading(bool ok, std::string_view output) {
    std::string_view first = trim(output.substr(0, output.find('\n')));
    if (ok)
        for (std::string_view s : kStates)
            if (trim(output) == s) return {.state = std::string(s)};
    if (first.empty()) first = ok ? "no answer" : "it failed";
    return {.error = std::string(first)};
}

bool valid_ims(std::string_view state) {
    for (std::string_view s : kStates)
        if (state == s) return true;
    return state == "unknown";
}

std::string ims_blame(const Call& c, bool alerted, std::string_view rat, std::string_view ims) {
    if (c.dir != "out" || c.emergency || c.connected || alerted) return "";
    if (c.reason == "hangup" || c.reason == "busy") return "";
    if (rat != "4G" && rat != "5G") return "";
    if (ims.empty() || ims == "registered") return "";
    return std::string(ims);
}

}  // namespace tel
