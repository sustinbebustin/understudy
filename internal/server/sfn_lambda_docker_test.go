//go:build docker

package server_test

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/server"
)

// slowRuntime is a provided.al2023 runtime that logs "start <event>",
// sleeps ten seconds, logs "done <event>" and answers {}.
const slowRuntime = `#!/bin/sh
set -eu
API="http://${AWS_LAMBDA_RUNTIME_API}/2018-06-01/runtime"
while true; do
  HEADERS=$(mktemp)
  EVENT=$(curl -sS -D "$HEADERS" "$API/invocation/next")
  ID=$(grep -i '^Lambda-Runtime-Aws-Request-Id:' "$HEADERS" | tr -d '\r' | cut -d' ' -f2)
  echo "start $EVENT"
  sleep 10
  echo "done $EVENT"
  curl -sS -X POST "$API/invocation/$ID/response" -d '{}' >/dev/null
done
`

// A lambda:invoke Task with InvocationType Event hands the payload to
// Lambda's asynchronous invoke: the execution succeeds with StatusCode 202
// while the function still runs, and the function runs once to its end
// afterwards (SFN-13).
func TestSFNLambdaInvokeEventRunsAsync(t *testing.T) {
	r := launch(t, server.Config{Docker: dockerConfig(t)})
	r.stopOnCleanup(t)

	pkg := base64.StdEncoding.EncodeToString(executableZip(t, slowRuntime))
	body := `{"FunctionName":"slow","Runtime":"provided.al2023","Handler":"bootstrap","Timeout":60,` +
		`"Role":"arn:aws:iam::000000000000:role/fn","Code":{"ZipFile":"` + pkg + `"}}`
	req := newRequest(t, http.MethodPost, r.endpoint+"/2015-03-31/functions", body)
	req.Header.Set("Content-Type", "application/json")
	if w := signedCall(t, dummy, "lambda", req, body); w.status != http.StatusCreated {
		t.Fatalf("CreateFunction = %d %s", w.status, w.body)
	}
	def := `{"StartAt":"E","States":{"E":{"Type":"Task","Resource":"arn:aws:states:::lambda:invoke",` +
		`"Parameters":{"FunctionName":"slow","InvocationType":"Event","Payload":{"marker":"m1"}},"End":true}}}`
	if w := createMachine(t, dummy, r.endpoint, "async", def, ""); w.status != http.StatusOK {
		t.Fatalf("CreateStateMachine = %d %s", w.status, w.body)
	}
	started := sfnCall(t, dummy, r.endpoint, "StartExecution", `{"stateMachineArn":"arn:aws:states:us-east-1:000000000000:stateMachine:async","input":"{}"}`)
	var exec struct{ ExecutionArn string }
	if err := json.Unmarshal([]byte(started.body), &exec); started.status != http.StatusOK || err != nil {
		t.Fatalf("StartExecution = %d %s", started.status, started.body)
	}

	var d struct{ Status, Output string }
	waitFor(t, r, "SUCCEEDED execution", func() bool {
		w := sfnCall(t, dummy, r.endpoint, "DescribeExecution", `{"executionArn":"`+exec.ExecutionArn+`"}`)
		if err := json.Unmarshal([]byte(w.body), &d); err != nil {
			t.Fatalf("DescribeExecution = %d %s", w.status, w.body)
		}
		if d.Status != "RUNNING" && d.Status != "SUCCEEDED" {
			t.Fatalf("execution %s, output %s; want SUCCEEDED", d.Status, d.Output)
		}
		return d.Status == "SUCCEEDED"
	})
	if done := functionLog(t, r.endpoint, "done "); len(done) != 0 {
		t.Errorf("the function had finished (%q) before the execution succeeded; want the Task not to wait for it", done)
	}
	var out struct {
		StatusCode int
		Payload    json.RawMessage
	}
	if err := json.Unmarshal([]byte(d.Output), &out); err != nil || out.StatusCode != http.StatusAccepted || string(out.Payload) != "null" {
		t.Errorf("output = %s, want StatusCode 202 and Payload null", d.Output)
	}

	waitForRun(t, "finished function run", func() bool { return len(functionLog(t, r.endpoint, "done ")) > 0 })
	starts := functionLog(t, r.endpoint, "start ")
	if len(starts) != 1 || !strings.Contains(starts[0], `"marker":"m1"`) {
		t.Errorf("function runs = %q, want one with the Task's payload", starts)
	}
}

// functionLog is the messages of the slow function's log group that start
// with prefix; none while the group does not exist yet.
func functionLog(t *testing.T, endpoint, prefix string) []string {
	t.Helper()
	w := jsonCall(t, dummy, endpoint, "logs", "1.1", "Logs_20140328.FilterLogEvents", `{"logGroupName":"/aws/lambda/slow"}`)
	if w.status == http.StatusBadRequest && strings.Contains(w.body, "ResourceNotFoundException") {
		return nil
	}
	var got struct{ Events []struct{ Message string } }
	if err := json.Unmarshal([]byte(w.body), &got); w.status != http.StatusOK || err != nil {
		t.Fatalf("FilterLogEvents = %d %s", w.status, w.body)
	}
	var out []string
	for _, e := range got.Events {
		if strings.HasPrefix(e.Message, prefix) {
			out = append(out, e.Message)
		}
	}
	return out
}

// waitForRun checks cond every 100 milliseconds until it holds, failing the
// test after two minutes: waitFor's timeout is too short for a function's
// cold start on a loaded machine.
func waitForRun(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.After(2 * time.Minute)
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for !cond() {
		select {
		case <-deadline:
			t.Fatalf("no %s within two minutes", what)
		case <-tick.C:
		}
	}
}

// executableZip is a deployment package holding body as an executable
// bootstrap.
func executableZip(t *testing.T, body string) []byte {
	t.Helper()
	var pkg bytes.Buffer
	zw := zip.NewWriter(&pkg)
	fh := &zip.FileHeader{Name: "bootstrap", Method: zip.Deflate}
	fh.SetMode(0o755)
	f, err := zw.CreateHeader(fh)
	if err == nil {
		_, err = f.Write([]byte(body))
	}
	if err == nil {
		err = zw.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	return pkg.Bytes()
}
