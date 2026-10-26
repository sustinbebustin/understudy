package events

import (
	"context"
	"errors"
	"slices"
	"unicode/utf8"

	"github.com/sustinbebustin/understudy/internal/api/eventbridgeapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/protocol/jsonx"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/service/events/envelope"
)

// maxPolicyLength is how many characters a bus policy holds (eb-quota.html,
// Event bus policy size; eventbridge-buses.md 4.1).
const maxPolicyLength = 10240

// bindPolicy binds PutPermission and RemovePermission with the coverage
// notes of eventbridge-buses.md 10.1: the policy is stored and returned,
// and no call is authorized against it (eventbridge-buses.md 4.4).
func (s *Service) bindPolicy(b eventbridgeapi.Binder) {
	b.PutPermission(s.putPermission,
		registry.StoredOnly{Note: "statements are stored in the bus Policy that DescribeEventBus returns; not enforced"},
		kernel.WithCheck(checkPutPermission),
		namedBus(func(in *eventbridgeapi.PutPermissionInput) *string { return in.EventBusName }))
	b.RemovePermission(s.removePermission,
		registry.StoredOnly{Note: "statements are removed from the bus Policy; not enforced"},
		namedBus(func(in *eventbridgeapi.RemovePermissionInput) *string { return in.EventBusName }))
}

// permission is the change a PutPermission request asks for.
//
//sumtype:decl
type permission interface{ isPermission() }

// grant is the statement form: one statement, upserted by its Sid.
type grant struct {
	sid, action, principal string
	condition              *eventbridgeapi.Condition
}

// replacement is the Policy form: the whole policy, its statements taken
// from document, which DescribeEventBus then returns verbatim.
type replacement struct {
	document   string
	statements []PolicyStatement
}

func (grant) isPermission()       {}
func (replacement) isPermission() {}

// errNilPermission is the cause of applying a permission that is nil, a bug
// in whatever built it.
var errNilPermission = errors.New("events: the PutPermission change is nil, so the bus policy was left as it was; parsePermission builds every permission")

// checkPutPermission runs PutPermission's stateless checks before fault
// rules and the bus lookup (eventbridge-buses.md 4.1; the order and texts
// are inferred, EBB-06).
func checkPutPermission(in *eventbridgeapi.PutPermissionInput) *awserr.Error {
	_, err := parsePermission(in)
	return err
}

// parsePermission reads the change a PutPermission request asks for. A
// Policy is sent "instead of" the statement members (API_PutPermission),
// so it cannot come with any of them; without it, StatementId, Action and
// Principal are all required. A Policy must be a JSON object whose
// Statement is an object or a list of objects, no two with one non-empty
// Sid, and a Sid must be a string: the statements are kept by Sid.
func parsePermission(in *eventbridgeapi.PutPermissionInput) (permission, *awserr.Error) {
	if in.Policy == nil {
		if in.StatementId == nil || in.Action == nil || in.Principal == nil {
			return nil, errPermissionIncomplete()
		}
		return grant{sid: *in.StatementId, action: *in.Action, principal: *in.Principal, condition: in.Condition}, nil
	}
	if in.StatementId != nil || in.Action != nil || in.Principal != nil || in.Condition != nil {
		return nil, errPolicyWithStatement()
	}
	statements, ok := documentStatements(*in.Policy)
	if !ok {
		return nil, errInvalidParameter("Policy")
	}
	return replacement{document: *in.Policy, statements: statements}, nil
}

// documentStatements returns the statements of a policy document, each as
// its compact JSON text with its Sid, in document order; ok is false for a
// document parsePermission refuses.
func documentStatements(text string) ([]PolicyStatement, bool) {
	v, ok := envelope.ParseJSON(text)
	doc, isObject := v.(document.Map)
	if !ok || !isObject {
		return nil, false
	}
	var list document.List
	switch st := member(doc, "Statement").(type) {
	case document.Map:
		list = document.List{st}
	case document.List:
		list = st
	case document.Null, document.Bool, document.Number, document.String, nil:
		return nil, false
	}
	out := make([]PolicyStatement, 0, len(list))
	sids := map[string]bool{}
	for _, item := range list {
		st, isObject := item.(document.Map)
		if !isObject {
			return nil, false
		}
		var sid string
		switch v := member(st, "Sid").(type) {
		case nil:
		case document.String:
			sid = string(v)
		case document.Null, document.Bool, document.Number, document.List, document.Map:
			return nil, false
		}
		if sid != "" && sids[sid] {
			return nil, false
		}
		sids[sid] = true
		e := jsonx.NewEncoder(jsonx.WireOrder)
		e.Value(st)
		text, err := e.Bytes()
		if err != nil {
			return nil, false
		}
		out = append(out, PolicyStatement{Sid: sid, JSON: string(text)})
	}
	return out, true
}

// member is the value of the member name of m, nil when m has none.
func member(m document.Map, name string) document.Value {
	for _, e := range m {
		if e.Key == name {
			return e.Value
		}
	}
	return nil
}

// applied is policy, which may be nil, after perm on the bus at busARN.
// The statement form replaces the statement with its Sid in place or
// appends it, and drops a stored document; the Policy form replaces the
// whole policy (eventbridge-buses.md 4.1, 10.2.5). policy is not changed.
func applied(policy *Policy, perm permission, bus aws.ARN) (*Policy, *awserr.Error) {
	switch p := perm.(type) {
	case grant:
		text, err := p.statement(bus)
		if err != nil {
			return nil, awserr.Internalf(err)
		}
		st := PolicyStatement{Sid: p.sid, JSON: text}
		var statements []PolicyStatement
		if policy != nil {
			statements = slices.Clone(policy.Statements)
		}
		if i := slices.IndexFunc(statements, func(s PolicyStatement) bool { return s.Sid == p.sid }); i >= 0 {
			statements[i] = st
		} else {
			statements = append(statements, st)
		}
		return &Policy{Statements: statements}, nil
	case replacement:
		return &Policy{Statements: slices.Clone(p.statements), Document: new(p.document)}, nil
	}
	return nil, awserr.Internalf(errNilPermission)
}

// statement is the grant as the policy holds it, compact, members in the
// recorded order Sid, Effect, Principal, Action, Resource, then Condition
// when sent: a 12-digit principal as the account's root, * as the string
// "*", Action and Resource as strings (eventbridge-buses.md 4.2), and the
// condition in its IAM form (eventbridge-buses.md 4.2, 8.4: Terraform's
// parse; AWS's rendering is unrecorded). An error (*jsonx.UsageError) is a
// bug in the encoder calls below.
func (g grant) statement(bus aws.ARN) (string, error) {
	e := jsonx.NewEncoder(jsonx.WireOrder)
	e.BeginObject()
	e.Name("Sid")
	e.String(g.sid)
	e.Name("Effect")
	e.String("Allow")
	e.Name("Principal")
	if g.principal == "*" {
		e.String("*")
	} else {
		e.BeginObject()
		e.Name("AWS")
		e.String("arn:" + bus.Partition + ":iam::" + g.principal + ":root")
		e.EndObject()
	}
	e.Name("Action")
	e.String(g.action)
	e.Name("Resource")
	e.String(bus.String())
	if c := g.condition; c != nil {
		e.Name("Condition")
		e.BeginObject()
		e.Name(c.Type)
		e.BeginObject()
		e.Name(c.Key)
		e.String(c.Value)
		e.EndObject()
		e.EndObject()
	}
	e.EndObject()
	out, err := e.Bytes()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// putPermission adds a statement to, or replaces, the policy of a bus of
// the caller's scope; no EventBusName names the default bus, which this
// materializes (eventbridge-buses.md 4.1, 10.2.2). A policy that would pass
// maxPolicyLength is refused and nothing changes. Any Action the model's
// pattern allows is stored, and the call moves the bus's LastModifiedTime;
// both are inferred (eventbridge-buses.md 4.1, 3.6; EBB-06, EBB-07).
func (s *Service) putPermission(_ context.Context, c *kernel.Call, in *eventbridgeapi.PutPermissionInput) (*eventbridgeapi.PutPermissionOutput, *awserr.Error) {
	perm, err := parsePermission(in)
	if err != nil {
		return nil, err
	}
	name, err := ownBusName(c.Scope(), in.EventBusName)
	if err != nil {
		return nil, err
	}
	_, err = kernel.Write(c, s.cells, c.Scope(), func(st *State, _ clock.Instant, _ *kernel.Tx) (struct{}, *awserr.Error) {
		bus, ok := st.bus(name, c.Admitted())
		if !ok {
			return struct{}{}, errBusNotFound(name)
		}
		policy, err := applied(bus.Policy, perm, busARN(c.Scope(), name))
		if err != nil {
			return struct{}{}, err
		}
		if utf8.RuneCountInString(policy.text()) > maxPolicyLength {
			return struct{}{}, errPolicyTooLong()
		}
		bus.Policy = policy
		bus.Modified = c.Admitted()
		st.setBus(bus, c.Admitted())
		return struct{}{}, nil
	})
	if err != nil {
		return nil, err
	}
	return &eventbridgeapi.PutPermissionOutput{}, nil
}

// removePermission removes one statement of a bus's policy, or the whole
// policy (eventbridge-buses.md 4.3). It moves the bus's LastModifiedTime,
// which is inferred (eventbridge-buses.md 3.6, EBB-07).
func (s *Service) removePermission(_ context.Context, c *kernel.Call, in *eventbridgeapi.RemovePermissionInput) (*eventbridgeapi.RemovePermissionOutput, *awserr.Error) {
	name, err := ownBusName(c.Scope(), in.EventBusName)
	if err != nil {
		return nil, err
	}
	_, err = kernel.Write(c, s.cells, c.Scope(), func(st *State, _ clock.Instant, _ *kernel.Tx) (struct{}, *awserr.Error) {
		bus, ok := st.bus(name, c.Admitted())
		if !ok {
			return struct{}{}, errBusNotFound(name)
		}
		policy, err := removed(bus.Policy, name, in)
		if err != nil {
			return struct{}{}, err
		}
		bus.Policy = policy
		bus.Modified = c.Admitted()
		st.setBus(bus, c.Admitted())
		return struct{}{}, nil
	})
	if err != nil {
		return nil, err
	}
	return &eventbridgeapi.RemovePermissionOutput{}, nil
}

// removed is policy, which may be nil, on the bus named bus less what in
// removes: with RemoveAllPermissions the whole policy, whatever
// StatementId says, and on a bus without one ResourceNotFoundException
// (events.bus-policy.remove-all-without-policy); otherwise the statement
// with the StatementId, which must exist, also when the bus has no policy
// (eventbridge-buses.md 4.3). Removing the last statement removes the
// policy (events.bus-policy.remove-last-statement). policy is not changed.
func removed(policy *Policy, bus string, in *eventbridgeapi.RemovePermissionInput) (*Policy, *awserr.Error) {
	if in.RemoveAllPermissions {
		if policy == nil {
			return nil, errNoPolicy(bus)
		}
		return nil, nil
	}
	if policy == nil || in.StatementId == nil {
		return nil, errStatementNotFound()
	}
	sid := *in.StatementId
	i := slices.IndexFunc(policy.Statements, func(s PolicyStatement) bool { return s.Sid == sid })
	if i < 0 {
		return nil, errStatementNotFound()
	}
	statements := slices.Delete(slices.Clone(policy.Statements), i, i+1)
	if len(statements) == 0 {
		return nil, nil
	}
	return &Policy{Statements: statements}, nil
}
