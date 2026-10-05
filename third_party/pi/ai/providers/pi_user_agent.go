package providers

import (
	"runtime"
	"sync"

	"github.com/sky-valley/pi/ai"
)

// piUserAgent returns the pi runtime user agent, ported from pi
// utils/pi-user-agent.ts getPiUserAgent(): "pi (<platform> <release>; <arch>)"
// from Node os.platform()/os.release()/os.arch(), or "pi (browser)" when the
// runtime has no node:os. Platform and arch use Node's names, not Go's
// (win32/x64/ia32), so the wire string matches pi byte-for-byte; the browser
// fallback maps to GOOSes where the release syscall is unavailable. The kernel
// release cannot change mid-process, so the string is computed once.
var piUserAgent = sync.OnceValue(func() string {
	release, ok := osRelease()
	if !ok {
		return "pi (browser)"
	}
	return "pi (" + nodePlatform() + " " + release + "; " + nodeArch() + ")"
})

// piUserAgentHeaders is pi's runtime user agent as the FIRST header source of a
// provider request (pi 87af49dec: every adapter's merge now starts with
// `{"User-Agent": getPiUserAgent()}`).
//
// It is a default, not an override. Upstream previously FORCED this string over
// every other source for two providers — kimi-coding in anthropic-messages
// (9d2ec7ffa) and xai in both openai adapters (70e878d4c, via a now-deleted
// forcePiUserAgent) — by deleting every case variant last. 87af49dec reversed
// that: the string is spread first, so model.Headers, the provider's own
// identity headers, the attribution bundle and the consumer's options.Headers
// all win over it, and a deletion marker in any of them suppresses it. Four
// providers that sent no user agent at all now send one by default; kimi-coding
// and xai no longer outrank a caller.
//
// The name is spelled exactly as upstream spells it, because the spelling is
// load-bearing: it claims slot 0 of the merged object, and a later source that
// reuses that spelling writes back into slot 0 rather than moving to the end
// (see headerObject). The anthropic OAuth branch is where that shows — it holds
// claude-cli/<v> under the lowercase name, at a later slot.
//
// The *string is freshly allocated per call, so nothing downstream can write
// through the pointer into shared state (see ai.ProviderHeaders' aliasing
// contract).
func piUserAgentHeaders() ai.ProviderHeaders {
	ua := piUserAgent()
	return ai.ProviderHeaders{"User-Agent": &ua}
}

// nodePlatform maps runtime.GOOS to Node's process.platform name. They agree
// everywhere the port builds except Windows.
func nodePlatform() string {
	if runtime.GOOS == "windows" {
		return "win32"
	}
	return runtime.GOOS
}

// nodeArch maps runtime.GOARCH to Node's os.arch() name (documented values:
// arm, arm64, ia32, loong64, mips, mipsel, ppc64, riscv64, s390x, x64).
func nodeArch() string {
	switch runtime.GOARCH {
	case "amd64":
		return "x64"
	case "386":
		return "ia32"
	case "mipsle":
		return "mipsel"
	case "ppc64le":
		return "ppc64"
	default:
		return runtime.GOARCH
	}
}
