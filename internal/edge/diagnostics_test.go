package edge_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/logsapi"
	"github.com/sustinbebustin/understudy/internal/edge"
)

// logLine is the part of a JSON log line the test reads.
type logLine struct {
	Level       string `json:"level"`
	Msg         string `json:"msg"`
	Operation   string `json:"operation"`
	SigningName string `json:"signing_name"`
	Status      int    `json:"status"`
	ModelSource string `json:"model_source"`
}

// logLines decodes the JSON log lines in buf, failing the test when one
// carries the request's signature.
func logLines(t *testing.T, buf *bytes.Buffer) []logLine {
	t.Helper()
	var lines []logLine
	s := bufio.NewScanner(buf)
	for s.Scan() {
		if bytes.Contains(s.Bytes(), []byte("Signature=")) || bytes.Contains(s.Bytes(), []byte("Credential=")) {
			t.Errorf("log line %s carries the request's credentials", s.Bytes())
		}
		var line logLine
		if err := json.Unmarshal(s.Bytes(), &line); err != nil {
			t.Fatalf("log line %q: %v", s.Text(), err)
		}
		lines = append(lines, line)
	}
	return lines
}

// unknownOperationWarning is the warning about an operation the vendored
// model does not define, whose wire answer carries no message.
const unknownOperationWarning = "a client called an operation that is not in understudy's vendored Smithy model;" +
	" the client may be newer than the model, or the name misspelled. Run understudy coverage to list the operations understudy serves"

// pinnedModelSource is the model source of the service dir, read from the
// pinned upstream record: "api-models-aws@<short commit> <model file>".
func pinnedModelSource(t *testing.T, dir string) string {
	t.Helper()
	data, err := os.ReadFile("../../third_party/api-models-aws/UPSTREAM.json")
	if err != nil {
		t.Fatal(err)
	}
	var upstream struct {
		Commit string `json:"commit"`
		Files  []struct {
			Dir  string `json:"dir"`
			Path string `json:"path"`
		} `json:"files"`
	}
	if err := json.Unmarshal(data, &upstream); err != nil {
		t.Fatal(err)
	}
	for _, f := range upstream.Files {
		if f.Dir == dir {
			return "api-models-aws@" + upstream.Commit[:7] + " " + path.Base(f.Path)
		}
	}
	t.Fatalf("UPSTREAM.json records no model for %s", dir)
	return ""
}

// The unknown-operation warning names the model commit and file it checked,
// so a developer sees that the client is newer than the pinned model.
func TestUnknownOperationWarningNamesTheModelSource(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	f := newFrontWith(t, nil, func(o *edge.Options) { o.Log = log })
	f.handler.ServeHTTP(httptest.NewRecorder(),
		request(t, http.MethodPost, "/", jsonHeader("AmazonSQS.Bogus", "application/x-amz-json-1.0"), `{}`, "sqs"))
	want := pinnedModelSource(t, "sqs")
	for _, line := range logLines(t, &buf) {
		if line.Msg == unknownOperationWarning {
			if line.ModelSource != want {
				t.Errorf("warning model_source = %q, want %q", line.ModelSource, want)
			}
			return
		}
	}
	t.Errorf("no unknown-operation warning in the log: %s", buf.String())
}

// The developer watching the server sees each request at Debug, and a
// warning, once per operation or service rather than per retry, when a
// client gets 501 for an unimplemented operation or a service understudy
// does not emulate, or calls an operation the model does not define.
func TestTheServerLogsRequestsAndWarnsOnceAboutWhatItDoesNotServe(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	f := newFrontWith(t, nil, func(o *edge.Options) { o.Log = log }, logsapi.Service())
	for range 2 {
		for _, r := range []*http.Request{
			request(t, http.MethodPost, "/", jsonHeader("Logs_20140328.DescribeLogGroups", "application/x-amz-json-1.1"), `{}`, "logs"),
			// A service no planned item adds, so the example stays not emulated.
			request(t, http.MethodPost, "/", jsonHeader("Firehose_20150804.ListDeliveryStreams", "application/x-amz-json-1.1"), `{}`, "firehose"),
			request(t, http.MethodPost, "/", jsonHeader("AmazonSQS.Bogus", "application/x-amz-json-1.0"), `{}`, "sqs"),
		} {
			f.handler.ServeHTTP(httptest.NewRecorder(), r)
		}
	}
	type key struct{ level, msg, subject string }
	counts := map[key]int{}
	for _, line := range logLines(t, &buf) {
		subject := line.Operation
		if line.SigningName != "" {
			subject = line.SigningName
		}
		counts[key{line.Level, line.Msg, subject}]++
		want := http.StatusNotImplemented
		if line.Operation == `sqs unknown operation "Bogus"` {
			want = http.StatusBadRequest
		}
		if line.Msg == "request" && line.Status != want {
			t.Errorf("request line %+v, want status %d", line, want)
		}
	}
	for k, want := range map[key]int{
		{"DEBUG", "request", "logs.DescribeLogGroups"}:                                                   2,
		{"DEBUG", "request", "firehose (not emulated)"}:                                                  2,
		{"WARN", "a client called an operation understudy does not implement", "logs.DescribeLogGroups"}: 1,
		{"WARN", "a client called a service understudy does not emulate", "firehose"}:                    1,
		{"DEBUG", "request", `sqs unknown operation "Bogus"`}:                                            2,
		{"WARN", unknownOperationWarning, "sqs.Bogus"}:                                                   1,
	} {
		if counts[k] != want {
			t.Errorf("%v logged %d times, want %d; all: %v", k, counts[k], want, counts)
		}
	}
}
