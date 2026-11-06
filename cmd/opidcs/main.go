// opidcs 是 OpenIDCS 的命令行管理工具。
package main

import (
	"os"

	"github.com/OpenIDCSTeam/HostAgent-Go/internal/cli/cmd"
)

func main() {
	os.Exit(cmd.Execute())
}
