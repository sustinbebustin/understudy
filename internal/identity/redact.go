package identity

import (
	"encoding/json"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/sustinbebustin/understudy/internal/ids"
)

const redacted = "[REDACTED]"

// redactedView is the one form Credentials, Minted and Temporary take outside
// the store: in slog output, through LogValue at the top level and
// MarshalJSON when slog's JSONHandler encodes a struct or slice that holds
// one, and in fmt formatting. It never holds a secret or a token, only the
// marker that one exists; empty fields are left out.
type redactedView struct {
	AccessKeyID     ids.AccessKeyID `json:"access_key_id,omitempty"`
	SecretAccessKey string          `json:"secret_access_key,omitempty"`
	SessionToken    string          `json:"session_token,omitempty"`
	Expiry          *time.Time      `json:"expiry,omitempty"`
}

// viewOf redacts key, a secret when hasSecret, and life's token.
func viewOf(key ids.AccessKeyID, hasSecret bool, life Lifetime) redactedView {
	v := redactedView{AccessKeyID: key}
	if hasSecret {
		v.SecretAccessKey = redacted
	}
	if t, ok := life.(Temporary); ok {
		v.SessionToken = redacted
		v.Expiry = &t.Expiry
	}
	return v
}

func (v redactedView) String() string {
	var parts []string
	if v.AccessKeyID != "" {
		parts = append(parts, "AccessKeyID:"+string(v.AccessKeyID))
	}
	if v.SecretAccessKey != "" {
		parts = append(parts, "SecretAccessKey:"+v.SecretAccessKey)
	}
	if v.SessionToken != "" {
		parts = append(parts, "SessionToken:"+v.SessionToken)
	}
	if v.Expiry != nil {
		parts = append(parts, "Expiry:"+v.Expiry.Format(time.RFC3339))
	}
	return "{" + strings.Join(parts, " ") + "}"
}

func (v redactedView) logValue() slog.Value {
	var attrs []slog.Attr
	if v.AccessKeyID != "" {
		attrs = append(attrs, slog.String("access_key_id", string(v.AccessKeyID)))
	}
	if v.SecretAccessKey != "" {
		attrs = append(attrs, slog.String("secret_access_key", v.SecretAccessKey))
	}
	if v.SessionToken != "" {
		attrs = append(attrs, slog.String("session_token", v.SessionToken))
	}
	if v.Expiry != nil {
		attrs = append(attrs, slog.Time("expiry", *v.Expiry))
	}
	return slog.GroupValue(attrs...)
}

func (c Credentials) view() redactedView { return viewOf(c.AccessKeyID, true, c.Lifetime) }

// LogValue logs the access key id and expiry; the secret and the session
// token never reach a log.
func (c Credentials) LogValue() slog.Value { return c.view().logValue() }

// MarshalJSON writes the LogValue view, so slog's JSONHandler redacts
// Credentials held in a struct or slice too. Export does not use it.
func (c Credentials) MarshalJSON() ([]byte, error) { return json.Marshal(c.view()) }

// String formats the credentials with the secret and token redacted, so a
// struct holding Credentials prints safely with %v.
func (c Credentials) String() string { return c.view().String() }

// GoString is String, so %#v redacts too.
func (c Credentials) GoString() string { return "identity.Credentials" + c.String() }

func (m Minted) view() redactedView { return viewOf("", true, m.Lifetime) }

// LogValue logs the expiry; the secret and the session token never reach a
// log.
func (m Minted) LogValue() slog.Value { return m.view().logValue() }

// MarshalJSON writes the LogValue view.
func (m Minted) MarshalJSON() ([]byte, error) { return json.Marshal(m.view()) }

// String formats the resolution with the secret and token redacted.
func (m Minted) String() string { return m.view().String() }

// GoString is String, so %#v redacts too.
func (m Minted) GoString() string { return "identity.Minted" + m.String() }

func (k StaticKey) view() redactedView { return viewOf(k.AccessKeyID, true, nil) }

// LogValue logs the access key id; the secret never reaches a log.
func (k StaticKey) LogValue() slog.Value { return k.view().logValue() }

// MarshalJSON writes the LogValue view.
func (k StaticKey) MarshalJSON() ([]byte, error) { return json.Marshal(k.view()) }

// String formats the key with the secret redacted.
func (k StaticKey) String() string { return k.view().String() }

// GoString is String, so %#v redacts too.
func (k StaticKey) GoString() string { return "identity.StaticKey" + k.String() }

func (Static) view() redactedView { return viewOf("", true, nil) }

// LogValue logs that a secret exists; the secret never reaches a log.
func (s Static) LogValue() slog.Value { return s.view().logValue() }

// MarshalJSON writes the LogValue view.
func (s Static) MarshalJSON() ([]byte, error) { return json.Marshal(s.view()) }

// String formats the resolution with the secret redacted.
func (s Static) String() string { return s.view().String() }

// GoString is String, so %#v redacts too.
func (s Static) GoString() string { return "identity.Static" + s.String() }

func (AccountKey) view() redactedView { return viewOf("", true, nil) }

// LogValue logs that a secret exists; the secret never reaches a log.
func (a AccountKey) LogValue() slog.Value { return a.view().logValue() }

// MarshalJSON writes the LogValue view.
func (a AccountKey) MarshalJSON() ([]byte, error) { return json.Marshal(a.view()) }

// String formats the resolution with the secret redacted.
func (a AccountKey) String() string { return a.view().String() }

// GoString is String, so %#v redacts too.
func (a AccountKey) GoString() string { return "identity.AccountKey" + a.String() }

func (t Temporary) view() redactedView { return viewOf("", false, t) }

// LogValue logs the expiry; the session token never reaches a log.
func (t Temporary) LogValue() slog.Value { return t.view().logValue() }

// MarshalJSON writes the LogValue view.
func (t Temporary) MarshalJSON() ([]byte, error) { return json.Marshal(t.view()) }

// String formats the lifetime with the session token redacted.
func (t Temporary) String() string { return t.view().String() }

// GoString is String, so %#v redacts too.
func (t Temporary) GoString() string { return "identity.Temporary" + t.String() }

// storeView is the one form a Store takes in logs and formatting: fmt reads
// its unexported fields directly, past the redaction of the Credentials they
// hold, so the Store reports only its default account and key count.
type storeView struct {
	DefaultAccount string `json:"default_account"`
	MintedKeys     int    `json:"minted_keys"`
}

func (s *Store) view() storeView {
	s.mu.Lock()
	defer s.mu.Unlock()
	return storeView{DefaultAccount: s.defaultAccount.String(), MintedKeys: len(s.keys)}
}

// LogValue logs the default account and the number of minted keys; no key,
// secret or token reaches a log.
func (s *Store) LogValue() slog.Value {
	v := s.view()
	return slog.GroupValue(slog.String("default_account", v.DefaultAccount), slog.Int("minted_keys", v.MintedKeys))
}

// MarshalJSON writes the LogValue view, so slog's JSONHandler reports a
// Store held in a struct or slice the same way.
func (s *Store) MarshalJSON() ([]byte, error) { return json.Marshal(s.view()) }

// String formats the store as its default account and key count.
func (s *Store) String() string {
	v := s.view()
	return "{DefaultAccount:" + v.DefaultAccount + " MintedKeys:" + strconv.Itoa(v.MintedKeys) + "}"
}

// GoString is String, so %#v reveals no credentials either.
func (s *Store) GoString() string { return "identity.Store" + s.String() }
