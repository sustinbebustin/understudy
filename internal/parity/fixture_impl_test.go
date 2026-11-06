package parity

import (
	"errors"
	"testing"
)

// The members the format fixes, broken one at a time beyond what
// TestParseFixtureIsStrict covers (testdata/README.md, "Parity fixture
// format").
func TestParseFixtureRefusesMalformedMembers(t *testing.T) {
	t.Parallel()
	const head = `"step": "s", "client": "aws-cli", "exit": 0, "exchanges": `
	cases := map[string]string{
		"an unknown client":           `{"step": "s", "client": "wget", "exchanges": []}`,
		"an empty step":               `{"step": "", "client": "curl", "exchanges": []}`,
		"a member named twice":        `{"step": "s", "step": "t", "client": "curl", "exchanges": []}`,
		"a request without a url":     `{` + head + `[{"request": {"method": "GET", "headers": []}}]}`,
		"a header that is not a pair": `{` + head + `[{"response": {"status": 200, "headers": [["a", "b", "c"]]}}]}`,
		"bytes that are not base64":   `{` + head + `[{"response": {"status": 200, "headers": [], "body_base64": "!!"}}]}`,
		"an omitted body without sha": `{` + head + `[{"response": {"status": 200, "headers": [], "body_omitted": {"bytes": 3}}}]}`,
		"an unrecorded body sans len": `{` + head + `[{"response": {"status": 200, "headers": [], "body_unrecorded": {}}}]}`,
		"a negative unrecorded len":   `{` + head + `[{"response": {"status": 200, "headers": [], "body_unrecorded": {"bytes": -1}}}]}`,
		"an unknown body member":      `{` + head + `[{"response": {"status": 200, "headers": [], "body_omitted": {"bytes": 3, "sha256": "x", "md5": "y"}}}]}`,
		"an interim response member":  `{` + head + `[{"interim_responses": [{"status": 100, "headers": [], "x": 1}]}]}`,
		"data after the document":     `{"step": "s", "client": "curl", "exchanges": []} {}`,
	}
	for name, doc := range cases {
		_, err := ParseFixture([]byte(doc))
		if fe, ok := errors.AsType[*FixtureError](err); !ok || fe.Reason == "" {
			t.Errorf("%s: ParseFixture error = %v; want a *FixtureError with a reason", name, err)
		}
	}
}
