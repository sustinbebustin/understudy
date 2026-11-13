package server_test

import (
	"encoding/json"
	"maps"
	"net/http"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/admin"
	"github.com/sustinbebustin/understudy/internal/parity"
	"github.com/sustinbebustin/understudy/internal/server"
)

// An EventBridge dead letter of a delivery its target refused, compared with
// the dead letters IAM-ENF-22 received (iam-enforcement-surfaces.md 8.4,
// aws-recorded): exactly ERROR_CODE NO_PERMISSIONS, ERROR_MESSAGE, RULE_ARN
// and TARGET_ARN, the message being the target's denial and EventBridge's
// SDK client suffix. The captures' account, Region and names are mapped onto
// the test's, and request ids are masked on both sides.

// recordedNames maps IAM-ENF-22's account, Region and names onto the test's.
var recordedNames = strings.NewReplacer(
	"123456789012", "000000000000", "us-east-2", "us-east-1",
	"us1103-enf-gone", "gone", "us1103-enf-fn", "fn", "us1103-enf-eq", "eq", "us1103-enf-r", "r",
)

// suffixRequestID is the request id an SDK client suffix carries.
var suffixRequestID = regexp.MustCompile(`Request ID: [0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12};`)

// letterAttributes decodes a ReceiveMessage answer's messages into their
// String attributes, request ids masked.
func letterAttributes(t *testing.T, received string) []map[string]string {
	t.Helper()
	var out struct {
		Messages []struct {
			MessageAttributes map[string]struct{ DataType, StringValue string }
		}
	}
	if err := json.Unmarshal([]byte(received), &out); err != nil {
		t.Fatalf("ReceiveMessage = %s: %v", received, err)
	}
	letters := make([]map[string]string, 0, len(out.Messages))
	for _, m := range out.Messages {
		attrs := map[string]string{}
		for name, v := range m.MessageAttributes {
			attrs[name] = v.DataType + " " + suffixRequestID.ReplaceAllString(v.StringValue, "Request ID: <request-id>;")
		}
		letters = append(letters, attrs)
	}
	return letters
}

// recordedDeadLetter is the one dead letter the capture testdata/parity/path
// received, its names mapped by recordedNames.
func recordedDeadLetter(t *testing.T, path string) map[string]string {
	t.Helper()
	f, err := parity.ReadFixture(os.DirFS("../../testdata/parity"), path)
	if err != nil || len(f.Exchanges) != 1 || f.Exchanges[0].Response == nil {
		t.Fatalf("ReadFixture(%s) = %+v, %v; want one recorded exchange", path, f, err)
	}
	body, ok := f.Exchanges[0].Response.Body.(parity.TextBody)
	if !ok {
		t.Fatalf("%s response body = %T, want text", path, f.Exchanges[0].Response.Body)
	}
	letters := letterAttributes(t, recordedNames.Replace(body.Text))
	if len(letters) != 1 {
		t.Fatalf("%s received %d dead letters, want 1", path, len(letters))
	}
	return letters[0]
}

// refusedDeliveryServer starts a server under mode with the queue dlq,
// whose policy admits events.amazonaws.com, and the rule r on the default
// bus matching source app.
func refusedDeliveryServer(t *testing.T, mode server.IAMEnforce) (c *admin.Client, endpoint string) {
	t.Helper()
	c, endpoint, _ = enforced(t, mode)
	mustSQS(t, endpoint, "CreateQueue", `{"QueueName":"dlq"}`)
	setQueuePolicy(t, endpoint, "dlq", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"events.amazonaws.com"},`+
		`"Action":"sqs:SendMessage","Resource":"arn:aws:sqs:us-east-1:000000000000:dlq"}]}`)
	if w := jsonCall(t, dummy, endpoint, "events", "1.1", "AWSEvents.PutRule", `{"Name":"r","EventPattern":"{\"source\":[\"app\"]}"}`); w.status != http.StatusOK {
		t.Fatalf("PutRule = %d %s", w.status, w.body)
	}
	return c, endpoint
}

// deadLetterOfEvent targets rule r at target with the dead-letter queue
// dlq, puts one event and returns the dead letters dlq received.
func deadLetterOfEvent(t *testing.T, c *admin.Client, endpoint, target string) []map[string]string {
	t.Helper()
	targets := `{"Rule":"r","Targets":[{"Id":"t","Arn":"` + target + `","DeadLetterConfig":{"Arn":"arn:aws:sqs:us-east-1:000000000000:dlq"}}]}`
	if w := jsonCall(t, dummy, endpoint, "events", "1.1", "AWSEvents.PutTargets", targets); w.status != http.StatusOK {
		t.Fatalf("PutTargets = %d %s", w.status, w.body)
	}
	if w := jsonCall(t, dummy, endpoint, "events", "1.1", "AWSEvents.PutEvents", `{"Entries":[{"Source":"app","DetailType":"t","Detail":"{}"}]}`); w.status != http.StatusOK {
		t.Fatalf("PutEvents = %d %s", w.status, w.body)
	}
	if _, err := c.Settle(t.Context(), 10*time.Second); err != nil {
		t.Fatal(err)
	}
	return letterAttributes(t, mustSQS(t, endpoint, "ReceiveMessage",
		`{"QueueUrl":"`+endpoint+`/000000000000/dlq","MaxNumberOfMessages":10,"MessageAttributeNames":["All"]}`))
}

// A function or queue whose policy does not admit EventBridge refuses the
// delivery under enforce, and its dead letter is the one IAM-ENF-22
// recorded: 22-dlq.json for the function, 22-dlq-sa.json for the queue.
func TestEnforceDeadLettersARefusedEventBridgeDeliveryAsRecorded(t *testing.T) {
	for _, c := range []struct {
		name, target, capture string
		create                func(t *testing.T, endpoint string)
	}{
		{"function", "arn:aws:lambda:us-east-1:000000000000:function:fn", "sqs/IAM-ENF/22-dlq.json", func(t *testing.T, endpoint string) {
			t.Helper()
			roleTrustingLambda(t, endpoint, "fn")
			if w := createFunction(t, dummy, endpoint, "fn"); w.status != http.StatusCreated {
				t.Fatalf("CreateFunction = %d %s", w.status, w.body)
			}
		}},
		{"queue", "arn:aws:sqs:us-east-1:000000000000:eq", "sqs/IAM-ENF/22-dlq-sa.json", func(t *testing.T, endpoint string) {
			t.Helper()
			mustSQS(t, endpoint, "CreateQueue", `{"QueueName":"eq"}`)
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			client, endpoint := refusedDeliveryServer(t, server.IAMEnforceEnforce)
			c.create(t, endpoint)
			want := recordedDeadLetter(t, c.capture)
			if got := deadLetterOfEvent(t, client, endpoint, c.target); len(got) != 1 || !maps.Equal(got[0], want) {
				t.Errorf("dead letters = %v, want one with exactly %v (%s)", got, want, c.capture)
			}
		})
	}
}

// A function deleted after PutTargets dead-letters as 22-dlq-gone.json
// shows, NO_PERMISSIONS with Lambda's denial naming it, under every
// iam.enforce mode: no policy can admit EventBridge to a function that does
// not exist, so the mode decides nothing.
func TestEventBridgeDeadLettersADeletedFunctionAsRecordedInEveryMode(t *testing.T) {
	want := recordedDeadLetter(t, "sqs/IAM-ENF/22-dlq-gone.json")
	for name, mode := range map[string]server.IAMEnforce{"off": server.IAMEnforceOff, "advise": server.IAMEnforceAdvise, "enforce": server.IAMEnforceEnforce} {
		t.Run(name, func(t *testing.T) {
			client, endpoint := refusedDeliveryServer(t, mode)
			roleTrustingLambda(t, endpoint, "fn")
			if w := createFunction(t, dummy, endpoint, "gone"); w.status != http.StatusCreated {
				t.Fatalf("CreateFunction = %d %s", w.status, w.body)
			}
			if w := signedCall(t, dummy, "lambda", newRequest(t, http.MethodDelete, endpoint+"/2015-03-31/functions/gone", ""), ""); w.status != http.StatusNoContent {
				t.Fatalf("DeleteFunction = %d %s", w.status, w.body)
			}
			got := deadLetterOfEvent(t, client, endpoint, "arn:aws:lambda:us-east-1:000000000000:function:gone")
			if len(got) != 1 || !maps.Equal(got[0], want) {
				t.Errorf("dead letters = %v, want one with exactly %v (22-dlq-gone.json)", got, want)
			}
		})
	}
}
