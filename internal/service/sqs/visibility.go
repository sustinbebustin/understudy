package sqs

import (
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
// HoldVisibility.
func (s *Service) bindVisibility(b sqsapi.Binder) {
	b.DeleteMessage(s.deleteMessage, registry.Full{},
		queueByURL(func(in *sqsapi.DeleteMessageInput) string { return in.QueueUrl }))
	b.DeleteMessageBatch(s.deleteMessageBatch, registry.Full{},
		kernel.WithCheck(func(in *sqsapi.DeleteMessageBatchInput) *awserr.Error {
			return checkBatchIDs("DeleteMessageBatchRequestEntry", entryIDs(in.Entries, func(e *sqsapi.DeleteMessageBatchRequestEntry) string { return e.Id }))
		}),
		queueByURL(func(in *sqsapi.DeleteMessageBatchInput) string { return in.QueueUrl }))
	b.ChangeMessageVisibility(s.changeMessageVisibility, registry.Full{},
		kernel.WithCheck(func(in *sqsapi.ChangeMessageVisibilityInput) *awserr.Error {
			return checkVisibility(in.VisibilityTimeout)
		}),
		queueByURL(func(in *sqsapi.ChangeMessageVisibilityInput) string { return in.QueueUrl }))
	b.ChangeMessageVisibilityBatch(s.changeMessageVisibilityBatch, registry.Full{},
		kernel.WithCheck(func(in *sqsapi.ChangeMessageVisibilityBatchInput) *awserr.Error {
			return checkBatchIDs("ChangeMessageVisibilityBatchRequestEntry", entryIDs(in.Entries, func(e *sqsapi.ChangeMessageVisibilityBatchRequestEntry) string { return e.Id }))
		}),
		queueByURL(func(in *sqsapi.ChangeMessageVisibilityBatchInput) string { return in.QueueUrl }))
	b.HoldVisibility(s.holdVisibility, registry.Full{},
		queueByURL(func(in *sqsapi.HoldVisibilityInput) string { return in.QueueUrl }))
}

func checkVisibility(v int32) *awserr.Error {
	if v < 0 || v > maxVisibility {
		return errVisibilityTimeout(v)
	}
	return nil
}

// deleteMessage deletes the message whose latest receipt handle is given
// (sqs.md 7.2).
func (s *Service) deleteMessage(_ context.Context, c *kernel.Call, in *sqsapi.DeleteMessageInput) (*sqsapi.DeleteMessageOutput, *awserr.Error) {
	q, err := s.existingQueue(c, in.QueueUrl)
	if err != nil {
		return nil, err
	}
	_, err = writeQueue(s, c, q, func(_ *State, queue *Queue, now clock.Instant, tx *kernel.Tx) (struct{}, *awserr.Error) {
		changed, err := s.delete(tx, q, queue, in.ReceiptHandle, now)
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
// (sqs.md 6.2).
func (s *Service) deleteMessageBatch(_ context.Context, c *kernel.Call, in *sqsapi.DeleteMessageBatchInput) (*sqsapi.DeleteMessageBatchOutput, *awserr.Error) {
	q, err := s.existingQueue(c, in.QueueUrl)
	if err != nil {
		return nil, err
	}
	return writeQueue(s, c, q, func(_ *State, queue *Queue, now clock.Instant, tx *kernel.Tx) (*sqsapi.DeleteMessageBatchOutput, *awserr.Error) {
		out := &sqsapi.DeleteMessageBatchOutput{}
		changed := false
		for _, e := range in.Entries {
			deleted, err := s.delete(tx, q, queue, e.ReceiptHandle, now)
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

// delete deletes the message whose latest receipt handle is handle and
// reports whether it did. The queue is resolved before the handle is read,
// as AWS does: a bad handle to a missing queue is NonExistentQueue. A
// handle of the right form that matches no message succeeds: the message
// was deleted already, or received again since (staleReceipt). A FIFO
// message's handle expires with its visibility timeout (sqs.md 7.2).
func (s *Service) delete(tx *kernel.Tx, q queueRef, queue *Queue, handle string, now clock.Instant) (bool, *awserr.Error) {
	if _, ok := ids.ParseReceiptHandle(handle); !ok {
		return false, errReceiptHandleIsInvalid(handle)
	}
	i := queue.byReceipt(handle)
	switch {
	case i < 0:
		return false, nil
	case queue.FIFO != nil && queue.Messages[i].visible(now):
		return false, errReceiptHandleExpired(handle)
	}
	s.removeMessage(tx, q, queue, i)
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
		return struct{}{}, s.change(tx, q, queue, in.ReceiptHandle, in.VisibilityTimeout, now, errNotAvailable)
	})
	if err != nil {
		return nil, err
	}
	return &sqsapi.ChangeMessageVisibilityOutput{}, nil
}

// changeMessageVisibilityBatch changes up to ten visibility timeouts, each
// entry failing alone (sqs.md 6.2). An entry without a VisibilityTimeout
// sets 0, as an unset integer serializes.
func (s *Service) changeMessageVisibilityBatch(_ context.Context, c *kernel.Call, in *sqsapi.ChangeMessageVisibilityBatchInput) (*sqsapi.ChangeMessageVisibilityBatchOutput, *awserr.Error) {
	q, err := s.existingQueue(c, in.QueueUrl)
	if err != nil {
		return nil, err
	}
	return writeQueue(s, c, q, func(_ *State, queue *Queue, now clock.Instant, tx *kernel.Tx) (*sqsapi.ChangeMessageVisibilityBatchOutput, *awserr.Error) {
		out := &sqsapi.ChangeMessageVisibilityBatchOutput{}
		for _, e := range in.Entries {
			var vt int32
			if e.VisibilityTimeout != nil {
				vt = *e.VisibilityTimeout
			}
			err := checkVisibility(vt)
			if err == nil {
				err = s.change(tx, q, queue, e.ReceiptHandle, vt, now, func(string) *awserr.Error { return errEntryNotAvailable() })
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

// change hides the message whose latest receipt handle is handle for vt
// seconds from now; 0 makes it receivable at once. A standard message whose
// timeout ran out is still changed while nobody received it again; a FIFO
// one's handle has expired. The 12-hour ceiling counts from the receive
// that issued the handle. notAvailable is the error for a handle that
// matches no message, which the single and the batch call word apart.
func (s *Service) change(tx *kernel.Tx, q queueRef, queue *Queue, handle string, vt int32, now clock.Instant, notAvailable func(string) *awserr.Error) *awserr.Error {
	if _, ok := ids.ParseReceiptHandle(handle); !ok {
		return errReceiptHandleIsInvalid(handle)
	}
	i := queue.byReceipt(handle)
	if i < 0 {
		return notAvailable(handle)
	}
	latest, _ := queue.Messages[i].latest()
	switch {
	case queue.FIFO != nil && queue.Messages[i].visible(now):
		return errReceiptHandleExpired(handle)
	case latest.At.Add(seconds(maxVisibility)).Before(now.Add(seconds(vt))):
		return errVisibilityCeiling(vt)
	}
	m := queue.message(i)
	m.VisibleAt = now.Add(seconds(vt))
	s.armVisibility(tx, q, m, now)
	if queue.FIFO != nil {
		queue.FIFO.forget(m.ID)
	}
	return nil
}

// holdVisibility pins received messages invisible, or releases them to
// their visibility timeout, for the Lambda event source mapping poller
// under a frozen or scaled clock (ARCHITECTURE.md 9). A released message
// whose timeout passed while pinned is receivable at once. A handle that
// matches no message is skipped, as the message was deleted already.
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
			i := queue.byReceipt(h)
			if i < 0 || queue.Messages[i].pinned() == pin {
				continue
			}
			m := queue.message(i)
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
