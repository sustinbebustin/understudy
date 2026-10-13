package edge

import (
	"testing"

	"github.com/sustinbebustin/understudy/internal/aws"
)

// Regional S3 hosts address S3 in both styles (ADR 0013): a host equal to
// s3.<region>.<base> is path style, and a label in front of it is the
// bucket, for every configured base. A region name Regions refuses makes
// the regional form an ordinary dotted label.
func TestVirtualHostBucketReadsRegionalHosts(t *testing.T) {
	h := &Handler{opts: Options{S3BaseDomains: []string{"localhost", "s3.localhost", "s3.dev.localhost"}, Regions: aws.KnownRegions()}}
	cases := []struct {
		host    string
		bucket  string
		virtual bool
	}{
		{"s3.us-west-2.localhost", "", false},
		{"photos.s3.us-west-2.localhost", "photos", true},
		{"my.dotted.bucket.s3.us-west-2.localhost", "my.dotted.bucket", true},
		{"photos.s3.localhost", "photos", true},
		{"photos.localhost", "photos", true},
		{"s3.eu-central-1.dev.localhost", "", false},
		{"photos.s3.eu-central-1.dev.localhost", "photos", true},
		{"photos.s3.mars-north-1.localhost", "photos.s3.mars-north-1", true},
		{"localhost", "", false},
	}
	for _, c := range cases {
		cl := &claimer{h: h, host: c.host}
		bucket, virtual := cl.virtualHostBucket()
		if bucket != c.bucket || virtual != c.virtual {
			t.Errorf("virtualHostBucket(%s) = %q, %v; want %q, %v", c.host, bucket, virtual, c.bucket, c.virtual)
		}
	}
}
