package s3

import (
	"context"
	"slices"

	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/blob"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/service/s3/notify"
)

// bindObjectLockState binds the operations that read and change one object
// version's retention and legal hold (s3-ssec-object-lock.md 6.4, 6.5).
// Each checks, in order (11.3): the body (Puts, through kernel.WithCheck),
// the bucket, its Object Lock configuration, then the version, so a bucket
// without Object Lock is refused before the key is looked up (switch
// s3.object-lock.check-order).
func (s *Service) bindObjectLockState(b s3api.Binder) {
	putRetention := func(in *s3api.PutObjectRetentionInput) access { return at(in.Bucket, in.ExpectedBucketOwner) }
	b.PutObjectRetention(s.putObjectRetention, checksumUnchecked(registry.Partial{Note: "event holds (EventHold, EventHoldDuration) are refused with NotImplemented; " +
		bypassNote + "; the s3:ObjectRetention:Put record carries mode and retainUntilDate as documented, " +
		"and its object fields, the date's form and whether a removed retention sends one (here: none) are unrecorded"}), touches(s, putRetention),
		kernel.WithCheck(func(in *s3api.PutObjectRetentionInput) *awserr.Error {
			_, err := parseRetentionBody(in.Retention)
			return err
		}))
	getRetention := func(in *s3api.GetObjectRetentionInput) access { return at(in.Bucket, in.ExpectedBucketOwner) }
	b.GetObjectRetention(reader(s, getRetention, getObjectRetention), registry.Full{}, touches(s, getRetention))
	putHold := func(in *s3api.PutObjectLegalHoldInput) access { return at(in.Bucket, in.ExpectedBucketOwner) }
	b.PutObjectLegalHold(s.putObjectLegalHold, checksumUnchecked(registry.Full{}), touches(s, putHold),
		kernel.WithCheck(func(in *s3api.PutObjectLegalHoldInput) *awserr.Error {
			_, err := parseLegalHoldBody(in.LegalHold)
			return err
		}))
	getHold := func(in *s3api.GetObjectLegalHoldInput) access { return at(in.Bucket, in.ExpectedBucketOwner) }
	b.GetObjectLegalHold(reader(s, getHold, getObjectLegalHold), registry.Full{}, touches(s, getHold))
}

// putObjectRetention sets, changes or removes a version's retention. The
// date is checked against the write's commit instant, then the change
// rules (withRetention). A retention placed raises s3:ObjectRetention:Put
// (aws-doc, s3-ssec-object-lock.md 7.4). Inferred: a removal raises
// nothing, as the record's mode and retainUntilDate, documented as always
// present, would have no value, and the documented triggers name none.
func (s *Service) putObjectRetention(ctx context.Context, c *kernel.Call, in *s3api.PutObjectRetentionInput) (*s3api.PutObjectRetentionOutput, *awserr.Error) {
	next, err := parseRetentionBody(in.Retention)
	if err != nil {
		return nil, err
	}
	bypass := s.governanceBypass(c, in.BypassGovernanceRetention, objectResource(in.Bucket, in.Key))
	_, _, err = writeObjects(ctx, s, c, at(in.Bucket, in.ExpectedBucketOwner), func(b *Bucket, w objectTx) (struct{}, []blob.Ref, *awserr.Error) {
		v, err := findLockVersion(b, in.Key, in.VersionId, "PUT")
		if err != nil {
			return struct{}{}, nil, err
		}
		if next != nil {
			// LocalStack test_s3.snapshot.json L9488 and S3SOL-18
			// 18-retention-past-summer name the element and its date in
			// Java's form.
			if err := checkRetainUntil(next.RetainUntil, w.now, "RetainUntilDate", javaDate(next.RetainUntil.Wire())); err != nil {
				return struct{}{}, nil, err
			}
		}
		lock, err := v.Object.Lock.withRetention(next, w.now, bypass)
		if err != nil {
			return struct{}{}, nil, err
		}
		relock(b, in.Key, v, lock)
		if r := lock.Retention; r != nil {
			s.commitObject(w, b, event{key: in.Key, versionID: v.ID, change: notify.Retained{
				Action: notify.RetentionPut, ETag: v.Object.ETag,
				Mode: s3api.ObjectLockRetentionMode(retentionModeTexts[r.Mode]), RetainUntil: r.RetainUntil.Wire(),
			}})
		}
		return struct{}{}, nil, nil
	})
	if err != nil {
		return nil, err
	}
	return &s3api.PutObjectRetentionOutput{}, nil
}

// getObjectRetention reads a version's retention, expired or not, in the
// millisecond form of the S3 dialect (switch
// s3.object-lock.retain-until-format). A version without one, never given
// or removed, is 404 (LocalStack test_s3.snapshot.json L9458; switch
// s3.object-lock.cleared-reads).
func getObjectRetention(in *s3api.GetObjectRetentionInput, b *Bucket) (*s3api.GetObjectRetentionOutput, *awserr.Error) {
	v, err := findLockVersion(b, in.Key, in.VersionId, "GET")
	if err != nil {
		return nil, err
	}
	r := v.Object.Lock.Retention
	if r == nil {
		return nil, errNoObjectLockState()
	}
	mode := s3api.ObjectLockRetentionMode(retentionModeTexts[r.Mode])
	return &s3api.GetObjectRetentionOutput{Retention: &s3api.ObjectLockRetention{Mode: &mode, RetainUntilDate: new(r.RetainUntil.Wire())}}, nil
}

// putObjectLegalHold sets a version's legal hold ON or OFF. Any caller
// allowed the operation may place or lift one, so there is no bypass
// (s3-ssec-object-lock.md 6.5).
func (s *Service) putObjectLegalHold(ctx context.Context, c *kernel.Call, in *s3api.PutObjectLegalHoldInput) (*s3api.PutObjectLegalHoldOutput, *awserr.Error) {
	hold, err := parseLegalHoldBody(in.LegalHold)
	if err != nil {
		return nil, err
	}
	_, _, err = writeObjects(ctx, s, c, at(in.Bucket, in.ExpectedBucketOwner), func(b *Bucket, _ objectTx) (struct{}, []blob.Ref, *awserr.Error) {
		v, err := findLockVersion(b, in.Key, in.VersionId, "PUT")
		if err != nil {
			return struct{}{}, nil, err
		}
		lock := v.Object.Lock
		lock.LegalHold = hold
		relock(b, in.Key, v, lock)
		return struct{}{}, nil, nil
	})
	if err != nil {
		return nil, err
	}
	return &s3api.PutObjectLegalHoldOutput{}, nil
}

// getObjectLegalHold reads a version's legal hold: 404 for a version no
// hold was ever set on (LocalStack test_s3.snapshot.json L9919), OFF once
// lifted (switch s3.object-lock.cleared-reads).
func getObjectLegalHold(in *s3api.GetObjectLegalHoldInput, b *Bucket) (*s3api.GetObjectLegalHoldOutput, *awserr.Error) {
	v, err := findLockVersion(b, in.Key, in.VersionId, "GET")
	if err != nil {
		return nil, err
	}
	text, ok := legalHoldTexts[v.Object.Lock.LegalHold]
	if !ok {
		return nil, errNoObjectLockState()
	}
	return &s3api.GetObjectLegalHoldOutput{LegalHold: &s3api.ObjectLockLegalHold{Status: new(s3api.ObjectLockLegalHoldStatus(text))}}, nil
}

// findLockVersion resolves the version a lock operation addresses, on a
// bucket with Object Lock only (LocalStack test_s3.snapshot.json L9510,
// L9520): the bucket's lock state is checked before the key (switch
// s3.object-lock.check-order), and a delete marker named by id is the 405
// for method (L9805, L9817).
func findLockVersion(b *Bucket, key string, id *string, method string) (Version, *awserr.Error) {
	if b.ObjectLock == nil {
		return Version{}, errInvalidRequest(missingLockConfigText)
	}
	v, err := findVersion(b, key, id, method)
	if err != nil {
		// The 405 on a delete marker carries no Last-Modified here, unlike
		// GetObject's (S3SOL-22 22-hold-delete-marker and
		// 22-get-hold-delete-marker, recorded 2026-11-03; inferred for the
		// retention operations).
		err.Headers = slices.DeleteFunc(err.Headers, func(h awserr.Header) bool { return h.Name == "Last-Modified" })
	}
	return v, err
}

// relock gives version v of key the lock state l. The Object is replaced
// with a copy, never changed in place, since readers may hold it (Object).
func relock(b *Bucket, key string, v Version, l ObjectLock) {
	obj := *v.Object
	obj.Lock = l
	v.Object = &obj
	b.Objects.replace(key, v)
}

// errNoObjectLockState answers a read of a retention or legal hold the
// version does not have (LocalStack test_s3.snapshot.json L9458, L9919).
func errNoObjectLockState() *awserr.Error {
	return s3api.ErrNoSuchObjectLockConfiguration("The specified object does not have a ObjectLock configuration")
}
