package agent

import (
	"testing"
	"time"

	"github.com/magicwubiao/go-magic/internal/approval"
)

// 背景（2026-10-20）：审批配置项必须名副其实。
//
//	① approval_timeout 以前完全不生效——cliTimeout 是硬编码 30s 常量，
//	   用户在设置页把超时改成 60/300，CLI 提示仍然 30 秒就超时；
//	② enable_cli_confirm 以前只被 Manager.CLIConfirm 读取，而那个方法零调用方，
//	   设置页上的开关是个假开关。

func TestCLITimeoutFollowsConfiguredApprovalTimeout(t *testing.T) {
	t.Setenv("GO_MAGIC_HOME", t.TempDir())

	cfg := approval.DefaultConfig()
	cfg.ApprovalTimeout = 300
	mgr, err := approval.NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager error: %v", err)
	}
	waitForApprovalFlush(t, mgr)

	h := NewApprovalHookWithManager(mgr)
	if got := h.CLITimeout(); got != 300*time.Second {
		t.Fatalf("CLITimeout() = %v, want 300s (must follow approval_timeout)", got)
	}

	// 热加载：manager 上改了超时，下一次提示前必须同步到 hook。
	mgr.SetApprovalTimeout(45)
	h.applyManagerConfig()
	if got := h.CLITimeout(); got != 45*time.Second {
		t.Fatalf("CLITimeout() after reload = %v, want 45s", got)
	}
}

// enable_cli_confirm=true 时，即使策略判定为自动放行，CLI 通道也要先问一次。
func TestEnableCLIConfirmEscalatesAutoApprovedToolCall(t *testing.T) {
	t.Setenv("GO_MAGIC_HOME", t.TempDir())

	cfg := approval.DefaultConfig()
	cfg.Strategy = approval.StrategySmart
	cfg.EnableCLIConfirm = true
	mgr, err := approval.NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager error: %v", err)
	}
	waitForApprovalFlush(t, mgr)

	h := NewApprovalHookWithManager(mgr)
	h.SetChannel(approval.ChannelCLI)
	// 用 promptFunc 当作"确实问了"的判据；不设 stdin 交互，避免测试阻塞。
	asked := false
	h.SetPromptFunc(func(command, reason string, risk approval.RiskLevel) bool {
		asked = true
		return false
	})
	// promptFunc 分支本身就会询问，所以这里直接验证判定函数：
	// 自动放行的结果在 CLI + 开关打开时应被升级。
	autoApproved := &approval.ApprovalResult{
		Approved:  true,
		Strategy:  approval.StrategySmart,
		Reason:    lowRiskAutoApproveReason,
		RiskLevel: approval.RiskLow,
	}
	if !h.cliConfirmEscalates("execute_command", autoApproved) {
		t.Fatal("enable_cli_confirm=true must escalate an auto-approved tool call in CLI")
	}

	// 开关关闭时不得升级。
	mgr.SetEnableCLIConfirm(false)
	if h.cliConfirmEscalates("execute_command", autoApproved) {
		t.Fatal("enable_cli_confirm=false must not escalate")
	}
	_ = asked
}

// 只读命令与白名单放行不应因为 enable_cli_confirm 被反复追问。
func TestEnableCLIConfirmSparesReadOnlyAndWhitelisted(t *testing.T) {
	t.Setenv("GO_MAGIC_HOME", t.TempDir())

	cfg := approval.DefaultConfig()
	cfg.Strategy = approval.StrategySmart
	cfg.EnableCLIConfirm = true
	mgr, err := approval.NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager error: %v", err)
	}
	waitForApprovalFlush(t, mgr)

	h := NewApprovalHookWithManager(mgr)
	h.SetChannel(approval.ChannelCLI)

	for _, reason := range []string{"Read-only command", "Command is whitelisted", "Trusted command pattern"} {
		res := &approval.ApprovalResult{Approved: true, Strategy: approval.StrategySmart, Reason: reason}
		if h.cliConfirmEscalates("execute_command", res) {
			t.Errorf("reason %q must not be escalated by enable_cli_confirm", reason)
		}
	}
}

// 无人值守通道没有可问的人：enable_cli_confirm 不得把放行升级成询问。
func TestEnableCLIConfirmIgnoredOnUnattendedChannel(t *testing.T) {
	t.Setenv("GO_MAGIC_HOME", t.TempDir())

	cfg := approval.DefaultConfig()
	cfg.EnableCLIConfirm = true
	mgr, err := approval.NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager error: %v", err)
	}
	waitForApprovalFlush(t, mgr)

	h := NewApprovalHookWithManager(mgr)
	h.SetChannel(approval.ChannelUnattended)
	h.SetNonInteractive(true)

	res := &approval.ApprovalResult{Approved: true, Strategy: approval.StrategyAutoNarrow, Reason: "Auto-narrow strategy: low/medium risk auto-approved"}
	if h.cliConfirmEscalates("execute_command", res) {
		t.Fatal("unattended channel must not escalate to a prompt nobody can answer")
	}
}
