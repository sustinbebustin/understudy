package ruler

import (
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// tokenKind is a JSON token as a streaming parser reports it. Patterns and
// events are read token by token, the way Ruler reads them with Jackson, so
// errors surface in source order and duplicate keys stay visible.
type tokenKind uint8

const (
	tokEOF tokenKind = iota
	tokStartObject
	tokEndObject
	tokStartArray
	tokEndArray
	tokFieldName
	tokString
	tokNumber
	tokTrue
	tokFalse
	tokNull
)

// token is one JSON token. text is the decoded string for tokFieldName and
// tokString and the source text for every other kind.
type token struct {
	kind tokenKind
	text string
	// malformed is the syntax error of a tokString whose body is not valid,
	// nil otherwise. Jackson reads a string value only when its text is
	// asked for or the parser moves past it, so a caller that rejects the
	// token for its kind never sees the error; stringText and the lexer's
	// next call report it.
	malformed *syntaxError
}

// stringText is a tokString's text, or its syntax error.
func stringText(t token) (string, *CompileError) {
	if t.malformed != nil {
		return "", &CompileError{Cause: InvalidJSON{}, Detail: t.malformed.message}
	}
	return t.text, nil
}

// display is the token's text as Jackson's getText reports it, which Ruler
// quotes in several error messages ("Invalid member in numeric match: ]").
func (t token) display() string {
	switch t.kind {
	case tokStartObject:
		return "{"
	case tokEndObject:
		return "}"
	case tokStartArray:
		return "["
	case tokEndArray:
		return "]"
	case tokEOF:
		return ""
	case tokFieldName, tokString, tokNumber, tokTrue, tokFalse, tokNull:
		return t.text
	}
	return t.text
}

// syntaxError is malformed JSON, worded as Jackson 2.22.2's
// ReaderBasedJsonParser words it (jackson.go).
type syntaxError struct {
	message string
}

// frameState is where a container is in its grammar.
type frameState uint8

const (
	frameOpened frameState = iota // just after '{' or '['
	frameNamed                    // object only: field name read, value next
	frameValued                   // a member or element was read
)

type frame struct {
	object bool
	state  frameState
	// start is where the container's '{' or '[' is, which Jackson names
	// when the container is left open or closed with the wrong marker.
	start sourcePos
}

// desc is the container's name in Jackson's messages.
func (f *frame) desc() string {
	if f.object {
		return "Object"
	}
	return "Array"
}

// sourcePos is a byte offset with the line it is on, as Jackson counts lines:
// 1-based, ended by "\n", "\r\n" or a lone "\r" between tokens.
type sourcePos struct {
	offset    int
	line      int
	lineStart int
}

// lexer is a pull parser over one JSON text. A root value may be followed by
// more root values; whether that is allowed is the caller's decision.
type lexer struct {
	src   []byte
	pos   int
	stack []frame
	// line and lineStart locate pos as a sourcePos does.
	line      int
	lineStart int
	// pending is a field's scalar value. Jackson reads a scalar together with
	// the field name before it, so the value's syntax errors come before the
	// caller sees the name; the next call returns it. Strings and containers
	// are read when their turn comes, as Jackson reads them.
	pending *token
	// malformed is the error of a string value returned as a token, which
	// every later call reports, as Jackson does when it skips the string.
	malformed *syntaxError
	// source is how locations in error messages name the source.
	source locationSource
}

// locationSource is how a Jackson location names its source: Ruler's
// pattern parser includes it, while the parser Ruler reads events with
// keeps Jackson's default, which redacts it.
type locationSource uint8

const (
	sourceIncluded locationSource = iota
	sourceRedacted
)

func newLexer(src []byte) *lexer {
	return &lexer{src: src, line: 1, source: sourceIncluded}
}

func (l *lexer) top() *frame {
	if len(l.stack) == 0 {
		return nil
	}
	return &l.stack[len(l.stack)-1]
}

// next returns the next token, or tokEOF once the input is exhausted
// outside every container.
func (l *lexer) next() (token, *syntaxError) {
	if l.malformed != nil {
		return token{}, l.malformed
	}
	if top := l.top(); top != nil && top.state == frameNamed {
		top.state = frameValued
		if p := l.pending; p != nil {
			l.pending = nil
			return *p, nil
		}
		return l.value()
	}
	if err := l.skipSpace(); err != nil {
		return token{}, err
	}
	top := l.top()
	if l.pos >= len(l.src) {
		if top == nil {
			return token{kind: tokEOF}, nil
		}
		return token{}, &syntaxError{message: "Unexpected end-of-input: expected close marker for " + top.desc() +
			" (start marker at " + l.location(top.start) + ")"}
	}
	if c := l.src[l.pos]; c == ']' || c == '}' {
		return l.closeContainer(c)
	}
	if top == nil {
		return l.value()
	}
	if top.state == frameValued {
		if l.src[l.pos] != ',' {
			return token{}, l.unexpected("was expecting comma to separate " + top.desc() + " entries")
		}
		l.pos++
		if err := l.skipSpace(); err != nil {
			return token{}, err
		}
		if l.pos >= len(l.src) {
			return token{}, betweenEntries(top)
		}
	}
	if top.object {
		return l.fieldName(top)
	}
	top.state = frameValued
	return l.value()
}

// fieldName reads a field name, its colon and, when the value is a scalar,
// the value.
func (l *lexer) fieldName(top *frame) (token, *syntaxError) {
	if l.src[l.pos] != '"' {
		return token{}, l.unexpected("was expecting double-quote to start field name")
	}
	l.pos++
	name, err := l.stringBody(inName)
	if err != nil {
		return token{}, err
	}
	if n := utf16Len([]byte(name)); n > maxNameLength {
		return token{}, &syntaxError{message: "Name length (" + strconv.Itoa(n) + ") exceeds the maximum allowed (" +
			strconv.Itoa(maxNameLength) + ", from `StreamReadConstraints.getMaxNameLength()`)"}
	}
	if err := l.skipSpace(); err != nil {
		return token{}, err
	}
	if l.pos >= len(l.src) {
		return token{}, betweenEntries(top)
	}
	if l.src[l.pos] != ':' {
		return token{}, l.unexpected("was expecting a colon to separate field name and value")
	}
	l.pos++
	if err := l.skipSpace(); err != nil {
		return token{}, err
	}
	if l.pos >= len(l.src) {
		return token{}, betweenEntries(top)
	}
	switch l.src[l.pos] {
	case '"', '[', '{':
	default:
		v, err := l.value()
		if err != nil {
			return token{}, err
		}
		l.pending = &v
	}
	top.state = frameNamed
	return token{kind: tokFieldName, text: name}, nil
}

// maxNameLength is the field name length limit of Jackson's
// StreamReadConstraints, in UTF-16 code units, whose default Ruler keeps.
const maxNameLength = 50000

func betweenEntries(top *frame) *syntaxError {
	return &syntaxError{message: "Unexpected end-of-input within/between " + top.desc() + " entries"}
}

// closeContainer reads a ']' or '}', which must close the innermost
// container.
func (l *lexer) closeContainer(c byte) (token, *syntaxError) {
	top := l.top()
	object := c == '}'
	if top == nil {
		name := "Array"
		if object {
			name = "Object"
		}
		return token{}, &syntaxError{message: "Unexpected close marker '" + string(c) + "': no open " + name + " to close"}
	}
	if top.object != object {
		want := "]"
		if top.object {
			want = "}"
		}
		return token{}, &syntaxError{message: "Unexpected close marker '" + string(c) + "': expected '" + want +
			"' (for " + top.desc() + " starting at " + l.location(top.start) + ")"}
	}
	l.pos++
	l.stack = l.stack[:len(l.stack)-1]
	if object {
		return token{kind: tokEndObject}, nil
	}
	return token{kind: tokEndArray}, nil
}

// maxNestingDepth is the nesting limit of Jackson's StreamReadConstraints,
// whose default Ruler keeps for patterns and events: at most 1000 objects
// and arrays open at once.
const maxNestingDepth = 1000

// value reads one JSON value, or the start of a container, at l.pos, which
// is within the input.
func (l *lexer) value() (token, *syntaxError) {
	switch c := l.src[l.pos]; c {
	case '{':
		return l.open(tokStartObject)
	case '[':
		return l.open(tokStartArray)
	case '"':
		l.pos++
		s, err := l.stringBody(inValue)
		if err != nil {
			l.malformed = err
			return token{kind: tokString, malformed: err}, nil
		}
		return token{kind: tokString, text: s}, nil
	case '-', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
		return l.number()
	case '+':
		return token{}, l.plusSign()
	case 't':
		return l.literal(tokTrue, "true")
	case 'f':
		return l.literal(tokFalse, "false")
	case 'n':
		return l.literal(tokNull, "null")
	case 'N':
		l.pos++
		return token{}, l.nonStandard("NaN", 1)
	case 'I':
		l.pos++
		return token{}, l.nonStandard("Infinity", 1)
	case '}':
		return token{}, l.unexpected("expected a value")
	}
	// Anything else: a token Jackson rejects when it starts like a Java
	// identifier, or an unexpected character.
	r, size := javaChar(l.src[l.pos:])
	if isJavaIdentifierStart(r) {
		l.pos += size
		return token{}, l.invalidToken(string(r))
	}
	return token{}, l.unexpected("expected a valid value " + validValues)
}

// open starts an object or array, within Jackson's nesting limit.
func (l *lexer) open(kind tokenKind) (token, *syntaxError) {
	if len(l.stack) == maxNestingDepth {
		return token{}, &syntaxError{message: "Document nesting depth (" + strconv.Itoa(maxNestingDepth+1) +
			") exceeds the maximum allowed (" + strconv.Itoa(maxNestingDepth) +
			", from `StreamReadConstraints.getMaxNestingDepth()`)"}
	}
	start := sourcePos{offset: l.pos, line: l.line, lineStart: l.lineStart}
	l.pos++
	l.stack = append(l.stack, frame{object: kind == tokStartObject, start: start})
	return token{kind: kind}, nil
}

// literal reads true, false or null, whose first byte is at l.pos.
func (l *lexer) literal(kind tokenKind, word string) (token, *syntaxError) {
	l.pos++
	if err := l.keyword(word, 1); err != nil {
		return token{}, err
	}
	return token{kind: kind, text: word}, nil
}

// nonStandard reads a non-numeric number Jackson recognizes but rejects
// (NaN, Infinity, -INF and the like), whose first from bytes were read.
func (l *lexer) nonStandard(word string, from int) *syntaxError {
	if err := l.keyword(word, from); err != nil {
		return err
	}
	return &syntaxError{message: "Non-standard token '" + word +
		"': enable `JsonReadFeature.ALLOW_NON_NUMERIC_NUMBERS` to allow"}
}

// keyword matches the rest of word, whose first from bytes were read, as
// Jackson's _matchToken does: a mismatch, or a Java identifier character
// right after the word, makes the text read so far an unrecognized token.
func (l *lexer) keyword(word string, from int) *syntaxError {
	for i := from; i < len(word); i++ {
		if l.pos >= len(l.src) || l.src[l.pos] != word[i] {
			return l.invalidToken(word[:i])
		}
		l.pos++
	}
	if l.pos < len(l.src) {
		if c := l.src[l.pos]; c >= '0' && c != ']' && c != '}' {
			if r, _ := javaChar(l.src[l.pos:]); isJavaIdentifierPart(r) {
				return l.invalidToken(word)
			}
		}
	}
	return nil
}

// invalidToken reports an unrecognized token: the text matched so far and
// the Java identifier characters after it, at most maxErrorTokenLength
// characters before an ellipsis (Jackson's _reportInvalidToken).
func (l *lexer) invalidToken(matched string) *syntaxError {
	var b strings.Builder
	b.WriteString(matched)
	n := utf8.RuneCountInString(matched)
	for l.pos < len(l.src) {
		r, size := javaChar(l.src[l.pos:])
		if !isJavaIdentifierPart(r) {
			break
		}
		b.Write(l.src[l.pos : l.pos+size])
		l.pos += size
		n++
		if n >= maxErrorTokenLength {
			b.WriteString("...")
			break
		}
	}
	return &syntaxError{message: "Unrecognized token '" + b.String() + "': was expecting " + validValues}
}

// number reads a number as Jackson's ReaderBasedJsonParser does: it stops at
// the first character that cannot continue the number, which the enclosing
// container then rejects, and words a malformed number with Jackson's
// message. Jackson's messages for an input that ends inside a number lack a
// space after "end-of-input"; they are reproduced as written. A root-level
// number must be followed by white space or the end of the input, and may
// have at most maxNumberLength digits.
func (l *lexer) number() (token, *syntaxError) {
	start := l.pos
	if l.src[l.pos] == '-' {
		l.pos++
		if err := l.afterMinus(); err != nil {
			return token{}, err
		}
	}
	leadingZero := l.src[l.pos] == '0'
	if leadingZero && l.pos+1 < len(l.src) && isDigit(l.src[l.pos+1]) {
		return token{}, &syntaxError{message: "Invalid numeric value: Leading zeroes not allowed"}
	}
	integer := l.pos
	l.pos = l.digitsEnd()
	parts := numberParts{integer: l.pos - integer, fraction: -1}
	if l.peek() == '.' {
		l.pos++
		if l.pos >= len(l.src) {
			return token{}, unexpectedNumberChar('.', "Decimal point not followed by a digit")
		}
		n, err := l.digits("Decimal point not followed by a digit")
		if err != nil {
			return token{}, err
		}
		parts.fraction = n
	}
	exp, err := l.exponent()
	if err != nil {
		return token{}, err
	}
	parts.exponent = exp
	if len(l.stack) == 0 && l.pos < len(l.src) {
		switch l.src[l.pos] {
		case ' ', '\t', '\r', '\n':
		default:
			return token{}, l.unexpected("Expected space separating root-level values")
		}
	}
	// Jackson parses a number that starts with a zero or runs to the end of
	// the input on its secondary path, which counts its length differently.
	if n := parts.length(leadingZero || l.pos == len(l.src)); n > maxNumberLength {
		return token{}, &syntaxError{message: "Number value length (" + strconv.Itoa(n) +
			") exceeds the maximum allowed (" + strconv.Itoa(maxNumberLength) +
			", from `StreamReadConstraints.getMaxNumberLength()`)"}
	}
	return token{kind: tokNumber, text: string(l.src[start:l.pos])}, nil
}

// maxNumberLength is the number length limit of Jackson's
// StreamReadConstraints, whose default Ruler keeps.
const maxNumberLength = 1000

// numberParts counts the digits of a number's integer, fraction and
// exponent; -1 is a part the number does not have.
type numberParts struct {
	integer, fraction, exponent int
}

// length is the number's length as Jackson checks it against
// maxNumberLength: the integer digits of an integer, and otherwise the
// digits of all three parts, where a missing part counts 0 on Jackson's main
// path and -1 on its secondary path.
func (p numberParts) length(secondary bool) int {
	if p.fraction < 0 && p.exponent < 0 {
		return p.integer
	}
	part := func(n int) int {
		if n < 0 && !secondary {
			return 0
		}
		return n
	}
	return p.integer + part(p.fraction) + part(p.exponent)
}

// exponent reads a number's exponent and returns its digit count, or -1
// when the number has no exponent.
func (l *lexer) exponent() (int, *syntaxError) {
	if c := l.peek(); c != 'e' && c != 'E' {
		return -1, nil
	}
	l.pos++
	if c := l.peek(); c == '+' || c == '-' {
		l.pos++
	}
	if l.pos >= len(l.src) {
		return 0, &syntaxError{message: "Unexpected end-of-inputexpected a digit for number exponent"}
	}
	return l.digits("Exponent indicator not followed by a digit")
}

// afterMinus checks the character after a leading minus sign.
func (l *lexer) afterMinus() *syntaxError {
	if l.pos >= len(l.src) {
		return &syntaxError{message: "Unexpected end-of-inputNo digit following sign"}
	}
	switch c := l.src[l.pos]; {
	case isDigit(c):
		return nil
	case c == '.':
		return l.unexpected("expected a valid value " + validValues)
	}
	return l.invalidNumberStart('-')
}

// plusSign rejects a number with a leading plus sign, which JSON does not
// allow.
func (l *lexer) plusSign() *syntaxError {
	l.pos++
	if l.pos >= len(l.src) {
		return &syntaxError{message: "Unexpected end-of-input in a Number value"}
	}
	return l.invalidNumberStart('+')
}

// invalidNumberStart words a sign followed by something other than a digit
// (Jackson's _handleInvalidNumberStart). After "I" it looks for INF and
// Infinity; otherwise the plus sign, or for a minus sign the character after
// it, is the unexpected one.
func (l *lexer) invalidNumberStart(sign byte) *syntaxError {
	if l.src[l.pos] == 'I' {
		l.pos++
		if l.pos >= len(l.src) {
			return &syntaxError{message: "Unexpected end-of-input in a Number value"}
		}
		switch l.src[l.pos] {
		case 'N':
			l.pos++
			return l.nonStandard(string(sign)+"INF", 3)
		case 'n':
			l.pos++
			return l.nonStandard(string(sign)+"Infinity", 3)
		}
	}
	if sign == '+' {
		return unexpectedNumberChar('+', "JSON spec does not allow numbers to have plus signs: "+
			"enable `JsonReadFeature.ALLOW_LEADING_PLUS_SIGN_FOR_NUMBERS` to allow")
	}
	r, _ := utf8.DecodeRune(l.src[l.pos:])
	return unexpectedNumberChar(r, "expected digit (0-9) to follow minus sign, for valid numeric value")
}

// digits consumes the digits a decimal point or exponent indicator needs and
// returns how many there are.
func (l *lexer) digits(missing string) (int, *syntaxError) {
	end := l.digitsEnd()
	if end == l.pos {
		r, _ := utf8.DecodeRune(l.src[l.pos:])
		return 0, unexpectedNumberChar(r, missing)
	}
	n := end - l.pos
	l.pos = end
	return n, nil
}

// digitsEnd returns the offset after the digits at l.pos.
func (l *lexer) digitsEnd() int {
	i := l.pos
	for i < len(l.src) && isDigit(l.src[i]) {
		i++
	}
	return i
}

func unexpectedNumberChar(r rune, comment string) *syntaxError {
	return &syntaxError{message: "Unexpected character (" + charDesc(r) + ") in numeric value: " + comment}
}

// stringContext is what a string is, as Jackson's string messages name it.
type stringContext uint8

const (
	inName stringContext = iota + 1
	inValue
)

// stringBody reads a string after its opening quote and decodes escapes.
func (l *lexer) stringBody(ctx stringContext) (string, *syntaxError) {
	var b strings.Builder
	for l.pos < len(l.src) {
		c := l.src[l.pos]
		switch {
		case c == '"':
			l.pos++
			return b.String(), nil
		case c == '\\':
			if err := l.escape(&b); err != nil {
				return "", err
			}
		case c < 0x20:
			included := "string value"
			if ctx == inName {
				included = "name"
			}
			return "", &syntaxError{message: "Illegal unquoted character (" + charDesc(rune(c)) +
				"): has to be escaped using backslash to be included in " + included}
		case c < utf8.RuneSelf:
			b.WriteByte(c)
			l.pos++
		default:
			r, size := utf8.DecodeRune(l.src[l.pos:])
			if r == utf8.RuneError && size == 1 {
				return "", &syntaxError{message: "Invalid UTF-8 start byte 0x" + strconv.FormatUint(uint64(c), 16)}
			}
			b.WriteRune(r)
			l.pos += size
		}
	}
	if ctx == inName {
		return "", &syntaxError{message: "Unexpected end-of-input in field name"}
	}
	return "", &syntaxError{message: "Unexpected end-of-input: was expecting closing quote for a string value"}
}

const escapeEOF = "Unexpected end-of-input in character escape sequence"

func (l *lexer) escape(b *strings.Builder) *syntaxError {
	if l.pos+1 >= len(l.src) {
		return &syntaxError{message: escapeEOF}
	}
	c := l.src[l.pos+1]
	l.pos += 2
	switch c {
	case '"', '\\', '/':
		b.WriteByte(c)
	case 'b':
		b.WriteByte('\b')
	case 'f':
		b.WriteByte('\f')
	case 'n':
		b.WriteByte('\n')
	case 'r':
		b.WriteByte('\r')
	case 't':
		b.WriteByte('\t')
	case 'u':
		r, err := l.hex4()
		if err != nil {
			return err
		}
		if isHighSurrogate(r) {
			if low, ok := l.lowSurrogate(); ok {
				r = utf16.DecodeRune(r, low)
			}
		}
		// A surrogate left unpaired is written as U+FFFD.
		b.WriteRune(r)
	default:
		r, _ := utf8.DecodeRune(l.src[l.pos-1:])
		return &syntaxError{message: "Unrecognized character escape " + charDesc(r)}
	}
	return nil
}

// hex4 reads the four hex digits of a \u escape, one character at a time.
func (l *lexer) hex4() (rune, *syntaxError) {
	var r rune
	for range 4 {
		if l.pos >= len(l.src) {
			return 0, &syntaxError{message: escapeEOF}
		}
		d, ok := hexDigit(l.src[l.pos])
		if !ok {
			c, _ := utf8.DecodeRune(l.src[l.pos:])
			return 0, &syntaxError{message: "Unexpected character (" + charDesc(c) +
				"): expected a hex-digit for character escape sequence"}
		}
		r = r<<4 | d
		l.pos++
	}
	return r, nil
}

// lowSurrogate consumes the next \uXXXX escape when it is a low surrogate,
// the second half of a pair. Anything else is left for the string reader,
// so a high surrogate followed by an ordinary escape keeps both characters,
// as Jackson does.
func (l *lexer) lowSurrogate() (rune, bool) {
	if l.pos+6 > len(l.src) || l.src[l.pos] != '\\' || l.src[l.pos+1] != 'u' {
		return 0, false
	}
	var r rune
	for _, c := range l.src[l.pos+2 : l.pos+6] {
		d, ok := hexDigit(c)
		if !ok {
			return 0, false
		}
		r = r<<4 | d
	}
	if r < 0xDC00 || r > 0xDFFF {
		return 0, false
	}
	l.pos += 6
	return r, true
}

func isHighSurrogate(r rune) bool { return r >= 0xD800 && r < 0xDC00 }

func hexDigit(c byte) (rune, bool) {
	switch {
	case isDigit(c):
		return rune(c - '0'), true
	case c >= 'a' && c <= 'f':
		return rune(c-'a') + 10, true
	case c >= 'A' && c <= 'F':
		return rune(c-'A') + 10, true
	}
	return 0, false
}

// unexpected reports the character at l.pos, which is within the input.
func (l *lexer) unexpected(expectation string) *syntaxError {
	r, _ := utf8.DecodeRune(l.src[l.pos:])
	return &syntaxError{message: "Unexpected character (" + charDesc(r) + "): " + expectation}
}

func (l *lexer) peek() byte {
	if l.pos >= len(l.src) {
		return 0
	}
	return l.src[l.pos]
}

// skipSpace skips the white space between tokens, counting lines. Any other
// control character is an error, and so is "/", which could only start a
// comment, a feature Ruler leaves off.
func (l *lexer) skipSpace() *syntaxError {
	for l.pos < len(l.src) {
		switch c := l.src[l.pos]; {
		case c == ' ' || c == '\t':
			l.pos++
		case c == '\n':
			l.pos++
			l.newLine()
		case c == '\r':
			l.pos++
			if l.peek() == '\n' {
				l.pos++
			}
			l.newLine()
		case c < 0x20:
			msg := "Illegal character (" + charDesc(rune(c)) +
				"): only regular white space (\\r, \\n, \\t) is allowed between tokens"
			if c == 0x1E {
				msg += " (consider enabling `JsonReadFeature.ALLOW_RS_CONTROL_CHAR` to allow use of Record Separators (\\u001E))"
			}
			return &syntaxError{message: msg}
		case c == '/':
			return l.unexpected("maybe a (non-standard) comment? " +
				"(not recognized as one since Feature 'ALLOW_COMMENTS' not enabled for parser)")
		default:
			return nil
		}
	}
	return nil
}

func (l *lexer) newLine() {
	l.line++
	l.lineStart = l.pos
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// isJSONNumber reports whether s is a number in the JSON grammar:
// -?(0|[1-9][0-9]*)(\.[0-9]+)?([eE][+-]?[0-9]+)?
func isJSONNumber(s string) bool {
	i := 0
	if i < len(s) && s[i] == '-' {
		i++
	}
	switch {
	case i < len(s) && s[i] == '0':
		i++
	case i < len(s) && isDigit(s[i]):
		i = digitsEnd(s, i)
	default:
		return false
	}
	if i < len(s) && s[i] == '.' {
		j := digitsEnd(s, i+1)
		if j == i+1 {
			return false
		}
		i = j
	}
	if i < len(s) && (s[i] == 'e' || s[i] == 'E') {
		i++
		if i < len(s) && (s[i] == '+' || s[i] == '-') {
			i++
		}
		j := digitsEnd(s, i)
		if j == i {
			return false
		}
		i = j
	}
	return i == len(s)
}

func digitsEnd(s string, i int) int {
	for i < len(s) && isDigit(s[i]) {
		i++
	}
	return i
}
