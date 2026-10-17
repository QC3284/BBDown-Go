package workflow

import (
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/QC3284/BBDown-Go/internal/config"
	"github.com/QC3284/BBDown-Go/internal/entity"
	"github.com/QC3284/BBDown-Go/internal/muxer"
	"github.com/QC3284/BBDown-Go/internal/util"
)

func TestParsePageSelection(t *testing.T) {
	cases := []struct {
		expr    string
		want    []string
		wantErr bool
	}{
		{"1,3,5", []string{"1", "3", "5"}, false},
		{"1-3", []string{"1", "2", "3"}, false},
		{"1-3,7,9-11", []string{"1", "2", "3", "7", "9", "10", "11"}, false},
		{"5", []string{"5"}, false},
		{"10-1", nil, true}, // start > end must abort (upstream)
		{"1-3-5", nil, true},
		{"abc", nil, true},
		{"-5", nil, true}, // invalid: clear error instead of upstream token quirk
		{"1,,2", []string{"1", "2"}, false},
	}
	for _, c := range cases {
		got, err := parsePageSelection(c.expr)
		if c.wantErr {
			if err == nil {
				t.Errorf("parsePageSelection(%q) expected error, got %v", c.expr, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("parsePageSelection(%q) unexpected error: %v", c.expr, err)
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("parsePageSelection(%q) = %v, want %v", c.expr, got, c.want)
		}
	}
}

func TestParsePageSelectionMaxExpanded(t *testing.T) {
	_, err := parsePageSelection("1-1000001")
	if err == nil {
		t.Fatal("expected expansion limit error")
	}
}

func TestParseEncodingPriority(t *testing.T) {
	m, first := parseEncodingPriority("hevc,avc,av1")
	if first != "HEVC" {
		t.Errorf("first = %q, want HEVC", first)
	}
	if m["HEVC"] != 0 || m["AVC"] != 1 || m["AV1"] != 2 {
		t.Errorf("map = %v", m)
	}
	// Earlier = higher priority: HEVC(0) < AVC(1).

	// Chinese comma, dashes removed (dots kept, matching upstream), dedup.
	m, first = parseEncodingPriority("H.264，hevc,-av1-,hevc")
	if first != "H.264" {
		t.Errorf("first = %q, want H.264", first)
	}
	if m["H.264"] != 0 || m["HEVC"] != 1 || m["AV1"] != 2 {
		t.Errorf("map = %v", m)
	}
}

func TestParseDfnPriority(t *testing.T) {
	m := parseDfnPriority("8K, 1080P 高码率，720P 高清")
	if m["8K"] != 0 || m["1080P 高码率"] != 1 || m["720P 高清"] != 2 {
		t.Errorf("map = %v", m)
	}
}

func TestParseDanmakuFormats(t *testing.T) {
	got, err := parseDanmakuFormats("")
	if err != nil || !reflect.DeepEqual(got, []string{"xml", "ass"}) {
		t.Errorf("default = %v, %v", got, err)
	}
	got, _ = parseDanmakuFormats("XML，ass")
	if !reflect.DeepEqual(got, []string{"xml", "ass"}) {
		t.Errorf("formats = %v", got)
	}
	got, _ = parseDanmakuFormats("bogus")
	if !reflect.DeepEqual(got, []string{"xml", "ass"}) {
		t.Errorf("invalid format should fall back to defaults, got %v", got)
	}
}

// ---- t66：分P选择的前导零规范化（吸收上游 c39cae4）+ 展示截断 ----

// TestParsePageSelectionNormalizesLeadingZeros 钉住 t66 的核心修复：
// 单 token 分支必须输出**规范形态**（去掉前导零），否则 "-p 01" 会被原样塞进选择列表，
// 而真实分P 是 "1"——字符串比对匹配不上，用户**静默少下**（不报错、不提示）。
// 范围与混合表达式本来就由 strconv.Itoa 产出，这里一并逐字钉住三条路径同口径。
//
// 变异验证：把单 token 分支改回 append(result, part) → 本用例红。
func TestParsePageSelectionNormalizesLeadingZeros(t *testing.T) {
	cases := []struct {
		expr string
		want []string
	}{
		{"01", []string{"1"}},
		{"001", []string{"1"}},
		{"007", []string{"7"}},
		{"01,2", []string{"1", "2"}},
		{"01-03", []string{"1", "2", "3"}},
		{"01-03,5", []string{"1", "2", "3", "5"}},
		{" 01 , 02 ", []string{"1", "2"}},
		{"1,03,1-2", []string{"1", "3", "1", "2"}},
	}
	for _, c := range cases {
		got, err := parsePageSelection(c.expr)
		if err != nil {
			t.Errorf("parsePageSelection(%q) 不该报错：%v", c.expr, err)
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("parsePageSelection(%q) = %v, want %v", c.expr, got, c.want)
		}
		for _, one := range got {
			if len(one) > 1 && one[0] == '0' {
				t.Errorf("选择项 %q 仍带前导零（会与真实分P 匹配不上）", one)
			}
		}
	}

	// 全零/负数等非法输入的错误语义不变。
	for _, bad := range []string{"0", "00", "-5", "abc"} {
		if _, err := parsePageSelection(bad); err == nil {
			t.Errorf("parsePageSelection(%q) 应当报错", bad)
		}
	}
}

// TestFilterPagesBySelectionMatchesNumerically 钉住三处选择来源的同一收口：
// -p（已规范）、URL 的 ?p=01、以及 VInfo 自带的 "01" 都要能匹配到 Index=1 那一页。
//
// 变异验证：把匹配改回字符串相等 → ?p=01 那一格红。
func TestFilterPagesBySelectionMatchesNumerically(t *testing.T) {
	pages := []entity.Page{{Index: 1}, {Index: 2}, {Index: 3}, {Index: 10}}
	cases := []struct {
		name string
		sel  []string
		want []int
	}{
		{"规范值", []string{"1", "3"}, []int{1, 3}},
		{"前导零（?p=01 / VInfo.Index）", []string{"01"}, []int{1}},
		{"混合前导零", []string{"02", "010"}, []int{2, 10}},
		{"首尾空白", []string{" 3 "}, []int{3}},
		{"不存在的页", []string{"9"}, nil},
		{"非数字项退回字符串比对", []string{"x"}, nil},
	}
	for _, c := range cases {
		got := filterPagesBySelection(pages, c.sel)
		var idx []int
		for _, p := range got {
			idx = append(idx, p.Index)
		}
		if len(idx) != len(c.want) {
			t.Errorf("%s：filterPagesBySelection(%v) = %v, want %v", c.name, c.sel, idx, c.want)
			continue
		}
		for i := range c.want {
			if idx[i] != c.want[i] {
				t.Errorf("%s：filterPagesBySelection(%v) = %v, want %v", c.name, c.sel, idx, c.want)
			}
		}
	}
	// 过滤结果保持原顺序（下载顺序 = 分P 顺序）。
	if got := filterPagesBySelection(pages, []string{"03", "01"}); len(got) != 2 || got[0].Index != 1 || got[1].Index != 3 {
		t.Errorf("过滤结果应当保持分P 原顺序：%+v", got)
	}
	// 空结果必须是空切片而不是 nil（调用方靠 len 判断，别引入 nil 语义差异）。
	if got := filterPagesBySelection(pages, []string{"99"}); got == nil {
		t.Error("空结果应当是空切片")
	}
}

// TestFormatSelectedPagesTruncatesLongSelections 钉住展示截断（上游同修）：
// 像 -p 1-60000 那样的大选择，日志/错误里只展示前 20 项 + 总数——否则一条几十 KB 的日志行
// 会把终端与 issue 一起刷爆。
//
// 变异验证：撤掉截断（直接 Join 全部）→ 本用例红。
func TestFormatSelectedPagesTruncatesLongSelections(t *testing.T) {
	if got := formatSelectedPages(nil); got != "ALL" {
		t.Errorf("nil（= ALL）应当显示 ALL，实际 %q", got)
	}
	if got := formatSelectedPages([]string{"1", "2"}); got != "1,2" {
		t.Errorf("少量选择应当原样列出，实际 %q", got)
	}
	// 恰好 20 项：不截断。
	exact := make([]string, maxShownPages)
	for i := range exact {
		exact[i] = strconv.Itoa(i + 1)
	}
	got := formatSelectedPages(exact)
	if strings.Contains(got, "共") || !strings.HasPrefix(got, "1,2,") {
		t.Errorf("恰好 %d 项不该截断：%q", maxShownPages, got)
	}
	// 21 项与 1000 项：前 20 项 + 总数。
	for _, n := range []int{maxShownPages + 1, 1000} {
		all := make([]string, n)
		for i := range all {
			all[i] = strconv.Itoa(i + 1)
		}
		got := formatSelectedPages(all)
		wantPrefix := all[:maxShownPages][maxShownPages-1] + "…"
		_ = wantPrefix
		if !strings.HasSuffix(got, fmt.Sprintf("…（共 %d 项）", n)) {
			t.Errorf("%d 项应当带总数后缀：%q", n, got)
		}
		if strings.Count(got, ",") != maxShownPages-1 {
			t.Errorf("%d 项应当只展示前 %d 项：%q", n, maxShownPages, got)
		}
		if want := strings.Join(all[:maxShownPages], ","); !strings.HasPrefix(got, want) {
			t.Errorf("%d 项的前缀应当是前 %d 项：%q", n, maxShownPages, got)
		}
	}
}

// TestGetSelectedPagesNormalizesLeadingZeros 端到端：-p 01 经 getSelectedPages 出来就是 ["1"]，
// 于是「已选择：1」与实际分P "1" 能对上（改前这里会静默少下）。
func TestGetSelectedPagesNormalizesLeadingZeros(t *testing.T) {
	cfg := &config.MyOption{SelectPage: "01"}
	vInfo := &entity.VInfo{PagesInfo: []entity.Page{{Index: 1}, {Index: 2}}}
	got, err := getSelectedPages(cfg, vInfo, "")
	if err != nil {
		t.Fatalf("getSelectedPages: %v", err)
	}
	if !reflect.DeepEqual(got, []string{"1"}) {
		t.Errorf("getSelectedPages(-p 01) = %v, want [1]", got)
	}
	// 没给选择、也没有自动选择来源时是 ALL（nil）。
	cfg2 := &config.MyOption{}
	got2, err := getSelectedPages(cfg2, &entity.VInfo{}, "")
	if err != nil || got2 != nil {
		t.Errorf("未指定分P 应当是 ALL(nil)，实际 %v err=%v", got2, err)
	}
}

// ---- t69：登录横幅按「模式 + 已加载 token」分流（吸收上游 1.7.5）----

// TestDecideLoginBannerMatrix 钉住组合矩阵：只有「已加载 token **且** 当前是 token 鉴权模式
// （TV/APP/INTL）」才改打中性说明；其余一律维持原横幅。
//
// 为什么必须分流：x/web-interface/nav 只验证 web cookie，而 TV/APP/INTL 凭 access_token 鉴权——
// 本地已有 TV token 的用户用 --use-tv-api 下载时会误报「未登录仅能试看 6 分钟」，实际 playurl
// 完全成功。
//
// 变异验证：让 decideLoginBanner 无视 token 模式（永远返回原横幅）→ 本用例红。
func TestDecideLoginBannerMatrix(t *testing.T) {
	token := config.MyOption{AccessToken: "tv-token-abc"}
	withToken := func(mod func(*config.MyOption)) config.MyOption {
		c := token
		mod(&c)
		return c
	}
	cases := []struct {
		name          string
		cfg           config.MyOption
		loggedIn      bool
		cookieExpired bool
		want          loginBannerKind
	}{
		{"已登录 → 不打任何横幅（TV 模式亦然）", config.MyOption{UseTvAPI: true}, true, false, loginBannerNone},
		{"无 token + WEB 未登录 → 原横幅", config.MyOption{}, false, false, loginBannerNotLoggedIn},
		{"无 token + WEB cookie 过期 → 原横幅", config.MyOption{}, false, true, loginBannerCookieExpired},
		{"无 token + TV 模式 → 原横幅（TV 没 token 也下不了完整视频）", config.MyOption{UseTvAPI: true}, false, false, loginBannerNotLoggedIn},
		{"有 token + TV → 中性说明", withToken(func(c *config.MyOption) { c.UseTvAPI = true }), false, false, loginBannerTokenMode},
		{"有 token + APP → 中性说明", withToken(func(c *config.MyOption) { c.UseAppAPI = true }), false, false, loginBannerTokenMode},
		{"有 token + INTL → 中性说明", withToken(func(c *config.MyOption) { c.UseIntlAPI = true }), false, false, loginBannerTokenMode},
		{"有 token + TV + web cookie 过期 → 仍然是中性说明", withToken(func(c *config.MyOption) { c.UseTvAPI = true }), false, true, loginBannerTokenMode},
		{"有 token 但纯 WEB 模式 → 原横幅（WEB 靠 cookie 鉴权）", token, false, false, loginBannerNotLoggedIn},
		{"有 token 但纯 WEB + cookie 过期 → 原横幅", token, false, true, loginBannerCookieExpired},
		{"token 只有空白 → 视为没有 token", config.MyOption{AccessToken: "   ", UseTvAPI: true}, false, false, loginBannerNotLoggedIn},
	}
	for _, c := range cases {
		if got := decideLoginBanner(c.cfg, c.loggedIn, c.cookieExpired); got != c.want {
			t.Errorf("%s：decideLoginBanner = %v, want %v（cfg=%+v）", c.name, got, c.want, c.cfg)
		}
	}
}

// TestLoginBannerLines 逐字钉住横幅文本：
//   - token 模式的中性说明必须说明「实际用的是哪个模式」，且**绝不能**出现
//     「未登录状态下仅能下载6分钟试看片段」（那正是误报）；
//   - 原横幅（未登录 / cookie 过期）逐字不变。
//
// 变异验证：撤掉分流后 token 模式会走原横幅 → TestDecideLoginBannerMatrix 与本用例同时红。
func TestLoginBannerLines(t *testing.T) {
	tokenLines := strings.Join(loginBannerLines(loginBannerTokenMode, "TV"), "\n")
	if tokenLines == "" {
		t.Fatal("token 模式应当有横幅文本")
	}
	if strings.Contains(tokenLines, "仅能下载6分钟试看片段") {
		t.Errorf("token 模式不能打「未登录仅能试看 6 分钟」：%q", tokenLines)
	}
	for _, want := range []string{"已加载 access_token", "TV 模式", "web cookie 只影响 WEB 模式", "可以忽略"} {
		if !strings.Contains(tokenLines, want) {
			t.Errorf("中性说明缺少 %q：%q", want, tokenLines)
		}
	}
	// 说明里的模式名跟着实际模式走。
	if app := strings.Join(loginBannerLines(loginBannerTokenMode, "APP"), "\n"); !strings.Contains(app, "APP 模式") {
		t.Errorf("中性说明应当带上实际模式名：%q", app)
	}

	notLogged := strings.Join(loginBannerLines(loginBannerNotLoggedIn, "WEB"), "\n")
	for _, want := range []string{"你尚未登录B站账号", "仅能下载6分钟试看片段", "BBDown login"} {
		if !strings.Contains(notLogged, want) {
			t.Errorf("原「未登录」横幅必须逐字保留 %q：%q", want, notLogged)
		}
	}
	expired := strings.Join(loginBannerLines(loginBannerCookieExpired, "WEB"), "\n")
	for _, want := range []string{"Cookie 已过期", "--use-tv-api 配合 --access-token"} {
		if !strings.Contains(expired, want) {
			t.Errorf("原「cookie 过期」横幅必须逐字保留 %q：%q", want, expired)
		}
	}
	if got := loginBannerLines(loginBannerNone, "WEB"); len(got) != 0 {
		t.Errorf("已登录时不该有任何横幅行：%v", got)
	}
}

// TestFilterAiSubsPolicyOverridesSkipAi 钉住 t83 F1 的接线语义：
// policy=only + --skip-ai=true → AI 字幕保留（policy 优先）；
// policy 未设 + --skip-ai=true → 照旧过滤（--skip-ai 单独使用行为不变）；
// 结束时恢复选择状态（不污染其它用例）。
// 变异验证：把 filterAiSubs 改回忽略 policy（恒用 skipAi 裸过滤）→ 本用例红。
func TestFilterAiSubsPolicyOverridesSkipAi(t *testing.T) {
	defer util.SetSubtitleSelection(nil, util.AISubtitleUnset)
	subs := []entity.Subtitle{{Lan: "ai-zh"}, {Lan: "zh-CN"}}

	util.SetSubtitleSelection(nil, util.AISubtitleOnly)
	got := filterAiSubs(subs, true, util.AISubtitleOnly)
	if len(got) != 2 {
		t.Fatalf("policy=only + skip-ai=true 应保留全部（policy 优先）：%+v", got)
	}

	util.SetSubtitleSelection(nil, util.AISubtitleUnset)
	got = filterAiSubs(subs, true, util.AISubtitleUnset)
	if len(got) != 1 || got[0].Lan != "zh-CN" {
		t.Fatalf("policy 未设 + skip-ai=true 应照旧过滤 AI：%+v", got)
	}

	got = filterAiSubs(subs, false, util.AISubtitleUnset)
	if len(got) != 2 {
		t.Fatalf("skip-ai=false 应全保留：%+v", got)
	}
}

// ---- t90（t82 评估 ⑤）：显式工具路径不存在时的告警 ----

// TestToolPathWarningText 逐字钉住告警文本：点名选项、路径与回退目标。
func TestToolPathWarningText(t *testing.T) {
	got := toolPathWarning("--ffmpeg-path", "/no/such/ffmpeg", "改为从 PATH 查找 ffmpeg")
	want := "--ffmpeg-path 指向的 \"/no/such/ffmpeg\" 不存在或不是文件，改为从 PATH 查找 ffmpeg（若这是笔误请修正）"
	if got != want {
		t.Errorf("告警文本 = %q", got)
		t.Errorf("，期望     %q", want)
	}
}

// TestFindBinariesWarnsOnMissingExplicitPaths 钉住行为：三个选项的显式路径不存在时都会告警
// （aria2c 还会在告警后报错——它没有「回退到 PATH」这条路）。
//
// 变异验证：撤掉 findBinaries 里的 toolPathWarn 调用 → 本用例红（收不到告警）。
func TestFindBinariesWarnsOnMissingExplicitPaths(t *testing.T) {
	orig := toolPathWarn
	var warns []string
	toolPathWarn = func(optName, path, fallback string) { warns = append(warns, optName+"|"+path) }
	t.Cleanup(func() { toolPathWarn = orig })

	cfg := config.DefaultMyOption()
	cfg.SkipMux = true // 只测告警本身，不触 PATH 查找（环境无关）
	cfg.FFmpegPath = "/no/such/ffmpeg"
	cfg.Mp4boxPath = "/no/such/MP4Box"
	w := New(cfg, nil)
	if err := w.findBinaries(); err != nil {
		t.Fatalf("SkipMux 时不该报错：%v", err)
	}
	if len(warns) != 2 {
		t.Fatalf("应当有两条告警（ffmpeg/mp4box），实际 %d：%v", len(warns), warns)
	}
	if warns[0] != "--ffmpeg-path|/no/such/ffmpeg" || warns[1] != "--mp4box-path|/no/such/MP4Box" {
		t.Errorf("告警没有点名选项/路径：%v", warns)
	}

	// aria2c：显式路径不存在 → 先告警、再报错（错误文案保持原样）。
	warns = nil
	cfg2 := config.DefaultMyOption()
	cfg2.SkipMux = true
	cfg2.UseAria2c = true
	cfg2.Aria2cPath = "/no/such/aria2c"
	err := New(cfg2, nil).findBinaries()
	if err == nil || !strings.Contains(err.Error(), "找不到可执行的aria2c文件") {
		t.Fatalf("aria2c 显式路径不存在必须报错（原文案）：%v", err)
	}
	if len(warns) != 1 || warns[0] != "--aria2c-path|/no/such/aria2c" {
		t.Errorf("aria2c 告警不合格：%v", warns)
	}
}

// TestFindBinariesNoWarnWhenPathExists 钉住另一半：路径真实存在时一条告警也不该有
// （既有行为不变——测试二进制本身就是个合法文件）。
func TestFindBinariesNoWarnWhenPathExists(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Skipf("拿不到测试二进制路径：%v", err)
	}
	orig := toolPathWarn
	var warns []string
	toolPathWarn = func(optName, path, fallback string) { warns = append(warns, optName+"|"+path) }
	t.Cleanup(func() { toolPathWarn = orig })

	cfg := config.DefaultMyOption()
	cfg.SkipMux = true
	cfg.FFmpegPath = exe
	cfg.Mp4boxPath = exe
	if err := New(cfg, nil).findBinaries(); err != nil {
		t.Fatalf("findBinaries: %v", err)
	}
	if len(warns) != 0 {
		t.Errorf("路径存在时不该有告警：%v", warns)
	}
	if muxer.FFMPEG != exe || muxer.MP4BOX != exe {
		t.Errorf("显式路径应当被采用：FFMPEG=%q MP4BOX=%q", muxer.FFMPEG, muxer.MP4BOX)
	}
}
