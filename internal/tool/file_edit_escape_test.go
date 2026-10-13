package tool

import "testing"

func TestValidateEscapedNewContentRejectsLiteralNewlines(t *testing.T) {
	content := "package x\n\nfunc a() {}\n"
	// 没有真实换行，只有字面量 \n：应拒绝
	if err := validateEscapedNewContent(content, `line one\nline two\nline three`); err == nil {
		t.Fatalf("literal \\n without real newline should be rejected")
	}
}

func TestValidateEscapedNewContentAllowsRealNewlines(t *testing.T) {
	content := "package x\n"
	if err := validateEscapedNewContent(content, "line one\nline two"); err != nil {
		t.Fatalf("real newlines should pass, got %v", err)
	}
}

func TestValidateEscapedNewContentAllowsEmpty(t *testing.T) {
	if err := validateEscapedNewContent("x\n", ""); err != nil {
		t.Fatalf("empty new_content should pass validation, got %v", err)
	}
}

func TestValidateEscapedNewContentAllowsWhenFileUsesLiteralEscapes(t *testing.T) {
	// 原文件本身大量使用字面量 \n（如生成的代码字符串），不应误判
	content := `s := "a\nb\nc\n"` + "\n"
	if err := validateEscapedNewContent(content, `t := "x\ny"`); err != nil {
		t.Fatalf("file already uses literal \\n heavily; should not reject, got %v", err)
	}
}
