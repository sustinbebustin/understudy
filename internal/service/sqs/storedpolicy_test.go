package sqs_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
	"github.com/sustinbebustin/understudy/internal/authz"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/policydoc"
)

// The evaluator reads a queue's Policy through StoredPolicyReader: none for
// a new queue ("A new SQS queue has an empty policy",
// iam-enforcement-surfaces.md 7.1), the parsed document once one is set,
// and the text with its parse error when SetQueueAttributes stored a JSON
// object policydoc cannot read, so an explicit Deny in it is never read as
// no policy (12.10). The owner is the queue's account; a queue that does
// not exist names none, as ResourcePolicy answers.
func TestStoredPolicyReadsTheQueuePolicy(t *testing.T) {
	run(t, func(h *harness) {
		var reader authz.StoredPolicyReader = h.svc
		url := h.queue("guarded", nil)
		arn, err := aws.ParseARN("arn:aws:sqs:us-east-1:000000000000:guarded")
		if err != nil {
			h.Fatal(err)
		}
		set := func(policy string) {
			h.Helper()
			_, err := h.Invoke(h.Context(), op[sqsapi.SetQueueAttributesInput, sqsapi.SetQueueAttributesOutput](h, "SetQueueAttributes"),
				&sqsapi.SetQueueAttributesInput{QueueUrl: url, Attributes: map[sqsapi.QueueAttributeName]string{sqsapi.QueueAttributeNamePolicy: policy}})
			if err != nil {
				h.Fatalf("SetQueueAttributes(Policy) = %s: %s", err.ID.QueryCode, err.Message)
			}
		}

		if got, ok := reader.StoredPolicy(arn); !ok || got != (authz.StoredPolicy{Owner: aws.DefaultAccount(), Policy: authz.NoPolicy{}}) {
			h.Errorf("StoredPolicy of a new queue = %+v, %t; want NoPolicy owned by the queue's account", got, ok)
		}

		const policy = `{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Principal":"*","Action":"sqs:*","Resource":"arn:aws:sqs:us-east-1:000000000000:guarded"}]}`
		set(policy)
		got, ok := reader.StoredPolicy(arn)
		if want := (authz.StoredPolicy{Owner: aws.DefaultAccount(), Policy: authz.Parsed{Document: *policydoc.ParseStored(policy)}}); !ok || !reflect.DeepEqual(got, want) {
			h.Errorf("StoredPolicy after SetQueueAttributes = %+v, %t; want %+v", got, ok, want)
		}

		// A JSON object with a Statement, which SQS stores, that policydoc
		// refuses (an unknown element).
		const unreadable = `{"Statement":[{"Effect":"Deny","Principal":"*","Action":"sqs:*","Resource":"*","Bogus":1}]}`
		set(unreadable)
		got, ok = reader.StoredPolicy(arn)
		u, isUnparsed := got.Policy.(authz.Unparsed)
		var parseErr *policydoc.ParseError
		if !ok || !isUnparsed || u.Text != unreadable || !errors.As(u.Err, &parseErr) {
			h.Errorf("StoredPolicy of an unreadable policy = %+v, %t; want Unparsed with the text and the *policydoc.ParseError", got, ok)
		}

		gone := arn
		gone.Resource = "missing"
		if got, ok := reader.StoredPolicy(gone); ok {
			h.Errorf("StoredPolicy of a missing queue = %+v, want none", got)
		}
	})
}
