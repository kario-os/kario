// The basebands phone-telephonyd is built with, by the name a board's
// "modem" gives (sku.json; docs/24-sku-framework.md §8.8). A backend is an
// implementation of Modem; what is particular to one SoC (its carrier
// configurations, IMS, voice path, SIM slot) stays outside telephonyd. A new
// backend is an entry in backend.cpp and its sources in CMakeLists.txt, and
// tools/sku.py MODEMS names it for the boards.
#pragma once

#include <expected>
#include <functional>
#include <memory>
#include <span>
#include <string>
#include <string_view>

#include "loop.hpp"
#include "modem.hpp"

namespace tel {

struct Backend {
    std::unique_ptr<Modem> modem;
    SimModem* sim = nullptr;       // the simulated baseband's test hooks (sim.*), else none
    std::function<bool()> failed;  // after the loop stops: whether the backend stopped it (exit 1)
};

// What a backend may be made with: the loop, the service's events, the
// simulated network's texts, read only by the backend that needs them, and
// the program that says the modem's IMS registration (ims.hpp), "" for none.
struct BackendDeps {
    Loop& loop;
    Modem::Events events;
    std::function<std::expected<SimNetwork, std::string>()> sim_network;
    std::string ims_status;
};

struct BackendKind {
    std::string_view name;
    std::expected<Backend, std::string> (*make)(BackendDeps& deps);
};

std::span<const BackendKind> backends();
// The built-in backend of that name, nullptr for none.
const BackendKind* find_backend(std::string_view name);

}  // namespace tel
