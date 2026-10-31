package kms_test

import (
	"slices"
	"testing"

	"github.com/sustinbebustin/understudy/internal/access"
	"github.com/sustinbebustin/understudy/internal/api/kmsapi"
)

// CreateKey's actions are recorded on "*", where step 8 authorizes them:
// the Service Authorization Reference lists no resource type for
// kms:CreateKey, and the key the call creates has no ARN before it runs,
// so a policy rendered from the record must grant them on "*", not on the
// created key (iam-enforcement-surfaces.md 10.2, 10.4, 12.6).
func TestCreateKeyIsRecordedOnAnyResource(t *testing.T) {
	run(t, func(h *harness) {
		must(h, kmsapi.OpCreateKey, &kmsapi.CreateKeyInput{Tags: []kmsapi.Tag{{TagKey: "team", TagValue: "a"}}})
		uses := h.Access.Usage(access.Filter{Principal: access.Root{Account: h.Scope.Account}, Service: "kms"})
		got := make([]string, 0, len(uses))
		for i := range uses {
			on := "*"
			if r, ok := uses[i].Resource.(access.OnResource); ok {
				on = r.ARN.String()
			}
			got = append(got, uses[i].Action.String()+" on "+on)
		}
		slices.Sort(got)
		want := []string{"kms:CreateKey on *", "kms:TagResource on *"}
		if !slices.Equal(got, want) {
			t.Errorf("recorded kms uses = %v, want %v", got, want)
		}
	})
}
