package codegen

import (
	_ "embed" // policySource
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"slices"
	"strconv"
	"strings"
)

// policySource is the hand-written validation policy runtime (package
// validationpolicy), compiled, linted and tested there; every
// validate_gen.go carries its declarations.
//
//go:embed validationpolicy/policy.go
var policySource []byte

// policyRuntime returns the declarations of the policy runtime without its
// package clause and imports, recording the imports in f. The runtime may
// import only what generated code may.
func policyRuntime(f *file) (string, error) {
	const subject = "internal/codegen/validationpolicy/policy.go"
	fset := token.NewFileSet()
	src, err := parser.ParseFile(fset, subject, policySource, parser.ImportsOnly)
	if err != nil {
		return "", failf(StageEmit, subject, "the embedded policy runtime does not parse: %w", err)
	}
	byPath := map[string]string{}
	for name, path := range importPaths() {
		byPath[path] = name
	}
	for _, imp := range src.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			return "", failf(StageEmit, subject, "import %s: %w", imp.Path.Value, err)
		}
		name, ok := byPath[path]
		if !ok || imp.Name != nil {
			return "", failf(StageEmit, subject, "imports %s, which generated code may not use unrenamed; keep the runtime to the packages in importPaths", path)
		}
		f.use(name)
	}
	end := src.Name.End()
	for _, d := range src.Decls {
		if g, ok := d.(*ast.GenDecl); ok && g.Tok == token.IMPORT {
			end = g.End()
		}
	}
	return strings.TrimSpace(string(policySource[fset.Position(end).Offset:])), nil
}

// emitPolicies writes the policy runtime, one policy per operation and the
// ValidationError methods.
func emitPolicies(f *file, ir *ServiceIR) error {
	runtime, err := policyRuntime(f)
	if err != nil {
		return err
	}
	f.p("%s\n", runtime)
	service := rulesLit(ir.Validation)
	for _, o := range ir.Operations {
		pv := "policy" + o.Name
		f.p("var %s = validationPolicy{", pv)
		f.p("\tpaths: []validationPath{")
		for _, p := range o.Paths {
			segs := make([]string, len(p.Segments))
			for i, s := range p.Segments {
				segs[i] = goString(s)
			}
			line := fmt.Sprintf("\t\t{segments: []string{%s}, param: %s", strings.Join(segs, ", "), goString(p.Param))
			if hasCodes(p.Rules) {
				line += ", rules: " + rulesLit(p.Rules)
			}
			f.p("%s},", line)
		}
		f.p("\t},")
		if hasCodes(o.Validation) {
			f.p("\toperation: %s,", rulesLit(o.Validation))
		}
		f.p("\tservice: %s,", service)
		f.p("}\n")
		f.p("// ValidationError returns the error %s answers for vs, the", o.Name)
		f.p("// violations of the generated and the hand-written validators, or nil")
		f.p("// when vs is empty.")
		f.p("func (*%s) ValidationError(vs []awserr.Violation) *awserr.Error { return %s.render(vs) }\n", o.Input.GoName, pv)
	}
	return nil
}

func rulesLit(rules []RuleIR) string {
	var parts []string
	for _, r := range rules {
		if r.Error == nil {
			continue
		}
		s := "{"
		if r.Kinds != 0 {
			s += "kinds: " + kindsLit(r.Kinds) + ", "
		}
		s += "id: Identity" + r.Error.Shape.GoName + ", style: " + styleLit(r.Style)
		if r.Reason != "" {
			s += ", reason: " + goString(r.Reason)
		}
		parts = append(parts, s+"}")
	}
	return "[]validationRule{" + strings.Join(parts, ", ") + "}"
}

// hasCodes reports whether any rule names an error, so a rule list that
// only sets an enum policy emits nothing.
func hasCodes(rules []RuleIR) bool {
	return slices.ContainsFunc(rules, func(r RuleIR) bool { return r.Error != nil })
}

// kindsLit names the runtime constants of a constraint-kind filter.
func kindsLit(k ConstraintKind) string {
	var names []string
	for _, c := range []struct {
		kind ConstraintKind
		name string
	}{{KindRequired, "kindRequired"}, {KindLength, "kindLength"}, {KindRange, "kindRange"}, {KindPattern, "kindPattern"}, {KindEnum, "kindEnum"}} {
		if k&c.kind != 0 {
			names = append(names, c.name)
		}
	}
	return strings.Join(names, " | ")
}

// styleLit names the runtime constant of a message style.
func styleLit(s ValidationStyle) string {
	switch s {
	case StyleCoral:
		return "styleCoral"
	case StyleParameter:
		return "styleParameter"
	case StyleMissingParameter:
		return "styleMissingParameter"
	case StyleSNSParameter:
		return "styleSNSParameter"
	default:
		return "styleCoral"
	}
}

// emitPatterns declares the pattern variables the IR names.
func emitPatterns(f *file, ir *ServiceIR) {
	if len(ir.Patterns) == 0 {
		return
	}
	f.use("regexp")
	for _, v := range ir.Patterns {
		sources := make([]string, len(v.Sources))
		for i, s := range v.Sources {
			sources[i] = goString(s)
		}
		f.p("// %s is %s.", v.Name, strings.Join(sources, ", also written "))
		if v.Validator == nil {
			f.p("var %s = regexp.MustCompile(%s)\n", v.Name, goString(v.RE2))
			continue
		}
		for j, re := range v.Validator.AllOf {
			f.p("var %sAll%d = regexp.MustCompile(%s)", v.Name, j, goString(re))
		}
		for j, re := range v.Validator.NoneOf {
			f.p("var %sNone%d = regexp.MustCompile(%s)", v.Name, j, goString(re))
		}
		f.p("")
	}
}
