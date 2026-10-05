package cbor

import (
	"bytes"
	"reflect"
	"slices"
)

var rawItemType = reflect.TypeOf(RawItem(nil))

// RawItem holds the exact wire bytes of one CBOR data item.
//
// pi's envelopes carry opaque JsonValue payloads (`call`, `result`, `update`)
// that the protocol layer relays without interpreting. A Node peer preserves
// those bytes through decode→re-encode for free, because a JS object keeps
// insertion order; a Go map keeps no order, and this package's encoder sorts
// map keys. Rather than an order-preserving container the decoder does not
// produce, an opaque payload travels as the bytes it arrived in: DecodeRaw
// captures the span of a designated top-level entry, and Encode writes a
// RawItem back unchanged. Relay is byte-exact by construction.
//
// Decode a RawItem with Decode (it is one complete item, so it decodes on its
// own); a nil RawItem is dropped by an omitempty field and rejected anywhere
// else.
type RawItem []byte

// DecodeRaw is Decode, except that when the top-level item is a map, the value
// of every entry whose key is in rawKeys comes back as a RawItem — the item's
// exact bytes, copied out of b — instead of a decoded value. The captured item
// is still read under the same rules and limits as the rest of the payload, so
// a span DecodeRaw returns is always one this package accepts. Capture is by
// top-level key only; a same-named key nested anywhere else decodes normally.
func DecodeRaw(b []byte, opts *Options, rawKeys ...string) (any, error) {
	return decode(b, opts, rawKeys)
}

// readRawItem reads one item exactly as readItem does and returns its span.
func (r *reader) readRawItem(depth int) (RawItem, error) {
	start := r.offset
	if _, err := r.readItem(depth); err != nil {
		return nil, err
	}
	// Copy: the transport reuses its frame buffer, the same reason a decoded
	// byte string never aliases it.
	return RawItem(bytes.Clone(r.bytes[start:r.offset])), nil
}

func (r *reader) captures(key string, depth int) bool {
	return depth == 0 && slices.Contains(r.rawKeys, key)
}

// encodeRawItem writes a RawItem verbatim. It is the one path by which Encode
// could produce a frame the decoder refuses — and a peer's message decoder
// fails permanently on its first bad frame — so the bytes are checked to be
// exactly one item readable under the encoder's own limits at this depth
// before they go out. The zero value is the realistic mistake: a required
// RawItem field left unset would otherwise vanish from the wire and shift
// every byte after it.
func (e *encoder) encodeRawItem(item RawItem, depth int) error {
	if len(item) == 0 {
		return &Error{Msg: "RawItem must not be empty; hold one encoded CBOR item in it, " +
			`or tag an optional field cbor:"...,omitempty" to drop it`}
	}
	check := &reader{bytes: item, opts: e.opts}
	if _, err := check.readItem(depth); err != nil {
		if ce, ok := err.(*Error); ok && ce.limit {
			// Too big, not malformed: pi's encoder, encoding the value these
			// bytes hold, refuses it in the same words.
			return err
		}
		return &Error{Msg: "RawItem must hold exactly one readable CBOR item: " + err.Error()}
	}
	if check.offset != len(item) {
		return &Error{Msg: "RawItem must hold exactly one readable CBOR item: CBOR payload contains trailing data"}
	}
	return e.w.writeBytes(item)
}
