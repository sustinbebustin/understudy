package server_test

import (
	"encoding/base64"
	"encoding/json"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/admin"
	"github.com/sustinbebustin/understudy/internal/server"
)

// Lambda under iam.enforce: PutFunctionEventInvokeConfig and
// UpdateFunctionEventInvokeConfig ask whether the function's execution
// role may write to an SNS or SQS destination (iam-enforcement-surfaces.md
// 8.5, IAM-ENF-27).

const (
	destTopic = "arn:aws:sns:us-east-1:000000000000:t"
	destQueue = "arn:aws:sqs:us-east-1:000000000000:dlq"
	destBus   = "arn:aws:events:us-east-1:000000000000:event-bus/default"
)

// destinationServer runs a server under mode holding the function fn, whose
// execution role fn trusts Lambda and has no policy, the topic t and the
// queue dlq.
func destinationServer(t *testing.T, mode server.IAMEnforce) (c *admin.Client, endpoint string) {
	t.Helper()
	c, endpoint, _ = enforced(t, mode)
	iamAsRoot(t, endpoint, url.Values{"Action": {"CreateRole"}, "RoleName": {"fn"}, "AssumeRolePolicyDocument": {trustOf("lambda.amazonaws.com")}})
	pkg := base64.StdEncoding.EncodeToString(zipped(t, "unused"))
	body := `{"FunctionName":"fn","Runtime":"provided.al2023","Handler":"bootstrap","Role":"arn:aws:iam::000000000000:role/fn","Code":{"ZipFile":"` + pkg + `"}}`
	if w := lambdaCall(t, dummy, endpoint, http.MethodPost, "/2015-03-31/functions", body); w.status != http.StatusCreated {
		t.Fatalf("CreateFunction = %d %s", w.status, w.body)
	}
	mustSNS(t, endpoint, url.Values{"Action": {"CreateTopic"}, "Name": {"t"}})
	mustSQS(t, endpoint, "CreateQueue", `{"QueueName":"dlq"}`)
	return c, endpoint
}

// eventInvoke sends PutFunctionEventInvokeConfig (PUT) or
// UpdateFunctionEventInvokeConfig (POST) for fn as root.
func eventInvoke(t *testing.T, endpoint, method, body string) wire {
	t.Helper()
	return lambdaCall(t, dummy, endpoint, method, "/2019-09-25/functions/fn/event-invoke-config", body)
}

func onSuccess(arn string) string {
	return `{"DestinationConfig":{"OnSuccess":{"Destination":"` + arn + `"}}}`
}

func onFailure(arn string) string {
	return `{"DestinationConfig":{"OnFailure":{"Destination":"` + arn + `"}}}`
}

// wantDestinationRefused checks w is the recorded refusal: 400
// InvalidParameterValueException with exactly the body
// {"Type":"User","message":<text>}.
func wantDestinationRefused(t *testing.T, what string, w wire, text string) {
	t.Helper()
	var body map[string]string
	if err := json.Unmarshal([]byte(w.body), &body); err != nil {
		t.Errorf("%s = %d %s, not a JSON error: %v", what, w.status, w.body, err)
		return
	}
	want := map[string]string{"Type": "User", "message": text}
	if w.status != http.StatusBadRequest || w.header.Get("X-Amzn-Errortype") != "InvalidParameterValueException" || !maps.Equal(body, want) {
		t.Errorf("%s = %d %s %s, want 400 InvalidParameterValueException %v", what, w.status, w.header.Get("X-Amzn-Errortype"), w.body, want)
	}
}

// Under enforce a role that may not publish to the topic or send to the
// queue refuses the configuration with the recorded answers
// (testdata/parity/lambda/IAM-ENF/27-sns.json, 27-sqs.json, account and
// Region swapped for understudy's); Update refuses the same way
// (inferred: only Put was recorded). A bus is not checked (unrecorded).
// Once the role may, both are stored.
func TestEventInvokeConfigChecksTheExecutionRoleUnderEnforce(t *testing.T) {
	_, endpoint := destinationServer(t, server.IAMEnforceEnforce)
	const (
		publish = "The function execution role does not have permissions to call Publish on " + destTopic
		send    = "The function execution role does not have permissions to call SendMessage on " + destQueue
	)
	wantDestinationRefused(t, "Put on success to the topic", eventInvoke(t, endpoint, http.MethodPut, onSuccess(destTopic)), publish)
	wantDestinationRefused(t, "Put on failure to the queue", eventInvoke(t, endpoint, http.MethodPut, onFailure(destQueue)), send)
	if w := eventInvoke(t, endpoint, http.MethodPut, onSuccess(destBus)); w.status != http.StatusOK {
		t.Fatalf("Put on success to a bus = %d %s, want 200: a bus is not checked", w.status, w.body)
	}
	wantDestinationRefused(t, "Update on failure to the queue", eventInvoke(t, endpoint, http.MethodPost, onFailure(destQueue)), send)
	if w := lambdaCall(t, dummy, endpoint, http.MethodGet, "/2019-09-25/functions/fn/event-invoke-config", ""); strings.Contains(w.body, destQueue) {
		t.Errorf("GetFunctionEventInvokeConfig = %s, want the refused queue not stored", w.body)
	}

	iamAsRoot(t, endpoint, url.Values{"Action": {"PutRolePolicy"}, "RoleName": {"fn"}, "PolicyName": {"p"}, "PolicyDocument": {
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["sns:Publish","sqs:SendMessage"],"Resource":["` + destTopic + `","` + destQueue + `"]}]}`,
	}})
	if w := eventInvoke(t, endpoint, http.MethodPut, `{"DestinationConfig":{"OnSuccess":{"Destination":"`+destTopic+`"},"OnFailure":{"Destination":"`+destQueue+`"}}}`); w.status != http.StatusOK {
		t.Errorf("Put once the role may = %d %s, want 200", w.status, w.body)
	}
}

// Under advise the configuration is stored and the role's denial
// journaled; under off nothing is asked.
func TestEventInvokeConfigRoleCheckUnderAdviseAndOff(t *testing.T) {
	t.Run("advise", func(t *testing.T) {
		c, endpoint := destinationServer(t, server.IAMEnforceAdvise)
		if w := eventInvoke(t, endpoint, http.MethodPut, onSuccess(destTopic)); w.status != http.StatusOK {
			t.Fatalf("Put under advise = %d %s, want 200", w.status, w.body)
		}
		advised := enforcementAdvisories(t, c, "access-would-deny")
		if !slices.ContainsFunc(advised, func(s string) bool {
			return strings.Contains(s, "role/fn") && strings.Contains(s, "sns:Publish") && strings.Contains(s, destTopic)
		}) {
			t.Errorf("access-would-deny advisories = %q, want one for the role publishing to %s", advised, destTopic)
		}
	})
	t.Run("off", func(t *testing.T) {
		c, endpoint := destinationServer(t, server.IAMEnforceOff)
		if w := eventInvoke(t, endpoint, http.MethodPut, onFailure(destQueue)); w.status != http.StatusOK {
			t.Fatalf("Put under off = %d %s, want 200", w.status, w.body)
		}
		if got := enforcementAdvisories(t, c, "access-would-deny"); len(got) != 0 {
			t.Errorf("off journaled access-would-deny advisories %q, want none", got)
		}
	})
}
