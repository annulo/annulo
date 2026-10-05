package jstext

import (
	"errors"
	"fmt"
	"unicode/utf16"
)

// JSONSyntaxError returns the SyntaxError V8's JSON.parse throws for text, as
// an error carrying its exact message, or nil when JSON.parse accepts text.
// pi surfaces that message wherever a JSON.parse (or a fetch Response's
// json()) fails inside a stream, so it is model-visible text.
//
// It mirrors V8's JsonParser (src/json/json-parser.cc): the same scan order,
// so the first error V8 reports is the one reported here, at the same
// position, counted in UTF-16 code units with V8's line and column; the same
// message templates; and the same choice between them — a template the
// parser names for what it expected, else one picked by the unexpected
// token, quoting the whole source when it is shorter than 21 code units and
// ten code units either side of the error otherwise. Verified against node
// v26.4.0 over testdata/json-parse-errors-node.json.
//
// A Go string holds UTF-8, so text is read as the UTF-16 string JavaScript
// would hold, invalid bytes as U+FFFD; and a quoted code unit that is half a
// surrogate pair, which JavaScript keeps, comes out as U+FFFD.
func JSONSyntaxError(text string) error {
	p := &jsonScanner{src: utf16.Encode([]rune(text))}
	p.value()
	if p.err == "" && !p.check(tokEOS) {
		p.report(p.next, msgNonWhitespaceAfter)
	}
	if p.err == "" {
		return nil
	}
	return errors.New(p.err)
}

// jsonToken is V8's JsonToken: the class of a code unit where a token starts.
type jsonToken int

const (
	tokIllegal jsonToken = iota
	tokEOS
	tokWhitespace
	tokString
	tokNumber
	tokLBrace
	tokRBrace
	tokLBrack
	tokRBrack
	tokColon
	tokComma
	tokTrue
	tokFalse
	tokNull
)

// endOfString is V8's kEndOfString, what the scanner reads past the end.
const endOfString = -1

// The templates V8 formats with the error's position (MessageTemplate
// kJsonParse*); each is followed by " at position N (line L column C)".
const (
	msgExpectedPropNameOrRBrace = "Expected property name or '}' in JSON"
	msgExpectedCommaOrRBrack    = "Expected ',' or ']' after array element in JSON"
	msgExpectedCommaOrRBrace    = "Expected ',' or '}' after property value in JSON"
	msgExpectedColon            = "Expected ':' after property name in JSON"
	msgExpectedDoubleQuotedName = "Expected double-quoted property name in JSON"
	msgUnterminatedString       = "Unterminated string in JSON"
	msgBadControlCharacter      = "Bad control character in string literal in JSON"
	msgBadUnicodeEscape         = "Bad Unicode escape in JSON"
	msgBadEscapedCharacter      = "Bad escaped character in JSON"
	msgNoNumberAfterMinusSign   = "No number after minus sign in JSON"
	msgUnterminatedFraction     = "Unterminated fractional number in JSON"
	msgExponentMissingNumber    = "Exponent part is missing a number in JSON"
	msgNonWhitespaceAfter       = "Unexpected non-whitespace character after JSON"
	msgUnexpectedNumber         = "Unexpected number in JSON"
	msgUnexpectedString         = "Unexpected string in JSON"
)

// jsonContextChars is V8's kMaxContextCharacters: how much of a long source
// an "Unexpected token" message quotes on each side of the error. A source
// shorter than twice that plus one is quoted whole.
const jsonContextChars = 10

type jsonScanner struct {
	src  []uint16
	pos  int       // V8's cursor_, in code units
	next jsonToken // the token skipWhitespace stopped at (V8's next_)
	err  string    // the first error's message; V8 stops at it
}

func (p *jsonScanner) cur() int {
	if p.pos < len(p.src) {
		return int(p.src[p.pos])
	}
	return endOfString
}

// tokenOf is V8's one_char_json_tokens lookup, with the end of input as EOS
// and anything past Latin-1 illegal.
func tokenOf(c int) jsonToken {
	switch {
	case c == endOfString:
		return tokEOS
	case c == ' ' || c == '\t' || c == '\n' || c == '\r':
		return tokWhitespace
	case c == '"':
		return tokString
	case c == '-' || (c >= '0' && c <= '9'):
		return tokNumber
	case c == '{':
		return tokLBrace
	case c == '}':
		return tokRBrace
	case c == '[':
		return tokLBrack
	case c == ']':
		return tokRBrack
	case c == ':':
		return tokColon
	case c == ',':
		return tokComma
	case c == 't':
		return tokTrue
	case c == 'f':
		return tokFalse
	case c == 'n':
		return tokNull
	}
	return tokIllegal
}

// skipWhitespace moves to the next non-whitespace code unit and records its
// token.
func (p *jsonScanner) skipWhitespace() jsonToken {
	for {
		p.next = tokenOf(p.cur())
		if p.next != tokWhitespace {
			return p.next
		}
		p.pos++
	}
}

// check consumes the next token when it is tok.
func (p *jsonScanner) check(tok jsonToken) bool {
	if p.skipWhitespace() != tok {
		return false
	}
	p.pos++
	return true
}

// expectNext consumes the next token, which must be tok, else reports msg.
func (p *jsonScanner) expectNext(tok jsonToken, msg string) bool {
	p.skipWhitespace()
	return p.expect(tok, msg)
}

func (p *jsonScanner) expect(tok jsonToken, msg string) bool {
	if p.next == tok {
		p.pos++
		return true
	}
	p.report(p.next, msg)
	return false
}

func (p *jsonScanner) reportUnexpectedCharacter(c int) {
	p.report(tokenOf(c), "")
}

// report records V8's SyntaxError for an error at the cursor: msg when the
// parser names what it expected, else the template the unexpected token
// picks (V8's ReportUnexpectedToken and LookUpErrorMessageForJsonToken).
func (p *jsonScanner) report(tok jsonToken, msg string) {
	if p.err != "" {
		return
	}
	defer func() { p.pos = len(p.src) }()
	if msg == "" {
		switch tok {
		case tokEOS:
			p.err = "Unexpected end of JSON input"
			return
		case tokNumber:
			msg = msgUnexpectedNumber
		case tokString:
			msg = msgUnexpectedString
		default:
			p.err = p.unexpectedToken()
			return
		}
	}
	line, column := p.location()
	p.err = fmt.Sprintf("%s at position %d (line %d column %d)", msg, p.pos, line, column)
}

// unexpectedToken is the "is not valid JSON" family: the source alone when it
// is one of the strings V8 names specially, else the offending code unit
// with the source or the stretch of it around the error.
func (p *jsonScanner) unexpectedToken() string {
	source := string(utf16.Decode(p.src))
	switch source {
	case "[object Object]", "undefined", "Infinity", "NaN":
		return fmt.Sprintf("%q is not valid JSON", source)
	}
	token := string(utf16.Decode(p.src[p.pos : p.pos+1]))
	n := len(p.src)
	quote := func(from, to int) string { return string(utf16.Decode(p.src[from:to])) }
	switch {
	case n < 2*jsonContextChars+1:
		return fmt.Sprintf(`Unexpected token '%s', "%s" is not valid JSON`, token, source)
	case p.pos < jsonContextChars:
		return fmt.Sprintf(`Unexpected token '%s', "%s"... is not valid JSON`, token, quote(0, p.pos+jsonContextChars))
	case p.pos < n-jsonContextChars:
		return fmt.Sprintf(`Unexpected token '%s', ..."%s"... is not valid JSON`, token, quote(p.pos-jsonContextChars, p.pos+jsonContextChars))
	default:
		return fmt.Sprintf(`Unexpected token '%s', ..."%s" is not valid JSON`, token, quote(p.pos-jsonContextChars, n))
	}
}

// location is V8's CalculateFileLocation: 1-based line and column of the
// cursor, where \r, \n and \r\n each end a line.
func (p *jsonScanner) location() (line, column int) {
	line, lineStart := 1, 0
	for i := 0; i < p.pos; i++ {
		if p.src[i] == '\r' && i < p.pos-1 && p.src[i+1] == '\n' {
			i++
		}
		if p.src[i] == '\r' || p.src[i] == '\n' {
			line++
			lineStart = i + 1
		}
	}
	return line, 1 + p.pos - lineStart
}

// value parses one JSON value (V8's ParseJsonValue).
func (p *jsonScanner) value() {
	switch p.skipWhitespace() {
	case tokString:
		p.pos++
		p.scanString()
	case tokNumber:
		p.number()
	case tokLBrace:
		p.pos++
		if p.check(tokRBrace) {
			return
		}
		if !p.property(msgExpectedPropNameOrRBrace) {
			return
		}
		for {
			p.value()
			if p.err != "" {
				return
			}
			if !p.check(tokComma) {
				p.expect(tokRBrace, msgExpectedCommaOrRBrace)
				return
			}
			if !p.property(msgExpectedDoubleQuotedName) {
				return
			}
		}
	case tokLBrack:
		p.pos++
		if p.check(tokRBrack) {
			return
		}
		for {
			p.value()
			if p.err != "" {
				return
			}
			if !p.check(tokComma) {
				p.expect(tokRBrack, msgExpectedCommaOrRBrack)
				return
			}
		}
	case tokTrue:
		p.literal("true")
	case tokFalse:
		p.literal("false")
	case tokNull:
		p.literal("null")
	default: // a colon, comma, closing bracket, illegal code unit or the end
		p.reportUnexpectedCharacter(p.cur())
	}
}

// property reads a property name and its colon; missing names msg.
func (p *jsonScanner) property(missing string) bool {
	if !p.expectNext(tokString, missing) {
		return false
	}
	p.scanString()
	return p.err == "" && p.expectNext(tokColon, msgExpectedColon)
}

// literal is V8's ScanLiteral: the cursor is on the literal's first letter.
func (p *jsonScanner) literal(word string) {
	remaining := len(p.src) - p.pos
	if remaining >= len(word) {
		match := true
		for i := 1; i < len(word); i++ {
			if p.src[p.pos+i] != uint16(word[i]) {
				match = false
				break
			}
		}
		if match {
			p.pos += len(word)
			return
		}
	}
	p.pos++
	for i := 0; i < min(len(word)-1, remaining-1); i++ {
		if uint16(word[1+i]) != p.src[p.pos] {
			p.reportUnexpectedCharacter(p.cur())
			return
		}
		p.pos++
	}
	p.report(tokEOS, "")
}

func isDecimalDigit(c int) bool { return c >= '0' && c <= '9' }

// isNumberPart is V8's IsNumberPart scan flag: a code unit that can continue
// a number.
func isNumberPart(c int) bool {
	return isDecimalDigit(c) || c == '.' || c == 'e' || c == 'E' || c == '-' || c == '+'
}

func (p *jsonScanner) advanceToNonDecimal() {
	for isDecimalDigit(p.cur()) {
		p.pos++
	}
}

// number is V8's ParseJsonNumber, for its errors.
func (p *jsonScanner) number() {
	negative := false
	c := p.cur()
	if c == '-' {
		negative = true
		p.pos++
		c = p.cur()
	}
	if c == '0' {
		// A leading zero is the only digit before a point or an exponent.
		p.pos++
		c = p.cur()
		if isNumberPart(c) {
			if isDecimalDigit(c) {
				p.report(tokNumber, "")
				return
			}
		} else if !negative {
			return
		}
	} else {
		start := p.pos
		p.advanceToNonDecimal()
		if p.pos == start {
			p.report(tokIllegal, msgNoNumberAfterMinusSign)
			return
		}
	}
	if p.cur() == '.' {
		p.pos++
		if !isDecimalDigit(p.cur()) {
			p.report(tokIllegal, msgUnterminatedFraction)
			return
		}
		p.advanceToNonDecimal()
	}
	if c := p.cur(); c == 'e' || c == 'E' {
		p.pos++
		if c := p.cur(); c == '-' || c == '+' {
			p.pos++
		}
		if !isDecimalDigit(p.cur()) {
			p.report(tokIllegal, msgExponentMissingNumber)
			return
		}
		p.advanceToNonDecimal()
	}
}

// scanString is V8's ScanJsonString, for its errors: the cursor is past the
// opening quote.
func (p *jsonScanner) scanString() {
	for {
		for p.pos < len(p.src) {
			c := p.src[p.pos]
			if c == '"' || c == '\\' || c < 0x20 {
				break
			}
			p.pos++
		}
		if p.pos >= len(p.src) {
			p.report(tokIllegal, msgUnterminatedString)
			return
		}
		switch p.src[p.pos] {
		case '"':
			p.pos++
			return
		case '\\':
			p.pos++
			c := p.cur()
			if c < 0 || c > 0xff {
				p.reportUnexpectedCharacter(c)
				return
			}
			switch c {
			case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
			case 'u':
				for range 4 {
					p.pos++
					if !isHexDigit(p.cur()) {
						p.report(tokIllegal, msgBadUnicodeEscape)
						return
					}
				}
			default:
				p.report(tokIllegal, msgBadEscapedCharacter)
				return
			}
			p.pos++
		default: // a control character
			p.report(tokIllegal, msgBadControlCharacter)
			return
		}
	}
}

func isHexDigit(c int) bool {
	return isDecimalDigit(c) || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}
