package server

import (
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QC3284/BBDown-Go/internal/util"
)

// Windows 不解释 POSIX 权限位：0600 断言显式跳过（跨平台准则）。
func skipUnlessUnixPermsServer(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skipf("Windows 不解释 POSIX 权限位，无法断言 0600")
	}
}

// TestWebhookLogTargetKeepsOnlySchemeHostPort 钉住回调地址的日志形态：
// 只允许 scheme://host[:port]，path/query/userinfo 一律丢弃——token 常在 query 或
// userinfo 里，原样写日志就是凭据泄漏（upstream #114）。
func TestWebhookLogTargetKeepsOnlySchemeHostPort(t *testing.T) {
	cases := []struct{ raw, want string }{
		{"https://hooks.example.com/cb?token=SUPERSECRET", "https://hooks.example.com"},
		{"https://hooks.example.com:8443/cb?token=SUPERSECRET&x=1", "https://hooks.example.com:8443"},
		{"http://user:pass@hooks.example.com:8080/cb?token=SUPERSECRET", "http://hooks.example.com:8080"},
		{"HTTPS://Hooks.Example.com:443/x", "https://hooks.example.com:443"},
		{"", "(未配置)"},
		{"http://[::1/cb?token=SUPERSECRET", "(无效回调地址)"},
		{"ftp://hooks.example.com/cb?token=SUPERSECRET", "(无效回调地址)"},
	}
	for _, c := range cases {
		got := webhookLogTarget(c.raw)
		if got != c.want {
			t.Errorf("webhookLogTarget(%q) = %q, want %q", c.raw, got, c.want)
		}
		for _, leak := range []string{"SUPERSECRET", "user:pass"} {
			if strings.Contains(got, leak) {
				t.Errorf("webhookLogTarget(%q) 泄漏了 %q：%q", c.raw, leak, got)
			}
		}
	}
}

// TestCallbackFailureLogDoesNotEchoURL: 回调失败日志只保留错误类型。
// *url.Error 的 Error() 会拼上完整 URL（含 query token），直接 %v 就是泄漏。
func TestCallbackFailureLogDoesNotEchoURL(t *testing.T) {
	raw := "https://hooks.example.com/cb?token=SUPERSECRET"
	err := &url.Error{Op: "Post", URL: raw, Err: errors.New("dial tcp: connection refused")}

	msg := callbackFailureLog(err)
	if !strings.Contains(msg, "url.Error") {
		t.Errorf("失败日志应包含错误类型，实际 %q", msg)
	}
	for _, leak := range []string{"SUPERSECRET", "hooks.example.com", "token="} {
		if strings.Contains(msg, leak) {
			t.Errorf("失败日志 %q 泄漏了 %q", msg, leak)
		}
	}
}

// TestCallbackTransportDoesNotUseProxy: 回调必须直连校验后的 IP，不能继承系统代理
// （否则请求会经过一个未校验的代理，绕过 SSRF 防护；upstream #114 同款）。
func TestCallbackTransportDoesNotUseProxy(t *testing.T) {
	tr := newCallbackTransport("127.0.0.1:80")
	if tr.Proxy != nil {
		t.Fatal("回调 Transport 的 Proxy 必须为 nil：不得继承系统代理")
	}
	if tr.DialContext == nil {
		t.Fatal("回调 Transport 必须拨校验后的 IP（DialContext 为 nil 会退回直拨原始域名）")
	}
}

// TestPersistTaskFileOwnerOnly: serve 的任务记录含任务参数，创建时就必须是 0600
// （upstream #113；Unix only）。
func TestPersistTaskFileOwnerOnly(t *testing.T) {
	skipUnlessUnixPermsServer(t)
	dir := t.TempDir()
	s := NewAPIServer("http://127.0.0.1:23333", 1, "", "")
	s.taskFile = filepath.Join(dir, "bbdown-tasks.json")

	task := &DownloadTask{JobID: "j1", Status: StatusSucceeded, TaskCreateTime: time.Now().Unix()}
	task.mu = &sync.Mutex{}
	s.mu.Lock()
	s.finishedTasks = append(s.finishedTasks, task)
	s.mu.Unlock()

	s.persistFinishedTasks()
	info, err := os.Stat(s.taskFile)
	if err != nil {
		t.Fatalf("任务记录应当被持久化: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("任务记录权限 = %#o, want 0600", perm)
	}
}

// TestStartupTightensExistingTaskFile: 升级前留下的 0644 任务记录，
// 启动加载时必须被收紧到 0600（upstream #113 的启动收紧；Unix only）。
func TestStartupTightensExistingTaskFile(t *testing.T) {
	skipUnlessUnixPermsServer(t)
	path := filepath.Join(t.TempDir(), "bbdown-tasks.json")
	if err := os.WriteFile(path, []byte("[]"), 0o644); err != nil {
		t.Fatal(err)
	}
	// umask 可能压低权限，显式 chmod 才真正制造「历史宽权限」现场。
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}

	s := NewAPIServer("http://127.0.0.1:23333", 1, "", "")
	s.taskFile = path
	s.loadFinishedTasks()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("启动加载后任务记录权限 = %#o, want 0600", perm)
	}
}

// TestSendCallbackLogsRedactedTarget 端到端钉住 warn 日志里不出现完整回调 URL：
// 每个分支都必须只写 scheme://host[:port]（或「无效回调地址」），query 里的 token 不得出现。
// 这些分支都在发送前被拒，不会碰网络。
func TestSendCallbackLogsRedactedTarget(t *testing.T) {
	cases := []struct{ webhook, wantTarget, wantMarker string }{
		{"http://localhost:9/cb?token=SUPERSECRET", "http://localhost:9", "内网地址"},
		{"http://127.0.0.1:9/cb?token=SUPERSECRET", "http://127.0.0.1:9", "敏感字面 IP"},
		{"http://[::1/cb?token=SUPERSECRET", "(无效回调地址)", "不合法"},
	}
	for _, c := range cases {
		logPath := filepath.Join(t.TempDir(), "serve.log")
		util.SetLogFile(logPath)
		t.Cleanup(func() { util.SetLogFile("") })

		s := NewAPIServer("http://127.0.0.1:23333", 1, "", c.webhook)
		task := &DownloadTask{JobID: "j1"}
		task.mu = &sync.Mutex{}
		s.sendCallback(task)

		data, err := os.ReadFile(logPath)
		if err != nil {
			t.Fatalf("webhook %q: 回调拒绝必须写日志文件: %v", c.webhook, err)
		}
		got := string(data)
		if !strings.Contains(got, c.wantMarker) {
			t.Errorf("webhook %q: 日志应含 %q，实际 %q", c.webhook, c.wantMarker, got)
		}
		if !strings.Contains(got, c.wantTarget) {
			t.Errorf("webhook %q: 日志应含脱敏目标 %q，实际 %q", c.webhook, c.wantTarget, got)
		}
		if strings.Contains(got, "SUPERSECRET") {
			t.Errorf("webhook %q: 日志泄漏了 query token：%q", c.webhook, got)
		}
	}
}
