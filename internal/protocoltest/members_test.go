package protocoltest_test

import (
	"reflect"
	"testing"

	"github.com/sustinbebustin/understudy/internal/protocoltest"
	"github.com/sustinbebustin/understudy/internal/protocoltest/gen"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// Every error case's value names its own identity, so the bench can hand it
// to awserr.WithMembers without an identity that disagrees with it, and its
// service decodes members into a value of the same shape: the corpus
// services are generated with serde in both directions
// (smithy-protocol-tests.md 14.7).
func TestCorpusErrorValuesCarryTheirIdentity(t *testing.T) {
	checked := 0
	for _, cs := range corpusServices() {
		s := cs.suite()
		for i := range s.Responses {
			c := &s.Responses[i]
			o, ok := c.Outcome.(gen.Error)
			if !ok || o.Value == nil {
				continue
			}
			checked++
			if got := o.Value.ErrorIdentity(); got != o.ID {
				t.Errorf("%s: the value's ErrorIdentity is %s, the case's identity %s", c.ID, got.Shape, o.ID.Shape)
			}
			if s.Service.NewErrorMembers == nil {
				t.Errorf("%s: the %s descriptor has no NewErrorMembers", c.ID, s.Service.ID)
				continue
			}
			v, ok := s.Service.NewErrorMembers(o.ID.Shape)
			if !ok || reflect.TypeOf(v) != reflect.TypeOf(o.Value) {
				t.Errorf("%s: NewErrorMembers(%s) = %T, %t; want a new %T", c.ID, o.ID.Shape, v, ok, o.Value)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no corpus response case answers an error")
	}
}

// With structured error members in place, the corpus holds no known
// failure: the 14 gap cases pass and the restXml ComplexError case is a
// corpus bug, for the reason smithy-protocol-tests.md 14.1 gives.
func TestSkipsHoldNoGap(t *testing.T) {
	skips, err := protocoltest.CheckedInSkips()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range skips {
		if _, gap := s.Category.(protocoltest.Gap); gap {
			t.Errorf("%s is still skipped as a gap: %s", s.Key, s.Reason)
		}
	}
	key := protocoltest.Key{Protocol: ref.RESTXML, Kind: protocoltest.Response, ID: "ComplexError"}
	s, ok := skips.Lookup(key)
	const reason = "The expected body carries <Message>Hi</Message>, which ComplexError does not model and the params do not set; " +
		"a server sends only modeled members, as for InvalidGreetingError. No reference server runs the RestXml service (smithy-protocol-tests.md 9.2, 11.3)."
	if _, bug := s.Category.(protocoltest.CorpusBug); !ok || !bug || s.Reason != reason {
		t.Errorf("skip of %s = %+v, %t; want corpus-bug with reason %q", key, s, ok, reason)
	}
}
