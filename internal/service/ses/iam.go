package ses

import (
	"github.com/sustinbebustin/understudy/internal/evidence"
	"github.com/sustinbebustin/understudy/internal/fidelity"
	"github.com/sustinbebustin/understudy/internal/kernel"
)

// ConditionKeys are the SES condition keys AWS sends with SES requests
// (SAR, ses.md 2.1) that understudy does not supply. Under the absent-key
// rule a Deny on, say, StringLike ses:FromAddress would never apply and a
// send AWS denies would go through, so every binding refuses them: under
// iam.enforce enforce a statement that tests one ends the call 501 naming
// the key (09g-ses spec, enforce matrix). Supplying them from the parsed
// send needs an iam-enforcement-surfaces.md section first.
func ConditionKeys() []string {
	return []string{"ses:FromAddress", "ses:Recipients", "ses:FromDisplayName", "ses:FeedbackAddress", "ses:ApiVersion"}
}

// refusedConditionKeys is the bind option every SES binding declares.
func refusedConditionKeys() kernel.BindOption { return kernel.RefuseConditionKeys(ConditionKeys()...) }

// DenialIdentityValue is switch ses.denial.identity's value: the identity a
// denial under iam.enforce answers with in each API, from the
// dev.understudy#denialError overlay traits.
const DenialIdentityValue = `v1 AccessDenied, Type Sender, 403, the IAM grammar; v2 AccessDeniedException in x-amzn-ErrorType, 403, body {"message": "<grammar>"}`

// denialSwitch is switch ses.denial.identity, governing ops. Both
// identities were inferred from the STS (aws-recorded) and IAM (inferred)
// families, as iam-enforcement-surfaces.md 12.3 requires a switch for
// every inferred denial identity. SES-28 recorded the codes, Type and
// status as inferred, but a message without the IAM grammar's "because"
// clause and, on v2, under "Message" (ses.md 11.4); understudy still
// sends the IAM grammar. It governs v2's operations too, which, being
// another service's, it does not list.
func denialSwitch(ops []string) fidelity.Entry {
	return fidelity.Switch[string]{
		Name:   "ses.denial.identity",
		Value:  DenialIdentityValue,
		Label:  evidence.Inferred,
		Ref:    "docs/research/iam-enforcement-surfaces.md#123-error-identities-to-emit",
		Record: "SES-28",
		Ops:    ops,
		Settles: fidelity.NoScenario{Reason: "ses/SES-28 is recorded and contradicts the message and the v2 member name " +
			"(ses.md 11.4); the replay signs as the account's root, which no denial reaches"},
	}.Entry()
}
