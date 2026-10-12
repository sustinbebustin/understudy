package sns_test

import (
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/snsapi"
	"github.com/sustinbebustin/understudy/internal/aws"
)

// ResourcePolicy reads a topic's policy for the delivery advisory: the
// default policy until one is set, then the one set; a topic that does not
// exist has none to read.
func TestResourcePolicyReadsTheTopicPolicy(t *testing.T) {
	run(t, func(h *harness) {
		out := must(h, snsapi.OpCreateTopic, &snsapi.CreateTopicInput{Name: "advised"})
		arn, err := aws.ParseARN(*out.TopicArn)
		if err != nil {
			h.Fatal(err)
		}
		doc, ok := h.svc.ResourcePolicy(arn)
		if !ok || doc == nil || doc.ID != "__default_policy_ID" {
			h.Fatalf("ResourcePolicy of a new topic = %+v, %t; want the default policy", doc, ok)
		}
		policy := `{"Version":"2012-10-17","Id":"set","Statement":[{"Effect":"Allow","Principal":{"Service":"s3.amazonaws.com"},"Action":"sns:Publish","Resource":"` + arn.String() + `"}]}`
		must(h, snsapi.OpSetTopicAttributes, &snsapi.SetTopicAttributesInput{TopicArn: *out.TopicArn, AttributeName: "Policy", AttributeValue: &policy})
		if doc, ok := h.svc.ResourcePolicy(arn); !ok || doc == nil || doc.ID != "set" || len(doc.Statements) != 1 {
			h.Errorf("ResourcePolicy after SetTopicAttributes = %+v, %t; want the policy set", doc, ok)
		}
		gone := arn
		gone.Resource = "missing"
		if doc, ok := h.svc.ResourcePolicy(gone); ok || doc != nil {
			h.Errorf("ResourcePolicy of a missing topic = %+v, %t; want none", doc, ok)
		}
	})
}
