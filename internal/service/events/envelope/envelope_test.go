package envelope_test

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/protocol/jsonx"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
	"github.com/sustinbebustin/understudy/internal/service/events/envelope"
)

// at is the instant the recorded events carry, with a fraction AWS's
// second-precision time drops.
var at = time.Date(2024, 5, 13, 12, 27, 7, 250_000_000, time.UTC)

// detail parses a Detail the way PutEvents does.
func detail(t *testing.T, s string) document.Map {
	t.Helper()
	m, ok := envelope.ParseDetail(s)
	if !ok {
		t.Fatalf("ParseDetail(%s) failed", s)
	}
	return m
}

// testEvent is the event of the recorded input tests
// (test_events_inputs.snapshot.json), with the given detail.
func testEvent(t *testing.T, d string) envelope.Event {
	t.Helper()
	return envelope.Event{
		ID:         "6b0f6e2a-2f0c-4f5e-9e4c-1c2d3e4f5a6b",
		DetailType: "core.update-account-command",
		Source:     "core.update-account-command",
		Account:    "111111111111",
		Time:       at,
		Region:     "us-east-1",
		Detail:     detail(t, d),
	}
}

// eventJSON is e's JSON, failing the test when it cannot be written.
func eventJSON(t *testing.T, e envelope.Event) string {
	t.Helper()
	b, err := e.JSON()
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}
	return string(b)
}

const (
	testDetail    = `{"command": "update-account", "payload": {"acc_id": "0a787ecb-4015", "sf_id": "baz"}}`
	specialDetail = `{"command": "update-account", "payload": {"acc_id": "0a787ecb-4015", "sf_id": "baz"}, "listsingle": ["HIGH"], "listmulti": ["ACTIVE", "INACTIVE"], "valid": true}`
)

// The delivered envelope has the recorded members in the recorded order,
// resources [] when none were sent and time to the second.
func TestEventJSONIsTheRecordedEnvelope(t *testing.T) {
	got := eventJSON(t, testEvent(t, testDetail))
	want := `{"version":"0","id":"6b0f6e2a-2f0c-4f5e-9e4c-1c2d3e4f5a6b","detail-type":"core.update-account-command","source":"core.update-account-command","account":"111111111111","time":"2024-05-13T12:27:07Z","region":"us-east-1","resources":[],"detail":{"command":"update-account","payload":{"acc_id":"0a787ecb-4015","sf_id":"baz"}}}`
	if got != want {
		t.Errorf("JSON =\n%s\nwant\n%s", got, want)
	}
}

// A scheduled rule's event is the documented one (lambda-events.md 5.2).
func TestScheduledEventIsTheDocumentedShape(t *testing.T) {
	e := envelope.Scheduled("89d1a02d-5ec7-412e-82f5-13505f849b41", "123456789012", "us-east-1",
		"arn:aws:events:us-east-1:123456789012:rule/SampleRule", time.Date(2016, 12, 30, 18, 44, 49, 0, time.UTC))
	want := `{"version":"0","id":"89d1a02d-5ec7-412e-82f5-13505f849b41","detail-type":"Scheduled Event","source":"aws.events","account":"123456789012","time":"2016-12-30T18:44:49Z","region":"us-east-1","resources":["arn:aws:events:us-east-1:123456789012:rule/SampleRule"],"detail":{}}`
	if got := eventJSON(t, e); got != want {
		t.Errorf("JSON =\n%s\nwant\n%s", got, want)
	}
}

// A Detail must be one JSON object; strings, arrays, broken JSON and JSON
// in a string are malformed (secrets-logs-events-alarms.md 4.7).
func TestParseDetailAcceptsOnlyAnObject(t *testing.T) {
	for _, s := range []string{`"string"`, `["a"]`, `{"a":`, `"{\"a\":1}"`, ``, `{} {}`, `123`} {
		if _, ok := envelope.ParseDetail(s); ok {
			t.Errorf("ParseDetail(%q) accepted", s)
		}
	}
	if _, ok := envelope.ParseDetail(` {"a": [1, 2.50, null]} `); !ok {
		t.Error("ParseDetail rejected an object")
	}
}

// InputPath selects a subtree and delivers its JSON: an object, a nested
// object, or a string with its quotes (the recorded test_put_events_with_input_path*).
func TestInputPathGoldens(t *testing.T) {
	tests := []struct {
		detail, path, want string
	}{
		{testDetail, "$.detail", `{"command":"update-account","payload":{"acc_id":"0a787ecb-4015","sf_id":"baz"}}`},
		{testDetail, "$.detail.payload", `{"acc_id":"0a787ecb-4015","sf_id":"baz"}`},
		{`{"command": "update-account", "payload": {"acc_id": "0a787ecb-4015", "payload": {"message": "baz", "id": "123"}}}`, "$.detail.payload", `{"acc_id":"0a787ecb-4015","payload":{"message":"baz","id":"123"}}`},
		{testDetail, "$.detail.payload.sf_id", `"baz"`},
		{specialDetail, "$.detail.listmulti[1]", `"INACTIVE"`},
		{specialDetail, "$.detail.listmulti[*]", `["ACTIVE","INACTIVE"]`},
		{testDetail, "$.detail.payload.*", `["0a787ecb-4015","baz"]`},
		{testDetail, "$.detail-type", `"core.update-account-command"`},
		{testDetail, "$", eventJSON(t, testEvent(t, testDetail))},
	}
	for _, tt := range tests {
		p, err := envelope.ParsePath(tt.path)
		if err != nil {
			t.Fatalf("ParsePath(%q): %v", tt.path, err)
		}
		got, ok, err := p.Payload(testEvent(t, tt.detail))
		if !ok || err != nil || string(got) != tt.want {
			t.Errorf("%s = %s, %v, %v; want %s", tt.path, got, ok, err, tt.want)
		}
	}
}

// A definite path that reaches nothing selects nothing; a wildcard path
// selects an empty list.
func TestInputPathMisses(t *testing.T) {
	for _, path := range []string{"$.detail.notinmessage", "$.detail.payload.sf_id.x", "$.resources[0]", "$.detail[0]"} {
		p, err := envelope.ParsePath(path)
		if err != nil {
			t.Fatalf("ParsePath(%q): %v", path, err)
		}
		if got, ok, err := p.Payload(testEvent(t, testDetail)); ok || err != nil {
			t.Errorf("%s selected %s, %v", path, got, err)
		}
	}
	p, err := envelope.ParsePath("$.detail.nothing[*]")
	if err != nil {
		t.Fatal(err)
	}
	if got, ok, err := p.Payload(testEvent(t, testDetail)); !ok || err != nil || string(got) != "[]" {
		t.Errorf("wildcard miss = %s, %v, %v; want []", got, ok, err)
	}
}

// Paths outside the documented subset are refused: bracket notation for
// names, filters, recursion, a missing root.
func TestParsePathRejects(t *testing.T) {
	for _, s := range []string{"", "detail", "$.", "$..detail", "$['detail']", "$.detail[", "$.detail[-1]", "$.detail[?(@.a)]", "$ .detail", "$.a b", "$[+1]"} {
		_, err := envelope.ParsePath(s)
		if pe, ok := errors.AsType[*envelope.PathError](err); !ok || pe.Path != s {
			t.Errorf("ParsePath(%q) = %v, want a *PathError naming it", s, err)
		}
	}
}

// A path built from a route through a generated document selects the
// value at the end of the route.
func TestPathSelectsTheRouteItWasBuiltFrom(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		leaf := document.String(rapid.StringMatching(`[a-z]{0,8}`).Draw(t, "leaf"))
		var v document.Value = leaf
		var route []string
		depth := rapid.IntRange(0, 5).Draw(t, "depth")
		for range depth {
			if rapid.Bool().Draw(t, "array") {
				n := rapid.IntRange(1, 4).Draw(t, "len")
				at := rapid.IntRange(0, n-1).Draw(t, "at")
				l := make(document.List, n)
				for i := range l {
					l[i] = document.Null{}
				}
				l[at] = v
				v = l
				route = append([]string{"[" + strconv.Itoa(at) + "]"}, route...)
				continue
			}
			name := rapid.StringMatching(`[A-Za-z0-9_/-]{1,6}`).Draw(t, "name")
			v = document.Map{{Key: name + "x", Value: document.Bool(true)}, {Key: name, Value: v}}
			route = append([]string{"." + name}, route...)
		}
		p, err := envelope.ParsePath("$" + strings.Join(route, ""))
		if err != nil {
			t.Fatal(err)
		}
		got, ok := p.Select(v)
		if !ok || got != document.Value(leaf) {
			t.Fatalf("Select = %v, %v; want %v", got, ok, leaf)
		}
	})
}

// transform renders template with variables over an event with detail.
func transform(t *testing.T, d string, paths map[string]string, template string) (string, bool) {
	t.Helper()
	tr, err := envelope.NewTransformer(paths, template)
	if err != nil {
		t.Fatalf("NewTransformer(%s): %v", template, err)
	}
	out, ok, err := tr.Render(testEvent(t, d), envelope.Context{
		RuleARN:  "arn:aws:events:us-east-1:111111111111:rule/test-rule",
		RuleName: "test-rule",
		Ingested: at,
	})
	if err != nil {
		t.Fatalf("Render(%s): %v", template, err)
	}
	return string(out), ok
}

// The recorded InputTransformer outputs of test_events_inputs.snapshot.json.
func TestInputTransformerGoldens(t *testing.T) {
	inputs := map[string]string{"detail-type": "$.detail-type", "timestamp": "$.time", "command": "$.detail.command"}
	missing := map[string]string{"detail-type": "$.detail-type", "timestamp": "$.time", "command": "$.detail.notinmessage"}
	special := map[string]string{
		"userId": "$.detail.payload.acc_id", "payload": "$.detail.payload", "systemstring": "$.detail.awsAccountId",
		"listsingle": "$.detail.listsingle", "listmulti": "$.detail.listmulti", "valid": "$.detail.valid",
	}
	tests := []struct {
		name, detail string
		paths        map[string]string
		template     string
		want         string
	}{
		{"string template", testDetail, inputs, `"Event of <detail-type> type, at time <timestamp>, info extracted from detail <command>"`, `"Event of core.update-account-command type, at time 2024-05-13T12:27:07Z, info extracted from detail update-account"`},
		{"string template, missing variable", testDetail, missing, `"Event of <detail-type> type, at time <timestamp>, info extracted from detail <command>"`, `"Event of core.update-account-command type, at time 2024-05-13T12:27:07Z, info extracted from detail "`},
		{"string template, special characters", testDetail, inputs, `"{[/Check with special starting characters for event of <detail-type> type"`, `"{[/Check with special starting characters for event of core.update-account-command type"`},
		{"json template", testDetail, inputs, `{"detailType": <detail-type>,  "time": <timestamp>, "command": <command>}`, `{"detailType": "core.update-account-command",  "time": "2024-05-13T12:27:07Z", "command": "update-account"}`},
		{"json template, missing variable", testDetail, missing, `{"detailType": <detail-type>,  "time": <timestamp>, "command": <command>}`, `{"detailType": "core.update-account-command",  "time": "2024-05-13T12:27:07Z", "command": ""}`},
		{"object in value position", specialDetail, special, `{"method": "PUT", "path": "users-service/users/<userId>", "bod": <payload>}`, `{"method": "PUT", "path": "users-service/users/0a787ecb-4015", "bod": {"acc_id":"0a787ecb-4015","sf_id":"baz"}}`},
		{"object in a string template", specialDetail, special, `"Payload of <payload> with path users-service/users/<userId> and <userId>"`, `"Payload of {acc_id:0a787ecb-4015,sf_id:baz} with path users-service/users/0a787ecb-4015 and 0a787ecb-4015"`},
		{"string quoted automatically", specialDetail, special, `{"id" : <userId>}`, `{"id" : "0a787ecb-4015"}`},
		{"string already quoted", specialDetail, special, `{"id" : "<userId>"}`, `{"id" : "0a787ecb-4015"}`},
		{"string in an array", specialDetail, special, `{"method": "PUT", "path": "users-service/users/<userId>", "bod": [<userId>, "hardcoded"]}`, `{"method": "PUT", "path": "users-service/users/0a787ecb-4015", "bod": ["0a787ecb-4015", "hardcoded"]}`},
		{"nested string", specialDetail, special, `{"method": "PUT", "nested": {"level1": {"level2": {"level3": "users-service/users/<userId>"} } }, "bod": "<userId>"}`, `{"method": "PUT", "nested": {"level1": {"level2": {"level3": "users-service/users/0a787ecb-4015"} } }, "bod": "0a787ecb-4015"}`},
		{"list in a string template", specialDetail, special, `"<listsingle> single list item"`, `"[HIGH] single list item"`},
		{"lists in a string template", specialDetail, special, `"<listmulti> multiple list items"`, `"[ACTIVE,INACTIVE] multiple list items"`},
		{"list in value position", specialDetail, special, `{"singlelistitem": <listsingle>}`, `{"singlelistitem": ["HIGH"]}`},
		{"every kind in a string template", specialDetail, special, `"<listsingle> single list item <listmulti> multiple list items <systemstring> system account id <payload> payload <userId> user id"`, `"[HIGH] single list item [ACTIVE,INACTIVE] multiple list items  system account id {acc_id:0a787ecb-4015,sf_id:baz} payload 0a787ecb-4015 user id"`},
		{"multi-line string template", specialDetail, special, "\"<listsingle> single list item\"\n\"<listmulti> multiple list items\"\n\"<systemstring> system account id\"", "\"[HIGH] single list item\"\n\"[ACTIVE,INACTIVE] multiple list items\"\n\" system account id\""},
		{"repeated variable", specialDetail, special, `{"multi_replacement": "users/<userId>/second/<userId>"}`, `{"multi_replacement": "users/0a787ecb-4015/second/0a787ecb-4015"}`},
		{"boolean in a string template", specialDetail, special, `"Command is <valid>!"`, `"Command is true!"`},
		{"reserved variables in a string template", testDetail, nil, `"Message containing all pre defined variables <aws.events.rule-arn> <aws.events.rule-name> <aws.events.event.ingestion-time>"`, `"Message containing all pre defined variables arn:aws:events:us-east-1:111111111111:rule/test-rule test-rule 2024-05-13T12:27:07Z"`},
		{"reserved event variables", testDetail, nil, `{"originalEvent": <aws.events.event>, "originalEventJson": <aws.events.event.json>}`, `{"originalEvent": {"id":"6b0f6e2a-2f0c-4f5e-9e4c-1c2d3e4f5a6b","account":"111111111111","detailType":"core.update-account-command","time":"2024-05-13T12:27:07Z","source":"core.update-account-command","region":"us-east-1","resources":[],"version":"0"}, "originalEventJson": ` + eventJSON(t, testEvent(t, testDetail)) + `}`},
	}
	for _, tt := range tests {
		got, ok := transform(t, tt.detail, tt.paths, tt.template)
		if !ok || got != tt.want {
			t.Errorf("%s: Render = %s, %v\nwant %s", tt.name, got, ok, tt.want)
		}
	}
}

// The recorded templates that deliver nothing: an object or array inside a
// string of a JSON template, and an unquoted text template, whose output
// is not JSON.
func TestInputTransformerDropsWhatAWSDoesNotDeliver(t *testing.T) {
	paths := map[string]string{"userId": "$.detail.payload.acc_id", "payload": "$.detail.payload", "listsingle": "$.detail.listsingle", "listmulti": "$.detail.listmulti"}
	for _, template := range []string{
		`{"not_valid": "users-service/users/<payload>", "bod": <payload>}`,
		`{"payload": "<payload>"}`,
		`{"singlelistitem": "<listsingle>"}`,
		`{"singlelistitem": "<listsingle>", "multiplelistitems": "<listmulti>"}`,
		`<listsingle> single list item`,
		`<listmulti> multiple list items`,
	} {
		if got, ok := transform(t, specialDetail, paths, template); ok {
			t.Errorf("Render(%s) delivered %s", template, got)
		}
	}
}

// Values are not escaped, so a string with a quote breaks a JSON template,
// and the delivery is dropped.
func TestInputTransformerDoesNotEscape(t *testing.T) {
	paths := map[string]string{"v": "$.detail.v"}
	if got, ok := transform(t, `{"v": "say \"hi\""}`, paths, `{"v": <v>}`); ok {
		t.Errorf("Render delivered %s", got)
	}
	got, ok := transform(t, `{"v": "say \"hi\""}`, paths, `"<v>"`)
	if !ok || got != `"say "hi""` {
		t.Errorf("string template = %s, %v; want the value raw", got, ok)
	}
}

// Every placeholder must name a variable or a reserved one; the error names
// the first undefined one (secrets-logs-events-alarms.md 4.6).
func TestNewTransformerRefusesAnUndefinedPlaceholder(t *testing.T) {
	_, err := envelope.NewTransformer(map[string]string{"detail-type": "$.detail-type"}, `"Event of <detail-type> type, with not defined key <notdefinedkey> <other>"`)
	var pe *envelope.PlaceholderError
	if !errors.As(err, &pe) || pe.Name != "notdefinedkey" {
		t.Fatalf("NewTransformer = %v, want a *PlaceholderError for notdefinedkey", err)
	}
	_, err = envelope.NewTransformer(map[string]string{"a": "$.detail", "b": "detail"}, `<a>`)
	var ve *envelope.VariableError
	if !errors.As(err, &ve) || ve.Name != "b" {
		t.Fatalf("NewTransformer = %v, want a *VariableError for b", err)
	}
	// Text in angle brackets that is not a name is literal.
	if got, ok := transform(t, testDetail, nil, `"a < b and <c d> and <>"`); !ok || got != `"a < b and <c d> and <>"` {
		t.Errorf("Render = %s, %v", got, ok)
	}
}

// jsonValue draws a JSON value whose strings are safe inside a JSON string:
// no quote, backslash or control character, as values are not escaped.
func jsonValue(depth int) *rapid.Generator[document.Value] {
	scalar := rapid.OneOf(
		rapid.Map(rapid.StringMatching(`[ -!#-\[\]-~é]{0,8}`), func(s string) document.Value { return document.String(s) }),
		rapid.Map(rapid.Int64(), func(n int64) document.Value { return document.Number(strconv.FormatInt(n, 10)) }),
		rapid.Map(rapid.Bool(), func(b bool) document.Value { return document.Bool(b) }),
		rapid.Just[document.Value](document.Null{}),
	)
	if depth == 0 {
		return scalar
	}
	return rapid.OneOf(scalar,
		rapid.Map(rapid.SliceOfN(jsonValue(depth-1), 0, 3), func(l []document.Value) document.Value { return document.List(l) }),
		rapid.Custom(func(t *rapid.T) document.Value {
			n := rapid.IntRange(0, 3).Draw(t, "members")
			m := make(document.Map, 0, n)
			for i := range n {
				m = append(m, document.Entry{Key: "k" + strconv.Itoa(i), Value: jsonValue(depth-1).Draw(t, "member")})
			}
			return m
		}),
	)
}

// A JSON template whose placeholders sit in value position, or inside
// strings for scalar variables, renders JSON for any event with unescaped-
// safe strings; and whatever any JSON template delivers parses as JSON.
func TestInputTransformerJSONTemplateRendersJSON(t *testing.T) {
	// Two reserved variables: a scalar and the event object, which is not
	// JSON-safe inside a string.
	const ruleName, event = "aws.events.rule-name", "aws.events.event"
	rapid.Check(t, func(t *rapid.T) {
		names := []string{"a", "b", "c"}
		values := make(document.Map, len(names))
		for i, n := range names {
			values[i] = document.Entry{Key: n, Value: jsonValue(2).Draw(t, n)}
		}
		paths := map[string]string{"a": "$.detail.a", "b": "$.detail.b", "c": "$.detail.c", "gone": "$.detail.gone"}
		var b strings.Builder
		b.WriteString("{")
		safe := true
		members := rapid.IntRange(1, 4).Draw(t, "members")
		for i := range members {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(`"m` + strconv.Itoa(i) + `": `)
			name := rapid.SampledFrom(append(names, "gone", ruleName, event)).Draw(t, "name")
			if rapid.Bool().Draw(t, "in string") {
				b.WriteString(`"x <` + name + `> y"`)
				v, _ := lookup(values, name)
				switch v.(type) {
				case document.Map, document.List:
					safe = false
				}
				if name == event {
					safe = false
				}
				continue
			}
			b.WriteString("<" + name + ">")
		}
		b.WriteString("}")
		tr, err := envelope.NewTransformer(paths, b.String())
		if err != nil {
			t.Fatal(err)
		}
		e := envelope.Event{ID: "id", DetailType: "t", Source: "s", Account: "111111111111", Time: at, Region: "us-east-1", Detail: values}
		out, ok, err := tr.Render(e, envelope.Context{RuleARN: "arn:aws:events:us-east-1:111111111111:rule/r", RuleName: "r", Ingested: at})
		if err != nil {
			t.Fatalf("Render(%s): %v", b.String(), err)
		}
		if safe && !ok {
			t.Fatalf("Render(%s) dropped the delivery", b.String())
		}
		if ok {
			d := jsonx.NewDecoder(out)
			if _, err := d.ReadValue(); err != nil || d.End() != nil {
				t.Fatalf("Render(%s) = %s, not JSON", b.String(), out)
			}
		}
	})
}

func lookup(m document.Map, name string) (document.Value, bool) {
	for _, e := range m {
		if e.Key == name {
			return e.Value, true
		}
	}
	return nil, false
}
