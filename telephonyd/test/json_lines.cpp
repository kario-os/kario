// Parses each line of its input with telephonyd's JSON reader, as the
// service reads its clients' requests (tools/cfuzz): what parses dumps
// to a line that parses to the same dump. Built with KARIO_LIBFUZZER
// defined it is a libFuzzer target instead (make fuzz-libfuzzer).
#include <cstdint>
#include <cstdlib>
#include <iostream>
#include <sstream>
#include <string>

#include "json.hpp"

static bool round_trips(const std::string& line) {
    auto v = json::parse(line);
    if (!v) return true;
    auto text = json::dump(*v);
    auto again = json::parse(text);
    if (!again || json::dump(*again) != text) {
        std::cerr << "round trip: " << text << "\n";
        return false;
    }
    return true;
}

#ifdef KARIO_LIBFUZZER
extern "C" int LLVMFuzzerTestOneInput(const uint8_t* data, size_t size) {
    std::istringstream in(std::string(reinterpret_cast<const char*>(data), size));
    std::string line;
    while (std::getline(in, line))
        if (!round_trips(line)) std::abort();
    return 0;
}
#else
int main() {
    std::string line;
    while (std::getline(std::cin, line))
        if (!round_trips(line)) return 3;
}
#endif
