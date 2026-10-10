package ruler

import (
	"slices"
	"strconv"
	"strings"
)

// glob is a parsed wildcard pattern: literal bytes where star is false, and
// "*" (zero or more bytes) where star is true.
type glob struct {
	items []globItem
}

type globItem struct {
	star bool
	b    byte
}

// parseGlob parses a wildcard operand with Ruler's grammar (WildcardParser):
// "*" is a wildcard, "\*" and "\\" are the only escapes, and consecutive "*"
// are rejected. Ruler parses the value with its quotes added, so the
// positions in its messages are the byte index plus one.
func parseGlob(s string) (g glob, detail string) {
	items := make([]globItem, 0, len(s))
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '*':
			if i+1 < len(s) && s[i+1] == '*' {
				return glob{}, "Consecutive wildcard characters at pos " + strconv.Itoa(i+1)
			}
			items = append(items, globItem{star: true})
		case '\\':
			if i+1 < len(s) && (s[i+1] == '*' || s[i+1] == '\\') {
				items = append(items, globItem{b: s[i+1]})
				i++
				continue
			}
			return glob{}, "Invalid escape character at pos " + strconv.Itoa(i+1)
		default:
			items = append(items, globItem{b: s[i]})
		}
	}
	return glob{items: items}, ""
}

// stars counts the wildcard characters.
func (g glob) stars() int {
	n := 0
	for _, it := range g.items {
		if it.star {
			n++
		}
	}
	return n
}

// matches reports whether the whole of s matches the pattern, byte by byte.
func (g glob) matches(s string) bool {
	p, i := 0, 0
	starP, starI := -1, 0
	for i < len(s) {
		switch {
		case p < len(g.items) && g.items[p].star:
			starP, starI = p, i
			p++
		case p < len(g.items) && g.items[p].b == s[i]:
			p++
			i++
		case starP >= 0:
			starI++
			p, i = starP+1, starI
		default:
			return false
		}
	}
	for p < len(g.items) && g.items[p].star {
		p++
	}
	return p == len(g.items)
}

// maxComplexitySubsets bounds the subset exploration of wildcardComplexity;
// a pattern set that reaches it is reported at the cap.
const maxComplexitySubsets = 1 << 14

// wildcardComplexity is Ruler's MachineComplexityEvaluator measure for the
// wildcard patterns of one field: the largest number of automaton states any
// input value can keep active at once, counting a state once per pattern that
// can still match through it. Each pattern is a chain of states, one per
// literal byte and one self-looping state per "*"; the start state is not
// counted. Ruler's machine holds a value with its JSON quotes, so each
// pattern starts and ends with a '"', and holds a repeated pattern once.
//
// It reproduces Ruler v2.2.0's values for the research cases
// (matching-engines.md 4.8): /Users/*/doc.txt 2, an ARN with two wildcards 3,
// *a*a*a*a* 9, *:*:*:*:*:event-bus/* 11. In a one-off run in October 2026
// against executed Ruler v2.2.0 (commit ced786201c1f), whose harness is not
// kept in the repository, it agreed on 3000 random sets of up to three short
// patterns except on 3 sets that repeat a pattern starting with "*" and a
// literal, which Ruler scores one higher.
// Evaluation stops at limit.
func wildcardComplexity(globs []glob, limit int) int {
	if len(globs) == 0 {
		return 0
	}
	n := newGlobNFA(quoted(globs))
	alphabet := n.alphabet()
	start := n.closure([]int{startState})
	seen := map[string]bool{key(start): true}
	queue := [][]int{start}
	best := n.weight(start)
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, b := range alphabet {
			next := n.step(cur, b)
			w := n.weight(next)
			if w >= limit {
				return limit
			}
			best = max(best, w)
			k := key(next)
			if w == 0 || seen[k] {
				continue
			}
			if len(seen) >= maxComplexitySubsets {
				return limit
			}
			seen[k] = true
			queue = append(queue, next)
		}
	}
	return best
}

// quoted returns the distinct patterns with a literal '"' around each, the
// form in which Ruler's machine holds them.
func quoted(globs []glob) []glob {
	out := make([]glob, 0, len(globs))
	for _, g := range globs {
		q := glob{items: make([]globItem, 0, len(g.items)+2)}
		q.items = append(q.items, globItem{b: '"'})
		q.items = append(q.items, g.items...)
		q.items = append(q.items, globItem{b: '"'})
		if !slices.ContainsFunc(out, func(o glob) bool { return slices.Equal(o.items, q.items) }) {
			out = append(out, q)
		}
	}
	return out
}

// startState is the shared start state; it is active only before the first
// byte and is never counted.
const startState = -1

// globNFA numbers the states of every pattern: pattern i occupies states
// offset[i]+1 .. offset[i]+len(items), and state offset[i]+p means the first
// p items matched.
type globNFA struct {
	globs  []glob
	offset []int
}

func newGlobNFA(globs []glob) globNFA {
	offset := make([]int, len(globs))
	total := 0
	for i, g := range globs {
		offset[i] = total
		total += len(g.items) + 1
	}
	return globNFA{globs: globs, offset: offset}
}

// alphabet is every literal byte plus one byte no pattern names, which
// stands for all the others.
func (n globNFA) alphabet() []byte {
	var bs []byte
	for _, g := range n.globs {
		for _, it := range g.items {
			if !it.star && !slices.Contains(bs, it.b) {
				bs = append(bs, it.b)
			}
		}
	}
	for c := range 256 {
		if !slices.Contains(bs, byte(c)) {
			return append(bs, byte(c))
		}
	}
	return bs
}

// locate maps a pattern state to its pattern and position.
func (n globNFA) locate(state int) (pattern, position int) {
	for i := len(n.offset) - 1; i > 0; i-- {
		if state > n.offset[i] {
			return i, state - n.offset[i]
		}
	}
	return 0, state - n.offset[0]
}

// item is the item a state waits on, if any.
func (n globNFA) item(state int) (globItem, bool) {
	gi, p := n.locate(state)
	items := n.globs[gi].items
	if p < len(items) {
		return items[p], true
	}
	return globItem{}, false
}

// closure adds every state reachable without input: entering a "*" item.
func (n globNFA) closure(states []int) []int {
	out := slices.Clone(states)
	for j := 0; j < len(out); j++ {
		if out[j] == startState {
			for i, g := range n.globs {
				if len(g.items) > 0 && g.items[0].star {
					out = append(out, n.offset[i]+1)
				}
			}
			continue
		}
		if it, ok := n.item(out[j]); ok && it.star {
			out = append(out, out[j]+1)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// step consumes byte b from the active states.
func (n globNFA) step(states []int, b byte) []int {
	var next []int
	for _, s := range states {
		if s == startState {
			for i, g := range n.globs {
				if len(g.items) > 0 && !g.items[0].star && g.items[0].b == b {
					next = append(next, n.offset[i]+1)
				}
			}
			continue
		}
		gi, p := n.locate(s)
		if n.globs[gi].items[p-1].star {
			next = append(next, s)
		}
		if it, ok := n.item(s); ok && !it.star && it.b == b {
			next = append(next, s+1)
		}
	}
	return n.closure(next)
}

// weight counts the active pattern states.
func (n globNFA) weight(states []int) int {
	if len(states) > 0 && states[0] == startState {
		return len(states) - 1
	}
	return len(states)
}

func key(states []int) string {
	var b strings.Builder
	for _, s := range states {
		b.WriteString(strconv.Itoa(s))
		b.WriteByte(',')
	}
	return b.String()
}
