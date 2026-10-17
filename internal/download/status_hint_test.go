package download

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QC3284/BBDown-Go/internal/util"
)

// 任务 F（C 代理遗留项 C-4）：下载层的 412 也要带可操作提示，且与 API 层同一份文案。
//
// 现状：API 层（util.statusSuffix）会补「疑似风控拦截…」，下载层（singleDownload /
// downloadRange）只报裸状态码——用户不知道该等还是该换网络出口。
//
// 变异验证：把 newHTTPStatusError 里的 util.StatusHint(code) 去掉（＝旧行为）→ 本文件
// 两个 412 用例变红，且是断言红（消息里没有提示关键词），不是构建错误；
// 把提示改成不分状态码一律追加（util.StatusHint(http.StatusPreconditionFailed)）
// → TestDownload404HasNoRiskControlHint 变红。

// assertRiskControlHint 断言 err 带着 412 的可操作提示，且错误链语义不变。
func assertRiskControlHint(t *testing.T, err error) {
	t.Helper()
	if !strings.Contains(err.Error(), "412") {
		t.Errorf("错误消息必须带状态码：%q", err)
	}
	// 单一来源：提示必须就是 util 那份——两层各写一份中文文案，下一次改口径就会漂移。
	if hint := util.StatusHint(http.StatusPreconditionFailed); hint == "" || !strings.Contains(err.Error(), hint) {
		t.Errorf("下载层 412 必须复用 util.StatusHint(%q)，实际 %q", hint, err)
	}
	// 用户可见的关键词，独立于实现钉住行为。
	if msg := err.Error(); !strings.Contains(msg, "风控") || !strings.Contains(msg, "更换网络出口") {
		t.Errorf("412 必须给出可操作提示（等一会儿/换出口）：%q", msg)
	}
	// 不能破坏类型判断：错误分类（412 退避 / 404 换候选）全靠 errors.As 取 code。
	var se *httpStatusError
	if !errors.As(err, &se) || se.code != http.StatusPreconditionFailed {
		t.Errorf("412 仍须是 code=412 的 *httpStatusError，实际 %#v", err)
	}
}

// 单线程路径：服务器恒 412 → 重试耗尽后的最终错误必须带提示。
func TestDownload412ExhaustedRetriesCarriesActionableHint(t *testing.T) {
	old := riskControlBackoffBase
	riskControlBackoffBase = time.Millisecond // 跑满 3 次尝试；退避本身不在本用例的量测范围
	t.Cleanup(func() { riskControlBackoffBase = old })

	body := []byte("blocked")
	var gets atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.Header().Set("Content-Length", strconv.Itoa(len(body)))
			w.WriteHeader(http.StatusOK)
			return
		}
		gets.Add(1)
		w.WriteHeader(http.StatusPreconditionFailed)
	}))
	defer srv.Close()

	cfg := DownloadConfig{Client: newTestClient(), RetryCount: 3, RetryDelayMs: 1}
	err := DownloadFile(context.Background(), srv.URL+"/a.m4s", filepath.Join(t.TempDir(), "out.m4s"), cfg)
	if err == nil {
		t.Fatal("恒 412 必须报错")
	}
	assertRiskControlHint(t, err)
	// 自动 UA 可轮换 → 单线程路径跑满 --retry-count 次；确认这里测的确实是「耗尽重试后」。
	if got := gets.Load(); got != 3 {
		t.Errorf("412 应重试到 --retry-count 用尽（GET %d 次，期望 3）：%q", got, err)
	}
}

// 分片路径（downloadRange）走的是另一处构造点，同样要带提示。
func TestMultiThread412CarriesActionableHint(t *testing.T) {
	old := riskControlBackoffBase
	riskControlBackoffBase = time.Millisecond
	t.Cleanup(func() { riskControlBackoffBase = old })

	// HEAD 成功 + Accept-Ranges ⇒ 走分片路径（见 retry_policy_range_test.go 的 rangeFailServer）。
	body := make([]byte, 3<<20)
	srv, _, _ := rangeFailServer(t, body, http.StatusPreconditionFailed)
	cfg := DownloadConfig{Client: newTestClient(), MultiThread: true, SegmentSizeMB: 1, RetryCount: 3, RetryDelayMs: 1}
	err := DownloadFile(context.Background(), srv.URL+"/v.m4s", filepath.Join(t.TempDir(), "out.bin"), cfg)
	if err == nil {
		t.Fatal("分片恒 412 必须报错")
	}
	assertRiskControlHint(t, err)
}

// 提示只对 412 生效：404 照旧原样抛出，否则会把「镜像没覆盖该对象」误导成风控。
func TestDownload404HasNoRiskControlHint(t *testing.T) {
	body := []byte("missing")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.Header().Set("Content-Length", strconv.Itoa(len(body)))
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	cfg := DownloadConfig{Client: newTestClient(), RetryCount: 1, RetryDelayMs: 1}
	err := DownloadFile(context.Background(), srv.URL+"/a.m4s", filepath.Join(t.TempDir(), "out.m4s"), cfg)
	if err == nil {
		t.Fatal("404 必须报错")
	}
	var se *httpStatusError
	if !errors.As(err, &se) || se.code != http.StatusNotFound {
		t.Fatalf("404 仍须是 code=404 的 *httpStatusError（换候选依赖它）：%#v", err)
	}
	if msg := err.Error(); strings.Contains(msg, "风控") {
		t.Errorf("404 不该带 412 风控提示：%q", msg)
	}
	if !strings.Contains(err.Error(), "download failed: HTTP 404") {
		t.Errorf("非 412 消息应与改前一致：%q", err)
	}
}
