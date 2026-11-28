# 贡献与 PR 流水线

> 一个 PR 从提交到可以合并：提交前要做到什么，CI 按改动跑哪些检查和测试，唯一的门禁检查 `pr-ok`，CI 通过后的 AI 评审报告，代码扫描，以及怎么在自己机器上跑同样的检查。约定本身见 [CONTRIBUTING.md](../CONTRIBUTING.md)。
> 状态：在开发机上（2026-11-16，RHEL 8，x86-64）跑过：actionlint 检查四个工作流（2026-11-18 起五个，加上 `fuzz`；2026-11-23 起六个，加上 `coverage`；2026-11-28 起七个，加上 `fuzz-weekly`）；`scripts/ci_plan_test.py`、`scripts/ai_review_test.py`（假的 GitHub 接口、假的模型服务）和 `scripts/yocto_check_test.py`（2026-11-21 起加上 `scripts/yocto_jobs_test.py`、`scripts/doctor_test.py`、`tools/gomodinc_test.py`，2026-11-23 起加上 `scripts/coverage_test.py`，在开发机上跑过，还没在 CI 里跑过）；`scripts/yocto-check.py` 和仓库的检查脚本；在临时克隆里对合成的提交跑 `scripts/ci-plan.py`。GitHub 上：`platform` 工作流手动触发跑通过（2026-11-18，`a3857c8`）；PR 触发还没跑过，AI 报告没有对真的模型跑过（没有提交触发它，跳过了）：这两项**未验证**。

## 1. 提交之前

- 每个提交都签 DCO：`git commit -s`（`scripts/check-dco.sh` 检查）。
- 代码是英文的，注释也是；中文只在数据里（语言目录、manifest、自测脚本、`sku/`、`testdata/`、`docs/`）。界面文字是英文消息，经 `tr.T` 等翻译调用，新消息要有 zh-Hans 翻译，zh-Hant 和 es 可以晚一点（[22](22-developer-api.md) §9.1）。
- 系统和应用共用的名字写在 `ui/`、`svc/` 的常量里；改了 `ui` 包要重新生成 TypeScript 类型（`go run ./tools/tsgen`）。
- 改了 `go.mod`、`go.sum` 里的模块：`make gomods` 重新生成 Yocto 配方的 Go 模块清单，和它们一起提交（要联网和不比 go.mod 的 `go` 行旧的 Go）。`checks` 里的 `gomodinc.py --check` 只看模块，`go mod tidy` 挪动注释和 `// indirect` 不算；忘了的话 `make yocto-build` 也会先自己生成（[13](13-prototype.md) §5.1）。
- 构建出来的东西不进 git：在源码目录里 `go build` 留下的程序删掉或写进 `.gitignore`。`scripts/binary-check.py`（`make test`、`checks`、`make publish`）查发布会提交的文件（跟踪的，和没被忽略的未跟踪文件），其中有程序（ELF、Mach-O、PE 开头）或大于 512 KiB 的文件就失败；确实要放进来的大文件写进它的 `BIG`，带上限和理由（现在只有拼音词库 `ime/data/zh.tsv.gz`，约 620 KiB，上限 1 MiB）。512 KiB 高于每类正当文件里最大的（示例视频 300 KiB，`shell/selftest.script` 230 KiB，示例照片 210 KiB，`docs/img` 的图 193 KiB），低于最小的 Go 程序（空程序 1.5 MiB）和 phone-paint（2.2 MiB）。
- 修 bug 要带一个没修时会失败的测试；测试分哪几层、怎么写自测用例、发布前跑什么，见 [40](40-testing.md)。
- 文档跟着代码改：中文在 `docs/`，英文译文同名放在 `docs/en/`，结构一致；计划中或模拟的东西标 **未实现**、**未上真机**、**未验证**。
- 维护者看的规则写在 [.github/ai-review/rules.md](../.github/ai-review/rules.md)，AI 评审报告（§5）也按它评。

## 2. 流水线

| 工作流 | 什么时候跑 | 做什么 |
|--------|-----------|--------|
| `platform`（[ci.yml](../.github/workflows/ci.yml)） | PR；手动（往 main 发布后：`gh workflow run platform`） | 计划 → 快速检查 → 按计划跑的构建和测试 → 门禁 `pr-ok` |
| `codeql`（[codeql.yml](../.github/workflows/codeql.yml)） | 改到 Go、JavaScript / TypeScript 或工作流的 PR；手动（扫 main，安全页里的告警以它为准） | 代码扫描（§6） |
| `ai-review`（[ai-review.yml](../.github/workflows/ai-review.yml)） | `platform` 在 PR 上通过之后；手动 | AI 评审报告（§5） |
| `release`（[release.yml](../.github/workflows/release.yml)） | 手动 | 发布版本 |
| `fuzz`（[fuzz.yml](../.github/workflows/fuzz.yml)） | 只手动（Actions › fuzz › Run workflow；`targets` 是对 `make fuzz-list` 的名字的正则，`minutes` 是每个目标的分钟数，默认 10） | 每个模糊测试目标一个作业：`make fuzz` 的（Go 的，和 ASan、UBSan 版 C/C++ 测试程序的 Go 驱动）和 `make fuzz-libfuzzer` 的（同样的测试程序的 libFuzzer 版）；语料按目标存在缓存里接着跑，失败的输入作为产物上传、运行失败（[17](17-app-sandbox.md) §9.2）；2026-11-19 在 GitHub 上手动跑过一次（`targets` 为 `libfuzzer|cfuzz`，每个 1 分钟），通过 |
| `fuzz-weekly`（[fuzz-weekly.yml](../.github/workflows/fuzz-weekly.yml)） | 每周一 03:23（UTC，定时的运行只从默认分支起）；手动（Actions › fuzz-weekly › Run workflow） | 七个 libFuzzer 目标各一个作业，各变异 5 分钟（`make fuzz-libfuzzer FUZZTIME=5m`），语料和 `fuzz` 用同一个缓存的键；失败的输入和长大的语料作为产物上传；不挡任何东西（[17](17-app-sandbox.md) §9.2）。在 GitHub 上**未验证** |
| `coverage`（[coverage.yml](../.github/workflows/coverage.yml)） | 只手动（Actions › coverage › Run workflow；`parts` 是 `make coverage` 的几部分，空着就是全部） | 一个作业跑 `make coverage`（[40](40-testing.md) §7）：Go 的单元测试和整份文本自测合并的覆盖率，phone-paint、telephonyd、C SDK 的测试和 Node.js 下的 JavaScript；报告作为产物 `coverage-report` 上传，摘要写进运行摘要。不在 PR 上跑：比 `go` 和 `selftest` 两个作业加起来还长（开发机上 30 分钟）。在 GitHub 上**未验证** |

运行记录只留每个工作流最新的 3 次（含 Pages 的部署）：`platform`、`fuzz`、`fuzz-weekly` 和 `coverage` 最后的 `prune` 作业删掉更早的，连同日志和产物（`scripts/ci-prune.sh`，kario-internal 的 `internal` 也一样）；手动清理：`sh scripts/ci-prune.sh [-n] kario-os/kario 3`，`-n` 只列出不删。

`platform` 的作业：

```
plan -> checks -> go, race, jsrt-sanitize, node-newest, telephonyd, mm, capi, sku, paint, gui, ts, selftest, pixel, yocto
        dco (pull requests only)       (each runs only when the plan needs it)
plan -> checks -> libfuzzer-replay     (every run, whatever changed)
all of the above -> pr-ok              (the one check branch protection requires)
pr-ok -> prune                         (only the newest 3 runs of each workflow stay)
platform passed on a pull request -> ai-review (workflow_run) -> one comment on the pull request
```

- `plan` 跑 `scripts/ci-plan.py`：PR 的检出是合并提交，它比较合并提交和它的第一个父提交（要合进去的基线），得出每个方面要不要跑、跑哪些 Go 包，写进作业的输出和作业摘要。
- `checks` 每次都跑，几分钟内结束：仓库的检查脚本（`no-chinese`、`i18n-check`、`const-check`、`license-check`、`docs-links`、`docs-en-check`、`binary-check`、`gomodinc.py --check`），CI 和构建脚本自己的测试（`ci-plan`、`ai-review`、`yocto-check`、`yocto-jobs`、`doctor`、`coverage`、`binary-check`、`docs-links`、`gomodinc`），改到的 Go 文件的 gofmt，计划里的 Go 包的 `go vet -composites=false`，改到的 shell 脚本的 shellcheck（出错就失败；新加的脚本连警告也不许有），改了工作流时的 actionlint，改了 `paint/`、`gui/` 时的 rustfmt（这两个 crate 现在没按 rustfmt 排版，只警告；哪个目录里有了 `rustfmt.toml` 就对它强制）。`dco` 检查 PR 的每个提交都签了名。
- 构建和测试的作业等 `checks` 通过才开始：格式不对的 PR 不占用跑测试的机器。
- 缓存：Go 模块和构建缓存（`setup-go`），cargo 的注册表和 `target/`（按 `Cargo.lock`），npm（`setup-node`）。同一个 PR 推了新提交，旧的运行被取消。每个工作流默认只有 `contents: read`。
- Node.js：起 Node.js 的测试在 `go`（`jsrt` 的 node 引擎、`cmd/kario`，还有 `shell`、`tools/cfuzz` 和日历的测试）、`race`（`shell`）、`jsrt-sanitize`（`jsrt`）和 `ts`（Kario Studio）作业里，它们都用 `setup-node` 固定在 kario 支持的最老的大版本 22（kario 要 22.15 或更新），不用 runner 镜像自带的那个：镜像一更新它就悄悄变了。别的工作流里起 Node.js 的作业也固定在 22：`fuzz` 的作业（日历的 `FuzzICS`、`tools/cfuzz` 的 `FuzzTextDecoder` 起它）、`coverage`，和 `codeql` 分析 JavaScript、TypeScript 的那个（CodeQL 的 TypeScript 提取器起 `PATH` 上的 Node.js）；`fuzz-weekly` 只跑 libFuzzer 的 C、C++ 目标，不起 Node.js。`22` 只定大版本，小版本是 runner 的工具缓存里的那个 22.x，不一定是 22.15。`node-newest` 作业在最新的大版本 24 上把 Node 运行时的测试再跑一遍（`go test -run 'Node|Fetch' ./jsrt`、`go test ./cmd/kario`），`sdk/ts/node/`、`jsrt/`、`cmd/kario/` 下或 `sdk/ts/embed.go`、`sdk/c/include/kario_native.h` 有改动时跑（`embed.go` 写出运行时、定 node 的选项、用 `kario_native.h` 编运行时加载原生模块的插件），和 `go` 作业同时跑：两个大版本的 `fetch`（22 带 undici 6，24 带 undici 7）解析响应头就不一样（`jsrt/fetchhead_test.go`）。它不编 kario-jsrt，所以 kario-jsrt 自己的用例在这里跳过（它们和 Node 的版本无关，`go` 作业跑）；`shell`、`tools/cfuzz` 和日历起 Node 的测试只在 22 上跑。开发机上（2026-11-28）只给 4 个核（`taskset -c 0-3`）、没有 kario-jsrt 时，这两条命令在 Node 24 上跑了 76 秒（测试已编好，`jsrt` 50 秒，`cmd/kario` 21 秒）；加上装 Go 和编测试，作业预计 3–5 分钟。GitHub 上还没跑过：**未验证**。
- `go test ./shell` 的端到端测试（`TestSelftestScenario`）把自测脚本按其中的 `#part <名字>` 行分成几部分，每部分在自己的子进程里、自己的一套服务上同时跑；在进程里起 Shell 的测试也各自在子进程里跑（Shell 会设进程的界面语言，同一进程里别的 Shell 都读它），换机在这里监听空闲端口，不占 47800。开发机上（2026-11-19，48 核，负载 4–7）整个包从 627 秒降到 155–163 秒（只给 4 个核时 168 秒），最长的是 `chat` 部分，约 142 秒；`make selftest` 照旧按顺序跑整个脚本。新加的自测用例归它上面最近的 `#part` 行；要用到别的部分里的用例留下的东西（配对的耳机、通话记录、照片）时，放进那一部分。
- `race` 作业在 `go` 作业要跑 Shell 包或 Settings（`apps/settings`）时，在自己的机器上把其中要跑的那几个包的测试在 `-race` 下再跑一遍，一个包跑完再跑下一个，和 `go` 作业同时跑，不拉长整个 CI。哪几个包写在 `scripts/ci-plan.py` 的 `RACE_PKGS`，计划把这次要跑的交给作业（`race_packages`）：只改了 Settings 的测试时只跑 Settings，不陪着跑 Shell 包。别的包在 `-race` 下还过不了（[40](40-testing.md) §7），所以只跑这两个。开发机上（2026-11-21）Shell 包在 `-race` 下 418 秒（48 核），只给 4 个核（`taskset -c 0-3`，GitHub 给公开仓库的 ubuntu-24.04 机器是 4 个核）时 436 秒，约是不带 `-race` 的 2.6 倍；限时和 `go` 作业一样。Settings 在 `-race` 下约 5 秒（2026-11-28，48 核），加上它的编译，这个作业长不了多少。GitHub 上 2026-11-27 跑过（dev、main 各一次，那时只有 Shell 包），都通过；加上 Settings 以后在 GitHub 上**未验证**。
- `jsrt-sanitize` 作业在 `go` 作业要跑 `jsrt` 包时，把 kario-jsrt 带 AddressSanitizer 和 UBSan、带 ThreadSanitizer 各编一份，`jsrt` 包的测试对着每一份再跑一遍（`make jsrt-asan`、`make jsrt-tsan`，两台机器，和 `go` 作业同时跑），有一份报告就失败（[40](40-testing.md) §8）。改 kario-jsrt 的 C 源码、它的测试或它们依赖的东西的每个 PR 都跑，不等定时：它的测试在开发机上 95–98 秒（不带 sanitizer 86 秒），多出来的主要是冷构建（ASan 约 2.3 分钟，TSan 约 1.1 分钟），而它找的是只在这里看得见的释放后使用和数据竞争。GitHub 上 2026-11-27 跑过（dev、main 各一次），两份都失败：失败的是 `TestNodeFetchResponseHeaders` 的三个子用例（作业用的是运行机自带的 Node.js 22：它的 fetch 收下了名字不是 token 的回应头、接上了折行，开发机的 Node.js 24 都拒绝；Node 运行时现在自己拒绝这种名字，测试按 Node 的版本等折行的结果），不是 sanitizer 的报告；在 GitHub 上整个通过**未验证**（[40](40-testing.md) §8）。
- `libfuzzer-replay` 作业每次都跑，不管改了什么（和 `checks` 一样在计划的 `jobs` 里，`pr-ok` 要它成功）：用 runner 上的 clang 19 编出七个 libFuzzer 目标，各在提交的种子上跑一遍（`make fuzz-libfuzzer-replay`，`-runs=0`，不变异），一个种子崩溃、泄漏或超时就失败，那个输入作为产物上传。种子跑完不到 1 秒，作业的时间花在装 clang 和编译上（开发机上 1 分 42 秒）。变异的模糊测试在每周的 `fuzz-weekly` 和手动的 `fuzz` 里（[17](17-app-sandbox.md) §9.2）。GitHub 上还没跑过：**未验证**。

## 3. 改了什么，跑什么

| 改动 | 跑什么 |
|------|--------|
| 只有文档（`docs/`、`*.md`、`LICENSE`） | 只有 `checks` 和 `libfuzzer-replay`（这两个每次都跑）；`docs/22-developer-api.md` 和 `docs/23-renderer-protocol.md` 由 `shell` 的测试读，改它们还跑 `go test ./shell` |
| Go 源码 | go，其中有 Shell 包或 `apps/settings` 时还有 race（其中的这几个包在 `-race` 下），有 `jsrt` 包时还有 jsrt-sanitize：改到的包和所有依赖它的包（`go list` 的导入图，测试里的导入也算）；包在 `make build` 链接的程序里时，还有 selftest、cts、pixel、`shell` 的端到端测试和 `tools/covflush` 的测试（每个程序在覆盖率构建里都边跑边写计数，[40](40-testing.md) §7） |
| 只有 `_test.go` 或 `testdata/` | 那个包的测试 |
| `make build` 装进系统的文件：`apps/`、`examples/`、`web/`、`icons/`、`profile.d/`、`bench/`、`brand.json`、`audio/testdata/`，以及包嵌入的文件 | selftest、cts、pixel、`go test ./shell`，和读这些文件的包的测试 |
| `jsrt/src/`（kario-jsrt 的 C 源码） | go（先 `make jsrt`，它的测试不许跳过）、jsrt-sanitize、selftest、cts、pixel |
| `sdk/ts/node/`、`jsrt/`、`cmd/kario/` 下的任何文件，Go 源码也算，和 `sdk/ts/embed.go`、`sdk/c/include/kario_native.h`（Node 运行时，写出它、编它的插件的文件，起它的测试） | 别的行说的之外，还有 node-newest（Node 运行时的测试在 Node 24 上，§2） |
| `telephonyd/` | telephonyd（构建和单元测试）；`src/` 还有 selftest、cts、pixel；ModemManager 后端的文件（`src/mm*`、`test/mock_mm.py`、`test/mm_test.py`、`test/Containerfile`、`CMakeLists.txt`）还有 mm（`make mm-selftest`，在 Podman 里） |
| `sdk/c/` | capi、selftest、cts、pixel |
| `paint/` | paint（clippy、`cargo test`）和 pixel；`Cargo.lock` 还有 yocto（配方的 crate 清单） |
| `gui/` | gui（clippy、构建、`cargo test`） |
| TypeScript：`sdk/ts/`、`ide/`、有 `tsconfig.json` 的目录 | ts（Kario Studio 的 `npm test`，每个 TypeScript 工程 `tsc -p`） |
| `sku/`、`tools/sku.py` | sku（`make sku-check`，先按固定的提交取 kario-bsp，§11） |
| `yocto/kas/phone-fajita.yml`（固定 kario-bsp 的提交） | yocto 和 sku |
| `yocto/` | yocto：`scripts/yocto-check.py`（配方在 `layer.conf` 找得到的地方、许可字段、本层的类、crate 清单、`GO_INSTALL` 点名的 Go 包不只有测试、kas 文件、配方的 `S` 指向源码解开的地方、配置里包的运行依赖只用 `:remove` 去掉、Python、shellcheck）、`yocto/fajita-flash-test.py`（6T 的刷机、备份和恢复，对着模拟的手机）、`yocto/verity-pad-test.py`（增量包垫到 dm-verity 的预读不伸进镜像，用 mksquashfs 做的包和照内核算的流式下载）、`yocto/qemu-ota-delta-test.py`（跨版本更新测试给这棵树的标签，新加的文件也算改动，在临时仓库上）、`yocto/gui-compare-test.py`（两次 GUI 运行的截屏比对，在编出来的图上）、`yocto/qemu-gui-drive-test.py`（GUI 驱动脚本等慢渲染器的画面稳定，和它读 gui-console 转来的控制台文件，内核的行落在里面时拼回来，对着编出来的控制台和截屏）和 `yocto/btvirt-test.py`（模拟器的虚拟蓝牙控制器 btvirt 带上层里的 BlueZ 补丁，用 AddressSanitizer 跑；从 kernel.org 取 BlueZ 的源码包），不构建镜像 |
| `Makefile`、`go.mod`、`go.sum`、`scripts/`、`.github/`，或者规则不认识的文件 | 全部 |
| 手动运行 | 全部 |

规则在 `scripts/ci-plan.py` 的 `RULES` 表里。Go 测试读别的目录里的文件（比如 `shell` 读的两篇文档）时，表里要有一行；`scripts/ci_plan_test.py` 扫描 Go 测试里以 `../` 开头的路径，表里漏了就失败；发布会提交的文件（跟踪的，和没被忽略的未跟踪文件）里，根目录以下有规则不认识的，它也失败。

## 4. 门禁检查 pr-ok

计划不需要的作业被跳过。GitHub 把跳过的作业算作通过，因为前面的作业失败而被跳过的作业也一样，所以不能直接把各个作业设成必需的检查。`pr-ok` 依赖所有作业，总会运行，只有在下面三条都成立时才通过：

- 没有一个作业失败或被取消；
- `plan` 和 `checks` 成功了；
- 计划要的每个作业（`plan` 的输出 `jobs`）都成功了，而不是被跳过。

分支保护只要求 `pr-ok` 这一个检查（§8，**需要你本人**）。原来的 `test`、`docs` 作业已经没有了，还留在必需检查里的话，每个 PR 都会一直等着。

## 5. AI 评审报告

CI 通过以后，模型读一遍 PR，写一份参考报告，作为一条评论贴在 PR 上。它是 AI 生成的建议，不是维护者的决定。

- 什么时候跑：`platform` 在一个 PR 上通过（`pr-ok` 成功）之后，由 `workflow_run` 触发。维护者可以手动重新生成：Actions › ai-review › Run workflow，填 PR 号，需要时填更大的 token 预算。
- 不跑 PR 的代码：它在本仓库的上下文里跑（拿得到密钥，fork 的 PR 也一样），所以只检出默认分支的 `scripts/` 和 `.github/`，PR 的标题、描述、改动的文件、diff、每处改动前后 20 行（head 提交上的文件）都经 API 当作数据取。权限只有 `contents: read` 和 `pull-requests: write`。
- 找 PR：fork 的 PR 在事件里没有编号，依次按 fork 的 `owner:分支`、按提交、按搜索找，再核对 head 提交。
- 一个 PR 一条评论：评论里隐藏的 HTML 标记记着 head 提交；新的提交更新这条评论，这个提交已经有报告就不再生成。只认 `github-actions[bot]` 写的标记。草稿、已被新提交取代的运行、没通过的运行不评。
- 内容：六节——这个 PR 做什么；和现有代码怎么衔接（引用 `path:line`）；正确性和安全问题，按严重程度；测试覆盖了什么、缺什么；文档、翻译和约定；建议（merge / merge after changes / needs discussion），标明是 AI 的参考意见。用 `AI_REVIEW_LANG` 的语言写。评审依据 [.github/ai-review/rules.md](../.github/ai-review/rules.md) 和 `CONTRIBUTING.md`（都取默认分支的）。
- 防提示注入：PR 的内容放在带随机编号的分隔符之间，系统提示要模型不理会里面的任何指令；模型没有工具；它的回答只当文字贴出：去掉 @ 提及和 HTML 注释，限制长度，万一出现密钥就替换掉。
- 花费：生成的文件（`sdk/ts/ui.d.ts` 等）、vendor、锁文件、二进制只列文件名；每个文件的 diff 和上下文有上限；整个 diff 超过 token 预算时不调用模型，只贴一条说明。

| 名字 | 种类 | 默认 | 作用 |
|------|------|------|------|
| `AI_REVIEW_API_KEY` | 密钥（secret） | 无：没设就不贴报告，作业照样成功 | `AI_REVIEW_PROVIDER` 那家的 API 密钥 |
| `AI_REVIEW_PROVIDER` | 变量 | `deepseek` | 服务商：`deepseek`、`qwen`（阿里云百炼）、`kimi`（月之暗面）、`glm`（智谱）、`openai`、`anthropic`、`gemini`、`openrouter`；按它定地址、接口和默认模型（`deepseek-flash`、`qwen3.7-plus`、`kimi-k3`、`glm-5.3`、`chat-latest`、`claude-sonnet-5-5`、`gemini-flash-latest`；`openrouter` 没有默认，要设 `AI_REVIEW_MODEL`）。默认模型是 2026-11 各家推荐的名字 |
| `AI_REVIEW_BASE_URL` | 变量 | 服务商的地址 | 可选，换掉服务商的地址（自建的网关、别的区域），必须是 https |
| `AI_REVIEW_MODEL` | 变量 | 服务商的默认模型 | 可选，换一个模型 |
| `AI_REVIEW_LANG` | 变量 | `Simplified Chinese` | 报告的语言，用英文写语言名 |
| `AI_REVIEW_TOKEN_BUDGET` | 变量 | `60000` | 一次评审估算的 token 上限，超出只贴说明 |

## 6. 代码扫描（CodeQL）

- Go、JavaScript / TypeScript 和工作流本身（Actions）：PR 改到哪种语言就扫哪种（语言由 `ci-plan.py` 给出），手动运行时全扫。
- C / C++（telephonyd、C SDK、kario-jsrt 自己的源码）只在手动运行时扫：它要一次被跟踪的构建，不值得每个 PR 等；这些组件里处理不可信输入的解析器已经有模糊测试（`make fuzz`）。QuickJS-ng 在开始跟踪之前构建，结果里只有本仓库的代码。CodeQL 对 GCC 14 和 C++23 的支持还没在这里跑过：**未验证**。
- 不扫：`vendor/`、`build/`、`node_modules`、`testdata/`（里面故意放了恶意输入）、生成的文件、Mermaid 的打包文件（[.github/codeql/codeql-config.yml](../.github/codeql/codeql-config.yml)）。
- 结果在 Security › Code scanning，也作为 PR 上的标注；`pr-ok` 不等它。要在仓库里打开代码扫描，用这个工作流（advanced setup），关掉 default setup（§8，**需要你本人**）。

## 7. 在自己机器上跑同样的检查

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
make fuzz-libfuzzer-replay LF_CC=clang-19 LF_CXX=clang++-19  # each libFuzzer target over its committed seeds
GITHUB_REPOSITORY=kario-os/kario python3 scripts/ai-review.py --pr <number> --dry-run  # the prompt, no model, no comment
```

`ci-plan.py` 的摘要列出每个要跑的方面在本机怎么跑（比如 `make selftest`、`go test` 加上包名、`make pixel-selftest`）。`make test` 仍然是全部的单元测试和检查。`--dry-run` 不需要密钥，不带 `GITHUB_TOKEN` 时用 GitHub 的匿名接口（有频率限制）。

## 8. 仓库要做的设置（需要你本人）

| 设置 | 在哪里 | 怎么设 |
|------|--------|--------|
| 只要求 `pr-ok` | Settings › Rules › Rulesets（或 Branches）› `main` | Require status checks to pass：`pr-ok`（来源 GitHub Actions）；去掉旧的 `test`、`pixel`、`gui`、`docs`、`dco` |
| fork 的工作流要批准 | Settings › Actions › General › Approval for running fork pull request workflows | 至少 first-time contributors；外部 PR 多时选 all outside collaborators（AI 报告只在 CI 通过后跑，这样也限住了花费） |
| token 默认只读 | Settings › Actions › General › Workflow permissions | Read repository contents and packages permissions（要写的工作流自己声明） |
| AI 评审报告 | Settings › Secrets and variables › Actions | 变量 `AI_REVIEW_PROVIDER` 和密钥 `AI_REVIEW_API_KEY`；别的变量可选（§5） |
| 代码扫描 | Settings › Code security › Code scanning | 用 `codeql.yml`（advanced setup），关掉 default setup |

## 9. 局限

- 按路径挑测试是近似的。Go 的部分来自导入图；测试在运行时读或执行的别处文件，靠 `ci-plan.py` 里的表，扫描只认得以 `../` 开头的路径。漏掉的由手动的全量运行接住（往 main 发布后跑一次）。
- PR 能改 `.github/` 和 `scripts/`，也就改了 CI 怎么评它自己。这类 PR 总是跑全部，AI 报告也会指出，但要维护者自己看这些改动。
- rustfmt 对 `paint/`、`gui/` 只警告；shellcheck 对已有的脚本只拦错误（它们现在有警告）。
- `mm` 作业从 Docker Hub 拉 `debian:trixie`，匿名拉取有频率限制。
- 第三方工具只有 actionlint（按版本下载，核对 sha256）。`actions/*` 和 `github/codeql-action` 用主版本标签，和原来的工作流一样，没有钉到提交。
- 不在 CI 里：Yocto 镜像和 QEMU 里的测试、`web-selftest`、`gui-selftest`、`sdk/rust/kario-native` 自己的 `cargo test`（`jsrt` 的测试只构建它的示例模块）。变异的模糊测试（`make fuzz`、`make fuzz-libfuzzer`）只在手动触发的 `fuzz` 和每周的 `fuzz-weekly` 工作流里跑，PR 上的 libFuzzer 目标只把提交的种子跑一遍；`make coverage` 只在手动触发的 `coverage` 工作流里跑。
- AI 报告可能错，也可能漏；它只是给维护者的参考。

## 10. 分支：main 和 dev

- `main` 是在手机上测的那一版，真机测试（[39](39-oneplus-6t.md)）总是用它。它只收两样：修 bug 的改动；在 `dev` 上做完全套验证（[40](40-testing.md) §5：开发机的 `make test`、`make cts`、文本和像素自测，QEMU 两个镜像）以后提升上来的功能。
- `dev` 是新功能先进的地方，发布前同样跑全套验证。
- 两条分支都是一个提交、随项目替换。`make publish` 发布当前工作区所在的那一条（`scripts/publish.sh`），别的分支名它拒绝。它提交工作区里所有没被忽略的文件，所以 `scripts/binary-check.py`（§1）失败时它什么也不暂存、不推送：Settings 目录里 `go build` 留下的 `apps/settings/settings`（6.4 MB 的 x86-64 程序，当时 `.gitignore` 没写它）曾这样被发布到 main 和 dev；`.gitignore` 现在列出每个 Go 应用的这个程序。两条没有共同的历史，所以"合并"就是把改动搬到另一条上：bug 修复同时进 `main` 和 `dev`；功能验证过以后，整体或者按改动从 `dev` 提升到 `main`。
- 两条各用一个工作区（`git worktree`），互不打扰；CI 在发布后手动触发：`gh workflow run ci.yml --ref main`（或 `--ref dev`）。

## 11. kario 和 kario-bsp 一起改

6T 的机型、内核、固件和高通的胶水在公开的 [kario-bsp](https://github.com/kario-os/kario-bsp) 里（[24](24-sku-framework.md) §8.7.6），kario 用 `yocto/kas/phone-fajita.yml` 里 `repos.kario-bsp.commit` 那一行固定的提交。

- **只改 kario**：什么也不用多做。`make test`、`make sku-check` 和 CI 先 `make bsp-fetch`，把那个提交取进 `build/kario-bsp`，`tools/sku.py check` 和读那些配方的测试经 `KARIO_BSP_LAYERS` 用它的层；取不到（固定的还是占位、网络不通、main 上已经没有那个提交）就失败，6T 的检查不会悄悄跳过。6T 的镜像由 kas 按同一个提交取；构建前生成机型文件时（`make yocto-build` 里的 `sku-gen`）`build/kario-bsp` 还没有，也先这样取一次（QEMU 的机型用不到它，不联网）。
- **两个一起改**：把 kario-bsp 检出在 kario 旁边，给 `KARIO_BSP`；构建再加上 `yocto/kas/local-bsp.yml`，kas-container 里它挂在 `/kario-bsp`：

```bash
git clone https://github.com/kario-os/kario-bsp ../kario-bsp
make test KARIO_BSP=../kario-bsp
make yocto-build WORK=$HOME/yocto/fajita-work KASFILE=yocto/kas/phone-fajita.yml SKU=oneplus-fajita-t9 \
    KARIO_BSP=../kario-bsp KASEXTRA=yocto/kas/local-bsp.yml
make -C ../kario-bsp test KARIO=$PWD   # its layer checks, yocto-check on its layers, sku.py check
```

- **合进去的顺序**：先合 kario-bsp 的 PR、发布，再在 kario 的 PR 里把 `repos.kario-bsp.commit` 改成它的提交。kario-bsp 现在每次发布是一个新的根提交（`scripts/publish.sh`），旧的提交 main 上就没有了，所以它每发布一次，这一行都要改。
- kario-bsp 里不放二进制，也不放保密的材料（厂商按保密协议给的、标着保密的）；它的 `make test` 查这些（`scripts/layer_check.py`）。闭源的部件进那家厂商私有的 BSP 仓库，文件进制品库（[24](24-sku-framework.md) §8）。
