//go:build windows

package fslock

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// tryLock 非阻塞地尝试加排他锁（锁住文件首字节，范围对锁语义无影响）。
func tryLock(f *os.File) error {
	ol := new(windows.Overlapped)
	return windows.LockFileEx(
		windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, 1, 0, ol,
	)
}

// unlock 释放锁；必须与加锁时的区域（偏移 0、长度 1）一致。
func unlock(f *os.File) error {
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, new(windows.Overlapped))
}

// isBusy 判断错误是否只是"锁已被别的进程/句柄持有"。
func isBusy(err error) bool {
	return errors.Is(err, windows.ERROR_LOCK_VIOLATION) ||
		errors.Is(err, windows.ERROR_IO_PENDING)
}
