package identity_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/ids"
)

// fataler is what testing.T and rapid.T share.
type fataler interface {
	Helper()
	Fatalf(format string, args ...any)
}

func account(t fataler, s string) aws.AccountID {
	t.Helper()
	a, err := aws.ParseAccountID(s)
	if err != nil {
		t.Fatalf("ParseAccountID(%q): %v", s, err)
	}
	return a
}

// mint issues credentials for p with MintLongTerm or MintTemporary, as life
// asks.
func mint(t fataler, s *identity.Store, p identity.Principal, life identity.Lifetime) identity.Credentials {
	t.Helper()
	var (
		c   identity.Credentials
		err error
	)
	switch life := life.(type) {
	case identity.LongTerm:
		c, err = s.MintLongTerm(p)
	case identity.Temporary:
		c, err = s.MintTemporary(p, life.Expiry)
	default:
		t.Fatalf("mint: lifetime %T is not LongTerm or Temporary", life)
	}
	if err != nil {
		t.Fatalf("mint(%s): %v", identity.Render(p), err)
	}
	return c
}

// sessionToken is c's session token, or "" for long-term credentials.
func sessionToken(c identity.Credentials) string {
	if t, ok := c.Lifetime.(identity.Temporary); ok {
		return string(t.SessionToken)
	}
	return ""
}

func TestResolveMintedKeysMapToTheirPrincipal(t *testing.T) {
	s := identity.New(ids.NewSeeded(1), aws.DefaultAccount())
	acct := account(t, "123456789012")
	fn, err := aws.ParseARN("arn:aws:lambda:us-east-1:123456789012:function:worker")
	if err != nil {
		t.Fatal(err)
	}
	expiry := time.Date(2026, 10, 8, 13, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name   string
		p      identity.Principal
		life   identity.Lifetime
		prefix string
	}{
		{"user long-term key", identity.User{Account: acct, Path: "/", Name: "Alice", ID: "AIDACKCEVSQ6C2EXAMPLE"}, identity.LongTerm{}, "AKIA"},
		{"root key", identity.Root{Account: acct}, identity.LongTerm{}, "AKIA"},
		{"AssumeRole session", identity.AssumedRole{
			Account: acct, Role: identity.RoleRef{Name: "demo", Path: "/", ID: "AROADBQP57FF2AEXAMPLE"},
			Session: "TestAR", Source: identity.FromAssumeRole{},
		}, identity.Temporary{Expiry: expiry}, "ASIA"},
		{"Lambda execution environment", identity.AssumedRole{
			Account: acct, Role: identity.RoleRef{Name: "worker-role", Path: "/service/", ID: "AROAV35GFBG7AT2QBVERC"},
			Session: "worker", Source: identity.FromExecutionEnvironment{Function: fn, Env: "env-1"},
		}, identity.Temporary{Expiry: expiry}, "ASIA"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := mint(t, s, tc.p, tc.life)
			if !strings.HasPrefix(string(c.AccessKeyID), tc.prefix) {
				t.Errorf("key %s does not start with %s", c.AccessKeyID, tc.prefix)
			}
			if got, ok := ids.DecodeAccount(string(c.AccessKeyID)); !ok || got != acct.String() {
				t.Errorf("minted key %s decodes to %q (ok %v), want the principal's account %s", c.AccessKeyID, got, ok, acct)
			}
			switch want := tc.life.(type) {
			case identity.Temporary:
				got, ok := c.Lifetime.(identity.Temporary)
				if !ok || !got.Expiry.Equal(want.Expiry) || got.SessionToken == "" {
					t.Errorf("lifetime %#v, want Temporary expiring at %s with a session token", c.Lifetime, want.Expiry)
				}
			case identity.LongTerm:
				if c.Lifetime != (identity.LongTerm{}) {
					t.Errorf("lifetime %#v, want LongTerm", c.Lifetime)
				}
			}
			p, res := s.Resolve(string(c.AccessKeyID))
			if p != tc.p {
				t.Errorf("Resolve = %#v, want %#v", p, tc.p)
			}
			// Comparing the whole Lifetime also compares the session token,
			// which verification checks X-Amz-Security-Token against.
			if m, ok := res.(identity.Minted); !ok || m.Secret != c.SecretAccessKey || m.Lifetime != c.Lifetime {
				t.Errorf("Resolve resolution = %#v, want Minted with the minted secret and lifetime", res)
			}
		})
	}
}

func TestResolveNeverDecodesUnmintedKeys(t *testing.T) {
	def := aws.DefaultAccount()
	s := identity.New(ids.NewSeeded(1), def)

	// AKIAIOSFODNN7EXAMPLE decodes to 581039954779 (iam-sts-kms.md 2.2).
	if got, _ := ids.DecodeAccount("AKIAIOSFODNN7EXAMPLE"); got != "581039954779" {
		t.Fatalf("precondition: AKIAIOSFODNN7EXAMPLE decodes to %q", got)
	}
	p, res := s.Resolve("AKIAIOSFODNN7EXAMPLE")
	if p != (identity.Root{Account: def}) || res != (identity.Fallback{Warn: true}) {
		t.Errorf("first Resolve(AKIAIOSFODNN7EXAMPLE) = %#v, %#v; want the default account root with a warning", p, res)
	}
	if _, res := s.Resolve("AKIAIOSFODNN7EXAMPLE"); res != (identity.Fallback{Warn: false}) {
		t.Errorf("second Resolve(AKIAIOSFODNN7EXAMPLE) = %#v; want one warning per key", res)
	}

	for _, key := range []string{"AKIAI44QH8DHBEXAMPLE", "", "ASIA", "TEST", "test2"} {
		p, res := s.Resolve(key)
		if p != (identity.Root{Account: def}) || res != (identity.Fallback{Warn: false}) {
			t.Errorf("Resolve(%q) = %#v, %#v; want the default account root without a warning", key, p, res)
		}
	}
}

// The store remembers at most 10,000 warned keys; past that, new keys
// resolve without a warning rather than growing memory.
func TestResolveWarnsAboutAtMostTenThousandKeys(t *testing.T) {
	const capacity = 10_000
	s := identity.New(ids.NewSeeded(1), aws.DefaultAccount())
	keys := ids.NewSeeded(2)
	draw := func() string {
		k, err := keys.AccessKeyID("123456789012")
		if err != nil {
			t.Fatal(err)
		}
		return string(k)
	}
	for range capacity {
		key := draw()
		if _, res := s.Resolve(key); res != (identity.Fallback{Warn: true}) {
			t.Fatalf("first Resolve(%s) under the cap = %#v; want a warning", key, res)
		}
	}
	key := draw()
	if _, res := s.Resolve(key); res != (identity.Fallback{}) {
		t.Errorf("Resolve(%s) past the cap = %#v; want no warning", key, res)
	}
}

func TestResolveTwelveDigitKeyIsThatAccountsRoot(t *testing.T) {
	s := identity.New(ids.NewSeeded(1), aws.DefaultAccount())
	p, res := s.Resolve("112233445566")
	if p != (identity.Root{Account: account(t, "112233445566")}) || res != (identity.AccountKey{Secret: identity.DummySecret}) {
		t.Errorf("Resolve(112233445566) = %#v, %#v; want that account's root with the dummy secret", p, res)
	}
	if _, res := s.Resolve("11223344556"); res != (identity.Fallback{}) {
		t.Errorf("an 11-digit key resolved as %#v; want the default account", res)
	}
}

// The quickstart pair test/test is the default account's root, and its
// secret is the dummy one, so it verifies under SigV4 verification.
func TestResolveDummyKeyIsTheDefaultAccountsRoot(t *testing.T) {
	def := account(t, "111122223333")
	s := identity.New(ids.NewSeeded(1), def)
	p, res := s.Resolve("test")
	if p != (identity.Root{Account: def}) || res != (identity.Static{Secret: "test"}) {
		t.Errorf("Resolve(test) = %#v, %#v; want the default account's root with secret test", p, res)
	}
}

// Every resolution but Fallback carries the secret its key signs with.
func TestSecretOf(t *testing.T) {
	for _, tc := range []struct {
		res    identity.Resolution
		want   ids.SecretAccessKey
		wantOK bool
	}{
		{identity.Minted{Secret: "minted", Lifetime: identity.LongTerm{}}, "minted", true},
		{identity.Static{Secret: "static"}, "static", true},
		{identity.AccountKey{Secret: identity.DummySecret}, identity.DummySecret, true},
		{identity.Fallback{Warn: true}, "", false},
	} {
		if got, ok := identity.SecretOf(tc.res); got != tc.want || ok != tc.wantOK {
			t.Errorf("SecretOf(%T) = %q, %v; want %q, %v", tc.res, got, ok, tc.want, tc.wantOK)
		}
	}
}

func TestStaticKeysResolveToTheirAccountsRoot(t *testing.T) {
	s := identity.New(ids.NewSeeded(1), aws.DefaultAccount())
	acct := account(t, "444455556666")
	if err := s.SetStaticKeys([]identity.StaticKey{{AccessKeyID: "CIKEY0000000000001", Secret: "ci-secret", Account: acct}}); err != nil {
		t.Fatal(err)
	}
	p, res := s.Resolve("CIKEY0000000000001")
	if p != (identity.Root{Account: acct}) || res != (identity.Static{Secret: "ci-secret"}) {
		t.Errorf("Resolve(static key) = %#v, %#v; want %s's root with its secret", p, res, acct)
	}
	// A static key wins over the dummy key and over a 12-digit key of the
	// same name.
	if err := s.SetStaticKeys([]identity.StaticKey{{AccessKeyID: "test", Secret: "other", Account: acct}, {AccessKeyID: "112233445566", Secret: "s", Account: acct}}); err != nil {
		t.Fatal(err)
	}
	if p, res := s.Resolve("test"); p != (identity.Root{Account: acct}) || res != (identity.Static{Secret: "other"}) {
		t.Errorf("Resolve(test) with a static key test = %#v, %#v; want the static key", p, res)
	}
	if p, res := s.Resolve("112233445566"); p != (identity.Root{Account: acct}) || res != (identity.Static{Secret: "s"}) {
		t.Errorf("Resolve(112233445566) with a static key of that name = %#v, %#v; want the static key", p, res)
	}
	// The previous list is replaced as a whole.
	if _, res := s.Resolve("CIKEY0000000000001"); res != (identity.Fallback{}) {
		t.Errorf("a static key no longer configured resolved as %#v; want Fallback", res)
	}
}

func TestSetStaticKeysRefusals(t *testing.T) {
	s := identity.New(ids.NewSeeded(1), aws.DefaultAccount())
	acct := account(t, "444455556666")
	minted := mint(t, s, identity.Root{Account: acct}, identity.LongTerm{})
	good := identity.StaticKey{AccessKeyID: "CIKEY0000000000001", Secret: "ci-secret", Account: acct}
	if err := s.SetStaticKeys([]identity.StaticKey{good}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		keys []identity.StaticKey
		want identity.StaticKeyProblem
	}{
		{"empty secret", []identity.StaticKey{{AccessKeyID: "CIKEY0000000000002", Account: acct}}, identity.StaticKeyNoSecret},
		{"minted key", []identity.StaticKey{{AccessKeyID: minted.AccessKeyID, Secret: "s", Account: acct}}, identity.StaticKeyMinted},
		{"listed twice", []identity.StaticKey{{AccessKeyID: "CIKEY0000000000002", Secret: "a", Account: acct}, {AccessKeyID: "CIKEY0000000000002", Secret: "b", Account: acct}}, identity.StaticKeyListedTwice},
	} {
		err := s.SetStaticKeys(tc.keys)
		keyErr, ok := errors.AsType[*identity.StaticKeyError](err)
		if !ok || keyErr.Problem != tc.want {
			t.Errorf("%s: SetStaticKeys = %v; want a StaticKeyError with problem %d", tc.name, err, tc.want)
			continue
		}
		if strings.Contains(err.Error(), "ci-secret") || strings.Contains(err.Error(), string(minted.SecretAccessKey)) {
			t.Errorf("%s: the error names a secret: %v", tc.name, err)
		}
		// All or nothing: the previous static keys stay.
		if _, res := s.Resolve(string(good.AccessKeyID)); res != (identity.Static{Secret: good.Secret}) {
			t.Errorf("%s: after the refusal the configured key resolved as %#v", tc.name, res)
		}
	}
}

// Static keys come from configuration at every start: a snapshot never
// holds them, and loading one keeps them.
func TestStaticKeysStayOutOfExportAndSurviveImport(t *testing.T) {
	s := identity.New(ids.NewSeeded(1), aws.DefaultAccount())
	acct := account(t, "444455556666")
	empty, err := s.Export()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetStaticKeys([]identity.StaticKey{{AccessKeyID: "CIKEY0000000000001", Secret: "ci-secret", Account: acct}}); err != nil {
		t.Fatal(err)
	}
	exported, err := s.Export()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(exported, empty) {
		t.Errorf("Export with only a static key = %s; want the empty store's %s", exported, empty)
	}
	mint(t, s, identity.Root{Account: acct}, identity.LongTerm{})
	if err := s.Import(empty); err != nil {
		t.Fatal(err)
	}
	if _, res := s.Resolve("CIKEY0000000000001"); res != (identity.Static{Secret: "ci-secret"}) {
		t.Errorf("after Import the static key resolved as %#v; want it kept", res)
	}

	// A snapshot minted elsewhere may hold a key now configured as static.
	// A key is minted or static, never both, so Import refuses the snapshot
	// rather than let the minted entry shadow the configured secret.
	elsewhere := identity.New(ids.NewSeeded(2), aws.DefaultAccount())
	taken := mint(t, elsewhere, identity.Root{Account: acct}, identity.LongTerm{})
	snapshot, err := elsewhere.Export()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetStaticKeys([]identity.StaticKey{{AccessKeyID: taken.AccessKeyID, Secret: "configured", Account: acct}}); err != nil {
		t.Fatal(err)
	}
	err = s.Import(snapshot)
	if importErr, ok := errors.AsType[*identity.ImportError](err); !ok || !strings.Contains(importErr.Error(), string(taken.AccessKeyID)) {
		t.Fatalf("Import of a snapshot minting configured key %s = %v; want an ImportError naming the key", taken.AccessKeyID, err)
	}
	if _, res := s.Resolve(string(taken.AccessKeyID)); res != (identity.Static{Secret: "configured"}) {
		t.Errorf("after the refused Import the configured key resolved as %#v; want the static entry", res)
	}
}

// Minting never hands out a configured key: Resolve checks minted keys
// first, so the minted entry would shadow the configured secret.
func TestMintRefusesAConfiguredKey(t *testing.T) {
	acct := account(t, "123456789012")
	probe := identity.New(ids.NewSeeded(9), aws.DefaultAccount())
	next := mint(t, probe, identity.Root{Account: acct}, identity.LongTerm{})

	s := identity.New(ids.NewSeeded(9), aws.DefaultAccount())
	if err := s.SetStaticKeys([]identity.StaticKey{{AccessKeyID: next.AccessKeyID, Secret: "s", Account: acct}}); err != nil {
		t.Fatal(err)
	}
	_, err := s.MintLongTerm(identity.Root{Account: acct})
	if mintErr, ok := errors.AsType[*identity.MintError](err); !ok || mintErr.Problem != identity.KeyCollision {
		t.Errorf("MintLongTerm drawing a configured key = %v; want a KeyCollision MintError", err)
	}
}

func TestRevokedKeyResolvesAsUnminted(t *testing.T) {
	s := identity.New(ids.NewSeeded(1), aws.DefaultAccount())
	c := mint(t, s, identity.Root{Account: account(t, "123456789012")}, identity.LongTerm{})
	if !s.Revoke(string(c.AccessKeyID)) {
		t.Fatal("Revoke of a minted key reported false")
	}
	if s.Revoke(string(c.AccessKeyID)) {
		t.Error("second Revoke reported true")
	}
	if p, _ := s.Resolve(string(c.AccessKeyID)); p != (identity.Root{Account: aws.DefaultAccount()}) {
		t.Errorf("revoked key resolved to %#v; want the default account root", p)
	}
}

func TestMintRefusals(t *testing.T) {
	s := identity.New(ids.NewSeeded(1), aws.DefaultAccount())
	role := identity.AssumedRole{Account: account(t, "123456789012"), Role: identity.RoleRef{Name: "r"}, Session: "sess", Source: identity.FromAssumeRole{}}
	expiry := time.Date(2026, 10, 8, 13, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name string
		mint func() (identity.Credentials, error)
		want identity.MintProblem
	}{
		{"long-term for a service principal", func() (identity.Credentials, error) {
			return s.MintLongTerm(identity.ServicePrincipal{Name: "sns.amazonaws.com"})
		}, identity.NoAccount},
		{"temporary for an anonymous caller", func() (identity.Credentials, error) {
			return s.MintTemporary(identity.Anonymous{}, time.Time{})
		}, identity.NoAccount},
		{"long-term for an assumed role", func() (identity.Credentials, error) { return s.MintLongTerm(role) }, identity.RoleNeedsTemporary},
		{"temporary for an assumed role with no source", func() (identity.Credentials, error) {
			noSource := role
			noSource.Source = nil
			return s.MintTemporary(noSource, expiry)
		}, identity.InvalidSessionSource},
		{"temporary for an execution environment with a zero function", func() (identity.Credentials, error) {
			zero := role
			zero.Source = identity.FromExecutionEnvironment{Env: "e"}
			return s.MintTemporary(zero, expiry)
		}, identity.InvalidSessionSource},
		{"temporary for an execution environment whose function does not parse back", func() (identity.Credentials, error) {
			colon := role
			colon.Source = identity.FromExecutionEnvironment{Function: aws.ARN{Partition: "aws:x", Service: "lambda", Resource: "function:f"}, Env: "e"}
			return s.MintTemporary(colon, expiry)
		}, identity.InvalidSessionSource},
		{"temporary expiring after year 9999", func() (identity.Credentials, error) {
			return s.MintTemporary(role, time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC))
		}, identity.InvalidExpiry},
		{"temporary expiring in year 9999 in its zone but after it in UTC", func() (identity.Credentials, error) {
			return s.MintTemporary(role, time.Date(9999, 12, 31, 23, 0, 0, 0, time.FixedZone("", -5*3600)))
		}, identity.InvalidExpiry},
		{"temporary with a zero expiry", func() (identity.Credentials, error) {
			return s.MintTemporary(role, time.Time{})
		}, identity.NoExpiry},
		{"long-term for a user with no name", func() (identity.Credentials, error) {
			return s.MintLongTerm(identity.User{Account: account(t, "123456789012"), ID: "AIDA1"})
		}, identity.MissingName},
		{"temporary for a role with no name", func() (identity.Credentials, error) {
			noName := role
			noName.Role.Name = ""
			return s.MintTemporary(noName, expiry)
		}, identity.MissingName},
		{"temporary for a session with no name", func() (identity.Credentials, error) {
			noSession := role
			noSession.Session = ""
			return s.MintTemporary(noSession, expiry)
		}, identity.MissingName},
		{"long-term for a user whose path has no slashes", func() (identity.Credentials, error) {
			return s.MintLongTerm(identity.User{Account: account(t, "123456789012"), Path: "nopath", Name: "Bob"})
		}, identity.MalformedPath},
		{"temporary for a role whose path lacks the closing slash", func() (identity.Credentials, error) {
			badPath := role
			badPath.Role.Path = "/service-role"
			return s.MintTemporary(badPath, expiry)
		}, identity.MalformedPath},
		{`long-term for a user whose path is "//"`, func() (identity.Credentials, error) {
			return s.MintLongTerm(identity.User{Account: account(t, "123456789012"), Path: "//", Name: "Bob"})
		}, identity.MalformedPath},
		{"long-term for a user whose path holds a space", func() (identity.Credentials, error) {
			return s.MintLongTerm(identity.User{Account: account(t, "123456789012"), Path: "/a b/", Name: "Bob"})
		}, identity.MalformedPath},
		{"temporary for a role whose path is not ASCII", func() (identity.Credentials, error) {
			badPath := role
			badPath.Role.Path = "/é/"
			return s.MintTemporary(badPath, expiry)
		}, identity.MalformedPath},
		{"long-term for a user whose name holds a slash", func() (identity.Credentials, error) {
			return s.MintLongTerm(identity.User{Account: account(t, "123456789012"), Name: "a/b"})
		}, identity.MalformedName},
		{"temporary for a role whose name holds a slash", func() (identity.Credentials, error) {
			badName := role
			badName.Role.Name = "a/b"
			return s.MintTemporary(badName, expiry)
		}, identity.MalformedName},
		{"temporary for a one-character session name", func() (identity.Credentials, error) {
			short := role
			short.Session = "s"
			return s.MintTemporary(short, expiry)
		}, identity.MalformedName},
	} {
		_, err := tc.mint()
		var me *identity.MintError
		if !errors.As(err, &me) || me.Problem != tc.want {
			t.Errorf("%s: %v; want MintError %d", tc.name, err, tc.want)
		}
	}
	if out, err := s.Export(); err != nil || !bytes.Contains(out, []byte(`"keys":[]`)) {
		t.Errorf("a refused Mint stored a key: %s", out)
	}
}

func TestRenderMatchesGetCallerIdentityArns(t *testing.T) {
	acct := account(t, "123456789012")
	for _, tc := range []struct {
		p    identity.Principal
		want string
	}{
		{identity.Root{Account: acct}, "arn:aws:iam::123456789012:root"},
		{identity.User{Account: acct, Path: "/", Name: "Alice"}, "arn:aws:iam::123456789012:user/Alice"},
		{identity.User{Account: acct, Name: "Alice"}, "arn:aws:iam::123456789012:user/Alice"},
		{identity.User{Account: acct, Path: "/division_abc/subdivision_xyz/", Name: "Bob"}, "arn:aws:iam::123456789012:user/division_abc/subdivision_xyz/Bob"},
		// The assumed-role ARN carries the role name only, never its path.
		{identity.AssumedRole{Account: acct, Role: identity.RoleRef{Name: "demo", Path: "/teams/"}, Session: "TestAR"}, "arn:aws:sts::123456789012:assumed-role/demo/TestAR"},
		{identity.ServicePrincipal{Name: "sns.amazonaws.com"}, "sns.amazonaws.com"},
		{identity.Anonymous{}, "anonymous"},
	} {
		if got := identity.Render(tc.p); got != tc.want {
			t.Errorf("Render(%#v) = %q, want %q", tc.p, got, tc.want)
		}
	}
}

func TestCredentialsNeverReachLogsOrFormatting(t *testing.T) {
	s := identity.New(ids.NewSeeded(7), aws.DefaultAccount())
	acct := account(t, "123456789012")
	temp := mint(t, s, identity.AssumedRole{Account: acct, Role: identity.RoleRef{Name: "r"}, Session: "sess", Source: identity.FromAssumeRole{}},
		identity.Temporary{Expiry: time.Date(2026, 10, 8, 13, 0, 0, 0, time.UTC)})
	long := mint(t, s, identity.Root{Account: acct}, identity.LongTerm{})

	for _, c := range []identity.Credentials{temp, long} {
		secrets := []string{string(c.SecretAccessKey), sessionToken(c)}
		t.Run("credentials "+string(c.AccessKeyID), func(t *testing.T) {
			assertRedacted(t, c, secrets, string(c.AccessKeyID))
		})
		// A caller logs the Resolution next to the Fallback warning.
		_, res := s.Resolve(string(c.AccessKeyID))
		m, ok := res.(identity.Minted)
		if !ok {
			t.Fatalf("Resolve(%s) = %#v, want Minted", c.AccessKeyID, res)
		}
		t.Run("minted resolution "+string(c.AccessKeyID), func(t *testing.T) { assertRedacted(t, m, secrets, "[REDACTED]") })
	}
	life, ok := temp.Lifetime.(identity.Temporary)
	if !ok {
		t.Fatalf("temporary credentials have lifetime %#v", temp.Lifetime)
	}
	t.Run("temporary lifetime", func(t *testing.T) { assertRedacted(t, life, []string{sessionToken(temp)}, "[REDACTED]") })

	static := identity.StaticKey{AccessKeyID: "CIKEY0000000000001", Secret: "ci-secret-value", Account: acct}
	if err := s.SetStaticKeys([]identity.StaticKey{static}); err != nil {
		t.Fatal(err)
	}
	t.Run("static key", func(t *testing.T) { assertRedacted(t, static, []string{"ci-secret-value"}, "CIKEY0000000000001") })
	_, res := s.Resolve("CIKEY0000000000001")
	staticRes, ok := res.(identity.Static)
	if !ok {
		t.Fatalf("Resolve(static key) = %#v, want Static", res)
	}
	t.Run("static resolution", func(t *testing.T) { assertRedacted(t, staticRes, []string{"ci-secret-value"}, "[REDACTED]") })
	_, res = s.Resolve("112233445566")
	accountRes, ok := res.(identity.AccountKey)
	if !ok {
		t.Fatalf("Resolve(112233445566) = %#v, want AccountKey", res)
	}
	t.Run("account key resolution", func(t *testing.T) { assertRedacted(t, accountRes, nil, "[REDACTED]") })

	// The store holds every minted secret in unexported fields, which fmt
	// reads past the Credentials redaction; kernel.ServiceDeps holds one, so
	// a debug log of the deps must not leak them.
	t.Run("store", func(t *testing.T) {
		secrets := []string{string(temp.SecretAccessKey), sessionToken(temp), string(long.SecretAccessKey)}
		holder := struct{ Identity *identity.Store }{s}
		outputs := render(
			[]slog.Attr{slog.Any("store", s), slog.Any("deps", holder), slog.Any("stores", []*identity.Store{s})},
			[]any{s, holder},
		)
		// Every rendering names the default account.
		checkRedacted(t, outputs, secrets, aws.DefaultAccount().String())
	})
}

// assertRedacted renders v through slog's text and JSON handlers, at the top
// level and held in a struct, a slice and a pointer, and through fmt, and
// fails if any rendering holds one of secrets or lacks marker, the proof that
// v was rendered at all.
func assertRedacted[T any](t *testing.T, v T, secrets []string, marker string) {
	t.Helper()
	outputs := render(
		[]slog.Attr{slog.Any("v", v), slog.Any("v", struct{ V T }{v}), slog.Any("v", []T{v}), slog.Any("v", &v)},
		[]any{v},
	)
	checkRedacted(t, outputs, secrets, marker)
}

// render logs each of attrs through slog's text and JSON handlers and
// formats each of values with the fmt verbs.
func render(attrs []slog.Attr, values []any) []string {
	handlers := []struct {
		name string
		new  func(io.Writer) slog.Handler
	}{
		{"text", func(w io.Writer) slog.Handler { return slog.NewTextHandler(w, nil) }},
		{"json", func(w io.Writer) slog.Handler { return slog.NewJSONHandler(w, nil) }},
	}
	verbs := []string{"%v", "%+v", "%#v", "%s"}
	outputs := make([]string, 0, len(handlers)*len(attrs)+len(verbs)*len(values))
	for _, h := range handlers {
		for _, attr := range attrs {
			var buf bytes.Buffer
			slog.New(h.new(&buf)).Info("m", attr)
			outputs = append(outputs, h.name+" handler: "+buf.String())
		}
	}
	for _, v := range values {
		for _, verb := range verbs {
			outputs = append(outputs, verb+": "+fmt.Sprintf(verb, v))
		}
	}
	return outputs
}

// checkRedacted fails if any output holds one of secrets or lacks marker,
// the proof that the value was rendered at all.
func checkRedacted(t *testing.T, outputs, secrets []string, marker string) {
	t.Helper()
	for _, out := range outputs {
		for _, secret := range secrets {
			if secret != "" && strings.Contains(out, secret) {
				t.Errorf("a secret reached the output: %s", out)
			}
		}
		if !strings.Contains(out, marker) {
			t.Errorf("%q is missing from the output, so the redaction check proves nothing: %s", marker, out)
		}
	}
}

func principalGen(acct aws.AccountID) *rapid.Generator[identity.Principal] {
	name := rapid.StringMatching(`[A-Za-z0-9+=,.@_-]{1,16}`)
	session := rapid.StringMatching(`[A-Za-z0-9+=,.@_-]{2,16}`)
	path := rapid.SampledFrom([]string{"", "/", "/a/", "/a/b/"})
	return rapid.Custom(func(t *rapid.T) identity.Principal {
		switch rapid.IntRange(0, 2).Draw(t, "kind") {
		case 0:
			return identity.Root{Account: acct}
		case 1:
			return identity.User{Account: acct, Path: path.Draw(t, "path"), Name: name.Draw(t, "name"), ID: "AIDA" + name.Draw(t, "id")}
		default:
			var src identity.SessionSource = identity.FromAssumeRole{}
			if rapid.Bool().Draw(t, "env") {
				fn := aws.ARN{Partition: "aws", Service: "lambda", Region: "us-east-1", Account: acct.String(), Resource: "function:" + name.Draw(t, "fn")}
				src = identity.FromExecutionEnvironment{Function: fn, Env: identity.EnvID(name.Draw(t, "envID"))}
			}
			return identity.AssumedRole{
				Account: acct,
				Role:    identity.RoleRef{Name: name.Draw(t, "role"), Path: path.Draw(t, "rolePath"), ID: "AROA" + name.Draw(t, "roleID")},
				Session: session.Draw(t, "session"),
				Source:  src,
			}
		}
	})
}

func TestExportImportRoundTrip(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		acct := account(t, rapid.StringMatching(`[0-9]{12}`).Draw(t, "account"))
		src := identity.New(ids.NewSeeded(rapid.Uint64().Draw(t, "seed")), aws.DefaultAccount())
		n := rapid.IntRange(0, 6).Draw(t, "n")
		minted := make([]identity.Credentials, 0, n)
		principals := make([]identity.Principal, 0, n)
		for i := range n {
			p := principalGen(acct).Draw(t, fmt.Sprintf("principal%d", i))
			var life identity.Lifetime = identity.LongTerm{}
			if _, role := p.(identity.AssumedRole); role || rapid.Bool().Draw(t, "temporary") {
				// Up to 9999-12-31, the last instant RFC 3339 can carry.
				life = identity.Temporary{Expiry: time.Unix(rapid.Int64Range(0, 253402300799).Draw(t, "expiry"), 0).UTC()}
			}
			minted = append(minted, mint(t, src, p, life))
			principals = append(principals, p)
		}
		out, err := src.Export()
		if err != nil {
			t.Fatalf("Export: %v", err)
		}
		dst := identity.New(ids.NewSeeded(0), aws.DefaultAccount())
		if err := dst.Import(out); err != nil {
			t.Fatalf("Import: %v", err)
		}
		for i, c := range minted {
			p, res := dst.Resolve(string(c.AccessKeyID))
			m, ok := res.(identity.Minted)
			if p != principals[i] || !ok || m.Secret != c.SecretAccessKey || m.Lifetime != c.Lifetime {
				t.Fatalf("after Import, Resolve(%s) = %#v, %#v; want %#v with its secret", c.AccessKeyID, p, res, principals[i])
			}
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

// Every persisted field is lowerCamel (ARCHITECTURE.md 13), and the session
// token lives in the temporary lifetime it belongs to.
func TestExportFormat(t *testing.T) {
	s := identity.New(ids.NewSeeded(1), aws.DefaultAccount())
	expiry := time.Date(2026, 10, 8, 13, 0, 0, 0, time.UTC)
	c := mint(t, s, identity.AssumedRole{
		Account: account(t, "123456789012"), Role: identity.RoleRef{Name: "demo", Path: "/teams/", ID: "AROA1"},
		Session: "sess", Source: identity.FromAssumeRole{},
	}, identity.Temporary{Expiry: expiry})
	out, err := s.Export()
	if err != nil {
		t.Fatal(err)
	}
	want := `{"version":1,"keys":[{"accessKeyId":"` + string(c.AccessKeyID) + `","secretAccessKey":"` + string(c.SecretAccessKey) +
		`","lifetime":{"kind":"temporary","expiry":"2026-10-08T13:00:00Z","sessionToken":"` + sessionToken(c) +
		`"},"principal":{"kind":"assumedRole","account":"123456789012","role":{"name":"demo","path":"/teams/","id":"AROA1"},` +
		`"session":"sess","source":{"kind":"assumeRole"}}}]}`
	if string(out) != want {
		t.Errorf("Export =\n%s\nwant\n%s", out, want)
	}
}

// An expiry in the host's zone, as clock.Wall gives, is held and written in
// UTC, so the same identity state exports the same bytes on every host
// (ADR 0009). A document written with an offset imports in UTC too.
func TestTemporaryExpiryIsUTC(t *testing.T) {
	const want = `"expiry":"2026-10-08T13:00:00Z"`
	role := identity.AssumedRole{Account: account(t, "123456789012"), Role: identity.RoleRef{Name: "r"}, Session: "sess", Source: identity.FromAssumeRole{}}
	s := identity.New(ids.NewSeeded(1), aws.DefaultAccount())
	local := time.Date(2026, 10, 8, 15, 0, 0, 0, time.FixedZone("CEST", 2*60*60))
	c := mint(t, s, role, identity.Temporary{Expiry: local})
	if tmp, ok := c.Lifetime.(identity.Temporary); !ok || tmp.Expiry.Location() != time.UTC || !tmp.Expiry.Equal(local) {
		t.Errorf("MintTemporary(%s) lifetime = %#v; want the same instant in UTC", local, c.Lifetime)
	}
	out, err := s.Export()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out, []byte(want)) {
		t.Errorf("Export = %s; want %s", out, want)
	}
	offset := bytes.Replace(out, []byte(want), []byte(`"expiry":"2026-10-08T15:00:00+02:00"`), 1)
	dst := identity.New(ids.NewSeeded(0), aws.DefaultAccount())
	if err := dst.Import(offset); err != nil {
		t.Fatalf("Import: %v", err)
	}
	if again, err := dst.Export(); err != nil || !bytes.Equal(again, out) {
		t.Errorf("Export after importing an offset expiry = %s, %v; want %s", again, err, out)
	}
}

func TestImportIsAllOrNothing(t *testing.T) {
	s := identity.New(ids.NewSeeded(1), aws.DefaultAccount())
	c := mint(t, s, identity.Root{Account: account(t, "123456789012")}, identity.LongTerm{})
	keys := ids.NewSeeded(9)
	akia, err := keys.AccessKeyID("123456789012")
	if err != nil {
		t.Fatal(err)
	}
	asia, err := keys.TemporaryAccessKeyID("123456789012")
	if err != nil {
		t.Fatal(err)
	}
	otherAccount, err := keys.AccessKeyID("210987654321")
	if err != nil {
		t.Fatal(err)
	}
	// record is one persisted key with the given extra fields and principal.
	record := func(key ids.AccessKeyID, extra, principal string) string {
		return `{"accessKeyId":"` + string(key) + `","secretAccessKey":"s",` + extra + `"principal":` + principal + `}`
	}
	doc := func(records ...string) string { return `{"version":1,"keys":[` + strings.Join(records, ",") + `]}` }
	const (
		longTerm  = `"lifetime":{"kind":"longTerm"},`
		temporary = `"lifetime":{"kind":"temporary","expiry":"2026-10-08T13:00:00Z","sessionToken":"t"},`
		root      = `{"kind":"root","account":"123456789012"}`
		user      = `{"kind":"user","account":"123456789012","path":"/","name":"Alice","id":"AIDA1"}`
		role      = `{"kind":"assumedRole","account":"123456789012","role":{"name":"r","path":"/","id":"AROA1"},"session":"sess","source":{"kind":"assumeRole"}}`
	)
	// The valid documents these cases break must import, or the cases would
	// fail for the wrong reason.
	for _, data := range []string{doc(record(akia, longTerm, root)), doc(record(akia, longTerm, user)), doc(record(asia, temporary, role))} {
		if err := identity.New(ids.NewSeeded(1), aws.DefaultAccount()).Import([]byte(data)); err != nil {
			t.Fatalf("precondition: Import(%s) = %v", data, err)
		}
	}
	for name, data := range map[string]string{
		"not JSON":                      `{`,
		"other version":                 `{"version":2,"keys":[]}`,
		"unknown field":                 `{"version":1,"keys":[],"extra":1}`,
		"trailing document":             doc() + `{}`,
		"trailing garbage":              doc() + `x`,
		"unknown principal":             doc(record(akia, longTerm, `{"kind":"group","account":"123456789012"}`)),
		"session token on long-term":    doc(record(akia, `"lifetime":{"kind":"longTerm","sessionToken":"t"},`, root)),
		"expiry on long-term":           doc(record(akia, `"lifetime":{"kind":"longTerm","expiry":"2026-10-08T13:00:00Z"},`, root)),
		"temporary without token":       doc(record(asia, `"lifetime":{"kind":"temporary","expiry":"2026-10-08T13:00:00Z"},`, role)),
		"temporary without expiry":      doc(record(asia, `"lifetime":{"kind":"temporary","sessionToken":"t"},`, role)),
		"temporary with zero expiry":    doc(record(asia, `"lifetime":{"kind":"temporary","expiry":"0001-01-01T00:00:00Z","sessionToken":"t"},`, role)),
		"expiry before year 0 in UTC":   doc(record(asia, `"lifetime":{"kind":"temporary","expiry":"0000-01-01T00:00:00+01:00","sessionToken":"t"},`, role)),
		"user without a name":           doc(record(akia, longTerm, strings.Replace(user, `"name":"Alice",`, ``, 1))),
		"user with a malformed path":    doc(record(akia, longTerm, strings.Replace(user, `"path":"/"`, `"path":"nopath"`, 1))),
		"role with a malformed path":    doc(record(asia, temporary, strings.Replace(role, `"path":"/"`, `"path":"/service-role"`, 1))),
		`user with path "//"`:           doc(record(akia, longTerm, strings.Replace(user, `"path":"/"`, `"path":"//"`, 1))),
		"user with a space in its path": doc(record(akia, longTerm, strings.Replace(user, `"path":"/"`, `"path":"/a b/"`, 1))),
		"user with a slash in its name": doc(record(akia, longTerm, strings.Replace(user, `"name":"Alice"`, `"name":"a/b"`, 1))),
		"role with a slash in its name": doc(record(asia, temporary, strings.Replace(role, `"name":"r"`, `"name":"a/b"`, 1))),
		"one-character session name":    doc(record(asia, temporary, strings.Replace(role, `"session":"sess"`, `"session":"s"`, 1))),
		"assumed role without a name":   doc(record(asia, temporary, strings.Replace(role, `"name":"r",`, ``, 1))),
		"assumed role without session":  doc(record(asia, temporary, strings.Replace(role, `"session":"sess",`, ``, 1))),
		"token outside the lifetime":    doc(record(asia, `"sessionToken":"t",`+temporary, role)),
		"bad account":                   doc(record(akia, longTerm, `{"kind":"root","account":"12"}`)),
		"duplicate key":                 doc(record(akia, longTerm, root), record(akia, longTerm, root)),
		"long-term assumed role":        doc(record(akia, longTerm, role)),
		"ASIA key with long-term life":  doc(record(asia, longTerm, root)),
		"AKIA key with temporary life":  doc(record(akia, temporary, root)),
		"key of another account":        doc(record(otherAccount, longTerm, root)),
		"undecodable key":               doc(record("AKIAX", longTerm, root)),
		"root with a name":              doc(record(akia, longTerm, `{"kind":"root","account":"123456789012","name":"Alice"}`)),
		"root with a path and an id":    doc(record(akia, longTerm, `{"kind":"root","account":"123456789012","path":"/","id":"AIDA1"}`)),
		"root with a role":              doc(record(akia, longTerm, `{"kind":"root","account":"123456789012","role":{"name":"r"}}`)),
		"root with a session":           doc(record(akia, longTerm, `{"kind":"root","account":"123456789012","session":"s"}`)),
		"root with a source":            doc(record(akia, longTerm, `{"kind":"root","account":"123456789012","source":{"kind":"assumeRole"}}`)),
		"user with a role":              doc(record(akia, longTerm, strings.Replace(user, `}`, `,"role":{"name":"r"}}`, 1))),
		"user with a session":           doc(record(akia, longTerm, strings.Replace(user, `}`, `,"session":"s"}`, 1))),
		"user with a source":            doc(record(akia, longTerm, strings.Replace(user, `}`, `,"source":{"kind":"assumeRole"}}`, 1))),
		"assumed role with a name":      doc(record(asia, temporary, strings.Replace(role, `"session"`, `"name":"Alice","session"`, 1))),
		"assumed role with a path":      doc(record(asia, temporary, strings.Replace(role, `"session"`, `"path":"/","session"`, 1))),
		"AssumeRole with a function":    doc(record(asia, temporary, strings.Replace(role, `{"kind":"assumeRole"}`, `{"kind":"assumeRole","function":"arn:aws:lambda:us-east-1:123456789012:function:f"}`, 1))),
		"AssumeRole with an env":        doc(record(asia, temporary, strings.Replace(role, `{"kind":"assumeRole"}`, `{"kind":"assumeRole","env":"e"}`, 1))),
	} {
		var ie *identity.ImportError
		if err := s.Import([]byte(data)); !errors.As(err, &ie) {
			t.Errorf("%s: Import = %v, want *ImportError", name, err)
		}
		if _, res := s.Resolve(string(c.AccessKeyID)); res == (identity.Fallback{}) {
			t.Fatalf("%s: a failed Import dropped the existing keys", name)
		}
	}
}

// An ImportError keeps the error behind it, so a caller can inspect a decode
// failure or a record's parse error with errors.As.
func TestImportErrorUnwrapsItsCause(t *testing.T) {
	s := identity.New(ids.NewSeeded(1), aws.DefaultAccount())
	var syntax *json.SyntaxError
	if err := s.Import([]byte(`x`)); !errors.As(err, &syntax) {
		t.Errorf("Import of a non-JSON document = %v; want it to unwrap to *json.SyntaxError", err)
	}
	asia, err := ids.NewSeeded(9).TemporaryAccessKeyID("123456789012")
	if err != nil {
		t.Fatal(err)
	}
	const role = `{"kind":"assumedRole","account":"123456789012","role":{"name":"r","path":"/","id":"AROA1"},"session":"sess",` +
		`"source":{"kind":"executionEnvironment","function":"not-an-arn","env":"e"}}`
	doc := `{"version":1,"keys":[{"accessKeyId":"` + string(asia) + `","secretAccessKey":"s",` +
		`"lifetime":{"kind":"temporary","expiry":"2026-10-08T13:00:00Z","sessionToken":"t"},"principal":` + role + `}]}`
	var arnErr *aws.ARNError
	err = s.Import([]byte(doc))
	var ie *identity.ImportError
	if !errors.As(err, &ie) || !errors.As(err, &arnErr) || !strings.Contains(ie.Detail, "key 0") {
		t.Errorf("Import of a record with a bad function ARN = %#v; want an ImportError naming key 0 that unwraps to *aws.ARNError", err)
	}
}

func TestConcurrentMintAndResolve(t *testing.T) {
	s := identity.New(ids.NewRandom(), aws.DefaultAccount())
	acct := account(t, "123456789012")
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 50 {
				c, err := s.MintLongTerm(identity.Root{Account: acct})
				if err != nil {
					t.Error(err)
					return
				}
				if p, _ := s.Resolve(string(c.AccessKeyID)); p != (identity.Root{Account: acct}) {
					t.Errorf("Resolve(%s) = %#v", c.AccessKeyID, p)
				}
				s.Resolve("AKIAIOSFODNN7EXAMPLE")
			}
		})
	}
	wg.Wait()
}
