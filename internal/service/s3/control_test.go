package s3_test

import (
	"net/http"
	"reflect"
	"slices"
	"strconv"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/service/s3"
)

// tagsPath is the S3 Control tags path of a bucket, its ARN percent-encoded
// as Terraform sends it (s3.md section 2, PROBE E2).
func tagsPath(bucket string) string {
	return "/v20180820/tags/arn%3Aaws%3As3%3A%3A%3A" + bucket
}

func TestS3ControlTagsShareTheBucketTagSet(t *testing.T) {
	run(t, s3.Options{}, func(h *harness) {
		ok(h, who{}, s3api.OpCreateBucket, &s3api.CreateBucketInput{Bucket: "tagged", CreateBucketConfiguration: &s3api.CreateBucketConfiguration{
			Tags: []s3api.Tag{{Key: "Name", Value: "probe"}},
		}})
		list := func() string {
			h.Helper()
			rec, err := h.raw("ListTagsForResource", http.MethodGet, tagsPath("tagged"), "")
			if err != nil {
				t.Fatalf("ListTagsForResource: %s: %s", err.ID.QueryCode, err.Message)
			}
			if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/xml" || rec.Header().Get("X-Amz-Request-Id") == "" {
				t.Fatalf("ListTagsForResource answered %d %v", rec.Code, rec.Header())
			}
			return rec.Body.String()
		}
		const prefix = `<?xml version="1.0" encoding="UTF-8"?>` + "\n" + `<ListTagsForResourceResult xmlns="http://awss3control.amazonaws.com/doc/2018-08-20/"><Tags>`
		if got, want := list(), prefix+`<Tag><Key>Name</Key><Value>probe</Value></Tag></Tags></ListTagsForResourceResult>`; got != want {
			t.Fatalf("tags from CreateBucket:\n got %s\nwant %s", got, want)
		}

		rec, err := h.raw("TagResource", http.MethodPost, tagsPath("tagged"),
			`<TagResourceRequest xmlns="http://awss3control.amazonaws.com/doc/2018-08-20/"><Tags><Tag><Key>team</Key><Value>storage</Value></Tag><Tag><Key>Name</Key><Value>renamed</Value></Tag></Tags></TagResourceRequest>`)
		if err != nil || rec.Code != http.StatusNoContent {
			t.Fatalf("TagResource = %v, %v; want 204", rec, err)
		}
		got := ok(h, who{}, s3api.OpGetBucketTagging, &s3api.GetBucketTaggingInput{Bucket: "tagged"})
		if want := []s3api.Tag{{Key: "Name", Value: "renamed"}, {Key: "team", Value: "storage"}}; !reflect.DeepEqual(got.TagSet, want) {
			t.Fatalf("GetBucketTagging after TagResource = %+v, want %+v", got.TagSet, want)
		}

		rec, err = h.raw("UntagResource", http.MethodDelete, tagsPath("tagged")+"?tagKeys=Name", "")
		if err != nil || rec.Code != http.StatusNoContent {
			t.Fatalf("UntagResource = %v, %v; want 204", rec, err)
		}
		ok(h, who{}, s3api.OpPutBucketTagging, &s3api.PutBucketTaggingInput{Bucket: "tagged", Tagging: s3api.Tagging{TagSet: []s3api.Tag{{Key: "env", Value: "test"}}}})
		if got, want := list(), prefix+`<Tag><Key>env</Key><Value>test</Value></Tag></Tags></ListTagsForResourceResult>`; got != want {
			t.Fatalf("tags after PutBucketTagging:\n got %s\nwant %s", got, want)
		}

		rec, err = h.raw("UntagResource", http.MethodDelete, tagsPath("tagged")+"?tagKeys=env", "")
		if err != nil || rec.Code != http.StatusNoContent {
			t.Fatalf("UntagResource = %v, %v; want 204", rec, err)
		}
		_, gerr := invoke(h, s3api.OpGetBucketTagging, &s3api.GetBucketTaggingInput{Bucket: "tagged"})
		wantCode(h.T, gerr, "NoSuchTagSet", http.StatusNotFound)
		if got, want := list(), prefix+`</Tags></ListTagsForResourceResult>`; got != want {
			t.Fatalf("tags after removing every tag:\n got %s\nwant %s", got, want)
		}
		// Each S3 Control call names the bucket, so a generated policy can
		// scope s3:TagResource and its siblings to it.
		touched := map[string]bool{}
		for _, e := range h.Journal.Query(journal.Filter{Kind: journal.KindAPICall}).Events {
			if call, ok := e.Detail.(journal.APICall); ok && call.Op.Service == "s3" && slices.Contains([]string{"ListTagsForResource", "TagResource", "UntagResource"}, call.Op.Name) {
				touched[call.Op.Name] = slices.ContainsFunc(call.Resources, func(a aws.ARN) bool { return a.String() == "arn:aws:s3:::tagged" })
			}
		}
		if want := map[string]bool{"ListTagsForResource": true, "TagResource": true, "UntagResource": true}; !reflect.DeepEqual(touched, want) {
			t.Fatalf("S3 Control calls touching the bucket = %v, want %v", touched, want)
		}
	})
}

func TestS3ControlErrors(t *testing.T) {
	cases := map[string]struct {
		name, method, target, body string
		// account is the x-amz-account-id sent; empty sends the default
		// account's.
		account string
		code    string
		status  int
		// fields are context elements the error must carry.
		fields map[string]string
	}{
		"missing bucket":       {name: "ListTagsForResource", method: http.MethodGet, target: tagsPath("missing"), code: "NoSuchBucket", status: http.StatusNotFound},
		"not a bucket ARN":     {name: "ListTagsForResource", method: http.MethodGet, target: "/v20180820/tags/arn%3Aaws%3As3%3Aus-east-1%3A000000000000%3Aaccesspoint%2Fap", code: "InvalidRequest", status: http.StatusBadRequest},
		"malformed tag body":   {name: "TagResource", method: http.MethodPost, target: tagsPath("present"), body: "<TagResourceRequest><Tags><Tag><Key>k</Key></Tag></Tags></TagResourceRequest>", code: "MalformedXML", status: http.StatusBadRequest},
		"body without Tags":    {name: "TagResource", method: http.MethodPost, target: tagsPath("present"), body: "<TagResourceRequest></TagResourceRequest>", code: "MalformedXML", status: http.StatusBadRequest},
		"system tag key":       {name: "TagResource", method: http.MethodPost, target: tagsPath("present"), body: "<TagResourceRequest><Tags><Tag><Key>aws:k</Key><Value>v</Value></Tag></Tags></TagResourceRequest>", code: "InvalidTag", status: http.StatusBadRequest},
		"body that is not XML": {name: "TagResource", method: http.MethodPost, target: tagsPath("present"), body: "tags", code: "MalformedXML", status: http.StatusBadRequest},
		"malformed account id": {
			name: "ListTagsForResource", method: http.MethodGet, target: tagsPath("present"), account: "not-an-account",
			code: "InvalidArgument", status: http.StatusBadRequest,
			fields: map[string]string{"ArgumentName": "x-amz-account-id", "ArgumentValue": "not-an-account"},
		},
		"another account": {name: "ListTagsForResource", method: http.MethodGet, target: tagsPath("present"), account: otherAccount, code: "AccessDenied", status: http.StatusForbidden},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			run(t, s3.Options{}, func(h *harness) {
				h.createBucket("present")
				account := tc.account
				if account == "" {
					account = aws.DefaultAccount().String()
				}
				_, err := h.rawFor(account, tc.name, tc.method, tc.target, tc.body)
				wantCode(h.T, err, tc.code, tc.status)
				wantFields(h.T, err, tc.fields)
			})
		})
	}
}

// TestTagResourceAcceptsAnyRootElement: no recording shows the root element
// the SDKs send, so only the Tags child is required (tf-storage.md 1.1).
func TestTagResourceAcceptsAnyRootElement(t *testing.T) {
	run(t, s3.Options{}, func(h *harness) {
		h.createBucket("rooted")
		rec, err := h.raw("TagResource", http.MethodPost, tagsPath("rooted"), `<Tagging><Tags><Tag><Key>k</Key><Value>v</Value></Tag></Tags></Tagging>`)
		if err != nil || rec.Code != http.StatusNoContent {
			t.Fatalf("TagResource = %v, %v; want 204", rec, err)
		}
		got := ok(h, who{}, s3api.OpGetBucketTagging, &s3api.GetBucketTaggingInput{Bucket: "rooted"})
		if want := []s3api.Tag{{Key: "k", Value: "v"}}; !reflect.DeepEqual(got.TagSet, want) {
			t.Fatalf("GetBucketTagging = %+v, want %+v", got.TagSet, want)
		}
	})
}

// TestTagResourcePastTheLimitNamesTheBucketRegion: a merge that would hold
// more than 50 tags is InvalidTag about the bucket, with its region header.
func TestTagResourcePastTheLimitNamesTheBucketRegion(t *testing.T) {
	run(t, s3.Options{}, func(h *harness) {
		h.createBucket("full")
		tags := make([]s3api.Tag, 50)
		for i := range tags {
			tags[i] = s3api.Tag{Key: "k" + strconv.Itoa(i), Value: "v"}
		}
		ok(h, who{}, s3api.OpPutBucketTagging, &s3api.PutBucketTaggingInput{Bucket: "full", Tagging: s3api.Tagging{TagSet: tags}})
		_, err := h.raw("TagResource", http.MethodPost, tagsPath("full"), `<TagResourceRequest><Tags><Tag><Key>one-more</Key><Value>v</Value></Tag></Tags></TagResourceRequest>`)
		wantCode(h.T, err, "InvalidTag", http.StatusBadRequest)
		if r := header(err, "x-amz-bucket-region"); r != "us-east-1" {
			t.Fatalf("x-amz-bucket-region = %q, want us-east-1", r)
		}
	})
}
