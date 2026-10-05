//go:build !windows

package config

// platformLocale：macOS 在 SystemLocale 里读 AppleLanguages，其他系统看环境变量，这里不用。
func platformLocale() string { return "" }
