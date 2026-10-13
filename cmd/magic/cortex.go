package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/magicwubiao/go-magic/internal/cortex"
	"github.com/magicwubiao/go-magic/pkg/config"
	"github.com/spf13/cobra"
)

// cortexCmd 是自进化运维的 CLI 入口。
//
// 存在理由：SOUL.md 的裁剪/拒绝指标、快照回滚、GEPA 收敛状态此前只存在于
// 内部类型上，没有任何入口。SetSoul 在拒绝写入时会提示
// `magic cortex soul compact`，但该命令此前并不存在 —— 运维看到告警却无从下手。
var cortexCmd = &cobra.Command{
	Use:   "cortex",
	Short: "Inspect and maintain the cortex self-evolution state",
	Long: `Inspect and maintain Magic's cortex (self-evolution) state.

Two things grow on their own inside ~/.magic/cortex and both can go wrong silently:
  - SOUL.md      the personality file that GEPA injects generated content into
  - gepa/        the evolution history (generations)

This command exposes the health signals that are otherwise invisible:

Examples:
  magic cortex soul stats            SOUL write metrics (refused / head cuts avoided)
  magic cortex soul snapshots        list rollback points
  magic cortex soul rollback latest  restore the most recent snapshot
  magic cortex soul compact          reclaim space used by injected content
  magic cortex gepa                  evolution engine metrics (converged? last run?)`,
}

var cortexSoulCmd = &cobra.Command{
	Use:   "soul",
	Short: "Maintain SOUL.md (personality file)",
}

var cortexSoulStatsCmd = &cobra.Command{
	Use:   "stats",
	Short: "Show SOUL.md write metrics",
	RunE: func(cmd *cobra.Command, args []string) error {
		sm, err := openSoulManager()
		if err != nil {
			return err
		}
		return printJSON(sm.GetStats())
	},
}

var cortexSoulSnapshotsCmd = &cobra.Command{
	Use:   "snapshots",
	Short: "List available SOUL.md rollback points",
	RunE: func(cmd *cobra.Command, args []string) error {
		sm, err := openSoulManager()
		if err != nil {
			return err
		}
		snaps := sm.ListSnapshots()
		if len(snaps) == 0 {
			fmt.Println("no snapshots yet (snapshots are taken before each overwrite)")
			return nil
		}
		return printJSON(snaps)
	},
}

var cortexSoulRollbackCmd = &cobra.Command{
	Use:   "rollback [name]",
	Short: "Restore SOUL.md from a snapshot (default: latest)",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		sm, err := openSoulManager()
		if err != nil {
			return err
		}
		name := "latest"
		if len(args) > 0 {
			name = args[0]
		}
		if err := sm.RollbackSoul(name); err != nil {
			return fmt.Errorf("rollback failed: %w", err)
		}
		fmt.Printf("SOUL.md restored from snapshot %q\n", name)
		return nil
	},
}

var cortexSoulCompactCmd = &cobra.Command{
	Use:   "compact",
	Short: "Drop auto-generated SOUL sections to reclaim space",
	RunE: func(cmd *cobra.Command, args []string) error {
		sm, err := openSoulManager()
		if err != nil {
			return err
		}
		// 从世代史推导"哪些文本是自动注入的"，避免误删手工内容。
		payloads := collectManagedPayloads()
		before := len(sm.GetSoul())
		dropped, err := sm.CompactSoul(payloads)
		if err != nil {
			return fmt.Errorf("compact failed: %w", err)
		}
		after := len(sm.GetSoul())
		fmt.Printf("SOUL.md compacted: %d bytes -> %d bytes, %d auto-generated section(s) dropped\n",
			before, after, dropped)
		if dropped == 0 {
			fmt.Println("(nothing to reclaim — all sections look hand-written)")
		}
		return nil
	},
}

var cortexGEPACmd = &cobra.Command{
	Use:   "gepa",
	Short: "Show self-evolution (GEPA) engine metrics",
	RunE: func(cmd *cobra.Command, args []string) error {
		dir := filepath.Join(config.GetMagicHome(), "cortex")
		store, err := cortex.NewTrajectoryStore(dir)
		if err != nil {
			return err
		}
		engine := cortex.NewGEPAEngine(dir, nil, store)
		engine.LoadHistory()
		return printJSON(engine.GetStats())
	},
}

// openSoulManager 打开 ~/.magic/cortex 下的 SOUL.md。
func openSoulManager() (*cortex.SoulManager, error) {
	dir := filepath.Join(config.GetMagicHome(), "cortex")
	sm := cortex.NewSoulManager(dir)
	if err := sm.Load(); err != nil {
		return nil, fmt.Errorf("load SOUL.md: %w", err)
	}
	return sm, nil
}

// collectManagedPayloads 从已落盘的世代文件里收集自动注入文本。
func collectManagedPayloads() []string {
	dir := filepath.Join(config.GetMagicHome(), "cortex", "gepa", "generations")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var gen struct {
			BestStrategy *struct {
				Changes []struct {
					NewContent string `json:"new_content"`
				} `json:"changes"`
			} `json:"best_strategy"`
		}
		if err := json.Unmarshal(data, &gen); err != nil || gen.BestStrategy == nil {
			continue
		}
		for _, c := range gen.BestStrategy.Changes {
			if len(c.NewContent) >= 60 && !seen[c.NewContent] {
				seen[c.NewContent] = true
				out = append(out, c.NewContent)
			}
		}
	}
	return out
}

// printJSON 统一输出 JSON（运维脚本消费）。
func printJSON(v interface{}) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func init() {
	cortexSoulCmd.AddCommand(cortexSoulStatsCmd)
	cortexSoulCmd.AddCommand(cortexSoulSnapshotsCmd)
	cortexSoulCmd.AddCommand(cortexSoulRollbackCmd)
	cortexSoulCmd.AddCommand(cortexSoulCompactCmd)
	cortexCmd.AddCommand(cortexSoulCmd)
	cortexCmd.AddCommand(cortexGEPACmd)
	rootCmd.AddCommand(cortexCmd)
}
