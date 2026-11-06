package s3

import (
	"testing"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/aws"
)

// TestBlockedEncryptionOfRefusesEmptyAndMixedLists: a BlockedEncryptionTypes
// element with no type, or with both NONE and SSE-C, says nothing a bucket
// can hold, and is MalformedXML (inferred, unrecorded; s3-ssec-object-lock.md
// 4.2); a list of one type, repeated or not, is that type.
func TestBlockedEncryptionOfRefusesEmptyAndMixedLists(t *testing.T) {
	property(t, func(t *rapid.T) {
		types := rapid.SliceOfN(rapid.SampledFrom([]s3api.EncryptionType{s3api.EncryptionTypeNone, s3api.EncryptionTypeSseC}), 0, 4).Draw(t, "types")
		got, err := blockedEncryptionOf(s3api.ServerSideEncryptionRule{BlockedEncryptionTypes: &s3api.BlockedEncryptionTypes{EncryptionType: types}}, aws.DefaultRegion())
		var none, sseC bool
		for _, e := range types {
			none = none || e == s3api.EncryptionTypeNone
			sseC = sseC || e == s3api.EncryptionTypeSseC
		}
		if none == sseC {
			if err == nil || err.ID.QueryCode != "MalformedXML" {
				t.Fatalf("blockedEncryptionOf(%v) = %d, %v; want MalformedXML", types, got, err)
			}
			return
		}
		want := BlockSSEC
		if none {
			want = BlockNone
		}
		if err != nil || got != want {
			t.Fatalf("blockedEncryptionOf(%v) = %d, %v; want %d", types, got, err, want)
		}
	})
}

// TestEncryptionReadKeepsTheStoredRule: GetBucketEncryption reports the
// stored default and bucket key as put, and fills in only what the rule
// left out (s3-ssec-object-lock.md 4.2; testdata/parity/s3/R06/02-get-encryption-arn.json).
func TestEncryptionReadKeepsTheStoredRule(t *testing.T) {
	kms := &s3api.ServerSideEncryptionByDefault{SSEAlgorithm: s3api.ServerSideEncryptionAwsKms, KMSMasterKeyID: new("arn:aws:kms:us-east-1:000000000000:key/k")}
	b := &Bucket{
		BlockedEncryption: BlockNone,
		Encryption:        &s3api.ServerSideEncryptionConfiguration{Rules: []s3api.ServerSideEncryptionRule{{ApplyServerSideEncryptionByDefault: kms, BucketKeyEnabled: new(true)}}},
	}
	got := encryptionRead(b)
	if got == nil || len(got.Rules) != 1 {
		t.Fatalf("encryptionRead = %+v, want one rule", got)
	}
	r := got.Rules[0]
	if r.ApplyServerSideEncryptionByDefault != kms || r.BucketKeyEnabled == nil || !*r.BucketKeyEnabled ||
		r.BlockedEncryptionTypes == nil || len(r.BlockedEncryptionTypes.EncryptionType) != 1 || r.BlockedEncryptionTypes.EncryptionType[0] != s3api.EncryptionTypeNone {
		t.Errorf("encryptionRead = %+v, want the stored KMS default, bucket key true and NONE", r)
	}
	if b.Encryption.Rules[0].BlockedEncryptionTypes != nil {
		t.Error("encryptionRead changed the stored rule")
	}
}

// TestCheckCustomerCompleteWithoutHeaders: a Complete without SSE-C
// headers needs none unless its upload is SSE-C and declared a checksum
// algorithm, as the model notes for
// CompleteMultipartUploadRequest$SSECustomerKey; that one is refused with
// the recorded text (s3-ssec-object-lock.md 3.6; S3SOL-08 08-no-key,
// recorded 2026-11-03).
func TestCheckCustomerCompleteWithoutHeaders(t *testing.T) {
	ssec := CustomerEncryption{Salt: make([]byte, 16), MAC: make([]byte, 32)}
	for _, tc := range []struct {
		name    string
		u       Upload
		refused bool
	}{
		{"plain, undeclared", Upload{Encryption: SSES3Encryption{}}, false},
		{"plain, CRC32", Upload{Encryption: SSES3Encryption{}, Checksum: UploadChecksum{Algorithm: "CRC32", Type: s3api.ChecksumTypeComposite}}, false},
		{"SSE-C, undeclared", Upload{Encryption: ssec}, false},
		{"SSE-C, CRC32", Upload{Encryption: ssec, Checksum: UploadChecksum{Algorithm: "CRC32", Type: s3api.ChecksumTypeComposite}}, true},
	} {
		err := checkCustomerComplete(&tc.u, customerHeaders{}, "http")
		switch {
		case tc.refused && (err == nil || err.Message != "The object was stored using a form of Server Side Encryption. Correct SSE-C request parameters are required for this request when specifying checksums for each part."):
			t.Errorf("%s: %v, want the recorded checksum refusal", tc.name, err)
		case !tc.refused && err != nil:
			t.Errorf("%s: %s %q, want accepted", tc.name, err.ID.QueryCode, err.Message)
		}
	}
}
