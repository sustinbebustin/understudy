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
// the key. Supplying them from the parsed
// send needs an iam-enforcement-surfaces.md section first.
func ConditionKeys() []string {
	return []string{"ses:FromAddress", "ses:Recipients", "ses:FromDisplayName", "ses:FeedbackAddress", "ses:ApiVersion"}
}

// refusedConditionKeys is the bind option every SES binding declares.
func refusedConditionKeys() kernel.BindOption { return kernel.RefuseConditionKeys(ConditionKeys()...) }

// DenialIdentityValue is switch ses.denial.identity's value: the identity a
// denial under iam.enforce answers with in each API, from the
// dev.understudy#denialError and dev.understudy#messageKey overlay traits,
// and the text, from enforce's grammar for SES actions.
const DenialIdentityValue = `v1 AccessDenied, Type Sender, 403; v2 AccessDeniedException in x-amzn-ErrorType, 403, body {"Message": "<text>"}; ` +
	`a send's text "User '<principal arn>' is not authorized to perform '<action>' on resource '<identity arn>'", every other action's the IAM grammar`

// denialSwitch is switch ses.denial.identity, governing ops. SES-28
// recorded v1 and v2 SendEmail's implicit denials (code, Type, status,
// v2's key "Message" and the send text naming the sender's identity), and
// v2 Raw content's as ses:SendRawEmail; IAM-ENF-38 and IAM-ENF-39 recorded
// the IAM grammar for a non-send action (ses.md 11.4). It stays inferred
// for what no capture covers, which follows the same text: v1
// SendRawEmail, v1 SendTemplatedEmail and the explicit-deny form. It
// governs v2's operations too, which, being another service's, it does
// not list.
func denialSwitch(ops []string) fidelity.Entry {
	return fidelity.Switch[string]{
		Name:   "ses.denial.identity",
		Value:  DenialIdentityValue,
		Label:  evidence.Inferred,
		Ref:    "docs/research/iam-enforcement-surfaces.md#123-error-identities-to-emit",
		Record: "SES-28",
		Ops:    ops,
		Settles: fidelity.NoScenario{Reason: "ses/SES-28 is recorded and leaves v1 SendRawEmail, v1 SendTemplatedEmail and the explicit-deny form " +
			"unrecorded (ses.md 11.4); the replay signs as the account's root, which no denial reaches"},
	}.Entry()
}
