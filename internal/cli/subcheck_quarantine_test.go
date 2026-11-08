package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRunSubCheckAbortsWhenManifestQuarantined pins the user-facing contract
// after a corrupt manifest was isolated: "sub check" must fail with a non-zero
// exit and name the .corrupt-* copy, instead of printing "当前没有订阅" and
// returning success (which hides the data loss and lets the next write start a
// fresh empty store).
//
// 变异验证：删掉 substore.Load 里的隔离副本检测（退回 return nil, nil）→ 本用例
// 拿到 nil error 变红。
func TestRunSubCheckAbortsWhenManifestQuarantined(t *testing.T) {
	dir := t.TempDir()
	setSubStoreRoot(t, dir)
	iso := filepath.Join(dir, "BBDownSubscriptions.json.corrupt-20260101120000.000-1")
	if err := os.WriteFile(iso, []byte("{broken"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := dirSignature(t, dir)

	optSubCheckSince, optSubCheckConcurrency = "", 1
	t.Cleanup(func() { optSubCheckSince, optSubCheckConcurrency = "", 1 })

	err := runSubCheck(subCheckCmd, nil)
	if err == nil {
		t.Fatal("清单已被隔离时 sub check 必须失败，而不是打印「当前没有订阅」静默成功")
	}
	if !strings.Contains(err.Error(), "隔离副本") || !strings.Contains(err.Error(), iso) {
		t.Fatalf("报错必须告诉用户隔离副本在哪，实际: %v", err)
	}
	if code := exitCodeFor(err); code == 0 {
		t.Errorf("清单被隔离必须以非 0 退出，实际退出码 %d", code)
	}
	if after := dirSignature(t, dir); after != before {
		t.Fatalf("中止的运行不得写状态文件：\nbefore=%s\nafter=%s", before, after)
	}
}
