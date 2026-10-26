package authz

import (
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// Grammar is the form a service writes a denial message in.
type Grammar uint8

// Grammar values.
const (
	// GrammarIAM is the documented grammar (iam-enforcement-surfaces.md 4.1,
	// 12.3).
	GrammarIAM Grammar = iota
	// GrammarS3 is S3's variant: a quoted resource and ", with policy ARN:",
	// or "Access Denied" outside the owner's account (4.2).
	GrammarS3
	// GrammarSTS is the recorded AssumeRole text, with no reason clause
	// (4.3).
	GrammarSTS
	// GrammarKMSCiphertext is the kms:Decrypt text that names "the resource
	// associated with this ciphertext" (4.3).
	GrammarKMSCiphertext
)

// Denial is a refused pair as its message names it.
type Denial struct {
	Principal identity.Principal
	Action    registry.IAMAction
	Resource  Resource
	Decision  Decision
}

// Message renders d in g, one line (iam-evaluation.md 2.11); "" when
// d.Decision is Allow or nil. The principal is named as identity.Render shows
// it, a session by its session ARN (iam-enforcement-surfaces.md 4.4).
//
//   - GrammarIAM: "User: P is not authorized to perform: A", then "because no
//     T allows the A action", or "on resource: R with an explicit deny in
//     <a|an|the> T" and ": X" for a managed policy's ARN. "on resource:" is
//     written on explicit denials of a resource only (switch
//     iam.denial.grammar, IAM-ENF-15).
//   - GrammarS3: the same with the resource in quotes and ", with policy ARN:
//     X"; "Access Denied" for a principal outside the owner's account, a
//     service principal or an anonymous caller (4.2).
//   - GrammarSTS: "User: P is not authorized to perform: A on resource: R",
//     with no reason (4.3, aws-recorded for the implicit trust denial).
//   - GrammarKMSCiphertext: "User P is not authorized to perform: A on the
//     resource associated with this ciphertext", then the reason (4.3).
//
// An unknown Grammar renders as GrammarIAM.
func (d Denial) Message(g Grammar) string {
	action := d.Action.String()
	var reason, policyARN string
	explicit := false
	switch dec := d.Decision.(type) {
	case ImplicitDeny:
		reason = " because no " + dec.PolicyType.String() + " allows the " + action + " action"
	case ExplicitDeny:
		explicit, policyARN = true, dec.PolicyARN
		reason = " with an explicit deny in " + dec.PolicyType.withArticle()
	case Allow, nil:
		return ""
	}
	head := "User: " + identity.Render(d.Principal) + " is not authorized to perform: " + action
	arn, onResource := d.Resource.(OnResource)
	switch g {
	case GrammarS3:
		if account, ok := identity.AccountOf(d.Principal); !ok || account != ownerOf(d.Resource) {
			return "Access Denied"
		}
		s := head
		if explicit && onResource {
			s += ` on resource: "` + arn.ARN.String() + `"`
		}
		s += reason
		if policyARN != "" {
			s += ", with policy ARN: " + policyARN
		}
		return s
	case GrammarSTS:
		if onResource {
			return head + " on resource: " + arn.ARN.String()
		}
		return head
	case GrammarKMSCiphertext:
		return "User " + identity.Render(d.Principal) + " is not authorized to perform: " + action +
			" on the resource associated with this ciphertext" + reason
	case GrammarIAM:
	}
	s := head
	if explicit && onResource {
		s += " on resource: " + arn.ARN.String()
	}
	s += reason
	if policyARN != "" {
		s += ": " + policyARN
	}
	return s
}

// withArticle is the type as an explicit denial names it: "an identity-based
// policy", "a resource-based policy", "a permissions boundary", "a session
// policy" and "the role trust policy" (iam-enforcement-surfaces.md 4.1).
func (t PolicyType) withArticle() string {
	switch t {
	case IdentityBased:
		return "an " + t.String()
	case RoleTrust:
		return "the " + t.String()
	case ResourceBased, PermissionsBoundary, SessionPolicy:
	}
	return "a " + t.String()
}
