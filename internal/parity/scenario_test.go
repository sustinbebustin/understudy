package parity

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"slices"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/google/go-cmp/cmp"
	"pgregory.net/rapid"
)

// fullScenario uses every member of the format (testdata/README.md,
// scenarios) with references that resolve.
const fullScenario = `{
  "version": 1,
  "item": "KMS-alias",
  "account": "123456789012",
  "region": "us-east-2",
  "client": "aws-cli",
  "needs": ["docker"],
  "setup": [
    {
      "service": "kms",
      "operation": "CreateKey",
      "input": {"Description": "usr-rec-20261014-alias", "Tags": [{"TagKey": "k", "TagValue": "v"}]},
      "bind": [{"path": "body/KeyMetadata/KeyId", "shape": {"kind": "uuid"}, "aws": "157cb509-e842-4095-b0b6-e97188e0a507"}],
      "before": "01-duplicate"
    },
    {"service": "eventbridge", "operation": "PutRule", "input": {}}
  ],
  "steps": [
    {
      "fixture": "kms/KMS-alias/01-duplicate.json",
      "compare": "all",
      "bind": [
        {"path": "body/Handle", "shape": {"kind": "id", "alphabet": "base64", "min": 36, "max": 1024, "prefix": "AQEB"}},
        {"path": "body/Arn", "shape": {"kind": "arn", "service": "kms", "resource": "key", "name": {"kind": "uuid"}}},
        {"path": "body/Role", "shape": {"kind": "arn", "service": "iam", "resource": "role"}},
        {"path": "body/QueueUrl", "shape": {"kind": "url", "path": "/{account}/{name}"}}
      ],
      "advance": "6s"
    },
    {"fixture": "sqs/R01/02-recv1.json", "compare": "final", "advance": "1m30s", "signing": "streams.dynamodb"}
  ],
  "waivers": [{"step": "01-duplicate", "field": "header/content-type", "reason": "a reason"}],
  "settles": [
    {"switch": "events.pattern.empty-input", "step": "02-recv1", "field": "body/Result"},
    {"overlay": "com.amazonaws.kms#AlreadyExistsException", "covers": ["dev.understudy#statusOverride"], "step": "01-duplicate", "field": "status"}
  ],
  "record": [
    {"argv": ["aws", "kms", "create-key", "--description", "$P-alias", "--query", "KeyMetadata.KeyId", "--output", "text"], "capture": "K"},
    {"step": "01-duplicate", "argv": ["aws", "kms", "create-alias", "--alias-name", "alias/$P-dup", "--target-key-id", "$K"]}
  ]
}
`

func fullScenarioValue() Scenario {
	return Scenario{
		Version: 1,
		Item:    "KMS-alias",
		Account: "123456789012",
		Region:  "us-east-2",
		Client:  AWSCLI,
		Needs:   []Need{NeedDocker},
		Setup: []Call{
			{
				Service:   "kms",
				Operation: "CreateKey",
				Input:     `{"Description":"usr-rec-20261014-alias","Tags":[{"TagKey":"k","TagValue":"v"}]}`,
				Bind:      []SetupBind{{Path: "body/KeyMetadata/KeyId", Shape: UUIDShape{}, AWS: "157cb509-e842-4095-b0b6-e97188e0a507"}},
				Before:    "01-duplicate",
			},
			{Service: "eventbridge", Operation: "PutRule", Input: `{}`},
		},
		Steps: []Step{
			{
				Fixture: "kms/KMS-alias/01-duplicate.json",
				Compare: CompareAll,
				Bind: []Bind{
					{Path: "body/Handle", Shape: IDShape{Alphabet: Base64, Min: 36, Max: 1024, Prefix: "AQEB"}},
					{Path: "body/Arn", Shape: ARNShape{Service: "kms", Resource: "key", Name: UUIDShape{}}},
					{Path: "body/Role", Shape: ARNShape{Service: "iam", Resource: "role"}},
					{Path: "body/QueueUrl", Shape: URLShape{Path: "/{account}/{name}"}},
				},
				Advance: 6 * time.Second,
			},
			{Fixture: "sqs/R01/02-recv1.json", Compare: CompareFinal, Advance: 90 * time.Second, Signing: "streams.dynamodb"},
		},
		Waivers: []Waiver{{Step: "01-duplicate", Field: "header/content-type", Reason: "a reason"}},
		Settles: []Claim{
			{Target: SwitchTarget{Name: "events.pattern.empty-input"}, Step: "02-recv1", Field: "body/Result"},
			{Target: OverlayTarget{Shape: "com.amazonaws.kms#AlreadyExistsException", Covers: []string{"dev.understudy#statusOverride"}}, Step: "01-duplicate", Field: "status"},
		},
		Record: []Command{
			RunCommand{Argv: []string{"aws", "kms", "create-key", "--description", "$P-alias", "--query", "KeyMetadata.KeyId", "--output", "text"}, Capture: "K"},
			RecCommand{Step: "01-duplicate", Argv: []string{"aws", "kms", "create-alias", "--alias-name", "alias/$P-dup", "--target-key-id", "$K"}},
		},
	}
}

func TestParseScenarioReadsEveryMember(t *testing.T) {
	t.Parallel()
	got, err := ParseScenario([]byte(fullScenario))
	if err != nil {
		t.Fatalf("ParseScenario: %v", err)
	}
	if diff := cmp.Diff(fullScenarioValue(), got, equateEmpty); diff != "" {
		t.Errorf("ParseScenario (-want +got):\n%s", diff)
	}
}

func TestStepNameIsTheFixtureBaseName(t *testing.T) {
	t.Parallel()
	if got := (Step{Fixture: "kms/KMS-alias/01-duplicate.json"}).Name(); got != "01-duplicate" {
		t.Errorf("Name() = %q; want 01-duplicate", got)
	}
}

// topLevelKeys lists the members of a JSON object in document order.
func topLevelKeys(tb testing.TB, doc []byte) []string {
	tb.Helper()
	dec := json.NewDecoder(bytes.NewReader(doc))
	if _, err := dec.Token(); err != nil {
		tb.Fatal(err)
	}
	var keys []string
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			tb.Fatal(err)
		}
		keys = append(keys, fmt.Sprint(tok))
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			tb.Fatal(err)
		}
	}
	return keys
}

// The canonical form: members in the format's order, two-space indentation,
// a trailing newline, empty optional members left out, the setup input as
// nested JSON and the advance as a Go duration.
func TestScenarioEncodeIsCanonical(t *testing.T) {
	t.Parallel()
	full, err := fullScenarioValue().Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if want := []string{"version", "item", "account", "region", "client", "needs", "setup", "steps", "waivers", "settles", "record"}; !slices.Equal(topLevelKeys(t, full), want) {
		t.Errorf("Encode members = %v; want %v", topLevelKeys(t, full), want)
	}
	if !bytes.HasPrefix(full, []byte("{\n  \"version\": 1,\n  \"item\": \"KMS-alias\",\n")) || !bytes.HasSuffix(full, []byte("}\n")) {
		t.Errorf("Encode =\n%s\nwant two-space indentation and a trailing newline", full)
	}
	var doc struct {
		Setup []struct {
			Input json.RawMessage `json:"input"`
		} `json:"setup"`
		Steps []struct {
			Advance string `json:"advance"`
		} `json:"steps"`
	}
	if err := json.Unmarshal(full, &doc); err != nil {
		t.Fatalf("Encode is not JSON: %v\n%s", err, full)
	}
	if len(doc.Setup) != 2 || !bytes.HasPrefix(doc.Setup[0].Input, []byte("{")) {
		t.Errorf("Encode setup = %+v; want each input written as a JSON object", doc.Setup)
	}
	if len(doc.Steps) != 2 || doc.Steps[0].Advance != "6s" || doc.Steps[1].Advance != "1m30s" {
		t.Errorf("Encode steps = %+v; want advances 6s and 1m30s (time.Duration.String)", doc.Steps)
	}

	minimal := Scenario{
		Version: 1, Item: "R15", Account: "123456789012", Region: "us-east-2", Client: AWSCLI,
		Steps: []Step{{Fixture: "sts/R15/06-assume-ok.json", Compare: CompareAll}},
	}
	got, err := minimal.Encode()
	if err != nil {
		t.Fatalf("Encode(minimal): %v", err)
	}
	if want := []string{"version", "item", "account", "region", "client", "steps"}; !slices.Equal(topLevelKeys(t, got), want) {
		t.Errorf("Encode(minimal) members = %v; want %v", topLevelKeys(t, got), want)
	}
	if bytes.Contains(got, []byte(`"advance"`)) || bytes.Contains(got, []byte(`"bind"`)) {
		t.Errorf("Encode(minimal) =\n%s\nwant no advance or bind on a step without them", got)
	}
}

// A file in another member order or layout reads the same and encodes to
// the canonical form.
func TestScenarioEncodeIgnoresTheSourceLayout(t *testing.T) {
	t.Parallel()
	shuffled := `{"steps":[{"compare":"all","fixture":"sts/R15/06-assume-ok.json"}],"client":"aws-cli","region":"us-east-2","account":"123456789012","item":"R15","version":1}`
	a, err := ParseScenario([]byte(shuffled))
	if err != nil {
		t.Fatalf("ParseScenario(shuffled): %v", err)
	}
	want := Scenario{
		Version: 1, Item: "R15", Account: "123456789012", Region: "us-east-2", Client: AWSCLI,
		Steps: []Step{{Fixture: "sts/R15/06-assume-ok.json", Compare: CompareAll}},
	}
	if diff := cmp.Diff(want, a, equateEmpty); diff != "" {
		t.Errorf("ParseScenario(shuffled) (-want +got):\n%s", diff)
	}
	enc, err := a.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	b, err := ParseScenario(enc)
	if err != nil {
		t.Fatalf("ParseScenario(Encode): %v\n%s", err, enc)
	}
	again, err := b.Encode()
	if err != nil || !bytes.Equal(enc, again) {
		t.Errorf("Encode is not stable:\n%s\nthen\n%s (%v)", enc, again, err)
	}
}

// scenarioDocument is a scenario file as generic members, for the
// validation cases to break one member at a time.
type scenarioDocument struct {
	top   map[string]any
	steps []map[string]any
}

// scenarioDoc is a valid scenario.
func scenarioDoc() *scenarioDocument {
	return &scenarioDocument{
		top: map[string]any{
			"version": 1,
			"item":    "KMS-alias",
			"account": "123456789012",
			"region":  "us-east-2",
			"client":  "aws-cli",
		},
		steps: []map[string]any{
			{"fixture": "kms/KMS-alias/01-duplicate.json", "compare": "all"},
			{"fixture": "kms/KMS-alias/02-duplicate-raw.json", "compare": "final"},
		},
	}
}

func (d *scenarioDocument) marshal(tb testing.TB) []byte {
	tb.Helper()
	top := maps.Clone(d.top)
	top["steps"] = d.steps
	data, err := json.Marshal(top)
	if err != nil {
		tb.Fatal(err)
	}
	return data
}

func TestParseScenarioValidates(t *testing.T) {
	t.Parallel()
	uuid := map[string]any{"kind": "uuid"}
	cases := []struct {
		name  string
		edit  func(*scenarioDocument)
		field string // the start of ScenarioError.Field
	}{
		{"version 2", func(d *scenarioDocument) { d.top["version"] = 2 }, "version"},
		{"no version", func(d *scenarioDocument) { delete(d.top, "version") }, "version"},
		{"empty item", func(d *scenarioDocument) { d.top["item"] = "" }, "item"},
		{"short account", func(d *scenarioDocument) { d.top["account"] = "12345678901" }, "account"},
		{"account with a letter", func(d *scenarioDocument) { d.top["account"] = "12345678901x" }, "account"},
		{"region", func(d *scenarioDocument) { d.top["region"] = "useast2" }, "region"},
		{"region upper case", func(d *scenarioDocument) { d.top["region"] = "US-EAST-2" }, "region"},
		{"client", func(d *scenarioDocument) { d.top["client"] = "wget" }, "client"},
		{"need", func(d *scenarioDocument) { d.top["needs"] = []any{"gpu"} }, "needs[0]"},
		{"no steps", func(d *scenarioDocument) { d.steps = []map[string]any{} }, "steps"},
		{"fixture outside the pattern", func(d *scenarioDocument) { d.steps[0]["fixture"] = "KMS/KMS-alias/01-duplicate.json" }, "steps[0].fixture"},
		{"fixture without a directory", func(d *scenarioDocument) { d.steps[0]["fixture"] = "01-duplicate.json" }, "steps[0].fixture"},
		{"fixture is the scenario", func(d *scenarioDocument) { d.steps[0]["fixture"] = "kms/KMS-alias/scenario.json" }, "steps[0].fixture"},
		{"duplicate step names", func(d *scenarioDocument) { d.steps[1]["fixture"] = "sqs/Other/01-duplicate.json" }, "steps[1]"},
		{"compare mode", func(d *scenarioDocument) { d.steps[0]["compare"] = "some" }, "steps[0].compare"},
		{"no compare mode", func(d *scenarioDocument) { delete(d.steps[0], "compare") }, "steps[0].compare"},
		{"advance not a duration", func(d *scenarioDocument) { d.steps[0]["advance"] = "6" }, "steps[0].advance"},
		{"advance zero", func(d *scenarioDocument) { d.steps[0]["advance"] = "0s" }, "steps[0].advance"},
		{"advance negative", func(d *scenarioDocument) { d.steps[0]["advance"] = "-1s" }, "steps[0].advance"},
		{"signing not a scope name", func(d *scenarioDocument) { d.steps[0]["signing"] = "Streams/DynamoDB" }, "steps[0].signing"},
		{"bind path", func(d *scenarioDocument) {
			d.steps[0]["bind"] = []any{map[string]any{"path": "Body/x", "shape": uuid}}
		}, "steps[0].bind[0].path"},
		{"bind on a waiver-only path", func(d *scenarioDocument) {
			d.steps[0]["bind"] = []any{map[string]any{"path": "header", "shape": uuid}}
		}, "steps[0].bind[0].path"},
		{"shape kind", func(d *scenarioDocument) {
			d.steps[0]["bind"] = []any{map[string]any{"path": "body/x", "shape": map[string]any{"kind": "guid"}}}
		}, "steps[0].bind[0].shape"},
		{"id shape min above max", func(d *scenarioDocument) {
			d.steps[0]["bind"] = []any{map[string]any{"path": "body/x", "shape": map[string]any{"kind": "id", "alphabet": "hex", "min": 9, "max": 8}}}
		}, "steps[0].bind[0].shape"},
		{"id shape min zero", func(d *scenarioDocument) {
			d.steps[0]["bind"] = []any{map[string]any{"path": "body/x", "shape": map[string]any{"kind": "id", "alphabet": "hex", "min": 0, "max": 8}}}
		}, "steps[0].bind[0].shape"},
		{"id shape alphabet", func(d *scenarioDocument) {
			d.steps[0]["bind"] = []any{map[string]any{"path": "body/x", "shape": map[string]any{"kind": "id", "alphabet": "emoji", "min": 1, "max": 8}}}
		}, "steps[0].bind[0].shape"},
		{"setup bind without an aws value", func(d *scenarioDocument) {
			d.top["setup"] = []any{map[string]any{"service": "kms", "operation": "CreateKey", "input": map[string]any{}, "bind": []any{map[string]any{"path": "body/KeyMetadata/KeyId", "shape": uuid}}}}
		}, "setup[0].bind[0]"},
		{"setup input not an object", func(d *scenarioDocument) {
			d.top["setup"] = []any{map[string]any{"service": "kms", "operation": "CreateKey", "input": "{}"}}
		}, "setup[0].input"},
		{"setup before an unknown step", func(d *scenarioDocument) {
			d.top["setup"] = []any{map[string]any{"service": "kms", "operation": "CreateKey", "input": map[string]any{}, "before": "09-none"}}
		}, "setup[0].before"},
		{"waiver without a reason", func(d *scenarioDocument) {
			d.top["waivers"] = []any{map[string]any{"step": "01-duplicate", "field": "status", "reason": ""}}
		}, "waivers[0].reason"},
		{"waiver field", func(d *scenarioDocument) {
			d.top["waivers"] = []any{map[string]any{"step": "01-duplicate", "field": "statuses", "reason": "r"}}
		}, "waivers[0].field"},
		{"waiver of an unknown step", func(d *scenarioDocument) {
			d.top["waivers"] = []any{map[string]any{"step": "09-none", "field": "status", "reason": "r"}}
		}, "waivers[0].step"},
		{"settles both a switch and an overlay", func(d *scenarioDocument) {
			d.top["settles"] = []any{map[string]any{"switch": "a.b", "overlay": "com.amazonaws.kms#X", "step": "01-duplicate", "field": "status"}}
		}, "settles[0]"},
		{"settles neither", func(d *scenarioDocument) {
			d.top["settles"] = []any{map[string]any{"step": "01-duplicate", "field": "status"}}
		}, "settles[0]"},
		{"overlay shape without #", func(d *scenarioDocument) {
			d.top["settles"] = []any{map[string]any{"overlay": "AlreadyExistsException", "step": "01-duplicate", "field": "status"}}
		}, "settles[0].overlay"},
		{"settles a field that is not a leaf", func(d *scenarioDocument) {
			d.top["settles"] = []any{map[string]any{"switch": "a.b", "step": "01-duplicate", "field": "error"}}
		}, "settles[0].field"},
		{"settles an unknown step", func(d *scenarioDocument) {
			d.top["settles"] = []any{map[string]any{"switch": "a.b", "step": "09-none", "field": "status"}}
		}, "settles[0].step"},
		{"record argv not aws", func(d *scenarioDocument) {
			d.top["record"] = []any{map[string]any{"step": "01-duplicate", "argv": []any{"curl", "x"}}}
		}, "record[0].argv"},
		{"record capture name", func(d *scenarioDocument) {
			d.top["record"] = []any{map[string]any{"argv": []any{"aws", "sts", "get-caller-identity"}, "capture": "lower"}}
		}, "record[0].capture"},
		{"unknown member", func(d *scenarioDocument) { d.top["extra"] = true }, ""},
		{"unknown step member", func(d *scenarioDocument) { d.steps[0]["fixtures"] = "x" }, "steps[0]"},
	}
	for _, c := range cases {
		doc := scenarioDoc()
		c.edit(doc)
		_, err := ParseScenario(doc.marshal(t))
		var se *ScenarioError
		if !errors.As(err, &se) {
			t.Errorf("%s: ParseScenario error = %v; want a *ScenarioError", c.name, err)
			continue
		}
		if !strings.HasPrefix(se.Field, c.field) || se.Reason == "" || se.Scenario != "" {
			t.Errorf("%s: ScenarioError{Scenario: %q, Field: %q, Reason: %q}; want no scenario id, a field starting %q and a reason", c.name, se.Scenario, se.Field, se.Reason, c.field)
		}
	}
	// The unedited document is valid, so each case fails for its edit alone.
	if _, err := ParseScenario(scenarioDoc().marshal(t)); err != nil {
		t.Errorf("ParseScenario(valid document): %v", err)
	}
}

func TestReadScenarioChecksTheItemAgainstItsDirectory(t *testing.T) {
	t.Parallel()
	valid := scenarioDoc().marshal(t)
	fsys := fstest.MapFS{
		"kms/KMS-alias/scenario.json": {Data: valid},
		"kms/Other/scenario.json":     {Data: valid},
	}
	if _, err := ReadScenario(fsys, "kms/KMS-alias"); err != nil {
		t.Errorf("ReadScenario(kms/KMS-alias): %v", err)
	}
	_, err := ReadScenario(fsys, "kms/Other")
	var se *ScenarioError
	if !errors.As(err, &se) || se.Scenario != "kms/Other" || se.Field != "item" {
		t.Errorf("ReadScenario(kms/Other) error = %v; want a *ScenarioError for kms/Other at item", err)
	}
	if _, err := ReadScenario(fsys, "kms/Absent"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("ReadScenario(kms/Absent) error = %v; want one wrapping fs.ErrNotExist", err)
	}
}

func TestListScenarios(t *testing.T) {
	t.Parallel()
	fsys := fstest.MapFS{
		"sts/R15/scenario.json":           {Data: []byte("{}")},
		"kms/KMS-alias/scenario.json":     {Data: []byte("{}")},
		"kms/KMS-alias/01-duplicate.json": {Data: []byte("{}")},
		"sqs/R01/02-send.json":            {Data: []byte("{}")},
		"events/R14/notes.md":             {Data: []byte("")},
	}
	got, err := ListScenarios(fsys)
	if err != nil {
		t.Fatalf("ListScenarios: %v", err)
	}
	if want := []string{"kms/KMS-alias", "sts/R15"}; !slices.Equal(got, want) {
		t.Errorf("ListScenarios = %v; want %v", got, want)
	}
}

// Generators for the round trip. Each draws only values the format accepts,
// so every drawn scenario must parse back.

var leafPaths = []Path{
	"status", "error/code", "error/message", "header/content-type", "header/x-amzn-query-error",
	"body", "body/KeyId", "body/Messages/0/ReceiptHandle", "body/a~1b",
	"body/ErrorResponse/Error/Code", "body/ListResponse/member[1]", "body/AssumeRoleResponse/@xmlns",
}

var waiverPaths = append(slices.Clone(leafPaths), "header", "error", "exchange/0/body", "exchange/1/status")

func shapeGen(depth int) *rapid.Generator[Shape] {
	return rapid.Custom(func(t *rapid.T) Shape {
		kinds := 2
		if depth > 0 {
			kinds = 4
		}
		switch rapid.IntRange(0, kinds-1).Draw(t, "shape kind") {
		case 0:
			return UUIDShape{}
		case 1:
			prefix := rapid.StringMatching(`[A-Z]{0,4}`).Draw(t, "prefix")
			lo := len(prefix) + rapid.IntRange(1, 40).Draw(t, "min")
			return IDShape{
				Alphabet: rapid.SampledFrom([]Alphabet{Hex, Base32, Base64, Alnum, Digits}).Draw(t, "alphabet"),
				Min:      lo,
				Max:      lo + rapid.IntRange(0, 1000).Draw(t, "span"),
				Prefix:   prefix,
			}
		case 2:
			var name Shape
			if rapid.Bool().Draw(t, "named") {
				name = shapeGen(0).Draw(t, "name")
			}
			return ARNShape{
				Service:  rapid.StringMatching(`[a-z][a-z0-9-]{1,10}`).Draw(t, "service"),
				Resource: rapid.SampledFrom([]string{"", "key", "role", "function", "event-bus"}).Draw(t, "resource"),
				Name:     name,
			}
		default:
			return URLShape{Path: rapid.SampledFrom([]string{"/{account}/{name}", "/{name}", "/2015-03-31/functions/{name}"}).Draw(t, "url path")}
		}
	})
}

// compactObject draws a JSON object in compact form with its members in
// sorted order at every level, the form Input holds.
func compactObject(t *rapid.T, depth int) string {
	keys := rapid.SliceOfNDistinct(rapid.StringMatching(`[A-Z][A-Za-z]{0,7}`), 0, 4, rapid.ID[string]).Draw(t, "keys")
	slices.Sort(keys)
	members := make([]string, len(keys))
	for i, k := range keys {
		var v string
		switch n := rapid.IntRange(0, 4).Draw(t, "input kind"); {
		case n == 0:
			v = `"` + rapid.StringMatching(`[A-Za-z0-9 ._:/-]{0,12}`).Draw(t, "input string") + `"`
		case n == 1:
			v = strconv.Itoa(rapid.IntRange(-5, 5000).Draw(t, "input number"))
		case n == 2:
			v = strconv.FormatBool(rapid.Bool().Draw(t, "input bool"))
		case n == 3 && depth > 0:
			v = "[" + compactObject(t, depth-1) + "]"
		case depth > 0:
			v = compactObject(t, depth-1)
		default:
			v = "null"
		}
		members[i] = `"` + k + `":` + v
	}
	return "{" + strings.Join(members, ",") + "}"
}

func scenarioGen() *rapid.Generator[Scenario] {
	return rapid.Custom(func(t *rapid.T) Scenario {
		svc := rapid.SampledFrom([]string{"kms", "sqs", "sts", "events", "iam", "s3"}).Draw(t, "svc")
		item := rapid.StringMatching(`[A-Za-z0-9][A-Za-z0-9-]{0,8}`).Draw(t, "item")
		n := rapid.IntRange(1, 4).Draw(t, "steps")
		names := make([]string, n)
		steps := make([]Step, n)
		for i := range n {
			names[i] = fmt.Sprintf("%02d%s", i, rapid.StringMatching(`[A-Za-z0-9._-]{0,8}`).Draw(t, "step suffix"))
			s := Step{
				Fixture: svc + "/" + item + "/" + names[i] + ".json",
				Compare: rapid.SampledFrom([]CompareMode{CompareAll, CompareFinal}).Draw(t, "compare"),
			}
			for range rapid.IntRange(0, 2).Draw(t, "binds") {
				s.Bind = append(s.Bind, Bind{Path: rapid.SampledFrom(leafPaths).Draw(t, "bind path"), Shape: shapeGen(1).Draw(t, "bind shape")})
			}
			if rapid.Bool().Draw(t, "advances") {
				s.Advance = time.Duration(rapid.Int64Range(1, 3_600_000).Draw(t, "advance ms")) * time.Millisecond
			}
			if rapid.Bool().Draw(t, "signs") {
				s.Signing = rapid.SampledFrom([]string{"dynamodb", "streams.dynamodb", "execute-api"}).Draw(t, "signing")
			}
			steps[i] = s
		}
		stepName := rapid.SampledFrom(names)
		sc := Scenario{
			Version: 1,
			Item:    item,
			Account: rapid.StringMatching(`[0-9]{12}`).Draw(t, "account"),
			Region:  rapid.StringMatching(`[a-z]{2}(-[a-z]{1,9}){1,2}-[0-9]`).Draw(t, "region"),
			Client:  rapid.SampledFrom([]Client{AWSCLI, Boto3, Curl, HTTPClient, Socket}).Draw(t, "client"),
			Steps:   steps,
		}
		if rapid.Bool().Draw(t, "docker") {
			sc.Needs = []Need{NeedDocker}
		}
		for range rapid.IntRange(0, 2).Draw(t, "setup") {
			c := Call{
				Service:   rapid.SampledFrom([]string{"kms", "iam", "sqs", "eventbridge", "lambda"}).Draw(t, "setup service"),
				Operation: rapid.StringMatching(`[A-Z][A-Za-z]{2,14}`).Draw(t, "setup operation"),
				Input:     compactObject(t, 2),
			}
			for range rapid.IntRange(0, 2).Draw(t, "setup binds") {
				c.Bind = append(c.Bind, SetupBind{
					Path:  rapid.SampledFrom(leafPaths).Draw(t, "setup bind path"),
					Shape: shapeGen(1).Draw(t, "setup bind shape"),
					AWS:   rapid.StringMatching(`[A-Za-z0-9:/._-]{1,40}`).Draw(t, "setup bind aws"),
				})
			}
			if rapid.Bool().Draw(t, "before") {
				c.Before = stepName.Draw(t, "before step")
			}
			sc.Setup = append(sc.Setup, c)
		}
		for range rapid.IntRange(0, 2).Draw(t, "waivers") {
			sc.Waivers = append(sc.Waivers, Waiver{
				Step:   stepName.Draw(t, "waiver step"),
				Field:  rapid.SampledFrom(waiverPaths).Draw(t, "waiver field"),
				Reason: rapid.StringMatching(`[A-Za-z0-9 ,.;:()"<>&'-]{1,30}`).Draw(t, "waiver reason"),
			})
		}
		for range rapid.IntRange(0, 2).Draw(t, "settles") {
			var target Target = SwitchTarget{Name: rapid.StringMatching(`[a-z]{2,8}(\.[a-z0-9-]{1,10}){1,3}`).Draw(t, "switch")}
			if rapid.Bool().Draw(t, "overlay") {
				target = OverlayTarget{
					Shape:  rapid.StringMatching(`com\.amazonaws\.[a-z]{2,8}#[A-Z][A-Za-z]{1,20}`).Draw(t, "overlay shape"),
					Covers: rapid.SliceOfN(rapid.StringMatching(`dev\.understudy#[a-z][A-Za-z]{1,15}`), 0, 2).Draw(t, "covers"),
				}
			}
			sc.Settles = append(sc.Settles, Claim{Target: target, Step: stepName.Draw(t, "settles step"), Field: rapid.SampledFrom(leafPaths).Draw(t, "settles field")})
		}
		argv := func(label string) []string {
			return append([]string{"aws"}, rapid.SliceOfN(rapid.StringMatching(`[A-Za-z0-9$/{}._=-]{0,12}`), 0, 5).Draw(t, label)...)
		}
		for range rapid.IntRange(0, 2).Draw(t, "record") {
			if rapid.Bool().Draw(t, "rec") {
				sc.Record = append(sc.Record, RecCommand{Step: stepName.Draw(t, "rec step"), Argv: argv("rec argv")})
				continue
			}
			capture := ""
			if rapid.Bool().Draw(t, "captures") {
				capture = rapid.StringMatching(`[A-Z_][A-Z0-9_]{0,6}`).Draw(t, "capture")
			}
			sc.Record = append(sc.Record, RunCommand{Argv: argv("run argv"), Capture: capture})
		}
		return sc
	})
}

// Any scenario the format accepts reads back as written, and encodes to the
// same bytes again.
func TestScenarioRoundTrip(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(t *rapid.T) {
		sc := scenarioGen().Draw(t, "scenario")
		enc, err := sc.Encode()
		if err != nil {
			t.Fatalf("Encode: %v", err)
		}
		back, err := ParseScenario(enc)
		if err != nil {
			t.Fatalf("ParseScenario(Encode(s)): %v\n%s", err, enc)
		}
		if diff := cmp.Diff(sc, back, equateEmpty); diff != "" {
			t.Fatalf("ParseScenario(Encode(s)) (-s +parsed):\n%s\n%s", diff, enc)
		}
		again, err := back.Encode()
		if err != nil {
			t.Fatalf("Encode(parsed): %v", err)
		}
		if !bytes.Equal(enc, again) {
			t.Fatalf("Encode is not stable:\n%s\nthen\n%s", enc, again)
		}
	})
}
