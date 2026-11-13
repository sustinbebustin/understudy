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
// 8.5, IAM-ENF-27), and CreateFunction and UpdateFunctionConfiguration
// whether it may write to the dead-letter target
// (lambda-destinations-layers.md 5.1, LDL-08 14).

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

// deadLetter sends UpdateFunctionConfiguration for fn setting its
// dead-letter target to arn, as root.
func deadLetter(t *testing.T, endpoint, arn string) wire {
	t.Helper()
	return lambdaCall(t, dummy, endpoint, http.MethodPut, "/2015-03-31/functions/fn/configuration", `{"DeadLetterConfig":{"TargetArn":"`+arn+`"}}`)
}

// Under enforce a role that may not send to the dead-letter queue refuses
// UpdateFunctionConfiguration with the recorded answer, which names the
// service, not the ARN (testdata/parity/lambda/LDL-08/14-no-permission-dlq.json),
// and CreateFunction the same way (inferred: only the update was
// recorded); the topic's sentence names Publish on SNS (inferred from the
// queue's). Nothing is stored, and once the role may, the target is taken.
func TestDeadLetterTargetChecksTheExecutionRoleUnderEnforce(t *testing.T) {
	_, endpoint := destinationServer(t, server.IAMEnforceEnforce)
	const (
		send    = "The provided execution role does not have permissions to call SendMessage on SQS"
		publish = "The provided execution role does not have permissions to call Publish on SNS"
	)
	wantDestinationRefused(t, "Update to the queue", deadLetter(t, endpoint, destQueue), send)
	wantDestinationRefused(t, "Update to the topic", deadLetter(t, endpoint, destTopic), publish)
	pkg := base64.StdEncoding.EncodeToString(zipped(t, "unused"))
	create := `{"FunctionName":"fn2","Runtime":"provided.al2023","Handler":"bootstrap","Role":"arn:aws:iam::000000000000:role/fn","Code":{"ZipFile":"` + pkg + `"},` +
		`"DeadLetterConfig":{"TargetArn":"` + destQueue + `"}}`
	wantDestinationRefused(t, "Create with the queue", lambdaCall(t, dummy, endpoint, http.MethodPost, "/2015-03-31/functions", create), send)
	if w := lambdaCall(t, dummy, endpoint, http.MethodGet, "/2015-03-31/functions/fn/configuration", ""); strings.Contains(w.body, "DeadLetterConfig") {
		t.Errorf("GetFunctionConfiguration = %s, want no dead-letter target stored", w.body)
	}

	iamAsRoot(t, endpoint, url.Values{"Action": {"PutRolePolicy"}, "RoleName": {"fn"}, "PolicyName": {"p"}, "PolicyDocument": {
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["sns:Publish","sqs:SendMessage"],"Resource":["` + destTopic + `","` + destQueue + `"]}]}`,
	}})
	if w := deadLetter(t, endpoint, destQueue); w.status != http.StatusOK || !strings.Contains(w.body, destQueue) {
		t.Errorf("Update to the queue once the role may = %d %s, want 200 with the target", w.status, w.body)
	}
}

// Under advise the dead-letter target is stored and the role's denial
// journaled; under off nothing is asked.
func TestDeadLetterTargetRoleCheckUnderAdviseAndOff(t *testing.T) {
	t.Run("advise", func(t *testing.T) {
		c, endpoint := destinationServer(t, server.IAMEnforceAdvise)
		if w := deadLetter(t, endpoint, destQueue); w.status != http.StatusOK {
			t.Fatalf("Update under advise = %d %s, want 200", w.status, w.body)
		}
		advised := enforcementAdvisories(t, c, "access-would-deny")
		if !slices.ContainsFunc(advised, func(s string) bool {
			return strings.Contains(s, "role/fn") && strings.Contains(s, "sqs:SendMessage") && strings.Contains(s, destQueue)
		}) {
			t.Errorf("access-would-deny advisories = %q, want one for the role sending to %s", advised, destQueue)
		}
	})
	t.Run("off", func(t *testing.T) {
		c, endpoint := destinationServer(t, server.IAMEnforceOff)
		if w := deadLetter(t, endpoint, destTopic); w.status != http.StatusOK {
			t.Fatalf("Update under off = %d %s, want 200", w.status, w.body)
		}
		if got := enforcementAdvisories(t, c, "access-would-deny"); len(got) != 0 {
			t.Errorf("off journaled access-would-deny advisories %q, want none", got)
		}
	})
}

// The least-privilege loop for a function's destinations: under advise
// the execution role's write to each SNS or SQS destination and
// dead-letter target it is asked about is access-recorded for the role,
// so the policy GET /iam/policy writes for the role grants it, and that
// policy lets the same configuration through under enforce
// (iam-enforcement-surfaces.md 8.5, 12.6).
func TestDestinationRoleChecksAreGrantedByTheRolesGeneratedPolicy(t *testing.T) {
	const role = "arn:aws:iam::000000000000:role/fn"
	for _, tc := range []struct {
		name      string
		configure func(t *testing.T, endpoint string) wire
	}{
		{"event-invoke destinations", func(t *testing.T, endpoint string) wire {
			t.Helper()
			return eventInvoke(t, endpoint, http.MethodPut, `{"DestinationConfig":{"OnSuccess":{"Destination":"`+destQueue+`"},"OnFailure":{"Destination":"`+destTopic+`"}}}`)
		}},
		{"dead-letter target", func(t *testing.T, endpoint string) wire {
			t.Helper()
			return deadLetter(t, endpoint, destQueue)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, endpoint := destinationServer(t, server.IAMEnforceAdvise)
			if w := tc.configure(t, endpoint); w.status != http.StatusOK {
				t.Fatalf("configure under advise = %d %s, want 200", w.status, w.body)
			}
			got := granted(t, c, role)
			if !slices.Equal(got[destQueue], []string{"sqs:SendMessage"}) {
				t.Errorf("/iam/policy for %s grants %v, want sqs:SendMessage on %s", role, got, destQueue)
			}
			p, err := c.IAMPolicy(t.Context(), role)
			if err != nil {
				t.Fatal(err)
			}

			_, endpoint = destinationServer(t, server.IAMEnforceEnforce)
			iamAsRoot(t, endpoint, url.Values{"Action": {"PutRolePolicy"}, "RoleName": {"fn"}, "PolicyName": {"generated"}, "PolicyDocument": {string(p.Document)}})
			if w := tc.configure(t, endpoint); w.status != http.StatusOK {
				t.Errorf("configure under enforce with the generated policy = %d %s, want 200", w.status, w.body)
			}
		})
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
