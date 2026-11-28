// Unit tests for the JSON codec and the telephony state machine, driven by the
// loop's fake clock. Run by `make test` (ctest).

#include <algorithm>
#include <cerrno>
#include <chrono>
#include <cstdlib>
#include <filesystem>
#include <format>
#include <fstream>
#include <functional>
#include <print>
#include <sstream>
#include <string>
#include <string_view>
#include <thread>
#include <vector>

#include <signal.h>
#include <sys/wait.h>
#include <unistd.h>

#include "backend.hpp"
#include "ims.hpp"
#include "json.hpp"
#include "loop.hpp"
#ifdef TEL_MODEMMANAGER
#include "mm.hpp"
#endif
#include "mm_state.hpp"
#include "modem.hpp"
#include "run.hpp"
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

// Two calls at most: dialing or answering beside a connected call holds it,
// resuming the held one swaps them; a call still being set up, a ringing
// one, two calls already and an emergency call refuse another.
static void test_second_calls() {
    tel::Loop loop;
    tel::Service svc("", [](const json::Value&) {});
    tel::SimModem sim(loop, svc.events(), network());
    svc.attach(&sim, &sim);
    auto state_of = [&](const std::string& id) {
        auto calls = svc.handle("calls", json::Object{});
        for (const auto& c : std::get<json::Array>(*calls))
            if (c.str("id") == id) return c.str("state");
        return std::string("none");
    };
    auto dial = [&](const std::string& number) { return svc.handle("dial", json::Object{{"number", number}}); };

    std::string first = dial("13800000001")->str("call_id");
    auto refused = dial("10086");
    CHECK(!refused && refused.error() == "another call is being set up");
    loop.advance(1400ms);
    CHECK(state_of(first) == "active");

    auto second = dial("10086");
    CHECK(second.has_value());
    CHECK(state_of(first) == "held" && state_of(second->str("call_id")) == "dialing");
    loop.advance(1400ms);
    CHECK(state_of(second->str("call_id")) == "active");
    auto third = dial("13800000003");
    CHECK(!third && third.error() == "two calls already");

    CHECK(svc.handle("hold", json::Object{{"call_id", first}, {"on", false}}).has_value());  // swap
    CHECK(state_of(first) == "active" && state_of(second->str("call_id")) == "held");
    CHECK(svc.handle("hangup", json::Object{{"call_id", first}}).has_value());
    CHECK(state_of(second->str("call_id")) == "held");  // stays held until resumed
    CHECK(svc.handle("hold", json::Object{{"call_id", second->str("call_id")}, {"on", false}}).has_value());
    CHECK(state_of(second->str("call_id")) == "active");

    // Call waiting: answering holds the connected call; the caller giving up
    // while it rings is a missed call.
    std::string waiting = svc.handle("sim.ring", json::Object{{"number", "13700000000"}})->str("call_id");
    auto busy = dial("13800000003");
    CHECK(!busy && busy.error() == "a call is ringing");
    CHECK(svc.handle("answer", json::Object{{"call_id", waiting}}).has_value());
    CHECK(state_of(waiting) == "active" && state_of(second->str("call_id")) == "held");
    CHECK(svc.handle("sim.hangup", json::Object{{"number", "13700000000"}}).has_value());  // the other party hangs up
    CHECK(state_of(waiting) == "none");
    auto log = std::get<json::Array>(*svc.handle("log", json::Object{}));
    CHECK(log[0].str("number") == "13700000000" && log[0].str("result") == "answered");
    CHECK(!svc.handle("sim.hangup", json::Object{{"number", "13700000000"}}).has_value());
    svc.handle("sim.ring", json::Object{{"number", "13700000001"}});
    CHECK(svc.handle("sim.hangup", json::Object{{"number", "13700000001"}}).has_value());
    log = std::get<json::Array>(*svc.handle("log", json::Object{}));
    CHECK(log[0].str("number") == "13700000001" && log[0].str("result") == "missed");
    svc.handle("hangup", json::Object{{"call_id", second->str("call_id")}});

    std::string sos = dial("112")->str("call_id");
    loop.advance(1400ms);
    CHECK(state_of(sos) == "active");
    auto during = dial("10086");
    CHECK(!during && during.error() == "an emergency call is in progress");
    svc.handle("hangup", json::Object{{"call_id", sos}});
}

// sim.slow: dial is answered late, as a modem acknowledging ATD late, and
// the called party answers late; the loop goes on meanwhile.
static void test_slow_network() {
    tel::Loop loop;
    std::vector<json::Value> events;
    tel::Service svc("", [&](const json::Value& ev) { events.push_back(ev); });
    tel::SimModem sim(loop, svc.events(), network());
    svc.attach(&sim, &sim);
    events.clear();  // the status with the APN chosen
    CHECK(svc.handle("sim.slow", json::Object{{"dial_ms", 3000}, {"answer_ms", 5000}}).has_value());
    std::optional<std::expected<json::Value, std::string>> answer;
    svc.handle("dial", json::Object{{"number", "152"}}, [&](std::expected<json::Value, std::string> r) { answer = std::move(r); });
    CHECK(!answer && events.empty());
    CHECK(svc.handle("status", json::Object{}).has_value());
    loop.advance(2900ms);
    CHECK(!answer);
    loop.advance(200ms);
    CHECK(answer && answer->has_value() && !(*answer)->str("call_id").empty());
    loop.advance(300ms);
    auto calls = std::get<json::Array>(*svc.handle("calls", json::Object{}));
    CHECK(calls.size() == 1 && calls[0].str("state") == "alerting");
    loop.advance(4000ms);
    CHECK(std::get<json::Array>(*svc.handle("calls", json::Object{}))[0].str("state") == "alerting");
    loop.advance(1100ms);
    CHECK(std::get<json::Array>(*svc.handle("calls", json::Object{}))[0].str("state") == "active");
    CHECK(svc.handle("sim.slow", json::Object{{"dial_ms", 0}}).has_value());
    CHECK(svc.handle("dial", json::Object{{"number", "10086"}}).has_value());  // at once again, the first held
}

// A number in a request that no integer of its field holds is refused, not
// converted (tools/cfuzz FuzzTelephonydRPC): JSON numbers are doubles, and
// 1e300 or a minute count past a day are no latency or zone.
static void test_numbers_out_of_range() {
    tel::Loop loop;
    tel::Service svc("", [](const json::Value&) {});
    tel::SimModem sim(loop, svc.events(), network());
    svc.attach(&sim, &sim);
    for (const json::Object& bad : {json::Object{{"dial_ms", 1e300}}, json::Object{{"dial_ms", -1}},
                                    json::Object{{"dial_ms", 0}, {"answer_ms", 1e15}}, json::Object{{"dial_ms", 60001}}})
        CHECK(!svc.handle("sim.slow", bad).has_value());
    CHECK(svc.handle("sim.slow", json::Object{{"dial_ms", 60000}, {"answer_ms", 60000}}).has_value());
    auto network_at = [](json::Value offset, json::Value dst) {
        return json::Object{{"sim_mcc", "460"}, {"net_mcc", "460"}, {"nitz_offset", std::move(offset)}, {"nitz_dst", std::move(dst)}};
    };
    for (const json::Object& bad : {network_at(1e300, 0), network_at(0, -1e300), network_at(1441, 0)})
        CHECK(!svc.handle("sim.network", bad).has_value());
    CHECK(svc.handle("sim.network", network_at(-720, 60)).has_value());
    auto cb = svc.handle("sim.cb", json::Object{{"channel", 4e71}, {"serial", 1}, {"text", "x"}});
    CHECK(!cb && cb.error() == "channel and serial are 16-bit");
    auto sms = svc.handle("sim.sms", json::Object{{"from", "10086"}, {"text", "x"}, {"class", 1e300}});
    CHECK(!sms && sms.error() == "class: 0-3, or -1 for none");
    auto ids = svc.handle("log.import", json::Object{{"entries", json::Array{json::Object{
                                                         {"number", "13800000001"}, {"dir", "in"}, {"result", "missed"}, {"at", 1e300}, {"duration", 0}}}}});
    CHECK(ids && std::get<json::Array>(*ids->find("ids"))[0].is_string() && std::get<std::string>(std::get<json::Array>(*ids->find("ids"))[0]).empty());
}

// DTMF goes only into a connected call, only as the sixteen keys, and the
// tones sent are part of the call's state.
static void test_dtmf() {
    tel::Loop loop;
    tel::Service svc("", [](const json::Value&) {});
    tel::SimModem sim(loop, svc.events(), network());
    svc.attach(&sim, &sim);
    auto send = [&](const std::string& id, const std::string& tones) {
        return svc.handle("dtmf", json::Object{{"call_id", id}, {"tones", tones}}).has_value();
    };
    auto sent = [&] {
        auto calls = svc.handle("calls", json::Object{});
        auto& a = std::get<json::Array>(*calls);
        return a.empty() ? std::string("none") : a[0].str("dtmf");
    };
    std::string id = svc.handle("dial", json::Object{{"number", "10086"}})->str("call_id");
    CHECK(!send(id, "1"));  // not connected yet
    loop.advance(1400ms);
    CHECK(send(id, "1") && send(id, "0#*AD") && sent() == "10#*AD");
    for (const std::string& bad : std::vector<std::string>{"", "12x", "e", " 1", std::string(33, '1')}) CHECK(!send(id, bad));
    CHECK(!send("nosuchcall", "1"));
    CHECK(svc.handle("hold", json::Object{{"call_id", id}, {"on", true}}).has_value());
    CHECK(!send(id, "2"));  // held
    CHECK(sent() == "10#*AD");
    CHECK(tel::valid_dtmf("0123456789*#ABCD") && !tel::valid_dtmf("a"));
    // However long the call, it keeps the last tel::kMaxDtmf tones sent.
    CHECK(svc.handle("hold", json::Object{{"call_id", id}, {"on", false}}).has_value());
    std::string all = "10#*AD";
    for (int i = 0; i < 100; i++) {
        std::string tones = std::to_string(i) + "0123456789*#ABCD";
        CHECK(send(id, tones));
        all += tones;
    }
    CHECK(sent() == all.substr(all.size() - tel::kMaxDtmf));
}

// The SIM's emergency numbers count as emergency calls too, and the status
// tells the shell which they are.
static void test_sim_emergency_numbers() {
    auto net = tel::SimNetwork::parse(R"({"emergency_numbers":["000","08"]})");
    CHECK(!net);  // 08 is not a dialable number
    net = tel::SimNetwork::parse(R"({"emergency_numbers":["000"]})");
    CHECK(net && net->emergency == std::vector<std::string>{"000"});
    CHECK(!tel::SimNetwork::parse(R"({"emergency_numbers":"000"})"));
    tel::Loop loop;
    tel::Service svc("", [](const json::Value&) {});
    tel::SimModem sim(loop, svc.events(), *net);
    svc.attach(&sim, &sim);
    auto st = svc.handle("status", json::Object{});
    const json::Value* e = st->find("emergency_numbers");
    CHECK(e && e->is_array() && std::get<json::Array>(*e).size() == 1);
    CHECK(svc.handle("sim.lock", json::Object{{"pin", "1234"}}).has_value());
    auto call = svc.handle("dial", json::Object{{"number", "000"}});
    CHECK(call.has_value());
    auto calls = svc.handle("calls", json::Object{});
    CHECK(!std::get<json::Array>(*calls).empty() && std::get<json::Array>(*calls)[0].flag("emergency"));
    CHECK(tel::emergency_number("112") && !tel::emergency_number("000") && tel::emergency_number("000", {"000"}));
}

// The SIM's phone book: the simulated SIM's from sim_contacts, none while
// it is locked; and how a modem's AT answers read.
static void test_sim_contacts() {
    CHECK(!tel::SimNetwork::parse(R"({"sim_contacts":[{"name":"A","number":"12a"}]})"));
    CHECK(!tel::SimNetwork::parse(R"({"sim_contacts":[{"number":"10086"}]})"));
    auto net = tel::SimNetwork::parse(R"({"sim_contacts":[{"name":"\u5988\u5988","number":"13800138000"},{"name":"Office","number":"+861088886666"}]})");
    CHECK(net && net->contacts.size() == 2 && net->contacts[1].index == 2);
    CHECK(!network().contacts.empty());
    tel::Loop loop;
    tel::Service svc("", [](const json::Value&) {});
    tel::SimModem sim(loop, svc.events(), *net);
    svc.attach(&sim, &sim);
    auto book = svc.handle("phonebook.read", json::Object{});
    CHECK(book.has_value());
    if (book) {
        const json::Value* c = book->find("contacts");
        CHECK(c && c->is_array() && std::get<json::Array>(*c).size() == 2);
        if (c && c->is_array() && std::get<json::Array>(*c).size() == 2) {
            const auto& first = std::get<json::Array>(*c)[0];
            CHECK(first.str("name") == "\u5988\u5988" && first.str("number") == "13800138000" && first.find("index"));
        }
    }
    CHECK(svc.handle("sim.lock", json::Object{{"pin", "1234"}}).has_value());
    CHECK(!svc.handle("phonebook.read", json::Object{}).has_value());

    CHECK(tel::cpbr_range("\r\n+CPBR: (1-250),40,14\r\n\r\nOK") == std::pair(1, 250));
    CHECK(!tel::cpbr_range("+CPBR: (0-0),40,14") && !tel::cpbr_range("ERROR"));
    CHECK(tel::cscs_charset("+CSCS: \"GSM\"\r\n") == "GSM" && tel::cscs_charset("OK").empty());
    CHECK(tel::ucs2_decode("59885988") == "\u5988\u5988");
    CHECK(tel::ucs2_decode("D83DDE00") == "\U0001F600");
    CHECK(!tel::ucs2_decode("5988598") && !tel::ucs2_decode("Bob!") && !tel::ucs2_decode("D83D0041"));
    std::string ucs2 =
        "+CPBR: 1,\"13800138000\",129,\"59885988\"\r\n"
        "+CPBR: 2,\"00310030003000380036\",129,\"004F00660066006900630065\"\r\n"
        "+CPBR: 3,\"861088886666\",145,\"0042006F0062\"\r\n"
        "+CPBR: 4,\"\",129,\"\"\r\n"
        "OK\r\n";
    auto e = tel::parse_cpbr(ucs2, true);
    CHECK(e.size() == 3);
    if (e.size() == 3) {
        CHECK(e[0].index == 1 && e[0].name == "\u5988\u5988" && e[0].number == "13800138000");
        CHECK(e[1].name == "Office" && e[1].number == "10086");
        CHECK(e[2].name == "Bob" && e[2].number == "+861088886666");
    }
    auto gsm = tel::parse_cpbr("+CPBR: 7,\"10086\",129,\"Say \"\"hi\"\", Bob\"\r\nOK", false);
    CHECK(gsm.size() == 1 && gsm[0].index == 7 && gsm[0].name == "Say \"hi\", Bob");
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

// Calls another phone kept go into the log by their time (newest first
// stays), marked imported; the same call again and malformed ones are left
// out, the cap holds, and log.delete takes out only imported entries.
static void test_log_import() {
    std::string dir = temp_dir();
    std::vector<std::string> ids;
    {
        tel::Loop loop;
        tel::Service svc(dir, [](const json::Value&) {});
        tel::SimModem sim(loop, svc.events(), network());
        svc.attach(&sim, &sim);
        svc.handle("dial", json::Object{{"number", "13900000000"}});  // busy, now
        loop.advance(2000ms);
        auto entry = [](const char* number, const char* dir, const char* result, long long at, long long dur) {
            return json::Value(json::Object{{"number", number}, {"dir", dir}, {"result", result}, {"at", at}, {"duration", dur}});
        };
        json::Array in{entry("13655550101", "in", "answered", 1000, 61), entry("13655550102", "out", "outgoing", 3000, 5),
                       entry("13655550199", "in", "missed", 2000, 0), entry("13655550101", "in", "answered", 1000, 61),
                       entry("x", "in", "missed", 5, 0), entry("13655550103", "out", "missed", 5, 0), entry("13655550104", "in", "missed", 0, 0)};
        auto r = svc.handle("log.import", json::Object{{"entries", in}});
        CHECK(r.has_value());
        for (const auto& id : std::get<json::Array>(*r->find("ids"))) ids.push_back(std::get<std::string>(id));
        CHECK(ids.size() == 7 && !ids[0].empty() && !ids[1].empty() && !ids[2].empty() && ids[0] != ids[1]);
        CHECK(ids[3].empty() && ids[4].empty() && ids[5].empty() && ids[6].empty());
        auto log = std::get<json::Array>(*svc.handle("log", json::Object{}));
        CHECK(log.size() == 4 && log[0].str("result") == "busy" && !log[0].find("imported"));
        CHECK(log[1].num("at") == 3000 && log[2].num("at") == 2000 && log[3].num("at") == 1000);
        CHECK(log[3].flag("imported") && log[3].str("id") == ids[0] && log[3].num("duration") == 61);
        auto again = svc.handle("log.import", json::Object{{"entries", json::Array{in[0]}}});
        CHECK(std::get<std::string>(std::get<json::Array>(*again->find("ids"))[0]).empty());
        CHECK(!svc.handle("log.import", json::Object{{"entries", "no"}}).has_value());
    }
    {
        tel::Service svc(dir, [](const json::Value&) {});
        auto log = std::get<json::Array>(*svc.handle("log", json::Object{}));
        CHECK(log.size() == 4 && log[1].str("id") == ids[1]);
        // The numbering goes on after a restart.
        auto r = svc.handle("log.import", json::Object{{"entries", json::Array{json::Object{{"number", "13655550105"}, {"dir", "in"}, {"result", "rejected"}, {"at", 4000}, {"duration", 0}}}}});
        std::string fresh = std::get<std::string>(std::get<json::Array>(*r->find("ids"))[0]);
        CHECK(!fresh.empty() && fresh != ids[0] && fresh != ids[1] && fresh != ids[2]);
        auto del = svc.handle("log.delete", json::Object{{"ids", json::Array{ids[0], ids[1], ids[2], fresh, "nope"}}});
        CHECK(del.has_value() && del->num("deleted") == 4);
        log = std::get<json::Array>(*svc.handle("log", json::Object{}));
        CHECK(log.size() == 1 && log[0].str("result") == "busy");
    }
    {
        // The cap: 200 entries, the oldest go.
        tel::Service svc(dir, [](const json::Value&) {});
        json::Array many(300);
        for (int i = 0; i < 300; ++i) {
            json::Object e{{"number", "1365555" + std::to_string(1000 + i)}, {"dir", "in"}, {"result", "missed"}, {"at", 10 + i}, {"duration", 0}};
            many[i] = std::move(e);
        }
        auto r = svc.handle("log.import", json::Object{{"entries", many}});
        auto got = std::get<json::Array>(*r->find("ids"));
        CHECK(std::get<std::string>(got[0]).empty() && !std::get<std::string>(got[299]).empty());
        auto log = std::get<json::Array>(*svc.handle("log", json::Object{}));
        CHECK(log.size() == 200 && log[0].str("result") == "busy" && log[1].num("at") == 309 && log[199].num("at") == 111);
        many.resize(501, many[0]);
        CHECK(!svc.handle("log.import", json::Object{{"entries", many}}).has_value());
    }
    std::filesystem::remove_all(dir);
}

static void test_cell_broadcast() {
    tel::Loop loop;
    std::vector<json::Value> events;
    tel::Service svc("", [&](const json::Value& ev) { events.push_back(ev); });
    tel::SimModem sim(loop, svc.events(), network());
    svc.attach(&sim, &sim);
    events.clear();  // the status with the APN chosen
    CHECK(svc.handle("sim.cb", json::Object{{"channel", 4371}, {"serial", 12288}, {"text", "Flood warning"}}).has_value());
    CHECK(!svc.handle("sim.cb", json::Object{{"channel", 70000}, {"text", "x"}}).has_value());
    CHECK(!svc.handle("sim.cb", json::Object{{"channel", 4370}, {"text", ""}}).has_value());
    CHECK(events.size() == 1 && events[0].str("event") == "cb" && events[0].find("cb")->str("text") == "Flood warning");
    CHECK(events[0].find("cb")->num("serial") == 12288 && events[0].str("key") == events[0].find("cb")->str("id"));
    CHECK(!events[0].find("cb")->find("wac"));
    // An alert for an area: its warning area coordinates go to phone-smsd as they came.
    CHECK(svc.handle("sim.cb", json::Object{{"channel", 4371}, {"serial", 1}, {"text", "Here"}, {"wac", "0a003028AB"}}).has_value());
    CHECK(events.size() == 2 && events[1].find("cb")->str("wac") == "0a003028AB");
    CHECK(!svc.handle("sim.cb", json::Object{{"channel", 4371}, {"serial", 2}, {"text", "x"}, {"wac", "0g"}}).has_value());
}

// An 8-bit message (a WAP push) carries its user data in hex; a class 0
// one says so. Both are queued for phone-smsd like any message.
static void test_sms_data_and_class() {
    tel::Loop loop;
    std::vector<json::Value> events;
    tel::Service svc("", [&](const json::Value& ev) { events.push_back(ev); });
    tel::SimModem sim(loop, svc.events(), network());
    svc.attach(&sim, &sim);
    events.clear();  // the status with the APN chosen
    CHECK(svc.handle("sim.sms", json::Object{{"from", "1065800"}, {"data", "0006be8c82"}}).has_value());
    CHECK(svc.handle("sim.sms", json::Object{{"from", "10086"}, {"text", "Balance 3.20"}, {"class", 0}}).has_value());
    CHECK(svc.handle("sim.sms", json::Object{{"from", "10086"}, {"text", "plain"}}).has_value());
    CHECK(!svc.handle("sim.sms", json::Object{{"from", "1065800"}, {"data", "abc"}}).has_value());
    CHECK(!svc.handle("sim.sms", json::Object{{"from", "10086"}, {"text", "x"}, {"class", 7}}).has_value());
    CHECK(events.size() == 3);
    const json::Value* data = events[0].find("sms");
    CHECK(data->str("data") == "0006be8c82" && data->str("text").empty() && !data->find("class"));
    CHECK(events[1].find("sms")->num("class") == 0 && !events[1].find("sms")->find("data"));
    CHECK(!events[2].find("sms")->find("class") && !events[2].find("sms")->find("data"));
    CHECK(std::get<json::Array>(*svc.handle("sms.pending", json::Object{})).size() == 3);
    CHECK(tel::to_hex(std::string("\x00\xbe\x0f", 3)) == "00be0f");
    CHECK(tel::valid_hex("00AbFf", 3) && !tel::valid_hex("00AbFf", 2) && !tel::valid_hex("0", 8) && tel::valid_hex("", 0));
}

// A call the shell declines for the block list is logged as blocked.
static void test_blocked_call() {
    tel::Loop loop;
    tel::Service svc("", [](const json::Value&) {});
    tel::SimModem sim(loop, svc.events(), network());
    svc.attach(&sim, &sim);
    auto r = svc.handle("sim.ring", json::Object{{"number", "13700000001"}});
    CHECK(r.has_value());
    CHECK(svc.handle("hangup", json::Object{{"call_id", r->str("call_id")}, {"blocked", true}}).has_value());
    r = svc.handle("sim.ring", json::Object{{"number", "13700000002"}});
    CHECK(svc.handle("hangup", json::Object{{"call_id", r->str("call_id")}}).has_value());
    auto log = std::get<json::Array>(*svc.handle("log", json::Object{}));
    CHECK(log.size() == 2 && log[0].str("result") == "rejected" && log[1].str("result") == "blocked" && log[1].str("number") == "13700000001");
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
    // An emergency call goes out with the SIM locked (3GPP TS 22.101).
    auto sos = svc.handle("dial", json::Object{{"number", "112"}});
    CHECK(sos.has_value());
    if (sos) svc.handle("hangup", json::Object{{"call_id", sos->str("call_id")}});
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
        auto status = st();
        const json::Value* ifaces = status.find("data_ifaces");
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
    events.clear();  // the status with the APN chosen
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

// The carriers' APNs (apns.json): chosen by MCC and MNC, a virtual
// operator's entry by the SIM's SPN or a GID1 prefix; the database refuses
// malformed entries.
static const tel::ApnDb& apn_db() {
    static const tel::ApnDb db = [] {
        auto d = tel::ApnDb::load(TEL_APNS);
        if (!d) {
            std::println(stderr, "{}", d.error());
            std::exit(EXIT_FAILURE);
        }
        return *d;
    }();
    return db;
}

static void test_apn_db() {
    const auto& db = apn_db();
    tel::ApnStore store;
    auto internet = [&](tel::SimId id) {
        auto l = store.list(db, id);
        return l.internet() ? l.internet()->apn : std::string("-");
    };
    for (const char* mnc : {"00", "02", "04", "07", "08", "13"}) CHECK(internet({"460", mnc}) == "cmnet");
    for (const char* mnc : {"01", "06", "09"}) CHECK(internet({"460", mnc}) == "3gnet");
    for (const char* mnc : {"03", "05", "11"}) CHECK(internet({"460", mnc}) == "ctnet");
    CHECK(internet({"460", "15"}) == "cbnet");
    CHECK(internet({"460", "20"}) == "-" && internet({"262", "02"}) == "-" && internet({"460", ""}) == "-");

    auto cm = store.list(db, {"460", "00"});
    CHECK(cm.carrier == "\u4e2d\u56fd\u79fb\u52a8" && cm.apns.size() == 3 && cm.selected == "cmnet" && !cm.edited);
    CHECK(cm.internet()->ip_type == "ipv4v6" && cm.internet()->has("ia") && cm.internet()->auth == "none");
    const tel::Apn& mms = cm.apns[1];
    CHECK(mms.apn == "cmwap" && mms.has("mms") && !mms.has("default") && mms.mmsc == "http://mmsc.monternet.com" && mms.mms_port == 80);
    CHECK(cm.apns[2].apn == "ims" && cm.apns[2].types == std::vector<std::string>{"ims"});

    // China Telecom's SIMs on 460-01 name themselves in their SPN.
    for (const char* spn : {"China Telecom", "CHINA TELECOM"}) {
        auto ct = store.list(db, {"460", "01", spn});
        CHECK(ct.internet() && ct.internet()->apn == "ctnet" && ct.internet()->auth == "pap-chap" && ct.internet()->user == "ctnet@mycdma.cn");
    }
    CHECK(internet({"460", "01", "\u4e2d\u56fd\u8054\u901a"}) == "3gnet");

    auto mvno = tel::ApnDb::parse(R"({"carriers":[
        {"name":"Host","mcc":"001","mnc":["01"],"apns":[{"id":"net","name":"Host","apn":"host","types":["default"]}]},
        {"name":"Virtual","mcc":"001","mnc":["01"],"gid1":"4C","apns":[{"id":"net","name":"V","apn":"virtual","types":["default","mms"],"ip_type":"ipv4"}]}]})");
    CHECK(mvno.has_value());
    if (mvno) {
        auto l = store.list(*mvno, {"001", "01", "", "4c01ff"});
        CHECK(l.carrier == "Virtual" && l.internet()->apn == "virtual" && l.internet()->ip_type == "ipv4");
        CHECK(store.list(*mvno, {"001", "01", "", "4d"}).internet()->apn == "host");
        CHECK(store.list(*mvno, {"001", "01"}).internet()->apn == "host");
    }
    for (const char* bad : {
             R"({"carriers":[{"name":"X","mcc":"001","mnc":["01"],"apns":[{"id":"a","name":"A","apn":"cm net","types":["default"]}]}]})",
             R"({"carriers":[{"name":"X","mcc":"001","mnc":["01"],"apns":[{"id":"a","name":"A","apn":"a","types":[]}]}]})",
             R"({"carriers":[{"name":"X","mcc":"001","mnc":["01"],"apns":[{"id":"a","name":"A","apn":"a","types":["web"]}]}]})",
             R"({"carriers":[{"name":"X","mcc":"001","mnc":["01"],"apns":[{"id":"a","name":"A","apn":"a","types":["default"]},{"id":"a","name":"B","apn":"b","types":["mms"]}]}]})",
             R"({"carriers":[{"name":"X","mcc":"001","mnc":["01"],"apns":[{"id":"a","name":"A","apn":"a","types":["default"],"auth":"pap"}]}]})",
             R"({"carriers":[{"name":"X","mcc":"001","mnc":["1"],"apns":[{"id":"a","name":"A","apn":"a","types":["default"]}]}]})",
             R"({"carriers":[{"name":"X","mcc":"001","mnc":["01"],"apns":[{"id":"a","name":"A","apn":"a","types":["default"],"proxy":"x"}]}]})",
             R"({"carriers":[{"name":"X","mcc":"001","mnc":["01"],"apns":[{"id":"a","name":"A","apn":"a.","types":["default"]}]}]})",
             R"({"carriers":[{"name":"X","mcc":"001","mnc":["01"],"apns":[{"id":"a","name":"A","apn":"a","types":["default"],"ip_type":"ip"}]}]})",
             R"({"carriers":[],"extra":1})", R"([])"})
        CHECK(!tel::ApnDb::parse(bad).has_value());
    CHECK(!tel::ApnDb::load("/nonexistent/apns.json").has_value());

    tel::Apn with_password = *cm.internet();
    with_password.password = "secret";
    auto j = with_password.to_json();
    CHECK(!j.find("password") && j.flag("password_set") && with_password.to_json(true).str("password") == "secret");
}

// The user's APNs: a change copies the carrier's list, kept per carrier
// (0600: passwords) across restarts; reset drops the copy.
static void test_apn_store() {
    const auto& db = apn_db();
    std::string dir = temp_dir(), path = dir + "/apns.json";
    const tel::SimId cm{"460", "00"};
    auto apn = [](std::string_view json) { return *tel::Apn::from_json(*json::parse(json)); };
    {
        tel::ApnStore store(path);
        CHECK(!store.select(db, cm, "cmwap"));  // not an internet APN
        CHECK(!store.select(db, cm, "nope"));
        auto id = store.save(db, cm, apn(R"({"name":"Office","apn":"corp.example","types":["default"],"auth":"chap","user":"me","password":"pw"})"), true);
        CHECK(id && *id == "u1");
        auto l = store.list(db, cm);
        CHECK(l.edited && l.apns.size() == 4 && l.apns[3].edited && !l.apns[0].edited && l.selected == "cmnet");
        CHECK(store.select(db, cm, "u1").has_value());
        CHECK(store.list(db, cm).internet()->apn == "corp.example");
        // Changed without a password: the old one stays.
        CHECK(store.save(db, cm, apn(R"({"id":"u1","name":"Office","apn":"corp2.example","types":["default"],"auth":"chap","user":"me"})"), false));
        CHECK(store.list(db, cm).internet()->password == "pw");
        CHECK(store.save(db, cm, apn(R"({"id":"cmnet","name":"CMNET","apn":"cmnet","types":["default"],"ip_type":"ipv4"})"), false));
        CHECK(!store.save(db, cm, apn(R"({"id":"zz","name":"Z","apn":"z","types":["default"]})"), false));
        CHECK(store.remove(db, cm, "cmwap").has_value() && !store.remove(db, cm, "cmwap"));
        CHECK(!store.save(db, {"460", ""}, apn(R"({"name":"Z","apn":"z","types":["default"]})"), false));
        CHECK(!store.reset({}));
    }
    auto perms = std::filesystem::status(path).permissions();
    CHECK((perms & (std::filesystem::perms::group_all | std::filesystem::perms::others_all)) == std::filesystem::perms::none);
    {
        tel::ApnStore store(path);
        auto l = store.list(db, cm);
        CHECK(l.apns.size() == 3 && l.selected == "u1" && l.internet()->password == "pw" && l.internet()->apn == "corp2.example");
        CHECK(l.apns[0].ip_type == "ipv4" && l.apns[0].edited);
        CHECK(store.list(db, {"460", "01"}).internet()->apn == "3gnet");  // another carrier's untouched
        CHECK(store.remove(db, cm, "u1").has_value() && store.list(db, cm).selected == "cmnet");
        CHECK(store.save(db, cm, apn(R"({"name":"New","apn":"new","types":["mms"]})"), false) == "u2");
        CHECK(store.reset(cm).has_value());
        l = store.list(db, cm);
        CHECK(!l.edited && l.apns.size() == 3 && l.selected == "cmnet" && l.internet()->ip_type == "ipv4v6");
        // New APNs up to tel::kMaxApns a SIM, the carrier's counted; then
        // none, but a change still goes, and a delete makes room.
        size_t added = 0;
        while (store.save(db, cm, apn(R"({"name":"N","apn":"n","types":["default"]})"), false) && added <= tel::kMaxApns) added++;
        CHECK(added == tel::kMaxApns - 3 && store.list(db, cm).apns.size() == tel::kMaxApns);
        auto full = store.save(db, cm, apn(R"({"name":"N","apn":"n","types":["default"]})"), false);
        CHECK(!full && full.error() == tel::refused::kFull);
        CHECK(store.save(db, cm, apn(R"({"id":"cmnet","name":"CMNET","apn":"cmnet2","types":["default"]})"), false));
        CHECK(store.remove(db, cm, "cmwap").has_value());
        CHECK(store.save(db, cm, apn(R"({"name":"N","apn":"n","types":["default"]})"), false));
        CHECK(store.reset(cm).has_value());
    }
    for (const char* bad : {R"({"name":"","apn":"a","types":["default"]})", R"({"name":"A","apn":"","types":["default"]})",
                            R"({"name":"A","apn":"a","types":["default"],"mms_port":70000})", R"({"name":"A","apn":"a","types":["default"],"user":"a\nb"})",
                            R"({"name":"A","apn":"a","types":["default","default"]})", R"("cmnet")"})
        CHECK(!tel::Apn::from_json(*json::parse(bad)));
    std::filesystem::remove_all(dir);
}

// apn.set's refusals end in the words Settings translates (tel::refused,
// ui.ErrAPN*): each member's limit, the SIM's network unknown, an APN gone,
// one not for the internet, a full list. A refused change leaves the
// carrier's list as it was.
static void test_apn_refusals() {
    namespace r = tel::refused;
    auto why = [](const auto& e) { return e ? std::string("done") : e.error(); };
    auto with = [&](const std::string& member, std::string value) {
        json::Object o{{"name", "A"}, {"apn", "a"}, {"types", json::Array{"default"}}};
        o[member] = std::move(value);
        return why(tel::Apn::from_json(o));
    };
    // Labels of nine letters, dots between: a valid APN up to its limit.
    auto text = [](size_t n) {
        std::string s(n, 'a');
        for (size_t i = 9; i + 1 < n; i += 10) s[i] = '.';
        return s;
    };
    for (auto [member, most, longer] : {std::tuple{"name", 64, r::kNameLong}, {"apn", 100, r::kBadApn}, {"user", 64, r::kUserLong},
                                        {"password", 64, r::kPasswordLong}, {"mmsc", 256, r::kMmscLong}, {"mms_proxy", 255, r::kMmsProxyLong}}) {
        CHECK(with(member, text(most)) == "done");
        CHECK(with(member, text(most + 1)) == longer);
        CHECK(with(member, "a\tb") == std::format("{}: {}", member, r::kControl));
    }
    CHECK(with("name", "") == r::kNoName && with("apn", "cm net") == r::kBadApn && with("apn", "a..b") == r::kBadApn);
    CHECK(with("auth", "chap") == r::kNeedsUser);

    const auto& db = apn_db();
    const tel::SimId cm{"460", "00"};
    tel::ApnStore store;
    tel::Apn net{.name = "N", .apn = "n", .types = {"default"}};
    CHECK(why(store.save(db, {"460", ""}, net, false)) == r::kSimUnknown && why(store.remove(db, {}, "cmnet")) == r::kSimUnknown);
    CHECK(why(store.select(db, {}, "cmnet")) == r::kSimUnknown && why(store.reset({})) == r::kSimUnknown);
    tel::Apn gone = net;
    gone.id = "zz";
    CHECK(why(store.save(db, cm, gone, false)) == r::kGone && why(store.remove(db, cm, "zz")) == r::kGone && why(store.select(db, cm, "zz")) == r::kGone);
    CHECK(!store.list(db, cm).edited);
    CHECK(why(store.select(db, cm, "cmwap")) == r::kNotInternet);
    CHECK(std::string_view(r::kFull).contains(std::format(" {} ", tel::kMaxApns)));

    tel::Loop loop;
    tel::Service svc("", [](const json::Value&) {}, db);
    tel::SimModem sim(loop, svc.events(), network());
    svc.attach(&sim, &sim);
    auto set = [&](json::Object p) { return why(svc.handle("apn.set", p)); };
    CHECK(set({{"op", "save"}, {"apn", json::Object{{"name", std::string(65, 'n')}, {"apn", "n"}, {"types", json::Array{"default"}}}}}) == r::kNameLong);
    CHECK(set({{"op", "save"}, {"apn", json::Object{{"name", "N"}, {"apn", "n"}, {"types", json::Array{"default"}}, {"user", "a\nb"}}}}) ==
          std::format("user: {}", r::kControl));
    CHECK(set({{"op", "select"}, {"id", "ims"}}) == r::kNotInternet && set({{"op", "delete"}, {"id", "zz"}}) == r::kGone);
    sim.lock("1234");
    CHECK(set({{"op", "reset"}}) == r::kSimUnknown);
}

// A state file cut short (a power cut, a full disk) is read from its copy;
// with neither whole the service still starts, from the defaults, but with
// the radio off: airplane mode must not give way to a transmitting radio
// before the Shell sets the switches again. Neither file there is a first
// start, the radio on.
static void test_damaged_state() {
    std::string dir = temp_dir();
    auto path = [&](const char* name) { return (std::filesystem::path(dir) / name).string(); };
    auto halve = [&](const char* name) { std::filesystem::resize_file(path(name), std::filesystem::file_size(path(name)) / 2); };
    auto spoil = [&](const char* name) {
        std::ofstream(path(name)) << "{\"radio\":";
        std::ofstream(path(name) + ".bak") << "{\"log\":[";
    };
    struct Seen {
        bool radio, data;
        size_t pending;
    };
    auto start = [&](auto&& act) {
        tel::Loop loop;
        tel::Service svc(dir, [](const json::Value&) {});
        tel::SimModem sim(loop, svc.events(), network());
        svc.attach(&sim, &sim);
        act(svc);
        auto st = *svc.handle("status", json::Object{});
        return Seen{st.flag("radio", true), st.flag("data", true), std::get<json::Array>(*svc.handle("sms.pending", json::Object{})).size()};
    };
    auto seen = start([](tel::Service& svc) {
        CHECK(svc.handle("data.set", json::Object{{"on", false}}).has_value());
        CHECK(svc.handle("radio.set", json::Object{{"on", false}}).has_value());
        svc.events().sms(tel::Sms{.id = "m1", .peer = "10086", .dir = "in", .text = "kept", .at = 6});
    });
    CHECK(!seen.radio && !seen.data && seen.pending == 1);

    halve("radio.json");
    halve("telephony.json");
    seen = start([](tel::Service&) {});
    CHECK(!seen.radio && !seen.data && seen.pending == 1);
    std::stringstream radio;
    radio << std::ifstream(path("radio.json")).rdbuf();
    CHECK(json::parse(radio.str()).has_value());

    spoil("radio.json");
    spoil("telephony.json");
    seen = start([](tel::Service&) {});
    CHECK(!seen.radio && seen.pending == 0);
    seen = start([](tel::Service& svc) { CHECK(svc.handle("radio.set", json::Object{{"on", true}}).has_value()); });
    CHECK(seen.radio);

    for (const char* f : {"radio.json", "radio.json.bak", "telephony.json", "telephony.json.bak"}) std::filesystem::remove(path(f));
    CHECK(start([](tel::Service&) {}).radio);

    const auto& db = apn_db();
    const tel::SimId cm{"460", "00"};
    {
        tel::ApnStore store(path("apns.json"));
        CHECK(store.save(db, cm, tel::Apn{.name = "Office", .apn = "corp.example", .types = {"default"}, .password = "pw"}, true) == "u1");
        CHECK(store.select(db, cm, "u1").has_value());
    }
    auto perms = std::filesystem::status(path("apns.json.bak")).permissions();
    CHECK((perms & (std::filesystem::perms::group_all | std::filesystem::perms::others_all)) == std::filesystem::perms::none);
    halve("apns.json");
    {
        auto l = tel::ApnStore(path("apns.json")).list(db, cm);
        CHECK(l.edited && l.selected == "u1" && l.internet() && l.internet()->password == "pw");
    }
    spoil("apns.json");
    {
        auto l = tel::ApnStore(path("apns.json")).list(db, cm);
        CHECK(!l.edited && l.selected == "cmnet");
    }
    std::filesystem::remove_all(dir);
}

// An apns.json with more than kMaxApns APNs for a SIM (an older
// telephonyd's, one written by hand) is read back whole: the internet APN
// in use, the 40th, stays in use. Adding is refused until deletes bring
// the list under the limit; changes go all along.
static void test_apn_overfull() {
    const auto& db = apn_db();
    const tel::SimId cm{"460", "00"};
    std::string dir = temp_dir(), path = dir + "/apns.json";
    json::Array apns;
    for (int i = 1; i <= 40; i++)
        apns.push_back(json::Object{{"id", std::format("u{}", i)}, {"name", std::format("N{}", i)}, {"apn", std::format("n{}", i)}, {"types", json::Array{"default"}}, {"edited", true}});
    std::ofstream(path) << json::dump(json::Object{{"carriers", json::Object{{"46000", json::Object{{"selected", "u40"}, {"next", 41}, {"apns", std::move(apns)}}}}}});
    tel::ApnStore store(path);
    auto l = store.list(db, cm);
    CHECK(l.apns.size() == 40 && l.edited && l.selected == "u40" && l.internet() && l.internet()->apn == "n40" && l.plan().internet.apn == "n40");
    auto add = [&] {
        auto id = store.save(db, cm, tel::Apn{.name = "New", .apn = "new", .types = {"default"}}, false);
        return id ? *id : id.error();
    };
    CHECK(add() == tel::refused::kFull);
    CHECK(store.save(db, cm, tel::Apn{.id = "u40", .name = "N40", .apn = "n40.new", .types = {"default"}}, false).has_value());
    for (int i = 1; i <= 8; i++) CHECK(store.remove(db, cm, std::format("u{}", i)).has_value());
    CHECK(store.list(db, cm).apns.size() == tel::kMaxApns && add() == tel::refused::kFull);
    CHECK(store.remove(db, cm, "u9").has_value() && add() == "u41");
    l = tel::ApnStore(path).list(db, cm);
    CHECK(l.apns.size() == tel::kMaxApns && l.selected == "u40" && l.internet()->apn == "n40.new");
    std::filesystem::remove_all(dir);
}

// The service gives the modem the SIM's APN; the status shows it with the
// link's addressing for its IP type; Settings' apn.* change it.
static void test_apn_service() {
    tel::Loop loop;
    tel::Service svc("", [](const json::Value&) {}, apn_db());
    tel::SimModem sim(loop, svc.events(), network());
    svc.attach(&sim, &sim);
    auto st = [&] { return *svc.handle("status", json::Object{}); };
    auto s = st();
    CHECK(s.str("data_state") == "connected" && s.str("data_apn") == "cmnet" && s.str("sim_mnc") == "00" && s.str("roaming").empty());
    const json::Value* ip = s.find("data_ip");
    CHECK(ip && ip->str("iface") == tel::kSimDataIface && ip->num("mtu") == 1400);
    if (ip) {
        CHECK(ip->find("ipv4") && ip->find("ipv4")->str("address") == "100.80.12.34" && ip->find("ipv4")->num("prefix") == 30);
        CHECK(ip->find("ipv4")->str("gateway") == "100.80.12.33" && ip->find("ipv4")->str("method") == "static");
        CHECK(ip->find("ipv6") && ip->find("ipv6")->num("prefix") == 64);
    }

    auto list = svc.handle("apn.list", json::Object{});
    CHECK(list && list->str("carrier") == "\u4e2d\u56fd\u79fb\u52a8" && list->str("selected") == "cmnet" && list->str("mcc") == "460" && list->str("mnc") == "00");
    CHECK(list && std::get<json::Array>(*list->find("apns")).size() == 3 && !std::get<json::Array>(*list->find("apns"))[0].find("password"));
    auto mms = svc.handle("apn.mms", json::Object{});
    CHECK(mms && mms->str("apn") == "cmwap" && mms->str("mmsc") == "http://mmsc.monternet.com" && mms->num("mms_port") == 80 && !mms->find("password"));

    json::Object cmnet{{"id", "cmnet"}, {"name", "CMNET"}, {"apn", "cmnet.test"}, {"types", json::Array{"default"}}, {"ip_type", "ipv4"}};
    auto saved = svc.handle("apn.set", json::Object{{"op", "save"}, {"apn", cmnet}});
    CHECK(saved && saved->flag("edited") && saved->str("selected") == "cmnet");
    s = st();
    CHECK(s.str("data_apn") == "cmnet.test" && s.find("data_ip") && !s.find("data_ip")->find("ipv6"));
    CHECK(svc.handle("apn.set", json::Object{{"op", "reset"}}).has_value() && st().str("data_apn") == "cmnet");
    CHECK(!svc.handle("apn.set", json::Object{{"op", "save"}}) && !svc.handle("apn.set", json::Object{{"op", "rename"}}));
    CHECK(!svc.handle("apn.set", json::Object{{"op", "select"}, {"id", "ims"}}));

    // Another SIM: a virtual operator's.
    CHECK(svc.handle("sim.network", json::Object{{"sim_mcc", "460"}, {"net_mcc", "460"}, {"sim_mnc", "01"}, {"spn", "China Telecom"}}).has_value());
    CHECK(st().str("data_apn") == "ctnet" && svc.handle("apn.list", json::Object{})->str("spn") == "China Telecom");
    CHECK(svc.handle("sim.network", json::Object{{"sim_mcc", "460"}, {"net_mcc", "460"}, {"sim_mnc", "00"}, {"spn", ""}}).has_value());
    CHECK(st().str("data_apn") == "cmnet");
    // A SIM the database lacks: the network's default APN.
    CHECK(svc.handle("sim.network", json::Object{{"sim_mcc", "334"}, {"net_mcc", "334"}}).has_value());
    s = st();
    CHECK(s.str("data_apn").empty() && s.str("data_state") == "connected" && svc.handle("apn.list", json::Object{})->str("carrier").empty());
    CHECK(!svc.handle("apn.mms", json::Object{}));
    CHECK(!svc.handle("sim.network", json::Object{{"sim_mcc", "460"}, {"sim_mnc", "1"}}));
    // Locked, the SIM's APNs are unknown.
    sim.lock("1234");
    CHECK(std::get<json::Array>(*svc.handle("apn.list", json::Object{})->find("apns")).empty());
    CHECK(!svc.handle("apn.set", json::Object{{"op", "reset"}}));
}

// What the modem gets of the SIM's APNs: the internet APN, the attach APN
// (ia), the MMS APN when it is an access point of its own, the IMS ones.
static void test_apn_plan() {
    tel::ApnStore store;
    auto cmcc = store.list(apn_db(), tel::SimId{"460", "00", "", ""}).plan();
    CHECK(cmcc.internet.id == "cmnet" && cmcc.attach && cmcc.attach->id == "cmnet");
    CHECK(cmcc.mms && cmcc.mms->apn == "cmwap" && cmcc.ims == std::vector<std::string>{"ims"});
    tel::Apn internet{.id = "net", .name = "Net", .apn = "Internet", .types = {"default", "supl"}};
    tel::Apn attach{.id = "lte", .name = "LTE", .apn = "lte.attach", .types = {"ia"}, .ip_type = "ipv6"};
    tel::Apn mms{.id = "mms", .name = "MMS", .apn = "internet", .types = {"mms"}, .mmsc = "http://mmsc.example"};
    tel::Apn ims{.id = "ims", .name = "IMS", .apn = "IMS.Example", .types = {"ims"}};
    tel::Apn sos{.id = "sos", .name = "SOS", .apn = "sos", .types = {"emergency"}};
    tel::ApnList l{.apns = {internet, attach, mms, ims, sos}, .selected = "net"};
    auto p = l.plan();
    // The MMS APN by another case is the same access point: MMS goes over
    // the data link.
    CHECK(p.internet.id == "net" && p.attach && p.attach->id == "lte" && !p.mms);
    CHECK(p.ims == (std::vector<std::string>{"ims.example", "sos"}));
    l.apns[2].apn = "mms.example";
    CHECK(l.plan().mms && l.plan().mms->id == "mms");
    // An MMS APN without an MMSC is none phone-smsd can use.
    l.apns[2].mmsc.clear();
    CHECK(!l.plan().mms);
    // The selected internet APN is the attach one when it is of type ia;
    // no ia APN leaves the modem's own.
    l.apns[0].types.push_back("ia");
    CHECK(l.plan().attach && l.plan().attach->id == "net");
    l.apns[0].types.pop_back();
    l.apns.erase(l.apns.begin() + 1);
    CHECK(!l.plan().attach);
    // The network's default APN (no carrier): nothing more.
    auto none = tel::ApnList{}.plan();
    CHECK(none.internet.apn.empty() && !none.attach && !none.mms && none.ims.empty());
}

// The bearers others bring up: 81voltd's IMS bearer has an APN and an IP
// type but no apn-type; the LTE attach's is BearerType default-attach.
static void test_bearer_roles() {
    using tel::mm::BearerRole;
    using tel::mm::Dict;
    auto plan = tel::ApnStore().list(apn_db(), tel::SimId{"460", "00", "", ""}).plan();
    auto role = [&](Dict props, long long type = 1) { return tel::mm::bearer_role(props, type, plan); };
    CHECK(role(Dict{{"apn", std::string("ims")}, {"ip-type", 1LL}, {"profile-id", 2LL}}) == BearerRole::Ims);
    CHECK(role(Dict{{"apn", std::string("IMS")}}) == BearerRole::Ims && role(Dict{{"apn", std::string("sos")}}) == BearerRole::Ims);
    CHECK(role(Dict{{"apn", std::string("cmnet")}, {"apn-type", 4LL}}) == BearerRole::Ims);
    CHECK(role(Dict{{"apn", std::string("cmnet")}, {"apn-type", 64LL}}) == BearerRole::Ims);
    CHECK(role(Dict{{"apn", std::string("ims")}, {"apn-type", 6LL}}) == BearerRole::Ims);
    CHECK(role(Dict{{"apn", std::string("cmnet")}, {"apn-type", 2LL}}) == BearerRole::Data);
    CHECK(role(Dict{{"apn", std::string("cmnet")}}) == BearerRole::Data && role(Dict{}) == BearerRole::Data);
    CHECK(role(Dict{{"apn", std::string("cmnet.mnc000.mcc460.gprs")}}) == BearerRole::Data);
    // A bearer with another APN and no type is the data link going (another
    // APN chosen), as before.
    CHECK(role(Dict{{"apn", std::string("cmnet.test")}}) == BearerRole::Data);
    CHECK(role(Dict{{"apn", std::string("cmwap")}}) == BearerRole::Mms && role(Dict{{"apn", std::string("x")}, {"apn-type", 8LL}}) == BearerRole::Mms);
    CHECK(role(Dict{{"apn", std::string("cmnet")}, {"apn-type", 1LL}}) == BearerRole::Other);
    CHECK(role(Dict{{"apn", std::string("cmnet")}}, tel::mm::BearerDefaultAttach) == BearerRole::Attach);
    // A profile the modem chose, with no APN or an unknown one: left alone;
    // with the internet APN it is the data link.
    CHECK(role(Dict{{"apn", std::string("")}, {"ip-type", 2LL}, {"profile-id", 3LL}}) == BearerRole::Other);
    CHECK(role(Dict{{"apn", std::string("ims.carrier")}, {"profile-id", 3LL}}) == BearerRole::Other);
    CHECK(role(Dict{{"apn", std::string("cmnet")}, {"profile-id", 1LL}}) == BearerRole::Data);
    // Without the carrier's APNs the well-known names still tell.
    CHECK(tel::mm::bearer_role(Dict{{"apn", std::string("ims")}}, 0, tel::ApnPlan{}) == BearerRole::Ims);
    CHECK(tel::mm::bearer_role(Dict{{"apn", std::string("cmwap")}}, 0, tel::ApnPlan{}) == BearerRole::Data);
    // An internet APN named like an IMS one stays the data link.
    tel::ApnPlan odd{.internet = tel::Apn{.apn = "ims"}};
    CHECK(tel::mm::bearer_role(Dict{{"apn", std::string("ims")}}, 0, odd) == BearerRole::Data);

    CHECK(tel::mm::apn_named("cmnet", "CMNET") && tel::mm::apn_named("cmnet", "cmnet.MNC000.MCC460.GPRS"));
    CHECK(!tel::mm::apn_named("cmnet", "cmnet2") && !tel::mm::apn_named("cmnet", "") && tel::mm::apn_named("", ""));
    const tel::Apn& ia = *plan.attach;
    CHECK(!tel::mm::eps_matches(Dict{}, ia) && !tel::mm::eps_matches(Dict{{"apn", std::string("")}, {"ip-type", 4LL}}, ia));
    CHECK(tel::mm::eps_matches(Dict{{"apn", std::string("cmnet")}, {"ip-type", 4LL}}, ia) && tel::mm::eps_matches(Dict{{"apn", std::string("CMNET")}}, ia));
    CHECK(!tel::mm::eps_matches(Dict{{"apn", std::string("cmnet")}, {"ip-type", 1LL}}, ia));
    CHECK(!tel::mm::eps_matches(Dict{{"apn", std::string("cmnet")}, {"allowed-auth", 4LL}, {"user", std::string("u")}}, ia));
    tel::Apn chap = ia;
    chap.auth = "chap", chap.user = "u", chap.password = "p";
    CHECK(tel::mm::eps_matches(Dict{{"apn", std::string("cmnet")}, {"allowed-auth", 4LL}, {"user", std::string("u")}}, chap));
    CHECK(!tel::mm::eps_matches(Dict{{"apn", std::string("cmnet")}}, chap));
}

// mms.connect on the simulated baseband: MMS goes over its data link.
static void test_mms_connect() {
    tel::Loop loop;
    tel::Service svc("", [](const json::Value&) {}, apn_db());
    tel::SimModem sim(loop, svc.events(), network());
    svc.attach(&sim, &sim);
    auto r = svc.handle("mms.connect", json::Object{});
    CHECK(r && r->flag("shared") && !r->find("iface"));
    CHECK(svc.handle("mms.disconnect", json::Object{}).has_value());
    CHECK(!svc.handle("status", json::Object{})->find("mms_link"));
}

// Data roaming (off by default, kept across restarts): no data link on
// another country's network until it is on; national roaming has a switch
// of its own, on by default. roaming_kind's and roaming_allowed's rules.
static void test_data_roaming() {
    CHECK(tel::roaming_kind(false, "460", "262").empty() && tel::roaming_kind(true, "460", "262") == "international");
    CHECK(tel::roaming_kind(true, "460", "460") == "national" && tel::roaming_kind(true, "460", "") == "international");
    CHECK(tel::roaming_kind(true, "460", "454") == "international" && tel::roaming_kind(true, "310", "311") == "national");
    CHECK(tel::same_country("404", "405") && tel::same_country("234", "235") && !tel::same_country("440", "450") && !tel::same_country("", ""));
    CHECK(tel::roaming_allowed("", false, false) && tel::roaming_allowed("national", false, true) && !tel::roaming_allowed("national", true, false));
    CHECK(!tel::roaming_allowed("international", false, true) && tel::roaming_allowed("international", true, false));
    std::string dir = temp_dir();
    {
        tel::Loop loop;
        std::vector<json::Value> events;
        tel::Service svc(dir, [&](const json::Value& ev) { events.push_back(ev); }, apn_db());
        tel::SimModem sim(loop, svc.events(), network());
        svc.attach(&sim, &sim);
        auto st = [&] { return *svc.handle("status", json::Object{}); };
        CHECK(!st().flag("data_roaming", true) && st().str("data_state") == "connected");
        CHECK(svc.handle("sim.roam", json::Object{{"net_mcc", "262"}, {"net_mnc", "02"}, {"operator", "Vodafone.de"}}).has_value());
        auto s = st();
        CHECK(s.str("roaming") == "international" && s.str("operator") == "Vodafone.de" && s.str("net_mcc") == "262" && s.str("net_mnc") == "02");
        CHECK(s.str("data_state") == "roaming" && !s.find("data_ip") && s.flag("registered"));
        CHECK(!events.empty() && events.back().find("status")->str("data_state") == "roaming");
        CHECK(svc.handle("roaming.set", json::Object{{"on", true}}).has_value());
        CHECK(st().str("data_state") == "connected" && st().flag("data_roaming") && st().find("data_ip"));
        CHECK(svc.handle("roaming.set", json::Object{{"on", false}}).has_value() && st().str("data_state") == "roaming");
        CHECK(svc.handle("sim.roam", json::Object{{"net_mcc", "460"}, {"net_mnc", "01"}}).has_value());
        s = st();
        CHECK(s.str("roaming") == "national" && s.str("data_state") == "connected" && s.str("operator") == "46001" && s.flag("national_roaming"));
        // National roaming off: no link on another network of the SIM's
        // country, whatever data roaming says; roaming.set without it
        // keeps it.
        CHECK(svc.handle("roaming.set", json::Object{{"on", true}, {"national", false}}).has_value());
        s = st();
        CHECK(s.str("data_state") == "roaming" && !s.find("data_ip") && !s.flag("national_roaming", true) && s.flag("data_roaming"));
        CHECK(svc.handle("roaming.set", json::Object{{"on", false}}).has_value() && st().str("data_state") == "roaming");
        CHECK(svc.handle("sim.roam", json::Object{{"net_mcc", "262"}, {"net_mnc", "02"}}).has_value() && st().str("data_state") == "roaming");
        CHECK(svc.handle("roaming.set", json::Object{{"on", true}}).has_value() && st().str("data_state") == "connected");
        CHECK(svc.handle("sim.roam", json::Object{}).has_value());
        s = st();
        CHECK(s.str("roaming").empty() && s.str("operator") == network().op && s.str("net_mnc") == "00" && s.str("data_state") == "connected");
        CHECK(!svc.handle("sim.roam", json::Object{{"net_mcc", "262"}}) && !svc.handle("sim.roam", json::Object{{"net_mcc", "26"}, {"net_mnc", "02"}}));
    }
    {
        tel::Loop loop;
        tel::Service svc(dir, [](const json::Value&) {}, apn_db());
        tel::SimModem sim(loop, svc.events(), network());
        svc.attach(&sim, &sim);
        auto s = *svc.handle("status", json::Object{});
        CHECK(s.flag("data_roaming") && !s.flag("national_roaming", true));
        CHECK(svc.handle("sim.roam", json::Object{{"net_mcc", "460"}, {"net_mnc", "01"}}).has_value());
        CHECK(svc.handle("status", json::Object{})->str("data_state") == "roaming");
        CHECK(svc.handle("roaming.set", json::Object{{"on", false}, {"national", true}}).has_value());
        CHECK(svc.handle("status", json::Object{})->str("data_state") == "connected");
    }
    std::filesystem::remove_all(dir);
}

// The built-in backends by the names boards give (tools/sku.py MODEMS):
// each reports its own name as Status.backend; an unknown name is none.
static void test_backends() {
    tel::Loop loop;
    tel::Service svc("", [](const json::Value&) {});
    tel::BackendDeps deps{.loop = loop, .events = svc.events(), .sim_network = [] { return std::expected<tel::SimNetwork, std::string>(network()); }};
    const tel::BackendKind* sim = tel::find_backend("sim");
    CHECK(sim && sim->name == "sim" && !tel::find_backend("qmi") && !tel::find_backend(""));
    auto made = sim->make(deps);
    CHECK(made && made->sim == made->modem.get() && !made->failed && made->modem->status().backend == sim->name);
    deps.sim_network = [] { return std::expected<tel::SimNetwork, std::string>(std::unexpected("sim-network.json: cannot read it")); };
    auto bad = sim->make(deps);
    CHECK(!bad && bad.error() == "sim-network.json: cannot read it");
    std::vector<std::string_view> names;
    for (const auto& b : tel::backends()) names.push_back(b.name);
#ifdef TEL_MODEMMANAGER
    CHECK(names == (std::vector<std::string_view>{"sim", "modemmanager"}));
    tel::MmModem mm(loop, svc.events());
    CHECK(mm.status().backend == "modemmanager" && tel::contract_problems(mm.status()).empty());
#else
    CHECK(names == std::vector<std::string_view>{"sim"});
#endif
}

// What the Shell relies on in every backend's status: the simulated
// baseband keeps it through its states, and each rule catches a status
// that breaks it.
static void test_contract() {
    tel::Loop loop;
    tel::Service svc("", [](const json::Value&) {}, apn_db());
    tel::SimModem sim(loop, svc.events(), network());
    svc.attach(&sim, &sim);
    auto fine = [&](const char* what) {
        auto p = tel::contract_problems(sim.status());
        for (const auto& x : p) std::println(stderr, "{}: {}", what, x);
        return p.empty();
    };
    const tel::Status good = sim.status();
    CHECK(good.data_state == "connected" && good.data_link && good.data_link->ipv4 && good.data_link->ipv6 && fine("connected"));
    CHECK(sim.set_ims("registered").has_value() && fine("IMS registered"));
    CHECK(svc.handle("data.set", json::Object{{"on", false}}).has_value() && fine("data off"));
    CHECK(svc.handle("data.set", json::Object{{"on", true}}).has_value());
    CHECK(sim.roam("262", "02", "Vodafone.de").has_value() && sim.status().data_state == "roaming" && fine("roaming"));
    CHECK(svc.handle("roaming.set", json::Object{{"on", true}}).has_value() && fine("roaming, data roaming on"));
    CHECK(sim.roam("", "", "").has_value());
    CHECK(svc.handle("radio.set", json::Object{{"on", false}}).has_value() && fine("radio off"));
    CHECK(svc.handle("radio.set", json::Object{{"on", true}}).has_value());
    sim.lock("1111");
    CHECK(sim.status().sim == "pin" && fine("SIM locked"));

    auto broken = [&](auto change, std::string_view want) {
        tel::Status s = good;
        change(s);
        auto p = tel::contract_problems(s);
        bool found = std::ranges::any_of(p, [&](const std::string& x) { return x.starts_with(want); });
        if (!found) std::println(stderr, "contract: no '{}' in {} problem(s)", want, p.size());
        return found;
    };
    CHECK(broken([](tel::Status& s) { s.backend.clear(); }, "backend"));
    CHECK(broken([](tel::Status& s) { s.sim = "missing"; }, "sim:"));
    CHECK(broken([](tel::Status& s) { s.sim_tries = 2; }, "sim_tries"));
    CHECK(broken([](tel::Status& s) { s.signal = 5; }, "signal: 5"));
    CHECK(broken([](tel::Status& s) { s.registered = false; }, "signal: while not registered"));
    CHECK(broken([](tel::Status& s) { s.roaming = "abroad"; }, "roaming: 'abroad'"));
    CHECK(broken([](tel::Status& s) { s.registered = false, s.signal = 0, s.roaming = "national"; }, "roaming: while not registered"));
    CHECK(broken([](tel::Status& s) { s.data_state = "up"; }, "data_state: 'up'"));
    CHECK(broken([](tel::Status& s) { s.data = false; }, "data_state: connected with the radio on and data off"));
    CHECK(broken([](tel::Status& s) { s.radio = false; }, "data_state: connected with the radio off"));
    tel::Status no_modem = good;
    no_modem.data_state = "off", no_modem.data_link.reset();
    CHECK(tel::contract_problems(no_modem).empty());
    CHECK(broken([](tel::Status& s) { s.data_state = "roaming", s.data_link.reset(); }, "data_state: roaming where"));
    CHECK(broken([](tel::Status& s) { s.data_state = "idle"; }, "data_ip: given while data_state is idle"));
    CHECK(broken([](tel::Status& s) { s.data_link.reset(); }, "data_ip: none while data_state is connected"));
    CHECK(broken([](tel::Status& s) { s.data_ifaces = {"wwan0"}; }, "data_ip: interface simwwan0 is not in data_ifaces"));
    CHECK(broken([](tel::Status& s) { s.data_ifaces.push_back("wwan0\n[Network]"); }, "data_ifaces: "));
    CHECK(broken([](tel::Status& s) { s.data_link->iface = "rmnet_data0123456"; }, "data_ip: interface 'rmnet_data0123456'"));
    CHECK(broken([](tel::Status& s) { s.data_link->ipv4->method = "slaac"; }, "data_ip: method 'slaac'"));
    CHECK(broken([](tel::Status& s) { s.data_link->ipv6->prefix = 0; }, "data_ip: static without an address and a prefix of 1-128"));
    CHECK(broken([](tel::Status& s) { s.data_link->ipv4->address.clear(); }, "data_ip: static without an address and a prefix of 1-32"));
    CHECK(broken([](tel::Status& s) { s.mms_link = tel::DataLink{.iface = "wwan2", .ipv4 = tel::IpConfig{.method = "dhcp"}}; }, "mms_link: interface wwan2"));
    CHECK(broken([](tel::Status& s) { s.ims = "volte"; }, "ims: 'volte'"));
    CHECK(broken([](tel::Status& s) { s.ims = "registered", s.rat = "3G"; }, "ims: registered while not registered on 4G or 5G"));
    CHECK(broken([](tel::Status& s) { s.ims = "not-registered", s.registered = false, s.signal = 0; }, "ims: not-registered while not"));
    for (std::string_view name : {"rmnet_ipa0", "qmapmux0.0", "wwan0", "ccmni0", "ppp0", "rmnet_data10"}) CHECK(tel::valid_iface(name));
    for (std::string_view name : {"", ".x", "a/b", "a b", "rmnet_data0123456"}) CHECK(!tel::valid_iface(name));
}

static void test_modemmanager_values() {
    using namespace tel::mm;
    CHECK(roaming(5) && roaming(7) && roaming(10) && !roaming(1) && !roaming(6) && !roaming(0));
    CHECK(ip_family("ipv4") == 1 && ip_family("ipv6") == 2 && ip_family("ipv4v6") == 4);
    CHECK(allowed_auth("none") == 1 && allowed_auth("pap") == 2 && allowed_auth("chap") == 4 && allowed_auth("pap-chap") == 6);
    // A bearer up with the APN in use but another IP type or other
    // credentials is not the link Settings asks for.
    tel::Apn cmnet{.id = "cmnet", .name = "CMNET", .apn = "cmnet", .types = {"default"}};
    Dict asked{{"apn", std::string("cmnet")}, {"ip-type", 4LL}, {"apn-type", 2LL}, {"allow-roaming", 0LL}};
    CHECK(bearer_matches(asked, cmnet) && bearer_matches(Dict{{"apn", std::string("cmnet")}}, cmnet));
    CHECK(!bearer_matches(Dict{{"apn", std::string("cmnet.test")}, {"ip-type", 4LL}}, cmnet));
    tel::Apn ipv4_only = cmnet;
    ipv4_only.ip_type = "ipv4";
    CHECK(!bearer_matches(asked, ipv4_only) && !cmnet.same_link(ipv4_only));
    tel::Apn chap = cmnet;
    chap.auth = "chap", chap.user = "me", chap.password = "secret";
    CHECK(!bearer_matches(asked, chap) && !cmnet.same_link(chap));
    Dict with_chap = asked;
    with_chap["allowed-auth"] = 4LL, with_chap["user"] = std::string("me");
    CHECK(bearer_matches(with_chap, chap) && !bearer_matches(with_chap, cmnet));
    with_chap["user"] = std::string("other");
    CHECK(!bearer_matches(with_chap, chap));
    tel::Apn renamed = cmnet;
    renamed.name = "China Mobile NET";
    CHECK(cmnet.same_link(renamed));
    tel::Apn other_pass = chap;
    other_pass.password = "new";
    CHECK(!chap.same_link(other_pass));
    tel::Apn network_default{};
    CHECK(bearer_matches(Dict{{"apn", std::string("")}, {"ip-type", 1LL}}, network_default));
    Dict v4{{"method", 2LL}, {"address", std::string("10.1.2.3")}, {"prefix", 30LL}, {"gateway", std::string("10.1.2.4")},
            {"dns1", std::string("8.8.8.8")}, {"dns3", std::string("1.1.1.1")}, {"mtu", 1400LL}};
    auto c = ip_config(v4);
    CHECK(c && c->method == "static" && c->address == "10.1.2.3" && c->prefix == 30 && c->gateway == "10.1.2.4");
    CHECK(c && c->dns == (std::vector<std::string>{"8.8.8.8", "1.1.1.1"}) && ip_mtu(v4) == 1400);
    CHECK(ip_config(Dict{{"method", 3LL}})->method == "dhcp" && ip_config(Dict{{"method", 1LL}})->method == "ppp");
    CHECK(!ip_config(Dict{}) && !ip_config(Dict{{"method", 0LL}}) && ip_mtu(Dict{}) == 0);
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
    // A call the user answered that the modem then ended with an error
    // failed: it did not go unanswered.
    CHECK(end_reason("in", false, false, ReasonError, true) == "failed");
    CHECK(end_reason("in", false, false, ReasonAudioSetupFailed, true) == "failed");
    CHECK(end_reason("in", false, false, ReasonError) == "missed");
    CHECK(end_reason("in", false, false, 4, true) == "missed");
    CHECK(end_reason("in", false, true, ReasonError, true) == "rejected");
    CHECK(end_error(ReasonError).contains("error") && end_error(ReasonAudioSetupFailed).contains("audio"));
    CHECK(end_error(0).empty() && end_error(4).empty() && end_error(ReasonRefusedOrBusy).empty());
    tel::Call failed{.id = "c1", .state = "ended", .reason = "failed", .error = "answering failed: modem busy"};
    CHECK(failed.to_json().str("error") == "answering failed: modem busy");
    CHECK(!tel::Call{.id = "c2"}.to_json().find("error"));
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

static void test_ims() {
    using tel::ims_blame;
    using tel::ims_reading;
    CHECK(ims_reading(true, "registered\n").state == "registered");
    CHECK(ims_reading(true, " not-registered ").state == "not-registered");
    CHECK(ims_reading(true, "limited-registered").state == "limited-registered");
    auto bad = ims_reading(false, "ims-status: couldn't create client for the 'imsa' service\nmore");
    CHECK(bad.state.empty() && bad.error == "ims-status: couldn't create client for the 'imsa' service");
    CHECK(ims_reading(false, "registered").state.empty());
    CHECK(ims_reading(true, "Registered").state.empty() && ims_reading(true, "").error == "no answer");
    CHECK(ims_reading(false, "").error == "it failed");

    tel::Call out{.id = "c1", .dir = "out", .state = "ended", .reason = "remote"};
    CHECK(ims_blame(out, false, "4G", "not-registered") == "not-registered");
    CHECK(ims_blame(out, false, "5G", "registering") == "registering");
    CHECK(ims_blame(out, false, "4G", "limited-registered") == "limited-registered");
    CHECK(ims_blame(out, false, "4G", "registered").empty());
    CHECK(ims_blame(out, false, "4G", "").empty());
    // On 2G or 3G (the modem fell back) the call did not need IMS.
    CHECK(ims_blame(out, false, "2G", "not-registered").empty() && ims_blame(out, false, "3G", "not-registered").empty());
    CHECK(ims_blame(out, true, "4G", "not-registered").empty());
    auto with = [&](auto f) {
        tel::Call c = out;
        f(c);
        return ims_blame(c, false, "4G", "not-registered");
    };
    CHECK(with([](tel::Call& c) { c.reason = "failed"; }) == "not-registered");
    CHECK(with([](tel::Call& c) { c.reason = "hangup"; }).empty());
    CHECK(with([](tel::Call& c) { c.reason = "busy"; }).empty());
    CHECK(with([](tel::Call& c) { c.connected = 5; }).empty());
    CHECK(with([](tel::Call& c) { c.emergency = true; }).empty());
    CHECK(with([](tel::Call& c) { c.dir = "in"; c.reason = "missed"; }).empty());
    tel::Call failed{.id = "c1", .reason = "failed", .ims = "not-registered"};
    CHECK(failed.to_json().str("ims") == "not-registered" && !out.to_json().find("ims"));
}

// The IMS registration in the status: unknown where nothing reads it (the
// simulated modem until sim.ims says, ModemManager's without the program),
// the simulated modem's while it is on the network; sim.ims takes the
// program's words and unknown.
static void test_ims_status() {
    CHECK(tel::valid_ims("registered") && tel::valid_ims("limited-registered") && tel::valid_ims("unknown"));
    CHECK(!tel::valid_ims("") && !tel::valid_ims("Registered") && !tel::valid_ims("limited"));
    tel::Loop loop;
    std::vector<json::Value> events;
    tel::Service svc("", [&](const json::Value& ev) { events.push_back(ev); }, apn_db());
    tel::SimModem sim(loop, svc.events(), network());
    svc.attach(&sim, &sim);
    auto ims = [&] { return svc.handle("status", json::Object{})->str("ims"); };
    CHECK(ims() == "unknown" && tel::Status{}.ims == "unknown");
    CHECK(svc.handle("sim.ims", json::Object{{"state", "registered"}}).has_value() && ims() == "registered");
    CHECK(!events.empty() && events.back().find("status")->str("ims") == "registered");
    CHECK(svc.handle("radio.set", json::Object{{"on", false}}).has_value() && ims() == "unknown");
    CHECK(svc.handle("radio.set", json::Object{{"on", true}}).has_value() && ims() == "registered");
    CHECK(svc.handle("sim.ims", json::Object{{"state", "limited-registered"}}).has_value() && ims() == "limited-registered");
    CHECK(!svc.handle("sim.ims", json::Object{{"state", "volte"}}) && ims() == "limited-registered");
    CHECK(svc.handle("sim.ims", json::Object{{"state", "unknown"}}).has_value() && ims() == "unknown");
    CHECK(events.back().find("status")->str("ims") == "unknown");
#ifdef TEL_MODEMMANAGER
    tel::MmModem mm(loop, svc.events());
    CHECK(mm.status().ims == "unknown");
#endif
}

// stop from a timer ends run there, not after the next wait for a timer or
// a descriptor.
static void test_loop() {
    int p[2];
    CHECK(::pipe(p) == 0 && ::write(p[1], "x", 1) == 1);
    tel::Loop loop;
    bool read = false;
    loop.watch(p[0], [&] { read = true; loop.stop(); });
    loop.after(0ms, [&] { loop.stop(); });
    loop.run();
    CHECK(!read);
    ::close(p[0]);
    ::close(p[1]);
}

// The IMS registration program, run off the loop.
static void test_run_program() {
    auto dir = std::filesystem::temp_directory_path() / std::format("telephonyd-test-{}", ::getpid());
    std::filesystem::create_directories(dir);
    auto script = [&](const char* name, const std::string& body) {
        auto p = dir / name;
        std::ofstream(p) << "#!/bin/sh\n" << body << "\n";
        std::filesystem::permissions(p, std::filesystem::perms::owner_all);
        return p.string();
    };
    auto pidfile = [&](const char* name) { return (dir / name).string() + ".pid"; };
    // The pids a script wrote to its file, once it has (none after 5 s).
    auto pids_in = [](const std::string& file) {
        std::vector<pid_t> pids;
        for (auto end = std::chrono::steady_clock::now() + 5s; pids.empty() && std::chrono::steady_clock::now() < end;) {
            std::this_thread::sleep_for(10ms);
            std::ifstream in(file);
            for (pid_t p; in >> p;)
                if (p > 0) pids.push_back(p);
        }
        return pids;
    };
    auto alive = [](pid_t pid) {
        std::string stat;
        std::getline(std::ifstream(std::format("/proc/{}/stat", pid)), stat);
        auto paren = stat.rfind(')');
        return paren != std::string::npos && paren + 2 < stat.size() && stat[paren + 2] != 'Z';
    };
    // What is killed still needs the CPU to go.
    auto gone = [&](pid_t pid) {
        for (auto end = std::chrono::steady_clock::now() + 5s; alive(pid); std::this_thread::sleep_for(10ms))
            if (std::chrono::steady_clock::now() > end) return false;
        return true;
    };
    struct Got {
        int calls = 0;
        bool ok = false, reaped = false;
        std::string out;
    };
    auto start = [](tel::Loop& loop, Got& got, const std::vector<std::string>& argv, std::chrono::milliseconds timeout) {
        tel::run_program(loop, argv, timeout, [&got](bool ok, std::string out) {
            ++got.calls;
            got.ok = ok;
            got.out = std::move(out);
        });
    };
    // Until done has come and nothing the run started (the guard neither) is
    // left to reap, or for 5 s.
    auto settle = [](tel::Loop& loop, Got& got) {
        std::function<void()> check = [&] {
            siginfo_t si{};
            got.reaped = got.calls && ::waitid(P_ALL, 0, &si, WEXITED | WNOHANG | WNOWAIT) < 0 && errno == ECHILD;
            if (got.reaped) return loop.stop();
            loop.after(10ms, check);
        };
        check();
        loop.after(5s, [&] { loop.stop(); });
        loop.run();
    };
    auto run = [&](const std::vector<std::string>& argv, std::chrono::milliseconds timeout) {
        tel::Loop loop;
        Got got;
        start(loop, got, argv, timeout);
        settle(loop, got);
        return got;
    };
    auto g = run({script("ok", "echo registered")}, 2s);
    CHECK(g.calls == 1 && g.reaped && g.ok && g.out == "registered\n");
    g = run({script("err", "echo \"no IMSA\" >&2; exit 1")}, 2s);
    CHECK(g.calls == 1 && g.reaped && !g.ok && g.out == "no IMSA\n");
    g = run({(dir / "missing").string()}, 2s);
    CHECK(g.calls == 1 && g.reaped && !g.ok && g.out.contains("missing"));

    // A program still running at the timeout ends with what it started. The
    // timeout comes on the loop's clock once the script has started its
    // child, however long that took.
    auto file = pidfile("hangs");
    tel::Loop hangs;
    Got got;
    start(hangs, got, {script("hangs", std::format("sleep 30 & echo $! > {0}.new && mv {0}.new {0}; wait; echo registered", file))}, 1h);
    auto pids = pids_in(file);
    hangs.advance(1h);
    settle(hangs, got);
    CHECK(got.calls == 1 && got.reaped && !got.ok && got.out == "it did not finish in time");
    CHECK(pids.size() == 1 && gone(pids[0]));

    // done comes at the timeout, not at the output's end: a child the kill
    // does not end at once (one stuck in the kernel; here one that left the
    // program's process group) keeps the output open.
    file = pidfile("escapes");
    tel::Loop escapes;
    got = {};
    start(escapes, got, {script("escapes", std::format("setsid sh -c 'echo $$ > {0}.new && mv {0}.new {0}; exec sleep 30' & wait", file))}, 1h);
    pids = pids_in(file);
    escapes.advance(1h);
    settle(escapes, got);
    CHECK(got.calls == 1 && got.reaped && !got.ok && got.out == "it did not finish in time");
    CHECK(pids.size() == 1);
    for (pid_t p : pids) ::kill(p, SIGKILL);

    // A program still running ends with telephonyd killed, and so does what
    // it started: restarted by phone-shell --services spawn, it left them
    // running.
    file = pidfile("stays");
    auto program = script("stays", std::format("sleep 30 & echo \"$$ $!\" > {0}.new && mv {0}.new {0}; wait", file));
    pid_t daemon = ::fork();
    if (daemon == 0) {
        tel::Loop loop;
        tel::run_program(loop, {program}, 60s, [](bool, std::string) {});
        loop.run();
        std::_Exit(0);
    }
    pids = pids_in(file);
    ::kill(daemon, SIGKILL);
    ::waitpid(daemon, nullptr, 0);
    CHECK(pids.size() == 2 && gone(pids[0]) && gone(pids[1]));
    for (pid_t p : pids)
        if (alive(p)) ::kill(p, SIGKILL);
    std::filesystem::remove_all(dir);
}

int main() {
    test_json();
    test_ims();
    test_ims_status();
    test_loop();
    test_run_program();
    test_calls_and_sms();
    test_second_calls();
    test_slow_network();
    test_numbers_out_of_range();
    test_dtmf();
    test_sim_emergency_numbers();
    test_sim_contacts();
    test_sms_pending();
    test_sms_legacy();
    test_log_import();
    test_cell_broadcast();
    test_sms_data_and_class();
    test_blocked_call();
    test_sim_lock();
    test_sim_pin_settings();
    test_imei();
    test_radio();
    test_ussd();
    test_sim_network();
    test_country_and_nitz();
    test_apn_db();
    test_apn_store();
    test_apn_refusals();
    test_apn_overfull();
    test_damaged_state();
    test_apn_service();
    test_apn_plan();
    test_bearer_roles();
    test_mms_connect();
    test_data_roaming();
    test_modemmanager_values();
    test_backends();
    test_contract();
    if (failures) {
        std::println(stderr, "{} check(s) failed", failures);
        return EXIT_FAILURE;
    }
    std::println("telephonyd tests passed");
}
