package chord

import (
	"fmt"

	"github.com/sky-valley/pi/chord/delta"
	"github.com/sky-valley/pi/chord/internal/jsonvalue"
)

// Value is a strict JSON value: nil, bool, a finite number, a string, an
// array of Values, or a string-keyed object of Values. It is an alias so any
// decoded tree already is one; [IsValue] is the check that a given value
// keeps the contract, and [CopyJSON] the copy that makes one owned.
type Value = any

// ValueError reports a value that is not strict JSON: Message is upstream's
// TypeError text, Value the offending Go value (nil for a cycle). A draft
// refuses a placement with the same type (delta.ValueError).
type ValueError = jsonvalue.Error

// IsValue reports whether value is finite strict JSON with plain containers
// and no cycles, without normalizing it.
//
// Every Go numeric kind is a number, as every JS number is, and so is a
// json.Number; only a finite one passes. A slice or array of anything but
// bytes is an array — a []byte is a binary blob, the way a Uint8Array is
// upstream, and encoding/json would write it as a base64 string rather than
// an array. A map with a string-kind key is an object. A struct is a class
// instance rather than a plain object, and pointers, funcs, channels and
// complex numbers have no JSON form. There is no depth limit: a cycle is
// refused as a cycle. A *delta.Draft is read as it holds its content now, as
// pi's isJsonValue reads a proxy; a settled one is not a value.
func IsValue(value any) bool {
	return jsonvalue.Is(value, readDraft)
}

// CopyJSON is upstream's copyJson: a detached, alias-free copy of value in the
// representation a decoded tree has — nil, bool, float64, string, []any and
// map[string]any — so that the caller owns every container of it, and a
// container reachable twice in value is two containers in the copy. It
// accepts what IsValue accepts and refuses anything else with a *ValueError.
//
// A *delta.Draft — the value itself, or one inside it — is copied as it holds
// its content now, as pi's copyJson reads a proxy through its traps; a settled
// one fails with delta.ErrDraftSettled. Upstream's omitUndefinedProperties
// option has no Go form: a Go value holds no undefined.
func CopyJSON(value any) (Value, error) {
	v, err := jsonvalue.Copy(value, readDraft)
	if err != nil {
		return nil, fmt.Errorf("chord: %w", err)
	}
	return v, nil
}

// readDraft is how the strict-JSON rules read a draft: through it.
func readDraft(v any) (any, bool, error) {
	d, ok := v.(*delta.Draft)
	if !ok {
		return nil, false, nil
	}
	c, err := d.Snapshot()
	return c, true, err
}
