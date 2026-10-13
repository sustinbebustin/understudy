package lambda

import (
	"context"
	"log/slog"
	"slices"
	"strconv"

	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/service/lambda/esm"
)

// A poller's calls to its queue that change what it holds: pinning
// messages while the clock does not run, releasing those pins, and
// deleting processed messages.

// pinHeld pins what pl holds unpinned when the clock pins: a batch
// received under a Running clock, held in its window when the clock was
// frozen or scaled, or one whose pin failed.
func (p *pollers) pinHeld(ctx context.Context, c *kernel.Call, pl *poller, target kernel.Target) {
	h := &pl.held
	if !pins(p.x.k.Clock().Mode()) || (h.batch.Len() == 0 && len(h.carry) == 0) {
		return
	}
	pinned := setOf(h.pinned)
	var handles []string
	held := slices.Concat(h.batch.Records(), h.carry)
	for i := range held {
		if _, ok := pinned[held[i].ReceiptHandle]; !ok {
			handles = append(handles, held[i].ReceiptHandle)
		}
	}
	p.pin(ctx, c, pl, target, handles)
}

// pin pins handles with HoldVisibility and keeps them, with the target to
// release them with, once the pin took.
func (p *pollers) pin(ctx context.Context, c *kernel.Call, pl *poller, target kernel.Target, handles []string) {
	h := &pl.held
	if p.hold(ctx, c, pl, target, sqsapi.HoldVisibilityModePin, handles) {
		h.pinned, h.target = append(h.pinned, handles...), target
	}
}

// giveBack drops what pl holds, releasing its pins, so the messages
// return to the queue when their visibility timeout ends.
func (p *pollers) giveBack(ctx context.Context, c *kernel.Call, pl *poller) {
	h := &pl.held
	h.batch, h.opened, h.carry = esm.Batch{}, clock.Instant{}, nil
	p.unpin(ctx, c, pl, h.pinned, h.pinned)
}

// unpin forgets the pins of done and releases those of release that are
// pinned.
func (p *pollers) unpin(ctx context.Context, c *kernel.Call, pl *poller, done, release []string) {
	h := &pl.held
	pinned := setOf(h.pinned)
	var held []string
	for _, handle := range release {
		if _, ok := pinned[handle]; ok {
			held = append(held, handle)
		}
	}
	gone := setOf(done)
	h.pinned = slices.DeleteFunc(h.pinned, func(handle string) bool {
		_, ok := gone[handle]
		return ok
	})
	p.hold(ctx, c, pl, h.target, sqsapi.HoldVisibilityModeRelease, held)
}

// hold pins or releases handles through HoldVisibility, and reports
// whether the call succeeded.
func (p *pollers) hold(ctx context.Context, c *kernel.Call, pl *poller, target kernel.Target, mode sqsapi.HoldVisibilityMode, handles []string) bool {
	if len(handles) == 0 {
		return true
	}
	if _, err := kernel.Invoke(ctx, p.x.k, c, sqsapi.OpHoldVisibility, target, &sqsapi.HoldVisibilityInput{
		QueueUrl: esm.QueueURL(pl.queue), ReceiptHandles: handles, Mode: mode,
	}); err != nil {
		p.x.k.Log().LogAttrs(ctx, slog.LevelWarn, "an event source mapping could not change the hold on its messages; they follow their visibility timeout",
			pl.attrs(slog.String("mode", string(mode)), slog.String("error", err.Message))...)
		return false
	}
	return true
}

// delete deletes the messages of handles, ten per DeleteMessageBatch, and
// returns the handles it could not delete: every handle of a call that
// failed, and each entry the queue refused.
func (p *pollers) delete(ctx context.Context, c *kernel.Call, pl *poller, target kernel.Target, handles []string) []string {
	var undeleted []string
	for chunk := range slices.Chunk(handles, maxReceive) {
		entries := make([]sqsapi.DeleteMessageBatchRequestEntry, len(chunk))
		for i, handle := range chunk {
			entries[i] = sqsapi.DeleteMessageBatchRequestEntry{Id: strconv.Itoa(i), ReceiptHandle: handle}
		}
		out, err := kernel.Invoke(ctx, p.x.k, c, sqsapi.OpDeleteMessageBatch, target, &sqsapi.DeleteMessageBatchInput{QueueUrl: esm.QueueURL(pl.queue), Entries: entries})
		switch {
		case err != nil:
			undeleted = append(undeleted, chunk...)
			p.x.k.Log().LogAttrs(ctx, slog.LevelWarn, "an event source mapping could not delete processed messages; they return to the queue when their visibility timeout ends",
				pl.attrs(slog.Int("messages", len(chunk)), slog.String("error", err.Message))...)
		case len(out.Failed) > 0:
			for _, f := range out.Failed {
				if i, perr := strconv.Atoi(f.Id); perr == nil && i >= 0 && i < len(chunk) {
					undeleted = append(undeleted, chunk[i])
				}
			}
			p.x.k.Log().LogAttrs(ctx, slog.LevelWarn, "an event source mapping could not delete some processed messages; they return to the queue when their visibility timeout ends",
				pl.attrs(slog.Int("failed", len(out.Failed)), slog.String("error", deref(out.Failed[0].Message)))...)
		}
	}
	return undeleted
}

// setOf is the set of the strings of s.
func setOf(s []string) map[string]struct{} {
	out := make(map[string]struct{}, len(s))
	for _, v := range s {
		out[v] = struct{}{}
	}
	return out
}

// pins reports whether the clock mode makes the poller pin what it holds
// (ARCHITECTURE.md 9).
func pins(m clock.Mode) bool {
	switch m.(type) {
	case clock.Frozen, clock.Scaled:
		return true
	case clock.Running:
	}
	return false
}
