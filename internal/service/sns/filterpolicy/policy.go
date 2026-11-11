package filterpolicy

import (
	"errors"
	"slices"

	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/evidence"
	"github.com/sustinbebustin/understudy/internal/fidelity"
	"github.com/sustinbebustin/understudy/internal/ruler"
)

// Scope is a subscription's FilterPolicyScope: what its policy reads.
type Scope uint8

const (
	// MessageAttributes filters on the message attributes and rejects
	// nesting. It is the scope of a policy set without one (sns.md 6).
	MessageAttributes Scope = iota
	// MessageBody filters on the message body, which must be a JSON
	// object.
	MessageBody
)

// ParseScope parses a FilterPolicyScope value; the names are
// case-sensitive.
func ParseScope(s string) (Scope, bool) {
	switch s {
	case "MessageAttributes":
		return MessageAttributes, true
	case "MessageBody":
		return MessageBody, true
	}
	return MessageAttributes, false
}

// String returns the FilterPolicyScope value.
func (s Scope) String() string {
	switch s {
	case MessageAttributes:
		return "MessageAttributes"
	case MessageBody:
		return "MessageBody"
	}
	return "unknown"
}

// profile is the Ruler profile of a scope. Its zero Options keep Ruler's
// behavior for the four conflicts with the SNS documentation (ADR 0015);
// Switches lists them.
func (s Scope) profile() ruler.SNS {
	if s == MessageBody {
		return ruler.SNS{Scope: ruler.MessageBody}
	}
	return ruler.SNS{Scope: ruler.MessageAttributes}
}

// Error is a filter policy SNS refuses. It is a value, not an error, as
// awserr.Error is: the caller renders it as InvalidParameter.
type Error struct {
	// Reason is the text after "Invalid parameter: " that
	// SetSubscriptionAttributes answers: "FilterPolicy: <detail>", or the
	// bare scope message for a nested policy under MessageAttributes.
	// Subscribe puts "Attributes Reason: " before it (sns.md 6.5).
	Reason string
}

// Policy is a compiled filter policy. It is immutable and safe for
// concurrent use.
type Policy struct {
	text    string
	scope   Scope
	pattern *ruler.Pattern
}

// Compile parses and validates a policy for scope with SNS's grammar,
// operator allow-list, limits and texts (sns.md 6.1 to 6.5).
func Compile(text string, scope Scope) (*Policy, *Error) {
	p, err := ruler.Compile([]byte(text), scope.profile())
	if err != nil {
		return nil, &Error{Reason: err.Message()}
	}
	return &Policy{text: text, scope: scope, pattern: p}, nil
}

// String returns the policy as it was given.
func (p *Policy) String() string { return p.text }

// Match reports whether a message passes the policy. The MessageAttributes
// scope reads attrs: String.Array elements are separate values, a Number
// is a JSON number and a Binary is ignored. The MessageBody scope reads
// body, and a body that is not a JSON object never matches (sns.md 6.3).
// An error is a match the engine could not decide, such as a
// *ruler.SearchBudgetError: the message neither matched nor was filtered,
// and the caller records it and does not deliver.
func (p *Policy) Match(attrs []delivery.MessageAttribute, body string) (bool, error) {
	if p.scope == MessageBody {
		matched, err := ruler.Match(p.pattern, []byte(body))
		// notObject is compared rather than discarded with "_": errcheck
		// reads a discarded *EventError as an unchecked error.
		if notObject, ok := errors.AsType[*ruler.EventError](err); ok && notObject != nil {
			return false, nil //nolint:nilerr // a body that is not a JSON object is filtered out, not a failure (sns.md 6.3)
		}
		return matched, err
	}
	in := make(map[string]ruler.Attribute, len(attrs))
	for _, a := range attrs {
		in[a.Name] = attribute(a.Value)
	}
	return ruler.MatchAttributes(p.pattern, in)
}

// attribute is the matcher's view of an attribute value: the DataType
// String.Array is a StringAttribute with the label "Array".
func attribute(v delivery.AttributeValue) ruler.Attribute {
	switch v := v.(type) {
	case delivery.StringAttribute:
		if v.Label == "Array" {
			return ruler.StringArrayAttribute{JSON: v.Value}
		}
		return ruler.StringAttribute{Value: v.Value}
	case delivery.NumberAttribute:
		return ruler.NumberAttribute{Value: v.Value}
	case delivery.BinaryAttribute:
	}
	// A Binary attribute is present, which the empty-input switch counts,
	// but its value is ignored (sns.md 6.3).
	return ruler.BinaryAttribute{}
}

// Switches lists the filter policy behaviors chosen without AWS-recorded
// evidence, each with the record-backlog item that settles it
// (matching-engines.md 3, 8 and 10), and the label, operations and
// settling step switchTable states for it.
func Switches() []fidelity.Entry {
	meta := ruler.Switches(ruler.SNS{})
	table := switchTable()
	out := make([]fidelity.Entry, 0, len(meta))
	for _, sw := range meta {
		m := table[sw.Name]
		out = append(out, fidelity.Switch[string]{
			Name: sw.Name, Value: sw.Value, Label: m.label, Ref: sw.Ref, Record: sw.Record, Ops: m.ops, Settles: m.settles,
		}.Entry())
	}
	return out
}

// switchMeta is what SNS states about one of Ruler's SNS profile switches:
// what backs its value, the operations it governs and what settles it.
type switchMeta struct {
	label   evidence.Label
	ops     []string
	settles fidelity.Settles
}

// switchTable holds the switchMeta of every SNS profile switch by name. A
// name it lacks gets no label, operations or settles, which the coverage
// checks refuse. Subscribe and SetSubscriptionAttributes compile a policy,
// so the switches that accept or refuse one govern them; Publish and
// PublishBatch match it, so the switches that decide a match govern those.
func switchTable() map[string]switchMeta {
	compile := []string{"sns.Subscribe", "sns.SetSubscriptionAttributes"}
	match := []string{"sns.Publish", "sns.PublishBatch"}
	r09 := func(steps string) fidelity.Settles {
		return fidelity.NoScenario{Reason: "recorded in sns/R09 (" + steps + "); no scenario replays sns/R09 yet"}
	}
	return map[string]switchMeta{
		"sns.filter-policy.exists-true-empty-value": {evidence.Inferred, match, r09("H2-body")},
		"sns.filter-policy.empty-input":             {evidence.Inferred, match, r09("T06, H1-attr, H1-body")},
		"sns.filter-policy.bare-number":             {evidence.Inferred, match, r09("T01 to T03, H6")},
		"sns.filter-policy.malformed-or":            {evidence.Inferred, compile, r09("H10-01-subscribe to H10-06-subscribe")},
		"sns.filter-policy.anything-but-suffix":     {evidence.Inferred, compile, r09("T07-subscribe, H7b-01-subscribe")},
		"sns.filter-policy.anything-but-list-forms": {
			evidence.Inferred, compile,
			fidelity.NoScenario{Reason: "sns/R09/H7b-06-subscribe.json records the prefix list only; the suffix and wildcard lists are unrecorded"},
		},
		"sns.filter-policy.ignore-case-forms": {evidence.Inferred, compile, r09("H7b-03-subscribe to H7b-05-subscribe")},
		"sns.filter-policy.key-counting":      {evidence.Inferred, compile, r09("T12-subscribe, H3-c-subscribe, H3-d-subscribe")},
		"sns.filter-policy.wildcard-points":   {evidence.Inferred, compile, r09("H11-10-single-subscribe and the other H11 subscribes")},
		"sns.filter-policy.limit-order": {
			evidence.Inferred, compile,
			fidelity.NoScenario{Reason: "unrecorded: sns/R09/T13-subscribe.json breaks one limit only, so it shows no check order"},
		},
		"sns.filter-policy.number-attribute-type": {evidence.Inferred, match, r09("T19a, T19b")},
		"sns.filter-policy.numeric-range":         {evidence.Inferred, slices.Concat(compile, match), r09("T15, H5")},
		"sns.filter-policy.anything-but-arrays":   {evidence.Inferred, match, r09("T08, H4")},
		"sns.filter-policy.array-consistency":     {evidence.Inferred, match, r09("T14")},
	}
}
