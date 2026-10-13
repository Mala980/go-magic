package cortex

import (
	"testing"
	"time"
)

// TestGEPAHealthDegradedAfterRepeatedFailures 锁死 P2 告警面。
//
// 缺口原貌：evolutionLoop 的失败只走 log.Printf —— 实测 provider 解析不了域名时
// 一天 288 条同措辞 WARN。"悄悄失败 3 周"能发生，正是因为**没有任何机器可读的
// 信号**说"进化停了"：is_converged 没人消费，日志也没人 grep。
//
// 断言：连续失败达到阈值后，GetStats() 必须给出 health=degraded + alert +
// alert_streak，使 HTTP /api/cortex/gepa 与 `magic cortex gepa` 可以直接判定。
func TestGEPAHealthDegradedAfterRepeatedFailures(t *testing.T) {
	g := NewGEPAEngine(t.TempDir(), nil, newTestStore(t))

	// 未失败：没有 LLM 时是 disabled，但绝不能报 degraded。
	if h := g.GetStats()["health"]; h == "degraded" {
		t.Fatalf("healthy engine reported degraded: %v", h)
	}

	g.mu.Lock()
	g.consecutiveFail = gepaAlertAfterFailures - 1
	g.lastFailureReason = "upstream timeout"
	g.lastEvolveFailureAt = time.Now()
	g.mu.Unlock()

	if h := g.GetStats()["health"]; h == "degraded" {
		t.Fatalf("must not alert before the threshold (%d failures): %v", gepaAlertAfterFailures-1, h)
	}

	g.mu.Lock()
	g.consecutiveFail = gepaAlertAfterFailures
	g.mu.Unlock()

	stats := g.GetStats()
	if stats["health"] != "degraded" {
		t.Fatalf("expected health=degraded at %d consecutive failures, got %v",
			gepaAlertAfterFailures, stats["health"])
	}
	alert, ok := stats["alert"].(string)
	if !ok || alert == "" {
		t.Fatal("degraded health must carry a human-readable alert string")
	}
	if stats["alert_streak"] != gepaAlertAfterFailures {
		t.Fatalf("alert_streak should report the streak, got %v", stats["alert_streak"])
	}
	if _, ok := stats["last_failure_at"]; !ok {
		t.Fatal("last_failure_at must be exposed so operators can see when it broke")
	}
	if stats["last_failure_reason"] != "upstream timeout" {
		t.Fatalf("failure reason lost: %v", stats["last_failure_reason"])
	}
}

// TestGEPAHealthRecoversAfterSuccess 验证恢复后告警必须消失（否则会永久误报）。
func TestGEPAHealthRecoversAfterSuccess(t *testing.T) {
	g := NewGEPAEngine(t.TempDir(), nil, newTestStore(t))

	g.mu.Lock()
	g.consecutiveFail = gepaAlertAfterFailures + 2
	g.lastFailureReason = "boom"
	g.mu.Unlock()
	if g.GetStats()["health"] != "degraded" {
		t.Fatal("precondition: expected degraded")
	}

	// 模拟一次成功收尾（recordSuccess 路径上的计数重置）。
	g.mu.Lock()
	g.consecutiveFail = 0
	g.lastFailureReason = ""
	g.lastEvolveFailureAt = time.Time{}
	g.mu.Unlock()

	stats := g.GetStats()
	if stats["health"] == "degraded" {
		t.Fatal("health must recover once failures stop")
	}
	if _, present := stats["alert"]; present {
		t.Fatalf("stale alert survived recovery: %v", stats["alert"])
	}
	if _, present := stats["last_failure_at"]; present {
		t.Fatal("stale last_failure_at survived recovery")
	}
}
