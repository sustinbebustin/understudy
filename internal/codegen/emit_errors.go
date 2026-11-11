package codegen

import (
	"fmt"
	"slices"
	"strings"

	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// emitErrors writes errors_gen.go: an Identity function, an Err
// constructor and an ErrorIdentity method per error shape, an ErrMembers
// constructor per shape with members beyond its message, NewErrorMembers,
// the service-level identities the overlay names (throttle, denial, unknown
// operation, malformed request) and the families' generic vocabularies.
func emitErrors(ir *ServiceIR) ([]byte, error) {
	f := newFile(ir, "errors_gen.go")
	f.use("awserr")
	for _, e := range ir.Errors {
		if err := f.errorFuncs(e); err != nil {
			return nil, err
		}
	}
	f.newErrorMembers(ir)
	if e := ir.Throttle; e != nil {
		f.p("// ThrottleError returns the identity a throttling fault rule injects for")
		f.p("// this service (dev.understudy#throttleError).")
		f.p("func ThrottleError() awserr.Identity { return Identity%s() }\n", e.Shape.GoName)
	}
	if d := ir.Denial; d != nil {
		f.p("// DenialError returns the identity this service answers an authorization")
		f.p("// refusal with (dev.understudy#denialError), evidence %s.", d.Label)
		f.p("func DenialError() awserr.Identity { return Identity%s() }\n", d.Error.Shape.GoName)
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
	msgList, msgs := "", ""
	if len(e.Messages) > 0 {
		f.use("ref")
		var parts []string
		for _, p := range allProtocols() {
			if m, ok := e.Messages[p]; ok {
				parts = append(parts, fmt.Sprintf("{Protocol: ref.%s, Message: %s}", protocolConst(p), goString(m)))
			}
		}
		msgList = "[]awserr.ProtocolMessage{" + strings.Join(parts, ", ") + "}"
		msgs = ", Messages: " + msgList
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
	f.p("// ErrorIdentity returns the identity of %s.", e.Shape.ID)
	f.p("func (*%s) ErrorIdentity() awserr.Identity { return Identity%s() }\n", name, name)
	f.membersFunc(e, msgList, origin)
	return nil
}

// membersFunc emits Err<X>Members, the constructor of an error carrying
// its members beyond the message, for a shape that has any. The message
// stays the error's one text: it replaces the value's own message member,
// which codecs never write.
func (f *file) membersFunc(e *ErrorIR, msgList, origin string) {
	msg := messageMember(e.Shape)
	if !slices.ContainsFunc(e.Shape.Members, func(m *MemberIR) bool { return m != msg }) {
		return
	}
	name := e.Shape.GoName
	text := "message"
	switch {
	case e.MessageKey == awserr.NoMessage:
		text = `""`
		f.p("// Err%sMembers returns %s error carrying v's members; the shape carries no message.", name, withArticle(name))
		f.p("func Err%sMembers(v %s) *awserr.Error {", name, name)
	case msg == nil:
		f.p("// Err%sMembers returns %s error with message, carrying v's members.", name, withArticle(name))
		f.p("func Err%sMembers(message string, v %s) *awserr.Error {", name, name)
	default:
		f.p("// Err%sMembers returns %s error with message, carrying v's members;", name, withArticle(name))
		f.p("// message replaces v.%s.", msg.GoName)
		f.p("func Err%sMembers(message string, v %s) *awserr.Error {", name, name)
		if msg.Value {
			f.p("\tv.%s = message", msg.GoName)
		} else {
			f.p("\tv.%s = nil", msg.GoName)
			f.p("\tif message != \"\" {")
			f.p("\t\tv.%s = &message", msg.GoName)
			f.p("\t}")
		}
	}
	if msgList == "" {
		f.p("\treturn awserr.WithMembers(&v, %s, %s)", text, origin)
	} else {
		f.p("\te := awserr.WithMembers(&v, %s, %s)", text, origin)
		f.p("\te.Messages = %s", msgList)
		f.p("\treturn e")
	}
	f.p("}\n")
}

// messageMember is the member of error shape s that holds its message,
// spelled message or Message as the IR builder reads it (ir_errors.go),
// or nil when s has none.
func messageMember(s *ShapeIR) *MemberIR {
	var msg *MemberIR
	for _, m := range s.Members {
		if m.Name == "message" || m.Name == "Message" {
			msg = m
		}
	}
	return msg
}

// newErrorMembers emits NewErrorMembers, which a service descriptor carries
// so fault rules can decode an error's members without importing the
// generated package. It names only the error shapes that get a
// deserializer (decodes).
func (f *file) newErrorMembers(ir *ServiceIR) {
	f.use("smithy")
	f.p("// NewErrorMembers returns a new, empty value of error shape id, to decode")
	f.p("// a fault rule's members into, or false for a shape this package does not")
	f.p("// generate a decoder for.")
	f.p("func NewErrorMembers(id smithy.ShapeID) (awserr.DecodableMembers, bool) {")
	f.p("\tswitch id {")
	for _, e := range ir.Errors {
		if !decodes(ir, e.Shape) {
			continue
		}
		f.p("\tcase %s:", goString(string(e.Shape.ID)))
		f.p("\t\treturn new(%s), true", e.Shape.GoName)
	}
	f.p("\tdefault:")
	f.p("\t\treturn nil, false")
	f.p("\t}")
	f.p("}\n")
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
	switch e.QueryErrorWhen {
	case awserr.QueryErrorAlways:
	case awserr.QueryErrorInQueryMode:
		parts = append(parts, "QueryErrorWhen: awserr.QueryErrorInQueryMode")
	case awserr.QueryErrorNever:
		return "", failf(StageEmit, string(e.Shape.ID), "awserr.QueryErrorNever is the edge's for an unclaimed request; no overlay trait sets it on a generated identity")
	default:
		return "", failf(StageEmit, string(e.Shape.ID), "awserr.QueryErrorWhen(%d) has no constant; the IR builder sets it from dev.understudy#queryErrorHeader (ir_errors.go)", e.QueryErrorWhen)
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
