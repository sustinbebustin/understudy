package jsonx

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"strconv"

	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
)

// Codec marshals Go values for persistence: snapshot sections, fault rules,
// configuration. Its Marshal and Unmarshal have the signatures of
// store.Codec. Values of the sums it was built with use the tagged-union
// convention.
type Codec struct {
	sums []Sum
}

// NewCodec returns a Codec that writes and reads the given sums in the
// tagged-union convention.
func NewCodec(sums ...Sum) Codec {
	return Codec{sums: sums}
}

// Verbatim is a value whose JSON form Codec.Marshal keeps as its
// MarshalJSON writes it, instead of sorting the members of its objects: a
// value whose member order is part of the value, such as an ordered
// document, or one whose MarshalJSON already writes its own canonical
// form. Its type implements both methods with value receivers. A field
// holding one takes omitzero, not omitempty, since omitempty reads the
// marshaled value.
type Verbatim interface {
	MarshalJSON() ([]byte, error)
	// JSONVerbatim marks the type; Codec never calls it.
	JSONVerbatim()
}

// Marshal encodes v with encoding/json/v2 and rewrites the result with
// every object's members sorted by name, so equal values give equal bytes
// whatever the field declaration or map iteration order. Numbers keep the
// text encoding/json/v2 wrote. A Verbatim value keeps its members in the
// order its MarshalJSON wrote them, wherever it appears, a union variant
// included.
func (c Codec) Marshal(v any) ([]byte, error) {
	st := &callState{}
	data, err := json.Marshal(v, c.options(st))
	if err != nil {
		return nil, err
	}
	dec := NewDecoder(data)
	tree, err := dec.ReadValue()
	if err != nil {
		return nil, err
	}
	enc := NewEncoder(SortedKeys)
	if len(st.verbatim) == 0 {
		enc.Value(tree)
		return enc.Bytes()
	}
	keep := make(map[jsontext.Pointer]bool, len(st.verbatim))
	for _, p := range st.verbatim {
		keep[p] = true
	}
	enc.sortedExcept(tree, "", keep)
	return enc.Bytes()
}

// Unmarshal decodes data into v strictly: unknown members, duplicate names
// and invalid UTF-8 are errors, because persisted documents are written by
// Marshal and anything else means the document is corrupt or from a schema
// version this binary does not read.
func (c Codec) Unmarshal(data []byte, v any) error {
	return json.Unmarshal(data, v, c.options(&callState{}), json.RejectUnknownMembers(true))
}

func (c Codec) options(st *callState) json.Options {
	marshalers := make([]*json.Marshalers, 0, 1+len(c.sums))
	unmarshalers := make([]*json.Unmarshalers, 0, len(c.sums))
	marshalers = append(marshalers, verbatimMarshaler(st))
	for _, s := range c.sums {
		marshalers = append(marshalers, s.marshalers(st))
		unmarshalers = append(unmarshalers, s.unmarshalers(st)...)
	}
	return json.JoinOptions(
		json.Deterministic(true),
		json.WithMarshalers(json.JoinMarshalers(marshalers...)),
		json.WithUnmarshalers(json.JoinUnmarshalers(unmarshalers...)),
	)
}

// verbatimMarshaler writes a Verbatim value as its MarshalJSON returns it
// and records where it went, so Marshal leaves it unsorted. The pointer is
// relative to the encoder's own document; a union that marshals a variant
// into a document of its own re-roots the pointers it gained
// (Union.marshal).
func verbatimMarshaler(st *callState) *json.Marshalers {
	return json.MarshalToFunc(func(enc *jsontext.Encoder, v Verbatim) error {
		data, err := v.MarshalJSON()
		if err != nil {
			return err
		}
		if err := enc.WriteValue(data); err != nil {
			return err
		}
		st.verbatim = append(st.verbatim, enc.StackPointer())
		return nil
	})
}

// sortedExcept writes v, which sits at pointer at, with every object's
// members sorted, except the values at the pointers in keep, which keep
// their order throughout.
func (e *Encoder) sortedExcept(v document.Value, at jsontext.Pointer, keep map[jsontext.Pointer]bool) {
	if keep[at] {
		e.verbatim++
		e.Value(v)
		e.verbatim--
		return
	}
	switch v := v.(type) {
	case document.List:
		e.BeginArray()
		for i, x := range v {
			e.sortedExcept(x, at.AppendToken(strconv.Itoa(i)), keep)
		}
		e.EndArray()
	case document.Map:
		e.BeginObject()
		for _, entry := range v {
			e.Name(entry.Key)
			e.sortedExcept(entry.Value, at.AppendToken(entry.Key), keep)
		}
		e.EndObject()
	case document.Null, document.Bool, document.Number, document.String, nil:
		e.Value(v)
	}
}

// Marshal is Codec.Marshal for values that hold no sums.
func Marshal(v any) ([]byte, error) { return Codec{}.Marshal(v) }

// Unmarshal is Codec.Unmarshal for values that hold no sums.
func Unmarshal(data []byte, v any) error { return Codec{}.Unmarshal(data, v) }
