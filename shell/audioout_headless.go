package shell

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/kario-os/kario/audio/route"
	"github.com/kario-os/kario/svc"
)

// audioOutput is the step audio-output: stand-in devices plugged in and
// pulled out through phone-audiod's output.sim, the picker's choice, and
// waits for where media and a VoIP call go.
func (r *Runner) audioOutput(f []string) error {
	usage := errors.New("usage: audio-output plug <id> wired|usb|bluetooth [mic] [hw] [<name>] | unplug <id> | pick <id>|auto | media <id> [ms] | call <id> <mic> [ms]")
	if len(f) < 2 {
		return usage
	}
	c := r.C
	sim := func(args map[string]any) error {
		var v outputsView
		if err := c.audioRPC(svc.AudioOutputSim, args, &v); err != nil {
			return fmt.Errorf("audio-output %s: %w", f[0], err)
		}
		return nil
	}
	wait := func(rest []string, ok func(v outputsView) bool) error {
		ms := 5000
		if len(rest) > 0 {
			n, err := strconv.Atoi(rest[0])
			if err != nil {
				return usage
			}
			ms = n
		}
		var v outputsView
		if _, done := r.waitFor(func(string) bool {
			v = outputsView{}
			return c.audioRPC(svc.AudioOutputs, nil, &v) == nil && ok(v) && ok(c.outputs)
		}, ms); !done {
			return fmt.Errorf("audio-output %s: phone-audiod: media on %q, a call on %q with the microphone %q; the Shell: media on %q (outputs %v)",
				strings.Join(f, " "), v.Media, v.Call, v.CallMic, c.outputs.Media, outputIDs(v.Outputs))
		}
		return nil
	}
	switch f[0] {
	case "plug":
		if len(f) < 3 {
			return usage
		}
		args := map[string]any{"op": "plug", "id": f[1], "kind": f[2]}
		rest := f[3:]
		for len(rest) > 0 && (rest[0] == "mic" || rest[0] == "hw") {
			args[map[string]string{"mic": "mic", "hw": "hw_volume"}[rest[0]]] = true
			rest = rest[1:]
		}
		args["name"] = strings.Join(rest, " ")
		if err := sim(args); err != nil {
			return err
		}
	case "unplug":
		if err := sim(map[string]any{"op": "unplug", "id": f[1]}); err != nil {
			return err
		}
	case "pick":
		id := f[1]
		if id == "auto" {
			id = ""
		}
		var v outputsView
		if err := c.audioRPC(svc.AudioOutputPick, map[string]string{"id": id}, &v); err != nil {
			return fmt.Errorf("audio-output pick: %w", err)
		}
	case "media":
		return wait(f[2:], func(v outputsView) bool { return v.Media == f[1] })
	case "call":
		if len(f) < 3 {
			return usage
		}
		return wait(f[3:], func(v outputsView) bool { return v.Call == f[1] && v.CallMic == f[2] })
	default:
		return usage
	}
	r.pump(100 * time.Millisecond)
	return nil
}

// simOutputs is whether phone-audiod takes stand-in devices: only the one
// a selftest starts with --sim-outputs, not a phone's or QEMU's.
func (r *Runner) simOutputs() bool {
	var v outputsView
	return r.C.HasAudio() && r.C.audioRPC(svc.AudioOutputs, nil, &v) == nil && v.Sim
}

func outputIDs(outs []route.Output) []string {
	var ids []string
	for _, o := range outs {
		ids = append(ids, o.ID)
	}
	return ids
}
