package codegen

import (
	"fmt"
	"strings"

	"github.com/sustinbebustin/understudy/internal/registry"
)

// emitService writes service_gen.go: the package comment and the immutable
// registry.Service.
func emitService(ir *ServiceIR) ([]byte, error) {
	f := newFile(ir, "service_gen.go")
	f.doc = []string{
		fmt.Sprintf("Package %s is the generated API of %s:", ir.Package, ir.Shape),
		"its shapes, serde, validation, errors and registry descriptors, from",
		fmt.Sprintf("api-models-aws@%s %s and the codegen overlays.", ir.Pin, ir.ModelFile),
		"Do not edit the *_gen.go files: change the model pin, the overlays or",
		"the generator, and regenerate.",
	}
	f.use("registry", "ref")
	protos := make([]string, len(ir.Protocols))
	for i, p := range ir.Protocols {
		protos[i] = "ref." + protocolConst(p)
	}
	f.p("var service = registry.Service{")
	f.p("\tID: %s,", goString(string(ir.ID)))
	f.p("\tShape: %s,", goString(string(ir.Shape)))
	for _, s := range []struct{ name, v string }{
		{"SigningName", ir.SigningName}, {"IAMPrefix", ir.IAMPrefix}, {"ARNNamespace", ir.ARNNamespace},
	} {
		f.p("\t%s: %s,", s.name, goString(s.v))
	}
	f.p("\tProtocols: []ref.ProtocolID{%s},", strings.Join(protos, ", "))
	for _, s := range []struct{ name, v string }{
		{"JSONTargetPrefix", ir.JSONTargetPrefix}, {"QueryVersion", ir.QueryVersion}, {"XMLNamespace", ir.XMLNamespace},
	} {
		if s.v != "" {
			f.p("\t%s: %s,", s.name, goString(s.v))
		}
	}
	if ir.QueryCompatible {
		f.p("\tQueryCompatible: true,")
	}
	if e := ir.MalformedRequest; e != nil {
		f.p("\tMalformedRequestError: Identity%s(),", e.Shape.GoName)
	}
	f.p("}\n")
	vars := make([]string, len(ir.Operations))
	for i, o := range ir.Operations {
		vars[i] = "&desc" + o.Name
	}
	f.p("// The operations point back at the service, so the list is linked once")
	f.p("// at init; nothing changes the descriptors after that.")
	f.p("func init() {")
	f.p("\tservice.Operations = []*registry.Operation{%s}", strings.Join(vars, ", "))
	f.p("}\n")
	f.p("// Service returns the %s descriptor with every operation. It is", ir.ID)
	f.p("// immutable: bindings live in each kernel, indexed by Ordinal.")
	f.p("func Service() *registry.Service { return &service }")
	return f.bytes()
}

// emitOps writes ops_gen.go: one immutable registry.Operation per operation.
func emitOps(ir *ServiceIR) ([]byte, error) {
	f := newFile(ir, "ops_gen.go")
	f.use("registry", "ref", "awserr")
	for _, o := range ir.Operations {
		kind := "registry.Modeled"
		iam := fmt.Sprintf("[]registry.IAMAction{{Prefix: %s, Name: %s}}", goString(ir.IAMPrefix), goString(o.Name))
		if o.Kind == registry.Internal {
			kind = "registry.Internal"
			iam = "nil"
		}
		f.p("var desc%s = registry.Operation{", o.Name)
		f.p("\tService: &service,")
		f.p("\tID: ref.OperationID{Service: %s, Name: %s},", goString(string(ir.ID)), goString(o.Name))
		f.p("\tOrdinal: %d,", o.Ordinal)
		f.p("\tKind: %s,", kind)
		f.p("\tShape: %s,", goString(string(o.ID)))
		f.p("\tInput: &%s,", o.Input.SchemaVar)
		f.p("\tOutput: &%s,", o.Output.SchemaVar)
		f.p("\tErrors: %s,", errorList(o.Errors))
		f.p("\tIAM: %s,", iam)
		if o.HTTP != nil {
			f.p("\tHTTP: %s,", httpBindingLit(o.HTTP))
		}
		f.p("\tSuccessCode: %d,", o.SuccessCode)
		if o.MaxRequestBytes > 0 {
			f.p("\tMaxRequestBytes: %d,", o.MaxRequestBytes)
		}
		if o.Readonly {
			f.p("\tReadonly: true,")
		}
		if o.Idempotent {
			f.p("\tIdempotent: true,")
		}
		f.p("}\n")
	}
	return f.bytes()
}

func httpBindingLit(h *registry.HTTPBinding) string {
	segs := make([]string, len(h.Path))
	for i, s := range h.Path {
		switch s := s.(type) {
		case registry.Literal:
			segs[i] = "registry.Literal{Value: " + goString(s.Value) + "}"
		case registry.Label:
			segs[i] = "registry.Label{Name: " + goString(s.Name) + "}"
		case registry.Greedy:
			segs[i] = "registry.Greedy{Name: " + goString(s.Name) + "}"
		}
	}
	out := "&registry.HTTPBinding{Method: " + goString(h.Method) + ", Path: []registry.Segment{" + strings.Join(segs, ", ") + "}"
	if len(h.Query) > 0 {
		qs := make([]string, len(h.Query))
		for i, q := range h.Query {
			switch q := q.(type) {
			case registry.QueryPresent:
				qs[i] = "registry.QueryPresent{Key: " + goString(q.Key) + "}"
			case registry.QueryEquals:
				qs[i] = "registry.QueryEquals{Key: " + goString(q.Key) + ", Value: " + goString(q.Value) + "}"
			}
		}
		out += ", Query: []registry.QueryMatch{" + strings.Join(qs, ", ") + "}"
	}
	return out + "}"
}

// emitBind writes bind_gen.go: an Op handle per operation and the typed
// Binder over kernel.Binder (ARCHITECTURE.md 3.2).
func emitBind(ir *ServiceIR) ([]byte, error) {
	f := newFile(ir, "bind_gen.go")
	f.use("kernel", "registry")
	for _, o := range ir.Operations {
		f.p("// Op%s is the typed handle on %s.", o.Name, o.Name)
		f.p("var Op%s = kernel.NewOp[%s, %s](&desc%s)\n", o.Name, o.Input.GoName, o.Output.GoName, o.Name)
	}
	f.p("// Binder binds %s handlers, one method per operation.", ir.ID)
	f.p("type Binder struct{ b *kernel.Binder }\n")
	f.p("// NewBinder wraps the kernel's binder for %s.", ir.ID)
	f.p("func NewBinder(b *kernel.Binder) Binder { return Binder{b: b} }\n")
	for _, o := range ir.Operations {
		f.p("// %s binds the handler of %s.", o.Name, o.Name)
		f.p("func (b Binder) %s(h kernel.Handler[%s, %s], cov registry.BindCoverage, opts ...kernel.BindOption) {", o.Name, o.Input.GoName, o.Output.GoName)
		f.p("\tkernel.Bind(b.b, Op%s, h, cov, opts...)", o.Name)
		f.p("}\n")
	}
	return f.bytes()
}
