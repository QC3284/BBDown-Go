package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/QC3284/BBDown-Go/internal/article"
	"github.com/QC3284/BBDown-Go/internal/config"
	"github.com/QC3284/BBDown-Go/internal/download"
	"github.com/QC3284/BBDown-Go/internal/fetcher"
	"github.com/QC3284/BBDown-Go/internal/live"
	"github.com/QC3284/BBDown-Go/internal/login"
	"github.com/QC3284/BBDown-Go/internal/muxer"
	"github.com/QC3284/BBDown-Go/internal/server"
	"github.com/QC3284/BBDown-Go/internal/substore"
	"github.com/QC3284/BBDown-Go/internal/util"
	"github.com/QC3284/BBDown-Go/internal/workflow"
	"github.com/spf13/cobra"
	"path/filepath"
)

// Batch-command WBI presets: fetched once per command, reused per item to
// avoid one nav request per video (upstream initializes the session once).
var (
	watchLaterWbi string
	subCheckWbi   string
)

// applyProgressJSON 把 --progress-json 传给下载层（F5，本仓新功能）。
// 默认关：关闭时下载层照旧画终端进度条，行为与改前一致。
func applyProgressJSON() { download.SetProgressJSON(optProgressJSON) }

// warnAppAPIWithoutToken 提示 APP 接口不识别 WEB 登录 Cookie（与上游行为一致）：
// 登录用户加 -a 看不到高清属于预期行为，需要 APP token 或改用默认 WEB 接口。
func warnAppAPIWithoutToken(useAppAPI bool, token string) {
	if useAppAPI && strings.TrimSpace(token) == "" {
		util.LogWarn("提示: APP 接口(-a)不识别 WEB 登录 Cookie，登录后也不会解锁更高画质；如需高清请使用默认 WEB 接口，或通过 --access-token 传入 APP token")
	}
}

var loginCmd = &cobra.Command{
	Use:   "login",
	Short: "通过APP扫描二维码以登录您的WEB账号",
	RunE: func(cmd *cobra.Command, args []string) error {
		// 中断语义由 Execute 统一安装（见 interrupt.go）：这里只取那份 ctx，不再自己注册信号，
		// 否则第二次 Ctrl+C 会被 signal 层吞掉。
		ctx := commandContext(cmd)
		client := buildHTTPClient(config.MyOption{})
		if err := login.LoginWeb(ctx, client); err != nil {
			return err
		}
		util.Log("WEB 登录成功！")
		return nil
	},
}

var loginTVCmd = &cobra.Command{
	Use:   "logintv",
	Short: "通过APP扫描二维码以登录您的TV账号",
	RunE: func(cmd *cobra.Command, args []string) error {
		// 中断语义由 Execute 统一安装（见 interrupt.go）：这里只取那份 ctx，不再自己注册信号，
		// 否则第二次 Ctrl+C 会被 signal 层吞掉。
		ctx := commandContext(cmd)
		client := buildHTTPClient(config.MyOption{})
		if err := login.LoginTV(ctx, client); err != nil {
			return err
		}
		util.Log("TV 登录成功！")
		return nil
	},
}

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "以服务器模式运行",
	RunE: func(cmd *cobra.Command, args []string) error {
		listen := optServeListen
		if listen == "" {
			listen = "http://127.0.0.1:23333"
		}
		// Upstream rejects max-concurrent < 1 instead of silently clamping.
		if optServeMaxConcurrent < 1 {
			return fmt.Errorf("参数有误：--max-concurrent 需 >= 1，当前为 %d", optServeMaxConcurrent)
		}

		serveToken := resolveServeToken(optServeToken, os.Getenv("BBDOWN_SERVE_TOKEN"))
		srv := server.NewAPIServer(listen, optServeMaxConcurrent, serveToken, optNotifyWebhook)
		srv.SetTrustedProxy(optTrustedProxy)

		// 中断语义由 Execute 统一安装（见 interrupt.go）：这里只取那份 ctx，不再自己注册信号，
		// 否则第二次 Ctrl+C 会被 signal 层吞掉。
		ctx := commandContext(cmd)

		// Fire-and-forget update check (upstream ServeCommand).
		util.CheckUpdateAsync(ctx, buildHTTPClient(config.MyOption{}), "v3.1.0")

		err := srv.Run(ctx)
		if errors.Is(err, http.ErrServerClosed) {
			// Ctrl+C 正常关停，不算错误。
			return nil
		}
		return err
	},
}

// resolveServeToken 决定 serve 使用的 token（上游 BBDownApiServer.ResolveServeToken）：
// 环境变量 BBDOWN_SERVE_TOKEN 优先于 --serve-token——它能让密钥不出现在进程命令行与容器调用里。
// 环境变量为空串时回落到旗标。
func resolveServeToken(cliToken, envToken string) string {
	if envToken != "" {
		return envToken
	}
	return cliToken
}

// resolveUnderWorkDir resolves a product path against --work-dir: an absolute
// path is used as-is, a relative one is joined with the work dir, and an unset
// work dir leaves it relative to the process directory. Subcommands that produce
// a single artefact (article, live) follow the download pipeline here.
func resolveUnderWorkDir(workDir, path string) string {
	if path == "" || workDir == "" || isRootedPath(path) {
		return path
	}
	return filepath.Join(workDir, path)
}

// isRootedPath reports whether a product path decides its own location. On
// Windows filepath.IsAbs rejects a root-relative path such as "\\dir\\file" or
// "/dir/file" (it wants a drive letter), yet such a path is clearly not meant to
// land under the work directory — upstream uses Path.IsPathRooted, which accepts it.
func isRootedPath(path string) bool {
	if filepath.IsAbs(path) || filepath.VolumeName(path) != "" {
		return true
	}
	return strings.HasPrefix(path, "/") || strings.HasPrefix(path, "\\\\")
}

var liveCmd = &cobra.Command{
	Use:   "live [room_id]",
	Short: "录制B站直播流",
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) < 1 {
			return fmt.Errorf("请提供直播间ID")
		}
		roomID := args[0]

		// Segment merging at the end requires ffmpeg; fail fast (upstream).
		if _, err := os.Stat(muxer.FFMPEG); err != nil {
			if p, err := exec.LookPath("ffmpeg"); err == nil {
				muxer.FFMPEG = p
			} else {
				return fmt.Errorf("找不到可执行的ffmpeg文件，直播分段合成需要 ffmpeg")
			}
		}

		// 中断语义由 Execute 统一安装（见 interrupt.go）：这里只取那份 ctx，不再自己注册信号，
		// 否则第二次 Ctrl+C 会被 signal 层吞掉。
		ctx := commandContext(cmd)

		client := buildHTTPClient(config.MyOption{})
		// Load credentials BEFORE resolving the stream: getRoomPlayInfo answers an
		// anonymous request with the guest tier only, so a logged-in recording was
		// silently capped and could never reach the qn=30000 tier asked for below.
		// An explicit --cookie wins over the local BBDown.data file (upstream runs
		// InitializeRequestSessionAsync before recording).
		sessOpt := config.DefaultMyOption()
		sessOpt.Cookie = optCookie
		if _, err := workflow.InitSession(ctx, &sessOpt, client); err != nil {
			util.LogWarn("读取登录凭据失败，将以游客身份录制: %v", err)
		} else {
			client.SetCookieFn(func() string { return sessOpt.Cookie })
			if sessOpt.Cookie != "" {
				util.Log("已加载登录凭据，将按账号权限请求画质")
			}
		}
		util.Log("正在解析直播间 %s...", roomID)
		_, title, uname, err := live.ResolveLive(ctx, roomID, client)
		if err != nil {
			return err
		}

		util.Log("直播间: %s (UP: %s)", title, uname)
		outPath := optLiveOutput
		if outPath == "" {
			outPath = fmt.Sprintf("%s_直播录制_%s.flv", live.SanitizeFileName(title), time.Now().Format("20060102_150405"))
		}
		// Both the default name and the .segs directory follow --work-dir.
		outPath = resolveUnderWorkDir(optWorkDir, outPath)
		util.Log("开始录制直播流: %s (Ctrl+C 停止，断流自动重连)", outPath)

		result, err := live.DownloadToFile(ctx, roomID, outPath, client)
		if err != nil {
			if result == live.LiveConcatFailedWithSegmentsSaved {
				util.LogWarn("直播合成未完成，原始分段已保留，可手动合成")
			}
			return silenceOnCancel(cmd, err)
		}
		switch result {
		case live.LiveNoData:
			util.Log("未录制到任何内容")
		case live.LiveConcatFailedWithSegmentsSaved:
			util.LogWarn("直播合成未完成，原始分段已保留，可手动合成")
		default:
			util.Log("直播录制完成: %s", outPath)
		}
		return nil
	},
}

var articleCmd = &cobra.Command{
	Use:   "article [cv_id]",
	Short: "下载B站专栏文章为 Markdown",
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) < 1 {
			return fmt.Errorf("请提供文章cv号")
		}
		// 中断语义由 Execute 统一安装（见 interrupt.go）：这里只取那份 ctx，不再自己注册信号，
		// 否则第二次 Ctrl+C 会被 signal 层吞掉。
		ctx := commandContext(cmd)

		cvID, err := article.ExtractCvId(args[0])
		if err != nil {
			return err
		}
		util.Log("正在获取专栏 cv%s...", cvID)
		client := buildHTTPClient(config.MyOption{})
		a, err := article.Fetch(ctx, client, cvID)
		if err != nil {
			return silenceOnCancel(cmd, err)
		}
		path := optArticleOutput
		if path == "" {
			name := util.GetValidFileName(a.Title, "_", true)
			if strings.TrimSpace(name) == "" {
				name = "专栏"
			}
			path = name + ".md"
		}
		path = resolveUnderWorkDir(optWorkDir, path)
		if err := article.SaveAsMarkdown(a, path); err != nil {
			return fmt.Errorf("专栏保存失败: %w", err)
		}
		util.Log("专栏已保存: %s", path)
		return nil
	},
}

var watchLaterCmd = &cobra.Command{
	Use:   "watchlater",
	Short: "下载稍后再看列表",
	RunE:  runWatchLater,
}

var subCmd = &cobra.Command{
	Use:   "sub",
	Short: "订阅管理 (add/list/remove/check)",
}

var subAddCmd = &cobra.Command{
	Use:   "add [target]",
	Short: "添加订阅",
	Args:  usageArgs(cobra.MinimumNArgs(1)),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := substore.Add(args[0], optSubName, optSubFilter); err != nil {
			return err
		}
		util.Log("已添加订阅: %s", args[0])
		return nil
	},
}

var subListCmd = &cobra.Command{
	Use:   "list",
	Short: "列出订阅",
	RunE: func(cmd *cobra.Command, args []string) error {
		subs, err := substore.ListSorted()
		if err != nil {
			return err
		}
		if len(subs) == 0 {
			util.Log("当前没有订阅，请先用 BBDown sub add <目标> 添加")
			return nil
		}
		util.Log("共 %d 个订阅:", len(subs))
		for _, s := range subs {
			// 没设过滤时这一行与改前逐字相同（旧清单/不用该功能的用户不受影响）。
			filter := ""
			if s.Filter != "" {
				filter = "  过滤: " + s.Filter
			}
			util.Log("  %s  [%s]  (添加于 %s)%s", s.Target, s.Name, time.Unix(s.AddedAt, 0).Local().Format("2006-01-02 15:04"), filter)
		}
		return nil
	},
}

var subRemoveCmd = &cobra.Command{
	Use:   "remove [target]",
	Short: "移除订阅",
	Args:  usageArgs(cobra.MinimumNArgs(1)),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := substore.Remove(args[0]); err != nil {
			return err
		}
		util.Log("已移除订阅: %s", args[0])
		return nil
	},
}

var subCheckCmd = &cobra.Command{
	Use:   "check",
	Short: "检查订阅并下载新内容",
	RunE:  runSubCheck,
}

// runWatchLater downloads the watch-later list (upstream WatchLaterCommand).
func runWatchLater(cmd *cobra.Command, args []string) error {
	// 中断语义由 Execute 统一安装（见 interrupt.go）：这里只取那份 ctx，不再自己注册信号，
	// 否则第二次 Ctrl+C 会被 signal 层吞掉。
	ctx := commandContext(cmd)

	cfg := config.DefaultMyOption()
	cfg.Cookie = optCookie
	cfg.AccessToken = optToken
	cfg.UseTvAPI = optUseTvAPI
	cfg.UseAppAPI = optUseAppAPI
	cfg.UseIntlAPI = optUseIntlAPI
	cfg.WorkDir = optWorkDir
	applyProgressJSON()
	warnAppAPIWithoutToken(cfg.UseAppAPI, cfg.AccessToken)

	client := buildHTTPClient(cfg)
	wbi, err := workflow.InitSession(ctx, &cfg, client)
	if err != nil {
		return err
	}
	watchLaterWbi = wbi

	util.Log("正在获取稍后再看列表...")
	list, err := fetchWatchLater(ctx, client)
	if err != nil {
		return err
	}
	if len(list) == 0 {
		util.Log("稍后再看列表为空")
		return nil
	}

	targets := list
	if optWatchLaterLimit > 0 && len(targets) > optWatchLaterLimit {
		targets = targets[:optWatchLaterLimit]
	}
	util.Log("共 %d 个稍后再看，开始下载 %d 个...", len(list), len(targets))
	succeeded := 0
	failed := 0
	for _, t := range targets {
		util.Log("--- 下载 av%s %s ---", t.Aid, t.Title)
		opt := config.DefaultMyOption()
		opt.URL = "av" + t.Aid
		opt.Cookie = cfg.Cookie
		opt.AccessToken = cfg.AccessToken
		opt.EncodingPriority = optEncodingPriority
		opt.DfnPriority = optDfnPriority
		opt.UseAppAPI = cfg.UseAppAPI
		opt.UseTvAPI = cfg.UseTvAPI
		opt.UseIntlAPI = cfg.UseIntlAPI
		opt.WorkDir = cfg.WorkDir
		opt.Wbi = watchLaterWbi
		wf := workflow.New(opt, client)
		wf.SetAudioLanguage(optAudioLanguage) // t79：与主下载路径同一开关
		if err := wf.Run(ctx); err != nil {
			if errors.Is(err, context.Canceled) {
				return silenceOnCancel(cmd, err)
			}
			failed++
			util.LogWarn("av%s 下载失败（继续下一个）: %v", t.Aid, err)
			continue
		}
		succeeded++
	}
	util.Log("稍后再看下载完成：成功 %d 个，失败 %d 个", succeeded, failed)
	if failed > 0 {
		return fmt.Errorf("%d 个下载失败", failed)
	}
	return nil
}

type watchLaterItem struct {
	Aid   string `json:"aid"`
	Title string `json:"title"`
}

func fetchWatchLater(ctx context.Context, client *util.HTTPClient) ([]watchLaterItem, error) {
	api := "https://api.bilibili.com/x/v2/history/toview"
	source, err := client.GetWebSource(ctx, api)
	if err != nil {
		return nil, err
	}
	var r struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    struct {
			List []watchLaterItem `json:"list"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(source), &r); err != nil {
		return nil, err
	}
	if r.Code != 0 {
		return nil, fmt.Errorf("获取稍后再看失败(code=%d): %s。该接口需要登录，请先运行 BBDown login 或传入 --cookie。", r.Code, r.Message)
	}
	var list []watchLaterItem
	for _, item := range r.Data.List {
		if item.Aid != "" {
			list = append(list, item)
		}
	}
	return list, nil
}

// runSubCheck checks all subscriptions and downloads new content (upstream).
//
// 本期新增调度（见 subcheck_schedule.go）：--since 增量窗口 + --concurrency 并发检查。
// 检查/下载的编排本身在 subCheckRun 里（依赖可注入，离线可测）；这里负责参数校验、
// 订阅清单与会话准备，以及把结果映射回既有的退出码语义。
func runSubCheck(cmd *cobra.Command, args []string) error {
	// ① 参数校验放在最前面：--since/--concurrency 打错时必须当场报错（退出码非 0），
	// 且不读、不写任何状态文件（损坏清单不会被隔离、也不发任何网络请求）。
	sched, err := parseSubCheckSchedule(optSubCheckSince, optSubCheckConcurrency)
	if err != nil {
		return err
	}

	subs, err := substore.Load()
	if err != nil {
		return err
	}
	// ② 去重（Load 之后、进入调度之前）：手改清单可能让同一 Target 出现多条，而 --concurrency≥2
	// 会先把所有订阅检查完再下载——重复的订阅各自在「历史还空着」时取到同一批新稿，同一批 aid
	// 就被下载两遍。正常用法造不出重复（sub add 同 Target 是替换），只有手改清单能。
	subs = subCheckDeduped(subs)
	if len(subs) == 0 {
		util.LogWarn("当前没有订阅，请先用 BBDown sub add <目标> 添加")
		return nil
	}

	// 中断语义由 Execute 统一安装（见 interrupt.go）：这里只取那份 ctx，不再自己注册信号，
	// 否则第二次 Ctrl+C 会被 signal 层吞掉。
	ctx := commandContext(cmd)

	cfg := config.DefaultMyOption()
	cfg.Cookie = optCookie
	cfg.AccessToken = optToken
	cfg.UseTvAPI = optUseTvAPI
	cfg.UseAppAPI = optUseAppAPI
	cfg.UseIntlAPI = optUseIntlAPI
	cfg.WorkDir = optWorkDir
	applyProgressJSON()
	warnAppAPIWithoutToken(cfg.UseAppAPI, cfg.AccessToken)
	client := buildHTTPClient(cfg)
	wbi, err := workflow.InitSession(ctx, &cfg, client)
	if err != nil {
		return err
	}
	subCheckWbi = wbi

	factory := fetcher.NewFactory(client, cfg.UseIntlAPI, wbi, cfg.Cookie, cfg.Host, cfg.EpHost, cfg.AccessToken)
	// --per-sub-dir 的目录规划在这里做一次（订阅集合此时已去重，规划结果整轮复用）：
	// 净化后重名的订阅按顺序拿 -2/-3，规划是纯函数，离线可逐字断言。
	runOpts := subCheckRunOptions{PerSubDir: optSubCheckPerSubDir, FullScan: optSubCheckFullScan}
	if runOpts.PerSubDir {
		runOpts.SubDirs = substore.PlanSubDirs(subs)
	}
	deps := defaultSubCheckDepsWith(cfg, client, factory, subCheckWbi, runOpts)

	// 窗口边界只取一次：同一次运行里所有订阅共用同一个 now，长跑时各订阅的窗口不会互相漂移。
	now := time.Now()
	failures, err := subCheckRun(ctx, subs, deps, sched, now)
	if err != nil {
		// ctx 被取消（订阅边界上中断）时与改前同样走 silenceOnCancel：静默 + 非 0 的取消退出码。
		return silenceOnCancel(cmd, err)
	}
	return subCheckResult(ctx.Err() != nil, failures)
}

// subCheckResult turns the per-video outcomes into the command's exit status.
// Partial failures must not report success: the check used to end with an
// unconditional "return nil", so scripts driving "BBDown sub check" saw exit 0
// even when videos failed. A user cancellation still returns 0, which is the
// documented behaviour for subcommands (see root.go silenceOnCancel).
func subCheckResult(cancelled bool, failures int) error {
	if cancelled {
		return nil
	}
	if failures > 0 {
		return fmt.Errorf("订阅检查完成，但有 %d 个视频下载失败", failures)
	}
	return nil
}

// subNewAids 计算某订阅本次要下载的 aid（F6 标题过滤 + 既有历史去重）：
//
//   - 稿件标题不匹配订阅的 --filter 正则 → 返回空，这一稿整条跳过；
//   - 过滤正则非法（清单被手改/旧版本写入）→ 返回错误，调用方据此跳过该订阅，
//     而不是当成「全部通过」——那会把用户明确排除的稿件也下下来；
//   - 没有过滤时就是「历史里没下载过的 aid」。
func subNewAids(sub substore.Subscription, title string, aids, history []string) ([]string, error) {
	re, err := sub.CompileFilter()
	if err != nil {
		return nil, err
	}
	if re != nil && !re.MatchString(title) {
		return nil, nil
	}
	var newAids []string
	for _, aid := range aids {
		known := false
		for _, h := range history {
			if h == aid {
				known = true
				break
			}
		}
		if !known {
			newAids = append(newAids, aid)
		}
	}
	return newAids, nil
}

func joinAids(aids []string) string {
	var sb []string
	for _, a := range aids {
		sb = append(sb, "av"+a)
	}
	return strings.Join(sb, ", ")
}

// buildMyOption constructs a MyOption from CLI flags.
// decryptDrmEnabled 把 --decrypt-drm / --no-decrypt-drm 两个开关折成一个布尔值（矩阵：负向优先）。
//
// 为什么要有这个函数：DRM 自动解密默认开启（t49/上游 1.7.1），而「关闭」是一个独立的负向开关——
// 只看 optDecryptDrm 的话，默认 true 会让 --no-decrypt-drm 失去意义；只看负向开关又会丢掉
// 旧脚本显式 --decrypt-drm=false 的语义。折成纯函数后，四种组合能在离线用例里逐格断言，
// 不必真跑一次命令。
func decryptDrmEnabled(decryptDrm, noDecryptDrm bool) bool {
	if noDecryptDrm {
		return false
	}
	return decryptDrm
}

func buildMyOption() config.MyOption {
	return config.MyOption{
		URL:                    optURL,
		UseTvAPI:               optUseTvAPI,
		UseAppAPI:              optUseAppAPI,
		UseIntlAPI:             optUseIntlAPI,
		UseMP4box:              optUseMP4box,
		EncodingPriority:       optEncodingPriority,
		DfnPriority:            optDfnPriority,
		OnlyShowInfo:           optOnlyShowInfo,
		ShowAll:                optShowAll,
		UseAria2c:              optUseAria2c,
		Interactive:            optInteractive,
		HideStreams:            optHideStreams,
		MultiThread:            optMultiThread,
		SimplyMux:              optSimplyMux,
		VideoOnly:              optVideoOnly,
		AudioOnly:              optAudioOnly,
		DanmakuOnly:            optDanmakuOnly,
		CoverOnly:              optCoverOnly,
		SubOnly:                optSubOnly,
		Debug:                  debug,
		SkipMux:                optSkipMux,
		Insecure:               optInsecure,
		DecryptDrm:             decryptDrmEnabled(optDecryptDrm, optNoDecryptDrm),
		AllowPreview:           optAllowPreview,
		DrmKeyHex:              optDrmKeyHex,
		DrmKidHex:              optDrmKidHex,
		Mp4decryptPath:         optMp4decryptPath,
		WvdPath:                optWvdPath,
		SkipSubtitle:           optSkipSubtitle,
		SkipCover:              optSkipCover,
		Overwrite:              optOverwrite,
		PrintURLs:              optPrintURLs,
		WriteNFO:               optNFO,
		Compat:                 optCompat,
		WriteM3U:               optM3U,
		InfoJSON:               optInfoJSON,
		ForceHTTP:              optForceHTTP,
		DownloadDanmaku:        optDownloadDanmaku,
		DownloadDanmakuFormats: optDanmakuFormats,
		DanmakuFilter:          optDanmakuFilter,
		DanmakuFilterUser:      optDanmakuFilterUser,
		DownloadComments:       optDownloadComments,
		NotifyWebhook:          optNotifyWebhook,
		SkipAi:                 optSkipAi,
		VideoAscending:         optVideoAscending,
		AudioAscending:         optAudioAscending,
		AllowPcdn:              optAllowPcdn,
		FilePattern:            optFilePattern,
		MultiFilePattern:       optMultiFilePattern,
		SelectPage:             optSelectPage,
		Language:               optLanguage,
		UserAgent:              optUserAgent,
		Cookie:                 optCookie,
		AccessToken:            optToken,
		Aria2cArgs:             optAria2cArgs,
		Aria2cProxy:            optAria2cProxy,
		AddDfnSuffix:           optAddDfnSuffix,
		OnlyHevc:               optOnlyHevc,
		OnlyAvc:                optOnlyAvc,
		OnlyAv1:                optOnlyAv1,
		NoPaddingPageNum:       optNoPaddingPageNum,
		BandwidthAscending:     optBandwidthAscending,
		WorkDir:                optWorkDir,
		FFmpegPath:             optFFmpegPath,
		Mp4boxPath:             optMp4boxPath,
		Aria2cPath:             optAria2cPath,
		UposHost:               optUposHost,
		ForceReplaceHost:       optForceReplaceHost,
		SaveArchivesToFile:     optSaveArchives,
		DelayPerPage:           optDelayPerPage,
		MuxerTimeout:           optMuxerTimeout,
		RetryCount:             optRetryCount,
		RetryDelay:             optRetryDelay,
		ThreadSegmentSize:      optThreadSegmentSize,
		Host:                   optHost,
		EpHost:                 optEpHost,
		TvHost:                 optTvHost,
		Area:                   optArea,
		ConfigFile:             optConfigFile,
		ServeListenURL:         optServeListen,
		ServeMaxConcurrent:     optServeMaxConcurrent,
		ServeToken:             optServeToken,
	}
}

// buildHTTPClient creates an HTTPClient from CLI options.
func buildHTTPClient(cfg config.MyOption) *util.HTTPClient {
	client := util.NewHTTPClient(
		func() bool { return optInsecure || cfg.Insecure },
		func() string { return optCookie },
		func(format string, args ...interface{}) {
			if debug || cfg.Debug {
				util.LogDebug(format, args...)
			}
		},
	)
	if optUserAgent != "" {
		client.SetUserAgent(optUserAgent)
	}
	return client
}
