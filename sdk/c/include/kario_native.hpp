/*
 * kario_native.hpp: native modules in C++ (C++17), header only, over
 * kario_native.h (22-developer-api.md §9.3), which stays the interface: the
 * library is the same kind of module, loads the same way in kario-jsrt and
 * in Node.js, and links nothing of the runtime.
 *
 * What it adds: functions are any callable taking kario::call&, returning
 * what becomes the JavaScript result (void, bool, a number, a string,
 * kario::json, a byte vector); arguments come typed (call.arg<T>(i)) or
 * throw; and an exception never crosses into the runtime: whatever a
 * function throws becomes the Error JavaScript gets (the rejection, when
 * asynchronous), with what() as its message.
 *
 *	#include "kario_native.hpp"
 *
 *	static double add(kario::call &c) { return c.arg<double>(0) + c.arg<double>(1); }
 *
 *	static void invert(kario::call &c)
 *	{
 *		for (auto &b : c.arg<kario::bytes>(0))
 *			b = 255 - b;
 *	}
 *
 *	KARIO_NATIVE_CPP_MODULE([](kario::module &m) {
 *		m.function("add", add);
 *		m.function("invertAsync", invert, kario::async);
 *		m.value("version", "1.2");
 *	})
 *
 * Build it like the C one, with the C++ compiler: c++ -std=c++17 -shared
 * -fPIC -I sdk/c/include. libstdc++ comes from the system (the image has
 * it) unless the module links it in (-static-libstdc++).
 *
 * The rules of kario_native.h hold: what the accessors give lives until
 * the function returns (std::string_view, kario::bytes, kario::json), a
 * function exported with kario::async runs on a worker thread, a module's
 * own threads use only an emitter.
 */
#ifndef KARIO_NATIVE_HPP
#define KARIO_NATIVE_HPP

#include <cmath>
#include <cstdint>
#include <cstdio>
#include <cstring>
#include <exception>
#include <limits>
#include <optional>
#include <stdexcept>
#include <string>
#include <string_view>
#include <type_traits>
#include <utility>
#include <vector>

#include "kario_native.h"

namespace kario {

/* The error a function throws to make JavaScript's call throw (any
 * std::exception does; this is the one without a library's meaning). */
class error : public std::runtime_error {
public:
	using std::runtime_error::runtime_error;
};

/* Bytes of an ArrayBuffer or typed array: JavaScript's own memory in a
 * plain function (changes show), a copy in an asynchronous one. */
class bytes {
public:
	bytes(uint8_t *p, size_t n) : p_(p), n_(n) {}
	uint8_t *data() const { return p_; }
	size_t size() const { return n_; }
	uint8_t *begin() const { return p_; }
	uint8_t *end() const { return p_ + n_; }
	uint8_t &operator[](size_t i) const { return p_[i]; }

private:
	uint8_t *p_;
	size_t n_;
};

/* JSON text: an argument read as JSON, or a result parsed as JSON. */
struct json {
	std::string text;
};

/* export_fn's flags. */
constexpr uint32_t async = KARIO_ASYNC;

namespace detail {
inline const char *type_name(kario_type t)
{
	switch (t) {
	case KARIO_UNDEFINED:
		return "undefined";
	case KARIO_NULL:
		return "null";
	case KARIO_BOOL:
		return "a boolean";
	case KARIO_NUMBER:
		return "a number";
	case KARIO_STRING:
		return "a string";
	case KARIO_BYTES:
		return "bytes";
	case KARIO_OBJECT:
		break;
	}
	return "an object";
}

template <class T> struct is_optional : std::false_type {};
template <class T> struct is_optional<std::optional<T>> : std::true_type {};
} // namespace detail

/* One call: its arguments and its result. */
class call {
public:
	call(const kario_api *api, kario_call *c, const char *name) : api_(api), c_(c), name_(name) {}

	const kario_api &api() const { return *api_; }
	kario_call *raw() const { return c_; }
	/* The exported name, for messages. */
	const char *name() const { return name_; }

	int argc() const { return api_->argc(c_); }
	kario_type type(int i) const { return api_->type(c_, i); }
	bool missing(int i) const { return type(i) == KARIO_UNDEFINED || type(i) == KARIO_NULL; }
	/* JavaScript's truthiness, of any argument. */
	bool truthy(int i) const { return api_->boolean(c_, i) != 0; }

	/* arg<T>(i) is argument i as T, or throws kario::error saying what it
	 * is instead: double, float and the integer types (a number; an
	 * integer type wants an integral number in its range), bool,
	 * std::string_view and std::string, kario::bytes, kario::json (any
	 * value JSON can hold), std::optional<T> (empty for undefined or
	 * null). Arguments count from 0, messages from 1. */
	template <class T> T arg(int i) const
	{
		if constexpr (detail::is_optional<T>::value) {
			if (missing(i))
				return std::nullopt;
			return arg<typename T::value_type>(i);
		} else if constexpr (std::is_same_v<T, bool>) {
			expect(i, KARIO_BOOL, "a boolean");
			return api_->boolean(c_, i) != 0;
		} else if constexpr (std::is_floating_point_v<T>) {
			expect(i, KARIO_NUMBER, "a number");
			return static_cast<T>(api_->number(c_, i));
		} else if constexpr (std::is_integral_v<T>) {
			expect(i, KARIO_NUMBER, "a number");
			double v = api_->number(c_, i);
			if (std::trunc(v) != v || v < static_cast<double>(std::numeric_limits<T>::lowest()) ||
			    v > static_cast<double>(std::numeric_limits<T>::max()))
				fail_arg(i, "is not an integer in range");
			return static_cast<T>(v);
		} else if constexpr (std::is_same_v<T, std::string_view> || std::is_same_v<T, std::string>) {
			expect(i, KARIO_STRING, "a string");
			size_t n = 0;
			const char *s = api_->string(c_, i, &n);
			return T(s ? s : "", s ? n : 0);
		} else if constexpr (std::is_same_v<T, bytes>) {
			expect(i, KARIO_BYTES, "bytes");
			size_t n = 0;
			void *p = api_->bytes(c_, i, &n);
			return bytes(static_cast<uint8_t *>(p), n);
		} else if constexpr (std::is_same_v<T, json>) {
			size_t n = 0;
			const char *s = api_->json(c_, i, &n);
			if (!s)
				fail_arg(i, (std::string("is ") + detail::type_name(type(i)) + ", which JSON cannot hold").c_str());
			return json{std::string(s, n)};
		} else {
			static_assert(sizeof(T) == 0, "kario::call::arg: not a type arguments come as");
		}
	}

	/* The result; returning a value from the function sets it too. */
	void ret(double v) const { api_->ret_number(c_, v); }
	void ret(bool v) const { api_->ret_bool(c_, v); }
	void ret(std::string_view s) const { api_->ret_string(c_, s.data(), s.size()); }
	void ret(const char *s) const { ret(std::string_view(s)); }
	void ret(const json &j) const { api_->ret_json(c_, j.text.data(), j.text.size()); }
	void ret(const std::vector<uint8_t> &v) const
	{
		bytes out = ret_bytes(v.size());
		if (v.size())
			std::memcpy(out.data(), v.data(), v.size());
	}
	/* The result ArrayBuffer, zeroed, to fill before returning. */
	bytes ret_bytes(size_t n) const
	{
		void *p = api_->ret_bytes(c_, n);
		if (!p)
			throw std::bad_alloc();
		return bytes(static_cast<uint8_t *>(p), n);
	}

private:
	void expect(int i, kario_type t, const char *what) const
	{
		if (type(i) != t)
			fail_arg(i, (std::string("is ") + detail::type_name(type(i)) + ", not " + what).c_str());
	}
	[[noreturn]] void fail_arg(int i, const char *what) const
	{
		throw error(std::string(name_) + ": argument " + std::to_string(i + 1) + " " + what);
	}

	const kario_api *api_;
	kario_call *c_;
	const char *name_;
};

/* What a module's own threads report through: emit from any thread. */
class emitter {
public:
	emitter(const kario_api *api, kario_module *m) : api_(api), m_(m) {}
	/* false when the queue is full or json is not JSON; no json: the
	 * handler gets undefined. */
	bool emit(const char *event, std::string_view json = {}) const
	{
		return api_->emit(m_, event, json.empty() ? nullptr : json.data(), json.size()) == 0;
	}

private:
	const kario_api *api_;
	kario_module *m_;
};

namespace detail {
template <class F> struct exported {
	F fn;
	std::string name;
};

template <class R> void set_result(call &c, R &&v)
{
	using T = std::decay_t<R>;
	if constexpr (std::is_same_v<T, bool>)
		c.ret(static_cast<bool>(v));
	else if constexpr (std::is_arithmetic_v<T>)
		c.ret(static_cast<double>(v));
	else if constexpr (std::is_same_v<T, std::string> || std::is_same_v<T, std::string_view>)
		c.ret(std::string_view(v));
	else if constexpr (std::is_same_v<T, const char *> || std::is_same_v<T, char *>)
		c.ret(static_cast<const char *>(v));
	else if constexpr (std::is_same_v<T, json> || std::is_same_v<T, std::vector<uint8_t>>)
		c.ret(v);
	else
		static_assert(sizeof(T) == 0, "kario: a function returns void, bool, a number, a string, kario::json or std::vector<uint8_t>");
}

template <class F> void trampoline(const kario_api *api, kario_call *kc, void *data)
{
	auto *e = static_cast<exported<F> *>(data);
	call c(api, kc, e->name.c_str());
	try {
		if constexpr (std::is_void_v<std::invoke_result_t<F &, call &>>)
			e->fn(c);
		else
			set_result(c, e->fn(c));
	} catch (const std::exception &x) {
		api->fail(kc, x.what());
	} catch (...) {
		api->fail(kc, (e->name + ": threw something that is not a std::exception").c_str());
	}
}
} // namespace detail

/* The module being loaded, inside KARIO_NATIVE_CPP_MODULE's function. */
class module {
public:
	module(const kario_api *api, kario_module *m) : api_(api), m_(m) {}

	const kario_api &api() const { return *api_; }
	/* "QuickJS-ng 0.18.0", "Node.js 24.14.0": for a log line. */
	const char *engine() const { return api_->engine; }

	/* Exports fn as name; throws kario::error for a bad or repeated name.
	 * The function object lives as long as the process (modules are never
	 * unloaded). */
	template <class F> void function(const char *name, F fn, uint32_t flags = 0)
	{
		using D = std::decay_t<F>;
		auto *e = new detail::exported<D>{D(std::move(fn)), name};
		if (api_->export_fn(m_, name, detail::trampoline<D>, e, flags) != 0) {
			delete e;
			throw error(std::string("cannot export \"") + name + "\" (a bad or repeated name)");
		}
	}
	void value(const char *name, double v)
	{
		if (api_->export_number(m_, name, v) != 0)
			throw error(std::string("cannot export \"") + name + "\" (a bad or repeated name)");
	}
	void value(const char *name, const char *s)
	{
		if (api_->export_string(m_, name, s) != 0)
			throw error(std::string("cannot export \"") + name + "\" (a bad or repeated name)");
	}
	emitter events() const { return emitter(api_, m_); }

private:
	const kario_api *api_;
	kario_module *m_;
};

namespace detail {
template <class F> int init(const kario_api *api, kario_module *m, F &&fn)
{
	try {
		module mod(api, m);
		fn(mod);
		return 0;
	} catch (const std::exception &x) {
		std::fprintf(stderr, "native module: %s\n", x.what());
	} catch (...) {
		std::fprintf(stderr, "native module: init threw something that is not a std::exception\n");
	}
	return 1;
}
} // namespace detail

} // namespace kario

/* KARIO_NATIVE_CPP_MODULE(fn) is KARIO_NATIVE_MODULE for C++: fn (a
 * function or lambda taking kario::module&) exports the names; an
 * exception from it refuses the module, its message in the log. Variadic,
 * so a lambda's commas do not split it. */
#define KARIO_NATIVE_CPP_MODULE(...)                                                                 \
	static int kario_native_cpp_init_(const kario_api *api, kario_module *m) { return ::kario::detail::init(api, m, __VA_ARGS__); } \
	KARIO_NATIVE_MODULE(kario_native_cpp_init_)

#endif
