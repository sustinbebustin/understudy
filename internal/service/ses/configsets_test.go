package ses_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/sustinbebustin/understudy/internal/api/sesapi"
	"github.com/sustinbebustin/understudy/internal/api/sesv2api"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/service/ses"
)

// createSet creates configuration set name through v1.
func (h *harness) createSet(name string) {
	h.Helper()
	must(h, sesapi.OpCreateConfigurationSet, &sesapi.CreateConfigurationSetInput{ConfigurationSet: sesapi.ConfigurationSet{Name: name}})
}

// setMissingText is the recorded answer for a configuration set the account
// and Region do not hold, the angle brackets part of AWS's text (SES-17
// step 02; third-party-recorded for DeleteConfigurationSet, ses.md 8).
func setMissingText(name string) string {
	return fmt.Sprintf("Configuration set <%s> does not exist.", name)
}

// Golden test_deleting_non_existent_configuration_set: DeleteConfigurationSet
// of a missing set is ConfigurationSetDoesNotExist, Type Sender, 400, with
// the recorded text (third-party-recorded 2023-08-25, ses.md 8;
// localstack@8b9a79f tests/aws/services/ses/test_ses.py L830-L837).
// DescribeConfigurationSet answers a created set by name and, once it is
// deleted, the same refusal.
func TestConfigurationSetCreateDescribeDelete(t *testing.T) {
	run(t, func(h *harness) {
		_, err := call(h, sesapi.OpDeleteConfigurationSet, &sesapi.DeleteConfigurationSetInput{ConfigurationSetName: "missing"})
		wantErr(h, err, sesapi.IdentityConfigurationSetDoesNotExistException(), setMissingText("missing"))
		if err.ID.Fault != awserr.Sender {
			h.Errorf("Type = %s, want Sender", err.ID.Fault)
		}

		h.createSet("orders")
		out := must(h, sesapi.OpDescribeConfigurationSet, &sesapi.DescribeConfigurationSetInput{ConfigurationSetName: "orders"})
		if out.ConfigurationSet == nil || out.ConfigurationSet.Name != "orders" {
			h.Errorf("DescribeConfigurationSet = %+v, want ConfigurationSet orders", out)
		}

		must(h, sesapi.OpDeleteConfigurationSet, &sesapi.DeleteConfigurationSetInput{ConfigurationSetName: "orders"})
		_, err = call(h, sesapi.OpDescribeConfigurationSet, &sesapi.DescribeConfigurationSetInput{ConfigurationSetName: "orders"})
		wantErr(h, err, sesapi.IdentityConfigurationSetDoesNotExistException(), setMissingText("orders"))
	})
}

// v2 CreateConfigurationSet stores its options and tags, which
// GetConfigurationSet echoes (StoredOnly, ses.md 11.1) and v1
// DescribeConfigurationSet reads as far as v1 has them; Get and Delete of a
// missing set are NotFoundException with v1's text (inferred, ses.md 8).
func TestV2ConfigurationSetEchoesWhatWasStored(t *testing.T) {
	run(t, func(h *harness) {
		must(h, sesv2api.OpCreateConfigurationSet, &sesv2api.CreateConfigurationSetInput{
			ConfigurationSetName: "marketing",
			DeliveryOptions:      &sesv2api.DeliveryOptions{TlsPolicy: new(sesv2api.TlsPolicyRequire)},
			SendingOptions:       &sesv2api.SendingOptions{SendingEnabled: false},
			Tags:                 []sesv2api.Tag{{Key: "team", Value: "mail"}},
		})
		got := must(h, sesv2api.OpGetConfigurationSet, &sesv2api.GetConfigurationSetInput{ConfigurationSetName: "marketing"})
		want := &sesv2api.GetConfigurationSetOutput{
			ConfigurationSetName: new("marketing"),
			DeliveryOptions:      &sesv2api.DeliveryOptions{TlsPolicy: new(sesv2api.TlsPolicyRequire)},
			SendingOptions:       &sesv2api.SendingOptions{SendingEnabled: false},
			Tags:                 []sesv2api.Tag{{Key: "team", Value: "mail"}},
		}
		if diff := cmp.Diff(want, got, cmpopts.IgnoreUnexported(sesv2api.Tag{})); diff != "" {
			h.Errorf("GetConfigurationSet (-want +got):\n%s", diff)
		}

		v1 := must(h, sesapi.OpDescribeConfigurationSet, &sesapi.DescribeConfigurationSetInput{
			ConfigurationSetName: "marketing",
			ConfigurationSetAttributeNames: []sesapi.ConfigurationSetAttribute{
				sesapi.ConfigurationSetAttributeDeliveryOptions, sesapi.ConfigurationSetAttributeReputationOptions,
			},
		})
		if v1.DeliveryOptions == nil || v1.DeliveryOptions.TlsPolicy == nil || *v1.DeliveryOptions.TlsPolicy != sesapi.TlsPolicyRequire {
			h.Errorf("v1 DeliveryOptions = %+v, want TlsPolicy Require", v1.DeliveryOptions)
		}
		if r := v1.ReputationOptions; r == nil || r.SendingEnabled || r.ReputationMetricsEnabled {
			h.Errorf("v1 ReputationOptions = %+v, want sending and metrics disabled", r)
		}

		must(h, sesv2api.OpDeleteConfigurationSet, &sesv2api.DeleteConfigurationSetInput{ConfigurationSetName: "marketing"})
		_, err := call(h, sesv2api.OpGetConfigurationSet, &sesv2api.GetConfigurationSetInput{ConfigurationSetName: "marketing"})
		wantErr(h, err, sesv2api.IdentityNotFoundException(), setMissingText("marketing"))
		_, err = call(h, sesv2api.OpDeleteConfigurationSet, &sesv2api.DeleteConfigurationSetInput{ConfigurationSetName: "marketing"})
		wantErr(h, err, sesv2api.IdentityNotFoundException(), setMissingText("marketing"))
	})
}

// setARN is the ARN of configuration set name in h.Scope, the form
// Terraform builds (ses.md 11.7).
func (h *harness) setARN(name string) string {
	return "arn:aws:ses:" + h.Scope.Region.String() + ":" + h.Scope.Account.String() + ":configuration-set/" + name
}

// The v2 tag operations read and change a configuration set's tags through
// its ARN, which aws_sesv2_configuration_set reads its tags by, also with
// the empty account skip_requesting_account_id leaves in it (ses.md 11.7);
// a missing set is NotFoundException.
func TestConfigurationSetTagsByARN(t *testing.T) {
	run(t, func(h *harness) {
		h.createSet("tagged")
		arn := h.setARN("tagged")
		must(h, sesv2api.OpTagResource, &sesv2api.TagResourceInput{ResourceArn: arn, Tags: []sesv2api.Tag{{Key: "env", Value: "dev"}}})
		noAccount := strings.Replace(arn, h.Scope.Account.String(), "", 1)
		tags := must(h, sesv2api.OpListTagsForResource, &sesv2api.ListTagsForResourceInput{ResourceArn: noAccount}).Tags
		if diff := cmp.Diff([]sesv2api.Tag{{Key: "env", Value: "dev"}}, tags, cmpopts.IgnoreUnexported(sesv2api.Tag{})); diff != "" {
			h.Errorf("ListTagsForResource (-want +got):\n%s", diff)
		}
		calls := h.apiCalls("sesv2", "TagResource")
		if len(calls) != 1 {
			h.Fatalf("%d TagResource calls journaled", len(calls))
		}
		wantTouches(h, calls[0].Resources, arn)

		_, err := call(h, sesv2api.OpListTagsForResource, &sesv2api.ListTagsForResourceInput{ResourceArn: h.setARN("gone")})
		wantErr(h, err, sesv2api.IdentityNotFoundException(), setMissingText("gone"))
	})
}

// An account and Region hold at most 10,000 configuration sets, across both
// APIs (ses.md 8): the next is the model's LimitExceeded (the text is
// inferred).
func TestConfigurationSetLimit(t *testing.T) {
	run(t, func(h *harness) {
		for i := range ses.MaxConfigurationSets {
			h.createSet(fmt.Sprintf("set-%d", i))
		}
		limitText := fmt.Sprintf(ses.ConfigurationSetLimitText, ses.MaxConfigurationSets)
		_, err := call(h, sesapi.OpCreateConfigurationSet, &sesapi.CreateConfigurationSetInput{ConfigurationSet: sesapi.ConfigurationSet{Name: "one-more"}})
		wantErr(h, err, sesapi.IdentityLimitExceededException(), limitText)
		_, err = call(h, sesv2api.OpCreateConfigurationSet, &sesv2api.CreateConfigurationSetInput{ConfigurationSetName: "one-more"})
		wantErr(h, err, sesv2api.IdentityLimitExceededException(), limitText)
	})
}

// A name of up to 64 ASCII letters, digits, "-" and "_" is accepted (ses.md
// 8); any other is refused, and so is a name already in use, whichever API
// made it (the texts are inferred).
func TestConfigurationSetNameRules(t *testing.T) {
	run(t, func(h *harness) {
		h.createSet(strings.Repeat("a", ses.ConfigurationSetNameMaxLength))
		h.createSet("Az09-_")
		for _, bad := range []string{"", strings.Repeat("a", ses.ConfigurationSetNameMaxLength+1), "dot.ted", "sp ace"} {
			_, err := call(h, sesapi.OpCreateConfigurationSet, &sesapi.CreateConfigurationSetInput{ConfigurationSet: sesapi.ConfigurationSet{Name: bad}})
			wantErr(h, err, sesapi.IdentityInvalidConfigurationSetException(), fmt.Sprintf(ses.ConfigurationSetNameText, bad))
			_, err = call(h, sesv2api.OpCreateConfigurationSet, &sesv2api.CreateConfigurationSetInput{ConfigurationSetName: bad})
			wantErr(h, err, sesv2api.IdentityBadRequestException(), fmt.Sprintf(ses.ConfigurationSetNameText, bad))
		}
		_, err := call(h, sesv2api.OpCreateConfigurationSet, &sesv2api.CreateConfigurationSetInput{ConfigurationSetName: "Az09-_"})
		wantErr(h, err, sesv2api.IdentityAlreadyExistsException(), fmt.Sprintf(ses.ConfigurationSetExistsText, "Az09-_"))
		_, err = call(h, sesapi.OpCreateConfigurationSet, &sesapi.CreateConfigurationSetInput{ConfigurationSet: sesapi.ConfigurationSet{Name: "Az09-_"}})
		wantErr(h, err, sesapi.IdentityConfigurationSetAlreadyExistsException(), fmt.Sprintf(ses.ConfigurationSetExistsText, "Az09-_"))
	})
}

// A send naming a missing configuration set from an unverified sender is
// refused for the set, not the sender (recorded for v1 SendEmail, SES-17
// step 02, ses.md 6.4); v2 answers NotFoundException with the same text
// (inferred, ses.md 8). Nothing is recorded.
func TestMissingConfigurationSetIsCheckedBeforeTheSender(t *testing.T) {
	run(t, func(h *harness) {
		const nobody = "us1103-nobody@example.com"
		in := email(nobody, simulator)
		in.ConfigurationSetName = new("us1103-missing")
		_, err := call(h, sesapi.OpSendEmail, in)
		wantErr(h, err, sesapi.IdentityConfigurationSetDoesNotExistException(), setMissingText("us1103-missing"))

		v2 := simpleEmail(nobody, simulator)
		v2.ConfigurationSetName = new("us1103-missing")
		_, err = call(h, sesv2api.OpSendEmail, v2)
		wantErr(h, err, sesv2api.IdentityNotFoundException(), setMissingText("us1103-missing"))

		raw := rawEmail(mime("body\r\n", "From: "+nobody, "To: "+simulator, "X-SES-CONFIGURATION-SET: us1103-missing"))
		_, err = call(h, sesapi.OpSendRawEmail, raw)
		wantErr(h, err, sesapi.IdentityConfigurationSetDoesNotExistException(), setMissingText("us1103-missing"))
		if got := len(h.sends()); got != 0 {
			h.Errorf("%d messages recorded from refused sends", got)
		}
	})
}

// A send naming an existing configuration set, by member or by the raw
// X-SES-CONFIGURATION-SET header, records it on the message and touches its
// ARN in the journaled call (ses.md 11.2).
func TestSendRecordsItsConfigurationSet(t *testing.T) {
	runVerified(t, ses.Options{}, func(h *harness) {
		h.createSet("orders")
		v1 := email(sender, simulator)
		v1.ConfigurationSetName = new("orders")
		must(h, sesapi.OpSendEmail, v1)
		must(h, sesapi.OpSendRawEmail, rawEmail(mime("body\r\n", "From: "+sender, "To: "+simulator, "X-SES-CONFIGURATION-SET: orders")))
		v2 := simpleEmail(sender, simulator)
		v2.ConfigurationSetName = new("orders")
		must(h, sesv2api.OpSendEmail, v2)

		sends := h.sends()
		if len(sends) != 3 {
			h.Fatalf("%d messages recorded, want 3", len(sends))
		}
		for _, m := range sends {
			if m.ConfigurationSet != "orders" {
				h.Errorf("%s.%s recorded configuration set %q, want orders", m.API, m.Operation, m.ConfigurationSet)
			}
		}
		for _, c := range []struct{ svc, op string }{{"ses", "SendEmail"}, {"ses", "SendRawEmail"}, {"sesv2", "SendEmail"}} {
			calls := h.apiCalls(c.svc, c.op)
			if len(calls) != 1 {
				h.Fatalf("%d %s.%s calls journaled, want 1", len(calls), c.svc, c.op)
			}
			wantTouches(h, calls[0].Resources, h.setARN("orders"))
			wantTouches(h, calls[0].Resources, h.identityARN(sender))
		}
	})
}

// Switch ses.store.shared-v1-v2 (configuration sets inferred, ses.md
// 11.4): shared, a set either API made is the other's; separate, each API
// lists, reads and sends with only its own.
func TestConfigurationSetsFollowTheStoreSwitch(t *testing.T) {
	run(t, func(h *harness) {
		h.createSet("by-v1")
		got := must(h, sesv2api.OpListConfigurationSets, &sesv2api.ListConfigurationSetsInput{}).ConfigurationSets
		if len(got) != 1 || got[0] != "by-v1" {
			h.Errorf("switch ses.store.shared-v1-v2=%s: v2 lists %q, want [by-v1]", ses.StoreShared, got)
		}
	})
	runVerified(t, ses.Options{Store: ses.StoreSeparate}, func(h *harness) {
		h.createSet("by-v1")
		if got := must(h, sesv2api.OpListConfigurationSets, &sesv2api.ListConfigurationSetsInput{}).ConfigurationSets; len(got) != 0 {
			h.Errorf("switch ses.store.shared-v1-v2=%s: v2 lists %q, want none", ses.StoreSeparate, got)
		}
		_, err := call(h, sesv2api.OpGetConfigurationSet, &sesv2api.GetConfigurationSetInput{ConfigurationSetName: "by-v1"})
		wantErr(h, err, sesv2api.IdentityNotFoundException(), setMissingText("by-v1"))
		must(h, sesv2api.OpCreateConfigurationSet, &sesv2api.CreateConfigurationSetInput{ConfigurationSetName: "by-v1"})
		h.createIdentity(sender)
		v2 := simpleEmail(sender, simulator)
		v2.ConfigurationSetName = new("by-v1")
		must(h, sesv2api.OpSendEmail, v2)
	})
}
