package iam

import (
	"crypto/sha256"
	"encoding/base32"
	"strings"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/ids"
)

// key is the map key of an entity name: IAM names are unique regardless of
// case (iam-sts-kms.md 2.1).
func key(name string) string { return strings.ToLower(name) }

// pathOrRoot is an optional Path input, "/" when absent.
func pathOrRoot(path *string) string {
	if path == nil {
		return "/"
	}
	return *path
}

// underPrefix reports whether path starts with an optional PathPrefix
// filter; an absent prefix matches every path.
func underPrefix(path string, prefix *string) bool {
	return prefix == nil || strings.HasPrefix(path, *prefix)
}

// iamARN is an ARN of the account's IAM namespace; IAM ARNs have no region.
func iamARN(account aws.AccountID, resource string) aws.ARN {
	return aws.ARN{Partition: aws.PartitionAWS().String(), Service: "iam", Account: account.String(), Resource: resource}
}

func roleARN(account aws.AccountID, path, name string) aws.ARN {
	return iamARN(account, "role"+path+name)
}

func userARN(account aws.AccountID, path, name string) aws.ARN {
	return iamARN(account, "user"+path+name)
}

func instanceProfileARN(account aws.AccountID, path, name string) aws.ARN {
	return iamARN(account, "instance-profile"+path+name)
}

func localPolicyARN(account aws.AccountID, path, name string) aws.ARN {
	return iamARN(account, "policy"+path+name)
}

// awsOwner is the account element of an AWS managed policy's ARN.
const awsOwner = "aws"

// policyRef is a managed policy named by its ARN: an AWS managed policy
// (arn:aws:iam::aws:policy/...) or a customer managed one of an account.
type policyRef struct {
	arn aws.ARN
	// owner is the ARN's account element: 12 digits, or "aws".
	owner string
	path  string
	name  string
}

// parsePolicyARN reads a managed policy ARN, and false when s is not one.
func parsePolicyARN(s string) (policyRef, bool) {
	arn, err := aws.ParseARN(s)
	if err != nil || arn.Partition != aws.PartitionAWS().String() || arn.Service != "iam" || arn.Region != "" {
		return policyRef{}, false
	}
	rest, ok := strings.CutPrefix(arn.Resource, "policy/")
	if !ok {
		return policyRef{}, false
	}
	path, name := aws.SplitIAMPath(rest)
	if !aws.ValidIAMPath(path) || name == "" {
		return policyRef{}, false
	}
	if arn.Account != awsOwner {
		if _, err := aws.ParseAccountID(arn.Account); err != nil {
			return policyRef{}, false
		}
	}
	return policyRef{arn: arn, owner: arn.Account, path: path, name: name}, true
}

// awsManaged reports an AWS managed policy.
func (p policyRef) awsManaged() bool { return p.owner == awsOwner }

// ownedBy reports a customer managed policy of account.
func (p policyRef) ownedBy(account aws.AccountID) bool { return p.owner == account.String() }

// awsManagedPolicyID is the PolicyId understudy shows for an AWS managed
// policy: ANPA and 17 base32 characters derived from the ARN, so the same
// policy shows the same id in every account and run without being drawn
// from the id source or stored.
func awsManagedPolicyID(arn string) ids.IAMID {
	sum := sha256.Sum256([]byte(arn))
	return ids.IAMID("ANPA" + base32.StdEncoding.EncodeToString(sum[:])[:17])
}
