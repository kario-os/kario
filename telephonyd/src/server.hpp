// Unix-socket server speaking newline-delimited JSON RPC (the envelope of
// fw/wire: {"id","method","params"} -> {"id","ok","err","result"}). A client
// that calls "subscribe" also receives every event as its own line. A reply
// may come after later requests' replies; clients match them by id.
#pragma once

#include <cstdint>
#include <expected>
#include <functional>
#include <map>
#include <string>

#include "json.hpp"
#include "loop.hpp"

namespace tel {

class Server {
public:
    using Reply = std::function<void(std::expected<json::Value, std::string>)>;
    // Answers through `reply`, during the call or later.
    using Handler = std::function<void(const std::string& method, const json::Value& params, Reply reply)>;

    Server(Loop& loop, Handler handler) : loop_(loop), handler_(std::move(handler)) {}
    ~Server();

    std::expected<void, std::string> listen(const std::string& path);
    void broadcast(const json::Value& event);

private:
    struct Client {
        std::uint64_t serial = 0;  // tells a late reply's client from a later one on the same fd
        std::string in;
        bool subscribed = false;
    };
    void accept_one();
    void read_from(int fd);
    void drop(int fd);
    bool write_line(int fd, const json::Value& v);
    void respond(int fd, std::uint64_t serial, const json::Value& v);
    bool connected(int fd, std::uint64_t serial) const;

    Loop& loop_;
    Handler handler_;
    int listen_fd_ = -1;
    std::string path_;
    std::map<int, Client> clients_;
    std::uint64_t next_serial_ = 0;
};

}  // namespace tel
