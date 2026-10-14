package codegen

import (
	"fmt"
	"strings"

	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// emitService writes service_gen.go: the package comment and the immutable
// registry.Service.
func emitService(ir *ServiceIR) ([]byte, error) {
	f := newFile(ir, "service_gen.go")
	f.doc = []string{
		fmt.Sprintf("Package %s is the generated API of %s:", ir.Package, ir.Shape),
		"its shapes, serde, validation, errors and registry descriptors, from",
		fmt.Sprintf("%s %s and the codegen overlays.", ir.Origin, ir.ModelFile),
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
	if env := ir.QueryErrorEnvelope; env != (registry.QueryErrorEnvelope{}) {
		f.p("\tQueryErrorEnvelope: registry.QueryErrorEnvelope{Namespace: %t, Detail: %t},", env.Namespace, env.Detail)
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
	f.use("registry", "ref")
	for _, o := range ir.Operations {
		if err := f.descriptor(ir.ID, o); err != nil {
			return nil, err
		}
	}
	for _, o := range ir.Operations {
		if err := f.notConfigured(o); err != nil {
			return nil, err
		}
	}
	return f.bytes()
}

// descriptor writes operation o's registry.Operation variable.
func (f *file) descriptor(svc ref.ServiceID, o *OperationIR) error {
	kind, ok := kindConst(o.Kind)
	if !ok {
		return failf(StageEmit, string(o.ID), "operation kind %v has no registry constant; add it to kindConst in internal/codegen/emit_ops.go", o.Kind)
	}
	if o.Kind == registry.Raw {
		f.p("// desc%s is a raw operation (codegen/services rawOperations): its", o.Name)
		f.p("// handler owns the HTTP exchange, so it has no input or output schema.")
	}
	f.p("var desc%s = registry.Operation{", o.Name)
	f.p("\tService: &service,")
	f.p("\tID: ref.OperationID{Service: %s, Name: %s},", goString(string(svc)), goString(o.Name))
	f.p("\tOrdinal: %d,", o.Ordinal)
	f.p("\tKind: registry.%s,", kind)
	f.p("\tShape: %s,", goString(string(o.ID)))
	if o.HasIO() {
		f.p("\tInput: &%s,", o.Input.SchemaVar)
		f.p("\tOutput: &%s,", o.Output.SchemaVar)
	}
	if len(o.Errors) > 0 {
		f.use("awserr")
		f.p("\tErrors: %s,", errorList(o.Errors))
	}
	if len(o.IAM) > 0 {
		f.p("\tIAM: %s,", iamLit(o.IAM))
	}
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
	return nil
}

// kindConst names the registry constant of an operation kind. A kind it
// does not know is a generator bug, not a Modeled operation.
func kindConst(k registry.OperationKind) (string, bool) {
	switch k {
	case registry.Internal:
		return "Internal", true
	case registry.Raw:
		return "Raw", true
	case registry.Modeled:
		return "Modeled", true
	default:
		return "", false
	}
}

// iamLit is the Go literal of an operation's IAM actions.
func iamLit(acts []registry.IAMAction) string {
	parts := make([]string, len(acts))
	for i, a := range acts {
		parts[i] = fmt.Sprintf("{Prefix: %s, Name: %s}", goString(a.Prefix), goString(a.Name))
	}
	return "[]registry.IAMAction{" + strings.Join(parts, ", ") + "}"
}

// notConfigured emits NotConfigured<Op>, the answer of an S3 Get* operation
// while its subresource is unset: the overlay's error, or its default
// output rendered as a typed literal.
func (f *file) notConfigured(o *OperationIR) error {
	switch nc := o.NotConfigured.(type) {
	case nil:
		return nil
	case NotConfiguredError:
		e := f.errorOf(nc.Shape)
		f.use("awserr")
		f.p("// NotConfigured%s is %s's answer while its subresource is unset:", o.Name, o.Name)
		f.p("// %s (dev.understudy#notConfigured).", nc.Shape)
		if e.MessageKey == awserr.NoMessage {
			f.p("func NotConfigured%s() *awserr.Error { return Err%s() }\n", o.Name, e.Shape.GoName)
		} else {
			f.p("func NotConfigured%s(message string) *awserr.Error { return Err%s(message) }\n", o.Name, e.Shape.GoName)
		}
	case NotConfiguredDefault:
		lit, err := f.exampleLit(o.Output, nc.Document, o.Name+" notConfigured default")
		if err != nil {
			return err
		}
		f.p("// NotConfigured%s is %s's answer while its subresource is unset: the", o.Name, o.Name)
		f.p("// default configuration (dev.understudy#notConfigured).")
		f.p("func NotConfigured%s() *%s { return &%s }\n", o.Name, o.Output.GoName, lit)
	}
	return nil
}

// errorOf finds the error identity of shape id.
func (f *file) errorOf(id smithy.ShapeID) *ErrorIR {
	for _, e := range f.ir.Errors {
		if e.Shape.ID == id {
			return e
		}
	}
	return nil
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

// emitBind writes bind_gen.go: an Op handle per typed operation and the
// typed Binder over kernel.Binder (ARCHITECTURE.md 3.2). A raw operation
// binds a kernel.RawHTTPHandler; with rawBinding, each modeled operation
// also has an <Op>Raw handle and method for the passthrough (ADR 0014).
func emitBind(ir *ServiceIR) ([]byte, error) {
	f := newFile(ir, "bind_gen.go")
	f.use("kernel", "registry")
	raw := func(o *OperationIR) bool { return ir.RawBinding && o.Kind == registry.Modeled }
	for _, o := range ir.Operations {
		if !o.HasIO() {
			continue
		}
		f.p("// Op%s is the typed handle on %s.", o.Name, o.Name)
		f.p("var Op%s = kernel.NewOp[%s, %s](&desc%s)\n", o.Name, o.Input.GoName, o.Output.GoName, o.Name)
		if raw(o) {
			f.p("// Op%sRaw is the passthrough handle on %s: its output is the", o.Name, o.Name)
			f.p("// response the handler forwards.")
			f.p("var Op%sRaw = kernel.NewOp[%s, kernel.RawOutput](&desc%s)\n", o.Name, o.Input.GoName, o.Name)
		}
	}
	f.p("// Binder binds %s handlers, one method per operation.", ir.ID)
	f.p("type Binder struct{ b *kernel.Binder }\n")
	f.p("// NewBinder wraps the kernel's binder for %s.", ir.ID)
	f.p("func NewBinder(b *kernel.Binder) Binder { return Binder{b: b} }\n")
	for _, o := range ir.Operations {
		if !o.HasIO() {
			f.p("// %s binds the raw HTTP handler of %s.", o.Name, o.Name)
			f.p("func (b Binder) %s(h kernel.RawHTTPHandler, cov registry.BindCoverage) {", o.Name)
			f.p("\tkernel.BindRawHTTP(b.b, &desc%s, h, cov)", o.Name)
			f.p("}\n")
			continue
		}
		f.p("// %s binds the handler of %s.", o.Name, o.Name)
		f.p("func (b Binder) %s(h kernel.Handler[%s, %s], cov registry.BindCoverage, opts ...kernel.BindOption) {", o.Name, o.Input.GoName, o.Output.GoName)
		f.p("\tkernel.Bind(b.b, Op%s, h, cov, opts...)", o.Name)
		f.p("}\n")
		if raw(o) {
			f.p("// %sRaw binds the passthrough handler of %s.", o.Name, o.Name)
			f.p("func (b Binder) %sRaw(h kernel.RawHandler[%s], cov registry.BindCoverage, opts ...kernel.BindOption) {", o.Name, o.Input.GoName)
			f.p("\tkernel.BindRaw(b.b, Op%sRaw, h, cov, opts...)", o.Name)
			f.p("}\n")
		}
	}
	return f.bytes()
}
