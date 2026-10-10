package codegen

import (
	"cmp"
	"encoding/json"
	"fmt"
	"go/token"
	"maps"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// ServiceIR is the typed intermediate representation of one service: the
// emitters read nothing else.
type ServiceIR struct {
	ID        ref.ServiceID
	Package   string
	ModelFile string // base name of the model file, for the header
	Pin       string // short upstream commit, for the header

	Shape            smithy.ShapeID
	SigningName      string
	IAMPrefix        string
	ARNNamespace     string
	Protocols        []ref.ProtocolID
	JSONTargetPrefix string
	QueryVersion     string
	XMLNamespace     string
	QueryCompatible  bool

	Operations []*OperationIR  // sorted by name; Ordinal is the index
	Shapes     []*ShapeIR      // every data shape, sorted by id; synthetic unit IO shapes last
	Errors     []*ErrorIR      // sorted by Go name
	Patterns   []*PatternVarIR // the distinct patterns input members check, by rendering

	Throttle         *ErrorIR
	UnknownJSON      *ErrorIR
	UnknownQuery     *ErrorIR
	MalformedRequest *ErrorIR

	// Validation are the service-level validationError rules; the last
	// rule with no constraint filter is the catch-all.
	Validation []RuleIR
}

// OperationIR is one operation.
type OperationIR struct {
	Name        string
	ID          smithy.ShapeID
	Ordinal     int
	Kind        registry.OperationKind
	Input       *ShapeIR
	Output      *ShapeIR
	Errors      []*ErrorIR // modeled errors in model order, then validation identities
	SuccessCode int
	// MaxRequestBytes is the overlay's dev.understudy#maxRequestBytes, or 0
	// when the overlay sets none and the edge applies the protocol default
	// (ARCHITECTURE.md 4, step 1 admit).
	MaxRequestBytes int64
	Readonly        bool
	Idempotent      bool
	HTTP            *registry.HTTPBinding
	Validation      []RuleIR // operation-level rules
	Paths           []PathIR // every member path of the input, for validation identity selection
	Examples        []ExampleIR
}

// ExampleIR is one smithy.api#examples entry whose input the generated test
// validates.
type ExampleIR struct {
	Title string
	Input json.RawMessage
}

// Role says which direction a shape travels.
type Role uint8

// Role bits.
const (
	RoleInput Role = 1 << iota
	RoleOutput
)

// ShapeIR is one data shape.
type ShapeIR struct {
	ID   smithy.ShapeID
	Type smithy.ShapeType
	// GoName is the Go type name of a structure, union, enum or intEnum,
	// and the helper-function stem of a list or map.
	GoName    string
	SchemaVar string
	Members   []*MemberIR
	Enum      []EnumValueIR

	TimestampFormat smithy.TimestampFormat
	MediaType       string
	Sensitive       bool
	Sparse          bool
	XMLName         string
	XMLNamespace    *smithy.XMLNamespace
	Error           *ErrorIR // set on error structures
	Role            Role
	// IO is the operation whose input or output this shape is, renamed to
	// <Op>Input or <Op>Output.
	IO *OperationIR
	// Unit marks a synthetic per-operation empty structure for a
	// smithy.api#Unit input or output.
	Unit bool
	// Tracked counts the required members whose presence Deserialize
	// records, so Validate can tell absent from zero.
	Tracked int
}

// IsStruct reports whether the shape is a structure or a unit.
func (s *ShapeIR) IsStruct() bool { return s.Type == smithy.TypeStructure || s.Type == smithy.TypeUnit }

// EnumValueIR is one enum member.
type EnumValueIR struct {
	Name   string // Go constant suffix
	Value  string // wire value (string enums)
	Number int64  // intEnum value
}

// MemberIR is one member with its effective traits (member over target).
type MemberIR struct {
	Name   string
	GoName string
	Index  int
	Var    string // the smithy.Member variable
	Owner  *ShapeIR
	Target *ShapeIR

	Required        bool
	Default         json.RawMessage // nil when absent or null
	JSONName        string
	XMLName         string
	XMLFlattened    bool
	XMLAttribute    bool
	XMLNamespace    *smithy.XMLNamespace
	TimestampFormat smithy.TimestampFormat
	Sensitive       bool
	HTTP            string // Go expression of the smithy.Location, or empty

	Length     *Bounds
	Range      *Bounds
	Pattern    *PatternIR
	EnumPolicy EnumPolicy

	AlwaysEmit bool
	Empty      EmptyCollection
	// Value: the Go field is a plain value rather than a pointer (scalars and
	// structures only; collections, unions and documents use nil for absent).
	Value bool
	// Bit is the presence bit of a tracked required member, else -1.
	Bit   int
	Rules []RuleIR // member-level validationError rules
	// rulesRaw holds the member's validationError until the error
	// identities exist to resolve it against.
	rulesRaw json.RawMessage
}

// Bounds is a length or range trait, decimal text as the model wrote it.
type Bounds struct {
	Min, Max string // empty when unbounded
}

// PatternIR is a pattern trait after the regex stage.
type PatternIR struct {
	Source    string
	RE2       string // empty when Validator renders it
	Echo      string
	Validator *PatternValidator
	Var       string // the PatternVarIR name of an input member's pattern
}

// PatternVarIR is one distinct rendering input members check, declared once
// as a package variable (or one per validator part) named Name.
type PatternVarIR struct {
	Name      string
	Sources   []string // every distinct model pattern with this rendering, sorted
	RE2       string   // empty when Validator renders it
	Validator *PatternValidator
}

// ErrorIR is an error shape's identity.
type ErrorIR struct {
	Shape *ShapeIR
	// HTTPError is the model's httpError, 0 when the shape has none.
	HTTPError       int
	JSONType        string
	QueryCode       string
	Status          int
	QueryModeStatus int
	Fault           awserr.Fault
	MessageKey      awserr.MessageKey
	Retryable       bool
	Messages        map[ref.ProtocolID]string
	// Generic marks shapes outside the service namespace (the generic
	// vocabulary of overlays/common.json).
	Generic bool
}

// RuleIR is a resolved validationError rule.
type RuleIR struct {
	Error      *ErrorIR // nil for a rule that only sets the enum policy
	Style      ValidationStyle
	Kinds      ConstraintKind
	Reason     string
	EnumPolicy EnumPolicy
}

// PathIR is one member path of an operation input: lowerCamel segments with
// "*" for a list index or map key.
type PathIR struct {
	Segments []string
	Param    string
	Rules    []RuleIR
}

// builder carries state while the IR is built.
type builder struct {
	model    *Model
	cfg      ServiceConfig
	clo      *closure
	ir       *ServiceIR
	shapes   map[smithy.ShapeID]*ShapeIR
	errors   map[smithy.ShapeID]*ErrorIR
	goNames  map[string]smithy.ShapeID
	enumDef  EnumPolicy
	emptyDef EmptyCollection
	style    PathStyle
}

// Build turns a merged model into the IR of the service cfg names.
func Build(m *Model, cfg ServiceConfig, modelFile, pin string) (*ServiceIR, error) {
	if err := m.checkUnderstudy(); err != nil {
		return nil, err
	}
	clo, err := m.closure(cfg.Service)
	if err != nil {
		return nil, err
	}
	b := &builder{
		model: m, cfg: cfg, clo: clo,
		ir:      &ServiceIR{ID: cfg.ID, Package: cfg.Package, ModelFile: modelFile, Pin: pin, Shape: cfg.Service},
		shapes:  map[smithy.ShapeID]*ShapeIR{},
		errors:  map[smithy.ShapeID]*ErrorIR{},
		goNames: map[string]smithy.ShapeID{},
	}
	for _, step := range []func() error{
		b.classify, b.service, b.dataShapes, b.operations, b.members, b.roles, b.errorIdentities, b.policies, b.patterns,
	} {
		if err := step(); err != nil {
			return nil, err
		}
	}
	return b.ir, nil
}

// classify fails on any trait the table does not know, and on honored traits
// this generator cannot emit yet.
func (b *builder) classify() error {
	check := func(subject string, traits Traits) error {
		for _, id := range traits.IDs() {
			if _, ok := Classify(id); !ok {
				return failf(StageClassify, subject, "trait %s is unclassified; add it to traitClasses in internal/codegen/trait.go as honor, generation-only or ignore (smithy-models.md 4.3)", id)
			}
			if slices.Contains(notYetEmitted(), id) {
				return failf(StageClassify, subject, "trait %s is honored but its emission lands with the all-models generator (PHASE1-PLAN T19)", id)
			}
		}
		return nil
	}
	all := slices.Concat([]*Shape{b.clo.service}, b.clo.operations)
	for _, id := range slices.Sorted(maps.Keys(b.clo.shapes)) {
		all = append(all, b.clo.shapes[id])
	}
	for _, s := range all {
		if err := check(string(s.ID), s.Traits); err != nil {
			return err
		}
		for _, mem := range s.Members {
			if err := check(string(s.ID)+"$"+mem.Name, mem.Traits); err != nil {
				return err
			}
		}
	}
	return nil
}

func (b *builder) service() error {
	svc := b.clo.service
	for _, step := range []func(*Shape) error{b.serviceIdentity, b.serviceProtocols, b.serviceDefaults} {
		if err := step(svc); err != nil {
			return err
		}
	}
	return nil
}

// serviceIdentity reads the ARN namespace, IAM prefix and signing name.
func (b *builder) serviceIdentity(svc *Shape) error {
	if raw, ok := svc.Traits[traitAWSService]; ok {
		var info struct {
			ARNNamespace string `json:"arnNamespace"`
		}
		if err := json.Unmarshal(raw, &info); err != nil {
			return failf(StageIR, string(svc.ID), "%s: %w", traitAWSService, err)
		}
		b.ir.ARNNamespace = info.ARNNamespace
	}
	b.ir.IAMPrefix = b.cfg.IAMPrefix
	if raw, ok := svc.Traits[traitSigV4]; ok {
		var v struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(raw, &v); err != nil {
			return failf(StageIR, string(svc.ID), "%s: %w", traitSigV4, err)
		}
		b.ir.SigningName = v.Name
	}
	return nil
}

// serviceProtocols lists the model's protocols, then the config's extras,
// and the per-protocol routing facts.
func (b *builder) serviceProtocols(svc *Shape) error {
	ir := b.ir
	for _, id := range svc.Traits.IDs() {
		if p, ok := protocolTraits()[id]; ok {
			ir.Protocols = append(ir.Protocols, p)
		}
	}
	for _, p := range b.cfg.ExtraProtocols {
		if slices.Contains(ir.Protocols, p) {
			return failf(StageIR, string(svc.ID), "extraProtocols repeats %s, which the model already declares", p)
		}
		ir.Protocols = append(ir.Protocols, p)
	}
	if len(ir.Protocols) == 0 {
		return failf(StageIR, string(svc.ID), "declares no protocol")
	}
	if slices.Contains(ir.Protocols, ref.AWSJSON10) || slices.Contains(ir.Protocols, ref.AWSJSON11) {
		ir.JSONTargetPrefix = svc.ID.Name()
	}
	if slices.Contains(ir.Protocols, ref.AWSQuery) {
		ir.QueryVersion = svc.Version
	}
	ir.QueryCompatible = svc.Traits.Has(traitQueryCompatible)
	if raw, ok := svc.Traits[traitXMLNamespace]; ok {
		ns, err := parseXMLNamespace(raw)
		if err != nil {
			return failf(StageIR, string(svc.ID), "%s: %w", traitXMLNamespace, err)
		}
		ir.XMLNamespace = ns.URI
	}
	return nil
}

// serviceDefaults reads the service-wide overlay defaults members inherit.
func (b *builder) serviceDefaults(svc *Shape) error {
	b.enumDef = EnumClosed
	if raw, ok := svc.Traits[utEmptyCollection]; ok {
		e, err := parseEmptyCollection(raw)
		if err != nil {
			return failf(StageIR, string(svc.ID), "%w", err)
		}
		b.emptyDef = e
	}
	b.style = PathIndexed
	if raw, ok := svc.Traits[utValidationPathStyle]; ok {
		s, err := parsePathStyle(raw)
		if err != nil {
			return failf(StageIR, string(svc.ID), "%w", err)
		}
		b.style = s
	}
	if b.style == PathRolledUp {
		return failf(StageIR, string(svc.ID), "validationPathStyle rolledUp is parsed, but this generator emits only indexed paths; rolled-up validators land with the all-models generator")
	}
	return nil
}

func parseXMLNamespace(raw json.RawMessage) (smithy.XMLNamespace, error) {
	var v struct {
		URI    string `json:"uri"`
		Prefix string `json:"prefix"`
	}
	if err := decodeTrait(raw, &v); err != nil {
		return smithy.XMLNamespace{}, err
	}
	return smithy.XMLNamespace(v), nil
}

// dataShapes creates a ShapeIR per data shape with its shape-level traits.
func (b *builder) dataShapes() error {
	for _, id := range slices.Sorted(maps.Keys(b.clo.shapes)) {
		s := b.clo.shapes[id]
		if s.Traits.Has(traitStreaming) {
			return failf(StageIR, string(id), "streaming shapes become smithy.Body and smithy.BodySource with the all-models generator; this generator does not emit them yet")
		}
		ir := &ShapeIR{ID: id, Type: s.Type, Sparse: s.Traits.Has(traitSparse), Sensitive: s.Traits.Has(traitSensitive)}
		if err := b.shapeTraits(ir, s); err != nil {
			return err
		}
		b.shapes[id] = ir
		b.ir.Shapes = append(b.ir.Shapes, ir)
	}
	return nil
}

func (b *builder) shapeTraits(ir *ShapeIR, s *Shape) error {
	subject := string(s.ID)
	if raw, ok := s.Traits[traitTimestampFormat]; ok {
		f, err := parseTimestampFormat(raw)
		if err != nil {
			return failf(StageIR, subject, "%w", err)
		}
		ir.TimestampFormat = f
	}
	if raw, ok := s.Traits[traitMediaType]; ok {
		if err := decodeTrait(raw, &ir.MediaType); err != nil {
			return failf(StageIR, subject, "%s: %w", traitMediaType, err)
		}
	}
	if raw, ok := s.Traits[traitXMLName]; ok {
		if err := decodeTrait(raw, &ir.XMLName); err != nil {
			return failf(StageIR, subject, "%s: %w", traitXMLName, err)
		}
	}
	if raw, ok := s.Traits[traitXMLNamespace]; ok {
		ns, err := parseXMLNamespace(raw)
		if err != nil {
			return failf(StageIR, subject, "%s: %w", traitXMLNamespace, err)
		}
		ir.XMLNamespace = &ns
	}
	if s.Type == smithy.TypeEnum || s.Type == smithy.TypeIntEnum {
		return b.enumValues(ir, s)
	}
	return nil
}

func parseTimestampFormat(raw json.RawMessage) (smithy.TimestampFormat, error) {
	var v string
	if err := decodeTrait(raw, &v); err != nil {
		return 0, err
	}
	switch v {
	case "date-time":
		return smithy.DateTime, nil
	case "http-date":
		return smithy.HTTPDate, nil
	case "epoch-seconds":
		return smithy.EpochSeconds, nil
	default:
		return 0, fmt.Errorf("%s %q is not date-time, http-date or epoch-seconds", traitTimestampFormat, v)
	}
}

func (b *builder) enumValues(ir *ShapeIR, s *Shape) error {
	seen := map[string]bool{}
	for _, m := range s.Members {
		v := EnumValueIR{Name: enumConstName(m.Name)}
		raw, ok := m.Traits[traitEnumValue]
		switch {
		case s.Type == smithy.TypeEnum && ok:
			if err := decodeTrait(raw, &v.Value); err != nil {
				return failf(StageIR, string(s.ID)+"$"+m.Name, "%s: %w", traitEnumValue, err)
			}
		case s.Type == smithy.TypeEnum:
			v.Value = m.Name
		case ok:
			if err := decodeTrait(raw, &v.Number); err != nil {
				return failf(StageIR, string(s.ID)+"$"+m.Name, "%s: %w", traitEnumValue, err)
			}
		default:
			return failf(StageIR, string(s.ID)+"$"+m.Name, "an intEnum member needs %s", traitEnumValue)
		}
		if seen[v.Name] {
			return failf(StageIR, string(s.ID)+"$"+m.Name, "two members map onto the Go constant suffix %s", v.Name)
		}
		seen[v.Name] = true
		ir.Enum = append(ir.Enum, v)
	}
	return nil
}

// operations builds the operation list and names IO structures.
func (b *builder) operations() error {
	refs := map[smithy.ShapeID]int{}
	for _, s := range b.clo.shapes {
		for _, m := range s.Members {
			refs[m.Target]++
		}
	}
	for _, op := range b.clo.operations {
		refs[op.Input]++
		refs[op.Output]++
	}
	for i, op := range b.clo.operations {
		o := &OperationIR{
			Name: op.ID.Name(), ID: op.ID, Ordinal: i, Kind: registry.Modeled, SuccessCode: 200,
			Readonly: op.Traits.Has(traitReadonly), Idempotent: op.Traits.Has(traitIdempotent),
		}
		if op.Traits.Has(utInternalOperation) {
			o.Kind = registry.Internal
		}
		if err := b.operationTraits(o, op); err != nil {
			return err
		}
		var err error
		if o.Input, err = b.ioShape(o, op.Input, "Input", refs); err != nil {
			return err
		}
		if o.Output, err = b.ioShape(o, op.Output, "Output", refs); err != nil {
			return err
		}
		b.ir.Operations = append(b.ir.Operations, o)
	}
	return b.nameShapes()
}

func (b *builder) operationTraits(o *OperationIR, op *Shape) error {
	subject := string(op.ID)
	if raw, ok := op.Traits[traitHTTP]; ok {
		var v struct {
			Method string `json:"method"`
			URI    string `json:"uri"`
			Code   int    `json:"code"`
		}
		if err := decodeTrait(raw, &v); err != nil {
			return failf(StageIR, subject, "%s: %w", traitHTTP, err)
		}
		hb, err := registry.ParseHTTPBinding(v.Method, v.URI)
		if err != nil {
			return failf(StageIR, subject, "%s: %w", traitHTTP, err)
		}
		o.HTTP = &hb
		if v.Code != 0 {
			o.SuccessCode = v.Code
		}
	}
	if raw, ok := op.Traits[utSuccessCode]; ok {
		code, err := parseStatus(raw)
		if err != nil {
			return failf(StageIR, subject, "%w", err)
		}
		o.SuccessCode = code
	}
	if raw, ok := op.Traits[utMaxRequestBytes]; ok {
		n, err := parseMaxRequestBytes(raw)
		if err != nil {
			return failf(StageIR, subject, "%w", err)
		}
		o.MaxRequestBytes = n
	}
	if raw, ok := op.Traits[traitExamples]; ok {
		ex, err := parseExamples(raw)
		if err != nil {
			return failf(StageIR, subject, "%s: %w", traitExamples, err)
		}
		o.Examples = ex
	}
	return nil
}

// parseExamples keeps the examples with an input that is meant to satisfy
// the constraints: one marked allowConstraintErrors shows a failure.
func parseExamples(raw json.RawMessage) ([]ExampleIR, error) {
	var v []struct {
		Title                 string          `json:"title"`
		Documentation         string          `json:"documentation"`
		Input                 json.RawMessage `json:"input"`
		Output                json.RawMessage `json:"output"`
		Error                 json.RawMessage `json:"error"`
		AllowConstraintErrors bool            `json:"allowConstraintErrors"`
	}
	if err := decodeTrait(raw, &v); err != nil {
		return nil, err
	}
	var out []ExampleIR
	for i, e := range v {
		if e.Title == "" {
			return nil, fmt.Errorf("example %d has no title", i)
		}
		if e.Input != nil && !e.AllowConstraintErrors {
			out = append(out, ExampleIR{Title: e.Title, Input: e.Input})
		}
	}
	return out, nil
}

// ioShape returns the IO structure of an operation. A structure only this
// operation uses this way is renamed <Op>Input or <Op>Output; a Unit gets a
// synthetic empty structure of that name.
func (b *builder) ioShape(o *OperationIR, id smithy.ShapeID, suffix string, refs map[smithy.ShapeID]int) (*ShapeIR, error) {
	name := o.Name + suffix
	if id == "smithy.api#Unit" {
		u := &ShapeIR{ID: id, Type: smithy.TypeUnit, GoName: name, SchemaVar: "schemaSmithyUnit", IO: o, Unit: true}
		b.ir.Shapes = append(b.ir.Shapes, u)
		return u, nil
	}
	s, ok := b.shapes[id]
	if !ok || s.Type != smithy.TypeStructure {
		return nil, failf(StageIR, string(o.ID), "%s %s is not a structure", strings.ToLower(suffix), id)
	}
	if s.IO != nil {
		return nil, failf(StageIR, string(o.ID), "shares its %s structure %s with %s; give each operation its own", strings.ToLower(suffix), id, s.IO.Name)
	}
	if refs[id] == 1 {
		s.IO = o
		s.GoName = name
	}
	return s, nil
}

// nameShapes gives every shape without a Go name one, and schema variables.
func (b *builder) nameShapes() error {
	reserved := []string{"Service", "Binder", "NewBinder", "ThrottleError", "UnknownOperationError", "MalformedRequestError"}
	for _, r := range reserved {
		b.goNames[r] = "reserved"
	}
	for _, s := range b.ir.Shapes {
		if s.Unit {
			if err := b.claim(s.GoName, s.ID); err != nil {
				return err
			}
			continue
		}
		if s.GoName == "" {
			s.GoName = exportName(s.ID.Name())
			if s.ID.Namespace() == "smithy.api" {
				s.GoName = "Smithy" + s.GoName
			}
		}
		s.SchemaVar = "schema" + s.GoName
		if declaresGoType(s.Type) {
			if err := b.claim(s.GoName, s.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

// declaresGoType reports whether shapes of type t become a named Go type;
// lists, maps and simple values use Go's own types.
func declaresGoType(t smithy.ShapeType) bool {
	switch t {
	case smithy.TypeStructure, smithy.TypeUnion, smithy.TypeEnum, smithy.TypeIntEnum:
		return true
	case smithy.TypeList, smithy.TypeMap, smithy.TypeString, smithy.TypeBlob, smithy.TypeBoolean,
		smithy.TypeByte, smithy.TypeShort, smithy.TypeInteger, smithy.TypeLong, smithy.TypeFloat,
		smithy.TypeDouble, smithy.TypeBigInteger, smithy.TypeBigDecimal, smithy.TypeTimestamp,
		smithy.TypeDocument, smithy.TypeUnit:
		return false
	default:
		return false
	}
}

func (b *builder) claim(name string, id smithy.ShapeID) error {
	if prev, taken := b.goNames[name]; taken && prev != id {
		return failf(StageIR, string(id), "its Go name %s is already %s", name, prev)
	}
	b.goNames[name] = id
	return nil
}

// members builds every member with effective traits and the regex stage.
func (b *builder) members() error {
	for _, s := range b.ir.Shapes {
		if s.Unit || s.Type == smithy.TypeEnum || s.Type == smithy.TypeIntEnum {
			continue
		}
		src := b.clo.shapes[s.ID]
		goNames := map[string]bool{}
		for i, m := range src.Members {
			mem, err := b.member(s, src, m, i)
			if err != nil {
				return err
			}
			if goNames[mem.GoName] {
				return failf(StageIR, string(s.ID)+"$"+m.Name, "two members map onto the Go field %s", mem.GoName)
			}
			goNames[mem.GoName] = true
			s.Members = append(s.Members, mem)
		}
	}
	return nil
}

func (b *builder) member(owner *ShapeIR, src *Shape, m *Member, index int) (*MemberIR, error) {
	subject := string(src.ID) + "$" + m.Name
	tsrc := b.clo.shapes[m.Target]
	eff := maps.Clone(tsrc.Traits)
	maps.Copy(eff, m.Traits)
	mem := &MemberIR{
		Name: m.Name, GoName: exportName(m.Name), Index: index, Owner: owner, Target: b.shapes[m.Target], Bit: -1,
		Var:          "member" + owner.GoName + "_" + m.Name,
		Required:     eff.Has(traitRequired),
		XMLFlattened: m.Traits.Has(traitXMLFlattened),
		XMLAttribute: m.Traits.Has(traitXMLAttribute),
		Sensitive:    eff.Has(traitSensitive),
		AlwaysEmit:   m.Traits.Has(utAlwaysEmit),
		Empty:        b.emptyDef,
		rulesRaw:     m.Traits[utValidationError],
	}
	if raw, ok := eff[traitDefault]; ok && string(raw) != "null" {
		mem.Default = raw
	}
	if err := memberWireTraits(mem, m.Traits, eff); err != nil {
		return nil, failf(StageEffective, subject, "%w", err)
	}
	if err := b.constraints(mem, eff, tsrc, subject); err != nil {
		return nil, err
	}
	if raw, ok := m.Traits[utEmptyCollection]; ok {
		e, err := parseEmptyCollection(raw)
		if err != nil {
			return nil, failf(StageEffective, subject, "%w", err)
		}
		mem.Empty = e
	}
	return mem, nil
}

// memberWireTraits reads the naming, namespace, timestamp and HTTP binding
// traits of a member: own traits for names and bindings, effective traits
// for the timestamp format.
func memberWireTraits(mem *MemberIR, own, eff Traits) error {
	for _, t := range []struct {
		id  string
		dst *string
	}{{traitJSONName, &mem.JSONName}, {traitXMLName, &mem.XMLName}} {
		if raw, ok := own[t.id]; ok {
			if err := decodeTrait(raw, t.dst); err != nil {
				return fmt.Errorf("%s: %w", t.id, err)
			}
		}
	}
	if raw, ok := own[traitXMLNamespace]; ok {
		ns, err := parseXMLNamespace(raw)
		if err != nil {
			return err
		}
		mem.XMLNamespace = &ns
	}
	if raw, ok := eff[traitTimestampFormat]; ok {
		f, err := parseTimestampFormat(raw)
		if err != nil {
			return err
		}
		mem.TimestampFormat = f
	}
	loc, err := httpLocation(own)
	if err != nil {
		return err
	}
	mem.HTTP = loc
	return nil
}

func httpLocation(t Traits) (string, error) {
	switch {
	case t.Has(traitHTTPLabel):
		return "smithy.HTTPLabel{}", nil
	case t.Has(traitHTTPQueryParams):
		return "smithy.HTTPQueryParams{}", nil
	case t.Has(traitHTTPPayload):
		return "smithy.HTTPPayload{}", nil
	case t.Has(traitHTTPResponse):
		return "smithy.HTTPResponseCode{}", nil
	}
	for _, f := range []struct{ id, typ, field string }{
		{traitHTTPQuery, "HTTPQuery", "Name"}, {traitHTTPHeader, "HTTPHeader", "Name"}, {traitHTTPPrefix, "HTTPPrefixHeaders", "Prefix"},
	} {
		if raw, ok := t[f.id]; ok {
			var v string
			if err := decodeTrait(raw, &v); err != nil {
				return "", err
			}
			return "smithy." + f.typ + "{" + f.field + ": " + goString(v) + "}", nil
		}
	}
	return "", nil
}

// constraints reads length, range and pattern from the effective traits and
// runs the regex stage.
func (b *builder) constraints(mem *MemberIR, eff Traits, target *Shape, subject string) error {
	for _, c := range []struct {
		id  string
		dst **Bounds
	}{{traitLength, &mem.Length}, {traitRange, &mem.Range}} {
		raw, ok := eff[c.id]
		if !ok {
			continue
		}
		var v struct {
			Min *json.Number `json:"min"`
			Max *json.Number `json:"max"`
		}
		if err := decodeTrait(raw, &v); err != nil {
			return failf(StageEffective, subject, "%s: %w", c.id, err)
		}
		bd := &Bounds{}
		if v.Min != nil {
			bd.Min = v.Min.String()
		}
		if v.Max != nil {
			bd.Max = v.Max.String()
		}
		*c.dst = bd
	}
	raw, ok := eff[traitPattern]
	if !ok {
		return nil
	}
	p, err := patternIR(raw, target)
	if err != nil {
		return failf(StageRegex, subject, "%w", err)
	}
	mem.Pattern = p
	return nil
}

// patternIR runs the regex stage on one pattern: the echo AWS prints, and
// either the RE2 translation or the target's hand validator.
func patternIR(raw json.RawMessage, target *Shape) (*PatternIR, error) {
	var src string
	if err := decodeTrait(raw, &src); err != nil {
		return nil, err
	}
	p := &PatternIR{Source: src, Echo: PatternEcho(src)}
	if raw, ok := target.Traits[utPatternEcho]; ok {
		echo, err := parseNonEmptyString(raw)
		if err != nil {
			return nil, err
		}
		p.Echo = echo
	}
	if raw, ok := target.Traits[utPatternValidator]; ok {
		pv, err := parsePatternValidator(raw)
		if err != nil {
			return nil, err
		}
		p.Validator = &pv
		return p, nil
	}
	re2, err := TranslatePattern(src)
	if err != nil {
		return nil, err
	}
	p.RE2 = re2
	return p, nil
}

// roles marks shapes by reachability from inputs and from outputs and errors,
// and decides value or pointer fields and presence tracking.
func (b *builder) roles() error {
	for _, o := range b.ir.Operations {
		markRole(o.Input, RoleInput)
		markRole(o.Output, RoleOutput)
	}
	for _, s := range b.ir.Shapes {
		if !s.Unit && b.clo.shapes[s.ID].Traits.Has(traitError) {
			markRole(s, RoleOutput)
		}
	}
	for _, s := range b.ir.Shapes {
		if err := s.fieldKinds(); err != nil {
			return err
		}
	}
	return nil
}

// patterns names every distinct rendering an input member checks, in the
// order of its rendering key, so one variable serves every member that
// shares it.
func (b *builder) patterns() error {
	byKey := map[string][]*PatternIR{}
	for _, s := range b.ir.Shapes {
		if s.Role&RoleInput == 0 {
			continue
		}
		for _, m := range s.Members {
			if m.Pattern != nil {
				k := patternKey(m.Pattern)
				byKey[k] = append(byKey[k], m.Pattern)
			}
		}
	}
	for i, k := range slices.Sorted(maps.Keys(byKey)) {
		ps := byKey[k]
		v := &PatternVarIR{Name: fmt.Sprintf("pattern%d", i), RE2: ps[0].RE2, Validator: ps[0].Validator}
		for _, p := range ps {
			p.Var = v.Name
			v.Sources = append(v.Sources, p.Source)
		}
		slices.Sort(v.Sources)
		v.Sources = slices.Compact(v.Sources)
		b.ir.Patterns = append(b.ir.Patterns, v)
	}
	return nil
}

// patternKey identifies a pattern by its rendering: two model patterns that
// translate alike share one variable.
func patternKey(p *PatternIR) string {
	if p.Validator == nil {
		return "re2:" + p.RE2
	}
	return "validator:" + strings.Join(p.Validator.AllOf, "\x00") + "\x01" + strings.Join(p.Validator.NoneOf, "\x00")
}

func markRole(s *ShapeIR, r Role) {
	if s.Role&r != 0 {
		return
	}
	s.Role |= r
	for _, m := range s.Members {
		markRole(m.Target, r)
	}
}

// fieldKinds decides, per member, a plain value or a pointer, and gives each
// required member of an input structure a presence bit. A required member
// with a default always has a value, so it needs none.
func (s *ShapeIR) fieldKinds() error {
	for _, m := range s.Members {
		if m.AlwaysEmit && s.Role&RoleOutput == 0 {
			return failf(StageRoles, string(s.ID)+"$"+m.Name, "%s belongs on output members", utAlwaysEmit)
		}
		m.Value = valueField(m)
		if s.Type != smithy.TypeStructure || !m.Required || m.Default != nil || s.Role&RoleInput == 0 {
			continue
		}
		if s.Tracked == 64 {
			return failf(StageRoles, string(s.ID), "has more than 64 required members; presence tracking holds 64")
		}
		m.Bit = s.Tracked
		s.Tracked++
	}
	return nil
}

// valueField reports whether a member is a plain Go value: required,
// defaulted or alwaysEmit scalars and structures. Collections, unions,
// documents, blobs and big integers use nil for absent instead.
func valueField(m *MemberIR) bool {
	switch {
	case m.Owner.Type != smithy.TypeStructure:
		return true
	case nilAbsent(m.Target.Type):
		return false
	case m.Target.Type == smithy.TypeStructure:
		return m.Required
	default:
		return m.Required || m.Default != nil || m.AlwaysEmit
	}
}

// errorIdentities builds an ErrorIR per error structure, each operation's
// error list and the service-level error traits.
func (b *builder) errorIdentities() error {
	svc := b.clo.service
	ns := svc.ID.Namespace()
	if raw, ok := svc.Traits[utErrorNamespace]; ok {
		v, err := parseErrorNamespace(raw)
		if err != nil {
			return failf(StageIR, string(svc.ID), "%w", err)
		}
		ns = v
	}
	for _, s := range b.ir.Shapes {
		if s.Unit || !b.clo.shapes[s.ID].Traits.Has(traitError) {
			continue
		}
		e, err := b.errorIR(s, b.clo.shapes[s.ID], ns)
		if err != nil {
			return err
		}
		s.Error = e
		b.errors[s.ID] = e
		b.ir.Errors = append(b.ir.Errors, e)
	}
	slices.SortFunc(b.ir.Errors, func(a, c *ErrorIR) int { return cmp.Compare(a.Shape.GoName, c.Shape.GoName) })
	if err := b.operationErrors(); err != nil {
		return err
	}
	return b.serviceErrors()
}

// operationErrors lists each operation's modeled errors, then the service's.
func (b *builder) operationErrors() error {
	for _, op := range b.ir.Operations {
		src := b.model.Shapes[op.ID]
		for _, id := range slices.Concat(src.Errors, b.clo.service.Errors) {
			e, ok := b.errors[id]
			if !ok {
				return failf(StageIR, string(op.ID), "lists %s as an error, but it has no error trait", id)
			}
			op.Errors = append(op.Errors, e)
		}
	}
	return nil
}

// serviceErrors resolves throttleError, malformedRequestError and
// unknownOperationError.
func (b *builder) serviceErrors() error {
	var err error
	if b.ir.Throttle, err = b.serviceError(utThrottleError); err != nil {
		return err
	}
	if b.ir.Throttle != nil {
		// The throttle identity is the service's member of the throttling
		// set, which awserr.Identity.Retryable covers with the trait.
		b.ir.Throttle.Retryable = true
	}
	if b.ir.MalformedRequest, err = b.serviceError(utMalformedRequest); err != nil {
		return err
	}
	svc := b.clo.service
	raw, ok := svc.Traits[utUnknownOperation]
	if !ok {
		return nil
	}
	u, err := parseUnknownOperation(raw)
	if err != nil {
		return failf(StageIR, string(svc.ID), "%w", err)
	}
	if b.ir.UnknownJSON, err = b.errorByID(utUnknownOperation, u.JSON); err != nil {
		return err
	}
	b.ir.UnknownQuery, err = b.errorByID(utUnknownOperation, u.Query)
	return err
}

func (b *builder) serviceError(trait string) (*ErrorIR, error) {
	raw, ok := b.clo.service.Traits[trait]
	if !ok {
		return nil, nil //nolint:nilnil // an absent trait has no identity, which is not an error
	}
	id, err := parseShapeRef(raw)
	if err != nil {
		return nil, failf(StageIR, string(b.clo.service.ID), "%s: %w", trait, err)
	}
	return b.errorByID(trait, id)
}

func (b *builder) errorByID(trait string, id smithy.ShapeID) (*ErrorIR, error) {
	if id == "" {
		return nil, nil //nolint:nilnil // an absent reference has no identity, which is not an error
	}
	e, ok := b.errors[id]
	if !ok {
		return nil, failf(StageIR, string(b.clo.service.ID), "%s names %s, which is not an error structure", trait, id)
	}
	return e, nil
}

// errorIR derives an error shape's identity (error-matrix.md 7, D7 to D9):
// status from httpError or the fault default, then statusOverride; the
// Query code from awsQueryError, else the shape name; __type from jsonType
// or the shape name, namespaced for awsJson1_0; the message key from the
// shape's own member spelling.
func (b *builder) errorIR(s *ShapeIR, src *Shape, namespace string) (*ErrorIR, error) {
	subject := string(s.ID)
	e := &ErrorIR{Shape: s, QueryCode: s.ID.Name(), Retryable: src.Traits.Has(traitRetryable), Generic: s.ID.Namespace() != b.cfg.Service.Namespace()}
	if err := e.status(src.Traits); err != nil {
		return nil, failf(StageIR, subject, "%w", err)
	}
	if err := e.queryError(src.Traits); err != nil {
		return nil, failf(StageIR, subject, "%w", err)
	}
	name := s.ID.Name()
	if raw, ok := src.Traits[utJSONType]; ok {
		v, err := parseNonEmptyString(raw)
		if err != nil {
			return nil, failf(StageIR, subject, "%w", err)
		}
		name = v
	}
	e.JSONType = name
	if slices.Contains(b.ir.Protocols, ref.AWSJSON10) {
		ns := namespace
		if e.Generic {
			ns = s.ID.Namespace()
		}
		e.JSONType = ns + "#" + name
	}
	e.MessageKey = awserr.NoMessage
	for _, m := range src.Members {
		switch m.Name {
		case "message":
			e.MessageKey = awserr.MessageLower
		case "Message":
			e.MessageKey = awserr.MessageUpper
		}
	}
	if raw, ok := src.Traits[utMessageByProtocol]; ok {
		msgs, err := parseMessageByProtocol(raw)
		if err != nil {
			return nil, failf(StageIR, subject, "%w", err)
		}
		e.Messages = msgs
	}
	return e, nil
}

// status reads the fault, httpError and statusOverride.
func (e *ErrorIR) status(traits Traits) error {
	var fault string
	if err := decodeTrait(traits[traitError], &fault); err != nil {
		return fmt.Errorf("%s: %w", traitError, err)
	}
	switch fault {
	case "client":
		e.Fault, e.Status = awserr.Sender, 400
	case "server":
		e.Fault, e.Status = awserr.Receiver, 500
	default:
		return fmt.Errorf("%s is %q; it must be client or server", traitError, fault)
	}
	if raw, ok := traits[traitHTTPError]; ok {
		if err := decodeTrait(raw, &e.HTTPError); err != nil {
			return fmt.Errorf("%s: %w", traitHTTPError, err)
		}
		e.Status = e.HTTPError
	}
	if raw, ok := traits[utStatusOverride]; ok {
		st, err := parseStatus(raw)
		if err != nil {
			return fmt.Errorf("%s: %w", utStatusOverride, err)
		}
		e.Status = st
	}
	return nil
}

// queryError reads awsQueryError: the Query code, and the status under
// x-amzn-query-mode when it differs from the JSON status.
func (e *ErrorIR) queryError(traits Traits) error {
	raw, ok := traits[traitQueryError]
	if !ok {
		return nil
	}
	var v struct {
		Code             string `json:"code"`
		HTTPResponseCode int    `json:"httpResponseCode"`
	}
	if err := decodeTrait(raw, &v); err != nil {
		return fmt.Errorf("%s: %w", traitQueryError, err)
	}
	e.QueryCode = v.Code
	if v.HTTPResponseCode != e.Status {
		e.QueryModeStatus = v.HTTPResponseCode
	}
	return nil
}

// rules resolves validationError rules against the error identities.
func (b *builder) rules(subject string, raw json.RawMessage) ([]RuleIR, error) {
	parsed, err := parseValidationRules(raw)
	if err != nil {
		return nil, failf(StageIR, subject, "%s: %w", utValidationError, err)
	}
	out := make([]RuleIR, 0, len(parsed))
	for _, r := range parsed {
		ir := RuleIR{Style: r.Style, Kinds: r.Kinds, Reason: r.Reason, EnumPolicy: r.EnumPolicy}
		if r.Code != "" {
			e, ok := b.errors[r.Code]
			if !ok {
				return nil, failf(StageIR, subject, "%s code %s is not an error structure in the model or an overlay", utValidationError, r.Code)
			}
			ir.Error = e
		}
		out = append(out, ir)
	}
	return out, nil
}

// policies resolves validation rules at every level, enum policies and the
// per-operation path tables, and adds validation identities to each
// operation's error list.
func (b *builder) policies() error {
	if err := b.serviceRules(); err != nil {
		return err
	}
	if err := b.memberRules(); err != nil {
		return err
	}
	for _, o := range b.ir.Operations {
		if err := b.operationRules(o); err != nil {
			return err
		}
		o.Paths = inputPaths(o.Input)
		b.addValidationErrors(o)
	}
	return nil
}

// serviceRules reads the mandatory service-level validationError, which
// needs an unfiltered rule so every violation has a code.
func (b *builder) serviceRules() error {
	svc := b.clo.service
	raw, ok := svc.Traits[utValidationError]
	if !ok {
		return failf(StageIR, string(svc.ID), "has no %s; every service states its constraint-failure code (ValidationException, InvalidParameterValue, ...) on its service shape in its overlay", utValidationError)
	}
	rules, err := b.rules(string(svc.ID), raw)
	if err != nil {
		return err
	}
	b.ir.Validation = rules
	if !slices.ContainsFunc(rules, func(r RuleIR) bool { return r.Error != nil && r.Kinds == 0 }) {
		return failf(StageIR, string(svc.ID), "%s needs a rule without a constraints filter, so every violation has a code", utValidationError)
	}
	for _, r := range rules {
		if r.EnumPolicy != 0 {
			b.enumDef = r.EnumPolicy
		}
	}
	return nil
}

// memberRules resolves member-level rules and each member's enum policy.
func (b *builder) memberRules() error {
	for _, s := range b.ir.Shapes {
		for _, m := range s.Members {
			if m.rulesRaw != nil {
				var err error
				if m.Rules, err = b.rules(string(s.ID)+"$"+m.Name, m.rulesRaw); err != nil {
					return err
				}
			}
			m.EnumPolicy = b.enumDef
			for _, r := range m.Rules {
				if r.EnumPolicy != 0 {
					m.EnumPolicy = r.EnumPolicy
				}
			}
		}
	}
	return nil
}

// operationRules resolves an operation's rules; enumPolicy is not one of
// them, as the enum check is generated per member.
func (b *builder) operationRules(o *OperationIR) error {
	raw, ok := b.model.Shapes[o.ID].Traits[utValidationError]
	if !ok {
		return nil
	}
	var err error
	if o.Validation, err = b.rules(string(o.ID), raw); err != nil {
		return err
	}
	for _, r := range o.Validation {
		if r.EnumPolicy != 0 {
			return failf(StageIR, string(o.ID), "enumPolicy applies at service or member level, where the validator that checks the enum is generated")
		}
	}
	return nil
}

func (b *builder) addValidationErrors(o *OperationIR) {
	add := func(rules []RuleIR) {
		for _, r := range rules {
			if r.Error != nil && !slices.Contains(o.Errors, r.Error) {
				o.Errors = append(o.Errors, r.Error)
			}
		}
	}
	for _, p := range o.Paths {
		add(p.Rules)
	}
	add(o.Validation)
	add(b.ir.Validation)
}

// inputPaths enumerates the member paths of an input structure, list
// elements (prefix.*.member), map keys (prefix.*) and map values
// (prefix.*.member) included, so a rule on a collection member applies. A
// collection's elements, keys and values name the structure member that
// holds the collection as their parameter, as SQS's hand text does. A shape
// already on the current path stops the walk, so recursive shapes end.
func inputPaths(in *ShapeIR) []PathIR {
	var out []PathIR
	var walk func(s *ShapeIR, prefix []string, param string, onPath map[*ShapeIR]bool)
	walk = func(s *ShapeIR, prefix []string, param string, onPath map[*ShapeIR]bool) {
		if onPath[s] {
			return
		}
		onPath[s] = true
		defer delete(onPath, s)
		switch s.Type {
		case smithy.TypeStructure, smithy.TypeUnion:
			for _, m := range s.Members {
				p := append(slices.Clip(prefix), lowerCamel(m.Name))
				out = append(out, PathIR{Segments: p, Param: m.Name, Rules: m.Rules})
				walk(m.Target, p, m.Name, onPath)
			}
		case smithy.TypeList:
			elem := s.Members[0]
			p := append(slices.Clip(prefix), "*", "member")
			out = append(out, PathIR{Segments: p, Param: param, Rules: elem.Rules})
			walk(elem.Target, p, param, onPath)
		case smithy.TypeMap:
			key, val := s.Members[0], s.Members[1]
			out = append(out, PathIR{Segments: append(slices.Clip(prefix), "*"), Param: param, Rules: key.Rules})
			p := append(slices.Clip(prefix), "*", "member")
			out = append(out, PathIR{Segments: p, Param: param, Rules: val.Rules})
			walk(val.Target, p, param, onPath)
		case smithy.TypeString, smithy.TypeEnum, smithy.TypeIntEnum, smithy.TypeBlob, smithy.TypeBoolean,
			smithy.TypeByte, smithy.TypeShort, smithy.TypeInteger, smithy.TypeLong, smithy.TypeFloat,
			smithy.TypeDouble, smithy.TypeBigInteger, smithy.TypeBigDecimal, smithy.TypeTimestamp,
			smithy.TypeDocument, smithy.TypeUnit:
			// A simple value ends the path.
		}
	}
	walk(in, nil, "", map[*ShapeIR]bool{})
	return out
}

// exportName makes a Smithy identifier an exported Go identifier.
func exportName(s string) string {
	r := []rune(s)
	r[0] = unicode.ToUpper(r[0])
	out := string(r)
	if !token.IsIdentifier(out) {
		out = strings.Map(func(c rune) rune {
			if unicode.IsLetter(c) || unicode.IsDigit(c) || c == '_' {
				return c
			}
			return '_'
		}, out)
	}
	return out
}

// lowerCamel is the member name with its first letter lower-cased, the
// path segment AWS prints (error-matrix.md 4).
func lowerCamel(s string) string {
	r := []rune(s)
	r[0] = unicode.ToLower(r[0])
	return string(r)
}

// enumConstName turns an enum member name into a Go constant suffix:
// SENDER_ID becomes SenderId, AWSTraceHeader stays.
func enumConstName(s string) string {
	if !strings.Contains(s, "_") && strings.ToUpper(s) != s {
		return exportName(s)
	}
	var b strings.Builder
	for part := range strings.SplitSeq(s, "_") {
		if part == "" {
			continue
		}
		r := []rune(strings.ToLower(part))
		r[0] = unicode.ToUpper(r[0])
		b.WriteString(string(r))
	}
	out := b.String()
	if first, _ := utf8.DecodeRuneInString(out); out == "" || !unicode.IsLetter(first) {
		out = "Value" + out
	}
	return exportName(out)
}
