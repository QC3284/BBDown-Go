package workflow

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/QC3284/BBDown-Go/internal/config"
	"github.com/QC3284/BBDown-Go/internal/download"
	"github.com/QC3284/BBDown-Go/internal/entity"
	"github.com/QC3284/BBDown-Go/internal/parser"
	"github.com/QC3284/BBDown-Go/internal/util"
)

// TestProductSkipSweepsStaleMuxTemps 端到端钉住「已存在, 跳过下载」分支的产物级残留清理：
// 首轮真下载产出 out.m4a；随后手工放入 <base>.muxing-<n>.m4a（模拟上次进程被硬杀时
// 混流临时件来不及删除）；跳过分支必须回收它，且不得误删其它形态的兄弟文件。
//
// 变异验证：删掉 handleExistingProduct 里的 download.CleanupProductResidues(...) → 本用例红。
func TestProductSkipSweepsStaleMuxTemps(t *testing.T) {
	t.Chdir(t.TempDir())

	body := make([]byte, 32<<10)
	for i := range body {
		body[i] = byte(i % 241)
	}
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "a.m4s", time.Time{}, bytes.NewReader(body))
	}))
	defer cdn.Close()

	playTmpl := "{'code':0,'data':{'dash':{'duration':100,'audio':[{'id':30280,'codecid':0,'codecs':'mp4a.40.2','bandwidth':64000,'base_url':'%s/a.m4s'}]}}}"
	playURL := strings.ReplaceAll(fmt.Sprintf(playTmpl, cdn.URL), "'", string('"'))
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(playURL))
	}))
	defer srv.Close()

	client := util.NewHTTPClient(func() bool { return true }, func() string { return "" }, nil)
	cfg := config.DefaultMyOption()
	cfg.AudioOnly = true
	cfg.SkipCover = true
	cfg.SkipSubtitle = true
	cfg.SkipMux = true
	cfg.ForceReplaceHost = false
	cfg.AllowPcdn = true
	cfg.RetryDelay = 1
	cfg.WriteM3U = false
	wf := New(cfg, client)

	pcfg := config.DefaultAppSettings()
	pcfg.Host = strings.TrimPrefix(srv.URL, "https://")
	pcfg.TvHost = pcfg.Host
	pcfg.EpHost = pcfg.Host
	pcfg.Wbi = "test_wbi_key"
	p := parser.NewParser(client, pcfg)

	page := entity.Page{Index: 1, Aid: "1", Cid: "2", Title: "t", Dur: 100}
	vInfo := &entity.VInfo{Title: "t", PagesInfo: []entity.Page{page}}

	run := func() (bool, string) {
		var ok bool
		out := captureStdout(t, func() {
			ok = wf.downloadOnePage(context.Background(), p, page, vInfo,
				[]entity.Page{page}, "av1", "out.m4a", "web",
				map[string]int{}, map[string]int{}, "", "",
				download.DownloadConfig{Client: client, RetryCount: 1, RetryDelayMs: 1}, false, nil)
		})
		return ok, out
	}

	ok, out := run()
	if !ok {
		t.Fatalf("首轮下载应当成功，输出: %s", out)
	}
	if _, err := os.Stat("out.m4a"); err != nil {
		t.Fatalf("首轮下载应当产出 out.m4a：%v", err)
	}

	write := func(name string) {
		t.Helper()
		if err := os.WriteFile(name, []byte("stale"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("out.muxing-123456789.m4a") // 本产物残留 → 跳过分支应删
	write("out.muxing-abc.m4a")       // 非数字序号段 → 不是本仓命名，保留
	write("other.muxing-1.m4a")       // 其它产物 → 保留

	ok, out = run()
	if !ok {
		t.Fatalf("产物已存在时应判成功（跳过下载），输出: %s", out)
	}
	if !strings.Contains(out, "已存在, 跳过下载") {
		t.Fatalf("第二轮应当走「已存在, 跳过下载」分支，实际输出: %s", out)
	}
	if _, err := os.Stat("out.muxing-123456789.m4a"); !os.IsNotExist(err) {
		t.Errorf("跳过分支应回收上次硬杀留下的混流临时件（err=%v）", err)
	}
	for _, keep := range []string{"out.muxing-abc.m4a", "other.muxing-1.m4a"} {
		if _, err := os.Stat(keep); err != nil {
			t.Errorf("不应删除 %s: %v", keep, err)
		}
	}
}
