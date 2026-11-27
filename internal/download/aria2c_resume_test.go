package download

// 上游 v1.7.9（85fabe1）：Windows 上 aria2 1.37.0 的 --input-file=-（stdin）模式收尾不了
// 控制文件——中断和成功都只留 <文件>.aria2__temp 而没有 .aria2，断点续传失效；预分配后的
// 等长产物还可能被当成完整文件跳过。本文件的用例钉住本仓的对应行为：
//  1. 平台选择：Windows 用私有临时输入文件，其它平台逐字保持 --input-file=-（Cookie 不落盘）；
//  2. 控制文件家族：.aria2__temp 也算「没下完」，成功/失败/清理路径都不能把它漏掉；
//  3. .aria2__temp 存在时整组丢弃不可信残留后完整重下，而正常的 .aria2 保留给 aria2c 续传。
//
// 纯函数用例在所有平台跑（windows-latest CI 的跨平台判据）；脚本形态的接线用例在 Windows
// 上按既有约定 t.Skip（假 aria2c 是 POSIX shell 脚本）。

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// withAria2cWindowsInput 把平台/临时目录两个注入点切到「Windows 模式」，测试结束恢复。
func withAria2cWindowsInput(t *testing.T) string {
	t.Helper()
	tempDir := t.TempDir()
	origGOOS, origTempDir := aria2cGOOS, aria2cInputTempDir
	aria2cGOOS, aria2cInputTempDir = "windows", tempDir
	t.Cleanup(func() { aria2cGOOS, aria2cInputTempDir = origGOOS, origTempDir })
	return tempDir
}

// writePosixExecutable 写一个可执行的 POSIX shell 脚本（假 aria2c）。
func writePosixExecutable(t *testing.T, dir, src string) string {
	t.Helper()
	path := filepath.Join(dir, "aria2c")
	if err := os.WriteFile(path, []byte(src), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// argvHasLine 报告假 aria2c 记录的 argv（每行一个参数）里是否有精确的 want 行。
func argvHasLine(argv, want string) bool {
	for _, line := range strings.Split(argv, "\n") {
		if line == want {
			return true
		}
	}
	return false
}

// assertNoAria2cInputTempFiles 断言本次 aria2c 调用的输入临时文件已删除（成功与失败路径都要删）。
func assertNoAria2cInputTempFiles(t *testing.T, dir string) {
	t.Helper()
	left, err := filepath.Glob(filepath.Join(dir, "bbdown-aria2-*.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Errorf("input-file 临时文件用后必须删除（含错误路径），实际残留: %v", left)
	}
}

// TestAria2cInputFilePlatformChoice 钉住平台选择本身（纯函数）：Windows → 临时文件形态，
// 其它平台（含空串）→ 逐字的 --input-file=-。这些断言在 windows-latest CI 上也直接跑，
// 不需要真实的 aria2c。
//
// 变异验证：把 aria2cNeedsTempInputFile 改成恒 false（或恒 true）→ windows 那行红。
func TestAria2cInputFilePlatformChoice(t *testing.T) {
	const tempPath = "/tmp/bbdown-aria2-input.txt"
	cases := []struct {
		name string
		goos string
		want string
	}{
		{"windows 用私有临时文件", "windows", "--input-file=" + tempPath},
		{"linux 保持 stdin", "linux", "--input-file=-"},
		{"darwin 保持 stdin", "darwin", "--input-file=-"},
		{"空 goos 保持 stdin", "", "--input-file=-"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := aria2cNeedsTempInputFile(c.goos); got != (c.goos == "windows") {
				t.Errorf("aria2cNeedsTempInputFile(%q) = %v，期望 %v", c.goos, got, c.goos == "windows")
			}
			if got := aria2cInputFileArg(c.goos, tempPath); got != c.want {
				t.Errorf("aria2cInputFileArg(%q) = %q，期望 %q", c.goos, got, c.want)
			}
		})
	}
}

// TestWriteAria2cInputTempFile 钉住临时文件本身：内容逐字落盘（Cookie 不能丢），Unix 上
// 权限必须是 0600（Windows 不应用 Unix 权限位，文件位于当前用户 %TEMP%）。
//
// 变异验证：把 os.CreateTemp 的 pattern/目录写错 → 路径断言红；把写入内容截断 → 内容断言红。
func TestWriteAria2cInputTempFile(t *testing.T) {
	dir := t.TempDir()
	orig := aria2cInputTempDir
	aria2cInputTempDir = dir
	t.Cleanup(func() { aria2cInputTempDir = orig })

	const content = "https://upos.example.com/a.m4s\n  header=Cookie: SESSDATA=secret\n  out=a.m4s\n"
	path, err := writeAria2cInputTempFile(content)
	if err != nil {
		t.Fatalf("writeAria2cInputTempFile: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(path) })

	// 路径判据用底层对象比较（t.TempDir 在 macOS 上可能是 /var 的符号链，禁止字符串相等）。
	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	fileDirInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(dirInfo, fileDirInfo) {
		t.Errorf("临时文件应当位于注入的目录 %q，实际 %q", dir, path)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != content {
		t.Errorf("临时文件内容 = %q，期望 %q", string(got), content)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("临时文件权限 = %o，期望 600（内含 Cookie，不能落成他人可读）", perm)
		}
	} else {
		t.Log("Windows 不应用 Unix 权限位：文件位于当前用户的临时目录，ACL 继承该目录")
	}
}

// TestAria2cWindowsTempInputFileLifecycle 是 Windows 分支的接线用例（在 Unix 上用注入点把
// 平台切成 windows，假 aria2c 照常是 POSIX shell）：--input-file 指向临时文件、内容经文件
// 真正到达 aria2c（不是 stdin）、且无论成功还是失败，临时文件都在 aria2c 退出后删除。
//
// 变异验证：去掉 downloadWithAria2c 里删除临时文件的 defer → 两个子用例都红（残留文件）；
// 把窗口分支改回永远 stdin → argv 断言与内容断言红（脚本读不到临时文件）。
func TestAria2cWindowsTempInputFileLifecycle(t *testing.T) {
	skipWithoutPOSIXShell(t)
	const size = 8
	tempDir := withAria2cWindowsInput(t)
	cdn := aria2TestCDN(t, size)

	t.Run("成功路径", func(t *testing.T) {
		dir := t.TempDir()
		dest := filepath.Join(dir, "out.bin")
		argvFile := filepath.Join(dir, "argv.txt")
		inputCopy := filepath.Join(dir, "input.txt")
		script := writePosixExecutable(t, dir, fmt.Sprintf(`#!/bin/sh
for a in "$@"; do echo "$a" >> %q; done
input=""
for a in "$@"; do
  case "$a" in
    --input-file=*) input="$(printf '%%s' "$a" | sed 's/^--input-file=//')" ;;
  esac
done
if [ -n "$input" ] && [ -f "$input" ]; then cp "$input" %q; fi
dd if=/dev/zero of=%q bs=1 count=%d 2>/dev/null
`, argvFile, inputCopy, dest, size))

		cfg := DownloadConfig{Client: newTestClient(), UseAria2c: true, Aria2cPath: script}
		if err := DownloadFile(context.Background(), cdn.URL+"/a.m4s", dest, cfg); err != nil {
			t.Fatalf("假 aria2c 正常退出，不该报错: %v", err)
		}

		argvBytes, err := os.ReadFile(argvFile)
		if err != nil {
			t.Fatalf("假 aria2c 没有写出 argv: %v", err)
		}
		argv := string(argvBytes)
		if !strings.Contains(argv, "--input-file="+tempDir) {
			t.Errorf("Windows 模式必须传临时文件路径（目录 %q），实际 argv:\n%s", tempDir, argv)
		}
		if argvHasLine(argv, "--input-file=-") {
			t.Errorf("Windows 模式不得再走 stdin（--input-file=- 正是控制文件收尾缺陷的来源）:\n%s", argv)
		}

		copied, err := os.ReadFile(inputCopy)
		if err != nil {
			t.Fatalf("aria2c 没能读到临时输入文件（说明内容仍走了 stdin）: %v", err)
		}
		want := buildAria2cInputFile(cdn.URL+"/a.m4s", needsBilibiliReferer(cdn.URL+"/a.m4s"),
			cfg.Cookie, cfg.userAgent(), filepath.Dir(dest), filepath.Base(dest))
		if string(copied) != want {
			t.Errorf("临时输入文件内容 = %q，期望 %q", string(copied), want)
		}
		assertNoAria2cInputTempFiles(t, tempDir)
	})

	t.Run("失败路径也删除临时文件", func(t *testing.T) {
		dir := t.TempDir()
		dest := filepath.Join(dir, "out.bin")
		script := writePosixExecutable(t, dir, "#!/bin/sh\nexit 3\n")
		cfg := DownloadConfig{Client: newTestClient(), UseAria2c: true, Aria2cPath: script}
		if err := DownloadFile(context.Background(), cdn.URL+"/a.m4s", dest, cfg); err == nil {
			t.Fatal("aria2c 非零退出必须报错")
		}
		assertNoAria2cInputTempFiles(t, tempDir)
	})
}

// TestAria2cPurgesUntrustedTempControlResidue：.aria2__temp 存在时没有可续传的块映射，
// 预分配出来的等长产物不可信——aria2c 启动前必须把产物与两个控制文件整组丢弃后完整重下
// （上游 v1.7.9 85fabe1 的 PrepareAria2cTarget 同款）。
//
// 变异验证：去掉 downloadWithAria2c 里的 purge 分支 → 假 aria2c 看到 dest/aria2/temp 全都
// 还在，状态断言红。
func TestAria2cPurgesUntrustedTempControlResidue(t *testing.T) {
	skipWithoutPOSIXShell(t)
	const size = 8
	cdn := aria2TestCDN(t, size)
	dir := t.TempDir()
	dest := filepath.Join(dir, "out.bin")
	stateFile := filepath.Join(dir, "state.txt")
	// 等长预分配残件 + 两种控制文件都在（Windows stdin 模式遗留的形态）。
	if err := os.WriteFile(dest, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(aria2ControlPath(dest), []byte("block-map"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(aria2TempControlPath(dest), []byte("unfinished"), 0o644); err != nil {
		t.Fatal(err)
	}

	script := writePosixExecutable(t, dir, fmt.Sprintf(`#!/bin/sh
{
  [ -e %q ] && echo "dest=present" || echo "dest=absent"
  [ -e %q ] && echo "aria2=present" || echo "aria2=absent"
  [ -e %q ] && echo "temp=present" || echo "temp=absent"
} > %q
dd if=/dev/zero of=%q bs=1 count=%d 2>/dev/null
`, dest, aria2ControlPath(dest), aria2TempControlPath(dest), stateFile, dest, size))

	cfg := DownloadConfig{Client: newTestClient(), UseAria2c: true, Aria2cPath: script}
	if err := DownloadFile(context.Background(), cdn.URL+"/a.m4s", dest, cfg); err != nil {
		t.Fatalf("整组丢弃后应当能完整重下: %v", err)
	}
	state, err := os.ReadFile(stateFile)
	if err != nil {
		t.Fatalf("假 aria2c 没有写出状态: %v", err)
	}
	const want = "dest=absent\naria2=absent\ntemp=absent\n"
	if string(state) != want {
		t.Errorf(".aria2__temp 存在时不可信残留必须整组丢弃，aria2c 看到的状态:\n%s", state)
	}
	for _, gone := range []string{aria2ControlPath(dest), aria2TempControlPath(dest)} {
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Errorf("成功重下后控制文件必须不存在：%s（err=%v）", filepath.Base(gone), err)
		}
	}
}

// TestAria2cKeepsTrustedControlFileForResume 钉住 t68 的续传路径：只有正常的 .aria2（有块
// 映射）时**不能**丢弃产物，必须原样交给 aria2c --continue 续传；这与中间态的处理相反。
//
// 变异验证：把 purge 分支的条件从 hasAria2TempControlFile 改成 hasAria2ControlFile →
// 假 aria2c 看到产物与控制文件都被删了，状态断言红。
func TestAria2cKeepsTrustedControlFileForResume(t *testing.T) {
	skipWithoutPOSIXShell(t)
	const size = 8
	cdn := aria2TestCDN(t, size)
	dir := t.TempDir()
	dest := filepath.Join(dir, "out.bin")
	stateFile := filepath.Join(dir, "state.txt")
	if err := os.WriteFile(dest, []byte("0123"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(aria2ControlPath(dest), []byte("block-map"), 0o644); err != nil {
		t.Fatal(err)
	}

	// 脚本末尾删除 .aria2 模拟 aria2c 正常收尾（成功下载会自己删控制文件）。
	script := writePosixExecutable(t, dir, fmt.Sprintf(`#!/bin/sh
{
  [ -e %q ] && echo "dest=present" || echo "dest=absent"
  [ -e %q ] && echo "aria2=present" || echo "aria2=absent"
  [ -e %q ] && echo "temp=present" || echo "temp=absent"
} > %q
dd if=/dev/zero of=%q bs=1 count=%d 2>/dev/null
rm -f %q
`, dest, aria2ControlPath(dest), aria2TempControlPath(dest), stateFile, dest, size, aria2ControlPath(dest)))

	cfg := DownloadConfig{Client: newTestClient(), UseAria2c: true, Aria2cPath: script}
	if err := DownloadFile(context.Background(), cdn.URL+"/a.m4s", dest, cfg); err != nil {
		t.Fatalf("可信续传凭证不该导致报错: %v", err)
	}
	state, err := os.ReadFile(stateFile)
	if err != nil {
		t.Fatalf("假 aria2c 没有写出状态: %v", err)
	}
	const want = "dest=present\naria2=present\ntemp=absent\n"
	if string(state) != want {
		t.Errorf("正常的 .aria2 是续传凭证，必须原样留给 aria2c（不能被当成不可信残留删掉）:\n%s", state)
	}
}

// TestAria2cPostRunRejectsControlFileResidue：aria2c 退出后只要 .aria2 或 .aria2__temp
// 还在，就说明它没正常收尾（没有块映射/长度不可信）——产物不能进入混流，必须报错。
//
// 变异验证：把末尾检查退回只看 .aria2 → .aria2__temp 子用例红（返回 nil）。
func TestAria2cPostRunRejectsControlFileResidue(t *testing.T) {
	skipWithoutPOSIXShell(t)
	const size = 8
	cdn := aria2TestCDN(t, size)
	cases := []struct {
		name   string
		suffix string
	}{
		{"aria2 控制文件", ".aria2"},
		{"aria2__temp 中间态", ".aria2__temp"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			dest := filepath.Join(dir, "out.bin")
			script := writePosixExecutable(t, dir, fmt.Sprintf(`#!/bin/sh
dd if=/dev/zero of=%q bs=1 count=%d 2>/dev/null
: > %q
`, dest, size, dest+c.suffix))
			cfg := DownloadConfig{Client: newTestClient(), UseAria2c: true, Aria2cPath: script}
			err := DownloadFile(context.Background(), cdn.URL+"/a.m4s", dest, cfg)
			if err == nil {
				t.Fatalf("aria2c 退出后仍有 %s，必须报 aria2 错误（产物不可信）", c.suffix)
			}
			if !strings.Contains(err.Error(), "aria2下载可能存在错误") {
				t.Errorf("错误信息应指出 aria2 残留，实际 %q", err.Error())
			}
		})
	}
}

// TestRemoveStaleAria2ControlRemovesBothSuffixes：非 aria2 路径成功重写产物后，两种控制
// 文件都必须清掉，否则下一次永远按「未完成」处理（白重下）。
//
// 变异验证：循环里只删 .aria2 → .aria2__temp 断言红。
func TestRemoveStaleAria2ControlRemovesBothSuffixes(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "out.bin")
	if err := os.WriteFile(aria2ControlPath(dest), []byte("block-map"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(aria2TempControlPath(dest), []byte("unfinished"), 0o644); err != nil {
		t.Fatal(err)
	}
	removeStaleAria2Control(dest)
	for _, gone := range []string{aria2ControlPath(dest), aria2TempControlPath(dest)} {
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Errorf("非 aria2 路径成功落盘后必须清掉两种控制文件：%s（err=%v）", filepath.Base(gone), err)
		}
	}
}
