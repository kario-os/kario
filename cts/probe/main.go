// The compatibility probe (com.phone.ctsprobe) is installed only for
// phone-shell --cts. It checks what no system app exercises through its
// UI: a capability without its permission must be refused, and an app with
// the phone permission can answer and control a call itself.
package main

import (
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/kario-os/kario/keystore"
	"github.com/kario-os/kario/sandbox"
	"github.com/kario-os/kario/sdk"
	"github.com/kario-os/kario/ui"
)

type check struct {
	name string
	err  error
}

func main() {
	app, err := sdk.New()
	if err != nil {
		log.Fatal(err)
	}
	var results []check
	status := tr.T("Press OK to start the checks")
	screen := func() ui.Screen {
		s := ui.Screen{ID: "main", Title: tr.T("Compatibility test"), Focus: "run", Body: []ui.Node{
			sdk.Button("run", tr.T("Start the checks"), "run"),
			sdk.Button("hog", tr.T("Memory limit check"), "hog"),
			sdk.Button("hang", tr.T("Not responding check"), "hang"),
			sdk.Button("crash", tr.T("Crash record check"), "crash"),
			sdk.Button("fake", tr.T("Fake system screen check"), "fake"),
			sdk.Button("save", tr.T("Save image check"), "save"),
			sdk.Button("file", tr.T("File check"), "file"),
			sdk.Button("sharelink", tr.T("Share check"), "sharelink"),
			sdk.Styled(ui.StyleAccent, status),
		}}
		for _, r := range results {
			if r.err == nil {
				s.Body = append(s.Body, sdk.Styled(ui.StyleOK, "✓ "+r.name))
			} else {
				s.Body = append(s.Body, sdk.Styled(ui.StyleWarn, tr.T("✗ %s: %v", r.name, r.err)))
			}
		}
		return s
	}
	report := func(name string, err error) {
		if err != nil {
			log.Printf("check failed: %s: %v", name, err)
		}
		app.Post(func() { results = append(results, check{name, err}); app.Update(screen()) })
	}
	refused := func(capName string, args any) error {
		err := app.Call(capName, args, nil)
		switch {
		case err == nil:
			return errors.New(tr.T("not refused"))
		case !strings.Contains(err.Error(), "permission"):
			return errors.New(tr.T("refused for the wrong reason: %v", err))
		}
		return nil
	}
	run := func() {
		for _, c := range []struct {
			name, cap string
			args      any
		}{
			{"Cannot send SMS without the sms permission", ui.CapSMSSend, ui.SMSSendArgs{To: "10086", Text: "cts"}},
			{"Cannot read message conversations without the sms permission", ui.CapSMSThreads, nil},
			{"Cannot read emergency alerts without the sms permission", ui.CapSMSAlerts, nil},
			{"Cannot delete messages without the sms permission", ui.CapSMSDelete, ui.SMSDeleteArgs{Peer: "10086"}},
			{"Cannot block numbers without the sms permission", ui.CapSMSBlock, ui.SMSBlockArgs{Number: "10086", On: true}},
			{"Cannot see blocked numbers without the sms permission", ui.CapSMSBlocked, nil},
			{"Cannot list all grants without the system permission", ui.CapPermList, nil},
			{"Cannot read contacts without the contacts permission", ui.CapContacts, nil},
			{"Cannot read system settings without the system permission", ui.CapSettingsGet, nil},
			{"Cannot record audio without the microphone permission", ui.CapAudioRecord, ui.AudioOpenArgs{Name: "cts"}},
			{"Cannot get the location without the location permission", ui.CapLocationGet, nil},
			{"Cannot manage packages without the install permission", ui.CapPkgList, nil},
			{"Cannot install packages without the install permission", ui.CapPkgInstall, ui.PkgArgs{File: "x.phoneapp"}},
			{"Cannot grant itself permissions without the system permission", ui.CapPermSet, ui.PermSetArgs{App: "com.phone.ctsprobe", Perm: ui.PermCamera, State: "granted"}},
			{"Cannot change system settings without the system permission", ui.CapSettingsSet, ui.SettingArgs{Key: ui.SettingDeveloperMode, Value: 1}},
			{"Cannot see others' photos in the album without the photos permission", ui.CapMediaList, ui.MediaArgs{Kind: ui.MediaImage}},
			{"Cannot see nearby Wi-Fi without the system permission", ui.CapWifiState, nil},
			{"Cannot turn Wi-Fi on or off without the system permission", ui.CapWifiSet, ui.WifiArgs{}},
			{"Cannot join Wi-Fi without the system permission", ui.CapWifiConnect, ui.WifiArgs{SSID: "cts"}},
			{"Cannot forget networks without the system permission", ui.CapWifiForget, ui.WifiArgs{SSID: "cts"}},
			{"Cannot see Bluetooth devices without the system permission", ui.CapBTState, nil},
			{"Cannot turn Bluetooth on or off without the system permission", ui.CapBTSet, ui.BTArgs{}},
			{"Cannot pair Bluetooth devices without the system permission", ui.CapBTPair, ui.BTArgs{Address: "00:00:00:00:00:00"}},
			{"Cannot unpair Bluetooth devices without the system permission", ui.CapBTForget, ui.BTArgs{Address: "00:00:00:00:00:00"}},
			{"Cannot send files over Bluetooth without the system permission", ui.CapBTSend, ui.BTArgs{Address: "00:00:00:00:00:00"}},
			{"Cannot see airplane mode and mobile data without the system permission", ui.CapRadioState, nil},
			{"Cannot switch airplane mode or mobile data without the system permission", ui.CapRadioSet, ui.RadioArgs{Airplane: new(bool)}},
			{"Cannot answer NFC readers without the nfc permission", ui.CapNFCReply, ui.NFCReplyArgs{Session: "cts", APDU: "9000"}},
			{"Cannot see the card pack without the system permission", ui.CapNFCState, nil},
			{"Cannot change the card pack without the system permission", ui.CapNFCSet, ui.NFCSetArgs{Key: ui.NFCSetPresent, Card: "door:1"}},
			{"Cannot read NFC cards without the nfc_reader permission", ui.CapNFCRead, ui.NFCReadArgs{Seconds: 1}},
			{"Cannot send commands to an NFC card without the nfc_reader permission", ui.CapNFCTransceive, ui.NFCTransceiveArgs{Session: "cts", APDU: "00A4040000"}},
		} {
			report(tr.T(c.name), refused(c.cap, c.args))
		}
		jobs := func() error {
			if app.Call(ui.CapJobSchedule, ui.JobArgs{ID: "cts", EveryMin: 5}, nil) == nil {
				return errors.New(tr.T("a job every 5 minutes was not refused"))
			}
			if err := app.Call(ui.CapJobSchedule, ui.JobArgs{ID: "cts", EveryMin: 15}, nil); err != nil {
				return err
			}
			return app.Call(ui.CapJobCancel, ui.JobArgs{ID: "cts"}, nil)
		}
		report(tr.T("Background jobs: at least 15 minutes apart, can be scheduled and cancelled"), jobs())
		// An app reaches only its own web nodes and video players.
		owned := func() error {
			if app.Call(ui.CapWebControl, ui.WebControlArgs{Screen: "root", Node: "page", Op: "reload"}, nil) == nil {
				return errors.New(tr.T("controlling a web node that does not exist was not refused"))
			}
			if app.Call(ui.CapVideoControl, ui.VideoArgs{Player: "play1", Op: "state"}, nil) == nil {
				return errors.New(tr.T("controlling another app's player was not refused"))
			}
			return nil
		}
		report(tr.T("Web nodes and video players obey only their own app"), owned())
		gmSign := func() error {
			var p ui.KeyProof
			if err := app.Call(ui.CapKeySign, ui.SignArgs{Key: "cts-gm", Alg: keystore.AlgSM2, Payload: "CTS|1"}, &p); err != nil {
				return err
			}
			root, err := keystore.LoadRootPub(os.Getenv(ui.EnvTrustRoot))
			if err != nil {
				return err
			}
			if err := keystore.VerifySign(root, &p, "com.phone.ctsprobe", "cts-gm", "CTS|1"); err != nil {
				return err
			}
			var att ui.Attestation
			if err := app.Call(ui.CapAttest, ui.AttestArgs{Nonce: "cts-sm2", Alg: keystore.AlgSM2}, &att); err != nil {
				return err
			}
			return keystore.VerifyAttestation(root, &att, "com.phone.ctsprobe", "cts-sm2")
		}
		report(tr.T("Chinese cryptography: SM2 app key signatures (SM3 digests) and SM2 device attestation, with an all-SM2 certificate chain, verify"), gmSign())
		// The sandbox itself, from inside: a device maker may not weaken it.
		// Checks for layers the device does not run are skipped (a developer
		// machine has no per-app uid).
		applied := sandbox.ParseApplied(os.Getenv(sandbox.Env))
		verdict := map[bool]string{true: tr.T("denied"), false: tr.T("allowed")}
		for _, ch := range sandbox.SelfChecks(app.DataDir) {
			if ch.Needs != "" && !applied[ch.Needs] {
				continue
			}
			var err error
			if denied := ch.Try() != nil; denied != ch.WantDenied {
				err = errors.New(tr.T("expected %s, got %s", verdict[ch.WantDenied], verdict[denied]))
			}
			report(tr.T("Sandbox: %s", ch.Name), err)
		}
		app.Post(func() { status = tr.T("Waiting for a call…"); app.Update(screen()) })
		var call *ui.TelCall
		for deadline := time.Now().Add(20 * time.Second); call == nil && time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
			var calls []ui.TelCall
			_ = app.Call(ui.CapTelCalls, nil, &calls)
			for _, c := range calls {
				if c.State == "incoming" {
					call = &c
				}
			}
		}
		if call == nil {
			report(tr.T("A call comes in"), errors.New(tr.T("no call within 20 seconds")))
			app.Post(func() { status = tr.T("Compatibility check: some checks failed"); app.Update(screen()) })
			return
		}
		state := func() string {
			var calls []ui.TelCall
			_ = app.Call(ui.CapTelCalls, nil, &calls)
			for _, c := range calls {
				if c.ID == call.ID {
					return fmt.Sprintf("%s muted=%v", c.State, c.Muted)
				}
			}
			return "gone"
		}
		// want: the call's state afterwards ("gone" once it left the list).
		step := func(name, capName string, args any, want ...string) {
			err := app.Call(capName, args, nil)
			reached := func() bool {
				got := state()
				for _, w := range want {
					if strings.HasPrefix(got, w) {
						return true
					}
				}
				return false
			}
			for i := 0; err == nil && i < 25 && !reached(); i++ {
				time.Sleep(100 * time.Millisecond)
			}
			if err == nil && !reached() {
				err = errors.New(tr.T("the call is %s, expected %s", state(), strings.Join(want, tr.T(" or "))))
			}
			report(name, err)
		}
		id := call.ID
		step(tr.T("The app answers the call (tel.answer)"), ui.CapTelAnswer, ui.CallIDArgs{CallID: id}, "active")
		step(tr.T("Mute (tel.mute)"), ui.CapTelMute, ui.CallIDArgs{CallID: id, On: true}, "active muted=true")
		step(tr.T("Hold (tel.hold)"), ui.CapTelHold, ui.CallIDArgs{CallID: id, On: true}, "held")
		step(tr.T("Resume the call"), ui.CapTelHold, ui.CallIDArgs{CallID: id, On: false}, "active")
		step(tr.T("Hang up (tel.hangup)"), ui.CapTelHangup, ui.CallIDArgs{CallID: id}, "ended", "gone")
		app.Post(func() {
			status = tr.T("Compatibility check: all passed")
			for _, r := range results {
				if r.err != nil {
					status = tr.T("Compatibility check: some checks failed")
				}
			}
			app.Update(screen())
		})
	}
	app.OnStart = func(string) { app.Reset(screen()) }
	app.OnAction = func(e sdk.ActionEvent) {
		if e.Action == "run" {
			results, status = nil, tr.T("Checking…")
			app.Update(screen())
			go run()
		}
		if e.Action == "fake" {
			// What a phishing app would show: the system's PIN dialog.
			app.Push(ui.Screen{ID: "fake", Title: tr.T("System security confirmation"), Body: []ui.Node{
				sdk.Text(tr.T("Enter your payment password to verify")),
				{Kind: ui.KindInput, ID: "pin", Placeholder: tr.T("6 digits")},
			}})
		}
		if e.Action == "file" {
			// Saving asks the user; picking hands over just the chosen file.
			content, name := tr.T("The compatibility test's file content"), tr.T("Compatibility test")+".txt"
			app.Go(ui.CapFileSave, ui.FileArgs{Name: name, Data: base64.StdEncoding.EncodeToString([]byte(content))}, nil, func(err error) {
				if err != nil {
					status = tr.T("File check: %v", err)
					app.Update(screen())
					return
				}
				go func() {
					var it ui.FileItem
					var got []byte
					_, err := app.CallFile(ui.CapFilePick, ui.FileArgs{Types: []string{"txt"}}, &it, func(f *os.File) (int64, error) {
						b, err := io.ReadAll(f)
						got = b
						return int64(len(b)), err
					})
					var again ui.FileItem
					_, openErr := app.CallFile(ui.CapFileOpen, ui.FileArgs{ID: it.ID}, &again, func(*os.File) (int64, error) { return 0, nil })
					_, otherErr := app.CallFile(ui.CapFileOpen, ui.FileArgs{ID: "f9999"}, nil, func(*os.File) (int64, error) { return 0, nil })
					app.Post(func() {
						switch {
						case err != nil:
							status = tr.T("File check: %v", err)
						case string(got) != content || it.Name != name:
							status = tr.T("File check: got a file other than the one chosen")
						case openErr != nil || again.ID != it.ID:
							status = tr.T("File check: the chosen file does not open")
						case otherErr == nil:
							status = tr.T("File check: opened a file it was not given")
						default:
							status = tr.T("File check: passed")
						}
						app.Update(screen())
					})
				}()
			})
		}
		if e.Action == "sharelink" {
			app.Go(ui.CapShareOpen, ui.ShareArgs{Kind: "link", URL: "https://example.com/cts", Text: tr.T("Compatibility test")}, nil, func(err error) {
				status = tr.T("Share check: passed")
				if err != nil {
					status = tr.T("Share check: %v", err)
				}
				app.Update(screen())
			})
		}
		if e.Action == "save" {
			// An image goes to the album only once the user agrees.
			var it ui.MediaItem
			app.Go(ui.CapMediaSave, ui.MediaSaveArgs{Src: "gen:cts", Name: tr.T("Compatibility test")}, &it, func(err error) {
				switch {
				case err != nil:
					status = tr.T("Save image check: %v", err)
				case it.ID == "":
					status = tr.T("Save image check: no photo ID came back")
				default:
					status = tr.T("Save image check: passed")
				}
				app.Update(screen())
			})
		}
		if e.Action == "crash" {
			log.Print("compatibility test: crashing on purpose")
			panic("ctsprobe: deliberate crash")
		}
		if e.Action == "hang" {
			// Blocks the loop that handles actions, as a stuck app does.
			time.Sleep(time.Duration(hangFor) * time.Second)
			status = tr.T("Not responding check: recovered")
			app.Update(screen())
		}
		if e.Action == "hog" {
			status = tr.T("Using memory…")
			app.Update(screen())
			go hog(func(mb int) {
				app.Post(func() {
					status = tr.T("Still running after using %d MB: no memory limit", mb)
					app.Update(screen())
				})
			})
		}
	}
	if err := app.Run(); err != nil {
		log.Print(err)
	}
}

// hangFor outlasts the system's ANR timeout, scaled for slow emulation
// (PHONE_TIMEOUT_SCALE).
var hangFor = 8 * max(1, atoiEnv("PHONE_TIMEOUT_SCALE"))

func atoiEnv(name string) int {
	n, _ := strconv.Atoi(os.Getenv(name))
	return n
}

// hog keeps touching new memory: on a compatible device the system ends the
// app at its memory limit long before the cap here.
func hog(done func(mb int)) {
	var keep [][]byte
	for mb := 16; mb <= 1024; mb += 16 {
		b := make([]byte, 16<<20)
		for i := 0; i < len(b); i += 4096 {
			b[i] = 1
		}
		keep = append(keep, b)
	}
	done(len(keep) * 16)
}
