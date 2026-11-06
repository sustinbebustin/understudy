package policycatalogue_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/policycatalogue"
)

// Every member of a policy object is required: one left out is a
// *DecodeError naming it, not the empty string. An empty description must
// be written as "".
func TestParseRefusesAMissingMember(t *testing.T) {
	for _, member := range []string{`"description": "",`, `"path": "/service-role/",`} {
		text := strings.Replace(canonical, "\n      "+member, "", 1)
		if text == canonical {
			t.Fatalf("the canonical text holds no %s", member)
		}
		c, err := policycatalogue.Parse([]byte(text))
		var de *policycatalogue.DecodeError
		if !errors.As(err, &de) || c != nil {
			t.Errorf("Parse without %s = %v, %v; want a *DecodeError", member, c, err)
			continue
		}
		if name, _, _ := strings.Cut(member, ":"); !strings.Contains(de.Error(), name) {
			t.Errorf("DecodeError %q does not name %s", de.Error(), name)
		}
	}
}

// policies.json's times are whole minutes (iam-evaluation.md 10.5): a
// fractional second, which time.Parse accepts after the seconds field, is
// refused.
func TestParseRefusesAFractionalSecond(t *testing.T) {
	text := strings.Replace(canonical, `"createDate": "2015-04-09T15:03:00Z"`, `"createDate": "2015-04-09T15:03:00.5Z"`, 1)
	if _, err := policycatalogue.Parse([]byte(text)); err == nil {
		t.Error("Parse accepted a createDate with a fractional second")
	}
}
