package ui

import (
	"encoding/json"
	"slices"
)

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
	// CapTelDTMF sends keypad tones into the connected call (voice menus).
	CapTelDTMF  = "tel.dtmf"
	CapTelCalls = "tel.calls"
	CapTelLog   = "tel.log"
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
	// sms.download {id} fetches a multimedia message whose download failed
	// again (the default SMS app).
	CapSMSDownload = "sms.download"
	// sms.draft keeps what the user typed for a conversation (SMSDraftArgs);
	// sms.backup saves every message to Files and sms.restore adds those of
	// a file the user picks in Files, both in the XML of Android's "SMS
	// Backup & Restore" and once the user agreed. The default SMS app.
	CapSMSDraft   = "sms.draft"
	CapSMSBackup  = "sms.backup"
	CapSMSRestore = "sms.restore"
	// sms.vcard {id, index} offers the contacts of a vCard a multimedia
	// message holds, as contacts.import does a file's.
	CapSMSVCard = "sms.vcard"
	// sms.otp waits for the next message with a verification code and
	// hands the code over once the user agreed (no permission: the user's
	// yes is the consent).
	CapSMSOTP = "sms.otp"
	// The system's block list (BlockList) for calls and messages:
	// block.list reads it, block.set changes it (BlockSetArgs). The default
	// SMS app, the system's Phone and Settings only.
	CapBlockList = "block.list"
	CapBlockSet  = "block.set"
	// tel.ussd sends a USSD code (*100#) or the reply to the network's
	// menu, or ends the session; *#06# answers the IMEI from the phone
	// itself. System apps only: such codes can subscribe to paid services.
	CapTelUSSD = "tel.ussd"

	// Contacts are the system's store (shell/contacts.go): contacts.list
	// reads it (the contacts permission). Writing goes past the user:
	// contacts.save adds a contact, or with the contacts permission changes
	// one, and contacts.delete removes one, each once the user agreed in a
	// system dialog; contacts.import adds what a vCard the user picks in
	// Files holds and contacts.export saves every contact into one in Files
	// (contacts permission), both confirmed. The system's Contacts app
	// writes without the dialogs: the user edits there.
	CapContacts       = "contacts.list"
	CapContactsSave   = "contacts.save"
	CapContactsDelete = "contacts.delete"
	CapContactsImport = "contacts.import"
	CapContactsExport = "contacts.export"
	// contacts.groups reads the groups, contacts.group changes them
	// (ContactGroupArgs), contacts.merge makes several contacts one; each
	// write goes past the user as contacts.save does. contacts.pick shows
	// the system's picker and hands over only the number the user chose
	// (no permission: choosing is the consent). contacts.photo makes a
	// contact's picture of a photo the app was handed (media.pick),
	// contacts.books lists the address books contacts sync with.
	// contacts.dates gives the system's calendar app the contacts'
	// birthdays and anniversaries (ContactDate), nothing else of them.
	CapContactsGroups = "contacts.groups"
	CapContactsGroup  = "contacts.group"
	CapContactsMerge  = "contacts.merge"
	CapContactsPick   = "contacts.pick"
	CapContactsPhoto  = "contacts.photo"
	CapContactsBooks  = "contacts.books"
	CapContactsDates  = "contacts.dates"

	// Audio through phone-audiod: apps play media from the library; the
	// focus policy pauses it under calls, ringtones and alarms.
	CapMediaList = "media.list"
	// CapMediaList2 is media.list's second version (capver.go): a page of
	// the library (MediaListArgs) in an object (MediaPage), where version 1
	// answers the whole library in one array, which a large music library
	// takes past the longest message (wire.MaxLine).
	CapMediaList2   = "media.list@2"
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

	// Location, for the foreground app only (location permission):
	// location.get is one fix, location.watch sends "location" events
	// until location.stop (paused while the app is in the background).
	CapLocationGet   = "location.get"
	CapLocationWatch = "location.watch"
	CapLocationStop  = "location.stop"
	// location.background asks, as a second step once the location is
	// allowed while in use, for it all the time (location_background):
	// the system's own dialog, once; replies {background: true}, or
	// "permission denied by user" when the user keeps it to while in use.
	CapLocationBackground = "location.background"
	// Background jobs: woken every N minutes (15 at least) within the wake
	// budget; a "job" event arrives.
	CapJobSchedule = "job.schedule"
	CapJobCancel   = "job.cancel"
	// net.state: the network the phone is on and whether the app's mobile
	// data is blocked (a NetState; the net permission). A "net.state"
	// event brings it again when the block changes.
	CapNetState = "net.state"
	// net.keepalive: the app's one long connection of its own, kept open
	// while it is frozen (the keepalive permission, which the store
	// reviews; 21-push.md §8). The system dials it and hands the app the
	// socket (a KeepaliveArgs, a KeepaliveRes and one fd); the heartbeats
	// come as "keepalive" events in the system's wake windows, and data
	// arriving thaws the app within its wake budget. close: true ends it.
	CapNetKeepalive = "net.keepalive"

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

	// The VPN (WireGuard) for Settings; system apps only. A profile comes
	// from a wg-quick file the user picks from Files in the system's list;
	// the private keys never leave the system.
	CapVPNList       = "vpn.list"
	CapVPNImport     = "vpn.import"
	CapVPNConnect    = "vpn.connect"
	CapVPNDisconnect = "vpn.disconnect"
	CapVPNDelete     = "vpn.delete"
	CapVPNKeygen     = "vpn.keygen"
	// vpn.set: always-on (with blocking connections without the VPN) and
	// which apps go through the tunnel.
	CapVPNSet = "vpn.set"

	// The contacts account (CardDAV), for Settings; system apps only: the
	// password goes to the system, which keeps it sealed and never gives
	// it back.
	CapAccountState  = "account.state"
	CapAccountAdd    = "account.add"
	CapAccountSync   = "account.sync"
	CapAccountSet    = "account.set"
	CapAccountRemove = "account.remove"
	// The calendar's sync on that account (CalDAV) goes through the system:
	// calendar.pull hands the system's calendar app the server's changes,
	// calendar.push takes the app's (ui.CalPull, ui.CalPushArgs). The
	// system's calendar app only.
	CapCalendarPull = "calendar.pull"
	CapCalendarPush = "calendar.push"

	// Airplane mode and mobile data, for Settings; system apps only.
	// Airplane mode turns the cellular radio, Wi-Fi and Bluetooth off (the
	// user may turn Wi-Fi and Bluetooth on again), an emergency call
	// turns it off; mobile data off stops the cellular data link.
	CapRadioState = "radio.state"
	CapRadioSet   = "radio.set"

	// The SIM's access point names (APNs), for Settings; system apps only:
	// apn.list, and apn.set to add, change, delete or choose one or go back
	// to the carrier's list. Passwords never leave the system.
	CapAPNList = "apn.list"
	CapAPNSet  = "apn.set"

	// The personal hotspot, for Settings; system apps only: Wi-Fi becomes
	// an access point that shares the phone's connection (NAT), and the
	// Wi-Fi station is off meanwhile.
	CapHotspotState = "hotspot.state"
	CapHotspotSet   = "hotspot.set"

	// Moving from an old phone (kario-switch/1, docs/36), for Settings;
	// system apps only. switch.start turns the receiver on for the kinds
	// the user ticked (and the personal hotspot when Wi-Fi is not
	// connected) and gives the pairing code to show; switch.state is how it
	// goes; switch.stop ends it; switch.undo takes out what the last
	// import added.
	CapSwitchState = "switch.state"
	CapSwitchStart = "switch.start"
	CapSwitchStop  = "switch.stop"
	CapSwitchUndo  = "switch.undo"

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

	// Scheduled notifications (iOS's calendar triggers, Android's exact
	// alarms posting a notification): the system shows the app's
	// notification at a time on a wall clock, whether the app runs, is
	// frozen or not running at all (notifications permission). The system
	// keeps them across restarts and reboots, wakes the phone for them as
	// for alarms, and shows one the phone was off for at the next start,
	// marked missed. notify.list gives the app's own; notify.cancel takes
	// one back, or all of them.
	CapNotifySchedule = "notify.schedule"
	CapNotifyCancel   = "notify.cancel"
	CapNotifyList     = "notify.list"

	// locale.get: what the system formats dates by (language, region, time
	// zone, clock, first day of the week, the order of a date's parts).
	// holidays.get: the region's public holiday calendar, the update
	// server's (days off, and the weekend days that are workdays instead).
	CapLocaleGet   = "locale.get"
	CapHolidaysGet = "holidays.get"
	// time.offsets: a zone's UTC offsets over a span of time (the tz
	// database's rules), for apps that show times of other zones than the
	// phone's (a calendar's imported events); a runtime without the tz
	// database (TypeScript apps) converts with them.
	CapTimeOffsets = "time.offsets"
)

// PlatformLevel is the platform API level this system provides
// (22-developer-api.md §2.2). An app's manifest names the lowest it runs on
// (min_platform), which the installer and the store hold it to; the
// broker serves a capability by the system's level, not the app's. The
// level rises by one in the first release that adds to the protocol after
// the last level shipped, and never falls.
const PlatformLevel = 2

// CapLevels are the platform levels the capabilities came in at: one
// added after the release of level N is N+1 (and PlatformLevel with it).
// Each version served has its own key (capver.go): the bare name for
// version 1, "name@N" past it.
var CapLevels = map[string]int{
	CapCallStart:      1,
	CapCallEnd:        1,
	CapCallControl:    1,
	CapScanQR:         1,
	CapAuthConfirm:    1,
	CapKeySign:        1,
	CapAttest:         1,
	CapPermList:       1,
	CapPermSet:        1,
	CapTelDial:        1,
	CapTelAnswer:      1,
	CapTelHangup:      1,
	CapTelHold:        1,
	CapTelMute:        1,
	CapTelDTMF:        1,
	CapTelCalls:       1,
	CapTelLog:         1,
	CapSMSSend:        1,
	CapSMSList:        1,
	CapSMSRead:        1,
	CapSMSThreads:     1,
	CapSMSAlerts:      1,
	CapSMSDelete:      1,
	CapSMSBlock:       1,
	CapSMSBlocked:     1,
	CapTelUSSD:        1,
	CapContacts:       1,
	CapContactsSave:   1,
	CapContactsDelete: 1,
	CapContactsImport: 1,
	CapContactsExport: 1,
	CapMediaList:      1,
	CapMediaList2:     2,
	CapAudioPlay:      1,
	CapAudioControl:   1,
	CapAudioState:     1,
	CapAudioRoute:     1,
	CapAudioStream:    1,
	CapAudioRecord:    1,
	CapCameraCapture:  1,
	CapMediaPick:      1,
	CapMediaSave:      1,
	CapFileSave:       1,
	CapFilePick:       1,
	CapFileOpen:       1,
	CapAppSettings:    1,
	CapWidgetSet:      1,
	CapSettingsApps:   1,
	CapSettingsAppSet: 1,
	CapMediaDelete:    1,
	CapPushKey:        1,
	CapPushOpen:       1,
	CapPushRegister:   1,
	CapPkgInstall:     1,
	CapPkgUninstall:   1,
	CapPkgList:        1,
	CapPkgRevocations: 1,
	CapIntentInvoke:   1,
	CapIntentReply:    1,
	CapPkgCatalog:     1,
	CapShareOpen:      1,
	CapLocationGet:    1,
	CapLocationWatch:  1,
	CapLocationStop:   1,
	CapJobSchedule:    1,
	CapJobCancel:      1,
	CapNetState:       1,
	CapClipboardSet:   1,
	CapSettingsGet:    1,
	CapSettingsSet:    1,
	CapSystemUpdate:   1,
	CapWifiState:      1,
	CapWifiSet:        1,
	CapWifiConnect:    1,
	CapWifiForget:     1,
	CapBTState:        1,
	CapBTSet:          1,
	CapBTPair:         1,
	CapBTForget:       1,
	CapBTSend:         1,
	CapVPNList:        1,
	CapVPNImport:      1,
	CapVPNConnect:     1,
	CapVPNDisconnect:  1,
	CapVPNDelete:      1,
	CapVPNKeygen:      1,
	CapVPNSet:         1,
	CapRadioState:     1,
	CapRadioSet:       1,
	CapHotspotState:   1,
	CapHotspotSet:     1,
	CapSwitchState:    1,
	CapSwitchStart:    1,
	CapSwitchStop:     1,
	CapSwitchUndo:     1,
	CapWebControl:     1,
	CapWallpaperSet:   1,
	CapVideoOpen:      1,
	CapVideoControl:   1,
	CapAlarmSet:       1,
	CapAlarmCancel:    1,
	CapAlarmList:      1,
	CapNotifySchedule: 1,
	CapNotifyCancel:   1,
	CapNotifyList:     1,
	CapLocaleGet:      1,
	CapHolidaysGet:    1,
	CapTimeOffsets:    1,
	CapNFCReply:       1,
	CapNFCState:       1,
	CapNFCSet:         1,
	CapNFCRead:        1,
	CapNFCTransceive:  1,
	CapSMSDownload:    1,
	CapBlockList:      1,
	CapBlockSet:       1,
	CapAPNList:        1,
	CapAPNSet:         1,
	CapAccountState:   1,
	CapAccountAdd:     1,
	CapAccountSync:    1,
	CapAccountSet:     1,
	CapAccountRemove:  1,
	CapCalendarPull:   1,
	CapCalendarPush:   1,
	CapSMSDraft:       1,
	CapSMSBackup:      1,
	CapSMSRestore:     1,
	CapSMSVCard:       1,
	CapSMSOTP:         1,
	CapContactsGroups: 1,
	CapContactsGroup:  1,
	CapContactsMerge:  1,
	CapContactsPick:   1,
	CapContactsPhoto:  1,
	CapContactsBooks:  1,
	CapDisplayHint:    1,
	CapContactsDates:  1,

	CapLocationBackground: 2,
	CapNetKeepalive:       2,
}

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
	// PermLocationBackground: the location also while the app is in the
	// background (Android 10's "all the time"). Never asked for with the
	// location: once that is allowed, location.background asks for it in
	// a dialog of its own, or the user gives it in Settings.
	PermLocationBackground = "location_background"
	PermInstall            = "install" // system image only (the store)
	// PermKeepalive: a long connection of the app's own while it is frozen
	// (net.keepalive). Every version that declares it waits for the
	// store's human review, with its purpose.
	PermKeepalive = "keepalive"
)

// PermNeeds: a permission that is only asked for on top of another, which the
// manifest must declare too (the location in the background is a second step
// after the location while in use; an own connection needs the network).
var PermNeeds = map[string]string{
	PermLocationBackground: PermLocation,
	PermKeepalive:          PermNet,
}

// MissingPerm is the permission perms lacks for one of them, and which one
// needs it; "" when none is missing.
func MissingPerm(perms []string) (needs, missing string) {
	for _, p := range perms {
		if q, ok := PermNeeds[p]; ok && !slices.Contains(perms, q) {
			return p, q
		}
	}
	return "", ""
}

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

	PermLocationBackground: "Location in the background",
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

	PermKeepalive: "Its own connection in the background",
}

// Telephony data as the shell passes it on (from phone-telephonyd).
type TelCall struct {
	ID        string `json:"id"`
	Number    string `json:"number"`
	Name      string `json:"name,omitempty"`
	Dir       string `json:"dir"`   // in | out
	State     string `json:"state"` // dialing | alerting | incoming | active | held | ended
	Reason    string `json:"reason,omitempty"`
	Error     string `json:"error,omitempty"` // with reason "failed": why, in the modem's or ModemManager's words
	Started   int64  `json:"started"`
	Connected int64  `json:"connected"`
	Ended     int64  `json:"ended"`
	Muted     bool   `json:"muted"`
	// Emergency: an emergency number, which the modem calls even in
	// airplane mode (the system leaves airplane mode for it).
	Emergency bool `json:"emergency,omitempty"`
	// DTMF: the last 64 tones sent during the call (tel.dtmf).
	DTMF string `json:"dtmf,omitempty"`
	// IMS: with reason "failed", the modem's IMS (VoLTE) registration the
	// end is put down to (not-registered, registering, limited-registered):
	// on 4G a call needs VoLTE or a fallback to 2G or 3G.
	IMS string `json:"ims,omitempty"`
}

// DTMFArgs are tel.dtmf's: 1 to 32 of 0-9, *, #, A-D.
type DTMFArgs struct {
	CallID string `json:"call_id"`
	Tones  string `json:"tones"`
}

type TelLogEntry struct {
	Number   string `json:"number"`
	Name     string `json:"name,omitempty"`
	Dir      string `json:"dir"`
	Result   string `json:"result"` // answered | missed | rejected | busy | outgoing
	At       int64  `json:"at"`
	Duration int64  `json:"duration"`
	// ID and Imported: an entry that came from another phone (Settings ›
	// My device › Move from an old phone); the phone's own have neither.
	ID       string `json:"id,omitempty"`
	Imported bool   `json:"imported,omitempty"`
}

// SMS is a message as phone-smsd keeps it. Thread is its conversation: the
// number written the one way smsd compares numbers (NumberKey; a group's
// numbers, sorted, joined by commas). Folder is "" or SMSFolderBlocked
// (from a blocked number: kept apart, no notification). Code is a
// verification code smsd found in the text.
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
	// Imported: it came from another phone (Settings › My device › Move
	// from an old phone), not over the air.
	Imported bool `json:"imported,omitempty"`
	// A multimedia message (Kind SMSKindMMS): its subject, its parts in the
	// order its presentation gives, the numbers of a group conversation
	// besides this phone's (Members; Peer is then the sender of a received
	// one), and a received one's download (MMS: "" downloaded, else one of
	// the MMS* states).
	Kind    string    `json:"kind,omitempty"`
	Subject string    `json:"subject,omitempty"`
	Parts   []SMSPart `json:"parts,omitempty"`
	Members []string  `json:"members,omitempty"`
	MMS     string    `json:"mms,omitempty"`
	// Flash: a class 0 message the user chose to keep (it was shown at
	// once, in a system dialog).
	Flash bool `json:"flash,omitempty"`
	// Due: when a scheduled message (State SMSScheduled) goes, Unix
	// seconds; Zone: the time zone it was picked in.
	Due  int64  `json:"due,omitempty"`
	Zone string `json:"zone,omitempty"`
}

// SMSPart is a part of a multimedia message: its media type, name and
// size; a text part's text; Media, a picture's photo in the media library
// ("media:<id>", shown by an image node); Local: phone-smsd keeps the
// bytes (a part the library does not take, or not yet).
type SMSPart struct {
	Type  string `json:"type"`
	Name  string `json:"name,omitempty"`
	Size  int64  `json:"size,omitempty"`
	Text  string `json:"text,omitempty"`
	Media string `json:"media,omitempty"`
	Local bool   `json:"local,omitempty"`
}

// SMSKindMMS marks a multimedia message; the MMS* states are a received
// one's download: waiting or under way, failed (sms.download tries again)
// or expired at the operator.
const (
	SMSKindMMS     = "mms"
	MMSPending     = "pending"
	MMSDownloading = "downloading"
	MMSFailed      = "failed"
	MMSExpired     = "expired"
)

// An outgoing message's states (delivered: the recipient's phone took it,
// by the network's delivery report; scheduled: waiting for its Due), and
// the blocked folder.
const (
	SMSScheduled     = "scheduled"
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

// SMSThread is a conversation, newest first in sms.threads. Draft is what
// the user typed and left unsent (sms.draft), at DraftAt; a conversation
// may hold a draft alone (Count 0).
type SMSThread struct {
	Thread  string `json:"thread"`
	Peer    string `json:"peer"`
	Name    string `json:"name,omitempty"`
	Last    SMS    `json:"last"`
	Count   int    `json:"count"`
	Unread  int    `json:"unread"`
	Blocked bool   `json:"blocked,omitempty"`
	Draft   string `json:"draft,omitempty"`
	DraftAt int64  `json:"draft_at,omitempty"`
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
// Level comes from the channel it came on (3GPP TS 23.041). Area is where
// it is meant for, when the network says (the warning area coordinates of
// WEA 3.0, ATIS-0700041): the system shows it when the phone is inside, or
// does not know where it is.
type SMSAlert struct {
	ID      string      `json:"id"`
	Level   string      `json:"level"`
	Channel int         `json:"channel"`
	Text    string      `json:"text"`
	At      int64       `json:"at"`
	Area    []AlertArea `json:"area,omitempty"`
}

// AlertArea is a circle (Points holds its centre, RadiusM its radius) or
// a polygon (Points, three or more, in order).
type AlertArea struct {
	Points  []GeoPoint `json:"points"`
	RadiusM float64    `json:"radius_m,omitempty"`
}

// GeoPoint is a position, WGS-84 degrees.
type GeoPoint struct {
	Lat float64 `json:"lat"`
	Lon float64 `json:"lon"`
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
// SMS app; others always send a new one). To may hold several numbers
// (commas or semicolons between them): a group message. Media are photos
// of the library the app may see ("media:<id>", at most MaxMMSMedia);
// with them, a subject or a group it goes as a multimedia message. At
// (Unix seconds, the default SMS app) schedules it: it goes then, whatever
// the time zone is by that time; Zone names the zone it was picked in.
// ID of a scheduled message sends it now.
type SMSSendArgs struct {
	To      string   `json:"to"`
	Text    string   `json:"text"`
	ID      string   `json:"id,omitempty"`
	Media   []string `json:"media,omitempty"`
	Subject string   `json:"subject,omitempty"`
	At      int64    `json:"at,omitempty"`
	Zone    string   `json:"zone,omitempty"`
}

// SMSIDArgs names a message (sms.download).
type SMSIDArgs struct {
	ID string `json:"id"`
}

// MaxMMSMedia: pictures in one multimedia message; MaxGroup: numbers in
// one group message.
const (
	MaxMMSMedia = 5
	MaxGroup    = 20
)

type PeerArgs struct {
	Peer string `json:"peer,omitempty"`
}

// Contact is an entry of the contacts store. Number is the first of
// Numbers, for apps that read only one. Pinyin is the system's: the name
// spelt in lower-case Latin syllables (a Latin name: its words), for
// search on a keypad (ContactMatches). Groups are the IDs of the groups it
// is in (contacts.list fills them in; contacts.group changes them).
// Photo names its picture (contacts.photo makes one; ContactPhotoPrefix
// shows it), "" for none. Book is the address book it is in (contacts.books),
// "" for the phone alone; contacts.save puts a new one in the default book
// unless Book is another or BookPhone, and only the system's Contacts app
// moves one (another Book). Birthday and Anniversary are "YYYY-MM-DD", or
// "--MM-DD" without the year; URLs are web addresses. ReadOnly (contacts.list
// says) marks a contact of a read-only book: it is not changed, moved or
// deleted. PhotoData is the system's own, never sent: a picture's bytes on
// their way into the store (a vCard's PHOTO).
type Contact struct {
	ID          string           `json:"id,omitempty"`
	Name        string           `json:"name"`
	Number      string           `json:"number"`
	Numbers     []ContactNumber  `json:"numbers,omitempty"`
	Emails      []ContactEmail   `json:"emails,omitempty"`
	Addresses   []ContactAddress `json:"addresses,omitempty"`
	Notes       string           `json:"notes,omitempty"`
	Nickname    string           `json:"nickname,omitempty"`
	Org         string           `json:"org,omitempty"`
	Title       string           `json:"title,omitempty"`
	Birthday    string           `json:"birthday,omitempty"`
	Anniversary string           `json:"anniversary,omitempty"`
	URLs        []string         `json:"urls,omitempty"`
	Pinyin      string           `json:"pinyin,omitempty"`
	Groups      []string         `json:"groups,omitempty"`
	Photo       string           `json:"photo,omitempty"`
	Book        string           `json:"book,omitempty"`
	ReadOnly    bool             `json:"read_only,omitempty"`
	PhotoData   []byte           `json:"-"`
}

// ContactEmail is an e-mail address and its label (NumberHome, NumberWork,
// NumberOther).
type ContactEmail struct {
	Address string `json:"address"`
	Label   string `json:"label,omitempty"`
}

// ContactAddress is a postal address as vCard's ADR has it, and its label
// (NumberHome, NumberWork, NumberOther).
type ContactAddress struct {
	Label    string `json:"label,omitempty"`
	POBox    string `json:"po_box,omitempty"`
	Extended string `json:"extended,omitempty"`
	Street   string `json:"street,omitempty"`
	City     string `json:"city,omitempty"`
	Region   string `json:"region,omitempty"`
	Postcode string `json:"postcode,omitempty"`
	Country  string `json:"country,omitempty"`
}

// ContactNumber is a phone number and its label (NumberMobile, NumberHome,
// NumberWork, NumberOther).
type ContactNumber struct {
	Number string `json:"number"`
	Label  string `json:"label,omitempty"`
}

const (
	NumberMobile = "mobile"
	NumberHome   = "home"
	NumberWork   = "work"
	NumberOther  = "other"
)

// ContactIDArgs names a contact (contacts.delete).
type ContactIDArgs struct {
	ID string `json:"id"`
}

// ContactsFile is what contacts.import and contacts.export did: the file
// in Files, how many contacts it added or holds, and (import) how many it
// left out, already in the contacts or without a name and a number.
type ContactsFile struct {
	File    string `json:"file"`
	Count   int    `json:"count"`
	Skipped int    `json:"skipped,omitempty"`
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
	// Original: the album file's size when media.pick handed over a smaller
	// copy (MaxSide); W, H, Size and Mime are then the copy's.
	Original int64  `json:"original,omitempty"`
	Mime     string `json:"mime,omitempty"` // image/png | image/jpeg
	At       int64  `json:"at,omitempty"`   // unix seconds
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
	// MaxSide (media.pick, PhotoSideMin to PhotoSideMax): the app gets a
	// JPEG copy whose longer side is at most this many pixels instead of
	// the album's file, unless that is a JPEG already within it.
	MaxSide int `json:"max_side,omitempty"`
}

const (
	PhotoSideMin = 64
	PhotoSideMax = 4096
	// PhotoSendSide is what a chat sends a photo at unless the user asks
	// for the original.
	PhotoSendSide = 1280
)

// MediaListArgs: media.list@2 lists Kind (audio, the default; image;
// video) in media.list's order, from item Offset, at most Limit items (1
// to MediaPageMax, default MediaPageDefault).
type MediaListArgs struct {
	Kind   string `json:"kind,omitempty"`
	Offset int    `json:"offset,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

// MediaPage is media.list@2's answer: the page's items, how many the kind
// has in all, and Next, the offset of the next page (0: this is the last).
type MediaPage struct {
	Items []MediaItem `json:"items"`
	Total int         `json:"total"`
	Next  int         `json:"next,omitempty"`
}

const (
	MediaPageDefault = 100
	MediaPageMax     = 500
)

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

// The routes of audio.route; the system goes back to the earpiece when
// the last call ends.
const (
	RouteEarpiece = "earpiece"
	RouteSpeaker  = "speaker"
)

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

// ScheduledNotif is a notification the system shows at a time (ID is the
// app's own; scheduling the same ID again replaces it). At is
// "YYYY-MM-DDTHH:MM" on the clock of Zone, an IANA name (a meeting at
// 09:00 Asia/Shanghai is that instant wherever the phone is), or with Zone
// "" on the clock of whatever zone the phone is in when it comes due
// (floating: "every day at 08:00" follows the traveller). A wall time a
// daylight-saving change skips comes as far after the change (02:30 in a
// gap from 02:00 to 03:00 at 03:30); one it repeats, at the first of the
// two. Repeat is "", RepeatDaily or
// RepeatWeekly. Arg is the deep link the app opens with when the user picks
// the notification. Next (in answers) is when it comes next, Unix ms.
type ScheduledNotif struct {
	ID     string `json:"id"`
	At     string `json:"at"`
	Zone   string `json:"zone,omitempty"`
	Repeat string `json:"repeat,omitempty"`
	Title  string `json:"title"`
	Body   string `json:"body,omitempty"`
	Arg    string `json:"arg,omitempty"`
	Next   int64  `json:"next,omitempty"`
}

const (
	RepeatDaily  = "daily"
	RepeatWeekly = "weekly"
	// MaxScheduledNotifs an app may have waiting (iOS's limit too).
	MaxScheduledNotifs = 64
	// ScheduledAtLayout is ScheduledNotif.At's layout.
	ScheduledAtLayout = "2006-01-02T15:04"
)

// NotifyCancelArgs: notify.cancel takes back the one with ID, or with All
// every one the app has waiting.
type NotifyCancelArgs struct {
	ID  string `json:"id,omitempty"`
	All bool   `json:"all,omitempty"`
}

// NotifyFired is the EventNotifyFired value: the scheduled notification
// the system just showed, Missed when it came due while the phone was off.
type NotifyFired struct {
	ID     string `json:"id"`
	Missed bool   `json:"missed,omitempty"`
}

// LocaleInfo is locale.get: the interface language, the region (ISO 3166),
// the time zone (IANA), the user's clock, the first day of the week (0
// Sunday to 6 Saturday) and the order the region writes a date's parts in
// (DateYMD, DateDMY or DateMDY), as the system's date fields show them.
type LocaleInfo struct {
	Lang         string `json:"lang"`
	Region       string `json:"region"`
	Zone         string `json:"zone"`
	Clock24      bool   `json:"clock_24h"`
	FirstWeekday int    `json:"first_weekday"`
	DateOrder    string `json:"date_order"`
}

const (
	DateYMD = "ymd"
	DateDMY = "dmy"
	DateMDY = "mdy"
)

// ZoneOffsetsArgs: time.offsets of Zone (an IANA name) from From to To
// (Unix seconds, at most MaxZoneSpanYears years apart).
type ZoneOffsetsArgs struct {
	Zone string `json:"zone"`
	From int64  `json:"from"`
	To   int64  `json:"to"`
}

// ZoneOffsets: each span's start (Unix seconds; the first starts at or
// before From) and its offset east of UTC in seconds, until the next one.
type ZoneOffsets struct {
	Zone  string     `json:"zone"`
	Spans []ZoneSpan `json:"spans"`
}

type ZoneSpan struct {
	Start  int64 `json:"start"`
	Offset int   `json:"offset"`
}

const MaxZoneSpanYears = 50

// HolidayCalendar is holidays.get: the region's days off and the weekend
// days that are workdays instead (YYYY-MM-DD, the region's own time), and
// where the calendar is from; both lists are empty when the phone has no
// calendar for the region.
type HolidayCalendar struct {
	Region   string   `json:"region"`
	Source   string   `json:"source,omitempty"`
	DaysOff  []string `json:"days_off"`
	Workdays []string `json:"workdays"`
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
	// Security is where the key lives and the fingerprint was matched
	// (SecuritySoftware, SecurityTEE). No signature covers it: only a TEE
	// could vouch for it, and in software the same process holds the keys.
	Security string `json:"security,omitempty"`
}

// Where the keystore keeps keys and matches fingerprints: in the system's
// key service process (no TEE on the phone), or in a trusted application
// in the TEE.
const (
	SecuritySoftware = "software"
	SecurityTEE      = "tee"
)

// FingerprintOKKey is the keypad's fingerprint sensor on the OK key (the
// SKU's keypad fingerprint = "ok-key"): an area sensor under the cap that
// reads a resting finger without a press. Every press of OK touches it, so
// it is powered only while a system prompt asks for a finger, and never
// unlocks the phone.
const FingerprintOKKey = "ok-key"

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
// MIME prefixes) to narrow file.pick, ID for file.open. With Read,
// file.pick and file.open answer with the file's bytes (FileItem.Data)
// instead of an open file, for a runtime that cannot take files
// (TypeScript apps); at most MaxFileRead bytes.
type FileArgs struct {
	ID    string   `json:"id,omitempty"`
	Name  string   `json:"name,omitempty"`
	Data  string   `json:"data,omitempty"`
	Types []string `json:"types,omitempty"`
	Read  bool     `json:"read,omitempty"`
}

// MaxFileRead bounds a file read whole into a reply (FileArgs.Read).
const MaxFileRead = 1 << 20

type FileItem struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Mime string `json:"mime"`
	Size int64  `json:"size"`
	At   int64  `json:"at"`
	// Data: the bytes, base64, when the request asked to read them.
	Data string `json:"data,omitempty"`
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
	// Security: as in KeyProof.
	Security string `json:"security,omitempty"`
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
	// Approximate: the location grant gives only the approximate location.
	Approximate bool `json:"approximate,omitempty"`
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

// Location is a fix: WGS-84 degrees, the horizontal accuracy in metres
// (about one standard deviation) and when it was taken (unix seconds).
// The receiver's altitude above mean sea level, speed over ground (m/s),
// course over ground (degrees clockwise from true north) and satellites
// used come when it gives them, never in an approximate fix: the user
// allows the app only an area of about 2 km (Approximate). Source is
// LocationSourceGNSS or LocationSourceNetwork (Wi-Fi and the mobile cell,
// while the satellites give no fix).
type Location struct {
	Lat         float64  `json:"lat"`
	Lon         float64  `json:"lon"`
	AccuracyM   float64  `json:"accuracy_m"`
	At          int64    `json:"at"`
	AltitudeM   *float64 `json:"altitude_m,omitempty"`
	SpeedMS     *float64 `json:"speed_mps,omitempty"`
	Bearing     *float64 `json:"bearing,omitempty"`
	Satellites  int      `json:"satellites,omitempty"`
	Approximate bool     `json:"approximate,omitempty"`
	Source      string   `json:"source,omitempty"`
}

// LocationGetArgs: location.get takes a fix up to MaxAgeS old (default: a
// current one) and waits up to TimeoutS for one (default 30, at most 120).
// Approximate: the app needs only the area, whatever the user allowed;
// asking, the system offers only that.
type LocationGetArgs struct {
	MaxAgeS     float64 `json:"max_age_s,omitempty"`
	TimeoutS    float64 `json:"timeout_s,omitempty"`
	Approximate bool    `json:"approximate,omitempty"`
}

// LocationWatchArgs: location.watch sends a "location" event at most every
// IntervalS (default 1, 1 to 3600) and only once the position moved
// MinDistanceM from the last one sent. A new watch replaces the app's
// last. Approximate as for location.get.
type LocationWatchArgs struct {
	IntervalS    float64 `json:"interval_s,omitempty"`
	MinDistanceM float64 `json:"min_distance_m,omitempty"`
	Approximate  bool    `json:"approximate,omitempty"`
}

type JobArgs struct {
	ID       string `json:"id"`
	EveryMin int    `json:"every_min,omitempty"`
	// Charging: run only on external power (backups, cache cleanup). A
	// run that is going when the charger is unplugged is stopped (a
	// job.stop event) and run again once the phone is plugged in.
	Charging bool `json:"charging,omitempty"`
	// Unmetered: run only on an unmetered network (large downloads):
	// Wi-Fi that is not metered, never mobile data. A run that is going
	// when the phone leaves such a network is stopped (a job.stop event)
	// and run again once it is back.
	Unmetered bool `json:"unmetered,omitempty"`
	// Network: run only with a network the app may use: any, but Wi-Fi
	// while its mobile data is blocked in the background (NetState). A
	// run is stopped as an unmetered one is when that network goes.
	Network bool `json:"network,omitempty"`
}

// KeepaliveArgs are net.keepalive's: the server to connect to (TCP; the
// app speaks its own protocol, TLS included, on the socket) and how long
// the connection may stay silent, KeepaliveMinSec to KeepaliveMaxSec
// (EverySec: the heartbeat interval its server or the network's NAT
// needs). A new call replaces the app's connection; Close ends it.
type KeepaliveArgs struct {
	Host     string `json:"host,omitempty"`
	Port     int    `json:"port,omitempty"`
	EverySec int    `json:"every_s,omitempty"`
	Close    bool   `json:"close,omitempty"`
}

// KeepaliveRes is net.keepalive's answer, with the connected socket as its
// one fd: the interval granted, the push connection's heartbeat now (0
// before it is known), whose wake windows the heartbeats share, and the
// address connected to.
type KeepaliveRes struct {
	EverySec int    `json:"every_s"`
	PushSec  int    `json:"push_s"`
	Addr     string `json:"addr,omitempty"`
}

const (
	KeepaliveMinSec = 60
	KeepaliveMaxSec = 3600
)

// NetState is net.state's answer and a net.state event's value: the
// network the phone is on (NetWifi, NetMobile, NetNone; Metered: mobile
// data, or a Wi-Fi network that counts as metered), whether the app may
// use mobile data in the background (BackgroundData false: the user
// turned it off for the app, or Data Saver is on and the app is not
// exempt; DataSaver: Data Saver is on), and why its mobile data is
// blocked now, whatever network the phone is on (Blocked, NetBlocked*; ""
// it is not): on mobile data its connections then fail at once (EPERM).
// Limit is its own mobile data limit a billing cycle (0 none), Used what
// it used of it, at home and abroad.
type NetState struct {
	Network        string `json:"network"`
	Metered        bool   `json:"metered,omitempty"`
	BackgroundData bool   `json:"background_data"`
	DataSaver      bool   `json:"data_saver,omitempty"`
	Blocked        string `json:"blocked,omitempty"`
	Limit          uint64 `json:"limit,omitempty"`
	Used           uint64 `json:"used,omitempty"`
}

// JobStop is a job.stop event's value: the job, and why (JobStopMetered,
// JobStopOffline, JobStopBlocked, JobStopUnplugged).
type JobStop struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
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

// TrafficUsage is Settings › Data usage (settings.get "data_usage"): the
// billing cycle (Start its first day, End the day after its last, local
// dates), what it used by network kind and line, today's and the previous
// cycle's mobile data, the plan, and where it stands. Mobile is the home
// network's, which the plan counts; Roaming is mobile data abroad, apart.
// Held: the plan's hard limit holds mobile data off; Override: the user
// turned it on past the limit until the cycle ends or the limit rises.
// Apps are the apps with the network permission, by name; DataSaver: Data
// Saver is on.
type TrafficUsage struct {
	Source      string        `json:"source"`
	Start       string        `json:"start"`
	End         string        `json:"end"`
	Mobile      uint64        `json:"mobile"`
	Wifi        uint64        `json:"wifi"`
	Roaming     uint64        `json:"roaming,omitempty"`
	MobileToday uint64        `json:"mobile_today"`
	PrevMobile  uint64        `json:"prev_mobile"`
	Lines       []TrafficLine `json:"lines"`
	Plan        DataPlan      `json:"plan"`
	Held        bool          `json:"held,omitempty"`
	Override    bool          `json:"override,omitempty"`
	Apps        []TrafficApp  `json:"apps"`
	DataSaver   bool          `json:"data_saver,omitempty"`
}

// TrafficApp is an app's own data settings (SettingApp*): its background
// data off, exempt from Data Saver, its mobile data limit a cycle (0
// none), and WifiOnly: App permissions keeps it to Wi-Fi. Used is its
// mobile data this cycle at home and abroad, which the limit counts, and
// Blocked why its mobile data is blocked now (NetBlocked*, "" not;
// Settings being on the screen, an app held back in the background says
// so).
type TrafficApp struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	BackgroundOff bool   `json:"background_off,omitempty"`
	Unrestricted  bool   `json:"unrestricted,omitempty"`
	WifiOnly      bool   `json:"wifi_only,omitempty"`
	Limit         uint64 `json:"limit,omitempty"`
	Used          uint64 `json:"used"`
	Blocked       string `json:"blocked,omitempty"`
}

// TrafficLine is what an app (by ID, with its Name) or another line
// (TrafficLine*) used in the cycle, in bytes by network kind and direction.
type TrafficLine struct {
	ID       string `json:"id"`
	Name     string `json:"name,omitempty"`
	MobileRx uint64 `json:"mobile_rx"`
	MobileTx uint64 `json:"mobile_tx"`
	WifiRx   uint64 `json:"wifi_rx"`
	WifiTx   uint64 `json:"wifi_tx"`
	RoamRx   uint64 `json:"roam_rx,omitempty"`
	RoamTx   uint64 `json:"roam_tx,omitempty"`
}

// DataPlan is the mobile data plan as set (the SettingData* keys).
// Warning is the warning's level in bytes (0: none, or a share of an
// allowance not set), WarningPercent its share of the allowance (0: an
// amount of its own, or no warning).
type DataPlan struct {
	Limit          uint64 `json:"limit,omitempty"`
	CycleDay       int    `json:"cycle_day"`
	Warning        uint64 `json:"warning,omitempty"`
	WarningPercent int    `json:"warning_percent,omitempty"`
	LimitOff       bool   `json:"limit_off,omitempty"`
}

// EnergyEntry is an app's CPU over the last day, in the foreground and in
// the background (settings.get "energy": the Battery page).
type EnergyEntry struct {
	App          string `json:"app"`
	Name         string `json:"name"`
	ForegroundMS int64  `json:"fg_ms"`
	BackgroundMS int64  `json:"bg_ms"`
	// Keepalive: what its own connection (net.keepalive) cost over the
	// day, for an app that had one.
	Keepalive *KeepaliveUse `json:"keepalive,omitempty"`
}

// KeepaliveUse counts an app's own connection's wakes over the last day:
// the ones the system made for its heartbeat alone (Own), the heartbeats
// that rode wakes made anyway (Shared), the thaws for data arriving
// (Data), and the charge estimated for its own wakes and its data, in mAh
// (push.LTEWake; 28-battery.md §8).
type KeepaliveUse struct {
	Own    int     `json:"own"`
	Shared int     `json:"shared"`
	Data   int     `json:"data"`
	MAh    float64 `json:"mah"`
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
// picture's size, FPS the frames a second it comes at.
type VideoState struct {
	Player string `json:"player"`
	Stream string `json:"stream"`
	State  string `json:"state"` // playing | paused | ended | failed
	PosMS  int64  `json:"pos_ms"`
	DurMS  int64  `json:"duration_ms"`
	W      int    `json:"w"`
	H      int    `json:"h"`
	Sound  bool   `json:"sound"`
	FPS    int    `json:"fps,omitempty"`
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
	// MeteredChoice is the user's (MeteredYes, MeteredNo; "" automatic);
	// Metered whether the network counts as metered (the connected one:
	// the choice, else what was detected, MeteredHint).
	MeteredChoice string `json:"metered_choice,omitempty"`
	Metered       bool   `json:"metered,omitempty"`
	MeteredHint   string `json:"metered_hint,omitempty"`
}

// WifiArgs: wifi.set takes Enabled, wifi.connect SSID and (for a new psk
// network) Passphrase, wifi.forget SSID. A new enterprise (802.1X) network
// takes the user's name (Identity) with the password in Passphrase, the
// domain the server's certificate must name (Domain: without it any server
// could collect the password) and the EAP method: peap (the default) or
// ttls with MSCHAPv2 inside, ttls-pap with PAP inside. wifi.set with SSID
// and Metered (MeteredAuto, MeteredYes, MeteredNo) sets the network's
// metered choice.
type WifiArgs struct {
	Enabled    *bool  `json:"enabled,omitempty"`
	Metered    string `json:"metered,omitempty"`
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
// switch is on, no link yet) | roaming (roaming where a roaming switch
// keeps it down) | off (the switch or airplane mode), "" without
// telephony. DataRoaming is the data roaming switch (international
// roaming, off by default), NationalRoaming the national roaming switch
// (other networks of the SIM's country, on by default); Roaming: national
// | international while the phone roams. APN is the access point the link
// uses, Address the link's address while connected (IPv4, else IPv6).
// LimitHeld: the data plan's hard limit holds mobile data off (MobileData
// is false); radio.set's mobile_data on is then the user going past it,
// until the cycle ends or the limit rises. IMS: the modem's IMS (VoLTE)
// registration as telephonyd last read it, registered | registering |
// not-registered | limited-registered | unknown (IMSRegistered ...), ""
// without telephony.
type RadioState struct {
	Airplane        bool   `json:"airplane"`
	MobileData      bool   `json:"mobile_data"`
	Cellular        string `json:"cellular"`
	Data            string `json:"data"`
	DataRoaming     bool   `json:"data_roaming"`
	NationalRoaming bool   `json:"national_roaming"`
	Roaming         string `json:"roaming,omitempty"`
	APN             string `json:"apn,omitempty"`
	Address         string `json:"address,omitempty"`
	LimitHeld       bool   `json:"limit_held,omitempty"`
	IMS             string `json:"ims,omitempty"`
}

// The modem's IMS (VoLTE) registration (RadioState.IMS), which calls on
// 4G and 5G need: registered, registering, not registered, registered
// with limited service (QMI IMSA's limited registration), or unknown: the
// phone cannot read it (nothing reads its modem's, or the reading failed)
// or is not registered on 4G or 5G, where alone it is read.
const (
	IMSRegistered    = "registered"
	IMSRegistering   = "registering"
	IMSNotRegistered = "not-registered"
	IMSLimited       = "limited-registered"
	IMSUnknown       = "unknown"
)

// RadioArgs: radio.set changes any of the switches.
type RadioArgs struct {
	Airplane        *bool `json:"airplane,omitempty"`
	MobileData      *bool `json:"mobile_data,omitempty"`
	DataRoaming     *bool `json:"data_roaming,omitempty"`
	NationalRoaming *bool `json:"national_roaming,omitempty"`
}

// APN is one of the SIM's access point names. Types: default (the
// internet), mms, ims, ia (the LTE attach), supl, xcap, emergency; IPType
// ipv4 | ipv6 | ipv4v6; Auth none | pap | chap | pap-chap. PasswordSet
// says whether it has a password; apn.set keeps it unless Password is
// given. Edited: the user added or changed it.
type APN struct {
	ID          string   `json:"id,omitempty"`
	Name        string   `json:"name"`
	APN         string   `json:"apn"`
	Types       []string `json:"types"`
	IPType      string   `json:"ip_type"`
	Auth        string   `json:"auth"`
	User        string   `json:"user"`
	Password    *string  `json:"password,omitempty"`
	PasswordSet bool     `json:"password_set,omitempty"`
	MMSC        string   `json:"mmsc,omitempty"`
	MMSProxy    string   `json:"mms_proxy,omitempty"`
	MMSPort     int      `json:"mms_port,omitempty"`
	Edited      bool     `json:"edited,omitempty"`
}

// APNState is apn.list and apn.set's answer: the SIM's network (MCC, MNC;
// empty while unknown), its SPN, its carrier ("" when the system has no
// APNs for it: the network's default APN is used), its APNs, the internet
// APN in use and whether the list is the user's (Edited; reset brings the
// carrier's back).
type APNState struct {
	MCC      string `json:"mcc"`
	MNC      string `json:"mnc"`
	SPN      string `json:"spn"`
	Carrier  string `json:"carrier"`
	APNs     []APN  `json:"apns"`
	Selected string `json:"selected"`
	Edited   bool   `json:"edited"`
}

// APNArgs: apn.set's Op is save (APN: with its ID it changes that one,
// else it adds one), delete and select (ID: the internet APN to use) or
// reset.
type APNArgs struct {
	Op  string `json:"op"`
	APN *APN   `json:"apn,omitempty"`
	ID  string `json:"id,omitempty"`
}

const (
	APNSave   = "save"
	APNDelete = "delete"
	APNSelect = "select"
	APNReset  = "reset"
)

// HotspotState is hotspot.state and the answer of hotspot.set. Backend:
// "iwd", "sim" (a developer machine) or "" (no Wi-Fi). Enabled is the
// switch, Started the access point being up; Passphrase is the password,
// which Settings shows; Clients: the devices connected now, Devices who
// they are; Upstream: the interface shared (the default route's), "" when
// there is none. Security, Hidden, Band and MaxClients are the user's
// settings; Securities, CanHide and Bands what this phone's access point
// can do (iwd's: WPA2 only, never hidden; 5 GHz where the radio and the
// country allow an access point there).
type HotspotState struct {
	Backend    string `json:"backend"`
	Enabled    bool   `json:"enabled"`
	Started    bool   `json:"started"`
	SSID       string `json:"ssid"`
	Passphrase string `json:"passphrase"`
	Clients    int    `json:"clients"`
	Upstream   string `json:"upstream,omitempty"`
	Security   string `json:"security"`
	Hidden     bool   `json:"hidden,omitempty"`
	Band       string `json:"band"`
	// MaxClients: the devices let on at once, 0 for no limit; one more
	// is sent away.
	MaxClients int             `json:"max_clients,omitempty"`
	Securities []string        `json:"securities"`
	CanHide    bool            `json:"can_hide,omitempty"`
	Bands      []string        `json:"bands"`
	Devices    []HotspotDevice `json:"devices"`
	Usage      HotspotUsage    `json:"usage"`
	// DailyLimit: the bytes the devices may use in a day, 0 for no limit;
	// near it a warning, at it the hotspot goes off (once a day).
	DailyLimit uint64 `json:"daily_limit,omitempty"`
}

// The hotspot's security (WPA2-Personal, WPA3-Personal, or both at once:
// WPA3's transition mode) and bands (GHz).
const (
	HotspotWPA2     = "wpa2"
	HotspotWPA3     = "wpa3"
	HotspotWPA2WPA3 = "wpa2-wpa3"
	HotspotBand24   = "2.4"
	HotspotBand5    = "5"
	// HotspotMaxClients is the highest device limit Settings offers.
	HotspotMaxClients = 10
)

// HotspotDevice is a device on the hotspot: its Wi-Fi address, the host
// name it gave in its DHCP request ("" before one), the address it got
// and since when it is connected (Unix seconds).
type HotspotDevice struct {
	MAC     string `json:"mac"`
	Name    string `json:"name,omitempty"`
	Address string `json:"address,omitempty"`
	Since   int64  `json:"since"`
}

// HotspotUsage is what the devices downloaded and uploaded through the
// hotspot, in bytes as the access point's interface counts them: since
// it came on (Session…, the last session while it is off) and today.
// LimitReached: today's use is at the daily limit.
type HotspotUsage struct {
	SessionDown  uint64 `json:"session_down"`
	SessionUp    uint64 `json:"session_up"`
	TodayDown    uint64 `json:"today_down"`
	TodayUp      uint64 `json:"today_up"`
	LimitReached bool   `json:"limit_reached,omitempty"`
}

// HotspotArgs: hotspot.set takes Enabled, a new SSID (1 to 32 bytes) or
// Passphrase (8 to 63 printable ASCII characters), Security (one of the
// state's Securities), Hidden (where CanHide), Band (one of Bands) and
// MaxClients (0 to HotspotMaxClients), DailyLimit (bytes, 0 none), or
// several at once; a new name, passphrase, security, band or hiding
// restarts a hotspot that is on, a new device limit holds for the devices
// that join next.
type HotspotArgs struct {
	Enabled    *bool   `json:"enabled,omitempty"`
	SSID       string  `json:"ssid,omitempty"`
	Passphrase string  `json:"passphrase,omitempty"`
	Security   string  `json:"security,omitempty"`
	Hidden     *bool   `json:"hidden,omitempty"`
	Band       string  `json:"band,omitempty"`
	MaxClients *int    `json:"max_clients,omitempty"`
	DailyLimit *uint64 `json:"daily_limit,omitempty"`
}

// The kinds of data moved from an old phone, as kario-switch/1 names them.
const (
	SwitchContacts = "contacts"
	SwitchCallLog  = "call_log"
	SwitchSMS      = "sms"
	SwitchCalendar = "calendar"
	SwitchPhotos   = "photos"
	SwitchVideos   = "videos"
)

// SwitchKinds are all of them, in the order Settings lists them.
var SwitchKinds = []string{SwitchContacts, SwitchCallLog, SwitchSMS, SwitchCalendar, SwitchPhotos, SwitchVideos}

// A switch session's states: none, the hotspot coming up, the code on the
// screen with no old phone yet, and receiving; and how one ended.
const (
	SwitchIdle      = "idle"
	SwitchStarting  = "starting"
	SwitchWaiting   = "waiting"
	SwitchReceiving = "receiving"

	SwitchEndedDone    = "done"
	SwitchEndedIdle    = "timeout"
	SwitchEndedStopped = "stopped"
	SwitchEndedNetwork = "network"
)

// The network a session listened on that went away under it (the Wi-Fi
// disconnected, moved to another network or changed the phone's address;
// the hotspot turned off): SwitchState.Lost.
const (
	SwitchLostWifi    = "wifi"
	SwitchLostHotspot = "hotspot"
)

// SwitchStartArgs: switch.start takes the kinds the user ticked; Hotspot:
// receive over the personal hotspot even with Wi-Fi connected, ending a
// session still waiting on the Wi-Fi network for it. Bluetooth: instead,
// read the paired phone with this address over Bluetooth, with nothing
// installed on it (the kinds of SwitchBluetoothKinds).
type SwitchStartArgs struct {
	Kinds     []string `json:"kinds"`
	Hotspot   bool     `json:"hotspot,omitempty"`
	Bluetooth string   `json:"bluetooth,omitempty"`
}

// SwitchBluetoothKinds are what a move over Bluetooth reads: the phone
// book and call histories (PBAP), the messages (MAP).
var SwitchBluetoothKinds = []string{SwitchContacts, SwitchCallLog, SwitchSMS}

// What became of a kind a move over Bluetooth asked for, when not all of
// it came (SwitchProgress.Note): the old phone has no such service, its
// user did not allow it, or it gave only part (an iPhone's messages).
const (
	SwitchNoteNotOffered = "not_offered"
	SwitchNoteRefused    = "refused"
	SwitchNotePartial    = "partial"
)

// SwitchState answers every switch.* call. While waiting or receiving,
// Code is the pairing URL the QR code carries (shown as a secure surface),
// Address where the receiver listens, and either Network and Passphrase,
// the hotspot the old phone joins, or Wifi, the Wi-Fi network the old
// phone has to be on as well; Device is the old phone once it said hello.
// Error: why the last start or undo failed, or why the last session ended
// by itself when Lost (SwitchLost*) says which network went. Last is the
// last import, while it can be undone or right after it was. Bluetooth is
// the old phone's name while a move over Bluetooth runs: no code, and
// waiting is the old phone asking its user to allow it.
type SwitchState struct {
	State      string           `json:"state"`
	Kinds      []string         `json:"kinds,omitempty"`
	Code       string           `json:"code,omitempty"`
	Address    string           `json:"address,omitempty"`
	Network    string           `json:"network,omitempty"`
	Passphrase string           `json:"passphrase,omitempty"`
	Wifi       string           `json:"wifi,omitempty"`
	Bluetooth  string           `json:"bluetooth,omitempty"`
	Device     string           `json:"device,omitempty"`
	Progress   []SwitchProgress `json:"progress,omitempty"`
	Error      string           `json:"error,omitempty"`
	Lost       string           `json:"lost,omitempty"`
	Last       *SwitchSummary   `json:"last,omitempty"`
}

// SwitchProgress is one kind's: what the old phone said it has (0 unknown),
// and what came of it; Note (SwitchNote*) when over Bluetooth not all
// came.
type SwitchProgress struct {
	Kind     string `json:"kind"`
	Expected int    `json:"expected"`
	Stored   int    `json:"stored"`
	Skipped  int    `json:"skipped"`
	Failed   int    `json:"failed"`
	Note     string `json:"note,omitempty"`
}

// SwitchSummary is an import that ended: when, how (SwitchEnded*), from
// which phone, what it added by kind and how many items failed; Undone
// once switch.undo took it out.
type SwitchSummary struct {
	At     int64            `json:"at"`
	Ended  string           `json:"ended"`
	Device string           `json:"device,omitempty"`
	Added  []SwitchProgress `json:"added"`
	Failed int              `json:"failed"`
	Undone bool             `json:"undone,omitempty"`
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
