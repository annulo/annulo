package ai

import "os"

// ProviderEnvValue resolves a provider configuration value from the per-stream
// scoped overrides first, then the OS environment. It ports pi's
// getProviderEnvValue (utils/provider-env.ts, 7f29e7a3): a scoped override wins
// over process.env, and an empty override falls through (pi uses `||`, so ""
// is not a real value).
//
// pi additionally consults a Bun sandbox fallback that re-reads
// /proc/self/environ to work around oven-sh/bun#27802, where Bun compiled
// binaries expose an empty process.env inside Linux sandboxes. Go binaries have
// no such defect — os.Getenv is always backed by the real environment — so that
// tier is deliberately omitted as a runtime workaround with no Go analog.
//
// This is the single source of the scoped-env precedence; the ai/providers
// helper delegates here, matching pi's one shared getProviderEnvValue.
func ProviderEnvValue(name string, env map[string]string) string {
	if v := env[name]; v != "" {
		return v
	}
	return os.Getenv(name)
}
