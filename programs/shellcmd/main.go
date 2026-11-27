// phone-shell runs the system shell in a terminal (development, SSH, serial
// console) or headless for automated verification.
//
//	phone-shell                 interactive; uses /run/phone services if present,
//	                            otherwise starts private ones under ~/.local/state/phone-sim
//	phone-shell --selftest      run the built-in end-to-end scenario and exit 0/1
//	phone-shell --script f.txt  run a custom key script headless
package shellcmd

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"hash/adler32"
	"io"
	"log"
	"maps"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	cv "github.com/kario-os/kario/canvas"
	"github.com/kario-os/kario/ime"
	"github.com/kario-os/kario/keystore"
	"github.com/kario-os/kario/push"
	"github.com/kario-os/kario/shell"
	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/ui"
	"github.com/kario-os/kario/wipe"
)

// rebootEnv tells a phone-shell started over in place that the phone
// restarted: its services start again (after a factory reset's wipe).
const rebootEnv = "PHONE_REBOOTED"

// services are the private instances phone-shell starts itself. Like
// systemd on the device, it restarts one that exits; one that exits with
// status 0 before its socket appears has declined (nothing to serve on this
// board: phone-nfcd without an NFC controller) and stays down.
type services struct {
	mu       sync.Mutex
	procs    map[string]*exec.Cmd
	declined map[string]bool
	stopping bool
	wg       sync.WaitGroup
}

func (s *services) supervise(name, sock string, start func() (*exec.Cmd, error), cmd *exec.Cmd) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for restarts := 0; ; restarts++ {
			err := cmd.Wait()
			s.mu.Lock()
			stopping := s.stopping
			s.mu.Unlock()
			if stopping || restarts >= 20 {
				return
			}
			if err == nil && sock != "" {
				if _, serr := os.Stat(sock); serr != nil {
					log.Printf("%s exited before serving: nothing for it on this board; not restarted", name)
					s.mu.Lock()
					s.declined[name] = true
					s.mu.Unlock()
					return
				}
			}
			log.Printf("%s exited (%v); restarting", name, err)
			time.Sleep(time.Second)
			next, err := start()
			if err != nil {
				log.Printf("%s: %v", name, err)
				return
			}
			s.mu.Lock()
			s.procs[name] = next
			s.mu.Unlock()
			cmd = next
		}
	}()
}

func (s *services) hasDeclined(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.declined[name]
}

// kill ends one service abruptly (the supervisor starts it again).
func (s *services) kill(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.procs[name]
	if p == nil || p.Process == nil {
		return fmt.Errorf("no service %s", name)
	}
	return p.Process.Kill()
}

func (s *services) stop() {
	s.mu.Lock()
	s.stopping = true
	for _, p := range s.procs {
		if p.Process != nil {
			_ = p.Process.Kill()
		}
	}
	s.mu.Unlock()
	s.wg.Wait()
}

// dumpLogs prints the end of the shell, service and app logs after a failed
// selftest: on the device the run directory is gone once the image powers off.
func dumpLogs(runDir string) {
	paths, _ := filepath.Glob(filepath.Join(runDir, "*.log"))
	for _, p := range paths {
		raw, err := os.ReadFile(p)
		if err != nil || len(bytes.TrimSpace(raw)) == 0 {
			continue
		}
		n := 12
		if filepath.Base(p) == "shell.log" {
			n = 60
		}
		lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
		fmt.Printf("--- %s (last %d lines)\n", filepath.Base(p), min(n, len(lines)))
		for _, l := range lines[max(0, len(lines)-n):] {
			fmt.Println("  " + l)
		}
	}
}

// stopChildren kills what an earlier image of this process started and
// nothing supervises any more: the services, once an in-place restart has
// handed the Shell over.
func stopChildren() {
	me := strconv.Itoa(os.Getpid())
	stats, _ := filepath.Glob("/proc/[0-9]*/stat")
	for _, st := range stats {
		raw, err := os.ReadFile(st)
		if err != nil {
			continue
		}
		f := strings.Fields(string(raw[bytes.LastIndexByte(raw, ')')+1:]))
		if len(f) < 2 || f[1] != me {
			continue
		}
		pid, _ := strconv.Atoi(filepath.Base(filepath.Dir(st)))
		_ = syscall.Kill(pid, syscall.SIGKILL)
		var ws syscall.WaitStatus
		_, _ = syscall.Wait4(pid, &ws, 0, nil)
	}
}

// ctsApps installs the compatibility probe for a --cts run.
func ctsApps(cts, prefix string) []string {
	if cts == "" {
		return nil
	}
	return []string{filepath.Join(prefix, "share", "phone", "cts")}
}

// appEnv tells apps where the simulated app servers are: the private ones
// started here, or what the system services' environment says; and in a
// selftest that compares snapshots, the seed of what they draw at random.
func appEnv(runDir string, scale int, compare bool) []string {
	env := []string{"PHONE_TIMEOUT_SCALE=" + strconv.Itoa(max(1, scale))}
	for v, file := range map[string]string{ui.EnvChatServer: svc.ChatsrvAddr, ui.EnvStoreServer: svc.StoresrvAddr} {
		url := os.Getenv(v)
		if raw, err := os.ReadFile(filepath.Join(runDir, file)); err == nil {
			url = strings.TrimSpace(string(raw))
		}
		if url != "" {
			env = append(env, v+"="+url)
		}
	}
	if compare {
		env = append(env, ui.EnvTestSeed+"="+strconv.Itoa(ui.TestSeed))
	}
	// A coverage build's apps write their counters where the Shell's go
	// (make coverage).
	if d := os.Getenv("GOCOVERDIR"); d != "" {
		env = append(env, "GOCOVERDIR="+d)
	}
	return env
}

// mediadArgs: in a selftest that compares snapshots every photo of the
// synthetic camera is the same.
func mediadArgs(runDir string, compare bool) []string {
	args := []string{"--run-dir", runDir, "--test-codes"}
	if compare {
		args = append(args, "--test-stills")
	}
	return args
}

// privateModem is the backend of the Shell's own telephonyd: the image's
// SKU's when the Shell runs a phone with its own services (in a chroot on
// another system, 24-sku-framework.md §6.2); the simulated baseband for
// scripts, which call the simulated network, and on the developer machine.
func privateModem(sku shell.SKU, headless bool) string {
	if headless || sku.ID == "" {
		return ""
	}
	return sku.Modem
}

// privateAudio is how the Shell's own audiod plays and sets the card up:
// the image's SKU's sound card (sku.json's audio, the board's values the
// device's phone-audiod has from its unit) where it names one: the PCM for
// media (a board whose UCM profile routes it elsewhere than card 0's first
// PCM, the 6T's speaker), its UCM set-up for media, and for calls only with
// the SKU's own modem, which carries a call's sound (a script's calls are
// the simulated baseband's, whose far end audiod plays itself); else what
// audiod finds.
func privateAudio(sku shell.SKU, headless bool) []string {
	args := privateSink(sku, headless)
	if headless {
		// The selftest's stand-ins for headsets, USB and Bluetooth devices.
		args = append(args, "--sim-outputs")
	}
	return args
}

func privateSink(sku shell.SKU, headless bool) []string {
	a := sku.Audio
	if sku.ID == "" || a.PCM == "" {
		return []string{"--sink", "auto"}
	}
	args := []string{"--sink", "alsa:" + a.PCM}
	if a.Card == "" || a.Verb == "" {
		return args
	}
	args = append(args, "--ucm-card", a.Card, "--ucm-verb", a.Verb)
	if a.Device != "" {
		args = append(args, "--ucm-device", a.Device)
	}
	if privateModem(sku, headless) == "" || a.CallVerb == "" || a.Earpiece == "" {
		return args
	}
	for _, f := range [][2]string{{"--ucm-call-verb", a.CallVerb}, {"--ucm-earpiece", a.Earpiece}, {"--ucm-earpiece-pcm", a.EarpiecePCM},
		{"--ucm-speaker", a.CallSpeaker}, {"--ucm-speaker-pcm", a.CallSpeakerPCM}, {"--ucm-mic", a.CallMic}} {
		if f[1] != "" {
			args = append(args, f[0], f[1])
		}
	}
	return args
}

func telephonydArgs(runDir, stateDir, modem string) []string {
	args := []string{"--run-dir", runDir, "--state-dir", stateDir}
	if modem != "" {
		args = append(args, "--modem", modem)
	}
	return args
}

// startServices starts the Shell's own services and waits for their
// sockets, 3 seconds each, times scale (--timeout-scale: under emulation
// with other emulators beside it a service can take longer to start).
func startServices(binDir, runDir, stateDir, modem string, audio []string, compare bool, scale int) (*services, error) {
	s := &services{procs: map[string]*exec.Cmd{}, declined: map[string]bool{}}
	type wait struct{ name, sock string }
	var waits []wait
	// The run directory outlives a session (make run): a previous run's
	// socket or address file would pass for this run's before the service is
	// up, and apps would get the old server's port.
	for _, name := range []string{svc.MediadSock, svc.MediadFramesSock, svc.KeystoredSock, svc.TelephonydSock, svc.AudiodSock,
		svc.ImedSock, svc.PushdSock, svc.ChatsrvAddr, svc.StoresrvAddr, svc.PushgwAddr, svc.PushgwBAddr, svc.NfcdSock, svc.SmsdSock, svc.DisplaydSock} {
		_ = os.Remove(filepath.Join(runDir, name))
	}
	// The simulated gateway is a cluster of two instances: they share a
	// secret and the server identity (made here, as a deployment would
	// install it), and the device and the Hongyan server know both.
	gwTLS := filepath.Join(runDir, "pushgw-tls")
	clusterKey := filepath.Join(runDir, "pushgw-cluster.key")
	if err := pushCluster(gwTLS, clusterKey); err != nil {
		return nil, err
	}
	gwAddrs := filepath.Join(runDir, svc.PushgwAddr) + "," + filepath.Join(runDir, svc.PushgwBAddr)
	gateway := func(name, peer string) []string {
		return []string{"--listen", "127.0.0.1:0", "--addr-file", filepath.Join(runDir, name+".addr"),
			"--state", filepath.Join(runDir, name+"-state.json"), "--trust-root", keystore.RootPubPath(stateDir),
			"--tls-dir", gwTLS, pushTransport(), "--cluster-key", clusterKey, "--peer-addr-file", filepath.Join(runDir, peer+".addr")}
	}
	for _, one := range []struct {
		name     string
		args     []string
		optional bool
		bin      string   // the binary, when not the service's name
		socks    []string // what it serves on, waited for before the Shell starts
	}{
		{"phone-mediad", mediadArgs(runDir, compare), false, "", []string{svc.MediadSock, svc.MediadFramesSock}},
		{"phone-keystored", []string{"--run-dir", runDir, "--state-dir", stateDir}, false, "", []string{svc.KeystoredSock}},
		{"phone-telephonyd", telephonydArgs(runDir, stateDir, modem), true, "", []string{svc.TelephonydSock}},
		{"phone-audiod", append([]string{"--run-dir", runDir}, audio...), true, "", []string{svc.AudiodSock}},
		{"phone-imed", []string{"--run-dir", runDir, "--state-dir", stateDir}, true, "", []string{svc.ImedSock}},
		{"phone-nfcd", []string{"--run-dir", runDir, "--state-dir", stateDir}, true, "", []string{svc.NfcdSock}},
		{"phone-smsd", []string{"--run-dir", runDir, "--state-dir", stateDir}, true, "", []string{svc.SmsdSock}},
		// The simulated 60/120 Hz panel, also in the emulator's selftest:
		// the phone's own one is systemd's instance (08-display-adaptive-refresh.md).
		{"phone-displayd", []string{"--run-dir", runDir, "--panel", "sim"}, true, "", []string{svc.DisplaydSock}},
		// The simulated cloud gateway runs next to the device here; pushd
		// reads the instances' URLs from the files they write.
		{"phone-pushgw", gateway("pushgw", "pushgw-b"), true, "", nil},
		{"phone-pushgw-b", gateway("pushgw-b", "pushgw"), true, "phone-pushgw", nil},
		{"phone-pushd", []string{"--run-dir", runDir, "--state-dir", stateDir, "--gateway-file", gwAddrs,
			"--gateway-cert", push.CertPath(gwTLS)}, true, "", []string{svc.PushdSock}},
		// Stands in for the Hongyan vendor's server; apps learn its URL from
		// their environment.
		{"phone-chatsrv", []string{"--listen", "127.0.0.1:0", "--addr-file", filepath.Join(runDir, svc.ChatsrvAddr), "--gateway-file", gwAddrs,
			"--gateway-cert", push.CertPath(gwTLS)}, true, "", []string{svc.ChatsrvAddr}},
		// The store backend; its root goes where the factory provisions the
		// stores a device trusts.
		{"phone-storesrv", []string{"--listen", "127.0.0.1:0", "--addr-file", filepath.Join(runDir, svc.StoresrvAddr),
			"--data", filepath.Join(filepath.Dir(runDir), "storesrv"), "--root-out", filepath.Join(stateDir, "factory", "stores", "store.pub"),
			"--admin-token-file", filepath.Join(runDir, "storesrv-admin.token")}, true, "", []string{svc.StoresrvAddr}},
	} {
		bin := one.bin
		if bin == "" {
			bin = one.name
		}
		if _, err := os.Stat(filepath.Join(binDir, bin)); err != nil && one.optional {
			continue
		}
		sock := ""
		for i, k := range one.socks {
			if i == 0 {
				sock = filepath.Join(runDir, k)
			}
			waits = append(waits, wait{one.name, k})
		}
		logf, err := os.OpenFile(filepath.Join(runDir, one.name+".log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return nil, err
		}
		start := func() (*exec.Cmd, error) {
			cmd := exec.Command(filepath.Join(binDir, bin), one.args...)
			cmd.Stdout, cmd.Stderr = logf, logf
			cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
			return cmd, shell.StartProcess(cmd)
		}
		cmd, err := start()
		if err != nil {
			s.stop()
			return nil, fmt.Errorf("%s: %w", one.name, err)
		}
		s.procs[one.name] = cmd
		s.supervise(one.name, sock, start, cmd)
	}
	for _, w := range waits {
		p := filepath.Join(runDir, w.sock)
		for i := 0; ; i++ {
			if _, err := os.Stat(p); err == nil || s.hasDeclined(w.name) {
				break
			}
			if i > 100*max(1, scale) {
				s.stop()
				return nil, fmt.Errorf("service socket %s did not appear", p)
			}
			time.Sleep(30 * time.Millisecond)
		}
	}
	return s, nil
}

// self is the path this program was started by, to start it again: on a
// device the link to kario-system named for the Shell, which names the
// process; else the executable.
func self() string {
	if strings.Contains(os.Args[0], "/") {
		if p, err := filepath.Abs(os.Args[0]); err == nil {
			return p
		}
	}
	if p, err := exec.LookPath(os.Args[0]); err == nil {
		return p
	}
	exe, _ := os.Executable()
	return exe
}

// Main is the program: kario-system runs it by the name it was called by.
func Main() {
	if shell.KeepServerApart() {
		return
	}
	shell.ServeProcesses(shellMain)
}

func shellMain() {
	_ = shell.DisableHugePages()
	exe, _ := os.Executable()
	run := self()
	// The prctl only covers memory touched from now on: start over once with
	// the runtime's own switch so its first heap arenas skip huge pages too
	// (children inherit both).
	if g := os.Getenv("GODEBUG"); !strings.Contains(g, "disablethp=1") && run != "" {
		_ = os.Setenv("GODEBUG", strings.TrimPrefix(g+",disablethp=1", ","))
		_ = syscall.Exec(run, os.Args, os.Environ())
	}
	// Apps a previous Shell left running (systemd's fd store, or an in-place
	// restart).
	inherited := shell.InheritedApps()
	resumeFile, resumeBase := os.Getenv("PHONE_RESUME"), os.Getenv("PHONE_RESUME_BASE")
	// rebooted: the previous process restarted the phone (a factory reset):
	// its services are gone, and the new boot starts them again.
	rebooted := os.Getenv(rebootEnv) == "1"
	os.Unsetenv("PHONE_RESUME")
	os.Unsetenv("PHONE_RESUME_BASE")
	os.Unsetenv(rebootEnv)
	defPrefix := filepath.Dir(filepath.Dir(exe))
	size := flag.String("size", "40x24", "device screen in cells (WxH)")
	prefix := flag.String("prefix", defPrefix, "install prefix containing bin/, lib/phone/bin, share/phone/apps")
	mode := flag.String("services", "auto", "auto | system (/run/phone) | spawn (private instances)")
	simDir := flag.String("sim-dir", filepath.Join(os.Getenv("HOME"), ".local", "state", "phone-sim"), "base dir for private services")
	selftest := flag.Bool("selftest", false, "run the built-in end-to-end scenario headless")
	ctsPath := flag.String("cts", "", "compatibility run: the selftest, then a JSON report (cases, capabilities exercised) written here")
	script := flag.String("script", "", "run a key script headless")
	resumeAt := flag.String("resume-file", "", "with --script: where shell-crash leaves the script's state; a run that finds it continues from there")
	snapDir := flag.String("snap-dir", "", "write snapshots from snap steps here")
	baselines := flag.String("baselines", "", "with --selftest or --script: compare the snapshots (--snap-dir's text, --pixel-dir's pixels) with the baselines in this directory, a difference failing its case (shell/testdata/snapshots)")
	updateBaselines := flag.Bool("update-baselines", false, "with --baselines: record this run's snapshots as the baselines instead")
	baselineImages := flag.String("baseline-images", "", "with --baselines and --pixel-dir: keep the pixel baselines' PNGs here (not in git), to draw a difference")
	scale := flag.Int("timeout-scale", 1, "multiply selftest timeouts (slow emulation)")
	keypad := flag.String("keypad", "", "device keypad: qwerty (full keyboard) or t9 (9-key; digits type); default from --device")
	device := flag.String("device", "", "form factor: "+deviceIDs()+" (default: the image's SKU, else bar-qwerty)")
	gui := flag.String("gui", "", `pixel renderer to drive ("auto": the image's renderer slot, <prefix>/lib/phone/renderer); with --selftest it runs headless and writes PNGs to --pixel-dir`)
	only := flag.String("only", "", "with --selftest or --script: run only the cases whose name matches this regular expression")
	parts := flag.String("parts", "", `with --selftest or --cts: run only these parts of the scenario (comma-separated names of its "#part" lines), after the boot, each case at its line; the report is then of these parts`)
	parallel := flag.Bool("parallel", false, `with --selftest or --cts: run every part of the scenario at once, each in a phone-shell of its own from the boot (as go test ./shell and the emulators do); their output, snapshots and JUnit report are put together, --cts writes a report a part beside its path (<name>-<part>.json)`)
	keepGoing := flag.Bool("keep-going", false, "with --selftest, --cts or --script: after a failed case start the phone over (the Shell, its services and apps, as a restart of the phone does) and go on with the next case; the run still fails, listing every failure")
	junit := flag.String("junit", "", "with --selftest or --script: write the cases as a JUnit XML report here")
	preview := flag.String("preview", "", `with --gui: serve the screen and take keys on a local web page at this address (e.g. 127.0.0.1:8790), the renderer headless`)
	healthFile := flag.String("health-file", "", "with --gui: written once the renderer and an app are up (the system update health check waits for it)")
	stayOn := flag.Bool("stay-on", false, "bring-up: the screen never times out and the Shell never turns it off (the backlight and the panel stay on, the phone does not sleep); not kept as a setting")
	noHaptics := flag.Bool("no-haptics", false, "bring-up: the vibration motor is left alone; not kept as a setting")
	guiFallback := flag.String("gui-fallback", "auto", `renderer to switch to when the renderer keeps crashing ("auto": <prefix>/lib/phone/renderer-fallback when installed; "" for none)`)
	pixelDir := flag.String("pixel-dir", "", "where the pixel renderer writes snapshots")
	skin := flag.Bool("skin", false, "with --pixel-dir: the renderer also writes each snapshot inside a phone body with its keypad (<name>.skin.png; phone-paint)")
	printPNG := flag.String("print-png", "", "after the selftest, print these pixel snapshots (comma-separated names) on stdout, each a console file in base64 (yocto/qemu-console.py)")
	dump := flag.Bool("dump", false, "print the running shell's state (apps, processes, queues, settings) and exit")
	dumpProfile := flag.Bool("dump-profile", false, "print the product profile merged from the composition layers, and which layer set each key; exit")
	devApp := flag.String("dev-app", "", "the developer's app: a directory kario build wrote, loaded after the system's apps (in place of one with its id) and opened at start (kario run)")
	devRunner := flag.String("dev-runner", "", `with --dev-app, on the developer machine: the program that runs it, outside the sandbox, as a JSON array (["node", "run.mjs"]; the app's module is added)`)
	skuFile := flag.String("sku", "", "the hardware variant to simulate, a sku.json (kario run -sku), in place of the image's share/phone/sku.json")
	profile := flag.String("profile", "", "with --dev-app: write the developer's app's timeline (CPU, memory, pages, capability calls, input to update, ANR pings) here as Chrome Trace Event JSON, for Perfetto (kario run -profile)")
	pagesFile := flag.String("render-pages", "", "with --dev-app, --gui and --pixel-dir: snap each page of this JSON array of {name, screen} as the developer's app's, without starting it, to <name>.png (kario preview)")
	flag.Parse()
	if *dump {
		os.Exit(dumpRunning("/run/phone"))
	}
	if *dumpProfile {
		cfg := shell.Config{AppsDir: filepath.Join(*prefix, "share", "phone", "apps"), StateDir: "/var/lib/phone"}
		p, err := shell.LoadProfile(shell.ProfileDirs(cfg, false)...)
		if err != nil {
			fail(err.Error())
		}
		fmt.Print(p.Dump())
		fmt.Printf("(developer mode adds %s)\n", shell.ProfileDirs(cfg, true)[1])
		return
	}
	guiAuto := *gui == "auto"
	if guiAuto {
		*gui = filepath.Join(*prefix, shell.DefaultRenderer)
	}
	if *guiFallback == "auto" {
		*guiFallback = filepath.Join(*prefix, shell.DefaultRenderer+"-fallback")
	}
	if *ctsPath != "" {
		*selftest = true
	}
	sku, err := shell.LoadSKU(filepath.Join(*prefix, "share", "phone"))
	if *skuFile != "" {
		sku, err = shell.SimulatedSKU(*skuFile)
	}
	if err != nil {
		fail(err.Error())
	}
	dev, ok := sku.SKUDevice()
	if *device != "" || !ok {
		if dev, ok = shell.DeviceByID(firstOf(*device, "bar-qwerty")); !ok {
			fail("unknown --device " + *device + "; one of " + deviceIDs())
		}
		// Whatever form it draws, the phone's own keys, touchscreen, sound
		// switch, lid and GPU are the same.
		dev.Keymap, _ = sku.Remap()
		dev.Touch, dev.SoundSwitch, dev.Lid = sku.Touch, sku.SoundSwitch, sku.Lid
		dev = sku.GPU(dev)
	}
	if *keypad == "" {
		*keypad = dev.Keypad
		// Scripts are written for the full keyboard and switch keypads
		// themselves where a case needs the 9-key pad.
		if *keypad == "full" || *selftest || *script != "" {
			*keypad = "qwerty"
		}
	}

	var W, H int
	if _, err := fmt.Sscanf(*size, "%dx%d", &W, &H); err != nil || W < 36 || H < 20 {
		fail("--size must be at least 36x20")
	}
	headless := *selftest || *script != "" || *pagesFile != ""
	compare := headless && *baselines != ""
	runDir, stateDir, persistDir := "/run/phone", "/var/lib/phone", "/var/lib/slots"
	_, sysErr := os.Stat(filepath.Join(runDir, svc.MediadSock))
	useSystem := *mode == "system" || (*mode == "auto" && sysErr == nil && !headless)
	var runner []string
	if *devRunner != "" {
		if useSystem || *devApp == "" {
			fail("--dev-runner goes with --dev-app, in the developer machine's Shell (not with the device's services)")
		}
		if err := json.Unmarshal([]byte(*devRunner), &runner); err != nil || len(runner) == 0 {
			fail(`--dev-runner: a JSON array of the program and its arguments, e.g. ["node", "run.mjs"]`)
		}
	}
	if *devApp != "" {
		abs, err := filepath.Abs(*devApp)
		if err != nil {
			fail(err.Error())
		}
		*devApp = abs
	}
	if guiAuto && useSystem {
		if p := shell.RendererOverride(stateDir); p != "" {
			// The image's own renderer stays the fallback.
			*guiFallback, *gui = *gui, p
			fmt.Println("phone-shell: unlocked device, using the owner's renderer", p)
		}
	}
	if *parallel {
		switch {
		case !*selftest || *script != "":
			fail("--parallel goes with --selftest or --cts")
		case *parts != "":
			fail("--parallel runs every part: not with --parts")
		case *updateBaselines:
			fail("--update-baselines records the whole script in one Shell: not with --parallel")
		}
		p := parallelRun{self: run, args: withoutFlag(os.Args[1:], "parallel"), script: shell.SelftestScript,
			snapDir: *snapDir, pixelDir: *pixelDir, junit: *junit, cts: *ctsPath, printPNG: *printPNG, out: os.Stdout}
		if *ctsPath != "" {
			p.script += "\n" + shell.CTSScript
		}
		if *only != "" {
			re, err := regexp.Compile(*only)
			if err != nil {
				fail("--only: " + err.Error())
			}
			p.only = re
		}
		if !p.run() {
			os.Exit(1)
		}
		return
	}
	var svcs *services
	tmpDir := ""
	if !useSystem {
		base := *simDir
		crashBase := ""
		if headless && resumeBase == "" && *resumeAt != "" {
			crashBase = shell.ResumeBase(*resumeAt)
			if filepath.Dir(crashBase) != filepath.Clean(os.TempDir()) || !strings.HasPrefix(filepath.Base(crashBase), "phone-selftest-") {
				crashBase = ""
			}
		}
		switch {
		case headless && resumeBase != "":
			// The previous Shell's services are still running there.
			defer os.RemoveAll(resumeBase)
			base, tmpDir = resumeBase, resumeBase
		case crashBase != "":
			// A Shell systemd started again after a crash (shell-crash):
			// its services went with the old one, the state stays.
			defer os.RemoveAll(crashBase)
			base, tmpDir = crashBase, crashBase
		case headless:
			tmp, err := os.MkdirTemp("", "phone-selftest-")
			if err != nil {
				fail(err.Error())
			}
			defer os.RemoveAll(tmp)
			base, tmpDir = tmp, tmp
		}
		// persist stands for the phone's state partition, which a factory
		// reset's wipe does not reach (package wipe).
		runDir, stateDir, persistDir = filepath.Join(base, "run"), filepath.Join(base, "state"), filepath.Join(base, "persist")
		for _, d := range []string{runDir, stateDir, persistDir} {
			if err := os.MkdirAll(d, 0o700); err != nil {
				fail(err.Error())
			}
		}
		if os.Geteuid() == 0 {
			// Sandboxed apps run under their own uid and must traverse to their data dir.
			_ = os.Chmod(base, 0o711)
		}
		if resumeBase == "" || rebooted {
			// The early boot's wipe a factory reset asked for, before any
			// service starts (on a phone yocto's data-wipe does it).
			if did, err := wipe.Boot(stateDir, persistDir); err != nil {
				fail("factory reset: " + err.Error())
			} else if did {
				fmt.Println("phone-shell: factory reset: the state is erased but for the device's identity")
			}
			var err error
			if svcs, err = startServices(filepath.Join(*prefix, "bin"), runDir, stateDir, privateModem(sku, headless), privateAudio(sku, headless), compare, *scale); err != nil {
				fail(err.Error())
			}
			defer svcs.stop()
			if *ctsPath != "" {
				provision(runDir)
			}
		}
	}
	// The developer machine's restart (a factory reset): the apps and the
	// services stop and the program starts over in place, where the new
	// boot's wipe comes before the services start again; a selftest goes on
	// after the line it stood at. A phone restarts through systemd.
	var restart func()
	var reboot func()
	if !useSystem {
		reboot = func() { restart() }
	}
	logf, _ := os.OpenFile(filepath.Join(runDir, "shell.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if *gui != "" && !headless && *preview == "" {
		shell.ScreenLog = os.Stderr
	}
	// The built-in input method (used while phone-imed is down) maps the
	// same compiled dictionary.
	ime.SetDictPath(filepath.Join(*prefix, "share", "phone", "ime.dict"))
	core, err := shell.New(shell.Config{
		W: W, H: H, Keypad: *keypad,
		AppsDir:   filepath.Join(*prefix, "share", "phone", "apps"),
		AppBinDir: filepath.Join(*prefix, "lib", "phone", "bin"),
		RunDir:    runDir, StateDir: stateDir, Log: logf, AppEnv: appEnv(runDir, *scale, compare), ExtraAppsDirs: ctsApps(*ctsPath, *prefix),
		DevApp: *devApp, DevRunner: runner,
		SKU: sku, UpdateURL: os.Getenv("PHONE_UPDATE_URL"), UpdateKey: os.Getenv("PHONE_UPDATE_KEY"),
		AccountURL: os.Getenv("PHONE_ACCOUNT_SERVER"), FamilyKey: os.Getenv("PHONE_FAMILY_KEY"),
		// Slow emulation must not look like a stuck app.
		ANRAfter: time.Duration(max(1, *scale)) * 5 * time.Second,
		FDStore:  os.Getenv("PHONE_FDSTORE") == "1" && os.Getenv("NOTIFY_SOCKET") != "",
		// The scripts' cases call the demo contacts, on the device too.
		DemoContacts: headless,
		PersistDir:   persistDir,
		Reboot:       reboot,
	})
	if err != nil {
		fail(err.Error())
	}
	if *stayOn {
		core.StayOn()
	}
	if *noHaptics {
		core.NoHaptics()
	}
	var selftestRun *shell.Runner
	// startOver is the restart; a selftest goes on from resume, from the
	// state keep has (--keep-going) when it is given.
	startOver := func(resume string, keep *shell.StateCopy) {
		env := []string{rebootEnv + "=1"}
		if selftestRun != nil {
			env = append(env, "PHONE_RESUME="+resume, "PHONE_RESUME_BASE="+tmpDir)
			if selftestRun.GUI != nil {
				selftestRun.GUI.Close()
			}
		}
		fmt.Println("phone-shell: restarting the phone")
		_ = os.Stdout.Sync()
		core.Shutdown()
		if svcs != nil {
			svcs.stop()
		} else if *keepGoing {
			stopChildren()
		}
		if keep != nil {
			st, err := keep.Restore()
			if err != nil {
				fmt.Println("phone-shell: the state is as the failed case left it:", err)
			} else {
				fmt.Println("phone-shell: the state is as the failed case found it:", st)
			}
		}
		fail("restart: " + shell.ExecFresh(run, os.Args, env).Error())
	}
	restart = func() {
		resume := ""
		if selftestRun != nil {
			var err error
			if resume, err = selftestRun.SaveResume(); err != nil {
				fmt.Println("phone-shell: restart:", err)
			}
		}
		startOver(resume, nil)
	}
	defer core.Shutdown()
	if *profile != "" {
		t, err := core.StartTrace(*profile)
		if err != nil {
			fail(err.Error())
		}
		defer t.Close()
	}
	core.Adopt(inherited)
	if *devApp != "" && !headless {
		core.LaunchDev()
	}
	// phone-shell --dump asks the running shell for its state.
	_ = os.WriteFile(shell.PIDPath(runDir), []byte(strconv.Itoa(os.Getpid())+"\n"), 0o644)
	// A stop (systemd, timeout) ends the apps through the shell: with the fd
	// store the unit leaves them running (KillMode=process) for a restart.
	term := make(chan os.Signal, 1)
	signal.Notify(term, syscall.SIGTERM)
	go func() {
		<-term
		core.Shutdown()
		os.Exit(143)
	}()
	usr1 := make(chan os.Signal, 1)
	signal.Notify(usr1, syscall.SIGUSR1)
	go func() {
		for range usr1 {
			core.Post(func() {
				if err := core.WriteDump(); err != nil {
					fmt.Fprintln(os.Stderr, "phone-shell: dump:", err)
				}
			})
		}
	}()
	// kario run -watch: a new build of the developer's app is ready.
	if *devApp != "" {
		usr2 := make(chan os.Signal, 1)
		signal.Notify(usr2, syscall.SIGUSR2)
		go func() {
			for range usr2 {
				core.Post(core.ReloadDev)
			}
		}()
	}

	if *pagesFile != "" {
		status := renderPages(core, *gui, dev, *pagesFile, *pixelDir, *skin)
		core.Shutdown()
		if svcs != nil {
			svcs.stop()
		}
		if tmpDir != "" {
			os.RemoveAll(tmpDir)
		}
		os.Exit(status)
	}
	if headless {
		text := shell.SelftestScript
		if *ctsPath != "" {
			text += "\n" + shell.CTSScript
		}
		if *script != "" {
			b, err := os.ReadFile(*script)
			if err != nil {
				fail(err.Error())
			}
			text = string(b)
		}
		var ran, allParts []string
		if *parts != "" {
			if *script != "" {
				fail("--parts goes with --selftest or --cts, not --script")
			}
			// The baselines of the other parts' snapshots would go as
			// no longer in the script.
			if *updateBaselines {
				fail("--update-baselines records the whole script's snapshots: not with --parts")
			}
			var err error
			if allParts, err = shell.ScriptParts(text); err != nil {
				fail("--parts: " + err.Error())
			}
			ran = strings.Split(*parts, ",")
			if text, err = shell.SelectParts(text, ran); err != nil {
				fail("--parts: " + err.Error())
			}
		}
		fmt.Printf("phone-shell selftest: screen %dx%d, services in %s, push over %s\n", W, H, runDir,
			map[bool]string{true: "TLCP (SM2/SM4)", false: "TLS 1.3"}[os.Getenv("PHONE_PUSH_TLCP") == "1"])
		r := &shell.Runner{C: core, SnapDir: *snapDir, Out: os.Stdout, Scale: *scale, CanReboot: reboot != nil}
		selftestRun = r
		if *baselines != "" {
			b, err := shell.LoadBaselines(*baselines, dev.ID)
			if err != nil {
				fail("--baselines: " + err.Error())
			}
			b.Update, b.Images = *updateBaselines, *baselineImages
			if *gui != "" {
				b.PixelDir = *pixelDir
			}
			r.Baselines = b
		}
		if *only != "" {
			re, err := regexp.Compile(*only)
			if err != nil {
				fail("--only: " + err.Error())
			}
			r.Only = re
		}
		r.Restart = func(resume string) error {
			_ = os.Stdout.Sync()
			return core.ExecHandoff(run, os.Args, []string{"PHONE_RESUME=" + resume, "PHONE_RESUME_BASE=" + tmpDir})
		}
		if svcs != nil {
			r.KillService = svcs.kill
		}
		if *keepGoing {
			if reboot == nil {
				fail("--keep-going starts the Shell's own services over: not with --services system")
			}
			r.Keep = &shell.StateCopy{Dirs: []string{stateDir, persistDir}, Dir: filepath.Join(tmpDir, "case-start")}
			r.KeepGoing = func(resume string) error {
				dumpLogs(runDir)
				startOver(resume, r.Keep)
				return nil
			}
		}
		r.ResumeFile, r.Base = *resumeAt, tmpDir
		if resumeFile == "" && *resumeAt != "" {
			if _, err := os.Stat(*resumeAt); err == nil {
				resumeFile = *resumeAt
			}
		}
		if resumeFile != "" {
			res, err := shell.LoadResume(resumeFile)
			if err != nil {
				fail("resume: " + err.Error())
			}
			_ = os.Remove(resumeFile)
			r.Resume = res
		}
		if *gui != "" {
			args := []string{"--headless"}
			if *pixelDir != "" {
				args = append(args, "--snap-dir", *pixelDir)
			}
			if steps := os.Getenv(shell.MotionStepsEnv); steps != "" && r.Resume == nil {
				_ = os.Remove(steps)
			}
			if *skin {
				args = append(args, "--skin")
			}
			w, err := shell.WatchRenderer(core, dev, *gui, "", args...)
			if err != nil {
				fail("pixel renderer: " + err.Error())
			}
			g := w.G
			r.GUI, r.Watch = g, w
			fmt.Printf("pixel renderer: %s (%s, %s), %s %dx%d\n", g.Info(), *gui, g.Info().Proto, dev.ID, dev.Width, dev.Height)
		}
		ok := r.Run(text)
		if *junit != "" {
			if err := shell.WriteJUnit(*junit, "phone-selftest", r.Cases); err != nil {
				fmt.Println("junit:", err)
			}
		}
		if path := os.Getenv(partResultEnv); path != "" {
			if err := writePartResult(path, r); err != nil {
				fmt.Println("phone-shell:", err)
			}
		}
		if *ctsPath != "" {
			rep := core.CompatReport(r.Cases, ok)
			if ran != nil {
				rep.OfParts(ran, allParts, ok)
			}
			if err := core.SignReport(&rep); err != nil {
				fmt.Println("cts: signing the report:", err)
			}
			raw, _ := json.MarshalIndent(rep, "", "  ")
			if err := os.WriteFile(*ctsPath, raw, 0o644); err != nil {
				fmt.Println("cts:", err)
			}
			// The prototype's device root is made on the device; production
			// devices chain to the certifier's root provisioned at the factory.
			if root, err := os.ReadFile(keystore.RootPubPath(stateDir)); err == nil {
				_ = os.WriteFile(*ctsPath+".root", root, 0o644)
				fmt.Printf("PHONE-CTS-ROOT: %s\n", strings.Join(strings.Fields(string(root)), " "))
			}
			if ran != nil {
				fmt.Printf("PHONE-CTS: %s, capabilities %s exercised in the parts %s (a part's report passes only with the others')", rep.PartResult, rep.Coverage, strings.Join(ran, ","))
			} else {
				fmt.Printf("PHONE-CTS: %s, capabilities %s exercised", rep.Result, rep.Coverage)
			}
			if len(rep.Skipped) > 0 {
				fmt.Printf(", skipped with their cases: %s", strings.Join(slices.Sorted(maps.Keys(rep.Skipped)), " "))
			}
			if len(rep.Uncovered) > 0 && ran == nil {
				fmt.Printf(", never called: %s", strings.Join(rep.Uncovered, " "))
			}
			fmt.Println()
			for _, name := range slices.Sorted(maps.Keys(rep.Skipped)) {
				fmt.Printf("PHONE-CTS-SKIPPED: %s: %s\n", name, rep.Skipped[name])
			}
			ok = ok && (ran != nil || rep.Result == "PASS")
		}
		if r.GUI != nil {
			fmt.Println("PHONE-GUI-FRAMES:", r.GUI.Stats())
			r.GUI.Close()
			if steps := os.Getenv(shell.MotionStepsEnv); steps != "" {
				if rep, err := shell.MotionReport(steps); err == nil {
					fmt.Println("PHONE-GUI-MOTION:", rep)
				}
			}
			printSnapshots(os.Stdout, *pixelDir, *printPNG)
		}
		core.Shutdown()
		if svcs != nil {
			svcs.stop()
		}
		if !ok {
			dumpLogs(runDir)
			if tmpDir != "" {
				os.RemoveAll(tmpDir)
			}
			os.Exit(1)
		}
		return
	}

	if *gui != "" && *preview != "" {
		runPreview(core, *gui, dev, *preview, *skin)
		return
	}
	if *gui != "" {
		runGUI(core, *gui, *guiFallback, dev, *healthFile)
		return
	}
	p := tea.NewProgram(&model{c: core}, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fail(err.Error())
	}
}

// printSnapshots lets an emulator run hand PNGs to the host over the serial
// console (the device image has no base64 tool), each as the console file
// <name>.png in base64.
func printSnapshots(w io.Writer, dir, names string) {
	if names == "" {
		return
	}
	for _, n := range strings.Split(names, ",") {
		raw, err := os.ReadFile(filepath.Join(dir, n+".png"))
		if err != nil {
			fmt.Fprintf(w, "PHONE-PNG-MISSING %s: %v\n", n, err)
			continue
		}
		enc := base64.StdEncoding.EncodeToString(raw)
		var lines []string
		for len(enc) > 0 {
			k := min(len(enc), 76)
			lines = append(lines, enc[:k])
			enc = enc[k:]
		}
		printFile(w, n+".png", lines, true)
	}
}

// printFile writes lines to w as the console file name, in the format of
// the image's console-file (the kernel can print a line of its own inside
// any line on the console; yocto/qemu-console.py takes it out where the
// sums then hold): each line in one write with its number and Adler-32,
// then their count and the Adler-32 of them all, each with its newline;
// b64 tells the host to decode them.
func printFile(w io.Writer, name string, lines []string, b64 bool) {
	whole := adler32.New()
	for i, l := range lines {
		io.WriteString(whole, l+"\n")
		fmt.Fprintf(w, "PHONE-FILE %s %d %08x %s\n", name, i+1, adler32.Checksum([]byte(l)), l)
	}
	enc := ""
	if b64 {
		enc = " base64"
	}
	fmt.Fprintf(w, "PHONE-FILE-END %s %d %08x%s\n", name, len(lines), whole.Sum32(), enc)
}

func firstOf(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}

func deviceIDs() string {
	var ids []string
	for _, d := range shell.Devices {
		ids = append(ids, d.ID)
	}
	return strings.Join(ids, " | ")
}

// runGUI is the device mode: no terminal; the pixel renderer owns the display
// and the keys, the shell sends it a frame whenever something changed.
func runGUI(core *shell.Core, path, fallback string, dev shell.Device, health string) {
	shell.GUIStderr = os.Stderr
	w, err := shell.WatchRenderer(core, dev, path, fallback)
	if err != nil {
		fail("pixel renderer: " + err.Error())
	}
	defer func() { w.G.Close() }()
	fmt.Println("PHONE-GUI-READY", dev.ID, w.G.Info().String(), "["+strings.Join(w.G.Info().Features, " ")+"]")
	core.OnInput = func(kind, name string, added bool) {
		state := "removed"
		if added {
			state = "added"
		}
		fmt.Println("PHONE-GUI-INPUT", kind, state, name)
	}
	core.RestartBoot()
	core.CheckHealth(health, w.G.Info().String())
	core.WatchLoop()
	w.RunFrames(func(err error) {
		if err != nil {
			fmt.Println("PHONE-GUI-EXIT:", err)
			return
		}
		fmt.Println("PHONE-GUI-RESTARTED", w.G.Info().String())
	})
}

func fail(msg string) {
	fmt.Fprintln(os.Stderr, "phone-shell:", msg)
	os.Exit(2)
}

type model struct {
	c    *shell.Core
	w, h int
}

type evMsg func()

func (m *model) wait() tea.Cmd {
	return func() tea.Msg { return evMsg(<-m.c.Events()) }
}

func (m *model) Init() tea.Cmd { return m.wait() }

var keymap = map[tea.KeyType]string{
	tea.KeyUp: shell.KeyUp, tea.KeyDown: shell.KeyDown, tea.KeyLeft: shell.KeyLeft, tea.KeyRight: shell.KeyRight,
	tea.KeyEnter: shell.KeyEnter, tea.KeyEsc: shell.KeyEsc, tea.KeyTab: shell.KeyTab, tea.KeyF1: shell.KeyTab,
	tea.KeyBackspace: shell.KeyBackspace, tea.KeyF2: shell.KeyF2, tea.KeyF3: shell.KeyF3, tea.KeyF4: shell.KeyF4, tea.KeyF5: shell.KeyCall,
	tea.KeyF6: shell.KeyIME, tea.KeyCtrlAt: shell.KeyIME, tea.KeyF7: shell.KeySym,
	tea.KeyF8: shell.KeyVolDown, tea.KeyF9: shell.KeyVolUp,
	tea.KeyF10: shell.KeyPlayPause, tea.KeyF11: shell.KeyPrevTrack, tea.KeyF12: shell.KeyNextTrack,
	tea.KeyHome: shell.KeyFirst, tea.KeyEnd: shell.KeyLast, tea.KeyPgUp: shell.KeyPageUp, tea.KeyPgDown: shell.KeyPageDown,
	tea.KeyDelete: shell.KeyDelete, tea.KeyCtrlX: shell.KeyCut, tea.KeyCtrlV: shell.KeyPaste, tea.KeyCtrlA: shell.KeySelectAll,
	tea.KeyCtrlZ: shell.KeyUndo,
}

// shiftKeys select text (Ctrl+C stays the terminal's quit: Copy is in the
// options menu).
var shiftKeys = map[tea.KeyType]string{
	tea.KeyShiftLeft: shell.KeyLeft, tea.KeyShiftRight: shell.KeyRight, tea.KeyShiftUp: shell.KeyUp, tea.KeyShiftDown: shell.KeyDown,
	tea.KeyShiftHome: shell.KeyFirst, tea.KeyShiftEnd: shell.KeyLast,
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if msg.Type == tea.KeyCtrlC {
			m.c.Shutdown()
			return m, tea.Quit
		}
		if name, ok := keymap[msg.Type]; ok && msg.Alt && (name == shell.KeyVolDown || name == shell.KeyVolUp) {
			// A terminal cannot press both volume keys at once: Alt+F8 is
			// the accessibility shortcut, Alt+F9 the shortcut held.
			m.c.Press(shell.Key{Name: shell.KeyA11y, Long: name == shell.KeyVolUp})
		} else if ok {
			// Alt+Backspace is the held Backspace (a word at a time).
			m.c.Press(shell.Key{Name: name, Long: msg.Alt && name == shell.KeyBackspace})
		} else if name, ok := shiftKeys[msg.Type]; ok {
			m.c.Press(shell.Key{Name: name, Shift: true})
		} else if msg.Type == tea.KeyRunes && msg.Alt {
			// A terminal has no long press; Alt+key stands in for it.
			m.c.Press(shell.LongRunes(string(msg.Runes)))
		} else if msg.Type == tea.KeyRunes || msg.Type == tea.KeySpace {
			m.c.Press(shell.Runes(string(msg.Runes)))
		}
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
	case evMsg:
		msg()
		return m, m.wait()
	}
	return m, nil
}

// help is the key line under the phone, in the language in force; a narrow
// terminal drops what follows " /".
func help() string {
	return tr.T("Enter=OK Esc=back Tab=options F2=right soft key F3=tasks F4=home F6=Zh/En F7=symbols Alt+key=long press /commands ^C=quit")
}

func (m *model) View() string {
	dev := m.c.Render()
	if m.w < dev.W+2 || m.h < dev.H+2 {
		return dev.ANSI()
	}
	term := cv.New(m.w, m.h, cv.Style{FG: cv.RGB{120, 120, 130}, BG: cv.RGB{0, 0, 0}})
	x0, y0 := (m.w-dev.W-2)/2, max(0, (m.h-dev.H-3)/2)
	term.Box(x0, y0, dev.W+2, dev.H+2, cv.Style{FG: cv.RGB{90, 92, 105}, BG: cv.RGB{0, 0, 0}}, false)
	term.Blit(x0+1, y0+1, dev)
	if y0+dev.H+2 < m.h {
		h := help()
		if cv.StringWidth(h) > m.w {
			h = strings.SplitN(h, " /", 2)[0]
		}
		term.Text(max(0, (m.w-cv.StringWidth(h))/2), y0+dev.H+2, h, cv.Style{FG: cv.RGB{110, 112, 125}, BG: cv.RGB{0, 0, 0}}, m.w)
	}
	return term.ANSI()
}

// provision does what the factory does before a device is certified: a CA
// standing in for the certifier's (in the run directory) signs the private
// keystore's device certificate, so the compatibility report chains to the
// certifier's root rather than to a root made on the device.
func provision(runDir string) {
	ca, err := keystore.LoadCA(filepath.Join(runDir, "factory-ca"))
	if err != nil {
		fail("provisioning: " + err.Error())
	}
	sock := filepath.Join(runDir, svc.KeystoredSock)
	for i := 0; i < 100; i++ {
		if _, err := os.Stat(sock); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	id, err := keystore.Provision(ca, sock)
	if err != nil {
		fail("provisioning: " + err.Error())
	}
	fmt.Printf("PHONE-PROVISIONED: %s, certified by the factory CA %s\n", id, ca.Root)
}

// dumpRunning signals the shell running on the device and prints the state
// it writes.
func dumpRunning(runDir string) int {
	raw, err := os.ReadFile(shell.PIDPath(runDir))
	if err != nil {
		fmt.Fprintln(os.Stderr, "phone-shell: no running shell:", err)
		return 1
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
	before, _ := os.Stat(shell.DumpPath(runDir))
	if err := syscall.Kill(pid, syscall.SIGUSR1); err != nil {
		fmt.Fprintln(os.Stderr, "phone-shell:", err)
		return 1
	}
	for i := 0; i < 60; i++ {
		time.Sleep(50 * time.Millisecond)
		st, err := os.Stat(shell.DumpPath(runDir))
		if err == nil && (before == nil || st.ModTime().After(before.ModTime())) {
			out, _ := os.ReadFile(shell.DumpPath(runDir))
			os.Stdout.Write(out)
			fmt.Println()
			return 0
		}
	}
	fmt.Fprintln(os.Stderr, "phone-shell: the shell did not answer")
	return 1
}

// pushCluster makes the gateway cluster's shared secret and server identity
// (TLS or TLCP, as pushTransport picks), once.
func pushCluster(tlsDir, keyFile string) error {
	if _, err := os.Stat(keyFile); err != nil {
		k := make([]byte, 32)
		if _, err := rand.Read(k); err != nil {
			return err
		}
		if err := os.WriteFile(keyFile, []byte(base64.StdEncoding.EncodeToString(k)+"\n"), 0o600); err != nil {
			return err
		}
	}
	var err error
	if os.Getenv("PHONE_PUSH_TLCP") == "1" {
		_, err = push.GatewayTLCP(tlsDir, "127.0.0.1", "localhost")
	} else {
		_, err = push.GatewayTLS(tlsDir, "127.0.0.1", "localhost")
	}
	return err
}

// pushTransport: the simulated gateway speaks TLCP (TLS on the SM
// algorithms) when PHONE_PUSH_TLCP=1, as the device image sets, TLS 1.3
// otherwise; the device side follows the certificate it pins.
func pushTransport() string {
	return "--tlcp=" + strconv.FormatBool(os.Getenv("PHONE_PUSH_TLCP") == "1")
}
