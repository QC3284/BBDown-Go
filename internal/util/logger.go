package util

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// MaxLogFieldLen bounds one server-controlled value inside a log line, so a
// hostile response cannot flood the terminal or the log file.
const MaxLogFieldLen = 512

// ANSI sequences mirroring the .NET ConsoleColor names the upstream C# uses.
//
// ConsoleColor.Red / Cyan / White are the *bright* variants of their colors, so they
// map to the 9x SGR codes; printing 31/36/37 would render errors, prompts and the
// banner darker than upstream on every terminal.
const (
	AnsiReset      = "\033[0m"
	AnsiBlack      = "\033[30m"  // ConsoleColor.Black      (二维码深色模块)
	AnsiRed        = "\033[91m"  // ConsoleColor.Red        (LogError)
	AnsiDarkYellow = "\033[33m"  // ConsoleColor.DarkYellow (LogWarn)
	AnsiCyan       = "\033[96m"  // ConsoleColor.Cyan       (LogColor / 提示符)
	AnsiDarkGray   = "\033[90m"  // ConsoleColor.DarkGray   (LogDebug)
	AnsiWhite      = "\033[97m"  // ConsoleColor.White      (横幅前景)
	AnsiBgDarkBlue = "\033[44m"  // ConsoleColor.DarkBlue   (横幅背景)
	AnsiBgRed      = "\033[101m" // ConsoleColor.Red       (失败输出底色，同样取亮色档)
)

// SanitizeLogString makes a value safe to interpolate into a single log line.
// Server-controlled text (video titles, uploader names, API messages, values
// derived from a request URL) can carry CR/LF and ANSI escapes that forge log
// lines or rewrite the terminal (upstream RF-54 / RF-70). Control characters
// become spaces and the result is truncated. Legitimate values pass through
// unchanged.
func SanitizeLogString(s string) string {
	if s == "" {
		return s
	}
	clean := true
	for i := 0; i < len(s); i++ {
		if c := s[i]; c < 0x20 || c == 0x7f {
			clean = false
			break
		}
	}
	if clean {
		return truncateRunes(s, MaxLogFieldLen)
	}
	var sb strings.Builder
	sb.Grow(len(s))
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			sb.WriteByte(' ')
			continue
		}
		sb.WriteRune(r)
	}
	return truncateRunes(strings.Join(strings.Fields(sb.String()), " "), MaxLogFieldLen)
}

func truncateRunes(s string, max int) string {
	if max <= 0 {
		return s
	}
	n := 0
	for i := range s {
		if n == max {
			return s[:i] + "…"
		}
		n++
	}
	return s
}

// sanitizeLogArg neutralises the argument forms that can carry server text.
// Values are sanitised per argument rather than on the rendered line so that a
// caller's own intentional formatting (indentation, prefixes) is preserved.
func sanitizeLogArg(v interface{}) interface{} {
	switch t := v.(type) {
	case string:
		return SanitizeLogString(t)
	case error:
		return errors.New(SanitizeLogString(t.Error()))
	default:
		return v
	}
}

func sanitizeLogArgs(args []interface{}) []interface{} {
	out := make([]interface{}, len(args))
	for i, a := range args {
		out[i] = sanitizeLogArg(a)
	}
	return out
}

// ConsoleLock 串行化进程内的所有控制台写入：日志与进度条共用它。
//
// 上游 BBDown.Core.Logger.ConsoleLock 就是被 BBDown/Infrastructure/ProgressBar 复用的同一把锁；
// 不加锁时进度条重绘会与日志写入互相插入，终端上留下半行进度残影（实测：下载结束打
// 「合并分片...」时，上一帧的 25.07% 尾巴连同它一起显示成两行进度）。
var ConsoleLock sync.Mutex

// logSink 保存一次「日志让位」的目标流（见 RedirectConsoleLogs）。
type logSink struct{ w io.Writer }

// consoleLogSink 是当前生效的让位目标；nil = 没有让位。
//
// 用原子指针而不是普通变量：后台协程（如更新检查）也会写日志，而读取发生在 ConsoleLock 之外。
var consoleLogSink atomic.Pointer[logSink]

// consoleLogWriter 解析一条日志行的落点。
//
// 默认路径**每次写入都重新解析 os.Stdout**，而不是把它存在变量里：本仓测试的既有约定是
// 「替换 os.Stdout 捕获输出」（internal/cli 与 internal/util 各有一份 captureStdout）。
// 目标一旦被冻结在变量里，替换 os.Stdout 就再也截不到日志——f828cba 的坑之一。
func consoleLogWriter() io.Writer {
	if s := consoleLogSink.Load(); s != nil {
		return s.w
	}
	return os.Stdout
}

// RedirectConsoleLogs 在**本次作用域**内把日志行（含给进度条那一行收尾的换行）写到 w，
// 返回还原函数——调用方必须 defer 它。
//
// 用途是机读模式（--info-json / doctor --json）：日志让位到 stderr，stdout 只留数据，
// jq / 脚本才拿得到干净输入。之所以是「作用域」而不是全局开关：作用域退出即还原成调用前的
// 落点，作用域之外的代码与同包用例拿回 os.Stdout——全局可变目标会跨用例泄漏，让后来的用例
// 捕获到空串（f828cba 的坑之二）。
func RedirectConsoleLogs(w io.Writer) (restore func()) {
	prev := consoleLogSink.Swap(&logSink{w: w})
	return func() { consoleLogSink.Store(prev) }
}

// progressLineActive 表示终端当前行上停着一条尚未收尾的进度条。
var progressLineActive atomic.Bool

// SetProgressLineActive 由进度条在绘制/擦除时告知日志：这一行现在归进度条。
func SetProgressLineActive(active bool) { progressLineActive.Store(active) }

// consoleWrite 在 ConsoleLock 之下执行一次完整的控制台写入。
//
// 写日志前先给进度条那一行收尾（换行）：进度条是原地重绘的，日志若直接接在它后面，
// 下一帧重绘会回到行首把这条日志整行擦掉。
func consoleWrite(write func(w io.Writer)) {
	ConsoleLock.Lock()
	defer ConsoleLock.Unlock()
	w := consoleLogWriter()
	if progressLineActive.Swap(false) {
		fmt.Fprint(w, "\n")
	}
	write(w)
}

// Logger provides thread-safe, colored console logging with optional file output.
type Logger struct {
	mu          sync.Mutex
	logFilePath string
	debugMode   func() bool // callback to check if debug is enabled

	logFailures       int
	logSuspendedUntil time.Time
}

// NewLogger creates a new Logger.
func NewLogger(debugFn func() bool) *Logger {
	return &Logger{
		debugMode: debugFn,
	}
}

// SetLogFile sets the path for persistent log file output.
func (l *Logger) SetLogFile(path string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.logFilePath = path
	// 启动时把已存在的日志文件收紧到属主可读（upstream #113：进程私有文件 0600）。
	// 之后新创建的文件由 writeLine 的 SecureFileMode + 句柄收紧紧跟上。
	TightenFilePermissions(path)
}

// logFailureCooldown is how long file logging is suspended after
// maxLogFailures consecutive write failures: a full or read-only volume would
// otherwise retry (and fail) on every single line.
const (
	maxLogFailures     = 5
	logFailureCooldown = 30 * time.Second
)

func (l *Logger) appendToFile(line string) {
	l.mu.Lock()
	path := l.logFilePath
	if path == "" || time.Now().Before(l.logSuspendedUntil) {
		l.mu.Unlock()
		return
	}
	l.mu.Unlock()

	// The file is opened per line rather than kept open: Go already opens with
	// FILE_SHARE_READ|WRITE|DELETE, so a second instance is not locked out, and a
	// long-lived handle would go stale if the file is rotated or removed.
	if err := writeLine(path, line); err != nil {
		l.mu.Lock()
		l.logFailures++
		if l.logFailures >= maxLogFailures {
			l.logSuspendedUntil = time.Now().Add(logFailureCooldown)
			l.logFailures = 0
			fmt.Fprintf(os.Stderr, "日志文件写入连续失败，已暂停 %v: %s\n", logFailureCooldown, path)
		}
		l.mu.Unlock()
		return
	}

	l.mu.Lock()
	l.logFailures = 0
	l.mu.Unlock()
}

// writeLine appends one line, flushing before close.
func writeLine(path, line string) error {
	// 进程私有的日志文件：Unix 上创建即 0600（Windows 保留 0644）。
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, SecureFileMode())
	if err != nil {
		return err
	}
	defer f.Close()
	// 升级前留下的日志文件可能仍是 0644，每行打开时顺路收紧（Unix；Windows no-op）。
	TightenOpenFilePermissions(f)
	if _, err := fmt.Fprintln(f, line); err != nil {
		return err
	}
	return f.Sync()
}

func timestamp() string {
	return time.Now().Format("[2006-01-02 15:04:05.000]")
}

// ---- t64 ②：常规事件行的屏幕宽度治理（截断 +「…」）----
//
// 有意偏离上游：上游的事件行不折行也不截断，超宽时由终端自己折行——续行没有缩进，
// 时间戳与正文错位（登记 UPSTREAM_ALIGNMENT §4.71，登记动作由队长收尾做）。
//
// 只治理「常规事件行」（带时间戳的 Log / LogWarn / LogError / LogColor）：
//   - 流表 / 侧车 / 清单走 LogColorNoTime*（无时间戳）——它们各自已有宽度契约（t42/t39），这里不动；
//   - --debug 行有自己的 120 列常量（t42），本步**不动**（t64 明确列为「候选，另议」）。
//
// 日志文件仍写**全文**：屏幕是给人看的、文件是给排查用的，两者取舍不同（与 t42 的 --debug 同一原则）。
func eventLineBudget() int {
	if budget := TerminalWidth() - LogIndentWidth; budget > 16 {
		return budget
	}
	return 16
}

func shortForConsole(msg string) string {
	// **只在交互终端**截断：管道 / 重定向（脚本、CI 日志、把输出写进文件）保留全文——它们没有
	// 「终端列宽」这个约束，截断只会丢信息（TerminalWidth 本身也是这么区分 TTY 的：
	// 非 TTY 不读 COLUMNS，直接按默认宽度排版）。
	if !stdoutIsTerminal() {
		return msg
	}
	return TruncateDisplay(msg, eventLineBudget())
}

// Log prints a normal log line.
func (l *Logger) Log(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, sanitizeLogArgs(args)...)
	line := timestamp() + " - " + msg
	short := timestamp() + " - " + shortForConsole(msg)
	consoleWrite(func(w io.Writer) { fmt.Fprintln(w, short) })
	l.appendToFile(line) // 文件写全文
}

// LogError prints an error line in red.
func (l *Logger) LogError(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, sanitizeLogArgs(args)...)
	line := timestamp() + " - " + msg
	consoleWrite(func(w io.Writer) {
		fmt.Fprint(w, timestamp()+" - ")
		fmt.Fprint(w, AnsiRed+shortForConsole(msg)+AnsiReset+"\n")
	})
	l.appendToFile(line) // 文件写全文
}

// LogWarn prints a warning line in yellow.
func (l *Logger) LogWarn(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, sanitizeLogArgs(args)...)
	line := timestamp() + " - " + msg
	consoleWrite(func(w io.Writer) {
		fmt.Fprint(w, timestamp()+" - ")
		fmt.Fprint(w, AnsiDarkYellow+shortForConsole(msg)+AnsiReset+"\n")
	})
	l.appendToFile(line) // 文件写全文
}

// LogColorNoTime prints a colored line in cyan without timestamp, indented to align.
func (l *Logger) LogColorNoTime(format string, args ...interface{}) {
	l.LogColorNoTimeIndent(LogIndentWidth, format, args...)
}

// LogColorNoTimeIndent 与 LogColorNoTime 同一条通道、同一套配色，只有行首缩进量由调用方给出。
//
// 为什么需要它：清单与表头在窄终端里必须把 28 列的缩进回收给数据列（40 列终端上 28 列缩进
// 只剩 12 列可用，整行会退化成只剩一个省略号）。宽终端下调用方仍传 LogIndentWidth，
// 输出与改前逐字节相同。
func (l *Logger) LogColorNoTimeIndent(indent int, format string, args ...interface{}) {
	msg := fmt.Sprintf(format, sanitizeLogArgs(args)...)
	if indent < 0 {
		indent = 0
	}
	pad := strings.Repeat(" ", indent)
	consoleWrite(func(w io.Writer) { fmt.Fprint(w, pad+AnsiCyan+msg+AnsiReset+"\n") })
	// 文件日志沿用既有的一格额外缩进（与 LogColorNoTime 的写入口径一致）。
	l.appendToFile(" " + pad + msg)
}

// LogColor prints a colored line in cyan.
func (l *Logger) LogColor(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, sanitizeLogArgs(args)...)
	line := timestamp() + " - " + msg
	consoleWrite(func(w io.Writer) {
		fmt.Fprint(w, timestamp()+" - ")
		fmt.Fprint(w, AnsiCyan+shortForConsole(msg)+AnsiReset+"\n")
	})
	l.appendToFile(line) // 文件写全文
}

// LogDebug prints a debug line in grey (only when debug mode is on).
func (l *Logger) LogDebug(format string, args ...interface{}) {
	if l.debugMode == nil || !l.debugMode() {
		return
	}
	msg := fmt.Sprintf(format, sanitizeLogArgs(args)...)
	line := timestamp() + " - " + msg
	consoleWrite(func(w io.Writer) { fmt.Fprint(w, AnsiDarkGray+line+AnsiReset+"\n") })
	l.appendToFile(line)
}

// Printf prints without timestamp prefix (for interactive prompts).
func (l *Logger) Printf(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	consoleWrite(func(w io.Writer) { fmt.Fprint(w, msg) })
}

// Default package-level logger.
var defaultLogger = NewLogger(nil)

// SetLogFile 让默认 logger 同时把日志写入 path（追加）。
//
// 实现早就在 Logger.SetLogFile 里（含写失败自动挂起与恢复提示，对齐上游 AliverAnme 的文件日志），
// 但此前**没有任何 CLI 入口调用它**——功能存在却不可达。本包装 + --log-file 开关把它接上。
func SetLogFile(path string) { defaultLogger.SetLogFile(path) }

// SetDefaultDebugFn sets the debug callback for the default logger.
func SetDefaultDebugFn(fn func() bool) {
	defaultLogger.debugMode = fn
}

// DebugLogEnabled reports whether debug logging is currently enabled.
func DebugLogEnabled() bool {
	return defaultLogger.debugMode != nil && defaultLogger.debugMode()
}

// Log is the package-level convenience function.
func Log(format string, args ...interface{}) {
	defaultLogger.Log(format, args...)
}

// LogError is the package-level convenience function.
func LogError(format string, args ...interface{}) {
	defaultLogger.LogError(format, args...)
}

// LogWarn is the package-level convenience function.
func LogWarn(format string, args ...interface{}) {
	defaultLogger.LogWarn(format, args...)
}

// LogColor is the package-level convenience function.
func LogColor(format string, args ...interface{}) {
	defaultLogger.LogColor(format, args...)
}

// LogColorNoTime prints colored text without timestamp prefix.
func LogColorNoTime(format string, args ...interface{}) {
	defaultLogger.LogColorNoTime(format, args...)
}

// LogColorNoTimeIndent prints colored text without timestamp prefix, with a caller-chosen indent.
func LogColorNoTimeIndent(indent int, format string, args ...interface{}) {
	defaultLogger.LogColorNoTimeIndent(indent, format, args...)
}

// LogDebug is the package-level convenience function.
func LogDebug(format string, args ...interface{}) {
	defaultLogger.LogDebug(format, args...)
}

// GetLogger returns the default logger instance.
func GetLogger() *Logger {
	return defaultLogger
}
