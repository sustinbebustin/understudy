package edge_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/ssmapi"
	"github.com/sustinbebustin/understudy/internal/edge"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// The other form of the legacy Query protocol, Action in a form body, is
// claimed too: a POST signed ssm with Content-Type
// application/x-www-form-urlencoded and no X-Amz-Target answers the same
// 501 naming the protocol, never SSM's unknown operation. AWS's answer to
// this form is unrecorded (ssm-parameters.md 2.3 records the query-string
// form); not serving it is answered 501 (CLAUDE.md invariant 3).
func TestLegacyQueryFormBodyIsNotImplemented(t *testing.T) {
	f := newFrontWith(t, nil, func(o *edge.Options) { o.LegacyQuery = []ref.ServiceID{"ssm"} }, ssmapi.Service())
	rec := httptest.NewRecorder()
	header := http.Header{"Content-Type": {"application/x-www-form-urlencoded; charset=utf-8"}}
	f.handler.ServeHTTP(rec, request(t, http.MethodPost, "/", header, "Action=GetParameter&Name=p", "ssm"))
	if rec.Code != http.StatusNotImplemented || !strings.Contains(rec.Body.String(), "legacy Query") || !strings.Contains(rec.Body.String(), "AmazonSSM.") {
		t.Errorf("answer = %d %s, want the legacy-Query 501 naming the X-Amz-Target prefix", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "UnknownOperation") {
		t.Errorf("body %s is an unknown operation", rec.Body)
	}
}

// The legacy-Query rule claims only its shape (ssm-parameters.md 2.3): a
// POST with an Action query parameter or a form body, and no X-Amz-Target. A GET with
// Action, or a POST with neither Action nor a form body, signed for SSM keeps the answer of a
// request SSM's JSON front end cannot place, never the legacy-Query 501.
func TestLegacyQueryClaimsOnlyItsShape(t *testing.T) {
	f := newFrontWith(t, nil, func(o *edge.Options) { o.LegacyQuery = []ref.ServiceID{"ssm"} }, ssmapi.Service())
	for _, c := range []struct{ name, method, target string }{
		{"GET with Action", http.MethodGet, "/?Action=GetParameter&Name=p"},
		{"POST without Action", http.MethodPost, "/?Name=p"},
	} {
		rec := httptest.NewRecorder()
		f.handler.ServeHTTP(rec, request(t, c.method, c.target, nil, "", "ssm"))
		if rec.Code == http.StatusNotImplemented || strings.Contains(rec.Body.String(), "legacy Query") {
			t.Errorf("%s: answer = %d %s, want no legacy-Query 501", c.name, rec.Code, rec.Body)
		}
	}
}
