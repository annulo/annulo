package jstext

import (
	"strings"
	"unicode/utf8"

	"golang.org/x/text/cases"
	"golang.org/x/text/language"
)

// ToLower is String.prototype.toLowerCase: the full Unicode default lowercase
// mapping, SpecialCasing included, with no locale tailoring.
//
// strings.ToLower is not: it applies the simple one-to-one mapping, so the two
// disagree where a character lowers to more than one (U+0130 lowers to "i" +
// U+0307 here and to a bare "i" there) and on a capital sigma that ends a word
// (Final_Sigma lowers it to U+03C2 here, U+03C3 there). Wherever pi compares
// names by their toLowerCase, a fold through strings.ToLower matches names pi
// keeps apart.
//
// golang.org/x/text/cases implements the mapping, from the Unicode tables the
// toolchain selects: 15.0.0 before go1.27 and 17.0.0 from it, where node 26
// carries 17.0. A character assigned after the tables lowers to itself.
// testdata/lower-node.json is node's answer for every scalar value and pins it.
func ToLower(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			// A Caser keeps state between calls, so each call builds its own.
			return cases.Lower(language.Und).String(s)
		}
	}
	// On ASCII the two mappings agree, and this one does not allocate a Caser.
	return strings.ToLower(s)
}
