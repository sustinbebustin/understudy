package server_test

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/sustinbebustin/understudy/admin"
	"github.com/sustinbebustin/understudy/internal/server"
)

// ssmEventQueue creates queue name and a rule of the default bus whose
// pattern sends matching events to it, and returns the queue's URL.
func ssmEventQueue(t *testing.T, endpoint, name, pattern string) string {
	t.Helper()
	mustSQS(t, endpoint, "CreateQueue", `{"QueueName":"`+name+`"}`)
	events := func(op string, body any) {
		t.Helper()
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		if status, out := rootJSONCall(t, endpoint, "events", "AWSEvents."+op, string(data)); status != http.StatusOK {
			t.Fatalf("events %s = %d %s", op, status, out)
		}
	}
	events("PutRule", map[string]string{"Name": name, "EventPattern": pattern})
	events("PutTargets", map[string]any{"Rule": name, "Targets": []map[string]string{{"Id": "q", "Arn": "arn:aws:sqs:us-east-1:000000000000:" + name}}})
	return endpoint + "/000000000000/" + name
}

// receivedEvents settles the server and returns the events queueURL
// holds, each with its id and time replaced by placeholders after checking
// they are present, sorted by their JSON form.
func receivedEvents(t *testing.T, c *admin.Client, endpoint, queueURL string) []map[string]any {
	t.Helper()
	if _, err := c.Settle(t.Context(), 10*time.Second); err != nil {
		t.Fatal(err)
	}
	var out struct{ Messages []struct{ Body string } }
	if err := json.Unmarshal([]byte(mustSQS(t, endpoint, "ReceiveMessage", `{"QueueUrl":"`+queueURL+`","MaxNumberOfMessages":10}`)), &out); err != nil {
		t.Fatal(err)
	}
	got := make([]map[string]any, 0, len(out.Messages))
	for _, m := range out.Messages {
		var ev map[string]any
		if err := json.Unmarshal([]byte(m.Body), &ev); err != nil {
			t.Fatalf("message body %s is not an event: %v", m.Body, err)
		}
		if id, ok := ev["id"].(string); !ok || id == "" {
			t.Errorf("event %s has no id", m.Body)
		}
		if at, ok := ev["time"].(string); !ok {
			t.Errorf("event %s has no time", m.Body)
		} else if _, err := time.Parse(time.RFC3339, at); err != nil {
			t.Errorf("event time %q is not RFC 3339: %v", at, err)
		}
		ev["id"], ev["time"] = "<id>", "<time>"
		got = append(got, ev)
	}
	return sortedEvents(t, got)
}

// sortedEvents sorts events by their JSON form, so a delivery order the
// queue does not promise does not decide a comparison.
func sortedEvents(t *testing.T, events []map[string]any) []map[string]any {
	t.Helper()
	keys := make([]string, len(events))
	order := make([]int, len(events))
	for i, ev := range events {
		data, err := json.Marshal(ev)
		if err != nil {
			t.Fatal(err)
		}
		keys[i], order[i] = string(data), i
	}
	slices.SortFunc(order, func(a, b int) int { return strings.Compare(keys[a], keys[b]) })
	out := make([]map[string]any, len(order))
	for i, j := range order {
		out[i] = events[j]
	}
	return out
}

// parameterChange is the Parameter Store Change event SSM-31 recorded for
// parameter /ev/p in the default account and Region, with detail.
func parameterChange(detail map[string]any) map[string]any {
	return map[string]any{
		"version": "0", "id": "<id>", "time": "<time>",
		"source": "aws.ssm", "detail-type": "Parameter Store Change",
		"account": "000000000000", "region": "us-east-1",
		"resources": []any{"arn:aws:ssm:us-east-1:000000000000:parameter/ev/p"},
		"detail":    detail,
	}
}

// Create, overwrite, first label, label move and delete each put one
// Parameter Store Change event on the default bus, with the envelope and
// detail SSM-31 recorded (ssm-parameters.md 9.2; SSM-31): the overwrite
// carries the kept description, and the label events carry label,
// fromVersion and toVersion as strings.
func TestSSMParameterChangesPutEventsOnTheDefaultBus(t *testing.T) {
	c, endpoint := adminServer(t, server.Config{})
	queueURL := ssmEventQueue(t, endpoint, "ssm-ev", `{"source":["aws.ssm"],"detail-type":["Parameter Store Change"]}`)
	mustSSM(t, endpoint, "PutParameter", `{"Name":"/ev/p","Type":"String","Value":"v","Description":"d"}`)
	mustSSM(t, endpoint, "PutParameter", `{"Name":"/ev/p","Type":"String","Value":"w","Overwrite":true}`)
	mustSSM(t, endpoint, "LabelParameterVersion", `{"Name":"/ev/p","Labels":["l1"]}`)
	mustSSM(t, endpoint, "LabelParameterVersion", `{"Name":"/ev/p","ParameterVersion":1,"Labels":["l1"]}`)
	mustSSM(t, endpoint, "DeleteParameter", `{"Name":"/ev/p"}`)

	common := func(op string) map[string]any {
		return map[string]any{"name": "/ev/p", "description": "d", "type": "String", "operation": op}
	}
	label := func(from, to string) map[string]any {
		d := common("LabelParameterVersion")
		d["label"], d["fromVersion"], d["toVersion"] = "l1", from, to
		return d
	}
	want := sortedEvents(t, []map[string]any{
		parameterChange(common("Create")),
		parameterChange(common("Update")),
		parameterChange(label("", "2")),
		parameterChange(label("2", "1")),
		parameterChange(common("Delete")),
	})
	if diff := cmp.Diff(want, receivedEvents(t, c, endpoint, queueURL)); diff != "" {
		t.Errorf("events (-want +got):\n%s", diff)
	}
}

// A rule on detail.operation, one of the documented pattern fields
// (ssm-parameters.md 9.2), receives only the events of that operation.
func TestSSMRuleOnDeleteReceivesOnlyTheDelete(t *testing.T) {
	c, endpoint := adminServer(t, server.Config{})
	queueURL := ssmEventQueue(t, endpoint, "ssm-del", `{"source":["aws.ssm"],"detail":{"operation":["Delete"]}}`)
	mustSSM(t, endpoint, "PutParameter", `{"Name":"/ev/p","Type":"String","Value":"v","Description":"d"}`)
	mustSSM(t, endpoint, "PutParameter", `{"Name":"/ev/p","Type":"String","Value":"w","Overwrite":true}`)
	mustSSM(t, endpoint, "LabelParameterVersion", `{"Name":"/ev/p","Labels":["l1"]}`)
	mustSSM(t, endpoint, "DeleteParameter", `{"Name":"/ev/p"}`)
	want := []map[string]any{parameterChange(map[string]any{"name": "/ev/p", "description": "d", "type": "String", "operation": "Delete"})}
	if diff := cmp.Diff(want, receivedEvents(t, c, endpoint, queueURL)); diff != "" {
		t.Errorf("events (-want +got):\n%s", diff)
	}
}

// A refused call changes nothing, so it sends no event: here PutParameter
// of an existing name without Overwrite (ssm-parameters.md 12.4).
func TestSSMRefusedPutParameterSendsNoEvent(t *testing.T) {
	c, endpoint := adminServer(t, server.Config{})
	queueURL := ssmEventQueue(t, endpoint, "ssm-refused", `{"source":["aws.ssm"]}`)
	mustSSM(t, endpoint, "PutParameter", `{"Name":"/ev/p","Type":"String","Value":"v","Description":"d"}`)
	if status, out := rootJSONCall(t, endpoint, "ssm", "AmazonSSM.PutParameter", `{"Name":"/ev/p","Type":"String","Value":"w"}`); status != http.StatusBadRequest || !strings.Contains(out, "ParameterAlreadyExists") {
		t.Fatalf("PutParameter without Overwrite = %d %s, want 400 ParameterAlreadyExists", status, out)
	}
	want := []map[string]any{parameterChange(map[string]any{"name": "/ev/p", "description": "d", "type": "String", "operation": "Create"})}
	if diff := cmp.Diff(want, receivedEvents(t, c, endpoint, queueURL)); diff != "" {
		t.Errorf("events (-want +got):\n%s", diff)
	}
}

// The cases SSM-31 did not record, as understudy infers them
// (ssm-parameters.md 9.2): a parameter with no description sends "",
// DeleteParameters sends one Delete event per parameter it deletes, and
// UnlabelParameterVersion sends nothing.
func TestSSMInferredParameterChangeEvents(t *testing.T) {
	c, endpoint := adminServer(t, server.Config{})
	queueURL := ssmEventQueue(t, endpoint, "ssm-inferred", `{"source":["aws.ssm"],"detail":{"operation":["Delete","LabelParameterVersion"]}}`)
	mustSSM(t, endpoint, "PutParameter", `{"Name":"/ev/p","Type":"String","Value":"v"}`)
	mustSSM(t, endpoint, "PutParameter", `{"Name":"/ev/q","Type":"StringList","Value":"a,b"}`)
	mustSSM(t, endpoint, "LabelParameterVersion", `{"Name":"/ev/p","Labels":["l1"]}`)
	if _, err := c.Settle(t.Context(), 10*time.Second); err != nil {
		t.Fatal(err)
	}
	mustSQS(t, endpoint, "PurgeQueue", `{"QueueUrl":"`+queueURL+`"}`)
	mustSSM(t, endpoint, "UnlabelParameterVersion", `{"Name":"/ev/p","ParameterVersion":1,"Labels":["l1"]}`)
	mustSSM(t, endpoint, "DeleteParameters", `{"Names":["/ev/p","/ev/q","/ev/missing"]}`)
	q := parameterChange(map[string]any{"name": "/ev/q", "description": "", "type": "StringList", "operation": "Delete"})
	q["resources"] = []any{"arn:aws:ssm:us-east-1:000000000000:parameter/ev/q"}
	want := sortedEvents(t, []map[string]any{
		parameterChange(map[string]any{"name": "/ev/p", "description": "", "type": "String", "operation": "Delete"}),
		q,
	})
	if diff := cmp.Diff(want, receivedEvents(t, c, endpoint, queueURL)); diff != "" {
		t.Errorf("events (-want +got):\n%s", diff)
	}
}
