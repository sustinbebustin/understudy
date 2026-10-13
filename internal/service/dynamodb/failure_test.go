package dynamodb_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/dynamodbapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/service/dynamodb"
)

// internalServerError is DynamoDB's own 500, which every SDK retries.
const internalServerError = "com.amazonaws.dynamodb.v20120810#InternalServerError"

// TestStartOutlastingTheWaitIsRetryable: a first call during a long pull
// waits a bounded time, then answers a retryable InternalServerError that
// says the start goes on (ARCHITECTURE.md 15).
func TestStartOutlastingTheWaitIsRetryable(t *testing.T) {
	runWith(t, &fakeLocal{startBlocks: true}, func(h *harness) {
		began := time.Now()
		got := h.call("ListTables", `{}`)
		waited := time.Since(began)
		e := parseError(h, got.Body)
		want := "DynamoDB Local is still being pulled or started after 30s; the start goes on in the background, so retry the call. " +
			"The first start pulls the image, which can take minutes on a slow network; the server log records when DynamoDB Local is ready."
		if got.Status != http.StatusInternalServerError || e.Type != internalServerError || e.Message != want {
			h.Errorf("ListTables = %d %s\nwant 500 InternalServerError %q", got.Status, got.Body, want)
		}
		if waited != 30*time.Second {
			h.Errorf("the call waited %s, want the 30s bound", waited)
		}
	})
}

// TestLocalThatCannotBeReachedAnswersWhy: a failed round trip, and a Local
// that stopped after it started, are InternalServerError naming the cause
// and the next step.
func TestLocalThatCannotBeReachedAnswersWhy(t *testing.T) {
	tests := []struct {
		name  string
		local *fakeLocal
		want  string
	}{
		{
			name: "transport error",
			local: &fakeLocal{fail: func(*http.Request) (*http.Response, error) {
				return nil, errors.New("read tcp 172.17.0.1:50000->172.17.0.2:8000: connection reset by peer")
			}},
			want: "understudy could not reach DynamoDB Local at " + fakeEndpoint.String() +
				": read tcp 172.17.0.1:50000->172.17.0.2:8000: connection reset by peer. Other services are unaffected; check `understudy doctor` and the server log, then retry.",
		},
		{
			name:  "stopped",
			local: &fakeLocal{stopped: true},
			want: "understudy could not reach DynamoDB Local: it stopped while the call was being forwarded. " +
				"Other services are unaffected; check `understudy doctor` and the server log, then retry.",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runWith(t, tt.local, func(h *harness) {
				got := h.call("ListTables", `{}`)
				if e := parseError(h, got.Body); got.Status != http.StatusInternalServerError || e.Type != internalServerError || e.Message != tt.want {
					h.Errorf("ListTables = %d %s\nwant 500 InternalServerError %q", got.Status, got.Body, tt.want)
				}
			})
		})
	}
}

// TestFailedRoundTripReportsTheLocalLost: a round trip that does not reach
// Local tells it so, so a container that died is replaced by the next
// call's Start.
func TestFailedRoundTripReportsTheLocalLost(t *testing.T) {
	local := &fakeLocal{fail: func(*http.Request) (*http.Response, error) {
		return nil, errors.New("dial tcp 172.17.0.2:8000: connect: connection refused")
	}}
	runWith(t, local, func(h *harness) {
		h.call("ListTables", `{}`)
		local.mu.Lock()
		defer local.mu.Unlock()
		if len(local.lost) != 1 || local.lost[0] != fakeEndpoint.String() {
			h.Errorf("Lost calls = %v, want one for %s", local.lost, fakeEndpoint.String())
		}
	})
}

// TestClientLeavingMidForwardIsAborted: a client that goes away while
// Local works is recorded as RequestAborted, not as Local's failure.
func TestClientLeavingMidForwardIsAborted(t *testing.T) {
	local := &fakeLocal{fail: func(r *http.Request) (*http.Response, error) {
		<-r.Context().Done()
		return nil, r.Context().Err()
	}}
	runWith(t, local, func(h *harness) {
		ctx, cancel := context.WithCancel(h.Context())
		type result struct {
			resp kernel.RawResponse
			err  *awserr.Error
		}
		done := make(chan result, 1)
		go func() {
			resp, err := dispatch(ctx, h, h.Call(dynamodbapi.OpListTablesRaw.Descriptor()), &dynamodbapi.ListTablesInput{}, kernel.Synthesized{})
			done <- result{resp, err}
		}()
		synctest.Wait()
		cancel()
		r := <-done
		if r.err == nil || r.err.ID != awserr.RequestAbortedException() || r.err.Message != awserr.RequestAborted().Message {
			h.Errorf("ListTables after the client left = %+v, %+v; want RequestAborted", r.resp, r.err)
		}
	})
}

// internalCause returns the cause of an InternalFailure, failing the test
// for any other error.
func internalCause(h *harness, err *awserr.Error) error {
	h.Helper()
	if err == nil {
		h.Fatal("the call succeeded, want InternalFailure")
		return nil
	}
	in, ok := err.Origin.(awserr.Internal)
	if err.ID != awserr.InternalFailure() || !ok {
		h.Fatalf("error %s %q (origin %T), want InternalFailure", err.ID.JSONType, err.Message, err.Origin)
	}
	return in.Cause
}

// TestAnswersUnderstudyCannotPassOnAreInternal: an answer larger than any
// DynamoDB answer, and a rewritten answer whose strings are not UTF-8, are
// understudy's InternalFailure carrying the typed cause.
func TestAnswersUnderstudyCannotPassOnAreInternal(t *testing.T) {
	t.Run("too large", func(t *testing.T) {
		local := &fakeLocal{fail: func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(zeros{})}, nil
		}}
		runWith(t, local, func(h *harness) {
			_, err := synthesize(h, dynamodbapi.OpGetItemRaw, `{"TableName":"Locks","Key":{"LockID":{"S":"a"}}}`)
			cause := internalCause(h, err)
			if big, ok := errors.AsType[*dynamodb.AnswerTooLargeError](cause); !ok || big.Op != dynamodbapi.OpGetItemRaw.Descriptor().ID || big.Limit != 64<<20 {
				h.Errorf("cause = %v, want *AnswerTooLargeError for GetItem at 64 MiB", cause)
			}
		})
	})
	t.Run("invalid UTF-8", func(t *testing.T) {
		local := &fakeLocal{answer: func(string, string) reply {
			return reply{Status: http.StatusOK, Body: "{\"Table\":{\"TableName\":\"Locks\xff\",\"TableArn\":\"arn:aws:dynamodb:ddblocal:000000000000:table/Locks\"}}"}
		}}
		runWith(t, local, func(h *harness) {
			_, err := synthesize(h, dynamodbapi.OpDescribeTableRaw, `{"TableName":"Locks"}`)
			cause := internalCause(h, err)
			if bad, ok := errors.AsType[*dynamodb.InvalidUTF8Error](cause); !ok || bad.Value != "Locks\xff" {
				h.Errorf("cause = %v, want *InvalidUTF8Error naming the string", cause)
			}
		})
	})
}

// TestCallWithoutScopeIsInternal: a call without the account or region
// Local partitions by never reaches Local.
func TestCallWithoutScopeIsInternal(t *testing.T) {
	run(t, func(h *harness) {
		op := dynamodbapi.OpListTablesRaw.Descriptor()
		c := h.Kernel.NewCall(kernel.ExternalCall{
			RequestID: h.Kernel.IDs().RequestID(),
			Operation: op.ID,
			Scope:     aws.Scope{Partition: aws.PartitionAWS()},
			Principal: identity.Root{Account: h.Scope.Account},
			Origin:    ref.External{Protocol: ref.AWSJSON10, Listener: ref.ListenerMain},
			Admitted:  h.Clock.Now(),
			Endpoint:  h.Endpoint,
		})
		_, err := dispatch(h.Context(), h, c, &dynamodbapi.ListTablesInput{}, kernel.Synthesized{})
		cause := internalCause(h, err)
		if se, ok := errors.AsType[*dynamodb.ScopeError](cause); !ok || se.Op != op.ID {
			h.Errorf("cause = %v, want *ScopeError for %s", cause, op.ID)
		}
		if seen := h.local.requests(); len(seen) != 0 {
			h.Errorf("Local received %d requests", len(seen))
		}
	})
}

// zeros is an endless body.
type zeros struct{}

func (zeros) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}
