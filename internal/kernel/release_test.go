package kernel_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/fault"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// heldBody is a streaming output body that holds a resource until the
// encoder takes it, as S3's opened GetObject body holds a blob file.
type heldBody struct {
	text   string
	closed int
	err    error
}

func (b *heldBody) Size() int64 { return int64(len(b.text)) }

func (b *heldBody) Range(int64, int64) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader(b.text)), nil
}

func (b *heldBody) Close() error {
	b.closed++
	return b.err
}

// streamed is an output with a streaming body and a required timestamp.
type streamed struct {
	At   time.Time
	Body smithy.BodySource
}

func (s *streamed) Serialize(z smithy.Serializer) {
	z.WriteStruct(nil, &smithy.Schema{ID: "test#Streamed", Type: smithy.TypeStructure})
	z.WriteTimestamp(&smithy.Member{Name: "CreatedAt", Required: true}, s.At)
	z.WriteBody(&smithy.Member{Name: "Body"}, s.Body)
	z.CloseStruct()
}

// streamedOp stubs GetQueueUrl to answer a streamed output over body.
func streamedOp(k *kerneltest.K, at time.Time, body *heldBody) kernel.Op[sqsapi.GetQueueUrlInput, streamed] {
	op := sqsOp[sqsapi.GetQueueUrlInput, streamed](k.T, "GetQueueUrl")
	k.Stub(op, func(context.Context, *kernel.Call, *sqsapi.GetQueueUrlInput) (*streamed, *awserr.Error) {
		return &streamed{At: at, Body: body}, nil
	})
	return op
}

// An after-commit fault answers the call with an error after the handler
// opened its body; the kernel closes the body nobody will stream.
func TestAnAfterCommitFaultClosesTheDroppedBody(t *testing.T) {
	run(t, func(k *kerneltest.K) {
		t := k.T
		if _, err := k.Faults.Add(fault.Rule{
			Match:   fault.Match{Operations: []ref.OperationID{sqsapi.OpGetQueueUrl.Descriptor().ID}},
			Phase:   fault.Request,
			Action:  fault.AfterCommit{Shape: "InternalFailure"},
			Trigger: fault.Always{},
		}); err != nil {
			t.Fatal(err)
		}
		body := &heldBody{text: "payload"}
		op := streamedOp(k, k.Clock.Now().Wire(), body)
		if _, err := k.Invoke(t.Context(), op, &sqsapi.GetQueueUrlInput{QueueName: "q"}); err == nil {
			t.Fatal("the after-commit fault did not answer the call")
		}
		if body.closed != 1 {
			t.Errorf("the dropped body closed %d times, want 1", body.closed)
		}
	})
}

// A strict refusal drops the output too, and closes its body.
func TestAStrictRefusalClosesTheDroppedBody(t *testing.T) {
	run(t, func(k *kerneltest.K) {
		t := k.T
		body := &heldBody{text: "payload"}
		op := streamedOp(k, time.Time{}, body)
		_, err := k.Invoke(t.Context(), op, &sqsapi.GetQueueUrlInput{QueueName: "q"})
		if !isA[*kernel.ZeroTimestampError](internalCause(t, err)) {
			t.Fatalf("err = %v, want a ZeroTimestampError", err)
		}
		if body.closed != 1 {
			t.Errorf("the dropped body closed %d times, want 1", body.closed)
		}
	})
}

// An answered output keeps its body open: the encoder or the internal
// caller streams it and closes what it takes.
func TestAnAnsweredOutputKeepsItsBodyOpen(t *testing.T) {
	run(t, func(k *kerneltest.K) {
		t := k.T
		body := &heldBody{text: "payload"}
		op := streamedOp(k, k.Clock.Now().Wire(), body)
		out, err := k.Invoke(t.Context(), op, &sqsapi.GetQueueUrlInput{QueueName: "q"})
		if err != nil {
			t.Fatal(err.AsError())
		}
		if out.Body != body || body.closed != 0 {
			t.Errorf("answered body closed %d times (same body: %t), want the open body handed over", body.closed, out.Body == body)
		}
	})
}

func TestReleaseClosesEveryClosableBodyAndReportsFailures(t *testing.T) {
	refused := errors.New("close refused")
	body := &heldBody{err: refused}
	if err := kernel.Release(&streamed{Body: body}); !errors.Is(err, refused) || body.closed != 1 {
		t.Errorf("Release = %v after %d closes, want the close error after 1", err, body.closed)
	}
	plain := smithyText("no resource")
	if err := kernel.Release(&streamed{Body: plain}); err != nil {
		t.Errorf("Release of a body holding nothing = %v, want nil", err)
	}
	if err := kernel.Release(&streamed{}); err != nil {
		t.Errorf("Release of an output without a body = %v, want nil", err)
	}
	if err := kernel.Release(kernel.RawWritten{}); err != nil {
		t.Errorf("Release of a raw answer = %v, want nil", err)
	}
}

// smithyText is a body that holds no resource and has no Close.
type smithyText string

func (s smithyText) Size() int64 { return int64(len(s)) }

func (s smithyText) Range(int64, int64) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader(string(s))), nil
}
