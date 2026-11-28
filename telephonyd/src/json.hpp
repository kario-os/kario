// Minimal JSON for the service control protocol (newline-delimited JSON, the
// same framing as fw/wire): parse one line, build replies, dump one line.
#pragma once

#include <expected>
#include <map>
#include <string>
#include <string_view>
#include <variant>
#include <vector>

namespace json {

struct Value;
using Array = std::vector<Value>;
using Object = std::map<std::string, Value>;

struct Value : std::variant<std::nullptr_t, bool, double, std::string, Array, Object> {
    using variant::variant;
    Value() : variant(nullptr) {}
    Value(const char* s) : variant(std::string(s)) {}
    Value(int v) : variant(static_cast<double>(v)) {}
    Value(long v) : variant(static_cast<double>(v)) {}
    Value(long long v) : variant(static_cast<double>(v)) {}

    bool is_object() const { return std::holds_alternative<Object>(*this); }
    bool is_array() const { return std::holds_alternative<Array>(*this); }
    bool is_string() const { return std::holds_alternative<std::string>(*this); }

    // Accessors for objects with defaults, so handlers stay short.
    std::string str(std::string_view key, std::string def = {}) const;
    double num(std::string_view key, double def = 0) const;
    bool flag(std::string_view key, bool def = false) const;
    const Value* find(std::string_view key) const;
};

std::expected<Value, std::string> parse(std::string_view text);
std::string dump(const Value& v);

}  // namespace json
