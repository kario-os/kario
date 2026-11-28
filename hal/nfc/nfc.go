// Package nfc is the NFC controller under phone-nfcd: card emulation (the
// phone answers a reader as a card), reader mode (the phone reads a card
// held to it) and Android 15's observe mode (the controller reports the
// reader's polling loop and stays silent until the host says what to
// answer as, so the phone can pick the card for that reader).
//
// The phone answers as one identity at a time: a UID card (ISO 14443-3A
// with a fixed UID, how Chinese phone wallets copy an unencrypted door
// card) or host card emulation (ISO-DEP, ISO 14443-4, with a random UID:
// the reader's SELECT AID and every command after it reach the host).
//
// Backends: Sim, for the developer machine and QEMU, with simulated
// readers and cards; NCI, the shape of a real controller (NXP PN7160 /
// PN553 class) on a raw NCI device, not run on hardware yet.
package nfc

import (
	"context"
	"crypto/rand"
	"errors"
)

// Controller is an NFC controller.
type Controller interface {
	// Events are what happens in the field, in order.
	Events() <-chan Event
	// Present is what the phone answers a reader as from now on; nil
	// answers nothing (with observe mode the polling loop is still
	// reported).
	Present(id *Identity) error
	// Respond answers the reader's last command (EventAPDU).
	Respond(apdu []byte) error
	// Read polls for a card held to the phone (reader mode) until one
	// answers or ctx ends (ErrNoCard).
	Read(ctx context.Context) (Tag, error)
	Info() Info
	Close() error
}

// Exchanger is a controller that keeps an ISO-DEP card it read activated
// for commands, as Android's IsoDep and Core NFC's NFCISO7816Tag do: an
// app's reader session. Only the simulated controller is one so far (on
// NCI it needs the poll-mode data connection, not written yet).
type Exchanger interface {
	// Exchange sends a command to the card read last and returns its
	// response; ErrLost once that card is gone.
	Exchange(cmd []byte) ([]byte, error)
	// Release deactivates the card.
	Release()
}

type Info struct {
	Name string
	// Observe: the controller reports polling loops before answering.
	Observe   bool
	Simulated bool
}

// Identity is what the phone answers as: a UID card (UID set: SAK 0x08,
// no ISO-DEP) or, with HCE, ISO-DEP with a random UID; both at once, a
// fixed UID with ISO-DEP, is a dual-interface card (a reader that reads
// UIDs reads it, one that speaks ISO-DEP reaches the host). AIDs is the
// routing table a real controller gets for HCE; whatever the reader
// selects reaches the host, which answers unknown AIDs itself.
type Identity struct {
	UID  []byte
	HCE  bool
	AIDs [][]byte
}

type EventType string

const (
	EventField       EventType = "field"       // On: a reader's field came or went
	EventFrame       EventType = "frame"       // Frame: one of the reader's polling loop
	EventActivated   EventType = "activated"   // the reader selected the card (ISODep: as HCE)
	EventDeactivated EventType = "deactivated" // Sleep: the reader halted (HLTA) or deselected it
	EventAPDU        EventType = "apdu"        // APDU: a command for the host
)

type Event struct {
	Type   EventType
	On     bool
	Frame  PollingFrame
	ISODep bool
	UID    []byte
	Sleep  bool
	APDU   []byte
}

// FrameType is what a polling frame is, as Android's PollingFrame names
// them: the field switching on or off within the loop, a poll of a
// technology, or a frame the controller does not know (a reader's vendor
// frame, like Apple's ECP).
type FrameType string

const (
	FrameOn     FrameType = "on"
	FrameOff    FrameType = "off"
	FrameA      FrameType = "A"
	FrameB      FrameType = "B"
	FrameF      FrameType = "F"
	FrameCustom FrameType = "custom"
)

type PollingFrame struct {
	Type FrameType `json:"type"`
	Data []byte    `json:"data,omitempty"`
}

// Tag is a card read in reader mode. Encrypted: a MIFARE Classic card
// whose sectors do not open with the transport keys, known to be one; a
// door card like that cannot be copied by its UID alone.
type Tag struct {
	UID       []byte  `json:"uid"`
	ATQA      [2]byte `json:"atqa"`
	SAK       byte    `json:"sak"`
	ISODep    bool    `json:"iso_dep"`
	Encrypted bool    `json:"encrypted"`
}

var (
	ErrNoCard    = errors.New("no card")
	ErrNotISODep = errors.New("the card does not speak ISO-DEP")
	ErrLost      = errors.New("the reader went away")
)

// RandomUID is the 4-byte random NFCID1 (first byte 0x08) a card without
// a fixed identity answers with, new for every field.
func RandomUID() []byte {
	b := make([]byte, 4)
	_, _ = rand.Read(b[1:])
	b[0] = 0x08
	return b
}

// Status words.
var (
	SWOK           = []byte{0x90, 0x00}
	SWNotFound     = []byte{0x6A, 0x82}
	SWUnknown      = []byte{0x6F, 0x00}
	SWConditions   = []byte{0x69, 0x85}
	SWInsufficient = []byte{0x94, 0x01}
)

// SW is a response's status word (0 for one too short).
func SW(resp []byte) uint16 {
	if len(resp) < 2 {
		return 0
	}
	return uint16(resp[len(resp)-2])<<8 | uint16(resp[len(resp)-1])
}

// SWDone: success, or a warning (61xx more data, 62xx and 63xx), not an
// error.
func SWDone(sw uint16) bool {
	return sw == 0x9000 || sw>>8 == 0x61 || sw>>8 == 0x62 || sw>>8 == 0x63
}

// SelectAID is the AID of a SELECT by name (00 A4 04 xx), or nil.
func SelectAID(apdu []byte) []byte {
	if len(apdu) < 5 || apdu[1] != 0xA4 || apdu[2] != 0x04 || apdu[0]&0x80 != 0 {
		return nil
	}
	n := int(apdu[4])
	if n == 0 || len(apdu) < 5+n {
		return nil
	}
	return apdu[5 : 5+n]
}

// Select is the SELECT by name of aid, first or only occurrence.
func Select(aid []byte) []byte {
	return append(append([]byte{0x00, 0xA4, 0x04, 0x00, byte(len(aid))}, aid...), 0x00)
}
