package s3_test

import (
	"slices"
	"testing"

	"github.com/sustinbebustin/understudy/internal/access"
	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/fault"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/policydoc"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/service/s3"
)

// rootUses returns the default account root's recorded uses of service's
// actions as "action on resource", the resource "*" when there is none.
func (h *harness) rootUses(service string) []string {
	h.Helper()
	root, err := access.ParsePrincipal("arn:aws:iam::000000000000:root")
	if err != nil {
		h.Fatal(err)
	}
	uses := h.Access.Usage(access.Filter{Principal: root, Service: service})
	out := make([]string, 0, len(uses))
	for i := range uses {
		on := "*"
		if r, ok := uses[i].Resource.(access.OnResource); ok {
			on = r.ARN.String()
		}
		out = append(out, uses[i].Action.String()+" on "+on)
	}
	return out
}

// rootPolicy is the default account root's generated policy.
func (h *harness) rootPolicy() string {
	h.Helper()
	doc, ok := h.Access.Policy(access.Root{Account: h.Scope.Account})
	if !ok {
		h.Fatal("no policy recorded for the root")
	}
	return string(policydoc.Encode(doc))
}

// A plain PutObject needs s3:PutObject on the object and nothing else: no
// ACL, tagging or lock action, and no s3-object-lambda action, since no
// Object Lambda access point was touched. The policy grants it on every
// object of the bucket; the usage keeps the object's ARN.
func TestPutObjectRecordsPutObjectOnTheObject(t *testing.T) {
	runObjects(t, func(h *harness) {
		h.createBucket("bkt")
		h.put("bkt", "dir/k", "x")
		if got, want := h.rootUses("s3"), []string{"s3:CreateBucket on arn:aws:s3:::bkt", "s3:PutObject on arn:aws:s3:::bkt/dir/k"}; !slices.Equal(got, want) {
			t.Errorf("s3 uses = %v, want %v", got, want)
		}
		if got := h.rootUses("s3-object-lambda"); len(got) != 0 {
			t.Errorf("s3-object-lambda uses = %v, want none", got)
		}
		want := `{"Version":"2012-10-17","Statement":[` +
			`{"Effect":"Allow","Action":"s3:CreateBucket","Resource":"arn:aws:s3:::bkt"},` +
			`{"Effect":"Allow","Action":"s3:PutObject","Resource":"arn:aws:s3:::bkt/*"}]}`
		if got := h.rootPolicy(); got != want {
			t.Errorf("Policy =\n%s\nwant\n%s", got, want)
		}
	})
}

// x-amz-tagging on a PutObject needs s3:PutObjectTagging as well
// (PutObject, Permissions).
func TestPutObjectWithTagsRecordsPutObjectTagging(t *testing.T) {
	runObjects(t, func(h *harness) {
		h.createBucket("bkt")
		ok(h, who{}, s3api.OpPutObject, &s3api.PutObjectInput{Bucket: "bkt", Key: "k", Body: bytesBody("x"), Tagging: new("team=a")})
		want := []string{"s3:CreateBucket on arn:aws:s3:::bkt", "s3:PutObject on arn:aws:s3:::bkt/k", "s3:PutObjectTagging on arn:aws:s3:::bkt/k"}
		if got := h.rootUses("s3"); !slices.Equal(got, want) {
			t.Errorf("s3 uses = %v, want %v", got, want)
		}
	})
}

// A copy reads its source, s3:GetObject, and writes its destination,
// s3:PutObject; both objects are the call's resources, and a source version
// needs s3:GetObjectVersion instead.
func TestCopyObjectTouchesBothObjects(t *testing.T) {
	runObjects(t, func(h *harness) {
		h.createBucket("inbox")
		h.createBucket("outbox")
		h.put("inbox", "a", "x")
		h.Access.Reset()
		ok(h, who{}, s3api.OpCopyObject, &s3api.CopyObjectInput{Bucket: "outbox", Key: "b", CopySource: "inbox/a"})
		want := []string{
			"s3:GetObject on arn:aws:s3:::inbox/a", "s3:GetObject on arn:aws:s3:::outbox/b",
			"s3:PutObject on arn:aws:s3:::inbox/a", "s3:PutObject on arn:aws:s3:::outbox/b",
		}
		if got := h.rootUses("s3"); !slices.Equal(got, want) {
			t.Errorf("s3 uses = %v, want %v", got, want)
		}
		h.Access.Reset()
		_, _ = as(h, who{}, s3api.OpCopyObject, &s3api.CopyObjectInput{Bucket: "outbox", Key: "b", CopySource: "inbox/a?versionId=null"})
		if got := h.rootUses("s3"); !slices.Contains(got, "s3:GetObjectVersion on arn:aws:s3:::inbox/a") || slices.Contains(got, "s3:GetObject on arn:aws:s3:::inbox/a") {
			t.Errorf("s3 uses of a versioned copy = %v, want s3:GetObjectVersion and no s3:GetObject on the source", got)
		}
	})
}

// Deleting a version needs s3:DeleteObjectVersion; deleting the current
// object only s3:DeleteObject.
func TestDeleteObjectRecordsDeleteObjectVersionForAVersion(t *testing.T) {
	runObjects(t, func(h *harness) {
		h.createBucket("bkt")
		h.put("bkt", "k", "x")
		h.Access.Reset()
		ok(h, who{}, s3api.OpDeleteObject, &s3api.DeleteObjectInput{Bucket: "bkt", Key: "k"})
		if got, want := h.rootUses("s3"), []string{"s3:DeleteObject on arn:aws:s3:::bkt/k"}; !slices.Equal(got, want) {
			t.Errorf("s3 uses = %v, want %v", got, want)
		}
		h.Access.Reset()
		ok(h, who{}, s3api.OpDeleteObjects, &s3api.DeleteObjectsInput{Bucket: "bkt", Delete: s3api.Delete{Objects: []s3api.ObjectIdentifier{{Key: "k", VersionId: new("null")}, {Key: "j"}}}})
		want := []string{
			"s3:DeleteObject on arn:aws:s3:::bkt/j", "s3:DeleteObject on arn:aws:s3:::bkt/k",
			"s3:DeleteObjectVersion on arn:aws:s3:::bkt/j", "s3:DeleteObjectVersion on arn:aws:s3:::bkt/k",
		}
		if got := h.rootUses("s3"); !slices.Equal(got, want) {
			t.Errorf("DeleteObjects s3 uses = %v, want %v", got, want)
		}
	})
}

// Every bound S3 operation whose generated list holds more than one s3
// action decides which of them its calls use, so none records the whole
// list.
func TestEveryMultiActionOperationDeclaresItsActions(t *testing.T) {
	run(t, s3.Options{}, func(h *harness) {
		if got := kerneltest.UndeclaredActions(h.Kernel, s3api.Service()); len(got) != 0 {
			t.Errorf("operations without an actions decision: %v; declare them in declareAccess", got)
		}
	})
}

// GetObjectAttributes reads the object, s3:GetObject, without a version
// and s3:GetObjectVersion with one, never both (GetObjectAttributes,
// Permissions).
func TestGetObjectAttributesRecordsTheReadOfItsVersion(t *testing.T) {
	runObjects(t, func(h *harness) {
		h.createBucket("bkt")
		h.put("bkt", "k", "x")
		h.Access.Reset()
		attrs := []s3api.ObjectAttributes{s3api.ObjectAttributesEtag}
		ok(h, who{}, s3api.OpGetObjectAttributes, &s3api.GetObjectAttributesInput{Bucket: "bkt", Key: "k", ObjectAttributes: attrs})
		want := []string{"s3:GetObject on arn:aws:s3:::bkt/k", "s3:GetObjectAttributes on arn:aws:s3:::bkt/k"}
		if got := h.rootUses("s3"); !slices.Equal(got, want) {
			t.Errorf("unversioned s3 uses = %v, want %v", got, want)
		}
		h.Access.Reset()
		ok(h, who{}, s3api.OpGetObjectAttributes, &s3api.GetObjectAttributesInput{Bucket: "bkt", Key: "k", VersionId: new("null"), ObjectAttributes: attrs})
		want = []string{"s3:GetObjectAttributes on arn:aws:s3:::bkt/k", "s3:GetObjectVersion on arn:aws:s3:::bkt/k"}
		if got := h.rootUses("s3"); !slices.Equal(got, want) {
			t.Errorf("versioned s3 uses = %v, want %v", got, want)
		}
	})
}

// PutBucketReplication passes the replication role: the record holds
// iam:PassRole on that role (PutBucketReplication, Permissions).
func TestPutBucketReplicationRecordsPassRoleOnTheRole(t *testing.T) {
	run(t, s3.Options{}, func(h *harness) {
		h.createBucket("src")
		h.enableVersioning("src")
		const role = "arn:aws:iam::000000000000:role/replicator"
		ok(h, who{}, s3api.OpPutBucketReplication, &s3api.PutBucketReplicationInput{Bucket: "src", ReplicationConfiguration: s3api.ReplicationConfiguration{
			Role:  role,
			Rules: []s3api.ReplicationRule{{Status: s3api.ReplicationRuleStatusEnabled, Prefix: new(""), Destination: s3api.Destination{Bucket: "arn:aws:s3:::dst"}}},
		}})
		if got, want := h.rootUses("iam"), []string{"iam:PassRole on " + role}; !slices.Equal(got, want) {
			t.Errorf("iam uses = %v, want %v", got, want)
		}
	})
}

// A canned ACL on CreateBucket needs s3:PutBucketAcl (CreateBucket,
// Permissions).
func TestCreateBucketWithAnACLRecordsPutBucketAcl(t *testing.T) {
	run(t, s3.Options{}, func(h *harness) {
		ok(h, who{}, s3api.OpCreateBucket, &s3api.CreateBucketInput{Bucket: "bkt", ACL: new(s3api.BucketCannedACL("private"))})
		want := []string{"s3:CreateBucket on arn:aws:s3:::bkt", "s3:PutBucketAcl on arn:aws:s3:::bkt"}
		if got := h.rootUses("s3"); !slices.Equal(got, want) {
			t.Errorf("s3 uses = %v, want %v", got, want)
		}
	})
}

// An object call's resource is its object: the journal lists the object
// ARN, and a fault rule scoped to the bucket's objects matches it.
func TestObjectCallsTouchTheObjectARN(t *testing.T) {
	runObjects(t, func(h *harness) {
		h.createBucket("bkt")
		h.put("bkt", "k", "x")
		calls := h.Journal.Query(journal.Filter{Kind: journal.KindAPICall, Operation: s3api.OpPutObject.Descriptor().ID}).Events
		if len(calls) != 1 {
			t.Fatalf("journal holds %d PutObject calls, want 1", len(calls))
		}
		call, isCall := calls[0].Detail.(journal.APICall)
		if !isCall || len(call.Resources) != 1 || call.Resources[0].String() != "arn:aws:s3:::bkt/k" {
			t.Errorf("journaled PutObject = %+v, want the resource arn:aws:s3:::bkt/k", calls[0].Detail)
		}

		if _, err := h.Faults.Add(fault.Rule{
			Match:   fault.Match{Operations: []ref.OperationID{s3api.OpPutObject.Descriptor().ID}, Resource: fault.NewARNGlob("arn:aws:s3:::bkt/*")},
			Phase:   fault.Request,
			Action:  fault.Throttle{},
			Trigger: fault.NextN{N: 1},
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := invoke(h, s3api.OpPutObject, &s3api.PutObjectInput{Bucket: "bkt", Key: "k", Body: bytesBody("y")}); err == nil {
			t.Error("PutObject on bkt/k answered, want the fault rule on arn:aws:s3:::bkt/* to throttle it")
		}
	})
}
