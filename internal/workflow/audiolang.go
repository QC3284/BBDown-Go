package workflow

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/QC3284/BBDown-Go/internal/config"
)

// t79：--audio-language 配音语言选择 + 产物语言后缀（吸收 BBDownT 2.1.x 的 AudioLanguageSelection）。
//
// 语义对照 BBDownT（BBDownT/AudioLanguageSelection.cs、BBDownT.Core/AudioLanguageMapper.cs）：
//   - 语言清单来自 playurl 文档的 language.items[]（每项 {lang, title, production_type}，
//     production_type == 2 表示 AI 生成），同一文档顶层的 cur_language 是当前播放语言；
//   - 选择是**代码精确匹配**（大小写不敏感），只认清单里真实出现的代码；
//   - 选中后产物名附加 .audio-<代码小写> 后缀（默认语言不加）；仅 WEB/DASH 模式；
//   - 显式指定语言时不用「已下载 aid 档案」去重（档案无法区分配音版本）。
//
// 与 BBDownT 的差别（有意，待登记 §4.x）：BBDownT 用 cur_language 参数**重新请求** playurl 来真正
// 切换音轨；本仓解析层（internal/parser.fetchPlayJSON）还没有这个参数（不在 t79 范围）。所以这里把
// 「清单解析 + 选择 + 命名」做成纯函数，而「选中的语言不是当前语言」在下载前**明确失败**——
// 绝不静默保存默认音轨却挂着所选语言的后缀（那比不支持更糟）。
const (
	audioLanguageSuffixPrefix = ".audio-"
	audioLanguageVideoMarker  = ".video"
	audioLanguageMaxCodeLen   = 64
)

// AudioLanguage 是一条可选配音语言（对应 playurl 文档的 language.items[]）。
type AudioLanguage struct {
	Code  string // 接口给的代码（如 chi / jpn / en-US），-I 列表与匹配都用它
	Title string // 显示名（接口没给就用代码）
	IsAI  bool   // production_type == 2：AI 生成/翻译
}

// audioLanguageCodeError 与 BBDownT 逐字一致：让照抄上游提示的用户能搜到同一条原因。
const audioLanguageCodeError = "--audio-language 必须是单个配音语言代码（字母、数字、- 或 _，最多64字符）；请从 -info 列表选择"

// NormalizeAudioLanguageCode 去掉首尾空白；空输入（含全空白）返回空串 = 未指定。
func NormalizeAudioLanguageCode(code string) string {
	return strings.TrimSpace(code)
}

func isASCIILetterOrDigit(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// ValidateAudioLanguageCode 校验代码**形态**（不校验是否在清单里——那要等解析结果）。
// 空串合法（= 未指定），与 BBDownT 的 ValidateCode 一致。
func ValidateAudioLanguageCode(code string) error {
	code = NormalizeAudioLanguageCode(code)
	if code == "" {
		return nil
	}
	if len(code) > audioLanguageMaxCodeLen || !isASCIILetterOrDigit(code[0]) {
		return errors.New(audioLanguageCodeError)
	}
	for i := 0; i < len(code); i++ {
		c := code[i]
		if !isASCIILetterOrDigit(c) && c != '-' && c != '_' {
			return errors.New(audioLanguageCodeError)
		}
	}
	return nil
}

// ValidateAudioLanguageOptions 校验与其它选项的组合（在发任何网络请求之前调用）。
//
// 两条互斥与 BBDownT 逐字一致：tv/app/intl 模式拿不到可切换的语言清单；仅字幕/仅封面/仅弹幕
// 根本不会下载音轨。
func ValidateAudioLanguageOptions(code string, opt config.MyOption) error {
	code = NormalizeAudioLanguageCode(code)
	if err := ValidateAudioLanguageCode(code); err != nil {
		return err
	}
	if code == "" {
		return nil
	}
	if opt.UseTvAPI || opt.UseAppAPI || opt.UseIntlAPI {
		return errors.New("--audio-language 目前仅支持默认 WEB 解析模式，不能与 -tv、-app、-intl 同时使用")
	}
	if opt.SubOnly || opt.CoverOnly || opt.DanmakuOnly {
		return errors.New("--audio-language 不能与仅字幕、仅封面或仅弹幕模式同时使用")
	}
	return nil
}

// ParseAudioLanguages 从 playurl 原始文档里取配音语言清单与当前语言。
//
// 复用 result.WebJSONString（已经拿到的同一份响应），不额外发请求；文档里没有 language 段时
// 返回空清单（绝大多数稿件都是这样，调用方据此跳过整块逻辑）。
func ParseAudioLanguages(rawJSON string) (langs []AudioLanguage, current string) {
	if strings.TrimSpace(rawJSON) == "" {
		return nil, ""
	}
	var doc struct {
		CurLanguage string `json:"cur_language"`
		Language    struct {
			Items []struct {
				Lang           string `json:"lang"`
				Title          string `json:"title"`
				ProductionType int    `json:"production_type"`
			} `json:"items"`
		} `json:"language"`
	}
	if err := json.Unmarshal([]byte(rawJSON), &doc); err != nil {
		return nil, ""
	}
	seen := make(map[string]bool, len(doc.Language.Items))
	for _, item := range doc.Language.Items {
		code := strings.TrimSpace(item.Lang)
		if code == "" || seen[strings.ToLower(code)] {
			continue // 空代码与重复代码跳过（与 BBDownT 的 seen 集合一致）
		}
		seen[strings.ToLower(code)] = true
		title := strings.TrimSpace(item.Title)
		if title == "" {
			title = code
		}
		langs = append(langs, AudioLanguage{Code: code, Title: title, IsAI: item.ProductionType == 2})
	}
	return langs, strings.TrimSpace(doc.CurLanguage)
}

// SelectAudioLanguage 按代码精确匹配（大小写不敏感）从清单里挑一条。
//
// 找不到时的错误文案与 BBDownT 同构：列出**真实可用**的代码（而不是只说「无效」），
// 用户可以直接照着改。
func SelectAudioLanguage(langs []AudioLanguage, requested string) (AudioLanguage, error) {
	code := NormalizeAudioLanguageCode(requested)
	if code == "" {
		return AudioLanguage{}, errors.New("--audio-language 需要一个配音语言代码（如 chi / jpn）")
	}
	for _, l := range langs {
		if strings.EqualFold(l.Code, code) {
			return l, nil
		}
	}
	if len(langs) == 0 {
		return AudioLanguage{}, fmt.Errorf("没有找到配音语言 %s；接口未提供可选配音语言（去掉 --audio-language 即用默认音轨）", code)
	}
	codes := make([]string, 0, len(langs))
	for _, l := range langs {
		codes = append(codes, l.Code)
	}
	return AudioLanguage{}, fmt.Errorf("没有找到配音语言 %s；可用：%s", code, strings.Join(codes, ", "))
}

// AudioLanguageLines 渲染 -I/信息块里的「可选配音语言」清单（格式与 BBDownT 的 PrintAvailable 逐字一致）：
//
//	可选配音语言：
//	  zh-CN: 中文原声 [默认]
//	  en-US: English [AI] [当前]
//
// 清单为空时返回 nil（不打任何行）：绝大多数稿件没有配音语言，不该在每屏都多一行噪音。
// [当前] 只在「当前语言 ≠ 默认语言」时出现，与 BBDownT 的判定一致。
func AudioLanguageLines(langs []AudioLanguage, current, def string) []string {
	if len(langs) == 0 {
		return nil
	}
	lines := make([]string, 0, len(langs)+1)
	lines = append(lines, "可选配音语言：")
	for _, l := range langs {
		line := "  " + l.Code + ": " + l.Title
		if l.IsAI {
			line += " [AI]"
		}
		if def != "" && strings.EqualFold(l.Code, def) {
			line += " [默认]"
		}
		if current != "" && !strings.EqualFold(current, def) && strings.EqualFold(l.Code, current) {
			line += " [当前]"
		}
		lines = append(lines, line)
	}
	return lines
}

// WithAudioLanguageSuffix 在扩展名前插入 .audio-<代码小写>（BBDownT 的 LanguageSuffixPrefix 逐字）：
//
//	video.mp4 + en-US → video.audio-en-us.mp4
//	video.mp4 + ""    → video.mp4（默认语言不加后缀）
func WithAudioLanguageSuffix(path, code string) string {
	code = NormalizeAudioLanguageCode(code)
	if code == "" || path == "" {
		return path
	}
	ext := filepath.Ext(path)
	return path[:len(path)-len(ext)] + audioLanguageSuffixPrefix + strings.ToLower(code) + ext
}

// AudioLanguageOutputPath 是产物名的最终形态（BBDownT 的 OutputPath 逐字）：
// 先加语言后缀，再按产物类型落定扩展名——
//
//	仅音频（--audio-only）：video.audio-en-us.m4a（在「已存在跳过」判定之前就落定，
//	                        否则同名 .mp4 会被误当成要的 .m4a）
//	仅视频（--video-only）：video.audio-en-us.video.mp4（与音轨变体区分开）
func AudioLanguageOutputPath(path, code string, audioOnly, videoOnly bool) string {
	out := WithAudioLanguageSuffix(path, code)
	if audioOnly {
		// 2026-10-17 起仅音频产物按音轨原生格式输出（FLAC → .flac）：扩展名在上游
		// savePath 阶段已落定，这里不再强制 .m4a（否则会把 .flac 改回去）。
		return out
	}
	if videoOnly {
		ext := filepath.Ext(out)
		return out[:len(out)-len(ext)] + audioLanguageVideoMarker + ext
	}
	return out
}

// changeExtension 等价于 .NET 的 Path.ChangeExtension：替换最后一个扩展名。
func changeExtension(path, ext string) string {
	old := filepath.Ext(path)
	return path[:len(path)-len(old)] + ext
}

// UseAidArchiveWithAudioLanguage 判定这次运行是否使用「已下载 aid 档案」去重。
//
// 档案只记 aid、无法区分配音版本（BBDownT 同款理由）：显式指定语言时不查也不写档案，
// 否则下了 jpn 版就会被记成「这稿已下过」，默认版反而跳过了。
func UseAidArchiveWithAudioLanguage(saveArchives bool, language string) bool {
	return saveArchives && NormalizeAudioLanguageCode(language) == ""
}

// resolveAudioLanguage 校验并确认本页的配音语言选择，把结果记到 w.audioLanguageConfirmed。
//
// 三种情形：
//   - 未指定（空串）：什么都不做，audioLanguageConfirmed 保持空 → 产物名逐字不变；
//   - 选中的代码 == 当前播放语言：确认成功，产物加 .audio-<code> 后缀（与 BBDownT 一致——显式
//     指定默认语言也要后缀，否则同一目录里的默认版与指定版无法区分）；
//   - 选中的代码 != 当前语言：解析层还没有 cur_language 参数（见文件头），这里**明确失败**，
//     绝不静默保存默认音轨却挂着所选语言的后缀。
func (w *Workflow) resolveAudioLanguage(langs []AudioLanguage, current string) error {
	w.audioLanguageConfirmed = ""
	code := NormalizeAudioLanguageCode(w.audioLanguage)
	if code == "" {
		return nil
	}
	selected, err := SelectAudioLanguage(langs, code)
	if err != nil {
		return err
	}
	// t95 起解析层已接入 cur_language 重请求并在响应侧确认（parser.confirmPlayURLanguage）：
	// 链路不一致时解析层已经抛错，这里不再自行对照 current——绝不静默下错音轨由解析层保证。
	_ = current
	w.audioLanguageConfirmed = selected.Code
	return nil
}

// confirmedAudioLanguage 返回本页已确认的语言代码（空 = 不加后缀）。
func (w *Workflow) confirmedAudioLanguage() string { return w.audioLanguageConfirmed }

// pageArtifactPath 是本任务**唯一**的语言后缀注入点：页面产物（混流输出 / 仅音频 / 仅视频）。
//
// 配音轨与背景音轨的路径由 role / background 分支各自生成（<aid>/<aid>.<cid>.<audio_id>.m4a），
// 刻意**不**经过这里：它们本来就是按 audio_id 区分的独立产物，再叠语言后缀只会让同一语言出现
// 多套命名（组合用例见 TestAudioLanguageAndDubbingAreOrthogonal）。
func (w *Workflow) pageArtifactPath(path string) string {
	code := w.confirmedAudioLanguage()
	if code == "" {
		return path
	}
	return AudioLanguageOutputPath(path, code, w.Cfg.AudioOnly, w.Cfg.VideoOnly)
}

// useAidArchive 判定本次运行是否使用「已下载 aid 档案」（显式配音语言时不查也不写）。
func (w *Workflow) useAidArchive() bool {
	return UseAidArchiveWithAudioLanguage(w.Cfg.SaveArchivesToFile, w.audioLanguage)
}
