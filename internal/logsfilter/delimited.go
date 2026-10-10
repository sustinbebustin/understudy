package logsfilter

import "strings"

// fixedFields is a space-delimited pattern without "...": pattern field i
// tests event field i, and Options.FieldCount decides what happens to extra
// event fields (rule N6).
type fixedFields struct{ fields []field }

// spreadFields is a space-delimited pattern with "...": head tests the first
// event fields, tail the last ones, and the ellipsis takes any number between.
type spreadFields struct{ head, tail []field }

// field is one pattern field. test is a disjunction of conjunctions, since
// && binds tighter than || and fields take no parentheses; an empty test
// accepts any field.
type field struct{ test [][]condition }

func (f field) holds(text string) bool {
	if len(f.test) == 0 {
		return true
	}
	sub := fieldSubject(text)
	for _, conj := range f.test {
		ok := true
		for _, c := range conj {
			ok = ok && c.holds(sub)
		}
		if ok {
			return true
		}
	}
	return false
}

type delimitedParser struct {
	s       *scanner
	regexes int
}

// parseDelimited reads [f1, f2, ...]. A trailing comma declares one more
// unnamed field; [] matches every event.
func parseDelimited(s *scanner) (matcher, *Error) {
	s.pos++
	p := delimitedParser{s: s}
	var fields []field
	spreadAt := -1
	s.skipSpace()
	closed := s.accept("]")
	for !closed {
		s.skipSpace()
		if s.accept("...") {
			if spreadAt >= 0 {
				return nil, s.fail("... may appear only once")
			}
			spreadAt = len(fields)
		} else {
			f, err := p.field()
			if err != nil {
				return nil, err
			}
			fields = append(fields, f)
		}
		s.skipSpace()
		if closed = s.accept("]"); closed {
			break
		}
		if !s.accept(",") {
			return nil, s.fail("expected , or ]")
		}
		s.skipSpace()
		if closed = s.accept("]"); closed {
			fields = append(fields, field{})
		}
	}
	s.skipSpace()
	if !s.eof() {
		return nil, s.fail("unexpected text after the closing ]")
	}
	switch {
	case spreadAt >= 0:
		return spreadFields{head: fields[:spreadAt], tail: fields[spreadAt:]}, nil
	case len(fields) == 0:
		return matchAll{}, nil
	}
	return fixedFields{fields: fields}, nil
}

// field reads name [op value {(&& | ||) [name] op value}]. Later tests may
// repeat the field's own name.
func (p *delimitedParser) field() (field, *Error) {
	s := p.s
	name := s.name()
	if name == "" {
		return field{}, s.fail("expected a field name or ...")
	}
	s.skipSpace()
	if s.peek() == ',' || s.peek() == ']' {
		return field{}, nil
	}
	var (
		dnf  [][]condition
		conj []condition
	)
	for {
		c, err := p.condition(name, len(conj) > 0 || len(dnf) > 0)
		if err != nil {
			return field{}, err
		}
		conj = append(conj, c)
		s.skipSpace()
		switch {
		case s.accept("&&"):
		case s.accept("||"):
			dnf, conj = append(dnf, conj), nil
		default:
			return field{test: append(dnf, conj)}, nil
		}
	}
}

func (p *delimitedParser) condition(name string, named bool) (condition, *Error) {
	s := p.s
	s.skipSpace()
	start := s.pos
	if named {
		if n := s.name(); n != "" && n != name {
			return condition{}, invalidPattern("a condition on field "+name+" names field "+n, start)
		}
		s.skipSpace()
	}
	o, ok := s.op()
	if !ok {
		return condition{}, s.fail("expected a comparison operator")
	}
	s.skipSpace()
	v, err := s.value()
	if err != nil {
		return condition{}, err
	}
	if err := countRegex(v, &p.regexes, start); err != nil {
		return condition{}, err
	}
	return newCondition(o, v, start)
}

// span is one event field: its byte range in the message and its text with
// grouping brackets or quotes removed.
type span struct {
	start, end int
	text       string
}

// splitFields cuts an event on whitespace; a [...] or "..." run is one field
// and loses its delimiters. An opener without a closer is an ordinary field.
func splitFields(msg string) []span {
	var out []span
	i := 0
	for {
		for i < len(msg) && isSpace(msg[i]) {
			i++
		}
		if i >= len(msg) {
			return out
		}
		start := i
		if closer, ok := groupCloser(msg[i]); ok {
			if j := strings.IndexByte(msg[i+1:], closer); j >= 0 {
				end := i + 1 + j
				out = append(out, span{start: start, end: end + 1, text: msg[i+1 : end]})
				i = end + 1
				continue
			}
		}
		for i < len(msg) && !isSpace(msg[i]) {
			i++
		}
		out = append(out, span{start: start, end: i, text: msg[start:i]})
	}
}

func groupCloser(c byte) (byte, bool) {
	switch c {
	case '[':
		return ']', true
	case '"':
		return '"', true
	}
	return 0, false
}

// match applies rule N6 through fieldCountAllows; under the default the
// last pattern field takes the raw text from its first field to the end.
func (p fixedFields) match(message string, o Options) bool {
	ev := splitFields(message)
	n := len(p.fields)
	if !fieldCountAllows(o.FieldCount, len(ev), n) {
		return false
	}
	for i, f := range p.fields {
		text := ev[i].text
		if i == n-1 && len(ev) > n {
			text = message[ev[i].start:ev[len(ev)-1].end]
		}
		if !f.holds(text) {
			return false
		}
	}
	return true
}

// fieldCountAllows is the one place the field-count question is decided.
func fieldCountAllows(c FieldCount, events, fields int) bool {
	switch c {
	case FieldCountLastAbsorbs:
		return events >= fields
	case FieldCountExact:
		return events == fields
	}
	return false
}

func (p spreadFields) match(message string, _ Options) bool {
	ev := splitFields(message)
	if len(ev) < len(p.head)+len(p.tail) {
		return false
	}
	for i, f := range p.head {
		if !f.holds(ev[i].text) {
			return false
		}
	}
	offset := len(ev) - len(p.tail)
	for i, f := range p.tail {
		if !f.holds(ev[offset+i].text) {
			return false
		}
	}
	return true
}
