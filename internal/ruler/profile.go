package ruler

import "strconv"

// Profile is the service a pattern is compiled for. It fixes the operator
// allow-list (matching-engines.md 5.1), the limits (5.2 and sns.md 6.5), the
// error wording (5.3), the input the pattern matches and the Options. Ruler
// itself has no allow-list and no limits, so the core grammar and semantics
// are the same for every profile.
//
//sumtype:decl
type Profile interface {
	options() Options
	input() Input
	// optionSwitches describes the Options fields the profile leaves open.
	optionSwitches() []Switch
	// fixedSwitches describes the allow-list and limit choices the profile
	// names as fidelity switches.
	fixedSwitches() []Switch
	allows(op Operator) bool
	// admit checks the raw pattern before Ruler's compiler sees it, and
	// returns the text the service hands to Ruler.
	admit(src []byte) ([]byte, *CompileError)
	// overrun returns a limit the pattern is already certain to exceed, judged
	// by counts that only grow while it is read, or nil.
	overrun(x expansion) *CompileError
	// check applies the service's limits to a compiled pattern.
	check(c *compiler) *CompileError
	message(e *CompileError) string
}

// EventBridge is an event bus rule's event pattern. Every Ruler operator is
// allowed; the limits are the 1000 $or combination cap and Ruler's wildcard
// complexity evaluator.
type EventBridge struct {
	Options Options
}

// SNSScope is a subscription's FilterPolicyScope.
type SNSScope uint8

const (
	// MessageAttributes filters on message attributes; nesting is rejected.
	// It is the scope SNS assumes when a policy has none.
	MessageAttributes SNSScope = iota
	// MessageBody filters on the JSON message body.
	MessageBody
)

// SNS is a subscription filter policy. The allow-list is every Ruler
// operator but none of the documentation leaves out: the equals-ignore-case
// forms of prefix, suffix and anything-but (sns/R09/H7b-03-subscribe.json to
// H7b-05-subscribe.json) and "exactly" (sns/R09/H7b-07-subscribe.json) are
// accepted. The limits are 5 keys, 150 combinations, the wildcard point
// budget and 256 KB. Empty input and exists:true follow the R09 recordings
// per scope, so only the other two Options fields are open.
type SNS struct {
	Scope   SNSScope
	Options SNSOptions
}

// SNSOptions are the Options fields the SNS profile leaves open.
type SNSOptions struct {
	BareNumber  BareNumber
	MalformedOr MalformedOr
}

// LambdaESM is an event source mapping filter pattern. The allow-list is the
// operator table of the Lambda filtering documentation plus the undocumented
// operators Lambda accepts: cidr, wildcard, anything-but with prefix or
// suffix, prefix with equals-ignore-case (lambda/R03/H13-01-create.json to
// H13-06-create.json) and "exactly" (H13-11-create.json). The three forms
// neither the table nor a recording covers stay refused. Every rejection is
// reported with Lambda's generic message. A string rule also matches a
// number with its text, and a wildcard rule matches nothing (lambda/R03 H8-02,
// H13-02); both are fixed, not Options.
type LambdaESM struct {
	Options Options
}

// maxExpansion bounds the work of expanding "$or" for every profile: the
// conditions, segments and branch entries written while a pattern is read.
// It bounds Lambda, which has no combination limit, and has no AWS
// counterpart. EventBridge's 1000 branches, each holding every key that fits
// in a 4096-character pattern, need under 400000; SNS stops at its own
// limits first unless a policy repeats one key many thousand times.
const maxExpansion = 1 << 19

const (
	ebMaxOrCombinations     = 1000
	ebWildcardComplexityCap = 11
	snsMaxBytes             = 256 * 1024
	snsMaxKeys              = 5
	snsMaxCombinations      = 150
	snsMaxWildcardPoints    = 100
	snsMaxWildcardsPerGlob  = 3
)

func (p EventBridge) options() Options { return p.Options }

// options: logs/R03/H8-02-01.json and H13-02-01.json.
func (p LambdaESM) options() Options {
	o := p.Options
	o.stringRule, o.wildcardRule = stringRuleNumberText, wildcardRuleNothing
	return o
}

// options: under MessageAttributes scope a message without attributes never
// matches and an empty String value does not exist (sns/R09 T06, H1-attr,
// H2-attr); under MessageBody scope {} can match and only null fails
// exists:true (H1-body, H2-body).
func (p SNS) options() Options {
	o := Options{BareNumber: p.Options.BareNumber, MalformedOr: p.Options.MalformedOr}
	switch p.Scope {
	case MessageAttributes:
		o.ExistsTrue, o.EmptyInput = ExistsTrueNonEmpty, EmptyInputNeverMatches
	case MessageBody:
		o.ExistsTrue, o.EmptyInput = ExistsTrueNonNull, EmptyInputMatchable
	}
	return o
}

func (EventBridge) input() Input { return InputEvent }
func (LambdaESM) input() Input   { return InputEvent }

func (p SNS) input() Input {
	if p.Scope == MessageAttributes {
		return InputAttributes
	}
	return InputEvent
}

// The option switch records follow matching-engines.md section 8: H1 covers
// SNS and EventBridge, H2 SNS and Lambda, H6 SNS bare numbers, H7(a) Lambda
// bare numbers, and H10 every service. The record backlog's R09 item 1 asks
// SNS the H6 question. EventBridge's exists:true and bare-number behaviors
// have no item: the research rates them from Ruler, which EventBridge runs.
func (p EventBridge) optionSwitches() []Switch {
	const prefix = "events.pattern"
	return []Switch{
		optionSwitch(prefix, "exists-true-empty-value", p.Options.ExistsTrue, ""),
		optionSwitch(prefix, "empty-input", p.Options.EmptyInput, "matching-engines.md H1"),
		optionSwitch(prefix, "bare-number", p.Options.BareNumber, ""),
		optionSwitch(prefix, "malformed-or", p.Options.MalformedOr, "events/R14 H10 (matching-engines.md H10)"),
	}
}

func (p SNS) optionSwitches() []Switch {
	const prefix = "sns.filter-policy"
	return []Switch{
		optionSwitch(prefix, "bare-number", p.Options.BareNumber, "R09/1 (matching-engines.md H6)"),
		optionSwitch(prefix, "malformed-or", p.Options.MalformedOr, "matching-engines.md H10"),
	}
}

func (p LambdaESM) optionSwitches() []Switch {
	const prefix = "lambda.esm-filter"
	return []Switch{
		optionSwitch(prefix, "exists-true-empty-value", p.Options.ExistsTrue, "matching-engines.md H2"),
		optionSwitch(prefix, "empty-input", p.Options.EmptyInput, ""),
		optionSwitch(prefix, "bare-number", p.Options.BareNumber, "matching-engines.md H7"),
		optionSwitch(prefix, "malformed-or", p.Options.MalformedOr, "matching-engines.md H10"),
	}
}

const (
	humanRecordsRef = "docs/research/matching-engines.md#8"
	// operatorsRef is the documented operator table.
	operatorsRef = "docs/research/matching-engines.md#5.1"
)

func (EventBridge) fixedSwitches() []Switch {
	return []Switch{
		{
			// Ruler loses a later "$or" member's values that an earlier
			// member with the same key shares, so its answer depends on
			// member order; this package keeps the documented MATCH.
			Name: "events.pattern.or-shared-values", Value: "documented",
			Ref: humanRecordsRef, Record: "matching-engines.md H14",
		},
		{
			Name: "events.pattern.wildcard-complexity-cap", Value: strconv.Itoa(ebWildcardComplexityCap),
			Ref: humanRecordsRef, Record: "matching-engines.md H11",
		},
		{
			Name: "events.pattern.or-combination-counting", Value: "product-including-nested",
			Ref: humanRecordsRef, Record: "matching-engines.md H12",
		},
	}
}

func (SNS) fixedSwitches() []Switch {
	return []Switch{
		{
			Name: "sns.filter-policy.anything-but-suffix", Value: "accepted",
			Ref: humanRecordsRef, Record: "R09/7 (matching-engines.md H7)",
		},
		{
			// {"anything-but":{"prefix":["a","b"]}} and the suffix and
			// wildcard lists, which the SNS documentation shows only with one
			// string.
			Name: "sns.filter-policy.anything-but-list-forms", Value: "accepted",
			Ref: operatorsRef, Record: "matching-engines.md H7",
		},
		{
			Name: "sns.filter-policy.wildcard-points", Value: "3-per-star",
			Ref: humanRecordsRef, Record: "matching-engines.md H11",
		},
		{
			Name: "sns.filter-policy.limit-order", Value: "nesting-keys-combinations-wildcards",
			Ref: "docs/research/sns.md#6", Record: "R09/13",
		},
		{
			Name: "sns.filter-policy.number-attribute-type", Value: "json-number",
			Ref: "docs/research/matching-engines.md#5.4", Record: "R09/19 (matching-engines.md H6)",
		},
		{
			// The SNS documentation says -1e9..1e9 with 5 decimals; Ruler's
			// binary64 model is used until a recording settles it.
			Name: "sns.filter-policy.numeric-range", Value: "binary64",
			Ref: "docs/research/matching-engines.md#5.2", Record: "R09/15 (matching-engines.md H5)",
		},
		{
			Name: "sns.filter-policy.anything-but-arrays", Value: "any-element",
			Ref: "docs/research/matching-engines.md#4.3", Record: "R09/8 (matching-engines.md H4)",
		},
		{
			Name: "sns.filter-policy.array-consistency", Value: "enforced",
			Ref: "docs/research/matching-engines.md#4.3", Record: "R09/14",
		},
	}
}

func (LambdaESM) fixedSwitches() []Switch { return nil }

func (EventBridge) allows(Operator) bool { return true }

func (SNS) allows(Operator) bool { return true }

func (LambdaESM) allows(op Operator) bool {
	switch op {
	case OperatorPrefix, OperatorSuffix, OperatorEqualsIgnoreCase, OperatorAnythingBut, OperatorNumeric,
		OperatorExists, OperatorExactly, OperatorOr, OperatorCIDR, OperatorWildcard, OperatorAnythingButPrefix,
		OperatorAnythingButSuffix, OperatorPrefixEqualsIgnoreCase:
		return true
	case OperatorSuffixEqualsIgnoreCase, OperatorAnythingButEqualsIgnoreCase, OperatorAnythingButWildcard:
		return false
	}
	return false
}

func (EventBridge) admit(src []byte) ([]byte, *CompileError) { return src, nil }
func (LambdaESM) admit(src []byte) ([]byte, *CompileError)   { return src, nil }

// admit applies the size limit in UTF-8 bytes (sns/R09/T16-subscribe.json),
// then parses the whole policy as JSON first, as SNS does: its syntax errors
// carry a "failed to parse JSON. " prefix and win over Ruler's grammar
// errors anywhere in the policy. Ruler reads the policy as SNS writes the
// parsed tree back (reserialize). The location line of a syntax error,
// which names SNS's own parser and not Ruler's, is left out: no recording
// shows it whole.
func (SNS) admit(src []byte) ([]byte, *CompileError) {
	if len(src) > snsMaxBytes {
		return nil, limitError(LimitSize, "must be at most "+strconv.Itoa(snsMaxBytes)+" bytes long in UTF-8 encoding")
	}
	if message, ok := SyntaxError(src); ok {
		return nil, &CompileError{Cause: InvalidJSON{}, Detail: "failed to parse JSON. " + message}
	}
	return reserialize(src), nil
}

// SyntaxError is the first JSON syntax error in src, worded as Jackson
// words it without its location line, and false when src is a sequence of
// well-formed JSON values. SNS appends it to "failed to parse JSON. " for
// any JSON attribute it parses, a filter policy or a RedrivePolicy (sns.md
// 3.3, 6.5).
func SyntaxError(src []byte) (string, bool) {
	lx := newLexer(src)
	for {
		t, err := lx.next()
		if err != nil {
			return err.message, true
		}
		if t.kind == tokEOF {
			return "", false
		}
	}
}

// expansion is what a profile's limits can see of a pattern while it is
// read. Each count only grows: branches are never dropped, and keys are
// never removed (a duplicate key replaces a rule array, not a key).
type expansion struct {
	// branches is the size of one partial expansion of "$or"; the final
	// expansion holds at least as many.
	branches int
	// keys is the count of distinct key paths read, across every branch.
	keys int
	// orProduct is the product of the sizes of the "$or" arrays read so far.
	orProduct int
}

// overrun: every branch holds at least one "$or" combination of every array
// read, so more than 1000 branches is more than 1000 combinations.
func (EventBridge) overrun(x expansion) *CompileError {
	if x.orProduct > ebMaxOrCombinations || x.branches > ebMaxOrCombinations {
		return limitError(LimitCombinations, "Or operator is producing over "+strconv.Itoa(ebMaxOrCombinations)+" rule combinations")
	}
	return nil
}

// overrun: every branch contributes at least one combination, so more than
// 150 branches is more than 150 combinations. Keys come before combinations
// in check, so a key overrun is reported as check would. A combination
// overrun comes first when the policy passes 150 branches before its sixth
// key is read, so such a policy is reported as too complex, not as having
// too many keys.
func (SNS) overrun(x expansion) *CompileError {
	if x.keys > snsMaxKeys {
		return snsKeysError()
	}
	if x.branches > snsMaxCombinations {
		return limitError(LimitCombinations, snsTooComplex)
	}
	return nil
}

func (LambdaESM) overrun(expansion) *CompileError { return nil }

func (EventBridge) check(c *compiler) *CompileError {
	if c.overrun != nil {
		return c.overrun
	}
	for _, gs := range c.machineGlobs() {
		if wildcardComplexity(gs, ebWildcardComplexityCap) >= ebWildcardComplexityCap {
			return limitError(LimitWildcardComplexity, "Rule is too complex - try using fewer wildcard characters "+
				"or fewer repeating character sequences after a wildcard character")
		}
	}
	return nil
}

const snsTooComplex = "Filter policy is too complex"

func snsKeysError() *CompileError {
	return limitError(LimitKeys, "Filter policy can not have more than "+strconv.Itoa(snsMaxKeys)+" keys")
}

// check applies the SNS limits in this order: nesting under
// MessageAttributes scope, keys, combinations, then wildcards. The relative
// order of the last three is not recorded (sns.md 6.5).
func (p SNS) check(c *compiler) *CompileError {
	if p.Scope == MessageAttributes && c.maxDepth > 1 {
		return limitError(LimitNesting, "Filter policy scope MessageAttributes does not support nested filter policy")
	}
	if c.overrun != nil {
		return c.overrun
	}
	if len(c.paths) > snsMaxKeys {
		return snsKeysError()
	}
	if snsCombinations(c.branches) > snsMaxCombinations {
		return limitError(LimitCombinations, snsTooComplex)
	}
	points := 0
	for _, cd := range c.liveConditions() {
		for _, g := range cd.globs() {
			if g.stars() > snsMaxWildcardsPerGlob {
				return limitError(LimitWildcardsPerPattern, snsWildcardComplexity+": pattern exceeds maximum allowed wildcards")
			}
		}
		points += cd.snsWildcardPoints()
	}
	if points > snsMaxWildcardPoints {
		return limitError(LimitWildcardComplexity, snsWildcardComplexity)
	}
	return nil
}

// snsWildcardComplexity is the SNS text for the wildcard limits, which SNS
// puts after "Attributes Reason: " without "FilterPolicy: " (sns/R09
// H11-11-single, H11-5-two-wildcard; H11-4-wildcards and T13 add the
// per-pattern suffix).
const snsWildcardComplexity = "Wildcard complexity too high"

func (LambdaESM) check(c *compiler) *CompileError { return c.overrun }

func (EventBridge) message(e *CompileError) string {
	return "Event pattern is not valid. Reason: " + e.reason()
}

func (SNS) message(e *CompileError) string {
	if c, ok := e.Cause.(LimitExceeded); ok {
		switch c.Limit {
		case LimitNesting, LimitWildcardComplexity, LimitWildcardsPerPattern:
			return e.Detail
		case LimitSize, LimitKeys, LimitCombinations:
		}
	}
	return "FilterPolicy: " + e.reason()
}

func (LambdaESM) message(*CompileError) string { return "Invalid filter pattern definition." }

// snsCombinations is the SNS complexity: summed over the "$or" branches, the
// product over each branch's keys of the key's value count times its nesting
// level (sns.md 6.5). It saturates just past the limit.
func snsCombinations(branches [][]*condition) int {
	total := 0
	for _, b := range branches {
		product := 1
		for _, cd := range b {
			product = min(product*len(cd.matchers)*cd.depth, snsMaxCombinations+1)
		}
		total = min(total+product, snsMaxCombinations+1)
	}
	return total
}

func limitError(l Limit, detail string) *CompileError {
	return &CompileError{Cause: LimitExceeded{Limit: l}, Detail: detail}
}
