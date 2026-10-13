package identity

import (
	"fmt"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/ids"
)

// StaticKey is an access key from configuration (accounts.keys): the root
// of Account, signing with Secret. StaticKey redacts the secret in slog
// output, JSON and fmt formatting.
type StaticKey struct {
	AccessKeyID ids.AccessKeyID
	Secret      ids.SecretAccessKey
	Account     aws.AccountID
}

// SetStaticKeys replaces the static keys with keys. It is all or nothing:
// on error the store keeps the static keys it held. Static keys come from
// configuration at every start, so Export never writes them and Import and
// a reset keep them.
func (s *Store) SetStaticKeys(keys []StaticKey) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	static := make(map[ids.AccessKeyID]StaticKey, len(keys))
	for i, k := range keys {
		switch _, minted := s.keys[k.AccessKeyID]; {
		case k.Secret == "":
			return &StaticKeyError{Index: i, Key: k.AccessKeyID, Problem: StaticKeyNoSecret}
		case minted:
			return &StaticKeyError{Index: i, Key: k.AccessKeyID, Problem: StaticKeyMinted}
		}
		if _, dup := static[k.AccessKeyID]; dup {
			return &StaticKeyError{Index: i, Key: k.AccessKeyID, Problem: StaticKeyListedTwice}
		}
		static[k.AccessKeyID] = k
	}
	s.static = static
	return nil
}

// StaticKeyProblem says why SetStaticKeys refused a key.
type StaticKeyProblem uint8

// StaticKeyProblem values.
const (
	// StaticKeyNoSecret: the key has an empty secret, which no signature
	// could be verified against.
	StaticKeyNoSecret StaticKeyProblem = iota + 1
	// StaticKeyMinted: the store minted the key, so it already resolves to
	// the principal it was minted for.
	StaticKeyMinted
	// StaticKeyListedTwice: the key appears earlier in the list.
	StaticKeyListedTwice
)

// StaticKeyError reports a static key SetStaticKeys refused. The store kept
// the static keys it held before the call.
type StaticKeyError struct {
	// Index is the key's position in the list passed.
	Index   int
	Key     ids.AccessKeyID
	Problem StaticKeyProblem
}

func (e *StaticKeyError) Error() string {
	var why string
	switch e.Problem {
	case StaticKeyNoSecret:
		why = "its secret access key is empty; give it the secret clients sign with"
	case StaticKeyMinted:
		why = "understudy minted that key itself, so it already belongs to a principal; choose another key id"
	case StaticKeyListedTwice:
		why = "it is listed twice; keep one entry"
	default:
		why = fmt.Sprintf("unknown problem %d", uint8(e.Problem))
	}
	return fmt.Sprintf("identity: static key %d (%s) is refused: %s; the store kept its previous static keys", e.Index, e.Key, why)
}
