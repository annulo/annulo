package providers

import (
	"bytes"
	"encoding/json"
)

// compatOverrides is a model's `compat` blob held as its top-level keys, each
// still undecoded.
//
// pi resolves every compat key on its own — `model.compat?.<key> ?? <default>`
// — so one key can never disturb another. Decoding the blob into a single Go
// struct cannot reproduce that, and not for the reason it first appears:
// encoding/json does NOT stop at a type mismatch. It records the error, skips
// that one value, and decodes every other key normally — but it has already
// allocated the mistyped field's own pointer and left it at the zero value.
// So neither branch is right. "Keep what decoded" turns a mistyped flag into an
// explicit false, since its pointer is non-nil; "drop everything when the blob
// errors" reverts every sibling override, which is the bug this replaced.
// Holding the values undecoded and decoding one at a time is.
type compatOverrides map[string]json.RawMessage

// newCompatOverrides splits a compat blob into its top-level keys. A blob that
// is absent, JSON null, not an object, or not valid JSON at all carries no
// overrides and every default stands: pi reads `model.compat?.<key>` off such a
// value and gets undefined for every key.
func newCompatOverrides(blob json.RawMessage) compatOverrides {
	if len(blob) == 0 {
		return nil
	}
	var o compatOverrides
	if json.Unmarshal(blob, &o) != nil {
		return nil
	}
	return o
}

// value returns one key's undecoded value. An explicit null counts as absent:
// pi's `??` falls through on null exactly as it does on undefined, so a null
// must leave the default standing rather than zero the field.
func (o compatOverrides) value(key string) (json.RawMessage, bool) {
	raw, ok := o[key]
	if !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, false
	}
	return raw, true
}

// applyCompat resolves one compat key into dst, reporting whether it applied.
// A key that is absent, null, or whose value does not have dst's type leaves dst
// at its default — a type-mismatched key costs only itself, never its siblings
// and never the default it failed to override.
//
// The value is decoded into a fresh T rather than into *dst because
// encoding/json populates as it goes: a slice with one unreadable element
// leaves half-built entries in dst before it errors (pinned by
// TestAnthropicCompatSurvivesMalformedFallbackTargets/legacy_string_targets).
// Assigning only a fully decoded T discards those. It also keeps pi's `??`
// replace semantics for object-valued overrides, where decoding in place would
// merge into the default rather than replace it.
func applyCompat[T any](o compatOverrides, key string, dst *T) bool {
	raw, ok := o.value(key)
	if !ok {
		return false
	}
	var v T
	if json.Unmarshal(raw, &v) != nil {
		return false
	}
	*dst = v
	return true
}
