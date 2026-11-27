package shell

import (
	"bufio"
	"bytes"
	"encoding/json"
	"encoding/xml"
	"errors"
	"math"
	"regexp"

	_ "embed"
	"fmt"
	"image/jpeg"
	"io"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/kario-os/kario/audio"
	"github.com/kario-os/kario/cts"
	"github.com/kario-os/kario/hal/fingerprint"
	"github.com/kario-os/kario/obex"
	"github.com/kario-os/kario/obex/btphone"
	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/ui"
)

//go:embed selftest.script
var SelftestScript string

// CTSScript is appended to the selftest by phone-shell --cts.
//
//go:embed cts.script
var CTSScript string

// Script is a list of steps, one per line:
//
//	== <case name>          start a named case (reported PASS/FAIL)
//	#part <name>            the cases below belong to that part (parts.go); in one Shell with other parts' cases, a part
//	                        gets back what it left there (partstate.go: the clock, the battery, the light sensor, the
//	                        freeze delay, screen-auto, system-sleep, nfc picker), the boot's the first time; the
//	                        jobs' next runs move with the clock, keeping their waits
//	key <name> [count]      press a key (enter esc tab up down left right backspace f2 f3 f4, power: the power key)
//	type <text>             type text
//	fill <text>             set the focused input to text, past the input method, once the keys
//	                        before it have reached the app; {chat} is the simulated chat server's
//	                        URL (its test pages), {fetch} fetch-server's, {keepalive} keep-server's address
//	fetch-server <host:port>  an HTTP server on the phone's loopback that answers "ok" (port 0: a free one); a stream,
//	                        a request it holds, cookies and a WebSocket echo on the paths of fetch_headless.go
//	fetch-got <METHOD> <path> <text> [ms]  it got such a request with text in its body (WS: a WebSocket message; GET
//	                        /hold gone: the client went away)
//	keep-server, keep-send, keep-got, keep-conns, keep-reset, keepalive, keepalive-due, keepalive-net   apps' own connections
//	                        (keepalive_headless.go)
//	long <char>|<key>       hold a key (9-key: digit, # = 123, * = symbols; end, power: the power menu)
//	button power [ms]       the board's own power button pressed for real when asked on the output
//	                        (PHONE-SELFTEST-PRESS; ask_headless.go), read from the kernel's input device (buttons_headless.go)
//	button-burst power <n> <gap ms> [ms]  the same, n presses gap apart, each given to the Shell as it comes
//	require-button <name>   skip the case unless PHONE_SELFTEST_BUTTONS names that button (whoever runs it presses it)
//	touch tap|long <x> <y> | touch tap|long row <text> | touch tap|long key <id> | touch swipe <x0> <y0> <x1> <y1> [ms]
//	                        a finger on the screen (logical pixels, a negative one from the right or bottom edge, n% of the
//	                        width or height; row: the middle of what shows of the body row titled text, the field with that
//	                        placeholder or the button with that label;
//	                        key: the on-screen keypad's key, 0-9 * # backspace left right up down enter), once the boot
//	                        screen is gone, played to the pixel renderer as from its touchscreen (touch_headless.go)
//	touchscreen <gesture>   the same made on the board's own touchscreen when asked on the output (PHONE-SELFTEST-TOUCH;
//	                        ask_headless.go), read from the kernel's touchscreen device
//	require-touch           skip the case without a pixel renderer that takes touches (feature touch)
//	require-tall <n>        skip the case on a frame less than n logical pixels high
//	require-touchscreen     skip the case unless the SKU has a touchscreen and PHONE_SELFTEST_TOUCH is set (whoever runs it
//	                        touches it: the emulator's harness)
//	usb-plug usb-kbd|usb-mouse on|off [ms]   a USB keyboard or mouse plugged into the port or pulled when asked on the
//	                        output (PHONE-SELFTEST-PLUG; ask_headless.go); done once the Shell allowed it or let it go
//	require-usb-plug        skip the case unless the SKU's port is a USB host and PHONE_SELFTEST_USB_PLUG is set (whoever
//	                        runs it plugs: the emulator's harness)
//	require-dev-machine     skip the case on a phone or the emulator (an SKU id): there the power menu's Power off powers off
//	fn-layer <k>=<c>...     the full keyboard's Fn layer, as a SKU gives it (w=1 e=2 ...); fn-layer alone: none
//	tel ring <n> | tel sms <from> <text> | tel lock <pin> | tel ussd <text>   simulated baseband (telephonyd)
//	tel hangup <n>          the other party of the call with that number hangs up (a caller giving up: missed)
//	tel slow <dial-ms> [<answer-ms>]   the modem answers dial that late, the called party answers that long
//	                        after it rings (tel slow 0: as usual again)
//	tel network <sim-mcc> <network-mcc> [<nitz-minutes>]   the SIM's country, the network's, the zone it sends
//	tel roaming <mcc><mnc> [<name>] | tel roaming off   registered roaming on that network (another country's:
//	                        abroad; the SIM's own: national roaming), or at home again
//	tel ims <state>         the IMS (VoLTE) registration the modem says on 4G: registered, registering,
//	                        not-registered, limited-registered, unknown (as at start)
//	tel cb <channel> <serial> <text>   a cell broadcast (an emergency alert; 4370 the highest level)
//	tel pending <n> [ms]    wait until telephonyd holds n events phone-smsd has not acknowledged
//	tel held <from> <text>  a message telephonyd stores but does not announce, as if it died right after
//	tel flash <from> <text> a class 0 (flash) message
//	mms offer <from> <text> | mms sent <n> [ms] | mms album <text> [ms]   multimedia messages through the
//	                        selftest's MMSC (mms_headless.go); flashes <n> [ms]: n class 0 messages shown
//	sms due | files-drop <text>   the scheduled messages sent now; the files named with text out of Files (mms_headless.go)
//	tel kill                kill phone-telephonyd; wait until its supervisor's new process answers
//	require-sms             skip the case without phone-smsd
//	sms-kill [wait]         kill phone-smsd (its supervisor starts it again); wait: until the new one answers
//	alerts <n> [ms]         wait until n emergency alerts have been shown
//	locale <lang> <zone>    wait until the shell's language and time zone are these
//	gui-env <name> <value>  wait until the pixel renderer runs with name=value in its environment (none: nothing to check)
//	timeshift <seconds>     move the shell's clock forward, the apps' with it (alarms, jobs, timeouts)
//	require-tel             skip the case without telephonyd
//	audio <category> | audio paused <category> | audio idle   wait for phone-audiod (a stream it reported in that
//	                        state since the last input or audio step counts, even one that ended; idle: now)
//	require-audio           skip the case without phone-audiod
//	require-sim-outputs     skip the case unless phone-audiod takes stand-in devices (--sim-outputs)
//	audio-channels mono|stereo <balance> [ms]   wait until phone-audiod plays mono or stereo with that balance
//	audio-output plug <id> wired|usb|bluetooth [mic] [hw] [<name>] | audio-output unplug <id>   a stand-in
//	                        device plugged in or pulled out (phone-audiod's output.sim; hw: its own volume)
//	audio-output pick <id>|auto   choose media's output as the picker does
//	audio-output media <id> [ms] | audio-output call <id> <mic> [ms]   wait until media, or a VoIP call and
//	                        its microphone, go to that output (phone-audiod and the Shell agree)
//	spoken <text> [ms]      wait until the screen reader says a line holding text (a line counts once; with
//	                        phone-audiod its speech service must have said it too)
//	unspoken <ms>           the screen reader says nothing new for that long
//	frame-a11y <field> <value>   the frame carries that Settings › Accessibility field (contrast bold invert
//	                        daltonize magnify) with that JSON value; - for none
//	a11y-shortcut <name>|- [ms]   wait until the Shell's accessibility shortcut is that (screen-reader,
//	                        color-inversion, off; - the magnifier): Settings shows a choice before the
//	                        Shell has it
//	push [voip] [normal] [sealed] [other] <app-id> <title> | <body> | <data>   send through the gateway as the app
//	                        server; normal: priority normal; sealed: data end-to-end encrypted to the app's key;
//	                        other: to a gateway instance the device is not connected to
//	require-push            skip the case without phone-pushd
//	require-nfc             skip the case without phone-nfcd (and its simulated controller)
//	require-display         skip the case without phone-displayd
//	display <hz> [ms] | display went <hz> [ms]   wait until the panel runs at hz (as displayd says), or went to
//	                        it since the last display step; display stayed <hz> [ms]: after ms (default 1000) the
//	                        panel has run at hz alone since then; display reason <why> [ms]: until displayd's reason is
//	                        why (idle, input, setting, cap:battery, ...); display thermal hot|cool: the simulated
//	                        thermal zone; display report: print PHONE-DISPLAY (display_headless.go)
//	nfc card <uid> [encrypted|cpu] | nfc card none   a physical card held to the phone, for reader mode
//	nfc reader <name> door <uid>[,<uid>...] | gate <aid> <fare> | pay <aid> <amount>   a simulated reader
//	                        (a door accepting those UIDs, a transit gate, a payment terminal; amounts in cents)
//	nfc tap <reader> ok|fail [<card name>]   hold the phone to the reader; it must accept or refuse (the card it saw)
//	nfc clock <HH:MM> [workday|weekend]   move the shell's clock forward to that time of day (on such a day)
//	nfc asleep | nfc woken  wait until the simulated sleep is on; the last sleep ended by a reader's field
//	nfc picker on|off       the end key's double press opens the quick card view (scripts start with it off)
//	require-gnss <kind>...  skip the case unless the location receiver is one of those kinds (sim, fifo, nmea,
//	                        modemmanager; "sim fifo": one location at moves)
//	require-netloc <kind>   skip the case unless the network location service is that kind (fake, http)
//	location at <lat> <lon> [<m/s> <course>] | location sky on|off   move the simulated receiver, take its
//	                        sky away; on a named pipe (the emulator) a simulated receiver writes NMEA into it
//	location gnss on|off [ms] | location indicator on|off [ms]   wait until the receiver, location in use, is so
//	location background <app-id> on|off [ms]   wait until the app uses the location in the background (the status
//	                        bar names it), or does not
//	location source <kind>|none | location use fifo|sim | location recent <app-id> [background|foreground] [ms]   the
//	                        receiver is that kind; (developer machine) read NMEA from a pipe in the run dir, or simulate
//	                        again; Settings › Location lists the app (its last day's use in the background, or none
//	                        there) (location_headless.go)
//	location fix gnss|network [ms] | location network on|off   wait until the last fix came from the receiver or
//	                        the network location service; the user's switch for the network location
//	location report         print PHONE-LOCATION with the receiver, its fixes this run, the last one and the network location
//	ota sim [slow [ms]]     a simulated A/B phone for system updates: its slots, RAUC (slow: a bundle takes
//	                        seconds, ms per 256 KiB of it, 400 by default), restart and network, and an update
//	                        server offering 9.0.0 (update_headless.go)
//	ota cut <n> | ota net wifi|mobile   the next n downloads stop halfway; the network the phone is on
//	ota clock <HH:MM>       move the shell's clock forward to that time of day
//	ota auto <text> [ms]    wait until the last automatic update step says text
//	ota installed [ms] | ota restarts <n> [ms] | ota off   wait for the install, the restarts; end the simulation
//	require-pushgw-cluster  skip the case without two gateway instances phone-shell can kill
//	pushgw-kill             kill the gateway instance the device is connected to; wait until it moves
//	require-netfw           skip the case without the per-app firewall (root)
//	require-webview         skip the case without the web view (phone-webview and a pixel renderer)
//	net-send <app-id> <sent|blocked>   send UDP to TEST-NET-1 as the app's uid; the firewall must agree (without
//	                        the firewall: as the Shell's policy says, by the app's mobile data block and the radios' Wi-Fi)
//	require-mobile-route    skip the case where net-send's packets leave by Wi-Fi or have no route: with the firewall, a
//	                        mobile data block holds only on a link that counts as mobile (netfw.WifiPrefix)
//	data-charge <app-id>|hotspot|system wifi|mobile <down> <up>   count bytes for the line today as if it had
//	                        used them (mobile while roaming abroad counts as roaming), then look at the data plan
//	data-usage save|restore|report   keep data usage's days and the hotspot's aside, put them back (the plan's
//	                        state with them), or print PHONE-DATA-USAGE with this cycle's (datausage_headless.go)
//	net-blocked <app-id> <why>|none [ms]   wait until the app's mobile data block in force is that (app_limit,
//	                        background_data, wifi_only; netpolicy.go), or none
//	job <app-id> <id> runs|stops <n> [ms] | job <app-id> <id> with <id> | job <app-id> <id> waits <text> [ms]   wait until
//	                        the background job ran (was stopped for the network) n times, last ran in the same wake as
//	                        the other job, or is due but waits for its network, why holding text
//	job-add <app-id> <id> <every-min> [charging] [unmetered] [network] | job-cancel <app-id> <id>   the app schedules a job,
//	                        or cancels it
//	appkill <app-id>        end the app process at once (the system ends it: no crash, no tombstone)
//	lowmem-kill <app-id>    end a background app as the low-memory killer does (asked first; it keeps a tombstone)
//	ended-itself <app-id> [ms]   wait until the app the system last asked to end has exited, and fail if it was
//	                        killed once its grace was up
//	appterm <app-id> [ms]   send the app SIGTERM as the Shell does when it stops; it must exit by itself within ms
//	                        (3000, the Shell's grace then), not as a crash; print PHONE-APP-TERM with how long it took
//	saved <app-id> [ms]     wait until the app has handed over its state (lifecycle background)
//	revived <app-id> [ms]   wait until a tombstoned app's new process has sent its rebuilt pages
//	revoked <app-id> [ms]   wait until the device has the store's takedown of the app
//	covers <capability>...  the case calls these capabilities (read before its requirements, so put it first): when it
//	                        is skipped and nothing else called one, the compatibility report counts it skipped, with why
//	require-env NAME        skip the case unless the environment variable NAME is set (a case not passing yet)
//	require-previous        skip the case when the case run before it in its part did not pass (it goes on from that
//	                        case's screen, call or counts); cases skipped for the environment or --only in between do not
//	                        count, but when none before it in its part ran and one was skipped for the environment, it is
//	                        skipped too (it would go on from a case that did not run)
//	require-fingerprint     skip the case without the simulated fingerprint sensor (a board with none, or its own)
//	finger <name>           touch the simulated fingerprint sensor with that finger (on the OK key: rest it on the cap, no press)
//	fingerprint-at ok-key|own   move the simulated sensor into the OK key (a flip phone's keypad) or back to a sensor
//	                        of its own (the case moves it back)
//	fingerprint-power on|off [ms]   wait until keystored has the fingerprint sensor powered, or not
//	locked on|off [ms]      wait until the phone is locked (any lock) or not
//	unlock <pin>            if the phone is locked with a PIN (it started locked: a restart, or the start over after a
//	                        failed case), enter the PIN on the lock screen as a person would; else nothing
//	require-kernel <name>... skip the case where the start's kernel probe found one of these protections missing
//	                        (kernel.go: landlock, cgroup-freezer, ...; the compatibility report's sandbox.missing)
//	require-own-kernel      skip the case where the running kernel is not one this system ships (no /lib/modules/<release>:
//	                        another system's, which this root is mounted in), saying what that kernel lacks
//	require-systemd         skip the case unless this system's service manager answers systemctl (not another system's)
//	require-radios          skip the case without Wi-Fi and Bluetooth (iwd and BlueZ, or the simulated ones)
//	require-wifi-ap         skip the case where wifi-ap cannot put an access point up: the simulated Wi-Fi, or iwd with a
//	                        second Wi-Fi device (the emulator's simulated radios)
//	require-eap-ap          skip the case where no enterprise (802.1X) access point can be put up (the simulated Wi-Fi, or hostapd on a radio iwd leaves alone: the emulator's third one)
//	wifi-eap-ap <ssid> <user> <password> <domain>   an 802.1X access point whose server takes that user and has a certificate for domain
//	wifi-forget <ssid>      the phone forgets the network (wifi.forget), as a case leaves the phone
//	wifi-ap <ssid> <passphrase>   an access point comes in range (on the emulator: the second simulated radio, iwd's access point,
//	                        which hands out addresses by DHCP); one already up under that name stays
//	wifi-join <ssid> <passphrase> [ms]   the phone joins that network (wifi.connect, unless it is on it already) and has an address
//	require-hotspot sim|iwd skip the case unless the personal hotspot is that kind (iwd: with wpa_supplicant, udhcpc and a radio iwd leaves alone for the device)
//	hotspot-client <name> <passphrase> [wpa2] [2.4ghz] [refused]   a device joins the personal hotspot (on the emulator: wpa_supplicant
//	                        on the free radio in a network namespace of its own, then an address from iwd's DHCP server, asking with
//	                        its name as host name; hotspot_client.go); @ is the hotspot's own passphrase; on the simulated Wi-Fi a
//	                        device that knows no WPA3 or has no 5 GHz, and refused: the hotspot must keep it off
//	hotspot-traffic <down> <up>   the simulated hotspot's devices download and upload that many bytes
//	hotspot-seen yes|no [ms]   wait until a device nearby scanning sees the simulated hotspot, or not (hidden)
//	hotspot-usage <bytes> [ms]   wait until the hotspot counted that many bytes this session and today (PHONE-HOTSPOT-USAGE)
//	hotspot-pass save|restore   keep the hotspot's passphrase aside (made now if there is none yet), put it back: a case
//	                        that sets one in Settings leaves the one it found (the switch's case shows it, laid out by its length)
//	hotspot-report [<name>] print PHONE-HOTSPOT with what the access point can do and its devices; a device of that name must be on it
//	wifi-client <name> <ssid> <passphrase>   the same device joins that Wi-Fi network instead, the one the phone is on (an address
//	                        by DHCP, no router needed); on the simulated Wi-Fi nothing: switch-send checks the network itself
//	hotspot-client-off [<name>] | wifi-client-off   the device leaves (the emulator: its namespace ends, the radio comes back; the
//	                        simulated Wi-Fi: that device, or all)
//	hotspot-ping phone|upstream [ms]   the emulator's device pings the phone or, through the NAT, the phone's default gateway
//	hotspot-share <iface>|default|none [ms]   wait until the firewall shares that interface (default: the default route's) with
//	                        the hotspot, forwarding on for both, or shares nothing with forwarding back as it was (require-netfw)
//	switch-send <kind>...   an old phone pairs by the code Settings › Move from an old phone shows (joining the hotspot first; on
//	                        the Wi-Fi path the emulator's old phone is the wifi-client, on the network the code names, and the
//	                        simulated phone must be on it) and sends switch_sample.json's data of those kinds (contacts call_log
//	                        sms calendar photos) with its own client of kario-switch/1, then done (switch_headless.go)
//	files <text> present|absent [ms]   Files has a file whose name holds text, or none
//	clock-at <HH:MM>[:SS]   the shell's clock, the apps' with it, at that time of day of its date (earlier or later);
//	                        a case whose steps read the time of day (today, a schedule's edge) pins it so, then clock-reset
//	clock-reset             the shell's clock, the apps' with it, back to the machine's (undoes timeshift, clock-at)
//	calendar-import <text> | calendar-undo <text>   hand the newest file of Files whose name holds text
//	                        to the calendar app's import entry point, or take that import out (calendar_headless.go)
//	scheduled <app-id> <n> [ms]   wait until the app has n scheduled notifications waiting
//	notified <app-id> <text> [ms]   wait until the notification centre has one of the app's holding text (its banner
//	                        may be gone already)
//	bt-peer <name>          a device to pair with appears nearby (on the emulator: the second virtual controller)
//	radio wifi|bt|bt-search|cell on|off [ms]   wait until Wi-Fi, Bluetooth, its search for devices or the modem's radio is on or off
//	data-cut <iface>|none [ms]   wait until the firewall closes just that interface for mobile data (require-netfw)
//	bt-push <name> <text>   the paired device sends the phone a file (the user answers on screen); \n in text is a line break
//	file-put <name> <text>  a file comes into Files, as a download does; \n in text is a line break
//	photo-put <name> <w>x<h>  a photo that size (a JPEG) comes into the album, the oldest there, so a picker lists it last
//	photo-drop <name>       the album's photos of that name go
//	bt-pull                 the paired device (a car kit) reads the phone book and the missed calls
//	bt-peer-result ok|refused [text] [ms]   wait until the device's push or pull ended so (a pull read text)
//	bt-peer-got <name> <text> [ms]   wait until the paired device has received the file with text in it
//	bt-peer-code <code>|none   the paired device asks for that code (OBEX authentication) before it takes a file the
//	                        phone sends it, or for none again (as a case leaves it)
//	bt-inquiry yes|no [ms]  btmon on the phone's controller sees HCI Inquiry commands in that time (25000 ms, not scaled),
//	                        or none (prints PHONE-BT-INQUIRY; the simulated radio: whether it searches now)
//	require-bt sim|bluez    skip the case unless Bluetooth is that kind (bluez: BlueZ with the selftest's second adapter, the emulator)
//	require-bt-peer         skip the case without a device to pair with: the simulated radio's, or a second virtual controller
//	                        (btvirt), which reaches only virtual ones: never where the phone's adapter is a controller of its own
//	bt-oldphone android|iphone <name>   an old phone to move from comes in range, showing a pairing code; it serves the phone book
//	                        and messages of package obex/btphone's phone of that shape (on the emulator: the second virtual
//	                        controller, unpaired from the phone first, so that the pairing reads its services afresh)
//	bt-oldphone-allow yes|no   the old phone's user answers its next question (may Kario read the phone book, the messages)
//	vpn-file <name> [full]  a WireGuard configuration (split tunnel, or full, keys made now) comes into Files, as copied over USB
//	vpn-drop [ms]           the always-on tunnel goes away under the Shell (the simulated one; on the device its link is deleted);
//	                        wait until the Shell has it connected again
//	vpn-net <iface> [ms]    the simulated tunnel's network becomes iface; wait until the Shell connected it again
//	vpn-route <app-id> tunnel|direct|blocked [ms]   wait until the app's packets to TEST-NET-1 go so: on the device sent as its uid
//	                        (the tunnel's counter, the firewall's EPERM), on a developer machine as the policies say (vpn_policy.go)
//	vpn-lock on|off [ms]    wait until the kill switch holds, or not (on the device: its chain in the firewall)
//	vpn on|off [ms]         wait until the status bar's VPN indicator is on or off (off on the device: the tunnel's link and rules are gone)
//	require-vpn <kind>...   skip the case unless the VPN is one of those kinds (sim; networkd: the device's, root)
//	require-vpn-peer        skip the case without the device's VPN (systemd-networkd, root)
//	require-direct-route    skip the case where the device's packets outside a tunnel have no route (no Wi-Fi or mobile
//	                        data): vpn-route's direct and blocked cannot be told from the kernel's network unreachable
//	require-cell-net        skip the case unless the Shell configures the cellular link (systemd-networkd, root)
//	cell-veth on|off        a veth pair named as the simulated modem's link (simwwan0), its other end in a network namespace
//	cell-link <addr/prefix>|none [ms]   wait until systemd-networkd has configured simwwan0 as telephonyd reports it (the
//	                        address, MTU, default route, DNS servers in systemd-resolved), or taken it away (cellnet_headless.go)
//	vpn-peer <name> | vpn-peer off   a WireGuard server in a network namespace behind a veth pair (vpn_headless.go),
//	                        for the phone's waiting key pair; its full-tunnel profile, without a PrivateKey, comes into Files
//	vpn-check [<app-id>]    the tunnel to that server works: handshake, routes, a TCP echo and DNS through it, the app's packets in it
//	carddav-server <user> <password> | carddav-server off   a CardDAV server in the Shell (on 127.0.0.1) as the product's
//	                        Kario account server, with one card (photo, e-mail) for the user (accounts_headless.go);
//	                        the user's account signs in with a code too (/v1/auth/, package account/accounttest)
//	account-code [wrong]    type the last sign-in code the server sent to the user's address (wrong: another)
//	account-device active|revoked|none [ms]   wait until the last password the server made for a phone is that (none: no
//	                        phone signed in with a code; active prints PHONE-ACCOUNT with its name)
//	account-revoke          the account's page signs the phone out: the last password the server made for a phone is revoked
//	carddav-book <name> <displayname>   another address book in the server's home
//	carddav-card [@<book>] <name> <vCard>   another device writes that card on it (\n between the lines)
//	carddav-has [@<book>] <text> present|absent [ms]   wait until one of its cards holds text (\n a line break), or none does
//	carddav-net offline|wifi|mobile|real   the network the contacts sync sees (real: the phone's); the Shell hears of it
//	                        as of a network event
//	carddav-contact <name> | <text>|absent [ms]   wait until the phone's contact of that name shows text (as the system's
//	                        dialogs list it, then its book), or is gone
//	carddav-group <name> | <members>|absent [ms]   wait until the phone's group has those members (names, sorted, a comma
//	                        between), or is gone
//	caldav-event <name> <iCalendar>   another device writes that calendar object on the server's calendar (\n between the lines;
//	                        carddav-server serves CalDAV on the same address)
//	caldav-delete <name>    another device deletes that calendar object
//	caldav-has <text> present|absent [ms]   wait until one of its calendar objects holds text, or none does
//	caldav-schedule on|off  the CalDAV server schedules (RFC 6638: calendar-auto-schedule, a scheduling inbox) or not
//	caldav-invite <name> <iCalendar>   someone's invitation to the user, as a scheduling server delivers it: the event into
//	                        the calendar, a REQUEST into the inbox
//	caldav-reply <text> <address> <PARTSTAT>   an attendee's answer to the user's invitation (the object holding text), as a
//	                        scheduling server takes it: into the user's event, a REPLY into the inbox
//	caldav-sent <text> [ms] wait until the scheduling server has sent a message whose line holds text ("REQUEST <uid>
//	                        <address>", "REPLY <uid> <PARTSTAT>")
//	caldav-inbox <n> [ms]   wait until the scheduling inbox holds n messages
//	parent-server on|off    a Kario account server's minor mode management in the Shell (on 127.0.0.1) as the
//	                        product's, its policy key as the product's, pushing through the gateway (family_headless.go)
//	parent-bind <email>     a parent signed in as email enters the code the phone shows
//	parent-policy <json>    the parent saves rules (family.Rules as JSON); the server signs them and pushes
//	parent-report <text> [ms] | parent-locate <text> [ms]   the parent asks for today's screen time, or where the
//	                        phone is; wait until the server's copy of the answer holds text (prints PHONE-FAMILY)
//	parent-unbind           the parent ends the binding on the page
//	parent-bound yes|no [ms]   wait until the server holds a bound phone, or none
//	parent-asked <text> [ms]   wait until the server holds a request for more time waiting, holding text (prints PHONE-FAMILY)
//	parent-answer <minutes>|no   the parent gives the oldest waiting request that many minutes (signed), or refuses it
//	parent-reset confirm | done [ms]   the parent, signed in afresh, confirms the phone's PIN reset; or wait until the
//	                        phone told the server the new PIN is set
//	key-if <key> <text> [ms]   press the key (or type the one character) if the text shows within the time, else go on
//	screen-auto on|off      the screen timeout applies (scripts start with it off: their keys would wake the screen)
//	screen on|off [ms]      wait until the screen (and the renderer's panel) is on or off
//	screen-key <key>        press a key as the keypad does: with the screen off it only wakes it
//	light <pattern> [ms]    wait until the notification light shows the pattern (off, blink 200/4800 ms)
//	backlight <percent> [ms]   wait until the panel's backlight (or the simulated one) is at that level
//	keylight <percent>|off [ms]   wait until the keyboard's backlight is at that level, or off
//	lux <lux>               the simulated light sensor reads that much light (fails with a real one)
//	require-lux             skip the case unless the light sensor is the simulated one (a board without one the Shell reads)
//	require-light           skip the case without a notification light (a board without one)
//	no-light on|off         the board has no notification light (on) or its own again (off)
//	sound-switch ring|vibrate|silent|restore   the phone's sound switch moved there, as the renderer reports it
//	                        (soundmode.go); restore: back where the renderer last said it is (nowhere without one)
//	require-sound-switch    skip the case unless the SKU has a sound switch (sku.json's sound_switch; on the developer
//	                        machine phone-shell --sku with one, shell/testdata/sku-sound-switch.json)
//	lid shut|open           a flip's lid shut or opened, as the renderer reports it (lid.go); fails on a SKU without one
//	require-lid             skip the case unless the SKU has a lid (sku.json's lid; on the developer machine
//	                        phone-shell --sku with one, shell/testdata/sku-lid.json)
//	buttons-only on|off     the phone's keys are its power and volume keys, no keyboard (on), or as the renderer says (off)
//	soft-keypad text|digits|phone|date|none [ms]   wait until the on-screen keypad (softkeypad.go) shows that layout, or
//	                        none; it shows on a renderer that takes touches, with buttons-only on
//	power-key on|off|sku    the board has a power key of its own (on), or none and the end key is the power key (off), or as
//	                        the SKU says (sku)
//	alarm-in <seconds> <label>  the clock app's alarm that far ahead on the shell's clock
//	system-sleep on|off     the phone may suspend (scripts start with it off; the developer machine gets a simulated sleep that moves the clock to the wake time)
//	slept <times> [ms]      wait until the phone has slept this often
//	require-sleep           skip the case where a wake lock someone else holds keeps the kernel from suspending
//	                        (/sys/power/wake_lock: the bring-up image's phone-bringup)
//	tap <text>              a pointer's click on the body node titled text, as the renderer reports it (require-gui)
//	cpu-charge <app-id> <ms>   count CPU time for the app as if it had used it
//	screen-use <app-id> <minutes>   count time on the screen today for the app as if it had been in front (screentime.go)
//	screen-use clear        forget the time on the screen counted so far, every app's and day's
//	bgcpu <app-id> <ms> [timeout]   wait until the app's background CPU of the last day reaches ms
//	light-blinks <n> [ms]   wait until the userspace LED has come on n times a blink period after it last did (the emulator)
//	require-uleds           skip the case unless the light is a userspace LED (the emulator)
//	require-usb             skip the case without a USB device controller (or the simulated one)
//	usb-cable on|off        plug the cable to a computer in or out (the emulator: dummy_hcd; else simulated)
//	usb-get <path> <text>   the computer fetches path with the password the phone shows; it must contain text
//	usb-put <path> <text>   the computer copies text into path (a file in a writable folder)
//	usb-refused             the computer no longer reaches the phone
//	usb-port <port> open|closed   the computer can (or cannot) connect to that port of the phone
//	require-usb-host        skip the case without the USB host side of the phone's own cable (dummy_hcd: the emulator)
//	usb-mtp <step> ...      the computer over file transfer (MTP), usbmtp.go: storages, ls, props, get, partial, put, refused, event, windows, libmtp, kernel, gone
//	dev [!]<op> [<arg>...] [-> text]   the computer runs phone-dev <op> (devbench.go; install <example>, trust devstore, logs, reload,
//	                        restart, uninstall <app-id>, push <path> <text>, pull <path>, screenshot <name>, key <name>..., type <text>,
//	                        syslog [-u <unit>] [-n <n>], reboot [bootloader|recovery], update signed|unsigned, bugreport); ! wants a
//	                        refusal; the output contains text
//	dev-ssh [!]<op> [<arg>...] [-> text]   the same over SSH on the cable (dropbear's dbclient; the emulator)
//	dev-debug [-ssh] [-attach] <app-id> -> <text>   the computer opens a debugger on the app (phone-dev debug, devdebug_headless.go;
//	                        -ssh over SSH on the cable, the emulator): the Shell's answer contains text, then the app's hello;
//	                        attached, the same process, otherwise a new one
//	dev-debug-send <json> [-> text]   a request (a JSON line with an id) to the app's debugger; its answer contains text (or ok)
//	dev-debug-wait <text> [ms]   wait for an event from the app's debugger containing text
//	dev-debug-end           the computer goes: the session ends, the app is no longer debugged and runs on, the same process
//	dev-rm <path>           remove a file the computer pushed into the developer directory
//	dev-forward <phone-port> <computer-port>   the emulator's computer forwards a port to an echo service on the phone's
//	                        loopback over SSH (dbclient -L) and reads its bytes back; developer mode going off while bytes
//	                        still flow must refuse a new SSH connection and end the session once the computer lets go
//	pkg-install <example>   sign the sample with the development store and install it without the dialog (developer mode)
//	pkg-uninstall <app-id>  uninstall it without the dialog
//	jscache <app-id> ready|none|used|unused|tamper|source|cache [ms]   a store TypeScript app's bytecode (jscache.go)
//	app-bench <app-id> <label> [runs]   open the app afresh runs times; print PHONE-APP-BENCH with the start to its first page
//	store-rollout <app-id> <version> <percent>   the developer moves a staged version
//	appstop <app-id>        end it if it runs and wait until it is gone (the next open starts it afresh)
//	push-sealed <app-id>    the app got end-to-end encrypted push data
//	perm-state <key> <perm> <state> [ms]   wait until a grant reads state (ask granted denied);
//	                        key is an app id or "<app-id>/<mini-program>"
//	require-cgroup          skip the case without per-app cgroups (developer machine)
//	require-gui             skip the case without a pixel renderer
//	gui-crash               kill the pixel renderer; the watchdog must start a new one and resend the frame
//	cgroup <app-id> <file> <value> [ms]   wait until the app's cgroup file reads value
//	mem-event <app-id> <event> <n> [ms]   wait until the app's memory.events counts the event (max, oom_kill...) at least n times
//	kernel missing|fallback <name>...|none   the start's kernel probe found exactly these protections
//	                        missing, or done another way (kernel.go); prints PHONE-KERNEL
//	appenv <app-id> <name> <value>   the running app's environment has name=value
//	mem-low <n>             the next n memory checks read as low (the low-memory killer runs)
//	pages-ended <n>         at least n background apps' pages were ended for low memory
//	vibrated [ms]           wait until the vibration motor is asked to run (again)
//	require-v4l2            skip the case unless phone-mediad captures from a V4L2 camera
//	camera-frames <n> [ms]  wait until the V4L2 camera has captured at least n frames
//	scan-code <text>|none   hold a QR code of text up to the scanner's camera (the synthetic one, a test driver's:
//	                        the emulator's vivid, or any with mediad's --test-codes), or none; it stays until changed
//	scan-tries <n> [ms]     wait until the open scan has decoded n frames
//	scan-report             print PHONE-SCAN with the camera and the decoder's tries, reads and times
//	setting <key> <value> [<text>]   change a system setting (developer_mode 1; app_data_limit 0 com.phone.browser;
//	                        lock_pin 1 raises the system's screen for a new PIN, as Settings does)
//	pkg-tamper <app-id> <file>   append a byte to a file of an installed package
//	pkg-build <example> <app-id> <file> [tamper]   sign share/phone/examples/<example> with the
//	                        development store into <file> in the app's data dir (tamper: then flip a byte)
//	require-example <example>   skip the case without share/phone/examples/<example> (left out of the build)
//	app-log <app-id> <prefix>   wait for the app's log line starting so and print it (PHONE-APP-LOG)
//	shell-log <text>        wait for a line of the Shell's log (shell.log in the run directory) holding text and print it
//	                        (PHONE-SHELL-LOG)
//	fdstore <app-id> app|keep yes|no [ms]   wait until systemd's fd store for the Shell's service holds the app's
//	                        connection (app) or the system's copy of its own connection (keep) for its process, or none of
//	                        that kind of the app's; print PHONE-FDSTORE-HOLDS with what it holds (fdstore_headless.go)
//	mp-build <example> <host-app-id> <file> [tamper]   the same for a mini-program, signed by a development
//	                        mini-program store the host is made to trust
//	app-file-drop <app-id> <file>   delete what pkg-build or mp-build put into the app's data dir (the store
//	                        lists its downloads/ as ready to install, and nothing on the phone deletes them)
//	widget <app-id> off     the app takes its standby line away, as its widget.set without a text does
//	store-publish <example> <version>   publish a sample app through the store backend (as its developer,
//	                        with the staff verifying the developer and approving a review)
//	store-takedown <app-id> <reason>   the store staff take an app down
//	at <screen-id> [ms]     wait for a screen by id (sys.idle, sys.menu, <app-id>/<screen>)
//	wait <text> [ms]        wait until the screen shows text (default 5000ms)
//	gone <text> [ms]        wait until the text disappears
//	wait-re <regexp> [ms]   wait until the screen matches (text that moves on: a play position)
//	frames <n> [ms]         wait until at least n more media frames reached the screen
//	painted [ms]            wait until the web view shows a painted page (not a single colour)
//	web-procs <n>           assert how many web processes were started since boot
//	masked                  assert the screen contains secure (masked) content
//	sleep <ms>
//	snap <name>             write the plain screen to the snapshot dir (and compare it, snapcmp.go)
type Runner struct {
	// Only: run the cases whose name matches, skip the others (--only).
	Only    *regexp.Regexp
	C       *Core
	SnapDir string
	// Baselines, when set, holds snapshots still and compares them; a
	// difference fails the case.
	Baselines *Baselines
	snapDiffs []string
	Out       io.Writer
	// Scale multiplies every timeout (e.g. 4 under QEMU TCG emulation).
	Scale int
	// fetch: the server fetch-server started (fetch_headless.go).
	fetch *fetchSink
	// keep: the server keep-server started (keepalive_headless.go).
	keep keepLines
	// GUI, when set, receives frames during the run and writes a PNG at
	// every snap step. With Watch it is Watch.G, replaced when the renderer
	// dies.
	GUI    *GUI
	Watch  *RendererWatch
	pushed time.Time
	// vibrations seen by the last "vibrated" step
	vibrations int
	// spoken: the screen reader's lines up to this one are used up ("spoken").
	spoken int
	// Cases are the results of the last Run; partFrom is where in them the
	// cases of the part running begin.
	Cases    []CaseResult
	partFrom int
	// part is the part running; parts holds what the others left in the
	// Shell, bootState what a part starts from (partstate.go); caseClock
	// is the clock's offset as the running case found it.
	part      string
	parts     map[string]partState
	bootState partState
	caseClock time.Duration
	// Restart execs a successor Shell that resumes the script from the file
	// it is given (phone-shell sets it; without it shell-restart cases skip).
	Restart func(resumeFile string) error
	// KillService ends one of the services phone-shell runs (its supervisor
	// starts it again); without it pushgw-kill cases skip.
	KillService func(name string) error
	// Resume continues a script a previous Shell ran up to a shell-restart
	// or a restart of the phone.
	Resume *Resume
	// CanReboot: Config.Reboot restarts the phone in place and the script
	// goes on after the line it stood at (phone-shell, SaveResume); without
	// it require-reboot cases skip.
	CanReboot bool
	// KeepGoing, when set, is called after a failed case with a resume file
	// that goes on at the next case: it starts the phone over (the Shell,
	// its services and apps, as a restart of the phone does; phone-shell
	// --keep-going) and returns only when it cannot. Without it a run
	// stops at its first failure.
	KeepGoing func(resumeFile string) error
	// Keep, with KeepGoing, is brought up to date as each case starts:
	// the phone that starts over after a failed case starts from the state
	// as that case found it (phone-shell restores it once everything has
	// stopped), not from what the case left behind.
	Keep *StateCopy
	kept KeptStats
	// line and cur: the line running and its case (SaveResume).
	line int
	cur  string
	// theft: the anti-theft lock's account server (reset_headless.go).
	theft *theftSim
	// ResumeFile is where shell-restart and shell-crash leave the script's
	// state (default: the run directory).
	ResumeFile string
	// Base is the directory the run's run, state and persist directories
	// are in, which the Shell after a shell-crash goes on in (ResumeBase).
	Base string
	pids map[string]int
	// hotspotPass: the passphrase hotspot-pass save kept (hotspot_headless.go).
	hotspotPass string
	// ota is the simulated phone of system updates (ota sim).
	ota *otaSim
	// mmsc is the selftest's MMSC (mms_headless.go).
	mmsc *mmsSim
	// presses: the button presses asked for so far (buttons_headless.go);
	// touches: the touchscreen's gestures (touch_headless.go); plugs: the
	// USB devices plugged or pulled (usbplug_headless.go); askID names the
	// console files they are asked in (ask).
	presses, touches, plugs int
	askID                   string
	// toasts: the toasts shown since the script's last input (watchToasts).
	toasts []string
	// heard: the stream states phone-audiod reported since the script's
	// last input or audio step (watchAudio).
	heard []audio.Stream
	// dbg: the debugger dev-debug opened.
	dbg *devDebugger
}

// holdBoot ends a compared run's boot snapshot hold: the animation lasts
// bootDuration from Run's start, and a loaded machine can spend that
// before the script's `sleep` + `snap` are done, yet the snapshot is of the
// boot. Any step but a sleep ends the hold once it is done, and so does
// the first case.
func (r *Runner) holdBoot(cmd string) {
	if r.C != nil && cmd != "sleep" {
		r.C.bootHeld = false
	}
}

// watchToasts keeps the toasts shown since the script's last input for
// wait: a toast lasts 2.5 s and can come and go while the runner is held
// up (a loaded machine), yet a person watching the screen saw it.
func (r *Runner) watchToasts(cmd string) {
	if r.C == nil {
		return
	}
	if r.C.onToast == nil {
		r.C.onToast = func(s string) { r.toasts = append(r.toasts, s) }
	}
	if slices.Contains(inputSteps, cmd) {
		r.toasts = nil
	}
}

// inputSteps are what a person does to the phone.
var inputSteps = []string{"key", "key-if", "type", "fill", "long", "chr", "button", "button-burst", "touch", "touchscreen"}

// watchAudio keeps the stream states phone-audiod reported since the
// script's last input or audio step for audio: a voice message of a second
// can start and end while the runner is held up, yet a person heard it.
func (r *Runner) watchAudio(cmd string) {
	if r.C == nil {
		return
	}
	if r.C.onAudio == nil {
		r.C.onAudio = func(s audio.Stream) { r.heard = append(r.heard, s) }
	}
	if slices.Contains(inputSteps, cmd) {
		r.heard = nil
	}
}

// press and typed send a key as a person would: one who reads a dialog
// before answering it (overlayArmDelay).
func (r *Runner) press(k Key) {
	if d := r.C.overlayArmLeft(); d > 0 {
		r.pump(d)
	}
	r.drawNow()
	r.C.Press(k)
}

func (r *Runner) typed(k Key) {
	if d := r.C.overlayArmLeft(); d > 0 {
		r.pump(d)
	}
	r.drawNow()
	r.C.Key(k)
}

// keysFirst lets the keys pressed before a fill reach the app first:
// those held behind an action it has not answered yet (input.go) or for
// its first page (tomb.go). Typing would wait behind them, so the text
// goes into the field they lead to, not the one they were pressed on.
func (r *Runner) keysFirst() {
	a := r.C.fg
	end := time.Now().Add(r.C.holdMax() + time.Duration(5*max(1, r.Scale))*time.Second)
	for a != nil && (a.input.up || len(a.heldKeys) > 0) && time.Now().Before(end) {
		r.pump(10 * time.Millisecond)
	}
}

// drawNow gives the pixel renderer the screen as it is before a key or a
// tap acts on it, as a person has seen it: where a list scrolls follows
// the frames the renderer laid out.
func (r *Runner) drawNow() {
	r.pushed = time.Time{}
	r.pushGUI()
}

func (r *Runner) pump(d time.Duration) {
	deadline := time.After(d)
	for {
		select {
		case f := <-r.C.Events():
			f()
		case <-r.guiDone():
			r.restartGUI()
		case <-deadline:
			r.pushGUI()
			return
		}
	}
}

func (r *Runner) guiDone() <-chan struct{} {
	if r.Watch == nil {
		return nil
	}
	return r.Watch.G.Done()
}

func (r *Runner) restartGUI() {
	if err := r.Watch.Restart(); err != nil {
		fmt.Fprintln(r.Out, "PHONE-GUI-EXIT:", err)
		r.Watch, r.GUI = nil, nil
		return
	}
	r.GUI = r.Watch.G
	r.pushed = time.Time{}
	r.pushGUI()
}

func (r *Runner) pushGUI() {
	if r.GUI == nil || time.Since(r.pushed) < 100*time.Millisecond {
		return
	}
	r.pushed = time.Now()
	r.C.Render()
	_ = r.GUI.Push()
}

// computer is the computer at the other end of the USB cable doing f,
// while the shell's loop keeps running (sharing needs it).
func (r *Runner) computer(addr string, f func(do httpDo, _ dialPort, base string) error) error {
	done := make(chan error, 1)
	go func() { done <- r.C.usbComputer(addr, f) }()
	for {
		select {
		case err := <-done:
			return err
		default:
			r.pump(20 * time.Millisecond)
		}
	}
}

func (r *Runner) screen() string {
	return r.C.Render().Plain()
}

func (r *Runner) waitFor(cond func(string) bool, ms int) (string, bool) {
	end := time.Now().Add(time.Duration(ms*max(1, r.Scale)) * time.Millisecond)
	for {
		s := r.screen()
		if cond(s) {
			return s, true
		}
		if time.Now().After(end) {
			return s, false
		}
		r.pump(30 * time.Millisecond)
	}
}

var keyNames = map[string]string{
	"enter": KeyEnter, "esc": KeyEsc, "tab": KeyTab, "up": KeyUp, "down": KeyDown,
	"left": KeyLeft, "right": KeyRight, "backspace": KeyBackspace, "f2": KeyF2, "f3": KeyF3, "f4": KeyF4, "end": KeyF4, "call": KeyCall,
	"ime": KeyIME, "sym": KeySym, "volup": KeyVolUp, "voldown": KeyVolDown,
	"playpause": KeyPlayPause, "next": KeyNextTrack, "previous": KeyPrevTrack, "power": KeyPower,
	KeyFnOnce: KeyFnOnce, KeyFnLock: KeyFnLock, KeyFnOff: KeyFnOff,
	KeyShiftOnce: KeyShiftOnce, KeyShiftLock: KeyShiftLock, KeyShiftOff: KeyShiftOff,
	KeyFirst: KeyFirst, KeyLast: KeyLast, KeyPageUp: KeyPageUp, KeyPageDown: KeyPageDown, KeyDelete: KeyDelete,
	KeyCopy: KeyCopy, KeyCut: KeyCut, KeyPaste: KeyPaste, KeySelectAll: KeySelectAll, KeyUndo: KeyUndo, KeyLang: KeyLang,
	KeyA11y: KeyA11y,
}

// KeyByName is the key a renderer or a script names (enter, left, f4, call…).
func KeyByName(name string) (Key, bool) {
	n, ok := keyNames[name]
	return Named(n), ok
}

// notSelected is why --only skips a case.
const notSelected = "not selected"

// CaseResult is one case of a run, for the compatibility report.
type CaseResult = cts.CaseResult

// WriteJUnit writes a run's cases as a JUnit XML report (CI test views).
func WriteJUnit(path, suite string, cases []CaseResult) error {
	type failure struct {
		Message string `xml:"message,attr"`
		Text    string `xml:",chardata"`
	}
	type skipped struct {
		Message string `xml:"message,attr,omitempty"`
	}
	type testcase struct {
		Name      string   `xml:"name,attr"`
		Classname string   `xml:"classname,attr"`
		Time      float64  `xml:"time,attr"`
		Failure   *failure `xml:"failure,omitempty"`
		Skipped   *skipped `xml:"skipped,omitempty"`
	}
	type testsuite struct {
		XMLName  xml.Name   `xml:"testsuite"`
		Name     string     `xml:"name,attr"`
		Tests    int        `xml:"tests,attr"`
		Failures int        `xml:"failures,attr"`
		Skipped  int        `xml:"skipped,attr"`
		Time     float64    `xml:"time,attr"`
		Cases    []testcase `xml:"testcase"`
	}
	ts := testsuite{Name: suite, Tests: len(cases)}
	for _, c := range cases {
		tc := testcase{Name: c.Name, Classname: suite, Time: c.Seconds}
		switch c.Status {
		case "FAIL":
			tc.Failure = &failure{Message: c.Message, Text: c.Message}
			ts.Failures++
		case "SKIP":
			tc.Skipped = &skipped{Message: c.Message}
			ts.Skipped++
		}
		ts.Time += c.Seconds
		ts.Cases = append(ts.Cases, tc)
	}
	raw, err := xml.MarshalIndent(ts, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append([]byte(xml.Header), append(raw, '\n')...), 0o644)
}

// Run executes the script and reports whether every case passed; the cases
// are kept in r.Cases.
// Resume is where a script stood when the Shell restarted in place.
type Resume struct {
	Line     int                `json:"line"`
	Case     string             `json:"case"`
	Cases    []CaseResult       `json:"cases"`
	CapStats map[string]CapStat `json:"cap_stats"`
	Pids     map[string]int     `json:"pids,omitempty"` // app-pid save
	// Rebooted: the phone restarted (SaveResume); a rebooted step right
	// after Line has happened then.
	Rebooted bool `json:"rebooted,omitempty"`
	// The snapshot comparison so far; SnapDiffs fail the case at its end.
	SnapCounts map[string]int `json:"snap_counts,omitempty"`
	SnapDiffs  []string       `json:"snap_diffs,omitempty"`
	Kept       *KeptStats     `json:"kept,omitempty"`
	// KeepServer: keep-server's helper, which a shell-restart keeps (and
	// a shell-crash, outside's).
	KeepServer *keepCarried `json:"keep_server,omitempty"`
	// Base: Runner.Base, for the Shell systemd starts after a shell-crash.
	Base string `json:"base,omitempty"`
	// Clock: the Shell's clock offset to go on with; Parts: what the
	// other parts left (partstate.go).
	Clock time.Duration        `json:"clock,omitempty"`
	Parts map[string]partState `json:"parts,omitempty"`
}

// ResumeBase is the directory a script that crashed (shell-crash) ran
// in, as its resume file says: the Shell systemd starts goes on there, as
// a phone's goes on with its state.
func ResumeBase(path string) string {
	raw, err := os.ReadFile(path)
	var res Resume
	if err != nil || json.Unmarshal(raw, &res) != nil {
		return ""
	}
	return res.Base
}

// KeptStats is what keeping the state at the cases' starts has cost so far
// (Runner.Keep), reported at the end.
type KeptStats struct {
	Cases int           `json:"cases"`
	Took  time.Duration `json:"took"`
	Max   time.Duration `json:"max"`
	Bytes int64         `json:"bytes"`
}

// resume is where the script stands, for the Shell that goes on with it.
func (r *Runner) resume(res Resume) Resume {
	res = r.withSnaps(res)
	res.Clock, res.Parts = r.C.clockSkew, r.parts
	if r.kept.Cases > 0 {
		k := r.kept
		res.Kept = &k
	}
	return res
}

// SaveResume leaves where the script stands for the Shell that starts over
// in place when the phone restarts (a factory reset): it goes on after the
// line running now, in the same case.
func (r *Runner) SaveResume() (string, error) {
	path := r.ResumeFile
	if path == "" {
		path = filepath.Join(r.C.cfg.RunDir, "selftest-resume.json")
	}
	raw, _ := json.Marshal(r.resume(Resume{Line: r.line, Case: r.cur, Cases: r.Cases, CapStats: r.C.CapStats(), Pids: r.pids, Rebooted: true}))
	return path, os.WriteFile(path, raw, 0o600)
}

// LoadResume reads what a previous Shell left for its successor.
func LoadResume(path string) (*Resume, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var res Resume
	if err := json.Unmarshal(raw, &res); err != nil {
		return &res, err
	}
	if k := res.KeepServer; k != nil && k.Path == "" {
		// What this Shell starts must not hold the helper's control.
		syscall.CloseOnExec(k.FD)
	}
	return &res, nil
}

func (r *Runner) Run(script string) bool {
	type result struct {
		name string
		ok   bool
		dur  time.Duration
	}
	var results []result
	r.Cases = nil
	r.part, r.parts = "", nil
	r.C.holdScale = r.Scale
	// With the simulated radios (a developer's machine) the move from an
	// old phone listens on a free port: other selftests on the machine may
	// hold 47800, the old phone takes the port from the pairing code, and
	// only a phone's firewall needs the real one.
	if _, sim := r.C.wifi.(*simWifi); sim {
		r.C.sw.anyPort = true
	}
	cur, t0, failed, skipping := "(setup)", time.Now(), false, false
	why := ""
	// covers: the capabilities the case says it calls (a skipped case's
	// count as skipped in the compatibility report, not as never called).
	var covers []string
	from := 0
	differed := false
	// afterReboot: the restart came during the step at the resume line,
	// so a rebooted step next is done.
	afterReboot := r.Resume != nil && r.Resume.Rebooted
	// kept: Keep holds the running case's start (a restart inside a case
	// goes on after it).
	kept := r.Resume != nil && r.Resume.Case != "(setup)"
	if r.Resume != nil {
		from, cur, r.Cases, r.pids, r.parts = r.Resume.Line, r.Resume.Case, r.Resume.Cases, r.Resume.Pids, r.Resume.Parts
		if r.Resume.Kept != nil {
			r.kept = *r.Resume.Kept
		}
		if k := r.Resume.KeepServer; k != nil {
			if kept, err := k.take(r.Scale); err != nil {
				fmt.Fprintf(r.Out, "keep-server: %v\n", err)
			} else {
				r.keep = kept
			}
		}
		r.C.AddCapStats(r.Resume.CapStats)
		if b := r.Baselines; b != nil {
			for k, n := range r.Resume.SnapCounts {
				b.counts[k] += n
			}
			r.snapDiffs = r.Resume.SnapDiffs
		}
		for _, c := range r.Cases {
			if c.Status != "SKIP" {
				results = append(results, result{c.Name, c.Status == "PASS", 0})
			}
			// The run went on past it: its snapshots differed.
			differed = differed || c.Status == "FAIL"
		}
	}
	finish := func(ok bool) {
		r.devDebugDrop()
		if len(r.snapDiffs) > 0 {
			if ok {
				why = snapsDiffer + ": " + strings.Join(r.snapDiffs, ", ")
			}
			ok, differed, r.snapDiffs = false, true, nil
		}
		status := "PASS"
		switch {
		case skipping:
			status = "SKIP"
		case !ok:
			status = "FAIL"
		}
		if !skipping {
			results = append(results, result{cur, ok, time.Since(t0)})
		}
		// A requirement met leaves its text in why.
		if status == "PASS" {
			why = ""
		}
		res := CaseResult{Name: cur, Status: status, Seconds: math.Round(time.Since(t0).Seconds()*10) / 10, Message: why}
		if skipping {
			res.Covers = covers
		}
		r.Cases = append(r.Cases, res)
		fmt.Fprintf(r.Out, "%s  %s  (%.1fs)\n", status, cur, time.Since(t0).Seconds())
		if skipping && why != "" && why != notSelected {
			fmt.Fprintf(r.Out, "  %s\n", why)
		}
	}
	if r.Resume == nil {
		r.C.RestartBoot()
		r.C.bootHeld = r.Baselines != nil
		if r.Baselines != nil {
			r.Baselines.begin(r)
		}
	}
	// The screen stays on unless a case turns its timeout on (screen-auto):
	// a script's keys would otherwise be spent waking it.
	r.C.SetScreenAuto(false)
	r.C.SetSleep(false)
	r.C.SetEndKeyPicker(false)
	r.bootState = r.partState()
	r.bootState.Skew = 0
	if r.Resume != nil {
		r.C.setClockSkew(r.Resume.Clock)
	}
	r.caseClock = r.C.clockSkew
	r.pump(200 * time.Millisecond)
	sc := bufio.NewScanner(strings.NewReader(script))
	lineNo := 0
	// begun counts the cases so far, those before a resume's line too:
	// r.Cases has one result for each, in order.
	begun := 0
	r.partFrom = 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		switch {
		case strings.HasPrefix(line, partMark):
			r.partFrom = begun
			if name := strings.TrimSpace(strings.TrimPrefix(line, partMark)); lineNo > from {
				r.enterPart(name)
			} else {
				r.part = name
			}
		case strings.HasPrefix(line, "== "):
			begun++
		}
		if lineNo <= from {
			continue
		}
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "== ") {
			r.holdBoot(line)
			if cur != "(setup)" {
				finish(true)
			}
			cur, t0, skipping, why, kept, covers = strings.TrimPrefix(line, "== "), time.Now(), false, "", false, nil
			r.caseClock = r.C.clockSkew
			// --only: the cases asked for; the others are skipped (a case
			// may need what an earlier one left behind).
			if r.Only != nil && !r.Only.MatchString(cur) {
				skipping, why = true, notSelected
			}
			continue
		}
		if caps, ok := strings.CutPrefix(line, "covers "); ok {
			covers = append(covers, strings.Fields(caps)...)
			continue
		}
		if skipping {
			continue
		}
		cmd, arg, _ := strings.Cut(line, " ")
		if strings.HasPrefix(cmd, "require-") {
			why = "needs " + strings.TrimPrefix(cmd, "require-") + " " + arg
		}
		if cmd == "require-app" {
			skipping = skipping || r.C.AppByID(arg) == nil
			continue
		}
		// A sample's package source (pkg-build), which the build may leave
		// out (examples/imgproc without a C compiler).
		if cmd == "require-example" {
			_, err := os.Stat(r.C.exampleDir(arg))
			skipping = skipping || err != nil
			continue
		}
		if cmd == "require-gnss" {
			skipping = skipping || !slices.Contains(strings.Fields(arg), r.C.gnssKind())
			continue
		}
		if cmd == "require-previous" {
			if prev, ok := r.lastRan(); ok && !ranThrough(prev) {
				skipping, why = true, previousFailed+": "+prev.Name
				if prev.Status == "SKIP" && !strings.HasPrefix(prev.Message, previousFailed) {
					why += " (" + prev.Message + ")"
				}
			}
			continue
		}
		// The finger step touches the simulated sensor only: a board's own
		// one, or none, cannot be touched by a script.
		if cmd == "require-fingerprint" {
			r.waitFor(func(string) bool { return r.C.lock.heard }, 3000)
			switch s := r.C.lock.st.Sensor; s {
			case fingerprint.SimName:
			case "":
				skipping, why = true, "needs the simulated fingerprint sensor: this board has no sensor the system can use"
			default:
				skipping, why = true, "needs the simulated fingerprint sensor: this board's is "+s
			}
			continue
		}
		if cmd == "require-lux" {
			skipping = skipping || !r.C.screen.alsSim
			continue
		}
		if cmd == "require-kernel" {
			for _, p := range strings.Fields(arg) {
				if miss, ok := r.C.kernel.Missing[p]; ok && !skipping {
					skipping, why = true, "needs kernel "+p+": "+miss
				}
			}
			continue
		}
		// What another system's kernel lacks (this root mounted in that
		// system) is reported, and says nothing of the image's.
		if cmd == "require-own-kernel" {
			if k := r.C.kernel; k.Foreign != "" && !skipping {
				var lacks []string
				for _, name := range slices.Sorted(maps.Keys(k.Missing)) {
					lacks = append(lacks, name+" ("+k.Missing[name]+")")
				}
				skipping, why = true, fmt.Sprintf("needs the image's own kernel: Linux %s is %s; it lacks %s; done another way: %s",
					k.Release, k.Foreign, firstNonEmpty(strings.Join(lacks, ", "), "nothing"),
					firstNonEmpty(strings.Join(slices.Sorted(maps.Keys(k.Fallback)), ", "), "nothing"))
			}
			continue
		}
		if cmd == "require-netloc" {
			skipping = skipping || r.C.netlocKind() != strings.TrimSpace(arg)
			continue
		}
		if cmd == "require-nfc" {
			r.waitFor(func(string) bool { return r.C.HasNFC() }, 3000)
			skipping = skipping || !r.C.HasNFC()
			continue
		}
		if cmd == "require-display" {
			r.waitFor(func(string) bool { return r.C.HasDisplay() }, 3000)
			skipping = skipping || !r.C.HasDisplay()
			continue
		}
		if cmd == "require-push" {
			r.waitFor(func(string) bool { return r.C.HasPush() }, 3000)
			skipping = skipping || !r.C.HasPush()
			continue
		}
		if cmd == "require-audio" {
			r.waitFor(func(string) bool { return r.C.HasAudio() }, 3000)
			skipping = skipping || !r.C.HasAudio()
			continue
		}
		if cmd == "require-sim-outputs" {
			skipping = skipping || !r.simOutputs()
			continue
		}
		if cmd == "require-tel" {
			r.waitFor(func(string) bool { return r.C.HasTelephony() }, 3000)
			skipping = skipping || !r.C.HasTelephony()
			continue
		}
		if cmd == "require-sms" {
			r.waitFor(func(string) bool { return r.C.HasSMS() }, 3000)
			skipping = skipping || !r.C.HasSMS()
			continue
		}
		if cmd == "require-video" {
			// mediad has read the sample video (it has a decoder).
			r.waitFor(func(string) bool { return r.C.CanPlayVideo() }, 5000)
			skipping = skipping || !r.C.CanPlayVideo()
			continue
		}
		if cmd == "require-sandbox" {
			skipping = skipping || !r.C.Sandboxed()
			continue
		}
		if cmd == "require-cgroup" {
			if r.C.cg == nil && !skipping {
				why = "no per-app cgroups: " + r.C.cgOff
			}
			skipping = skipping || r.C.cg == nil
			continue
		}
		if cmd == "require-pushgw-cluster" {
			gws, _ := r.C.PushGateways()
			skipping = skipping || len(gws) < 2 || r.KillService == nil
			continue
		}
		if cmd == "require-webview" {
			skipping = skipping || !r.C.HasWebView()
			continue
		}
		if cmd == "require-netfw" {
			skipping = skipping || r.C.traffic == nil
			continue
		}
		if cmd == "require-v4l2" {
			skipping = skipping || !strings.HasPrefix(r.C.MediaInfo()["camera"].(string), "v4l2:")
			continue
		}
		if cmd == "require-gui" {
			skipping = skipping || r.Watch == nil
			continue
		}
		if cmd == "require-store" {
			skipping = skipping || r.C.appEnvValue(ui.EnvStoreServer) == ""
			continue
		}
		if cmd == "require-usb" {
			skipping = skipping || !r.C.HasUSB()
			continue
		}
		if cmd == "require-usb-host" {
			skipping = skipping || !strings.HasPrefix(r.C.usb.udc, "dummy_udc")
			continue
		}
		if cmd == "require-light" {
			skipping = skipping || r.C.Light() == ""
			continue
		}
		if cmd == "require-uleds" {
			skipping = skipping || r.C.screen.uled == nil
			continue
		}
		// A case still being made to pass: it runs only where the variable
		// is set.
		if cmd == "require-env" {
			skipping = skipping || os.Getenv(strings.TrimSpace(arg)) == ""
			continue
		}
		if cmd == "require-button" {
			skipping = skipping || !slices.Contains(strings.Fields(os.Getenv(ButtonsEnv)), strings.TrimSpace(arg))
			continue
		}
		if cmd == "require-touch" {
			skipping = skipping || r.Watch == nil || r.GUI == nil || !r.GUI.info.Has("touch")
			continue
		}
		// A frame at least that many logical pixels high: under the
		// on-screen keypad a shorter one has no body left.
		if cmd == "require-tall" {
			n, err := strconv.Atoi(strings.TrimSpace(arg))
			if _, h := r.touchSize(); err != nil {
				skipping, why = true, fmt.Sprintf("require-tall %q: not a number of logical pixels", arg)
			} else if h < float64(n) && !skipping {
				skipping, why = true, fmt.Sprintf("needs a frame %d logical pixels high, this one is %.0f", n, h)
			}
			continue
		}
		if cmd == "require-touchscreen" {
			skipping = skipping || os.Getenv(TouchEnv) == "" || !r.C.cfg.SKU.Touch
			continue
		}
		if cmd == "require-usb-plug" {
			skipping = skipping || os.Getenv(USBPlugEnv) == "" || !r.C.usb.host.on
			continue
		}
		if cmd == "require-sound-switch" {
			skipping = skipping || r.C.cfg.SKU.SoundSwitch == ""
			continue
		}
		if cmd == "require-lid" {
			skipping = skipping || r.C.cfg.SKU.Lid == ""
			continue
		}
		if cmd == "require-radios" {
			skipping = skipping || r.C.wifi == nil || r.C.bt == nil
			continue
		}
		if cmd == "require-bt" {
			skipping = skipping || r.C.bt == nil || radioName(r.C.bt) != strings.TrimSpace(arg)
			continue
		}
		if cmd == "require-bt-peer" {
			if off := r.C.btPeerOff(); off != "" && !skipping {
				skipping, why = true, "needs bt-peer: "+off
			}
			continue
		}
		if cmd == "require-vpn" {
			if v := &r.C.vpn; !slices.Contains(strings.Fields(arg), v.kind) {
				skipping, why = true, "needs vpn "+strings.TrimSpace(arg)+": the VPN here is "+firstNonEmpty(v.kind, "none ("+v.none+")")
			}
			continue
		}
		if cmd == "require-mobile-route" {
			if off := r.C.mobileRouteOff(); off != "" {
				skipping, why = true, "needs mobile-route: "+off
			}
			continue
		}
		if cmd == "require-sleep" {
			if off := r.C.sleepOff(); off != "" && !skipping {
				skipping, why = true, "needs sleep: "+off
			}
			continue
		}
		if cmd == "require-systemd" {
			if off := systemdOff(); off != "" {
				skipping, why = true, "needs systemd: "+off
			}
			continue
		}
		if cmd == "require-wifi-ap" {
			// iwd may not have the emulator's second radio yet.
			off := r.C.wifiAPOff()
			if _, iwd := r.C.wifi.(*iwdRadio); iwd && off != "" {
				r.waitFor(func(string) bool { off = r.C.wifiAPOff(); return off == "" }, 3000)
			}
			if off != "" {
				skipping, why = true, "needs wifi-ap: "+off
			}
			continue
		}
		if cmd == "require-vpn-peer" {
			skipping = skipping || !r.C.canVPNPeer()
			continue
		}
		if cmd == "require-direct-route" {
			if off := r.C.directRouteOff(); off != "" && !skipping {
				skipping, why = true, "needs direct-route: "+off
			}
			continue
		}
		if cmd == "require-cell-net" {
			skipping = skipping || !r.C.canCellNet()
			continue
		}
		if cmd == "require-hotspot" {
			skipping = skipping || r.C.hotspotKind() != strings.TrimSpace(arg)
			continue
		}
		if cmd == "require-metered-ap" {
			_, sim := r.C.wifi.(*simWifi)
			skipping = skipping || !sim
			continue
		}
		if cmd == "require-eap-ap" {
			can := false
			switch w := r.C.wifi.(type) {
			case *simWifi:
				can = true
			case *iwdRadio:
				can = w.canEAP()
			}
			skipping = skipping || !can
			continue
		}
		if cmd == "require-restart" {
			skipping = skipping || r.Restart == nil
			continue
		}
		if cmd == "require-reboot" {
			skipping = skipping || !r.CanReboot
			continue
		}
		// The anti-theft lock keeps its record there; go test's stacks
		// have none.
		if cmd == "require-persist" {
			skipping = skipping || r.C.cfg.PersistDir == ""
			continue
		}
		// A phone's image (its board's sku.json), not a developer machine.
		if cmd == "require-device" {
			skipping = skipping || r.C.cfg.SKU.ID == ""
			continue
		}
		// A developer machine: on a phone, the emulator too, the power
		// menu's Power off powers it off.
		if cmd == "require-dev-machine" {
			skipping = skipping || r.C.cfg.SKU.ID != ""
			continue
		}
		if cmd == "require-fdstore" {
			skipping = skipping || !r.C.fdstore || r.ResumeFile == ""
			continue
		}
		if r.Keep != nil && !kept && cur != "(setup)" {
			kept = true
			r.keepState()
		}
		if cmd == "shell-restart" || cmd == "shell-crash" {
			// The next Shell continues after this line, inside this case:
			// an in-place restart, or (shell-crash) the one systemd starts
			// with the connections from its fd store.
			path := r.ResumeFile
			if path == "" {
				path = filepath.Join(r.C.cfg.RunDir, "selftest-resume.json")
			}
			res := Resume{Line: lineNo, Case: cur, Cases: r.Cases, CapStats: r.C.CapStats(), Pids: r.pids}
			if cmd == "shell-crash" {
				res.Base = r.Base
			}
			var err error
			res.KeepServer, err = r.keepCarry(cmd == "shell-crash")
			if err == nil {
				raw, _ := json.Marshal(r.resume(res))
				err = os.WriteFile(path, raw, 0o600)
			}
			if err == nil && cmd == "shell-crash" {
				fmt.Fprintf(r.Out, "  line %d: shell-crash: exiting without cleanup\n", lineNo)
				os.Exit(3)
			}
			if err == nil {
				if r.Restart == nil {
					err = errors.New("no in-place restart here")
				} else {
					err = r.Restart(path)
				}
			}
			finish(false)
			fmt.Fprintf(r.Out, "  line %d: %s: %v\n", lineNo, line, err)
			failed = true
			break
		}
		r.C.Trace("script %d: %s", lineNo, line)
		r.line, r.cur = lineNo, cur
		if afterReboot {
			afterReboot = false
			if cmd == "rebooted" {
				continue
			}
		}
		switch cmd {
		case "key", "key-if", "type", "fill", "long", "snap":
			// A script's input comes from someone looking at the screen.
			r.C.Wake("script")
		}
		if err := r.step(cmd, arg); err != nil {
			why = fmt.Sprintf("line %d: %s: %v", lineNo, line, err)
			finish(false)
			fmt.Fprintf(r.Out, "  line %d: %s: %v\n  screen:\n%s", lineNo, line, err, indent(r.screen()))
			fmt.Fprintf(r.Out, "  processes:\n")
			for _, l := range r.C.ProcReport() {
				fmt.Fprintf(r.Out, "    %s\n", l)
			}
			failed = true
			break
		}
	}
	if failed && r.KeepGoing != nil {
		for next := lineNo + 1; sc.Scan(); next++ {
			if strings.HasPrefix(strings.TrimSpace(sc.Text()), "== ") {
				fmt.Fprintf(r.Out, "  keep going: %v\n", r.keepGoing(next-1))
				break
			}
		}
	}
	if !failed && cur != "(setup)" {
		finish(true)
	}
	if r.Baselines != nil {
		r.Baselines.end(r, script)
	}
	pass := 0
	for _, x := range results {
		if x.ok {
			pass++
		}
	}
	verdict := "PASS"
	if failed || differed {
		verdict = "FAIL"
	}
	for _, c := range r.Cases {
		if c.Status == "FAIL" {
			fmt.Fprintf(r.Out, "PHONE-SELFTEST-FAILED: %s\n", strings.TrimSuffix(c.Name+": "+c.Message, ": "))
		}
	}
	if k := r.kept; k.Cases > 0 {
		fmt.Fprintf(r.Out, "PHONE-SELFTEST-KEPT: the state at %d case starts, %s each on average, %s at most, %d bytes copied\n",
			k.Cases, (k.Took / time.Duration(k.Cases)).Round(time.Microsecond), k.Max.Round(time.Microsecond), k.Bytes)
	}
	fmt.Fprintf(r.Out, "PHONE-SELFTEST-RESULT: %s (%d/%d cases)\n", verdict, pass, len(results))
	return !failed && !differed
}

// snapsDiffer begins the message of a case that ran through but whose
// snapshots differ from the baselines.
const snapsDiffer = "snapshots differ from the baselines"

// previousFailed begins the message of a case skipped by require-previous.
const previousFailed = "needs the case before it, which did not pass"

// partMark begins a part of the script: its cases go on from the boot and
// the cases above them in the part, never from another part's.
const partMark = "#part "

// lastRan is the last case of this part that ran before this one, or was
// skipped for the one before it (require-previous): the cases skipped for
// the environment or --only in between do not count. With none, it is the
// last case of the part skipped for the environment: this one would go on
// from a case that did not run.
func (r *Runner) lastRan() (CaseResult, bool) {
	var skipped *CaseResult
	for _, c := range slices.Backward(r.Cases[min(r.partFrom, len(r.Cases)):]) {
		if c.Status != "SKIP" || strings.HasPrefix(c.Message, previousFailed) {
			return c, true
		}
		if skipped == nil && c.Message != notSelected {
			skipped = &c
		}
	}
	if skipped != nil {
		return *skipped, true
	}
	return CaseResult{}, false
}

// ranThrough: the case did all its steps (it passed, or only its
// snapshots differ).
func ranThrough(c CaseResult) bool {
	return c.Status == "PASS" || c.Status == "FAIL" && strings.HasPrefix(c.Message, snapsDiffer)
}

// unlock enters the PIN on the lock screen, as a person would, if the phone
// is locked with one: a phone with a PIN starts locked (a restart, or the
// start over after a failed case), and keystored lets a fingerprint unlock
// only once the PIN has been entered since it started.
func (r *Runner) unlock(pin string) error {
	r.waitFor(func(string) bool { return r.C.lock.heard }, 3000)
	if !r.C.secureLocked() {
		return nil
	}
	pad := func(string) bool {
		if n := len(r.C.overlays); n > 0 {
			_, ok := r.C.overlays[n-1].(*lockPadOverlay)
			return ok
		}
		return false
	}
	if !pad("") {
		r.press(Named(KeyEnter))
	}
	if _, ok := r.waitFor(pad, 3000); !ok {
		return errors.New("the lock screen's PIN pad did not open")
	}
	for _, ch := range pin {
		r.typed(Runes(string(ch)))
		r.pump(10 * time.Millisecond)
	}
	r.press(Named(KeyEnter))
	if _, ok := r.waitFor(func(string) bool { return !r.C.Locked() }, 5000); !ok {
		return fmt.Errorf("the PIN %s did not unlock the phone", pin)
	}
	return nil
}

// keepState brings Keep up to date at a case's start. A copy that failed
// is not restored (Restore finds no whole copy).
func (r *Runner) keepState() {
	st, err := r.Keep.Save()
	if err != nil {
		fmt.Fprintf(r.Out, "  keep going: the state at the case's start was not kept: %v\n", err)
		return
	}
	r.kept.Cases++
	r.kept.Took += st.Took
	r.kept.Max = max(r.kept.Max, st.Took)
	r.kept.Bytes += st.Bytes
	r.C.Trace("state kept: %s", st)
}

// keepGoing hands the run to a phone started over, which goes on after
// line (the end of the failed case) with the cases, the capabilities
// called and the snapshots compared so far.
func (r *Runner) keepGoing(line int) error {
	path := r.ResumeFile
	if path == "" {
		path = filepath.Join(r.C.cfg.RunDir, "selftest-resume.json")
	}
	res := r.resume(Resume{Line: line, Case: "(setup)", Cases: r.Cases, CapStats: r.C.CapStats(), Pids: r.pids})
	res.Clock = r.caseClock
	raw, _ := json.Marshal(res)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return err
	}
	fmt.Fprintf(r.Out, "PHONE-SELFTEST-KEEP-GOING: the phone starts over for the case at line %d\n", line+1)
	return r.KeepGoing(path)
}

func indent(s string) string {
	var b strings.Builder
	for _, l := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		b.WriteString("  | " + l + "\n")
	}
	return b.String()
}

// lastNum splits a trailing integer argument off, e.g. "<contact name> 3000".
func lastNum(arg string, def int) (string, int) {
	if i := strings.LastIndex(arg, " "); i > 0 {
		if n, err := strconv.Atoi(arg[i+1:]); err == nil {
			return arg[:i], n
		}
	}
	return arg, def
}

func (r *Runner) step(cmd, arg string) error {
	r.watchToasts(cmd)
	r.watchAudio(cmd)
	defer r.holdBoot(cmd)
	switch cmd {
	case "key-if":
		// key-if <key> <text> [ms]: press the key if the text shows within
		// the time (a dialog only some devices raise), else go on.
		name, rest, _ := strings.Cut(strings.TrimSpace(arg), " ")
		text, ms := lastNum(rest, 3000)
		k, ok := keyNames[name]
		if !ok && utf8.RuneCountInString(name) != 1 {
			return fmt.Errorf("unknown key %q", name)
		}
		if _, shown := r.waitFor(func(s string) bool { return strings.Contains(s, strings.TrimSpace(text)) }, ms); shown {
			if ok {
				r.press(Named(k))
			} else {
				r.typed(Runes(name))
			}
			r.pump(40 * time.Millisecond)
		}
	case "screen-auto":
		r.C.SetScreenAuto(strings.TrimSpace(arg) == "on")
	case "cpu-charge":
		id, ms := lastNum(strings.TrimSpace(arg), 0)
		r.C.ChargeCPU(strings.TrimSpace(id), time.Duration(ms)*time.Millisecond)
	case "screen-use":
		if strings.TrimSpace(arg) == "clear" {
			r.C.ClearScreenTime()
			break
		}
		id, m := lastNum(strings.TrimSpace(arg), 0)
		r.C.ChargeScreenTime(strings.TrimSpace(id), time.Duration(m)*time.Minute)
	case "bgcpu":
		// bgcpu <app-id> <ms> [timeout ms]: wait until the app's background CPU of the day reaches ms.
		f := strings.Fields(arg)
		if len(f) < 2 {
			return errors.New("bgcpu <app-id> <ms> [timeout ms]")
		}
		want, _ := strconv.Atoi(f[1])
		ms := 10000
		if len(f) > 2 {
			ms, _ = strconv.Atoi(f[2])
		}
		got := func() time.Duration {
			for _, u := range r.C.CPUUsage() {
				if u.ID == f[0] {
					return u.Background
				}
			}
			return 0
		}
		if _, ok := r.waitFor(func(string) bool { return got() >= time.Duration(want)*time.Millisecond }, ms); !ok {
			return fmt.Errorf("%s used %s in the background, not %d ms", f[0], got(), want)
		}
	case "no-light":
		r.C.SetLightPresent(strings.TrimSpace(arg) != "on")
	case "sound-switch":
		return r.C.SetSoundSwitch(strings.TrimSpace(arg))
	case "lid":
		switch a := strings.TrimSpace(arg); a {
		case "shut", "open":
			return r.C.SetLid(a == "shut")
		}
		return errors.New("lid shut|open")
	case "buttons-only":
		r.C.SetButtonsOnly(strings.TrimSpace(arg) == "on")
	case "soft-keypad":
		f := strings.Fields(arg)
		ms := 3000
		if len(f) > 1 {
			ms, _ = strconv.Atoi(f[1])
		}
		if len(f) == 0 {
			return fmt.Errorf("usage: soft-keypad text|digits|phone|date|none [ms]")
		}
		want := strings.TrimPrefix(f[0], "none")
		got := ""
		if _, ok := r.waitFor(func(string) bool { got = r.C.keypadLayout(); return got == want }, ms); !ok {
			return fmt.Errorf("the on-screen keypad is %q, want %q", got, want)
		}
	case "power-key":
		switch a := strings.TrimSpace(arg); a {
		case "on", "off":
			r.C.SetPowerKey(a == "on")
		case "sku":
			r.C.SetPowerKey(r.C.cfg.SKU.PowerKey)
		default:
			return fmt.Errorf("power-key on|off|sku")
		}
	case "alarm-in":
		secs, label, _ := strings.Cut(strings.TrimSpace(arg), " ")
		n, err := strconv.Atoi(secs)
		if err != nil {
			return fmt.Errorf("alarm-in <seconds> <label>")
		}
		if err := r.C.AlarmIn(time.Duration(n)*time.Second, label); err != nil {
			return err
		}
	case "system-sleep":
		r.C.SetSleep(strings.TrimSpace(arg) == "on")
	case "slept":
		n, ms := lastNum(strings.TrimSpace(arg), 5000)
		want, err := strconv.Atoi(strings.TrimSpace(n))
		if err != nil {
			return fmt.Errorf("slept <times> [ms]")
		}
		if _, ok := r.waitFor(func(string) bool { got, _ := r.C.Sleeps(); return got >= want }, ms); !ok {
			got, why := r.C.Sleeps()
			return fmt.Errorf("slept %d times, not %d (awake: %s)", got, want, why)
		}
	case "tap":
		r.drawNow()
		if err := r.C.tapText(strings.TrimSpace(arg)); err != nil {
			return err
		}
	case "screen":
		state, ms := lastNum(strings.TrimSpace(arg), 5000)
		off := strings.TrimSpace(state) == "off"
		if _, ok := r.waitFor(func(string) bool {
			return r.C.ScreenOff() == off && r.C.panelSettled()
		}, ms); !ok {
			return fmt.Errorf("the screen is not %s (off %v, panel on %v%s)", state, r.C.ScreenOff(), r.C.screen.panel, r.C.panelWhy())
		}
	case "screen-key":
		name := strings.TrimSpace(arg)
		k, ok := keyNames[name]
		if !ok {
			return fmt.Errorf("unknown key %q", name)
		}
		r.press(Named(k))
		r.pump(40 * time.Millisecond)
	case "finger":
		// finger <name>: touch the simulated fingerprint sensor with that finger.
		if err := r.C.TouchFinger(strings.TrimSpace(arg)); err != nil {
			return err
		}
		r.pump(100 * time.Millisecond)
	case "fingerprint-at":
		place := strings.TrimSpace(arg)
		switch place {
		case "own":
			place = ""
		case ui.FingerprintOKKey:
		default:
			return fmt.Errorf("fingerprint-at %q: ok-key or own", place)
		}
		if err := r.C.SetFingerPlace(place); err != nil {
			return err
		}
	case "fingerprint-power":
		state, ms := lastNum(strings.TrimSpace(arg), 5000)
		want := strings.TrimSpace(state) == "on"
		var last error
		if _, ok := r.waitFor(func(string) bool {
			on, err := r.C.FingerPowered()
			last = err
			return err == nil && on == want
		}, ms); !ok {
			return fmt.Errorf("the fingerprint sensor is not powered %s (%v)", state, last)
		}
	case "unlock":
		return r.unlock(strings.TrimSpace(arg))
	case "locked":
		// locked on|off [ms]: wait for the lock (any kind) to be on or off.
		state, ms := lastNum(strings.TrimSpace(arg), 5000)
		want := strings.TrimSpace(state) == "on"
		if _, ok := r.waitFor(func(string) bool { return r.C.Locked() == want }, ms); !ok {
			return fmt.Errorf("the phone is not locked %s", state)
		}
	case "backlight":
		rest, ms := lastNum(arg, 5000)
		want, err := strconv.Atoi(strings.TrimSpace(rest))
		if err != nil {
			return fmt.Errorf("backlight <percent> [ms]")
		}
		if _, ok := r.waitFor(func(string) bool { return r.C.Backlight() == want }, ms); !ok {
			return fmt.Errorf("backlight at %d%%, want %d%%", r.C.Backlight(), want)
		}
	case "keylight":
		rest, ms := lastNum(arg, 5000)
		want := -1
		if rest = strings.TrimSpace(rest); rest != "off" {
			n, err := strconv.Atoi(rest)
			if err != nil {
				return fmt.Errorf("keylight <percent>|off [ms]")
			}
			want = n
		}
		if _, ok := r.waitFor(func(string) bool { return r.C.KeyLight() == want }, ms); !ok {
			return fmt.Errorf("keyboard backlight at %d%%, want %d%% (-1: off)", r.C.KeyLight(), want)
		}
	case "lux":
		lux, err := strconv.ParseFloat(strings.TrimSpace(arg), 64)
		if err != nil {
			return fmt.Errorf("lux <lux>")
		}
		if !r.C.SetLux(lux) {
			return fmt.Errorf("lux: the light sensor is a real one")
		}
	case "light":
		want, ms := lastNum(strings.TrimSpace(arg), 5000)
		want = strings.TrimSpace(want)
		if _, ok := r.waitFor(func(string) bool { return r.C.Light() == want }, ms); !ok {
			return fmt.Errorf("the notification light shows %q", r.C.Light())
		}
	case "light-blinks":
		ns, ms := lastNum(strings.TrimSpace(arg), 15000)
		n, _ := strconv.Atoi(strings.TrimSpace(ns))
		if _, ok := r.waitFor(func(string) bool { return r.C.LightBlinks() >= int64(n) }, ms); !ok {
			return fmt.Errorf("the light blinked %d times with the slow blink's period, not %d", r.C.LightBlinks(), n)
		}
	case "usb-cable":
		return r.C.PlugUSB(strings.TrimSpace(arg) == "on")
	case "usb-get", "usb-put":
		path, text, _ := strings.Cut(strings.TrimSpace(arg), " ")
		addr, pass := r.C.USBSharing()
		return r.computer(addr, func(do httpDo, _ dialPort, base string) error {
			method, body, want := "GET", "", http.StatusOK
			if cmd == "usb-put" {
				method, body, want = "PUT", text, http.StatusCreated
			}
			req, err := http.NewRequest(method, base+path, strings.NewReader(body))
			if err != nil {
				return err
			}
			req.SetBasicAuth("kario", pass)
			resp, err := do(req)
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			got, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			if resp.StatusCode != want {
				return fmt.Errorf("%s %s: %s", method, path, resp.Status)
			}
			if cmd == "usb-get" && !strings.Contains(string(got), text) {
				return fmt.Errorf("GET %s has no %q: %.300s", path, text, got)
			}
			return nil
		})
	case "sandbox-read":
		return r.sandboxRead(arg)
	case "fetch-server":
		return r.fetchServe(arg)
	case "fetch-got":
		return r.fetchGot(arg)
	case "keep-server", "keep-send", "keep-got", "keep-conns", "keep-reset", "keepalive", "keepalive-due", "keepalive-net":
		return r.keepStep(cmd, arg)
	case "fdstore":
		return r.fdStoreStep(arg)
	case "shell-log":
		// shell-log <text>: a line of the Shell's own log that holds
		// text, copied into the report.
		var found string
		if _, ok := r.waitFor(func(string) bool {
			raw, _ := os.ReadFile(r.shellLog())
			for _, l := range strings.Split(string(raw), "\n") {
				if strings.Contains(l, arg) {
					found = l
				}
			}
			return found != ""
		}, 5000); !ok {
			return fmt.Errorf("no line holding %q in the Shell's log", arg)
		}
		fmt.Fprintf(r.Out, "PHONE-SHELL-LOG: %s\n", found)
	case "usb-port":
		// usb-port <port> open|closed: the computer can (or cannot) connect to the phone's port.
		f := strings.Fields(arg)
		if len(f) != 2 {
			return errors.New("usb-port <port> open|closed")
		}
		port, _ := strconv.Atoi(f[0])
		addr, _ := r.C.USBSharing()
		return r.computer(addr, func(_ httpDo, dial dialPort, _ string) error {
			conn, err := dial(port)
			if conn != nil {
				conn.Close()
			}
			switch {
			case f[1] == "open" && err != nil:
				return fmt.Errorf("port %d is closed to the computer: %v", port, err)
			case f[1] == "closed" && err == nil:
				return fmt.Errorf("port %d is open to the computer", port)
			}
			return nil
		})
	case "usb-refused":
		if addr, _ := r.C.USBSharing(); addr != "" {
			return fmt.Errorf("still sharing on %s", addr)
		}
		err := r.computer("", func(do httpDo, _ dialPort, base string) error {
			req, _ := http.NewRequest("GET", base+"/", nil)
			resp, err := do(req)
			if err == nil {
				resp.Body.Close()
				return fmt.Errorf("the computer still reaches the phone: %s", resp.Status)
			}
			return nil
		})
		if err != nil && strings.Contains(err.Error(), "still reaches") {
			return err
		}
	case "usb-helper":
		return r.usbHelperCmd(arg)
	case "usb-put-big":
		return r.usbPutBig(arg)
	case "usb-mtp":
		return r.usbMTPCmd(arg)
	case "dev", "dev-ssh":
		return r.devCmd(arg, cmd == "dev-ssh")
	case "dev-debug":
		return r.devDebugOpen(arg)
	case "dev-debug-send":
		return r.devDebugSend(arg)
	case "dev-debug-wait":
		return r.devDebugWaitStep(arg)
	case "dev-debug-end":
		return r.devDebugEnd()
	case "dev-rm":
		return r.devRemove(arg)
	case "dev-forward":
		return r.devForwardStep(arg)
	case "pkg-install":
		return r.pkgInstall(strings.TrimSpace(arg))
	case "pkg-uninstall":
		return r.pkgUninstall(strings.TrimSpace(arg))
	case "jscache":
		return r.jsCacheCmd(arg)
	case "app-bench":
		return r.appBench(arg)
	case "key":
		// key [shift+]<name> [n]: shift+ as Shift held (selecting text).
		name, n := lastNum(arg, 1)
		name, shift := strings.CutPrefix(name, "shift+")
		k, ok := keyNames[name]
		if !ok && name != "space" {
			return fmt.Errorf("unknown key %q", name)
		}
		for i := 0; i < n; i++ {
			if name == "space" {
				r.typed(Runes(" "))
			} else {
				r.press(Key{Name: k, Shift: shift})
			}
			r.pump(40 * time.Millisecond)
		}
	case "button":
		return r.button(arg)
	case "button-burst":
		return r.buttonBurst(arg)
	case "touch":
		return r.touch(arg)
	case "touchscreen":
		return r.touchscreen(arg)
	case "usb-plug":
		return r.usbPlug(arg)
	case "chr":
		// chr <character> [n]: the key pressed n times with Chr held.
		ch, n := lastNum(arg, 1)
		if len([]rune(ch)) != 1 {
			return fmt.Errorf("usage: chr <character> [n]")
		}
		for i := 1; i <= n; i++ {
			r.typed(Key{Name: KeyRunes, Runes: []rune(ch), Chr: i})
			r.pump(40 * time.Millisecond)
		}
	case "tel":
		// tel ring <number> | tel sms <from> <text> | tel lock <pin>: drive the simulated baseband.
		f := strings.SplitN(arg, " ", 3)
		var err error
		switch {
		case len(f) >= 2 && f[0] == "ring":
			err = r.C.TelHook(svc.TelSimRing, map[string]string{"number": f[1]})
		case len(f) == 2 && f[0] == "hangup":
			err = r.C.TelHook(svc.TelSimHangup, map[string]string{"number": f[1]})
		case len(f) >= 2 && f[0] == "slow":
			ms := strings.Fields(strings.TrimPrefix(arg, "slow "))
			args := map[string]int{}
			for i, k := range []string{"dial_ms", "answer_ms"} {
				if i < len(ms) {
					n, perr := strconv.Atoi(ms[i])
					if perr != nil || n < 0 {
						return fmt.Errorf("usage: tel slow <dial-ms> [<answer-ms>]")
					}
					args[k] = n
				}
			}
			err = r.C.TelHook(svc.TelSimSlow, args)
		case len(f) == 3 && f[0] == "sms":
			err = r.C.TelHook(svc.TelSimSMS, map[string]string{"from": f[1], "text": f[2]})
		case len(f) == 3 && f[0] == "held":
			err = r.C.TelHook(svc.TelSimSMS, map[string]any{"from": f[1], "text": f[2], "held": true})
		case len(f) == 3 && f[0] == "flash":
			err = r.C.TelHook(svc.TelSimSMS, map[string]any{"from": f[1], "text": f[2], "class": 0})
		case len(f) == 1 && f[0] == "kill":
			err = r.C.TelKill(func(cond func() bool) bool {
				_, ok := r.waitFor(func(string) bool { return cond() }, 15000)
				return ok
			})
		case len(f) == 3 && f[0] == "cb":
			cb := strings.SplitN(arg, " ", 4)
			ch, e1 := strconv.Atoi(f[1])
			serial, e2 := strconv.Atoi(strings.Fields(f[2])[0])
			if e1 != nil || e2 != nil || len(cb) < 4 {
				return fmt.Errorf("usage: tel cb <channel> <serial> <text>")
			}
			err = r.C.TelHook(svc.TelSimCB, map[string]any{"channel": ch, "serial": serial, "text": cb[3]})
		case len(f) >= 2 && f[0] == "pending":
			rest, ms := lastNum(strings.TrimPrefix(arg, "pending "), 5000)
			want, perr := strconv.Atoi(strings.TrimSpace(rest))
			if perr != nil {
				return fmt.Errorf("usage: tel pending <n> [ms]")
			}
			n := -1
			if _, ok := r.waitFor(func(string) bool {
				var pending []json.RawMessage
				if r.C.telRPC(svc.TelSMSPending, nil, &pending) == nil {
					n = len(pending)
				}
				return n == want
			}, ms); !ok {
				return fmt.Errorf("telephonyd holds %d unacknowledged events, want %d", n, want)
			}
		case len(f) == 2 && f[0] == "lock":
			err = r.C.TelHook(svc.TelSimLock, map[string]string{"pin": f[1]})
		case len(f) >= 2 && f[0] == "ussd":
			// The network asks a question by USSD (a reply ends it).
			err = r.C.TelHook(svc.TelSimUSSD, map[string]any{"text": strings.TrimPrefix(arg, "ussd "), "request": true})
		case f[0] == "network":
			err = r.C.TelHook(svc.TelSimNetwork, simNetworkArgs(strings.Fields(arg)[1:]))
		case len(f) >= 2 && f[0] == "roaming" && f[1] == "off":
			err = r.C.TelHook(svc.TelSimRoam, map[string]string{})
		case len(f) >= 2 && f[0] == "roaming" && len(f[1]) >= 5 && len(f[1]) <= 6:
			args := map[string]string{"net_mcc": f[1][:3], "net_mnc": f[1][3:]}
			if len(f) == 3 {
				args["operator"] = f[2]
			}
			err = r.C.TelHook(svc.TelSimRoam, args)
		case len(f) == 2 && f[0] == "ims":
			err = r.C.TelHook(svc.TelSimIMS, map[string]string{"state": f[1]})
		default:
			err = fmt.Errorf("usage: tel ring <number> | tel hangup <number> | tel slow <dial-ms> [<answer-ms>] | tel sms <from> <text> | tel held <from> <text> | tel kill | tel cb <channel> <serial> <text> | tel pending <n> [ms] | tel lock <pin> | tel ussd <text> | tel network <sim-mcc> <network-mcc> [<nitz-minutes>] | tel roaming <mcc><mnc> [<name>] | tel roaming off | tel ims <state>")
		}
		if err != nil {
			return err
		}
		r.pump(100 * time.Millisecond)
	case "audio":
		// audio <category> | audio paused <category> | audio idle: wait for
		// what phone-audiod reports. A stream it reported in that state since
		// the last input or audio step counts too (watchAudio); idle is now.
		rest, ms := lastNum(arg, 8000)
		f := strings.Fields(rest)
		idle := len(f) == 1 && f[0] == "idle"
		in := func(s audio.Stream) bool {
			switch {
			case len(f) == 2 && f[0] == ui.PlayPaused:
				return s.Category == f[1] && s.State == ui.PlayPaused
			case len(f) == 1 && !idle:
				return s.Category == f[0] && s.State == ui.PlayPlaying
			}
			return false
		}
		want := func(streams []audio.Stream) bool {
			if idle {
				for _, s := range streams {
					if s.State == ui.PlayPlaying && s.Category != audio.Notify {
						return false
					}
				}
				return true
			}
			return slices.ContainsFunc(streams, in)
		}
		var last []audio.Stream
		_, ok := r.waitFor(func(string) bool {
			st, err := r.C.AudioState()
			last = st
			return (err == nil && want(st)) || slices.ContainsFunc(r.heard, in)
		}, ms)
		heard := r.heard
		r.heard = nil
		if !ok {
			return fmt.Errorf("audio %s: streams are %+v (reported since the last input or audio step: %+v)", rest, last, heard)
		}
	case "push":
		// push [voip] [normal] [sealed] <app-id> <title> | <body> | <data>: as the app's server.
		var o TestPush
		for {
			word, rest, _ := strings.Cut(strings.TrimSpace(arg), " ")
			switch word {
			case "voip":
				o.Kind = "voip"
			case "normal":
				o.Priority = "normal"
			case "sealed":
				o.Seal = true
			case "other":
				o.Other = true
			default:
				goto parsed
			}
			arg = rest
		}
	parsed:
		id, rest, _ := strings.Cut(strings.TrimSpace(arg), " ")
		var fields []string
		for _, f := range strings.SplitN(rest, "|", 3) {
			fields = append(fields, strings.TrimSpace(f))
		}
		if err := r.C.SendTestPush(id, o, fields...); err != nil {
			return fmt.Errorf("push: %w", err)
		}
	case "timeshift":
		sec, _ := strconv.Atoi(arg)
		r.C.SetClockSkew(time.Duration(sec) * time.Second)
		r.pump(600 * time.Millisecond)
	case "long":
		if k, ok := keyNames[arg]; ok {
			r.press(Key{Name: k, Long: true})
		} else {
			r.typed(LongRunes(arg))
		}
		r.pump(40 * time.Millisecond)
	case "keypad":
		r.C.SetKeypad(arg)
	case "fn-layer":
		layer := map[string]string{}
		for _, kv := range strings.Fields(arg) {
			k, v, ok := strings.Cut(kv, "=")
			if !ok || k == "" || len([]rune(v)) != 1 {
				return fmt.Errorf("fn-layer: %q is not <key>=<character>", kv)
			}
			layer[k] = v
		}
		r.C.SetFnLayer(layer)
	case "imemode":
		r.C.SetIMEMode(arg)
	case "gui-env":
		f := strings.Fields(arg)
		if len(f) != 2 {
			return fmt.Errorf("usage: gui-env <name> <value>")
		}
		if r.Watch == nil {
			return nil
		}
		has := func(string) bool {
			raw, _ := os.ReadFile(fmt.Sprintf("/proc/%d/environ", r.Watch.G.Pid()))
			return strings.Contains("\x00"+string(raw), "\x00"+f[0]+"="+f[1]+"\x00")
		}
		if _, ok := r.waitFor(has, 5000); !ok {
			return fmt.Errorf("the renderer (pid %d) does not have %s=%s", r.Watch.G.Pid(), f[0], f[1])
		}
	case "locale":
		f := strings.Fields(arg)
		if len(f) != 2 {
			return fmt.Errorf("usage: locale <lang> <zone>")
		}
		if _, ok := r.waitFor(func(string) bool { return r.C.Language() == f[0] && r.C.ZoneName() == f[1] }, 5000); !ok {
			return fmt.Errorf("the language is %s and the time zone %s", r.C.Language(), r.C.ZoneName())
		}
	case "setting":
		f := strings.Fields(arg)
		if len(f) != 2 && len(f) != 3 {
			return fmt.Errorf("usage: setting <key> <value> [<text>]")
		}
		v, err := strconv.ParseFloat(f[1], 64)
		if err != nil {
			return err
		}
		args := ui.SettingArgs{Key: f[0], Value: v}
		if len(f) == 3 {
			args.Text = f[2]
		}
		if ok, err := r.C.appNetSetting(args); ok {
			return err
		}
		// As Settings asks: lock_pin raises the system's PIN screen, which
		// the script then types into.
		if r.C.securitySetting(args, func(err error) {
			if err != nil {
				r.C.log.Printf("selftest: setting %s: %v", args.Key, err)
			}
		}) {
			return nil
		}
		return r.C.settingsSet(f[0], v)
	case "pkg-tamper":
		f := strings.Fields(arg)
		a := r.C.AppByID(f[0])
		if len(f) != 2 || a == nil || !a.M.installed {
			return fmt.Errorf("usage: pkg-tamper <installed app-id> <file>")
		}
		fh, err := os.OpenFile(filepath.Join(a.M.dir, f[1]), os.O_WRONLY|os.O_APPEND, 0)
		if err != nil {
			return err
		}
		_, err = fh.Write([]byte{0})
		fh.Close()
		return err
	case "camera-frames":
		rest, ms := lastNum(arg, 5000)
		n, _ := strconv.Atoi(strings.TrimSpace(rest))
		if _, ok := r.waitFor(func(string) bool { f, _ := r.C.MediaInfo()["frames"].(float64); return int(f) >= n }, ms); !ok {
			return fmt.Errorf("the camera captured fewer than %d frames", n)
		}
	case "audio-output":
		if err := r.audioOutput(strings.Fields(arg)); err != nil {
			return err
		}
	case "audio-channels":
		f := strings.Fields(arg)
		if len(f) < 2 || len(f) > 3 || (f[0] != "mono" && f[0] != "stereo") {
			return errors.New("usage: audio-channels mono|stereo <balance> [ms]")
		}
		want, err := strconv.ParseFloat(f[1], 64)
		if err != nil {
			return err
		}
		ms := 5000
		if len(f) == 3 {
			if ms, err = strconv.Atoi(f[2]); err != nil {
				return err
			}
		}
		var st struct {
			Mono    bool    `json:"mono"`
			Balance float64 `json:"balance"`
		}
		if _, ok := r.waitFor(func(string) bool {
			return r.C.audioRPC(svc.AudioState, nil, &st) == nil && st.Mono == (f[0] == "mono") && math.Abs(st.Balance-want) < 1e-9
		}, ms); !ok {
			return fmt.Errorf("phone-audiod plays mono %v, balance %v", st.Mono, st.Balance)
		}
	case "spoken":
		text, ms := lastNum(strings.TrimSpace(arg), 5000)
		text = strings.TrimSpace(text)
		var lines []string
		if _, ok := r.waitFor(func(string) bool {
			var last int
			lines, last = r.C.spokenSince(r.spoken)
			for i, l := range lines {
				if !strings.Contains(l, text) {
					continue
				}
				if r.C.HasAudio() && !r.C.speechHas(l) {
					return false
				}
				r.spoken = last - len(lines) + i + 1
				return true
			}
			return false
		}, ms); !ok {
			return fmt.Errorf("the screen reader did not say %q; it said %q", text, lines)
		}
	case "unspoken":
		ms, err := strconv.Atoi(strings.TrimSpace(arg))
		if err != nil {
			return errors.New("usage: unspoken <ms>")
		}
		_, from := r.C.spokenSince(r.spoken)
		r.pump(time.Duration(ms*max(1, r.Scale)) * time.Millisecond)
		r.C.Render()
		if lines, last := r.C.spokenSince(from); len(lines) > 0 {
			r.spoken = last
			return fmt.Errorf("the screen reader said %q", lines)
		}
		r.spoken = from
	case "a11y-shortcut":
		rest, ms := lastNum(arg, 5000)
		want := strings.TrimSpace(rest)
		if want == "-" {
			want = ""
		}
		var got string
		if _, ok := r.waitFor(func(string) bool { got = r.C.settings.A11y.Shortcut; return got == want }, ms); !ok {
			return fmt.Errorf("the accessibility shortcut is %q, want %q", got, want)
		}
	case "frame-a11y":
		f := strings.Fields(arg)
		if len(f) != 2 {
			return errors.New("usage: frame-a11y <field> <value>")
		}
		fr := &Frame{}
		r.C.a11yFrame(fr)
		raw, _ := json.Marshal(fr)
		var m map[string]json.RawMessage
		_ = json.Unmarshal(raw, &m)
		got := string(m[f[0]])
		if got == "" {
			got = "-"
		}
		if got != f[1] {
			return fmt.Errorf("frame %s is %s, want %s", f[0], got, f[1])
		}
	case "vibrated":
		_, ms := lastNum(arg, 5000)
		base := r.vibrations
		if _, ok := r.waitFor(func(string) bool { return r.C.Vibrations() > base }, ms); !ok {
			return fmt.Errorf("no vibration")
		}
		r.vibrations = r.C.Vibrations()
	case "mem-low":
		n, err := strconv.Atoi(strings.TrimSpace(arg))
		if err != nil {
			return fmt.Errorf("usage: mem-low <n>")
		}
		r.C.SimulateLowMemory(n)
		for range n {
			r.C.checkMemory()
			r.pump(40 * time.Millisecond)
		}
	case "appenv":
		f := strings.Fields(arg)
		if len(f) != 3 {
			return fmt.Errorf("usage: appenv <app-id> <name> <value>")
		}
		a := r.C.AppByID(f[0])
		if a == nil || a.proc == nil {
			return fmt.Errorf("%s is not running", f[0])
		}
		raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/environ", a.proc.Pid))
		if err != nil {
			return err
		}
		want := f[1] + "=" + f[2]
		for _, kv := range strings.Split(string(raw), "\x00") {
			if kv == want {
				return nil
			}
		}
		return fmt.Errorf("%s does not have %s", f[0], want)
	case "gui-crash":
		if r.Watch == nil {
			return fmt.Errorf("no pixel renderer")
		}
		old := r.Watch.G
		pid := old.Pid()
		if err := old.Kill(); err != nil {
			return err
		}
		select {
		case <-old.Done():
		case <-time.After(time.Duration(5*max(1, r.Scale)) * time.Second):
			return fmt.Errorf("renderer %d did not exit", pid)
		}
		r.restartGUI()
		if r.Watch == nil || r.Watch.G.Pid() == pid {
			return fmt.Errorf("the watchdog did not start a new renderer")
		}
		if r.GUI.last == "" {
			return fmt.Errorf("the new renderer did not get the current frame")
		}
		fmt.Fprintf(r.Out, "PHONE-GUI-RESTARTED %s (pid %d → %d)\n", r.GUI.Info(), pid, r.GUI.Pid())
	case "type":
		for _, ch := range arg {
			r.typed(Runes(string(ch)))
			r.pump(10 * time.Millisecond)
		}
		r.pump(40 * time.Millisecond)
	case "fill":
		r.keysFirst()
		v := r.C.current().top()
		if v == nil || v.focused() == nil || v.focused().Kind != ui.KindInput {
			return fmt.Errorf("fill: no input has focus")
		}
		v.inputs[v.focus] = r.fillText(arg)
		r.C.postFrame()
		r.pump(40 * time.Millisecond)
	case "wait-re":
		// wait-re <regexp> [ms]: for text that moves on (a play position).
		pat, ms := lastNum(arg, 8000)
		re, err := regexp.Compile(strings.TrimSpace(pat))
		if err != nil {
			return err
		}
		if _, ok := r.waitFor(func(s string) bool { return re.MatchString(s) || slices.ContainsFunc(r.toasts, re.MatchString) }, ms); !ok {
			return fmt.Errorf("nothing matching %q appeared", pat)
		}
	case "wait", "gone":
		text, ms := lastNum(arg, 8000)
		want := cmd == "wait"
		shown := func(s string) bool { return strings.Contains(s, text) }
		if _, ok := r.waitFor(func(s string) bool { return shown(s) == want || want && slices.ContainsFunc(r.toasts, shown) }, ms); !ok {
			if want {
				return fmt.Errorf("text %q did not appear", text)
			}
			return fmt.Errorf("text %q did not go away", text)
		}
	case "frames":
		_, ms := lastNum(arg, 5000)
		n, _ := strconv.Atoi(strings.Fields(arg)[0])
		base := r.C.FramesSeen()
		if _, ok := r.waitFor(func(string) bool { return r.C.FramesSeen()-base >= n }, ms); !ok {
			return fmt.Errorf("only %d media frames reached the screen", r.C.FramesSeen()-base)
		}
	case "painted":
		_, ms := lastNum(arg, 8000)
		if _, ok := r.waitFor(func(string) bool { ok, _ := r.C.WebPainted(); return ok }, ms); !ok {
			_, why := r.C.WebPainted()
			return fmt.Errorf("the page is not painted: %s", why)
		}
	case "pages-ended":
		n, err := strconv.Atoi(strings.TrimSpace(arg))
		if err != nil {
			return fmt.Errorf("usage: pages-ended <n>")
		}
		if got := r.C.LowMemoryPageStops(); got < n {
			return fmt.Errorf("%d background apps' pages were ended for low memory, want at least %d", got, n)
		}
	case "web-procs":
		n, _ := strconv.Atoi(strings.TrimSpace(arg))
		if got := r.C.WebProcsStarted(); got != n {
			return fmt.Errorf("%d web processes were started, want %d", got, n)
		}
	case "masked":
		if !strings.Contains(r.screen(), "▒") {
			return fmt.Errorf("secure content is not masked in the snapshot")
		}
	case "sleep":
		ms, _ := strconv.Atoi(arg)
		r.pump(time.Duration(ms) * time.Millisecond)
	case "rebooted":
		// rebooted [ms]: the phone restarts before ms pass; the script goes
		// on after this line, in the Shell that starts over (SaveResume).
		ms, err := strconv.Atoi(strings.TrimSpace(arg))
		if err != nil {
			ms = 20000
		}
		r.pump(time.Duration(ms*max(1, r.Scale)) * time.Millisecond)
		return errors.New("the phone did not restart")
	case "theft-server", "theft-bound", "theft-code", "theft-remove", "theft-bind-other", "device-id", "state-file", "device-wipe":
		return r.theftStep(cmd, arg)
	case "freeze-after":
		ms, _ := strconv.Atoi(arg)
		r.C.SetFreezeAfter(time.Duration(ms) * time.Millisecond)
	case "at":
		// at <screen-id>: wait until that screen is on top ("sys.idle", or
		// "<app-id>/<screen-id>").
		rest, ms := lastNum(arg, 8000)
		id := strings.TrimSpace(rest)
		at := func() bool { s := r.C.ScreenID(); return s == id || strings.HasSuffix(s, "/"+id) }
		if _, ok := r.waitFor(func(string) bool { return at() }, ms); !ok {
			return fmt.Errorf("at %s: the screen is %s", id, r.C.ScreenID())
		}
	case "nfc":
		return r.nfcStep(arg)
	case "display":
		return r.displayStep(arg)
	case "location":
		return r.locationStep(arg)
	case "mms", "flashes", "sms", "files-drop":
		return r.mmsStep(cmd, arg)
	case "ota":
		return r.otaStep(arg)
	case "push-sealed":
		// push-sealed <app-id>: the app has received end-to-end encrypted
		// push data (from its server).
		rest, ms := lastNum(arg, 8000)
		id := strings.TrimSpace(rest)
		if _, ok := r.waitFor(func(string) bool { return r.C.SealedPushes(id) > 0 }, ms); !ok {
			return fmt.Errorf("%s received no sealed push data", id)
		}
	case "pushgw-kill":
		// The gateway instance the device is connected to dies; the device
		// must move to another.
		gws, cur := r.C.PushGateways()
		name := ""
		for i, gw := range gws {
			if gw == cur {
				name = []string{"phone-pushgw", "phone-pushgw-b"}[min(i, 1)]
			}
		}
		if name == "" {
			return fmt.Errorf("pushd is not connected to a known gateway (%q of %v)", cur, gws)
		}
		if err := r.KillService(name); err != nil {
			return err
		}
		if _, ok := r.waitFor(func(string) bool { _, now := r.C.PushGateways(); return now != "" && now != cur }, 8000); !ok {
			return fmt.Errorf("the device did not move off %s", cur)
		}
	case "net-send":
		f := strings.Fields(arg)
		if len(f) != 2 || (f[1] != "sent" && f[1] != "blocked") {
			return fmt.Errorf("usage: net-send <app-id> <sent|blocked>")
		}
		send := r.C.sendAs
		if r.C.traffic == nil {
			send = r.C.policySend
		}
		err := send(f[0])
		switch {
		case f[1] == "sent" && err != nil:
			return fmt.Errorf("%s could not send: %v", f[0], err)
		case f[1] == "blocked" && err == nil:
			return fmt.Errorf("%s sent past the firewall", f[0])
		case f[1] == "blocked" && !errors.Is(err, syscall.EPERM):
			return fmt.Errorf("%s: %v, want the firewall's EPERM", f[0], err)
		}
		fmt.Fprintf(r.Out, "PHONE-NET-SEND: %s %s (%s)\n", f[0], f[1], map[bool]string{true: "the firewall", false: "the Shell's policy"}[r.C.traffic != nil])
	case "perm-state":
		f := strings.Fields(arg)
		if len(f) != 3 {
			return fmt.Errorf("usage: perm-state <key> <perm> <state>")
		}
		var got string
		if _, ok := r.waitFor(func(string) bool {
			got = firstNonEmpty(r.C.broker.perms[f[0]][f[1]], "ask")
			return got == f[2]
		}, 5000); !ok {
			return fmt.Errorf("%s %s is %s, want %s", f[0], f[1], got, f[2])
		}
	case "cgroup":
		f, ms := strings.Fields(arg), 8000
		if len(f) == 4 {
			n, err := strconv.Atoi(f[3])
			if err != nil {
				return fmt.Errorf("usage: cgroup <app-id> <file> <value> [ms]")
			}
			f, ms = f[:3], n
		}
		if len(f) != 3 {
			return fmt.Errorf("usage: cgroup <app-id> <file> <value> [ms]")
		}
		if r.C.cg == nil {
			return fmt.Errorf("no per-app cgroups")
		}
		var got string
		if _, ok := r.waitFor(func(string) bool { got = r.C.cg.value(f[0], f[1]); return got == f[2] }, ms); !ok {
			return fmt.Errorf("cgroup %s %s is %q, want %q", f[0], f[1], got, f[2])
		}
	case "mem-event":
		f, ms := strings.Fields(arg), 8000
		var err error
		if len(f) == 4 {
			ms, err = strconv.Atoi(f[3])
			f = f[:3]
		}
		atLeast := 0
		if err == nil && len(f) == 3 {
			atLeast, err = strconv.Atoi(f[2])
		}
		if len(f) != 3 || err != nil {
			return fmt.Errorf("usage: mem-event <app-id> <event> <at-least> [ms]")
		}
		if r.C.cg == nil {
			return fmt.Errorf("no per-app cgroups")
		}
		var got int64
		if _, ok := r.waitFor(func(string) bool {
			got = cgKey(r.C.cg.value(f[0], "memory.events"), f[1])
			return got >= int64(atLeast)
		}, ms); !ok {
			return fmt.Errorf("%s: memory.events %s is %d, want at least %d", f[0], f[1], got, atLeast)
		}
	case "kernel":
		f := strings.Fields(arg)
		if len(f) < 2 || f[0] != "missing" && f[0] != "fallback" {
			return fmt.Errorf("usage: kernel missing|fallback <name>...|none")
		}
		k := r.C.kernel
		missing, fallback := slices.Sorted(maps.Keys(k.Missing)), slices.Sorted(maps.Keys(k.Fallback))
		fmt.Fprintf(r.Out, "PHONE-KERNEL: Linux %s, cgroups %s, Landlock ABI %d, missing %v, fallback %v\n",
			k.Release, k.Cgroups, k.Sandbox.Landlock, missing, fallback)
		got, why := missing, k.Missing
		if f[0] == "fallback" {
			got, why = fallback, k.Fallback
		}
		want := slices.Sorted(slices.Values(f[1:]))
		if len(want) == 1 && want[0] == "none" {
			want = nil
		}
		if !slices.Equal(got, want) {
			return fmt.Errorf("kernel %s %v, want %v: %v", f[0], got, want, why)
		}
	case "mp-build":
		f := strings.Fields(arg)
		if len(f) < 3 {
			return fmt.Errorf("usage: mp-build <example> <host-app-id> <file> [tamper]")
		}
		if err := r.C.buildMiniProgram(f[0], f[1], f[2], len(f) > 3 && f[3] == "tamper"); err != nil {
			return err
		}
	case "pkg-build":
		f := strings.Fields(arg)
		if len(f) < 3 {
			return fmt.Errorf("usage: pkg-build <example> <app-id> <file> [tamper]")
		}
		if err := r.C.buildExample(f[0], f[1], f[2], len(f) > 3 && f[3] == "tamper"); err != nil {
			return err
		}
	case "app-file-drop":
		f := strings.Fields(arg)
		var a *App
		if len(f) == 2 {
			a = r.C.AppByID(f[0])
		}
		if a == nil {
			return fmt.Errorf("usage: app-file-drop <app-id> <file>")
		}
		return os.Remove(r.C.appFile(a, f[1]))
	case "widget":
		f := strings.Fields(arg)
		var a *App
		if len(f) == 2 && f[1] == "off" {
			a = r.C.AppByID(f[0])
		}
		if a == nil {
			return fmt.Errorf("usage: widget <app-id> off")
		}
		return r.C.setWidget(a, "", "")
	case "ime-remote":
		// ime-remote on|off [ms]: keys go (or no longer go) to phone-imed.
		f := strings.Fields(arg)
		ms := 5000
		if len(f) > 1 {
			ms, _ = strconv.Atoi(f[1])
		}
		want := len(f) > 0 && f[0] == "on"
		if _, ok := r.waitFor(func(string) bool { return r.C.IMERemote(want) == want }, ms); !ok {
			return fmt.Errorf("input method remote=%v, want %v", !want, want)
		}
	case "power":
		// power <level> [charging [fast]]
		f := strings.Fields(arg)
		level, _ := strconv.Atoi(f[0])
		r.C.SetPower(Power{Level: level, Charging: len(f) > 1 && f[1] == "charging", Fast: len(f) > 2 && f[2] == "fast"})
		r.pump(200 * time.Millisecond)
	case "job":
		// job <app> <id> runs <n> [ms] | job <app> <id> stops <n> [ms] |
		// job <app> <id> with <other id>
		f, ms := strings.Fields(arg), 3000
		if len(f) == 5 && f[2] != "with" {
			ms, _ = strconv.Atoi(f[4])
			f = f[:4]
		}
		if len(f) != 4 {
			return fmt.Errorf("job <app> <id> runs <n> [ms] | stops <n> [ms] | with <id>")
		}
		ok := func(string) bool {
			runs, batch := r.C.JobRuns(f[0], f[1])
			want, _ := strconv.Atoi(f[3])
			switch f[2] {
			case "with":
				_, other := r.C.JobRuns(f[0], f[3])
				return batch != 0 && batch == other
			case "stops":
				return r.C.JobStops(f[0], f[1]) == want
			case "waits":
				return strings.Contains(r.C.JobHeld(f[0], f[1]), f[3])
			}
			return runs == want
		}
		if _, done := r.waitFor(ok, ms); !done {
			runs, batch := r.C.JobRuns(f[0], f[1])
			return fmt.Errorf("job %s: %d runs, last in wake %d, stopped %d times, waiting for %q", f[1], runs, batch, r.C.JobStops(f[0], f[1]), r.C.JobHeld(f[0], f[1]))
		}
	case "job-add":
		// job-add <app> <id> <every-min> [charging] [unmetered] [network]:
		// the app schedules a job (job.schedule).
		f := strings.Fields(arg)
		a := r.C.AppByID(f[0])
		every := 0
		if len(f) >= 3 {
			every, _ = strconv.Atoi(f[2])
		}
		if a == nil || len(f) < 3 {
			return fmt.Errorf("usage: job-add <app-id> <id> <every-min> [charging] [unmetered] [network]")
		}
		args := ui.JobArgs{ID: f[1], EveryMin: every, Charging: slices.Contains(f[3:], "charging"), Unmetered: slices.Contains(f[3:], "unmetered"),
			Network: slices.Contains(f[3:], "network")}
		return r.C.scheduleJob(a, args)
	case "job-cancel":
		f := strings.Fields(arg)
		if len(f) != 2 || r.C.AppByID(f[0]) == nil {
			return fmt.Errorf("usage: job-cancel <app-id> <id>")
		}
		r.C.cancelJob(r.C.AppByID(f[0]), f[1])
	case "data-charge", "data-usage", "net-blocked": // const-check: ok (a step's name, not the notification's)
		return r.dataStep(cmd, arg)
	case "store-publish":
		// store-publish <example> <version> [rollout <percent>]: a developer
		// publishes through the store backend, to everyone or staged.
		f := strings.Fields(arg)
		rollout := 100
		if len(f) == 4 && f[2] == "rollout" {
			rollout, _ = strconv.Atoi(f[3])
		} else if len(f) != 2 {
			return fmt.Errorf("store-publish <example> <version> [rollout <percent>]")
		}
		if err := r.C.PublishExample(f[0], f[1], rollout); err != nil {
			return err
		}
	case "store-rollout":
		// store-rollout <app-id> <version> <percent>: its developer moves a
		// staged version.
		f := strings.Fields(arg)
		if len(f) != 3 {
			return fmt.Errorf("store-rollout <app-id> <version> <percent>")
		}
		percent, _ := strconv.Atoi(f[2])
		if err := r.C.RolloutExample(f[0], f[1], percent); err != nil {
			return err
		}
	case "store-takedown":
		// store-takedown <app-id> <reason>: the store staff take an app down.
		id, reason, _ := strings.Cut(strings.TrimSpace(arg), " ")
		if err := r.C.TakedownExample(id, reason); err != nil {
			return err
		}
	case "music-import":
		// Copies the sample music files into the media library, as a USB
		// transfer would.
		src := filepath.Join(filepath.Dir(r.C.cfg.AppsDir), "examples", "music")
		ents, err := os.ReadDir(src)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(r.C.musicDir(), 0o700); err != nil {
			return err
		}
		for _, e := range ents {
			raw, err := os.ReadFile(filepath.Join(src, e.Name()))
			if err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(r.C.musicDir(), e.Name()), raw, 0o600); err != nil {
				return err
			}
		}
	case "app-pid":
		// app-pid <id> save | same: the process survives a Shell restart.
		id, op, _ := strings.Cut(arg, " ")
		pid := r.C.AppPid(id)
		if pid == 0 {
			return fmt.Errorf("%s is not running", id)
		}
		if op == "save" {
			if r.pids == nil {
				r.pids = map[string]int{}
			}
			r.pids[id] = pid
			return nil
		}
		if was := r.pids[id]; was != pid {
			return fmt.Errorf("%s runs as pid %d, was %d: it was restarted", id, pid, was)
		}
	case "adopted":
		if !r.C.Adopted(strings.TrimSpace(arg)) {
			return fmt.Errorf("%s was not taken over from the previous Shell", arg)
		}
	case "mem-report":
		fmt.Fprintln(r.Out, MemReport())
	case "app-log":
		// app-log <app-id> <prefix>: the app's log line that starts so,
		// copied into the report (numbers an app measured on the device).
		id, prefix, _ := strings.Cut(strings.TrimSpace(arg), " ")
		var found string
		if _, ok := r.waitFor(func(string) bool {
			raw, _ := os.ReadFile(filepath.Join(r.C.cfg.RunDir, filepath.Base(id)+".log"))
			for _, l := range strings.Split(string(raw), "\n") {
				if strings.HasPrefix(l, prefix) {
					found = l
				}
			}
			return found != ""
		}, 5000); !ok {
			return fmt.Errorf("no line %q in %s's log", prefix, id)
		}
		fmt.Fprintf(r.Out, "PHONE-APP-LOG: %s: %s\n", id, found)
	case "ime-kill":
		pid, err := r.C.IMEPid()
		if err != nil {
			return err
		}
		if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
			return err
		}
	case "sms-kill":
		var wait func(cond func() bool) bool
		switch strings.TrimSpace(arg) {
		case "":
		case "wait":
			wait = func(cond func() bool) bool {
				_, ok := r.waitFor(func(string) bool { return cond() }, 15000)
				return ok
			}
		default:
			return fmt.Errorf("usage: sms-kill [wait]")
		}
		if err := r.C.SMSKill(wait); err != nil {
			return err
		}
	case "alerts":
		rest, ms := lastNum(strings.TrimSpace(arg), 5000)
		want, err := strconv.Atoi(strings.TrimSpace(rest))
		if err != nil {
			return fmt.Errorf("usage: alerts <n> [ms]")
		}
		if _, ok := r.waitFor(func(string) bool { return r.C.AlertsShown() == want }, ms); !ok {
			return fmt.Errorf("%d emergency alerts shown, want %d", r.C.AlertsShown(), want)
		}
	case "appstop":
		id := strings.TrimSpace(arg)
		if r.C.ProcState(id) != ui.PlayStopped {
			if err := r.C.KillApp(id); err != nil {
				return err
			}
		}
		// The process can be gone before the Shell has taken its exit in.
		ended := func(string) bool { a := r.C.appByID(id); return a == nil || !a.running }
		if _, ok := r.waitFor(ended, 5000); !ok {
			return fmt.Errorf("%s did not stop", id)
		}
		r.C.ForgetTombstone(id)
	case "appkill":
		if err := r.C.KillApp(strings.TrimSpace(arg)); err != nil {
			return err
		}
	case "wifi-ap":
		// "metered": its DHCP server sends Android's metered hint (only
		// the simulated radios' can).
		f := strings.Fields(arg)
		if len(f) != 2 && (len(f) != 3 || f[2] != "metered") {
			return fmt.Errorf("wifi-ap <ssid> <passphrase> [metered]")
		}
		hint := ""
		if len(f) == 3 {
			hint = ui.MeteredHintDHCP
		}
		switch w := r.C.wifi.(type) {
		case *simWifi:
			w.addNetwork(f[0], f[1], hint)
		case *iwdRadio:
			if hint != "" {
				return fmt.Errorf("wifi-ap: iwd's access point sends no metered hint")
			}
			if err := w.startAP(f[0], f[1]); err != nil {
				return err
			}
		default:
			return fmt.Errorf("no Wi-Fi")
		}
	case "wifi-join":
		rest, ms := lastNum(arg, 30000)
		f := strings.Fields(rest)
		if len(f) != 2 || r.C.wifi == nil {
			return fmt.Errorf("usage: wifi-join <ssid> <passphrase> [ms] (with Wi-Fi)")
		}
		// iwd knows an access point that came up a moment ago only after a
		// scan (which state starts): a failed connect is tried again.
		w := r.C.wifi
		var l wifiLink
		var res chan error
		var next time.Time
		err := errors.New("not tried")
		if _, ok := r.waitFor(func(string) bool {
			if l, _ = w.link(); l.ssid == f[0] && l.addr != "" {
				return true
			}
			select {
			case err = <-res:
				res, next = nil, time.Now().Add(3*time.Second)
			default:
			}
			if res == nil && l.ssid != f[0] && time.Now().After(next) {
				res = make(chan error, 1)
				go func() {
					_, _ = w.state()
					res <- w.connect(f[0], ui.WifiArgs{SSID: f[0], Passphrase: f[1]})
				}()
			}
			return false
		}, ms); !ok {
			return fmt.Errorf("on %q with address %q, want %q with an address (connect: %v)", l.ssid, l.addr, f[0], err)
		}
		// As wifi.connect does.
		r.C.netMoved()
		fmt.Fprintf(r.Out, "PHONE-WIFI: on %q at %s\n", l.ssid, l.addr)
	case "wifi-forget":
		if r.C.wifi == nil {
			return errors.New("wifi-forget: no Wi-Fi")
		}
		return r.C.wifi.forget(strings.TrimSpace(arg))
	case "wifi-eap-ap":
		f := strings.Fields(arg)
		if len(f) != 4 {
			return fmt.Errorf("wifi-eap-ap <ssid> <user> <password> <domain>")
		}
		switch w := r.C.wifi.(type) {
		case *simWifi:
			w.addEnterprise(f[0], f[1], f[2], f[3])
		case *iwdRadio:
			if err := w.startEAP(eapTestDir, f[0], f[1], f[2], f[3]); err != nil {
				return err
			}
		default:
			return fmt.Errorf("no Wi-Fi")
		}
	case "hotspot-client", "hotspot-client-off", "hotspot-ping", "hotspot-share", "wifi-client", "wifi-client-off",
		"hotspot-traffic", "hotspot-seen", "hotspot-usage", "hotspot-pass", "hotspot-report":
		return r.hotspotStep(cmd, arg)
	case "switch-send", "files":
		return r.switchStep(cmd, arg)
	case "scan-code", "scan-tries", "scan-report":
		return r.scanStep(cmd, arg)
	case "clock-at", "clock-reset", "calendar-import", "calendar-undo", "scheduled", "notified":
		return r.calendarStep(cmd, arg)
	case "radio":
		rest, ms := lastNum(arg, 8000)
		f := strings.Fields(rest)
		if len(f) != 2 || (f[1] != "on" && f[1] != "off") {
			return fmt.Errorf("usage: radio wifi|bt|bt-search|cell on|off [ms]")
		}
		want := f[1] == "on"
		on := func() (bool, error) {
			switch {
			case f[0] == "wifi" && r.C.wifi != nil:
				st, err := r.C.wifi.state()
				return st.Enabled, err
			case f[0] == "bt" && r.C.bt != nil:
				st, err := r.C.bt.state()
				return st.Enabled, err
			case f[0] == "bt-search" && r.C.bt != nil:
				st, err := r.C.bt.state()
				return st.Discovering, err
			case f[0] == "cell" && r.C.tel != nil:
				return r.C.tel.Radio, nil
			}
			return false, fmt.Errorf("no %s", f[0])
		}
		var now bool
		var err error
		if _, ok := r.waitFor(func(string) bool { now, err = on(); return err == nil && now == want }, ms); !ok {
			return fmt.Errorf("%s is on: %v (%v), want %v", f[0], now, err, want)
		}
	case "data-cut":
		rest, ms := lastNum(arg, 5000)
		want := strings.TrimSpace(rest)
		if r.C.traffic == nil {
			return fmt.Errorf("no firewall")
		}
		var got []string
		if _, ok := r.waitFor(func(string) bool {
			got, _ = r.C.traffic.fw.Cut(cutMobile)
			return (want == "none" && len(got) == 0) || (len(got) == 1 && got[0] == want)
		}, ms); !ok {
			return fmt.Errorf("the firewall closes %v, want %s", got, want)
		}
	case "vpn-file", "vpn", "vpn-peer", "vpn-check", "vpn-drop", "vpn-net", "vpn-route", "vpn-lock":
		return r.vpnStep(cmd, arg)
	case "cell-veth", "cell-link":
		return r.cellStep(cmd, arg)
	case "carddav-server", "carddav-book", "carddav-card", "carddav-has", "carddav-net", "carddav-contact", "carddav-group", "caldav-event", "caldav-delete", "caldav-has",
		"caldav-schedule", "caldav-invite", "caldav-reply", "caldav-sent", "caldav-inbox", "account-code", "account-device", "account-revoke":
		return r.accountStep(cmd, arg)
	case "parent-server", "parent-bind", "parent-policy", "parent-report", "parent-locate", "parent-unbind", "parent-bound",
		"parent-asked", "parent-answer", "parent-reset":
		return r.familyStep(cmd, arg)
	case "bt-push", "bt-pull":
		name, text, _ := strings.Cut(strings.TrimSpace(arg), " ")
		if cmd == "bt-push" && (name == "" || text == "") {
			return fmt.Errorf("usage: bt-push <name> <text>")
		}
		return r.C.peerRun(cmd == "bt-pull", name, strings.ReplaceAll(text, `\n`, "\r\n"))
	case "file-put":
		name, text, _ := strings.Cut(strings.TrimSpace(arg), " ")
		clean, err := cleanFileName(name)
		if err != nil || text == "" {
			return fmt.Errorf("usage: file-put <name> <text>")
		}
		f, err := r.C.files.add(clean, []byte(strings.ReplaceAll(text, `\n`, "\r\n")), r.C.now())
		if err == nil {
			r.C.log.Printf("selftest: file %s (%s) put into Files", f.ID, f.Name)
		}
		return err
	case "photo-put":
		name, size, _ := strings.Cut(strings.TrimSpace(arg), " ")
		ws, hs, _ := strings.Cut(size, "x")
		w, werr := strconv.Atoi(ws)
		h, herr := strconv.Atoi(hs)
		if name == "" || werr != nil || herr != nil || w <= 0 || h <= 0 || w > maxPhotoSide || h > maxPhotoSide {
			return fmt.Errorf("usage: photo-put <name> <w>x<h> (at most %d a side)", maxPhotoSide)
		}
		var b bytes.Buffer
		if err := jpeg.Encode(&b, samplePhoto("gen:"+name, w, h), &jpeg.Options{Quality: 90}); err != nil {
			return err
		}
		it, err := r.C.media.add(b.Bytes(), name, r.C.now().AddDate(0, 0, -30))
		if err == nil {
			r.C.log.Printf("selftest: photo %s (%s, %dx%d, %d bytes) put into the album", it.ID, name, w, h, it.Size)
		}
		return err
	case "photo-drop":
		name := strings.TrimSpace(arg)
		for _, it := range append([]*mediaItem(nil), r.C.media.items...) {
			if it.Name == name {
				r.C.media.remove(it)
			}
		}
	case "bt-peer-result":
		rest, ms := lastNum(arg, 15000)
		want, text, _ := strings.Cut(strings.TrimSpace(rest), " ")
		var res *peerResult
		if _, ok := r.waitFor(func(string) bool { res = r.C.peerDone(); return res != nil }, ms); !ok {
			return fmt.Errorf("the device's transfer has not ended")
		}
		var refused obex.ResponseError
		switch {
		case want == "ok" && res.err != nil:
			return fmt.Errorf("the device's transfer failed: %v", res.err)
		case want == "refused" && !errors.As(res.err, &refused):
			return fmt.Errorf("the device's transfer was not refused: %v", res.err)
		case text != "" && !strings.Contains(res.book, text):
			return fmt.Errorf("the phone book lacks %q:\n%s", text, res.book)
		}
	case "bt-inquiry":
		rest, ms := lastNum(arg, 25000)
		want := strings.TrimSpace(rest)
		if want != "yes" && want != "no" {
			return fmt.Errorf("usage: bt-inquiry yes|no [ms]")
		}
		return r.btInquiry(want == "yes", ms)
	case "bt-peer-got":
		rest, ms := lastNum(arg, 10000)
		name, text, _ := strings.Cut(strings.TrimSpace(rest), " ")
		var got []byte
		if _, ok := r.waitFor(func(string) bool { got = r.C.peerGot(name); return got != nil }, ms); !ok || !strings.Contains(string(got), text) {
			return fmt.Errorf("the device got %q as %s, want %q in it", got, name, text)
		}
	case "bt-peer-code":
		code := strings.TrimSpace(arg)
		if code == "" || strings.ContainsAny(code, " \t") {
			return fmt.Errorf("usage: bt-peer-code <code>|none")
		}
		if _, ok := r.C.bt.(obexPeer); !ok {
			return fmt.Errorf("no Bluetooth")
		}
		if code == "none" {
			code = ""
		}
		r.C.radios.obex.setCode(code)
	case "bt-peer":
		name := strings.TrimSpace(arg)
		switch b := r.C.bt.(type) {
		case *simBT:
			b.addPeer(name, false)
		case *bluezRadio:
			if err := b.setPeer(name); err != nil {
				return err
			}
		default:
			return fmt.Errorf("no Bluetooth")
		}
	case "bt-oldphone":
		shape, name, _ := strings.Cut(strings.TrimSpace(arg), " ")
		ph, err := btphone.New(shape)
		if err != nil || strings.TrimSpace(name) == "" {
			return fmt.Errorf("usage: bt-oldphone android|iphone <name> (%v)", err)
		}
		switch b := r.C.bt.(type) {
		case *simBT:
			r.C.radios.obex.asks(ph)
			b.addOldPhone(strings.TrimSpace(name), ph)
		case *bluezRadio:
			if err := b.setOldPhone(strings.TrimSpace(name), ph); err != nil {
				return err
			}
		default:
			return fmt.Errorf("no Bluetooth")
		}
	case "bt-oldphone-allow":
		a := strings.TrimSpace(arg)
		if a != "yes" && a != "no" {
			return fmt.Errorf("usage: bt-oldphone-allow yes|no")
		}
		return r.C.radios.obex.answer(a == "yes")
	case "lowmem-kill":
		if err := r.C.EndInBackground(strings.TrimSpace(arg)); err != nil {
			return err
		}
	case "ended-itself":
		// ended-itself <app-id> [ms]: the app the Shell last asked to end
		// has exited by itself, not killed once its grace was up.
		id, ms := lastNum(strings.TrimSpace(arg), 8000)
		id = strings.TrimSpace(id)
		if _, ok := r.waitFor(func(string) bool { ended, _ := r.C.EndedItself(id); return ended }, ms); !ok {
			return fmt.Errorf("%s was not asked to end, or has not exited", id)
		}
		if _, itself := r.C.EndedItself(id); !itself {
			return fmt.Errorf("%s did not end by itself: killed once its grace was up", id)
		}
	case "appterm":
		id, ms := lastNum(strings.TrimSpace(arg), int(stopGrace/time.Millisecond))
		id = strings.TrimSpace(id)
		last := func() CrashRecord {
			if cs := r.C.Crashes(); len(cs) > 0 {
				return cs[len(cs)-1]
			}
			return CrashRecord{}
		}
		before, t0 := last(), time.Now()
		if err := r.C.TermApp(id); err != nil {
			return err
		}
		ended := func(string) bool { a := r.C.appByID(id); return a == nil || !a.running }
		if _, ok := r.waitFor(ended, ms); !ok {
			return fmt.Errorf("%s still running %v after SIGTERM", id, time.Since(t0).Round(time.Millisecond))
		}
		fmt.Fprintf(r.Out, "PHONE-APP-TERM: %s: ended %d ms after SIGTERM\n", id, time.Since(t0).Milliseconds())
		if l := last(); l.App == id && !l.Time.Equal(before.Time) {
			return fmt.Errorf("%s ended on SIGTERM as a crash: %s", id, l.Reason)
		}
	case "saved", "revived", "revoked":
		id, ms := lastNum(arg, 8000)
		id = strings.TrimSpace(id)
		check := map[string]func(string) bool{"saved": r.C.HasSavedState, "revived": r.C.Revived, "revoked": r.C.Revoked}[cmd]
		if _, ok := r.waitFor(func(string) bool { return check(id) }, ms); !ok {
			return fmt.Errorf("%s: not %s", id, cmd)
		}
	case "appstate":
		rest, ms := lastNum(arg, 8000)
		f := strings.Fields(rest)
		if len(f) != 2 {
			return fmt.Errorf("usage: appstate <app-id> <frozen|running|stopped> [ms]")
		}
		if _, ok := r.waitFor(func(string) bool { return r.C.ProcState(f[0]) == f[1] }, ms); !ok {
			return fmt.Errorf("%s is %s, want %s", f[0], r.C.ProcState(f[0]), f[1])
		}
	case "snap":
		return r.snapshot(arg)
	default:
		return fmt.Errorf("unknown step %q", cmd)
	}
	return nil
}
