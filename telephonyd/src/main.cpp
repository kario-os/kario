// phone-telephonyd: calls, SMS and network status for the shell.
//
//	phone-telephonyd --run-dir DIR [--state-dir DIR] [--modem sim|modemmanager] [--sku FILE] [--network FILE] [--apns FILE]
//	                 [--ims-status PROGRAM]
//
// Listens on DIR/telephonyd.sock. Without --modem the backend is the "modem"
// of the device description (--sku, the phone-sku package's sku.json), else
// the simulated baseband. The simulated network's texts come from --network,
// by default share/phone/sim-network.json under the prefix the program is
// installed in; without that file it speaks English. The carriers' APNs
// come from --apns, by default share/phone/apns.json there; without it
// the data link uses the network's default APN. The modem's IMS
// registration (ims.hpp) comes from --ims-status, by default
// libexec/phone-telephonyd/ims-status there, which the board's SoC glue
// installs; without it a call is not put down to IMS.

#include <unistd.h>

#include <csignal>
#include <filesystem>
#include <fstream>
#include <print>
#include <sstream>
#include <string>
#include <string_view>

#include "backend.hpp"
#include "json.hpp"
#include "loop.hpp"
#include "modem.hpp"
#include "server.hpp"
#include "service.hpp"

static std::string sku_modem(const std::string& path) {
    std::ifstream in(path);
    if (!in) return "";
    std::stringstream ss;
    ss << in.rdbuf();
    auto v = json::parse(ss.str());
    return v && v->is_object() ? v->str("modem") : "";
}

// A data file of the prefix this program runs from (PREFIX/bin/phone-telephonyd),
// "" when it is not there.
static std::string shared(const char* name) {
    std::error_code ec;
    auto exe = std::filesystem::read_symlink("/proc/self/exe", ec);
    auto found = exe.parent_path().parent_path() / "share" / "phone" / name;
    if (ec || !std::filesystem::exists(found)) {
        std::println(stderr, "phone-telephonyd: no {}", found.string());
        return "";
    }
    return found.string();
}

// The simulated network: --network, else sim-network.json in the prefix's
// data, else the built-in English texts.
static std::expected<tel::SimNetwork, std::string> sim_network(std::string path) {
    if (path.empty()) path = shared("sim-network.json");
    if (path.empty()) return tel::SimNetwork{};
    return tel::SimNetwork::load(path);
}

// The IMS registration program: --ims-status, else the prefix's, else none.
static std::string ims_status(std::string path) {
    if (!path.empty()) return path;
    std::error_code ec;
    auto exe = std::filesystem::read_symlink("/proc/self/exe", ec);
    auto found = exe.parent_path().parent_path() / "libexec" / "phone-telephonyd" / "ims-status";
    return !ec && ::access(found.c_str(), X_OK) == 0 ? found.string() : "";
}

// The carriers' APNs: --apns, else apns.json in the prefix's data, else none.
static std::expected<tel::ApnDb, std::string> apn_db(std::string path) {
    if (path.empty()) path = shared("apns.json");
    if (path.empty()) return tel::ApnDb{};
    return tel::ApnDb::load(path);
}

int main(int argc, char** argv) {
    std::string run_dir = "/run/phone", state_dir, modem, sku, network, apns, ims;
    for (int i = 1; i + 1 < argc; i += 2) {
        std::string_view k = argv[i];
        if (k == "--run-dir") run_dir = argv[i + 1];
        else if (k == "--state-dir") state_dir = argv[i + 1];
        else if (k == "--modem") modem = argv[i + 1];
        else if (k == "--sku") sku = argv[i + 1];
        else if (k == "--network") network = argv[i + 1];
        else if (k == "--apns") apns = argv[i + 1];
        else if (k == "--ims-status") ims = argv[i + 1];
    }
    if (modem.empty() && !sku.empty()) modem = sku_modem(sku);
    if (modem.empty()) modem = "sim";
    std::signal(SIGPIPE, SIG_IGN);
    auto db = apn_db(apns);
    if (!db) {
        std::println(stderr, "phone-telephonyd: {}", db.error());
        return 1;
    }
    tel::Loop loop;
    tel::Server* server = nullptr;
    tel::Service service(state_dir.empty() ? "" : (std::filesystem::path(state_dir) / "telephony").string(),
                         [&](const json::Value& ev) {
                             if (server) server->broadcast(ev);
                         },
                         std::move(*db));
    const tel::BackendKind* kind = tel::find_backend(modem);
    if (!kind) {
        std::println(stderr, "phone-telephonyd: modem backend '{}' is not built in", modem);
        return 2;
    }
    tel::BackendDeps deps{.loop = loop, .events = service.events(), .sim_network = [&] { return sim_network(network); },
                          .ims_status = ims_status(ims)};
    auto backend = kind->make(deps);
    if (!backend) {
        std::println(stderr, "phone-telephonyd: {}", backend.error());
        return 1;
    }
    service.attach(backend->modem.get(), backend->sim);
    tel::Server srv(loop, [&](const std::string& m, const json::Value& p, tel::Server::Reply r) { service.handle(m, p, std::move(r)); });
    server = &srv;
    if (auto r = srv.listen((std::filesystem::path(run_dir) / "telephonyd.sock").string()); !r) {
        std::println(stderr, "phone-telephonyd: {}", r.error());
        return 1;
    }
    std::println("phone-telephonyd: modem {} on {}/telephonyd.sock", modem, run_dir);
    if (modem == "modemmanager" && !deps.ims_status.empty()) std::println("phone-telephonyd: IMS registration from {}", deps.ims_status);
    std::fflush(stdout);
    loop.run();
    return backend->failed && backend->failed() ? 1 : 0;
}
