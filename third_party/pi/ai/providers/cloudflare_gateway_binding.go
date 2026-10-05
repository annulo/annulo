package providers

// AI Gateway transport over the Workers AI binding (pi 230029078,
// packages/ai/src/api/cloudflare-gateway-binding.ts).
//
// pi's Cloudflare AI Gateway support speaks HTTPS
// (`gateway.ai.cloudflare.com/v1/{account}/{gateway}/{provider}/...`, see
// cloudflare.go), which needs a Cloudflare API token even when the caller is a
// Worker in the gateway's own account.
//
// NewGatewayBindingDoer returns an [ai.HTTPDoer] that translates requests under
// a gateway HTTPS prefix into calls to the Workers AI binding's universal
// endpoint, `env.AI.gateway(id).run({provider, endpoint, headers, query})`.
// Binding calls are pre-authenticated in-account and return the provider's
// native wire format as a regular (streaming) response, so API implementations
// behave identically over either transport.
//
// The result is the transport for one gateway-bound client, not a
// general-purpose doer: requests it cannot serve — URLs outside the prefix, or
// in-prefix requests the universal endpoint cannot express (non-POST, non-JSON
// body) — fail with a descriptive error. Transport selection is the caller's
// job, per client: route such traffic over HTTPS with real gateway auth
// instead of through this shim.
//
// This surface is LATENT in the Go port: an [AIGatewayBinding] can only come
// from a Cloudflare Workers runtime, which Go does not target, so nothing in
// the port supplies one today. It is ported because it is public SDK surface
// under pi-ai's "./api/*" subpath export — the same reason StreamOptions.Env
// and ProviderRequestOptions.TelemetryContext are ported latent. See the
// 2026-08-11 ruling in docs/UPSTREAM.md.
//
// pi's fetch-input polymorphism has no counterpart here. Its
// createGatewayBindingFetch must reconcile two overlapping sources of truth
// (a Request input and a RequestInit override) across six body types and four
// header shapes, with fetch-spec rules for `body: null` and `signal: null`.
// An *http.Request has already normalized all of that, so the port keeps the
// observable behavior and drops the reconciliation.
//
// What does NOT come for free is pi's URL layer: `new URL()` applies WHATWG
// normalization that neither url.Parse nor path.Clean reproduces. Empty path
// segments survive, a trailing "."/".." leaves a trailing slash, the host is
// lowercased and a default port is dropped. resolveGatewayPath and
// normalizedGatewayOrigin below exist to match it; path.Clean is wrong here
// because it collapses "//" (which would change how provider/endpoint splits)
// and drops the trailing slash (which decides which rejection fires).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/internal/jstext"
)

// AIGatewayBinding is the Workers AI binding's gateway surface (`env.AI`). pi
// declares it structurally so the module needs no @cloudflare/workers-types
// dependency; the Go equivalent of that is an interface a host satisfies.
type AIGatewayBinding interface {
	Gateway(id string) AIGatewayBindingGateway
}

// AIGatewayBindingGateway issues universal-endpoint requests for one gateway.
//
// pi passes an AbortSignal in a trailing options bag; cancellation here travels
// on the context, as everywhere else in the port.
type AIGatewayBindingGateway interface {
	Run(ctx context.Context, req AIGatewayUniversalRequest) (*http.Response, error)
}

// AIGatewayUniversalRequest is one universal-endpoint request entry, as accepted
// by `AiGateway.run()`.
type AIGatewayUniversalRequest struct {
	Provider string
	Endpoint string
	Headers  map[string]string
	// Query is the validated JSON request body, passed through byte-for-byte.
	//
	// pi types this `unknown` and hands over whatever JSON.parse returned,
	// because a JS object is what AiGateway.run() takes. Decoding to `any` here
	// instead would be lossy in two ways HTTPS is not: every number would become
	// a float64 (corrupting integer ids past 2^53), and Go map iteration would
	// replace the body's key order with encoding/json's alphabetical order —
	// where JS preserves insertion order and therefore re-serializes the keys as
	// the provider wrote them. Raw bytes keep both properties, so the body a
	// host forwards is the body the provider built.
	Query json.RawMessage
}

// CloudflareGatewayBindingAuthSentinel is the placeholder value for auth headers
// on binding-routed requests. API implementations require an API key or a
// recognized auth header (`authorization`, `x-api-key`, `cf-aig-authorization`)
// before dispatch; binding calls are pre-authenticated, so pass
// `cf-aig-authorization: Bearer ` + CloudflareGatewayBindingAuthSentinel to
// satisfy the check. The shim strips `cf-aig-authorization` before calling the
// binding. Pair it with nil-valued `Authorization` / `x-api-key` entries in
// [ai.ProviderHeaders] so the SDKs' placeholder auth headers never reach the
// gateway, which would treat a request-supplied auth header as a BYOK provider
// key that overrides its stored keys — the same as it would over HTTPS.
const CloudflareGatewayBindingAuthSentinel = "cloudflare-gateway-binding"

// Failure classes a caller can branch on. pi throws plain Errors here; sentinels
// let an SDK consumer decide to re-route over HTTPS without string-matching,
// while Error() stays byte-identical to pi's message (the retry.go precedent).
// These are exported, unlike errServerRetryDelayTooLong, because the consumer of
// this transport is outside the repo.
var (
	// ErrOutsideGatewayPrefix marks a request URL that does not fall under the
	// configured gateway prefix. Route it over HTTPS instead.
	ErrOutsideGatewayPrefix = errors.New("request URL is outside the configured gateway prefix")
	// ErrUnexpressibleGatewayRequest marks an in-prefix request the universal
	// endpoint cannot express (non-POST, absent or non-JSON body, or no
	// provider/endpoint path). Route it over HTTPS with gateway auth instead.
	ErrUnexpressibleGatewayRequest = errors.New("request cannot be expressed as a universal gateway request")
)

type gatewayBindingError struct {
	sentinel error
	msg      string
}

func (e *gatewayBindingError) Error() string { return e.msg }

func (e *gatewayBindingError) Is(target error) bool { return target == e.sentinel }

// gatewayBindingStripHeaders are never forwarded to the binding: hop-by-hop and
// derived headers, plus gateway auth (binding calls are pre-authenticated; the
// sentinel must not reach the wire).
var gatewayBindingStripHeaders = map[string]bool{"content-length": true, "host": true, "cf-aig-authorization": true}

// gatewayBindingDoer routes AI Gateway requests through the Workers AI binding.
// It is safe for concurrent use: every field is set at construction and only
// read thereafter.
type gatewayBindingDoer struct {
	binding  AIGatewayBinding
	gateway  string
	origin   string
	basePath string
}

// NewGatewayBindingDoer creates an [ai.HTTPDoer] that routes AI Gateway requests
// through the Workers AI binding. See the file-level comment for behavior and
// composition notes.
//
// pi's constructor throws on an unparseable baseUrl; this returns the error.
func NewGatewayBindingDoer(opts GatewayBindingDoerOptions) (ai.HTTPDoer, error) {
	if opts.Binding == nil {
		return nil, errors.New("NewGatewayBindingDoer: Binding is required; pass the Workers AI binding (env.AI)")
	}
	base, err := url.Parse(opts.BaseURL)
	if err != nil {
		return nil, fmt.Errorf("NewGatewayBindingDoer: BaseURL %q is not a valid URL: %w", opts.BaseURL, err)
	}
	if base.Scheme == "" || base.Host == "" {
		return nil, fmt.Errorf("NewGatewayBindingDoer: BaseURL %q must be absolute, like "+
			"https://gateway.ai.cloudflare.com/v1/{accountId}/{gatewayName}", opts.BaseURL)
	}
	basePath := resolveGatewayPath(base.EscapedPath())
	if !strings.HasSuffix(basePath, "/") {
		basePath += "/"
	}
	return &gatewayBindingDoer{
		binding:  opts.Binding,
		gateway:  opts.Gateway,
		origin:   normalizedGatewayOrigin(base),
		basePath: basePath,
	}, nil
}

// GatewayBindingDoerOptions configures [NewGatewayBindingDoer].
type GatewayBindingDoerOptions struct {
	// Binding is the Workers AI binding (e.g. `env.AI`).
	Binding AIGatewayBinding
	// BaseURL is the gateway HTTPS prefix every request must fall under,
	// without a trailing slash:
	// `https://gateway.ai.cloudflare.com/v1/{accountId}/{gatewayName}`.
	BaseURL string
	// Gateway is the gateway name passed to Binding.Gateway. It must match the
	// BaseURL gateway.
	Gateway string
}

// normalizedGatewayOrigin renders a URL's origin the way WHATWG does: scheme and
// host lowercased, and a scheme-default port dropped. url.Parse lowercases the
// scheme but leaves the host and an explicit `:443` alone, so comparing raw
// u.Host would reject requests pi accepts.
func normalizedGatewayOrigin(u *url.URL) string {
	scheme := strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Host)
	defaultPort := ""
	switch scheme {
	case "https":
		defaultPort = ":443"
	case "http":
		defaultPort = ":80"
	}
	if defaultPort != "" && strings.HasSuffix(host, defaultPort) {
		host = strings.TrimSuffix(host, defaultPort)
	}
	return scheme + "://" + host
}

// resolveGatewayPath resolves dot segments in an absolute URL path the way the
// WHATWG URL parser does. Unlike path.Clean it preserves empty segments and
// keeps the trailing slash that a final "." or ".." leaves behind — see the
// file-level comment for why both matter.
func resolveGatewayPath(escapedPath string) string {
	if escapedPath == "" {
		return "/"
	}
	segments := strings.Split(escapedPath, "/")
	out := make([]string, 0, len(segments))
	for i, segment := range segments {
		last := i == len(segments)-1
		switch segment {
		case ".":
			if last {
				out = append(out, "")
			}
		case "..":
			// Never pop the leading empty segment: "/.." stays at the root.
			if len(out) > 1 {
				out = out[:len(out)-1]
			}
			if last {
				out = append(out, "")
			}
		default:
			out = append(out, segment)
		}
	}
	return strings.Join(out, "/")
}

func (d *gatewayBindingDoer) Do(req *http.Request) (*http.Response, error) {
	// This stands in for *http.Client, whose contract is that the transport
	// closes a non-nil body — callers written against it do not close their own.
	if req.Body != nil {
		defer req.Body.Close()
	}

	// pi resolves `init.method ?? request.method ?? "GET"` and uppercases it. The
	// uppercasing is kept for parity even though http.Client would send "post"
	// verbatim (and the gateway would reject it).
	method := req.Method
	if method == "" {
		method = http.MethodGet
	}
	method = strings.ToUpper(method)

	rawURL := ""
	if req.URL != nil {
		// Redacted keeps any userinfo out of an error a caller may log.
		rawURL = req.URL.Redacted()
	}
	if req.URL == nil {
		return nil, d.outsidePrefix(method, rawURL)
	}
	// Split on the ENCODED path: url.URL.Path is percent-decoded, so a segment
	// containing %2F would otherwise split as if it were a separator, unlike
	// what http.Client puts on the wire and unlike pi's URL.pathname.
	reqPath := resolveGatewayPath(req.URL.EscapedPath())
	if normalizedGatewayOrigin(req.URL) != d.origin || !strings.HasPrefix(reqPath, d.basePath) {
		return nil, d.outsidePrefix(method, rawURL)
	}
	if method != http.MethodPost {
		return nil, unexpressibleGatewayRequest(method, rawURL, "only POST is supported")
	}

	rest := strings.TrimPrefix(reqPath, d.basePath)
	slash := strings.Index(rest, "/")
	if slash <= 0 {
		return nil, unexpressibleGatewayRequest(method, rawURL, "missing provider/endpoint path")
	}
	provider := rest[:slash]
	// Keep the query string on the endpoint — it is part of what HTTPS would send.
	endpoint := rest[slash+1:]
	if req.URL.RawQuery != "" {
		endpoint += "?" + req.URL.RawQuery
	}

	// Only an absent body is "missing body". A present-but-empty one reads as
	// zero bytes and falls through to "non-JSON body", because that is what
	// JSON.parse("") does — http.NoBody is present-and-empty, not absent.
	if req.Body == nil {
		return nil, unexpressibleGatewayRequest(method, rawURL, "missing body")
	}
	// Consuming a one-shot body here is fine — unexpressible requests reject
	// rather than replay, so nothing downstream needs it again.
	bodyBytes, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, &gatewayBindingError{sentinel: ErrUnexpressibleGatewayRequest, msg: fmt.Sprintf(
			"NewGatewayBindingDoer: cannot express %s %s as a universal gateway request (unreadable body: %s); "+
				"route it over HTTPS with gateway auth instead", method, rawURL, err)}
	}
	if !json.Valid(bodyBytes) {
		return nil, unexpressibleGatewayRequest(method, rawURL, "non-JSON body")
	}

	return d.binding.Gateway(d.gateway).Run(req.Context(), AIGatewayUniversalRequest{
		Provider: provider,
		Endpoint: endpoint,
		Headers:  collectGatewayHeaders(req.Header),
		Query:    bodyBytes,
	})
}

// outsidePrefix mirrors pi's out-of-prefix throw. Out-of-prefix URLs are a
// configuration bug, not passthrough traffic: silently forwarding would ship the
// auth sentinel to whatever host the URL names.
//
// The message names the constructor rather than Do, as pi's names
// createGatewayBindingFetch rather than the fetch it returns.
func (d *gatewayBindingDoer) outsidePrefix(method, rawURL string) error {
	return &gatewayBindingError{sentinel: ErrOutsideGatewayPrefix, msg: fmt.Sprintf(
		"NewGatewayBindingDoer: %s %s is outside the configured gateway prefix (%s%s); "+
			"this doer only serves its gateway-bound client", method, rawURL, d.origin, d.basePath)}
}

// unexpressibleGatewayRequest mirrors pi's unexpressible throw. In-prefix
// requests the universal endpoint cannot express always reject: forwarding them
// over HTTPS would send the sentinel to the gateway and fail with a misleading
// auth error instead of naming the real problem. Callers that need such
// endpoints route them over HTTPS with real gateway auth themselves.
func unexpressibleGatewayRequest(method, rawURL, reason string) error {
	return &gatewayBindingError{sentinel: ErrUnexpressibleGatewayRequest, msg: fmt.Sprintf(
		"NewGatewayBindingDoer: cannot express %s %s as a universal gateway request (%s); "+
			"route it over HTTPS with gateway auth instead", method, rawURL, reason)}
}

// collectGatewayHeaders lowercases entry header names so case-variant duplicates
// collapse and stripping is uniform, joining multi-value headers the way a fetch
// Headers iterator yields them. It reads each value as that iterator does, one
// character per byte (see isomorphicDecode): the adapters write a value's
// U+0080..U+00FF characters as single Latin-1 bytes, as pi's Headers holds them
// (see byteString), and pi hands the binding those characters, not bytes.
//
// pi collapses case-variant duplicates in insertion order, which http.Header
// (a map) does not have. Names are visited in sorted order so the collapse is at
// least deterministic; the case only arises from a raw map write, since
// Header.Set and Header.Add canonicalize.
func collectGatewayHeaders(h http.Header) map[string]string {
	names := make([]string, 0, len(h))
	for key := range h {
		names = append(names, key)
	}
	slices.Sort(names)

	result := make(map[string]string, len(h))
	for _, key := range names {
		name := strings.ToLower(key)
		if gatewayBindingStripHeaders[name] {
			continue
		}
		value := jstext.IsomorphicDecode(strings.Join(h[key], ", "))
		if existing, ok := result[name]; ok {
			value = existing + ", " + value
		}
		result[name] = value
	}
	return result
}
