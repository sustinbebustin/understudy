package awsquery_test

import (
	"bytes"
	"encoding/base64"
	"math"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/protocol/awsquery"
	"github.com/sustinbebustin/understudy/internal/protocol/form"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/protocol/xmlw"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// Year 1 to 9999: the range date-time and http-date can print.
const (
	minMillis = -62135596800000
	maxMillis = 253402300799999
)

// drawing says what a wire can carry back unchanged: XML text cannot hold
// most C0 control characters, and a response cannot tell an empty
// flattened list from an absent one.
type drawing struct {
	xml   bool
	depth int
}

func (o drawing) str(t *rapid.T, label string) string {
	if !o.xml {
		return rapid.String().Draw(t, label)
	}
	return rapid.StringOf(rapid.Rune().Filter(func(r rune) bool {
		return r == '\t' || r == '\n' || r == '\r' || !unicode.IsControl(r) || r >= 0x7F
	}).Filter(func(r rune) bool { return r != 0xFFFE && r != 0xFFFF })).Draw(t, label)
}

// drawStruct draws a value of s with at least one member set: an SDK sends
// nothing for an empty structure, so it would not come back.
func (o drawing) drawStruct(t *rapid.T, s *smithy.Schema, label string) dynStruct {
	v := dynStruct{schema: s, fields: map[string]any{}}
	for _, m := range s.Members {
		if m.Target == s && o.depth == 0 {
			continue
		}
		if rapid.Bool().Draw(t, label+"."+m.Name+"?") {
			v.fields[m.Name] = o.drawValue(t, m, true, label+"."+m.Name)
		}
	}
	if len(v.fields) == 0 {
		first := s.Members[0]
		v.fields[first.Name] = o.drawValue(t, first, true, label+"."+first.Name)
	}
	return v
}

func (o drawing) drawValue(t *rapid.T, m *smithy.Member, inStruct bool, label string) any {
	switch m.Target.Type {
	case smithy.TypeString:
		return o.str(t, label)
	case smithy.TypeBoolean:
		return rapid.Bool().Draw(t, label)
	case smithy.TypeInteger:
		return rapid.Int32().Draw(t, label)
	case smithy.TypeLong:
		return rapid.Int64().Draw(t, label)
	case smithy.TypeDouble:
		return rapid.OneOf(rapid.Float64(), rapid.SampledFrom([]float64{math.NaN(), math.Inf(1), math.Inf(-1), 0, math.MaxFloat64})).Draw(t, label)
	case smithy.TypeTimestamp:
		ms := rapid.Int64Range(minMillis, maxMillis).Draw(t, label)
		if m.TimestampFormat == smithy.HTTPDate {
			ms -= ms % 1000
		}
		return time.UnixMilli(ms).UTC()
	case smithy.TypeBlob:
		return rapid.SliceOf(rapid.Byte()).Draw(t, label)
	case smithy.TypeStructure:
		return drawing{xml: o.xml, depth: o.depth - 1}.drawStruct(t, m.Target, label)
	case smithy.TypeUnion:
		um := rapid.SampledFrom(m.Target.Members).Draw(t, label+".member")
		return dynUnion{member: um.Name, value: o.drawValue(t, um, true, label+"."+um.Name)}
	case smithy.TypeList:
		// Requests draw up to 11 items, so member.10 and member.11 arrive
		// before member.2 in a string-sorted order.
		minLen, maxLen := 0, 11
		if o.xml {
			maxLen = 3
			if inStruct && m.XMLFlattened {
				minLen = 1
			}
		}
		out := []any{}
		for i := range rapid.IntRange(minLen, maxLen).Draw(t, label+".len") {
			out = append(out, o.drawValue(t, m.Target.Members[0], false, label+"["+strconv.Itoa(i)+"]"))
		}
		return out
	case smithy.TypeMap:
		// An SDK sends nothing for an empty map, so draw at least one entry.
		keys := rapid.SliceOfNDistinct(rapid.Custom(func(t *rapid.T) string { return o.str(t, "key") }), 1, 3, rapid.ID[string]).Draw(t, label+".keys")
		out := map[string]any{}
		for _, k := range keys {
			out[k] = o.drawValue(t, m.Target.Members[1], false, label+"."+k)
		}
		return out
	case smithy.TypeEnum, smithy.TypeIntEnum, smithy.TypeByte, smithy.TypeShort, smithy.TypeFloat,
		smithy.TypeBigInteger, smithy.TypeBigDecimal, smithy.TypeDocument, smithy.TypeUnit:
	}
	t.Fatalf("no drawing for %s", m.Target.Type)
	return nil
}

func sinkService() (*registry.Service, *smithy.Schema) {
	sink := kitchenSink()
	return newService("sink", "2020-01-08", "https://example.com/doc/", opSpec{name: "Sink", in: sink, out: sink}), sink
}

// wire-json-query.md Implication 10: a request an SDK encodes by Smithy's
// key rules decodes to the value it encodes, in any pair order and with
// spaces as %20 or '+'.
func TestPropertyRequestRoundTrip(t *testing.T) {
	t.Parallel()
	svc, sink := sinkService()
	reg := newRegistry(t, svc)
	rapid.Check(t, func(t *rapid.T) {
		want := drawing{depth: 2}.drawStruct(t, sink, "in")
		pairs := append(encodeForm("", want), form.Pair{Key: "Action", Value: "Sink"}, form.Pair{Key: "Version", Value: "2020-01-08"})
		body := form.Encode(rapid.Permutation(pairs).Draw(t, "order"))
		if rapid.Bool().Draw(t, "plus") {
			body = strings.ReplaceAll(body, "%20", "+")
		}
		r := formPost("http://localhost/", body)
		claim, ok := awsquery.Codec{}.Claim(r, reg).(awsquery.Claimed)
		if !ok {
			t.Fatalf("the request was not claimed: %s", body)
		}
		got := dynStruct{schema: sink}
		if aerr := (awsquery.Codec{}).Decode(r, claim.Op, &got); aerr != nil {
			t.Fatalf("Decode(%s) = %s %q", body, aerr.ID.Shape, aerr.Message)
		}
		if !equalFields(got.fields, want.fields) {
			t.Fatalf("round trip of %s\n got %#v\nwant %#v", body, normalize(got.fields), normalize(want.fields))
		}
	})
}

// A response read back by Smithy's XML rules (wire-json-query.md 8.3), as
// an SDK reads it, is the value the handler returned.
func TestPropertyResponseRoundTrip(t *testing.T) {
	t.Parallel()
	svc, sink := sinkService()
	op := svc.Operations[0]
	rapid.Check(t, func(t *rapid.T) {
		want := drawing{xml: true, depth: 2}.drawStruct(t, sink, "out")
		resp, aerr := awsquery.Codec{}.Encode(op, requestID, &want, http.StatusOK)
		if aerr != nil {
			t.Fatalf("Encode: %v", aerr.AsError())
		}
		root, err := xmlw.Decode(bytes.NewReader(resp.Body), int64(len(resp.Body)))
		if err != nil {
			t.Fatalf("the response is not XML: %v\n%s", err, resp.Body)
		}
		result, ok := root.Child("SinkResult")
		if !ok || root.Name != "SinkResponse" {
			t.Fatalf("no SinkResponse/SinkResult in %s", resp.Body)
		}
		got := xmlStruct(t, result, sink)
		if !equalFields(got.fields, want.fields) {
			t.Fatalf("round trip of %s\n got %#v\nwant %#v", resp.Body, normalize(got.fields), normalize(want.fields))
		}
	})
}

// Decode answers any bytes with a value or an AWS-shaped error, never a
// panic or an Internal error.
func TestPropertyDecodeNeverPanics(t *testing.T) {
	t.Parallel()
	svc, sink := sinkService()
	op := svc.Operations[0]
	keys := []string{"String", "Integer", "List.member.1", "Flat.2", "Map.entry.1.key", "Map.entry.1.value", "Attribute.1.Name", "Attribute.1.Value.Id", "Choice.Text", "Choice.Number", "Nested.Nested.Boolean", "Epoch", "Blob", "MapOfLists.entry.1.value.member.1"}
	rapid.Check(t, func(t *rapid.T) {
		body := rapid.OneOf(
			rapid.String(),
			rapid.Custom(func(t *rapid.T) string {
				n := rapid.IntRange(0, 8).Draw(t, "n")
				pairs := make([]string, 0, n)
				for range n {
					pairs = append(pairs, rapid.SampledFrom(keys).Draw(t, "key")+"="+rapid.StringOf(rapid.SampledFrom([]rune("1a.%=&+-_xT:Z"))).Draw(t, "value"))
				}
				return strings.Join(pairs, "&")
			}),
		).Draw(t, "body")
		got := dynStruct{schema: sink}
		aerr := awsquery.Codec{}.Decode(formPost("http://localhost/", body), op, &got)
		if aerr != nil && aerr.ID.Status >= 500 {
			t.Fatalf("Decode(%q) = %s %q, want a client error", body, aerr.ID.Shape, aerr.Message)
		}
	})
}

// xmlStruct reads a structure from XML by Smithy's rules, independently of
// the codec.
func xmlStruct(t *rapid.T, n *xmlw.Node, s *smithy.Schema) dynStruct {
	out := dynStruct{schema: s, fields: map[string]any{}}
	for _, m := range s.Members {
		name := keyOf(m, m.Name)
		if m.XMLFlattened && (m.Target.Type == smithy.TypeList || m.Target.Type == smithy.TypeMap) {
			var items []*xmlw.Node
			for c := range n.ChildrenNamed(name) {
				items = append(items, c)
			}
			if len(items) > 0 {
				out.fields[m.Name] = xmlCollection(t, m, items)
			}
			continue
		}
		if c, ok := n.Child(name); ok {
			out.fields[m.Name] = xmlValue(t, c, m)
		}
	}
	return out
}

func xmlValue(t *rapid.T, n *xmlw.Node, m *smithy.Member) any {
	switch m.Target.Type {
	case smithy.TypeStructure:
		return xmlStruct(t, n, m.Target)
	case smithy.TypeUnion:
		if len(n.Children) != 1 {
			t.Fatalf("union %s has %d members", m.Name, len(n.Children))
		}
		um := memberNamed(m.Target, n.Children[0].Name)
		return dynUnion{member: um.Name, value: xmlValue(t, n.Children[0], um)}
	case smithy.TypeList:
		var items []*xmlw.Node
		for c := range n.ChildrenNamed(keyOf(m.Target.Members[0], "member")) {
			items = append(items, c)
		}
		return xmlCollection(t, m, items)
	case smithy.TypeMap:
		var items []*xmlw.Node
		for c := range n.ChildrenNamed("entry") {
			items = append(items, c)
		}
		return xmlCollection(t, m, items)
	case smithy.TypeString, smithy.TypeEnum, smithy.TypeIntEnum, smithy.TypeBlob, smithy.TypeBoolean, smithy.TypeByte,
		smithy.TypeShort, smithy.TypeInteger, smithy.TypeLong, smithy.TypeFloat, smithy.TypeDouble, smithy.TypeBigInteger,
		smithy.TypeBigDecimal, smithy.TypeTimestamp, smithy.TypeDocument, smithy.TypeUnit:
	}
	return xmlScalar(t, n.Text, m)
}

// xmlCollection reads list items or map entries.
func xmlCollection(t *rapid.T, m *smithy.Member, items []*xmlw.Node) any {
	if m.Target.Type == smithy.TypeList {
		out := make([]any, 0, len(items))
		for _, item := range items {
			out = append(out, xmlValue(t, item, m.Target.Members[0]))
		}
		return out
	}
	km, vm := m.Target.Members[0], m.Target.Members[1]
	out := map[string]any{}
	for _, entry := range items {
		k, kok := entry.Child(keyOf(km, "key"))
		v, vok := entry.Child(keyOf(vm, "value"))
		if !kok || !vok || len(entry.Children) != 2 {
			t.Fatalf("map entry of %s is not exactly a key and a value", m.Name)
		}
		out[k.Text] = xmlValue(t, v, vm)
	}
	return out
}

func xmlScalar(t *rapid.T, text string, m *smithy.Member) any {
	fail := func(err error) {
		if err != nil {
			t.Fatalf("%s: %q: %v", m.Name, text, err)
		}
	}
	switch m.Target.Type {
	case smithy.TypeString:
		return text
	case smithy.TypeBoolean:
		if text != "true" && text != "false" {
			t.Fatalf("%s: boolean %q is not lowercase true or false", m.Name, text)
		}
		return text == "true"
	case smithy.TypeInteger:
		v, err := strconv.ParseInt(text, 10, 32)
		fail(err)
		return int32(v)
	case smithy.TypeLong:
		v, err := strconv.ParseInt(text, 10, 64)
		fail(err)
		return v
	case smithy.TypeDouble:
		switch text {
		case "NaN":
			return math.NaN()
		case "Infinity":
			return math.Inf(1)
		case "-Infinity":
			return math.Inf(-1)
		}
		v, err := strconv.ParseFloat(text, 64)
		fail(err)
		return v
	case smithy.TypeTimestamp:
		return xmlTimestamp(t, text, m)
	case smithy.TypeBlob:
		v, err := base64.StdEncoding.DecodeString(text)
		fail(err)
		return v
	case smithy.TypeStructure, smithy.TypeUnion, smithy.TypeList, smithy.TypeMap, smithy.TypeEnum, smithy.TypeIntEnum,
		smithy.TypeByte, smithy.TypeShort, smithy.TypeFloat, smithy.TypeBigInteger, smithy.TypeBigDecimal, smithy.TypeDocument, smithy.TypeUnit:
	}
	t.Fatalf("no XML reading for %s", m.Target.Type)
	return nil
}

func xmlTimestamp(t *rapid.T, text string, m *smithy.Member) time.Time {
	var v time.Time
	var err error
	switch m.TimestampFormat {
	case smithy.EpochSeconds:
		var f float64
		f, err = strconv.ParseFloat(text, 64)
		v = time.UnixMilli(int64(math.Round(f * 1000)))
	case smithy.HTTPDate:
		v, err = http.ParseTime(text)
	case smithy.DateTime, smithy.TimestampProtocolDefault:
		if !strings.HasSuffix(text, "Z") || strings.Count(text, ".") > 1 {
			t.Fatalf("%s: date-time %q is not UTC with at most a millisecond fraction", m.Name, text)
		}
		v, err = time.Parse(time.RFC3339Nano, text)
	}
	if err != nil {
		t.Fatalf("%s: %q: %v", m.Name, text, err)
	}
	return v
}
