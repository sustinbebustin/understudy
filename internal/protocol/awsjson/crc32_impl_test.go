package awsjson_test

import (
	"hash/crc32"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/dynamodbstreamsapi"
	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
	"github.com/sustinbebustin/understudy/internal/protocol/awsjson"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// A service declaring dev.understudy#responseCrc32 (DynamoDB Streams)
// answers every encoded body, success and error, with x-amz-crc32, the
// CRC-32 (IEEE) of the body in decimal (aws-recorded on a 200 and a 400:
// testdata/parity/dynamodb/R11/07-streams-signed-dynamodb.json,
// 07-streams-signed-streams.dynamodb.json; dynamodb-streams.md 3.1). A
// service without it, SQS, carries no such header.
func TestResponseCRC32(t *testing.T) {
	codec := awsjson.Codec{Version: awsjson.V10}
	op := func(svc *registry.Service, name string) *registry.Operation {
		t.Helper()
		for _, o := range svc.Operations {
			if o.ID.Name == name {
				return o
			}
		}
		t.Fatalf("%s has no operation %s", svc.ID, name)
		return nil
	}
	sum := func(body []byte) string { return strconv.FormatUint(uint64(crc32.ChecksumIEEE(body)), 10) }
	r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "http://localhost/", http.NoBody)

	streams := dynamodbstreamsapi.Service()
	ok, aerr := codec.Encode(op(streams, "ListStreams"), requestID, &dynamodbstreamsapi.ListStreamsOutput{Streams: []dynamodbstreamsapi.Stream{}}, http.StatusOK)
	if aerr != nil {
		t.Fatal(aerr.AsError())
	}
	// The recorded 200's body and checksum.
	if string(ok.Body) != `{"Streams":[]}` || ok.Header.Get("X-Amz-Crc32") != "1490112021" {
		t.Errorf("200: body %s, x-amz-crc32 %q; want the recorded {\"Streams\":[]} and 1490112021", ok.Body, ok.Header.Get("X-Amz-Crc32"))
	}
	e := codec.EncodeError(r, streams, requestID, dynamodbstreamsapi.ErrResourceNotFoundException("Stream does not exist"))
	if got := e.Header.Get("X-Amz-Crc32"); e.Status != http.StatusBadRequest || got != sum(e.Body) {
		t.Errorf("400: x-amz-crc32 %q over %s, want %s", got, e.Body, sum(e.Body))
	}

	queues := sqsapi.Service()
	plain, aerr := codec.Encode(op(queues, "ListQueues"), requestID, &sqsapi.ListQueuesOutput{}, http.StatusOK)
	if aerr != nil {
		t.Fatal(aerr.AsError())
	}
	if got := plain.Header.Get("X-Amz-Crc32"); got != "" {
		t.Errorf("SQS 200 carries x-amz-crc32 %q", got)
	}
	if got := codec.EncodeError(r, queues, requestID, sqsapi.ErrQueueDoesNotExist("gone")).Header.Get("X-Amz-Crc32"); got != "" {
		t.Errorf("SQS 400 carries x-amz-crc32 %q", got)
	}
}
