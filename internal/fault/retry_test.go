package fault_test

import (
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/fault"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/ref"
)

func TestRetryableWarningNamesCodesTheSDKsRetry(t *testing.T) {
	onAll := func(ops []ref.OperationID, a fault.Action, trigger fault.Trigger) fault.Rule {
		return fault.Rule{
			ID: "r", Match: fault.Match{Operations: ops}, Phase: fault.Request,
			Action: a, Trigger: trigger,
		}
	}
	on := func(op ref.OperationID, a fault.Action, trigger fault.Trigger) fault.Rule {
		return onAll([]ref.OperationID{op}, a, trigger)
	}
	errorOn := func(op ref.OperationID, shape smithy.ShapeID) fault.Rule {
		return on(op, fault.Error{Shape: shape}, fault.NextN{N: 3})
	}
	errorOnAll := func(shape smithy.ShapeID, ops ...ref.OperationID) fault.Rule {
		return onAll(ops, fault.Error{Shape: shape}, fault.NextN{N: 3})
	}
	throttleFrom := func(origin fault.OriginMatch) fault.Rule {
		r := throttleSend(fault.NextN{N: 3})
		r.Match.Origin = origin
		return r
	}
	cases := []struct {
		name string
		rule fault.Rule
		want string // a fragment of the warning; empty means no warning
	}{
		{"sqs throttle", throttleSend(fault.NextN{N: 3}), "RequestThrottled, which AWS SDKs retry (a throttling error)"},
		{"sqs throttle from any origin", throttleFrom(fault.Any), "a throttling error"},
		{"sqs throttle from external callers", throttleFrom(fault.ExternalOnly), "a throttling error"},
		{"sqs throttle from internal callers", throttleFrom(fault.InternalOnly), ""},
		{"lambda throttle", errorOn(invoke, "TooManyRequestsException"), "a throttling error"},
		{"generic 500", errorOn(sendMessage, "InternalFailure"), "HTTP 500"},
		{"s3 slow down", errorOn(completeUpload, "SlowDown"), "a throttling error"},
		{"s3 access denied", errorOn(completeUpload, "AccessDenied"), ""},
		{"s3 request timeout", errorOn(completeUpload, "RequestTimeout"), "RequestTimeout, which AWS SDKs retry (a timeout error)"},
		{"modeled retryable 400", errorOn(putItem, "ReplicatedWriteConflictException"), "ReplicatedWriteConflictException, which AWS SDKs retry (the error is retryable)"},
		{"kms invalid state", errorOn(decrypt, "KMSInvalidStateException"), ""},
		// kms.Decrypt sorts before lambda.Invoke and sends the shape as a 409,
		// so the warning comes from Lambda's 502 whichever order the rule
		// lists them in.
		{"retryable only on the later operation", errorOnAll("KMSInvalidStateException", decrypt, invoke), "KMSInvalidStateException, which AWS SDKs retry (HTTP 502)"},
		{"retryable only on the earlier listed operation", errorOnAll("KMSInvalidStateException", invoke, decrypt), "KMSInvalidStateException, which AWS SDKs retry (HTTP 502)"},
		// lambda sorts before sqs, so its throttle is the one named.
		{"throttle of two services", onAll([]ref.OperationID{sendMessage, invoke}, fault.Throttle{}, fault.Always{}), "TooManyRequestsException, which AWS SDKs retry"},
		{
			"s3 access denied in a 200 response",
			on(completeUpload, fault.StatusOKError{Shape: "AccessDenied"}, fault.Always{}),
			"AccessDenied, which AWS SDKs retry (they read a 200 response with an <Error> body as HTTP 500)",
		},
		{"queue does not exist", errorOn(sendMessage, "QueueDoesNotExist"), ""},
		{"latency", fault.Rule{Phase: fault.Request, Action: fault.Latency{Wall: 1}, Trigger: fault.Always{}}, ""},
		{"invalid rule", throttleSend(fault.NextN{N: 0}), ""},
	}
	s := newStore(1)
	for _, c := range cases {
		got, ok := s.RetryableWarning(c.rule)
		if ok != (c.want != "") || !strings.Contains(got, c.want) {
			t.Errorf("%s: RetryableWarning = %q, %v; want a warning containing %q", c.name, got, ok, c.want)
		}
		if ok && !strings.Contains(got, "25 attempts") {
			t.Errorf("%s: warning %q does not mention Terraform's retries", c.name, got)
		}
	}
}

func TestRetryableWarningNamesTheConsequenceForTheRule(t *testing.T) {
	const budget, commits = "next-n budget", "runs the handler again against the state the first attempt already committed"
	cases := []struct {
		name      string
		rule      fault.Rule
		mentions  []string
		leavesOut []string
	}{
		{"next-n throttle", throttleSend(fault.NextN{N: 3}), []string{budget}, []string{commits}},
		{"always throttle", throttleSend(fault.Always{}), nil, []string{budget, commits}},
		{"probability throttle", throttleSend(fault.Probability{P: 0.5}), nil, []string{budget, commits}},
		{
			"after-commit",
			fault.Rule{
				Match: fault.Match{Operations: []ref.OperationID{sendMessage}}, Phase: fault.Request,
				Action: fault.AfterCommit{Shape: "InternalFailure"}, Trigger: fault.Always{},
			},
			[]string{commits},
			[]string{budget},
		},
		{
			"next-n status-ok-error",
			fault.Rule{
				Match: fault.Match{Operations: []ref.OperationID{completeUpload}}, Phase: fault.Request,
				Action: fault.StatusOKError{Shape: "InternalError"}, Trigger: fault.NextN{N: 2},
			},
			[]string{budget, commits},
			nil,
		},
	}
	s := newStore(1)
	for _, c := range cases {
		got, ok := s.RetryableWarning(c.rule)
		if !ok {
			t.Errorf("%s: no warning", c.name)
			continue
		}
		for _, m := range c.mentions {
			if !strings.Contains(got, m) {
				t.Errorf("%s: warning %q does not mention %q", c.name, got, m)
			}
		}
		for _, m := range c.leavesOut {
			if strings.Contains(got, m) {
				t.Errorf("%s: warning %q mentions %q", c.name, got, m)
			}
		}
	}
}
