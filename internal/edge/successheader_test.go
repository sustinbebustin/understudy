package edge_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/edge"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/protocol/awsjson"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/server"
)

// A header a handler sets with Call.SetSuccessHeader reaches the wire
// beside the codec's answer when the call succeeds, and is not sent when
// the same call fails (ARCHITECTURE.md 3). The edge writes it for every
// protocol; awsJson stands for them here.
func TestASuccessHeaderReachesTheWireOnlyOnSuccess(t *testing.T) {
	const name = "X-Test-Success"
	kerneltest.Run(t, func(k *kerneltest.K) {
		t := k.T
		k.Stub(sqsapi.OpGetQueueUrl, func(_ context.Context, c *kernel.Call, in *sqsapi.GetQueueUrlInput) (*sqsapi.GetQueueUrlOutput, *awserr.Error) {
			c.SetSuccessHeader(name, "set")
			if in.QueueName == "missing" {
				return nil, sqsapi.ErrQueueDoesNotExist("The specified queue does not exist.")
			}
			return &sqsapi.GetQueueUrlOutput{QueueUrl: new("u")}, nil
		})
		front, err := edge.New(edge.Options{
			Kernel: k.Kernel, Registry: k.Kernel.Registry(), Identity: k.Identity, Regions: aws.KnownRegions(),
			Codecs:   map[ref.ProtocolID]edge.Codec{ref.AWSJSON10: server.JSONCodec(awsjson.V10)},
			Listener: ref.ListenerMain, Unclaimed: ref.AWSJSON10,
		})
		if err != nil {
			t.Fatal(err)
		}
		send := func(queue string) *httptest.ResponseRecorder {
			r := request(t, http.MethodPost, "/", jsonHeader("AmazonSQS.GetQueueUrl", "application/x-amz-json-1.0"), `{"QueueName":"`+queue+`"}`, "sqs")
			rec := httptest.NewRecorder()
			front.ServeHTTP(rec, r)
			return rec
		}

		ok := send("orders")
		if ok.Code != http.StatusOK || ok.Header().Get(name) != "set" {
			t.Errorf("a success = %d with %s %q, want 200 with %q; body %s", ok.Code, name, ok.Header().Get(name), "set", ok.Body)
		}
		if ok.Header().Get("Content-Type") == "" {
			t.Errorf("a success lost the codec's Content-Type: %v", ok.Header())
		}

		failed := send("missing")
		if failed.Code != http.StatusBadRequest {
			t.Errorf("a failure = %d, want 400; body %s", failed.Code, failed.Body)
		}
		if got, sent := failed.Header()[http.CanonicalHeaderKey(name)]; sent {
			t.Errorf("a failure sent %s %q, want no such header", name, got)
		}
	}, kerneltest.Services(sqsapi.Service()))
}
