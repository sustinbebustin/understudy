package secretsmanager_test

import (
	"testing"

	sm "github.com/sustinbebustin/understudy/internal/api/secretsmanagerapi"
	"github.com/sustinbebustin/understudy/internal/journal"
)

// With BlockPublicPolicy true, AWS refuses a policy that does not prevent
// broad access with PublicPolicyException (iam-enforcement-surfaces.md
// 7.6). understudy does not run that check, so a put sending it true
// journals a check-not-run advisory naming the secret, and one sending it
// false or not at all journals none.
func TestBlockPublicPolicyJournalsCheckNotRun(t *testing.T) {
	run(t, func(h *harness) {
		t := h.T
		arn := *h.create("blocked", "v").ARN
		const policy = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::000000000000:root"},"Action":"secretsmanager:GetSecretValue","Resource":"*"}]}`
		unchecked := func() []string {
			var out []string
			events := h.Journal.Query(journal.Filter{Kind: journal.KindAdvisory}).Events
			for i := range events {
				if a, ok := events[i].Detail.(journal.Advisory); ok && a.Kind == journal.AdvisoryCheckNotRun {
					out = append(out, a.Resource.String())
				}
			}
			return out
		}
		must(h, sm.OpPutResourcePolicy, &sm.PutResourcePolicyInput{SecretId: arn, ResourcePolicy: policy})
		must(h, sm.OpPutResourcePolicy, &sm.PutResourcePolicyInput{SecretId: arn, ResourcePolicy: policy, BlockPublicPolicy: new(false)})
		if got := unchecked(); len(got) != 0 {
			t.Fatalf("advisories without BlockPublicPolicy = %v, want none", got)
		}
		must(h, sm.OpPutResourcePolicy, &sm.PutResourcePolicyInput{SecretId: arn, ResourcePolicy: policy, BlockPublicPolicy: new(true)})
		if got := unchecked(); len(got) != 1 || got[0] != arn {
			t.Errorf("check-not-run advisories name %v, want [%s]", got, arn)
		}
	})
}
