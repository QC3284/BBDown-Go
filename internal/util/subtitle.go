package util

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strings"
	"unicode"

	"github.com/QC3284/BBDown-Go/internal/entity"
)

// GetSubtitles fetches subtitles with multi-API fallback matching C#.
func GetSubtitles(ctx context.Context, client *HTTPClient, aid, cid, epid string, index int, intl bool, cookie string) ([]entity.Subtitle, error) {
	var subtitles []entity.Subtitle

	if intl {
		subtitles = getIntlSubAPI1(ctx, client, aid, cid, epid)
		if subtitles == nil {
			subtitles = getIntlSubAPI2(ctx, client, aid, cid, epid, index)
		}
	} else {
		// 新版接口（protobuf）优先：它是当前 web 端在用的那个（BBDownT 的 2.1.3 也换到了它）。
		// 2.15.3 起**不再要求 cookie**：t36 时代的门控理由「匿名拿不到」已被 t44 证伪——
		// 匿名也能拿到新接口条目（混淆 URL 经 XOR 解码后，CDN 匿名可下；qa 匿名实测
		// 405B 响应 → 解码 → curl 200/7578B）。失败（nil）时自然落老三条回退。
		subtitles = getSubWebAPI(ctx, client, aid, cid)
		if subtitles == nil && cookie == "" {
			subtitles = getSubAPI3(ctx, client, aid, cid)
		}
		if subtitles == nil && cookie != "" {
			subtitles = getSubAPI2(ctx, client, aid, cid)
			if subtitles == nil {
				subtitles = getSubAPI1(ctx, client, aid, cid)
			}
			if subtitles == nil {
				subtitles = getSubAPI3(ctx, client, aid, cid)
			}
		}
	}
	if subtitles == nil {
		return nil, nil
	}
	for i := range subtitles {
		if strings.HasPrefix(subtitles[i].URL, "//") {
			subtitles[i].URL = "https:" + subtitles[i].URL
		}
	}
	// t78：统一在这里应用 --subtitle-language / --ai-subtitle-policy（两者都未设时原样返回，
	// 行为与改前逐字一致）。放在 GetSubtitles 里而不是调用方，是为了让三条解析路径
	// （新接口 / 老三条 / INTL）都吃到同一层筛选。
	return applySubtitleSelection(subtitles), nil
}

func getSubAPI1(ctx context.Context, client *HTTPClient, aid, cid string) []entity.Subtitle {
	resp, err := client.GetWebSource(ctx, fmt.Sprintf("https://api.bilibili.com/x/web-interface/view?aid=%s&cid=%s", aid, cid))
	if err != nil {
		return nil
	}
	var r struct {
		Data struct {
			Subtitle struct {
				List []struct {
					Lan         string `json:"lan"`
					SubtitleURL string `json:"subtitle_url"`
				} `json:"list"`
			} `json:"subtitle"`
		} `json:"data"`
	}
	if json.Unmarshal([]byte(resp), &r) != nil {
		return nil
	}
	var subs []entity.Subtitle
	for _, s := range r.Data.Subtitle.List {
		if s.SubtitleURL == "" {
			continue
		}
		subs = append(subs, entity.Subtitle{
			Lan:  s.Lan,
			URL:  s.SubtitleURL,
			Path: fmt.Sprintf("%s/%s.%s.%s.srt", aid, aid, cid, SanitizePathSegment(s.Lan)),
		})
	}
	return subs
}

func getSubAPI2(ctx context.Context, client *HTTPClient, aid, cid string) []entity.Subtitle {
	resp, err := client.GetWebSource(ctx, fmt.Sprintf("https://api.bilibili.com/x/player/wbi/v2?cid=%s&aid=%s", cid, aid))
	if err != nil {
		return nil
	}
	var r struct {
		Data struct {
			Subtitle struct {
				Subtitles []struct {
					Lan         string `json:"lan"`
					SubtitleURL string `json:"subtitle_url"`
				} `json:"subtitles"`
			} `json:"subtitle"`
		} `json:"data"`
	}
	if json.Unmarshal([]byte(resp), &r) != nil {
		return nil
	}
	var subs []entity.Subtitle
	for _, s := range r.Data.Subtitle.Subtitles {
		if s.SubtitleURL == "" {
			continue
		}
		subs = append(subs, entity.Subtitle{
			Lan:  s.Lan,
			URL:  s.SubtitleURL,
			Path: fmt.Sprintf("%s/%s.%s.%s.srt", aid, aid, cid, SanitizePathSegment(s.Lan)),
		})
	}
	return subs
}

func getSubAPI3(ctx context.Context, client *HTTPClient, aid, cid string) []entity.Subtitle {
	resp, err := client.GetWebSource(ctx, fmt.Sprintf("https://api.bilibili.com/x/player/v2?cid=%s&aid=%s", cid, aid))
	if err != nil {
		return nil
	}
	var r struct {
		Data struct {
			Subtitle struct {
				Subtitles []struct {
					Lan         string `json:"lan"`
					SubtitleURL string `json:"subtitle_url"`
				} `json:"subtitles"`
			} `json:"subtitle"`
		} `json:"data"`
	}
	if json.Unmarshal([]byte(resp), &r) != nil {
		return nil
	}
	var subs []entity.Subtitle
	for _, s := range r.Data.Subtitle.Subtitles {
		if s.SubtitleURL == "" {
			continue
		}
		subs = append(subs, entity.Subtitle{
			Lan:  s.Lan,
			URL:  s.SubtitleURL,
			Path: fmt.Sprintf("%s/%s.%s.%s.srt", aid, aid, cid, SanitizePathSegment(s.Lan)),
		})
	}
	return subs
}

func getIntlSubAPI1(ctx context.Context, client *HTTPClient, aid, cid, epid string) []entity.Subtitle {
	resp, err := client.GetWebSource(ctx, fmt.Sprintf("https://api.biliintl.com/intl/gateway/web/v2/subtitle?episode_id=%s", epid))
	if err != nil {
		return nil
	}
	var r struct {
		Data struct {
			Subtitles []struct {
				LangKey string `json:"lang_key"`
				URL     string `json:"url"`
			} `json:"subtitles"`
		} `json:"data"`
	}
	if json.Unmarshal([]byte(resp), &r) != nil {
		return nil
	}
	var subs []entity.Subtitle
	for _, s := range r.Data.Subtitles {
		if s.URL == "" {
			continue
		}
		ext := ".ass"
		if strings.Contains(s.URL, ".json") {
			ext = ".srt"
		}
		subs = append(subs, entity.Subtitle{
			Lan:  s.LangKey,
			URL:  s.URL,
			Path: fmt.Sprintf("%s/%s.%s.%s%s", aid, aid, cid, SanitizePathSegment(s.LangKey), ext),
		})
	}
	return subs
}

func getIntlSubAPI2(ctx context.Context, client *HTTPClient, aid, cid, epid string, index int) []entity.Subtitle {
	resp, err := client.GetWebSource(ctx, fmt.Sprintf("https://api.bilibili.tv/intl/gateway/v2/ogv/view/app/season?ep_id=%s&platform=android&s_locale=zh_SG", epid))
	if err != nil {
		return nil
	}
	var r struct {
		Result struct {
			Modules []struct {
				Data struct {
					Episodes []struct {
						Subtitles []struct {
							Key string `json:"key"`
							URL string `json:"url"`
						} `json:"subtitles"`
					} `json:"episodes"`
				} `json:"data"`
			} `json:"modules"`
		} `json:"result"`
	}
	if json.Unmarshal([]byte(resp), &r) != nil || len(r.Result.Modules) == 0 || len(r.Result.Modules[0].Data.Episodes) == 0 {
		return nil
	}
	eps := r.Result.Modules[0].Data.Episodes
	if index < 1 || index > len(eps) {
		return nil
	}
	var subs []entity.Subtitle
	for _, s := range eps[index-1].Subtitles {
		if s.URL == "" {
			continue
		}
		u := strings.ReplaceAll(s.URL, "\\/", "/")
		ext := ".ass"
		if strings.Contains(u, ".json") {
			ext = ".srt"
		}
		subs = append(subs, entity.Subtitle{
			Lan:  s.Key,
			URL:  u,
			Path: fmt.Sprintf("%s/%s.%s.%s%s", aid, aid, cid, SanitizePathSegment(s.Key), ext),
		})
	}
	return subs
}

// SaveSubtitle downloads and saves a subtitle, converting JSON to SRT if needed.
func SaveSubtitle(client *HTTPClient, url, path string) error {
	body, err := client.GetWebSource(context.Background(), url)
	if err != nil {
		return err
	}
	if strings.HasSuffix(path, ".srt") {
		body = ConvertSubFromJSON(body)
	}
	return os.WriteFile(path, []byte(body), 0644)
}

// ConvertSubFromJSON converts Bilibili JSON subtitle format to SRT.
func ConvertSubFromJSON(jsonStr string) string {
	var data struct {
		Body []struct {
			From    float64 `json:"from"`
			To      float64 `json:"to"`
			Content string  `json:"content"`
		} `json:"body"`
	}
	if err := json.Unmarshal([]byte(jsonStr), &data); err != nil {
		return ""
	}
	var sb strings.Builder
	for i, line := range data.Body {
		sb.WriteString(fmt.Sprintf("%d\n", i+1))
		sb.WriteString(fmt.Sprintf("%s --> %s\n", FormatSubTime(line.From), FormatSubTime(line.To)))
		sb.WriteString(SanitizeSRT(line.Content) + "\n\n")
	}
	return sb.String()
}

// FormatSubTime formats seconds to an SRT timestamp (上游 SubUtil.FormatTime 的同一契约)：
//
//   - NaN 与负数一律归 0（-1 秒曾被格式化成 00:00:01，符号被静默丢弃）；
//   - 毫秒按四舍五入取整，不是截断：1.001 秒的差值 0.000999… 会被截成 0ms，
//     与上游 TimeSpan.FromSeconds 的取整结果差 1ms；
//   - 小时数可以超过 24（SRT 允许；用 hh 会丢掉整天数，超长视频的字幕整体跳回开头）；
//   - 极大值先夹到 TimeSpan.MaxValue 量级，避免 double→int64 溢出让整个格式转换出错。
func FormatSubTime(sec float64) string {
	if math.IsNaN(sec) || sec < 0 {
		sec = 0
	}
	// 上游先夹到 TimeSpan.MaxValue 量级再格式化；这里取略低一点的同量级常数，
	// 保证下面的 tick 换算不越过 int64 上限。
	const maxSeconds = 922337203685.477
	if sec > maxSeconds {
		sec = maxSeconds
	}
	// TimeSpan.FromSeconds 按 tick 四舍五入，而 Milliseconds 属性再截断到毫秒——
	// 直接对毫秒四舍五入会在 1.001s 这类取值上多 1ms（0.000999… 进位）。
	ticks := int64(sec*1e7 + 0.5)
	totalMS := ticks / 10000
	h := totalMS / 3600000
	m := (totalMS % 3600000) / 60000
	s := (totalMS % 60000) / 1000
	ms := totalMS % 1000
	return fmt.Sprintf("%02d:%02d:%02d,%03d", h, m, s, ms)
}

// SanitizeSRT cleans content for valid SRT format.
func SanitizeSRT(content string) string {
	content = strings.ReplaceAll(content, "\r\n", "\n")
	content = strings.ReplaceAll(content, "\r", "\n")
	lines := strings.Split(content, "\n")
	var kept []string
	for _, l := range lines {
		// 上游用 TrimEnd()（Unicode 空白，含全角空格），不是只裁 ASCII 空格/制表符。
		l = strings.TrimRightFunc(l, unicode.IsSpace)
		if len(l) == 0 {
			continue
		}
		// A "-->" inside the cue text is read as a timing separator and shifts
		// every following cue (upstream SanitizeSrtContent).
		if strings.Contains(l, "-->") {
			l = strings.ReplaceAll(l, "-->", "->")
		}
		kept = append(kept, l)
	}
	return strings.Join(kept, "\n")
}

// GetSubtitleCode / SubCode2 now live in sublang.go (full upstream table).
