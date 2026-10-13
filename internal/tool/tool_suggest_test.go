package tool

import (
	"strings"
	"testing"
)

func TestSuggestToolNamesAlias(t *testing.T) {
	registered := []string{"file_edit", "read_file", "write_file", "process"}
	got := suggestToolNames("edit_file", registered)
	if len(got) != 1 || got[0] != "file_edit" {
		t.Fatalf("edit_file should suggest file_edit, got %v", got)
	}
}

func TestSuggestToolNamesFuzzy(t *testing.T) {
	registered := []string{"file_edit", "read_file", "write_file", "process"}
	got := suggestToolNames("read_fle", registered)
	if len(got) == 0 || got[0] != "read_file" {
		t.Fatalf("read_fle should suggest read_file first, got %v", got)
	}
}

func TestSuggestToolNamesNormalizeCase(t *testing.T) {
	registered := []string{"file_edit", "read_file"}
	got := suggestToolNames("File-Edit", registered)
	if len(got) == 0 || got[0] != "file_edit" {
		t.Fatalf("File-Edit should normalize to file_edit, got %v", got)
	}
}

func TestSuggestToolNamesNoRelated(t *testing.T) {
	registered := []string{"file_edit", "read_file"}
	if got := suggestToolNames("zzzqqq_unrelated_tool", registered); len(got) != 0 {
		t.Fatalf("unrelated name should give no suggestion, got %v", got)
	}
}

func TestToolNotFoundErrorContainsHint(t *testing.T) {
	err := toolNotFoundError("edit_file", []string{"file_edit", "read_file"})
	if err == nil || !strings.Contains(err.Error(), "Did you mean file_edit") {
		t.Fatalf("expected 'Did you mean file_edit' hint, got %v", err)
	}
}

func TestToolNotFoundErrorPlainWhenNoSuggestion(t *testing.T) {
	err := toolNotFoundError("zzzqqq_unrelated_tool", []string{"file_edit"})
	if err == nil || strings.Contains(err.Error(), "Did you mean") {
		t.Fatalf("expected plain not-found without hint, got %v", err)
	}
}

func TestLevenshtein(t *testing.T) {
	cases := map[[2]string]int{
		{"", ""}:              0,
		{"abc", "abc"}:        0,
		{"kitten", "sitting"}: 3,
	}
	for in, want := range cases {
		if got := levenshtein(in[0], in[1]); got != want {
			t.Errorf("levenshtein(%q,%q)=%d want %d", in[0], in[1], got, want)
		}
	}
}
