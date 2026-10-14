package registry

import (
	"cmp"
	"fmt"
	"maps"
	"slices"

	"github.com/sustinbebustin/understudy/internal/ref"
)

// Registry indexes the service descriptors the server wires. It is
// immutable after New.
type Registry struct {
	services  []*Service // sorted by ID
	byID      map[ref.ServiceID]*Service
	byTarget  map[string]*Service
	bySigning map[string]*Service
	byQuery   map[queryKey]*Operation
}

type queryKey struct{ action, version string }

// DescriptorProblem says what is wrong with a descriptor New was given.
type DescriptorProblem uint8

// DescriptorProblem values.
const (
	NilService DescriptorProblem = iota + 1
	EmptyServiceID
	DuplicateService
	DuplicateJSONTarget
	DuplicateSigningName
	MissingQueryVersion
	DuplicateQueryAction
	NilOperation
	OrdinalMismatch
	ForeignOperation
	OperationOrder
	UnknownOperationKind
	InvalidRoute
	ConflictingRoutes
)

// DescriptorError reports a descriptor New refuses. Generated descriptors
// never trigger it; a hand-edited or mis-wired one does, at boot.
type DescriptorError struct {
	Problem   DescriptorProblem
	Service   ref.ServiceID
	Operation string // empty when the problem is the service's
	Detail    string
}

func (e *DescriptorError) Error() string {
	where := fmt.Sprintf("service %q", e.Service)
	if e.Operation != "" {
		where += fmt.Sprintf(" operation %q", e.Operation)
	}
	return fmt.Sprintf("registry: %s: %s; regenerate the descriptors with 'just generate' or fix the wiring in internal/server/wire.go", where, e.Detail)
}

// New indexes services. It fails on a nil or duplicate service, a JSON target
// prefix or signing name two services share, an awsQuery service with no
// QueryVersion or with an Action and Version another service holds, a nil
// operation, an operation whose Ordinal is not its index, operations out of
// name order, an operation of another service or of an unknown kind, an
// invalid route, and two routes no request can tell apart.
func New(services ...*Service) (*Registry, error) {
	r := &Registry{
		byID:      make(map[ref.ServiceID]*Service, len(services)),
		byTarget:  map[string]*Service{},
		bySigning: map[string]*Service{},
		byQuery:   map[queryKey]*Operation{},
	}
	for _, s := range services {
		if err := r.add(s); err != nil {
			return nil, err
		}
	}
	r.services = slices.SortedFunc(maps.Values(r.byID), func(a, b *Service) int { return cmp.Compare(a.ID, b.ID) })
	return r, nil
}

// Services returns every service, sorted by id.
func (r *Registry) Services() []*Service { return slices.Clone(r.services) }

// Service finds a service by id.
func (r *Registry) Service(id ref.ServiceID) (*Service, bool) {
	s, ok := r.byID[id]
	return s, ok
}

// Operation finds an operation by id.
func (r *Registry) Operation(id ref.OperationID) (*Operation, bool) {
	s, ok := r.byID[id.Service]
	if !ok {
		return nil, false
	}
	return s.Operation(id.Name)
}

// ByJSONTarget finds the service whose X-Amz-Target prefix is prefix.
func (r *Registry) ByJSONTarget(prefix string) (*Service, bool) {
	s, ok := r.byTarget[prefix]
	return s, ok
}

// ByQueryAction finds the modeled operation an awsQuery request names with
// its Action and Version parameters.
func (r *Registry) ByQueryAction(action, version string) (*Operation, bool) {
	op, ok := r.byQuery[queryKey{action: action, version: version}]
	return op, ok
}

// BySigningName finds the service whose SigV4 credential-scope name is name.
func (r *Registry) BySigningName(name string) (*Service, bool) {
	s, ok := r.bySigning[name]
	return s, ok
}

func (r *Registry) add(s *Service) error {
	if s == nil {
		return &DescriptorError{Problem: NilService, Detail: "the service descriptor is nil"}
	}
	if err := r.checkService(s); err != nil {
		return err
	}
	if err := checkOperations(s); err != nil {
		return err
	}
	if err := checkRoutes(s); err != nil {
		return err
	}
	if err := r.indexQuery(s); err != nil {
		return err
	}
	r.byID[s.ID] = s
	if s.SigningName != "" {
		r.bySigning[s.SigningName] = s
	}
	if s.JSONTargetPrefix != "" {
		r.byTarget[s.JSONTargetPrefix] = s
	}
	return nil
}

// checkService checks the service's own fields against the services already
// added.
func (r *Registry) checkService(s *Service) error {
	fail := func(p DescriptorProblem, detail string) error {
		return &DescriptorError{Problem: p, Service: s.ID, Detail: detail}
	}
	if s.ID == "" {
		return fail(EmptyServiceID, "the service id is empty")
	}
	if _, dup := r.byID[s.ID]; dup {
		return fail(DuplicateService, "two descriptors share the service id")
	}
	if other, dup := r.bySigning[s.SigningName]; dup {
		return fail(DuplicateSigningName, fmt.Sprintf("signing name %q is also service %q's", s.SigningName, other.ID))
	}
	if other, dup := r.byTarget[s.JSONTargetPrefix]; dup {
		return fail(DuplicateJSONTarget, fmt.Sprintf("JSON target prefix %q is also service %q's", s.JSONTargetPrefix, other.ID))
	}
	if slices.Contains(s.Protocols, ref.AWSQuery) && s.QueryVersion == "" {
		return fail(MissingQueryVersion, "the service speaks awsquery but has no QueryVersion")
	}
	return nil
}

// indexQuery adds the (Action, Version) of every modeled operation of an
// awsQuery service.
func (r *Registry) indexQuery(s *Service) error {
	if !slices.Contains(s.Protocols, ref.AWSQuery) {
		return nil
	}
	for _, op := range s.Operations {
		if op.Kind != Modeled {
			continue
		}
		k := queryKey{action: op.ID.Name, version: s.QueryVersion}
		if other, dup := r.byQuery[k]; dup {
			return &DescriptorError{
				Problem: DuplicateQueryAction, Service: s.ID, Operation: op.ID.Name,
				Detail: fmt.Sprintf("Action %q at Version %q is also service %q's", k.action, k.version, other.Service.ID),
			}
		}
		r.byQuery[k] = op
	}
	return nil
}

func checkOperations(s *Service) error {
	for i, op := range s.Operations {
		if op == nil {
			return &DescriptorError{Problem: NilOperation, Service: s.ID, Detail: fmt.Sprintf("operation %d is nil", i)}
		}
		var p DescriptorProblem
		var detail string
		switch {
		case op.Ordinal != i:
			p, detail = OrdinalMismatch, fmt.Sprintf("ordinal %d at index %d; ordinals must run from 0 to %d in order", op.Ordinal, i, len(s.Operations)-1)
		case op.Service != s || op.ID.Service != s.ID:
			p, detail = ForeignOperation, fmt.Sprintf("the operation belongs to service %q", op.ID.Service)
		case i > 0 && s.Operations[i-1].ID.Name >= op.ID.Name:
			p, detail = OperationOrder, fmt.Sprintf("it follows %q; operations must be sorted by name without duplicates", s.Operations[i-1].ID.Name)
		case op.Kind < Modeled || op.Kind > Raw:
			p, detail = UnknownOperationKind, fmt.Sprintf("kind %d is not modeled, internal or raw", op.Kind)
		default:
			continue
		}
		return &DescriptorError{Problem: p, Service: s.ID, Operation: op.ID.Name, Detail: detail}
	}
	return nil
}

func checkRoutes(s *Service) error {
	if s.Routes == nil {
		return nil
	}
	for i, rt := range s.Routes.Routes {
		var op, detail string
		switch {
		case rt == nil || rt.Operation == nil:
			detail = fmt.Sprintf("route %d has no operation", i)
		case rt.Operation.Service != s:
			op, detail = rt.Operation.ID.Name, fmt.Sprintf("route %d names an operation of service %q", i, rt.Operation.ID.Service)
		// checkOperations ran first, so a descriptor in s.Operations sits at its Ordinal.
		case rt.Operation.Ordinal < 0 || rt.Operation.Ordinal >= len(s.Operations) || s.Operations[rt.Operation.Ordinal] != rt.Operation:
			op, detail = rt.Operation.ID.Name, fmt.Sprintf("route %d names an operation descriptor that is not in the service's operation list (ordinal %d); routes must point at the descriptors in Operations", i, rt.Operation.Ordinal)
		case rt.Operation.Kind != Modeled:
			op, detail = rt.Operation.ID.Name, fmt.Sprintf("route %d names a %s operation; only modeled operations are routed, internal ones are never claimed by the edge and raw ones by its prefix and host rules", i, rt.Operation.Kind)
		case rt.Method == "":
			op, detail = rt.Operation.ID.Name, fmt.Sprintf("route %d has no method", i)
		default:
			err := validatePath(rt.Path)
			if err == nil {
				err = validateQuery(rt.Query)
			}
			if err == nil {
				err = validateHeaders(rt.Headers)
			}
			if err == nil {
				continue
			}
			op, detail = rt.Operation.ID.Name, fmt.Sprintf("route %d: %v", i, err)
		}
		return &DescriptorError{Problem: InvalidRoute, Service: s.ID, Operation: op, Detail: detail}
	}
	return checkRouteConflicts(s)
}

// checkRouteConflicts rejects two rows every request matches alike: Match
// would always pick the earlier, leaving the later operation unreachable, and
// the http trait forbids such conflicting patterns. Rows that differ but can
// both match one request (?acl and ?tagging) are fine; Match ranks them.
func checkRouteConflicts(s *Service) error {
	seen := make(map[string]int, len(s.Routes.Routes))
	for i, rt := range s.Routes.Routes {
		sig := rt.Signature()
		j, dup := seen[sig]
		if !dup {
			seen[sig] = i
			continue
		}
		return &DescriptorError{
			Problem: ConflictingRoutes, Service: s.ID, Operation: rt.Operation.ID.Name,
			Detail: fmt.Sprintf("route %d has the same method, path shape, query conditions and required headers as route %d (operation %q), so no request can reach it; add a query or header condition that tells them apart",
				i, j, s.Routes.Routes[j].Operation.ID.Name),
		}
	}
	return nil
}
