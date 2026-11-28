// A C++ app on the C ABI (sdk/c): a counter, and a capability call. It is
// what a vendor porting existing C++ code starts from. Its text is written in
// English; locale/<language>.json in the app's directory, where the shell
// starts it, maps each message to the device's language (PHONE_LANG, zh-Hans
// when unset), with the same fallbacks as the Go SDK's catalogs: a message
// zh-Hant lacks comes from zh-Hans, and any language ends in English.
#include <phoneui.h>

#include <cstdio>
#include <cstdlib>
#include <fstream>
#include <sstream>
#include <string>
#include <vector>

namespace {

// The catalogs along the language's fallback chain, the language's own
// first; each a flat JSON object read with the SDK's JSON reader.
std::vector<std::string> catalogs;

void load_catalogs()
{
	const char* env = std::getenv("PHONE_LANG");
	std::string lang = env && *env ? env : "zh-Hans";
	std::vector<std::string> chain{lang};
	if (lang == "zh-Hant")
		chain.push_back("zh-Hans");
	for (const auto& l : chain) {
		std::ifstream in("locale/" + l + ".json");
		if (!in)
			continue;
		std::stringstream ss;
		ss << in.rdbuf();
		catalogs.push_back(ss.str());
	}
}

// tr is msg in the device's language, with arg in place of its %s.
std::string tr(const char* msg, const std::string& arg = "")
{
	std::string s = msg;
	for (const auto& c : catalogs) {
		if (char* t = phoneui_get_string(c.c_str(), msg)) {
			s = t;
			std::free(t);
			break;
		}
	}
	if (auto at = s.find("%s"); at != std::string::npos)
		s.replace(at, 2, arg);
	return s;
}

void screen(phoneui* ui, const char* msg_type, int count, const std::string& note)
{
	phoneui_json* j = phoneui_json_new();
	auto button = [&](const char* id, const std::string& label) {
		phoneui_json_begin_object(j);
		phoneui_json_key(j, "kind");
		phoneui_json_string(j, "button");
		phoneui_json_key(j, "id");
		phoneui_json_string(j, id);
		phoneui_json_key(j, "text");
		phoneui_json_string(j, label.c_str());
		phoneui_json_key(j, "actions");
		phoneui_json_begin_array(j);
		phoneui_json_begin_object(j);
		phoneui_json_key(j, "id");
		phoneui_json_string(j, id);
		phoneui_json_key(j, "label");
		phoneui_json_string(j, label.c_str());
		phoneui_json_key(j, "primary");
		phoneui_json_bool(j, 1);
		phoneui_json_end_object(j);
		phoneui_json_end_array(j);
		phoneui_json_end_object(j);
	};
	auto text = [&](const std::string& s) {
		phoneui_json_begin_object(j);
		phoneui_json_key(j, "kind");
		phoneui_json_string(j, "text");
		phoneui_json_key(j, "text");
		phoneui_json_string(j, s.c_str());
		phoneui_json_end_object(j);
	};
	phoneui_json_begin_object(j);
	phoneui_json_key(j, "t");
	phoneui_json_string(j, msg_type);
	phoneui_json_key(j, "screen");
	phoneui_json_begin_object(j);
	phoneui_json_key(j, "id");
	phoneui_json_string(j, "main");
	phoneui_json_key(j, "title");
	phoneui_json_string(j, tr("C++ sample").c_str());
	phoneui_json_key(j, "body");
	phoneui_json_begin_array(j);
	text(tr("Speaks the phone.Ui protocol through the C interface"));
	text(tr("Tapped %s times", std::to_string(count)));
	button("inc", tr("Tap me"));
	button("copy", tr("Copy the count"));
	if (!note.empty())
		text(note);
	phoneui_json_end_array(j);
	phoneui_json_end_object(j);
	phoneui_json_end_object(j);
	phoneui_send(ui, phoneui_json_text(j));
	phoneui_json_free(j);
}

} // namespace

int main()
{
	load_catalogs();
	phoneui* ui = phoneui_open();
	if (!ui) {
		std::fprintf(stderr, "phone-app-hellocpp: not started by phone-shell\n");
		return 1;
	}
	int count = 0;
	std::string note;
	phoneui_msg m;
	while (phoneui_next(ui, &m) == 0) {
		std::string t = m.t ? m.t : "";
		if (t == "start" || t == "resync") {
			screen(ui, "reset", count, note);
		} else if (t == "action" && m.action) {
			std::string action = m.action;
			if (action == "inc") {
				count++;
			} else if (action == "copy") {
				phoneui_json* args = phoneui_json_new();
				phoneui_json_begin_object(args);
				phoneui_json_key(args, "text");
				phoneui_json_string(args, tr("Count %s", std::to_string(count)).c_str());
				phoneui_json_end_object(args);
				const char* err = nullptr;
				note = phoneui_call(ui, "clipboard.set", phoneui_json_text(args), nullptr, &err) == 0
				           ? tr("Copied to the clipboard")
				           : tr("Copy failed: %s", err ? err : "");
				phoneui_json_free(args);
			}
			screen(ui, "update", count, note);
		}
	}
	phoneui_close(ui);
	return 0;
}
