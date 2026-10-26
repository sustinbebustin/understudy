package s3

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/blob"
	"github.com/sustinbebustin/understudy/internal/clock"
	"github.com/sustinbebustin/understudy/internal/protocol/jsonx"
)

// fataler is what the snapshot helpers need from *testing.T and *rapid.T.
type fataler interface {
	Helper()
	Fatal(args ...any)
	Fatalf(format string, args ...any)
}

// snapshotObject is an object version with every field a snapshot needs
// set, holding enc and lock.
func snapshotObject(t fataler, enc Encryption, lock ObjectLock) Object {
	t.Helper()
	ref, err := blob.ParseRef(strings.Repeat("ab", 32))
	if err != nil {
		t.Fatal(err)
	}
	return Object{Blob: ref, ETag: "etag", Encryption: enc, Lock: lock}
}

// snapshotForm is v as stateCodec writes it, decoded as plain JSON, and v
// read back from it.
func snapshotForm[T any](t fataler, v T) (doc map[string]any, back T) {
	t.Helper()
	codec := stateCodec()
	data, err := codec.Marshal(&v)
	if err != nil {
		t.Fatalf("Marshal(%+v): %v", v, err)
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("the snapshot form %s is not a JSON object: %v", data, err)
	}
	if err := codec.Unmarshal(data, &back); err != nil {
		t.Fatalf("Unmarshal(%s): %v", data, err)
	}
	return doc, back
}

// TestEncryptionSnapshotForm: each encryption variant is held in a snapshot
// as the tagged union of s3-ssec-object-lock.md 11.2 (ARCHITECTURE.md 13),
// with the SSE-KMS fields as they were before the sum, and reads back as
// the same variant.
func TestEncryptionSnapshotForm(t *testing.T) {
	salt := []byte("0123456789abcdef")
	mac := []byte("0123456789abcdef0123456789abcdef")
	kms := KMSEncryption{
		Algorithm: s3api.ServerSideEncryptionAwsKms, KeyARN: "arn:aws:kms:us-east-1:000000000000:key/k",
		BucketKey: true, Context: map[string]string{arnContextKey: "arn:aws:s3:::b/k"}, DataKey: []byte("data key"),
	}
	for _, tc := range []struct {
		name string
		enc  Encryption
		want map[string]any
	}{
		{"sse-s3", SSES3Encryption{}, map[string]any{"kind": "sse-s3"}},
		{"sse-kms", kms, map[string]any{
			"kind": "sse-kms", "algorithm": "aws:kms", "keyArn": kms.KeyARN, "bucketKey": true,
			"context": map[string]any{arnContextKey: "arn:aws:s3:::b/k"}, "dataKey": base64.StdEncoding.EncodeToString(kms.DataKey),
		}},
		{"sse-c", CustomerEncryption{Salt: salt, MAC: mac}, map[string]any{
			"kind": "sse-c", "salt": base64.StdEncoding.EncodeToString(salt), "mac": base64.StdEncoding.EncodeToString(mac),
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc, back := snapshotForm(t, snapshotObject(t, tc.enc, ObjectLock{}))
			if d := cmp.Diff(tc.want, doc["encryption"]); d != "" {
				t.Errorf("encryption snapshot form (-want +got):\n%s", d)
			}
			if d := cmp.Diff(tc.enc, back.Encryption); d != "" {
				t.Errorf("encryption read back (-want +got):\n%s", d)
			}
		})
	}
}

// TestEncryptionSnapshotRefusesUnknownKind: a snapshot whose encryption
// names no variant is refused rather than read as some default, which
// would report the wrong encryption for the object.
func TestEncryptionSnapshotRefusesUnknownKind(t *testing.T) {
	var o Object
	err := stateCodec().Unmarshal([]byte(`{"blob":"`+strings.Repeat("ab", 32)+`","etag":"e","encryption":{"kind":"sse-x"}}`), &o)
	if _, ok := errors.AsType[*jsonx.UnionError](err); !ok {
		t.Fatalf("Unmarshal of kind sse-x = %v, want a *jsonx.UnionError", err)
	}
}

// TestSnapshotRefusesMissingEncryption: a bucket section whose object
// version or upload has a null or absent encryption is refused, naming the
// bucket, key and holder, rather than read as an object with no encryption,
// which state never holds (Encryption).
func TestSnapshotRefusesMissingEncryption(t *testing.T) {
	obj := snapshotObject(t, SSES3Encryption{}, ObjectLock{})
	withObject := BucketCell{Bucket: &Bucket{Name: "b", Objects: Objects{Keys: map[string][]Version{"k": {{ID: "v1", Object: &obj}}}}}}
	withUpload := BucketCell{Bucket: &Bucket{Name: "b", Uploads: Uploads{ByID: map[string]*Upload{"u1": {ID: "u1", Key: "k", Encryption: SSES3Encryption{}}}}}}
	const stored = `,"encryption":{"kind":"sse-s3"}`
	codec := stateCodec()
	for _, tc := range []struct {
		name, holder string
		cell         BucketCell
		replacement  string
	}{
		{"version, null", "version v1", withObject, `,"encryption":null`},
		{"version, absent", "version v1", withObject, ""},
		{"upload, null", "upload u1", withUpload, `,"encryption":null`},
		{"upload, absent", "upload u1", withUpload, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, err := codec.Marshal(&tc.cell)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Count(string(data), stored) != 1 {
				t.Fatalf("the snapshot form %s does not hold %s once", data, stored)
			}
			var back BucketCell
			err = codec.Unmarshal([]byte(strings.Replace(string(data), stored, tc.replacement, 1)), &back)
			e, ok := errors.AsType[*MissingEncryptionError](err)
			if !ok || *e != (MissingEncryptionError{Bucket: "b", Key: "k", Holder: tc.holder}) {
				t.Fatalf("Unmarshal = %v, want a MissingEncryptionError for %s of b/k", err, tc.holder)
			}
		})
	}
}

// TestObjectLockSnapshotForm: a version's lock state is held as
// "lock":{"legalHold":...,"retention":{"mode":...,"retainUntil":...}} to the
// millisecond (s3-ssec-object-lock.md 11.2, switch
// s3.object-lock.retain-until-format); a version with none, and an upload
// that asked for none, write no lock member, so their snapshot form is the
// one version 1 had.
func TestObjectLockSnapshotForm(t *testing.T) {
	until := clock.FromWire(time.Date(2030, 1, 1, 0, 0, 0, 123_000_000, time.UTC))
	lock := ObjectLock{Retention: &Retention{Mode: RetentionGovernance, RetainUntil: until}, LegalHold: LegalHoldOn}
	doc, back := snapshotForm(t, snapshotObject(t, SSES3Encryption{}, lock))
	want := map[string]any{"legalHold": "ON", "retention": map[string]any{"mode": "GOVERNANCE", "retainUntil": "2030-01-01T00:00:00.123Z"}}
	if d := cmp.Diff(want, doc["lock"]); d != "" {
		t.Errorf("lock snapshot form (-want +got):\n%s", d)
	}
	if d := cmp.Diff(lock, back.Lock, cmp.AllowUnexported(clock.Instant{})); d != "" {
		t.Errorf("lock read back (-want +got):\n%s", d)
	}

	released := ObjectLock{LegalHold: LegalHoldOff}
	doc, _ = snapshotForm(t, snapshotObject(t, SSES3Encryption{}, released))
	if d := cmp.Diff(map[string]any{"legalHold": "OFF"}, doc["lock"]); d != "" {
		t.Errorf("released hold snapshot form (-want +got):\n%s", d)
	}

	doc, _ = snapshotForm(t, snapshotObject(t, SSES3Encryption{}, ObjectLock{}))
	if _, ok := doc["lock"]; ok {
		t.Errorf("an object without lock state writes lock %v, want no member", doc["lock"])
	}
	upload, _ := snapshotForm(t, Upload{ID: "u", Key: "k", Encryption: SSES3Encryption{}})
	if _, ok := upload["lock"]; ok {
		t.Errorf("an upload without lock settings writes lock %v, want no member", upload["lock"])
	}
}

// TestBlockedEncryptionSnapshotForm: the bucket's SSE-C block is absent
// from a snapshot while it is the new-bucket default, SSE-C blocked, and
// "NONE" once unblocked (s3-ssec-object-lock.md 4.1, 4.2).
func TestBlockedEncryptionSnapshotForm(t *testing.T) {
	for block, want := range map[BlockedEncryption]any{BlockSSEC: nil, BlockNone: "NONE"} {
		doc, back := snapshotForm(t, Bucket{Name: "b", BlockedEncryption: block})
		if doc["blockedEncryption"] != want {
			t.Errorf("BlockedEncryption %d writes %v, want %v", block, doc["blockedEncryption"], want)
		}
		if back.BlockedEncryption != block {
			t.Errorf("BlockedEncryption %d reads back as %d", block, back.BlockedEncryption)
		}
	}
}

// TestStateTextsRefuseUnknownValues: an enum value without a text fails
// the save that would write it, naming the enum and its texts, so a
// snapshot never holds a value it could not read back.
func TestStateTextsRefuseUnknownValues(t *testing.T) {
	_, err := RetentionMode(0).MarshalText()
	e, ok := errors.AsType[*EnumTextError](err)
	if !ok || e.Type != "RetentionMode" || e.Value != "0" || !cmp.Equal(e.Known, []string{"GOVERNANCE", "COMPLIANCE"}) {
		t.Fatalf("MarshalText(RetentionMode(0)) = %v, want an EnumTextError naming RetentionMode, 0 and its two texts", err)
	}
	if got, want := e.Error(), "s3: RetentionMode 0 is not one of GOVERNANCE, COMPLIANCE"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
	var h LegalHold
	err = h.UnmarshalText([]byte("on"))
	if e, ok := errors.AsType[*EnumTextError](err); !ok || e.Value != `"on"` || !cmp.Equal(e.Known, []string{"ON", "OFF"}) {
		t.Errorf(`UnmarshalText("on") = %v, want an EnumTextError quoting "on" with the texts ON, OFF`, err)
	}
}

// TestLockedVersionRoundTrip: any lock state and encryption a version, or
// an upload that completes to one, can hold survive a snapshot unchanged
// (ARCHITECTURE.md 13).
func TestLockedVersionRoundTrip(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		var enc Encryption
		switch rapid.IntRange(0, 2).Draw(t, "variant") {
		case 0:
			enc = SSES3Encryption{}
		case 1:
			enc = KMSEncryption{
				Algorithm: rapid.SampledFrom([]s3api.ServerSideEncryption{s3api.ServerSideEncryptionAwsKms, s3api.ServerSideEncryptionAwsKmsDsse}).Draw(t, "algorithm"),
				KeyARN:    rapid.StringMatching(`arn:aws:kms:[a-z0-9-]{1,12}:[0-9]{12}:key/[a-f0-9-]{1,36}`).Draw(t, "keyArn"),
				BucketKey: rapid.Bool().Draw(t, "bucketKey"),
				Context:   rapid.MapOf(rapid.StringN(1, 8, -1), rapid.String()).Draw(t, "context"),
				DataKey:   rapid.SliceOfN(rapid.Byte(), 1, 64).Draw(t, "dataKey"),
			}
		default:
			enc = CustomerEncryption{Salt: rapid.SliceOfN(rapid.Byte(), 16, 16).Draw(t, "salt"), MAC: rapid.SliceOfN(rapid.Byte(), 32, 32).Draw(t, "mac")}
		}
		var lock ObjectLock
		if rapid.Bool().Draw(t, "retained") {
			ms := rapid.Int64Range(0, 253402300799999).Draw(t, "retainUntilMillis")
			lock.Retention = &Retention{
				Mode:        rapid.SampledFrom([]RetentionMode{RetentionGovernance, RetentionCompliance}).Draw(t, "mode"),
				RetainUntil: clock.FromWire(time.UnixMilli(ms)),
			}
		}
		lock.LegalHold = rapid.SampledFrom([]LegalHold{LegalHoldNever, LegalHoldOn, LegalHoldOff}).Draw(t, "hold")
		obj := snapshotObject(t, enc, lock)
		_, back := snapshotForm(t, obj)
		if d := cmp.Diff(obj, back, cmp.AllowUnexported(clock.Instant{}, blob.Ref{})); d != "" {
			t.Fatalf("version read back (-want +got):\n%s", d)
		}
		up := Upload{ID: "u", Key: "k", Encryption: enc, Lock: lock}
		_, upBack := snapshotForm(t, up)
		if d := cmp.Diff(up, upBack, cmp.AllowUnexported(clock.Instant{}, blob.Ref{})); d != "" {
			t.Fatalf("upload read back (-want +got):\n%s", d)
		}
	})
}
