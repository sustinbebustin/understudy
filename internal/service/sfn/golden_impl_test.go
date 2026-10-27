package sfn_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	// Masks are LocalStack's key transforms: every member a key names, at
	// any depth of understudy's history, has its value replaced by the
	// string LocalStack recorded for it.
	Masks   map[string]string `json:"masks"`
	History json.RawMessage   `json:"history"`
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
				if g.LambdaPayload != nil {
					payload := []byte(compact(h, g.LambdaPayload))
					h.answer(func(_ *kernel.Call, _ *lambdaapi.InvokeInput) (*lambdaapi.InvokeOutput, *awserr.Error) {
						return &lambdaapi.InvokeOutput{StatusCode: 200, Payload: payload, ExecutedVersion: new("$LATEST")}, nil
					})
				}
				machine := h.create("golden", compact(h, g.Definition))
				arn := h.start(machine, "golden", new(compact(h, g.Input)))
				if g.Stop {
					must(h, sfnapi.OpStopExecution, &sfnapi.StopExecutionInput{ExecutionArn: arn})
					h.settled()
				}
				got := masked(undoTransforms(wireHistory(h, arn), h.roleArn()), g.Masks)
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

// withPlaceholders replaces the fixture's placeholders in every string of
// v with understudy's text for them.
func withPlaceholders(v document.Value, placeholders map[string]string) document.Value {
	switch v := v.(type) {
	case document.Map:
		out := make(document.Map, 0, len(v))
		for _, e := range v {
			out = append(out, document.Entry{Key: e.Key, Value: withPlaceholders(e.Value, placeholders)})
		}
		return out
	case document.List:
		out := make(document.List, 0, len(v))
		for _, e := range v {
			out = append(out, withPlaceholders(e, placeholders))
		}
		return out
	case document.String:
		s := string(v)
		for k, r := range placeholders {
			s = strings.ReplaceAll(s, k, r)
		}
		return document.String(s)
	case document.Number, document.Bool, document.Null:
	}
	return v
}
