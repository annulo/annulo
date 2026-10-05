package ai

import (
	"os"
	"strings"

	"github.com/sky-valley/pi/internal/jstext"
)

// defaultAuthContext is the default AuthContext: env vars from the OS
// environment, file existence via os.Stat (pi packages/ai/src/auth/context.ts).
type defaultAuthContext struct{}

// Env returns the OS environment value, or "" when unset or blank as JavaScript
// trims (pi normalizes empty to undefined).
func (defaultAuthContext) Env(name string) string {
	v := os.Getenv(name)
	if jstext.Trim(v) == "" {
		return ""
	}
	return v
}

// FileExists reports whether path exists, expanding a leading "~" to the home
// directory.
func (defaultAuthContext) FileExists(path string) bool {
	if strings.HasPrefix(path, "~") {
		home, err := os.UserHomeDir()
		if err != nil {
			return false
		}
		path = home + path[1:]
	}
	_, err := os.Stat(path)
	return err == nil
}

// DefaultProviderAuthContext returns the default AuthContext backed by the OS
// environment and filesystem.
func DefaultProviderAuthContext() AuthContext {
	return defaultAuthContext{}
}
