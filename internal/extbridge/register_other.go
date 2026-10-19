//go:build !windows

package extbridge

func setRegistry(key, value string) error { return nil }
