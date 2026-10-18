package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QC3284/BBDown-Go/internal/config"
	"github.com/QC3284/BBDown-Go/internal/entity"
	"github.com/QC3284/BBDown-Go/internal/fetcher"
	"github.com/QC3284/BBDown-Go/internal/util"
	"github.com/QC3284/BBDown-Go/internal/workflow"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

var (
	debug bool

	// Global option flags (mapped to MyOption fields)
	optCookie     string
	optToken      string
	optHost       string
	optEpHost     string
	optTvHost     string
	optArea       string
	optUserAgent  string
	optWorkDir    string
	optFFmpegPath string
	optMp4boxPath string
	optAria2cPath string
	optConfigFile string
	optInsecure   bool

	// Download options
	optURL              string
	optUseTvAPI         bool
	optUseAppAPI        bool
	optUseIntlAPI       bool
	optUseMP4box        bool
	optEncodingPriority string
	optDfnPriority      string
	optOnlyShowInfo     bool
	optShowAll          bool
	optUseAria2c        bool
	optInteractive      bool
	optHideStreams      bool
	optMultiThread      bool
	optSimplyMux        bool
	optVideoOnly        bool
	optAudioOnly        bool
	optDanmakuOnly      bool
	optCoverOnly        bool
	optSubOnly          bool
	optSkipMux          bool
	optDecryptDrm       bool
	// t56：DRM 自动解密默认开启，--no-decrypt-drm 显式关闭（负向开关优先，见 decryptDrmEnabled）。
	optNoDecryptDrm       bool
	optAllowPreview       bool
	optDrmKeyHex          string
	optDrmKidHex          string
	optMp4decryptPath     string
	optWvdPath            string
	optSkipSubtitle       bool
	optSkipCover          bool
	optOverwrite          bool
	optPrintURLs          bool
	optNFO                bool
	optCompat             bool
	optLogFile            string
	optM3U                bool
	optInfoJSON           bool
	optForceHTTP          bool
	optAria2cProxy        string
	optAddDfnSuffix       bool
	optOnlyHevc           bool
	optOnlyAvc            bool
	optOnlyAv1            bool
	optNoPaddingPageNum   bool
	optBandwidthAscending bool
	optDownloadDanmaku    bool
	optDanmakuFormats     string
	optDanmakuFilter      string
	optDanmakuFilterUser  string
	optDownloadComments   bool
	optNotifyWebhook      string
	optSkipAi             bool
	// 字幕筛选（t78，吸收 BBDownT SubtitleSelection）：--subtitle-language 语言筛选 +
	// --ai-subtitle-policy AI 字幕策略（显式给出时优先于 --skip-ai）。
	optSubtitleLanguage string
	optAISubtitlePolicy string
	optAudioLanguage    string
	optVideoAscending   bool
	optAudioAscending   bool
	optAllowPcdn        bool
	optFilePattern      string
	optMultiFilePattern string
	optSelectPage       string
	optLanguage         string
	optAria2cArgs       string
	optUposHost         string
	optForceReplaceHost bool
	optSaveArchives     bool
	optDelayPerPage     int
	optMuxerTimeout     int
	optRetryCount       int
	optRetryDelay       int
	optURLsFile         string
	// 批量下载三件（t80，吸收 BBDownT）：视频之间的间隔 + 空间全量清单下载。
	// --save-archives-to-file 早已存在（optSaveArchives，见 buildMyOption），这里不重复声明。
	optDelayPerVideo     int
	optDownloadAll       bool
	optThreadSegmentSize int
	optProgressJSON      bool

	// Serve options
	optServeListen        string
	optServeMaxConcurrent int
	optServeToken         string
	optTrustedProxy       string

	// Subcommand options
	optLiveOutput      string
	optArticleOutput   string
	optSubName         string
	optSubFilter       string
	optWatchLaterLimit int

	// sub check 的调度参数（本期新增）：--since 增量窗口 + --concurrency 并发检查。
	optSubCheckSince       string
	optSubCheckConcurrency int
	// 订阅增强（t47，吸收上游 1.6.21/1.6.22）：--per-sub-dir 按订阅分目录 + --full-scan 关掉
	// 「整页都已下载就停止翻页」的提前结束。
	optSubCheckPerSubDir bool
	optSubCheckFullScan  bool
	// 订阅节流（t71，吸收上游 1.7.7）：sub check 此前完全没有节流，首检大 UP 会把所有新增
	// 稿件连续下载，把账号推进风控窗口（412/-352/v_voucher）。--delay-per-item 让每个工作单元
	// 之间停顿 N 秒，把压力摊开。
	optSubCheckDelayPerItem int
)

// rootCmd represents the base command.
var rootCmd = &cobra.Command{
	Use:   "BBDown [URL]",
	Short: "BBDown - Bilibili Downloader",
	Long: `BBDown —— 命令行哔哩哔哩下载器（本仓为 BBDown 生态的 Go 主线实现）。
支持普通视频、番剧、课程、合集、收藏夹、个人空间、直播与图文。

常用示例:
  BBDown BV1xx411c7mD                     下载（自动挑最高清晰度）
  BBDown -I BV1xx411c7mD                  只列流信息，不下载
  BBDown --audio-only --skip-mux <URL>    只下音频（保留原始轨道）
  BBDown --print-urls <URL>               只打印所选流直链，不下载（接 aria2c/脚本）
  BBDown --urls-file list.txt             批量下载（每行一个，# 注释，- 表示 stdin）
  BBDown --nfo --progress-json <URL>      写侧车元数据 + 逐行 JSON 进度（媒体库/监控）
  BBDown --compat <URL>                   兼容优先：避开 HDR Vivid/杜比视界档位
  BBDown --write-m3u <URL>                产物旁写 .m3u 播放列表（多P 按分P顺序）
  BBDown --info-json <URL>                只输出解析结果 JSON 元数据（给脚本/GUI/调度器）
  BBDown --overwrite <URL>                忽略已存在产物，强制重下
  BBDown doctor                           环境自检（混流工具/输出目录/登录态/风控）
  BBDown resume                           重试上次未完成的任务
  BBDown login                            扫码登录（高清与字幕需要）

完整选项见 BBDown --help；与上游的行为差异见仓库 docs/UPSTREAM_ALIGNMENT.md。`,
	Version: "3.1.1",
	Args:    cobra.ArbitraryArgs,
	RunE:    runDownload,

	SilenceUsage:  true,
	SilenceErrors: true,

	// 上游 Spectre.Console.Cli 只为「参数解析失败」打印帮助文本，运行期异常走
	// SetExceptionHandler——只打异常消息。cobra 默认对任何错误都打 usage 与
	// "Error: xxx"，这里全部关掉，改由 Execute/reportError 按上游语义分类打印。
}

func init() {
	// 未知标志、标志取值非法都走这里：标记成用法错误，这是唯一需要附带 usage 的一类。
	rootCmd.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return usageError{err} })
}

// Execute adds all child commands and runs root.
//
// banner 是人看模式的开场横幅（版本号的事实来源仍在 cmd/bbdown/main.go，由 main 传进来）；
// 机读模式（--info-json / doctor --json）不打印它——stdout 只留数据。
func Execute(banner string) {
	// 任务 G-1：Ctrl+C 的收尾（首次优雅取消 + 提示 / 二次强制退出 130 / 退出前恢复终端）
	// 在这里统一安装，所有子命令共用同一份计数——此前只有下载路径（downloadTargets）装了，
	// 其余子命令用 signal.NotifyContext，第二次 Ctrl+C 被信号层吞掉。安装是幂等的（见
	// installInterrupts）：子命令里再取只会复用，不会把计数打乱成「按一次就强退」。
	stopInterrupts := installRootInterrupts()
	defer stopInterrupts()

	// --log-file 要在任何命令真正干活之前生效：放在根命令的 PersistentPreRun（cobra 在所有命令前调用它）。
	rootCmd.PersistentPreRun = func(cmd *cobra.Command, args []string) {
		if optLogFile != "" {
			util.SetLogFile(optLogFile)
			util.LogDebug("日志同时写入 %s", optLogFile)
		}
	}
	util.SetDefaultDebugFn(func() bool { return debug })

	// Normalize legacy single-dash aliases (upstream NormalizeCliArgs), then
	// merge BBDown.config (line-based args) as option defaults.
	effective, configLoaded, mergeErr := preprocessArgs(os.Args[1:])

	// 机读模式（--info-json / doctor --json）的判定必须在**任何输出之前**完成，所以看的是
	// 「本次真正会跑的」参数（含 BBDown.config 带进来的开关），而不是等 cobra 解析完。
	// 横幅仍要排在配置加载那行日志之前（与改前同序），所以先定模式、再输出。
	restoreOutput := prepareConsoleOutput(machineReadableArgs(effective), banner)
	defer restoreOutput()

	if mergeErr == nil {
		rootCmd.SetArgs(effective)
		if configLoaded {
			util.Log("加载配置文件完成（配置为默认值，命令行参数优先）")
		}
	}

	if err := rootCmd.Execute(); err != nil {
		// 退出码由纯函数决定（可单测）：取消/中断 → 130，其它错误 → 1。
		code := exitCodeFor(err)
		if code == interruptExitCode {
			util.LogWarn("已取消")
		} else {
			reportError(err, os.Stderr)
		}
		os.Exit(code)
	}
}

// errInterrupted 标记「用户中断导致的中止」（Ctrl+C 的优雅取消路径）。
var errInterrupted = errors.New("interrupted")

// exitCodeFor 决定进程退出码：取消/中断 → 130（128+SIGINT，同上游 Environment.Exit(130)），其它错误 → 1。
//
// 抽成纯函数是为了能直接单测（os.Exit 在用例里观察不到）。此前取消是退出 0，与上游不一致；
// 本仓 docs/alignment 把这条列为待对齐项，本次补齐。

func exitCodeFor(err error) int {
	if err == nil {
		return 0
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, errInterrupted) {
		return interruptExitCode
	}
	return 1
}

// usageError 标记「参数/用法错误」：只有这类错误才连带打印 usage。
//
// 上游用 Spectre.Console.Cli：解析参数失败时给帮助文本，运行期异常走
// SetExceptionHandler——只打印异常消息与一句升级提示，绝不打印帮助。
type usageError struct{ err error }

func (e usageError) Error() string { return e.err.Error() }

func (e usageError) Unwrap() error { return e.err }

// usageArgs 把 cobra 的参数校验器包成 usageError：参数个数不对与标志解析失败同属用法错误。
func usageArgs(v cobra.PositionalArgs) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if err := v(cmd, args); err != nil {
			return usageError{err}
		}
		return nil
	}
}

// 失败块的行首标签：两个字 + 全角冒号，建议与命令的内容因此从同一列开始（有序、好扫）。
const (
	errorAdviceLabel  = "提示："
	errorCommandLabel = "命令："
)

// upgradeHint 是上游 SetExceptionHandler 的固定句：只在错误无法归类时保留。
const upgradeHint = "请尝试升级到最新版本后重试!"

// adviceInput 是「参数/输入」类的建议：用法错误与运行期输入错误共用同一条文案，
// 「每类错误一种说法」靠共用常量钉住，而不是靠两处手写保持一致。
const adviceInput = "检查下载目标与参数写法：URL、BV/av 号、分P 选择器"

// reportError 按上游 SetExceptionHandler 的语义打印失败块：红底标题行（原因）→ 建议行 →
// 可执行命令（给不出就省略第三行）。只有用法错误才附 usage（Spectre 在解析失败时打印帮助
// 文本）。失败细节（堆栈）不进终端——上游同样只把它写进日志文件。
//
// 三段有序是有意的：改前是「原因 + 一句固定升级提示」，无论 412、断网还是目录不可写，用户
// 拿到的下一步都一样。现在第二行按错误类型给建议、第三行给可以直接复制的命令，固定句退回
// 兜底位置（只在无法归类时出现）。
func reportError(err error, w io.Writer) {
	// 上游 SetExceptionHandler 把「异常那一行」设成 ConsoleColor.Red 底 + White 字（都是亮色档，
	// 对应 ANSI 101/97）——它是给用户的行动指引，不该淹没在普通日志里。只有原因行保留红底：
	// 建议与命令是正文，整块刷红反而看不清。
	fmt.Fprintln(w, util.AnsiBgRed+util.AnsiWhite+err.Error()+util.AnsiReset)
	advice, command := errorAdvice(err)
	fmt.Fprintln(w, errorAdviceLabel+advice)
	if command != "" {
		fmt.Fprintln(w, errorCommandLabel+command)
	}
	var ue usageError
	if errors.As(err, &ue) {
		fmt.Fprintln(w, rootCmd.UsageString())
	}
}

// errorAdvice 按错误给出「下一步做什么」：建议（第 2 行）+ 可执行命令（第 3 行，可为空）。
//
// 失败来自解析/下载/混流多层，都是 fmt.Errorf 拼出来的文本，没有统一的错误类型可判；这里按
// 错误文本里的确定性标记归类，判断顺序（风控 → 读写权限 → 网络 → 参数 → 登录态）即优先级：
//   - 412 的文案自带「更换网络出口」字样，风控必须排在网络之前；
//   - IO 错误（permission denied 等）比网络错误更具体，也排在网络之前；
//   - 登录态兜在最后，让「需要大会员权限」这类内容错误按最贴切的那一类给建议。
func errorAdvice(err error) (advice, command string) {
	// 用法错误（未知标志/参数个数不对）的消息形态由 cobra 决定，不能只靠文本标记：按类型先判。
	var ue usageError
	if errors.As(err, &ue) {
		return adviceInput, "bbdown --help"
	}
	msg := strings.ToLower(err.Error())
	switch {
	case containsAny(msg, "412", "风控"):
		return "疑似触发 B 站风控：等几分钟到几十分钟再试，或更换网络出口（连续重试会加重风控）", "bbdown doctor"
	case containsAny(msg, "permission denied", "access is denied", "read-only file system", "no space left on device", "不可写"):
		return "检查输出目录是否可写、磁盘是否还有余量，或用 --work-dir 换到可写目录", "bbdown doctor"
	case containsAny(msg,
		"dial tcp", "connection refused", "connection reset", "connection timed out",
		"no such host", "i/o timeout", "context deadline exceeded", "tls handshake", "x509:",
		"proxyconnect", "network is unreachable", "no route to host", "unexpected eof",
		"请求超时", "连接超时"):
		return "检查网络、代理与 TLS 证书：DNS 解析失败、连接被拒、超时都属这一层", "bbdown doctor"
	case containsAny(msg, "输入有误", "无法识别", "格式不正确", "请提供视频地址", "unknown flag",
		"unknown command", "invalid argument", "flag needs an argument", "参数", "不合法"):
		return adviceInput, "bbdown --help"
	case containsAny(msg, "未登录", "登录已过期", "请先登录", "登录态", "cookie 已过期", "sessdata", "大会员", "需要登录", "credential", "凭据"):
		return "登录态缺失或已过期：扫码登录后再试（高清与字幕需要登录）", "bbdown login"
	}
	// 归不了类的才退回上游那句固定提示；给不出可执行命令就不打第三行。
	return upgradeHint, ""
}

// containsAny 判断 s（调用方已转小写）是否含任一标记。
func containsAny(s string, markers ...string) bool {
	for _, m := range markers {
		if strings.Contains(s, m) {
			return true
		}
	}
	return false
}

// silenceOnCancel 在错误是用户 Ctrl+C 取消时，屏蔽 cobra 自带的 "Error: ..."
// 与 usage 输出；由 Execute 统一打印 "Force Exit..." 并以 0 退出。
func silenceOnCancel(cmd *cobra.Command, err error) error {
	if errors.Is(err, context.Canceled) {
		cmd.SilenceErrors = true
		cmd.SilenceUsage = true
	}
	return err
}

// 上游 1.6.22 对齐核查（t48，实测结论）：上游那版是 C# 的 tokenizer 把 `-` 开头的 token 一律
// 当选项，于是 `--name -尾野` 直接报错。我们用 cobra/pflag，**实测是对的**：需要取值的标志
// 按 GNU getopt 语义取下一个 token（哪怕它以 `-` 开头）。实测矩阵（真实 CLI 子进程，
// 见 internal/cli/argv_dashvalue_test.go）：
//
//	--work-dir -x          → work-dir = -x    （与 --work-dir=-x 逐字相同）
//	--user-agent -尾野      → user-agent = -尾野（与 --user-agent=-尾野 相同）
//	--danmaku-filter -abc  → danmaku-filter = -abc
//	--skip-mux --skip-subtitle → 两个开关都置真（bool 后跟选项不误合并）
//	--multi-thread false   → false（上游 arity 0..1 的折行写法仍然有效）
//	--work-dir --cookie    → work-dir = "--cookie"（GNU 语义：已知选项名也能当值；不特殊处理）
//
// 所以 **pflag 这条链路无需改动**，本文件的改动只有一处：下面这条别名重写此前是「无脑逐
// token 替换」，值恰好等于 -help/-?/-version 时会把值改掉（如 `--user-agent -version` 变成
// `--user-agent --version`，UA 被静默写成 "--version"）。这是与上游同源的历史写法，
// 但既然本轮就在核查「`-` 开头的值」，一并按「值位置不重写」修掉——修复点与用例见
// normalizeCliArgsSkippingValues。
//
// normalizeCliArgs 是上游 NormalizeCliArgs 的直译：**全量**逐 token 重写，不看位置。
// 生产路径已改用下面的 normalizeCliArgsSkippingValues（值位置不能重写）；这个函数保留为
// 「别名规则本身」的直译与既有用例（normalize_test.go）的锚点，两者共用 normalizeToken。
func normalizeCliArgs(args []string) []string {
	out := append([]string(nil), args...)
	for i := range out {
		out[i] = normalizeToken(out[i])
	}
	return out
}

// normalizeToken 是别名重写的**单 token 规则**（上游 NormalizeCliArgs 的三条映射）。
// 抽出来是为了让「全量重写」与「跳过值位置的重写」共用同一份规则，不会各写一套而漂移。
func normalizeToken(tok string) string {
	switch tok {
	case "-help", "-?":
		return "--help"
	case "-version":
		return "--version"
	}
	return tok
}

// normalizeCliArgsSkippingValues 是**生产路径**用的版本：在 normalizeCliArgs 之上跳过「值位置」，
// 只重写真正站在选项位置上的 token。
//
// 为什么需要（t48 实测发现）：`--user-agent -version` 这类argv 里，-version 是**值**而不是选项，
// 无脑重写会把它变成 `--version`——UA 被静默写成 "--version"（不报错，只是错），与上游 1.6.22
// 想修的那类问题同源（「`-` 开头的值被当成选项」）。
//
// 判据只看前一个 token：它是**已知且需要取值**的选项（非 bool）时，当前 token 就是它的值。
// 不追踪更复杂的形态（`--flag=value` 里没有独立的值 token；`--` 之后是位置参数，本来就不该重写，
// 这里也不特殊处理——上游 NormalizeCliArgs 同样逐 token 全量替换，行为一致）。
func normalizeCliArgsSkippingValues(args []string, aliasMap map[string]string, boolFlags map[string]bool) []string {
	out := append([]string(nil), args...)
	for i, a := range out {
		if i > 0 && takesValue(out[i-1], aliasMap, boolFlags) {
			continue // 前一个是需要取值的选项：这一项是它的值，别动
		}
		out[i] = normalizeToken(a)
	}
	return out
}

// takesValue 判断 token 是不是「已知且需要取值」的选项（别名表命中且不是 bool 开关）。
// 带 = 的写法自带值，不算；未知 token 不算（不能凭猜保护）。
func takesValue(token string, aliasMap map[string]string, boolFlags map[string]bool) bool {
	if token == "" || token[0] != '-' || strings.Contains(token, "=") {
		return false
	}
	name, ok := aliasMap[token]
	return ok && !boolFlags[name]
}

// preprocessArgs 把进程 argv 走完整条预处理链：别名重写（跳过值位置）→ bool 值折行 →
// 合并 BBDown.config → 再折一次（配置文件里的 "--flag false" 同样是上游写法）。
//
// 抽成函数不只为了 Execute 可读：t48 的「真实 CLI 子进程」用例（argv_dashvalue_test.go）驱动
// 的必须是**同一份实现**——用例里再抄一条链，生产代码改了用例也不会红。
func preprocessArgs(argv []string) (effective []string, configLoaded bool, err error) {
	aliasMap, boolFlags := buildFlagMaps()
	args := foldBoolFlagValues(normalizeCliArgsSkippingValues(argv, aliasMap, boolFlags), aliasMap, boolFlags)
	merged, configLoaded, err := mergeConfigArgs(args)
	if err != nil {
		return args, false, err
	}
	return foldBoolFlagValues(merged, aliasMap, boolFlags), configLoaded, nil
}

// prepareConsoleOutput 按「是不是机读模式」准备本次运行的控制台输出，返回还原函数：
//   - 人看的模式：照旧先打横幅，日志仍写 os.Stdout（输出一字不改）；
//   - 机读模式：不打横幅，日志让位到 stderr（含随后的「加载配置文件完成」这类解析前输出），
//     stdout 只留数据。
//
// 调用方必须 defer 还原函数。抽成函数是为了让「横幅打不打、日志去哪」能被用例钉住——
// Execute 本身会 os.Exit，测不到整条路径。
func prepareConsoleOutput(machine bool, banner string) (restore func()) {
	if machine {
		return util.RedirectConsoleLogs(stderrWriter{})
	}
	fmt.Print(banner)
	return func() {}
}

// machineReadableArgs 判断一组（已归一化、已折行的）参数是否落在**机读输出模式**：
// `--info-json`（根命令的解析结果 JSON）或 `doctor --json`（自检 JSON）。
//
// 判定只看参数本身、不看 cobra 的解析状态：横幅要在 cobra 之前决定打不打，日志让位要在 RunE 里
// 生效，两处共用这一份判定，避免「横幅省了、日志没省」这类漂移。
func machineReadableArgs(args []string) bool {
	if on, seen := lastBoolFlag(args, "--info-json"); seen && on {
		return true
	}
	// --json 只注册在 doctorCmd 上；再要求出现 doctor 子命令名，让判定读起来就是
	// 「bbdown doctor --json」，也不会被别处的同名取值误命中。
	if !containsArg(args, "doctor") {
		return false
	}
	on, seen := lastBoolFlag(args, "--json")
	return seen && on
}

// lastBoolFlag 返回 --name / --name=<bool> 的最后一次取值（后写覆盖先写，与 pflag 一致）；
// 参数里没出现过这个开关时 seen=false。取值非法时按「开」处理——cobra 随后会把它报成用法错误。
func lastBoolFlag(args []string, name string) (on, seen bool) {
	for _, a := range args {
		switch {
		case a == name:
			on, seen = true, true
		case strings.HasPrefix(a, name+"="):
			v, err := strconv.ParseBool(strings.TrimPrefix(a, name+"="))
			on, seen = err != nil || v, true
		}
	}
	return on, seen
}

// containsArg 判断参数里有没有这一项（子命令名这类按字面出现的参数）。
func containsArg(args []string, name string) bool {
	for _, a := range args {
		if a == name {
			return true
		}
	}
	return false
}

// yieldLogsToStderr 让日志在本次作用域内让位到 stderr（stdout 只留给数据），返回还原函数，
// 调用方必须 defer 它。enabled=false（人看的模式）时是空操作，输出路径一字不改。
func yieldLogsToStderr(enabled bool) (restore func()) {
	if !enabled {
		return func() {}
	}
	return util.RedirectConsoleLogs(stderrWriter{})
}

// stderrWriter 每次写入时重新解析 os.Stderr——与日志默认落点重新解析 os.Stdout 同理：
// 「替换 os.Stderr 捕获输出」的手法对让位后的日志才同样有效。
type stderrWriter struct{}

func (stderrWriter) Write(p []byte) (int, error) { return os.Stderr.Write(p) }

// mergeConfigArgs builds the option alias map from the registered cobra flags
// and merges the config file (config defaults < CLI args).
//
// 第二个返回值表示配置文件确实带进了参数（长度变化），它决定要不要打那行「加载配置文件完成」。
// 打印留给调用方：机读模式下这行是解析前输出，必须先让位到 stderr 才能打。
func mergeConfigArgs(cliArgs []string) (merged []string, loaded bool, err error) {
	aliasMap, boolFlags := buildFlagMaps()
	out, err := config.MergeWithConfig(cliArgs, aliasMap, boolFlags)
	if err != nil {
		return cliArgs, false, err
	}
	return out, len(out) != len(cliArgs), nil
}

// buildFlagMaps collects every registered flag's alias (long and shorthand) and
// whether it is boolean. Both the config merge and the bool-value folding below
// need the same view of the command tree.
func buildFlagMaps() (map[string]string, map[string]bool) {
	aliasMap := make(map[string]string)
	boolFlags := make(map[string]bool)
	add := func(f *pflag.Flag) {
		aliasMap["--"+f.Name] = f.Name
		if f.Shorthand != "" {
			aliasMap["-"+f.Shorthand] = f.Name
		}
		boolFlags[f.Name] = f.Value.Type() == "bool"
	}
	rootCmd.PersistentFlags().VisitAll(add)
	rootCmd.Flags().VisitAll(add)
	serveCmd.Flags().VisitAll(add)
	liveCmd.Flags().VisitAll(add)
	articleCmd.Flags().VisitAll(add)
	watchLaterCmd.Flags().VisitAll(add)
	subAddCmd.Flags().VisitAll(add)
	subCheckCmd.Flags().VisitAll(add)
	return aliasMap, boolFlags
}

// foldBoolFlagValues rewrites "--flag true|false" into "--flag=true|false" for
// boolean flags.
//
// 上游用 System.CommandLine，布尔选项的 arity 是 0..1，所以上游 README 里的
// `--multi-thread false`（关闭多线程）是有效写法；Go 的 pflag 对布尔选项只认
// `--flag=false`，`--flag false` 会把 flag 置为 true、并把 "false" 留成一个位置参数
// （在这里会被当成输入 URL）。照上游文档抄命令的用户会「关不掉」多线程，
// 所以要在这里折平。
//
// "--" 之后全是位置参数，不再折行。值的大小写不敏感（上游 Boolean.Parse 同样如此）。
func foldBoolFlagValues(args []string, aliasMap map[string]string, boolFlags map[string]bool) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			out = append(out, args[i:]...)
			break
		}
		if len(a) > 1 && a[0] == '-' && !strings.Contains(a, "=") {
			if name, ok := aliasMap[a]; ok && boolFlags[name] && i+1 < len(args) {
				switch strings.ToLower(args[i+1]) {
				case "true", "false":
					out = append(out, a+"="+strings.ToLower(args[i+1]))
					i++
					continue
				}
			}
		}
		out = append(out, a)
	}
	return out
}

func init() {
	// Global flags
	rootCmd.PersistentFlags().StringVarP(&optCookie, "cookie", "c", "", "设置cookie用以下载会员内容")
	rootCmd.PersistentFlags().StringVar(&optToken, "access-token", "", "设置access_token用于TV/APP接口")
	rootCmd.PersistentFlags().BoolVar(&debug, "debug", false, "输出调试日志")
	rootCmd.PersistentFlags().StringVar(&optHost, "host", "api.bilibili.com", "指定API host")
	rootCmd.PersistentFlags().StringVar(&optEpHost, "ep-host", "api.bilibili.com", "指定EP host")
	rootCmd.PersistentFlags().StringVar(&optTvHost, "tv-host", "api.snm0516.aisee.tv", "自定义TV端host")
	rootCmd.PersistentFlags().StringVar(&optArea, "area", "", "指定BiliPlus area")
	rootCmd.PersistentFlags().BoolVar(&optInsecure, "insecure", false, "跳过SSL证书验证")
	rootCmd.PersistentFlags().StringVarP(&optUserAgent, "user-agent", "u", "", "指定user-agent")
	rootCmd.PersistentFlags().StringVar(&optWorkDir, "work-dir", "", "设置工作目录")
	rootCmd.PersistentFlags().StringVar(&optFFmpegPath, "ffmpeg-path", "", "设置ffmpeg路径")
	rootCmd.PersistentFlags().StringVar(&optMp4boxPath, "mp4box-path", "", "设置mp4box路径")
	rootCmd.PersistentFlags().StringVar(&optAria2cPath, "aria2c-path", "", "设置aria2c路径")
	rootCmd.PersistentFlags().StringVar(&optConfigFile, "config-file", "", "指定配置文件")

	// Download flags
	rootCmd.Flags().BoolVarP(&optUseTvAPI, "use-tv-api", "t", false, "使用TV端解析模式")
	rootCmd.Flags().BoolVarP(&optUseAppAPI, "use-app-api", "a", false, "使用APP端解析模式")
	rootCmd.Flags().BoolVar(&optUseIntlAPI, "use-intl-api", false, "使用国际版解析模式")
	rootCmd.Flags().BoolVar(&optUseMP4box, "use-mp4box", false, "使用MP4Box混流")
	rootCmd.Flags().StringVarP(&optEncodingPriority, "encoding-priority", "e", "", "编码优先级, 逗号分隔")
	rootCmd.Flags().StringVarP(&optDfnPriority, "dfn-priority", "q", "", "画质优先级, 逗号分隔")
	rootCmd.Flags().BoolVarP(&optOnlyShowInfo, "only-show-info", "I", false, "仅解析不下载")
	rootCmd.Flags().BoolVar(&optShowAll, "show-all", false, "展示所有分P标题")
	rootCmd.Flags().BoolVar(&optUseAria2c, "use-aria2c", false, "调用aria2c下载")
	rootCmd.Flags().BoolVarP(&optInteractive, "interactive", "i", false, "交互式选择清晰度")
	rootCmd.Flags().BoolVar(&optHideStreams, "hide-streams", false, "不要显示所有可用流")
	rootCmd.Flags().BoolVar(&optMultiThread, "multi-thread", true, "使用多线程下载")
	rootCmd.Flags().BoolVar(&optSimplyMux, "simply-mux", false, "精简混流")
	rootCmd.Flags().BoolVar(&optVideoOnly, "video-only", false, "仅下载视频")
	rootCmd.Flags().BoolVar(&optAudioOnly, "audio-only", false, "仅下载音频")
	rootCmd.Flags().BoolVar(&optDanmakuOnly, "danmaku-only", false, "仅下载弹幕")
	rootCmd.Flags().BoolVar(&optCoverOnly, "cover-only", false, "仅下载封面")
	rootCmd.Flags().BoolVar(&optSubOnly, "sub-only", false, "仅下载字幕")
	rootCmd.Flags().BoolVar(&optSkipMux, "skip-mux", false, "跳过混流步骤")
	// DRM 自动检测 + 自动解密默认开启（吸收上游 1.7.1）：解析阶段会带 drm_tech_type=2，
	// 响应标记 DRM 时自动取钥解密。旧脚本显式写的 --decrypt-drm 仍然有效（默认值就是 true），
	// 需要回到旧形态（不解密、产物保持加密格式）用 --no-decrypt-drm。
	rootCmd.Flags().BoolVar(&optDecryptDrm, "decrypt-drm", true, "尝试解密DRM视频（默认开启）")
	rootCmd.Flags().BoolVar(&optNoDecryptDrm, "no-decrypt-drm", false, "关闭DRM自动检测与解密：解析不带 drm_tech_type=2，产物保持加密格式")
	rootCmd.Flags().BoolVar(&optAllowPreview, "allow-preview", false, "允许下载充电试看片段")
	rootCmd.Flags().StringVar(&optDrmKeyHex, "key", "", "DRM解密密钥(hex)")
	rootCmd.Flags().StringVar(&optDrmKidHex, "kid", "", "DRM密钥ID(hex)")
	rootCmd.Flags().StringVar(&optMp4decryptPath, "mp4decrypt-path", "", "mp4decrypt路径")
	rootCmd.Flags().StringVar(&optWvdPath, "wvd-path", "", "device.wvd路径")
	rootCmd.Flags().BoolVar(&optSkipSubtitle, "skip-subtitle", false, "跳过字幕下载")
	rootCmd.Flags().BoolVar(&optSkipCover, "skip-cover", false, "跳过封面下载")
	rootCmd.Flags().BoolVar(&optOverwrite, "overwrite", false, "强制重新下载（忽略已存在的产物；默认沿用上游语义：存在则跳过）")
	rootCmd.Flags().BoolVar(&optPrintURLs, "print-urls", false, "只打印所选流的直链（一行一个）后退出，不下载")
	rootCmd.Flags().BoolVar(&optNFO, "nfo", false, "产物旁写同名 .nfo 侧车元数据（Kodi/Emby/Jellyfin 可读）")
	rootCmd.Flags().BoolVar(&optCompat, "compat", false, "兼容优先：选档避开 HDR Vivid / 杜比视界（本机或多数播放器可能播不了）")
	rootCmd.PersistentFlags().StringVar(&optLogFile, "log-file", "", "同时把日志写入文件（追加；写失败会自动挂起并在控制台提示）")
	rootCmd.Flags().BoolVar(&optM3U, "write-m3u", false, "产物旁写 .m3u 播放列表（多P 按分P顺序，播放器可直接播）")
	rootCmd.Flags().BoolVar(&optInfoJSON, "info-json", false, "只输出解析结果的 JSON 元数据后退出（给脚本/GUI；比 -I 更适合程序消费）")
	rootCmd.Flags().BoolVar(&optForceHTTP, "force-http", false, "强制HTTP协议")
	// Deprecated compatibility options (upstream hidden flags).
	rootCmd.Flags().StringVar(&optAria2cProxy, "aria2c-proxy", "", "aria2c代理(已弃用)")
	rootCmd.Flags().BoolVar(&optAddDfnSuffix, "add-dfn-subfix", false, "添加画质后缀(已弃用)")
	rootCmd.Flags().BoolVar(&optOnlyHevc, "only-hevc", false, "仅HEVC(已弃用)")
	rootCmd.Flags().BoolVar(&optOnlyAvc, "only-avc", false, "仅AVC(已弃用)")
	rootCmd.Flags().BoolVar(&optOnlyAv1, "only-av1", false, "仅AV1(已弃用)")
	rootCmd.Flags().BoolVar(&optNoPaddingPageNum, "no-padding-page-num", false, "分P编号不补零(已弃用)")
	rootCmd.Flags().BoolVar(&optBandwidthAscending, "bandwith-ascending", false, "码率升序(已弃用)")
	rootCmd.Flags().BoolVarP(&optDownloadDanmaku, "download-danmaku", "d", false, "下载弹幕")
	rootCmd.Flags().StringVar(&optDanmakuFormats, "download-danmaku-formats", "", "弹幕格式, 逗号分隔")
	rootCmd.Flags().StringVar(&optDanmakuFilter, "danmaku-filter", "", "弹幕关键词过滤")
	rootCmd.Flags().StringVar(&optDanmakuFilterUser, "danmaku-filter-user", "", "弹幕用户过滤")
	rootCmd.Flags().BoolVar(&optDownloadComments, "comments", false, "下载评论区")
	rootCmd.Flags().StringVar(&optNotifyWebhook, "notify-webhook", "", "下载完成通知URL")
	rootCmd.Flags().BoolVar(&optSkipAi, "skip-ai", false, "跳过AI字幕（默认下载AI字幕，与 BBDownT 默认一致）")
	rootCmd.Flags().StringVar(&optSubtitleLanguage, "subtitle-language", "", "只下载这些语言的字幕(逗号分隔；短标签按语言族匹配，如 zh 含 ai-zh/zh-Hans；带连字符则精确匹配)")
	rootCmd.Flags().StringVar(&optAISubtitlePolicy, "ai-subtitle-policy", "", "AI字幕策略: exclude(过滤AI) / include(全留) / prefer-human(同族有人工时弃AI) / only(只留AI)；显式给定时优先于 --skip-ai")
	rootCmd.Flags().StringVar(&optAudioLanguage, "audio-language", "", "选择配音语言(代码见 -I 列出的可选配音语言，如 chi/jpn)；仅默认 WEB 解析模式，选中后产物名附加 .audio-<代码>")
	rootCmd.Flags().BoolVar(&optVideoAscending, "video-ascending", false, "视频升序")
	rootCmd.Flags().BoolVar(&optAudioAscending, "audio-ascending", false, "音频升序")
	rootCmd.Flags().BoolVar(&optAllowPcdn, "allow-pcdn", false, "不替换PCDN域名")
	rootCmd.Flags().StringVarP(&optFilePattern, "file-pattern", "F", "", "单P文件名模板")
	rootCmd.Flags().StringVarP(&optMultiFilePattern, "multi-file-pattern", "M", "", "多P文件名模板")
	rootCmd.Flags().StringVarP(&optSelectPage, "select-page", "p", "", "选择分P")
	rootCmd.Flags().StringVar(&optLanguage, "language", "", "音频语言代码")
	rootCmd.Flags().StringVar(&optAria2cArgs, "aria2c-args", "", "aria2c附加参数")
	rootCmd.Flags().StringVar(&optUposHost, "upos-host", "", "自定义upos服务器")
	rootCmd.Flags().BoolVar(&optForceReplaceHost, "force-replace-host", true, "强制替换下载host")
	rootCmd.Flags().BoolVar(&optSaveArchives, "save-archives-to-file", false, "记录已下载视频")
	rootCmd.Flags().IntVar(&optDelayPerPage, "delay-per-page", 0, "分P下载间隔(秒)")
	rootCmd.Flags().IntVar(&optMuxerTimeout, "muxer-timeout", 30, "混流超时(分钟)")
	rootCmd.Flags().IntVar(&optRetryCount, "retry-count", 3, "重试次数")
	rootCmd.Flags().IntVar(&optRetryDelay, "retry-delay", 3000, "重试间隔(毫秒)")
	rootCmd.Flags().IntVar(&optThreadSegmentSize, "thread-segment-size", 0, "分片大小(MB)，0=自动（约按并发数份）")
	rootCmd.Flags().StringVar(&optURLsFile, "urls-file", "", "从文件批量读取下载目标（每行一个，# 注释；- 表示 stdin）")

	rootCmd.Flags().IntVar(&optDelayPerVideo, "delay-per-video", defaultDelayPerVideo, "批量下载时视频之间的间隔秒数(0-600；默认 10，0=不等待)")
	rootCmd.Flags().BoolVar(&optDownloadAll, "download-all", false, "把空间/UP 主目标展开为全部投稿逐条下载，并导出 TXT 清单（可用 --urls-file 复用）")
	rootCmd.Flags().BoolVar(&optProgressJSON, "progress-json", false, "进度输出为逐行 JSON 事件到 stderr（供 GUI/自动化集成；默认仍是终端进度条）")

	// Serve flags
	serveCmd.Flags().StringVarP(&optServeListen, "listen", "l", "http://127.0.0.1:23333", "API服务器监听地址")
	serveCmd.Flags().IntVar(&optServeMaxConcurrent, "max-concurrent", 3, "最大并发下载数")
	serveCmd.Flags().StringVar(&optServeToken, "serve-token", "", "API认证Token")
	serveCmd.Flags().StringVar(&optTrustedProxy, "trusted-proxy", "", "信任的反向代理地址（仅此时采用 X-Forwarded-For 判定客户端 IP）")
	serveCmd.Flags().StringVar(&optNotifyWebhook, "notify-webhook", "", "任务完成通知URL")

	// Subcommand flags
	liveCmd.Flags().StringVarP(&optLiveOutput, "output", "o", "", "输出文件路径(默认: 直播间标题_直播录制_时间.flv)")
	articleCmd.Flags().StringVarP(&optArticleOutput, "output", "o", "", "输出 Markdown 文件路径(默认: 专栏标题.md)")
	// 上游 ArticleSettings / LiveSettings 各自声明了 -w|--work-dir（根命令的 --work-dir 没有简写），
	// 少了这个简写，照上游文档敲 `BBDown article -w <dir>` 会直接报 unknown shorthand flag。
	liveCmd.Flags().StringVarP(&optWorkDir, "work-dir", "w", "", "设置工作目录(所有相对路径的根目录)")
	articleCmd.Flags().StringVarP(&optWorkDir, "work-dir", "w", "", "设置工作目录(所有相对路径的根目录)")
	// 上游 ArticleSettings / LiveSettings 各自声明了 -w|--work-dir（根命令的 --work-dir 没有简写），
	// 少了这个简写，照上游文档敲 `BBDown article -w <dir>` 会直接报 unknown shorthand flag。

	watchLaterCmd.Flags().IntVar(&optWatchLaterLimit, "limit", 0, "最多下载前 N 个稍后再看视频(默认 0=全部)")
	subAddCmd.Flags().StringVar(&optSubName, "name", "", "订阅显示名称(默认使用目标字符串)")
	subAddCmd.Flags().StringVar(&optSubFilter, "filter", "", "标题过滤正则(仅下载标题匹配的新稿)")
	// 订阅调度（本期新增）：默认 since 空 + concurrency 1，即与改前完全相同的串行检查、
	// 不做窗口过滤——cron 用户显式给参数才启用增量。
	subCheckCmd.Flags().StringVar(&optSubCheckSince, "since", "", "只下载最近这段时间内发布的新内容，Go duration 语法(如 24h/30m；不支持 d，一天写 24h)")
	subCheckCmd.Flags().IntVar(&optSubCheckConcurrency, "concurrency", 1, "检查阶段的并发订阅数(1-8)，下载仍按订阅顺序串行")
	// 订阅增强（默认都关闭）：不开 --per-sub-dir 时产物布局与改前逐字一致；
	// 不开 --full-scan 时 mid: 订阅默认走增量扫描（空间列举只取 aid，且整页都已下载就停止翻页）。
	subCheckCmd.Flags().BoolVar(&optSubCheckPerSubDir, "per-sub-dir", false, "每条订阅各自下到 <work-dir>/<订阅名>/ 子目录（默认关闭：全部平铺在 work-dir）")
	subCheckCmd.Flags().BoolVar(&optSubCheckFullScan, "full-scan", false, "mid: 订阅翻完所有页（默认增量扫描：整页都已下载就停止翻页，首次部署/历史重建/怀疑漏下时用）")
	// 订阅节流（t71）：默认 0 = 不节流，行为与改前逐字一致。
	subCheckCmd.Flags().IntVar(&optSubCheckDelayPerItem, "delay-per-item", 0, "每个工作单元(开始检查一个订阅/下载一个新增内容)前停顿的秒数(0-600；默认 0=不节流)")
	// 校验挂在 cobra 的 PreRunE 上（subCheckCmd 的 RunE 在 commands.go，本任务 inScope 之外）：
	// 越界值必须在**发任何网络请求之前**报错退出 1。
	subCheckCmd.PreRunE = func(_ *cobra.Command, _ []string) error {
		_, err := parseDelayPerItem(optSubCheckDelayPerItem)
		return err
	}
	doctorCmd.Flags().Bool("json", false, "以 JSON 输出自检结果（便于脚本/监控）")

	// watchlater / sub check inherit the download option semantics (upstream).
	for _, c := range []*cobra.Command{watchLaterCmd, subCheckCmd} {
		c.Flags().StringVarP(&optEncodingPriority, "encoding-priority", "e", "", "视频编码优先级, 如 hevc,avc,av1")
		c.Flags().StringVarP(&optDfnPriority, "dfn-priority", "q", "", "视频清晰度优先级, 如 8K 4K 1080P 高清 720P 高清")
		c.Flags().BoolVarP(&optUseAppAPI, "use-app-api", "a", false, "使用APP端解析模式")
		c.Flags().BoolVarP(&optUseTvAPI, "use-tv-api", "t", false, "使用TV端解析模式")
		c.Flags().BoolVar(&optUseIntlAPI, "use-intl-api", false, "使用国际版解析模式")
		c.Flags().StringVarP(&optWorkDir, "work-dir", "w", "", "设置工作目录(所有相对路径的根目录)")
		c.Flags().BoolVar(&optProgressJSON, "progress-json", false, "进度输出为逐行 JSON 事件到 stderr（供 GUI/自动化集成）")
	}

	// Register subcommands
	rootCmd.AddCommand(doctorCmd)
	rootCmd.AddCommand(resumeCmd) // 本仓特色：一条命令定位「为什么下不动」
	rootCmd.AddCommand(loginCmd)
	rootCmd.AddCommand(loginTVCmd)
	rootCmd.AddCommand(serveCmd)
	rootCmd.AddCommand(liveCmd)
	rootCmd.AddCommand(articleCmd)
	rootCmd.AddCommand(watchLaterCmd)
	rootCmd.AddCommand(subCmd)
	subCmd.AddCommand(subAddCmd)
	subCmd.AddCommand(subListCmd)
	subCmd.AddCommand(subRemoveCmd)
	subCmd.AddCommand(subCheckCmd)
}

func runDownload(cmd *cobra.Command, args []string) error {
	// t78：字幕筛选的两个开关在**发任何请求之前**校验（非法语言/策略当场报错，退出码非 0），
	// 校验通过后写进 util 的选择状态，由 GetSubtitles 统一应用（三个解析路径同一层筛选）。
	subLangs, err := util.ParseSubtitleLanguages(optSubtitleLanguage)
	if err != nil {
		return err
	}
	aiPolicy, err := util.ParseAISubtitlePolicy(optAISubtitlePolicy)
	if err != nil {
		return err
	}
	util.SetSubtitleSelection(subLangs, aiPolicy)
	// t88 ⑤：本次运行结束时恢复选择状态（单命令进程，防御性复位，避免跨命令复用状态）。
	defer util.SetSubtitleSelection(nil, util.AISubtitleUnset)

	// 机读模式（--info-json）：日志让位到 stderr，stdout 只留 JSON 数据；RunE 返回时还原。
	restoreLogs := yieldLogsToStderr(optInfoJSON)
	defer restoreLogs()

	// F2 批量输入（本仓新功能）：位置参数可给多个（此前只取 args[0]，其余被静默忽略），
	// 也可用 --urls-file 从文件/stdin 读列表。
	targets, err := collectTargets(args, optURLsFile, os.Stdin)
	if err != nil {
		return err
	}
	if len(targets) == 0 {
		return fmt.Errorf("请提供视频地址")
	}
	optURL = targets[0]

	// F5：把 --progress-json 交给下载层（默认关，关闭时仍是终端进度条）。
	applyProgressJSON()

	// Build MyOption from flags
	cfg := buildMyOption()

	// t80：--delay-per-video 同样在发任何请求之前校验（0-600；默认 10 = BBDownT 口径）。
	if d, err := parseDelayPerVideo(optDelayPerVideo); err != nil {
		return err
	} else {
		batchDelayPerVideo = d
	}

	// t79：--audio-language 的形态与模式互斥在**任何网络请求之前**判定（与上面字幕两个开关同一纪律；
	// workflow.Run 里还有一次同样的校验，供 serve 等其它入口复用）。
	if err := workflow.ValidateAudioLanguageOptions(optAudioLanguage, cfg); err != nil {
		return err
	}

	// APP 接口不识别 WEB 登录 Cookie：登录用户加 -a 看不到高清时给出提示。
	warnAppAPIWithoutToken(cfg.UseAppAPI, cfg.AccessToken)

	// t80：--download-all 在批量循环之前把空间/UP 主目标展开成逐个投稿（并在 work-dir 写 TXT 清单，
	// 可直接回喂 --urls-file）。展开失败要明确报错，不能静默少下。
	if optDownloadAll {
		client := buildHTTPClient(cfg)
		spacePageLister = func(lctx context.Context, mid string) ([]entity.Page, error) {
			// 与 sub check 同一条路径：一次 InitSession 拿 wbi，然后全量扫描该 UP 主的投稿。
			wbi, err := workflow.InitSession(lctx, &cfg, client)
			if err != nil {
				return nil, err
			}
			space := fetcher.NewSpaceVideoFetcher(client, wbi, cfg.Cookie, fetcher.SpaceScanOptions{FullScan: true})
			info, err := space.Fetch(lctx, "mid:"+mid)
			if err != nil {
				return nil, err
			}
			return info.PagesInfo, nil
		}
		expanded, err := buildDownloadAllTargets(cmd.Context(), targets, spacePageLister, spaceListPathFor(targets, cfg.WorkDir))
		if err != nil {
			return err
		}
		targets = expanded
		if len(targets) == 0 {
			return fmt.Errorf("--download-all 没有展开出任何投稿")
		}
		optURL = targets[0]
	}

	// Run the workflow
	client := buildHTTPClient(cfg)

	// Fire-and-forget update check (upstream DefaultCommand)：批量也只查一次。
	updateCheck(context.Background(), client, "v3.1.1")

	// 中断 ctx 来自 Execute 的统一安装（见 interrupt.go）：runDownload 与 resume 走同一条
	// downloadTargets，不会出现两套取消语义。
	return downloadTargets(commandContext(cmd), cmd, cfg, client, targets)
}

// updateCheck 是「检查新版本」的接线点（默认 util.CheckUpdateAsync，上游 fire-and-forget 语义不变）。
//
// 抽成变量是为了让用例能走**真实 RunE** 又不出网：runDownload 里唯一的后台网络调用就是它，
// 机读模式的守卫用例（machineoutput_test.go）需要离线跑完整条 RunE。手法同 doctorChecks。
var updateCheck = util.CheckUpdateAsync

// productsUnknown 表示拿不到本轮的产出文件数（见 batchSummary.products 的说明）。
const productsUnknown = -1

// batchSummary 是一次批量下载的收尾统计。格式化成纯函数（formatBatchSummary），
// 可以直接断言字符串，不用把统计逻辑纠缠进下载流程。
type batchSummary struct {
	succeeded int
	failed    int
	elapsed   time.Duration

	// bytes 是本轮落盘的**字节数**（OnSaved 钩子累计）；<=0 表示拿不到，此时汇总里不报平均速率。
	bytes int64

	// products 是本轮落盘的产物文件数；<0 表示拿不到产出列表，此时汇总里不报这一项。
	//
	// 下载路径目前恒为 productsUnknown：workflow.Run 只返回 error，要拿到完整产物列表
	// 就得给它加返回值（不为统计去改 workflow 接口）；既有的 OnSaved 钩子只覆盖部分路径
	// ——弹幕-only、字幕-only、评论导出都不上报（workflow.go 的 DanmakuOnly / SubOnly /
	// FetchComments 分支），拿它充数会打出比实际小的文件数，宁可少报一项。
	products int
}

// byteAccumulator 累计落盘字节（按路径去重）。OnSaved 在分片重试/断点续传时会重复调用，
// 同路径重复上报只计一次（与 serve 的 onArtifactSaved 同一语义）；stat 失败/非普通文件
// 不计（宁可少报不虚报）。返回本次实际计入的字节数，0 = 重复或拿不到。
type byteAccumulator struct {
	saved sync.Map
}

func (a *byteAccumulator) add(path string) int64 {
	if _, dup := a.saved.LoadOrStore(path, struct{}{}); dup {
		return 0
	}
	if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
		return info.Size()
	}
	return 0
}

// formatBatchSummary 把收尾统计拼成一行。耗时按 100ms 取整：一次批量下载报「1m2.3s」
// 足够，没必要把亚毫秒抖动写进去。
func formatBatchSummary(stats batchSummary) string {
	line := fmt.Sprintf("下载完成：成功 %d 个，失败 %d 个，耗时 %s",
		stats.succeeded, stats.failed, stats.elapsed.Round(100*time.Millisecond))
	if speed := formatAverageSpeed(stats.bytes, stats.elapsed); speed != "" {
		line += " · 平均 " + speed
	}
	if stats.products >= 0 {
		line += fmt.Sprintf("，产出 %d 个文件", stats.products)
	}
	return line
}

// formatAverageSpeed 是本批下载的平均速率（落盘字节 / 实际耗时）。
//
// 单位与进度行的速率**同一口径**（MB 一位小数、KB/B 取整，见 download.formatSpeed）：
// 用户在进度行看到 6.2 MB/s，收尾行说「平均 6.8 MB/s」——两处单位一致才看得出快慢。
//
// 字节数或耗时为 0（拿不到产物、或瞬时完成）时返回空串：汇总里不报这一项，
// 而不是打一个没有意义的「平均 0 B/s」。耗时与字节数都是入参，用例可注入，不读墙钟。
func formatAverageSpeed(bytes int64, elapsed time.Duration) string {
	secs := elapsed.Seconds()
	if bytes <= 0 || secs <= 0 {
		return ""
	}
	b := float64(bytes) / secs
	switch {
	case b >= 1024*1024:
		return fmt.Sprintf("%.1f MB/s", b/(1024*1024))
	case b >= 1024:
		return fmt.Sprintf("%.0f KB/s", b/1024)
	default:
		return fmt.Sprintf("%.0f B/s", b)
	}
}

// downloadTargets 执行一批目标，并维护**未完成任务清单**（bbdown resume 的底座）：
// 成功的从清单移除，失败/被取消的登记（含最后错误）。抽成函数让 runDownload 与 resume 共用同一条路径。
//
// 取消语义由 Execute 统一安装（installRootInterrupts，见 interrupt.go）；这里的
// installInterrupts 是幂等的：已有安装就复用那一份（不重复注册信号），直接调用（用例、
// 或将来别的入口）时这里自己装、返回时注销。调用方不需要自己建信号 ctx。
func downloadTargets(ctx context.Context, cmd *cobra.Command, cfg config.MyOption, client *util.HTTPClient, targets []string) error {
	ctx, stopInterrupts := installInterrupts(ctx)
	defer stopInterrupts()

	started := time.Now()
	var batchBytes atomic.Int64 // 本批落盘字节数（OnSaved 累计）

	var firstErr error
	failures := runTargets(ctx, targets, func(ctx context.Context, target string) error {
		one := cfg
		one.URL = target
		wf := workflow.New(one, client)
		wf.SetAudioLanguage(optAudioLanguage) // t79：配音语言选择（校验在 workflow.Run 开头统一做）
		// 收尾汇总的平均速率要的是**落盘字节**：OnSaved 是产物落盘的唯一出口（与 serve 同一钩子），
		// 这里顺手累计一次；拿不到大小（stat 失败）就不计，宁可少报也不虚报。
		// 按路径去重（与 serve 的 onArtifactSaved 同一语义）：分片重试/断点续传会重复调用 OnSaved，
		// 同路径重复上报只计一次（byteAccumulator 有回归用例）。
		acc := &byteAccumulator{}
		wf.OnSaved = func(path string) {
			if n := acc.add(path); n > 0 {
				batchBytes.Add(n)
			}
		}
		err := wf.Run(ctx)
		// Ctrl+C 取消：静默 cobra 的 "Error:" 与 usage 输出，由 Execute 统一提示。
		if errors.Is(err, context.Canceled) {
			cmd.SilenceErrors = true
			cmd.SilenceUsage = true
		}
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			if len(targets) > 1 {
				util.LogError("%s 失败: %v", target, err)
			}
			if perr := upsertPending(cfg.WorkDir, target, err.Error()); perr != nil {
				util.LogWarn("记录未完成任务失败: %v", perr)
			}
		} else if rerr := removePending(cfg.WorkDir, target); rerr != nil {
			util.LogWarn("清理未完成任务失败: %v", rerr)
		}
		return err
	})
	if ctx.Err() != nil {
		return ctx.Err()
	}
	// 任务 G-2：整批跑完时打一行收尾汇总（含部分失败——那正是要看到失败数的时候）。
	// 被 Ctrl+C 打断时不打：「成功/失败数」没有意义，取消提示已由中断处理给出。
	util.Log("%s", formatBatchSummary(batchSummary{
		succeeded: len(targets) - failures,
		failed:    failures,
		elapsed:   time.Since(started),
		bytes:     batchBytes.Load(),
		products:  productsUnknown,
	}))
	if failures > 0 {
		if failures == 1 && firstErr != nil {
			return firstErr
		}
		if firstErr != nil {
			return fmt.Errorf("%d/%d 个任务失败，第一个错误：%w", failures, len(targets), firstErr)
		}
		return fmt.Errorf("%d/%d 个任务失败", failures, len(targets))
	}
	return nil
}
