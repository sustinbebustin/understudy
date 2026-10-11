package iam

import (
	"encoding/json"
	"unicode"

	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/policydoc"
)

// IAM quotas (iam-sts-kms.md 3.2). Document sizes count characters other
// than white space, which IAM does not count.
const (
	maxTrustPolicy    = 2048  // ACLSizePerRole: a role's trust policy
	maxManagedPolicy  = 6144  // PolicySize: one managed policy version
	maxRoleInline     = 10240 // a role's inline policies together
	maxUserInline     = 2048  // a user's inline policies together
	maxPolicyVersions = 5
	maxRoleAttached   = 20 // PoliciesPerRole
	maxUserAttached   = 10 // PoliciesPerUser
	maxAccessKeys     = 2  // AccessKeysPerUser
	maxTags           = 50
)

// checkTrustPolicy validates a role's assume-role policy as IAM does:
// syntax, the trust policy rules, then the size quota.
func checkTrustPolicy(doc string) *awserr.Error {
	parsed, err := parsePolicy(doc)
	if err != nil {
		return err
	}
	if verr := policydoc.ValidateTrust(parsed); verr != nil {
		return errMalformedPolicy(verr.Message())
	}
	if policySize(doc) > maxTrustPolicy {
		return errQuota("ACLSizePerRole", maxTrustPolicy)
	}
	return nil
}

// checkIdentityPolicy validates a managed or inline policy as IAM does:
// syntax, then the identity policy rules.
func checkIdentityPolicy(doc string) *awserr.Error {
	parsed, err := parsePolicy(doc)
	if err != nil {
		return err
	}
	if verr := policydoc.ValidateIdentity(parsed); verr != nil {
		return errMalformedPolicy(verr.Message())
	}
	return nil
}

// parsePolicy runs IAM's first two checks of any policy document: that it
// is JSON (error-matrix.md 5.10 T3), then that the JSON is a policy.
func parsePolicy(doc string) (policydoc.Document, *awserr.Error) {
	if !json.Valid([]byte(doc)) {
		return policydoc.Document{}, errInvalidJSON()
	}
	parsed, perr := policydoc.Parse([]byte(doc))
	if perr != nil {
		return policydoc.Document{}, errMalformedPolicy(perr.Message())
	}
	return parsed, nil
}

// checkManagedPolicy validates one managed policy version, its size
// included.
func checkManagedPolicy(doc string) *awserr.Error {
	if err := checkIdentityPolicy(doc); err != nil {
		return err
	}
	if policySize(doc) > maxManagedPolicy {
		return errQuota("PolicySize", maxManagedPolicy)
	}
	return nil
}

// policySize is a document's size as IAM's quotas count it: its characters
// other than white space.
func policySize(doc string) int {
	n := 0
	for _, r := range doc {
		if !unicode.IsSpace(r) {
			n++
		}
	}
	return n
}
