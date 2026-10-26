package protocoltest_test

import (
	"encoding/base64"
	"reflect"
	"testing"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/codegen/serdetest"
	"github.com/sustinbebustin/understudy/internal/protocol/cbor"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/protocoltest/gen/restjson"
	"github.com/sustinbebustin/understudy/internal/protocoltest/gen/rpcv2protocol"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// drawSetMap draws a SparseSetMap: each value null (nil), empty (a non-nil
// empty slice) or a set of strings. Null and empty are different values
// (smithy-protocol-tests.md 13.1), as cases 12 to 15 assert side by side.
func drawSetMap(t *rapid.T) map[string][]string {
	keys := drawKeys(t, 4, "keys")
	m := make(map[string][]string, len(keys))
	for _, k := range keys {
		switch rapid.IntRange(0, 2).Draw(t, k+".kind") {
		case 0:
			m[k] = nil
		case 1:
			m[k] = []string{}
		default:
			set := rapid.SliceOfNDistinct(rapid.StringMatching(`[a-z]{1,3}`), 1, 3, func(s string) string { return s }).Draw(t, k)
			m[k] = set
		}
	}
	return m
}

// setMapWire is m as the wire carries it: a null for a nil set, an array
// otherwise (smithy-protocol-tests.md 13.2). undefined spells the null as
// CBOR undefined instead, which a server reads as null but never writes
// (13.1).
func setMapWire(m map[string][]string, undefined bool) wObject {
	out := make(wObject, 0, len(m))
	for k, set := range m {
		if set == nil {
			var null wire = wNull{}
			if undefined {
				null = wUndefined{}
			}
			out = append(out, wEntry{k, null})
			continue
		}
		l := make(wList, len(set))
		for i, s := range set {
			l[i] = wString(s)
		}
		out = append(out, wEntry{k, l})
	}
	return out
}

// sparseProtocol is the sparse-maps operation of one protocol's corpus
// service, whose input and output are one shape with member sparseSetMap
// (smithy-protocol-tests.md 13.2).
type sparseProtocol struct {
	wireProtocol
	op    *registry.Operation
	value reflect.Type
}

func sparseProtocols() []sparseProtocol {
	return []sparseProtocol{
		{restJSON1(), restjson.OpSparseJsonMaps.Descriptor(), reflect.TypeFor[restjson.SparseJsonMapsInputOutput]()},
		{rpcv2CBOR(), rpcv2protocol.OpRpcV2CborSparseMaps.Descriptor(), reflect.TypeFor[rpcv2protocol.RpcV2CborSparseMapsInputOutput]()},
	}
}

// A server writes a nil set of a sparse map as null and an empty one as
// [], and reads them back apart: null as nil, [] as a non-nil empty set
// (smithy-protocol-tests.md 13.2 cases 12 to 15, 14.8). CBOR null is f6,
// never undefined (13.1), which the structural comparison tells apart.
func TestSparseSetMapKeepsNullApartFromEmpty(t *testing.T) {
	for _, p := range sparseProtocols() {
		t.Run(p.name, func(t *testing.T) {
			rapid.Check(t, func(t *rapid.T) {
				m := drawSetMap(t)
				v, out, _ := newValue(t, p.value)
				v.Elem().FieldByName("SparseSetMap").Set(reflect.ValueOf(m))
				got := p.encode(t, p.op, out)
				p.compare(t, p.body(t, wObject{{"sparseSetMap", setMapWire(m, false)}}), got)

				back, _, in := newValue(t, p.value)
				p.decode(t, p.op, got, in)
				if err := diffInput(v.Interface(), back.Interface()); err != nil {
					t.Fatalf("decoding %q: %v", got, err)
				}
			})
		})
	}
}

// A server reads CBOR undefined in a sparse map as null
// (smithy-protocol-tests.md 13.1: servers "MUST deserialize this CBOR value
// to null").
func TestRpcV2CborReadsUndefinedAsNull(t *testing.T) {
	p := rpcv2CBOR()
	op := rpcv2protocol.OpRpcV2CborSparseMaps.Descriptor()
	rapid.Check(t, func(t *rapid.T) {
		m := drawSetMap(t)
		body, err := cbor.Encode(cborOf(t, wObject{{"sparseSetMap", setMapWire(m, true)}}))
		if err != nil {
			t.Fatal(err)
		}
		var got rpcv2protocol.RpcV2CborSparseMapsInputOutput
		p.decode(t, op, body, &got)
		if err := diffInput(&rpcv2protocol.RpcV2CborSparseMapsInputOutput{SparseSetMap: m}, &got); err != nil {
			t.Fatalf("decoding %s: %v", base64.StdEncoding.EncodeToString(body), err)
		}
	})
}

// sparseValue is a generated shape with a sparse map of sets, built from m.
type sparseValue struct {
	name  string
	build func(m map[string][]string) (smithy.Serializable, func() (smithy.Deserializable, func() map[string][]string))
}

func sparseValues() []sparseValue {
	return []sparseValue{
		{"restjson", func(m map[string][]string) (smithy.Serializable, func() (smithy.Deserializable, func() map[string][]string)) {
			return &restjson.SparseJsonMapsInputOutput{SparseSetMap: m}, func() (smithy.Deserializable, func() map[string][]string) {
				v := &restjson.SparseJsonMapsInputOutput{}
				return v, func() map[string][]string { return v.SparseSetMap }
			}
		}},
		{"rpcv2protocol", func(m map[string][]string) (smithy.Serializable, func() (smithy.Deserializable, func() map[string][]string)) {
			return &rpcv2protocol.RpcV2CborSparseMapsInputOutput{SparseSetMap: m}, func() (smithy.Deserializable, func() map[string][]string) {
				v := &rpcv2protocol.RpcV2CborSparseMapsInputOutput{}
				return v, func() map[string][]string { return v.SparseSetMap }
			}
		}},
	}
}

// The generated serde of a sparse map of sets, with no codec in between:
// Serialize writes WriteNull for each nil set and a list for every other,
// and Deserialize reads the recording back to the same map, nil and empty
// sets apart (smithy-protocol-tests.md 14.8).
func TestSparseSetMapSerdeWritesAndReadsNull(t *testing.T) {
	for _, sv := range sparseValues() {
		t.Run(sv.name, func(t *testing.T) {
			rapid.Check(t, func(t *rapid.T) {
				m := drawSetMap(t)
				out, fresh := sv.build(m)
				var rec serdetest.Recorder
				out.Serialize(&rec)
				nulls, lists := 0, 0
				for _, e := range rec.Events {
					if e.Op == serdetest.OpWriteNull {
						nulls++
					}
					if e.Op == serdetest.OpWriteList {
						lists++
					}
				}
				wantNulls := 0
				for _, set := range m {
					if set == nil {
						wantNulls++
					}
				}
				if nulls != wantNulls || lists != len(m)-wantNulls {
					t.Fatalf("Serialize of %v wrote %d nulls and %d lists, want %d and %d:\n%v", m, nulls, lists, wantNulls, len(m)-wantNulls, rec.Strings())
				}
				in, read := fresh()
				p := serdetest.NewReplayer(rec.Events)
				if err := in.Deserialize(p); err != nil {
					t.Fatalf("Deserialize of %v: %v", rec.Strings(), err)
				}
				if err := p.Done(); err != nil {
					t.Fatal(err)
				}
				if err := diffInput(m, read()); err != nil {
					t.Fatalf("read back %v: %v", read(), err)
				}
			})
		})
	}
}

// nullFirstElement replaces the first list element inside the collection
// member named member, from its WriteList to the matching CloseList, with a
// WriteNull of the same member: the events a null element would produce.
func nullFirstElement(t fataler, events []serdetest.Event, member string) []serdetest.Event {
	t.Helper()
	start := -1
	for i, e := range events {
		if (e.Op == serdetest.OpWriteMap || e.Op == serdetest.OpWriteList) && e.Member != nil && e.Member.Name == member {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatalf("no collection member %s in %v", member, events)
	}
	open := -1
	for i := start + 1; i < len(events); i++ {
		if events[i].Op == serdetest.OpWriteList {
			open = i
			break
		}
	}
	if open < 0 {
		t.Fatalf("collection member %s holds no list element", member)
	}
	depth := 0
	for i := open; i < len(events); i++ {
		if events[i].Op == serdetest.OpWriteList {
			depth++
		}
		if events[i].Op == serdetest.OpCloseList {
			depth--
		}
		if depth == 0 {
			out := append([]serdetest.Event{}, events[:open]...)
			out = append(out, serdetest.Event{Op: serdetest.OpWriteNull, Member: events[open].Member})
			return append(out, events[i+1:]...)
		}
	}
	t.Fatalf("the element of %s is never closed", member)
	return nil
}

// A null element of a dense collection is a decode error, never a silent
// drop (smithy-protocol-tests.md 13.1, 14.8, 14.14): a dense map of sets
// and a dense list of lists refuse it, while the same null in a sparse map
// of sets reads as nil.
func TestDenseCollectionNullIsADecodeError(t *testing.T) {
	type dense struct {
		name   string
		value  smithy.Serializable
		member string
		fresh  func() smithy.Deserializable
	}
	for _, c := range []dense{
		{
			"restjson DenseSetMap", &restjson.JsonMapsInputOutput{DenseSetMap: map[string][]string{"x": {"a"}}}, "denseSetMap",
			func() smithy.Deserializable { return &restjson.JsonMapsInputOutput{} },
		},
		{
			"restjson NestedStringList", &restjson.JsonListsInputOutput{NestedStringList: [][]string{{"a"}}}, "nestedStringList",
			func() smithy.Deserializable { return &restjson.JsonListsInputOutput{} },
		},
		{
			"rpcv2protocol DenseSetMap", &rpcv2protocol.RpcV2CborDenseMapsInputOutput{DenseSetMap: map[string][]string{"x": {"a"}}}, "denseSetMap",
			func() smithy.Deserializable { return &rpcv2protocol.RpcV2CborDenseMapsInputOutput{} },
		},
		{
			"rpcv2protocol NestedStringList", &rpcv2protocol.RpcV2CborListInputOutput{NestedStringList: [][]string{{"a"}}}, "nestedStringList",
			func() smithy.Deserializable { return &rpcv2protocol.RpcV2CborListInputOutput{} },
		},
	} {
		var rec serdetest.Recorder
		c.value.Serialize(&rec)
		events := nullFirstElement(t, rec.Events, c.member)
		if err := c.fresh().Deserialize(serdetest.NewReplayer(events)); err == nil {
			t.Errorf("%s: Deserialize of a null element succeeded; want a decode error:\n%v", c.name, (&serdetest.Recorder{Events: events}).Strings())
		}
	}
	for _, sv := range sparseValues() {
		out, fresh := sv.build(map[string][]string{"x": {"a"}})
		var rec serdetest.Recorder
		out.Serialize(&rec)
		events := nullFirstElement(t, rec.Events, "sparseSetMap")
		in, read := fresh()
		if err := in.Deserialize(serdetest.NewReplayer(events)); err != nil {
			t.Errorf("%s: Deserialize of a null sparse set = %v, want it read as nil", sv.name, err)
			continue
		}
		if got := read(); len(got) != 1 || got["x"] != nil {
			t.Errorf("%s: a null sparse set read as %#v, want {x: nil}", sv.name, got)
		}
	}
}
