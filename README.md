# BBDown

[![CI](https://github.com/QC3284/BBDown-Go/actions/workflows/ci.yml/badge.svg)](https://github.com/QC3284/BBDown-Go/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/QC3284/BBDown-Go)](https://github.com/QC3284/BBDown-Go/releases/latest)
[![Go](https://img.shields.io/github/go-mod/go-version/QC3284/BBDown-Go)](go.mod)
[![License](https://img.shields.io/github/license/QC3284/BBDown-Go)](LICENSE)

命令行式哔哩哔哩下载器。Bilibili Downloader. 当前版本 **3.0.0**。

> **本仓是 BBDown 生态的 Go 主线实现**：起步自 C# 版 [aliveranme/BBDown](https://github.com/aliveranme/BBDown) v1.6.20 的重写，此后独立演进，版本号走自己的线。
> 规格来源共四个：上游 `AliverAnme/BBDown`（已到 **1.7.3**）、C# 2.x 接手线 [BBDownT](https://github.com/LOVAHE/BBDownT)（已到 **2.1.7**）、[bilibili-API-collect](https://github.com/SocialSisterYi/bilibili-API-collect)、以及**实测**（冲突时以实测为准）。
> 与上游不同的行为逐条登记在 [docs/UPSTREAM_ALIGNMENT.md](docs/UPSTREAM_ALIGNMENT.md) §4（区分「有意偏离」与「尚未对齐」），变更说明见 [CHANGELOG.md](CHANGELOG.md)，候选清单见 [docs/ROADMAP.md](docs/ROADMAP.md)。Go 重写由 AI 辅助完成。

## 安装

### Arch Linux（AUR 未发布，用仓库自带 PKGBUILD）

```bash
git clone https://github.com/QC3284/BBDown-Go.git -b main && cd BBDown-Go
makepkg -si        # 包名 BBDown-Go-git；pkgver() 取 git describe，本地构建会带 .r<提交数>.<短哈希>
```

### 手动编译

依赖：Go 1.26+、ffmpeg（混流/直播合成；也可用 mp4box 混流）。DRM 解密所需的外部件**发布包已内置**（见「功能」）。

```bash
git clone https://github.com/QC3284/BBDown-Go.git -b main && cd BBDown-Go
make build                     # 产物在 bin/BBDown
sudo cp bin/BBDown /usr/bin/
```

## 快速开始

```bash
BBDown https://www.bilibili.com/video/BV1xx411c7mD   # 基本下载（短号也行：BV1xx411c7mD / av2 / ep123 / ss456）
BBDown -e "avc,hevc" -q "1080P 高码率" BV1xx411c7mD # 编码/画质优先级
BBDown -p 1,3-5 BV1xx411c7mD                        # 选分P；-i 交互式渐进选档；-I 只看信息
BBDown --audio-only BV1xx411c7mD                    # 仅音频；--sub-only / --cover-only / --danmaku-only 同理
BBDown --nfo --progress-json BV1xx411c7mD           # 写 .nfo 侧车 + 逐行 JSON 进度（媒体库/监控）
BBDown --write-m3u BV1xx411c7mD                     # 产物旁写 .m3u 播放列表（多P 按分P顺序）
BBDown --urls-file urls.txt                         # 批量：每行一个，# 注释，- 表示 stdin；位置参数也可给多个
BBDown login && BBDown doctor                       # 扫码登录；一条命令自检外部件/输出目录/接口与登录态
BBDown sub add mid:123456 --name 某UP && BBDown sub check --since 24h --concurrency 4
BBDown serve                                        # HTTP API + 内嵌 Web UI（http://127.0.0.1:23333/）
BBDown live 12345 && BBDown article cv123 && BBDown watchlater
```

## 功能

**解析**
- 普通视频 / 番剧 / 课程 / 合集 / 系列 / 收藏夹 / UP 主全部投稿（完整分页）；四种解析模式：WEB（WBI 签名）/ APP（gRPC protobuf）/ TV / 国际版。
- 最高 8K / HDR / 杜比视界 / 杜比全景声；杜比视界在 ffmpeg<5.0 时自动切换 mp4box 混流。
- `-i` 渐进式分层选档（先选档位 → 再选流；★ 标推荐档、`--compat` 避开 HDR Vivid/杜比视界），零二次请求。

**下载**
- 多线程下载（并发上限 8、Range 校验、分片重试、自动分片大小）+ aria2c；断点续传（`.tmp` + 资源身份清单，ETag/Last-Modified 校验）。
- aria2c **逐字节进度**：解析 aria2c 自己的 `--summary-interval` 摘要块（本仓在观察者路径上自动加上该参数），经进度观察者管线发帧——serve 的 SSE 与 Web UI 因此也能看到 aria2c 的真实字节；收尾汇总的累计字节按产物路径去重（分片重试/断点续传的重复上报只计一次）。
- 低画质 FLV 流分段下载与合并；**下载并行/串行**语义与终端渲染边界见 [docs/parallel-download-eval.md](docs/parallel-download-eval.md)。

**产物与侧车**
- 自动混流（ffmpeg / mp4box，含章节、封面、多音轨、`creation_time` 元数据）；背景音轨与角色配音下载（番剧 `dubbing_info`）。
- 弹幕（XML / ASS + 关键词与用户过滤）、字幕（多 API 回退、**146 项**语言表（`internal/util/sublang.go` 的 `subtitleLangMap`）、侧车**四态**不谎报）、章节信息（`view_points`）。
- **AI 字幕开箱可下**：字幕地址是「百分号编码 + XOR」双重混淆，解码后还原真实 CDN 地址；**匿名（未登录）同样可下**，`--skip-ai` 默认关闭即**默认下载 AI 字幕**。
- `--nfo` 侧车元数据（Kodi/Emby/Jellyfin 可读）、`--write-m3u` 播放列表（每条目写自研注释 `#EXT-BBDOWN-PAGE:N`，回读恢复分P序号）、评论区导出（`.comments.json`）、专栏 Markdown、稍后再看。
- **DRM 开箱即用**：发布包内置 `device.wvd` 与 `mp4decrypt`（构建时固定版本/SHA256）；DRM 自动检测与解密**默认开启**（`--no-decrypt-drm` 关闭，`--decrypt-drm` 保留兼容），**下载前**检查外部件齐备（缺件立刻给可操作指引，不再下完才失败），`doctor` 给三态检查。

**订阅与服务**
- `sub check` 订阅调度：`--since <duration>` 增量窗口（Go duration，不含 `d`）、`--concurrency <1-8>` 并行检查（下载仍串行）、`--per-sub-dir` 按订阅分目录、mid: 订阅**增量扫描默认开启**（整页已下载即停止翻页，`--full-scan` 关闭）。
- `serve` HTTP API + **内嵌单文件 Web UI**（无框架/无 CDN，≤64KiB）：任务列表 + 详情卡（进度/统计/SVG 速率曲线/事件流）+ 新增任务表单（多行批量：完整链接与 **BV/av/ep/ss 短号**混写、空行与 `#` 注释跳过、`.txt` 拖拽/选择导入、逐条结果点名）+ SSE 实时进度。
- 直播录制（分段 + 断流自动重连 + 指数退避 + ffmpeg 合成）、二维码登录（WEB/TV，凭据脱敏）、逐行参数配置文件。

**风控与健壮性**
- **v_voucher 人机验证识别**：playurl 返回「HTTP 200 + code=0 + 无轨道」的验证凭证时给出可读错误 + 处置建议，并纳入页面级重试（不再静默零轨道）。
- `HTTP 412` 追加可操作提示；镜像 404 自动回退原地址；argv 中以 `-` 开头的值按 GNU 语义正确取用。

**观感与集成**
- CLI 观感（BBDownT 风格，有意偏离上游，§4.66）：流表 `0. [1080P 高码率] [1920x1080] [HEVC] [60] [~4999 kbps] [246.90 MB]`（kbps 由体积反推）、按终端宽度自适应列；信息行含分P清单/字幕清单/⚠ 杜比视界标记；进度区含分P游标 `P1/8`、续传提示、侧车状态行；收尾汇总含平均速率；信息行与 `--debug` 长行按显示宽度夹取（CJK 计 2 列，窄终端自动收回缩进，完整内容仍进日志文件）。
- **机器契约逐字节不动**：`-I` 直链、`--print-urls`、`--info-json`、`--progress-json`（逐行 JSON 事件写 stderr：`{percent, downloaded, total, speed, state}`）与管道输出都是被用例钉住的稳定接口，脚本/GUI 可依赖。
- `doctor` 自检（外部工具 / 输出目录 / 接口 / 登录态）；启动时检查新版本。

## 子命令

| 命令 | 说明 |
|---|---|
| `login` / `logintv` | APP 扫描二维码登录 WEB / TV 账号 |
| `serve` | HTTP API + 内置 Web UI（非回环监听必须配 `--serve-token`） |
| `live` | 录制直播流（断流自动重连、分段合成，支持 `-o`） |
| `article` | 下载专栏文章为 Markdown（支持 `-o`） |
| `watchlater` | 下载稍后再看列表（`--limit`，需登录） |
| `resume` | 重试未完成的任务（记录在工作目录的 `.bbdown-pending.json`） |
| `sub` | 订阅管理：`add`（`--name`/`--filter`）/ `list` / `remove` / `check`（`--since`/`--concurrency`/`--per-sub-dir`/`--full-scan`） |
| `doctor` | 自检外部工具、输出目录、接口与登录态 |
| `completion` | 生成 shell 补全脚本（cobra 内置） |

## 主要选项

| 选项 | 说明 |
|---|---|
| `-t/-a/--use-intl-api` | TV / APP / 国际版解析模式 |
| `-e/-q` | 编码优先级（`--encoding-priority`）/ 画质优先级（`--dfn-priority`） |
| `-p, --select-page` | 选分P：`-p 1,3,5`、`-p 3-5`、`-p LAST` |
| `-i/-I` | 交互式选档 / 仅解析不下载 |
| `-F/-M` | 单P / 多P 文件名模板（`--file-pattern` / `--multi-file-pattern`） |
| `--video-only` `--audio-only` `--sub-only` `--cover-only` `--danmaku-only` | 只取其中一类产物 |
| `--skip-ai` | 跳过 AI 字幕（**默认下载**，与 BBDownT 默认一致） |
| `--skip-mux` `--simply-mux` `--use-mp4box` | 混流控制 |
| `--nfo` `--write-m3u` `--compat` | 侧车元数据 / 播放列表 / 兼容优先选档 |
| `--comments` `--download-danmaku` `--danmaku-filter` | 评论区导出 / 弹幕与过滤 |
| `--multi-thread` `--thread-segment-size` `--use-aria2c` `--aria2c-path` | 多线程与 aria2c（`--multi-thread false` 关闭） |
| `--force-http` `--force-replace-host` `--upos-host` `--allow-pcdn` | host/镜像相关（`--force-replace-host` 默认开） |
| `--urls-file` | 批量目标（每行一个，`#` 注释，`-` 表示 stdin） |
| `--progress-json` | 逐行 JSON 进度到 stderr |
| `--decrypt-drm` `--no-decrypt-drm` `--wvd-path` `--mp4decrypt-path` | DRM：自动检测解密默认开 / 关闭 / 自定义外部件 |
| `-c, --cookie` `--access-token` `-u, --user-agent` | 凭据与请求头 |
| `--work-dir` `--config-file` `--log-file` `--debug` | 工作目录 / 配置文件 / 日志 / 调试 |
| `--retry-count` `--retry-delay` `--save-archives-to-file` | 重试策略 / 已下载 aid 记录 |

`serve`：`-l/--listen`（默认 `http://127.0.0.1:23333`）、`--max-concurrent`（默认 3）、`--serve-token`、`--trusted-proxy`、`--notify-webhook`。
完整选项与默认值以 `bin/BBDown --help`、`bin/BBDown <子命令> --help` 为准（本文档不重复 help 里没有的旗标）。

## 配置文件

默认取可执行文件同目录的 `BBDown.config`（`--config-file` 可指定），逐行参数文本，与命令行同语法：

```text
# 注释以 # 开头
--encoding-priority
hevc,avc
--skip-cover
https://www.bilibili.com/video/BV1xx411c7mD
```

- 命令行显式给出的选项优先；配置中的 URL 仅在命令行未给目标时生效。
- 子命令（`login`/`serve`/`live`/`article`/`watchlater`/`sub`）不读取配置文件。

## API 服务器（`serve`）

| 端点 | 方法 | 说明 |
|---|---|---|
| `/` | GET | 内嵌 Web UI 单页（无外部资源） |
| `/events` | GET | SSE 实时事件流（`task_start`/`task_progress`/`task_done`/`task_failed`；`?token=` 或 `X-Serve-Token`） |
| `/get-tasks`（`/running`、`/finished`、`/{id}`） | GET | 任务列表与详情（`JobId/Aid/Url/Status/Progress/TotalDownloadedBytes/SavePaths/...`） |
| `/add-task` | POST | 提交任务，202 + `{"TaskId":"..."}` |
| `/cancel/{id}` | POST | 取消排队/进行中的任务 |
| `/remove-finished[/failed|/{id}]` | DELETE | 清空 / 清除失败项 / 清除单个 |
| `/health` | GET | 健康检查（按设计无需 token） |

`/add-task` 是**严格 15 字段白名单**（snake_case）：`url` `select_page` `dfn_priority` `encoding_priority` `multi_thread` `overwrite` `skip_mux` `skip_ai` `write_nfo` `compat` `use_app_api` `use_tv_api` `use_intl_api` `work_dir` `language`——
未知字段或非法值一律 400 并点名字段；`bool` 用指针区分「未传」与 `false`（与默认相同即不传）；`select_page` 复用 CLI 解析器并有 1000 项上限；危险字段（`interactive`/`file_pattern`/`cookie`/`notify_webhook` 等）不开放；只发 `url` 的老客户端行为不变。
`Progress` 保持 0~1 边界语义（执行期 0、成功 1.0）；`TotalDownloadedBytes` 在下载执行期间与 SSE 的真实字节同口径（多产物任务按文件身份求和）。
完成列表持久化到 `bbdown-tasks.json`（保留 30 天 / 最近 1000 条）；`--notify-webhook` 仅接受公网地址；`Cache-Control: no-store`，写端点要求 `Content-Type: application/json` 且 `Origin`（若带）必须与请求的 scheme+host+port 同源（CSRF 闸门）。

## 测试与 CI

```bash
make test        # go test ./...；提交前另跑 gofmt -l internal/ cmd/ && go build ./... && go vet ./...
```

推送与 PR 触发 GitHub Actions（build/vet/test/gofmt，覆盖 linux/windows/macos）。

## 与上游的关系

基于 [AliverAnme/BBDown](https://github.com/aliveranme/BBDown)（C# v1.6.20）重写，此后**独立演进**：上游已到 **1.7.3**、BBDownT 已到 **2.1.7**，本仓为 **3.0.0**（3.0 起仓库与 module 路径更名为 BBDown-Go，二进制仍为 BBDown），按「四源同步」机制跟进（并入评估 → 补用例与变异验证 → 登记差异 → 记 CHANGELOG），**版本号只随自己的发布前移**。
逐条差异（含「有意偏离」与「尚未对齐」）见 [docs/UPSTREAM_ALIGNMENT.md](docs/UPSTREAM_ALIGNMENT.md) §4；近期吸收：上游 1.6.21~1.7.3（订阅增量扫描 / `--per-sub-dir` / DRM 内置与参数修复 / v_voucher 风控）与 BBDownT 2.1.7（AI 字幕混淆解码）。

- **进度条实时化**：重绘由数据到达驱动（16ms 节流 + 停滞 125ms 心跳），上游是 1/8 秒定时器（§4.31）。
- **`--work-dir` 是根命令持久标志**：所有子命令可用（上游只有部分命令声明）。
- **CLI 观感对齐 BBDownT**：`[值]` 流表、青色标题、信息行/进度区扩展、体积估算除数 1024→1000（§4.66，有意偏离上游 `Display.cs`）。
- **失败输出**：运行期失败只打错误 + 升级提示（红底白字），只有用法错误才附帮助（§4.32.1）；`HTTP 412` 追加可操作提示（§4.33）。
- **镜像 404 回退原地址**；解析阶段「qn=127 优先」少发一次 playurl（实测 5→3 请求、0.53s→0.27~0.32s，§4.35）。
- **订阅调度**（`--since`/`--concurrency`/`--per-sub-dir`）与 **serve 进度口径统一**为自研，上游无对应实现（§4.64）。

| 分支 | 内容 |
|---|---|
| `main` | Go 重写（当前分支） |
| `master` | C# 原版快照 |

## 常见问题

### 看到 `HTTP 412` 或「请求被拦截」

B 站风控（也可能 WAF 直接 412），与下载器无关：同账号/IP 高频请求会触发。本仓的重试**按错误分类**，不是「4xx 一律不重试」：

- **412**：**参与 HTTP 层重试**——退避 `1s → 2s → 4s …`（上限 8s），且**只在成功轮换自动 UA 之后**才重试（API 层最多 3 次尝试）。带同一个 UA 空转只会加重风控，所以显式 `--user-agent` 换不掉 UA 时直接抛出并给可操作提示（有意偏离上游，§4.40）。
- **其余 4xx（412/404 之外）**：确定性失败，**不重试**（换地址或重试都是白烧预算）。
- **404**：有候选地址时**立刻切换候选**（不退避）；没有候选时走既有阶梯退避 `(已失败次数+1) × --retry-delay`。
- **页面级解析失败**：最多 **3 次**尝试，每次等待 `--retry-delay`（默认 `3s`）。5xx / 网络传输失败 / 其它错误沿用阶梯退避（网络失败且无候选时用 ≤500ms 短退避，`--retry-delay` 更短则按用户的来）。

处理办法：等几分钟到几十分钟再试、降低下载频率、确认 `--cookie` 是当前账号且未过期，必要时更换网络出口。风控以「HTTP 200 + HTML 页面」返回时会直接报「疑似风控页」。

### 出现「v_voucher 人机验证」怎么办

`v_voucher` 是风控要求人机验证的凭证，形态是 **HTTP 200 + `code=0` 但没有任何轨道**（不是报错）。本仓会明确报出这是验证凭证并给处置建议，同时把它纳入页面级重试；处理方式与前一条相同（等待 + 换出口 + 确认登录态），也可以用 `-a`/`-t` 换个解析接口再试。

### AI 字幕需要登录吗？

**不需要**。AI 字幕（`ai-zh` 等）的地址是「百分号编码 + XOR」混淆，本仓解码后即可匿名下载；`--skip-ai` 默认关闭，即**默认下载 AI 字幕**（`--skip-ai` 跳过）。若确实没有可用字幕，会提示「未找到可用字幕：可能需要登录（bbdown login）、选择别的语言（AI 字幕请加 --skip-ai=false），或该视频没有字幕」，而不是谎报成功。

### DRM 解密报缺件（没有 Bento4 / 没有 device.wvd）

发布包**内置** `device.wvd` 与 `mp4decrypt`，开箱即用；自定义安装（手动编译、自备外部件）时本仓会在**下载之前**检查并给指引：`--wvd-path` 指定 device.wvd、`--mp4decrypt-path` 指定 mp4decrypt（Bento4 版 mp4decrypt 即可），`doctor` 有三态检查。不需要解密时用 `--no-decrypt-drm` 关闭（解析不带 `drm_tech_type=2`，产物保持加密格式）。

### linux-arm64 上为什么没有内置 mp4decrypt

Bento4 官方对 linux-arm64 没有预编译包，因此该平台的发布包只内置 `device.wvd`，mp4decrypt 需自行安装或用 `--mp4decrypt-path` 指定；缺件时会在下载前给出同样的指引。

### 下载报 `HTTP 404`，尤其是刚看到「强制替换…镜像」之后

`--force-replace-host` 默认开启，会把每条流 host 换成镜像 `upos-sz-mirrorcoso1.bilivideo.com`，镜像不保证覆盖全部对象，命中缺口即 404。加 `--debug` 看 `Start downloading: <脱敏 URL>` 判断失败在镜像还是原站；绕开用 `--force-replace-host false` 或 `--upos-host <另一个镜像>`。

### 运行失败时为什么只打印一行错误

上游只在**参数解析失败**时打印帮助；运行期异常走异常处理器，只给消息加一句升级提示。本仓对齐这一行为（§4.32.1）。

## 注意事项及警告

- 本软件仅供学习交流，**请勿用于商业用途或传播下载内容**；下载受版权保护的内容可能构成侵权，请仅下载您拥有合法权限的内容。
- 使用本软件下载视频时，请遵守哔哩哔哩 [用户协议](https://www.bilibili.com/protocal/licence.html) 及相关法律法规；**禁止用于任何违法用途**，使用者自行承担一切法律后果。
- **本仓独立演进**：会主动增加优化与新功能，并跟踪生态里的其他实现（如 BBDownT 的风控与字幕改动），差异逐条登记在「与上游的关系」与 `docs/UPSTREAM_ALIGNMENT.md`。
- 使用 `--cookie` / `--access-token` 时凭据以明文存储于本地文件（`BBDown.data` / `BBDownTV.data` / `BBDownApp.data`），请注意保管；`--debug` 生成的 `debug_*.json` 含接口响应原文。
- ffmpeg / mp4box / aria2c / mp4decrypt 等外部程序需自行安装（发布包内置的除外），本软件不为其行为负责；Widevine 解密请自行确保符合当地法律，勿传播密钥或解密产物。
- 本软件不校验下载内容的完整性与安全性；第三方下载工具可能违反平台服务条款，请自行评估。

### 免责声明

- 本软件按「现状」提供，不作任何担保；哔哩哔哩接口可能随时调整，不保证始终可用。
- 使用本软件（含登录、下载、DRM 解密）产生的任何直接或间接后果——账号被限制或封禁、数据丢失、设备损坏、法律纠纷——均由使用者自行承担，作者不承担任何责任。
- 本软件为非官方工具，与哔哩哔哩公司无任何关联，亦未获得其授权或认可；下载内容由第三方提供，其合法性、安全性与适宜性请自行判断。

## License

本项目（Go 重写）以 MIT 协议发布，完整文本见 [LICENSE](LICENSE)。版权声明：

- © 2020 [nilaoda](https://github.com/nilaoda)（原作者）
- © 2025 [AliverAnme](https://github.com/aliveranme)（上游维护者）
- © 2026 [QC3284](https://github.com/QC3284)（Go 重写）

所链接的第三方库均采用宽松许可证（SPDX：MIT / Apache-2.0 / BSD-3-Clause），无 GPL 系链接依赖；发布包**另外分发** GPL-2.0 的 Bento4（mp4decrypt）与专有的 device.wvd，来源与许可见 THIRD-PARTY-NOTICES.md。
依赖清单与再分发要求见 [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md)，各许可证逐字原文见 [LICENSES/](LICENSES/) 目录。

## 相关文档

[CHANGELOG.md](CHANGELOG.md)（逐版本行为）· [docs/UPSTREAM_ALIGNMENT.md](docs/UPSTREAM_ALIGNMENT.md)（差异表 §4）· [docs/ROADMAP.md](docs/ROADMAP.md)（候选清单）· [docs/HANDOVER.md](docs/HANDOVER.md)（交接现状）
