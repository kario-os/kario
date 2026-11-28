package ui

// Why apn.set refuses: the error ends in one of these, telephonyd's words
// (telephonyd/src/apn.hpp), which Settings says in the user's language.
// Any other refusal is a request Settings does not make (a type, IP type
// or authentication it does not offer, an unknown member) or the service
// failing.
const (
	ErrAPNSIMUnknown   = "the SIM's network is not known yet"
	ErrAPNFull         = "a SIM has at most 32 APNs: delete some first"
	ErrAPNGone         = "no APN with that id"
	ErrAPNNotInternet  = "not an internet APN"
	ErrAPNNoName       = "name: empty"
	ErrAPNNameLong     = "name: at most 64 bytes"
	ErrAPNBadAPN       = "apn: letters, digits, hyphens and dots, 100 at most"
	ErrAPNNeedsUser    = "auth: needs a user"
	ErrAPNUserLong     = "user: at most 64 bytes"
	ErrAPNPasswordLong = "password: at most 64 bytes"
	ErrAPNMMSCLong     = "mmsc: at most 256 bytes"
	ErrAPNMMSProxyLong = "mms_proxy: at most 255 bytes"
	// After the member's name: "user: no control characters".
	ErrAPNControl = "no control characters"
)

// ErrNoService: the Shell's own refusal of apn.list and apn.set, before
// telephonyd is asked: there is no telephonyd (the status bar's "No
// service"). The Shell says it in the user's language, the words of its
// catalog for this message, and Settings shows them as they come.
const ErrNoService = "No service"
