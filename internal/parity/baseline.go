package parity

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// Baseline is internal/parity/baseline.json: the ratchet of every
// scenario's step outcomes and claim results.
type Baseline struct {
	Scenarios []BaselineScenario
}

// BaselineScenario is one scenario's entry.
type BaselineScenario struct {
	Scenario string
	Steps    []BaselineStep
	Claims   []BaselineClaim
}

// BaselineStep is one step's recorded outcome and routed operation.
type BaselineStep struct {
	Step, Fixture, Operation string
	Outcome                  Outcome
}

// BaselineClaim is one settles entry's recorded result.
type BaselineClaim struct {
	Target Target
	Step   string
	Field  Path
	Result ClaimState
}

// BaselineError reports a baseline file that does not follow the format.
type BaselineError struct {
	Reason string
}

func (e *BaselineError) Error() string {
	return "internal/parity/baseline.json: " + e.Reason + "; rewrite it with just parity-baseline rather than by hand"
}

// The outcome, target and result kinds as baseline.json spells them.
const (
	kindMatch         = "match"
	kindDiffers       = "differs"
	kindReconstructed = "reconstructed"
	kindSkipped       = "skipped"
	kindPending       = "pending"

	kindSwitch  = "switch"
	kindOverlay = "overlay"

	kindSettled      = "settled"
	kindInsensitive  = "insensitive"
	kindContradicted = "contradicted"
	kindOpen         = "open"
)

// OutcomeName is an outcome's kind in baseline.json: "match", "differs",
// "reconstructed", "skipped" or "pending"; "" for nil.
func OutcomeName(o Outcome) string {
	switch o.(type) {
	case Match:
		return kindMatch
	case Differs:
		return kindDiffers
	case Reconstructed:
		return kindReconstructed
	case Skipped:
		return kindSkipped
	case Pending:
		return kindPending
	}
	return ""
}

// The file form of a baseline, members in the canonical order. Pointers
// tell an absent member from an empty one, which the parser refuses.
type (
	baselineDoc struct {
		Version   *int                   `json:"version"`
		Scenarios *[]baselineScenarioDoc `json:"scenarios"`
	}
	baselineScenarioDoc struct {
		Scenario string     `json:"scenario"`
		Steps    []stepDoc  `json:"steps"`
		Claims   []claimDoc `json:"claims,omitempty"`
	}
	stepDoc struct {
		Step      string      `json:"step"`
		Fixture   string      `json:"fixture"`
		Operation string      `json:"operation,omitempty"`
		Outcome   *outcomeDoc `json:"outcome"`
	}
	outcomeDoc struct {
		Kind   string  `json:"kind"`
		Reason *string `json:"reason,omitempty"`
	}
	claimDoc struct {
		Target *targetDoc `json:"target"`
		Step   string     `json:"step"`
		Field  string     `json:"field"`
		Result *resultDoc `json:"result"`
	}
	targetDoc struct {
		Kind   string   `json:"kind"`
		Name   *string  `json:"name,omitempty"`
		Shape  *string  `json:"shape,omitempty"`
		Covers []string `json:"covers,omitempty"`
	}
	resultDoc struct {
		Kind string  `json:"kind"`
		By   *string `json:"by,omitempty"`
	}
)

// ParseBaseline reads baseline.json strictly: an unknown or missing member,
// an unknown kind, a reason or by its kind lacks or does not take, a field
// that is not a leaf path, or a claim on a step its scenario does not list
// is a *BaselineError.
func ParseBaseline(data []byte) (Baseline, error) {
	var doc baselineDoc
	if err := json.Unmarshal(data, &doc, json.RejectUnknownMembers(true)); err != nil {
		return Baseline{}, &BaselineError{Reason: "not a baseline document: " + err.Error()}
	}
	switch {
	case doc.Version == nil:
		return Baseline{}, &BaselineError{Reason: "the version member is missing"}
	case *doc.Version != 1:
		return Baseline{}, &BaselineError{Reason: fmt.Sprintf("version %d is not 1, the only version this build reads", *doc.Version)}
	case doc.Scenarios == nil:
		return Baseline{}, &BaselineError{Reason: "the scenarios member is missing"}
	}
	var b Baseline
	seen := map[string]bool{}
	for i, sd := range *doc.Scenarios {
		field := fmt.Sprintf("scenarios[%d]", i)
		if seen[sd.Scenario] {
			return Baseline{}, &BaselineError{Reason: fmt.Sprintf("%s: scenario %q is listed twice", field, sd.Scenario)}
		}
		seen[sd.Scenario] = true
		sc, reason := sd.scenario(field)
		if reason != "" {
			return Baseline{}, &BaselineError{Reason: reason}
		}
		b.Scenarios = append(b.Scenarios, sc)
	}
	return b, nil
}

func (d baselineScenarioDoc) scenario(field string) (sc BaselineScenario, reason string) {
	if !scenarioIDPattern.MatchString(d.Scenario) {
		return sc, fmt.Sprintf("%s.scenario %q is not a scenario id <svc>/<item>", field, d.Scenario)
	}
	if len(d.Steps) == 0 {
		return sc, field + ".steps is empty; every scenario has a step"
	}
	sc.Scenario = d.Scenario
	steps := map[string]bool{}
	for i, s := range d.Steps {
		sf := fmt.Sprintf("%s.steps[%d]", field, i)
		switch {
		case s.Step == "":
			return sc, sf + ".step is missing"
		case steps[s.Step]:
			return sc, fmt.Sprintf("%s.step %q is listed twice", sf, s.Step)
		case s.Fixture == "":
			return sc, sf + ".fixture is missing"
		case s.Outcome == nil:
			return sc, sf + ".outcome is missing"
		}
		steps[s.Step] = true
		o, reason := s.Outcome.outcome()
		if reason != "" {
			return sc, sf + ".outcome: " + reason
		}
		sc.Steps = append(sc.Steps, BaselineStep{Step: s.Step, Fixture: s.Fixture, Operation: s.Operation, Outcome: o})
	}
	for i, c := range d.Claims {
		claim, reason := c.claim(steps)
		if reason != "" {
			return sc, fmt.Sprintf("%s.claims[%d]%s", field, i, reason)
		}
		sc.Claims = append(sc.Claims, claim)
	}
	return sc, ""
}

func (d outcomeDoc) outcome() (o Outcome, reason string) {
	switch d.Kind {
	case kindMatch, kindReconstructed, kindPending:
		if d.Reason != nil {
			return nil, fmt.Sprintf("a %s outcome takes no reason", d.Kind)
		}
		return map[string]Outcome{kindMatch: Match{}, kindReconstructed: Reconstructed{}, kindPending: Pending{}}[d.Kind], ""
	case kindDiffers:
		if d.Reason == nil || *d.Reason == "" {
			return nil, "a differs outcome needs the reason: the paths that differ"
		}
		return Differs{Reason: *d.Reason}, ""
	case kindSkipped:
		if d.Reason == nil {
			return nil, "a skipped outcome needs the reason: docker, rest-routing or no-exchange"
		}
		r := SkipReason(*d.Reason)
		if !slices.Contains([]SkipReason{SkipDocker, SkipRESTRouting, SkipNoExchange}, r) {
			return nil, fmt.Sprintf("skip reason %q is not docker, rest-routing or no-exchange", r)
		}
		return Skipped{Reason: r}, ""
	}
	return nil, fmt.Sprintf("kind %q is not match, differs, reconstructed, skipped or pending", d.Kind)
}

func (d claimDoc) claim(steps map[string]bool) (c BaselineClaim, reason string) {
	switch {
	case d.Target == nil:
		return c, ".target is missing"
	case d.Result == nil:
		return c, ".result is missing"
	case !steps[d.Step]:
		return c, fmt.Sprintf(".step %q is not a step of the scenario", d.Step)
	}
	p, err := ParsePath(d.Field)
	if err != nil {
		return c, ".field: " + err.Error()
	}
	if reason := p.leafReason(); reason != "" {
		return c, fmt.Sprintf(".field %q %s", d.Field, reason)
	}
	t, reason := d.Target.target()
	if reason != "" {
		return c, ".target: " + reason
	}
	r, reason := d.Result.result()
	if reason != "" {
		return c, ".result: " + reason
	}
	return BaselineClaim{Target: t, Step: d.Step, Field: p, Result: r}, ""
}

func (d targetDoc) target() (t Target, reason string) {
	switch d.Kind {
	case kindSwitch:
		if d.Name == nil || *d.Name == "" || d.Shape != nil || d.Covers != nil {
			return nil, "a switch target has a name and nothing else"
		}
		return SwitchTarget{Name: *d.Name}, ""
	case kindOverlay:
		if d.Shape == nil || !isShapeID(*d.Shape) || d.Name != nil {
			return nil, "an overlay target has a shape id, such as com.amazonaws.kms#AlreadyExistsException, optional covers and no name"
		}
		return OverlayTarget{Shape: *d.Shape, Covers: d.Covers}, ""
	}
	return nil, fmt.Sprintf("kind %q is not switch or overlay", d.Kind)
}

func (d resultDoc) result() (r ClaimState, reason string) {
	if d.Kind != kindInsensitive && d.By != nil {
		return nil, fmt.Sprintf("a %s result takes no by", d.Kind)
	}
	switch d.Kind {
	case kindSettled:
		return Settled{}, ""
	case kindContradicted:
		return Contradicted{}, ""
	case kindOpen:
		return Open{}, ""
	case kindInsensitive:
		if d.By == nil || (*d.By != string(HiddenByWaiver) && *d.By != string(HiddenByNormalizer)) {
			return nil, "an insensitive result needs by: waiver or normalizer"
		}
		return Insensitive{By: HiddenBy(*d.By)}, ""
	}
	return nil, fmt.Sprintf("kind %q is not settled, insensitive, contradicted or open", d.Kind)
}

// Encode writes b in the canonical form: members in the format's order,
// two-space indentation, each step and claim on a line of its own, an
// empty operation and empty claims left out, a trailing newline. Encode
// refuses, as a *BaselineError, a baseline ParseBaseline would refuse.
func (b Baseline) Encode() ([]byte, error) {
	var out bytes.Buffer
	out.WriteString("{\n  \"version\": 1,\n  \"scenarios\": [")
	for i, sc := range b.Scenarios {
		if i > 0 {
			out.WriteByte(',')
		}
		if err := sc.encode(&out); err != nil {
			return nil, err
		}
	}
	if len(b.Scenarios) > 0 {
		out.WriteString("\n  ")
	}
	out.WriteString("]\n}\n")
	if _, err := ParseBaseline(out.Bytes()); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// encode writes sc as one element of the scenarios array.
func (sc BaselineScenario) encode(out *bytes.Buffer) error {
	out.WriteString("\n    {\n      \"scenario\": " + quoteJSON(sc.Scenario) + ",\n      \"steps\": [")
	lines := make([]any, 0, len(sc.Steps))
	for _, s := range sc.Steps {
		od, err := outcomeDocOf(s.Outcome)
		if err != nil {
			return &BaselineError{Reason: fmt.Sprintf("%s#%s: %v", sc.Scenario, s.Step, err)}
		}
		lines = append(lines, stepDoc{Step: s.Step, Fixture: s.Fixture, Operation: s.Operation, Outcome: &od})
	}
	if err := writeLines(out, lines); err != nil {
		return err
	}
	out.WriteString("\n      ]")
	if len(sc.Claims) > 0 {
		out.WriteString(",\n      \"claims\": [")
		lines = lines[:0]
		for _, c := range sc.Claims {
			cd, err := claimDocOf(c)
			if err != nil {
				return &BaselineError{Reason: fmt.Sprintf("%s#%s: %v", sc.Scenario, c.Step, err)}
			}
			lines = append(lines, cd)
		}
		if err := writeLines(out, lines); err != nil {
			return err
		}
		out.WriteString("\n      ]")
	}
	out.WriteString("\n    }")
	return nil
}

// writeLines writes each value as one compact line, a space after each
// colon and comma, at the depth of a scenario's steps.
func writeLines(out *bytes.Buffer, vs []any) error {
	for i, v := range vs {
		line, err := json.Marshal(v, jsontext.SpaceAfterColon(true), jsontext.SpaceAfterComma(true))
		if err != nil {
			return &BaselineError{Reason: "encoding " + err.Error()}
		}
		if i > 0 {
			out.WriteByte(',')
		}
		out.WriteString("\n        ")
		out.Write(line)
	}
	return nil
}

func outcomeDocOf(o Outcome) (outcomeDoc, error) {
	d := outcomeDoc{Kind: OutcomeName(o)}
	switch o := o.(type) {
	case Differs:
		d.Reason = &o.Reason
	case Skipped:
		r := string(o.Reason)
		d.Reason = &r
	case Match, Reconstructed, Pending:
	case nil:
		return d, errors.New("the step has no outcome")
	}
	return d, nil
}

func claimDocOf(c BaselineClaim) (claimDoc, error) {
	var t targetDoc
	switch tg := c.Target.(type) {
	case SwitchTarget:
		t = targetDoc{Kind: kindSwitch, Name: &tg.Name}
	case OverlayTarget:
		t = targetDoc{Kind: kindOverlay, Shape: &tg.Shape, Covers: tg.Covers}
	case nil:
		return claimDoc{}, fmt.Errorf("the claim on %s has no target", c.Field)
	}
	r, err := resultDocOf(c.Result)
	if err != nil {
		return claimDoc{}, err
	}
	return claimDoc{Target: &t, Step: c.Step, Field: string(c.Field), Result: &r}, nil
}

func resultDocOf(r ClaimState) (resultDoc, error) {
	switch r := r.(type) {
	case Settled:
		return resultDoc{Kind: kindSettled}, nil
	case Insensitive:
		by := string(r.By)
		return resultDoc{Kind: kindInsensitive, By: &by}, nil
	case Contradicted:
		return resultDoc{Kind: kindContradicted}, nil
	case Open:
		return resultDoc{Kind: kindOpen}, nil
	}
	return resultDoc{}, errors.New("the claim has no result")
}

// BaselineOf is the baseline the results give: their outcomes, routed
// operations and claim results without the differences, scenarios sorted
// by id, steps and claims in scenario order.
func BaselineOf(results []Result) Baseline {
	var b Baseline
	for _, r := range results {
		sc := BaselineScenario{Scenario: r.Scenario}
		for _, s := range r.Steps {
			sc.Steps = append(sc.Steps, BaselineStep{Step: s.Step, Fixture: s.Fixture, Operation: s.Operation, Outcome: s.Outcome})
		}
		for _, c := range r.Claims {
			sc.Claims = append(sc.Claims, BaselineClaim{Target: c.Claim.Target, Step: c.Claim.Step, Field: c.Claim.Field, Result: c.State})
		}
		b.Scenarios = append(b.Scenarios, sc)
	}
	slices.SortFunc(b.Scenarios, func(x, y BaselineScenario) int { return strings.Compare(x.Scenario, y.Scenario) })
	return b
}

// Drift is one key whose outcome or result differs from the baseline.
type Drift struct {
	// Key is "<scenario>#<step>", or that followed by a claim's target and
	// field.
	Key string
	// Was and Now are the outcome or result in baseline.json's compact JSON
	// form, "absent" when the key is missing on that side; a routed
	// operation that changed follows as " operation <op>".
	Was, Now string
}

// String renders the drift on one line, naming a regression from match or
// settled and an improvement to one that the baseline does not record.
func (d Drift) String() string {
	s := fmt.Sprintf("%s: baseline %s, replay %s", d.Key, d.Was, d.Now)
	switch was, now := goodText(d.Was), goodText(d.Now); {
	case was && !now:
		s += " (regression)"
	case !was && now:
		s += " (improvement not recorded)"
	}
	return s
}

// goodText reports whether a Drift side is a match or a settled claim.
func goodText(s string) bool {
	kind, _, _ := strings.Cut(s, " ")
	return kind == `{"kind":"match"}` || kind == `{"kind":"settled"}`
}

// Ratchet lists every key on which got differs from want: a step's outcome
// or routed operation, a claim's result, or a key on one side only. Keys
// follow want's order, then those only got has; nil when none differ.
func Ratchet(want, got Baseline) []Drift {
	have := map[string]ratchetEntry{}
	for _, e := range ratchetEntries(got) {
		have[e.key] = e
	}
	var drifts []Drift
	known := map[string]bool{}
	for _, w := range ratchetEntries(want) {
		known[w.key] = true
		g, ok := have[w.key]
		was, now := w.value, "absent"
		if ok {
			now = g.value
			if w.operation != g.operation {
				was, now = was+operationSuffix(w.operation), now+operationSuffix(g.operation)
			}
		}
		if was != now {
			drifts = append(drifts, Drift{Key: w.key, Was: was, Now: now})
		}
	}
	for _, g := range ratchetEntries(got) {
		if !known[g.key] {
			drifts = append(drifts, Drift{Key: g.key, Was: "absent", Now: g.value})
		}
	}
	return drifts
}

// ratchetEntry is one key of a baseline with its compared value.
type ratchetEntry struct {
	key, value, operation string
}

// ratchetEntries lists b's keys in order; a key that repeats within b
// (two claims with one target, step and field) gains " (n)".
func ratchetEntries(b Baseline) []ratchetEntry {
	var out []ratchetEntry
	count := map[string]int{}
	add := func(key, value, operation string) {
		count[key]++
		if n := count[key]; n > 1 {
			key += " (" + strconv.Itoa(n) + ")"
		}
		out = append(out, ratchetEntry{key: key, value: value, operation: operation})
	}
	for _, sc := range b.Scenarios {
		for _, s := range sc.Steps {
			add(sc.Scenario+"#"+s.Step, compactText(outcomeDocOf(s.Outcome)), s.Operation)
		}
		for _, c := range sc.Claims {
			add(sc.Scenario+"#"+c.Step+" "+targetKey(c.Target)+" "+string(c.Field), compactText(resultDocOf(c.Result)), "")
		}
	}
	return out
}

func targetKey(t Target) string {
	switch t := t.(type) {
	case SwitchTarget:
		return "switch:" + t.Name
	case OverlayTarget:
		return "overlay:" + t.Shape
	}
	return "target:none"
}

func operationSuffix(op string) string {
	if op == "" {
		return " operation none"
	}
	return " operation " + op
}

// compactText is v as compact JSON, or the error that kept it from one.
func compactText[T any](v T, err error) string {
	if err != nil {
		return "invalid (" + err.Error() + ")"
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "invalid (" + err.Error() + ")"
	}
	return string(b)
}
