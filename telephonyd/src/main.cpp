// phone-telephonyd: calls, SMS and network status for the shell.
//
//	phone-telephonyd --run-dir DIR [--state-dir DIR] [--modem sim|modemmanager] [--sku FILE] [--network FILE]
//
// Listens on DIR/telephonyd.sock. Without --modem the backend is the "modem"
// of the device description (--sku, the phone-sku package's sku.json), else
// the simulated baseband. The simulated network's texts come from --network,
// by default share/phone/sim-network.json under the prefix the program is
// installed in; without that file it speaks English.

#include <csignal>
#include <filesystem>
#include <fstream>
#include <memory>
#include <print>
#include <sstream>
#include <string>
#include <string_view>

#include "json.hpp"
#include "loop.hpp"
#include "modem.hpp"
#include "server.hpp"
#include "service.hpp"
#ifdef TEL_MODEMMANAGER
#include "mm.hpp"
#endif

static std::string sku_modem(const std::string& path) {
    std::ifstream in(path);
    if (!in) return "";
    std::stringstream ss;
    ss << in.rdbuf();
    auto v = json::parse(ss.str());
    return v && v->is_object() ? v->str("modem") : "";
}

// The simulated network: --network, else sim-network.json in the data of the
// prefix this program runs from (PREFIX/bin/phone-telephonyd), else the
// built-in English texts.
static std::expected<tel::SimNetwork, std::string> sim_network(const std::string& path) {
    if (!path.empty()) return tel::SimNetwork::load(path);
    std::error_code ec;
    auto exe = std::filesystem::read_symlink("/proc/self/exe", ec);
    auto found = exe.parent_path().parent_path() / "share" / "phone" / "sim-network.json";
    if (ec || !std::filesystem::exists(found)) {
        std::println(stderr, "phone-telephonyd: no {}: the simulated network speaks English", found.string());
        return tel::SimNetwork{};
    }
    return tel::SimNetwork::load(found.string());
}

int main(int argc, char** argv) {
    std::string run_dir = "/run/phone", state_dir, modem, sku, network;
    for (int i = 1; i + 1 < argc; i += 2) {
        std::string_view k = argv[i];
        if (k == "--run-dir") run_dir = argv[i + 1];
        else if (k == "--state-dir") state_dir = argv[i + 1];
        else if (k == "--modem") modem = argv[i + 1];
        else if (k == "--sku") sku = argv[i + 1];
        else if (k == "--network") network = argv[i + 1];
    }
    if (modem.empty() && !sku.empty()) modem = sku_modem(sku);
    if (modem.empty()) modem = "sim";
    std::signal(SIGPIPE, SIG_IGN);
    tel::Loop loop;
    tel::Server* server = nullptr;
    tel::Service service(state_dir.empty() ? "" : (std::filesystem::path(state_dir) / "telephony").string(),
                         [&](const json::Value& ev) {
                             if (server) server->broadcast(ev);
                         });
    std::unique_ptr<tel::Modem> backend;
    tel::SimModem* sim = nullptr;
#ifdef TEL_MODEMMANAGER
    tel::MmModem* mm = nullptr;
#endif
    if (modem == "sim") {
        auto net = sim_network(network);
        if (!net) {
            std::println(stderr, "phone-telephonyd: {}", net.error());
            return 1;
        }
        auto s = std::make_unique<tel::SimModem>(loop, service.events(), std::move(*net));
        sim = s.get();
        backend = std::move(s);
#ifdef TEL_MODEMMANAGER
    } else if (modem == "modemmanager") {
        auto m = std::make_unique<tel::MmModem>(loop, service.events());
        if (auto r = m->start(); !r) {
            std::println(stderr, "phone-telephonyd: {}", r.error());
            return 1;
        }
        mm = m.get();
        backend = std::move(m);
#endif
    } else {
        std::println(stderr, "phone-telephonyd: modem backend '{}' is not built in", modem);
        return 2;
    }
    service.attach(backend.get(), sim);
    tel::Server srv(loop, [&](const std::string& m, const json::Value& p, tel::Server::Reply r) { service.handle(m, p, std::move(r)); });
    server = &srv;
    if (auto r = srv.listen((std::filesystem::path(run_dir) / "telephonyd.sock").string()); !r) {
        std::println(stderr, "phone-telephonyd: {}", r.error());
        return 1;
    }
    std::println("phone-telephonyd: modem {} on {}/telephonyd.sock", modem, run_dir);
    std::fflush(stdout);
    loop.run();
#ifdef TEL_MODEMMANAGER
    if (mm && mm->failed()) return 1;
#endif
    return 0;
}
