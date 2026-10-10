package jsonx

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"

	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
)

// KindKey is the member that names a variant in the tagged-union
// convention: {"kind": "<variant>", ...the variant's own fields}.
const KindKey = "kind"

// UnionError reports a union definition, or a value, that the tagged-union
// convention cannot represent.
type UnionError struct {
	// Sum names the sum type, such as "sqs.MessageState".
	Sum    string
	Reason string
}

func (e *UnionError) Error() string {
	return fmt.Sprintf("jsonx: union %s: %s", e.Sum, e.Reason)
}

// Case is one variant of the sum T: the struct type V and the kind that
// names it on the wire.
type Case[T any] struct {
	kind string
	typ  reflect.Type
	// unmarshalers builds the variant's own unmarshaler, which needs V as a
	// static type.
	unmarshalers func(*callState, *Union[T]) *json.Unmarshalers
	// zero returns a new *V as an any.
	zero func() any
	// rejectPointer refuses to marshal a *V. Written as V's kind, it would
	// read back as a V, so the Go type behind the sum would change across a
	// save and load; a nil *V would read back as a nil sum. json calls a
	// **V function for every value of type *V, which the sum's own function
	// cannot tell apart from a V.
	rejectPointer func(sum string) *json.Marshalers
}

// Variant declares the struct type V as the variant kind of the sum T. V
// must be a struct type that implements T with value receivers, which is
// how the repository writes sealed sums. Variants are stored as values: a
// *V, in a T or in a field, fails to marshal with a *UnionError.
func Variant[T, V any](kind string) Case[T] {
	return Case[T]{
		kind: kind,
		typ:  reflect.TypeFor[V](),
		unmarshalers: func(st *callState, u *Union[T]) *json.Unmarshalers {
			return json.UnmarshalFromFunc(func(dec *jsontext.Decoder, v *V) error {
				return u.unmarshalVariant(dec, st, kind, v)
			})
		},
		zero: func() any { return new(V) },
		rejectPointer: func(sum string) *json.Marshalers {
			return json.MarshalToFunc(func(*jsontext.Encoder, **V) error {
				return &UnionError{Sum: sum, Reason: fmt.Sprintf("a *%v is stored; variants are stored as values, so store a %[1]v", reflect.TypeFor[V]())}
			})
		},
	}
}

// Union is the table of one sealed sum T: which struct types are its
// variants and the kind that names each. A Codec built with it writes any
// value of a variant type, whether held in a T or in a field of the variant
// type itself, as {"kind": "<kind>", ...fields}, and reads it back.
type Union[T any] struct {
	sum    string
	kinds  map[string]Case[T]
	byType map[reflect.Type]string
}

// NewUnion builds the table of the sum T, named sum in errors. It fails
// when T is not an interface, a variant is not a struct implementing T, or
// two variants share a kind or a type.
func NewUnion[T any](sum string, cases ...Case[T]) (*Union[T], error) {
	u := &Union[T]{sum: sum, kinds: map[string]Case[T]{}, byType: map[reflect.Type]string{}}
	iface := reflect.TypeFor[T]()
	if iface.Kind() != reflect.Interface {
		return nil, &UnionError{Sum: sum, Reason: fmt.Sprintf("the sum type %v is not an interface", iface)}
	}
	for _, c := range cases {
		var reason string
		switch {
		case c.kind == "":
			reason = fmt.Sprintf("variant %v has an empty kind", c.typ)
		case c.typ.Kind() != reflect.Struct:
			reason = fmt.Sprintf("variant %v is not a struct type", c.typ)
		case !c.typ.Implements(iface):
			reason = fmt.Sprintf("variant %v does not implement %v with value receivers", c.typ, iface)
		case u.kinds[c.kind].typ != nil:
			reason = fmt.Sprintf("kind %q names both %v and %v", c.kind, u.kinds[c.kind].typ, c.typ)
		case u.byType[c.typ] != "":
			reason = fmt.Sprintf("variant %v is declared twice, as %q and %q", c.typ, u.byType[c.typ], c.kind)
		}
		if reason != "" {
			return nil, &UnionError{Sum: sum, Reason: reason}
		}
		u.kinds[c.kind] = c
		u.byType[c.typ] = c.kind
	}
	return u, nil
}

// Sum is a union table a Codec can use; *Union[T] implements it.
type Sum interface {
	marshalers(st *callState) *json.Marshalers
	unmarshalers(st *callState) []*json.Unmarshalers
}

func (u *Union[T]) marshalers(st *callState) *json.Marshalers {
	marshalers := make([]*json.Marshalers, 0, 1+len(u.kinds))
	for _, kind := range u.kindNames() {
		marshalers = append(marshalers, u.kinds[kind].rejectPointer(u.sum))
	}
	marshalers = append(marshalers, json.MarshalToFunc(func(enc *jsontext.Encoder, v T) error {
		return u.marshal(enc, st, v)
	}))
	return json.JoinMarshalers(marshalers...)
}

func (u *Union[T]) unmarshalers(st *callState) []*json.Unmarshalers {
	unmarshalers := make([]*json.Unmarshalers, 0, 1+len(u.kinds))
	unmarshalers = append(unmarshalers, json.UnmarshalFromFunc(func(dec *jsontext.Decoder, v *T) error {
		return u.unmarshalSum(dec, v)
	}))
	for _, kind := range u.kindNames() {
		unmarshalers = append(unmarshalers, u.kinds[kind].unmarshalers(st, u))
	}
	return unmarshalers
}

// marshal writes v, a variant held in a T or a field of a variant type, in
// the tagged form. The function json calls for every value implementing T
// is this one, so encoding the variant's own fields defers to json's
// default struct encoding for exactly one call (callState.bypass).
func (u *Union[T]) marshal(enc *jsontext.Encoder, st *callState, v T) error {
	if st.bypass {
		st.bypass = false
		return errors.ErrUnsupported
	}
	// json hands a sum's function a non-nil pointer to the value, even
	// for a value stored as V; a *V the caller stored never reaches here
	// (Case.rejectPointer refuses it).
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Pointer {
		rv = rv.Elem()
	}
	kind, ok := u.byType[rv.Type()]
	if !ok {
		return &UnionError{Sum: u.sum, Reason: fmt.Sprintf("%v is not a declared variant; add it to the union's table", rv.Type())}
	}
	st.bypass = true
	body, err := json.Marshal(rv.Interface(), enc.Options())
	st.bypass = false
	if err != nil {
		return err
	}
	dec := NewDecoder(body)
	fields, err := dec.ReadValue()
	if err != nil {
		return err
	}
	object, ok := fields.(document.Map)
	if !ok {
		return &UnionError{Sum: u.sum, Reason: fmt.Sprintf("variant %v did not encode as an object", rv.Type())}
	}
	if _, reserved := member(object, KindKey); reserved {
		return &UnionError{Sum: u.sum, Reason: fmt.Sprintf("variant %v has a field named %q, which the tagged form reserves", rv.Type(), KindKey)}
	}
	out := NewEncoder(WireOrder)
	out.BeginObject()
	out.Name(KindKey)
	out.String(kind)
	for _, e := range object {
		out.Name(e.Key)
		out.Value(e.Value)
	}
	out.EndObject()
	tagged, err := out.Bytes()
	if err != nil {
		return err
	}
	return enc.WriteValue(tagged)
}

// unmarshalSum reads a tagged value into a T: null leaves the zero T, any
// other value must be an object whose kind names a declared variant.
func (u *Union[T]) unmarshalSum(dec *jsontext.Decoder, v *T) error {
	raw, err := dec.ReadValue()
	if err != nil {
		return err
	}
	if raw.Kind() == jsontext.KindNull {
		var zero T
		*v = zero
		return nil
	}
	kind, _, err := u.splitKind(raw)
	if err != nil {
		return err
	}
	c, ok := u.kinds[kind]
	if !ok {
		return &UnionError{Sum: u.sum, Reason: fmt.Sprintf("kind %q names no variant; the known kinds are %v", kind, u.kindNames())}
	}
	p := c.zero()
	if err := json.Unmarshal(raw, p, dec.Options()); err != nil {
		return err
	}
	variant, ok := reflect.TypeAssert[T](reflect.ValueOf(p).Elem())
	if !ok {
		return &UnionError{Sum: u.sum, Reason: fmt.Sprintf("variant %v does not implement the sum", c.typ)}
	}
	*v = variant
	return nil
}

// unmarshalVariant reads a tagged value into the variant type it declares,
// checking the kind, then decodes the remaining fields with json's default
// struct decoding.
func (u *Union[T]) unmarshalVariant(dec *jsontext.Decoder, st *callState, want string, v any) error {
	if st.bypass {
		st.bypass = false
		return errors.ErrUnsupported
	}
	raw, err := dec.ReadValue()
	if err != nil {
		return err
	}
	kind, fields, err := u.splitKind(raw)
	if err != nil {
		return err
	}
	if kind != want {
		return &UnionError{Sum: u.sum, Reason: fmt.Sprintf("kind %q where variant %q was expected", kind, want)}
	}
	st.bypass = true
	err = json.Unmarshal(fields, v, dec.Options())
	st.bypass = false
	return err
}

// splitKind returns the kind of a tagged object and the object without it.
func (u *Union[T]) splitKind(raw []byte) (kind string, fields []byte, err error) {
	dec := NewDecoder(raw)
	value, err := dec.ReadValue()
	if err != nil {
		return "", nil, err
	}
	object, ok := value.(document.Map)
	if !ok {
		return "", nil, &UnionError{Sum: u.sum, Reason: "a tagged value must be a JSON object"}
	}
	tag, _ := member(object, KindKey)
	name, ok := tag.(document.String)
	if !ok {
		return "", nil, &UnionError{Sum: u.sum, Reason: fmt.Sprintf("the object has no string %q member naming its variant", KindKey)}
	}
	rest := NewEncoder(WireOrder)
	rest.BeginObject()
	for _, e := range object {
		if e.Key != KindKey {
			rest.Name(e.Key)
			rest.Value(e.Value)
		}
	}
	rest.EndObject()
	fields, err = rest.Bytes()
	return string(name), fields, err
}

func (u *Union[T]) kindNames() []string {
	return slices.Sorted(maps.Keys(u.kinds))
}

// member returns the value of the member named key.
func member(m document.Map, key string) (document.Value, bool) {
	for _, e := range m {
		if e.Key == key {
			return e.Value, true
		}
	}
	return nil, false
}

// callState is shared by every union of one Marshal or Unmarshal call.
// bypass makes the next union function json calls defer to json's default
// encoding of the value, which is how a variant's own fields are written
// and read: the function for a sum also fires for the variant type itself.
// A call is sequential, so the flag needs no lock; each call has its own.
type callState struct {
	bypass bool
}
