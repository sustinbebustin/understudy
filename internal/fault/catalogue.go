package fault

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// Catalogue is what rule validation reads. The kernel implements it over the
// registry, so fault never imports a descriptor.
type Catalogue interface {
	// Errors returns every identity op can send: its modeled and overlay
	// errors plus the generic catalogue of its service's protocol family.
	// It returns false for an operation the registry does not know.
	// Validation ignores identities in understudy's own dev.understudy
	// namespace, which AWS never sends.
	Errors(op ref.OperationID) ([]awserr.Identity, bool)
	// Throttle returns the service's throttling identity (the overlay's
	// throttleError), or the zero Identity for a service the registry does
	// not know; validation also reads it to check that match.services names
	// known services.
	Throttle(svc ref.ServiceID) awserr.Identity
}

// RuleError reports a rule that cannot be added. No rule was added or
// changed.
type RuleError struct {
	// Rule is the rule's ID as given; empty when Add would have minted one.
	Rule ref.RuleID
	// Field is the rule member at fault, spelled as in the JSON form:
	// "action.shape", "trigger.n", "match.operations".
	Field  string
	Reason string
	// Known lists the errors the operation really sends when the rule names
	// one it does not, or the full shape ids a bare shape name could mean
	// when it names more than one.
	Known []smithy.ShapeID
}

// Error describes the rejected rule, the field at fault and how to fix it.
func (e *RuleError) Error() string {
	var b strings.Builder
	b.WriteString("fault rule")
	if e.Rule != "" {
		fmt.Fprintf(&b, " %q", e.Rule)
	}
	fmt.Fprintf(&b, " rejected, no rule was added or changed: %s: %s", e.Field, e.Reason)
	if len(e.Known) > 0 {
		names := make([]string, len(e.Known))
		for i, s := range e.Known {
			names[i] = string(s)
		}
		b.WriteString("; use one of the errors it sends: ")
		b.WriteString(strings.Join(names, ", "))
	}
	return b.String()
}

// statusOKOperations are the operations S3 answers with an error inside a
// 200 response (wire-rest.md 3.7).
func statusOKOperations() []ref.OperationID {
	return []ref.OperationID{
		{Service: "s3", Name: "CompleteMultipartUpload"},
		{Service: "s3", Name: "CopyObject"},
		{Service: "s3", Name: "UploadPartCopy"},
	}
}

// resolved holds the identities a valid rule's action injects: per matched
// operation for the shape-carrying actions, per matched service for
// Throttle, and neither for Latency and DropDelivery.
type resolved struct {
	byOp      map[ref.OperationID]awserr.Identity
	byService map[ref.ServiceID]awserr.Identity
}

// identity returns what the rule injects into the call in, or false when the
// call is outside what the rule resolved. Latency and DropDelivery inject
// no error and apply to every call with the zero identity.
func (r resolved) identity(a Action, in MatchInput) (id awserr.Identity, ok bool) {
	switch a.(type) {
	case Error, AfterCommit, StatusOKError:
		id, ok = r.byOp[in.Op]
	case Throttle:
		id, ok = r.byService[in.Service]
	case Latency, DropDelivery:
		ok = true
	}
	return id, ok
}

// all returns every resolved identity, in a stable order.
func (r resolved) all() []awserr.Identity {
	out := make([]awserr.Identity, 0, len(r.byOp)+len(r.byService))
	for _, op := range slices.SortedFunc(maps.Keys(r.byOp), compareOps) {
		out = append(out, r.byOp[op])
	}
	for _, svc := range slices.Sorted(maps.Keys(r.byService)) {
		out = append(out, r.byService[svc])
	}
	return out
}

func compareOps(a, b ref.OperationID) int {
	return cmp.Or(cmp.Compare(a.Service, b.Service), cmp.Compare(a.Name, b.Name))
}

// resolve validates r against the catalogue and returns the identities its
// action injects.
func resolve(cat Catalogue, r Rule) (resolved, *RuleError) {
	if err := checkShape(r); err != nil {
		return resolved{}, err
	}
	if err := checkMatch(cat, r); err != nil {
		return resolved{}, err
	}
	if err := checkTrigger(r); err != nil {
		return resolved{}, err
	}
	return resolveAction(cat, r)
}

func reject(r Rule, field, reason string, args ...any) *RuleError {
	return &RuleError{Rule: r.ID, Field: field, Reason: fmt.Sprintf(reason, args...)}
}

// checkShape checks the ID, the phase and that the action and match
// dimensions belong to the phase.
func checkShape(r Rule) *RuleError {
	switch {
	case r.ID != "" && !validRuleID(r.ID):
		return reject(r, "id", "%q is not a rule id: use 1 to %d letters, digits, '.', '_' and '-'", r.ID, maxRuleIDLen)
	case r.Phase == 0:
		return reject(r, "phase", `is missing; set "phase" to request or delivery`)
	case !r.Phase.valid():
		return reject(r, "phase", `%d is not a phase; set "phase" to request or delivery`, uint8(r.Phase))
	case r.Action == nil:
		return reject(r, "action", "is missing; give one of error, throttle, latency, after-commit, status-ok-error and drop-delivery")
	case r.Trigger == nil:
		return reject(r, "trigger", "is missing; give one of always, next-n and probability")
	case r.Phase == Request && len(r.Match.Mechanisms) > 0:
		return reject(r, "match.mechanisms", "apply to the delivery phase only; in the request phase match operations")
	case r.Phase == Delivery && len(r.Match.Operations) > 0:
		return reject(r, "match.operations", "apply to the request phase only; in the delivery phase match mechanisms")
	case r.Phase == Delivery && r.Match.Origin == ExternalOnly:
		return reject(r, "match.origin", "a delivery attempt is always internal, so origin external could never match; use any or internal")
	}
	switch a := r.Action.(type) {
	case Error, Throttle, AfterCommit, StatusOKError:
		if r.Phase != Request {
			return reject(r, "phase", "the %s action runs in the request phase; to fail a delivery, match the target operation in the request phase with origin internal", actionKind(a))
		}
	case DropDelivery:
		if r.Phase != Delivery {
			return reject(r, "phase", "a drop-delivery action runs in the delivery phase")
		}
	case Latency:
		if a.Wall <= 0 {
			return reject(r, "action.wall", "is %s; a latency must be positive", a.Wall)
		}
	}
	return nil
}

func checkMatch(cat Catalogue, r Rule) *RuleError {
	m := r.Match
	if !m.Origin.valid() {
		return reject(r, "match.origin", `%d is not an origin; set "match.origin" to any, external or internal`, uint8(m.Origin))
	}
	if !m.Resource.canMatchARN() {
		return reject(r, "match.resource", "%q can never match, because every ARN starts with %q; "+
			"write the whole ARN with wildcards, such as arn:aws:sqs:*:*:orders-*", m.Resource, arnPrefix)
	}
	for _, a := range m.Accounts {
		if a.IsZero() {
			return reject(r, "match.accounts", "holds an empty account id")
		}
	}
	for _, reg := range m.Regions {
		if reg.IsZero() {
			return reject(r, "match.regions", "holds an empty region")
		}
	}
	for _, svc := range m.Services {
		if cat.Throttle(svc).Shape == "" {
			return reject(r, "match.services", "%q is not a service understudy knows, so the rule could never match; service ids are lower case, such as sqs", svc)
		}
	}
	for _, op := range m.Operations {
		if _, ok := cat.Errors(op); !ok {
			return reject(r, "match.operations", "%s is not an operation understudy knows", op)
		}
		if len(m.Services) > 0 && !slices.Contains(m.Services, op.Service) {
			return reject(r, "match.operations", "%s belongs to service %s, which match.services does not list, so the rule could never match", op, op.Service)
		}
	}
	return checkMechanisms(r)
}

// checkMechanisms checks the delivery-phase match: in the delivery phase
// match.services selects the target, so each listed service must receive
// some delivery, and each mechanism must land in a listed service.
func checkMechanisms(r Rule) *RuleError {
	if r.Phase == Delivery {
		for _, svc := range r.Match.Services {
			if err := checkDeliveryTarget(r, svc); err != nil {
				return err
			}
		}
	}
	for _, mech := range r.Match.Mechanisms {
		if _, err := mech.MarshalText(); err != nil {
			return reject(r, "match.mechanisms", "%s", err)
		}
		if target := mech.Target(); len(r.Match.Services) > 0 && !slices.Contains(r.Match.Services, target) {
			return reject(r, "match.mechanisms", "%s delivers to service %s, which match.services does not list, so the rule could never match; "+
				"in the delivery phase match.services names the target", mech, target)
		}
	}
	return nil
}

// checkDeliveryTarget rejects svc in a delivery-phase match.services when no
// mechanism delivers to it, pointing to the mechanisms that deliver from it
// when it is a source; a mechanism's wire name is "<source>-to-<target>".
func checkDeliveryTarget(r Rule, svc ref.ServiceID) *RuleError {
	var targets, from []string
	for _, m := range delivery.Mechanisms() {
		if m.Target() == svc {
			return nil
		}
		if t := string(m.Target()); !slices.Contains(targets, t) {
			targets = append(targets, t)
		}
		if name := m.String(); strings.HasPrefix(name, string(svc)+"-to-") {
			from = append(from, name)
		}
	}
	e := reject(r, "match.services", "no delivery lands in %s, so the rule could never match; in the delivery phase match.services names the target, one of %s",
		svc, orList(targets))
	if len(from) > 0 {
		e.Reason += fmt.Sprintf("; to select deliveries from %s use match.mechanisms %s", svc, orList(from))
	}
	return e
}

// orList joins items as "a, b or c".
func orList(items []string) string {
	if len(items) < 2 {
		return strings.Join(items, "")
	}
	return strings.Join(items[:len(items)-1], ", ") + " or " + items[len(items)-1]
}

func checkTrigger(r Rule) *RuleError {
	switch t := r.Trigger.(type) {
	case NextN:
		if t.N < 1 {
			return reject(r, "trigger.n", "is %d; a next-n budget must be at least 1", t.N)
		}
	case Probability:
		if !(t.P > 0 && t.P <= 1) {
			return reject(r, "trigger.p", "is %v; a probability must be greater than 0 and at most 1", t.P)
		}
	case Always:
	}
	return nil
}

func resolveAction(cat Catalogue, r Rule) (resolved, *RuleError) {
	switch a := r.Action.(type) {
	case Error:
		res, err := resolveShape(cat, r, a.Shape)
		if err != nil || a.Message == "" {
			return res, err
		}
		for _, op := range slices.SortedFunc(maps.Keys(res.byOp), compareOps) {
			if res.byOp[op].MessageKey == awserr.NoMessage {
				return resolved{}, reject(r, "action.message", "%s has no message member, so %s never sends a message with it; leave action.message empty",
					res.byOp[op].Shape, op)
			}
		}
		return res, nil
	case AfterCommit:
		return resolveShape(cat, r, a.Shape)
	case StatusOKError:
		for _, op := range r.Match.Operations {
			if !slices.Contains(statusOKOperations(), op) {
				return resolved{}, reject(r, "match.operations", "%s never sends an error in a 200 response; only s3.CompleteMultipartUpload, s3.CopyObject and s3.UploadPartCopy do", op)
			}
		}
		return resolveShape(cat, r, a.Shape)
	case Throttle:
		return resolveThrottle(cat, r)
	case Latency, DropDelivery:
		return resolved{}, nil
	default:
		return resolved{}, reject(r, "action", "is an action this version does not know")
	}
}

// resolveShape finds shape among the errors of every matched operation.
func resolveShape(cat Catalogue, r Rule, shape smithy.ShapeID) (resolved, *RuleError) {
	if shape == "" {
		return resolved{}, reject(r, "action.shape", "is missing; the %s action needs one of the errors the matched operations send", actionKind(r.Action))
	}
	if len(r.Match.Operations) == 0 {
		return resolved{}, reject(r, "match.operations", "the %s action needs at least one operation, because each operation sends its own set of errors", actionKind(r.Action))
	}
	byOp := make(map[ref.OperationID]awserr.Identity, len(r.Match.Operations))
	for _, op := range r.Match.Operations {
		errs, _ := cat.Errors(op)
		id, err := findShape(r, op, errs, shape)
		if err != nil {
			return resolved{}, err
		}
		byOp[op] = id
	}
	return resolved{byOp: byOp}, nil
}

// emulatorNamespace holds understudy's own shapes, such as the
// NotImplemented error it answers an unbound operation with. AWS never sends
// them, so a rule can never inject them.
const emulatorNamespace = "dev.understudy"

// findShape picks shape from errs, the errors op sends, leaving out the
// emulator's own. A shape without a namespace must name exactly one of them:
// a modeled shape and a generic one can share a name, and the order of errs
// is not part of the Catalogue contract, so an ambiguous name is rejected
// with the full ids to choose from.
func findShape(r Rule, op ref.OperationID, errs []awserr.Identity, shape smithy.ShapeID) (awserr.Identity, *RuleError) {
	// none is the identity returned beside an error; Identity literals are
	// reserved to the generated packages (ARCHITECTURE.md 3.3).
	var none awserr.Identity
	errs = slices.DeleteFunc(slices.Clone(errs), func(id awserr.Identity) bool {
		return id.Shape.Namespace() == emulatorNamespace
	})
	bare := !strings.Contains(string(shape), "#")
	var found []awserr.Identity
	for _, id := range errs {
		named := id.Shape == shape || bare && id.Shape.Name() == string(shape)
		if named && !slices.ContainsFunc(found, func(f awserr.Identity) bool { return f.Shape == id.Shape }) {
			found = append(found, id)
		}
	}
	switch len(found) {
	case 0:
		e := reject(r, "action.shape", "%s never sends %q", op, shape)
		for _, id := range errs {
			if !slices.Contains(e.Known, id.Shape) {
				e.Known = append(e.Known, id.Shape)
			}
		}
		return none, e
	case 1:
		return found[0], nil
	default:
		e := reject(r, "action.shape", "%q names %d different errors %s sends; write the full shape id", shape, len(found), op)
		for _, id := range found {
			e.Known = append(e.Known, id.Shape)
		}
		return none, e
	}
}

// resolveThrottle resolves each matched service's throttling identity; the
// services are those listed plus those of the listed operations.
func resolveThrottle(cat Catalogue, r Rule) (resolved, *RuleError) {
	services := slices.Clone(r.Match.Services)
	for _, op := range r.Match.Operations {
		services = append(services, op.Service)
	}
	if len(services) == 0 {
		return resolved{}, reject(r, "match.services", "a throttle action needs a service or an operation, because each service throttles with its own error")
	}
	byService := make(map[ref.ServiceID]awserr.Identity, len(services))
	for _, svc := range services {
		id := cat.Throttle(svc)
		if id.Shape == "" {
			return resolved{}, reject(r, "match.services", "%s is not a service understudy knows", svc)
		}
		byService[svc] = id
	}
	return resolved{byService: byService}, nil
}
