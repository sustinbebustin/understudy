package parity

import (
	"errors"
	"io/fs"
	"os"
	"path"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/google/go-cmp/cmp"
)

// recordings is the recorded corpus, testdata/parity at the repository root.
func recordings() fs.FS { return os.DirFS("../../testdata/parity") }

// readRecorded returns the bytes of the recorded fixture at p, a path under
// testdata/parity.
func readRecorded(tb testing.TB, p string) []byte {
	tb.Helper()
	b, err := fs.ReadFile(recordings(), p)
	if err != nil {
		tb.Fatalf("reading recorded fixture %s: %v", p, err)
	}
	return b
}

// recordedFixture parses the recorded fixture at p.
func recordedFixture(tb testing.TB, p string) Fixture {
	tb.Helper()
	f, err := ParseFixture(readRecorded(tb, p))
	if err != nil {
		tb.Fatalf("ParseFixture(%s): %v", p, err)
	}
	return f
}

// finalResponse is the response of a fixture's final exchange.
func finalResponse(tb testing.TB, f Fixture) Response {
	tb.Helper()
	if len(f.Exchanges) == 0 {
		tb.Fatalf("fixture %s has no exchanges", f.Step)
	}
	r := f.Exchanges[len(f.Exchanges)-1].Response
	if r == nil {
		tb.Fatalf("fixture %s: final exchange has no response", f.Step)
	}
	return *r
}

// Every recorded file parses, and its step member is its file name
// (testdata/README.md, "Parity fixture format").
func TestFixturesParse(t *testing.T) {
	t.Parallel()
	paths, err := fs.Glob(recordings(), "*/*/*.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) == 0 {
		t.Fatal("no fixtures under testdata/parity")
	}
	for _, p := range paths {
		if path.Base(p) == "scenario.json" {
			continue
		}
		f, err := ReadFixture(recordings(), p)
		if err != nil {
			t.Errorf("ReadFixture(%s): %v", p, err)
			continue
		}
		if want := strings.TrimSuffix(path.Base(p), ".json"); f.Step != want {
			t.Errorf("%s: step %q; want %q, the file name", p, f.Step, want)
		}
	}
}

func TestParseFixtureReadsARecordedCLIStep(t *testing.T) {
	t.Parallel()
	got := recordedFixture(t, "kms/KMS-alias/01-duplicate.json")
	const message = "An alias with the name arn:aws:kms:us-east-2:123456789012:alias/usr-rec-20261014-dup already exists"
	want := Fixture{
		Step:   "01-duplicate",
		Client: AWSCLI,
		Exit:   new(254),
		Error:  "aws: [ERROR]: An error occurred (AlreadyExistsException) when calling the CreateAlias operation: " + message,
		Exchanges: []Exchange{{
			Request: &Request{
				Method: "POST",
				URL:    "https://kms.us-east-2.amazonaws.com/",
				Headers: []Header{
					{"X-Amz-Target", "TrentService.CreateAlias"},
					{"Content-Type", "application/x-amz-json-1.1"},
					{"User-Agent", "aws-cli/2.37.13 md/awscrt#0.37.0 ua/2.1 os/linux#6.8.0-142-generic md/arch#x86_64 lang/python#3.15.0 md/pyimpl#CPython m/E,AN,Z,g,b cfg/retry-mode#standard md/installer#source sid/000000000001 md/distrib#ubuntu.24 md/prompt#off md/command#kms.create-alias"},
					{"X-Amz-Date", "20261014T064028Z"},
					{"amz-sdk-invocation-id", "ca4d4a87-4df5-4e8d-bca5-25b1a377c7bf"},
					{"amz-sdk-request", "attempt=1"},
					{"Content-Length", "98"},
				},
				Body: TextBody{Text: `{"AliasName": "alias/usr-rec-20261014-dup", "TargetKeyId": "157cb509-e842-4095-b0b6-e97188e0a507"}`},
			},
			Response: &Response{
				Status: 400,
				Headers: []Header{
					{"x-amzn-RequestId", "09e237f3-57a4-43a2-8599-16817aa6c9d2"},
					{"Cache-Control", "no-cache, no-store, must-revalidate, private"},
					{"Expires", "0"},
					{"Pragma", "no-cache"},
					{"Date", "Wed, 14 Oct 2026 06:40:28 GMT"},
					{"Content-Type", "application/x-amz-json-1.1"},
					{"Content-Length", "147"},
					{"connection", "keep-alive"},
				},
				Body: TextBody{Text: `{"__type":"AlreadyExistsException","message":"` + message + `"}`},
			},
		}},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("ParseFixture(kms/KMS-alias/01-duplicate.json) (-want +got):\n%s", diff)
	}
}

// A curl step has no exit status, and its request body was not kept
// (testdata/parity/kms/KMS-alias/02-duplicate-raw.json).
func TestParseFixtureReadsARawStepWithAnUnrecordedBody(t *testing.T) {
	t.Parallel()
	f := recordedFixture(t, "kms/KMS-alias/02-duplicate-raw.json")
	if f.Client != Curl || f.Exit != nil || len(f.Exchanges) != 1 {
		t.Fatalf("got client %q, exit %v, %d exchanges; want curl, none, 1", f.Client, f.Exit, len(f.Exchanges))
	}
	x := f.Exchanges[0]
	if diff := cmp.Diff(UnrecordedBody{Bytes: new(int64(95))}, x.Request.Body); diff != "" {
		t.Errorf("request body (-want +got):\n%s", diff)
	}
	if x.Response.Status != 400 || x.Response.Reason != "Bad Request" {
		t.Errorf("response status %d %q; want 400 \"Bad Request\"", x.Response.Status, x.Response.Reason)
	}
}

// A client that refused the call before sending it recorded no exchange
// (testdata/parity/dynamodb/R04/03-ttl-empty-name.json).
func TestParseFixtureReadsAStepWithNoExchange(t *testing.T) {
	t.Parallel()
	f := recordedFixture(t, "dynamodb/R04/03-ttl-empty-name.json")
	if len(f.Exchanges) != 0 || f.Exit == nil || *f.Exit != 252 {
		t.Errorf("got %d exchanges, exit %v; want none, 252", len(f.Exchanges), f.Exit)
	}
}

// A body over 64 KiB is its length and digest
// (testdata/parity/lambda/R13/04-event-over-1mb.json).
func TestParseFixtureReadsAnOmittedBody(t *testing.T) {
	t.Parallel()
	f := recordedFixture(t, "lambda/R13/04-event-over-1mb.json")
	want := OmittedBody{Bytes: 1048688, SHA256: "979b6597254002620fc600b1849b6aa059ad47616008dba3875371fd12cd337c"}
	if diff := cmp.Diff(want, f.Exchanges[0].Request.Body); diff != "" {
		t.Errorf("request body (-want +got):\n%s", diff)
	}
}

// The forms no recording happens to hold yet, written by the format's rules
// (testdata/README.md, "Parity fixture format"): bytes that are not UTF-8,
// an unknown length, curl's 100 Continue, no request, no response, no
// status, no body.
func TestParseFixtureReadsEveryBodyForm(t *testing.T) {
	t.Parallel()
	got, err := ParseFixture([]byte(`{
  "step": "forms",
  "client": "boto3",
  "exit": 0,
  "note": "n",
  "stdout": "o",
  "exchanges": [
    {
      "request": {"method": "PUT", "url": "/k", "headers": [["Expect", "100-continue"]], "body_base64": "AAH/"},
      "interim_responses": [{"status": 100, "reason": "Continue", "headers": []}],
      "response": {"headers": [["ETag", "\"e\""]], "body_unrecorded": {"bytes": null}}
    },
    {"response": {"status": 204, "headers": []}},
    {"request": {"method": "GET", "url": "https://h/", "headers": []}}
  ]
}`))
	if err != nil {
		t.Fatalf("ParseFixture: %v", err)
	}
	want := Fixture{
		Step: "forms", Client: Boto3, Exit: new(0), Note: "n", Stdout: "o",
		Exchanges: []Exchange{
			{
				Request:  &Request{Method: "PUT", URL: "/k", Headers: []Header{{"Expect", "100-continue"}}, Body: BinaryBody{Bytes: []byte{0, 1, 0xff}}},
				Interim:  []Response{{Status: 100, Reason: "Continue", Headers: []Header{}, Body: NoBody{}}},
				Response: &Response{Headers: []Header{{"ETag", `"e"`}}, Body: UnrecordedBody{}},
			},
			{Response: &Response{Status: 204, Headers: []Header{}, Body: NoBody{}}},
			{Request: &Request{Method: "GET", URL: "https://h/", Headers: []Header{}, Body: NoBody{}}},
		},
	}
	if diff := cmp.Diff(want, got, equateEmpty); diff != "" {
		t.Errorf("ParseFixture (-want +got):\n%s", diff)
	}
}

func TestParseFixtureIsStrict(t *testing.T) {
	t.Parallel()
	const ok = `"step": "s", "client": "aws-cli", "exit": 0`
	cases := map[string]string{
		"unknown member":         `{` + ok + `, "exchanges": [], "extra": 1}`,
		"unknown request member": `{` + ok + `, "exchanges": [{"request": {"method": "GET", "url": "https://h/", "headers": [], "verb": "x"}}]}`,
		"two body keys":          `{` + ok + `, "exchanges": [{"response": {"status": 200, "headers": [], "body": "a", "body_base64": "YQ=="}}]}`,
		"missing step":           `{"client": "aws-cli", "exchanges": []}`,
		"missing client":         `{"step": "s", "exchanges": []}`,
		"missing exchanges":      `{` + ok + `}`,
		"not json":               `{`,
	}
	for name, doc := range cases {
		_, err := ParseFixture([]byte(doc))
		var fe *FixtureError
		if !errors.As(err, &fe) {
			t.Errorf("%s: ParseFixture error = %v; want a *FixtureError", name, err)
			continue
		}
		if fe.Path != "" {
			t.Errorf("%s: FixtureError.Path = %q; want empty from ParseFixture", name, fe.Path)
		}
	}
	// The members the cases break, unbroken.
	valid := `{` + ok + `, "exchanges": [{"request": {"method": "GET", "url": "https://h/", "headers": []}, "response": {"status": 200, "headers": [], "body": "a"}}]}`
	if _, err := ParseFixture([]byte(valid)); err != nil {
		t.Errorf("ParseFixture(%s): %v; want nil", valid, err)
	}
}

func TestReadFixture(t *testing.T) {
	t.Parallel()
	fsys := fstest.MapFS{"kms/X/bad.json": {Data: []byte(`{"step": "bad"}`)}}
	if _, err := ReadFixture(fsys, "kms/X/absent.json"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("ReadFixture(absent) error = %v; want one wrapping fs.ErrNotExist", err)
	}
	_, err := ReadFixture(fsys, "kms/X/bad.json")
	var fe *FixtureError
	if !errors.As(err, &fe) || fe.Path != "kms/X/bad.json" {
		t.Errorf("ReadFixture(bad) error = %v; want a *FixtureError naming kms/X/bad.json", err)
	}
}
