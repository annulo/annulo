package protocol

import (
	"reflect"

	"github.com/sky-valley/pi/protocol/cbor"
)

// Constraint helpers mirroring the TypeBox modifiers pi's schemas use:
// IdSchema is String({minLength: 1}), TimestampSchema is Integer({minimum: 0}),
// and so on. Keeping them as named helpers means a schema change upstream maps
// to one call site here rather than an open-coded comparison.

// nilMember rejects a typed nil union member instead of dereferencing it.
//
// A typed nil satisfies a union interface, so ItemFinishedProgress{Item:
// (*AssistantTranscriptItem)(nil)} type-checks and then panics inside Validate.
// The decoder always allocates, so this is unreachable from the wire and only
// ever fires on a hand-built message — the one caller that needs to be told
// which member is empty rather than handed a nil dereference.
func nilMember(member any) error {
	return invalidf("%T must not be a nil pointer; build the value before validating or encoding it", member)
}

func requireNonEmpty(name, value string) error {
	if value == "" {
		return invalidf("%s must not be empty", name)
	}
	return nil
}

// requireID is String({minLength: 1}) — pi's IdSchema.
func requireID(name, value string) error { return requireNonEmpty(name, value) }

func requireNonNegative(name string, value int64) error {
	if value < 0 {
		return invalidf("%s must be greater than or equal to 0", name)
	}
	return nil
}

func requirePositive(name string, value int64) error {
	if value < 1 {
		return invalidf("%s must be greater than or equal to 1", name)
	}
	return nil
}

func requireNonNegativeFloat(name string, value float64) error {
	if !(value >= 0) { // also rejects NaN
		return invalidf("%s must be greater than or equal to 0", name)
	}
	return nil
}

// requireTimestamp is TimestampSchema: Integer({minimum: 0}).
func requireTimestamp(name string, value int64) error { return requireNonNegative(name, value) }

func requireLiteral(name, got, want string) error {
	if got != want {
		return invalidf("%s must be %q, got %q", name, want, got)
	}
	return nil
}

// maxJSONValueDepth bounds recursion through a JsonValue. The CBOR layer
// already caps nesting, but a JsonValue can also be built in-process before
// encoding, and this keeps validation from recursing without bound on a
// structure that contains a cycle.
const maxJSONValueDepth = 64

// requireJSONValue checks that value is one of pi's JsonValue shapes.
//
// pi's isProtocolValue accepts null, boolean, number, string, arrays, and
// objects whose prototype is Object.prototype — which means a Uint8Array is
// NOT a legal JsonValue. []byte is rejected here for the same reason: pi would
// refuse to encode it, so accepting it in Go would let a Go peer produce a
// message no Node peer would have sent.
func requireJSONValue(name string, value any) error {
	return checkJSONValue(name, value, 0)
}

func checkJSONValue(name string, value any, depth int) error {
	if depth > maxJSONValueDepth {
		return invalidf("%s is nested too deeply", name)
	}
	switch v := value.(type) {
	case nil, bool, string:
		return nil
	case []byte:
		// pi's isProtocolValue refuses a Uint8Array, so a Go peer must not be
		// able to send one either. Checked before the reflect walk, which
		// would otherwise accept it as a slice.
		return invalidf("%s must be a JSON value", name)
	case []any:
		for _, item := range v {
			if err := checkJSONValue(name, item, depth+1); err != nil {
				return err
			}
		}
		return nil
	case map[string]any:
		for _, item := range v {
			if err := checkJSONValue(name, item, depth+1); err != nil {
				return err
			}
		}
		return nil
	case cbor.OrderedObject:
		// An object whose authored key order is being carried to the wire. It
		// is a JS object in pi, so it is a JsonValue here — checked before the
		// reflect walk, which would otherwise read it as a slice of structs.
		for _, field := range v {
			if err := checkJSONValue(name, field.Value, depth+1); err != nil {
				return err
			}
		}
		return nil
	}

	// pi tests `typeof value === "number"`, which every Go numeric kind spells.
	// Matching only int64/float64 would reject the natural literal — Input: 5
	// encodes fine but failed validation — so the check is by kind, mirroring
	// what the encoder already accepts.
	rv := reflect.ValueOf(value)
	switch rv.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return nil
	case reflect.Slice, reflect.Array:
		for i := range rv.Len() {
			if err := checkJSONValue(name, rv.Index(i).Interface(), depth+1); err != nil {
				return err
			}
		}
		return nil
	case reflect.Map:
		if rv.Type().Key().Kind() != reflect.String {
			return invalidf("%s must be a JSON value", name)
		}
		for _, key := range rv.MapKeys() {
			if err := checkJSONValue(name, rv.MapIndex(key).Interface(), depth+1); err != nil {
				return err
			}
		}
		return nil
	default:
		return invalidf("%s must be a JSON value", name)
	}
}
