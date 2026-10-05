package providers

// The built-in API providers register themselves when this package is
// imported, mirroring pi, where importing @earendil-works/pi-ai runs
// registerBuiltInApiProviders() as a module-load side effect
// (providers/register-builtins.ts). The coding package imports this package,
// so SDK consumers get a populated registry without any manual wiring.
func init() {
	RegisterBuiltins()
}

// RegisterBuiltins registers all built-in real API providers. It runs
// automatically via init() when this package is imported; calling it again is
// harmless (kept for compatibility and for re-registering after a registry
// reset in tests).
func RegisterBuiltins() {
	RegisterAnthropic()
	RegisterOpenAICompletions()
	RegisterOpenAIResponses()
	RegisterGoogle()
	RegisterPiMessages()
}
