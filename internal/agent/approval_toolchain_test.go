package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/magicwubiao/go-magic/internal/agent/hooks"
	"github.com/magicwubiao/go-magic/internal/approval"
)

// 背景（2026-10-16）：工具层的命令清单（tool.allowedCommands）默认改为软策略——
// 未列出的命令不再由工具硬拦，而是落到审批层判定。为了让"软"不等于"静默放行"，
// 审批钩子把"不在标准工具链清单"的命令升级为询问：smartApprove 原本会给
// 低风险命令（如 `foo_bar_baz --flag`）静默放行，升级后必须询问用户。
//
// 三件事必须同时成立：
//  1. 未列出的命令 → 询问（而不是静默通过）；
//  2. 标准工具链命令（含技能自荐的 npx）→ 照旧自动放行，不打扰用户；
//  3. strategy=auto → 尊重用户的"都别问"，不升级。

func newSmartHook(t *testing.T) *ApprovalHook {
	t.Helper()
	t.Setenv("GO_MAGIC_HOME", t.TempDir())
	// 用生产默认配置（含危险模式黑名单与只读白名单），只覆盖策略。
	cfg := approval.DefaultConfig()
	cfg.Strategy = approval.StrategySmart
	mgr, err := approval.NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager error: %v", err)
	}
	waitForApprovalFlush(t, mgr)
	return NewApprovalHookWithManager(mgr)
}

// askedUser 用 promptFunc 是否被调用作为"确实问了用户"的判据：
// 返回 false（拒绝）让断言可观测，同时避免测试阻塞在 CLI 提示上。
func askedUser(h *ApprovalHook) *bool {
	asked := false
	h.SetPromptFunc(func(command, reason string, riskLevel approval.RiskLevel) bool {
		asked = true
		return false
	})
	return &asked
}

func TestUnlistedCommandIsEscalatedToUser(t *testing.T) {
	h := newSmartHook(t)
	asked := askedUser(h)

	_, dec, err := h.BeforeTool(context.Background(), &hooks.ToolCallHookRequest{
		ToolName: "execute_command",
		ToolArgs: map[string]interface{}{"command": "foo_bar_baz --flag"},
	})
	if err != nil {
		t.Fatalf("BeforeTool error: %v", err)
	}
	if !*asked {
		t.Fatal("unlisted command must be escalated to the user, not auto-approved")
	}
	if dec.Action != hooks.HookActionReject {
		t.Fatalf("action = %v (reason=%q), want Reject after the user declined", dec.Action, dec.Reason)
	}
	if !strings.Contains(dec.Reason, "foo_bar_baz") {
		t.Errorf("reason should name the command, got %q", dec.Reason)
	}
}

// 技能正文直接建议运行的 npx/gofmt 必须在标准清单里，走自动放行、不打扰用户。
func TestStandardToolchainCommandIsNotEscalated(t *testing.T) {
	for _, cmd := range []string{
		"npx eslint --fix src/",
		"gofmt -l .",
		"dlv test ./...",
	} {
		h := newSmartHook(t)
		asked := askedUser(h)

		_, dec, err := h.BeforeTool(context.Background(), &hooks.ToolCallHookRequest{
			ToolName: "execute_command",
			ToolArgs: map[string]interface{}{"command": cmd},
		})
		if err != nil {
			t.Fatalf("BeforeTool(%q) error: %v", cmd, err)
		}
		if *asked {
			t.Errorf("%q is a standard toolchain command and must not prompt the user", cmd)
		}
		if dec.Action != hooks.HookActionContinue {
			t.Errorf("BeforeTool(%q) action = %v (reason=%q), want Continue", cmd, dec.Action, dec.Reason)
		}
	}
}

// strategy=auto 是用户在配置里显式表达"都别问"，升级规则不得推翻它。
func TestAutoStrategySkipsToolchainEscalation(t *testing.T) {
	t.Setenv("GO_MAGIC_HOME", t.TempDir())
	cfg := approval.DefaultConfig()
	cfg.Strategy = approval.StrategyAutoApprove
	mgr, err := approval.NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager error: %v", err)
	}
	waitForApprovalFlush(t, mgr)
	h := NewApprovalHookWithManager(mgr)
	asked := askedUser(h)

	_, dec, err := h.BeforeTool(context.Background(), &hooks.ToolCallHookRequest{
		ToolName: "execute_command",
		ToolArgs: map[string]interface{}{"command": "foo_bar_baz --flag"},
	})
	if err != nil {
		t.Fatalf("BeforeTool error: %v", err)
	}
	if *asked {
		t.Error("strategy=auto must not be overridden by the toolchain escalation")
	}
	if dec.Action != hooks.HookActionContinue {
		t.Fatalf("action = %v (reason=%q), want Continue", dec.Action, dec.Reason)
	}
}

// 危险命令仍然硬拒，不受软策略影响。
func TestDangerousCommandStillRejectedInSoftMode(t *testing.T) {
	h := newSmartHook(t)
	asked := askedUser(h)

	_, dec, err := h.BeforeTool(context.Background(), &hooks.ToolCallHookRequest{
		ToolName: "execute_command",
		ToolArgs: map[string]interface{}{"command": "rm -rf /"},
	})
	if err != nil {
		t.Fatalf("BeforeTool error: %v", err)
	}
	if *asked {
		t.Error("dangerous patterns should be rejected by policy, not escalated to the user")
	}
	if dec.Action != hooks.HookActionReject {
		t.Fatalf("action = %v (reason=%q), want Reject", dec.Action, dec.Reason)
	}
}
