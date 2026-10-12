package s3_test

import (
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/protocol/restxml"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/service/s3"
)

// encoded is an output as the restXml codec answers a GET with it.
type encoded struct {
	header http.Header
	body   string
}

// encode renders out, op's output, as the restXml codec answers a GET.
func encode(t *testing.T, op *registry.Operation, out smithy.Serializable) encoded {
	t.Helper()
	resp, err := restxml.Codec{}.Encode(httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody), op, requestID, out, op.SuccessCode)
	if err != nil {
		t.Fatalf("encode %s: %s", op.ID, err.Message)
	}
	body, rerr := io.ReadAll(resp.Body)
	if rerr != nil {
		t.Fatal(rerr)
	}
	return encoded{header: resp.Header, body: string(body)}
}

const (
	declaration = `<?xml version="1.0" encoding="UTF-8"?>` + "\n"
	s3Namespace = `xmlns="http://s3.amazonaws.com/doc/2006-03-01/"`
)

// TestWireForms checks the answers Terraform's reads depend on as the
// client receives them (tf-storage.md 2.2 and 2.6).
func TestWireForms(t *testing.T) {
	run(t, s3.Options{}, func(h *harness) {
		h.createBucket("wired")

		loc := encode(h.T, s3api.OpGetBucketLocation.Descriptor(), ok(h, who{}, s3api.OpGetBucketLocation, &s3api.GetBucketLocationInput{Bucket: "wired"}))
		if want := declaration + "<LocationConstraint " + s3Namespace + "></LocationConstraint>"; loc.body != want {
			t.Errorf("GetBucketLocation of a us-east-1 bucket = %q, want the empty element %q", loc.body, want)
		}

		head := encode(h.T, s3api.OpHeadBucket.Descriptor(), ok(h, who{}, s3api.OpHeadBucket, &s3api.HeadBucketInput{Bucket: "wired"}))
		if head.header.Get("X-Amz-Bucket-Region") != "us-east-1" || head.body != "" {
			t.Errorf("HeadBucket = %v %q, want x-amz-bucket-region us-east-1 and no body", head.header, head.body)
		}

		versioning := encode(h.T, s3api.OpGetBucketVersioning.Descriptor(), ok(h, who{}, s3api.OpGetBucketVersioning, &s3api.GetBucketVersioningInput{Bucket: "wired"}))
		if want := declaration + "<VersioningConfiguration " + s3Namespace + "></VersioningConfiguration>"; versioning.body != want {
			t.Errorf("GetBucketVersioning of a new bucket = %q, want %q", versioning.body, want)
		}

		acl := encode(h.T, s3api.OpGetBucketAcl.Descriptor(), ok(h, who{}, s3api.OpGetBucketAcl, &s3api.GetBucketAclInput{Bucket: "wired"}))
		if !strings.Contains(acl.body, `xsi:type="CanonicalUser"`) || !strings.Contains(acl.body, "<Permission>FULL_CONTROL</Permission>") {
			t.Errorf("GetBucketAcl = %q, want a CanonicalUser grantee with FULL_CONTROL", acl.body)
		}

		ok(h, who{}, s3api.OpPutBucketLifecycleConfiguration, &s3api.PutBucketLifecycleConfigurationInput{Bucket: "wired", LifecycleConfiguration: &s3api.BucketLifecycleConfiguration{
			Rules: []s3api.LifecycleRule{{ID: new("r"), Status: s3api.ExpirationStatusEnabled, Filter: &s3api.LifecycleRuleFilter{}}},
		}})
		lc := encode(h.T, s3api.OpGetBucketLifecycleConfiguration.Descriptor(),
			ok(h, who{}, s3api.OpGetBucketLifecycleConfiguration, &s3api.GetBucketLifecycleConfigurationInput{Bucket: "wired"}))
		if want := declaration + "<LifecycleConfiguration " + s3Namespace + "><Rule><ID>r</ID><Filter></Filter><Status>Enabled</Status></Rule></LifecycleConfiguration>"; lc.body != want {
			t.Errorf("GetBucketLifecycleConfiguration = %q, want the rule as sent %q", lc.body, want)
		}
		if got := lc.header.Get("X-Amz-Transition-Default-Minimum-Object-Size"); got != "all_storage_classes_128K" {
			t.Errorf("x-amz-transition-default-minimum-object-size = %q, want all_storage_classes_128K", got)
		}
	})
}

// TestPutStatusesRecordedAgainstAWS checks the Puts real S3 answers with
// 204 although the model says 200 (s3.md 4).
// TestListBucketsWithNoMatchSendsAnEmptyBucketsElement: AWS sends the
// Buckets wrapper even when nothing matches, which SDKs read as an empty
// list rather than an absent member (REC LocalStack
// test_s3_list_operations.snapshot.json, TestS3ListBuckets
// list-objects-by-prefix-empty: "Buckets": []).
func TestListBucketsWithNoMatchSendsAnEmptyBucketsElement(t *testing.T) {
	run(t, s3.Options{}, func(h *harness) {
		h.createBucket("listed")
		for name, w := range map[string]struct {
			who who
			in  s3api.ListBucketsInput
		}{
			"an account with no buckets": {who: who{account: "999999999999"}},
			"a prefix nothing matches":   {in: s3api.ListBucketsInput{Prefix: new("absent")}},
		} {
			got := encode(h.T, s3api.OpListBuckets.Descriptor(), ok(h, w.who, s3api.OpListBuckets, &w.in))
			if !strings.Contains(got.body, "<Buckets></Buckets>") {
				t.Errorf("ListBuckets for %s = %q, want an empty <Buckets></Buckets>", name, got.body)
			}
		}
	})
}

// TestObjectMetadataNamesGoOutLowerCase: S3 stores user metadata names in
// lower case and sends them as x-amz-meta-<name>, never in Go's canonical
// header case, because SDKs take the name after the prefix verbatim
// (s3.md 5.4).
func TestObjectMetadataNamesGoOutLowerCase(t *testing.T) {
	runObjects(t, func(h *harness) {
		h.createBucket("meta")
		ok(h, who{}, s3api.OpPutObject, &s3api.PutObjectInput{
			Bucket: "meta", Key: "k", Body: bytesBody("x"),
			Metadata: map[string]string{"mixed-case": "v1", "under_score": "v2"},
		})
		want := map[string]string{"x-amz-meta-mixed-case": "v1", "x-amz-meta-under_score": "v2"}
		check := func(op string, header http.Header) {
			got := map[string]string{}
			for name, values := range header {
				if strings.HasPrefix(strings.ToLower(name), "x-amz-meta-") {
					got[name] = strings.Join(values, ",")
				}
			}
			if !maps.Equal(got, want) {
				t.Errorf("%s metadata headers = %v, want exactly %v", op, got, want)
			}
		}
		check("HeadObject", encode(h.T, s3api.OpHeadObject.Descriptor(),
			ok(h, who{}, s3api.OpHeadObject, &s3api.HeadObjectInput{Bucket: "meta", Key: "k"})).header)
		check("GetObject", encode(h.T, s3api.OpGetObject.Descriptor(),
			ok(h, who{}, s3api.OpGetObject, &s3api.GetObjectInput{Bucket: "meta", Key: "k"})).header)
	})
}

func TestPutStatusesRecordedAgainstAWS(t *testing.T) {
	for _, op := range []*registry.Operation{
		s3api.OpPutBucketPolicy.Descriptor(), s3api.OpPutBucketTagging.Descriptor(),
		s3api.OpPutBucketMetricsConfiguration.Descriptor(), s3api.OpPutBucketInventoryConfiguration.Descriptor(),
		s3api.OpPutBucketAnalyticsConfiguration.Descriptor(), s3api.OpPutBucketIntelligentTieringConfiguration.Descriptor(),
		s3api.OpDeleteBucket.Descriptor(), s3api.OpDeleteBucketCors.Descriptor(),
	} {
		if op.SuccessCode != http.StatusNoContent {
			t.Errorf("%s answers %d, want 204", op.ID, op.SuccessCode)
		}
	}
	for _, op := range []*registry.Operation{s3api.OpPutBucketVersioning.Descriptor(), s3api.OpPutBucketEncryption.Descriptor(), s3api.OpPutBucketLifecycleConfiguration.Descriptor()} {
		if op.SuccessCode != http.StatusOK {
			t.Errorf("%s answers %d, want 200", op.ID, op.SuccessCode)
		}
	}
}
