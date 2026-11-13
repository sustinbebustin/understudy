package s3_test

import (
	"net/http"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/service/s3"
	"github.com/sustinbebustin/understudy/internal/store"
)

const otherAccount = "111122223333"

func TestCreateBucketNameTakenByAnotherAccountIsBucketAlreadyExists(t *testing.T) {
	run(t, s3.Options{}, func(h *harness) {
		h.createBucket("shared-name")
		_, err := as(h, who{account: otherAccount}, s3api.OpCreateBucket, &s3api.CreateBucketInput{Bucket: "shared-name"})
		wantCode(h.T, err, "BucketAlreadyExists", http.StatusConflict)
		got := render(h.T, err)
		want := errorBody("BucketAlreadyExists", "The requested bucket name is not available. The bucket namespace is shared by all users of the system. Please select a different name and try again.",
			"BucketName", "shared-name")
		if got.body != want {
			t.Fatalf("body = %s, want %s", got.body, want)
		}
	})
}

func TestConcurrentCreatesOfOneNameYieldOneBucket(t *testing.T) {
	run(t, s3.Options{}, func(h *harness) {
		accounts := []string{"111111111111", "222222222222", "333333333333", "444444444444"}
		errs := make(chan *awserr.Error, len(accounts))
		var wg sync.WaitGroup
		for _, a := range accounts {
			wg.Go(func() {
				_, err := as(h, who{account: a}, s3api.OpCreateBucket, &s3api.CreateBucketInput{Bucket: "contended"})
				errs <- err
			})
		}
		wg.Wait()
		close(errs)
		created := 0
		for err := range errs {
			switch {
			case err == nil:
				created++
			case err.ID.QueryCode != "BucketAlreadyExists":
				t.Fatalf("got %s, want success or BucketAlreadyExists", err.ID.QueryCode)
			}
		}
		if created != 1 {
			t.Fatalf("%d creates succeeded, want exactly 1", created)
		}
		var owners int
		for _, a := range accounts {
			out := ok(h, who{account: a}, s3api.OpListBuckets, &s3api.ListBucketsInput{})
			owners += len(out.Buckets)
		}
		if owners != 1 {
			t.Fatalf("%d accounts list the bucket, want 1", owners)
		}
	})
}

func TestCreateBucketAgainByItsOwner(t *testing.T) {
	constraint := func(region string) *s3api.CreateBucketConfiguration {
		return &s3api.CreateBucketConfiguration{LocationConstraint: new(s3api.BucketLocationConstraint(region))}
	}
	cases := map[string]struct {
		// home is the existing bucket's region; empty is us-east-1.
		home string
		w    who
		in   s3api.CreateBucketInput
		code string
	}{
		"us-east-1 without tags succeeds": {w: who{}, in: s3api.CreateBucketInput{Bucket: "mine"}},
		"us-east-1 with tags": {w: who{}, in: s3api.CreateBucketInput{Bucket: "mine", CreateBucketConfiguration: &s3api.CreateBucketConfiguration{
			Tags: []s3api.Tag{{Key: "k", Value: "v"}},
		}}, code: "BucketAlreadyOwnedByYou"},
		"another region":                    {w: signed("us-west-2"), in: s3api.CreateBucketInput{Bucket: "mine", CreateBucketConfiguration: constraint("us-west-2")}, code: "BucketAlreadyOwnedByYou"},
		"us-east-1 naming another region":   {w: who{}, in: s3api.CreateBucketInput{Bucket: "mine", CreateBucketConfiguration: constraint("us-west-2")}, code: "BucketAlreadyOwnedByYou"},
		"a eu-west-1 bucket from us-east-1": {home: "eu-west-1", w: who{}, in: s3api.CreateBucketInput{Bucket: "mine"}, code: "BucketAlreadyOwnedByYou"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			run(t, s3.Options{}, func(h *harness) {
				if tc.home == "" {
					h.createBucket("mine")
				} else {
					ok(h, signed(tc.home), s3api.OpCreateBucket, &s3api.CreateBucketInput{Bucket: "mine", CreateBucketConfiguration: constraint(tc.home)})
				}
				_, err := as(h, tc.w, s3api.OpCreateBucket, &tc.in)
				if tc.code == "" {
					if err != nil {
						t.Fatalf("got %s, want success", err.ID.QueryCode)
					}
					return
				}
				wantCode(h.T, err, tc.code, http.StatusConflict)
			})
		})
	}
}

func TestCreateBucketLocationConstraints(t *testing.T) {
	cases := map[string]struct {
		endpoint   string
		constraint string
		code       string
		message    string
		// home and location are the bucket's region and GetBucketLocation's
		// answer on success.
		home, location string
		// host is the request's Host, localhost:4566 when empty; created
		// is CreateBucket's Location header on success.
		host, created string
	}{
		"us-east-1 without a constraint":  {endpoint: "us-east-1", home: "us-east-1", created: "/located"},
		"us-east-1 naming another region": {endpoint: "us-east-1", constraint: "us-west-2", home: "us-west-2", location: "us-west-2", created: "http://located.localhost:4566/"},
		"virtual-hosted naming another region": {
			endpoint: "us-east-1", host: "located.localhost:4566", constraint: "us-west-2",
			home: "us-west-2", location: "us-west-2", created: "http://located.localhost:4566/",
		},
		"us-east-1 with EU":                  {endpoint: "us-east-1", constraint: "EU", home: "eu-west-1", location: "EU"},
		"us-east-1 naming us-east-1":         {endpoint: "us-east-1", constraint: "us-east-1", code: "InvalidLocationConstraint", message: "The specified location-constraint is not valid"},
		"us-east-1 naming an unknown value":  {endpoint: "us-east-1", constraint: "bar", code: "InvalidLocationConstraint", message: "The specified location-constraint is not valid"},
		"regional without a constraint":      {endpoint: "us-west-2", code: "IllegalLocationConstraintException", message: "The unspecified location constraint is incompatible for the region specific endpoint this request was sent to."},
		"regional naming another region":     {endpoint: "us-west-2", constraint: "eu-central-1", code: "IllegalLocationConstraintException", message: "The eu-central-1 location constraint is incompatible for the region specific endpoint this request was sent to."},
		"regional naming its region":         {endpoint: "us-west-2", constraint: "us-west-2", home: "us-west-2", location: "us-west-2"},
		"eu-west-1 with EU":                  {endpoint: "eu-west-1", constraint: "EU", home: "eu-west-1", location: "EU"},
		"regional with EU elsewhere":         {endpoint: "us-west-2", constraint: "EU", code: "IllegalLocationConstraintException", message: "The EU location constraint is incompatible for the region specific endpoint this request was sent to."},
		"regional naming an unknown value":   {endpoint: "us-west-2", constraint: "bar", code: "IllegalLocationConstraintException", message: "The bar location constraint is incompatible for the region specific endpoint this request was sent to."},
		"empty constraint element in region": {endpoint: "us-east-1", constraint: "", home: "us-east-1"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			run(t, s3.Options{}, func(h *harness) {
				in := &s3api.CreateBucketInput{Bucket: "located", CreateBucketConfiguration: &s3api.CreateBucketConfiguration{}}
				if tc.constraint != "" {
					in.CreateBucketConfiguration.LocationConstraint = new(s3api.BucketLocationConstraint(tc.constraint))
				}
				out, err := as(h, who{region: tc.endpoint, host: tc.host}, s3api.OpCreateBucket, in)
				if tc.code != "" {
					wantCode(h.T, err, tc.code, http.StatusBadRequest)
					if err.Message != tc.message {
						t.Fatalf("message = %q, want %q", err.Message, tc.message)
					}
					return
				}
				if err != nil {
					t.Fatalf("got %s: %s, want success", err.ID.QueryCode, err.Message)
				}
				if tc.created != "" && (out.Location == nil || *out.Location != tc.created) {
					t.Fatalf("Location = %v, want %s", out.Location, tc.created)
				}
				head := ok(h, signed(tc.home), s3api.OpHeadBucket, &s3api.HeadBucketInput{Bucket: "located"})
				if *head.BucketRegion != tc.home {
					t.Fatalf("home region = %s, want %s", *head.BucketRegion, tc.home)
				}
				loc := ok(h, signed(tc.home), s3api.OpGetBucketLocation, &s3api.GetBucketLocationInput{Bucket: "located"})
				got := ""
				if loc.LocationConstraint != nil {
					got = string(*loc.LocationConstraint)
				}
				if got != tc.location {
					t.Fatalf("location = %q, want %q", got, tc.location)
				}
			})
		})
	}
}

func TestCreateBucketChecksItsInput(t *testing.T) {
	cases := map[string]struct {
		in   s3api.CreateBucketInput
		code string
	}{
		"uppercase name":   {in: s3api.CreateBucketInput{Bucket: "Upper"}, code: "InvalidBucketName"},
		"ipv4 shaped name": {in: s3api.CreateBucketInput{Bucket: "192.168.5.4"}, code: "InvalidBucketName"},
		"bad ownership":    {in: s3api.CreateBucketInput{Bucket: "ok-name", ObjectOwnership: new(s3api.ObjectOwnership("RandomValue"))}, code: "InvalidArgument"},
		"system tag": {in: s3api.CreateBucketInput{Bucket: "ok-name", CreateBucketConfiguration: &s3api.CreateBucketConfiguration{
			Tags: []s3api.Tag{{Key: "aws:k", Value: "v"}},
		}}, code: "InvalidTag"},
		"duplicate tags": {in: s3api.CreateBucketInput{Bucket: "ok-name", CreateBucketConfiguration: &s3api.CreateBucketConfiguration{
			Tags: []s3api.Tag{{Key: "k", Value: "1"}, {Key: "k", Value: "2"}},
		}}, code: "InvalidTag"},
		"public ACL with enforced ownership": {in: s3api.CreateBucketInput{Bucket: "ok-name", ACL: new(s3api.BucketCannedACLPublicRead)}, code: "InvalidBucketAclWithObjectOwnership"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			run(t, s3.Options{}, func(h *harness) {
				_, err := invoke(h, s3api.OpCreateBucket, &tc.in)
				wantCode(h.T, err, tc.code, http.StatusBadRequest)
				if h.svc.BucketExists(tc.in.Bucket) {
					t.Fatalf("a refused CreateBucket left bucket %s behind", tc.in.Bucket)
				}
			})
		})
	}
}

func TestCreateBucketAcceptsWhatTerraformSends(t *testing.T) {
	run(t, s3.Options{}, func(h *harness) {
		out := ok(h, who{}, s3api.OpCreateBucket, &s3api.CreateBucketInput{
			Bucket: "terraform-bucket", ACL: new(s3api.BucketCannedACLPrivate),
			CreateBucketConfiguration: &s3api.CreateBucketConfiguration{Tags: []s3api.Tag{{Key: "Name", Value: "probe"}}},
		})
		if *out.Location != "/terraform-bucket" || *out.BucketArn != "arn:aws:s3:::terraform-bucket" {
			t.Fatalf("output = %s %s, want /terraform-bucket and its ARN", *out.Location, *out.BucketArn)
		}
		tags := ok(h, who{}, s3api.OpGetBucketTagging, &s3api.GetBucketTaggingInput{Bucket: "terraform-bucket"})
		if want := []s3api.Tag{{Key: "Name", Value: "probe"}}; !reflect.DeepEqual(tags.TagSet, want) {
			t.Fatalf("tags = %+v, want the tags CreateBucket carried", tags.TagSet)
		}
	})
}

func TestObjectLockAtCreateTurnsVersioningOn(t *testing.T) {
	run(t, s3.Options{}, func(h *harness) {
		ok(h, who{}, s3api.OpCreateBucket, &s3api.CreateBucketInput{Bucket: "locked", ObjectLockEnabledForBucket: new(true)})
		v := ok(h, who{}, s3api.OpGetBucketVersioning, &s3api.GetBucketVersioningInput{Bucket: "locked"})
		if v.Status == nil || *v.Status != s3api.BucketVersioningStatusEnabled {
			t.Fatalf("versioning = %+v, want Enabled", v.Status)
		}
		l := ok(h, who{}, s3api.OpGetObjectLockConfiguration, &s3api.GetObjectLockConfigurationInput{Bucket: "locked"})
		if *l.ObjectLockConfiguration.ObjectLockEnabled != s3api.ObjectLockEnabledEnabled {
			t.Fatalf("object lock = %+v, want Enabled", l.ObjectLockConfiguration)
		}
	})
}

func TestHeadBucket(t *testing.T) {
	run(t, s3.Options{}, func(h *harness) {
		h.createBucket("headed")
		out := ok(h, who{}, s3api.OpHeadBucket, &s3api.HeadBucketInput{Bucket: "headed"})
		want := &s3api.HeadBucketOutput{BucketRegion: new("us-east-1"), BucketArn: new("arn:aws:s3:::headed"), AccessPointAlias: new(false)}
		if !reflect.DeepEqual(out, want) {
			t.Fatalf("got %+v, want %+v", out, want)
		}
		_, err := invoke(h, s3api.OpHeadBucket, &s3api.HeadBucketInput{Bucket: "missing"})
		wantCode(h.T, err, "NotFound", http.StatusNotFound)
		_, err = invoke(h, s3api.OpHeadBucket, &s3api.HeadBucketInput{Bucket: "headed", ExpectedBucketOwner: new(otherAccount)})
		wantCode(h.T, err, "AccessDenied", http.StatusForbidden)
	})
}

// TestListBucketsCreationDatePrecision: CreationDate is reported in whole
// seconds, as S3 reports every date, whatever the clock's precision
// (API_ListBuckets sample 2019-12-11T23:32:47+00:00).
func TestListBucketsCreationDatePrecision(t *testing.T) {
	run(t, s3.Options{}, func(h *harness) {
		if _, err := h.Clock.Advance(1500 * time.Millisecond); err != nil {
			t.Fatal(err)
		}
		now := h.Clock.Now().Wire()
		if now.Equal(now.Truncate(time.Second)) {
			t.Fatalf("clock at %s after a sub-second advance, want a fraction of a second to truncate", now)
		}
		h.createBucket("precise")
		out := ok(h, who{}, s3api.OpListBuckets, &s3api.ListBucketsInput{})
		if len(out.Buckets) != 1 || !out.Buckets[0].CreationDate.Equal(now.Truncate(time.Second)) {
			t.Fatalf("ListBuckets = %+v, want one bucket created at %s", out.Buckets, now.Truncate(time.Second))
		}
	})
}

func TestListBucketsListsTheCallersBucketsInNameOrder(t *testing.T) {
	run(t, s3.Options{}, func(h *harness) {
		for _, b := range []string{"charlie", "alpha", "bravo"} {
			h.createBucket(b)
		}
		ok(h, who{account: otherAccount}, s3api.OpCreateBucket, &s3api.CreateBucketInput{Bucket: "zulu"})
		ok(h, signed("us-west-2"), s3api.OpCreateBucket, &s3api.CreateBucketInput{Bucket: "delta", CreateBucketConfiguration: &s3api.CreateBucketConfiguration{
			LocationConstraint: new(s3api.BucketLocationConstraint("us-west-2")),
		}})
		names := func(out *s3api.ListBucketsOutput) []string {
			got := make([]string, 0, len(out.Buckets))
			for _, b := range out.Buckets {
				got = append(got, *b.Name)
			}
			return got
		}
		all := ok(h, who{}, s3api.OpListBuckets, &s3api.ListBucketsInput{})
		if got, want := names(all), []string{"alpha", "bravo", "charlie", "delta"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("buckets = %v, want %v", got, want)
		}
		if all.ContinuationToken != nil || all.Owner == nil || all.Owner.ID == nil {
			t.Fatalf("got token %v and owner %+v, want no token and an owner", all.ContinuationToken, all.Owner)
		}
		west := ok(h, who{}, s3api.OpListBuckets, &s3api.ListBucketsInput{BucketRegion: new("us-west-2")})
		if got := names(west); !reflect.DeepEqual(got, []string{"delta"}) {
			t.Fatalf("us-west-2 buckets = %v, want [delta]", got)
		}
		var paged []string
		in := &s3api.ListBucketsInput{MaxBuckets: new(int32(2))}
		for {
			page := ok(h, who{}, s3api.OpListBuckets, in)
			paged = append(paged, names(page)...)
			if page.ContinuationToken == nil {
				break
			}
			in.ContinuationToken = page.ContinuationToken
		}
		if want := names(all); !reflect.DeepEqual(paged, want) {
			t.Fatalf("pages = %v, want %v", paged, want)
		}
		_, err := invoke(h, s3api.OpListBuckets, &s3api.ListBucketsInput{BucketRegion: new("eu-east-1")})
		wantCode(h.T, err, "InvalidArgument", http.StatusBadRequest)
	})
}

// A bucket policy naming a bare account id reads back with the account's
// root ARN and otherwise as sent (iam-evaluation.md 5.4,
// IAM-EVAL-25/03-s3, recorded 2026-11-03).
func TestBucketPolicyStoresAnAccountPrincipalAsItsRootARN(t *testing.T) {
	run(t, s3.Options{}, func(h *harness) {
		h.createBucket("norm")
		policy := func(principal string) string {
			return `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"` + principal + `"},"Action":"s3:ListBucket","Resource":"arn:aws:s3:::norm"}]}`
		}
		ok(h, who{}, s3api.OpPutBucketPolicy, &s3api.PutBucketPolicyInput{Bucket: "norm", Policy: policy("000000000000")})
		got := ""
		if p := ok(h, who{}, s3api.OpGetBucketPolicy, &s3api.GetBucketPolicyInput{Bucket: "norm"}).Policy; p != nil {
			got = *p
		}
		if want := policy("arn:aws:iam::000000000000:root"); got != want {
			t.Errorf("GetBucketPolicy = %s\nwant %s", got, want)
		}
	})
}

func TestDeleteBucket(t *testing.T) {
	run(t, s3.Options{}, func(h *harness) {
		h.createBucket("doomed")
		ok(h, who{}, s3api.OpPutBucketPolicy, &s3api.PutBucketPolicyInput{Bucket: "doomed", Policy: `{"Statement":[]}`})
		ok(h, who{}, s3api.OpDeleteBucket, &s3api.DeleteBucketInput{Bucket: "doomed"})
		if h.svc.BucketExists("doomed") {
			t.Fatal("BucketExists reports a deleted bucket")
		}
		_, err := invoke(h, s3api.OpHeadBucket, &s3api.HeadBucketInput{Bucket: "doomed"})
		wantCode(h.T, err, "NotFound", http.StatusNotFound)
		_, err = invoke(h, s3api.OpDeleteBucket, &s3api.DeleteBucketInput{Bucket: "doomed"})
		wantCode(h.T, err, "NoSuchBucket", http.StatusNotFound)
		ok(h, who{account: otherAccount}, s3api.OpCreateBucket, &s3api.CreateBucketInput{Bucket: "doomed"})
		_, err = invoke(h, s3api.OpGetBucketPolicy, &s3api.GetBucketPolicyInput{Bucket: "doomed"})
		wantCode(h.T, err, "NoSuchBucketPolicy", http.StatusNotFound)
	})
}

func TestBucketExists(t *testing.T) {
	run(t, s3.Options{}, func(h *harness) {
		if h.svc.BucketExists("2015-03-31") {
			t.Fatal("BucketExists reports a bucket nobody created")
		}
		h.createBucket("2015-03-31")
		if !h.svc.BucketExists("2015-03-31") {
			t.Fatal("BucketExists does not report a created bucket")
		}
	})
}

func TestCrossRegionRequests(t *testing.T) {
	cases := map[string]struct {
		mode   s3.CrossRegion
		w      who
		code   string
		status int
		// fields are context elements the error must carry.
		fields map[string]string
		warned bool
	}{
		"signed for the home region": {w: signed("us-west-2")},
		"signed for another region": {
			w: signed("us-east-1"), code: "AuthorizationHeaderMalformed", status: http.StatusBadRequest,
			fields: map[string]string{"Region": "us-west-2"},
		},
		"unsigned to another region's host": {
			w: who{anonymous: true, host: "s3.eu-central-1.localhost:4566"}, code: "PermanentRedirect", status: http.StatusMovedPermanently,
			fields: map[string]string{"Endpoint": "s3.us-west-2.localhost:4566", "Bucket": "west"},
		},
		"unsigned to the home region's host": {w: who{anonymous: true, host: "west.s3.us-west-2.localhost:4566"}},
		"unsigned without a region":          {w: who{anonymous: true}},
		"SigV2 header or query":              {w: who{sigV2: true}},
		"lenient signed for another region":  {mode: s3.CrossRegionLenient, w: signed("us-east-1"), warned: true},
		"lenient unsigned to another host":   {mode: s3.CrossRegionLenient, w: who{anonymous: true, host: "s3.eu-central-1.localhost:4566"}, warned: true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			run(t, s3.Options{CrossRegion: tc.mode}, func(h *harness) {
				ok(h, signed("us-west-2"), s3api.OpCreateBucket, &s3api.CreateBucketInput{Bucket: "west", CreateBucketConfiguration: &s3api.CreateBucketConfiguration{
					LocationConstraint: new(s3api.BucketLocationConstraint("us-west-2")),
				}})
				head, err := as(h, tc.w, s3api.OpHeadBucket, &s3api.HeadBucketInput{Bucket: "west"})
				if tc.code != "" {
					wantCode(h.T, err, tc.code, tc.status)
					wantFields(h.T, err, tc.fields)
					if r := header(err, "x-amz-bucket-region"); r != "us-west-2" {
						t.Fatalf("x-amz-bucket-region = %q, want us-west-2", r)
					}
					return
				}
				if err != nil {
					t.Fatalf("got %s: %s, want the bucket served", err.ID.QueryCode, err.Message)
				}
				if *head.BucketRegion != "us-west-2" {
					t.Fatalf("x-amz-bucket-region = %s, want us-west-2", *head.BucketRegion)
				}
				var warned []journal.Advisory
				for _, e := range h.Journal.Query(journal.Filter{Kind: journal.KindAdvisory}).Events {
					if a, ok := e.Detail.(journal.Advisory); ok && a.Kind == journal.AdvisoryCrossRegionServed {
						warned = append(warned, a)
					}
				}
				if got := len(warned) == 1 && warned[0].Resource.String() == "arn:aws:s3:::west"; got != tc.warned {
					t.Fatalf("cross-region advisories = %+v, want one on the bucket: %v", warned, tc.warned)
				}
			})
		})
	}
}

// TestDottedBucketNameAssertsNoRegion: an unsigned virtual-hosted request
// to a bucket whose name holds s3.<region> asserts no region, so the bucket
// is served from its home region rather than redirected.
func TestDottedBucketNameAssertsNoRegion(t *testing.T) {
	run(t, s3.Options{}, func(h *harness) {
		const name = "logs.s3.eu-west-1.archive"
		ok(h, signed("us-west-2"), s3api.OpCreateBucket, &s3api.CreateBucketInput{Bucket: name, CreateBucketConfiguration: &s3api.CreateBucketConfiguration{
			LocationConstraint: new(s3api.BucketLocationConstraint("us-west-2")),
		}})
		head := ok(h, who{anonymous: true, host: name + ".localhost:4566"}, s3api.OpHeadBucket, &s3api.HeadBucketInput{Bucket: name})
		if *head.BucketRegion != "us-west-2" {
			t.Fatalf("x-amz-bucket-region = %s, want us-west-2", *head.BucketRegion)
		}
	})
}

func TestCrossRegionAppliesToEveryBucketOperation(t *testing.T) {
	run(t, s3.Options{}, func(h *harness) {
		ok(h, signed("us-west-2"), s3api.OpCreateBucket, &s3api.CreateBucketInput{Bucket: "west", CreateBucketConfiguration: &s3api.CreateBucketConfiguration{
			LocationConstraint: new(s3api.BucketLocationConstraint("us-west-2")),
		}})
		_, err := as(h, signed("us-east-1"), s3api.OpPutBucketTagging, &s3api.PutBucketTaggingInput{Bucket: "west", Tagging: s3api.Tagging{TagSet: []s3api.Tag{{Key: "k", Value: "v"}}}})
		wantCode(h.T, err, "AuthorizationHeaderMalformed", http.StatusBadRequest)
		_, err = as(h, signed("us-east-1"), s3api.OpDeleteBucket, &s3api.DeleteBucketInput{Bucket: "west"})
		wantCode(h.T, err, "AuthorizationHeaderMalformed", http.StatusBadRequest)
		if !h.svc.BucketExists("west") {
			t.Fatal("a refused DeleteBucket deleted the bucket")
		}
	})
}

// TestBucketRegionOnSameRegionErrors: of the recorded S3 errors on requests
// signed for the bucket's own region, only ListObjectsV2's carry
// x-amz-bucket-region (ADR 0013, amendment 2026-11-13). HeadBucket keeps
// it, and so does every error on a request that asserts no region
// (amendment 2026-10-08). The cross-region refusals are
// TestCrossRegionRules.
func TestBucketRegionOnSameRegionErrors(t *testing.T) {
	other := new("111111111111")
	cases := map[string]struct {
		w      who
		call   func(h *harness) *awserr.Error
		code   string
		status int
		region bool
	}{
		// testdata/parity/s3/R06/01-get-get-bucket-policy.json: 404 with
		// no x-amz-bucket-region.
		"GetBucketPolicy with no policy": {
			call: func(h *harness) *awserr.Error {
				return errOnly(invoke(h, s3api.OpGetBucketPolicy, &s3api.GetBucketPolicyInput{Bucket: "home"}))
			},
			code: "NoSuchBucketPolicy", status: http.StatusNotFound,
		},
		// testdata/parity/s3/IAM-ENF/13-s3.json: x-amz-bucket-region:
		// us-east-2 on a request to s3.us-east-2.
		"ListObjectsV2 with a negative max-keys": {
			call: func(h *harness) *awserr.Error {
				return errOnly(invoke(h, s3api.OpListObjectsV2, &s3api.ListObjectsV2Input{Bucket: "home", MaxKeys: new(int32(-1))}))
			},
			code: "InvalidArgument", status: http.StatusBadRequest, region: true,
		},
		// Inferred: an error about the bucket's owner is a ListObjectsV2
		// error like the recorded ones.
		"ListObjectsV2 naming another owner": {
			call: func(h *harness) *awserr.Error {
				return errOnly(invoke(h, s3api.OpListObjectsV2, &s3api.ListObjectsV2Input{Bucket: "home", ExpectedBucketOwner: other}))
			},
			code: "AccessDenied", status: http.StatusForbidden, region: true,
		},
		// Inferred: ListObjects and ListObjectVersions are unrecorded and
		// take the default, no header.
		"ListObjects with a negative max-keys": {
			call: func(h *harness) *awserr.Error {
				return errOnly(invoke(h, s3api.OpListObjects, &s3api.ListObjectsInput{Bucket: "home", MaxKeys: new(int32(-1))}))
			},
			code: "InvalidArgument", status: http.StatusBadRequest,
		},
		"ListObjectVersions with a negative max-keys": {
			call: func(h *harness) *awserr.Error {
				return errOnly(invoke(h, s3api.OpListObjectVersions, &s3api.ListObjectVersionsInput{Bucket: "home", MaxKeys: new(int32(-1))}))
			},
			code: "InvalidArgument", status: http.StatusBadRequest,
		},
		"GetBucketPolicy naming another owner": {
			call: func(h *harness) *awserr.Error {
				return errOnly(invoke(h, s3api.OpGetBucketPolicy, &s3api.GetBucketPolicyInput{Bucket: "home", ExpectedBucketOwner: other}))
			},
			code: "AccessDenied", status: http.StatusForbidden,
		},
		"DeleteBucket on a bucket with objects": {
			call: func(h *harness) *awserr.Error {
				h.put("home", "k", "v")
				return errOnly(invoke(h, s3api.OpDeleteBucket, &s3api.DeleteBucketInput{Bucket: "home"}))
			},
			code: "BucketNotEmpty", status: http.StatusConflict,
		},
		// ADR 0013: HeadBucket names the region on its errors too.
		"HeadBucket naming another owner": {
			call: func(h *harness) *awserr.Error {
				return errOnly(invoke(h, s3api.OpHeadBucket, &s3api.HeadBucketInput{Bucket: "home", ExpectedBucketOwner: other}))
			},
			code: "AccessDenied", status: http.StatusForbidden, region: true,
		},
		// ADR 0013, amendment 2026-10-08: a request that asserts no region
		// is told the bucket's.
		"GetBucketPolicy over SigV2": {
			call: func(h *harness) *awserr.Error {
				return errOnly(as(h, who{sigV2: true}, s3api.OpGetBucketPolicy, &s3api.GetBucketPolicyInput{Bucket: "home"}))
			},
			code: "NoSuchBucketPolicy", status: http.StatusNotFound, region: true,
		},
		"DeleteBucket with objects over SigV2": {
			call: func(h *harness) *awserr.Error {
				h.put("home", "k", "v")
				return errOnly(as(h, who{sigV2: true}, s3api.OpDeleteBucket, &s3api.DeleteBucketInput{Bucket: "home"}))
			},
			code: "BucketNotEmpty", status: http.StatusConflict, region: true,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			runObjects(t, func(h *harness) {
				h.createBucket("home")
				err := tc.call(h)
				wantCode(h.T, err, tc.code, tc.status)
				want := ""
				if tc.region {
					want = "us-east-1"
				}
				if r := header(err, "x-amz-bucket-region"); r != want {
					h.Fatalf("x-amz-bucket-region = %q, want %q", r, want)
				}
			})
		})
	}
}

func TestSnapshotRoundTrip(t *testing.T) {
	run(t, s3.Options{}, func(h *harness) {
		h.createBucket("kept")
		ok(h, who{}, s3api.OpPutBucketMetricsConfiguration, &s3api.PutBucketMetricsConfigurationInput{
			Bucket: "kept", Id: "m", MetricsConfiguration: s3api.MetricsConfiguration{Id: "m", Filter: s3api.MetricsFilterMemberPrefix{Value: "p/"}},
		})
		ok(h, who{}, s3api.OpPutBucketTagging, &s3api.PutBucketTaggingInput{Bucket: "kept", Tagging: s3api.Tagging{TagSet: []s3api.Tag{{Key: "k", Value: "v"}}}})
		sections, err := h.svc.Export()
		if err != nil {
			t.Fatal(err)
		}
		h.svc.Reset(kernel.ResetScope{})
		if h.svc.BucketExists("kept") {
			t.Fatal("Reset left a bucket")
		}
		if err := h.svc.Import(sections); err != nil {
			t.Fatal(err)
		}
		m := ok(h, who{}, s3api.OpGetBucketMetricsConfiguration, &s3api.GetBucketMetricsConfigurationInput{Bucket: "kept", Id: "m"})
		if f, isPrefix := m.MetricsConfiguration.Filter.(s3api.MetricsFilterMemberPrefix); !isPrefix || f.Value != "p/" {
			t.Fatalf("metrics filter = %#v, want the prefix p/", m.MetricsConfiguration.Filter)
		}
		tags := ok(h, who{}, s3api.OpGetBucketTagging, &s3api.GetBucketTaggingInput{Bucket: "kept"})
		if want := []s3api.Tag{{Key: "k", Value: "v"}}; !reflect.DeepEqual(tags.TagSet, want) {
			t.Fatalf("tags = %+v, want %+v", tags.TagSet, want)
		}
		again, err := h.svc.Export()
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(again, sections) {
			t.Fatal("an export after import differs from the export it loaded")
		}
	})
}

// TestImportDropsANameWithoutItsBucket: a snapshot cut between the two
// writes of CreateBucket or DeleteBucket holds the name in the index but no
// bucket section. Loaded, the name is free again: it can be created and
// deleted.
func TestImportDropsANameWithoutItsBucket(t *testing.T) {
	run(t, s3.Options{}, func(h *harness) {
		h.createBucket("kept")
		h.createBucket("half")
		sections, err := h.svc.Export()
		if err != nil {
			t.Fatal(err)
		}
		cut := slices.DeleteFunc(sections, func(s store.Section) bool { return s.Path == "buckets/half.json" })
		if len(cut) != 2 {
			t.Fatalf("sections = %v, want the index and kept's after dropping half's", cut)
		}
		h.svc.Reset(kernel.ResetScope{})
		if err := h.svc.Import(cut); err != nil {
			t.Fatal(err)
		}
		list := ok(h, who{}, s3api.OpListBuckets, &s3api.ListBucketsInput{})
		if len(list.Buckets) != 1 || *list.Buckets[0].Name != "kept" {
			t.Fatalf("ListBuckets = %+v, want only kept", list.Buckets)
		}
		if got := h.svc.Resources(aws.Scope{Partition: aws.PartitionAWS(), Account: aws.DefaultAccount(), Region: aws.DefaultRegion()}); len(got) != 1 {
			t.Fatalf("Resources = %v, want only kept", got)
		}
		ok(h, who{account: otherAccount}, s3api.OpCreateBucket, &s3api.CreateBucketInput{Bucket: "half"})
		ok(h, who{account: otherAccount}, s3api.OpDeleteBucket, &s3api.DeleteBucketInput{Bucket: "half"})
		ok(h, who{}, s3api.OpCreateBucket, &s3api.CreateBucketInput{Bucket: "half"})
	})
}

func TestResetDropsOnlyTheCoveredScopes(t *testing.T) {
	run(t, s3.Options{}, func(h *harness) {
		h.createBucket("east")
		ok(h, signed("us-west-2"), s3api.OpCreateBucket, &s3api.CreateBucketInput{Bucket: "west", CreateBucketConfiguration: &s3api.CreateBucketConfiguration{
			LocationConstraint: new(s3api.BucketLocationConstraint("us-west-2")),
		}})
		west, err := aws.KnownRegions().Parse("us-west-2")
		if err != nil {
			t.Fatal(err)
		}
		h.svc.Reset(kernel.ResetScope{Regions: []aws.Region{west}})
		if h.svc.BucketExists("west") || !h.svc.BucketExists("east") {
			t.Fatalf("after a us-west-2 reset: west %v, east %v; want only east", h.svc.BucketExists("west"), h.svc.BucketExists("east"))
		}
		ok(h, who{account: otherAccount, region: "us-west-2"}, s3api.OpCreateBucket, &s3api.CreateBucketInput{Bucket: "west", CreateBucketConfiguration: &s3api.CreateBucketConfiguration{
			LocationConstraint: new(s3api.BucketLocationConstraint("us-west-2")),
		}})
		if got := h.svc.Scopes(); len(got) != 2 {
			t.Fatalf("scopes = %v, want the east and the new west owner's", got)
		}
	})
}
