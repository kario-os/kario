#include "server.hpp"

#include <sys/socket.h>
#include <sys/stat.h>
#include <sys/un.h>
#include <unistd.h>

#include <cerrno>
#include <cstring>
#include <format>
#include <vector>

namespace tel {

constexpr size_t max_line = 1 << 20;

Server::~Server() {
    for (const auto& [fd, _] : clients_) ::close(fd);
    if (listen_fd_ >= 0) {
        ::close(listen_fd_);
        ::unlink(path_.c_str());
    }
}

std::expected<void, std::string> Server::listen(const std::string& path) {
    sockaddr_un addr{};
    if (path.size() >= sizeof(addr.sun_path)) return std::unexpected("socket path too long");
    addr.sun_family = AF_UNIX;
    std::memcpy(addr.sun_path, path.c_str(), path.size() + 1);
    ::unlink(path.c_str());
    listen_fd_ = ::socket(AF_UNIX, SOCK_STREAM | SOCK_CLOEXEC, 0);
    if (listen_fd_ < 0) return std::unexpected(std::format("socket: {}", std::strerror(errno)));
    if (::bind(listen_fd_, reinterpret_cast<sockaddr*>(&addr), sizeof(addr)) < 0)
        return std::unexpected(std::format("bind {}: {}", path, std::strerror(errno)));
    // Only the shell (same uid / root) may talk to telephonyd; apps reach it
    // through the shell's broker.
    ::chmod(path.c_str(), 0600);
    if (::listen(listen_fd_, 16) < 0) return std::unexpected(std::format("listen: {}", std::strerror(errno)));
    path_ = path;
    loop_.watch(listen_fd_, [this] { accept_one(); });
    return {};
}

void Server::accept_one() {
    int fd = ::accept4(listen_fd_, nullptr, nullptr, SOCK_CLOEXEC);
    if (fd < 0) return;
    clients_[fd] = Client{.serial = ++next_serial_};
    loop_.watch(fd, [this, fd] { read_from(fd); });
}

void Server::drop(int fd) {
    loop_.unwatch(fd);
    clients_.erase(fd);
    ::close(fd);
}

bool Server::write_line(int fd, const json::Value& v) {
    std::string line = json::dump(v) + "\n";
    size_t off = 0;
    while (off < line.size()) {
        ssize_t n = ::write(fd, line.data() + off, line.size() - off);
        if (n < 0 && errno == EINTR) continue;
        if (n <= 0) return false;
        off += static_cast<size_t>(n);
    }
    return true;
}

bool Server::connected(int fd, std::uint64_t serial) const {
    auto it = clients_.find(fd);
    return it != clients_.end() && it->second.serial == serial;
}

void Server::respond(int fd, std::uint64_t serial, const json::Value& v) {
    if (connected(fd, serial) && !write_line(fd, v)) drop(fd);
}

void Server::read_from(int fd) {
    char buf[8192];
    ssize_t n = ::read(fd, buf, sizeof(buf));
    if (n <= 0) {
        drop(fd);
        return;
    }
    Client& c = clients_[fd];
    c.in.append(buf, static_cast<size_t>(n));
    if (c.in.size() > max_line) {
        drop(fd);
        return;
    }
    std::uint64_t serial = c.serial;
    size_t nl;
    // Writing a reply or an event can drop the client.
    while (connected(fd, serial) && (nl = clients_[fd].in.find('\n')) != std::string::npos) {
        std::string line = clients_[fd].in.substr(0, nl);
        clients_[fd].in.erase(0, nl + 1);
        auto req = json::parse(line);
        if (!req || !req->is_object()) {
            respond(fd, serial, json::Object{{"ok", false}, {"err", req ? std::string("request is not an object") : req.error()}});
            continue;
        }
        json::Value id = req->num("id");
        std::string method = req->str("method");
        json::Value params = req->find("params") ? *req->find("params") : json::Value(json::Object{});
        if (method == "subscribe") {
            clients_[fd].subscribed = true;
            respond(fd, serial, json::Object{{"id", id}, {"ok", true}});
            continue;
        }
        handler_(method, params, [this, fd, serial, id](std::expected<json::Value, std::string> r) {
            json::Object resp{{"id", id}, {"ok", r.has_value()}};
            if (r) resp["result"] = std::move(*r);
            else resp["err"] = r.error();
            respond(fd, serial, resp);
        });
    }
}

void Server::broadcast(const json::Value& event) {
    std::vector<int> dead;
    for (auto& [fd, c] : clients_)
        if (c.subscribed && !write_line(fd, event)) dead.push_back(fd);
    for (int fd : dead) drop(fd);
}

}  // namespace tel
