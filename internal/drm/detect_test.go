package drm

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// ---- t49：DRM 内置件解析 + 下载前前置检查（三态）----

func touch(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestResolveAssetsPrecedence 钉住解析顺序：显式路径 → 程序目录（发布包内置）→ PATH。
// mp4decrypt 走三级（PATH 是常规安装方式）；device.wvd 只走两级（它不会进 PATH）。
func TestResolveAssetsPrecedence(t *testing.T) {
	exeDir := t.TempDir()
	bundledMp4 := touch(t, exeDir, "mp4decrypt")
	bundledWvd := touch(t, exeDir, BundledFileName)

	assets := ResolveAssets(exeDir, "", "")
	if assets.Mp4decrypt.Path != bundledMp4 || assets.Mp4decrypt.Source != AssetBundled {
		t.Errorf("内置 mp4decrypt 应当被识别：%+v", assets.Mp4decrypt)
	}
	if assets.Wvd.Path != bundledWvd || assets.Wvd.Source != AssetBundled {
		t.Errorf("内置 %s 应当被识别：%+v", BundledFileName, assets.Wvd)
	}
	if !assets.Mp4decrypt.Found() || !assets.Wvd.Found() {
		t.Error("两个内置件都应当 Found")
	}

	// 显式路径优先于内置。
	other := t.TempDir()
	explicitMp4 := touch(t, other, "my-mp4decrypt")
	explicitWvd := touch(t, other, "my.wvd")
	assets = ResolveAssets(exeDir, explicitMp4, explicitWvd)
	if assets.Mp4decrypt.Path != explicitMp4 || assets.Mp4decrypt.Source != AssetExplicit {
		t.Errorf("显式 mp4decrypt 应当优先：%+v", assets.Mp4decrypt)
	}
	if assets.Wvd.Path != explicitWvd || assets.Wvd.Source != AssetExplicit {
		t.Errorf("显式 wvd 应当优先：%+v", assets.Wvd)
	}

	// 显式给了但文件不存在：按缺失处理（文案里带上这个路径，用户才知道是哪个路径不对）。
	missingMp4 := filepath.Join(other, "nope-mp4decrypt")
	assets = ResolveAssets(exeDir, missingMp4, "")
	if assets.Mp4decrypt.Found() {
		t.Errorf("显式路径不存在时应当视为缺失（不再回落内置，以免掩盖路径写错），实际 %+v", assets.Mp4decrypt)
	}

	// 空的程序目录 + 显式也没有：仍然有 PATH 这一级（本机没装 Bento4 时就是缺失）。
	empty := ResolveAssets(t.TempDir(), "", "")
	if empty.Mp4decrypt.Source != AssetMissing || empty.Wvd.Source != AssetMissing {
		t.Errorf("什么都没有时两个件都应当缺失：%+v", empty)
	}

	// PATH 级（Windows 上 LookPath 需要 .exe 后缀，跳过这一臂）。
	if runtime.GOOS != "windows" {
		pathDir := t.TempDir()
		pathMp4 := touch(t, pathDir, "mp4decrypt")
		t.Setenv("PATH", pathDir)
		got := ResolveAssets(t.TempDir(), "", "")
		if got.Mp4decrypt.Path != pathMp4 || got.Mp4decrypt.Source != AssetPath {
			t.Errorf("PATH 里的 mp4decrypt 应当被识别：%+v", got.Mp4decrypt)
		}
		if got.Wvd.Found() {
			t.Errorf("%s 不该从 PATH 里找：%+v", BundledFileName, got.Wvd)
		}
	}
}

// TestCheckDecryptPrerequisitesThreeStates 钉住三态：
//  1. 手动 --key/--kid 齐备 → 通过（不需要 device.wvd，但 mp4decrypt 仍然必需）；
//  2. 缺 mp4decrypt → 报错并给安装指引；
//  3. 缺 device.wvd（无手动密钥）→ 报错并给三条出路。
func TestCheckDecryptPrerequisitesThreeStates(t *testing.T) {
	withBoth := Assets{Mp4decrypt: Asset{Path: "/x/mp4decrypt", Source: AssetBundled}, Wvd: Asset{Path: "/x/device.wvd", Source: AssetBundled}}
	onlyMp4 := Assets{Mp4decrypt: Asset{Path: "/x/mp4decrypt", Source: AssetBundled}}
	noMp4 := Assets{Wvd: Asset{Path: "/x/device.wvd", Source: AssetBundled}}
	nothing := Assets{}

	const kid = "00112233445566778899aabbccddeeff"
	const key = "ffeeddccbbaa99887766554433221100"

	// 态一：自动解密、两个件齐备。
	if err := CheckDecryptPrerequisites(withBoth, "", ""); err != nil {
		t.Errorf("件齐备时不该报错：%v", err)
	}
	// 态一（手动密钥）：只要有 mp4decrypt，不需要 wvd。
	if err := CheckDecryptPrerequisites(onlyMp4, key, kid); err != nil {
		t.Errorf("手动 --key/--kid 齐备时不需要 device.wvd：%v", err)
	}
	// 手动密钥缺一半 → 仍然按「没有手动密钥」处理（只给 key 不给 kid 拼不出 kid:key）。
	err := CheckDecryptPrerequisites(onlyMp4, key, "")
	if err == nil || !IsPrerequisiteError(err) {
		t.Fatalf("只给一半手动密钥时应当按缺 wvd 处理：%v", err)
	}
	if !strings.Contains(err.Error(), BundledFileName) {
		t.Errorf("缺 wvd 的文案要点名 %s：%v", BundledFileName, err)
	}

	// 态二：缺 mp4decrypt → 指引里要有安装/指定路径，且区分 linux-arm64。
	err = CheckDecryptPrerequisites(noMp4, key, kid)
	if err == nil || !IsPrerequisiteError(err) {
		t.Fatalf("缺 mp4decrypt 应当报前置检查失败：%v", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, "mp4decrypt") || !strings.Contains(msg, "--mp4decrypt-path") {
		t.Errorf("缺 mp4decrypt 的指引要可操作：%v", err)
	}
	if runtime.GOOS == "linux" && runtime.GOARCH == "arm64" {
		if !strings.Contains(msg, "linux-arm64") {
			t.Errorf("linux-arm64 上要写明「官方无预编译包」：%v", err)
		}
	}

	// 态三：缺 wvd（无手动密钥）→ 三条出路（--wvd-path / 放程序目录 / --key --kid）。
	err = CheckDecryptPrerequisites(nothing, "", "")
	if err == nil || !IsPrerequisiteError(err) {
		t.Fatalf("什么都没有时应当报前置检查失败：%v", err)
	}
	msg = err.Error()
	for _, want := range []string{"mp4decrypt", BundledFileName, "--wvd-path", "--key"} {
		if !strings.Contains(msg, want) {
			t.Errorf("三态文案缺少 %q：%v", want, err)
		}
	}
	// 两个都缺时都要列出来（不能只报一个，用户修完一个又撞第二个）。
	if !strings.Contains(msg, "缺少") || !strings.Contains(msg, "mp4decrypt、device.wvd") {
		t.Errorf("两个都缺时应当一起列出来：%v", err)
	}
}

// TestMp4decryptInstallHint 文案必须提到 Bento4（按平台给不同建议）。
func TestMp4decryptInstallHint(t *testing.T) {
	hint := Mp4decryptInstallHint()
	if !strings.Contains(hint, "Bento4") {
		t.Errorf("安装指引应当提到 Bento4：%q", hint)
	}
	if hint = strings.TrimSpace(hint); hint == "" {
		t.Error("安装指引不能是空串")
	}
}

// TestDescribeAssets 日志/doctor 文案：缺失时点名「缺失」，找到时给出路径与来源。
func TestDescribeAssets(t *testing.T) {
	got := DescribeAssets(Assets{
		Mp4decrypt: Asset{Path: "/usr/bin/mp4decrypt", Source: AssetPath},
		Wvd:        Asset{Path: "/app/device.wvd", Source: AssetBundled},
	})
	for _, want := range []string{"mp4decrypt=/usr/bin/mp4decrypt(PATH)", BundledFileName + "=/app/device.wvd(bundled)"} {
		if !strings.Contains(got, want) {
			t.Errorf("文案缺少 %q：%q", want, got)
		}
	}
	missing := DescribeAssets(Assets{})
	if !strings.Contains(missing, "mp4decrypt=缺失") || !strings.Contains(missing, BundledFileName+"=缺失") {
		t.Errorf("缺失时的文案应当点名缺失：%q", missing)
	}
}

// ---- t90（t82 评估 ⑤）：前置检查要点名「你显式给的那个路径」 ----

// TestPrerequisiteHintsNameExplicitMissingPaths 钉住 DRM 侧的两个选项：显式路径不存在时，
// 错误里必须点名是哪个路径不对（只说「缺 mp4decrypt」用户会以为自己已经指定了）。
func TestPrerequisiteHintsNameExplicitMissingPaths(t *testing.T) {
	assets := ResolveAssets("", "/no/such/mp4decrypt", "/no/such/device.wvd")
	err := CheckDecryptPrerequisites(assets, "", "")
	if err == nil {
		t.Fatal("缺 mp4decrypt 与 device.wvd 时必须失败")
	}
	msg := err.Error()
	for _, want := range []string{
		"你显式指定的路径 \"/no/such/mp4decrypt\" 不存在",
		"--mp4decrypt-path",
		"你显式指定的路径 \"/no/such/device.wvd\" 不存在",
		"--wvd-path",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("错误文案缺少 %q：%v", want, msg)
		}
	}
}

// TestPrerequisiteNoExplicitHintWhenUnset：没给显式路径时不该出现这条 hint（保持原文案不变）。
func TestPrerequisiteNoExplicitHintWhenUnset(t *testing.T) {
	assets := ResolveAssets("", "", "")
	err := CheckDecryptPrerequisites(assets, "", "")
	if err == nil {
		t.Skip("本机 mp4decrypt/device.wvd 齐全，无法构造缺失场景")
	}
	if strings.Contains(err.Error(), "你显式指定的路径") {
		t.Errorf("没给显式路径时不该出现该 hint：%v", err)
	}
}
