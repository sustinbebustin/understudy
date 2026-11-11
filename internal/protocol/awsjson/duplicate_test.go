package awsjson_test

import (
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/kmsapi"
	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
	"github.com/sustinbebustin/understudy/internal/protocol/awsjson"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// Only DynamoDB is recorded refusing a repeated member
// (testdata/parity/dynamodb/R11/06-dynamodb-duplicate-member.json), so the
// other awsJson services, SQS and KMS among them, keep the last value; what
// their front ends do is unrecorded.
func TestServicesWithoutTheTraitKeepTheLastDuplicate(t *testing.T) {
	operation := func(svc *registry.Service, name string) *registry.Operation {
		t.Helper()
		for _, o := range svc.Operations {
			if o.ID.Name == name {
				return o
			}
		}
		t.Fatalf("%s has no operation %s", svc.ID, name)
		return nil
	}
	decode := func(codec awsjson.Codec, target, contentType string, op *registry.Operation, body string, in smithy.Deserializable) {
		t.Helper()
		if aerr := codec.Decode(request(target, contentType, []byte(body)), op, in); aerr != nil {
			t.Fatalf("Decode(%s) = %s %q, want the last value kept", body, aerr.ID.Shape, aerr.Message)
		}
	}

	var queue sqsapi.GetQueueUrlInput
	decode(awsjson.Codec{Version: awsjson.V10}, "AmazonSQS.GetQueueUrl", "application/x-amz-json-1.0",
		operation(sqsapi.Service(), "GetQueueUrl"), `{"QueueName":"first","QueueName":"last"}`, &queue)
	if queue.QueueName != "last" {
		t.Errorf("SQS QueueName = %q, want last", queue.QueueName)
	}

	var key kmsapi.DescribeKeyInput
	decode(awsjson.Codec{Version: awsjson.V11}, "TrentService.DescribeKey", "application/x-amz-json-1.1",
		operation(kmsapi.Service(), "DescribeKey"), `{"KeyId":"first","KeyId":"last"}`, &key)
	if key.KeyId != "last" {
		t.Errorf("KMS KeyId = %q, want last", key.KeyId)
	}
}
