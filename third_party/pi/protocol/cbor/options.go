// Package cbor implements the strict, definite-length RFC 8949 subset pi
// speaks on the wire (port of packages/protocol/src/cbor).
//
// The subset is deliberately narrow: no tags, no indefinite-length items, no
// half/single-precision floats, no non-string map keys, and integers confined
// to the IEEE-754 exactly-representable range. Every limit below exists so an
// untrusted peer cannot make the decoder allocate without bound; keeping them
// byte-for-byte identical to pi's is what lets a Go peer and a Node peer talk
// to each other at all.
package cbor

import (
	"fmt"
	"math"
)

const (
	uint32Base        = 1 << 32
	maxUint32  uint64 = 0xffff_ffff

	// maxSafeInteger is JavaScript's Number.MAX_SAFE_INTEGER. pi encodes every
	// number as a float64, so it rejects integers it could not round-trip. Go
	// has no such limit, but a Go peer that emitted one would be rejected by a
	// Node peer — so the port enforces pi's range on both sides.
	maxSafeInteger = 1<<53 - 1

	maxConfiguredDepth = 512
)

// Safe defaults for untrusted protocol payloads.
const (
	DefaultMaxByteLength      = 16 * 1024 * 1024
	DefaultMaxContainerLength = 1_000_000
	DefaultMaxDepth           = 64
)

// Options bounds what Encode and Decode will process. A nil field takes the
// corresponding default (pi's `??`), which is why these are pointers rather
// than plain ints: zero is a meaningful limit, not "unset".
type Options struct {
	// MaxByteLength caps encoded input/output bytes and any single byte or
	// text string.
	MaxByteLength *int
	// MaxContainerLength caps elements in an array or entries in a map.
	MaxContainerLength *int
	// MaxDepth caps recursive item depth.
	MaxDepth *int
}

type resolved struct {
	maxByteLength      int
	maxContainerLength int
	maxDepth           int
}

// Error is a malformed-or-out-of-policy CBOR payload (pi's CborError).
type Error struct {
	Msg string
	// limit marks a well-formed item refused only for exceeding a configured
	// limit, in the words pi's encoder and decoder both use for it.
	limit bool
}

func (e *Error) Error() string { return e.Msg }

func cborErrf(format string, a ...any) *Error {
	return &Error{Msg: fmt.Sprintf(format, a...)}
}

// limitErrf is cborErrf for a configured limit exceeded.
func limitErrf(format string, a ...any) *Error {
	return &Error{Msg: fmt.Sprintf(format, a...), limit: true}
}

// RangeError is an invalid Options value — a caller bug, not a peer's fault,
// which is why it is a distinct type from Error.
type RangeError struct{ Msg string }

func (e *RangeError) Error() string { return e.Msg }

func resolveLimit(name string, value *int, def int, maximum uint64) (int, error) {
	v := def
	if value != nil {
		v = *value
	}
	if v < 0 || uint64(v) > maximum {
		return 0, &RangeError{Msg: fmt.Sprintf("%s must be an integer between 0 and %d", name, maximum)}
	}
	return v, nil
}

func resolveOptions(o *Options) (resolved, error) {
	// A nil Options means "every limit unset", which is what the zero value
	// already says — pi's `options?.x ?? default` with the optional chain
	// hoisted out of the three reads.
	var opts Options
	if o != nil {
		opts = *o
	}
	var (
		r   resolved
		err error
	)
	if r.maxByteLength, err = resolveLimit("maxByteLength", opts.MaxByteLength,
		DefaultMaxByteLength, maxUint32); err != nil {
		return resolved{}, err
	}
	if r.maxContainerLength, err = resolveLimit("maxContainerLength", opts.MaxContainerLength,
		DefaultMaxContainerLength, maxUint32); err != nil {
		return resolved{}, err
	}
	if r.maxDepth, err = resolveLimit("maxDepth", opts.MaxDepth,
		DefaultMaxDepth, maxConfiguredDepth); err != nil {
		return resolved{}, err
	}
	return r, nil
}

// isIntegralFloat reports whether v is an integer value that pi would encode as
// a CBOR integer rather than a float. pi's test is Number.isInteger(v) &&
// !Object.is(v, -0), so negative zero stays a float even though it is integral.
func isIntegralFloat(v float64) bool {
	if math.IsInf(v, 0) || math.IsNaN(v) {
		return false
	}
	if v == 0 && math.Signbit(v) {
		return false
	}
	return v == math.Trunc(v)
}
