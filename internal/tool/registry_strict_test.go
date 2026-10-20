package tool

import (
	"context"
	"testing"
)

// blockedOf 读取 execute_command 的拒绝原因（工具用 result 里的 blocked 字段
// 表达拦截，错误值本身是 nil）。
func blockedOf(t *testing.T, res interface{}) string {
	t.Helper()
	m, ok := res.(map[string]interface{})
	if !ok {
		t.Fatalf("unexpected result type %T", res)
	}
	blocked, _ := m["blocked"].(string)
	return blocked
}

// TestRegistryStrictCommandListReachesExecuteTool 保证 tools.strict_command_list
// 这个配置项真的传到 execute_command 实例上。
//
// 此前 SetStrictWhitelist 只有测试调用它——配置层没有任何入口，于是「只允许
// 已知工具链」这个模式在生产里永远无法开启（报告 skills-review-round2 §8
// 「有意不做」的那一条）。这条用例从 Registry 入口验证到工具行为，防止再次
// 出现「开关存在、接线断了」。
func TestRegistryStrictCommandListReachesExecuteTool(t *testing.T) {
	ctx := context.Background()
	unlisted := "definitely_not_in_the_toolchain_list --version"

	soft := NewRegistry()
	soft.SetStrictCommandList(false)
	soft.RegisterAll("")
	softTool, err := soft.Get("execute_command")
	if err != nil {
		t.Fatal("execute_command not registered")
	}
	res, err := softTool.Execute(ctx, map[string]interface{}{"command": unlisted})
	if err != nil {
		t.Fatalf("soft policy must not return an error for unlisted commands, got %v", err)
	}
	if blocked := blockedOf(t, res); blocked == "not_whitelisted" {
		t.Fatal("soft policy must not hard-reject unlisted commands (approval layer decides)")
	}

	strict := NewRegistry()
	strict.SetStrictCommandList(true)
	strict.RegisterAll("")
	strictTool, err := strict.Get("execute_command")
	if err != nil {
		t.Fatal("execute_command not registered")
	}
	res, err = strictTool.Execute(ctx, map[string]interface{}{"command": unlisted})
	if err != nil {
		t.Fatalf("strict policy should report the block in the result, not as an error: %v", err)
	}
	if blocked := blockedOf(t, res); blocked != "not_whitelisted" {
		t.Fatalf("strict policy must block unlisted commands, got blocked=%q res=%v", blocked, res)
	}
}

// TestRegistryRegistersFormatCode 保证 format_code 真的注册了。
//
// 它是「有实现、从未注册」的四个孤儿工具之一（file_search / format_code /
// suggest_fix / trace_execution）。技能正文明确让模型跑 gofmt，而工具表里只有
// lint 没有 format，属于「文档教模型跑、工具不存在」。
func TestRegistryRegistersFormatCode(t *testing.T) {
	r := NewRegistry()
	r.RegisterAll("")
	for _, name := range []string{"format_code", "lint"} {
		if _, err := r.Get(name); err != nil {
			t.Errorf("%s must be registered", name)
		}
	}
	// 重复暴露的搜索工具不该进表：search_in_files 才是唯一入口。
	if _, err := r.Get("file_search"); err == nil {
		t.Error("file_search must not be registered (duplicate of search_in_files)")
	}
	// 低质量启发式工具已删除，不应再出现在工具表里。
	for _, name := range []string{"suggest_fix", "trace_execution"} {
		if _, err := r.Get(name); err == nil {
			t.Errorf("%s should not be registered", name)
		}
	}
}
