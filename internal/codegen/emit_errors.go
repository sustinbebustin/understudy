package codegen

import (
	"fmt"
	"strings"

	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// emitErrors writes errors_gen.go: an Identity function and an Err
// constructor per error shape, the service-level identities the overlay
// names (throttle, unknown operation, malformed request) and the families'
// generic vocabularies.
func emitErrors(ir *ServiceIR) ([]byte, error) {
	f := newFile(ir, "errors_gen.go")
	f.use("awserr")
	for _, e := range ir.Errors {
		if err := f.errorFuncs(e); err != nil {
			return nil, err
		}
	}
	if e := ir.Throttle; e != nil {
		f.p("// ThrottleError returns the identity a throttling fault rule injects for")
		f.p("// this service (dev.understudy#throttleError).")
		f.p("func ThrottleError() awserr.Identity { return Identity%s() }\n", e.Shape.GoName)
	}
	if ir.UnknownJSON != nil || ir.UnknownQuery != nil {
		f.p("// UnknownOperationError returns the identity this service answers for an")
		f.p("// operation absent from its model, per protocol family")
		f.p("// (dev.understudy#unknownOperationError); false for a family it does not")
		f.p("// name.")
		f.p("func UnknownOperationError(f awserr.Family) (awserr.Identity, bool) {")
		f.p("\tswitch f {")
		for _, c := range []struct {
			fam string
			e   *ErrorIR
		}{{"JSONFamily", ir.UnknownJSON}, {"QueryFamily", ir.UnknownQuery}} {
			if c.e != nil {
				f.p("\tcase awserr.%s:", c.fam)
				f.p("\t\treturn Identity%s(), true", c.e.Shape.GoName)
			}
		}
		f.p("\tdefault:")
		f.p("\t\treturn awserr.Identity{}, false")
		f.p("\t}")
		f.p("}\n")
	}
	if e := ir.MalformedRequest; e != nil {
		f.p("// MalformedRequestError returns the identity this service answers for a")
		f.p("// body that does not decode (dev.understudy#malformedRequestError).")
		f.p("func MalformedRequestError() awserr.Identity { return Identity%s() }\n", e.Shape.GoName)
	}
	if len(ir.Vocabularies) > 0 {
		if err := f.genericErrors(ir.Vocabularies); err != nil {
			return nil, err
		}
	}
	return f.bytes()
}

// genericErrors emits GenericErrors, the identities of each family's
// generic vocabulary.
func (f *file) genericErrors(vocabularies []VocabularyIR) error {
	f.p("// GenericErrors returns the generic, protocol-level identities this")
	f.p("// service sends over protocol family f (overlays/common.json), the codes a")
	f.p("// fault rule may name beside each operation's own; nil for a family it")
	f.p("// does not speak.")
	f.p("func GenericErrors(f awserr.Family) []awserr.Identity {")
	f.p("\tswitch f {")
	for _, v := range vocabularies {
		calls := make([]string, 0, len(v.Errors))
		for _, e := range v.Errors {
			calls = append(calls, "Identity"+e.Shape.GoName+"()")
		}
		fam, err := familyConst(v.Family)
		if err != nil {
			return err
		}
		f.p("\tcase awserr.%s:", fam)
		f.p("\t\treturn []awserr.Identity{%s}", strings.Join(calls, ", "))
	}
	f.p("\tdefault:")
	f.p("\t\treturn nil")
	f.p("\t}")
	f.p("}\n")
	return nil
}

// familyConst names family f's awserr constant.
func familyConst(f awserr.Family) (string, error) {
	switch f {
	case awserr.JSONFamily:
		return "JSONFamily", nil
	case awserr.QueryFamily:
		return "QueryFamily", nil
	default:
		return "", fmt.Errorf("emit errors_gen.go: awserr.Family(%d) has no constant; add it to familyConst", f)
	}
}

// errorFuncs emits the Identity function and the Err constructor of one
// error shape.
func (f *file) errorFuncs(e *ErrorIR) error {
	fields, err := identityFields(e)
	if err != nil {
		return err
	}
	name := e.Shape.GoName
	f.p("// Identity%s returns the identity of %s.", name, e.Shape.ID)
	f.p("func Identity%s() awserr.Identity {", name)
	f.p("\treturn awserr.Identity{%s}", fields)
	f.p("}\n")
	origin := "awserr.Modeled{}"
	if e.Generic {
		origin = "awserr.Generic{}"
	}
	msgs := ""
	if len(e.Messages) > 0 {
		f.use("ref")
		var parts []string
		for _, p := range allProtocols() {
			if m, ok := e.Messages[p]; ok {
				parts = append(parts, fmt.Sprintf("{Protocol: ref.%s, Message: %s}", protocolConst(p), goString(m)))
			}
		}
		msgs = ", Messages: []awserr.ProtocolMessage{" + strings.Join(parts, ", ") + "}"
	}
	if e.MessageKey == awserr.NoMessage {
		f.p("// Err%s returns %s error; the shape carries no message.", name, withArticle(name))
		f.p("func Err%s() *awserr.Error {", name)
		f.p("\treturn &awserr.Error{ID: Identity%s()%s, Origin: %s}", name, msgs, origin)
	} else {
		f.p("// Err%s returns %s error with message.", name, withArticle(name))
		if len(e.Messages) > 0 {
			f.p("// The protocols with a fixed text of their own send that text instead.")
		}
		f.p("func Err%s(message string) *awserr.Error {", name)
		f.p("\treturn &awserr.Error{ID: Identity%s(), Message: message%s, Origin: %s}", name, msgs, origin)
	}
	f.p("}\n")
	return nil
}

// withArticle prefixes a or an by the name's first letter.
func withArticle(name string) string {
	if strings.ContainsAny(name[:1], "AEIOU") {
		return "an " + name
	}
	return "a " + name
}

func identityFields(e *ErrorIR) (string, error) {
	key, err := messageKeyConst(e.Shape.ID, e.MessageKey)
	if err != nil {
		return "", err
	}
	parts := []string{
		"Shape: " + goString(string(e.Shape.ID)),
		"JSONType: " + goString(e.JSONType),
		"QueryCode: " + goString(e.QueryCode),
		fmt.Sprintf("Status: %d", e.Status),
	}
	if e.QueryModeStatus != 0 {
		parts = append(parts, fmt.Sprintf("QueryModeStatus: %d", e.QueryModeStatus))
	}
	parts = append(parts, "Fault: awserr."+e.Fault.String(), "MessageKey: awserr."+key)
	if e.Retryable {
		parts = append(parts, "Retryable: true")
	}
	return strings.Join(parts, ", "), nil
}

// messageKeyConst names key k's awserr constant. The zero MessageKey is
// invalid: the IR builder sets every error's key, so an unset one is a
// builder bug, not a shape without a message.
func messageKeyConst(shape smithy.ShapeID, k awserr.MessageKey) (string, error) {
	switch k {
	case awserr.MessageLower:
		return "MessageLower", nil
	case awserr.MessageUpper:
		return "MessageUpper", nil
	case awserr.NoMessage:
		return "NoMessage", nil
	default:
		return "", failf(StageEmit, string(shape), "awserr.MessageKey(%d) has no constant; the IR builder sets every error's MessageKey (ir_errors.go)", k)
	}
}

// protocolConst is the ref constant naming protocol p.
func protocolConst(p ref.ProtocolID) string {
	names := map[ref.ProtocolID]string{
		ref.AWSJSON10: "AWSJSON10", ref.AWSJSON11: "AWSJSON11", ref.AWSQuery: "AWSQuery",
		ref.RESTJSON1: "RESTJSON1", ref.RESTXML: "RESTXML", ref.RPCv2CBOR: "RPCv2CBOR", ref.Raw: "Raw",
	}
	return names[p]
}

// errorList is the Go literal of an operation's error identities.
func errorList(errs []*ErrorIR) string {
	parts := make([]string, len(errs))
	for i, e := range errs {
		parts[i] = "Identity" + e.Shape.GoName + "()"
	}
	return "[]awserr.Identity{" + strings.Join(parts, ", ") + "}"
}
