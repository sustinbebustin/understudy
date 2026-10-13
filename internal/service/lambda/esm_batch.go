package lambda

import (
	"cmp"
	"context"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/service/lambda/esm"
)

// A poller's batch: what a step receives, filters and holds, and the
// invocation that consumes it. The poller's lifecycle is in
// esm_runtime.go, its calls that pin, release and delete messages in
// esm_sqs.go.

// receive fills pl's batch: the messages carried from the last one, then
// ReceiveMessage of up to ten at a time until the batch holds BatchSize,
// reaches 6 MB or the queue has nothing receivable. Messages no filter
// matches are deleted at once. received reports whether the queue
// returned any message.
func (p *pollers) receive(ctx context.Context, c *kernel.Call, pl *poller, m *Mapping, filters esm.Filters, target kernel.Target) (received bool, err *awserr.Error) {
	h := &pl.held
	carried := h.carry
	h.carry = nil
	for i := range carried {
		if err := p.add(pl, carried[i]); err != nil {
			return false, err
		}
	}
	for h.batch.Len() < int(m.BatchSize) && len(h.carry) == 0 {
		want := min(maxReceive, int(m.BatchSize)-h.batch.Len())
		out, err := kernel.Invoke(ctx, p.x.k, c, sqsapi.OpReceiveMessage, target, &sqsapi.ReceiveMessageInput{
			QueueUrl:                    esm.QueueURL(pl.queue),
			MaxNumberOfMessages:         new(int32(want)), //nolint:gosec // want is between 1 and maxReceive
			WaitTimeSeconds:             new(int32(0)),
			MessageSystemAttributeNames: []sqsapi.MessageSystemAttributeName{sqsapi.MessageSystemAttributeNameAll},
			MessageAttributeNames:       []string{"All"},
		})
		if err != nil {
			return received, err
		}
		if len(out.Messages) == 0 {
			return received, nil
		}
		received = true
		if err := p.take(ctx, c, pl, out.Messages, filters, target); err != nil {
			return received, err
		}
	}
	return received, nil
}

// take holds the messages a receive returned: pinned first when the
// clock pins, then filtered, then added to the batch or carried.
func (p *pollers) take(ctx context.Context, c *kernel.Call, pl *poller, msgs []sqsapi.Message, filters esm.Filters, target kernel.Target) *awserr.Error {
	if pins(p.x.k.Clock().Mode()) {
		handles := make([]string, len(msgs))
		for i := range msgs {
			handles[i] = deref(msgs[i].ReceiptHandle)
		}
		p.pin(ctx, c, pl, target, handles)
	}
	var unmatched []string
	for i := range msgs {
		r := esm.NewRecord(&msgs[i], pl.queue)
		ok, err := filters.Match(&r)
		if err != nil {
			p.x.k.Log().LogAttrs(ctx, slog.LevelWarn, "an event source mapping's filters could not evaluate a message; it is deleted as unmatched",
				pl.attrs(slog.String("message_id", r.MessageID), slog.String("error", err.Error()))...)
			c.Record(ctx, pl.scope, journal.Advisory{
				Kind:     journal.AdvisoryFilterUndecided,
				Resource: pl.arn,
				Text:     "message " + r.MessageID + " of " + pl.queue.String() + " was deleted as unmatched: " + err.Error(),
			})
		}
		if !ok {
			unmatched = append(unmatched, r.ReceiptHandle)
			continue
		}
		if err := p.add(pl, r); err != nil {
			return err
		}
	}
	undeleted := p.delete(ctx, c, pl, target, unmatched)
	p.unpin(ctx, c, pl, unmatched, undeleted)
	return nil
}

// add puts r in pl's batch, opening its window, or carries it once the
// batch is at 6 MB.
func (p *pollers) add(pl *poller, r esm.Record) *awserr.Error {
	h := &pl.held
	if len(h.carry) > 0 {
		h.carry = append(h.carry, r)
		return nil
	}
	if h.batch.Len() == 0 {
		h.opened = p.x.k.Clock().Now()
	}
	ok, err := h.batch.Add(r)
	if err != nil {
		return awserr.Internalf(err)
	}
	if !ok {
		h.carry = append(h.carry, r)
	}
	return nil
}

// windowOpen reports whether h's batching window is still open at now, and
// when it closes. A full batch, a batch at 6 MB and a FIFO queue's batch
// never wait.
func windowOpen(m *Mapping, h *held, now clock.Instant) (clock.Instant, bool) {
	if m.Window == 0 || strings.HasSuffix(m.Source, ".fifo") || h.batch.Len() >= int(m.BatchSize) || len(h.carry) > 0 {
		return clock.Instant{}, false
	}
	closes := h.opened.Add(time.Duration(m.Window) * time.Second)
	return closes, now.Before(closes)
}

// lambdaService is the service principal an event source mapping invokes
// its function as.
const lambdaService = "lambda.amazonaws.com"

// invoke runs the function synchronously on pl's batch through Invoke, as
// Lambda on its own behalf, so fault rules on lambda.Invoke reach it like
// any internal call (ADR 0008). The Invoke continues the messages' causal
// chains: its parent is the earliest message's enqueue event and the other
// messages' are its links (ADR 0007), and the deletes hang from the same
// event. It deletes the messages that succeeded and releases the pins of
// the rest, which return to the queue when their visibility timeout ends.
func (p *pollers) invoke(ctx context.Context, c *kernel.Call, pl *poller, m *Mapping, t target, sqs kernel.Target) {
	h := &pl.held
	batch := h.batch
	h.batch, h.opened = esm.Batch{}, clock.Instant{}
	cause, links := p.causes(ctx, c, pl, sqs, &batch)
	fn := kernel.Target{Scope: pl.scope, As: kernel.AsPlatform{Name: lambdaService}, Via: ref.LambdaESM, Resume: cause, Links: links}
	out, err := kernel.Invoke(ctx, p.x.k, c, lambdaapi.OpInvoke, fn, &lambdaapi.InvokeInput{
		FunctionName: t.invoked.String(), InvocationType: new(lambdaapi.InvocationTypeRequestResponse), Payload: batch.Event(),
	})
	v := verdict(out, err, m.ReportFailures, batch.MessageIDs())
	if f, ok := v.(esm.Failed); ok {
		p.x.k.Log().LogAttrs(ctx, slog.LevelInfo, "an event source mapping's batch failed; its messages return to the queue after their visibility timeout",
			pl.attrs(slog.Int("messages", batch.Len()), slog.String("reason", f.Reason))...)
	}
	failed := setOf(failedIDs(v, batch.MessageIDs()))
	var succeeded, release []string
	records := batch.Records()
	for i := range records {
		if _, ok := failed[records[i].MessageID]; ok {
			release = append(release, records[i].ReceiptHandle)
		} else {
			succeeded = append(succeeded, records[i].ReceiptHandle)
		}
	}
	sqs.Resume = cause
	undeleted := p.delete(ctx, c, pl, sqs, succeeded)
	p.unpin(ctx, c, pl, append(succeeded, release...), append(release, undeleted...))
}

// causes asks the queue which calls enqueued batch's messages and returns
// the earliest's event as the invocation's cause and the others' events,
// oldest first, as its links. When the queue cannot say, the cause is
// zero, so the invocation hangs from the poll; the lookup failure is
// logged and the batch still runs.
func (p *pollers) causes(ctx context.Context, c *kernel.Call, pl *poller, target kernel.Target, batch *esm.Batch) (ref.Cause, []ref.EventID) {
	records := batch.Records()
	handles := make([]string, len(records))
	for i := range records {
		handles[i] = records[i].ReceiptHandle
	}
	out, err := kernel.Invoke(ctx, p.x.k, c, sqsapi.OpMessageCauses, target, &sqsapi.MessageCausesInput{QueueUrl: esm.QueueURL(pl.queue), ReceiptHandles: handles})
	if err != nil {
		p.x.k.Log().LogAttrs(ctx, slog.LevelWarn, "an event source mapping could not read which calls sent its batch; the invocation is journaled under the poll",
			pl.attrs(slog.String("error", err.Message))...)
		return ref.Cause{}, nil
	}
	found := slices.SortedFunc(slices.Values(out.Causes), func(a, b sqsapi.MessageCause) int { return cmp.Compare(a.EnqueuedEvent, b.EnqueuedEvent) })
	found = slices.CompactFunc(found, func(a, b sqsapi.MessageCause) bool { return a.EnqueuedEvent == b.EnqueuedEvent })
	found = slices.DeleteFunc(found, func(m sqsapi.MessageCause) bool { return m.EnqueuedEvent <= 0 })
	if len(found) == 0 {
		return ref.Cause{}, nil
	}
	cause := ref.Cause{Parent: ref.EventID(found[0].EnqueuedEvent), Trace: ref.EventID(found[0].EnqueuedTrace)} //nolint:gosec // event ids are positive
	var links []ref.EventID
	for _, m := range found[1:] {
		links = append(links, ref.EventID(m.EnqueuedEvent)) //nolint:gosec // event ids are positive
	}
	return cause, links
}

// verdict judges an invocation: an error or a function error fails the
// batch; a success is read for batch item failures under
// ReportBatchItemFailures and is a full success otherwise.
func verdict(out *lambdaapi.InvokeOutput, err *awserr.Error, report bool, ids []string) esm.Verdict {
	switch {
	case err != nil:
		return esm.Failed{Reason: "the invocation did not run: " + err.Message}
	case out.FunctionError != nil:
		return esm.Failed{Reason: "the function failed: " + *out.FunctionError}
	case report:
		return esm.ReadResponse(out.Payload, ids)
	}
	return esm.Succeeded{}
}

// failedIDs lists the message ids of batch a verdict fails.
func failedIDs(v esm.Verdict, batch []string) []string {
	switch v := v.(type) {
	case esm.Succeeded:
		return nil
	case esm.Failed:
		return batch
	case esm.PartiallyFailed:
		return v.IDs
	}
	return batch
}
