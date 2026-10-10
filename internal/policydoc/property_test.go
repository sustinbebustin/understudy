package policydoc_test

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/policydoc"
)

// genStringOrList draws a string or a list of strings from gen, in the two
// forms the grammar allows for one value.
func genStringOrList(t *rapid.T, label string, gen *rapid.Generator[string]) any {
	if rapid.Bool().Draw(t, label+"-scalar") {
		return gen.Draw(t, label)
	}
	return rapid.SliceOfN(gen, 0, 3).Draw(t, label+"-list")
}

// genDocumentJSON draws a valid policy document as a decoded JSON tree, in
// any of the forms the grammar allows.
func genDocumentJSON(t *rapid.T) map[string]any {
	text := rapid.String()
	doc := map[string]any{}
	if v := rapid.SampledFrom([]string{"", "2008-10-17", "2012-10-17"}).Draw(t, "version"); v != "" {
		doc["Version"] = v
	}
	if rapid.Bool().Draw(t, "has-id") {
		doc["Id"] = text.Draw(t, "id")
	}
	statements := rapid.SliceOfN(rapid.Custom(genStatementJSON), 1, 3).Draw(t, "statements")
	if len(statements) == 1 && rapid.Bool().Draw(t, "single-statement") {
		doc["Statement"] = statements[0]
	} else {
		doc["Statement"] = statements
	}
	return doc
}

func genStatementJSON(t *rapid.T) map[string]any {
	text := rapid.String()
	st := map[string]any{"Effect": rapid.SampledFrom([]string{"Allow", "Deny"}).Draw(t, "effect")}
	if rapid.Bool().Draw(t, "has-sid") {
		st["Sid"] = text.Draw(t, "sid")
	}
	if name := rapid.SampledFrom([]string{"", "Principal", "NotPrincipal"}).Draw(t, "principal-element"); name != "" {
		st[name] = genPrincipalJSON(t)
	}
	if name := rapid.SampledFrom([]string{"", "Action", "NotAction"}).Draw(t, "action-element"); name != "" {
		st[name] = genStringOrList(t, "action", text)
	}
	if name := rapid.SampledFrom([]string{"", "Resource", "NotResource"}).Draw(t, "resource-element"); name != "" {
		st[name] = genStringOrList(t, "resource", text)
	}
	if rapid.Bool().Draw(t, "has-condition") {
		st["Condition"] = genConditionJSON(t)
	}
	return st
}

func genPrincipalJSON(t *rapid.T) any {
	if rapid.Bool().Draw(t, "principal-star") {
		return "*"
	}
	keys := rapid.SliceOfNDistinct(rapid.SampledFrom([]string{"AWS", "CanonicalUser", "Federated", "Service"}), 1, 4, rapid.ID).Draw(t, "principal-keys")
	p := map[string]any{}
	for _, k := range keys {
		if rapid.Bool().Draw(t, "principal-scalar") {
			p[k] = rapid.String().Draw(t, "principal-value")
		} else {
			p[k] = rapid.SliceOfN(rapid.String(), 1, 3).Draw(t, "principal-values")
		}
	}
	return p
}

func genConditionJSON(t *rapid.T) map[string]any {
	operators := []string{
		"StringEquals", "StringNotLike", "ArnLike", "Bool", "NumericLessThan", "DateGreaterThan", "IpAddress", "Null",
		"ForAnyValue:StringLike", "ForAllValues:StringEquals", "StringEqualsIfExists", "ForAnyValue:ArnNotLikeIfExists",
	}
	scalar := rapid.OneOf(
		rapid.Map(rapid.String(), func(s string) any { return s }),
		rapid.Map(rapid.Bool(), func(b bool) any { return b }),
		rapid.Map(rapid.StringMatching(`-?(0|[1-9][0-9]{0,4})(\.[0-9]{1,3})?`), func(n string) any { return json.Number(n) }),
	)
	c := map[string]any{}
	for _, op := range rapid.SliceOfNDistinct(rapid.SampledFrom(operators), 0, 3, rapid.ID).Draw(t, "operators") {
		block := map[string]any{}
		for _, key := range rapid.SliceOfNDistinct(rapid.StringN(1, 8, -1), 0, 3, rapid.ID).Draw(t, "keys") {
			if rapid.Bool().Draw(t, "condition-scalar") {
				block[key] = scalar.Draw(t, "condition-value")
			} else {
				block[key] = rapid.SliceOfN(scalar, 0, 3).Draw(t, "condition-values")
			}
		}
		c[op] = block
	}
	return c
}

func TestParseEncodeParseIsStable(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		in, err := json.Marshal(genDocumentJSON(t))
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		first, perr := policydoc.Parse(in)
		if perr != nil {
			t.Fatalf("Parse(%s): %v", in, perr)
		}
		encoded := policydoc.Encode(first)
		second, perr := policydoc.Parse(encoded)
		if perr != nil {
			t.Fatalf("Parse(Encode(%s)) = %s: %v", in, encoded, perr)
		}
		if !reflect.DeepEqual(first, second) {
			t.Fatalf("Parse(Encode(doc)) differs from doc\ninput:   %s\nencoded: %s\nfirst:   %#v\nsecond:  %#v", in, encoded, first, second)
		}
		if again := policydoc.Encode(second); !bytes.Equal(again, encoded) {
			t.Fatalf("Encode is not idempotent:\n%s\n%s", encoded, again)
		}
	})
}

// Any input Parse accepts, including truncated and random bytes, encodes to
// a document Parse accepts again.
func TestParseAcceptsItsEncoding(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		in := rapid.OneOf(
			rapid.SliceOf(rapid.Byte()),
			rapid.Map(rapid.Custom(genDocumentJSON), func(doc map[string]any) []byte {
				b, err := json.Marshal(doc)
				if err != nil {
					t.Fatalf("marshal: %v", err)
				}
				return b
			}),
		).Draw(t, "input")
		if cut := rapid.IntRange(0, len(in)).Draw(t, "cut"); cut < len(in) {
			in = in[:cut]
		}
		doc, err := policydoc.Parse(in)
		if err != nil {
			return
		}
		if _, err := policydoc.Parse(policydoc.Encode(doc)); err != nil {
			t.Fatalf("Parse accepted %q but not its encoding %s: %v", in, policydoc.Encode(doc), err)
		}
	})
}
