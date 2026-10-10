package codegen

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"

	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
)

// Kind is a JSON AST shape type. Data kinds map onto smithy.ShapeType; the
// service, resource and operation kinds never reach a codec.
type Kind uint8

// Kind values.
const (
	KindService Kind = iota + 1
	KindResource
	KindOperation
	KindData
)

// Model is a loaded Smithy 2.0 JSON AST, merged with overlays once Merge
// has run. Shapes holds every shape by absolute id, prelude shapes included.
type Model struct {
	Shapes map[smithy.ShapeID]*Shape
}

// Shape is one shape of the AST. Only the fields of its kind are set.
type Shape struct {
	ID   smithy.ShapeID
	Kind Kind
	// Type is the data type of a KindData shape.
	Type   smithy.ShapeType
	Traits Traits
	// Members are a structure's, union's or enum's members in model order; a
	// list has one, "member"; a map has two, "key" then "value".
	Members []*Member

	// Operation fields.
	Input, Output smithy.ShapeID
	Errors        []smithy.ShapeID

	// Service and resource fields.
	Version              string
	Operations           []smithy.ShapeID
	Resources            []smithy.ShapeID
	Lifecycle            []smithy.ShapeID // a resource's create, put, read, update, delete and list operations
	CollectionOperations []smithy.ShapeID
	// Rename gives shapes of the service's closure the names the service
	// knows them by (the corpus's RestJson renames a nested GreetingStruct).
	Rename map[smithy.ShapeID]string

	// Source is the file that defined the shape, for error messages.
	Source string
}

// Member is one member of a shape.
type Member struct {
	Name   string
	Target smithy.ShapeID
	Traits Traits
}

// Member returns the member named name.
func (s *Shape) Member(name string) (*Member, bool) {
	for _, m := range s.Members {
		if m.Name == name {
			return m, true
		}
	}
	return nil, false
}

// Traits maps absolute trait ids to their JSON values. Values keep their
// exact text (numbers included), so a trait never loses precision and two
// values compare by canonical form.
type Traits map[string]json.RawMessage

// UnmarshalJSON decodes a traits object and rejects a repeated trait id.
func (t *Traits) UnmarshalJSON(data []byte) error {
	out := Traits{}
	err := decodeObject(data, "trait", func(id string, v json.RawMessage) error {
		out[id] = v
		return nil
	})
	if err != nil {
		return err
	}
	*t = out
	return nil
}

// rawShapes is the "shapes" object of a model or an overlay, by key.
type rawShapes map[string]json.RawMessage

// UnmarshalJSON decodes the shapes object and rejects a repeated key.
func (s *rawShapes) UnmarshalJSON(data []byte) error {
	out := rawShapes{}
	err := decodeObject(data, "shape", func(key string, v json.RawMessage) error {
		out[key] = v
		return nil
	})
	if err != nil {
		return err
	}
	*s = out
	return nil
}

// duplicateKeyError is a JSON object that repeats a key. encoding/json would
// keep the last value and silently drop the others, so the loader refuses.
type duplicateKeyError struct {
	what, key string
}

func (e *duplicateKeyError) Error() string {
	return fmt.Sprintf("%s %q appears twice; merge the two into one", e.what, e.key)
}

// decodeObject walks a JSON object in document order, handing each key and
// value to each, and fails with a *duplicateKeyError on a repeated key. A
// null is an empty object. what names a key in messages.
func decodeObject(data []byte, what string, each func(key string, value json.RawMessage) error) error {
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return fmt.Errorf("%ss must be an object", what)
	}
	seen := map[string]bool{}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		key, ok := tok.(string)
		if !ok {
			return fmt.Errorf("%s names must be strings", what)
		}
		if seen[key] {
			return &duplicateKeyError{what: what, key: key}
		}
		seen[key] = true
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return err
		}
		if err := each(key, v); err != nil {
			return err
		}
	}
	_, err = dec.Token()
	return err
}

// Has reports whether the trait id is applied.
func (t Traits) Has(id string) bool {
	_, ok := t[id]
	return ok
}

// IDs returns the applied trait ids, sorted.
func (t Traits) IDs() []string { return slices.Sorted(maps.Keys(t)) }

// rawModel is the top level of a JSON AST file.
type rawModel struct {
	Smithy   string          `json:"smithy"`
	Metadata json.RawMessage `json:"metadata"`
	Shapes   rawShapes       `json:"shapes"`
}

// rawShape is one JSON AST shape with every property the spec defines, so a
// strict decoder rejects anything else. Replace is overlay-only.
type rawShape struct {
	Type                 string            `json:"type"`
	Traits               Traits            `json:"traits"`
	Members              orderedMembers    `json:"members"`
	Member               *rawMember        `json:"member"`
	Key                  *rawMember        `json:"key"`
	Value                *rawMember        `json:"value"`
	Input                *rawRef           `json:"input"`
	Output               *rawRef           `json:"output"`
	Errors               []rawRef          `json:"errors"`
	Version              string            `json:"version"`
	Operations           []rawRef          `json:"operations"`
	Resources            []rawRef          `json:"resources"`
	Create               *rawRef           `json:"create"`
	Put                  *rawRef           `json:"put"`
	Read                 *rawRef           `json:"read"`
	Update               *rawRef           `json:"update"`
	Delete               *rawRef           `json:"delete"`
	List                 *rawRef           `json:"list"`
	CollectionOperations []rawRef          `json:"collectionOperations"`
	Identifiers          map[string]rawRef `json:"identifiers"`
	Properties           map[string]rawRef `json:"properties"`
	Rename               map[string]string `json:"rename"`
	Mixins               []rawRef          `json:"mixins"`
	Replace              bool              `json:"replace"`
}

type rawRef struct {
	Target string `json:"target"`
}

type rawMember struct {
	Target string `json:"target"`
	Traits Traits `json:"traits"`
}

type namedMember struct {
	name string
	m    rawMember
}

// orderedMembers keeps the JSON object order of a members map: structure
// member order is the order codecs and validators follow.
type orderedMembers []namedMember

// UnmarshalJSON decodes the members object in document order and rejects a
// duplicate name.
func (o *orderedMembers) UnmarshalJSON(data []byte) error {
	var out orderedMembers
	err := decodeObject(data, "member", func(name string, v json.RawMessage) error {
		var m rawMember
		if err := strictUnmarshal(v, &m); err != nil {
			return fmt.Errorf("member %q: %w", name, err)
		}
		out = append(out, namedMember{name: name, m: m})
		return nil
	})
	if err != nil {
		return err
	}
	*o = out
	return nil
}

// strictUnmarshal decodes with UseNumber and refuses unknown properties.
func strictUnmarshal(raw []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if dec.More() {
		return errors.New("trailing data after the JSON value")
	}
	return nil
}

// Load reads a vendored model. It fails on anything the published models do
// not use and the generator would otherwise have to guess at
// (smithy-models.md 2): unknown shape types, apply and set shapes, mixins,
// renames outside a service shape and malformed ids. source names the file
// in messages.
func Load(r io.Reader, source string) (*Model, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, failf(StageLoad, source, "read: %w", err)
	}
	var rm rawModel
	if err := strictUnmarshal(raw, &rm); err != nil {
		if dup := (*duplicateKeyError)(nil); errors.As(err, &dup) {
			return nil, failf(StageLoad, source, "%w", err)
		}
		return nil, failf(StageLoad, source, "not a Smithy JSON AST: %w", err)
	}
	if rm.Smithy != "2.0" && rm.Smithy != "2" {
		return nil, failf(StageLoad, source, `"smithy" is %q; only Smithy 2.0 models are supported`, rm.Smithy)
	}
	m := &Model{Shapes: prelude()}
	for _, key := range slices.Sorted(maps.Keys(rm.Shapes)) {
		s, err := parseShape(key, rm.Shapes[key], source, StageLoad)
		if err != nil {
			return nil, err
		}
		if _, dup := m.Shapes[s.ID]; dup {
			return nil, failf(StageLoad, source+": "+key, "defined twice (the prelude already defines it)")
		}
		m.Shapes[s.ID] = s
	}
	return m, nil
}

// parseShape decodes one shape of source; stage is StageLoad for a vendored
// model and StageOverlay for a shape an overlay defines. The replace
// property belongs to overlays only.
func parseShape(key string, raw json.RawMessage, source string, stage Stage) (*Shape, error) {
	subject := source + ": " + key
	id, err := smithy.Parse(key)
	if err != nil {
		return nil, failf(stage, subject, "%w", err)
	}
	var rs rawShape
	if err := strictUnmarshal(raw, &rs); err != nil {
		return nil, failf(stage, subject, "%w", err)
	}
	switch {
	case len(rs.Mixins) > 0:
		return nil, failf(stage, subject, "uses mixins, which no published model uses; the generator does not flatten them")
	case len(rs.Rename) > 0 && rs.Type != "service":
		return nil, failf(stage, subject, "uses rename, which only a service shape may")
	case rs.Replace && stage != StageOverlay:
		return nil, failf(stage, subject, `"replace" is an overlay property`)
	}
	s := &Shape{ID: id, Traits: rs.Traits, Source: source}
	if s.Traits == nil {
		s.Traits = Traits{}
	}
	switch rs.Type {
	case "service":
		s.Kind = KindService
		s.Version = rs.Version
		s.Operations = refs(rs.Operations)
		s.Resources = refs(rs.Resources)
		s.Errors = refs(rs.Errors)
		s.Rename = renames(rs.Rename)
	case "resource":
		s.Kind = KindResource
		s.Operations = refs(rs.Operations)
		s.Resources = refs(rs.Resources)
		s.CollectionOperations = refs(rs.CollectionOperations)
		for _, r := range []*rawRef{rs.Create, rs.Put, rs.Read, rs.Update, rs.Delete, rs.List} {
			if r != nil {
				s.Lifecycle = append(s.Lifecycle, smithy.ShapeID(r.Target))
			}
		}
	case "operation":
		s.Kind = KindOperation
		s.Input = refOr(rs.Input, "smithy.api#Unit")
		s.Output = refOr(rs.Output, "smithy.api#Unit")
		s.Errors = refs(rs.Errors)
	case "apply":
		return nil, failf(stage, subject, "apply is not allowed in a vendored model (smithy-models.md 2); corrections belong in codegen/overlays")
	case "set":
		return nil, failf(stage, subject, "set is a Smithy 1.0 shape type, not allowed in a Smithy 2.0 model (smithy-models.md 2)")
	default:
		t, err := smithy.ParseShapeType(rs.Type)
		if err != nil {
			return nil, failf(stage, subject, "%w", err)
		}
		s.Kind = KindData
		s.Type = t
		if err := s.setMembers(rs); err != nil {
			return nil, failf(stage, subject, "%w", err)
		}
	}
	return s, nil
}

// renames reads a service's rename property, nil when it has none.
func renames(raw map[string]string) map[smithy.ShapeID]string {
	if len(raw) == 0 {
		return nil
	}
	out := make(map[smithy.ShapeID]string, len(raw))
	for id, name := range raw {
		out[smithy.ShapeID(id)] = name
	}
	return out
}

func (s *Shape) setMembers(rs rawShape) error {
	add := func(name string, m *rawMember) {
		traits := m.Traits
		if traits == nil {
			traits = Traits{}
		}
		s.Members = append(s.Members, &Member{Name: name, Target: smithy.ShapeID(m.Target), Traits: traits})
	}
	switch s.Type {
	case smithy.TypeList:
		if rs.Member == nil {
			return errors.New("a list needs a member")
		}
		add("member", rs.Member)
	case smithy.TypeMap:
		if rs.Key == nil || rs.Value == nil {
			return errors.New("a map needs a key and a value")
		}
		add("key", rs.Key)
		add("value", rs.Value)
	case smithy.TypeStructure, smithy.TypeUnion, smithy.TypeEnum, smithy.TypeIntEnum:
		for _, nm := range rs.Members {
			add(nm.name, &nm.m)
		}
	case smithy.TypeString, smithy.TypeBlob, smithy.TypeBoolean, smithy.TypeByte, smithy.TypeShort,
		smithy.TypeInteger, smithy.TypeLong, smithy.TypeFloat, smithy.TypeDouble, smithy.TypeBigInteger,
		smithy.TypeBigDecimal, smithy.TypeTimestamp, smithy.TypeDocument, smithy.TypeUnit:
		if len(rs.Members) > 0 || rs.Member != nil || rs.Key != nil || rs.Value != nil {
			return fmt.Errorf("a %s shape has no members", s.Type)
		}
	}
	for _, m := range s.Members {
		if _, err := smithy.Parse(string(m.Target)); err != nil {
			return fmt.Errorf("member %s: %w", m.Name, err)
		}
	}
	return nil
}

func refs(rs []rawRef) []smithy.ShapeID {
	out := make([]smithy.ShapeID, len(rs))
	for i, r := range rs {
		out[i] = smithy.ShapeID(r.Target)
	}
	return out
}

func refOr(r *rawRef, def smithy.ShapeID) smithy.ShapeID {
	if r == nil {
		return def
	}
	return smithy.ShapeID(r.Target)
}

// prelude returns the smithy.api simple shapes a model or overlay may
// target without defining them.
func prelude() map[smithy.ShapeID]*Shape {
	out := map[smithy.ShapeID]*Shape{}
	add := func(name string, t smithy.ShapeType, traits Traits) {
		id := smithy.ShapeID("smithy.api#" + name)
		if traits == nil {
			traits = Traits{}
		}
		out[id] = &Shape{ID: id, Kind: KindData, Type: t, Traits: traits, Source: "prelude"}
	}
	add("String", smithy.TypeString, nil)
	add("Blob", smithy.TypeBlob, nil)
	add("Boolean", smithy.TypeBoolean, nil)
	add("Byte", smithy.TypeByte, nil)
	add("Short", smithy.TypeShort, nil)
	add("Integer", smithy.TypeInteger, nil)
	add("Long", smithy.TypeLong, nil)
	add("Float", smithy.TypeFloat, nil)
	add("Double", smithy.TypeDouble, nil)
	add("BigInteger", smithy.TypeBigInteger, nil)
	add("BigDecimal", smithy.TypeBigDecimal, nil)
	add("Timestamp", smithy.TypeTimestamp, nil)
	add("Document", smithy.TypeDocument, nil)
	add("PrimitiveBoolean", smithy.TypeBoolean, Traits{traitDefault: json.RawMessage("false")})
	add("PrimitiveByte", smithy.TypeByte, Traits{traitDefault: json.RawMessage("0")})
	add("PrimitiveShort", smithy.TypeShort, Traits{traitDefault: json.RawMessage("0")})
	add("PrimitiveInteger", smithy.TypeInteger, Traits{traitDefault: json.RawMessage("0")})
	add("PrimitiveLong", smithy.TypeLong, Traits{traitDefault: json.RawMessage("0")})
	add("PrimitiveFloat", smithy.TypeFloat, Traits{traitDefault: json.RawMessage("0")})
	add("PrimitiveDouble", smithy.TypeDouble, Traits{traitDefault: json.RawMessage("0")})
	add("Unit", smithy.TypeUnit, Traits{traitUnitType: json.RawMessage("{}")})
	return out
}
