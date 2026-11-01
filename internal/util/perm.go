package util

import (
	"os"
	"runtime"
)

// 进程私有文件（serve 的任务记录、--log-file 日志）在 Unix 上只允许属主读写。
//
// 上游 #113（unreleased Security）把 bbdown-tasks.json / bbdown-api.log 收紧到 0600：
// 这两个文件会带任务参数与运行日志，多用户机器的其它账号不该读得到。
// Windows 不解释 POSIX 权限位，这里保留 0644，避免代码看起来做了实际不存在的防护。
const ownerOnlyFileMode os.FileMode = 0o600

// SecureFileMode 返回创建进程私有文件时应传的权限位。
// Unix：0600；Windows：0644（mode 在 Windows 只影响只读位）。
func SecureFileMode() os.FileMode {
	if runtime.GOOS == "windows" {
		return 0o644
	}
	return ownerOnlyFileMode
}

// TightenFilePermissions 尽力把已存在的进程私有文件收紧到 0600（仅 Unix）。
//
// 覆盖「升级前就已存在、权限是 0644」的历史文件（上游启动时同样会收紧既有文件）。
// 文件不存在、权限不允许或 Windows 上静默返回：权限收紧失败不该让启动或写日志失败。
func TightenFilePermissions(path string) {
	if runtime.GOOS == "windows" || path == "" {
		return
	}
	_ = os.Chmod(path, ownerOnlyFileMode)
}

// TightenOpenFilePermissions 与 TightenFilePermissions 相同，但作用于已打开的句柄，
// 避免「先检查路径、再 chmod」之间被替换的窗口（日志每行 open 一次，顺路收紧）。
func TightenOpenFilePermissions(f *os.File) {
	if runtime.GOOS == "windows" || f == nil {
		return
	}
	_ = f.Chmod(ownerOnlyFileMode)
}
