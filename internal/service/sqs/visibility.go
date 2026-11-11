package sqs

import (
	"cmp"
	"context"

	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// bindVisibility binds what acts on received messages by receipt handle:
// DeleteMessage, ChangeMessageVisibility, their batches and the internal
// HoldVisibility and MessageCauses. Every handle a message was issued
// reaches it, and the state's receipt handle index finds the queue that
// issued a handle (receiptIssuers); the notes are what no recording shows.
func (s *Service) bindVisibility(b sqsapi.Binder) {
	b.DeleteMessage(s.deleteMessage, registry.Partial{Note: "unrecorded, understudy's choice: a receipt handle of another queue succeeds and deletes nothing, as AWS answers one batch entry with it as not valid for this queue"},
		queueByURL(s, func(in *sqsapi.DeleteMessageInput) string { return in.QueueUrl }))
	b.DeleteMessageBatch(s.deleteMessageBatch, registry.Partial{Note: "a receipt handle of a queue in another account or Region, or of a message gone from another queue, is reported successful as one of a deleted message, where AWS fails it as not valid for this queue by inference from sqs/R01 06-delete-batch"},
		kernel.WithCheck(func(in *sqsapi.DeleteMessageBatchInput) *awserr.Error {
			return checkBatchIDs("DeleteMessageBatchRequestEntry", entryIDs(in.Entries, func(e *sqsapi.DeleteMessageBatchRequestEntry) string { return e.Id }))
		}),
		queueByURL(s, func(in *sqsapi.DeleteMessageBatchInput) string { return in.QueueUrl }))
	b.ChangeMessageVisibility(s.changeMessageVisibility, registry.Partial{Note: "unrecorded, understudy's choice: a receipt handle of another queue is refused as not available for a visibility change, as AWS answers one batch entry with it as not valid for this queue"},
		kernel.WithCheck(func(in *sqsapi.ChangeMessageVisibilityInput) *awserr.Error {
			return checkVisibility(in.VisibilityTimeout)
		}),
		queueByURL(s, func(in *sqsapi.ChangeMessageVisibilityInput) string { return in.QueueUrl }))
	b.ChangeMessageVisibilityBatch(s.changeMessageVisibilityBatch, registry.Partial{Note: "a receipt handle of a queue in another account or Region, or of a message gone from another queue, fails as not available for a visibility change, where AWS fails it as not valid for this queue by inference from sqs/R01 06-cmv-batch"},
		kernel.WithCheck(func(in *sqsapi.ChangeMessageVisibilityBatchInput) *awserr.Error {
			return checkBatchIDs("ChangeMessageVisibilityBatchRequestEntry", entryIDs(in.Entries, func(e *sqsapi.ChangeMessageVisibilityBatchRequestEntry) string { return e.Id }))
		}),
		queueByURL(s, func(in *sqsapi.ChangeMessageVisibilityBatchInput) string { return in.QueueUrl }))
	b.HoldVisibility(s.holdVisibility, registry.Full{},
		queueByURL(s, func(in *sqsapi.HoldVisibilityInput) string { return in.QueueUrl }))
	b.MessageCauses(s.messageCauses, registry.Full{},
		queueByURL(s, func(in *sqsapi.MessageCausesInput) string { return in.QueueUrl }))
}

// messageCauses answers the internal MessageCauses: the journal event that
// enqueued each message a receipt handle names, and its trace, so an event
// source mapping's invocation continues the messages' causal chains
// (ADR 0007). A handle of a message that is gone is left out. A message
// whose saved state carries no trace reports its event as the trace.
func (s *Service) messageCauses(_ context.Context, c *kernel.Call, in *sqsapi.MessageCausesInput) (*sqsapi.MessageCausesOutput, *awserr.Error) {
	q, err := s.existingQueue(c, in.QueueUrl)
	if err != nil {
		return nil, err
	}
	for _, h := range in.ReceiptHandles {
		if _, ok := ids.ParseReceiptHandle(h); !ok {
			return nil, errReceiptHandleIsInvalid(h)
		}
	}
	causes, err := readQueue(s, q, func(_ *State, queue *Queue) []sqsapi.MessageCause {
		var out []sqsapi.MessageCause
		for _, h := range in.ReceiptHandles {
			i, _ := queue.byReceipt(h)
			if i < 0 {
				continue
			}
			m := queue.Messages.at(i)
			out = append(out, sqsapi.MessageCause{ReceiptHandle: h, EnqueuedEvent: int64(m.Enqueued), EnqueuedTrace: int64(cmp.Or(m.EnqueuedTrace, m.Enqueued))}) //nolint:gosec // event ids stay far below 2^63
		}
		return out
	})
	if err != nil {
		return nil, err
	}
	return &sqsapi.MessageCausesOutput{Causes: causes}, nil
}

func checkVisibility(v int32) *awserr.Error {
	if v < 0 || v > maxVisibility {
		return errVisibilityTimeout(v)
	}
	return nil
}

// deleteMessage deletes the message the receipt handle was issued to
// (sqs.md 7.2). A handle that reaches no message in the queue succeeds:
// the message was deleted already. One another queue issued succeeds too;
// what AWS answers it outside a batch is unrecorded.
func (s *Service) deleteMessage(_ context.Context, c *kernel.Call, in *sqsapi.DeleteMessageInput) (*sqsapi.DeleteMessageOutput, *awserr.Error) {
	q, err := s.existingQueue(c, in.QueueUrl)
	if err != nil {
		return nil, err
	}
	_, err = writeQueue(s, c, q, func(st *State, queue *Queue, now clock.Instant, tx *kernel.Tx) (struct{}, *awserr.Error) {
		changed, err := s.delete(tx, st, q, queue, in.ReceiptHandle, now, func(string) *awserr.Error { return nil })
		if !changed {
			tx.Unchanged()
		}
		return struct{}{}, err
	})
	if err != nil {
		return nil, err
	}
	return &sqsapi.DeleteMessageOutput{}, nil
}

// deleteMessageBatch deletes up to ten messages, each entry failing alone
// (sqs.md 6.2). An entry whose handle reaches no message succeeds, unless
// another queue issued it (notForQueue).
func (s *Service) deleteMessageBatch(_ context.Context, c *kernel.Call, in *sqsapi.DeleteMessageBatchInput) (*sqsapi.DeleteMessageBatchOutput, *awserr.Error) {
	q, err := s.existingQueue(c, in.QueueUrl)
	if err != nil {
		return nil, err
	}
	return writeQueue(s, c, q, func(st *State, queue *Queue, now clock.Instant, tx *kernel.Tx) (*sqsapi.DeleteMessageBatchOutput, *awserr.Error) {
		out := &sqsapi.DeleteMessageBatchOutput{}
		changed := false
		unknown := notForQueue(st, q, func(string) *awserr.Error { return nil })
		for _, e := range in.Entries {
			deleted, err := s.delete(tx, st, q, queue, e.ReceiptHandle, now, unknown)
			changed = changed || deleted
			if err != nil {
				out.Failed = append(out.Failed, batchFailure(e.Id, err))
				continue
			}
			out.Successful = append(out.Successful, sqsapi.DeleteMessageBatchResultEntry{Id: e.Id})
		}
		if !changed {
			tx.Unchanged()
		}
		return out, nil
	})
}

// delete deletes the message that was issued handle and reports whether it
// did. The queue is resolved before the handle is read, as AWS does: a bad
// handle to a missing queue is NonExistentQueue. On a standard queue any
// handle of the message deletes it (sqs/R01 03-recv-after-6s). A FIFO
// message's handle expires with its visibility timeout, and an older one
// once the message is received again (sqs/R01
// 07-fifo-delete-h1-after-rereceive). unknown is what a handle of the right
// form that reaches no message in queue answers, which the single and the
// batch call tell apart.
func (s *Service) delete(tx *kernel.Tx, st *State, q queueRef, queue *Queue, handle string, now clock.Instant, unknown func(string) *awserr.Error) (bool, *awserr.Error) {
	if _, ok := ids.ParseReceiptHandle(handle); !ok {
		return false, errReceiptHandleIsInvalid(handle)
	}
	i, how := queue.byReceipt(handle)
	switch {
	case how == notIssued:
		return false, unknown(handle)
	case queue.FIFO != nil && (how == issuedEarlier || queue.Messages.at(i).visible(now)):
		return false, errReceiptHandleExpired(handle)
	}
	s.removeMessage(tx, st, q, queue, i)
	return true, nil
}

// changeMessageVisibility sets a received message's visibility timeout
// from now (sqs.md 7.2).
func (s *Service) changeMessageVisibility(_ context.Context, c *kernel.Call, in *sqsapi.ChangeMessageVisibilityInput) (*sqsapi.ChangeMessageVisibilityOutput, *awserr.Error) {
	q, err := s.existingQueue(c, in.QueueUrl)
	if err != nil {
		return nil, err
	}
	_, err = writeQueue(s, c, q, func(_ *State, queue *Queue, now clock.Instant, tx *kernel.Tx) (struct{}, *awserr.Error) {
		// A handle another queue issued answers as one that reaches no
		// message: what AWS answers it outside a batch is unrecorded.
		return struct{}{}, s.change(tx, q, queue, in.ReceiptHandle, in.VisibilityTimeout, now, errNotAvailable)
	})
	if err != nil {
		return nil, err
	}
	return &sqsapi.ChangeMessageVisibilityOutput{}, nil
}

// changeMessageVisibilityBatch changes up to ten visibility timeouts, each
// entry failing alone (sqs.md 6.2). An entry without a VisibilityTimeout
// sets 0, as an unset integer serializes. An entry whose handle reaches no
// message fails as not available, or as not valid for this queue when
// another queue issued it (notForQueue).
func (s *Service) changeMessageVisibilityBatch(_ context.Context, c *kernel.Call, in *sqsapi.ChangeMessageVisibilityBatchInput) (*sqsapi.ChangeMessageVisibilityBatchOutput, *awserr.Error) {
	q, err := s.existingQueue(c, in.QueueUrl)
	if err != nil {
		return nil, err
	}
	return writeQueue(s, c, q, func(st *State, queue *Queue, now clock.Instant, tx *kernel.Tx) (*sqsapi.ChangeMessageVisibilityBatchOutput, *awserr.Error) {
		out := &sqsapi.ChangeMessageVisibilityBatchOutput{}
		unknown := notForQueue(st, q, func(string) *awserr.Error { return errEntryNotAvailable() })
		for _, e := range in.Entries {
			var vt int32
			if e.VisibilityTimeout != nil {
				vt = *e.VisibilityTimeout
			}
			err := checkVisibility(vt)
			if err == nil {
				err = s.change(tx, q, queue, e.ReceiptHandle, vt, now, unknown)
			}
			if err != nil {
				out.Failed = append(out.Failed, batchFailure(e.Id, err))
				continue
			}
			out.Successful = append(out.Successful, sqsapi.ChangeMessageVisibilityBatchResultEntry{Id: e.Id})
		}
		if len(out.Successful) == 0 {
			tx.Unchanged()
		}
		return out, nil
	})
}

// change hides the message that was issued handle for vt seconds from now;
// 0 makes it receivable at once. On a standard queue any handle of the
// message changes it, whose timeout ran out or not (sqs/R01 05-cmv-stale);
// a FIFO message's handle expires with its visibility timeout, and an
// older one once the message is received again, as for DeleteMessage
// (sqs.fifo.receipt-expiry). The 12-hour ceiling counts from the latest
// receive (sqs.visibility.ceiling-from). unknown is the error for a handle
// that reaches no message in queue, which the single and the batch call
// word apart.
func (s *Service) change(tx *kernel.Tx, q queueRef, queue *Queue, handle string, vt int32, now clock.Instant, unknown func(string) *awserr.Error) *awserr.Error {
	if _, ok := ids.ParseReceiptHandle(handle); !ok {
		return errReceiptHandleIsInvalid(handle)
	}
	i, how := queue.byReceipt(handle)
	if how == notIssued {
		return unknown(handle)
	}
	m := queue.Messages.at(i)
	latest, _ := m.latest()
	switch {
	case queue.FIFO != nil && (how == issuedEarlier || m.visible(now)):
		return errReceiptHandleExpired(handle)
	case latest.At.Add(seconds(maxVisibility)).Before(now.Add(seconds(vt))):
		return errVisibilityCeiling(vt)
	}
	m = queue.Messages.change(i)
	m.VisibleAt = now.Add(seconds(vt))
	s.armVisibility(tx, q, m, now)
	if queue.FIFO != nil {
		queue.FIFO.forget(m.ID)
	}
	return nil
}

// notForQueue is what a batch entry whose handle reaches no message in q
// answers: ReceiptHandleIsInvalid "not valid for this queue" when another
// queue of the state issued the handle (sqs/R01 06-delete-batch,
// 06-cmv-batch), otherwise what otherwise answers. A handle of a queue in
// another account or Region, or of a message gone from its queue, is not
// told apart.
func notForQueue(st *State, q queueRef, otherwise func(string) *awserr.Error) func(string) *awserr.Error {
	return func(handle string) *awserr.Error {
		if st.issuedElsewhere(handle, q.name) {
			return errReceiptHandleNotForQueue(handle)
		}
		return otherwise(handle)
	}
}

// holdVisibility pins received messages invisible, or releases them to
// their visibility timeout, for the Lambda event source mapping poller
// under a frozen or scaled clock (ARCHITECTURE.md 9). A released message
// whose timeout passed while pinned is receivable at once. A handle that
// is not the message's latest is skipped: the message was deleted
// already, or received again since, and the pin belongs to that receive.
func (s *Service) holdVisibility(_ context.Context, c *kernel.Call, in *sqsapi.HoldVisibilityInput) (*sqsapi.HoldVisibilityOutput, *awserr.Error) {
	q, err := s.existingQueue(c, in.QueueUrl)
	if err != nil {
		return nil, err
	}
	for _, h := range in.ReceiptHandles {
		if _, ok := ids.ParseReceiptHandle(h); !ok {
			return nil, errReceiptHandleIsInvalid(h)
		}
	}
	pin := in.Mode == sqsapi.HoldVisibilityModePin
	_, err = writeQueue(s, c, q, func(_ *State, queue *Queue, now clock.Instant, tx *kernel.Tx) (struct{}, *awserr.Error) {
		changed := false
		for _, h := range in.ReceiptHandles {
			i, how := queue.byReceipt(h)
			if how != issuedLatest || queue.Messages.at(i).pinned() == pin {
				continue
			}
			m := queue.Messages.change(i)
			m.pin(pin)
			s.armVisibility(tx, q, m, now)
			changed = true
		}
		if !changed {
			tx.Unchanged()
		}
		return struct{}{}, nil
	})
	if err != nil {
		return nil, err
	}
	return &sqsapi.HoldVisibilityOutput{}, nil
}
