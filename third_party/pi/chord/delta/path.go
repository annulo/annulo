package delta

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
)

// ─── Paths ───────────────────────────────────────────────────────────────────

// Seg is one path segment: a Key into an object or an Index into an array.
//
// Upstream spells this `string | number`; the sealed interface keeps the two
// apart at the type level, so an applier can refuse a string-spelled index
// ("7") on an array without first parsing it.
type Seg interface {
	// seg seals the set to Key and Index.
	seg()
}

// Key is an object-key segment.
type Key string

// Index is an array-index segment. A valid index is non-negative and within
// Number.MAX_SAFE_INTEGER; Validate on the enclosing Path reports the rest.
// It is 64 bits wide on every platform, so a segment addresses the same slot
// or property on a 32-bit build as on a 64-bit one.
type Index int64

func (Key) seg()   {}
func (Index) seg() {}

func (k Key) String() string   { return string(k) }
func (i Index) String() string { return strconv.FormatInt(int64(i), 10) }

// MarshalJSON writes the key as a JSON string.
func (k Key) MarshalJSON() ([]byte, error) { return marshalJSON(string(k)) }

// MarshalJSON writes the index as a JSON number.
func (i Index) MarshalJSON() ([]byte, error) { return strconv.AppendInt(nil, int64(i), 10), nil }

// Path addresses a value inside a JSON tree: object keys and array indices,
// root first. The empty path is the root; only "p" may address it.
//
//	["operation", "message", "content", 0, "text"]
type Path []Seg

// PathID references a path the encoder interned with a "#" definition. IDs
// are non-negative and scoped to one encoder/decoder pair. Like Index, it is
// 64 bits wide on every platform.
type PathID int64

// PathRef is what a wire op carries in place of a path: the Path inline, or a
// PathID assigned by the encoder on the path's second use.
type PathRef interface {
	// pathRef seals the set to Path and PathID.
	pathRef()
}

func (Path) pathRef()   {}
func (PathID) pathRef() {}

// MarshalJSON writes the path as a JSON array; a nil path is the root, `[]`.
func (p Path) MarshalJSON() ([]byte, error) {
	if p == nil {
		return []byte("[]"), nil
	}
	return marshalJSON([]Seg(p))
}

// UnmarshalJSON reads a JSON array of strings (keys) and integers (indices).
// A number that is not an integer, or any other JSON value, is not a segment
// and fails here; a negative or unsafely large index decodes and fails
// Validate, as upstream's assertSafePath refuses it after the fact.
func (p *Path) UnmarshalJSON(data []byte) error {
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	parsed, err := parsePath(v)
	if err != nil {
		return err
	}
	*p = parsed
	return nil
}

// String is the path's JSON form — the text upstream puts in a PathError, and
// the key the codec's dictionary is built on (a joined string would collide on
// keys containing the separator; JSON cannot).
func (p Path) String() string {
	data, _ := p.MarshalJSON() // a Key or Index cannot fail to encode
	return string(data)
}

// Validate is upstream's assertSafePath: every segment must be either a key
// that is not reserved or a non-negative index. The first offender is
// returned as an *UnsafePathError.
//
// An index beyond Number.MAX_SAFE_INTEGER is unsafe too. It is past any
// array's append window, which is what pi's apply says of it, and a double
// cannot spell it exactly, so as an object key it would name a different
// property on each side.
func (p Path) Validate() error {
	for _, seg := range p {
		switch s := seg.(type) {
		case Key:
			if ReservedSegments[string(s)] {
				return &UnsafePathError{Segment: s}
			}
		case Index:
			if s < 0 || s > maxSafeInteger {
				return &UnsafePathError{Segment: s}
			}
		}
	}
	return nil
}

// parsePath turns the any-tree form of a path (a []any of strings and
// integral numbers) into a Path. It checks shape only; safety is the op's
// Validate, which is where upstream's assertPathArg runs assertSafePath. A Go
// producer may hand it a Path, or Key, Index, int, int64 and json.Number
// segments, as well as the float64 encoding/json produces.
func parsePath(v any) (Path, error) {
	if p, ok := v.(Path); ok {
		return p, nil
	}
	raw, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("%w: path is not an array: got %s (a bare key is not a path; wrap it as [key])", ErrInvalidOp, describe(v))
	}
	path := make(Path, 0, len(raw))
	for _, elem := range raw {
		seg, err := parseSeg(elem)
		if err != nil {
			return nil, err
		}
		path = append(path, seg)
	}
	return path, nil
}

func parseSeg(v any) (Seg, error) {
	switch s := v.(type) {
	case Seg:
		return s, nil
	case string:
		return Key(s), nil
	}
	if n, ok := integer(v); ok {
		return Index(n), nil
	}
	return nil, fmt.Errorf("%w: path segment %s is neither an object key (string) nor an array index (integer)", ErrInvalidOp, describe(v))
}

// ─── Path safety ─────────────────────────────────────────────────────────────

// ReservedSegments are the keys that reach the prototype chain in JavaScript.
//
// Every consumer of these ops is not necessarily this Go port: a replica in pi
// itself applies `parent[key] = value`, and paths are data. A Go producer that
// emits ["s", ["__proto__", "isAdmin"], true] hands a TypeScript replica a
// prototype-pollution primitive for its whole process — so the producer refuses
// the segment, the wire validator refuses it, and the applier refuses it, on
// both sides. The names are reserved as SEGMENTS only; a value may contain
// them as keys, because a value is written whole and never walked.
//
// Ops arrive from a facet, a plugin compartment, or a tool whose details may
// echo model output, so none of it is trusted input.
var ReservedSegments = map[string]bool{
	"__proto__":   true,
	"constructor": true,
	"prototype":   true,
}

// UnsafePathError reports a segment that must not be walked: a reserved key,
// a negative index, an index past the one-slot append window, or a key where
// an array wants an index.
type UnsafePathError struct{ Segment Seg }

func (e *UnsafePathError) Error() string {
	return fmt.Sprintf("unsafe path segment: %s (keys must not be %s; indices must be non-negative and at most one past the end)",
		e.Segment, strings.Join(slices.Sorted(maps.Keys(ReservedSegments)), ", "))
}

// PathError reports a path — or, in a decoder, a path id — that does not
// resolve against the value it addresses. The stream it came from is finished:
// discard the decoder and replica and recover from a later base batch.
type PathError struct{ Ref PathRef }

func (e *PathError) Error() string {
	return "unresolvable path: " + refString(e.Ref)
}

func refString(ref PathRef) string {
	switch r := ref.(type) {
	case Path:
		return r.String()
	case PathID:
		return strconv.FormatInt(int64(r), 10)
	default:
		return Path(nil).String()
	}
}

// ─── Numbers ─────────────────────────────────────────────────────────────────

// maxSafeInteger is JavaScript's Number.MAX_SAFE_INTEGER: the largest integer
// a double spells exactly, and so the range of an address — a segment or a
// path id — on the wire.
const maxSafeInteger = 1<<53 - 1

// integer reports v as an int64 when it is an integral number — one a
// JavaScript peer's Number.isInteger accepts, which includes 1e300. It reads
// the float64 encoding/json produces, the int kinds Go code writes, and the
// json.Number a UseNumber decoder yields. A magnitude past what an int64
// holds saturates: the callers that take an address check the safe range in
// Validate, and the ones that take a quantity clamp it (clampInt) against a
// length. It is int64 rather than int so that an address past 2^31 means the
// same on a 32-bit build.
func integer(v any) (int64, bool) {
	switch n := v.(type) {
	case int:
		return int64(n), true
	case int64:
		return n, true
	case int32:
		return int64(n), true
	case uint:
		return saturateUint(uint64(n)), true
	case uint32:
		return int64(n), true
	case uint64:
		return saturateUint(n), true
	case float64:
		return floatInteger(n)
	case float32:
		return floatInteger(float64(n))
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return i, true
		}
		if f, err := n.Float64(); err == nil {
			return floatInteger(f)
		}
	}
	return 0, false
}

// isNumber reports whether v is a numeric kind integer reads, integral or not.
func isNumber(v any) bool {
	switch v.(type) {
	case int, int64, int32, uint, uint32, uint64, float64, float32, json.Number:
		return true
	}
	return false
}

func floatInteger(f float64) (int64, bool) {
	if f != math.Trunc(f) || math.IsInf(f, 0) {
		return 0, false
	}
	// Past ±2^63 the conversion is undefined, and an integer this large
	// behaves like any other past-the-end quantity or unsafe address anyway.
	switch {
	case f >= -math.MinInt64:
		return math.MaxInt64, true
	case f <= math.MinInt64:
		return math.MinInt64, true
	}
	return int64(f), true
}

func saturateUint(u uint64) int64 {
	return int64(min(u, math.MaxInt64))
}

// clampInt is a quantity as an int: past what an int holds (2^31 on a 32-bit
// build) it saturates, which is past the end of every slice there.
func clampInt(n int64) int {
	return int(min(max(n, math.MinInt), math.MaxInt))
}

// describe names a value for an error message without printing a payload.
func describe(v any) string {
	switch v := v.(type) {
	case nil:
		return "null"
	case string:
		return strconv.Quote(v)
	case bool:
		return fmt.Sprint(v)
	case []any:
		return fmt.Sprintf("an array of %d", len(v))
	case map[string]any:
		return "an object"
	}
	if isNumber(v) {
		return fmt.Sprint(v)
	}
	return fmt.Sprintf("%T", v)
}

// marshalJSON is json.Marshal without HTML escaping, so a key or text with
// `<`, `>` or `&` reads the way JSON.stringify writes it.
func marshalJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}
