package sns

import (
	"sync"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/service/sns/envelope"
)

// key returns the stored key in the envelope package's form.
func (k *SigningKey) key() envelope.Key {
	return envelope.Key{Private: k.Private, Certificate: k.Certificate}
}

// ensureKey makes the signing key of the topic's scope when the topic
// exists and the scope has none yet. The key is made outside the cell
// lock, since RSA generation is slow; a key another call stored first
// wins.
func (s *Service) ensureKey(c *kernel.Call, ref topicRef) *awserr.Error {
	needed, _ := read(s, ref.scope, func(st *State) bool {
		_, ok := st.Topics[ref.name]
		return ok && st.Signing == nil
	})
	if !needed {
		return nil
	}
	key, err := envelope.NewKey("understudy SNS " + ref.scope.Account.String() + " " + ref.scope.Region.String())
	if err != nil {
		return awserr.Internalf(err)
	}
	_, aerr := kernel.Write(c, s.cells, ref.scope, func(st *State, _ clock.Instant, tx *kernel.Tx) (struct{}, *awserr.Error) {
		if st.Signing != nil {
			tx.Unchanged()
			return struct{}{}, nil
		}
		st.Signing = &SigningKey{Private: key.Private, Certificate: key.Certificate}
		return struct{}{}, nil
	})
	return aerr
}

// signing is how one write's messages from a topic are signed: the
// scope's key, behind a memo so a string to sign is signed once however
// many subscriptions receive it, the given version and the certificate's
// URL.
func (s *Service) signing(key *SigningKey, ref topicRef, v envelope.Version, l links) (envelope.Signing, *awserr.Error) {
	if key == nil {
		return envelope.Signing{}, awserr.Internalf(&KeyMissingError{Scope: ref.scope})
	}
	k := key.key()
	name := k.CertificateName()
	signer, err := s.signers.get(ref.scope, name, k)
	if err != nil {
		return envelope.Signing{}, awserr.Internalf(err)
	}
	return envelope.Signing{Version: v, Signer: newMemoSigner(signer), CertURL: l.cert(ref.scope, name)}, nil
}

// KeyMissingError is a scope that lost its signing key between ensureKey
// and the write that signs: a reset or snapshot load ran in between.
type KeyMissingError struct {
	Scope aws.Scope
}

func (e *KeyMissingError) Error() string {
	return "sns signing key: " + e.Scope.Account.String() + "/" + e.Scope.Region.String() +
		" has no signing key; a reset or snapshot load removed it during the call, so make the call again"
}

// signerKey names a parsed signer: the scope and the file name of its
// certificate, which differs for every key.
type signerKey struct {
	scope aws.Scope
	cert  string
}

// signerCache holds the parsed signer of each scope's key, so a write that
// signs does not parse the PKCS #1 key again under the cell lock. Reset
// and Import clear it; an entry for a replaced key is never looked up
// again, since its certificate name is gone with it.
type signerCache struct {
	mu      sync.Mutex
	signers map[signerKey]envelope.Signer
}

// get returns the signer of k, parsing it on first use.
func (c *signerCache) get(scope aws.Scope, cert string, k envelope.Key) (envelope.Signer, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	id := signerKey{scope: scope, cert: cert}
	if signer, ok := c.signers[id]; ok {
		return signer, nil
	}
	signer, err := k.Signer()
	if err != nil {
		return nil, err
	}
	if c.signers == nil {
		c.signers = map[signerKey]envelope.Signer{}
	}
	c.signers[id] = signer
	return signer, nil
}

// clear forgets every parsed signer.
func (c *signerCache) clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.signers = nil
}

// memoSigner signs each distinct string to sign once per version. The
// string to sign has no subscription-specific member (sns.md 5.6), so the
// subscriptions of one publish that receive the same body share one RSA
// signature. One write uses it, so it needs no lock.
type memoSigner struct {
	signer envelope.Signer
	signed map[memoKey][]byte
}

type memoKey struct {
	version envelope.Version
	text    string
}

func newMemoSigner(signer envelope.Signer) memoSigner {
	return memoSigner{signer: signer, signed: map[memoKey][]byte{}}
}

// Sign returns the signature made for the same version and text before,
// or makes it.
func (m memoSigner) Sign(v envelope.Version, stringToSign []byte) ([]byte, error) {
	k := memoKey{version: v, text: string(stringToSign)}
	if sig, ok := m.signed[k]; ok {
		return sig, nil
	}
	sig, err := m.signer.Sign(v, stringToSign)
	if err != nil {
		return nil, err
	}
	m.signed[k] = sig
	return sig, nil
}
