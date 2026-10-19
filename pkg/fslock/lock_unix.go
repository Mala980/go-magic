//go:build !windows

package fslock

import (
	"errors"
	"os"
	"syscall"
)

// tryLock 非阻塞地尝试加排他锁。
func tryLock(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}

// unlock 释放锁。
func unlock(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}

// isBusy 判断错误是否只是"锁已被别的进程/句柄持有"。
//
// EINTR 也按 busy 处理：信号打断的是这次尝试，不是锁本身不可用。
func isBusy(err error) bool {
	return errors.Is(err, syscall.EWOULDBLOCK) ||
		errors.Is(err, syscall.EAGAIN) ||
		errors.Is(err, syscall.EINTR)
}
