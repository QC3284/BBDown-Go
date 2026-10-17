package main

import (
	"github.com/QC3284/BBDown-Go/internal/cli"
	"github.com/QC3284/BBDown-Go/internal/util"
)

// banner 是终端开场横幅（版本号硬编码点之一，见 internal/cli/version_consistency_test.go）。
//
// Banner: ConsoleColor.DarkBlue background + ConsoleColor.White text, which in
// ANSI terms is 44 + 97 (White is the bright variant, not 37).
//
// 打不打由 cli.Execute 决定：机读模式（--info-json / doctor --json）下不打，stdout 只留数据。
// 判定必须放在 Execute 里——它要看合并 BBDown.config 之后的参数，main 这里只看得到命令行。
var banner = util.AnsiBgDarkBlue + util.AnsiWhite + "BBDown version 3.1.0, Bilibili Downloader." + util.AnsiReset + "\r\n" +
	"遇到问题请首先到以下地址查阅有无相关信息：\r\n" +
	"https://github.com/QC3284/BBDown-Go/issues\r\n" +
	"\n"

func main() {
	cli.Execute(banner)
}
