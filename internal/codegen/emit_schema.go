package codegen

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
)

// emitSchema writes schema_gen.go: one immutable smithy.Schema per shape and
// one smithy.Member per member, linked statically: a member's Target and a
// schema's Members are fields of the variables' literals, so the linker lays
// the graph out as data rather than as init code. A recursive shape is the
// exception: a variable initializer cannot reach its own variable, so its
// Members are assigned in init. A shape wiredShapes leaves out gets no
// Members; its member variables are still declared, with their Targets, for
// the serde code the linker drops.
func emitSchema(ir *ServiceIR) ([]byte, error) {
	f := newFile(ir, "schema_gen.go")
	f.use("smithy")
	shapes := slices.DeleteFunc(sortedShapes(ir), func(s *ShapeIR) bool { return s.Unit })
	wired := wiredShapes(ir, shapes)
	recursive := recursiveShapes(shapes, wired)
	var links []string
	for _, s := range shapes {
		vars := make([]string, len(s.Members))
		for i, m := range s.Members {
			vars[i] = "&" + m.Var
		}
		members := ""
		if len(vars) > 0 && wired[s] {
			members = "[]*smithy.Member{" + strings.Join(vars, ", ") + "}"
		}
		static := members
		if recursive[s] && members != "" {
			links = append(links, s.SchemaVar+".Members = "+members)
			static = ""
		}
		f.p("var %s = smithy.Schema{%s}\n", s.SchemaVar, f.schemaFields(s, static))
		for _, m := range s.Members {
			fields, err := f.memberFields(m)
			if err != nil {
				return nil, err
			}
			f.p("var %s = smithy.Member{%s}\n", m.Var, fields)
		}
	}
	if len(links) > 0 {
		f.p("func init() {")
		for _, l := range links {
			f.p("\t%s", l)
		}
		f.p("}")
	}
	return f.bytes()
}

// wiredShapes reports the shapes whose schemas carry their members: every
// shape under WireAll. Under WireBound, the shapes the error shapes and the
// bound operations' inputs and outputs reach, which are all a request to a
// bound operation decodes or encodes; an unbound operation answers 501
// before it reads its input, and coverage reads only schema IDs. One reader
// precedes that 501: signature verification of a service that checks
// payloads looks one level into the input for a @streaming member
// (edge.streamsInput, restxml Codec.BodyLimit), so an unbound input with a
// member targeting a @streaming shape keeps its own members; their targets
// carry Streaming in their literals.
func wiredShapes(ir *ServiceIR, shapes []*ShapeIR) map[*ShapeIR]bool {
	wired := map[*ShapeIR]bool{}
	bound, ok := ir.Wiring.(WireBound)
	if !ok {
		for _, s := range shapes {
			wired[s] = true
		}
		return wired
	}
	var walk func(s *ShapeIR)
	walk = func(s *ShapeIR) {
		if wired[s] {
			return
		}
		wired[s] = true
		for _, m := range s.Members {
			walk(m.Target)
		}
	}
	for _, s := range shapes {
		if s.Error != nil {
			walk(s)
		}
	}
	isBound := func(o *OperationIR) bool { return slices.Contains(bound.Operations, o.Name) }
	for _, o := range ir.Operations {
		if o.HasIO() && isBound(o) {
			walk(o.Input)
			walk(o.Output)
		}
	}
	// After every walk: walk stops at a wired shape, so an input marked
	// before a bound closure reached it would leave its targets unwired.
	streams := func(m *MemberIR) bool { return m.Target.Streaming }
	for _, o := range ir.Operations {
		if o.HasIO() && !isBound(o) && slices.ContainsFunc(o.Input.Members, streams) {
			wired[o.Input] = true
		}
	}
	return wired
}

// recursiveShapes reports the wired shapes on a cycle of the schema graph,
// whose edges run from a wired shape to its members' targets: the shapes of
// a strongly connected component (Tarjan) with more than one shape, and the
// shapes that target themselves. Every cycle runs only through such shapes,
// so leaving their Members out of the literals leaves no initialization
// cycle.
func recursiveShapes(shapes []*ShapeIR, wired map[*ShapeIR]bool) map[*ShapeIR]bool {
	t := &tarjan{wired: wired, index: map[*ShapeIR]int{}, low: map[*ShapeIR]int{}, onStack: map[*ShapeIR]bool{}, out: map[*ShapeIR]bool{}}
	for _, s := range shapes {
		if _, seen := t.index[s]; !seen {
			t.visit(s)
		}
	}
	return t.out
}

// tarjan is the state of recursiveShapes' walk.
type tarjan struct {
	wired   map[*ShapeIR]bool
	index   map[*ShapeIR]int
	low     map[*ShapeIR]int
	onStack map[*ShapeIR]bool
	stack   []*ShapeIR
	out     map[*ShapeIR]bool
}

func (t *tarjan) visit(s *ShapeIR) {
	t.index[s], t.low[s] = len(t.index), len(t.index)
	t.stack = append(t.stack, s)
	t.onStack[s] = true
	if t.wired[s] {
		for _, m := range s.Members {
			t.edge(s, m.Target)
		}
	}
	if t.low[s] != t.index[s] {
		return
	}
	i := slices.Index(t.stack, s)
	component := t.stack[i:]
	t.stack = t.stack[:i]
	for _, c := range component {
		t.onStack[c] = false
		if len(component) > 1 {
			t.out[c] = true
		}
	}
}

func (t *tarjan) edge(from, to *ShapeIR) {
	if to == from {
		t.out[from] = true
	}
	if _, seen := t.index[to]; !seen {
		t.visit(to)
		t.low[from] = min(t.low[from], t.low[to])
	} else if t.onStack[to] {
		t.low[from] = min(t.low[from], t.index[to])
	}
}

// schemaFields is the literal of s's smithy.Schema; members is the literal
// of its Members, empty to leave them out.
func (f *file) schemaFields(s *ShapeIR, members string) string {
	parts := []string{"ID: " + goString(string(s.ID)), "Type: smithy." + typeConst(s.Type)}
	if members != "" {
		parts = append(parts, "Members: "+members)
	}
	if s.XMLName != "" {
		parts = append(parts, "XMLName: "+goString(s.XMLName))
	}
	if s.XMLNamespace != nil {
		parts = append(parts, "XMLNamespace: "+xmlNamespaceLit(s.XMLNamespace))
	}
	if s.TimestampFormat != smithy.TimestampProtocolDefault {
		parts = append(parts, "TimestampFormat: smithy."+timestampConst(s.TimestampFormat))
	}
	if s.MediaType != "" {
		parts = append(parts, "MediaType: "+goString(s.MediaType))
	}
	if s.Sensitive {
		parts = append(parts, "Sensitive: true")
	}
	if s.Sparse {
		parts = append(parts, "Sparse: true")
	}
	if s.Streaming {
		parts = append(parts, "Streaming: true")
	}
	if s.Error != nil {
		fault := "smithy.ClientFault"
		if s.Error.Fault == awserr.Receiver {
			fault = "smithy.ServerFault"
		}
		status := ""
		if s.Error.HTTPError != 0 {
			status = fmt.Sprintf(", HTTPError: %d", s.Error.HTTPError)
		}
		parts = append(parts, "Error: &smithy.ErrorTrait{Fault: "+fault+status+"}")
	}
	return strings.Join(parts, ", ")
}

func (f *file) memberFields(m *MemberIR) (string, error) {
	parts := []string{"Name: " + goString(m.Name), fmt.Sprintf("Index: %d", m.Index), "Target: &" + m.Target.SchemaVar}
	if m.Required {
		parts = append(parts, "Required: true")
	}
	if m.Default != nil {
		lit, err := f.documentLit(m.Default)
		if err != nil {
			return "", failf(StageEmit, m.Var, "default: %w", err)
		}
		parts = append(parts, "Default: "+lit)
	}
	for _, s := range []struct{ name, v string }{{"JSONName", m.JSONName}, {"XMLName", m.XMLName}} {
		if s.v != "" {
			parts = append(parts, s.name+": "+goString(s.v))
		}
	}
	if m.XMLFlattened {
		parts = append(parts, "XMLFlattened: true")
	}
	if m.XMLAttribute {
		parts = append(parts, "XMLAttribute: true")
	}
	if m.XMLNamespace != nil {
		parts = append(parts, "XMLNamespace: "+xmlNamespaceLit(m.XMLNamespace))
	}
	if m.TimestampFormat != smithy.TimestampProtocolDefault {
		parts = append(parts, "TimestampFormat: smithy."+timestampConst(m.TimestampFormat))
	}
	if m.Sensitive {
		parts = append(parts, "Sensitive: true")
	}
	if m.HTTP != nil {
		parts = append(parts, "HTTP: "+locationLit(m.HTTP))
	}
	if a := m.QueryKeyAlias; a != nil {
		parts = append(parts, "QueryKeyAlias: &smithy.QueryKeyAlias{Key: "+goString(a.Key)+", Decode: smithy."+aliasDecodeConst(a.Decode)+"}")
	}
	return strings.Join(parts, ", "), nil
}

// aliasDecodeConst is the smithy constant naming d.
func aliasDecodeConst(d smithy.AliasDecode) string {
	switch d {
	case smithy.AliasReceive:
		return "AliasReceive"
	case smithy.AliasIgnore:
		return "AliasIgnore"
	}
	return fmt.Sprintf("AliasDecode(%d)", d)
}

// locationLit is the Go literal of an HTTP binding location.
func locationLit(l smithy.Location) string {
	switch l := l.(type) {
	case smithy.HTTPLabel:
		return "smithy.HTTPLabel{}"
	case smithy.HTTPQuery:
		return "smithy.HTTPQuery{Name: " + goString(l.Name) + "}"
	case smithy.HTTPQueryParams:
		return "smithy.HTTPQueryParams{}"
	case smithy.HTTPHeader:
		if l.Trailer {
			return "smithy.HTTPHeader{Name: " + goString(l.Name) + ", Trailer: true}"
		}
		return "smithy.HTTPHeader{Name: " + goString(l.Name) + "}"
	case smithy.HTTPPrefixHeaders:
		return "smithy.HTTPPrefixHeaders{Prefix: " + goString(l.Prefix) + "}"
	case smithy.HTTPPayload:
		return "smithy.HTTPPayload{}"
	case smithy.HTTPResponseCode:
		return "smithy.HTTPResponseCode{}"
	}
	return "nil"
}

func xmlNamespaceLit(ns *smithy.XMLNamespace) string {
	if ns.Prefix == "" {
		return fmt.Sprintf("&smithy.XMLNamespace{URI: %s}", goString(ns.URI))
	}
	return fmt.Sprintf("&smithy.XMLNamespace{URI: %s, Prefix: %s}", goString(ns.URI), goString(ns.Prefix))
}

// documentLit renders a JSON value as a document.Value literal.
func (f *file) documentLit(raw json.RawMessage) (string, error) {
	f.use("document")
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return "", err
	}
	return documentValue(v), nil
}

func documentValue(v any) string {
	switch x := v.(type) {
	case nil:
		return "document.Null{}"
	case bool:
		return fmt.Sprintf("document.Bool(%t)", x)
	case json.Number:
		return "document.Number(" + goString(x.String()) + ")"
	case string:
		return "document.String(" + goString(x) + ")"
	case []any:
		items := make([]string, len(x))
		for i, e := range x {
			items[i] = documentValue(e)
		}
		return "document.List{" + strings.Join(items, ", ") + "}"
	case map[string]any:
		var entries []string
		for _, k := range slices.Sorted(maps.Keys(x)) {
			entries = append(entries, fmt.Sprintf("{Key: %s, Value: %s}", goString(k), documentValue(x[k])))
		}
		return "document.Map{" + strings.Join(entries, ", ") + "}"
	default:
		return "document.Null{}"
	}
}

func typeConst(t smithy.ShapeType) string {
	s := t.String()
	return "Type" + strings.ToUpper(s[:1]) + s[1:]
}

func timestampConst(t smithy.TimestampFormat) string {
	switch t {
	case smithy.DateTime:
		return "DateTime"
	case smithy.HTTPDate:
		return "HTTPDate"
	case smithy.EpochSeconds:
		return "EpochSeconds"
	case smithy.TimestampProtocolDefault:
		return "TimestampProtocolDefault"
	default:
		return "TimestampProtocolDefault"
	}
}
