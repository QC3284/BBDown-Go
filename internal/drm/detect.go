package drm

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// ---- DRM 开箱即用：内置件的解析与**下载前**的前置检查（t49，吸收上游 1.7.1）----
//
// 目标：发布包里带上 mp4decrypt（Bento4）与 device.wvd，DRM 内容自动解密默认开启；
// 本地构建（没有内置件）优雅降级：--mp4decrypt-path / --wvd-path / 手动 --key --kid 仍然可用。
//
// 关键点是**前置检查要发生在下载之前**：旧行为是「下完整段视频、进到解密阶段才发现没有
// device.wvd / mp4decrypt」——用户白等一次下载，而且失败点在流程末尾。

// AssetSource 说明某个外部件从哪来（日志与 doctor 用）。
type AssetSource string

const (
	// AssetMissing：没找到。
	AssetMissing AssetSource = "missing"
	// AssetExplicit：来自显式参数（--mp4decrypt-path / --wvd-path）。
	AssetExplicit AssetSource = "explicit"
	// AssetBundled：来自可执行文件同目录（发布包内置）。
	AssetBundled AssetSource = "bundled"
	// AssetPath：来自 PATH（只对 mp4decrypt 有意义）。
	AssetPath AssetSource = "PATH"
)

// Asset 是一个已解析（或缺失）的外部件。
type Asset struct {
	Path   string
	Source AssetSource
}

// Found 报告该外部件是否可用。
func (a Asset) Found() bool { return a.Path != "" && a.Source != AssetMissing }

// Assets 是解密所需的两个外部件。
type Assets struct {
	Mp4decrypt Asset
	Wvd        Asset
	// ExplicitMp4decrypt / ExplicitWvd 记录用户**显式给出**的路径原文（t90）：
	// 路径打错时前置检查要能点名「你给的这个路径不存在」，而不是只说「缺 mp4decrypt」。
	ExplicitMp4decrypt string
	ExplicitWvd        string
}

// BundledFileName 是随发布包内置的 Widevine 设备文件名（与上游一致）。
const BundledFileName = "device.wvd"

// ResolveAssets 解析解密所需的外部件，顺序是「显式路径 → 可执行文件同目录（内置） → PATH」。
//
// mp4decrypt 走三级（PATH 是常规安装方式）；device.wvd 只走两级——它不会被装进 PATH，
// 要么显式给，要么放在程序目录里（这正是发布包内置的位置）。
// exeDir 为空时跳过「同目录」那一级（离线用例可以只测显式与 PATH 两条）。
func ResolveAssets(exeDir, explicitMp4decrypt, explicitWvd string) Assets {
	var out Assets
	out.Mp4decrypt = resolveOne(exeDir, explicitMp4decrypt, "mp4decrypt", true)
	out.Wvd = resolveWvd(exeDir, explicitWvd)
	out.ExplicitMp4decrypt = strings.TrimSpace(explicitMp4decrypt)
	out.ExplicitWvd = strings.TrimSpace(explicitWvd)
	return out
}

func resolveOne(exeDir, explicit, name string, allowPath bool) Asset {
	if strings.TrimSpace(explicit) != "" {
		if info, err := os.Stat(explicit); err == nil && !info.IsDir() {
			return Asset{Path: explicit, Source: AssetExplicit}
		}
		// 显式给了但不存在：按缺失处理（错误文案里会带上这个路径，让用户知道是哪个路径不对）。
		return Asset{Source: AssetMissing}
	}
	if exeDir != "" {
		candidate := filepath.Join(exeDir, name)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return Asset{Path: candidate, Source: AssetBundled}
		}
	}
	if allowPath {
		if p, err := exec.LookPath(name); err == nil {
			return Asset{Path: p, Source: AssetPath}
		}
	}
	return Asset{Source: AssetMissing}
}

func resolveWvd(exeDir, explicit string) Asset {
	if strings.TrimSpace(explicit) != "" {
		if info, err := os.Stat(explicit); err == nil && !info.IsDir() {
			return Asset{Path: explicit, Source: AssetExplicit}
		}
		return Asset{Source: AssetMissing}
	}
	if exeDir != "" {
		candidate := filepath.Join(exeDir, BundledFileName)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return Asset{Path: candidate, Source: AssetBundled}
		}
	}
	return Asset{Source: AssetMissing}
}

// PrerequisiteError 是下载前前置检查的失败：带上缺什么、以及**可操作的**下一步。
type PrerequisiteError struct {
	Missing []string
	Hints   []string
}

func (e *PrerequisiteError) Error() string {
	return fmt.Sprintf("DRM 解密前置检查未通过（缺少 %s）：%s",
		strings.Join(e.Missing, "、"), strings.Join(e.Hints, "；"))
}

// IsPrerequisiteError 报告 err 是不是前置检查失败（调用方据此提前失败、不再下载）。
func IsPrerequisiteError(err error) bool {
	var target *PrerequisiteError
	return errors.As(err, &target)
}

// Mp4decryptInstallHint 给出安装 mp4decrypt（Bento4）的可操作建议，按平台区分。
//
// linux/arm64 要单独说清楚：Bento4 官方**不发布** linux-arm64 二进制，
// 只写一句「请安装 Bento4」会让该平台的用户一直找不到东西。
func Mp4decryptInstallHint() string {
	hint := "安装 Bento4（提供 mp4decrypt）后重试，或用 --mp4decrypt-path 指向可执行文件"
	if runtime.GOOS == "linux" && runtime.GOARCH == "arm64" {
		hint = "Bento4 官方没有 linux-arm64 预编译包：请用发行版仓库安装（如 apt install bento4），" +
			"或自行编译 Bento4 后用 --mp4decrypt-path 指定"
	}
	return hint
}

// CheckDecryptPrerequisites 是**下载前**的前置检查（三态）：
//
//  1. 手动 --key/--kid 齐备：不需要 device.wvd（用户自己给了密钥），但 mp4decrypt 仍然必需；
//  2. 缺 mp4decrypt：给出安装指引（linux-arm64 另说）；
//  3. 缺 device.wvd（且没有手动密钥）：给出三条出路——--wvd-path、放到程序目录、
//     或改用 --key --kid 手动提供。
//
// 返回 nil 表示可以继续（下载 → 解密）。
func CheckDecryptPrerequisites(assets Assets, keyHex, kidHex string) error {
	manual := strings.TrimSpace(keyHex) != "" && strings.TrimSpace(kidHex) != ""
	var missing, hints []string

	if !assets.Mp4decrypt.Found() {
		missing = append(missing, "mp4decrypt")
		if assets.ExplicitMp4decrypt != "" {
			hints = append(hints, fmt.Sprintf("你显式指定的路径 %q 不存在或不是文件（--mp4decrypt-path）；请检查拼写，或去掉该选项改从 PATH/程序目录查找", assets.ExplicitMp4decrypt))
		}
		hints = append(hints, Mp4decryptInstallHint())
	}
	if !manual && !assets.Wvd.Found() {
		missing = append(missing, BundledFileName)
		if assets.ExplicitWvd != "" {
			// t90：点名「你给的路径」——只说缺 device.wvd，用户会以为自己已经指定了。
			hints = append(hints, fmt.Sprintf("你显式指定的路径 %q 不存在或不是文件（--wvd-path）；请检查拼写，或去掉该选项改用程序目录", assets.ExplicitWvd))
		}
		hints = append(hints,
			"把 "+BundledFileName+" 放到程序目录（发布包内置）或用 --wvd-path 指定它的路径",
			"也可以直接用 --key <hex> --kid <hex> 手动提供密钥，这样不需要 "+BundledFileName)
	}
	if len(missing) == 0 {
		return nil
	}
	// 顺序固定为「先 mp4decrypt、再 device.wvd」（检查顺序），不排序：文案要稳定，
	// 而且这个顺序本身就是告诉用户「先修哪个」。
	return &PrerequisiteError{Missing: missing, Hints: hints}
}

// DescribeAssets 把解析结果写成一行日志/doctor 文案（缺失时给指引）。
func DescribeAssets(assets Assets) string {
	part := func(name string, a Asset) string {
		if !a.Found() {
			return name + "=缺失"
		}
		return fmt.Sprintf("%s=%s(%s)", name, a.Path, a.Source)
	}
	return part("mp4decrypt", assets.Mp4decrypt) + " " + part(BundledFileName, assets.Wvd)
}
