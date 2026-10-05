package providers

import "github.com/sky-valley/pi/ai"

// getProviderEnvValue resolves a provider configuration value from the
// per-stream scoped overrides first, then the OS environment. It delegates to
// ai.ProviderEnvValue (the single source of the scoped-env precedence), which
// ports pi's getProviderEnvValue (utils/provider-env.ts, 7f29e7a3) — see there
// for the deliberately-omitted Bun /proc/self/environ fallback.
func getProviderEnvValue(name string, env map[string]string) string {
	return ai.ProviderEnvValue(name, env)
}
