package lambda_test

import (
	"encoding/base64"
	"regexp"
	"slices"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/sustinbebustin/understudy/internal/api/lambdaapi"
	"github.com/sustinbebustin/understudy/internal/service/lambda/pool/pooltest"
	"github.com/sustinbebustin/understudy/internal/service/lambda/runtimeapi"
)

// execRole is the session function logs are written as.
const execRole = "arn:aws:sts::000000000000:assumed-role/lambda-role/understudy"

var streamPattern = regexp.MustCompile(`^/aws/lambda/fn \d{4}/\d{2}/\d{2}/\[\$LATEST\][0-9a-f]{32}$`)

func TestFunctionLogsReachCloudWatchLogsAsTheExecutionRole(t *testing.T) {
	s := newScript()
	runExec(t, s.handle, func(h *execHarness) {
		h.createFunction("fn")
		s.setDuring(func(runtimeapi.Invocation) { h.output.Print("hello from fn") })
		h.invoke(invokeInput("fn", `{}`))
		h.settle()
		id := string(s.invoked()[0].RequestID)
		got := h.logs.messages()
		if len(got) != 5 {
			t.Fatalf("messages = %q, want INIT_START, START, the output, END and REPORT", got)
		}
		checks := []struct {
			line string
			ok   bool
		}{
			{got[0], strings.HasPrefix(got[0], "INIT_START Runtime Version: provided:al2023\tRuntime Version ARN: arn:aws:lambda:us-east-1::runtime:") && strings.HasSuffix(got[0], "\n")},
			{got[1], got[1] == "START RequestId: "+id+" Version: $LATEST\n"},
			{got[2], got[2] == "hello from fn\n"},
			{got[3], got[3] == "END RequestId: "+id+"\n"},
			{got[4], strings.HasPrefix(got[4], "REPORT RequestId: "+id+"\tDuration: ") && strings.Contains(got[4], "\tMemory Size: 128 MB\tMax Memory Used: 36 MB\tInit Duration: ") && strings.HasSuffix(got[4], "\t\n")},
		}
		for i, c := range checks {
			if !c.ok {
				t.Errorf("message %d = %q", i, c.line)
			}
		}
		h.logs.mu.Lock()
		groups, streams, callers := slices.Clone(h.logs.groups), slices.Clone(h.logs.streams), slices.Clone(h.logs.callers)
		h.logs.mu.Unlock()
		if !slices.Equal(groups, []string{"/aws/lambda/fn"}) || len(streams) != 1 || !streamPattern.MatchString(streams[0]) {
			t.Errorf("created groups %q and streams %q, want /aws/lambda/fn and one YYYY/MM/DD/[$LATEST]<id> stream", groups, streams)
		}
		for _, c := range callers {
			if c != execRole {
				t.Errorf("a logs call ran as %s, want the execution role %s", c, execRole)
			}
		}

		// A warm invocation writes no INIT_START and creates nothing.
		h.invoke(invokeInput("fn", `{}`))
		h.settle()
		warm := h.logs.messages()[5:]
		if len(warm) != 4 || !strings.HasPrefix(warm[0], "START ") || strings.Contains(warm[3], "Init Duration") {
			t.Errorf("warm messages = %q, want START, output, END and a REPORT without Init Duration", warm)
		}
		h.logs.mu.Lock()
		defer h.logs.mu.Unlock()
		if len(h.logs.groups) != 1 || len(h.logs.streams) != 1 {
			t.Errorf("a warm invocation created groups %q and streams %q again", h.logs.groups, h.logs.streams)
		}
	})
}

func TestLogTypeTailReturnsTheInvocationsLogFromStartToReport(t *testing.T) {
	s := newScript()
	runExec(t, s.handle, func(h *execHarness) {
		h.createFunction("fn")
		s.setDuring(func(runtimeapi.Invocation) { h.output.Print("{}") })
		out := h.invoke(&lambdaapi.InvokeInput{FunctionName: "fn", LogType: new(lambdaapi.LogTypeTail)})
		if out.LogResult == nil {
			t.Fatal("LogResult is missing with LogType Tail")
		}
		log, err := base64.StdEncoding.DecodeString(*out.LogResult)
		if err != nil {
			t.Fatal(err)
		}
		id := string(s.invoked()[0].RequestID)
		prefix := "START RequestId: " + id + " Version: $LATEST\n{}\nEND RequestId: " + id + "\nREPORT RequestId: " + id + "\tDuration: "
		if !strings.HasPrefix(string(log), prefix) || !strings.HasSuffix(string(log), "\t\n") {
			t.Errorf("tail = %q, want the recorded shape %q...", log, prefix)
		}
		if plain := h.invoke(invokeInput("fn", `{}`)); plain.LogResult != nil {
			t.Error("LogResult is set without LogType Tail")
		}

		// A long log keeps its last 4 KB, REPORT included.
		s.setDuring(func(runtimeapi.Invocation) {
			for range 100 {
				h.output.Print(strings.Repeat("x", 99))
			}
		})
		out = h.invoke(&lambdaapi.InvokeInput{FunctionName: "fn", LogType: new(lambdaapi.LogTypeTail)})
		log, err = base64.StdEncoding.DecodeString(*out.LogResult)
		if err != nil {
			t.Fatal(err)
		}
		if len(log) != 4096 || !strings.Contains(string(log), "\nREPORT RequestId: ") {
			t.Errorf("tail of a long log is %d bytes ending %q, want the last 4096 with REPORT", len(log), log[max(0, len(log)-80):])
		}
	})
}

// Output printed after an invocation ended is not lost: it is delivered
// once the container stops, or on its own once it fills a PutLogEvents
// request.
func TestOutputBetweenInvocationsIsDelivered(t *testing.T) {
	runExec(t, pooltest.Echo, func(h *execHarness) {
		h.createFunction("fn")
		h.invoke(invokeInput("fn", `{}`))
		h.settle()
		delivered := len(h.logs.messages())

		big := strings.Repeat("x", 100_000)
		for range 11 {
			h.output.Print(big)
		}
		synctest.Wait()
		h.settle()
		if got := h.logs.messages()[delivered:]; len(got) != 11 {
			t.Fatalf("delivered %d lines after 1.1 MB of output between invocations, want 11", len(got))
		}

		h.output.Print("background work")
		synctest.Wait()
		h.output.End()
		synctest.Wait()
		h.settle()
		if got := h.logs.messages(); got[len(got)-1] != "background work\n" {
			t.Errorf("last message = %q, want the output printed before the container stopped", got[len(got)-1])
		}
	})
}

func TestReportOfATimeoutSaysSo(t *testing.T) {
	s := newScript()
	runExec(t, s.handle, func(h *execHarness) {
		h.createFunction("fn")
		h.invoke(invokeInput("fn", `{"action":"timeout"}`))
		h.settle()
		got := h.logs.messages()
		report := got[len(got)-1]
		if !strings.Contains(report, "\tDuration: 3000.00 ms\tBilled Duration: 3000 ms\t") || !strings.HasSuffix(report, "\tStatus: timeout\t\n") {
			t.Errorf("REPORT = %q, want the timeout as its duration and Status: timeout", report)
		}
	})
}
