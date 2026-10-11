package kms

import (
	"strings"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/kernel"
)

// keyRef is what a KeyId member names, with the scope it resolves in.
//
//sumtype:decl
type keyRef interface {
	isKeyRef()
	scope() aws.Scope
}

// keyByID is a key id or key ARN.
type keyByID struct {
	in aws.Scope
	id string
}

// keyByAlias is an alias name or alias ARN; name keeps its "alias/".
type keyByAlias struct {
	in   aws.Scope
	name string
}

func (keyByID) isKeyRef()    {}
func (keyByAlias) isKeyRef() {}

func (r keyByID) scope() aws.Scope    { return r.in }
func (r keyByAlias) scope() aws.Scope { return r.in }

// refForms says which identifier forms an operation accepts: every one, or
// only a key id and key ARN (GetKeyRotationStatus, the key management
// operations; iam-sts-kms.md 5.1).
type refForms uint8

const (
	keyOrAlias refForms = iota + 1
	keyOnly
)

// reach says whose keys an operation may name: any account's, by ARN, for
// the operations AWS documents with "Cross-account use: Yes" (DescribeKey,
// GetKeyRotationStatus, the cryptographic operations), or only the
// caller's own, for the key management operations, which AWS never lets
// one account run on another's key whatever the key policy says.
type reach uint8

const (
	anyAccount reach = iota + 1
	ownAccount
)

// check refuses, for an ownAccount operation, a reference to another
// account's key. Which error AWS answers is unrecorded; understudy answers
// the not-found error of the identifier given (the fidelity switch
// kms.cross-account.management-error).
func (rc reach) check(c *kernel.Call, r keyRef) *awserr.Error {
	if rc == anyAccount || r.scope().Account == c.Scope().Account {
		return nil
	}
	switch r := r.(type) {
	case keyByID:
		return errKeyNotFound(keyARN(r.in, r.id))
	case keyByAlias:
		return errAliasNotFound(aliasARN(r.in, r.name))
	}
	return awserr.Internalf(&RefError{})
}

// parseKeyRef reads a KeyId member from c's point of view: a key id, key
// ARN, alias name or alias ARN. An ARN may name another account, as
// cross-account use requires; one naming another Region is "Invalid arn
// <region>" (recorded for DescribeKey).
func parseKeyRef(c *kernel.Call, s string, forms refForms) (keyRef, *awserr.Error) {
	scope := c.Scope()
	rest, isARN := strings.CutPrefix(s, "arn:")
	if !isARN {
		return localRef(scope, s, forms)
	}
	a, err := aws.ParseARN("arn:" + rest)
	if err != nil || a.Service != "kms" || a.Partition != scope.Partition.String() {
		return nil, errInvalidKeyID(s)
	}
	if a.Region != scope.Region.String() {
		return nil, errInvalidARNRegion(a.Region)
	}
	account, perr := aws.ParseAccountID(a.Account)
	if perr != nil {
		return nil, errInvalidKeyID(s)
	}
	scope.Account = account
	if id, ok := strings.CutPrefix(a.Resource, "key/"); ok {
		if !validKeyID(id) {
			return nil, errInvalidKeyID(s)
		}
		return keyByID{in: scope, id: id}, nil
	}
	if strings.HasPrefix(a.Resource, "alias/") && forms == keyOrAlias {
		return keyByAlias{in: scope, name: a.Resource}, nil
	}
	return nil, errInvalidKeyID(s)
}

// parseKeyID reads a KeyId member that admits only a key id or key ARN, so
// the write path, which takes a keyByID, can never mint an AWS managed key.
func parseKeyID(c *kernel.Call, s string) (keyByID, *awserr.Error) {
	r, err := parseKeyRef(c, s, keyOnly)
	if err != nil {
		return keyByID{}, err
	}
	id, ok := r.(keyByID)
	if !ok {
		return keyByID{}, awserr.Internalf(&RefError{})
	}
	return id, nil
}

// localRef reads a key id or alias name in scope.
func localRef(scope aws.Scope, s string, forms refForms) (keyRef, *awserr.Error) {
	if strings.HasPrefix(s, "alias/") && forms == keyOrAlias {
		return keyByAlias{in: scope, name: s}, nil
	}
	if !validKeyID(s) {
		return nil, errInvalidKeyID(s)
	}
	return keyByID{in: scope, id: s}, nil
}

// validKeyID reports the key id forms KMS accepts: a UUID
// (1234abcd-12ab-34cd-56ef-1234567890ab) or a multi-Region id (mrk- and 32
// hex digits). Others are "Invalid keyId", as recorded for fake-key-id, a
// UUID without hyphens and mrk-fake-key-id.
func validKeyID(s string) bool {
	if hex, ok := strings.CutPrefix(s, "mrk-"); ok {
		return len(hex) == 32 && isHex(hex)
	}
	if len(s) != 36 {
		return false
	}
	for i, r := range s {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			if !isHex(string(r)) {
				return false
			}
		}
	}
	return true
}

func isHex(s string) bool {
	for _, r := range s {
		if ('0' > r || r > '9') && ('a' > r || r > 'f') && ('A' > r || r > 'F') {
			return false
		}
	}
	return true
}

// keyARN is the ARN of key id in scope.
func keyARN(scope aws.Scope, id string) string {
	return aws.ARN{Partition: scope.Partition.String(), Service: "kms", Region: scope.Region.String(), Account: scope.Account.String(), Resource: "key/" + id}.String()
}

// aliasARN is the ARN of the alias name ("alias/..." ) in scope.
func aliasARN(scope aws.Scope, name string) string {
	return aws.ARN{Partition: scope.Partition.String(), Service: "kms", Region: scope.Region.String(), Account: scope.Account.String(), Resource: name}.String()
}
