package tool

import (
	"strings"
	"testing"
	"time"
)

func TestIsLongRunningCommand(t *testing.T) {
	cases := map[string]bool{
		"go test ./internal/skills/ -count=1": true,
		"cd /tmp/x && go build ./...":         true,
		"npm install":                         true,
		"ls -la":                              false,
		"echo hello":                          false,
	}
	for cmd, want := range cases {
		if got := isLongRunningCommand(cmd); got != want {
			t.Errorf("isLongRunningCommand(%q)=%v want %v", cmd, got, want)
		}
	}
}

func TestLongRunningTimeoutHintSuggestsProcess(t *testing.T) {
	hint := longRunningTimeoutHint("go test ./...", 120*time.Second)
	if !strings.Contains(hint, "process(action=run") {
		t.Fatalf("long-running hint should suggest process(action=run), got %q", hint)
	}
}

func TestLongRunningTimeoutHintGenericForShortCommand(t *testing.T) {
	hint := longRunningTimeoutHint("ls -la", 120*time.Second)
	if !strings.Contains(hint, "background") {
		t.Fatalf("generic hint should still mention background, got %q", hint)
	}
}
