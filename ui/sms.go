package ui

import "unicode/utf16"

// SMSDraftArgs: sms.draft keeps Text as the draft of the conversation with
// Peer (one number or a group's, as sms.send takes them); an empty Text
// drops it. Sending to the conversation drops it too.
type SMSDraftArgs struct {
	Peer string `json:"peer"`
	Text string `json:"text"`
}

// SMSPartArgs names a part of a multimedia message (sms.vcard).
type SMSPartArgs struct {
	ID    string `json:"id"`
	Index int    `json:"index"`
}

// SMSOTPArgs: sms.otp waits up to Seconds (default SMSOTPWait, at most
// SMSOTPMaxWait) for a message with a verification code.
type SMSOTPArgs struct {
	Seconds int `json:"seconds,omitempty"`
}

// SMSOTP is the code the user agreed to hand over, and who sent it.
type SMSOTP struct {
	Code string `json:"code"`
	From string `json:"from"`
}

const (
	SMSOTPWait    = 120
	SMSOTPMaxWait = 300
)

// SMSFile is what sms.backup and sms.restore did: the file in Files, how
// many messages it holds or added, and (restore) how many it left out:
// already on the phone, unreadable, or not a message (a draft).
type SMSFile struct {
	File    string `json:"file"`
	Count   int    `json:"count"`
	Skipped int    `json:"skipped,omitempty"`
}

// SMSTypeVCard and SMSTypeXVCard are a contact card's media types in a
// multimedia message.
const (
	SMSTypeVCard  = "text/vcard"
	SMSTypeXVCard = "text/x-vcard"
)

// gsm7 is the GSM 03.38 default alphabet; gsm7Ext its extension table,
// whose characters take two septets.
const (
	gsm7    = "@£$¥èéùìòÇ\nØø\rÅåΔ_ΦΓΛΩΠΨΣΘΞÆæßÉ !\"#¤%&'()*+,-./0123456789:;<=>?¡ABCDEFGHIJKLMNOPQRSTUVWXYZÄÖÑÜ§¿abcdefghijklmnopqrstuvwxyzäöñüà"
	gsm7Ext = "^{}\\[~]|€\f"
)

// SMSSegments is how a text goes over the air: in how many parts, how many
// characters the last part still has room for, and whether it needs UCS-2
// (any character outside the GSM 7-bit alphabet: 70 a part, 67 when split)
// instead of GSM 7-bit (160, 153 when split).
func SMSSegments(text string) (parts, left int, ucs2 bool) {
	septets := 0
	for _, r := range text {
		switch {
		case containsRune(gsm7, r):
			septets++
		case containsRune(gsm7Ext, r):
			septets += 2
		default:
			ucs2 = true
		}
	}
	single, multi, n := 160, 153, septets
	if ucs2 {
		single, multi, n = 70, 67, len(utf16.Encode([]rune(text)))
	}
	switch {
	case n == 0:
		return 0, single, ucs2
	case n <= single:
		return 1, single - n, ucs2
	}
	parts = (n + multi - 1) / multi
	return parts, parts*multi - n, ucs2
}

func containsRune(s string, r rune) bool {
	for _, c := range s {
		if c == r {
			return true
		}
	}
	return false
}
