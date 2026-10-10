package store_test

import (
	"errors"
	"testing"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/store"
)

func account(t rapid.TB, digits string) aws.AccountID {
	t.Helper()
	a, err := aws.ParseAccountID(digits)
	if err != nil {
		t.Fatalf("test account: %v", err)
	}
	return a
}

// region admits a name the way a persisted region is: by syntax only, so
// configured extras are covered too.
func region(t rapid.TB, name string) aws.Region {
	t.Helper()
	var r aws.Region
	if err := r.UnmarshalText([]byte(name)); err != nil {
		t.Fatalf("test region: %v", err)
	}
	return r
}

func scope(t rapid.TB, digits, name string) aws.Scope {
	t.Helper()
	return aws.Scope{Partition: aws.PartitionAWS(), Account: account(t, digits), Region: region(t, name)}
}

func drawAccount(t *rapid.T) aws.AccountID {
	return account(t, rapid.StringMatching(`[0-9]{12}`).Draw(t, "account"))
}

func drawScope(t *rapid.T) aws.Scope {
	name := rapid.OneOf(
		rapid.SampledFrom([]string{"us-east-1", "us-west-2", "eu-west-1"}),
		rapid.StringMatching(`[a-z][a-z0-9-]{0,10}[a-z0-9]`),
	).Draw(t, "region")
	return aws.Scope{Partition: aws.PartitionAWS(), Account: drawAccount(t), Region: region(t, name)}
}

func TestSharedKeyCodecsRoundTripThroughExportAndImport(t *testing.T) {
	t.Parallel()
	t.Run("scope keys", func(t *testing.T) {
		t.Parallel()
		rapid.Check(t, func(t *rapid.T) {
			src := newQueues[aws.Scope]()
			fill(t, src, rapid.SliceOf(rapid.Custom(drawScope)).Draw(t, "keys"))
			exportImportExport(t, newQueues[aws.Scope], store.ScopeKeys(), src)
		})
	})
	t.Run("account keys", func(t *testing.T) {
		t.Parallel()
		rapid.Check(t, func(t *rapid.T) {
			src := newQueues[aws.AccountID]()
			fill(t, src, rapid.SliceOf(rapid.Custom(drawAccount)).Draw(t, "keys"))
			exportImportExport(t, newQueues[aws.AccountID], store.AccountKeys(), src)
		})
	})
}

// Any section path either is refused with a *SectionError or names a key
// that exports under exactly that path again, so no snapshot can load state
// that the next save would move or refuse.
func TestSharedKeyCodecsImportOnlyCanonicalPaths(t *testing.T) {
	t.Parallel()
	paths := rapid.OneOf(
		rapid.StringMatching(`[0-9]{12}/[a-z][a-z0-9-]{0,8}\.json`),
		rapid.StringMatching(`[0-9]{12}\.json`),
		rapid.StringMatching(`[0-9]{10,13}(/[a-zA-Z0-9.-]{0,6}){0,3}(\.json)?`),
		rapid.StringMatching(`[0-9a-zA-Z/._-]{0,30}`),
	)
	t.Run("scope keys", func(t *testing.T) {
		t.Parallel()
		rapid.Check(t, func(t *rapid.T) {
			importOnlyCanonical(t, newQueues[aws.Scope](), store.ScopeKeys(), paths.Draw(t, "path"))
		})
	})
	t.Run("account keys", func(t *testing.T) {
		t.Parallel()
		rapid.Check(t, func(t *rapid.T) {
			importOnlyCanonical(t, newQueues[aws.AccountID](), store.AccountKeys(), paths.Draw(t, "path"))
		})
	})
}

func importOnlyCanonical[K comparable](t *rapid.T, p *store.Partitioned[K, queues], kc store.KeyCodec[K], path string) {
	err := p.Import(kc, 1, []store.Section{{Path: path, Version: 1, Data: []byte(`{"bodies":["x"]}`)}})
	if err != nil {
		if se, ok := errors.AsType[*store.SectionError](err); !ok || se.Path != path {
			t.Fatalf("Import(%q) error = %v, want a *SectionError naming the path", path, err)
		}
		return
	}
	got, err := p.Export(kc, 1)
	if err != nil {
		t.Fatalf("Export after importing %q: %v", path, err)
	}
	if len(got) != 1 || got[0].Path != path {
		t.Fatalf("imported %q but Export wrote %+v", path, got)
	}
}

func TestScopeKeysDecodeRefusesPathsOutsideTheLayout(t *testing.T) {
	t.Parallel()
	for _, enc := range []string{
		"000000000000",
		"000000000000/",
		"000000000000/us-east-1/extra",
		"/us-east-1",
		"12345/us-east-1",
		"000000000000/US-EAST-1",
		"000000000000/us-east-",
	} {
		t.Run(enc, func(t *testing.T) {
			t.Parallel()
			if s, err := store.ScopeKeys().Decode(enc); err == nil {
				t.Fatalf("Decode(%q) = %+v, want an error", enc, s)
			}
		})
	}
}

func TestScopeKeysDecodeSetsTheAWSPartition(t *testing.T) {
	t.Parallel()
	got, err := store.ScopeKeys().Decode("123456789012/eu-west-1")
	if err != nil {
		t.Fatal(err)
	}
	if want := scope(t, "123456789012", "eu-west-1"); got != want {
		t.Fatalf("Decode = %+v, want %+v", got, want)
	}
}

// A scope Decode can never produce has no encoding, so Export refuses it
// rather than writing a section that would load under a different key.
func TestScopeKeysExportRefusesUnrepresentableScopes(t *testing.T) {
	t.Parallel()
	full := scope(t, "000000000000", "us-east-1")
	cases := map[string]aws.Scope{
		"zero partition": {Account: full.Account, Region: full.Region},
		"zero account":   {Partition: full.Partition, Region: full.Region},
		"zero region":    {Partition: full.Partition, Account: full.Account},
	}
	for name, s := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			p := newQueues[aws.Scope]()
			store.Update(p.For(s), func(q *queues) (struct{}, bool) { q.Bodies = []string{"x"}; return struct{}{}, true })
			_, err := p.Export(store.ScopeKeys(), 1)
			wantSectionError(t, err, ".json", store.NoEncoding)
		})
	}
}

func TestAccountKeysRefuseTheZeroAccountAndNonDigits(t *testing.T) {
	t.Parallel()
	if a, err := store.AccountKeys().Decode("12345678901a"); err == nil {
		t.Fatalf("Decode accepted a non-digit account as %v", a)
	}
	p := newQueues[aws.AccountID]()
	store.Update(p.For(aws.AccountID{}), func(q *queues) (struct{}, bool) { q.Bodies = []string{"x"}; return struct{}{}, true })
	_, err := p.Export(store.AccountKeys(), 1)
	wantSectionError(t, err, ".json", store.NoEncoding)
}
