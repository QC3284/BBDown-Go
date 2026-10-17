# 交接文件（HANDOVER）

> 面向接手的下一位（人或代理）。**先读 `AGENTS.md`**（项目规矩），再看本文件（现状 + 坑 + 待办）。
> 最后更新：版本 `3.1.0`（上游同步大批次：五件同病修复 + BBDownT 六功能件），提交见 tag `v3.1.0`，

---

## 1. 一句话现状

此后独立演进。当前版本 **`3.1.0`**，已发布 tag / GitHub Release（5 产物，含内置 device.wvd+mp4decrypt）/ AUR 包 `bbdown-go-git`（VCS 包，随 tag 自动更新）。

规格来源有四个，冲突时**以实测为准**：上游 C#（本地 git 对象库即可查）、`LOVAHE/BBDownT`（C# 2.x，风控情报价值最高）、
`bilibili-API-collect`、探针实测。

---

## 2. 干活前先记住的验收判据（本次会话血泪换来的）

```bash
gofmt -l internal/ cmd/            # 必须无输出
go build ./... && go vet ./...     # 必须 0
go test ./... -count=1             # 判据是**退出码**，不是「数 ok 行数」
```

- **不要用 `grep -c '^ok'` 当判据**：12/16 也会返回成功。用 `go test ./... -count=1 > /dev/null && ...` 串起来。
- **每次改动都要变异验证**：撤掉实现必须让用例**断言红**（不是构建错误）。
- **不要用 `.catch(() => {})` 吞掉工具调用失败**：本次会话因此让台账 §4.47–§4.58 **静默缺失**，而 CHANGELOG 一直在引用它们，
  最后靠一个代理发现才补回。**吞错 = 把失败变成谎言**。
- **变异验证用编辑器改回**，不要 `git checkout -- <文件>`（会把同文件其它未提交改动一起丢）。

---

## 3. 三条跨领域机械规则（已写进 `AGENTS.md`）

1. **判据必须跨平台**：路径比较一律 `os.Stat` + `os.SameFile`（**禁止字符串相等**——macOS 上 `/var` 是 `/private/var` 的符号链，
   `os.Chdir("/var/…")` 成功但 `Getwd()` 返回解析路径）；已删除目录里的 cwd 在三平台语义不同（Linux `getcwd` ENOENT、Windows 返回已消失路径、
   macOS 保留 name cache）；mtime 粒度/权限/符号链接创建能力差异用显式 `t.Skipf` 写明。**写守卫前先列「判据 × 三平台语义」表。**
   （教训原文见 `docs/UPSTREAM_ALIGNMENT.md` §4.58；最值得记的是：**这条规则在修复它自己的代码里复发过一次**。）
2. **优化必须有改前/改后数字**（耗时/请求数/CPU/字节数），没有数字不算优化。
3. **默认观感属于用户界面契约**：本次会话我按自己审美改了两版 CLI 输出（`2.12.5`/`2.12.6`），
   用户明确要求「还原上游的」→ 已 `revert`（`2.12.7`）。**要做主题化必须加开关且默认保持上游风格。**
   自研视觉留在 git 历史 `ef40461`（清爽化）与 `9d36672`（视觉重做）备查。

---

## 4. 已完成的能力（本次会话线）

| 类别 | 内容 |
|---|---|
| 新功能 | `--nfo`（侧车元数据）、`--compat`（避开 HDR Vivid/杜比视界）、`--write-m3u`（播放列表，含读回合并）、`--info-json`（解析结果机读化）、`--log-file`（日志落盘）、`--print-urls`、`--overwrite`、`bbdown resume`（断点续跑）、`bbdown doctor`（`--json`）、**`sub check --since/--concurrency`（2.13.0 订阅调度，可 cron）** |
| 服务端 | `serve` 的 **Web UI + SSE**（`GET /` 内嵌单页、`GET /events` 推 `task_start/task_progress/task_done/task_failed`；连接上限 64、队列满丢帧、回放 64 条 + `seq` 去重）；**SSE 已接真实字节进度**（`download.WithProgressObserver(ctx, fn)`，context 携带、无全局）；**2.13.0 口径统一**：`/get-tasks` 的 `TotalDownloadedBytes` 与 SSE 同口径（`ProgressEvent.Key` 文件身份 + max(Σ实时, 产物)），`Progress` 保持边界语义（上游兼容契约） |
| 风控/网络 | 412：UA 轮换 + 退避 1s→2s→4s（上限 8s），且需先成功轮换 UA 才重试；**按错误分类的重试**（`planTrackRetry`）：确定性 4xx 不重试、网络错误有候选立刻换、无候选短退避；下载层 412 最终错误带可操作提示（`util.StatusHint` 单一来源） |
| 交互 | Ctrl+C 双击语义（首次优雅取消 + 提示 / 二次退出 **130** / 退出前恢复终端），**全局子命令**生效；收尾汇总行；进度条口径统一（含续传 `base`） |
| 接口 | **新版字幕接口是 protobuf**（`x/v2/subtitle/web/view`，字段号取自 BBDownT 的 `dmviewreply.proto`），手解三字段、新接口优先、老三条回退、**只在有 cookie 时试** |
| 可用性 | 流列表**自适应终端宽度 + 列标签**（放不下按 体积→帧率→编码→分辨率 省列 → 缩列宽 → 悬挂折行）；**进度条不再撑到换行**（宽度反算）；`-I` 直链 TTY 省略、**管道逐字节原样** |
| 工程 | 宽度表收敛（三份 → `download.DisplayWidth/PadDisplay`）；测试卫生（包级工作目录守卫，防状态文件被写进包目录/误提交）；pacer **节流窗口多画一帧的真缺陷**修复；时间判据去墙钟（可注入时钟） |

---

## 5. 未完成与已知遗留（**接手优先看这里**）

`docs/ROADMAP.md` 第 98 行起是「下一批（未完成）」。要点：

1. ~~**订阅调度**~~ **已完成（2.13.0）**：`sub check` 支持 `--since`（Go duration 语法，按 Page.PubTime 增量过滤）与 `--concurrency`（并行检查、下载串行），可 cron；详见 CHANGELOG 与 UPSTREAM_ALIGNMENT §4.64；
2. ~~**Web UI 增强**~~ **已完成（2.13.0 + 2.14.0）**：2.13.0 统一字节口径（`ProgressEvent` 文件身份、`/get-tasks` 与 SSE 同口径）；
   2.14.0 整页重设计（任务列表+详情卡+15 字段表单+localStorage 预设，单文件 34KB 无外链）；
   字节口径细节
   （单产物=最近一帧、多产物=Σ各身份最近一帧不回跳、aria2c 无观察者保持边界语义）；`Progress` 仍是边界语义（0/成功 1.0，上游兼容契约，有意不改）。
   勘误：原计划写「同步改 3 处既有用例」，实测钉旧口径的只有 1 处（progress_test.go 产物帧断言），另加 download 侧 3 处身份断言；
3. **`--progress-json` 首窗速率**在续传时仍含 `base`（既有行为，未修）；聚合帧在「分片计数瞬时越过总长」时可能短暂显示 `4.0/3.0 MB`（pct 已夹 100%）；
4. ~~**aria2c 路径没有逐字节进度**~~ **已完成（2.15.1）**：解析 `--summary-interval` 摘要块经观察者管线发帧（双流接泵——真 aria2c 摘要走 stdout）；`--progress-json` 仍无 aria2c 事件（走独立 emitter，候选已登记 ROADMAP）；
5. **`~` 估算标记**（**勘误，本条过时**）：2.12.8 起体积列**全部**带 `~`，与上游 C# `Display.cs` 一致；「精确值不带 `~`」属于有意偏离，需开关+登记差异，未采纳；
6. ~~**M3U**：回读条目的**分P序号缺失**~~ **已完成（2.15.1）**：`#EXT-BBDOWN-PAGE:N` 自研注释 + 回读排序（未知序号=旧条目最小，升级用户补下新集保持追加）；要彻底解决需把序号写进文件；
7. ~~**`internal/util/testsupport` 抽包**~~ **已完成（2.15.1）**：判据合并进 `internal/util/testsupport`，两侧薄接线共用同一实现；
8. **风格化（若要做）**：见 §3.3——必须加开关、默认上游风格；
9. **偶发 15/16 的观察项**：2.15.1 四期负载 campaign 7 轮（24+8 suite，峰值 load 31）**未复现**纯负载 flake；但抓到并修掉 stdin 测试接缝的**确定性数据竞争**（`-race` 确定性红→绿）。剩余观察按新纪律追：**先冻结工作树** + `go test -json` 记失败用例名。候选：CI 加 workflow 包 `-race`。
10. **/add-task 白名单边界**（2.15.0）：work_dir 无根目录约束 + os.Chdir 进程级副作用（有意设计风险：非回环必须带 token）；select_page 1000 上限在展开后判定（优化候选：先按表达式计数）。
11. **Web UI renderList 每帧重建 DOM**：几百任务看板需增量更新。
12. **下载并行实现**（五期首选，按 docs/parallel-download-eval.md 表 1/表 2 开工，B 路线 ≈4.5 人日 + 量化 0.5）。

---

## 6. 环境事实与坑（本机）

- **未登录**：匿名只能拿低清晰度、**拿不到字幕**——字幕路径（`x/v2/subtitle/web/view` protobuf）**真机未验证**，登录后请跑 `bbdown --sub-only <URL>` 或 `make smoke` 复核；
- **mp4box 缺失**；ffmpeg 存在且带 DOVI 支持；`doctor` 会如实报这两条；
- 测试视频：`BV1xx411c7mD`（av2，2055s，已用于多数真机验证）、`BV1ZH4y167mH`；
- **AUR 构建偶发瞬时失败**（拉源超时，输出 `正在放弃...`）：**重跑一次即可**，不是代码问题；`PKGBUILD` 是 VCS 包 `BBDown-Go-git`；
- **验证终端观感必须看 TTY**：管道/重定向下按设计无色（`NO_COLOR`/`TERM=dumb`/非 TTY → 零 ANSI），
  用 `| sed 去 ANSI` 复核等于没看——用 `script -qec "<cmd>" /dev/null` 抓伪终端，再对照「元素 × 色码」表；
  量显示宽度用 Python `unicodedata.east_asian_width`（中文按 2 列）；
- 本沙箱环境自带 `NO_COLOR=1` + `TERM=dumb`，要复现彩色 TTY 需 `env -u NO_COLOR TERM=xterm-256color`；
- 版本号有 **5 处硬编码**，必须同步：`cmd/bbdown/main.go` 横幅、`internal/cli/root.go` 的 `Version` 与更新检查、`internal/cli/commands.go` 的更新检查、`PKGBUILD`（有 `version_consistency_test` 守着）。

---

## 7. 发布流程（每次改动都走）

```bash
# 1) 验收（判据=退出码）
gofmt -l internal/ cmd/ && go build ./... && go vet ./... && go test ./... -count=1 > /dev/null

# 2) 文档三件套（偏离留痕 + 变更说明）
#    docs/UPSTREAM_ALIGNMENT.md（§4.x，注意是**降序**追加；本次会话的 §4.47–§4.63 已在文末）
#    CHANGELOG.md（版本 + 差异点 + 用户价值）
#    docs/ROADMAP.md（完成项划掉、新遗留登记）

# 3) 版本前移（内核式：功能批次 → minor；修复/优化 → patch，**patch 是默认动作**）
# 4) 提交（Conventional Commits，正文用中文说明**为什么**）
git add -A && git commit -F /tmp/msg.txt --no-verify && git push origin main && git tag vX.Y.Z && git push origin vX.Y.Z

# 5) 发布后核对三件：tag CI 三平台绿、Release 产物 5 个、AUR 包名（失败就重跑一次）
gh run view <id> -R QC3284/BBDown-Go --json jobs,conclusion --jq '.conclusion + " | " + ([.jobs[] | .name + "=" + (.conclusion // "-")] | join("  "))'
```

---

## 8. 接手第一步建议

1. 跑一遍 `go test ./... -count=1` 与 `make smoke`（后者需联网，会做真实下载 + ffprobe + 进度 JSON）；
2. 读 `docs/ROADMAP.md` 的「下一批（未完成）」；
3. 按 §5 的编号挑一件：**下载并行二期**（`--concurrency` 目前只并行检查、下载串行，大件）或 §5.3/§5.6/§5.7 的小项（低风险、适合热身）；
4. 动任何东西之前先想清楚：**这条判据跨平台吗？有变异验证吗？会有数字吗？会不会改变默认观感？**
