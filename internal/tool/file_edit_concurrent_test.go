package tool

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// TestFileEditConcurrentReplaceSameFileNoLostUpdate 回归测试：
// 模型在同一轮里并行下发多个针对同一文件的 replace。若 file_edit 的读-改-写
// 没有按文件串行化，后写者会基于旧内容整文件覆盖前写者，导致修改丢失。
// 这里并发修改同一文件的 N 个不同函数，断言全部修改都保留。
func TestFileEditConcurrentReplaceSameFileNoLostUpdate(t *testing.T) {
	const n = 16

	dir := t.TempDir()
	path := filepath.Join(dir, "multi.go")

	var b strings.Builder
	b.WriteString("package p\n")
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "\nfunc f%d() int {\n\treturn %d\n}\n", i, i)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0644); err != nil {
		t.Fatal(err)
	}

	tool := &FileEditTool{}
	var wg sync.WaitGroup
	errs := make(chan error, n)
	start := make(chan struct{})

	for i := 0; i < n; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start // 让所有 goroutine 尽量同时出发，放大竞争窗口
			params := map[string]interface{}{
				"operation":   "replace",
				"path":        path,
				"old_content": fmt.Sprintf("func f%d() int {\n\treturn %d\n}", i, i),
				"new_content": fmt.Sprintf("func f%d() int {\n\treturn %d\n}", i, i+1000),
			}
			if _, err := tool.Execute(context.Background(), params); err != nil {
				errs <- fmt.Errorf("edit %d: %w", i, err)
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Error(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	for i := 0; i < n; i++ {
		want := fmt.Sprintf("func f%d() int {\n\treturn %d\n}", i, i+1000)
		if !strings.Contains(got, want) {
			t.Errorf("lost update for f%d: modification missing from final file", i)
		}
	}
}
