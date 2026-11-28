#include "statefile.hpp"

#include <fcntl.h>
#include <sys/stat.h>
#include <unistd.h>

#include <cerrno>
#include <cstring>
#include <expected>
#include <filesystem>
#include <format>
#include <print>

namespace tel::statefile {

namespace fs = std::filesystem;

namespace {

// The bytes at path, or the errno that kept them.
std::expected<std::string, int> slurp(const std::string& path) {
    int fd = ::open(path.c_str(), O_RDONLY | O_CLOEXEC);
    if (fd < 0) return std::unexpected(errno);
    std::string out;
    char buf[8192];
    for (;;) {
        ssize_t n = ::read(fd, buf, sizeof buf);
        if (n < 0 && errno == EINTR) continue;
        if (n < 0) {
            int e = errno;
            ::close(fd);
            return std::unexpected(e);
        }
        if (n == 0) break;
        out.append(buf, static_cast<size_t>(n));
    }
    ::close(fd);
    return out;
}

std::expected<json::Value, std::string> object(const std::string& text) {
    auto v = json::parse(text);
    if (!v) return std::unexpected(v.error());
    if (!v->is_object()) return std::unexpected("not a JSON object");
    return v;
}

bool replace(const std::string& path, std::string_view data) {
    std::string tmp = path + ".tmp";
    int fd = ::open(tmp.c_str(), O_WRONLY | O_CREAT | O_TRUNC | O_CLOEXEC, 0600);
    if (fd < 0) return false;
    bool ok = ::fchmod(fd, 0600) == 0;
    for (size_t off = 0; ok && off < data.size();) {
        ssize_t n = ::write(fd, data.data() + off, data.size() - off);
        if (n < 0 && errno == EINTR) continue;
        ok = n > 0;
        if (ok) off += static_cast<size_t>(n);
    }
    ok = ok && ::fsync(fd) == 0;
    ok = ::close(fd) == 0 && ok;
    std::error_code ec;
    if (ok) fs::rename(tmp, path, ec);
    if (!ok || ec) {
        fs::remove(tmp, ec);
        return false;
    }
    return true;
}

// A file system that cannot sync a directory has the renames in it durable
// with its next commit.
void sync_dir(const fs::path& dir) {
    int fd = ::open(dir.empty() ? "." : dir.c_str(), O_RDONLY | O_DIRECTORY | O_CLOEXEC);
    if (fd < 0) return;
    (void)::fsync(fd);
    ::close(fd);
}

}  // namespace

bool write(const std::string& path, std::string_view data) {
    fs::path dir = fs::path(path).parent_path();
    std::error_code ec;
    if (!dir.empty()) fs::create_directories(dir, ec);
    bool ok = replace(path, data) && replace(path + std::string(kBak), data);
    sync_dir(dir);
    return ok;
}

Read read(const std::string& path) {
    Read r;
    auto raw = slurp(path);
    bool missing = !raw && raw.error() == ENOENT;
    std::string why = missing ? "missing" : raw ? "" : std::strerror(raw.error());
    if (raw) {
        auto v = object(*raw);
        if (v) {
            r.found = Found::kWhole;
            r.value = std::move(*v);
            return r;
        }
        why = v.error();
    }
    auto kept = slurp(path + std::string(kBak));
    if (!kept && kept.error() == ENOENT) {
        r.found = missing ? Found::kNone : Found::kDamaged;
        r.why = missing ? "" : why;
        return r;
    }
    if (kept) {
        auto v = object(*kept);
        if (v) {
            r.found = Found::kCopy;
            r.value = std::move(*v);
            r.why = why;
            if (replace(path, *kept)) sync_dir(fs::path(path).parent_path());
            return r;
        }
        r.why = std::format("{}; its copy: {}", why, v.error());
    } else {
        r.why = std::format("{}; its copy: {}", why, std::strerror(kept.error()));
    }
    r.found = Found::kDamaged;
    return r;
}

void say(const std::string& path, const Read& r, std::string_view damaged) {
    if (r.found == Found::kCopy)
        std::println(stderr, "phone-telephonyd: state: {}: {}; read from its copy", path, r.why);
    else if (r.found == Found::kDamaged)
        std::println(stderr, "phone-telephonyd: state: {}: damaged: {}; {}", path, r.why, damaged);
}

}  // namespace tel::statefile
