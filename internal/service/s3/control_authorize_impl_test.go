package s3_test

import (
	"context"
	"encoding/xml"
	"net/http"
	"slices"
	"sync"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/s3api"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
)

// denyGate is a step 8 Authorizer that denies every client call and
// records each pair it is asked about.
type denyGate struct {
	mu    sync.Mutex
	pairs []string
}

func (g *denyGate) Authorize(_ context.Context, _ *kernel.Call, q kernel.AuthzQuery) kernel.Verdict {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, p := range q.Pairs {
		if r, ok := p.Resource.(kernel.ResourceARN); ok {
			g.pairs = append(g.pairs, p.Action.String()+" "+r.ARN.String())
		}
	}
	return kernel.Denied{Message: "denied in this test"}
}

func (g *denyGate) asked() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return slices.Clone(g.pairs)
}

// TestS3ControlTagsAreAuthorizedOnTheBucket: the S3 Control tag operations
// are raw bindings, which kernel step 8 skips, so each handler asks about
// its own action on the bucket the ARN names (s3:ListTagsForResource,
// s3:TagResource, s3:UntagResource; iam-evaluation.md 2.1). A denial is
// S3's AccessDenied in S3 Control's <ErrorResponse> wrapper, answered
// before the bucket is looked up: the bucket here does not exist
// (iam-enforcement-surfaces.md 5.3, switch iam.enforce.missing-resource).
func TestS3ControlTagsAreAuthorizedOnTheBucket(t *testing.T) {
	gate := &denyGate{}
	kerneltest.Run(t, func(k *kerneltest.K) {
		t := k.T
		send := edgeClient(k)
		tags := tagsPath("absent")
		const tagBody = `<TagResourceRequest xmlns="http://awss3control.amazonaws.com/doc/2018-08-20/"><Tags><Tag><Key>team</Key><Value>storage</Value></Tag></Tags></TagResourceRequest>`
		for _, req := range []struct{ name, method, target, body string }{
			{"ListTagsForResource", http.MethodGet, tags, ""},
			{"TagResource", http.MethodPost, tags, tagBody},
			{"UntagResource", http.MethodDelete, tags + "?tagKeys=team", ""},
		} {
			code, body := send(req.method, controlHost, req.target, req.body)
			var doc struct {
				XMLName xml.Name `xml:"ErrorResponse"`
				Error   struct {
					Code string `xml:"Code"`
				} `xml:"Error"`
			}
			if err := xml.Unmarshal([]byte(body), &doc); err != nil || code != http.StatusForbidden || doc.Error.Code != "AccessDenied" {
				t.Errorf("%s under a denying Authorizer = %d %s, want 403 <ErrorResponse><Error><Code>AccessDenied", req.name, code, body)
			}
		}
		want := []string{
			"s3:ListTagsForResource arn:aws:s3:::absent",
			"s3:TagResource arn:aws:s3:::absent",
			"s3:UntagResource arn:aws:s3:::absent",
		}
		if got := gate.asked(); !slices.Equal(got, want) {
			t.Errorf("asked about %v, want %v", got, want)
		}
	}, kerneltest.Services(s3api.Service()), kerneltest.Authorizer(gate))
}
