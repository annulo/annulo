package coding

import "strings"

// utf8BOM is the decoded UTF-8 byte order mark (U+FEFF). Text read off disk
// keeps it as an ordinary leading rune — Go's os.ReadFile does not strip it,
// and neither does Node's readFile("utf-8") — so every reader that cares has
// to peel it off itself.
const utf8BOM = "\ufeff"

// splitBOM splits a leading UTF-8 BOM off decoded text, returning the BOM
// (empty when absent) and the remaining text. Callers that rewrite the file
// re-prepend the BOM so it survives the round trip.
//
// Port of utils/text.ts splitBom (pi 1355cd36e), which moved this helper out of
// core/tools/edit-diff.ts. Exactly one BOM is removed, matching pi's
// `content.slice(1)` over a single UTF-16 code unit.
func splitBOM(content string) (bom, text string) {
	if rest, ok := strings.CutPrefix(content, utf8BOM); ok {
		return utf8BOM, rest
	}
	return "", content
}

// stripBOM removes a leading UTF-8 BOM from decoded text (port of
// utils/text.ts stripBom). Defined through splitBOM so the two cannot drift.
func stripBOM(content string) string {
	_, text := splitBOM(content)
	return text
}
