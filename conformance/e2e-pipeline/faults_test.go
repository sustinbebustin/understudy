package e2e_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/sustinbebustin/understudy/admin"
)

// putFault adds rule and removes it when the test ends.
func putFault(t *testing.T, rule admin.FaultRule) {
	t.Helper()
	if _, err := env.admin.PutFault(t.Context(), rule); err != nil {
		t.Fatalf("admin PutFault %s: %v", rule.ID, err)
	}
	t.Cleanup(func() {
		// A next-n rule removes itself once spent, so not_found is fine.
		err := env.admin.DeleteFault(context.WithoutCancel(t.Context()), rule.ID)
		if ae, ok := errors.AsType[*admin.Error](err); err != nil && (!ok || ae.Code != admin.NotFound) {
			t.Errorf("admin DeleteFault %s (cleanup): %v", rule.ID, err)
		}
	})
}

// faultHits counts the fault-injected journal events of rule.
func faultHits(t *testing.T, rule string) int {
	t.Helper()
	page, err := env.admin.JournalEvents(t.Context(), admin.JournalQuery{Kind: "fault-injected", Limit: 1000})
	if err != nil {
		t.Fatalf("journal query of fault-injected events: %v", err)
	}
	return len(slices.DeleteFunc(page.Events, func(e admin.JournalEvent) bool {
		var d struct {
			Rule string `json:"rule"`
		}
		return json.Unmarshal(e.Detail, &d) != nil || d.Rule != rule
	}))
}

// TestFaultsThrottleTheFunctionURL throttles the next function URL call:
// the caller gets HTTP 429, as when a function's concurrency is exhausted
// (lambda-events.md 7.2), and the call after it reaches the function.
func TestFaultsThrottleTheFunctionURL(t *testing.T) {
	p := pipe(t)
	rule := admin.FaultRule{
		ID: "e2e-furl-throttle", Phase: admin.RequestPhase,
		Match:  admin.FaultMatch{Operations: []string{"lambda.InvokeFunctionUrl"}},
		Action: admin.Throttle{}, Trigger: admin.NextN{N: 1},
	}
	putFault(t, rule)
	if resp, body := get(t, p.functionURL+"throttle-probe"); resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("the throttled function URL call answered HTTP %d (%s), want 429", resp.StatusCode, body)
	}
	if resp, body := get(t, p.functionURL+"throttle-probe"); resp.StatusCode != http.StatusCreated {
		t.Errorf("the call after the throttle answered HTTP %d (%s), want the function's 201", resp.StatusCode, body)
	}
	if n := faultHits(t, rule.ID); n != 1 {
		t.Errorf("the journal records %d hits of %s, want 1", n, rule.ID)
	}
}

// TestFaultsThrottledMappingInvokeIsRetried throttles the image worker's
// next invocation by its event source mapping. A throttle is not a
// function error: Lambda keeps retrying the message, at the latest once
// its visibility timeout lapses, so it is processed and never reaches the
// dead-letter queue (lambda-events.md 2.3).
func TestFaultsThrottledMappingInvokeIsRetried(t *testing.T) {
	p := pipe(t)
	w := p.workers[imageWorker]
	rule := admin.FaultRule{
		ID: "e2e-esm-throttle", Phase: admin.RequestPhase,
		Match:  admin.FaultMatch{Operations: []string{"lambda.Invoke"}, Resource: w.functionARN + "*", Origin: admin.InternalOrigin},
		Action: admin.Throttle{}, Trigger: admin.NextN{N: 1},
	}
	putFault(t, rule)
	id := newID(t)
	publish(t, message{ID: id}, map[string]string{"kind": "refund", "amount": "300"})
	settle(t)
	if _, done := getObject(t, processedKey(imageWorker, id)); !done {
		advance(t, visibilityOf(imageWorker))
	}
	waitProcessed(t, imageWorker, id)
	if n := faultHits(t, rule.ID); n != 1 {
		t.Errorf("the journal records %d hits of %s, want 1: the mapping's invoke was not throttled", n, rule.ID)
	}
	out, err := env.sqs.GetQueueAttributes(t.Context(), &sqs.GetQueueAttributesInput{
		QueueUrl: aws.String(w.dlq.url), AttributeNames: []sqstypes.QueueAttributeName{sqstypes.QueueAttributeNameApproximateNumberOfMessages},
	})
	if err != nil {
		t.Fatalf("sqs GetQueueAttributes %s: %v", w.dlq.url, err)
	}
	if n := out.Attributes["ApproximateNumberOfMessages"]; n != "0" {
		t.Errorf("%s holds %s messages after a throttled invoke, want 0", w.dlq.arn, n)
	}
}
