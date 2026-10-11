package sqs_test

import (
	"encoding/base64"
	"encoding/json"
	"slices"
	"strconv"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/ref"
)

const arnPrefix = "arn:aws:sqs:us-east-1:000000000000:"

func redrivePolicy(dlq string, count int) string {
	return `{"deadLetterTargetArn":"` + arnPrefix + dlq + `","maxReceiveCount":` + strconv.Itoa(count) + `}`
}

// sqs.md 9.2: with maxReceiveCount N a message is delivered N times; the
// receive that would be the next one moves it to the dead-letter queue
// with its id, attributes and count, and DeadLetterQueueSourceArn. The
// receive is the cause of the move: its journal event names the
// dead-letter queue, and the moved message's enqueue event is the receive.
func TestRedriveMovesAfterMaxReceiveCount(t *testing.T) {
	run(t, func(h *harness) {
		dlq := h.queue("dlq", nil)
		src := h.queue("src", map[sqsapi.QueueAttributeName]string{"VisibilityTimeout": "0", "RedrivePolicy": redrivePolicy("dlq", 2)})
		x := "x"
		sent, err := h.send(&sqsapi.SendMessageInput{QueueUrl: src, MessageBody: "poison", MessageAttributes: map[string]sqsapi.MessageAttributeValue{"k": {DataType: "String", StringValue: &x}}})
		if err != nil {
			h.Fatal(err.Message)
		}
		for i := range 2 {
			if got := h.receiveBodies(src, 1); len(got) != 1 {
				h.Fatalf("receive %d = %q, want the message", i+1, got)
			}
		}
		if got := h.receiveBodies(src, 1); len(got) != 0 {
			h.Fatalf("the third receive = %q, want none: the message moved", got)
		}
		moved := h.mustReceive(&sqsapi.ReceiveMessageInput{QueueUrl: dlq, MessageSystemAttributeNames: []sqsapi.MessageSystemAttributeName{"All"}, MessageAttributeNames: []string{"All"}})
		if len(moved) != 1 {
			h.Fatalf("dead-letter queue holds %d messages, want 1", len(moved))
		}
		m := moved[0]
		if val(m.MessageId) != val(sent.MessageId) || m.Attributes["ApproximateReceiveCount"] != "3" || m.Attributes["DeadLetterQueueSourceArn"] != arnPrefix+"src" || val(m.MD5OfMessageAttributes) != "a45daa70926828a2f0a1db3418e6feb4" {
			h.Errorf("moved message = %+v", m)
		}
		sources, err := h.Invoke(h.Context(), sqsapi.OpListDeadLetterSourceQueues, &sqsapi.ListDeadLetterSourceQueuesInput{QueueUrl: dlq})
		if err != nil || !slices.Equal(sources.QueueUrls, []string{src}) {
			h.Errorf("ListDeadLetterSourceQueues = %+v, %v", sources, err)
		}

		page := h.Journal.Query(journal.Filter{ResourceGlob: arnPrefix + "dlq", Operation: ref.OperationID{Service: "sqs", Name: "ReceiveMessage"}})
		var receive ref.EventID
		for _, e := range page.Events {
			if call, ok := e.Detail.(journal.APICall); ok && slices.ContainsFunc(call.Resources, func(a aws.ARN) bool { return a.String() == arnPrefix+"src" }) {
				receive = e.ID
			}
		}
		if receive == 0 {
			h.Fatalf("no ReceiveMessage on src names the dead-letter queue: %+v", page.Events)
		}
		if got := enqueueEvents(h, "dlq"); !slices.Equal(got, []ref.EventID{receive}) {
			h.Errorf("dead-letter enqueue causes = %v, want the receive %d", got, receive)
		}
	})
}

// enqueueEvents reads the enqueue event of each message of queue name from
// a snapshot of the default scope.
func enqueueEvents(h *harness, name string) []ref.EventID {
	h.Helper()
	sections, err := h.svc.Export()
	if err != nil {
		h.Fatal(err)
	}
	var state struct {
		Queues map[string]struct {
			Messages []struct {
				Enqueued ref.EventID `json:"enqueuedEventId"`
			} `json:"messages"`
		} `json:"queues"`
	}
	if err := json.Unmarshal(sections[0].Data, &state); err != nil {
		h.Fatal(err)
	}
	out := make([]ref.EventID, 0, len(state.Queues[name].Messages))
	for _, m := range state.Queues[name].Messages {
		out = append(out, m.Enqueued)
	}
	return out
}

// sqs.md 9.2: a FIFO message moves with its id as its deduplication id, and
// the move opens its group for the same receive.
func TestFIFORedriveOpensTheGroup(t *testing.T) {
	run(t, func(h *harness) {
		dlq := h.fifoQueue("dlq.fifo", nil)
		src := h.fifoQueue("src.fifo", map[sqsapi.QueueAttributeName]string{
			"ContentBasedDeduplication": "true", "VisibilityTimeout": "0", "RedrivePolicy": redrivePolicy("dlq.fifo", 1),
		})
		h.sendTo(src, "g", "m1", nil)
		h.sendTo(src, "g", "m2", nil)
		if got := h.receiveBodies(src, 1); !slices.Equal(got, []string{"m1"}) {
			h.Fatalf("first receive = %q", got)
		}
		if got := h.receiveBodies(src, 1); !slices.Equal(got, []string{"m2"}) {
			h.Fatalf("second receive = %q, want m2 once m1 moved", got)
		}
		moved := h.mustReceive(&sqsapi.ReceiveMessageInput{QueueUrl: dlq, MessageSystemAttributeNames: []sqsapi.MessageSystemAttributeName{"MessageDeduplicationId", "MessageGroupId"}})
		if len(moved) != 1 || moved[0].Attributes["MessageDeduplicationId"] != val(moved[0].MessageId) || moved[0].Attributes["MessageGroupId"] != "g" {
			h.Fatalf("moved = %+v", moved)
		}
	})
}

// sqs.md 9.1: the target must exist, be of the source's type and admit the
// source through its RedriveAllowPolicy.
func TestRedrivePolicyTargets(t *testing.T) {
	run(t, func(h *harness) {
		h.queue("closed", map[sqsapi.QueueAttributeName]string{"RedriveAllowPolicy": `{"redrivePermission":"denyAll"}`})
		h.queue("chosen", map[sqsapi.QueueAttributeName]string{"RedriveAllowPolicy": `{"redrivePermission":"byQueue","sourceQueueArns":["` + arnPrefix + `picked"]}`})
		h.fifoQueue("other.fifo", nil)
		for _, tt := range []struct{ source, target, reason string }{
			{"denied", "closed", "Queue " + arnPrefix + "closed does not allow " + arnPrefix + "denied to use it as a dead-letter queue."},
			{"unlisted", "chosen", "Queue " + arnPrefix + "chosen does not allow " + arnPrefix + "unlisted to use it as a dead-letter queue."},
			{"mixed", "other.fifo", "Dead-letter queue must be the same type of queue as the source queue."},
		} {
			policy := redrivePolicy(tt.target, 3)
			_, err := h.create(&sqsapi.CreateQueueInput{QueueName: tt.source, Attributes: map[sqsapi.QueueAttributeName]string{"RedrivePolicy": policy}})
			wantError(h.T, err, "InvalidParameterValue", "Value "+policy+" for parameter RedrivePolicy is invalid. Reason: "+tt.reason)
		}
		h.queue("picked", map[sqsapi.QueueAttributeName]string{"RedrivePolicy": redrivePolicy("chosen", 3)})
	})
}

// deadLetterMessages fills dlq with n messages dead-lettered from src.
func (h *harness) deadLetterMessages(src string, n int) {
	h.Helper()
	for i := range n {
		h.mustSend(src, "m"+strconv.Itoa(i))
	}
	if got := h.receiveBodies(src, 10); len(got) != n {
		h.Fatalf("first receive = %q, want all %d", got, n)
	}
	if got := h.receiveBodies(src, 10); len(got) != 0 {
		h.Fatalf("second receive = %q, want none: every message moved", got)
	}
}

func (h *harness) startTask(in *sqsapi.StartMessageMoveTaskInput) (string, *awserr.Error) {
	out, err := h.Invoke(h.Context(), sqsapi.OpStartMessageMoveTask, in)
	if err != nil {
		return "", err
	}
	synctest.Wait()
	return val(out.TaskHandle), nil
}

func (h *harness) tasks(source string, limit int32) []sqsapi.ListMessageMoveTasksResultEntry {
	h.Helper()
	out, err := h.Invoke(h.Context(), sqsapi.OpListMessageMoveTasks, &sqsapi.ListMessageMoveTasksInput{SourceArn: arnPrefix + source, MaxResults: &limit})
	if err != nil {
		h.Fatalf("ListMessageMoveTasks = %s: %s", err.ID.QueryCode, err.Message)
	}
	return out.Results
}

func (h *harness) count(url string) string {
	h.Helper()
	out, err := h.Invoke(h.Context(), sqsapi.OpGetQueueAttributes, &sqsapi.GetQueueAttributesInput{QueueUrl: url, AttributeNames: []sqsapi.QueueAttributeName{"ApproximateNumberOfMessages"}})
	if err != nil {
		h.Fatal(err.Message)
	}
	return out.Attributes["ApproximateNumberOfMessages"]
}

// sqs.md 9.3: a task moves MaxNumberOfMessagesPerSecond messages a second
// of emulator time back to their source, as new messages, then completes.
func TestMoveTaskMovesAtItsRateUnderAdvance(t *testing.T) {
	run(t, func(h *harness) {
		dlq := h.queue("dlq", nil)
		src := h.queue("src", map[sqsapi.QueueAttributeName]string{"VisibilityTimeout": "0", "RedrivePolicy": redrivePolicy("dlq", 1)})
		h.deadLetterMessages(src, 5)
		if n := h.count(dlq); n != "5" {
			h.Fatalf("dead-letter queue holds %s messages, want 5", n)
		}
		handle, err := h.startTask(&sqsapi.StartMessageMoveTaskInput{SourceArn: arnPrefix + "dlq", MaxNumberOfMessagesPerSecond: new(int32(2))})
		if err != nil {
			h.Fatal(err.Message)
		}
		for step, want := range []int64{2, 4, 5} {
			if step > 0 {
				h.advance(time.Second)
			}
			task := h.tasks("dlq", 1)[0]
			if task.ApproximateNumberOfMessagesMoved != want || *task.ApproximateNumberOfMessagesToMove != 5 {
				h.Fatalf("after %ds moved %d of %d, want %d", step, task.ApproximateNumberOfMessagesMoved, *task.ApproximateNumberOfMessagesToMove, want)
			}
		}
		done := h.tasks("dlq", 1)[0]
		if val(done.Status) != "COMPLETED" || done.TaskHandle != nil || *done.MaxNumberOfMessagesPerSecond != 2 || done.DestinationArn != nil {
			h.Errorf("finished task = %+v", done)
		}
		if h.count(src) != "5" || h.count(dlq) != "0" {
			h.Errorf("source holds %s and the dead-letter queue %s, want 5 and 0", h.count(src), h.count(dlq))
		}
		if _, err := h.Invoke(h.Context(), sqsapi.OpCancelMessageMoveTask, &sqsapi.CancelMessageMoveTaskInput{TaskHandle: handle}); err == nil || err.ID.QueryCode != "ResourceNotFoundException" {
			h.Errorf("cancelling a finished task = %v, want ResourceNotFoundException", err)
		}
	})
}

// sqs.md 9.3: cancelling keeps what moved; deleting the destination fails
// the task with NonExistentQueue.
func TestMoveTaskCancelAndFailure(t *testing.T) {
	run(t, func(h *harness) {
		h.queue("dlq", nil)
		src := h.queue("src", map[sqsapi.QueueAttributeName]string{"VisibilityTimeout": "0", "RedrivePolicy": redrivePolicy("dlq", 1)})
		dest := h.queue("dest", nil)
		h.deadLetterMessages(src, 3)
		handle, err := h.startTask(&sqsapi.StartMessageMoveTaskInput{SourceArn: arnPrefix + "dlq", MaxNumberOfMessagesPerSecond: new(int32(1))})
		if err != nil {
			h.Fatal(err.Message)
		}
		_, err = h.startTask(&sqsapi.StartMessageMoveTaskInput{SourceArn: arnPrefix + "dlq"})
		wantError(h.T, err, "InvalidParameterValue", "There is already a task running. Only one active task is allowed for a source queue arn at a given time.")
		out, err := h.Invoke(h.Context(), sqsapi.OpCancelMessageMoveTask, &sqsapi.CancelMessageMoveTaskInput{TaskHandle: handle})
		if err != nil || out.ApproximateNumberOfMessagesMoved != 1 {
			h.Fatalf("CancelMessageMoveTask = %+v, %v; want 1 moved", out, err)
		}
		h.advance(5 * time.Second)
		if task := h.tasks("dlq", 1)[0]; val(task.Status) != "CANCELLED" || task.ApproximateNumberOfMessagesMoved != 1 {
			h.Fatalf("cancelled task = %+v", task)
		}
		if _, err := h.startTask(&sqsapi.StartMessageMoveTaskInput{SourceArn: arnPrefix + "dlq", DestinationArn: new(arnPrefix + "dest"), MaxNumberOfMessagesPerSecond: new(int32(1))}); err != nil {
			h.Fatal(err.Message)
		}
		if _, err := h.Invoke(h.Context(), sqsapi.OpDeleteQueue, &sqsapi.DeleteQueueInput{QueueUrl: dest}); err != nil {
			h.Fatal(err.Message)
		}
		listed := h.tasks("dlq", 10)
		if len(listed) != 2 || val(listed[0].Status) != "FAILED" || val(listed[0].FailureReason) != "AWS.SimpleQueueService.NonExistentQueue" || val(listed[1].Status) != "CANCELLED" {
			h.Fatalf("tasks, newest first = %+v", listed)
		}
	})
}

// sqs.md 2.4: the move task errors AWS recorded.
func TestMoveTaskErrors(t *testing.T) {
	run(t, func(h *harness) {
		h.queue("plain", nil)
		h.queue("dlq", nil)
		h.queue("src", map[sqsapi.QueueAttributeName]string{"RedrivePolicy": redrivePolicy("dlq", 1)})
		_, err := h.startTask(&sqsapi.StartMessageMoveTaskInput{SourceArn: arnPrefix + "plain"})
		wantError(h.T, err, "InvalidParameterValue", "Source queue must be configured as a Dead Letter Queue.")
		if listed := h.tasks("plain", 10); len(listed) != 0 {
			h.Errorf("tasks of a queue that never had one = %+v", listed)
		}
		_, err = h.startTask(&sqsapi.StartMessageMoveTaskInput{SourceArn: arnPrefix + "dlq", DestinationArn: new(arnPrefix + "missing")})
		wantError(h.T, err, "ResourceNotFoundException", "The resource that you specified for the DestinationArn parameter doesn't exist.")
		if err.ID.Status != 404 {
			h.Errorf("status = %d, want 404", err.ID.Status)
		}
		_, err = h.startTask(&sqsapi.StartMessageMoveTaskInput{SourceArn: arnPrefix + "dlq", DestinationArn: new(arnPrefix + "dlq")})
		wantError(h.T, err, "InvalidParameterValue", "The destination queue must be different from the source queue.")
		_, err = h.startTask(&sqsapi.StartMessageMoveTaskInput{SourceArn: "arn:aws:sqs:eu-west-1:000000000000:dlq"})
		wantError(h.T, err, "ResourceNotFoundException", "The resource that you specified for the SourceArn parameter doesn't exist.")
		_, err = h.startTask(&sqsapi.StartMessageMoveTaskInput{SourceArn: arnPrefix + "dlq", MaxNumberOfMessagesPerSecond: new(int32(501))})
		wantError(h.T, err, "InvalidParameterValue", "Value 501 for parameter MaxNumberOfMessagesPerSecond is invalid. Reason: Must be between 1 and 500, if provided.")
		cancel := func(handle string) *awserr.Error {
			_, err := h.Invoke(h.Context(), sqsapi.OpCancelMessageMoveTask, &sqsapi.CancelMessageMoveTaskInput{TaskHandle: handle})
			return err
		}
		wantError(h.T, cancel("garbage"), "InvalidParameterValue", "Value for parameter TaskHandle is invalid.")
		handle := func(source string) string {
			return base64.StdEncoding.EncodeToString([]byte(`{"taskId":"00000000-0000-4000-8000-000000000000","sourceArn":"` + arnPrefix + source + `"}`))
		}
		wantError(h.T, cancel(handle("dlq")), "ResourceNotFoundException", "Task does not exist.")
		wantError(h.T, cancel(handle("missing")), "ResourceNotFoundException", "The resource that you specified for the SourceArn parameter doesn't exist.")
		// A dead-letter queue with nothing to move completes at once.
		if _, err := h.startTask(&sqsapi.StartMessageMoveTaskInput{SourceArn: arnPrefix + "dlq"}); err != nil {
			h.Fatal(err.Message)
		}
		if task := h.tasks("dlq", 1)[0]; val(task.Status) != "COMPLETED" || task.ApproximateNumberOfMessagesMoved != 0 {
			h.Errorf("empty task = %+v", task)
		}
	})
}
