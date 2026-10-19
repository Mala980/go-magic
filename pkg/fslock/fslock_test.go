package fslock

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// 两个独立的 *Lock 模拟两个进程（各持自己的 fd/句柄），验证互斥语义。
func TestAcquireIsExclusive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.json.lock")

	first, err := Acquire(path, time.Second)
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	defer first.Release()

	if _, err := Acquire(path, 100*time.Millisecond); !errors.Is(err, ErrTimeout) {
		t.Fatalf("second Acquire while held = %v, want ErrTimeout", err)
	}

	first.Release()

	second, err := Acquire(path, time.Second)
	if err != nil {
		t.Fatalf("Acquire after release: %v", err)
	}
	second.Release()
}

// Release 必须可重复调用（defer 与显式释放同时存在时不应 panic）。
func TestReleaseIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.lock")
	lk, err := Acquire(path, time.Second)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	lk.Release()
	lk.Release()
	var nilLock *Lock
	nilLock.Release()
}

// 锁文件所在目录不存在时应自动创建（分身目录可能尚未建出来）。
func TestAcquireCreatesParentDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a", "b", "c.lock")
	lk, err := Acquire(path, time.Second)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	lk.Release()
}
