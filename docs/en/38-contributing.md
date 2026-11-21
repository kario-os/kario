> English translation of [the Chinese original](../38-contributing.md). The Chinese text is authoritative; keep both in sync.

# Contributing and the pull request pipeline

> A pull request from submission to merge: what to do before submitting, which checks and tests CI runs for which change, the one gate check `pr-ok`, the AI review report once CI has passed, code scanning, and how to run the same checks on your machine. The conventions themselves are in [CONTRIBUTING.md](../../CONTRIBUTING.md).
> Status: run on the developer machine (2026-11-16, RHEL 8, x86-64): actionlint over the four workflows (five from 2026-11-18, with `fuzz`); `scripts/ci_plan_test.py`, `scripts/ai_review_test.py` (a fake GitHub API, a fake model service) and `scripts/yocto_check_test.py`; `scripts/yocto-check.py` and the repository's check scripts; `scripts/ci-plan.py` over synthetic commits in a temporary clone. On GitHub: the `platform` workflow passed when run by hand (2026-11-18, `a3857c8`); the pull request trigger has not run yet, and no real model has produced the AI report (with no pull request it was skipped): these two are **not verified**.

## 1. Before you open a pull request

- Sign off every commit for the DCO: `git commit -s` (checked by `scripts/check-dco.sh`).
- Code is English, comments included; Chinese only in data (locale catalogs, manifests, selftest scripts, `sku/`, `testdata/`, `docs/`). UI text is English messages passed through `tr.T` and the other translation calls; a new message needs its zh-Hans translation, zh-Hant and es may follow later ([22](22-developer-api.md) §9.1).
- Names the system and apps share are constants in `ui/` and `svc/`; a change to package `ui` regenerates the TypeScript types (`go run ./tools/tsgen`).
- A bug fix comes with a test that fails without it; the layers of testing, how to write selftest cases and what runs before publishing are in [40](40-testing.md).
- Documents change with the code: Chinese in `docs/`, the English translation under the same name in `docs/en/`, with the same structure; what is planned or simulated is marked **未实现** (not implemented), **未上真机** (not on a real phone), **未验证** (not verified).
- The rules the maintainers apply are in [.github/ai-review/rules.md](../../.github/ai-review/rules.md); the AI review report (§5) uses them too.

## 2. The pipeline

| Workflow | When it runs | What it does |
|--------|-----------|--------|
| `platform` ([ci.yml](../../.github/workflows/ci.yml)) | Pull requests; by hand (after publishing to main: `gh workflow run platform`) | The plan → fast checks → the builds and tests the plan asks for → the gate `pr-ok` |
| `codeql` ([codeql.yml](../../.github/workflows/codeql.yml)) | Pull requests touching Go, JavaScript / TypeScript or workflows; by hand (scanning main, which the Security tab's alerts are about) | Code scanning (§6) |
| `ai-review` ([ai-review.yml](../../.github/workflows/ai-review.yml)) | After `platform` passed on a pull request; by hand | The AI review report (§5) |
| `release` ([release.yml](../../.github/workflows/release.yml)) | By hand | Publishes a release |
| `fuzz` ([fuzz.yml](../../.github/workflows/fuzz.yml)) | By hand only (Actions › fuzz › Run workflow; `targets` is a regular expression on `make fuzz-list`'s names, `minutes` the minutes per target, 10 by default) | One job per fuzz target: `make fuzz`'s (Go's own, and the Go drivers of the C/C++ harnesses built with ASan and UBSan) and `make fuzz-libfuzzer`'s (the libFuzzer builds of the same harnesses); each target's corpus kept in the cache to carry on from, failing inputs uploaded as artifacts and failing the run ([17](17-app-sandbox.md) §9.2); run once by hand on GitHub on 2026-11-19 (`targets` `libfuzzer|cfuzz`, a minute each), passed |

Only the newest 3 runs of each workflow are kept (the Pages deployments too): the last job of `platform` and of `fuzz`, `prune`, deletes the older ones with their logs and artifacts (`scripts/ci-prune.sh`; kario-internal's `internal` does the same). By hand: `sh scripts/ci-prune.sh [-n] kario-os/kario 3`, `-n` only lists.

The jobs of `platform`:

```
plan -> checks -> go, telephonyd, mm, capi, sku, paint, gui, ts, selftest, pixel, yocto
        dco (pull requests only)       (each runs only when the plan needs it)
all of the above -> pr-ok              (the one check branch protection requires)
pr-ok -> prune                         (only the newest 3 runs of each workflow stay)
platform passed on a pull request -> ai-review (workflow_run) -> one comment on the pull request
```

- `plan` runs `scripts/ci-plan.py`: a pull request's checkout is its merge commit, which it compares with its first parent (the base it merges into), and it decides which areas run and which Go packages are tested, as the job's outputs and its job summary.
- `checks` always runs and ends within minutes: the repository's check scripts (`no-chinese`, `i18n-check`, `const-check`, `license-check`, `docs-links`, `docs-en-check`, `gomodinc.py --check`), the CI scripts' own tests, gofmt of the changed Go files, `go vet -composites=false` of the planned Go packages, shellcheck of the changed shell scripts (an error fails; a new script may not have warnings either), actionlint when workflows change, rustfmt when `paint/` or `gui/` change (the two crates are not rustfmt-formatted yet, so it only warns; it is enforced for a directory once it has a `rustfmt.toml`). `dco` checks that every commit of the pull request is signed off.
- The build and test jobs start only after `checks` passed: a pull request with formatting errors does not take up the test machines.
- Caches: Go modules and the build cache (`setup-go`), cargo's registry and `target/` (by `Cargo.lock`), npm (`setup-node`). A new push to the same pull request cancels its older run. Each workflow has only `contents: read` by default.
- The end-to-end test of `go test ./shell` (`TestSelftestScenario`) splits the selftest script by its `#part <name>` lines, and runs the parts at once, each in a child process of its own on a stack of its own; the tests that start a Shell in the process run in a child process each too (a Shell sets the process's `time.Local`), and phone migration listens on a free port there instead of 47800. On the development machine (2026-11-19, 48 cores, load 4 to 7) the whole package went from 627 to 155–163 seconds (168 on 4 cores), the longest part being `chat` at about 142; `make selftest` still runs the whole script in order. A new selftest case belongs to the nearest `#part` line above it; one that needs what a case of another part left (a paired headset, a call in the log, a photo) goes into that part.

## 3. What runs for which change

| Change | What runs |
|------|--------|
| Documents only (`docs/`, `*.md`, `LICENSE`) | Only `checks`; `docs/22-developer-api.md` and `docs/23-renderer-protocol.md` are read by the `shell` tests, so a change to them also runs `go test ./shell` |
| Go source | go: the changed packages and every package that depends on them (the import graph from `go list`, test imports included); when a package is linked into the programs `make build` builds, also selftest, cts, pixel and the `shell` end-to-end tests |
| Only `_test.go` or `testdata/` | That package's tests |
| Files `make build` installs into the system: `apps/`, `examples/`, `web/`, `icons/`, `profile.d/`, `bench/`, `brand.json`, `audio/testdata/`, and files packages embed | selftest, cts, pixel, `go test ./shell`, and the tests of the packages that read those files |
| `jsrt/src/` (kario-jsrt's C source) | go (`make jsrt` first; its tests may not skip), selftest, cts, pixel |
| `telephonyd/` | telephonyd (build and unit test); `src/` also selftest, cts, pixel; the ModemManager backend's files (`src/mm*`, `test/mock_mm.py`, `test/mm_test.py`, `test/Containerfile`, `CMakeLists.txt`) also mm (`make mm-selftest`, in Podman) |
| `sdk/c/` | capi, selftest, cts, pixel |
| `paint/` | paint (clippy, `cargo test`) and pixel; `Cargo.lock` also yocto (the recipe's crate list) |
| `gui/` | gui (clippy, build, `cargo test`) |
| TypeScript: `sdk/ts/`, `ide/`, directories with a `tsconfig.json` | ts (Kario Studio's `npm test`, `tsc -p` of each TypeScript project) |
| `sku/`, `tools/sku.py` | sku (`make sku-check`) |
| `yocto/` | yocto: `scripts/yocto-check.py` (recipes where `layer.conf` finds them, licence fields, the layer's classes, crate lists, Go packages named by `GO_INSTALL` that are more than tests, kas files, Python, shellcheck) `yocto/abslot-test.py` (the OnePlus 6T's A/B boot slot marker `phone-abslot`, on disk images), `yocto/fajita-flash-test.py` (the 6T's flashing, backup and restore, against a simulated phone) and `yocto/btvirt-test.py` (the emulator's virtual Bluetooth controllers, btvirt, with the layer's BlueZ patches, under AddressSanitizer; BlueZ's release tarball from kernel.org), no image build |
| `Makefile`, `go.mod`, `go.sum`, `scripts/`, `.github/`, or a file no rule knows | Everything |
| A run by hand | Everything |

The rules are the `RULES` table of `scripts/ci-plan.py`. When a Go test reads files in another directory (like the two documents the `shell` tests read), the table needs a line for it; `scripts/ci_plan_test.py` scans the Go tests for paths starting with `../` and fails when the table lacks one.

## 4. The gate check pr-ok

A job the plan does not need is skipped. GitHub counts a skipped job as passed, and so is a job skipped because a job before it failed, so the individual jobs cannot simply be made required checks. `pr-ok` depends on every job, always runs, and passes only when all three hold:

- no job failed or was cancelled;
- `plan` and `checks` succeeded;
- every job the plan asked for (the `jobs` output of `plan`) succeeded rather than being skipped.

Branch protection requires this one check, `pr-ok` (§8, **the owner's to do**). The old `test` and `docs` jobs no longer exist; left among the required checks, they would keep every pull request waiting.

## 5. The AI review report

Once CI has passed, a model reads the pull request and writes an advisory report, posted as one comment on the pull request. It is advice generated by AI, not a decision by the maintainers.

- When it runs: after `platform` passed on a pull request (`pr-ok` succeeded), triggered by `workflow_run`. Maintainers can generate it again by hand: Actions › ai-review › Run workflow, with the pull request's number and, if needed, a larger token budget.
- It never runs the pull request's code: it runs in this repository's context (with access to the secret, for pull requests from forks too), so it checks out only the default branch's `scripts/` and `.github/`, and fetches the pull request's title, description, changed files, diff and the 20 lines around each change (the files at the head commit) from the API as data. Its permissions are only `contents: read` and `pull-requests: write`.
- Finding the pull request: for a fork the event carries no number, so it looks it up by the fork's `owner:branch`, then by commit, then by search, and checks the head commit.
- One comment per pull request: a hidden HTML marker in the comment records the head commit; a new commit updates the comment, and a commit that already has a report gets none again. Only a marker written by `github-actions[bot]` counts. Drafts, runs superseded by a newer commit and runs that did not pass are not reviewed.
- Content: six sections — what the pull request does; how it fits the existing code (citing `path:line`); correctness and safety concerns by severity; what the tests cover and what is missing; documents, translations and conventions; a recommendation (merge / merge after changes / needs discussion), labelled as AI advice. It is written in the language of `AI_REVIEW_LANG`. The review follows [.github/ai-review/rules.md](../../.github/ai-review/rules.md) and `CONTRIBUTING.md` (both from the default branch).
- Against prompt injection: the pull request's content sits between delimiters carrying a random number, and the system prompt tells the model to disregard any instruction inside them; the model has no tools; its answer is only text to post: @-mentions and HTML comments are removed, the length is capped, and a secret, should one appear, is replaced.
- Cost: generated files (`sdk/ts/ui.d.ts` and the like), vendored files, lockfiles and binaries are listed by name only; each file's diff and context are capped; when the whole diff exceeds the token budget the model is not called and a short note is posted instead.

| Name | Kind | Default | What for |
|------|------|------|------|
| `AI_REVIEW_API_KEY` | Secret | None: without it no report is posted, and the job still succeeds | The API key of the `AI_REVIEW_PROVIDER` provider |
| `AI_REVIEW_PROVIDER` | Variable | `deepseek` | The provider: `deepseek`, `qwen` (Alibaba Cloud Model Studio), `kimi` (Moonshot), `glm` (Zhipu), `openai`, `anthropic`, `gemini`, `openrouter`; it sets the address, the API and the default model (`deepseek-flash`, `qwen3.7-plus`, `kimi-k3`, `glm-5.3`, `chat-latest`, `claude-sonnet-5-5`, `gemini-flash-latest`; `openrouter` has none, so set `AI_REVIEW_MODEL`). The default models are the providers' recommended names in 2026-11 |
| `AI_REVIEW_BASE_URL` | Variable | The provider's | Optional: another address for the provider (a gateway of your own, another region); must be https |
| `AI_REVIEW_MODEL` | Variable | The provider's default | Optional: another model |
| `AI_REVIEW_LANG` | Variable | `Simplified Chinese` | The report's language, its name written in English |
| `AI_REVIEW_TOKEN_BUDGET` | Variable | `60000` | The estimated token limit of one review; above it only the note is posted |

## 6. Code scanning (CodeQL)

- Go, JavaScript / TypeScript and the workflows themselves (Actions): a pull request is scanned in the languages it touches (`ci-plan.py` names them); a run by hand scans all of them.
- C / C++ (telephonyd, the C SDK, kario-jsrt's own sources) are scanned in runs by hand only: they need a traced build, not worth making every pull request wait for; the parsers of untrusted input among these components already have fuzz targets (`make fuzz`). QuickJS-ng is built before tracing starts, so the results cover only this repository's code. CodeQL's support for GCC 14 and C++23 has not been run here: **not verified**.
- Not scanned: `vendor/`, `build/`, `node_modules`, `testdata/` (which holds hostile input on purpose), generated files, the Mermaid bundle ([.github/codeql/codeql-config.yml](../../.github/codeql/codeql-config.yml)).
- The results are under Security › Code scanning and appear as annotations on the pull request; `pr-ok` does not wait for them. Code scanning has to be switched on for the repository with this workflow (advanced setup), and the default setup switched off (§8, **the owner's to do**).

## 7. Running the same checks on your machine

```bash
python3 scripts/ci-plan.py --base origin/main              # this branch, uncommitted changes included: what CI runs
python3 scripts/ci-plan.py --base origin/main --head HEAD  # the commits only
make vendor
python3 scripts/no-chinese.py && python3 scripts/i18n-check.py && python3 scripts/const-check.py
python3 scripts/license-check.py && python3 scripts/docs-links.py docs && python3 scripts/docs-en-check.py
python3 -m unittest scripts/ci_plan_test.py scripts/ai_review_test.py scripts/yocto_check_test.py
python3 scripts/yocto-check.py
GITHUB_REPOSITORY=kario-os/kario python3 scripts/ai-review.py --pr <number> --dry-run  # the prompt, no model, no comment
```

The summary of `ci-plan.py` lists how to run each area it plans on your machine (for example `make selftest`, `go test` with the package names, `make pixel-selftest`). `make test` is still all of the unit tests and checks. `--dry-run` needs no key; without `GITHUB_TOKEN` it uses GitHub's anonymous API (rate-limited).

## 8. Repository settings (the owner's to do)

| Setting | Where | How |
|------|--------|--------|
| Require only `pr-ok` | Settings › Rules › Rulesets (or Branches) › `main` | Require status checks to pass: `pr-ok` (source GitHub Actions); remove the old `test`, `pixel`, `gui`, `docs`, `dco` |
| Approve fork workflows | Settings › Actions › General › Approval for running fork pull request workflows | At least first-time contributors; with many outside pull requests, all outside collaborators (the AI report runs only after CI passed, so this also bounds its cost) |
| Read-only token by default | Settings › Actions › General › Workflow permissions | Read repository contents and packages permissions (workflows that write declare it themselves) |
| The AI review report | Settings › Secrets and variables › Actions | The variable `AI_REVIEW_PROVIDER` and the secret `AI_REVIEW_API_KEY`; the other variables are optional (§5) |
| Code scanning | Settings › Code security › Code scanning | Use `codeql.yml` (advanced setup), switch the default setup off |

## 9. Limits

- Choosing tests by path is an approximation. The Go part comes from the import graph; files elsewhere that tests read or run at run time depend on the table in `ci-plan.py`, and the scan only recognises paths starting with `../`. What slips through is caught by the full run by hand (once after publishing to main).
- A pull request can change `.github/` and `scripts/`, and so change how CI judges it. Such pull requests always run everything and the AI report points them out, but the maintainers have to review those changes themselves.
- rustfmt only warns for `paint/` and `gui/`; shellcheck only stops errors in existing scripts (they have warnings now).
- The `mm` job pulls `debian:trixie` from Docker Hub, which rate-limits anonymous pulls.
- The only third-party tool is actionlint (downloaded by version, its sha256 checked). `actions/*` and `github/codeql-action` use major version tags, as the earlier workflows did, not pinned commits.
- Not in CI: the Yocto image and the tests in QEMU, `web-selftest`, `gui-selftest`, `cargo test` of `sdk/rust/kario-native` (the `jsrt` tests only build its example module). `make fuzz` runs only in the `fuzz` workflow, by hand; pull requests do not build the libFuzzer harnesses.
- The AI report can be wrong or miss things; it is advice for the maintainers.
