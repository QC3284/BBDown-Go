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
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// CheckUpdateAsync probes the GitHub latest-release redirect and logs when a
// newer version exists (upstream CheckUpdateAsync, fire-and-forget).
func CheckUpdateAsync(ctx context.Context, client *HTTPClient, currentVersion string) {
	go func() {
		ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		redirect, err := client.GetWebLocation(ctx, "https://github.com/QC3284/BBDown-Go/releases/latest")
		if err != nil {
			LogDebug("检查更新失败: %v", err)
			return
		}
		latest := strings.TrimPrefix(redirect, "https://github.com/QC3284/BBDown-Go/releases/tag/")
		if latest != "" && !strings.HasPrefix(latest, "https") && !strings.EqualFold(latest, currentVersion) {
			LogColor("发现新版本：%s", latest)
		}
	}()
}

// ============================================================================
// t81：self-update 自更新（吸收 BBDownT 2.1.3 的独立二进制自更新）
// ============================================================================
//
// 流程与安全边界（每一条都有离线用例）：
//   1. 只问**官方仓库**的 Releases API（常量写死；测试用假服务器注入 APIURL，生产不可改）；
//   2. 版本口径与 `BBDown --version` 相同（tag 去掉 v 前缀后逐字比较）；已是最新则**不下载**；
//   3. 资产必须是**当前平台的官方命名**（BBDown_<os>_<arch>.tar.gz / .zip），且下载地址必须是
//      https + 官方主机白名单——API 响应被篡改也换不到别处去下；
//   4. **校验**：资产必须带 GitHub 的 sha256 摘要（`digest: "sha256:..."`），下载同时算哈希，
//      不一致或缺失一律中止，**原文件一个字节都不动**；
//   5. **原子替换**：先写同目录临时文件 → 再 rename（同文件系统内是原子的）；
//      Windows 不能覆盖正在运行的 exe，用「旧文件改名 → 新文件就位 → 失败则改回」的语义，
//      任一步失败都回滚旧文件（回滚本身也有用例）。
//
// 有意不做的事：不动归档里的其它文件（mp4decrypt / device.wvd）——只替换主二进制，
// 其余内容在结果里点名提示去 Release 页手动补齐，避免「半套资产」的混合状态。

// 官方仓库与 Releases API 常量：生产路径只认这两个（测试通过 SelfUpdateConfig.APIURL 注入假服务器）。
const (
	officialRepoOwner  = "QC3284"
	officialRepoName   = "BBDown-Go"
	officialReleaseAPI = "https://api.github.com/repos/" + officialRepoOwner + "/" + officialRepoName + "/releases/latest"
)

// selfUpdateAssetName 返回当前平台的官方资产名（与 .github/workflows/release.yml 的打包命名逐字一致）。
func selfUpdateAssetName(goos, goarch string) (string, error) {
	switch goos {
	case "linux", "darwin":
		return "BBDown_" + goos + "_" + goarch + ".tar.gz", nil
	case "windows":
		return "BBDown_windows_" + goarch + ".zip", nil
	}
	return "", fmt.Errorf("self-update 暂不支持 %s/%s：Release 未提供该平台资产", goos, goarch)
}

// releaseAsset 是 Releases API 里的一条资产（只取自更新需要的字段）。
type releaseAsset struct {
	Name        string `json:"name"`
	Size        int64  `json:"size"`
	Digest      string `json:"digest"`
	DownloadURL string `json:"browser_download_url"`
}

type releaseInfo struct {
	TagName string         `json:"tag_name"`
	Assets  []releaseAsset `json:"assets"`
}

// SelfUpdateConfig 是一次自更新的输入（全部可注入，用例因此不需要网络）。
type SelfUpdateConfig struct {
	CurrentVersion string       // 与 --version 同一口径（如 3.0.1）
	GOOS           string       // 空 = runtime.GOOS
	GOARCH         string       // 空 = runtime.GOARCH
	ExecutablePath string       // 空 = os.Executable()
	APIURL         string       // 仅测试：假服务器地址；空 = 官方 Releases API
	Client         *http.Client // 空 = 默认客户端（https + 超时）
}

// SelfUpdateResult 描述一次自更新的结果（调用方据此打印）。
type SelfUpdateResult struct {
	Updated         bool
	FromVersion     string
	ToVersion       string
	AssetName       string
	DownloadedBytes int64
}

// maxSelfUpdateAssetSize 是下载上限（防止被一个畸形响应喂爆内存）：256 MiB 远大于任何真实资产。
const maxSelfUpdateAssetSize = 256 << 20

// officialDownloadHosts 是允许下载资产的主机白名单（GitHub Release 资产的三种实际跳转落点）。
var officialDownloadHosts = map[string]bool{
	"github.com":                           true,
	"objects.githubusercontent.com":        true,
	"release-assets.githubusercontent.com": true,
}

// normalizeVersion 把 vX.Y.Z / X.Y.Z 统一成 X.Y.Z（版本口径与 --version 一致）。
func normalizeVersion(v string) string {
	return strings.TrimPrefix(strings.TrimSpace(v), "v")
}

// SelfUpdate 执行一次自更新：查最新版 → 比对 → 下载 → 校验 → 原子替换。
//
// 任何一步失败都返回错误且**不改动原可执行文件**（校验在写盘之前完成，替换是最后一步）。
func SelfUpdate(ctx context.Context, cfg SelfUpdateConfig) (SelfUpdateResult, error) {
	res := SelfUpdateResult{FromVersion: normalizeVersion(cfg.CurrentVersion)}
	goos, goarch := cfg.GOOS, cfg.GOARCH
	if goos == "" {
		goos = runtime.GOOS
	}
	if goarch == "" {
		goarch = runtime.GOARCH
	}
	assetName, err := selfUpdateAssetName(goos, goarch)
	if err != nil {
		return res, err
	}
	apiURL := cfg.APIURL
	if apiURL == "" {
		apiURL = officialReleaseAPI
	}
	client := cfg.Client
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}

	rel, err := fetchLatestRelease(ctx, client, apiURL)
	if err != nil {
		return res, err
	}
	res.ToVersion = normalizeVersion(rel.TagName)
	if res.ToVersion == "" {
		return res, errors.New("self-update：Release 响应里没有 tag_name，拒绝更新")
	}
	if res.ToVersion == res.FromVersion {
		return res, nil // 已是最新：不下载、不动文件
	}
	asset, err := selectReleaseAsset(rel.Assets, assetName)
	if err != nil {
		return res, err
	}
	res.AssetName = asset.Name
	if err := validateDownloadURL(asset.DownloadURL); err != nil {
		return res, err
	}
	wantDigest, err := parseAssetDigest(asset.Digest)
	if err != nil {
		return res, err
	}

	archive, err := downloadAsset(ctx, client, asset.DownloadURL, wantDigest)
	if err != nil {
		return res, err
	}
	res.DownloadedBytes = int64(len(archive))
	bin, err := extractExecutable(archive, asset.Name, goos)
	if err != nil {
		return res, err
	}
	target := cfg.ExecutablePath
	if target == "" {
		target, err = os.Executable()
		if err != nil {
			return res, fmt.Errorf("self-update：定位当前可执行文件失败: %w", err)
		}
	}
	if err := replaceExecutable(bin, target, goos); err != nil {
		return res, err
	}
	res.Updated = true
	return res, nil
}

// fetchLatestRelease 读官方（或测试注入的）Releases API。
func fetchLatestRelease(ctx context.Context, client *http.Client, apiURL string) (releaseInfo, error) {
	var out releaseInfo
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return out, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "BBDown-self-update")
	resp, err := client.Do(req)
	if err != nil {
		return out, fmt.Errorf("self-update：查询最新版本失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return out, fmt.Errorf("self-update：查询最新版本失败（HTTP %d）", resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return out, fmt.Errorf("self-update：解析 Release 响应失败: %w", err)
	}
	return out, nil
}

// selectReleaseAsset 在当前平台的官方资产名里精确挑一条（不去模糊匹配：资产名就是契约）。
func selectReleaseAsset(assets []releaseAsset, name string) (releaseAsset, error) {
	for _, a := range assets {
		if a.Name == name {
			return a, nil
		}
	}
	names := make([]string, 0, len(assets))
	for _, a := range assets {
		names = append(names, a.Name)
	}
	return releaseAsset{}, fmt.Errorf("self-update：最新 Release 里没有当前平台的资产 %s（现有：%s）", name, strings.Join(names, ", "))
}

// validateDownloadURL 只允许 https + 官方主机白名单（响应被篡改也换不到别处下载）。
func validateDownloadURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("self-update：下载地址不可解析: %w", err)
	}
	if u.Scheme != "https" {
		return fmt.Errorf("self-update：下载地址必须是 https（实际 %q），拒绝更新", u.Scheme)
	}
	if !officialDownloadHosts[strings.ToLower(u.Hostname())] {
		return fmt.Errorf("self-update：下载地址主机 %q 不在官方白名单内，拒绝下载", u.Hostname())
	}
	return nil
}

// parseAssetDigest 要求资产带 sha256 摘要：没有摘要就**没有校验依据**，一律拒绝（不带摘要照装
// 等于放弃校验）。digest 形如 "sha256:<hex>"。
func parseAssetDigest(digest string) (string, error) {
	digest = strings.TrimSpace(digest)
	if digest == "" {
		return "", errors.New("self-update：Release 资产缺少 SHA256 摘要（digest），无法校验下载内容，拒绝更新")
	}
	parts := strings.SplitN(digest, ":", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "sha256") || len(parts[1]) != 64 {
		return "", fmt.Errorf("self-update：无法识别的资产摘要 %q（期望 sha256:<64位十六进制>）", digest)
	}
	return strings.ToLower(parts[1]), nil
}

// downloadAsset 下载并**同时**校验 sha256：不一致立即失败（此时还没写过任何文件）。
func downloadAsset(ctx context.Context, client *http.Client, downloadURL, wantSHA256 string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "BBDown-self-update")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("self-update：下载资产失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("self-update：下载资产失败（HTTP %d）", resp.StatusCode)
	}
	h := sha256.New()
	limited := io.LimitReader(resp.Body, maxSelfUpdateAssetSize+1)
	b, err := io.ReadAll(io.TeeReader(limited, h))
	if err != nil {
		return nil, fmt.Errorf("self-update：读取资产失败: %w", err)
	}
	if len(b) > maxSelfUpdateAssetSize {
		return nil, fmt.Errorf("self-update：资产超过 %d MiB 上限，拒绝更新", maxSelfUpdateAssetSize>>20)
	}
	got := hex.EncodeToString(h.Sum(nil))
	if got != wantSHA256 {
		return nil, fmt.Errorf("self-update：SHA256 校验失败（got %s want %s），已中止，原文件未改动", got, wantSHA256)
	}
	return b, nil
}

// extractExecutable 从官方归档里取出主二进制（tar.gz 给 unix，zip 给 windows）。
func extractExecutable(archive []byte, assetName, goos string) ([]byte, error) {
	want := "BBDown"
	if goos == "windows" {
		want = "BBDown.exe"
	}
	if strings.HasSuffix(assetName, ".zip") {
		zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
		if err != nil {
			return nil, fmt.Errorf("self-update：归档不是合法 zip: %w", err)
		}
		for _, f := range zr.File {
			if path.Base(f.Name) != want {
				continue
			}
			rc, err := f.Open()
			if err != nil {
				return nil, err
			}
			defer rc.Close()
			return io.ReadAll(io.LimitReader(rc, maxSelfUpdateAssetSize))
		}
		return nil, fmt.Errorf("self-update：归档里没有 %s", want)
	}
	gr, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, fmt.Errorf("self-update：归档不是合法 tar.gz: %w", err)
	}
	defer gr.Close()
	tr := tar.NewReader(gr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("self-update：读取 tar.gz 失败: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg || path.Base(hdr.Name) != want {
			continue
		}
		return io.ReadAll(io.LimitReader(tr, maxSelfUpdateAssetSize))
	}
	return nil, fmt.Errorf("self-update：归档里没有 %s", want)
}

// 替换用的文件操作接缝：用例靠它把「第二步 rename 失败」注入出来，验证回滚。
var (
	renameFile = os.Rename
	removeFile = os.Remove
	chmodFile  = os.Chmod
)

// replaceExecutable 原子替换可执行文件。
//
// 临时文件写在目标**同目录**（同一个文件系统，rename 才是原子的），写完立刻改成可执行位。
//
//	unix：rename(tmp → target) 一步到位（运行中的旧 inode 由内核保留，进程继续跑完）；
//	windows：运行中的 exe 不能被覆盖，所以先把旧文件改名让位，再把新文件 rename 就位；
//	         第二步失败则把旧文件改回去（回滚），并清掉临时文件。
func replaceExecutable(bin []byte, target, goos string) error {
	dir := filepath.Dir(target)
	tmp, err := os.CreateTemp(dir, ".bbdown-selfupdate-*")
	if err != nil {
		return fmt.Errorf("self-update：创建临时文件失败: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := func() {
		tmp.Close()
		removeFile(tmpPath)
	}
	if _, err := tmp.Write(bin); err != nil {
		cleanup()
		return fmt.Errorf("self-update：写入临时文件失败: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("self-update：刷盘失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		removeFile(tmpPath)
		return fmt.Errorf("self-update：关闭临时文件失败: %w", err)
	}
	if goos != "windows" {
		if err := chmodFile(tmpPath, 0o755); err != nil {
			removeFile(tmpPath)
			return fmt.Errorf("self-update：设置可执行位失败: %w", err)
		}
	}
	if goos != "windows" {
		if err := renameFile(tmpPath, target); err != nil {
			removeFile(tmpPath)
			// unix 上 rename 失败通常意味着目录不可写：原文件一个字都没动。
			return fmt.Errorf("self-update：替换可执行文件失败（原文件未改动）: %w", err)
		}
		return nil
	}
	// Windows 语义：旧文件让位 → 新文件就位 → 失败回滚。
	oldPath := target + ".old"
	if err := renameFile(target, oldPath); err != nil {
		removeFile(tmpPath)
		return fmt.Errorf("self-update：无法让位旧可执行文件（可能正被其它进程占用；原文件未改动）: %w", err)
	}
	if err := renameFile(tmpPath, target); err != nil {
		// 回滚：把旧文件改回原名，保证「失败 = 什么都没变」。
		if rbErr := renameFile(oldPath, target); rbErr != nil {
			removeFile(tmpPath)
			return fmt.Errorf("self-update：替换失败且回滚失败（旧文件留在 %s，请手动改回）: 替换错误 %v；回滚错误 %w", oldPath, err, rbErr)
		}
		removeFile(tmpPath)
		return fmt.Errorf("self-update：替换可执行文件失败，已回滚旧文件: %w", err)
	}
	// 成功：尽量删掉旧文件（运行中可能删不掉，留着也不影响下次启动）。
	_ = removeFile(oldPath)
	return nil
}
