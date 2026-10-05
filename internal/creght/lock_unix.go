//go:build !windows

package creght

import (
	"os"
	"path/filepath"
	"syscall"
)

// lockFile 拿文件锁（跨进程），返回解锁函数。
func lockFile(p string) (func(), error) {
	os.MkdirAll(filepath.Dir(p), 0o700)
	f, err := os.OpenFile(p, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return func() { syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
}
