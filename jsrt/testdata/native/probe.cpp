/* probe.c written with kario_native.hpp: the same exports, so native_test.go
 * drives it with the same main.js, plus what the wrapper adds (typed
 * arguments, exceptions). Built by the test (c++ -std=c++17 -shared). */
#include <cctype>
#include <chrono>
#include <string>
#include <thread>

#include "kario_native.hpp"

static std::thread::id main_thread;

static double add(kario::call &c) { return c.arg<double>(0) + c.arg<double>(1); }

static std::string upper(kario::call &c)
{
	std::string s = c.arg<std::string>(0);
	for (auto &ch : s)
		ch = static_cast<char>(std::toupper(static_cast<unsigned char>(ch)));
	return s;
}

static size_t invert(kario::call &c)
{
	kario::bytes b = c.arg<kario::bytes>(0);
	for (auto &x : b)
		x = static_cast<uint8_t>(255 - x);
	return b.size();
}

static void reversed(kario::call &c)
{
	kario::bytes in = c.arg<kario::bytes>(0);
	kario::bytes out = c.ret_bytes(in.size());
	for (size_t i = 0; i < in.size(); i++)
		out[i] = in[in.size() - 1 - i];
}

static void echo(kario::call &c)
{
	if (auto j = c.arg<std::optional<kario::json>>(0))
		c.ret(*j);
}

static std::string types(kario::call &c)
{
	static const char code[] = "u0bnsyo";
	std::string out;
	for (int i = 0; i < c.argc(); i++)
		out += code[c.type(i)];
	return out;
}

static void fail(kario::call &c)
{
	c.ret(1.0);
	throw kario::error(c.type(0) == KARIO_STRING ? c.arg<std::string>(0) : "failed");
}

static kario::json bad_json(kario::call &) { return {"{oops"}; }

static kario::json slow(kario::call &c)
{
	std::this_thread::sleep_for(std::chrono::milliseconds(static_cast<long>(c.arg<double>(0))));
	return {std::string("{\"worker\":") + (std::this_thread::get_id() == main_thread ? "false" : "true") + "}"};
}

static void crash(kario::call &)
{
	volatile int *p = nullptr;
	*p = 1;
}

static int typed(kario::call &c) { return c.arg<int32_t>(0) * 2; }

static void thrown(kario::call &) { throw std::runtime_error("thrown: from C++"); }

static void unknown(kario::call &) { throw 42; }

KARIO_NATIVE_CPP_MODULE([](kario::module &m) {
	main_thread = std::this_thread::get_id();
	kario::emitter events = m.events();
	m.function("add", add);
	m.function("upper", upper);
	m.function("invert", invert);
	m.function("reversed", reversed);
	m.function("reversedAsync", reversed, kario::async);
	m.function("echo", echo);
	m.function("echoAsync", echo, kario::async);
	m.function("types", types);
	m.function("fail", fail);
	m.function("failAsync", fail, kario::async);
	m.function("badJson", bad_json);
	m.function("slow", slow, kario::async);
	m.function("start", [events](kario::call &c) {
		int n = c.arg<int>(0);
		std::thread([events, n] {
			for (int i = 0; i < n; i++)
				events.emit("tick", "{\"i\":" + std::to_string(i) + "}");
			events.emit("done");
		}).detach();
	});
	m.function("crash", crash);
	m.function("crashAsync", crash, kario::async);
	m.function("typed", typed);
	m.function("thrown", thrown);
	m.function("thrownAsync", thrown, kario::async);
	m.function("unknown", unknown);
	m.value("engine", m.engine());
	m.value("abi", m.api().major * 100 + m.api().minor);
	bool refused = false;
	try {
		m.function("on", add);
	} catch (const kario::error &) {
		refused = true;
	}
	if (!refused)
		throw kario::error("exported the runtime's own \"on\"");
})
