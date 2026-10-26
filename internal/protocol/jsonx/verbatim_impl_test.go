package jsonx_test

import (
	"bytes"
	"encoding/json/v2"
	"errors"
	"strconv"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/protocol/jsonx"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
)

// orderedDoc is an ordered document: its member order is part of the value.
type orderedDoc document.Map

func (orderedDoc) JSONVerbatim() {}

func (d orderedDoc) MarshalJSON() ([]byte, error) {
	e := jsonx.NewEncoder(jsonx.WireOrder)
	e.Value(document.Map(d))
	return e.Bytes()
}

// holder keeps an ordered document beside fields the codec sorts.
type holder struct {
	Z   int        `json:"z"`
	Doc orderedDoc `json:"doc"`
	A   int        `json:"a"`
}

// tagged is a sum whose variant carries an ordered document.
type tagged interface{ isTagged() }

type withDoc struct {
	Z   int        `json:"z"`
	Doc orderedDoc `json:"doc,omitzero"`
	A   int        `json:"a"`
}

func (withDoc) isTagged() {}

type taggedHolder struct {
	One  tagged   `json:"one"`
	Many []tagged `json:"many"`
}

func taggedCodec(t *testing.T) jsonx.Codec {
	t.Helper()
	u, err := jsonx.NewUnion[tagged]("tagged", jsonx.Variant[tagged, withDoc]("with-doc"))
	if err != nil {
		t.Fatal(err)
	}
	return jsonx.NewCodec(u)
}

// doc is {"b":1,"a":{"y":true,"x":[{"d":1,"c":2}]}}: members out of name
// order at every depth.
func doc() orderedDoc {
	return orderedDoc{
		{Key: "b", Value: document.Number("1")},
		{Key: "a", Value: document.Map{
			{Key: "y", Value: document.Bool(true)},
			{Key: "x", Value: document.List{document.Map{{Key: "d", Value: document.Number("1")}, {Key: "c", Value: document.Number("2")}}}},
		}},
	}
}

const docJSON = `{"b":1,"a":{"y":true,"x":[{"d":1,"c":2}]}}`

// A Verbatim value keeps its members in the order it wrote them, at every
// depth inside it, while the objects around it are sorted as before.
func TestVerbatimKeepsItsOrder(t *testing.T) {
	got, err := jsonx.Marshal(holder{Z: 1, Doc: doc(), A: 2})
	if want := `{"a":2,"doc":` + docJSON + `,"z":1}`; err != nil || string(got) != want {
		t.Errorf("Marshal = %s, %v; want %s", got, err, want)
	}
}

// Inside a union variant, held through the sum or in a list of it, the
// document keeps its order too: the variant's fields are sorted, the
// document is not.
func TestVerbatimInsideAVariant(t *testing.T) {
	v := taggedHolder{One: withDoc{Z: 1, Doc: doc(), A: 2}, Many: []tagged{withDoc{Z: 3, A: 4}, withDoc{Doc: doc()}}}
	got, err := taggedCodec(t).Marshal(v)
	want := `{"many":[{"a":4,"kind":"with-doc","z":3},{"a":0,"doc":` + docJSON + `,"kind":"with-doc","z":0}],` +
		`"one":{"a":2,"doc":` + docJSON + `,"kind":"with-doc","z":1}}`
	if err != nil || string(got) != want {
		t.Errorf("Marshal = %s, %v\nwant %s", got, err, want)
	}
}

// Whatever names the document holds, Marshal writes them in the order
// given; the same value gives the same bytes.
func TestVerbatimOrderProperty(t *testing.T) {
	c := taggedCodec(t)
	rapid.Check(t, func(t *rapid.T) {
		names := rapid.SliceOfNDistinct(rapid.StringMatching(`[a-z]{1,4}`), 1, 5, func(s string) string { return s }).Draw(t, "names")
		d := make(orderedDoc, len(names))
		parts := make([]string, len(names))
		for i, n := range names {
			d[i] = document.Entry{Key: n, Value: document.Number(strconv.Itoa(i))}
			parts[i] = strconv.Quote(n) + ":" + strconv.Itoa(i)
		}
		want := `{` + strings.Join(parts, ",") + `}`
		for _, v := range []any{holder{Doc: d}, taggedHolder{One: withDoc{Doc: d}}} {
			first, err := c.Marshal(v)
			if err != nil {
				t.Fatal(err)
			}
			again, err := c.Marshal(v)
			if err != nil || !bytes.Equal(again, first) {
				t.Fatalf("Marshal twice gave %s and %s, %v", first, again, err)
			}
			if !strings.Contains(string(first), `"doc":`+want) {
				t.Fatalf("Marshal(%T) = %s, want the document %s as given", v, first, want)
			}
		}
	})
}

// An error decoding a variant's field names it from the document's root,
// not from the variant: the variant is decoded from a document of its own.
func TestVariantErrorsNameTheWholePath(t *testing.T) {
	c := testCodec(t)
	var m message
	err := c.Unmarshal([]byte(`{"id":"m","state":{"kind":"delayed","until":"soon"}}`), &m)
	sem, ok := errors.AsType[*json.SemanticError](err)
	if !ok || sem.JSONPointer != "/state/until" {
		t.Errorf("Unmarshal = %v; want a SemanticError at /state/until", err)
	}
	err = c.Unmarshal([]byte(`{"id":"m","history":[{"kind":"visible"},{"kind":"delayed","until":"soon"}]}`), &m)
	if sem, ok := errors.AsType[*json.SemanticError](err); !ok || sem.JSONPointer != "/history/1/until" {
		t.Errorf("Unmarshal = %v; want a SemanticError at /history/1/until", err)
	}
}
