package blob

import (
	"bytes"
	"errors"
	"io"
	"slices"
)

const (
	firstChunk = 4 << 10
	maxChunk   = 1 << 20
)

// spool reads r into chunks until EOF or until more than limit bytes have
// arrived. It reports complete when r ended within limit; otherwise the
// chunks hold the first bytes and the caller streams the rest elsewhere.
// Chunks are never copied while reading and the last chunk is cut to the
// limit, so a body that turns out to be large costs at most limit+1 bytes.
//
// A non-negative hint sizes the first chunk as hint+1, capped at limit+1, so an honest body is
// read into one chunk that join returns without copying; a dishonest hint
// costs no more than the limit+1 bound above. With no hint, chunks start at
// firstChunk and grow geometrically up to maxChunk, and join copies them into
// one slice, so a body of unknown length briefly holds up to twice its size.
func spool(r io.Reader, limit, hint int64) (chunks [][]byte, n int64, complete bool, err error) {
	size := int64(firstChunk)
	if hint >= 0 {
		size = min(hint, limit) + 1
	}
	for {
		c := make([]byte, min(size, limit+1-n))
		k, err := fill(r, c)
		chunks = append(chunks, c[:k])
		n += int64(k)
		// The limit is checked before EOF: a reader may return its last bytes
		// together with io.EOF, so the read that crosses the limit can also
		// be the one that ends the body. Such a body still goes to disk, and
		// the caller's next read of r sees EOF again.
		eof := errors.Is(err, io.EOF)
		switch {
		case err != nil && !eof:
			return nil, 0, false, err
		case n > limit:
			return chunks, n, false, nil
		case eof:
			return chunks, n, true, nil
		}
		size = min(2*size, maxChunk)
	}
}

// fill reads from r until c is full or r returns an error. Unlike
// io.ReadFull it returns r's error unchanged, so only io.EOF from r itself
// means the body ended; an io.ErrUnexpectedEOF from r (a net/http body cut
// short of its Content-Length) stays a failure.
func fill(r io.Reader, c []byte) (int, error) {
	k := 0
	for k < len(c) {
		m, err := r.Read(c[k:])
		k += m
		if err != nil {
			return k, err
		}
	}
	return k, nil
}

// join returns the spooled bytes as one slice of exactly their length.
func join(chunks [][]byte) []byte {
	if len(chunks) == 1 && cap(chunks[0])-len(chunks[0]) <= 1 {
		return slices.Clip(chunks[0])
	}
	return bytes.Join(chunks, nil)
}
