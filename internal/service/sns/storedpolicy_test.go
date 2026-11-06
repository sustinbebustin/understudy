package sns_test

import (
	"reflect"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/snsapi"
	"github.com/sustinbebustin/understudy/internal/authz"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/policydoc"
)

// The evaluator reads a topic's policy through StoredPolicyReader: the
// synthesized default policy until one is set (iam-enforcement-surfaces.md
// 7.2), then the one set, parsed, owned by the topic's account; a topic that
// does not exist names none, as ResourcePolicy answers.
func TestStoredPolicyReadsTheTopicPolicy(t *testing.T) {
	run(t, func(h *harness) {
		var reader authz.StoredPolicyReader = h.svc
		out := must(h, snsapi.OpCreateTopic, &snsapi.CreateTopicInput{Name: "guarded"})
		arn, err := aws.ParseARN(*out.TopicArn)
		if err != nil {
			h.Fatal(err)
		}
		got, ok := reader.StoredPolicy(arn)
		parsed, isParsed := got.Policy.(authz.Parsed)
		if !ok || got.Owner != aws.DefaultAccount() || !isParsed || parsed.Document.ID != "__default_policy_ID" {
			h.Fatalf("StoredPolicy of a new topic = %+v, %t; want the default policy, parsed, owned by the topic's account", got, ok)
		}
		policy := `{"Version":"2012-10-17","Id":"set","Statement":[{"Effect":"Allow","Principal":{"Service":"s3.amazonaws.com"},"Action":"sns:Publish","Resource":"` + arn.String() + `"}]}`
		must(h, snsapi.OpSetTopicAttributes, &snsapi.SetTopicAttributesInput{TopicArn: *out.TopicArn, AttributeName: "Policy", AttributeValue: &policy})
		got, ok = reader.StoredPolicy(arn)
		if want := (authz.StoredPolicy{Owner: aws.DefaultAccount(), Policy: authz.Parsed{Document: *policydoc.ParseStored(policy)}}); !ok || !reflect.DeepEqual(got, want) {
			h.Errorf("StoredPolicy after SetTopicAttributes = %+v, %t; want %+v", got, ok, want)
		}
		gone := arn
		gone.Resource = "missing"
		if got, ok := reader.StoredPolicy(gone); ok {
			h.Errorf("StoredPolicy of a missing topic = %+v, want none", got)
		}
	})
}
