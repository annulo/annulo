package ai

import (
	"fmt"
	"regexp"
	"strings"
)

// providerErrorPattern is pi's buildProviderErrorPattern result,
// new RegExp(patterns.join("|"), "i"), matched with that RegExp's semantics
// rather than RE2's. A JavaScript RegExp without the "u" flag differs from RE2
// in two ways these patterns can observe:
//
//   - "i" compares the toUpperCase of each character but never lets a
//     non-ASCII character canonicalize to an ASCII one, so against all-ASCII
//     patterns it folds ASCII letters only. RE2's (?i) folds by Unicode, under
//     which U+212A KELVIN SIGN matches "k" and U+017F LATIN SMALL LETTER LONG S
//     matches "s". The alternatives are therefore compiled lowercased, without
//     (?i), and matched against the input with only A-Z lowercased.
//   - "." matches one UTF-16 code unit other than a line terminator (\n, \r,
//     U+2028, U+2029), where RE2's matches one code point other than \n. Every
//     "." in these lists sits between ASCII literals. A surrogate always has
//     its partner on one side, so it never sits between two ASCII characters,
//     and the code units such a "." can match are exactly the BMP code points:
//     an astral character is excluded, not counted as two. Go strings cannot
//     hold a lone surrogate; an invalid UTF-8 byte decodes as one U+FFFD,
//     which "." matches in both.
type providerErrorPattern struct {
	source string // RegExp.prototype.source: the alternatives joined with "|"
	re     *regexp.Regexp
}

// jsDot is "." of a JavaScript RegExp without the "u" flag, as the RE2 class
// matching the same text where every "." is flanked by ASCII literals.
const jsDot = `[^\n\r\x{2028}\x{2029}\x{10000}-\x{10FFFF}]`

func buildProviderErrorPattern(patterns []string) providerErrorPattern {
	alternatives := make([]string, len(patterns))
	for i, pattern := range patterns {
		if strings.ContainsAny(pattern, `\^$|*+()[]{}`) {
			// Lowercasing would corrupt escapes and classes (\S is not \s), and
			// RE2 and JavaScript disagree on several of them.
			panic(fmt.Sprintf("ai: provider error pattern %q uses RegExp syntax beyond literals, \".\" and \"?\"; "+
				"teach buildProviderErrorPattern its JavaScript semantics before adding it", pattern))
		}
		alternatives[i] = strings.ReplaceAll(asciiLower(pattern), ".", jsDot)
	}
	return providerErrorPattern{
		source: strings.Join(patterns, "|"),
		re:     regexp.MustCompile(strings.Join(alternatives, "|")),
	}
}

// MatchString is RegExp.prototype.test on s.
func (p providerErrorPattern) MatchString(s string) bool {
	return p.re.MatchString(asciiLower(s))
}

// asciiLower lowercases A-Z and leaves every other byte as it is, so UTF-8
// sequences pass through untouched.
func asciiLower(s string) string {
	for i := 0; i < len(s); i++ {
		if 'A' <= s[i] && s[i] <= 'Z' {
			b := []byte(s)
			for j := i; j < len(b); j++ {
				if 'A' <= b[j] && b[j] <= 'Z' {
					b[j] += 'a' - 'A'
				}
			}
			return string(b)
		}
	}
	return s
}

// nonRetryableProviderLimitErrorPattern matches provider error text that
// indicates a subscription/account/billing limit rather than a transient
// failure. Matches here suppress retries.
var nonRetryableProviderLimitErrorPattern = buildProviderErrorPattern([]string{
	// OpenCode Go/free-tier limits returned as 429 JSON error types by OpenCode's
	// Zen API. These are subscription/account limits, not transient throttles.
	"GoUsageLimitError",
	"FreeUsageLimitError",

	// OpenCode Go subscription-limit text asks users to enable available-balance
	// usage after rolling/weekly/monthly limits are reached.
	"Monthly usage limit reached",
	"available balance",

	// Generic quota/budget/billing exhaustion. `insufficient_quota` is OpenAI's
	// quota/billing error code; the other strings cover common gateway wording.
	"insufficient_quota",
	"out of budget",
	"quota exceeded",
	"billing",
})

// retryableProviderErrorPattern matches provider/transport error text that
// looks like a transient failure worth retrying.
var retryableProviderErrorPattern = buildProviderErrorPattern([]string{
	// Generic provider load, HTTP status, and server-side transient failures.
	"overloaded",
	// Azure OpenAI's peak-load capacity refusal (#9669): a transient capacity
	// message, not an account limit.
	"currently experiencing high demand",
	"rate.?limit",
	"too many requests",
	"429",
	"500",
	"502",
	"503",
	"504",
	"520", // Cloudflare unknown-error status (#9627)
	"524", // Cloudflare origin-timeout status (#6239)
	"service.?unavailable",
	"server.?error",
	"internal.?error",

	// Wrapper/provider text for transient upstream failures, including OpenRouter
	// "Provider returned error" responses (#2264).
	"provider.?returned.?error",
	"exceeded request buffer limit while retrying upstream",

	// Network, proxy, and fetch transport failures. This includes OpenAI Codex
	// raw-fetch failures such as "upstream connect", "connection refused", and
	// "reset before headers" (#733), plus OpenRouter connection drops (#3317).
	"network.?error",
	"connection.?error",
	"connection.?refused",
	"connection.?lost",
	"other side closed",
	"fetch failed",
	// DNS resolution failures surface as node/libuv transport errors (#6904).
	"getaddrinfo",
	"ENOTFOUND",
	"EAI_AGAIN",
	"upstream.?connect",
	"reset before headers",
	"socket hang up",
	"socket connection was closed",
	"timed? out",
	"timeout",
	"terminated",

	// WebSocket transports can report close/error text instead of HTTP/fetch text.
	"websocket.?closed",
	"websocket.?error",

	// Premature stream endings from SDKs and transports. Anthropic can throw
	// "stream ended without ..." and "Anthropic stream ended before message_stop"
	// (#4433); Bedrock/Smithy can throw an HTTP/2 no-response error (#3594).
	"ended without",
	"stream ended before message_stop",
	"stream ended before a terminal response event",
	"http2 request did not get a response",

	// Provider-requested retry delay cap failures should flow through the outer
	// retry policy so callers can surface/abort the backoff (#1123).
	"retry delay",

	// Explicit retry guidance emitted mid-stream by OpenAI Responses and Bedrock
	// stream exceptions (#6019).
	"you can retry your request",
	"try your request again",
	"please retry your request",

	// gRPC based providers (e.g. NVIDIA NIM) surface transient throttles as a
	// ResourceExhausted status (#6449).
	"ResourceExhausted",
})

// IsRetryableAssistantError classifies whether a failed assistant message looks
// like a transient provider or transport error, so callers can decide if the
// last assistant turn should be restarted.
//
// This does not implement retry policy. Callers should first handle context
// overflow separately, then apply their own retry budget, backoff, and reporting
// before restarting the assistant turn.
func IsRetryableAssistantError(m AssistantMessage) bool {
	if m.StopReason != StopError || m.ErrorMessage == "" {
		return false
	}
	if nonRetryableProviderLimitErrorPattern.MatchString(m.ErrorMessage) {
		return false
	}
	return retryableProviderErrorPattern.MatchString(m.ErrorMessage)
}
