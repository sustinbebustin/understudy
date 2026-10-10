package jsonx

import (
	"encoding/json/v2"
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

// Marshal encodes v with encoding/json/v2 and rewrites the result with
// every object's members sorted by name, so equal values give equal bytes
// whatever the field declaration or map iteration order. Numbers keep the
// text encoding/json/v2 wrote.
func (c Codec) Marshal(v any) ([]byte, error) {
	data, err := json.Marshal(v, c.options())
	if err != nil {
		return nil, err
	}
	dec := NewDecoder(data)
	tree, err := dec.ReadValue()
	if err != nil {
		return nil, err
	}
	enc := NewEncoder(SortedKeys)
	enc.Value(tree)
	return enc.Bytes()
}

// Unmarshal decodes data into v strictly: unknown members, duplicate names
// and invalid UTF-8 are errors, because persisted documents are written by
// Marshal and anything else means the document is corrupt or from a schema
// version this binary does not read.
func (c Codec) Unmarshal(data []byte, v any) error {
	return json.Unmarshal(data, v, c.options(), json.RejectUnknownMembers(true))
}

func (c Codec) options() json.Options {
	st := &callState{}
	marshalers := make([]*json.Marshalers, 0, len(c.sums))
	unmarshalers := make([]*json.Unmarshalers, 0, len(c.sums))
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

// Marshal is Codec.Marshal for values that hold no sums.
func Marshal(v any) ([]byte, error) { return Codec{}.Marshal(v) }

// Unmarshal is Codec.Unmarshal for values that hold no sums.
func Unmarshal(data []byte, v any) error { return Codec{}.Unmarshal(data, v) }
