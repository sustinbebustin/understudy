package restbind

import (
	"errors"
	"io"
	"sync/atomic"

	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
)

// Body is the request body restbind hands a @streaming payload member: the
// edge's body with its end of stream observed. Its Trailer answers nothing
// until the body has been read to EOF, because a trailer (S3 x-amz-checksum-*
// sent as an aws-chunked trailer) arrives after the last byte; after EOF it
// answers the trailer's value and whether the client sent it.
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
