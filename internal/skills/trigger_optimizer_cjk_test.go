package skills

import (
	"reflect"
	"testing"
)

// TestTokenizeCJK 验证分词对中日韩文本按 2-gram 切分、拉丁文本保持按词。
// 背景：此前 tokenize 只按空格切，中文整句会变成一个"关键词"，
// 回灌匹配（strings.Contains(input, keyword)）要求整句原样复现才能命中。
func TestTokenizeCJK(t *testing.T) {
	to := &TriggerOptimizer{}

	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"latin unchanged", "fix login timeout", []string{"fix", "login", "timeout"}},
		{
			"chinese bigrams",
			"帮我修复登录超时",
			[]string{"帮我", "我修", "修复", "复登", "登录", "录超", "超时"},
		},
		{"mixed", "修复login超时", []string{"修复", "login", "超时"}},
		{"punctuation stripped", "修复，超时！", []string{"修复", "超时"}},
		{"single cjk char dropped", "好", []string{}},
		{"japanese", "ログインのバグ", []string{"ログ", "グイ", "イン", "ンの", "のバ", "バグ"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := to.tokenize(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("tokenize(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// TestExtractKeywordsFromMissedTriggersCJK 验证中文漏触发样本能产出可复用的
// 高频 2-gram 关键词，而不是把整句当关键词。
func TestExtractKeywordsFromMissedTriggersCJK(t *testing.T) {
	to := &TriggerOptimizer{}

	triggers := []string{
		"帮我修复登录超时的问题",
		"再次修复登录超时",
		"修复登录超时报错",
		"登录超时到底怎么修",
	}

	got := to.extractKeywordsFromMissedTriggers(triggers)
	if len(got) == 0 {
		t.Fatal("expected keywords, got none")
	}

	wantSet := map[string]bool{"修复": true, "登录": true, "超时": true}
	found := map[string]bool{}
	for _, kw := range got {
		found[kw] = true
		// 整句不应成为关键词：任何关键词都不应长于 2 个 rune
		if len([]rune(kw)) > 2 {
			t.Errorf("keyword %q is longer than 2 runes; whole-sentence keywords must not leak", kw)
		}
	}
	for kw := range wantSet {
		if !found[kw] {
			t.Errorf("expected learned keyword %q, got %v", kw, got)
		}
	}
}
