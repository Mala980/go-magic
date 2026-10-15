package server

import (
	"testing"
	"time"
)

// waitForMCPInit 在 mcpInitDone 已关闭时应立即返回（不消耗等待预算）。
func TestWaitForMCPInitReturnsWhenClosed(t *testing.T) {
	s := &Server{mcpInitDone: make(chan struct{})}
	close(s.mcpInitDone)

	done := make(chan struct{})
	go func() {
		s.waitForMCPInit(30 * time.Second)
		close(done)
	}()

	select {
	case <-done:
		// 立即放行，符合预期
	case <-time.After(2 * time.Second):
		t.Fatal("waitForMCPInit blocked although mcpInitDone was already closed")
	}
}

// mcpInitDone 永不关闭时必须有界放行，不能无限阻塞业务。
func TestWaitForMCPInitBoundedWhenNeverClosed(t *testing.T) {
	s := &Server{mcpInitDone: make(chan struct{})}

	start := time.Now()
	done := make(chan struct{})
	go func() {
		s.waitForMCPInit(100 * time.Millisecond)
		close(done)
	}()

	select {
	case <-done:
		elapsed := time.Since(start)
		if elapsed > 5*time.Second {
			t.Fatalf("waitForMCPInit took %v, expected ~100ms bound", elapsed)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("waitForMCPInit did not return within bound")
	}
}

// 手工构造的 Server（未初始化 mcpInitDone，如部分测试）必须零值安全。
func TestWaitForMCPInitNilChannelSafe(t *testing.T) {
	s := &Server{}
	s.waitForMCPInit(time.Second) // 不应 panic，也不应等待

	var nilServer *Server
	nilServer.waitForMCPInit(time.Second) // 接收者为 nil 同样安全
}
