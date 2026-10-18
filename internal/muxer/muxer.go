package muxer

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QC3284/BBDown-Go/internal/entity"
	"github.com/QC3284/BBDown-Go/internal/util"
)

// FFMPEG and MP4BOX are configurable external tool paths (set by findBinaries).
var FFMPEG = "ffmpeg"
var MP4BOX = "mp4box"

// MuxAV merges audio and video tracks using ffmpeg or mp4box (upstream BBDownMuxer).
func MuxAV(ctx context.Context, useMp4box bool, bvid, videoPath, audioPath, outPath, desc, title, author, episodeID, pic, lang string, subs []entity.Subtitle, audioOnly, videoOnly, simplyMux bool, points []entity.ViewPoint, pubTime int64, isHevc bool, audioMaterial []entity.AudioMaterial, timeoutMinutes int) error {
	if audioOnly && audioPath != "" {
		videoPath = ""
	}
	if videoOnly {
		audioPath = ""
	}

	url := "https://www.bilibili.com/video/" + bvid + "/"

	if useMp4box {
		return muxByMp4box(ctx, url, videoPath, audioPath, audioMaterial, outPath, desc, title, author, episodeID, pic, lang, subs, audioOnly, videoOnly, points, timeoutMinutes)
	}
	return muxByFFmpeg(ctx, url, videoPath, audioPath, outPath, desc, title, author, episodeID, pic, lang, subs, audioOnly, videoOnly, simplyMux, points, pubTime, isHevc, audioMaterial, timeoutMinutes)
}

// escapeString escapes backslashes and double quotes (upstream EscapeString).
// escapeString escapes backslashes and double quotes and folds line breaks away
// (upstream EscapeString). The value is embedded into mp4box's own -itags/-add
// token syntax, where a raw newline would break the token and a lone backslash
// would be consumed as an escape sequence.
func escapeString(s string) string {
	if s == "" {
		return s
	}
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "\"", "\\\"")
	s = strings.ReplaceAll(s, "\r", " ")
	return strings.ReplaceAll(s, "\n", " ")
}

func muxByFFmpeg(ctx context.Context, url, videoPath, audioPath, outPath, desc, title, author, episodeID, pic, lang string, subs []entity.Subtitle, audioOnly, videoOnly, simplyMux bool, points []entity.ViewPoint, pubTime int64, isHevc bool, audioMaterial []entity.AudioMaterial, timeoutMinutes int) error {
	// FLAC 原生容器（2026-10-17）：FLAC 装不下 mov_text 字幕轨，字幕走侧车 .srt 文件（下载阶段已落盘）。
	nativeFlac := audioOnly && strings.EqualFold(filepath.Ext(outPath), ".flac")
	if nativeFlac {
		subs = nil
	}
	dir := filepath.Dir(outPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	args := []string{}
	// Output-side options must come AFTER every -i input: ffmpeg applies an
	// option to the next file, so emitting -metadata/-disposition between two
	// -i arguments turns them into input options and the whole mux fails with
	// exit 234 (upstream BBDownMuxer emits them in the output section).
	var outputOpts []string
	inputCount := 0

	if videoPath != "" {
		args = append(args, "-i", videoPath)
		inputCount++
	}
	if audioPath != "" {
		args = append(args, "-i", audioPath)
		inputCount++
	}

	// Background audio / dubbing tracks (upstream audioMaterial).
	if len(audioMaterial) > 0 {
		outputOpts = append(outputOpts, "-metadata:s:a:0", "title=原音频")
		audioCount := 0
		for _, a := range audioMaterial {
			args = append(args, "-i", a.Path)
			inputCount++
			audioCount++
			if a.Title != "" {
				outputOpts = append(outputOpts, fmt.Sprintf("-metadata:s:a:%d", audioCount), "title="+escapeString(a.Title))
			}
			if a.PersonName != "" {
				outputOpts = append(outputOpts, fmt.Sprintf("-metadata:s:a:%d", audioCount), "artist="+escapeString(a.PersonName))
			}
		}
	}

	if pic != "" {
		args = append(args, "-i", pic)
		inputCount++
	}

	subIdx := 0
	for _, s := range subs {
		if stat, err := os.Stat(s.Path); err == nil && stat.Size() > 0 {
			args = append(args, "-i", s.Path)
			_, langName := util.GetSubtitleCode(s.Lan)
			langCode, _ := util.GetSubtitleCode(s.Lan)
			outputOpts = append(outputOpts,
				fmt.Sprintf("-metadata:s:s:%d", subIdx), "title="+escapeString(langName),
				fmt.Sprintf("-metadata:s:s:%d", subIdx), "language="+escapeString(langCode),
			)
			inputCount++
			subIdx++
		}
	}

	if pic != "" {
		dispositionIdx := "1"
		if audioOnly {
			dispositionIdx = "0"
		}
		outputOpts = append(outputOpts, "-disposition:v:"+dispositionIdx, "attached_pic")
	}

	// Chapters (view points) via an FFMETADATA sidecar file.
	metaFile := ""
	if len(points) > 0 {
		baseDir := filepath.Dir(videoPath)
		if baseDir == "" {
			baseDir = filepath.Dir(audioPath)
		}
		if baseDir == "" {
			baseDir = "."
		}
		metaFile = filepath.Join(baseDir, "chapters-"+strings.TrimSuffix(filepath.Base(outPath), filepath.Ext(outPath)))
		if err := os.WriteFile(metaFile, []byte(ffmpegMetaString(points)), 0o644); err != nil {
			return err
		}
		args = append(args, "-i", metaFile)
		outputOpts = append(outputOpts, "-map_chapters", fmt.Sprintf("%d", inputCount))
	}

	// All inputs are in place now: flush the deferred output-side options.
	args = append(args, outputOpts...)

	for i := 0; i < inputCount; i++ {
		args = append(args, "-map", fmt.Sprintf("%d", i))
	}

	args = append(args, "-loglevel")
	if util.DebugLogEnabled() {
		args = append(args, "verbose")
	} else {
		args = append(args, "warning")
	}
	args = append(args, "-y")

	if !simplyMux {
		metaTitle := episodeID
		if metaTitle == "" {
			metaTitle = title
		}
		args = append(args, "-metadata", "title="+escapeString(metaTitle))
		args = append(args, "-metadata", "comment="+escapeString(url))
		if lang != "" {
			args = append(args, "-metadata:s:a:0", "language="+escapeString(lang))
		}
		if desc != "" {
			args = append(args, "-metadata", "description="+escapeString(desc))
		}
		if author != "" {
			args = append(args, "-metadata", "artist="+escapeString(author))
		}
		if episodeID != "" {
			args = append(args, "-metadata", "album="+escapeString(title))
		}
		if pubTime != 0 {
			args = append(args, "-metadata", "creation_time="+time.Unix(pubTime, 0).UTC().Format("2006-01-02T15:04:05.000000Z"))
		}
	}

	args = append(args, "-c:v", "copy", "-c:a", "copy")
	if audioOnly && audioPath == "" {
		args = append(args, "-vn")
	}
	if subs != nil {
		args = append(args, "-c:s", "mov_text")
	}
	// fix macOS hev1, see https://discussions.apple.com/thread/253081863
	if runtime.GOOS == "darwin" && isHevc {
		args = append(args, "-tag:v:0", "hvc1")
	}
	// 仅音频 + FLAC 原生容器（用户定 2026-10-17：文件原本什么格式就输出什么格式）：
	// movflags/faststart 与 -f mp4 都是 MP4 专属，FLAC 容器必须走 -f flac。
	if audioOnly && strings.EqualFold(filepath.Ext(outPath), ".flac") {
		args = append(args, "-f", "flac", "--", outPath)
	} else {
		args = append(args, "-movflags", "faststart", "-strict", "unofficial", "-strict", "-2", "-f", "mp4", "--", outPath)
	}

	util.LogDebug("ffmpeg: %s %s", FFMPEG, strings.Join(args, " "))
	cmd := exec.CommandContext(ctx, FFMPEG, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err := cmd.Run()
	if metaFile != "" {
		os.Remove(metaFile)
	}
	return err
}

// CheckFFmpegDOVI reports whether ffmpeg supports Dolby Vision muxing
// (libavutil > 57 or 57.17+, upstream ExternalToolHelper.CheckFFmpegDOVIAsync).
// doviProbeTimeout bounds the ffmpeg version probe. A hung or broken binary
// must not stall the download: the probe runs before muxing on the critical
// path (upstream guards CheckFFmpegDOVI with a 5s async timeout).
var doviProbeTimeout = 5 * time.Second

// doviProbeWaitDelay bounds the drain of the probe's output after the context
// is done (see the WaitDelay comment in CheckFFmpegDOVI).
var doviProbeWaitDelay = 2 * time.Second

// doviProbeCache 按 ffmpeg 二进制路径缓存探测结果：探测要启动一次 ffmpeg 进程且在混流关键
// 路径上，而同一进程内同一二进制的版本不会变——多P 杜比视界稿件此前每P都探一次。serve 下
// 不同任务可能配不同 --ffmpeg-path，所以按路径存而不是全局一次（本仓优化，见 ROADMAP O6）。
var (
	doviProbeMu    sync.Mutex
	doviProbeCache = map[string]bool{}
)

func CheckFFmpegDOVI() bool {
	doviProbeMu.Lock()
	if v, ok := doviProbeCache[FFMPEG]; ok {
		doviProbeMu.Unlock()
		return v
	}
	doviProbeMu.Unlock()

	v := probeFFmpegDOVI()

	doviProbeMu.Lock()
	doviProbeCache[FFMPEG] = v
	doviProbeMu.Unlock()
	return v
}

func probeFFmpegDOVI() bool {
	ctx, cancel := context.WithTimeout(context.Background(), doviProbeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, FFMPEG, "-version")
	// Killing the process is not enough: a probe that spawned a child (or leaked
	// its stdout pipe) keeps the pipe open and Wait blocks regardless of the
	// context. WaitDelay bounds that wait explicitly.
	cmd.WaitDelay = doviProbeWaitDelay
	out, err := cmd.CombinedOutput()
	if err != nil {
		return false
	}
	m := doviVersionRegex.FindStringSubmatch(string(out))
	if m == nil {
		return false
	}
	major, err := strconv.Atoi(m[1])
	if err != nil {
		return false
	}
	minor, err := strconv.Atoi(m[2])
	if err != nil {
		return false
	}
	// 上游 ExternalToolHelper.CheckFFmpegDOVIAsync：libavutil > 57 或 57.17+（≈ ffmpeg 6.0+）。
	// 本仓此前写成 major >= 5——对任何真实 libavutil（55~60）都成立，等于恒 true；它与
	// 「正则丢反斜杠」两个 bug 恰好互相掩盖（一个恒 false、一个恒 true），于是杜比视界要么
	// 一律回退 mp4box、要么一律用 ffmpeg，只是从没按版本判断过。
	return major > 57 || (major == 57 && minor >= 17)
}

// 用 POSIX 字符类而不是 \s/\d：等价的 [`libavutil\s+(\d+)\. +(\d+)\.`]（上游 BBDownUtil），
// 但不受这层编辑链的转义影响。此前那版字面量把反斜杠全丢了（`libavutils+(d+). +(d+).`），
// 永远匹配不到 ffmpeg 的 "libavutil  58.  2.100"，于是 CheckFFmpegDOVI 恒 false——杜比视界
// 一律回退 mp4box，没装 mp4box 的机器直接混流失败。
var doviVersionRegex = regexp.MustCompile("libavutil[[:space:]]+([0-9]+)[.][[:space:]]+([0-9]+)[.]")

// ffmpegMetaString builds an FFMETADATA chapters file (upstream GetFFmpegMetaString).
func ffmpegMetaString(points []entity.ViewPoint) string {
	var sb strings.Builder
	sb.WriteString(";FFMETADATA\n")
	for _, p := range points {
		const timeBase = 1000
		sb.WriteString("[CHAPTER]\n")
		sb.WriteString(fmt.Sprintf("TIMEBASE=1/%d\n", timeBase))
		sb.WriteString(fmt.Sprintf("START=%d\n", p.Start*timeBase))
		sb.WriteString(fmt.Sprintf("END=%d\n", p.End*timeBase))
		title := strings.NewReplacer("\n", " ", "\r", " ").Replace(p.Title)
		sb.WriteString("title=" + title + "\n")
		sb.WriteString("\n")
	}
	return sb.String()
}

func muxByMp4box(ctx context.Context, url, videoPath, audioPath string, audioMaterial []entity.AudioMaterial, outPath, desc, title, author, episodeID, pic, lang string, subs []entity.Subtitle, audioOnly, videoOnly bool, points []entity.ViewPoint, timeoutMinutes int) error {
	dir := filepath.Dir(outPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	args := []string{"-inter", "500", "-noprog"}
	nowID := 0

	if videoPath != "" {
		trackID := "1"
		if audioOnly && audioPath == "" {
			trackID = "2"
		}
		args = append(args, "-add", fmt.Sprintf("%s#trackID=%s:name=", videoPath, trackID))
		nowID++
	}
	if audioPath != "" {
		audioLang := lang
		if audioLang == "" {
			audioLang = "und"
		}
		args = append(args, "-add", fmt.Sprintf("%s:lang=%s", audioPath, audioLang))
		nowID++
	}

	// Dubbing / background audio tracks must join the -add chain exactly like the
	// ffmpeg branch does. Otherwise switching to mp4box automatically for Dolby
	// Vision (ffmpeg < 5.0) silently dropped already-downloaded tracks, which the
	// workflow then deleted — permanent data loss with no warning (upstream v1.6.17).
	for _, material := range audioMaterial {
		args = append(args, "-add", fmt.Sprintf("%s:lang=und", material.Path))
		nowID++
		name := material.Title
		if name == "" {
			name = material.PersonName
		}
		if name != "" {
			args = append(args, "-udta", fmt.Sprintf("%d:type=name:str=\"%s\"", nowID, escapeString(name)))
		}
	}

	metaFile := ""
	if len(points) > 0 {
		baseDir := filepath.Dir(videoPath)
		if baseDir == "" {
			baseDir = filepath.Dir(audioPath)
		}
		if baseDir == "" {
			baseDir = "."
		}
		metaFile = filepath.Join(baseDir, "chapters-"+strings.TrimSuffix(filepath.Base(outPath), filepath.Ext(outPath)))
		if err := os.WriteFile(metaFile, []byte(mp4boxMetaString(points)), 0o644); err != nil {
			return err
		}
		args = append(args, "-chap", metaFile)
	}

	// -itags metadata blob.
	var metaArg strings.Builder
	metaArg.WriteString("tool=")
	if pic != "" {
		metaArg.WriteString(":cover=\"")
		// The cover path goes through the same escaping as the other values: on
		// Windows a "\" in the path was consumed as an escape sequence and the
		// cover was silently dropped (upstream RF-6).
		metaArg.WriteString(escapeString(pic))
		metaArg.WriteString("\"")
	}
	if episodeID != "" {
		metaArg.WriteString(":album=\"")
		metaArg.WriteString(escapeString(title))
		metaArg.WriteString("\":title=\"")
		metaArg.WriteString(escapeString(episodeID))
		metaArg.WriteString("\"")
	} else {
		metaArg.WriteString(":title=\"")
		metaArg.WriteString(escapeString(title))
		metaArg.WriteString("\"")
	}
	metaArg.WriteString(":sdesc=\"")
	metaArg.WriteString(escapeString(desc))
	metaArg.WriteString("\":comment=\"")
	metaArg.WriteString(escapeString(url))
	metaArg.WriteString("\":artist=\"")
	metaArg.WriteString(escapeString(author))
	metaArg.WriteString("\"")
	if metaArg.Len() > len("tool=") {
		args = append(args, "-itags", metaArg.String())
	}

	for _, s := range subs {
		if stat, err := os.Stat(s.Path); err == nil && stat.Size() > 0 {
			nowID++
			langCode, langName := util.GetSubtitleCode(s.Lan)
			args = append(args, "-add", fmt.Sprintf("%s#trackID=1:name=%s:hdlr=sbtl:lang=%s", s.Path, langName, langCode))
			args = append(args, "-udta", fmt.Sprintf("%d:type=name:str=\"%s\"", nowID, escapeString(langName)))
		}
	}

	if util.DebugLogEnabled() {
		args = append(args, "-v")
	}
	args = append(args, "-new", "--", outPath)

	util.LogDebug("mp4box: %s %s", MP4BOX, strings.Join(args, " "))
	cmd := exec.CommandContext(ctx, MP4BOX, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err := cmd.Run()
	if metaFile != "" {
		os.Remove(metaFile)
	}
	return err
}

// mp4boxMetaString builds an mp4box chapter file (upstream GetMp4boxMetaString).
func mp4boxMetaString(points []entity.ViewPoint) string {
	var sb strings.Builder
	for _, p := range points {
		title := strings.NewReplacer("\n", " ", "\r", " ").Replace(p.Title)
		sb.WriteString(fmt.Sprintf("%s %s\n", util.FormatTime(p.Start, true), title))
	}
	return sb.String()
}

// MergeFLV merges FLV segments into a single MP4 file with validation (upstream).
func MergeFLV(ctx context.Context, files []string, outPath string) error {
	if len(files) == 0 {
		return nil
	}
	if len(files) == 1 {
		return os.Rename(files[0], outPath)
	}

	var tsFiles []string
	// Intermediate .ts files must not survive a failure path (upstream wraps the
	// conversion in try/finally): an error mid-loop used to leave every already
	// converted segment behind, accumulating gigabytes in the work directory.
	converted := false
	defer func() {
		if converted {
			return
		}
		for _, ts := range tsFiles {
			os.Remove(ts)
		}
	}()
	for _, file := range files {
		tsFile := strings.TrimSuffix(file, filepath.Ext(file)) + ".ts"
		args := []string{"-loglevel", "warning", "-y", "-i", file, "-map", "0", "-c", "copy", "-f", "mpegts", "-bsf:v", "h264_mp4toannexb", tsFile}
		util.LogDebug("ffmpeg: %s %s", FFMPEG, strings.Join(args, " "))
		cmd := exec.CommandContext(ctx, FFMPEG, args...)
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("FLV segment conversion failed: %w", err)
		}
		if info, err := os.Stat(tsFile); err != nil || info.Size() == 0 {
			return fmt.Errorf("FLV segment conversion produced no output")
		}
		tsFiles = append(tsFiles, tsFile)
	}

	if err := util.CombineMultipleFilesIntoSingleFile(ctx, tsFiles, outPath); err != nil {
		return err
	}
	converted = true
	// All conversions succeeded: clean up .ts intermediates and sources.
	for _, ts := range tsFiles {
		os.Remove(ts)
	}
	for _, file := range files {
		os.Remove(file)
	}
	return nil
}
