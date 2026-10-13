package logs_test

import (
	"slices"
	"testing"

	"github.com/sustinbebustin/understudy/internal/access"
	"github.com/sustinbebustin/understudy/internal/api/logsapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/policydoc"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// Every bound Logs operation whose generated list holds more than one logs
// action decides which of them its calls use, so none records the whole
// list.
func TestEveryMultiActionOperationDeclaresItsActions(t *testing.T) {
	run(t, func(h *harness) {
		if got := kerneltest.UndeclaredActions(h.Kernel, logsapi.Service()); len(got) != 0 {
			t.Errorf("operations without an actions decision: %v; declare them in declareActions", got)
		}
	})
}

// Reading events unmasked needs logs:Unmask beside the read action; a
// plain read records the read action alone.
func TestUnmaskedReadsRecordUnmask(t *testing.T) {
	run(t, func(h *harness) {
		h.group("g")
		h.stream("g", "s")
		h.Access.Reset()
		root := access.Root{Account: h.Scope.Account}
		actions := func() []string {
			uses := h.Access.Usage(access.Filter{Principal: root, Service: "logs"})
			out := make([]string, 0, len(uses))
			for i := range uses {
				if a := uses[i].Action.String(); !slices.Contains(out, a) {
					out = append(out, a)
				}
			}
			return out
		}
		must(h, logsapi.OpGetLogEvents, &logsapi.GetLogEventsInput{LogGroupName: new("g"), LogStreamName: "s"})
		must(h, logsapi.OpFilterLogEvents, &logsapi.FilterLogEventsInput{LogGroupName: new("g")})
		if got, want := actions(), []string{"logs:FilterLogEvents", "logs:GetLogEvents"}; !slices.Equal(got, want) {
			t.Errorf("plain reads recorded %v, want %v", got, want)
		}
		h.Access.Reset()
		must(h, logsapi.OpGetLogEvents, &logsapi.GetLogEventsInput{LogGroupName: new("g"), LogStreamName: "s", Unmask: true})
		must(h, logsapi.OpFilterLogEvents, &logsapi.FilterLogEventsInput{LogGroupName: new("g"), Unmask: true})
		if got, want := actions(), []string{"logs:FilterLogEvents", "logs:GetLogEvents", "logs:Unmask"}; !slices.Equal(got, want) {
			t.Errorf("unmasked reads recorded %v, want %v", got, want)
		}
	})
}

// Lambda writes a function's logs as its execution role: it creates the
// group and stream and puts the events through PutLogEventsInternal, which
// is recorded as logs:PutLogEvents. The role's policy then carries the
// three actions of AWSLambdaBasicExecutionRole, on the group and on the
// stream the call touched.
func TestFunctionLogDeliveryRecordsPutLogEventsForTheExecutionRole(t *testing.T) {
	run(t, func(h *harness) {
		role, err := aws.ParseARN("arn:aws:iam::000000000000:role/worker")
		if err != nil {
			t.Fatal(err)
		}
		const group, stream = "/aws/lambda/fn", "2000/01/01/[$LATEST]abc"
		parent := h.Call(logsapi.OpCreateLogGroup.Descriptor())
		target := kernel.Target{Scope: h.Scope, As: kernel.AsExecutionRole{Role: role}, Via: ref.LambdaLogs}
		if _, err := kernel.Invoke(h.Context(), h.Kernel, parent, logsapi.OpCreateLogGroup, target, &logsapi.CreateLogGroupInput{LogGroupName: group}); err != nil {
			t.Fatalf("CreateLogGroup = %s", err.Message)
		}
		if _, err := kernel.Invoke(h.Context(), h.Kernel, parent, logsapi.OpCreateLogStream, target, &logsapi.CreateLogStreamInput{LogGroupName: group, LogStreamName: stream}); err != nil {
			t.Fatalf("CreateLogStream = %s", err.Message)
		}
		if _, err := kernel.Invoke(h.Context(), h.Kernel, parent, logsapi.OpPutLogEventsInternal, target,
			&logsapi.PutLogEventsInternalInput{LogGroupName: group, LogStreamName: stream, LogEvents: []logsapi.InputLogEvent{event(h.now(), "START")}}); err != nil {
			t.Fatalf("PutLogEventsInternal = %s", err.Message)
		}

		doc, ok := h.Access.Policy(access.Role{Account: h.Scope.Account, Name: "worker"})
		if !ok {
			t.Fatal("no policy recorded for the execution role")
		}
		groupARN := "arn:aws:logs:us-east-1:000000000000:log-group:" + group
		want := `{"Version":"2012-10-17","Statement":[` +
			`{"Effect":"Allow","Action":"logs:CreateLogGroup","Resource":"` + groupARN + `"},` +
			`{"Effect":"Allow","Action":["logs:CreateLogStream","logs:PutLogEvents"],"Resource":"` + groupARN + `:log-stream:` + stream + `"}]}`
		if got := string(policydoc.Encode(doc)); got != want {
			t.Errorf("Policy =\n%s\nwant\n%s", got, want)
		}
	})
}
