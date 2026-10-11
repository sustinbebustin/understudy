package logs_test

import (
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/logsapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/service/logs"
)

// harness is a kernel with the Logs service and typed shortcuts for its
// operations.
type harness struct {
	*kerneltest.K
	svc *logs.Service
}

func run(t *testing.T, fn func(h *harness), opts ...kerneltest.Option) {
	t.Helper()
	kerneltest.Run(t, func(k *kerneltest.K) {
		svc := logs.New(k.Deps(logsapi.Service()), logs.Options{})
		fn(&harness{K: k, svc: svc})
	}, append(opts, kerneltest.Services(logsapi.Service()))...)
}

// call runs op as a client request.
func call[In, Out any](h *harness, op kernel.Op[In, Out], in *In) (*Out, *awserr.Error) {
	return h.Invoke(h.Context(), op, in)
}

// must runs op and fails the test on an error.
func must[In, Out any](h *harness, op kernel.Op[In, Out], in *In) *Out {
	h.Helper()
	out, err := call(h, op, in)
	if err != nil {
		h.Fatalf("%s = %s: %s", op.Descriptor().ID.Name, err.ID.JSONType, err.Message)
	}
	return out
}

func (h *harness) group(name string) {
	h.Helper()
	must(h, logsapi.OpCreateLogGroup, &logsapi.CreateLogGroupInput{LogGroupName: name})
}

func (h *harness) stream(group, name string) {
	h.Helper()
	must(h, logsapi.OpCreateLogStream, &logsapi.CreateLogStreamInput{LogGroupName: group, LogStreamName: name})
}

func (h *harness) put(group, stream string, events ...logsapi.InputLogEvent) *logsapi.PutLogEventsOutput {
	h.Helper()
	return must(h, logsapi.OpPutLogEvents, &logsapi.PutLogEventsInput{LogGroupName: group, LogStreamName: stream, LogEvents: events})
}

// now is the emulator clock in milliseconds, the unit of event timestamps.
func (h *harness) now() int64 { return h.Clock.Now().Wire().UnixMilli() }

func (h *harness) advance(d time.Duration) {
	h.Helper()
	if _, err := h.Clock.Advance(d); err != nil {
		h.Fatal(err)
	}
}

func event(ts int64, msg string) logsapi.InputLogEvent {
	return logsapi.InputLogEvent{Timestamp: ts, Message: msg}
}

// messages returns the messages of a GetLogEvents page.
func messages(events []logsapi.OutputLogEvent) []string {
	out := make([]string, len(events))
	for i, e := range events {
		out[i] = val(e.Message)
	}
	return out
}

// val reads an optional output member as "<nil>" when it is absent.
func val(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

func wantError(t *testing.T, err *awserr.Error, jsonType, message string) {
	t.Helper()
	switch {
	case err == nil:
		t.Fatalf("no error, want %s %q", jsonType, message)
	case err.ID.JSONType != jsonType || err.Message != message:
		t.Fatalf("error = %s %q\nwant  %s %q", err.ID.JSONType, err.Message, jsonType, message)
	}
}
