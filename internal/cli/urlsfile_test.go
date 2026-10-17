package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// F2 批量输入：列表解析与顺序执行的语义。
//
// 变异验证：runTargets 改成「遇错即返回」→ 第二段变红；readURLList 不跳注释 → 第一段变红。
func TestReadURLListSkipsCommentsAndBlanks(t *testing.T) {
	in := "# 注释\r\n\r\n  https://www.bilibili.com/video/BV1xx411c7mD  \nBV1ZH4y167mH\n# 又一条注释\nav270935199\n"
	got, err := readURLList(strings.NewReader(in))
	if err != nil {
		t.Fatalf("readURLList: %v", err)
	}
	want := []string{"https://www.bilibili.com/video/BV1xx411c7mD", "BV1ZH4y167mH", "av270935199"}
	if len(got) != len(want) {
		t.Fatalf("目标 = %v，期望 %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("第 %d 个目标 = %q，期望 %q", i+1, got[i], want[i])
		}
	}
}

func TestCollectTargetsMergesArgsAndFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "urls.txt")
	if err := os.WriteFile(path, []byte("# 注释\nBV1ZH4y167mH\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := collectTargets([]string{"av1", "av2"}, path, strings.NewReader(""))
	if err != nil {
		t.Fatalf("collectTargets: %v", err)
	}
	if len(got) != 3 || got[0] != "av1" || got[1] != "av2" || got[2] != "BV1ZH4y167mH" {
		t.Errorf("目标 = %v，期望位置参数在前、文件在后", got)
	}

	// "-" 表示读 stdin。
	got, err = collectTargets(nil, "-", strings.NewReader("av9\n"))
	if err != nil || len(got) != 1 || got[0] != "av9" {
		t.Errorf("stdin 目标 = %v（err=%v），期望 [av9]", got, err)
	}

	// 文件不存在要报错（不能静默变成「没有目标」）。
	if _, err := collectTargets(nil, filepath.Join(dir, "missing.txt"), strings.NewReader("")); err == nil {
		t.Error("文件不存在时必须报错")
	}

	// 两边都空 → 空切片，由调用方提示。
	if got, err := collectTargets(nil, "", strings.NewReader("")); err != nil || len(got) != 0 {
		t.Errorf("空输入 = %v（err=%v），期望空", got, err)
	}
}

func TestRunTargetsContinuesAfterFailure(t *testing.T) {
	var seen []string
	run := func(_ context.Context, target string) error {
		seen = append(seen, target)
		if target == "bad" {
			return errors.New("boom")
		}
		return nil
	}

	oldDelay := batchDelayPerVideo
	batchDelayPerVideo = 0
	t.Cleanup(func() { batchDelayPerVideo = oldDelay })
	failures := runTargets(context.Background(), []string{"a", "bad", "c"}, run)
	if failures != 1 {
		t.Errorf("失败数 = %d，期望 1", failures)
	}
	if strings.Join(seen, ",") != "a,bad,c" {
		t.Errorf("执行顺序 = %v，期望三个目标都跑到（单个失败不中断其余）", seen)
	}

	// ctx 取消：立即停下。
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	seen = nil
	if failures := runTargets(ctx, []string{"a", "b"}, run); failures != 0 || len(seen) != 0 {
		t.Errorf("取消后不应继续执行：failures=%d seen=%v", failures, seen)
	}
}
