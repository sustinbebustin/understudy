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
// one smithy.Member per member, linked in init because shapes may refer to
// each other in cycles a variable initializer cannot express.
func emitSchema(ir *ServiceIR) ([]byte, error) {
	f := newFile(ir, "schema_gen.go")
	f.use("smithy")
	var links []string
	for _, s := range sortedShapes(ir) {
		if s.Unit {
			continue
		}
		f.p("var %s = smithy.Schema{%s}\n", s.SchemaVar, f.schemaFields(s))
		if len(s.Members) == 0 {
			continue
		}
		vars := make([]string, len(s.Members))
		for i, m := range s.Members {
			fields, err := f.memberFields(m)
			if err != nil {
				return nil, err
			}
			f.p("var %s = smithy.Member{%s}\n", m.Var, fields)
			vars[i] = "&" + m.Var
			links = append(links, fmt.Sprintf("%s.Target = &%s", m.Var, m.Target.SchemaVar))
		}
		links = append(links, fmt.Sprintf("%s.Members = []*smithy.Member{%s}", s.SchemaVar, strings.Join(vars, ", ")))
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

func (f *file) schemaFields(s *ShapeIR) string {
	parts := []string{"ID: " + goString(string(s.ID)), "Type: smithy." + typeConst(s.Type)}
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
	parts := []string{"Name: " + goString(m.Name), fmt.Sprintf("Index: %d", m.Index)}
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
	if m.HTTP != "" {
		parts = append(parts, "HTTP: "+m.HTTP)
	}
	return strings.Join(parts, ", "), nil
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
