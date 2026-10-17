package cli

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/QC3284/BBDown-Go/internal/util"
)

// t81 的命令层用例：只钉「接得对不对」——版本口径与 --version 同源、参数与错误直通、已注册。
// 自更新的行为（三态/校验/回滚）由 internal/util 的离线用例覆盖。

func TestSelfUpdateCommandWiring(t *testing.T) {
	if got := selfUpdateCmd.Name(); got != "self-update" {
		t.Errorf("命令名 = %q，want self-update", got)
	}
	if len(selfUpdateCmd.Aliases) == 0 || selfUpdateCmd.Aliases[0] != "update" {
		t.Errorf("别名缺少 update：%v", selfUpdateCmd.Aliases)
	}
	// 帮助文本必须写清「与 --version 同一口径」与官方仓库（用户据此判断更新来源）。
	for _, want := range []string{"--version", "QC3284/BBDown-Go", "SHA256"} {
		if !strings.Contains(selfUpdateCmd.Long, want) {
			t.Errorf("帮助文本缺少 %q：\n%s", want, selfUpdateCmd.Long)
		}
	}
	if err := selfUpdateCmd.Args(selfUpdateCmd, []string{"unexpected"}); err == nil {
		t.Error("self-update 不接受位置参数")
	}
	// 必须真的挂在根命令上（否则 `BBDown self-update` 跑不到）。
	found := false
	for _, c := range rootCmd.Commands() {
		if c.Name() == "self-update" {
			found = true
			break
		}
	}
	if !found {
		t.Error("self-update 未注册到根命令")
	}
}

func TestSelfUpdateCommandUsesVersionAndSeam(t *testing.T) {
	orig := selfUpdateRunner
	t.Cleanup(func() { selfUpdateRunner = orig })
	var got util.SelfUpdateConfig
	selfUpdateRunner = func(ctx context.Context, cfg util.SelfUpdateConfig) (util.SelfUpdateResult, error) {
		got = cfg
		return util.SelfUpdateResult{Updated: false, FromVersion: cfg.CurrentVersion, ToVersion: cfg.CurrentVersion}, nil
	}
	selfUpdateCmd.SetContext(context.Background())
	if err := selfUpdateCmd.RunE(selfUpdateCmd, nil); err != nil {
		t.Fatalf("已是最新路径不该报错：%v", err)
	}
	if got.CurrentVersion != rootCmd.Version {
		t.Errorf("传给自更新的版本 %q 与 --version 的 %q 不一致", got.CurrentVersion, rootCmd.Version)
	}
	if got.CurrentVersion == "" {
		t.Error("根命令 Version 为空：--version 与 self-update 会一起失真")
	}
	if got.ExecutablePath == "" {
		t.Error("应把当前可执行文件路径传下去（原子替换的目标）")
	}
}

func TestSelfUpdateCommandPropagatesError(t *testing.T) {
	orig := selfUpdateRunner
	t.Cleanup(func() { selfUpdateRunner = orig })
	want := errors.New("boom")
	selfUpdateRunner = func(ctx context.Context, cfg util.SelfUpdateConfig) (util.SelfUpdateResult, error) {
		return util.SelfUpdateResult{}, want
	}
	selfUpdateCmd.SetContext(context.Background())
	if err := selfUpdateCmd.RunE(selfUpdateCmd, nil); !errors.Is(err, want) {
		t.Errorf("错误应原样上抛（由 Execute 统一渲染失败块），实际 %v", err)
	}
}

func TestSelfUpdateCommandUpdatedPath(t *testing.T) {
	orig := selfUpdateRunner
	t.Cleanup(func() { selfUpdateRunner = orig })
	selfUpdateRunner = func(ctx context.Context, cfg util.SelfUpdateConfig) (util.SelfUpdateResult, error) {
		return util.SelfUpdateResult{Updated: true, FromVersion: "1.0.0", ToVersion: "1.0.1", AssetName: "BBDown_linux_amd64.tar.gz", DownloadedBytes: 3 << 20}, nil
	}
	selfUpdateCmd.SetContext(context.Background())
	if err := selfUpdateCmd.RunE(selfUpdateCmd, nil); err != nil {
		t.Fatalf("成功路径不该报错：%v", err)
	}
}
