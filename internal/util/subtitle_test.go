package util

import (
	"strings"
	"testing"

	"github.com/QC3284/BBDown-Go/internal/entity"
)

func TestConvertSubFromJSON(t *testing.T) {
	jsonStr := `{"body":[{"from":1.5,"to":3.2,"content":"第一行"},{"from":4,"to":5.5,"content":"第二行"}]}`
	want := "1\n" + "00:00:01,500 --> 00:00:03,200\n" + "第一行\n\n" +
		"2\n" + "00:00:04,000 --> 00:00:05,500\n" + "第二行\n\n"
	if got := ConvertSubFromJSON(jsonStr); got != want {
		t.Errorf("ConvertSubFromJSON =\n%q\nwant\n%q", got, want)
	}
	// Invalid JSON returns empty string.
	if got := ConvertSubFromJSON("{invalid"); got != "" {
		t.Errorf("invalid json should produce empty, got %q", got)
	}
}

func TestFormatSubTime(t *testing.T) {
	cases := []struct {
		sec  float64
		want string
	}{
		{0, "00:00:00,000"},
		{61.25, "00:01:01,250"},
		// 对齐上游 SubUtil.FormatTime：毫秒来自 TimeSpan.Milliseconds（tick 级四舍五入后
		// 截断到毫秒），不是「小数部分 ×1000 截断」——后者在这类取值上会少 1ms。
		{3661.999, "01:01:01,999"},
		{-5, "00:00:00,000"},
	}
	for _, c := range cases {
		if got := FormatSubTime(c.sec); got != c.want {
			t.Errorf("FormatSubTime(%v) = %q, want %q", c.sec, got, c.want)
		}
	}
}

func TestSubCode2(t *testing.T) {
	cases := map[string]string{
		"zh":    "chi", // 新版字幕接口的短键
		"ai-zh": "chi", // 小写形态：normalizeSubtitleLangKey 负责补成 ai-Zh
		"zh-CN": "chi",
		"en-US": "eng",
		"ja":    "jpn",
		"ko":    "kor",
		"xx-YY": "und", // fallback (upstream Undetermined)
	}
	for k, want := range cases {
		if got := SubCode2(k); got != want {
			t.Errorf("SubCode2(%q) = %q, want %q", k, got, want)
		}
	}
}

func TestGetSubtitleCodeDisplayNames(t *testing.T) {
	cases := map[string][2]string{
		"zh-CN":   {"chi", "中文（简体）"},
		"zh-hans": {"chi", "中文（简体）"}, // normalized zh-hans => zh-Hans
		"en-US":   {"eng", "English(USA)"},
		"ja":      {"jpn", "日本語"},
		"ai-Zh":   {"chi", "中文（简体, AI识别）"},
	}
	for k, want := range cases {
		code, name := GetSubtitleCode(k)
		if code != want[0] || name != want[1] {
			t.Errorf("GetSubtitleCode(%q) = (%q, %q), want (%q, %q)", k, code, name, want[0], want[1])
		}
	}
}

func TestSanitizeSRT(t *testing.T) {
	cases := []struct{ in, want string }{
		{"a\r\nb\nc", "a\nb\nc"},
		{"  padded  \n", "  padded"},
		{"", ""},
	}
	for _, c := range cases {
		if got := SanitizeSRT(c.in); got != c.want {
			t.Errorf("SanitizeSRT(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// ---- t78：字幕筛选（--subtitle-language / --ai-subtitle-policy）----

// TestParseSubtitleLanguages 钉住解析与校验：逗号分隔、去空白、去重保序；非法标签报错并点名选项。
func TestParseSubtitleLanguages(t *testing.T) {
	got, err := ParseSubtitleLanguages(" zh , en ")
	if err != nil {
		t.Fatalf("合法输入不该报错：%v", err)
	}
	if len(got) != 2 || got[0] != "zh" || got[1] != "en" {
		t.Errorf("ParseSubtitleLanguages = %v, want [zh en]", got)
	}
	got, err = ParseSubtitleLanguages("zh,ZH,zh-Hans,ai-zh")
	if err != nil {
		t.Fatalf("合法输入不该报错：%v", err)
	}
	if len(got) != 3 {
		t.Errorf("重复项应当去重（zh 与 ZH 同义）：%v", got)
	}
	if got, err := ParseSubtitleLanguages(""); err != nil || got != nil {
		t.Errorf("空串应当是「不筛选」：got %v err %v", got, err)
	}
	for _, bad := range []string{"zh_CN", "中文", "z", "zh-", "zh--Hans"} {
		_, err := ParseSubtitleLanguages(bad)
		if err == nil {
			t.Errorf("ParseSubtitleLanguages(%q) 应当报错", bad)
		} else if !strings.Contains(err.Error(), "--subtitle-language") {
			t.Errorf("错误文案应当点名选项：%v", err)
		}
	}
	if _, err := ParseSubtitleLanguages(" , "); err == nil {
		t.Error("只有分隔符时应当报错（没有有效标签）")
	}
}

// TestParseAISubtitlePolicy 枚举校验：四种取值 + 空串；非法值报错并点名选项与可选值。
func TestParseAISubtitlePolicy(t *testing.T) {
	for _, ok := range []string{"exclude", "include", "prefer-human", "only", " EXCLUDE "} {
		if _, err := ParseAISubtitlePolicy(ok); err != nil {
			t.Errorf("ParseAISubtitlePolicy(%q) 不该报错：%v", ok, err)
		}
	}
	if p, err := ParseAISubtitlePolicy(""); err != nil || p != AISubtitleUnset {
		t.Errorf("空串应当是未指定：%v %v", p, err)
	}
	_, err := ParseAISubtitlePolicy("prefer")
	if err == nil {
		t.Fatal("非法策略应当报错")
	}
	for _, want := range []string{"--ai-subtitle-policy", "exclude", "prefer-human", "only"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("错误文案缺少 %q：%v", want, err)
		}
	}
}

// TestSubtitleLanguageFamilyAndMatch 是族/精确匹配的逐字表（acceptance ①）：
// 短标签按语言族（ai-zh 归 zh 族），带连字符的标签精确匹配。
func TestSubtitleLanguageFamilyAndMatch(t *testing.T) {
	families := map[string]string{
		"ai-zh": "zh", "zh": "zh", "zh-CN": "zh", "zh-Hant": "zh",
		"en-US": "en", "ai-En": "en", "": "",
	}
	for lan, want := range families {
		if got := SubtitleLanguageFamily(lan); got != want {
			t.Errorf("SubtitleLanguageFamily(%q) = %q, want %q", lan, got, want)
		}
	}

	cases := []struct {
		lan, want string
		ok        bool
	}{
		{"ai-zh", "zh", true},           // ai- 前缀归族
		{"zh-CN", "zh", true},           // 族匹配
		{"zh-Hant", "zh", true},         // 同族不同变体
		{"en-US", "en", true},           // 族匹配
		{"ai-En", "en", true},           // AI 的英文也归 en 族
		{"ja", "zh", false},             // 不同族
		{"zh-Hans", "zh-Hans", true},    // 带连字符 → 精确
		{"zh-Hans", "zh", true},         // 短标签仍然按族命中
		{"zh-Hant", "zh-Hans", false},   // 带连字符不再放宽到族
		{"ai-zh-Hans", "zh-Hans", true}, // AI 的对应变体精确命中
		{"zh", "zh-Hans", false},        // 精确匹配不放宽
		{"", "zh", false},               // 空语言不命中
		{"zh-CN", "", true},             // 空 want = 不筛
	}
	for _, c := range cases {
		if got := SubtitleLanguageMatches(c.lan, c.want); got != c.ok {
			t.Errorf("SubtitleLanguageMatches(%q, %q) = %v, want %v", c.lan, c.want, got, c.ok)
		}
	}
}

// subFixture 造一组字幕：中文人工、中文 AI、英文人工、英文 AI、日文人工、韩文只有 AI。
func subFixture() []entity.Subtitle {
	return []entity.Subtitle{
		{Lan: "zh-CN"}, {Lan: "ai-zh"},
		{Lan: "en-US"}, {Lan: "ai-En"},
		{Lan: "ja"}, {Lan: "ai-ko"},
	}
}

func lans(subs []entity.Subtitle) []string {
	out := make([]string, 0, len(subs))
	for _, s := range subs {
		out = append(out, s.Lan)
	}
	return out
}

func sameLans(got []entity.Subtitle, want ...string) bool {
	g := lans(got)
	if len(g) != len(want) {
		return false
	}
	for i := range want {
		if g[i] != want[i] {
			return false
		}
	}
	return true
}

// TestFilterSubtitlesPolicyMatrix 是四策略矩阵（acceptance ②）：exclude / include /
// prefer-human（同族有人工时弃 AI，但**只为 AI 的族照留**） / only；未指定与 include 同义。
//
// 变异验证：把 prefer-human 的「同族有人工」判据去掉（改成一律保留 AI）→ 本用例红；
// 把 exclude/only 的分支对调 → 也红。
func TestFilterSubtitlesPolicyMatrix(t *testing.T) {
	subs := subFixture()

	if got := FilterSubtitles(subs, nil, AISubtitleUnset); !sameLans(got, "zh-CN", "ai-zh", "en-US", "ai-En", "ja", "ai-ko") {
		t.Errorf("未指定策略应当全留：%v", lans(got))
	}
	if got := FilterSubtitles(subs, nil, AISubtitleInclude); !sameLans(got, "zh-CN", "ai-zh", "en-US", "ai-En", "ja", "ai-ko") {
		t.Errorf("include 应当全留：%v", lans(got))
	}
	if got := FilterSubtitles(subs, nil, AISubtitleExclude); !sameLans(got, "zh-CN", "en-US", "ja") {
		t.Errorf("exclude 应当只留人工：%v", lans(got))
	}
	if got := FilterSubtitles(subs, nil, AISubtitleOnly); !sameLans(got, "ai-zh", "ai-En", "ai-ko") {
		t.Errorf("only 应当只留 AI：%v", lans(got))
	}
	// prefer-human：zh / en 两族有人工 → 弃该族 AI；ko 族只有 AI → 保留（与 exclude 的区别就在这里）。
	if got := FilterSubtitles(subs, nil, AISubtitlePreferHuman); !sameLans(got, "zh-CN", "en-US", "ja", "ai-ko") {
		t.Errorf("prefer-human 应当只在有人工字幕的族里弃 AI：%v", lans(got))
	}

	// 语言筛选 + 策略组合：先筛语言，再套策略。
	if got := FilterSubtitles(subs, []string{"zh"}, AISubtitleExclude); !sameLans(got, "zh-CN") {
		t.Errorf("zh + exclude = %v, want [zh-CN]", lans(got))
	}
	if got := FilterSubtitles(subs, []string{"zh"}, AISubtitleOnly); !sameLans(got, "ai-zh") {
		t.Errorf("zh + only = %v, want [ai-zh]", lans(got))
	}
	if got := FilterSubtitles(subs, []string{"zh-Hans"}, AISubtitleUnset); len(got) != 0 {
		t.Errorf("zh-Hans 精确匹配不该命中 zh-CN：%v", lans(got))
	}
	// 保持原顺序（下载顺序 = 接口顺序）。
	if got := FilterSubtitles(subs, []string{"zh", "en"}, AISubtitleUnset); !sameLans(got, "zh-CN", "ai-zh", "en-US", "ai-En") {
		t.Errorf("筛选结果应当保持原顺序：%v", lans(got))
	}
	// 空清单不 panic。
	if got := FilterSubtitles(nil, []string{"zh"}, AISubtitlePreferHuman); len(got) != 0 {
		t.Errorf("空输入应当返回空：%v", lans(got))
	}
}

// TestFilterSubtitlesUnknownSourceDoesNotDisplaceHuman 钉住「未知来源不顶替已确认 CC」：
// 空语言条目不参与 prefer-human 的「有人工」判据。
func TestFilterSubtitlesUnknownSourceDoesNotDisplaceHuman(t *testing.T) {
	subs := []entity.Subtitle{{Lan: "ai-zh"}, {Lan: ""}}
	got := FilterSubtitles(subs, nil, AISubtitlePreferHuman)
	// "" 不算人工字幕 → zh 族没有人工 → ai-zh 保留；空语言条目本身也保留（不筛它）。
	if !sameLans(got, "ai-zh", "") {
		t.Errorf("未知来源不该把 AI 字幕顶掉：%v", lans(got))
	}
}

// TestEffectiveSkipAi 钉住优先级（acceptance ③）：显式 policy 优先，--skip-ai 单独使用不变。
func TestEffectiveSkipAi(t *testing.T) {
	cases := []struct {
		policy AISubtitlePolicy
		skipAi bool
		want   bool
	}{
		{AISubtitleUnset, true, true},   // 只给 --skip-ai：维持原语义
		{AISubtitleUnset, false, false}, // 都不给
		{AISubtitleOnly, true, false},   // 显式 policy 优先 → --skip-ai 不再生效
		{AISubtitleInclude, true, false},
		{AISubtitleExclude, true, false}, // exclude 自己就会滤掉 AI
		{AISubtitlePreferHuman, true, false},
	}
	for _, c := range cases {
		if got := EffectiveSkipAi(c.policy, c.skipAi); got != c.want {
			t.Errorf("EffectiveSkipAi(%q, %v) = %v, want %v", c.policy, c.skipAi, got, c.want)
		}
	}
}

// TestApplySubtitleSelection 钉住 GetSubtitles 那一层的接线：未设置选择时原样返回（行为与改前
// 逐字一致），设置后按选择筛选。
func TestApplySubtitleSelection(t *testing.T) {
	origLangs, origPolicy := subtitleSelection()
	t.Cleanup(func() { SetSubtitleSelection(origLangs, origPolicy) })

	subs := subFixture()
	SetSubtitleSelection(nil, AISubtitleUnset)
	if got := applySubtitleSelection(subs); !sameLans(got, "zh-CN", "ai-zh", "en-US", "ai-En", "ja", "ai-ko") {
		t.Errorf("未设置选择时应当原样返回：%v", lans(got))
	}

	SetSubtitleSelection([]string{"zh"}, AISubtitleExclude)
	if got := applySubtitleSelection(subs); !sameLans(got, "zh-CN") {
		t.Errorf("设置选择后应当筛选：%v", lans(got))
	}
	// SetSubtitleSelection 拷贝入参：外部切片改动不影响内部状态。
	langs := []string{"en"}
	SetSubtitleSelection(langs, AISubtitleUnset)
	langs[0] = "ja"
	if got := applySubtitleSelection(subs); !sameLans(got, "en-US", "ai-En") {
		t.Errorf("选择状态不应被外部改动影响：%v", lans(got))
	}
}
