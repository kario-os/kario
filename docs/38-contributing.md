# 贡献与 PR 流水线

> 一个 PR 从提交到可以合并：提交前要做到什么，CI 按改动跑哪些检查和测试，唯一的门禁检查 `pr-ok`，CI 通过后的 AI 评审报告，代码扫描，以及怎么在自己机器上跑同样的检查。约定本身见 [CONTRIBUTING.md](../CONTRIBUTING.md)。
> 状态：在开发机上（2026-11-16，RHEL 8，x86-64）跑过：actionlint 检查四个工作流（2026-11-18 起五个，加上 `fuzz`）；`scripts/ci_plan_test.py`、`scripts/ai_review_test.py`（假的 GitHub 接口、假的模型服务）和 `scripts/yocto_check_test.py`；`scripts/yocto-check.py` 和仓库的检查脚本；在临时克隆里对合成的提交跑 `scripts/ci-plan.py`。GitHub 上：`platform` 工作流手动触发跑通过（2026-11-18，`a3857c8`）；PR 触发还没跑过，AI 报告没有对真的模型跑过（没有提交触发它，跳过了）：这两项**未验证**。

## 1. 提交之前

- 每个提交都签 DCO：`git commit -s`（`scripts/check-dco.sh` 检查）。
- 代码是英文的，注释也是；中文只在数据里（语言目录、manifest、自测脚本、`sku/`、`testdata/`、`docs/`）。界面文字是英文消息，经 `tr.T` 等翻译调用，新消息要有 zh-Hans 翻译，zh-Hant 和 es 可以晚一点（[22](22-developer-api.md) §9.1）。
- 系统和应用共用的名字写在 `ui/`、`svc/` 的常量里；改了 `ui` 包要重新生成 TypeScript 类型（`go run ./tools/tsgen`）。
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

运行记录只留每个工作流最新的 3 次（含 Pages 的部署）：`platform` 和 `fuzz` 最后的 `prune` 作业删掉更早的，连同日志和产物（`scripts/ci-prune.sh`，kario-internal 的 `internal` 也一样）；手动清理：`sh scripts/ci-prune.sh [-n] kario-os/kario 3`，`-n` 只列出不删。

`platform` 的作业：

```
plan -> checks -> go, telephonyd, mm, capi, sku, paint, gui, ts, selftest, pixel, yocto
        dco (pull requests only)       (each runs only when the plan needs it)
all of the above -> pr-ok              (the one check branch protection requires)
pr-ok -> prune                         (only the newest 3 runs of each workflow stay)
platform passed on a pull request -> ai-review (workflow_run) -> one comment on the pull request
```

- `plan` 跑 `scripts/ci-plan.py`：PR 的检出是合并提交，它比较合并提交和它的第一个父提交（要合进去的基线），得出每个方面要不要跑、跑哪些 Go 包，写进作业的输出和作业摘要。
- `checks` 每次都跑，几分钟内结束：仓库的检查脚本（`no-chinese`、`i18n-check`、`const-check`、`license-check`、`docs-links`、`docs-en-check`、`gomodinc.py --check`），CI 脚本自己的测试，改到的 Go 文件的 gofmt，计划里的 Go 包的 `go vet -composites=false`，改到的 shell 脚本的 shellcheck（出错就失败；新加的脚本连警告也不许有），改了工作流时的 actionlint，改了 `paint/`、`gui/` 时的 rustfmt（这两个 crate 现在没按 rustfmt 排版，只警告；哪个目录里有了 `rustfmt.toml` 就对它强制）。`dco` 检查 PR 的每个提交都签了名。
- 构建和测试的作业等 `checks` 通过才开始：格式不对的 PR 不占用跑测试的机器。
- 缓存：Go 模块和构建缓存（`setup-go`），cargo 的注册表和 `target/`（按 `Cargo.lock`），npm（`setup-node`）。同一个 PR 推了新提交，旧的运行被取消。每个工作流默认只有 `contents: read`。
- `go test ./shell` 的端到端测试（`TestSelftestScenario`）把自测脚本按其中的 `#part <名字>` 行分成几部分，每部分在自己的子进程里、自己的一套服务上同时跑；在进程里起 Shell 的测试也各自在子进程里跑（Shell 会改进程的 `time.Local`），换机在这里监听空闲端口，不占 47800。开发机上（2026-11-19，48 核，负载 4–7）整个包从 627 秒降到 155–163 秒（只给 4 个核时 168 秒），最长的是 `chat` 部分，约 142 秒；`make selftest` 照旧按顺序跑整个脚本。新加的自测用例归它上面最近的 `#part` 行；要用到别的部分里的用例留下的东西（配对的耳机、通话记录、照片）时，放进那一部分。

## 3. 改了什么，跑什么

| 改动 | 跑什么 |
|------|--------|
| 只有文档（`docs/`、`*.md`、`LICENSE`） | 只有 `checks`；`docs/22-developer-api.md` 和 `docs/23-renderer-protocol.md` 由 `shell` 的测试读，改它们还跑 `go test ./shell` |
| Go 源码 | go：改到的包和所有依赖它的包（`go list` 的导入图，测试里的导入也算）；包在 `make build` 链接的程序里时，还有 selftest、cts、pixel 和 `shell` 的端到端测试 |
| 只有 `_test.go` 或 `testdata/` | 那个包的测试 |
| `make build` 装进系统的文件：`apps/`、`examples/`、`web/`、`icons/`、`profile.d/`、`bench/`、`brand.json`、`audio/testdata/`，以及包嵌入的文件 | selftest、cts、pixel、`go test ./shell`，和读这些文件的包的测试 |
| `jsrt/src/`（kario-jsrt 的 C 源码） | go（先 `make jsrt`，它的测试不许跳过）、selftest、cts、pixel |
| `telephonyd/` | telephonyd（构建和单元测试）；`src/` 还有 selftest、cts、pixel；ModemManager 后端的文件（`src/mm*`、`test/mock_mm.py`、`test/mm_test.py`、`test/Containerfile`、`CMakeLists.txt`）还有 mm（`make mm-selftest`，在 Podman 里） |
| `sdk/c/` | capi、selftest、cts、pixel |
| `paint/` | paint（clippy、`cargo test`）和 pixel；`Cargo.lock` 还有 yocto（配方的 crate 清单） |
| `gui/` | gui（clippy、构建、`cargo test`） |
| TypeScript：`sdk/ts/`、`ide/`、有 `tsconfig.json` 的目录 | ts（Kario Studio 的 `npm test`，每个 TypeScript 工程 `tsc -p`） |
| `sku/`、`tools/sku.py` | sku（`make sku-check`） |
| `yocto/` | yocto：`scripts/yocto-check.py`（配方在 `layer.conf` 找得到的地方、许可字段、本层的类、crate 清单、`GO_INSTALL` 点名的 Go 包不只有测试、kas 文件、Python、shellcheck）、`yocto/abslot-test.py`（一加 6T 标记 A/B 启动槽的 `phone-abslot`，在磁盘镜像上）、`yocto/fajita-flash-test.py`（6T 的刷机、备份和恢复，对着模拟的手机）和 `yocto/btvirt-test.py`（模拟器的虚拟蓝牙控制器 btvirt 带上层里的 BlueZ 补丁，用 AddressSanitizer 跑；从 kernel.org 取 BlueZ 的源码包），不构建镜像 |
| `Makefile`、`go.mod`、`go.sum`、`scripts/`、`.github/`，或者规则不认识的文件 | 全部 |
| 手动运行 | 全部 |

规则在 `scripts/ci-plan.py` 的 `RULES` 表里。Go 测试读别的目录里的文件（比如 `shell` 读的两篇文档）时，表里要有一行；`scripts/ci_plan_test.py` 扫描 Go 测试里以 `../` 开头的路径，表里漏了就失败。

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
python3 -m unittest scripts/ci_plan_test.py scripts/ai_review_test.py scripts/yocto_check_test.py
python3 scripts/yocto-check.py
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
- 不在 CI 里：Yocto 镜像和 QEMU 里的测试、`web-selftest`、`gui-selftest`、`sdk/rust/kario-native` 自己的 `cargo test`（`jsrt` 的测试只构建它的示例模块）。`make fuzz` 只在手动触发的 `fuzz` 工作流里跑，PR 不构建 libFuzzer 版的测试程序。
- AI 报告可能错，也可能漏；它只是给维护者的参考。
