// State files written whole, as the Go package statefile writes the Shell's
// (11-platform-architecture.md §17): a temporary file beside the real one,
// synced, renamed over it, then the directory synced, so a power cut, a
// kill or a full disk in mid-write leaves the old file or the new one. Each
// is written twice, the second time to path.bak, read when path is damaged.
#pragma once

#include <string>
#include <string_view>

#include "json.hpp"

namespace tel::statefile {

inline constexpr std::string_view kBak = ".bak";

// Replaces path, then path.bak, with data, both 0600 (APN passwords);
// false when either could not be written.
bool write(const std::string& path, std::string_view data);

enum class Found { kWhole, kCopy, kNone, kDamaged };

struct Read {
    Found found = Found::kNone;
    json::Value value;  // kWhole, kCopy: a JSON object
    std::string why;    // kCopy, kDamaged: what is wrong with path, and its copy
};

// path's JSON object. When path does not read whole as one, or is missing
// beside its copy, the copy's, and path is written again from it (kCopy).
// Neither file there: kNone. Neither whole: kDamaged, both left as they are.
Read read(const std::string& path);

// Says on stderr what read found when it was not path whole; damaged: what
// the service does instead.
void say(const std::string& path, const Read& r, std::string_view damaged);

}  // namespace tel::statefile
