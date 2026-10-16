package tool

import (
	"errors"
	"testing"
)

func TestIsEnvAssignment(t *testing.T) {
	cases := map[string]bool{
		"GOOS=linux":   true,
		"_X=1":         true,
		"A1=b":         true,
		"=bad":         false,
		"1A=bad":       false,
		"a-b=bad":      false,
		"go":           false,
		"--flag=value": false,
	}
	for in, want := range cases {
		if got := isEnvAssignment(in); got != want {
			t.Errorf("isEnvAssignment(%q)=%v want %v", in, got, want)
		}
	}
}

func TestCheckWhitelistSkipsEnvPrefix(t *testing.T) {
	tool := &ExecuteCommandTool{strictWhitelist: true}
	if err := tool.checkWhitelist("GOOS=linux GOARCH=amd64 go build ./..."); err != nil {
		t.Fatalf("env-prefixed whitelisted command should pass, got %v", err)
	}
}

func TestCheckWhitelistStrictBlocksUnknownAfterEnvPrefix(t *testing.T) {
	tool := &ExecuteCommandTool{strictWhitelist: true}
	if err := tool.checkWhitelist("FOO=bar definitely_not_allowed_cmd"); err == nil {
		t.Fatalf("strict mode must reject unknown command after env prefix")
	}
}

// 软策略（默认）下未列出的命令不再由工具层硬拦：交给审批层判定。
// 这是"文档/技能建议的命令被工具拒绝"这类问题的根因修复。
func TestCheckWhitelistSoftAllowsUnlisted(t *testing.T) {
	tool := &ExecuteCommandTool{}
	for _, cmd := range []string{
		"definitely_not_allowed_cmd --flag",
		"FOO=bar definitely_not_allowed_cmd",
	} {
		if err := tool.checkWhitelist(cmd); err != nil {
			t.Errorf("soft mode should defer %q to the approval layer, got %v", cmd, err)
		}
	}
}

// 严格模式保留硬拦，且错误类型可被识别（方便上层区分"未列出"与"空命令"）。
func TestCheckWhitelistStrictReturnsTypedError(t *testing.T) {
	tool := &ExecuteCommandTool{strictWhitelist: true}
	err := tool.checkWhitelist("definitely_not_allowed_cmd")
	var unlisted *UnlistedCommandError
	if !errors.As(err, &unlisted) {
		t.Fatalf("want *UnlistedCommandError, got %T (%v)", err, err)
	}
	if unlisted.Command != "definitely_not_allowed_cmd" {
		t.Errorf("reported command = %q, want the base command", unlisted.Command)
	}
}

// env 赋值前缀不参与判定：BaseCommand 必须跳过它们。
func TestBaseCommandSkipsEnvAssignments(t *testing.T) {
	cases := map[string]string{
		"GOOS=linux go build ./...": "go",
		"A=1 B=2 python -m pytest":  "python",
		"npx eslint --fix src/":     "npx",
		"FOO=bar":                   "",
		"":                          "",
	}
	for in, want := range cases {
		if got := BaseCommand(in); got != want {
			t.Errorf("BaseCommand(%q) = %q, want %q", in, got, want)
		}
	}
}

// 技能正文建议模型运行的工具必须都在标准工具链清单里，
// 否则会出现"技能教模型跑、工具拒绝跑"。
func TestStandardCommandsCoverSkillRecommendedTools(t *testing.T) {
	for _, cmd := range []string{
		"npx", "gofmt", "golangci-lint", "staticcheck", "dlv",
		"ruff", "black", "mypy", "uv", "poetry", "pipenv",
		"bun", "bunx", "deno", "http", "clinic", "kernprof",
	} {
		if !IsStandardCommand(cmd) {
			t.Errorf("%q is recommended by built-in skills but missing from the standard toolchain list", cmd)
		}
	}
}

func TestCheckWhitelistOnlyAssignmentsRejected(t *testing.T) {
	tool := &ExecuteCommandTool{}
	if err := tool.checkWhitelist("FOO=bar"); err == nil {
		t.Fatalf("assignment-only command should be rejected as empty")
	}
}

func TestCheckInjectionAllowsDollarParenInSingleQuotes(t *testing.T) {
	tool := &ExecuteCommandTool{}
	if err := tool.checkInjection("echo 'price $(not expanded)'"); err != nil {
		t.Fatalf("$() inside single quotes is literal and should pass, got %v", err)
	}
}

func TestCheckInjectionBlocksUnquotedDollarParen(t *testing.T) {
	tool := &ExecuteCommandTool{}
	if err := tool.checkInjection("echo $(rm -rf /tmp/x)"); err == nil {
		t.Fatalf("unquoted $() must still be blocked")
	}
}

func TestCheckInjectionBlocksUnclosedQuoteFallback(t *testing.T) {
	tool := &ExecuteCommandTool{}
	// 引号未闭合：保守地保留全文，因此 $( 仍被拦截
	if err := tool.checkInjection("echo 'unclosed $(id)"); err == nil {
		t.Fatalf("unclosed quote must not hide $() from the check")
	}
}
