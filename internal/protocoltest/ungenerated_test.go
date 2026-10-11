package protocoltest_test

import (
	"testing"

	"github.com/sustinbebustin/understudy/internal/protocoltest"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// The generated tables and the ungenerated cases together are the
// server-runnable cases ARCHITECTURE.md 6.4 counts, and no ungenerated
// case is in a table.
func TestUngeneratedCasesReconcileWithTheCorpusCounts(t *testing.T) {
	type count struct {
		p ref.ProtocolID
		k protocoltest.Kind
	}
	held := map[protocoltest.Key]bool{}
	got := map[count]int{}
	for _, cs := range corpusServices() {
		s := cs.suite()
		for _, c := range s.Requests {
			held[protocoltest.Key{Protocol: c.Protocol, Kind: protocoltest.Request, ID: c.ID}] = true
			got[count{c.Protocol, protocoltest.Request}]++
		}
		for _, c := range s.Responses {
			got[count{c.Protocol, protocoltest.Response}]++
		}
	}
	for _, u := range protocoltest.UngeneratedCases() {
		if held[u.Key] {
			t.Errorf("%s is listed as ungenerated but a generated table holds it", u.Key)
		}
		if u.Reason == "" {
			t.Errorf("%s has no reason", u.Key)
		}
		got[count{u.Key.Protocol, u.Key.Kind}]++
	}
	want := map[count]int{
		{ref.AWSJSON10, protocoltest.Request}: 26, {ref.AWSJSON10, protocoltest.Response}: 23,
		{ref.AWSJSON11, protocoltest.Request}: 58, {ref.AWSJSON11, protocoltest.Response}: 45,
		{ref.AWSQuery, protocoltest.Request}: 35, {ref.AWSQuery, protocoltest.Response}: 31,
		{ref.RESTJSON1, protocoltest.Request}: 137, {ref.RESTJSON1, protocoltest.Response}: 92,
		{ref.RESTXML, protocoltest.Request}: 101, {ref.RESTXML, protocoltest.Response}: 77,
		{ref.RPCv2CBOR, protocoltest.Request}: 39, {ref.RPCv2CBOR, protocoltest.Response}: 29,
	}
	for c, n := range want {
		if got[c] != n {
			t.Errorf("%s %s: %d generated and ungenerated cases, ARCHITECTURE.md 6.4 counts %d", c.p, c.k, got[c], n)
		}
	}
}
