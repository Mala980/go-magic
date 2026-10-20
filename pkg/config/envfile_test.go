package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseEnvContentBasics(t *testing.T) {
	content := "\ufeff# comment\r\n" +
		"PLAIN=abc\n" +
		"export EXPORTED=def\n" +
		`QUOTED="a b"` + "\n" +
		`SINGLE='c d'` + "\n" +
		"INLINE=val  # trailing note\n" +
		"HASHY=a#b\n" +
		"EMPTY=\n" +
		"\n" +
		"NOT_A_PAIR line\n"

	var pairs []string
	for _, ln := range parseEnvContent(content) {
		if ln.isPair {
			pairs = append(pairs, ln.key+"="+ln.value)
		}
	}
	got := strings.Join(pairs, "|")
	want := "PLAIN=abc|EXPORTED=def|QUOTED=a b|SINGLE=c d|INLINE=val|HASHY=a#b|EMPTY="
	if got != want {
		t.Fatalf("parsed pairs mismatch: got %q want %q", got, want)
	}
}

func TestParseEnvContentKeepsUnparsedLines(t *testing.T) {
	// 跨行续写这类我们解析不了的行必须原样保留，不能被一次保存吃掉。
	content := "A=1\nB=multi \\\n  line\n"
	lines := parseEnvContent(content)
	// 末尾换行会切出一个空行，属于正常形态。
	if len(lines) != 4 {
		t.Fatalf("expected 4 lines, got %d: %+v", len(lines), lines)
	}
	if lines[1].raw != "B=multi \\" {
		t.Fatalf("unexpected raw line: %q", lines[1].raw)
	}
	if lines[2].raw != "  line" {
		t.Fatalf("continuation line not preserved: %q", lines[2].raw)
	}
	if lines[2].isPair {
		t.Fatalf("continuation line must not be treated as a key/value pair")
	}

	// 写回后续写内容仍在（我们只解析不重排无法解析的行）。
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	vars, err := LoadEnvFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if vars["A"] != "1" {
		t.Fatalf("A not parsed: %v", vars)
	}
	if err := WriteEnvFile(path, vars); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if !strings.Contains(string(after), "  line") {
		t.Fatalf("continuation dropped on rewrite:\n%s", after)
	}
}

func TestWriteEnvFilePreservesOrderAndComments(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	original := "# providers\nOPENAI_API_KEY=old\n\n# tools\nGITHUB_TOKEN=gh\n"
	if err := os.WriteFile(path, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}

	vars, err := LoadEnvFile(path)
	if err != nil {
		t.Fatal(err)
	}
	vars["OPENAI_API_KEY"] = "new"
	vars["TAVILY_API_KEY"] = "tv"
	if err := WriteEnvFile(path, vars); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	// 注释与分组顺序保留，改动只在值上，新键追加在末尾。
	if !strings.HasPrefix(got, "# providers\nOPENAI_API_KEY=new\n") {
		t.Fatalf("comments/order not preserved:\n%s", got)
	}
	if !strings.Contains(got, "# tools\nGITHUB_TOKEN=gh") {
		t.Fatalf("second group not preserved:\n%s", got)
	}
	if !strings.HasSuffix(got, "TAVILY_API_KEY=tv\n") {
		t.Fatalf("new key not appended:\n%s", got)
	}
}

func TestWriteEnvFileDropsDeletedKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, []byte("A=1\nB=2\n"), 0600); err != nil {
		t.Fatal(err)
	}
	vars, _ := LoadEnvFile(path)
	delete(vars, "A")
	if err := WriteEnvFile(path, vars); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "A=1") {
		t.Fatalf("deleted key still present:\n%s", data)
	}
	if !strings.Contains(string(data), "B=2") {
		t.Fatalf("surviving key lost:\n%s", data)
	}
}

func TestWriteEnvFileQuotesValuesNeedingIt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	if err := WriteEnvFile(path, map[string]string{
		"SPACED": "a b",
		"HASH":   "x#y",
		"PLAIN":  "abc",
	}); err != nil {
		t.Fatal(err)
	}
	vars, err := LoadEnvFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if vars["SPACED"] != "a b" {
		t.Fatalf("spaced value roundtrip failed: %q", vars["SPACED"])
	}
	if vars["HASH"] != "x#y" {
		t.Fatalf("hash value roundtrip failed: %q", vars["HASH"])
	}
	if vars["PLAIN"] != "abc" {
		t.Fatalf("plain value roundtrip failed: %q", vars["PLAIN"])
	}
}

func TestLoadEnvFileMissingIsNotAnError(t *testing.T) {
	vars, err := LoadEnvFile(filepath.Join(t.TempDir(), "nope.env"))
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if len(vars) != 0 {
		t.Fatalf("expected empty map, got %v", vars)
	}
}

func TestEnvFilePathPerProfile(t *testing.T) {
	home := "/magic"
	if got := EnvFilePath(home, ""); got != filepath.Join(home, ".env") {
		t.Fatalf("empty profile should map to magicHome/.env, got %q", got)
	}
	if got := EnvFilePath(home, DefaultProfileName); got != filepath.Join(home, ".env") {
		t.Fatalf("default profile should map to magicHome/.env, got %q", got)
	}
	want := filepath.Join(home, "profiles", "work", ".env")
	if got := EnvFilePath(home, "work"); got != want {
		t.Fatalf("named profile path: got %q want %q", got, want)
	}
	// 非法名字绝不能拼出路径（防目录穿越）。
	if got := EnvFilePath(home, "../etc"); got != "" {
		t.Fatalf("invalid profile must return empty path, got %q", got)
	}
}

func TestEnvSetDoesNotOverrideRealEnvironment(t *testing.T) {
	const key = "MAGIC_TEST_ENVSET_REAL"
	t.Setenv(key, "from-os")

	var e EnvSet
	applied := e.Apply(map[string]string{key: "from-dotenv"})
	if len(applied) != 0 {
		t.Fatalf("real env var must win, but was overridden: %v", applied)
	}
	if got := os.Getenv(key); got != "from-os" {
		t.Fatalf("real env var changed to %q", got)
	}
	// 撤销也不能动它。
	if e.UnsetKey(key) {
		t.Fatalf("UnsetKey must not claim a variable it never set")
	}
	if got := os.Getenv(key); got != "from-os" {
		t.Fatalf("UnsetKey removed a real env var: %q", got)
	}
}

func TestEnvSetInjectsAndRevokesOnlyItsOwn(t *testing.T) {
	const ours = "MAGIC_TEST_ENVSET_OURS"
	const real = "MAGIC_TEST_ENVSET_KEEP"
	_ = os.Unsetenv(ours)
	t.Setenv(real, "keep-me")

	var e EnvSet
	applied := e.Apply(map[string]string{ours: "v1", real: "ignored"})
	if len(applied) != 1 || applied[0] != ours {
		t.Fatalf("expected only %s to be applied, got %v", ours, applied)
	}
	if got := os.Getenv(ours); got != "v1" {
		t.Fatalf("ours not injected: %q", got)
	}

	// 同一进程内值可以更新（用户改了 .env 就该生效）。
	e.Apply(map[string]string{ours: "v2"})
	if got := os.Getenv(ours); got != "v2" {
		t.Fatalf("existing managed key not updated: %q", got)
	}

	e.Clear()
	if _, exists := os.LookupEnv(ours); exists {
		t.Fatalf("Clear should have removed managed key")
	}
	if got := os.Getenv(real); got != "keep-me" {
		t.Fatalf("Clear touched a real env var: %q", got)
	}
}

func TestEnvSetSetKeyAndUnsetKey(t *testing.T) {
	const key = "MAGIC_TEST_ENVSET_SINGLE"
	_ = os.Unsetenv(key)

	var e EnvSet
	if !e.SetKey(key, "instant") {
		t.Fatalf("SetKey should succeed for an unset variable")
	}
	if got := os.Getenv(key); got != "instant" {
		t.Fatalf("SetKey did not take effect immediately: %q", got)
	}
	if !e.Has(key) {
		t.Fatalf("SetKey should register the key as managed")
	}
	if !e.UnsetKey(key) {
		t.Fatalf("UnsetKey should remove a managed key")
	}
	if _, exists := os.LookupEnv(key); exists {
		t.Fatalf("key still in environment after UnsetKey")
	}
}

func TestEnvSetManagedSnapshot(t *testing.T) {
	const key = "MAGIC_TEST_ENVSET_SNAPSHOT"
	_ = os.Unsetenv(key)

	var e EnvSet
	e.Apply(map[string]string{key: "snap"})
	snapshot := e.Managed()
	if snapshot[key] != "snap" {
		t.Fatalf("managed snapshot missing key: %v", snapshot)
	}
	// 快照必须是副本：改动它不能影响内部状态。
	snapshot[key] = "mutated"
	if got := os.Getenv(key); got != "snap" {
		t.Fatalf("snapshot was not a copy: env=%q", got)
	}
	_ = os.Unsetenv(key)
}
