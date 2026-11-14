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
| Stop the Running App | ends the Shell the panel shows |
| Run Tests | `kario test DIR` in a terminal (`test/*.test.ts` against a fake Shell) |
| Package (.phoneapp) | `kario package` with the developer's key and certificate, or a development store |
| Publish to the Store | packages, then `kario publish -store URL FILE` (the store's `POST /v1/submissions`) |
| Add a Debug Configuration | writes an attach configuration to `.vscode/launch.json` and the task that starts the app to `.vscode/tasks.json` (F5) |

Settings are under `kario.*`: the `kario` program (`kario.cli`, e.g. `build/bin/kario` of a
Kario tree after `make build`), the form factor, the language, the addresses of the preview and the
inspector, signing, the store.

## Building it

```bash
npm ci            # typescript, @types/vscode, @types/node: dev only, Apache-2.0 / MIT
npm test          # tsc, then node --test: the extension's logic and the packager
npm run vsix      # kario-studio-0.1.0.vsix, without @vscode/vsce
code --install-extension kario-studio-0.1.0.vsix   # or: cursor --install-extension …
```

`npm run vsix` writes the package with `scripts/vsix.mjs` (no dependencies) rather than
`@vscode/vsce`, whose `@vscode/vsce-sign` is not under an open licence; `npx @vscode/vsce package`
makes an equivalent package for those who accept it.
