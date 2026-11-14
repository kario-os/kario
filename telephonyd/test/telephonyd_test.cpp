// Unit tests for the JSON codec and the telephony state machine, driven by the
// loop's fake clock. Run by `make test` (ctest).

#include <algorithm>
#include <chrono>
#include <cstdlib>
#include <filesystem>
#include <fstream>
#include <print>
#include <string>
#include <string_view>
#include <vector>

#include "json.hpp"
#include "loop.hpp"
#include "mm_state.hpp"
#include "modem.hpp"
#include "service.hpp"

using namespace std::chrono_literals;

static int failures = 0;

#define CHECK(cond)                                                          \
    do {                                                                     \
        if (!(cond)) {                                                       \
            std::println(stderr, "{}:{}: CHECK failed: {}", __FILE__, __LINE__, #cond); \
            ++failures;                                                      \
        }                                                                    \
    } while (0)

// The network the boot selftest sees: sim-network.json in the source tree.
static const tel::SimNetwork& network() {
    static const tel::SimNetwork net = [] {
        auto n = tel::SimNetwork::load(TEL_SIM_NETWORK);
        if (!n) {
            std::println(stderr, "{}", n.error());
            std::exit(EXIT_FAILURE);
        }
        return *n;
    }();
    return net;
}

static void test_json() {
    auto v = json::parse(R"({"a":[1,2.5,"x\u4e2d\n"],"b":true,"c":null,"d":{"e":"f"}})");
    CHECK(v.has_value());
    CHECK(v->find("a")->is_array());
    CHECK(std::get<json::Array>(*v->find("a"))[2] == json::Value("x\u4e2d\n"));
    CHECK(v->flag("b"));
    CHECK(v->find("d")->str("e") == "f");
    CHECK(json::dump(*json::parse(json::dump(*v))) == json::dump(*v));
    CHECK(json::dump(json::Object{{"n", 42}, {"s", "\u5f15\u53f7\""}}) == "{\"n\":42,\"s\":\"\u5f15\u53f7\\\"\"}");
    CHECK(!json::parse("{\"a\":").has_value());
    CHECK(!json::parse("[1,2] x").has_value());
}

static void test_calls_and_sms() {
    tel::Loop loop;
    std::vector<json::Value> events;
    tel::Service svc("", [&](const json::Value& ev) { events.push_back(ev); });
    tel::SimModem sim(loop, svc.events(), network());
    svc.attach(&sim, &sim);
    auto state = [&] {
        auto calls = svc.handle("calls", json::Object{});
        auto& a = std::get<json::Array>(*calls);
        return a.empty() ? std::string("none") : a[0].str("state");
    };

    auto r = svc.handle("dial", json::Object{{"number", "13800000001"}});
    CHECK(r.has_value());
    std::string id = r->str("call_id");
    CHECK(state() == "dialing");
    loop.advance(400ms);
    CHECK(state() == "alerting");
    loop.advance(1000ms);
    CHECK(state() == "active");
    CHECK(!svc.handle("dial", json::Object{{"number", "10086"}}).has_value());  // one call at a time
    CHECK(svc.handle("hangup", json::Object{{"call_id", id}}).has_value());
    CHECK(state() == "none");

    CHECK(!svc.handle("dial", json::Object{{"number", "12a"}}).has_value());
    svc.handle("dial", json::Object{{"number", "13900000000"}});  // ...000 is busy
    loop.advance(2000ms);
    auto log = std::get<json::Array>(*svc.handle("log", json::Object{}));
    CHECK(log.size() == 2 && log[0].str("result") == "busy" && log[1].str("result") == "outgoing");

    auto ring = svc.handle("sim.ring", json::Object{{"number", "13700000000"}});
    CHECK(ring.has_value() && state() == "incoming");
    CHECK(svc.handle("answer", json::Object{{"call_id", ring->str("call_id")}}).has_value());
    CHECK(state() == "active");
    svc.handle("hangup", json::Object{{"call_id", ring->str("call_id")}});
    log = std::get<json::Array>(*svc.handle("log", json::Object{}));
    CHECK(log[0].str("result") == "answered");

    ring = svc.handle("sim.ring", json::Object{{"number", "13700000000"}});
    loop.advance(31000ms);
    log = std::get<json::Array>(*svc.handle("log", json::Object{}));
    CHECK(log[0].str("result") == "missed");

    CHECK(svc.handle("sms.send", json::Object{{"to", "13800000001"}, {"text", "hello"}, {"id", "o1"}})->str("id") == "o1");
    loop.advance(1000ms);
    auto pending = std::get<json::Array>(*svc.handle("sms.pending", json::Object{}));
    CHECK(pending.size() == 3 && pending[0].str("key") == "o1@sent" && pending[1].str("key") == "o1@delivered");
    CHECK(pending[2].find("sms")->str("dir") == "in" && pending[2].find("sms")->str("text") == network().sms_echo + "hello");

    CHECK(svc.handle("sms.send", json::Object{{"to", "10086999"}, {"text", "this fails"}}).has_value());
    CHECK(svc.handle("sms.send", json::Object{{"to", "10086998"}, {"text", "never delivered"}}).has_value());
    loop.advance(1000ms);
    pending = std::get<json::Array>(*svc.handle("sms.pending", json::Object{}));
    CHECK(pending.size() == 6 && pending[3].find("sms")->str("state") == "failed" && pending[4].find("sms")->str("state") == "sent");
    CHECK(pending[5].find("sms")->str("dir") == "in");
    CHECK(!svc.handle("sms.list", json::Object{}).has_value());

    int call_events = 0, sms_events = 0;
    for (const auto& e : events) {
        call_events += e.str("event") == "call";
        sms_events += e.str("event") == "sms";
    }
    CHECK(call_events >= 10 && sms_events == 6);
}

static std::string temp_dir() {
    std::string dir = (std::filesystem::temp_directory_path() / "telephonyd-test-XXXXXX").string();
    CHECK(mkdtemp(dir.data()) != nullptr);
    return dir;
}

// What phone-smsd has not acknowledged stays queued, across restarts; a
// message the modem reports twice is queued once; "sending" is not queued.
static void test_sms_pending() {
    std::string dir = temp_dir();
    {
        tel::Service svc(dir, [](const json::Value&) {});
        auto ev = svc.events();
        ev.sms(tel::Sms{.id = "m1", .peer = "10010", .dir = "out", .text = "balance?", .at = 5, .read = true, .state = "sending"});
        ev.sms(tel::Sms{.id = "m2", .peer = "10010", .dir = "in", .text = "88.88", .at = 6});
        ev.sms(tel::Sms{.id = "m2", .peer = "10010", .dir = "in", .text = "88.88", .at = 6});
        ev.sms(tel::Sms{.id = "m1", .peer = "10010", .dir = "out", .text = "balance?", .at = 5, .read = true, .state = "failed"});
        ev.cb(tel::Cb{.id = "b1", .channel = 4370, .serial = 0x3000, .text = "Alert", .at = 7});
        auto pending = std::get<json::Array>(*svc.handle("sms.pending", json::Object{}));
        CHECK(pending.size() == 3 && pending[0].str("key") == "m2" && pending[1].str("key") == "m1@failed" && pending[2].str("key") == "b1");
        CHECK(pending[2].str("event") == "cb" && pending[2].find("cb")->num("channel") == 4370);
        CHECK(svc.handle("sms.ack", json::Object{{"keys", json::Array{"m1@failed", "nothing"}}})->num("acked") == 1);
    }
    auto perms = std::filesystem::status(std::filesystem::path(dir) / "telephony.json").permissions();
    CHECK((perms & (std::filesystem::perms::group_all | std::filesystem::perms::others_all)) == std::filesystem::perms::none);
    tel::Service again(dir, [](const json::Value&) {});
    auto pending = std::get<json::Array>(*again.handle("sms.pending", json::Object{}));
    CHECK(pending.size() == 2 && pending[0].str("key") == "m2" && pending[1].str("key") == "b1");
    CHECK(again.handle("sms.ack", json::Object{{"keys", json::Array{"m2", "b1"}}})->num("acked") == 2);
    CHECK(std::get<json::Array>(*again.handle("sms.pending", json::Object{})).empty());

    // A held message (selftest): queued and on disk, never announced; the
    // next process offers it.
    {
        tel::Loop loop;
        int events = 0;
        tel::Service svc(dir, [&](const json::Value& ev) { events += ev.str("event") == "sms"; });
        tel::SimModem sim(loop, svc.events(), network());
        svc.attach(&sim, &sim);
        CHECK(svc.handle("sim.sms", json::Object{{"from", "10086"}, {"text", "held"}, {"held", true}}).has_value());
        CHECK(events == 0 && std::get<json::Array>(*svc.handle("sms.pending", json::Object{})).size() == 1);
    }
    tel::Service restarted(dir, [](const json::Value&) {});
    pending = std::get<json::Array>(*restarted.handle("sms.pending", json::Object{}));
    CHECK(pending.size() == 1 && pending[0].find("sms")->str("text") == "held");
    std::filesystem::remove_all(dir);
}

// The store telephonyd kept before phone-smsd is handed over once.
static void test_sms_legacy() {
    std::string dir = temp_dir();
    {
        std::ofstream out(std::filesystem::path(dir) / "telephony.json");
        out << R"({"log":[],"sms":[{"id":"s1","peer":"10086","dir":"in","text":"old","at":1,"read":false}]})";
    }
    {
        tel::Service svc(dir, [](const json::Value&) {});
        auto legacy = std::get<json::Array>(*svc.handle("sms.legacy", json::Object{}));
        CHECK(legacy.size() == 1 && legacy[0].str("text") == "old");
        tel::Service reread(dir, [](const json::Value&) {});
        CHECK(std::get<json::Array>(*reread.handle("sms.legacy", json::Object{})).size() == 1);
        CHECK(svc.handle("sms.legacy_drop", json::Object{}).has_value());
    }
    tel::Service after(dir, [](const json::Value&) {});
    CHECK(std::get<json::Array>(*after.handle("sms.legacy", json::Object{})).empty());
    std::filesystem::remove_all(dir);
}

static void test_cell_broadcast() {
    tel::Loop loop;
    std::vector<json::Value> events;
    tel::Service svc("", [&](const json::Value& ev) { events.push_back(ev); });
    tel::SimModem sim(loop, svc.events(), network());
    svc.attach(&sim, &sim);
    CHECK(svc.handle("sim.cb", json::Object{{"channel", 4371}, {"serial", 12288}, {"text", "Flood warning"}}).has_value());
    CHECK(!svc.handle("sim.cb", json::Object{{"channel", 70000}, {"text", "x"}}).has_value());
    CHECK(!svc.handle("sim.cb", json::Object{{"channel", 4370}, {"text", ""}}).has_value());
    CHECK(events.size() == 1 && events[0].str("event") == "cb" && events[0].find("cb")->str("text") == "Flood warning");
    CHECK(events[0].find("cb")->num("serial") == 12288 && events[0].str("key") == events[0].find("cb")->str("id"));
}

static void test_sim_lock() {
    tel::Loop loop;
    std::vector<json::Value> events;
    tel::Service svc("", [&](const json::Value& ev) { events.push_back(ev); });
    tel::SimModem sim(loop, svc.events());
    svc.attach(&sim, &sim);
    auto status = [&] { return *svc.handle("status", json::Object{}); };
    auto unlocked = [&] {
        for (auto it = events.rbegin(); it != events.rend(); ++it)
            if (it->str("event") == "unlock") return it->flag("ok");
        return false;
    };
    CHECK(status().str("sim") == "ready");
    CHECK(svc.handle("sim.lock", json::Object{{"pin", "1234"}}).has_value());
    CHECK(status().str("sim") == "pin" && status().num("sim_tries") == 3 && !status().flag("registered"));
    CHECK(!svc.handle("dial", json::Object{{"number", "10086"}}).has_value());
    CHECK(!svc.handle("sim.unlock", json::Object{{"pin", "12"}}).has_value());
    CHECK(svc.handle("sim.unlock", json::Object{{"pin", "0000"}}).has_value());
    loop.advance(300ms);
    CHECK(!unlocked() && status().num("sim_tries") == 2);
    svc.handle("sim.unlock", json::Object{{"pin", "0000"}});
    svc.handle("sim.unlock", json::Object{{"pin", "0000"}});
    loop.advance(300ms);
    CHECK(status().str("sim") == "puk" && status().num("sim_tries") == 10);
    CHECK(svc.handle("sim.unlock", json::Object{{"puk", "12345678"}, {"pin", "4321"}}).has_value());
    loop.advance(300ms);
    CHECK(unlocked() && status().str("sim") == "ready" && status().flag("registered"));
}

// Settings › SIM lock: the PIN lock off and on, the PIN changed; wrong PINs
// use up the tries and end in the PUK.
static void test_sim_pin_settings() {
    tel::Loop loop;
    std::vector<json::Value> events;
    tel::Service svc("", [&](const json::Value& ev) { events.push_back(ev); });
    tel::SimModem sim(loop, svc.events());
    svc.attach(&sim, &sim);
    auto status = [&] { return *svc.handle("status", json::Object{}); };
    auto pin_ok = [&] {
        for (auto it = events.rbegin(); it != events.rend(); ++it)
            if (it->str("event") == "pin") return it->flag("ok");
        return false;
    };
    CHECK(!status().flag("pin_lock"));
    CHECK(!svc.handle("sim.change_pin", json::Object{{"pin", "1234"}, {"new_pin", "2468"}}).has_value());
    CHECK(svc.handle("sim.pin_lock", json::Object{{"pin", "1234"}, {"on", true}}).has_value());
    loop.advance(300ms);
    CHECK(pin_ok() && status().flag("pin_lock"));
    CHECK(svc.handle("sim.change_pin", json::Object{{"pin", "1234"}, {"new_pin", "2468"}}).has_value());
    loop.advance(300ms);
    CHECK(pin_ok());
    svc.handle("sim.pin_lock", json::Object{{"pin", "1234"}, {"on", false}});
    loop.advance(300ms);
    CHECK(!pin_ok() && status().flag("pin_lock"));
    CHECK(svc.handle("sim.pin_lock", json::Object{{"pin", "2468"}, {"on", false}}).has_value());
    loop.advance(300ms);
    CHECK(pin_ok() && !status().flag("pin_lock"));
    for (int i = 0; i < 3; i++) svc.handle("sim.pin_lock", json::Object{{"pin", "0000"}, {"on", true}});
    loop.advance(300ms);
    CHECK(status().str("sim") == "puk" && !status().flag("registered"));
}

static bool luhn(std::string_view digits) {
    int sum = 0;
    for (size_t i = 0; i < digits.size(); ++i) {
        int d = digits[digits.size() - 1 - i] - '0';
        if (i % 2) d = d * 2 > 9 ? d * 2 - 9 : d * 2;
        sum += d;
    }
    return sum % 10 == 0;
}

static void test_imei() {
    CHECK(luhn("490154203237518") && !luhn("490154203237519"));
    tel::Loop loop;
    tel::Service svc("", [](const json::Value&) {});
    tel::SimModem sim(loop, svc.events());
    svc.attach(&sim, &sim);
    std::string imei = svc.handle("status", json::Object{})->str("imei");
    CHECK(imei.size() == 15 && imei.starts_with("86") && luhn(imei));
    CHECK(std::ranges::all_of(imei, [](char c) { return c >= '0' && c <= '9'; }));
    svc.handle("sim.lock", json::Object{{"pin", "1234"}});
    CHECK(svc.handle("status", json::Object{})->str("imei") == imei);
}

// Airplane mode and mobile data: with the radio off the phone is on no
// network (calls, SMS, USSD and incoming calls refused) but an emergency
// number powers it up and rings; both switches survive a restart.
static void test_radio() {
    auto dir = std::filesystem::temp_directory_path() / "telephonyd_radio_test";
    std::filesystem::remove_all(dir);
    auto refused = [](const std::expected<json::Value, std::string>& r, std::string_view why) { return !r && r.error() == why; };
    {
        tel::Loop loop;
        std::vector<json::Value> events;
        tel::Service svc(dir.string(), [&](const json::Value& ev) { events.push_back(ev); });
        tel::SimModem sim(loop, svc.events(), network());
        svc.attach(&sim, &sim);
        auto st = [&] { return *svc.handle("status", json::Object{}); };
        CHECK(st().flag("radio") && st().flag("data") && st().str("data_state") == "connected");
        const json::Value* ifaces = st().find("data_ifaces");
        CHECK(ifaces && ifaces->is_array() && std::get<json::Array>(*ifaces) == json::Array{json::Value(tel::kSimDataIface)});
        CHECK(svc.handle("data.set", json::Object{{"on", false}}).has_value());
        CHECK(st().str("data_state") == "off" && st().flag("registered"));

        std::string id = svc.handle("dial", json::Object{{"number", "13800000001"}})->str("call_id");
        loop.advance(1500ms);
        CHECK(svc.handle("radio.set", json::Object{{"on", false}}).has_value());
        CHECK(!st().flag("radio") && !st().flag("registered") && st().num("signal") == 0 && st().str("operator").empty());
        CHECK(std::get<json::Array>(*svc.handle("calls", json::Object{})).empty());
        CHECK(refused(svc.handle("dial", json::Object{{"number", "10086"}}), "the radio is off"));
        CHECK(refused(svc.handle("sms.send", json::Object{{"to", "10086"}, {"text", "hi"}}), "the radio is off"));
        CHECK(refused(svc.handle("sim.ring", json::Object{{"number", "13800000002"}}), "the radio is off"));
        CHECK(refused(svc.handle("ussd.initiate", json::Object{{"code", "*100#"}}), "the radio is off"));

        events.clear();
        auto sos = svc.handle("dial", json::Object{{"number", "112"}});
        CHECK(sos.has_value());
        CHECK(!events.empty() && events.front().str("event") == "call" && events.front().find("call")->flag("emergency"));
        loop.advance(600ms);
        CHECK(st().flag("radio") && st().flag("registered"));
        loop.advance(1200ms);
        auto calls = std::get<json::Array>(*svc.handle("calls", json::Object{}));
        CHECK(calls.size() == 1 && calls[0].str("state") == "active" && calls[0].flag("emergency"));
        CHECK(refused(svc.handle("radio.set", json::Object{{"on", false}}), "an emergency call is in progress"));
        CHECK(svc.handle("hangup", json::Object{{"call_id", sos->str("call_id")}}).has_value());
        CHECK(svc.handle("radio.set", json::Object{{"on", false}}).has_value());
    }
    {
        tel::Loop loop;
        tel::Service svc(dir.string(), [](const json::Value&) {});
        tel::SimModem sim(loop, svc.events(), network());
        svc.attach(&sim, &sim);
        auto st = *svc.handle("status", json::Object{});
        CHECK(!st.flag("radio", true) && !st.flag("data", true) && !st.flag("registered"));
        CHECK(svc.handle("radio.set", json::Object{{"on", true}}).has_value());
        st = *svc.handle("status", json::Object{});
        CHECK(st.flag("registered") && st.str("data_state") == "off");
    }
    std::filesystem::remove_all(dir);
}

static void test_ussd() {
    tel::Loop loop;
    std::vector<json::Value> events;
    tel::Service svc("", [&](const json::Value& ev) { events.push_back(ev); });
    const tel::SimNetwork& net = network();
    tel::SimModem sim(loop, svc.events(), net);
    svc.attach(&sim, &sim);
    auto send = [&](const char* method, const char* key, std::string value) {
        return svc.handle(method, json::Object{{key, std::move(value)}});
    };
    auto is = [](const std::expected<json::Value, std::string>& r, std::string_view state, std::string_view text) {
        return r && r->str("state") == state && r->str("text") == text;
    };
    auto fails = [](const std::expected<json::Value, std::string>& r, std::string_view err) { return !r && r.error() == err; };
    const std::string& menu = net.ussd_menu;

    CHECK(is(send("ussd.initiate", "code", "*100#"), "done", net.ussd_answers.at("*100#")));
    CHECK(is(send("ussd.initiate", "code", "*123#"), "done", net.ussd_unknown));
    CHECK(is(send("ussd.initiate", "code", "#" + std::string(30, '1') + "#"), "done", net.ussd_unknown));
    for (const auto& bad : std::vector<std::string>{"", "100#", "*100", "*#", "*10a#", "**", "1*100#", "*" + std::string(31, '1') + "#"})
        CHECK(fails(send("ussd.initiate", "code", bad), "invalid code"));
    CHECK(fails(send("ussd.respond", "text", "1"), "no ussd session"));
    CHECK(svc.handle("ussd.cancel", json::Object{}).has_value());

    CHECK(is(send("ussd.initiate", "code", "*101#"), "input", menu));
    CHECK(fails(send("ussd.initiate", "code", "*100#"), "ussd session open"));
    CHECK(is(send("ussd.respond", "text", "9"), "input", net.ussd_menu_retry + "\n" + menu));
    CHECK(is(send("ussd.respond", "text", "1"), "done", net.ussd_menu_answers.at("1")));
    CHECK(fails(send("ussd.respond", "text", "1"), "no ussd session"));
    send("ussd.initiate", "code", "*101#");
    CHECK(is(send("ussd.respond", "text", "2"), "done", net.ussd_menu_answers.at("2")));
    send("ussd.initiate", "code", "*101#");
    CHECK(is(send("ussd.respond", "text", "3"), "done", net.ussd_menu_answers.at("3")));

    send("ussd.initiate", "code", "*101#");
    CHECK(svc.handle("ussd.cancel", json::Object{}).has_value());
    CHECK(fails(send("ussd.respond", "text", "1"), "no ussd session"));
    CHECK(events.empty());

    CHECK(svc.handle("sim.ussd", json::Object{{"text", "Your plan renews tomorrow."}, {"request", false}}).has_value());
    CHECK(events.size() == 1 && events[0].str("event") == "ussd" && events[0].str("state") == "notify" &&
          events[0].str("text") == "Your plan renews tomorrow.");
    CHECK(fails(send("ussd.respond", "text", "1"), "no ussd session"));
    CHECK(svc.handle("sim.ussd", json::Object{{"text", "Reply 1 to renew"}, {"request", true}}).has_value());
    CHECK(events.size() == 2 && events[1].str("state") == "input" && events[1].str("text") == "Reply 1 to renew");
    CHECK(fails(send("ussd.initiate", "code", "*100#"), "ussd session open"));
    CHECK(is(send("ussd.respond", "text", "anything"), "done", net.ussd_received));

    // Locking the SIM ends a waiting session from the network's side.
    send("ussd.initiate", "code", "*101#");
    svc.handle("sim.lock", json::Object{{"pin", "1234"}});
    CHECK(events.size() >= 3 && events[2].str("event") == "ussd" && events[2].str("state") == "done" && events[2].str("text") == "");
    CHECK(fails(send("ussd.respond", "text", "1"), "no ussd session"));
    CHECK(fails(send("ussd.initiate", "code", "*100#"), "the SIM is locked"));
    CHECK(fails(svc.handle("sim.ussd", json::Object{{"text", "x"}, {"request", true}}), "the SIM is locked"));
}

static void test_sim_network() {
    const tel::SimNetwork def;
    auto empty = tel::SimNetwork::parse("{}");
    CHECK(empty && empty->op == def.op && empty->ussd_menu == def.ussd_menu && empty->sms_replies == def.sms_replies);
    auto some = tel::SimNetwork::parse(R"({"operator":"X","ussd_menu_answers":{"1":"one"}})");
    const tel::SimNetwork::Texts one{{"1", "one"}};
    CHECK(some && some->op == "X" && some->ussd_menu_answers == one);
    CHECK(some && some->ussd_unknown == def.ussd_unknown && some->ussd_answers == def.ussd_answers);
    for (const char* bad : {"[]", "{", R"({"operator":1})", R"({"opertor":"X"})", R"({"sms_replies":"x"})", R"({"sms_replies":{"1":2}})"})
        CHECK(!tel::SimNetwork::parse(bad).has_value());
    CHECK(!tel::SimNetwork::load("/nonexistent/sim-network.json").has_value());

    // The data file sets every text, so none of the English shows through.
    const tel::SimNetwork& net = network();
    CHECK(net.op != def.op && net.sms_echo != def.sms_echo && net.sms_replies != def.sms_replies);
    CHECK(net.ussd_answers != def.ussd_answers && net.ussd_menu != def.ussd_menu && net.ussd_menu_answers != def.ussd_menu_answers);
    CHECK(net.ussd_menu_retry != def.ussd_menu_retry && net.ussd_unknown != def.ussd_unknown && net.ussd_received != def.ussd_received);

    tel::Loop loop;
    tel::Service svc("", [](const json::Value&) {});
    tel::SimModem sim(loop, svc.events(), net);
    svc.attach(&sim, &sim);
    CHECK(svc.handle("status", json::Object{})->str("operator") == net.op);
    CHECK(svc.handle("sms.send", json::Object{{"to", "10086"}, {"text", "balance?"}}).has_value());
    loop.advance(1000ms);
    auto pending = std::get<json::Array>(*svc.handle("sms.pending", json::Object{}));
    CHECK(pending.size() == 3 && pending[2].find("sms")->str("text") == net.sms_replies.at("10086"));

    tel::SimModem english(loop, svc.events());
    CHECK(english.status().op == def.op);
}

// The SIM's country, the network's and the zone the network sends: from
// sim-network.json, then as a test sets them (a SIM from Mexico).
static void test_country_and_nitz() {
    const tel::SimNetwork& net = network();
    CHECK(net.sim_mcc == "460" && net.nitz && net.nitz->offset == 480);
    for (const char* bad : {R"({"sim_mcc":"46"})", R"({"net_mcc":"4600"})", R"({"nitz_offset":"x"})"})
        CHECK(!tel::SimNetwork::parse(bad).has_value());
    tel::Loop loop;
    std::vector<json::Value> events;
    tel::Service svc("", [&](const json::Value& ev) { events.push_back(ev); });
    tel::SimModem sim(loop, svc.events(), net);
    svc.attach(&sim, &sim);
    auto st = *svc.handle("status", json::Object{});
    CHECK(st.str("sim_mcc") == "460" && st.str("net_mcc") == "460");
    CHECK(st.find("nitz") && st.find("nitz")->num("offset") == 480 && st.find("nitz")->num("time") > 0);
    CHECK(svc.handle("sim.network", json::Object{{"sim_mcc", "334"}, {"net_mcc", "334"}, {"nitz_offset", -360}}).has_value());
    st = *svc.handle("status", json::Object{});
    CHECK(st.str("sim_mcc") == "334" && st.find("nitz")->num("offset") == -360 && st.find("nitz")->num("dst") == 0);
    CHECK(!events.empty() && events.back().str("event") == "status" && events.back().find("status")->str("sim_mcc") == "334");
    CHECK(svc.handle("sim.network", json::Object{{"sim_mcc", "460"}, {"net_mcc", "214"}}).has_value());
    st = *svc.handle("status", json::Object{});
    CHECK(st.str("net_mcc") == "214" && !st.find("nitz"));
    CHECK(!svc.handle("sim.network", json::Object{{"sim_mcc", "33"}}).has_value());
    sim.lock("1234");
    CHECK(svc.handle("status", json::Object{})->str("sim_mcc") == "");
    CHECK(tel::SimModem(loop, svc.events()).status().sim_mcc == "");
}

static void test_modemmanager_values() {
    using namespace tel::mm;
    CHECK(sim_state(8, LockNone, 0) == "ready" && sim_state(ModemLocked, LockSimPin, 0) == "pin");
    CHECK(sim_state(ModemLocked, LockSimPuk, 0) == "puk" && sim_state(ModemLocked, 5, 0) == "locked");
    CHECK(sim_state(ModemFailed, 0, FailedSimMissing) == "absent" && sim_state(ModemFailed, 0, FailedSimError) == "error");
    CHECK(rat(0) == "");
    CHECK(rat(1u << 1) == "2G");
    CHECK(rat(1u << 4) == "2G");
    CHECK(rat((1u << 5) | (1u << 9)) == "3G");
    CHECK(rat(1u << 14) == "4G");
    CHECK(rat((1u << 14) | (1u << 15)) == "5G");
    CHECK(bars(0) == 0 && bars(1) == 1 && bars(49) == 2 && bars(70) == 3 && bars(100) == 4);
    CHECK(registered(1) && registered(5) && !registered(2) && !registered(3) && !registered(8));
    CHECK(call_state(CallRingingOut) == "alerting" && call_state(CallWaiting) == "incoming");
    CHECK(call_state(CallTerminated) == "ended" && call_state(CallUnknown) == "");
    CHECK(end_reason("in", false, true, 0) == "rejected");
    CHECK(end_reason("in", true, true, 0) == "hangup");
    CHECK(end_reason("out", false, false, ReasonRefusedOrBusy) == "busy");
    CHECK(end_reason("out", true, false, ReasonRefusedOrBusy) == "remote");
    CHECK(end_reason("in", false, false, 4) == "missed");
    CHECK(end_reason("out", true, false, ReasonAudioSetupFailed) == "failed");
    CHECK(sms_time("2026-11-09T10:20:30+08:00", 7) == 1794190830);
    CHECK(sms_time("2026-11-09T02:20:30Z", 7) == 1794190830);
    CHECK(sms_time("2026-11-09T10:20:30+08", 7) == 1794190830);
    CHECK(sms_time("2026-11-09T02:20:30.250", 7) == 1794190830);
    CHECK(sms_time("2026-11-09T07:50:30+0530", 7) == 1794190830);
    CHECK(sms_time("", 7) == 7 && sms_time("2026-11-09 10:20:30", 7) == 7 && sms_time("2026-11-09T10:20:30+x", 7) == 7);
    CHECK(delivery(0) == "delivered" && delivery(0x02) == "delivered" && delivery(0x21) == "" && delivery(0x41) == "failed");
    CHECK(delivery(0x62) == "failed" && delivery(0x100) == "" && delivery(0x205) == "failed" && delivery(0x305) == "");
    CHECK(stable_id('m', "") == "mcbf29ce484222325" && stable_id('m', "a") == stable_id('m', "a") && stable_id('m', "a") != stable_id('m', "b"));
}

int main() {
    test_json();
    test_calls_and_sms();
    test_sms_pending();
    test_sms_legacy();
    test_cell_broadcast();
    test_sim_lock();
    test_sim_pin_settings();
    test_imei();
    test_radio();
    test_ussd();
    test_sim_network();
    test_country_and_nitz();
    test_modemmanager_values();
    if (failures) {
        std::println(stderr, "{} check(s) failed", failures);
        return EXIT_FAILURE;
    }
    std::println("telephonyd tests passed");
}
