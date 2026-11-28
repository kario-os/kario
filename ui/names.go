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
	// ActionHide: the system puts the app in the background, its page kept
	// (a call screen while the call goes on), and shows what was in front
	// before it; Back on a page with it does the same. The app is not sent
	// the action.
	ActionHide = "hide"
)

// Media kinds (media.list, media.pick) and media IDs ("media:p4").
const (
	MediaAudio  = "audio"
	MediaImage  = "image"
	MediaVideo  = "video"
	MediaPrefix = "media:"
)

// ContactPhotoPrefix + a contact's Photo is an image src the system draws
// from the contacts store (the contacts permission); on an item with an
// Icon it takes the icon's place.
const ContactPhotoPrefix = "contact:"

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

// Intents the system's apps handle (intent.invoke): the Contacts app saves
// a number as a new contact (payload: a Contact, its number at least;
// result {"id"}), Messages opens the conversation with a number to write
// in (payload: a Contact).
const (
	IntentContactSave    = "contact.save"
	IntentMessageCompose = "message.compose"
)

// ErrNeedPassphrase: wifi.connect to a protected network the phone does not
// know yet, without its passphrase.
const ErrNeedPassphrase = "the network needs its passphrase"

// ErrNeedCredentials: wifi.connect to an enterprise (802.1X) network the
// phone does not know yet, without the user's name, password and domain.
const ErrNeedCredentials = "the network needs a user name, password and domain"

// A Wi-Fi network's metered choice (WifiNet.MeteredChoice, wifi.set's
// metered): automatic, or the user's; and why automatic detection takes a
// network as metered (WifiNet.MeteredHint): its DHCP server says so, or
// its addresses are a phone's hotspot's (package metered).
const (
	MeteredAuto        = "auto"
	MeteredYes         = "metered"
	MeteredNo          = "unmetered"
	MeteredHintDHCP    = "dhcp"
	MeteredHintHotspot = "hotspot"
)

// ErrVPNNeedsKey: vpn.import of a file without a PrivateKey while the
// phone has no key pair of its own waiting (vpn.keygen).
const ErrVPNNeedsKey = "the configuration has no PrivateKey and the phone has no key pair waiting"

// ErrNeedsUser: a background update (PkgArgs.Auto) the system will not
// install by itself (new permissions, the app in use); the user updates.
const ErrNeedsUser = "the update needs the user"

// ErrBusy: the app has too many requests the system has not answered yet,
// or a system dialog of its own is still open (22-developer-api.md §8.1);
// it may ask again once they are answered.
const ErrBusy = "too many requests in flight"

func Cancelled(err error) bool { return err != nil && strings.HasSuffix(err.Error(), ErrCancelled) }

// The sound modes, Android's ringer modes: calls and notifications ring,
// only vibrate, or do neither; alarms sound in all three. A hardware sound
// switch's positions are these (a OnePlus alert slider: top silent, middle
// vibrate, bottom ring), and while the phone has one its position is the
// mode (settings.get's "sound").
const (
	SoundRing    = "ring"
	SoundVibrate = "vibrate"
	SoundSilent  = "silent"
)

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
	// The status bar: the battery's percentage beside its icon (on unless
	// turned off) and the network's speed right now (off unless turned on).
	SettingBatteryPercent = "battery_percent"
	SettingNetSpeed       = "net_speed"
	// SettingBrightness is the backlight in percent, from MinBrightness (the
	// screen never looks off) to 100.
	SettingBrightness = "brightness"
	MinBrightness     = 10
	// SettingAutoBrightness: the backlight follows the light sensor; the
	// brightness set is then its level in an ordinary room.
	SettingAutoBrightness = "auto_brightness"
	// SettingMotion is Settings › Animations: 0 standard, 1 reduced (no
	// transitions), 2 off (nothing moves).
	SettingMotion         = "motion"
	MotionReduced         = "reduced"
	MotionOff             = "off"
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
	// SettingSMSApp is Settings › Apps › Default apps › SMS: the text is
	// the app ID (one with the sms permission), "" the system's Messages;
	// settings.get's "roles" lists the choices.
	SettingSMSApp = "sms_app"
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

// settings.set keys of Settings › Screen time and Minor mode
// (19-core-apps.md): screen_limit takes the app ID as the text and the
// minutes a day as the value (0: no limit); downtime 1 on, 0 off;
// downtime_start and downtime_end the minutes after midnight;
// downtime_now 1 starts downtime now, until its end, 0 ends it;
// downtime_app and minor_app the app ID as the text, 1 allowed, 0 not;
// minor_contact the number as the text, 1 allowed, 0 not. minor_mode (1
// on, 0 off) and parent_pin (change it) raise the system's parental PIN
// screen, which every change asks for while minor mode is on; the PIN is
// never in them. parent_link 1 asks the account server for a code a
// parent enters on its web page, 0 ends that or the binding;
// parent_confirm 1 lets the parent who entered it manage the phone, 0
// refuses; parent_reset 1 asks that parent to confirm a reset of a
// forgotten parental PIN, or, confirmed, raises the system's screen for
// the new one, 0 withdraws it. settings.get's "screen_time" and "minor"
// (its "remote") show them.
const (
	SettingParentLink    = "parent_link"
	SettingParentConfirm = "parent_confirm"
	SettingParentReset   = "parent_reset"
	SettingScreenLimit   = "screen_limit"
	SettingDowntime      = "downtime"
	SettingDowntimeStart = "downtime_start"
	SettingDowntimeEnd   = "downtime_end"
	SettingDowntimeNow   = "downtime_now"
	SettingDowntimeApp   = "downtime_app"
	SettingMinorMode     = "minor_mode"
	SettingMinorApp      = "minor_app"
	SettingMinorContact  = "minor_contact"
	SettingParentPIN     = "parent_pin"
)

// Settings › More settings › Factory reset (31-lock-and-biometrics.md §11):
// factory_reset 1 erases the phone, 2 erases it keeping the anti-theft
// lock on. The system asks for the lock-screen PIN (the parental one in
// minor mode) and a last confirmation on its own screens; the reply comes
// if the user stops there, else the phone restarts. settings.get's
// "reset" shows what the reset page needs.
const (
	SettingFactoryReset = "factory_reset"
	ResetKeepLock       = 2
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

// SettingAutoSystemUpdate (settings.set): 1 lets system updates download
// and install by themselves, then ask to restart (25-system-update.md §6).
const SettingAutoSystemUpdate = "auto_system_update"

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

// The system's calendar app takes a calendar file (.ics) of Files the
// system granted it with the deep link LinkCalendarImport + the file's ID:
// it adds the events it does not have yet, marked with the file, and
// LinkCalendarUndo + the ID takes exactly those out again (with
// LinkCalendarAccount + an account's ID: the events that came from the
// account). LinkCalendarSync: the account's calendar sync has changes for
// the app, or wants all its events (calendar.pull).
const (
	LinkCalendarImport  = "calendar.import:"
	LinkCalendarUndo    = "calendar.undo:"
	LinkCalendarSync    = "calendar.sync:"
	LinkCalendarAccount = "account:"
)

// Events the system sends apps (MsgEvent; 22-developer-api.md §5).
const (
	EventOpen = "open"
	// EventTimeZone: the time zone changed (value: its IANA name); the SDKs
	// put it in force (TZ) before the app hears of it.
	EventTimeZone = "time.zone"
	// EventClock: the system clock moved away from the machine's (value:
	// how far ahead it is in milliseconds, negative behind; "0" again
	// with the machine's). Only a selftest moves it (timeshift,
	// clock-at); the SDKs' clocks (sdk.Now, JavaScript's Date) follow it
	// before the app hears of it, EnvClockOffset at the start.
	EventClock    = "time.clock"
	EventPush     = "push"
	EventAudio    = "audio"
	EventMedia    = "media"
	EventSettings = "settings"
	EventAlarm    = "alarm.fire"
	EventJob      = "job"
	// EventJobStop: a job of the app's that runs must stop (a JobStop):
	// the unmetered network or the charger it asked for went away. The
	// system ends the job's wake a few seconds later and runs it again
	// once the network or the charger is back.
	EventJobStop = "job.stop"
	// EventKeepalive is about the app's own connection (net.keepalive),
	// thawed for it: KeepaliveBeat, send the heartbeat now; KeepaliveData,
	// data arrived on it; KeepaliveClosed, the server closed it (or it
	// broke) and the system let its copy go; KeepaliveLost, a restarted
	// system could not take its copy over: the connection may still work,
	// but nothing wakes the app for it any more until it calls
	// net.keepalive again. The message's text says why (KeepaliveLostFull,
	// ...).
	EventKeepalive  = "keepalive"
	EventSMS        = "sms"
	EventContacts   = "contacts" // the contacts changed: read them again
	EventWeb        = "web"
	EventTelCall    = "tel.call"    // the dialer: a cellular call changed
	EventTelOpen    = "tel.open"    // the dialer: show this call
	EventUSSD       = "tel.ussd"    // the dialer: the network sent USSD (ui.USSDReply)
	EventCallResume = "call.resume" // back to a call from the system menu
	// EventCallHint: the dialer: how the call is hung up and whether touch
	// works on this phone now, a line each, for the call screen ("" none).
	EventCallHint = "call.hint"
	// The keystore was cleared (device unlocked or locked again): the app's
	// push key is new, its server must get it before sealing again.
	EventPushKeyChanged = "push.key_changed"
	// EventFocus: a system dialog or menu came over the app's screen, or the
	// screen went off (FocusLost), and the keys are the app's again
	// (FocusGained); Android's onWindowFocusChanged. A game pauses on lost.
	EventFocus = "focus"
	// EventLocation is a fix of the app's location.watch (a Location).
	// EventLocationOff: the user turned location off in Settings; the
	// watch stays, silent, until it is turned on again.
	EventLocation    = "location"
	EventLocationOff = "location.off"
	// EventLocationStopped: the user stopped the app's location in the
	// background (the system's notification of it); its watch stays,
	// silent, and its requests out of the foreground are refused, until
	// it is in the foreground again.
	EventLocationStopped = "location.stopped"
	// EventNotifyFired: one of the app's scheduled notifications was shown
	// (a NotifyFired); sent to an app that runs, never waking or starting
	// one for it.
	EventNotifyFired = "notify.fired"
	// EventNetState: the block on the app's mobile data changed (a
	// NetState); sent to an app that runs and is not frozen.
	EventNetState = "net.state"
)

// The values of EventKeepalive.
const (
	KeepaliveBeat   = "beat"
	KeepaliveData   = "data"
	KeepaliveClosed = "closed"
	KeepaliveLost   = "lost"
)

// Why a KeepaliveLost came (the event's text): KeepaliveLostFull, the
// system's fd store, which keeps sockets over a crash of the Shell, had
// no room for its copy (other apps used theirs more recently);
// KeepaliveLostDropped, its copy did not come along (it had hung up, or
// the store refused it); KeepaliveLostUnsaved, its copy came without the
// schedule the system keeps for it.
const (
	KeepaliveLostFull    = "full"
	KeepaliveLostDropped = "dropped"
	KeepaliveLostUnsaved = "unsaved"
)

// The network the phone is on (NetState.Network).
const (
	NetWifi   = "wifi"
	NetMobile = "mobile"
	NetNone   = "none"
)

// Why an app's mobile data is blocked (NetState.Blocked,
// TrafficApp.Blocked): App permissions keeps it to Wi-Fi, its own limit
// for the billing cycle is used up, or it is not on the screen while its
// background data is off (its own switch, or Data Saver).
const (
	NetBlockedWifiOnly   = "wifi_only"
	NetBlockedLimit      = "app_limit"
	NetBlockedBackground = "background_data"
)

// Errors of location.get and location.watch; ErrLocationWhileInUse, of
// location.background: the location while in use comes first.
const (
	ErrLocationOff         = "location is off in Settings"
	ErrLocationBackground  = "location is for the foreground app only"
	ErrLocationTimeout     = "no location fix in time"
	ErrLocationUnavailable = "this phone has no location receiver"
	ErrLocationWhileInUse  = "the location must be allowed while in use first"
)

// Settings › Location (settings.set): location 1 on (the default), 0 off
// for every app; location_approximate 1 gives the app named by text (its
// permission key) only its approximate location, 0 the precise one;
// location_network 1 (the default) lets the network location service the
// product names place the phone by Wi-Fi and the mobile cell while the
// satellites give no fix, 0 never asks it.
const (
	SettingLocation        = "location"
	SettingLocationApprox  = "location_approximate"
	SettingLocationNetwork = "location_network"
)

// Where a Location came from: the satellites, or the network location
// service (nearby Wi-Fi and the mobile cell).
const (
	LocationSourceGNSS    = "gnss"
	LocationSourceNetwork = "network"
)

// NotifBgLocation: the system's reminder, in Settings' name, that apps used
// the location in the background; opening it shows Settings › Location.
const NotifBgLocation = "background-location"

// NotifBgLocationUse begins the ID of the system's notification, in
// Settings' name, that an app uses the location in the background now:
// its permission key follows a colon; it offers to stop that, and opening
// it shows Settings › Location.
const NotifBgLocationUse = "background-location-use"

// NotifDataUsage: the system's notice, in Settings' name, that the mobile
// data plan's warning or allowance was reached; opening it shows Settings ›
// Data usage. NotifHotspotLimit: the same for the hotspot's daily limit,
// opening Settings › Personal hotspot.
const (
	NotifDataUsage    = "data-usage"
	NotifHotspotLimit = "hotspot-limit"
)

// NotifAppDataLimit begins the ID of the system's notice, in Settings'
// name, that an app neared or used up its own mobile data limit: the
// app's ID follows a colon; opening it shows the app's data usage page.
const NotifAppDataLimit = "app-data-limit"

// NotifDataRoaming: the system's notice, in Settings' name, that mobile
// data is down because the phone roams where a roaming switch keeps it
// off; it goes when that ends, and opening it shows Settings › Mobile
// network.
const NotifDataRoaming = "data-roaming"

// Why a running job is stopped (JobStop.Reason): the phone moved to a
// metered network (mobile data, a metered Wi-Fi), or to none; or (a job
// that asked for a network) to mobile data, which is blocked for the app;
// the charger of a job that asked for one was unplugged.
const (
	JobStopMetered   = "metered"
	JobStopOffline   = "offline"
	JobStopBlocked   = "blocked"
	JobStopUnplugged = "unplugged"
)

// The mobile data plan (settings.set): data_limit is the monthly allowance
// in bytes (0 none), data_cycle_day the day of the month each billing
// cycle starts (1–31; a shorter month's last day stands in), data_warning a
// warning in bytes (0: a share of the allowance instead) and
// data_warning_percent that share (1–100; 0 no warning), data_limit_off 1
// turns mobile data off once the allowance is used up (0 only tells).
const (
	SettingDataLimit          = "data_limit"
	SettingDataCycleDay       = "data_cycle_day"
	SettingDataWarning        = "data_warning"
	SettingDataWarningPercent = "data_warning_percent"
	SettingDataLimitOff       = "data_limit_off"
)

// Data Saver and an app's own data settings (settings.set; for the app_
// keys text is the app's ID): data_saver 1 lets apps use mobile data only
// while they are on the screen, but those with unrestricted data;
// app_background_data 0 does so for the app alone (1, the default, lifts
// it); app_data_unrestricted 1 exempts it from Data Saver; app_data_limit
// is its mobile data a billing cycle in bytes (0 none): a notice at
// AppDataWarnPercent of it, then its mobile data is blocked until the
// cycle ends or the limit rises.
const (
	SettingDataSaver           = "data_saver"
	SettingAppBackgroundData   = "app_background_data"
	SettingAppDataUnrestricted = "app_data_unrestricted"
	SettingAppDataLimit        = "app_data_limit"
)

// AppDataWarnPercent: an app's own mobile data limit warns at this share.
const AppDataWarnPercent = 80

// Settings › Data usage's lines besides the apps (TrafficLine.ID): the
// hotspot's devices, the system with what no app's counter saw, and apps
// since uninstalled. Where the counts come from (TrafficUsage.Source): the
// per-app firewall's counters with the network interfaces', the
// interfaces' alone (a kernel without the per-app counters), or none (a
// developer machine; the selftest charges bytes).
const (
	TrafficLineHotspot = "hotspot"
	TrafficLineSystem  = "system"
	TrafficLineRemoved = "removed"
	TrafficSourceApps  = "apps"
	TrafficSourceLinks = "links"
	TrafficSourceSim   = "sim"
)

const (
	FocusLost   = "lost"
	FocusGained = "gained"
)

// The environment apps run in: the servers of the simulated cloud, the
// device root that keys and attestation chain to, and the system's
// PlatformLevel (a system that does not say is level 1).
const (
	EnvChatServer  = "PHONE_CHAT_SERVER"
	EnvStoreServer = "PHONE_STORE_SERVER"
	EnvTrustRoot   = "PHONE_TRUST_ROOT"
	EnvPlatform    = "PHONE_PLATFORM"
	// EnvTestForms: the forms of other majors an app's SDK speaks besides
	// its own (ProtoForm, a JSON array), for tests only.
	EnvTestForms = "PHONE_UI_TEST_FORMS"
	// EnvTestSeed: the seed of what an app draws at random (the snake's
	// food), TestSeed in a selftest that compares snapshots; the system
	// never sets it.
	EnvTestSeed = "PHONE_TEST_SEED"
	// EnvClockOffset: the system clock's offset when the app started
	// (EventClock's value); unset with the machine's clock.
	EnvClockOffset = "PHONE_CLOCK_OFFSET_MS"
)

// TestSeed is the EnvTestSeed of a selftest that compares snapshots.
const TestSeed = 1

// MotionLevels are Settings › Animations in order: the setting takes the index.
var MotionLevels = []string{"", MotionReduced, MotionOff}

// Settings › Accessibility (19-core-apps.md §1.4). The switches take 1 on,
// 0 off: high_contrast_text (text in black or white), bold_text,
// color_inversion, screen_reader, screen_reader_on_demand (it speaks only
// when asked), sticky_keys (Shift, Fn, Ctrl and Alt need not be held),
// mono_audio. color_correction, key_feedback and a11y_shortcut take the
// index in ColorCorrections, KeyFeedbacks and A11yShortcuts; magnification
// the zoom, one of MagnificationLevels; key_repeat_delay, slow_keys and
// long_press milliseconds, one of KeyRepeatDelays, SlowKeyDelays and
// LongPressDelays; key_repeat_rate repeats a second, one of KeyRepeatRates
// (0: keys do not repeat); audio_balance -1 (left only) to 1 (right only).
// power_ends_call (Android's "Power button ends call") is a switch too,
// on a board with a power key of its own.
const (
	SettingHighContrast    = "high_contrast_text"
	SettingBoldText        = "bold_text"
	SettingColorInversion  = "color_inversion"
	SettingColorCorrection = "color_correction"
	SettingMagnification   = "magnification"
	SettingScreenReader    = "screen_reader"
	SettingReaderOnDemand  = "screen_reader_on_demand"
	SettingKeyRepeatDelay  = "key_repeat_delay"
	SettingKeyRepeatRate   = "key_repeat_rate"
	SettingStickyKeys      = "sticky_keys"
	SettingSlowKeys        = "slow_keys"
	SettingLongPress       = "long_press"
	SettingKeyFeedback     = "key_feedback"
	SettingMonoAudio       = "mono_audio"
	SettingAudioBalance    = "audio_balance"
	SettingA11yShortcut    = "a11y_shortcut"
	SettingPowerEndsCall   = "power_ends_call"
)

// Colour correction for weak green, red or blue cones (Settings ›
// Accessibility › Colour correction; the frame's "daltonize").
const (
	CorrectDeuteranomaly = "deuteranomaly"
	CorrectProtanomaly   = "protanomaly"
	CorrectTritanomaly   = "tritanomaly"
)

// Key press feedback besides none: a click, a short vibration, or both.
const (
	FeedbackSound   = "sound"
	FeedbackVibrate = "vibrate"
	FeedbackBoth    = "both"
)

// What the accessibility shortcut (both volume keys at once) does besides
// zooming (""): the screen reader or colour inversion on and off, or
// nothing.
const (
	ShortcutReader = "screen-reader"
	ShortcutInvert = "color-inversion"
	ShortcutOff    = "off"
)

// The accessibility settings' choices, in the order Settings steps
// through them; the defaults are the Default* values.
var (
	ColorCorrections    = []string{"", CorrectDeuteranomaly, CorrectProtanomaly, CorrectTritanomaly}
	KeyFeedbacks        = []string{"", FeedbackSound, FeedbackVibrate, FeedbackBoth}
	A11yShortcuts       = []string{"", ShortcutReader, ShortcutInvert, ShortcutOff}
	MagnificationLevels = []int{2, 3}
	KeyRepeatDelays     = []int{250, 500, 1000, 2000}
	KeyRepeatRates      = []int{30, 15, 8, 4, 0}
	SlowKeyDelays       = []int{0, 300, 500, 1000}
	LongPressDelays     = []int{400, 600, 1000, 1500}
)

// The defaults: the kernel's key repeat (250 ms, then 30 a second) and the
// long press the renderers had before it could be set.
const (
	DefaultMagnification  = 2
	DefaultKeyRepeatDelay = 250
	DefaultKeyRepeatRate  = 30
	DefaultLongPress      = 600
)
