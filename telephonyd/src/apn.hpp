// Access point names: what the data link asks the network for. The
// carriers' come from a database (apns.json: Android's apns-conf.xml in
// JSON), chosen by the SIM's home network (MCC and MNC) and, for a virtual
// operator that needs others on that network, by its service provider name
// (SPN) or group identifier (GID1). The user's changes are kept per carrier
// on top of the database's (ApnStore).
#pragma once

#include <expected>
#include <map>
#include <optional>
#include <string>
#include <string_view>
#include <vector>

#include "json.hpp"

namespace tel {

// What of the SIM chooses its APNs: its home network, EF_SPN, EF_GID1 in hex.
struct SimId {
    std::string mcc, mnc, spn, gid1;
    // The key of the user's changes: MCC and MNC, "" while unknown.
    std::string carrier() const { return mcc.empty() || mnc.empty() ? "" : mcc + mnc; }
};

struct Apn {
    std::string id, name, apn;
    // default (the internet), mms, ims, ia (the LTE attach), supl, xcap, emergency
    std::vector<std::string> types;
    std::string ip_type = "ipv4v6";  // ipv4 | ipv6 | ipv4v6
    std::string auth = "none";       // none | pap | chap | pap-chap (either)
    std::string user, password;
    // For MMS: apn.mms gives them to phone-smsd.
    std::string mmsc, mms_proxy;
    int mms_port = 0;
    bool edited = false;  // the user added or changed it

    bool has(std::string_view type) const;
    bool operator==(const Apn&) const = default;
    // What the data link asks the network for is the same: the APN, its IP
    // type, authentication, user and password (a new name is not a new link).
    bool same_link(const Apn& o) const;
    // Without the password (its presence as password_set) unless asked:
    // it leaves telephonyd only for its own file.
    json::Value to_json(bool password = false) const;
    // Every member checked; an unknown one is an error. Members absent
    // keep their defaults; edited is not read.
    static std::expected<Apn, std::string> from_json(const json::Value& v);
};

class ApnDb {
public:
    struct Carrier {
        std::string name, mcc, spn, gid1;
        std::vector<std::string> mnc;
        std::vector<Apn> apns;
    };
    static std::expected<ApnDb, std::string> parse(std::string_view json);
    static std::expected<ApnDb, std::string> load(const std::string& path);
    // The SIM's carrier: a virtual operator's entry whose SPN (any case) or
    // GID1 (a prefix) the SIM has, else the network's own; nullptr for none.
    const Carrier* find(const SimId& sim) const;
    size_t size() const { return carriers_.size(); }

private:
    std::vector<Carrier> carriers_;
};

// What the modem needs of the SIM's APNs: the internet APN (an empty apn:
// the network's default); the LTE attach APN (the selected internet APN
// when it is of type ia, else the first ia one; none: the modem keeps its
// own); the MMS APN (apn.mms's) when it is another access point than the
// internet one (none: MMS goes over the data link); the IMS and emergency
// APNs (lower case), whose bearers other software brings up (81voltd for
// VoLTE, docs/39 §11.1) and telephonyd leaves alone.
struct ApnPlan {
    Apn internet;
    std::optional<Apn> attach, mms;
    std::vector<std::string> ims;
    bool operator==(const ApnPlan&) const = default;
};

// The APNs of the SIM in the phone and the one the data link uses.
struct ApnList {
    std::string carrier;  // its name, "" when the database has none
    std::vector<Apn> apns;
    std::string selected;  // the internet APN in use, "" for none
    bool edited = false;   // the user's copy of the list
    // The selected APN; none: the network's default.
    const Apn* internet() const;
    ApnPlan plan() const;
    json::Value to_json() const;
};

// The most APNs a SIM's list has, the carrier's and the user's own: one of
// each of the seven types is 7, a carrier in apns.json has 3 at most, and
// every save sends the whole list to Settings and writes it to disk.
inline constexpr size_t kMaxApns = 32;

// Why apn.set refuses: its error ends in one of these, which Settings says
// in the user's language (ui.ErrAPN*, the same words). Literals: Settings'
// tests read them here. Any other refusal is a request Settings does not make.
namespace refused {
inline constexpr const char* kSimUnknown = "the SIM's network is not known yet";
// A list read back over kMaxApns needs more than one delete.
inline constexpr const char* kFull = "a SIM has at most 32 APNs: delete some first";
inline constexpr const char* kGone = "no APN with that id";
inline constexpr const char* kNotInternet = "not an internet APN";
inline constexpr const char* kNoName = "name: empty";
inline constexpr const char* kNameLong = "name: at most 64 bytes";
inline constexpr const char* kBadApn = "apn: letters, digits, hyphens and dots, 100 at most";
inline constexpr const char* kNeedsUser = "auth: needs a user";
inline constexpr const char* kUserLong = "user: at most 64 bytes";
inline constexpr const char* kPasswordLong = "password: at most 64 bytes";
inline constexpr const char* kMmscLong = "mmsc: at most 256 bytes";
inline constexpr const char* kMmsProxyLong = "mms_proxy: at most 255 bytes";
// After the member's name: "user: no control characters".
inline constexpr const char* kControl = "no control characters";
}  // namespace refused

// The database's APNs until the user changes one; then the user's copy of
// that carrier's list, which reset drops. The choice of the internet APN is
// the user's too (the first internet APN until then). A copy read back with
// more than kMaxApns (an older telephonyd's, one written by hand) is kept
// whole, as the internet APN chosen may be any of it: only adding waits
// until deletes bring it under.
class ApnStore {
public:
    using Done = std::expected<void, std::string>;
    // path "" keeps nothing on disk.
    explicit ApnStore(std::string path = {});
    ApnList list(const ApnDb& db, const SimId& sim) const;
    // Adds an APN (no id; refused with kMaxApns in the list) or changes
    // one; the answer is its id. A change without a password keeps the old one.
    std::expected<std::string, std::string> save(const ApnDb& db, const SimId& sim, Apn apn, bool password);
    Done remove(const ApnDb& db, const SimId& sim, std::string_view id);
    Done select(const ApnDb& db, const SimId& sim, std::string_view id);
    Done reset(const SimId& sim);

private:
    struct Record {
        std::optional<std::vector<Apn>> apns;
        std::string selected;
        int next = 1;
    };
    Record* own(const ApnDb& db, const SimId& sim);
    void load();
    void persist() const;

    std::map<std::string, Record> carriers_;
    std::string path_;
};

}  // namespace tel
