#include "json.hpp"

#include <charconv>
#include <cmath>
#include <format>

namespace json {

const Value* Value::find(std::string_view key) const {
    if (auto* o = std::get_if<Object>(this)) {
        if (auto it = o->find(std::string(key)); it != o->end()) return &it->second;
    }
    return nullptr;
}

std::string Value::str(std::string_view key, std::string def) const {
    if (auto* v = find(key); v && v->is_string()) return std::get<std::string>(*v);
    return def;
}

double Value::num(std::string_view key, double def) const {
    if (auto* v = find(key); v && std::holds_alternative<double>(*v)) return std::get<double>(*v);
    return def;
}

bool Value::flag(std::string_view key, bool def) const {
    if (auto* v = find(key); v && std::holds_alternative<bool>(*v)) return std::get<bool>(*v);
    return def;
}

namespace {

struct Parser {
    std::string_view s;
    size_t i = 0;

    void ws() {
        while (i < s.size() && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r')) ++i;
    }

    std::unexpected<std::string> fail(std::string_view what) const {
        return std::unexpected(std::format("json: {} at offset {}", what, i));
    }

    static void utf8(std::string& out, unsigned cp) {
        if (cp < 0x80) {
            out += static_cast<char>(cp);
        } else if (cp < 0x800) {
            out += static_cast<char>(0xC0 | (cp >> 6));
            out += static_cast<char>(0x80 | (cp & 0x3F));
        } else if (cp < 0x10000) {
            out += static_cast<char>(0xE0 | (cp >> 12));
            out += static_cast<char>(0x80 | ((cp >> 6) & 0x3F));
            out += static_cast<char>(0x80 | (cp & 0x3F));
        } else {
            out += static_cast<char>(0xF0 | (cp >> 18));
            out += static_cast<char>(0x80 | ((cp >> 12) & 0x3F));
            out += static_cast<char>(0x80 | ((cp >> 6) & 0x3F));
            out += static_cast<char>(0x80 | (cp & 0x3F));
        }
    }

    std::expected<unsigned, std::string> hex4() {
        if (i + 4 > s.size()) return fail("short \\u escape");
        unsigned v = 0;
        auto [p, ec] = std::from_chars(s.data() + i, s.data() + i + 4, v, 16);
        if (ec != std::errc{} || p != s.data() + i + 4) return fail("bad \\u escape");
        i += 4;
        return v;
    }

    std::expected<std::string, std::string> string() {
        ++i;  // opening quote
        std::string out;
        while (i < s.size()) {
            char c = s[i++];
            if (c == '"') return out;
            if (c != '\\') {
                out += c;
                continue;
            }
            if (i >= s.size()) break;
            switch (char e = s[i++]) {
                case '"': out += '"'; break;
                case '\\': out += '\\'; break;
                case '/': out += '/'; break;
                case 'b': out += '\b'; break;
                case 'f': out += '\f'; break;
                case 'n': out += '\n'; break;
                case 'r': out += '\r'; break;
                case 't': out += '\t'; break;
                case 'u': {
                    auto hi = hex4();
                    if (!hi) return std::unexpected(hi.error());
                    unsigned cp = *hi;
                    if (cp >= 0xD800 && cp < 0xDC00 && i + 6 <= s.size() && s[i] == '\\' && s[i + 1] == 'u') {
                        i += 2;
                        auto lo = hex4();
                        if (!lo) return std::unexpected(lo.error());
                        cp = 0x10000 + ((cp - 0xD800) << 10) + (*lo - 0xDC00);
                    }
                    utf8(out, cp);
                    break;
                }
                default: return fail(std::format("bad escape '{}'", e));
            }
        }
        return fail("unterminated string");
    }

    std::expected<Value, std::string> value() {
        ws();
        if (i >= s.size()) return fail("unexpected end");
        char c = s[i];
        if (c == '{') {
            ++i;
            Object o;
            ws();
            if (i < s.size() && s[i] == '}') return ++i, Value(std::move(o));
            for (;;) {
                ws();
                if (i >= s.size() || s[i] != '"') return fail("expected key");
                auto k = string();
                if (!k) return std::unexpected(k.error());
                ws();
                if (i >= s.size() || s[i] != ':') return fail("expected ':'");
                ++i;
                auto v = value();
                if (!v) return v;
                o.insert_or_assign(std::move(*k), std::move(*v));
                ws();
                if (i < s.size() && s[i] == ',') { ++i; continue; }
                if (i < s.size() && s[i] == '}') return ++i, Value(std::move(o));
                return fail("expected ',' or '}'");
            }
        }
        if (c == '[') {
            ++i;
            Array a;
            ws();
            if (i < s.size() && s[i] == ']') return ++i, Value(std::move(a));
            for (;;) {
                auto v = value();
                if (!v) return v;
                a.push_back(std::move(*v));
                ws();
                if (i < s.size() && s[i] == ',') { ++i; continue; }
                if (i < s.size() && s[i] == ']') return ++i, Value(std::move(a));
                return fail("expected ',' or ']'");
            }
        }
        if (c == '"') {
            auto str = string();
            if (!str) return std::unexpected(str.error());
            return Value(std::move(*str));
        }
        if (s.substr(i, 4) == "true") return i += 4, Value(true);
        if (s.substr(i, 5) == "false") return i += 5, Value(false);
        if (s.substr(i, 4) == "null") return i += 4, Value(nullptr);
        double d = 0;
        auto [p, ec] = std::from_chars(s.data() + i, s.data() + s.size(), d);
        if (ec != std::errc{}) return fail("bad value");
        i = static_cast<size_t>(p - s.data());
        return Value(d);
    }
};

void dump_string(std::string& out, const std::string& s) {
    out += '"';
    for (unsigned char c : s) {
        switch (c) {
            case '"': out += "\\\""; break;
            case '\\': out += "\\\\"; break;
            case '\n': out += "\\n"; break;
            case '\r': out += "\\r"; break;
            case '\t': out += "\\t"; break;
            default:
                if (c < 0x20) out += std::format("\\u{:04x}", c);
                else out += static_cast<char>(c);
        }
    }
    out += '"';
}

void dump_to(std::string& out, const Value& v) {
    std::visit(
        [&](const auto& x) {
            using T = std::decay_t<decltype(x)>;
            if constexpr (std::is_same_v<T, std::nullptr_t>) out += "null";
            else if constexpr (std::is_same_v<T, bool>) out += x ? "true" : "false";
            else if constexpr (std::is_same_v<T, double>) {
                if (std::isfinite(x) && x == std::floor(x) && std::fabs(x) < 1e15) out += std::format("{}", static_cast<long long>(x));
                else out += std::format("{}", x);
            } else if constexpr (std::is_same_v<T, std::string>) dump_string(out, x);
            else if constexpr (std::is_same_v<T, Array>) {
                out += '[';
                for (size_t k = 0; k < x.size(); ++k) {
                    if (k) out += ',';
                    dump_to(out, x[k]);
                }
                out += ']';
            } else {
                out += '{';
                bool first = true;
                for (const auto& [k, val] : x) {
                    if (!first) out += ',';
                    first = false;
                    dump_string(out, k);
                    out += ':';
                    dump_to(out, val);
                }
                out += '}';
            }
        },
        static_cast<const Value::variant&>(v));
}

}  // namespace

std::expected<Value, std::string> parse(std::string_view text) {
    Parser p{text};
    auto v = p.value();
    if (!v) return v;
    p.ws();
    if (p.i != text.size()) return p.fail("trailing data");
    return v;
}

std::string dump(const Value& v) {
    std::string out;
    dump_to(out, v);
    return out;
}

}  // namespace json
