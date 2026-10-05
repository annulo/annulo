package delta

import (
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// Upstream's defaults for overlap's probe and maxCandidates parameters. No
// caller in pi passes either — not the revision diff, not upstream's tests —
// so here they are constants.
const (
	// overlapProbe is the length, in UTF-16 code units, of the head of b
	// tried first. A probe of length h can only find overlaps of at least h —
	// the head must actually occur in a — so the long head catches the large
	// overlaps a rolling window produces with few candidates, and the
	// one-unit fallback finds any overlap at the cost of more.
	overlapProbe = 64
	// overlapMaxCandidates bounds how many positions of one head are tried.
	// Repetitive output — a build log, or any run of one character — makes a
	// long head match at thousands of positions. Giving up returns 0, which
	// emits a set: larger, never wrong.
	overlapMaxCandidates = 8
)

// Overlap is the longest suffix of a that is a prefix of b, looking back at
// most scan code units of a: upstream's exported overlap(a, b, scan), with its
// default probe (64 units) and candidate budget (8). The revision diff uses it
// to publish a rolling window as a front-truncation plus an append. It probes
// with strings.Index and verifies exact substring equality, so the hot loops
// are native; a hand-written KMP is asymptotically equivalent and much slower
// in practice.
//
// Always correct: the returned n satisfies a[len(a)-n:] == b[:n] in UTF-16
// code units. It is not always maximal — the candidate budget can give up on
// a repetitive tail — and the diff turns 0 into a whole-value set.
//
// Every count is in UTF-16 code units: scan, the probe length and the result,
// because that is the unit the "t" op carries and the unit pi's own overlap
// counts. The search itself runs on bytes — a match of a whole-rune head is a
// match in either unit, and candidates come in the same order — so only the
// tail's start, the head's end and the result are translated. A cut that lands
// inside a surrogate pair is where the two units disagree: pi's head then ends
// with a lone high surrogate and pi's tail may open with a lone low one. The
// head keeps the whole runes before the cut and remembers the rune the cut
// split, so a candidate must also continue with the same high surrogate; the
// tail begins after the split pair, because a lone low surrogate opens no
// string b could be.
func Overlap(a, b string, scan int) int {
	if a == "" || b == "" || scan <= 0 {
		// a.slice(a.length - scan) with a negative scan is the empty tail.
		return 0
	}
	tail := a[tailStart(a, scan):]
	long := headOf(b, overlapProbe)
	if n := long.find(tail, b); n > 0 || long.units() == 1 {
		// A one-unit b makes the fallback the same probe again.
		return n
	}
	return headOf(b, 1).find(tail, b)
}

// head is the first h code units of b, as strings.Index can search for them:
// the whole runes, plus the rune whose high surrogate the h-th unit is when
// the cut split a pair.
type head struct {
	runes string
	split rune // 0 when the head ends on a rune boundary
}

// headOf takes the first probe code units of b, or all of b if it is shorter.
func headOf(b string, probe int) head {
	i := 0
	for probe > 0 && i < len(b) {
		r, size := utf8.DecodeRuneInString(b[i:])
		units := utf16.RuneLen(r)
		if units > probe {
			return head{runes: b[:i], split: r}
		}
		probe -= units
		i += size
	}
	return head{runes: b[:i]}
}

func (h head) units() int {
	n := utf16Len(h.runes)
	if h.split != 0 {
		n++
	}
	return n
}

// find is one pass of upstream's candidate loop: each occurrence of the head
// in tail, up to the candidate budget, is checked as a suffix of tail that is
// a prefix of b, and the first that is gives the answer in code units.
func (h head) find(tail, b string) int {
	tried := 0
	for k := strings.Index(tail, h.runes); k != -1; k = indexFrom(tail, h.runes, k+1) {
		if !h.continues(tail[k+len(h.runes):]) {
			// Not an occurrence of the head at all, so not a candidate.
			continue
		}
		tried++
		if tried > overlapMaxCandidates {
			break
		}
		if strings.HasPrefix(b, tail[k:]) {
			return utf16Len(tail[k:])
		}
	}
	return 0
}

// continues reports whether rest opens with the split rune's high surrogate —
// upstream's head, one code unit longer than h.runes, matched here.
func (h head) continues(rest string) bool {
	if h.split == 0 {
		return true
	}
	r, _ := utf8.DecodeRuneInString(rest)
	// Two astral runes share a high surrogate when they share their top ten
	// bits above the 0x10000 base — a full 1024-rune block.
	return utf16.RuneLen(r) == 2 && r>>10 == h.split>>10
}

// tailStart is the byte offset where the last scan code units of a begin. When
// the cut lands inside a surrogate pair, pi's tail opens with the pair's lone
// low surrogate, which no head can match; the pair is left out.
func tailStart(a string, scan int) int {
	i := len(a)
	for scan > 0 && i > 0 {
		r, size := utf8.DecodeLastRuneInString(a[:i])
		units := utf16.RuneLen(r)
		if units > scan {
			break
		}
		scan -= units
		i -= size
	}
	return i
}

// indexFrom is s.indexOf(sub, from): the first index of sub in s at or after
// from, or -1.
func indexFrom(s, sub string, from int) int {
	if from > len(s) {
		return -1
	}
	if k := strings.Index(s[from:], sub); k != -1 {
		return from + k
	}
	return -1
}

// utf16Len is s.length for a JavaScript string: its UTF-16 code units.
func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		n += utf16.RuneLen(r)
	}
	return n
}
