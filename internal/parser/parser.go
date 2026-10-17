package parser

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/QC3284/BBDown-Go/internal/appapi"
	"github.com/QC3284/BBDown-Go/internal/config"
	"github.com/QC3284/BBDown-Go/internal/entity"
	"github.com/QC3284/BBDown-Go/internal/util"
)

// isVipRestricted reports whether a playurl response is the 大会员专享限制 business
// error. The JSON root "message" is authoritative; a raw substring check only
// backs up non-JSON bodies, so a wording or encoding change cannot silently
// disable the web-page fallback (upstream IsVipRestrictedResponse).
// ParseModeName 返回当前解析模式的展示名（t69，吸收上游 1.7.5）：
// TV / APP / INTL / WEB。登录横幅要据此说明「实际用的是哪个模式」——TV/APP/INTL 凭
// access_token 鉴权，与 web cookie 无关，不能因为 web 未登录就报「未登录只能试看」。
func ParseModeName(tvAPI, appAPI, intlAPI bool) string {
	switch {
	case intlAPI:
		return "INTL"
	case appAPI:
		return "APP"
	case tvAPI:
		return "TV"
	default:
		return "WEB"
	}
}

func isVipRestricted(jsonStr string) bool {
	var doc map[string]interface{}
	if util.UnmarshalJSON(jsonStr, &doc) == nil {
		if msg, ok := doc["message"].(string); ok && msg != "" {
			return strings.Contains(msg, "大会员专享限制")
		}
	}
	return strings.Contains(jsonStr, "大会员专享限制")
}

// throwIfPlayLimited 对应上游 Parser.ThrowIfPlayLimited：result.play_check 里给了
// limit_play_reason / play_detail 就说明接口判定不可播放，按原因给出可读文案
// （只说「播放受限」会让用户无法判断是区域、付费、大会员还是未到时间）。
func throwIfPlayLimited(root map[string]interface{}) error {
	resultData, ok := root["result"].(map[string]interface{})
	if !ok {
		return nil
	}
	playCheck, ok := resultData["play_check"].(map[string]interface{})
	if !ok {
		return nil
	}
	reason, _ := playCheck["limit_play_reason"].(string)
	detail, _ := playCheck["play_detail"].(string)
	if strings.TrimSpace(reason) == "" && strings.TrimSpace(detail) == "" {
		return nil
	}
	var message string
	switch reason {
	case "AREA_LIMIT":
		message = "当前番剧/视频存在区域限制，接口返回不可播放"
	case "PAY_LIMIT":
		message = "当前番剧/视频存在付费限制，接口返回不可播放"
	case "VIP_LIMIT":
		message = "当前番剧/视频需要大会员权限，接口返回不可播放"
	case "TIME_LOCK":
		message = "当前番剧/视频尚未到可播放时间，接口返回不可播放"
	default:
		message = "当前番剧/视频存在播放限制，接口返回不可播放"
	}
	return fmt.Errorf("%s (limit_play_reason=%s, play_detail=%s)", message, reason, detail)
}

// throwIfBizError 对应上游 Parser.ThrowIfBizError：只在根是对象、且 code 是**JSON 数字**
// 且非 0 时才报错——字符串形态的 code（如 "-412"）按上游语义不算业务错误。
func throwIfBizError(root map[string]interface{}) error {
	code, ok := root["code"].(float64)
	if !ok || code == 0 {
		return nil
	}
	msg, _ := root["message"].(string)
	if msg == "" {
		msg = fmt.Sprintf("接口返回错误码 %d", int64(code))
	}
	return fmt.Errorf("接口返回错误: %s (code=%d)", msg, int64(code))
}

// ErrRiskControlVoucher 是「播放接口回的是风控人机验证凭证」的哨兵错误。
//
// 上游 1.7.3（BBDownT）在 fetch 层识别 data.v_voucher 并自己转 3 次 User-Agent 后**照样把
// 响应交给解析**（于是静默零轨道）；本仓把它扩到三种落点并**抛错**，让既有的页面级重试
// （workflow 的 pageRetryLimit=3，退避由 --retry-delay/--retry-count 控制）接手，
// 同时在文案里给出可执行的处置建议。
var ErrRiskControlVoucher = errors.New("触发风控人机验证（v_voucher）")

// voucherErrorMessage 是给用户看的完整文案：现象 + 三类处置建议（缺任一条都会让用户只剩
// 「任务完成但没有产物」这一种反馈）。
const voucherErrorMessage = "播放接口返回的是人机验证凭证(v_voucher)而不是播放地址" +
	"（HTTP 200 / code=0，但既无 dash 也无 durl，属于被风控判定为机器人）。" +
	"处置建议：等几分钟再试；更换出口 IP（换网络或代理）；或换账号/设备后再试" +
	"（本页会自动重试，重试次数与退避由 --retry-count/--retry-delay 控制；UA 轮换仅对 HTTP 412 生效）"

// voucherError 每次返回一个新的错误实例（保留 errors.Is 语义，文案带处置建议）。
func voucherError() error {
	return fmt.Errorf("%w：%s", ErrRiskControlVoucher, voucherErrorMessage)
}

// hasVoucherField 报告节点里有没有**非空字符串**的 v_voucher（空串/非字符串不算——
// 与上游 IsRiskControlVoucherResponse 的判定一致）。
func hasVoucherField(node map[string]interface{}) bool {
	v, ok := node["v_voucher"].(string)
	return ok && strings.TrimSpace(v) != ""
}

// throwIfVoucher 统一识别 v_voucher 风控响应，三种落点都认：
//   - 顶层（UGC 的 playurl 有时把凭证放根上）；
//   - data.v_voucher（上游 1.7.3 只认这一处）；
//   - result.v_voucher（番剧 pgc 走 result），以及叠加形态 data.result.v_voucher。
func throwIfVoucher(root map[string]interface{}) error {
	if hasVoucherField(root) {
		return voucherError()
	}
	if data, ok := root["data"].(map[string]interface{}); ok {
		if hasVoucherField(data) {
			return voucherError()
		}
		if nested, ok := data["result"].(map[string]interface{}); ok && hasVoucherField(nested) {
			return voucherError()
		}
	}
	if result, ok := root["result"].(map[string]interface{}); ok && hasVoucherField(result) {
		return voucherError()
	}
	return nil
}

// shouldExtractDubbing 是上游对 dubbing_info 的门控：只有 APP API + 番剧（ep/cheese）
// 场景的响应里才有这两类轨道，其它路径里出现同名节点不能当数据用。
func shouldExtractDubbing(appAPI bool, aidOri string) bool {
	return appAPI && (strings.HasPrefix(aidOri, "ep:") || strings.HasPrefix(aidOri, "cheese:"))
}

// extractDubbingInfo 读取 data.dubbing_info 下的背景音频与配音（上游 Parser.cs 的同一段，
// 仅在 APP API + 番剧场景下出现）。此前本仓完全没有这段解析：工作流虽然写了背景音轨/配音
// 的下载循环，但两个列表永远为空，等于死代码。
func extractDubbingInfo(data map[string]interface{}, result *entity.ParsedResult, aid, cid string, pDur int) {
	dub, ok := data["dubbing_info"].(map[string]interface{})
	if !ok {
		return
	}
	bgArr, hasBg := dub["background_audio"].([]interface{})
	roleArr, hasRole := dub["role_audio_list"].([]interface{})
	if !hasBg || !hasRole {
		return
	}

	toAudio := func(node map[string]interface{}) entity.Audio {
		audioID := getString(node, "id")
		finalURL, backups := urlCandidates(getString(node, "base_url"), node["backup_url"])
		return entity.Audio{
			ID:         audioID,
			Dfn:        audioID,
			Dur:        pDur,
			Bandwidth:  getInt64(node, "bandwidth") / 1000,
			BaseURL:    finalURL,
			BackupURLs: backups,
			Codecs:     getString(node, "codecs"),
		}
	}

	for _, n := range bgArr {
		node, ok := n.(map[string]interface{})
		if !ok {
			continue
		}
		result.BackgroundAudioTracks = append(result.BackgroundAudioTracks, toAudio(node))
	}

	for _, r := range roleArr {
		role, ok := r.(map[string]interface{})
		if !ok {
			continue
		}
		material := entity.AudioMaterialInfo{
			AudioID:    getString(role, "audio_id"),
			Title:      getString(role, "title"),
			PersonName: getString(role, "person_name"),
		}
		if arr, ok := role["audio"].([]interface{}); ok {
			for _, n := range arr {
				node, ok := n.(map[string]interface{})
				if !ok {
					continue
				}
				material.Audio = append(material.Audio, toAudio(node))
			}
		}
		if len(material.Audio) == 0 {
			continue
		}
		// 产物路径与上游同构：<aid>/<aid>.<cid>.<净化后的 audio_id>.m4a
		seg := util.SanitizePathSegment(material.AudioID)
		if seg == "" {
			seg = util.SanitizePathSegment(material.Audio[0].ID)
		}
		if seg == "" {
			seg = "audio"
		}
		material.Path = path.Join(aid, fmt.Sprintf("%s.%s.%s.m4a", aid, cid, seg))
		result.RoleAudioList = append(result.RoleAudioList, material)
	}
}

// apiBase returns the scheme-qualified base URL for an API host. A host that
// already carries a scheme is used as-is (upstream WithApiScheme), which is what
// lets --host point at a plain-http mirror or a local test server.
func apiBase(host string) string {
	h := strings.TrimRight(strings.TrimSpace(host), "/")
	if strings.HasPrefix(h, "http://") || strings.HasPrefix(h, "https://") {
		return h
	}
	if h == "" {
		return "https://api.bilibili.com"
	}
	return "https://" + h
}

// parseKidFromDrmURI extracts the key id from a bilidrm:// URI. Only a bare
// 32-hex value is accepted: a malformed URI carrying a host, path or query must
// not be mistaken for a key id (the upstream drm-dash-badkid fixture expects no kid).
func parseKidFromDrmURI(uri string) string {
	rest := uri
	if i := strings.Index(rest, "://"); i >= 0 {
		rest = rest[i+3:]
	}
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
		rest = rest[:i]
	}
	if len(rest) != 32 {
		return ""
	}
	for i := 0; i < len(rest); i++ {
		c := rest[i]
		if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F') {
			continue
		}
		return ""
	}
	return rest
}

// baseURLRegex matches a direct host URL carrying an explicit port. It must be
// anchored: the previous `http.*:\d+` also matched a ':port' appearing inside a
// query string, which picked the wrong base_url (upstream tightened it to
// ^https?://[^/:]+:\d+).
var baseURLRegex = regexp.MustCompile(`^https?://[^/:]+:\d+`)
var playerJSONRegex = regexp.MustCompile(`window\.__playinfo__=([\s\S]*?)<\/script>`)

// Parser handles video stream URL extraction.
type Parser struct {
	HTTPClient *util.HTTPClient
	Cfg        config.AppSettings
}

// NewParser creates a new Parser.
func NewParser(client *util.HTTPClient, cfg config.AppSettings) *Parser {
	return &Parser{HTTPClient: client, Cfg: cfg}
}

// WbiSign generates a Wbi-signed API url.
func (p *Parser) WbiSign(api string) string {
	h := md5.Sum([]byte(api + p.Cfg.Wbi))
	return api + "&w_rid=" + hex.EncodeToString(h[:])
}

// VideoCodec converts codec ID to name.
func VideoCodec(code string) string {
	switch code {
	case "13":
		return "AV1"
	case "12":
		return "HEVC"
	case "7":
		return "AVC"
	default:
		return "UNKNOWN"
	}
}

func timeStamp() string {
	// Corrected by the observed server clock offset: a drifted local clock makes
	// the signed request look stale and the API rejects it.
	return strconv.FormatInt(util.UnixNow(), 10)
}

// ExtractTracks parses video/audio track information from Bilibili playurl API.
func (p *Parser) ExtractTracks(ctx context.Context, aidOri, aid, cid, epid string, tvAPI, intlAPI, appAPI bool, encoding string, wantDrm bool, qn string) (*entity.ParsedResult, error) {
	if qn == "" {
		qn = "0"
	}

	result := &entity.ParsedResult{}

	jsonStr, err := p.fetchPlayJSON(ctx, encoding, aidOri, aid, cid, epid, tvAPI, intlAPI, &appAPI, wantDrm, qn, requestAudioLanguage)
	if err != nil {
		return nil, err
	}
	result.WebJSONString = jsonStr

	// Handle intl API (two-pass: code=0 and code=1)
	if intlAPI {
		return p.parseIntlStreams(ctx, result, aid, cid, epid, qn)
	}

	return p.parseDomesticStreams(ctx, result, aidOri, aid, cid, epid, tvAPI, appAPI, encoding, wantDrm)
}

// maxQn is the highest quality id the API accepts (upstream GetMaxQn).
// maxQn 是 web playurl 请求的最高档：**仍是 127（8K 超高清）**。
//
// 129（HDR Vivid）是 **APP 协议**的档位，web 接口未必受理——2.2.0 曾把它一起提到 129，导致真实解析
// 直接失败（错误还被批量输入那层吞掉了，见 §4.42）。BBDownT 也只改 APP 的 MaximumQuality。
const maxQn = "127"

// requestAudioLanguage 是 --audio-language 的**请求值**（t95）：空串 = 与改前逐字一致的请求形态
// （不带 cur_language 参数）。非空时 playurl 请求带 cur_language=<code>，并核对响应里的 cur_language 是否真的切过去了。
//
// 为什么用包级变量而不是给 Parse 加参数：Parse 的签名被 parser/fetcher/workflow 三处调用，加参数会波及 t95 inScope
// 之外的包。编排层接线只需一行 parser.SetRequestAudioLanguage(code)（t79 的收尾任务负责把 --audio-language 传进来，
// 并删掉 workflow 侧那条「解析层尚未接入」的失败门）。
var requestAudioLanguage string

// SetRequestAudioLanguage 写入本次解析要切换的配音语言代码（空串 = 不切换、请求形态不变）。
func SetRequestAudioLanguage(code string) {
	requestAudioLanguage = strings.TrimSpace(code)
}

// RequestAudioLanguage 返回当前请求的配音语言（用例/调试用）。
func RequestAudioLanguage() string { return requestAudioLanguage }

// confirmPlayURLanguage 核对响应里的实际配音语言（t95 ②）：请求带了 cur_language 时，响应若声明了
// cur_language 就必须与请求一致——不一致说明服务端没切过去，继续下载会把**默认音轨**挂上所选语言的
// 后缀（t79 最担心的事）。响应没有 cur_language（绝大多数稿件没有语言段）时视为无可核对。
func confirmPlayURLanguage(jsonStr, want string) error {
	if want == "" {
		return nil
	}
	cur, ok := playURLanguage(jsonStr)
	if !ok || cur == "" {
		return nil
	}
	if !strings.EqualFold(cur, want) {
		return fmt.Errorf("播放文档返回的配音语言是 %q，与请求的 %q 不一致：服务端没有切换到该语言，继续下载会把默认音轨存成所选语言（可用 -I 查看该稿件真实可用的配音语言）", cur, want)
	}
	return nil
}

// playURLanguage 从播放文档里读 cur_language（顶层或 result/data 下，兼容几种根形态）。
func playURLanguage(jsonStr string) (string, bool) {
	var doc map[string]interface{}
	if util.UnmarshalJSON(jsonStr, &doc) != nil {
		return "", false
	}
	roots := []map[string]interface{}{doc}
	if res, ok := doc["result"].(map[string]interface{}); ok {
		roots = append(roots, res)
	}
	if data, ok := doc["data"].(map[string]interface{}); ok {
		roots = append(roots, data)
	}
	for _, r := range roots {
		if v, ok := r["cur_language"].(string); ok && v != "" {
			return v, true
		}
	}
	return "", false
}

// fetchPlayJSON 取播放文档：非 INTL/APP 时优先请求最高清晰度（qn=127），只在它失败或
// 不带 dash.video 时回落调用方给的 qn（默认 "0"）。
//
// 这是本仓对上游两遍请求的合并（优化，见 docs/ROADMAP.md O2）：上游先请求 qn=0、再重发
// qn=127，而重发结果一旦带 dash.video 就**整份取代**前者——常见情况下第一份 qn=0 文档是
// 白发的，单次 -I 解析的 5 个 GET 里它占一个。落点与上游一致：要么用 qn127 文档、要么用
// qn0 文档，只是常见路径少一次请求。INTL/APP 不参与（上游同样不重发）。
//
// 取消必须原样向上传播（上游 RF-17）：两遍都失败时报回落那次的错误——旧流程在同样情形下
// 报的正是 qn=0 这次的错误。
func (p *Parser) fetchPlayJSON(ctx context.Context, encoding, aidOri, aid, cid, epid string, tvAPI, intlAPI bool, appAPI *bool, wantDrm bool, qn, curLanguage string) (string, error) {
	if intlAPI || *appAPI {
		doc, err := p.getPlayJSON(ctx, encoding, aidOri, aid, cid, epid, tvAPI, intlAPI, appAPI, wantDrm, qn, curLanguage)
		if err != nil {
			return "", err
		}
		if err := confirmPlayURLanguage(doc, curLanguage); err != nil {
			return "", err
		}
		return doc, nil
	}

	first, err := p.getPlayJSON(ctx, encoding, aidOri, aid, cid, epid, tvAPI, false, appAPI, wantDrm, maxQn, curLanguage)
	if err == nil && hasDashVideo(first) {
		if cerr := confirmPlayURLanguage(first, curLanguage); cerr != nil {
			return "", cerr
		}
		return first, nil
	}
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if err != nil {
		util.LogDebug("最高清晰度请求失败，回落 qn=%s: %v", qn, err)
	} else {
		util.LogDebug("最高清晰度请求未返回 DASH 轨道，回落 qn=%s", qn)
	}

	fallback, ferr := p.getPlayJSON(ctx, encoding, aidOri, aid, cid, epid, tvAPI, false, appAPI, wantDrm, qn, curLanguage)
	if ferr != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", ferr
	}
	if cerr := confirmPlayURLanguage(fallback, curLanguage); cerr != nil {
		return "", cerr
	}
	return fallback, nil
}

// hasDashVideo reports whether a playurl document offers a usable DASH video
// list, walking the same root variants the parser accepts (result.video_info,
// result, data, or the document itself).
func hasDashVideo(jsonStr string) bool {
	var doc map[string]interface{}
	if util.UnmarshalJSON(jsonStr, &doc) != nil {
		return false
	}
	roots := []map[string]interface{}{doc}
	if res, ok := doc["result"].(map[string]interface{}); ok {
		if vi, ok := res["video_info"].(map[string]interface{}); ok {
			roots = append([]map[string]interface{}{vi}, roots...)
		}
		roots = append([]map[string]interface{}{res}, roots...)
	}
	if data, ok := doc["data"].(map[string]interface{}); ok {
		roots = append([]map[string]interface{}{data}, roots...)
	}
	for _, r := range roots {
		dash, ok := r["dash"].(map[string]interface{})
		if !ok {
			continue
		}
		if videos, ok := dash["video"].([]interface{}); ok && len(videos) > 0 {
			return true
		}
	}
	return false
}

func (p *Parser) getPlayJSON(ctx context.Context, encoding, aidOri, aid, cid, epid string, tvAPI, intlAPI bool, appAPI *bool, wantDrm bool, qn, curLanguage string) (string, error) {
	if intlAPI {
		return p.getIntlPlayJSON(ctx, aid, cid, epid, qn, "0")
	}

	cheese := strings.HasPrefix(aidOri, "cheese:")
	bangumi := cheese || strings.HasPrefix(aidOri, "ep:")

	if *appAPI {
		// APP gRPC playurl (upstream AppHelper.DoReqAsync).
		jsonStr, err := appapi.DoReq(ctx, p.HTTPClient, aid, cid, epid, qn, bangumi, encoding, p.Cfg.Token)
		if err != nil {
			return "", err
		}
		util.LogDebug("APP API 响应已获取")
		return jsonStr, nil
	}

	var api string
	prefix := ""

	if tvAPI {
		base := apiBase(p.Cfg.TvHost)
		if bangumi {
			prefix = base + "/pgc/player/api/playurltv?"
		} else {
			prefix = base + "/x/tv/playurl?"
		}
	} else {
		base := apiBase(p.Cfg.Host)
		if bangumi {
			prefix = base + "/pgc/player/web/v2/playurl?"
		} else {
			// Cfg.Host, not a hardcoded api.bilibili.com: --host must apply to
			// the UGC playurl path too (upstream uses Config.Current.Host here).
			prefix = base + "/x/player/wbi/playurl?"
		}
	}

	if tvAPI {
		var parts []string
		if p.Cfg.Token != "" {
			parts = append(parts, "access_key="+p.Cfg.Token)
		}
		parts = append(parts, "appkey=4409e2ce8ffd12b8", "build=106500", "cid="+cid, "device=android")
		if bangumi {
			parts = append(parts, "ep_id="+epid, "expire=0")
		}
		parts = append(parts,
			"fnval=4048", "fnver=0", "fourk=1", "mid=0", "mobi_app=android_tv_yst",
			"object_id="+aid, "platform=android", "playurl_type=1", "qn="+qn, "ts="+timeStamp(),
		)
		params := strings.Join(parts, "&")
		sign := util.GetSign(params, false)
		api = prefix + params + "&sign=" + sign
	} else {
		var parts []string
		parts = append(parts, "support_multi_audio=true", "from_client=BROWSER", "avid="+aid, "cid="+cid, "fnval=4048", "fnver=0", "fourk=1")
		if p.Cfg.Area != "" {
			parts = append(parts, "access_key="+p.Cfg.Token, "area="+p.Cfg.Area)
		}
		parts = append(parts, "otype=json", "qn="+qn)
		if bangumi {
			parts = append(parts, "module=bangumi", "ep_id="+epid, "session=")
		}
		if p.Cfg.Cookie == "" && !wantDrm {
			parts = append(parts, "try_look=1")
		}
		if wantDrm {
			parts = append(parts, "drm_tech_type=2")
		}
		// t95：显式指定配音语言时带上 cur_language（空串不拼，请求形态与改前逐字一致）。
		if curLanguage != "" {
			parts = append(parts, "cur_language="+curLanguage)
		}
		parts = append(parts, "wts="+timeStamp())
		params := strings.Join(parts, "&")
		if bangumi {
			api = prefix + params
		} else {
			api = prefix + p.WbiSign(params)
		}

		if cheese {
			api = strings.Replace(api, "/pgc/player/web/v2/playurl", "/pugv/player/web/playurl", 1)
		}
	}

	resp, err := p.HTTPClient.GetWebSource(ctx, api)
	if err != nil {
		return "", err
	}

	// Fallback: if response indicates VIP-only content, try parsing from webpage source
	if isVipRestricted(resp) && epid != "" {
		util.Log("此视频需要大会员，您大概率需要登录一个有大会员的账号才可以下载，尝试从网页源码解析")
		// Follow the configured mirror: only the default api host falls back to the
		// official web host, otherwise a mirror user would be pulled back to a
		// domain that may be unreachable for them (upstream EpHost handling).
		webHost := p.Cfg.EpHost
		if webHost == "" || webHost == "api.bilibili.com" {
			webHost = "www.bilibili.com"
		}
		webURL := apiBase(webHost) + "/bangumi/play/ep" + epid
		webSource, err := p.HTTPClient.GetWebSource(ctx, webURL)
		if err != nil {
			return "", fmt.Errorf("大会员回退请求失败: %w", err)
		}
		match := playerJSONRegex.FindStringSubmatch(webSource)
		if match == nil || match[1] == "" {
			return "", fmt.Errorf("大会员回退失败：网页源码中未找到播放信息")
		}
		resp = match[1]
	}

	return resp, nil
}

func (p *Parser) getIntlPlayJSON(ctx context.Context, aid, cid, epid, qn, code string) (string, error) {
	isBiliPlus := p.Cfg.Host != "api.bilibili.com"
	host := p.Cfg.Host
	if !isBiliPlus {
		host = "api.biliintl.com"
	}

	var parts []string
	if p.Cfg.Token != "" {
		parts = append(parts, "access_key="+p.Cfg.Token)
	}
	parts = append(parts, "aid="+aid)
	if isBiliPlus {
		area := p.Cfg.Area
		if area == "" {
			area = "th"
		}
		parts = append(parts, "appkey=7d089525d3611b1c", "area="+area)
	}
	parts = append(parts, "cid="+cid, "ep_id="+epid, "platform=android", "prefer_code_type="+code, "qn="+qn)
	if isBiliPlus {
		parts = append(parts, "ts="+timeStamp())
	}
	parts = append(parts, "s_locale=zh_SG")
	params := strings.Join(parts, "&")
	api := apiBase(host) + "/intl/gateway/v2/ogv/playurl?"
	if isBiliPlus {
		api += params + "&sign=" + util.GetSign(params, true)
	} else {
		api += params
	}

	return p.HTTPClient.GetWebSource(ctx, api)
}

func (p *Parser) parseDomesticStreams(ctx context.Context, result *entity.ParsedResult, aidOri, aid, cid, epid string, tvAPI, appAPI bool, encoding string, wantDrm bool) (*entity.ParsedResult, error) {
	var root map[string]interface{}
	if err := util.UnmarshalJSON(result.WebJSONString, &root); err != nil {
		return nil, fmt.Errorf("parse playurl JSON: %w", err)
	}

	// 播放限制与业务错误码（上游在解析前统一兜底：play_check 只在 pgc 的 result 节点出现，
	// UGC 的限制通过顶层 code 表达）
	if err := throwIfPlayLimited(root); err != nil {
		return nil, err
	}
	if err := throwIfBizError(root); err != nil {
		return nil, err
	}
	// 风控新形态：HTTP 200 + code=0 + 只有 v_voucher（无 dash/durl）。必须在「零轨道」
	// 之前拦住，否则用户只看到「任务完成却没有产物」，也进不了页面级重试。
	if err := throwIfVoucher(root); err != nil {
		return nil, err
	}

	// Navigate to data node
	var data map[string]interface{}
	if resultData, ok := root["result"].(map[string]interface{}); ok {
		if vi, ok := resultData["video_info"].(map[string]interface{}); ok {
			data = vi
		} else {
			data = resultData
		}
	} else if d, ok := root["data"].(map[string]interface{}); ok {
		data = d
	} else {
		data = root
	}

	// Extract DRM metadata
	if isDrm, ok := data["is_drm"].(bool); ok {
		result.IsDrm = isDrm
	}
	if drmTech, ok := data["drm_tech_type"].(float64); ok {
		result.DrmTechType = int(drmTech)
	}
	if drmType, ok := data["drm_type"].(string); ok {
		result.DrmType = drmType
	}

	// DASH streams
	if dash, ok := data["dash"].(map[string]interface{}); ok {
		var pDur int
		if dur, ok := dash["duration"].(float64); ok {
			pDur = int(dur)
		} else if tl, ok := data["timelength"].(float64); ok {
			pDur = int(tl) / 1000
		}
		result.ActualDurationSec = pDur

		// 背景音频与配音只在 APP API + 番剧场景出现（上游同一门控）。
		// 缺了这段解析，工作流里的背景音轨/配音下载循环永远拿不到数据。
		if shouldExtractDubbing(appAPI, aidOri) {
			extractDubbingInfo(data, result, aid, cid, pDur)
		}

		// Parse video tracks
		if videos, ok := dash["video"].([]interface{}); ok {
			for _, v := range videos {
				vm, ok := v.(map[string]interface{})
				if !ok {
					continue
				}
				videoID := getString(vm, "id")
				baseURL := getString(vm, "base_url")
				if baseURL == "" {
					continue
				}
				// 首选之外仍保留其余候选（backup_url）：镜像漏对象/连不上时按序回退。
				finalURL, backups := urlCandidates(baseURL, vm["backup_url"])

				video := entity.Video{
					ID:         videoID,
					Dfn:        config.QualityMap[videoID],
					BaseURL:    finalURL,
					BackupURLs: backups,
					Codecs:     VideoCodec(getString(vm, "codecid")),
					Bandwidth:  getInt64(vm, "bandwidth") / 1000,
					Dur:        pDur,
					Size:       getFloat64(vm, "size"),
				}
				if video.Dfn == "" {
					video.Dfn = fmt.Sprintf("未知(%s)", videoID)
				}
				if !tvAPI && !appAPI {
					video.Res = fmt.Sprintf("%sx%s", getString(vm, "width"), getString(vm, "height"))
					video.FPS = getString(vm, "frame_rate")
				}
				result.VideoTracks = appendUniqueVideo(result.VideoTracks, video)
			}

			// Extract DRM info from first video
			if result.IsDrm && result.KidHex == "" && len(videos) > 0 {
				if firstV, ok := videos[0].(map[string]interface{}); ok {
					if drmURI, ok := firstV["bilidrm_uri"].(string); ok {
						if kid := parseKidFromDrmURI(drmURI); kid != "" {
							result.KidHex = kid
						}
					}
					if pssh, ok := firstV["widevine_pssh"].(string); ok && pssh != "" {
						result.PsshBase64 = pssh
					}
				}
			}
		}

		// Parse audio tracks
		if audios, ok := dash["audio"].([]interface{}); ok {
			for _, a := range audios {
				am, ok := a.(map[string]interface{})
				if !ok {
					continue
				}
				baseURL := getString(am, "base_url")
				if baseURL == "" {
					continue
				}
				finalURL, backups := urlCandidates(baseURL, am["backup_url"])
				audioID := getString(am, "id")
				codecs := getString(am, "codecs")
				switch codecs {
				case "mp4a.40.2", "mp4a.40.5":
					codecs = "M4A"
				case "ec-3":
					codecs = "E-AC-3"
				case "fLaC":
					codecs = "FLAC"
				}

				audio := entity.Audio{
					ID:         audioID,
					Dfn:        audioID,
					BaseURL:    finalURL,
					BackupURLs: backups,
					Codecs:     codecs,
					Bandwidth:  getInt64(am, "bandwidth") / 1000,
					Dur:        pDur,
				}
				result.AudioTracks = append(result.AudioTracks, audio)
			}
		}

		// Dolby audio
		if dolby, ok := dash["dolby"].(map[string]interface{}); ok && !tvAPI {
			if dbAudios, ok := dolby["audio"].([]interface{}); ok {
				for _, a := range dbAudios {
					am, _ := a.(map[string]interface{})
					if am == nil {
						continue
					}
					baseURL := getString(am, "base_url")
					if baseURL == "" {
						continue
					}
					finalURL, backups := urlCandidates(baseURL, am["backup_url"])
					audio := entity.Audio{
						ID:         getString(am, "id"),
						Dfn:        getString(am, "id"),
						BaseURL:    finalURL,
						BackupURLs: backups,
						Codecs:     "E-AC-3",
						Bandwidth:  getInt64(am, "bandwidth") / 1000,
						Dur:        pDur,
					}
					result.AudioTracks = append(result.AudioTracks, audio)
				}
			}
		}

		// Hi-Res FLAC
		if flac, ok := dash["flac"].(map[string]interface{}); ok && !tvAPI {
			if flacAudio, ok := flac["audio"].(map[string]interface{}); ok {
				baseURL := getString(flacAudio, "base_url")
				if baseURL != "" {
					finalURL, backups := urlCandidates(baseURL, flacAudio["backup_url"])
					audio := entity.Audio{
						ID:         getString(flacAudio, "id"),
						Dfn:        getString(flacAudio, "id"),
						BaseURL:    finalURL,
						BackupURLs: backups,
						Codecs:     "FLAC",
						Bandwidth:  getInt64(flacAudio, "bandwidth") / 1000,
						Dur:        pDur,
					}
					result.AudioTracks = append(result.AudioTracks, audio)
				}
			}
		}
		return result, nil
	}

	// FLV streams
	if durls, ok := data["durl"].([]interface{}); ok {
		quality := getString(data, "quality")
		videoCodecid := getString(data, "video_codecid")

		var size, length float64
		for _, d := range durls {
			dm, _ := d.(map[string]interface{})
			if dm == nil {
				continue
			}
			if url, ok := dm["url"].(string); ok {
				result.Clips = append(result.Clips, url)
			}
			size += getFloat64(dm, "size")
			length += getFloat64(dm, "length")
		}

		// Available qualities
		if qnExtras, ok := data["qn_extras"].([]interface{}); ok {
			for _, q := range qnExtras {
				if qs, ok := q.(map[string]interface{}); ok {
					if qn, ok := qs["qn"].(string); ok {
						result.Dfns = append(result.Dfns, qn)
					}
				}
			}
		} else if acceptQ, ok := data["accept_quality"].([]interface{}); ok {
			for _, q := range acceptQ {
				if qn, ok := q.(string); ok {
					result.Dfns = append(result.Dfns, qn)
				} else if qn, ok := q.(float64); ok {
					result.Dfns = append(result.Dfns, fmt.Sprintf("%.0f", qn))
				}
			}
		}

		result.ActualDurationSec = int(length) / 1000
		video := entity.Video{
			ID:     quality,
			Dfn:    config.QualityMap[quality],
			Codecs: VideoCodec(videoCodecid),
			Dur:    int(length) / 1000,
			Size:   size,
		}
		result.VideoTracks = appendUniqueVideo(result.VideoTracks, video)
	}

	return result, nil
}

func (p *Parser) parseIntlStreams(ctx context.Context, result *entity.ParsedResult, aid, cid, epid, qn string) (*entity.ParsedResult, error) {
	// 风控凭证：INTL 是两轮请求（prefer_code_type=0 再 1），两轮都可能被风控。
	// 策略与免二压 qn=127 同款——后一轮被风控而前一轮已有可用轨道时降级跳过该轮（打 Warn），
	// 两轮都没轨道才抛错（让页面级重试接手）。
	var voucherErr error
	for _, code := range []string{"0", "1"} {
		if code == "1" {
			var err error
			result.WebJSONString, err = p.getIntlPlayJSON(ctx, aid, cid, epid, qn, code)
			if err != nil {
				continue
			}
		}

		var root map[string]interface{}
		if err := util.UnmarshalJSON(result.WebJSONString, &root); err != nil {
			continue
		}

		if err := throwIfVoucher(root); err != nil {
			voucherErr = err
			if code == "1" && len(result.VideoTracks) > 0 {
				util.LogWarn("INTL 第二遍(prefer_code_type=1)触发风控人机验证：保留第一遍已解析出的 %d 条视频轨道继续（要完整轨道请稍后重试）", len(result.VideoTracks))
				break
			}
			continue
		}

		data, ok := root["data"].(map[string]interface{})
		if !ok {
			continue
		}
		videoInfo, ok := data["video_info"].(map[string]interface{})
		if !ok {
			continue
		}
		streamList, ok := videoInfo["stream_list"].([]interface{})
		if !ok {
			continue
		}

		pDur := int(getFloat64(videoInfo, "timelength") / 1000)
		var audioElements []map[string]interface{}
		if dashAudios, ok := videoInfo["dash_audio"].([]interface{}); ok {
			for _, a := range dashAudios {
				if am, ok := a.(map[string]interface{}); ok {
					audioElements = append(audioElements, am)
				}
			}
		}

		for _, stream := range streamList {
			sm, ok := stream.(map[string]interface{})
			if !ok {
				continue
			}
			dashVideo, ok := sm["dash_video"].(map[string]interface{})
			if !ok {
				continue
			}
			baseURL := getString(dashVideo, "base_url")
			if baseURL == "" {
				continue
			}

			streamInfo, ok := sm["stream_info"].(map[string]interface{})
			if !ok {
				continue
			}

			videoID := getString(streamInfo, "quality")
			finalURL, backups := urlCandidates(baseURL, dashVideo["backup_url"])

			v := entity.Video{
				ID:         videoID,
				Dfn:        config.QualityMap[videoID],
				BaseURL:    finalURL,
				BackupURLs: backups,
				Codecs:     VideoCodec(getString(dashVideo, "codecid")),
				Bandwidth:  getInt64(dashVideo, "bandwidth") / 1000,
				Dur:        pDur,
				Size:       getFloat64(dashVideo, "size"),
			}
			if v.Dfn == "" {
				v.Dfn = fmt.Sprintf("未知(%s)", videoID)
			}
			result.VideoTracks = appendUniqueVideo(result.VideoTracks, v)
		}

		for _, node := range audioElements {
			baseURL := getString(node, "base_url")
			if baseURL == "" {
				continue
			}
			finalURL, backups := urlCandidates(baseURL, node["backup_url"])

			audio := entity.Audio{
				ID:         getString(node, "id"),
				Dfn:        getString(node, "id"),
				BaseURL:    finalURL,
				BackupURLs: backups,
				Codecs:     "M4A",
				Bandwidth:  getInt64(node, "bandwidth") / 1000,
				Dur:        pDur,
			}
			result.AudioTracks = appendUniqueAudio(result.AudioTracks, audio)
		}
	}

	if len(result.VideoTracks) == 0 && voucherErr != nil {
		return nil, voucherErr
	}
	return result, nil
}

// ---- Helpers ----

func getString(m map[string]interface{}, key string) string {
	if v, ok := m[key]; ok {
		switch val := v.(type) {
		case string:
			return val
		case float64:
			if val == float64(int64(val)) {
				return strconv.FormatInt(int64(val), 10)
			}
			return strconv.FormatFloat(val, 'f', -1, 64)
		case json.Number:
			return string(val)
		}
	}
	return ""
}

func getInt64(m map[string]interface{}, key string) int64 {
	if v, ok := m[key]; ok {
		switch val := v.(type) {
		case float64:
			return int64(val)
		case json.Number:
			n, _ := val.Int64()
			return n
		case string:
			n, _ := strconv.ParseInt(val, 10, 64)
			return n
		}
	}
	return 0
}

func getFloat64(m map[string]interface{}, key string) float64 {
	if v, ok := m[key]; ok {
		switch val := v.(type) {
		case float64:
			return val
		case json.Number:
			f, _ := val.Float64()
			return f
		}
	}
	return 0
}

// urlCandidates 归一化一条轨道的地址候选：首选仍是列表里第一个不含显式端口（PCDN 直连）的地址
// （上游与本仓既有的选择规则，见 baseURLRegex），其余候选按响应里的原始顺序返回、去掉重复项。
//
// 此前只留下被选中的那一个，其余 backup_url 直接丢弃：playurl 本来就给了 1~2 个备用镜像，镜像
// 漏对象或连不上时却没有任何地址可换（本仓有意差异，见 docs/UPSTREAM_ALIGNMENT.md §4.33）。
func urlCandidates(baseURL string, rawBackups interface{}) (string, []string) {
	urlList := make([]string, 0, 4)
	if baseURL != "" {
		urlList = append(urlList, baseURL)
	}
	if backups, ok := rawBackups.([]interface{}); ok {
		for _, b := range backups {
			if bs, ok := b.(string); ok && bs != "" {
				urlList = append(urlList, bs)
			}
		}
	}
	if len(urlList) == 0 {
		return "", nil
	}

	final := urlList[0]
	for _, u := range urlList {
		if !baseURLRegex.MatchString(u) {
			final = u
			break
		}
	}
	// 回退顺序尊重既有的选择偏好：先列其余非 PCDN 地址（响应里的原始顺序），把带显式端口的
	// 直连地址放到最后——它们是首选规则刻意避开的，但作为最后的兜底仍然比"没有地址可换"强。
	rest := make([]string, 0, len(urlList)-1)
	appendUnique := func(u string) {
		if u == final {
			return
		}
		for _, seen := range rest {
			if seen == u {
				return
			}
		}
		rest = append(rest, u)
	}
	for _, u := range urlList {
		if !baseURLRegex.MatchString(u) {
			appendUnique(u)
		}
	}
	for _, u := range urlList {
		if baseURLRegex.MatchString(u) {
			appendUnique(u)
		}
	}
	return final, rest
}

func appendUniqueVideo(videos []entity.Video, v entity.Video) []entity.Video {
	for _, existing := range videos {
		if existing.ID == v.ID && existing.Dfn == v.Dfn && existing.Res == v.Res && existing.FPS == v.FPS && existing.Codecs == v.Codecs && existing.Bandwidth == v.Bandwidth && existing.Dur == v.Dur {
			return videos
		}
	}
	return append(videos, v)
}

func appendUniqueAudio(audios []entity.Audio, a entity.Audio) []entity.Audio {
	for _, existing := range audios {
		if existing.ID == a.ID && existing.Dfn == a.Dfn && existing.Codecs == a.Codecs && existing.Bandwidth == a.Bandwidth && existing.Dur == a.Dur {
			return audios
		}
	}
	return append(audios, a)
}
