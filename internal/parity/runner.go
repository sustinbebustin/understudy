package parity

import (
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/sustinbebustin/understudy/internal/normalize"
)

// loadedStep is a scenario step with its fixture; fixture is nil for a
// queued step that has none yet.
type loadedStep struct {
	step    Step
	fixture *Fixture
}

// loadSteps reads every step's fixture before anything is sent, so a
// scenario that cannot run fails before it changes understudy's state.
func loadSteps(fsys fs.FS, id string, sc Scenario) ([]loadedStep, error) {
	queued := map[string]bool{}
	for _, c := range sc.Record {
		if rc, ok := c.(RecCommand); ok {
			queued[rc.Step] = true
		}
	}
	steps := make([]loadedStep, 0, len(sc.Steps))
	for i, s := range sc.Steps {
		f, err := ReadFixture(fsys, s.Fixture)
		switch {
		case errors.Is(err, fs.ErrNotExist) && queued[s.Name()]:
			steps = append(steps, loadedStep{step: s})
		case errors.Is(err, fs.ErrNotExist):
			return nil, &ScenarioError{Scenario: id, Field: fmt.Sprintf("steps[%d].fixture", i), Reason: fmt.Sprintf(
				"testdata/parity/%s does not exist and no record entry records the step %s; record it, or add {\"step\": %q, \"argv\": [...]} to record",
				s.Fixture, s.Name(), s.Name())}
		case err != nil:
			return nil, err
		default:
			steps = append(steps, loadedStep{step: s, fixture: &f})
		}
	}
	return steps, nil
}

// runner replays one scenario's steps in order.
type runner struct {
	id     string
	sc     Scenario
	t      Transport
	docker bool
	// bound are the bindings so far, in the order they were made.
	bound []binding
	// halted is the reason every step from here on differs, once a setup
	// call failed; "" while none has.
	halted string
}

// binding is one bound value: AWS's and understudy's at the same path.
type binding struct {
	aws, ours string
}

// bind records aws as bound to ours; false when aws is bound already to
// another value.
func (r *runner) bind(aws, ours string) bool {
	for _, b := range r.bound {
		if b.aws == aws {
			return b.ours == ours
		}
	}
	r.bound = append(r.bound, binding{aws: aws, ours: ours})
	return true
}

// toUnderstudy substitutes understudy's values for the bound AWS values in a
// request.
func (r *runner) toUnderstudy() []normalize.Replacement {
	rs := make([]normalize.Replacement, len(r.bound))
	for i, b := range r.bound {
		rs[i] = normalize.Replacement{Old: b.aws, New: b.ours}
	}
	return rs
}

// toAWS is the bound normalizer: AWS's values for understudy's in a
// response.
func (r *runner) toAWS() []normalize.Replacement {
	rs := make([]normalize.Replacement, len(r.bound))
	for i, b := range r.bound {
		rs[i] = normalize.Replacement{Old: b.ours, New: b.aws}
	}
	return rs
}

// stepRun is one replayed step and, for a compared one, what its claims
// are checked against.
type stepRun struct {
	result   StepResult
	compared *compared
}

// compared is a compared step's final exchange as the comparison saw it.
type compared struct {
	recorded, ours Response
	bindings       []normalize.Replacement
	waivers        []Path
	// binds are the step's own binds, and before the runner as it stood
	// before them, so the sensitivity check can bind them again from the
	// mutated recording.
	binds  []Bind
	before runner
}

// step runs setup due before step i, then the step.
func (r *runner) step(i int, ls loadedStep) (stepRun, error) {
	res := StepResult{Step: ls.step.Name(), Fixture: ls.step.Fixture}
	if r.docker {
		if err := r.setup(i, res.Step); err != nil {
			return stepRun{}, err
		}
	}
	f := ls.fixture
	switch {
	case r.halted != "":
		res.Outcome = Differs{Reason: r.halted}
	case f == nil:
		res.Outcome = Pending{}
	case len(f.Exchanges) == 0:
		res.Outcome = Skipped{Reason: SkipNoExchange}
	case !r.docker:
		res.Outcome = Skipped{Reason: SkipDocker}
	default:
		return r.replay(ls.step, *f, res)
	}
	return stepRun{result: res}, nil
}

// setup issues the setup calls due before step name, the i-th: those whose
// before names it, and before the first step those that name none.
func (r *runner) setup(i int, name string) error {
	for j, c := range r.sc.Setup {
		if r.halted != "" || c.Before != name && (c.Before != "" || i != 0) {
			continue
		}
		halt, err := r.call(j, c)
		if err != nil {
			return err
		}
		r.halted = halt
	}
	return nil
}

// call issues setup call j; the result is the halt reason when it fails.
func (r *runner) call(j int, c Call) (string, error) {
	resp, err := r.t.Call(c.Service, c.Operation, []byte(substituteJSON("application/json", c.Input, r.toUnderstudy())))
	if err != nil {
		return "", fmt.Errorf("replaying testdata/parity/%s: setup %d %s.%s: %w", r.id, j, c.Service, c.Operation, err)
	}
	label := fmt.Sprintf("setup %d %s.%s", j, c.Service, c.Operation)
	if resp.Status < 200 || resp.Status > 299 {
		return label + ": " + strconv.Itoa(resp.Status), nil
	}
	for _, b := range c.Bind {
		if ours, ok := valueAt(resp, b.Path); !ok || !r.tryBind(b.Shape, b.AWS, ours) {
			return label + ": bind " + string(b.Path), nil
		}
	}
	return "", nil
}

// tryBind binds aws to ours when both have the shape sh and aws is not
// bound to another value; false when it does not.
func (r *runner) tryBind(sh Shape, aws, ours string) bool {
	return CheckBind(sh, aws, ours, r.sc.Account, r.sc.Region) == nil && r.bind(aws, ours)
}

// replay sends a step's considered exchanges and compares them (Run rules
// 4 to 8).
func (r *runner) replay(s Step, f Fixture, res StepResult) (stepRun, error) {
	first := 0
	if s.Compare == CompareFinal {
		first = len(f.Exchanges) - 1
	}
	xs := f.Exchanges[first:]
	switch {
	case slices.ContainsFunc(xs, requestIncomplete):
		res.Outcome = Reconstructed{}
		return stepRun{result: res}, nil
	case slices.ContainsFunc(xs, func(x Exchange) bool { return !routable(*x.Request) }):
		res.Outcome = Skipped{Reason: SkipRESTRouting}
		return stepRun{result: res}, nil
	}
	if s.Advance > 0 {
		if err := r.t.Advance(s.Advance); err != nil {
			return stepRun{}, fmt.Errorf("replaying testdata/parity/%s: advancing the clock by %s before %s: %w", r.id, s.Advance, res.Step, err)
		}
	}
	ours := make([]Response, len(xs))
	for i, x := range xs {
		out, err := outgoing(*x.Request, r.toUnderstudy(), r.sc.Region)
		if err == nil {
			ours[i], res.Operation, err = r.t.Send(out)
		}
		if err != nil {
			return stepRun{}, fmt.Errorf("replaying testdata/parity/%s exchange %d: %w", s.Fixture, first+i, err)
		}
	}
	if slices.ContainsFunc(xs, responseIncomplete) {
		res.Outcome = Reconstructed{}
		return stepRun{result: res}, nil
	}
	return r.compare(s, xs, ours, first, res), nil
}

// compare reads the step's binds from its final exchange, then compares
// every considered exchange with the bindings, its own included.
func (r *runner) compare(s Step, xs []Exchange, ours []Response, first int, res StepResult) stepRun {
	last := len(xs) - 1
	recorded := *xs[last].Response
	before := *r
	failed := r.readBinds(s.Bind, recorded, ours[last])
	c := compared{recorded: recorded, ours: ours[last], bindings: r.toAWS(), waivers: r.waivers(res.Step), binds: s.Bind, before: before}
	for i, x := range xs {
		prefix := Path("")
		if i < last {
			prefix = Path("exchange/" + strconv.Itoa(first+i) + "/")
		}
		for _, d := range Compare(*x.Response, ours[i], CompareOptions{Bindings: c.bindings}) {
			d.Path = prefix + d.Path
			if !waived(d.Path, c.waivers) {
				res.Differences = append(res.Differences, d)
			}
		}
	}
	res.Outcome = Match{}
	if len(res.Differences) > 0 || len(failed) > 0 {
		res.Outcome = Differs{Reason: differsReason(res.Differences, failed)}
	}
	return stepRun{result: res, compared: &c}
}

// readBinds binds each of binds from the recorded response and
// understudy's, and names those that fail: a value absent on either side,
// outside its shape, or bound already to another value.
func (r *runner) readBinds(binds []Bind, recorded, ours Response) []string {
	var failed []string
	for _, b := range binds {
		aws, okAWS := valueAt(recorded, b.Path)
		got, okOurs := valueAt(ours, b.Path)
		if !okAWS || !okOurs || !r.tryBind(b.Shape, aws, got) {
			failed = append(failed, "bind "+string(b.Path))
		}
	}
	return failed
}

// waivers are the fields waived in step name.
func (r *runner) waivers(name string) []Path {
	var ps []Path
	for _, w := range r.sc.Waivers {
		if w.Step == name {
			ps = append(ps, w.Field)
		}
	}
	return ps
}

func waived(p Path, waivers []Path) bool {
	return slices.ContainsFunc(waivers, func(w Path) bool { return w.Covers(p) })
}

// differsReason is a Differs outcome's reason: the distinct paths that
// differ in comparison order, then each failed bind, without values so the
// baseline stays stable.
func differsReason(ds []Difference, failed []string) string {
	var parts []string
	for _, d := range ds {
		if !slices.Contains(parts, string(d.Path)) {
			parts = append(parts, string(d.Path))
		}
	}
	return strings.Join(append(parts, failed...), ", ")
}

// requestIncomplete reports whether x's recorded request cannot be resent:
// absent, without a host, or with a body the capture did not keep.
func requestIncomplete(x Exchange) bool {
	if x.Request == nil {
		return true
	}
	u, err := url.Parse(x.Request.URL)
	if err != nil || u.Host == "" {
		return true
	}
	switch x.Request.Body.(type) {
	case UnrecordedBody, OmittedBody:
		return true
	case NoBody, TextBody, BinaryBody, nil:
	}
	return false
}

// responseIncomplete reports whether x's recorded response cannot be
// compared: absent, without a status, or with a body the capture did not
// keep.
func responseIncomplete(x Exchange) bool {
	if x.Response == nil || x.Response.Status == 0 {
		return true
	}
	_, unrecorded := x.Response.Body.(UnrecordedBody)
	return unrecorded
}

// claim is what the step says of a settles entry on field: a match is
// settled when the comparison still sees the field changed (the
// sensitivity check), a difference contradicts, anything else is open.
func (s stepRun) claim(field Path) (ClaimState, error) {
	switch s.result.Outcome.(type) {
	case Match:
		return s.compared.sensitivity(field)
	case Differs:
		return Contradicted{}, nil
	case Reconstructed, Skipped, Pending, nil:
	}
	return Open{}, nil
}

// sensitivity mutates the recorded final response at field and compares it
// with understudy's captured answer (D6): any unwaived difference settles
// the claim; otherwise a waiver hid the field when the mutation changed the
// unwaived comparison, else a normalizer did.
//
// The re-comparison stands in for the replay of the mutated recording the
// spec describes ("Sensitivity check"), which differs from the first replay
// in one more way: the step's own binds read the mutated value. So they are
// bound again from it, on the bindings made before the step: a value only
// the bind carries is hidden by the bound normalizer, and a mutation its
// bind refuses fails the step, which settles the claim.
func (c *compared) sensitivity(field Path) (ClaimState, error) {
	m, err := Mutate(c.recorded, field)
	if err != nil {
		return nil, err
	}
	again := c.before
	again.bound = slices.Clone(c.before.bound)
	if len(again.readBinds(c.binds, m, c.ours)) > 0 {
		return Settled{}, nil
	}
	mutated := Compare(m, c.ours, CompareOptions{Bindings: again.toAWS()})
	if slices.ContainsFunc(mutated, func(d Difference) bool { return !waived(d.Path, c.waivers) }) {
		return Settled{}, nil
	}
	if !slices.Equal(mutated, Compare(c.recorded, c.ours, CompareOptions{Bindings: c.bindings})) {
		return Insensitive{By: HiddenByWaiver}, nil
	}
	return Insensitive{By: HiddenByNormalizer}, nil
}
