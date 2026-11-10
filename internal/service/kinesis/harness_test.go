package kinesis_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/kinesisapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/blob"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/service/kinesis"
	"github.com/sustinbebustin/understudy/internal/shardlog"
)

// account is the harness caller's account (kerneltest's Scope).
const account = "000000000000"

// harness is a kernel with the Kinesis service and typed shortcuts for its
// operations.
type harness struct {
	*kerneltest.K
	svc  *kinesis.Service
	opts kinesis.Options
}

func run(t *testing.T, o kinesis.Options, fn func(h *harness)) {
	t.Helper()
	kerneltest.Run(t, func(k *kerneltest.K) {
		blobs, err := blob.NewMemory(k.TempDir(), 0)
		if err != nil {
			k.Fatal(err)
		}
		d := k.Deps(kinesisapi.Service())
		d.Blob = blobs
		fn(&harness{K: k, svc: kinesis.New(d, o), opts: o})
	}, kerneltest.Services(kinesisapi.Service()))
}

// call runs op and fails the test on an error.
func call[In, Out any](h *harness, op kernel.Op[In, Out], in *In) *Out {
	h.Helper()
	out, err := h.Invoke(h.Context(), op, in)
	if err != nil {
		h.Fatalf("%s = %s (%d): %s", op.Descriptor().ID, err.ID.JSONType, err.ID.Status, err.Message)
	}
	return out
}

// fail runs op and fails the test when it succeeds.
func fail[In, Out any](h *harness, op kernel.Op[In, Out], in *In) *awserr.Error {
	h.Helper()
	out, err := h.Invoke(h.Context(), op, in)
	if err == nil {
		h.Fatalf("%s succeeded with %+v, want an error", op.Descriptor().ID, out)
	}
	return err
}

// wantErr checks an error's bare __type, status and message. Kinesis
// errors are the bare shape name, 400, body key message (kinesis.md 2.5).
func wantErr(tb testing.TB, what string, err *awserr.Error, typ string, status int, msg string) {
	tb.Helper()
	switch {
	case err == nil:
		tb.Errorf("%s: no error, want %s %d %q", what, typ, status, msg)
	case err.ID.JSONType != typ || err.ID.Status != status || err.Message != msg:
		tb.Errorf("%s: error = %s %d %q\nwant %s %d %q", what, err.ID.JSONType, err.ID.Status, err.Message, typ, status, msg)
	}
}

// invalidArg checks an InvalidArgumentException (400, kinesis.md 6).
func invalidArg(tb testing.TB, what string, err *awserr.Error, msg string) {
	tb.Helper()
	wantErr(tb, what, err, "InvalidArgumentException", 400, msg)
}

// notFound is the not-found text, which names the stream and account even
// when the caller sent an ARN (kinesis.md finding 5, third-party-recorded
// over CBOR; the JSON text inferred identical, KIN-05).
func notFound(stream, acct string) string {
	return "Stream " + stream + " under account " + acct + " not found."
}

// notImplemented is the 501 text of a setting slice 1 does not build.
func notImplemented(setting, op string) string {
	return "understudy does not implement " + setting + " on " + op + ". Run 'understudy coverage --service kinesis' for what is supported."
}

// wantNotImplemented checks a 501 NotImplemented with the notImplemented text.
func wantNotImplemented(tb testing.TB, what string, err *awserr.Error, setting, op string) {
	tb.Helper()
	wantErr(tb, what, err, "NotImplemented", 501, notImplemented(setting, op))
}

// arnOf is the stream ARN arn:aws:kinesis:<region>:<account>:stream/<name>
// (kinesis.md 3.1, aws-doc).
func arnOf(name string) string {
	return "arn:aws:kinesis:us-east-1:" + account + ":stream/" + name
}

// shardID is shardId- and 12 zero-padded digits, from 0 (kinesis.md 3.2).
func shardID(i int) string { return fmt.Sprintf("shardId-%012d", i) }

// create makes a PROVISIONED stream of n shards: ShardCount without a mode
// is PROVISIONED (kinesis.md 13.6 item 1, finding 4).
func (h *harness) create(name string, n int32) {
	h.Helper()
	call(h, kinesisapi.OpCreateStream, &kinesisapi.CreateStreamInput{StreamName: name, ShardCount: new(n)})
}

func (h *harness) summary(name string) kinesisapi.StreamDescriptionSummary {
	h.Helper()
	return call(h, kinesisapi.OpDescribeStreamSummary, &kinesisapi.DescribeStreamSummaryInput{StreamName: new(name)}).StreamDescriptionSummary
}

func (h *harness) describe(name string) kinesisapi.StreamDescription {
	h.Helper()
	return call(h, kinesisapi.OpDescribeStream, &kinesisapi.DescribeStreamInput{StreamName: new(name)}).StreamDescription
}

func (h *harness) put(name, key, data string) *kinesisapi.PutRecordOutput {
	h.Helper()
	return call(h, kinesisapi.OpPutRecord, &kinesisapi.PutRecordInput{StreamName: new(name), PartitionKey: new(key), Data: []byte(data)})
}

func (h *harness) iterator(name, shard string, typ kinesisapi.ShardIteratorType, seq string) string {
	h.Helper()
	in := &kinesisapi.GetShardIteratorInput{StreamName: new(name), ShardId: shard, ShardIteratorType: typ}
	if seq != "" {
		in.StartingSequenceNumber = new(seq)
	}
	out := call(h, kinesisapi.OpGetShardIterator, in)
	if out.ShardIterator == nil {
		h.Fatalf("GetShardIterator(%s %s) returned no ShardIterator", shard, typ)
	}
	return *out.ShardIterator
}

func (h *harness) records(it string, limit int32) *kinesisapi.GetRecordsOutput {
	h.Helper()
	in := &kinesisapi.GetRecordsInput{ShardIterator: it}
	if limit > 0 {
		in.Limit = new(limit)
	}
	return call(h, kinesisapi.OpGetRecords, in)
}

// readAll reads a shard from it to its tip, following NextShardIterator
// until a page is empty.
func (h *harness) readAll(it string) []kinesisapi.Record {
	h.Helper()
	var out []kinesisapi.Record
	for range 10000 {
		page := h.records(it, 0)
		out = append(out, page.Records...)
		if page.NextShardIterator == nil {
			h.Fatal("an open shard's page has no NextShardIterator (kinesis.md 5.5, aws-doc)")
		}
		if len(page.Records) == 0 {
			return out
		}
		it = *page.NextShardIterator
	}
	h.Fatal("readAll did not reach the tip")
	return nil
}

func (h *harness) advance(d time.Duration) {
	h.Helper()
	if _, err := h.Clock.Advance(d); err != nil {
		h.Fatal(err)
	}
}

// sequencer is the kinesis.sequence.layout switch's Sequencer, read from
// the harness options, never written out.
func (h *harness) sequencer() shardlog.Sequencer {
	if h.opts.SequenceLayout == kinesis.LayoutDecimal56 {
		return shardlog.DecimalCounter{Digits: 56}
	}
	return kinesis.V2Sequencer{}
}

func deref[T any](p *T) any {
	if p == nil {
		return nil
	}
	return *p
}

// str reads an optional string member as "" when it is absent.
func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
