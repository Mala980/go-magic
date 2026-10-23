package bot

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/magicwubiao/go-magic/pkg/config"
)

// TestIsEnabledDefaults locks in the "bot mode on by default" semantics:
// a missing bot_mode section must fall back to the default (enabled),
// while explicit true/false in config always wins.
func TestIsEnabledDefaults(t *testing.T) {
	// Missing bot_mode section -> default (enabled).
	if !IsEnabled(&config.Config{}) {
		t.Error("IsEnabled with nil BotMode should default to enabled")
	}

	// Explicit enabled.
	cfgOn := &config.Config{BotMode: &config.BotModeConfig{Enabled: true}}
	if !IsEnabled(cfgOn) {
		t.Error("IsEnabled with explicit Enabled:true should be true")
	}

	// Explicit disabled must win over the default.
	cfgOff := &config.Config{BotMode: &config.BotModeConfig{Enabled: false}}
	if IsEnabled(cfgOff) {
		t.Error("IsEnabled with explicit Enabled:false should be false")
	}

	// DefaultBotModeConfig itself must be enabled.
	if !config.DefaultBotModeConfig().Enabled {
		t.Error("DefaultBotModeConfig().Enabled should be true")
	}

	// defaultConfig should carry the default BotMode section.
	if dc := config.DefaultConfig(); dc.BotMode == nil || !dc.BotMode.Enabled {
		t.Error("defaultConfig() should include an enabled BotMode section")
	}
}

// botWorkDirFor 是 buildBotDeps（创建沙箱目录）与回合 runner（tool.WithWorkDir
// 注入）共用的路径推导函数 —— 两边必须一致，否则文件工具解析出的相对路径会
// 落到审批 hook 范围判定与沙箱设计之外。

func TestBotWorkDirForUsesWorkingDir(t *testing.T) {
	base := t.TempDir()
	cfg := &config.Config{WorkingDir: base}
	bc := &Config{Name: "Coder Bot!"}

	got := botWorkDirFor(cfg, bc)
	want := filepath.Join(base, "bots", "coder_bot_")
	if got != want {
		t.Errorf("botWorkDirFor = %q, want %q", got, want)
	}
}

func TestBotWorkDirForEmptyWorkingDirFallsBackToCwd(t *testing.T) {
	cfg := &config.Config{}
	bc := &Config{Name: "coder"}

	got := botWorkDirFor(cfg, bc)
	if !filepath.IsAbs(got) {
		t.Fatalf("botWorkDirFor = %q, want absolute path (cwd fallback)", got)
	}
	// 结构：<cwd>/bots/coder
	if filepath.Base(filepath.Dir(got)) != "bots" || filepath.Base(got) != "coder" {
		t.Errorf("botWorkDirFor = %q, want .../bots/coder", got)
	}
}

func TestBotWorkDirForSanitizesName(t *testing.T) {
	cfg := &config.Config{WorkingDir: t.TempDir()}
	cases := map[string]string{
		"":       "bot", // 空名兜底
		"..":     "bot", // 路径穿越兜底
		"Mi-X_1": "mi-x_1",
		"中文 Bot": "___bot",
	}
	for name, want := range cases {
		got := filepath.Base(botWorkDirFor(cfg, &Config{Name: name}))
		if got != want {
			t.Errorf("sanitize(%q) = %q, want %q", name, got, want)
		}
	}
}

// TestListStableOrder 锁定 List() 的确定性顺序：bots 存在 map 里，Go 运行时
// 每次 range 的迭代顺序都是随机的。List() 同时喂给 GET /api/bots（面板）和
// GET /api/relay/v1/bots（跨机 Peer 列表），不排序就会出现"每次刷新 bots
// 顺序都变"的现象。这里多次调用并断言顺序一致且按名称升序。
func TestListStableOrder(t *testing.T) {
	mgr := newTestManager(t, "zeta", "alpha", "Mid", "beta")

	want := []string{"alpha", "beta", "mid", "zeta"}
	for pass := 0; pass < 20; pass++ {
		got := mgr.List()
		if len(got) != len(want) {
			t.Fatalf("List() pass %d: got %d bots, want %d", pass, len(got), len(want))
		}
		for i, cfg := range got {
			if !strings.EqualFold(cfg.Name, want[i]) {
				t.Fatalf("List() pass %d: position %d = %q, want %q",
					pass, i, cfg.Name, want[i])
			}
		}
	}

	// TagList 同样遍历 map，也要稳定（用于工具错误信息/提示词）。
	for pass := 0; pass < 20; pass++ {
		if tags := mgr.TagList(); tags != "@alpha, @beta, @mid, @zeta" {
			t.Fatalf("TagList() pass %d = %q, want %q", pass, tags, "@alpha, @beta, @mid, @zeta")
		}
	}
}
