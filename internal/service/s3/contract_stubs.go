package s3

import (
	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/awserr"
)

// The SSE-C and Object Lock API below is declared ahead of its
// implementation (s3-ssec-object-lock.md 11): every stub answers
// errNotImplementedYet, a NotImplementedYetError or a zero value, and
// nothing calls one yet, so no request behaves differently. Each stub
// leaves this file when the change that implements it wires it in; the
// file goes once the last one has.

// errNotImplementedYet is a stub's answer: 501 NotImplemented naming the
// function.
func errNotImplementedYet(fn string) *awserr.Error {
	return s3api.ErrNotImplemented("understudy has not implemented " + fn + " yet; nothing was changed")
}

// NotImplementedYetError is a stub's error where the caller expects a
// plain error, as a text marshaler does.
type NotImplementedYetError struct {
	// Func names the stub.
	Func string
}

func (e *NotImplementedYetError) Error() string {
	return "s3: " + e.Func + " is not implemented yet"
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
	customerHeaders{algorithm: nil, key: nil, keyMD5: nil},
	customerHeaders.sent,
	customerKey{key: nil, keyMD5: ""},
	parseCustomerWrite,
	parseCustomerRead,
	newCustomerEncryption,
	CustomerEncryption.matches,
	checkCustomerWrite,
	checkCustomerRead,
	checkCustomerPart,
	newBucketBlock,
	blockedEncryptionOf,
	encryptionRead,
}
