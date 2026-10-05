//go:build !linux && !darwin && !dragonfly && !freebsd && !netbsd && !openbsd && !windows

package providers

// osRelease is unavailable on this platform; piUserAgent degrades to the same
// "pi (browser)" string pi uses when node:os is unavailable.
func osRelease() (string, bool) { return "", false }
