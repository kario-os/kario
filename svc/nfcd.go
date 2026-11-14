package svc

// nfcd: the NFC controller, the cards and which one a reader sees
// (30-nfc.md).
const (
	Nfcd     = "nfcd"
	NfcdSock = Nfcd + ".sock"
)

const (
	// NFCState: the controller, the cards in order and the user's choices.
	NFCState = "state"
	// NFCPolicy is the shell's side of the decision: locked, screen on,
	// the foreground app, the cards apps declare, the shell's clock.
	NFCPolicy = "policy"
	// NFCSet takes ui.NFCSetArgs: the card pack's and Settings' changes,
	// reading a door card ("read", reader mode) and adding it.
	NFCSet = "set"
	// NFCPresent presents one card for a while (the quick card view), ""
	// stops.
	NFCPresent = "present"
	// NFCReply answers the reader's command an app got.
	NFCReply = "apdu.reply"
	// An app's reader session (ui.CapNFCRead), one at a time: opened for
	// the app, it waits for a card held to the phone, exchanges commands
	// with an ISO-DEP one until it ends, and is closed (all NFCReaderArgs).
	NFCReaderOpen     = "reader.open"
	NFCReaderWait     = "reader.wait"
	NFCReaderExchange = "reader.exchange"
	NFCReaderClose    = "reader.close"
	// The simulated controller's world: a reader the phone is held to, a
	// card held to the phone.
	NFCSimTap  = "sim.tap"
	NFCSimCard = "sim.card"

	// Events: a reader's field coming or going, a reader's command for an
	// app's card, how a tap went, the reader done with an app's card
	// (Session, App, Card, Reason: ui.NFCDeactivated*), and a reader
	// session ending (Session, App, Reason: "closed", "done", "timeout",
	// "lost").
	NFCEventField       = "field"
	NFCEventAPDU        = "apdu"
	NFCEventResult      = "result"
	NFCEventDeactivated = "deactivated"
	NFCEventReaderEnd   = "reader.end"
)

// NFCReaderArgs: a reader session (Session, from NFCReaderOpen) of App;
// Seconds, how long NFCReaderWait waits for a card; APDU, a command in
// hex for NFCReaderExchange (answered with ui.NFCTransceiveRes).
type NFCReaderArgs struct {
	Session string `json:"session,omitempty"`
	App     string `json:"app"`
	Seconds int    `json:"seconds,omitempty"`
	APDU    string `json:"apdu,omitempty"`
}

// NFCPolicyArgs is what the shell tells nfcd whenever it changes.
type NFCPolicyArgs struct {
	Locked     bool         `json:"locked"`
	ScreenOn   bool         `json:"screen_on"`
	Foreground string       `json:"foreground"`
	Cards      []NFCAppCard `json:"cards"`
	// The shell's clock, which door cards are chosen by the time of day
	// of: its skew from the system clock (a selftest's timeshift) and its
	// time zone's offset east of UTC now, in seconds.
	ClockSkewMS int64 `json:"clock_skew_ms,omitempty"`
	ZoneOffset  int   `json:"zone_offset"`
	// The region's holiday calendar (ota.Holidays), dates as YYYY-MM-DD:
	// public holidays, and weekend days that are workdays (China's
	// make-up workdays). Without one, Monday to Friday are workdays.
	DaysOff  []string `json:"days_off,omitempty"`
	Workdays []string `json:"workdays,omitempty"`
}

// NFCAppCard is a card an app's manifest declares, its description in
// the device's language.
type NFCAppCard struct {
	App            string   `json:"app"`
	AppName        string   `json:"app_name"`
	Card           string   `json:"card"`
	Name           string   `json:"name"`
	Category       string   `json:"category"`
	AIDs           []string `json:"aids"`
	RequiresUnlock bool     `json:"requires_unlock,omitempty"`
	Express        bool     `json:"express,omitempty"`
}

// NFCEvent is an nfcd event. field: On, a reader's field came (the
// controller's interrupt wakes a suspended phone for it) or went. apdu:
// Session, App, Card (the manifest id), AID, APDU (hex). result, when a
// reader is done (or the lock kept the card it wanted silent): OK, Card
// and Name; on a failure the card the next tap presents (Next, NextName)
// and Reason: "locked" or "screen" (the card was not allowed to answer),
// "timeout" (its app did not answer in time), or what went wrong.
type NFCEvent struct {
	Event    string `json:"event"`
	On       bool   `json:"on,omitempty"`
	Session  string `json:"session,omitempty"`
	App      string `json:"app,omitempty"`
	Card     string `json:"card,omitempty"`
	AID      string `json:"aid,omitempty"`
	APDU     string `json:"apdu,omitempty"`
	OK       bool   `json:"ok,omitempty"`
	Name     string `json:"name,omitempty"`
	Kind     string `json:"kind,omitempty"`
	Next     string `json:"next,omitempty"`
	NextName string `json:"next_name,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

// NFCTapArgs is a simulated reader the phone is held to: a door that
// accepts UIDs (hex), a transit gate charging Fare from the card with
// AID, a payment terminal charging Amount to it.
type NFCTapArgs struct {
	Kind       string   `json:"kind"` // door | gate | pay
	Name       string   `json:"name"`
	Accept     []string `json:"accept,omitempty"`
	AID        string   `json:"aid,omitempty"`
	Fare       int      `json:"fare,omitempty"`
	Amount     int      `json:"amount,omitempty"`
	PatienceMS int      `json:"patience_ms,omitempty"`
}

// NFCTapRes: what the reader made of the tap and which card the phone
// presented.
type NFCTapRes struct {
	OK   bool   `json:"ok"`
	Note string `json:"note"`
	Card string `json:"card"`
	Name string `json:"name"`
}
