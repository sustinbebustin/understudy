package parity

import (
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// The absent-field rules for XML and the error fields: an attribute or the
// next sibling of a name is added as "x", an element with children is
// removed, and an error field the body lacks is added where the comparison
// reads it.
func TestMutateAddsAndRemovesWhatTheFieldNames(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		r     Response
		field Path
		want  string
	}{
		{"an absent attribute", xmlOK(`<r><a>1</a></r>`), "body/r/a/@k", `<r><a k="x">1</a></r>`},
		{"the next sibling of a name", xmlOK(`<r><a>1</a></r>`), "body/r/a[1]", `<r><a>1</a><a>x</a></r>`},
		{"an element with children", xmlOK(`<r><a><b>1</b></a><c>2</c></r>`), "body/r/a", `<r><c>2</c></r>`},
		{"escaped text", xmlOK(`<r><a>x &amp; 1</a></r>`), "body/r/a", `<r><a>x &amp; 2</a></r>`},
		{"an absent JSON error code", withType("application/x-amz-json-1.1", 400, `{"message":"m"}`), "error/code", `{"message":"m","__type":"x"}`},
		{"a JSON Message", withType("application/x-amz-json-1.1", 400, `{"Message":"m"}`), "error/message", `{"Message":"n"}`},
		{"an XML error code", withType("text/xml", 400, `<ErrorResponse><Error><Code>AccessDenied</Code></Error></ErrorResponse>`), "error/code", `<ErrorResponse><Error><Code>AccessDeniee</Code></Error></ErrorResponse>`},
		{"an absent XML error message", withType("text/xml", 400, `<ErrorResponse><Error><Code>C</Code></Error></ErrorResponse>`), "error/message", `<ErrorResponse><Error><Code>C</Code><Message>x</Message></Error></ErrorResponse>`},
	}
	for _, c := range cases {
		if got := bodyText(t, mutate(t, c.r, c.field)); got != c.want {
			t.Errorf("%s: Mutate(%s) body = %s; want %s", c.name, c.field, got, c.want)
		}
	}
}

// The mutated XML body reads back as the recording with one change, which
// the comparison sees at the settled path.
func TestMutateXMLKeepsTheRestOfTheDocument(t *testing.T) {
	t.Parallel()
	denied := recordedSide(t, stsAssumeDenied)
	got := Compare(mutate(t, denied, deniedError+"/Type"), denied, CompareOptions{})
	want := []Difference{{Path: deniedError + "/Type", Kind: Changed, Expected: "Sendes", Actual: "Sender"}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Compare(Mutate(Type), recording) (-want +got):\n%s", diff)
	}
}

func TestMutateRefusesWhatTheRecordingCannotHold(t *testing.T) {
	t.Parallel()
	omitted := Response{Status: 200, Body: OmittedBody{Bytes: 70000, SHA256: "0"}}
	cases := map[string]struct {
		r     Response
		field Path
	}{
		"a member of an omitted body":      {omitted, "body/a"},
		"a member of a text body":          {withType("text/plain", 200, "hello"), "body/a"},
		"a second root element":            {xmlOK(`<r/>`), "body/s"},
		"an array index past its end":      {jsonOK(`{"l":[1]}`), "body/l/2"},
		"below an attribute":               {xmlOK(`<r a="1"/>`), "body/r/@a/b"},
		"an XML error without its Error":   {withType("text/xml", 400, `<ErrorResponse/>`), "error/code"},
		"an exchange before the final one": {jsonOK(`{}`), "exchange/0/status"},
	}
	for name, c := range cases {
		if _, err := Mutate(c.r, c.field); !errors.As(err, new(*PathError)) {
			t.Errorf("%s: Mutate(%s) error = %v; want a *PathError", name, c.field, err)
		}
	}
}
