package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/QC3284/BBDown-Go/internal/entity"
	"github.com/QC3284/BBDown-Go/internal/util"
)

// ---- t80（吸收 BBDownT）：批量下载三件 ----
//
//   --delay-per-video <秒>：批量下载（多 URL / --urls-file / --download-all）时**视频之间**的
//     间隔。默认 10（BBDownT 默认）；0 = 不等待。与 sub check 的 --delay-per-item 完全独立：
//     两者是不同循环上的不同开关（各自的等待接缝也不共享）。
//   --download-all：把空间目标展开成「全部投稿」的逐条下载目标，并把清单写成 TXT（每行一个
//     https://www.bilibili.com/video/av<aid>，带 # 注释头，可直接回喂给 --urls-file 消费）。
//   --save-archives-to-file：**已有实现**（workflow 的 BBDown.archives + checkAidArchived/
//     ArchiveTracker），本任务只核对、不重复实现（见 t80 报告）。

// 默认间隔取自 BBDownT（DelayPerVideo = 10s）。
const defaultDelayPerVideo = 10

// batchSleep 是批量间隔的实现（用例替换成假等待：不真睡，只记录事件与时长）。
// 必须感知 ctx：等待期间取消要原样上抛（与 t71 的 subCheckSleep 同一纪律，但**各自独立**）。
var batchSleep = func(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// parseDelayPerVideo 校验 --delay-per-video（秒）：0~600（0 = 不等待，与 BBDownT 一致）。
func parseDelayPerVideo(seconds int) (time.Duration, error) {
	if seconds < 0 || seconds > 600 {
		return 0, fmt.Errorf("--delay-per-video %d 无效：取值范围 0-600 秒（默认 %d；0 = 不等待）",
			seconds, defaultDelayPerVideo)
	}
	return time.Duration(seconds) * time.Second, nil
}

// ---- --download-all：空间清单生成与消费 ----

// spaceLister 是空间枚举的接缝：真实实现走 fetcher 的全量扫描，用例注入假 fetcher。
// 返回该 UP 主的全部投稿页（顺序即下载顺序）。
type spaceLister func(ctx context.Context, mid string) ([]entity.Page, error)

// spacePageLister 由 runDownload 在发请求前接上真实实现；未接线时 --download-all 会明确报错。
var spacePageLister spaceLister

// spaceMidOf 从目标里认出空间/UP 主形态：
//   - https://space.bilibili.com/<mid>[/video][?…]
//   - mid:<mid>（我们内部清单/订阅用的写法）
//
// 其它目标返回 ok=false（原样保留，交给普通下载路径）。
func spaceMidOf(target string) (string, bool) {
	t := strings.TrimSpace(target)
	if strings.HasPrefix(t, "mid:") {
		mid := strings.TrimSpace(strings.TrimPrefix(t, "mid:"))
		if mid != "" && isAllDigits(mid) {
			return mid, true
		}
		return "", false
	}
	const host = "space.bilibili.com/"
	idx := strings.Index(t, host)
	if idx < 0 {
		return "", false
	}
	rest := t[idx+len(host):]
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
		rest = rest[:i]
	}
	if rest != "" && isAllDigits(rest) {
		return rest, true
	}
	return "", false
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// spaceListLine 是清单里的一行（可直接回喂 --urls-file）。
func spaceListLine(aid string) string {
	return "https://www.bilibili.com/video/av" + aid + "/"
}

// buildDownloadAllTargets 把目标列表里的空间目标展开成逐个投稿的下载目标，并写 TXT 清单。
//
// 设计取舍：
//   - 非空间目标原样保留（--download-all 可以与普通 URL 混用）；
//   - 清单只写**展开出来的**投稿（每行一个 URL，带 # 注释头），因此 --urls-file 能直接消费它，
//     第二次运行不需要重新扫空间（--download-all 的 TXT 就是「清单生成与消费」的闭环）；
//   - 一个空间都没认出时不是错误（此时 --download-all 等价于普通批量）；但认出了空间却没接线
//     lister（或 lister 报错）必须明确失败，不能静默少下。
func buildDownloadAllTargets(ctx context.Context, targets []string, lister spaceLister, listPath string) ([]string, error) {
	var out []string
	var lines []string
	expanded := 0
	for _, t := range targets {
		mid, ok := spaceMidOf(t)
		if !ok {
			out = append(out, t)
			continue
		}
		if lister == nil {
			return nil, fmt.Errorf("--download-all 需要空间枚举：内部未接线（这是构建问题，请报告）")
		}
		pages, err := lister(ctx, mid)
		if err != nil {
			return nil, fmt.Errorf("导出 UP 主 %s 的投稿清单失败: %w", mid, err)
		}
		for _, p := range pages {
			out = append(out, "av"+p.Aid)
			lines = append(lines, spaceListLine(p.Aid))
		}
		expanded++
	}
	if expanded > 0 && listPath != "" && len(lines) > 0 {
		if err := writeSpaceListFile(listPath, lines); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// writeSpaceListFile 写空间清单（每行一个 URL；文件名固定、内容可被 --urls-file 直接吃）。
func writeSpaceListFile(path string, lines []string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("创建清单目录失败: %w", err)
	}
	var sb strings.Builder
	sb.WriteString("# BBDown 空间投稿清单（--download-all 生成；可直接用 --urls-file 消费）\n")
	for _, line := range lines {
		sb.WriteString(line)
		sb.WriteString("\n")
	}
	if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
		return fmt.Errorf("写清单 %s 失败: %w", path, err)
	}
	util.Log("已导出 UP 主投稿清单: %s（%d 条）", path, len(lines))
	return nil
}

// defaultSpaceListPath 是清单的默认落点：work-dir 优先，其次 appDir（与历史侧车文件同一惯例）。
func defaultSpaceListPath(workDir, mid string) string {
	base := workDir
	if base == "" {
		base = util.ExecutableDir()
	}
	name := "space-" + mid + ".txt"
	if mid == "" {
		name = "space-all.txt"
	}
	return filepath.Join(base, name)
}

// spaceListPathFor 按目标集合决定清单路径：单个空间 → space-<mid>.txt；多个 → space-all.txt。
func spaceListPathFor(targets []string, workDir string) string {
	var mids []string
	for _, t := range targets {
		if mid, ok := spaceMidOf(t); ok {
			mids = append(mids, mid)
		}
	}
	sort.Strings(mids)
	if len(mids) == 1 {
		return defaultSpaceListPath(workDir, mids[0])
	}
	if len(mids) > 1 {
		return defaultSpaceListPath(workDir, "")
	}
	return ""
}
