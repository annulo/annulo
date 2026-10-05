// Package ai is the Go port of pi's unified multi-provider LLM layer
// (@earendil-works/pi-ai): message, content, tool, and model types; the
// channel-based EventStream and streaming protocol; JSON-Schema tool
// validation; the embedded model catalog with cost accounting; and the
// provider registry. Concrete providers live in the providers subpackage.
//
// Two layers, mirroring pi's post-model-registry structure (upstream 732bb161):
//   - The Models runtime object-model — CreateModels/CreateProvider, the
//     Provider and Models interfaces, the credential/auth substrate
//     (CredentialStore, ProviderAuth, AuthContext, resolveProviderAuth), and
//     BuiltinModels. This is the primary surface.
//   - The global free functions (Stream/Complete, GetModel/GetModels/
//     GetProviders, GetEnvApiKey, RegisterApiProvider) — the compat surface,
//     equivalent to pi's "@earendil-works/pi-ai/compat". Existing callers keep
//     working unchanged.
package ai
