package ui

import "encoding/json"

// Capabilities an app can request with a "req" message. The shell is the
// broker: it identifies the caller by the socket it handed out, checks the
// manifest permission, asks the user for runtime consent where required, and
// forwards to the owning system service.
const (
	CapCallStart   = "media.call.start"
	CapCallEnd     = "media.call.end"
	CapCallControl = "media.call.control"
	CapScanQR      = "scan.qr"
	CapAuthConfirm = "auth.confirm"
	CapKeySign     = "keystore.sign"
	CapAttest      = "attest.get"
	CapPermList    = "perm.list"
	CapPermSet     = "perm.set"

	// Telephony (phone-telephonyd). An app without the "phone" permission
	// may still ask to dial: the system then asks the user first.
	CapTelDial   = "tel.dial"
	CapTelAnswer = "tel.answer"
	CapTelHangup = "tel.hangup"
	CapTelHold   = "tel.hold"
	CapTelMute   = "tel.mute"
	CapTelCalls  = "tel.calls"
	CapTelLog    = "tel.log"
	// Messages (phone-smsd, 32-messaging.md): sending, the messages and
	// their conversations, marking a conversation read, the emergency
	// alerts received. Deleting and the block list belong to the default
	// SMS app, the system's Messages, alone.
	CapSMSSend    = "sms.send"
	CapSMSList    = "sms.list"
	CapSMSRead    = "sms.read"
	CapSMSThreads = "sms.threads"
	CapSMSAlerts  = "sms.alerts"
	CapSMSDelete  = "sms.delete"
	CapSMSBlock   = "sms.block"
	CapSMSBlocked = "sms.blocked"
	// tel.ussd sends a USSD code (*100#) or the reply to the network's
	// menu, or ends the session; *#06# answers the IMEI from the phone
	// itself. System apps only: such codes can subscribe to paid services.
	CapTelUSSD = "tel.ussd"

	CapContacts = "contacts.list"

	// Audio through phone-audiod: apps play media from the library; the
	// focus policy pauses it under calls, ringtones and alarms.
	CapMediaList    = "media.list"
	CapAudioPlay    = "audio.play"
	CapAudioControl = "audio.control"
	CapAudioState   = "audio.state"
	CapAudioRoute   = "audio.route" // earpiece | speaker, during a call
	// The app's own audio through a shared ring (48 kHz mono S16, see
	// audio.RingBuf) that arrives with the reply: audio.stream plays what
	// the app writes (VoIP, voice messages, effects), audio.record fills it
	// from the microphone, only in the foreground or during a call.
	CapAudioStream = "audio.stream"
	CapAudioRecord = "audio.record"

	// Photos belong to the system's media library. camera.capture shows the
	// system viewfinder and stores what the user shoots; media.pick shows the
	// system picker and hands over only the chosen photo, as a read-only file
	// (no permission needed: choosing is the consent). media.list of images
	// needs the photos permission. Image nodes show a photo with src
	// "media:<id>" (the item's ID), drawn by the system.
	CapCameraCapture = "camera.capture"
	CapMediaPick     = "media.pick"
	// CapMediaSave stores an image in the album after the user agrees.
	CapMediaSave = "media.save"
	// Files: save one after the user agrees, pick one in the system's
	// list (read-only file in the reply), open one shared with the app.
	CapFileSave = "file.save"
	CapFilePick = "file.pick"
	CapFileOpen = "file.open"
	// CapAppSettings reads the app's own declared settings (manifest
	// "settings"); CapWidgetSet sets its standby line (manifest "widget").
	CapAppSettings = "app.settings"
	CapWidgetSet   = "widget.set"
	// System settings app: every app's declared settings, and changing one.
	CapSettingsApps   = "settings.apps"
	CapSettingsAppSet = "settings.app.set"
	// media.delete {id}: the system asks the user first (photos permission).
	CapMediaDelete = "media.delete"

	// Push: the token an app server uses to reach this app on this device
	// (through the gateway and phone-pushd). Messages arrive as "push"
	// events; see shell/push.go for when they wake the app.
	// End-to-end push keys: the public key for the app's server, and opening
	// data sealed to it. The private key stays in the keystore.
	CapPushKey      = "push.key"
	CapPushOpen     = "push.open"
	CapPushRegister = "push.register"

	// Packages (system store app only): install a signed .phoneapp from the
	// caller's data dir, uninstall, list what is installed. The system asks
	// the user in both cases.
	CapPkgInstall   = "pkg.install"
	CapPkgUninstall = "pkg.uninstall"
	CapPkgList      = "pkg.list"
	// CapPkgRevocations hands the system a store's signed takedown list
	// (the store app fetches it); the system verifies it and stops the
	// apps on it from starting.
	CapPkgRevocations = "pkg.revocations"
	// Intents: ask the app that handles an action (a manifest's intents) to
	// perform it, and get its answer; the handler answers with intent.reply.
	CapIntentInvoke = "intent.invoke"
	CapIntentReply  = "intent.reply"
	// CapPkgCatalog hands the system a store's signed catalogue (the store
	// app fetches it); the system checks the signature and the age and
	// gives back the apps, so the app shows nothing a mirror changed.
	CapPkgCatalog = "pkg.catalog"

	// Share sheet: the system lists the apps that accept the kind and opens
	// the user's choice with a "share:<kind>:<payload>" deep link.
	CapShareOpen = "share.open"

	// Location: coordinates for the foreground app (location permission).
	CapLocationGet = "location.get"
	// Background jobs: woken every N minutes (15 at least) within the wake
	// budget; a "job" event arrives.
	CapJobSchedule = "job.schedule"
	CapJobCancel   = "job.cancel"

	// The clipboard is write-only: apps copy text; pasting is done by the user
	// in an input field (the system's Paste menu entry).
	CapClipboardSet = "clipboard.set"

	// System settings (volumes, auto keypad lock) and the About phone facts;
	// system apps only.
	CapSettingsGet = "settings.get"
	CapSettingsSet = "settings.set"

	// System updates (A/B slots): status, check, install, reboot; system
	// apps only.
	CapSystemUpdate = "system.update"

	// Wi-Fi (iwd) and Bluetooth (BlueZ) for Settings; system apps only. The
	// state calls start a scan when the last one is old; Bluetooth pairing
	// asks the user to compare the code in a system dialog.
	CapWifiState   = "wifi.state"
	CapWifiSet     = "wifi.set"
	CapWifiConnect = "wifi.connect"
	CapWifiForget  = "wifi.forget"
	CapBTState     = "bt.state"
	CapBTSet       = "bt.set"
	CapBTPair      = "bt.pair"
	CapBTForget    = "bt.forget"
	// bt.send: the user picks a file from Files in the system's list and
	// it goes to a paired device (OBEX Object Push); the app never sees it.
	CapBTSend = "bt.send"

	// Airplane mode and mobile data, for Settings; system apps only.
	// Airplane mode turns the cellular radio, Wi-Fi and Bluetooth off (the
	// user may turn Wi-Fi and Bluetooth on again), an emergency call
	// turns it off; mobile data off stops the cellular data link.
	CapRadioState = "radio.state"
	CapRadioSet   = "radio.set"

	// The app's own web nodes: reload, forward, back, stop, zoom, find in
	// page, clear the app's website data; the page's state comes as the
	// "web" event.
	CapWebControl = "web.control"

	// The standby screen's picture: a photo the app may see, for the
	// unlocked phone, the lock screen or both; apps outside the system
	// image ask the user.
	CapWallpaperSet = "wallpaper.set"

	// Videos of the media library (media.list kind "video"), decoded by the
	// system: video.open starts a player whose stream the app shows in a
	// media node; video.control pauses, resumes, seeks, reads its state
	// or closes it. The system pauses a player whose stream is not on the
	// app's screen in the foreground and closes it with that screen.
	CapVideoOpen    = "video.open"
	CapVideoControl = "video.control"

	// Alarms fire from the system even when the app is frozen or stopped.
	CapAlarmSet    = "alarm.set"
	CapAlarmCancel = "alarm.cancel"
	CapAlarmList   = "alarm.list"
)

// Manifest permissions.
const (
	PermCamera     = "camera"
	PermMicrophone = "microphone"
	PermKeystore   = "keystore"
	PermAttest     = "attest"
	PermNotify     = "notifications"
	PermVoIP       = "voip"
	PermSystem     = "system"
	PermNet        = "net"
	PermPhone      = "phone"
	PermSMS        = "sms"
	PermContacts   = "contacts"
	PermPhotos     = "photos"
	PermLocation   = "location"
	PermInstall    = "install" // system image only (the store)
)

// RuntimePerms need explicit user consent on first use, in addition to being
// declared in the manifest. The labels are English; the system shows them in
// the device's language.
var RuntimePerms = map[string]string{
	PermCamera:     "Camera",
	PermMicrophone: "Microphone",
	PermContacts:   "Contacts",
	PermPhotos:     "Photos",
	PermLocation:   "Location",
	PermNFCReader:  "Reading NFC cards",
}

// MaxPurpose is the longest purpose a manifest may give a runtime
// permission (purposes: why the app asks, shown in the system's dialog).
const MaxPurpose = 80

// DeclaredPerms label the permissions granted with the installation, as the
// installer lists them.
var DeclaredPerms = map[string]string{
	PermNet:      "Network",
	PermNotify:   "Notifications",
	PermVoIP:     "Internet calls (answered in the background)",
	PermPhone:    "Phone calls (confirmed before each call)",
	PermSMS:      "SMS (confirmed before each message)",
	PermKeystore: "App keys",
	PermAttest:   "Device attestation",
	PermNFC:      "Contactless cards (NFC)",
}

// Telephony data as the shell passes it on (from phone-telephonyd).
type TelCall struct {
	ID        string `json:"id"`
	Number    string `json:"number"`
	Name      string `json:"name,omitempty"`
	Dir       string `json:"dir"`   // in | out
	State     string `json:"state"` // dialing | alerting | incoming | active | held | ended
	Reason    string `json:"reason,omitempty"`
	Started   int64  `json:"started"`
	Connected int64  `json:"connected"`
	Ended     int64  `json:"ended"`
	Muted     bool   `json:"muted"`
	// Emergency: an emergency number, which the modem calls even in
	// airplane mode (the system leaves airplane mode for it).
	Emergency bool `json:"emergency,omitempty"`
}

type TelLogEntry struct {
	Number   string `json:"number"`
	Name     string `json:"name,omitempty"`
	Dir      string `json:"dir"`
	Result   string `json:"result"` // answered | missed | rejected | busy | outgoing
	At       int64  `json:"at"`
	Duration int64  `json:"duration"`
}

// SMS is a message as phone-smsd keeps it. Thread is its conversation: the
// number written the one way smsd compares numbers. Folder is "" or
// SMSFolderBlocked (from a blocked number: kept apart, no notification).
// Code is a verification code smsd found in the text.
type SMS struct {
	ID     string `json:"id"`
	Peer   string `json:"peer"`
	Name   string `json:"name,omitempty"`
	Dir    string `json:"dir"`
	Text   string `json:"text"`
	At     int64  `json:"at"`
	Read   bool   `json:"read"`
	State  string `json:"state,omitempty"` // outgoing: SMSSending | SMSSent | SMSDelivered | SMSFailed
	Thread string `json:"thread,omitempty"`
	Folder string `json:"folder,omitempty"`
	Code   string `json:"code,omitempty"`
}

// An outgoing message's states (delivered: the recipient's phone took it,
// by the network's delivery report), and the blocked folder.
const (
	SMSSending       = "sending"
	SMSSent          = "sent"
	SMSDelivered     = "delivered"
	SMSFailed        = "failed"
	SMSFolderBlocked = "blocked"
)

// SMSListArgs: sms.list gives one conversation's messages (Peer, its
// number written any way), those whose text or number holds Query, or the
// blocked folder's (Folder); with none of them every message outside that
// folder. sms.threads takes Folder.
type SMSListArgs struct {
	Peer   string `json:"peer,omitempty"`
	Query  string `json:"query,omitempty"`
	Folder string `json:"folder,omitempty"`
}

// SMSThread is a conversation, newest first in sms.threads.
type SMSThread struct {
	Thread  string `json:"thread"`
	Peer    string `json:"peer"`
	Name    string `json:"name,omitempty"`
	Last    SMS    `json:"last"`
	Count   int    `json:"count"`
	Unread  int    `json:"unread"`
	Blocked bool   `json:"blocked,omitempty"`
}

// SMSDeleteArgs: messages by ID, or a whole conversation (Peer).
type SMSDeleteArgs struct {
	IDs  []string `json:"ids,omitempty"`
	Peer string   `json:"peer,omitempty"`
}

// SMSBlockArgs blocks a number (On) or unblocks it; sms.block and
// sms.blocked answer with the blocked numbers.
type SMSBlockArgs struct {
	Number string `json:"number"`
	On     bool   `json:"on"`
}

// SMSAlert is an emergency alert (a cell broadcast) phone-smsd received.
// Level comes from the channel it came on (3GPP TS 23.041).
type SMSAlert struct {
	ID      string `json:"id"`
	Level   string `json:"level"`
	Channel int    `json:"channel"`
	Text    string `json:"text"`
	At      int64  `json:"at"`
}

// Emergency alert levels, highest first. The highest cannot be switched
// off; the others can, in Settings (tests are off until switched on).
const (
	AlertPresidential = "presidential"
	AlertExtreme      = "extreme"
	AlertSevere       = "severe"
	AlertAmber        = "amber"
	AlertTest         = "test"
)

type DialArgs struct {
	Number string `json:"number"`
}

type CallIDArgs struct {
	CallID string `json:"call_id"`
	On     bool   `json:"on,omitempty"`
}

// SMSSendArgs: ID sends a message that was not sent again (the default
// SMS app; others always send a new one).
type SMSSendArgs struct {
	To   string `json:"to"`
	Text string `json:"text"`
	ID   string `json:"id,omitempty"`
}

type PeerArgs struct {
	Peer string `json:"peer,omitempty"`
}

type Contact struct {
	Name   string `json:"name"`
	Number string `json:"number"`
	Pinyin string `json:"pinyin,omitempty"` // for 9-key search
}

type MediaItem struct {
	ID     string `json:"id"`   // "demo:…" for audio, "media:p<N>" for photos
	Kind   string `json:"kind"` // audio | image
	Title  string `json:"title"`
	Artist string `json:"artist,omitempty"`
	LenMS  int    `json:"len_ms,omitempty"` // audio and video
	W      int    `json:"w,omitempty"`
	H      int    `json:"h,omitempty"`
	Size   int64  `json:"size,omitempty"` // bytes of the file
	Mime   string `json:"mime,omitempty"` // image/png | image/jpeg
	At     int64  `json:"at,omitempty"`   // unix seconds
	// Music in the system's library (audio): what a player shows besides.
	Album       string `json:"album,omitempty"`
	AlbumArtist string `json:"album_artist,omitempty"`
	Number      int    `json:"track,omitempty"`
	Disc        int    `json:"disc,omitempty"`
	Genre       string `json:"genre,omitempty"`
	Year        string `json:"year,omitempty"`
	Folder      string `json:"folder,omitempty"`  // in the library, "" at the top
	Format      string `json:"format,omitempty"`  // "FLAC 24/96", "SACD DSD64"
	Quality     string `json:"quality,omitempty"` // "24/96", "DSD64"
	Codec       string `json:"codec,omitempty"`
	Rate        int    `json:"rate,omitempty"`
	Bits        int    `json:"bits,omitempty"`
	Channels    int    `json:"channels,omitempty"`
	Lossless    bool   `json:"lossless,omitempty"`
	ReplayGain  bool   `json:"replaygain,omitempty"`
	Cover       bool   `json:"cover,omitempty"`
	From        string `json:"from,omitempty"` // "cue" | "sacd" for their tracks
}

// MediaArgs selects a kind for media.list (audio, the default; image;
// video) and media.pick.
type MediaArgs struct {
	Kind string `json:"kind,omitempty"`
	ID   string `json:"id,omitempty"` // media.delete
}

// AudioPlayArgs: for music also the track to queue after it, where to
// start, ReplayGain ("" off, track, album) and how DSD goes out (auto,
// dop, pcm).
type AudioPlayArgs struct {
	Media      string `json:"media"`
	Next       string `json:"next,omitempty"`
	AtMS       int64  `json:"at_ms,omitempty"`
	ReplayGain string `json:"replaygain,omitempty"`
	DSD        string `json:"dsd,omitempty"`
}

// AudioControlArgs: Op is pause, resume or stop, and for music seek (AtMS),
// queue (Next) or options (ReplayGain, DSD).
type AudioControlArgs struct {
	ID         string `json:"id"`
	Op         string `json:"op"`
	AtMS       int64  `json:"at_ms,omitempty"`
	Next       string `json:"next,omitempty"`
	ReplayGain string `json:"replaygain,omitempty"`
	DSD        string `json:"dsd,omitempty"`
}

type AudioOpenArgs struct {
	Name string `json:"name"`
}

type AudioRouteArgs struct {
	Route string `json:"route"`
}

type PushToken struct {
	Token string `json:"token"`
}

// PushEvent is the value of a "push" event.
type PushEvent struct {
	ID   string `json:"id"`
	Kind string `json:"kind,omitempty"` // "" | "voip"
	Data string `json:"data,omitempty"`
	Sent int64  `json:"sent"` // unix ms at the gateway
	// Notified: the system already showed the push's notification, so the
	// app should not show its own.
	Notified bool `json:"notified,omitempty"`
	// Collapse: this push replaces earlier ones with the same key.
	Collapse string `json:"collapse_id,omitempty"`
}

type Alarm struct {
	ID    string `json:"id"`
	Time  string `json:"time"` // HH:MM, local
	Label string `json:"label,omitempty"`
	Daily bool   `json:"daily,omitempty"`
	On    bool   `json:"on"`
}

type CallStartArgs struct {
	Peer  string `json:"peer"`
	Video bool   `json:"video"`
}

type CallStartResult struct {
	CallID string `json:"call_id"`
	Local  string `json:"local"`
	Remote string `json:"remote"`
}

type CallControlArgs struct {
	CallID string `json:"call_id"`
	Mic    *bool  `json:"mic,omitempty"`
	Cam    *bool  `json:"cam,omitempty"`
}

type ScanResult struct {
	Text string `json:"text"`
}

// AuthConfirmArgs asks the system to show a trusted confirmation dialog. The
// signature covers Prompt exactly as displayed ("what you see is what you
// sign"), so a compromised app cannot show one amount and sign another.
type AuthConfirmArgs struct {
	Key     string `json:"key"`
	Prompt  string `json:"prompt"`
	Payload string `json:"payload"`
	// Alg picks the key's algorithm when it is first created: "" for
	// Ed25519, "sm2" for SM2 with SM3 digests (the Chinese national
	// standards). A key keeps it.
	Alg string `json:"alg,omitempty"`
	// BioKey offers a fingerprint as well: a match signs with this
	// biometric-bound key instead, the payment PIN still signs with Key.
	// The system offers it only when the user turned on fingerprints for
	// payments; the key is made (bound to the fingers enrolled then) when
	// the PIN confirms, and a change to the fingers voids it until the PIN
	// confirms again. The proof's KeyAlias says which key signed.
	BioKey string `json:"bio_key,omitempty"`
}

// KeyProof is returned by signing operations: the signature plus the chain a
// relying server needs: root -> device cert -> app key cert -> signature.
type KeyProof struct {
	// Alg is the app key's algorithm ("" Ed25519, "sm2"); the app key
	// certificate binds it.
	Alg        string `json:"alg,omitempty"`
	Digest     string `json:"digest"`
	Sig        string `json:"sig"`
	AppID      string `json:"app_id"`
	KeyAlias   string `json:"key_alias"`
	AppPub     string `json:"app_pub"`
	AppKeyCert string `json:"app_key_cert"`
	UserAuth   bool   `json:"user_auth"`
	// Bio: a biometric-bound key signed, after a fingerprint match; its
	// certificate says so (keystore.AppKeyCertMsgBio).
	Bio        bool   `json:"bio,omitempty"`
	DeviceID   string `json:"device_id"`
	DevicePub  string `json:"device_pub"`
	DeviceCert string `json:"device_cert"`
}

// AppSetting is one setting an app declares (toggle or choice); Value is
// filled in when read.
type AppSetting struct {
	Key     string   `json:"key"`
	Title   string   `json:"title"`
	Type    string   `json:"type"` // toggle | choice | number | text
	Choices []string `json:"choices,omitempty"`
	// Labels: what each choice is called in the user's language, from the
	// manifest's locales (settings.apps fills it; Choices stay the values).
	Labels []string `json:"labels,omitempty"`
	// number: the range and step; text: the longest value (default 64).
	Min     float64 `json:"min,omitempty"`
	Max     float64 `json:"max,omitempty"`
	Step    float64 `json:"step,omitempty"`
	MaxLen  int     `json:"max_len,omitempty"`
	Default any     `json:"default,omitempty"`
	Value   any     `json:"value,omitempty"`
}

type AppSettings struct {
	App      string       `json:"app"`
	Name     string       `json:"name"`
	Icon     string       `json:"icon"`
	Settings []AppSetting `json:"settings"`
}

type AppSettingArgs struct {
	App   string `json:"app"`
	Key   string `json:"key"`
	Value any    `json:"value"`
}

type WidgetArgs struct {
	Text string `json:"text"`
	// Arg is the deep link the app opens with when the user picks the
	// widget on the standby screen.
	Arg string `json:"arg,omitempty"`
}

// FileArgs: Name and Data (base64) for file.save, Types (extensions or
// MIME prefixes) to narrow file.pick, ID for file.open.
type FileArgs struct {
	ID    string   `json:"id,omitempty"`
	Name  string   `json:"name,omitempty"`
	Data  string   `json:"data,omitempty"`
	Types []string `json:"types,omitempty"`
}

type FileItem struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Mime string `json:"mime"`
	Size int64  `json:"size"`
	At   int64  `json:"at"`
}

// MediaSaveArgs is an image for the album: bytes the app has (PNG or JPEG,
// base64) or an image the system shows for it ("gen:" pictures).
type MediaSaveArgs struct {
	Data string `json:"data,omitempty"`
	Src  string `json:"src,omitempty"`
	Name string `json:"name,omitempty"`
}

type SignArgs struct {
	Key     string `json:"key"`
	Payload string `json:"payload"`
	Alg     string `json:"alg,omitempty"` // as in AuthConfirmArgs
}

// AttestArgs: Alg "sm2" attests on the device's SM2 chain (the Chinese
// national standards).
type AttestArgs struct {
	Nonce string `json:"nonce"`
	Alg   string `json:"alg,omitempty"`
}

type Attestation struct {
	Alg        string `json:"alg,omitempty"`
	DeviceID   string `json:"device_id"`
	DevicePub  string `json:"device_pub"`
	DeviceCert string `json:"device_cert"`
	AppID      string `json:"app_id"`
	Nonce      string `json:"nonce"`
	Boot       string `json:"boot"`
	Sig        string `json:"sig"`
}

type PermEntry struct {
	App   string `json:"app"`
	Name  string `json:"name"`
	Perm  string `json:"perm"`
	Label string `json:"label"`
	State string `json:"state"`
	// LastUsed (unix ms) and Uses: when the grant was last exercised and
	// how often (the privacy dashboard).
	LastUsed int64 `json:"last_used,omitempty"`
	Uses     int   `json:"uses,omitempty"`
	// Reset (unix ms): the system took the grant back because the app was
	// not opened for months; it asks again on the next use.
	Reset int64 `json:"reset,omitempty"`
}

// PkgArgs: File is a package, or a delta from the installed version; SHA256,
// when given, is the catalogue's digest of the whole package, which the
// one installed (rebuilt from a delta or not) must have.
type PkgArgs struct {
	File   string `json:"file,omitempty"` // relative to the caller's data dir
	ID     string `json:"id,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
	// Auto: an update the store fetched in the background, installed without
	// asking if it changes nothing the user agreed to; else ErrNeedsUser.
	Auto bool `json:"auto,omitempty"`
}

type RevocationArgs struct {
	Data string `json:"data"` // the store's signed list (JSON)
}

// IntentArgs: the action, its data for the handler, and a few words the
// system shows when it asks which app (labelled with the caller's name).
type IntentArgs struct {
	Action  string `json:"action"`
	Payload string `json:"payload,omitempty"`
	What    string `json:"what,omitempty"`
}

// IntentRes is the handler's answer: which app, whether it did it, and
// its result.
type IntentRes struct {
	App    string `json:"app"`
	OK     bool   `json:"ok"`
	Result string `json:"result,omitempty"`
}

// Intent is what the handler is opened with: the deep link IntentPrefix
// followed by its JSON.
type Intent struct {
	ID       string `json:"id"`
	Action   string `json:"action"`
	Payload  string `json:"payload,omitempty"`
	From     string `json:"from"`
	FromName string `json:"from_name"`
}

const IntentPrefix = "intent:"

type IntentReplyArgs struct {
	ID     string `json:"id"`
	OK     bool   `json:"ok"`
	Result string `json:"result,omitempty"`
}

type CatalogArgs struct {
	Data string `json:"data"` // the store's signed catalogue (JSON)
}

// CatalogRes: the verified catalogue's apps (the store's JSON array) and
// when the store signed it (Unix seconds).
type CatalogRes struct {
	Store  string          `json:"store"`
	Issued int64           `json:"issued"`
	Apps   json.RawMessage `json:"apps"`
}

type PkgInfo struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Version     string   `json:"version"`
	Developer   string   `json:"developer"`
	Store       string   `json:"store"`
	Permissions []string `json:"permissions,omitempty"`
}

// PushKeyArgs: rotate replaces the key (the previous one still opens data
// sealed to it).
type PushKeyArgs struct {
	Rotate bool `json:"rotate,omitempty"`
}

type PushKeyRes struct {
	Key string `json:"key"`
}

type PushOpenArgs struct {
	Data string `json:"data"` // sealed ("e1.…")
}

type PushOpenRes struct {
	Data string `json:"data"` // base64 plaintext
}

type ShareArgs struct {
	Kind  string `json:"kind"` // "text" | "image" | "link" | "file"
	Text  string `json:"text,omitempty"`
	Media string `json:"media,omitempty"` // photo ID for "image"
	URL   string `json:"url,omitempty"`   // "link"; Text is its title
	File  string `json:"file,omitempty"`  // file ID for "file"
}

type Location struct {
	Lat       float64 `json:"lat"`
	Lon       float64 `json:"lon"`
	AccuracyM float64 `json:"accuracy_m"`
	At        int64   `json:"at"`
}

type JobArgs struct {
	ID       string `json:"id"`
	EveryMin int    `json:"every_min,omitempty"`
	// Charging: run only on external power (backups, cache cleanup).
	Charging bool `json:"charging,omitempty"`
}

type ClipArgs struct {
	Text string `json:"text"`
}

type SettingArgs struct {
	Key   string  `json:"key"`
	Value float64 `json:"value"`
	// Text is the value of the settings that take one (fingerprint_rename).
	Text string `json:"text,omitempty"`
}

// PermSetArgs: State is granted, denied or ask; the network permission
// takes wifi (Wi-Fi only) instead of ask.
type PermSetArgs struct {
	App   string `json:"app"`
	Perm  string `json:"perm"`
	State string `json:"state"`
}

// TrafficEntry is one app's bytes since the meter started (settings.get
// "traffic"; absent without the per-app meter).
type TrafficEntry struct {
	App      string `json:"app"`
	Name     string `json:"name"`
	WifiRx   uint64 `json:"wifi_rx"`
	WifiTx   uint64 `json:"wifi_tx"`
	MobileRx uint64 `json:"mobile_rx"`
	MobileTx uint64 `json:"mobile_tx"`
}

// EnergyEntry is an app's CPU over the last day, in the foreground and in
// the background (settings.get "energy": the Battery page).
type EnergyEntry struct {
	App          string `json:"app"`
	Name         string `json:"name"`
	ForegroundMS int64  `json:"fg_ms"`
	BackgroundMS int64  `json:"bg_ms"`
}

// UpdateArgs: system.update's action (status | check | install | reboot).
type UpdateArgs struct {
	Action string `json:"action"`
}

// WebControlArgs: web.control on the web node Node of screen Screen. Op is
// reload | forward | back | stop | zoom (Value, 0.5–3) | find (Text) |
// find_next | find_prev | find_end | clear_data.
type WebControlArgs struct {
	Screen string  `json:"screen"`
	Node   string  `json:"node"`
	Op     string  `json:"op"`
	Text   string  `json:"text,omitempty"`
	Value  float64 `json:"value,omitempty"`
}

// WallpaperArgs: wallpaper.set's photo ("media:<id>") and Target (idle |
// lock | both).
type WallpaperArgs struct {
	Media  string `json:"media"`
	Target string `json:"target"`
}

// VideoArgs: video.open's video ("media:<id>") and where to start;
// video.control's Player and Op (pause, resume, seek to AtMS, state, close).
type VideoArgs struct {
	Media  string `json:"media,omitempty"`
	Player string `json:"player,omitempty"`
	Op     string `json:"op,omitempty"`
	AtMS   int64  `json:"at_ms,omitempty"`
}

// VideoState is a player: Stream goes in a KindMedia node; W × H is the
// picture's size.
type VideoState struct {
	Player string `json:"player"`
	Stream string `json:"stream"`
	State  string `json:"state"` // playing | paused | ended | failed
	PosMS  int64  `json:"pos_ms"`
	DurMS  int64  `json:"duration_ms"`
	W      int    `json:"w"`
	H      int    `json:"h"`
	Sound  bool   `json:"sound"`
	Error  string `json:"error,omitempty"` // failed: why the decoder stopped
}

// WebState is the "web" event: what a web node of the app shows now.
type WebState struct {
	Screen     string  `json:"screen"`
	Node       string  `json:"node"`
	URL        string  `json:"url"`
	Title      string  `json:"title"`
	Loading    bool    `json:"loading"`
	CanBack    bool    `json:"can_back"`
	CanForward bool    `json:"can_forward"`
	Zoom       float64 `json:"zoom"`
}

// WifiState is wifi.state: the radio, its state and the networks in range,
// strongest first. Backend says what drives it: "iwd", "sim" (a developer
// machine) or "" (no Wi-Fi).
type WifiState struct {
	Backend  string    `json:"backend"`
	Enabled  bool      `json:"enabled"`
	State    string    `json:"state"` // off | disconnected | connecting | connected
	Networks []WifiNet `json:"networks"`
	Address  string    `json:"address,omitempty"` // IPv4 while connected
}

type WifiNet struct {
	SSID      string `json:"ssid"`
	Security  string `json:"security"` // open | psk | 8021x
	Signal    int    `json:"signal"`   // bars, 0-4
	Known     bool   `json:"known,omitempty"`
	Connected bool   `json:"connected,omitempty"`
}

// WifiArgs: wifi.set takes Enabled, wifi.connect SSID and (for a new psk
// network) Passphrase, wifi.forget SSID. A new enterprise (802.1X) network
// takes the user's name (Identity) with the password in Passphrase, the
// domain the server's certificate must name (Domain: without it any server
// could collect the password) and the EAP method: peap (the default) or
// ttls with MSCHAPv2 inside, ttls-pap with PAP inside.
type WifiArgs struct {
	Enabled    *bool  `json:"enabled,omitempty"`
	SSID       string `json:"ssid,omitempty"`
	Passphrase string `json:"passphrase,omitempty"`
	Identity   string `json:"identity,omitempty"`
	Domain     string `json:"domain,omitempty"`
	EAP        string `json:"eap,omitempty"`
}

// BTState is bt.state: the adapter and the devices it knows or found.
type BTState struct {
	Backend     string     `json:"backend"` // bluez | sim | ""
	Enabled     bool       `json:"enabled"`
	Discovering bool       `json:"discovering,omitempty"`
	Name        string     `json:"name,omitempty"` // this phone as others see it
	Devices     []BTDevice `json:"devices"`
}

type BTDevice struct {
	Address   string `json:"address"`
	Name      string `json:"name"`
	Paired    bool   `json:"paired,omitempty"`
	Connected bool   `json:"connected,omitempty"`
	// Phonebook: whether the device (a car kit) may read the contacts and
	// the call log (PBAP): "allowed", "denied", or "" until it first asks.
	Phonebook string `json:"phonebook,omitempty"`
}

// BTArgs: bt.set takes Enabled and Discover, or Address with Phonebook
// (allow or deny that device the contacts); bt.pair, bt.forget and bt.send
// take Address.
type BTArgs struct {
	Enabled   *bool  `json:"enabled,omitempty"`
	Discover  *bool  `json:"discover,omitempty"`
	Address   string `json:"address,omitempty"`
	Phonebook *bool  `json:"phonebook,omitempty"`
}

// RadioState is radio.state. Cellular: the modem's radio, on | off, ""
// without telephony; Data: the cellular data link, connected | idle (the
// switch is on, no link yet) | off (the switch or airplane mode), ""
// without telephony.
type RadioState struct {
	Airplane   bool   `json:"airplane"`
	MobileData bool   `json:"mobile_data"`
	Cellular   string `json:"cellular"`
	Data       string `json:"data"`
}

// RadioArgs: radio.set changes either or both switches.
type RadioArgs struct {
	Airplane   *bool `json:"airplane,omitempty"`
	MobileData *bool `json:"mobile_data,omitempty"`
}

// USSDArgs: tel.ussd takes a Code to send, the Text replying to the
// network's question, or Cancel.
type USSDArgs struct {
	Code   string `json:"code,omitempty"`
	Text   string `json:"text,omitempty"`
	Cancel bool   `json:"cancel,omitempty"`
}

// USSDReply is tel.ussd's answer and the EventUSSD value: "done" (Text is
// the network's last word), "input" (it waits for a reply to Text) or
// "notify" (a notice the network sent).
type USSDReply struct {
	State string `json:"state"`
	Text  string `json:"text"`
}
