package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/QC3284/BBDown-Go/internal/util"
)

// t81：self-update（吸收 BBDownT 2.1.3）——独立二进制的自更新命令。
//
// 真正干活的部分全在 internal/util/update.go（可离线用例化：假更新服务器三态、校验失败不动原文件、
// 替换失败回滚）。本文件只做三件事：把命令挂到根命令、把「当前版本」按 --version 的口径传下去、
// 把结果翻译成给人看的行。

// selfUpdateRunner 是命令层的接缝：用例替换它就不需要网络、也不会真的替换测试二进制。
var selfUpdateRunner = util.SelfUpdate

var selfUpdateCmd = &cobra.Command{
	Use:     "self-update",
	Aliases: []string{"update"},
	Short:   "更新到最新 Release（版本口径与 --version 相同）",
	Long: `从官方仓库（QC3284/BBDown-Go）的 GitHub Releases 拉取最新版本并原地替换当前可执行文件。

版本口径与「BBDown --version」完全相同：Releases 的 tag 去掉 v 前缀后与当前版本逐字比较，
相同即视为「已是最新」，不做任何下载。

安全边界（每一步失败都中止，且不会改动原可执行文件）：
  - 只查官方仓库的 Releases API，资产名必须是当前平台的官方命名；
  - 下载地址必须是 https 且落在 GitHub 官方主机上；
  - 资产必须带 SHA256 摘要，下载时边下边校验，不一致即中止；
  - 替换走「同目录临时文件 + rename」，Windows 上先给旧文件让位，失败则回滚。

注意：只替换主二进制；归档里的其它内置件（mp4decrypt / device.wvd）不会自动更新，
需要时请从 Release 页面手动补齐。`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		exe, err := os.Executable()
		if err != nil {
			return fmt.Errorf("self-update：定位当前可执行文件失败: %w", err)
		}
		ctx := cmd.Context()
		if ctx == nil {
			ctx = context.Background()
		}
		res, err := selfUpdateRunner(ctx, util.SelfUpdateConfig{
			CurrentVersion: rootCmd.Version, // 与 --version 同一口径
			ExecutablePath: exe,
		})
		if err != nil {
			return err
		}
		if !res.Updated {
			util.Log("已是最新版本 %s（与 --version 口径一致）", res.FromVersion)
			return nil
		}
		util.Log("已更新: %s → %s（%s，%.1f MiB）", res.FromVersion, res.ToVersion, res.AssetName, float64(res.DownloadedBytes)/(1<<20))
		util.Log("提示: 归档里的其它内置件（mp4decrypt / device.wvd）未自动替换，需要时请从 Release 页手动补齐")
		return nil
	},
}

// init 里注册：root.go 的命令注册区（rootCmd.AddCommand 汇总）不在 t81 的 inScope，
// 所以这个命令自己挂上去。scope 打开后把这一行挪进 root.go 的注册区、删掉本 init 即可。
func init() {
	rootCmd.AddCommand(selfUpdateCmd)
}
