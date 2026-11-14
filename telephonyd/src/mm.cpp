#include "mm.hpp"

#include <algorithm>
#include <cerrno>
#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <format>
#include <print>
#include <utility>

#include "json.hpp"
#include "mm_state.hpp"

namespace tel {

using namespace std::chrono_literals;

namespace {

constexpr const char* kService = "org.freedesktop.ModemManager1";
constexpr const char* kRoot = "/org/freedesktop/ModemManager1";
constexpr const char* kModem = "org.freedesktop.ModemManager1.Modem";
constexpr const char* k3gpp = "org.freedesktop.ModemManager1.Modem.Modem3gpp";
constexpr const char* kUssd = "org.freedesktop.ModemManager1.Modem.Modem3gpp.Ussd";
constexpr const char* kVoice = "org.freedesktop.ModemManager1.Modem.Voice";
constexpr const char* kMessaging = "org.freedesktop.ModemManager1.Modem.Messaging";
constexpr const char* kCellBroadcast = "org.freedesktop.ModemManager1.Modem.CellBroadcast";
constexpr const char* kCall = "org.freedesktop.ModemManager1.Call";
constexpr const char* kSms = "org.freedesktop.ModemManager1.Sms";
constexpr const char* kCbm = "org.freedesktop.ModemManager1.Cbm";
constexpr const char* kSim = "org.freedesktop.ModemManager1.Sim";
constexpr const char* kTime = "org.freedesktop.ModemManager1.Modem.Time";
constexpr const char* kSimple = "org.freedesktop.ModemManager1.Modem.Simple";
constexpr const char* kBearer = "org.freedesktop.ModemManager1.Bearer";
constexpr const char* kProps = "org.freedesktop.DBus.Properties";
constexpr const char* kObjects = "org.freedesktop.DBus.ObjectManager";

// Network operations ModemManager only answers once the network has (sending
// an SMS waits for the SMSC's acknowledgement).
constexpr std::uint64_t kNetworkTimeout = 120'000'000;

void warn(std::string_view what) {
    std::println(stderr, "phone-telephonyd: {}", what);
}

std::string err_text(const sd_bus_error* e) {
    if (!e) return "malformed reply";
    if (e->message && *e->message) return e->message;
    return e->name ? e->name : "error";
}

std::vector<std::string> take_strv(char** v) {
    std::vector<std::string> out;
    for (char** p = v; p && *p; ++p) {
        out.emplace_back(*p);
        std::free(*p);
    }
    std::free(v);
    return out;
}

int read_value(sd_bus_message* m, const char* sig, MmModem::Prop& out) {
    std::string_view s = sig;
    int r;
    if (s == "s" || s == "o") {
        const char* v = nullptr;
        if ((r = sd_bus_message_read_basic(m, s[0], &v)) > 0) out = std::string(v);
        return r;
    }
    if (s == "b") {
        int v = 0;
        if ((r = sd_bus_message_read_basic(m, 'b', &v)) > 0) out = static_cast<long long>(v != 0);
        return r;
    }
    if (s == "i") {
        std::int32_t v = 0;
        if ((r = sd_bus_message_read_basic(m, 'i', &v)) > 0) out = static_cast<long long>(v);
        return r;
    }
    if (s == "u") {
        std::uint32_t v = 0;
        if ((r = sd_bus_message_read_basic(m, 'u', &v)) > 0) out = static_cast<long long>(v);
        return r;
    }
    if (s == "y") {
        std::uint8_t v = 0;
        if ((r = sd_bus_message_read_basic(m, 'y', &v)) > 0) out = static_cast<long long>(v);
        return r;
    }
    if (s == "as" || s == "ao") {
        char** v = nullptr;
        if ((r = sd_bus_message_read_strv(m, &v)) < 0) return r;
        out = take_strv(v);
        return 1;
    }
    if (s == "a{uu}") {
        MmModem::Pairs pairs;
        if ((r = sd_bus_message_enter_container(m, 'a', "{uu}")) < 0) return r;
        std::uint32_t k = 0, v = 0;
        while ((r = sd_bus_message_read(m, "{uu}", &k, &v)) > 0) pairs.emplace_back(k, v);
        if (r < 0 || (r = sd_bus_message_exit_container(m)) < 0) return r;
        out = std::move(pairs);
        return 1;
    }
    if (s == "a{sv}") {
        MmModem::Ints ints;
        if ((r = sd_bus_message_enter_container(m, 'a', "{sv}")) < 0) return r;
        while ((r = sd_bus_message_enter_container(m, 'e', "sv")) > 0) {
            const char* key = nullptr;
            const char* vsig = nullptr;
            MmModem::Prop v;
            if ((r = sd_bus_message_read_basic(m, 's', &key)) < 0 || (r = sd_bus_message_peek_type(m, nullptr, &vsig)) < 0 ||
                (r = sd_bus_message_enter_container(m, 'v', vsig)) < 0 || (r = read_value(m, vsig, v)) < 0 ||
                (r = sd_bus_message_exit_container(m)) < 0 || (r = sd_bus_message_exit_container(m)) < 0)
                return r;
            if (auto* n = std::get_if<long long>(&v)) ints[key] = *n;
        }
        if (r < 0 || (r = sd_bus_message_exit_container(m)) < 0) return r;
        out = std::move(ints);
        return 1;
    }
    if (s == "(ub)") {
        std::uint32_t q = 0;
        int recent = 0;
        if ((r = sd_bus_message_read(m, "(ub)", &q, &recent)) > 0) out = static_cast<long long>(q);
        return r;
    }
    if (s == "a(su)") {
        MmModem::Ints ports;
        if ((r = sd_bus_message_enter_container(m, 'a', "(su)")) < 0) return r;
        const char* name = nullptr;
        std::uint32_t type = 0;
        while ((r = sd_bus_message_read(m, "(su)", &name, &type)) > 0) ports[name] = type;
        if (r < 0 || (r = sd_bus_message_exit_container(m)) < 0) return r;
        out = std::move(ports);
        return 1;
    }
    return sd_bus_message_skip(m, sig);
}

int read_props(sd_bus_message* m, MmModem::Props& out) {
    int r = sd_bus_message_enter_container(m, 'a', "{sv}");
    if (r < 0) return r;
    while ((r = sd_bus_message_enter_container(m, 'e', "sv")) > 0) {
        const char* key = nullptr;
        const char* sig = nullptr;
        MmModem::Prop v;
        if ((r = sd_bus_message_read_basic(m, 's', &key)) < 0) return r;
        if ((r = sd_bus_message_peek_type(m, nullptr, &sig)) < 0) return r;
        if ((r = sd_bus_message_enter_container(m, 'v', sig)) < 0) return r;
        if ((r = read_value(m, sig, v)) < 0) return r;
        if ((r = sd_bus_message_exit_container(m)) < 0) return r;
        if ((r = sd_bus_message_exit_container(m)) < 0) return r;
        out[key] = std::move(v);
    }
    if (r < 0) return r;
    return sd_bus_message_exit_container(m);
}

int read_ifaces(sd_bus_message* m, MmModem::Ifaces& out) {
    int r = sd_bus_message_enter_container(m, 'a', "{sa{sv}}");
    if (r < 0) return r;
    while ((r = sd_bus_message_enter_container(m, 'e', "sa{sv}")) > 0) {
        const char* iface = nullptr;
        if ((r = sd_bus_message_read_basic(m, 's', &iface)) < 0) return r;
        if ((r = read_props(m, out[iface])) < 0) return r;
        if ((r = sd_bus_message_exit_container(m)) < 0) return r;
    }
    if (r < 0) return r;
    return sd_bus_message_exit_container(m);
}

long long num(const MmModem::Props& p, const char* key, long long def = 0) {
    auto it = p.find(key);
    const long long* v = it == p.end() ? nullptr : std::get_if<long long>(&it->second);
    return v ? *v : def;
}

const std::string* text(const MmModem::Props& p, const char* key) {
    auto it = p.find(key);
    return it == p.end() ? nullptr : std::get_if<std::string>(&it->second);
}

const std::vector<std::string>* list(const MmModem::Props& p, const char* key) {
    auto it = p.find(key);
    return it == p.end() ? nullptr : std::get_if<std::vector<std::string>>(&it->second);
}

int on_reply(sd_bus_message* m, void* data, sd_bus_error*) {
    auto& done = *static_cast<MmModem::Reply*>(data);
    if (done) done(m, sd_bus_message_get_error(m));
    return 0;
}

MmModem::Args object_arg(std::string path) {
    return [path = std::move(path)](sd_bus_message* m) { return sd_bus_message_append(m, "o", path.c_str()); };
}

MmModem::Args iface_arg(const char* iface) {
    return [iface](sd_bus_message* m) { return sd_bus_message_append(m, "s", iface); };
}

}  // namespace

template <void (MmModem::*F)(sd_bus_message*)>
int MmModem::on_signal(sd_bus_message* m, void* self, sd_bus_error*) {
    (static_cast<MmModem*>(self)->*F)(m);
    return 0;
}

MmModem::MmModem(Loop& loop, Events events) : loop_(loop), ev_(std::move(events)) {}

MmModem::~MmModem() {
    if (!bus_) return;
    loop_.unwatch(sd_bus_get_fd(bus_));
    sd_bus_flush_close_unref(bus_);
}

Result MmModem::start() {
    int r = sd_bus_open_system(&bus_);
    if (r < 0) return std::unexpected(std::format("cannot connect to the system bus: {}", std::strerror(-r)));
    struct Match {
        const char* path;
        const char* iface;
        const char* member;
        sd_bus_message_handler_t handler;
    };
    const Match matches[] = {
        {kRoot, kObjects, "InterfacesAdded", on_signal<&MmModem::on_added>},
        {kRoot, kObjects, "InterfacesRemoved", on_signal<&MmModem::on_removed>},
        {nullptr, kProps, "PropertiesChanged", on_signal<&MmModem::on_props>},
        {nullptr, kVoice, "CallAdded", on_signal<&MmModem::on_call_added>},
        {nullptr, kVoice, "CallDeleted", on_signal<&MmModem::on_call_deleted>},
        {nullptr, kCall, "StateChanged", on_signal<&MmModem::on_call_state>},
        {nullptr, kMessaging, "Added", on_signal<&MmModem::on_sms_added>},
        {nullptr, kCellBroadcast, "Added", on_signal<&MmModem::on_cbm_added>},
        {nullptr, kTime, "NetworkTimeChanged", on_signal<&MmModem::on_network_time>},
    };
    r = sd_bus_add_match(bus_, nullptr,
                         "type='signal',sender='org.freedesktop.DBus',path='/org/freedesktop/DBus',"
                         "interface='org.freedesktop.DBus',member='NameOwnerChanged',arg0='org.freedesktop.ModemManager1'",
                         on_signal<&MmModem::on_owner>, this);
    for (const auto& mt : matches)
        if (r >= 0) r = sd_bus_match_signal(bus_, nullptr, kService, mt.path, mt.iface, mt.member, mt.handler, this);
    if (r < 0) return std::unexpected(std::format("cannot subscribe to ModemManager: {}", std::strerror(-r)));
    loop_.watch(sd_bus_get_fd(bus_), [this] { pump(); });
    tick();
    discover();
    // Subscribing was synchronous; whatever arrived meanwhile is already read.
    loop_.after(0ms, [this] { pump(); });
    return {};
}

void MmModem::invoke(const std::string& path, const char* iface, const char* member, const Args& args, Reply done,
                     std::uint64_t timeout_usec) {
    sd_bus_message* m = nullptr;
    sd_bus_slot* slot = nullptr;
    auto* pending = new Reply(std::move(done));
    int r = sd_bus_message_new_method_call(bus_, &m, kService, path.c_str(), iface, member);
    if (r >= 0 && args) r = args(m);
    if (r >= 0) r = sd_bus_call_async(bus_, &slot, m, on_reply, pending, timeout_usec);
    sd_bus_message_unref(m);
    if (r < 0) {
        warn(std::format("{}.{} on {}: {}", iface, member, path, std::strerror(-r)));
        Reply cb = std::move(*pending);
        delete pending;
        sd_bus_error e{};
        e.name = SD_BUS_ERROR_FAILED;
        e.message = "the request could not be sent";
        if (cb) cb(nullptr, &e);
        return;
    }
    sd_bus_slot_set_destroy_callback(slot, [](void* p) { delete static_cast<Reply*>(p); });
    sd_bus_slot_set_floating(slot, 1);
    sd_bus_slot_unref(slot);
}

MmModem::Reply MmModem::logged(std::string what) {
    return [what = std::move(what)](sd_bus_message*, const sd_bus_error* e) {
        if (e) warn(std::format("{}: {}", what, err_text(e)));
    };
}

void MmModem::pump() {
    if (failed_) return;
    int r;
    while ((r = sd_bus_process(bus_, nullptr)) > 0) {}
    if (r < 0) fail(std::format("system bus: {}", std::strerror(-r)));
}

// sd-bus's own timeouts (a request ModemManager never answers) need the clock
// to be looked at even when nothing arrives.
void MmModem::tick() {
    loop_.after(1s, [this] {
        pump();
        if (!failed_) tick();
    });
}

void MmModem::fail(const std::string& why) {
    failed_ = true;
    warn(why);
    loop_.stop();
}

void MmModem::discover() {
    invoke(kRoot, kObjects, "GetManagedObjects", {}, [this](sd_bus_message* m, const sd_bus_error* e) {
        if (modem_) return;
        if (e) {
            if (!warned_absent_) warn(std::format("ModemManager is not available ({}); waiting for it", err_text(e)));
            warned_absent_ = true;
            publish();
            return;
        }
        int r = sd_bus_message_enter_container(m, 'a', "{oa{sa{sv}}}");
        while (r >= 0 && (r = sd_bus_message_enter_container(m, 'e', "oa{sa{sv}}")) > 0) {
            const char* path = nullptr;
            Ifaces ifaces;
            if ((r = sd_bus_message_read_basic(m, 'o', &path)) < 0 || (r = read_ifaces(m, ifaces)) < 0) break;
            r = sd_bus_message_exit_container(m);
            if (!modem_ && ifaces.contains(kModem)) adopt(path, ifaces);
        }
        if (r < 0) warn(std::format("reading ModemManager's modems: {}", std::strerror(-r)));
        if (!modem_) std::println("phone-telephonyd: ModemManager has no modem yet");
        publish();
    });
}

void MmModem::adopt(const std::string& path, const Ifaces& ifaces) {
    modem_ = Device{.path = path};
    warned_absent_ = false;
    std::println("phone-telephonyd: modem {}", path);
    std::fflush(stdout);
    for (const auto& [iface, props] : ifaces) apply(iface, props, true);
    if (ifaces.contains(kTime)) fetch_network_time();
    publish();
    follow_radio();
    fetch_sim();
}

void MmModem::apply(const std::string& iface, const Props& p, bool initial) {
    Device& d = *modem_;
    if (iface == kModem) {
        d.state = num(p, "State", d.state);
        d.quality = num(p, "SignalQuality", d.quality);
        d.access = num(p, "AccessTechnologies", d.access);
        if (auto* own = list(p, "OwnNumbers")) d.own = *own;
        d.lock = num(p, "UnlockRequired", d.lock);
        d.failed_reason = num(p, "StateFailedReason", d.failed_reason);
        if (auto it = p.find("UnlockRetries"); it != p.end())
            if (auto* r = std::get_if<Pairs>(&it->second)) d.retries = *r;
        if (auto* sim = text(p, "Sim"); sim && *sim != d.sim) {
            d.sim = *sim;
            d.sim_mcc.clear();
            d.sim_asked = false;
        }
        if (auto* imei = text(p, "EquipmentIdentifier")) d.imei = *imei;
        d.power = num(p, "PowerState", d.power);
        if (auto it = p.find("Ports"); it != p.end())
            if (auto* ports = std::get_if<Ints>(&it->second)) d.ports = *ports;
        if (auto* bearers = list(p, "Bearers")) {
            std::erase_if(d.bearers, [&](const auto& b) { return std::ranges::find(*bearers, b.first) == bearers->end(); });
            for (const auto& b : *bearers)
                if (!d.bearers.contains(b)) {
                    d.bearers[b] = {};
                    fetch_bearer(b);
                }
        }
    } else if (iface == k3gpp) {
        long long reg = d.reg;
        d.reg = num(p, "RegistrationState", d.reg);
        if (d.reg != reg) d.connect_failed = false;
        d.facility_locks = num(p, "EnabledFacilityLocks", d.facility_locks);
        if (auto* op = text(p, "OperatorName")) d.op = *op;
        if (auto* code = text(p, "OperatorCode")) d.net_mcc = code->size() >= 3 ? code->substr(0, 3) : "";
    } else if (iface == kTime) {
        if (auto it = p.find("NetworkTimezone"); it != p.end()) {
            if (auto* tz = std::get_if<Ints>(&it->second); tz && tz->contains("offset")) {
                Nitz n = d.nitz.value_or(Nitz{});
                n.offset = static_cast<int>(tz->at("offset"));
                n.dst = tz->contains("dst-offset") ? static_cast<int>(tz->at("dst-offset")) : 0;
                d.nitz = n;
            }
        }
    } else if (iface == kVoice) {
        d.voice = true;
        if (auto* calls = list(p, "Calls"); calls && initial)
            for (const auto& c : *calls) fetch_call(c);
    } else if (iface == kMessaging) {
        d.messaging = true;
        if (auto* messages = list(p, "Messages"); messages && initial)
            for (const auto& s : *messages) fetch_sms(s);
    } else if (iface == kCellBroadcast) {
        d.cell_broadcast = true;
        if (initial) {
            set_cb_channels();
            if (auto* cbms = list(p, "CellBroadcasts"))
                for (const auto& c : *cbms) fetch_cbm(c);
        }
    } else if (iface == kUssd) {
        d.ussd = true;
        d.ussd_state = num(p, "State", d.ussd_state);
        if (!initial) ussd_changed(p);
    }
}

void MmModem::drop_calls(const std::string& why) {
    std::vector<std::string> ids;
    for (const auto& [id, _] : calls_) ids.push_back(id);
    for (const auto& id : ids) {
        const Call& c = calls_[id].call;
        end(id, c.dir == "in" && !c.connected ? "missed" : "dropped", false);
    }
    if (!ids.empty()) warn(std::format("{} call(s) dropped: {}", ids.size(), why));
}

void MmModem::drop_modem(const std::string& why) {
    if (!modem_) return;
    warn(std::format("modem {} {}", modem_->path, why));
    ussd_ended();
    modem_.reset();
    drop_calls(why);
    receiving_.clear();
    delivered_.clear();
    unacked_sms_.clear();
    unacked_cbm_.clear();
    cbm_receiving_.clear();
    cbm_seen_.clear();
    reports_.clear();
    publish();
}

void MmModem::follow_radio() {
    if (!modem_) return;
    if (radio_) maybe_enable();
    else power_down();
    maybe_connect();
    if (!sos_.empty() && modem_->voice && modem_->state >= mm::ModemEnabled) place(std::exchange(sos_, {}));
}

// Airplane mode: disabled (calls and bearers end with it), then in low
// power, which takes the radio off the air.
void MmModem::power_down() {
    Device& d = *modem_;
    if (d.disabling || d.enabling || d.state == mm::ModemDisabling || d.state == mm::ModemEnabling) return;
    std::string path = d.path;
    if (d.state >= mm::ModemEnabled) {
        d.disabling = true;
        invoke(path, kModem, "Enable", [](sd_bus_message* m) { return sd_bus_message_append(m, "b", 0); },
               [this, path](sd_bus_message*, const sd_bus_error* e) {
                   if (!modem_ || modem_->path != path) return;
                   modem_->disabling = false;
                   if (e) warn(std::format("cannot disable the modem: {}", err_text(e)));
                   else follow_radio();
               }, kNetworkTimeout);
        return;
    }
    if (d.power == mm::PowerLow || d.state == mm::ModemFailed) return;
    d.power = mm::PowerLow;
    invoke(path, kModem, "SetPowerState", [](sd_bus_message* m) { return sd_bus_message_append(m, "u", static_cast<std::uint32_t>(mm::PowerLow)); },
           logged("powering the radio down"), kNetworkTimeout);
}

// The bearer follows the data switch: connected with the network's default
// APN (none given) once the network takes the phone. Choosing an APN is not
// implemented.
void MmModem::maybe_connect() {
    Device& d = *modem_;
    bool up = std::ranges::any_of(d.bearers, [](const auto& b) { return b.second.connected; });
    std::string path = d.path;
    if (!data_ || !radio_) {
        if (!up || d.disconnecting) return;
        d.disconnecting = true;
        invoke(path, kSimple, "Disconnect", [](sd_bus_message* m) { return sd_bus_message_append(m, "o", "/"); },
               [this, path](sd_bus_message*, const sd_bus_error* e) {
                   if (!modem_ || modem_->path != path) return;
                   modem_->disconnecting = false;
                   if (e) warn(std::format("mobile data: cannot disconnect: {}", err_text(e)));
               }, kNetworkTimeout);
        return;
    }
    if (up || d.connecting || d.connect_failed || !mm::registered(d.reg) || d.state < mm::ModemRegistered) return;
    d.connecting = true;
    invoke(path, kSimple, "Connect", [](sd_bus_message* m) { return sd_bus_message_append(m, "a{sv}", 1, "apn", "s", ""); },
           [this, path](sd_bus_message*, const sd_bus_error* e) {
               if (!modem_ || modem_->path != path) return;
               modem_->connecting = false;
               if (!e) return;
               modem_->connect_failed = true;
               warn(std::format("mobile data: cannot connect: {}", err_text(e)));
           }, kNetworkTimeout);
}

void MmModem::fetch_bearer(const std::string& path) {
    invoke(path, kProps, "GetAll", iface_arg(kBearer), [this, path](sd_bus_message* m, const sd_bus_error* e) {
        Props p;
        if (e || !modem_ || !modem_->bearers.contains(path) || read_props(m, p) < 0) return;
        auto& b = modem_->bearers[path];
        b.connected = num(p, "Connected") != 0;
        if (auto* i = text(p, "Interface")) b.iface = *i;
        publish();
        follow_radio();
    });
}

Result MmModem::set_radio(bool on) {
    if (!on)
        for (const auto& [_, t] : calls_)
            if (t.call.emergency) return std::unexpected("an emergency call is in progress");
    if (on == radio_) return {};
    radio_ = on;
    if (modem_) {
        modem_->gave_up = false;
        modem_->connect_failed = false;
        if (on && modem_->power == mm::PowerLow) {
            modem_->power = mm::PowerOn;
            std::string path = modem_->path;
            invoke(path, kModem, "SetPowerState", [](sd_bus_message* m) { return sd_bus_message_append(m, "u", static_cast<std::uint32_t>(mm::PowerOn)); },
                   [this, path](sd_bus_message*, const sd_bus_error* e) {
                       if (e) warn(std::format("powering the radio up: {}", err_text(e)));
                       if (modem_ && modem_->path == path) follow_radio();
                   }, kNetworkTimeout);
        } else {
            follow_radio();
        }
    }
    publish();
    return {};
}

Result MmModem::set_data(bool on) {
    if (on == data_) return {};
    data_ = on;
    if (modem_) {
        modem_->connect_failed = false;
        follow_radio();
    }
    publish();
    return {};
}

void MmModem::maybe_enable() {
    if (!modem_ || !radio_) return;
    Device& d = *modem_;
    if (d.state != mm::ModemDisabled || d.enabling || d.gave_up) return;
    d.enabling = true;
    std::string path = d.path;
    invoke(path, kModem, "Enable", [](sd_bus_message* m) { return sd_bus_message_append(m, "b", 1); },
           [this, path](sd_bus_message*, const sd_bus_error* e) {
               if (!modem_ || modem_->path != path) return;
               modem_->enabling = false;
               if (e) {
                   modem_->gave_up = true;
                   warn(std::format("cannot enable the modem: {}", err_text(e)));
               }
           }, kNetworkTimeout);
}

Status MmModem::status() const {
    Status s{.backend = "modemmanager"};
    s.radio = radio_;
    s.data = data_;
    if (!modem_) return s;
    bool up = false;
    for (const auto& [name, type] : modem_->ports)
        if (type == mm::PortNet) s.data_ifaces.push_back(name);
    for (const auto& [_, b] : modem_->bearers) {
        up = up || b.connected;
        if (!b.iface.empty() && std::ranges::find(s.data_ifaces, b.iface) == s.data_ifaces.end()) s.data_ifaces.push_back(b.iface);
    }
    s.data_state = !radio_ || !data_ ? "off" : up ? "connected" : "idle";
    s.sim = mm::sim_state(modem_->state, modem_->lock, modem_->failed_reason);
    for (const auto& [lock, left] : modem_->retries)
        if (lock == modem_->lock) s.sim_tries = static_cast<int>(left);
    if (s.sim != "pin" && s.sim != "puk") s.sim_tries = 0;
    s.registered = mm::registered(modem_->reg);
    s.op = modem_->op;
    s.rat = mm::rat(static_cast<std::uint64_t>(modem_->access));
    s.own = modem_->own.empty() ? "" : modem_->own.front();
    s.signal = s.registered ? mm::bars(modem_->quality) : 0;
    s.pin_lock = (modem_->facility_locks & 1) != 0;
    s.imei = modem_->imei;
    s.sim_mcc = s.sim == "ready" ? modem_->sim_mcc : "";
    if (s.registered) {
        s.net_mcc = modem_->net_mcc;
        s.nitz = modem_->nitz;
    }
    return s;
}

// The SIM's identity is readable once it is unlocked: its home network's
// code (MCC and MNC) is where the phone is from.
void MmModem::fetch_sim() {
    if (!modem_ || modem_->sim.empty() || modem_->sim == "/" || modem_->sim_asked || modem_->state <= mm::ModemLocked) return;
    modem_->sim_asked = true;
    std::string sim = modem_->sim;
    invoke(sim, kProps, "GetAll", iface_arg(kSim), [this, sim](sd_bus_message* m, const sd_bus_error* e) {
        if (!modem_ || modem_->sim != sim) return;
        Props p;
        if (e || read_props(m, p) < 0) {
            modem_->sim_asked = false;
            return;
        }
        if (auto* id = text(p, "OperatorIdentifier")) modem_->sim_mcc = id->size() >= 3 ? id->substr(0, 3) : "";
        publish();
    });
}

void MmModem::fetch_network_time() {
    std::string path = modem_->path;
    invoke(path, kTime, "GetNetworkTime", {}, [this, path](sd_bus_message* m, const sd_bus_error* e) {
        const char* iso = nullptr;
        if (e || !modem_ || modem_->path != path || sd_bus_message_read_basic(m, 's', &iso) < 0) return;
        network_time(iso);
    });
}

void MmModem::on_network_time(sd_bus_message* m) {
    const char* p = sd_bus_message_get_path(m);
    const char* iso = nullptr;
    if (!modem_ || !p || modem_->path != p || sd_bus_message_read_basic(m, 's', &iso) < 0) return;
    network_time(iso);
}

void MmModem::network_time(std::string_view iso) {
    long long t = mm::sms_time(iso, 0);
    if (t == 0) return;
    Nitz n = modem_->nitz.value_or(Nitz{});
    n.time = t;
    n.received = unix_now();
    modem_->nitz = n;
    publish();
}

void MmModem::publish() {
    Status s = status();
    std::string now = json::dump(s.to_json());
    if (now == last_status_) return;
    last_status_ = std::move(now);
    if (ev_.status) ev_.status(s);
}

void MmModem::on_owner(sd_bus_message* m) {
    const char *name = nullptr, *old_owner = nullptr, *new_owner = nullptr;
    if (sd_bus_message_read(m, "sss", &name, &old_owner, &new_owner) < 0) return;
    if (*old_owner) drop_modem("went away with ModemManager");
    if (*new_owner) {
        std::println("phone-telephonyd: ModemManager is up");
        std::fflush(stdout);
        discover();
    }
}

void MmModem::on_added(sd_bus_message* m) {
    const char* path = nullptr;
    Ifaces ifaces;
    if (sd_bus_message_read_basic(m, 'o', &path) < 0 || read_ifaces(m, ifaces) < 0) return;
    if (!modem_) {
        if (ifaces.contains(kModem)) adopt(path, ifaces);
        return;
    }
    if (modem_->path != path) return;
    for (const auto& [iface, props] : ifaces) apply(iface, props, true);
    publish();
    follow_radio();
}

void MmModem::on_removed(sd_bus_message* m) {
    const char* path = nullptr;
    char** names = nullptr;
    if (sd_bus_message_read_basic(m, 'o', &path) < 0 || sd_bus_message_read_strv(m, &names) < 0) return;
    std::vector<std::string> gone = take_strv(names);
    auto has = [&](const char* iface) { return std::ranges::find(gone, iface) != gone.end(); };
    if (!modem_ || modem_->path != path) return;
    if (has(kModem)) {
        drop_modem("was removed");
        discover();
        return;
    }
    if (has(kVoice)) {
        modem_->voice = false;
        drop_calls("the modem lost its voice interface");
    }
    if (has(kMessaging)) modem_->messaging = false;
    if (has(kCellBroadcast)) modem_->cell_broadcast = false;
    if (has(kUssd)) {
        ussd_ended();
        modem_->ussd = false;
    }
}

void MmModem::on_props(sd_bus_message* m) {
    const char* p = sd_bus_message_get_path(m);
    std::string path = p ? p : "";
    const char* iface = nullptr;
    Props props;
    if (sd_bus_message_read_basic(m, 's', &iface) < 0 || read_props(m, props) < 0) return;
    std::string_view in = iface;
    if (modem_ && path == modem_->path) {
        if (in == kModem || in == k3gpp || in == kUssd || in == kTime) {
            apply(iface, props, false);
            publish();
            follow_radio();
            fetch_sim();
        }
        return;
    }
    if (modem_ && in == kBearer && modem_->bearers.contains(path)) {
        auto& b = modem_->bearers[path];
        if (props.contains("Connected")) b.connected = num(props, "Connected") != 0;
        if (auto* i = text(props, "Interface")) b.iface = *i;
        publish();
        follow_radio();
        return;
    }
    if (modem_ && path == modem_->sim && in == kSim) {
        if (auto* id = text(props, "OperatorIdentifier")) {
            modem_->sim_mcc = id->size() >= 3 ? id->substr(0, 3) : "";
            publish();
        }
        return;
    }
    if (in == kCall) {
        auto it = by_path_.find(path);
        const std::string* number = text(props, "Number");
        if (it == by_path_.end() || !number || number->empty()) return;
        Tracked& t = calls_[it->second];
        if (t.call.number == *number) return;
        t.call.number = *number;
        emit(t.call);
        return;
    }
    if (in == kSms && receiving_.contains(path) && num(props, "State") == mm::SmsReceived) {
        receiving_.erase(path);
        fetch_sms(path);
        return;
    }
    if (in == kSms && reports_.contains(path) && props.contains("DeliveryState")) {
        delivery_report(path, props);
        return;
    }
    if (in == kCbm && cbm_receiving_.contains(path) && num(props, "State") == mm::CbmReceived) {
        cbm_receiving_.erase(path);
        fetch_cbm(path);
    }
}

void MmModem::on_call_added(sd_bus_message* m) {
    const char* path = nullptr;
    if (sd_bus_message_read_basic(m, 'o', &path) < 0) return;
    if (!by_path_.contains(path)) fetch_call(path);
}

void MmModem::on_call_deleted(sd_bus_message* m) {
    const char* path = nullptr;
    if (sd_bus_message_read_basic(m, 'o', &path) < 0) return;
    auto it = by_path_.find(path);
    if (it == by_path_.end()) return;
    std::string id = it->second;
    const Tracked& t = calls_[id];
    end(id, mm::end_reason(t.call.dir, t.call.connected != 0, t.hung_up, 0), false);
}

void MmModem::on_call_state(sd_bus_message* m) {
    const char* p = sd_bus_message_get_path(m);
    std::int32_t old_state = 0, new_state = 0;
    std::uint32_t reason = 0;
    if (!p || sd_bus_message_read(m, "iiu", &old_state, &new_state, &reason) < 0) return;
    auto it = by_path_.find(p);
    if (it == by_path_.end()) return;
    std::string id = it->second;
    Tracked& t = calls_[id];
    std::string state = mm::call_state(new_state);
    if (state.empty() || state == t.call.state) return;
    if (state == "ended") {
        end(id, mm::end_reason(t.call.dir, t.call.connected != 0, t.hung_up, reason), true);
        return;
    }
    t.call.state = state;
    if (state == "active" && !t.call.connected) t.call.connected = unix_now();
    emit(t.call);
}

void MmModem::on_sms_added(sd_bus_message* m) {
    const char* path = nullptr;
    int received = 0;
    if (sd_bus_message_read(m, "ob", &path, &received) < 0) return;
    if (received) fetch_sms(path);
}

void MmModem::on_cbm_added(sd_bus_message* m) {
    const char* path = nullptr;
    if (sd_bus_message_read_basic(m, 'o', &path) < 0) return;
    fetch_cbm(path);
}

MmModem::Tracked* MmModem::find(std::string_view id) {
    auto it = calls_.find(std::string(id));
    return it == calls_.end() ? nullptr : &it->second;
}

void MmModem::fetch_call(const std::string& path) {
    invoke(path, kProps, "GetAll", iface_arg(kCall), [this, path](sd_bus_message* m, const sd_bus_error* e) {
        if (e || !modem_ || by_path_.contains(path)) return;
        Props p;
        if (read_props(m, p) < 0) return;
        long long state = num(p, "State");
        if (state == mm::CallTerminated) {
            invoke(modem_->path, kVoice, "DeleteCall", object_arg(path), logged("deleting an ended call"));
            return;
        }
        std::string shown = mm::call_state(state);
        if (shown.empty()) return;
        const std::string* number = text(p, "Number");
        std::string id = std::format("c{}", ++next_call_);
        Tracked& t = calls_[id] = Tracked{
            .call = Call{.id = id, .number = number ? *number : "", .dir = num(p, "Direction") == mm::DirIncoming ? "in" : "out",
                         .state = shown, .started = unix_now()},
            .path = path};
        if (shown == "active" || shown == "held") t.call.connected = t.call.started;
        by_path_[path] = id;
        emit(t.call);
    });
}

void MmModem::end(const std::string& id, const std::string& reason, bool delete_on_modem) {
    auto it = calls_.find(id);
    if (it == calls_.end()) return;
    Tracked t = std::move(it->second);
    calls_.erase(it);
    if (!t.path.empty()) by_path_.erase(t.path);
    t.call.state = "ended";
    t.call.reason = reason;
    t.call.ended = unix_now();
    emit(t.call);
    if (delete_on_modem && modem_ && !t.path.empty())
        invoke(modem_->path, kVoice, "DeleteCall", object_arg(t.path), logged("deleting an ended call"));
}

void MmModem::emit(const Call& c) {
    if (ev_.call) ev_.call(c);
}

std::vector<Call> MmModem::calls() const {
    std::vector<Call> out;
    for (const auto& [_, t] : calls_) out.push_back(t.call);
    return out;
}

std::expected<std::string, std::string> MmModem::dial(std::string_view number) {
    if (!valid_number(number)) return std::unexpected(std::format("invalid number '{}'", number));
    bool sos = emergency_number(number);
    if (!radio_ && !sos) return std::unexpected("the radio is off");
    if (!modem_ || (radio_ && !modem_->voice)) return std::unexpected("no modem that can place calls");
    if (modem_->state == mm::ModemLocked) return std::unexpected("the SIM is locked");
    for (const auto& [_, t] : calls_)
        if (t.call.state != "held") return std::unexpected("another call is in progress");
    std::string id = std::format("c{}", ++next_call_);
    Tracked& t = calls_[id] = Tracked{
        .call = Call{.id = id, .number = std::string(number), .dir = "out", .state = "dialing", .started = unix_now(), .emergency = sos}};
    emit(t.call);
    if (radio_) {
        place(id);
    } else {
        // Powered and enabled for the call, which follow_radio then places.
        sos_ = id;
        (void)set_radio(true);
    }
    return id;
}

void MmModem::place(const std::string& id) {
    Tracked* call = find(id);
    if (!call || !modem_) return;
    invoke(modem_->path, kVoice, "CreateCall",
           [n = call->call.number](sd_bus_message* m) { return sd_bus_message_append(m, "a{sv}", 1, "number", "s", n.c_str()); },
           [this, id](sd_bus_message* m, const sd_bus_error* e) {
               const char* p = nullptr;
               if (e || sd_bus_message_read_basic(m, 'o', &p) < 0) {
                   warn(std::format("placing call {}: {}", id, err_text(e)));
                   end(id, "failed", false);
                   return;
               }
               std::string path = p;
               Tracked* t = find(id);
               if (!t) {
                   // Hung up before ModemManager made the call.
                   if (modem_) invoke(modem_->path, kVoice, "DeleteCall", object_arg(path), logged("deleting a cancelled call"));
                   return;
               }
               t->path = path;
               by_path_[path] = id;
               invoke(path, kCall, "Start", {}, [this, id](sd_bus_message*, const sd_bus_error* e) {
                   if (!e) return;
                   warn(std::format("starting call {}: {}", id, err_text(e)));
                   end(id, "failed", true);
               }, kNetworkTimeout);
           });
}

Result MmModem::answer(std::string_view id) {
    Tracked* t = find(id);
    if (!t || t->call.state != "incoming" || t->path.empty() || !modem_) return std::unexpected("no incoming call with that id");
    bool other = false;
    for (const auto& [k, o] : calls_)
        if (k != t->call.id && o.call.state == "active") other = true;
    if (other) invoke(modem_->path, kVoice, "HoldAndAccept", {}, logged("answering a waiting call"), kNetworkTimeout);
    else invoke(t->path, kCall, "Accept", {}, logged("answering"), kNetworkTimeout);
    return {};
}

Result MmModem::hangup(std::string_view id) {
    Tracked* t = find(id);
    if (!t) return std::unexpected("no such call");
    t->hung_up = true;
    if (t->path.empty()) {
        end(t->call.id, "hangup", false);
        return {};
    }
    invoke(t->path, kCall, "Hangup", {}, logged("hanging up"), kNetworkTimeout);
    return {};
}

// ModemManager has no per-call hold: HoldAndAccept holds the active calls and
// resumes a held one, which for a single call is hold and resume.
Result MmModem::hold(std::string_view id, bool on) {
    Tracked* t = find(id);
    if (!t || (t->call.state != "active" && t->call.state != "held") || !modem_) return std::unexpected("call is not connected");
    if (on == (t->call.state == "held")) return {};
    invoke(modem_->path, kVoice, "HoldAndAccept", {}, logged(on ? "holding" : "resuming"), kNetworkTimeout);
    return {};
}

// The microphone belongs to the audio path, not to ModemManager.
Result MmModem::mute(std::string_view id, bool on) {
    Tracked* t = find(id);
    if (!t) return std::unexpected("no such call");
    t->call.muted = on;
    emit(t->call);
    return {};
}

std::expected<std::string, std::string> MmModem::send_sms(std::string_view to, std::string_view body, std::string_view id) {
    if (!valid_number(to)) return std::unexpected(std::format("invalid number '{}'", to));
    if (body.empty()) return std::unexpected("empty message");
    if (!modem_ || !modem_->messaging) return std::unexpected("no modem that can send SMS");
    if (modem_->state == mm::ModemLocked) return std::unexpected("the SIM is locked");
    Sms out{.id = id.empty() ? std::format("m{}-{}", boot_, ++next_sms_) : std::string(id), .peer = std::string(to), .dir = "out",
            .text = std::string(body), .at = unix_now(), .read = true, .state = "sending"};
    if (ev_.sms) ev_.sms(out);
    auto settle = [this, out](const sd_bus_error* e) mutable {
        if (e) warn(std::format("SMS to {} not sent: {}", out.peer, err_text(e)));
        out.state = e ? "failed" : "sent";
        if (ev_.sms) ev_.sms(out);
    };
    invoke(modem_->path, kMessaging, "Create",
           [out](sd_bus_message* m) {
               return sd_bus_message_append(m, "a{sv}", 3, "number", "s", out.peer.c_str(), "text", "s", out.text.c_str(),
                                            "delivery-report-request", "b", 1);
           },
           [this, settle, out](sd_bus_message* m, const sd_bus_error* e) mutable {
               const char* p = nullptr;
               if (e || sd_bus_message_read_basic(m, 'o', &p) < 0) {
                   sd_bus_error bad{};
                   bad.name = SD_BUS_ERROR_FAILED;
                   bad.message = "ModemManager did not create the message";
                   settle(e ? e : &bad);
                   return;
               }
               std::string path = p;
               invoke(path, kSms, "Send", {}, [this, settle, path, out](sd_bus_message*, const sd_bus_error* e) mutable {
                   settle(e);
                   if (e || !modem_) {
                       delete_sms(path);
                       return;
                   }
                   // A report may never come (the recipient's phone stays
                   // off for days): the oldest wait gives way.
                   if (reports_.size() >= 64) {
                       delete_sms(reports_.begin()->first);
                       reports_.erase(reports_.begin());
                   }
                   out.state = "sent";
                   reports_[path] = out;
               }, kNetworkTimeout);
           });
    return out.id;
}

void MmModem::delivery_report(const std::string& path, const Props& props) {
    std::string state = mm::delivery(num(props, "DeliveryState", 0x100));
    if (state.empty()) return;
    Sms s = reports_[path];
    reports_.erase(path);
    s.state = state;
    if (ev_.sms) ev_.sms(s);
    delete_sms(path);
}

Result MmModem::unlock(std::string_view pin, std::string_view puk) {
    if (!modem_ || modem_->state != mm::ModemLocked || modem_->sim.empty() || modem_->sim == "/")
        return std::unexpected("the SIM is not locked");
    if (!valid_pin(pin) || (!puk.empty() && (puk.size() != 8 || !valid_pin(puk)))) return std::unexpected("a PIN is 4-8 digits, a PUK 8");
    std::string p(pin), k(puk);
    invoke(modem_->sim, kSim, k.empty() ? "SendPin" : "SendPuk",
           [p, k](sd_bus_message* m) {
               return k.empty() ? sd_bus_message_append(m, "s", p.c_str()) : sd_bus_message_append(m, "ss", k.c_str(), p.c_str());
           },
           [this](sd_bus_message*, const sd_bus_error* e) {
               if (ev_.unlock) ev_.unlock(e ? err_text(e) : "");
           }, kNetworkTimeout);
    return {};
}

Result MmModem::set_pin_lock(std::string_view pin, bool on) {
    if (!modem_ || modem_->sim.empty() || modem_->sim == "/") return std::unexpected("no SIM");
    if (modem_->state == mm::ModemLocked) return std::unexpected("the SIM is locked");
    if (!valid_pin(pin)) return std::unexpected("a PIN is 4-8 digits");
    std::string p(pin);
    invoke(modem_->sim, kSim, "EnablePin",
           [p, on](sd_bus_message* m) { return sd_bus_message_append(m, "sb", p.c_str(), on ? 1 : 0); },
           [this](sd_bus_message*, const sd_bus_error* e) {
               if (ev_.pin) ev_.pin(e ? err_text(e) : "");
           }, kNetworkTimeout);
    return {};
}

Result MmModem::change_pin(std::string_view pin, std::string_view new_pin) {
    if (!modem_ || modem_->sim.empty() || modem_->sim == "/") return std::unexpected("no SIM");
    if (modem_->state == mm::ModemLocked) return std::unexpected("the SIM is locked");
    if (!(modem_->facility_locks & 1)) return std::unexpected("turn the PIN lock on first");
    if (!valid_pin(pin) || !valid_pin(new_pin)) return std::unexpected("a PIN is 4-8 digits");
    std::string p(pin), n(new_pin);
    invoke(modem_->sim, kSim, "ChangePin",
           [p, n](sd_bus_message* m) { return sd_bus_message_append(m, "ss", p.c_str(), n.c_str()); },
           [this](sd_bus_message*, const sd_bus_error* e) {
               if (ev_.pin) ev_.pin(e ? err_text(e) : "");
           }, kNetworkTimeout);
    return {};
}

void MmModem::fetch_sms(const std::string& path) {
    if (delivered_.contains(path)) return;
    invoke(path, kProps, "GetAll", iface_arg(kSms), [this, path](sd_bus_message* m, const sd_bus_error* e) {
        if (e || !modem_ || delivered_.contains(path)) return;
        Props p;
        if (read_props(m, p) < 0) return;
        long long state = num(p, "State");
        if (state == mm::SmsReceiving) {
            receiving_.insert(path);
            return;
        }
        if (state != mm::SmsReceived) return;
        delivered_.insert(path);
        receiving_.erase(path);
        // A delivery report ModemManager did not match to a message it sent.
        if (num(p, "PduType") == mm::PduStatusReport) {
            delete_sms(path);
            return;
        }
        std::string from = text(p, "Number") ? *text(p, "Number") : "";
        std::string body = text(p, "Text") ? *text(p, "Text") : "";
        std::string at = text(p, "Timestamp") ? *text(p, "Timestamp") : "";
        std::string id = mm::stable_id('m', from + '\x1f' + at + '\x1f' + body);
        unacked_sms_[id] = path;
        if (ev_.sms) ev_.sms(Sms{.id = id, .peer = from, .dir = "in", .text = body, .at = mm::sms_time(at, unix_now())});
    });
}

void MmModem::ack_sms(std::string_view id) {
    auto it = unacked_sms_.find(std::string(id));
    if (it == unacked_sms_.end()) return;
    std::string path = it->second;
    unacked_sms_.erase(it);
    delete_sms(path);
}

// Only once phone-smsd has a message: the modem's storage (often the SIM's
// few dozen slots) would otherwise fill up and stop taking new ones.
void MmModem::delete_sms(const std::string& path) {
    if (!modem_) return;
    invoke(modem_->path, kMessaging, "Delete", object_arg(path), [this, path](sd_bus_message*, const sd_bus_error* e) {
        if (e) warn(std::format("deleting {} from the modem: {}", path, err_text(e)));
        else delivered_.erase(path);
    });
}

// ETWS (4352-4359) and CMAS / EU-Alert (4370-4400); a modem may listen to
// none of them until told.
void MmModem::set_cb_channels() {
    invoke(modem_->path, kCellBroadcast, "SetChannels",
           [](sd_bus_message* m) { return sd_bus_message_append(m, "a(uu)", 2, 4352u, 4359u, 4370u, 4400u); },
           logged("choosing the cell broadcast channels"));
}

void MmModem::fetch_cbm(const std::string& path) {
    if (cbm_seen_.contains(path)) return;
    invoke(path, kProps, "GetAll", iface_arg(kCbm), [this, path](sd_bus_message* m, const sd_bus_error* e) {
        if (e || !modem_ || cbm_seen_.contains(path)) return;
        Props p;
        if (read_props(m, p) < 0) return;
        long long state = num(p, "State");
        if (state == mm::CbmReceiving) {
            cbm_receiving_.insert(path);
            return;
        }
        if (state != mm::CbmReceived) return;
        cbm_seen_.insert(path);
        int channel = static_cast<int>(num(p, "Channel"));
        int serial = static_cast<int>((num(p, "MessageCode") << 4) | (num(p, "Update") & 0xf));
        std::string body = text(p, "Text") ? *text(p, "Text") : "";
        std::string id = mm::stable_id('b', std::format("{}\x1f{}\x1f{}", channel, serial, body));
        unacked_cbm_[id] = path;
        if (ev_.cb) ev_.cb(Cb{.id = id, .channel = channel, .serial = serial, .text = body, .at = unix_now()});
    });
}

void MmModem::ack_cb(std::string_view id) {
    auto it = unacked_cbm_.find(std::string(id));
    if (it == unacked_cbm_.end() || !modem_) return;
    std::string path = it->second;
    unacked_cbm_.erase(it);
    invoke(modem_->path, kCellBroadcast, "Delete", object_arg(path), [this, path](sd_bus_message*, const sd_bus_error* e) {
        if (e) warn(std::format("deleting {} from the modem: {}", path, err_text(e)));
        else cbm_seen_.erase(path);
    });
}

void MmModem::ussd_initiate(std::string_view code, UssdReply done) {
    if (!valid_ussd(code)) return done(std::unexpected("invalid code"));
    if (!modem_ || !modem_->ussd) return done(std::unexpected("no modem that can send USSD"));
    if (modem_->state == mm::ModemLocked) return done(std::unexpected("the SIM is locked"));
    if (!mm::registered(modem_->reg)) return done(std::unexpected("not registered on the network"));
    if (modem_->ussd_busy || modem_->ussd_waiting) return done(std::unexpected("ussd session open"));
    ussd_request("Initiate", std::string(code), std::move(done));
}

void MmModem::ussd_respond(std::string_view response, UssdReply done) {
    if (!modem_ || !modem_->ussd_waiting || modem_->ussd_busy) return done(std::unexpected("no ussd session"));
    ussd_request("Respond", std::string(response), std::move(done));
}

Result MmModem::ussd_cancel() {
    if (!modem_ || !modem_->ussd) return {};
    Device& d = *modem_;
    bool open = d.ussd_waiting || d.ussd_busy || d.ussd_state == mm::UssdActive || d.ussd_state == mm::UssdUserResponse;
    d.ussd_waiting = false;
    if (open) invoke(d.path, kUssd, "Cancel", {}, logged("cancelling USSD"));
    return {};
}

// Initiate and Respond answer with the network's text. Whether the network
// then waits for a reply is the session's State, read once more because its
// PropertiesChanged may come after the answer.
void MmModem::ussd_request(const char* member, std::string arg, UssdReply done) {
    std::string path = modem_->path;
    modem_->ussd_busy = true;
    modem_->ussd_waiting = false;
    invoke(path, kUssd, member, [arg](sd_bus_message* m) { return sd_bus_message_append(m, "s", arg.c_str()); },
           [this, path, done](sd_bus_message* m, const sd_bus_error* e) {
               const char* reply = nullptr;
               if (e || sd_bus_message_read_basic(m, 's', &reply) < 0) {
                   if (modem_ && modem_->path == path) modem_->ussd_busy = false;
                   return done(std::unexpected(err_text(e)));
               }
               invoke(path, kProps, "Get", [](sd_bus_message* m) { return sd_bus_message_append(m, "ss", kUssd, "State"); },
                      [this, path, done, answer = std::string(reply)](sd_bus_message* m, const sd_bus_error* e) {
                          bool ours = modem_ && modem_->path == path;
                          std::uint32_t state = 0;
                          if (e || sd_bus_message_read(m, "v", "u", &state) < 0) state = ours ? modem_->ussd_state : 0;
                          bool waiting = ours && state == mm::UssdUserResponse;
                          if (ours) {
                              modem_->ussd_busy = false;
                              modem_->ussd_waiting = waiting;
                          }
                          done(Ussd{waiting ? "input" : "done", answer});
                      });
           }, kNetworkTimeout);
}

// What the network sends by itself comes as property changes; replies to
// Initiate and Respond come back from those calls instead.
void MmModem::ussd_changed(const Props& p) {
    Device& d = *modem_;
    if (auto* t = text(p, "NetworkNotification"); t && !t->empty() && ev_.ussd) ev_.ussd(Ussd{"notify", *t});
    if (auto* t = text(p, "NetworkRequest"); t && !t->empty() && !d.ussd_busy) {
        d.ussd_waiting = true;
        if (ev_.ussd) ev_.ussd(Ussd{"input", *t});
    } else if (p.contains("State") && d.ussd_state == mm::UssdIdle && !d.ussd_busy) {
        ussd_ended();
    }
}

void MmModem::ussd_ended() {
    if (!modem_ || !modem_->ussd_waiting) return;
    modem_->ussd_waiting = false;
    if (ev_.ussd) ev_.ussd(Ussd{"done", ""});
}

}  // namespace tel
