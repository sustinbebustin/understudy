package e2e_test

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/admin"
)

// callDetail is the payload of an api-call journal event.
type callDetail struct {
	Service   string   `json:"service"`
	Operation string   `json:"operation"`
	Principal string   `json:"principal"`
	Resources []string `json:"resources"`
	Outcome   struct {
		Kind string `json:"kind"`
	} `json:"outcome"`
}

// invocationDetail is the payload of an invocation journal event.
type invocationDetail struct {
	Function string `json:"function"`
	Outcome  struct {
		Kind string `json:"kind"`
	} `json:"outcome"`
}

func decodeDetail[T any](t *testing.T, e admin.JournalEvent) T {
	t.Helper()
	var d T
	if err := json.Unmarshal(e.Detail, &d); err != nil {
		t.Fatalf("journal event %d (%s) has a detail that does not decode: %v\n%s", e.ID, e.Kind, err, e.Detail)
	}
	return d
}

// isCall reports whether e is an api-call of service.operation touching a
// resource that contains resource ("" matches any).
func isCall(t *testing.T, e admin.JournalEvent, service, operation, resource string) bool {
	t.Helper()
	if e.Kind != "api-call" {
		return false
	}
	d := decodeDetail[callDetail](t, e)
	if d.Service != service || d.Operation != operation {
		return false
	}
	return resource == "" || slices.ContainsFunc(d.Resources, func(r string) bool { return strings.Contains(r, resource) })
}

func isInvocationOf(t *testing.T, e admin.JournalEvent, fn string) bool {
	t.Helper()
	if e.Kind != "invocation" {
		return false
	}
	d := decodeDetail[invocationDetail](t, e)
	return strings.HasSuffix(d.Function, ":function:"+fn) || d.Function == fn
}

// find returns the first node of tr, in pre-order, that matches.
func find(tr admin.Trace, match func(admin.JournalEvent) bool) (admin.Trace, bool) {
	if match(tr.Event) {
		return tr, true
	}
	for _, c := range tr.Children {
		if n, ok := find(c, match); ok {
			return n, true
		}
	}
	return admin.Trace{}, false
}

// describe lists a trace for a failure message.
func describe(tr admin.Trace, depth int) string {
	var b strings.Builder
	label := tr.Event.Kind
	var d struct {
		Service, Operation, Function, Mechanism string
	}
	if json.Unmarshal(tr.Event.Detail, &d) == nil {
		label = strings.TrimSpace(fmt.Sprintf("%s %s.%s %s %s", tr.Event.Kind, d.Service, d.Operation, d.Function, d.Mechanism))
	}
	fmt.Fprintf(&b, "%s%d %s\n", strings.Repeat("  ", depth), tr.Event.ID, label)
	for _, c := range tr.Children {
		b.WriteString(describe(c, depth+1))
	}
	return b.String()
}

// TestJournalLinksAMessageFromPublishToDownstreamCalls follows one message
// through the causal journal: Publish, then the SNS-to-SQS SendMessage,
// then the Go worker's invocation, beside which, under the same parent,
// sit the function's own calls to Secrets Manager, DynamoDB, S3 (with its
// KMS data key) and CloudWatch Logs, made as the execution role, and the
// mapping's delete of the message.
func TestJournalLinksAMessageFromPublishToDownstreamCalls(t *testing.T) {
	p := pipe(t)
	id := newID(t)
	pub := publish(t, message{ID: id}, map[string]string{"kind": "order"})
	waitProcessed(t, goWorker, id)
	settle(t)

	tr, err := env.admin.Trace(t.Context(), admin.ByRequestID{RequestID: pub.RequestID})
	if err != nil {
		t.Fatalf("journal trace of Publish request %s: %v", pub.RequestID, err)
	}
	if !isCall(t, tr.Event, "sns", "Publish", p.topicARN) {
		t.Fatalf("the trace of request %s is rooted at event %d (%s), want the sns Publish call:\n%s", pub.RequestID, tr.Event.ID, tr.Event.Kind, describe(*tr, 0))
	}
	send, ok := find(*tr, func(e admin.JournalEvent) bool {
		return isCall(t, e, "sqs", "SendMessage", p.workers[goWorker].queue.arn)
	})
	if !ok {
		t.Fatalf("no sqs SendMessage to %s under the Publish:\n%s", p.workers[goWorker].queue.arn, describe(*tr, 0))
	}
	inv, ok := find(send, func(e admin.JournalEvent) bool { return isInvocationOf(t, e, goWorker) })
	if !ok {
		t.Errorf("no %s invocation caused by the SendMessage (event %d):\n%s", goWorker, send.Event.ID, describe(*tr, 0))
		// Go on from the invocation that wrote the message's object, to
		// check the function's calls link to it wherever it sits.
		if inv, ok = invocationThatWrote(t, send.Event.ID, processedKey(goWorker, id)); !ok {
			t.Fatalf("no %s invocation since event %d has the function's PutObject of %s under it; the journal since then:\n%s",
				goWorker, send.Event.ID, processedKey(goWorker, id), eventsSince(t, send.Event.ID))
		}
	}
	if d := decodeDetail[invocationDetail](t, inv.Event); d.Outcome.Kind != "ok" {
		t.Errorf("invocation %d outcome %q, want ok", inv.Event.ID, d.Outcome.Kind)
	}
	// The function's calls hang from the event the invocation hangs from,
	// beside it: an Invocation event carries the duration, so understudy
	// appends it when the invocation ends, after the calls it caused, and
	// a journal parent always precedes its children (internal/journal
	// trace.go; internal/service/lambda/invoke.go).
	invTrace, err := env.admin.Trace(t.Context(), admin.ByEvent{ID: inv.Event.ID})
	if err != nil {
		t.Fatalf("journal trace of invocation %d: %v", inv.Event.ID, err)
	}
	cause, ok := find(*invTrace, func(e admin.JournalEvent) bool { return e.ID == inv.Event.Cause.Parent })
	if !ok {
		t.Fatalf("invocation %d's parent %d is not in its trace:\n%s", inv.Event.ID, inv.Event.Cause.Parent, describe(*invTrace, 0))
	}
	wantRole := wantRoleSession(goWorker)
	downstream := []struct{ service, operation, resource string }{
		{"secretsmanager", "GetSecretValue", p.secretARN},
		{"dynamodb", "PutItem", lockTable},
		{"s3", "PutObject", bucketName},
		{"logs", "PutLogEvents", traceLogGroup},
	}
	for _, c := range downstream {
		n, ok := find(cause, func(e admin.JournalEvent) bool { return isCall(t, e, c.service, c.operation, c.resource) })
		if !ok {
			t.Errorf("no %s.%s on %s beside the invocation (event %d), under event %d:\n%s", c.service, c.operation, c.resource, inv.Event.ID, cause.Event.ID, describe(cause, 0))
			continue
		}
		d := decodeDetail[callDetail](t, n.Event)
		if d.Outcome.Kind != "ok" || !strings.HasSuffix(d.Principal, wantRole) {
			t.Errorf("%s.%s (event %d): outcome %q, principal %q; want ok, made as the execution role (%s)", c.service, c.operation, n.Event.ID, d.Outcome.Kind, d.Principal, wantRole)
		}
		if c.operation == "PutObject" {
			if _, ok := find(n, func(e admin.JournalEvent) bool { return isCall(t, e, "kms", "GenerateDataKey", p.keyARN) }); !ok {
				t.Errorf("no kms.GenerateDataKey under the SSE-KMS PutObject (event %d):\n%s", n.Event.ID, describe(n, 0))
			}
		}
	}
	isDelete := func(e admin.JournalEvent) bool {
		return isCall(t, e, "sqs", "DeleteMessageBatch", p.workers[goWorker].queue.arn) || isCall(t, e, "sqs", "DeleteMessage", p.workers[goWorker].queue.arn)
	}
	whole, err := env.admin.Trace(t.Context(), admin.ByEvent{ID: inv.Event.ID})
	if err != nil {
		t.Fatalf("journal trace of invocation %d: %v", inv.Event.ID, err)
	}
	if _, ok := find(*whole, isDelete); !ok {
		t.Errorf("no sqs delete of the processed batch in the invocation's trace:\n%s", describe(*whole, 0))
	}
}

// wantRoleSession is the principal a function's own calls carry: its
// execution role assumed under a session named after the function.
func wantRoleSession(fn string) string { return ":assumed-role/" + roleName + "/" + fn }

// eventsSince lists every journal event from event from on, each with its
// parent and links, for a failure message.
func eventsSince(t *testing.T, from uint64) string {
	t.Helper()
	page, err := env.admin.JournalEvents(t.Context(), admin.JournalQuery{FromID: from, Limit: 1000})
	if err != nil {
		t.Fatalf("journal query from event %d: %v", from, err)
	}
	var b strings.Builder
	for _, e := range page.Events {
		var d struct {
			Service, Operation, Function, Mechanism, Principal string
		}
		label := e.Kind
		if json.Unmarshal(e.Detail, &d) == nil {
			label = strings.Join(strings.Fields(fmt.Sprintf("%s %s.%s %s %s %s", e.Kind, d.Service, d.Operation, d.Function, d.Mechanism, d.Principal)), " ")
		}
		fmt.Fprintf(&b, "  event %d %s, parent %d, links %v\n", e.ID, label, e.Cause.Parent, e.Links)
	}
	return b.String()
}

// invocationThatWrote finds the fn invocation recorded after event from
// whose subtree holds the s3 PutObject of key.
func invocationThatWrote(t *testing.T, from uint64, key string) (admin.Trace, bool) {
	t.Helper()
	page, err := env.admin.JournalEvents(t.Context(), admin.JournalQuery{Kind: "invocation", FromID: from, Limit: 1000})
	if err != nil {
		t.Fatalf("journal query of invocations: %v", err)
	}
	for _, e := range page.Events {
		tr, err := env.admin.Trace(t.Context(), admin.ByEvent{ID: e.ID})
		if err != nil {
			t.Fatalf("journal trace of invocation %d: %v", e.ID, err)
		}
		node, ok := find(*tr, func(n admin.JournalEvent) bool { return n.ID == e.ID })
		if !ok {
			continue
		}
		// The function's calls sit beside the invocation, under its parent.
		parent, ok := find(*tr, func(n admin.JournalEvent) bool { return n.ID == e.Cause.Parent })
		if !ok {
			continue
		}
		writes := func(n admin.JournalEvent) bool {
			if !isCall(t, n, "s3", "PutObject", "") {
				return false
			}
			var d struct {
				Input struct{ Key string } `json:"input"`
			}
			return isCall(t, n, "s3", "PutObject", key) || (json.Unmarshal(n.Detail, &d) == nil && d.Input.Key == key)
		}
		if _, wrote := find(parent, writes); wrote {
			return node, true
		}
	}
	return admin.Trace{}, false
}

// TestJournalLinksEveryMessageOfABatch publishes three messages that the
// Python worker's batching window gathers, and checks each message's
// SendMessage is the parent of, or linked from, a py-worker invocation.
func TestJournalLinksEveryMessageOfABatch(t *testing.T) {
	p := pipe(t)
	var sends []uint64
	var ids []string
	for range 3 {
		id := newID(t)
		ids = append(ids, id)
		pub := publish(t, message{ID: id, Priority: 5}, nil)
		settle(t)
		tr, err := env.admin.Trace(t.Context(), admin.ByRequestID{RequestID: pub.RequestID})
		if err != nil {
			t.Fatalf("journal trace of %s: %v", pub.RequestID, err)
		}
		send, ok := find(*tr, func(e admin.JournalEvent) bool {
			return isCall(t, e, "sqs", "SendMessage", p.workers[pyWorker].queue.arn)
		})
		if !ok {
			t.Fatalf("no SendMessage to %s under Publish %s:\n%s", p.workers[pyWorker].queue.arn, pub.RequestID, describe(*tr, 0))
		}
		sends = append(sends, send.Event.ID)
	}
	advance(t, pyWindow())
	for _, id := range ids {
		waitProcessed(t, pyWorker, id)
	}
	settle(t)

	page, err := env.admin.JournalEvents(t.Context(), admin.JournalQuery{Kind: "invocation", FromID: sends[0], Limit: 1000})
	if err != nil {
		t.Fatalf("journal query of invocations: %v", err)
	}
	var unlinked []uint64
	for _, s := range sends {
		linked := slices.ContainsFunc(page.Events, func(e admin.JournalEvent) bool {
			return isInvocationOf(t, e, pyWorker) && (e.Cause.Parent == s || slices.Contains(e.Links, s))
		})
		if !linked {
			unlinked = append(unlinked, s)
		}
	}
	if len(unlinked) > 0 {
		t.Errorf("no py-worker invocation has SendMessage events %v as its parent or among its links; the journal since the first send:\n%s", unlinked, eventsSince(t, sends[0]))
	}
}
