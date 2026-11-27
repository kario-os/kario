package svc

import "github.com/kario-os/kario/ui"

// smsd: the messages, their conversations, the block list and the
// emergency alerts received (32-messaging.md). It takes what telephonyd
// receives and acknowledges it once stored.
const (
	Smsd     = "smsd"
	SmsdSock = Smsd + ".sock"
)

// telephonyd's side of the hand-off (with TelSMSSend, TelEventSMS and
// TelEventCB): what smsd has not acknowledged (received messages, cell
// broadcasts, outgoing messages' outcomes), each with its key;
// TelSMSAck {keys} lets them go. TelSMSLegacy: the messages telephonyd
// kept before smsd, until TelSMSLegacyDrop.
const (
	TelSMSPending    = "sms.pending"
	TelSMSAck        = "sms.ack"
	TelSMSLegacy     = "sms.legacy"
	TelSMSLegacyDrop = "sms.legacy_drop"
)

// TelAPNMMS asks telephonyd for the SIM's MMS access point: the APN of
// type mms its APN database (and the user's changes) has for the SIM,
// answered as that database writes an APN (its other members are not
// read), or an error for none, when smsd falls back to the defaults of
// the SIM's operator (programs/smsd/apn.go).
const TelAPNMMS = "apn.mms"

// MMSAPN is where multimedia messages go: the MMSC's URL and the proxy
// (host and port, "" and 0 for none) on the access point APN.
type MMSAPN struct {
	APN   string `json:"apn,omitempty"`
	MMSC  string `json:"mmsc"`
	Proxy string `json:"mms_proxy,omitempty"`
	Port  int    `json:"mms_port,omitempty"`
}

// TelMMSConnect brings up the SIM's MMS access point where the operator
// keeps it apart from the internet one (China Mobile's cmwap beside
// cmnet), held three minutes from each call; TelMMSDisconnect lets it go.
// The answer is an MMSLink, or an error where the link may not be up
// (mobile data off, roaming where it may not) or did not connect.
const (
	TelMMSConnect    = "mms.connect"
	TelMMSDisconnect = "mms.disconnect"
)

// MMSLink is TelMMSConnect's answer: Shared when MMS goes over the data
// link, else the access point's interface and IPv4 addressing (as
// telephonyd's data_ip; its other members are not read). The Shell routes
// what leaves from that address over the link.
type MMSLink struct {
	Shared bool       `json:"shared"`
	Iface  string     `json:"iface,omitempty"`
	IPv4   *MMSLinkIP `json:"ipv4,omitempty"`
}

type MMSLinkIP struct {
	Address string   `json:"address"`
	DNS     []string `json:"dns,omitempty"`
}

const (
	// SMSSend {to, text}, or {id} to send a failed message again: stored
	// as sending, then handed to telephonyd.
	SMSSend = "send"
	// SMSList and SMSThreads take ui.SMSListArgs; SMSRead {peer} marks a
	// conversation read.
	SMSList    = "list"
	SMSThreads = "threads"
	SMSRead    = "read"
	// SMSDelete takes ui.SMSDeleteArgs, SMSBlock ui.SMSBlockArgs; SMSBlock
	// and SMSBlocked answer with the blocked numbers.
	SMSDelete  = "delete"
	SMSBlock   = "block"
	SMSBlocked = "blocked"
	SMSAlerts  = "alerts"
	// SMSUnnotified: the new messages and alerts the shell has not shown
	// yet (SMSUnnotifiedRes), kept until SMSNotified {ids}: what came while
	// the shell was not subscribed is shown when it is.
	SMSUnnotified = "unnotified"
	SMSNotified   = "notified"
	// SMSState: smsd's process id, its counts and whether telephonyd is
	// connected (selftest).
	SMSState = "state"
	// SMSImport stores messages from another phone (SMSImportArgs, at most
	// 500): read or unread as given, sent ones as sent, marked imported,
	// notifying nothing; SMSImportRes gives their IDs. SMSDeleteImported
	// {ids} deletes the imported ones among them (the import undone).
	SMSImport         = "import"
	SMSDeleteImported = "delete_imported"
	// SMSFlashSave {id} keeps a class 0 message the shell showed: it goes
	// into the inbox, read. Until then smsd holds it in memory only.
	SMSFlashSave = "flash.save"
	// SMSDownload {id} fetches a multimedia message whose download failed.
	SMSDownload = "download"
	// SMSPart {id, index} gives a part smsd keeps (SMSPartRes);
	// SMSPartMedia {id, index, media} records that the shell put it in the
	// media library as media ("media:<id>"), and smsd lets its copy go.
	SMSPart      = "part"
	SMSPartMedia = "part.media"
	// SMSRegion {region} is the region numbers written nationally are read
	// in (the shell's); SMSKnown {numbers} the contacts' numbers, for the
	// block list's "not in the contacts" (held in memory; the shell sends
	// them whenever it subscribes and they change).
	SMSRegion = "region"
	SMSKnown  = "known"
	// SMSBlockList answers ui.BlockList; SMSBlockSet takes ui.BlockSetArgs
	// and answers the list.
	SMSBlockList = "blocklist"
	SMSBlockSet  = "blockset"
	// SMSOnly {on, numbers}: minor mode's allowed numbers (the emergency
	// numbers among them); while on, a message from any other number is
	// blocked as the block list blocks (ui.BlockedMinor). Kept across a
	// restart, so a message arriving before the shell is up is held too.
	SMSOnly = "only"
	// SMSMMSSim {mmsc}: a test hook, the MMSC to send to (no proxy), taken
	// only while telephonyd runs the simulated baseband.
	SMSMMSSim = "mms.sim"
	// SMSDraft takes ui.SMSDraftArgs.
	SMSDraft = "draft"
	// SMSScheduleSim: a test hook, the scheduled messages sent now, taken
	// only while telephonyd runs the simulated baseband.
	SMSScheduleSim = "schedule.sim"

	// Events: a message came in or changed (its delivery state, a
	// multimedia message's download); messages went or changed otherwise
	// (read, deleted, blocked); an emergency alert came; a class 0 message
	// came, which the shell shows at once (SMS) and confirms with
	// SMSNotified.
	SMSEventMessage = "message"
	SMSEventChanged = "changed"
	SMSEventAlert   = "alert"
	SMSEventFlash   = "flash"
)

// SMSEvent is an smsd event. A message event's New is a message received
// for the inbox now, which the shell notifies (and then acknowledges with
// SMSNotified, as it does an alert it showed).
type SMSEvent struct {
	Event string       `json:"event"`
	SMS   *ui.SMS      `json:"sms,omitempty"`
	New   bool         `json:"new,omitempty"`
	Alert *ui.SMSAlert `json:"alert,omitempty"`
}

// SMSUnnotifiedRes is SMSUnnotified's answer, oldest first: Flash holds
// the class 0 messages not shown yet.
type SMSUnnotifiedRes struct {
	Messages []ui.SMS      `json:"messages"`
	Alerts   []ui.SMSAlert `json:"alerts"`
	Flash    []ui.SMS      `json:"flash"`
}

// SMSSendReq is SMSSend's: the ui.SMSSendArgs, with the pictures the
// shell read for the app (Parts: each one's media type, name, bytes and
// the library photo it came from).
type SMSSendReq struct {
	To      string      `json:"to"`
	Text    string      `json:"text"`
	ID      string      `json:"id,omitempty"`
	Subject string      `json:"subject,omitempty"`
	Parts   []SMSPartIn `json:"parts,omitempty"`
	At      int64       `json:"at,omitempty"`
	Zone    string      `json:"zone,omitempty"`
}

type SMSPartIn struct {
	Type  string `json:"type"`
	Name  string `json:"name,omitempty"`
	Data  []byte `json:"data"`
	Media string `json:"media,omitempty"`
}

// SMSPartArgs names a part of a message (SMSPart, SMSPartMedia).
type SMSPartArgs struct {
	ID    string `json:"id"`
	Index int    `json:"index"`
	Media string `json:"media,omitempty"`
}

// SMSPartRes is a part's bytes.
type SMSPartRes struct {
	Type string `json:"type"`
	Name string `json:"name,omitempty"`
	Data []byte `json:"data"`
}

// SMSImportArgs are SMSImport's: each message's Peer, Dir (in, out), Text,
// At (Unix seconds) and Read; a multimedia message's Kind, Subject and
// Members (a group's) too, and State failed for one that was not sent; the
// rest is smsd's to fill in.
type SMSImportArgs struct {
	Messages []ui.SMS `json:"messages"`
}

// SMSImportRes gives each message's new ID, by place; "" for one left out
// (unreadable, or one smsd has already: same conversation, direction, time
// and text).
type SMSImportRes struct {
	IDs []string `json:"ids"`
}

// SMSStateRes is SMSState's answer: also the multimedia messages waiting
// to be downloaded and the class 0 messages held.
type SMSStateRes struct {
	Pid       int  `json:"pid"`
	Messages  int  `json:"messages"`
	Alerts    int  `json:"alerts"`
	Telephony bool `json:"telephony"`
	MMSWait   int  `json:"mms_wait"`
	Flash     int  `json:"flash"`
}
