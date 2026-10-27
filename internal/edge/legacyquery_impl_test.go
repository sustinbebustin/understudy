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

// The legacy-Query rule claims only its shape (ssm-parameters.md 2.3): a
// POST with an Action query parameter and no X-Amz-Target. A GET with
// Action, or a POST without Action, signed for SSM keeps the answer of a
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
