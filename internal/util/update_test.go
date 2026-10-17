package util

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// t81 的离线用例：假更新服务器（三态）+ 归档解析 + 原子替换/回滚 + Windows 语义。
// 全程不联网：API 地址经 SelfUpdateConfig.APIURL 注入，资产由 httptest 直接吐出。

// tarGzWith 打一个只含给定条目的 tar.gz（unix 资产形态）。
func tarGzWith(t *testing.T, name string, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// zipWith 打一个只含给定条目的 zip（windows 资产形态）。
func zipWith(t *testing.T, name string, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// fakeUpdateServer 是最小的 Releases API 假服务器：/releases/latest 返回给定 JSON，/asset 返回资产字节。
type fakeUpdateServer struct {
	srv        *httptest.Server
	assetHits  int
	releaseTag string
	assetBytes []byte
}

func newFakeUpdateServer(t *testing.T, tag string, assetName string, assetBytes []byte, digest string, downloadURLOverride string) *fakeUpdateServer {
	t.Helper()
	f := &fakeUpdateServer{releaseTag: tag, assetBytes: assetBytes}
	mux := http.NewServeMux()
	mux.HandleFunc("/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		dl := downloadURLOverride
		if dl == "" {
			dl = f.srv.URL + "/asset"
		}
		rel := map[string]any{"tag_name": tag, "assets": []map[string]any{{
			"name":                 assetName,
			"size":                 len(assetBytes),
			"digest":               digest,
			"browser_download_url": dl,
		}}}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(rel); err != nil {
			t.Errorf("encode: %v", err)
		}
	})
	mux.HandleFunc("/asset", func(w http.ResponseWriter, r *http.Request) {
		f.assetHits++
		_, _ = w.Write(f.assetBytes)
	})
	// 必须用 TLS：生产路径只接受 https 下载地址（http 假服务器会被安全校验挡掉——那是正确行为）。
	f.srv = httptest.NewTLSServer(mux)
	t.Cleanup(f.srv.Close)
	// 主机白名单是**生产安全边界**，不为了测试放宽：这里把假服务器的主机临时登记进白名单，
	// 用例结束立刻撤销（与 fetcher 的 spaceSearchAPI 同一套「包级变量当接缝」做法）。
	host := testHostOf(t, f.srv.URL)
	officialDownloadHosts[host] = true
	t.Cleanup(func() { delete(officialDownloadHosts, host) })
	return f
}

// testHostOf 取出假服务器 URL 的主机名（用例里把它临时登记进下载白名单）。
func testHostOf(t *testing.T, raw string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("解析假服务器地址失败: %v", err)
	}
	return u.Hostname()
}

func writeExecutable(t *testing.T, dir string) string {
	t.Helper()
	target := filepath.Join(dir, "BBDown")
	if err := os.WriteFile(target, []byte("old-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	return target
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// assertNoTempLeftovers 断言同目录下没有残留的临时文件/旧文件。
func assertNoTempLeftovers(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".bbdown-selfupdate-") || strings.HasSuffix(e.Name(), ".old") {
			t.Errorf("残留文件：%s", e.Name())
		}
	}
}

// 三态之一：已是最新 —— 不下载、不动文件。
func TestSelfUpdateAlreadyLatest(t *testing.T) {
	dir := t.TempDir()
	target := writeExecutable(t, dir)
	fake := newFakeUpdateServer(t, "v3.0.1", "BBDown_linux_amd64.tar.gz", tarGzWith(t, "BBDown", []byte("new-binary")), "sha256:"+sha256Hex([]byte("x")), "")

	res, err := SelfUpdate(context.Background(), SelfUpdateConfig{
		CurrentVersion: "3.0.1", GOOS: "linux", GOARCH: "amd64",
		ExecutablePath: target, APIURL: fake.srv.URL + "/releases/latest", Client: fake.srv.Client(),
	})
	if err != nil {
		t.Fatalf("已是最新不该报错：%v", err)
	}
	if res.Updated {
		t.Error("已是最新却报告 Updated=true")
	}
	if fake.assetHits != 0 {
		t.Errorf("已是最新不该下载资产（assetHits=%d）", fake.assetHits)
	}
	if got := readFile(t, target); got != "old-binary" {
		t.Errorf("原文件被改动：%q", got)
	}
}

// 三态之二：有新版本 —— 下载 + 校验 + 原子替换。
func TestSelfUpdateDownloadsVerifiesAndReplaces(t *testing.T) {
	dir := t.TempDir()
	target := writeExecutable(t, dir)
	archive := tarGzWith(t, "BBDown", []byte("new-binary"))
	fake := newFakeUpdateServer(t, "v3.0.2", "BBDown_linux_amd64.tar.gz", archive, "sha256:"+sha256Hex(archive), "")

	res, err := SelfUpdate(context.Background(), SelfUpdateConfig{
		CurrentVersion: "3.0.1", GOOS: "linux", GOARCH: "amd64",
		ExecutablePath: target, APIURL: fake.srv.URL + "/releases/latest", Client: fake.srv.Client(),
	})
	if err != nil {
		t.Fatalf("自更新失败：%v", err)
	}
	if !res.Updated || res.FromVersion != "3.0.1" || res.ToVersion != "3.0.2" {
		t.Errorf("结果不符：%+v", res)
	}
	if res.AssetName != "BBDown_linux_amd64.tar.gz" || res.DownloadedBytes != int64(len(archive)) {
		t.Errorf("资产信息不符：%+v", res)
	}
	if got := readFile(t, target); got != "new-binary" {
		t.Errorf("替换后的内容不符：%q", got)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o100 == 0 {
		t.Errorf("替换后丢了可执行位：%v", info.Mode())
	}
	assertNoTempLeftovers(t, dir)
}

// 三态之三：校验失败 —— 中止且原文件一个字节都不动。
func TestSelfUpdateRejectsBadDigest(t *testing.T) {
	dir := t.TempDir()
	target := writeExecutable(t, dir)
	archive := tarGzWith(t, "BBDown", []byte("new-binary"))
	fake := newFakeUpdateServer(t, "v3.0.2", "BBDown_linux_amd64.tar.gz", archive, "sha256:"+strings.Repeat("0", 64), "")

	_, err := SelfUpdate(context.Background(), SelfUpdateConfig{
		CurrentVersion: "3.0.1", GOOS: "linux", GOARCH: "amd64",
		ExecutablePath: target, APIURL: fake.srv.URL + "/releases/latest", Client: fake.srv.Client(),
	})
	if err == nil || !strings.Contains(err.Error(), "SHA256 校验失败") {
		t.Fatalf("摘要不匹配应失败并说明原因，实际 %v", err)
	}
	if got := readFile(t, target); got != "old-binary" {
		t.Errorf("校验失败却改动了原文件：%q", got)
	}
	assertNoTempLeftovers(t, dir)
}

// 没有摘要 = 没有校验依据：同样必须中止（不带摘要照装等于放弃校验）。
func TestSelfUpdateRejectsMissingDigest(t *testing.T) {
	dir := t.TempDir()
	target := writeExecutable(t, dir)
	archive := tarGzWith(t, "BBDown", []byte("new-binary"))
	fake := newFakeUpdateServer(t, "v3.0.2", "BBDown_linux_amd64.tar.gz", archive, "", "")

	_, err := SelfUpdate(context.Background(), SelfUpdateConfig{
		CurrentVersion: "3.0.1", GOOS: "linux", GOARCH: "amd64",
		ExecutablePath: target, APIURL: fake.srv.URL + "/releases/latest", Client: fake.srv.Client(),
	})
	if err == nil || !strings.Contains(err.Error(), "缺少 SHA256 摘要") {
		t.Fatalf("缺摘要应失败，实际 %v", err)
	}
	if fake.assetHits != 0 {
		t.Errorf("缺摘要时不该下载资产（assetHits=%d）", fake.assetHits)
	}
	if got := readFile(t, target); got != "old-binary" {
		t.Errorf("原文件被改动：%q", got)
	}
}

// 只从官方主机下载：假服务器把下载地址指到别处 → 拒绝（且不发起下载）。
func TestSelfUpdateRejectsForeignDownloadHost(t *testing.T) {
	dir := t.TempDir()
	target := writeExecutable(t, dir)
	archive := tarGzWith(t, "BBDown", []byte("new-binary"))
	fake := newFakeUpdateServer(t, "v3.0.2", "BBDown_linux_amd64.tar.gz", archive, "sha256:"+sha256Hex(archive), "https://evil.example.com/BBDown.tar.gz")

	_, err := SelfUpdate(context.Background(), SelfUpdateConfig{
		CurrentVersion: "3.0.1", GOOS: "linux", GOARCH: "amd64",
		ExecutablePath: target, APIURL: fake.srv.URL + "/releases/latest", Client: fake.srv.Client(),
	})
	if err == nil || !strings.Contains(err.Error(), "不在官方白名单") {
		t.Fatalf("非官方下载主机应被拒绝，实际 %v", err)
	}
	if got := readFile(t, target); got != "old-binary" {
		t.Errorf("原文件被改动：%q", got)
	}
}

// 生产默认 API 必须就是官方仓库（防止「只从官方拉取」被静默改掉）。
func TestOfficialReleaseAPIPointsAtOfficialRepo(t *testing.T) {
	want := "https://api.github.com/repos/QC3284/BBDown-Go/releases/latest"
	if officialReleaseAPI != want {
		t.Errorf("officialReleaseAPI = %q，want %q", officialReleaseAPI, want)
	}
	for _, tc := range []struct{ goos, goarch, want string }{
		{"linux", "amd64", "BBDown_linux_amd64.tar.gz"},
		{"windows", "amd64", "BBDown_windows_amd64.zip"},
		{"darwin", "arm64", "BBDown_darwin_arm64.tar.gz"},
	} {
		got, err := selfUpdateAssetName(tc.goos, tc.goarch)
		if err != nil || got != tc.want {
			t.Errorf("资产命名与 release.yml 不一致：%s/%s → %q（%v），want %q", tc.goos, tc.goarch, got, err, tc.want)
		}
	}
	if _, err := selfUpdateAssetName("plan9", "amd64"); err == nil {
		t.Error("不支持的平台应有明确错误")
	}
}

func TestValidateDownloadURLAndDigest(t *testing.T) {
	for _, raw := range []string{
		"https://github.com/QC3284/BBDown-Go/releases/download/v1.0.0/BBDown_linux_amd64.tar.gz",
		"https://objects.githubusercontent.com/x",
		"https://release-assets.githubusercontent.com/y",
	} {
		if err := validateDownloadURL(raw); err != nil {
			t.Errorf("%q 应被接受：%v", raw, err)
		}
	}
	for _, raw := range []string{"http://github.com/x", "https://evil.example.com/x", "https://github.com.evil.example/x", "not-a-url"} {
		if err := validateDownloadURL(raw); err == nil {
			t.Errorf("%q 应被拒绝", raw)
		}
	}
	if _, err := parseAssetDigest("sha256:" + strings.Repeat("a", 64)); err != nil {
		t.Errorf("合法摘要应通过：%v", err)
	}
	for _, bad := range []string{"", "md5:abc", "sha256:zz", "sha256:" + strings.Repeat("a", 63)} {
		if _, err := parseAssetDigest(bad); err == nil {
			t.Errorf("非法摘要 %q 应被拒绝", bad)
		}
	}
}

func TestSelectReleaseAsset(t *testing.T) {
	assets := []releaseAsset{{Name: "BBDown_linux_arm64.tar.gz"}, {Name: "BBDown_linux_amd64.tar.gz"}, {Name: "checksums.txt"}}
	got, err := selectReleaseAsset(assets, "BBDown_linux_amd64.tar.gz")
	if err != nil || got.Name != "BBDown_linux_amd64.tar.gz" {
		t.Fatalf("应精确命中当前平台资产：%+v %v", got, err)
	}
	if _, err := selectReleaseAsset(assets, "BBDown_darwin_amd64.tar.gz"); err == nil || !strings.Contains(err.Error(), "现有：") {
		t.Errorf("缺资产时应列出已有资产名，实际 %v", err)
	}
}

// extractExecutable：tar.gz 与 zip 两种归档都要能取出主二进制，且不误取其它文件。
func TestExtractExecutable(t *testing.T) {
	tgz := tarGzWith(t, "BBDown", []byte("unix-bin"))
	if _, err := gzip.NewReader(bytes.NewReader(tgz)); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range []struct{ name, body string }{{"device.wvd", "wvd"}, {"mp4decrypt", "dec"}, {"BBDown", "unix-bin"}} {
		if err := tw.WriteHeader(&tar.Header{Name: e.name, Mode: 0o644, Size: int64(len(e.body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(e.body)); err != nil {
			t.Fatal(err)
		}
	}
	_ = tw.Close()
	_ = gz.Close()
	bin, err := extractExecutable(buf.Bytes(), "BBDown_linux_amd64.tar.gz", "linux")
	if err != nil || string(bin) != "unix-bin" {
		t.Fatalf("tar.gz 提取失败：%q %v", bin, err)
	}

	zipBytes := zipWith(t, "BBDown.exe", []byte("win-bin"))
	bin, err = extractExecutable(zipBytes, "BBDown_windows_amd64.zip", "windows")
	if err != nil || string(bin) != "win-bin" {
		t.Fatalf("zip 提取失败：%q %v", bin, err)
	}
	if _, err := extractExecutable(zipBytes, "BBDown_windows_amd64.zip", "linux"); err == nil {
		t.Error("归档里没有 BBDown（只有 .exe）时 linux 提取应报错")
	}
}

// unix 语义：rename 一步到位；替换后内容与可执行位都对。
func TestReplaceExecutableUnix(t *testing.T) {
	dir := t.TempDir()
	target := writeExecutable(t, dir)
	if err := replaceExecutable([]byte("unix-new"), target, "linux"); err != nil {
		t.Fatalf("unix 替换失败：%v", err)
	}
	if got := readFile(t, target); got != "unix-new" {
		t.Errorf("内容不符：%q", got)
	}
	assertNoTempLeftovers(t, dir)
}

// Windows 语义（离线断言）：旧文件改名让位 → 新文件就位 → 旧文件清理。
func TestReplaceExecutableWindowsSemantics(t *testing.T) {
	dir := t.TempDir()
	target := writeExecutable(t, dir)
	if err := replaceExecutable([]byte("win-new"), target, "windows"); err != nil {
		t.Fatalf("windows 语义替换失败：%v", err)
	}
	if got := readFile(t, target); got != "win-new" {
		t.Errorf("内容不符：%q", got)
	}
	assertNoTempLeftovers(t, dir)
}

// Windows 语义的回滚：第二步 rename 失败 → 旧文件必须原样回来。
func TestReplaceExecutableWindowsRollback(t *testing.T) {
	dir := t.TempDir()
	target := writeExecutable(t, dir)
	origRename := renameFile
	calls := 0
	renameFile = func(old, new string) error {
		calls++
		if calls == 2 { // 第一次是「旧文件让位」，第二次是「新文件就位」——这里注入失败
			return fmt.Errorf("injected rename failure")
		}
		return origRename(old, new)
	}
	t.Cleanup(func() { renameFile = origRename })

	err := replaceExecutable([]byte("win-new"), target, "windows")
	if err == nil || !strings.Contains(err.Error(), "已回滚旧文件") {
		t.Fatalf("替换失败应回滚并说明，实际 %v", err)
	}
	if got := readFile(t, target); got != "old-binary" {
		t.Errorf("回滚后旧文件内容不符：%q", got)
	}
	assertNoTempLeftovers(t, dir)
}

// Windows 语义下连「让位」都失败（例如被其它进程锁住）：原文件不动、临时文件清掉。
func TestReplaceExecutableWindowsRenameOldFails(t *testing.T) {
	dir := t.TempDir()
	target := writeExecutable(t, dir)
	origRename := renameFile
	renameFile = func(old, new string) error { return fmt.Errorf("busy") }
	t.Cleanup(func() { renameFile = origRename })

	err := replaceExecutable([]byte("win-new"), target, "windows")
	if err == nil || !strings.Contains(err.Error(), "原文件未改动") {
		t.Fatalf("让位失败应报「原文件未改动」，实际 %v", err)
	}
	if got := readFile(t, target); got != "old-binary" {
		t.Errorf("原文件被改动：%q", got)
	}
	assertNoTempLeftovers(t, dir)
}
