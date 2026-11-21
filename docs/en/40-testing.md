> English translation of [the Chinese original](../40-testing.md). The Chinese text is authoritative; keep both in sync.

# Testing: methods and strategy

> How Kario is tested: the layers, the command for each, which hardware is simulated, how one scenario script runs both on the developer machine and in the emulator, what to run for what change, what to run before publishing, and the rules for writing tests. The commit and CI flow is in [38](38-contributing.md); each round's results are in [13](13-prototype.md) §5 and the internal verification baseline (57 (internal document)).
> Status: the commands and flows written here have been used on the developer machine (RHEL 8, x86-64, 48 cores) and in QEMU (qemuarm64, TCG) (2026-11-19); **not tried on a phone**: testing on a real phone (§7) has not started.

## 0. Summary

- **One scenario script runs everywhere**: `shell/selftest.script` (208 cases) runs headless on the developer machine (the text UI), through the pixel renderer in three form factors, and at boot in QEMU's two images; the compatibility test (CTS) is that plus capability coverage and a signed report. On a real phone it will be the same script (§7).
- **Simulations stand in for hardware, and their limits are written down**: the baseband, ModemManager, Wi-Fi, Bluetooth, NFC, satellite positioning, sensors, fingerprint, camera and the update server all have a simulated side (§3); the real-phone part of each is marked **not tried on a phone**.
- **Every bug fix comes with a test that fails on the old code**, seen failing before the fix; **every capability is called at least once**, or the CTS fails; **every place that parses untrusted input has a fuzz target** (67 targets).
- **Run what the change needs**: CI picks by the change ([38](38-contributing.md) §3); before publishing the whole set runs on the developer machine and in QEMU (§5).
- **Results go into the docs as they are**: ✓ names only the environment it ran in and the date, simulated things say "simulated", what has not run says **not verified**.

## 1. Layers

| Layer | What it tests | Command | Time (developer machine) |
|-------|---------------|---------|--------------------------|
| Static checks | Formatting, `go vet`, clippy, no Chinese in code, complete translations, shared constants, the Chinese and English docs' structure, doc links, licences, the Yocto recipes, workflows (actionlint), DCO | the first half of `make test`; CI's `checks` | 1 minute |
| Unit tests | Go's 126 packages (976 test functions), Rust (`paint/`, `gui/`), C++ (telephonyd), the C SDK's sample app, TypeScript (`tsc`, Kario Studio's `npm test`), Python scripts | `make test`, `cargo test --release`, `make telephonyd`, `make capi` | 5 minutes (with the next row) |
| End to end (developer machine) | Real processes: the Shell, the system services, the apps; the scenario script in six parallel parts | `go test ./shell` (`TestSelftestScenario` and others, run last and on its own in `make test`) | 3 minutes |
| Selftest (developer machine) | The same script once through, serially, with snapshots and a JUnit report | `make selftest`, `ONLY='regexp'` for a subset | about 10 minutes |
| Compatibility test | Selftest + the probe app (`cts/probe`) + capability coverage (every capability called at least once) + a report signed by the device | `make cts` | about 12 minutes |
| Pixel selftest | The same script drawn by phone-paint in the 9-key, full-keyboard and whole-phone form factors, a PNG per `snap`, and at the end the live display's time a step (not checked, [37](37-motion.md) §5) | `make pixel-selftest` (to compare: `make gui-selftest`) | about 30 minutes |
| Baseband backend | telephonyd's ModemManager backend against a simulated ModemManager 1.24 (D-Bus, in a Podman container) | `make mm-selftest` | 1 minute |
| Fuzzing | 67 targets (Go's native fuzzing; the C / C++ ones with ASan and UBSan), 30 seconds each by default | `make fuzz`, `make fuzz-libfuzzer`; in CI by hand: the `fuzz` workflow | about 5 minutes (8 at a time) |
| QEMU boot selftest | The device image and the product image run the same script at boot, then the sound, CTS and fd store checks | `make yocto-selftest` (the product image from kario-internal with `KARIO=`) | build 4 minutes + 20 minutes |
| QEMU special runs | System updates (A/B, rollback, staged rollout, automatic updates), cross-version deltas and resuming, upgrades between releases, cloud services, 1 GB memory pressure, soak, semantic vs web comparison, the pixel UI on screen | `make yocto-ota-test`, `yocto-ota-base` + `yocto-ota-delta`, `yocto-ota-release`, `yocto-cloud-test`, `yocto-stress`, `yocto-soak`, `yocto-bench`, `yocto-gui` | 30–75 minutes each |
| Soak, old apps | 30 minutes on the developer machine watching for leaks; apps built from an old version work on the new Shell | `make soak`, `make compat-old OLD_REV=<commit>` | 30 minutes, 10 minutes |
| Real phone | OnePlus 6T: apps in a chroot, then the whole system flashed ([39](39-oneplus-6t.md)) | **not tried on a phone** | — |

## 2. The scenario script

`shell/selftest.script` is a key script: `== title` starts a case, one command per line below it, run by `Runner` in `shell/headless.go`. The commands are of three kinds:

- **Keys and waiting**: `key`, `type`, `long`, `tap` (pixel renderer only), `wait text [ms]`, `wait-re regexp`, `gone text`, `at screen`, `key-if key text ms`.
- **Simulating the world**: calls and messages (`tel`, `sms`, `mms`), Wi-Fi access points (`wifi-ap`, `wifi-join`), Bluetooth peers (`bt-peer`, `bt-push`, `bt-oldphone`), card readers (`nfc`), position (`location`), light (`lux`), fingerprints (`finger`), time (`timeshift`), servers (`fetch-server`, `carddav-server`, `parent-server`, `store-publish`) and more, each described at the top of `headless.go`.
- **Checks**: commands that look at the system's state directly, such as `saved`, `sealed`, `perm-state`, `vibrated`, `appstate`, and `snap name` (a snapshot).

`require-*` lines skip a case where the environment lacks something (158 lines: no baseband `require-tel`, no SMS service `require-sms`, BlueZ only `require-bt bluez`, pixel renderer only `require-gui`, ...); a skipped case says SKIP in the report and is not a failure.

**Four ways the same script runs**:

| Run | How | Waits scaled |
|-----|-----|--------------|
| `go test ./shell` | `#part name` lines split the script into six parts (chat, apps, keys, phone, location, power); each boots its own set of services and runs only its own cases, all six at once | 1x |
| `make selftest`, `make cts` | The whole script serially: what an earlier case left is there for a later one | 1x |
| `make pixel-selftest` | The whole script serially, once per form factor, drawn by the pixel renderer | 1x |
| `make yocto-selftest` | The image boots with `phone.selftest=1`, the whole script serially | scaled by `--timeout-scale` |

**What a failure prints**: the failing line and why, the whole screen's text at that moment, each app process's state (frozen or not, what it waits on), the last few dozen lines of the Shell's log; in QEMU also the Bluetooth and iwd journals and the snapshots' PNGs (base64 on the serial console). The system update test (`ota-test`) also prints the screen at a failure and the Shell's last lines about system updates.

**Rules for writing cases** (each learned the hard way):

- A case leaves the phone as it found it: delete the pictures, contacts, settings and pairings it added; later cases count things.
- Put a new case under the `#part` it belongs to; never rely on state another part left (in parallel it is not there).
- Wait for the thing you act on, not for any text: `wait X` passes as soon as X is anywhere on screen (a toast or the status bar count). To move to a row of a list, wait for that row's own text (its subtitle), or use `wait-re`.
- After a key that changes the screen, `at screen` or `wait` before the next key; never press keys back to back across a screen change.
- Lists may wrap: go to the first or last item with `key first`, `key last`, not `key up 5`.
- Do not hide a race behind a longer wait: when something does not appear, first look for two requests whose answers come in either order, or an old state replacing a new one (examples in §6).

## 3. What is simulated

| Part | Developer machine | QEMU | The real side |
|------|-------------------|------|---------------|
| Baseband, calls, SMS, cell broadcast | telephonyd's simulated baseband | same | the ModemManager backend against a simulated ModemManager (`make mm-selftest`); the real baseband **not tried on a phone** |
| Mobile data | a simulated bearer | a veth pair in place of the baseband's interface, addresses by systemd-networkd | **not tried on a phone** |
| Wi-Fi, hotspot | a simulated radio | real iwd on mac80211_hwsim, the test access point 192.168.80.1/24 | **not tried on a phone** |
| Bluetooth | a simulated radio | real BlueZ on btvirt's two virtual controllers (the headset, car kit and Android old phone played by the second) | **not tried on a phone** |
| NFC | a simulated controller and readers | same | the NCI backend **not tried on a phone** |
| Satellite positioning | a simulated receiver | NMEA through a QEMU pipe, through the real parser | **not tried on a phone** |
| Camera, QR scanning | synthesized frames | vivid (a virtual V4L2 device), real decoding | **not tried on a phone** |
| Fingerprint, light | simulated sensors | same | **not tried on a phone** |
| System updates | a simulated A/B phone | real RAUC, dm-verity and A/B disk, `ota-www.py` (which can simulate a network outage) or the real update server | booting a real phone's slots **not tried on a phone** |
| Cloud (store, push, accounts, CardDAV) | simulated servers in the process | the services in the build container (`yocto-cloud-test`) | the production services are tested separately (kario-internal) |
| A computer (USB) | simulated MTP and network sharing peers | FunctionFS on dummy_hcd | real computers **not tried on a phone** |

How far each simulation goes and what it leaves out: [13](13-prototype.md) §6.

## 4. What runs for what change

CI's rules are the `RULES` table in `scripts/ci-plan.py` ([38](38-contributing.md) §3): docs only run the checks; a Go package runs it and every package that depends on it; changes that go into `make build` add the selftest, CTS and pixel selftest; telephonyd's ModemManager backend adds `mm-selftest`; `yocto/` runs the recipe checks and two script tests that do not boot; unknown files run everything. The only gate is `pr-ok`. After publishing to main everything runs once by hand (`gh workflow run ci.yml --ref main`); each workflow keeps only its last 3 runs (`scripts/ci-prune.sh`).

What CI does not run (too slow or needs privileges): everything in QEMU, long `make fuzz` runs, soaks; these run on the developer machine as in §5.

## 5. Before publishing

In this order, on the developer machine:

1. The check scripts and `go vet` (seconds each).
2. `make test` (about 6 minutes).
3. `make cts` (about 12 minutes).
4. `make pixel-selftest` (about 30 minutes).
5. QEMU: `make yocto-selftest` of the device image and the product image, the two chains a minute apart at the same time (about 25 minutes each).
6. If telephonyd changed: `make mm-selftest`. If system updates changed: `make yocto-ota-test` (product image, about 45 minutes) and `make yocto-ota-delta` (about 75 minutes, its base saved by `make yocto-ota-base` on the previous release).
7. Publish (`make publish`) only when all of it passes; write the results into [13](13-prototype.md) §5 and the internal verification baseline.

**One shared lock**: on the developer machine the selftest, CTS, `go test ./shell` and the pixel selftest all take `/tmp/kario-selftest.lock` (`flock /tmp/kario-selftest.lock make cts`): they use the same fixed port (moving from an old phone, 47800) and the same state directories and would disturb each other. Give `go test ./shell` `-timeout 30m`: when the default 10 minutes run out the test is killed, and the service processes it started stay behind, still holding the lock.

## 6. Rules

- **A regression test fails first**: when fixing a bug, first write a test that reproduces it, run it once on the old code and see it fail (failing the way the bug did), then fix, then see it pass.
- **Every capability covered**: a new capability needs a case that calls it; `make cts` saying "never called" fails it (example: the account's "Use mobile data" switch passed only once a case pressed it).
- **Untrusted input is fuzzed**: the network, files, Bluetooth, NFC, SMS, update bundles, messages from apps. What fuzzing finds stays as a minimal input in a regression case (`testdata/fuzz/`).
- **An intermittent failure is a real bug until shown otherwise**: first rerun once on its own to tell intermittent from always, then find the root cause. Examples:
  - Bluetooth turned on in Settings and the search pressed at once: the Shell handed both changes to BlueZ concurrently, and an old state that came back late wiped the devices found. The fix made the changes run in order and Settings drop out-of-date refreshes, instead of waiting longer.
  - The command palette's "Go to" lists only the rows on the page when it opens, and on real iwd the network's row arrives late. The case now waits for that row before pressing `/`.
- **Intermittent failures on a busy machine**: QEMU emulates in software (TCG), and three emulators at once are much slower than one; judge an intermittent failure by rerunning when the machine is quiet. CI's machines have few cores and the Shell package's six parallel parts starve the timed tests of other packages, so `make test` and CI run the Shell package last and on its own.
- **Results go into the docs**: status lines name the environment and the date (developer machine, QEMU, real phone), what has not run says **not verified**, simulated things say "simulated"; figures from assessments say "estimate".

## 7. Not there yet

- **Real phones**: the steps for the OnePlus 6T's chroot app test and whole-system flash test are written ([39](39-oneplus-6t.md) §13); the phone has not arrived, **not tried on a phone**. On a real phone the same scenario script runs, plus what the emulator cannot test: call audio and routing, radio, power and standby current, temperature, the real screen's refresh.
- **Snapshot comparison**: the PNGs the selftest and pixel selftest keep are archived only, not compared pixel by pixel with reference images (**not implemented**); the renderer's own unit tests check that drawing by increments gives the same bytes as a full redraw.
- **Coverage**: code coverage is not measured (**not implemented**).
- **`-race`**: the changed packages run under `-race`; the scenario test does not pass under `-race`: rewriting the process-wide `time.Local` when the time zone changes is a data race, and the race runtime crashes while reporting it; Contacts' durable save of about 600 ms uses up the loop budget the Shell gives it and is falsely reported as not responding. Neither is fixed yet.
- **Production-line testing**: the station's flow for getting the device certificate exists ([22](22-developer-api.md) §11); the line's hardware tests (keys, screen, sound, radio) come with the whole phone, **not implemented**.
- **Network access and certification tests** (China's network access licence, CCC, SRRC) are done by a qualified phone maker, not in this repository.
