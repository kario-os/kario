// The modem's IMS (VoLTE) registration. On 4G and 5G a call needs IMS or a
// fallback to 2G or 3G (CSFB); which one the modem takes is its own. The
// registration comes from a program the board's SoC glue installs
// (phone-qcom-ims on Qualcomm phones, through QMI's IMSA service): it
// prints one of registered, registering, not-registered or
// limited-registered and exits 0, or exits non-zero with why.
#pragma once

#include <string>
#include <string_view>

#include "modem.hpp"

namespace tel {

struct ImsReading {
    std::string state;  // one of the program's words, "" when it gave none
    std::string error;  // with no state: why, the program's first line
};

ImsReading ims_reading(bool ok, std::string_view output);

// One of the program's words, or unknown: what Status::ims may say.
bool valid_ims(std::string_view state);

// The IMS registration an ended call is put down to: ims when the call
// went out (not an emergency call), never rang at the other end nor
// connected, was neither hung up here nor busy, and ended on 4G or 5G
// (rat) with IMS not registered; "" when its end says nothing of IMS.
std::string ims_blame(const Call& c, bool alerted, std::string_view rat, std::string_view ims);

}  // namespace tel
