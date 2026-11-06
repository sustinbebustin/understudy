package edge_test

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/edge"
	"github.com/sustinbebustin/understudy/internal/protocol/awsjson"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/server"
)

// A REST candidate whose codec is not registered is skipped: the next
// candidate's table claims the request, and a request no usable table
// matches is the first usable candidate's unknown operation (ADR 0019).
func TestARESTCandidateWithoutACodecIsSkipped(t *testing.T) {
	codecs := map[ref.ProtocolID]edge.Codec{ref.RESTJSON1: server.Codecs()[ref.RESTJSON1], ref.AWSJSON10: server.JSONCodec(awsjson.V10)}
	build := func(t *testing.T) []*registry.Service {
		t.Helper()
		return []*registry.Service{
			restDescriptor(t, "xmlthings", "shared", ref.RESTXML, route{http.MethodGet, "/xml/things", "List"}),
			restDescriptor(t, "jsonthings", "shared", ref.RESTJSON1, route{http.MethodGet, "/json/things", "List"}),
		}
	}
	cases := []struct {
		path string
		want ref.OperationID
	}{
		{"/json/things", ref.OperationID{Service: "jsonthings", Name: "List"}},
		{"/xml/things", ref.OperationID{Service: "jsonthings"}},
	}
	for _, c := range cases {
		t.Run(c.path, func(t *testing.T) {
			f := newFront(t, codecs, build(t)...)
			f.serve(t, request(t, http.MethodGet, c.path, nil, "", "shared"))
			if got := f.lastCall(t); got != c.want {
				t.Errorf("the request reached %s, want %s", got, c.want)
			}
		})
	}
}

// With REST candidates registered but none usable, the request is
// unclaimed (rule 9), never answered by a candidate of another protocol.
func TestUnusableRESTCandidatesAreNotAnsweredByAQueryCandidate(t *testing.T) {
	ses := &registry.Service{
		ID: "ses", SigningName: "ses", IAMPrefix: "ses", ARNNamespace: "ses",
		Protocols: []ref.ProtocolID{ref.AWSQuery}, QueryVersion: "2010-12-01",
	}
	ses.Operations = []*registry.Operation{{Service: ses, ID: ref.OperationID{Service: "ses", Name: "SendEmail"}, Kind: registry.Modeled}}
	sesv2 := restDescriptor(t, "sesv2", "ses", ref.RESTJSON1, route{http.MethodPost, "/v2/email/outbound-emails", "SendEmail"})
	f := newFront(t, nil, ses, sesv2)
	got := f.serve(t, request(t, http.MethodPost, "/v2/email/outbound-emails", http.Header{"Content-Type": {"application/json"}}, `{}`, "ses"))
	if !strings.HasPrefix(got, "400\n") {
		t.Errorf("answer = %s, want rule 9's 400", got)
	}
	reasons := f.unclaimedReasons(t)
	if want := "rule 8: sesv2: no restjson1 codec is registered"; reasons[len(reasons)-1] != want {
		t.Errorf("reasons = %q, want %q last", reasons, want)
	}
	if slices.ContainsFunc(reasons, func(r string) bool { return strings.Contains(r, "no protocol rule claimed") }) {
		t.Errorf("reasons = %q: the query candidate answered", reasons)
	}
	if ops := f.calls(t); len(ops) != 0 {
		t.Errorf("journaled calls = %v, want none", ops)
	}
}
