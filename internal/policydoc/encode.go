package policydoc

import (
	"slices"
	"strconv"
	"unicode/utf8"
)

// Encode writes doc as compact JSON in canonical form: elements in the order
// IAM documents them (Version, Id, Statement; Sid, Effect, Principal, Action,
// Resource, Condition), principal types and condition operators and keys in
// sort order, Statement always a list, and every one-element list of
// actions, resources, principals or condition values written as its single
// value, as Lambda and KMS write their policies. Parse of the output returns
// doc unchanged for any doc Parse returned.
func Encode(doc Document) []byte {
	w := encoder{b: []byte{'{'}}
	first := true
	if doc.Version != VersionAbsent {
		w.key(&first, "Version")
		w.str(doc.Version.String())
	}
	if doc.ID != "" {
		w.key(&first, "Id")
		w.str(doc.ID)
	}
	w.key(&first, "Statement")
	w.raw("[")
	for i, st := range doc.Statements {
		if i > 0 {
			w.raw(",")
		}
		w.statement(st)
	}
	w.raw("]}")
	return w.b
}

type encoder struct{ b []byte }

func (w *encoder) raw(s string) { w.b = append(w.b, s...) }

func (w *encoder) statement(st Statement) {
	w.raw("{")
	first := true
	if st.Sid != "" {
		w.key(&first, "Sid")
		w.str(st.Sid)
	}
	w.key(&first, "Effect")
	w.str(st.Effect.String())
	switch p := st.Principal.(type) {
	case nil:
	case Principal:
		w.key(&first, "Principal")
		w.who(p.Who)
	case NotPrincipal:
		w.key(&first, "NotPrincipal")
		w.who(p.Who)
	}
	switch a := st.Action.(type) {
	case nil:
	case Action:
		w.key(&first, "Action")
		w.strings(a)
	case NotAction:
		w.key(&first, "NotAction")
		w.strings(a)
	}
	switch r := st.Resource.(type) {
	case nil:
	case Resource:
		w.key(&first, "Resource")
		w.strings(r)
	case NotResource:
		w.key(&first, "NotResource")
		w.strings(r)
	}
	if len(st.Condition) > 0 {
		w.key(&first, "Condition")
		w.condition(st.Condition)
	}
	w.raw("}")
}

func (w *encoder) who(who Who) {
	switch x := who.(type) {
	case nil:
		// Names nobody, as in Allows; Parse rejects it rather than reading a
		// grant to everyone.
		w.raw("{}")
	case Anyone:
		w.str("*")
	case Named:
		w.raw("{")
		first := true
		for _, f := range []struct {
			key    string
			values []string
		}{{"AWS", x.AWS}, {"CanonicalUser", x.CanonicalUser}, {"Federated", x.Federated}, {"Service", x.Service}} {
			if len(f.values) > 0 {
				w.key(&first, f.key)
				w.strings(f.values)
			}
		}
		w.raw("}")
	}
}

// condition writes the clauses grouped by operator, in sort order.
func (w *encoder) condition(c Condition) {
	c = slices.SortedStableFunc(slices.Values(c), compareClauses)
	w.raw("{")
	for i, cl := range c {
		switch {
		case i == 0:
		case c[i-1].Operator != cl.Operator:
			w.raw("},")
		default:
			w.raw(",")
		}
		if i == 0 || c[i-1].Operator != cl.Operator {
			w.str(cl.Operator.String())
			w.raw(":{")
		}
		w.str(cl.Key)
		w.raw(":")
		if len(cl.Values) == 1 {
			w.value(cl.Values[0])
			continue
		}
		w.raw("[")
		for j, v := range cl.Values {
			if j > 0 {
				w.raw(",")
			}
			w.value(v)
		}
		w.raw("]")
	}
	w.raw("}}")
}

func (w *encoder) value(v Value) {
	switch x := v.(type) {
	case nil:
		w.raw("null")
	case StringValue:
		w.str(string(x))
	case BoolValue:
		w.b = strconv.AppendBool(w.b, bool(x))
	case NumberValue:
		w.raw(string(x))
	}
}

// strings writes one value as a string and any other count as a list.
func (w *encoder) strings(values []string) {
	if len(values) == 1 {
		w.str(values[0])
		return
	}
	w.raw("[")
	for i, s := range values {
		if i > 0 {
			w.raw(",")
		}
		w.str(s)
	}
	w.raw("]")
}

func (w *encoder) key(first *bool, k string) {
	if !*first {
		w.raw(",")
	}
	*first = false
	w.str(k)
	w.raw(":")
}

// str writes s as a JSON string, escaping only what JSON requires; invalid
// UTF-8 becomes U+FFFD, as encoding/json writes it.
func (w *encoder) str(s string) {
	const hex = "0123456789abcdef"
	w.b = append(w.b, '"')
	for _, r := range s {
		switch {
		case r == '"' || r == '\\':
			w.b = append(w.b, '\\', byte(r))
		case r < 0x20:
			w.b = append(w.b, '\\', 'u', '0', '0', hex[r>>4], hex[r&0xf])
		case r == utf8.RuneError:
			w.raw(`�`)
		default:
			w.b = utf8.AppendRune(w.b, r)
		}
	}
	w.b = append(w.b, '"')
}
