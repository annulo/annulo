package jstext

import (
	"strings"
	"unicode/utf8"
)

// DecodeUTF8 is what a TextDecoder's decode makes of b, less its byte-order
// mark handling: the WHATWG Encoding Standard's UTF-8 decoder in replacement
// mode. Valid UTF-8 comes through as is. An invalid sequence becomes one
// U+FFFD per maximal subpart, the longest prefix of it that some valid
// sequence starts with, so a truncated three-byte character is one U+FFFD and
// an overlong or surrogate encoding is one per byte. Go's decoders replace
// each invalid byte on its own (encoding/json, utf8.DecodeRune) or each run
// of them (strings.ToValidUTF8) instead.
func DecodeUTF8(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	var out strings.Builder
	out.Grow(len(b) + 8)
	for i := 0; i < len(b); {
		lead := b[i]
		if lead < 0x80 {
			out.WriteByte(lead)
			i++
			continue
		}
		needed, lower, upper := UTF8Lead(lead)
		if needed == 0 {
			out.WriteRune(utf8.RuneError)
			i++
			continue
		}
		end := i + 1
		for end <= i+needed && end < len(b) && b[end] >= lower && b[end] <= upper {
			lower, upper = 0x80, 0xBF
			end++
		}
		if end == i+needed+1 {
			out.Write(b[i:end])
		} else {
			// The maximal subpart b[i:end] ends at a byte that cannot
			// continue it, or at the end of b; that byte starts afresh.
			out.WriteRune(utf8.RuneError)
		}
		i = end
	}
	return out.String()
}

// UTF8Lead is how the Encoding Standard's UTF-8 decoder reads a byte of 0x80
// or more as the start of a sequence: needed is how many continuation bytes
// the sequence takes, and lower and upper bound the first of them, which is
// where overlong forms, surrogates and code points past U+10FFFF are ruled
// out; each later one is 0x80 to 0xBF. needed is 0 for a byte no sequence
// starts with (a continuation byte, 0xC0, 0xC1, 0xF5 and up), and for an
// ASCII byte, which is a character on its own.
func UTF8Lead(lead byte) (needed int, lower, upper byte) {
	lower, upper = 0x80, 0xBF
	switch {
	case lead >= 0xC2 && lead <= 0xDF:
		needed = 1
	case lead >= 0xE0 && lead <= 0xEF:
		needed = 2
		if lead == 0xE0 {
			lower = 0xA0
		} else if lead == 0xED {
			upper = 0x9F
		}
	case lead >= 0xF0 && lead <= 0xF4:
		needed = 3
		if lead == 0xF0 {
			lower = 0x90
		} else if lead == 0xF4 {
			upper = 0x8F
		}
	}
	return needed, lower, upper
}
