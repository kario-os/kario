package ui

import (
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// NFC card emulation (30-nfc.md). An app declares its cards in the
// manifest's "nfc" list (AID groups, as Android's HostApduService does);
// the system decides which card a reader sees and hands the reader's
// commands for one of the app's cards to it as "nfc.apdu" events, which
// it answers with nfc.reply. nfc.state and nfc.set are the card pack's
// (com.phone.cards) and Settings' (the system permission).
const (
	CapNFCReply = "nfc.reply"
	CapNFCState = "nfc.state"
	CapNFCSet   = "nfc.set"

	EventNFCAPDU = "nfc.apdu"
	// EventNFCDeactivated: the reader is done with the card an app's
	// commands came for (Android's HostApduService.onDeactivated).
	EventNFCDeactivated = "nfc.deactivated"

	// PermNFC lets an app declare cards; granted with the installation.
	PermNFC = "nfc"
)

// Reader mode for apps (Android's enableReaderMode, iOS's Core NFC):
// nfc.read shows the system sheet ("hold the card to the phone") and
// gives the app the card held there; an ISO-DEP card stays activated for
// nfc.transceive, in a session of at most NFCReaderHold seconds that the
// sheet shows and the user can end. Only the app in the foreground, with
// the runtime permission PermNFCReader.
const (
	CapNFCRead       = "nfc.read"
	CapNFCTransceive = "nfc.transceive"

	PermNFCReader = "nfc_reader"

	NFCReaderHold = 30

	// ErrNFCNoCard: nfc.read's time ran out with no card on the phone
	// (ErrCancelled when the user or the app ended it first).
	ErrNFCNoCard = "no card was held to the phone"
)

// NFCReadArgs: how long nfc.read waits for a card, in seconds (default
// 20, at most 60).
type NFCReadArgs struct {
	Seconds int `json:"seconds,omitempty"`
}

// NFCTag is the card nfc.read found: its UID (NFCID1), ATQA and SAK in
// hex, whether it speaks ISO-DEP, and for one that does, where the
// controller can keep it, the session nfc.transceive reaches it in until
// Until (Unix milliseconds).
type NFCTag struct {
	UID     string `json:"uid"`
	ATQA    string `json:"atqa"`
	SAK     string `json:"sak"`
	ISODep  bool   `json:"iso_dep"`
	Session string `json:"session,omitempty"`
	Until   int64  `json:"until,omitempty"`
}

// NFCTransceiveArgs: a command (hex) for the card of a reader session;
// Close ends the session, after the command if there is one.
type NFCTransceiveArgs struct {
	Session string `json:"session"`
	APDU    string `json:"apdu,omitempty"`
	Close   bool   `json:"close,omitempty"`
}

// NFCTransceiveRes is the card's response, in hex, ending in its status
// word.
type NFCTransceiveRes struct {
	APDU string `json:"apdu,omitempty"`
}

// NFCDeactivated is the value of an "nfc.deactivated" event: after
// commands of Session reached the app's Card, the reader left
// (NFCDeactivatedLinkLoss) or selected another AID
// (NFCDeactivatedDeselected).
type NFCDeactivated struct {
	Session string `json:"session"`
	Card    string `json:"card"`
	Reason  string `json:"reason"`
}

const (
	NFCDeactivatedLinkLoss   = "link_loss"
	NFCDeactivatedDeselected = "deselected"
)

// Card categories, as Android's: a payment card answers only for the
// default payment app and only on an unlocked phone; other cards (transit,
// loyalty, access) for the app that declares the AID.
const (
	NFCPayment = "payment"
	NFCOther   = "other"
	// NFCDoor is the kind of a door card the system copied (not a manifest
	// category).
	NFCDoor = "door"
)

// NFCCard is one entry of a manifest's "nfc" list: the AIDs a reader
// selects it by. RequiresUnlock keeps it silent while the phone is locked
// (payment cards always are); Express lets the user make it the express
// transit card, which answers with the screen off and the phone locked.
// Locales gives the description in other languages.
type NFCCard struct {
	ID             string            `json:"id"`
	Category       string            `json:"category"`
	Description    string            `json:"description"`
	AIDs           []string          `json:"aids"`
	RequiresUnlock bool              `json:"requires_unlock,omitempty"`
	Express        bool              `json:"express,omitempty"`
	Locales        map[string]string `json:"locales,omitempty"`
}

const (
	MaxNFCCards = 8
	MaxNFCAIDs  = 16
)

// PPSE is the proximity payment system environment's AID
// ("2PAY.SYS.DDF01"), the directory a payment terminal selects first.
const PPSE = "325041592E5359532E4444463031"

var nfcCardID = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,15}$`)

// ParseAID reads an AID in hex: 5 to 16 bytes (ISO/IEC 7816-4).
func ParseAID(s string) ([]byte, error) {
	b, err := hex.DecodeString(s)
	if err != nil || len(b) < 5 || len(b) > 16 {
		return nil, fmt.Errorf("AID %q: 5 to 16 bytes in hex", s)
	}
	return b, nil
}

// CheckNFCCards is the installer's and the store's check of a manifest's
// cards; lang says whether a locale is one the system offers.
func CheckNFCCards(cards []NFCCard, permissions []string, lang func(string) bool) error {
	if len(cards) == 0 {
		return nil
	}
	declared := false
	for _, p := range permissions {
		declared = declared || p == PermNFC
	}
	if !declared {
		return fmt.Errorf("nfc cards need the %q permission", PermNFC)
	}
	if len(cards) > MaxNFCCards {
		return fmt.Errorf("at most %d nfc cards", MaxNFCCards)
	}
	text := func(what, s string) error {
		if n := utf8.RuneCountInString(strings.TrimSpace(s)); n == 0 || n > 32 || strings.ContainsAny(s, "\n\t") {
			return fmt.Errorf("%s: 1–32 characters on one line", what)
		}
		return nil
	}
	ids, aids := map[string]bool{}, map[string]string{}
	for _, c := range cards {
		if !nfcCardID.MatchString(c.ID) || ids[c.ID] {
			return fmt.Errorf("nfc card %q: a unique id of lower-case letters, digits, - and _ (at most 16)", c.ID)
		}
		ids[c.ID] = true
		if c.Category != NFCPayment && c.Category != NFCOther {
			return fmt.Errorf("nfc card %s: category is %s or %s", c.ID, NFCPayment, NFCOther)
		}
		if c.Express && c.Category != NFCOther {
			return fmt.Errorf("nfc card %s: only %s cards can be express transit cards", c.ID, NFCOther)
		}
		if err := text("nfc card "+c.ID+": description", c.Description); err != nil {
			return err
		}
		for l, d := range c.Locales {
			if !lang(l) {
				return fmt.Errorf("nfc card %s: locale %q is not offered", c.ID, l)
			}
			if err := text("nfc card "+c.ID+": locales."+l, d); err != nil {
				return err
			}
		}
		if len(c.AIDs) == 0 || len(c.AIDs) > MaxNFCAIDs {
			return fmt.Errorf("nfc card %s: 1 to %d AIDs", c.ID, MaxNFCAIDs)
		}
		for _, a := range c.AIDs {
			b, err := ParseAID(a)
			if err != nil {
				return fmt.Errorf("nfc card %s: %w", c.ID, err)
			}
			key := strings.ToUpper(hex.EncodeToString(b))
			if key == PPSE && c.Category != NFCPayment {
				return fmt.Errorf("nfc card %s: the PPSE belongs to payment cards", c.ID)
			}
			if other, dup := aids[key]; dup {
				return fmt.Errorf("nfc card %s: AID %s is also in %s", c.ID, key, other)
			}
			aids[key] = c.ID
		}
	}
	return nil
}

// NFCAPDU is the value of an "nfc.apdu" event: a command from the reader
// for one of the app's cards (its manifest id), after the reader selected
// AID. The app answers each command with nfc.reply on the same session,
// a response APDU that ends with its status word (9000: done).
type NFCAPDU struct {
	Session string `json:"session"`
	Card    string `json:"card"`
	AID     string `json:"aid"`
	APDU    string `json:"apdu"` // hex
}

type NFCReplyArgs struct {
	Session string `json:"session"`
	APDU    string `json:"apdu"` // hex
}

// NFCState is nfc.state's answer: the controller, the cards in the order
// the phone tries them, and the user's choices.
type NFCState struct {
	// Controller names the hardware ("" without NFC); Simulated: no NFC
	// chip, the developer machine's or the emulator's simulated one.
	Controller string        `json:"controller"`
	Simulated  bool          `json:"simulated"`
	Observe    bool          `json:"observe"`
	Enabled    bool          `json:"enabled"`
	Cards      []NFCCardInfo `json:"cards"`
	// DefaultPayment is the payment app's id; Foreground lets the payment
	// app in the foreground pay instead (Android's "except when another
	// payment app is open").
	DefaultPayment string   `json:"default_payment"`
	PaymentApps    []NFCApp `json:"payment_apps"`
	Express        string   `json:"express"`
	Foreground     bool     `json:"foreground"`
	// DoorFirst is the door card a door gets first now (by the time of
	// use, else the order).
	DoorFirst string `json:"door_first,omitempty"`
	// Read is the answer to nfc.set "read" only.
	Read *NFCRead `json:"read,omitempty"`
}

// NFCRead is the card held to the phone, read in reader mode: Result is
// one of the NFCRead* values; UID the last two digits of a card that can
// be added (nfc.set "add_door" adds it), Name the card it already is.
type NFCRead struct {
	Result string `json:"result"`
	UID    string `json:"uid,omitempty"`
	Name   string `json:"name,omitempty"`
}

const (
	NFCReadOK        = "ok"
	NFCReadNone      = "none"      // no card within the time
	NFCReadEncrypted = "encrypted" // MIFARE Classic with its own keys
	NFCReadSmart     = "smart"     // a CPU card (ISO-DEP): its issuer's app adds it
	NFCReadDuplicate = "duplicate" // already in the card pack (Name)
)

// NFCHours is a span of the day a door card is usually tapped in, on
// workdays or (Weekend) on days off, minutes after midnight. Days off are
// Saturdays and Sundays, and with the region's holiday calendar its
// public holidays, but not the weekend days it makes workdays.
type NFCHours struct {
	Weekend bool `json:"weekend,omitempty"`
	From    int  `json:"from"`
	To      int  `json:"to"`
}

type NFCApp struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// NFCCardInfo is one card as the system lists it: a door card the system
// keeps (Kind "door", UID the last digits only) or an app's (ID
// "<app>/<card>").
type NFCCardInfo struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Kind    string `json:"kind"` // door | payment | other
	App     string `json:"app,omitempty"`
	AppName string `json:"app_name,omitempty"`
	UID     string `json:"uid,omitempty"`
	// CanExpress: an app's card that may be the express transit card.
	CanExpress bool `json:"can_express,omitempty"`
	// Locked: it answers with the phone locked and the screen off (door
	// cards and the express transit card unless the user says otherwise);
	// LockedFixed: never (payment cards, cards their app keeps for an
	// unlocked phone), whatever nfc.set "locked" says.
	Locked      bool `json:"locked,omitempty"`
	LockedFixed bool `json:"locked_fixed,omitempty"`
	// Usual: when a door card is usually tapped, in the order of the day,
	// workdays first.
	Usual []NFCHours `json:"usual,omitempty"`
}

// nfc.set keys.
const (
	NFCSetEnabled    = "enabled"            // On
	NFCSetPayment    = "default_payment"    // App
	NFCSetForeground = "foreground_payment" // On
	NFCSetExpress    = "express"            // Card ("" for none)
	NFCSetMove       = "move"               // Card, By (-1 up, +1 down)
	NFCSetLocked     = "locked"             // Card, On: answers locked with the screen off
	// The card pack's door cards: read the card held to the phone
	// (Seconds, reader mode; the answer's Read), stop that read early
	// (its answer is then "none"), add the card just read (Name), rename
	// or delete one (Card, Name).
	NFCSetRead       = "read"
	NFCSetCancelRead = "cancel_read"
	NFCSetAddDoor    = "add_door"
	NFCSetRename     = "rename"
	NFCSetDelete     = "delete"
	// NFCSetPresent presents Card to readers for a minute with the
	// system's countdown ("" stops); NFCSetOpen opens the card pack.
	NFCSetPresent = "present"
	NFCSetOpen    = "open_cards"
)

type NFCSetArgs struct {
	Key     string `json:"key"`
	On      bool   `json:"on,omitempty"`
	App     string `json:"app,omitempty"`
	Card    string `json:"card,omitempty"`
	By      int    `json:"by,omitempty"`
	Name    string `json:"name,omitempty"`
	Seconds int    `json:"seconds,omitempty"`
}
