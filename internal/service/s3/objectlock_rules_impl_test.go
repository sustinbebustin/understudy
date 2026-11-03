package s3

import (
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/checksum"
)

// TestIntegrityDeclared: Content-MD5, an x-amz-checksum-* header or a
// trailer declare a body's integrity; a signed payload hash or nothing do
// not (s3-ssec-object-lock.md 6.3; S3SOL-14 14-retention-signed-sha256,
// recorded 2026-11-03).
func TestIntegrityDeclared(t *testing.T) {
	signed := "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
	for name, tc := range map[string]struct {
		u    upload
		want bool
	}{
		"Content-MD5":         {upload{checksum: noChecksum{}, md5: &contentMD5{}}, true},
		"checksum header":     {upload{checksum: headerChecksum{sent: sentChecksum{alg: checksum.CRC32}}}, true},
		"checksum trailer":    {upload{checksum: trailerChecksum{alg: checksum.CRC32}}, true},
		"signed payload hash": {upload{checksum: noChecksum{}, payloadSHA256: signed}, false},
		"nothing":             {upload{checksum: noChecksum{}}, false},
	} {
		if got := tc.u.integrityDeclared(); got != tc.want {
			t.Errorf("%s: integrityDeclared = %v, want %v", name, got, tc.want)
		}
	}
}

// TestLockRequestCheck: against its bucket a write's lock settings are
// judged in the order of s3-ssec-object-lock.md 11.3: explicit settings on
// a bucket without Object Lock first (switch
// s3.object-lock.headers-without-lock), then the Content-MD5 or checksum
// requirement of a PutObject with lock settings of its own, a legal hold
// alone included, or whose version gets the bucket default's retention
// (testdata/parity/s3/R12/11-put-without-checksum.json; S3SOL-14
// 14-legal-hold-only, recorded 2026-11-03; a hold set OFF is inferred to
// count as ON does).
func TestLockRequestCheck(t *testing.T) {
	missing := wantError{status: 400, code: "InvalidRequest", message: "Bucket is missing Object Lock Configuration"}
	needsSum := wantError{status: 400, code: "InvalidRequest", message: "Content-MD5 OR x-amz-checksum- HTTP header is required for Put Object requests with Object Lock parameters"}
	gov := s3api.ObjectLockRetentionModeGovernance
	rapid.Check(t, func(t *rapid.T) {
		var b Bucket
		hasDefault := false
		switch rapid.IntRange(0, 2).Draw(t, "bucket") {
		case 1:
			b.ObjectLock = lockConfig(gov, nil, nil)
		case 2:
			b.ObjectLock, hasDefault = lockConfig(gov, new(int32(1)), nil), true
		}
		var r lockRequest
		if rapid.Bool().Draw(t, "retention") {
			r.explicit.Retention = &Retention{Mode: RetentionCompliance, RetainUntil: lockNow.Add(time.Hour)}
		}
		r.explicit.LegalHold = rapid.SampledFrom([]LegalHold{LegalHoldNever, LegalHoldOn, LegalHoldOff}).Draw(t, "hold")
		r.undeclared = rapid.Bool().Draw(t, "undeclared")
		got := r.check(&b)
		switch {
		case r.explicit != (ObjectLock{}) && b.ObjectLock == nil:
			missing.check(t, "explicit settings on a plain bucket", got)
		case r.undeclared && (r.explicit != (ObjectLock{}) || hasDefault):
			needsSum.check(t, "lock settings without a checksum", got)
		case got != nil:
			t.Fatalf("check(%+v) on %+v = %s %q, want accepted", r, b.ObjectLock, got.ID.QueryCode, got.Message)
		}
	})
}
