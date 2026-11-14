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

	// Events: a message came in or changed (its delivery state); messages
	// went or changed otherwise (read, deleted, blocked); an emergency
	// alert came.
	SMSEventMessage = "message"
	SMSEventChanged = "changed"
	SMSEventAlert   = "alert"
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

// SMSUnnotifiedRes is SMSUnnotified's answer, oldest first.
type SMSUnnotifiedRes struct {
	Messages []ui.SMS      `json:"messages"`
	Alerts   []ui.SMSAlert `json:"alerts"`
}

// SMSStateRes is SMSState's answer.
type SMSStateRes struct {
	Pid       int  `json:"pid"`
	Messages  int  `json:"messages"`
	Alerts    int  `json:"alerts"`
	Telephony bool `json:"telephony"`
}
