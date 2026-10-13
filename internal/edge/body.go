package edge

import (
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// requestBody is the edge's view of a request body (smithy.Body): opened
// once, read through a counting reader that fails past the operation's cap
// instead of buffering, with trailers readable after EOF.
type requestBody struct {
	r     *http.Request
	limit int64
	op    ref.OperationID

	once sync.Once
}

// BodyOpenedError is a body opened twice; the first reader consumed it.
type BodyOpenedError struct{}

func (*BodyOpenedError) Error() string {
	return "edge: the request body was opened twice; a codec or handler reads it once"
}

// Open returns the body, capped. net/http sends 100 Continue on the first
// read, so a handler that answers before reading never pulls the body.
func (b *requestBody) Open() (io.ReadCloser, error) {
	first := false
	b.once.Do(func() { first = true })
	if !first {
		return nil, &BodyOpenedError{}
	}
	if b.r.Body == nil {
		return io.NopCloser(eofReader{}), nil
	}
	return &cappedReader{rc: b.r.Body, limit: b.limit, op: b.op}, nil
}

// Len returns the declared length, when the request states one.
func (b *requestBody) Len() (int64, bool) {
	return b.r.ContentLength, b.r.ContentLength >= 0
}

// Trailer returns a trailer the client sent after the body; valid after EOF.
func (b *requestBody) Trailer(name string) (string, bool) {
	v := b.r.Trailer.Values(name)
	if len(v) == 0 {
		return "", false
	}
	return v[0], true
}

// bodyIdleTimeout bounds how long a client may pause in the middle of a
// request body.
const bodyIdleTimeout = time.Minute

// progressBody is a request body read under a deadline renewed on every
// read: a client that stops sending mid-body (a slow-body attack, or a
// client that hung) frees its handler, spool file and connection after
// bodyIdleTimeout, while an upload that keeps arriving is never cut. Each
// read that ends in EOF or another error lifts the deadline: net/http
// starts a background read of the connection at EOF, and a read after
// EOF, as a decoder checking for trailing bytes makes, would otherwise
// leave the deadline on it, which cancels the request when it passes
// during a long poll. A writer without deadline support, such as a test
// recorder, leaves the body unbounded.
type progressBody struct {
	rc   io.ReadCloser
	ctrl *http.ResponseController
	wall func() time.Time
}

func (b *progressBody) Read(p []byte) (int, error) {
	b.setDeadline(b.wall().Add(bodyIdleTimeout))
	n, err := b.rc.Read(p)
	if err != nil {
		b.setDeadline(time.Time{})
	}
	return n, err
}

func (b *progressBody) Close() error { return b.rc.Close() }

// setDeadline sets the connection's read deadline. A writer without
// deadline support, or a connection already gone, stops it trying again:
// the body then reads unbounded, and a gone connection fails the read
// itself.
func (b *progressBody) setDeadline(t time.Time) {
	if b.ctrl == nil {
		return
	}
	if err := b.ctrl.SetReadDeadline(t); err != nil {
		b.ctrl = nil
	}
}

type eofReader struct{}

func (eofReader) Read([]byte) (int, error) { return 0, io.EOF }

// cappedReader counts what it reads and fails once the total passes limit,
// with the RequestEntityTooLarge error the codecs pass on.
type cappedReader struct {
	rc    io.ReadCloser
	limit int64
	n     int64
	op    ref.OperationID
}

func (c *cappedReader) Read(p []byte) (int, error) {
	if c.n > c.limit {
		return 0, tooLarge(c.op, c.limit).AsError()
	}
	n, err := c.rc.Read(p)
	c.n += int64(n)
	if c.n > c.limit {
		return n, tooLarge(c.op, c.limit).AsError()
	}
	return n, err
}

func (c *cappedReader) Close() error { return c.rc.Close() }

// tooLarge is the answer for a body past op's cap.
func tooLarge(op ref.OperationID, limit int64) *awserr.Error {
	return &awserr.Error{
		ID:      awserr.RequestEntityTooLarge(),
		Message: fmt.Sprintf("The request body exceeds the %d-byte limit of %s; send a smaller body.", limit, op),
		Origin:  awserr.Generic{},
	}
}
