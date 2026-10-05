//go:build windows

package providers

import (
	"fmt"
	"syscall"
	"unsafe"
)

// rtlOSVersionInfo mirrors RTL_OSVERSIONINFOW.
type rtlOSVersionInfo struct {
	osVersionInfoSize uint32
	majorVersion      uint32
	minorVersion      uint32
	buildNumber       uint32
	platformID        uint32
	csdVersion        [128]uint16
}

// ntdll is a KnownDLL, so the loader resolves it from system32 regardless of
// search path.
var procRtlGetVersion = syscall.NewLazyDLL("ntdll.dll").NewProc("RtlGetVersion")

// osRelease returns "major.minor.build" from RtlGetVersion, matching Node
// os.release() on Windows (libuv uv_os_uname formats the same three fields;
// unlike GetVersion it is not capped by the process manifest).
func osRelease() (string, bool) {
	if procRtlGetVersion.Find() != nil {
		return "", false
	}
	var vi rtlOSVersionInfo
	vi.osVersionInfoSize = uint32(unsafe.Sizeof(vi))
	if ret, _, _ := procRtlGetVersion.Call(uintptr(unsafe.Pointer(&vi))); ret != 0 {
		return "", false
	}
	return fmt.Sprintf("%d.%d.%d", vi.majorVersion, vi.minorVersion, vi.buildNumber), true
}
