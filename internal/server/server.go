package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"crypto/subtle"
	"errors"
	"github.com/QC3284/BBDown-Go/internal/config"
	"github.com/QC3284/BBDown-Go/internal/entity"
	"github.com/QC3284/BBDown-Go/internal/util"
	"github.com/QC3284/BBDown-Go/internal/workflow"
	"sort"
)

// TaskStatus represents the state of a download task (PascalCase values,
// matching the upstream API contract).
type TaskStatus string

const (
	StatusQueued    TaskStatus = "Queued"
	StatusRunning   TaskStatus = "Running"
	StatusSucceeded TaskStatus = "Succeeded"
	StatusFailed    TaskStatus = "Failed"
	StatusCancelled TaskStatus = "Cancelled"
)

const (
	maxFinishedTasks   = 1000
	finishedRetention  = 30 * 24 * time.Hour
	callbackTimeout    = 2 * time.Minute
	maxRequestBodySize = 64 << 10 // 64KB

	// defaultTaskFileName 是完成任务清单的默认文件名。相对路径 → 落在 serve **进程的工作目录**
	// （对真实用户是设计：清单跟着工作目录走）。声明成常量而不是字面量，是因为
	// workdir_guard_test.go 的包级守卫要盯住这个名字——两处必须指向同一个名字。
	defaultTaskFileName = "bbdown-tasks.json"
)

// DownloadTask tracks a single download operation (upstream JSON contract).
type DownloadTask struct {
	JobID          string `json:"JobId"`
	Aid            string `json:"Aid"`
	URL            string `json:"Url"`
	TaskCreateTime int64  `json:"TaskCreateTime"`
	Title          string `json:"Title,omitempty"`
	Pic            string `json:"Pic,omitempty"`
	VideoPubTime   int64  `json:"VideoPubTime,omitempty"`
	TaskFinishTime int64  `json:"TaskFinishTime,omitempty"`
	// 进度口径（契约由 progress_contract_test.go 钉住，理由见 progress.go 顶部注释）：
	//   - Progress 是任务级进度的**服务端边界值**（0~1）：执行期间保持 0，只在任务成功时
	//     置 1.0（见 processTask）。它**不是**实时字节比例，是 bbdown-tasks.json 与上游
	//     兼容契约消费的字段；
	//   - TotalDownloadedBytes 是**任务已下载字节数**：取两个来源的较大者（口径见
	//     totalBytesLocked）——①下载执行期间按文件身份累计的实时字节（含续传 base，
	//     与 SSE 同口径）；②已落盘产物字节合计（aria2c 等没有逐字节观察者的路径的唯一
	//     进度信号，§4.57）。
	// SSE（GET /events）的 task_progress 帧是**逐文件**口径：percent 0~100、downloaded/total
	// 为整份文件（含续传 base）；任务级数字按文件身份求和，文件切换不回跳（详见 progress.go）。
	Progress             float64    `json:"Progress"`
	DownloadSpeed        string     `json:"DownloadSpeed,omitempty"`
	TotalDownloadedBytes int64      `json:"TotalDownloadedBytes"`
	IsSuccessful         bool       `json:"IsSuccessful"`
	Status               TaskStatus `json:"Status"`
	ErrorMessage         string     `json:"ErrorMessage,omitempty"`
	SavePaths            []string   `json:"SavePaths,omitempty"`

	cancelFn context.CancelFunc
	mu       *sync.Mutex

	// SSE 速率采样（未导出 → 不进 JSON 契约）：与 --progress-json 同一口径，满 1 秒结算一次。
	lastSampleTime  time.Time
	lastSampleBytes int64
	speedBps        float64

	// lastProgressPublish 是上一个逐字节进度帧的发布时刻，供 SSE 侧的每任务节流使用
	// （见 progress.go 的 publishDownloadProgress）。未导出 → 不进 JSON 契约。
	lastProgressPublish time.Time

	// artifactBytes 是**已落盘产物**的字节合计（onArtifactSaved 累加、同一路径去重）。
	// 未导出 → 不进 JSON 契约；TotalDownloadedBytes 由它与 progressBytes 刷新。
	artifactBytes int64
	// progressBytes 是**按文件身份累计**的实时字节映射：身份 → 该文件最新一帧观察者上报的
	// 整份文件已完成字节（含续传 base）。未导出 → 不进 JSON 契约；口径见 progress.go。
	progressBytes map[string]int64
}

// Snapshot returns a thread-safe copy of the task.
func (t *DownloadTask) Snapshot() DownloadTask {
	t.mu.Lock()
	defer t.mu.Unlock()
	cp := *t
	cp.SavePaths = make([]string, len(t.SavePaths))
	copy(cp.SavePaths, t.SavePaths)
	// 未导出的实时映射不进快照：只读副本不该与任务共享可变映射（它也不进 JSON 契约）。
	cp.progressBytes = nil
	return cp
}

// AddSavePath adds a file path to the task save list and reports whether it was
// new. The caller (serve's SSE layer) uses the result to count artefact bytes:
// a resumed page or a retry reports the same artefact twice and double counting
// would inflate TotalDownloadedBytes. The list is a set for the same reason
// (upstream dedupes).
func (t *DownloadTask) AddSavePath(path string) bool {
	if path == "" {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, p := range t.SavePaths {
		if p == path {
			return false
		}
	}
	t.SavePaths = append(t.SavePaths, path)
	return true
}

// recordProgressBytesLocked 记下一帧观察者的逐字节进度，并按 totalBytesLocked 的口径
// 刷新 TotalDownloadedBytes。**调用方必须持 t.mu**（发布路径已经在节流的那段临界区里）。
//
// identity 来自 download.ProgressEvent.Key（任务内文件身份，见 internal/download/
// progressobserver.go）：同一件产物续传/重试拿同一个身份，不同产物不同。空身份合法
// ——自建观察者的调用方（用例）全部落在同一个「未知文件」桶里。
func (t *DownloadTask) recordProgressBytesLocked(identity string, current int64) {
	if current < 0 {
		current = 0 // 负值只来自计数异常；契约里 0 就是「未知」，负值只会污染累计
	}
	if t.progressBytes == nil {
		t.progressBytes = make(map[string]int64, 1)
	}
	t.progressBytes[identity] = current
	t.TotalDownloadedBytes = t.totalBytesLocked()
}

// addArtifactBytes 记下一件已落盘产物的字节数并刷新 TotalDownloadedBytes。
func (t *DownloadTask) addArtifactBytes(size int64) {
	if size <= 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.artifactBytes += size
	t.TotalDownloadedBytes = t.totalBytesLocked()
}

// totalBytesLocked 是 TotalDownloadedBytes 的口径：**两个来源取较大者**（调用方持 t.mu）。
//
//   - 实时映射 progressBytes：下载执行期间与服务端边界无关的真实字节（含续传 base），
//     按文件身份求和——多产物任务在文件切换时不回跳，单产物任务就等于最近一帧；
//   - 已落盘产物字节 artifactBytes：aria2c 路径没有逐字节观察者（§4.57），这是那里
//     唯一的进度信号，也是「产物已落盘」这一服务端边界的字节数。
//
// 为什么不是相加：两个来源量的是同一批字节（观察者报完 → 产物落盘），相加会双计。
// 为什么不是只取实时：aria2c 任务会永久停在 0。取较大者让来源切换时数字不回跳。
func (t *DownloadTask) totalBytesLocked() int64 {
	var live int64
	for _, n := range t.progressBytes {
		live += n
	}
	if t.artifactBytes > live {
		return t.artifactBytes
	}
	return live
}

// sampleSpeed 按 --progress-json 的口径（满 1 秒结算一次，分片回退时 delta 按 0 处理）
// 计算任务的平均速率，供 SSE 事件使用。调用方：publishTaskEvent。
func (t *DownloadTask) sampleSpeed(now time.Time) float64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.lastSampleTime.IsZero() {
		t.lastSampleTime = now
		t.lastSampleBytes = t.TotalDownloadedBytes
		return 0
	}
	if elapsed := now.Sub(t.lastSampleTime).Seconds(); elapsed >= 1.0 {
		delta := t.TotalDownloadedBytes - t.lastSampleBytes
		if delta < 0 {
			delta = 0
		}
		t.speedBps = float64(delta) / elapsed
		t.lastSampleBytes = t.TotalDownloadedBytes
		t.lastSampleTime = now
	}
	return t.speedBps
}

// SetStatus updates the task status and derives IsSuccessful (upstream).
func (t *DownloadTask) SetStatus(s TaskStatus) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.Status = s
	t.IsSuccessful = s == StatusSucceeded
}

// AddTaskResponse for the add-task endpoint (upstream {"TaskId": ...}).
type AddTaskResponse struct {
	TaskID string `json:"TaskId"`
}

// TaskListResponse wraps running and finished tasks.
type TaskListResponse struct {
	Running  []DownloadTask `json:"Running"`
	Finished []DownloadTask `json:"Finished"`
}

// APIServer runs BBDown in HTTP API server mode.
type APIServer struct {
	listenURL     string
	maxConcurrent int
	serveToken    string
	notifyWebhook string

	mu            sync.Mutex
	runningTasks  []*DownloadTask
	finishedTasks []*DownloadTask
	semaphore     chan struct{} // execution concurrency (maxConcurrent)
	acceptLimiter chan struct{} // pending-queue cap (maxConcurrent * 9)
	taskFile      string
	taskWG        sync.WaitGroup
	auth          *authGuard
	trustedProxy  string
	queryLimiter  chan struct{} // bounds concurrent /get-tasks queries

	// events 是 SSE 进度流（Web UI 的数据源）；eventsAuth 是它**独立**的 token 失败计数器：
	// EventSource 会自动重连，与 API 共用 authGuard 会让一个没带 token 的页面把浏览器
	// 锁在 API 外面（见 handleEvents）。
	events     *eventHub
	eventsAuth *authGuard

	// progressPublishInterval 是同一任务两帧**逐字节**进度之间的最小间隔（SSE 侧的第二道
	// 节流，见 progress.go）。字段而非常量：用例可以把它放大（验证帧数上限）或调零
	// （验证上限过去后照发、以及 hub 的队列丢帧兜底）。零值 = 不限速。
	progressPublishInterval time.Duration

	// persistMu serialises bbdown-tasks.json writes: several tasks finish
	// concurrently and each calls persistFinishedTasks.
	persistMu sync.Mutex
	// taskBaseCtx is the parent of every task context, so a shutdown can cancel
	// in-flight downloads instead of truncating them at process exit.
	taskBaseCtx context.Context
}

// NewAPIServer creates a new API server instance.
func NewAPIServer(listenURL string, maxConcurrent int, serveToken, notifyWebhook string) *APIServer {
	if maxConcurrent < 1 {
		maxConcurrent = 1
	}
	return &APIServer{
		listenURL:     listenURL,
		maxConcurrent: maxConcurrent,
		serveToken:    serveToken,
		notifyWebhook: notifyWebhook,
		semaphore:     make(chan struct{}, maxConcurrent),
		acceptLimiter: make(chan struct{}, maxConcurrent*9),
		taskFile:      defaultTaskFileName,
		auth:          newAuthGuard(),
		queryLimiter:  make(chan struct{}, maxConcurrentQueries),
		events:        newEventHub(maxEventClients),
		eventsAuth:    newAuthGuard(),

		progressPublishInterval: defaultProgressPublishInterval,
	}
}

// validateListenURL enforces the listen-address security rules (upstream
// ServeCommand + Run double check): http scheme only; non-loopback hosts
// (0.0.0.0/::/interface IPs included) require --serve-token.
func validateListenURL(listenURL, serveToken string) error {
	if !strings.HasPrefix(listenURL, "http://") {
		return fmt.Errorf("%s 不是合法的 http URL，url 示例：http://0.0.0.0:5000", listenURL)
	}
	host := hostOfListenURL(listenURL)
	if !isLoopbackHost(host) && serveToken == "" {
		return fmt.Errorf("监听地址 %s 不是回环地址，必须配置 --serve-token 才能启动", listenURL)
	}
	return nil
}

// Run starts the API server.
// buildHandler wires the API routes plus the token and guard middlewares. It is
// a method so tests can assert the guards are actually installed — the exact
// regression that left the default loopback deployment (no token) with no host
// check and no cross-origin protection at all.
func (s *APIServer) buildHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/get-tasks", s.handleGetTasks)
	mux.HandleFunc("/get-tasks/", s.handleGetTasks)
	mux.HandleFunc("/add-task", s.handleAddTask)
	mux.HandleFunc("/cancel/", s.handleCancel)
	mux.HandleFunc("/remove-finished", s.handleRemoveFinished)
	mux.HandleFunc("/remove-finished/", s.handleRemoveFinished)
	mux.HandleFunc("/health", s.handleHealth)
	// 任务 N：极简 Web UI + SSE 进度流。两者都不是 API 路径（tokenMiddleware 的 isAPI
	// 判定与语义未改）：页面不含用户数据，事件流在配了 --serve-token 的部署里自带 token
	// 校验（见 handleEvents），两者都仍受 guardMiddleware 约束。
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/events", s.handleEvents)

	var handler http.Handler = mux
	if s.serveToken != "" {
		handler = s.tokenMiddleware(handler)
	}
	// The guard is installed unconditionally: the default loopback deployment
	// has no token, and without it a browser could reach the API through DNS
	// rebinding or a cross-origin "simple request".
	return s.guardMiddleware(handler)
}

func (s *APIServer) Run(ctx context.Context) error {
	if err := validateListenURL(s.listenURL, s.serveToken); err != nil {
		return err
	}

	s.loadFinishedTasks()

	taskBaseCtx, taskCancelAll := context.WithCancel(ctx)
	s.mu.Lock()
	s.taskBaseCtx = taskBaseCtx
	s.mu.Unlock()
	defer taskCancelAll()

	util.Log("API服务器已启动: %s", s.listenURL)
	util.Log("最大并发: %d", s.maxConcurrent)

	server := &http.Server{
		Addr:              strings.TrimPrefix(s.listenURL, "http://"),
		Handler:           s.buildHandler(),
		ReadHeaderTimeout: 30 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	go func() {
		<-ctx.Done()
		// Cancel in-flight tasks first. Waiting for them without cancelling meant
		// they never reached Cancelled and never persisted, and the process exit
		// truncated them after the 30s grace period.
		taskCancelAll()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		done := make(chan struct{})
		go func() { s.taskWG.Wait(); close(done) }()
		select {
		case <-done:
		case <-shutdownCtx.Done():
			util.LogWarn("等待后台任务超时，强制退出")
		}
		// Persist whatever finished, including tasks cancelled above whose own
		// deferred write may not have run yet.
		s.persistFinishedTasks()
		server.Shutdown(context.Background())
	}()

	return server.ListenAndServe()
}

func hostOfListenURL(listenURL string) string {
	u, err := url.Parse(listenURL)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// isLoopbackHost mirrors upstream IsLoopbackListenAddress: only localhost and
// IP.IsLoopback count as loopback; 0.0.0.0/::/interface IPs are NOT loopback.
func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback()
}

// Auth brute-force guard (upstream RF-9/RF-74): without it an unauthenticated
// client may retry the serve token indefinitely, and an unbounded per-client map
// would itself become the memory leak.
const (
	maxAuthFailures   = 10
	authLockoutWindow = 5 * time.Minute
	maxAuthTrackers   = 1000
)

type authFailure struct {
	count int
	until time.Time
}

// authGuard tracks failed token attempts per client, with a hard cap on the
// number of tracked clients.
type authGuard struct {
	mu   sync.Mutex
	hits map[string]*authFailure
}

func newAuthGuard() *authGuard {
	return &authGuard{hits: map[string]*authFailure{}}
}

// blocked reports whether the client must wait before another attempt.
func (g *authGuard) blocked(client string) (time.Duration, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	e, ok := g.hits[client]
	if !ok || e.count < maxAuthFailures || time.Now().After(e.until) {
		return 0, false
	}
	return time.Until(e.until), true
}

// fail records one failed attempt and re-arms the lockout window.
func (g *authGuard) fail(client string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.pruneLocked()
	e, ok := g.hits[client]
	if !ok || time.Now().After(e.until) {
		e = &authFailure{}
		g.hits[client] = e
	}
	e.count++
	e.until = time.Now().Add(authLockoutWindow)
}

// reset clears the counter after a successful authentication.
func (g *authGuard) reset(client string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.hits, client)
}

// pruneLocked keeps the map bounded: expired entries go first, and if that is
// not enough one arbitrary entry is dropped so a flood of distinct clients
// cannot grow it without limit.
func (g *authGuard) pruneLocked() {
	if len(g.hits) < maxAuthTrackers {
		return
	}
	now := time.Now()
	for k, v := range g.hits {
		if now.After(v.until) {
			delete(g.hits, k)
		}
	}
	for k := range g.hits {
		if len(g.hits) < maxAuthTrackers {
			break
		}
		delete(g.hits, k)
	}
}

// SetTrustedProxy marks a reverse proxy whose X-Forwarded-For header may be
// believed when identifying clients.
func (s *APIServer) SetTrustedProxy(host string) { s.trustedProxy = strings.TrimSpace(host) }

// clientKey identifies the caller for the auth guard. RemoteAddr is authoritative
// unless the request arrived from an explicitly trusted proxy: without
// --trusted-proxy the XFF header is attacker-controlled and one client could
// otherwise forge unlimited identities and bypass the lockout.
func (s *APIServer) clientKey(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if s.trustedProxy == "" || !hostMatches(s.trustedProxy, host) {
		return host
	}
	xff := r.Header.Get("X-Forwarded-For")
	if xff == "" {
		return host
	}
	parts := strings.Split(xff, ",")
	if last := strings.TrimSpace(parts[len(parts)-1]); last != "" {
		return last
	}
	return host
}

// hostMatches reports whether a client address is the configured trusted proxy
// (an IP or hostname, optionally with a port).
func hostMatches(trusted, host string) bool {
	if h, _, err := net.SplitHostPort(trusted); err == nil {
		trusted = h
	}
	return strings.EqualFold(trusted, host)
}

// tokenMatches compares the presented serve token in constant time. A plain
// "!=" leaks the shared secret through response timing, which is measurable
// across a loopback or LAN connection.
func tokenMatches(presented, expected string) bool {
	if expected == "" {
		return true
	}
	return subtle.ConstantTimeCompare([]byte(presented), []byte(expected)) == 1
}

// tokenMiddleware guards API paths with the X-Serve-Token header, using
// path-segment boundary matching (upstream StartsWithSegments).
func (s *APIServer) tokenMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		isAPI := segmentHasPrefix(path, "/get-tasks") ||
			segmentHasPrefix(path, "/add-task") ||
			segmentHasPrefix(path, "/cancel") ||
			segmentHasPrefix(path, "/remove-finished")
		if isAPI && !tokenMatches(r.Header.Get("X-Serve-Token"), s.serveToken) {
			client := s.clientKey(r)
			if retry, blocked := s.auth.blocked(client); blocked {
				w.Header().Set("Retry-After", fmt.Sprintf("%d", int(retry.Seconds())+1))
				http.Error(w, `{"error":"too many failed attempts"}`, http.StatusTooManyRequests)
				return
			}
			s.auth.fail(client)
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		if isAPI && s.serveToken != "" {
			s.auth.reset(s.clientKey(r))
		}
		next.ServeHTTP(w, r)
	})
}

func segmentHasPrefix(path, prefix string) bool {
	return path == prefix || strings.HasPrefix(path, prefix+"/")
}

// guardMiddleware enforces request-level boundaries that must hold even when no
// token is configured. Two independent holes are closed here (upstream RF-15
// DNS rebinding and the v1.6.14 CSRF fix):
//
//   - Host: a public page can resolve its own hostname to 127.0.0.1 so the
//     browser sends the request to this loopback server. Requiring the Host
//     header to name a loopback address blocks that without affecting LAN
//     deployments (which require a token and bind a non-loopback address).
//   - Origin/Content-Type: a cross-origin fetch with Content-Type text/plain
//     is a CORS "simple request" sent without a preflight, which would let any
//     web page drive /add-task and /cancel.
func (s *APIServer) guardMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")

		if isLoopbackHost(hostOfListenURL(s.listenURL)) && !s.hostAllowed(r.Host) {
			http.Error(w, `{"error":"invalid host"}`, http.StatusForbidden)
			return
		}
		if isWriteEndpoint(r.URL.Path) {
			if !s.originAllowed(r) {
				http.Error(w, `{"error":"cross-origin request rejected"}`, http.StatusForbidden)
				return
			}
			if !isJSONContentType(r) {
				http.Error(w, `{"error":"content-type must be application/json"}`, http.StatusUnsupportedMediaType)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// hostAllowed reports whether a Host header may be served. Only loopback names
// and the configured listen host are accepted.
func (s *APIServer) hostAllowed(hostHeader string) bool {
	host := strings.ToLower(strings.TrimSpace(hostHeader))
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if host == "" || host == "localhost" || isLoopbackHost(host) {
		return true
	}
	return host == strings.ToLower(hostOfListenURL(s.listenURL))
}

// originAllowed accepts requests without an Origin header (CLI clients) and
// same-origin browser requests only.
func (s *APIServer) originAllowed(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}

func isJSONContentType(r *http.Request) bool {
	ct := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Type")))
	return strings.HasPrefix(ct, "application/json")
}

func isWriteEndpoint(path string) bool {
	return segmentHasPrefix(path, "/add-task") ||
		segmentHasPrefix(path, "/cancel") ||
		segmentHasPrefix(path, "/remove-finished")
}

// maxConcurrentQueries caps simultaneous /get-tasks requests.
const maxConcurrentQueries = 32

func (s *APIServer) handleGetTasks(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	// Bound concurrent queries: every listing walks the whole finished-task slice
	// and each response exposes absolute save paths, so unbounded parallelism is
	// both a CPU/memory amplifier and an information-disclosure one.
	select {
	case s.queryLimiter <- struct{}{}:
		defer func() { <-s.queryLimiter }()
	default:
		w.Header().Set("Retry-After", "1")
		http.Error(w, `{"error":"too many concurrent queries"}`, http.StatusTooManyRequests)
		return
	}
	s.mu.Lock()
	runningSnap := snapshotList(s.runningTasks)
	finishedSnap := snapshotList(s.finishedTasks)
	s.mu.Unlock()

	path := strings.TrimPrefix(r.URL.Path, "/get-tasks")
	path = strings.Trim(path, "/")
	switch path {
	case "":
		writeJSON(w, http.StatusOK, TaskListResponse{Running: runningSnap, Finished: finishedSnap})
		return
	case "running":
		writeJSON(w, http.StatusOK, runningSnap)
		return
	case "finished":
		writeJSON(w, http.StatusOK, finishedSnap)
		return
	}

	// Specific ID lookup: finished first (upstream: avoids same-name drift).
	for _, t := range finishedSnap {
		if t.JobID == path || t.Aid == path || t.URL == path {
			writeJSON(w, http.StatusOK, t)
			return
		}
	}
	for _, t := range runningSnap {
		if t.JobID == path || t.Aid == path || t.URL == path {
			writeJSON(w, http.StatusOK, t)
			return
		}
	}
	http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
}

func snapshotList(tasks []*DownloadTask) []DownloadTask {
	out := make([]DownloadTask, len(tasks))
	for i, t := range tasks {
		out[i] = t.Snapshot()
	}
	return out
}

// addTaskAllowedFields 是 /add-task 的白名单字段（顺序 = 报错与文档里列出的顺序）。
//
// /add-task 原本只解出 url 一个字段，把 configFile/area/interactive/filePattern 等一整族注入面
// 在构造上关死了（A-serve 第 10-14 条）。Web UI 要开表单就必须**显式**放开一批字段，于是改成
// 白名单：只认下面这 15 个（snake_case，与 config.MyOption 的 json tag 一致），其余字段 400
// 并点名——字段拼错却拿到 202 是最难查的一类问题（客户以为生效了）。
//
// 明确不开放（各自有理由，用例逐条断言 400）：
//   - interactive：任务会阻塞在 Console.ReadLine 上，不可取消地占死执行槽（A-serve 第 12 条）；
//   - file_pattern / multi_file_pattern：文件名模板 = 路径拼接面；
//   - cookie / access_token / user_agent：凭据与请求头由部署侧配置，不该由请求注入；
//   - danmaku_filter / notify_webhook：一个进日志与过滤逻辑，一个是出网回调（SSRF 面）；
//   - drm_* / mp4decrypt_path / wvd_path / insecure / decrypt_drm：本机工具路径与 TLS 降级。
var addTaskAllowedFields = []string{
	"url", "select_page", "dfn_priority", "encoding_priority", "multi_thread",
	"overwrite", "skip_mux", "skip_ai", "write_nfo", "compat",
	"use_app_api", "use_tv_api", "use_intl_api", "work_dir", "language",
}

var addTaskAllowedSet = func() map[string]bool {
	m := make(map[string]bool, len(addTaskAllowedFields))
	for _, f := range addTaskAllowedFields {
		m[f] = true
	}
	return m
}()

// 白名单字段的值域上限（各用例逐条钉住）。
const (
	// maxSelectPageItems 是 select_page 展开后的条目上限。
	//
	// 比 internal/workflow 的 MaxExpandedPages(100000) 严得多：那个上限是给「在终端里自己敲 -p」
	// 的用户定的，而 select_page 是任何能打到 serve 的客户端可控输入——一次请求让服务端展开
	// 10 万个分P（再把它们写进任务日志与状态文件）就是内存/CPU 放大面（A-serve 第 28 条）。
	// 1000 远超任何真实稿件的分P数（B 站单个稿件上限是几百），也足够表达「第 1..N 集」。
	maxSelectPageItems = 1000
	// maxPriorityLen 是 dfn_priority / encoding_priority 的长度上限，按**字符数**：
	// 白名单允许中文，按字节算会让「长度 200」对中文实际只有 66 个字。
	maxPriorityLen = 200
	// maxWorkDirLen 是 work_dir 原始输入的长度上限，按字节（它是一条路径）。
	maxWorkDirLen = 512
	// maxLanguageLen 是 language 的长度上限，按字符数（语言代码形如 zh-CN / ai-zh / jpn）。
	maxLanguageLen = 20
)

// addTaskRequest 是 /add-task 的白名单字段集。
//
// 指针表示「客户端**显式给了**这个字段」：DefaultMyOption() 里 multi_thread / skip_ai /
// force_replace_host 默认是 true，而 bool 的零值是 false——直接解码会把「没传」当成
// 「传了 false」，只发 url 的老客户端拿到的配置就不再与 2.13.0 逐字一致。
type addTaskRequest struct {
	URL              string  `json:"url"`
	SelectPage       *string `json:"select_page"`
	DfnPriority      *string `json:"dfn_priority"`
	EncodingPriority *string `json:"encoding_priority"`
	MultiThread      *bool   `json:"multi_thread"`
	Overwrite        *bool   `json:"overwrite"`
	SkipMux          *bool   `json:"skip_mux"`
	SkipAI           *bool   `json:"skip_ai"`
	WriteNFO         *bool   `json:"write_nfo"`
	Compat           *bool   `json:"compat"`
	UseAppAPI        *bool   `json:"use_app_api"`
	UseTvAPI         *bool   `json:"use_tv_api"`
	UseIntlAPI       *bool   `json:"use_intl_api"`
	WorkDir          *string `json:"work_dir"`
	Language         *string `json:"language"`
}

// addTaskOptionObserver 是 /add-task 的用例观察点：serve 用例不联网、任务也跑不起来，从任务结果
// 反推不出「请求字段 → 任务 cfg」的映射。生产路径恒为 nil（非测试代码不读它）。
var addTaskOptionObserver func(config.MyOption)

// parseAddTaskRequest 解析并校验 /add-task 的请求体（纯函数：bytes 进、结构出，离线可测）。
//
// 规则：
//  1. 未知字段 → 报错并点名（宁可报错，不让字段静默丢弃）；
//  2. url 必须非空，文案与 2.13.0 逐字一致；
//  3. 白名单字段值域逐个校验，报错说清「哪个字段、为什么」。
func parseAddTaskRequest(body []byte) (addTaskRequest, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return addTaskRequest{}, fmt.Errorf("invalid request body, 'url' required")
	}
	if unknown := unknownAddTaskFields(raw); len(unknown) > 0 {
		noun := "field"
		if len(unknown) > 1 {
			noun = "fields"
		}
		return addTaskRequest{}, fmt.Errorf("unknown %s %s in request body (allowed: %s)",
			noun, quotedJoin(unknown), strings.Join(addTaskAllowedFields, ", "))
	}

	var req addTaskRequest
	if err := json.Unmarshal(body, &req); err != nil {
		var typeErr *json.UnmarshalTypeError
		if errors.As(err, &typeErr) && typeErr.Field != "" {
			return addTaskRequest{}, fmt.Errorf("field %q 的类型不对：%v", typeErr.Field, err)
		}
		return addTaskRequest{}, fmt.Errorf("invalid request body, 'url' required")
	}
	if req.URL == "" {
		return addTaskRequest{}, fmt.Errorf("invalid request body, 'url' required")
	}
	if err := validateAddTaskFields(&req); err != nil {
		return addTaskRequest{}, err
	}
	return req, nil
}

// unknownAddTaskFields 返回请求体里的未知字段（排序后返回，保证多处拼错时报错稳定）。
func unknownAddTaskFields(raw map[string]json.RawMessage) []string {
	var unknown []string
	for name := range raw {
		if !addTaskAllowedSet[name] {
			unknown = append(unknown, name)
		}
	}
	sort.Strings(unknown)
	return unknown
}

// quotedJoin 把字段名拼成 "a", "b" 这样的清单（点名字段时带引号更好读）。
func quotedJoin(names []string) string {
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = strconv.Quote(n)
	}
	return strings.Join(quoted, ", ")
}

// validateAddTaskFields 校验并规范化白名单字段的值（纯函数）。
func validateAddTaskFields(req *addTaskRequest) error {
	// select_page 复用 CLI 的解析器（workflow.ParsePageSelection），不复制一份实现：
	// 分P表达式的规则（负号只算单段、连字符两侧允许空白、单段与累计两套上限）只有那一份。
	if req.SelectPage != nil {
		sel := strings.TrimSpace(*req.SelectPage)
		if sel != "" {
			pages, err := workflow.ParsePageSelection(sel)
			if err != nil {
				return fmt.Errorf("select_page %q 无效：%v（留空表示全部）", sel, err)
			}
			if len(pages) > maxSelectPageItems {
				return fmt.Errorf("select_page %q 展开后有 %d 个分P，超过服务端上限 %d", sel, len(pages), maxSelectPageItems)
			}
		}
		req.SelectPage = &sel
	}
	if req.DfnPriority != nil {
		v, err := validatePriorityList("dfn_priority", *req.DfnPriority)
		if err != nil {
			return err
		}
		req.DfnPriority = &v
	}
	if req.EncodingPriority != nil {
		v, err := validatePriorityList("encoding_priority", *req.EncodingPriority)
		if err != nil {
			return err
		}
		req.EncodingPriority = &v
	}
	if req.WorkDir != nil {
		v, err := validateWorkDir(*req.WorkDir)
		if err != nil {
			return err
		}
		req.WorkDir = &v
	}
	if req.Language != nil {
		v, err := validateLanguage(*req.Language)
		if err != nil {
			return err
		}
		req.Language = &v
	}
	return nil
}

// validatePriorityList 校验「编码/画质优先级」这类逗号列表：长度按字符数上限，字符只允许
// 字母、数字、中文（IsLetter 覆盖 CJK）、逗号与空格——分档名就是这些（如 "8K 4K 1080P 高码率"）。
// 其余字符（引号、换行、分号、斜杠……）一律拒绝：它们没有合法用途，却能把值带进日志与后续参数。
func validatePriorityList(field, value string) (string, error) {
	v := strings.TrimSpace(value)
	if n := utf8.RuneCountInString(v); n > maxPriorityLen {
		return "", fmt.Errorf("%s 过长（%d 字符，上限 %d）", field, n, maxPriorityLen)
	}
	for _, r := range v {
		if r == ',' || r == ' ' || unicode.IsLetter(r) || unicode.IsDigit(r) {
			continue
		}
		return "", fmt.Errorf("%s 含非法字符 %q：只允许字母、数字、中文、逗号与空格", field, string(r))
	}
	return v, nil
}

// validateWorkDir 校验并绝对化 work_dir。
//
// 语义与 CLI 一致：值最终交给 workflow 的 applyConfig（os.MkdirAll + 切换工作目录），
// 环境变量展开也由它完成，这里只做「绝对化 + 输入长度 + 控制字符」三件事。
//
// 注意：applyConfig 的切换工作目录是**进程级**副作用（CLI 下每次只跑一个任务，没问题；
// serve 下并发任务共享进程 cwd）。本期按契约放开该字段，多任务并发时的隔离问题留在 workflow 侧。
func validateWorkDir(value string) (string, error) {
	v := strings.TrimSpace(value)
	if v == "" {
		return "", nil
	}
	if len(v) > maxWorkDirLen {
		return "", fmt.Errorf("work_dir 过长（%d 字节，上限 %d）", len(v), maxWorkDirLen)
	}
	for _, r := range v {
		if r < 0x20 || r == 0x7f {
			return "", fmt.Errorf("work_dir 含控制字符（0x%02x）：路径里不允许", r)
		}
	}
	abs, err := filepath.Abs(v)
	if err != nil {
		return "", fmt.Errorf("work_dir %q 无法解析成绝对路径：%v", v, err)
	}
	return abs, nil
}

// validateLanguage 校验音频语言代码：长度 ≤ maxLanguageLen，字符只允许字母、数字、连字符与下划线
// （zh-CN / ai-zh / jpn）。这个值会进 ffmpeg 的 -metadata language= 与 mp4box 的 lang= 参数，
// 所以 ':'、'=' 这类会破坏参数结构的字符必须在门口拦掉。
func validateLanguage(value string) (string, error) {
	v := strings.TrimSpace(value)
	if n := utf8.RuneCountInString(v); n > maxLanguageLen {
		return "", fmt.Errorf("language 过长（%d 字符，上限 %d）", n, maxLanguageLen)
	}
	for _, r := range v {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_' {
			continue
		}
		return "", fmt.Errorf("language 含非法字符 %q：只允许字母、数字、连字符与下划线（如 zh-CN / jpn）", string(r))
	}
	return v, nil
}

// applyAddTaskOption 把白名单字段套到默认配置上：**只有客户端显式给了的字段**才覆盖默认值，
// 因此只发 url 的老客户端拿到的 cfg 与改前逐字段相同（= DefaultMyOption() + URL）。
func applyAddTaskOption(cfg *config.MyOption, req addTaskRequest) {
	set := func(dst *string, src *string) {
		if src != nil {
			*dst = *src
		}
	}
	setBool := func(dst *bool, src *bool) {
		if src != nil {
			*dst = *src
		}
	}
	cfg.URL = req.URL
	set(&cfg.SelectPage, req.SelectPage)
	set(&cfg.DfnPriority, req.DfnPriority)
	set(&cfg.EncodingPriority, req.EncodingPriority)
	setBool(&cfg.MultiThread, req.MultiThread)
	setBool(&cfg.Overwrite, req.Overwrite)
	setBool(&cfg.SkipMux, req.SkipMux)
	setBool(&cfg.SkipAi, req.SkipAI)
	setBool(&cfg.WriteNFO, req.WriteNFO)
	setBool(&cfg.Compat, req.Compat)
	setBool(&cfg.UseAppAPI, req.UseAppAPI)
	setBool(&cfg.UseTvAPI, req.UseTvAPI)
	setBool(&cfg.UseIntlAPI, req.UseIntlAPI)
	set(&cfg.WorkDir, req.WorkDir)
	set(&cfg.Language, req.Language)
}

// writeAddTaskBadRequest 写 /add-task 的 400 错误体。消息先经 json.Marshal：字段级报错里带引号
// （点名 "cookie" 这类字段），手拼字符串会拼出非法 JSON。
func writeAddTaskBadRequest(w http.ResponseWriter, msg string) {
	body, err := json.Marshal(struct {
		Error string `json:"error"`
	}{msg})
	if err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}
	http.Error(w, string(body), http.StatusBadRequest)
}

func (s *APIServer) handleAddTask(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBodySize))
	if err != nil {
		// An oversized body is a 413, not a generic 400: clients need to tell
		// "too big" from "malformed" to decide whether to retry.
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, `{"error":"request body too large"}`, http.StatusRequestEntityTooLarge)
			return
		}
		writeAddTaskBadRequest(w, "invalid request body, 'url' required")
		return
	}
	req, err := parseAddTaskRequest(body)
	if err != nil {
		writeAddTaskBadRequest(w, err.Error())
		return
	}

	opt := config.DefaultMyOption()
	applyAddTaskOption(&opt, req)
	if addTaskOptionObserver != nil {
		addTaskOptionObserver(opt)
	}

	// Accept-queue limit: 429 when the pending queue is full (upstream).
	select {
	case s.acceptLimiter <- struct{}{}:
	default:
		w.Header().Set("Retry-After", "5")
		http.Error(w, `{"error":"任务队列已满，请稍后再试"}`, http.StatusTooManyRequests)
		return
	}

	task := &DownloadTask{
		JobID:          generateJobID(),
		Aid:            req.URL,
		URL:            req.URL,
		Status:         StatusQueued,
		TaskCreateTime: time.Now().Unix(),
		mu:             &sync.Mutex{},
	}
	base := s.taskBaseCtx
	if base == nil {
		base = context.Background()
	}
	ctx, cancel := context.WithCancel(base)
	task.cancelFn = cancel

	s.mu.Lock()
	s.runningTasks = append(s.runningTasks, task)
	s.mu.Unlock()

	// 先发「任务被接受」再启协程：事件的先后顺序与状态推进一致（Status=Queued）。
	s.publishTaskEvent(EventTaskStart, task, StateProgress)

	s.taskWG.Add(1)
	go s.processTask(ctx, task, opt)

	writeJSON(w, http.StatusAccepted, AddTaskResponse{TaskID: task.JobID})
}

// acquireSlot 取一个执行槽（并发上限 maxConcurrent），ctx 取消时返回 false。
//
// 抽成方法是为了让并发闸门可以**离线量测与回归**（O7，见 concurrency_test.go）：闸门此前内联在
// processTask 里，只能靠真实网络任务才走得到，没有用例守「并发不超过上限」这条不变量。
func (s *APIServer) acquireSlot(ctx context.Context) bool {
	select {
	case s.semaphore <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

// releaseSlot 归还执行槽。
func (s *APIServer) releaseSlot() { <-s.semaphore }

// processTask runs a real download via the workflow (upstream
// ProcessDownloadTaskAsync): parse URL → fetch metadata → download pages.
//
// opt 是 handleAddTask 按白名单构造好的任务配置（默认值 + 客户端显式给的字段），
// opt.URL 就是要处理的地址。
func (s *APIServer) processTask(ctx context.Context, task *DownloadTask, opt config.MyOption) {
	url := opt.URL
	defer s.taskWG.Done()
	defer func() { <-s.acceptLimiter }()
	defer s.persistFinishedTasks()

	if !s.acquireSlot(ctx) {
		task.SetStatus(StatusCancelled)
		s.finishTask(task, "")
		s.publishTaskEvent(EventTaskDone, task, StateDone)
		s.sendCallback(task)
		return
	}
	defer s.releaseSlot()

	util.Log("处理任务 %s: %s", task.JobID, url)
	client := util.NewHTTPClient(
		func() bool { return false },
		func() string { return "" },
		func(format string, args ...interface{}) { util.LogDebug(format, args...) },
	)

	// Resolve the URL up front so failures land in the task state.
	resolved, err := workflow.ResolveURL(ctx, client, url)
	if err != nil {
		task.SetStatus(StatusFailed)
		s.finishTask(task, err.Error())
		s.publishTaskEvent(EventTaskFailed, task, StateDone)
		s.sendCallback(task)
		return
	}
	if resolved == "" {
		task.SetStatus(StatusFailed)
		s.finishTask(task, "无法解析目标 URL")
		s.publishTaskEvent(EventTaskFailed, task, StateDone)
		s.sendCallback(task)
		return
	}
	task.mu.Lock()
	task.Aid = resolved
	task.mu.Unlock()

	cfg := opt
	wf := workflow.New(cfg, client)
	wf.MetaHandler = func(v *entity.VInfo) {
		task.mu.Lock()
		task.Title = v.Title
		task.Pic = v.Pic
		task.VideoPubTime = v.PubTime
		task.mu.Unlock()
		s.publishTaskEvent(EventTaskProgress, task, StateProgress)
	}
	// 每件产物落盘都推一条进度事件：产物字节数是 serve 侧唯一可得的进度信号。
	wf.OnSaved = func(path string) { s.onArtifactSaved(task, path) }

	task.SetStatus(StatusRunning)
	s.publishTaskEvent(EventTaskProgress, task, StateProgress)
	// 任务 Q：把逐字节进度观察者装到任务 ctx 上（在 workflow.Run 之前），下载层在每次
	// DownloadFile 里从 ctx 取出它，事件经 hub 变成新的 task_progress 帧（见 progress.go）。
	err = wf.Run(s.taskDownloadContext(ctx, task))
	if err != nil {
		status, msg := classifyTaskCancellation(ctx, err)
		task.SetStatus(status)
		s.finishTask(task, msg)
		if status == StatusFailed {
			s.publishTaskEvent(EventTaskFailed, task, StateDone)
		} else {
			s.publishTaskEvent(EventTaskDone, task, StateDone)
		}
		s.sendCallback(task)
		return
	}

	task.SetStatus(StatusSucceeded)
	task.mu.Lock()
	task.TaskFinishTime = time.Now().Unix()
	task.Progress = 1.0
	task.mu.Unlock()
	s.publishTaskEvent(EventTaskDone, task, StateDone)
	s.finishTask(task, "")
	s.sendCallback(task)
}

// maskTaskError removes local absolute paths from an error message before it is
// served to API clients: a failure such as
// "open /home/user/Downloads/BV1xx/...mp4: no such file" leaks the server's
// directory layout to any caller that can reach /get-tasks.
func maskTaskError(msg string) string {
	if msg == "" {
		return msg
	}
	if dir := util.ExecutableDir(); len(dir) > 1 {
		msg = strings.ReplaceAll(msg, dir, "<app-dir>")
	}
	if wd, err := os.Getwd(); err == nil && len(wd) > 1 {
		msg = strings.ReplaceAll(msg, wd, "<work-dir>")
	}
	return util.SanitizeLogString(msg)
}

// finishTask moves a task to the finished list and sets its terminal fields.
// classifyTaskCancellation 对应上游 BBDownApiServer.ClassifyCancellation：
// 只有「任务确实被取消」（ctx 是 Canceled，而非 DeadlineExceeded）才算 Cancelled 并给出
// 统一文案「已取消」；HttpClient 超时抛出的取消类错误其 ctx 并未取消，必须归为 Failed——
// 否则任务被误标「已取消」，把真实失败原因掩盖成用户操作。
func classifyTaskCancellation(ctx context.Context, err error) (TaskStatus, string) {
	if ctx.Err() == context.Canceled {
		return StatusCancelled, "已取消"
	}
	if err != nil {
		return StatusFailed, err.Error()
	}
	return StatusFailed, ""
}

func (s *APIServer) finishTask(task *DownloadTask, errMsg string) {
	task.mu.Lock()
	if task.TaskFinishTime == 0 {
		task.TaskFinishTime = time.Now().Unix()
	}
	if errMsg != "" {
		task.ErrorMessage = maskTaskError(errMsg)
	}
	task.mu.Unlock()
	if task.cancelFn != nil {
		task.cancelFn()
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for i, t := range s.runningTasks {
		if t.JobID == task.JobID {
			s.runningTasks = append(s.runningTasks[:i], s.runningTasks[i+1:]...)
			break
		}
	}
	s.finishedTasks = append(s.finishedTasks, task)
}

func (s *APIServer) handleCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodDelete {
		methodNotAllowed(w)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/cancel/")
	if id == "" {
		http.Error(w, `{"error":"task id required"}`, http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.runningTasks {
		if t.JobID == id || t.Aid == id || t.URL == id {
			if t.cancelFn != nil {
				t.cancelFn()
			}
			writeJSON(w, http.StatusOK, map[string]string{"status": "cancelled"})
			return
		}
	}
	http.Error(w, `{"error":"task not found or already finished"}`, http.StatusNotFound)
}

func (s *APIServer) handleRemoveFinished(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		methodNotAllowed(w)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/remove-finished/")
	id = strings.Trim(id, "/")

	s.mu.Lock()
	defer s.mu.Unlock()

	switch id {
	case "":
		s.finishedTasks = nil
	case "failed":
		out := s.finishedTasks[:0]
		for _, t := range s.finishedTasks {
			if t.Snapshot().Status != StatusFailed {
				out = append(out, t)
			}
		}
		s.finishedTasks = out
	default:
		for i, t := range s.finishedTasks {
			if t.JobID == id || t.Aid == id || t.URL == id {
				s.finishedTasks = append(s.finishedTasks[:i], s.finishedTasks[i+1:]...)
				break
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *APIServer) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// webhookLogTarget 返回回调地址适合写日志的形态：只保留 scheme://host[:port]，
// 丢弃 path、query 与 userinfo。回调 URL 常带 token（查询参数或 userinfo），
// 原样写进日志就是凭据泄漏（upstream #114 的 WebhookTargetForLog 同款策略）。
func webhookLogTarget(raw string) string {
	if raw == "" {
		return "(未配置)"
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "(无效回调地址)"
	}
	// 主机名大小写不敏感，统一小写让日志可比较（IPv6 十六进制同理）。
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host)
}

// callbackFailureLog 返回回调失败日志的正文：只保留错误类型，绝不回显错误值——
// *url.Error 的 Error() 会带上完整 URL（含 query token），写进日志就是泄漏（upstream #114）。
func callbackFailureLog(err error) string {
	return fmt.Sprintf("回调失败（%T）", err)
}

// newCallbackTransport 返回回调专用 Transport：只拨已校验的 IP、不跟随重定向、
// **不走系统代理**。显式把 Proxy 置 nil 是安全边界：若继承环境代理，回调会绕过
// 上面「拨校验后 IP」的防护，走到一个未校验的代理地址（upstream #114 的回调直连）。
func newCallbackTransport(dialAddr string) *http.Transport {
	return &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			// Dial the VALIDATED IP: TLS SNI and the Host header still come
			// from the original URL (upstream ConnectCallback).
			var d net.Dialer
			return d.DialContext(ctx, network, dialAddr)
		},
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
	}
}

// sendCallback posts the task snapshot to the webhook on EVERY terminal state
// (upstream), with the full SSRF guard: literal-IP and DNS branches are checked
// with the same rules as upstream, the connection is dialed to a validated IP,
// redirects are disabled and the timeout is 2 minutes.
func (s *APIServer) sendCallback(task *DownloadTask) {
	if s.notifyWebhook == "" {
		return
	}
	u, err := url.Parse(s.notifyWebhook)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		util.LogWarn("回调地址不合法，已跳过: %s", webhookLogTarget(s.notifyWebhook))
		return
	}
	host := u.Hostname()
	port := u.Port()
	if port == "" {
		if u.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}

	// Resolve/validate the dial target (upstream SendCallbackAsync).
	var target net.IP
	if literal := net.ParseIP(host); literal != nil {
		literal = normalizeMappedIP(literal)
		if isUnsafeLiteralIP(literal) {
			util.LogWarn("回调地址是敏感字面 IP，已跳过本次回调: %s", webhookLogTarget(s.notifyWebhook))
			return
		}
		target = literal
	} else {
		if strings.EqualFold(host, "localhost") {
			util.LogWarn("回调地址不安全（内网地址），已跳过: %s", webhookLogTarget(s.notifyWebhook))
			return
		}
		addrs, err := dnsLookupIP(context.Background(), "ip", host)
		if err != nil {
			util.LogWarn("回调域名无法解析，已跳过: %s", webhookLogTarget(s.notifyWebhook))
			return
		}
		var chosen net.IP
		for _, a := range addrs {
			a = normalizeMappedIP(a)
			if isBlockedAddress(a) {
				util.LogWarn("回调地址不安全（内网地址），已跳过: %s", webhookLogTarget(s.notifyWebhook))
				return
			}
			if chosen == nil {
				chosen = a
			}
		}
		target = chosen
	}

	snapshot := task.Snapshot()
	body, _ := json.Marshal(snapshot)

	ctx, cancel := context.WithTimeout(context.Background(), callbackTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.notifyWebhook, strings.NewReader(string(body)))
	if err != nil {
		// 只记目标与错误类型：错误的 Error() 可能带完整 URL（含 token）。
		util.LogDebug("%s（目标 %s）", callbackFailureLog(err), webhookLogTarget(s.notifyWebhook))
		return
	}
	req.Header.Set("Content-Type", "application/json")

	dialAddr := net.JoinHostPort(target.String(), port)
	client := &http.Client{
		Timeout: callbackTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse // no redirects
		},
		Transport: newCallbackTransport(dialAddr),
	}
	resp, err := client.Do(req)
	if err != nil {
		util.LogDebug("%s（目标 %s）", callbackFailureLog(err), webhookLogTarget(s.notifyWebhook))
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		util.LogDebug("回调返回 HTTP %d", resp.StatusCode)
	}
}

// isSafeCallbackURL validates a callback URL without network I/O for the literal
// branch; the domain branch is checked at send time (upstream IsSafeCallbackUrl).
func isSafeCallbackURL(raw string) bool {
	if raw == "" {
		return true
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	host := u.Hostname()
	if literal := net.ParseIP(host); literal != nil {
		literal = normalizeMappedIP(literal)
		return !isUnsafeLiteralIP(literal)
	}
	if strings.EqualFold(host, "localhost") {
		return false
	}
	addrs, err := dnsLookupIP(context.Background(), "ip", host)
	if err != nil {
		return false
	}
	for _, a := range addrs {
		if isBlockedAddress(normalizeMappedIP(a)) {
			return false
		}
	}
	return true
}

// dnsLookupIP is the DNS resolver used by the callback SSRF checks; it is a
// variable so tests can stub it (production uses net.DefaultResolver).
var dnsLookupIP = func(ctx context.Context, network, host string) ([]net.IP, error) {
	return net.DefaultResolver.LookupIP(ctx, network, host)
}

// normalizeMappedIP maps IPv4-mapped IPv6 addresses back to IPv4 (upstream).
func normalizeMappedIP(ip net.IP) net.IP {
	if v4 := ip.To4(); v4 != nil {
		return v4
	}
	return ip
}

// isUnsafeLiteralIP blocks loopback / link-local / 169.254/16 / unspecified for
// EXPLICITLY configured literal IPs (upstream: RFC1918 literals are allowed —
// no DNS rebinding is possible for literal addresses).
func isUnsafeLiteralIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
		return true
	}
	if v4 := ip.To4(); v4 != nil {
		return v4[0] == 169 && v4[1] == 254 // 169.254.0.0/16 cloud metadata
	}
	return false
}

// isBlockedAddress blocks loopback / link-local / 169.254/16 / RFC1918 / CGNAT
// 100.64/10 / IPv6 ULA fc00::/7 (upstream IsBlockedAddress, domain branch).
func isBlockedAddress(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
		return true
	}
	if v4 := ip.To4(); v4 != nil {
		// 169.254.0.0/16 cloud metadata
		if v4[0] == 169 && v4[1] == 254 {
			return true
		}
		// RFC1918
		if v4[0] == 10 {
			return true
		}
		if v4[0] == 172 && v4[1] >= 16 && v4[1] <= 31 {
			return true
		}
		if v4[0] == 192 && v4[1] == 168 {
			return true
		}
		// CGNAT 100.64.0.0/10
		if v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127 {
			return true
		}
		return false
	}
	// IPv6 ULA fc00::/7
	return len(ip) == 16 && (ip[0]&0xfe) == 0xfc
}

// ---- Finished-task persistence (upstream bbdown-tasks.json) ----

func (s *APIServer) persistFinishedTasks() {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()

	s.mu.Lock()
	list := snapshotList(s.finishedTasks)
	// The in-memory list is what /get-tasks serves and it used to grow without
	// bound for the life of the process; apply the same cap here (only the
	// oldest entries are dropped, so the remaining pointers stay valid).
	if len(s.finishedTasks) > maxFinishedTasks {
		// Retention is keyed on creation time, not on list position: the slice is in
		// completion order, so dropping the head would discard a task that was
		// created earlier but happened to finish later (upstream sorts by
		// TaskCreateTime before trimming).
		byCreate := append([]*DownloadTask(nil), s.finishedTasks...)
		sort.SliceStable(byCreate, func(i, j int) bool { return byCreate[i].TaskCreateTime < byCreate[j].TaskCreateTime })
		s.finishedTasks = byCreate[len(byCreate)-maxFinishedTasks:]
	}
	s.mu.Unlock()

	// Retention: drop entries created more than 30 days ago, keep the newest 1000.
	// Both bounds are keyed on creation time for the same reason as above.
	cutoff := time.Now().Add(-finishedRetention).Unix()
	kept := list[:0]
	for _, t := range list {
		if t.TaskCreateTime >= cutoff {
			kept = append(kept, t)
		}
	}
	if len(kept) > maxFinishedTasks {
		sort.SliceStable(kept, func(i, j int) bool { return kept[i].TaskCreateTime < kept[j].TaskCreateTime })
		kept = kept[len(kept)-maxFinishedTasks:]
	}

	data, err := json.MarshalIndent(kept, "", "  ")
	if err != nil {
		return
	}
	// A unique temp name: a shared "<file>.tmp" let two concurrent writers race
	// onto the same path and rename a half-written file into place.
	tmp := s.taskFile + ".tmp-" + generateJobID()
	// 任务记录含任务参数与状态：Unix 上属主私有（0600），并压住 umask 造成的宽松权限。
	if err := os.WriteFile(tmp, data, util.SecureFileMode()); err != nil {
		return
	}
	util.TightenFilePermissions(tmp)
	if err := os.Rename(tmp, s.taskFile); err != nil {
		os.Remove(tmp)
	}
}

func (s *APIServer) loadFinishedTasks() {
	// 启动时收紧历史任务记录的权限（升级前可能是 0644；upstream #113 同样处理既有文件）。
	util.TightenFilePermissions(s.taskFile)
	data, err := os.ReadFile(s.taskFile)
	if err != nil {
		return
	}
	var list []DownloadTask
	if err := json.Unmarshal(data, &list); err != nil {
		util.LogDebug("读取任务持久化文件失败（已忽略）: %v", err)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range list {
		t := list[i]
		t.mu = &sync.Mutex{}
		s.finishedTasks = append(s.finishedTasks, &t)
	}
}

func methodNotAllowed(w http.ResponseWriter) {
	http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// generateJobID returns a 32-char hex GUID (upstream Guid.NewGuid().ToString("N")).
func generateJobID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
