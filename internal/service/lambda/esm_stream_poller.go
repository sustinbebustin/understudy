package lambda

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/service/lambda/esm"
	"github.com/sustinbebustin/understudy/internal/shardlog"
)

// A stream mapping's reader (lambda-stream-sources.md 12.5):
// the poller of a mapping on a DynamoDB or Kinesis stream, which reads
// either through its esm.StreamSource and holds no kind-specific code
// (12.1 item 4). It shares the SQS poller's lifecycle
// (esm_runtime.go): an idle reader waits on shardlog.WaitKey of its stream
// and steps when an append wakes it, so a quiet stream never holds a
// settle.
//
// A step reads the stream from the mapping's checkpoint through an
// esm.StreamSource as the execution role, gathering records into one
// batch until BatchSize, until the next record would take the serialized
// event past 6 MB, or until the batching window closes. The window opens
// when the reader starts and again when each invocation ends (aws-doc
// 4.2). It invokes the function synchronously and on success moves the
// checkpoint past the batch. A failed invocation retries the same batch
// after lambda.esm-stream.retry-delay (inferred, ESMS-06); under
// ReportBatchItemFailures a partial failure moves the checkpoint to just
// before the lowest reported record and retries from it (5.5). A mapping
// without a checkpoint on a shard reads from TRIM_HORIZON, or under LATEST
// from the shard's tip, which it saves as its checkpoint at once.
//
// Every checkpoint move is journaled on the mapping's ARN as a StateChange
// between positions (TRIM_HORIZON, LATEST, AFTER <seq>, AT <seq>). A
// stream that is gone stops the reader with a StateChange from Polling to
// StreamGone; the mapping stays Enabled (recorded, 7.3). A checkpoint, or
// the oldest record of a batch being retried, that falls behind the
// stream's retention drops the lost records without an on-failure record
// (inferred, UNVERIFIED), journals Polling to RecordsTrimmed and back, and
// resumes at TRIM_HORIZON (aws-doc 5.1, 5.3). Record age and retention
// are measured from the stored arrival, which GetRecords answers a
// mapping's read unfloored (dynamodb streamread.go creationExact).
//
// MaximumRetryAttempts discards a batch once that many retries have failed
// (5.2), and MaximumRecordAgeInSeconds discards each record past its age
// before an invocation (5.3); both move the checkpoint past what they
// discard. An invocation that does not reach the function is not counted
// (5.1). A discarded batch, or each record past its age, sends an
// on-failure record to the mapping's destination through the outbox, as
// the execution role (5.6). One reader
// reads the stream's shards in turn. The invocation hangs from the step's
// poll: the stream does not report the events of the writes that
// appended its records.

// The stream reader's journal states, recorded on the mapping's ARN.
const (
	pollerStreamGone     journal.ResourceState = "StreamGone"
	pollerRecordsTrimmed journal.ResourceState = "RecordsTrimmed"
)

// streamHeld is what a stream reader keeps between its steps; only the
// running step touches it.
type streamHeld struct {
	// stream is the mapping's source.
	stream aws.ARN
	// shards are the stream's shard ids, read at the first step.
	shards []string
	// retry is the failed batch waiting for its retry; nil when none is.
	retry *streamRetry
	// window is when the batching window opened: the reader's first step,
	// then the end of each invocation; zero before the first step.
	window clock.Instant
	// gone is set once a read found the stream gone: the reader then reads
	// nothing until a load or reset replaces the mapping's state, or the
	// mapping is disabled and enabled again, which starts a new reader.
	gone bool
}

// streamRetry is a failed batch and when it is retried.
type streamRetry struct {
	shard string
	batch esm.Batch[esm.StreamRecord]
	at    clock.Instant
	// attempts counts the batch's invocations that reached the function;
	// those that did not (a throttle, a missing function) are not attempts
	// (aws-doc, lambda-stream-sources.md 5.1).
	attempts int
	// request is the request id of the last of those invocations, which an
	// on-failure record names (5.6); empty before the first.
	request ids.LambdaRequestID
}

// newStreamPoller is the poller of mapping uuid, whose source is a
// DynamoDB or Kinesis stream; false when the stream ARN is not one this
// emulator can address.
func newStreamPoller(scope aws.Scope, uuid string, m Mapping) (*poller, bool) {
	stream, err := aws.ParseARN(m.Source)
	if err != nil {
		return nil, false
	}
	streamScope, serr := aws.ScopeOf(stream)
	if serr != nil {
		return nil, false
	}
	arn := aws.ARN{Partition: scope.Partition.String(), Service: "lambda", Region: scope.Region.String(), Account: scope.Account.String(), Resource: "event-source-mapping:" + uuid}
	return &poller{arn: arn, id: arn.String(), scope: scope, uuid: uuid, queueScope: streamScope, stream: &streamHeld{stream: stream}}, true
}

// streamStep runs one step of the stream reader pl on behalf of the
// timer's call c.
func (p *pollers) streamStep(ctx context.Context, c *kernel.Call, pl *poller) pollNext {
	sh := pl.stream
	if p.replaced(pl) {
		*sh = streamHeld{stream: sh.stream}
	}
	m, ok := p.mapping(pl)
	switch {
	case !ok:
		return pollStop{}
	case m.Transition != nil && (m.Transition.State == MappingCreating || m.Transition.State == MappingEnabling):
		return pollAt{at: m.Transition.Settles}
	case sh.gone:
		return pollIdle{}
	}
	kind := m.SourceKind
	start, _, ok := esm.StreamOf(kind)
	if !ok {
		return pollStop{}
	}
	now := p.x.k.Clock().Now()
	if sh.window.IsZero() {
		sh.window = now
	}
	t, err := p.x.resolve(functionRef{scope: pl.scope, name: m.Function, qualifier: m.Qualifier})
	if err != nil {
		p.unavailable(ctx, c, pl, err)
		return pollAt{at: now.Add(pl.held.backoff())}
	}
	p.available(ctx, c, pl)
	target, ok := streamTarget(sh.stream, t.role)
	if !ok {
		return pollStop{}
	}
	src := newStreamSource(p.x.k, c, kind, target, sh.stream, t.role)
	if r := sh.retry; r != nil {
		// The retention is the stream's current one: fixed for DynamoDB, the
		// RetentionPeriodHours DescribeStreamSummary answers for Kinesis.
		// A failed read keeps the batch for its retry.
		retention, rerr := src.Retention(ctx)
		if rerr != nil {
			return p.readFailed(ctx, c, pl, rerr)
		}
		sh.retry = nil
		if oldest := r.batch.Records()[0].Arrival; oldest.Compare(now.Add(-retention)) <= 0 {
			p.dropTrimmed(ctx, c, pl, r.shard, "the oldest record of the batch being retried is past the stream's retention")
			return pollAgain{}
		}
		if now.Before(r.at) {
			sh.retry = r
			return pollAt{at: r.at}
		}
		return p.expireAndInvoke(ctx, c, pl, t, m, kind, r.shard, r.batch, r.attempts, r.request)
	}
	shard, b, cut, rerr := p.readBatch(ctx, c, pl, src, int(m.BatchSize), start)
	switch {
	case rerr != nil:
		return p.readFailed(ctx, c, pl, rerr)
	case b.Len() == 0:
		return pollIdle{}
	}
	if m.Window > 0 && b.Len() < int(m.BatchSize) && !cut {
		if closes := sh.window.Add(time.Duration(m.Window) * time.Second); now.Before(closes) {
			// The records stay in the stream; the step at closes, or one an
			// append wakes sooner, reads them again.
			return pollAt{at: closes}
		}
	}
	return p.expireAndInvoke(ctx, c, pl, t, m, kind, shard, b, 0, "")
}

// expireAndInvoke discards the records of b past their age, then invokes
// the function on the rest; attempts counts b's earlier invocations that
// reached the function. A record already past its age when read reports
// one invocation (recorded, lambda-stream-sources.md 5.3); one that passed
// it while being retried reports the invocations made (recorded
// approximateInvokeCount 8, 5.3). request is the last of those
// invocations' request id, empty before the first.
func (p *pollers) expireAndInvoke(ctx context.Context, c *kernel.Call, pl *poller, t target, m Mapping, kind esm.EventSourceKind, shard string, b esm.Batch[esm.StreamRecord], attempts int, request ids.LambdaRequestID) pollNext {
	rest, err := p.expireStream(ctx, c, pl, t, kind, shard, b, attempts, request)
	switch {
	case err != nil:
		return p.readFailed(ctx, c, pl, err)
	case rest.Len() == 0:
		return pollAgain{}
	}
	return p.invokeStream(ctx, c, pl, t, m, kind, shard, rest, attempts, request)
}

// expireStream discards the records of b, read from shard, whose age
// (now minus their arrival in the stream) exceeds the settings' limit,
// each on its own with RecordAgeExceeded (recorded batchSize 1,
// lambda-stream-sources.md 5.3; age measured from the arrival and one
// record each, inferred, ESMS-09), and returns the rest. Each record's
// approximateInvokeCount is the batch's attempts, at least 1, and its
// requestId the last invocation's, or a new one when none was made.
// Arrivals do not decrease within a shard, so the expired
// records lead the batch.
func (p *pollers) expireStream(ctx context.Context, c *kernel.Call, pl *poller, t target, kind esm.EventSourceKind, shard string, b esm.Batch[esm.StreamRecord], attempts int, request ids.LambdaRequestID) (esm.Batch[esm.StreamRecord], error) {
	limit, ok := streamSettingsOf(kind).RecordAge.(esm.MaxAge)
	if !ok {
		return b, nil
	}
	oldest := p.x.k.Clock().Now().Add(-time.Duration(limit.Seconds) * time.Second)
	records := b.Records()
	n := 0
	for n < len(records) && records[n].Arrival.Before(oldest) {
		n++
	}
	if n == 0 {
		return b, nil
	}
	failed := make([]esm.FailedBatch, n)
	for i := range failed {
		id := request
		if attempts == 0 {
			id = p.x.k.IDs().LambdaRequestID()
		}
		failed[i] = p.failedBatch(pl, t, shard, records[i:i+1], esm.RecordAgeExceeded{}, max(attempts, 1), id)
	}
	p.discardStream(ctx, c, pl, t, kind, shard, failed)
	return batchOf(records[n:])
}

// streamSettingsOf is a stream kind's settings; the zero settings for an
// SQS kind, which the stream reader never holds.
func streamSettingsOf(kind esm.EventSourceKind) esm.StreamSettings {
	_, settings, _ := esm.StreamOf(kind)
	return settings
}

// failedBatch is what an on-failure record reports of records, read from
// shard and discarded now for cond after count invocations, the last
// numbered request (lambda-stream-sources.md 5.6).
func (p *pollers) failedBatch(pl *poller, t target, shard string, records []esm.StreamRecord, cond esm.FailCondition, count int, request ids.LambdaRequestID) esm.FailedBatch {
	return esm.FailedBatch{
		RequestID:   string(request),
		FunctionARN: t.invoked.String(),
		Condition:   cond,
		InvokeCount: count,
		Timestamp:   p.x.k.Clock().Now(),
		Stream:      pl.stream.stream,
		Shard:       shard,
		First:       records[0],
		Last:        records[len(records)-1],
		Size:        len(records),
	}
}

// batchOf batches records, part of a batch that fit, so they fit.
func batchOf(records []esm.StreamRecord) (esm.Batch[esm.StreamRecord], error) {
	var b esm.Batch[esm.StreamRecord]
	for _, r := range records {
		if _, err := b.Add(r); err != nil {
			return esm.Batch[esm.StreamRecord]{}, err
		}
	}
	return b, nil
}

// discardStream discards the failed batches of shard, in sequence order
// ("Lambda discards the records and continues", aws-doc,
// lambda-stream-sources.md 5.1), and moves the checkpoint past the last.
// With an on-failure destination in kind's settings, the same write hands
// the outbox one on-failure record per batch in kind's form (5.6), sent
// once as the execution role t runs as: a destination that is
// missing or refuses it is journaled as a failed delivery, and the batch
// stays discarded. Without one, the log says what was lost.
func (p *pollers) discardStream(ctx context.Context, c *kernel.Call, pl *poller, t target, kind esm.EventSourceKind, shard string, failed []esm.FailedBatch) {
	dest := streamSettingsOf(kind).OnFailure
	var sends []delivery.Request
	for i := range failed {
		f := &failed[i]
		if req, ok := onFailureDelivery(dest, pl.arn, t.role, esm.FailureRecord(kind, *f)); ok {
			sends = append(sends, req)
		} else {
			p.x.k.Log().LogAttrs(ctx, slog.LevelWarn, "an event source mapping discarded stream records its retry limit or record age ended; the mapping has no on-failure destination, and it continues after them",
				pl.attrs(slog.String("condition", conditionName(f.Condition)), slog.String("shard", shard), slog.String("first", string(f.First.Seq)), slog.String("last", string(f.Last.Seq)),
					slog.Int("records", f.Size), slog.Int("approximate_invoke_count", f.InvokeCount))...)
		}
	}
	last := failed[len(failed)-1].Last.Seq
	if err := p.moveCheckpoint(c, pl, shard, shardlog.After{Seq: last}, false, sends); err != nil {
		p.x.k.Log().LogAttrs(ctx, slog.LevelError, "an event source mapping could not move its stream checkpoint past discarded records or hand their on-failure records to the outbox; the records may be read and delivered again",
			pl.attrs(slog.String("shard", shard), slog.String("error", err.Message))...)
	}
}

// conditionName is cond's name in an on-failure record and the log.
func conditionName(cond esm.FailCondition) string {
	switch cond.(type) {
	case esm.RetryAttemptsExhausted:
		return "RetryAttemptsExhausted"
	case esm.RecordAgeExceeded:
		return "RecordAgeExceeded"
	}
	return ""
}

// onFailureDelivery is the outbox request that sends record, an on-failure
// record of the mapping source, to dest as role: SendMessage to a queue,
// or Publish to a topic with no subject and no attributes
// (lambda-stream-sources.md 5.6), each attempted once. A
// failed send counts as a destination record's failure
// (delivery.DestinationRecord). False when dest is no destination.
func onFailureDelivery(dest esm.OnFailure, source, role aws.ARN, record []byte) (delivery.Request, bool) {
	req := delivery.Request{Source: source, Policy: delivery.None{}, Sender: &delivery.LambdaSender{Role: role, Carries: delivery.DestinationRecord}}
	switch d := dest.(type) {
	case esm.QueueDestination:
		req.Mechanism, req.Target = delivery.ESMToSQS, delivery.SQSQueue{ARN: d.ARN, Body: string(record)}
	case esm.TopicDestination:
		req.Mechanism, req.Target = delivery.ESMToSNS, delivery.SNSTopic{ARN: d.ARN, Message: string(record)}
	case esm.NoDestination, nil:
		return delivery.Request{}, false
	}
	return req, true
}

// readBatch reads the first shard of pl's stream that has records past
// its checkpoint, page after page, until the batch holds limit records,
// the next record would not fit in its 6 MB (cut), or the shard has no
// more; an empty batch when no shard has any. A read that succeeds ends
// the backoff of earlier failures.
func (p *pollers) readBatch(ctx context.Context, c *kernel.Call, pl *poller, src esm.StreamSource, limit int, start esm.StreamStart) (shard string, b esm.Batch[esm.StreamRecord], cut bool, err error) {
	sh := pl.stream
	if sh.shards == nil {
		shards, err := src.Shards(ctx)
		if err != nil {
			return "", b, false, err
		}
		sh.shards = shards
	}
	for _, shard := range sh.shards {
		from, err := p.checkpoint(ctx, c, pl, src, shard, start)
		if err != nil {
			return "", b, false, err
		}
		b, cut, err = p.readShard(ctx, pl, src, shard, from, limit)
		if err != nil || b.Len() > 0 {
			return shard, b, cut, err
		}
	}
	return "", b, false, nil
}

// readShard batches the records of shard from from, page after page, as
// readBatch describes. A read that fails after earlier pages filled part
// of the batch ends the filling: the batch goes on, and the next step
// meets the error.
func (p *pollers) readShard(ctx context.Context, pl *poller, src esm.StreamSource, shard string, from shardlog.Position, limit int) (esm.Batch[esm.StreamRecord], bool, error) {
	var b esm.Batch[esm.StreamRecord]
	for b.Len() < limit {
		page, err := src.Read(ctx, shard, from, limit-b.Len())
		switch {
		case err != nil && b.Len() > 0:
			return b, false, nil
		case err != nil:
			return b, false, err
		}
		pl.held.retry = 0
		for _, r := range page.Records {
			added, err := b.Add(r)
			if err != nil {
				return esm.Batch[esm.StreamRecord]{}, false, err
			}
			if !added {
				return b, true, nil
			}
		}
		if len(page.Records) == 0 || page.Closed {
			break
		}
		from = page.Next
	}
	return b, false, nil
}

// readFailed follows a read of pl's stream that failed. A stream that is
// gone stops the reader with a journal entry, the mapping staying Enabled
// (lambda-stream-sources.md 7.3); a checkpoint behind the trim horizon
// drops the lost records and resumes at TRIM_HORIZON (5.1, 5.3); any
// other failure is logged and backs off.
func (p *pollers) readFailed(ctx context.Context, c *kernel.Call, pl *poller, err error) pollNext {
	if gone, ok := errors.AsType[*esm.GoneError](err); ok {
		pl.stream.gone = true
		p.record(ctx, c, pl, pollerPolling, pollerStreamGone)
		p.x.k.Log().LogAttrs(ctx, slog.LevelWarn, "an event source mapping's stream is gone; its reader stops and the mapping stays Enabled, as on AWS. Delete the mapping, or create one on the table's new stream",
			pl.attrs(slog.String("reason", gone.Reason))...)
		return pollIdle{}
	}
	if trimmed, ok := errors.AsType[*shardlog.TrimmedError](err); ok {
		if _, horizon := trimmed.From.(shardlog.TrimHorizon); !horizon {
			p.dropTrimmed(ctx, c, pl, trimmed.Shard, "the checkpoint is past the stream's retention")
			return pollAgain{}
		}
	}
	p.x.k.Log().LogAttrs(ctx, slog.LevelWarn, "an event source mapping could not read its stream; it tries again, backing off from a second to five minutes of emulator time, or at once when the stream receives a record",
		pl.attrs(slog.String("error", err.Error()))...)
	return pollAt{at: p.x.k.Clock().Now().Add(pl.held.backoff())}
}

// dropTrimmed drops the records of shard that pl lost to the stream's
// retention, without an on-failure record (inferred, UNVERIFIED; the
// CreateEventSourceMapping note says so), and moves its checkpoint to
// TRIM_HORIZON, journaling both edges of the RecordsTrimmed state.
func (p *pollers) dropTrimmed(ctx context.Context, c *kernel.Call, pl *poller, shard, why string) {
	p.record(ctx, c, pl, pollerPolling, pollerRecordsTrimmed)
	p.x.k.Log().LogAttrs(ctx, slog.LevelWarn, "an event source mapping lost records to its stream's retention; it drops them without an on-failure record and resumes at TRIM_HORIZON",
		pl.attrs(slog.String("shard", shard), slog.String("reason", why))...)
	p.saveCheckpoint(ctx, c, pl, shard, shardlog.TrimHorizon{}, false)
	p.record(ctx, c, pl, pollerRecordsTrimmed, pollerPolling)
}

// checkpoint is where pl reads shard from: its checkpoint, else
// TRIM_HORIZON, or under LATEST the shard's tip, resolved now and saved
// as the checkpoint so the records that arrive after it are read. A
// mapping created under the create-instant anchor already has that
// checkpoint; this resolves it under first-read, and for a mapping loaded
// from a version 2 snapshot, which saved none.
func (p *pollers) checkpoint(ctx context.Context, c *kernel.Call, pl *poller, src esm.StreamSource, shard string, start esm.StreamStart) (shardlog.Position, error) {
	pos, _ := view(p.x.svc, pl.scope, func(st *State) (shardlog.Position, *awserr.Error) {
		return st.checkpoint(pl.uuid, shard), nil
	})
	if pos != nil {
		return pos, nil
	}
	if _, latest := start.(esm.Latest); !latest {
		return shardlog.TrimHorizon{}, nil
	}
	tip, err := shardTip(ctx, src, shard)
	if err != nil {
		return nil, err
	}
	p.saveCheckpoint(ctx, c, pl, shard, tip, false)
	return tip, nil
}

// saveCheckpoint moves pl's checkpoint of shard to next, journaling the
// move on the mapping's ARN, and marks the mapping
// processed when a batch succeeded. A shard without a checkpoint moves
// from the mapping's starting position. A failed save is logged: the
// records after the old checkpoint may be delivered again.
func (p *pollers) saveCheckpoint(ctx context.Context, c *kernel.Call, pl *poller, shard string, next shardlog.Position, processed bool) {
	if werr := p.moveCheckpoint(c, pl, shard, next, processed, nil); werr != nil {
		p.x.k.Log().LogAttrs(ctx, slog.LevelError, "an event source mapping could not save its stream checkpoint; the records after its previous checkpoint may be delivered again",
			pl.attrs(slog.String("error", werr.Message))...)
	}
}

// moveCheckpoint is saveCheckpoint's write, which also hands sends to the
// outbox once it commits.
func (p *pollers) moveCheckpoint(c *kernel.Call, pl *poller, shard string, next shardlog.Position, processed bool, sends []delivery.Request) *awserr.Error {
	_, werr := write(p.x.svc, c, pl.scope, func(st *State, _ clock.Instant, tx *kernel.Tx) (struct{}, *awserr.Error) {
		for i := range sends {
			tx.Deliver(sends[i])
		}
		m, ok := st.Mappings[pl.uuid]
		if !ok {
			tx.Unchanged()
			return struct{}{}, nil
		}
		from := checkpointState(st.checkpoint(pl.uuid, shard))
		if start, _, ok := esm.StreamOf(m.SourceKind); ok && from == "" {
			from = journal.ResourceState(esm.StreamStartName(start))
		}
		if to := checkpointState(next); to != from {
			tx.Record(pl.scope, journal.StateChange{Resource: pl.arn, From: from, To: to})
		}
		st.setCheckpoint(pl.uuid, shard, next)
		if processed {
			m.Processed = true
			st.Mappings[pl.uuid] = m
		}
		return struct{}{}, nil
	})
	return werr
}

// checkpointState names a checkpoint in the journal:
// TRIM_HORIZON, LATEST, AFTER <seq> or AT <seq>; "" for none.
func checkpointState(p shardlog.Position) journal.ResourceState {
	switch p := p.(type) {
	case shardlog.TrimHorizon:
		return "TRIM_HORIZON"
	case shardlog.Latest:
		return "LATEST"
	case shardlog.After:
		return journal.ResourceState("AFTER " + string(p.Seq))
	case shardlog.At:
		return journal.ResourceState("AT " + string(p.Seq))
	case shardlog.AtTime:
		return journal.ResourceState("AT_TIMESTAMP " + p.At.String())
	}
	return ""
}

// invokeStream runs the function synchronously on b, read from shard, as
// Lambda on its own behalf (as for SQS, esm_batch.go); attempts counts the
// batch's earlier invocations that reached the function. The batching
// window opens again when the invocation ends. A success moves the
// checkpoint past b and marks the mapping processed. An invocation that
// did not reach the function is retried without counting
// (lambda-stream-sources.md 5.1). A failure counts and goes to
// failStream. Under ReportBatchItemFailures, a partial failure first
// moves the checkpoint to just before the lowest reported record and
// keeps only the records from it (5.5); when that leaves out records, the
// rest is a new batch whose count lambda.esm-stream.partial-resend-budget
// restarts or continues (inferred, ESMS-08). request is the last earlier
// invocation's request id; the invocation made here replaces it.
func (p *pollers) invokeStream(ctx context.Context, c *kernel.Call, pl *poller, t target, m Mapping, kind esm.EventSourceKind, shard string, b esm.Batch[esm.StreamRecord], attempts int, request ids.LambdaRequestID) pollNext {
	fn := kernel.Target{Scope: pl.scope, As: kernel.AsPlatform{Name: lambdaService}, Via: ref.LambdaESM}
	var invoked ids.LambdaRequestID
	out, err := kernel.Invoke(withInvokedRequest(ctx, &invoked), p.x.k, c, lambdaapi.OpInvoke, fn, &lambdaapi.InvokeInput{
		FunctionName: t.invoked.String(), InvocationType: new(lambdaapi.InvocationTypeRequestResponse), Payload: b.Event(),
	})
	pl.stream.window = p.x.k.Clock().Now()
	if err != nil {
		p.x.k.Log().LogAttrs(ctx, slog.LevelInfo, "an event source mapping's stream batch did not reach its function; it is retried with the same records and the attempt is not counted",
			pl.attrs(slog.Int("records", b.Len()), slog.String("reason", err.Message))...)
		return p.retryStream(pl, kind, shard, b, attempts, request)
	}
	attempts++
	request = invoked
	fail := func(rest esm.Batch[esm.StreamRecord], attempts int, response esm.RetryAttemptsExhausted) pollNext {
		return p.failStream(ctx, c, pl, t, kind, shard, rest, attempts, request, response)
	}
	records := b.Records()
	seqs := make([]string, len(records))
	for i, r := range records {
		seqs[i] = string(r.Seq)
	}
	response := esm.RetryAttemptsExhausted{StatusCode: int(out.StatusCode), ExecutedVersion: deref(out.ExecutedVersion), FunctionError: deref(out.FunctionError)}
	switch v := streamVerdict(out, m.ReportFailures, seqs).(type) {
	case esm.Failed:
		p.x.k.Log().LogAttrs(ctx, slog.LevelInfo, "an event source mapping's stream batch failed",
			pl.attrs(slog.Int("records", b.Len()), slog.Int("attempts", attempts), slog.String("reason", v.Reason))...)
		return fail(b, attempts, response)
	case esm.PartiallyFailed:
		lowest := slices.Index(seqs, v.IDs[0])
		p.x.k.Log().LogAttrs(ctx, slog.LevelInfo, "an event source mapping's stream batch partially failed; it is retried from the lowest reported record",
			pl.attrs(slog.Int("records", b.Len()), slog.Int("attempts", attempts), slog.String("lowest", v.IDs[0]))...)
		if lowest == 0 {
			return fail(b, attempts, response)
		}
		p.saveCheckpoint(ctx, c, pl, shard, shardlog.After{Seq: records[lowest-1].Seq}, false)
		rest, err := batchOf(records[lowest:])
		if err != nil {
			return p.readFailed(ctx, c, pl, err)
		}
		if p.x.svc.opts.Streams.ResendBudget == esm.ResendRestart {
			attempts = 0
		}
		return fail(rest, attempts, response)
	case esm.Succeeded:
	}
	p.saveCheckpoint(ctx, c, pl, shard, shardlog.After{Seq: records[len(records)-1].Seq}, true)
	return pollAgain{}
}

// streamVerdict reads an invocation that reached the function: a function
// error is complete failure; under report the response is read as
// lambda-stream-sources.md 5.5 records; otherwise any result is success.
func streamVerdict(out *lambdaapi.InvokeOutput, report bool, seqs []string) esm.Verdict {
	switch {
	case out.FunctionError != nil:
		return esm.Failed{Reason: "the function failed: " + *out.FunctionError}
	case report:
		return esm.ReadStreamResponse(out.Payload, seqs)
	}
	return esm.Succeeded{}
}

// failStream follows a failed invocation of b, read from shard, the
// batch's attempts-th: once MaximumRetryAttempts + 1 invocations have
// failed, b is discarded with RetryAttemptsExhausted and response, the
// last invocation's (recorded, lambda-stream-sources.md 5.2); otherwise b
// is kept for its retry. A batch whose last retry fails is reported so
// even when its records have also passed their age (inferred, ESMS-09).
// The record names request, the last invocation's id.
func (p *pollers) failStream(ctx context.Context, c *kernel.Call, pl *poller, t target, kind esm.EventSourceKind, shard string, b esm.Batch[esm.StreamRecord], attempts int, request ids.LambdaRequestID, response esm.RetryAttemptsExhausted) pollNext {
	if limit, ok := streamSettingsOf(kind).Retries.(esm.Attempts); ok && attempts > int(limit.N) {
		p.discardStream(ctx, c, pl, t, kind, shard, []esm.FailedBatch{p.failedBatch(pl, t, shard, b.Records(), response, attempts, request)})
		return pollAgain{}
	}
	return p.retryStream(pl, kind, shard, b, attempts, request)
}

// retryStream keeps b, read from shard after attempts counted
// invocations, the last numbered request, for its retry after
// lambda.esm-stream.retry-delay: under poll-interval, kind's documented
// polling interval (lambda-stream-sources.md 4.1).
func (p *pollers) retryStream(pl *poller, kind esm.EventSourceKind, shard string, b esm.Batch[esm.StreamRecord], attempts int, request ids.LambdaRequestID) pollNext {
	at := p.x.k.Clock().Now()
	if p.x.svc.opts.Streams.RetryDelay == esm.RetryAfterPollInterval {
		at = at.Add(esm.PollInterval(kind))
	}
	pl.stream.retry = &streamRetry{shard: shard, batch: b, at: at, attempts: attempts, request: request}
	return pollAt{at: at}
}
