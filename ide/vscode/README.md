# Kario Studio

The VS Code / Cursor extension for Kario TypeScript apps ([docs/29](../../docs/en/29-app-development.md) §3,
the tutorial in [docs/34](../../docs/en/34-app-tutorial.md)). It drives the `kario` command line;
it has no build or run logic of its own.

| Command (`Kario: …`) | What it runs |
|------|------|
| New App | `kario new DIR`, then opens the folder |
| Build | `kario build -minify=false -sourcemap DIR`; errors go to the Problems view |
| Run in the Terminal | `kario run DIR`: the developer machine's Shell in a terminal (text UI) |
| Run in the Preview Panel | `kario run -preview ADDR -watch DIR`: the Shell's pixel UI (phone-paint) in a panel; the panel's keyboard is the keypad; on save the app is built again and restarted on its pages (setting `kario.reloadOnSave`) |
| Debug (Node.js) | the same with `-inspect ADDR -brk`, then VS Code's Node.js debugger attaches: breakpoints in `src/*.ts` |
| Debug on the Phone (Developer Mode) / Debug the App Already on the Phone | a session of the debug type `kario`, whose adapter is `kario dap`: builds the project unminified, installs it signed by the development store (launch) and restarts it under kario-jsrt's debugger over the trusted cable; breakpoints in `src/*.ts` through the source map. launch.json: `{"type": "kario", "request": "launch", "project": "${workspaceFolder}"}` |
| Stop the Running App | ends the Shell the panel shows |
| Run Tests | `kario test DIR` in a terminal (`test/*.test.ts` against a fake Shell) |
| Choose the Device (SKU or Form Factor) | a pick of `kario skus -json` and the three form factors, kept in `kario.sku` / `kario.device` and shown in the status bar; runs and previews use it (`-sku` or `-device`) |
| Edit the Manifest | a form over `manifest.json` (permissions, purposes, privacy label, id, name, version, description, icon, language), also offered by "Reopen Editor With…"; every change is written back and checked by `kario manifest check -json -`, its problems beside their fields |
| Preview the Views / … on Every Form Factor | `kario preview -json DIR`: the pages `src/previews.ts` names (KarioUI's `preview()`), drawn without running the app, in a panel; drawn again when a file under `src/` is saved |
| Run with the Performance Timeline (Perfetto) | the preview panel with `kario run -profile DIR/.kario/profile/trace.json`; when stopped, buttons open Perfetto, the trace or the JavaScript CPU profile |
| Package (.phoneapp) | `kario package` with the developer's key and certificate, or a development store |
| Publish to the Store | packages, then `kario publish -store URL -key KEY FILE` (the store's `POST /v1/submissions`, then the symbol file `kario package` wrote beside the package) |
| Add a Debug Configuration | writes an attach configuration to `.vscode/launch.json` and the task that starts the app to `.vscode/tasks.json` (F5) |

Settings are under `kario.*`: the `kario` program (`kario.cli`: the default `kario` is the link
`make install-kario` puts into `~/.local/bin`; or `build/bin/kario` of a Kario tree after
`make build`), the form factor, the language, the addresses of the preview and the inspector,
signing, the store. The shortest path from installing to an app in the preview panel is
[docs/35](../../docs/en/35-quickstart.md) §2.

## Building it

```bash
npm ci            # typescript, @types/vscode, @types/node, @vscode/test-electron: dev only, Apache-2.0 / MIT
npm test          # tsc, then node --test: the extension's logic, the manifest form (against kario built with Go) and the packager
npm run e2e       # the commands clicked through in VSCodium (below)
npm run vsix      # kario-studio-0.1.0.vsix, without @vscode/vsce
code --install-extension kario-studio-0.1.0.vsix   # or: cursor --install-extension …
```

In a Cursor (or VS Code) remote session over SSH, run `cursor --install-extension "$PWD/kario-studio-0.1.0.vsix"`
in the editor's terminal: that `cursor` is the remote server's, and the extension goes into
`~/.cursor-server/extensions` on the remote machine, where it runs. "Install from VSIX…" in the
Extensions view does the same.

`npm run vsix` writes the package with `scripts/vsix.mjs` (no dependencies) rather than
`@vscode/vsce`, whose `@vscode/vsce-sign` is not under an open licence; `npx @vscode/vsce package`
makes an equivalent package for those who accept it.

## End to end in an editor

`npm run e2e` (`scripts/e2e.mjs`, `test/e2e/index.ts`) starts a real editor with
`@vscode/test-electron` (MIT) and goes through the extension's own commands: New App (its dialogs
answered by the test), Build, Edit the Manifest, Run in the Preview Panel (a frame, then OK changes
it), Preview the Views, Run with the Performance Timeline, Package. The editor is VSCodium, the
MIT-licensed build of VS Code's sources (Microsoft's VS Code binaries are not under an open
licence): version 1.143.17232 is downloaded once into `~/.cache/kario/vscodium` and checked against
the release's SHA-256, for the test only (`KARIO_E2E_EDITOR` names another). Without a display it
runs itself under `xvfb-run`. It uses `KARIO_CLI` (default: the Kario tree's `build/bin/kario`
after `make build`), which finds the Shell and phone-paint (`make paint`) as it does for a user;
`KARIO_SHELL` and `KARIO_RENDERER` name others. Debugging with breakpoints is not part of it.
