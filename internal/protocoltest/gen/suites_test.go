package gen_test

import (
	"testing"

	"github.com/sustinbebustin/understudy/internal/protocoltest/gen"
	"github.com/sustinbebustin/understudy/internal/protocoltest/gen/awsquery"
	"github.com/sustinbebustin/understudy/internal/protocoltest/gen/jsonprotocol"
	"github.com/sustinbebustin/understudy/internal/protocoltest/gen/jsonrpc10"
	"github.com/sustinbebustin/understudy/internal/protocoltest/gen/nonquerycompatiblerpcv2protocol"
	"github.com/sustinbebustin/understudy/internal/protocoltest/gen/querycompatiblejsonrpc10"
	"github.com/sustinbebustin/understudy/internal/protocoltest/gen/querycompatiblerpcv2protocol"
	"github.com/sustinbebustin/understudy/internal/protocoltest/gen/restjson"
	"github.com/sustinbebustin/understudy/internal/protocoltest/gen/restjsonvalidation"
	"github.com/sustinbebustin/understudy/internal/protocoltest/gen/restxml"
	"github.com/sustinbebustin/understudy/internal/protocoltest/gen/restxmlwithnamespace"
	"github.com/sustinbebustin/understudy/internal/protocoltest/gen/rpcv2protocol"
	"github.com/sustinbebustin/understudy/internal/protocoltest/gen/s3"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
)

func suites() []gen.Suite {
	return []gen.Suite{
		awsquery.Suite(), jsonprotocol.Suite(), jsonrpc10.Suite(), nonquerycompatiblerpcv2protocol.Suite(),
		querycompatiblejsonrpc10.Suite(), querycompatiblerpcv2protocol.Suite(), restjson.Suite(),
		restjsonvalidation.Suite(), restxml.Suite(), restxmlwithnamespace.Suite(), rpcv2protocol.Suite(), s3.Suite(),
	}
}

type counts struct{ requests, responses, malformed int }

// TestSuitesHoldEveryServerRunnableCase: the tables hold the server-runnable
// cases smithy-protocol-tests.md 3 counts for 1.74.0 (ARCHITECTURE.md 6.4),
// less the cases phase 1 does not generate: one Machine Learning awsJson1_1
// request, and four restJson1 requests: two Glacier and API Gateway
// customizations and two cases of aws.protocoltests.misc#AcceptHeaderStarService,
// which no corpus service binds. restJson1 malformed entries are expanded
// by their testParameters.
func TestSuitesHoldEveryServerRunnableCase(t *testing.T) {
	got := map[ref.ProtocolID]counts{}
	for _, s := range suites() {
		if _, err := registry.New(s.Service); err != nil {
			t.Fatalf("%s: %v", s.Service.ID, err)
		}
		for _, c := range s.Requests {
			n := got[c.Protocol]
			n.requests++
			got[c.Protocol] = n
		}
		for _, c := range s.Responses {
			n := got[c.Protocol]
			n.responses++
			got[c.Protocol] = n
		}
		for _, c := range s.Malformed {
			n := got[c.Protocol]
			n.malformed++
			got[c.Protocol] = n
		}
	}
	want := map[ref.ProtocolID]counts{
		ref.AWSJSON10: {26, 23, 0},
		ref.AWSJSON11: {58 - 1, 45, 0},
		ref.AWSQuery:  {35, 31, 0},
		ref.RESTJSON1: {137 - 4, 92, 655},
		ref.RESTXML:   {101, 77, 0},
		ref.RPCv2CBOR: {39, 29, 0},
	}
	for p, w := range want {
		if got[p] != w {
			t.Errorf("%s: requests, responses, malformed = %+v, want %+v", p, got[p], w)
		}
	}
	if len(got) != len(want) {
		t.Errorf("protocols = %v, want only %v", got, want)
	}
}

// TestNoClientOnlyCase: appliesTo client cases are the client's to run.
func TestNoClientOnlyCase(t *testing.T) {
	for _, s := range suites() {
		for _, c := range s.Requests {
			if c.AppliesTo != gen.Both && c.AppliesTo != gen.ServerOnly {
				t.Errorf("%s request %s applies to %v", s.Service.ID, c.ID, c.AppliesTo)
			}
		}
		for _, c := range s.Responses {
			if c.AppliesTo != gen.Both && c.AppliesTo != gen.ServerOnly {
				t.Errorf("%s response %s applies to %v", s.Service.ID, c.ID, c.AppliesTo)
			}
		}
	}
}
