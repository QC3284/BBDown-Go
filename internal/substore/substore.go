package substore

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/QC3284/BBDown-Go/internal/util"
)

// Subscription is a persisted subscription entry.
type Subscription struct {
	Target  string `json:"Target"`
	Name    string `json:"Name"`
	AddedAt int64  `json:"AddedAt"`
	// Filter 是可选的稿件标题正则（F6）：`sub check` 只下载标题匹配的新稿。
	// 旧清单没有这个字段，反序列化为空串 = 不过滤，因此升级不需要迁移。
	Filter string `json:"Filter,omitempty"`
}

// CompileFilter 编译订阅的标题过滤正则；空 Filter 表示不过滤（返回 nil, nil）。
// 清单可以被手改，非法正则在这里变成错误，由调用方跳过该订阅，
// 而不是当成「全部通过」——那会把用户明确排除的稿件也下下来。
func (s Subscription) CompileFilter() (*regexp.Regexp, error) {
	if s.Filter == "" {
		return nil, nil
	}
	re, err := regexp.Compile(s.Filter)
	if err != nil {
		return nil, fmt.Errorf("订阅 %s 的标题过滤正则无效(%q): %v", s.Target, s.Filter, err)
	}
	return re, nil
}

// validateFilter 在写入前校验过滤正则：非法正则在 `sub add` 当场报错，
// 而不是等 `sub check` 才发现——那时订阅已经进了清单，用户以为加上了。
func validateFilter(filter string) error {
	if filter == "" {
		return nil
	}
	if _, err := regexp.Compile(filter); err != nil {
		return fmt.Errorf("标题过滤正则无效 %q: %v", filter, err)
	}
	return nil
}

// CorruptError signals corrupted persistence data; callers must abort the whole
// batch instead of continuing with an untrustworthy history (upstream
// SubscriptionDataCorruptException).
type CorruptError struct{ msg string }

func (e *CorruptError) Error() string { return e.msg }

// StoreRoot is the directory holding the subscription files (exe dir by default).
var StoreRoot = exeDir()

var ioLock sync.Mutex

// maxHistoryPerTarget bounds the per-subscription history: large enough that a
// normal subscription never reaches it, small enough to keep parsing cheap.
const maxHistoryPerTarget = 5000

func exeDir() string {
	return util.ExecutableDir()
}

func subFile() string     { return filepath.Join(StoreRoot, "BBDownSubscriptions.json") }
func historyFile() string { return filepath.Join(StoreRoot, "BBDownSubscriptions.history.json") }

// atomicWrite replaces the file atomically (temp + rename).
func atomicWrite(path string, v interface{}) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.%d.tmp", path, time.Now().UnixNano())
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// isolateCorruptFile quarantines a corrupt file.
func isolateCorruptFile(path string) string {
	corrupt := fmt.Sprintf("%s.corrupt-%s-%d", path, time.Now().Format("20060102150405.000"), time.Now().UnixNano())
	if err := os.Rename(path, corrupt); err != nil {
		return ""
	}
	return corrupt
}

// isolatedCopyOf returns the newest quarantined copy of path ("<name>.corrupt-*",
// written by isolateCorruptFile), or "" when none exists. os.ReadDir sorts entries
// by filename, so the last match is the most recent timestamp.
func isolatedCopyOf(path string) string {
	dir := filepath.Dir(path)
	prefix := filepath.Base(path) + ".corrupt-"
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	found := ""
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), prefix) {
			found = filepath.Join(dir, e.Name())
		}
	}
	return found
}

// Load returns the subscription list. Corruption aborts with CorruptError.
func Load() ([]Subscription, error) {
	data, err := os.ReadFile(subFile())
	if err != nil {
		if os.IsNotExist(err) {
			// A missing file with a quarantined sibling means an earlier run already
			// isolated a corrupt manifest: treating this as an empty store would hide
			// the data loss and let the next write start over (upstream 1.7.9
			// ThrowIfIsolatedFileExists).
			if isolated := isolatedCopyOf(subFile()); isolated != "" {
				return nil, &CorruptError{fmt.Sprintf(
					"订阅清单 %s 缺失，但发现隔离副本 %s（上次读取失败时已隔离）。请恢复清单文件，或确认放弃数据后把隔离副本移出目录再重试",
					subFile(), isolated)}
			}
			return nil, nil
		}
		return nil, &CorruptError{fmt.Sprintf("读取订阅清单失败: %v", err)}
	}
	var subs []Subscription
	if err := json.Unmarshal(data, &subs); err != nil {
		corrupt := isolateCorruptFile(subFile())
		util.LogError("订阅清单损坏（%v），已隔离为 %s，中止操作以避免覆盖订阅", err, corrupt)
		return nil, &CorruptError{fmt.Sprintf("订阅清单损坏，已隔离为 %s，请检查后恢复", corrupt)}
	}
	return subs, nil
}

// Add appends a subscription (replacing an existing one with the same target).
// filter 是可选的稿件标题正则；非法正则当场报错，不写清单。
func Add(target, name, filter string) error {
	if err := validateFilter(filter); err != nil {
		return err
	}
	ioLock.Lock()
	defer ioLock.Unlock()
	subs, err := Load()
	if err != nil {
		return err
	}
	if name == "" {
		name = target
	}
	for i := range subs {
		if subs[i].Target == target {
			subs[i].Name = name
			subs[i].Filter = filter
			return atomicWrite(subFile(), subs)
		}
	}
	subs = append(subs, Subscription{Target: target, Name: name, Filter: filter, AddedAt: time.Now().Unix()})
	return atomicWrite(subFile(), subs)
}

// Remove deletes a subscription by target.
func Remove(target string) error {
	ioLock.Lock()
	defer ioLock.Unlock()
	subs, err := Load()
	if err != nil {
		return err
	}
	out := subs[:0]
	for _, s := range subs {
		if s.Target != target {
			out = append(out, s)
		}
	}
	return atomicWrite(subFile(), out)
}

// ListSorted returns subscriptions ordered by AddedAt.
func ListSorted() ([]Subscription, error) {
	subs, err := Load()
	if err != nil {
		return nil, err
	}
	sort.SliceStable(subs, func(i, j int) bool { return subs[i].AddedAt < subs[j].AddedAt })
	return subs, nil
}

// LoadHistory returns the downloaded aid history for a target.
func LoadHistory(target string) ([]string, error) {
	data, err := os.ReadFile(historyFile())
	if err != nil {
		if os.IsNotExist(err) {
			if isolated := isolatedCopyOf(historyFile()); isolated != "" {
				return nil, &CorruptError{fmt.Sprintf(
					"订阅历史 %s 缺失，但发现隔离副本 %s（上次读取失败时已隔离）。请恢复历史文件，或确认放弃数据后把隔离副本移出目录再重试",
					historyFile(), isolated)}
			}
			return nil, nil
		}
		return nil, &CorruptError{fmt.Sprintf("读取订阅历史失败: %v", err)}
	}
	var m map[string][]string
	if err := json.Unmarshal(data, &m); err != nil {
		corrupt := isolateCorruptFile(historyFile())
		util.LogError("订阅历史损坏（%v），已隔离为 %s，中止操作以避免重复下载", err, corrupt)
		return nil, &CorruptError{fmt.Sprintf("订阅历史损坏，已隔离为 %s，请检查后恢复", corrupt)}
	}
	return m[target], nil
}

// RecordDownloaded marks an aid as downloaded for a target.
func RecordDownloaded(target, aid string) error {
	ioLock.Lock()
	defer ioLock.Unlock()
	var m map[string][]string
	data, err := os.ReadFile(historyFile())
	if err == nil {
		if jerr := json.Unmarshal(data, &m); jerr != nil {
			corrupt := isolateCorruptFile(historyFile())
			util.LogError("订阅历史损坏（%v），已隔离为 %s，中止操作以避免重复下载", jerr, corrupt)
			return &CorruptError{fmt.Sprintf("订阅历史损坏，已隔离为 %s，请检查后恢复", corrupt)}
		}
	}
	if m == nil {
		m = make(map[string][]string)
	}
	// Move an already-recorded aid to the end rather than ignoring it: the list is
	// ordered by recency and the cap below evicts from the front.
	list := m[target]
	out := list[:0]
	for _, a := range list {
		if a != aid {
			out = append(out, a)
		}
	}
	out = append(out, aid)
	// Bound the history: it otherwise grows forever and every `sub check` parses it
	// in full (upstream keeps the most recent MaxHistoryPerTarget entries).
	if len(out) > maxHistoryPerTarget {
		out = out[len(out)-maxHistoryPerTarget:]
	}
	m[target] = out
	return atomicWrite(historyFile(), m)
}
