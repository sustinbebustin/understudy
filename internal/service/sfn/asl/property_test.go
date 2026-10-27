package asl_test

import (
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"testing"
	"unicode/utf16"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
	"github.com/sustinbebustin/understudy/internal/service/sfn/asl"
)

// machine draws a definition whose states only move forward, so every
// execution ends: Pass, Wait, Choice, Parallel and inline Map states, the
// last a Succeed, Fail or ending Pass. Parallel and Map keep the input
// (ResultPath null) so later Choices still read $.k.
func machine(t *rapid.T) string {
	n := rapid.IntRange(1, 6).Draw(t, "states")
	name := func(i int) string { return fmt.Sprintf("S%d", i) }
	next := func(i int, label string) string {
		return name(rapid.IntRange(i+1, n-1).Draw(t, label))
	}
	states := make([]string, 0, n)
	for i := range n - 1 {
		var s string
		switch rapid.IntRange(0, 4).Draw(t, fmt.Sprintf("kind%d", i)) {
		case 0:
			s = fmt.Sprintf(`{"Type":"Pass","Next":%q}`, next(i, fmt.Sprintf("next%d", i)))
		case 1:
			s = fmt.Sprintf(`{"Type":"Wait","Seconds":%d,"Next":%q}`, rapid.IntRange(0, 3).Draw(t, fmt.Sprintf("wait%d", i)), next(i, fmt.Sprintf("next%d", i)))
		case 2:
			s = fmt.Sprintf(`{"Type":"Choice","Choices":[{"Variable":"$.k","NumericEquals":%d,"Next":%q}],"Default":%q}`,
				rapid.IntRange(0, 2).Draw(t, fmt.Sprintf("eq%d", i)), next(i, fmt.Sprintf("yes%d", i)), next(i, fmt.Sprintf("no%d", i)))
		case 3:
			nb := rapid.IntRange(1, 3).Draw(t, fmt.Sprintf("branches%d", i))
			branches := make([]string, nb)
			for b := range nb {
				branches[b] = fmt.Sprintf(`{"StartAt":"B%d_%d","States":{"B%d_%d":{"Type":"Pass","End":true}}}`, i, b, i, b)
			}
			s = fmt.Sprintf(`{"Type":"Parallel","ResultPath":null,"Branches":[%s],"Next":%q}`, strings.Join(branches, ","), next(i, fmt.Sprintf("next%d", i)))
		default:
			s = fmt.Sprintf(`{"Type":"Map","ItemsPath":"$.items","ResultPath":null,"MaxConcurrency":%d,"ItemProcessor":{"StartAt":"M%d","States":{"M%d":{"Type":"Pass","End":true}}},"Next":%q}`,
				rapid.IntRange(0, 2).Draw(t, fmt.Sprintf("conc%d", i)), i, i, next(i, fmt.Sprintf("next%d", i)))
		}
		states = append(states, fmt.Sprintf("%q:%s", name(i), s))
	}
	last := rapid.SampledFrom([]string{
		`{"Type":"Succeed"}`, `{"Type":"Fail","Error":"E","Cause":"C"}`, `{"Type":"Pass","End":true}`,
	}).Draw(t, "last")
	states = append(states, fmt.Sprintf("%q:%s", name(n-1), last))
	return `{"StartAt":"S0","States":{` + strings.Join(states, ",") + `}}`
}

func machineInput(t *rapid.T) string {
	items := make([]string, rapid.IntRange(0, 3).Draw(t, "items"))
	for i := range items {
		items[i] = strconv.Itoa(i)
	}
	return fmt.Sprintf(`{"k":%d,"items":[%s]}`, rapid.IntRange(0, 2).Draw(t, "k"), strings.Join(items, ","))
}

var terminalTypes = map[string]bool{
	"ExecutionSucceeded": true, "ExecutionFailed": true, "ExecutionAborted": true, "ExecutionTimedOut": true,
}

// fingerprint renders a history with everything a client sees.
func fingerprint(h []asl.HistoryEvent) string {
	var b strings.Builder
	for _, e := range h {
		fmt.Fprintf(&b, "%d<-%d %s %s %s\n", e.ID, e.PreviousEventID, e.Type, e.Timestamp.Format("2006-01-02T15:04:05.000000000Z07:00"), encode(e.Details))
	}
	return b.String()
}

// Spec T8: over generated definitions the history ids run from 1, every
// previousEventId is smaller than its id, ExecutionStarted is first and
// one terminal event is last (stepfunctions-asl.md 11.1: ids "numbered
// sequentially, starting at one"; 11.2 rule 1). Step is deterministic.
func TestHistoryProperties(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		def, input := machine(t), machineInput(t)
		d := newDriver(t, def, rules())
		d.start(input)
		d.runToEnd(10_000)
		h := d.history
		if len(h) < 2 || h[0].Type != "ExecutionStarted" || h[0].ID != 1 || h[0].PreviousEventID != 0 {
			t.Fatalf("history of %s does not open with ExecutionStarted 1<-0:\n%s", def, shape(h))
		}
		terminals := 0
		for i, e := range h {
			if e.ID != int64(i+1) {
				t.Fatalf("event %d has id %d, want ids from 1 in order:\n%s", i, e.ID, shape(h))
			}
			if e.PreviousEventID < 0 || e.PreviousEventID >= e.ID {
				t.Fatalf("event %d points at %d:\n%s", e.ID, e.PreviousEventID, shape(h))
			}
			if terminalTypes[e.Type] {
				terminals++
			}
		}
		if terminals != 1 || !terminalTypes[h[len(h)-1].Type] {
			t.Fatalf("history of %s has %d terminal events, want one, last:\n%s", def, terminals, shape(h))
		}

		again := newDriver(t, def, rules())
		again.start(input)
		again.runToEnd(10_000)
		if fingerprint(again.history) != fingerprint(h) {
			t.Fatalf("Step is not deterministic for %s:\n%s\nthen\n%s", def, fingerprint(h), fingerprint(again.history))
		}
	})
}

// A jsonx round trip of the execution at any step leaves the rest of the
// run unchanged (contract 5.4, ADR 0009).
func TestPersistenceAtAnyStep(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		def, input := machine(t), machineInput(t)
		whole := newDriver(t, def, rules())
		whole.start(input)
		whole.runToEnd(10_000)

		cut := rapid.IntRange(0, 40).Draw(t, "cut")
		d := newDriver(t, def, rules())
		d.start(input)
		for range cut {
			if !d.next() {
				break
			}
		}
		raw, err := d.x.MarshalJSON()
		if err != nil {
			t.Fatalf("MarshalJSON: %v", err)
		}
		var loaded asl.Execution
		if err := loaded.UnmarshalJSON(raw); err != nil {
			t.Fatalf("UnmarshalJSON(%s): %v", raw, err)
		}
		d.x = loaded
		d.runToEnd(10_000)
		if fingerprint(d.history) != fingerprint(whole.history) {
			t.Fatalf("history after a reload at step %d of %s:\n%s\nwant\n%s", cut, def, fingerprint(d.history), fingerprint(whole.history))
		}
	})
}

// javaCompare compares strings by UTF-16 code unit, as Java compareTo does
// (stepfunctions-asl.md 6.2: "strings compare by character with no folding
// or normalisation, like Java compareTo").
func javaCompare(a, b string) int {
	x, y := utf16.Encode([]rune(a)), utf16.Encode([]rune(b))
	for i := range min(len(x), len(y)) {
		if x[i] != y[i] {
			if x[i] < y[i] {
				return -1
			}
			return 1
		}
	}
	return len(x) - len(y)
}

// holds reports whether cmp (-1, 0, 1 style) satisfies the operator suffix.
func holds(op string, cmp int) bool {
	switch {
	case strings.HasSuffix(op, "LessThanEquals"):
		return cmp <= 0
	case strings.HasSuffix(op, "GreaterThanEquals"):
		return cmp >= 0
	case strings.HasSuffix(op, "LessThan"):
		return cmp < 0
	case strings.HasSuffix(op, "GreaterThan"):
		return cmp > 0
	}
	return cmp == 0
}

func decimal(t *rapid.T, label string) (string, *big.Rat) {
	h := rapid.IntRange(-2000, 2000).Draw(t, label)
	r := big.NewRat(int64(h), 100)
	text := r.FloatString(2)
	return text, r
}

var (
	numericOps = []string{"NumericEquals", "NumericLessThan", "NumericGreaterThan", "NumericLessThanEquals", "NumericGreaterThanEquals"}
	stringOps  = []string{"StringEquals", "StringLessThan", "StringGreaterThan", "StringLessThanEquals", "StringGreaterThanEquals"}
)

// Choice comparison semantics over generated operands: numbers compare by
// value, strings by UTF-16 code unit, booleans by equality, and an operand
// of another type is false, never an error (spec
// #data-test-expression-for-jsonpath-choice-rules; stepfunctions-asl.md 6.2,
// recorded for NumericEquals and BooleanEquals in
// localstack@8b9a79f v2/choice_operators/test_numeric.snapshot.json:L2 and
// test_boolean_equals.snapshot.json:L2). The Path forms read the operand
// from the input.
func TestChoiceComparisonProperty(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		var op, operand, value string
		var match bool
		switch rapid.IntRange(0, 2).Draw(t, "family") {
		case 0:
			op = rapid.SampledFrom(numericOps).Draw(t, "op")
			var a, b *big.Rat
			value, a = decimal(t, "value")
			operand, b = decimal(t, "operand")
			match = holds(op, a.Cmp(b))
		case 1:
			op = rapid.SampledFrom(stringOps).Draw(t, "op")
			alphabet := rapid.SampledFrom([]rune("aAb \t\u00e9\uffe0\U0001F600"))
			a := rapid.StringOfN(alphabet, 0, 4, -1).Draw(t, "value")
			b := rapid.StringOfN(alphabet, 0, 4, -1).Draw(t, "operand")
			value, operand = encode(document.String(a)), encode(document.String(b))
			match = holds(op, javaCompare(a, b))
		default:
			op = "BooleanEquals"
			a, b := rapid.Bool().Draw(t, "value"), rapid.Bool().Draw(t, "operand")
			value, operand = strconv.FormatBool(a), strconv.FormatBool(b)
			match = a == b
		}
		if rapid.Bool().Draw(t, "wrongType") {
			// Swap the variable for a value of another type.
			value = rapid.SampledFrom([]string{`null`, `[1]`, `{"a":1}`, `"1"`, `true`, `1`}).Draw(t, "other")
			kind := decode(t, value)
			_, isNum := kind.(document.Number)
			_, isStr := kind.(document.String)
			_, isBool := kind.(document.Bool)
			if strings.HasPrefix(op, "Numeric") && isNum || strings.HasPrefix(op, "String") && isStr || op == "BooleanEquals" && isBool {
				return
			}
			match = false
		}
		var rule, input string
		if rapid.Bool().Draw(t, "path") {
			rule = fmt.Sprintf(`{"Variable":"$.v","%sPath":"$.w","Next":"Yes"}`, op)
			input = fmt.Sprintf(`{"v":%s,"w":%s}`, value, operand)
		} else {
			rule = fmt.Sprintf(`{"Variable":"$.v",%q:%s,"Next":"Yes"}`, op, operand)
			input = fmt.Sprintf(`{"v":%s}`, value)
		}
		d := newDriver(t, fmt.Sprintf(choiceMachine, rule), rules())
		d.start(input)
		s, ok := d.runToEnd(20).(asl.Succeeded)
		want := map[bool]document.Value{true: document.String("yes"), false: document.String("no")}[match]
		if !ok || !equal(s.Output, want) {
			t.Fatalf("rule %s on %s took %v, want %s", rule, input, s.Output, encode(want))
		}
		// Not inverts the rule.
		not := strings.Replace(rule, `{"Variable"`, `{"Not":{"Variable"`, 1)
		not = strings.Replace(not, `,"Next":"Yes"}`, `},"Next":"Yes"}`, 1)
		n := newDriver(t, fmt.Sprintf(choiceMachine, not), rules())
		n.start(input)
		s, ok = n.runToEnd(20).(asl.Succeeded)
		want = map[bool]document.Value{false: document.String("yes"), true: document.String("no")}[match]
		if !ok || !equal(s.Output, want) {
			t.Fatalf("rule %s on %s took %v, want %s", not, input, s.Output, encode(want))
		}
	})
}
