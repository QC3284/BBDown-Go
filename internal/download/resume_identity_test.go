package download

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// O5：断点续传命中率。上游 v1.6.20 用 StableResourceIdentity（剥离会刷新的签名参数）比较清单，
// 本仓此前比完整 URL——B 站每次解析都会换 deadline/sign/trid/upsig，于是跨进程续传**永不命中**。

func TestStableResourceIdentityStripsSignatureParams(t *testing.T) {
	a := "https://upos.example.com/video.mp4?mid=1&deadline=1700000000&sign=abc&wts=1700000000&qn=80"
	b := "https://upos.example.com/video.mp4?mid=1&deadline=1700000300&sign=def&wts=1700000300&qn=80"
	if stableResourceIdentity(a) != stableResourceIdentity(b) {
		t.Errorf("同一资源刷新签名后身份必须相同：%q vs %q", stableResourceIdentity(a), stableResourceIdentity(b))
	}
	stable := stableResourceIdentity(a)
	for _, want := range []string{"mid=1", "qn=80"} {
		if !strings.Contains(stable, want) {
			t.Errorf("稳定参数 %s 必须保留：%q", want, stable)
		}
	}
	for _, bad := range []string{"sign=", "deadline=", "wts="} {
		if strings.Contains(stable, bad) {
			t.Errorf("签名参数 %s 必须剥离：%q", bad, stable)
		}
	}
	if stableResourceIdentity("https://upos.example.com/other.mp4?mid=1&deadline=1&sign=x") == stable {
		t.Error("不同路径必须是不同身份")
	}
}

// TestManifestMatchesProbe pins the resume gate. task-9 (user-approved D1, 2026-11-08) adopts
// upstream 79e3a91's strict rule: identity+size are mandatory, and the validator sets of the
// manifest and the fresh probe must agree — both sides present with at least one comparable
// type, or both sides empty. A one-sided validator set (the old permissive path) or disjoint
// validator types must reject the stale .tmp instead of resuming from it.
//
// 变异验证：撤掉 presence 检查或类型不相交检查 → 对应用例变红（误续传）。
func TestManifestMatchesProbe(t *testing.T) {
	const ident = "https://cdn.example.com/1080p.mp4?qn=80"
	mk := func(identity, url string, size int64, etag, lm string) resumeManifest {
		return resumeManifest{Identity: identity, URL: url, Size: size, ETag: etag, LastModified: lm}
	}
	cases := []struct {
		name               string
		m                  resumeManifest
		identity           string
		size               int64
		etag, lastModified string
		want               bool
	}{
		{"同身份同长（双方都无校验器）", mk(ident, "", 12345, "", ""), ident, 12345, "", "", true},
		{"旧清单只存完整 URL，按稳定身份回退匹配", mk("", "https://cdn.example.com/1080p.mp4?qn=80&deadline=1", 12345, "", ""), ident, 12345, "", "", true},
		{"不同资源（路径不同）", mk("https://cdn.example.com/720p.mp4", "", 12345, "", ""), ident, 12345, "", "", false},
		{"总长不一致", mk(ident, "", 999, "", ""), ident, 12345, "", "", false},
		{"ETag 双方都有且不同", mk(ident, "", 12345, "W/old", ""), ident, 12345, "W/new", "", false},
		// 旧实现（≤3.1.2）对单边缺失退化为身份+长度；task-9 起视为「无法确认版本」拒绝。
		{"清单无校验器、探测有 ETag → 拒绝续传", mk(ident, "", 12345, "", ""), ident, 12345, "W/new", "", false},
		{"清单有 ETag、探测无校验器 → 拒绝续传", mk(ident, "", 12345, "\"v1\"", ""), ident, 12345, "", "", false},
		{"Last-Modified 双方都有且不同", mk(ident, "", 12345, "", "Mon, 01 Jan 2024 00:00:00 GMT"), ident, 12345, "", "Tue, 02 Jan 2024 00:00:00 GMT", false},
		{"Last-Modified 双方都有且相同 → 续传", mk(ident, "", 12345, "", "Mon, 01 Jan 2024 00:00:00 GMT"), ident, 12345, "", "Mon, 01 Jan 2024 00:00:00 GMT", true},
		// 上游 79e3a91 的回归场景：清单同时有 ETag 与 Last-Modified，探测只剩
		// Last-Modified 且相同。上游旧实现「每边挑一个校验器」会拿清单 ETag 比探测
		// Last-Modified 而误删可续传的 .tmp；本仓按同类型比较，这一场景仍应续传。
		{"清单有 ETag+Last-Modified，探测只有 Last-Modified 且相同（上游 79e3a91 场景）",
			mk(ident, "", 12345, "\"v1\"", "Mon, 01 Jan 2024 00:00:00 GMT"), ident, 12345, "", "Mon, 01 Jan 2024 00:00:00 GMT", true},
		{"清单有 ETag+Last-Modified，探测只有相同 ETag → 续传",
			mk(ident, "", 12345, "\"v1\"", "Mon, 01 Jan 2024 00:00:00 GMT"), ident, 12345, "\"v1\"", "", true},
		{"双方都有 ETag 与 Last-Modified 且都相同 → 续传",
			mk(ident, "", 12345, "\"v1\"", "Mon, 01 Jan 2024 00:00:00 GMT"), ident, 12345, "\"v1\"", "Mon, 01 Jan 2024 00:00:00 GMT", true},
		{"双方都有 ETag 与 Last-Modified，Last-Modified 不同 → 拒绝续传",
			mk(ident, "", 12345, "\"v1\"", "Mon, 01 Jan 2024 00:00:00 GMT"), ident, 12345, "\"v1\"", "Tue, 02 Jan 2024 00:00:00 GMT", false},
		// 校验器类型不相交（清单 ETag、探测 Last-Modified）：上游 79e3a91 起拒绝续传。
		{"校验器类型不相交（清单 ETag、探测 Last-Modified）→ 拒绝续传",
			mk(ident, "", 12345, "\"v1\"", ""), ident, 12345, "", "Mon, 01 Jan 2024 00:00:00 GMT", false},
		{"校验器类型不相交（清单 Last-Modified、探测 ETag）→ 拒绝续传",
			mk(ident, "", 12345, "", "Mon, 01 Jan 2024 00:00:00 GMT"), ident, 12345, "\"v1\"", "", false},
	}
	for _, c := range cases {
		if got := manifestMatchesProbe(c.m, c.identity, c.size, c.etag, c.lastModified); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

// TestResumeHitsAcrossRefreshedSignature 是 O5 的数字来源：第一次下载被中断（服务端发一半就断连），
// 第二次带**刷新后的签名**重跑，必须只补缺口（Range: bytes=N-），而不是从头重下。
//
// 变异验证：身份比较改回完整 URL（manifestMatchesProbe 里比 m.URL），第二段会变成整份重下，本用例变红。
func TestResumeHitsAcrossRefreshedSignature(t *testing.T) {
	const total = 2 << 20
	body := make([]byte, total)
	for i := range body {
		body[i] = byte(i % 251)
	}
	const cut = 512 << 10

	var mu sync.Mutex
	var ranges []string
	var servedSecondRun int64
	firstRun := true

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("ETag", "v1") // 用例只比较字符串，不入 RFC 的引号要求
		w.Header().Set("Accept-Ranges", "bytes")
		if r.Method == http.MethodHead {
			w.Header().Set("Content-Length", fmt.Sprint(total))
			w.WriteHeader(http.StatusOK)
			return
		}
		rng := r.Header.Get("Range")
		ranges = append(ranges, rng)
		if firstRun {
			// 只发一半就断连：模拟中断（客户端拿到 unexpected EOF），.tmp 与清单留在磁盘上。
			w.Header().Set("Content-Length", fmt.Sprint(cut))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(body[:cut])
			firstRun = false
			panic(http.ErrAbortHandler)
		}
		start := int64(0)
		if strings.HasPrefix(rng, "bytes=") {
			_, _ = fmt.Sscanf(strings.TrimSuffix(strings.TrimPrefix(rng, "bytes="), "-"), "%d", &start)
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, total-1, total))
		w.Header().Set("Content-Length", fmt.Sprint(total-start))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(body[start:])
		servedSecondRun += total - start
	}))
	defer srv.Close()

	dir := t.TempDir()
	dest := filepath.Join(dir, "out.bin")
	cfg := DownloadConfig{Client: newTestClient(), RetryCount: 1, RetryDelayMs: 1}

	// 第一段：旧签名，中途断连 → 失败但留下 .tmp 与清单。
	err := DownloadFile(context.Background(), srv.URL+"/v.mp4?deadline=1&sign=old&qn=80", dest, cfg)
	if err == nil {
		t.Fatal("第一段应当失败（服务端中途断连）")
	}
	partial, statErr := os.Stat(dest + ".tmp")
	if statErr != nil || partial.Size() == 0 {
		t.Fatalf("中断后应留下部分 .tmp：%v", statErr)
	}

	// 第二段：签名刷新（同一资源）→ 必须从断点续传。
	if err := DownloadFile(context.Background(), srv.URL+"/v.mp4?deadline=999&sign=new&qn=80", dest, cfg); err != nil {
		t.Fatalf("第二段应当续传成功：%v", err)
	}
	mu.Lock()
	secondRanges := append([]string(nil), ranges...)
	secondServed := servedSecondRun
	mu.Unlock()

	got, readErr := os.ReadFile(dest)
	if readErr != nil || !bytes.Equal(got, body) {
		t.Fatalf("续传产物不完整或内容不符：err=%v 大小=%d", readErr, len(got))
	}
	if len(secondRanges) < 2 || !strings.HasPrefix(secondRanges[1], "bytes=") {
		t.Errorf("第二段应当发 Range 续传，实际 Range 序列 = %v（partial=%d）", secondRanges, partial.Size())
	}
	if want := int64(total) - partial.Size(); secondServed != want {
		t.Errorf("第二段实际下载 %d 字节，期望只补缺口 %d（总量 %d，已存 %d）", secondServed, want, total, partial.Size())
	}
}

// TestResumeRejectedWhenValidatorSetDiffers 验证 task-9（D1）的收紧：清单里有 ETag、
// 第二次探测不再返回任何校验器（清单与探测的校验器集合不一致）时，旧 .tmp 必须视为
// 不可信、整份重下——不能拿「身份 + 长度」继续补缺口，因为 CDN 可能换了文件却保持
// 同样的长度。旧实现（≤3.1.2 的宽松规则）会从断点续传，本用例变红。
//
// 变异验证：撤掉 manifestMatchesProbe 的 presence 检查 → 本用例变红（第二段带 Range）。
func TestResumeRejectedWhenValidatorSetDiffers(t *testing.T) {
	const total = 2 << 20
	const cut = 512 << 10
	body := make([]byte, total)
	for i := range body {
		body[i] = byte(i % 251)
	}

	var mu sync.Mutex
	var headCount int
	var getRanges []string
	var servedSecondRun int64
	firstGET := true

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Accept-Ranges", "bytes")
		if r.Method == http.MethodHead {
			headCount++
			if headCount == 1 {
				// 第一次探测有 ETag（写进续传清单），第二次不再返回任何校验器。
				w.Header().Set("ETag", "v1")
			}
			w.Header().Set("Content-Length", fmt.Sprint(total))
			w.WriteHeader(http.StatusOK)
			return
		}
		rng := r.Header.Get("Range")
		getRanges = append(getRanges, rng)
		if firstGET {
			w.Header().Set("Content-Length", fmt.Sprint(cut))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(body[:cut])
			firstGET = false
			panic(http.ErrAbortHandler)
		}
		start := int64(0)
		if strings.HasPrefix(rng, "bytes=") {
			_, _ = fmt.Sscanf(strings.TrimSuffix(strings.TrimPrefix(rng, "bytes="), "-"), "%d", &start)
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, total-1, total))
			w.Header().Set("Content-Length", fmt.Sprint(int64(total)-start))
			w.WriteHeader(http.StatusPartialContent)
		} else {
			w.Header().Set("Content-Length", fmt.Sprint(total))
			w.WriteHeader(http.StatusOK)
		}
		_, _ = w.Write(body[start:])
		servedSecondRun += int64(total) - start
	}))
	defer srv.Close()

	dir := t.TempDir()
	dest := filepath.Join(dir, "out.bin")
	cfg := DownloadConfig{Client: newTestClient(), RetryCount: 1, RetryDelayMs: 1}

	// 第一段：旧签名，中途断连 → 失败但留下 .tmp 与带 ETag 的清单。
	if err := DownloadFile(context.Background(), srv.URL+"/v.mp4?deadline=1&sign=old&qn=80", dest, cfg); err == nil {
		t.Fatal("第一段应当失败（服务端中途断连）")
	}
	if info, err := os.Stat(dest + ".tmp"); err != nil || info.Size() == 0 {
		t.Fatalf("中断后应留下部分 .tmp：%v", err)
	}

	// 第二段：校验器集合不一致 → 不可信，必须整份重下。
	if err := DownloadFile(context.Background(), srv.URL+"/v.mp4?deadline=999&sign=new&qn=80", dest, cfg); err != nil {
		t.Fatalf("第二段应当完整重下成功：%v", err)
	}
	mu.Lock()
	ranges := append([]string(nil), getRanges...)
	served := servedSecondRun
	mu.Unlock()

	if got, err := os.ReadFile(dest); err != nil || !bytes.Equal(got, body) {
		t.Fatalf("产物不完整或内容不符：err=%v 大小=%d", err, len(got))
	}
	if len(ranges) < 2 || ranges[1] != "" {
		t.Errorf("校验器集合不一致时必须整份重下（第二段不应带 Range），实际 Range 序列 = %v", ranges)
	}
	if served != total {
		t.Errorf("第二段实际下载 %d 字节，期望整份 %d（旧行为会只补缺口）", served, total)
	}
}

// TestSelectIfRange pins the RFC 7233 If-Range validator selection (upstream
// 1.7.9 BBDownDownloadUtil.SelectIfRange): a strong ETag is preferred, a weak
// ETag ("W/...") is forbidden in If-Range and falls back to Last-Modified,
// and with neither a validator header is sent at all.
//
// 变异验证：去掉 selectIfRange 里的弱 ETag 判断（直接返回 ETag）→ 弱 ETag 两行变红。
func TestSelectIfRange(t *testing.T) {
	const lm = "Mon, 01 Jan 2024 00:00:00 GMT"
	cases := []struct {
		name, etag, lastModified, want string
	}{
		{"强 ETag 优先", `"v1"`, lm, `"v1"`},
		{"无 ETag 时用 Last-Modified", "", lm, lm},
		{"弱 ETag 退回 Last-Modified", `W/"v1"`, lm, lm},
		{"小写 w/ 同样视为弱", `w/"v1"`, lm, lm},
		{"弱 ETag 且无 Last-Modified 时不发 If-Range", `W/"v1"`, "", ""},
		{"两者都缺失", "", "", ""},
		{"空白值视为缺失", "  ", "  ", ""},
	}
	for _, c := range cases {
		if got := selectIfRange(c.etag, c.lastModified); got != c.want {
			t.Errorf("%s: selectIfRange(%q, %q) = %q, want %q", c.name, c.etag, c.lastModified, got, c.want)
		}
	}
}
