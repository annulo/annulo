package cbor

import (
	"math"
	"reflect"
	"slices"
	"strings"
	"unicode/utf8"
)

type writer struct {
	buf           []byte
	maxByteLength int
}

func (w *writer) ensure(additional int) error {
	if len(w.buf)+additional > w.maxByteLength {
		return limitErrf("CBOR byte length exceeds configured limit of %d", w.maxByteLength)
	}
	return nil
}

func (w *writer) writeByte(b byte) error {
	if err := w.ensure(1); err != nil {
		return err
	}
	w.buf = append(w.buf, b)
	return nil
}

func (w *writer) writeBytes(b []byte) error {
	if err := w.ensure(len(b)); err != nil {
		return err
	}
	w.buf = append(w.buf, b...)
	return nil
}

// writeArgument emits a major type and its argument in the shortest form that
// holds it — CBOR's canonical minimal-length encoding, and the only form pi's
// decoder accepts on the wire it produces.
func (w *writer) writeArgument(majorType byte, value uint64) error {
	prefix := majorType << 5
	switch {
	case value < 24:
		return w.writeByte(prefix | byte(value))
	case value <= 0xff:
		if err := w.writeByte(prefix | 24); err != nil {
			return err
		}
		return w.writeByte(byte(value))
	case value <= 0xffff:
		if err := w.writeByte(prefix | 25); err != nil {
			return err
		}
		return w.writeBytes([]byte{byte(value >> 8), byte(value)})
	case value <= maxUint32:
		if err := w.writeByte(prefix | 26); err != nil {
			return err
		}
		return w.writeBytes([]byte{byte(value >> 24), byte(value >> 16), byte(value >> 8), byte(value)})
	default:
		if err := w.writeByte(prefix | 27); err != nil {
			return err
		}
		return w.writeBytes([]byte{
			byte(value >> 56), byte(value >> 48), byte(value >> 40), byte(value >> 32),
			byte(value >> 24), byte(value >> 16), byte(value >> 8), byte(value),
		})
	}
}

func (w *writer) writeFloat64(v float64) error {
	if err := w.writeByte(0xfb); err != nil {
		return err
	}
	bits := math.Float64bits(v)
	return w.writeBytes([]byte{
		byte(bits >> 56), byte(bits >> 48), byte(bits >> 40), byte(bits >> 32),
		byte(bits >> 24), byte(bits >> 16), byte(bits >> 8), byte(bits),
	})
}

func (w *writer) writeText(s string, opts resolved) error {
	if len(s) > opts.maxByteLength {
		return limitErrf("CBOR text string length exceeds configured limit of %d", opts.maxByteLength)
	}
	// pi round-trips through TextDecoder({fatal:true}) to reject lone
	// surrogates; the Go equivalent of "is this a valid Unicode scalar
	// sequence" is utf8.ValidString.
	if !utf8.ValidString(s) {
		return &Error{Msg: "CBOR text strings must contain valid Unicode scalar values"}
	}
	if err := w.writeArgument(3, uint64(len(s))); err != nil {
		return err
	}
	return w.writeBytes([]byte(s))
}

// visitKey identifies a container by its underlying pointer so cycles are
// detected the way pi's `ancestors` Set does.
type visitKey struct {
	ptr  uintptr
	kind reflect.Kind
}

type encoder struct {
	w    *writer
	opts resolved
	seen map[visitKey]bool
}

func (e *encoder) enter(v reflect.Value) (visitKey, bool) {
	switch v.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice:
		if v.IsNil() {
			return visitKey{}, true
		}
		k := visitKey{ptr: v.Pointer(), kind: v.Kind()}
		if e.seen[k] {
			return k, false
		}
		e.seen[k] = true
		return k, true
	default:
		return visitKey{}, true
	}
}

func (e *encoder) leave(k visitKey) {
	if k != (visitKey{}) {
		delete(e.seen, k)
	}
}

func (e *encoder) encode(v reflect.Value, depth int) error {
	if depth > e.opts.maxDepth {
		return limitErrf("CBOR nesting depth exceeds configured limit of %d", e.opts.maxDepth)
	}

	if !v.IsValid() {
		return e.w.writeByte(0xf6) // null
	}

	// An OrderedObject is a slice by construction but an object on the wire,
	// so it is recognised before the kind switch would encode it as an array.
	if v.Type() == orderedObjectType {
		key, ok := e.enter(v)
		if !ok {
			return &Error{Msg: "CBOR values must not contain cycles"}
		}
		defer e.leave(key)
		return e.encodeOrderedObject(v.Interface().(OrderedObject), depth)
	}
	// A RawItem is a byte slice by construction but already-encoded CBOR on
	// the wire, so it too is recognised before the kind switch would wrap it
	// in a byte string.
	if v.Type() == rawItemType {
		return e.encodeRawItem(v.Bytes(), depth)
	}

	switch v.Kind() {
	case reflect.Interface:
		if v.IsNil() {
			return e.w.writeByte(0xf6)
		}
		return e.encode(v.Elem(), depth)

	case reflect.Pointer:
		if v.IsNil() {
			return e.w.writeByte(0xf6)
		}
		key, ok := e.enter(v)
		if !ok {
			return &Error{Msg: "CBOR values must not contain cycles"}
		}
		defer e.leave(key)
		return e.encode(v.Elem(), depth)

	case reflect.Bool:
		if v.Bool() {
			return e.w.writeByte(0xf5)
		}
		return e.w.writeByte(0xf4)

	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n := v.Int()
		if n > maxSafeInteger || n < -maxSafeInteger {
			return &Error{Msg: "CBOR integers must be safe JavaScript integers"}
		}
		if n >= 0 {
			return e.w.writeArgument(0, uint64(n))
		}
		return e.w.writeArgument(1, uint64(-1-n))

	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		n := v.Uint()
		if n > maxSafeInteger {
			return &Error{Msg: "CBOR integers must be safe JavaScript integers"}
		}
		return e.w.writeArgument(0, n)

	case reflect.Float32, reflect.Float64:
		f := v.Float()
		if math.IsInf(f, 0) || math.IsNaN(f) {
			return &Error{Msg: "CBOR numbers must be finite"}
		}
		// pi encodes any integral number as a CBOR integer, so 1.0 and 1 are
		// the same bytes on the wire. Preserving that is what keeps a Go
		// encoding byte-identical to a Node one.
		if isIntegralFloat(f) {
			if f > maxSafeInteger || f < -maxSafeInteger {
				return &Error{Msg: "CBOR integers must be safe JavaScript integers"}
			}
			if f >= 0 {
				return e.w.writeArgument(0, uint64(f))
			}
			return e.w.writeArgument(1, uint64(-1-f))
		}
		return e.w.writeFloat64(f)

	case reflect.String:
		return e.w.writeText(v.String(), e.opts)

	case reflect.Slice:
		if v.Type().Elem().Kind() == reflect.Uint8 {
			b := v.Bytes()
			if len(b) > e.opts.maxByteLength {
				return limitErrf("CBOR byte string length exceeds configured limit of %d", e.opts.maxByteLength)
			}
			if err := e.w.writeArgument(2, uint64(len(b))); err != nil {
				return err
			}
			return e.w.writeBytes(b)
		}
		key, ok := e.enter(v)
		if !ok {
			return &Error{Msg: "CBOR values must not contain cycles"}
		}
		defer e.leave(key)
		return e.encodeArray(v, depth)

	case reflect.Array:
		return e.encodeArray(v, depth)

	case reflect.Map:
		key, ok := e.enter(v)
		if !ok {
			return &Error{Msg: "CBOR values must not contain cycles"}
		}
		defer e.leave(key)
		return e.encodeMap(v, depth)

	case reflect.Struct:
		return e.encodeStruct(v, depth)

	default:
		return cborErrf("Unsupported CBOR value type: %s", v.Kind())
	}
}

func (e *encoder) encodeArray(v reflect.Value, depth int) error {
	n := v.Len()
	if n > e.opts.maxContainerLength {
		return limitErrf("CBOR array length exceeds configured limit of %d", e.opts.maxContainerLength)
	}
	if err := e.w.writeArgument(4, uint64(n)); err != nil {
		return err
	}
	for i := range n {
		if err := e.encode(v.Index(i), depth+1); err != nil {
			return err
		}
	}
	return nil
}

func (e *encoder) encodeMap(v reflect.Value, depth int) error {
	if v.Type().Key().Kind() != reflect.String {
		return &Error{Msg: "CBOR map keys must be strings"}
	}
	keys := make([]string, 0, v.Len())
	for iter := v.MapRange(); iter.Next(); {
		// A nil entry is kept and encoded as null. Maps carry JsonValue
		// payloads, and JsonValue has null but no undefined — pi's
		// drop-undefined rule belongs to optional *struct* fields, which the
		// port models with omitempty, not to map entries.
		keys = append(keys, iter.Key().String())
	}
	// DIVERGENCE (deliberate): pi emits map entries in JS insertion order; Go
	// map iteration is randomized, so entries are sorted by key to make the
	// encoding deterministic. CBOR maps are order-independent and pi's decoder
	// accepts any order, so this is interop-safe. Struct encoding below keeps
	// declaration order, which is what the protocol's own messages use, and a
	// value whose authored order is known travels as an OrderedObject.
	slices.Sort(keys)

	if len(keys) > e.opts.maxContainerLength {
		return limitErrf("CBOR map length exceeds configured limit of %d", e.opts.maxContainerLength)
	}
	if err := e.w.writeArgument(5, uint64(len(keys))); err != nil {
		return err
	}
	for _, k := range keys {
		if err := e.w.writeText(k, e.opts); err != nil {
			return err
		}
		if err := e.encode(v.MapIndex(reflect.ValueOf(k).Convert(v.Type().Key())), depth+1); err != nil {
			return err
		}
	}
	return nil
}

type structField struct {
	name      string
	index     []int
	omitEmpty bool
}

func (e *encoder) encodeStruct(v reflect.Value, depth int) error {
	fields, err := cachedStructFields(v.Type())
	if err != nil {
		return err
	}
	type entry struct {
		name string
		val  reflect.Value
	}
	entries := make([]entry, 0, len(fields))
	for _, f := range fields {
		fv := v.FieldByIndex(f.index)
		if f.omitEmpty && isOmitted(fv) {
			continue
		}
		entries = append(entries, entry{name: f.name, val: fv})
	}
	if len(entries) > e.opts.maxContainerLength {
		return limitErrf("CBOR map length exceeds configured limit of %d", e.opts.maxContainerLength)
	}
	if err := e.w.writeArgument(5, uint64(len(entries))); err != nil {
		return err
	}
	for _, en := range entries {
		if err := e.w.writeText(en.name, e.opts); err != nil {
			return err
		}
		if err := e.encode(en.val, depth+1); err != nil {
			return err
		}
	}
	return nil
}

// isOmitted reports whether a field tagged omitempty should be skipped. This is
// pi's "drop undefined properties" rule, so it is deliberately narrower than
// encoding/json's: only nil-able absence counts, never a zero number, empty
// string, or false — those are real protocol values.
func isOmitted(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Map, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}

// structLayout is a struct type's wire field list, or the reason the type has
// no legal encoding.
type structLayout struct {
	fields []structField
	err    error
}

var structFieldCache = newTypeCache()

func cachedStructFields(t reflect.Type) ([]structField, error) {
	if cached, ok := structFieldCache.load(t); ok {
		return cached.fields, cached.err
	}
	layout := buildStructLayout(t)
	structFieldCache.store(t, layout)
	return layout.fields, layout.err
}

// buildStructLayout resolves a struct's cbor tags, refusing any field set whose
// encoding this package would not accept back. Encode is exported, so the check
// runs once per type here rather than being left to whoever first notices that a
// property went missing on the wire.
func buildStructLayout(t reflect.Type) structLayout {
	fields := make([]structField, 0, t.NumField())
	declaredBy := make(map[string]string, t.NumField())
	for i := range t.NumField() {
		sf := t.Field(i)
		tag := sf.Tag.Get("cbor")
		if tag == "-" {
			continue
		}
		name, rest, _ := strings.Cut(tag, ",")

		// DIVERGENCE (deliberate): encoding/json promotes an embedded struct's
		// fields into the enclosing object. pi has no embedding to be faithful
		// to and no protocol message embeds anything, so rather than port
		// encoding/json's promotion and shadowing rules this encoder refuses an
		// untagged embedded struct. What it must not do is what it did before:
		// drop the embedded fields and encode a message that is quietly missing
		// properties. A tagged embedded field is a plain named field, which is
		// also how encoding/json reads a tag on an anonymous field.
		if sf.Anonymous && name == "" && structLike(sf.Type) {
			return structLayout{err: cborErrf(
				"CBOR cannot encode %s: it embeds %s, and embedded structs are not flattened; "+
					"give the field a name with a cbor tag, declare its fields on %s directly, or tag it cbor:\"-\"",
				t, sf.Type, t)}
		}
		// An unexported field never reaches the wire, as in encoding/json. The
		// exception is the escape the error above offers: a cbor name turns an
		// embedded field into an ordinary named one even when its type is
		// unexported, and such a value is still readable through reflection.
		if !sf.IsExported() && !(sf.Anonymous && name != "") {
			continue
		}
		if name == "" {
			name = sf.Name
		}
		// A duplicate wire name encodes a map with a repeated key, which this
		// package's own Decode rejects — so the value would be unreadable by
		// every peer, including us.
		if previous, duplicate := declaredBy[name]; duplicate {
			return structLayout{err: cborErrf(
				"CBOR cannot encode %s: fields %s and %s both encode to the property %q; "+
					"rename one with a cbor tag or tag it cbor:\"-\"",
				t, previous, sf.Name, name)}
		}
		declaredBy[name] = sf.Name
		fields = append(fields, structField{
			name:      name,
			index:     sf.Index,
			omitEmpty: slices.Contains(strings.Split(rest, ","), "omitempty"),
		})
	}
	return structLayout{fields: fields}
}

func structLike(t reflect.Type) bool {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t.Kind() == reflect.Struct
}

// Encode writes value as the protocol's strict, definite-length RFC 8949
// subset. Struct fields are emitted in declaration order (matching the field
// order of pi's message interfaces); map entries are emitted in sorted key
// order, and an OrderedObject's entries in its own.
func Encode(value any, opts *Options) ([]byte, error) {
	r, err := resolveOptions(opts)
	if err != nil {
		return nil, err
	}
	e := &encoder{
		w:    &writer{maxByteLength: r.maxByteLength, buf: make([]byte, 0, 256)},
		opts: r,
		seen: map[visitKey]bool{},
	}
	if err := e.encode(reflect.ValueOf(value), 0); err != nil {
		return nil, err
	}
	return e.w.buf, nil
}
