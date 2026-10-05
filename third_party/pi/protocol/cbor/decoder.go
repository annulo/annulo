package cbor

import (
	"encoding/binary"
	"math"
	"unicode/utf8"
)

type reader struct {
	bytes  []byte
	offset int
	opts   resolved
	// rawKeys names the top-level map entries whose values are captured as
	// RawItem spans rather than decoded (see DecodeRaw).
	rawKeys []string
}

func (r *reader) readByte() (byte, error) {
	if r.offset >= len(r.bytes) {
		return 0, &Error{Msg: "Truncated CBOR payload"}
	}
	b := r.bytes[r.offset]
	r.offset++
	return b, nil
}

func (r *reader) readBytes(length int) ([]byte, error) {
	if length > len(r.bytes)-r.offset {
		return nil, &Error{Msg: "Truncated CBOR payload"}
	}
	b := r.bytes[r.offset : r.offset+length]
	r.offset += length
	return b, nil
}

// readArgument decodes a major type's argument. pi caps 8-byte arguments at the
// safe-integer range rather than the full uint64 range, so an oversized length
// is rejected before it can be used to size an allocation.
func (r *reader) readArgument(additional byte) (uint64, error) {
	if additional < 24 {
		return uint64(additional), nil
	}
	switch additional {
	case 24:
		b, err := r.readByte()
		return uint64(b), err
	case 25:
		b, err := r.readBytes(2)
		if err != nil {
			return 0, err
		}
		return uint64(binary.BigEndian.Uint16(b)), nil
	case 26:
		b, err := r.readBytes(4)
		if err != nil {
			return 0, err
		}
		return uint64(binary.BigEndian.Uint32(b)), nil
	case 27:
		b, err := r.readBytes(8)
		if err != nil {
			return 0, err
		}
		v := binary.BigEndian.Uint64(b)
		if v>>32 > 0x1f_ffff {
			return 0, &Error{Msg: "Decoded CBOR integer or length is outside the safe range"}
		}
		return v, nil
	case 31:
		return 0, &Error{Msg: "Indefinite-length CBOR items are not supported"}
	default:
		return 0, &Error{Msg: "Malformed CBOR additional information"}
	}
}

func (r *reader) readLength(additional byte, kind string, limit int) (int, error) {
	if additional == 31 {
		return 0, cborErrf("Indefinite-length CBOR %ss are not supported", kind)
	}
	n, err := r.readArgument(additional)
	if err != nil {
		return 0, err
	}
	if n > uint64(limit) {
		return 0, limitErrf("CBOR %s length exceeds configured limit of %d", kind, limit)
	}
	return int(n), nil
}

func (r *reader) readItem(depth int) (any, error) {
	if depth > r.opts.maxDepth {
		return nil, limitErrf("CBOR nesting depth exceeds configured limit of %d", r.opts.maxDepth)
	}
	initial, err := r.readByte()
	if err != nil {
		return nil, err
	}
	majorType := initial >> 5
	additional := initial & 0x1f

	switch majorType {
	case 0:
		n, err := r.readArgument(additional)
		if err != nil {
			return nil, err
		}
		if n > maxSafeInteger {
			return nil, &Error{Msg: "Decoded CBOR integer is outside the safe range"}
		}
		return int64(n), nil

	case 1:
		n, err := r.readArgument(additional)
		if err != nil {
			return nil, err
		}
		// pi computes -1 - n and then tests Number.isSafeInteger, which is
		// false for -2^53 — so the largest legal argument is 2^53-2, not
		// 2^53-1. Bounding the argument with > would admit a value Encode
		// then refuses to write back.
		if n >= maxSafeInteger {
			return nil, &Error{Msg: "Decoded CBOR integer is outside the safe range"}
		}
		return -1 - int64(n), nil

	case 2:
		length, err := r.readLength(additional, "byte string", r.opts.maxByteLength)
		if err != nil {
			return nil, err
		}
		b, err := r.readBytes(length)
		if err != nil {
			return nil, err
		}
		// Copy: the caller must not alias the frame buffer, which the
		// transport reuses.
		out := make([]byte, len(b))
		copy(out, b)
		return out, nil

	case 3:
		length, err := r.readLength(additional, "text string", r.opts.maxByteLength)
		if err != nil {
			return nil, err
		}
		b, err := r.readBytes(length)
		if err != nil {
			return nil, err
		}
		if !utf8.Valid(b) {
			return nil, &Error{Msg: "CBOR text string contains invalid UTF-8"}
		}
		return string(b), nil

	case 4:
		length, err := r.readLength(additional, "array", r.opts.maxContainerLength)
		if err != nil {
			return nil, err
		}
		result := make([]any, 0, min(length, 1024))
		for range length {
			item, err := r.readItem(depth + 1)
			if err != nil {
				return nil, err
			}
			result = append(result, item)
		}
		return result, nil

	case 5:
		length, err := r.readLength(additional, "map", r.opts.maxContainerLength)
		if err != nil {
			return nil, err
		}
		result := make(map[string]any, min(length, 1024))
		for range length {
			rawKey, err := r.readItem(depth + 1)
			if err != nil {
				return nil, err
			}
			key, ok := rawKey.(string)
			if !ok {
				return nil, &Error{Msg: "CBOR map keys must be strings"}
			}
			if _, duplicate := result[key]; duplicate {
				return nil, &Error{Msg: "CBOR map contains a duplicate key"}
			}
			var value any
			if r.captures(key, depth) {
				value, err = r.readRawItem(depth + 1)
			} else {
				value, err = r.readItem(depth + 1)
			}
			if err != nil {
				return nil, err
			}
			result[key] = value
		}
		return result, nil

	case 6:
		return nil, &Error{Msg: "CBOR tags are not supported"}

	case 7:
		return r.readSimple(additional)

	default:
		return nil, &Error{Msg: "Malformed CBOR major type"}
	}
}

func (r *reader) readSimple(additional byte) (any, error) {
	switch additional {
	case 20:
		return false, nil
	case 21:
		return true, nil
	case 22:
		return nil, nil
	case 27:
		b, err := r.readBytes(8)
		if err != nil {
			return nil, err
		}
		v := math.Float64frombits(binary.BigEndian.Uint64(b))
		if math.IsInf(v, 0) || math.IsNaN(v) {
			return nil, &Error{Msg: "Decoded CBOR number must be finite"}
		}
		if isIntegralFloat(v) && (v > maxSafeInteger || v < -maxSafeInteger) {
			return nil, &Error{Msg: "Decoded CBOR integer is outside the safe range"}
		}
		return v, nil
	case 31:
		return nil, &Error{Msg: "CBOR break marker is not supported"}
	default:
		return nil, &Error{Msg: "Unsupported CBOR simple value or floating-point width"}
	}
}

// Decode reads exactly one item from the protocol's strict RFC 8949 subset and
// rejects any trailing bytes.
//
// Numbers come back as int64 for CBOR integers and float64 for CBOR floats.
// pi has only one number type and returns float64 for both; splitting them is
// lossless in the other direction, because Encode writes any integral float as
// a CBOR integer.
func Decode(b []byte, opts *Options) (any, error) {
	return decode(b, opts, nil)
}

func decode(b []byte, opts *Options, rawKeys []string) (any, error) {
	r, err := resolveOptions(opts)
	if err != nil {
		return nil, err
	}
	if len(b) > r.maxByteLength {
		return nil, limitErrf("CBOR byte length exceeds configured limit of %d", r.maxByteLength)
	}
	reader := &reader{bytes: b, opts: r, rawKeys: rawKeys}
	value, err := reader.readItem(0)
	if err != nil {
		return nil, err
	}
	if reader.offset != len(reader.bytes) {
		return nil, &Error{Msg: "CBOR payload contains trailing data"}
	}
	return value, nil
}
