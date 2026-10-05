package providers

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/sky-valley/pi/ai"
)

// Cloudflare base URLs with {VAR} placeholders, ported from pi cloudflare.ts.
const (
	// cloudflareWorkersAIBaseURL is the Workers AI direct endpoint.
	cloudflareWorkersAIBaseURL = "https://api.cloudflare.com/client/v4/accounts/{CLOUDFLARE_ACCOUNT_ID}/ai/v1"
	// cloudflareAIGatewayCompatBaseURL is the AI Gateway Unified API.
	// https://developers.cloudflare.com/ai-gateway/usage/unified-api/
	cloudflareAIGatewayCompatBaseURL = "https://gateway.ai.cloudflare.com/v1/{CLOUDFLARE_ACCOUNT_ID}/{CLOUDFLARE_GATEWAY_ID}/compat"
	// cloudflareAIGatewayOpenAIBaseURL is the AI Gateway → OpenAI passthrough.
	// Used until /compat supports /v1/responses.
	cloudflareAIGatewayOpenAIBaseURL = "https://gateway.ai.cloudflare.com/v1/{CLOUDFLARE_ACCOUNT_ID}/{CLOUDFLARE_GATEWAY_ID}/openai"
	// cloudflareAIGatewayAnthropicBaseURL is the AI Gateway → Anthropic passthrough.
	cloudflareAIGatewayAnthropicBaseURL = "https://gateway.ai.cloudflare.com/v1/{CLOUDFLARE_ACCOUNT_ID}/{CLOUDFLARE_GATEWAY_ID}/anthropic"
)

// isCloudflareProvider reports whether provider is one of pi's Cloudflare
// providers (cloudflare.ts isCloudflareProvider).
func isCloudflareProvider(provider ai.ProviderId) bool {
	return provider == "cloudflare-workers-ai" || provider == "cloudflare-ai-gateway"
}

// cloudflareAIGatewayAuthHeaders is the header-owned auth of Cloudflare AI
// Gateway, ported from pi providers/cloudflare-auth.ts
// (cloudflareAIGatewayAuth().resolve): the gateway key rides in
// cf-aig-authorization, and the upstream provider's own auth headers are
// suppressed with deletion markers so no placeholder credential reaches the
// gateway (upstream a24fb9e96, #7030).
//
// pi produces these in the auth resolver and the adapters never mention
// Cloudflare; the Go port resolves this auth inline in the adapters instead
// (the 2026-06-24 divergence, because the compat globals do not route through
// the Models runtime). The markers, however, are the real mechanism — the
// adapters emit their normal auth header and this bundle deletes it, rather
// than each adapter branching on the provider before setting one.
func cloudflareAIGatewayAuthHeaders(apiKey string) ai.ProviderHeaders {
	gatewayAuth := "Bearer " + apiKey
	return ai.ProviderHeaders{
		"cf-aig-authorization": &gatewayAuth,
		"Authorization":        nil,
		"x-api-key":            nil,
	}
}

// cloudflarePlaceholderRe matches {VAR} placeholders, mirroring pi's
// /\{([A-Z_][A-Z0-9_]*)\}/g.
var cloudflarePlaceholderRe = regexp.MustCompile(`\{([A-Z_][A-Z0-9_]*)\}`)

// resolveCloudflareBaseURL substitutes `{VAR}` placeholders in a Cloudflare
// baseUrl from the environment (cloudflare.ts resolveCloudflareBaseUrl). It
// errors with pi's exact message when a referenced variable is unset or empty.
// Provider-scoped env overrides win over the OS environment (pi 7f29e7a3).
func resolveCloudflareBaseURL(model *ai.Model, env map[string]string) (string, error) {
	url := model.BaseURL
	if !strings.Contains(url, "{") {
		return url, nil
	}
	var missing string
	resolved := cloudflarePlaceholderRe.ReplaceAllStringFunc(url, func(match string) string {
		name := match[1 : len(match)-1]
		value := getProviderEnvValue(name, env)
		if value == "" {
			if missing == "" {
				missing = name
			}
			return match
		}
		return value
	})
	if missing != "" {
		return "", fmt.Errorf("%s is required for provider %s but is not set.", missing, model.Provider)
	}
	return resolved, nil
}
