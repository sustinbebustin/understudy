package policycatalogue_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/policycatalogue"
)

// genPolicy draws a policy New accepts: a name of IAM's policyNameType
// characters, a Type on its path (iam-evaluation.md 10.4), a version of
// policyVersionIdType, minute-precision UTC times (10.5), any description
// and a JSON document in any layout holding any text.
func genPolicy() *rapid.Generator[policycatalogue.Policy] {
	types := []policycatalogue.Type{policycatalogue.AWSManaged, policycatalogue.ServiceLinkedRole, policycatalogue.ServiceRole, policycatalogue.JobFunction}
	return rapid.Custom(func(t *rapid.T) policycatalogue.Policy {
		name := rapid.StringMatching(`[A-Za-z0-9_+=,.@-]{1,24}`).Draw(t, "name")
		typ := rapid.SampledFrom(types).Draw(t, "type")
		at := func(label string) time.Time {
			m := rapid.Int64Range(0, 20*365*24*60).Draw(t, label)
			return time.Date(2010, time.January, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(m) * time.Minute)
		}
		value, err := json.Marshal(rapid.String().Draw(t, "value"))
		if err != nil {
			t.Fatal(err)
		}
		ws := rapid.SampledFrom([]string{"", " ", "\n  ", "\t", "\r\n"})
		doc := fmt.Sprintf(`{%s"Version"%s:%s"2012-10-17",%s"Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":%s}]%s}`,
			ws.Draw(t, "w1"), ws.Draw(t, "w2"), ws.Draw(t, "w3"), ws.Draw(t, "w4"), value, ws.Draw(t, "w5"))
		return policycatalogue.Policy{
			Name:        name,
			ARN:         "arn:aws:iam::aws:policy" + pathOf[typ] + name,
			Path:        pathOf[typ],
			Type:        typ,
			Description: rapid.String().Draw(t, "description"),
			VersionID:   rapid.StringMatching(`v[1-9][0-9]{0,2}(\.[A-Za-z0-9-]{0,3})?`).Draw(t, "version"),
			Created:     at("created"),
			Updated:     at("updated"),
			Document:    doc,
		}
	})
}

// genPolicies draws policies whose names differ regardless of case.
func genPolicies(t *rapid.T) []policycatalogue.Policy {
	return rapid.SliceOfNDistinct(genPolicy(), 0, 8, func(p policycatalogue.Policy) string { return strings.ToLower(p.Name) }).Draw(t, "policies")
}

// For any valid policies, New lists each once by name regardless of case
// and finds each by its exact ARN; Encode, Parse and Encode again is the
// same text; and the order New was given changes nothing.
func TestCatalogueProperties(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		ps := genPolicies(t)
		c, err := policycatalogue.New(ps)
		if err != nil {
			t.Fatalf("New = %v", err)
		}
		if c.Len() != len(ps) {
			t.Fatalf("Len = %d, want %d", c.Len(), len(ps))
		}
		listed := slices.Collect(c.All())
		if !slices.IsSortedFunc(listed, func(a, b policycatalogue.Policy) int {
			return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
		}) {
			t.Fatalf("All = %v, want names in order regardless of case", names(c))
		}
		for _, p := range ps {
			got, ok := c.Lookup(p.ARN)
			if !ok {
				t.Fatalf("Lookup(%s) found nothing", p.ARN)
			}
			if diff := cmp.Diff(p, got); diff != "" {
				t.Fatalf("Lookup(%s) (-given +got):\n%s", p.ARN, diff)
			}
			if _, ok := c.Lookup(strings.ToUpper(p.ARN)); ok {
				t.Fatalf("Lookup(%s) matched regardless of case", strings.ToUpper(p.ARN))
			}
		}

		encoded, err := c.Encode()
		if err != nil {
			t.Fatalf("Encode = %v", err)
		}
		parsed, err := policycatalogue.Parse(encoded)
		if err != nil {
			t.Fatalf("Parse(Encode) = %v\n%s", err, encoded)
		}
		if diff := cmp.Diff(listed, slices.Collect(parsed.All())); diff != "" {
			t.Fatalf("Parse(Encode) (-New +Parse):\n%s", diff)
		}
		again, err := parsed.Encode()
		if err != nil || !bytes.Equal(again, encoded) {
			t.Fatalf("Encode(Parse(Encode)) = %q, %v; want %q", again, err, encoded)
		}

		shuffled, err := policycatalogue.New(rapid.Permutation(ps).Draw(t, "shuffled"))
		if err != nil {
			t.Fatalf("New(shuffled) = %v", err)
		}
		if other, err := shuffled.Encode(); err != nil || !bytes.Equal(other, encoded) {
			t.Fatalf("Encode of a permutation = %q, %v; want %q", other, err, encoded)
		}
	})
}
