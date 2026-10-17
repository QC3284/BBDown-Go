# 路线图（优化 / 新功能）

> 方向：**以上游为基线，在其上做优化与新功能**（2026-09-24 起，见 `AGENTS.md` 最高约束）。
> 本文件只放**候选与优先级**；判定与差异登记在 `docs/UPSTREAM_ALIGNMENT.md`，逐版变更在 `CHANGELOG.md`。

## 外部信号（来自另一条接手线 LOVAHE/BBDownT 2.x，2026-09-25 调研）

背景：原始 `nilaoda/BBDown` 已归档，社区分成两条接手线——我们的基线 `AliverAnme/BBDown`（C# 1.6.x）与
`LOVAHE/BBDownT`（C# 2.x，默认分支 v2，dotnet tool 分发）。后者 2.1.x 连续三版都在做**新风控适配**，
与我们在解析阶段撞到的 HTTP 412 高度相关。

| # | 项 | 现状（本仓） | BBDownT 的做法 | 优先级 |
|---|---|---|---|---|
| P0 | 412 风控 | **已完成（`2.1.0`）**：412 时轮换自动 UA + 退避重试（3 次/1s），显式 `--user-agent` 不轮换；依据 ea0e825/5d2b088，但**不照抄 UA 池**（探针实测低频下 UA 类别不是主因） | 上游 4xx 一律直接抛出 | 已发布 |
| P1 | 字幕接口 | **已定性（真实缺口）**：我们只有三条老接口（`x/web-interface/view`、`x/player/wbi/v2`、`x/player/v2`），没有新接口 | 3625f8f 改用 `x/v2/subtitle/web/view?oid=<cid>&pid=<aid>&context_ext=...&type=1&cur_production_type=0&preferred_language=ai-zh&playlist_switch=0`，`Accept: application/octet-stream`，**响应是 protobuf**（`SubtitleWebReply`），且**放弃了 player/view 系回退** | **高**（不是换 URL：要加 protobuf 解码） |
| P2 | 登录态请求配置 | 用浏览器 UA 时不配套浏览器指纹头 | 46f08da「完善登录态浏览器请求配置」 | 中 |
| P3 | 特性对照 | **HDR Vivid 已完成（`2.2.0`）**：QualityMap 加 129、APP 最高档提到 129、解析最高档同步；配音语言与分P调度解耦仍待做 | 2cd4084；ccd06fd | 低~中 |

**2026-09-25 探针复现（先量再学）**：同一接口 `/x/web-interface/view`、各 8 次、间隔 0.8s——
浏览器 UA 与传输型 UA **都是 8/8 个 200**，没有任何 412。结论：**低频率下 UA 类别不是 412 的主因**
（此前那轮 412 是高频解析自己招来的）。因此：**不照抄 UA 池改动**，只实现「412 → 轮换自动 UA + 退避重试」
（成功路径零变化），UA 池本身降级为「需要更高强度/更贴近真实场景的复现证据再决定」。
**P1 待办（下一步）**：仓库已有 protobuf 依赖（`internal/drm/proto`），计划用 `protowire` 只手解需要的字段
（lan / lan_doc / subtitle_url）——先从 BBDownT 的生成代码取字段号，再补夹具回放用例；新接口作为**首选**，
我们现有的三条老接口保留为回退（覆盖度只增不减）。
来源：[BBDownT releases](https://github.com/LOVAHE/BBDownT/releases) 与其提交 10db034/5d2b088/ea0e825/46f08da/3625f8f/2cd4084/ccd06fd。
注意：BBDownT 与基线不是同一代码线，**不能直接移植**，只能作为规格与线索来源；跟进时同样要按本仓规矩
补回归用例、做变异验证，并在差异台账登记。
## 规则

- **进入清单**：写清「现状 → 目标 → 怎么量化 → 规模」；优化必须能给出改前/改后的数字。
- **出清单**：做完 → `CHANGELOG.md` 记数字 + 差异表登记（与上游行为不同时）→ 移到「已完成」。
- **优先级**：可量化、影响真实使用路径、改动可控的先做；没有数字的「感觉慢」不进清单。

## 优化候选

| # | 项目 | 现状（实测） | 目标 | 怎么量化 | 规模 | 状态 |
|---|---|---|---|---|---|---|
| O1 | 启动延迟 | `--version` 中位 **4.6 ms**（min 4.2 / max 48.8，10 次） | 保持；48.8 ms 的抖动若可复现再查 | 计时 × 10 取中位与最大 | 小 | 已测：暂无需优化 |
| O2 | 解析阶段请求数 | 单次 `-I` 解析：**0.59 s / 5 个 GET** | 画出这 5 个请求的来龙去脉，能省则省 | 同一视频 `--debug` 数 `GET` 行 + 墙钟 | 小~中 | **已完成（见下）** |
| O3 | 进度条 CPU | 单帧 **2.4 µs** → 60fps 上限 ≈0.015% CPU | 无需优化 | 单帧基准 × 帧率上限 | 小 | **已完成（见下）** |
| O4 | 分片大小自适应 | 固定 20MB：40MB 文件只切 3 片 | 未指定时按并发数倒推分片 | 限速服务器墙钟 + 连接数 | 中 | **已完成（见下）** |
| O5 | 断点续传命中率 | 清单比的是**完整 URL** → B 站每次解析换签名 → 跨进程续传**永不命中** | 重跑只补缺口 | 重跑下载字节 / 总字节 | 中 | **已完成（见下）** |
| O6 | 混流阶段进程启动 | 每次混流 1 次；杜比视界探测每 P 一次且有 2 个掩盖型 bug | 探测按二进制缓存 + 修正版本规则 | 假 ffmpeg 计数 + 版本边界表 | 小 | **已完成（见下）** |
| O7 | serve 并发任务 | 闸门内联在 processTask，只能靠真实网络任务走到 | 可离线量测并回归 | 峰值并发计数（不用墙钟） | 中 | **已完成（见下）** |
| O8 | 二进制体积 | 本地 `go build` **14.4 MB**；发布产物 `-s -w -trimpath`（linux_amd64 压缩包 3.7 MB） | 不追求更小，除非影响启动/分发 | `ls -l` 与启动耗时 | 小 | 已测 |

## 新功能候选

| # | 项目 | 价值 | 与上游的差异 | 规模 |
|---|---|---|---|---|
| F1 | 模板变量扩展 | **对账结论：上游 17 个占位符本仓全有**（`<ownerMid>`/`<publishDate:格式>`/`<videoCodecs>`…），不另发明新名字 | — | 小 |
| F2 | 批量输入 | 脚本化批量下载 | 上游只接受单个位置参数（多余的**静默忽略**） | 小~中 |
| F3 | 产物校验 | 防静默损坏 | 上游只校验长度 | 小 | **已完成（见下）** |
| F4 | 多镜像回退扩展 | 抗单点 CDN 抖动 | 上游无回退；本仓扩为有序候选链 | 中 | **已完成（见下）** |
| F5 | 进度事件 JSON | 外部集成（GUI / 自动化） | 上游只有终端进度条 | 中 | **已完成（见下）** |
| F6 | 订阅增强（正则过滤） | 订阅更可控 | 上游 `sub` 无过滤 | 中 | **已完成（见下）** |
| F9 | `bbdown doctor` 自检 | **已完成（`2.4.0`）**：外部工具 / 输出目录 / 接口与登录态，退出码可用于脚本 | 两条 C# 线都没有 | 小 |
| F7 | 配置档案（profiles） | 多套配置快速切换 | 上游单配置文件 | 中 |
| F8 | 下载队列与调度（CLI 侧并发/串行队列） | 批量不手写循环 | 上游无 | 大 |

## 已完成

### F3 产物校验（`1.6.20-go.5`）

先把「B 站声明的 size 能不能用来判定」量清楚（真实响应实测 BV1ZH4y167mH/cid=1314018414）：

- **不能用**：现行 web playurl 的 dash.video/audio 节点**根本没有 size 字段**；bandwidth×时长只是估算，
  实测偏差 hevc +0.04%、av1 **−1.39%**、audio +0.03%；durl/FLV 的 size 虽精确（8918136 == Content-Length），
  但 FLV 会经 ffmpeg 转 .ts 再合并，产物 ≠ 分段之和。
- **判定只用可靠来源**：HTTP Content-Length / Content-Range 权威总长 / 实际写入字节。

四处修复：① 声明长度未知时无条件丢弃陈旧 .tmp（旧代码 206 分支既不删也不截断，尾部残留会留在产物里，
`--skip-mux` 下无人兜底）；② HEAD 与 206 两次声明矛盾即报错（旧行为会先把内容落盘，分片路径甚至会把
另一份资源的 4MB 前缀当成功合并出去）；③ aria2c 路径补长度复核（此前该路径零校验）；④ workflow 侧
拦下 0 字节的原始轨道/FLV 产物（200 + 空体时旧行为一路判成功）。

### F4 多镜像回退扩展（`1.6.20-go.5`）

候选链数据驱动、无新增开关：parser 保留每条轨道的 backup_url 候选（首选规则不变；非 PCDN 在前、带端口
直连最后、去重），workflow 把「替换前原地址 → backup_url」传给下载器，下载器按序回退。

- 换候选的触发：404 **或**连接/传输失败；其它错误（非 404 状态码、Range 不支持、本地磁盘错误、ctx 取消）
  保持原地址重试，**不烧候选**；
- 尝试次数 = max(--retry-count, 候选数)，硬上限 8；**没有候选时与改前逐字一致（3 次）**，9 次阶梯不动。

### F5 进度事件 JSON（`1.6.20-go.5`）

`--progress-json`（默认关，写 stderr）：逐行 JSON，字段 {percent, downloaded, total, speed, state}，
事件 74~90 字节/行；频率与终端进度条同一节流（≤62 帧/秒，静默心跳 ≤8 帧/秒）；关闭时每次分片上报只多
一次 atomic load、无分配无 IO，终端路径代码逐字未改。续传场景 base=offset，末帧报整文件已完成。

### F6 订阅正则过滤（`1.6.20-go.5`）

`sub add --filter <正则>`（按稿件标题过滤，RE2；非法正则当场报错且不落盘），`sub list` 显示过滤条件
（空过滤时该行与改前逐字相同），`sub check` 应用；旧订阅文件（无 Filter 字段）兼容。

## 下一批（未完成，接着做）

1. ~~**P1 字幕 protobuf**~~ **已完成（`2.8.0`）**：字段号取自 BBDownT 的 `dmviewreply.proto`，手解三个字段、新接口优先、老三条回退；**待登录后真机确认**。原计划：：现役三条老接口（`x/web-interface/view`、`x/player/wbi/v2`、`x/player/v2`）在**未登录**时
   拿不到字幕（真机实测），新接口 `x/v2/subtitle/web/view`（`Accept: application/octet-stream`）返回 **protobuf**，
   需要 `SubtitleWebReply` 的字段号：先试 BBDownT 仓库的生成代码（不在 `.proto` 里，可能是 protobuf-net 的
   `[ProtoMember]` 分散定义），拿不到就抓一次真实响应（登录后）按 wire 格式反推；然后用 `protowire` 手解
   `lan`/`lan_doc`/`subtitle_url` 三个字段，新接口作首选、老三条保留回退，补夹具用例 + 变异。
2. **测试辅助迁移**：`internal/workflow/fakecdn_test.go` 已提供 `newFakeCDN`/`newFakeAPI`（自动给出
   `ForceReplaceHost=false`、`AllowPcdn=true` 的配置）；把 `overwrite_test.go`、`printurls_test.go`、
   `hdr_reminder_test.go`、`onlyshowinfo_chapters_test.go` 改成用它，消掉「假地址被生产逻辑改写」这一类坑。
3. **NFO 侧车元数据**：渲染层已完成（`internal/download/nfo.go` 的 `RenderNFO` + 用例，字段/转义/单P不写 episode/
   时间未知不写 aired）；**接线待做**——`--nfo` 开关 + 产物成功后写 `savePath + ".nfo"` + 变异验证。
4. **偶发 15/16 的定位（观察项）**：全仓 `go test ./... -count=1` 在**高负载**下（并行跑测试、多代理同时作业）出现过数次
   单包失败，空载连跑 8 次 0 失败，且每次都来不及抓到失败用例名 → 怀疑是**负载敏感的时序用例**（墙钟/进度/超时类）。
   下一步：在有负载的情况下连跑并落盘 `--- FAIL` 行（脚本已备：8 轮循环写 /tmp/flake.log），拿到名字再修；
   在那之前 CI 是判据。
5. **进度条的两处已知回退（小）**：① 续传时 `x/y` 与 ETA 不含 `base`（高估剩余），修前先定「终端百分比是否改用 base+current」；
   ② 多线程**聚合**渲染仍是旧格式，可直接复用 `renderProgressInfo`。
6. **宽度表收敛（小）**：`displayWidth`/`padDisplay` 现有三份同名实现（`internal/download/tracklayout.go` 已导出，
   `internal/cli/doctor.go` 与 `internal/workflow/taskcard.go` 各私有）。收敛方向：后两者改用 `download.DisplayWidth`
   （两个包都已 import download），或统一上移 `internal/util`；做的时候三处用例都要保留「显示宽度一致」这条判据。
6. **`--info-json 的输出分流（试过一次，失败，已回退；下面是诊断）**

   目标：机读模式下 stdout 只留 JSON（日志走 stderr、横幅不打印）。**第一次尝试（提交 f828cba，已 revert）**：
   把 logger 的 11 处 `fmt.Print` 改成 `fmt.Fprint(consoleOut, …)`。两个坑：
   ① 输出目标被**冻结在变量**里 → 测试常用的「替换 `os.Stdout` 捕获输出」失效（改成每次动态解析后这一条解决）；
   ② 动态化之后 `internal/cli` 仍有 5 条用例拿到空串（跨用例泄漏）——说明**全局可变输出目标**这个设计本身与
   「测试会替换 os.Stdout」的既有约定冲突。

   正确做法（下一轮按这个做）：**不要动全局日志目标**；改为给「数据输出」一条独立通道——例如 `--info-json` 时
   把 JSON 写入一个显式的 data writer（默认 stdout，可注入），日志保持原样走 stdout 之外（或让机读模式把日志降级/静默）。
   先写守卫用例：跑一次 `doctor --json` 后，同包后续用例用「替换 os.Stdout」仍能捕获到日志；再动实现。

5. **`--info-json` 的日志分流（小）**：JSON 目前与日志同在 stdout，管道消费要截取最后一段；让 `--info-json` 时
   日志走 stderr（或加 `--quiet`）即可，根因在 util 日志缺少输出分流。
6. **M3U 改进（小）**：时长目前写 `-1`，改用 `page.Dur` 需同时更新 `m3u_sidecar_test.go` 的两条接线期望（渲染层已支持）。
5. ~~**下一批功能候选**~~：`--compat` 智能选档 **已完成（2.9.0）**；订阅调度（`sub check --since/--concurrency`）**已完成（2.13.0）**。

### 2.13.0 批次（2026-09-28）闭环

- 完成：订阅调度（t1/t3）、serve 进度口径统一（t2/t4）、`avav101` 双前缀修复、重复 Target 去重（t5/t6）。
- 新增遗留（随本批发现）：
  1. **下载并行**（`--concurrency` 二期）：当前只并行检查阶段、下载串行——并行下载会引入终端进度条/SSE 渲染竞态，需先评估渲染层互斥；
  2. `--progress-json` 首窗速率续传含 base（既有行为，未修）；聚合帧在分片计数瞬时越过总长时显示可能短暂超限（pct 已夹 100%，终端显示口径待定）；
  3. aria2c 路径无逐字节进度（黑盒）；M3U 回读分P序号缺失；`internal/util/testsupport` 抽包；偶发 15/16 观察项；
  4. ROADMAP「进度条两处回退」（续传 x/y 不含 base、聚合旧格式）经核实已被 2.11.2/2.12.4 口径统一覆盖，本条关闭；
  5. 交接 §5.5「`~` 估算标记」经核实过时（2.12.8 已全带 `~`、与上游一致），关闭；「精确值不带 `~`」如需实现属有意偏离，需开关+登记。

### 2.14.0 批次（2026-10-01）闭环

- 完成：/add-task 15 字段白名单（t13/t14）、serve Web UI 重设计（t8/t10）、页面 415 修复、202 用例异步 join 竞态修复（疑为 15/16 真根因之一）。

### 3.1.0 批次（2026-10-17）闭环（上游同步大批次）

- 完成：同病修复五件（aria2c 完整性 / token 横幅分流 / grpc-status / live 完整性 / 下载硬化两件 / 工具路径告警）+ BBDownT 功能件六件（--delay-per-item / 字幕双开关 / --audio-language+换轨 / --delay-per-video+--download-all / self-update）；登记 §4.73。
- 候选（按优先级）：① APP/INTL 的 cur_language 重请求；② 多线程续传 + 分片布局校验（独立功能批次，2~3 人日）；③ util.PostResponseWithHeaders 收敛（postGRPC 的 412 文案/64MiB 上限）；④ tokenAuthMode 与 ParseModeName 单一定义；⑤ live 合并产物重复帧上限；⑥ --audio-format 开关（溏心音乐）；⑦ Web 背景三套；⑧ 五期下载并行实现。


### 2.16.0 批次（2026-10-06）闭环（上游同步批次）

- 完成：AI 字幕起死回生（XOR 解码 + 匿名门控移除）、DRM 开箱即用（内置 wvd/mp4decrypt + 自动解密默认开 + 前置检查）、订阅三件（--per-sub-dir/增量扫描/--full-scan）、mp4decrypt --key 修复、v_voucher 识别、argv 值位置 bug。
- 已批准排队（下一工作周期）：① --audio-format 开关（溏心音乐封面兼容）；② Web 背景三套；③ CLI 宽度第二步（标题折行+事件行截断）；
- 候选：--skip-ai 默认值（现 true；AI 字幕已可下且匿名可用——是否改默认 false 待用户）；--ai-subtitle-policy（吸收 BBDownT）；sidecar 双常量统一；「取值标志全在表」自检用例；BBDownT 断点续传/国际站修复评估。


### 2.15.2 批次（2026-10-06）闭环

- 完成：字幕诚实回退（混淆令牌过滤 + 侧车四态）、Web 输入简化（短号/多行批量/文件导入）、CLI 信息行宽度治理第一步（三档 0 超宽）、emoji 宽度区间补全、cli-width-audit.md。
- 遗留/观察：**AI 字幕（ai-zh）生态内无人可下**（B 站混淆令牌，上游与 BBDownT 均无解；若现公开解码方案则跟进）。
- 已批准排队（下一工作周期，用户拍板）：① --audio-format 开关（FLAC 封装原生 .flac，溏心音乐封面兼容）；② Web 背景三套（品牌渐变/四预设/跟随系统，docs/bbdown-ui-bg-v1/v2/v3.png）；③ CLI 宽度第二步（标题行折行 + 事件行截断，实现时登记 UPSTREAM_ALIGNMENT 有意偏离）。
- 候选：--ai-subtitle-policy（吸收 BBDownT SubtitleSelection）；sidecar 双常量统一；debugScreenMaxCols 改 TerminalWidth()；短号+?query 前端接纳。

- 新增遗留（随本批发现）：

### 2.15.1 批次（2026-10-02）闭环（四期）

- 完成：M3U 分P序号（#EXT-BBDOWN-PAGE:N，排序=2.15.0 语义）、testsupport 抽包、aria2c 逐字节进度（双流接泵）、stdin 接缝数据竞争修复（-race 全绿）、下载并行评估文档（定案 B）、汇总字节去重。
- 关闭的旧条目：M3U 回读分P序号缺失、testsupport 抽包、aria2c 无逐字节进度（能力补全）；15/16 观察项**部分关闭**（纯负载 flake 7 轮未复现；抓到并修掉 stdin 竞争这一个真根因）。
- 新增候选：
  1. **下载并行实现**（按 docs/parallel-download-eval.md 表 1/表 2 开工，B 路线 ≈4.5 人日 + 量化 0.5）；
  2. CI/Makefile 加 workflow 包 `-race` 常规跑法（t25/t31 两次建议）；
  3. `--progress-json` 显式速率/字节入口（aria2c 进度进机器面，t24 契约偏差的后续）；
  4. ctx 感知的可取消读（readIntSafe 不泄漏读者协程）；
  5. flake 排查纪律：先冻结工作树（go test -json 记失败用例名，不用全量重跑猜）。

  1. **select_page 1000 上限在展开后判定**：单请求可先让解析器展开 10 万项（受 MaxExpandedPages 约束、约几 MB 瞬时分配）才被拒——优化候选：先按表达式计数再展开；
  2. **renderList 每帧重建列表 DOM**：几十任务无感，几百任务看板需增量更新；
  3. **work_dir 无根目录约束** + applyConfig 的 os.Chdir 进程级副作用（有意设计风险，非回环监听必须带 token，已在 CHANGELOG/A-serve 登记）；
  4. **CLI 观感候选**（docs/bbdown-cli-concepts.png / cli-concept-*.png）：用户选定「分层进度」方向后进三期，须以 --ui 开关形态落地（默认观感契约不动）。

### 待办（代理披露的未覆盖点，如实记录）

- F5：三处 CLI 调用点（runDownload/runWatchLater/runSubCheck 的 applyProgressJSON）删掉不会变红——需要真实网络
  才能端到端跑；已覆盖「旗标注册」与「applyProgressJSON→下载层」两段。
- F6：`sub check`「Fetch 之后调用 subNewAids」的接线无用例（需要假 B 站 API）。
- 顺带量出的**既有异常**：分片完成后 totalBytes.Add(n) 会让终端进度帧瞬时越过总长（3MiB 文件末帧读数
  4,194,304，+33%），JSON 侧已绕开；终端侧是否要夹到 100% 待定（夹住可能掩盖真实计数错误，先记下）。
### O7 serve 并发闸门（`1.6.20-go.5`）

闸门此前内联在 processTask 的 select 里，只有真实网络任务才走得到——「并发不超过上限」这条不变量没有任何
用例守。现抽成 acquireSlot/releaseSlot（行为不变）并加离线回归：

| 指标 | 实测 |
|---|---|
| 6 个任务 / 上限 3 / 每个 100ms | **峰值并发 3**，总时长 **201ms**（理想 200ms） |
| 变异：闸门容量放大到 64（等于没有上限） | 峰值并发 6 > 3 → 断言变红 |
| 取消语义 | 槽占满 + ctx 已取消 → acquireSlot 立刻返回 false（不挂住任务） |

用例：internal/server/concurrency_test.go。判定只用**计数器**（峰值并发），墙钟只作参考日志——慢 runner 上
时间断言会变 flake。第一次变异我写成「直接 return true」，releaseSlot 随即阻塞、触发运行时 deadlock panic：
虽然也红，但不是断言红，**变异本身也要写得忠实**（换成容量放大这版）。
### F1 模板变量对账（2026-09-24，`1.6.20-go.4`）

结论：**上游 `PathHelper` 的 17 个占位符本仓全部可用**——`<videoTitle>`/`<pageNumber>`/
`<pageNumberWithZero>`/`<pageTitle>`/`<bvid>`/`<aid>`/`<cid>`/`<ownerName>`/`<ownerMid>`/`<dfn>`/`<res>`/
`<fps>`/`<videoCodecs>`/`<videoBandwidth>`/`<audioCodecs>`/`<audioBandwidth>`/`<publishDate>`/`<videoDate>`
（含 `<publishDate:自定义格式>`）。所以 F1 不新增占位符——`<upMid>` 之类只是同一件事的第二个名字。

这一项的产出是**回归网**：`internal/download/pathformat_tokens_test.go` 把整张表钉住（此前没有任何用例
守它，「我们有这些占位符」只是读代码的印象）。变异验证：删掉任意一条替换即变红。

### F2 批量输入（2026-09-24，`1.6.20-go.4`）

- 位置参数可给多个目标（**修复**：此前只取 `args[0]`，其余被静默忽略——`BBDown url1 url2` 会悄悄只下第一个）；
- 新增 `--urls-file <path>`（每行一个，`#` 注释与空行跳过，CRLF 容忍，单行上限 1MB；`-` 表示 stdin）；
- 顺序执行，**单个失败不中断其余**，末尾汇总 `N/M 个任务失败`；Ctrl+C 立即停下；更新检查只在开头查一次；
- 单目标时的输出与行为与改前完全一致（逐条失败日志只在多目标时打）。

用例：`internal/cli/urlsfile_test.go`（列表解析、目标汇总、顺序执行语义）。变异验证：遇错即返回、不跳注释，
两处都变红。
### O6 混流阶段进程启动（2026-09-24，`1.6.20-go.3`）

每次混流仍是 **1 次 ffmpeg 进程**（已最小），但顺手查出**两个互相掩盖的杜比视界探测 bug**：

| # | 问题 | 后果 |
|---|---|---|
| 1 | 版本正则字面量把反斜杠全丢了（`libavutils+(d+). +(d+).`） | 永远匹配不到 `libavutil  58.  2.100` → `CheckFFmpegDOVI` **恒 false** → 杜比视界一律回退 mp4box（没装 mp4box 直接混流失败） |
| 2 | 阈值写成 `major >= 5`（上游是 `> 57 或 57.17+`） | 对任何真实 libavutil（55~60）都成立 → **恒 true** |

两个 bug 恰好互相掩盖：修好正则后第二个才会显形（变成「一律用 ffmpeg」）。现按上游规则修正，并把
探测按 ffmpeg 路径缓存——多P 杜比视界此前每 P 探一次进程，现在**进程内每二进制一次**。

用例：`internal/muxer/dovi_probe_test.go`（58.2/57.17 → true，57.16/56.70 → false；三次调用只探一次）。
变异验证：正则退回旧字面量 → 红；阈值退回 `>=5` → 红；去掉缓存 → 红。

### O4 分片大小自适应（2026-09-24，`1.6.20-go.3`）

| 指标 | 改前 | 改后 |
|---|---|---|
| 40 MB 文件、限速服务器（每连接 ≈12.5 MB/s）墙钟 | **1.698 s**（固定 20MB → 3 条连接） | **869 ms**（自动 → 5 条连接），**≈2×** |

规则：显式给 `--thread-segment-size` 时原样生效；**未指定（默认 0）**时按「分片数 ≈ 并发上限」倒推，
下界 1 MB、上界 20 MB。多线程的触发门槛仍是 20 MB（与分片大小解耦）。
用例：`internal/download/segment_plan_test.go`（规划表 + 限速服务器墙钟对比）。

### O3 进度条 CPU（2026-09-24，`1.6.20-go.3`）

单帧成本 **2.4 µs**（`BenchmarkProgressRenderFrame`：算条形 + 格式化 + 加锁 + 写行，stdout 丢弃）。
实时进度条受 16 ms 节流，上限 62.5 帧/秒 → **≈0.015% CPU**；停滞心跳只有 8 帧/秒，更低。
**结论：无需优化**（这也是结果，省得下次再猜）。
### O5 断点续传命中率（2026-09-24，`1.6.20-go.2`）

| 指标 | 改前 | 改后 |
|---|---|---|
| 2 MB 文件中断在 512 KB 后再跑，第二段下载字节 | **2,097,152（整份重下）** | **1,572,864（只补缺口）** |
| 跨进程续传命中（B 站场景：每次解析换签名） | 永不命中 | 命中 |

根因：清单里存的是**完整签名 URL**，而 B 站 URL 每次解析都会刷新 `deadline/sign/trid/upsig` 等参数，
于是「资源身份」每次都对不上 → `.tmp` 被判不可信后删除重下。上游 v1.6.20 用 `StableResourceIdentity`
（剥离会刷新的 query 参数）比较，本仓此前没有跟上——这一条属于**对齐**，不是偏离。

顺带对齐两点：总长必须一致才算同一资源；ETag/Last-Modified 只在**双方都有**时才比较（有些 CDN 不返回）。
用例：`internal/download/resume_identity_test.go`（身份剥离表 + 7 条匹配表 + 端到端字节数）。

### O2 解析阶段请求数（2026-09-24，`1.6.20-go.1`）

| 指标 | 改前 | 改后 |
|---|---|---|
| 单次 `-I` 解析的 HTTP 请求数 | 5（playurl×2、view、nav、player/wbi/v2） | **3**（playurl、view、nav） |
| 同一视频墙钟（3 次取样） | 0.53 s | **0.27 ~ 0.32 s** |
| `-I` 输出（去掉每次都变的 URL 行） | 32 行 | **32 行，逐行完全一致** |

两处改动：① 解析改为「qn=127 优先，失败或不带 `dash.video` 时回落 qn=0」（上游是 qn=0 先发再重发，
重发带 dash 就整份取代前者——常见情况下第一份白发）；② `-I` 只打印流信息，跳过章节抓取
（`player/wbi/v2`）。FLV/durl 稿件没有 dash 轨道，仍会回落一次（与上游同为 2 次请求，省不下）。

差异登记见 `docs/UPSTREAM_ALIGNMENT.md` §4.35；用例：`internal/parser/playurl_single_pass_test.go`、
`internal/workflow/onlyshowinfo_chapters_test.go`，另同步更新了钉住旧请求顺序的三条 fixture 用例。


## 怎么用

- 你勾选（或直接说「做 O2」），我按上面的「怎么量化」先测基线再动手；
- 首轮建议 **O2**：请求数是解析阶段最直观的浪费来源，且完全可量化（同一视频、同一网络、同一账号）。
