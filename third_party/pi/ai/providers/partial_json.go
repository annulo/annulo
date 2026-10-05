package providers

import (
	"errors"
	"math"
	"sort"
	"strings"

	"github.com/sky-valley/pi/ai"
	"github.com/sky-valley/pi/internal/jstext"
)

// This file ports the partial-json package (0.1.7, the version pi's
// package-lock.json locks) as pi's parseStreamingJson calls it: `parse(text)`,
// whose default allows every kind of value to be partial (Allow.ALL). It is
// ported line for line, quirks included, since what it makes of a truncated
// tool-call argument text is what pi streams and, when finish_reason is
// "length", keeps.
//
// partial-json indexes its text by UTF-16 code unit; this port indexes the
// UTF-8 bytes. Every position the parser compares, cuts or searches is at an
// ASCII character or an end of the text, so the two agree.

// errPartialJSON stands for whatever partial-json throws — its PartialJSON and
// MalformedJSON errors, and the SyntaxError of a JSON.parse it does not catch.
// Every one of its callers catches them all alike.
var errPartialJSON = errors.New("partial-json: unparseable")

// partialJSONParse is partial-json's parse(text): the value the text begins,
// as much of it as the text holds, in ai.DecodeOrderedValue's shapes. NaN and
// ±Infinity, which partial-json also reads, are float64s.
func partialJSONParse(text string) (any, error) {
	trimmed := jstext.Trim(text)
	if trimmed == "" {
		return nil, errPartialJSON // `${jsonString} is empty`
	}
	p := &partialJSONParser{s: trimmed}
	return p.parseAny()
}

type partialJSONParser struct {
	s string
	i int
}

// is reports whether the text holds c at i (JS `jsonString[i] === c`).
func (p *partialJSONParser) is(i int, c byte) bool {
	return i >= 0 && i < len(p.s) && p.s[i] == c
}

func (p *partialJSONParser) skipBlank() {
	for p.i < len(p.s) && strings.IndexByte(" \n\r\t", p.s[p.i]) >= 0 {
		p.i++
	}
}

// partialJSONLiterals are the words parseAny reads whole or, at the text's
// end, as any prefix — longer than minLength of them for -Infinity, whose "-"
// alone is left to parseNum.
var partialJSONLiterals = []struct {
	word      string
	value     any
	minLength int
}{
	{"null", nil, 0},
	{"true", true, 0},
	{"false", false, 0},
	{"Infinity", math.Inf(1), 0},
	{"-Infinity", math.Inf(-1), 1},
	{"NaN", math.NaN(), 0},
}

func (p *partialJSONParser) parseAny() (any, error) {
	p.skipBlank()
	if p.i >= len(p.s) {
		return nil, errPartialJSON // Unexpected end of input
	}
	switch p.s[p.i] {
	case '"':
		return p.parseStr()
	case '{':
		return p.parseObj()
	case '[':
		return p.parseArr()
	}
	rest := p.s[p.i:]
	for _, lit := range partialJSONLiterals {
		if strings.HasPrefix(rest, lit.word) || (len(rest) < len(lit.word) && len(rest) > lit.minLength && strings.HasPrefix(lit.word, rest)) {
			p.i += len(lit.word)
			return lit.value, nil
		}
	}
	return p.parseNum()
}

// parseStr reads a string from its opening quote — or from wherever an object
// key is expected, quote or not — to the next quote no backslash escapes.
// JSON.parse reads what lies between; an unterminated string is read up to
// its end (less a trailing backslash), and when that does not parse either,
// up to the last backslash in the whole text.
func (p *partialJSONParser) parseStr() (any, error) {
	start := p.i
	escape := false
	p.i++ // skip initial quote
	for p.i < len(p.s) && (p.s[p.i] != '"' || (escape && p.s[p.i-1] == '\\')) {
		escape = p.s[p.i] == '\\' && !escape
		p.i++
	}
	trailing := 0 // Number(escape)
	if escape {
		trailing = 1
	}
	if p.is(p.i, '"') {
		p.i++
		value, err := ai.DecodeOrderedValue([]byte(jsSubstring(p.s, start, p.i-trailing)))
		if err != nil {
			return nil, errPartialJSON // throwMalformedError
		}
		return value, nil
	}
	if value, err := ai.DecodeOrderedValue([]byte(jsSubstring(p.s, start, p.i-trailing) + `"`)); err == nil {
		return value, nil
	}
	value, err := ai.DecodeOrderedValue([]byte(jsSubstring(p.s, start, strings.LastIndexByte(p.s, '\\')) + `"`))
	if err != nil {
		return nil, errPartialJSON // the SyntaxError, uncaught here
	}
	return value, nil
}

// parseObj reads an object's members until its closing brace. Whatever fails
// — the text ending, a key or value that does not parse — ends the object with
// the members read so far. The colon is skipped without being looked at, and
// so is a comma after a value, when there is one.
func (p *partialJSONParser) parseObj() (any, error) {
	p.i++ // skip initial brace
	p.skipBlank()
	var obj partialJSONObject
	for !p.is(p.i, '}') {
		p.skipBlank()
		if p.i >= len(p.s) {
			return obj.value(), nil
		}
		key, err := p.parseStr()
		if err != nil {
			return obj.value(), nil
		}
		p.skipBlank()
		p.i++ // skip colon
		value, err := p.parseAny()
		if err != nil {
			return obj.value(), nil
		}
		if err := obj.set(key, value); err != nil {
			return obj.value(), nil
		}
		p.skipBlank()
		if p.is(p.i, ',') {
			p.i++ // skip comma
		}
	}
	p.i++ // skip final brace
	return obj.value(), nil
}

// parseArr reads an array's elements until its closing bracket, ending it with
// the elements read so far when one fails.
func (p *partialJSONParser) parseArr() (any, error) {
	p.i++ // skip initial bracket
	arr := []any{}
	for !p.is(p.i, ']') {
		value, err := p.parseAny()
		if err != nil {
			return arr, nil
		}
		arr = append(arr, value)
		p.skipBlank()
		if p.is(p.i, ',') {
			p.i++ // skip comma
		}
	}
	p.i++ // skip final bracket
	return arr, nil
}

// parseNum reads a number as the text up to the next ',', ']' or '}' — the
// whole text when the number starts it — that JSON.parse accepts, else that
// text cut at the last "e" in the whole text.
func (p *partialJSONParser) parseNum() (any, error) {
	if p.i == 0 {
		if p.s == "-" {
			return nil, errPartialJSON // Not sure what '-' is
		}
		if value, err := ai.DecodeOrderedValue([]byte(p.s)); err == nil {
			return value, nil
		}
		if value, err := ai.DecodeOrderedValue([]byte(jsSubstring(p.s, 0, strings.LastIndexByte(p.s, 'e')))); err == nil {
			return value, nil
		}
		return nil, errPartialJSON
	}
	start := p.i
	if p.s[p.i] == '-' {
		p.i++
	}
	for p.i < len(p.s) && strings.IndexByte(",]}", p.s[p.i]) < 0 {
		p.i++
	}
	text := p.s[start:p.i]
	if value, err := ai.DecodeOrderedValue([]byte(text)); err == nil {
		return value, nil
	}
	if text == "-" {
		return nil, errPartialJSON // Not sure what '-' is
	}
	if value, err := ai.DecodeOrderedValue([]byte(jsSubstring(p.s, start, strings.LastIndexByte(p.s, 'e')))); err == nil {
		return value, nil
	}
	return nil, errPartialJSON
}

// jsSubstring is String.prototype.substring: each index clamped to the text,
// and the two swapped when the start is past the end.
func jsSubstring(s string, start, end int) string {
	start, end = min(max(start, 0), len(s)), min(max(end, 0), len(s))
	if start > end {
		start, end = end, start
	}
	return s[start:end]
}

// partialJSONObject is the plain object partial-json builds with `obj[key] =
// value`: a repeated key keeps its first position and takes the last value,
// and keys that are array indices list first, ascending. "__proto__" runs
// Object.prototype's setter rather than making a property of its own: an
// object or null becomes the prototype, anything else is ignored — and once
// the prototype is null, no setter is left, so a later "__proto__" is an
// ordinary property.
type partialJSONObject struct {
	fields    ai.OrderedObject
	nullProto bool
}

func (o *partialJSONObject) set(key, value any) error {
	name, isString := key.(string)
	if !isString {
		text, err := jsToString(key) // ToPropertyKey
		if err != nil {
			return err
		}
		name = text
	}
	if name == "__proto__" && !o.nullProto {
		o.nullProto = value == nil
		return nil
	}
	for i := range o.fields {
		if o.fields[i].Key == name {
			o.fields[i].Value = value
			return nil
		}
	}
	o.fields = append(o.fields, ai.OrderedField{Key: name, Value: value})
	return nil
}

func (o *partialJSONObject) value() ai.OrderedObject {
	fields := o.fields
	if fields == nil {
		fields = ai.OrderedObject{}
	}
	sort.SliceStable(fields, func(i, j int) bool {
		a, aIndex := jstext.ArrayIndexKey(fields[i].Key)
		b, bIndex := jstext.ArrayIndexKey(fields[j].Key)
		return aIndex && (!bIndex || a < b)
	})
	return fields
}
