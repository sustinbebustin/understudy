package edge_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/apigatewayapi"
	"github.com/sustinbebustin/understudy/internal/api/apigatewayv2api"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/edge"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
)

var (
	invokeAPI     = ref.OperationID{Service: "apigatewayv2", Name: "InvokeApi"}
	invokeRestAPI = ref.OperationID{Service: "apigateway", Name: "InvokeRestApi"}
)

// owners is an ExecuteAPIOwner holding a fixed set of API ids.
type owners map[string]bool

func (o owners) OwnsExecuteAPI(id string) bool { return o[id] }

// rawOp finds the raw operation id in the generated service.
func rawOp(t *testing.T, svc *registry.Service, id ref.OperationID) *registry.Operation {
	t.Helper()
	for _, op := range svc.Operations {
		if op.ID == id {
			return op
		}
	}
	t.Fatalf("service %s has no operation %v", svc.ID, id)
	return nil
}

// twoFronts is the edge with both API Gateway services (their generated
// route tables, ADR 0019) and both execute-api fronts bound, HTTP APIs then
// REST APIs (wire.go's registration order), each answering 200 with its
// service name; v2 and v1 hold the given ids.
func twoFronts(t *testing.T, v2, v1 owners) *front {
	t.Helper()
	v2svc, v1svc := apigatewayv2api.Service(), apigatewayapi.Service()
	return newFrontWith(t, nil, func(o *edge.Options) {
		for svc, id := range map[*registry.Service]ref.OperationID{v2svc: invokeAPI, v1svc: invokeRestAPI} {
			name := string(svc.ID)
			kernel.BindRawHTTP(o.Kernel.Binder(svc), rawOp(t, svc, id), func(_ context.Context, _ *kernel.Call, _ *http.Request, w http.ResponseWriter) *awserr.Error {
				if _, err := io.WriteString(w, name); err != nil {
					return awserr.Internalf(err)
				}
				return nil
			}, registry.Full{})
		}
		if err := o.Kernel.BindErr(); err != nil {
			t.Fatal(err)
		}
		o.ExecuteAPI = []edge.ExecuteAPIService{{Op: invokeAPI, Owner: v2}, {Op: invokeRestAPI, Owner: v1}}
	}, v2svc, v1svc)
}

// An execute-api request goes to the raw operation of the one service
// whose owner holds the API id, on the path fallback (sandboxed, ADR 0020)
// and on the host form with the id's first label lowercased.
func TestExecuteAPIOwnerResolution(t *testing.T) {
	f := twoFronts(t, owners{"httpapi001": true}, owners{"restapi001": true})
	for _, c := range []struct {
		path, host string
		want       string
		op         ref.OperationID
	}{
		{"/_understudy/execute-api/restapi001/s/", "", "apigateway", invokeRestAPI},
		{"/_understudy/execute-api/httpapi001/hello", "", "apigatewayv2", invokeAPI},
		{"/s/", "RESTAPI001.execute-api.us-east-1.localhost:4566", "apigateway", invokeRestAPI},
		{"/hello", "httpapi001.execute-api.us-east-1.localhost:4566", "apigatewayv2", invokeAPI},
	} {
		r := request(t, http.MethodGet, c.path, nil, "", "")
		if c.host != "" {
			r.Host = c.host
		}
		rec := httptest.NewRecorder()
		f.handler.ServeHTTP(rec, r)
		if rec.Code != http.StatusOK || rec.Body.String() != c.want {
			t.Errorf("%s %s = %d %q, want %s's handler", c.host, c.path, rec.Code, rec.Body, c.want)
		}
		if c.host == "" && !slices.Equal(policies(rec), []string{sandbox}) {
			t.Errorf("%s: Content-Security-Policy = %q, want the path form's sandbox", c.path, policies(rec))
		}
	}
	if ops := f.calls(t); !slices.Equal(ops, []ref.OperationID{invokeRestAPI, invokeAPI, invokeRestAPI, invokeAPI}) {
		t.Errorf("journaled calls = %v", ops)
	}
}

// An id no service owns goes to the first registered front, whose handler
// answers its unknown-API answer: v2's, as wire.go registers HTTP APIs
// first.
func TestExecuteAPIUnownedIDGoesToTheFirstFront(t *testing.T) {
	f := twoFronts(t, owners{}, owners{})
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, request(t, http.MethodGet, "/_understudy/execute-api/nosuch0001/s/", nil, "", ""))
	if rec.Body.String() != "apigatewayv2" {
		t.Errorf("an unowned id = %d %q, want the first front, apigatewayv2", rec.Code, rec.Body)
	}
}

// An id two services claim is an understudy bug: the edge answers 500
// naming both services and saying nothing was changed, as JSON, journaled,
// and neither handler runs.
func TestExecuteAPIIDClaimedTwiceIsABug(t *testing.T) {
	f := twoFronts(t, owners{"both000001": true}, owners{"both000001": true})
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, request(t, http.MethodGet, "/_understudy/execute-api/both000001/s/", nil, "", ""))
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body %s is not {\"message\": ...}: %v", rec.Body, err)
	}
	want := "understudy found API id both000001 in both apigatewayv2 and apigateway, which is an understudy bug; nothing was changed. Report it at https://github.com/sustinbebustin/understudy/issues"
	if rec.Code != http.StatusInternalServerError || body["message"] != want || rec.Header().Get("Content-Type") != "application/json" {
		t.Errorf("an id claimed twice = %d %s %s, want 500 application/json %q", rec.Code, rec.Header().Get("Content-Type"), rec.Body, want)
	}
	for _, op := range f.calls(t) {
		if op == invokeAPI || op == invokeRestAPI {
			t.Errorf("journaled call %v, want neither front to run", op)
		}
	}
	if n := len(f.journal.Query(journal.Filter{Limit: 1000}).Events); n == 0 {
		t.Error("nothing journaled for an id claimed twice")
	}
}
