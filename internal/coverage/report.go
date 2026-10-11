package coverage

import (
	"cmp"
	"slices"

	"github.com/sustinbebustin/understudy/internal/evidence"
	"github.com/sustinbebustin/understudy/internal/fidelity"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/protocoltest"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// Input is what a report is built from: the registry and one kernel's
// bindings for operation coverage, the checked-in protocol-test baseline
// and skip list and the corpus cases left ungenerated for protocol
// compliance, and the fidelity switches and overlay entries for evidence.
type Input struct {
	Registry    *registry.Registry
	Bindings    *kernel.Bindings
	Baseline    protocoltest.Baseline
	Skips       protocoltest.Skips
	Ungenerated []protocoltest.Ungenerated
	Switches    []fidelity.Entry
	Overlays    Overlays
}

// Report is the coverage report: protocol compliance, operation coverage
// and evidence, three measures that are never added together
// (ARCHITECTURE.md 3.4).
type Report struct {
	Protocols []Suite `json:"protocols"`
	// Ungenerated are the server-runnable corpus cases no generated table
	// holds, which the protocol counts therefore leave out.
	Ungenerated []UngeneratedCase `json:"ungenerated"`
	Services    []Service         `json:"services"`
	Switches    []Switch          `json:"switches"`
	Overlays    Overlays          `json:"overlays"`
}

// UngeneratedCase is a server-runnable corpus case the harness cannot run
// because the generator leaves it out, and why.
type UngeneratedCase struct {
	Protocol ref.ProtocolID    `json:"protocol"`
	Kind     protocoltest.Kind `json:"kind"`
	ID       string            `json:"id"`
	Reason   string            `json:"reason"`
}

// Suite is the compliance of one protocol's cases of one kind.
type Suite struct {
	Protocol ref.ProtocolID    `json:"protocol"`
	Kind     protocoltest.Kind `json:"kind"`
	Cases    int               `json:"cases"`
	Passed   int               `json:"passed"`
	// Failed are the cases neither passing nor skipped. The corpus test
	// fails on any of them a checked-in baseline does not record as a
	// known failure.
	Failed  int           `json:"failed"`
	Skipped []SkippedCase `json:"skipped"`
	// KnownFailures are the failed cases skips.json records as gaps in
	// understudy itself, with the reason and the owning package.
	KnownFailures []KnownFailure `json:"knownFailures"`
}

// KnownFailure is a case understudy fails because a part it builds on
// cannot yet express what the case asserts.
type KnownFailure struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

// SkippedCase is a case the corpus test does not hold understudy to.
type SkippedCase struct {
	ID       string `json:"id"`
	Category string `json:"category"`
	// Label and Source are an aws-differs entry's evidence.
	Label  evidence.Label `json:"label,omitempty"`
	Source string         `json:"source,omitempty"`
	Reason string         `json:"reason"`
}

// Service is the operation coverage of one service against its model.
type Service struct {
	Service ref.ServiceID `json:"service"`
	// Operations are the model's operations, which clients call.
	Operations []Operation `json:"operations"`
	// Internal are the overlay's typed operations the edge never claims.
	Internal []Operation `json:"internal"`
	// Raw are the synthetic raw-HTTP operations claimed by a prefix or
	// host rule.
	Raw []Operation `json:"raw"`
}

// Operation is one operation and what the kernel reports for it.
type Operation struct {
	Name   string `json:"name"`
	Status Status `json:"status"`
}

// Status is an operation's coverage: bound with the coverage its bind
// site stated, or unbound.
//
//sumtype:decl
type Status interface{ isStatus() }

// Full is an operation implemented as AWS behaves, as far as the research
// knows.
type Full struct{}

// StoredOnly is an operation that accepts, stores and echoes its input
// with no runtime effect.
type StoredOnly struct {
	Note string `json:"note"`
}

// Partial is an operation implemented with named gaps.
type Partial struct {
	Note string `json:"note"`
}

// Unbound is an operation without a handler; calls answer 501
// NotImplemented.
type Unbound struct{}

func (Full) isStatus()       {}
func (StoredOnly) isStatus() {}
func (Partial) isStatus()    {}
func (Unbound) isStatus()    {}

// Switch is a fidelity switch: a behavior chosen without AWS-recorded
// evidence, and what settles it.
type Switch struct {
	Name   string         `json:"name"`
	Value  string         `json:"value"`
	Label  evidence.Label `json:"label"`
	Ref    string         `json:"ref"`
	Record string         `json:"record,omitempty"`
}

// Build assembles the report.
func Build(in Input) Report {
	r := Report{Overlays: in.Overlays}
	if r.Overlays == nil {
		r.Overlays = OverlaysNotCarried{}
	}
	for _, s := range in.Baseline.Suites {
		r.Protocols = append(r.Protocols, suiteOf(s, in.Skips))
	}
	for _, u := range in.Ungenerated {
		r.Ungenerated = append(r.Ungenerated, UngeneratedCase{Protocol: u.Key.Protocol, Kind: u.Key.Kind, ID: u.Key.ID, Reason: u.Reason})
	}
	if in.Registry != nil && in.Bindings != nil {
		for _, svc := range in.Registry.Services() {
			r.Services = append(r.Services, serviceOf(svc, in.Bindings))
		}
	}
	slices.SortFunc(r.Services, func(a, b Service) int { return cmp.Compare(a.Service, b.Service) })
	for _, e := range in.Switches {
		r.Switches = append(r.Switches, Switch(e))
	}
	slices.SortFunc(r.Switches, func(a, b Switch) int {
		return cmp.Or(cmp.Compare(labelRank(a.Label), labelRank(b.Label)), cmp.Compare(a.Name, b.Name))
	})
	return r
}

func suiteOf(s protocoltest.Suite, skips protocoltest.Skips) Suite {
	out := Suite{Protocol: s.Protocol, Kind: s.Kind, Cases: s.Cases, Passed: len(s.Passing)}
	for _, sk := range skips {
		if sk.Key.Protocol != s.Protocol || sk.Key.Kind != s.Kind {
			continue
		}
		if _, gap := sk.Category.(protocoltest.Gap); gap {
			out.KnownFailures = append(out.KnownFailures, KnownFailure{ID: sk.Key.ID, Reason: sk.Reason})
			continue
		}
		c := SkippedCase{ID: sk.Key.ID, Category: sk.Category.Name(), Reason: sk.Reason}
		if d, ok := sk.Category.(protocoltest.AWSDiffers); ok {
			c.Label, c.Source = d.Label, d.Source
		}
		out.Skipped = append(out.Skipped, c)
	}
	out.Failed = max(out.Cases-out.Passed-len(out.Skipped), 0)
	return out
}

func serviceOf(svc *registry.Service, b *kernel.Bindings) Service {
	out := Service{Service: svc.ID}
	for _, op := range svc.Operations {
		o := Operation{Name: op.ID.Name, Status: statusOf(b.Status(op))}
		switch op.Kind {
		case registry.Modeled:
			out.Operations = append(out.Operations, o)
		case registry.Internal:
			out.Internal = append(out.Internal, o)
		case registry.Raw:
			out.Raw = append(out.Raw, o)
		}
	}
	for _, ops := range [][]Operation{out.Operations, out.Internal, out.Raw} {
		slices.SortStableFunc(ops, func(a, b Operation) int {
			return cmp.Or(cmp.Compare(statusRank(a.Status), statusRank(b.Status)), cmp.Compare(a.Name, b.Name))
		})
	}
	return out
}

func statusOf(s kernel.Status) Status {
	bound, ok := s.(kernel.Bound)
	if !ok {
		return Unbound{}
	}
	switch c := bound.Coverage.(type) {
	case registry.Full:
		return Full{}
	case registry.StoredOnly:
		return StoredOnly(c)
	case registry.Partial:
		return Partial(c)
	}
	return Unbound{}
}

// Statuses returns one value of each status, in report order.
func Statuses() []Status { return []Status{Full{}, StoredOnly{}, Partial{}, Unbound{}} }

// StatusName is a status's spelling in the report.
func StatusName(s Status) string {
	switch s.(type) {
	case Full:
		return "full"
	case StoredOnly:
		return "stored-only"
	case Partial:
		return "partial"
	case Unbound:
		return "unbound"
	}
	return "unknown"
}

func statusRank(s Status) int {
	name := StatusName(s)
	return slices.IndexFunc(Statuses(), func(x Status) bool { return StatusName(x) == name })
}

// labelRank orders labels strongest evidence first.
func labelRank(l evidence.Label) int {
	if i := slices.Index(evidence.Labels(), l); i >= 0 {
		return i
	}
	return len(evidence.Labels())
}
