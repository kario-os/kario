#include "backend.hpp"

#include <algorithm>
#include <array>

#ifdef TEL_MODEMMANAGER
#include "mm.hpp"
#endif

namespace tel {

namespace {

std::expected<Backend, std::string> make_sim(BackendDeps& d) {
    auto net = d.sim_network();
    if (!net) return std::unexpected(net.error());
    auto s = std::make_unique<SimModem>(d.loop, d.events, std::move(*net));
    SimModem* sim = s.get();
    return Backend{.modem = std::move(s), .sim = sim};
}

#ifdef TEL_MODEMMANAGER
std::expected<Backend, std::string> make_modemmanager(BackendDeps& d) {
    auto m = std::make_unique<MmModem>(d.loop, d.events, d.ims_status);
    if (auto r = m->start(); !r) return std::unexpected(r.error());
    MmModem* mm = m.get();
    return Backend{.modem = std::move(m), .failed = [mm] { return mm->failed(); }};
}
#endif

constexpr std::array kBackends{
    BackendKind{"sim", make_sim},
#ifdef TEL_MODEMMANAGER
    BackendKind{"modemmanager", make_modemmanager},
#endif
};

}  // namespace

std::span<const BackendKind> backends() {
    return kBackends;
}

const BackendKind* find_backend(std::string_view name) {
    auto it = std::ranges::find(kBackends, name, &BackendKind::name);
    return it == kBackends.end() ? nullptr : &*it;
}

}  // namespace tel
