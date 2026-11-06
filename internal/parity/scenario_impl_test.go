package parity

import (
	"bytes"
	"errors"
	"maps"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/google/go-cmp/cmp"
	"pgregory.net/rapid"
)

// canonicalScenario and canonicalScenarioFile pin the canonical layout:
// every member on its own line at two-space indentation, nested setup input
// and shapes included.
func canonicalScenario() Scenario {
	return Scenario{
		Version: 1, Item: "R01", Account: "123456789012", Region: "us-east-2", Client: AWSCLI,
		Setup: []Call{{
			Service: "sqs", Operation: "CreateQueue",
			Input: `{"QueueName":"q","Attributes":{"VisibilityTimeout":"30"}}`,
			Bind:  []SetupBind{{Path: "body/QueueUrl", Shape: URLShape{Path: "/{account}/{name}"}, AWS: "https://sqs.us-east-2.amazonaws.com/123456789012/q"}},
		}},
		Steps: []Step{{
			Fixture: "sqs/R01/02-recv1.json", Compare: CompareFinal,
			Bind:    []Bind{{Path: "body/Messages/0/ReceiptHandle", Shape: IDShape{Alphabet: Base64, Min: 36, Max: 1024, Prefix: "AQEB"}}},
			Advance: 90 * time.Second,
		}},
		Settles: []Claim{{Target: SwitchTarget{Name: "sqs.receipt.stale-handle"}, Step: "02-recv1", Field: "body/Messages/0/ReceiptHandle"}},
	}
}

const canonicalScenarioFile = `{
  "version": 1,
  "item": "R01",
  "account": "123456789012",
  "region": "us-east-2",
  "client": "aws-cli",
  "setup": [
    {
      "service": "sqs",
      "operation": "CreateQueue",
      "input": {
        "QueueName": "q",
        "Attributes": {
          "VisibilityTimeout": "30"
        }
      },
      "bind": [
        {
          "path": "body/QueueUrl",
          "shape": {
            "kind": "url",
            "path": "/{account}/{name}"
          },
          "aws": "https://sqs.us-east-2.amazonaws.com/123456789012/q"
        }
      ]
    }
  ],
  "steps": [
    {
      "fixture": "sqs/R01/02-recv1.json",
      "compare": "final",
      "bind": [
        {
          "path": "body/Messages/0/ReceiptHandle",
          "shape": {
            "kind": "id",
            "alphabet": "base64",
            "min": 36,
            "max": 1024,
            "prefix": "AQEB"
          }
        }
      ],
      "advance": "1m30s"
    }
  ],
  "settles": [
    {
      "switch": "sqs.receipt.stale-handle",
      "step": "02-recv1",
      "field": "body/Messages/0/ReceiptHandle"
    }
  ]
}
`

func TestScenarioEncodeWritesTheCanonicalLayout(t *testing.T) {
	t.Parallel()
	got, err := canonicalScenario().Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if diff := cmp.Diff(canonicalScenarioFile, string(got)); diff != "" {
		t.Errorf("Encode (-want +got):\n%s", diff)
	}
	back, err := ParseScenario(got)
	if err != nil {
		t.Fatalf("ParseScenario(Encode): %v", err)
	}
	if diff := cmp.Diff(canonicalScenario(), back, equateEmpty); diff != "" {
		t.Errorf("ParseScenario(Encode) (-want +got):\n%s", diff)
	}
}

// uEscape is the JSON escape of the code point hex, built at run time so
// the source holds no escape an editor could decode.
func uEscape(hex string) string { return string(rune(0x5c)) + "u" + hex }

// A setup input is held compact with its members in the file's order, its
// strings in their shortest encoding and its numbers as written: a 64-bit
// id beyond float64 precision reaches the request unrounded.
func TestParseScenarioHoldsTheInputAsWritten(t *testing.T) {
	t.Parallel()
	doc := scenarioDoc()
	doc.top["setup"] = []any{map[string]any{"service": "kms", "operation": "CreateKey", "input": map[string]any{}}}
	data := strings.Replace(string(doc.marshal(t)), `"input":{}`,
		`"input": { "Z" : "`+uEscape("0041")+`\/b", "N": 9007199254740993, "F": 1.0E3, "Nested": {"B": [1, 2], "A": null} }`, 1)
	got, err := ParseScenario([]byte(data))
	if err != nil {
		t.Fatalf("ParseScenario: %v\n%s", err, data)
	}
	if want := `{"Z":"A/b","N":9007199254740993,"F":1.0E3,"Nested":{"B":[1,2],"A":null}}`; len(got.Setup) != 1 || got.Setup[0].Input != want {
		t.Fatalf("Setup = %+v; want one call with input %s", got.Setup, want)
	}
	enc, err := got.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	back, err := ParseScenario(enc)
	if err != nil || back.Setup[0].Input != got.Setup[0].Input {
		t.Errorf("ParseScenario(Encode) = %+v, %v; want input %s", back.Setup, err, got.Setup[0].Input)
	}
}

// More of the format's rules, each case breaking one member of a valid
// document; the field is matched exactly.
func TestParseScenarioLocatesEachRefusal(t *testing.T) {
	t.Parallel()
	uuid := map[string]any{"kind": "uuid"}
	bind := func(shape any) func(*scenarioDocument) {
		return func(d *scenarioDocument) {
			d.steps[0]["bind"] = []any{map[string]any{"path": "body/x", "shape": shape}}
		}
	}
	setup := func(call map[string]any) func(*scenarioDocument) {
		return func(d *scenarioDocument) {
			c := map[string]any{"service": "kms", "operation": "CreateKey", "input": map[string]any{}}
			maps.Copy(c, call)
			d.top["setup"] = []any{c}
		}
	}
	settle := func(entry map[string]any) func(*scenarioDocument) {
		return func(d *scenarioDocument) {
			e := map[string]any{"step": "01-duplicate", "field": "status"}
			maps.Copy(e, entry)
			d.top["settles"] = []any{e}
		}
	}
	record := func(entry map[string]any) func(*scenarioDocument) {
		return func(d *scenarioDocument) { d.top["record"] = []any{entry} }
	}
	cases := []struct {
		name  string
		edit  func(*scenarioDocument)
		field string
	}{
		{"version a string", func(d *scenarioDocument) { d.top["version"] = "1" }, "version"},
		{"needs an object", func(d *scenarioDocument) { d.top["needs"] = map[string]any{} }, "needs"},
		{"need listed twice", func(d *scenarioDocument) { d.top["needs"] = []any{"docker", "docker"} }, "needs[1]"},
		{"item with a slash", func(d *scenarioDocument) { d.top["item"] = "KMS/alias" }, "item"},
		{"region without a number", func(d *scenarioDocument) { d.top["region"] = "us-east" }, "region"},
		{"shape not an object", bind("uuid"), "steps[0].bind[0].shape"},
		{"shape without a kind", bind(map[string]any{}), "steps[0].bind[0].shape"},
		{"shape kind not a string", bind(map[string]any{"kind": 1}), "steps[0].bind[0].shape.kind"},
		{"uuid shape with an id member", bind(map[string]any{"kind": "uuid", "min": 1}), "steps[0].bind[0].shape.min"},
		{"id shape max below its prefix", bind(map[string]any{"kind": "id", "alphabet": "base64", "min": 1, "max": 3, "prefix": "AQEB"}), "steps[0].bind[0].shape.max"},
		{"id shape min not an integer", bind(map[string]any{"kind": "id", "alphabet": "hex", "min": 1.5, "max": 8}), "steps[0].bind[0].shape.min"},
		{"arn shape service", bind(map[string]any{"kind": "arn", "service": "KMS"}), "steps[0].bind[0].shape.service"},
		{"arn shape resource with a separator", bind(map[string]any{"kind": "arn", "service": "kms", "resource": "key/x"}), "steps[0].bind[0].shape.resource"},
		{"arn shape name kind", bind(map[string]any{"kind": "arn", "service": "kms", "resource": "key", "name": map[string]any{"kind": "guid"}}), "steps[0].bind[0].shape.name.kind"},
		{"arn shape name rule", bind(map[string]any{"kind": "arn", "service": "kms", "resource": "key", "name": map[string]any{"kind": "id", "alphabet": "hex", "min": 0, "max": 1}}), "steps[0].bind[0].shape.name.min"},
		{"url shape relative", bind(map[string]any{"kind": "url", "path": "{account}/{name}"}), "steps[0].bind[0].shape.path"},
		{"url shape unknown placeholder", bind(map[string]any{"kind": "url", "path": "/{acct}/{name}"}), "steps[0].bind[0].shape.path"},
		{"bind without a shape", func(d *scenarioDocument) { d.steps[0]["bind"] = []any{map[string]any{"path": "body/x"}} }, "steps[0].bind[0].shape"},
		{"step bind with an aws value", func(d *scenarioDocument) {
			d.steps[0]["bind"] = []any{map[string]any{"path": "body/x", "shape": uuid, "aws": "v"}}
		}, "steps[0].bind[0].aws"},
		{"bind on an earlier exchange", func(d *scenarioDocument) {
			d.steps[0]["bind"] = []any{map[string]any{"path": "exchange/0/body/x", "shape": uuid}}
		}, "steps[0].bind[0].path"},
		{"advance not a string", func(d *scenarioDocument) { d.steps[0]["advance"] = 6 }, "steps[0].advance"},
		{"setup service", setup(map[string]any{"service": "KMS"}), "setup[0].service"},
		{"setup operation", setup(map[string]any{"operation": "createKey"}), "setup[0].operation"},
		{"setup without an input", func(d *scenarioDocument) {
			d.top["setup"] = []any{map[string]any{"service": "kms", "operation": "CreateKey"}}
		}, "setup[0].input"},
		{"setup input an array", setup(map[string]any{"input": []any{}}), "setup[0].input"},
		{"setup bind with an empty aws value", setup(map[string]any{"bind": []any{map[string]any{"path": "body/KeyMetadata/KeyId", "shape": uuid, "aws": ""}}}), "setup[0].bind[0].aws"},
		{"setup bind on a group", setup(map[string]any{"bind": []any{map[string]any{"path": "error", "shape": uuid, "aws": "v"}}}), "setup[0].bind[0].path"},
		{"switch with covers", settle(map[string]any{"switch": "kms.queued", "covers": []any{"dev.understudy#statusOverride"}}), "settles[0].covers"},
		{"switch name", settle(map[string]any{"switch": "Kms"}), "settles[0].switch"},
		{"covers without #", settle(map[string]any{"overlay": "com.amazonaws.kms#X", "covers": []any{"statusOverride"}}), "settles[0].covers[0]"},
		{"settles an earlier exchange", settle(map[string]any{"switch": "kms.queued", "field": "exchange/0/status"}), "settles[0].field"},
		{"rec command with a capture", record(map[string]any{"step": "01-duplicate", "argv": []any{"aws", "kms", "list-keys"}, "capture": "K"}), "record[0].capture"},
		{"rec command of an unknown step", record(map[string]any{"step": "09-none", "argv": []any{"aws"}}), "record[0].step"},
		{"run command without argv", record(map[string]any{"capture": "K"}), "record[0].argv"},
	}
	for _, c := range cases {
		doc := scenarioDoc()
		c.edit(doc)
		_, err := ParseScenario(doc.marshal(t))
		se, ok := errors.AsType[*ScenarioError](err)
		if !ok {
			t.Errorf("%s: ParseScenario error = %v; want a *ScenarioError", c.name, err)
			continue
		}
		if se.Field != c.field || se.Reason == "" {
			t.Errorf("%s: ScenarioError{Field: %q, Reason: %q}; want field %q and a reason", c.name, se.Field, se.Reason, c.field)
		}
	}
}

func TestParseScenarioRefusesWhatIsNotOneDocument(t *testing.T) {
	t.Parallel()
	valid := string(scenarioDoc().marshal(t))
	for _, data := range []string{
		"",
		"[]",
		valid + " {}",
		strings.Replace(valid, `"version":1`, `"version":1,"version":1`, 1),
	} {
		_, err := ParseScenario([]byte(data))
		if se, ok := errors.AsType[*ScenarioError](err); !ok || se.Reason == "" {
			t.Errorf("ParseScenario(%.40q) error = %v; want a *ScenarioError with a reason", data, err)
		}
	}
}

func TestReadScenarioNamesTheScenario(t *testing.T) {
	t.Parallel()
	doc := scenarioDoc()
	doc.top["version"] = 2
	fsys := fstest.MapFS{"kms/KMS-alias/scenario.json": {Data: doc.marshal(t)}}
	_, err := ReadScenario(fsys, "kms/KMS-alias")
	se, ok := errors.AsType[*ScenarioError](err)
	if !ok || se.Scenario != "kms/KMS-alias" || se.Field != "version" {
		t.Fatalf("ReadScenario error = %v; want a *ScenarioError for kms/KMS-alias at version", err)
	}
	if got := se.Error(); !strings.HasPrefix(got, "testdata/parity/kms/KMS-alias/scenario.json: version: ") {
		t.Errorf("Error() = %q; want it to name the file and the member", got)
	}
	for _, id := range []string{"kms", "kms/KMS-alias/extra", "../kms/KMS-alias", "KMS/alias"} {
		if _, err := ReadScenario(fsys, id); !errors.As(err, new(*ScenarioError)) {
			t.Errorf("ReadScenario(%q) error = %v; want a *ScenarioError for an id that is not <svc>/<item>", id, err)
		}
	}
}

func TestScenarioEncodeRefusesWhatParseScenarioRefuses(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		edit  func(*Scenario)
		field string
	}{
		{"version", func(s *Scenario) { s.Version = 2 }, "version"},
		{"input with whitespace", func(s *Scenario) { s.Setup[0].Input = `{ "QueueName": "q" }` }, "setup[0].input"},
		{"input with an escape ParseScenario shortens", func(s *Scenario) { s.Setup[0].Input = `{"QueueName":"` + uEscape("0071") + `"}` }, "setup[0].input"},
		{"no shape", func(s *Scenario) { s.Steps[0].Bind[0].Shape = nil }, "steps[0].bind[0].shape"},
		{"negative advance", func(s *Scenario) { s.Steps[0].Advance = -time.Second }, "steps[0].advance"},
		{"no target", func(s *Scenario) { s.Settles[0].Target = nil }, "settles[0]"},
		{"rec command without a step", func(s *Scenario) { s.Record = []Command{RecCommand{Argv: []string{"aws"}}} }, "record[0].step"},
		{"nil command", func(s *Scenario) { s.Record = []Command{nil} }, "record[0]"},
	}
	for _, c := range cases {
		s := canonicalScenario()
		c.edit(&s)
		_, err := s.Encode()
		if se, ok := errors.AsType[*ScenarioError](err); !ok || se.Field != c.field {
			t.Errorf("%s: Encode error = %v; want a *ScenarioError at %s", c.name, err, c.field)
		}
	}
}

// damage applies one drawn edit that may make a valid scenario invalid.
func damage(t *rapid.T, s *Scenario) {
	if len(s.Steps) == 0 {
		return
	}
	pick := func(label string, xs ...string) string { return rapid.SampledFrom(xs).Draw(t, label) }
	edits := []func(){
		func() { s.Version = rapid.IntRange(-1, 3).Draw(t, "version") },
		func() { s.Item = pick("item", "", "a/b", "R01", "-") },
		func() { s.Account = pick("account", "", "12345678901", "1234567890123", "123456789012") },
		func() { s.Region = pick("region", "", "US-EAST-2", "us-east", "eu-west-1") },
		func() { s.Client = Client(pick("client", "", "wget", "boto3")) },
		func() { s.Needs = append(s.Needs, Need(pick("need", "docker", "gpu"))) },
		func() { s.Steps = nil },
		func() { s.Steps = append(s.Steps, s.Steps[0]) },
		func() { s.Steps[0].Advance = time.Duration(rapid.Int64Range(-2, 2).Draw(t, "advance")) },
		func() { s.Steps[0].Compare = CompareMode(pick("compare", "", "some", "all")) },
		func() { s.Steps[0].Fixture = pick("fixture", "x.json", "kms/a/scenario.json", "kms/a/b.json") },
		func() {
			s.Steps[0].Bind = append(s.Steps[0].Bind, Bind{
				Path: Path(pick("bind path", "error", "header", "exchange/0/status", "Body", "body/x")),
				Shape: rapid.SampledFrom([]Shape{
					nil,
					UUIDShape{},
					IDShape{Alphabet: Hex, Min: 0, Max: 1},
					IDShape{Alphabet: "x", Min: 1, Max: 1},
					IDShape{Alphabet: Hex, Min: 2, Max: 1},
					IDShape{Alphabet: Hex, Min: 1, Max: 2, Prefix: "ABC"},
					ARNShape{Service: "", Resource: "key"},
					ARNShape{Service: "kms", Resource: "key/x"},
					ARNShape{Service: "kms", Name: IDShape{}},
					URLShape{Path: "x"},
					URLShape{Path: "/{x}"},
				}).Draw(t, "bind shape"),
			})
		},
		func() {
			s.Setup = append(s.Setup, Call{
				Service: pick("service", "kms", "KMS", ""), Operation: pick("operation", "CreateKey", "createKey"),
				Input: pick("input", "", "[]", "{", `{ "a": 1 }`, `{"a":1,"a":2}`, `{"n":9007199254740993,"f":1.0E3}`,
					`{"a":"`+uEscape("0041")+`"}`, `{"a":"`+uEscape("2028")+`"}`, `{"a":"`+uEscape("0022")+`"}`),
				Before: pick("before", "", "nope"),
			})
		},
		func() {
			s.Waivers = append(s.Waivers, Waiver{Step: pick("waiver step", "", "nope"), Field: Path(pick("waiver field", "x", "status")), Reason: pick("waiver reason", "", "r")})
		},
		func() {
			s.Settles = append(s.Settles, Claim{
				Target: rapid.SampledFrom([]Target{nil, SwitchTarget{}, SwitchTarget{Name: "a.b"}, OverlayTarget{Shape: "x"}, OverlayTarget{Shape: "a#B", Covers: []string{"c"}}}).Draw(t, "target"),
				Step:   s.Steps[0].Name(),
				Field:  Path(pick("settles field", "header", "status", "exchange/1/status")),
			})
		},
		func() {
			s.Record = append(s.Record, rapid.SampledFrom([]Command{
				nil,
				RecCommand{Argv: []string{"aws"}},
				RecCommand{Step: s.Steps[0].Name()},
				RunCommand{},
				RunCommand{Argv: []string{"aws"}, Capture: "x"},
				RunCommand{Argv: []string{"aws", "\xff"}},
			}).Draw(t, "command"))
		},
		func() {
			s.Waivers = append(s.Waivers, Waiver{Step: s.Steps[0].Name(), Field: "status", Reason: "\xff"})
		},
	}
	edits[rapid.IntRange(0, len(edits)-1).Draw(t, "edit")]()
}

// Encode and ParseScenario apply the same rules: whatever Encode writes,
// from a valid or a damaged scenario, reads back as the scenario and
// encodes to the same bytes; a refusal is a *ScenarioError, or an encoding
// failure for text that is not UTF-8.
func TestScenarioEncodeAgreesWithParseScenario(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		s := scenarioGen().Draw(t, "scenario")
		for range rapid.IntRange(0, 2).Draw(t, "edits") {
			damage(t, &s)
		}
		enc, err := s.Encode()
		if err != nil {
			if !errors.As(err, new(*ScenarioError)) && !strings.HasPrefix(err.Error(), "encoding scenario ") {
				t.Fatalf("Encode error = %v; want a *ScenarioError or an encoding failure", err)
			}
			return
		}
		back, err := ParseScenario(enc)
		if err != nil {
			t.Fatalf("ParseScenario(Encode(s)): %v\n%s", err, enc)
		}
		if diff := cmp.Diff(s, back, equateEmpty); diff != "" {
			t.Fatalf("ParseScenario(Encode(s)) (-s +parsed):\n%s\n%s", diff, enc)
		}
		if again, err := back.Encode(); err != nil || !bytes.Equal(again, enc) {
			t.Fatalf("Encode is not stable (%v):\n%s\nthen\n%s", err, enc, again)
		}
	})
}

func TestListScenariosIsEmptyWithoutScenarios(t *testing.T) {
	t.Parallel()
	got, err := ListScenarios(fstest.MapFS{
		"kms/KMS-alias/01-duplicate.json": {Data: []byte("{}")},
		"scenario.json":                   {Data: []byte("{}")},
		"kms/scenario.json":               {Data: []byte("{}")},
	})
	if err != nil || len(got) != 0 {
		t.Errorf("ListScenarios = %v, %v; want none: a scenario sits at <svc>/<item>/scenario.json", got, err)
	}
}
