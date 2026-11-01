# AGENTS.md

## 项目

BBDown —— 命令行哔哩哔哩下载器，**BBDown 生态的 Go 主线实现**：起步自 C# 版
[aliveranme/BBDown](https://github.com/aliveranme/BBDown) v1.6.20 的重写，此后独立演进。

- `cmd/bbdown/` —— 入口
- `internal/cli/` —— cobra 命令、参数归一化、配置文件合并
- `internal/config/` —— 选项与 `BBDown.config` 解析
- `internal/workflow/` —— 编排：分P选择、下载、清理、入档
- `internal/parser/` + `internal/fetcher/` + `internal/appapi/` —— 四种解析模式与各页源
- `internal/download/` + `internal/muxer/` —— 多线程下载与混流
- `internal/server/` —— `serve` HTTP API
- `internal/live/` `internal/article/` `internal/substore/` `internal/login/` `internal/drm/`

## 最高约束：独立实现，多源参考

本仓不是上游的影子：它起步于 C# 版 v1.6.20 的重写，但**独立演进**——版本号、功能与风控策略都走自己的线。
上游（`AliverAnme/BBDown`）是**参考基线之一**，不再是唯一规格来源。动手前查上游，目的从「保持一致」
变成「知道差异在哪、以及别人解决过什么」：

- 上游源码就在本地 git 对象库里（remote `upstream`，tags `v1.6.11`~`v1.6.20`），**无需联网**：
  ```bash
  git show v1.6.20:BBDown.Core/Parser.cs
  git show v1.6.20:BBDown.Tests/DownloadPipelineTests.cs
  ```
- 上游的 `CHANGELOG.md` 与 `docs/REVIEW_FINDINGS.md`（RF-1..RF-88）是行为规格的第二来源。
- 上游的 `BBDown.Tests/`（含 `FakeBilibiliApiServer` 与 `Fixtures/`）是**回归网与规格来源**：
  新行为优先移植其断言当基线，再叠加我们自己的。
- **本仓库的注释也会过时**：曾有一条 `matching C#: no early return` 的注释与上游行为完全相反。
  有疑问就查上游真身，不要信注释。
- 版本号语义是**我们自己的** `MAJOR.MINOR.PATCH`，**模仿 Linux 内核**（历史 `1.6.20-go[.N]` 条目保留在
  `CHANGELOG.md`，该线不再新增）：

  | 位 | 含义 | 什么时候动 |
  |---|---|---|
  | `MAJOR` | 不兼容变更（CLI/产物布局/配置格式破坏性改动） | **极少**动 |
  | `MINOR` | **功能周期**：一批新功能落地 | 功能批次完成时 +1；**不因单个小功能跳动** |
  | `PATCH` | **修复批次**：只有修复/优化、没有新功能 | **这是默认动作**——改了行为就发 patch（`2.4.2`、`2.4.3`…） |
  | `-rcN` | 预发布：风险变更（风控/协议/架构）先打 `vX.Y.Z-rcN` 验证 | **暂不启用**（用户 2026-10-07 定：预发布算了） |

  纪律：**改动不攒着**——攒着就无法区分「修复」与「功能」，也就无法按内核的节奏走。上一个 minor 在
  下一个 minor 发布前只收修复；发布前必须 CI 全绿（tag 即发布）。

  **开发构建后缀（Linux EXTRAVERSION 同款，2026-10-07 用户定）**：非 tag 的自编译构建，横幅版本串
  应带构建标识以与官方 Release 区分——`BBDown version X.Y.Z-git.<短提交号>`，工作区有未提交改动时
  加 `-dirty`。官方 Release 的横幅保持纯净的 `X.Y.Z`。横幅实现落地时一并更新
  `version_consistency_test` 的口径（Release 构建必须仍逐字等于五处硬编码）。

- 与上游的行为对应关系写在 `CHANGELOG.md` 与 `docs/UPSTREAM_ALIGNMENT.md`，**不写进版本号**。

### 两条硬规矩：偏离留痕、改动有证据

1. **偏离留痕**：与上游不同的行为逐条登记到 `docs/UPSTREAM_ALIGNMENT.md` 的差异表（§4.x），并在
   CHANGELOG 对应版本里写清「差异点 + 用户价值」。**有意偏离**与**尚未对齐**必须分开记，否则下次
   对账会把有意差异当成待修的落后项。
2. **证据门槛**：
   - 功能与修复：回归用例 + 变异验证（撤掉实现必须变红），与既有测试纪律一致；
   - **优化：改前/改后的量化数字**（耗时 / HTTP 请求数 / CPU / 内存 / 产物大小）。没有数字不算优化。
3. 候选清单与优先级见 `docs/ROADMAP.md`。

## 命令

```bash
make build            # go build -ldflags="-s -w" -o bin/BBDown ./cmd/bbdown
make test             # go test ./...
make smoke            # 真机冒烟（需联网）：doctor + 三条解析路径 + 真实下载 + 产物 ffprobe + 进度 JSON

# 提交前必须全过
gofmt -l internal/ cmd/     # 必须无输出
go build ./... && go vet ./... && go test ./...
```

## 测试纪律

- **每条修复都要有回归用例**，并以「撤掉修复必须让测试变红」验证（变异验证）。没有验证过的
  回归网等同于没有。
- **变异验证用编辑器改回，不要用 `git checkout -- <文件>` 撤销**：它会把同一个文件里其它尚未
  提交的改动一起丢掉（本仓库真发生过一次，颜色常量被整块回滚）。
- **推送前必须跑 `go test ./... -count=1`**：默认的 `go test ./...` 会用测试缓存，本地可能报绿而
  CI（`-count=1`）报红。改动 `internal/util` 这类被广泛依赖的契约时尤其要留意。
- **改动既有函数的契约时，先跑一遍受影响的包**：仓库里已有的测试钉住了旧行为，若那次改动是
  有意的（对齐上游），就同步更新旧测试并在用例里写明理由。
- 断言要检验**行为**，不要检验实现细节；不要写永远成立或只驱动副本的断言（上游把这类叫做"假绿"，
  并在 RF-58/RF-60/RF-68/RF-75/RF-88 多次修正）。
- 外部工具用**假可执行文件**（脚本 dump argv）而非真实调用；需要真实 ffmpeg 时用
  `t.Skip` 保护并说明依赖。
- 解析层改动优先接到 `internal/parser/testdata/` 的夹具回放基座上（`fixture_test.go`）。
- 计时相关的用例把超时做成变量（如 `readStallTimeout`、`downloadStallTimeout`）以便测试收窄。
- **判据必须跨平台**（两次真栽过：`2.12.1`/`2.12.2` 的工作目录守卫，同一提交在 ubuntu 绿、windows/macOS 红）：
  1) 涉及**路径**的比较一律用 `os.Stat` + `os.SameFile`（比较底层对象），**禁止字符串相等**——macOS 上 `/var` 是 `/private/var` 的符号链，
     `os.Chdir("/var/…")` 成功但 `os.Getwd()` 返回解析后的路径；Windows 盘符大小写与 8.3 短名同理；
  2) 涉及**已删除目录中的 cwd**：Linux 的 `getcwd(2)` 返回 ENOENT（比较被短路 → 假绿）、Windows 返回已消失路径、macOS 保留 name cache——
     不要依赖这些差异；
  3) 涉及**时间/mtime 粒度**（NTFS 100ns / APFS 1ns / HFS+ 1s）、**权限与符号链接创建能力**（Windows 需特权）时，用显式 `t.Skipf` 写明原因，
     不要靠「本机跑得过」；
  4) 写守卫时**先列一张「判据 × 三平台语义」表**再写代码；表里有任何一条不一致，就把判据换成与平台无关的形式，或显式吸收该差异
     （如字符串仅用于打印、判定改用底层对象比较）。
     教训原文见 `docs/UPSTREAM_ALIGNMENT.md` §4.58 —— 其中最值得记的是：**这条规则在修复它自己的代码里复发过一次**。
- **不要用固定 `time.Sleep` 等待异步副作用**：慢速 CI runner（尤其 windows-latest）上会变成时序竞态。
  改为轮询可观测的状态（磁盘字节数、channel 信号）并设上限。
- **断言失败前先释放资源**：若被测协程还挂在网络上，`t.Fatal` 会走 defer（如 `srv.Close()`）等待挂起的 handler，
  失败看起来像超时。先用 `cancel()` 断开再断言。
- `internal/live` 的回环流式 harness 在 GitHub 的 windows-latest 上不可靠（刷出的字节收不到，读挂到 60s 看门狗），
  该用例按平台跳过；跨平台逻辑请另找不依赖 socket 时序的断言方式。

## 提交与分支

- Conventional Commits：`fix(scope): …` / `feat(scope): …` / `test(…)` / `docs: …`，
  正文用中文说明**为什么**（现象 + 后果），而不只是改了什么。
- 一个提交只做一件事；跨模块的批量修复按包拆分。

### 三个 BBDown 仓库的关系（别搞混）

- `nilaoda/BBDown` —— **原始仓库，已归档**（README 只剩归档说明），不再维护；
- `AliverAnme/BBDown` —— **参考基线之一**（git remote `upstream`）：原仓的 fork，继续走 C# 1.6.x；我们起步自它，但独立演进；
- `LOVAHE/BBDownT` —— **另一条接手线**（非 fork，C# 2.x，默认分支 `v2`，dotnet tool 分发）：也接手自原仓，
  但代码线不同，**不能直接同步**。它的提交/发布说明是**第二规格来源**，尤其风控相关——例如 2.1.x 的
  「移除易触发 412 的默认 User-Agent」「412 时轮换 UA 重试最多 3 次」「完善登录态浏览器请求配置」。
  我们踩到同类问题时（412/风控/接口变更），先去查它做过什么，再决定是否跟进（跟进的同样要登记差异）。
## 与外部源同步（四源）

规格来源有四个，任一更新都可触发动作，不必等某个仓库发版：

1. `AliverAnme/BBDown` —— 参考基线（tags `v1.6.11`~`v1.7.8`，本地 git 对象库即可查，`git fetch upstream --tags` 更新）；
2. `LOVAHE/BBDownT` —— C# 2.x 接手线，**风控与接口变更的情报价值最高**（已发 v2.2.0）；
3. `bilibili-API-collect` —— 接口规格；**⚠️ 2026-01 起仓库已弃用关停**（README 声明「停止维护并永久关停」），降权为历史参考，接口结论以实测与上游为准；
4. **实测** —— 探针脚本 + `--debug` 日志；与前三者冲突时**以实测为准**。


```bash
git fetch upstream --tags
git rev-list --left-right --count origin/master...upstream/master   # 0 ahead / N behind
git show <tag>:CHANGELOG.md          # 逐版本行为要点
git show <tag>:docs/REVIEW_FINDINGS.md
```

外部源有更新时：并入四源评估 → 值得跟的补用例 + 变异验证 → 在 `docs/UPSTREAM_ALIGNMENT.md` 的 §4 判定表
登记（有意偏离 / 尚未对齐）→ 记 `CHANGELOG.md`。**版本号只随我们自己的发布前移**，五处硬编码：
横幅 `cmd/bbdown/main.go`、`internal/cli/root.go` 的 `Version` 与更新检查、`internal/cli/commands.go`
的更新检查、`PKGBUILD`。

## serve 子命令的坑

- 其选项（`-l`/`--max-concurrent`/`--serve-token`）不从 `BBDown.config` 读取。
- 非回环监听且未配 token 会**故意拒绝启动**（`docker run` 直接退出是预期行为，不是 bug）。
- `/health` **按设计无需认证**；限速与门禁只覆盖 API 路径。
