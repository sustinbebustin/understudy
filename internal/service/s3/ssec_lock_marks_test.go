package s3_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/evidence"
	"github.com/sustinbebustin/understudy/internal/fidelity"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/protocol/restxml"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/service/s3"
	"github.com/sustinbebustin/understudy/internal/store"
)

// TestSSECAndObjectLockCoverageMarks: each operation this unit touches is
// bound with the coverage mark and note s3-ssec-object-lock.md 11.1 gives
// it, and UpdateObjectEncryption stays unbound.
func TestSSECAndObjectLockCoverageMarks(t *testing.T) {
	const (
		putNote      = "an object ACL a bucket accepts is not stored; user metadata is stored as sent, without RFC 2047 decoding; chunk signatures are verified under sigv4 verify; Object Lock event-hold headers are refused with NotImplemented"
		readNote     = "presigned URLs are verified and expire on the emulator clock under sigv4 verify"
		copyNote     = "an object ACL a bucket accepts is not stored; errors after the copy starts are not sent as 200-with-error; Object Lock event-hold headers are refused with NotImplemented"
		createNote   = "an object ACL a bucket accepts is not stored; no lifecycle abort date; Object Lock event-hold headers are refused with NotImplemented"
		partNote     = "chunk signatures are verified under sigv4 verify"
		answeredNote = "errors are answered before the response starts, so only a StatusOKError fault rule sends 200-with-error"
		deleteNote   = "no MFA delete; s3:BypassGovernanceRetention is not evaluated, so x-amz-bypass-governance-retention alone bypasses GOVERNANCE retention"
		notifyNote   = "deliveries to SQS, SNS and Lambda; an EventBridge configuration is stored but sends nothing; " +
			"destination validation checks existence and refuses FIFO queues and topics, not permissions, where AWS refuses a destination whose policy does not admit S3; " +
			"under iam.enforce=enforce such a destination refuses each notification, which is dropped (s3.notification.delivery-denied); " +
			"s3:ObjectRetention:Put is stored but sends nothing"
	)
	marks := map[string]registry.BindCoverage{
		"PutObject":                          registry.Partial{Note: putNote},
		"GetObject":                          registry.Partial{Note: readNote},
		"HeadObject":                         registry.Partial{Note: readNote},
		"GetObjectAttributes":                registry.Full{},
		"CopyObject":                         registry.Partial{Note: copyNote},
		"CreateMultipartUpload":              registry.Partial{Note: createNote},
		"UploadPart":                         registry.Partial{Note: partNote},
		"UploadPartCopy":                     registry.Partial{Note: answeredNote},
		"CompleteMultipartUpload":            registry.Partial{Note: answeredNote},
		"DeleteObject":                       registry.Partial{Note: deleteNote},
		"DeleteObjects":                      registry.Partial{Note: deleteNote},
		"PutObjectRetention":                 registry.Partial{Note: "event holds (EventHold, EventHoldDuration) are refused with NotImplemented; s3:BypassGovernanceRetention is not evaluated; no s3:ObjectRetention:Put notification is sent"},
		"GetObjectRetention":                 registry.Full{},
		"PutObjectLegalHold":                 registry.Full{},
		"GetObjectLegalHold":                 registry.Full{},
		"PutObjectLockConfiguration":         registry.Partial{Note: "DefaultEventHold is refused with NotImplemented; x-amz-bucket-object-lock-token is accepted and ignored"},
		"GetObjectLockConfiguration":         registry.Full{},
		"PutBucketEncryption":                registry.Full{},
		"GetBucketEncryption":                registry.Full{},
		"DeleteBucketEncryption":             registry.Full{},
		"PutBucketNotificationConfiguration": registry.Partial{Note: notifyNote},
	}
	run(t, s3.Options{}, func(h *harness) {
		t := h.T
		operation := func(name string) *registry.Operation {
			op, ok := h.Kernel.Registry().Operation(ref.OperationID{Service: "s3", Name: name})
			if !ok {
				t.Fatalf("no operation s3.%s", name)
			}
			return op
		}
		for name, want := range marks {
			bound, isBound := h.Kernel.Bindings().Status(operation(name)).(kernel.Bound)
			if !isBound {
				t.Errorf("s3.%s is unbound, want %#v", name, want)
				continue
			}
			if bound.Coverage != want {
				t.Errorf("s3.%s coverage = %#v, want %#v", name, bound.Coverage, want)
			}
		}
		if _, unbound := h.Kernel.Bindings().Status(operation("UpdateObjectEncryption")).(kernel.Unbound); !unbound {
			t.Error("s3.UpdateObjectEncryption is bound, want it unbound (s3-ssec-object-lock.md 8, 11.1)")
		}
	})
}

// wantSwitch is one switch of s3-ssec-object-lock.md 11.4 as the service
// must list it.
type wantSwitch struct {
	name, value string
	label       evidence.Label
	anchor      string
	record      string
	ops         []string
}

// TestSSECAndObjectLockSwitches: the service lists the switches of
// s3-ssec-object-lock.md 11.4 after its earlier five, in that table's
// order, each pointing at its section and its recording-queue item and
// settled by no scenario yet (section 12); s3.encryption.after-delete is
// now backed by the recording of section 2. IAM enforcement's two S3
// switches (s3.denial.text, s3.root.bucket-policy-lockout) follow them
// (iam-enforcement-surfaces.md 12.8).
func TestSSECAndObjectLockSwitches(t *testing.T) {
	plainHTTPOps := []string{"s3.PutObject", "s3.CopyObject", "s3.CreateMultipartUpload", "s3.UploadPart", "s3.UploadPartCopy", "s3.CompleteMultipartUpload", "s3.GetObject", "s3.HeadObject", "s3.GetObjectAttributes"}
	inferred, recorded := evidence.Inferred, evidence.AWSRecorded
	want := []wantSwitch{
		{"s3.sse-c.plain-http", "refused", evidence.AWSDoc, "39-https", "S3SOL-02", append(slices.Clone(plainHTTPOps), "s3.ListParts")},
		{"s3.sse-c.blocked-write", "AccessDenied", recorded, "43-what-the-block-refuses", "S3SOL-01", []string{"s3.PutObject", "s3.CopyObject", "s3.CreateMultipartUpload", "s3.UploadPart", "s3.UploadPartCopy"}},
		{"s3.sse-c.missing-key-md5", "InvalidArgument", recorded, "32-write-side-validation-third-party-recorded", "S3SOL-03", []string{"s3.PutObject", "s3.CopyObject", "s3.CreateMultipartUpload", "s3.UploadPart", "s3.UploadPartCopy", "s3.CompleteMultipartUpload"}},
		{"s3.sse-c.key-encoding", "write-side-texts", recorded, "32-write-side-validation-third-party-recorded", "S3SOL-04", plainHTTPOps},
		{"s3.sse-c.metadata-without-key", "InvalidRequest", inferred, "33-reads-getobject-headobject-getobjectattributes", "S3SOL-05", []string{"s3.HeadObject", "s3.GetObjectAttributes"}},
		{"s3.sse-c.copy-source-not-applicable", "InvalidRequest", recorded, "35-copyobject-and-uploadpartcopy", "S3SOL-06", []string{"s3.CopyObject", "s3.UploadPartCopy"}},
		{"s3.sse-c.copy-source-wrong-key", "InvalidRequest", recorded, "35-copyobject-and-uploadpartcopy", "S3SOL-07", []string{"s3.CopyObject", "s3.UploadPartCopy"}},
		{"s3.sse-c.complete-headers", "checked-when-sent", inferred, "36-multipart-uploads", "S3SOL-08", []string{"s3.CompleteMultipartUpload", "s3.ListParts"}},
		{"s3.encryption.unblocked-read", "NONE-element", inferred, "42-blocking-and-unblocking", "S3SOL-10", []string{"s3.PutBucketEncryption", "s3.GetBucketEncryption"}},
		{"s3.encryption.blocked-types-omitted", "kept", inferred, "42-blocking-and-unblocking", "S3SOL-11", []string{"s3.PutBucketEncryption", "s3.DeleteBucketEncryption"}},
		{"s3.object-lock.retain-until-format", "milliseconds", inferred, "67-response-headers-on-get-and-head", "S3SOL-12", []string{"s3.PutObject", "s3.CopyObject", "s3.CreateMultipartUpload", "s3.PutObjectRetention", "s3.GetObject", "s3.HeadObject", "s3.GetObjectRetention", "s3.DeleteObject", "s3.DeleteObjects"}},
		{"s3.object-lock.default-years", "365-day", inferred, "53-default-retention-dating", "S3SOL-13", []string{"s3.PutObjectLockConfiguration", "s3.PutObject", "s3.CopyObject", "s3.CompleteMultipartUpload"}},
		{"s3.object-lock.checksum-scope", "PutObject-retention", inferred, "63-the-content-md5-or-checksum-requirement", "S3SOL-14, S3SOL-15", []string{"s3.PutObject", "s3.UploadPart"}},
		{"s3.object-lock.copy-default", "default-applied", inferred, "68-copyobject-does-not-carry-lock-state", "S3SOL-16", []string{"s3.CopyObject"}},
		{"s3.object-lock.headers-without-lock", "InvalidRequest", inferred, "62-header-validation-third-party-recorded-all-400-invalidargument", "S3SOL-17", []string{"s3.PutObject", "s3.CopyObject", "s3.CreateMultipartUpload"}},
		{"s3.object-lock.header-texts", "InvalidArgument", inferred, "62-header-validation-third-party-recorded-all-400-invalidargument", "S3SOL-18", []string{"s3.PutObject", "s3.CopyObject", "s3.CreateMultipartUpload", "s3.PutObjectRetention"}},
		{"s3.object-lock.mode-upgrade", "refused", inferred, "64-putobjectretention-and-getobjectretention", "S3SOL-19", []string{"s3.PutObjectRetention"}},
		{"s3.object-lock.date-without-mode", "MalformedXML", inferred, "64-putobjectretention-and-getobjectretention", "S3SOL-20", []string{"s3.PutObjectRetention"}},
		{"s3.object-lock.cleared-reads", "404-and-OFF", inferred, "64-putobjectretention-and-getobjectretention", "S3SOL-21", []string{"s3.GetObjectRetention", "s3.GetObjectLegalHold", "s3.GetObject", "s3.HeadObject"}},
		{"s3.object-lock.check-order", "lock-state-first", inferred, "65-putobjectlegalhold-and-getobjectlegalhold", "S3SOL-22", []string{"s3.PutObjectRetention", "s3.GetObjectRetention", "s3.PutObjectLegalHold", "s3.GetObjectLegalHold"}},
		{"s3.object-lock.bypass-without-lock", "refused", evidence.ThirdPartyRecorded, "73-the-bypass-header-on-a-bucket-without-object-lock", "S3SOL-23", []string{"s3.DeleteObject", "s3.DeleteObjects"}},
	}
	run(t, s3.Options{}, func(h *harness) {
		t := h.T
		entries := h.svc.Switches()
		// The switches the service declared before this unit, which stay
		// first: encryption after delete, cross-account access, public ACLs
		// at create, tag limits and lifecycle rule limits.
		// IAM enforcement's follow: the denial text, the refused
		// notification and the root's bucket-policy lockout.
		const earlier, enforcement = 5, 3
		if len(entries) != earlier+len(want)+enforcement {
			names := make([]string, len(entries))
			for i, e := range entries {
				names[i] = e.Name
			}
			t.Fatalf("Switches lists %d switches %v, want the earlier %d, these %d and IAM enforcement's %d", len(entries), names, earlier, len(want), enforcement)
		}
		for i, w := range want {
			e := entries[earlier+i]
			if e.Name != w.name {
				t.Errorf("switch %d is %s, want %s (s3-ssec-object-lock.md 11.4 order)", earlier+i, e.Name, w.name)
				continue
			}
			if e.Value != w.value || e.Label != w.label || e.Ref != "docs/research/s3-ssec-object-lock.md#"+w.anchor || e.Record != w.record || !slices.Equal(e.Ops, w.ops) {
				t.Errorf("switch %s = value %q label %s ref %s record %q ops %v; want %q %s %s %q %v",
					w.name, e.Value, e.Label, e.Ref, e.Record, e.Ops, w.value, w.label, "docs/research/s3-ssec-object-lock.md#"+w.anchor, w.record, w.ops)
			}
			reason, unsettled := e.Settles.(fidelity.NoScenario)
			if w.label == evidence.AWSRecorded {
				if want := w.record + " is recorded; the replayer skips S3's REST requests"; !unsettled || reason.Reason != want {
					t.Errorf("switch %s settles %#v, want NoScenario %q", w.name, e.Settles, want)
				}
				continue
			}
			if !unsettled || !strings.HasPrefix(reason.Reason, "unrecorded: "+w.record+" ") || !strings.HasSuffix(reason.Reason, "queued in docs/research/s3-ssec-object-lock.md 12") {
				t.Errorf("switch %s settles %#v, want NoScenario naming %s queued in s3-ssec-object-lock.md 12", w.name, e.Settles, w.record)
			}
		}
		after := entries[0]
		if after.Name != "s3.encryption.after-delete" || after.Value != "default-sse-s3" || after.Label != evidence.AWSRecorded ||
			after.Ref != "docs/research/s3-ssec-object-lock.md#2-what-the-repos-recordings-already-settled" ||
			after.Settles != (fidelity.NoScenario{Reason: "s3/R06/02-get-encryption-after-delete.json is recorded; the replayer skips S3's REST requests"}) {
			t.Errorf("s3.encryption.after-delete = %+v, want value default-sse-s3, aws-recorded, the section 2 ref and its settles unchanged", after)
		}
	})
}

// TestStateVersionTwo: the object version's lock state and SSE-C
// fingerprint are a new state format, version 2 (s3-ssec-object-lock.md
// 11.2), and before the first tagged release a snapshot section of another
// version is refused, not migrated (ARCHITECTURE.md 13), leaving the state
// as it was.
func TestStateVersionTwo(t *testing.T) {
	runObjects(t, func(h *harness) {
		t := h.T
		if v := h.svc.SchemaVersion(); v != 2 {
			t.Errorf("SchemaVersion = %d, want 2", v)
		}
		h.createBucket("kept")
		sections, err := h.svc.Export()
		if err != nil {
			t.Fatalf("Export: %v", err)
		}
		old := make([]store.Section, len(sections))
		for i, s := range sections {
			s.Version = 1
			old[i] = s
		}
		ierr := h.svc.Import(old)
		var mismatch *store.VersionMismatchError
		if !errors.As(ierr, &mismatch) || mismatch.Section != 1 || mismatch.Running != 2 {
			t.Errorf("Import of version 1 sections = %v, want a VersionMismatchError of section 1 against 2", ierr)
		}
		if _, err := as(h, who{}, s3api.OpHeadBucket, &s3api.HeadBucketInput{Bucket: "kept"}); err != nil {
			t.Errorf("the refused import changed the state: HeadBucket = %s", err.ID.QueryCode)
		}
	})
}

// findObject returns the first JSON object under v that has every member
// of keys, or nil.
func findObject(v any, keys ...string) map[string]any {
	switch x := v.(type) {
	case map[string]any:
		has := true
		for _, k := range keys {
			if _, ok := x[k]; !ok {
				has = false
			}
		}
		if has {
			return x
		}
		for _, child := range x {
			if found := findObject(child, keys...); found != nil {
				return found
			}
		}
	case []any:
		for _, child := range x {
			if found := findObject(child, keys...); found != nil {
				return found
			}
		}
	}
	return nil
}

// TestSnapshotFormOfALockedSSECObject: a snapshot holds an SSE-C version's
// encryption as the tagged union {"kind":"sse-c","salt":...,"mac":...}, a
// 16-byte salt and HMAC-SHA256(salt, key), never the key (s3-ssec-object-lock.md
// 3.11, 11.2; ARCHITECTURE.md 13), and its lock state as
// "lock":{"legalHold":"ON","retention":{"mode":...,"retainUntil":...}} to
// the millisecond (switch s3.object-lock.retain-until-format).
func TestSnapshotFormOfALockedSSECObject(t *testing.T) {
	runObjects(t, func(h *harness) {
		t := h.T
		h.lockBucket("locked")
		h.allowSSEC("locked")
		until := h.Clock.Now().Wire().Add(24*time.Hour + 123456789)
		ok(h, overTLS, s3api.OpPutObject, &s3api.PutObjectInput{
			Bucket: "locked", Key: "k", Body: bytesBody("v"), ContentMD5: new(contentMD5("v")),
			SSECustomerAlgorithm: key1.alg(), SSECustomerKey: key1.k(), SSECustomerKeyMD5: key1.m(),
			ObjectLockMode: new(s3api.ObjectLockModeGovernance), ObjectLockRetainUntilDate: &until,
			ObjectLockLegalHoldStatus: new(s3api.ObjectLockLegalHoldStatusOn),
		})
		sections, err := h.svc.Export()
		if err != nil {
			t.Fatalf("Export: %v", err)
		}
		var enc, lock map[string]any
		for _, s := range sections {
			var doc any
			if err := json.Unmarshal(s.Data, &doc); err != nil {
				t.Fatalf("section %s is not JSON: %v", s.Path, err)
			}
			if found := findObject(doc, "encryption", "lock"); found != nil {
				enc, lock = objectOf(found["encryption"]), objectOf(found["lock"])
			}
		}
		if enc == nil || lock == nil {
			t.Fatal("no snapshot section holds an object version with encryption and lock members")
		}
		salt, serr := base64.StdEncoding.DecodeString(stringOf(enc["salt"]))
		mac, merr := base64.StdEncoding.DecodeString(stringOf(enc["mac"]))
		if enc["kind"] != "sse-c" || serr != nil || merr != nil || len(salt) != 16 || len(enc) != 3 {
			t.Fatalf("encryption = %v, want exactly kind sse-c, a 16-byte salt and a mac", enc)
		}
		want := hmac.New(sha256.New, salt)
		_, _ = want.Write(key1.raw)
		if !hmac.Equal(mac, want.Sum(nil)) {
			t.Errorf("mac = %x, want HMAC-SHA256(salt, key)", mac)
		}
		retention := objectOf(lock["retention"])
		if lock["legalHold"] != "ON" || retention == nil || retention["mode"] != "GOVERNANCE" {
			t.Fatalf("lock = %v, want legal hold ON and GOVERNANCE retention", lock)
		}
		stored, perr := time.Parse(time.RFC3339Nano, stringOf(retention["retainUntil"]))
		if perr != nil || !stored.Equal(until.Truncate(time.Millisecond)) {
			t.Errorf("retainUntil = %v, want %s", retention["retainUntil"], until.Truncate(time.Millisecond).Format(time.RFC3339Nano))
		}
	})
}

// stringOf is v when it is a JSON string, else "".
func stringOf(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// objectOf is v when it is a JSON object, else nil.
func objectOf(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return nil
}

// TestRetainUntilHeaderForm: S3's restXml dialect writes the retain-until
// date in GET and HEAD headers as ISO 8601 UTC with milliseconds always,
// as its bodies already do (switch s3.object-lock.retain-until-format;
// s3-ssec-object-lock.md 6.7), while the zero dialect keeps Smithy's
// date-time form without a fraction for whole seconds
// (https://smithy.io/2.0/spec/protocol-traits.html#timestampformat-trait).
func TestRetainUntilHeaderForm(t *testing.T) {
	run(t, s3.Options{}, func(h *harness) {
		t := h.T
		h.follows("s3.object-lock.retain-until-format", "milliseconds")
		s3Codec := restxml.Codec{Dialect: func(ref.ServiceID) restxml.Dialect { return restxml.S3Dialect(s3api.IdentityInvalidArgument()) }}
		encodeWith := func(c restxml.Codec, op *registry.Operation, out smithy.Serializable) (http.Header, string) {
			resp, err := c.Encode(httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody), op, requestID, out, op.SuccessCode)
			if err != nil {
				t.Fatalf("encode %s: %s", op.ID, err.Message)
			}
			var b strings.Builder
			if resp.Body != nil {
				if _, rerr := io.Copy(&b, resp.Body); rerr != nil {
					t.Fatal(rerr)
				}
			}
			return resp.Header, b.String()
		}
		whole := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
		fraction := whole.Add(123 * time.Millisecond)
		for in, want := range map[time.Time]string{whole: "2030-01-01T00:00:00.000Z", fraction: "2030-01-01T00:00:00.123Z"} {
			head, _ := encodeWith(s3Codec, s3api.OpHeadObject.Descriptor(), &s3api.HeadObjectOutput{ObjectLockMode: new(s3api.ObjectLockModeGovernance), ObjectLockRetainUntilDate: &in})
			if got := head.Get("X-Amz-Object-Lock-Retain-Until-Date"); got != want {
				t.Errorf("HeadObject x-amz-object-lock-retain-until-date = %q, want %q", got, want)
			}
			get, _ := encodeWith(s3Codec, s3api.OpGetObject.Descriptor(), &s3api.GetObjectOutput{ObjectLockMode: new(s3api.ObjectLockModeGovernance), ObjectLockRetainUntilDate: &in})
			if got := get.Get("X-Amz-Object-Lock-Retain-Until-Date"); got != want {
				t.Errorf("GetObject x-amz-object-lock-retain-until-date = %q, want %q", got, want)
			}
			gov := s3api.ObjectLockRetentionModeGovernance
			_, body := encodeWith(s3Codec, s3api.OpGetObjectRetention.Descriptor(), &s3api.GetObjectRetentionOutput{Retention: &s3api.ObjectLockRetention{Mode: &gov, RetainUntilDate: &in}})
			if !strings.Contains(body, "<RetainUntilDate>"+want+"</RetainUntilDate>") {
				t.Errorf("GetObjectRetention body = %q, want RetainUntilDate %s", body, want)
			}
		}
		zeroDialect, _ := encodeWith(restxml.Codec{}, s3api.OpHeadObject.Descriptor(), &s3api.HeadObjectOutput{ObjectLockRetainUntilDate: &whole})
		if got := zeroDialect.Get("X-Amz-Object-Lock-Retain-Until-Date"); got != "2030-01-01T00:00:00Z" {
			t.Errorf("the zero dialect writes %q, want Smithy's %q unchanged", got, "2030-01-01T00:00:00Z")
		}
	})
}

// TestRetentionActions: PutObjectRetention needs s3:PutObjectRetention, and
// s3:BypassGovernanceRetention as well when it sends
// x-amz-bypass-governance-retention: true
// (https://docs.aws.amazon.com/AmazonS3/latest/userguide/object-lock-managing.html,
// "Bypassing governance mode"); the other lock operations need their own
// action (https://docs.aws.amazon.com/AmazonS3/latest/API/API_GetObjectRetention.html,
// API_PutObjectLegalHold.html, API_GetObjectLegalHold.html).
func TestRetentionActions(t *testing.T) {
	runObjects(t, func(h *harness) {
		t := h.T
		h.lockBucket("bkt")
		v := h.mustPutLocked("bkt", "k", "v", s3api.PutObjectInput{})
		until := h.Clock.Now().Wire().Add(time.Hour)
		if err := h.putRetention("bkt", "k", v, s3api.ObjectLockRetentionModeGovernance, until, nil); err != nil {
			t.Fatalf("PutObjectRetention: %s %q", err.ID.QueryCode, err.Message)
		}
		if uses := h.rootUses("s3"); !slices.Contains(uses, "s3:PutObjectRetention on arn:aws:s3:::bkt/*") || slices.Contains(uses, "s3:BypassGovernanceRetention on arn:aws:s3:::bkt/*") {
			t.Errorf("s3 uses after PutObjectRetention = %v, want s3:PutObjectRetention and no bypass", uses)
		}
		if err := h.putRetention("bkt", "k", v, s3api.ObjectLockRetentionModeGovernance, until.Add(time.Hour), new(true)); err != nil {
			t.Fatalf("PutObjectRetention with bypass: %s %q", err.ID.QueryCode, err.Message)
		}
		ok(h, who{}, s3api.OpGetObjectRetention, &s3api.GetObjectRetentionInput{Bucket: "bkt", Key: "k", VersionId: v})
		if err := h.putHold("bkt", "k", v, s3api.ObjectLockLegalHoldStatusOff); err != nil {
			t.Fatalf("PutObjectLegalHold: %s %q", err.ID.QueryCode, err.Message)
		}
		ok(h, who{}, s3api.OpGetObjectLegalHold, &s3api.GetObjectLegalHoldInput{Bucket: "bkt", Key: "k", VersionId: v})
		uses := h.rootUses("s3")
		for _, action := range []string{"s3:BypassGovernanceRetention", "s3:GetObjectRetention", "s3:PutObjectLegalHold", "s3:GetObjectLegalHold"} {
			if !slices.Contains(uses, action+" on arn:aws:s3:::bkt/*") {
				t.Errorf("s3 uses = %v, want %s on the object", uses, action)
			}
		}
	})
}
