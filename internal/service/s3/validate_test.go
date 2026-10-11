package s3

import (
	"net/http"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/clock"
)

// bucketName draws a name the naming rules accept: dot-separated labels of
// lowercase letters, digits and inner hyphens, 3 to 63 characters, never
// four numbers and never a reserved prefix or suffix.
func bucketName() *rapid.Generator[string] {
	label := rapid.StringMatching(`[a-z0-9]([a-z0-9-]{0,8}[a-z0-9])?`)
	return rapid.Custom(func(t *rapid.T) string {
		labels := rapid.SliceOfN(label, 1, 4).Draw(t, "labels")
		name := strings.Join(labels, ".")
		for len(name) < minBucketName {
			name += "a"
		}
		if len(name) > maxBucketName {
			name = strings.TrimRight(name[:maxBucketName], ".-")
		}
		return "b" + name[1:]
	}).Filter(func(name string) bool {
		for _, s := range append(reservedSuffixes(), accountRegionalSuffix) {
			if strings.HasSuffix(name, s) {
				return false
			}
		}
		return len(name) >= minBucketName
	})
}

func TestValidBucketNamesAreAccepted(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		name := bucketName().Draw(t, "name")
		if !validBucketName(name, nil) {
			t.Fatalf("validBucketName(%q) = false, want true", name)
		}
	})
}

func TestBucketNamesBreakingARuleAreRefused(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		name := bucketName().Draw(t, "name")
		broken := rapid.SampledFrom([]func(string) string{
			func(n string) string { return strings.ToUpper(n[:1]) + n[1:] },
			func(n string) string { return n[:1] + "_" + n[1:] },
			func(n string) string { return n + "-" },
			func(n string) string { return "." + n },
			func(n string) string { return n[:1] + ".." + n[1:] },
			func(n string) string { return "xn--" + n },
			func(n string) string { return n + "-s3alias" },
			func(n string) string { return n + strings.Repeat("a", maxBucketName) },
		}).Draw(t, "break")(name)
		if validBucketName(broken, nil) {
			t.Fatalf("validBucketName(%q) = true, want false", broken)
		}
	})
}

func TestBucketNameRuleExamples(t *testing.T) {
	regional := s3api.BucketNamespaceAccountRegional
	cases := []struct {
		name      string
		namespace *s3api.BucketNamespace
		want      bool
	}{
		{"my-bucket", nil, true},
		{"a.b.c", nil, true},
		{"amzn-s3-demo-bucket", nil, true},
		{"ab", nil, false},
		{"192.168.5.4", nil, false},
		{"192.168.5.4x", nil, true},
		{"sthree-bucket", nil, false},
		{"bucket--ol-s3", nil, false},
		{"bucket--x-s3", nil, false},
		{"bucket--table-s3", nil, false},
		{"bucket.mrap", nil, false},
		{"prefix-000000000000-us-east-1-an", nil, false},
		{"prefix-000000000000-us-east-1-an", &regional, true},
	}
	for _, tc := range cases {
		if got := validBucketName(tc.name, tc.namespace); got != tc.want {
			t.Errorf("validBucketName(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestBucketARNRoundTrips(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		name := bucketName().Draw(t, "name")
		got, ok := bucketOfARN(bucketARN(name).String())
		if !ok || got != name {
			t.Fatalf("bucketOfARN(bucketARN(%q)) = %q, %v", name, got, ok)
		}
	})
	for _, s := range []string{"arn:aws:s3:::bucket/key", "arn:aws:s3:us-east-1:000000000000:accesspoint/ap", "arn:aws:sqs:::bucket", "bucket"} {
		if _, ok := bucketOfARN(s); ok {
			t.Errorf("bucketOfARN(%q) accepted an ARN that names no general purpose bucket", s)
		}
	}
}

func TestCrossRegionMatrix(t *testing.T) {
	region := func(name string) aws.Region {
		r, err := aws.KnownRegions().Parse(name)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	home := Home{OwnerAccount: aws.DefaultAccount(), Region: region("us-west-2"), Created: clock.Instant{}}
	cases := map[string]struct {
		mode    CrossRegion
		src     aws.RegionSource
		status  int
		crossed string
	}{
		"none":                   {src: aws.None{}},
		"signed home":            {src: aws.CredentialScope{Region: region("us-west-2")}},
		"signed elsewhere":       {src: aws.CredentialScope{Region: region("eu-west-1")}, status: http.StatusBadRequest},
		"host home":              {src: aws.HostRegion{Region: region("us-west-2")}},
		"host elsewhere":         {src: aws.HostRegion{Region: region("eu-west-1")}, status: http.StatusMovedPermanently},
		"lenient none":           {mode: CrossRegionLenient, src: aws.None{}},
		"lenient signed":         {mode: CrossRegionLenient, src: aws.CredentialScope{Region: region("eu-west-1")}, crossed: "eu-west-1"},
		"lenient host":           {mode: CrossRegionLenient, src: aws.HostRegion{Region: region("eu-west-1")}, crossed: "eu-west-1"},
		"lenient signed at home": {mode: CrossRegionLenient, src: aws.CredentialScope{Region: region("us-west-2")}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			crossed, err := crossRegion(tc.mode, tc.src, "b.s3.eu-west-1.localhost:4566", "b", home)
			status := 0
			if err != nil {
				status = err.ID.Status
			}
			if status != tc.status || crossed.String() != tc.crossed {
				t.Fatalf("got status %d crossed %q, want %d %q", status, crossed, tc.status, tc.crossed)
			}
		})
	}
}

func TestHostRegion(t *testing.T) {
	cases := []struct{ host, bucket, want string }{
		{"s3.us-west-2.localhost:4566", "bucket", "us-west-2"},
		{"bucket.s3.eu-west-1.localhost", "bucket", "eu-west-1"},
		{"s3.us-west-2.amazonaws.com", "bucket", "us-west-2"},
		{"localhost:4566", "bucket", ""},
		{"bucket.s3.localhost:4566", "bucket", ""},
		{"s3.not-a-region.localhost:4566", "bucket", ""},
		{"000000000000.localhost:4566", "", ""},
		{"bucket.s3.us-east-1.localhost:4566", "bucket", "us-east-1"},
		// A dotted bucket name holding s3.<region> asserts nothing.
		{"logs.s3.eu-west-1.archive.localhost:4566", "logs.s3.eu-west-1.archive", ""},
		{"logs.s3.eu-west-1.archive.s3.us-west-2.localhost", "logs.s3.eu-west-1.archive", "us-west-2"},
		{"s3.us-west-2.localhost", "logs.s3.eu-west-1.archive", "us-west-2"},
	}
	for _, tc := range cases {
		r, ok := hostRegion(tc.host, tc.bucket, aws.KnownRegions())
		if ok != (tc.want != "") || r.String() != tc.want {
			t.Errorf("hostRegion(%q, %q) = %q, %v; want %q", tc.host, tc.bucket, r, ok, tc.want)
		}
	}
}

// TestRedirectEndpoint: PermanentRedirect names the request's host with
// the region label after the bucket's own labels replaced, port kept.
func TestRedirectEndpoint(t *testing.T) {
	regions := aws.KnownRegions()
	from, err := regions.Parse("eu-west-1")
	if err != nil {
		t.Fatal(err)
	}
	home, err := regions.Parse("us-west-2")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct{ host, bucket, want string }{
		{"s3.eu-west-1.localhost:4566", "bucket", "s3.us-west-2.localhost:4566"},
		{"bucket.s3.eu-west-1.localhost:4566", "bucket", "bucket.s3.us-west-2.localhost:4566"},
		// A dotted bucket name holding s3.<region> keeps its labels.
		{"a.s3.eu-west-1.b.s3.eu-west-1.localhost", "a.s3.eu-west-1.b", "a.s3.eu-west-1.b.s3.us-west-2.localhost"},
		{"s3.eu-west-1.localhost", "a.s3.eu-west-1.b", "s3.us-west-2.localhost"},
	}
	for _, tc := range cases {
		if got := redirectEndpoint(tc.host, tc.bucket, from, home); got != tc.want {
			t.Errorf("redirectEndpoint(%q, %q) = %q, want %q", tc.host, tc.bucket, got, tc.want)
		}
	}
}
