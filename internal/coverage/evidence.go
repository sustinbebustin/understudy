package coverage

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"github.com/sustinbebustin/understudy/internal/evidence"
	"github.com/sustinbebustin/understudy/internal/fidelity"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// Claim is one piece of evidence attributed to an operation, and how the
// page labels it.
type Claim struct {
	Source ClaimSource
	Label  ClaimLabel
}

// ClaimSource is where a claim comes from.
//
//sumtype:decl
type ClaimSource interface{ isClaimSource() }

// FromSwitch is a fidelity switch whose Ops list the operation.
type FromSwitch struct {
	Name string
}

// FromOverlay is an overlay evidence item whose shape is the operation, its
// input or output, or one of its errors.
type FromOverlay struct {
	File, Shape string
	Covers      []string
}

// FromStep is a parity step replayed against the operation.
type FromStep struct {
	Scenario, Step, Fixture string
}

func (FromSwitch) isClaimSource()  {}
func (FromOverlay) isClaimSource() {}
func (FromStep) isClaimSource()    {}

// ClaimLabel is a claim's evidence as the page prints it.
//
//sumtype:decl
type ClaimLabel interface{ isClaimLabel() }

// CodeLabel is the label the code or the overlay states.
type CodeLabel struct {
	Label evidence.Label
}

// RecordedLabel is a recorded step that matches AWS, printed
// "aws-recorded (<fixture>)".
type RecordedLabel struct {
	Fixture string
}

// ContradictedLabel is a recorded step that differs from AWS, printed
// "contradicted (<fixture>)".
type ContradictedLabel struct {
	Fixture string
}

func (CodeLabel) isClaimLabel()         {}
func (RecordedLabel) isClaimLabel()     {}
func (ContradictedLabel) isClaimLabel() {}

// ClaimLabelText is the label as the page prints it; "" for nil.
func ClaimLabelText(l ClaimLabel) string {
	switch l := l.(type) {
	case CodeLabel:
		return string(l.Label)
	case RecordedLabel:
		return string(evidence.AWSRecorded) + " (" + l.Fixture + ")"
	case ContradictedLabel:
		return "contradicted (" + l.Fixture + ")"
	}
	return ""
}

// claimRank orders claim labels weakest first: contradicted 0, the code
// labels from inferred 1 to aws-recorded 5, and a recorded label with
// aws-recorded. A label outside the vocabulary ranks 0.
func claimRank(l ClaimLabel) int {
	top := len(evidence.Labels())
	switch l := l.(type) {
	case RecordedLabel:
		return top
	case CodeLabel:
		return top - labelRank(l.Label)
	case ContradictedLabel:
	}
	return 0
}

// Evidence is one operation's evidence cell: its weakest claim and how many
// of its claims carry that label.
type Evidence struct {
	// Weakest is nil when the operation has no claim.
	Weakest   ClaimLabel
	AtWeakest int
	Claims    int
}

// String is the cell's text: "unassessed" for no claim, else the weakest
// label and "<n> of <m> claims".
func (e Evidence) String() string {
	if e.Claims == 0 || e.Weakest == nil {
		return "unassessed"
	}
	noun := "claims"
	if e.Claims == 1 {
		noun = "claim"
	}
	return fmt.Sprintf("%s, %d of %d %s", ClaimLabelText(e.Weakest), e.AtWeakest, e.Claims, noun)
}

// Retirement is a switch or overlay evidence item a recording settles while
// its code label is below aws-recorded.
type Retirement struct {
	Source  ClaimSource
	Code    evidence.Label
	Fixture string
	Field   string
}

// settlement is what the baseline's settles entries say of one switch or
// overlay item: the label they give it, nil when none settles or
// contradicts it, and the deciding entry's fixture and field.
type settlement struct {
	label   ClaimLabel
	fixture string
	field   string
}

// settle reads the baseline's settles entries that target matches. One
// contradicted entry decides, since the recording that differs is a finding
// the page must not hide; else the first settled one does. Insensitive and
// open entries settle nothing.
func (p ParityRead) settle(target func(ClaimTarget) bool) settlement {
	var settled settlement
	for _, sc := range p.Scenarios {
		for _, c := range sc.Claims {
			if !target(c.Target) {
				continue
			}
			fixture := sc.fixture(c.Step)
			switch c.Result.(type) {
			case ClaimContradicted:
				return settlement{label: ContradictedLabel{Fixture: fixture}, fixture: fixture, field: c.Field}
			case ClaimSettled:
				if settled.label == nil {
					settled = settlement{label: RecordedLabel{Fixture: fixture}, fixture: fixture, field: c.Field}
				}
			case ClaimInsensitive, ClaimOpen:
			}
		}
	}
	return settled
}

// fixture is the fixture of sc's step named step, "" when sc lists none.
func (sc ParityScenario) fixture(step string) string {
	for _, s := range sc.Steps {
		if s.Step == step {
			return s.Fixture
		}
	}
	return ""
}

// parity is r's baseline; an empty one when r carries none.
func (r Report) parity() ParityRead {
	if p, ok := r.Parity.(ParityRead); ok {
		return p
	}
	return ParityRead{}
}

// switchSettlement is what the baseline says of switch sw.
func (r Report) switchSettlement(sw Switch) settlement {
	return r.parity().settle(func(t ClaimTarget) bool {
		s, ok := t.(TargetSwitch)
		return ok && s.Name == sw.Name
	})
}

// overlaySettlement is what the baseline says of overlay item e: the
// entries on its shape whose covers are all among the item's. An entry
// without covers names every item of the shape, and an item without covers
// backs every trait of its entry.
func (r Report) overlaySettlement(e OverlayEntry) settlement {
	return r.parity().settle(func(t ClaimTarget) bool {
		o, ok := t.(TargetOverlay)
		if !ok || o.Shape != e.Shape {
			return false
		}
		return len(e.Covers) == 0 || !slices.ContainsFunc(o.Covers, func(c string) bool { return !slices.Contains(e.Covers, c) })
	})
}

// labelOf is the label a switch or overlay item shows: what the baseline
// settles of it, else the code's.
func labelOf(s settlement, code evidence.Label) ClaimLabel {
	if s.label != nil {
		return s.label
	}
	return CodeLabel{Label: code}
}

// switchesByName are r's switches in claim order.
func (r Report) switchesByName() []Switch {
	out := slices.Clone(r.Switches)
	slices.SortStableFunc(out, func(a, b Switch) int { return cmp.Compare(a.Name, b.Name) })
	return out
}

// overlayItems are r's overlay evidence items in claim order, none when r
// carries no overlay files.
func (r Report) overlayItems() []OverlayEntry {
	read, ok := r.Overlays.(OverlaysRead)
	if !ok {
		return nil
	}
	out := slices.Clone(read.Entries)
	SortOverlayEntries(out)
	return out
}

// operation is the operation of service svc named op, in any of its
// lists; ok is false when the report has none.
func (r Report) operation(svc ref.ServiceID, op string) (Operation, bool) {
	for _, s := range r.Services {
		if s.Service != svc {
			continue
		}
		for _, list := range [][]Operation{s.Operations, s.Internal, s.Raw} {
			if i := slices.IndexFunc(list, func(o Operation) bool { return o.Name == op }); i >= 0 {
				return list[i], true
			}
		}
	}
	return Operation{}, false
}

// Claims are the claims attributed to operation op of service svc, in
// claim order: the switches whose Ops list it, by name; the overlay items
// whose shape, a member's taken as the shape that holds it, is one of the
// operation's, in SortOverlayEntries order; and the baseline's steps
// routed to it that were compared (match or differs), by scenario id, then
// in scenario order. An overlay item on the service shape is no
// operation's: it applies to them all, and the overlay table shows it. An
// unbound operation's claims are its steps only: it answers 501, so the
// behavior a switch or an overlay item describes is not one it has.
func (r Report) Claims(svc ref.ServiceID, op string) []Claim {
	id := ref.OperationID{Service: svc, Name: op}.String()
	var out []Claim
	o, ok := r.operation(svc, op)
	if _, unbound := o.Status.(Unbound); ok && unbound {
		return r.stepClaims(id)
	}
	for _, sw := range r.switchesByName() {
		if slices.Contains(sw.Ops, id) {
			out = append(out, Claim{Source: FromSwitch{Name: sw.Name}, Label: labelOf(r.switchSettlement(sw), sw.Label)})
		}
	}
	if ok {
		for _, e := range r.overlayItems() {
			shape, _, _ := strings.Cut(e.Shape, "$")
			if slices.Contains(o.Shapes, shape) {
				out = append(out, Claim{Source: FromOverlay{File: e.File, Shape: e.Shape, Covers: e.Covers}, Label: labelOf(r.overlaySettlement(e), e.Label)})
			}
		}
	}
	return append(out, r.stepClaims(id)...)
}

// stepClaims are the claims of the baseline's steps routed to operation id
// and compared, by scenario id, then in scenario order.
func (r Report) stepClaims(id string) []Claim {
	var out []Claim
	scenarios := slices.Clone(r.parity().Scenarios)
	slices.SortStableFunc(scenarios, func(a, b ParityScenario) int { return cmp.Compare(a.Scenario, b.Scenario) })
	for _, sc := range scenarios {
		for _, s := range sc.Steps {
			if s.Operation != id {
				continue
			}
			var label ClaimLabel
			switch s.Outcome.(type) {
			case StepMatch:
				label = RecordedLabel{Fixture: s.Fixture}
			case StepDiffers:
				label = ContradictedLabel{Fixture: s.Fixture}
			case StepReconstructed, StepSkipped, StepPending:
				continue
			}
			out = append(out, Claim{Source: FromStep{Scenario: sc.Scenario, Step: s.Step, Fixture: s.Fixture}, Label: label})
		}
	}
	return out
}

// OperationEvidence is the evidence cell of operation op of service svc:
// the first of its weakest claims in claim order, and how many claims share
// that rank.
func (r Report) OperationEvidence(svc ref.ServiceID, op string) Evidence {
	claims := r.Claims(svc, op)
	e := Evidence{Claims: len(claims)}
	for _, c := range claims {
		switch {
		case e.Weakest == nil || claimRank(c.Label) < claimRank(e.Weakest):
			e.Weakest, e.AtWeakest = c.Label, 1
		case claimRank(c.Label) == claimRank(e.Weakest):
			e.AtWeakest++
		}
	}
	return e
}

// PendingRetirement lists the switches, by name, then the overlay evidence
// items, in SortOverlayEntries order, that a recording settles while their
// code label is below aws-recorded; none when the report carries no
// baseline.
func (r Report) PendingRetirement() []Retirement {
	var out []Retirement
	for _, sw := range r.switchesByName() {
		if s := r.switchSettlement(sw); pending(s, sw.Label) {
			out = append(out, Retirement{Source: FromSwitch{Name: sw.Name}, Code: sw.Label, Fixture: s.fixture, Field: s.field})
		}
	}
	for _, e := range r.overlayItems() {
		if s := r.overlaySettlement(e); pending(s, e.Label) {
			out = append(out, Retirement{Source: FromOverlay{File: e.File, Shape: e.Shape, Covers: e.Covers}, Code: e.Label, Fixture: s.fixture, Field: s.field})
		}
	}
	return out
}

// pending reports whether s settles a claim whose code label is below
// aws-recorded.
func pending(s settlement, code evidence.Label) bool {
	_, recorded := s.label.(RecordedLabel)
	return recorded && code != evidence.AWSRecorded
}

// CheckEvidence checks the switches' Ops and Settles against the services
// and the parity baseline: every Ops entry names an operation of the
// report's services; every switch claim of the baseline targets a switch
// whose Settles names that claim's step; every SettledBy names a step the
// baseline lists with a claim on that switch; and, when the overlays are
// read, every overlay claim's shape is an overlay entry's. The
// *ParityError lists every problem. A report without the baseline is
// refused, since there is nothing to check against.
func (r Report) CheckEvidence() error {
	p, ok := r.Parity.(ParityRead)
	if !ok {
		return &ParityError{Problems: []string{"the report carries no parity baseline to check the switches against; read internal/parity/baseline.json with ParseParity first"}}
	}
	problems := r.opsProblems()
	settledBy := map[string]string{}
	for _, sw := range r.Switches {
		if s, ok := sw.Settles.(fidelity.SettledBy); ok {
			settledBy[sw.Name] = s.Step.String()
		}
	}
	steps, claimed := map[string]bool{}, map[string]bool{}
	for _, sc := range p.Scenarios {
		for _, s := range sc.Steps {
			steps[sc.Scenario+"#"+s.Step] = true
		}
		for _, c := range sc.Claims {
			step := sc.Scenario + "#" + c.Step
			if t, ok := c.Target.(TargetSwitch); ok {
				claimed[t.Name+" "+step] = true
			}
			if problem := r.claimProblem(c.Target, step, settledBy); problem != "" {
				problems = append(problems, problem)
			}
		}
	}
	for _, sw := range r.switchesByName() {
		step, ok := settledBy[sw.Name]
		switch {
		case !ok:
		case !steps[step]:
			problems = append(problems, fmt.Sprintf("switch %s: Settles names %s, a step the parity baseline does not list", sw.Name, step))
		case !claimed[sw.Name+" "+step]:
			problems = append(problems, fmt.Sprintf("switch %s: Settles names %s, but no claim of that step targets the switch", sw.Name, step))
		}
	}
	if len(problems) > 0 {
		return &ParityError{Problems: problems}
	}
	return nil
}

// opsProblems lists each switch Ops entry that names no operation of the
// report's services, switches by name.
func (r Report) opsProblems() []string {
	ops := map[string]bool{}
	for _, s := range r.Services {
		for _, list := range [][]Operation{s.Operations, s.Internal, s.Raw} {
			for _, o := range list {
				ops[ref.OperationID{Service: s.Service, Name: o.Name}.String()] = true
			}
		}
	}
	var problems []string
	for _, sw := range r.switchesByName() {
		for _, op := range sw.Ops {
			if !ops[op] {
				problems = append(problems, fmt.Sprintf("switch %s: Ops entry %q names no operation of the report's services", sw.Name, op))
			}
		}
	}
	return problems
}

// claimProblem is what is wrong with a baseline claim on target at step,
// "" when nothing is: a switch that is not declared or whose Settles names
// another step, or, when the overlays are read, a shape no overlay entry
// has, or a claim without covers on a shape with several evidence items,
// which would settle or contradict items for traits its field does not
// decide.
func (r Report) claimProblem(target ClaimTarget, step string, settledBy map[string]string) string {
	switch t := target.(type) {
	case TargetSwitch:
		want, settles := settledBy[t.Name]
		switch {
		case !slices.ContainsFunc(r.Switches, func(sw Switch) bool { return sw.Name == t.Name }):
			return fmt.Sprintf("%s claims switch %s, which no wired service declares", step, t.Name)
		case !settles:
			return fmt.Sprintf("%s claims switch %s, whose Settles names no scenario step; set it to SettledBy %s", step, t.Name, step)
		case want != step:
			return fmt.Sprintf("%s claims switch %s, whose Settles names %s", step, t.Name, want)
		}
	case TargetOverlay:
		if _, read := r.Overlays.(OverlaysRead); !read {
			return ""
		}
		var traits []string
		n := 0
		for _, e := range r.overlayItems() {
			if e.Shape == t.Shape {
				n++
				traits = append(traits, e.Covers...)
			}
		}
		switch {
		case n == 0:
			return fmt.Sprintf("%s claims overlay shape %s, which no overlay entry has", step, t.Shape)
		case n > 1 && len(t.Covers) == 0:
			return fmt.Sprintf("%s claims overlay shape %s without covers, which names all %d of its evidence items; "+
				"give the settles entry the covers of the item its field decides, among %v", step, t.Shape, n, traits)
		}
	}
	return ""
}
