package live

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"encoding/binary"
	"github.com/QC3284/BBDown-Go/internal/muxer"
	"github.com/QC3284/BBDown-Go/internal/util"
)

const (
	reconnectBaseBackoff = 3 * time.Second
	reconnectMaxBackoff  = 30 * time.Second
)

// liveAPIBase is the live API origin; a variable so tests can point the
// recorder at a local server (the download path has no config host of its own).
var liveAPIBase = "https://api.live.bilibili.com"

// readStallTimeout is a variable so tests can shrink it; production uses 60s.
var readStallTimeout = 60 * time.Second

// resolveLive is a seam for tests: DownloadToFile is otherwise untestable
// without hitting the live Bilibili API.
var resolveLive = ResolveLive

// ResolveLive resolves a Bilibili live room ID to a stream URL and metadata.
func ResolveLive(ctx context.Context, roomID string, client *util.HTTPClient) (streamURL, title, uname string, err error) {
	if _, err := strconv.ParseInt(roomID, 10, 64); err != nil {
		return "", "", "", fmt.Errorf("直播间 ID 必须是数字，当前值: %q", roomID)
	}

	// Get room info
	infoAPI := liveAPIBase + "/room/v1/Room/get_info?room_id=" + roomID
	infoJSON, err := client.GetWebSource(ctx, infoAPI)
	if err != nil {
		return "", "", "", fmt.Errorf("获取直播间信息失败: %w", err)
	}

	var infoResult struct {
		Code int `json:"code"`
		Data struct {
			Title      string `json:"title"`
			Uname      string `json:"uname"`
			LiveStatus int    `json:"live_status"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(infoJSON), &infoResult); err != nil {
		return "", "", "", fmt.Errorf("解析直播间信息: %w", err)
	}

	d := infoResult.Data
	title = d.Title
	if title == "" {
		title = "直播间" + roomID
	}
	uname = d.Uname
	if d.LiveStatus != 1 {
		return "", "", "", fmt.Errorf("直播间 %s 当前未在直播", roomID)
	}

	// qn=30000 asks for the highest tier the account is entitled to (credentials
	// are loaded before recording). An anonymous or unprivileged session may be
	// offered nothing usable at that tier, in which case the request is repeated
	// once at qn=10000 (upstream v1.6.13).
	streamURL, err = fetchLiveStreamURL(ctx, client, roomID, "30000")
	if err != nil {
		return "", "", "", err
	}
	if streamURL == "" {
		streamURL, err = fetchLiveStreamURL(ctx, client, roomID, "10000")
		if err != nil {
			return "", "", "", err
		}
	}
	if streamURL == "" {
		return "", "", "", fmt.Errorf("无法获取直播间 %s 的可录制流地址（qn=30000 与 qn=10000 均无可用 flv 流）", roomID)
	}
	return streamURL, title, uname, nil
}

// fetchLiveStreamURL requests the room play URL at one quality tier. An empty
// URL with a nil error means the API answered but offered no usable flv stream,
// which lets the caller fall back to a lower tier instead of failing outright.
func fetchLiveStreamURL(ctx context.Context, client *util.HTTPClient, roomID, qn string) (string, error) {
	playAPI := fmt.Sprintf(
		liveAPIBase+"/xlive/web-room/v2/index/getRoomPlayInfo?room_id=%s&protocol=0,1&format=0,1,2&codec=0,1&qn=%s&platform=web",
		roomID, qn,
	)
	playJSON, err := client.GetWebSource(ctx, playAPI)
	if err != nil {
		return "", fmt.Errorf("获取直播流地址失败: %w", err)
	}

	var playResult struct {
		Code int `json:"code"`
		Data struct {
			PlayurlInfo struct {
				Playurl struct {
					Stream []struct {
						Format []struct {
							FormatName string `json:"format_name"`
							Codec      []struct {
								BaseURL string `json:"base_url"`
								URLInfo []struct {
									Host  string `json:"host"`
									Extra string `json:"extra"`
								} `json:"url_info"`
							} `json:"codec"`
						} `json:"format"`
					} `json:"stream"`
				} `json:"playurl"`
			} `json:"playurl_info"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(playJSON), &playResult); err != nil {
		return "", fmt.Errorf("解析直播流信息: %w", err)
	}

	for _, stream := range playResult.Data.PlayurlInfo.Playurl.Stream {
		for _, format := range stream.Format {
			if format.FormatName != "flv" {
				continue
			}
			for _, codec := range format.Codec {
				baseURL := codec.BaseURL
				if baseURL == "" {
					continue
				}
				for _, urlInfo := range codec.URLInfo {
					if urlInfo.Host == "" {
						continue
					}
					return urlInfo.Host + baseURL + urlInfo.Extra, nil
				}
			}
		}
	}

	// No usable flv stream at this tier: report it as "nothing found" rather than
	// an error so the caller can fall back to a lower quality.
	return "", nil
}

// DownloadToFile records a live stream with automatic reconnection, writing
// per-connection segments which are merged with ffmpeg at the end (upstream
// LiveStreamUtil). Cancellation stops recording but still finalizes segments.
// LiveRecordResult classifies one recording session (upstream LiveRecordResult).
// A plain bool could not tell "nothing was captured" from "the merge failed but
// the raw segments are still on disk for manual recovery".
type LiveRecordResult int

const (
	// LiveNoData: the room produced no usable segment.
	LiveNoData LiveRecordResult = iota
	// LiveSuccess: the segments were merged into the output file.
	LiveSuccess
	// LiveConcatFailedWithSegmentsSaved: the merge failed or produced a truncated
	// product; the raw segments were kept.
	LiveConcatFailedWithSegmentsSaved
)

func (r LiveRecordResult) String() string {
	switch r {
	case LiveSuccess:
		return "success"
	case LiveConcatFailedWithSegmentsSaved:
		return "concat-failed-segments-saved"
	default:
		return "no-data"
	}
}

// stateFor classifies an aborted session: anything already captured is kept for
// manual recovery, otherwise nothing was recorded at all.
func stateFor(total int64) LiveRecordResult {
	if total > 0 {
		return LiveConcatFailedWithSegmentsSaved
	}
	return LiveNoData
}

func DownloadToFile(ctx context.Context, roomID, path string, client *util.HTTPClient) (LiveRecordResult, error) {
	segRoot := path + ".segs"
	sessionDir := filepath.Join(segRoot, "session-"+time.Now().Format("20060102_150405"))
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		return LiveNoData, err
	}
	// Only this session's directory is removed once the recording finished
	// cleanly; on a terminal failure the segments are kept for manual recovery
	// (upstream v1.6.13). The .segs root is never removed recursively — it may
	// still hold segments retained by earlier sessions.
	keepSegments := false
	defer func() {
		if keepSegments {
			return
		}
		os.RemoveAll(sessionDir)
		os.Remove(segRoot) // succeeds only when it is now empty
	}()

	// Recordings left behind by earlier sessions are reported, never deleted: they
	// may be the only copy of a session whose merge failed.
	if stale := staleSessions(segRoot, sessionDir); len(stale) > 0 {
		util.LogWarn("检测到 %d 个此前保留的录制会话（不会自动清理，可手动合成）: %s", len(stale), strings.Join(stale, ", "))
	}

	var segFiles []string
	var total int64
	reconnect := 0
	segIdx := 0

	for {
		streamURL, _, _, err := resolveLive(ctx, roomID, client)
		if err != nil {
			// Live has ended: finish normally.
			if strings.Contains(err.Error(), "当前未在直播") {
				break
			}
			backoff := reconnectBackoff(reconnect)
			reconnect++
			util.LogWarn("直播流中断（%v），%v 后重连（第 %d 次）...", err, backoff, reconnect)
			if !sleepCtx(ctx, backoff) {
				break
			}
			continue
		}

		segPath := filepath.Join(sessionDir, fmt.Sprintf("seg-%03d.flv", segIdx))
		segIdx++
		n, err := streamToFile(ctx, streamURL, segPath)
		if err == nil {
			// t72 ①：干净 EOF 也要核对长度前缀——CDN 在直播结束时往往直接断开，最后一个 FLV
			// 标签的声明长度没被数据填满，那个分段就是**截断包**。已经写下的字节保留（可恢复），
			// 但这里要按「被中断」处理：走重连/收尾逻辑，并在合并前把尾巴裁掉。
			if res, serr := scanFLVFile(segPath); serr != nil {
				util.LogDebug("分段 %s 不是可识别的 FLV（%v），按原样保留", segPath, serr)
			} else if res.Truncated {
				err = truncatedSegmentError(segPath, res)
			}
		}
		if n > 0 {
			// Keep whatever was written: a cancelled or interrupted segment is
			// still recoverable content, and it takes part in the merge.
			total += n
			segFiles = append(segFiles, segPath)
		}
		if err != nil {
			var werr errLiveWrite
			if errors.As(err, &werr) {
				// Local write failure is terminal — retrying cannot help.
				keepSegments = total > 0
				util.LogWarn("直播分段写入失败（%v），已录制内容保留在 %s", werr, sessionDir)
				return stateFor(total), fmt.Errorf("直播录制写盘失败: %w", werr)
			}
			if ctx.Err() != nil {
				break
			}
			backoff := reconnectBackoff(reconnect)
			reconnect++
			util.LogWarn("直播流中断（%v），%v 后重连（第 %d 次）...", err, backoff, reconnect)
			if !sleepCtx(ctx, backoff) {
				break
			}
			continue
		}
		if n > 0 {
			reconnect = 0
		} else {
			// Zero bytes: re-check whether the streamer went offline.
			os.Remove(segPath)
			if _, _, _, err := resolveLive(ctx, roomID, client); err != nil {
				if strings.Contains(err.Error(), "当前未在直播") {
					break
				}
			}
		}

		// Stream ended (EOF): confirm offline; keep recording if still live.
		if ctx.Err() != nil {
			break
		}
		if _, _, _, err := resolveLive(ctx, roomID, client); err != nil {
			if strings.Contains(err.Error(), "当前未在直播") {
				break
			}
		}
	}

	if total == 0 {
		return LiveNoData, nil
	}

	// Merge segments (single segment: plain rename; more: ffmpeg concat).
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return stateFor(total), err
	}

	// t72 ②③：合并前逐段体检（媒体帧数）。零媒体帧的分段跳过、不否决整场；有坏段或全部
	// 零帧则整场失败——两条失败路径都**保留原分段与旧产物**（旧产物在 path 上，只有最终
	// rename 成功才会被替换）。
	plan, planErr := planLiveMerge(segFiles)
	if len(plan.Skipped) > 0 {
		util.LogWarn("跳过 %d 个零媒体帧的分段（断流重连的空段）：%d 个分段里保留 %d 个参与合成",
			len(plan.Skipped), len(segFiles), len(plan.Kept))
	}
	if planErr != nil {
		keepSegments = true
		return LiveConcatFailedWithSegmentsSaved, planErr
	}
	segFiles = plan.Kept

	if len(segFiles) == 1 {
		// 单段也要先裁掉截断尾巴（t72 ①），再改名：坏段绝不覆盖旧产物。
		if dropped, err := trimFLVTail(segFiles[0]); err == nil && dropped > 0 {
			util.LogDebug("分段 %s 丢弃了 %d 字节不完整标签", segFiles[0], dropped)
		}
		if err := os.Rename(segFiles[0], path); err != nil {
			return stateFor(total), err
		}
		return LiveSuccess, nil
	}

	// Cut every segment back to its last complete FLV tag first: one truncated
	// segment makes the concat demuxer abort the whole merge.
	for _, f := range segFiles {
		if dropped, err := trimFLVTail(f); err == nil && dropped > 0 {
			util.LogDebug("分段 %s 丢弃了 %d 字节不完整标签", f, dropped)
		}
	}

	util.Log("正在合成 %d 个直播分段...", len(segFiles))
	listPath := filepath.Join(sessionDir, "concat.txt")
	var sb strings.Builder
	for _, f := range segFiles {
		sb.WriteString("file \"")
		sb.WriteString(strings.ReplaceAll(f, "\\", "/"))
		sb.WriteString("\"\n")
	}
	if err := os.WriteFile(listPath, []byte(sb.String()), 0o644); err != nil {
		keepSegments = true
		return LiveConcatFailedWithSegmentsSaved, err
	}

	// Segment merging reuses the muxer timeout ceiling (default 30 minutes).
	mergeCtx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	// Merge into a staging file and rename only on success: writing straight to path
	// left a truncated product behind when the merge failed, which then looked like a
	// finished recording.
	stagingPath := path + ".staging"
	_ = os.Remove(stagingPath)
	cmd := exec.CommandContext(mergeCtx, muxer.FFMPEG, "-loglevel", "warning", "-y", "-f", "concat", "-safe", "0", "-i", listPath, "-c", "copy", stagingPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		_ = os.Remove(stagingPath)
		keepSegments = true
		return LiveConcatFailedWithSegmentsSaved, fmt.Errorf("直播分段合成失败（分段保留在 %s）: %w", sessionDir, err)
	}

	// Validate the product before the segments are dropped. ffmpeg can exit 0
	// after a truncated concat (a broken segment ends the output early), and the
	// sources are cleaned up right after — which lost the whole recording
	// silently. A far smaller output means the merge did not cover the input.
	var inputBytes int64
	for _, f := range segFiles {
		if st, err := os.Stat(f); err == nil {
			inputBytes += st.Size()
		}
	}
	outStat, statErr := os.Stat(stagingPath)
	if statErr != nil || outStat.Size() == 0 || (inputBytes > 0 && float64(outStat.Size()) < float64(inputBytes)*0.8) {
		var outBytes int64
		if statErr == nil {
			outBytes = outStat.Size()
		}
		_ = os.Remove(stagingPath)
		keepSegments = true
		return LiveConcatFailedWithSegmentsSaved, fmt.Errorf("直播分段合成产物异常（产物 %d 字节，源 %d 字节），分段已保留在 %s", outBytes, inputBytes, sessionDir)
	}
	// t72 ②：大小只是粗判据，再核对**媒体帧数**（concat -c copy 应当帧数守恒）。帧数掉了
	// 说明合并中途丢了段/内容，同样按失败处理并保留原分段与旧产物。
	if err := validateMergeProduct(stagingPath, plan); err != nil {
		_ = os.Remove(stagingPath)
		keepSegments = true
		return LiveConcatFailedWithSegmentsSaved, fmt.Errorf("%w，分段已保留在 %s", err, sessionDir)
	}
	if err := os.Rename(stagingPath, path); err != nil {
		_ = os.Remove(stagingPath)
		keepSegments = true
		return LiveConcatFailedWithSegmentsSaved, err
	}
	return LiveSuccess, nil
}

// errLiveWrite marks a local write failure: unlike a network interruption it
// is terminal and must not be retried (upstream LiveStreamWriteException).
type errLiveWrite struct{ err error }

func (e errLiveWrite) Error() string { return e.err.Error() }
func (e errLiveWrite) Unwrap() error { return e.err }

// reconnectBackoff grows from a few seconds up to a ceiling. Reconnection is
// unlimited (upstream v1.6.13): as long as the room is still broadcasting and
// the user has not cancelled, a lost network must not end the recording.
func reconnectBackoff(attempt int) time.Duration {
	d := time.Duration(attempt+1) * reconnectBaseBackoff
	if d > reconnectMaxBackoff {
		return reconnectMaxBackoff
	}
	return d
}

// streamToFile writes one segment to disk. A read that stops producing data for
// readStallTimeout is treated as an interruption: during a network black hole
// the connection neither resets nor EOFs, and the body read would otherwise
// block forever, hanging the whole recording. Bytes already written are
// returned even on error so the caller can keep them.
func streamToFile(ctx context.Context, url, segPath string) (int64, error) {
	reqCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	stall := time.AfterFunc(readStallTimeout, cancel)
	defer stall.Stop()

	req, err := http.NewRequestWithContext(reqCtx, "GET", url, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	req.Header.Set("Referer", "https://live.bilibili.com/")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	f, err := os.Create(segPath)
	if err != nil {
		return 0, errLiveWrite{err}
	}
	defer f.Close()

	buf := make([]byte, 1<<20) // 1MB buffer
	var offset int64
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			stall.Reset(readStallTimeout)
			if _, werr := f.Write(buf[:n]); werr != nil {
				return offset, errLiveWrite{werr}
			}
			offset += int64(n)
		}
		if err == io.EOF {
			break // stream ended
		}
		if err != nil {
			return offset, err
		}
	}
	return offset, nil
}

// trimFLVTail truncates a segment to its last complete FLV tag and reports how
// many trailing bytes were dropped. A network interruption or a cancellation
// leaves half a tag at the end of the current segment; the ffmpeg concat demuxer
// then aborts the entire merge at that point, losing the whole recording
// (upstream v1.6.13 "分段尾裁剪").
func trimFLVTail(path string) (int64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	// FLV header: "FLV" + version + flags + 4-byte data offset, then a 4-byte
	// PreviousTagSize0.
	if len(data) < 13 || string(data[:3]) != "FLV" {
		return 0, nil // not a container we understand: leave it untouched
	}
	offset := int(binary.BigEndian.Uint32(data[5:9]))
	if offset < 9 || offset+4 > len(data) {
		offset = 9
	}
	pos := offset + 4

	last := pos
	for pos+11 <= len(data) {
		dataSize := int(uint32(data[pos+1])<<16 | uint32(data[pos+2])<<8 | uint32(data[pos+3]))
		next := pos + 11 + dataSize + 4
		if next > len(data) {
			break // the tag (or its trailing size field) is incomplete
		}
		pos = next
		last = pos
	}
	if last >= len(data) {
		return 0, nil
	}
	if err := os.Truncate(path, int64(last)); err != nil {
		return 0, err
	}
	return int64(len(data) - last), nil
}

// staleSessions lists session directories under root other than the current one.
func staleSessions(root, current string) []string {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var stale []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if p := filepath.Join(root, e.Name()); p != current {
			stale = append(stale, e.Name())
		}
	}
	return stale
}

// SanitizeFileName produces a usable product name. The shared rules (invalid
// characters, Windows reserved names, trailing dots/spaces, length) live in
// util.GetValidFileName; only the "nothing usable left" fallback is specific to
// live recordings.
func SanitizeFileName(name string) string {
	if strings.TrimSpace(name) == "" {
		return "直播"
	}
	if s := strings.TrimSpace(util.GetValidFileName(name, "_", true)); s != "" {
		return s
	}
	return "直播"
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}
