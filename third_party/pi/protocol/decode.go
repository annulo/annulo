package protocol

import "github.com/sky-valley/pi/internal/jsonstrict"

// The strict tree decoder itself lives in internal/jsonstrict; this file binds
// it to the protocol's CBOR field tags and to its union types. Strict means
// what TypeBox's additionalProperties:false means: an unknown property is a
// rejection, not something to ignore — see the package doc there.

// ValidationError is a protocol message that failed structural or constraint
// checking (pi's ProtocolValidationError).
type ValidationError = jsonstrict.Error

// Validator is implemented by message types carrying constraints the struct
// shape alone cannot express — TypeBox's minLength, minimum, and literals.
type Validator = jsonstrict.Validator

func invalidf(format string, a ...any) *ValidationError {
	return jsonstrict.Errorf(format, a...)
}

// decoder reads the protocol's `cbor` field tags and names the top-level value
// "message" in errors, the way pi's ProtocolValidationError does.
var decoder = &jsonstrict.Decoder{Tag: "cbor", Root: "message"}

func registerUnion[T any](decode func(any) (T, error)) {
	jsonstrict.RegisterUnion(decoder, decode)
}

// decodeInto strictly decodes a CBOR-decoded value into target, which must be a
// non-nil pointer.
func decodeInto(value any, target any) error {
	return decoder.Decode(value, target)
}

// decodeMember decodes value into a fresh T and returns it, for union arms.
func decodeMember[T any](value any) (T, error) {
	return jsonstrict.DecodeMember[T](decoder, value)
}
