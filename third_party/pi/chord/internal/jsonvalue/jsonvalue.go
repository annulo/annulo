// Package jsonvalue is chord's strict-JSON rules — upstream's src/json.ts,
// copyJson and isJsonValue — shared by package chord, which exports them, and
// chord/delta, whose drafts copy every value placed into them with them. It
// is a package of its own because chord imports delta.
//
// A value is strict JSON in its Go spelling: nil, a bool, a finite number of
// any Go numeric kind or a json.Number, a string, a slice or array of anything
// but bytes (a []byte is a binary blob, the way a Uint8Array is upstream, and
// encoding/json writes it as a base64 string), or a map with a string-kind
// key. A struct is a class instance, not a plain object, and pointers, funcs,
// channels and complex numbers have no JSON form. No container may reach
// itself.
package jsonvalue

import (
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"reflect"
	"slices"
	"strings"
)

// Upstream's copyJson TypeError texts.
const (
	NonFinite = "Value contains a non-finite number and is not strict JSON"
	Cycle     = "Value contains cycles and is not strict JSON"
	Plain     = "Value must contain strict JSON plain objects or arrays"
	// NonJSON is a template: upstream fills in typeof — "function" for a
	// func, which is the one Go kind JavaScript has — and the port its Go
	// type for the kinds JavaScript has no typeof for.
	NonJSON = "Value contains a non-JSON %s; expected strict JSON"
)

// Error reports a value that is not strict JSON. Message is upstream's
// TypeError text; Value the offending Go value, nil for a cycle.
type Error struct {
	Message string
	Value   any
}

func (e *Error) Error() string {
	switch e.Message {
	case Cycle:
		return e.Message + " (a container reaches itself; break the cycle before handing the value over)"
	case NonFinite:
		got := "NaN"
		if n, ok := e.Value.(json.Number); ok {
			got = string(n)
		} else if f, ok := number(reflect.ValueOf(e.Value)); ok && !math.IsNaN(f) {
			got = fmt.Sprint(f)
		}
		return fmt.Sprintf("%s: got %s (a JSON number is finite)", e.Message, got)
	}
	return fmt.Sprintf("%s: got %T (build values from nil, bool, string, finite numbers, slices and string-keyed maps, as encoding/json decodes them)", e.Message, e.Value)
}

// Hook lets a caller handle a value the rules do not know: handled reports
// that it did. chord/delta copies a draft with it, as it holds its content
// now — what upstream's copy reads through the draft's traps.
type Hook func(v any) (copied any, handled bool, err error)

// Copy is copyJson: a detached, alias-free copy of v in the representation a
// decoded tree has — nil, bool, float64, string, []any and map[string]any —
// with every container reachable twice copied twice, or an *Error. Every
// array it allocates has a backing array of its own (capacity at least one),
// so that each copy is a container of its own identity. hook, which may be
// nil, sees every value before the rules do. Of several bad members of an
// object, the one reported is the first in key order.
func Copy(v any, hook Hook) (any, error) {
	w := walker{hook: hook, copy: true}
	return w.walk(v)
}

// Is is isJsonValue: whether v is strict JSON, without copying or normalizing
// it — and, for a tree encoding/json decoded, without allocating. hook is as
// for Copy.
func Is(v any, hook Hook) bool {
	w := walker{hook: hook}
	_, err := w.walk(v)
	return err == nil
}

// cycleDepth is how deep the walk goes before it tracks the containers on its
// path, as encoding/json does: a value shallower than that cannot reach
// itself, and one deeper is very deep or a cycle, which the tracking then
// finds. There is no depth limit.
const cycleDepth = 1_000

// identity is a container on the current path: a map's header or a slice's
// backing array together with its length (a shorter view of the same array is
// a different array, and a descendant equal to an ancestor in both is a
// cycle).
type identity struct {
	ptr uintptr
	len int
}

type walker struct {
	hook      Hook
	copy      bool
	depth     int
	ancestors map[identity]bool
}

func (w *walker) walk(v any) (any, error) {
	if w.hook != nil {
		if c, ok, err := w.hook(v); ok || err != nil {
			return c, err
		}
	}
	switch x := v.(type) {
	case nil, bool, string:
		return v, nil
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return nil, &Error{Message: NonFinite, Value: v}
		}
		return v, nil
	case json.Number:
		f, err := x.Float64()
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, &Error{Message: NonFinite, Value: v}
		}
		if !w.copy {
			return nil, nil
		}
		return f, nil
	case []any:
		return w.slice(v, x)
	case map[string]any:
		return w.object(v, x)
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Bool:
		return rv.Bool(), nil
	case reflect.String:
		return rv.String(), nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64:
		f, _ := number(rv)
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, &Error{Message: NonFinite, Value: v}
		}
		return f, nil
	case reflect.Slice, reflect.Array:
		if rv.Type().Elem().Kind() == reflect.Uint8 {
			return nil, &Error{Message: Plain, Value: v}
		}
		items := make([]any, rv.Len())
		for i := range items {
			items[i] = rv.Index(i).Interface()
		}
		return w.slice(v, items)
	case reflect.Map:
		if rv.Type().Key().Kind() != reflect.String {
			return nil, &Error{Message: Plain, Value: v}
		}
		m := make(map[string]any, rv.Len())
		for it := rv.MapRange(); it.Next(); {
			m[it.Key().String()] = it.Value().Interface()
		}
		return w.object(v, m)
	case reflect.Struct, reflect.Pointer:
		return nil, &Error{Message: Plain, Value: v}
	case reflect.Func:
		return nil, &Error{Message: fmt.Sprintf(NonJSON, "function"), Value: v}
	}
	return nil, &Error{Message: fmt.Sprintf(NonJSON, rv.Type()), Value: v}
}

func number(rv reflect.Value) (float64, bool) {
	switch rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return float64(rv.Int()), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return float64(rv.Uint()), true
	case reflect.Float32, reflect.Float64:
		return rv.Float(), true
	}
	return math.NaN(), false
}

// enter steps one level down into container v, refusing it once the walk is
// past cycleDepth and v is its own ancestor. The identity it returns, when
// tracked, is what leave takes off the path again.
func (w *walker) enter(v any) (identity, bool, error) {
	w.depth++
	if w.depth <= cycleDepth {
		return identity{}, false, nil
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Array {
		return identity{}, false, nil
	}
	ptr := rv.Pointer()
	if ptr == 0 {
		return identity{}, false, nil
	}
	id := identity{ptr: ptr, len: -1}
	if rv.Kind() == reflect.Slice {
		id.len = rv.Len()
	}
	if w.ancestors[id] {
		w.depth--
		return identity{}, false, &Error{Message: Cycle}
	}
	if w.ancestors == nil {
		w.ancestors = map[identity]bool{}
	}
	w.ancestors[id] = true
	return id, true, nil
}

// leave steps back up from a container enter stepped into.
func (w *walker) leave(id identity, tracked bool) {
	if tracked {
		delete(w.ancestors, id)
	}
	w.depth--
}

// slice walks the elements of v, items.
func (w *walker) slice(v any, items []any) (any, error) {
	id, tracked, err := w.enter(v)
	if err != nil {
		return nil, err
	}
	defer w.leave(id, tracked)
	var out []any
	if w.copy {
		out = make([]any, len(items), max(len(items), 1))
	}
	for i, item := range items {
		c, err := w.walk(item)
		if err != nil {
			return nil, err
		}
		if w.copy {
			out[i] = c
		}
	}
	if !w.copy {
		return nil, nil
	}
	return out, nil
}

// object walks the members of v, m, in whatever order the map yields them —
// unless one is bad and the walk is a copy, which reports the first bad member
// in key order, so that of several the one reported is always the same.
func (w *walker) object(v any, m map[string]any) (any, error) {
	id, tracked, err := w.enter(v)
	if err != nil {
		return nil, err
	}
	defer w.leave(id, tracked)
	var out map[string]any
	if w.copy {
		out = make(map[string]any, len(m))
	}
	for k, item := range m {
		c, err := w.walk(item)
		if err != nil {
			if w.copy {
				return nil, w.firstError(m)
			}
			return nil, err
		}
		if w.copy {
			out[k] = c
		}
	}
	if !w.copy {
		return nil, nil
	}
	return out, nil
}

// firstError re-walks an object's members in key order, without copying, for
// the first error.
func (w *walker) firstError(m map[string]any) error {
	w.copy = false
	defer func() { w.copy = true }()
	for _, k := range slices.SortedFunc(maps.Keys(m), strings.Compare) {
		if _, err := w.walk(m[k]); err != nil {
			return err
		}
	}
	return nil
}
