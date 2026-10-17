package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/QC3284/BBDown-Go/internal/util"
)

// F2 批量输入（本仓新功能，见 docs/ROADMAP.md）：位置参数可以给多个 URL，也可以用
// --urls-file 从文件/stdin 读列表。语义与位置参数一致：顺序执行，单个失败不中断其余。

// readURLList 解析 URL 列表：每行一个目标；空行与 # 开头的注释行跳过；容忍 CRLF 与首尾空白。
func readURLList(r io.Reader) ([]string, error) {
	var out []string
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20) // 单行上限 1MB，防止畸形输入吃内存
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("读取 URL 列表失败: %w", err)
	}
	return out, nil
}

// collectTargets 汇总本次要下载的目标：位置参数（第一个起全部）+ --urls-file。
// urlsFile 为 "-" 时读 stdin；为 "" 时不读。两者都空则返回空切片，由调用方提示。
func collectTargets(args []string, urlsFile string, stdin io.Reader) ([]string, error) {
	targets := make([]string, 0, len(args)+8)
	targets = append(targets, args...)

	if urlsFile != "" {
		var (
			r    io.Reader = stdin
			file *os.File
		)
		if urlsFile != "-" {
			f, err := os.Open(urlsFile)
			if err != nil {
				return nil, fmt.Errorf("打开 --urls-file 失败: %w", err)
			}
			file = f
			r = f
			defer file.Close()
		}
		fromFile, err := readURLList(r)
		if err != nil {
			return nil, err
		}
		targets = append(targets, fromFile...)
	}
	return targets, nil
}

// runTargets 顺序执行多个目标：单个失败只记日志并继续（与 `sub check` 的批量语义一致），
// 返回失败数。ctx 取消则立即停下（返回已发生的失败数），不吞掉取消原因。
// batchDelayPerVideo 是批量下载「视频之间」的间隔（t80，--delay-per-video 的落地值）：
// 由 runDownload 在发请求前按 flag 解析写入；默认取 BBDownT 的 10 秒，0 = 不等待。
// 与 sub check 的 --delay-per-item 完全独立（那是另一个循环、另一个接缝）。
var batchDelayPerVideo = time.Duration(defaultDelayPerVideo) * time.Second

func runTargets(ctx context.Context, targets []string, run func(context.Context, string) error) int {
	failures := 0
	waited := false
	for i, target := range targets {
		if ctx.Err() != nil {
			break
		}
		// t80：目标之间按 batchDelayPerVideo 停顿；**首个目标不等待**（间隔是为了摊开连发，
		// 不是给第一个动作加延迟）。等待期间取消 → 记日志后退出，不把取消吞掉。
		if batchDelayPerVideo > 0 && waited {
			if err := batchSleep(ctx, batchDelayPerVideo); err != nil {
				util.LogWarn("批量下载在间隔期间被取消：%v", err)
				break
			}
		}
		if len(targets) > 1 {
			fmt.Printf("[%d/%d] %s\n", i+1, len(targets), target)
		}
		if err := run(ctx, target); err != nil {
			failures++
		}
		waited = true
	}
	return failures
}
