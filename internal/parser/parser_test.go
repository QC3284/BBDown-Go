package parser

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QC3284/BBDown-Go/internal/config"
	"github.com/QC3284/BBDown-Go/internal/util"
)

// TestParseModeName 钉住解析模式的展示名（t69）：登录横幅要据此说明「实际用的是哪个模式」，
// 名字错了用户会以为自己在用别的方式解析。
//
// 优先级 INTL > APP > TV：三者理论上互斥（handleConflictingOptions 会归一），同时出现时
// 取「最外层」的那个——INTL 走的是 biliintl 接口，APP 走 gRPC，TV 走 tv 接口。
func TestParseModeName(t *testing.T) {
	cases := []struct {
		name          string
		tv, app, intl bool
		want          string
	}{
		{"默认 WEB", false, false, false, "WEB"},
		{"TV", true, false, false, "TV"},
		{"APP", false, true, false, "APP"},
		{"INTL", false, false, true, "INTL"},
		{"INTL 优先于 APP/TV", true, true, true, "INTL"},
		{"APP 优先于 TV", true, true, false, "APP"},
	}
	for _, c := range cases {
		if got := ParseModeName(c.tv, c.app, c.intl); got != c.want {
			t.Errorf("%s：ParseModeName(%v,%v,%v) = %q, want %q", c.name, c.tv, c.app, c.intl, got, c.want)
		}
	}
}

// ---- t95：--audio-language 换轨重请求（cur_language）----

// curLangDoc 往夹具文档里注入顶层 cur_language（模拟「服务端按请求切到了该语言」）。
func curLangDoc(t *testing.T, fixture, curLang string) string {
	t.Helper()
	doc := fixtureBody(t, fixture)
	if curLang == "" {
		return doc
	}
	return strings.Replace(doc, "{", "{\"cur_language\":\""+curLang+"\",", 1)
}

// startCurLangServer 起一个记录请求 URL 的假 playurl 服务器，返回带 cur_language 的 DASH 文档。
func startCurLangServer(t *testing.T, curLang string) (*httptest.Server, *[]string) {
	t.Helper()
	var urls []string
	doc := curLangDoc(t, "dash-reparse-pass2", curLang)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		urls = append(urls, r.URL.String())
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(doc))
	}))
	t.Cleanup(srv.Close)
	return srv, &urls
}

func curLangParser(t *testing.T, srv *httptest.Server) *Parser {
	t.Helper()
	cfg := config.DefaultAppSettings()
	cfg.Host = strings.TrimPrefix(srv.URL, "https://")
	cfg.TvHost = cfg.Host
	cfg.Wbi = "test_wbi_key"
	client := util.NewHTTPClient(func() bool { return true }, func() string { return "" }, nil)
	return NewParser(client, cfg)
}

// TestPlayURLLanguageParsing 钉住 cur_language 的读取：顶层 / result / data 三种根形态都能读到，
// 没有就返回 ok=false（绝大多数稿件没有语言段）。
func TestPlayURLLanguageParsing(t *testing.T) {
	cases := []struct {
		doc   string
		want  string
		found bool
	}{
		{"{\"cur_language\":\"zh-CN\"}", "zh-CN", true},
		{"{\"result\":{\"cur_language\":\"en-US\"}}", "en-US", true},
		{"{\"data\":{\"cur_language\":\"ja\"}}", "ja", true},
		{"{\"result\":{}}", "", false},
		{"not json", "", false},
		{"{\"cur_language\":\"\"}", "", false},
	}
	for _, c := range cases {
		got, ok := playURLanguage(c.doc)
		if got != c.want || ok != c.found {
			t.Errorf("playURLanguage(%s) = (%q, %v), want (%q, %v)", c.doc, got, ok, c.want, c.found)
		}
	}
}

// TestConfirmPlayURLanguage 是响应确认的两臂（t95 ②）：一致放行；不一致给可读错误（点名两个代码）；
// 未请求 / 响应没声明都放行（无可核对）。
func TestConfirmPlayURLanguage(t *testing.T) {
	if err := confirmPlayURLanguage("{\"cur_language\":\"en-US\"}", ""); err != nil {
		t.Errorf("未请求语言时不该报错：%v", err)
	}
	if err := confirmPlayURLanguage("{\"cur_language\":\"en-US\"}", "EN-us"); err != nil {
		t.Errorf("一致（大小写不敏感）应当放行：%v", err)
	}
	if err := confirmPlayURLanguage("{\"result\":{}}", "zh-CN"); err != nil {
		t.Errorf("响应没声明语言时应当放行（无可核对）：%v", err)
	}
	err := confirmPlayURLanguage("{\"cur_language\":\"en-US\"}", "zh-CN")
	if err == nil {
		t.Fatal("语言不一致必须报错")
	}
	for _, want := range []string{"en-US", "zh-CN", "不一致"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("错误文案缺少 %q：%v", want, err)
		}
	}
}

// TestExtractTracksSendsCurLanguage 是 acceptance ① 的核心：显式指定语言时，playurl 请求
// 必须带 cur_language，且响应确认通过。
//
// 变异验证：撤掉 getPlayJSON 里拼 cur_language 的那一段 → 本用例红（请求里没有该参数）。
func TestExtractTracksSendsCurLanguage(t *testing.T) {
	orig := requestAudioLanguage
	SetRequestAudioLanguage("en-US")
	t.Cleanup(func() { requestAudioLanguage = orig })

	srv, urls := startCurLangServer(t, "en-US")
	p := curLangParser(t, srv)
	if _, err := p.ExtractTracks(context.Background(), "1", "1", "2", "", false, false, false, "", false, ""); err != nil {
		t.Fatalf("ExtractTracks: %v", err)
	}
	if len(*urls) == 0 {
		t.Fatal("没有发出 playurl 请求")
	}
	if !strings.Contains((*urls)[0], "cur_language=en-US") {
		t.Errorf("playurl 请求缺少 cur_language=en-US：%s", (*urls)[0])
	}
}

// TestExtractTracksRejectsCurLanguageMismatch：请求 zh-CN、服务端返回 en-US → 必须报可读错误，
// 绝不能把默认音轨当所请求语言下下去（这是 t79「诚实失败门」在解析层的替代）。
//
// 变异验证：撤掉 confirmPlayURLanguage 调用 → 本用例红。
func TestExtractTracksRejectsCurLanguageMismatch(t *testing.T) {
	orig := requestAudioLanguage
	SetRequestAudioLanguage("zh-CN")
	t.Cleanup(func() { requestAudioLanguage = orig })

	srv, _ := startCurLangServer(t, "en-US")
	p := curLangParser(t, srv)
	_, err := p.ExtractTracks(context.Background(), "1", "1", "2", "", false, false, false, "", false, "")
	if err == nil {
		t.Fatal("服务端没切到请求的语言时必须失败")
	}
	if !strings.Contains(err.Error(), "en-US") || !strings.Contains(err.Error(), "zh-CN") {
		t.Errorf("错误应当点名两个语言代码：%v", err)
	}
}

// TestExtractTracksNoCurLanguageWhenUnset 钉住「未指定 = 请求形态与改前逐字一致」：
// 不带 cur_language 参数。
func TestExtractTracksNoCurLanguageWhenUnset(t *testing.T) {
	orig := requestAudioLanguage
	requestAudioLanguage = ""
	t.Cleanup(func() { requestAudioLanguage = orig })

	srv, urls := startCurLangServer(t, "")
	p := curLangParser(t, srv)
	if _, err := p.ExtractTracks(context.Background(), "1", "1", "2", "", false, false, false, "", false, ""); err != nil {
		t.Fatalf("ExtractTracks: %v", err)
	}
	for _, u := range *urls {
		if strings.Contains(u, "cur_language") {
			t.Errorf("未指定语言时不该带 cur_language：%s", u)
		}
	}
}
