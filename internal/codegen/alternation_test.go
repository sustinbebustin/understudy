package codegen_test

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/codegen"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
)

// looseAlternationsFile records the shapes whose loosely anchored pattern
// keeps Smithy's find semantics on purpose, each with its reason.
const looseAlternationsFile = "testdata/loose-alternations.json"

// span is one group of a pattern: the byte offsets of its ( and its ).
type span struct{ open, close int }

// scan splits ECMA pattern p at each | outside a group and a class, and
// returns the spans of its outermost groups.
func scan(p string) (branches []string, groups []span) {
	depth, start, open, inClass := 0, 0, 0, false
	for i := 0; i < len(p); i++ {
		switch c := p[i]; {
		case c == '\\':
			i++
		case inClass:
			inClass = c != ']'
		case c == '[':
			inClass = true
		case c == '(':
			if depth == 0 {
				open = i
			}
			depth++
		case c == ')':
			depth--
			if depth == 0 {
				groups = append(groups, span{open, i})
			}
		case c == '|' && depth == 0:
			branches = append(branches, p[start:i])
			start = i + 1
		}
	}
	return append(branches, p[start:]), groups
}

// groupBody is the content of a capturing or non-capturing group of b at
// g, or false for a lookaround or a group carrying flags, which anchor
// nothing for the check.
func groupBody(b string, g span) (string, bool) {
	body := b[g.open+1 : g.close]
	if rest, ok := strings.CutPrefix(body, "?:"); ok {
		return rest, true
	}
	return body, !strings.HasPrefix(body, "?")
}

// anchoredStart reports whether every match of branch b starts at the
// start of the value: b begins with ^, or with an unquantified group whose
// branches all do.
func anchoredStart(b string) bool {
	if strings.HasPrefix(b, "^") {
		return true
	}
	_, groups := scan(b)
	if len(groups) == 0 || groups[0].open != 0 {
		return false
	}
	g := groups[0]
	if g.close+1 < len(b) && strings.IndexByte("?*+{", b[g.close+1]) >= 0 {
		return false
	}
	return allBranches(b, g, anchoredStart)
}

// anchoredEnd reports whether every match of branch b ends at the end of
// the value: b ends in an unescaped $, or in a group whose branches all do.
// A group ending b cannot be quantified, as nothing follows it.
func anchoredEnd(b string) bool {
	if body, ok := strings.CutSuffix(b, "$"); ok {
		slashes := len(body) - len(strings.TrimRight(body, `\`))
		return slashes%2 == 0
	}
	_, groups := scan(b)
	if len(groups) == 0 || groups[len(groups)-1].close != len(b)-1 {
		return false
	}
	return allBranches(b, groups[len(groups)-1], anchoredEnd)
}

// allBranches reports whether anchored holds for every branch of the group
// of b at g.
func allBranches(b string, g span, anchored func(string) bool) bool {
	body, ok := groupBody(b, g)
	if !ok {
		return false
	}
	branches, _ := scan(body)
	for _, br := range branches {
		if !anchored(br) {
			return false
		}
	}
	return true
}

// looseAlternation reports whether ECMA pattern p has a top-level
// alternation with a branch not anchored at both ends. Smithy's pattern
// trait has find semantics, so ^a|b$ accepts any value starting with a or
// ending with b, while AWS has been seen matching the whole value against
// either branch (lambda#NumericLatestPublishedOrAliasQualifier in
// codegen/overlays/lambda.json).
func looseAlternation(p string) bool {
	branches, _ := scan(p)
	if len(branches) < 2 {
		return false
	}
	for _, b := range branches {
		if !anchoredStart(b) || !anchoredEnd(b) {
			return true
		}
	}
	return false
}

func TestLooseAlternation(t *testing.T) {
	for _, tt := range []struct {
		p    string
		want bool
	}{
		{`^a|b$`, true},
		{`^a$|b$`, true},
		{`a|b`, true},
		{`^a$|^b\$`, true},
		{`^a$|^b\\$`, false},
		{`^a$|^b$`, false},
		{`^(a|b)$`, false},
		{`^[a|b]$`, false},
		{`^a\|b$`, false},
		{`^abc$`, false},
		{`^(^a$)|(^b$)$`, false},
		{`^(?:^a$)|(?:^b|^c)$`, false},
		{`^(^a.*)|(b)$`, true},
		{`^(^a$)?|^b$`, true},
		{`^a$|(?=^b)$`, true},
		{`^(?i)a|b$`, true},
	} {
		if got := looseAlternation(tt.p); got != tt.want {
			t.Errorf("looseAlternation(%q) = %v, want %v", tt.p, got, tt.want)
		}
	}
}

// looseHits maps the target shape of every input member pattern ir checks
// that has a loose alternation to its source.
func looseHits(ir *codegen.ServiceIR) map[smithy.ShapeID]string {
	out := map[smithy.ShapeID]string{}
	for _, s := range ir.Shapes {
		if s.Role&codegen.RoleInput == 0 {
			continue
		}
		for _, m := range s.Members {
			if m.Pattern != nil && looseAlternation(m.Pattern.Source) {
				out[m.Target.ID] = m.Pattern.Source
			}
		}
	}
	return out
}

// unresolved lists each hit the confirmed list does not record.
func unresolved(hits, confirmed map[smithy.ShapeID]string) []string {
	var out []string
	for _, id := range slices.Sorted(maps.Keys(hits)) {
		if _, ok := confirmed[id]; !ok {
			out = append(out, fmt.Sprintf("%s %q", id, hits[id]))
		}
	}
	return out
}

// readConfirmed reads looseAlternationsFile by shape id; each entry needs a
// reason.
func readConfirmed(t *testing.T) map[smithy.ShapeID]string {
	t.Helper()
	raw, err := os.ReadFile(looseAlternationsFile)
	if err != nil {
		t.Fatal(err)
	}
	var entries []struct {
		Shape  smithy.ShapeID `json:"shape"`
		Reason string         `json:"reason"`
	}
	if err := json.Unmarshal(raw, &entries); err != nil {
		t.Fatalf("%s: %v", looseAlternationsFile, err)
	}
	out := map[smithy.ShapeID]string{}
	for _, e := range entries {
		if e.Reason == "" {
			t.Errorf("%s: %s has no reason; say why find semantics stand", looseAlternationsFile, e.Shape)
		}
		if _, dup := out[e.Shape]; dup {
			t.Errorf("%s: %s is listed twice; remove one", looseAlternationsFile, e.Shape)
		}
		out[e.Shape] = e.Reason
	}
	return out
}

func buildIR(t *testing.T, in codegen.Inputs) *codegen.ServiceIR {
	t.Helper()
	ir, err := codegen.BuildInputs(in)
	if err != nil {
		t.Fatal(err)
	}
	return ir
}

// TestLooseAlternationNeedsAnOverrideOrAConfirmation: a fixture pattern
// ^a|b$ is reported until the overlay replaces it or the confirmed list
// records it; a fully anchored alternation is never reported.
func TestLooseAlternationNeedsAnOverrideOrAConfirmation(t *testing.T) {
	const name = smithy.ShapeID("com.example.golden#Name")
	loose := withModelEdit(t, `"smithy.api#pattern": "^[a-z]+$"`, `"smithy.api#pattern": "^a|b$"`)

	if got := unresolved(looseHits(buildIR(t, loose)), nil); !slices.Equal(got, []string{`com.example.golden#Name "^a|b$"`}) {
		t.Errorf("unresolved = %v, want the fixture's Name", got)
	}
	if got := unresolved(looseHits(buildIR(t, loose)), map[smithy.ShapeID]string{name: "confirmed: find semantics kept, inferred"}); got != nil {
		t.Errorf("a confirmed shape is still reported: %v", got)
	}
	overridden := loose
	overridden.Overlays = append(slices.Clone(loose.Overlays), codegen.Source{Name: "extra.json", Content: []byte(overlay(
		`"com.example.golden#Name": {"type": "apply", "replace": true, "traits": {"smithy.api#pattern": "^(a|b)$", ` + evidence + `}}`))})
	if got := unresolved(looseHits(buildIR(t, overridden)), nil); got != nil {
		t.Errorf("an overridden shape is still reported: %v", got)
	}

	anchored := withModelEdit(t, `"smithy.api#pattern": "^[a-z]+$"`, `"smithy.api#pattern": "^a$|^b$"`)
	if got := unresolved(looseHits(buildIR(t, anchored)), nil); got != nil {
		t.Errorf("a fully anchored alternation is reported: %v", got)
	}
}

// TestEveryLooseAlternationIsResolved walks every pattern the generator
// emits a validator for, across every service: each loosely anchored
// alternation is overridden in codegen/overlays or recorded in
// looseAlternationsFile, and every recorded shape is still a hit.
func TestEveryLooseAlternationIsResolved(t *testing.T) {
	repo, err := codegen.FindRepo(".")
	if err != nil {
		t.Fatal(err)
	}
	ids, err := repo.Services()
	if err != nil {
		t.Fatal(err)
	}
	hits := map[smithy.ShapeID]string{}
	for _, id := range ids {
		in, err := repo.Inputs(id)
		if err != nil {
			t.Fatal(err)
		}
		maps.Copy(hits, looseHits(buildIR(t, in)))
	}
	confirmed := readConfirmed(t)
	if got := unresolved(hits, confirmed); got != nil {
		t.Errorf("patterns with a loosely anchored top-level alternation keep find semantics; override each in codegen/overlays or record it in %s with a reason:\n%s",
			looseAlternationsFile, strings.Join(got, "\n"))
	}
	for _, id := range slices.Sorted(maps.Keys(confirmed)) {
		if _, ok := hits[id]; !ok {
			t.Errorf("%s records %s, which no longer has a loose alternation the generator checks; remove the entry", looseAlternationsFile, id)
		}
	}
}
