package s3api

import (
	"net/http"
	"net/url"
	"testing"

	"github.com/sustinbebustin/understudy/internal/registry"
)

// TestRoutesDispatchTheS3Table sends the request lines the Go SDK writes
// (x-id literals, subresources as "key=") and the ones botocore writes
// (bare subresource keys, no x-id) and expects the operation of
// wire-rest.md 3.2 for both (wire-rest.md Implications 2). Paths are
// path-style; the edge maps virtual-hosted requests onto them.
func TestRoutesDispatchTheS3Table(t *testing.T) {
	if _, err := registry.New(Service()); err != nil {
		t.Fatal(err)
	}
	copySource := http.Header{"X-Amz-Copy-Source": {"/src/key"}}
	tests := []struct {
		method, target string
		header         http.Header
		want           string
	}{
		{"GET", "/", nil, "ListBuckets"},
		{"GET", "/?x-id=ListDirectoryBuckets", nil, "ListDirectoryBuckets"},
		{"GET", "/bucket", nil, "ListObjects"},
		{"GET", "/bucket?list-type=2", nil, "ListObjectsV2"},
		{"GET", "/bucket?list-type=2&x-id=ListObjectsV2", nil, "ListObjectsV2"},
		{"GET", "/bucket?location", nil, "GetBucketLocation"},
		{"GET", "/bucket?location=", nil, "GetBucketLocation"},
		{"GET", "/bucket?versions", nil, "ListObjectVersions"},
		{"GET", "/bucket?uploads", nil, "ListMultipartUploads"},
		{"GET", "/bucket?analytics", nil, "ListBucketAnalyticsConfigurations"},
		{"GET", "/bucket?analytics&id=a1", nil, "GetBucketAnalyticsConfiguration"},
		{"GET", "/bucket?analytics=&id=a1&x-id=GetBucketAnalyticsConfiguration", nil, "GetBucketAnalyticsConfiguration"},
		{"PUT", "/bucket", nil, "CreateBucket"},
		{"PUT", "/bucket?tagging", nil, "PutBucketTagging"},
		{"PUT", "/bucket?tagging=", nil, "PutBucketTagging"},
		{"DELETE", "/bucket", nil, "DeleteBucket"},
		{"DELETE", "/bucket?tagging", nil, "DeleteBucketTagging"},
		{"POST", "/bucket?delete", nil, "DeleteObjects"},
		{"POST", "/bucket?delete=&x-id=DeleteObjects", nil, "DeleteObjects"},
		{"HEAD", "/bucket", nil, "HeadBucket"},
		{"GET", "/bucket/key", nil, "GetObject"},
		{"GET", "/bucket/key?x-id=GetObject", nil, "GetObject"},
		{"GET", "/bucket/dir/sub/key.txt", nil, "GetObject"},
		{"GET", "/bucket/dir/", nil, "GetObject"},
		{"GET", "/bucket/a%2Fb", nil, "GetObject"},
		{"GET", "/bucket/key?tagging", nil, "GetObjectTagging"},
		{"GET", "/bucket/key?uploadId=u1", nil, "ListParts"},
		{"HEAD", "/bucket/key", nil, "HeadObject"},
		{"PUT", "/bucket/key", nil, "PutObject"},
		{"PUT", "/bucket/key?x-id=PutObject", nil, "PutObject"},
		{"PUT", "/bucket/key", copySource, "CopyObject"},
		{"PUT", "/bucket/key?partNumber=1&uploadId=u1", nil, "UploadPart"},
		{"PUT", "/bucket/key?partNumber=1&uploadId=u1&x-id=UploadPart", nil, "UploadPart"},
		{"PUT", "/bucket/key?partNumber=1&uploadId=u1", copySource, "UploadPartCopy"},
		{"PUT", "/bucket/key?tagging", nil, "PutObjectTagging"},
		{"POST", "/bucket/key?uploads", nil, "CreateMultipartUpload"},
		{"POST", "/bucket/key?uploads=&x-id=CreateMultipartUpload", nil, "CreateMultipartUpload"},
		{"POST", "/bucket/key?uploadId=u1", nil, "CompleteMultipartUpload"},
		{"POST", "/bucket/key?restore", nil, "RestoreObject"},
		{"DELETE", "/bucket/key", nil, "DeleteObject"},
		{"DELETE", "/bucket/key?uploadId=u1", nil, "AbortMultipartUpload"},
		{"DELETE", "/bucket/key?tagging", nil, "DeleteObjectTagging"},
	}
	for _, tt := range tests {
		t.Run(tt.method+" "+tt.target, func(t *testing.T) {
			u, err := url.Parse(tt.target)
			if err != nil {
				t.Fatal(err)
			}
			r, ok := Service().Routes.Match(tt.method, u.EscapedPath(), u.Query(), tt.header)
			if !ok {
				t.Fatalf("no route; want %s", tt.want)
			}
			if r.Operation.ID.Name != tt.want {
				t.Fatalf("routed to %s, want %s", r.Operation.ID.Name, tt.want)
			}
		})
	}
}
