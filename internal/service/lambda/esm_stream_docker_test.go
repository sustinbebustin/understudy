//go:build docker

package lambda_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/dynamodbapi"
	"github.com/sustinbebustin/understudy/internal/api/dynamodbstreamsapi"
	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/config/configtest"
	"github.com/sustinbebustin/understudy/internal/docker"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/service/dynamodb"
	"github.com/sustinbebustin/understudy/internal/service/dynamodb/ddblocal"
)

// echoRuntime is a provided.al2023 runtime that prints each event on one
// line after "EVENT " and answers null.
const echoRuntime = `#!/bin/sh
set -eu
API="http://${AWS_LAMBDA_RUNTIME_API}/2018-06-01/runtime"
while true; do
  HEADERS=$(mktemp)
  EVENT=$(curl -sS -D "$HEADERS" "$API/invocation/next")
  ID=$(grep -i '^Lambda-Runtime-Aws-Request-Id:' "$HEADERS" | tr -d '\r' | cut -d' ' -f2)
  echo "EVENT $EVENT"
  curl -sS -X POST "$API/invocation/$ID/response" -d 'null' >/dev/null
done
`

// withDynamoDB serves DynamoDB over the pinned DynamoDB Local, and DynamoDB
// Streams from its log, beside Lambda.
func withDynamoDB(t *testing.T) stackServices {
	t.Helper()
	client := configtest.EngineClient(t)
	if _, err := client.ImageInspect(t.Context(), ddblocal.Reference()); err != nil {
		t.Skipf("the stream thin slice needs DynamoDB Local's pinned image %s: %v", ddblocal.Reference(), err)
	}
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	own := docker.Ownership{Instance: "esmddb-" + hex.EncodeToString(b), Owner: "esmddb-" + hex.EncodeToString(b)}
	m, err := ddblocal.New(ddblocal.Config{Docker: client, Network: ddblocal.Published{}, Labels: docker.Labels{Ownership: own, Test: true}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := m.Stop(context.WithoutCancel(t.Context())); err != nil {
			t.Errorf("stop DynamoDB Local: %v", err)
		}
	})
	return stackServices{
		apis: []*registry.Service{dynamodbapi.Service(), dynamodbstreamsapi.Service()},
		bind: func(_ *testing.T, deps func(*registry.Service) kernel.ServiceDeps) {
			svc := dynamodb.New(deps(dynamodbapi.Service()), dynamodb.Options{Local: m})
			dynamodb.NewStreams(deps(dynamodbstreamsapi.Service()), svc)
		},
	}
}

// The thin slice end to end: one PutItem to a stream-enabled
// table on the real DynamoDB Local reaches the mapped function, running in
// its container, as a one-record event in the Lambda form
// (dynamodb-streams.md 7.8: eventSourceARN added, eventVersion 1.1,
// ApproximateCreationDateTime whole epoch seconds).
func TestDockerStreamPutItemReachesTheFunction(t *testing.T) {
	s := newDockerStack(t, withDynamoDB(t))
	s.x.StartPollers()
	created := mustDispatch(t, s, dynamodbapi.OpCreateTableRaw, &dynamodbapi.CreateTableInput{
		TableName:            "Orders",
		AttributeDefinitions: []dynamodbapi.AttributeDefinition{{AttributeName: "id", AttributeType: dynamodbapi.ScalarAttributeTypeS}},
		KeySchema:            []dynamodbapi.KeySchemaElement{{AttributeName: "id", KeyType: dynamodbapi.KeyTypeHash}},
		BillingMode:          new(dynamodbapi.BillingModePayPerRequest),
		StreamSpecification:  &dynamodbapi.StreamSpecification{StreamEnabled: true, StreamViewType: new(dynamodbapi.StreamViewTypeNewImage)},
	})
	var table struct {
		TableDescription struct{ LatestStreamArn string }
	}
	if err := json.Unmarshal(created.Response.Body, &table); err != nil || table.TableDescription.LatestStreamArn == "" {
		t.Fatalf("CreateTable answered %s (%v), want a LatestStreamArn", created.Response.Body, err)
	}
	stream := table.TableDescription.LatestStreamArn
	s.create(t, "worker", "provided.al2023", "bootstrap", 10, map[string]string{"bootstrap": echoRuntime})
	mustDispatch(t, s, lambdaapi.OpCreateEventSourceMapping, &lambdaapi.CreateEventSourceMappingInput{
		FunctionName: "worker", EventSourceArn: new(stream), StartingPosition: new(lambdaapi.EventSourcePositionTrimHorizon),
	})
	put := mustDispatch(t, s, dynamodbapi.OpPutItemRaw, &dynamodbapi.PutItemInput{
		TableName: "Orders",
		Item:      map[string]dynamodbapi.AttributeValue{"id": dynamodbapi.AttributeValueMemberS{Value: "a"}, "v": dynamodbapi.AttributeValueMemberN{Value: "1"}},
	})
	if put.Response.Status != 200 {
		t.Fatalf("PutItem answered %d %s", put.Response.Status, put.Response.Body)
	}

	// The append wakes the reader from a goroutine the work tracker does not
	// count, so settling alone can end before the reader's step starts:
	// settle until the function has logged an event, for at most two
	// minutes of wall time.
	events := func() []string {
		var out []string
		for _, m := range s.logs.messages() {
			if rest, ok := strings.CutPrefix(m, "EVENT "); ok {
				out = append(out, strings.TrimSpace(rest))
			}
		}
		return out
	}
	deadline := time.After(2 * time.Minute)
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for len(events()) == 0 {
		if _, err := s.k.Settle(t.Context(), time.Minute); err != nil {
			t.Fatal(err)
		}
		select {
		case <-deadline:
			t.Fatalf("no event reached the function within two minutes; logs %q", s.logs.messages())
		case <-tick.C:
		}
	}
	if _, err := s.k.Settle(t.Context(), time.Minute); err != nil {
		t.Fatal(err)
	}
	delivered := events()
	if len(delivered) != 1 {
		t.Fatalf("the function was invoked with %d events, want 1: %q", len(delivered), delivered)
	}
	event := delivered[0]
	var got struct {
		Records []struct {
			EventName      string `json:"eventName"`
			EventVersion   string `json:"eventVersion"`
			EventSource    string `json:"eventSource"`
			AWSRegion      string `json:"awsRegion"`
			EventSourceARN string `json:"eventSourceARN"`
			EventID        string `json:"eventID"`
			Dynamodb       struct {
				ApproximateCreationDateTime json.Number
				Keys                        map[string]map[string]string
				NewImage                    map[string]map[string]string
				SequenceNumber              string
				StreamViewType              string
			} `json:"dynamodb"`
			UserIdentity json.RawMessage `json:"userIdentity"`
		}
	}
	if err := json.Unmarshal([]byte(event), &got); err != nil {
		t.Fatalf("event %s: %v", event, err)
	}
	if len(got.Records) != 1 {
		t.Fatalf("event has %d records, want 1: %s", len(got.Records), event)
	}
	r := got.Records[0]
	if r.EventName != "INSERT" || r.EventVersion != "1.1" || r.EventSource != "aws:dynamodb" || r.AWSRegion != s.scope.Region.String() ||
		r.EventSourceARN != stream || len(r.EventID) != 32 || r.UserIdentity != nil {
		t.Errorf("record = %+v", r)
	}
	if r.Dynamodb.Keys["id"]["S"] != "a" || r.Dynamodb.NewImage["v"]["N"] != "1" || r.Dynamodb.StreamViewType != "NEW_IMAGE" || r.Dynamodb.SequenceNumber == "" {
		t.Errorf("dynamodb = %+v", r.Dynamodb)
	}
	// ESMS-17 09-ddb.json: the handler reads a Python float,
	// 1793725107.0; the `.0` spelling is inferred.
	n := r.Dynamodb.ApproximateCreationDateTime.String()
	if whole, ok := strings.CutSuffix(n, ".0"); !ok || len(whole) != 10 || strings.ContainsAny(whole, ".eE") {
		t.Errorf("ApproximateCreationDateTime = %s, want 10 integral digits then .0 (ESMS-17)", n)
	}
}
