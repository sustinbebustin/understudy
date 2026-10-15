package access

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/policydoc"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// Mode says whom AWS would authorize a call as (ARCHITECTURE.md 3.2,
// PrincipalMode); an Entry's Principal is whom the mode resolved to.
//
//sumtype:decl
type Mode interface{ isMode() }

// Direct is a request a principal made itself, over the wire.
type Direct struct{}

// AsCaller is a forward-access call AWS charges to the original caller: S3,
// Secrets Manager and SQS calling KMS (iam-sts-kms.md 5.8).
type AsCaller struct{}

// AsExecutionRole is a call AWS makes as a function's execution role: event
// source mapping polls and Lambda log writes.
type AsExecutionRole struct{}

// AsService is a delivery a service principal makes from Source, gated by
// the target's resource policy.
type AsService struct{ Source aws.ARN }

func (Direct) isMode()          {}
func (AsCaller) isMode()        {}
func (AsExecutionRole) isMode() {}
func (AsService) isMode()       {}

// Outcome is how a recorded call ended. Injected faults are never recorded.
//
//sumtype:decl
type Outcome interface{ isOutcome() }

// Succeeded is a call that returned a result.
type Succeeded struct{}

// Failed is a call that returned an AWS error; its permissions are still
// needed, because the application handles the failure.
type Failed struct{ Code string }

func (Succeeded) isOutcome() {}
func (Failed) isOutcome()    {}

// Entry is one call: the IAM actions it used and every resource it
// touched. Record pairs each action with the touched resources of its own
// service.
type Entry struct {
	// Principal is a ServicePrincipal when Mode is AsService and an Identity
	// otherwise; a role session is its Role (see Principal).
	Principal Principal
	Mode      Mode
	// Service is the IAM prefix of the operation's service, such as "s3".
	// An action under this prefix with no touched resource of its service
	// is granted on "*"; an action under another prefix (iam:PassRole on
	// CreateFunction) only on a touched resource of its service.
	Service   string
	Actions   []registry.IAMAction
	Resources []aws.ARN
	Outcome   Outcome
	// At is the call's admission instant in emulator time.
	At time.Time
}

// EntryProblem is why Record refused an Entry.
type EntryProblem uint8

// EntryProblem values.
const (
	EntryNoPrincipal EntryProblem = iota + 1
	EntryNoMode
	EntryNoOutcome
	// EntryNoInstant is an Entry whose At is the zero time.
	EntryNoInstant
	// EntryInvalidPrincipal is a Principal whose String ParsePrincipal does
	// not read back as that principal, such as a Role without an account or
	// with a path lacking its slashes.
	EntryInvalidPrincipal
	// EntryModeMismatch is an AsService entry whose Principal is not a
	// ServicePrincipal, or another mode's entry whose Principal is one.
	EntryModeMismatch
	// EntryInvalidAction is an action with an empty Prefix or Name, which
	// would render as an action IAM rejects.
	EntryInvalidAction
	// EntryNoService is an Entry without the IAM prefix of its operation's
	// service, without which an action of another service could not be
	// told from the operation's own.
	EntryNoService
)

// EntryError is an Entry Record refused; nothing of it was recorded.
type EntryError struct {
	Problem EntryProblem
	// Principal is the entry's principal as text, empty when it had none.
	Principal string
	Actions   []registry.IAMAction
}

// Error describes the refusal and how to complete the entry.
func (e *EntryError) Error() string {
	var problem string
	switch e.Problem {
	case EntryNoPrincipal:
		problem = "it has no Principal; set the IAM identity or service principal the call's mode resolved to"
	case EntryNoMode:
		problem = "it has no Mode; set Direct, AsCaller, AsExecutionRole or AsService"
	case EntryNoOutcome:
		problem = "it has no Outcome; set Succeeded or Failed"
	case EntryNoInstant:
		problem = "its At is the zero time; set the call's admission instant from the emulator clock"
	case EntryInvalidPrincipal:
		problem = "its Principal is malformed, so no request could name it; give a Root, User or Role a 12-digit account, " +
			"a path of \"/\" or \"/<segments>/\" and an IAM name, and a ServicePrincipal a name ending in .amazonaws.com"
	case EntryModeMismatch:
		problem = "its Mode does not fit its Principal; an AsService delivery is attributed to a ServicePrincipal and every other mode to a Root, User or Role"
	case EntryInvalidAction:
		problem = "an action has an empty Prefix or Name, so no policy could grant it; take the actions from the operation's registry descriptor"
	case EntryNoService:
		problem = "it has no Service; set the IAM prefix of the operation's service (registry.Service.IAMPrefix)"
	default:
		problem = fmt.Sprintf("problem %d", uint8(e.Problem))
	}
	return fmt.Sprintf("access: Record refused the call of %v by %q, so the access record lacks it and earlier records are unchanged: %s",
		e.Actions, e.Principal, problem)
}

// Use aggregates the calls of one principal, in one mode, of one action on
// one resource.
type Use struct {
	Principal   Principal
	Mode        Mode
	Action      registry.IAMAction
	Resource    Scope
	Calls       int // every call, failures included
	Failures    int
	First, Last time.Time
}

// Scope is what a Use's action was called on.
//
//sumtype:decl
type Scope interface{ isScope() }

// OnResource is an action called on one resource of its own service.
type OnResource struct{ ARN aws.ARN }

// BucketObjects is an action called on objects of one S3 bucket, whose
// ARN is Bucket: every object a call touched (arn:aws:s3:::<bucket>/<key>)
// is recorded under its bucket, so the record grows with the buckets a run
// used rather than its keys, and a policy grants it as
// arn:aws:s3:::<bucket>/*. The journal keeps each call's exact objects.
type BucketObjects struct{ Bucket aws.ARN }

// AnyResource is an action of the operation's own service whose call
// touched no resource of that service, such as sqs:ListQueues; a policy
// grants it on "*".
type AnyResource struct{}

func (OnResource) isScope()    {}
func (BucketObjects) isScope() {}
func (AnyResource) isScope()   {}

// scopeText is the policy Resource text of s, which Usage sorts by. An
// ARN is escaped, because a * or ? in a resource name (an S3 key or a log
// stream may hold them) would otherwise widen the grant to other
// resources; BucketObjects is its escaped bucket ARN followed by /*.
func scopeText(s Scope) string {
	switch s := s.(type) {
	case OnResource:
		return policydoc.Literal(s.ARN.String())
	case BucketObjects:
		return policydoc.Literal(s.Bucket.String()) + "/*"
	case AnyResource:
	}
	return "*"
}

// scopeOf is the scope a touched resource is recorded under: its bucket's
// objects for an S3 object, the resource itself otherwise.
func scopeOf(a aws.ARN) Scope {
	if bucket, object := s3ObjectBucket(a); object {
		return BucketObjects{Bucket: aws.ARN{Partition: a.Partition, Service: "s3", Resource: bucket}}
	}
	return OnResource{ARN: a}
}

// s3ObjectBucket returns the bucket of an S3 object ARN,
// arn:<partition>:s3:::<bucket>/<key>; false for any other ARN, a bucket's
// and an access point's (which carries a region and account) included.
func s3ObjectBucket(a aws.ARN) (string, bool) {
	if a.Service != "s3" || a.Region != "" || a.Account != "" {
		return "", false
	}
	bucket, _, object := strings.Cut(a.Resource, "/")
	return bucket, object && bucket != ""
}

// scopesFor pairs action with the touched resources of its own service,
// because IAM authorizes an action only against its service's resource
// types; STS actions act on IAM roles. An S3 object is paired as its
// bucket's objects (scopeOf). An action with no such resource is
// granted on "*" when it is of the operation's service, opService
// (sqs:ListQueues), and not at all when it is of another (iam:PassRole on
// a CreateFunction that passed no role): the call did not use it.
func scopesFor(action registry.IAMAction, opService string, resources []aws.ARN) []Scope {
	service := action.Prefix
	if service == "sts" {
		service = "iam"
	}
	var out []Scope
	for _, r := range resources {
		if r.Service == service {
			out = append(out, scopeOf(r))
		}
	}
	if len(out) == 0 && action.Prefix == opService {
		out = append(out, AnyResource{})
	}
	return out
}

// Filter selects uses; a zero field matches everything. A malformed
// Principal selects nothing, because Record refuses one.
type Filter struct {
	Principal Principal
	// Service is an IAM action prefix, such as "sqs".
	Service string
}

func (f Filter) matches(u *Use) bool {
	return (f.Principal == nil || canonical(f.Principal) == u.Principal) && (f.Service == "" || f.Service == u.Action.Prefix)
}

// Requirement is a resource-policy statement a delivery needs in AWS: the
// policy of Target must allow Principal to call Action from Source.
type Requirement struct {
	Target    aws.ARN
	Principal ServicePrincipal
	Action    registry.IAMAction
	Source    aws.ARN
}

// Recorder records which IAM actions each principal used. The zero value is
// ready to use, and a Recorder is safe for concurrent use. It keeps one Use
// per distinct (principal, mode, action, resource), S3 objects counted per
// bucket (BucketObjects), so memory grows with the variety of calls, not
// their number.
type Recorder struct {
	mu   sync.Mutex
	uses map[useKey]*Use
}

type useKey struct {
	principal Principal
	mode      Mode
	action    registry.IAMAction
	resource  Scope
}

// Record adds a call. It refuses, with an *EntryError and recording
// nothing, an Entry whose Principal, Mode or Outcome is nil, whose At is
// zero, whose Service is empty, whose Principal ParsePrincipal would not
// read back from its String, whose Mode does not fit its Principal, or
// with an action lacking its Prefix or Name: none has a default the kernel
// could rely on without stating it (a zero At would pin Use.First to the
// zero time), and a malformed principal could never be queried.
func (r *Recorder) Record(e Entry) error {
	if err := checkEntry(e); err != nil {
		return err
	}
	principal := canonical(e.Principal)
	failed := false
	switch e.Outcome.(type) {
	case Failed:
		failed = true
	case Succeeded:
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.uses == nil {
		r.uses = map[useKey]*Use{}
	}
	for _, k := range useKeys(principal, e) {
		u, ok := r.uses[k]
		if !ok {
			u = &Use{Principal: principal, Mode: e.Mode, Action: k.action, Resource: k.resource, First: e.At, Last: e.At}
			r.uses[k] = u
		}
		u.Calls++
		if failed {
			u.Failures++
		}
		if e.At.Before(u.First) {
			u.First = e.At
		}
		if e.At.After(u.Last) {
			u.Last = e.At
		}
	}
	return nil
}

// useKeys returns the distinct uses of e by principal, in order. An Entry is
// one call, so a use it names twice (CopyObject onto its own source, or a
// repeated action) counts once.
func useKeys(principal Principal, e Entry) []useKey {
	var out []useKey
	for _, action := range e.Actions {
		for _, res := range scopesFor(action, e.Service, e.Resources) {
			k := useKey{principal: principal, mode: e.Mode, action: action, resource: res}
			if !slices.Contains(out, k) {
				out = append(out, k)
			}
		}
	}
	return out
}

// checkEntry returns the *EntryError Record refuses e with, or nil.
func checkEntry(e Entry) error {
	refuse := func(p EntryProblem) error {
		text := ""
		if e.Principal != nil {
			text = e.Principal.String()
		}
		return &EntryError{Problem: p, Principal: text, Actions: e.Actions}
	}
	switch {
	case e.Principal == nil:
		return refuse(EntryNoPrincipal)
	case e.Mode == nil:
		return refuse(EntryNoMode)
	case e.Outcome == nil:
		return refuse(EntryNoOutcome)
	case e.At.IsZero():
		return refuse(EntryNoInstant)
	case e.Service == "":
		return refuse(EntryNoService)
	}
	c := canonical(e.Principal)
	if p, err := ParsePrincipal(c.String()); err != nil || p != c {
		return refuse(EntryInvalidPrincipal)
	}
	_, service := e.Principal.(ServicePrincipal)
	_, delivery := e.Mode.(AsService)
	if service != delivery {
		return refuse(EntryModeMismatch)
	}
	if slices.ContainsFunc(e.Actions, func(a registry.IAMAction) bool { return a.Prefix == "" || a.Name == "" }) {
		return refuse(EntryInvalidAction)
	}
	return nil
}

// Usage returns the uses f selects, sorted by principal, action, resource
// and mode.
func (r *Recorder) Usage(f Filter) []Use {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Use
	for _, u := range r.uses {
		if f.matches(u) {
			out = append(out, *u)
		}
	}
	slices.SortFunc(out, func(a, b Use) int {
		return cmp.Or(
			cmp.Compare(a.Principal.String(), b.Principal.String()),
			cmp.Compare(a.Action.String(), b.Action.String()),
			cmp.Compare(scopeText(a.Resource), scopeText(b.Resource)),
			cmp.Compare(modeRank(a.Mode), modeRank(b.Mode)),
			cmp.Compare(modeSource(a.Mode), modeSource(b.Mode)),
		)
	})
	return out
}

// Policy returns the least-privilege identity policy for principal
// (ARCHITECTURE.md 17): one Allow statement per (service, resource) with the
// exact resource ARN, arn:aws:s3:::<bucket>/* for the objects of a bucket,
// or "*" for an action of its operation's service without a resource of
// that service, listing every action used there in any mode, failed calls
// included. A Role's policy covers every session of the role. ok is false
// when principal is nil or has no recorded use: there is no policy to
// write, and IAM rejects one without statements.
func (r *Recorder) Policy(principal Identity) (doc policydoc.Document, ok bool) {
	if principal == nil {
		return policydoc.Document{}, false
	}
	type group struct{ service, resource string }
	actions := map[group][]string{}
	uses := r.Usage(Filter{Principal: principal})
	for i := range uses {
		u := &uses[i]
		g := group{service: u.Action.Prefix, resource: scopeText(u.Resource)}
		actions[g] = append(actions[g], u.Action.String())
	}
	grants := make([]policydoc.Grant, 0, len(actions))
	for g, as := range actions {
		grants = append(grants, policydoc.Grant{Actions: as, Resources: []string{g.resource}})
	}
	return policydoc.Render(grants)
}

// ResourceRequirements returns the resource-policy statements the recorded
// deliveries f selects would need in AWS, one per (target, service
// principal, action, source), sorted by target, principal, action and
// source. f.Principal selects a service principal. A delivery recorded
// without a target of its action's service is left out: no resource policy
// gates it.
func (r *Recorder) ResourceRequirements(f Filter) []Requirement {
	var out []Requirement
	uses := r.Usage(f)
	for i := range uses {
		u := &uses[i]
		s, delivery := u.Mode.(AsService)
		service, byService := u.Principal.(ServicePrincipal)
		target, onResource := u.Resource.(OnResource)
		if delivery && byService && onResource {
			out = append(out, Requirement{Target: target.ARN, Principal: service, Action: u.Action, Source: s.Source})
		}
	}
	slices.SortFunc(out, func(a, b Requirement) int {
		return cmp.Or(
			cmp.Compare(a.Target.String(), b.Target.String()),
			cmp.Compare(a.Principal.Name, b.Principal.Name),
			cmp.Compare(a.Action.String(), b.Action.String()),
			cmp.Compare(a.Source.String(), b.Source.String()),
		)
	})
	return out
}

// Reset forgets every recorded call.
func (r *Recorder) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.uses = nil
}

// modeRank orders modes for sorting.
func modeRank(m Mode) int {
	switch m.(type) {
	case Direct:
		return 0
	case AsCaller:
		return 1
	case AsExecutionRole:
		return 2
	case AsService:
		return 3
	default:
		return 4
	}
}

// modeSource is the delivery source of an AsService mode, "" otherwise.
func modeSource(m Mode) string {
	if s, ok := m.(AsService); ok {
		return s.Source.String()
	}
	return ""
}
