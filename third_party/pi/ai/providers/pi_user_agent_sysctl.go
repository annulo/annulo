//go:build darwin || dragonfly || freebsd || netbsd || openbsd

package providers

import "syscall"

// osRelease returns the kernel release via sysctl, matching Node os.release()
// (libuv uv_os_uname → uname(2) release) on Darwin and the BSDs.
func osRelease() (string, bool) {
	release, err := syscall.Sysctl("kern.osrelease")
	if err != nil {
		return "", false
	}
	return release, true
}
