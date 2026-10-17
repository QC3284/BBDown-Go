package download

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QC3284/BBDown-Go/internal/entity"
)

func TestFormatSavePath(t *testing.T) {
	page := entity.Page{
		Index:     3,
		Aid:       "170001",
		Cid:       "279786",
		Title:     "第三话",
		OwnerName: "UP主",
		OwnerMid:  "12345",
		PubTime:   1500000000,
	}
	video := &entity.Video{ID: "80", Dfn: "1080P 高清", Codecs: "AVC", Res: "1920x1080", FPS: "30", Bandwidth: 3000}
	audio := &entity.Audio{ID: "30280", Codecs: "M4A", Bandwidth: 192}

	got := FormatSavePath("<videoTitle>/[P<pageNumberWithZero>]<pageTitle>", "标题", video, audio, page, 12, "WEB", 1500000000)
	want := "标题/[P03]第三话"
	if got != want {
		t.Errorf("FormatSavePath = %q, want %q", got, want)
	}

	dateStr := time.Unix(1500000000, 0).Local().Format("2006-01-02_15-04-05")
	got = FormatSavePath("<bvid>_<ownerName>_<ownerMid>_<dfn>_<videoCodecs>_<audioCodecs>_<videoBandwidth>_<audioBandwidth>_<publishDate>", "t", video, audio, page, 1, "WEB", 1500000000)
	want = "BV17x411w7KC_UP主_12345_1080P 高清_AVC_M4A_3000_192_" + dateStr
	if got != want {
		t.Errorf("FormatSavePath(placeholders) = %q, want %q", got, want)
	}
}

func TestFormatSavePathPageNumberPadding(t *testing.T) {
	page := entity.Page{Index: 7}
	got := FormatSavePath("<pageNumberWithZero>", "t", nil, nil, page, 100, "WEB", 0)
	if got != "007" {
		t.Errorf("padding = %q, want 007", got)
	}
	got = FormatSavePath("<pageNumberWithZero>", "t", nil, nil, page, 9, "WEB", 0)
	if got != "7" {
		t.Errorf("padding = %q, want 7", got)
	}
}

func TestSortVideoTracksPriority(t *testing.T) {
	tracks := []entity.Video{
		{ID: "1", Dfn: "1080P 高清", Codecs: "HEVC", Bandwidth: 3000},
		{ID: "2", Dfn: "8K 超高清", Codecs: "AVC", Bandwidth: 8000},
		{ID: "3", Dfn: "1080P 高清", Codecs: "AVC", Bandwidth: 2000},
	}
	// Priority: HEVC first; among AVC, 1080P(0) outranks 8K(1).
	encoding := map[string]int{"HEVC": 0, "AVC": 1}
	dfn := map[string]int{"1080P 高清": 0, "8K 超高清": 1}
	sorted := SortVideoTracks(tracks, dfn, encoding, false)
	if sorted[0].ID != "1" || sorted[1].ID != "3" || sorted[2].ID != "2" {
		t.Errorf("sorted ids = %s,%s,%s", sorted[0].ID, sorted[1].ID, sorted[2].ID)
	}

	// Same priorities: id descending as tiebreak.
	tracks2 := []entity.Video{
		{ID: "16", Dfn: "360P 流畅", Codecs: "AVC", Bandwidth: 300},
		{ID: "32", Dfn: "480P 清晰", Codecs: "AVC", Bandwidth: 300},
	}
	sorted2 := SortVideoTracks(tracks2, nil, nil, false)
	if sorted2[0].ID != "32" {
		t.Errorf("id desc tiebreak failed: first = %s", sorted2[0].ID)
	}
}

func TestSortAudioTracksShortCodecs(t *testing.T) {
	tracks := []entity.Audio{
		{ID: "1", Codecs: "E-AC-3", Bandwidth: 448},
		{ID: "2", Codecs: "M4A", Bandwidth: 192},
	}
	encoding := map[string]int{"EAC3": 0, "M4A": 1}
	sorted := SortAudioTracks(tracks, encoding, false)
	if sorted[0].ID != "1" {
		t.Errorf("first = %s, want E-AC-3 track", sorted[0].ID)
	}
}

// ---- t68：aria2c 预分配出来的残件不再被当成完整文件跳过（吸收上游 1.7.4）----

// TestExistingDownloadComplete 钉住判定顺序：大小相等只是**必要条件**，旁边还有 aria2 控制
// 文件就说明这是预分配出来的残件——不能按完整跳过（否则后续混流会拿半个视频去 mux）。
//
// 变异验证：把 existingDownloadComplete 里的 hasAria2ControlFile 判定去掉（退回只比大小）→ 本用例红。
func TestExistingDownloadComplete(t *testing.T) {
	dir := t.TempDir()
	complete := filepath.Join(dir, "complete.bin")
	incomplete := filepath.Join(dir, "preallocated.bin")
	short := filepath.Join(dir, "short.bin")
	missing := filepath.Join(dir, "missing.bin")

	for path, size := range map[string]int{complete: 10, incomplete: 10, short: 5} {
		if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// 预分配残件：长度已等于远端总长，但 aria2c 的控制文件还在。
	if err := os.WriteFile(aria2ControlPath(incomplete), []byte("control"), 0o644); err != nil {
		t.Fatal(err)
	}

	if !existingDownloadComplete(complete, 10) {
		t.Error("大小相等且没有控制文件 → 应当判完整（行为与改前一致）")
	}
	if existingDownloadComplete(incomplete, 10) {
		t.Error("大小相等但存在 aria2 控制文件 → 必须判未完成（t68 的核心）")
	}
	if existingDownloadComplete(short, 10) {
		t.Error("大小不符不该判完整")
	}
	if existingDownloadComplete(missing, 10) {
		t.Error("文件不存在不该判完整")
	}
	if existingDownloadComplete(complete, 0) {
		t.Error("远端长度未知（<=0）不该判完整")
	}

	// 控制文件消失后恢复「完整」：证明判定确实是由控制文件驱动的，不是别的副作用。
	if err := os.Remove(aria2ControlPath(incomplete)); err != nil {
		t.Fatal(err)
	}
	if !existingDownloadComplete(incomplete, 10) {
		t.Error("控制文件清掉之后应当判完整")
	}
}

// newAria2TestServer 造一个支持 HEAD 的假 CDN：body 由调用方给，hits 记录真正的 GET 次数。
func newAria2TestServer(t *testing.T, body []byte) (*httptest.Server, *int32) {
	t.Helper()
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.Header().Set("Accept-Ranges", "bytes")
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}
		atomic.AddInt32(&hits, 1)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// TestDownloadFileRedownloadsPreallocatedWithControlFile 是行为级用例（t68 ①）：
// 磁盘上已有**等于远端总长**的文件 + 遗留的 .aria2 控制文件（aria2c 预分配后中断的形态）
// → 必须真的重新下载，而不是「大小相等，跳过」。成功之后控制文件要清掉（否则下一次又会白重下）。
//
// 变异验证：撤掉 existingDownloadComplete 里的控制文件检查 → 本用例红（GET 次数为 0）。
func TestDownloadFileRedownloadsPreallocatedWithControlFile(t *testing.T) {
	body := []byte("0123456789")
	srv, hits := newAria2TestServer(t, body)
	dest := filepath.Join(t.TempDir(), "out.bin")
	// 预分配残件：长度 == 远端总长，内容其实是零；控制文件还在。
	if err := os.WriteFile(dest, make([]byte, len(body)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(aria2ControlPath(dest), []byte("control"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := DownloadConfig{Client: newTestClient(), RetryCount: 1, RetryDelayMs: 1}
	if err := DownloadFile(context.Background(), srv.URL+"/a.m4s", dest, cfg); err != nil {
		t.Fatalf("残件应当被重新下载：%v", err)
	}
	if atomic.LoadInt32(hits) == 0 {
		t.Error("大小相等但存在控制文件时必须重新下载，实际一次 GET 都没发（被当成完整文件跳过了）")
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("产物应当是本次下载的内容，实际 %q", got)
	}
	if _, err := os.Stat(aria2ControlPath(dest)); !os.IsNotExist(err) {
		t.Errorf("成功重写之后应当清掉遗留的 aria2 控制文件（否则下次还会白重下）：%v", err)
	}
}

// TestDownloadFileSkipsCompleteWithoutControlFile 钉住「真完整照旧跳过」（t68 ③）：
// 没有控制文件、大小相等 → 不发 GET、直接返回。
func TestDownloadFileSkipsCompleteWithoutControlFile(t *testing.T) {
	body := []byte("0123456789")
	srv, hits := newAria2TestServer(t, body)
	dest := filepath.Join(t.TempDir(), "out.bin")
	if err := os.WriteFile(dest, body, 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := DownloadConfig{Client: newTestClient(), RetryCount: 1, RetryDelayMs: 1}
	if err := DownloadFile(context.Background(), srv.URL+"/a.m4s", dest, cfg); err != nil {
		t.Fatalf("完整文件应当直接跳过：%v", err)
	}
	if n := atomic.LoadInt32(hits); n != 0 {
		t.Errorf("真完整的文件不该重新下载，实际发了 %d 次 GET", n)
	}
	if got, _ := os.ReadFile(dest); !bytes.Equal(got, body) {
		t.Errorf("跳过时不该改动产物：%q", got)
	}
}

// TestFailedDownloadKeepsAria2ControlFile 钉住 t68 ②：下载**失败**时控制文件必须留着
// （它是 aria2c 续传的凭证，删了就得从头再下）。
func TestFailedDownloadKeepsAria2ControlFile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodHead {
			w.Header().Set("Content-Length", "10")
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "fail.bin")
	if err := os.WriteFile(aria2ControlPath(dest), []byte("control"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := DownloadConfig{Client: newTestClient(), RetryCount: 1, RetryDelayMs: 1}
	if err := DownloadFile(context.Background(), srv.URL+"/a.m4s", dest, cfg); err == nil {
		t.Fatal("服务器 500 时应当报错")
	}
	if _, err := os.Stat(aria2ControlPath(dest)); err != nil {
		t.Errorf("失败后控制文件必须保留（续传凭证）：%v", err)
	}
}
