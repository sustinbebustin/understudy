package s3

import (
	"io"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/kernel"
)

// closeCounter is a reader that counts its Close calls.
type closeCounter struct {
	io.Reader
	closed int
}

func (c *closeCounter) Close() error {
	c.closed++
	return nil
}

// A GetObject output dropped before encoding closes its opened blob reader
// through kernel.Release; one the encoder took is the encoder's to close.
func TestReleaseClosesAGetObjectBodyNobodyTook(t *testing.T) {
	untaken := &closeCounter{Reader: strings.NewReader("abc")}
	out := &s3api.GetObjectOutput{Body: &openedBody{rc: untaken, n: 3}}
	if err := kernel.Release(out); err != nil {
		t.Fatalf("Release = %v", err)
	}
	if err := kernel.Release(out); err != nil {
		t.Fatalf("second Release = %v", err)
	}
	if untaken.closed != 1 {
		t.Errorf("untaken reader closed %d times, want 1", untaken.closed)
	}

	taken := &closeCounter{Reader: strings.NewReader("abc")}
	body := &openedBody{rc: taken, n: 3}
	if _, err := body.Range(0, 3); err != nil {
		t.Fatalf("Range = %v", err)
	}
	if err := kernel.Release(&s3api.GetObjectOutput{Body: body}); err != nil {
		t.Fatalf("Release after Range = %v", err)
	}
	if taken.closed != 0 {
		t.Errorf("reader handed to the encoder closed %d times by Release, want 0", taken.closed)
	}
}
