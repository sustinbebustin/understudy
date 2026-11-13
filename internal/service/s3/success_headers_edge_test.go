package s3_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/aws"
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
