package s3

import (
	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/kernel"
)

// bindObjects binds the object operations. This build binds none, so each
// answers 501 NotImplemented in S3's error format and coverage lists it as
// unbound.
func (*Service) bindObjects(s3api.Binder) {}

// Objects is a bucket's object keys and their versions. No operation of
// this build writes an object, so it holds none.
type Objects struct{}

// empty reports a bucket without objects, versions or delete markers,
// which DeleteBucket requires.
func (Objects) empty() bool { return true }

// commitObject is the one place an object change lands in its bucket: the
// object operations call it inside the bucket's kernel.Write, after every
// check, and it raises the change's event notifications in the same
// commit, so a notification never reports a change that did not happen.
//
//nolint:unused // The seam the object operations commit through; this build binds none of them.
func (s *Service) commitObject(tx *kernel.Tx, b *Bucket, ev event) {
	s.notify(tx, b, ev)
}
