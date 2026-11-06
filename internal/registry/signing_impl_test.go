package registry_test

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// The refusal says which rule cannot tell the pair apart, so the reader
// knows which descriptor field to change (ADR 0019).
func TestSigningRefusalsSayWhyNoRuleTellsThePairApart(t *testing.T) {
	pair := func(p ref.ProtocolID, adjust func(a, b *registry.Service)) func(*testing.T) (a, b *registry.Service) {
		return func(*testing.T) (a, b *registry.Service) {
			a, b = service("one", "Get"), service("two", "List")
			a.SigningName, b.SigningName = "shared", "shared"
			a.Protocols, b.Protocols = []ref.ProtocolID{p}, []ref.ProtocolID{p}
			adjust(a, b)
			return a, b
		}
	}
	cases := []struct {
		name  string
		build func(*testing.T) (a, b *registry.Service)
		want  string
	}{
		{
			"no protocol",
			func(*testing.T) (a, b *registry.Service) {
				a, b = service("one", "Get"), service("two", "Get")
				a.SigningName, b.SigningName = "shared", "shared"
				return a, b
			},
			`signing name "shared" is also service "one"'s, and no claim rule tells their requests apart: one of them declares no protocol`,
		},
		{
			"awsQuery at one Version",
			pair(ref.AWSQuery, func(a, b *registry.Service) { a.QueryVersion, b.QueryVersion = "2010-12-01", "2010-12-01" }),
			`signing name "shared" is also service "one"'s, and no claim rule tells their requests apart: both speak awsQuery at Version "2010-12-01"`,
		},
		{
			"awsJson without prefixes",
			pair(ref.AWSJSON11, func(*registry.Service, *registry.Service) {}),
			`signing name "shared" is also service "one"'s, and no claim rule tells their requests apart: both speak awsJson with X-Amz-Target prefix ""`,
		},
		{
			"rpcv2Cbor with one shape",
			pair(ref.RPCv2CBOR, func(a, b *registry.Service) { a.Shape, b.Shape = "com.example#Shared", "com.example#Shared" }),
			`signing name "shared" is also service "one"'s, and no claim rule tells their requests apart: both speak rpcv2Cbor as shape com.example#Shared`,
		},
		{
			"REST without a route table",
			func(t *testing.T) (a, b *registry.Service) {
				t.Helper()
				b = restService(t, "two", "shared", restRoute{http.MethodGet, "/v2/things", "List"})
				b.Routes = nil
				return restService(t, "one", "shared", restRoute{http.MethodGet, "/things", "List"}), b
			},
			`signing name "shared" is also service "one"'s, and no claim rule tells their requests apart: service "two" speaks REST without a route table`,
		},
		{
			// The overlapping pair is named by index and pattern, the added
			// service's route first.
			"overlapping route tables",
			func(t *testing.T) (a, b *registry.Service) {
				t.Helper()
				return restService(t, "one", "shared",
						restRoute{http.MethodGet, "/v2/things", "List"},
						restRoute{http.MethodPost, "/v2/things/{Id}/parts", "Put"}),
					restService(t, "two", "shared",
						restRoute{http.MethodGet, "/v1/things", "Old"},
						restRoute{http.MethodDelete, "/{Id}/{Key+}", "Delete"})
			},
			`signing name "shared" is also service "one"'s, and route 1 (DELETE /{Id}/{Key+}) of one can match a request that route 0 (GET /v2/things) of the other matches; give each table its own first literal path segment`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a, b := c.build(t)
			_, err := registry.New(a, b)
			var target *registry.DescriptorError
			if !errors.As(err, &target) {
				t.Fatalf("New error = %v, want a *DescriptorError", err)
			}
			if target.Detail != c.want {
				t.Errorf("Detail =\n%s\nwant\n%s", target.Detail, c.want)
			}
			if !strings.Contains(err.Error(), `service "two"`) {
				t.Errorf("Error() = %q, want it to name the refused service two", err)
			}
		})
	}
}

// The root pattern / matches only the empty path, so a table holding it is
// apart from one whose routes all start with a literal, and overlaps
// another table holding /.
func TestRootRoutesAndFirstLiterals(t *testing.T) {
	root := func(t *testing.T, id string) *registry.Service {
		t.Helper()
		return restService(t, id, "shared", restRoute{http.MethodGet, "/", "List" + id})
	}
	if _, err := registry.New(root(t, "one"), restService(t, "two", "shared", restRoute{http.MethodGet, "/v2/things", "List"})); err != nil {
		t.Errorf("New refused / against a literal-first table: %v", err)
	}
	_, err := registry.New(root(t, "one"), root(t, "two"))
	wantDescriptorError(t, err, registry.OverlappingRouteTables, "two", "shared", "one")
}

// An empty route table matches no request, so it overlaps nothing, even a
// table whose route starts with a label (spec: every route of one starts
// with a literal none of the other's routes can match, vacuously true).
func TestAnEmptyRouteTableOverlapsNothing(t *testing.T) {
	empty := restService(t, "one", "shared")
	if _, err := registry.New(empty, restService(t, "two", "shared", restRoute{http.MethodGet, "/{Bucket}", "List"})); err != nil {
		t.Errorf("New refused an empty route table: %v", err)
	}
}

// ref.Raw is no protocol family: raw operations are claimed by prefix and
// host rules, so a raw-only service never collides with a REST one.
func TestRawServicesShareNoFamily(t *testing.T) {
	raw := service("one", "Invoke")
	raw.SigningName, raw.Protocols = "shared", []ref.ProtocolID{ref.Raw}
	r, err := registry.New(raw, restService(t, "two", "shared", restRoute{http.MethodGet, "/{Bucket}", "List"}))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := r.BySigningName("shared"); len(got) != 2 {
		t.Errorf("BySigningName(shared) = %v, want both services", ids(got))
	}
	if got := r.BySigningName("unknown"); got != nil {
		t.Errorf("BySigningName(unknown) = %v, want nil", ids(got))
	}
}
