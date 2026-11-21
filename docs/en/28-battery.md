> English translation of [the Chinese original](../28-battery.md). The Chinese text is authoritative; keep both in sync.

# Battery life: the screen, the notification light, needless wake-ups

> Status: **prototype ✓ (developer machine, QEMU)**. Turning the screen off, waking it with a key, the notification light's slow blink, its going out once things are seen, and calls, alarms and chargers lighting the screen pass in the selftest; on the emulator the notification light is a userspace LED that the kernel's timer trigger blinks with a 5-second period (the selftest measures the period). Power draw on real hardware has not been measured: that **waits for a Snapdragon board** (R16).

## 0. Conclusions

- The screen is the phone's biggest draw, so: with no key pressed it goes off (30 seconds by default, 15 seconds to 5 minutes in Settings), dimmed for its last 5 seconds; while it is off the renderer turns the panel off and the Shell sends no frames.
- New SMS, new messages and missed calls **do not light the screen**: when the board has a notification light it slow-blinks (lit 0.2 seconds every 5) until the user has seen them in the notification centre or in their app.
- The software itself wakes less: with the screen off the Shell's housekeeping slows from twice a second to once every 5 seconds, and frames are drawn only when something changed, so an idle phone does not wake 60 times a second.
- Which board has which light and backlight is written in the SKU's board configuration ([24](24-sku-framework.md)), and the code drives them through `hal/` ([11](11-platform-architecture.md) §"Then what is hal/").

## 1. The screen

| Situation | Screen |
|------|------|
| No key for the timeout | First dimmed to 20% (where there is a backlight device), with the keyboard backlight turned off, then off; dimming, going off, lighting up and a new brightness all fade instead of jumping ([37](37-motion.md) §1, **not tried on a phone**) |
| Keyboard backlight | Where there is one it goes on and off with the screen, at the screen's brightness (Settings › Display and brightness); dimming turns it off first. The board configuration's `keyboard_backlight` names its LED; without it the first LED named `<device>::kbd_backlight` is used (the kernel's naming convention); `hal/backlight`'s unit tests and the Shell's tests ✓, **not tried on a phone** |
| Automatic brightness | Settings › Display and brightness › Auto brightness (off by default): the backlight follows the ambient light sensor (`hal/sensor`, IIO); the brightness set is the level in an ordinary room (300 lux), a dark room scales it down to three tenths and daylight up to two and a half times, between 10% and 100%. The reading is smoothed on a logarithmic scale, faster when it gets brighter (about 3 seconds) than darker (about 7); light that stays within a quarter of what it was does not move the level, so a hand's shadow does not make it flicker. In daylight (above 1000 lux, back below 600) the keyboard backlight goes off, automatic brightness or not. The board configuration's `light_sensor` gives the sensor's IIO name; without it the first IIO device that measures illuminance is used; on machines without a sensor it is simulated (the settings page says so). Unit tests and selftest ✓, **not tried on a phone** |
| A video, the camera's viewfinder or a video call on the screen, or something ringing | Stays on |
| Any key with the screen off | Lights it; **that press only lights it** and does nothing else (a press in a pocket does not open the menu) |
| An incoming call, an alarm, a charger plugged in, a computer asking to be trusted | Lights by itself; on a charger the status bar's battery has a lightning bolt beside it and, below full, fills from its level a step at a time and starts again, and the standby screen draws a big battery and its percentage under the date, moving with it (phone-paint times it itself and draws only the battery again, [37](37-motion.md) §3; **not tried on a phone**) |
| New SMS, new messages, missed calls | With a notification light the screen stays off and the light slow-blinks; a model without one lights the screen, and the usual timeout applies. The notification still goes into the notification centre and plays its sound |

While the screen is off:

- The Shell tells the renderer to turn the panel off (the protocol's `power`, [23](23-renderer-protocol.md)): phone-paint stops the display controller's scan-out (the CRTC) and the panel driver powers the panel and its backlight down; on again, it shows the last frame received while off.
- The Shell draws and sends no frames; the housekeeping (freezing background apps, memory, alarms, jobs, USB) runs every 5 seconds, so an alarm rings at most 5 seconds late.
- The keypad lock still follows its own setting (Settings › Keypad lock); it is a separate matter from the screen going off.

## 2. The notification light

| Part | How |
|------|------|
| When it blinks | The screen is off and the notification centre holds notifications the user has not seen |
| What counts as seen | Opening the notification centre (all of them), or the app coming to the foreground (its own); an app's new notifications while it is in the foreground count as seen at once |
| How it blinks | `hal/led` uses the LED class's timer trigger: `timer` written to `trigger`, `delay_on` 200 and `delay_off` 4800 (milliseconds), at maximum brightness (short and bright costs less than long and dim). Where the driver can blink by itself (a phone PMIC's LED, such as Qualcomm's LPG) the hardware blinks and the CPU does not wake; otherwise a kernel timer switches it, still with no user-space process involved |
| Multicolor lights | The board configuration's `notify_color` gives each channel's intensity (the F(x)tec Pro1 X's `rgb:status`: green) |
| Boards without a light | No `[leds]` is configured (the OnePlus N100): an unseen notification arriving with the screen off lights the screen, and the usual timeout turns it off again |
| The emulator | The kernel has uleds (`CONFIG_LEDS_USER`); when the light the board should have does not exist, the Shell makes a userspace LED of that name itself; the timer trigger works on it the same way, and the Shell reads when the kernel switches it on and off |
| The developer machine | A simulated light in memory, whose state the selftest checks |

## 3. The software wakes less

| Before | Now |
|------|------|
| On the device the Shell woke every 16 milliseconds to see whether to draw a frame | A frame is drawn 16 milliseconds after an event (a key, an app's message, a media frame, the housekeeping), and nothing wakes otherwise; during the boot animation it still draws frame by frame |
| Housekeeping every 0.5 seconds, always | 0.5 seconds with the screen on, 5 seconds with it off |
| Background apps | Already: frozen when they leave the foreground (the cgroup freezer), with pushes and jobs woken together within a budget ([21](21-push.md), [22](22-developer-api.md)) |
| Users cannot tell how much CPU an app uses in the background | Every 30 seconds the Shell reads each app's CPU (the cgroup's `cpu.stat`, or `/proc/<pid>/stat` without cgroups) and keeps a day of it, split into foreground and background. An app that is not in a call or playing music yet averages more than 5% of a core in the background over 10 minutes is named in a system notification (once a day), whose "Stop it" (the `s` key) ends it; Settings › Battery shows the charge and lists each app's background and foreground CPU over the last day, by background use (`energy` in `settings.get`) |
| With the screen off the phone stayed awake | 30 seconds after the screen goes off, with no call, no sound playing, no update installing, no computer on the USB link and no app woken for a push or a job, the whole phone suspends: to RAM where the board has it (`mem` in `/sys/power/state`), else to idle (`freeze`). Before it does, the RTC's wake alarm is set 3 seconds before the next alarm, scheduled notification (`notify.schedule`, [22](22-developer-api.md) §4.4) or job deadline, 15 minutes later at the latest (the push connection catches up on its keep-alive, due jobs run together); the baseband's calls and messages and the keys wake it any time. After a wake, what it brought (a push, a message) gets 10 seconds first. The kernel's `wakeup_count` is read before deciding and written back when suspending: a wake-up event in between (a key press) makes the kernel refuse, and it is not lost (`hal/pm`, `shell/sleep.go`). The Shell's clock is now the wall clock, so time asleep counts towards alarms and jobs (Go's monotonic clock stands still while suspended). Only on a phone whose board configuration says it can be woken from suspend (`suspend` in `sku.json`: real boards by default, the emulator's board not); the developer machine never suspends; nor does a board that can, where no key can wake the system (no input device's `power/wakeup` is `enabled`), since its user could not wake it |

## 4. Settings

Settings › Keypad lock: "Auto lock" at the top, "Screen off" below it (15 seconds, 30 seconds, 1 minute, 2 minutes, 5 minutes; 30 seconds by default). The setting is `screen_off` (seconds, 0 for the default).

## 5. Tests

| Case | What it tests |
|------|--------|
| A model without a notification light | With the light taken away, an SMS arriving with the screen off lights it; after the timeout it goes off again |
| Suspend | With an alarm set 2 minutes ahead and suspending turned on, the phone suspends 30 seconds after the screen goes off, the RTC wakes it and the alarm rings (the wake time for a scheduled notification is checked by the Shell's unit test `TestScheduledFiresWhileFrozen`, not by the suspend selftest). On the developer machine and the emulator the suspend is simulated (the Shell's clock moves to the wake time): the emulator, once really suspended to idle, cannot be woken (its RTC, the pl031, arms no wake-up interrupt for suspend-to-idle, and its virtual keyboard is no wake-up source either), so it does not suspend by default there; real suspend and wake-up **must be verified on real hardware** |
| Background drain | The calculator is charged 40 seconds of CPU in the background (`cpu-charge`); a system notification names it, and "Stop it" in the notification ends it |
| Settings › Battery | After the case above, the Battery page lists the calculator with 40 seconds in the background |
| Screen power saving | With the timeout on, moving the clock 31 seconds on turns the screen off; an SMS arriving then does not light it, and the light slow-blinks; one key press only lights it (still on the idle screen); off again, the light blinks again; once the notification centre has been opened, off again, the light stays dark |
| Notification light (emulator) | The userspace LED: the kernel's timer trigger lit it at least 2 times with the 5-second period (within a fifth) |
| Charging | A PD charger plugged in with the screen off lights it, with "Fast charging"; the pixel renderer's snapshot `78-charging`: the charging battery on the standby screen (at its level in a snapshot) and the bolt in the status bar |
| `hal/led`, `hal/backlight`, `hal/pm` unit tests | How the timer trigger is written, a multicolor light's intensities, turning it off; the backlight's percentages and power, its fades (along CIE lightness, no jump when the target changes mid-way, down to the darkest before off, back up from a fade-out, on a fake sysfs tree), LED-class backlights (a keyboard's `::kbd_backlight`, the vendor kernels' `lcd-backlight`: off is brightness 0, on again brings the level back); the suspend handshake (`wakeup_count` written back), the wake alarm set and cleared again, `mem` used where there is `mem`, and no suspend without an RTC wake alarm |

The selftest turns the timeout off at its start (a script's key presses would be spent lighting the screen) and on only in the cases above (`screen-auto on`).

## 6. Not done

- Measuring power on real hardware: the current with the screen off, how much a day of standby costs (that needs a board).
- Suspend on real hardware: whether the baseband (calls, messages) and the keys are the board's wake-up sources, and whether the notification light keeps blinking in deep sleep (a PMIC light blinking on its own does, the kernel's timer trigger stops with the CPU): needs the board.
- The proximity sensor (screen off against the face during a call) is **not implemented** ([11](11-platform-architecture.md)).