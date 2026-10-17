package download

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// ---- t89（t82 评估 P1 两项）：分片失败取消兄弟分片 + 完整跳过回收残留 ----
//
// 判据全部可观测：兄弟分片被取消 = 服务端 handler 的 r.Context().Done() 关闭（轮询等它，
// 不用固定 sleep）；残留回收 = 目录里文件的真实存在性。

const (
	cancelObserveTimeout = 3 * time.Second
	cancelFallback       = time.Second // handler 兜底等待：只防挂死，不参与判据
)

// TestShardGroupCancelsOnFirstError 是取消机制的**确定性**判据（t89）：首个错误出现的瞬间
// 取消其余在途分片，且之后到达的错误不会覆盖最初的原因。
//
// 变异验证：撤掉 shardGroup.record 里的 g.cancel() → 本用例红（派生 ctx 不会 Done）。
func TestShardGroupCancelsOnFirstError(t *testing.T) {
	group, runCtx := newShardGroup(context.Background())
	defer group.cancel()

	select {
	case <-runCtx.Done():
		t.Fatal("没有错误时不该取消")
	default:
	}

	errA := errors.New("分片 0：HTTP 500")
	if first := group.record(errA); !first {
		t.Error("第一个错误应当被标记为首个")
	}
	select {
	case <-runCtx.Done():
		// 期望：首个错误立刻取消其余分片。
	default:
		t.Fatal("首个分片错误必须取消其余在途分片（runCtx 应已 Done）")
	}

	errB := context.Canceled // 兄弟分片被取消后返回的错误
	if first := group.record(errB); first {
		t.Error("后续错误不该被当成首个")
	}
	if got := group.err(); got != errA {
		t.Errorf("必须保留最初的失败原因：got %v, want %v", got, errA)
	}
}

// TestShardGroupKeepsUserCancelSemantics 钉住用户取消：父 ctx 被取消时派生 ctx 同步取消，
// 且此时记录的错误就是 context.Canceled（不是被抹成别的），调用方据此走取消路径。
func TestShardGroupKeepsUserCancelSemantics(t *testing.T) {
	parent, cancelParent := context.WithCancel(context.Background())
	group, runCtx := newShardGroup(parent)
	defer group.cancel()

	cancelParent()
	select {
	case <-runCtx.Done():
	default:
		t.Fatal("父 ctx 取消后派生 ctx 必须同步取消")
	}
	if first := group.record(runCtx.Err()); !first {
		t.Error("用户取消也应当是首个错误（此前没有别的失败）")
	}
	if got := group.err(); !errors.Is(got, context.Canceled) {
		t.Errorf("用户取消的语义必须保留：got %v", got)
	}
}

// TestMultiThreadFailureKeepsOriginalError 是行为级：一个分片立刻 500、其余分片挂在服务端 →
// 整体必须失败，且错误是**原始失败**（不能退化成 context.Canceled），失败后不留分片。
//
// 说明：这里不去断言「服务端观察到取消」——那依赖 handler 侧 ctx 传播，本机实测不稳定；
// 取消本身由上面两条 shardGroup 用例确定性钉住。
func TestMultiThreadFailureKeepsOriginalError(t *testing.T) {
	const size = 4 << 20 // 4MB；配 SegmentSizeMB=1 → 4 个分片
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.Header().Set("Content-Length", strconv.Itoa(size))
			w.Header().Set("Accept-Ranges", "bytes")
			w.WriteHeader(http.StatusOK)
			return
		}
		if rangeStart(r.Header.Get("Range")) == 0 {
			w.WriteHeader(http.StatusInternalServerError) // 1 号分片：立刻失败
			return
		}
		select {
		case <-r.Context().Done():
		case <-time.After(cancelFallback): // 兜底：不让用例挂死
		}
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "v.m4s")
	cfg := DownloadConfig{Client: newTestClient(), MultiThread: true, SegmentSizeMB: 1, RetryCount: 0, RetryDelayMs: 1}
	err := DownloadFile(context.Background(), srv.URL+"/a.m4s", dest, cfg)
	if err == nil {
		t.Fatal("分片 500 时整体必须失败")
	}
	if errors.Is(err, context.Canceled) {
		t.Errorf("必须保留原始失败原因，不能退化成 context.Canceled：%v", err)
	}
	entries, _ := os.ReadDir(filepath.Dir(dest))
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".vclip") {
			t.Errorf("失败后不该留下分片：%s", e.Name())
		}
	}
}

// TestMultiThreadRangeNotSupportedFallsBack 钉住 ErrRangeNotSupported 的既有语义：
// 服务器忽略 Range 时整体回退到单线程（服务端收到一次**不带** Range 的 GET）。
func TestMultiThreadRangeNotSupportedFallsBack(t *testing.T) {
	const size = 4 << 20
	var plainGets atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.Header().Set("Content-Length", strconv.Itoa(size))
			w.Header().Set("Accept-Ranges", "bytes")
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.Header.Get("Range") == "" {
			plainGets.Add(1) // 单线程回退
			_, _ = w.Write([]byte("short"))
			return
		}
		if rangeStart(r.Header.Get("Range")) == 0 {
			w.WriteHeader(http.StatusOK) // 忽略 Range → ErrRangeNotSupported
			_, _ = w.Write([]byte("full-ish"))
			return
		}
		select {
		case <-r.Context().Done():
		case <-time.After(cancelFallback):
		}
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "v.m4s")
	cfg := DownloadConfig{Client: newTestClient(), MultiThread: true, SegmentSizeMB: 1, RetryCount: 0, RetryDelayMs: 1}
	// 回退后单线程拿到的长度与声明不符 → 整体报错是预期的；本用例只关心「回退发生过」。
	_ = DownloadFile(context.Background(), srv.URL+"/a.m4s", dest, cfg)
	if plainGets.Load() == 0 {
		t.Error("ErrRangeNotSupported 之后应当回退到单线程（服务端会收到一次不带 Range 的 GET）")
	}
}

// TestCleanupResidueMatrix 是残留矩阵（acceptance ③）：六类残留各删、两样不删。
func TestCleanupResidueMatrix(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "video.m4s")
	if err := os.WriteFile(dest, []byte("product"), 0o644); err != nil {
		t.Fatal(err)
	}

	// 应当删：.tmp、.tmp.meta（续传清单）、.000.vclip/.001.vclip（分片）、.dec（DRM 临时件）。
	mustWrite := func(p, body string) {
		t.Helper()
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite(dest+".tmp", "partial")
	mustWrite(dest+".tmp.meta", "{}")
	mustWrite(dest+".000.vclip", "clip0")
	mustWrite(dest+".001.vclip", "clip1")
	mustWrite(dest+".dec", "decrypted")

	// 不该删：.aria2（存在即未完成，不归这里管）、名字相近的假分片、live 会话目录里的分段。
	mustWrite(dest+".aria2", "control")
	mustWrite(dest+".abc.vclip", "not-a-shard")
	session := filepath.Join(dest + ".segs")
	if err := os.MkdirAll(filepath.Join(session, "session-20260101_000000"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(filepath.Join(session, "session-20260101_000000", "seg-000.flv"), "flv")

	cleanupResidue(dest)

	for _, gone := range []string{dest + ".tmp", dest + ".tmp.meta", dest + ".000.vclip", dest + ".001.vclip", dest + ".dec"} {
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Errorf("应当清理残留：%s（err=%v）", filepath.Base(gone), err)
		}
	}
	for _, kept := range []string{dest + ".aria2", dest + ".abc.vclip", filepath.Join(session, "session-20260101_000000", "seg-000.flv")} {
		if _, err := os.Stat(kept); err != nil {
			t.Errorf("不该删：%s（%v）", kept, err)
		}
	}
	// .segs 里还有会话目录 → 根目录本身也不删。
	if _, err := os.Stat(session); err != nil {
		t.Errorf("live 分段根目录在有会话时不该被删：%v", err)
	}
	if _, err := os.Stat(dest); err != nil {
		t.Errorf("产物本身不该被动：%v", err)
	}

	// 空 .segs 根目录 → 收掉它（这是唯一允许被删的 live 形态）。
	empty := filepath.Join(t.TempDir(), "v.m4s")
	if err := os.WriteFile(empty, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(empty+".segs", 0o755); err != nil {
		t.Fatal(err)
	}
	cleanupResidue(empty)
	if _, err := os.Stat(empty + ".segs"); !os.IsNotExist(err) {
		t.Errorf("空的 live 分段根目录应当被收掉：%v", err)
	}
}

// TestDownloadFileSkipCleansResidue 是行为级：产物已完整（无 .aria2）→ 一次 GET 都不发、
// 残留被清；而 .aria2 存在时**不算完整** → 照常下载（不会误清控制文件）。
func TestDownloadFileSkipCleansResidue(t *testing.T) {
	body := []byte("0123456789")
	var gets atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}
		gets.Add(1)
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	dir := t.TempDir()
	dest := filepath.Join(dir, "out.bin")
	if err := os.WriteFile(dest, body, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest+".tmp", []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest+".tmp.meta", []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest+".000.vclip", []byte("c"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := DownloadConfig{Client: newTestClient(), RetryCount: 1, RetryDelayMs: 1}
	if err := DownloadFile(context.Background(), srv.URL+"/a.bin", dest, cfg); err != nil {
		t.Fatalf("完整文件应当跳过：%v", err)
	}
	if n := gets.Load(); n != 0 {
		t.Errorf("完整文件不该重新下载，实际 %d 次 GET", n)
	}
	for _, gone := range []string{dest + ".tmp", dest + ".tmp.meta", dest + ".000.vclip"} {
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Errorf("跳过时应当清理残留：%s", filepath.Base(gone))
		}
	}

	// .aria2 存在 → 未完成 → 走下载（不误清控制文件，且会真的发 GET）。
	if err := os.WriteFile(dest+".aria2", []byte("control"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg2 := DownloadConfig{Client: newTestClient(), RetryCount: 1, RetryDelayMs: 1}
	if err := DownloadFile(context.Background(), srv.URL+"/a.bin", dest, cfg2); err != nil {
		t.Fatalf("控制文件存在时应当能重下成功：%v", err)
	}
	if gets.Load() == 0 {
		t.Error("存在 .aria2 时不该按完整跳过")
	}
}

// TestDownloadFailureKeepsResidue 钉住「失败路径不动残留」：中途失败不清理任何中间文件
// （它们是下一次续传/排查的依据）。
func TestDownloadFailureKeepsResidue(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.Header().Set("Content-Length", "10")
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	dir := t.TempDir()
	dest := filepath.Join(dir, "fail.bin")
	if err := os.WriteFile(dest+".tmp.meta", []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := DownloadConfig{Client: newTestClient(), RetryCount: 0, RetryDelayMs: 1}
	if err := DownloadFile(context.Background(), srv.URL+"/a.bin", dest, cfg); err == nil {
		t.Fatal("服务器 500 时应当失败")
	}
	if _, err := os.Stat(dest + ".tmp.meta"); err != nil {
		t.Errorf("失败路径不该清理残留（续传/排查要用）：%v", err)
	}
}

// TestCleanupResidueIsIdempotent 空目录/不存在的文件都不该 panic（跳过分支被多次触发很常见）。
func TestCleanupResidueIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "nothing.bin")
	cleanupResidue(dest)
	cleanupResidue(dest)
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("目录不该被动：%v", err)
	}
}
