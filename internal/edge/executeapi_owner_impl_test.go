package edge_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/edge"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// An id two services claim is refused on both forms, the path fallback
// sandboxed and the host unsandboxed (ADR 0020), and the Unclaimed event's
// last reason is the claiming rule's, carrying the answer (ADR 0019
// amendment: "journaled Unclaimed with that reason").
func TestExecuteAPIIDClaimedTwiceOnBothForms(t *testing.T) {
	const msg = "understudy found API id both000001 in both apigatewayv2 and apigateway, which is an understudy bug; nothing was changed. Report it at https://github.com/sustinbebustin/understudy/issues"
	for _, c := range []struct {
		name, host, path, reason string
		policy                   []string
	}{
		{
			"path", "", "/_understudy/execute-api/both000001/s/",
			"rule 1: /_understudy/execute-api/both000001/s/ is the API Gateway execute-api path fallback, and " + msg,
			[]string{sandbox},
		},
		{
			"host", "BOTH000001.execute-api.us-east-1.localhost:4566", "/s/",
			`rule 2: host "both000001.execute-api.us-east-1.localhost" is an API Gateway execute-api host, and ` + msg,
			nil,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := twoFronts(t, owners{"both000001": true}, owners{"both000001": true})
			r := request(t, http.MethodGet, c.path, nil, "", "")
			if c.host != "" {
				r.Host = c.host
			}
			rec := httptest.NewRecorder()
			f.handler.ServeHTTP(rec, r)
			if rec.Code != http.StatusInternalServerError {
				t.Errorf("status = %d, want 500", rec.Code)
			}
			if got := policies(rec); !slices.Equal(got, c.policy) {
				t.Errorf("Content-Security-Policy = %q, want %q", got, c.policy)
			}
			if got := f.lastReason(t); got != c.reason {
				t.Errorf("last reason = %q, want %q", got, c.reason)
			}
		})
	}
}

// A front whose operation is not registered Raw is passed over: its owner
// holding the id does not route there, and the first registered front
// answers instead (ADR 0019 amendment: "the fronts whose operation is
// registered raw").
func TestExecuteAPIUnregisteredFrontIsPassedOver(t *testing.T) {
	svc := invokeAPIService()
	f := newFrontWith(t, nil, func(o *edge.Options) {
		kernel.BindRawHTTP(o.Kernel.Binder(svc), svc.Operations[0], func(_ context.Context, _ *kernel.Call, _ *http.Request, w http.ResponseWriter) *awserr.Error {
			if _, err := io.WriteString(w, "apigatewayv2"); err != nil {
				return awserr.Internalf(err)
			}
			return nil
		}, registry.Full{})
		if err := o.Kernel.BindErr(); err != nil {
			t.Fatal(err)
		}
		o.ExecuteAPI = []edge.ExecuteAPIService{{Op: invokeRestAPI, Owner: owners{"restapi001": true}}, {Op: invokeAPI, Owner: owners{}}}
	}, svc)
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, request(t, http.MethodGet, "/_understudy/execute-api/restapi001/s/", nil, "", ""))
	if rec.Code != http.StatusOK || rec.Body.String() != "apigatewayv2" {
		t.Errorf("answer = %d %q, want the only registered front, apigatewayv2", rec.Code, rec.Body)
	}
	if ops := f.calls(t); !slices.Equal(ops, []ref.OperationID{invokeAPI}) {
		t.Errorf("journaled calls = %v, want [%v]", ops, invokeAPI)
	}
}

// With fronts listed but none registered, the request is still claimed
// and answered 501, never S3's (ADR 0019 amendment: "With no front
// registered the request is answered 501 as before").
func TestExecuteAPINoFrontRegisteredIsNotImplemented(t *testing.T) {
	f := newFrontWith(t, map[ref.ProtocolID]edge.Codec{ref.RESTXML: xmlCodec{}}, func(o *edge.Options) {
		o.ExecuteAPI = []edge.ExecuteAPIService{{Op: invokeAPI, Owner: owners{"abc123": true}}, {Op: invokeRestAPI, Owner: owners{"abc123": true}}}
	}, s3())
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, request(t, http.MethodGet, "/_understudy/execute-api/abc123/s/", nil, "", ""))
	if rec.Code != http.StatusNotImplemented {
		t.Errorf("status = %d %q, want 501", rec.Code, rec.Body)
	}
}
