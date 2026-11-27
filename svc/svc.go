// Package svc names the system services, their sockets and their methods:
// the contract between the shell (the only client but for telephonyd's
// others: phone-smsd, and phone-audiod where it routes calls through a UCM
// profile), the services and their tests. telephonyd, in C++, answers the
// same method names (telephonyd/src/service.cpp).
package svc

// Services, as the shell's broker names them; a service's socket is its
// name plus ".sock" in the run directory.
const (
	Mediad     = "mediad"
	Audiod     = "audiod"
	Keystored  = "keystored"
	Imed       = "imed"
	Pushd      = "pushd"
	Telephonyd = "telephonyd"
)

// Sock is a service's socket file in the run directory.
func Sock(service string) string { return service + ".sock" }

// Sockets and address files in the run directory.
const (
	MediadSock       = Mediad + ".sock"
	MediadFramesSock = "mediad-frames.sock" // frame streams (media.SubscribeReq)
	AudiodSock       = Audiod + ".sock"
	KeystoredSock    = Keystored + ".sock"
	ImedSock         = Imed + ".sock"
	PushdSock        = Pushd + ".sock"
	TelephonydSock   = Telephonyd + ".sock"
	ChatsrvAddr      = "chatsrv.addr"  // the chat server's listening address
	StoresrvAddr     = "storesrv.addr" // the store server's
	PushgwAddr       = "pushgw.addr"   // the two push gateway instances'
	PushgwBAddr      = "pushgw-b.addr"
)

// mediad: camera, call media, the scanner, video playback.
const (
	MediaCallStart     = "call.start"
	MediaCallControl   = "call.control"
	MediaCallEnd       = "call.end"
	MediaCameraOpen    = "camera.open"
	MediaCameraCapture = "camera.capture"
	MediaCameraClose   = "camera.close"
	MediaInfo          = "info"
	MediaScanOpen      = "scan.open"
	MediaScanWait      = "scan.wait"
	MediaScanClose     = "scan.close"
	// Test hook: {text} is a QR code held up to the camera from now on,
	// "" none; only with the synthetic camera or the kernel's test driver
	// (vivid), refused with a sensor unless mediad runs with --test-codes.
	MediaSimCode       = "sim.code"
	MediaPlayerProbe   = "player.probe"
	MediaPlayerOpen    = "player.open"
	MediaPlayerControl = "player.control"
	MediaPlayerClose   = "player.close"
)

// audiod: sounds, streams, volume and routing.
const (
	AudioPlay    = "play"
	AudioOpen    = "open"
	AudioControl = "control"
	AudioState   = "state"
	AudioVolume  = "volume"
	AudioRoute   = "route"
	// AudioChannels {mono, balance}: Settings › Accessibility's mono audio
	// and left/right balance (-1 to 1) for everything played.
	AudioChannels = "channels"
	// The speech service (package audio/tts): SpeechSay {text, lang,
	// interrupt} speaks (interrupt: cut what is being said, else after
	// it), SpeechStop stops, SpeechState answers tts.State.
	SpeechSay   = "speech.say"
	SpeechStop  = "speech.stop"
	SpeechState = "speech.state"
	// AudioOpen's directions: the caller writes, or the microphone does.
	AudioDirPlay   = "play"
	AudioDirRecord = "record"
	// The event of a stream's change.
	AudioEventStream = "stream"
	// AudioOutputs answers the outputs connected and where each sound
	// goes (audio/route's Decision, with media's volume on its output);
	// AudioOutputPick {id} is the user's choice of the output for media
	// ("" the one connected last); AudioOutputSim {op: plug | unplug, id,
	// kind, name, mic, hw_volume} is a selftest's stand-in for a device,
	// refused unless audiod runs with --sim-outputs. AudioEventOutputs
	// tells each change ({outputs}; noisy: the output whose going paused
	// media).
	AudioOutputs      = "outputs"
	AudioOutputPick   = "output.pick"
	AudioOutputSim    = "output.sim"
	AudioEventOutputs = "outputs"
)

// keystored: keys, attestation, provisioning.
const (
	KeyAttest      = "attest"
	KeyForget      = "forget"
	KeyCSR         = "csr"
	KeyInstallCert = "install_cert"
	KeySetUnlocked = "set_unlocked"
	KeySign        = "sign"
	KeyPushKey     = "push_key"
	KeyPushOpen    = "push_open"
	KeyConfirm     = "confirm"
)

// imed: the input method.
const (
	IMEOpen   = "open"
	IMEKey    = "key"
	IMETick   = "tick"
	IMECancel = "cancel"
	IMEState  = "state"
	IMEStats  = "stats"
)

// pushd: the push connection (the shell's side).
const (
	PushRegister    = "register"
	PushUnregister  = "unregister"
	PushRadioActive = "radio_active"
	PushPending     = "pending"
	PushAck         = "ack"
	PushState       = "state"
	PushEvent       = "push"
	// PushWindowEvent: a line came on the gateway connection (a heartbeat,
	// or the gateway answering a change of interval), so the radio is up;
	// heartbeat_ms is the connection's interval.
	PushWindowEvent = "window"
)

// telephonyd: calls, SMS, the SIM; and its events.
const (
	TelStatus       = "status"
	TelCalls        = "calls"
	TelDial         = "dial"
	TelAnswer       = "answer"
	TelHangup       = "hangup"
	TelHold         = "hold"
	TelMute         = "mute"
	TelDTMF         = "dtmf"
	TelLog          = "log"
	TelSMSSend      = "sms.send"
	TelSimUnlock    = "sim.unlock"
	TelSimPinLock   = "sim.pin_lock"
	TelSimChangePin = "sim.change_pin"
	TelUSSDInitiate = "ussd.initiate"
	TelUSSDRespond  = "ussd.respond"
	TelUSSDCancel   = "ussd.cancel"
	// TelLogImport {entries: [{number, dir, result, at, duration}]}, at most
	// 500: calls another phone kept, put into the log by their time
	// (newest first, the cap of 200 holding) and marked imported; {ids} by
	// place, "" for one left out (malformed, in the log already, or past
	// the cap). TelLogDelete {ids} deletes the imported entries among them.
	TelLogImport = "log.import"
	TelLogDelete = "log.delete"
	// {contacts: [{index, name, number}]}: the SIM's phone book.
	TelPhonebook = "phonebook.read"
	// {on}: the modem's radio (off is airplane mode), the cellular data
	// link and data roaming; telephonyd keeps them across restarts.
	TelRadioSet   = "radio.set"
	TelDataSet    = "data.set"
	TelRoamingSet = "roaming.set"
	// The SIM's APNs: apn.list, apn.set {op, apn, id} (ui.APNArgs), both
	// answering ui.APNState.
	TelAPNList = "apn.list"
	TelAPNSet  = "apn.set"
	// Test hooks of the simulated baseband.
	TelSimRing = "sim.ring"
	TelSimLock = "sim.lock"
	TelSimSMS  = "sim.sms"
	TelSimCB   = "sim.cb" // a cell broadcast: {channel, serial, text}
	TelSimUSSD = "sim.ussd"
	// TelSimHangup {number}: the other party of that call hangs up.
	// TelSimSlow {dial_ms, answer_ms}: dial is answered dial_ms late (the
	// call does not exist until then), the called party answers answer_ms
	// after it rings (default 900).
	TelSimHangup = "sim.hangup"
	TelSimSlow   = "sim.slow"
	// TelSimNetwork: which country the simulated SIM and network are in
	// (MCC), and the time the network sends (NITZ); the SIM's sim_mnc, spn
	// and gid1 when given.
	TelSimNetwork = "sim.network"
	// TelSimRoam {net_mcc, net_mnc, operator}: registered roaming on that
	// network; empty codes: at home again.
	TelSimRoam = "sim.roam"
	// TelSimIMS {state}: the IMS registration it says while registered on
	// 4G (ui.IMSRegistered...; unknown until set).
	TelSimIMS = "sim.ims"

	TelEventStatus = "status"
	TelEventCall   = "call"
	TelEventSMS    = "sms"
	TelEventCB     = "cb"
	TelEventUnlock = "unlock"
	TelEventPin    = "pin"
	TelEventUSSD   = "ussd"
)
