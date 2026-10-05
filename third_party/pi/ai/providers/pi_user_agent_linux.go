//go:build linux

package providers

import "syscall"

// osRelease returns uname(2)'s release field, matching Node os.release() on
// Linux (libuv uv_os_uname).
func osRelease() (string, bool) {
	var u syscall.Utsname
	if err := syscall.Uname(&u); err != nil {
		return "", false
	}
	return utsField(u.Release[:]), true
}

// utsField converts a NUL-terminated utsname char array to a string. The
// element type is int8 on some GOARCHes and uint8 on others.
func utsField[T int8 | uint8](f []T) string {
	b := make([]byte, 0, len(f))
	for _, c := range f {
		if c == 0 {
			break
		}
		b = append(b, byte(c))
	}
	return string(b)
}
