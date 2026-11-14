package ui

import "strings"

// The vocabulary apps and the system share besides capabilities: one place,
// so that both sides spell a word the same.

// Lifecycle states (MsgLifecycle).
const (
	StateForeground = "foreground"
	StateBackground = "background"
)

// NotifUnusedPerms: the system's notice, in Settings' name, that grants of
// apps left unused were taken back; opening it shows Settings › App
// permissions.
const NotifUnusedPerms = "unused-perms"

// Action IDs the system gives a meaning to (22-developer-api.md §3).
const (
	ActionOpen   = "open"   // OK on an item that leads somewhere; → opens it too
	ActionCall   = "call"   // the call key
	ActionDial   = "dial"   // the call key on a number
	ActionHangup = "hangup" // the end key
)

// Media kinds (media.list, media.pick) and media IDs ("media:p4").
const (
	MediaAudio  = "audio"
	MediaImage  = "image"
	MediaVideo  = "video"
	MediaPrefix = "media:"
)

// Playback states of audio streams and video players.
const (
	PlayPlaying = "playing"
	PlayPaused  = "paused"
	PlayEnded   = "ended"
	PlayFailed  = "failed"
	PlayStopped = "stopped"
)

// Ops of audio.control and video.control.
const (
	OpPause  = "pause"
	OpResume = "resume"
	OpStop   = "stop"
	OpSeek   = "seek"
	OpState  = "state"
	OpClose  = "close"
	// Music only: the track to play after this one (Next, "" for none),
	// and the output options (ReplayGain, DSD).
	OpQueue   = "queue"
	OpOptions = "options"
)

// ErrCancelled ends the error of a request the user declined (a system
// dialog, a picker); Cancelled tells it from a failure.
const ErrCancelled = "cancelled"

// ErrNoHandler: no app handles the intent's action.
const ErrNoHandler = "no app handles this"

// ErrNeedPassphrase: wifi.connect to a protected network the phone does not
// know yet, without its passphrase.
const ErrNeedPassphrase = "the network needs its passphrase"

// ErrNeedCredentials: wifi.connect to an enterprise (802.1X) network the
// phone does not know yet, without the user's name, password and domain.
const ErrNeedCredentials = "the network needs a user name, password and domain"

// ErrNeedsUser: a background update (PkgArgs.Auto) the system will not
// install by itself (new permissions, the app in use); the user updates.
const ErrNeedsUser = "the update needs the user"

func Cancelled(err error) bool { return err != nil && strings.HasSuffix(err.Error(), ErrCancelled) }

// settings.set keys (system apps; 22-developer-api.md §4.5).
const (
	SettingAutoLock       = "auto_lock"
	SettingScreenOff      = "screen_off"
	SettingRingVolume     = "ring_volume"
	SettingMediaVolume    = "media_volume"
	SettingNotifyVolume   = "notify_volume"
	SettingSilent         = "silent"
	SettingIMEFuzzy       = "ime_fuzzy"
	SettingDeveloperMode  = "developer_mode"
	SettingDeviceUnlocked = "device_unlocked"
	SettingCrashSend      = "crash_send"
	SettingTheme          = "theme"
	SettingLargeText      = "large_text"
	SettingWallpaperClear = "wallpaper_clear"
	SettingSimPinLock     = "sim_pin_lock"
	SettingSimPinChange   = "sim_pin_change"
	// Language, region and time zone take the 1-based index in the lists
	// settings.get gives (languages, regions, zones), 0 for automatic;
	// auto_time 1 follows network time, 0 lets the user set it with time:
	// the wall-clock date and time in the zone in force, as seconds since
	// 1970-01-01 00:00 of that wall clock.
	SettingLanguage = "language"
	SettingRegion   = "region"
	SettingTimeZone = "time_zone"
	SettingAutoTime = "auto_time"
	SettingTime     = "time"
	SettingClock24  = "clock_24h"
	// Emergency alerts by level, 1 shown, 0 not (32-messaging.md): the
	// highest level (AlertPresidential) has no key, it is always shown.
	SettingAlertExtreme = "alert_extreme"
	SettingAlertSevere  = "alert_severe"
	SettingAlertAmber   = "alert_amber"
	SettingAlertTest    = "alert_test"
)

// Settings › USB (27-usb-typec.md §3.3): what a trusted computer gets,
// settings.set usb_mode 1 file transfer (MTP), 0 network sharing (the
// default); settings.get's "usb" says which in "mode".
const (
	SettingUSBMode = "usb_mode"
	USBModeMTP     = "mtp"
	USBModeNetwork = "network"
)

// settings.set keys of Settings › Security (31-lock-and-biometrics.md).
// The PIN is never in them: lock_pin (1 set or change, 0 remove) and
// fingerprint_auth / fingerprint_enroll raise the system's own screens and
// the reply waits for them. fingerprint_rename takes the finger's ID as
// the value and the name as the text; fingerprint_delete the ID.
const (
	SettingLockPIN           = "lock_pin"
	SettingLockGrace         = "lock_grace"
	SettingLockShowContent   = "lock_show_content"
	SettingFingerprintAuth   = "fingerprint_auth"
	SettingFingerprintEnroll = "fingerprint_enroll"
	SettingFingerprintRename = "fingerprint_rename"
	SettingFingerprintDelete = "fingerprint_delete"
	SettingFingerprintUnlock = "fingerprint_unlock"
	SettingFingerprintPay    = "fingerprint_pay"
)

// wallpaper.set targets.
const (
	WallpaperIdle = "idle"
	WallpaperLock = "lock"
	WallpaperBoth = "both"
)

// system.update actions.
const (
	UpdateStatus  = "status"
	UpdateCheck   = "check"
	UpdateInstall = "install"
	UpdateReboot  = "reboot"
)

// web.control ops.
const (
	WebReload    = "reload"
	WebBack      = "back"
	WebForward   = "forward"
	WebStop      = "stop"
	WebZoom      = "zoom"
	WebFind      = "find"
	WebFindNext  = "find_next"
	WebFindPrev  = "find_prev"
	WebFindEnd   = "find_end"
	WebClearData = "clear_data"
	// WebSites: the system lists the sites that keep data for this app
	// (cookies, storage, cache) and deletes the one the user picks; the
	// app sees neither the list nor the data.
	WebSites = "sites"
)

// Events the system sends apps (MsgEvent; 22-developer-api.md §5).
const (
	EventOpen       = "open"
	EventPush       = "push"
	EventAudio      = "audio"
	EventMedia      = "media"
	EventSettings   = "settings"
	EventAlarm      = "alarm.fire"
	EventJob        = "job"
	EventSMS        = "sms"
	EventWeb        = "web"
	EventTelCall    = "tel.call"    // the dialer: a cellular call changed
	EventTelOpen    = "tel.open"    // the dialer: show this call
	EventUSSD       = "tel.ussd"    // the dialer: the network sent USSD (ui.USSDReply)
	EventCallResume = "call.resume" // back to a call from the system menu
	// The keystore was cleared (device unlocked or locked again): the app's
	// push key is new, its server must get it before sealing again.
	EventPushKeyChanged = "push.key_changed"
	// EventFocus: a system dialog or menu came over the app's screen, or the
	// screen went off (FocusLost), and the keys are the app's again
	// (FocusGained); Android's onWindowFocusChanged. A game pauses on lost.
	EventFocus = "focus"
)

const (
	FocusLost   = "lost"
	FocusGained = "gained"
)

// The environment apps run in: the servers of the simulated cloud, and the
// device root that keys and attestation chain to.
const (
	EnvChatServer  = "PHONE_CHAT_SERVER"
	EnvStoreServer = "PHONE_STORE_SERVER"
	EnvTrustRoot   = "PHONE_TRUST_ROOT"
)
