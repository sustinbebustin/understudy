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
	switchPrefix() string
	switchRecords() switchRecords
	// fixedSwitches describes the allow-list and limit choices the profile
	// makes without AWS-recorded evidence.
	fixedSwitches() []Switch
	allows(op Operator) bool
	// admit checks the raw pattern before Ruler's compiler sees it.
	admit(src []byte) *CompileError
	// overrun returns a limit the pattern is already certain to exceed, judged
	// by counts that only grow while it is read, or nil.
	overrun(x expansion) *CompileError
	// check applies the service's limits to a compiled pattern.
	check(c *compiler) *CompileError
	message(e *CompileError) string
}

// EventBridge is an event bus rule's event pattern. Every Ruler operator is
// allowed except the undocumented "exactly"; the limits are the 1000 $or
// combination cap and Ruler's wildcard complexity evaluator.
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

// SNS is a subscription filter policy. The allow-list leaves out the
// equals-ignore-case forms of prefix, suffix and anything-but, which the SNS
// documentation does not list; the limits are 5 keys, 150 combinations, the
// wildcard point budget and 256 KB.
type SNS struct {
	Scope   SNSScope
	Options Options
}

// LambdaESM is an event source mapping filter pattern. The allow-list is the
// operator table of the Lambda filtering documentation; every rejection is
// reported with Lambda's generic message.
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
func (p SNS) options() Options         { return p.Options }
func (p LambdaESM) options() Options   { return p.Options }

func (EventBridge) input() Input { return InputEvent }
func (LambdaESM) input() Input   { return InputEvent }

func (p SNS) input() Input {
	if p.Scope == MessageAttributes {
		return InputAttributes
	}
	return InputEvent
}

func (EventBridge) switchPrefix() string { return "events.pattern" }
func (SNS) switchPrefix() string         { return "sns.filter-policy" }
func (LambdaESM) switchPrefix() string   { return "lambda.esm-filter" }

// switchRecords follows matching-engines.md section 8: H1 covers SNS and
// EventBridge, H2 SNS and Lambda, H6 SNS bare numbers, H7(a) Lambda bare
// numbers, and H10 every service. The record backlog's R09 items 6 and 1 ask
// SNS the H1 and H6 questions. EventBridge's exists:true and bare-number
// behaviors have no item: the research rates them from Ruler, which
// EventBridge runs.
func (EventBridge) switchRecords() switchRecords {
	return switchRecords{emptyInput: "matching-engines.md H1", malformedOr: "matching-engines.md H10"}
}

func (SNS) switchRecords() switchRecords {
	return switchRecords{
		existsTrue: "matching-engines.md H2", emptyInput: "R09/6 (matching-engines.md H1)",
		bareNumber: "R09/1 (matching-engines.md H6)", malformedOr: "matching-engines.md H10",
	}
}

func (LambdaESM) switchRecords() switchRecords {
	return switchRecords{
		existsTrue: "matching-engines.md H2", bareNumber: "matching-engines.md H7",
		malformedOr: "matching-engines.md H10",
	}
}

const (
	humanRecordsRef = "docs/research/phase1/matching-engines.md#8"
	// operatorsRef is the documented operator table, which lists "exactly"
	// for no service although Ruler accepts it.
	operatorsRef = "docs/research/phase1/matching-engines.md#5.1"
)

// exactlyRejected is the switch for Ruler's "exactly" operator, which every
// profile rejects as an unknown match type.
func exactlyRejected(prefix, record string) Switch {
	return Switch{Name: prefix + ".exactly-operator", Value: "rejected", Ref: operatorsRef, Record: record}
}

func (p EventBridge) fixedSwitches() []Switch {
	return []Switch{
		exactlyRejected(p.switchPrefix(), "matching-engines.md H14"),
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

func (p SNS) fixedSwitches() []Switch {
	return []Switch{
		exactlyRejected(p.switchPrefix(), "matching-engines.md H7"),
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
			Name: "sns.filter-policy.ignore-case-forms", Value: "rejected",
			Ref: humanRecordsRef, Record: "matching-engines.md H7",
		},
		{
			Name: "sns.filter-policy.key-counting", Value: "leaf-keys-per-branch",
			Ref: humanRecordsRef, Record: "R09/11, R09/12 (matching-engines.md H3)",
		},
		{
			Name: "sns.filter-policy.wildcard-points", Value: "3-per-star",
			Ref: humanRecordsRef, Record: "matching-engines.md H11",
		},
		{
			Name: "sns.filter-policy.limit-order", Value: "nesting-keys-combinations-wildcards",
			Ref: "docs/research/phase1/sns.md#6", Record: "R09/13",
		},
		{
			Name: "sns.filter-policy.number-attribute-type", Value: "json-number",
			Ref: "docs/research/phase1/matching-engines.md#5.4", Record: "R09/19 (matching-engines.md H6)",
		},
		{
			// The SNS documentation says -1e9..1e9 with 5 decimals; Ruler's
			// binary64 model is used until a recording settles it.
			Name: "sns.filter-policy.numeric-range", Value: "binary64",
			Ref: "docs/research/phase1/matching-engines.md#5.2", Record: "R09/15 (matching-engines.md H5)",
		},
		{
			Name: "sns.filter-policy.anything-but-arrays", Value: "any-element",
			Ref: "docs/research/phase1/matching-engines.md#4.3", Record: "R09/8 (matching-engines.md H4)",
		},
		{
			Name: "sns.filter-policy.array-consistency", Value: "enforced",
			Ref: "docs/research/phase1/matching-engines.md#4.3", Record: "R09/14",
		},
	}
}

func (p LambdaESM) fixedSwitches() []Switch {
	return []Switch{
		exactlyRejected(p.switchPrefix(), "matching-engines.md H13"),
		{
			Name: "lambda.esm-filter.operator-allow-list", Value: "documented-table",
			Ref: humanRecordsRef, Record: "matching-engines.md H13",
		},
	}
}

func (EventBridge) allows(Operator) bool { return true }

func (SNS) allows(op Operator) bool {
	switch op {
	case OperatorPrefixEqualsIgnoreCase, OperatorSuffixEqualsIgnoreCase, OperatorAnythingButEqualsIgnoreCase:
		return false
	case OperatorPrefix, OperatorSuffix, OperatorEqualsIgnoreCase, OperatorWildcard, OperatorAnythingBut,
		OperatorAnythingButPrefix, OperatorAnythingButSuffix, OperatorAnythingButWildcard, OperatorNumeric,
		OperatorExists, OperatorCIDR, OperatorOr:
		return true
	}
	return false
}

func (LambdaESM) allows(op Operator) bool {
	switch op {
	case OperatorPrefix, OperatorSuffix, OperatorEqualsIgnoreCase, OperatorAnythingBut, OperatorNumeric,
		OperatorExists, OperatorOr:
		return true
	case OperatorPrefixEqualsIgnoreCase, OperatorSuffixEqualsIgnoreCase, OperatorWildcard,
		OperatorAnythingButPrefix, OperatorAnythingButSuffix, OperatorAnythingButEqualsIgnoreCase,
		OperatorAnythingButWildcard, OperatorCIDR:
		return false
	}
	return false
}

func (EventBridge) admit([]byte) *CompileError { return nil }
func (LambdaESM) admit([]byte) *CompileError   { return nil }

// admit applies the size limit, then parses the whole policy as JSON first,
// as SNS does: its syntax errors carry a "failed to parse JSON. " prefix and
// win over Ruler's grammar errors anywhere in the policy.
func (SNS) admit(src []byte) *CompileError {
	if len(src) > snsMaxBytes {
		return limitError(LimitSize, "Filter policy can not be larger than "+strconv.Itoa(snsMaxBytes/1024)+" KB")
	}
	lx := newLexer(src)
	for {
		t, err := lx.next()
		if err != nil {
			return &CompileError{Cause: InvalidJSON{}, Detail: "failed to parse JSON. " + err.message}
		}
		if t.kind == tokEOF {
			return nil
		}
	}
}

// expansion is what a profile's limits can see of a pattern while it is
// read. Each count only grows: branches are never dropped, and a branch's
// keys are never removed (a duplicate key replaces a rule array, not a key).
type expansion struct {
	// branches is the size of one partial expansion of "$or"; the final
	// expansion holds at least as many.
	branches int
	// keys is the key count of one branch.
	keys int
	// orProduct is the product of the sizes of the "$or" arrays read so far.
	orProduct int
}

// overrun: every branch holds at least one "$or" combination of every array
// read, so more than 1000 branches is more than 1000 combinations.
func (EventBridge) overrun(x expansion) *CompileError {
	if x.orProduct > ebMaxOrCombinations || x.branches > ebMaxOrCombinations {
		return limitError(LimitCombinations, "The pattern has more than "+strconv.Itoa(ebMaxOrCombinations)+" $or combinations")
	}
	return nil
}

// overrun: every branch contributes at least one combination, so more than
// 150 branches is more than 150 combinations. Keys come before combinations
// in check, so a key overrun is reported as check would. A combination
// overrun stops the expansion, so a branch that reaches six keys only after
// the policy passes 150 branches is reported as too complex, not as having
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
	for _, b := range c.branches {
		if len(b) > snsMaxKeys {
			return snsKeysError()
		}
	}
	if snsCombinations(c.branches) > snsMaxCombinations {
		return limitError(LimitCombinations, snsTooComplex)
	}
	points := 0
	for _, cd := range c.liveConditions() {
		for _, g := range cd.globs() {
			if g.stars() > snsMaxWildcardsPerGlob {
				return limitError(LimitWildcardsPerPattern, snsTooComplex)
			}
		}
		points += cd.snsWildcardPoints()
	}
	if points > snsMaxWildcardPoints {
		return limitError(LimitWildcardComplexity, snsTooComplex)
	}
	return nil
}

func (LambdaESM) check(c *compiler) *CompileError { return c.overrun }

func (EventBridge) message(e *CompileError) string {
	return "Event pattern is not valid. Reason: " + e.Detail
}

func (SNS) message(e *CompileError) string {
	if c, ok := e.Cause.(LimitExceeded); ok && c.Limit == LimitNesting {
		return e.Detail
	}
	return "FilterPolicy: " + e.Detail
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
