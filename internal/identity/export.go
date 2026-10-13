package identity

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/ids"
)

// StateVersion is the version of the Export format; Import refuses others.
const StateVersion = 1

// The persisted form. Sums use the tagged-union shape of ARCHITECTURE.md 13,
// {"kind": "<variant>", ...fields}; this package may not import jsonx, so it
// writes the shape with encoding/json.
type (
	stateDoc struct {
		Version int         `json:"version"`
		Keys    []keyRecord `json:"keys"`
	}
	keyRecord struct {
		AccessKeyID     ids.AccessKeyID     `json:"accessKeyId"`
		SecretAccessKey ids.SecretAccessKey `json:"secretAccessKey"`
		Lifetime        lifetimeRecord      `json:"lifetime"`
		Principal       principalRecord     `json:"principal"`
	}
	lifetimeRecord struct {
		Kind         string           `json:"kind"`
		Expiry       *time.Time       `json:"expiry,omitempty"`
		SessionToken ids.SessionToken `json:"sessionToken,omitempty"`
	}
	principalRecord struct {
		Kind    string        `json:"kind"`
		Account aws.AccountID `json:"account,omitzero"`
		Path    string        `json:"path,omitempty"`
		Name    string        `json:"name,omitempty"`
		ID      string        `json:"id,omitempty"`
		Role    *roleRecord   `json:"role,omitempty"`
		Session string        `json:"session,omitempty"`
		Source  *sourceRecord `json:"source,omitempty"`
	}
	// roleRecord is RoleRef's persisted form, kept apart so the format does
	// not follow the domain type's field names.
	roleRecord struct {
		Name string `json:"name"`
		Path string `json:"path,omitempty"`
		ID   string `json:"id,omitempty"`
	}
	sourceRecord struct {
		Kind     string `json:"kind"`
		Function string `json:"function,omitempty"`
		Env      EnvID  `json:"env,omitempty"`
	}
)

const (
	kindLongTerm    = "longTerm"
	kindTemporary   = "temporary"
	kindRoot        = "root"
	kindUser        = "user"
	kindAssumedRole = "assumedRole"
	kindAssumeRole  = "assumeRole"
	kindExecEnv     = "executionEnvironment"

	longTermPrefix  = "AKIA"
	temporaryPrefix = "ASIA"
)

// Export returns every minted key and its principal as deterministic JSON,
// sorted by ids.CompareAccessKeys, for a snapshot section: the order does
// not depend on the account a key encodes, so runs under different accounts
// list their keys alike. It holds secrets.
func (s *Store) Export() ([]byte, error) {
	s.mu.Lock()
	doc := stateDoc{Version: StateVersion, Keys: make([]keyRecord, 0, len(s.keys))}
	for _, k := range s.sortedKeys() {
		e := s.keys[k]
		doc.Keys = append(doc.Keys, keyRecord{
			AccessKeyID:     e.creds.AccessKeyID,
			SecretAccessKey: e.creds.SecretAccessKey,
			Lifetime:        exportLifetime(e.creds.Lifetime),
			Principal:       exportPrincipal(e.principal),
		})
	}
	s.mu.Unlock()
	b, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("identity: export %d keys: %w", len(doc.Keys), err)
	}
	return b, nil
}

// Import replaces every minted key with those in data, as Export wrote them.
// It is all or nothing: on error the store keeps its keys. A key is minted
// or static, never both, so data minting a configured static key is
// refused: the minted entry would shadow the configured secret.
func (s *Store) Import(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var doc stateDoc
	if err := dec.Decode(&doc); err != nil {
		return &ImportError{Detail: "the document does not decode", Cause: err}
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return &ImportError{Detail: "data follows the JSON document"}
	}
	if doc.Version != StateVersion {
		return &ImportError{Detail: fmt.Sprintf("format version %d; this binary reads version %d", doc.Version, StateVersion)}
	}
	keys := make(map[ids.AccessKeyID]entry, len(doc.Keys))
	for i := range doc.Keys {
		key := doc.Keys[i].AccessKeyID
		e, err := importKey(&doc.Keys[i])
		if err != nil {
			return &ImportError{Detail: fmt.Sprintf("key %d (%s)", i, key), Cause: err}
		}
		if _, dup := keys[key]; dup {
			return &ImportError{Detail: fmt.Sprintf("key %d (%s) appears twice", i, key)}
		}
		keys[key] = e
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, key := range slices.SortedFunc(maps.Keys(keys), ids.CompareAccessKeys) {
		if _, static := s.static[key]; static {
			return &ImportError{Detail: fmt.Sprintf("key %s is configured as a static key (accounts.keys) and also minted in this state; remove that accounts.keys entry or load state that does not hold the key", key)}
		}
	}
	s.keys = keys
	return nil
}

// ImportError reports identity state Import could not read. The store kept
// the keys it held before the call.
type ImportError struct {
	// Detail says where the document is at fault: the document itself or
	// one key record.
	Detail string
	// Cause is the decode or record error behind Detail, if any.
	Cause error
}

func (e *ImportError) Error() string {
	msg := "identity: cannot import minted keys: " + e.Detail
	if e.Cause != nil {
		msg += ": " + e.Cause.Error()
	}
	return msg + "; the store kept its previous keys"
}

// Unwrap returns Cause, so errors.As reaches the decode or parse error.
func (e *ImportError) Unwrap() error { return e.Cause }

func exportLifetime(l Lifetime) lifetimeRecord {
	switch l := l.(type) {
	case LongTerm:
		return lifetimeRecord{Kind: kindLongTerm}
	case Temporary:
		return lifetimeRecord{Kind: kindTemporary, Expiry: &l.Expiry, SessionToken: l.SessionToken}
	}
	return lifetimeRecord{}
}

func exportPrincipal(p Principal) principalRecord {
	switch p := p.(type) {
	case Root:
		return principalRecord{Kind: kindRoot, Account: p.Account}
	case User:
		return principalRecord{Kind: kindUser, Account: p.Account, Path: p.Path, Name: p.Name, ID: p.ID}
	case AssumedRole:
		return principalRecord{
			Kind: kindAssumedRole, Account: p.Account, Session: p.Session,
			Role:   &roleRecord{Name: p.Role.Name, Path: p.Role.Path, ID: p.Role.ID},
			Source: exportSource(p.Source),
		}
	case ServicePrincipal, Anonymous:
		// The mint methods refuse these, so the store never holds one.
	}
	return principalRecord{}
}

func exportSource(src SessionSource) *sourceRecord {
	switch src := src.(type) {
	case FromAssumeRole:
		return &sourceRecord{Kind: kindAssumeRole}
	case FromExecutionEnvironment:
		return &sourceRecord{Kind: kindExecEnv, Function: src.Function.String(), Env: src.Env}
	}
	return &sourceRecord{}
}

// importKey admits only what the mint methods can produce: an AKIA key for
// LongTerm and an ASIA key for Temporary, encoding the principal's account,
// Temporary credentials for an assumed role, and the principals and
// expiries the mint methods accept.
func importKey(r *keyRecord) (entry, error) {
	life, err := importLifetime(r.Lifetime)
	if err != nil {
		return entry{}, err
	}
	_, temporary := life.(Temporary)
	prefix := longTermPrefix
	if temporary {
		prefix = temporaryPrefix
	}
	if !strings.HasPrefix(string(r.AccessKeyID), prefix) {
		return entry{}, fmt.Errorf("a %s key must start with %s", r.Lifetime.Kind, prefix)
	}
	p, err := importPrincipal(r.Principal)
	if err != nil {
		return entry{}, err
	}
	if problem, ok := wellFormed(p); !ok {
		return entry{}, fmt.Errorf("the %s principal is refused: %s", r.Principal.Kind, problem.reason())
	}
	if _, role := p.(AssumedRole); role && !temporary {
		return entry{}, fmt.Errorf("an %s principal takes only %s credentials", kindAssumedRole, kindTemporary)
	}
	account, _ := AccountOf(p)
	if got, ok := ids.DecodeAccount(string(r.AccessKeyID)); !ok || got != account.String() {
		return entry{}, fmt.Errorf("the key does not encode the principal's account %s", account)
	}
	if r.SecretAccessKey == "" {
		return entry{}, errors.New("a secret access key is required")
	}
	return entry{principal: p, creds: Credentials{
		AccessKeyID: r.AccessKeyID, SecretAccessKey: r.SecretAccessKey, Lifetime: life,
	}}, nil
}

func importLifetime(r lifetimeRecord) (Lifetime, error) {
	switch r.Kind {
	case kindLongTerm:
		if err := strayFields("lifetime", r.Kind, field{"expiry", r.Expiry != nil}, field{"sessionToken", r.SessionToken != ""}); err != nil {
			return nil, err
		}
		return LongTerm{}, nil
	case kindTemporary:
		if r.Expiry == nil || r.SessionToken == "" {
			return nil, fmt.Errorf("a %s lifetime needs an expiry and a sessionToken", kindTemporary)
		}
		expiry, problem, ok := sessionExpiry(*r.Expiry)
		if !ok {
			return nil, fmt.Errorf("a %s lifetime's expiry is refused: %s", kindTemporary, problem.reason())
		}
		return Temporary{Expiry: expiry, SessionToken: r.SessionToken}, nil
	}
	return nil, fmt.Errorf("lifetime kind %q is not %s or %s", r.Kind, kindLongTerm, kindTemporary)
}

func importPrincipal(r principalRecord) (Principal, error) {
	if r.Account.IsZero() {
		return nil, fmt.Errorf("principal of kind %q has no account", r.Kind)
	}
	path, name, id := field{"path", r.Path != ""}, field{"name", r.Name != ""}, field{"id", r.ID != ""}
	role, session, source := field{"role", r.Role != nil}, field{"session", r.Session != ""}, field{"source", r.Source != nil}
	switch r.Kind {
	case kindRoot:
		if err := strayFields("principal", r.Kind, path, name, id, role, session, source); err != nil {
			return nil, err
		}
		return Root{Account: r.Account}, nil
	case kindUser:
		if err := strayFields("principal", r.Kind, role, session, source); err != nil {
			return nil, err
		}
		return User{Account: r.Account, Path: r.Path, Name: r.Name, ID: r.ID}, nil
	case kindAssumedRole:
		if err := strayFields("principal", r.Kind, path, name, id); err != nil {
			return nil, err
		}
		if r.Role == nil || r.Source == nil {
			return nil, fmt.Errorf("an %s principal needs a role and a source", kindAssumedRole)
		}
		src, err := importSource(*r.Source)
		if err != nil {
			return nil, err
		}
		return AssumedRole{
			Account: r.Account, Role: RoleRef{Name: r.Role.Name, Path: r.Role.Path, ID: r.Role.ID},
			Session: r.Session, Source: src,
		}, nil
	}
	return nil, fmt.Errorf("principal kind %q is not %s, %s or %s", r.Kind, kindRoot, kindUser, kindAssumedRole)
}

func importSource(r sourceRecord) (SessionSource, error) {
	switch r.Kind {
	case kindAssumeRole:
		if err := strayFields("session source", r.Kind, field{"function", r.Function != ""}, field{"env", r.Env != ""}); err != nil {
			return nil, err
		}
		return FromAssumeRole{}, nil
	case kindExecEnv:
		fn, err := aws.ParseARN(r.Function)
		if err != nil {
			return nil, fmt.Errorf("execution environment function: %w", err)
		}
		return FromExecutionEnvironment{Function: fn, Env: r.Env}, nil
	}
	return nil, fmt.Errorf("session source kind %q is not %s or %s", r.Kind, kindAssumeRole, kindExecEnv)
}

// field is a persisted field's name and whether a record sets it.
type field struct {
	name string
	set  bool
}

// strayFields refuses a record of the given kind that sets any of fields,
// which that kind does not have: dropping them would make a later Export
// differ from what was imported.
func strayFields(what, kind string, fields ...field) error {
	var names []string
	for _, f := range fields {
		if f.set {
			names = append(names, f.name)
		}
	}
	if names == nil {
		return nil
	}
	return fmt.Errorf("a %s %s has no %s", kind, what, strings.Join(names, ", "))
}
