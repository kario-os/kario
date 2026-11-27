> English translation of [the Chinese original](../38-contributing.md). The Chinese text is authoritative; keep both in sync.

# Contributing and the pull request pipeline

> A pull request from submission to merge: what to do before submitting, which checks and tests CI runs for which change, the one gate check `pr-ok`, the AI review report once CI has passed, code scanning, and how to run the same checks on your machine. The conventions themselves are in [CONTRIBUTING.md](../../CONTRIBUTING.md).
> Status: run on the developer machine (2026-11-16, RHEL 8, x86-64): actionlint over the four workflows (five from 2026-11-18, with `fuzz`; six from 2026-11-23, with `coverage`); `scripts/ci_plan_test.py`, `scripts/ai_review_test.py` (a fake GitHub API, a fake model service) and `scripts/yocto_check_test.py` (from 2026-11-21 also `scripts/yocto_jobs_test.py`, `scripts/doctor_test.py` and `tools/gomodinc_test.py`, from 2026-11-23 `scripts/coverage_test.py`, run on the developer machine, not yet in CI); `scripts/yocto-check.py` and the repository's check scripts; `scripts/ci-plan.py` over synthetic commits in a temporary clone. On GitHub: the `platform` workflow passed when run by hand (2026-11-18, `a3857c8`); the pull request trigger has not run yet, and no real model has produced the AI report (with no pull request it was skipped): these two are **not verified**.

## 1. Before you open a pull request

- Sign off every commit for the DCO: `git commit -s` (checked by `scripts/check-dco.sh`).
- Code is English, comments included; Chinese only in data (locale catalogs, manifests, selftest scripts, `sku/`, `testdata/`, `docs/`). UI text is English messages passed through `tr.T` and the other translation calls; a new message needs its zh-Hans translation, zh-Hant and es may follow later ([22](22-developer-api.md) §9.1).
- Names the system and apps share are constants in `ui/` and `svc/`; a change to package `ui` regenerates the TypeScript types (`go run ./tools/tsgen`).
- A change to the modules in `go.mod` or `go.sum`: `make gomods` writes the Yocto recipe's Go module list again, committed with them (it needs the network and a Go no older than go.mod's `go` line). `gomodinc.py --check` in `checks` looks at the modules only, so `go mod tidy` moving comments and `// indirect` does not count; if you forget, `make yocto-build` writes the list itself first ([13](13-prototype.md) §5.1).
- Build products stay out of git: a program a `go build` left in a source directory is deleted or named in `.gitignore`. `scripts/binary-check.py` (`make test`, `checks`, `make publish`) looks at the files a publish commits (the tracked ones and the untracked ones not ignored) and fails when one is a program (starting as ELF, Mach-O or PE does) or larger than 512 KiB; a big file that does belong goes into its `BIG` with a ceiling and why (now only the pinyin dictionary `ime/data/zh.tsv.gz`, about 620 KiB, ceiling 1 MiB). 512 KiB is above the largest legitimate file of each kind (the sample video 300 KiB, `shell/selftest.script` 230 KiB, a sample photo 210 KiB, a picture in `docs/img` 193 KiB) and below the smallest Go program (1.5 MiB empty) and phone-paint (2.2 MiB).
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
| `coverage` ([coverage.yml](../../.github/workflows/coverage.yml)) | By hand only (Actions › coverage › Run workflow; `parts` is `make coverage`'s parts, empty for all) | One job running `make coverage` ([40](40-testing.md) §7): the coverage of the Go unit tests and the whole text selftest merged, of the tests of phone-paint, telephonyd and the C SDK, and of the JavaScript under Node.js; the report uploaded as the `coverage-report` artifact, its summary in the run's summary. Not run for pull requests: it takes longer than the `go` and `selftest` jobs together (30 minutes on the developer machine). **Not verified** on GitHub |

Only the newest 3 runs of each workflow are kept (the Pages deployments too): the last job of `platform`, `fuzz` and `coverage`, `prune`, deletes the older ones with their logs and artifacts (`scripts/ci-prune.sh`; kario-internal's `internal` does the same). By hand: `sh scripts/ci-prune.sh [-n] kario-os/kario 3`, `-n` only lists.

The jobs of `platform`:

```
plan -> checks -> go, race, jsrt-sanitize, telephonyd, mm, capi, sku, paint, gui, ts, selftest, pixel, yocto
        dco (pull requests only)       (each runs only when the plan needs it)
all of the above -> pr-ok              (the one check branch protection requires)
pr-ok -> prune                         (only the newest 3 runs of each workflow stay)
platform passed on a pull request -> ai-review (workflow_run) -> one comment on the pull request
```

- `plan` runs `scripts/ci-plan.py`: a pull request's checkout is its merge commit, which it compares with its first parent (the base it merges into), and it decides which areas run and which Go packages are tested, as the job's outputs and its job summary.
- `checks` always runs and ends within minutes: the repository's check scripts (`no-chinese`, `i18n-check`, `const-check`, `license-check`, `docs-links`, `docs-en-check`, `binary-check`, `gomodinc.py --check`), the CI and build scripts' own tests (`ci-plan`, `ai-review`, `yocto-check`, `yocto-jobs`, `doctor`, `coverage`, `binary-check`, `docs-links`, `gomodinc`), gofmt of the changed Go files, `go vet -composites=false` of the planned Go packages, shellcheck of the changed shell scripts (an error fails; a new script may not have warnings either), actionlint when workflows change, rustfmt when `paint/` or `gui/` change (the two crates are not rustfmt-formatted yet, so it only warns; it is enforced for a directory once it has a `rustfmt.toml`). `dco` checks that every commit of the pull request is signed off.
- The build and test jobs start only after `checks` passed: a pull request with formatting errors does not take up the test machines.
- Caches: Go modules and the build cache (`setup-go`), cargo's registry and `target/` (by `Cargo.lock`), npm (`setup-node`). A new push to the same pull request cancels its older run. Each workflow has only `contents: read` by default.
- The end-to-end test of `go test ./shell` (`TestSelftestScenario`) splits the selftest script by its `#part <name>` lines, and runs the parts at once, each in a child process of its own on a stack of its own; the tests that start a Shell in the process run in a child process each too (a Shell sets the process's UI language, which every other Shell in it reads), and phone migration listens on a free port there instead of 47800. On the development machine (2026-11-19, 48 cores, load 4 to 7) the whole package went from 627 to 155–163 seconds (168 on 4 cores), the longest part being `chat` at about 142; `make selftest` still runs the whole script in order. A new selftest case belongs to the nearest `#part` line above it; one that needs what a case of another part left (a paired headset, a call in the log, a photo) goes into that part.
- The `race` job, when the `go` job runs the shell package, runs the shell package's tests again with `go test -race` on a runner of its own, at the same time as the `go` job, so CI takes no longer as a whole. Other packages do not pass under `-race` yet ([40](40-testing.md) §7), so it runs the shell package only. On the development machine (2026-11-21) the shell package took 418 seconds under `-race` on 48 cores and 436 on 4 (`taskset -c 0-3`; GitHub's ubuntu-24.04 runners for public repositories have 4), about 2.6 times as long as without `-race`; its time limits are the `go` job's. It has not run on GitHub yet: **not verified**.
- The `jsrt-sanitize` job, when the `go` job runs the `jsrt` package, builds kario-jsrt once with AddressSanitizer and UBSan and once with ThreadSanitizer and runs the `jsrt` package's tests again against each (`make jsrt-asan`, `make jsrt-tsan`, two runners, at the same time as the `go` job); any report fails it ([40](40-testing.md) §8). It runs on every pull request that changes kario-jsrt's C source, its tests or what they depend on, not on a schedule: its tests take 95–98 seconds on the developer machine (86 without a sanitizer), what it adds is mostly the cold build (about 2.3 minutes for ASan, 1.1 for TSan), and what it finds, use after free and data races, shows nowhere else. It has not run on GitHub yet: **not verified**.

## 3. What runs for which change

| Change | What runs |
|------|--------|
| Documents only (`docs/`, `*.md`, `LICENSE`) | Only `checks`; `docs/22-developer-api.md` and `docs/23-renderer-protocol.md` are read by the `shell` tests, so a change to them also runs `go test ./shell` |
| Go source | go, and race when the shell package is among them (the shell package under `-race`), jsrt-sanitize when the `jsrt` package is: the changed packages and every package that depends on them (the import graph from `go list`, test imports included); when a package is linked into the programs `make build` builds, also selftest, cts, pixel, the `shell` end-to-end tests and the tests of `tools/covflush` (every program writes its counters while it runs in a coverage build, [40](40-testing.md) §7) |
| Only `_test.go` or `testdata/` | That package's tests |
| Files `make build` installs into the system: `apps/`, `examples/`, `web/`, `icons/`, `profile.d/`, `bench/`, `brand.json`, `audio/testdata/`, and files packages embed | selftest, cts, pixel, `go test ./shell`, and the tests of the packages that read those files |
| `jsrt/src/` (kario-jsrt's C source) | go (`make jsrt` first; its tests may not skip), jsrt-sanitize, selftest, cts, pixel |
| `telephonyd/` | telephonyd (build and unit test); `src/` also selftest, cts, pixel; the ModemManager backend's files (`src/mm*`, `test/mock_mm.py`, `test/mm_test.py`, `test/Containerfile`, `CMakeLists.txt`) also mm (`make mm-selftest`, in Podman) |
| `sdk/c/` | capi, selftest, cts, pixel |
| `paint/` | paint (clippy, `cargo test`) and pixel; `Cargo.lock` also yocto (the recipe's crate list) |
| `gui/` | gui (clippy, build, `cargo test`) |
| TypeScript: `sdk/ts/`, `ide/`, directories with a `tsconfig.json` | ts (Kario Studio's `npm test`, `tsc -p` of each TypeScript project) |
| `sku/`, `tools/sku.py` | sku (`make sku-check`, which first fetches kario-bsp at its pinned commit, §11) |
| `yocto/kas/phone-fajita.yml` (pins kario-bsp's commit) | yocto and sku |
| `yocto/` | yocto: `scripts/yocto-check.py` (recipes where `layer.conf` finds them, licence fields, the layer's classes, crate lists, Go packages named by `GO_INSTALL` that are more than tests, kas files, recipes' `S` where their sources unpack, packages' runtime dependencies taken out of configurations with `:remove` only, Python, shellcheck) `yocto/fajita-flash-test.py` (the 6T's flashing, backup and restore, against a simulated phone), `yocto/verity-pad-test.py` (the delta bundle padded so that dm-verity's read-ahead stays out of its image, on bundles made with mksquashfs and a streamed download worked out as the kernel makes it), `yocto/qemu-ota-delta-test.py` (the label the update test across versions gives this tree, a new file counting as a change, on a temporary repository), `yocto/gui-compare-test.py` (two GUI runs' screendumps compared, on made-up pictures), `yocto/qemu-gui-drive-test.py` (the GUI drive's waiting for a slow renderer's screen to settle, and its reading of gui-console's console file with kernel lines printed inside it, on a made-up console and screendumps) and `yocto/btvirt-test.py` (the emulator's virtual Bluetooth controllers, btvirt, with the layer's BlueZ patches, under AddressSanitizer; BlueZ's release tarball from kernel.org), no image build |
| `Makefile`, `go.mod`, `go.sum`, `scripts/`, `.github/`, or a file no rule knows | Everything |
| A run by hand | Everything |

The rules are the `RULES` table of `scripts/ci-plan.py`. When a Go test reads files in another directory (like the two documents the `shell` tests read), the table needs a line for it; `scripts/ci_plan_test.py` scans the Go tests for paths starting with `../` and fails when the table lacks one; it also fails when a file below the root that a publish commits (the tracked ones and the untracked ones not ignored) is one no rule knows.

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
python3 scripts/binary-check.py
python3 -m unittest scripts/ci_plan_test.py scripts/ai_review_test.py scripts/yocto_check_test.py \
    scripts/yocto_jobs_test.py scripts/doctor_test.py scripts/coverage_test.py scripts/binary_check_test.py \
    scripts/docs_links_test.py scripts/kario_bsp_test.py tools/gomodinc_test.py
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
- Not in CI: the Yocto image and the tests in QEMU, `web-selftest`, `gui-selftest`, `cargo test` of `sdk/rust/kario-native` (the `jsrt` tests only build its example module). `make fuzz` runs only in the `fuzz` workflow, by hand; pull requests do not build the libFuzzer harnesses; `make coverage` runs only in the `coverage` workflow, by hand.
- The AI report can be wrong or miss things; it is advice for the maintainers.

## 10. Branches: main and dev

- `main` is the version phones are tested on; testing on a real phone ([39](39-oneplus-6t.md)) always uses it. It takes two things only: bug fixes, and features promoted from `dev` after the full verification there ([40](40-testing.md) §5: `make test`, `make cts`, the text and pixel selftests on the developer machine, both QEMU images).
- `dev` is where new features land first; it gets the same full verification before it is published.
- Each branch is one commit, replaced as the project changes. `make publish` publishes the branch the work tree is on (`scripts/publish.sh`) and refuses any other branch name. It commits every file in the work tree that git does not ignore, so while `scripts/binary-check.py` (§1) fails it stages and pushes nothing: `apps/settings/settings`, a 6.4 MB x86-64 program a `go build` left in Settings' directory, which `.gitignore` did not name then, was published to main and dev that way; `.gitignore` now names that program of every Go app. The two share no history, so "merging" means carrying changes over: a bug fix goes into both `main` and `dev`; a feature, once verified, is promoted from `dev` to `main` whole or change by change.
- Each branch has its own work tree (`git worktree`), so they do not get in each other's way; CI is started by hand after publishing: `gh workflow run ci.yml --ref main` (or `--ref dev`).

## 11. Changing kario and kario-bsp together

The 6T's machine, kernel, firmware and the Qualcomm glue are in the public [kario-bsp](https://github.com/kario-os/kario-bsp) ([24](24-sku-framework.md) §8.7.6); kario uses the commit pinned on the line `repos.kario-bsp.commit` of `yocto/kas/phone-fajita.yml`.

- **Changing kario only**: nothing more to do. `make test`, `make sku-check` and CI run `make bsp-fetch` first, which fetches that commit into `build/kario-bsp`, and `tools/sku.py check` and the tests reading those recipes use its layers through `KARIO_BSP_LAYERS`; when it cannot be fetched (the pin still a placeholder, no network, main no longer having that commit) they fail, so the 6T's checks are never skipped quietly. kas fetches the same commit for the 6T's images; when the build generates the SKU's files first (`sku-gen` in `make yocto-build`) and `build/kario-bsp` is not there yet, it is fetched the same way once (QEMU's machines do not need it and stay offline).
- **Changing both**: check kario-bsp out beside kario and give `KARIO_BSP`; builds also take `yocto/kas/local-bsp.yml`, which kas-container mounts at `/kario-bsp`:

```bash
git clone https://github.com/kario-os/kario-bsp ../kario-bsp
make test KARIO_BSP=../kario-bsp
make yocto-build WORK=$HOME/yocto/fajita-work KASFILE=yocto/kas/phone-fajita.yml SKU=oneplus-fajita-t9 \
    KARIO_BSP=../kario-bsp KASEXTRA=yocto/kas/local-bsp.yml
make -C ../kario-bsp test KARIO=$PWD   # its layer checks, yocto-check on its layers, sku.py check
```

- **The order of merging**: merge kario-bsp's pull request and publish it first, then change `repos.kario-bsp.commit` to its commit in kario's pull request. Each publication of kario-bsp is a new root commit for now (`scripts/publish.sh`), so main no longer has the commit before, and the line has to change every time it is published.
- kario-bsp holds no binaries and no confidential material (what a vendor gave under an NDA, or marked confidential); its `make test` checks for them (`scripts/layer_check.py`). Closed parts go into that vendor's private BSP repository, the files into the artefact store ([24](24-sku-framework.md) §8).
