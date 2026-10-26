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

// Message renders d in g, one line; "" when d.Decision is Allow.
//
// Not implemented yet: it returns "".
func (d Denial) Message(_ Grammar) string {
	return ""
}
