package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QC3284/BBDown-Go/internal/config"
	"github.com/QC3284/BBDown-Go/internal/entity"
	"github.com/QC3284/BBDown-Go/internal/fetcher"
	"github.com/QC3284/BBDown-Go/internal/substore"
	"github.com/QC3284/BBDown-Go/internal/util"
)

// ---- 订阅调度（--since 增量窗口 / --concurrency 并发检查）的回归网 ----
//
// 全部用例都走 subCheckDeps 注入的假依赖 + 临时 StoreRoot：不碰真网、不写包目录。
// 并发、订阅顺序、中断、状态文件这些判据因此能在 CI 上稳定复现——真网用例既控制不了
// 检查的返回顺序，也造不出「取消时正好有 worker 在飞」的时刻。

// 等待上限只作兜底：正常路径上这些 channel 立即就绪（判据本身不看墙钟）。
// 没有上限的话，一个被改成串行的实现会让用例永久挂住，失败形态从「断言红」变成「超时」。
const subCheckTestBarrierTimeout = 2 * time.Second

// setSubStoreRoot 把订阅清单与历史指到 dir，用例结束还原。
// 一个用例里可以多次调用（如「串行跑一次、并行跑一次」要各自的空历史）。
func setSubStoreRoot(t *testing.T, dir string) {
	t.Helper()
	prev := substore.StoreRoot
	substore.StoreRoot = dir
	t.Cleanup(func() { substore.StoreRoot = prev })
}

// dirSignature 把目录下所有文件的名字与内容摘要拼成一个可比较的字符串，
// 用来判「一个字节都没写」——只比名字会漏掉「原地改写了既有文件」。
func dirSignature(t *testing.T, dir string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("读取目录 %s: %v", dir, err)
	}
	var parts []string
	for _, e := range entries {
		if e.IsDir() {
			parts = append(parts, e.Name()+"=<dir>")
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("读取 %s: %v", e.Name(), err)
		}
		sum := sha256.Sum256(data)
		parts = append(parts, e.Name()+"="+hex.EncodeToString(sum[:]))
	}
	sort.Strings(parts)
	return strings.Join(parts, "\n")
}

// mustAddSub 往临时清单里加一条订阅并返回它（顺带覆盖真实写入路径）。
func mustAddSub(t *testing.T, target, name string) substore.Subscription {
	t.Helper()
	if err := substore.Add(target, name, ""); err != nil {
		t.Fatalf("substore.Add(%s): %v", target, err)
	}
	subs, err := substore.ListSorted()
	if err != nil {
		t.Fatalf("substore.ListSorted: %v", err)
	}
	for _, s := range subs {
		if s.Target == target {
			return s
		}
	}
	t.Fatalf("订阅 %s 没写进清单", target)
	return substore.Subscription{}
}

// vinfo 造一份假稿件：aids 的顺序就是分P 顺序，pubTimes 缺键 = 发布时间未知（0）。
func vinfo(title string, aids []string, pubTimes map[string]int64) *entity.VInfo {
	pages := make([]entity.Page, 0, len(aids))
	for _, aid := range aids {
		pages = append(pages, entity.Page{Aid: aid, PubTime: pubTimes[aid]})
	}
	return &entity.VInfo{Title: title, PagesInfo: pages}
}

// fakeDeps 造一份假依赖：resolve 原样返回 target（mid:x 这类前缀目标本身就是标识），
// fetch 查表，download 记录顺序。infos 只被并发读。
func fakeDeps(infos map[string]*entity.VInfo, download func(aid string) error) subCheckDeps {
	return subCheckDeps{
		resolve: func(_ context.Context, target string) (string, error) { return target, nil },
		fetch: func(_ context.Context, resolved string) (*entity.VInfo, error) {
			vi, ok := infos[resolved]
			if !ok {
				return nil, fmt.Errorf("没有为 %s 准备假稿件", resolved)
			}
			return vi, nil
		},
		download: func(_ context.Context, aid string) error {
			if download != nil {
				return download(aid)
			}
			return nil
		},
	}
}

// recordingDownload 返回记录下载顺序的 download，以及读取记录的函数。
func recordingDownload() (func(aid string) error, func() []string) {
	var mu sync.Mutex
	var got []string
	return func(aid string) error {
			mu.Lock()
			got = append(got, aid)
			mu.Unlock()
			return nil
		}, func() []string {
			mu.Lock()
			defer mu.Unlock()
			return append([]string(nil), got...)
		}
}

// assertHistory 断言某个订阅的历史内容（逗号连接）。
func assertHistory(t *testing.T, target, want string) {
	t.Helper()
	got, err := substore.LoadHistory(target)
	if err != nil {
		t.Fatalf("LoadHistory(%s): %v", target, err)
	}
	if strings.Join(got, ",") != want {
		t.Errorf("订阅 %s 的历史: got %q want %q", target, strings.Join(got, ","), want)
	}
}

// stripANSI 删掉日志里的 ANSI 色码（util.LogWarn 会给警告行上色，管道/非 TTY 下也一样）。
// 不用正则：ESC 字符写在源码里读不出来（终端观感的坑同一个道理）。
func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && !(s[j] >= 'a' && s[j] <= 'z' || s[j] >= 'A' && s[j] <= 'Z') {
				j++
			}
			if j < len(s) {
				j++
			}
			i = j
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// logMessages 把捕获到的日志拆成「去掉时间戳与色码后的行」，用于逐字比较消息与顺序。
func logMessages(out string) []string {
	out = strings.TrimRight(stripANSI(out), "\n")
	if out == "" {
		return nil
	}
	lines := strings.Split(out, "\n")
	for i, l := range lines {
		if strings.HasPrefix(l, "[") {
			if j := strings.Index(l, "] - "); j > 0 {
				lines[i] = l[j+4:]
			}
		}
	}
	return lines
}

// TestParseSubCheckSchedule 钉住 --since / --concurrency 的取值域：
// 默认（不带新参数）必须是「不做窗口过滤 + 串行检查」，非法值必须当场报错并指出是哪个参数，
// 而且 "1d" 的报错要给出可照抄的写法（用户照直觉写天数单位时唯一的线索）。
//
// 变异验证：把 d <= 0 的检查去掉（"0" 静默当成不启用）→ 本用例的 "0"/"-1h" 两行变红；
// 把 concurrency 的上限判断去掉 → 9 那一行变红。
func TestParseSubCheckSchedule(t *testing.T) {
	cases := []struct {
		name        string
		since       string
		concurrency int
		wantSince   time.Duration
		wantErr     string // 期望错误里必须出现的片段（空 = 必须成功）
	}{
		{"默认不启用窗口", "", 1, 0, ""},
		{"24h 窗口", "24h", 1, 24 * time.Hour, ""},
		{"30m 窗口", "30m", 1, 30 * time.Minute, ""},
		{"并发上限 8", "", subCheckMaxConcurrency, 0, ""},
		{"since 文本非法", "abc", 1, 0, "--since"},
		{"since 天数单位不支持", "1d", 1, 0, "24h"},
		{"since 为 0", "0", 1, 0, "--since"},
		{"since 为负", "-1h", 1, 0, "--since"},
		{"并发为 0", "", 0, 0, "--concurrency"},
		{"并发为负", "", -1, 0, "--concurrency"},
		{"并发超上限", "", 9, 0, "--concurrency"},
	}
	for _, c := range cases {
		sched, err := parseSubCheckSchedule(c.since, c.concurrency)
		if c.wantErr != "" {
			if err == nil {
				t.Fatalf("%s（%q/%d）：非法值必须报错", c.name, c.since, c.concurrency)
			}
			if !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("%s: 报错里应当出现 %q（用户要知道改哪个参数）: %v", c.name, c.wantErr, err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s（%q/%d）: %v", c.name, c.since, c.concurrency, err)
		}
		if sched.Since != c.wantSince || sched.Concurrency != c.concurrency {
			t.Errorf("%s: got %+v want since=%v concurrency=%d", c.name, sched, c.wantSince, c.concurrency)
		}
	}
}

// TestSubCheckScheduleFlagDefaults 钉住两个开关本身的默认值与类型：
// 默认 concurrency=1、since 空 → 走与 2.12.8 完全相同的串行路径（默认观感属于用户界面契约）。
func TestSubCheckScheduleFlagDefaults(t *testing.T) {
	for _, c := range []struct{ name, def, typ string }{
		{"since", "", "string"},
		{"concurrency", "1", "int"},
	} {
		f := subCheckCmd.Flags().Lookup(c.name)
		if f == nil {
			t.Fatalf("sub check 缺 --%s", c.name)
		}
		if f.DefValue != c.def || f.Value.Type() != c.typ {
			t.Errorf("--%s 应为 %s 且默认 %q（默认必须与改前一致），实际 %s/%q",
				c.name, c.typ, c.def, f.Value.Type(), f.DefValue)
		}
	}
}

// TestSubSinceFilterWindow 逐条钉住 --since 的窗口判定：
// 窗口外的进 skipped、恰在边界上的算窗口内、发布时间未知（PubTime=0 或表里没有）不因 since 过滤。
//
// 变异验证：把 ">" 改成 ">="（边界被排除）→ 边界那一行变红；把 unknown 分支并进 skipped
// → 未知那一行变红。
func TestSubSinceFilterWindow(t *testing.T) {
	now := time.Date(2024, 5, 1, 12, 0, 0, 0, time.UTC)
	since := 24 * time.Hour
	at := func(d time.Duration) int64 { return now.Add(-d).Unix() }

	aids := []string{"1", "2", "3", "4", "5", "6"}
	pubTimes := map[string]int64{
		"1": at(25 * time.Hour),             // 窗口外
		"2": at(24 * time.Hour),             // 恰在边界 → 窗口内
		"3": at(time.Hour),                  // 窗口内
		"4": 0,                              // 发布时间未知
		"6": at(24*time.Hour + time.Second), // 比边界再旧 1 秒 → 窗口外
		// "5" 不在表里：同样按未知处理
	}
	kept, skipped, unknown := subSinceFilter(aids, pubTimes, since, now)
	if got := strings.Join(kept, ","); got != "2,3,4,5" {
		t.Errorf("窗口内（含边界与未知）: got %q want %q", got, "2,3,4,5")
	}
	if got := strings.Join(skipped, ","); got != "1,6" {
		t.Errorf("窗口外: got %q want %q", got, "1,6")
	}
	if got := strings.Join(unknown, ","); got != "4,5" {
		t.Errorf("发布时间未知: got %q want %q", got, "4,5")
	}
}

// TestSubPubTimesPicksFirstNonZero：同一 aid 多个分P 时取第一个非零 PubTime——
// 缺 PubTime 的分P 不能把整稿判成「未知」（会绕过 --since 的过滤）。
func TestSubPubTimesPicksFirstNonZero(t *testing.T) {
	pages := []entity.Page{
		{Aid: "1", PubTime: 0},
		{Aid: "1", PubTime: 100}, // 同稿的另一个分P 有真实时间
		{Aid: "2", PubTime: 200},
		{Aid: "2", PubTime: 999}, // 已有非零值时不被覆盖
		{Aid: "", PubTime: 300},  // 空 aid 不进表
	}
	got := subPubTimes(pages)
	if len(got) != 2 || got["1"] != 100 || got["2"] != 200 {
		t.Fatalf("aid → 发布时间: got %v want map[1:100 2:200]", got)
	}
}

// TestSubCheckInvalidScheduleWritesNothing：非法 --since / --concurrency 必须
// 「当场报错 + 退出非 0 + 不写任何状态文件」——校验放在 runSubCheck 最前面，
// 连损坏清单的隔离与网络请求都不会发生。
//
// 变异验证：把校验移到 substore.Load() 之后 → 损坏清单（这里用「损坏历史」等价物）
// 会被隔离、状态目录签名变化，本用例变红；把上限判断删掉 → 9 那一行变红。
func TestSubCheckInvalidScheduleWritesNothing(t *testing.T) {
	dir := t.TempDir()
	setSubStoreRoot(t, dir)
	mustAddSub(t, "mid:1", "甲")
	before := dirSignature(t, dir)

	reset := func() {
		optSubCheckSince = ""
		optSubCheckConcurrency = 1
	}
	reset()
	t.Cleanup(reset)

	for _, c := range []struct {
		since string
		conc  int
	}{
		{"abc", 1}, {"1d", 1}, {"0", 1}, {"-1h", 1},
		{"", 0}, {"", -1}, {"", 9},
	} {
		optSubCheckSince, optSubCheckConcurrency = c.since, c.conc
		err := runSubCheck(subCheckCmd, nil)
		if err == nil {
			t.Fatalf("--since %q / --concurrency %d 必须在检查前就报错", c.since, c.conc)
		}
		if !strings.Contains(err.Error(), "--since") && !strings.Contains(err.Error(), "--concurrency") {
			t.Errorf("--since %q / --concurrency %d 的报错要指出参数名: %v", c.since, c.conc, err)
		}
		if code := exitCodeFor(err); code == 0 {
			t.Errorf("--since %q / --concurrency %d 的退出码必须非 0", c.since, c.conc)
		}
		if after := dirSignature(t, dir); after != before {
			t.Fatalf("--since %q / --concurrency %d 不得写任何状态文件：\nbefore=%s\nafter=%s",
				c.since, c.conc, before, after)
		}
	}

	// 非数字的 --concurrency 由 pflag 在解析阶段拒绝（RunE 根本不会跑）：同样不许写状态文件。
	t.Cleanup(func() {
		rootCmd.SetArgs(nil)
		reset()
	})
	rootCmd.SetArgs([]string{"sub", "check", "--concurrency", "abc"})
	if err := rootCmd.Execute(); err == nil {
		t.Fatal("--concurrency abc 必须当场报错")
	}
	if after := dirSignature(t, dir); after != before {
		t.Fatalf("非数字的 --concurrency 不得写任何状态文件：\nbefore=%s\nafter=%s", before, after)
	}

	// 校验还必须早于 Load：清单损坏时 substore.Load 会把文件「隔离」（重命名）——那也是写。
	// 参数打错的一次调用连这份隔离都不该发生（否则用户只是敲错一个数字，清单就被改名了）。
	corruptDir := t.TempDir()
	setSubStoreRoot(t, corruptDir)
	corruptPath := filepath.Join(corruptDir, "BBDownSubscriptions.json")
	if err := os.WriteFile(corruptPath, []byte("{ 这不是合法 JSON"), 0o644); err != nil {
		t.Fatal(err)
	}
	corruptBefore := dirSignature(t, corruptDir)
	optSubCheckSince, optSubCheckConcurrency = "", 0
	err := runSubCheck(subCheckCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "--concurrency") {
		t.Fatalf("非法 --concurrency 必须优先报参数错误，实际 %v", err)
	}
	if after := dirSignature(t, corruptDir); after != corruptBefore {
		t.Fatalf("非法参数不得隔离（重命名）损坏清单：\nbefore=%s\nafter=%s", corruptBefore, after)
	}
}

// TestSubCheckSinceWindowEndToEnd 是 --since 的离线端到端：假稿件给出窗口内/边界/窗口外/未知四种
// 发布时间，断言「谁被下载、谁进历史、谁只留日志」。
// 关键点：窗口外的 aid 不能进历史——否则窗口放宽后永远补不下它。
func TestSubCheckSinceWindowEndToEnd(t *testing.T) {
	dir := t.TempDir()
	setSubStoreRoot(t, dir)
	subA := mustAddSub(t, "mid:1", "甲")
	subB := mustAddSub(t, "mid:2", "乙")

	now := time.Date(2024, 5, 1, 12, 0, 0, 0, time.UTC)
	since := 24 * time.Hour
	at := func(d time.Duration) int64 { return now.Add(-d).Unix() }
	infos := map[string]*entity.VInfo{
		"mid:1": vinfo("甲的最新稿", []string{"101", "102", "103"}, map[string]int64{
			"101": at(48 * time.Hour), // 窗口外 → 跳过
			"102": at(24 * time.Hour), // 恰在边界 → 下载
			"103": at(2 * time.Hour),  // 窗口内 → 下载
		}),
		"mid:2": vinfo("乙的最新稿", []string{"201"}, nil), // 发布时间未知 → 不因 since 过滤
	}
	dl, downloads := recordingDownload()

	out := captureStdout(t, func() {
		failures, err := subCheckRun(context.Background(), []substore.Subscription{subA, subB},
			fakeDeps(infos, dl), subCheckSchedule{Since: since, Concurrency: 1}, now)
		if err != nil {
			t.Fatalf("subCheckRun: %v", err)
		}
		if failures != 0 {
			t.Fatalf("不该有下载失败，实际 %d", failures)
		}
	})

	if got := strings.Join(downloads(), ","); got != "102,103,201" {
		t.Fatalf("窗口判定后的下载集合/顺序: got %q want %q", got, "102,103,201")
	}
	assertHistory(t, "mid:1", "102,103") // 窗口外的 101 不进历史
	assertHistory(t, "mid:2", "201")

	msgs := strings.Join(logMessages(out), "\n")
	if !strings.Contains(msgs, "跳过 1 个超出 --since "+since.String()+" 窗口的内容: av101") {
		t.Errorf("窗口外内容要留一条日志（cron 日志里能看出为什么没下）: %q", msgs)
	}
	if !strings.Contains(msgs, "1 个内容的发布时间未知，不按 --since 过滤: av201") {
		t.Errorf("发布时间未知要留一条日志（说明为什么它绕过了窗口）: %q", msgs)
	}
}

// TestSubCheckConcurrencyMatchesSerialAndKeepsOrder 是 --concurrency 的核心判据：
//
//	① 默认（Concurrency=1）的日志文本与顺序逐字固定——默认行为必须与 2.12.8 一致；
//	② Concurrency=2 时检查阶段**真的**重叠执行（观察峰值并发），但下载仍严格按订阅顺序，
//	   最终下载的 aid 序列与串行结果逐字相同。
//
// 变异验证：把下载阶段也放进 worker（并发下载）→ 下载序列在多数运行里不再是订阅顺序，本用例变红；
// 把 worker 池换成串行 for → 峰值并发断言变红。
func TestSubCheckConcurrencyMatchesSerialAndKeepsOrder(t *testing.T) {
	now := time.Date(2024, 5, 1, 12, 0, 0, 0, time.UTC)
	wantOrder := "101,102,201,202,301,302,401,402"
	mk := func() ([]substore.Subscription, map[string]*entity.VInfo) {
		subs := []substore.Subscription{
			{Target: "mid:1", Name: "甲"},
			{Target: "mid:2", Name: "乙"},
			{Target: "mid:3", Name: "丙"},
			{Target: "mid:4", Name: "丁"},
		}
		infos := make(map[string]*entity.VInfo, len(subs))
		for i, s := range subs {
			infos[s.Target] = vinfo(s.Name, []string{
				fmt.Sprintf("%d01", i+1),
				fmt.Sprintf("%d02", i+1),
			}, nil)
		}
		return subs, infos
	}

	// ① 串行（默认路径）：基准 + 输出逐字固定。
	setSubStoreRoot(t, t.TempDir())
	subs, infos := mk()
	dl, downloads := recordingDownload()
	serialOut := captureStdout(t, func() {
		failures, err := subCheckRun(context.Background(), subs, fakeDeps(infos, dl),
			subCheckSchedule{Concurrency: 1}, now)
		if err != nil || failures != 0 {
			t.Fatalf("串行跑失败: failures=%d err=%v", failures, err)
		}
	})
	if got := strings.Join(downloads(), ","); got != wantOrder {
		t.Fatalf("串行下载顺序: got %q want %q", got, wantOrder)
	}
	// 注意 "av101"：这里钉的是 2.13.0 修复后的形态。2.12.8 输出过 "avav101"（格式串 av +
	// joinAids 的 av 双重前缀），是显示缺陷而非上游观感契约；用户确认作为缺陷修复，
	// 属有意偏离逐字基线（CHANGELOG 2.13.0 已登记）。
	wantLog := strings.Join([]string{
		"检查订阅: 甲 (mid:1)",
		"  发现 2 个新内容: av101, av102",
		"检查订阅: 乙 (mid:2)",
		"  发现 2 个新内容: av201, av202",
		"检查订阅: 丙 (mid:3)",
		"  发现 2 个新内容: av301, av302",
		"检查订阅: 丁 (mid:4)",
		"  发现 2 个新内容: av401, av402",
		"订阅检查完成",
	}, "\n")
	if got := strings.Join(logMessages(serialOut), "\n"); got != wantLog {
		t.Errorf("默认（不带新参数）的输出必须与改前逐字一致，实际:\n%s", got)
	}

	// ② 并发 2：检查重叠执行（barrier 观察峰值并发），下载顺序仍是订阅顺序。
	setSubStoreRoot(t, t.TempDir())
	subs, infos = mk()
	dl2, downloads2 := recordingDownload()
	var mu sync.Mutex
	inflight, peak := 0, 0
	release := make(chan struct{})
	var once sync.Once
	deps := fakeDeps(infos, dl2)
	deps.fetch = func(_ context.Context, resolved string) (*entity.VInfo, error) {
		mu.Lock()
		inflight++
		if inflight > peak {
			peak = inflight
		}
		reached := inflight >= 2
		mu.Unlock()
		if reached {
			once.Do(func() { close(release) })
		}
		select {
		case <-release:
		case <-time.After(subCheckTestBarrierTimeout):
		}
		mu.Lock()
		inflight--
		mu.Unlock()
		return infos[resolved], nil
	}
	parallelOut := captureStdout(t, func() {
		failures, err := subCheckRun(context.Background(), subs, deps,
			subCheckSchedule{Concurrency: 2}, now)
		if err != nil || failures != 0 {
			t.Fatalf("并发跑失败: failures=%d err=%v", failures, err)
		}
	})
	if peak < 2 {
		t.Errorf("--concurrency 2 下检查必须真的重叠执行（观察到的峰值并发 %d）：串行化会让这个开关失去意义", peak)
	}
	if got := strings.Join(downloads2(), ","); got != wantOrder {
		t.Errorf("并发检查后的下载集合/顺序必须与串行一致: got %q want %q", got, wantOrder)
	}
	// 汇报顺序同样按订阅顺序（并发检查的结论按订阅序收集后统一汇报）。
	var parallelReport []string
	for _, m := range logMessages(parallelOut) {
		if strings.HasPrefix(m, "检查订阅") || strings.HasPrefix(m, "  发现") {
			parallelReport = append(parallelReport, m)
		}
	}
	var serialReport []string
	for _, m := range logMessages(serialOut) {
		if strings.HasPrefix(m, "检查订阅") || strings.HasPrefix(m, "  发现") {
			serialReport = append(serialReport, m)
		}
	}
	if strings.Join(parallelReport, "\n") != strings.Join(serialReport, "\n") {
		t.Errorf("并发下的汇报顺序必须与串行一致:\ngot:\n%s\nwant:\n%s",
			strings.Join(parallelReport, "\n"), strings.Join(serialReport, "\n"))
	}
}

// TestSubCheckSerialKeepsCheckLogBeforeFetcherLogs：串行路径（默认）的日志顺序必须与改前一致——
// fetcher 自己会打日志（internal/fetcher/spacevideo.go 的「共 N 个投稿, 正在获取分P信息...」），
// 「检查订阅: ...」必须先出现，否则用户不知道那行「正在获取」属于哪个订阅。
//
// 变异验证：把串行路径也改成「先全部检查、再统一汇报」→ 本用例的顺序断言变红。
func TestSubCheckSerialKeepsCheckLogBeforeFetcherLogs(t *testing.T) {
	setSubStoreRoot(t, t.TempDir())
	now := time.Date(2024, 5, 1, 12, 0, 0, 0, time.UTC)
	subs := []substore.Subscription{
		{Target: "mid:1", Name: "甲"},
		{Target: "mid:2", Name: "乙"},
	}
	infos := map[string]*entity.VInfo{
		"mid:1": vinfo("甲", []string{"1"}, nil),
		"mid:2": vinfo("乙", []string{"2"}, nil),
	}
	deps := fakeDeps(infos, nil)
	baseFetch := deps.fetch
	deps.fetch = func(ctx context.Context, resolved string) (*entity.VInfo, error) {
		util.Log("共 1 个投稿, 正在获取分P信息...") // 模拟 fetcher 在拉取阶段自己打的日志
		return baseFetch(ctx, resolved)
	}

	out := captureStdout(t, func() {
		failures, err := subCheckRun(context.Background(), subs, deps, subCheckSchedule{Concurrency: 1}, now)
		if err != nil || failures != 0 {
			t.Fatalf("串行跑失败: failures=%d err=%v", failures, err)
		}
	})
	want := strings.Join([]string{
		"检查订阅: 甲 (mid:1)",
		"共 1 个投稿, 正在获取分P信息...",
		"  发现 1 个新内容: av1",
		"检查订阅: 乙 (mid:2)",
		"共 1 个投稿, 正在获取分P信息...",
		"  发现 1 个新内容: av2",
		"订阅检查完成",
	}, "\n")
	if got := strings.Join(logMessages(out), "\n"); got != want {
		t.Errorf("串行路径的日志顺序必须与改前一致，实际:\n%s\nwant:\n%s", got, want)
	}
}

// TestSubCheckRecordsOnlySuccessfulDownloads：下载失败的 aid 不进历史——RecordDownloaded 只在
// 下载成功之后调用，否则一次失败会被记成「已完成」，cron 下一轮永远补不下它。
//
// 变异验证：把 RecordDownloaded 提到 download 之前（或忽略 download 的错误）→ 历史断言变红。
func TestSubCheckRecordsOnlySuccessfulDownloads(t *testing.T) {
	setSubStoreRoot(t, t.TempDir())
	now := time.Date(2024, 5, 1, 12, 0, 0, 0, time.UTC)
	sub := mustAddSub(t, "mid:1", "甲")
	infos := map[string]*entity.VInfo{"mid:1": vinfo("甲", []string{"1", "2", "3"}, nil)}
	deps := fakeDeps(infos, func(aid string) error {
		if aid == "2" {
			return errors.New("boom")
		}
		return nil
	})

	var failures int
	out := captureStdout(t, func() {
		var err error
		failures, err = subCheckRun(context.Background(), []substore.Subscription{sub}, deps,
			subCheckSchedule{Concurrency: 1}, now)
		if err != nil {
			t.Fatalf("单个下载失败不该中止整次检查: %v", err)
		}
	})
	if failures != 1 {
		t.Errorf("下载失败数: got %d want 1", failures)
	}
	if code := exitCodeFor(subCheckResult(false, failures)); code == 0 {
		t.Error("有下载失败时退出码必须非 0（脚本/cron 才能发现）")
	}
	assertHistory(t, "mid:1", "1,3") // 失败的 2 不进历史
	if msgs := strings.Join(logMessages(out), "\n"); !strings.Contains(msgs, "av2 下载失败: boom") {
		t.Errorf("失败要留日志: %q", msgs)
	}
}

// TestSubCheckCancelStopsAllWorkersAndWritesNoState：中断（ctx 取消）语义不变——
//
//	① 取消传导到所有 worker：返回时每个 worker 都已经走完（join，不留后台协程）；
//	② 取消后不下载、也不汇报（与改前一致：订阅边界上的取消直接走 silenceOnCancel 语义）；
//	③ 状态文件不半截：历史上一个 aid 都没有（RecordDownloaded 只在下载成功后调用）。
func TestSubCheckCancelStopsAllWorkersAndWritesNoState(t *testing.T) {
	dir := t.TempDir()
	setSubStoreRoot(t, dir)
	subs := []substore.Subscription{
		{Target: "mid:1", Name: "甲"},
		{Target: "mid:2", Name: "乙"},
		{Target: "mid:3", Name: "丙"},
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	finished := make(chan string, len(subs)) // 每个 worker 结束前发一次
	var started int32
	allIn := make(chan struct{})
	var inOnce sync.Once
	deps := subCheckDeps{
		resolve: func(ctx context.Context, target string) (string, error) {
			defer func() { finished <- target }()
			// 等三个 worker 都进入检查阶段再取消（并发 3 == 订阅数，不会自锁），
			// 这样每个 worker 都真的在飞，取消必须把它们都收回来。
			if atomic.AddInt32(&started, 1) == int32(len(subs)) {
				inOnce.Do(func() { close(allIn) })
			}
			select {
			case <-allIn:
			case <-time.After(subCheckTestBarrierTimeout):
			}
			if target == "mid:1" {
				cancel()
				return "", context.Canceled
			}
			<-ctx.Done()
			return "", ctx.Err()
		},
		fetch: func(context.Context, string) (*entity.VInfo, error) {
			t.Error("取消后不该再进入拉取阶段")
			return nil, errors.New("不应该到这里")
		},
		download: func(context.Context, string) error {
			t.Error("取消后不该下载任何内容")
			return nil
		},
	}

	var failures int
	var err error
	out := captureStdout(t, func() {
		failures, err = subCheckRun(ctx, subs, deps, subCheckSchedule{Concurrency: 3}, time.Now())
	})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("订阅边界上的取消要返回 context.Canceled（改前 same 语义），实际 %v", err)
	}
	if failures != 0 {
		t.Errorf("取消时不该累计下载失败: %d", failures)
	}
	// ① 返回即代表所有 worker 已经退出：三条收尾信号必须都已发出（未 join 的实现这里会缺信号）。
	for i := 0; i < len(subs); i++ {
		select {
		case <-finished:
		default:
			t.Fatalf("subCheckRun 返回时还有 worker 没退出（第 %d 个）", i+1)
		}
	}
	// ② 没有「发现新内容/订阅检查完成」这类汇报或下载。
	for _, m := range logMessages(out) {
		if strings.HasPrefix(m, "检查订阅") || strings.HasPrefix(m, "  发现") || strings.Contains(m, "订阅检查完成") {
			t.Errorf("取消后不该汇报或下载，实际输出: %q", m)
		}
	}
	// ③ 状态文件不半截。
	histPath := filepath.Join(dir, "BBDownSubscriptions.history.json")
	if _, statErr := os.Stat(histPath); !os.IsNotExist(statErr) {
		t.Fatalf("取消后不得留下历史文件（RecordDownloaded 只能在下载成功后调用），stat err=%v", statErr)
	}
}

// TestSubCheckSharedClientIsConcurrencySafe 支撑 defaultSubCheckDeps 注释里的并发安全依据：
// 并发检查阶段共享同一个 client / factory / wbi，其中 HTTPClient 唯一的可变字段是自动 UA
// （uaMu 保护）。这里用本地回环服务器并发打同一个 client（一半协程在轮换 UA），
// 在 -race 下跑可以抓住任何未加锁的读写。
func TestSubCheckSharedClientIsConcurrencySafe(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()

	client := buildHTTPClient(config.DefaultMyOption())
	factory := fetcher.NewFactory(client, false, "wbi-preset", "", "api.bilibili.com", "api.bilibili.com", "")

	var wg sync.WaitGroup
	errs := make(chan error, 64)
	for i := 0; i < 9; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 4; j++ {
				switch i % 3 {
				case 0:
					client.RotateAutomaticUserAgent()
				case 1:
					body, err := client.GetWebSource(context.Background(), srv.URL)
					if err != nil {
						errs <- err
						return
					}
					if body != "ok" {
						errs <- fmt.Errorf("响应体 = %q", body)
						return
					}
				default:
					_ = factory.Create("mid:1") // 字段只读，Create 每次返回新的 Fetcher
				}
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("并发共享 client/factory 出错: %v", err)
	}
}

// ---- t71：--delay-per-item 节流（吸收上游 1.7.7）----
//
// 判据全部在**事件流**上：注入假等待（subCheckSleep）、假 fetcher、假下载，按真实顺序断言
// 「list → delay → download → delay → …」。这样不靠墙钟，也就不受慢速 CI runner 影响。

// withFakeSleep 把停顿换成记录事件（不真睡），返回事件切片指针；用例结束还原。
func withFakeSleep(t *testing.T) *[]string {
	t.Helper()
	var events []string
	orig := subCheckSleep
	subCheckSleep = func(ctx context.Context, d time.Duration) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		events = append(events, "wait:"+d.String())
		return nil
	}
	t.Cleanup(func() { subCheckSleep = orig })
	return &events
}

// eventDeps 造一组会往 events 里记事件的假依赖：fetch=列举（list:<target>），download=下载。
func eventDeps(t *testing.T, events *[]string, aids map[string][]string) subCheckDeps {
	t.Helper()
	return subCheckDeps{
		resolve: func(_ context.Context, target string) (string, error) { return target, nil },
		fetch: func(_ context.Context, resolved string) (*entity.VInfo, error) {
			*events = append(*events, "list:"+resolved)
			var pages []entity.Page
			for _, aid := range aids[resolved] {
				pages = append(pages, entity.Page{Aid: aid, PubTime: time.Now().Unix()})
			}
			return &entity.VInfo{Title: resolved, PagesInfo: pages}, nil
		},
		download: func(_ context.Context, aid string) error {
			*events = append(*events, "download:"+aid)
			return nil
		},
	}
}

// TestParseDelayPerItem 钉住取值范围与错误文案：0~600 合法；越界必须点名选项名与范围。
func TestParseDelayPerItem(t *testing.T) {
	for _, ok := range []int{0, 1, 60, 600} {
		d, err := parseDelayPerItem(ok)
		if err != nil {
			t.Errorf("parseDelayPerItem(%d) 不该报错: %v", ok, err)
			continue
		}
		if d != time.Duration(ok)*time.Second {
			t.Errorf("parseDelayPerItem(%d) = %v, want %v", ok, d, time.Duration(ok)*time.Second)
		}
	}
	for _, bad := range []int{-1, 601, -100} {
		_, err := parseDelayPerItem(bad)
		if err == nil {
			t.Errorf("parseDelayPerItem(%d) 应当报错", bad)
			continue
		}
		for _, want := range []string{"--delay-per-item", "0-600"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("错误文案缺少 %q：%v", want, err)
			}
		}
	}
}

// TestSubCheckDelayEventFlow 是核心用例：两个订阅各一个新增内容，--delay-per-item 2s →
// 事件流必须是 list → delay → download → delay → list → delay → download，
// 即「首个单元不等待、之后每个单元前都停顿」。
//
// 变异验证：撤掉 subCheckRun 里对 pacer.wait 的调用 → 本用例红（事件流里没有 wait）。
func TestSubCheckDelayEventFlow(t *testing.T) {
	setSubStoreRoot(t, t.TempDir())
	origFlag := optSubCheckDelayPerItem
	optSubCheckDelayPerItem = 0
	t.Cleanup(func() { optSubCheckDelayPerItem = origFlag })

	events := withFakeSleep(t)
	subs := []substore.Subscription{
		{Target: "mid:1", Name: "a"},
		{Target: "mid:2", Name: "b"},
	}
	deps := eventDeps(t, events, map[string][]string{"mid:1": {"101"}, "mid:2": {"102"}})
	sched := subCheckSchedule{Concurrency: 1, DelayPerItem: 2 * time.Second}
	if _, err := subCheckRun(context.Background(), subs, deps, sched, time.Now()); err != nil {
		t.Fatalf("subCheckRun: %v", err)
	}
	want := []string{
		"list:mid:1",
		"wait:2s", "download:101",
		"wait:2s", "list:mid:2",
		"wait:2s", "download:102",
	}
	if strings.Join(*events, ",") != strings.Join(want, ",") {
		t.Errorf("事件流 = %v\n               want %v", *events, want)
	}
	if n := len(*events); n != len(want) {
		t.Errorf("事件数 = %d, want %d", n, len(want))
	}
}

// TestSubCheckDelayKeepsGapWithoutNewContent 钉住「无新增内容时相邻订阅之间仍停顿」：
// 两个订阅都已下载过 → 工作单元只有两次「开始检查」，中间仍要有一个停顿。
func TestSubCheckDelayKeepsGapWithoutNewContent(t *testing.T) {
	root := t.TempDir()
	setSubStoreRoot(t, root)
	origFlag := optSubCheckDelayPerItem
	optSubCheckDelayPerItem = 0
	t.Cleanup(func() { optSubCheckDelayPerItem = origFlag })

	// 先把两个 aid 记进历史，制造「没有新增内容」。
	for _, target := range []string{"mid:1", "mid:2"} {
		if err := substore.RecordDownloaded(target, "101"); err != nil {
			t.Fatal(err)
		}
	}
	events := withFakeSleep(t)
	subs := []substore.Subscription{{Target: "mid:1", Name: "a"}, {Target: "mid:2", Name: "b"}}
	deps := eventDeps(t, events, map[string][]string{"mid:1": {"101"}, "mid:2": {"101"}})
	if _, err := subCheckRun(context.Background(), subs, deps,
		subCheckSchedule{Concurrency: 1, DelayPerItem: time.Second}, time.Now()); err != nil {
		t.Fatalf("subCheckRun: %v", err)
	}
	want := []string{"list:mid:1", "wait:1s", "list:mid:2"}
	if strings.Join(*events, ",") != strings.Join(want, ",") {
		t.Errorf("事件流 = %v, want %v", *events, want)
	}
}

// TestSubCheckNoDelayByDefault 钉住「默认 0 = 与现状逐字一致」：没有任何停顿事件，
// 事件顺序也只由 list/download 组成。
func TestSubCheckNoDelayByDefault(t *testing.T) {
	setSubStoreRoot(t, t.TempDir())
	origFlag := optSubCheckDelayPerItem
	optSubCheckDelayPerItem = 0
	t.Cleanup(func() { optSubCheckDelayPerItem = origFlag })

	events := withFakeSleep(t)
	subs := []substore.Subscription{{Target: "mid:1", Name: "a"}, {Target: "mid:2", Name: "b"}}
	deps := eventDeps(t, events, map[string][]string{"mid:1": {"101"}, "mid:2": {"102"}})
	if _, err := subCheckRun(context.Background(), subs, deps, subCheckSchedule{Concurrency: 1}, time.Now()); err != nil {
		t.Fatalf("subCheckRun: %v", err)
	}
	for _, e := range *events {
		if strings.HasPrefix(e, "wait:") {
			t.Errorf("默认（0）不该有任何停顿：%v", *events)
		}
	}
	want := []string{"list:mid:1", "download:101", "list:mid:2", "download:102"}
	if strings.Join(*events, ",") != strings.Join(want, ",") {
		t.Errorf("事件流 = %v, want %v", *events, want)
	}
}

// TestSubCheckDelayFromFlag 钉住 flag→schedule 的桥接：只设 --delay-per-item=3 也能生效。
func TestSubCheckDelayFromFlag(t *testing.T) {
	setSubStoreRoot(t, t.TempDir())
	origFlag := optSubCheckDelayPerItem
	optSubCheckDelayPerItem = 3
	t.Cleanup(func() { optSubCheckDelayPerItem = origFlag })

	events := withFakeSleep(t)
	subs := []substore.Subscription{{Target: "mid:1", Name: "a"}}
	deps := eventDeps(t, events, map[string][]string{"mid:1": {"101", "102"}})
	if _, err := subCheckRun(context.Background(), subs, deps, subCheckSchedule{Concurrency: 1}, time.Now()); err != nil {
		t.Fatalf("subCheckRun: %v", err)
	}
	want := []string{"list:mid:1", "wait:3s", "download:101", "wait:3s", "download:102"}
	if strings.Join(*events, ",") != strings.Join(want, ",") {
		t.Errorf("事件流 = %v, want %v", *events, want)
	}
}

// TestSubCheckDelayCancelDuringWait 钉住取消语义：停顿期间 ctx 被取消 → 错误原样上抛
// （context.Canceled），既不能吞掉，也不能接着去下载。
func TestSubCheckDelayCancelDuringWait(t *testing.T) {
	setSubStoreRoot(t, t.TempDir())
	origFlag := optSubCheckDelayPerItem
	optSubCheckDelayPerItem = 0
	t.Cleanup(func() { optSubCheckDelayPerItem = origFlag })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	origSleep := subCheckSleep
	subCheckSleep = func(c context.Context, _ time.Duration) error {
		cancel() // 在第一次停顿期间取消
		return c.Err()
	}
	t.Cleanup(func() { subCheckSleep = origSleep })

	var downloaded bool
	subs := []substore.Subscription{{Target: "mid:1", Name: "a"}}
	deps := subCheckDeps{
		resolve: func(_ context.Context, target string) (string, error) { return target, nil },
		fetch: func(_ context.Context, resolved string) (*entity.VInfo, error) {
			return &entity.VInfo{Title: resolved, PagesInfo: []entity.Page{{Aid: "101", PubTime: time.Now().Unix()}}}, nil
		},
		download: func(context.Context, string) error { downloaded = true; return nil },
	}
	_, err := subCheckRun(ctx, subs, deps, subCheckSchedule{Concurrency: 1, DelayPerItem: time.Second}, time.Now())
	if !errors.Is(err, context.Canceled) {
		t.Errorf("停顿期间取消应当原样上抛 context.Canceled，实际 %v", err)
	}
	if downloaded {
		t.Error("取消之后不该继续下载")
	}
}
