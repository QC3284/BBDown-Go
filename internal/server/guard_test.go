package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newLoopbackServer builds a server the way the default deployment does: bound
// to loopback with no token configured.
//
// taskFile 注入测试临时目录：NewAPIServer 的默认值 "bbdown-tasks.json" 是相对路径，会落到
// **进程工作目录**（测试进程的 cwd 是包目录），把完成任务清单写进仓库。任何会真正跑任务的
// serve 用例都必须像 tasks_test.go / shutdown_test.go 那样给出绝对路径。
//
// 变异验证：把 taskFile 改回默认 → TestBuildHandlerRejectsSimpleRequestCSRF 里
// 「不得写进进程工作目录」的断言变红。
func newLoopbackServer(t *testing.T) *APIServer {
	t.Helper()
	s := NewAPIServer("http://127.0.0.1:23333", 2, "", "")
	s.taskFile = filepath.Join(t.TempDir(), "bbdown-tasks.json")
	return s
}

func do(h http.Handler, method, target, host, origin, contentType, reqBody string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(reqBody))
	if host != "" {
		req.Host = host
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestBuildHandlerRejectsRebindingHost is the regression for the missing host
// check: with no token configured the handler had no request-level guard at
// all, so a page rebinding its own hostname to 127.0.0.1 could read
// /get-tasks (including absolute SavePaths) from the user's browser.
func TestBuildHandlerRejectsRebindingHost(t *testing.T) {
	h := newLoopbackServer(t).buildHandler()

	for _, host := range []string{"attacker.example", "attacker.example:23333", "192.168.1.10:23333"} {
		if got := do(h, http.MethodGet, "http://127.0.0.1:23333/health", host, "", "", "").Code; got != http.StatusForbidden {
			t.Errorf("Host %q: status %d, want 403", host, got)
		}
	}
	for _, host := range []string{"127.0.0.1:23333", "localhost:23333", "[::1]:23333"} {
		if got := do(h, http.MethodGet, "http://127.0.0.1:23333/health", host, "", "", "").Code; got != http.StatusOK {
			t.Errorf("Host %q: status %d, want 200", host, got)
		}
	}
}

// TestBuildHandlerRejectsSimpleRequestCSRF covers the cross-origin write path:
// a text/plain POST is a CORS "simple request" delivered without a preflight,
// so Origin and Content-Type must be checked on the write endpoints.
func TestBuildHandlerRejectsSimpleRequestCSRF(t *testing.T) {
	s := newLoopbackServer(t)
	h := s.buildHandler()
	const target = "http://127.0.0.1:23333/add-task"
	jsonBody := "{\"url\":\"x\"}"

	if got := do(h, http.MethodPost, target, "127.0.0.1:23333", "https://evil.example", "text/plain", jsonBody).Code; got != http.StatusForbidden {
		t.Errorf("cross-origin write: status %d, want 403", got)
	}
	if got := do(h, http.MethodPost, target, "127.0.0.1:23333", "", "text/plain", jsonBody).Code; got != http.StatusUnsupportedMediaType {
		t.Errorf("text/plain write: status %d, want 415", got)
	}
	// A same-origin JSON request must still reach the handler.
	if got := do(h, http.MethodPost, target, "127.0.0.1:23333", "http://127.0.0.1:23333", "application/json", jsonBody).Code; got == http.StatusForbidden || got == http.StatusUnsupportedMediaType {
		t.Errorf("same-origin JSON write was blocked: status %d", got)
	}
	// Read endpoints carry no body and must not be gated on Content-Type.
	if got := do(h, http.MethodGet, "http://127.0.0.1:23333/get-tasks", "127.0.0.1:23333", "", "", "").Code; got != http.StatusOK {
		t.Errorf("GET /get-tasks: status %d, want 200", got)
	}

	// The accepted task runs asynchronously and persists through processTask's defer.
	// Wait for the observable end state (the file), then pin where it landed:
	// ① 登记到注入的 taskFile（serve 的持久化契约）；② 不得落进进程工作目录。
	if !waitFor(5*time.Second, func() bool {
		_, err := os.Stat(s.taskFile)
		return err == nil
	}) {
		t.Fatalf("失败任务应当被持久化到注入的 taskFile %s", s.taskFile)
	}
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(wd, "bbdown-tasks.json")); !os.IsNotExist(err) {
		t.Errorf("serve 用例把完成任务清单写进了进程工作目录 %s：taskFile 必须注入 t.TempDir()", wd)
	}
}

// TestBuildHandlerSecurityHeaders pins the no-store/nosniff hardening: task
// listings expose local absolute paths and must not be cached.
func TestBuildHandlerSecurityHeaders(t *testing.T) {
	rec := do(newLoopbackServer(t).buildHandler(), http.MethodGet, "http://127.0.0.1:23333/health", "127.0.0.1:23333", "", "", "")
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
}

// TestOriginSchemeMustMatch pins RFC 6454 same-origin semantics for the CSRF
// gate: Host alone is not enough — the Origin scheme must match the request
// scheme, otherwise an https page with the same host:port passes as same-origin.
//
// 变异验证：删掉 originAllowed 里的 scheme 比较 → 第一条断言（https Origin 打到
// http 请求）变红。
func TestOriginSchemeMustMatch(t *testing.T) {
	s := newLoopbackServer(t)
	h := s.buildHandler()
	const host = "127.0.0.1:23333"
	// 故意发送非法 JSON：400 证明请求已经穿过 CSRF 闸门，且不产生异步任务副作用。
	const body = "not-json"

	httpTarget := "http://127.0.0.1:23333/add-task"
	if got := do(h, http.MethodPost, httpTarget, host, "https://"+host, "application/json", body).Code; got != http.StatusForbidden {
		t.Errorf("https Origin on an http request: status %d, want 403", got)
	}
	if got := do(h, http.MethodPost, httpTarget, host, "http://"+host, "application/json", body).Code; got != http.StatusBadRequest {
		t.Errorf("same scheme+host+port origin must pass the guard (400 for the malformed body), got %d", got)
	}

	// TLS 请求与 https Origin 才是同源（httptest.NewRequest 对 https target 会置 fake TLS）。
	req := httptest.NewRequest(http.MethodPost, "https://127.0.0.1:23333/add-task", strings.NewReader(body))
	req.Host = host
	req.Header.Set("Origin", "https://"+host)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("https Origin on a TLS request must pass the guard (400 expected), got %d", rec.Code)
	}
}
