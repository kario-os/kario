// Package svc names the system services, their sockets and their methods:
// the contract between the shell (the only client but for phone-smsd,
// telephonyd's other one), the services and their tests. telephonyd, in
// C++, answers the same method names (telephonyd/src/service.cpp).
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
	// AudioOpen's directions: the caller writes, or the microphone does.
	AudioDirPlay   = "play"
	AudioDirRecord = "record"
	// The event of a stream's change.
	AudioEventStream = "stream"
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
	TelLog          = "log"
	TelSMSSend      = "sms.send"
	TelSimUnlock    = "sim.unlock"
	TelSimPinLock   = "sim.pin_lock"
	TelSimChangePin = "sim.change_pin"
	TelUSSDInitiate = "ussd.initiate"
	TelUSSDRespond  = "ussd.respond"
	TelUSSDCancel   = "ussd.cancel"
	// {on}: the modem's radio (off is airplane mode) and the cellular data
	// link; telephonyd keeps both across restarts.
	TelRadioSet = "radio.set"
	TelDataSet  = "data.set"
	// Test hooks of the simulated baseband.
	TelSimRing = "sim.ring"
	TelSimLock = "sim.lock"
	TelSimSMS  = "sim.sms"
	TelSimCB   = "sim.cb" // a cell broadcast: {channel, serial, text}
	TelSimUSSD = "sim.ussd"
	// TelSimNetwork: which country the simulated SIM and network are in
	// (MCC), and the time the network sends (NITZ).
	TelSimNetwork = "sim.network"

	TelEventStatus = "status"
	TelEventCall   = "call"
	TelEventSMS    = "sms"
	TelEventCB     = "cb"
	TelEventUnlock = "unlock"
	TelEventPin    = "pin"
	TelEventUSSD   = "ussd"
)
