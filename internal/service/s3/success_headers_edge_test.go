package s3_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
)

// Every ListObjectsV2 200 names the bucket's home region in
// x-amz-bucket-region, a header ListObjectsV2Output does not model, on a
// request signed for that same region: testdata/parity/s3/R06/07-list-objects-v2.json
// answers x-amz-bucket-region: us-east-2 to a request to s3.us-east-2 signed
// for us-east-2, as do R12/05-list-max-keys-0.json and
// R12/13-list-without-encoding.json.
func TestListObjectsV2NamesTheBucketRegion(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		t := k.T
		usEast2, err := aws.KnownRegions().Parse("us-east-2")
		if err != nil {
			t.Fatal(err)
		}
		e := newObjectEdge(k).signedFor(usEast2)
		create := "<CreateBucketConfiguration><LocationConstraint>us-east-2</LocationConstraint></CreateBucketConfiguration>"
		if rec := e.send(http.MethodPut, "/listed", strings.NewReader(create), int64(len(create)), nil); rec.Code != http.StatusOK {
			t.Fatalf("CreateBucket = %d %s", rec.Code, rec.Body)
		}
		rec := e.send(http.MethodGet, "/listed?list-type=2&encoding-type=url", strings.NewReader(""), 0, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("ListObjectsV2 = %d %s", rec.Code, rec.Body)
		}
		if got := rec.Header().Values("X-Amz-Bucket-Region"); len(got) != 1 || got[0] != "us-east-2" {
			t.Errorf("ListObjectsV2 x-amz-bucket-region = %q, want [us-east-2]", got)
		}
	}, kerneltest.Services(s3api.Service()))
}

// listingDenier is a step 8 Authorizer that denies s3:ListBucket and
// s3:GetBucketPolicy and allows every other action.
type listingDenier struct{}

func (listingDenier) Authorize(_ context.Context, _ *kernel.Call, q kernel.AuthzQuery) kernel.Verdict {
	for _, p := range q.Pairs {
		if a := p.Action.String(); a == "s3:ListBucket" || a == "s3:GetBucketPolicy" {
			return kernel.Denied{Message: "denied in this test"}
		}
	}
	return kernel.Allowed{}
}

// A ListObjectsV2 refused before the handler looks the bucket up still
// names an existing bucket's home region, as AWS's do: an IAM denial
// (testdata/parity/s3/IAM-ENF/12-list.json, and every capture under
// testdata/parity/s3/IAM-EVAL-18/) and the codec's refusal of a max-keys
// that is not an integer (testdata/parity/s3/R12/05-list-max-keys-abc.json),
// each x-amz-bucket-region: us-east-2 on a request signed for us-east-2. A
// refusal about a bucket that does not exist names no region (inferred:
// there is none), and no other operation's early refusals gain the header:
// ListObjects is unrecorded and takes the default, and a same-region
// GetBucketPolicy error carries none
// (testdata/parity/s3/R06/01-get-get-bucket-policy.json).
func TestEarlyListObjectsV2RefusalsNameTheBucketRegion(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		t := k.T
		usEast2, err := aws.KnownRegions().Parse("us-east-2")
		if err != nil {
			t.Fatal(err)
		}
		e := newObjectEdge(k).signedFor(usEast2)
		create := "<CreateBucketConfiguration><LocationConstraint>us-east-2</LocationConstraint></CreateBucketConfiguration>"
		if rec := e.send(http.MethodPut, "/listed", strings.NewReader(create), int64(len(create)), nil); rec.Code != http.StatusOK {
			t.Fatalf("CreateBucket = %d %s", rec.Code, rec.Body)
		}
		cases := []struct {
			name, target, code string
			status             int
			// region is the x-amz-bucket-region wanted; "" wants none.
			region string
		}{
			{"ListObjectsV2 denied", "/listed?list-type=2&encoding-type=url", "AccessDenied", http.StatusForbidden, "us-east-2"},
			{"ListObjectsV2 with max-keys=abc", "/listed?list-type=2&max-keys=abc", "InvalidArgument", http.StatusBadRequest, "us-east-2"},
			{"ListObjectsV2 with max-keys=abc on a missing bucket", "/absent?list-type=2&max-keys=abc", "InvalidArgument", http.StatusBadRequest, ""},
			{"ListObjectsV2 on a missing bucket", "/absent?list-type=2", "NoSuchBucket", http.StatusNotFound, ""},
			{"ListObjects denied", "/listed", "AccessDenied", http.StatusForbidden, ""},
			{"ListObjects with max-keys=abc", "/listed?max-keys=abc", "InvalidArgument", http.StatusBadRequest, ""},
			{"GetBucketPolicy denied", "/listed?policy", "AccessDenied", http.StatusForbidden, ""},
		}
		for _, c := range cases {
			rec := e.send(http.MethodGet, c.target, strings.NewReader(""), 0, nil)
			if rec.Code != c.status || !strings.Contains(rec.Body.String(), "<Code>"+c.code+"</Code>") {
				t.Errorf("%s = %d %s, want %d %s", c.name, rec.Code, rec.Body, c.status, c.code)
			}
			got := rec.Header().Values("X-Amz-Bucket-Region")
			switch {
			case c.region == "" && len(got) != 0:
				t.Errorf("%s: x-amz-bucket-region = %q, want none", c.name, got)
			case c.region != "" && (len(got) != 1 || got[0] != c.region):
				t.Errorf("%s: x-amz-bucket-region = %q, want [%s]", c.name, got, c.region)
			}
		}
	}, kerneltest.Services(s3api.Service()), kerneltest.Authorizer(listingDenier{}))
}

// A PutObject that stores a class other than STANDARD answers
// x-amz-storage-class with it, a header PutObjectOutput does not model; a
// STANDARD one answers none. Recorded: STANDARD_IA
// (testdata/parity/s3/R06/07-put-object-ia.json) and a put naming no class
// (testdata/parity/s3/R06/07-put-object.json). The other cases are
// inferred from the same rule.
func TestPutObjectNamesANonStandardStorageClass(t *testing.T) {
	cases := []struct {
		name string
		// class is the x-amz-storage-class the put sends; "" sends none.
		class string
		// want is the x-amz-storage-class answered; "" wants none.
		want string
	}{
		{name: "STANDARD_IA, recorded", class: "STANDARD_IA", want: "STANDARD_IA"},
		{name: "no class, recorded", class: "", want: ""},
		{name: "STANDARD named, inferred", class: "STANDARD", want: ""},
		{name: "ONEZONE_IA, inferred", class: "ONEZONE_IA", want: "ONEZONE_IA"},
		{name: "GLACIER, inferred", class: "GLACIER", want: "GLACIER"},
	}
	kerneltest.Run(t, func(k *kerneltest.K) {
		t := k.T
		e := newObjectEdge(k)
		if rec := e.send(http.MethodPut, "/classes", strings.NewReader(""), 0, nil); rec.Code != http.StatusOK {
			t.Fatalf("CreateBucket = %d %s", rec.Code, rec.Body)
		}
		for _, c := range cases {
			header := http.Header{}
			if c.class != "" {
				header.Set("X-Amz-Storage-Class", c.class)
			}
			rec := e.send(http.MethodPut, "/classes/o", strings.NewReader("object"), 6, header)
			if rec.Code != http.StatusOK {
				t.Fatalf("%s: PutObject = %d %s", c.name, rec.Code, rec.Body)
			}
			got := rec.Header().Values("X-Amz-Storage-Class")
			switch {
			case c.want == "" && len(got) != 0:
				t.Errorf("%s: x-amz-storage-class = %q, want none", c.name, got)
			case c.want != "" && (len(got) != 1 || got[0] != c.want):
				t.Errorf("%s: x-amz-storage-class = %q, want [%s]", c.name, got, c.want)
			}
		}
	}, kerneltest.Services(s3api.Service()))
}
