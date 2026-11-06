package esm_test

import (
	"testing"

	"github.com/sustinbebustin/understudy/internal/service/lambda/esm"
)

// An SQS queue is the one kind of source a mapping polls today
// (docs/coverage.md, CreateEventSourceMapping: "SQS queues only"); every
// other EventSourceArn names no kind until its poller lands.
func TestSourceKindOf(t *testing.T) {
	kind, ok := esm.SourceKindOf(queueARN(t, "arn:aws:sqs:us-east-1:000000000000:orders"))
	if _, sqs := kind.(esm.SQS); !ok || !sqs {
		t.Errorf("SourceKindOf(an SQS queue) = %#v, %t; want SQS", kind, ok)
	}
	for _, arn := range []string{
		"arn:aws:kinesis:us-east-1:000000000000:stream/orders",
		"arn:aws:dynamodb:us-east-1:000000000000:table/orders/stream/2026-10-26T00:00:00.000",
		"arn:aws:kafka:us-east-1:000000000000:cluster/orders/abc",
		"arn:aws:mq:us-east-1:000000000000:broker:orders:b-1",
	} {
		if kind, ok := esm.SourceKindOf(queueARN(t, arn)); ok {
			t.Errorf("SourceKindOf(%s) = %#v, want no kind", arn, kind)
		}
	}
}
