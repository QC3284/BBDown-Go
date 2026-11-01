package download

import (
	"os"
	"path/filepath"
	"testing"
)

// TestCleanupProductResiduesSweepsOnlyStaleMuxTemps: 跳过分支只回收与本产物同名、
// 且序号段是纯数字的 <base>.muxing-<n><ext>；其它形态一律不动（避免误删兄弟文件）。
func TestCleanupProductResiduesSweepsOnlyStaleMuxTemps(t *testing.T) {
	dir := t.TempDir()
	mk := func(name string) string {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}

	product := mk("out.mp4")
	stale := mk("out.muxing-123456789.mp4")  // 上次硬杀留下的混流临时件 → 应删
	nonDigit := mk("out.muxing-abc.mp4")     // 不是本仓的命名形态 → 保留
	otherProduct := mk("other.muxing-1.mp4") // 别的产物的临时件 → 保留
	singleTmp := mk("out.mp4.tmp")           // 单线程残留，归 cleanupResidue 管 → 保留

	CleanupProductResidues(product)

	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("应删除残留混流临时件 %s（err=%v）", stale, err)
	}
	for _, keep := range []string{product, nonDigit, otherProduct, singleTmp} {
		if _, err := os.Stat(keep); err != nil {
			t.Errorf("不应删除 %s: %v", keep, err)
		}
	}
}
