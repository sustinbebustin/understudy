package identity

import (
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/ids"
)

// Lifetime says how long minted credentials live.
//
//sumtype:decl
type Lifetime interface{ isLifetime() }

// LongTerm is an AKIA key with no session token and no expiry, as IAM
// CreateAccessKey issues.
type LongTerm struct{}

// Temporary is an ASIA key with a session token, as STS issues. Expiry is
// wall time, because SDKs compare it with their own clocks
// (iam-sts-kms.md implication 8); the store holds it in UTC, so a snapshot
// does not depend on the host's zone. Temporary redacts the token in slog
// output, JSON and fmt formatting.
type Temporary struct {
	Expiry       time.Time
	SessionToken ids.SessionToken
}

func (LongTerm) isLifetime()  {}
func (Temporary) isLifetime() {}

// Credentials are an access key the store minted. Credentials redact the
// secret and the token in slog output, JSON and fmt formatting.
type Credentials struct {
	AccessKeyID     ids.AccessKeyID
	SecretAccessKey ids.SecretAccessKey
	Lifetime        Lifetime
}

// Resolution says how Resolve chose a principal.
//
//sumtype:decl
type Resolution interface{ isResolution() }

// Minted is a key the store minted. Secret verifies the request signature,
// and a Temporary Lifetime's token the X-Amz-Security-Token, when SigV4
// verification is on. Minted redacts both in slog output, JSON and fmt
// formatting.
type Minted struct {
	Secret   ids.SecretAccessKey
	Lifetime Lifetime
}

// Static is a key from configuration (SetStaticKeys), or the dummy key
// DummyAccessKeyID, read as an account's root. Secret verifies the request
// signature when SigV4 verification is on. Static redacts it in slog
// output, JSON and fmt formatting.
type Static struct{ Secret ids.SecretAccessKey }

// AccountKey is a 12-digit key, read as that account's root. Secret is
// DummySecret, which verifies its signature when SigV4 verification is on.
// AccountKey redacts it in slog output, JSON and fmt formatting.
type AccountKey struct{ Secret ids.SecretAccessKey }

// Fallback is any other key, read as the default account's root. It has no
// secret, so SigV4 verification refuses it as an unknown key. Warn is true
// the first time the store sees a real-looking AKIA or ASIA key it did not
// mint: such a key would decode to a foreign account, which understudy
// refuses to do (ADR 0012), so the caller logs one warning per key.
type Fallback struct{ Warn bool }

func (Minted) isResolution()     {}
func (Static) isResolution()     {}
func (AccountKey) isResolution() {}
func (Fallback) isResolution()   {}

// SecretOf returns the secret that verifies signatures by a key resolved as
// r, and false for Fallback, which has none: SigV4 verification refuses
// such a key as unknown.
func SecretOf(r Resolution) (ids.SecretAccessKey, bool) {
	switch r := r.(type) {
	case Minted:
		return r.Secret, true
	case Static:
		return r.Secret, true
	case AccountKey:
		return r.Secret, true
	case Fallback:
	}
	return "", false
}

// The dummy credentials every quickstart uses: AWS_ACCESS_KEY_ID=test and
// AWS_SECRET_ACCESS_KEY=test. The key is the default account's root, and
// DummySecret is also the secret of every 12-digit account key, so both
// keep working when SigV4 verification is on (ADR 0012 pre-tag review
// amendment). They are no secret: anyone may sign with them.
const (
	DummyAccessKeyID ids.AccessKeyID     = "test"
	DummySecret      ids.SecretAccessKey = "test"
)

// maxWarned caps the real-looking keys the store remembers having warned
// about, so a client cycling random keys cannot grow memory without bound.
// Past the cap, keys not yet seen resolve with no warning.
const maxWarned = 10_000

// Store maps access keys to principals and mints new keys. It is safe for
// concurrent use; its lock is a leaf lock.
type Store struct {
	ids            *ids.Source
	defaultAccount aws.AccountID

	mu   sync.Mutex
	keys map[ids.AccessKeyID]entry
	// static holds the keys from configuration, which Export never writes
	// and Import never replaces.
	static map[ids.AccessKeyID]StaticKey
	// warned holds the real-looking unminted keys already reported, at most
	// maxWarned of them.
	warned map[string]struct{}
}

type entry struct {
	principal Principal
	creds     Credentials
}

// New returns an empty store that mints from src and resolves unknown keys
// to defaultAccount.
func New(src *ids.Source, defaultAccount aws.AccountID) *Store {
	return &Store{
		ids:            src,
		defaultAccount: defaultAccount,
		keys:           map[ids.AccessKeyID]entry{},
		static:         map[ids.AccessKeyID]StaticKey{},
		warned:         map[string]struct{}{},
	}
}

// MintLongTerm issues an AKIA key for p, as IAM CreateAccessKey does. The key
// encodes p's account (iam-sts-kms.md 2.2), so the account decodes in other
// tools too. An assumed role, a service principal and an anonymous caller
// take no long-term key.
func (s *Store) MintLongTerm(p Principal) (Credentials, error) {
	account, err := mintable(p)
	if err != nil {
		return Credentials{}, err
	}
	if _, isRole := p.(AssumedRole); isRole {
		return Credentials{}, &MintError{Principal: Render(p), Problem: RoleNeedsTemporary}
	}
	// Draw before taking mu: the ids streams hold leaf locks of their own,
	// and a leaf lock never calls out (ARCHITECTURE.md 8 rule 2).
	key, err := s.ids.AccessKeyID(account.String())
	if err != nil {
		return Credentials{}, fmt.Errorf("identity: mint a key for %s: %w", Render(p), err)
	}
	return s.store(p, Credentials{AccessKeyID: key, SecretAccessKey: s.ids.SecretAccessKey(), Lifetime: LongTerm{}})
}

// MintTemporary issues an ASIA key and a session token for p that expire at
// expiry, as STS does. The key encodes p's account as MintLongTerm's does. A
// service principal and an anonymous caller take none. The credentials
// carry expiry in UTC.
func (s *Store) MintTemporary(p Principal, expiry time.Time) (Credentials, error) {
	account, err := mintable(p)
	if err != nil {
		return Credentials{}, err
	}
	if !restorableSource(p) {
		return Credentials{}, &MintError{Principal: Render(p), Problem: InvalidSessionSource}
	}
	utc, problem, ok := sessionExpiry(expiry)
	if !ok {
		return Credentials{}, &MintError{Principal: Render(p), Problem: problem}
	}
	key, err := s.ids.TemporaryAccessKeyID(account.String())
	if err != nil {
		return Credentials{}, fmt.Errorf("identity: mint a key for %s: %w", Render(p), err)
	}
	return s.store(p, Credentials{
		AccessKeyID:     key,
		SecretAccessKey: s.ids.SecretAccessKey(),
		Lifetime:        Temporary{Expiry: utc, SessionToken: s.ids.SessionToken()},
	})
}

// Resolve maps an access key to the principal making a call (ADR 0012), in
// order: a key the store minted is its principal (Minted); a static key is
// its account's root (Static); the dummy key DummyAccessKeyID is the
// default account's root (Static with DummySecret); a 12-digit key is that
// account's root (AccountKey with DummySecret); anything else, including
// real-looking keys and AWS's documentation keys, is the default account's
// root (Fallback, with no secret) and is never decoded.
func (s *Store) Resolve(key string) (Principal, Resolution) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.keys[ids.AccessKeyID(key)]; ok {
		return e.principal, Minted{Secret: e.creds.SecretAccessKey, Lifetime: e.creds.Lifetime}
	}
	if k, ok := s.static[ids.AccessKeyID(key)]; ok {
		return Root{Account: k.Account}, Static{Secret: k.Secret}
	}
	if ids.AccessKeyID(key) == DummyAccessKeyID {
		return Root{Account: s.defaultAccount}, Static{Secret: DummySecret}
	}
	if account, err := aws.ParseAccountID(key); err == nil {
		return Root{Account: account}, AccountKey{Secret: DummySecret}
	}
	warn := false
	if _, decodes := ids.DecodeAccount(key); decodes {
		if _, seen := s.warned[key]; !seen && len(s.warned) < maxWarned {
			s.warned[key] = struct{}{}
			warn = true
		}
	}
	return Root{Account: s.defaultAccount}, Fallback{Warn: warn}
}

// Revoke forgets a minted key and reports whether the store held it. A
// revoked key resolves as an unminted one from then on. Like Resolve, it
// takes the key as a request names it.
func (s *Store) Revoke(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.keys[ids.AccessKeyID(key)]
	delete(s.keys, ids.AccessKeyID(key))
	return ok
}

// store records freshly drawn credentials for p.
func (s *Store) store(p Principal, creds Credentials) (Credentials, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, minted := s.keys[creds.AccessKeyID]
	if _, static := s.static[creds.AccessKeyID]; minted || static {
		return Credentials{}, &MintError{Principal: Render(p), Problem: KeyCollision}
	}
	s.keys[creds.AccessKeyID] = entry{principal: p, creds: creds}
	return creds, nil
}

// sortedKeys returns the minted keys in ids.CompareAccessKeys order, which
// does not depend on the account a key encodes; the caller holds mu.
func (s *Store) sortedKeys() []ids.AccessKeyID {
	return slices.SortedFunc(maps.Keys(s.keys), ids.CompareAccessKeys)
}

// mintable returns the account p's key encodes, or a MintError for a
// principal that belongs to none or that wellFormed refuses.
func mintable(p Principal) (aws.AccountID, error) {
	account, ok := AccountOf(p)
	if !ok {
		return aws.AccountID{}, &MintError{Principal: Render(p), Problem: NoAccount}
	}
	if problem, ok := wellFormed(p); !ok {
		return aws.AccountID{}, &MintError{Principal: Render(p), Problem: problem}
	}
	return account, nil
}

// wellFormed reports whether p has every name its ARN shows, and names and
// paths IAM and STS accept (the rules in package aws, which the access
// recorder also applies). Minting and Import share it, so the store holds
// the same principals however they arrive, and only ones the access
// recorder can attribute calls to.
func wellFormed(p Principal) (MintProblem, bool) {
	switch p := p.(type) {
	case User:
		if p.Name == "" {
			return MissingName, false
		}
		if !aws.ValidIAMName(p.Name) {
			return MalformedName, false
		}
		if !validPath(p.Path) {
			return MalformedPath, false
		}
	case AssumedRole:
		if p.Role.Name == "" || p.Session == "" {
			return MissingName, false
		}
		if !aws.ValidIAMName(p.Role.Name) || !aws.ValidRoleSessionName(p.Session) {
			return MalformedName, false
		}
		if !validPath(p.Role.Path) {
			return MalformedPath, false
		}
	case Root, ServicePrincipal, Anonymous:
	}
	return 0, true
}

// validPath reports whether path is empty, which means "/", or an IAM path.
func validPath(path string) bool {
	return path == "" || aws.ValidIAMPath(path)
}

// restorableSource reports whether p, if an assumed role, has a session
// source that survives Export and Import, so a snapshot of the store always
// loads.
func restorableSource(p Principal) bool {
	role, ok := p.(AssumedRole)
	if !ok {
		return true
	}
	switch src := role.Source.(type) {
	case nil:
		return false
	case FromExecutionEnvironment:
		back, err := aws.ParseARN(src.Function.String())
		return err == nil && back == src.Function
	case FromAssumeRole:
	}
	return true
}

// sessionExpiry returns expiry in UTC, the form the store holds and Export
// writes, or the problem that keeps it from being a session's expiry: zero,
// or without an RFC 3339 form in UTC, which would make every later Export
// fail. MintTemporary and Import share it, so the store holds the same
// expiries however they arrive.
func sessionExpiry(expiry time.Time) (time.Time, MintProblem, bool) {
	if expiry.IsZero() {
		return time.Time{}, NoExpiry, false
	}
	utc := expiry.UTC()
	if _, err := utc.MarshalText(); err != nil {
		return time.Time{}, InvalidExpiry, false
	}
	return utc, 0, true
}
