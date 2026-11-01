package util

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// Windows 不解释 POSIX 权限位：断言 0600 的用例显式跳过（跨平台准则，不靠「本机跑得过」）。
func skipUnlessUnixPerms(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skipf("Windows 不解释 POSIX 权限位，无法断言 0600")
	}
}

// TestSecureFileModeOwnerOnly 钉住创建权限：Unix 上只允许属主读写（组/其它位必须为 0）。
func TestSecureFileModeOwnerOnly(t *testing.T) {
	skipUnlessUnixPerms(t)
	if got := SecureFileMode(); got != 0o600 {
		t.Errorf("SecureFileMode() = %#o, want 0600", got)
	}
	if got := SecureFileMode() & 0o077; got != 0 {
		t.Errorf("SecureFileMode() 的组/其它位 = %#o, want 0（属主私有）", got)
	}
}

// TestLogFileIsCreatedOwnerOnly: 新日志文件（serve 的 bbdown-api.log 同款路径）
// 必须在创建时就是 0600，而不是等到有人手动 chmod。
func TestLogFileIsCreatedOwnerOnly(t *testing.T) {
	skipUnlessUnixPerms(t)
	path := filepath.Join(t.TempDir(), "bbdown-api.log")
	l := NewLogger(nil)
	l.SetLogFile(path)
	l.appendToFile("hello")

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("日志文件应当被创建: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("新建日志文件权限 = %#o, want 0600", perm)
	}
}

// TestLogFileTightensExistingPermissions: 升级前留下的 0644 日志文件，
// 启动（SetLogFile）与后续写入都必须把它收紧到 0600（upstream #113 的启动收紧）。
func TestLogFileTightensExistingPermissions(t *testing.T) {
	skipUnlessUnixPerms(t)
	path := filepath.Join(t.TempDir(), "bbdown-api.log")
	if err := os.WriteFile(path, []byte("old line\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// umask 可能把 0644 降成别的值，显式 chmod 成 0644 才真正制造「历史宽权限」现场。
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}

	l := NewLogger(nil)
	l.SetLogFile(path)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("SetLogFile 后权限 = %#o, want 0600（已存在的日志文件也要收紧）", perm)
	}

	l.appendToFile("new line")
	info, err = os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("写入后权限 = %#o, want 0600", perm)
	}
}
