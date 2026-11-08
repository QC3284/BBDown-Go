package substore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withTempRoot switches the store root to a temp dir for the test.
func withTempRoot(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	old := StoreRoot
	StoreRoot = dir
	t.Cleanup(func() { StoreRoot = old })
	return dir
}

func TestSubStoreAddListRemove(t *testing.T) {
	withTempRoot(t)

	if err := Add("mid:123", "某人", ""); err != nil {
		t.Fatal(err)
	}
	if err := Add("ep:456", "", ""); err != nil {
		t.Fatal(err)
	}

	subs, err := ListSorted()
	if err != nil {
		t.Fatal(err)
	}
	if len(subs) != 2 {
		t.Fatalf("want 2 subs, got %d", len(subs))
	}
	// Unnamed subscription falls back to the target string.
	if subs[1].Target != "ep:456" || subs[1].Name != "ep:456" {
		t.Fatalf("fallback name wrong: %+v", subs[1])
	}

	if err := Remove("mid:123"); err != nil {
		t.Fatal(err)
	}
	subs, err = ListSorted()
	if err != nil {
		t.Fatal(err)
	}
	if len(subs) != 1 || subs[0].Target != "ep:456" {
		t.Fatalf("remove failed: %+v", subs)
	}
}

func TestSubStoreHistory(t *testing.T) {
	withTempRoot(t)

	history, err := LoadHistory("mid:123")
	if err != nil || history != nil {
		t.Fatalf("empty history = %v, %v", history, err)
	}

	if err := RecordDownloaded("mid:123", "170001"); err != nil {
		t.Fatal(err)
	}
	if err := RecordDownloaded("mid:123", "170001"); err != nil {
		t.Fatal(err)
	}
	history, err = LoadHistory("mid:123")
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || history[0] != "170001" {
		t.Fatalf("history = %v", history)
	}
}

func TestSubStoreCorruptDetection(t *testing.T) {
	dir := withTempRoot(t)
	if err := os.WriteFile(filepath.Join(dir, "BBDownSubscriptions.json"), []byte("{corrupt"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load()
	if err == nil {
		t.Fatal("corrupt store should error")
	}
	// The corrupt file must be quarantined, not overwritten.
	matches, _ := filepath.Glob(filepath.Join(dir, "BBDownSubscriptions.json.corrupt-*"))
	if len(matches) != 1 {
		t.Fatalf("corrupt file not quarantined: %v", matches)
	}
}

// TestSubStoreAbortsWhenDataFileMissingButIsolatedCopyExists pins the upstream
// 1.7.9 ThrowIfIsolatedFileExists rule: once a corrupt store was quarantined,
// the next run must abort with a readable error naming the .corrupt-* copy
// instead of silently treating the store as empty (which hides the data loss
// and lets the next write start over with a fresh list/history).
//
// 变异验证：删掉 Load/LoadHistory 里的 isolatedCopyOf 分支（退回 return nil, nil）
// → 本用例的「必须报错」断言变红。
func TestSubStoreAbortsWhenDataFileMissingButIsolatedCopyExists(t *testing.T) {
	dir := withTempRoot(t)
	store := filepath.Join(dir, "BBDownSubscriptions.json")
	iso := store + ".corrupt-20260101120000.000-1"
	if err := os.WriteFile(iso, []byte("{broken"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load()
	if err == nil {
		t.Fatal("订阅清单缺失但存在隔离副本时必须报错，而不是当作空清单")
	}
	if !strings.Contains(err.Error(), "隔离副本") || !strings.Contains(err.Error(), iso) {
		t.Fatalf("报错必须点名隔离副本，实际: %v", err)
	}

	history := filepath.Join(dir, "BBDownSubscriptions.history.json")
	historyISO := history + ".corrupt-20260101120000.000-2"
	if err := os.WriteFile(historyISO, []byte("{broken"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadHistory("mid:1"); err == nil {
		t.Fatal("订阅历史缺失但存在隔离副本时必须报错")
	} else if !strings.Contains(err.Error(), "隔离副本") || !strings.Contains(err.Error(), historyISO) {
		t.Fatalf("历史报错必须点名隔离副本，实际: %v", err)
	}

	// 没有隔离副本时保持原语义：数据文件缺失 = 空清单（首次使用）。
	empty := t.TempDir()
	old := StoreRoot
	StoreRoot = empty
	t.Cleanup(func() { StoreRoot = old })
	if subs, err := Load(); err != nil || subs != nil {
		t.Fatalf("首次使用（无清单、无隔离副本）应为空清单: subs=%v err=%v", subs, err)
	}
	if h, err := LoadHistory("mid:1"); err != nil || h != nil {
		t.Fatalf("首次使用（无历史、无隔离副本）应为空历史: h=%v err=%v", h, err)
	}
}

// F6 订阅过滤：--filter 是稿件标题正则，存进清单、旧文件兼容、非法正则当场报错。

// TestSubStoreFilterPersistsAndStaysOptional: 过滤条件存进 JSON（F6），
// 旧清单没有这个字段时反序列化为空串 = 不过滤——升级不需要迁移文件。
func TestSubStoreFilterPersistsAndStaysOptional(t *testing.T) {
	dir := withTempRoot(t)

	if err := Add("mid:1", "某人", "^【(教程|实况)】"); err != nil {
		t.Fatal(err)
	}
	subs, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(subs) != 1 || subs[0].Filter != "^【(教程|实况)】" {
		t.Fatalf("过滤条件没有落盘: %+v", subs)
	}

	// 旧版清单：整个文件里没有 Filter 字段。
	legacy := "[{\"Target\":\"mid:2\",\"Name\":\"旧订阅\",\"AddedAt\":1}]"
	if err := os.WriteFile(filepath.Join(dir, "BBDownSubscriptions.json"), []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	subs, err = Load()
	if err != nil {
		t.Fatalf("旧清单必须仍然可读: %v", err)
	}
	if len(subs) != 1 || subs[0].Filter != "" {
		t.Fatalf("旧清单的过滤条件应为空（不过滤）: %+v", subs)
	}
	re, err := subs[0].CompileFilter()
	if err != nil || re != nil {
		t.Fatalf("空过滤 = 不过滤，实际 re=%v err=%v", re, err)
	}
}

// TestSubStoreRejectsInvalidFilter: 非法正则在 add 当场报错，且一个字都不写盘（F6）。
//
// 变异验证：去掉 Add 开头的 validateFilter，Add 会把坏正则存下来并返回 nil，本用例变红。
func TestSubStoreRejectsInvalidFilter(t *testing.T) {
	dir := withTempRoot(t)

	err := Add("mid:1", "", "[")
	if err == nil {
		t.Fatal("非法过滤正则在 add 时必须报错（否则要到 sub check 才发现订阅是坏的）")
	}
	if !strings.Contains(err.Error(), "过滤正则") {
		t.Errorf("报错要说清是过滤正则的问题，实际: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "BBDownSubscriptions.json")); statErr == nil {
		t.Error("非法正则不得写进清单")
	}
}

// TestSubStoreFilterUpdatedOnReAdd: 同一个 target 再 add 一次要连过滤条件一起更新，
// 否则用户改了 --filter 却仍是旧条件，且没有任何提示。
func TestSubStoreFilterUpdatedOnReAdd(t *testing.T) {
	withTempRoot(t)

	if err := Add("mid:1", "名字", "教程"); err != nil {
		t.Fatal(err)
	}
	if err := Add("mid:1", "名字2", "实况"); err != nil {
		t.Fatal(err)
	}
	subs, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(subs) != 1 || subs[0].Name != "名字2" || subs[0].Filter != "实况" {
		t.Fatalf("重复 add 没有更新名字/过滤条件: %+v", subs)
	}
}

// TestSubscriptionCompileFilter: 空过滤不过滤；合法正则可用；非法正则给出可读错误。
func TestSubscriptionCompileFilter(t *testing.T) {
	if re, err := (Subscription{Target: "mid:1"}).CompileFilter(); err != nil || re != nil {
		t.Errorf("空过滤应当返回 nil, nil，实际 re=%v err=%v", re, err)
	}
	re, err := (Subscription{Target: "mid:1", Filter: "教程|实况"}).CompileFilter()
	if err != nil || re == nil || !re.MatchString("【教程】第一课") {
		t.Errorf("合法正则应当可用: re=%v err=%v", re, err)
	}
	if _, err := (Subscription{Target: "mid:1", Filter: "["}).CompileFilter(); err == nil {
		t.Error("非法正则必须报错，而不是当成不过滤")
	} else if !strings.Contains(err.Error(), "过滤正则") {
		t.Errorf("错误信息要能读懂，实际: %v", err)
	}
}
