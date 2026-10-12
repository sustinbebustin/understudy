package sqs

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/protocol/jsonx"
	"github.com/sustinbebustin/understudy/internal/registry"
)

const (
	// maxMoveRate is the highest MaxNumberOfMessagesPerSecond (sqs.md 9.3).
	maxMoveRate = 500
	// maxListedTasks is ListMessageMoveTasks' MaxResults cap, and how many
	// tasks a source queue keeps.
	maxListedTasks = 10
	// moveStep is how often a rate-limited task moves its next messages.
	moveStep = time.Second
)

// bindMoveTasks binds the message move task operations.
func (s *Service) bindMoveTasks(b sqsapi.Binder) {
	b.StartMessageMoveTask(s.startMessageMoveTask, registry.Full{},
		kernel.WithCheck(func(in *sqsapi.StartMessageMoveTaskInput) *awserr.Error {
			if r := in.MaxNumberOfMessagesPerSecond; r != nil && (*r < 1 || *r > maxMoveRate) {
				return errMoveRate(*r)
			}
			return nil
		}),
		queueByARN(func(in *sqsapi.StartMessageMoveTaskInput) string { return in.SourceArn }))
	b.ListMessageMoveTasks(s.listMessageMoveTasks, registry.Full{},
		kernel.WithCheck(func(in *sqsapi.ListMessageMoveTasksInput) *awserr.Error {
			if n := in.MaxResults; n != nil && (*n < 1 || *n > maxListedTasks) {
				return errMaxResults(*n, maxListedTasks)
			}
			return nil
		}),
		queueByARN(func(in *sqsapi.ListMessageMoveTasksInput) string { return in.SourceArn }))
	b.CancelMessageMoveTask(s.cancelMessageMoveTask, registry.Full{},
		queueByARN(func(in *sqsapi.CancelMessageMoveTaskInput) string {
			h, _ := decodeTaskHandle(in.TaskHandle)
			return h.SourceArn
		}))
}

// startMessageMoveTask starts moving the messages of a dead-letter queue,
// oldest first, to DestinationArn or each to the queue it was dead-lettered
// from (sqs.md 9.3). The first messages move at once; a rate moves that
// many more every second of emulator time, and no rate moves everything in
// one step.
func (s *Service) startMessageMoveTask(_ context.Context, c *kernel.Call, in *sqsapi.StartMessageMoveTaskInput) (*sqsapi.StartMessageMoveTaskOutput, *awserr.Error) {
	src, err := s.taskQueue(c, in.SourceArn, "SourceArn")
	if err != nil {
		return nil, err
	}
	t := MoveTask{ID: s.ids.UUID(ids.KindMessageID).String(), Source: src, Target: ToSources{}}
	if in.DestinationArn != nil {
		dst, err := s.taskQueue(c, *in.DestinationArn, "DestinationArn")
		if err != nil {
			return nil, err
		}
		t.Target = ToQueue{Queue: dst}
	}
	if in.MaxNumberOfMessagesPerSecond != nil {
		t.Rate = new(*in.MaxNumberOfMessagesPerSecond)
	}
	t, err = writeQueue(s, c, src, func(st *State, source *Queue, now clock.Instant, tx *kernel.Tx) (MoveTask, *awserr.Error) {
		if err := checkMoveTask(st, source, &t); err != nil {
			return MoveTask{}, err
		}
		t.ToMove, t.Started = t.movable(source), now
		if t.ToMove == 0 {
			t.State = TaskCompleted{}
		} else {
			t.State = TaskRunning{Next: now}
			tx.Arm(s.moveTask, t.timerID(), now)
		}
		st.Tasks = append(pruneTasks(st.Tasks, t.Source), t)
		return t, nil
	})
	if err != nil {
		return nil, err
	}
	return &sqsapi.StartMessageMoveTaskOutput{TaskHandle: new(encodeTaskHandle(&t))}, nil
}

// checkMoveTask checks a task t would start against the state: the source
// is the dead-letter queue of some queue, a destination is another queue
// that exists and is of the source's type, and no other task of the
// source runs. taskQueue resolved both queues in the caller's scope.
func checkMoveTask(st *State, source *Queue, t *MoveTask) *awserr.Error {
	if len(sourcesOf(st, t.Source)) == 0 {
		return errNotDeadLetterQueue()
	}
	if to, ok := t.Target.(ToQueue); ok {
		if to.Queue == t.Source {
			return errMoveToSource()
		}
		dq, ok := st.Queues[to.Queue.name]
		switch {
		case !ok:
			return errNoSuchArn("DestinationArn")
		case (dq.FIFO == nil) != (source.FIFO == nil):
			return errMoveQueueTypes()
		}
	}
	for i := range st.Tasks {
		if o := &st.Tasks[i]; o.Source == t.Source && o.running() {
			return errTaskRunning()
		}
	}
	return nil
}

// pruneTasks drops the oldest finished tasks of source, so with one more
// it keeps at most maxListedTasks.
func pruneTasks(tasks []MoveTask, source queueRef) []MoveTask {
	n := 0
	for i := range tasks {
		if tasks[i].Source == source {
			n++
		}
	}
	return slices.DeleteFunc(tasks, func(t MoveTask) bool {
		if n < maxListedTasks || t.Source != source || t.running() {
			return false
		}
		n--
		return true
	})
}

// listMessageMoveTasks lists the tasks of a source queue, newest first,
// MaxResults of them (default 1).
func (s *Service) listMessageMoveTasks(_ context.Context, c *kernel.Call, in *sqsapi.ListMessageMoveTasksInput) (*sqsapi.ListMessageMoveTasksOutput, *awserr.Error) {
	src, err := s.taskQueue(c, in.SourceArn, "SourceArn")
	if err != nil {
		return nil, err
	}
	limit := 1
	if in.MaxResults != nil {
		limit = int(*in.MaxResults)
	}
	results, err := readQueue(s, src, func(st *State, _ *Queue) []sqsapi.ListMessageMoveTasksResultEntry {
		var out []sqsapi.ListMessageMoveTasksResultEntry
		for i := len(st.Tasks) - 1; i >= 0 && len(out) < limit; i-- {
			if t := &st.Tasks[i]; t.Source == src {
				out = append(out, t.entry())
			}
		}
		return out
	})
	if err != nil {
		return nil, errNoSuchArn("SourceArn")
	}
	return &sqsapi.ListMessageMoveTasksOutput{Results: results}, nil
}

// cancelMessageMoveTask stops a running task; what it moved stays moved.
func (s *Service) cancelMessageMoveTask(_ context.Context, c *kernel.Call, in *sqsapi.CancelMessageMoveTaskInput) (*sqsapi.CancelMessageMoveTaskOutput, *awserr.Error) {
	h, ok := decodeTaskHandle(in.TaskHandle)
	if !ok {
		return nil, errTaskHandle()
	}
	src, err := s.taskQueue(c, h.SourceArn, "SourceArn")
	if err != nil {
		return nil, err
	}
	moved, err := writeQueue(s, c, src, func(st *State, _ *Queue, _ clock.Instant, tx *kernel.Tx) (int64, *awserr.Error) {
		i := slices.IndexFunc(st.Tasks, func(t MoveTask) bool {
			return t.ID == h.TaskID && t.Source == src && t.running()
		})
		if i < 0 {
			return 0, errNoSuchTask()
		}
		t := st.Tasks[i]
		s.endTask(tx, &t, TaskCancelled{})
		st.Tasks[i] = t
		return t.Moved, nil
	})
	if err != nil {
		return nil, err
	}
	return &sqsapi.CancelMessageMoveTaskOutput{ApproximateNumberOfMessagesMoved: moved}, nil
}

// onMoveTask moves a running task's next messages, oldest first: Rate of
// them, or all that are left without one. A message whose destination does
// not exist fails the task with that error's code (sqs.md 9.3). The task
// completes once it has moved what it set out to or nothing it moves is
// left.
func (s *Service) onMoveTask(c *kernel.Call, key clock.TimerKey, due clock.Instant) {
	src, id, ok := parseTimerID(key.ID)
	if !ok {
		return
	}
	s.timerWrite(c, src, "message move task", func(st *State, source *Queue, now clock.Instant, tx *kernel.Tx) bool {
		i := slices.IndexFunc(st.Tasks, func(t MoveTask) bool { return t.ID == id && t.running() })
		if i < 0 {
			return false
		}
		t := st.Tasks[i]
		s.step(c, st, src, source, &t, due, now, tx)
		st.Tasks[i] = t
		return true
	})
}

// step moves t's next messages out of source and sets what t does next.
// It copies each message to its destination, then takes every one it
// copied out of source in one pass, so a step that moves a whole queue
// costs one pass over it. The destination queues change through working
// copies, stored back once the step ends.
func (s *Service) step(c *kernel.Call, st *State, src queueRef, source *Queue, t *MoveTask, due, now clock.Instant, tx *kernel.Tx) {
	budget := t.ToMove - t.Moved
	if t.Rate != nil {
		budget = min(budget, int64(*t.Rate))
	}
	dests := map[string]*Queue{}
	var moved []int
	failed := false
	for i := range source.Messages {
		if int64(len(moved)) == budget {
			break
		}
		m := &source.Messages[i]
		to, ok := t.targetOf(m)
		if !ok {
			continue
		}
		dest, ok := destination(st, dests, src, to)
		if !ok {
			failed = true
			break
		}
		s.moveMessage(c, m, to, dest, now, tx)
		moved = append(moved, i)
	}
	for name, q := range dests {
		st.Queues[name] = *q
	}
	s.removeMessages(tx, src, source, moved)
	t.Moved += int64(len(moved))
	switch {
	case failed:
		t.State = TaskFailed{Reason: sqsapi.IdentityQueueDoesNotExist().QueryCode}
	case t.Moved >= t.ToMove || t.nextMovable(source) < 0:
		t.State = TaskCompleted{}
	default:
		next := due.Add(moveStep)
		t.State = TaskRunning{Next: next}
		tx.Arm(s.moveTask, t.timerID(), next)
	}
}

// destination returns the working copy of the queue to, cloning it into
// dests, by name, on first use. ok is false when no such queue exists in
// src's scope; src itself, whose working copy the caller holds, is never
// one.
func destination(st *State, dests map[string]*Queue, src, to queueRef) (*Queue, bool) {
	if to.scope != src.scope || to.name == src.name {
		return nil, false
	}
	if q, ok := dests[to.name]; ok {
		return q, true
	}
	stored, ok := st.Queues[to.name]
	if !ok {
		return nil, false
	}
	q := stored.clone()
	dests[to.name] = &q
	return &q, true
}

// moves reports whether t moves m (targetOf).
func (t *MoveTask) moves(m *Message) bool {
	_, ok := t.targetOf(m)
	return ok
}

// targetOf returns the queue t moves m to: the destination when t has
// one; without one, the queue m was dead-lettered from, when that is
// another queue. A message sent straight to the dead-letter queue has no
// queue to go back to, so ok is false and it stays where it is.
func (t *MoveTask) targetOf(m *Message) (queueRef, bool) {
	switch to := t.Target.(type) {
	case ToQueue:
		return to.Queue, true
	case ToSources:
		if from := m.DeadLetterSource; from != nil && *from != t.Source {
			return *from, true
		}
	}
	return queueRef{}, false
}

// movable counts the messages of queue t moves.
func (t *MoveTask) movable(queue *Queue) int64 {
	var n int64
	for i := range queue.Messages {
		if t.moves(&queue.Messages[i]) {
			n++
		}
	}
	return n
}

// nextMovable returns the index of the oldest message of queue t moves, or
// -1 when there is none.
func (t *MoveTask) nextMovable(queue *Queue) int {
	for i := range queue.Messages {
		if t.moves(&queue.Messages[i]) {
			return i
		}
	}
	return -1
}

// moveMessage copies m to dest, the working copy of the queue d, as a new
// message: a new id, sent now, never received (movedIdentity). The caller
// takes m out of its queue.
func (s *Service) moveMessage(c *kernel.Call, m *Message, d queueRef, dest *Queue, now clock.Instant, tx *kernel.Tx) {
	moved := Message{
		ID:            s.ids.MessageID(),
		Body:          m.Body,
		BodyMD5:       m.BodyMD5,
		Attributes:    cloneAttributes(m.Attributes),
		System:        cloneAttributes(m.System),
		GroupID:       m.GroupID,
		SenderID:      m.SenderID,
		Enqueued:      c.Event(),
		EnqueuedTrace: c.Cause().Trace,
		Sent:          now,
		VisibleAt:     now.Add(seconds(dest.Attributes.DelaySeconds)),
	}
	if dest.FIFO != nil {
		moved.FIFO = &FIFOMessage{DedupID: string(moved.ID), Sequence: dest.FIFO.nextSequence()}
	}
	s.append(tx, d, dest, moved, now)
}

// endTask ends running task t in end, a finished state, and disarms its
// timer.
func (s *Service) endTask(tx *kernel.Tx, t *MoveTask, end TaskState) {
	tx.Disarm(s.moveTask, t.timerID())
	t.State = end
}

// taskQueue resolves a move task's queue ARN in the caller's account and
// region; a malformed ARN, one of another scope and a missing queue are
// all ResourceNotFoundException naming param (sqs.md 2.4).
func (s *Service) taskQueue(c *kernel.Call, raw, param string) (queueRef, *awserr.Error) {
	a, err := aws.ParseARN(raw)
	if err != nil || a.Service != "sqs" || strings.Contains(a.Resource, ":") {
		return queueRef{}, errNoSuchArn(param)
	}
	if _, err := kernel.ResolveScope(c, kernel.SameScope{ARN: a, Mismatch: errNoSuchArn(param)}); err != nil {
		return queueRef{}, err
	}
	q, ok := queueOf(a)
	if !ok || !s.exists(q) {
		return queueRef{}, errNoSuchArn(param)
	}
	return q, nil
}

// entry renders t as ListMessageMoveTasks does: the handle only while it
// runs, the destination and rate only when the caller set them, the
// failure reason only when it failed.
func (t *MoveTask) entry() sqsapi.ListMessageMoveTasksResultEntry {
	e := sqsapi.ListMessageMoveTasksResultEntry{
		Status:                            new(t.State.status()),
		SourceArn:                         new(t.Source.arn().String()),
		ApproximateNumberOfMessagesMoved:  t.Moved,
		ApproximateNumberOfMessagesToMove: new(t.ToMove),
		StartedTimestamp:                  t.Started.Wire().UnixMilli(),
	}
	switch st := t.State.(type) {
	case TaskRunning:
		e.TaskHandle = new(encodeTaskHandle(t))
	case TaskFailed:
		e.FailureReason = new(st.Reason)
	case TaskCompleted, TaskCancelled:
	}
	switch to := t.Target.(type) {
	case ToQueue:
		e.DestinationArn = new(to.Queue.arn().String())
	case ToSources:
	}
	if t.Rate != nil {
		e.MaxNumberOfMessagesPerSecond = new(*t.Rate)
	}
	return e
}

// queueByARN names the queue an input addresses by ARN as the call's
// resource, before fault rules run.
func queueByARN[In any](arnOf func(*In) string) kernel.BindOption {
	return kernel.WithResources(func(_ *kernel.Call, in *In) []aws.ARN {
		a, err := aws.ParseARN(arnOf(in))
		if err != nil || a.Service != "sqs" {
			return nil
		}
		return []aws.ARN{a}
	})
}

// taskHandle is what a TaskHandle encodes: base64 of a JSON document with
// the task id and the source queue's ARN, the form AWS accepted (sqs.md 3).
type taskHandle struct {
	TaskID    string `json:"taskId"`
	SourceArn string `json:"sourceArn"`
}

func encodeTaskHandle(t *MoveTask) string {
	b := append([]byte(`{"taskId":`), jsonx.AppendString(nil, t.ID)...)
	b = append(append(b, `,"sourceArn":`...), jsonx.AppendString(nil, t.Source.arn().String())...)
	return base64.StdEncoding.EncodeToString(append(b, '}'))
}

func decodeTaskHandle(s string) (taskHandle, bool) {
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return taskHandle{}, false
	}
	var h taskHandle
	if json.Unmarshal(raw, &h) != nil || h.TaskID == "" || h.SourceArn == "" {
		return taskHandle{}, false
	}
	return h, true
}
