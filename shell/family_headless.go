package shell

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/kario-os/kario/family"
	"github.com/kario-os/kario/family/familytest"
	"github.com/kario-os/kario/push"
	"github.com/kario-os/kario/svc"
)

// The remote management's selftest steps (headless.go): a Kario account
// server's kario-family/1 in the Shell's process (package familytest, on
// 127.0.0.1) as the product's, its key as the one the product ships, its
// pushes through the push gateway as the real server's go, and the parent
// on its web page as steps (answering a request for more time, confirming
// a PIN reset among them).

type familySim struct {
	srv                *familytest.Server
	ts                 *httptest.Server
	savedURL, savedKey string
	keyFile            string
}

func (r *Runner) familyStep(cmd, arg string) (err error) {
	c := r.C
	f := c.family()
	arg = strings.TrimSpace(arg)
	if cmd != "parent-server" && f.sim == nil {
		return errors.New("no parent-server")
	}
	device := func() (string, error) {
		if b := f.file.Bound; b != nil {
			return b.Device, nil
		}
		return "", errors.New("the phone is not managed")
	}
	pushes := cmd != "parent-server" && cmd != "parent-bound" && cmd != "parent-asked" && !(cmd == "parent-reset" && strings.HasPrefix(arg, "done"))
	if f.sim != nil && pushes {
		sent, failed := f.sim.srv.Pushes()
		defer func() {
			if s, fl := f.sim.srv.Pushes(); err == nil && (s == sent || fl > failed) {
				err = fmt.Errorf("%s: the server's push to the phone did not go through the gateway", cmd)
			}
		}()
	}
	switch cmd {
	case "parent-server":
		if s := f.sim; s != nil {
			s.ts.Close()
			_ = os.Remove(s.keyFile)
			c.cfg.AccountURL, c.cfg.FamilyKey, f.sim = s.savedURL, s.savedKey, nil
		}
		switch arg {
		case "off":
			return nil
		case "on":
		default:
			return errors.New("usage: parent-server on|off")
		}
		s := &familySim{srv: familytest.New(), savedURL: c.cfg.AccountURL, savedKey: c.cfg.FamilyKey,
			keyFile: filepath.Join(c.cfg.RunDir, "family-selftest.pub")}
		if err := os.WriteFile(s.keyFile, []byte(base64.StdEncoding.EncodeToString(s.srv.Pub)+"\n"), 0o644); err != nil {
			return err
		}
		s.srv.Push = c.familySimPush
		s.ts = httptest.NewServer(s.srv)
		c.cfg.AccountURL, c.cfg.FamilyKey, f.sim = s.ts.URL, s.keyFile, s
	case "parent-bind":
		if f.pair == nil {
			return errors.New("the phone shows no code")
		}
		if _, err := f.sim.srv.Claim(f.pair.code, arg); err != nil {
			return err
		}
	case "parent-policy":
		var rules family.Rules
		if err := json.Unmarshal([]byte(arg), &rules); err != nil {
			return fmt.Errorf("usage: parent-policy <rules as JSON>: %v", err)
		}
		id, err := device()
		if err != nil {
			return err
		}
		v, err := f.sim.srv.SetRules(id, rules)
		if err == nil {
			c.log.Printf("selftest: the parent saved policy %d", v)
		}
		return err
	case "parent-report", "parent-locate":
		text, ms := lastNum(arg, 8000)
		id, err := device()
		if err != nil {
			return err
		}
		if cmd == "parent-report" {
			err = f.sim.srv.AskReport(id)
		} else {
			_, err = f.sim.srv.Locate(id)
		}
		if err != nil {
			return err
		}
		got := ""
		if _, ok := r.waitFor(func(string) bool {
			st, _ := f.sim.srv.Device(id)
			var v any = st.Report
			if cmd == "parent-locate" {
				v = st.Location
			}
			raw, _ := json.Marshal(v)
			got = string(raw)
			return strings.Contains(got, text)
		}, ms); !ok {
			return fmt.Errorf("the server's %s: %s", strings.TrimPrefix(cmd, "parent-"), got)
		}
		fmt.Fprintf(r.Out, "PHONE-FAMILY: %s %s\n", strings.TrimPrefix(cmd, "parent-"), got)
	case "parent-unbind":
		id, err := device()
		if err != nil {
			return err
		}
		return f.sim.srv.Unbind(id)
	case "parent-asked":
		text, ms := lastNum(arg, 8000)
		id, err := device()
		if err != nil {
			return err
		}
		got := ""
		if _, ok := r.waitFor(func(string) bool {
			qs, _ := f.sim.srv.Asks(id)
			for _, q := range qs {
				raw, _ := json.Marshal(q.Ask)
				if got = string(raw); q.State == family.AskPending && strings.Contains(got, text) {
					return true
				}
			}
			return false
		}, ms); !ok {
			return fmt.Errorf("no request for more time holding %q: %s", text, got)
		}
		fmt.Fprintf(r.Out, "PHONE-FAMILY: asked %s\n", got)
	case "parent-answer":
		id, err := device()
		if err != nil {
			return err
		}
		qs, _ := f.sim.srv.Asks(id)
		i := slices.IndexFunc(qs, func(q familytest.Request) bool { return q.State == family.AskPending })
		if i < 0 {
			return errors.New("no request for more time waits")
		}
		minutes, err := strconv.Atoi(arg)
		if arg != "no" && (err != nil || minutes < 1) {
			return errors.New("usage: parent-answer <minutes>|no")
		}
		return f.sim.srv.Answer(id, qs[i].ID, arg != "no", minutes)
	case "parent-reset":
		id, err := device()
		if err != nil {
			return err
		}
		what, ms := lastNum(arg, 8000)
		switch what {
		case "confirm":
			return f.sim.srv.ConfirmReset(id)
		case "done":
			if _, ok := r.waitFor(func(string) bool { st, _ := f.sim.srv.Device(id); return st.Reset == "done" }, ms); !ok {
				st, _ := f.sim.srv.Device(id)
				return fmt.Errorf("the server's reset is %q, not done", st.Reset)
			}
		default:
			return errors.New("usage: parent-reset confirm|done [ms]")
		}
	case "parent-bound":
		want, ms := lastNum(arg, 8000)
		if want != "yes" && want != "no" {
			return errors.New("usage: parent-bound yes|no [ms]")
		}
		bound := func() bool {
			for _, id := range f.sim.srv.Devices() {
				if st, _ := f.sim.srv.Device(id); st.State == family.StateBound {
					return true
				}
			}
			return false
		}
		if _, ok := r.waitFor(func(string) bool { return bound() == (want == "yes") }, ms); !ok {
			return fmt.Errorf("the server holds a bound phone: %v", !(want == "yes"))
		}
	}
	return nil
}

// familySimPush posts a push to the phone's token through the gateway pushd
// is connected to, as the account server posts to the product's.
func (c *Core) familySimPush(token string, d family.PushData) error {
	var st struct {
		Gateway string `json:"gateway"`
		Cert    string `json:"gateway_cert"`
	}
	if err := c.pushRPC(svc.PushState, nil, &st); err != nil || st.Gateway == "" {
		return fmt.Errorf("push gateway unknown: %v", err)
	}
	client, err := push.Client(st.Cert, 10*time.Second)
	if err != nil {
		return err
	}
	data, _ := json.Marshal(d)
	raw, _ := json.Marshal(push.Send{Token: token, Data: string(data)})
	resp, err := client.Post(st.Gateway+"/v1/push", "application/json", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("gateway: %s %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	return nil
}
