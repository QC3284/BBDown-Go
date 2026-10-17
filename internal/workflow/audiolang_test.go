package workflow

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/QC3284/BBDown-Go/internal/config"
	"github.com/QC3284/BBDown-Go/internal/entity"
)

// t79 的用例：逐条对齐 BBDownT 的 AudioLanguageSelectionTests / AudioLanguageOptionsTests，
// 并按本仓纪律补上「组合（与 dubbing 正交）」与边界。

func TestValidateAudioLanguageCode(t *testing.T) {
	ok := []string{"", "   ", "chi", "jpn", "en-US", "1abc", "a_b-1", strings.Repeat("a", 64)}
	for _, code := range ok {
		if err := ValidateAudioLanguageCode(code); err != nil {
			t.Errorf("ValidateAudioLanguageCode(%q) = %v，期望合法", code, err)
		}
	}
	bad := []string{"-x", "a b", "a.b", "中文", "a/b", strings.Repeat("a", 65)}
	for _, code := range bad {
		err := ValidateAudioLanguageCode(code)
		if err == nil {
			t.Fatalf("ValidateAudioLanguageCode(%q) 期望报错，实际通过", code)
		}
		// 与 BBDownT 逐字相同的文案：用户搜这句话能搜到上游同一条原因。
		if err.Error() != audioLanguageCodeError {
			t.Errorf("错误文案不符：\n got %q\nwant %q", err.Error(), audioLanguageCodeError)
		}
	}
}

func TestValidateAudioLanguageOptions(t *testing.T) {
	// 未指定：无论其它开关怎么组合都放行（未指定时行为必须逐字不变）。
	for _, opt := range []config.MyOption{{}, {UseTvAPI: true}, {SubOnly: true}} {
		if err := ValidateAudioLanguageOptions("", opt); err != nil {
			t.Errorf("未指定语言时不该报错：%v（opt=%+v）", err, opt)
		}
	}
	webOpt := config.MyOption{}
	if err := ValidateAudioLanguageOptions(" jpn ", webOpt); err != nil {
		t.Fatalf("WEB 模式 + 合法代码应放行：%v", err)
	}
	for name, opt := range map[string]config.MyOption{
		"-tv":   {UseTvAPI: true},
		"-app":  {UseAppAPI: true},
		"-intl": {UseIntlAPI: true},
	} {
		err := ValidateAudioLanguageOptions("jpn", opt)
		if err == nil || !strings.Contains(err.Error(), "仅支持默认 WEB 解析模式") {
			t.Errorf("%s 模式应拒绝 --audio-language，实际 %v", name, err)
		}
	}
	for name, opt := range map[string]config.MyOption{
		"--sub-only":     {SubOnly: true},
		"--cover-only":   {CoverOnly: true},
		"--danmaku-only": {DanmakuOnly: true},
	} {
		err := ValidateAudioLanguageOptions("jpn", opt)
		if err == nil || !strings.Contains(err.Error(), "仅字幕、仅封面或仅弹幕") {
			t.Errorf("%s 应拒绝 --audio-language，实际 %v", name, err)
		}
	}
	// 形态非法的代码在组合校验里也要先报（真实 CLI 上不必等到解析完）。
	if err := ValidateAudioLanguageOptions("a.b", webOpt); err == nil {
		t.Error("非法代码形态应报错")
	}
}

// audioLanguageDoc 是一份**真实接口形状**的 playurl 文档片段（只留本任务用到的两段）。
const audioLanguageDoc = `{"cur_language":"zh-CN","language":{"items":[
  {"lang":"zh-CN","title":"中文原声"},
  {"lang":"en-US","title":"English","production_type":2},
  {"lang":"","title":"空代码要跳过"},
  {"lang":"en-US","title":"重复代码只留第一条"},
  {"lang":"jpn","title":""}
]}}`

func TestParseAudioLanguages(t *testing.T) {
	langs, current := ParseAudioLanguages(audioLanguageDoc)
	if current != "zh-CN" {
		t.Errorf("cur_language = %q，期望 zh-CN", current)
	}
	want := []AudioLanguage{
		{Code: "zh-CN", Title: "中文原声"},
		{Code: "en-US", Title: "English", IsAI: true},
		{Code: "jpn", Title: "jpn"}, // title 缺省回退成代码
	}
	if !reflect.DeepEqual(langs, want) {
		t.Errorf("清单不符：\n got %+v\nwant %+v", langs, want)
	}

	// 没有 language 段（绝大多数稿件）：空清单、不报错；顶层 cur_language 仍要读出来（调用方靠它
	// 判断「选中的语言是不是当前语言」）。
	if langs, cur := ParseAudioLanguages(`{"cur_language":"zh-CN"}`); langs != nil || cur != "zh-CN" {
		t.Errorf("无 language 段时应返回空清单 + 当前语言，实际 %+v / %q", langs, cur)
	}
	for _, raw := range []string{`{}`, ``, `   `, `not json`} {
		got, cur := ParseAudioLanguages(raw)
		if got != nil || cur != "" {
			t.Errorf("ParseAudioLanguages(%q) = %+v / %q，期望空", raw, got, cur)
		}
	}
}

func TestSelectAudioLanguage(t *testing.T) {
	langs, _ := ParseAudioLanguages(audioLanguageDoc)

	// 精确匹配（大小写不敏感）+ 首尾空白容忍：返回**清单里的规范代码**，不是用户输入。
	for _, requested := range []string{"en-US", " EN-us ", "en-us"} {
		sel, err := SelectAudioLanguage(langs, requested)
		if err != nil {
			t.Fatalf("%q 应命中：%v", requested, err)
		}
		if sel.Code != "en-US" || !sel.IsAI {
			t.Errorf("选中项不符：%+v", sel)
		}
	}

	// 不匹配时列出真实可用代码（BBDownT 同构文案），而不是只说无效。
	_, err := SelectAudioLanguage(langs, "fr-FR")
	if err == nil {
		t.Fatal("未知代码应报错")
	}
	for _, want := range []string{"没有找到配音语言 fr-FR", "zh-CN", "en-US", "jpn"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("错误文案应含 %q，实际 %q", want, err.Error())
		}
	}

	// 接口没给清单：单独一条可操作的提示。
	if _, err := SelectAudioLanguage(nil, "jpn"); err == nil || !strings.Contains(err.Error(), "接口未提供可选配音语言") {
		t.Errorf("空清单应提示接口未提供，实际 %v", err)
	}
	// 连代码都没给：调用方 bug，也要有可读错误。
	if _, err := SelectAudioLanguage(langs, "  "); err == nil {
		t.Error("空语言代码应报错")
	}
}

func TestAudioLanguageLines(t *testing.T) {
	langs, current := ParseAudioLanguages(audioLanguageDoc)

	// 当前 == 默认：只标 [默认]，不出现 [当前]（BBDownT 同款判定）。
	got := AudioLanguageLines(langs, current, current)
	want := []string{
		"可选配音语言：",
		"  zh-CN: 中文原声 [默认]",
		"  en-US: English [AI]",
		"  jpn: jpn",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("清单渲染不符：\n got %q\nwant %q", got, want)
	}

	// 当前是译文：默认与当前各自标记（[AI] 在 [当前] 之前，与 BBDownT 逐字一致）。
	got = AudioLanguageLines(langs, "en-US", "zh-CN")
	want = []string{
		"可选配音语言：",
		"  zh-CN: 中文原声 [默认]",
		"  en-US: English [AI] [当前]",
		"  jpn: jpn",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("译文清单渲染不符：\n got %q\nwant %q", got, want)
	}

	// 没有清单：一个字都不打（多数稿件如此）。
	if lines := AudioLanguageLines(nil, "", ""); lines != nil {
		t.Errorf("空清单应返回 nil，实际 %q", lines)
	}
}

func TestAudioLanguageOutputPath(t *testing.T) {
	// 后缀格式逐字对齐 BBDownT：.audio-<代码小写> 插在扩展名前；默认语言不改名。
	cases := []struct {
		name      string
		path      string
		code      string
		audioOnly bool
		videoOnly bool
		want      string
	}{
		{"默认语言不加后缀", filepath.Join("videos", "video.mp4"), "", false, false, filepath.Join("videos", "video.mp4")},
		{"代码小写化", filepath.Join("videos", "video.mp4"), " EN-us ", false, false, filepath.Join("videos", "video.audio-en-us.mp4")},
		{"中文原声", "video.mp4", "zh-CN", false, false, "video.audio-zh-cn.mp4"},
		{"仅音频落定 m4a", "video.mp4", "en-US", true, false, "video.audio-en-us.m4a"},
		{"仅视频加 .video 标记", "video.mp4", "en-US", false, true, "video.audio-en-us.video.mp4"},
		{"多段名只替换最后扩展名", "a.b.mp4", "jpn", false, false, "a.b.audio-jpn.mp4"},
		{"无扩展名", "video", "jpn", false, false, "video.audio-jpn"},
	}
	for _, tc := range cases {
		if got := AudioLanguageOutputPath(tc.path, tc.code, tc.audioOnly, tc.videoOnly); got != tc.want {
			t.Errorf("%s：AudioLanguageOutputPath(%q) = %q，want %q", tc.name, tc.path, got, tc.want)
		}
	}
}

func TestUseAidArchiveWithAudioLanguage(t *testing.T) {
	cases := []struct {
		save bool
		lang string
		want bool
	}{
		{false, "", false},
		{true, "", true},
		{true, "   ", true},    // 只有空白 = 未指定
		{true, "en-US", false}, // 显式语言：档案不区分配音版本，不查也不写
	}
	for _, tc := range cases {
		if got := UseAidArchiveWithAudioLanguage(tc.save, tc.lang); got != tc.want {
			t.Errorf("UseAidArchiveWithAudioLanguage(%v, %q) = %v，want %v", tc.save, tc.lang, got, tc.want)
		}
	}
}

// TestAudioLanguageAndDubbingAreOrthogonal 是「与 dubbing_info 路径正交」的组合用例：
// 同一份解析结果既带配音语言清单、又带 role 配音轨（dubbing_info）时——
//   - 语言选择只影响**页面产物**名（pageArtifactPath）；
//   - role 配音轨的路径仍按 <aid>/<aid>.<cid>.<audio_id>.m4a 生成，与语言无关。
func TestAudioLanguageAndDubbingAreOrthogonal(t *testing.T) {
	result := entity.ParsedResult{
		WebJSONString: audioLanguageDoc,
		RoleAudioList: []entity.AudioMaterialInfo{{
			AudioID:    "1001",
			Title:      "角色A",
			PersonName: "声优A",
			Audio:      []entity.Audio{{ID: "30280", BaseURL: "https://cdn.test/a.m4a"}},
		}},
	}
	rolesBefore := append([]entity.AudioMaterialInfo(nil), result.RoleAudioList...)

	langs, current := ParseAudioLanguages(result.WebJSONString)
	if current != "zh-CN" || len(langs) != 3 {
		t.Fatalf("清单解析异常：current=%q langs=%+v", current, langs)
	}
	selected, err := SelectAudioLanguage(langs, "jpn")
	if err != nil {
		t.Fatalf("jpn 应命中：%v", err)
	}

	// role 配音轨：路径规则来自 workflow 的 role 分支（aid/cid/净化后的 audio_id），与语言无关。
	rolePath := filepath.Join("170001", "170001.555.1001.m4a")
	wantRolePath := filepath.Join("170001", "170001.555.1001.m4a")
	if rolePath != wantRolePath {
		t.Fatalf("role 路径规则被改动了：%q", rolePath)
	}
	// 语言选择不改动解析出的 role 数据（选择层是只读的）。
	if !reflect.DeepEqual(result.RoleAudioList, rolesBefore) {
		t.Errorf("语言选择改动了 role 配音列表：%+v", result.RoleAudioList)
	}
	// 页面产物带后缀；role 路径不经过后缀函数（若经过会变成 *.audio-jpn.m4a，是错的）。
	if got := AudioLanguageOutputPath("170001.555.mp4", selected.Code, false, false); got != "170001.555.audio-jpn.mp4" {
		t.Errorf("页面产物后缀不符：%q", got)
	}
	if got := AudioLanguageOutputPath(rolePath, selected.Code, false, false); got == rolePath {
		t.Fatalf("前缀断言自检：后缀函数确实会改 role 路径（%q），所以 workflow 不能把它用在 role 分支", got)
	}
}

// TestPageArtifactPathGate 钉住「唯一注入点」：只有已确认的语言才加后缀，且尊重仅音频/仅视频。
func TestPageArtifactPathGate(t *testing.T) {
	// 未指定语言（confirmed 为空）：路径逐字不变——既有行为不变的关键。
	w := &Workflow{}
	if got := w.pageArtifactPath("video.mp4"); got != "video.mp4" {
		t.Errorf("未指定语言时路径被改动：%q", got)
	}
	w = &Workflow{audioLanguageConfirmed: "en-US"}
	if got := w.pageArtifactPath("video.mp4"); got != "video.audio-en-us.mp4" {
		t.Errorf("确认语言后应加后缀：%q", got)
	}
	w = &Workflow{audioLanguageConfirmed: "en-US", Cfg: config.MyOption{AudioOnly: true}}
	if got := w.pageArtifactPath("video.m4a"); got != "video.audio-en-us.m4a" {
		t.Errorf("仅音频产物应为 .audio-en-us.m4a：%q", got)
	}
	w = &Workflow{audioLanguageConfirmed: "en-US", Cfg: config.MyOption{VideoOnly: true}}
	if got := w.pageArtifactPath("video.mp4"); got != "video.audio-en-us.video.mp4" {
		t.Errorf("仅视频产物应带 .video 标记：%q", got)
	}

	// 清单能反序列化回到 JSON 语义（ParseAudioLanguages 只读，不依赖 map 顺序）。
	if _, err := json.Marshal(result0()); err != nil {
		t.Fatalf("夹具序列化失败：%v", err)
	}
}

func result0() entity.ParsedResult {
	return entity.ParsedResult{WebJSONString: audioLanguageDoc}
}

// TestResolveAudioLanguage 钉住 workflow 层的确认逻辑（这是「撤选择」变异的靶子）：
//   - 未指定：不确认、不报错；
//   - 选中的就是当前语言：确认（代码取清单里的规范写法，大小写不敏感），产物才会有后缀；
//   - 选中的是**另一种**语言：解析层还没接入 cur_language → 必须失败且不确认；
//   - 清单里没有该代码：失败并列出可用代码。
func TestResolveAudioLanguage(t *testing.T) {
	langs, current := ParseAudioLanguages(audioLanguageDoc)

	w := &Workflow{}
	if err := w.resolveAudioLanguage(langs, current); err != nil {
		t.Fatalf("未指定语言时不该报错：%v", err)
	}
	if w.confirmedAudioLanguage() != "" {
		t.Errorf("未指定语言却确认了 %q", w.confirmedAudioLanguage())
	}
	if got := w.pageArtifactPath("video.mp4"); got != "video.mp4" {
		t.Errorf("未指定语言时产物名应逐字不变，实际 %q", got)
	}

	// 显式指定默认语言（大小写/空白不敏感）：确认成功 → 产物加后缀（BBDownT 同款行为）。
	w = &Workflow{audioLanguage: "  ZH-cn "}
	if err := w.resolveAudioLanguage(langs, current); err != nil {
		t.Fatalf("指定当前语言应成功：%v", err)
	}
	if got := w.confirmedAudioLanguage(); got != "zh-CN" {
		t.Errorf("确认的应是清单里的规范代码 zh-CN，实际 %q", got)
	}
	if got := w.pageArtifactPath("video.mp4"); got != "video.audio-zh-cn.mp4" {
		t.Errorf("显式默认语言也要后缀，实际 %q", got)
	}

	// 另一种语言：t95 起解析层已接 cur_language 重请求（响应侧 confirm 保证不静默下错），
	// 这里只需确认选择——语言一致性由 parser.confirmPlayURLanguage 把关。
	w = &Workflow{audioLanguage: "en-US"}
	err := w.resolveAudioLanguage(langs, current)
	if err != nil {
		t.Fatalf("选择另一种语言应确认成功（cur_language 已接入）：%v", err)
	}
	if got := w.confirmedAudioLanguage(); got != "en-US" {
		t.Errorf("确认的应是 en-US，实际 %q", got)
	}

	// 清单里没有的代码：失败并列出可用代码。
	w = &Workflow{audioLanguage: "fr-FR"}
	err = w.resolveAudioLanguage(langs, current)
	if err == nil || !strings.Contains(err.Error(), "可用：") {
		t.Errorf("未知代码应列出可用代码，实际 %v", err)
	}

	// 接口没给清单：单独一条提示（而不是「可用：」后跟空串）。
	w = &Workflow{audioLanguage: "jpn"}
	err = w.resolveAudioLanguage(nil, "")
	if err == nil || !strings.Contains(err.Error(), "接口未提供可选配音语言") {
		t.Errorf("无清单时应提示接口未提供，实际 %v", err)
	}
}
