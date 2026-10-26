package identity_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/ids"
)

// What AssumeRole passed for a session (session policies, session tags,
// the source identity and the issue time) is read by IAM evaluation
// (iam-evaluation.md 2.7, 8.1), so it survives a snapshot: Export writes it
// on the principal record and Import reads it back. Attributes are a
// pointer, so principals compare by value here.

func attributesGen() *rapid.Generator[*identity.SessionAttributes] {
	text := rapid.StringMatching(`[A-Za-z0-9 {}":,*/_-]{0,24}`)
	word := rapid.StringMatching(`[A-Za-z0-9+=,.@_:/-]{1,12}`)
	return rapid.Custom(func(t *rapid.T) *identity.SessionAttributes {
		if rapid.Bool().Draw(t, "none") {
			return nil
		}
		a := &identity.SessionAttributes{
			Policy:         text.Draw(t, "policy"),
			SourceIdentity: rapid.SampledFrom([]string{"", "ana@example.com", "build-42"}).Draw(t, "source identity"),
			// Whole seconds up to 9999-12-31, the last instant RFC 3339 carries.
			Issued: clock.FromWire(time.Unix(rapid.Int64Range(0, 253402300799).Draw(t, "issued"), 0).UTC()),
		}
		for i := range rapid.IntRange(0, 3).Draw(t, "policy arns") {
			a.PolicyARNs = append(a.PolicyARNs, "arn:aws:iam::123456789012:policy/"+word.Draw(t, fmt.Sprint("arn ", i)))
		}
		for i := range rapid.IntRange(0, 3).Draw(t, "tags") {
			a.Tags = append(a.Tags, identity.SessionTag{Key: word.Draw(t, fmt.Sprint("key ", i)), Value: text.Draw(t, fmt.Sprint("value ", i))})
		}
		for i := range rapid.IntRange(0, 2).Draw(t, "transitive") {
			a.TransitiveTagKeys = append(a.TransitiveTagKeys, word.Draw(t, fmt.Sprint("transitive ", i)))
		}
		return a
	})
}

// Export then Import round-trips every attribute field, and the second
// Export writes the same bytes (contract of identity Export; ARCHITECTURE.md
// 13: snapshots are deterministic).
func TestExportImportRoundTripsSessionAttributes(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		acct := account(t, "123456789012")
		src := identity.New(ids.NewSeeded(rapid.Uint64().Draw(t, "seed")), aws.DefaultAccount())
		role := identity.AssumedRole{
			Account: acct, Role: identity.RoleRef{Name: "app", Path: "/", ID: "AROAAPPEXAMPLE000001"},
			Session: "s1", Source: identity.FromAssumeRole{}, Attributes: attributesGen().Draw(t, "attributes"),
		}
		c := mint(t, src, role, identity.Temporary{Expiry: time.Date(2026, 10, 26, 13, 0, 0, 0, time.UTC)})
		out, err := src.Export()
		if err != nil {
			t.Fatalf("Export: %v", err)
		}
		dst := identity.New(ids.NewSeeded(0), aws.DefaultAccount())
		if err := dst.Import(out); err != nil {
			t.Fatalf("Import: %v", err)
		}
		got, _ := dst.Resolve(string(c.AccessKeyID))
		if !reflect.DeepEqual(got, identity.Principal(role)) {
			t.Fatalf("after Import, Resolve = %#v (attributes %+v); want %#v (attributes %+v)", got, attributesOf(got), role, role.Attributes)
		}
		again, err := dst.Export()
		if err != nil {
			t.Fatalf("second Export: %v", err)
		}
		if !bytes.Equal(out, again) {
			t.Fatalf("Export after Import differs:\n%s\n%s", out, again)
		}
	})
}

func attributesOf(p identity.Principal) *identity.SessionAttributes {
	if r, ok := p.(identity.AssumedRole); ok {
		return r.Attributes
	}
	return nil
}

// The persisted shape: an "attributes" object on the principal record with
// lowerCamel fields (ARCHITECTURE.md 13), "issued" in RFC 3339 UTC, and the
// state version unchanged, since a record without attributes still reads
// (as nil).
func TestExportFormatOfSessionAttributes(t *testing.T) {
	s := identity.New(ids.NewSeeded(1), aws.DefaultAccount())
	role := identity.AssumedRole{
		Account: account(t, "123456789012"), Role: identity.RoleRef{Name: "app", Path: "/", ID: "AROA1"},
		Session: "s1", Source: identity.FromAssumeRole{},
		Attributes: &identity.SessionAttributes{
			Policy:            `{"Version":"2012-10-17"}`,
			PolicyARNs:        []string{"arn:aws:iam::123456789012:policy/p"},
			Tags:              []identity.SessionTag{{Key: "team", Value: "blue"}},
			TransitiveTagKeys: []string{"team"},
			SourceIdentity:    "ana",
			Issued:            clock.FromWire(time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)),
		},
	}
	mint(t, s, role, identity.Temporary{Expiry: time.Date(2026, 10, 26, 13, 0, 0, 0, time.UTC)})
	out, err := s.Export()
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Version int `json:"version"`
		Keys    []struct {
			Principal struct {
				Attributes map[string]any `json:"attributes"`
			} `json:"principal"`
		} `json:"keys"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Version != identity.StateVersion || identity.StateVersion != 1 || len(doc.Keys) != 1 {
		t.Fatalf("Export = %s; want version 1 and one key", out)
	}
	want := map[string]any{
		"policy":            `{"Version":"2012-10-17"}`,
		"policyArns":        []any{"arn:aws:iam::123456789012:policy/p"},
		"tags":              []any{map[string]any{"key": "team", "value": "blue"}},
		"transitiveTagKeys": []any{"team"},
		"sourceIdentity":    "ana",
		"issued":            "2026-10-26T12:00:00Z",
	}
	if got := doc.Keys[0].Principal.Attributes; !reflect.DeepEqual(got, want) {
		t.Errorf("principal attributes = %#v\nwant %#v\nin %s", got, want, out)
	}

	// Each field is omitted when empty, and a session without attributes
	// has no "attributes" at all.
	bare := identity.New(ids.NewSeeded(2), aws.DefaultAccount())
	role.Attributes = &identity.SessionAttributes{Issued: clock.FromWire(time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC))}
	mint(t, bare, role, identity.Temporary{Expiry: time.Date(2026, 10, 26, 13, 0, 0, 0, time.UTC)})
	role.Attributes = nil
	mint(t, bare, role, identity.Temporary{Expiry: time.Date(2026, 10, 26, 13, 0, 0, 0, time.UTC)})
	out, err = bare.Export()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out, []byte(`"attributes":{"issued":"2026-10-26T12:00:00Z"}`)) {
		t.Errorf("Export = %s; want attributes holding only issued", out)
	}
	if n := bytes.Count(out, []byte(`"attributes"`)); n != 1 {
		t.Errorf("Export has %d attributes objects, want 1 (none for the session without attributes): %s", n, out)
	}
}
