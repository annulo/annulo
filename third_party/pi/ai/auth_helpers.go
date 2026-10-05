package ai

import (
	"context"
	"sync"
)

// EnvAPIKeyAuth builds the standard api-key auth (pi
// packages/ai/src/auth/helpers.ts envApiKeyAuth): a stored credential key wins,
// otherwise the first set env var resolves. Includes a Login that prompts for
// the key (acquisition, out of scope but ported for parity). Providers with
// non-standard resolution (provider env, ambient files, IAM) write their own
// ApiKeyAuth.
func EnvAPIKeyAuth(name string, envVars ...string) *ApiKeyAuth {
	return &ApiKeyAuth{
		Name: name,
		Login: func(ctx context.Context, interaction AuthInteraction) (*Credential, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			key, err := interaction.Prompt(AuthPrompt{Type: AuthPromptSecret, Message: "Enter " + name})
			if err != nil {
				return nil, err
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			return &Credential{Type: CredentialAPIKey, Key: key}, nil
		},
		Resolve: func(ctx context.Context, authCtx AuthContext, credential *Credential) (*AuthResult, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if credential != nil && credential.Key != "" {
				return &AuthResult{Auth: ModelAuth{APIKey: credential.Key}, Env: credential.Env, Source: "stored credential"}, nil
			}
			for _, envVar := range envVars {
				if value := authCtx.Env(envVar); value != "" {
					return &AuthResult{Auth: ModelAuth{APIKey: value}, Source: envVar}, nil
				}
			}
			return nil, nil
		},
	}
}

// LazyOAuthOptions is LazyOAuth's input (pi helpers.ts lazyOAuth, b0bd0ff9d):
// the descriptor fields a provider advertises up front plus the deferred
// implementation load.
type LazyOAuthOptions struct {
	// Name is the display name, e.g. "Anthropic (Claude Pro/Max)".
	Name string

	// IsSubscription marks subscription-backed access (OAuthAuth.IsSubscription).
	IsSubscription bool

	// LoginLabel is the selector label for the OAuth login option
	// (OAuthAuth.LoginLabel; optional, "" when unset).
	LoginLabel string

	// Load constructs the implementation; called once on first use.
	Load func() (*OAuthAuth, error)
}

// LazyOAuth wraps a lazily-loaded OAuthAuth so provider definitions can
// advertise OAuth without constructing the implementation up front (pi
// helpers.ts lazyOAuth). The descriptor fields carry over to the wrapper so
// they are readable without loading; the implementation loads once on first
// Login/Refresh/ToAuth — pi memoizes the load promise, Go uses sync.Once.
func LazyOAuth(opts LazyOAuthOptions) *OAuthAuth {
	var (
		once    sync.Once
		loaded  *OAuthAuth
		loadErr error
	)
	get := func() (*OAuthAuth, error) {
		once.Do(func() { loaded, loadErr = opts.Load() })
		return loaded, loadErr
	}
	return &OAuthAuth{
		Name:           opts.Name,
		IsSubscription: opts.IsSubscription,
		LoginLabel:     opts.LoginLabel,
		Login: func(ctx context.Context, interaction AuthInteraction) (*Credential, error) {
			o, err := get()
			if err != nil {
				return nil, err
			}
			return o.Login(ctx, interaction)
		},
		Refresh: func(ctx context.Context, credential OAuthCredentials) (OAuthCredentials, error) {
			o, err := get()
			if err != nil {
				return OAuthCredentials{}, err
			}
			return o.Refresh(ctx, credential)
		},
		ToAuth: func(credential OAuthCredentials) (ModelAuth, error) {
			o, err := get()
			if err != nil {
				return ModelAuth{}, err
			}
			return o.ToAuth(credential)
		},
	}
}
