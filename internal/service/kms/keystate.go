package kms

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/sustinbebustin/understudy/internal/api/kmsapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
)

// stateRule is the column of the key-state table an operation follows
// (iam-sts-kms.md 5.6, from AWS's key-state page): which of Enabled,
// Disabled and PendingDeletion it accepts and what it answers for the rest.
type stateRule uint8

const (
	// anyState: DescribeKey, GetKeyPolicy, PutKeyPolicy, ListResourceTags,
	// GetKeyRotationStatus, ListKeyPolicies, DeleteAlias, an UpdateAlias
	// source.
	anyState stateRule = iota + 1
	// usable: the cryptographic operations and EnableKeyRotation and
	// DisableKeyRotation; Disabled is DisabledException.
	usable
	// notPending: EnableKey, DisableKey, ScheduleKeyDeletion, TagResource,
	// UntagResource, CreateAlias, UpdateKeyDescription.
	notPending
	// aliasDestination: an UpdateAlias destination, notPending with the
	// shorter text of footnote 10.
	aliasDestination
	// onlyPending: CancelKeyDeletion.
	onlyPending
)

// check answers the error the rule gives a key in its current state, nil
// when the operation may proceed.
func (r stateRule) check(k Key, arn string) *awserr.Error {
	switch k.Lifecycle.(type) {
	case enabled:
		if r == onlyPending {
			return errNotPendingDeletion(arn)
		}
	case disabled:
		switch r {
		case usable:
			return errDisabled(arn)
		case onlyPending:
			return errNotPendingDeletion(arn)
		case anyState, notPending, aliasDestination:
		}
	case pendingDeletion:
		switch r {
		case usable, notPending:
			return errPendingDeletion(arn)
		case aliasDestination:
			return errDestinationPendingDeletion(arn)
		case anyState, onlyPending:
		}
	}
	return nil
}

// withKey runs fn on the key r names, in the cell of its scope, once the
// operation's state rule accepts the key: the path of the operations that
// change state. r is a key id or key ARN, so it never creates a key.
func withKey[R any](s *Service, c *kernel.Call, r keyByID, rule stateRule, fn func(st *State, k Key, now clock.Instant, tx *kernel.Tx) (R, *awserr.Error)) (R, *awserr.Error) {
	return kernel.Write(c, s.cells, r.scope(), func(st *State, now clock.Instant, tx *kernel.Tx) (R, *awserr.Error) {
		var zero R
		k, err := find(st, r)
		if err != nil {
			return zero, err
		}
		if err := rule.check(k, keyARN(r.scope(), k.ID)); err != nil {
			return zero, err
		}
		return fn(st, k, now, tx)
	})
}

// keyFor returns the key r names once the operation's state rule accepts
// it: the path of the operations that only read a key, the cryptographic
// ones included. It reads the cell, so it neither takes the cell's write
// lock nor creates a cell for a scope nobody wrote to; a Key is a value
// never changed in place, so the caller works on it after the read. Only
// an AWS managed key's first use writes, to create it (mint).
func (s *Service) keyFor(c *kernel.Call, r keyRef, rule stateRule) (Key, *awserr.Error) {
	k, err := lookup(s, r)
	if a, ok := mintable(r, c.Scope()); ok && err != nil {
		k, err = s.mint(c, a)
	}
	if err != nil {
		return Key{}, err
	}
	if err := rule.check(k, keyARN(r.scope(), k.ID)); err != nil {
		return Key{}, err
	}
	return k, nil
}

// lookup is find on a read of r's cell.
func lookup(s *Service, r keyRef) (Key, *awserr.Error) {
	type found struct {
		k   Key
		err *awserr.Error
	}
	f := readState(s, r.scope(), func(st *State) found {
		k, err := find(st, r)
		return found{k, err}
	})
	return f.k, f.err
}

// mintable reports an alias/aws/ alias of an AWS service, which names an
// AWS managed key that AWS creates on first use (iam-sts-kms.md 5.4). Only
// a caller in the alias's own account creates one: AWS managed keys are not
// usable across accounts, so a reference from another account finds only a
// key that exists.
func mintable(r keyRef, caller aws.Scope) (keyByAlias, bool) {
	a, ok := r.(keyByAlias)
	return a, ok && managedAlias(a.name) && a.in.Account == caller.Account
}

// wouldMint reports a mintable alias that names no key yet.
func (s *Service) wouldMint(c *kernel.Call, r keyRef) bool {
	if _, ok := mintable(r, c.Scope()); !ok {
		return false
	}
	_, err := lookup(s, r)
	return err != nil
}

// mint creates the AWS managed key alias a names, or returns the one a
// concurrent first use created.
func (s *Service) mint(c *kernel.Call, a keyByAlias) (Key, *awserr.Error) {
	return kernel.Write(c, s.cells, a.in, func(st *State, now clock.Instant, tx *kernel.Tx) (Key, *awserr.Error) {
		if k, err := find(st, a); err == nil {
			tx.Unchanged()
			return k, nil
		}
		k, err := s.newKey(a.in, now, kmsapi.KeyManagerTypeAws, "Default key", "")
		if err != nil {
			return Key{}, err
		}
		st.Keys[k.ID] = k
		st.Aliases[a.name] = Alias{Name: a.name, KeyID: k.ID, Created: now, Updated: now}
		return k, nil
	})
}

// find returns the key r names in st, creating none. An alias whose key is
// missing, which only a hand-edited snapshot can hold, names no key.
func find(st *State, r keyRef) (Key, *awserr.Error) {
	switch r := r.(type) {
	case keyByID:
		k, ok := st.Keys[r.id]
		if !ok {
			return Key{}, errKeyNotFound(keyARN(r.in, r.id))
		}
		return k, nil
	case keyByAlias:
		a, ok := st.Aliases[r.name]
		if !ok {
			return Key{}, errAliasNotFound(aliasARN(r.in, r.name))
		}
		k, ok := st.Keys[a.KeyID]
		if !ok {
			return Key{}, errKeyNotFound(keyARN(r.in, a.KeyID))
		}
		return k, nil
	}
	return Key{}, awserr.Internalf(&RefError{})
}

// customerOnly refuses an AWS managed key to an operation that would change
// it: AWS lets no caller tag, rotate, enable, disable, describe anew,
// re-policy or delete one (iam-sts-kms.md 5.2 and 5.4).
func customerOnly(k Key, arn string) *awserr.Error {
	if k.Manager == kmsapi.KeyManagerTypeAws {
		return errAWSManagedKey(arn)
	}
	return nil
}

// RefError is a key reference of no known form, a bug in keyref.go.
type RefError struct{}

func (*RefError) Error() string { return "kms: a key reference of no known form reached resolve" }

// managedAlias reports an alias/aws/ name of a service that keeps an AWS
// managed key, from moto's table plus SNS and SQS (iam-sts-kms.md 5.4).
func managedAlias(name string) bool {
	svc, ok := strings.CutPrefix(name, "alias/aws/")
	if !ok {
		return false
	}
	switch svc {
	case "acm", "dynamodb", "ebs", "elasticfilesystem", "es", "glue", "kinesisvideo", "lambda",
		"rds", "redshift", "s3", "secretsmanager", "sns", "sqs", "ssm", "xray":
		return true
	}
	return false
}

// newKey makes an enabled key with fresh material in scope: the default key
// policy when policy is empty.
func (s *Service) newKey(scope aws.Scope, now clock.Instant, manager kmsapi.KeyManagerType, description, policy string) (Key, *awserr.Error) {
	material, err := randomBytes(materialBytes)
	if err != nil {
		return Key{}, err
	}
	if policy == "" {
		policy = defaultPolicy(scope.Account)
	}
	id := string(s.ids.KMSKeyID())
	return Key{
		ID:          id,
		Created:     now,
		Description: description,
		Manager:     manager,
		Lifecycle:   enabled{},
		Policy:      policy,
		Material:    material,
		MaterialID:  materialID(id),
	}, nil
}

// materialID is the CurrentKeyMaterialId of key id: 64 hex characters, as
// AWS documents, derived from the seeded key id because ADR 0018 exempts
// only the material itself from the seeded source.
func materialID(id string) string {
	sum := sha256.Sum256([]byte("understudy-kms-material:" + id))
	return hex.EncodeToString(sum[:])
}

// randomBytes draws n bytes from crypto/rand: key material and data keys are
// never seeded (ADR 0018).
func randomBytes(n int) ([]byte, *awserr.Error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return nil, awserr.Internalf(err)
	}
	return b, nil
}

// defaultPolicy is the key policy a key gets when CreateKey has none, in
// the layout of the model's GetKeyPolicy example.
func defaultPolicy(account aws.AccountID) string {
	return `{
  "Version" : "2012-10-17",
  "Id" : "key-default-1",
  "Statement" : [ {
    "Sid" : "Enable IAM User Permissions",
    "Effect" : "Allow",
    "Principal" : {
      "AWS" : "arn:aws:iam::` + account.String() + `:root"
    },
    "Action" : "kms:*",
    "Resource" : "*"
  } ]
}`
}
