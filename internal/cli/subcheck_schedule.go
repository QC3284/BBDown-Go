package cli

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/QC3284/BBDown-Go/internal/config"
	"github.com/QC3284/BBDown-Go/internal/entity"
	"github.com/QC3284/BBDown-Go/internal/fetcher"
	"github.com/QC3284/BBDown-Go/internal/substore"
	"github.com/QC3284/BBDown-Go/internal/util"
	"github.com/QC3284/BBDown-Go/internal/workflow"
)

// ---- sub check 的调度：--since 增量窗口 + --concurrency 并发检查（docs/ROADMAP.md「订阅调度」）----
//
// 目标：让 sub check 能当 cron 周期任务跑——每次只看最近一段时间内发布的新内容（--since），
// 订阅多的时候并行做「检查」这一半（--concurrency）。不带这两个参数时行为与 2.12.8 逐字一致：
// 默认 Concurrency=1（串行检查）、Since=0（不做窗口过滤），检查与下载的日志顺序都不变。
//
// 分工（架构定案 ②）：只有**检查阶段**（resolve → fetch → 选出待下载的 aid）并行；
// 下载阶段仍严格串行、且按订阅顺序——进度条/SSE 的渲染竞态留到二期评估，
// 这里不动下载时序，是为了让「下载顺序 = 订阅顺序」这条可断言的性质保持在手。
//
// 本文件里的窗口判定与编排都做成纯函数/可注入依赖：真网用例既测不了并发顺序，
// 也测不了中断与「状态文件不半截」，所以判据必须能离线跑。

// ---- 清单去重：手改清单让同一 Target 出现多条时的健壮性缺口 ----

// dedupSubscriptions 按 Target 去重：保留**先出现**的那一条（含它的 Name/Filter/AddedAt），
// 其余重复项按原顺序作为 dups 返回（纯函数，离线可测）。
//
// 为什么必须去重：串行路径下重复 Target 是自愈的——前一条下载完就把 aid 写进该 Target 的历史，
// 后一条再检查时已经「没有新增内容」；而 --concurrency≥2 先把所有订阅的检查做完（都在历史为空
// 时拿到同一批新稿）、再逐个串行下载，于是同一批 aid 会被下载两遍（每个 aid 一次变两次，
// 白下 + 白占磁盘）。正常用法造不出重复（sub add 同 Target 是替换），但清单是用户可手改的
// JSON——qa 探针实测手改清单就能踩到。
func dedupSubscriptions(subs []substore.Subscription) (kept, dups []substore.Subscription) {
	seen := make(map[string]bool, len(subs))
	for _, sub := range subs {
		if seen[sub.Target] {
			dups = append(dups, sub)
			continue
		}
		seen[sub.Target] = true
		kept = append(kept, sub)
	}
	return kept, dups
}

// subCheckDeduped 去重并留一条点名日志，返回本次真正要调度的订阅。
// 无重复时原样返回且不打任何日志——「清单没毛病」的运行输出必须一字不改。
//
// 调用点在 runSubCheck 的 substore.Load() 之后、进入调度之前（见 runSubCheck 里的注释）。
func subCheckDeduped(subs []substore.Subscription) []substore.Subscription {
	kept, dups := dedupSubscriptions(subs)
	if len(dups) == 0 {
		return kept
	}
	targets := make([]string, 0, len(dups))
	for _, d := range dups {
		targets = append(targets, d.Target)
	}
	util.LogWarn("订阅清单里有 %d 条重复 Target（保留先出现的一条，重复项已跳过）: %s",
		len(dups), strings.Join(targets, ", "))
	return kept
}

// subCheckRunOptions 是 sub check 的两个「订阅增强」开关（t47，吸收上游 1.6.21/1.6.22）：
// 都为零值时行为与 2.15.2 逐字一致。
type subCheckRunOptions struct {
	// PerSubDir 打开时，每条订阅下到 <work-dir>/<订阅名>/（见 subCheckWorkDir）。
	PerSubDir bool
	// FullScan 打开时，mid: 订阅不做「整页都已下载就停止翻页」的提前结束。
	FullScan bool
	// SubDirs 是 PerSubDir 时预先规划好的 target → 目录名（substore.PlanSubDirs）。
	SubDirs map[string]string
}

// subCheckWorkDir 给出下载某条订阅时该用的工作目录：--per-sub-dir 关闭（plan 为空）时就是
// work-dir 原样；打开时是 <work-dir>/<规划出来的订阅名>。纯函数，离线可逐字断言目录布局。
func subCheckWorkDir(workDir string, plan map[string]string, sub substore.Subscription) string {
	if len(plan) == 0 {
		return workDir
	}
	name, ok := plan[sub.Target]
	if !ok || name == "" {
		return workDir
	}
	return filepath.Join(workDir, name)
}

// subCheckMaxConcurrency 是 --concurrency 的上限：检查阶段是若干次 B 站 API 调用，
// 并发再高只会给接口加压、更容易踩风控。8 足够把「订阅多、每条检查慢」的等待摊平。
const subCheckMaxConcurrency = 8

// subCheckSchedule 是校验后的调度参数。
type subCheckSchedule struct {
	// Since > 0：只下载发布时间落在窗口内的新内容（发布时间未知的不因此被过滤）。
	Since time.Duration
	// Concurrency 是检查阶段的并发订阅数（下载阶段恒为串行）。
	Concurrency int
	// DelayPerItem 是每个工作单元之间的停顿（t71，--delay-per-item）；0 = 不节流。
	// 工作单元 = 开始检查一个订阅 / 下载一个新增内容；列表翻页不算（请求量小两个数量级）。
	DelayPerItem time.Duration
}

// subCheckMaxDelayPerItem 是 --delay-per-item 的上限（秒）：再长就该用 cron 而不是干等。
const subCheckMaxDelayPerItem = 600

// parseDelayPerItem 校验 --delay-per-item（秒）：0~600，越界时错误里同时给出选项名与范围。
func parseDelayPerItem(seconds int) (time.Duration, error) {
	if seconds < 0 || seconds > subCheckMaxDelayPerItem {
		return 0, fmt.Errorf("--delay-per-item %d 无效：取值范围 0-%d 秒（默认 0 = 不节流）",
			seconds, subCheckMaxDelayPerItem)
	}
	return time.Duration(seconds) * time.Second, nil
}

// subCheckSleep 是停顿的实现：用例替换成假等待（不真睡，只记录事件与时长）。
//
// 必须感知 ctx：停顿期间被取消要**原样上抛** context.Canceled（不能把取消吞掉再继续下载）。
var subCheckSleep = func(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// subCheckDelayFromFlags 把 CLI 的 --delay-per-item 读成 duration。
//
// 为什么在这里读包级 flag 变量：flag→schedule 的桥接点本该在 runSubCheck（commands.go），
// 而那个文件不在本任务的 inScope 里。等那条 scope 打开后，把这一行挪回 commands.go 即可，
// 判定/节流逻辑本身不动。
func subCheckDelayFromFlags() time.Duration {
	d, err := parseDelayPerItem(optSubCheckDelayPerItem)
	if err != nil {
		return 0 // 非法值由 PreRunE 先拦住；这里只做兜底
	}
	return d
}

// subCheckPacer 实现 --delay-per-item：**首个工作单元不等待**，之后每个单元前停顿 delay。
//
// 为什么首个不等：节流是为了把连发摊开，不是给第一个动作额外加延迟——cron 场景下第一个
// 单元本来就在用户预期的时刻开始（上游 1.7.7 同语义）。
type subCheckPacer struct {
	delay  time.Duration
	waited bool
}

// wait 在该工作单元开始前调用；返回的错误原样上抛（取消语义由此保住）。
func (p *subCheckPacer) wait(ctx context.Context) error {
	if p.delay <= 0 {
		return nil
	}
	if !p.waited {
		p.waited = true
		return nil
	}
	return subCheckSleep(ctx, p.delay)
}

// parseSubCheckSchedule 校验 --since / --concurrency（纯函数，离线可测）。
//
// 刻意把校验放在 runSubCheck 的**最前面**：参数打错时必须当场报错（退出码非 0），
// 且不读、不写任何状态文件——既不隔离「损坏的清单」，也不触发任何网络请求。
//
// --since 用 Go duration 语法（time.ParseDuration）：24h / 30m 可以，1d 不行——
// 解析失败时把「不支持 d、一天写 24h」写进错误里，否则用户照直觉写 1d 只会看到一句
// 英文的 parse 错误。非正值（"0" / "-1h"）同样报错而不是静默当成「不启用」：
// 静默吞掉用户显式给出的窗口，比报错更难发现。
func parseSubCheckSchedule(sinceRaw string, concurrency int) (subCheckSchedule, error) {
	var sched subCheckSchedule
	if raw := strings.TrimSpace(sinceRaw); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return sched, fmt.Errorf("--since %q 无效：%v。请用 Go duration 语法（如 24h / 30m）；不支持天数单位 \"d\"，一天请写 24h", raw, err)
		}
		if d <= 0 {
			return sched, fmt.Errorf("--since %q 无效：窗口必须是正的 duration（如 24h / 30m）", raw)
		}
		sched.Since = d
	}
	if concurrency < 1 || concurrency > subCheckMaxConcurrency {
		return sched, fmt.Errorf("--concurrency %d 无效：取值范围 1-%d（默认 1：串行检查）", concurrency, subCheckMaxConcurrency)
	}
	sched.Concurrency = concurrency
	return sched, nil
}

// subPubTimes 收集 aid → 发布时间：同一 aid 出现在多个分P 时取第一个非零值
// （某个分P 缺 PubTime 不代表这稿的发布时间未知）。
//
// 单独抽出来是为了让窗口过滤成为纯函数：输入只有 aid 列表与这张表，输出可以逐条断言。
func subPubTimes(pages []entity.Page) map[string]int64 {
	out := make(map[string]int64, len(pages))
	for _, p := range pages {
		if p.Aid == "" {
			continue
		}
		prev, seen := out[p.Aid]
		if !seen {
			out[p.Aid] = p.PubTime
			continue
		}
		if prev <= 0 && p.PubTime > 0 {
			out[p.Aid] = p.PubTime
		}
	}
	return out
}

// subSinceFilter 是 --since 的增量窗口判定（纯函数：since 与 now 都由调用方传入，离线可测）。
//
// 规则（架构定案 ①）：
//   - PubTime > 0 且 now-PubTime > since → 窗口外，进 skipped（不下载、也不进历史，
//     窗口放宽后还能补下）；
//   - now-PubTime == since（恰落在边界上）→ 视为窗口内，留在 kept；
//   - PubTime <= 0（发布时间未知）→ **不因 since 过滤**，同样留在 kept：把未知当成「太旧」
//     会静默漏掉新稿，当成「太新」又会破坏 --since 的语义，只能放行并说明。
//
// 返回的 kept 保持 aids 的原顺序（下载顺序 = 稿件顺序）；unknown 是 kept 里「发布时间未知」
// 那一部分的副本，单独返回只是为了让调用方留一条说明性日志（不是「被过滤掉」的集合）。
func subSinceFilter(aids []string, pubTimes map[string]int64, since time.Duration, now time.Time) (kept, skipped, unknown []string) {
	for _, aid := range aids {
		pub, ok := pubTimes[aid]
		if !ok || pub <= 0 {
			unknown = append(unknown, aid)
			kept = append(kept, aid)
			continue
		}
		if now.Sub(time.Unix(pub, 0)) > since {
			skipped = append(skipped, aid)
			continue
		}
		kept = append(kept, aid)
	}
	return kept, skipped, unknown
}

// subCheckDeps 是 sub check 三段外部依赖：解析目标 → 拉取稿件 → 下载单个 aid。
//
// 抽成可注入的三段，是为了让「并发检查 + 串行下载」的编排能被离线用例断言。
type subCheckDeps struct {
	resolve  func(ctx context.Context, target string) (string, error)
	fetch    func(ctx context.Context, resolved string) (*entity.VInfo, error)
	download func(ctx context.Context, aid string) error

	// fetchResolved / downloadInto 是 t47 新增的**可选**依赖：非 nil 时优先于 fetch / download。
	// 之所以加字段而不是改旧字段的签名，是为了让既有离线用例（只填旧三样）逐字不用改——
	// 默认路径（不开 --per-sub-dir）仍然走 download，行为与 2.15.2 一致。
	//
	// fetchResolved 需要知道是哪条订阅：mid: 订阅的增量扫描要用该订阅自己的历史判「整页都已下载」。
	fetchResolved func(ctx context.Context, sub substore.Subscription, resolved string) (*entity.VInfo, error)
	downloadInto  func(ctx context.Context, sub substore.Subscription, aid string) error
}

// subCheckFetcher/fetchOne 选依赖：优先新字段，回落到旧字段。
func (d subCheckDeps) fetchOne(ctx context.Context, sub substore.Subscription, resolved string) (*entity.VInfo, error) {
	if d.fetchResolved != nil {
		return d.fetchResolved(ctx, sub, resolved)
	}
	return d.fetch(ctx, resolved)
}

func (d subCheckDeps) downloadOne(ctx context.Context, sub substore.Subscription, aid string) error {
	if d.downloadInto != nil {
		return d.downloadInto(ctx, sub, aid)
	}
	return d.download(ctx, aid)
}

// defaultSubCheckDeps 装配真实依赖。
//
// 并发安全依据（架构定案 ④）：并发检查阶段**共享**同一个 client / factory / wbi，不做每 worker
// 一份复制，理由是这三样在检查阶段都是只读的：
//   - util.HTTPClient：唯一的可变字段是自动 UA，读写都在 uaMu（sync.RWMutex）之下
//     （internal/util/http.go 的 rotateAutomaticUserAgent/currentUserAgent）；服务端时钟偏移是
//     atomic.Int64（internal/util/clock.go）；retries / cookieFn / credentialHosts 只在命令启动
//     阶段设置一次，检查阶段不再写；
//   - fetcher.Factory：字段在 NewFactory 之后只被读，Create 每次返回一个新的 Fetcher
//     （每个 Fetcher 自己的状态都是 per-实例的，不跨订阅共享）；
//   - wbi 是 string（不可变）。
//
// 因此并发调用同一份 client/factory/wbi 是安全的；TestSubCheckSharedClientIsConcurrencySafe
// 在 -race 下并发打同一个 client 钉住这条依据（含并发轮换 UA）。
func defaultSubCheckDeps(cfg config.MyOption, client *util.HTTPClient, factory *fetcher.Factory, wbi string) subCheckDeps {
	// 旧签名保留（既有用例与其它调用点不用改）：零选项 = 与 2.15.2 一致的行为。
	// 注意增量扫描是**默认**行为（只有 --full-scan 能关掉提前结束），所以真实链路请走
	// defaultSubCheckDepsWith（runSubCheck 就是这么调的）。
	return defaultSubCheckDepsWith(cfg, client, factory, wbi, subCheckRunOptions{})
}

// defaultSubCheckDepsWith 装配带订阅增强选项的依赖。
func defaultSubCheckDepsWith(cfg config.MyOption, client *util.HTTPClient, factory *fetcher.Factory, wbi string, opts subCheckRunOptions) subCheckDeps {
	deps := subCheckDeps{
		resolve: func(ctx context.Context, target string) (string, error) {
			return workflow.ResolveURL(ctx, client, target)
		},
		fetch: func(ctx context.Context, resolved string) (*entity.VInfo, error) {
			return factory.Create(resolved).Fetch(ctx, resolved)
		},
		download: func(ctx context.Context, aid string) error {
			opt := config.DefaultMyOption()
			opt.URL = "av" + aid
			opt.Cookie = cfg.Cookie
			opt.AccessToken = cfg.AccessToken
			opt.EncodingPriority = optEncodingPriority
			opt.DfnPriority = optDfnPriority
			opt.UseAppAPI = cfg.UseAppAPI
			opt.UseTvAPI = cfg.UseTvAPI
			opt.UseIntlAPI = cfg.UseIntlAPI
			opt.WorkDir = cfg.WorkDir
			opt.Wbi = wbi
			return workflow.New(opt, client).Run(ctx)
		},
	}

	// 增量扫描（默认）：mid: 订阅改成「轻量列举（只取 aid/标题/封面/发布时间）+ 整页都已下载
	// 就停止翻页」，不再逐投稿发详情请求展开分P——每个新 aid 的下载本来就会各自解析。
	// 非 mid: 目标原样走旧的 fetch（byte 级不变）。
	deps.fetchResolved = func(ctx context.Context, sub substore.Subscription, resolved string) (*entity.VInfo, error) {
		if !strings.HasPrefix(resolved, "mid:") {
			return deps.fetch(ctx, resolved)
		}
		history, err := substore.LoadHistory(sub.Target)
		if err != nil {
			return nil, err
		}
		downloaded := make(map[string]bool, len(history))
		for _, aid := range history {
			downloaded[aid] = true
		}
		space := fetcher.NewSpaceVideoFetcher(client, wbi, cfg.Cookie, fetcher.SpaceScanOptions{
			Incremental: true,
			FullScan:    opts.FullScan,
			Downloaded:  func(aid string) bool { return downloaded[aid] },
		})
		return space.Fetch(ctx, resolved)
	}

	// --per-sub-dir：每条订阅换一个工作目录。关闭时不填这个字段，下载路径与改前逐字一致。
	if opts.PerSubDir {
		deps.downloadInto = func(ctx context.Context, sub substore.Subscription, aid string) error {
			opt := config.DefaultMyOption()
			opt.URL = "av" + aid
			opt.Cookie = cfg.Cookie
			opt.AccessToken = cfg.AccessToken
			opt.EncodingPriority = optEncodingPriority
			opt.DfnPriority = optDfnPriority
			opt.UseAppAPI = cfg.UseAppAPI
			opt.UseTvAPI = cfg.UseTvAPI
			opt.UseIntlAPI = cfg.UseIntlAPI
			opt.WorkDir = subCheckWorkDir(cfg.WorkDir, opts.SubDirs, sub)
			opt.Wbi = wbi
			return workflow.New(opt, client).Run(ctx)
		}
	}
	return deps
}

// subCheckOutcomeKind 是检查阶段对单个订阅的结论。
type subCheckOutcomeKind int

const (
	// subCheckNew：窗口内有新内容，可以下载。
	subCheckNew subCheckOutcomeKind = iota
	// subCheckNoNew：没有新内容（或新内容都被窗口/标题过滤掉了）。
	subCheckNoNew
	// subCheckResolveFailed：目标解析失败（跳过该订阅）。
	subCheckResolveFailed
	// subCheckResolvedEmpty：解析结果为空（静默跳过，与改前一致）。
	subCheckResolvedEmpty
	// subCheckFetchFailed：拉取稿件信息失败（跳过该订阅）。
	subCheckFetchFailed
	// subCheckFilterInvalid：订阅的标题过滤正则非法（跳过该订阅）。
	subCheckFilterInvalid
	// subCheckHistoryFailed：读历史失败（中止整个命令：历史不可信，不能接着下）。
	subCheckHistoryFailed
	// subCheckCancelled：ctx 已取消，该订阅的检查没有启动。
	subCheckCancelled
)

// subCheckOutcome 是一个订阅的检查结论。并发 worker **只产出结论、不打印任何日志**：
// 日志统一由调用方按订阅顺序汇报，并发检查的输出顺序因此仍是确定的（订阅顺序）。
type subCheckOutcome struct {
	sub     substore.Subscription
	kind    subCheckOutcomeKind
	err     error
	title   string
	newAids []string
	// skipped / unknown 是 --since 判定里「窗口外」与「发布时间未知」的 aid（只在启用 --since 时非空）。
	skipped []string
	unknown []string
}

// subCheckOne 跑一个订阅的检查阶段：resolve → fetch → --since 窗口过滤 → 历史/标题过滤。
func subCheckOne(ctx context.Context, deps subCheckDeps, sub substore.Subscription, sched subCheckSchedule, now time.Time) subCheckOutcome {
	oc := subCheckOutcome{sub: sub, kind: subCheckNoNew}
	resolved, err := deps.resolve(ctx, sub.Target)
	if err != nil {
		oc.kind, oc.err = subCheckResolveFailed, err
		return oc
	}
	if resolved == "" {
		oc.kind = subCheckResolvedEmpty
		return oc
	}
	vInfo, err := deps.fetchOne(ctx, sub, resolved)
	if err != nil {
		oc.kind, oc.err = subCheckFetchFailed, err
		// 增量扫描要在翻页前读历史（用它判断「整页都已下载」），历史损坏时这个错误从
		// fetch 路径冒出来——必须还原成 history-failed 语义（中止整个命令，而不是把这条
		// 订阅当作「拉取失败」跳过：历史不可信时继续下会重复下载）。
		var corrupt *substore.CorruptError
		if errors.As(err, &corrupt) {
			oc.kind = subCheckHistoryFailed
		}
		return oc
	}
	oc.title = vInfo.Title

	var allAids []string
	seen := make(map[string]bool)
	for _, p := range vInfo.PagesInfo {
		if p.Aid != "" && !seen[p.Aid] {
			seen[p.Aid] = true
			allAids = append(allAids, p.Aid)
		}
	}
	if sched.Since > 0 {
		allAids, oc.skipped, oc.unknown = subSinceFilter(allAids, subPubTimes(vInfo.PagesInfo), sched.Since, now)
	}

	history, err := substore.LoadHistory(sub.Target)
	if err != nil {
		oc.kind, oc.err = subCheckHistoryFailed, err
		return oc
	}
	newAids, err := subNewAids(sub, vInfo.Title, allAids, history)
	if err != nil {
		oc.kind, oc.err = subCheckFilterInvalid, err
		return oc
	}
	if len(newAids) == 0 {
		oc.kind = subCheckNoNew
		return oc
	}
	oc.kind, oc.newAids = subCheckNew, newAids
	return oc
}

// subCheckChecks 跑**并发**检查阶段，结果按订阅顺序放在 outcomes[i]（每个订阅恰好一个槽位）。
// 它不打印任何日志：调用方拿到结论后按订阅顺序统一汇报。worker 只写自己的槽位（不同下标，
// 无需加锁），ctx 取消后不再启动新 worker。
//
// 返回前等所有 worker 退出（sync.WaitGroup）：ctx 取消后不留仍在读文件/发请求的协程，
// 「中断后状态文件不半截」由此成立——写状态文件的只有随后串行的下载阶段。
//
// 串行（--concurrency 1，默认）不走这里：那一路在 subCheckRun 里逐订阅交错执行，
// 好让「检查订阅」日志与 fetcher/下载层自己的日志保持改前的先后关系。
func subCheckChecks(ctx context.Context, subs []substore.Subscription, deps subCheckDeps, sched subCheckSchedule, now time.Time) []subCheckOutcome {
	outcomes := make([]subCheckOutcome, len(subs))
	capacity := sched.Concurrency
	if capacity < 1 {
		capacity = 1
	}

	sem := make(chan struct{}, capacity)
	var wg sync.WaitGroup
	for i := range subs {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			outcomes[i] = subCheckOutcome{sub: subs[i], kind: subCheckCancelled, err: ctx.Err()}
			continue
		}
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			outcomes[i] = subCheckOne(ctx, deps, subs[i], sched, now)
		}(i)
	}
	wg.Wait()
	return outcomes
}

// subCheckReportOne 按检查结论汇报（日志）并串行下载新内容。
// failures 累计下载失败数；返回非 nil 表示整体中止（读历史失败 / 写历史失败）。
//
// 注意下载失败**不**中止：与改前一致，继续下一个 aid、最后用退出码汇总。
// RecordDownloaded 只在下载成功之后调用——中断时状态文件因此不会记进没下完的 aid。
func subCheckReportOne(ctx context.Context, deps subCheckDeps, oc subCheckOutcome, sched subCheckSchedule, failures *int, pacer *subCheckPacer) error {
	switch oc.kind {
	case subCheckCancelled, subCheckResolvedEmpty:
		return nil
	case subCheckResolveFailed:
		util.LogWarn("订阅解析失败（跳过）: %v", oc.err)
		return nil
	case subCheckFetchFailed:
		util.LogWarn("订阅拉取失败（跳过）: %v", oc.err)
		return nil
	case subCheckFilterInvalid:
		util.LogWarn("订阅 %s 的过滤条件无效（跳过）: %v", oc.sub.Name, oc.err)
		return nil
	case subCheckHistoryFailed:
		return oc.err
	}

	if sched.Since > 0 {
		if len(oc.skipped) > 0 {
			util.Log("  跳过 %d 个超出 --since %s 窗口的内容: %s", len(oc.skipped), sched.Since, joinAids(oc.skipped))
		}
		if len(oc.unknown) > 0 {
			util.LogWarn("  %d 个内容的发布时间未知，不按 --since 过滤: %s", len(oc.unknown), joinAids(oc.unknown))
		}
	}
	if len(oc.newAids) == 0 {
		if oc.sub.Filter != "" {
			util.Log("  没有匹配过滤 %q 的新内容（稿件标题: %s）", oc.sub.Filter, oc.title)
		} else {
			util.Log("  没有新增内容")
		}
		return nil
	}
	util.Log("  发现 %d 个新内容: %s", len(oc.newAids), joinAids(oc.newAids))
	for _, aid := range oc.newAids {
		// t71：每个「下载一个新增内容」也是一个工作单元，前面同样停顿（首个单元除外）。
		if err := pacer.wait(ctx); err != nil {
			return err
		}
		if err := deps.downloadOne(ctx, oc.sub, aid); err != nil {
			util.LogWarn("av%s 下载失败: %v", aid, err)
			*failures = *failures + 1
			continue
		}
		if err := substore.RecordDownloaded(oc.sub.Target, aid); err != nil {
			return err
		}
	}
	return nil
}

// subCheckRun 是 sub check 的编排核心：检查阶段（可并发）+ 按订阅顺序的串行下载阶段。
// 它不碰 cobra、不装信号、不建会话——外部依赖全经 deps 传入，离线用例可以整条跑通。
//
// 返回值：failures 是下载失败的视频数；err 为 nil 表示正常跑完（含「跑完时 ctx 已被取消」），
// 为 context.Canceled 表示在订阅边界上被取消（与改前 return silenceOnCancel(cmd, ctx.Err())
// 同一语义），其它值表示整体中止。
func subCheckRun(ctx context.Context, subs []substore.Subscription, deps subCheckDeps, sched subCheckSchedule, now time.Time) (failures int, err error) {
	// --delay-per-item 的桥接（见 subCheckDelayFromFlags 的注释）：显式 0 与未设置等价，
	// 都是「不节流」，所以这里统一兜底。
	if sched.DelayPerItem <= 0 {
		sched.DelayPerItem = subCheckDelayFromFlags()
	}
	pacer := &subCheckPacer{delay: sched.DelayPerItem}

	if sched.Concurrency <= 1 {
		// 串行路径（默认）：检查与下载逐订阅交错，日志顺序与改前逐字一致。
		// 这里的顺序是承重的：fetcher 与下载层自己也会打日志（「共 N 个投稿, 正在获取分P信息...」、
		// 任务卡、进度条收尾），「先报订阅名、再解析拉取、再下载」这条先后关系对用户可见，
		// 不能图省事改成「先全部检查、再统一汇报」。
		for _, sub := range subs {
			if ctx.Err() != nil {
				return failures, context.Canceled
			}
			// t71：串行路径下「开始检查一个订阅」也是工作单元——无新增内容时相邻订阅之间
			// 同样要停顿（首检大 UP 之后的那几次检查同样在风控窗口里）。
			if err := pacer.wait(ctx); err != nil {
				return failures, err
			}
			util.Log("检查订阅: %s (%s)", sub.Name, sub.Target)
			oc := subCheckOne(ctx, deps, sub, sched, now)
			if err := subCheckReportOne(ctx, deps, oc, sched, &failures, pacer); err != nil {
				return failures, err
			}
		}
		util.Log("订阅检查完成")
		return failures, nil
	}

	// 并发路径：先把检查阶段并行跑完（worker 不打印日志），再按订阅顺序汇报并串行下载。
	util.Log("并行检查 %d 个订阅（--concurrency %d，下载仍按订阅顺序串行）...", len(subs), sched.Concurrency)
	for _, oc := range subCheckChecks(ctx, subs, deps, sched, now) {
		if ctx.Err() != nil {
			// 与改前一致：取消后不再汇报剩余订阅，交给调用方的取消路径（silenceOnCancel）。
			return failures, context.Canceled
		}
		if oc.kind == subCheckCancelled {
			continue
		}
		util.Log("检查订阅: %s (%s)", oc.sub.Name, oc.sub.Target)
		// 并发路径只节流**下载**：检查阶段本来就是并行跑的，把它们的启动串行排队会直接
		// 抵消 --concurrency（见 subCheckChecks 的注释）。
		if err := subCheckReportOne(ctx, deps, oc, sched, &failures, pacer); err != nil {
			return failures, err
		}
	}
	util.Log("订阅检查完成")
	return failures, nil
}
