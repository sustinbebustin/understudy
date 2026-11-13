package ses_test

import (
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/sesapi"
	"github.com/sustinbebustin/understudy/internal/api/sesv2api"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/service/ses"
)

// A ses.productionAccess list takes only its account and Region pairs out of
// the sandbox (ses.md 11.3, 6.1): a send, GetSendQuota and v2 GetAccount in
// any other account or Region are still the sandbox's.
func TestProductionAccessListLiftsListedPairsOnly(t *testing.T) {
	const stranger = "stranger@unverified.test"
	listed := aws.Scope{Partition: aws.PartitionAWS(), Account: aws.DefaultAccount(), Region: aws.DefaultRegion()}
	otherAccount, err := aws.ParseAccountID("111111111111")
	if err != nil {
		t.Fatal(err)
	}
	opts := ses.Options{ProductionAccess: ses.ProductionAccess{Pairs: []ses.AccountRegion{{Account: listed.Account, Region: listed.Region}}}}
	runVerified(t, opts, func(h *harness) {
		otherRegion := listed
		otherRegion.Region = westRegion(h)
		elsewhere := listed
		elsewhere.Account = otherAccount
		for _, tc := range []struct {
			name       string
			scope      aws.Scope
			production bool
		}{
			{"listed pair", listed, true},
			{"other Region", otherRegion, false},
			{"other account", elsewhere, false},
		} {
			h.Scope = tc.scope
			h.verifyEmail(sender)
			_, sendErr := call(h, sesapi.OpSendEmail, email(sender, stranger))
			if (sendErr == nil) != tc.production {
				h.Errorf("%s: send to an unverified recipient error %+v, want accepted %v", tc.name, sendErr, tc.production)
			}
			wantQuota, wantRate := ses.SandboxMax24HourSend, ses.SandboxMaxSendRate
			if tc.production {
				wantQuota, wantRate = ses.ProductionMax24HourSend, ses.ProductionMaxSendRate
			}
			if q := h.quota(); q.Max24HourSend != wantQuota || q.MaxSendRate != wantRate {
				h.Errorf("%s: GetSendQuota = %+v, want %v and %v", tc.name, q, wantQuota, wantRate)
			}
			acct := must(h, sesv2api.OpGetAccount, &sesv2api.GetAccountInput{})
			if acct.ProductionAccessEnabled != tc.production || acct.SendQuota == nil ||
				acct.SendQuota.Max24HourSend != wantQuota || acct.SendQuota.MaxSendRate != wantRate {
				h.Errorf("%s: GetAccount = %+v quota %+v, want production %v", tc.name, acct, acct.SendQuota, tc.production)
			}
		}
	})
}
