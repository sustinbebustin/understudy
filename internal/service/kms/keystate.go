package kms

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"slices"
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
		if err := rule.check(k, keyARN(r.scope(), k.ID).String()); err != nil {
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
	if m, ok := mintable(r, c.Scope()); ok && err != nil {
		k, err = s.mint(c, m)
	}
	if err != nil {
		return Key{}, err
	}
	if err := rule.check(k, keyARN(r.scope(), k.ID).String()); err != nil {
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

// mintRef is a reference to an AWS managed key that its first use creates.
type mintRef struct {
	alias keyByAlias
	key   managedKey
}

// mintable reports an alias/aws/ alias of an AWS service, which names an
// AWS managed key that AWS creates on first use (iam-sts-kms.md 5.4). Only
// a caller in the alias's own account creates one: AWS managed keys are not
// usable across accounts, so a reference from another account finds only a
// key that exists.
func mintable(r keyRef, caller aws.Scope) (mintRef, bool) {
	a, ok := r.(keyByAlias)
	if !ok || a.in.Account != caller.Account {
		return mintRef{}, false
	}
	m, ok := managedKeyFor(a.name)
	return mintRef{alias: a, key: m}, ok
}

// wouldMint reports a mintable alias that names no key yet.
func (s *Service) wouldMint(c *kernel.Call, r keyRef) bool {
	if _, ok := mintable(r, c.Scope()); !ok {
		return false
	}
	_, err := lookup(s, r)
	return err != nil
}

// mint creates the AWS managed key m names, or returns the one a
// concurrent first use created.
func (s *Service) mint(c *kernel.Call, m mintRef) (Key, *awserr.Error) {
	a := m.alias
	return kernel.Write(c, s.cells, a.in, func(st *State, now clock.Instant, tx *kernel.Tx) (Key, *awserr.Error) {
		if k, err := find(st, a); err == nil {
			tx.Unchanged()
			return k, nil
		}
		k, err := s.newKey(a.in, now, kmsapi.KeyManagerTypeAws, m.key.description, m.key.policy(a.in))
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
			return Key{}, errKeyNotFound(keyARN(r.in, r.id).String())
		}
		return k, nil
	case keyByAlias:
		a, ok := st.Aliases[r.name]
		if !ok {
			return Key{}, errAliasNotFound(aliasARN(r.in, r.name).String())
		}
		k, ok := st.Keys[a.KeyID]
		if !ok {
			return Key{}, errKeyNotFound(keyARN(r.in, a.KeyID).String())
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

// managedKey is the AWS managed key a predefined alias/aws/ alias names:
// the alias, the service's name as its key policy's statement ids give it,
// and the Description AWS gives the key it creates.
type managedKey struct {
	alias, serviceName, description string
}

// unrecordedManagedDescription is the Description of an AWS managed key
// whose service-specific text AWS has not been recorded giving; it is
// understudy's own.
const unrecordedManagedDescription = "Default key"

// managedKeys returns the predefined alias/aws/ aliases of the services
// that keep an AWS managed key, from moto's table plus SNS and SQS
// (iam-sts-kms.md 5.4). The aws/s3 description is recorded on DescribeKey
// in LocalStack's AWS-validated
// test_s3_bucket_encryption_sse_kms_aws_managed_key
// (tests/aws/services/s3/test_s3_api.snapshot.json@c2cb023 lines
// 1500-1519). Only the aws/secretsmanager service name is documented in a
// key policy (see policy); the others are the services' product names.
func managedKeys() []managedKey {
	return []managedKey{
		{"alias/aws/acm", "AWS Certificate Manager", unrecordedManagedDescription},
		{"alias/aws/dynamodb", "Amazon DynamoDB", unrecordedManagedDescription},
		{"alias/aws/ebs", "Amazon EBS", unrecordedManagedDescription},
		{"alias/aws/elasticfilesystem", "Amazon Elastic File System", unrecordedManagedDescription},
		{"alias/aws/es", "Amazon OpenSearch Service", unrecordedManagedDescription},
		{"alias/aws/glue", "AWS Glue", unrecordedManagedDescription},
		{"alias/aws/kinesisvideo", "Amazon Kinesis Video Streams", unrecordedManagedDescription},
		{"alias/aws/lambda", "AWS Lambda", unrecordedManagedDescription},
		{"alias/aws/rds", "Amazon RDS", unrecordedManagedDescription},
		{"alias/aws/redshift", "Amazon Redshift", unrecordedManagedDescription},
		{"alias/aws/s3", "Amazon S3", "Default key that protects my S3 objects when no other key is defined"},
		{"alias/aws/secretsmanager", "AWS Secrets Manager", unrecordedManagedDescription},
		{"alias/aws/sns", "Amazon SNS", unrecordedManagedDescription},
		{"alias/aws/sqs", "Amazon SQS", unrecordedManagedDescription},
		{"alias/aws/ssm", "AWS Systems Manager", unrecordedManagedDescription},
		{"alias/aws/xray", "AWS X-Ray", unrecordedManagedDescription},
	}
}

// policy is the fixed key policy of m's key in scope, in the GetKeyPolicy
// layout defaultPolicy uses. Its statements are the ones AWS documents for
// aws/secretsmanager (iam-sts-kms.md 5.4, from
// https://docs.aws.amazon.com/secretsmanager/latest/userguide/security-encryption.html):
// principals of the account use the key only through the service
// (kms:ViaService and kms:CallerAccount), and the account root may only
// read the key's metadata and revoke grants, never kms:*. AWS documents no
// other service's managed key policy, so theirs follow that one and are
// understudy's own, as is the Id's version suffix -1 (the documented
// example's is -2).
func (m managedKey) policy(scope aws.Scope) string {
	service := strings.TrimPrefix(m.alias, "alias/aws/")
	account := scope.Account.String()
	via := service + "." + scope.Region.String() + ".amazonaws.com"
	sid := "Allow access through " + m.serviceName + " for all principals in the account that are authorized to use " + m.serviceName
	return `{
  "Version" : "2012-10-17",
  "Id" : "auto-` + service + `-1",
  "Statement" : [ {
    "Sid" : "` + sid + `",
    "Effect" : "Allow",
    "Principal" : {
      "AWS" : [ "*" ]
    },
    "Action" : [ "kms:Encrypt", "kms:Decrypt", "kms:ReEncrypt*", "kms:CreateGrant", "kms:DescribeKey" ],
    "Resource" : "*",
    "Condition" : {
      "StringEquals" : {
        "kms:CallerAccount" : "` + account + `",
        "kms:ViaService" : "` + via + `"
      }
    }
  }, {
    "Sid" : "` + sid + `",
    "Effect" : "Allow",
    "Principal" : {
      "AWS" : [ "*" ]
    },
    "Action" : "kms:GenerateDataKey*",
    "Resource" : "*",
    "Condition" : {
      "StringEquals" : {
        "kms:CallerAccount" : "` + account + `"
      },
      "StringLike" : {
        "kms:ViaService" : "` + via + `"
      }
    }
  }, {
    "Sid" : "Allow direct access to key metadata to the account",
    "Effect" : "Allow",
    "Principal" : {
      "AWS" : [ "` + aws.RootARN(scope.Partition, account).String() + `" ]
    },
    "Action" : [ "kms:Describe*", "kms:Get*", "kms:List*", "kms:RevokeGrant" ],
    "Resource" : "*"
  } ]
}`
}

// managedKeyFor returns the AWS managed key the alias name names, if name
// is a predefined alias/aws/ alias.
func managedKeyFor(name string) (managedKey, bool) {
	i := slices.IndexFunc(managedKeys(), func(m managedKey) bool { return m.alias == name })
	if i < 0 {
		return managedKey{}, false
	}
	return managedKeys()[i], true
}

// newKey makes an enabled key with fresh material in scope: the default key
// policy when policy is empty.
func (s *Service) newKey(scope aws.Scope, now clock.Instant, manager kmsapi.KeyManagerType, description, policy string) (Key, *awserr.Error) {
	material, err := randomBytes(materialBytes)
	if err != nil {
		return Key{}, err
	}
	if policy == "" {
		policy = defaultPolicy(scope)
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

// defaultPolicy is the key policy a key in scope gets when CreateKey has
// none, in the layout of the model's GetKeyPolicy example.
func defaultPolicy(scope aws.Scope) string {
	return `{
  "Version" : "2012-10-17",
  "Id" : "key-default-1",
  "Statement" : [ {
    "Sid" : "Enable IAM User Permissions",
    "Effect" : "Allow",
    "Principal" : {
      "AWS" : "` + aws.RootARN(scope.Partition, scope.Account.String()).String() + `"
    },
    "Action" : "kms:*",
    "Resource" : "*"
  } ]
}`
}
