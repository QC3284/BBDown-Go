package download

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QC3284/BBDown-Go/internal/entity"
	"github.com/QC3284/BBDown-Go/internal/util"
)

// DownloadConfig holds download options (mirrors upstream MyOption semantics).
type DownloadConfig struct {
	UseAria2c     bool
	Aria2cArgs    string
	Aria2cPath    string
	ForceHTTP     bool
	MultiThread   bool
	SegmentSizeMB int
	RetryCount    int
	RetryDelayMs  int
	Cookie        string
	// UserAgent 留空时取 HTTPClient 的 UA（显式 --user-agent 或进程级随机默认）。
	UserAgent string
	Client    *util.HTTPClient
	// FallbackURLs 是主地址之后的候选地址链（按序回退）：host 被强制替换
	// （--force-replace-host / --upos-host）之前的原地址，以及 playurl 里同一条轨道的
	// backup_url。首个 404 或连接失败就换下一个候选，而不是对同一个死地址重试满 retryCount
	// 次（本仓有意差异，见 §4.33）。地址来源全部数据驱动，没有新增 CLI 开关。
	FallbackURLs []string
	// ProgressCursor 是进度行行首的分P游标（如 "P1/8"）：编排层知道「第几P/共几P」，
	// 下载层只负责渲染；空串 = 单P任务，行首不显示游标段（默认值，行为与改前一致）。
	ProgressCursor string
}

// httpStatusError 携带 HTTP 状态码：回退原地址只针对 404，字符串匹配既脆又会被脱敏后的
// URL 干扰。Error() 文本由 newHTTPStatusError 生成：非 412 与改前逐字相同（既有日志与用例
// 不受影响），412 追加风控提示。
type httpStatusError struct {
	code int
	msg  string
}

func (e *httpStatusError) Error() string { return e.msg }

// newHTTPStatusError 构造带状态码的下载错误。412（B 站风控）除了裸状态码还要告诉用户
// 「等一会儿 / 换网络出口」——提示语单一来源：util.StatusHint，与 API 层同一份文案
// （此前下载层只报「download failed: HTTP 412」，用户不知道该等还是该换出口）。
// 其余状态码 util.StatusHint 返回空串，消息与改前逐字相同；404 的换候选只看 code，不受影响。
func newHTTPStatusError(code int, prefix string) *httpStatusError {
	return &httpStatusError{
		code: code,
		msg:  fmt.Sprintf("%s: HTTP %d%s", prefix, code, util.StatusHint(code)),
	}
}

// maxTrackAttempts 是单个文件的尝试次数硬上限：候选链再长也不能把失败路径变成请求风暴
// （畸形响应可以塞进任意多个候选地址）。
const maxTrackAttempts = 8

// candidateChain 返回本次下载的候选链：主地址在前，FallbackURLs 依次在后（跳过空项、去掉重复）。
// 链上的地址指向同一个对象的多个镜像，按序尝试；--force-http 对候选与主地址一视同仁，否则
// 回退之后又把 https 带回来了。
func (c DownloadConfig) candidateChain(primary string) []string {
	chain := make([]string, 0, 1+len(c.FallbackURLs))
	add := func(u string) {
		if u == "" {
			return
		}
		u = forceHTTPIfNeeded(u, c.ForceHTTP)
		for _, seen := range chain {
			if seen == u {
				return
			}
		}
		chain = append(chain, u)
	}
	add(primary)
	for _, u := range c.FallbackURLs {
		add(u)
	}
	return chain
}

// candidateAttempts 返回单文件的尝试次数：每个候选至少出场一次，总数不少于 --retry-count。
// 没有候选时与改前完全一致（默认 3 次），9 次重试阶梯（页面级 3 × 轨道级 3）的语义不变——
// 候选只是把失败的那几次换成「下一个地址」，不会在同一个地址上叠加次数。
func candidateAttempts(chain []string, retryCount int) int {
	n := retryCount
	if len(chain) > n {
		n = len(chain)
	}
	if n > maxTrackAttempts {
		n = maxTrackAttempts
	}
	if n < 1 {
		n = 1
	}
	return n
}

// 「要不要换候选、换之前退多久」此前由 candidateAdvanceable/advanceCandidate 各自判断；
// 现在统一由 planTrackRetry（见 retry_policy.go）按错误分类给出，单线程与分片两条循环共用。
//
// candidateSwitchReason 给出换候选的用户可见原因（404 与连接失败的说法不同）。
func candidateSwitchReason(err error) string {
	var se *httpStatusError
	if errors.As(err, &se) && se.code == http.StatusNotFound {
		return "目标返回 404（镜像可能未覆盖该对象）"
	}
	return fmt.Sprintf("连接失败（%v）", err)
}

// userAgent 返回本次下载使用的 UA。
//
// 上游 HTTPUtil.GetUserAgent(null) 的优先级：显式值 → 进程默认随机 UA。本仓把 --user-agent
// 写在 HTTPClient 上（SetUserAgent），这里直接取它——此前下载路径写死 "Mozilla/5.0"，
// 用户设了 --user-agent 也只影响 API 请求，媒体下载仍带着这个极易被 CDN 识别的裸 UA。
func (c DownloadConfig) userAgent() string {
	if c.UserAgent != "" {
		return c.UserAgent
	}
	if c.Client != nil {
		if ua := c.Client.UserAgent(); ua != "" {
			return ua
		}
	}
	return util.RandomUserAgent()
}

// planSegmentBytes 规划分片大小：显式指定 --thread-segment-size 时按用户值，未指定（0）时按
// 「分片数 ≈ 并发上限」倒推。
//
// 旧行为是固定 20MB：40MB 文件只切 2 片，而并发上限是 8——中等大小文件白白浪费 6 个连接。
// 下界 1MB 避免小文件切出上百片，上界 20MB 与旧默认一致，避免大文件分片过多。
// 多线程的**触发门槛**仍由 segmentSize()（默认 20MB）决定，与分片大小解耦。
func planSegmentBytes(size int64, cfg DownloadConfig) int64 {
	const minSeg = int64(1) << 20
	const maxSeg = int64(20) << 20
	if cfg.SegmentSizeMB > 0 {
		if seg := int64(cfg.SegmentSizeMB) << 20; seg > 0 {
			return seg
		}
	}
	parallel := int64(maxConcurrentClips())
	if parallel < 1 {
		parallel = 1
	}
	seg := size / parallel
	if seg < minSeg {
		seg = minSeg
	}
	if seg > maxSeg {
		seg = maxSeg
	}
	return seg
}

// maxConcurrentClips caps parallel range requests per file (upstream uses
// MaxDegreeOfParallelism = min(8, max(1, CPU count))).
func maxConcurrentClips() int {
	n := runtime.NumCPU()
	if n < 1 {
		n = 1
	}
	if n > 8 {
		n = 8
	}
	return n
}

func (c DownloadConfig) segmentSize() int {
	if c.SegmentSizeMB < 1 {
		return 20
	}
	return c.SegmentSizeMB
}

func (c DownloadConfig) retryCount() int {
	if c.RetryCount < 1 {
		return 3
	}
	return c.RetryCount
}

func (c DownloadConfig) retryDelay() time.Duration {
	if c.RetryDelayMs < 0 {
		return 3 * time.Second
	}
	return time.Duration(c.RetryDelayMs) * time.Millisecond
}

// ErrRangeNotSupported signals that the server ignored a Range request header;
// the caller falls back to single-threaded download (upstream NotSupportedException).
var ErrRangeNotSupported = fmt.Errorf("服务器不支持 Range 请求")

// probeResult is the outcome of a HEAD probe of a download URL.
type probeResult struct {
	size         int64
	ranges       bool
	etag         string
	lastModified string
}

// resumeManifest 是 .tmp 的旁车清单（上游 ResumeManifest）：身份存**稳定身份**（剥离会刷新的
// 签名参数），不是完整签名 URL——媒体 URL 的 deadline/sign 每次请求都刷新，用完整 URL 比较
// 会让同一资源永远无法跨进程续传（上游 v1.6.20 StableResourceIdentity）。URL 字段兼容旧清单。
type resumeManifest struct {
	Identity     string `json:"identity"`
	URL          string `json:"url,omitempty"`
	Size         int64  `json:"size"`
	ETag         string `json:"etag,omitempty"`
	LastModified string `json:"last_modified,omitempty"`
}

// signatureQueryKeys 是每次请求都会刷新的 query 参数（上游 SignatureQueryKeys）：
// 它们不构成资源身份，比较续传清单时必须剥离。
var signatureQueryKeys = map[string]bool{
	"deadline": true, "sign": true, "w_rid": true, "wts": true, "ts": true,
	"expires": true, "auth_key": true, "ok": true, "ok_av": true, "oid": true,
	"trid": true, "platform": true, "fnval": true, "fnver": true, "fourk": true,
	"type": true, "uparams": true, "t": true,
}

// stableResourceIdentity 返回剥离签名参数后的稳定资源身份：路径 + 非签名参数（按键名排序）。
func stableResourceIdentity(rawURL string) string {
	i := strings.Index(rawURL, "?")
	if i < 0 {
		return rawURL
	}
	path, query := rawURL[:i], rawURL[i+1:]
	kept := make([]string, 0, 8)
	for _, pair := range strings.Split(query, "&") {
		if pair == "" {
			continue
		}
		key := pair
		if eq := strings.Index(pair, "="); eq > 0 {
			key = pair[:eq]
		}
		if signatureQueryKeys[strings.ToLower(key)] {
			continue
		}
		kept = append(kept, pair)
	}
	sort.Strings(kept)
	if len(kept) == 0 {
		return path
	}
	return path + "?" + strings.Join(kept, "&")
}

// manifestIdentity 取清单记录的身份；旧清单只存了完整 URL 时按稳定身份回退计算。
func manifestIdentity(m resumeManifest) string {
	if m.Identity != "" {
		return m.Identity
	}
	if m.URL != "" {
		return stableResourceIdentity(m.URL)
	}
	return ""
}

// manifestMatchesProbe 判断清单是否仍代表当前探测到的资源（上游 CanResumeFromAsync）：
// 稳定身份与总长必须一致；ETag/Last-Modified 只在**双方都有**时才比较——有些 CDN 不返回，
// 单边缺失时退化为「身份 + 长度」判断。
func manifestMatchesProbe(m resumeManifest, identity string, size int64, etag, lastModified string) bool {
	if manifestIdentity(m) != identity {
		return false
	}
	if m.Size != size {
		return false
	}
	if etag != "" && m.ETag != "" && m.ETag != etag {
		return false
	}
	if lastModified != "" && m.LastModified != "" && !strings.EqualFold(m.LastModified, lastModified) {
		return false
	}
	return true
}

// DownloadFile downloads a URL to a local file, with optional multi-threading,
// resume support and retries (mirrors upstream BBDownDownloadUtil).
// aria2cHardTimeout is the outer ceiling for one aria2c invocation.
const aria2cHardTimeout = 6 * time.Hour

// downloadStallTimeout bounds how long a media download may go without
// receiving a single byte. It is a variable so tests can shrink it.
var downloadStallTimeout = 60 * time.Second

// stallGuard aborts a body read that has stopped producing data. Media
// downloads use a client without an overall timeout (a large file legitimately
// takes minutes), so a connection that neither resets nor EOFs — a network
// black hole — blocked io.Copy forever and hung the task. Closing the body is
// what unblocks the pending Read.
type stallGuard struct {
	body  io.ReadCloser
	timer *time.Timer
}

func newStallGuard(body io.ReadCloser, d time.Duration) *stallGuard {
	g := &stallGuard{body: body}
	g.timer = time.AfterFunc(d, func() { body.Close() })
	return g
}

func (g *stallGuard) Read(p []byte) (int, error) {
	n, err := g.body.Read(p)
	if n > 0 {
		g.timer.Reset(downloadStallTimeout)
	}
	return n, err
}

// Stop releases the watchdog; the underlying body keeps its own Close.
func (g *stallGuard) Stop() { g.timer.Stop() }

// aria2ControlPath 返回 aria2c 为某个目标文件留下的控制文件路径（默认 <文件>.aria2）。
func aria2ControlPath(destPath string) string { return destPath + ".aria2" }

// hasAria2ControlFile 报告目标文件旁边是否还有 aria2c 控制文件（= 上一次没下完）。
func hasAria2ControlFile(destPath string) bool {
	_, err := os.Stat(aria2ControlPath(destPath))
	return err == nil
}

// existingDownloadComplete 判定「磁盘上已有完整文件，可以跳过下载」。
//
// 大小相等只是**必要条件**：aria2c 用「预分配 + 控制文件」续传，中断时磁盘上的文件可能已经
// 等于远端总长，只有 <文件>.aria2 还在才说明它是残的。只看大小会把残件当完整件跳过，
// 后面混流就会拿半个视频去 mux（上游 1.7.4 修的正是这个）。控制文件**保留**给 aria2c 续传。
func existingDownloadComplete(destPath string, remoteSize int64) bool {
	if remoteSize <= 0 {
		return false
	}
	info, err := os.Stat(destPath)
	if err != nil || info.Size() != remoteSize {
		return false
	}
	return !hasAria2ControlFile(destPath)
}

// cleanupResidue 在「产物已完整、跳过下载」时回收各模式遗留的中间文件（t89，t82 评估 ③）。
//
// 删：
//   - dest.tmp（单线程下载的断点文件）；
//   - dest.tmp.meta（单线程续传清单，singleDownload 的 metaPath = tmp + ".meta"）；
//   - dest.NNN.vclip（多线程分片，clipPath 的命名，仅匹配「数字+vclip」形态）；
//   - dest.dec（DRM 解密的唯一临时件）；
//   - dest.segs（live 分段根目录）——**仅当它是空目录**。
//
// 不删：
//   - dest.aria2：它的存在本身就说明「上次没下完」，existingDownloadComplete 会判未完成，
//     这个分支根本走不到；留着给 aria2c 续传（t68）；
//   - live 的会话目录 dest.segs/session-*：里面有分段，可能是用户唯一的恢复副本
//     （live.go 的 staleSessions 只告警不删，这里保持一致——只在根目录空了才收掉它）。
func cleanupResidue(destPath string) {
	removeIfExists := func(p string) {
		if err := os.Remove(p); err == nil {
			util.LogDebug("已清理残留: %s", p)
		}
	}
	removeIfExists(destPath + ".tmp")
	removeIfExists(destPath + ".tmp.meta")
	removeIfExists(destPath + ".dec")

	// 多线程分片：精确匹配 <base>.<数字>.vclip，避免误删同目录下的同名兄弟文件。
	dir := filepath.Dir(destPath)
	base := filepath.Base(destPath)
	if entries, err := os.ReadDir(dir); err == nil {
		prefix := base + "."
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, ".vclip") {
				continue
			}
			mid := strings.TrimSuffix(strings.TrimPrefix(name, prefix), ".vclip")
			if mid == "" {
				continue
			}
			digits := true
			for _, r := range mid {
				if r < '0' || r > '9' {
					digits = false
					break
				}
			}
			if digits {
				removeIfExists(filepath.Join(dir, name))
			}
		}
	}

	// live 分段根目录：只在空目录时收掉（会话目录里的分段不碰）。
	if entries, err := os.ReadDir(destPath + ".segs"); err == nil && len(entries) == 0 {
		removeIfExists(destPath + ".segs")
	}
}

// removeStaleAria2Control 在**非 aria2c** 路径成功落盘之后清掉遗留的 aria2 控制文件。
//
// 为什么要清：控制文件是「还有东西可以续」的凭证。文件已经被我们完整重写之后它就成了谎言——
// 留着会让下一次判断永远按「未完成」处理（白重下），也会诱导 aria2c 拿旧偏移去续一个内容
// 已经换了的文件。aria2c 自己的下载路径不调用它（续传凭证归 aria2c 自己管理）。
func removeStaleAria2Control(destPath string) {
	if hasAria2ControlFile(destPath) {
		if err := os.Remove(aria2ControlPath(destPath)); err == nil {
			util.LogDebug("已清理遗留的 aria2 控制文件: %s", aria2ControlPath(destPath))
		}
	}
}

func DownloadFile(ctx context.Context, url, destPath string, cfg DownloadConfig) error {
	if cfg.UseAria2c {
		util.LogDebug("Start downloading: %s", util.MaskUrl(url))
		return downloadWithAria2c(ctx, url, destPath, cfg)
	}

	// Force-http replacement: gated by the option, mcdn excluded (upstream).
	url = forceHTTPIfNeeded(url, cfg.ForceHTTP)

	// 上游 DownloadFileCoreAsync / MultiThreadDownloadCoreAsync 都以脱敏 URL 打这一行
	// （每文件一次）。本仓此前完全没有：用户报「404 概率高」时，日志里既看不出是哪台
	// host、也看不出哪条路径——「强制替换到镜像后 404」这种最常见的解释因此无法证实。
	util.LogDebug("Start downloading: %s", util.MaskUrl(url))

	multi := cfg.MultiThread
	if multi && strings.Contains(url, "-cmcc-") {
		util.LogWarn("检测到cmcc域名cdn, 已经禁用多线程")
		multi = false
	}

	pr, err := probeFile(ctx, url, cfg)
	if err == nil && pr.size > 0 && existingDownloadComplete(destPath, pr.size) {
		util.Log("%s 已存在, 跳过下载...", destPath)
		// t89（t82 评估 ③）：产物已完整时顺手回收各模式遗留的中间文件，避免磁盘垃圾与
		// 「下次又续到旧前缀」的困惑。**不删** dest.aria2（它存在就说明未完成，跳过分支根本
		// 走不到这里），**不删** live 的会话目录（可能是用户唯一的恢复副本）。
		cleanupResidue(destPath)
		return nil
	}
	// 走到这里说明要么没有本地文件、要么大小不符、要么大小相符但旁边还有 aria2 控制文件
	// （= 预分配出来的残件）。后一种情况必须走下载：交给 aria2c 续传，或由单/多线程从头覆盖。
	if err == nil && pr.size > 0 && hasAria2ControlFile(destPath) {
		util.LogWarn("%s 大小已等于远端总长，但仍存在 aria2 控制文件，按**未完成**处理（续传或重下）", destPath)
	}

	if multi && pr.ranges && pr.size > int64(cfg.segmentSize())*1024*1024 {
		err := multiThreadDownload(ctx, url, destPath, pr.size, cfg)
		if err == ErrRangeNotSupported {
			util.LogWarn("服务器可能并不支持多线程下载, 请使用 --multi-thread false")
			if err := singleDownload(ctx, url, destPath, pr, cfg); err != nil {
				return err
			}
			removeStaleAria2Control(destPath)
			return nil
		}
		if err != nil {
			return err
		}
		removeStaleAria2Control(destPath)
		return nil
	}
	if err := singleDownload(ctx, url, destPath, pr, cfg); err != nil {
		return err
	}
	removeStaleAria2Control(destPath)
	return nil
}

// forceHTTPIfNeeded downgrades https to http only when the option is enabled
// (upstream: default off; .mcdn.bilivideo.cn never downgraded).
func forceHTTPIfNeeded(url string, force bool) string {
	if !force {
		return url
	}
	if strings.Contains(url, ".mcdn.bilivideo.cn:") {
		return url
	}
	return strings.Replace(url, "https:", "http:", 1)
}

// needsBilibiliReferer mirrors upstream BBDownDownloadUtil: app/TV 平台
// (platform=android*) 的下载 URL 不能携带 Referer，否则 CDN 返回 403；
// web (pc) URL 则必须携带。
func needsBilibiliReferer(url string) bool {
	return !strings.Contains(url, "platform=android_tv_yst") && !strings.Contains(url, "platform=android")
}

// probeFile issues a HEAD request and returns content length, whether the
// server advertises byte-range support, and validator headers for resume.
func probeFile(ctx context.Context, url string, cfg DownloadConfig) (probeResult, error) {
	pr := probeResult{}
	client := cfg.Client.DownloadClient()
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return pr, err
	}
	if needsBilibiliReferer(url) {
		req.Header.Set("Referer", "https://www.bilibili.com")
	}
	req.Header.Set("User-Agent", cfg.userAgent())
	resp, err := client.Do(req)
	if err != nil {
		return pr, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return pr, fmt.Errorf("HEAD failed: HTTP %d", resp.StatusCode)
	}
	pr.size = resp.ContentLength
	pr.ranges = strings.Contains(strings.ToLower(resp.Header.Get("Accept-Ranges")), "bytes")
	pr.etag = resp.Header.Get("ETag")
	pr.lastModified = resp.Header.Get("Last-Modified")
	return pr, nil
}

// singleDownload downloads with resume support: partial data goes to dest+".tmp"
// and is renamed on success. An existing .tmp is resumed via Range only when a
// sidecar manifest proves the remote resource identity matches, otherwise the
// stale prefix is discarded (upstream resume-manifest semantics).
func singleDownload(ctx context.Context, url, destPath string, pr probeResult, cfg DownloadConfig) error {
	// 逐字节进度观察者（serve 的 SSE 数据源）随 ctx 进来，不落 DownloadConfig：
	// 先取一次，nil 表示没装——下面的进度门槛与渲染路径都保持与改前一致。
	// 身份绑定：本件产物的路径。同一文件续传/重试拿同一个身份，上层据此累计任务级字节。
	observer := progressObserverFor(ctx, destPath)

	dir := filepath.Dir(destPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp := destPath + ".tmp"
	metaPath := tmp + ".meta"

	readManifest := func() (resumeManifest, bool) {
		var m resumeManifest
		data, err := os.ReadFile(metaPath)
		if err != nil || json.Unmarshal(data, &m) != nil {
			return m, false
		}
		return m, true
	}

	// 身份比对用**稳定身份**（上游 v1.6.20 起）：签名参数每次刷新，比完整 URL 会让同一资源
	// 永远无法续传——B 站每次解析都会换 deadline/sign/trid/upsig。
	currentIdentity := stableResourceIdentity(url)
	identityMatches := func(m resumeManifest) bool {
		return manifestMatchesProbe(m, currentIdentity, pr.size, pr.etag, pr.lastModified)
	}

	// 声明长度未知（HEAD 不被支持/没带 Content-Length）时，磁盘上的旧 .tmp 无从校验：从 0 写入
	// 会把上一次的尾部留在产物里——新资源比旧 .tmp 短时，产物比本次实际写入的字节还长，末尾是
	// 上一份资源的残留。--skip-mux 保留原始轨道时这个文件就是交付物，没有任何下游环节能发现它。
	if pr.size <= 0 {
		if fileSizeOrZero(tmp) > 0 {
			util.LogDebug("断点续传: 服务器未声明长度, 丢弃无法校验的临时文件")
		}
		os.Remove(tmp)
		os.Remove(metaPath)
	}

	// Write the manifest BEFORE the first byte: an interrupted .tmp must carry a
	// manifest or the next run cannot trust it and has to redownload.
	{
		m := resumeManifest{Identity: currentIdentity, URL: url, Size: pr.size, ETag: pr.etag, LastModified: pr.lastModified}
		if data, err := json.Marshal(m); err == nil {
			os.WriteFile(metaPath, data, 0o644)
		}
	}

	lastErr := fmt.Errorf("download failed")
	// 候选链：主地址在前，替换前的原地址与 playurl 的 backup_url 依次在后（本仓有意差异，§4.33）。
	// 每个候选至少出场一次，所以尝试次数取 max(--retry-count, 候选数)。
	chain := cfg.candidateChain(url)
	attempts := candidateAttempts(chain, cfg.retryCount())
	cur := 0
	for attempt := 0; attempt < attempts; attempt++ {
		activeURL := chain[cur]

		var offset int64
		var ifRange string
		if pr.size > 0 {
			if info, err := os.Stat(tmp); err == nil {
				m, ok := readManifest()
				trusted := ok && identityMatches(m)
				switch {
				case info.Size() == pr.size && trusted:
					// Complete temp file with matching identity: adopt it.
					util.LogDebug("断点续传: 检测到已完整下载的临时文件且资源身份一致, 直接移动")
					if err := os.Rename(tmp, destPath); err != nil {
						return err
					}
					os.Remove(metaPath)
					return nil
				case info.Size() > 0 && info.Size() < pr.size && trusted:
					offset = info.Size()
					if m.ETag != "" {
						ifRange = m.ETag
					} else if m.LastModified != "" {
						ifRange = m.LastModified
					}
					util.LogDebug("断点续传: 从现有临时文件 %d 字节处继续（资源身份一致）", offset)
				case !trusted || info.Size() > pr.size:
					// Stale prefix / changed resource / oversized tmp: discard.
					util.LogDebug("断点续传: 临时文件资源身份不可信, 删除后完整重下")
					os.Remove(tmp)
					os.Remove(metaPath)
				}
			}
		}

		err := func() error {
			client := cfg.Client.DownloadClient()
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, activeURL, nil)
			if err != nil {
				return err
			}
			if needsBilibiliReferer(activeURL) {
				req.Header.Set("Referer", "https://www.bilibili.com")
			}
			req.Header.Set("User-Agent", cfg.userAgent())
			if cfg.Cookie != "" {
				req.Header.Set("Cookie", cfg.Cookie)
			}
			if offset > 0 {
				req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
				if ifRange != "" {
					// Let the server confirm the local prefix still belongs to this
					// resource; a 200/412 response makes us restart/rewrite below.
					req.Header.Set("If-Range", ifRange)
				}
			}

			resp, err := client.Do(req)
			if err != nil {
				return err
			}
			defer resp.Body.Close()

			switch {
			case resp.StatusCode == http.StatusPartialContent:
				// 产物校验：HEAD 探测与 Range 响应是两个独立声明，互相矛盾时产物不可信——
				// 旧行为会先把这份长度不对的内容落盘，再靠末尾那条长度检查报错。
				if total := rangeTotal(resp.Header.Get("Content-Range")); pr.size > 0 && total > 0 && total != pr.size {
					return fmt.Errorf("服务器两次声明的长度不一致：HEAD 探测 %d 字节，Range 响应 %d 字节", pr.size, total)
				}
				if offset > 0 {
					if got := rangeStart(resp.Header.Get("Content-Range")); got >= 0 && got != offset {
						// Server ignored our offset: restart from scratch.
						offset = 0
						if err := os.Remove(tmp); err != nil && !os.IsNotExist(err) {
							return err
						}
					}
				}
			case resp.StatusCode >= 200 && resp.StatusCode < 300:
				// Full body (no range support or If-Range mismatch): restart.
				offset = 0
				if err := os.Remove(tmp); err != nil && !os.IsNotExist(err) {
					return err
				}
			default:
				return newHTTPStatusError(resp.StatusCode, "download failed")
			}

			out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY, 0o644)
			if err != nil {
				return err
			}
			defer out.Close()
			if _, err := out.Seek(offset, io.SeekStart); err != nil {
				return err
			}

			guard := newStallGuard(resp.Body, downloadStallTimeout)
			defer guard.Stop()
			// 小于 1MiB 的辅助资源（封面/字幕等）不显示进度条：它们在百毫秒内完成，
			// 进度条只会留下一行 100% 噪声。--progress-json 与观察者不看终端、也不看大小——
			// 它们正是给 GUI/自动化/SSE 消费的，每个文件都该有事件（含收尾帧）。
			withProgress := resp.ContentLength > 0 &&
				(progressJSONEnabled.Load() || observer != nil || (isTerminalOut() && pr.size >= 1<<20))
			// 观察者还多覆盖一种情况：响应没声明长度（chunked / 无 Content-Length）。
			// 终端与 JSON 的既有门槛不变，观察者照收事件、Total 报 0（未知）或由 HEAD
			// 探测到的整份长度推出本次剩余。
			if resp.ContentLength <= 0 && observer != nil {
				withProgress = true
			}
			if !withProgress {
				_, err = io.Copy(out, guard)
				return err
			}
			// 终端进度条、JSON 事件与观察者共用一个读取器与一套口径：total 是本次响应的
			// **剩余**长度，base=offset 是磁盘上已就位的字节数。曾经终端那条忘了设 base，
			// 于是同一次续传里百分比按 current/total、x/y 却按 (base+current)/total。
			remaining := resp.ContentLength
			if remaining < 0 {
				remaining = 0
				if pr.size > offset {
					remaining = pr.size - offset
				}
			}
			pr2 := newProgressReader(guard, remaining, observer)
			pr2.base = offset
			pr2.cursor = cfg.ProgressCursor
			defer pr2.Close()
			_, err = io.Copy(out, pr2)
			return err
		}()
		// Verify the final length before adopting the file.
		if err == nil && pr.size > 0 {
			if info, statErr := os.Stat(tmp); statErr != nil || info.Size() != pr.size {
				err = fmt.Errorf("下载产物长度(%d)与服务器声明(%d)不符", fileSizeOrZero(tmp), pr.size)
			}
		}
		if err == nil {
			if renameErr := os.Rename(tmp, destPath); renameErr != nil {
				return renameErr
			}
			os.Remove(metaPath)
			return nil
		}

		lastErr = err
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// 分类重试策略（见 retry_policy.go）：确定性失败立刻放弃；412 只在成功轮换自动 UA
		// 之后才重试；网络/404 有候选就立刻换地址，没有候选时才退避（网络短退避、404 沿用阶梯）。
		plan := planTrackRetry(err, attempt, len(chain)-cur, cfg.retryDelay())
		if !plan.Retry {
			util.LogDebug("下载失败（%s，不再重试）: %v", plan.Class, err)
			break
		}
		if attempt+1 >= attempts {
			break // 阶梯用尽：不要再轮换 UA / 换候选（那些只对"下一次尝试"有意义）
		}
		if plan.NeedsUARotation && !cfg.rotateUserAgentFor412() {
			util.LogDebug("下载被风控拦截(412)，但 UA 为显式指定、无法轮换，不再重试: %v", err)
			break
		}
		if plan.SwitchCandidate {
			cur++
			util.LogWarn("%s，改用候选地址重试: %s", candidateSwitchReason(err), util.MaskUrl(chain[cur]))
		}
		if plan.Backoff <= 0 {
			continue
		}
		// 轨道级重试（上游 BBDownDownloadUtil.DownloadFileCoreAsync）记 Debug：
		// 终端默认只该看到页面级那条 Warn。两级此前用同一句话，日志读起来像
		// 「3×3=9 次连续重试」——用户实测就是这么被绕进去的。
		util.LogDebug("下载失败(%s，第%d次重试, %dms后): %v", plan.Class, attempt+1, plan.Backoff.Milliseconds(), lastErr)
		if !sleepCtx(ctx, plan.Backoff) {
			return ctx.Err()
		}
	}
	return lastErr
}

// fileSizeOrZero returns the size of a file, or 0 on error.
func fileSizeOrZero(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}

// rangeTotal 解析 Content-Range 声明的资源总长（"bytes N-M/S" 的 S），未知返回 -1。
// 它是**权威总长**：HEAD 的 Content-Length 可能来自缓存/占位，206 的 S 是这次真正要给的
// 那份内容的总长——两者不一致说明对象已经换了，产物不可信（上游 RemoteSizeMismatchException）。
func rangeTotal(contentRange string) int64 {
	idx := strings.Index(contentRange, "/")
	if idx < 0 {
		return -1
	}
	total := strings.TrimSpace(contentRange[idx+1:])
	if total == "" || total == "*" {
		return -1
	}
	n, err := strconv.ParseInt(total, 10, 64)
	if err != nil {
		return -1
	}
	return n
}

// rangeStart parses the start offset from a Content-Range header ("bytes N-M/S").
func rangeStart(contentRange string) int64 {
	if contentRange == "" {
		return -1
	}
	rest := strings.TrimPrefix(strings.ToLower(contentRange), "bytes ")
	if idx := strings.Index(rest, "-"); idx > 0 {
		if n, err := strconv.ParseInt(rest[:idx], 10, 64); err == nil {
			return n
		}
	}
	return -1
}

// shardGroup 管理一次多线程下载的分片生命周期（t89，t82 评估 ②）：
// **首次**分片失败时取消其余在途分片，同时保留最初的失败原因（调用方最终看到的必须是它，
// 而不是后面那堆 context.Canceled）。
//
// 为什么做成类型：取消与「保留原始错误」的语义要能被确定性用例钉住——用真服务器观察
// 取消传播会受平台/时序影响（本机实测 handler 侧观察不稳定），机制层用纯逻辑测才可靠。
type shardGroup struct {
	mu     sync.Mutex
	first  error
	cancel context.CancelFunc
}

// newShardGroup 从父 ctx 派生可取消的 runCtx；调用方负责在结束时 cancel（defer）。
func newShardGroup(parent context.Context) (*shardGroup, context.Context) {
	ctx, cancel := context.WithCancel(parent)
	return &shardGroup{cancel: cancel}, ctx
}

// record 记录一次分片失败：只有**第一个**错误会被保留，并在那一刻取消其余在途分片。
// 返回是否为首个错误（调用方据此决定是否打印/计数）。
func (g *shardGroup) record(err error) bool {
	g.mu.Lock()
	first := g.first == nil
	if first {
		g.first = err
	}
	g.mu.Unlock()
	if first {
		g.cancel()
	}
	return first
}

// err 返回首个错误（没有则 nil）。
func (g *shardGroup) err() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.first
}

// multiThreadDownload downloads segments concurrently, validates range responses
// and falls back to single-threaded download when the server ignores Range.
func multiThreadDownload(ctx context.Context, url, destPath string, size int64, cfg DownloadConfig) error {
	dir := filepath.Dir(destPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	segSize := planSegmentBytes(size, cfg)
	var clips []clipRange
	var offset int64
	idx := 0
	for offset < size {
		end := offset + segSize - 1
		if end >= size {
			end = size - 1
		}
		clips = append(clips, clipRange{idx: idx, from: offset, to: end})
		offset = end + 1
		idx++
	}

	// 逐字节进度观察者（serve 的 SSE 数据源）随 ctx 进来，与单线程路径同一契约；
	// 身份绑定在 destPath 上：分片聚合帧与单线程帧在服务端是同一个「文件」（同一条身份）。
	observer := progressObserverFor(ctx, destPath)

	// 进度按「分片累计字节」实时聚合（上游 ProgressAggregator），只按分片完成累加会让
	// 进度条以分片数为台阶跳变。
	aggregate := newProgressAggregator(len(clips))
	var totalBytes atomic.Int64
	pacer := newProgressPacer()
	reportProgress := func(idx int) func(int64) {
		return func(cumulative int64) {
			totalBytes.Store(aggregate.Report(idx, cumulative))
			// 数据到达即打点（合并式、不阻塞）：进度帧不再等 125ms 定时器（见 pacer.go）。
			pacer.Signal()
		}
	}

	progressDone := make(chan struct{})
	progressStopped := make(chan struct{})
	// 分P游标由编排层经 DownloadConfig 传入；聚合路径与单线程路径同一口径。
	go renderProgressBar(&totalBytes, size, cfg.ProgressCursor, pacer, progressDone, progressStopped, observer)

	sem := make(chan struct{}, maxConcurrentClips())
	var wg sync.WaitGroup
	var notSupported atomic.Bool

	// t89（t82 评估 ②）：任一在途分片失败就取消其余分片——否则它们会把整段白白下完
	// （用户侧感受是「明明已经报错了还在跑」，同时白耗流量与时间）。取消的是派生 runCtx，
	// 调用方传入的 ctx 不受影响，用户取消语义照旧透传。
	group, runCtx := newShardGroup(ctx)
	defer group.cancel()

	recordErr := func(err error) {
		group.record(err)
	}

	for _, clip := range clips {
		if notSupported.Load() {
			break
		}
		wg.Add(1)
		go func(c clipRange) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-runCtx.Done():
				recordErr(runCtx.Err())
				return
			}

			n, err := downloadRange(runCtx, url, destPath, c, size, cfg, reportProgress(c.idx))
			if err == ErrRangeNotSupported {
				notSupported.Store(true)
				recordErr(err)
				return
			}
			if err != nil {
				recordErr(err)
				return
			}
			// 分片完成后再补一次 Add 是终端进度条的既有行为（帧在两次上报之间也不会低于
			// 已完成分片）；JSON 事件与观察者按聚合总量出数，不做这一步，否则会发出
			// downloaded > total（percent 只能靠截断才不越界）——那是给程序读的数据
			// （SSE 事件同属这一类），不能自相矛盾。
			if !progressJSONEnabled.Load() && observer == nil {
				totalBytes.Add(n)
			}
		}(clip)
	}
	wg.Wait()
	close(progressDone)
	// 等进度行擦干净再往下打日志：不等的话日志会和最后一帧挤在同一行。
	<-progressStopped

	if firstErr := group.err(); notSupported.Load() || firstErr != nil {
		for _, c := range clips {
			os.Remove(clipPath(destPath, c.idx))
		}
		if notSupported.Load() {
			return ErrRangeNotSupported
		}
		return firstErr
	}

	// Verify every clip has the expected byte count before merging.
	for _, c := range clips {
		info, err := os.Stat(clipPath(destPath, c.idx))
		if err != nil || info.Size() != c.to-c.from+1 {
			for _, cc := range clips {
				os.Remove(clipPath(destPath, cc.idx))
			}
			return fmt.Errorf("分片大小校验失败")
		}
	}

	// Merge clip files
	util.Log("合并分片...")
	var clipFiles []string
	for _, c := range clips {
		clipFiles = append(clipFiles, clipPath(destPath, c.idx))
	}
	if err := util.CombineMultipleFilesIntoSingleFile(ctx, clipFiles, destPath); err != nil {
		return err
	}
	util.Log("清理分片...")
	for _, f := range clipFiles {
		os.Remove(f)
	}
	return nil
}

// countingWriter 把已写入的累计字节数回报给进度聚合。
type countingWriter struct {
	w       io.Writer
	written int64
	onWrite func(int64)
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	if n > 0 {
		c.written += int64(n)
		if c.onWrite != nil {
			c.onWrite(c.written)
		}
	}
	return n, err
}

type clipRange struct {
	idx      int
	from, to int64
}

func clipPath(dest string, idx int) string {
	return fmt.Sprintf("%s.%03d.vclip", dest, idx)
}

// downloadRange downloads one byte range with per-clip retries and validates
// that the server honored the requested offset.
//
// onProgress 收到的是**该分片自己的累计字节数**（不是增量）：聚合方按「新值 - 上次值」
// 推进总量，重试时先上报 0 让总量回退，与上游 ProgressAggregator 同语义。
func downloadRange(ctx context.Context, url, destPath string, clip clipRange, expectedTotal int64, cfg DownloadConfig, onProgress func(int64)) (int64, error) {
	tmpPath := clipPath(destPath, clip.idx)

	// 候选链与单线程路径同语义：每个分片独立走一遍链，404/连接失败就换下一个候选
	// （本仓有意差异，见 §4.33）。
	chain := cfg.candidateChain(url)
	attempts := candidateAttempts(chain, cfg.retryCount())
	cur := 0
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		activeURL := chain[cur]
		if attempt > 0 && onProgress != nil {
			onProgress(0) // 分片从头重下，聚合总量随之回退
		}

		n, err := func() (int64, error) {
			client := cfg.Client.DownloadClient()
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, activeURL, nil)
			if err != nil {
				return 0, err
			}
			if needsBilibiliReferer(activeURL) {
				req.Header.Set("Referer", "https://www.bilibili.com")
			}
			req.Header.Set("User-Agent", cfg.userAgent())
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", clip.from, clip.to))
			if cfg.Cookie != "" {
				req.Header.Set("Cookie", cfg.Cookie)
			}

			resp, err := client.Do(req)
			if err != nil {
				return 0, err
			}
			defer resp.Body.Close()

			switch {
			case resp.StatusCode == http.StatusPartialContent:
				// 产物校验：分片声明的内容总长必须与规划分片时的总长一致，否则拼出来的是另一份
				// 资源的前缀——旧行为会把它当成功合并出去（每个分片的字节数都"对"）。
				// 产物校验：分片声明的内容总长必须与规划分片时的总长一致，否则拼出来的是另一份
				// 资源的前缀——旧行为会把它当成功合并出去（每个分片的字节数都"对"）。
				if total := rangeTotal(resp.Header.Get("Content-Range")); total > 0 && expectedTotal > 0 && total != expectedTotal {
					return 0, fmt.Errorf("分片总长与探测不一致：服务器声明 %d 字节，规划时探测到 %d 字节", total, expectedTotal)
				}
				if got := rangeStart(resp.Header.Get("Content-Range")); got >= 0 && got != clip.from {
					return 0, fmt.Errorf("range request ignored: expected offset %d, got %d", clip.from, got)
				}
			case resp.StatusCode >= 200 && resp.StatusCode < 300:
				// Server sent the full body: it does not support ranges.
				return 0, ErrRangeNotSupported
			default:
				return 0, newHTTPStatusError(resp.StatusCode, "range request failed")
			}

			out, err := os.Create(tmpPath)
			if err != nil {
				return 0, err
			}
			defer out.Close()
			counter := &countingWriter{w: out, onWrite: onProgress}
			if _, err := io.Copy(counter, resp.Body); err != nil {
				return counter.written, err
			}
			return counter.written, nil
		}()
		if err == nil {
			return n, nil
		}
		if err == ErrRangeNotSupported || ctx.Err() != nil {
			return 0, err
		}
		lastErr = err
		os.Remove(tmpPath)
		// 与单线程同一条分类策略（见 retry_policy.go）：分片 412 同样只在轮换 UA 后重试，
		// 404/连接失败同样立刻换候选（镜像覆盖不全时会整片 404）。
		plan := planTrackRetry(err, attempt, len(chain)-cur, cfg.retryDelay())
		if !plan.Retry {
			util.LogDebug("分段下载失败（%s，不再重试）: %v", plan.Class, err)
			return 0, err
		}
		if attempt+1 >= attempts {
			continue // 阶梯用尽：循环随即结束，不再轮换 UA / 换候选
		}
		if plan.NeedsUARotation && !cfg.rotateUserAgentFor412() {
			util.LogDebug("分段下载被风控拦截(412)，但 UA 为显式指定、无法轮换，不再重试: %v", err)
			return 0, err
		}
		if plan.SwitchCandidate {
			cur++
			util.LogWarn("%s，改用候选地址重试: %s", candidateSwitchReason(err), util.MaskUrl(chain[cur]))
		}
		if plan.Backoff <= 0 {
			continue
		}
		// 上游多线程分片重试用同一口径的 Debug（分段下载失败(第N次重试, Xms后)）。
		util.LogDebug("分段下载失败(%s，第%d次重试, %dms后): %v", plan.Class, attempt+1, plan.Backoff.Milliseconds(), lastErr)
		if !sleepCtx(ctx, plan.Backoff) {
			return 0, ctx.Err()
		}
	}
	return 0, lastErr
}

// renderProgressBar 是给测试留的接缝：替换它即可在不依赖真实终端的前提下断言
// 「进度行收尾之后才允许打日志」的时序。observer 为 nil 表示没装进度观察者；
// cursor 是分P游标（空串 = 单P），见 DownloadConfig.ProgressCursor。
var renderProgressBar = renderAggregateProgress

// renderAggregateProgress 画多线程下载的聚合进度行，并把帧交给观察者。
//
// 帧内容与单线程路径**同一个渲染函数**（renderProgressFrame）：进度条 + 信息段
// （百分比 · 速率 · ETA · 总量）。此前这里是一份私有格式，只有百分比与速率、速率还
// 不对齐——同一次下载在 --multi-thread true/false 下看到两种进度行。
//
// 观察者（serve 的 SSE 数据源）与进度行 / JSON 事件共用 runProgressLoop 的节奏：
// 只在节流后的帧上回调，而不是每个 32KiB 写入块一次。回调不持锁（counter 是原子的，
// 帧数字先取出再调）。
//
// 返回前关闭 stopped：调用方据此保证「进度行已擦干净」先于后续日志（上游用 using
// 作用域表达同一件事——ProgressBar 的 Dispose 必须在合并日志之前完成）。
func renderAggregateProgress(counter *atomic.Int64, total int64, cursor string, pacer progressPacer, done <-chan struct{}, stopped chan<- struct{}, observer func(ProgressEvent)) {
	defer close(stopped)

	// notify 把一帧交给观察者；nil 时立刻返回（未装观察者 = 不进回调路径）。
	notify := func(downloaded int64, speedBps float64) {
		if observer == nil {
			return
		}
		observer(ProgressEvent{Current: downloaded, Total: total, SpeedBps: speedBps})
	}

	// --progress-json：同一套节奏，帧内容换成一行 JSON（写 stderr），收尾补 done 帧。
	// 零值 progressLine（enabled=false）保证这条路径不碰终端。
	if progressJSONEnabled.Load() {
		emit := newJSONProgressEmitter(total)
		runProgressLoop(pacer.Signals(), done, true, &progressLine{}, func() {
			emit.progress(counter.Load())
			// 观察者与 JSON 事件同帧、同速率口径（emitter 的 1 秒窗口）。
			notify(counter.Load(), emit.speed)
		})
		emit.done(counter.Load())
		// 收尾事件与 done 帧同口径：调用方拿到 stopped 时它一定已经回调过。
		notify(counter.Load(), emit.speed)
		return
	}
	line := newProgressLine()
	// 改前非终端就没有进度帧可画；只有观察者时才继续跑这条纯回调循环。
	if !line.enabled && observer == nil {
		return
	}
	animIdx := 0
	lastBytes := int64(0)
	lastTime := time.Now()
	var speedBps float64
	render := func() {
		cur := counter.Load()
		now := time.Now()
		// 速率窗口与单线程路径同口径（增量 / 实际间隔、满 1 秒结算一次）：ETA 由它推算。
		if elapsed := now.Sub(lastTime).Seconds(); elapsed >= 1.0 {
			delta := cur - lastBytes
			if delta > 0 {
				speedBps = float64(delta) / elapsed
			}
			lastBytes = cur
			lastTime = now
		}
		line.draw(renderProgressFrameWith(progressFrame{
			speedBps:   speedBps,
			downloaded: cur,
			total:      total,
			cursor:     cursor,
		}, progressChars[animIdx%len(progressChars)]))
		animIdx++
		// 同一帧、画完之后回调（数字已复制成值，不持任何锁）。
		notify(cur, speedBps)
	}

	// 首帧、节流、静默心跳与收尾擦行统一在 runProgressLoop 里（见 pacer.go）：
	// 上游此处是 125ms 定时器，本仓按「数据到达即重绘」的节奏驱动。
	runProgressLoop(pacer.Signals(), done, true, line, render)
	// 末帧可能被节流合并掉：补一条收尾事件，保证观察者一定看到最终计数。
	notify(counter.Load(), speedBps)
}

// isTerminalOut 是变量而非函数：用例需要在不依赖真实终端的前提下驱动进度条绘制。
var isTerminalOut = func() bool {
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

// downloadWithAria2c invokes aria2c with the upstream-compatible argument set,
// passing URL/headers/cookie/dir/out via stdin (input-file) so credentials do
// not appear on the command line.
func downloadWithAria2c(ctx context.Context, url, destPath string, cfg DownloadConfig) error {
	bin := "aria2c"
	if cfg.Aria2cPath != "" {
		bin = cfg.Aria2cPath
	}
	args := []string{
		"--auto-file-renaming=false",
		"--download-result=hide",
		"--allow-overwrite=true",
		"--console-log-level=warn",
		"-x16", "-s16", "-j16", "-k5M",
	}
	// 逐字节进度（任务 R）：aria2c 是黑盒，只有 --summary-interval 的摘要块能给出已下载/
	// 总量/速率。**只在挂了观察者时加**：没有观察者时摘要只是终端噪声，而 CLI 的 aria2c
	// 路径必须与改前逐字一致（参数、stderr 转发方式都不动）。
	observer := progressObserverFor(ctx, destPath)
	if observer != nil {
		args = append(args, fmt.Sprintf("--summary-interval=%d", aria2cSummaryInterval))
	}
	if cfg.Aria2cArgs != "" {
		args = append(args, splitArgs(cfg.Aria2cArgs)...)
	}
	args = append(args, "--input-file=-")

	// A hung aria2c would hold a concurrency slot forever. The task context already
	// carries user cancellation; this adds a hard ceiling on top (upstream 6h).
	aria2cCtx, cancelAria2c := context.WithTimeout(ctx, aria2cHardTimeout)
	defer cancelAria2c()
	cmd := exec.CommandContext(aria2cCtx, bin, args...)
	cmd.Stdout = os.Stdout
	if observer == nil {
		// 没有观察者（普通 CLI）：stderr 直接继承，行为与改前逐字一致。
		cmd.Stderr = os.Stderr
	} else {
		// 有观察者（serve 的 SSE 等）：stdout 与 stderr **两条流都**走解析泵——实测 aria2c 的
		// 摘要块写在 **stdout** 上（--summary-interval 的摘要 + --show-console-readout 的进度条），
		// 只接 stderr 会一帧都收不到；stderr 上则可能来 --log 与错误行。泵只消费识别出的
		// 摘要行，其余行一律转发到 os.Stderr（错误一个都不吞）；解析失败静默降级。
		pump := startAria2ProgressPump(func(s aria2Summary) {
			observer(s.progressEvent(destPath))
		})
		defer pump.Close() // 等泵处理完所有已写出的行再返回：事件不会漏在返回之后
		cmd.Stdout = pump
		cmd.Stderr = pump
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	go func() {
		defer stdin.Close()
		io.WriteString(stdin, buildAria2cInputFile(url, needsBilibiliReferer(url), cfg.Cookie, cfg.userAgent(),
			filepath.Dir(destPath), filepath.Base(destPath)))
	}()

	// aria2c 只给退出码、自己不核对长度：先探一次服务器声明的长度，跑完复核落盘字节数。
	// 探测失败（CDN 不支持 HEAD 等）就退化为「只检查文件存在」，不凭空造出误报。
	var declared int64
	if cfg.Client != nil {
		if pr, perr := probeFile(ctx, url, cfg); perr == nil && pr.size > 0 {
			declared = pr.size
		}
	}

	if err := cmd.Run(); err != nil {
		return err
	}
	if _, err := os.Stat(destPath + ".aria2"); err == nil {
		return fmt.Errorf("aria2下载可能存在错误")
	}
	info, err := os.Stat(destPath)
	if err != nil {
		return fmt.Errorf("aria2下载可能存在错误: 未找到输出文件")
	}
	if declared > 0 && info.Size() != declared {
		return fmt.Errorf("aria2 产物长度(%d)与服务器声明(%d)不符", info.Size(), declared)
	}
	return nil
}

// splitArgs splits an argument string the way a shell would: whitespace
// separates, single and double quotes group. strings.Fields would break a quoted
// value such as --user-agent="Mozilla/5.0 (X11)" into three tokens, and an
// unclosed quote must keep the collected tail instead of discarding the whole
// configuration (upstream behaves the same way).
func splitArgs(s string) []string {
	var out []string
	var cur strings.Builder
	inSingle, inDouble, started := false, false, false

	for _, r := range s {
		switch {
		case r == '\'' && !inDouble:
			inSingle = !inSingle
			started = true
		case r == '"' && !inSingle:
			inDouble = !inDouble
			started = true
		case (r == ' ' || r == '\t' || r == '\n') && !inSingle && !inDouble:
			if started {
				out = append(out, cur.String())
				cur.Reset()
				started = false
			}
		default:
			cur.WriteRune(r)
			started = true
		}
	}
	if started {
		out = append(out, cur.String())
	}
	return out
}

// aria2cSanitize strips the line breaks that would let a value break out of its
// aria2c input-file line and inject extra options.
func aria2cSanitize(v string) string {
	return strings.NewReplacer("\r", "", "\n", "").Replace(v)
}

// buildAria2cInputFile renders one aria2c input-file entry. Every interpolated
// value is forced onto a single line: the URL comes straight from the API
// response (base_url), so a mirror or an --insecure MITM could otherwise append
// "\n  out=..." and make aria2c write to an arbitrary path. The Cookie already
// had this guard while the URL — the one value that is never local — did not.
func buildAria2cInputFile(url string, needsReferer bool, cookie, userAgent, dir, out string) string {
	var sb strings.Builder
	sb.WriteString(aria2cSanitize(url) + "\n")
	if needsReferer {
		sb.WriteString("  header=Referer: https://www.bilibili.com\n")
	}
	sb.WriteString("  header=User-Agent: " + aria2cSanitize(userAgent) + "\n")
	if cookie != "" {
		sb.WriteString("  header=Cookie: " + aria2cSanitize(cookie) + "\n")
	}
	sb.WriteString("  dir=" + aria2cSanitize(dir) + "\n")
	sb.WriteString("  out=" + aria2cSanitize(out) + "\n")
	return sb.String()
}

// SortVideoTracks sorts video tracks by encoding and quality priority
// (upstream TrackSort: codecs priority, dfn priority, id descending, bandwidth).
func SortVideoTracks(tracks []entity.Video, dfnPriority, encodingPriority map[string]int, ascending bool) []entity.Video {
	sorted := make([]entity.Video, len(tracks))
	copy(sorted, tracks)
	sort.SliceStable(sorted, func(i, j int) bool {
		epI, ok := encodingPriority[sorted[i].Codecs]
		if !ok {
			epI = 100
		}
		epJ, ok := encodingPriority[sorted[j].Codecs]
		if !ok {
			epJ = 100
		}
		if epI != epJ {
			return epI < epJ
		}
		dpI, ok := dfnPriority[sorted[i].Dfn]
		if !ok {
			dpI = 100
		}
		dpJ, ok := dfnPriority[sorted[j].Dfn]
		if !ok {
			dpJ = 100
		}
		if dpI != dpJ {
			return dpI < dpJ
		}
		idI := parseTrackQuality(sorted[i].ID)
		idJ := parseTrackQuality(sorted[j].ID)
		if idI != idJ {
			return idI > idJ
		}
		if ascending {
			return sorted[i].Bandwidth < sorted[j].Bandwidth
		}
		return sorted[i].Bandwidth > sorted[j].Bandwidth
	})
	return sorted
}

// parseTrackQuality 解析清晰度 id：失败或超 Int32 一律降级为 0。
//
// 上游用 int.TryParse（Int32）——"999999999999" 这类服务器可控的畸形值在那边是 0，
// 用 Go 的 Atoi（64 位）会把它当合法值参与排序，位次与上游不同（RF-31 的降级语义）。
// NumberStyles.Integer 容忍两侧空白，这里一并 TrimSpace。
func parseTrackQuality(id string) int {
	v, err := strconv.ParseInt(strings.TrimSpace(id), 10, 32)
	if err != nil {
		return 0
	}
	return int(v)
}

// SortAudioTracks sorts audio tracks by encoding priority (upstream uses the
// short codec name, e.g. E-AC-3 => EAC3).
func SortAudioTracks(tracks []entity.Audio, encodingPriority map[string]int, ascending bool) []entity.Audio {
	sorted := make([]entity.Audio, len(tracks))
	copy(sorted, tracks)
	sort.SliceStable(sorted, func(i, j int) bool {
		epI, ok := encodingPriority[sorted[i].ShortCodecs()]
		if !ok {
			epI = 100
		}
		epJ, ok := encodingPriority[sorted[j].ShortCodecs()]
		if !ok {
			epJ = 100
		}
		if epI != epJ {
			return epI < epJ
		}
		if ascending {
			return sorted[i].Bandwidth < sorted[j].Bandwidth
		}
		return sorted[i].Bandwidth > sorted[j].Bandwidth
	})
	return sorted
}

// PrintAllTracks displays available tracks, in the order upstream Display.PrintAllTracksInfo
// prints them: 背景音频流与配音（仅当两者都存在时）→ 视频流 → 音频流。
//
// 每一段都是「段标题（带时间戳的日志行）+ 表头 + 数据行」。表头与数据行共用同一个 plan
// （见 tracklayout.go）：每一列的宽度只算一次、只有一处，用户看得到每列是什么，终端窄时
// 按同一份优先级省列，不会各打印一处、各排一套。
func PrintAllTracks(result *entity.ParsedResult, pageDur int, onlyShowInfo bool) {
	plan := newTrackPlan(util.TerminalWidth())
	// 每行自带缩进（折行的续行是悬挂缩进，与首行不同），所以这里按行写、不再另加缩进。
	printRows := func(lines []planLine) {
		for _, line := range lines {
			util.LogColorNoTimeIndent(line.indent, "%s", line.text)
		}
	}

	// 背景音频与配音属于同一块信息（上游 Display.cs:19-35）：两者都存在才打印，
	// 只打印首条配音名下的配音流。
	if len(result.BackgroundAudioTracks) > 0 && len(result.RoleAudioList) > 0 {
		util.Log("共计%d条背景音频流.", len(result.BackgroundAudioTracks))
		printRows(plan.headerLines(audioTrackHeaders))
		for i, a := range result.BackgroundAudioTracks {
			printRows(plan.audioLines(fmt.Sprintf("%d.", i), a, pageDur))
		}
		if firstRole := result.RoleAudioList[0].Audio; len(firstRole) > 0 {
			util.Log("共计%d条配音, 每条包含%d条配音流.", len(result.RoleAudioList), len(firstRole))
			printRows(plan.headerLines(audioTrackHeaders))
			for i, a := range firstRole {
				printRows(plan.audioLines(fmt.Sprintf("%d.", i), a, pageDur))
			}
		}
	}
	if len(result.VideoTracks) > 0 {
		util.Log("共计%d条视频流.", len(result.VideoTracks))
		printRows(plan.headerLines(videoTrackHeaders))
		for i, v := range result.VideoTracks {
			printRows(plan.videoLines(fmt.Sprintf("%d.", i), v, pageDur))
			// --only-show-info：每条流后面直接给出可下载地址（上游 Console.WriteLine(v.baseUrl)），
			// 少了这一行，-I 拿到的就只是体积/码率清单，脚本无法据此取流。
			if onlyShowInfo {
				printStreamURL(plan, v.BaseURL)
			}
		}
	}
	if len(result.AudioTracks) > 0 {
		util.Log("共计%d条音频流.", len(result.AudioTracks))
		printRows(plan.headerLines(audioTrackHeaders))
		for i, a := range result.AudioTracks {
			printRows(plan.audioLines(fmt.Sprintf("%d.", i), a, pageDur))
			if onlyShowInfo {
				printStreamURL(plan, a.BaseURL)
			}
		}
	}
}

// printStreamURL 打出一条流的直链（--only-show-info）。
//
// 管道/重定向里**逐字节原样**输出（一行地址加换行：无缩进、无颜色、无截断），600+ 字符的
// CDN 直链也必须完整——脚本按行取地址是本仓的对外契约（TestOnlyShowInfoKeepsRawURLInPipe）。
// 终端里则换成省略形式的提示行（↳ host/…/文件名）：这么长的地址在终端里既读不了、也复制不走，
// 还会把整份清单冲散；要全文用 --print-urls（专为管道设计的出口）。
func printStreamURL(plan trackPlan, raw string) {
	if raw == "" {
		return
	}
	if !isTerminalOut() {
		fmt.Println(raw)
		return
	}
	for _, line := range plan.urlLines(raw) {
		util.LogColorNoTimeIndent(line.indent, "%s", line.text)
	}
}

// PrintSelectedTrack shows the chosen tracks (matching C# format).
//
// 与流清单共用 tracklayout.go 的同一套列宽与缩进：行首是 [视频]/[音频] 标签而不是序号，
// 但名称列、码率列、体积列都落在与清单行相同的显示列上，上下对照着看。
//
// 这里不另打表头：同一次运行里清单刚打过表头（workflow 先 PrintAllTracks 再走到这里），
// 而且这两行自带 [视频]/[音频] 标签说明各自是什么；再插一行标签只会把两行数据挤散。
func PrintSelectedTrack(video *entity.Video, audio *entity.Audio, pageDur int) {
	plan := newTrackPlan(util.TerminalWidth())
	if video != nil {
		for _, line := range plan.videoLines("[视频]", *video, pageDur) {
			util.LogColorNoTimeIndent(line.indent, "%s", line.text)
		}
	}
	if audio != nil {
		for _, line := range plan.audioLines("[音频]", *audio, pageDur) {
			util.LogColorNoTimeIndent(line.indent, "%s", line.text)
		}
	}
}

// ParsePriority converts a comma-separated priority string to a map with index
// values (kept for API compatibility; the workflow uses parseEncodingPriority/
// parseDfnPriority with upstream index++ semantics).
func ParsePriority(priorityStr string) map[string]int {
	if priorityStr == "" {
		return nil
	}
	result := make(map[string]int)
	parts := strings.Split(priorityStr, ",")
	for i, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result[p] = i
		}
	}
	return result
}

// FormatSavePath builds the output file path from the save pattern (upstream
// PathHelper placeholder set).
func FormatSavePath(pattern, title string, video *entity.Video, audio *entity.Audio, page entity.Page, pagesCount int, apiType string, pubTime int64) string {
	result := strings.ReplaceAll(pattern, "\\", "/")
	title = util.GetValidFileName(title, "_", true)
	title = strings.Trim(strings.TrimRight(title, "."), " ")
	pageTitle := util.GetValidFileName(page.Title, "_", true)
	pageTitle = strings.Trim(strings.TrimRight(pageTitle, "."), " ")
	ownerName := util.GetValidFileName(page.OwnerName, "_", true)
	ownerName = strings.Trim(strings.TrimRight(ownerName, "."), " ")

	result = strings.ReplaceAll(result, "<videoTitle>", title)
	result = strings.ReplaceAll(result, "<pageNumber>", fmt.Sprintf("%d", page.Index))
	result = strings.ReplaceAll(result, "<pageNumberWithZero>", fmt.Sprintf("%0*d", digits(pagesCount), page.Index))
	result = strings.ReplaceAll(result, "<pageTitle>", pageTitle)
	result = strings.ReplaceAll(result, "<aid>", util.SanitizePathSegment(page.Aid))
	result = strings.ReplaceAll(result, "<cid>", util.SanitizePathSegment(page.Cid))
	result = strings.ReplaceAll(result, "<bvid>", page.Bvid())
	result = strings.ReplaceAll(result, "<ownerName>", ownerName)
	result = strings.ReplaceAll(result, "<ownerMid>", util.SanitizePathSegment(page.OwnerMid))
	result = strings.ReplaceAll(result, "<apiType>", apiType)

	if video != nil {
		result = strings.ReplaceAll(result, "<dfn>", util.SanitizePathSegment(video.Dfn))
		result = strings.ReplaceAll(result, "<videoCodecs>", util.SanitizePathSegment(video.Codecs))
		result = strings.ReplaceAll(result, "<res>", util.SanitizePathSegment(video.Res))
		result = strings.ReplaceAll(result, "<fps>", util.SanitizePathSegment(video.FPS))
		result = strings.ReplaceAll(result, "<videoBandwidth>", fmt.Sprintf("%d", video.Bandwidth))
	}
	if audio != nil {
		result = strings.ReplaceAll(result, "<audioCodecs>", util.SanitizePathSegment(audio.Codecs))
		result = strings.ReplaceAll(result, "<audioBandwidth>", fmt.Sprintf("%d", audio.Bandwidth))
	}
	result = replaceDatePlaceholder(result, "<publishDate:", pubTime, "yyyy-MM-dd_HH-mm-ss")
	result = replaceDatePlaceholder(result, "<videoDate:", page.PubTime, "yyyy-MM-dd_HH-mm-ss")
	result = strings.ReplaceAll(result, "<publishDate>", dateOrEmpty(pubTime))
	result = strings.ReplaceAll(result, "<videoDate>", dateOrEmpty(page.PubTime))

	return result
}

// replaceDatePlaceholder handles <key:fmt> placeholders.
func replaceDatePlaceholder(s, prefix string, ts int64, defFmt string) string {
	for {
		idx := strings.Index(s, prefix)
		if idx < 0 {
			return s
		}
		end := strings.IndexByte(s[idx:], '>')
		if end < 0 {
			return s
		}
		format := s[idx+len(prefix) : idx+end]
		if format == "" {
			format = defFmt
		}
		value := ""
		if ts > 0 {
			value = formatTimeStamp(ts, format)
		}
		s = s[:idx] + value + s[idx+end+1:]
	}
}

// dateOrEmpty formats the default timestamp, empty for zero.
func dateOrEmpty(ts int64) string {
	if ts <= 0 {
		return ""
	}
	return formatTimeStamp(ts, "yyyy-MM-dd_HH-mm-ss")
}

// digits returns the number of decimal digits of n (min 1).
func digits(n int) int {
	if n <= 0 {
		return 1
	}
	d := 0
	for n > 0 {
		n /= 10
		d++
	}
	return d
}

// formatTimeStamp converts a unix timestamp with a yyyy-MM-dd_HH-mm-ss style
// format string (upstream FormatTimeStamp token subset).
func formatTimeStamp(ts int64, format string) string {
	t := time.Unix(ts, 0).Local()
	r := strings.NewReplacer(
		"yyyy", "2006",
		"MM", "01",
		"dd", "02",
		"HH", "15",
		"mm", "04",
		"ss", "05",
		"M", "1",
		"d", "2",
		"H", "3",
		"m", "4",
		"s", "5",
	)
	return t.Format(r.Replace(format))
}
