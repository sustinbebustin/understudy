package logsfilter

// Filter is a compiled filter pattern. Compile it once where the pattern
// enters the API and match it against each event message.
type Filter struct {
	m    matcher
	opts Options
}

// matcher is a compiled pattern of one kind.
//
//sumtype:decl
type matcher interface {
	match(message string, o Options) bool
}

// matchAll is the empty or all-blank pattern, and the empty field list [].
type matchAll struct{}

func (matchAll) match(string, Options) bool { return true }

// Compile compiles a pattern under the default Options.
func Compile(pattern string) (*Filter, *Error) { return Options{}.Compile(pattern) }

// Compile parses a pattern. The first non-blank character picks the kind
// (rule N7): { for JSON, [ for space-delimited, anything else for
// unstructured terms. A pattern AWS would reject returns an Error.
func (o Options) Compile(pattern string) (*Filter, *Error) {
	if err := checkLength(pattern); err != nil {
		return nil, err
	}
	s := &scanner{src: pattern}
	s.skipSpace()
	var (
		m   matcher
		err *Error
	)
	switch {
	case s.eof():
		m = matchAll{}
	case s.peek() == '{':
		m, err = parseJSON(s)
	case s.peek() == '[':
		m, err = parseDelimited(s)
	default:
		m, err = parseText(s)
	}
	if err != nil {
		return nil, err
	}
	return &Filter{m: m, opts: o}, nil
}

// Match reports whether an event message satisfies the filter.
func Match(f *Filter, message string) bool { return f.m.match(message, f.opts) }
