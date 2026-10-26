package authz

import "github.com/sustinbebustin/understudy/internal/fidelity"

// Switches are the evaluation and context switches (iam-evaluation.md 12.3,
// iam-enforcement-surfaces.md 12.8), each at the one value understudy
// implements; the IAM service reports them.
//
// Not implemented yet: it returns none.
func Switches() []fidelity.Entry {
	return nil
}
