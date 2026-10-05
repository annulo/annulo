package cbor

import "reflect"

var orderedObjectType = reflect.TypeOf(OrderedObject(nil))

// OrderedField is one key/value pair of an OrderedObject.
type OrderedField struct {
	Key   string
	Value any
}

// OrderedObject encodes as a CBOR map whose entries keep slice order.
//
// pi holds a JsonValue object in a JS object and its encoder walks
// Object.keys, so the wire order of a tool call's `input` is the order the
// model authored it. A Go map has no order at all, so this package sorts map
// keys to stay deterministic — deterministic, but not the order a Node peer
// would have written. Where the authored order is known and observable, the
// value travels as an OrderedObject instead and reaches the wire unchanged.
type OrderedObject []OrderedField

func (e *encoder) encodeOrderedObject(o OrderedObject, depth int) error {
	if len(o) > e.opts.maxContainerLength {
		return limitErrf("CBOR map length exceeds configured limit of %d", e.opts.maxContainerLength)
	}
	// A repeated key encodes a map this package's own Decode rejects, so it
	// would be unreadable by every peer including us — the same rule
	// buildStructLayout applies to two fields sharing a wire name.
	seen := make(map[string]struct{}, len(o))
	for _, f := range o {
		if _, duplicate := seen[f.Key]; duplicate {
			return cborErrf("CBOR maps must not repeat a key; %q appears twice", f.Key)
		}
		seen[f.Key] = struct{}{}
	}
	if err := e.w.writeArgument(5, uint64(len(o))); err != nil {
		return err
	}
	for _, f := range o {
		if err := e.w.writeText(f.Key, e.opts); err != nil {
			return err
		}
		if err := e.encode(reflect.ValueOf(f.Value), depth+1); err != nil {
			return err
		}
	}
	return nil
}
