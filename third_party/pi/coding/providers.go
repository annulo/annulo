package coding

// Importing the providers package registers the built-in API providers
// (Anthropic, OpenAI Chat Completions + Responses, Google) as an import side
// effect, mirroring pi where the coding agent's import of @earendil-works/pi-ai
// runs registerBuiltInApiProviders() at module load. This guarantees that a
// session built from a catalog model is streamable without any manual wiring:
// ResolveModel + NewSession + Run just work.
import _ "github.com/sky-valley/pi/ai/providers"
