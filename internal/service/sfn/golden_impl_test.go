package sfn_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/api/sfnapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/protocol/awsjson"
	"github.com/sustinbebustin/understudy/internal/protocol/jsonx"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy/document"
)

// goldenFixture is one LocalStack history golden under testdata/localstack
// (spec "Done checks"): a definition, an input, and the history AWS
// recorded for them, as LocalStack's snapshot stores it.
type goldenFixture struct {
	Golden           int             `json:"golden"`
	Source           string          `json:"source"`
	Test             string          `json:"test"`
	Validated        string          `json:"validated"`
	DefinitionSource string          `json:"definitionSource"`
	Definition       json.RawMessage `json:"definition"`
	Input            json.RawMessage `json:"input"`
	// Placeholders maps a LocalStack placeholder in a recorded string to
	// understudy's text for it. LocalStack replaced the JSON a cause quotes
	// with <json-input:N>; AWS's serialisation of it is UNVERIFIED (ASL-04),
	// so the fixture names understudy's (stepfunctions-asl.md 15.4 item 16).
	Placeholders map[string]string `json:"placeholders"`
	// Stop says the recording stopped the execution with StopExecution,
	// without error or cause, once it had settled.
	Stop bool `json:"stop"`
	// LambdaPayload is the recorded function's output: the stub Lambda
	// answers every Invoke with it (spec "Done checks": a test double for
	// Lambda, never for SFN). Absent, the stub echoes the payload.
	LambdaPayload json.RawMessage `json:"lambdaPayload"`
	// LambdaAnswers, when given, are the recorded function's answers, one
	// per invocation in order, the last repeated: the stub Lambda gives
	// each in place of LambdaPayload.
	LambdaAnswers []goldenAnswer `json:"lambdaAnswers"`
	// Advance is how far the emulator clock moves, a second at a time,
	// once the execution started, so retry delays pass.
	Advance int `json:"advance"`
	// Masks are LocalStack's key transforms: every member a key names, at
	// any depth of understudy's history, has its value replaced by the
	// string LocalStack recorded for it.
	Masks map[string]string `json:"masks"`
	// Transforms are LocalStack's regex transforms (its RegexTransformer
	// and request-id replacements): each pattern is replaced in every
	// string of understudy's history.
	Transforms []goldenTransform `json:"transforms"`
	// Omit names members AWS added to the context object after the
	// recording date; they are removed from understudy's history at any
	// depth before comparing.
	Omit    []string        `json:"omit"`
	History json.RawMessage `json:"history"`
}

// goldenAnswer is one answer of the stub Lambda: a function error with
// the payload text as recorded, a payload that is a JSON string of
// LargeString "a" characters, or Lambda's ResourceNotFoundException for a
// function that does not exist.
type goldenAnswer struct {
	FunctionError string `json:"functionError"`
	Payload       string `json:"payload"`
	LargeString   int    `json:"largeString"`
	NotFound      bool   `json:"notFound"`
}

type goldenTransform struct {
	Pattern string `json:"pattern"`
	Replace string `json:"replace"`
}

// stub answers the i-th invocation, counted from 0, of in as the fixture
// says. A missing function gets Lambda's recorded message, which names
// the function's ARN and $LATEST (aws-recorded localstack@8b9a79f
// v2/error_handling/test_task_service_lambda.snapshot.json:L1064).
func (g *goldenFixture) stub(h *harness, i int, in *lambdaapi.InvokeInput) (*lambdaapi.InvokeOutput, *awserr.Error) {
	a := g.LambdaAnswers[min(i, len(g.LambdaAnswers)-1)]
	out := &lambdaapi.InvokeOutput{StatusCode: 200, Payload: []byte(a.Payload), ExecutedVersion: new("$LATEST")}
	switch {
	case a.NotFound:
		return nil, lambdaapi.ErrResourceNotFoundException("Function not found: " + h.functionArn(in.FunctionName) + ":$LATEST")
	case a.LargeString > 0:
		out.Payload = []byte(`"` + strings.Repeat("a", a.LargeString) + `"`)
	case a.FunctionError != "":
		out.FunctionError = new(a.FunctionError)
	}
	return out, nil
}

// TestLocalStackHistoryGoldens replays every fixture under
// testdata/localstack through the service and compares the history, as
// GetExecutionHistory writes it on the wire, with the recorded one at the
// parsed level, LocalStack's transforms undone (stepfunctions-asl.md 1.2):
// timestamps and the role ARN are placeholders, input and output are
// parsed JSON, and objects compare as member sets.
func TestLocalStackHistoryGoldens(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("testdata", "localstack", "*.json"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no golden fixtures under testdata/localstack (%v)", err)
	}
	for _, path := range paths {
		t.Run(strings.TrimSuffix(filepath.Base(path), ".json"), func(t *testing.T) {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var g goldenFixture
			if err := json.Unmarshal(raw, &g); err != nil {
				t.Fatalf("fixture %s: %v", path, err)
			}
			if g.Golden == 0 || !strings.HasPrefix(g.Source, "localstack@8b9a79f ") || g.Validated == "" {
				t.Fatalf("fixture %s lacks its golden number, localstack@8b9a79f source or validation date", path)
			}
			run(t, func(h *harness) {
				switch {
				case len(g.LambdaAnswers) > 0:
					var calls atomic.Int64
					h.answer(func(_ *kernel.Call, in *lambdaapi.InvokeInput) (*lambdaapi.InvokeOutput, *awserr.Error) {
						return g.stub(h, int(calls.Add(1)-1), in)
					})
				case g.LambdaPayload != nil:
					payload := []byte(compact(h, g.LambdaPayload))
					h.answer(func(_ *kernel.Call, _ *lambdaapi.InvokeInput) (*lambdaapi.InvokeOutput, *awserr.Error) {
						return &lambdaapi.InvokeOutput{StatusCode: 200, Payload: payload, ExecutedVersion: new("$LATEST")}, nil
					})
				}
				machine := h.create("golden", compact(h, g.Definition))
				arn := h.start(machine, "golden", new(compact(h, g.Input)))
				// One second at a time: an invocation's answer arrives in
				// tracked work after the clock moved, and the retry it arms
				// must be due at its own instant, not after one long jump.
				for range g.Advance {
					h.advance(time.Second)
				}
				if g.Stop {
					must(h, sfnapi.OpStopExecution, &sfnapi.StopExecutionInput{ExecutionArn: arn})
					h.settled()
				}
				got := masked(undoTransforms(wireHistory(h, arn), h.roleArn()), g.Masks)
				got = omitted(transformed(h, got, g.Transforms), g.Omit)
				want := withPlaceholders(decode(h, string(g.History)), g.Placeholders)
				if !equal(got, want) {
					h.Fatalf("history of golden %d (%s, validated %s):\n%s\nwant:\n%s", g.Golden, g.Source, g.Validated, docText(got), docText(want))
				}
			})
		})
	}
}

// compact is raw JSON text in compact form, member order kept.
func compact(tb testing.TB, raw json.RawMessage) string {
	tb.Helper()
	return docText(decode(tb, string(raw)))
}

// wireHistory is the execution's history as GetExecutionHistory's awsJson
// body carries it: the events member of the encoded output.
func wireHistory(h *harness, arn string) document.Value {
	h.Helper()
	out := &sfnapi.GetExecutionHistoryOutput{Events: h.history(arn)}
	resp, err := awsjson.Codec{Version: awsjson.V10}.Encode(sfnapi.OpGetExecutionHistory.Descriptor(), "golden", out, 200)
	if err != nil {
		h.Fatalf("encoding the history: %s", err.Message)
	}
	return at(decode(h, string(resp.Body)), "events")
}

// undoTransforms rewrites a wire history the way LocalStack's snapshot
// transforms rewrote AWS's: every timestamp becomes "timestamp", the role
// ARN "snf_role_arn", and the input and output JSON texts of event details
// their parsed values.
func undoTransforms(events document.Value, role string) document.Value {
	list, ok := events.(document.List)
	if !ok {
		return events
	}
	out := make(document.List, 0, len(list))
	for _, e := range list {
		m, ok := e.(document.Map)
		if !ok {
			out = append(out, e)
			continue
		}
		event := make(document.Map, 0, len(m))
		for _, entry := range m {
			switch {
			case entry.Key == "timestamp":
				entry.Value = document.String("timestamp")
			case strings.HasSuffix(entry.Key, "EventDetails"):
				entry.Value = undoDetails(entry.Value, role)
			}
			event = append(event, entry)
		}
		out = append(out, event)
	}
	return out
}

func undoDetails(v document.Value, role string) document.Value {
	m, ok := v.(document.Map)
	if !ok {
		return v
	}
	out := make(document.Map, 0, len(m))
	for _, entry := range m {
		s, isString := entry.Value.(document.String)
		switch {
		case isString && (entry.Key == "input" || entry.Key == "output" || entry.Key == "parameters"):
			if parsed, ok := parseJSON(string(s)); ok {
				entry.Value = parsed
			}
		case isString && entry.Key == "cause":
			// LocalStack parses a cause that is a JSON object, such as a
			// function error's payload, and keeps any other text.
			if parsed, ok := parseJSON(string(s)); ok {
				if _, isObject := parsed.(document.Map); isObject {
					entry.Value = parsed
				}
			}
		case isString && string(s) == role:
			entry.Value = document.String("snf_role_arn")
		}
		out = append(out, entry)
	}
	return out
}

// parseJSON reads JSON text; false when s is not JSON.
func parseJSON(s string) (document.Value, bool) {
	d := jsonx.NewDecoder([]byte(s))
	v, err := d.ReadValue()
	if err == nil {
		err = d.End()
	}
	return v, err == nil
}

// docText is v's compact JSON text, member order kept.
func docText(v document.Value) string {
	if v == nil {
		return "<nil>"
	}
	e := jsonx.NewEncoder(jsonx.WireOrder)
	e.Value(v)
	b, err := e.Bytes()
	if err != nil {
		return "<unencodable: " + err.Error() + ">"
	}
	return string(b)
}

// masked replaces the value of every member masks names, at any depth of
// v, with the string masks gives for it.
func masked(v document.Value, masks map[string]string) document.Value {
	switch v := v.(type) {
	case document.Map:
		out := make(document.Map, 0, len(v))
		for _, e := range v {
			if s, ok := masks[e.Key]; ok {
				out = append(out, document.Entry{Key: e.Key, Value: document.String(s)})
				continue
			}
			out = append(out, document.Entry{Key: e.Key, Value: masked(e.Value, masks)})
		}
		return out
	case document.List:
		out := make(document.List, 0, len(v))
		for _, e := range v {
			out = append(out, masked(e, masks))
		}
		return out
	case document.String, document.Number, document.Bool, document.Null:
	}
	return v
}

// transformed applies the fixture's regex transforms to every string of v.
func transformed(tb testing.TB, v document.Value, transforms []goldenTransform) document.Value {
	tb.Helper()
	for _, t := range transforms {
		re, err := regexp.Compile(t.Pattern)
		if err != nil {
			tb.Fatalf("fixture transform %q: %v", t.Pattern, err)
		}
		v = mapStrings(v, func(s string) string { return re.ReplaceAllString(s, t.Replace) })
	}
	return v
}

// omitted removes every member the keys name, at any depth of v.
func omitted(v document.Value, keys []string) document.Value {
	switch v := v.(type) {
	case document.Map:
		out := make(document.Map, 0, len(v))
		for _, e := range v {
			if !slices.Contains(keys, e.Key) {
				out = append(out, document.Entry{Key: e.Key, Value: omitted(e.Value, keys)})
			}
		}
		return out
	case document.List:
		out := make(document.List, 0, len(v))
		for _, e := range v {
			out = append(out, omitted(e, keys))
		}
		return out
	case document.String, document.Number, document.Bool, document.Null:
	}
	return v
}

// withPlaceholders replaces the fixture's placeholders in every string of
// v with understudy's text for them.
func withPlaceholders(v document.Value, placeholders map[string]string) document.Value {
	return mapStrings(v, func(s string) string {
		for k, r := range placeholders {
			s = strings.ReplaceAll(s, k, r)
		}
		return s
	})
}

// mapStrings applies fn to every string of v, member names aside.
func mapStrings(v document.Value, fn func(string) string) document.Value {
	switch v := v.(type) {
	case document.Map:
		out := make(document.Map, 0, len(v))
		for _, e := range v {
			out = append(out, document.Entry{Key: e.Key, Value: mapStrings(e.Value, fn)})
		}
		return out
	case document.List:
		out := make(document.List, 0, len(v))
		for _, e := range v {
			out = append(out, mapStrings(e, fn))
		}
		return out
	case document.String:
		return document.String(fn(string(v)))
	case document.Number, document.Bool, document.Null:
	}
	return v
}
