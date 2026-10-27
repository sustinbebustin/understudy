package lambda

import (
	"context"
	"log/slog"

	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/service/lambda/esm"
	"github.com/sustinbebustin/understudy/internal/shardlog"
)

// A stream mapping's reader (lambda-stream-sources.md 12.5): the poller of
// a mapping on a DynamoDB stream. It shares the SQS poller's lifecycle
// (esm_runtime.go): an idle reader waits on shardlog.WaitKey of its stream
// and steps when an append wakes it, so a quiet stream never holds a
// settle (spec conflict row 2). A step reads the stream from the mapping's
// checkpoint through an esm.StreamSource as the execution role, batches
// what one read returns up to BatchSize and 6 MB, invokes the function
// synchronously, and on success moves the checkpoint past the batch. A
// failed invocation retries the same batch after
// lambda.esm-stream.retry-delay (inferred, ESMS-06).
//
// Not built yet, and so not offered by the control plane: the batching
// window, a retry limit, record age, partial batch responses and
// on-failure destinations; one reader reads the stream's shards in turn.

// streamHeld is what a stream reader keeps between its steps; only the
// running step touches it.
type streamHeld struct {
	// stream is the mapping's source.
	stream aws.ARN
	// shards are the stream's shard ids, read at the first step.
	shards []string
	// retry is the failed batch waiting for its retry; nil when none is.
	retry *streamRetry
}

// streamRetry is a failed batch and when it is retried.
type streamRetry struct {
	shard string
	batch esm.Batch[esm.StreamRecord]
	at    clock.Instant
}

// newStreamPoller is the poller of mapping uuid, whose source is a
// DynamoDB stream; false when the stream ARN is not one this emulator can
// address.
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
	}
	kind, ok := m.SourceKind.(esm.DynamoDBStream)
	if !ok {
		return pollStop{}
	}
	t, err := p.x.resolve(functionRef{scope: pl.scope, name: m.Function, qualifier: m.Qualifier})
	if err != nil {
		p.unavailable(ctx, c, pl, err)
		return pollAt{at: p.x.k.Clock().Now().Add(pl.held.backoff())}
	}
	p.available(ctx, c, pl)
	target, ok := streamTarget(sh.stream, t.role)
	if !ok {
		return pollStop{}
	}
	if r := sh.retry; r != nil {
		if now := p.x.k.Clock().Now(); now.Before(r.at) {
			return pollAt{at: r.at}
		}
		sh.retry = nil
		return p.invokeStream(ctx, c, pl, t, r.shard, r.batch)
	}
	src := dynamoSource{k: p.x.k, parent: c, target: target, stream: sh.stream}
	shard, b, rerr := p.readBatch(ctx, pl, src, int(m.BatchSize), kind.Position)
	switch {
	case rerr != nil:
		return p.readFailed(ctx, pl, rerr)
	case b.Len() == 0:
		return pollIdle{}
	}
	return p.invokeStream(ctx, c, pl, t, shard, b)
}

// readBatch reads the first shard of pl's stream that has records past
// its checkpoint, up to limit of them, and batches what fits in 6 MB; an
// empty batch when no shard has any. A read that succeeds ends the
// backoff of earlier failures.
func (p *pollers) readBatch(ctx context.Context, pl *poller, src esm.StreamSource, limit int, start esm.StreamStart) (string, esm.Batch[esm.StreamRecord], error) {
	sh := pl.stream
	var b esm.Batch[esm.StreamRecord]
	if sh.shards == nil {
		shards, err := src.Shards(ctx)
		if err != nil {
			return "", b, err
		}
		sh.shards = shards
	}
	for _, shard := range sh.shards {
		page, err := src.Read(ctx, shard, p.checkpoint(pl, shard, start), limit)
		if err != nil {
			return "", b, err
		}
		pl.held.retry = 0
		for _, r := range page.Records {
			added, err := b.Add(r)
			if err != nil {
				return "", esm.Batch[esm.StreamRecord]{}, err
			}
			if !added {
				break
			}
		}
		if b.Len() > 0 {
			return shard, b, nil
		}
	}
	return "", b, nil
}

// readFailed logs a read of pl's stream that failed and backs off. Not
// built yet: a stream that is gone or trimmed is retried like any other
// failure.
func (p *pollers) readFailed(ctx context.Context, pl *poller, err error) pollNext {
	p.x.k.Log().LogAttrs(ctx, slog.LevelWarn, "an event source mapping could not read its stream; it tries again, backing off from a second to five minutes of emulator time, or at once when the stream receives a record",
		pl.attrs(slog.String("error", err.Error()))...)
	return pollAt{at: p.x.k.Clock().Now().Add(pl.held.backoff())}
}

// checkpoint is where pl reads shard from: its checkpoint, or the
// mapping's starting position before its first batch succeeded.
func (p *pollers) checkpoint(pl *poller, shard string, start esm.StreamStart) shardlog.Position {
	pos, _ := view(p.x.svc, pl.scope, func(st *State) (shardlog.Position, *awserr.Error) {
		return st.Checkpoints[pl.uuid][shard], nil
	})
	if pos != nil {
		return pos
	}
	switch start.(type) {
	case esm.Latest:
		return shardlog.Latest{}
	case esm.TrimHorizon:
	}
	return shardlog.TrimHorizon{}
}

// invokeStream runs the function synchronously on b, read from shard, as
// Lambda on its own behalf (as for SQS, esm_batch.go). A success moves the
// checkpoint past b and marks the mapping processed; a failure keeps b for
// its retry after lambda.esm-stream.retry-delay, timed from the
// invocation's end.
func (p *pollers) invokeStream(ctx context.Context, c *kernel.Call, pl *poller, t target, shard string, b esm.Batch[esm.StreamRecord]) pollNext {
	fn := kernel.Target{Scope: pl.scope, As: kernel.AsPlatform{Name: lambdaService}, Via: ref.LambdaESM}
	out, err := kernel.Invoke(ctx, p.x.k, c, lambdaapi.OpInvoke, fn, &lambdaapi.InvokeInput{
		FunctionName: t.invoked.String(), InvocationType: new(lambdaapi.InvocationTypeRequestResponse), Payload: b.Event(),
	})
	if f, failed := verdict(out, err, false, nil).(esm.Failed); failed {
		p.x.k.Log().LogAttrs(ctx, slog.LevelInfo, "an event source mapping's stream batch failed; it is retried with the same records",
			pl.attrs(slog.Int("records", b.Len()), slog.String("reason", f.Reason))...)
		at := p.x.k.Clock().Now()
		if p.x.svc.opts.Streams.RetryDelay == esm.RetryAfterPollInterval {
			at = at.Add(esm.DynamoDBRetryDelay)
		}
		pl.stream.retry = &streamRetry{shard: shard, batch: b, at: at}
		return pollAt{at: at}
	}
	records := b.Records()
	next := shardlog.After{Seq: records[len(records)-1].Seq}
	if _, werr := write(p.x.svc, c, pl.scope, func(st *State, _ clock.Instant, tx *kernel.Tx) (struct{}, *awserr.Error) {
		m, ok := st.Mappings[pl.uuid]
		if !ok {
			tx.Unchanged()
			return struct{}{}, nil
		}
		if st.Checkpoints == nil {
			st.Checkpoints = map[string]map[string]shardlog.Position{}
		}
		if st.Checkpoints[pl.uuid] == nil {
			st.Checkpoints[pl.uuid] = map[string]shardlog.Position{}
		}
		st.Checkpoints[pl.uuid][shard] = next
		m.Processed = true
		st.Mappings[pl.uuid] = m
		return struct{}{}, nil
	}); werr != nil {
		p.x.k.Log().LogAttrs(ctx, slog.LevelError, "an event source mapping could not save its stream checkpoint; the batch may be delivered again",
			pl.attrs(slog.String("error", werr.Message))...)
	}
	return pollAgain{}
}
