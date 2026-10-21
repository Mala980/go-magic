package approval

import (
	"testing"
)

// 背景（2026-10-20）：审批模型改成分层结构。
//
//	① 硬底线：只有真正灾难级、不可挽回的命令（rm -rf /、mkfs、dd 到裸盘、
//	   fork bomb、shutdown/reboot …）无条件拒绝，永不询问；
//	② 高风险但可批准：目录名前缀类删除（/root、/usr、/var、/etc …）降级到
//	   这一层——交互通道询问用户，无人值守通道由 auto_narrow 拒绝。
//
// 旧行为把 ② 也塞进 DangerousPatterns，后果是用户连批准入口都没有：agent
// 连自己数据目录下的 /root/.magic/... 都删不掉，设置页也没有任何开关能放开。

func newTestManager(t *testing.T, mutate func(*ApprovalConfig)) *Manager {
	t.Helper()
	t.Setenv("GO_MAGIC_HOME", t.TempDir())
	cfg := DefaultConfig()
	if mutate != nil {
		mutate(cfg)
	}
	mgr, err := NewManager(cfg)
	if err != nil {
		t.Fatalf("NewManager error: %v", err)
	}
	if mgr != nil {
		// 审批记录异步落盘，必须在 TempDir 删除前刷完。
		t.Cleanup(mgr.FlushPendingSaves)
	}
	return mgr
}

func TestCatastrophicCommandIsStillHardDenied(t *testing.T) {
	mgr := newTestManager(t, nil)

	for _, cmd := range []string{
		"rm -rf /",
		"mkfs.ext4 /dev/sda1",
		"dd if=/dev/zero of=/dev/sda",
		"shutdown -h now",
		"reboot",
	} {
		res, err := mgr.RequestApproval(&ApprovalRequest{
			Command: cmd,
			Channel: ChannelCLI,
		})
		if err != nil {
			t.Fatalf("%q: RequestApproval error: %v", cmd, err)
		}
		if res.Approved {
			t.Errorf("%q must never be approved", cmd)
		}
		if res.AskUser {
			t.Errorf("%q is a hard-deny pattern and must not be askable", cmd)
		}
		if res.RiskLevel != RiskCritical {
			t.Errorf("%q: risk = %v, want critical", cmd, res.RiskLevel)
		}
	}
}

// 目录前缀删除不再硬拒：交互通道必须给出"询问"而不是"拒绝"，
// 否则用户连批准入口都没有。
func TestDirectoryPrefixDeleteIsAskableNotHardDenied(t *testing.T) {
	mgr := newTestManager(t, nil)

	res, err := mgr.RequestApproval(&ApprovalRequest{
		Command: "rm -rf /root/.magic/skills/broken",
		Channel: ChannelCLI,
	})
	if err != nil {
		t.Fatalf("RequestApproval error: %v", err)
	}
	if res.Approved {
		t.Fatal("high-risk delete must not be auto-approved on an interactive channel")
	}
	if !res.AskUser {
		t.Fatalf("high-risk delete must be askable (got reason %q)", res.Reason)
	}
	if res.RiskLevel != RiskCritical {
		t.Errorf("risk = %v, want critical", res.RiskLevel)
	}
}

// 同一个命令在无人值守通道上不能被"询问"（没人能回答），必须直接拒绝，
// 且理由要能说清是哪个策略挡的。
func TestHighRiskDeniedOnUnattendedChannel(t *testing.T) {
	mgr := newTestManager(t, nil)

	res, err := mgr.RequestApproval(&ApprovalRequest{
		Command: "rm -rf /root/.magic/skills/broken",
		Channel: ChannelUnattended,
	})
	if err != nil {
		t.Fatalf("RequestApproval error: %v", err)
	}
	if res.Approved {
		t.Fatal("high-risk delete must not be approved on an unattended channel")
	}
	if res.AskUser {
		t.Fatal("unattended channel has nobody to ask; must not set AskUser")
	}
	if res.Strategy != StrategyAutoNarrow {
		t.Errorf("strategy = %q, want %q", res.Strategy, StrategyAutoNarrow)
	}
}

// 通道级策略解析：显式覆盖 > 全局 strategy > 无人值守兜底。
func TestEffectiveStrategyPerChannel(t *testing.T) {
	mgr := newTestManager(t, func(c *ApprovalConfig) {
		c.Strategy = StrategySmart
		c.ChannelOverrides = map[string]Strategy{
			string(ChannelGateway): StrategyAutoApprove,
		}
	})

	cases := []struct {
		channel Channel
		want    Strategy
	}{
		{ChannelGateway, StrategyAutoApprove}, // 显式覆盖
		{ChannelWeb, StrategySmart},           // 全局
		{ChannelCLI, StrategySmart},           // 全局
		{ChannelBot, StrategyAutoNarrow},      // 无人值守兜底
		{ChannelUnattended, StrategyAutoNarrow},
	}
	for _, c := range cases {
		if got := mgr.EffectiveStrategy(c.channel); got != c.want {
			t.Errorf("channel %q: strategy = %q, want %q", c.channel, got, c.want)
		}
	}
}

// 全局 strategy 不得泄漏到无人值守通道：生产上 strategy=auto（都别问）很常见，
// 若被后台任务继承，就会在没人看见的情况下执行高危命令。放开必须逐通道显式写。
func TestGlobalStrategyDoesNotLeakToUnattended(t *testing.T) {
	mgr := newTestManager(t, func(c *ApprovalConfig) {
		c.Strategy = StrategyAutoApprove
		c.ChannelOverrides = map[string]Strategy{string(ChannelUnattended): StrategyAutoApprove}
	})
	// 显式覆盖生效。
	if got := mgr.EffectiveStrategy(ChannelUnattended); got != StrategyAutoApprove {
		t.Fatalf("explicit override: strategy = %q, want %q", got, StrategyAutoApprove)
	}
	// 其它无人值守通道仍走兜底，不继承全局 auto。
	if got := mgr.EffectiveStrategy(ChannelBot); got != StrategyAutoNarrow {
		t.Fatalf("bot: strategy = %q, want %q (global auto must not leak)", got, StrategyAutoNarrow)
	}
}

// auto_narrow：低/中风险放行，高风险及以上拒绝。
func TestAutoNarrowAllowsLowRiskOnly(t *testing.T) {
	mgr := newTestManager(t, func(c *ApprovalConfig) { c.Strategy = StrategyAutoNarrow })

	low, err := mgr.RequestApproval(&ApprovalRequest{Command: "ls -la", Channel: ChannelUnattended})
	if err != nil {
		t.Fatalf("RequestApproval error: %v", err)
	}
	if !low.Approved {
		t.Fatalf("low risk command must be auto-approved under auto_narrow (reason %q)", low.Reason)
	}

	high, err := mgr.RequestApproval(&ApprovalRequest{Command: "rm -rf /root/.magic/skills/broken", Channel: ChannelUnattended})
	if err != nil {
		t.Fatalf("RequestApproval error: %v", err)
	}
	if high.Approved || high.AskUser {
		t.Fatalf("high risk command must be denied without asking (approved=%v ask=%v)", high.Approved, high.AskUser)
	}
}

// SetChannelOverrides 必须整体替换，否则用户在设置页删掉一条覆盖后旧值仍然生效。
func TestSetChannelOverridesReplaces(t *testing.T) {
	mgr := newTestManager(t, func(c *ApprovalConfig) {
		c.ChannelOverrides = map[string]Strategy{string(ChannelBot): StrategyAutoApprove}
	})
	if got := mgr.EffectiveStrategy(ChannelBot); got != StrategyAutoApprove {
		t.Fatalf("setup: strategy = %q", got)
	}
	mgr.SetChannelOverrides(map[string]Strategy{string(ChannelWeb): StrategyManual})
	if got := mgr.EffectiveStrategy(ChannelBot); got != StrategyAutoNarrow {
		t.Errorf("bot channel should fall back to auto_narrow after override removal, got %q", got)
	}
	if got := mgr.EffectiveStrategy(ChannelWeb); got != StrategyManual {
		t.Errorf("web channel = %q, want manual", got)
	}
}
