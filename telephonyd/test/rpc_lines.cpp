// Writes its input to phone-telephonyd's socket as one client (tools/cfuzz):
// the real Server and Service over the simulated modem, whose clock moves on
// while the client waits. Every line gets one answer, an error with its
// reason if it is not a request, within the simulated time any answer
// takes. Built with KARIO_LIBFUZZER defined it is a libFuzzer target
// instead (make fuzz-libfuzzer).
#include <sys/socket.h>
#include <sys/un.h>
#include <fcntl.h>
#include <unistd.h>

#include <algorithm>
#include <cstdint>
#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <functional>
#include <iostream>
#include <iterator>
#include <string>

#include "json.hpp"
#include "loop.hpp"
#include "modem.hpp"
#include "server.hpp"
#include "service.hpp"

namespace {

// The simulated time an answer may take: a call's set-up and answer, a
// USSD session, the network's replies, with room to spare.
constexpr auto tick = std::chrono::milliseconds(100);
constexpr int max_ticks = 1200;

// The socket's directory, in TMPDIR, made once and removed at exit.
char sock_dir[4096];

const std::string& sock_path() {
    static const std::string path = [] {
        const char* tmp = std::getenv("TMPDIR");
        std::string tpl = std::string(tmp && *tmp ? tmp : "/tmp") + "/telephonyd-rpc-XXXXXX";
        if (tpl.size() >= sizeof(sock_dir) || !::mkdtemp(tpl.data())) {
            std::perror("mkdtemp");
            std::abort();
        }
        std::strcpy(sock_dir, tpl.c_str());
        std::atexit([] { ::rmdir(sock_dir); });
        return tpl + "/telephonyd.sock";
    }();
    return path;
}

int dial() {
    int fd = ::socket(AF_UNIX, SOCK_STREAM | SOCK_CLOEXEC, 0);
    sockaddr_un addr{};
    addr.sun_family = AF_UNIX;
    std::strcpy(addr.sun_path, sock_path().c_str());
    if (fd < 0 || ::connect(fd, reinterpret_cast<sockaddr*>(&addr), sizeof(addr)) < 0) {
        std::perror("connect");
        std::abort();
    }
    return fd;
}

// serve says what went wrong, "" if nothing did.
std::string serve(const std::string& input) {
    tel::Loop loop;
    tel::Server* server = nullptr;
    tel::Service svc("", [&](const json::Value& ev) {
        if (server) server->broadcast(ev);
    });
    tel::SimModem sim(loop, svc.events(), tel::SimNetwork{});
    svc.attach(&sim, &sim);
    tel::Server srv(loop, [&](const std::string& m, const json::Value& p, tel::Server::Reply r) { svc.handle(m, p, std::move(r)); });
    server = &srv;
    if (auto r = srv.listen(sock_path()); !r) {
        std::cerr << r.error() << "\n";
        std::abort();
    }
    int fd = dial(), wake[2];
    if (::pipe2(wake, O_CLOEXEC) < 0) {
        std::perror("pipe2");
        std::abort();
    }
    loop.watch(wake[0], [] {});
    const auto want = static_cast<size_t>(std::count(input.begin(), input.end(), '\n'));
    size_t off = 0, got = 0;
    int ticks = 0;
    std::string in, fault;
    std::function<void()> pump = [&] {
        while (off < input.size()) {
            ssize_t n = ::send(fd, input.data() + off, input.size() - off, MSG_DONTWAIT | MSG_NOSIGNAL);
            if (n <= 0) break;
            off += static_cast<size_t>(n);
        }
        char buf[8192];
        ssize_t n;
        while ((n = ::recv(fd, buf, sizeof(buf), MSG_DONTWAIT)) > 0) in.append(buf, static_cast<size_t>(n));
        for (size_t nl; fault.empty() && (nl = in.find('\n')) != std::string::npos;) {
            std::string line = in.substr(0, nl);
            in.erase(0, nl + 1);
            auto v = json::parse(line);
            if (!v || !v->is_object()) fault = "not an object: " + line;
            else if (v->find("event")) continue;
            else if (!v->find("ok")) fault = "neither an answer nor an event: " + line;
            else if (!v->flag("ok") && v->str("err").empty()) fault = "an error without its reason: " + line;
            else ++got;
        }
        if (fault.empty() && got > want) fault = "more answers than lines";
        if (fault.empty() && n == 0 && got < want) fault = "the connection was closed with lines unanswered";
        if (fault.empty() && ++ticks > max_ticks && got < want)
            fault = std::to_string(want - got) + " of " + std::to_string(want) + " lines unanswered after " +
                    std::to_string(max_ticks * tick.count() / 1000) + " s";
        if (!fault.empty() || (off == input.size() && got == want)) {
            // The loop polls once more before it stops.
            loop.stop();
            (void)!::write(wake[1], "", 1);
            return;
        }
        // Due after the loop has polled the server once more.
        loop.advance(tick);
        loop.after(std::chrono::milliseconds(1), pump);
    };
    loop.after(std::chrono::milliseconds(0), pump);
    loop.run();
    for (int f : {fd, wake[0], wake[1]}) ::close(f);
    return fault;
}

}  // namespace

#ifdef KARIO_LIBFUZZER
extern "C" int LLVMFuzzerTestOneInput(const uint8_t* data, size_t size) {
    if (auto fault = serve(std::string(reinterpret_cast<const char*>(data), size)); !fault.empty()) {
        std::cerr << fault << "\n";
        std::abort();
    }
    return 0;
}
#else
int main() {
    std::string input{std::istreambuf_iterator<char>(std::cin), std::istreambuf_iterator<char>()};
    if (auto fault = serve(input); !fault.empty()) {
        std::cerr << fault << "\n";
        return 3;
    }
}
#endif
