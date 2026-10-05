package jstext

import (
	"strings"
	"unicode/utf8"
)

// CompareUTF16 orders two strings by UTF-16 code unit, as JavaScript's < and
// Array.prototype.sort's default order do. It differs from Go's byte order
// only between an astral rune and a BMP rune above the surrogate range
// (U+E000..U+FFFF), which UTF-8 puts first and UTF-16 last.
//
// Invalid UTF-8 has no JavaScript counterpart, and each invalid byte decodes
// to U+FFFD here, so two strings that differ only there would compare equal.
// They are ordered by their bytes instead, which keeps the order total and a
// sort by it deterministic; valid UTF-8 never reaches that tie-break.
func CompareUTF16(a, b string) int {
	a0, b0 := a, b
	for a != "" && b != "" {
		ra, na := utf8.DecodeRuneInString(a)
		rb, nb := utf8.DecodeRuneInString(b)
		if ra != rb {
			ua, ub := ra, rb
			if ua >= 0x10000 {
				ua = 0xD800 + (ua-0x10000)>>10
			}
			if ub >= 0x10000 {
				ub = 0xD800 + (ub-0x10000)>>10
			}
			if ua != ub {
				return int(ua) - int(ub)
			}
			// Same high surrogate: the low surrogates order as the runes do.
			return int(ra) - int(rb)
		}
		a, b = a[na:], b[nb:]
	}
	if len(a) != len(b) {
		return len(a) - len(b)
	}
	return strings.Compare(a0, b0)
}
