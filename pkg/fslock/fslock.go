// Package fslock 提供跨进程的排他文件锁。
//
// 用途：gateway 与 CLI 可能同时写同一份 USER.json。原子写（temp+rename）只能
// 保证"不会读到半截文件"，挡不住"后写整份覆盖前写"导致的丢更新。要彻底解决，
// 必须在**读-改-写**区间外面加一把所有进程都能看见的锁。
//
// 实现是操作系统的建议性文件锁：Unix 用 flock(2)，Windows 用 LockFileEx。
// 进程崩溃时内核会自动释放，不会留下死锁；锁文件本身可以长期存在（它只是
// 锁的载体，不是数据）。
package fslock

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ErrTimeout 表示在超时时间内没能拿到锁。
var ErrTimeout = errors.New("fslock: timeout waiting for lock")

// 重试间隔：锁临界区只有一次小文件读+写，通常毫秒级就结束。10ms 足够勤，
// 又不会在长时间争用时把 CPU 打满。
const retryInterval = 10 * time.Millisecond

// Lock 是一个已持有的排他锁。Release 之后不可复用。
type Lock struct {
	f *os.File
}

// Acquire 获取 path 上的排他锁，最多等 timeout 后返回 ErrTimeout。
//
// path 一般取被保护数据文件的同目录文件（例如 USER.json.lock）。锁文件用
// O_CREATE 打开，不会截断已存在的文件。
func Acquire(path string, timeout time.Duration) (*Lock, error) {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("fslock: create lock dir: %w", err)
		}
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("fslock: open lock file: %w", err)
	}

	deadline := time.Now().Add(timeout)
	for {
		err := tryLock(f)
		if err == nil {
			return &Lock{f: f}, nil
		}
		if !isBusy(err) {
			_ = f.Close()
			return nil, fmt.Errorf("fslock: lock %s: %w", path, err)
		}
		if time.Now().After(deadline) {
			_ = f.Close()
			return nil, fmt.Errorf("%w: %s", ErrTimeout, path)
		}
		time.Sleep(retryInterval)
	}
}

// Release 释放锁并关闭文件句柄，可安全地多次调用。
func (l *Lock) Release() {
	if l == nil || l.f == nil {
		return
	}
	_ = unlock(l.f)
	_ = l.f.Close()
	l.f = nil
}
