package providers

import (
	"strings"

	"github.com/sky-valley/pi/internal/jstext"
)

// utf8StreamDecoder is a TextDecoder("utf-8") decoding a stream one read at a
// time, as decode(read, {stream: true}) does (the WHATWG Encoding Standard):
// one byte-order mark before the stream's first character is dropped, however
// the reads split it; invalid UTF-8 becomes one U+FFFD per maximal subpart
// (jstext.DecodeUTF8); and a sequence a read leaves incomplete is held for the
// next read. At the stream's end the bytes still held are what a flush,
// decode(), would turn into U+FFFD; a caller that never flushes drops them.
type utf8StreamDecoder struct {
	held []byte
	// started is whether the stream's first character has been decoded,
	// after which no byte-order mark is dropped.
	started bool
}

// decode is the text read decodes to, given the reads before it.
func (d *utf8StreamDecoder) decode(read []byte) string {
	b := read
	if len(d.held) > 0 {
		b = append(d.held, read...)
		d.held = nil
	}
	cut := len(b) - incompleteUTF8Suffix(b)
	text := jstext.DecodeUTF8(b[:cut])
	if cut < len(b) {
		d.held = append([]byte(nil), b[cut:]...)
	}
	if !d.started && text != "" {
		d.started = true
		text = strings.TrimPrefix(text, utf8BOM)
	}
	return text
}

// incompleteUTF8Suffix is the length of the incomplete sequence b ends with:
// a lead byte and the continuation bytes after it, when more continuation
// bytes could still complete a valid sequence. It is 0 when b ends where a
// character ends, or on bytes no later byte could make valid, which decode
// replaces at once.
func incompleteUTF8Suffix(b []byte) int {
	for k := 1; k <= 3 && k <= len(b); k++ {
		lead := b[len(b)-k]
		if lead < 0x80 {
			return 0
		}
		if lead < 0xC0 {
			continue // a continuation byte: its lead is further back
		}
		// The bounds jstext.DecodeUTF8 reads the sequence with.
		needed, lower, upper := jstext.UTF8Lead(lead)
		if needed == 0 {
			return 0 // a byte no sequence starts with
		}
		if k-1 >= needed {
			return 0 // the sequence is complete
		}
		for _, c := range b[len(b)-k+1:] {
			if c < lower || c > upper {
				return 0
			}
			lower, upper = 0x80, 0xBF
		}
		return k
	}
	return 0
}
