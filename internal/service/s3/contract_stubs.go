package s3

import (
	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/awserr"
)

// The Object Lock API below is declared ahead of its implementation
// (s3-ssec-object-lock.md 11): every stub answers errNotImplementedYet or
// a zero value, and nothing calls one yet, so no request behaves
// differently. Each leaves this file when the change that wires it in
// lands; the file goes once the last one has.

// errNotImplementedYet is a stub's answer: 501 NotImplemented naming the
// function.
func errNotImplementedYet(fn string) *awserr.Error {
	return s3api.ErrNotImplemented("understudy has not implemented " + fn + " yet; nothing was changed")
}

// stubs keeps the stubs referenced until their callers exist, so the
// linter reports a stub left behind once it is wired rather than every
// stub now. The keyed literals stand for the fields only stubs read.
var _ = []any{
	objectLockHeaders.parse,
	ObjectLock.withRetention,
	ObjectLock.checkDelete,
	Retention.inForce,
	checkLockTarget,
	defaultRetention{mode: 0, period: 0},
	bucketDefaultRetention,
	defaultRetention.from,
	lockOnWrite,
	needsIntegrity,
	upload.integrityDeclared,
	parseRetentionBody,
	checkRetainUntil,
	parseLegalHoldBody,
	javaDate,
	checkBypassHeader,
	(*Service).governanceBypass,
}
