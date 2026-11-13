package ses

import (
	"slices"

	"github.com/sustinbebustin/understudy/internal/evidence"
	"github.com/sustinbebustin/understudy/internal/fidelity"
	"github.com/sustinbebustin/understudy/internal/kernel"
)

// The SES condition keys (SAR, iam-enforcement-surfaces.md 9.2).
const (
	keyFromAddress     = "ses:FromAddress"
	keyRecipients      = "ses:Recipients"
	keyFromDisplayName = "ses:FromDisplayName"
	keyFeedbackAddress = "ses:FeedbackAddress"
	keyAPIVersion      = "ses:ApiVersion"
)

// RefusedConditionKeys are the SES address keys AWS sends with a send
// that understudy does not supply on every binding
// (iam-enforcement-surfaces.md 12.5). Under the absent-key rule a Deny on, say, StringLike
// ses:FromAddress would never apply and a send AWS denies would go through,
// so each binding refuses them: under iam.enforce enforce a statement that
// tests one the call does not supply ends the call 501 naming the key. A
// key a call supplies is evaluated, refused or not, so v2 SendEmail, which
// supplies ses:FromAddress and ses:Recipients for Simple and Template
// content only, refuses them for Raw content alone. Where the non-send
// operations take them from is not settled either, so they refuse all four.
func RefusedConditionKeys() []string {
	return []string{keyFromAddress, keyRecipients, keyFromDisplayName, keyFeedbackAddress}
}

// SendRefusedConditionKeys are the keys v1 SendEmail and SendTemplatedEmail
// refuse: those of RefusedConditionKeys they do not supply. ses:FromDisplayName
// is unsettled for a send without a name, the commonest, and
// ses:FeedbackAddress for one without ReturnPath (iam-enforcement-surfaces.md
// 12.5).
func SendRefusedConditionKeys() []string {
	return slices.DeleteFunc(RefusedConditionKeys(), func(k string) bool { return k == keyFromAddress || k == keyRecipients })
}

// APIVersionV1 is switch ses.condition.api-version-v1's value: the
// ses:ApiVersion every v1 call carries. No AWS page states it
// (iam-enforcement-surfaces.md 12.5, inferred from v2's documented "2").
const APIVersionV1 = "1"

// apiVersionV2 is the ses:ApiVersion every v2 call carries: the SES
// access-control page's policy for API version 2 tests StringEquals "2"
// (iam-enforcement-surfaces.md 12.5, aws-doc).
const apiVersionV2 = "2"

// v1ConditionKeys is the condition-key option of every v1 binding but the
// Simple and templated sends: ses:ApiVersion, and the address keys refused.
func v1ConditionKeys() kernel.BindOption {
	return kernel.BindOptions(kernel.WithFixedConditionKeys(kernel.ConditionKeys{keyAPIVersion: {APIVersionV1}}),
		kernel.RefuseConditionKeys(RefusedConditionKeys()...))
}

// v1SendConditionKeys is v1 SendEmail's and SendTemplatedEmail's: v1's
// ses:ApiVersion, the address keys addresses supplies from the input, and
// the rest refused.
func v1SendConditionKeys(addresses kernel.BindOption) kernel.BindOption {
	return kernel.BindOptions(kernel.WithFixedConditionKeys(kernel.ConditionKeys{keyAPIVersion: {APIVersionV1}}),
		kernel.RefuseConditionKeys(SendRefusedConditionKeys()...), addresses)
}

// v2ConditionKeys is the condition-key option of every v2 binding:
// ses:ApiVersion "2", and the address keys refused.
func v2ConditionKeys() kernel.BindOption {
	return kernel.BindOptions(kernel.WithFixedConditionKeys(kernel.ConditionKeys{keyAPIVersion: {apiVersionV2}}),
		kernel.RefuseConditionKeys(RefusedConditionKeys()...))
}

// addressKeys are ses:FromAddress and ses:Recipients of a Simple or
// templated send from source to the to, cc and bcc addresses
// (iam-enforcement-surfaces.md 12.5). Each address is as written when bare,
// +label and case kept (aws-doc), and one written with a display name,
// "Name <address>", is the text between the angle brackets, case, +label
// and the quotes of a quoted local part kept (addrSpecText); that, and that
// ReplyToAddresses are not recipients, is switch ses.condition.address-form
// (inferred). ses:Recipients is multivalued, so a send with none carries it
// empty.
func addressKeys(source string, to, cc, bcc []string) kernel.ConditionKeys {
	recipients := make([]string, 0, len(to)+len(cc)+len(bcc))
	for _, a := range slices.Concat(to, cc, bcc) {
		recipients = append(recipients, addrSpecText(a))
	}
	return kernel.ConditionKeys{keyFromAddress: {addrSpecText(source)}, keyRecipients: recipients}
}

// AddressFormValue is switch ses.condition.address-form's value.
const AddressFormValue = "a Name <address> parameter carries the address between the angle brackets, case, +label and quotes as written; " +
	"ses:Recipients are To, Cc and Bcc, not ReplyTo"

// apiVersionV1Switch is switch ses.condition.api-version-v1, governing
// ops, the authorized v1 operations: every v1 action carries
// ses:ApiVersion (SAR), and its value is inferred.
func apiVersionV1Switch(ops []string) fidelity.Entry {
	return fidelity.Switch[string]{
		Name:    "ses.condition.api-version-v1",
		Value:   APIVersionV1,
		Label:   evidence.Inferred,
		Ref:     "docs/research/iam-enforcement-surfaces.md#125-context-keys",
		Record:  "SES-31",
		Ops:     ops,
		Settles: fidelity.NoScenario{Reason: "unrecorded: SES-31 steps 01, 02 and 04 (ses.md 12.2) have not been run"},
	}.Entry()
}

// addressFormSwitch is switch ses.condition.address-form: the form
// ses:FromAddress and ses:Recipients take from a parameter written with a
// display name, and that ReplyToAddresses are not recipients. It governs
// v2 SendEmail's Simple and Template content too, which, being another
// service's operation, it does not list.
func addressFormSwitch() fidelity.Entry {
	return fidelity.Switch[string]{
		Name:    "ses.condition.address-form",
		Value:   AddressFormValue,
		Label:   evidence.Inferred,
		Ref:     "docs/research/iam-enforcement-surfaces.md#125-context-keys",
		Record:  "SES-31",
		Ops:     []string{"ses.SendEmail", "ses.SendTemplatedEmail"},
		Settles: fidelity.NoScenario{Reason: "unrecorded: SES-31 steps 05, 06, 09, 10, 12 and 17 (ses.md 12.2) have not been run"},
	}.Entry()
}

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
