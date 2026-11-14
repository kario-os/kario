package shell

import (
	"encoding/json"
	"errors"
	"slices"

	"github.com/kario-os/kario/svc"
	"github.com/kario-os/kario/ui"
)

// Airplane mode and mobile data (Settings, the power menu; radio.state,
// radio.set). Airplane mode powers the modem's radio down (telephonyd) and
// turns Wi-Fi and Bluetooth off, keeping the user's switches; the user may
// turn either on again meanwhile, which airplane mode then leaves on the
// next time too, and its end restores the switches as the user last set
// them (Android's behaviour). Dialing an emergency number powers the radio
// up (telephonyd does, whoever dials) and ends airplane mode so the
// emergency services can call back, as Android does. Mobile data off
// disconnects the cellular data link (telephonyd) and closes its interfaces
// in the firewall. Both switches are settings and hold across restarts;
// telephonyd keeps its own copy, so the modem starts as it was before the
// shell is up.

// airplaneMark is the status bar's network while airplane mode is on.
const airplaneMark = "✈"

// cutMobile names the firewall's cut of the cellular data interfaces.
const cutMobile = "mobile"

type radioShell struct {
	cut    []string // the interfaces the firewall closes now
	cutSet bool
	obex   obexState
}

func (c *Core) radioState() ui.RadioState {
	s := ui.RadioState{Airplane: c.settings.Airplane, MobileData: !c.settings.MobileDataOff}
	if c.tel != nil {
		s.Cellular = map[bool]string{true: "on", false: "off"}[c.tel.Radio]
		s.Data = c.tel.DataState
	}
	return s
}

func (b *broker) dispatchAirplane(m ui.Msg, reply func(any, error)) {
	c := b.c
	if m.Cap == ui.CapRadioState {
		reply(c.radioState(), nil)
		return
	}
	var args ui.RadioArgs
	_ = json.Unmarshal(m.Args, &args)
	if args.Airplane == nil && args.MobileData == nil {
		reply(nil, errors.New("radio.set takes airplane or mobile_data"))
		return
	}
	if args.MobileData != nil {
		c.setMobileData(*args.MobileData)
	}
	if args.Airplane == nil {
		reply(c.radioState(), nil)
		return
	}
	c.setAirplane(*args.Airplane, func(err error) { reply(c.radioState(), err) })
}

func (c *Core) setMobileData(on bool) {
	if on == !c.settings.MobileDataOff {
		return
	}
	c.settings.MobileDataOff = !on
	c.saveSettings()
	c.applyDataCut()
	c.log.Printf("mobile data %v", on)
	if c.HasTelephony() {
		go func() {
			if err := c.telRPC(svc.TelDataSet, map[string]bool{"on": on}, nil); err != nil {
				c.log.Printf("mobile data: %v", err)
			}
		}()
	}
}

// setAirplane switches airplane mode; done runs on the loop once the radios
// have followed. telephonyd refusing (an emergency call) undoes it.
func (c *Core) setAirplane(on bool, done func(error)) {
	if on == c.settings.Airplane {
		done(nil)
		return
	}
	for _, call := range c.cells {
		if on && call.Emergency {
			done(errors.New(tr.T("Not during an emergency call")))
			return
		}
	}
	wifi, bt, tel, s := c.wifi, c.bt, c.HasTelephony(), c.settings
	c.settings.Airplane = on
	c.saveSettings()
	c.log.Printf("airplane mode %v", on)
	go func() {
		if tel {
			if err := c.telRPC(svc.TelRadioSet, map[string]bool{"on": !on}, nil); err != nil {
				c.Post(func() {
					c.settings.Airplane = !on
					c.saveSettings()
					done(err)
				})
				return
			}
		}
		wasWifi, wasBT := airplaneRadios(wifi, bt, on, s)
		c.Post(func() {
			if on {
				c.settings.AirplaneWifi, c.settings.AirplaneBT = wasWifi, wasBT
				c.saveSettings()
			}
			done(nil)
		})
	}()
}

// airplaneRadios turns Wi-Fi and Bluetooth off for airplane mode, but not
// one the user turned on in it before, and reports how they were; at its
// end it brings back the user's switches.
func airplaneRadios(wifi wifiRadio, bt btRadio, on bool, s sysSettings) (wasWifi, wasBT bool) {
	if wifi != nil {
		if on {
			st, err := wifi.state()
			wasWifi = err == nil && st.Enabled
			if wasWifi && !s.KeepWifi {
				_ = wifi.setEnabled(false)
			}
		} else {
			_ = wifi.setEnabled(s.AirplaneWifi)
		}
	}
	if bt != nil {
		if on {
			st, err := bt.state()
			wasBT = err == nil && st.Enabled
			if wasBT && !s.KeepBT {
				_ = bt.setEnabled(false)
			}
		} else {
			_ = bt.setEnabled(s.AirplaneBT)
		}
	}
	return wasWifi, wasBT
}

// airplaneSwitch records the user turning Wi-Fi or Bluetooth on or off in
// airplane mode: it holds when airplane mode ends, and airplane mode
// leaves the radio on the next time if it is on.
func (c *Core) airplaneSwitch(capName string, on bool) {
	if !c.settings.Airplane {
		return
	}
	switch capName {
	case ui.CapWifiSet:
		c.settings.AirplaneWifi, c.settings.KeepWifi = on, on
	case ui.CapBTSet:
		c.settings.AirplaneBT, c.settings.KeepBT = on, on
	default:
		return
	}
	c.saveSettings()
}

// airplaneAtBoot: iwd and BlueZ power their radios up when they start; in
// airplane mode only those the user turned on in it stay on.
func (c *Core) airplaneAtBoot() {
	s := c.settings
	if !s.Airplane {
		return
	}
	wifi, bt := c.wifi, c.bt
	go func() {
		if wifi != nil {
			_ = wifi.setEnabled(s.AirplaneWifi && s.KeepWifi)
		}
		if bt != nil {
			_ = bt.setEnabled(s.AirplaneBT && s.KeepBT)
		}
	}()
}

// syncRadio brings telephonyd's switches to the settings when it
// (re)connects: it may have started with an older copy.
func (c *Core) syncRadio() {
	radio, data := !c.settings.Airplane, !c.settings.MobileDataOff
	for _, sw := range []struct {
		method   string
		now, has bool
	}{{svc.TelRadioSet, radio, c.tel.Radio}, {svc.TelDataSet, data, c.tel.Data}} {
		if sw.now == sw.has {
			continue
		}
		go func() {
			if err := c.telRPC(sw.method, map[string]bool{"on": sw.now}, nil); err != nil {
				c.log.Printf("telephony %s: %v", sw.method, err)
			}
		}()
	}
	c.applyDataCut()
}

// emergencyRadio ends airplane mode for an emergency call: telephonyd has
// powered the radio up for it, and the emergency services may call back.
func (c *Core) emergencyRadio(call ui.TelCall) {
	if !call.Emergency || !c.settings.Airplane || call.State == "ended" {
		return
	}
	c.setAirplane(false, func(err error) {
		if err != nil {
			c.log.Printf("airplane mode: %v", err)
			return
		}
		c.showToast(tr.T("Airplane mode is off for the emergency call"))
	})
}

// dataNet is the status bar's network type: none without the data link,
// as phones then show only the bars.
func (c *Core) dataNet() string {
	if c.settings.MobileDataOff || c.tel.DataState != "connected" {
		return ""
	}
	return c.tel.RAT
}

// applyDataCut closes the cellular data interfaces telephonyd reports in
// the firewall while mobile data is off, and opens them again. Without
// telephonyd the last cut stays.
func (c *Core) applyDataCut() {
	if c.traffic == nil || (c.tel == nil && c.settings.MobileDataOff) {
		return
	}
	var ifaces []string
	if c.settings.MobileDataOff {
		ifaces = c.tel.DataIfaces
	}
	if c.radios.cutSet && slices.Equal(ifaces, c.radios.cut) {
		return
	}
	if err := c.traffic.fw.ApplyCut(cutMobile, ifaces); err != nil {
		c.log.Printf("mobile data: firewall: %v", err)
		return
	}
	c.radios.cut, c.radios.cutSet = slices.Clone(ifaces), true
	c.log.Printf("mobile data: the firewall closes %v", ifaces)
}

// toggleAirplane is the power menu's entry.
func (c *Core) toggleAirplane() {
	on := !c.settings.Airplane
	c.setAirplane(on, func(err error) {
		switch {
		case err != nil:
			c.showToast(tr.T("Failed: %v", err))
		case on:
			c.showToast(tr.T("Airplane mode on"))
		default:
			c.showToast(tr.T("Airplane mode off"))
		}
	})
}
