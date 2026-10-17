package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/QC3284/BBDown-Go/internal/entity"
)

// ---- t80：批量三件的用例（--delay-per-video / --download-all；--save-archives-to-file 已有实现）----

// withFakeBatchSleep 把批量间隔换成记录事件（不真睡）。
func withFakeBatchSleep(t *testing.T) *[]string {
	t.Helper()
	var events []string
	orig := batchSleep
	batchSleep = func(ctx context.Context, d time.Duration) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		events = append(events, "wait:"+d.String())
		return nil
	}
	t.Cleanup(func() { batchSleep = orig })
	return &events
}

// TestParseDelayPerVideo 钉住取值范围（0-600，默认 10；0 = 不等待）。
func TestParseDelayPerVideo(t *testing.T) {
	for _, ok := range []int{0, 1, 10, 600} {
		d, err := parseDelayPerVideo(ok)
		if err != nil || d != time.Duration(ok)*time.Second {
			t.Errorf("parseDelayPerVideo(%d) = (%v, %v)，期望 (%v, nil)", ok, d, err, time.Duration(ok)*time.Second)
		}
	}
	for _, bad := range []int{-1, 601} {
		_, err := parseDelayPerVideo(bad)
		if err == nil {
			t.Errorf("parseDelayPerVideo(%d) 应当报错", bad)
			continue
		}
		for _, want := range []string{"--delay-per-video", "0-600"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("错误文案缺少 %q：%v", want, err)
			}
		}
	}
	if defaultDelayPerVideo != 10 {
		t.Errorf("默认间隔应当对齐 BBDownT 的 10 秒，实际 %d", defaultDelayPerVideo)
	}
}

// TestRunTargetsDelayEventFlow 是 acceptance ① 的核心：批量循环里**首次不等待**、之后每个目标前
// 停顿 batchDelayPerVideo。
//
// 变异验证：撤掉 runTargets 里的 batchSleep 调用 → 事件流里没有 wait。
func TestRunTargetsDelayEventFlow(t *testing.T) {
	events := withFakeBatchSleep(t)
	old := batchDelayPerVideo
	batchDelayPerVideo = 2 * time.Second
	t.Cleanup(func() { batchDelayPerVideo = old })

	var order []string
	failures := runTargets(context.Background(), []string{"a", "b", "c"}, func(_ context.Context, target string) error {
		*events = append(*events, "run:"+target)
		order = append(order, target)
		return nil
	})
	if failures != 0 {
		t.Fatalf("不应有失败：%d", failures)
	}
	want := []string{"run:a", "wait:2s", "run:b", "wait:2s", "run:c"}
	if strings.Join(*events, ",") != strings.Join(want, ",") {
		t.Errorf("事件流 = %v, want %v", *events, want)
	}
	if strings.Join(order, ",") != "a,b,c" {
		t.Errorf("顺序被打乱：%v", order)
	}
}

// TestRunTargetsNoDelayWhenZero 钉住「0 = 不等待」（默认之外的显式关闭）。
func TestRunTargetsNoDelayWhenZero(t *testing.T) {
	events := withFakeBatchSleep(t)
	old := batchDelayPerVideo
	batchDelayPerVideo = 0
	t.Cleanup(func() { batchDelayPerVideo = old })

	runTargets(context.Background(), []string{"a", "b"}, func(_ context.Context, target string) error {
		*events = append(*events, "run:"+target)
		return nil
	})
	if strings.Join(*events, ",") != "run:a,run:b" {
		t.Errorf("0 秒不该有任何停顿：%v", *events)
	}
}

// TestDelayPerVideoIndependentOfDelayPerItem 钉住正交性：批量间隔用 batchSleep/3s，
// sub check 的间隔用 subCheckSleep/7s，互不影响。
func TestDelayPerVideoIndependentOfDelayPerItem(t *testing.T) {
	batchEvents := withFakeBatchSleep(t)
	oldBatch := batchDelayPerVideo
	batchDelayPerVideo = 3 * time.Second
	t.Cleanup(func() { batchDelayPerVideo = oldBatch })

	origSleep := subCheckSleep
	var subEvents []string
	subCheckSleep = func(_ context.Context, d time.Duration) error {
		subEvents = append(subEvents, "wait:"+d.String())
		return nil
	}
	t.Cleanup(func() { subCheckSleep = origSleep })

	runTargets(context.Background(), []string{"a", "b"}, func(context.Context, string) error { return nil })
	if strings.Join(*batchEvents, ",") != "wait:3s" {
		t.Errorf("批量间隔应当只出现 3s：%v", *batchEvents)
	}

	pacer := &subCheckPacer{delay: 7 * time.Second}
	_ = pacer.wait(context.Background()) // 首个不等待
	if err := pacer.wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.Join(subEvents, ",") != "wait:7s" {
		t.Errorf("sub check 的间隔应当只出现 7s：%v", subEvents)
	}
}

// TestSpaceMidOf 钉住空间目标识别（认不出来就原样走普通下载，不能误伤）。
func TestSpaceMidOf(t *testing.T) {
	cases := []struct {
		in  string
		mid string
		ok  bool
	}{
		{"https://space.bilibili.com/123456", "123456", true},
		{"https://space.bilibili.com/123456/video", "123456", true},
		{"https://space.bilibili.com/123456/video?tid=0#x", "123456", true},
		{"mid:987", "987", true},
		{"https://www.bilibili.com/video/BV1xx411c7mD", "", false},
		{"https://space.bilibili.com/abc", "", false},
		{"mid:", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		mid, ok := spaceMidOf(c.in)
		if mid != c.mid || ok != c.ok {
			t.Errorf("spaceMidOf(%q) = (%q, %v), want (%q, %v)", c.in, mid, ok, c.mid, c.ok)
		}
	}
}

// TestBuildDownloadAllTargets 是 acceptance ③ 的核心：空间目标 → 全部投稿逐条目标 + TXT 清单，
// 且清单能被 --urls-file 的解析器直接消费（生成与消费闭环）。
//
// 变异验证：撤掉展开（原样返回 targets）→ 本用例红；撤掉写文件 → 文件断言红。
func TestBuildDownloadAllTargets(t *testing.T) {
	dir := t.TempDir()
	listPath := filepath.Join(dir, "space-111.txt")

	fake := func(_ context.Context, mid string) ([]entity.Page, error) {
		switch mid {
		case "111":
			return []entity.Page{{Aid: "1001"}, {Aid: "1002"}}, nil
		case "222":
			return []entity.Page{{Aid: "2001"}}, nil
		}
		return nil, errors.New("未知 mid")
	}

	targets, err := buildDownloadAllTargets(context.Background(),
		[]string{"https://space.bilibili.com/111/video", "https://www.bilibili.com/video/BV1xx"}, fake, listPath)
	if err != nil {
		t.Fatalf("buildDownloadAllTargets: %v", err)
	}
	// 顺序 = 输入顺序：空间目标按投稿顺序展开，普通 URL 原样保留在原位。
	want := []string{"av1001", "av1002", "https://www.bilibili.com/video/BV1xx"}
	if strings.Join(targets, ",") != strings.Join(want, ",") {
		t.Errorf("目标 = %v, want %v", targets, want)
	}

	data, err := os.ReadFile(listPath)
	if err != nil {
		t.Fatalf("清单没写出来：%v", err)
	}
	text := string(data)
	if !strings.HasPrefix(text, "#") {
		t.Errorf("清单应当带 # 注释头（便于 --urls-file 跳过）：%q", text)
	}
	if strings.Count(text, "https://www.bilibili.com/video/av") != 2 {
		t.Errorf("清单应当有两条投稿：%q", text)
	}
	// 消费闭环：清单直接喂 readURLList → 两条 URL。
	consumed, err := readURLList(strings.NewReader(text))
	if err != nil {
		t.Fatalf("readURLList: %v", err)
	}
	if len(consumed) != 2 || consumed[0] != "https://www.bilibili.com/video/av1001/" {
		t.Errorf("清单消费结果 = %v", consumed)
	}
}

// TestBuildDownloadAllTargetsErrors：枚举失败 / 未接线都必须明确报错（不能静默少下）。
func TestBuildDownloadAllTargetsErrors(t *testing.T) {
	if _, err := buildDownloadAllTargets(context.Background(), []string{"mid:111"},
		func(context.Context, string) ([]entity.Page, error) { return nil, errors.New("boom") }, ""); err == nil {
		t.Error("枚举失败必须报错")
	} else if !strings.Contains(err.Error(), "boom") {
		t.Errorf("错误应当带上原因：%v", err)
	}
	if _, err := buildDownloadAllTargets(context.Background(), []string{"mid:111"}, nil, ""); err == nil {
		t.Error("未接线时必须报错（不能静默返回空）")
	}
	// 一个空间都没认出 → 不是错误，原样返回（--download-all 与普通 URL 混用时）。
	got, err := buildDownloadAllTargets(context.Background(),
		[]string{"https://www.bilibili.com/video/BV1xx"}, func(context.Context, string) ([]entity.Page, error) {
			t.Fatal("不该调用 lister")
			return nil, nil
		}, "")
	if err != nil || len(got) != 1 {
		t.Errorf("非空间目标应当原样保留：got %v err %v", got, err)
	}
}

// TestSpaceListPathFor 钉住清单落点命名：单空间 → space-<mid>.txt；多空间 → space-all.txt。
func TestSpaceListPathFor(t *testing.T) {
	dir := t.TempDir()
	if got := spaceListPathFor([]string{"mid:111"}, dir); got != filepath.Join(dir, "space-111.txt") {
		t.Errorf("单空间路径 = %q", got)
	}
	if got := spaceListPathFor([]string{"mid:111", "space.bilibili.com/222"}, dir); got != filepath.Join(dir, "space-all.txt") {
		t.Errorf("多空间路径 = %q", got)
	}
	if got := spaceListPathFor([]string{"https://www.bilibili.com/video/BV1xx"}, dir); got != "" {
		t.Errorf("没有空间时不该有清单路径：%q", got)
	}
}
