package server

import (
	"crypto/rand"
	"encoding/hex"
)

// newUUID returns a random RFC 4122 version 4 UUID in the canonical
// hyphenated, lowercase form.
//
// DIVERGENCE (deliberate): pi calls node:crypto randomUUID. Go's standard
// library has no UUID, and the identifiers this produces are never parsed —
// they are opaque connection and session IDs whose only requirements are
// collision resistance and being a non-empty string on the wire. Sixteen bytes
// from crypto/rand formatted the same way keeps them indistinguishable from a
// Node peer's, without taking a dependency for it.
func newUUID() string {
	var b [16]byte
	// crypto/rand.Read is documented never to return an error since Go 1.24.
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // RFC 4122 variant

	var out [36]byte
	hex.Encode(out[0:8], b[0:4])
	out[8] = '-'
	hex.Encode(out[9:13], b[4:6])
	out[13] = '-'
	hex.Encode(out[14:18], b[6:8])
	out[18] = '-'
	hex.Encode(out[19:23], b[8:10])
	out[23] = '-'
	hex.Encode(out[24:36], b[10:16])
	return string(out[:])
}
