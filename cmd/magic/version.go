package main

import (
	"fmt"
	"runtime"

	"github.com/magicwubiao/go-magic/internal/mcp"
	"github.com/magicwubiao/go-magic/internal/tool"
	"github.com/spf13/cobra"
)

// 这些变量通过 ldflags 注入
var (
	Version   = "dev"
	Commit    = "unknown"
	BuildDate = "unknown"
)

func init() {
	// MCP clientInfo 上报真实构建版本，而不是 mcp 包里的静态默认值。
	// "dev"（本地 go build）不上报，避免非 release 构建污染 server 端日志。
	if Version != "dev" {
		mcp.ClientVersion = Version
	}
	rootCmd.AddCommand(versionCmd)
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Show version information",
	Run:   runVersion,
}

func runVersion(cmd *cobra.Command, args []string) {
	fmt.Printf("go-magic version %s\n", Version)
	fmt.Println("A Go implementation of Magic Agent")
	fmt.Println("by Magic Tech")
	fmt.Println()
	fmt.Printf("Go Version: %s\n", runtime.Version())
	fmt.Printf("OS/Arch:    %s/%s\n", runtime.GOOS, runtime.GOARCH)
	fmt.Printf("Commit:     %s\n", Commit)
	fmt.Printf("Build Date: %s\n", BuildDate)
	fmt.Println()

	// 动态统计命令数（排除 help）
	cmds := rootCmd.Commands()
	cmdCount := 0
	for _, c := range cmds {
		if c.Name() != "help" {
			cmdCount++
		}
	}
	fmt.Printf("Commands: %d\n", cmdCount)

	// 动态统计工具数
	tools := tool.GetAllTools()
	fmt.Printf("Tools: %d\n", len(tools))

	fmt.Println("Providers: See config for available providers")
	fmt.Println("Systems: Config, Skills, Cron, Plugin, Prompt, Gateway")
	fmt.Println("\nFeatures:")
	fmt.Println("  - Health check endpoint: http://localhost:8081/health")
}
