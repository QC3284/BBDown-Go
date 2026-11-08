package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestMergeWithConfig(t *testing.T) {
	aliasMap := map[string]string{"--dfn-priority": "dfn", "-q": "dfn", "--cookie": "cookie", "-c": "cookie", "--multi-thread": "multi"}
	boolFlags := map[string]bool{"multi": true, "cookie": false, "dfn": false}

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "BBDown.config")
	lines := []string{
		"# comment",
		"BV1xx411c7mD", // config URL (honored when the CLI has no URL)
		"--cookie abc123",
		"--dfn-priority 8K,1080P",
	}
	if err := os.WriteFile(cfgPath, []byte(joinLines(lines)), 0o644); err != nil {
		t.Fatal(err)
	}

	// CLI has a URL: config URL dropped; explicit --cookie overrides config.
	merged, err := MergeWithConfig([]string{"--config-file", cfgPath, "--cookie", "cli-cookie", "av170001"}, aliasMap, boolFlags)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"--config-file", cfgPath, "--cookie", "cli-cookie", "av170001", "--dfn-priority", "8K,1080P"}
	if !reflect.DeepEqual(merged, want) {
		t.Fatalf("merged = %v, want %v", merged, want)
	}
}

func TestUrlLikeToken(t *testing.T) {
	if !urlLikeToken.MatchString("av170001") {
		t.Fatal("av170001 should match urlLikeToken")
	}
	if !urlLikeToken.MatchString("BV1xx411c7mD") {
		t.Fatal("BV should match urlLikeToken")
	}
	if urlLikeToken.MatchString("not-a-url") {
		t.Fatal("plain text should not match")
	}
}

func TestIsSubCommandInvocation(t *testing.T) {
	aliasMap := map[string]string{"--cookie": "cookie", "-c": "cookie", "--limit": "limit", "--config-file": "configfile"}
	boolFlags := map[string]bool{"limit": true, "cookie": false}
	if !IsSubCommandInvocation([]string{"watchlater", "--limit", "5"}, aliasMap, boolFlags) {
		t.Fatal("watchlater should be a subcommand invocation")
	}
	if !IsSubCommandInvocation([]string{"--config-file", "x.config", "sub", "list"}, aliasMap, boolFlags) {
		t.Fatal("sub after value-consuming option should be detected")
	}
	if IsSubCommandInvocation([]string{"av170001"}, aliasMap, boolFlags) {
		t.Fatal("plain download should not be a subcommand invocation")
	}
}

func joinLines(lines []string) string {
	out := ""
	for _, l := range lines {
		out += l + "\n"
	}
	return out
}

func TestMergeWithConfigKeepsURLWhenOptionValueLooksLikeURL(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "BBDown.config")
	if err := os.WriteFile(cfgPath, []byte("--encoding-priority\nhevc,avc\n# 目标\nhttps://www.bilibili.com/video/BV1xx411c7mD\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	aliasMap := map[string]string{
		"--config-file":  "config-file",
		"--aria2c-proxy": "aria2c-proxy",
		"--work-dir":     "work-dir",
		"--audio-only":   "audio-only",
	}
	boolFlags := map[string]bool{"audio-only": true}

	// Both option values look like a target (a proxy URL and "av123"), but both
	// are consumed by their option, so the config URL must survive (upstream RF-7).
	args := []string{"--config-file", cfgPath, "--aria2c-proxy", "http://127.0.0.1:1080", "--work-dir", "av123"}
	merged, err := MergeWithConfig(args, aliasMap, boolFlags)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(merged, " "), "BV1xx411c7mD") {
		t.Errorf("config URL was dropped because an option value looked like a URL: %v", merged)
	}

	// A real CLI target must still suppress the config one.
	merged, err = MergeWithConfig([]string{"--config-file", cfgPath, "BV1yy411c7mD", "--audio-only"}, aliasMap, boolFlags)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(merged, " "), "BV1xx411c7mD") {
		t.Errorf("config URL should be dropped when the CLI already carries one: %v", merged)
	}
}

// TestMergeWithConfigKeepsTrailingURLWhenOptionOverridden pins the upstream
// 1.7.9 ConfigParser fix: when the CLI overrides an option that also appears in
// BBDown.config, only that option's own value may be skipped — the config
// file's trailing positional URL must survive. The old "collect tokens until
// the next known option" loop consumed it, so a CLI override silently dropped
// the target and the run failed with "请提供视频地址".
//
// 变异验证：把 MergeWithConfig 里的 optionValueCount 换回「收集到下一个已知选项」
// 的旧循环 → 下列 URL 存活断言变红。
func TestMergeWithConfigKeepsTrailingURLWhenOptionOverridden(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "BBDown.config")
	content := "--cookie\nSESSDATA=from_config\nhttps://www.bilibili.com/video/BV1xx411c7mD\n"
	if err := os.WriteFile(cfgPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	aliasMap := map[string]string{
		"--config-file": "config-file",
		"--cookie":      "cookie",
		"-c":            "cookie",
		"--debug":       "debug",
	}
	boolFlags := map[string]bool{"config-file": false, "cookie": false, "debug": true}
	const configURL = "https://www.bilibili.com/video/BV1xx411c7mD"

	// 1) 取值选项被命令行覆盖：配置里的取值跳过，但后面的 URL 必须留下。
	merged, err := MergeWithConfig([]string{"--config-file", cfgPath, "--cookie", "SESSDATA=from_cli"}, aliasMap, boolFlags)
	if err != nil {
		t.Fatal(err)
	}
	if joined := strings.Join(merged, " "); !strings.Contains(joined, configURL) {
		t.Fatalf("CLI 覆盖 --cookie 后配置 URL 被吞：%v", merged)
	}
	// 覆盖值本身仍必须生效（不能把配置值追加回来压掉命令行）。
	if !strings.Contains(strings.Join(merged, " "), "SESSDATA=from_cli") ||
		strings.Contains(strings.Join(merged, " "), "SESSDATA=from_config") {
		t.Fatalf("命令行覆盖值没有生效：%v", merged)
	}

	// 2) 布尔选项被命令行覆盖：布尔只吃可选的 true/false，URL 不能被当成它的值。
	merged, err = MergeWithConfig([]string{"--config-file", cfgPath, "--debug"}, aliasMap, boolFlags)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(merged, " "), configURL) {
		t.Fatalf("CLI 覆盖布尔选项后配置 URL 被吞：%v", merged)
	}

	// 3) 等号写法的覆盖同样只跳过取值，URL 留下。
	merged, err = MergeWithConfig([]string{"--config-file", cfgPath, "--cookie=SESSDATA=from_cli"}, aliasMap, boolFlags)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(merged, " "), configURL) {
		t.Fatalf("等号写法覆盖后配置 URL 被吞：%v", merged)
	}

	// 4) CLI 自带 URL 时仍按既有规则丢弃配置 URL（这次修复不改变该语义）。
	merged, err = MergeWithConfig([]string{"--config-file", cfgPath, "--cookie", "x", "av170001"}, aliasMap, boolFlags)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(merged, " "), configURL) {
		t.Fatalf("CLI 已带 URL 时配置 URL 应被丢弃：%v", merged)
	}
}
