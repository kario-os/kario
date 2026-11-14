package smsd

import "github.com/kario-os/kario/ui"

// alertLevel reads an alert's level from the channel (message identifier)
// it came on, 3GPP TS 23.041 §9.4.1.2.2: CMAS / EU-Alert from 4370, the
// same levels again from 4383 in the subscriber's other language; ETWS
// earthquake and tsunami warnings 4352-4354, 4356. Monthly tests,
// exercises, operator messages and the rest are tests.
func alertLevel(channel int) string {
	switch {
	case channel == 4370, channel == 4383:
		return ui.AlertPresidential
	case channel >= 4371 && channel <= 4372, channel >= 4384 && channel <= 4385,
		channel >= 4352 && channel <= 4354, channel == 4356:
		return ui.AlertExtreme
	case channel >= 4373 && channel <= 4378, channel >= 4386 && channel <= 4391, channel == 4396, channel == 4397:
		return ui.AlertSevere
	case channel == 4379, channel == 4392:
		return ui.AlertAmber
	}
	return ui.AlertTest
}
