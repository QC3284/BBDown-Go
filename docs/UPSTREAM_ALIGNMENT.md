# 上游对齐追踪（Upstream Alignment）

> 本文件是 **上游基线 + 差异台账**：既把「落后多少版本」变成「缺哪些条目」，也逐条登记我们
> **主动偏离上游**的地方（§4.x 差异表）——同步进度与有意差异分开记，互不混淆。
> 目的：让每次同步可复现、可交接；让每条偏离都有据可查（取舍理由 + 用例 + 变异验证）。
> 建立日期：2026-09-19 ｜ 结论基于 `upstream/master` = v1.6.20（tag `5de84c4`，merge `165d075`）。

## 1. 参照源

| 名称 | remote / ref | 版本 | commit | 日期 |
|---|---|---|---|---|
| 上游（权威参照） | `upstream` → aliveranme/BBDown | **v1.6.20** | `165d075`（tag `5de84c4`） | 2026-09-24 |
| 本项目 C# 快照分支 | `origin/master` | v1.6.10 + 2 补丁 | `9fb96f2` | 2026-08-11 |
| Go 重写（工作分支） | `origin/main` | **1.6.20-go** | — | — |

同步状态：`origin/master` 是 `upstream/master` 的**严格祖先**（`0 ahead / 124 behind`），可纯快进，无分叉、无本地独有提交。

Go 侧自报版本硬编码于五处：`cmd/bbdown/main.go`（横幅）、`internal/cli/root.go`（`Version` 与启动更新检查）、
`internal/cli/commands.go`（serve 的更新检查）、`PKGBUILD`（`pkgver`），另有 `CHANGELOG.md` 记变更。

### 1.1 项目谱系（重要）

真正的原始上游是 **nilaoda/BBDown**；`aliveranme/BBDown` 本身是它的 fork，且已领先原文 **313 个提交**
（依据上游 `docs/FORK_DIFFERENCES.md` 头部快照基准）：

```
nilaoda/BBDown (C# 原版)
  └─ aliveranme/BBDown (fork：深度重构 + DRM + serve API + 4 个子命令 + 测试/CI)
       └─ QC3284/BBDown-Go (本仓库：main = Go 重写，master = C# 快照)
```

**对齐对象是 aliveranme fork，不是 nilaoda。** 含义：

1. 上游 fork 已有成熟的「同步更上游」流程（`FORK_DIFFERENCES.md` §6），本仓库可以照搬同一姿态 —— 我们的对齐义务止于 aliveranme，nilaoda 的增量由上游 fork 消化。
2. 上游 fork §6.2 列出的冲突热点（`Parser.cs`、`HTTPUtil.cs`、`BBDownApiServer.cs`、`MyOption.cs`、`SubUtil.cs`、各 Fetcher）与 §2 的改动热点表**高度重合** —— 即上游重构最深的地方就是本次对账的重点。
3. 上游承诺「相对 nilaoda 的 57 个 CLI 选项零删除」，本仓库 README 列出的选项集与之吻合，说明 Go 侧继承了 fork 的选项面。

### 1.2 上游可复用的对照资产

| 资产 | 用途 |
|---|---|
| `BBDown.Tests/`（659 例） | 行为规格的第二来源；`failSkips: true` 防静默跳过 |
| `BBDown.Tests/FakeBilibiliApiServer.cs` | 假 B 站服务器，集成测试驱动完整录制/下载循环 |
| `BBDown.Tests/Fixtures/parser/*.json` | 20+ 真实响应夹具（`biz-error`/`missing-nodes-tolerant`/`dash-reparse-pass1,2`/`drm-dash-badkid`/`play-limited` …）**可直接移植成 Go 测试** |
| `docs/REVIEW_FINDINGS.md` | RF-1..RF-88 缺陷清单（含已采纳/技术债/维持现状/待议四类） |
| `AGENTS.md` / `CONTRIBUTING.md` / `SECURITY.md` / `CHANGELOG.md` | 工程与流程姿态；本仓库当前只有 `ci.yml`，无 CHANGELOG、无协作规范 |
| `docs/wiki/`（15 页 + `scripts/sync-wiki.ps1`） | 用户文档主体 |

## 2. 差距规模

v1.6.11 → v1.6.19：**8 个 release / 111 个提交 / 163 个文件（+16386 −1892）**。

上游为**三项目结构**：`BBDown/`（CLI 与管线，`Application/*.cs` 是 `partial class Program` 拆分）、`BBDown.Core/`（`Parser.cs`/`Fetcher/`/`DRM/`/`Util/HTTPUtil.cs` 真身）、`BBDown.Tests/`（含 `FakeBilibiliApiServer.cs` 假服务器与 `Fixtures/parser/*.json` 夹具）。该结构在 v1.6.11 已存在，区间内**无新增源文件**（新增的是 `docs/`、`AGENTS.md`、`scripts/`、CI 与测试夹具）。

源码改动热点（变动行 = 新增 + 删除）：

| 上游文件 | 新增/删除 | 变动 | 对应 Go 包 |
|---|---|---|---|
| `Application/Download.cs` | +850 / −507 | 1357 | `internal/download`、`internal/workflow` |
| `BBDown.Core/Util/HTTPUtil.cs` | +686 / −122 | 808 | `internal/util/http.go` |
| `Infrastructure/BBDownApiServer.cs` | +558 / −79 | 637 | `internal/server` |
| `Infrastructure/BBDownDownloadUtil.cs` | +545 / −137 | 682 | `internal/download` |
| `Infrastructure/LiveStreamUtil.cs` | +467 / −107 | 574 | `internal/live` |
| `BBDown.Core/Parser.cs` | +229 / −78 | 307 | `internal/parser` |
| `BBDown.Core/DRM/WidevineCdm.cs` | +171 / −75 | 246 | `internal/drm` |
| `Commands/SubCommand.cs` | +123 / −60 | 183 | `internal/substore` |
| `BBDown.Core/Logger.cs` | +164 / −8 | 172 | `internal/util/logger.go` |
| `Infrastructure/BBDownMuxer.cs` | +127 / −34 | 161 | `internal/muxer` |
| `Utilities/BBDownUtil.cs` | +86 / −42 | 128 | 分散 |
| `Commands/LiveCommand.cs` | +79 / −47 | 126 | `internal/cli`、`internal/live` |
| `Commands/WatchLaterCommand.cs` | +69 / −53 | 122 | `internal/cli` |
| `Infrastructure/BBDownLoginUtil.cs` | +87 / −16 | 103 | `internal/login` |
| `BBDown.Core/Util/PathUtil.cs` | +75 / −3 | 78 | `internal/util/file.go` |

## 3. 可复现的检查步骤

```bash
git fetch upstream --tags
git rev-list --left-right --count origin/master...upstream/master   # 0  ahead / N  behind
git log --oneline origin/master..upstream/master | head -40
git diff --stat origin/master upstream/master

# 逐版本行为要点（1.6.12 ~ 1.6.19）
git show v1.6.19:CHANGELOG.md
# 上游自我审查发现条目 RF-1..RF-88（含采纳/技术债/维持现状/待议）
git show v1.6.19:docs/REVIEW_FINDINGS.md
# C# 实现真相
git diff v1.6.11 v1.6.19 -- BBDown/
```

上游额外产出的可复用资产（Go 仓库尚未对应）：`docs/REVIEW_FINDINGS.md`、`REVIEW_PLAN.md`、
`OPTIMIZATION_PLAN.md`、`FORK_DIFFERENCES.md`、`MAINTENANCE_PLAN.md`、`PROJECT_ANALYSIS.md`、
`docs/wiki/*`（13 篇）、`AGENTS.md`。

## 4. 条目清单与判定

判定口径：

| 标记 | 含义 |
|---|---|
| ✅ | 已对齐 —— Go 有等价实现，附 `文件:行号` |
| ⚠️ | 不等价 —— 部分覆盖或语义不同 |
| ❌ | 缺失 —— 需补，指出应落位置 |
| ➖ | N/A —— C# 特有（culture 敏感、async-over-sync、AOT 绑定、NuGet/CI 门禁、异常类型体系差异等） |
| ❓ | 待验证 —— 静态阅读无法判定，需运行或集成测试 |

审计按 Go 包簇分 5 片并行进行，明细见下表：

| 片 | 范围 | 上游源码 | Go 包 | 报告 |
|---|---|---|---|---|
| A | serve / API 服务器 | `Infrastructure/BBDownApiServer.cs`、`Commands/ServeCommand.cs`、`Program.cs` | `internal/server`、`internal/cli` | `docs/alignment/A-serve.md` |
| B | 下载 / 混流 / 外部程序 / 进度 | `Application/Download.cs`、`Infrastructure/BBDownDownloadUtil.cs`、`BBDownMuxer.cs`、`BBDownAria2c.cs`、`ExternalProcessRunner.cs`、`Utilities/ExternalToolHelper.cs`、`Application/{Archive,PathHelper,Pages,TrackSort}.cs`、`ProgressBar.cs` | `internal/download`、`internal/muxer`、`internal/workflow` | `docs/alignment/B-download-muxer.md` |
| C | live / article / sub / watchlater / login | `LiveStreamUtil.cs`、`Commands/{Live,Sub,WatchLater,Article,Login,LoginTV}Command.cs`、`Infrastructure/{ArticleUtil,SubscriptionStore,BBDownLoginUtil,ConsoleQRCode}.cs`、`BBDown.Core/Util/ServerClock.cs`（新增） | `internal/live`、`internal/article`、`internal/substore`、`internal/login`、`internal/cli` | `docs/alignment/C-live-article-login.md` |
| D | 网络 / 凭据 / URL / 配置 / CLI | `BBDown.Core/Util/HTTPUtil.cs`（真身）、`BBDown.Core/{Logger,AppHelper,Config,AppSettings}.cs`、`Util/PathUtil.cs`、`Utilities/UrlResolver.cs`、`Configuration/{BBDownConfigParser,MyOption}.cs`、`Program.cs` | `internal/util/http.go`、`internal/util/logger.go`、`internal/workflow/resolve.go`、`internal/config`、`internal/cli` | `docs/alignment/D-http-config-cli.md` |
| E | fetcher / parser / DRM / 评论 / 弹幕字幕 | `BBDown.Core/{Parser.cs,Fetcher/*.cs,DRM/*.cs,DanmakuUtil.cs,Entity/Entity.cs}`、`Util/{SubUtil,RiskControlResponseException,BilibiliBvConverter}.cs`、`Infrastructure/CommentUtil.cs` | `internal/fetcher`、`internal/parser`、`internal/drm`、`internal/appapi`、`internal/util/{danmaku,subtitle}.go` | `docs/alignment/E-fetcher-parser-drm.md` |

### 4.1 判定总表（五片已回收，2026-09-19）

| 片 | 范围 | 条目 | ✅ | ⚠️ | ❌ | ➖ | ❓ |
|---|---|---|---|---|---|---|---|
| A | serve / API 服务器 | 33 | 11 | 6 | 15 | 1 | 0 |
| B | 下载 / 混流 / 外部程序 / 进度归档 | 39 | 10 | 10 | 15 | 3 | 1 |
| C | live / article / sub / watchlater / login | 35 | 8 | 5 | 17 | 5 | 0 |
| D | HTTP / 凭据 / URL / 配置 / CLI | 34 | 8 | 8 | 14 | 4 | 0 |
| E | fetcher / parser / DRM / 评论 / 弹幕字幕 | 46 | 13 | 6 | 23 | 4 | 0 |
| **合计** | | **187** | **50** | **35** | **84** | **17** | **1** |

**❌ 84 / 187 ≈ 45%** 的上游行为变更在 Go 侧没有落点；➖ 17 条为 C# 特有（culture 敏感、async-over-sync、AOT、CI 门禁、异常类型体系），天然无需移植。

### 4.2 P0 队列（正确性 / 安全 / 数据丢失）

| # | 条目 | 影响 | 证据（Go 侧） | 验证状态 |
|---|---|---|---|---|
| 1 | **混流输出选项位置错**（B#15，上游 v1.6.16 已修） | 封面+章节 / ≥2 字幕 / 字幕+章节 / 任何背景音轨 → **混流必败** | `internal/muxer/muxer.go:68-110`、`:126-132` | **已双向复现**：审计员 + 本会话独立复现，ffmpeg n9.0.1 `exit=234`；选项移到全部 `-i` 之后即 `exit=0` |
| 2 | **mp4box 分支无 `audioMaterial`**（B#22） | 杜比视界自动切 mp4box 时配音/背景轨静默丢弃，随后被删除 → **永久丢失** | `internal/muxer/muxer.go:24-38`、`:227`；`internal/workflow/workflow.go:807-809` | 代码级确认 |
| 3 | **占位符与 id 路径穿越**（B#30/#31 + E） | `<aid>/<cid>`、`<dfn>/<res>/<fps>/<videoCodecs>/<audioCodecs>`、`lan/audio_id` 裸替换；镜像站 / `--insecure` 中间人可写出 `--work-dir` 之外 | `internal/download/downloader.go:846-861`；`internal/entity/entity.go:9-23`；`internal/util/subtitle.go:72` | 上游 RF-48/58/63/73；**两片独立命中** |
| 4 | ~~**serve 读/写门禁整片缺失**（A#1）~~ **已修复（2.0.0）** | 未配 token 时 `tokenMiddleware` 曾根本不挂 → 无 Host 校验 + 无 Origin/Content-Type 校验；现 guardMiddleware 无条件安装三门禁（qa 2.14.0 实测：text/plain→415、跨源→403、坏 Host→403，先于白名单解析） | internal/server/server.go 的 guardMiddleware；t8 补了页面请求的 Content-Type（TestWebUIActionRequestsSatisfyGuardMiddleware） | 关闭（A-serve 第 15/17 行已同步修订） |
| 5 | **携凭据请求可被 3xx 引走**（C#2 + D#1） | WEB 轮询 / TV `auth_code`+轮询 / gRPC POST 均未拦 3xx，SESSDATA 与轮询下发的 `access_token` 可被引向任意主机 | `internal/login/login.go:69`、`:149`、`:191`；`internal/util/http.go:193` | D 用本机探针实测：跨主机名标准库剥 Cookie、**同主机名不同端口不剥** → 定 ⚠️ 而非 ❌（RF-13/RF-37 未落地） |
| 6 | **直播录制删除已录内容**（C#1） | `defer os.RemoveAll(segRoot)` 在所有返回路径删掉含历次保留分段的整棵 `.segs`，三处日志却称"已保留"；读中断先删段再判取消 → Ctrl+C 丢整段 | `internal/live/live.go:124`、`:158-161`、`:140/:164/:237` | **本会话抽验确认**；另发现已写字节数 `n` 在该路径被直接丢弃 |
| 7 | **畸形响应直接 panic 崩进程**（E#1） | 零字节 `device.wvd`、截断 wvd、垃圾 protobuf（`int(length)` 溢出绕过判界）、DRM 许可证解析无边界检查；**全仓 `recover()` 0 命中** | `internal/drm/device.go:43`、`:62`、`:66`；`internal/appapi/appapi.go:321-326`；`internal/drm/widevine.go:434/467/507` | **本会话抽验确认**：`device.go:43` 对零字节直接取 `data[0]`；`:62/:66` 用文件内 uint16 长度切片且无判界 |

### 4.3 P1 队列（用户可见行为不一致）

- **直播韧性整批未落地**（C）：合并产物大小校验（0.8 阈值 + staging）、`LiveRecordResult` 三态、`qn=30000` + 回落（`live.go:59` 仍 `qn=10000`）、FLV 分段尾裁剪、终结态区分（无 flv / 写盘失败不重试）、录制前加载本地凭据（`commands.go:124` 不调 `InitSessionAsync`）；`live.go:250` 用 `http.DefaultClient`（Timeout=0）且无 60s 读停滞看门狗；`reconnectLimit=3` 与"不限次退避重连"相悖。
- **serve 关停与持久化**（A）：任务 ctx 取自 `context.Background()`（`server.go:315`），关停只 `taskWG.Wait()`（`:178-190`）→ 在途任务不落 Cancelled、不落盘；固定 `.tmp` 名 + 每任务并发写盘（`:699`、`:333`）、`finishedTasks` 内存列表永不裁剪（`:426`、`:683-693`）、`ErrorMessage` 未脱敏路径（`:356`、`:390`）；认证失败限速与字典有界（RF-9/RF-74）、token 常量时间比较、`BBDOWN_SERVE_TOKEN` 环境变量优先、`--trusted-proxy`、`/add-task` 429 缺 `Retry-After`（RF-83）、`/get-tasks` 缺 `no-store/nosniff` 与查询限流、`ParsePageSelection` 缺累计上限（RF-81 半对齐）、413 退化为 400。
- **配置与文件名**（D）：`cliHasUrl` 误判未修（`internal/config/parser.go:88-94` 全量扫描 argv，`--aria2c-proxy http://…` / `--work-dir av123` 会压掉配置里的 URL，上游 RF-7 已修）；`internal/util/file.go:35-69` 无 `TrimEnd`，Windows 上 `video.` / `video ` 落盘失败，另缺 100 字符基名截断。
- **DRM 安全纵深**（E2）：取钥链全程无 context（`internal/drm/decryptor.go:13`、`widevine.go:246-256`、`workflow.go:855`）→ serve `/cancel` 与 Ctrl+C 在取钥窗口（最长约 6 分钟）无效（RF-35）；无密钥内存清零；临时 key 文件覆写长度 64/65 字节（`internal/drm/mp4decrypt.go:50`）。
- **解析层静默语义**（E3）：`internal/fetcher/bangumi.go:95`、`intl_bangumi.go:119` 无条件丢预告 ep 且未命中 ep 不报错 → `workflow.go:1279-1281` 回落 ALL **整季静默下载**；反向的 intl 空轨道（`internal/parser/parser.go:487-514` 无 code / play-limited 校验）在 `workflow.go:761/820` 记为成功并入档。
- **其余**（B/C）：多段 FLV 中间 `.ts` 无 `finally` 清理（`muxer.go:343-367`）；DOVI 探针无 5s 超时（`muxer.go:192-206`）；`--comments` 父目录不建；SubOnly 恒 `.srt`；无事务化 `.muxing` 临时产物（直写 `savePath`，半成品会被"存在即跳过"）；入档按分 P 而非 aid；VOD 读停滞无看门狗；cover-only 无提前 return（会下全量）；aria2c URL 未剔除换行；`sub check` 逐 aid 只 `LogWarn+continue`、末尾恒 `return nil`（`commands.go:437-447`，部分失败仍以退出码 0 结束）；article/live 不读 `--work-dir` 且未过保留名净化；登录 `code==0` 与空 `access_token` 校验缺失（`login.go:104`、`:221-226`）。

### 4.4 结构性结论

1. **上游最大的主题在 Go 侧天然消失。** 「异常类型穿透两级失败隔离过滤器」一族（RF-14/31/43/44/47/49/52/64/72…）源于 C# 异常体系；Go 用 error 返回值 + 页面级 `bool`，**无该逃逸面** —— B、E 两片独立得出同一结论，共 10+ 条判 ✅。这是移植量最大的净减项。
   但**唯一逃逸通道是 panic，且全仓 `recover()` 为 0** → 收口 panic 边界即可整族关闭（见 P0#7）。
2. **三个横切设施谁修都得先有**，否则补一条漏一条：
   - **日志净化**：全仓只有 `internal/workflow/workflow.go:970` 的 `maskSecret`（仅 DRM 密钥），无 `SanitizeLogString`/`SanitizeServerText`/控制字符剥离 → RF-54/RF-70 无落点，A/C/D/E 四片都会被触发。
   - **路径段净化**：无 `PathUtil.SanitizePathSegment` 等价物（P0#3 的收口点）。
   - **响应体上限**：5 处裸 `io.ReadAll`，无 64MB / gzip 48MB 上限（RF-28/51/79）。
3. **测试基座为零。** `internal/` 下 0 处 `httptest`、0 个 `testdata`；19 个测试文件全是纯函数单测。上游的 `FakeBilibiliApiServer` + `Fixtures/parser/*.json` 对 Go 仓库**不是移植而是从零建**。E 已把上游 18 个夹具逐条标注 Go 预期红/绿，其中 `drm-dash-badkid`、`dash-reparse-pass1/2`、`durl-replay-first/empty` 四项**当前必红**。
   → **建议建基座排在补具体行为之前。**
4. **DRM 子系统是全仓最薄弱环节**：无取消贯通、无密钥零化、临时 key 文件覆写长度不足、许可证解析无边界检查。

### 4.5 决策记录（均已定案）

| # | 事项 | 结论 | 落地 |
|---|---|---|---|
| 1 | **免二压重发机制整体缺失** | **补齐** | 已按上游 `ParseDash` 的 `reparsePass` 实现：qn=0 首请求 + qn=127 重取，仅带 `dash.video` 的文档整体接管，重取被拒/失败沿用首轮，用户取消向上传播。原本被它阻塞的 5 个夹具（`dash-reparse-pass1/2`、`durl-replay-first/empty`、`flv-durl`）全部转绿 |
| 2 | ❓ RF-45（免二压重发降级丢杜比音轨） | **随 #1 一并解决** | 接管时才重置 dolby/flac 追加标记的语义已由「整体替换文档后统一解析」覆盖 |
| 3 | 版本号策略（D4） | **`<上游版本>-go`** | 已升至 **1.6.19-go**：banner / cobra `Version` / 更新检查 tag / README / PKGBUILD 同步 |
| 4 | 协作姿态（D5） | **引入** | 新增 `CHANGELOG.md`（Keep a Changelog，版本号语义写进头部）与 `AGENTS.md`（布局、最高约束、测试纪律、上游同步步骤） |
| 5 | 修复批次划分 | P0 → 横切设施 → 测试基座 → P1 分批 | 已按此顺序执行完毕 |

### 4.6 修复记录

| 批次 | 条目 | 改动 | 回归测试 | 变异验证 |
|---|---|---|---|---|
| 1 | **P0#1 混流输出选项位置** | `internal/muxer/muxer.go`：`-metadata:s:*` / `-disposition` / `-map_chapters` 从「紧跟各自 `-i`」改为累积到 `outputOpts`，等全部输入就位后统一落位 | 新增 `internal/muxer/muxer_test.go`（该包此前**零测试**）：参数顺序断言 + 真实 ffmpeg 端到端（封面+章节+字幕） | 撤掉修复后两条全红，端到端复现 `exit status 234`；恢复后全绿 |
| 1 | **P0#7 畸形响应 panic** | `internal/drm/device.go`：空文件守卫 + `parseWvd` 两处长度判界；`internal/drm/widevine.go`：新增 `readDelimited` 有界读取，三处解析器改用它，并补 `n==0` 守卫与未知 wire type 的 `default` 退出；`internal/appapi/appapi.go`：`walkFields` 长度改按 `uint64` 比较（原 `int(length)` 溢出可绕过判界）+ varint 守卫 | 新增 `internal/drm/parsers_test.go`、扩展 `internal/appapi/appapi_test.go` | 撤掉修复后 panic：`index out of range [0] with length 0`、`slice bounds out of range [11:10]`；恢复后全绿 |

> 顺带修掉一个未登记的缺陷：`parseLicenseKeys` / `parseKeyContainer` 在 wire type 为 1/3/5 时 **`pos` 不推进 → 死循环**（同一批畸形输入即可触发挂死，比 panic 更隐蔽）。已加 `default` 分支退出。

### 4.7 第二轮修复（P0#2 – P0#6）

| 条目 | 改动 | 回归测试 | 变异验证 |
|---|---|---|---|
| **P0#2 mp4box 丢配音/背景轨** | `muxer.go`：`muxByMp4box` 增 `audioMaterial` 形参，配音轨进入 `-add` 链并以 `-udta` 命名（与上游 v1.6.17 逐行对齐）；`workflow.go`：背景轨只在**确实混流**的分支删除，`--skip-mux` 下改为 `OnSaved` 上报 | `internal/muxer/muxer_test.go` 新增 mp4box 参数断言（假 mp4box） | 去掉该循环 → `the dubbing track never reached the mp4box -add chain` |
| **P0#3 占位符与 id 路径穿越** | 新增 `util.SanitizePathSegment`（合法值恒等；分隔符/控制字符/`.`/`..` 收口，复用 `GetValidFileName` 的保留名防护）；接入 13 处：`downloader.go` 的 `<aid>/<cid>/<ownerMid>/<dfn>/<videoCodecs>/<res>/<fps>/<audioCodecs>`，`subtitle.go` 的 5 处字幕 `lan`/`langKey`/`key` | `internal/util/sanitize_test.go` | 放开 `..` 的快速路径 → 测试立即红 |
| **P0#4 serve 读/写门禁** | `server.go`：新增 `guardMiddleware` 并**无条件安装**（原先无 token 时中间件根本不挂）；Host 校验挡 DNS rebinding（仅回环监听生效，不影响 LAN + token 部署）；写端点要求同源 Origin 与 `application/json`；补 `no-store`/`nosniff` 与 429 的 `Retry-After`；抽出 `buildHandler()` 让「门禁是否真的挂上」可测 | `internal/server/guard_test.go`（3 条） | — |
| **P0#5 携凭据 3xx 外发** | `util/http.go`：新增 `IsTrustedCookieHost` 与 `credentialRedirectGuard`，共享 client 只允许跳到 B 站系域名，其余拒绝并报错（`DownloadClient` 有意不设：媒体 URL 合法跳 CDN） | `internal/util/http_test.go`（含真实 httptest 跳转的泄露断言） | 去掉 `CheckRedirect` → 两条测试全红 |
| **P0#6 直播录制丢内容 / 挂死** | `live.go`：`defer os.RemoveAll(segRoot)` 改为只清本会话目录、`.segs` 根仅在空时删除，失败时保留分段（三处日志不再说谎）；读中断不再先删分段，已写字节计入合成；新增 60s 读停滞看门狗与无限指数退避重连（3s→30s）；本地写盘失败经 `errLiveWrite` 归为终结态；留 `resolveLive` 测试缝 | `internal/live/stream_test.go`（5 条） | 恢复「先删段再判取消」→ `recorded bytes were discarded`；禁掉看门狗 → 挂 5s 报 `the watchdog did not fire` |

> **P0 队列已全部关闭（7/7）。** 新增 6 个测试文件（`muxer` 与 `drm` 两个包此前零测试），全量 14 个包通过；每条修复都以「撤掉修复必须让测试变红」验证过，避免留下假绿的回归网。

> **仍未处理**：§4.3 的 P1 队列（约 30 条）与 §4.5 的 5 项待决策。

### 4.10 第五轮：夹具基座扩容

上游 15 个 parser 夹具已全部落地到 `internal/parser/testdata/`，回放基座覆盖其中 7 个：

- 已接（绿）：`drm-dash-badkid`、`drm-dash`、`biz-error`、`missing-nodes-tolerant`、`dolby-flac-audio`（1 视频 / 4 音轨 — **DoVi + FLAC 追加在 Go 侧本来就正确**，此前只是静态推断，现有夹具背书）。
- **已接（跳过）**：`TestFixtureReparseProtocol` —— 免二压重发协议（qn=0 首请求 → qn=127 重发 → 失败回退首次响应）在 Go 侧**完全不存在**，单次请求。断言已写好，只等实现后删掉 `t.Skip`。
- 依赖重发协议、暂不可接：`dash-reparse-pass1/2`、`durl-replay-first/empty`、`flv-durl`（期望 2 次请求、第二次带 `qn=127`）。
- 需按 query 分流的假服务器：`intl-code0/1`、`bangumi-web-dash-*`。

> 该测试即 §4.5 待决策 1 的**具体形态**：决策不再是纸面讨论，而是一个断言写好的跳启用例。

### 4.11 第六轮：DRM 取钥链贯通 context

| 条目 | 改动 | 验证 |
|---|---|---|
| **E2 DRM 取钥不可取消**（上游 RF-35）+ RF-4 + RF-79 | 取钥链是线性的 `GetKeyWidevine → GetKeys → getKeysInternal → sendRequest`，一次贯通：`sendRequest` 改 `http.NewRequestWithContext`；`http.DefaultClient` 换成 `licenseClient`（**禁跟随重定向** —— 请求体是从设备密钥签出的 challenge，不能跳到别的主机，上游 RF-4）；响应体加 64MB 上限（RF-79）；`workflow.go` 调用点包 `context.WithTimeout(ctx, 2*time.Minute)` 给出整体上限 | 取钥窗口（最长约 6 分钟）现在随 ctx 取消 —— serve `/cancel` 与 Ctrl+C 生效；15 个包全绿 |

> 至此取钥链的三个问题（无取消 / 跟随重定向 / 无上限）一并收口。**剩余**：§4.3 的 P1 队列其余条目（serve 关停取消、解析层静默整季下载、直播韧性与 `qn=30000`、`sub check` 失败计数等）与 §4.5 的 5 项待决策。

### 4.9 第四轮：P1 起步

| 条目 | 改动 | 验证 |
|---|---|---|
| **D#2 配置合并 `cliHasUrl` 误判**（上游 RF-7，v1.6.14 已修） | `internal/config/parser.go`：URL 启发式原先**全量扫描 argv**，`--aria2c-proxy http://127.0.0.1:1080`、`--work-dir av123` 这类**选项值**会被当成目标 URL，进而丢弃 `BBDown.config` 里的真实 URL，表现为「缺少参数」。新增 `positionalTokens`（复用已有的 `aliasMap`/`boolFlags` 判断选项是否吃值，与 `IsSubCommandInvocation` 同一套规则），只对**位置参数**做 URL 判定 | 新增用例先红后绿：修复前合并结果把配置 URL 丢掉并留下悬空的 `--encoding-priority`；修复后保留配置 URL、且命令行真给出目标时仍然压制配置 |

> **下一步（明确起点）**：A 方案 —— 把剩余 9 个上游夹具（`dash-reparse-pass1/2`、`durl-replay-first/empty`、`dolby-flac-audio`、`intl-code0/1`、`bangumi-*`）接上同一个回放基座。其中 `dash-reparse-pass1/2` 正是「免二压重发机制整体缺失」那条待决策，接上后决策就从纸面讨论变成一个有红测试支撑的具体选择。因此**该做在 DRM 取钥传 context 之前**：基座就位后，后续 P1 每条都能顺手带一条回归用例。

### 4.8 第三轮：横切设施与测试基座

| 项 | 改动 | 验证 |
|---|---|---|
| **日志净化设施** | `internal/util/logger.go`：新增 `SanitizeLogString`（控制字符 → 空格、按 rune 截断到 512）与 `sanitizeLogArgs`，**在 sink 处按参数净化** 6 个日志方法（`Printf` 交互提示保持原样）。一次覆盖 `util.Log*` 的全部调用点，C/A/D/E 四片点名的 RF-54 / RF-70 从此有落点 | 新增 `internal/util/logsanitize_test.go`；变异后伪造的 `[伪造] 我是一整行假日志` 真的成为独立日志行、`\x1b[0m` 打到终端 |
| **parser 夹具基座** | 落地上游 6 个 `Fixtures/parser/*.json` 到 `internal/parser/testdata/`；新增 `fixture_test.go`：`httptest` TLS 假服务器 + `config.Host` 指向它 + `skipSSL` 客户端，把上游 `ParserFixtureTests` 的回放方式移植过来。**`internal/parser` 首次有测试** | 4 条用例（badkid / 正常 kid / 业务错误 / 缺节点容错），为后续把 15 个夹具全部接上留下了基座 |
| **顺带修掉两个偏差** | ① UGC playurl **硬编码 `api.bilibili.com`**，导致 `--host`（镜像站）在普通视频路径上失效；改为用 `Cfg.Host` 并新增 `apiBase`（上游 `WithApiScheme` 语义，允许 host 自带 scheme）—— 这同时也是夹具注入的前提。② `bilidrm_uri` 原先取 `//` 之后的**全部内容**当作 kid，畸形 URI 会把 `evil.example/path?x=1` 存成 key id；现仅接受 32 位 hex（`parseKidFromDrmURI`） | 变异回旧解析 → badkid 用例立即红 |

## 5. 已知缺口（对账前采样，待各片报告确认）

| 上游加固项 | Go 现状 | 初判 |
|---|---|---|
| Windows 保留名（CON/NUL/COM1） | `internal/util/file.go:16` `reservedNames` | ✅ 已对齐 |
| 文件名净化 | `internal/util/file.go:35` `GetValidFileName`、`internal/live/live.go:287` `SanitizeFileName` | ✅ 已对齐 |
| 429 补 `Retry-After`（RF-83） | `internal/server/server.go:303` 有 429，全仓库无 `Retry-After` | ❌ 缺失 |
| API 响应体 64MB 有界读取（RF-28/51/79） | `io.ReadAll` 5 处，无 `LimitReader` | ❌ 缺失 |
| 携凭据 GET 逐跳可信校验（RF-50） | `util/http.go` 的 client 未设 `CheckRedirect`，依赖 Go 标准库跨域剥离 Cookie 的默认行为 | ⚠️ 不等价 |

## 6. 决策记录

| # | 决策 | 结论 | 日期 |
|---|---|---|---|
| D1 | 参照源如何维护 | 新增 `upstream` remote（aliveranme/BBDown），保留 `origin/master` 作为本项目 C# 快照分支；对齐以 `upstream` 为准 | 2026-09-19 |
| D2 | 是否快进 `origin/master` | 暂不推送远端。本地以 `upstream/master` + tags 作参照即可满足对账；是否把 C# 快照推到 v1.6.19 待定 | 2026-09-19 |
| D3 | 对齐目标版本 | 目标 **v1.6.19**（8 个 release / 111 提交） | 2026-09-19 |
| D4 | Go 版本号策略 | **建议 <上游版本>-go**（如 `1.6.19-go`）：版本号即「已对齐到哪一版」的声明，一处可见、可被脚本校验。待补完 1.6.12+ 行为后落地，届时同步改三处硬编码与 `CheckUpdateAsync` 的 tag | 2026-09-19 |
| D5 | 是否引入 CHANGELOG / AGENTS.md 等协作姿态 | **建议引入**：Go 仓库当前无 CHANGELOG、无协作规范、CI 仅 build/vet/test/gofmt；上游有 Keep-a-Changelog + AGENTS.md + 格式硬门禁，可作为模板 | 2026-09-19 |

## 7. 维护约定

- 每次上游发版后重跑 §3 步骤，把新增条目追加到对应片的明细表。
- 判定必须带 Go 侧 `文件:行号` 证据；给不出证据就标 ❓，不要凭印象写 ✅。
- 本文件是**基线与差异的唯一台账**，不另建平行清单；`docs/ROADMAP.md` 只放候选与优先级，不放判定。
- **新功能与优化同样要登记**：与上游不同的行为写进对应 §4.x 差异表（取舍理由 + 用例 + 变异验证）；
  行为等价的内部重构只在 CHANGELOG 说明，不进差异表。

### 4.12 第七轮起：P1 队列批量推进

| 条目 | 改动 | 验证 |
|---|---|---|
| **serve 关停不取消在途任务**（A#2） | 任务 ctx 统一派生自可取消的服务基础 ctx；关停时先取消再等待，最后落盘 —— 原先既不落 Cancelled 也不落盘，30s 后被进程退出截断 | `shutdown_test.go`：预占执行槽使任务必然走取消分支，断言状态与落盘；变异后用例超时 |
| **serve 持久化三缺**（A#3） | 唯一临时名 + `persistMu` 串行化（共享 `<file>.tmp` 曾让并发写者抢同一路径）；内存 `finishedTasks` 同样按上限裁剪（`/get-tasks` 服务的就是它）；`ErrorMessage` 脱敏本机绝对路径并去控制字符 | 并发写 + 无残留临时文件用例 |
| **serve token 硬化** | 常量时间比较（明文 `!=` 经响应时间泄露密钥）；`BBDOWN_SERVE_TOKEN` 环境变量优先于旗标 | `token_test.go` |
| **`-p` 展开累计上限**（RF-81） | 原先上限按「每个范围」判定，`1-60000,1-60000` 可连过两次并展开 12 万项 | 用例覆盖累计与单范围|
| **超大请求体返回 413** | 原先退化为通用 400，客户端无法区分「太大」与「格式错」 | `body_test.go` |
| **`sub check` 部分失败返回 0**（C） | 末尾恒 `return nil`，全失败也报成功；现统计失败数并非零退出（主动取消仍为 0） | `subcheck_test.go` |
| **空 access_token 写盘**（C） | TV 登录 `default` 分支把空 token 写进 `BBDownTV.data`，留下看似已登录实则无效的凭据 | `token_test.go`（login） |
| **文件名卫生**（D#3） | 尾随点/空格（Windows 静默丢弃）、基名按 rune 截断到 100、全为点时回退、保留名判定后置 | `filename_hygiene_test.go`（含真实 `utf8.ValidString` 断言） |
| **aria2c 输入注入**（B） | URL 直接来自接口 `base_url`，可带换行注入 `out=`/`dir=` 选项；Cookie 早有防护而 URL 没有 | `aria2c_input_test.go`；变异后注入行真的出现 |
| **评论导出不建父目录**（B） | 路径来自保存模板，父目录不存在直接失败 | `comments_test.go` |
| **SubOnly 恒 `.srt`**（B，RF-18） | ASS/JSON 轨道会被改成扩展名与内容不符；现沿用真实扩展名并对 `lan` 段净化 | 代码级 + 净化函数已有用例 |
| **CoverOnly 不提前返回**（B） | 代码里写着「matching C#: no early return」，但上游明确 `return true` 并注明「否则…用户只要封面却白白下载完整视频」——**在码注释是错的**，现补上提前返回（无封面资源时报失败而非零产物成功） | 对照上游源码确认 |
| **混流非事务化**（B） | 直写 `savePath` 时中断会留下截断成品，而「已存在, 跳过下载」会把它永久当成已完成；现写唯一 `.muxing-*.mp4` 后改名 | 代码级 |
| **直播 qn=30000 + 回落**（C） | 原固定游客档 `qn=10000`；现请求 30000，该档无可用 flv 流时以 10000 再取一次 | `resolve_test.go`（回落 + 快路径两条） |
| **直播合并产物大小校验**（C） | ffmpeg 遇坏段会截断并仍以 0 退出，紧随其后的分段清理会静默丢掉整场录制；现产物显著小于源时保留分段并报错 | 代码级 |
| **直播录制前不加载凭据**（C） | client 用空配置构造，永远游客身份 —— 上述 qn=30000 因此永远落空；现走 `InitSession`（`--cookie` 优先，否则本地 `BBDown.data`） | 代码级 |
| **VOD 读停滞无看门狗**（B） | 媒体下载用无总超时的 client，连接既不 RST 也不 EOF 时 `io.Copy` 永久阻塞 | `stall_test.go`（停滞中止 + 慢速存活两条） |

> 经核实**与上游一致、不作为缺口**：CoverOnly 的「不提前返回」注释与上游行为相反，已按上游修正；`GetValidFileName` 的保留名处理原本已对齐。

### 4.13 剩余队列（2026-09-19 逐条核对后修正）

> **修正**：此前本节写「P1 队列已清空」是**错的** —— 那是按汇总时挑出的重点条目判的。
> 逐行核对五片报告的 ❌/⚠️ 行（约 60 条）后，确认仍有下列条目未处理。

**已完成（本轮补做，见 §4.15）**：mp4box 封面转义与 EscapeString 换行折叠、SRT `-->`` 与 ASS 反斜杠注入、
`baseURLRegex` 收紧、显式 epid 的预告保留、国际版多余的转义预替换、收藏夹翻页不再静默截断、
密钥临时文件按实际长度覆写、响应体 64MB 与 gRPC 48MB 上限、携 Cookie 前校验目标主机、
`SavePaths` 去重、已完成任务按创建时间裁剪、aria2c 兜底超时、`--skip-mux` 跳过 DOVI 探测、
大会员回退跟随镜像主机、VIP 判定走 JSON message、API 层有界重试。

**队列已清空**：截至 2026-09-19，五片报告中的 ❌/⚠️ 条目全部处理完毕（除下表列出的 N/A 项）。

本轮收尾的三项：

| 条目 | 改动 |
|---|---|
| 直播旧会话分段 | `staleSessions` 扫描 `.segs` 下的其他会话目录并**只提示不删**（可能是某次合成失败后唯一的副本） |
| SESSDATA 有效期 | `util.EstimateSessdataExpiryDays`：解析 URL 编码载荷首段的 base64 JSON `expires`，剩余 ≤7 天时告警、已过期时提示重新登录；解析失败 fail-open 返回 nil，不误报 |
| Logger 写失败退避 | 连续 5 次写失败后暂停文件日志 30 秒并输出一次提示，避免磁盘满/只读时每一行都重试一次注定失败的 open |


> 判定为「与上游一致、不作为缺口」：`SavePaths` 集合语义、`/health` 无需认证、格式串中的文化敏感项（Go 无此问题）。

**判定为「与上游一致、不作为缺口」**：`SavePaths` 集合语义、`/health` 无需认证、格式串中的文化敏感项（Go 无此问题）。

### 4.14 第八轮：P1 收尾

| 条目 | 改动 | 验证 |
|---|---|---|
| **入档粒度**（B，RF-75） | 多P稿件共享同一 aid，原先下完**第一个分P**就写进 `BBDown.archives`，下次运行余下分P被判定已下载而跳过。移植上游 `ArchiveTracker`：该 aid 全部分P成功才入档，任一页失败即本 run 内不再入档；单P稿件仍立即入档 | `archive_test.go` 5 条，对齐上游 `ArchiveGranularityTests` |
| **serve 认证失败限速**（A，RF-9/RF-74） | token 可被无限次尝试；现按客户端计数，10 次失败锁定 5 分钟并带 `Retry-After`，成功即清零，跟踪表有硬上限（防自身成为内存增长点） | 单元（锁定/复位/有界）+ 端到端（10×401 → 429 → 正确 token 仍通） |
| **`--trusted-proxy`**（A） | 限速按 IP 归属，而 XFF 可伪造；无条件采信等于给攻击者无限个假身份。现仅当请求确实来自配置的代理时取 XFF 最后一跳 | `proxy_test.go` |
| **直播分段尾裁剪**（C） | 网络中断/取消后段尾可能只剩半个 FLV 标签，concat demuxer 会在该处中止**整场**合成；现合成前逐段裁到最后一个完整标签 | `trimflv_test.go`（构造真实 FLV 标签序列） |
| **登录顶层 `code` 校验**（C） | 顶层 code 解析了却从未检查，风控/限流响应会落进 default 成功分支，最终以「回调 URL 为空」这种误导性原因失败 | `token_test.go`（login） |
| **intl 夹具接入** | intl 路径 scheme 改走 `apiBase` 使其可注入；新增按 query 分流的回放夹具 | `TestFixtureIntlMergesTwoPassStreamLists` —— **两趟 `prefer_code_type` 合并本来就是对的**，现有夹具背书 |
| **bangumi 夹具接入** | `result` 根（而非 `data`）的轨道映射 | `TestFixtureBangumiWebDashParsesTracks` |

**夹具覆盖**：15 个上游夹具中 **10 个已接（绿）**，其余 5 个（`dash-reparse-pass1/2`、`durl-replay-first/empty`、`flv-durl`）统一被「免二压重发」决策阻塞 —— 即 `TestFixtureReparseProtocol` 那一条跳启用例。接入过程中 **intl 两趟合并、bangumi 的 `result` 与 `result.video_info` 两种根形状、DoVi+FLAC 音轨追加** 四条「静态判定 ✅」被真实响应夹具证实为正确。
- **夹具**：按 query 分流的假服务器 → 接 `intl-code0/1`、`bangumi-web-dash-*` 四个夹具。
### 4.15 第九轮起：逐条核对后的补做

> 起因：本轮重新逐行核对五片报告的 ❌/⚠️（约 60 条），发现 §4.13 早先「队列已清空」的判断有误。
> 下列条目由此补做，每条都带回归用例。

| 类别 | 条目 |
|---|---|
| 转义与注入 | mp4box `-itags` 封面路径未转义（Windows 路径的反斜杠被当转义序列，封面静默丢失，RF-6）；`EscapeString` 缺 CR/LF 折叠（换行破坏 itags token 语法）；SRT 正文 `-->` 会错位后续字幕；ASS 弹幕字面反斜杠可注入排版标签 |
| 解析 | `baseURLRegex` 未锚定，query 里的「冒号+数字」被误判为端口而选错 base_url；用户显式指定的 epid 即便是预告也应保留；国际版多余的转义预替换会把「反斜杠+斜杠」归并丢数据；VIP 判定改以 JSON message 为准（裸子串仅兜底）；大会员回退抓网页时跟随 EpHost 镜像主机 |
| 网络 | API 层有界重试（5xx 与传输错误指数退避，4xx 不重试）；响应体 64MB 与 gRPC 解压 48MB 上限；gRPC 帧首字节合法性校验；**按服务器 Date 头校准时钟偏移**（本地偏差 >60s 时 WBI 签名被拒，该机所有下载都会失败） |
| 凭据 | 携 Cookie 请求发送前校验目标主机：仅官方域名与 `--host/--ep-host/--tv-host` 显式配置的主机可见 |
| 数据完整性 | 密钥临时文件按实际载荷长度覆写（固定 64 字节覆写 65 字节的 kid:key 行，最后一个字符仍留在盘上）；订阅历史保「最近」语义并按 5000 条裁剪 |
| 任务与工具 | `SavePaths` 去重（集合语义）；已完成任务的保留期与上限改按 `TaskCreateTime`（避免「后创建但先完成」被误删）；aria2c 6 小时兜底超时；`--skip-mux` 跳过杜比视界探测；收藏夹翻页不再对风控/空页静默截断 |

**方法论收获**：本轮 6 条修复是先写会红的用例再改代码；另有 4 条是「断言先写错、测试报红、回头发现是断言的问题」（行数、引号、测试路径、变量作用域）——**两种红都必须查清是哪一种**，否则会把断言错误当成实现错误去改。

### 4.16 第十轮：交互可见项复核（配色 / 二维码 / 布尔选项写法）

> 起因：用户提出「尝试修饰颜色」。核对方式是把每一处转义序列与上游源码里的 .NET
> ConsoleColor 名字对上，而不是凭观感猜；顺带复核了二维码渲染与命令行布尔写法。

| 类别 | 结论与改动 |
|---|---|
| 终端配色 | 上游 `ConsoleColor` 的 Red/Cyan/White 是**亮色**变体（对应 91/96/97），本实现写成了 31/36/37 这组暗色，横幅前景更只给了 37；抽 `Ansi*` 常量并改用。`LogWarn`(33)、`LogDebug`(90)、横幅背景(44) 本来就一致 |
| 二维码 | 上游 `ConsoleQRCode.GetGraphic` 把模块矩阵里为 true 的**深色**模块画成 `ConsoleColor.Black`(30)；本实现画反了（深色模块用白底 47），整张码是反色的——浅色主题终端上会扫不出来。`skip2/go-qrcode` 的 `Bitmap()` 为 true 即深色模块，极性据此对齐 |
| 命令行 | 上游（System.CommandLine）布尔选项 arity 为 0..1，其 README 就按 `--multi-thread false` 文档化「关闭多线程」；pflag 只认 `--flag=false`，`--flag false` 会把 flag 置 true 并把 `false` 漏成位置参数——实测被当成输入地址去请求 API，报 `获取视频信息失败 (code=-400)`。归一化层折平，长/短别名与配置文件同写法一并处理 |

**方法论收获**：这两条修复都不是读代码读出来的——配色是把转义序列与上游源码里的颜色名逐一
对照得到的；布尔写法则是**照着上游 README 的命令行原样敲一遍**才暴露的（复现命令
`BBDown --multi-thread false`：修复前输出的是 API 报错，而不是「关掉多线程」）。
**验收要走上游文档里的用户路径**，只看实现是否符合直觉会漏掉这类差异。

### 4.17 第十一轮：进度条渲染与聚合（用户报「为什么会有两个进度」）

> 起因：一次 27MB 无损下载的终端输出里出现两行进度。逐项对照上游 `BBDown/Infrastructure/ProgressBar.cs`
> 与 `BBDownDownloadUtil` 后确认有三处不一致；这三处此前**从未进入判定表**（A~E 五片报告都没覆盖进度条）。

| 类别 | 上游行为 | 本实现（修复前） | 修复 |
|---|---|---|---|
| 控制台写入串行化 | `Logger.ConsoleLock` 被 `ProgressBar` 复用，日志与进度条写入互斥 | 进度条直接 `fmt.Printf`，与日志无共同锁 | `util.ConsoleLock`，且日志写入前先给进度行收尾 |
| 结束时的进度行 | `Dispose` → `UpdateText(string.Empty)` 擦掉整行 | 补画一帧 100% 并换行，把最后一帧留在屏幕上 | 擦除整行，与上游同 |
| 多线程进度聚合 | `ProgressAggregator`：分片上报**累计**字节，按差值聚合，重试回退 | 只在分片下载**完**才累加 → 以分片数为台阶跳变 | 移植 `progressAggregator`（含上游三条断言的用例） |
| 收尾与后续日志的时序 | `using var progress` 保证 `Dispose` 先于后续日志 | 不等渲染协程，日志先打 | 渲染协程关闭 `stopped` 通道，主流程等它 |

**实测对照**（同一条命令 `--thread-segment-size 1`，真 PTY 下用 `tr '\r' '\n'` 切开逐帧还原）：

```text
修复前进度帧： 0.00% → 20.90% → 52.54% → 100.00%（台阶跳变，末帧留在屏幕上）
修复后进度帧： 0.00% → 34.99% → 63.41%（连续推进，随后整行擦除、日志独占新行）
```

### 4.18 第十二轮：按「上游日志字面量」扫描未审计面（用户问「还有哪些不一致」）

> 方法：①把上游 `BBDown/` 与 `BBDown.Core/` 的 75 个 `.cs` 按「文档是否提及」过一遍，
> 筛出从未进入审计报告的文件；②抽取上游 282 条 `Logger.Log*` 调用的字面量，
> 按「最长中文片段」做探针在本仓源码里比对，未命中的逐条人工判定。
> 两者互补：文件级扫描漏不掉整块功能，字面量扫描漏不掉「功能在、说法不在」。

| 位置 | 上游行为 | 本仓（修复前） |
|---|---|---|
| `Workflow.cs:142-153` | 头部四行：`视频标题:` / `发布时间:`(带 zzz 时区) / `视频URL:` / `UP主页:` | 标题与 URL 是裸值，UP 主页不打印，时间无时区 |
| `Workflow.cs:156` | 互动视频 + `-t` → 提示并改回默认解析 | `IsSteinGate` 有解析、无消费者，照 TV 跑 |
| `Display.cs:49,60` | `--only-show-info` 时每条流后打印 `baseUrl` | 参数被忽略，脚本拿不到取流地址 |
| `Display.cs:19-35` | 背景音频流 + 配音清单（两者都在时） | 只列视频流与音频流 |
| `Pages.cs:ExpandPageAliases` | 别名按段全词匹配，非别名段原样交给解析器报错 | 子串替换：`-p 1LATEST` 静默变成页码 112 |
| `Pages.cs:22-33` | 自动选集数时提示；`p` 参数按查询串解析 | 无提示；只认 `?p=` 开头，漏 `?a=1&p=4` |
| `Download.cs:553-563` | 零视频流/音频流时告警，only 模式下判失败 | 只清空轨道 → 零产物报「任务完成」 |

**仍然一致的部分**（扫描后逐条确认无差异）：四种解析模式与 URL 分发（`FetcherFactory` 六个前缀）、
DRM 取钥与解密（`Decrypt.cs` / `WidevineCdm` / `WvdDevice`：手动密钥、WVD 缺失提示、Key 与 Kid 必须同时
具备、临时密钥文件按实际长度覆写、超时后 Kill 进程树）、登录两条命令（薄壳，逻辑在 `BBDownLoginUtil`）、
`ServeRequestOptions`、`ParsedResult` 字段集、`BBDownEnums` 弹幕格式。

### 4.19 第十三轮：把上游测试表搬来做差分（用户说「那开始撞？」）

> 方法：上游 `BBDown.Tests/` 的数据表就是行为规格。逐文件把 `[Theory]/[InlineData]` 与 `[Fact]`
> 的输入输出搬进本仓用例，先跑一遍——红的就是与上游不一致的地方，再由修复把它们变绿。

| 上游测试文件 | 接入内容 | 撞出的差异 |
|---|---|---|
| `PageSelectionTests` | 8 条成功表 + 16 条拒绝表 + 16 条别名表 | `-p "1 - 3"`（范围两侧空白）本仓报错，上游合法 |
| `FormatHelperTests` | 7 条体积表 + 4 条时长表 | `<1KB` 本仓显示 `0.00 B`、且多一个 TB 档；上游 `{n} bytes`、无 TB 档 |
| `PathUtilTests` | 5 + 8 + 5 条文件名表 | 超长截断本仓加省略号并丢掉扩展名；上游保留扩展名、长度恰为上限 |
| `BilibiliBvConverterTests` | 3 条编码 + 7 条解码 + 3 条报错 | 无（解析层前缀剥离位置不同，行为一致） |
| `TrackSortTests` | 1 条畸形 id 表 | 清晰度 id 本仓按 64 位解析，`999999999999` 参与排序；上游 Int32 降级 0 |
| `UrlResolverTests` | 15 条输入表 + 4 条 BV 表 + 2 条拒绝用例 | 识别不出的输入本仓原样透传，上游报「无法识别的视频 URL 或 ID」 |
| `PathFormatTests` | 7 条模板选择表 | 无（本仓逻辑等价，顺手抽成与上游同名的 `resolveSavePathFormat` 以便钉住） |

**另由选项面扫描撞出**：`article` / `live` 缺 `-w` 简写（上游两个子命令的 settings 各自声明了
`-w|--work-dir`）。做法是把上游 `MyOption.cs` 与各 `*Command.cs` 的 `[CommandOption]` 抽出来，
与本仓 flag 注册做双向差集——79 个长选项一一对应，无缺失、无自造；再把每个子命令的选项集单独比对，
才发现这个只出现在两个子命令上的简写。

**已知且有意的差异**（记录在案，不再视为缺口）：`GetValidFileName("")` 本仓返回空串——调用方
用空串表示「无值」，返回 `"_"` 会让占位符缺失时凭空多出一个名为 `_` 的文件。

### 4.20 第十四轮：差分续跑（脱敏 / 分片合并 / 数值校验）

> 同一套办法继续搬上游测试表。这一轮挑的是「纯函数 + 表格式断言」的文件，
> 撞出的问题集中在**日志脱敏**与**合并的数据完整性**两处。

| 上游测试文件 | 撞出的差异 |
|---|---|
| `SensitiveDataMaskerTests` | ① 本仓漏掩签名媒体 URL 的 `sign`/`x_sign`/`w_rid`/`deadline`/`marlin_token`（**票据明文落盘**）；② 本仓用 net/url 往返脱敏，把 `***` 编码成 `%2A%2A%2A` 并重编码其它参数（上游是字符串手术）；③ 本仓误掩 `buvid3`（非凭据） |
| `CombineFilesTests` | ④ 合并失败留半截产物（上游删除）；⑤ 缺单分片 rename、自动建父目录、取消支持、空列表短路 |
| `NumericOptionValidationTests` | ⑥ **无差异**——本仓 `validateNumericOptions` 的范围与文案与上游逐字一致（顺带补上差分用例钉住） |

**契约变更**：`CombineMultipleFilesIntoSingleFile` 签名加上 `ctx`（对齐上游的 CancellationToken），
两个调用点（多线程分片合并、直播 FLV 分段合并）同步传入；既有用例与 muxer 用例按新契约更新。

### 4.21 第十五~十六轮：差分续跑（弹幕 / 字幕 / 充电 / 凭据 / 时钟 / UA / 专栏）

| 上游测试文件 | 结果 |
|---|---|
| `DanmakuFilterTests`、`DanmakuAssEscapingTests` | 无差异（关键词/midHash 过滤、ASS 大括号与反斜杠中和、BGR 颜色、点号时间轴） |
| `SubtitleFormatTests` | **两处差异**：毫秒按「小数 ×1000 截断」而非 tick 进位（`1.001s` 少 1ms）；NaN 未处理、极大值溢出；`SanitizeSRT` 只裁 ASCII 空白 |
| `UpowerGuardTests`、`SessdataExpiryTests`、`ClockCalibrationTests` | 无差异 |
| `HttpUtilUserAgentTests` | **一处差异**：下载路径写死 `Mozilla/5.0`，`--user-agent` 对媒体下载不生效 |
| `ArticleUtilTests` | 无差异（cv 号解析含大小写与 URL 形态；Markdown 头部与 HH:MM 时间） |
| `WorkDirResolutionTests` | 无差异（既有 `workdir_test.go` 已覆盖同一组契约，含 Windows 根相对路径） |
| `RedirectHopValidationTests`、`LoggerFileTests` | 上游用例依赖本地重定向服务/句柄夹具，本仓对应契约由 `credhost_test.go`、`loggerfail_test.go` 覆盖 |

### 4.22 第十七轮（目标轮 1~2）：实体 / 分发 / 配置合并 · 全部一致

| 上游测试文件 | 结果 |
|---|---|
| `EntityTests` | 无差异：`Page.Bvid` 对越界 aid（0 / 负数 / ≥2^51）回落**原始 aid**、非数字 aid 原样返回；`Audio.ShortCodecs` 大写去横线 |
| `FetcherFactoryTests` | 无差异：`mid:`/`favId:`/`listBizId:`/`seriesBizId:`/`ep:`/`cheese:`/裸援助各自落到对应 fetcher，`ep:` + `--use-intl-api` 落 IntlBangumiInfoFetcher，订阅目标都不会落到 NormalInfoFetcher |
| `ConfigMergeTests` | 无差异：空格与等号写法的命令行都压过配置文件、`--config-file=<path>` 被认、以 `-` 开头的配置取值不被吞、子命令调用整段跳过合并 |

### 4.23 第十八轮（目标轮 3）：JSON 取值 / 混流参数

| 上游测试文件 | 结果 |
|---|---|
| `JsonElementExtensionsTests` | **一处差异**：本仓 `gi`/`gi64` 用 `fmt.Sscanf("%d")`，它接受数字**前缀**——`"12abc"` 会变成 12、`"1.5"` 变成 1；上游 `int.TryParse(NumberStyles.Integer)` 要求整串是数字，脏数据必须落到默认值 0。另 `gi` 未做 int32 范围判定（上游 `TryGetInt32` 失败即取默认值）。改为整串解析 + 范围判定。 |
| `MuxerArgsTests` | 无差异（**加强既有用例**）：补上上游的三条断言——章节 meta 是第 5 个输入（下标 4）、`-map_chapters` 指向它自身、`-map` 序列不含该下标（meta 只供取章节）。原本只断言 `-map_chapters` 存在与顺序，现按上游口径钉死。 |

### 4.24 第十九轮（目标轮 4）：风控页识别 / 会话传播 / 重试策略

| 上游测试文件 | 结果 |
|---|---|
| `HttpUtilRetryTests` | **一处差异**：本仓**完全没有**风控页识别。上游在 API 响应是 HTML 时抛 `RiskControlResponseException`（“疑似风控页：接口返回 HTML 而非预期数据…”），本仓把 HTML 直接交给 `json.Unmarshal`，用户看到 `invalid character '<' looking for beginning of value`——而风控页/登录墙恰恰都以 HTTP 200 + HTML 返回。现加 `util.LooksLikeHTMLPage` + `util.UnmarshalJSON`（剥前导空白与 BOM 后判首字符），parser/fetcher/评论导出的 22 处解析统一走它。其余重试语义（5xx 重试、4xx 不重试、耗尽即停、请求计数精确、body 上限）与上游一致，既有 `retry_test.go` 已按相同口径断言。 |
| `ConfigPropagationTests` | 不适用于 Go：该文件用反射检查 C# 的 `AsyncLocal` 配置传播（子方法写 Config 不回流父流程，须显式返回并应用）。本仓等价契约由返回值承担——`workflow.InitSession` 返回 `(wbi, error)` 并回写 `*cfg`，三处调用点（watchlater / sub check / serve 任务）都把返回的 wbi 传给 `fetcher.NewFactory`，不存在“子方法写完父流程看不见”的形态。 |

### 4.25 第二十一轮（目标轮 5）：SSL 策略 / 不跟随重定向

| 上游测试文件 | 结果 |
|---|---|
| `HttpUtilSslPolicyTests`、`VerifiedNoRedirectClientTests` | 无差异（**补上安全前提的钉住用例**）。上游用反射比较「校验池/不安全池是不是同一个实例」，Go 侧无对应机制；改按可移植的行为断言钉住三件事：① 许可证请求不跟随重定向（307 原样返回，目标一次都没被请求）；② 许可证请求始终校验证书（自签站点必然失败，不随 `--insecure` 降级）；③ `--insecure` 真的切换校验（skipSSL=true 能连自签、false 拒绝）。为可测性把 `licenseURL` 由常量改为变量（用例指向本地服务器），生产值不变。 |

### 4.26 第二十二轮（目标轮 6）：选项默认值 / buvid3 / 取消归类

| 上游测试文件 | 结果 |
|---|---|
| `OptionDefaultsBindingTests` | 无差异（补钉住用例）：帮助文本标称「默认开启」的三个布尔选项（`--multi-thread` / `--skip-ai` / `--force-replace-host`）默认值确为 true，`--force-http`/`--use-tv-api`/`--only-show-info`/`--audio-only` 确为 false。上游当年踩的是 Spectre 把未出现的 flag 写回 false 覆盖初始化器，Go 侧无此机制，但默认值本身值得钉住。 |
| `BuvidProviderTests` | 无差异：`HasBuvid3` 与上游逐字一致（`contains("buvid3=", OrdinalIgnoreCase)`），`buvid4=` / `buvid=` 不会误判。 |
| `CancellationClassificationTests` | **一处差异**：本仓按 `ctx.Err() != nil` 判「已取消」，它把 **DeadlineExceeded 也算成用户取消**；上游只认 `CancellationRequested`，超时必须归 Failed 并保留原始错误。另外上游对真取消给出统一文案「已取消」，本仓此前落的是 `err.Error()`。现抽出 `classifyTaskCancellation` 按上游口径归类。 |

### 4.27 第二十三轮（目标轮 7）：播放限制文案 / 官方域名白名单 / serve token

| 上游测试文件 | 结果 |
|---|---|
| `ParserPlayLimitTests` | **一处差异**：本仓播放受限只报「播放受限: limit_play_reason=…, play_detail=…」，**不给原因**；上游按 `limit_play_reason` 映射为「区域限制 / 付费限制 / 需要大会员 / 尚未到可播放时间 / 存在播放限制」，用户据此才知道是哪种限制。现抽出与上游同名的 `throwIfPlayLimited` / `throwIfBizError`（后者只在根是对象且 `code` 是 **JSON 数字**且非 0 时报错，字符串 code 不算）并对齐文案。`isVipRestricted` 与上游一致（JSON message 优先、非 JSON 回落裸子串）。 |
| `ServeCommandTests` | **一处差异**：官方域名白名单少一个 **`biliapi.com`**——上游 `HTTPUtil.OfficialHostSuffixes` 是 9 项，本仓 8 项。该域是官方 API 镜像，漏掉会让凭据校验误判「非可信主机」拒绝发 Cookie、重定向守卫也会误拦。补齐后白名单逐项一致（含子域匹配与否定的 4 例）。token 解析（`BBDOWN_SERVE_TOKEN` 优先、空串回落旗标）抽成 `resolveServeToken` 并钉住 7 例，无差异。 |

### 4.28 第二十四轮（目标轮 8）：gRPC 帧 / .wvd 加载

| 上游测试文件 | 结果 |
|---|---|
| `AppHelperMessageTests` | **一处差异**：gRPC **空载荷**帧（flag=0、length=0）本仓报 `invalid gRPC payload length: 0`，上游返回空数组——空载荷是合法响应。其余（帧头不足 5 字节、压缩标志只能 0/1、gzip 往返、解压上限）与上游一致。 |
| `WvdDeviceKeyTests` | **一处差异**：**无 magic 的 v2 .wvd 被误判为「无法识别的 WVD 文件格式 (首字节: 2)」**——本仓探测只放行首字节 1（v1），而上游探测放行 1/2（`ParseWvd` 本身两种都支持）。上游注释里专门记了这个坑（RF-78），我们踩的是同一个。此外空文件、截断、加密 v2、垃圾数据的诊断与上游一致。 |

### 4.29 第二十五轮（目标轮 9）：dubbing_info 解析 / 配音下载（整块缺失）

| 上游测试文件 | 结果 |
|---|---|
| `DownloadPipelineTests` | **一处整块缺失**：上游 `Parser.cs` 会读 `data.dubbing_info`（`background_audio` 与 `role_audio_list`，门控为 **APP API + 番剧**），本仓**完全没有这段解析**——于是 `ParsedResult.BackgroundAudioTracks` / `RoleAudioList` 永远是空的，工作流里那两段下载循环（背景音轨、配音）等同于死代码。现补上同名提取（含 `backup_url` 选择、带宽 /1000、`audio_id` 净化后拼路径），并为**配音**接上下载与混流物料（`ClampRoleAudioIndex` 按 role 自己的列表夹取，越界钳末位、空列表跳过——上游同款表已接入）。`entity.AudioMaterialInfo` 增加 `AudioID` 字段承载该 role 的 `audio_id`。 |

### 4.30 第二十六轮（目标轮 10）：收尾盘点 · 61 个上游测试文件全部定性

| 上游测试文件 | 定性 |
|---|---|
| `BBDownLoginUtilMergeTests` | 无差异（**已接**）：新版协议 Set-Cookie 才是真凭据来源，本仓 `MergeLoginCookies` 的合并/属性剥离/`;` 连接/名称归一语义一致 |
| `WidevineCryptoTests` | 无差异（**已接**）：AES-CMAC 用 RFC 4493 四组已知答案向量 + 子密钥 L + PKCS7 往返/畸形填充 |
| `ProgramArgumentTests` | 已覆盖：`NormalizeCliArgs` 的 `-help`/`-?`/`-version` 映射由 `internal/cli/normalize_test.go` 逐字钉住 |
| `DownloadPathLockTests` | 已覆盖：`pathlock_test.go` / `pagelock_test.go` / `deadlock_test.go`（含用户报的 Ctrl+C 死锁） |
| `ParserTests` | 已覆盖：`fixture_test.go` 回放 15 个上游夹具 + `upstream_playlimit_test.go` |
| `ServeApiHttpTests` | 已覆盖：`internal/server` 10 个测试文件（guard/token/authguard/proxy/querylimit/body/tasks/shutdown/cancel） |
| `WidevineCdmTests` | 不适用：端到端需要真实 `device.wvd` 与 B 站 DRM 服务器；错误路径已由 `upstream_wvd_test.go`、`upstream_sslpolicy_test.go` 覆盖 |
| `ConfigIsolationTests`、`AotCliBindingTests`、`AssemblyInfo`、`FakeBilibiliApiServer` | 不适用：分别是 C# `AsyncLocal` 隔离、AOT 反射绑定、程序集元数据、C# 假服务器夹具——Go 侧无对应机制或已用 httptest 替代 |

**总账**：上游 `BBDown.Tests` 61 个文件——**接入差分 40 个**（`internal/*/upstream_*_test.go` 20 个文件承载）、
**同契约已覆盖 6 个**、**确认不适用 7 个**、其余为夹具/元数据。
目标是把这些文件当规格逐条对照，因此「无差异」与「已覆盖」同样是结果：它们把『本来就对、但没人守』的地方变成了有守卫的。
**方法论收获**：这是本目标里最深的一处——不是某个分支写错，而是**整条数据链断在解析层**：
下载、混流、封面/章节清理都写好了，上游也给这些循环写了用例，但我们从来没读过那个 JSON 节点。
差分表之所以能撞到它，是因为上游用例的名字（`ClampRoleAudioIndex`）指向了一个我们根本没有的函数。
**方法论收获**：这轮两处都是「合法性判断过严」——空载荷与 v2 文件本来都合法，我们却把它们当成畸形输入报错。
对照上游时要专门看**边界值属于合法集还是非法集**：把合法的挡在外面，用户得到的是一条无从下手的错误。
**方法论收获**：白名单这类「枚举」最容易少一项——它不报错、只在特定镜像站场景下静默少发凭据。
差分表的做法是把上游的**枚举值逐个搬过来比对**，比读代码「看起来对」可靠得多。
**方法论收获**：这次差异藏在「看起来对」的一行 `ctx.Err() != nil` 里——它覆盖了两种语义
（用户取消 / 超时），而只有前者才该叫「已取消」。对照上游时要注意**这类把两种原因混为一个判断**的写法，
它们不会报错，只会把失败原因伪装成用户操作。
**方法论收获**：这一片「无差异」但**原先没有任何用例**——安全前提靠代码里的一行 `CheckRedirect` 撑着，
谁把它删掉都不会有测试变红。差分表的价值在这里是另一面：它把「本来就对、但没人守」的地方变成有守卫的。
**方法论收获**：风控这次是「上游有、本仓没有」的**整块能力**，而它不体现在任何功能路径上——
正常跑永远走不到。差分表能撞出它，是因为上游专门为它写了用例。这也再次说明：
「我们没这条逻辑」和「我们这条逻辑写错了」是两类问题，只有对着上游用例清单才会有前者。
**方法论收获**：「前缀解析」这类差异读代码时几乎不可能看出来——`fmt.Sscanf("%d")` 看上去完全合理，
只有拿上游的 `"12abc"` 这类脏值去喂才会现形。差分表里那些**看起来没意义的边界值**
（`"invalid"`、`null`、缺字段）恰恰是最有价值的部分。
**说明**：这一轮只补用例、无代码改动——三处都是「跑一遍确认一致」，
按既定口径同样记入文档：**没撞出差异也是结果**，下次不必重查。
另核对了剩余清单里的一批「假剩余」：`ParserFixtureTests`、`ProgressBarTests`、
`DownloadProgressAggregationTests`、`ArchiveGranularityTests`、`ServeApiSecurityTests`、
`CommentUtilTests`、`SubscriptionStoreTests`、`LiveStreamUtilTests` 等在前几轮已按别的名字覆盖，
真正的剩余面集中在：`MuxerArgsTests`、`HttpUtilRetryTests`、`HttpUtilSslPolicyTests`、
`VerifiedNoRedirectClientTests`、`ConfigPropagationTests`、`DownloadPipelineTests`、
`DownloadTaskSnapshotTests`、`ServeCommandTests`、`ParserPlayLimitTests`、
`ExternalProcessRunnerTests`、`BBDownAria2cTests`、`JsonElementExtensionsTests`、
`Widevine*Tests`、`DrmDecryptorTests`、`AppHelperMessageTests`、`CancellationClassificationTests`。
**方法论收获**：这一轮最有价值的一条是 UA——它不在任何「模块」里，而是横跨「参数解析 →
HTTPClient → 下载器」的**传递链**：参数解析对了、HTTPClient 也存对了，只有最后一段没接上。
差分用例的价值就在于它直接对着「用户设了参数该有什么效果」断言，从而把断链暴露出来。
**方法论收获**：这轮两处「功能性」缺陷都藏在**跨模块的契约**里——脱敏键表小一号、合并失败不清理，
读单个函数的实现都挑不出毛病，只有拿上游的断言表逐条对照才会红。
另外确认了一条经验：**没撞出差异的也要记**（如数值校验），它是「这块已对齐」的证据，下次不必重查。
**方法论收获**：这一轮的价值不在「又修了几个」，而在于**差分测试的建法**：上游测试的
`[InlineData]` 是逐字的输入输出对，搬过来就能跑，比读源码找差异可靠得多；
而「选项面双向差集」能覆盖到只在某个子命令上出现的细微差别（如 `-w`），这类差异人工阅读必然漏。
**方法论收获**：这七处都不是读代码读出来的，而是**拿上游的每一句用户可见输出当检查表**反查本仓——
五片审计报告按模块写，天然会漏掉「横跨模块但用户能看见」的面（显示层、分P选择）。
字面量扫描本身有假阳性（本仓用 printf 占位、措辞不同），必须逐条人工判定；但它的高召回率值这个成本。
**方法论收获**：用户报的是「为什么有两个进度」——只看代码很容易把「两个」当成两次渲染器调用去查，
实际是**一次渲染 + 一次竞态**：末帧残影被日志压掉左半边，紧接着又画出最后一帧。
定位靠的是把终端里的回车切开逐帧还原，而不是盯着代码猜。

### 4.31 第二十七轮：进度条改为实时（用户要求的新特性 · **有意偏离上游**）

> 用户请求：「新特性：进度条改为实时」。上游的重绘**完全由定时器驱动**——`ProgressBar.cs` 的
> `animationInterval = TimeSpan.FromSeconds(1.0 / 8)`，`TimerHandler` 画完再 `ResetTimer()` 续一次；
> 本仓此前同样是 `time.NewTicker(125 * time.Millisecond)`。这一条与「行为对齐」相反，
> 属于用户明确要求的功能差异，按 §7 维护约定登记在此。

| 项 | 上游 / 本仓（改前） | 本仓（改后） |
|---|---|---|
| 重绘触发 | 125ms 定时器，每帧无条件画（与有没有新数据无关） | 数据到达即重绘：多线程 `countingWriter` 每 32KB 块、单线程每次 `Read` 打点 |
| 帧率上限 | 8 帧/秒 | `minFrameInterval = 16ms`（≈60fps）节流，窗口内的信号合并 |
| 停滞时（无数据） | 定时器照转，转圈继续动 | `idleHeartbeat = 125ms` 心跳补帧，只推进转圈——保留上游这一可见表现 |
| 首帧 | 构造后等第一个 tick | 进入渲染循环立即画（下载一开始进度条就在） |
| 字节计数 | 已实时（`ProgressAggregator` 分片累计差值） | 不变 |
| 收尾 | `Dispose` → 整行擦除，先于后续日志 | 不变（`done` → `line.clear()` → `stopped`/`finished`） |
| 速度显示 | 每 1 秒结算一次的平均速度 | 不变（未采纳「瞬时速度」，用户选了 A 方案） |

**实现**：`internal/download/pacer.go` 新增 `progressPacer`（非阻塞、缓冲 1 的合并信号）与
`runProgressLoop`（首帧 + 事件帧 + 节流 + 心跳 + 收尾），两条下载路径共用。`Signal()` 从下载协程
同步调用，必须非阻塞，否则会把下载拖慢；零值 pacer 退化为「不打点」而不是崩溃。

**测试**（五处变异均验证「撤掉实现即变红」）：

| 用例 | 钉住的行为 | 变异 → 红 |
|---|---|---|
| `TestProgressLoopDrawsOnDataNotOnTimer` | 无数据不出帧、数据一到立刻出帧 | 退回 125ms 定时器 → 安静窗口内画了 4 帧 |
| `TestProgressLoopThrottlesFrameRate` | 1ms 间隔的 100 个信号 ≤ `历时/16ms+3` 帧 | 信号到达即画 → 100 帧 |
| `TestProgressLoopHeartbeatKeepsSpinnerAlive` | 停滞时转圈仍在转 | 关掉心跳 → 500ms 只有 1 帧 |
| `TestProgressReaderDrawsOnDataArrival` | 单线程路径帧跟着数据走（10% → 20%） | 删 `Read` 里的 `Signal()` → 只有 10.00% |
| `TestMultiThreadDownloadSignalsRendererOnDataArrival` | 多线程路径数据到达唤醒渲染器 | 删 `reportProgress` 里的 `Signal()` → 全程没唤醒 |

**方法论收获**：第一版节流用例只发了 200 次连续信号，撤掉节流**仍然全绿**——因为 `Signal()` 的
发送侧合并（缓冲 1 + 非阻塞丢弃）已经替节流兜了底，那个用例实际测的是合并，不是帧率。把信号间隔
改成 1ms（消费者能立刻跟上、缓冲不积压）之后，帧数上限才只能由 `minFrameInterval` 保证。
**变异验证要用「被撤掉的那一层真正负责的场景」，否则测得再绿也只是测了别人的兜底。**

### 4.32 第二十八轮：失败时的用户可见输出（用户报「非命令错误也会弹出帮助」+「重试 3×3=9 次」）

#### 4.32.1 运行期失败不再打印帮助

上游用 Spectre.Console.Cli：**只有参数解析失败**才打印帮助文本；运行期异常走
`Program.cs` 的 `SetExceptionHandler`——打印异常消息 + 一句「请尝试升级到最新版本后重试!」，
返回 1，**绝不打印帮助**。本仓用 cobra，默认对**任何** `RunE` 错误都打印 `Error: xxx` 与整篇 usage，
而且 `Execute()` 自己又把错误打印了一遍。

用户实测（`BBDown notaurl`）：stderr **95 行**，其中 90 行是帮助文本，错误消息出现两次。

| 项 | 上游 | 本仓（修复前） | 修复后 |
|---|---|---|---|
| 运行期失败 | 消息 + 升级提示，无帮助 | 消息 ×2 + 整篇 usage | 消息 + 升级提示（2 行，exit 1） |
| 参数/用法失败 | 错误 + 帮助文本 | 错误 + usage（同） | 错误 + usage（不变） |

实现：`rootCmd.SilenceUsage/SilenceErrors = true`（关掉 cobra 的自动输出），`Execute()` 统一走
`reportError`：用 `usageError` 区分两类错误——未知标志经 `SetFlagErrorFunc`、参数个数经 `usageArgs`
包装成用法错误（唯一附带 usage 的一类），其余按上游打印消息 + 升级提示。

#### 4.32.2 两级重试的日志分级

上游对同一个失败轨道就是**两级阶梯**：页面级 `DownloadPageAsync`（`while (retryCount < maxRetry)`，
`maxRetry = --retry-count`）× 轨道级 `DownloadFileCoreAsync`（`while (retry < maxRetry)`，
`MaxRetryCount` 同源），合计 3×3 = 9 次请求。本仓结构相同，实测
（`internal/workflow/retry_ladder_test.go` 用恒 404 的假 CDN 计数）：**GET = 9，HEAD = 3**。

但两级此前的日志**文案与级别完全相同**（都是 `下载异常(err), X 后重试... (n/3)` 的 Warn），
读起来像一次 9 连试——用户就是这么被绕进去的。上游是分开的：

| 级别 | 上游 | 本仓（修复后） |
|---|---|---|
| 轨道级（单线程） | `LogDebug(下载失败(第N次重试, Xms后): msg)` | 同 |
| 轨道级（多线程分片） | `LogDebug(分段下载失败(第N次重试, Xms后): msg)` | 同（此前无日志） |
| 页面级 | `LogError([Type] msg)` + `LogWarn(下载出现异常, X 秒后将进行自动重试...)` | 同 |

**测试**：`TestRetryLadderAttemptsMatchUpstream` 同时钉住「GET = 9 / HEAD = 3」与「默认级别下重试日志
恰好 2 条」。变异验证：页面级 3→2 → GET=6，红；轨道级退回 Warn → 8 条日志，红。

**方法论收获**：这次两个问题都出在「输出层」而不是功能层——下载、重试、帮助文本各自都对，
但**同一句话说给两个不同的对象**（两级重试）与**一次失败说两遍**（cobra + Execute）把用户绕进去了。
对齐检查不能只看「有没有这条输出」，还要看「这条输出在什么级别、说几遍」。

#### 4.32.3 下载请求 URL 的 Debug 行（上游有、本仓没有）

用户报「目前 404 概率比较高」时，排查发现**日志里没有任何请求 URL**：上游
`BBDownDownloadUtil.DownloadFileCoreAsync` 与 `MultiThreadDownloadCoreAsync` 各有一行
`Logger.LogDebug("Start downloading: {0}", SensitiveDataMasker.MaskUrl(url))`（每文件一次，
签名参数脱敏），本仓一行都没有——于是「强制替换到镜像后 404」这个最常见的解释在日志里无法证实。
现按上游补上（`DownloadFile` 每文件一次，三条路径共用；aria2c 分支单独一行，同样只打一次）。

**复现尝试（均 0 次 404，故未能定位用户环境里的成因）**：

- 12 次真实下载（同一音频，`--force-replace-host` 默认/关闭各 6 次）：0 次 404；
- 4 支视频的音频 URL 用 curl 探测（HEAD/Range 各 3 次 × 原站与镜像）：全 200/206；
- 1 支视频的多线程下载（1MB 分片、103 MB 产物）：分片 Range 请求 0 次 404。

另外：连续高频解析会触发 **HTTP 412 风控**（与本条的 404 是两回事，别混为一谈）。
下一步要看的是用户那份 `--debug` 日志里 `Start downloading:` 行的 host。

### 4.33 第二十九轮：三处**有意差异**（用户逐条点了「可以」）

用户看完 §4.32 的判定后确认做三件事，都不是对齐、而是有意的行为差异：

| # | 差异 | 上游行为 | 本仓行为 |
|---|---|---|---|
| 1 | 412 提示 | `EnsureSuccessStatusCode()` 抛 .NET 默认消息（`Response status code does not indicate success: 412 (Precondition Failed).`） | 追加「（疑似风控拦截：请等待数分钟至数十分钟后重试，或更换网络出口；持续重试会加重风控）」，只对 412 生效 |
| 2 | 失败输出配色 | 两行都是白字红底（`BackgroundColor = Red`、`ForegroundColor = White`，亮色档） | 同（ANSI 101/97）——go.12 只搬了文本漏了配色，用户报「这个不够显眼」 |
| 3 | 镜像 404 回退 | 对同一个死地址重试满 3×3 次再整页重来 | 首次 404 即换回 host 替换前的原地址（单线程与分片两条路径都覆盖） |

**第 3 条的接线**：`handlePcdn` 改写 URL 前先记下原地址（`origVideoURL`/`origAudioURL`），下载调用处经
`withFallback` 在「当前地址 ≠ 原地址」时把它放进 `DownloadConfig.FallbackURLs`（§4.39 起为有序候选链，元素 0 即原地址）；下载器把 404 做成
类型化错误（`httpStatusError`，`Error()` 文本与改前逐字相同），两条下载循环在 404 时切到原地址并
各打一条 Warn。

**测试与变异验证**（六处全部「撤掉即红」）：

| 用例 | 钉住的行为 | 变异 → 红 |
|---|---|---|
| `TestHTTP412CarriesActionableHint` | 412 带提示、URL 脱敏 | 撤 412 分支 |
| `TestHTTP404HasNoRiskControlHint` | 404 **不带**风控提示（不许误导） | （反向保护） |
| `TestReportRuntimeErrorPrintsNoUsage` | 失败输出逐行白字红底 + 行尾复位 | 消息行不上色 / 提示行不上色 |
| `TestDownloadFallsBackToOriginalHostOn404` | 单线程 404 回退原地址 | 撤单线程回退分支 |
| `TestDownloadFallsBackPerClipOn404` | 分片 404 回退原地址 | 撤分片回退分支 |
| `TestPageDownloadFallsBackWhenMirrorReplacedHost404s` | 端到端：`--upos-host` 指向恒 404 的假镜像，产物仍完整 | `withFallback` 不透传 |

### 4.34 第三十轮：上游 v1.6.20 定性（**重构版本**，无新规格）

上游 2026-09-24 发布 v1.6.20（`upstream/master` = `165d075`，tag `5de84c4`；相对 v1.6.19 共 2 个提交、52 个文件）。
这一版以重构为主，逐项判定如下：

| 项 | 判定 | 依据 |
|---|---|---|
| `Download.cs`（1088 行）拆成 8 个新文件 | **无行为差异** | 机械扫描：把改动文件里每一行逻辑拿去 v1.6.19 全树比对（限定符/`await` 两边同样归一化），未命中的行全是 record/context 类与签名管线；再逐段核对 `DownloadFinalizer`（跳过分支、`finally` 里的轨道清理、封面删除条件「单P ∥ 末P ∥ 换稿」、aid 空目录兜底）与 v1.6.19 的 `MuxAndFinalizeAsync` 一致，且本仓实现早已是同一套边界 |
| `Parser.cs` 110 行 | **N/A** | 纯 C# 资源管理：`JsonDocument`/ArrayPool 的 dispose 改 try/finally、免二压重发的文档所有权转移；Go 由 GC 管 |
| fetcher 六处 + 新增 `FetcherJson.cs` | **一处窄边界已对齐** | 上游 v1.6.19 那六处写成「if (code != 0) { var msg = …; }」——**只算不抛**，诊断永远不可达（上游自己的 bug）。本仓一直是 `return err`，所以这一半是上游补齐；但上游新增的 `ThrowIfApiError` 在 **data 存在时也查 code**，本仓此前只在 data 缺失时报错 → 本轮补齐四处（系列首屏/分页、合集首屏/分页），消息格式 `<文案> (code=N): <message>` 与上游逐字一致 |
| 5 个测试文件的「新表」 | **无新规格可搬** | 逐行过滤掉 async/命名空间改动后，剩余差异全是机械重命名（`Program.ArchiveTracker` → 独立类、`MergeWithConfig` → `MergeWithConfigAsync`、`CanResumeFrom(..., out var)` → 元组返回、`Program.ClampRoleAudioIndex` → `DownloadPageExecutor.…`），**断言一条未改** |
| `RetryPolicy.cs`（新）+ serve 的 `NormalizeForServe` | **部分 N/A → 2.14.0 起有面** | 钳制针对 serve 请求体里的每任务选项；本仓 2.0.0~2.13.0 `/add-task` 只接受 `url`（攻击面不存在），**2.14.0 起扩展为 15 字段显式白名单**（严格模式：未知/危险字段 400 点名、select_page 复用 ParsePageSelection + 1000 项上限），等价于自带 SanitizeUntrustedOptions 语义 |
| `Archive.cs`/`SubscriptionStore.cs`/`AppSettings.cs`/构建 | **N/A** | async I/O 迁移、`IsServeMode` 与时钟偏移字段搬家、NuGet 锁文件、Dockerfile、CI 缓存 |

**用例**：`internal/fetcher/upstream_apierror_test.go`（code=0/缺 code 不报错、字符串形态的 code、缺 message 仍带 code、
消息格式逐字比对）；变异验证：撤掉 code 判断即变红。

### 4.46 第四十一轮：`bbdown resume`（**新功能**）

把「失败/被取消的目标」登记到工作目录的 `.bbdown-pending.json`，`bbdown resume` 一条命令全部接着下；
成功即移除、清单空则删文件、损坏则报错（不静默丢任务）、原子写入。

与既有能力的关系：稳定身份续传（§4.35）负责「同一文件的字节级续传」，跳过已存在产物负责「已完成的不重下」，
**resume 负责「用户不必记得哪些没下完」**——三者合起来才是完整的「中断可恢复」。

**用例与变异**：`TestPendingRegistry`（去重/刷新错误/清空删文件/损坏报错）、
`TestDownloadTargetsRecordsFailuresForResume`（失败目标真的被登记且带错误信息）。变异：去掉登记调用 → 变红。

**方法论收获**：这次先把「登记层」写成纯函数并单测，再接 CLI——比上次 `--overwrite` 直接写端到端用例顺得多
（那次因为环境改写连撞三次）。**先纯函数、后端到端**应当成为默认顺序。
### 4.45 第四十轮：`--print-urls`（**新功能**）

只打印所选流的直链（视频/分片/音频，一行一个）后退出，不下载。定位与 `-I` 互补：`-I` 面向人
（清晰度/编码/体积列表），`--print-urls` 面向机器（纯地址，可直接喂给 aria2c 或自建流水线）。
打印的是**最终用于下载的地址**（即已经过镜像/PCDN 改写的结果），与真实下载保持一致。

**用例与变异**：`TestPrintURLsDoesNotDownload`（断言两条直链出现 + 目录里没有任何产物）；
变异：去掉早退 → 产物出现，变红。

**方法论收获**：写用例时第三次被同一类坑绊到——假 CDN 的 `127.0.0.1` 被 PCDN 正则命中、默认镜像替换把
地址改写。已在测试里显式关掉两者；这条经验值当写进测试模板：**造 `httptest` 地址前先想一遍生产代码里
有没有基于 Host/IP 的正则或替换**。
### 4.44 第三十九轮：`--overwrite` 与 `doctor --json`（**新功能**）

| # | 项 | 性质 | 内容 |
|---|---|---|---|
| 1 | `--overwrite` | **有意偏离** | 上游默认「产物存在即跳过」；本仓保留该默认，另加开关可强制重下（归档换档/重编码场景） |
| 2 | `doctor --json` | 新功能 | 自检结果机读化（`name`/`level`/`detail`），退出码同文本模式 |

**用例与变异**：`TestSkipExistingProduct`（存在/空文件/不存在/`--overwrite` 四态，直接调生产函数 `shouldSkipProduct`）、
`TestDoctorJSONIsMachineReadable`（真解析 JSON、检查小写键）。变异：去掉 `--overwrite` 短路、删掉 json tag，
两处分别变红。

**方法论收获**：写用例时又被环境绊了两次——假 CDN 的 `127.0.0.1` 被 PCDN 正则命中而改写域名、默认镜像替换
把地址换成不存在的镜像站。**假环境的每个字符都可能被真实逻辑命中**：凡是用 `httptest` 造地址，先想一遍
「生产代码里有没有基于 Host/IP 的正则或替换」。
### 4.43 第三十八轮：真机使用带出的三个问题（**修复 + 测试通道**）

用户反馈「状态不是很好」，实际用一遍后确认——问题比单元测试能发现的大：

| # | 问题 | 影响 | 修复 |
|---|---|---|---|
| 1 | `--thread-segment-size` 默认值(0=自动，O4 改的)与校验(1~1024)冲突 | **默认参数下任何下载都直接失败**（自 `1.6.20-go.3` 起） | 校验放宽为 `0~1024`（`2.4.1`） |
| 2 | 批量输入那层吞掉了单目标的真实错误 | 用户只看到「N/M 个任务失败」，无从自救 | 单目标原样抛错、多目标汇总附第一条错误（`2.4.1`） |
| 3 | 解析侧 `maxQn` 被误提到 129 | web playurl 不受理 APP 协议的档位（129 = HDR Vivid） | 回退到 127，APP 侧仍用 `maxAppQn = 129`（`2.4.1`） |
| 4 | `--sub-only` 无字幕时静默空转 | exit 0 +「任务完成」但无产物，分不清三种原因 | 明确提示（`2.4.2`） |

**根因（元问题）**：我们的回归网全部停在「单元 + 假服务器」这一层，没有任何一条用例走
「CLI 默认值 → 参数校验 → 真实下载 → 产物可解析」这条路。数值校验用例用写死的 `valid()`（分片=20），
从不走 CLI 默认值，所以三个多版本都没抓到默认参数是坏的。

**补救**：`scripts/smoke.sh` + `make smoke`（doctor → web/APP/TV 解析 → 真实下载 → 产物存在性与 ffprobe →
进度 JSON），需要网络故不进 CI 常规 job；另补 `TestDefaultOptionsPassValidation`（默认配置必须过校验）
与 `TestSubOnlyWarnsWhenNoSubtitleFound` 两条单元守卫，以及版本号五处一致性守卫。

**方法论收获**：**「测过」与「用过」之间有一条沟**。变异验证能保证「撤掉实现会红」，但保证不了
「默认配置能跑通」——后者只有真机跑一次才知道。以后每批改动收口前先 `make smoke`。
### 4.41 第三十七轮：HDR Vivid（129）与字幕短键（**新功能/修复**）

| 项 | 上游 v1.6.20 | 本仓 |
|---|---|---|
| 最高清晰度 | `127`（8K 超高清）为顶 | **`129`（HDR Vivid）**：`QualityMap` 新增条目，APP 协议 `maxAppQn = 129`，解析 `maxQn = 129` |
| 字幕语言键 | 映射表无裸键 `zh` | 补上（新版字幕接口用短键） |
| HDR Vivid 提醒 | 无（上游没有这一档） | 选中 129 时提醒兼容性风险（偏色/无法播放），并指引 `--dfn`/`-Q` 换档；档位 ID 用 `config.HDRVividID` 常量 |

**来源**：同生态 C# 2.x 接手线 BBDownT 的 2cd4084（HDR Vivid）与其 2.1.3 字幕修补；上游基线没有这两项。

**过程记录**：档位从 127 提到 129 撞掉三条**写死字面量**的用例（`rec.count("127")`、`got[3] != 127`），
已全部改为常量驱动（`maxQn` / `maxAppQn`）并补一条映射表用例。教训：凡「会随平台演进的档位/枚举」，
用例必须引用常量，否则下一次变更会误报，进而诱导人去改期望值而不是改逻辑。
### 4.40 第三十六轮：412 风控重试（**有意偏离**）

| 项 | 上游 | 本仓 |
|---|---|---|
| HTTP 412 | 4xx 视为确定性错误直接抛出（重试只覆盖 5xx 与传输错误） | 视为**风控信号**：最多 3 次尝试、退避 1s，且**轮换自动 UA** |
| 显式 `--user-agent` | —— | **不轮换**（登录态刻意伪装浏览器时，换 UA 更像异常客户端） |
| UA 并发安全 | —— | UA 读写加互斥（轮换会发生在并发请求中） |

**依据**：同生态 C# 2.x 接手线 BBDownT 的 2.1.x（移除易触发 412 的默认 UA + 412 轮换重试）。
**边界（先量再学）**：我们在 `/x/web-interface/view` 上各发 8 次、间隔 0.8s 复核——浏览器 UA 与传输型 UA
**都是 8/8 个 200**，低频率下 UA 类别不是主因。因此只吸收「412 轮换重试」，**不照抄 UA 池**。

**用例与变异**：`internal/util/riskcontrol_test.go`（重试并轮换 / 显式 UA 不轮换 / 成功路径只发一次）；
变异三处（去掉轮换、去掉 412 分支、去掉显式 UA 保护）分别变红。

**方法论收获**：跨实现「抄近路」的正确形态是**抄机制、不抄参数**——BBDownT 的机制（412 轮换）在我们这里
应当成立，但它的参数（UA 池该长什么样）取决于它的观测环境；我们的探针没复现出 UA 类别的差异，就不动池子。
### 4.39 第三十五轮：产物校验与候选链（**新功能 + 修复**）、进度 JSON、订阅过滤、serve 闸门量测

| # | 项 | 性质 | 内容 |
|---|---|---|---|
| 1 | F3 产物校验 | 修复（本仓自愈） | 判定只用 Content-Length / Content-Range 权威总长 / 实际写入字节；接口 size 不做判定（dash 无该字段、bandwidth 估算误差 −1.39%~+0.04%）| 
| 2 | F4 候选链 | **有意偏离**（§4.33 扩展） | DownloadConfig.FallbackURL → FallbackURLs 有序候选链：404 或连接/传输失败换下一个；其它错误不烧候选；尝试次数 max(retry, 候选数) 上限 8 |
| 3 | F5 进度 JSON | 新功能 | `--progress-json` 逐行 JSON 到 stderr；关闭时行为与改前一致 |
| 4 | F6 订阅过滤 | 新功能 | `sub add --filter <正则>` 按标题过滤（RE2），旧文件兼容 |
| 5 | O7 serve 闸门 | 优化（量测+回归） | 闸门抽成 acquireSlot/releaseSlot；6 任务/上限 3 → 峰值并发 3、201ms；变异（容量放大）变红 |

**证据**：代理 A 12 条变异、代理 B 14 条变异全部「改回旧行为即红」；本仓全仓 `go test ./... -count=1` 16 包绿。
非回归：9 次重试阶梯（GET=9/HEAD=3）、§4.33 两条回退用例、§4.35 单次请求与 dash-reparse 夹具全绿。

**方法论收获**：这轮把「先量清楚再判定」做成了硬步骤——F3 一开始的候选是「用接口声明的 size 校验产物」，
真去量才发现 dash 节点没有 size、bandwidth 估算能差 1.4%，若照原计划做就会给用户造出一堆误报。另外，
**变异脚本本身也会假绿**：代理 A 的第一次变异脚本把待删块插到了文件头，导致 M2~M11 是编译失败而非真红，
它自己发现并用「唯一 marker + 原地替换」重跑了全部 12 条——这类披露比结论更重要。
### 4.38 第三十四轮：批量输入（**新功能**）与模板变量对账

| # | 项 | 性质 | 内容 |
|---|---|---|---|
| 1 | 批量输入 | **新功能（有意偏离）** | 上游只接受一个位置参数（多余的静默忽略）；本仓支持多位置参数 + `--urls-file`（`-` = stdin），顺序执行、单失败不中断其余、末尾汇总 |
| 2 | 模板变量 | 对账（无缺口） | 上游 `PathHelper` 17 个占位符本仓全有；新增整表用例把这张表钉住（此前无守卫） |

**用例与变异验证**：`internal/cli/urlsfile_test.go`（列表解析/目标汇总/顺序执行；变异：遇错即返回、不跳注释，
两处变红）、`internal/download/pathformat_tokens_test.go`（整表展开；变异：删掉任一替换即红）。

**方法论收获**：F1 这条差点做歪——候选清单里写的是「加 `<upMid>`/`<pubDate>`」，真去对上游才发现
`<ownerMid>`/`<publishDate>` 早就在上游的表里、我们也都实现了。**先对账再动功能**：否则我们会凭空造出
一套与上游不同名的占位符，用户照着上游文档写模板就会失败。顺带补上了「表类功能必须有整表用例」这条
——17 个占位符只测 3 个，等于没有守卫。
### 4.37 第三十三轮：分片自适应（**有意偏离**）与杜比视界探测修复（对齐）

| # | 项 | 性质 | 内容 |
|---|---|---|---|
| 1 | `--thread-segment-size` 默认值 | **有意偏离** | 上游默认 20 MB；本仓默认 **0 = 自动**（按「分片数 ≈ 并发上限」倒推，1–20 MB）。显式给值时语义不变。理由：40 MB 文件在固定 20 MB 下只切 3 片，浪费并发；实测限速服务器上 1.698 s → **869 ms** |
| 2 | 杜比视界版本探测 | 对齐修复 | 上游规则 `libavutil > 57 或 57.17+`；本仓正则丢反斜杠（恒 false）+ 阈值 `>=5`（恒 true）两个 bug 互相掩盖 |
| 3 | 探测缓存 | 优化（等价） | 同一 ffmpeg 二进制进程内只探一次（多P 杜比视界每 P 一次 → 一次）；serve 下按 `--ffmpeg-path` 分别缓存 |

**用例与变异验证**：`segment_plan_test.go`（规划表 + 限速服务器墙钟，变异：去掉倒推 → 红）、
`dovi_probe_test.go`（58.2/57.17 → true、57.16/56.70 → false；三次调用只探一次；三处变异分别变红）。
`progress_bench_test.go` 是 O3 的量测工具（单帧 **2.4 µs** → 上限 ≈0.015% CPU，判定无需优化）。

**方法论收获**：这一轮最有价值的是「**两个 bug 互相掩盖**」——正则恒 false 与阈值恒 true 叠在一起，
任何单侧修改都只会把行为从一个错误翻到另一个错误；只有把两个都对着上游核实，才能得到「按版本判断」
这个本意。凡「探测/判定」类函数，用例要同时钉**两侧边界**（支持与不支持），否则一个恒真/恒假的实现
也能过。
### 4.36 第三十二轮：断点续传改用稳定资源身份（**对齐补齐**，非偏离）

用户在 O5（`docs/ROADMAP.md`）里报了「断点续传命中率」这项，量出来是**跨进程续传从未命中**：

| 项 | 上游 v1.6.20 | 本仓（修复前） |
|---|---|---|
| 清单身份 | `StableResourceIdentity`：剥离会刷新的签名 query 参数后比较 | 完整签名 URL 字符串比较 |
| B 站场景（每次解析换 `deadline`/`sign`/`trid`/`upsig`） | 同一资源 → 命中续传 | 身份永远对不上 → 删 `.tmp` 重下 |
| 总长 | 纳入身份比较 | 未纳入 |
| ETag / Last-Modified | 仅**双方都有**时比较（有些 CDN 不返回） | 探测有、清单无时也拒绝 |

**实测**（`internal/download/resume_identity_test.go`，2 MB 文件、服务端发到 512 KB 时断连）：第二段下载
**2,097,152 → 1,572,864 字节**。变异验证：把身份比较退回完整 URL（即改前行为），本用例报「第二段实际
下载 2097152 字节，期望只补缺口 1572864」，变红。

**方法论收获**：这一条不是「上游新增功能」，而是**上游 v1.6.20 修了我们没跟上的规则**——O5 之所以能
很快定位，是因为优化项写清了「怎么量化」（重跑下载字节 / 总字节），量一次就看出命中率是 0。凡是
「缓存/续传/去重」这类按身份匹配的机制，都要先问一句：**身份里有没有每次都变的东西**。
### 4.35 第三十一轮：解析请求优化（**有意偏离**，结果等价）

方向变更后的第一个优化版本（`1.6.20-go.1`）。两条都改的是**请求模式**，可观测结果不变：

| # | 偏离 | 上游行为 | 本仓行为 | 等价性依据 |
|---|---|---|---|---|
| 1 | playurl 请求顺序 | 先 qn=0、再 qn=127 重发；重发带 `dash.video` 就整份取代前者 | qn=127 优先；失败或不带 `dash.video` 时回落调用方的 qn | 落点相同（要么 qn127 文档、要么 qn0 文档）；fixture 用例覆盖「127 生效」「127 被拒→回落」两条路径 |
| 2 | `-I` 的章节抓取 | 无条件抓 `player/wbi/v2`（`OnlyShowInfo` 的早退在抓取之后） | `--only-show-info` 时跳过 | `-I` 只打印流信息，章节只进混流产物；`-I` 在混流前返回 |

**实测**（BV1ZH4y167mH，同网络同账号）：`-I` 解析 5 → **3** 个请求，墙钟 0.53 s → **0.27~0.32 s**；
去掉每次都变的 URL 行后输出 32 行**逐行一致**（旧版用已安装的 `1.6.19-go.9` 对比）。

**用例与变异验证**：`playurl_single_pass_test.go`（127 生效只发 1 次；127 无 dash 时回落 1 次，撤掉回落即红）、
`onlyshowinfo_chapters_test.go`（`-I` 不调用章节抓取；撤掉门槛即红）。`fetchPoints` 的 URL 硬编码
`api.bilibili.com`（与上游一致），假服务器拦不下，故加了 `fetchPointsFunc` 接缝供用例离线断言。

**方法论收获**：优化与对齐的差别在证据形态——对齐要「与上游逐字一致」，优化要「可观测结果一致 +
前后数字」。这两条都满足：输出逐行一致、请求数与时延有实测。另外，改请求顺序会**撞掉钉住旧顺序的
既有用例**，这是好事：它证明回归网真的覆盖了那段契约；同步更新时必须写明「改了顺序、没改落点」。
**方法论收获**：这是第一次遇到「上游发版但**没有新规格**」——机械扫描（改动行 × 旧版全树）+ 关键边界逐段核对，
比逐文件通读更省也更可复现；「无行为差异」同样要写进基线，否则下一轮会重复劳动。另一点：**上游也会写出
「只算不抛」的死代码**，本仓「一开始就 return err」不等于落后——对账要区分「我们不齐」与「上游刚补上」。

**方法论收获**：「不主动加功能」的约束下，偏离必须**逐条点名、逐条留痕**——这三条都写进了本节与
README 的「已知有意差异」，而 §4.32 那两条（下载 URL 的 Debug 行、两级重试日志分级）是对齐、不算
差异。两类分开放的用处是：下次与上游对账时，不会把有意差异当成待修的落后项去「修」掉。




### 4.58 第五十三轮：守卫判据跨平台化（**修复，两轮**）

工作目录守卫（防测试把状态文件写进包目录）第一版把「收尾时 cwd 与开始逐字相同」当判据 → 同一提交 ubuntu 绿 / windows 红 / macos 红：
Linux 上目录被删后 getcwd 返回 ENOENT（比较被短路 → **假绿**）、Windows 返回已消失路径、macOS 保留 name cache 且解析符号链接。
第二版改为「先还原、还原不了才红」，但「还原后是否仍等于原字符串」**再次平台相关**：macOS 上 /var 是 /private/var 的符号链，
chdir 成功而 Getwd 返回解析路径。第三版抽 sameDir（os.Stat + os.SameFile 比较底层对象；不可解析时才退 EvalSymlinks/Clean）→ 三平台全绿。

**机械规则已写进 AGENTS.md 测试纪律**：路径比较禁止字符串相等；写守卫前先列「判据 × 三平台语义」表；
平台受限场景（Windows 建符号链接 / 删进程当前目录）显式 t.Skipf 写明原因。**最值得记的是：这条规则在修复它自己的代码里复发过一次。**

### 4.57 第五十一/五十二轮：进度观察者、SSE 真实进度、测试卫生（**优化 + 修复**）

- **进度观察者**：ProgressEvent{Current,Total,SpeedBps} + WithProgressObserver(ctx, fn) + ProgressObserverFromContext(ctx)；
  context 携带、**无包级全局**（§4.56 的教训）；回调在既有节流之后（去掉节流 → 16MiB/35.8ms 产生 534 帧，用例上限 10 帧）；
  单线程与多线程都覆盖，续传含 base，末帧计满。
- **SSE 真实进度**：internal/server/progress.go 单一映射纯函数（total<=0 → percent/total 均 0、**不估算**；Current>Total 只夹 percent；NaN/Inf 归 0）
  + 第二道节流（≥100ms/任务）。代价：aria2c 无逐字节观察者（黑盒）；节流丢帧不补发（成功路径由 task_done 兜底）；
  观察者只覆盖真实下载阶段（解析/混流停在最后一帧）；/get-tasks 的 Progress 与 TotalDownloadedBytes 起初是**服务端边界值**
  （与 SSE 数字不一致、有意不改既有 API 契约）；2.13.0 起字节口径统一为「按文件身份累计的实时字节与产物字节的较大者」，
  Progress 仍保持边界语义（详见 §4.64，progress_contract_test.go 机械钉住）。
- **测试卫生**：internal/cli/.bbdown-pending.json 曾被误提交进 git（587bd09）；删文件 + 测试注入 t.TempDir() + 包级 TestMain 守卫
  （cli 与 server 各一；server 侧覆盖 bbdown-tasks.json 与原子写临时文件）。实测：改前一次全量测试改写 2 个文件 → 改后 0 个。

### 4.56 第五十轮：serve Web UI + SSE、机读模式输出分流（**新功能 + 修复**）

- **GET /**：go:embed 单文件页面（无框架/无 CDN/离线可用，二进制 +8 KB）；未注册路径仍 404。
- **GET /events**：task_start / task_progress / task_done / task_failed + hello 握手 + 25s 心跳；64 连接上限、单连接 64 帧队列
  （满则丢帧不阻塞发布者）、回放 64 条 + 客户端按 seq 去重；percent 0~100 与 --progress-json 同口径；
  页面与 /health 同级免 token，/events 带任务数据故配 token 时需带。**两条 C# 线都没有 Web UI/SSE，属有意新增。**
- **机读模式 stdout 只留数据**（--info-json / doctor --json）：日志让位 stderr、横幅不打印；真机管道由 rc=1 变 rc=0。
  **第一次尝试失败**（f828cba，已 revert）：把输出目标冻结在变量里 + 改全局状态不还原 → 破坏「替换 os.Stdout 捕获输出」的既有手法并跨用例泄漏；
  第二次用「动态解析 + 作用域让位 + defer 还原」，并**先写守卫用例再动实现**。

### 4.55 第四十九轮：进度条口径统一与宽度表收敛（**修复 + 有意偏离**）

- **真实缺陷**：续传时终端百分比用「本次传输占比」而 --progress-json 用「含已就位字节的实际占比」，同一次续传两个数字。
  统一为含 base 的实际进度（速率仍按本次增量）：实测 100MiB（已就位 50MiB、再读 10MiB）终端行 10.00% / ETA 00:00:18 → **60.00% / ETA 00:00:08**，与 JSON 一致。
- **多线程聚合行**改用同一渲染路径；**宽度表**三份实现收敛到 download.DisplayWidth / PadDisplay
  （跨包变异：改坏单一来源 → cli 与 workflow 的对齐用例同时红）。

### 4.54 第四十八轮（续）：任务卡与进度条信息密度（**有意偏离上游排版**）

任务卡（值列固定第 12 显示列、值过 SanitizeLogString 防伪造行；-I / --info-json / --print-urls 不打）
+ 进度条加总量与 ETA（速率口径与 --progress-json 统一为 delta/elapsed）。
代价：续传 x/y 与 ETA 曾不含 base（已由 §4.55 修）；--hide-streams 时卡片省两条流行；页面级重试会重打卡片；聚合渲染曾是旧格式（已由 §4.55 统一）。

### 4.53 第四十八轮：界面「清爽」三件（**有意偏离上游排版**）

用户标准：清爽优先、简洁就算了——信息不砍，只做秩序。
- **流列表 / 已选择的流**：去方括号、按显示宽度（中文/全角 2 列）对齐；四种行宽 4 种 → 1 种（70 列），码率列 4 位置 → 1 个；-I 的直链行未改。
- **失败块**：原因（红底标题行）/ 提示（风控·权限·网络·参数·登录态 分类）/ 命令（可复制，给不出则省略）；固定句只在无法归类时保留。
  归类是文本标记启发式——**只少给建议，不给错建议**。
- **doctor 表格**：+ / ! / x 符号 + 名称列按最长名补位 + 详情列折行缩进；--json **逐字节未改**。

### 4.52 第四十七轮：--info-json（**新功能**）

解析结果机读化：标题/BV/aid/cid/分P/时长/视频轨道/音频轨道/分片；字段 snake_case 与 doctor --json、--progress-json 一致；
空轨道写 [] 不写 null，空 clips 省略；只解析不下载、不打印人看列表。（输出分流后来由 §4.56 解决。）

### 4.51 第四十六轮：收尾三件（**优化/修复**）

M3U 时长改用真实分P时长；M3U 列表**读回旧文件并合并**（-p 1 重跑条目 1 → 3，不再缩减）；
下载层 412 最终错误带可操作提示（提示语单一来源 util.StatusHint，非 412 逐字不变）。

### 4.50 第四十五轮：三条线并行（**新功能 + 优化 + 有意偏离**）

- Ctrl+C 双击语义（首次优雅取消 + 提示 / 二次强制退出 130 / 退出前恢复终端）——根因：signal.NotifyContext 第一次取消后不注销信号，第二次被吞；
  首次取消退出码也补齐 130。
- --write-m3u 侧车播放列表（按分P排序、按路径去重、写失败只告警）。
- 重试分类（planTrackRetry 唯一决策点）：确定性 4xx **不重试**（上游 C# 连 4xx 一起重试——**有意偏离**）；网络错误有候选立刻换；
  无候选短退避 ≤500ms；412 退避 1s→2s→4s 上限 8s 且需先成功轮换 UA。实测 403 GET 3→1、分片 9→3。

### 4.49 第四十四轮：--log-file（**新功能，接线**）

Logger.SetLogFile 早已实现（含写失败自动挂起与恢复提示），但**没有任何 CLI 入口**——加 --log-file 持久标志并在 PersistentPreRun 生效。

### 4.48 第四十三轮：--nfo、--compat、帮助首屏（**新功能 + 界面**）

--nfo 侧车元数据（Kodi/Emby/Jellyfin；失败只告警不影响下载）；--compat 选档避开 HDR Vivid/杜比视界（全被滤掉时原样返回，绝不把候选清空）；
--help 首屏列出自研能力（**当时它们在帮助里完全不可见**），并加守卫用例防「做完不可见」。

### 4.47 第四十二轮：新版字幕接口（protobuf，**新功能/对齐生态**）

x/v2/subtitle/web/view 返回 protobuf；字段号取自 BBDownT 的 dmviewreply.proto（SubtitleWebReply{subtitle=1}、VideoSubtitle{subtitles=3}、
SubtitleItem{lan=3,lanDoc=4,subtitleUrl=5}）。只手解三个字段（手写 varint），不引 protobuf 运行时；新接口优先、老三条回退；
**只在有 cookie 时试**（匿名多打一次请求曾在 CI 挂过一次）。**未验证项**：字幕需登录，本机未登录，真机待复核。

（注：本节起于文件末尾追加，编号与上面的降序排列不连续；`grep -n "^### 4\." 可查全部编号。）


### 4.59 第五十四轮：进度节流真缺陷与时间判据去墙钟（**修复 + 测试基础设施**）

- **真缺陷**：`pacer` 的「信号到达且窗口已过 → 立即重绘」分支没撤掉在途延迟 timer，timer 随后再响一次 → 同一个 16ms 窗口补画第二帧。
  修法：立即重绘前 `delayTimer.Stop()` + 清空。根因确认方式很硬：旧判据在负载下偶发红（172ms 画 14 帧 > 上界 13），
  先怀疑判据、后定位到实现——**「偶发红」的两种解释（判据太严 / 实现有 bug）必须用可注入时钟区分开**，不能靠放宽上界掩盖。
- **测试基础设施**：`progressReader` 与 `pacer` 注入时钟；速率/ETA 改结构性判据（正数 + 输入推导区间 + 帧内自洽），
  节流改确定性不变量（窗口未满只排 timer、同窗口合并、推过一窗补一帧、间隔 ≥ 0.8×）；7 处变异全断言红。
  效果：`-race ./internal/download/` 由 FAIL 变连跑 3 次全绿；帧数用例 0.1~0.2s → 0.02s。
- **跨平台**（U 交付的判据表）：注入时钟的窗口运算、float64 除法、Go 的格式化（不走 libc/locale）、ASCII 正则均三平台一致；
  轮询上限只影响等待时长（Windows 粒度 ~15ms 只让它更慢，不参与判定）；本轮**未新增任何路径/权限/mtime 判据**，无需 `t.Skipf`。


### 4.62 第五十七轮：外观回退到上游（**撤回有意偏离**）

用户在看过 `2.12.5`/`2.12.6` 的自研视觉后要求「还原上游的」。这两个提交的外观改动（事件短时间戳、内容通道、段标题、表格 60 列、
颜色角色与降级链、`█/░` 进度条、横幅去底色）**全部撤回**，恢复上游呈现：蓝底版本行 + 求助链接、完整时间戳、方括号流列表、40 格 `#` 进度条、`[ok]/[warn]/[fail]`。

**保留的判断**：机器契约面（`-I` 裸 URL、`--print-urls`、`--info-json`、`--progress-json`）自始至终未变，撤回不涉及它们；
与外观无关的功能与修复（`--nfo`/`--compat`/`--write-m3u`/`--log-file`/`resume`/重试分类/412 提示/SSE/进度观察者/节流修复/跨平台守卫）也不受影响。

**教训**：默认观感属于用户界面契约，不是可以按自己审美改的东西。若将来要做主题化，**必须加开关且默认保持上游风格**；
自研视觉留在 git 历史（`ef40461`、`9d36672`）备查。


### 4.63 第五十八轮：流列表与进度条的自适应宽度（**可用性修复**，观感保持上游）

用户报告：① 音视频流展示太宽、提示不足、无自适应；② 进度条撑到终端换行。

- **根因 1**：列宽六列写死（72 列）+ 内容行 28 列缩进 = 整行 100 列；无表头；`-I` 把 600+ 字符裸直链插进列表。
  修法：`util.TerminalWidth()`（注入 → `term.GetSize` → `COLUMNS` → 80；**非 TTY 直接 80**，不读 COLUMNS 以保证管道输出跨机器可复现）驱动单一排版 plan；
  每段打表头；降级顺序「体积 → 帧率 → 编码 → 分辨率 → 缩列宽 → 悬挂折行」；缩进可回收（装饰 vs 数据列的取舍）。
- **根因 2**：满字段帧 = 28 缩进 + 40 格条 + 动画 + 44 统计区 = **116 列**（我此前口算的 88 列漏了缩进）。
  修法：bar 宽度反算 + 统计区按 ETA→总量→速率 裁剪。
- **契约**：`-I` 管道裸 URL、`--print-urls`、`--info-json`、`--progress-json` 逐字节不变（用例钉住）；**事件行不参与宽度契约**（与上游一致）。

**用例与变异**：宽度表覆盖 40/60/80/120/200（每行 ≤ 宽度、表头存在且对齐、降级顺序、窄终端不退化、悬挂缩进）；7 组变异全断言红
（去自适应、去表头、管道也省略、固定 40 格、非 TTY 读 COLUMNS、续行顶格、终端打裸直链）。
**独立复验**（我做，不是代理自述）：80 列 `-I` 流列表 0 行超宽；真实下载 29 帧全部恰好 80 列、超宽 0；`-I` 管道仍逐字节原样。

### 4.64 第五十九轮：订阅调度 + serve 进度口径统一（**新功能 + 修复**）

- **`sub check` 订阅调度（自研）**：上游 v1.6.20 `SubCommand.cs` 无对应选项，本批为自研功能（无对齐物，仅登记）。
  `--since <duration>`（Go duration 语法，不支持 `"d"`，报错提示用 `"24h"`）按 `Page.PubTime` 增量过滤（PubTime<=0 保留+日志、恰等边界在窗口内）；
  `--concurrency <n>`（默认 1、上限 8）并行检查阶段、下载串行（渲染竞态二期再评估）；非法参数校验在清单加载之前、退出非 0、零状态文件；
  默认无参行为与 2.12.8 逐字一致（串行/并发两条路径）；并发共享 client/factory/wbi 已证安全（UA 在 uaMu 下、时钟偏移 atomic、Factory 只读，-race 钉住）。
- **serve 进度口径统一**：`ProgressEvent` 加文件身份 `Key`（按产物路径绑定）；`/get-tasks` 的 `TotalDownloadedBytes` =
  「按身份累计的实时字节 与 产物字节 的较大者」——与 SSE 同口径（单产物=最近一帧、多产物=Σ各身份最近一帧不回跳）；
  aria2c 无观察者路径保持边界语义。与上游对比（A-serve 第 30 条）：上游 `TotalDownloadedBytes` 由进度条累计，本次字节口径**向该语义靠拢**；
  **`Progress` 保持 0/1.0 边界语义为有意保守偏离**——bbdown-tasks.json 与上游兼容契约消费它，且实时分母（各产物总长）在多产物任务里不完整，
  理由写进 internal/server/progress.go 顶部声明。
- **修复 `avav101` 双前缀**（2.12.8 既有显示缺陷，用户确认作为缺陷修复）：格式串 `av` + `joinAids` 的 `av` 双重前缀 → 单前缀；
  属**有意偏离 2.12.8 逐字基线**的用户可见修复，同步更新既有逐字断言并变异验证（撤掉修复 → 断言红）。
- **修复手改清单重复 Target 去重**：并发路径丢失「前一条下载写进该 Target 历史」的跨订阅交互 → `--concurrency 2` 重复下载；
  `Load` 后按 Target 去重（保留先出现条目 + 日志点名），无重复清单行为逐字不变。
- 审查门双 PASS：无删断言（回退测试文件实证）、变异全红、真实 CLI 非法参数组、并发 barrier 真重叠、0 处 time.Sleep。

### 4.65 第六十轮：serve /add-task 白名单 + Web UI 重设计（**新功能**）

- **/add-task 15 字段显式白名单**（对齐「上游 SanitizeUntrustedOptions 的钳制面」这一设计意图，形态自研）：允许 url/select_page/dfn_priority/encoding_priority/multi_thread/overwrite/skip_mux/skip_ai/write_nfo/compat/use_app_api/use_tv_api/use_intl_api/work_dir/language（snake_case，与 MyOption json tag 一致）；
  **严格模式**：未知字段 400 点名、非法值 400 说明原因、危险字段（interactive/file_pattern/cookie/access_token/user_agent/danmaku_filter/notify_webhook/drm_*/insecure/decrypt_drm）一律 400；
  select_page 复用 workflow.ParsePageSelection（3 行纯包装导出，不复制实现）+ serve 侧 1000 项上限；bool 用指针字段区分「未传」与 false（multi_thread/skip_ai 默认 true）；
  老客户端（仅发 url）行为与 2.13.0 逐字不变。
  **兼容性变化（如实登记）**：HEAD 曾接受 {"URL":"x"}/{"Url":"x"}（解码器大小写不敏感）与 body 尾随内容（Decoder 只读一个值），2.14.0 起一律 400——从「静默忽略」变「显式拒绝」，契约字段为小写 url。
- **serve Web UI 重设计**（自研差异化能力，参照 gnattu/bbdown-webui 与 AriaNg 的功能面，见 docs/bbdown-ui-redesign.png）：任务列表（四色状态点/迷你进度）+ 详情卡（大进度条/统计网格/SVG 速率曲线/事件流/取消重试移除、暂停置灰）+ 15 字段表单（与白名单逐字一致、[data-field] 遍历生成）+ localStorage 预设；单文件 go:embed 34100 字节（≤64KiB）、无框架无 CDN 无外链；XSS 全 textContent 单一出口（黑名单含 createContextualFragment/setAttribute("on)；mergeEvent 跳过 __proto__ 纵深）。
- **真机冒烟修复**：页面取消/移除原先只带 token 不带 Content-Type，撞 guardMiddleware 的 JSON 闸门 → 实机 415；已修 + TestWebUIActionRequestsSatisfyGuardMiddleware 钉住（t8 交付期间抓到，证明中间件契约与页面形态必须同测）。
- 审查门双 PASS（t14/t10）：老客户端逐字实证、攻击面零副作用（9 组拒绝请求后零任务零状态文件）、XSS 三载荷三路径 81 次 textContent 落地、变异全红。

### 4.66 第六十一轮：CLI 观感对齐 BBDownT + Web UI/白名单同批（**有意偏离 + 新功能**）

- **有意偏离 1：CLI 观感对齐 BBDownT 2.x 接手线（用户定稿 v3）**。上游 v1.6.20 的流表是「声明 kbps + 带 ~ 的体积」，BBDownT 是「体积反推的 ~kbps + 不带 ~ 的体积」；
  本批起默认观感随 BBDownT（标题青字、发布时间/URL/UP主页并入元信息行、-i 渐进式分层选择、分P清单/字幕清单/⚠ 杜比标记/预计大小/分P游标/侧车行）。
  与上游的差异点：① 流表口径（见下条）；② 「视频标题:」青色（上游无色）；③ 头部信息行合并（上游三行分立）；④ 任务卡退役；⑤ 机器契约面（-I/--print-urls/--info-json/--progress-json/管道）不受影响。
  依据：BBDownT 源码 /tmp/BBDownT（Logger.cs/Program.Methods.cs/ProgressiveStreamSelection.cs——该类在其 v2 **未接线**，默认全自动选流；我们默认同样非交互，渐进式只挂 -i）。
- **有意偏离 2：体积估算除数 1024 → 1000**。上游 Display.cs 按「时长×bandwidth×1024/8」估算体积；1024 会与声明 kbps 反推值自相矛盾（声明 132 kbps → 反推 ~135）。
  改用 1000 后估算行反推值 == 声明值；真实 size 行显示真正的平均码率（声明 5000 → ~4194）。用户价值：流表数字自洽、不误导。
- 同批（§4.65 之后落地的增量）：⚠ 编号空间修复（-i 分层下曾用全表下标，现与屏幕表一致）；汇总字节按路径去重（对齐 serve 语义）；tracklayout 死常量清理。
- 审查门：t14/t10/t17/t18→t19/t20 全 PASS（含窄终端 20..300 零超宽、零额外 fetch 复算、⚠ 编号 ⊆ 屏上表行号断言）。

### 4.67 第六十二轮：四期——竞争修复、aria2c 进度、M3U 序号、testsupport 抽包（**修复 + 能力 + 重构**）

- **stdin 测试接缝数据竞争修复**：包级 stdinReader 的读写无 happens-before 边（-race 确定性红，15/16 观察项定位的战果）。接缝改 `atomic.Pointer[stdinSource]` + 访问器（生产行为不变）；
  blockingReader 加 entered 信号（「进读之前 cleanup 不得返回」）。ctx 感知的可取消读仍是候选（ROADMAP）。
- **aria2c 逐字节进度（能力补全）**：`--summary-interval=1` 摘要块经观察者管线发帧（SSE 受益）；上游/aria2c 无对齐物（自研）。审查真机实测抓出「摘要走 stdout、实现接 stderr」的接错流——
  **教训：单元级假执行器必须复刻外部进程的流，不只是行格式**。修复=双流接泵；无观察者路径逐字不变。
- **M3U 分P序号（自研扩展）**：`#EXT-BBDOWN-PAGE:N` 注释行（播放器忽略未知注释）+ 回读排序。排序口径与 2.15.0 语义一致（未知序号最小），
  升级混合场景（旧列表+补下新集）保持追加语义——t26 审查抓出的「新分P插队」回归已闭环并永久钉住（sidecar 级用例）。
- **testsupport 抽包**：cli/server 两份工作目录守卫合并 `internal/util/testsupport`，判据逐条一致、不进生产二进制。
- **下载并行评估文档**（docs/parallel-download-eval.md）：方案定案 B；发现 serve 多任务 TTY 下既有单行进度互踩（B 顺带修复）。

### 4.68 第六十三轮：字幕 URL 混淆登记 + Web 输入简化 + CLI 信息行宽度治理（**修复 + 生态对照**）

- **字幕「地址疑似失效」真相（2.15.2）**：新版字幕接口 x/v2/subtitle/web/view 返回的 subtitle_url 是服务端混淆令牌——宿主 subtitle.bilibili.com 公网 NXDOMAIN（AliDNS±中国 ECS + Cloudflare DoH 三方一致）、宿主与 ?auth_key 间为 7bit 密文块（三次抓取稳定）；字段号/长度无漂移（protowire 全量解码复核）。
  处理：解析时过滤不可用条目 → 回退老三条接口；侧车字幕四态（✔/✗/—）不再谎报。**AI 字幕（ai-zh 等）生态内无人可下**（实测对照：上游 v1.6.20 不碰新接口；BBDownT v2 用同接口但只校验 URL 形态、会把坏地址当可用下载时 DNS 失败）——
  本仓行为为三线最优（诚实回退），登记为**观察项**（若 B 站调整或出现公开解码方案则跟进）。
- **可吸收候选（BBDownT）**：--ai-subtitle-policy（exclude/include/prefer-human/only + SkipAi，SubtitleSelection.cs）——记 ROADMAP，未实现。
- **Web 输入简化（2.15.2）**：短号/多行批量/文件导入（与 CLI --urls-file 同语义），后端零改动（BV 短号本就可解析）。

### 4.69 第六十四轮：上游同步批次（1.6.21~1.7.3 + BBDownT 2.1.7）——六项吸收与真实偏离

吸收（来源逐条可查）：
1. **字幕 URL 双重混淆解码** ← BBDownT 7408653（SubtitleUrlResolver.cs：%-解码 → XOR → prefix 剥离 → 宿主重写；常量逐字移植并比对）。**本仓超出 BBDownT 的一步**：移除「新版接口仅 cookie 才试」门控——t50 审查实证匿名链路全通（405B 响应 → 解码 → CDN 匿名 200/7578B），BBDownT 未做此步。另：--skip-ai 默认改 false（与 BBDownT 默认 include 一致，用户拍板）。
2. **mp4decrypt `--key` 参数** ← 上游 1.7.2（Decrypt.cs:212），形态逐位同形；小写化位置不同（上游源头、本仓构造时），结果一致。
3. **v_voucher 风控识别** ← 上游 1.7.3。**真实偏离两条**（t52 审查权威结论，此前「上游只认 data 且静默」的叙述不成立）：(a) INTL 第一轮命中时上游立即抛错、本仓记录后继续试第二轮（更耐风控，TestIntlVoucherFirstPassSecondPassWorks 钉住）；(b) 识别面多一档（data.result 叠加形态上游不查）+ Go 哨兵 errors.Is + 文案点出 --retry-count/--retry-delay。
4. **订阅增强三件** ← 上游 1.6.21/1.6.22：--per-sub-dir、mid: 增量扫描（默认）、--full-scan。本仓兼容策略：deps 只加可选字段、旧签名保留、默认 byte 级不变；分目录+增量下多P 命名不带空间 fetcher 前缀（与上游增量语义一致，固有取舍）。
5. **argv `-` 开头值** ← 上游 1.6.22 的对齐核查：pflag 本就 GNU 正确（**无需移植**，防回归用例钉住）；反抓到同族本仓 bug（值位置别名重写）并修。
6. **DRM 开箱即用** ← 上游 1.7.1：内置 wvd/mp4decrypt、自动解密默认开、--no-decrypt-drm、解密前置检查。工程差异：SHA256 采用「仓库变量 + fail-closed」；wvd 取自上游公开提交的 BBDown.Core/device.wvd（sha256 b16fe9ca…，THIRD-PARTY-NOTICES 注明专有+来源）；linux-arm64 不内置 mp4decrypt（官方无预编译包，与上游一致）。

上游情报（已核、未跟）：1.7.0 异常过滤器收敛/重构（用户不可见）、sha256sums 路径修复（我们的 Release 校验文件无此问题）；BBDownT 2.1.7 的断点续传/国际站修复（候选，未评估）。

### 4.70 第六十五轮：更名 BBDown-Go（3.0.0）

- 仓库与 Go module 路径从 `QC3284/BBDown` 更名 `QC3284/BBDown-Go`（用户拍板）：独立品牌落地；二进制名保持 `BBDown`（CLI/产物/配置契约零变化）；旧链接由 GitHub 301 承接。
- 版本位：module 路径变更对库导入者是不兼容变更 → MAJOR（3.0.0），与更名同发。
- README 重写（244 行）：功能清单对齐 2.13.0~2.16.0；旗标以 --help 白名单逐字核实（62 旗标 0 无法核实）；重试口径按代码实际行为订正（412 参与 HTTP 层重试 1s→2s→4s + 成功轮换 UA 后才重试；页面级恒定 --retry-delay×3 次；语言表 146 项）。

### 4.71 第六十六轮：CLI 宽度治理第二步（有意偏离上游事件行契约）

- **标题行折行 + 事件行截断（用户拍板「标题折行+事件行截断」）**：上游对超宽行不折行不截断（终端自行换行）；本仓在**交互终端**下——标题行按显示宽度折行、续行缩进 28 列、极窄终端（40 列）让出时间戳；常规事件行超宽截断 +「…」。
- **只在交互终端生效（TTY 判定）**：管道/重定向（脚本、CI 日志、输出写文件）不截断——没有列宽约束，截断只丢信息；与 TerminalWidth 的 TTY 语义一致。
- **不动清单**：URL 行（-I/--print-urls 脚本契约）、流表/进度/侧车（2.12.8 契约）、时间戳压缩（t39 建议不做）、debug 行 120 列预算（候选另议）。
- 顺带：**emoji 宽度表统一**——download.DisplayWidth 与 util 新宽度表同口径（0x1F300-0x1FAFF 计 2），修掉 t43 报过的 🚀 漏算（download 侧此前同病）。

### 4.72 第六十七轮：分P选择前导零（吸收上游 master c39cae4）

- **前导零规范化**：`-p 01` 匹配 P1（上游 master c39cae4，v1.7.3 之后的未发布修复；Pages.cs 单 token 分支 int.TryParse → ToString）。本仓 parsePageSelection 同病已修，语义逐字同上游。
- **本仓额外收口（覆盖面比上游广）**：`?p=01`（URL 分P参数）与 `VInfo.Index` 两个原样字符串来源一并改为数值匹配（上游只修了 -p 一路）；非数字项退回字符串相等，行为不变。
- **已免疫项**：int.MaxValue 计数回绕（本仓 maxExpandedPages 段长/累计两处检查都在展开循环之前，无回绕窗口）；展示截前 20 项+总数（maxShownPages=20，与上游 MaxReportedPages 同）。

### 4.73 第六十八轮：上游同步大批次（1.7.4~1.7.7 + BBDownT 2.1.7 功能件）——吸收与有意偏离

**吸收（来源逐条可查）**：
1. aria2c 预分配中断文件误判完整 → 「大小相等 + <文件>.aria2 存在」判未完成、成功后清理（上游 1.7.4 / 5e57baf 语义一致）；
2. TV/APP/INTL token 用户误报「未登录仅能试看 6 分钟」→ 按「模式 + 已加载 token」分流（上游 1.7.5）；
3. gRPC 同时检查 grpc-status（trailer 优先、header 兜底）——HTTP 200 权限拒绝不再误报 protobuf 损坏（上游 1.7.4）；
4. sub check --delay-per-item 节流（上游 1.7.7，事件流/边界/取消语义逐条对齐）；
5. live 完整性：截断长度前缀包拒绝 + 零媒体帧段跳过（全零才失败）+ 帧数守恒产物核对（上游 38799fc / 1.7.4 / 1.7.5）；
6. 分片失败取消兄弟分片 + 完整跳过回收残留（上游 1.7.4/1.7.5，t82 评估 P1）；
7. 显式工具路径不存在告警（上游 1.7.5，P2）；DRM 侧补「你给的路径不存在」hint；
8. BBDownT 功能件：--subtitle-language / --ai-subtitle-policy（含 EffectiveSkipAi 接线）、--audio-language（清单+选择+命名+cur_language 重请求）、--delay-per-video / --download-all（--save-archives 既有实现未重复）、self-update（digest fail-closed、只换主二进制、Windows 回滚）。

**有意偏离/取舍（对账勿误判为落后）**：
- --delay-per-item 并发路径（--concurrency≥2）仅节流下载、不节流检查启动（避免抵消并发；上游 1.7.7 只覆盖串行事件流）；
- live 合并 Legacy 分支：整场非 FLV（HLS/TS/既有合成负载）沿用旧大小路径，仅「部分 FLV+坏段」整场失败（兼容多源）；
- grpc-status 非数字不认、落回原解析路径（上游只要存在就抛——本仓更合理）；
- --audio-language：APP(gRPC)/INTL 未接 cur_language 重请求（响应侧 confirm 仍核对、不一致即报错不静默下错；双模式支持记候选）；
- self-update 用 GitHub 资产 digest 而非独立 checksums 文件（缺摘要即拒更新）。

**已免疫（登记备查）**：TV playurl 的字符串 result（Go 类型断言天然放行，上游 1.7.6 同病我们未病）；替换 host 404 回退（§4.33 早于上游）；多线程续传布局校验（本仓无多线程续传，不同病——若将来补续传必须同时做布局校验）；服务器透传文本净化（logger/file 层已有 Sanitize）；DRM 唯一临时文件（t45 已是 .dec+rename 语义）。





- **CLI 信息行宽度治理第一步（2.15.2）**：v3 信息行与 --debug 长行按显示宽度夹取；实测三档超宽 26/26/17 → 0/0/0。第二步（标题行/事件行）已获用户批准，下一工作周期实现（属**有意偏离上游事件行契约**，实现时在此登记）。






