package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/magicwubiao/go-magic/internal/usage"
	"github.com/magicwubiao/go-magic/pkg/config"
)

var usageDays int

var usageCmd = &cobra.Command{
	Use:   "usage",
	Short: "Show token usage statistics",
	Long: `Display token usage statistics from the usage manager.

Shows today's usage and historical trends including:
  - Total requests, input/output tokens
  - Token breakdown by model
  - Top sessions by consumption

Examples:
  magic usage           # Show today's stats
  magic usage -d 7     # Show last 7 days
  magic usage -d 30    # Show last 30 days (default)`,
	Run: runUsage,
}

func init() {
	rootCmd.AddCommand(usageCmd)
	usageCmd.Flags().IntVarP(&usageDays, "days", "d", 1, "Number of days to show")
}

func runUsage(cmd *cobra.Command, args []string) {
	usageDir := filepath.Join(config.GetMagicHome(), "usage")

	mgr, err := usage.NewManager(usageDir)
	if err != nil {
		fmt.Printf("Failed to load usage manager: %v\n", err)
		os.Exit(1)
	}

	// Today's stats
	today, _ := mgr.GetTodayStats()

	fmt.Println("=== Token Usage ===")
	fmt.Println()

	// Today's summary
	fmt.Println("Today:")
	fmt.Printf("  Requests:     %d\n", today.TotalRequests)
	fmt.Printf("  Input Tokens: %s\n", formatTokens(today.TotalInput))
	fmt.Printf("  Output Tokens:%s\n", formatTokens(today.TotalOutput))
	fmt.Printf("  Total Tokens: %s\n", formatTokens(today.TotalInput+today.TotalOutput))
	fmt.Println()

	// By model
	if len(today.ByModel) > 0 {
		fmt.Println("By Model:")
		for model, stats := range today.ByModel {
			fmt.Printf("  %s:\n", model)
			fmt.Printf("    Requests: %d | Tokens: %s\n",
				stats.Requests, formatTokens(stats.InputTokens+stats.OutputTokens))
		}
		fmt.Println()
	}

	// Historical stats
	if usageDays > 1 {
		insights, _ := mgr.GetInsights(usageDays)
		fmt.Printf("Last %d days:\n", usageDays)
		fmt.Printf("  Total Requests:  %d\n", insights.TotalRequests)
		fmt.Printf("  Total Tokens:    %s\n", formatTokens(insights.TotalTokens))
		fmt.Printf("  Avg Tokens/Req:  %.0f\n", insights.AvgTokensPerReq)
		fmt.Println()

		// Top models
		if len(insights.TopModels) > 0 {
			fmt.Println("Top Models:")
			for i, m := range insights.TopModels {
				if i >= 5 {
					break
				}
				fmt.Printf("  %d. %s: %s (%.1f%%)\n",
					i+1, m.Model, formatTokens(m.Tokens), m.Percentage)
			}
			fmt.Println()
		}

		// Top sessions
		sessions, _ := mgr.GetTopSessions(5)
		if len(sessions) > 0 {
			fmt.Println("Top Sessions:")
			for i, s := range sessions {
				fmt.Printf("  %d. %s: %s\n",
					i+1, s.SessionID[:8]+"...", formatTokens(s.TotalTokens))
			}
			fmt.Println()
		}
	}

	// Record count
	count := mgr.GetRecordsCount()
	fmt.Printf("Total records: %d\n", count)
}

func formatTokens(n int) string {
	if n >= 1_000_000 {
		return fmt.Sprintf("%.2fM", float64(n)/1_000_000)
	}
	if n >= 1_000 {
		return fmt.Sprintf("%.2fK", float64(n)/1_000)
	}
	return fmt.Sprintf("%d", n)
}
