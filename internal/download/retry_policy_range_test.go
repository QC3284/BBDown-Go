package download

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
)

// 分片循环（downloadRange）与单线程共用同一条分类策略，但调用点各自独立——
// 这里钉住分片路径也真的按策略执行：确定性 4xx 立刻放弃、412 在 UA 无法轮换时立刻放弃。
//
// HEAD 必须成功（否则退化成单线程路径），所以两个假服务器都声明长度 + Accept-Ranges。

// rangeFailServer 起一个「分片恒失败」的假服务器，返回 (总 GET 次数, 单个分片的最大 GET 次数)。
//
// t89 起分片失败会取消兄弟分片（t82 评估 ②），所以**总次数**不再是「分片数」这个定值——
// 被取消的兄弟可能一次 GET 都没发出去。判「不重试」要看**每个分片**的请求数（最大值为 1），
// 而不是总数。
func rangeFailServer(t *testing.T, body []byte, status int) (*httptest.Server, *atomic.Int64, func() int) {
	t.Helper()
	var gets atomic.Int64
	var mu sync.Mutex
	perRange := make(map[string]int)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.Header().Set("Accept-Ranges", "bytes")
			w.Header().Set("Content-Length", strconv.Itoa(len(body)))
			w.WriteHeader(http.StatusOK)
			return
		}
		gets.Add(1)
		mu.Lock()
		perRange[r.Header.Get("Range")]++
		mu.Unlock()
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	maxPerRange := func() int {
		mu.Lock()
		defer mu.Unlock()
		max := 0
		for _, n := range perRange {
			if n > max {
				max = n
			}
		}
		return max
	}
	return srv, &gets, maxPerRange
}

func TestMultiThreadHonorsRetryClassification(t *testing.T) {
	body := make([]byte, 3<<20) // 3MB / 1MB 分片 = 3 个分片

	t.Run("403 每个分片只试一次（确定性失败不重试）", func(t *testing.T) {
		srv, gets, maxPerRange := rangeFailServer(t, body, http.StatusForbidden)
		cfg := DownloadConfig{Client: newTestClient(), MultiThread: true, SegmentSizeMB: 1, RetryCount: 3, RetryDelayMs: 1}
		err := DownloadFile(context.Background(), srv.URL+"/v.m4s", filepath.Join(t.TempDir(), "out.bin"), cfg)
		if err == nil {
			t.Fatal("分片恒 403 必须报错")
		}
		// 旧行为每个分片重试满 3 次 = 9 次 GET。t89 起失败会取消兄弟分片，总数可能是 1..3，
		// 所以判据改成「每个分片最多一次」。
		// 变异验证：去掉 downloadRange 里的「if !plan.Retry 就 return」，maxPerRange 变红（>1）。
		if max := maxPerRange(); max > 1 {
			t.Errorf("同一分片最多发一次 GET，实际最多 %d 次（确定性 4xx 不得重试）", max)
		}
		if got := gets.Load(); got < 1 || got > 3 {
			t.Errorf("总 GET 次数应当在 1..3 之间（可能因取消兄弟分片而少于 3），实际 %d", got)
		}
	})

	t.Run("412 且显式 UA 无法轮换：每个分片也只试一次", func(t *testing.T) {
		srv, gets, maxPerRange := rangeFailServer(t, body, http.StatusPreconditionFailed)
		client := newTestClient()
		client.SetUserAgent("explicit-ua")
		cfg := DownloadConfig{Client: client, MultiThread: true, SegmentSizeMB: 1, RetryCount: 3, RetryDelayMs: 1}
		err := DownloadFile(context.Background(), srv.URL+"/v.m4s", filepath.Join(t.TempDir(), "out.bin"), cfg)
		if err == nil {
			t.Fatal("分片恒 412 必须报错")
		}
		// 变异验证：去掉 downloadRange 里的 412 轮换门，maxPerRange 变红（>1，即同一分片被重试）。
		if max := maxPerRange(); max > 1 {
			t.Errorf("同一分片最多发一次 GET，实际最多 %d 次（UA 无法轮换时不得重试）", max)
		}
		if got := gets.Load(); got < 1 || got > 3 {
			t.Errorf("总 GET 次数应当在 1..3 之间，实际 %d", got)
		}
	})
}
