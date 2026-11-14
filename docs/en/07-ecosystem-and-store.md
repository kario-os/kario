> English translation of [the Chinese original](../07-ecosystem-and-store.md). The Chinese text is authoritative; keep both in sync.

# Ecosystem and app store

## 0. App tiers (D2, D5 decided)

The app framework has four "semantic-first" tiers ([14](14-tui-route-and-app-framework.md) §5, based on the measurements in [15](15-web-vs-semantic-benchmark.md)):

| Tier | Where the UI comes from | Keyboard operability | Used for |
|----|------------|------------|------|
| **Native semantic app** (default) | The app hands over content + actions through `phone.Ui`, and the Shell renders them ([10](10-keyboard-interaction-model.md) §4) | Guaranteed by the system; displayable in both the text and the pixel UI | Phone, SMS, contacts, settings, reading, IM, most third-party apps |
| **System-rendered document** | The app hands over Markdown / Mermaid, and the system lays it out | Guaranteed by the system | Help, agreements, manuals |
| **JS mini-program** | JS logic + `phone.Ui` views, running inside a host | Guaranteed by the system | Convenience services, lightweight business |
| **Web view component** | WPE WebKit hosts existing H5 content, embedded as a controlled component in a semantic page | Backstopped by the system's focus navigation | Existing web pages |
| **Canvas app** (exception) | The page holds a canvas node (`canvas`); what the app hands over is graphics (rectangles, lines, circles, points, polygons, with coordinates in its own units), which the system draws as pixels, so they can be shown in both the text and the pixel UI; text still uses ordinary nodes | While the canvas has the focus, OK, the arrow keys, digits and letters go to the app, while Back, the soft keys and End stay with the system; keyboard reachability is reviewed manually before listing | Games, maps, charts (sample: Snake) |

## 1. Vendor publishing (D4)

Goal: vendors such as WeChat and Alipay can publish `phone.Ui` versions of their apps using platform capabilities. For the capability catalog, the porting path and the non-technical hurdles see [12](12-super-app-capabilities.md); for runnable samples (a WeChat-like chat app with video calls, an Alipay-like wallet with trusted payment) see [13](13-prototype.md). SDKs: the Go SDK, the C ABI SDK (`sdk/c`) and "speak the protocol directly from any language" are all available ([22](22-developer-api.md) §9).

## 2. What we don't do

| Not doing | Reason |
|------|------|
| Android app compatibility | Memory, power and touchscreen dependencies; WeChat may ban accounts running in container environments ([09](09-ui-feasibility-review.md) §5) |
| WeChat / Alipay / Bilibili clients (reverse-engineered or unofficial APIs) | Violates the terms of service and can break at any time; payment also needs financial certification |
| Treating "adaptation by the big vendors" as a planning premise | Big vendors only adapt to platforms that have scale; following D4, the platform gets the capabilities and the porting cost ready, but the product roadmap does not depend on it |
| Native shared-library plugins | Third-party code always runs in its own process and is never loaded into the Shell as a `.so` (crash isolation) |
| PC low-code designer + script engine | Deferred until after v2; to be evaluated once the semantic model is frozen |

## 3. Phases

| Phase | Visible to users | Status |
|------|----------|------|
| **E0** | Only the apps in the system image | ✓ |
| **E1** | Sideloaded signed packages `.phoneapp` | ✓ signature verification, certificate chain, system confirmation, install, update, uninstall |
| **E2** | Official app store: catalog, downloads, updates | ✓ open core and simulated backend (§4.1): catalog, downloads, delta updates, takedown; the store client is a reference implementation; the official backend is not open source |
| **E3** | Developer portal: registration, upload, review, versions | Backend API ✓ (identity verification, manual review queue, crash reports, developers viewing their own apps, §4.1); web portal and staff review desk ✓ (not open source: the portal loads the developer key in the browser to sign requests, and can also pick a directory and sign and package it directly; it covers registration, downloading the certificate, submitting app packages, and viewing review status and crash reports; the review desk uses staff tokens to verify developers, review submissions, handle reports, take apps down and approve device attestation) |

## 4. App package `phoneapp/1`

Implemented in `pkg` (packaging, signature verification) and `shell/install.go` (install service); for the install policy see [22](22-developer-api.md) §12.

```
notes.phoneapp                 # tar
├── manifest.json
├── META/signature.json        # SHA-256 of every file + developer Ed25519 signature + developer certificate issued by the store
├── main / main.js / ...       # phone.Ui program in any language (Go SDK, Python, mini-program)
└── icon.png
```

| `manifest.json` field | Meaning |
|----------------------|------|
| `id` | Reverse domain name, e.g. `com.example.notes` (IDs starting with `system.` are reserved for the system) |
| `name`, `description`, `version` | Display name, short description, version |
| `exec`, `args` | Entry program and its arguments |
| `permissions` | Capabilities the app uses, e.g. `contacts.read`, `network`, `camera`; `system` and `install` are only for apps in the system image, and the installer rejects them |
| `background` | Background mode, e.g. `voip` (not recommended; messages and incoming calls should rely on push) |
| `share` | Types received from the share panel: `text`, `image` |
| `icon`, `icon_image` | Icon character for the text UI, PNG for the pixel UI (≤ 256×256) |

Rules:
- **Install**: signature and certificate chain verification → manifest checks (app ID format, no system permissions declared, no pre-granted permissions) → system confirmation dialog (listing the source and the permissions) → the code is unpacked into `packages/<id>/` (read-only for the app), and the data lives in `apps/<id>/`.
- **Update**: must be signed by the same developer. An update the store installs in the background (automatic updates) must also have a higher version and request no more permissions; otherwise it waits for the user to select it.
- **Before launch**: the signed manifest is stored with the code; on the first launch after boot, and after developer mode changes, every file is re-checked against the store roots trusted at that moment; an app with modified files, extra files, or a store that is no longer trusted is not allowed to launch.
- **Uninstall**: deletes the code and the data; revokes grants and usage records, scheduled tasks, alarms and photo grants; deletes all of this app's keys in the key service; and invalidates its push tokens ([21](21-push.md)).
- **Developer mode** (a toggle in Settings): only while it is on does the device trust the development store, so that packages signed by it can be installed and run.
- **Trusted stores**: the store roots shipped in the image (`share/phone/trust`) and the store roots provisioned at the factory (`factory/stores`) are always trusted; the development store is trusted only in developer mode.

### 4.1 Store backend: open core and simulated backend

The store backend has two layers (D11). The open core is the `appstore` package, which implements every interface that devices and developers deal with (table below); `cmd/phone-storesrv` is a simulated backend built on it, which, like the simulated push gateway, runs next to the device for device testing. The officially operated store backend is not open source; it uses the same core plus what operations need: a separate token and audit log for each staff member, the rules for the APP filing number, report handling, network logs, a web catalog for humans to read, device attestation and factory issuance, an interface the payment backend uses to check devices, and the developer portal and staff review desk (63 (internal document)). The core leaves extension points for this: who counts as staff (`Staff`), auditing (`Audit`), extra rules for submissions (`Accept`), extra columns in the review queue (`Queue`), and routes can be added on `Routes()`. The developer-side tool is `cmd/phone-pkg`.

| Stage | How it works |
|------|------|
| Developer registration and identity verification | `phone-pkg keygen` generates an Ed25519 key; `phone-pkg register` submits the public key, the name and an ID number: companies enter their unified social credit code (GB 32100, MOD 31 check character), individuals their resident ID card number (GB 11643, MOD 11-2 check digit). A wrong check digit is rejected on the spot; a correct one is recorded as 「待核验」 (Pending verification), and only the masked number is stored. Only after store staff have verified it (`POST /v1/admin/developers/<name>`; in mass production this connects to a business registration or ID card verification service, **not implemented**) does the store root issue the developer certificate; run `register` once more to fetch the certificate. A registration under the same name with a different public key is rejected |
| Packaging | `phone-pkg build` signs and packages; for mini-programs this step compiles `app.ts` / `app.tsx` into `app.js`, so only JavaScript runs on the device. The web portal can also pick the app's directory and sign and package it in the browser (same format; the browser does not expose file permissions, so the file that `exec` in the manifest points to is marked executable; TypeScript mini-programs still use `phone-pkg`) |
| Submission and review | `phone-pkg submit`. Automatic checks come first: the signature chains to this store's root, the developer is verified, the manifest is valid, the app ID does not use a system prefix, it declares neither `system` nor `install`, it pre-grants no permissions, and it has never been taken down; an update must come from the same developer and have a higher version. **New apps** and **updates that request new permissions** go into the manual review queue (`GET /v1/admin/queue`; staff approve with `POST /v1/admin/reviews/<id>/<version>` or reject with a reason); other updates are listed directly |
| Listing and download | Once approved, the app enters the catalog (`GET /v1/catalog`, signed by the store root, with an issue time: `phoneapp-catalog/1` signs the store name, the issue time and the raw JSON of the app list); only the package of the latest version is served (`GET /v1/packages/<id>/<version>`; for an app in a staged rollout, the previous version as well). The store app hands the fetched catalog to the system (`pkg.catalog`): the system verifies the signature against the trusted store roots and rejects catalogs issued more than 7 days ago or older than the last one received from this store (so a mirror or CDN can neither alter the developer, filing number or version, nor hide updates); only if it passes does it return the app list. The store app lists the catalog at the bottom of the page: 「安装」 (Install) for apps not yet installed, 「更新到 x」 (Update to x) for apps with a new version; after downloading it checks the SHA-256 from the catalog and then hands the package to the system installer, which verifies the signature and asks for confirmation as usual, with the source shown as 「应用商店（已审核）」 (App store (reviewed)) |
| Delta updates | When updating, the store app sends the installed version (`?from=1.0`); if the store still has that version, it replies with a delta package (response header `X-Phoneapp-Delta`): the complete signed manifest of the new version plus the files whose digests changed. The system fills in the rest from the installed files (checking each one against the digest in the signed manifest) and gets an ordinary complete package, which is fully verified as usual and then checked against the SHA-256 in the catalog (the reassembled result is byte-for-byte identical to the complete package). So a delta package needs no signature of its own and cannot smuggle in files the developer did not sign; if the installed version is not the one it starts from, it is rejected. The confirmation dialog notes 「增量 N KB」 (Delta N KB) |
| Staged rollout | An update can go to only some of the devices at first: `phone-pkg submit -rollout 10` (`POST /v1/submissions?rollout=10`). In the catalog the app then carries `rollout` (a percentage) and `previous` (the previous version, which everyone gets: version, size, SHA-256, permissions), and the packages of both versions stay available for download. Each device decides for itself whether it is in this batch: the system computes it once from a bucket stored only on the device (the same one as for system updates, 0–9999) mixed with the app ID; a device outside the batch gets the previous version from `pkg.catalog`, and the store does not know which devices got the new version. The developer uses `phone-pkg rollout` (`POST /v1/developer/rollout`, with the name, the time and a signature over `phone-rollout/1\|<name>\|<time>\|<app>\|<version>\|<percent>`) to raise the percentage, set it to 0 to pause, or set it to 100 to release to all devices. If 10% of the devices that downloaded a version in a staged rollout have reported crashes (at least 5 reports), the backend sets the percentage to 0 and records the reason (`halted`), and waits for the developer to deal with it |
| Automatic updates | The store app registers two background jobs ([22](22-developer-api.md) §4.7): fetching the revocation list every 6 hours, and checking for updates every 6 hours while charging (a "charging only" job gets 1 minute per run, longer than the 3 seconds of an ordinary wake-up). An update it finds is handed to the system installer (`pkg.install`) with `auto`, and the system installs it without asking only when that changes nothing the user agreed to: the app is installed, the developer is the same, the version is higher (an old signed package cannot be slipped back in), no more permissions are requested, and the app is neither in the foreground nor working in the background (a call, playback); an app that is frozen or idle is ended first and then replaced. Everything else is answered "needs the user" and stays in the store as 「可更新」 (Update available) until the user selects it. Once installed, the store posts a 「已自动更新」 ("updated automatically") notification. It can be turned off in the store's app settings (「自动更新应用（充电时）」 (Update apps automatically (while charging))) |
| Takedown | Staff call `POST /v1/admin/takedowns/<id>` (a reason is required): the app leaves the catalog and goes onto the revocation list (`GET /v1/revocations`, signed by the store root, with an issue time). When the store app refreshes, and in a background job (every 6 hours, without the store being opened), it fetches the list and hands it to the system (`pkg.revocations`); the system verifies the signature against the trusted store roots and ignores a list older than the one it holds (replay protection), then refuses to launch installed apps on the list (wake-ups by push and scheduled tasks included) and ends the ones that are running; opening such an app shows 「已被商店下架：理由」 (Taken down by the store: reason). An app ID that was taken down cannot be submitted again |
| Crash reports | In 设置 › 关于本机 › 应用崩溃记录 (Settings › About phone › App crash log), select an entry and press 「发送给开发者」 (Send to developer): a system dialog explains what will be sent (cause, version, time, the last few log lines), and the report is sent only after confirmation; only apps installed from the store can send reports. Only the app's developer can read them (`phone-pkg crashes`): the request carries the developer name, the time, and a signature made with the developer key over `phone-crashes/1\|<app>\|<time>`; the backend checks that the developer is verified, the app belongs to them, the signature is valid and the time is within five minutes |
| Developers viewing their own status | `GET /v1/developer`: the request carries the developer name (may be percent-encoded, since browser request headers can only hold Latin-1), the time, and a signature over `phone-dev/1\|<name>\|<time>`; the reply has the verification status, the certificate, the listed apps and the apps waiting for manual review; nobody can see anyone else's |
| State | Developers, catalog, review queue, revocation records and reports are stored in `state.json` in the data directory, app packages in `packages/`; everything is written to a temporary file first and then renamed into place, so a crash or a full disk leaves the old version behind, never a half-written one |
| Public disclosure and filing number | A company developer's unified social credit code is public information: it is stored in full and published with the app (`developer_credit` in the catalog); for individuals only the masked ID card number is stored. The manifest can carry `filing` (the APP filing number), which is published with the app; the official backend checks its format, and a public store in China also makes it mandatory. In the store app every app has a 「详情」 (Details) page: developer, credit code, filing number, description, permissions |
| Reports | 「举报」 (Report) on the app details page: `POST /v1/reports` (app, reason; at most 20 per hour from the same address); staff see unhandled reports in the review queue (handling them and recording who handled them is done in the official backend) |
| Device trust | On startup the backend writes the store root public key into the device's `factory/stores/`, standing in for factory provisioning |

**Not implemented**: paid developer accounts; connecting real business registration / ID card verification services and phone number verification (combined real-name verification); putting the store root and package signing into an HSM; notifying reporters of the outcome of their reports; a page in the developer portal for adjusting the rollout percentage (for now there is only `phone-pkg rollout`); halting on signals other than crashes (uninstall rate, bad reviews).
- **Limits**: package ≤ 64 MB, ≤ 1000 files; paths must not escape the package directory, and link files are not allowed.
- **Lifecycle**: initialize → foreground → background (frozen after 10 seconds) → destroy; a single foreground app, the others suspended ([17](17-app-sandbox.md) §5).

## 5. API layers

```
App (any language) ──phone.Ui (JSON lines)──► Shell: rendering · keys · capability broker
                                              │  forwarded to system services after the permission check
                                              ▼
                      telephonyd · audiod · mediad · pushd · keystored · …
```

Apps can reach system capabilities only through the Shell's capability broker ([22](22-developer-api.md)); third parties are **forbidden** to connect directly to ModemManager or to any system service, and the sandbox does not let them either ([17](17-app-sandbox.md)).

## 6. App store

- **Chain of trust**: store root key → developer certificate → package signed by the developer; the store root is built into the device, and in developer mode the development store's root is trusted as well.
- **Store client**: the platform's 「应用商店」 (App store) is the open-source reference implementation and uses `pkg.install` / `pkg.uninstall` / `pkg.list`, which only system apps can use. The official client is closed source and is only installed on certified devices.
- **Backend**: for the open core and the simulated backend see §4.1 (catalog, review, delta updates, takedown, crash reports); the official backend is not open source; in mass production packages are kept in object storage; payment is **not implemented**.

## 7. Developer deliverables

| Deliverable | Status |
|--------|------|
| Go SDK, protocol documentation, capability catalog | ✓ ([22](22-developer-api.md)) |
| Sample app `examples/notes` and the packaging command | ✓ |
| Compatibility test suite (CTS) and signed report | ✓ (`make cts`, `phone-cts-verify`) |
| C ABI SDK (`sdk/c`) and C++ sample | ✓ |
| Developer portal | Web portal and staff review desk ✓ (not open source, with their own end-to-end tests) |
