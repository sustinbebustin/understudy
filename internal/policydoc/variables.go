package policydoc

import "strings"

// expansion is a policy text compiled with its policy variables substituted
// (iam-evaluation.md 7).
type expansion struct {
	// tokens are the text's tokens: the policy's own characters read as
	// compile reads them, and each substituted value as literal characters.
	tokens []token
	// resolved is false when a variable had no value and no default, so the
	// text has no value (iam-evaluation.md 7.3).
	resolved bool
	// unsupplied are the variable names, as the text spells them, whose key
	// the context marks Unsupplied.
	unsupplied []string
}

// expand compiles s, a text of a document of version v, substituting its
// policy variables from ctx. Only a 2012-10-17 document has variables
// (iam-evaluation.md 5.5); in any other, s compiles as written.
//
// A variable is ${name} or ${name, 'default'}, with exactly a comma, one
// space and single quotes; any other spelling is part of the name
// (IAM-EVAL-15, inferred). The name is looked up case-insensitively: a
// present single-valued key gives its value; an absent, unsupplied or
// multivalued key (IAM-EVAL-14, inferred) gives the default when one is
// written, else the text has no value. A value or default is substituted as
// literal characters, so a * or ? it holds is no wildcard (inferred). The
// escapes ${*}, ${?} and ${$} stay literal characters.
func expand(s string, v Version, ctx *Context) expansion {
	if v != Version20121017 {
		return expansion{tokens: compile(s, v), resolved: true}
	}
	out := expansion{resolved: true}
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		if r, ok := escapeAt(rs, i, v); ok {
			out.tokens = append(out.tokens, token{kind: literal, r: r})
			i += 3
			continue
		}
		end := variableEnd(rs, i)
		if end < 0 {
			out.tokens = append(out.tokens, tokenOf(rs[i]))
			continue
		}
		out.substitute(string(rs[i+2:end]), ctx)
		i = end
	}
	return out
}

// variableEnd returns the index of the } that closes a variable opening at
// rs[i], or -1 when no variable opens there.
func variableEnd(rs []rune, i int) int {
	if i+1 >= len(rs) || rs[i] != '$' || rs[i+1] != '{' {
		return -1
	}
	for j := i + 2; j < len(rs); j++ {
		if rs[j] == '}' {
			return j
		}
	}
	return -1
}

// substitute appends the value of the variable spelled spec, the text
// between ${ and }.
func (e *expansion) substitute(spec string, ctx *Context) {
	name, def, hasDefault := spec, "", false
	if n, rest, ok := strings.Cut(spec, ", '"); ok && strings.HasSuffix(rest, "'") {
		name, def, hasDefault = n, strings.TrimSuffix(rest, "'"), true
	}
	value, presence := ctx.Lookup(name)
	switch {
	case presence == Present && !value.Multivalued && len(value.Values) == 1:
		e.literal(value.Values[0])
		return
	case presence == Unsupplied:
		e.unsupplied = append(e.unsupplied, name)
	}
	if hasDefault {
		e.literal(def)
		return
	}
	e.resolved = false
}

func (e *expansion) literal(s string) {
	for _, r := range s {
		e.tokens = append(e.tokens, token{kind: literal, r: r})
	}
}

// text is the expansion as the operators that compare without wildcards read
// it: every token its character, a wildcard as the * or ? it was written as.
func (e *expansion) text() string {
	var b strings.Builder
	for _, t := range e.tokens {
		switch t.kind {
		case literal:
			b.WriteRune(t.r)
		case anyRun:
			b.WriteByte('*')
		case anyOne:
			b.WriteByte('?')
		}
	}
	return b.String()
}
