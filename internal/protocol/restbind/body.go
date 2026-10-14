package restbind

import (
	"errors"
	"io"
	"sync/atomic"

	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
)

// Body is the request body restbind hands a @streaming payload member: the
// edge's body with its end of stream observed, so a trailer-bound member
// (S3 x-amz-checksum-* sent as an aws-chunked trailer) resolves only once
// the handler has read the body through. Until then its value is pending,
// never "absent": the trailer arrives after the last byte.
//
// Deferred tells pending from absent, but handlers see only smithy.Body,
// whose Trailer answers both the same way, so they cannot tell them apart.
type Body struct {
	inner smithy.Body
	eof   atomic.Bool
}

// Open opens the underlying body; reading it to io.EOF resolves the
// trailers.
func (b *Body) Open() (io.ReadCloser, error) {
	rc, err := b.inner.Open()
	if err != nil {
		return nil, err
	}
	return &eofReader{rc: rc, eof: &b.eof}, nil
}

// Len returns the declared length, when the request states one.
func (b *Body) Len() (int64, bool) { return b.inner.Len() }

// Trailer implements smithy.Body: false until the body has reached EOF,
// then the trailer's value and whether the client sent it.
func (b *Body) Trailer(name string) (string, bool) {
	if !b.eof.Load() {
		return "", false
	}
	return b.inner.Trailer(name)
}

// Deferred returns a trailer-bound member's value: *PendingError while the
// body has not been read to EOF, then the trailer's value and whether the
// client sent it.
func (b *Body) Deferred(name string) (value string, sent bool, err error) {
	if !b.eof.Load() {
		return "", false, &PendingError{Name: name}
	}
	v, ok := b.inner.Trailer(name)
	return v, ok, nil
}

// PendingError is a trailer-bound member read before the body that carries
// it reached EOF: its value is not yet available.
type PendingError struct{ Name string }

func (e *PendingError) Error() string {
	return "restbind: trailer " + e.Name + " is not yet available; read the request body to EOF first"
}

// eofReader records when the underlying reader reports io.EOF.
type eofReader struct {
	rc  io.ReadCloser
	eof *atomic.Bool
}

func (r *eofReader) Read(p []byte) (int, error) {
	n, err := r.rc.Read(p)
	if errors.Is(err, io.EOF) {
		r.eof.Store(true)
	}
	return n, err
}

func (r *eofReader) Close() error { return r.rc.Close() }
