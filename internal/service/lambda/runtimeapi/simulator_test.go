package runtimeapi_test

import (
	"context"
	"fmt"
	"io"
	"maps"
	"net/http"
	"strings"
	"testing"
)

// simulator is an in-process runtime interface client: it speaks the
// Runtime API to an environment exactly as a runtime in a container does.
type simulator struct {
	tb   testing.TB
	base string
	http *http.Client
}

func newSimulator(tb testing.TB, addr string, transport *http.Transport) *simulator {
	tb.Helper()
	client := &http.Client{Transport: transport}
	tb.Cleanup(client.CloseIdleConnections)
	return &simulator{tb: tb, base: "http://" + addr + "/2018-06-01/runtime", http: client}
}

// event is one invocation as the runtime received it from /next.
type event struct {
	status int
	header http.Header
	body   []byte
}

func (e event) requestID() string { return e.header.Get("Lambda-Runtime-Aws-Request-Id") }

// next blocks in GET /next, as every runtime does, until ctx ends.
func (s *simulator) next(ctx context.Context) (event, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.base+"/invocation/next", http.NoBody)
	if err != nil {
		return event{}, err
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return event{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	return event{status: resp.StatusCode, header: resp.Header, body: body}, err
}

// reply is the Runtime API's answer to a POST.
type reply struct {
	status int
	body   string
}

// post sends body with headers from the test's goroutine, reporting a
// transport failure on tb. With a trailer the body goes out chunked and the
// trailer after it, as aws-lambda-go sends responses.
func (s *simulator) post(ctx context.Context, path string, body io.Reader, header, trailer http.Header) reply {
	s.tb.Helper()
	r, err := s.send(ctx, path, body, header, trailer)
	if err != nil {
		s.tb.Error(err)
	}
	return r
}

// send is post without tb, safe on any goroutine.
func (s *simulator) send(ctx context.Context, path string, body io.Reader, header, trailer http.Header) (reply, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.base+path, body)
	if err != nil {
		return reply{}, fmt.Errorf("POST %s: %w", path, err)
	}
	maps.Copy(req.Header, header)
	if trailer != nil {
		req.Trailer = trailer
		req.ContentLength = -1
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return reply{}, fmt.Errorf("POST %s: %w", path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return reply{}, fmt.Errorf("POST %s: read reply: %w", path, err)
	}
	return reply{status: resp.StatusCode, body: string(raw)}, nil
}

func (s *simulator) respond(ctx context.Context, id, payload string) reply {
	return s.post(ctx, "/invocation/"+id+"/response", strings.NewReader(payload), nil, nil)
}

func (s *simulator) fail(ctx context.Context, id, errorType, payload string) reply {
	h := http.Header{}
	if errorType != "" {
		h.Set("Lambda-Runtime-Function-Error-Type", errorType)
	}
	return s.post(ctx, "/invocation/"+id+"/error", strings.NewReader(payload), h, nil)
}

func (s *simulator) initFail(ctx context.Context, errorType, payload string) reply {
	h := http.Header{"Lambda-Runtime-Function-Error-Type": {errorType}}
	return s.post(ctx, "/init/error", strings.NewReader(payload), h, nil)
}

// served is what serveOnce's goroutine saw: the event it answered, or why
// it could not take or answer one.
type served struct {
	ev  event
	err error
}

// serveOnce takes one invocation on its own goroutine and answers it with
// handle's payload. The channel yields once the reply is acknowledged or
// failed. The goroutine never touches tb, so it may outlive the test until
// ctx ends.
func (s *simulator) serveOnce(ctx context.Context, handle func(event) string) <-chan served {
	got := make(chan served, 1)
	go func() {
		ev, err := s.next(ctx)
		if err != nil {
			got <- served{err: fmt.Errorf("GET /next: %w", err)}
			return
		}
		r, err := s.send(ctx, "/invocation/"+ev.requestID()+"/response", strings.NewReader(handle(ev)), nil, nil)
		if err == nil && r.status != http.StatusAccepted {
			err = fmt.Errorf("POST /response = %d %s, want 202", r.status, r.body)
		}
		got <- served{ev: ev, err: err}
	}()
	return got
}

// fataler is the part of testing.TB and *rapid.T that receive needs.
type fataler interface {
	Helper()
	Fatal(args ...any)
}

// receive waits for serveOnce's result and fails tb if it could not serve.
func receive(tb fataler, got <-chan served) event {
	tb.Helper()
	s := <-got
	if s.err != nil {
		tb.Fatal(s.err)
	}
	return s.ev
}
