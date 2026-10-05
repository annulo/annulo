//go:build windows

package config

import (
	"syscall"
	"unsafe"
)

// platformLocale：Windows 上 LANG 一般没设，读系统的用户区域（比如 zh-CN、en-US）。
func platformLocale() string {
	proc := syscall.NewLazyDLL("kernel32.dll").NewProc("GetUserDefaultLocaleName")
	buf := make([]uint16, 85) // LOCALE_NAME_MAX_LENGTH
	if r, _, _ := proc.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf))); r == 0 {
		return ""
	}
	return syscall.UTF16ToString(buf)
}
