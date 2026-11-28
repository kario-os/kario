package factory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/kario-os/kario/keystore"
	"github.com/kario-os/kario/svc"
)

// The runner's HTTP API (docs/40-testing.md §10.4). The station's part is
// served on the USB link (Port) and on the runner's socket; the factory
// screen's part only on the socket.
const (
	// Port is the runner's TCP port on the USB link in factory mode, the one
	// port netfw opens there besides DHCP.
	Port = 7171
	// Sock is the runner's socket in the run directory (0600: the Shell
	// and root's tools).
	Sock = "factory.sock"

	PathUnit    = "/v1/unit"
	PathTest    = "/v1/test"
	PathCSR     = "/v1/keystore/csr"
	PathAttest  = "/v1/keystore/attest"
	PathInstall = "/v1/keystore/install_cert"

	PathPlan       = "/v1/plan"
	PathResult     = "/v1/result"
	PathMicrophone = "/v1/microphone"
	PathReport     = "/v1/report"
)

// UnitSchema names GET /v1/unit's answer.
const UnitSchema = "kario.factory-unit/1"

// UnitInfo is GET /v1/unit: which unit this is and whether factory mode
// still holds (Provisioned turns true once the station installed its
// certificates).
type UnitInfo struct {
	Schema      string `json:"schema"`
	DeviceID    string `json:"device_id"`
	SKU         string `json:"sku"`
	Board       string `json:"board"`
	Provisioned bool   `json:"provisioned"`
}

// TestRequest is POST /v1/test's body: Mode "auto" or "full"; WaitS, how
// long a full test waits for the operator to finish the factory screen's
// checks before it reports the ones left as not done.
type TestRequest struct {
	Mode  string `json:"mode"`
	WaitS int    `json:"wait_s,omitempty"`
}

// PlanInfo is GET /v1/plan: what the factory screen runs.
type PlanInfo struct {
	DeviceID string  `json:"device_id"`
	SKU      SKU     `json:"sku"`
	Checks   []Check `json:"checks"`
	// Done: the interactive checks with a result.
	Done []CheckResult `json:"done,omitempty"`
}

// MicRequest is POST /v1/microphone's body: Loopback plays a tone through
// the speaker while recording.
type MicRequest struct {
	Loopback bool `json:"loopback,omitempty"`
}

// apiError is an error's JSON body.
type apiError struct {
	Error string `json:"error"`
}

// Server is phone-factory-test --serve: it holds the factory screen's
// results and answers the station.
type Server struct {
	Env *Env
	SKU SKU

	mu          sync.Mutex
	plan        []Check
	deviceID    string
	provisioned bool
	done        map[string]CheckResult
	last        *Report
	changed     chan struct{}
}

// NewServer is the runner for this SKU.
func NewServer(e *Env, s SKU, deviceID string) *Server {
	return &Server{Env: e, SKU: s, plan: Plan(s), deviceID: deviceID, done: map[string]CheckResult{}, changed: make(chan struct{})}
}

func (s *Server) keystoreSock() string { return filepath.Join(s.Env.RunDir, svc.KeystoredSock) }

// Station is the station's part of the API.
func (s *Server) Station() http.Handler { return s.routes(false) }

// Local is the whole API: the station's and the factory screen's.
func (s *Server) Local() http.Handler { return s.routes(true) }

func (s *Server) routes(local bool) http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET "+PathUnit, s.unit)
	m.HandleFunc("POST "+PathTest, s.test)
	m.HandleFunc("GET "+PathCSR, s.csr)
	m.HandleFunc("POST "+PathAttest, s.attest)
	m.HandleFunc("POST "+PathInstall, s.install)
	if local {
		m.HandleFunc("GET "+PathPlan, s.planInfo)
		m.HandleFunc("POST "+PathResult, s.result)
		m.HandleFunc("POST "+PathMicrophone, s.microphone)
		m.HandleFunc("GET "+PathReport, s.report)
	}
	return m
}

func reply(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, code int, err error) { reply(w, code, apiError{err.Error()}) }

func readBody(r *http.Request, v any) error {
	raw, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	if err != nil {
		return err
	}
	if len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, v)
}

// gate is the per-request check: factory mode holds only while the
// keystore says it is not provisioned.
func (s *Server) gate(w http.ResponseWriter) bool {
	id, err := s.Env.Gate()
	s.mu.Lock()
	if id != "" {
		s.deviceID = id
	}
	if errors.Is(err, ErrProvisioned) {
		s.provisioned = true
	}
	s.mu.Unlock()
	if err != nil {
		fail(w, http.StatusForbidden, err)
		return false
	}
	return true
}

func (s *Server) unit(w http.ResponseWriter, r *http.Request) {
	id, err := s.Env.Gate()
	if err != nil && !errors.Is(err, ErrProvisioned) {
		fail(w, http.StatusServiceUnavailable, err)
		return
	}
	s.mu.Lock()
	s.deviceID = id
	s.provisioned = s.provisioned || errors.Is(err, ErrProvisioned)
	info := UnitInfo{Schema: UnitSchema, DeviceID: id, SKU: s.SKU.ID, Board: s.SKU.Board, Provisioned: s.provisioned}
	s.mu.Unlock()
	reply(w, http.StatusOK, info)
}

func (s *Server) test(w http.ResponseWriter, r *http.Request) {
	var req TestRequest
	if err := readBody(r, &req); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	if req.Mode == "" {
		req.Mode = ModeFull
	}
	if req.Mode != ModeAuto && req.Mode != ModeFull {
		fail(w, http.StatusBadRequest, fmt.Errorf("mode %q: auto or full", req.Mode))
		return
	}
	rep := s.Run(r.Context(), req.Mode, time.Duration(req.WaitS)*time.Second)
	code := http.StatusOK
	if rep.Refused != "" {
		code = http.StatusForbidden
	}
	reply(w, code, rep)
}

// Run is one test of the unit: the gate, the automatic checks and, for a
// full test, the factory screen's results (waiting up to wait for them).
func (s *Server) Run(ctx context.Context, mode string, wait time.Duration) *Report {
	rep := &Report{Schema: Schema, SKU: s.SKU.ID, Board: s.SKU.Board, Mode: mode, Started: time.Now().UTC()}
	id, err := s.Env.Gate()
	rep.DeviceID = id
	if err != nil {
		rep.Refused = err.Error()
		rep.Checks = []CheckResult{}
		rep.Finish()
		return rep
	}
	if mode == ModeFull {
		s.waitDone(ctx, wait)
	}
	checks, _ := s.Env.RunAuto(ctx, s.SKU, s.plan)
	if mode == ModeFull {
		checks = append(checks, s.interactive()...)
	}
	rep.Checks = checks
	rep.DurationMS = time.Since(rep.Started).Milliseconds()
	rep.Finish()
	s.mu.Lock()
	s.deviceID = id
	s.last = rep
	s.mu.Unlock()
	return rep
}

// interactive is the factory screen's results, a check not done yet as
// skipped.
func (s *Server) interactive() []CheckResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []CheckResult
	for _, c := range Only(s.plan, Interactive) {
		switch r, ok := s.done[c.ID]; {
		case c.Skip != "":
			out = append(out, skipped(c))
		case ok:
			r.Kind, r.Required = c.Kind, c.Required
			out = append(out, r)
		default:
			out = append(out, CheckResult{ID: c.ID, Kind: c.Kind, Required: c.Required, Result: Skip, Reason: "not done on the factory screen"})
		}
	}
	return out
}

// left is how many applicable interactive checks have no result yet.
func (s *Server) left() int {
	n := 0
	for _, c := range Only(s.plan, Interactive) {
		if _, ok := s.done[c.ID]; c.Skip == "" && !ok {
			n++
		}
	}
	return n
}

func (s *Server) waitDone(ctx context.Context, wait time.Duration) {
	end := time.After(wait)
	for {
		s.mu.Lock()
		left, ch := s.left(), s.changed
		s.mu.Unlock()
		if left == 0 {
			return
		}
		select {
		case <-ch:
		case <-end:
			return
		case <-ctx.Done():
			return
		}
	}
}

func (s *Server) csr(w http.ResponseWriter, r *http.Request) {
	if !s.gate(w) {
		return
	}
	csr, err := keystore.ReadCSR(s.keystoreSock())
	if err != nil {
		fail(w, http.StatusBadGateway, err)
		return
	}
	reply(w, http.StatusOK, csr)
}

func (s *Server) attest(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Nonce string `json:"nonce"`
	}
	if err := readBody(r, &req); err != nil || req.Nonce == "" || len(req.Nonce) > 256 {
		fail(w, http.StatusBadRequest, errors.New("a nonce of 1 to 256 bytes"))
		return
	}
	if !s.gate(w) {
		return
	}
	att, err := keystore.AttestFor(s.keystoreSock(), req.Nonce)
	if err != nil {
		fail(w, http.StatusBadGateway, err)
		return
	}
	reply(w, http.StatusOK, att)
}

func (s *Server) install(w http.ResponseWriter, r *http.Request) {
	var certs keystore.DeviceCerts
	if err := readBody(r, &certs); err != nil || certs.Cert == "" || certs.Root == "" {
		fail(w, http.StatusBadRequest, errors.New("cert, cert_sm2 and root"))
		return
	}
	if !s.gate(w) {
		return
	}
	if err := keystore.InstallCerts(s.keystoreSock(), certs); err != nil {
		fail(w, http.StatusBadGateway, err)
		return
	}
	s.mu.Lock()
	s.provisioned = true
	s.mu.Unlock()
	reply(w, http.StatusOK, struct{}{})
}

func (s *Server) planInfo(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	info := PlanInfo{DeviceID: s.deviceID, SKU: s.SKU, Checks: s.plan}
	for _, c := range s.plan {
		if d, ok := s.done[c.ID]; ok {
			info.Done = append(info.Done, d)
		}
	}
	s.mu.Unlock()
	reply(w, http.StatusOK, info)
}

func (s *Server) result(w http.ResponseWriter, r *http.Request) {
	var res CheckResult
	if err := readBody(r, &res); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	i := slices.IndexFunc(s.plan, func(c Check) bool { return c.ID == res.ID && c.Kind == Interactive })
	if i < 0 || res.Result != Pass && res.Result != Fail && res.Result != Skip {
		fail(w, http.StatusBadRequest, fmt.Errorf("no interactive check %q, or result %q not pass, fail or skip", res.ID, res.Result))
		return
	}
	res.Kind, res.Required = Interactive, s.plan[i].Required
	s.mu.Lock()
	s.done[res.ID] = res
	close(s.changed)
	s.changed = make(chan struct{})
	s.mu.Unlock()
	reply(w, http.StatusOK, struct{}{})
}

func (s *Server) microphone(w http.ResponseWriter, r *http.Request) {
	var req MicRequest
	if err := readBody(r, &req); err != nil {
		fail(w, http.StatusBadRequest, err)
		return
	}
	v, err := s.Env.Microphone(r.Context(), s.SKU, req.Loopback)
	res := CheckResult{ID: "microphone", Kind: Interactive, Result: Pass, Value: v}
	if err != nil {
		res.Result, res.Reason = Fail, err.Error()
	}
	reply(w, http.StatusOK, res)
}

func (s *Server) report(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	last := s.last
	s.mu.Unlock()
	if last == nil {
		fail(w, http.StatusNotFound, errors.New("no test has run yet"))
		return
	}
	reply(w, http.StatusOK, last)
}

// ListenSock listens on the runner's socket, only root and the Shell
// reaching it.
func ListenSock(path string) (net.Listener, error) {
	os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return nil, err
	}
	return ln, nil
}

// ListenUSB listens on the USB link's address once it is there: the
// Shell gives the link its address when the station configures the phone.
func ListenUSB(ctx context.Context, addr string) (net.Listener, error) {
	for {
		ln, err := net.Listen("tcp", addr)
		if err == nil {
			return ln, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// Serve answers on ln until it closes.
func Serve(ln net.Listener, h http.Handler) error {
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second}
	return srv.Serve(ln)
}
