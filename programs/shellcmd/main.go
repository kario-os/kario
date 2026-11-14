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
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
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
)

// services are the private instances phone-shell starts itself. Like
// systemd on the device, it restarts one that exits.
type services struct {
	mu       sync.Mutex
	procs    map[string]*exec.Cmd
	stopping bool
	wg       sync.WaitGroup
}

func (s *services) supervise(name string, start func() (*exec.Cmd, error), cmd *exec.Cmd) {
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

// ctsApps installs the compatibility probe for a --cts run.
func ctsApps(cts, prefix string) []string {
	if cts == "" {
		return nil
	}
	return []string{filepath.Join(prefix, "share", "phone", "cts")}
}

// appEnv tells apps where the simulated app servers are: the private ones
// started here, or what the system services' environment says.
func appEnv(runDir string, scale int) []string {
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
	return env
}

func startServices(binDir, runDir, stateDir string) (*services, error) {
	s := &services{procs: map[string]*exec.Cmd{}}
	socks := []string{svc.MediadSock, svc.MediadFramesSock, svc.KeystoredSock}
	// The run directory outlives a session (make run): a previous run's
	// socket or address file would pass for this run's before the service is
	// up, and apps would get the old server's port.
	for _, name := range []string{svc.MediadSock, svc.MediadFramesSock, svc.KeystoredSock, svc.TelephonydSock, svc.AudiodSock,
		svc.ImedSock, svc.PushdSock, svc.ChatsrvAddr, svc.StoresrvAddr, svc.PushgwAddr, svc.PushgwBAddr, svc.NfcdSock, svc.SmsdSock} {
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
		bin      string // the binary, when not the service's name
	}{
		{"phone-mediad", []string{"--run-dir", runDir}, false, ""},
		{"phone-keystored", []string{"--run-dir", runDir, "--state-dir", stateDir}, false, ""},
		{"phone-telephonyd", []string{"--run-dir", runDir, "--state-dir", stateDir}, true, ""},
		{"phone-audiod", []string{"--run-dir", runDir, "--sink", "auto"}, true, ""},
		{"phone-imed", []string{"--run-dir", runDir, "--state-dir", stateDir}, true, ""},
		{"phone-nfcd", []string{"--run-dir", runDir, "--state-dir", stateDir}, true, ""},
		{"phone-smsd", []string{"--run-dir", runDir, "--state-dir", stateDir}, true, ""},
		// The simulated cloud gateway runs next to the device here; pushd
		// reads the instances' URLs from the files they write.
		{"phone-pushgw", gateway("pushgw", "pushgw-b"), true, ""},
		{"phone-pushgw-b", gateway("pushgw-b", "pushgw"), true, "phone-pushgw"},
		{"phone-pushd", []string{"--run-dir", runDir, "--state-dir", stateDir, "--gateway-file", gwAddrs,
			"--gateway-cert", push.CertPath(gwTLS)}, true, ""},
		// Stands in for the Hongyan vendor's server; apps learn its URL from
		// their environment.
		{"phone-chatsrv", []string{"--listen", "127.0.0.1:0", "--addr-file", filepath.Join(runDir, svc.ChatsrvAddr), "--gateway-file", gwAddrs,
			"--gateway-cert", push.CertPath(gwTLS)}, true, ""},
		// The store backend; its root goes where the factory provisions the
		// stores a device trusts.
		{"phone-storesrv", []string{"--listen", "127.0.0.1:0", "--addr-file", filepath.Join(runDir, svc.StoresrvAddr),
			"--data", filepath.Join(filepath.Dir(runDir), "storesrv"), "--root-out", filepath.Join(stateDir, "factory", "stores", "store.pub"),
			"--admin-token-file", filepath.Join(runDir, "storesrv-admin.token")}, true, ""},
	} {
		bin := one.bin
		if bin == "" {
			bin = one.name
		}
		if _, err := os.Stat(filepath.Join(binDir, bin)); err != nil && one.optional {
			continue
		}
		switch one.name {
		case "phone-telephonyd":
			socks = append(socks, svc.TelephonydSock)
		case "phone-audiod":
			socks = append(socks, svc.AudiodSock)
		case "phone-imed":
			socks = append(socks, svc.ImedSock)
		case "phone-nfcd":
			socks = append(socks, svc.NfcdSock)
		case "phone-smsd":
			socks = append(socks, svc.SmsdSock)
		case "phone-pushd":
			socks = append(socks, svc.PushdSock)
		case "phone-chatsrv":
			socks = append(socks, svc.ChatsrvAddr)
		case "phone-storesrv":
			socks = append(socks, svc.StoresrvAddr)
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
		s.supervise(one.name, start, cmd)
	}
	for _, sock := range socks {
		p := filepath.Join(runDir, sock)
		for i := 0; ; i++ {
			if _, err := os.Stat(p); err == nil {
				break
			}
			if i > 100 {
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
	os.Unsetenv("PHONE_RESUME")
	os.Unsetenv("PHONE_RESUME_BASE")
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
	scale := flag.Int("timeout-scale", 1, "multiply selftest timeouts (slow emulation)")
	keypad := flag.String("keypad", "", "device keypad: qwerty (full keyboard) or t9 (9-key; digits type); default from --device")
	device := flag.String("device", "", "form factor: "+deviceIDs()+" (default: the image's SKU, else bar-qwerty)")
	gui := flag.String("gui", "", `pixel renderer to drive ("auto": the image's renderer slot, <prefix>/lib/phone/renderer); with --selftest it runs headless and writes PNGs to --pixel-dir`)
	only := flag.String("only", "", "with --selftest or --script: run only the cases whose name matches this regular expression")
	junit := flag.String("junit", "", "with --selftest or --script: write the cases as a JUnit XML report here")
	preview := flag.String("preview", "", `with --gui: serve the screen and take keys on a local web page at this address (e.g. 127.0.0.1:8790), the renderer headless`)
	healthFile := flag.String("health-file", "", "with --gui: written once the renderer and an app are up (the system update health check waits for it)")
	guiFallback := flag.String("gui-fallback", "auto", `renderer to switch to when the renderer keeps crashing ("auto": <prefix>/lib/phone/renderer-fallback when installed; "" for none)`)
	pixelDir := flag.String("pixel-dir", "", "where the pixel renderer writes snapshots")
	skin := flag.Bool("skin", false, "with --pixel-dir: the renderer also writes each snapshot inside a phone body with its keypad (<name>.skin.png; phone-paint)")
	printPNG := flag.String("print-png", "", "after the selftest, print these pixel snapshots (comma-separated names) as base64 on stdout")
	dump := flag.Bool("dump", false, "print the running shell's state (apps, processes, queues, settings) and exit")
	dumpProfile := flag.Bool("dump-profile", false, "print the product profile merged from the composition layers, and which layer set each key; exit")
	devApp := flag.String("dev-app", "", "the developer's app: a directory kario build wrote, loaded after the system's apps (in place of one with its id) and opened at start (kario run)")
	devRunner := flag.String("dev-runner", "", `with --dev-app, on the developer machine: the program that runs it, outside the sandbox, as a JSON array (["node", "run.mjs"]; the app's module is added)`)
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
	if err != nil {
		fail(err.Error())
	}
	dev, ok := sku.SKUDevice()
	if *device != "" || !ok {
		if dev, ok = shell.DeviceByID(firstOf(*device, "bar-qwerty")); !ok {
			fail("unknown --device " + *device + "; one of " + deviceIDs())
		}
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
	headless := *selftest || *script != ""
	runDir, stateDir := "/run/phone", "/var/lib/phone"
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
	var svcs *services
	tmpDir := ""
	if !useSystem {
		base := *simDir
		switch {
		case headless && resumeBase != "":
			// The previous Shell's services are still running there.
			defer os.RemoveAll(resumeBase)
			base, tmpDir = resumeBase, resumeBase
		case headless:
			tmp, err := os.MkdirTemp("", "phone-selftest-")
			if err != nil {
				fail(err.Error())
			}
			defer os.RemoveAll(tmp)
			base, tmpDir = tmp, tmp
		}
		runDir, stateDir = filepath.Join(base, "run"), filepath.Join(base, "state")
		for _, d := range []string{runDir, stateDir} {
			if err := os.MkdirAll(d, 0o700); err != nil {
				fail(err.Error())
			}
		}
		if os.Geteuid() == 0 {
			// Sandboxed apps run under their own uid and must traverse to their data dir.
			_ = os.Chmod(base, 0o711)
		}
		if resumeBase == "" {
			var err error
			if svcs, err = startServices(filepath.Join(*prefix, "bin"), runDir, stateDir); err != nil {
				fail(err.Error())
			}
			defer svcs.stop()
			if *ctsPath != "" {
				provision(runDir)
			}
		}
	}
	logf, _ := os.OpenFile(filepath.Join(runDir, "shell.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	// The built-in input method (used while phone-imed is down) maps the
	// same compiled dictionary.
	ime.SetDictPath(filepath.Join(*prefix, "share", "phone", "ime.dict"))
	core, err := shell.New(shell.Config{
		W: W, H: H, Keypad: *keypad,
		AppsDir:   filepath.Join(*prefix, "share", "phone", "apps"),
		AppBinDir: filepath.Join(*prefix, "lib", "phone", "bin"),
		RunDir:    runDir, StateDir: stateDir, Log: logf, AppEnv: appEnv(runDir, *scale), ExtraAppsDirs: ctsApps(*ctsPath, *prefix),
		DevApp: *devApp, DevRunner: runner,
		SKU: sku, UpdateURL: os.Getenv("PHONE_UPDATE_URL"), UpdateKey: os.Getenv("PHONE_UPDATE_KEY"),
		// Slow emulation must not look like a stuck app.
		ANRAfter: time.Duration(max(1, *scale)) * 5 * time.Second,
		FDStore:  os.Getenv("PHONE_FDSTORE") == "1" && os.Getenv("NOTIFY_SOCKET") != "",
	})
	if err != nil {
		fail(err.Error())
	}
	defer core.Shutdown()
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
		fmt.Printf("phone-shell selftest: screen %dx%d, services in %s, push over %s\n", W, H, runDir,
			map[bool]string{true: "TLCP (SM2/SM4)", false: "TLS 1.3"}[os.Getenv("PHONE_PUSH_TLCP") == "1"])
		r := &shell.Runner{C: core, SnapDir: *snapDir, Out: os.Stdout, Scale: *scale}
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
		r.ResumeFile = *resumeAt
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
		if *ctsPath != "" {
			rep := core.CompatReport(r.Cases, ok)
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
			fmt.Printf("PHONE-CTS: %s, capabilities %s exercised", rep.Result, rep.Coverage)
			if len(rep.Uncovered) > 0 {
				fmt.Printf(", never called: %s", strings.Join(rep.Uncovered, " "))
			}
			fmt.Println()
			ok = ok && rep.Result == "PASS"
		}
		if r.GUI != nil {
			fmt.Println("PHONE-GUI-FRAMES:", r.GUI.Stats())
			r.GUI.Close()
			printSnapshots(*pixelDir, *printPNG)
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
// console (the device image has no base64 tool).
func printSnapshots(dir, names string) {
	if names == "" {
		return
	}
	for _, n := range strings.Split(names, ",") {
		raw, err := os.ReadFile(filepath.Join(dir, n+".png"))
		if err != nil {
			fmt.Printf("PHONE-PNG-MISSING %s: %v\n", n, err)
			continue
		}
		enc := base64.StdEncoding.EncodeToString(raw)
		fmt.Printf("PHONE-PNG-BEGIN %s\n", n)
		for len(enc) > 0 {
			k := min(len(enc), 76)
			fmt.Println(enc[:k])
			enc = enc[k:]
		}
		fmt.Printf("PHONE-PNG-END %s\n", n)
	}
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
	// A frame is drawn a moment after something changed (events come in
	// bursts) and only then, so an idle phone, and one with its screen off,
	// does not wake up to draw; the boot animation ticks while it runs.
	var frame <-chan time.Time
	soon := func() {
		if frame == nil {
			frame = time.After(16 * time.Millisecond)
		}
	}
	soon()
	for !core.Quit() {
		select {
		case f := <-core.Events():
			f()
			soon()
		case <-frame:
			frame = nil
			if !core.ScreenOff() {
				core.Render()
				_ = w.G.Push()
			}
			if core.BootProgress() >= 0 {
				soon()
			}
		case <-w.G.Done():
			// The renderer died; the shell has every piece of state, so a new
			// one only needs the current frame.
			if err := w.Restart(); err != nil {
				fmt.Println("PHONE-GUI-EXIT:", err)
				return
			}
			fmt.Println("PHONE-GUI-RESTARTED", w.G.Info().String())
			soon()
		}
	}
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
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if msg.Type == tea.KeyCtrlC {
			m.c.Shutdown()
			return m, tea.Quit
		}
		if name, ok := keymap[msg.Type]; ok {
			m.c.Key(shell.Named(name))
		} else if msg.Type == tea.KeyRunes && msg.Alt {
			// A terminal has no long press; Alt+key stands in for it.
			m.c.Key(shell.LongRunes(string(msg.Runes)))
		} else if msg.Type == tea.KeyRunes || msg.Type == tea.KeySpace {
			m.c.Key(shell.Runes(string(msg.Runes)))
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
