package tool

import "testing"

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
	tool := &ExecuteCommandTool{}
	if err := tool.checkWhitelist("GOOS=linux GOARCH=amd64 go build ./..."); err != nil {
		t.Fatalf("env-prefixed whitelisted command should pass, got %v", err)
	}
}

func TestCheckWhitelistStillBlocksUnknownAfterEnvPrefix(t *testing.T) {
	tool := &ExecuteCommandTool{}
	if err := tool.checkWhitelist("FOO=bar definitely_not_allowed_cmd"); err == nil {
		t.Fatalf("unknown command after env prefix must still be rejected")
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
